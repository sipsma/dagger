package dagql

import (
	"context"
	"fmt"
	"slices"

	"github.com/dagger/dagger/dagql/call"
	"github.com/dagger/dagger/engine/slog"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/opencontainers/go-digest"
)

//nolint:gocyclo // intrinsically long state machine; refactoring would hurt clarity
func (c *Cache) importPersistedState(ctx context.Context) error {
	if c.pdb == nil {
		return nil
	}
	importRunID := c.nextImportRunID()

	resultRows, err := c.pdb.ListMirrorResults(ctx)
	if err != nil {
		return fmt.Errorf("list mirror results: %w", err)
	}
	eqClassRows, err := c.pdb.ListMirrorEqClasses(ctx)
	if err != nil {
		return fmt.Errorf("list mirror eq_classes: %w", err)
	}
	eqClassDigestRows, err := c.pdb.ListMirrorEqClassDigests(ctx)
	if err != nil {
		return fmt.Errorf("list mirror eq_class_digests: %w", err)
	}
	termRows, err := c.pdb.ListMirrorTerms(ctx)
	if err != nil {
		return fmt.Errorf("list mirror terms: %w", err)
	}
	termInputRows, err := c.pdb.ListMirrorTermInputs(ctx)
	if err != nil {
		return fmt.Errorf("list mirror term_inputs: %w", err)
	}
	resultOutputEqClassRows, err := c.pdb.ListMirrorResultOutputEqClasses(ctx)
	if err != nil {
		return fmt.Errorf("list mirror result_output_eq_classes: %w", err)
	}
	persistedEdgeRows, err := c.pdb.ListMirrorPersistedEdges(ctx)
	if err != nil {
		return fmt.Errorf("list mirror persisted_edges: %w", err)
	}
	resultDepRows, err := c.pdb.ListMirrorResultDeps(ctx)
	if err != nil {
		return fmt.Errorf("list mirror result_deps: %w", err)
	}
	resultSnapshotRows, err := c.pdb.ListMirrorResultSnapshotLinks(ctx)
	if err != nil {
		return fmt.Errorf("list mirror result_snapshot_links: %w", err)
	}
	snapshotContentRows, err := c.pdb.ListMirrorSnapshotContentLinks(ctx)
	if err != nil {
		return fmt.Errorf("list mirror snapshot_content_links: %w", err)
	}
	importedLayerBlobRows, err := c.pdb.ListMirrorImportedLayerBlobIndex(ctx)
	if err != nil {
		return fmt.Errorf("list mirror imported_layer_blob_index: %w", err)
	}
	importedLayerDiffRows, err := c.pdb.ListMirrorImportedLayerDiffIndex(ctx)
	if err != nil {
		return fmt.Errorf("list mirror imported_layer_diff_index: %w", err)
	}

	if len(resultRows) == 0 && len(eqClassRows) == 0 && len(termRows) == 0 {
		return nil
	}

	// Snapshot metadata hydrates before vetting: attaching an owner lease —
	// vetting's snapshot-presence check — needs the content-digest rows.
	if c.snapshotManager != nil {
		rows := bkcache.PersistentMetadataRows{
			SnapshotContent: make([]bkcache.SnapshotContentRow, 0, len(snapshotContentRows)),
			ImportedByBlob:  make([]bkcache.ImportedLayerBlobRow, 0, len(importedLayerBlobRows)),
			ImportedByDiff:  make([]bkcache.ImportedLayerDiffRow, 0, len(importedLayerDiffRows)),
		}
		for _, row := range snapshotContentRows {
			rows.SnapshotContent = append(rows.SnapshotContent, bkcache.SnapshotContentRow{
				SnapshotID: row.SnapshotID,
				Digest:     normalizeImportedDigest(row.Digest),
			})
		}
		for _, row := range importedLayerBlobRows {
			rows.ImportedByBlob = append(rows.ImportedByBlob, bkcache.ImportedLayerBlobRow{
				ParentSnapshotID: row.ParentSnapshotID,
				BlobDigest:       normalizeImportedDigest(row.BlobDigest),
				SnapshotID:       row.SnapshotID,
			})
		}
		for _, row := range importedLayerDiffRows {
			rows.ImportedByDiff = append(rows.ImportedByDiff, bkcache.ImportedLayerDiffRow{
				ParentSnapshotID: row.ParentSnapshotID,
				DiffID:           normalizeImportedDigest(row.DiffID),
				SnapshotID:       row.SnapshotID,
			})
		}
		if err := c.snapshotManager.LoadPersistentMetadata(rows); err != nil {
			return fmt.Errorf("hydrate snapshot metadata: %w", err)
		}
	}

	keptRows, restoreSummary, err := c.vetRestoredResults(ctx, resultRows, resultDepRows, resultSnapshotRows)
	if err != nil {
		return err
	}

	var eagerDecodeResultIDs []sharedResultID

	c.egraphMu.Lock()
	importErr := func() error {
		c.initEgraphLocked()

		var maxEqClassID eqClassID
		for _, row := range eqClassRows {
			eqID := eqClassID(row.ID)
			if eqID == 0 {
				return fmt.Errorf("import eq_class: zero ID")
			}
			if eqID > maxEqClassID {
				maxEqClassID = eqID
			}
		}
		c.egraphParents = make([]eqClassID, maxEqClassID+1)
		c.egraphRanks = make([]uint8, maxEqClassID+1)
		for _, row := range eqClassRows {
			eqID := eqClassID(row.ID)
			c.egraphParents[eqID] = eqID
			if c.eqClassToDigests[eqID] == nil {
				c.eqClassToDigests[eqID] = make(map[string]struct{})
			}
		}

		for _, row := range eqClassDigestRows {
			eqID := eqClassID(row.EqClassID)
			if eqID == 0 {
				return fmt.Errorf("import eq_class_digest %q: zero eq_class_id", row.Digest)
			}
			if int(eqID) >= len(c.egraphParents) || c.egraphParents[eqID] == 0 {
				return fmt.Errorf("import eq_class_digest %q: missing eq_class %d", row.Digest, eqID)
			}
			if row.Digest == "" {
				return fmt.Errorf("import eq_class_digest: empty digest for eq_class %d", eqID)
			}
			c.egraphDigestToClass[row.Digest] = eqID
			digests := c.eqClassToDigests[eqID]
			if digests == nil {
				digests = make(map[string]struct{})
				c.eqClassToDigests[eqID] = digests
			}
			digests[row.Digest] = struct{}{}
			if row.Label != "" {
				extras := c.eqClassExtraDigests[eqID]
				if extras == nil {
					extras = make(map[call.ExtraDigest]struct{})
					c.eqClassExtraDigests[eqID] = extras
				}
				extras[call.ExtraDigest{
					Digest: digest.Digest(row.Digest),
					Label:  row.Label,
				}] = struct{}{}
			}
		}

		var maxResultID sharedResultID
		for _, row := range resultRows {
			resultID := sharedResultID(row.ID)
			if resultID > maxResultID {
				maxResultID = resultID
			}
			restored, wasKept := keptRows[resultID]
			if !wasKept {
				continue
			}
			env := restored.env

			res := &sharedResult{
				id:                    resultID,
				isObject:              env.Kind == persistedResultKindObject,
				sessionResourceHandle: env.SessionResourceHandle,
				expiresAtUnix:         row.ExpiresAtUnix,
				createdAtUnixNano:     row.CreatedAtUnixNano,
				lastUsedAtUnixNano:    row.LastUsedAtUnixNano,
				description:           row.Description,
				recordType:            row.RecordType,
				materialization:       materializationState{envelope: &restored.env},
				restored:              true,
			}
			if len(restored.links) > 0 {
				res.materialization.setLocalSnapshotSource(restored.links)
			}
			if len(env.LazyJSON) > 0 {
				res.materialization.setLazyFragment(&PersistedLazyFragment{
					Kind: env.LazyKind,
					JSON: env.LazyJSON,
				})
			}
			res.storeResultCall(restored.frame)
			c.traceResultCallFrameUpdated(ctx, res, "import_persisted_result", nil, restored.frame)

			if env.Kind == persistedResultKindNull {
				res.materialization.realized = true
				res.materialization.envelope = nil
				c.tracePersistedPayloadImportedEager(ctx, importRunID, resultID, "", "nil")
			} else {
				eagerDecodeResultIDs = append(eagerDecodeResultIDs, resultID)
			}
			c.resultsByID[resultID] = res
			c.traceImportResultLoaded(ctx, importRunID, resultID, row.CallFrameJSON)
		}

		for _, row := range persistedEdgeRows {
			resultID := sharedResultID(row.ResultID)
			if resultID == 0 {
				return fmt.Errorf("import persisted_edge: zero result ID")
			}
			res := c.resultsByID[resultID]
			if res == nil {
				// The edge's result was dropped at vetting (or never
				// existed); a retention root without a row retains nothing.
				continue
			}
			if c.persistedEdgesByResult == nil {
				c.persistedEdgesByResult = make(map[sharedResultID]persistedEdge)
			}
			edge := persistedEdge{
				resultID:          resultID,
				createdAtUnixNano: row.CreatedAtUnixNano,
				expiresAtUnix:     row.ExpiresAtUnix,
				unpruneable:       row.Unpruneable,
			}
			if edge.unpruneable {
				edge.expiresAtUnix = 0
				res.expiresAtUnix = 0
			}
			c.persistedEdgesByResult[resultID] = edge
			c.incrementIncomingOwnershipLocked(ctx, res)
		}

		// Identity-table references must resolve exactly: a broken reference
		// silently collapsing to the zero class would change key derivation,
		// so it is store-level corruption and wipes.
		eqClassExists := func(id eqClassID) bool {
			return id != 0 && int(id) < len(c.egraphParents) && c.egraphParents[id] != 0
		}

		type importTermInput struct {
			position       int
			inputEqClassID eqClassID
			provenanceKind egraphInputProvenanceKind
		}
		inputsByTermID := make(map[egraphTermID][]importTermInput, len(termRows))
		for _, row := range termInputRows {
			termID := egraphTermID(row.TermID)
			if termID == 0 {
				return fmt.Errorf("import term_input: zero term_id")
			}
			provenance := egraphInputProvenanceKind(row.ProvenanceKind)
			switch provenance {
			case egraphInputProvenanceKindResult, egraphInputProvenanceKindDigest:
			default:
				return fmt.Errorf("import term_input %d/%d: unsupported provenance %q", row.TermID, row.Position, row.ProvenanceKind)
			}
			inputEqID := eqClassID(row.InputEqClassID)
			if inputEqID != 0 && !eqClassExists(inputEqID) {
				return fmt.Errorf("import term_input %d/%d: missing eq_class %d", row.TermID, row.Position, row.InputEqClassID)
			}
			inputsByTermID[termID] = append(inputsByTermID[termID], importTermInput{
				position:       int(row.Position),
				inputEqClassID: inputEqID,
				provenanceKind: provenance,
			})
		}

		var maxTermID egraphTermID
		for _, row := range termRows {
			termID := egraphTermID(row.ID)
			if termID == 0 {
				return fmt.Errorf("import term: zero ID")
			}
			if termID > maxTermID {
				maxTermID = termID
			}

			inputs := inputsByTermID[termID]
			slices.SortFunc(inputs, func(a, b importTermInput) int {
				switch {
				case a.position < b.position:
					return -1
				case a.position > b.position:
					return 1
				default:
					return 0
				}
			})
			inputEqIDs := make([]eqClassID, 0, len(inputs))
			inputProvenance := make([]egraphInputProvenanceKind, 0, len(inputs))
			for idx, input := range inputs {
				if input.position != idx {
					return fmt.Errorf("import term %d inputs: missing position %d", termID, idx)
				}
				inputEqIDs = append(inputEqIDs, c.findEqClassLocked(input.inputEqClassID))
				inputProvenance = append(inputProvenance, input.provenanceKind)
			}

			selfDigest := normalizeImportedDigest(row.SelfDigest)
			if row.OutputEqClassID != 0 && !eqClassExists(eqClassID(row.OutputEqClassID)) {
				return fmt.Errorf("import term %d: missing output eq_class %d", termID, row.OutputEqClassID)
			}
			outputEqID := c.findEqClassLocked(eqClassID(row.OutputEqClassID))

			// Term lookup keys are process-local: newEgraphTerm derives this
			// boot's term digest from the persisted self digest and the input
			// classes as numbered by this process. Nothing persisted carries a
			// term key.
			term := newEgraphTerm(termID, selfDigest, inputEqIDs, outputEqID)
			c.egraphTerms[termID] = term
			c.termInputProvenance[termID] = inputProvenance
			digestTerms := c.egraphTermsByTermDigest[term.termDigest]
			if digestTerms == nil {
				digestTerms = newEgraphTermIDSet()
				c.egraphTermsByTermDigest[term.termDigest] = digestTerms
			}
			digestTerms.Insert(termID)
			for _, inputEqID := range inputEqIDs {
				if inputEqID == 0 {
					continue
				}
				classTerms := c.inputEqClassToTerms[inputEqID]
				if classTerms == nil {
					classTerms = make(map[egraphTermID]struct{})
					c.inputEqClassToTerms[inputEqID] = classTerms
				}
				classTerms[termID] = struct{}{}
			}
			outputTerms := c.outputEqClassToTerms[outputEqID]
			if outputTerms == nil {
				outputTerms = make(map[egraphTermID]struct{})
				c.outputEqClassToTerms[outputEqID] = outputTerms
			}
			outputTerms[termID] = struct{}{}
			c.traceTermCreated(ctx, "import", importRunID, term)
		}
		for termID := range inputsByTermID {
			if _, termLoaded := c.egraphTerms[termID]; !termLoaded {
				return fmt.Errorf("import term_input: missing term %d", termID)
			}
		}

		for _, row := range resultOutputEqClassRows {
			resultID := sharedResultID(row.ResultID)
			res := c.resultsByID[resultID]
			if res == nil {
				continue
			}
			outputEqID := c.findEqClassLocked(eqClassID(row.EqClassID))
			if outputEqID == 0 {
				return fmt.Errorf("import result_output_eq_class: missing eq_class %d", row.EqClassID)
			}
			outputEqClasses := c.resultOutputEqClasses[resultID]
			if outputEqClasses == nil {
				outputEqClasses = make(map[eqClassID]struct{})
				c.resultOutputEqClasses[resultID] = outputEqClasses
			}
			outputEqClasses[outputEqID] = struct{}{}
		}

		for _, row := range resultDepRows {
			parentID := sharedResultID(row.ParentResultID)
			parent := c.resultsByID[parentID]
			if parent == nil {
				continue
			}
			depID := sharedResultID(row.DepResultID)
			if c.resultsByID[depID] == nil {
				// Vetting drops any row whose dependency is gone, so a
				// missing dep here means the parent was dropped too; edges
				// between dropped rows carry nothing.
				continue
			}
			if parent.deps == nil {
				parent.deps = make(map[sharedResultID]struct{})
			}
			dep := c.resultsByID[depID]
			parent.deps[depID] = struct{}{}
			c.rememberDependencyEdgeLocked(parent, dep)
			c.incrementIncomingOwnershipLocked(ctx, dep)
			c.traceImportResultDepLoaded(ctx, importRunID, parentID, depID)
			c.traceExplicitDepAdded(ctx, parentID, depID, "import")
		}

		for _, restored := range keptRows {
			for _, link := range restored.links {
				c.traceImportResultSnapshotLinkLoaded(ctx, importRunID, restored.id, link.RefKey, link.Role)
			}
		}

		for _, res := range c.resultsByID {
			res.onRelease = joinOnRelease(c.resultSnapshotLeaseCleanup(res), res.onRelease)
		}

		for _, res := range c.resultsByID {
			if err := c.recomputeRequiredSessionResourcesLocked(res); err != nil {
				return fmt.Errorf("recompute imported required session resources for result %d: %w", res.id, err)
			}
		}

		for resultID := range c.resultsByID {
			outputEqClasses := c.outputEqClassesForResultLocked(resultID)
			for outputEqID := range outputEqClasses {
				for dig := range c.eqClassToDigests[outputEqID] {
					set := c.egraphResultsByDigest[dig]
					if set == nil {
						set = newSharedResultIDSet()
						c.egraphResultsByDigest[dig] = set
					}
					set.Insert(resultID)
				}
			}
		}

		c.nextSharedResultID = maxResultID + 1
		c.nextEgraphTermID = maxTermID + 1
		c.nextEgraphClassID = maxEqClassID + 1
		if c.nextSharedResultID == 0 {
			c.nextSharedResultID = 1
		}
		if c.nextEgraphTermID == 0 {
			c.nextEgraphTermID = 1
		}
		if c.nextEgraphClassID == 0 {
			c.nextEgraphClassID = 1
		}

		return nil
	}()
	c.egraphMu.Unlock()
	if importErr != nil {
		return importErr
	}

	for _, resultID := range eagerDecodeResultIDs {
		res := c.resultsByID[resultID]
		state := res.loadPayloadState()
		if res == nil || state.realized || state.persistedEnvelope == nil {
			continue
		}
		call := res.loadResultCall()
		if call == nil {
			continue
		}
		decodeCtx := ContextWithCall(ctx, call)
		if decoded, err := DefaultPersistedSelfCodec.DecodeResult(decodeCtx, nil, uint64(resultID), call, *state.persistedEnvelope); err == nil && decoded != nil {
			res.payloadMu.Lock()
			if !res.materialization.realized && res.materialization.envelope != nil {
				res.self = decoded.Unwrap()
				res.materialization.realized = true
				if objDecoded, ok := decoded.(AnyObjectResult); ok && res.objClass == nil {
					res.objClass = objDecoded.ObjectType()
				}
				decodedShared := decoded.cacheSharedResult()
				if decodedShared != nil {
					res.sessionResourceHandle = decodedShared.sessionResourceHandle
					if decodedShared.requiredSessionResources != nil {
						res.requiredSessionResources = decodedShared.requiredSessionResources.Copy()
					} else if decodedShared.sessionResourceHandle == "" {
						res.requiredSessionResources = nil
					}
				}
				res.materialization.envelope = nil
			}
			res.payloadMu.Unlock()
			if onReleaser, ok := UnwrapAs[OnReleaser](decoded); ok {
				res.onRelease = joinOnRelease(c.resultSnapshotLeaseCleanup(res), onReleaser.OnRelease)
			}
			if err := c.syncResultSnapshotLeases(ctx, res); err != nil {
				return err
			}
			c.tracePersistedPayloadImportedEager(ctx, importRunID, resultID, "", "materialized")
		}
	}

	for _, resultID := range eagerDecodeResultIDs {
		res := c.resultsByID[resultID]
		state := res.loadPayloadState()
		if res == nil || state.persistedEnvelope == nil || state.realized {
			continue
		}
		c.tracePersistedPayloadImportedLazy(ctx, importRunID, resultID, "", state.persistedEnvelope.Kind, state.persistedEnvelope.TypeName)
	}

	if c.snapshotManager != nil {
		// Vetting attached owner leases for kept rows as its presence check;
		// everything else — dropped rows' leases, partial attachments from
		// rows that lost their snapshot source, strays from earlier boots —
		// is stale and goes, so a dropped row can never pin content.
		keepLeaseIDs := make(map[string]struct{})
		for _, restored := range keptRows {
			for _, link := range restored.links {
				keepLeaseIDs[resultSnapshotLeaseID(restored.id, link.Role)] = struct{}{}
			}
		}
		if err := c.snapshotManager.DeleteStaleDaggerOwnerLeases(ctx, keepLeaseIDs); err != nil {
			return fmt.Errorf("delete stale owner leases: %w", err)
		}
	}

	c.egraphMu.Lock()
	c.restoreSummary = restoreSummary
	c.importedResultCount = int64(restoreSummary.Kept)
	c.egraphMu.Unlock()
	c.traceRestoreSummary(ctx, restoreSummary)
	slog.Info("dagql persistence restore complete",
		"kept", restoreSummary.Kept, "dropped", restoreSummary.Dropped)

	return nil
}

