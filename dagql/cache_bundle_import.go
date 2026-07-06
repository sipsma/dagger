package dagql

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/dagger/dagger/dagql/call"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine/slog"
	"github.com/opencontainers/go-digest"
)

// CacheBundleImportSummary reports one bundle's merge outcome.
type CacheBundleImportSummary struct {
	StoreUUID       string
	ResultsInBundle int

	// RowsImported are rows staged as new local results; RowsDedupedByOrigin
	// are bundle rows whose origin was already present (no new row).
	RowsImported        int
	RowsDedupedByOrigin int

	// RowsDroppedVetting counts rows dropped by the shared vetting rules
	// (malformed, missing-dep, cycles, cascades) before staging;
	// RowsDroppedRewrite counts rows dropped because a reference could not
	// be rewritten into the local ID space (its target was dropped) or a
	// payload token was malformed — the missing-dep rule, applied
	// post-remap.
	RowsDroppedVetting int
	RowsDroppedRewrite int

	TermsImported int
	TermsDeduped  int
}

// ImportBundle merges one bundle into the live cache. It is a separate
// entry point from local restore with a deliberately separate failure
// vocabulary: any store-level problem in the bundle returns a
// *CacheBundleSkipError and leaves the local store untouched — no code
// path here constructs a local persistence reset reason or touches the
// local DB. Per-result damage inside the bundle drops that row with its
// in-bundle dependents, exactly like local restore vetting.
//
// The merge is all-or-nothing per bundle: every fallible step (parse,
// vetting, reference rewrite) runs before the first mutation of shared
// cache state; the commit phase performs only infallible map writes.
func (c *Cache) ImportBundle(ctx context.Context, r io.Reader) (CacheBundleImportSummary, error) {
	var summary CacheBundleImportSummary
	if c == nil || c.pdb == nil || c.storeUUID == "" {
		return summary, errors.New("import bundle: cache has no persistence store")
	}

	tmpDir, err := os.MkdirTemp("", "dagger-cache-bundle-import-*")
	if err != nil {
		return summary, fmt.Errorf("import bundle: scratch dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	manifest, metadataPath, err := readCacheBundleArchive(r, tmpDir)
	if err != nil {
		return summary, err
	}
	summary.StoreUUID = manifest.StoreUUID
	if manifest.BundleFormat != CacheBundleFormatVersion {
		return summary, bundleSkip(CacheBundleSkipManifestMismatch,
			fmt.Errorf("bundle format %d, engine speaks %d", manifest.BundleFormat, CacheBundleFormatVersion))
	}
	if manifest.SchemaVersion != cachePersistenceSchemaVersion {
		return summary, bundleSkip(CacheBundleSkipManifestMismatch,
			fmt.Errorf("bundle schema version %q, engine speaks %q", manifest.SchemaVersion, cachePersistenceSchemaVersion))
	}

	rows, err := readBundleMetadataRows(ctx, metadataPath)
	if err != nil {
		return summary, err
	}
	summary.ResultsInBundle = len(rows.results)

	if err := validateBundleIdentityRows(rows); err != nil {
		return summary, bundleSkip(CacheBundleSkipBrokenIdentity, err)
	}

	// Per-result vetting: the exact local rules (malformed, missing-dep,
	// cycle, cascade), bundle-flavored — bundles carry no snapshot link
	// rows (and no chain rows: chains live in the manifest), so the local
	// snapshot-presence check has nothing to test and content viability is
	// what the row structurally carries.
	kept, vetSummary, err := c.vetRestoredResults(ctx, rows.results, rows.resultDeps, nil, rows.resultOrigins, nil)
	if err != nil {
		return summary, bundleSkip(CacheBundleSkipBrokenIdentity, err)
	}
	summary.RowsDroppedVetting = vetSummary.Dropped

	order := topoOrderRestoredRows(kept)

	c.egraphMu.Lock()
	defer c.egraphMu.Unlock()
	c.initEgraphLocked()

	// Phase 1 — origin dedup + local ID assignment. Origin is the row
	// creation gate: a row whose origin is already present maps onto the
	// existing local row and creates nothing. In-bundle duplicate origins
	// collapse onto the first occurrence (first-imported wins, like any
	// same-origin observation).
	remap := make(map[sharedResultID]sharedResultID, len(kept))
	bundleIDByOrigin := make(map[resultOrigin]sharedResultID, len(kept))
	// aliasOf points an in-bundle duplicate origin at its first occurrence,
	// so a duplicate resolves through — and drops with — the row that won.
	aliasOf := make(map[sharedResultID]sharedResultID)
	stagedIDs := make(map[sharedResultID]struct{}, len(kept))
	dedupedIDs := make(map[sharedResultID]struct{})
	// IDs are RESERVED from a local cursor during staging and the shared
	// allocator advances only at commit, over the rows that actually land:
	// a bundle whose rows all drop leaves the allocator exactly as it was
	// (the all-or-nothing contract covers allocator state too). Reserved
	// IDs of rewrite-dropped rows are never committed and never referenced
	// — any row referencing a dropped row drops with it.
	reservedNextID := c.nextSharedResultID
	for _, bundleID := range order {
		restored := kept[bundleID]
		if firstBundleID, dup := bundleIDByOrigin[restored.origin]; dup {
			aliasOf[bundleID] = firstBundleID
			dedupedIDs[bundleID] = struct{}{}
			summary.RowsDedupedByOrigin++
			continue
		}
		bundleIDByOrigin[restored.origin] = bundleID
		// The dedup gate reads "origin already present" as present on a
		// live, servable row: a row dropped at source exhaustion is no
		// longer servable (and never flushes), so a bundle re-supplying its
		// origin stages a fresh row — the healing miss, one level earlier.
		if localID, exists := c.resultsByOrigin[restored.origin]; exists {
			if existing := c.resultsByID[localID]; existing != nil && !existing.dropped {
				remap[bundleID] = localID
				dedupedIDs[bundleID] = struct{}{}
				summary.RowsDedupedByOrigin++
				continue
			}
		}
		localID := reservedNextID
		reservedNextID++
		remap[bundleID] = localID
		stagedIDs[bundleID] = struct{}{}
	}
	resolveBundleID := func(bundleID sharedResultID) sharedResultID {
		if first, ok := aliasOf[bundleID]; ok {
			return first
		}
		return bundleID
	}

	// Phase 2 — rewrite references into the local ID space, still without
	// touching shared state. A reference that misses the remap table means
	// its target was dropped by vetting: the referring row drops with its
	// dependents (the missing-dep rule, post-remap). A malformed payload
	// token is per-row damage and drops the same way.
	remapRef := func(bundleResultID uint64) (uint64, error) {
		localID, ok := remap[resolveBundleID(sharedResultID(bundleResultID))]
		if !ok {
			return 0, fmt.Errorf("reference to result %d not present after vetting", bundleResultID)
		}
		return uint64(localID), nil
	}
	staged := make(map[sharedResultID]*stagedBundleRow, len(stagedIDs))
	droppedAtRewrite := make(map[sharedResultID]struct{})
	var stagedOrder []sharedResultID
	for _, bundleID := range order {
		if _, isStaged := stagedIDs[bundleID]; !isStaged {
			continue
		}
		restored := kept[bundleID]
		dropRow := func(err error) {
			droppedAtRewrite[bundleID] = struct{}{}
			delete(remap, bundleID)
			summary.RowsDroppedRewrite++
			slog.Warn("dropping bundle result at reference rewrite",
				"bundleResultID", bundleID, "err", err)
			c.traceRestoreResultDropped(ctx, bundleID, restoreDropMissingDep)
		}
		// Dependency cascade within this wave: deps vetted fine but were
		// then dropped here.
		var cascade error
		for _, depID := range restored.deps {
			if _, dropped := droppedAtRewrite[resolveBundleID(depID)]; dropped {
				cascade = fmt.Errorf("dependency %d dropped at rewrite", depID)
				break
			}
		}
		if cascade != nil {
			dropRow(cascade)
			continue
		}
		row, err := rewriteBundleRow(restored, remap[bundleID], remapRef, c.rewriteBundleCallID(remapRef))
		if err != nil {
			dropRow(err)
			continue
		}
		staged[bundleID] = row
		stagedOrder = append(stagedOrder, bundleID)
	}

	// Phase 3 — commit. Only infallible map writes from here on: staged
	// rows land, identity teaches into the live e-graph with local-space
	// key recomputation (R16), deps and persisted edges land with dedup,
	// same-origin observations union sources and refresh timestamps.

	// The allocator advances over exactly the IDs that commit. Surviving
	// reserved IDs are contiguous-from-current except for rewrite-dropped
	// gaps, which nothing references; a bundle that commits nothing leaves
	// the allocator untouched.
	for _, bundleID := range stagedOrder {
		if localID := remap[bundleID]; localID >= c.nextSharedResultID {
			c.nextSharedResultID = localID + 1
			c.noteAllocatedResultIDLocked(localID)
		}
	}

	for _, bundleID := range stagedOrder {
		row := staged[bundleID]
		restored := kept[bundleID]
		localID := remap[bundleID]

		res := &sharedResult{
			id:                    localID,
			isObject:              row.envelope.Kind == persistedResultKindObject,
			sessionResourceHandle: row.envelope.SessionResourceHandle,
			expiresAtUnix:         restored.row.ExpiresAtUnix,
			createdAtUnixNano:     restored.row.CreatedAtUnixNano,
			lastUsedAtUnixNano:    restored.row.LastUsedAtUnixNano,
			description:           restored.row.Description,
			recordType:            restored.row.RecordType,
			materialization:       materializationState{envelope: row.envelope},
			// Bundle rows are created from persisted state exactly like
			// local restore's: their re-attached deferred work failing
			// permanently is retained-source exhaustion (the row drops and
			// future lookups heal), and their hits classify as restored.
			restored: true,
		}
		c.assignResultOriginLocked(res, restored.origin)
		if len(row.envelope.LazyJSON) > 0 {
			// Fragment-with-no-snapshot-links is also the shape the decode
			// walk reads as a retired snapshot source, which is exactly
			// right for bundle rows: any snapshot IDs their payloads
			// textually carry are the exporter's, and first-use decode must
			// re-make from the fragment, never probe local snapshots.
			res.materialization.setLazyFragment(&PersistedLazyFragment{
				Kind: row.envelope.LazyKind,
				JSON: row.envelope.LazyJSON,
			})
		}
		res.storeResultCall(row.frame)
		if row.envelope.Kind == persistedResultKindNull {
			res.materialization.realized = true
			res.materialization.envelope = nil
		}
		res.onRelease = joinOnRelease(c.resultSnapshotLeaseCleanup(res), res.onRelease)
		c.resultsByID[localID] = res
		c.traceResultCallFrameUpdated(ctx, res, "import_bundle_result", nil, row.frame)
		summary.RowsImported++
	}

	// Dependency edges for staged rows.
	for _, bundleID := range stagedOrder {
		restored := kept[bundleID]
		parent := c.resultsByID[remap[bundleID]]
		for _, depBundleID := range restored.deps {
			depLocalID := remap[resolveBundleID(depBundleID)]
			dep := c.resultsByID[depLocalID]
			if dep == nil {
				continue
			}
			if parent.deps == nil {
				parent.deps = make(map[sharedResultID]struct{})
			}
			if _, exists := parent.deps[depLocalID]; exists {
				continue
			}
			parent.deps[depLocalID] = struct{}{}
			c.rememberDependencyEdgeLocked(parent, dep)
			c.incrementIncomingOwnershipLocked(ctx, dep)
		}
	}

	// Persisted edges: what the exporter retained, the importer retains —
	// as ordinary pruneable edges (the unpruneable bit was stripped at the
	// export boundary; strip again defensively), deduped against existing
	// local edges.
	if c.persistedEdgesByResult == nil {
		c.persistedEdgesByResult = make(map[sharedResultID]persistedEdge)
	}
	for _, edgeRow := range rows.persistedEdges {
		localID, ok := remap[resolveBundleID(sharedResultID(edgeRow.ResultID))]
		if !ok {
			continue
		}
		res := c.resultsByID[localID]
		if res == nil {
			continue
		}
		if _, exists := c.persistedEdgesByResult[localID]; exists {
			continue
		}
		c.persistedEdgesByResult[localID] = persistedEdge{
			resultID:          localID,
			createdAtUnixNano: edgeRow.CreatedAtUnixNano,
			expiresAtUnix:     edgeRow.ExpiresAtUnix,
			unpruneable:       false,
		}
		c.incrementIncomingOwnershipLocked(ctx, res)
	}

	// Identity: class digests teach into the live e-graph — this is where
	// cross-engine equivalence composes — and term keys recompute in the
	// local ID space, the same recompute local restore runs (R16).
	classRemap := make(map[int64]eqClassID, len(rows.eqClasses))
	digestsByClass := make(map[int64][]persistdb.MirrorEqClassDigest)
	for _, row := range rows.eqClassDigests {
		digestsByClass[row.EqClassID] = append(digestsByClass[row.EqClassID], row)
	}
	for _, classRow := range rows.eqClasses {
		var mergeIDs []eqClassID
		for _, digRow := range digestsByClass[classRow.ID] {
			if id := c.ensureEqClassForDigestLocked(ctx, digRow.Digest); id != 0 {
				mergeIDs = append(mergeIDs, id)
			}
		}
		var localClass eqClassID
		switch len(mergeIDs) {
		case 0:
			// A class with no digests identifies nothing portable.
		case 1:
			localClass = mergeIDs[0]
		default:
			localClass = c.mergeEqClassesLocked(ctx, mergeIDs...)
		}
		classRemap[classRow.ID] = localClass
		if localClass == 0 {
			continue
		}
		for _, digRow := range digestsByClass[classRow.ID] {
			if digRow.Label == "" {
				continue
			}
			extras := c.eqClassExtraDigests[localClass]
			if extras == nil {
				extras = make(map[call.ExtraDigest]struct{})
				c.eqClassExtraDigests[localClass] = extras
			}
			extras[call.ExtraDigest{
				Digest: digest.Digest(digRow.Digest),
				Label:  digRow.Label,
			}] = struct{}{}
		}
	}

	localClassFor := func(bundleClassID int64) eqClassID {
		if bundleClassID == 0 {
			return 0
		}
		return c.findEqClassLocked(classRemap[bundleClassID])
	}

	bundleTermInputs := make(map[int64][]persistdb.MirrorTermInput, len(rows.terms))
	for _, input := range rows.termInputs {
		bundleTermInputs[input.TermID] = append(bundleTermInputs[input.TermID], input)
	}
	for _, termRow := range rows.terms {
		inputs := bundleTermInputs[termRow.ID]
		slices.SortFunc(inputs, func(a, b persistdb.MirrorTermInput) int {
			switch {
			case a.Position < b.Position:
				return -1
			case a.Position > b.Position:
				return 1
			default:
				return 0
			}
		})
		inputEqIDs := make([]eqClassID, 0, len(inputs))
		inputProvenance := make([]egraphInputProvenanceKind, 0, len(inputs))
		for _, input := range inputs {
			inputEqIDs = append(inputEqIDs, localClassFor(input.InputEqClassID))
			inputProvenance = append(inputProvenance, egraphInputProvenanceKind(input.ProvenanceKind))
		}
		outputEqID := localClassFor(termRow.OutputEqClassID)
		selfDigest := normalizeImportedDigest(termRow.SelfDigest)

		// The same term may already exist locally (an earlier import of an
		// overlapping bundle, or independent local work): same recomputed
		// key and same canonical output class means nothing new to teach.
		termDigest := calcEgraphTermDigest(selfDigest, inputEqIDs)
		alreadyKnown := false
		if existingTerms := c.egraphTermsByTermDigest[termDigest]; existingTerms != nil {
			for existingID := range existingTerms.Items() {
				existing := c.egraphTerms[existingID]
				if existing == nil {
					continue
				}
				if c.findEqClassLocked(existing.outputEqID) == outputEqID {
					alreadyKnown = true
					break
				}
			}
		}
		if alreadyKnown {
			summary.TermsDeduped++
			continue
		}

		termID := c.nextEgraphTermID
		c.nextEgraphTermID++
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
		summary.TermsImported++
	}

	// Output-class membership and digest indexes. This teaching applies to
	// every kept row's local target — staged rows AND rows deduped by
	// origin: a later bundle's observation of an existing row may carry
	// identity evidence (a content digest, a new output class) the local
	// store has never seen, and same-origin observations union identity
	// exactly as they union sources. Without the union, exact-digest
	// lookups on the new evidence could never reach the existing row
	// (lookup reads egraphResultsByDigest directly).
	touchedLocalIDs := make(map[sharedResultID]struct{}, len(stagedOrder)+len(dedupedIDs))
	for _, row := range rows.resultOutputEqClasses {
		bundleID := sharedResultID(row.ResultID)
		localID, found := remap[resolveBundleID(bundleID)]
		if !found || c.resultsByID[localID] == nil {
			continue
		}
		outputEqID := localClassFor(row.EqClassID)
		if outputEqID == 0 {
			// Unreachable after up-front validation (every referenced class
			// exists and carries digests); loud if it ever regresses.
			slog.Error("bundle membership row resolved to no local class",
				"bundleResultID", bundleID, "bundleEqClassID", row.EqClassID)
			continue
		}
		outputEqClasses := c.resultOutputEqClasses[localID]
		if outputEqClasses == nil {
			outputEqClasses = make(map[eqClassID]struct{})
			c.resultOutputEqClasses[localID] = outputEqClasses
		}
		outputEqClasses[outputEqID] = struct{}{}
		touchedLocalIDs[localID] = struct{}{}
	}
	for _, bundleID := range stagedOrder {
		touchedLocalIDs[remap[bundleID]] = struct{}{}
	}
	for localID := range touchedLocalIDs {
		for outputEqID := range c.outputEqClassesForResultLocked(localID) {
			for dig := range c.eqClassToDigests[outputEqID] {
				set := c.egraphResultsByDigest[dig]
				if set == nil {
					set = newSharedResultIDSet()
					c.egraphResultsByDigest[dig] = set
				}
				set.Insert(localID)
			}
		}
	}

	// Session-resource requirements recompute transitively, deps-first
	// (the staged order is topological), exactly like local import.
	for _, bundleID := range stagedOrder {
		localID := remap[bundleID]
		if err := c.recomputeRequiredSessionResourcesLocked(c.resultsByID[localID]); err != nil {
			// Cannot happen: every staged dependency was committed above.
			// Loud, but never a local reset: the row's requirements stay
			// conservative (unset ⇒ recomputed on next lookup path).
			slog.Error("recompute session resources for bundle result", "localResultID", localID, "err", err)
		}
	}

	// Same-origin observations union sources and refresh timestamps, never
	// replace rows: a deduped bundle row may add its lazy-form source to an
	// existing row that lacks one; nothing is overwritten.
	for _, bundleID := range order {
		if _, isDeduped := dedupedIDs[bundleID]; !isDeduped {
			continue
		}
		restored := kept[bundleID]
		localID, ok := remap[resolveBundleID(bundleID)]
		if !ok {
			continue
		}
		res := c.resultsByID[localID]
		if res == nil {
			continue
		}
		res.payloadMu.Lock()
		if restored.row.LastUsedAtUnixNano > res.lastUsedAtUnixNano {
			res.lastUsedAtUnixNano = restored.row.LastUsedAtUnixNano
		}
		hasLazySource := res.materialization.lazyFragment() != nil
		res.payloadMu.Unlock()
		if hasLazySource || len(restored.env.LazyJSON) == 0 {
			continue
		}
		rewrittenLazy, err := rewritePersistedPayloadRefs(restored.env.LazyJSON, remapRef, c.rewriteBundleCallID(remapRef))
		if err != nil {
			// The union is additive best-effort: an unrewritable fragment
			// adds nothing (the row itself is untouched).
			slog.Warn("skipping lazy-source union for deduped bundle row", "bundleResultID", bundleID, "err", err)
			continue
		}
		res.payloadMu.Lock()
		res.materialization.setLazyFragment(&PersistedLazyFragment{
			Kind: restored.env.LazyKind,
			JSON: rewrittenLazy,
		})
		res.payloadMu.Unlock()
	}

	// The same opportunistic eager decode local restore runs: payloads
	// that reconstruct without a live dagql server (scalars and lists of
	// scalars — object decode requires a server and stays lazy) realize
	// now; everything else stays an envelope for first-use decode. This is serve-path parity — a bundle
	// row must be exactly as servable as the same row restored locally.
	// Deliberately absent from local restore's version: the owner-lease
	// sync. A foreign payload's decoded value may textually carry the
	// exporter's engine-local snapshot IDs, and those must never bind
	// leases onto coincidentally-matching local snapshots (R4: refKeys
	// never cross).
	for _, bundleID := range stagedOrder {
		res := c.resultsByID[remap[bundleID]]
		state := res.loadPayloadState()
		if res == nil || state.realized || state.persistedEnvelope == nil {
			continue
		}
		frame := res.loadResultCall()
		if frame == nil {
			continue
		}
		decodeCtx := ContextWithCall(ctx, frame)
		decoded, err := DefaultPersistedSelfCodec.DecodeResult(decodeCtx, nil, uint64(res.id), frame, *state.persistedEnvelope)
		if err != nil || decoded == nil {
			continue
		}
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
	}

	c.importedResultCount += int64(summary.RowsImported)

	slog.Info("cache bundle imported",
		"storeUUID", manifest.StoreUUID,
		"results", summary.ResultsInBundle,
		"imported", summary.RowsImported,
		"dedupedByOrigin", summary.RowsDedupedByOrigin,
		"droppedVetting", summary.RowsDroppedVetting,
		"droppedRewrite", summary.RowsDroppedRewrite,
	)
	return summary, nil
}

// stagedBundleRow is one kept bundle row with every reference rewritten
// into the local ID space, ready for infallible commit.
type stagedBundleRow struct {
	frame    *ResultCall
	envelope *PersistedResultEnvelope
}

// rewriteBundleRow applies the portable-ref contract's four mechanisms to
// one row: the structured frame walk, the envelope's structural self-IDs
// (per-item for lists), and the payload/lazy-fragment token walks (result
// refs and call IDs together).
func rewriteBundleRow(
	restored *restoredResultRow,
	localID sharedResultID,
	remapRef func(uint64) (uint64, error),
	rewriteCallID func(string) (string, error),
) (*stagedBundleRow, error) {
	frame := restored.frame.clone()
	if err := rewriteResultCallRefs(frame, remapRef); err != nil {
		return nil, fmt.Errorf("rewrite frame refs: %w", err)
	}

	env := restored.env
	rewrittenEnv, err := rewriteBundleEnvelope(&env, uint64(localID), remapRef, rewriteCallID)
	if err != nil {
		return nil, err
	}
	return &stagedBundleRow{frame: frame, envelope: rewrittenEnv}, nil
}

func rewriteBundleEnvelope(
	env *PersistedResultEnvelope,
	selfID uint64,
	remapRef func(uint64) (uint64, error),
	rewriteCallID func(string) (string, error),
) (*PersistedResultEnvelope, error) {
	out := *env
	if out.ResultID != 0 {
		out.ResultID = selfID
	}
	var err error
	if out.ObjectJSON, err = rewritePersistedPayloadRefs(env.ObjectJSON, remapRef, rewriteCallID); err != nil {
		return nil, fmt.Errorf("rewrite object payload: %w", err)
	}
	if out.ScalarJSON, err = rewritePersistedPayloadRefs(env.ScalarJSON, remapRef, rewriteCallID); err != nil {
		return nil, fmt.Errorf("rewrite scalar payload: %w", err)
	}
	if out.LazyJSON, err = rewritePersistedPayloadRefs(env.LazyJSON, remapRef, rewriteCallID); err != nil {
		return nil, fmt.Errorf("rewrite lazy fragment: %w", err)
	}
	if len(env.Items) > 0 {
		out.Items = make([]PersistedResultEnvelope, len(env.Items))
		for i := range env.Items {
			item := env.Items[i]
			itemSelfID := item.ResultID
			if itemSelfID != 0 {
				remapped, err := remapRef(itemSelfID)
				if err != nil {
					return nil, fmt.Errorf("rewrite list item %d self-ID: %w", i, err)
				}
				itemSelfID = remapped
			}
			rewrittenItem, err := rewriteBundleEnvelope(&item, itemSelfID, remapRef, rewriteCallID)
			if err != nil {
				return nil, fmt.Errorf("list item %d: %w", i, err)
			}
			out.Items[i] = *rewrittenItem
		}
	}
	return &out, nil
}

// rewriteBundleCallID rewrites one encoded call ID: handle-form IDs embed
// an engine-local result ID and are decoded, remapped, and re-encoded;
// recipe-form IDs reference only intra-recipe call digests and cross
// unchanged (the chunk-A audit: the recipe DAG wire format has no
// engine-result leaf other than the handle form itself).
func (c *Cache) rewriteBundleCallID(remapRef func(uint64) (uint64, error)) func(string) (string, error) {
	return func(encoded string) (string, error) {
		if encoded == "" {
			return encoded, nil
		}
		var id call.ID
		if err := id.Decode(encoded); err != nil {
			return "", fmt.Errorf("decode persisted call ID: %w", err)
		}
		if !id.IsHandle() {
			return encoded, nil
		}
		remapped, err := remapRef(id.EngineResultID())
		if err != nil {
			return "", fmt.Errorf("rewrite handle-form call ID: %w", err)
		}
		rewritten := call.NewEngineResultID(remapped, id.Type())
		reencoded, err := rewritten.Encode()
		if err != nil {
			return "", fmt.Errorf("re-encode handle-form call ID: %w", err)
		}
		return reencoded, nil
	}
}

// bundleMetadataRows is every table bundle import reads. The engine-local
// tables (snapshot links, snapshot-manager mirrors) are deliberately never
// read from a bundle.
type bundleMetadataRows struct {
	results               []persistdb.MirrorResult
	resultDeps            []persistdb.MirrorResultDep
	resultOrigins         []persistdb.MirrorResultOrigin
	persistedEdges        []persistdb.MirrorPersistedEdge
	eqClasses             []persistdb.MirrorEqClass
	eqClassDigests        []persistdb.MirrorEqClassDigest
	terms                 []persistdb.MirrorTerm
	termInputs            []persistdb.MirrorTermInput
	resultOutputEqClasses []persistdb.MirrorResultOutputEqClass
}

func readBundleMetadataRows(ctx context.Context, metadataPath string) (rows bundleMetadataRows, rerr error) {
	db, q, err := prepareCacheDBs(ctx, metadataPath)
	if err != nil {
		return rows, bundleSkip(CacheBundleSkipUnreadableMetadata, err)
	}
	defer func() {
		if cerr := closeCacheDBs(db, q); cerr != nil && rerr == nil {
			rerr = bundleSkip(CacheBundleSkipUnreadableMetadata, cerr)
		}
	}()

	read := func(name string, do func() error) {
		if rerr != nil {
			return
		}
		if err := do(); err != nil {
			rerr = bundleSkip(CacheBundleSkipUnreadableMetadata, fmt.Errorf("read bundle %s: %w", name, err))
		}
	}
	read("results", func() (err error) { rows.results, err = q.ListMirrorResults(ctx); return })
	read("result_deps", func() (err error) { rows.resultDeps, err = q.ListMirrorResultDeps(ctx); return })
	read("result_origins", func() (err error) { rows.resultOrigins, err = q.ListMirrorResultOrigins(ctx); return })
	read("persisted_edges", func() (err error) { rows.persistedEdges, err = q.ListMirrorPersistedEdges(ctx); return })
	read("eq_classes", func() (err error) { rows.eqClasses, err = q.ListMirrorEqClasses(ctx); return })
	read("eq_class_digests", func() (err error) { rows.eqClassDigests, err = q.ListMirrorEqClassDigests(ctx); return })
	read("terms", func() (err error) { rows.terms, err = q.ListMirrorTerms(ctx); return })
	read("term_inputs", func() (err error) { rows.termInputs, err = q.ListMirrorTermInputs(ctx); return })
	read("result_output_eq_classes", func() (err error) {
		rows.resultOutputEqClasses, err = q.ListMirrorResultOutputEqClasses(ctx)
		return
	})
	return rows, rerr
}

// validateBundleIdentityRows applies the same store-level identity checks
// local restore treats as wipe-worthy — zero IDs, broken identity-table
// references, invalid provenance — as pure pre-validation. In a bundle they
// mean "skip this bundle", never anything about the local store. The rules
// are deliberately complete: everything the merge later resolves (class
// refs from membership rows, terms and term inputs; result refs from
// membership and edge rows) is proven resolvable here, so the commit phase
// never meets a broken reference it would have to skip silently. A class
// referenced by anything must also carry at least one digest — a
// digest-less class identifies nothing and cannot be taught into the local
// e-graph, so a reference to one is broken identity metadata, not a
// harmless row.
//
//nolint:gocyclo // one flat rule list; splitting would obscure the contract
func validateBundleIdentityRows(rows bundleMetadataRows) error {
	resultExists := make(map[int64]struct{}, len(rows.results))
	for _, row := range rows.results {
		resultExists[row.ID] = struct{}{}
	}
	classExists := make(map[int64]struct{}, len(rows.eqClasses))
	for _, row := range rows.eqClasses {
		if row.ID == 0 {
			return errors.New("eq_class with zero ID")
		}
		classExists[row.ID] = struct{}{}
	}
	classHasDigests := make(map[int64]struct{}, len(rows.eqClasses))
	for _, row := range rows.eqClassDigests {
		if row.EqClassID == 0 {
			return fmt.Errorf("eq_class_digest %q with zero eq_class_id", row.Digest)
		}
		if _, ok := classExists[row.EqClassID]; !ok {
			return fmt.Errorf("eq_class_digest %q references missing eq_class %d", row.Digest, row.EqClassID)
		}
		if row.Digest == "" {
			return fmt.Errorf("empty digest for eq_class %d", row.EqClassID)
		}
		classHasDigests[row.EqClassID] = struct{}{}
	}
	referencedClassUsable := func(classID int64) error {
		if _, ok := classExists[classID]; !ok {
			return fmt.Errorf("missing eq_class %d", classID)
		}
		if _, ok := classHasDigests[classID]; !ok {
			return fmt.Errorf("eq_class %d has no digests", classID)
		}
		return nil
	}
	termExists := make(map[int64]struct{}, len(rows.terms))
	for _, row := range rows.terms {
		if row.ID == 0 {
			return errors.New("term with zero ID")
		}
		// A zero output class never occurs in an honest store: every live
		// term is born from a nonzero merged output class and flush
		// preserves that, so a bundle claiming one is corrupt — and
		// accepting it would let bundles introduce zero-output terms into
		// stores that otherwise never contain them.
		if row.OutputEqClassID == 0 {
			return fmt.Errorf("term %d has zero output eq_class", row.ID)
		}
		if err := referencedClassUsable(row.OutputEqClassID); err != nil {
			return fmt.Errorf("term %d output: %w", row.ID, err)
		}
		termExists[row.ID] = struct{}{}
	}
	positionsByTerm := make(map[int64][]int64)
	for _, row := range rows.termInputs {
		if row.TermID == 0 {
			return errors.New("term_input with zero term_id")
		}
		if _, ok := termExists[row.TermID]; !ok {
			return fmt.Errorf("term_input references missing term %d", row.TermID)
		}
		switch egraphInputProvenanceKind(row.ProvenanceKind) {
		case egraphInputProvenanceKindResult, egraphInputProvenanceKindDigest:
		default:
			return fmt.Errorf("term_input %d/%d has unsupported provenance %q", row.TermID, row.Position, row.ProvenanceKind)
		}
		// A zero INPUT class is legal store state, unlike a zero output:
		// digest-provenance input slots whose digest resolved to no class
		// persist as zero (ensureTermInputEqIDsLocked produces them, local
		// restore accepts them), so bundle legality matches the local
		// store's exactly.
		if row.InputEqClassID != 0 {
			if err := referencedClassUsable(row.InputEqClassID); err != nil {
				return fmt.Errorf("term_input %d/%d: %w", row.TermID, row.Position, err)
			}
		}
		positionsByTerm[row.TermID] = append(positionsByTerm[row.TermID], row.Position)
	}
	for termID, positions := range positionsByTerm {
		slices.Sort(positions)
		for i, pos := range positions {
			if pos != int64(i) {
				return fmt.Errorf("term %d inputs missing position %d", termID, i)
			}
		}
	}
	for _, row := range rows.resultOutputEqClasses {
		if row.ResultID == 0 {
			return errors.New("result_output_eq_class with zero result ID")
		}
		if _, ok := resultExists[row.ResultID]; !ok {
			return fmt.Errorf("result_output_eq_class references missing result %d", row.ResultID)
		}
		if row.EqClassID == 0 {
			return fmt.Errorf("result_output_eq_class for result %d with zero eq_class_id", row.ResultID)
		}
		if err := referencedClassUsable(row.EqClassID); err != nil {
			return fmt.Errorf("result_output_eq_class for result %d: %w", row.ResultID, err)
		}
	}
	for _, row := range rows.persistedEdges {
		if row.ResultID == 0 {
			return errors.New("persisted_edge with zero result ID")
		}
		if _, ok := resultExists[row.ResultID]; !ok {
			return fmt.Errorf("persisted_edge references missing result %d", row.ResultID)
		}
	}
	return nil
}

// topoOrderRestoredRows orders kept rows dependencies-first. Kept rows are
// acyclic by vetting (cycles dropped there), so the order always covers
// every row.
func topoOrderRestoredRows(kept map[sharedResultID]*restoredResultRow) []sharedResultID {
	undecided := make(map[sharedResultID]int, len(kept))
	dependents := make(map[sharedResultID][]sharedResultID)
	for id, row := range kept {
		count := 0
		for _, depID := range row.deps {
			if _, ok := kept[depID]; !ok {
				continue
			}
			count++
			dependents[depID] = append(dependents[depID], id)
		}
		undecided[id] = count
	}
	queue := make([]sharedResultID, 0, len(kept))
	for id, count := range undecided {
		if count == 0 {
			queue = append(queue, id)
		}
	}
	slices.Sort(queue)
	order := make([]sharedResultID, 0, len(kept))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		next := dependents[id]
		slices.Sort(next)
		for _, dependent := range next {
			undecided[dependent]--
			if undecided[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	return order
}