func normalizeImportedDigest(raw string) digest.Digest {
	if raw == "" {
		return ""
	}
	return digest.Digest(raw)
}

func resolverServer(resolver TypeResolver) *Server {
	if resolver == nil {
		return nil
	}
	dag, ok := resolver.(*Server)
	if !ok {
		return nil
	}
	return dag
}

func persistedEnvelopeObjectTypeNames(env PersistedResultEnvelope, names []string) []string {
	switch env.Kind {
	case persistedResultKindObject:
		if env.TypeName != "" {
			names = append(names, env.TypeName)
		}
	case persistedResultKindList:
		for _, item := range env.Items {
			names = persistedEnvelopeObjectTypeNames(item, names)
		}
	}
	return names
}

func (c *Cache) ensurePersistedHitValueLoaded(ctx context.Context, resolver TypeResolver, hit AnyResult) (AnyResult, error) {
	if resolver == nil {
		return nil, fmt.Errorf("ensure persisted hit value loaded: type resolver is nil")
	}
	if hit == nil {
		return nil, nil
	}
	res := hit.cacheSharedResult()
	if res == nil {
		return hit, nil
	}
	res.attachDepsMu.Lock()
	attachDepsWaitCh := res.attachDepsWaitCh
	res.attachDepsMu.Unlock()
	if attachDepsWaitCh != nil {
		select {
		case <-attachDepsWaitCh:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
		res.attachDepsMu.Lock()
		attachDepsErr := res.attachDepsErr
		res.attachDepsMu.Unlock()
		if attachDepsErr != nil {
			return nil, fmt.Errorf("wait for dependency attachment: %w", attachDepsErr)
		}
	}

	for {
		state := res.loadPayloadState()
		if state.isObject && state.realized && state.self == nil {
			return nil, fmt.Errorf("ensure persisted hit value loaded: invalid object payload state for result %d (realized=true, self=nil)", res.id)
		}
		if state.realized || state.persistedEnvelope == nil {
			// A live result published with a nil value is legitimately
			// unrealized with nothing to decode; only a restored row in this
			// state is a stranded hit — nothing remains that could make its
			// value usable, so the caller demotes it to a miss.
			if !state.realized && !state.servable && res.restored {
				return nil, fmt.Errorf("%w: result %d has no value, envelope, or retained source", errSourcesExhausted, res.id)
			}
			if !state.isObject {
				c.markPendingWorkFromRestore(res, hit)
				c.registerLazyEvaluation(res, hit)
				return hit, nil
			}
			objRes, err := wrapSharedResultWithResolver(ctx, res, hit.HitCache(), resolver)
			if err != nil {
				return nil, fmt.Errorf("reconstruct object result from cache hit payload: %w", err)
			}
			c.markPendingWorkFromRestore(res, objRes)
			c.registerLazyEvaluation(res, objRes)
			return objRes, nil
		}

		res.materializeMu.Lock()
		if res.materializeWaitCh != nil {
			waitCh := res.materializeWaitCh
			res.materializeWaiters++
			res.materializeMu.Unlock()
			if err := c.waitForMaterialization(ctx, res, waitCh); err != nil {
				return nil, err
			}
			continue
		}

		waitCh := make(chan struct{})
		decodeCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
		res.materializeWaitCh = waitCh
		res.materializeCancel = cancel
		res.materializeWaiters = 1
		res.materializeErr = nil
		res.materializeMu.Unlock()

		go c.runRestoredValueDecode(decodeCtx, resolver, res, state.persistedEnvelope, waitCh)

		if err := c.waitForMaterialization(ctx, res, waitCh); err != nil {
			return nil, err
		}
	}
}

// runRestoredValueDecode is the decode phase's runner: one goroutine per
// in-flight decode, running the retained-source walk on a context detached
// from any single demander. Demanders are counted waiters; a demander that
// gives up while others remain just leaves, the last one to give up cancels
// the runner with its own cause, and a failed attempt is retried by the
// next demand once the protocol state clears. This is the same protocol the
// deferred-work phase runs (runDeferredWork); the two phases never overlap
// in demand for one result, so they can share the fields.
func (c *Cache) runRestoredValueDecode(runCtx context.Context, resolver TypeResolver, res *sharedResult, env *PersistedResultEnvelope, waitCh chan struct{}) {
	err := c.decodeRestoredValueWalk(runCtx, resolver, res, env)

	res.materializeMu.Lock()
	res.materializeErr = err
	clearState := res.materializeWaiters == 0 && res.materializeWaitCh == waitCh
	if clearState {
		res.materializeWaitCh = nil
		res.materializeCancel = nil
		res.materializeErr = nil
	}
	res.materializeMu.Unlock()
	close(waitCh)
}

// decodeRestoredValueWalk is the value phase of the retained-source walk: it
// decodes the persisted envelope, preferring the local snapshot and falling
// through to the lazy fragment when the snapshots turn out to be gone from
// the store (external loss after boot vetting). When no source can deliver,
// it reports source exhaustion, which the lookup path consumes by demoting
// the hit to a miss.
func (c *Cache) decodeRestoredValueWalk(ctx context.Context, resolver TypeResolver, res *sharedResult, env *PersistedResultEnvelope) error {
	for {
		attemptCtx := ctx
		// The walk owns source accounting. When the home retains the lazy
		// fragment but no local-snapshot source — because this walk just
		// retired a dead snapshot source, or because boot vetting or a prior
		// boot retired it and the row flushed link-less — the attempt is
		// deliberately decoding against the fragment, and fragment-capable
		// content decoders are told so explicitly. They must never infer
		// retirement from raw link absence: legitimate shapes (pending
		// values, config-only values) are link-less too.
		if res.loadLazyFragment() != nil && len(res.loadSnapshotOwnerLinks()) == 0 {
			attemptCtx = contextWithRetiredSnapshotSource(ctx, uint64(res.id))
		}
		err := c.decodeRestoredValueOnce(attemptCtx, resolver, res, env)
		if err == nil {
			outcome := cacheServeFromSnapshot
			if len(res.loadSnapshotOwnerLinks()) == 0 {
				outcome = cacheServeFromLazyForm
			}
			c.classifyServeOutcome(ctx, outcome, res.loadResultCall(), res.id)
			return nil
		}
		if !bkcache.IsNotFound(err) {
			return err
		}
		// A snapshot this value needs is gone from the local store. If the
		// snapshot source is still recorded and a lazy fragment remains,
		// retire the snapshot source — its leases pin nothing real — and
		// decode again against the fragment.
		links := res.loadSnapshotOwnerLinks()
		if len(links) > 0 && res.loadLazyFragment() != nil {
			c.traceRestoredSnapshotSourceRetired(ctx, res, err)
			res.storeSnapshotOwnerLinks(nil)
			seen := make(map[string]struct{}, len(links))
			for _, link := range links {
				leaseID := resultSnapshotLeaseID(res.id, link.Role)
				if _, alreadySeen := seen[leaseID]; alreadySeen {
					continue
				}
				seen[leaseID] = struct{}{}
				if c.snapshotManager != nil {
					if removeErr := c.snapshotManager.RemoveLease(ctx, leaseID); removeErr != nil {
						return fmt.Errorf("remove dead snapshot owner lease %q: %w", leaseID, removeErr)
					}
				}
			}
			continue
		}
		return fmt.Errorf("%w: result %d: %w", errSourcesExhausted, res.id, err)
	}
}

func (c *Cache) decodeRestoredValueOnce(ctx context.Context, resolver TypeResolver, res *sharedResult, env *PersistedResultEnvelope) error {
	call := res.loadResultCall()
	if call == nil {
		return fmt.Errorf("decode persisted hit payload: missing authoritative call for object result %d", res.id)
	}
	decodeResolver := resolver
	seenTypeNames := map[string]struct{}{}
	for _, typeName := range persistedEnvelopeObjectTypeNames(*env, nil) {
		if _, seen := seenTypeNames[typeName]; seen {
			continue
		}
		seenTypeNames[typeName] = struct{}{}
		var err error
		decodeResolver, err = resolverForSharedResultObject(ctx, decodeResolver, res, typeName)
		if err != nil {
			return fmt.Errorf("decode persisted hit payload: %w", err)
		}
	}
	dag := resolverServer(decodeResolver)
	if dag == nil {
		return fmt.Errorf("decode persisted hit payload: type resolver %T does not provide dagql server", decodeResolver)
	}
	decodeCtx := ContextWithCall(ctx, call)
	decoded, err := DefaultPersistedSelfCodec.DecodeResult(decodeCtx, dag, uint64(res.id), call, *env)
	if err != nil {
		c.tracePersistedPayloadDecodeFailed(ctx, res, env, err)
		if bkcache.IsNotFound(err) {
			return err
		}
		return fmt.Errorf("decode persisted hit payload: %w", err)
	}
	if decoded == nil || decoded.Unwrap() == nil {
		return fmt.Errorf("decode persisted hit payload: decoded nil payload for object result %d", res.id)
	}

	res.payloadMu.Lock()
	decodeWon := false
	if !res.materialization.realized && res.materialization.envelope != nil {
		decodeWon = true
		res.self = decoded.Unwrap()
		res.materialization.realized = true
		if objDecoded, ok := decoded.(AnyObjectResult); ok && res.objClass == nil {
			res.objClass = objDecoded.ObjectType()
		}
		res.materialization.envelope = nil
	}
	res.payloadMu.Unlock()
	if decodeWon {
		// The session-resource fields belong to candidate eligibility, which
		// reads them under the e-graph lock — never under payloadMu (the
		// established order is egraphMu before payloadMu, so they cannot be
		// written inside the block above). Import populated both from the
		// same envelope already; this re-affirms them from the decoded value.
		if decodedShared := decoded.cacheSharedResult(); decodedShared != nil {
			c.egraphMu.Lock()
			res.sessionResourceHandle = decodedShared.sessionResourceHandle
			if decodedShared.requiredSessionResources != nil {
				res.requiredSessionResources = decodedShared.requiredSessionResources.Copy()
			} else if decodedShared.sessionResourceHandle == "" {
				res.requiredSessionResources = nil
			}
			c.egraphMu.Unlock()
		}
		c.tracePersistedPayloadDecoded(ctx, res, env)
	}
	if onReleaser, ok := UnwrapAs[OnReleaser](decoded); ok {
		res.onRelease = joinOnRelease(c.resultSnapshotLeaseCleanup(res), onReleaser.OnRelease)
	}
	if err := c.syncResultSnapshotLeases(ctx, res); err != nil {
		return fmt.Errorf("sync persisted hit owner leases: %w", err)
	}
	return nil
}
