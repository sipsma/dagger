package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine/slog"
)

func (c *Cache) persistCurrentState(ctx context.Context) error {
	if c.sqlDB == nil || c.pdb == nil {
		return nil
	}

	snapshot, err := c.snapshotPersistState(ctx)
	if err != nil {
		return err
	}
	return c.applyPersistStateSnapshot(ctx, snapshot)
}

//nolint:gocyclo // intrinsically long state machine; refactoring would hurt clarity
func (c *Cache) snapshotPersistState(ctx context.Context) (persistStateSnapshot, error) {
	var snapshot persistStateSnapshot

	c.egraphMu.RLock()

	addEqClassID := func(eqClassIDs map[eqClassID]struct{}, eqID eqClassID) {
		eqID = c.findEqClassLocked(eqID)
		if eqID == 0 {
			return
		}
		eqClassIDs[eqID] = struct{}{}
	}

	eqClassIDs := make(map[eqClassID]struct{})

	for eqID := range c.eqClassToDigests {
		addEqClassID(eqClassIDs, eqID)
	}
	for eqID := range c.eqClassExtraDigests {
		addEqClassID(eqClassIDs, eqID)
	}

	termIDs := make([]egraphTermID, 0, len(c.egraphTerms))
	for termID := range c.egraphTerms {
		termIDs = append(termIDs, termID)
	}
	slices.Sort(termIDs)
	for _, termID := range termIDs {
		term := c.egraphTerms[termID]
		if term == nil {
			continue
		}
		outputEqID := c.findEqClassLocked(term.outputEqID)
		addEqClassID(eqClassIDs, outputEqID)
		inputProvenance := c.termInputProvenance[termID]
		if len(inputProvenance) != len(term.inputEqIDs) {
			c.egraphMu.RUnlock()
			return persistStateSnapshot{}, fmt.Errorf("persist term %d: input provenance len %d does not match input eq IDs len %d", termID, len(inputProvenance), len(term.inputEqIDs))
		}
		inputEqIDs := make([]eqClassID, len(term.inputEqIDs))
		copy(inputEqIDs, term.inputEqIDs)
		for i, inputEqID := range inputEqIDs {
			inputEqID = c.findEqClassLocked(inputEqID)
			inputEqIDs[i] = inputEqID
			addEqClassID(eqClassIDs, inputEqID)
			snapshot.termInputs = append(snapshot.termInputs, persistdb.MirrorTermInput{
				TermID:         int64(termID),
				Position:       int64(i),
				InputEqClassID: int64(inputEqID),
				ProvenanceKind: string(inputProvenance[i]),
			})
		}
		snapshot.terms = append(snapshot.terms, persistdb.MirrorTerm{
			ID:              int64(termID),
			SelfDigest:      term.selfDigest.String(),
			TermDigest:      calcEgraphTermDigest(term.selfDigest, inputEqIDs),
			OutputEqClassID: int64(outputEqID),
		})
	}

	resultIDs := make([]sharedResultID, 0, len(c.resultsByID))
	for resultID := range c.resultsByID {
		resultIDs = append(resultIDs, resultID)
	}
	slices.Sort(resultIDs)

	omittedResultTypes := make(map[sharedResultID]string)
	for _, resultID := range resultIDs {
		if typeName, ok := persistSnapshotNonPersistedObjectType(c.resultsByID[resultID]); ok {
			omittedResultTypes[resultID] = typeName
		}
	}

	for _, resultID := range resultIDs {
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		if _, omitted := omittedResultTypes[resultID]; omitted {
			continue
		}

		depIDs := make([]sharedResultID, 0, len(res.deps))
		for depID := range res.deps {
			depIDs = append(depIDs, depID)
		}
		slices.Sort(depIDs)
		resultDeps := make([]persistdb.MirrorResultDep, 0, len(depIDs))
		for _, depID := range depIDs {
			if typeName, omitted := omittedResultTypes[depID]; omitted {
				c.egraphMu.RUnlock()
				return persistStateSnapshot{}, fmt.Errorf("persist result %d: dependency references non-persisted result %d (%s)", resultID, depID, typeName)
			}
			resultDeps = append(resultDeps, persistdb.MirrorResultDep{
				ParentResultID: int64(resultID),
				DepResultID:    int64(depID),
			})
		}

		outputEqClasses := c.outputEqClassesForResultLocked(resultID)
		outputEqIDs := make([]eqClassID, 0, len(outputEqClasses))
		for outputEqID := range outputEqClasses {
			addEqClassID(eqClassIDs, outputEqID)
			outputEqIDs = append(outputEqIDs, outputEqID)
		}
		slices.Sort(outputEqIDs)
		for _, outputEqID := range outputEqIDs {
			snapshot.resultOutputEqClasses = append(snapshot.resultOutputEqClasses, persistdb.MirrorResultOutputEqClass{
				ResultID:  int64(resultID),
				EqClassID: int64(outputEqID),
			})
		}

		payload := res.loadPayloadState()
		frame := res.loadResultCall()
		if err := c.validatePersistSnapshotResultCallRefsLocked(resultID, frame, omittedResultTypes); err != nil {
			c.egraphMu.RUnlock()
			return persistStateSnapshot{}, err
		}
		var frameSnapshot *ResultCall
		if frame != nil {
			frameSnapshot = frame.clone()
		}
		snapshot.results = append(snapshot.results, persistResultSnapshot{
			resultID:              resultID,
			frame:                 frameSnapshot,
			self:                  payload.self,
			isObject:              payload.isObject,
			hasValue:              payload.hasValue,
			sessionResourceHandle: res.sessionResourceHandle,
			persistedEnvelope:     payload.persistedEnvelope,
			snapshotOwnerLinks:    payload.snapshotOwnerLinks,
			snapshotRemoteChains:  res.loadRemoteSnapshotChains(),
			row: persistdb.MirrorResult{
				ID:                 int64(resultID),
				ExpiresAtUnix:      res.expiresAtUnix,
				CreatedAtUnixNano:  payload.createdAtUnixNano,
				LastUsedAtUnixNano: payload.lastUsedAtUnixNano,
				RecordType:         res.recordType,
				Description:        res.description,
				OriginSourceID:     res.originSourceID,
				OriginResultID:     int64(res.originResultID),
			},
			resultDeps: resultDeps,
		})
	}

	persistedResultIDs := make([]sharedResultID, 0, len(c.persistedEdgesByResult))
	for resultID := range c.persistedEdgesByResult {
		persistedResultIDs = append(persistedResultIDs, resultID)
	}
	slices.Sort(persistedResultIDs)
	for _, resultID := range persistedResultIDs {
		if typeName, omitted := omittedResultTypes[resultID]; omitted {
			c.egraphMu.RUnlock()
			return persistStateSnapshot{}, fmt.Errorf("persisted edge references non-persisted result %d (%s)", resultID, typeName)
		}
		edge := c.persistedEdgesByResult[resultID]
		snapshot.persistedEdges = append(snapshot.persistedEdges, persistdb.MirrorPersistedEdge{
			ResultID:          int64(resultID),
			CreatedAtUnixNano: edge.createdAtUnixNano,
			ExpiresAtUnix:     edge.expiresAtUnix,
			Unpruneable:       edge.unpruneable,
		})
	}

	eqIDs := make([]eqClassID, 0, len(eqClassIDs))
	for eqID := range eqClassIDs {
		eqIDs = append(eqIDs, eqID)
	}
	slices.Sort(eqIDs)
	for _, eqID := range eqIDs {
		snapshot.eqClasses = append(snapshot.eqClasses, persistdb.MirrorEqClass{ID: int64(eqID)})

		digestRows := make(map[string]persistdb.MirrorEqClassDigest, len(c.eqClassToDigests[eqID]))
		for dig := range c.eqClassToDigests[eqID] {
			if dig == "" {
				continue
			}
			digestRows[dig+"\x00"] = persistdb.MirrorEqClassDigest{
				EqClassID: int64(eqID),
				Digest:    dig,
				Label:     "",
			}
		}
		for extra := range c.eqClassExtraDigests[eqID] {
			if extra.Digest == "" {
				continue
			}
			dig := extra.Digest.String()
			digestRows[dig+"\x00"] = persistdb.MirrorEqClassDigest{
				EqClassID: int64(eqID),
				Digest:    dig,
				Label:     "",
			}
			digestRows[dig+"\x01"+extra.Label] = persistdb.MirrorEqClassDigest{
				EqClassID: int64(eqID),
				Digest:    dig,
				Label:     extra.Label,
			}
		}
		rowKeys := make([]string, 0, len(digestRows))
		for key := range digestRows {
			rowKeys = append(rowKeys, key)
		}
		slices.Sort(rowKeys)
		for _, key := range rowKeys {
			snapshot.eqClassDigests = append(snapshot.eqClassDigests, digestRows[key])
		}
	}

	c.egraphMu.RUnlock()

	if c.snapshotManager != nil {
		rows := c.snapshotManager.PersistentMetadataRows()
		for _, row := range rows.SnapshotContent {
			snapshot.snapshotContentLinks = append(snapshot.snapshotContentLinks, persistdb.MirrorSnapshotContentLink{
				SnapshotID: row.SnapshotID,
				Digest:     row.Digest.String(),
			})
		}
		for _, row := range rows.ImportedByBlob {
			snapshot.importedLayerByBlob = append(snapshot.importedLayerByBlob, persistdb.MirrorImportedLayerBlobIndex{
				ParentSnapshotID: row.ParentSnapshotID,
				BlobDigest:       row.BlobDigest.String(),
				SnapshotID:       row.SnapshotID,
			})
		}
		for _, row := range rows.ImportedByDiff {
			snapshot.importedLayerByDiff = append(snapshot.importedLayerByDiff, persistdb.MirrorImportedLayerDiffIndex{
				ParentSnapshotID: row.ParentSnapshotID,
				DiffID:           row.DiffID.String(),
				SnapshotID:       row.SnapshotID,
			})
		}
	}

	for i := range snapshot.results {
		resultSnapshot := &snapshot.results[i]
		if resultSnapshot.frame == nil {
			if resultSnapshot.self == nil || resultSnapshot.self.Type() == nil || resultSnapshot.self.Type().Name() != "Query" {
				return persistStateSnapshot{}, fmt.Errorf("persist result %d: missing result call frame", resultSnapshot.resultID)
			}
		}

		encoding, err := c.persistResultEnvelope(ctx, resultSnapshot)
		switch {
		case errors.Is(err, ErrPersistStateNotReady):
			return persistStateSnapshot{}, err
		case err != nil:
			return persistStateSnapshot{}, fmt.Errorf("persist result %d envelope: %w", resultSnapshot.resultID, err)
		}

		if err := validatePersistSnapshotEnvelopeRefs(&encoding.Envelope, omittedResultTypes); err != nil {
			return persistStateSnapshot{}, fmt.Errorf("persist result %d payload refs: %w", resultSnapshot.resultID, err)
		}

		payload, err := json.Marshal(encoding.Envelope)
		if err != nil {
			return persistStateSnapshot{}, fmt.Errorf("persist result %d payload JSON: %w", resultSnapshot.resultID, err)
		}
		if resultSnapshot.frame != nil {
			callFrameJSON, err := json.Marshal(resultSnapshot.frame)
			if err != nil {
				return persistStateSnapshot{}, fmt.Errorf("persist result %d call frame JSON: %w", resultSnapshot.resultID, err)
			}
			resultSnapshot.row.CallFrameJSON = string(callFrameJSON)
		}
		resultSnapshot.row.SelfPayload = payload
		resultSnapshot.resultSnapshotLinks = resultSnapshotLinkRows(resultSnapshot.resultID, encoding.SnapshotLinks)
		chainRows, layerRows := resultSnapshotChainRows(resultSnapshot.resultID, resultSnapshot.snapshotRemoteChains)
		resultSnapshot.resultSnapshotChains = append(resultSnapshot.resultSnapshotChains, chainRows...)
		snapshot.snapshotChainLayers = append(snapshot.snapshotChainLayers, layerRows...)
	}
	return snapshot, nil
}

func persistSnapshotNonPersistedObjectType(res *sharedResult) (string, bool) {
	if res == nil {
		return "", false
	}
	payload := res.loadPayloadState()
	if !payload.hasValue || !payload.isObject || payload.self == nil {
		return "", false
	}
	nonPersisted, ok := payload.self.(NonPersistedObject)
	if !ok {
		return "", false
	}
	typeName := "<unknown>"
	if typ := nonPersisted.Type(); typ != nil && typ.Name() != "" {
		typeName = typ.Name()
	}
	return typeName, true
}

func (c *Cache) validatePersistSnapshotResultCallRefsLocked(resultID sharedResultID, frame *ResultCall, omittedResultTypes map[sharedResultID]string) error {
	if len(omittedResultTypes) == 0 {
		return nil
	}
	if err := c.cachemoneyWalkResultCallRefsLocked(frame, func(ref *ResultCallRef) error {
		if ref == nil || ref.ResultID == 0 {
			return nil
		}
		refID := sharedResultID(ref.ResultID)
		if typeName, omitted := omittedResultTypes[refID]; omitted {
			return fmt.Errorf("references non-persisted result %d (%s)", refID, typeName)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("persist result %d call frame refs: %w", resultID, err)
	}
	return nil
}

func validatePersistSnapshotEnvelopeRefs(env *PersistedResultEnvelope, omittedResultTypes map[sharedResultID]string) error {
	if len(omittedResultTypes) == 0 {
		return nil
	}
	resultIDs, err := cachemoneyPersistedEnvelopeResultIDs(env)
	if err != nil {
		return err
	}
	for _, id := range resultIDs {
		refID := sharedResultID(id)
		if typeName, omitted := omittedResultTypes[refID]; omitted {
			return fmt.Errorf("references non-persisted result %d (%s)", refID, typeName)
		}
	}
	return nil
}

//nolint:gocyclo // intrinsically long state machine; refactoring would hurt clarity
func (c *Cache) applyPersistStateSnapshot(ctx context.Context, snapshot persistStateSnapshot) error {
	if c.sqlDB == nil || c.pdb == nil {
		return nil
	}

	tx, err := c.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin persistence mirror tx: %w", err)
	}
	q := c.pdb.WithTx(tx)
	if err := q.ClearMirrorState(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear mirror state: %w", err)
	}

	for _, row := range snapshot.eqClasses {
		if err := q.InsertMirrorEqClass(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert eq_class %d: %w", row.ID, err)
		}
	}
	for _, row := range snapshot.eqClassDigests {
		if err := q.InsertMirrorEqClassDigest(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert eq_class_digest (%d,%s,%s): %w", row.EqClassID, row.Digest, row.Label, err)
		}
	}
	for _, result := range snapshot.results {
		if err := q.InsertMirrorResult(ctx, result.row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert result %d: %w", result.resultID, err)
		}
	}
	for _, row := range snapshot.terms {
		if err := q.InsertMirrorTerm(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert term %d: %w", row.ID, err)
		}
	}
	for _, row := range snapshot.termInputs {
		if err := q.InsertMirrorTermInput(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert term_input (%d,%d): %w", row.TermID, row.Position, err)
		}
	}
	for _, row := range snapshot.resultOutputEqClasses {
		if err := q.InsertMirrorResultOutputEqClass(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert result_output_eq_class (%d,%d): %w", row.ResultID, row.EqClassID, err)
		}
	}
	for _, row := range snapshot.persistedEdges {
		if err := q.InsertMirrorPersistedEdge(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert persisted_edge (%d): %w", row.ResultID, err)
		}
	}
	for _, result := range snapshot.results {
		for _, row := range result.resultDeps {
			if err := q.InsertMirrorResultDep(ctx, row); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("insert result_dep (%d,%d): %w", row.ParentResultID, row.DepResultID, err)
			}
		}
		for _, row := range result.resultSnapshotLinks {
			if err := q.InsertMirrorResultSnapshotLink(ctx, row); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("insert result_snapshot_link (%d,%s,%s): %w", row.ResultID, row.RefKey, row.Role, err)
			}
		}
		for _, row := range result.resultSnapshotChains {
			if err := q.InsertMirrorResultSnapshotChain(ctx, row); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("insert result_snapshot_chain (%d,%s,%s): %w", row.ResultID, row.Role, row.ChainID, err)
			}
		}
	}
	snapshotChainLayers, err := normalizeSnapshotChainLayerRows(snapshot.snapshotChainLayers)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, row := range snapshotChainLayers {
		if err := q.InsertMirrorSnapshotChainLayer(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert snapshot_chain_layer (%s,%d): %w", row.ChainID, row.Position, err)
		}
	}
	for _, row := range snapshot.snapshotContentLinks {
		if err := q.InsertMirrorSnapshotContentLink(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert snapshot_content_link (%s,%s): %w", row.SnapshotID, row.Digest, err)
		}
	}
	for _, row := range snapshot.importedLayerByBlob {
		if err := q.InsertMirrorImportedLayerBlobIndex(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert imported_layer_blob_index (%s,%s,%s): %w", row.ParentSnapshotID, row.BlobDigest, row.SnapshotID, err)
		}
	}
	for _, row := range snapshot.importedLayerByDiff {
		if err := q.InsertMirrorImportedLayerDiffIndex(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert imported_layer_diff_index (%s,%s,%s): %w", row.ParentSnapshotID, row.DiffID, row.SnapshotID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit persistence mirror tx: %w", err)
	}
	return nil
}

func resultSnapshotLinkRows(resultID sharedResultID, links []PersistedSnapshotRefLink) []persistdb.MirrorResultSnapshotLink {
	if len(links) == 0 {
		return nil
	}
	links = slices.Clone(links)
	slices.SortFunc(links, func(a, b PersistedSnapshotRefLink) int {
		switch {
		case a.RefKey < b.RefKey:
			return -1
		case a.RefKey > b.RefKey:
			return 1
		case a.Role < b.Role:
			return -1
		case a.Role > b.Role:
			return 1
		default:
			return 0
		}
	})
	rows := make([]persistdb.MirrorResultSnapshotLink, 0, len(links))
	for _, link := range links {
		rows = append(rows, persistdb.MirrorResultSnapshotLink{
			ResultID: int64(resultID),
			RefKey:   link.RefKey,
			Role:     link.Role,
		})
	}
	return rows
}

func resultSnapshotChainRows(resultID sharedResultID, chains []PersistedSnapshotChain) ([]persistdb.MirrorResultSnapshotChain, []persistdb.MirrorSnapshotChainLayer) {
	if len(chains) == 0 {
		return nil, nil
	}
	chains = clonePersistedSnapshotChains(chains)
	slices.SortFunc(chains, func(a, b PersistedSnapshotChain) int {
		switch {
		case a.Role < b.Role:
			return -1
		case a.Role > b.Role:
			return 1
		case a.ChainID < b.ChainID:
			return -1
		case a.ChainID > b.ChainID:
			return 1
		default:
			return 0
		}
	})

	chainRows := make([]persistdb.MirrorResultSnapshotChain, 0, len(chains))
	layerRowsByKey := map[string]persistdb.MirrorSnapshotChainLayer{}
	for _, chain := range chains {
		chainRows = append(chainRows, persistdb.MirrorResultSnapshotChain{
			ResultID: int64(resultID),
			Role:     chain.Role,
			ChainID:  chain.ChainID,
		})
		for pos, layer := range chain.Layers {
			key := fmt.Sprintf("%s\x00%d", chain.ChainID, pos)
			layerRowsByKey[key] = persistdb.MirrorSnapshotChainLayer{
				ChainID:        chain.ChainID,
				Position:       int64(pos),
				DiffID:         layer.DiffID,
				BlobDigest:     layer.BlobDigest,
				Size:           layer.Size,
				MediaType:      layer.MediaType,
				DescriptorJSON: string(layer.DescriptorJSON),
			}
		}
	}

	layerKeys := make([]string, 0, len(layerRowsByKey))
	for key := range layerRowsByKey {
		layerKeys = append(layerKeys, key)
	}
	slices.Sort(layerKeys)
	layerRows := make([]persistdb.MirrorSnapshotChainLayer, 0, len(layerKeys))
	for _, key := range layerKeys {
		layerRows = append(layerRows, layerRowsByKey[key])
	}
	return chainRows, layerRows
}

type snapshotChainLayerKey struct {
	chainID  string
	position int64
}

func normalizeSnapshotChainLayerRows(rows []persistdb.MirrorSnapshotChainLayer) ([]persistdb.MirrorSnapshotChainLayer, error) {
	if len(rows) < 2 {
		return rows, nil
	}
	rowsByKey := make(map[snapshotChainLayerKey]persistdb.MirrorSnapshotChainLayer, len(rows))
	for _, row := range rows {
		key := snapshotChainLayerKey{
			chainID:  row.ChainID,
			position: row.Position,
		}
		existing, ok := rowsByKey[key]
		if ok {
			if existing != row {
				return nil, fmt.Errorf("conflicting snapshot_chain_layer (%s,%d)", row.ChainID, row.Position)
			}
			continue
		}
		rowsByKey[key] = row
	}

	keys := make([]snapshotChainLayerKey, 0, len(rowsByKey))
	for key := range rowsByKey {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b snapshotChainLayerKey) int {
		switch {
		case a.chainID < b.chainID:
			return -1
		case a.chainID > b.chainID:
			return 1
		case a.position < b.position:
			return -1
		case a.position > b.position:
			return 1
		default:
			return 0
		}
	})

	normalized := make([]persistdb.MirrorSnapshotChainLayer, 0, len(keys))
	for _, key := range keys {
		normalized = append(normalized, rowsByKey[key])
	}
	return normalized, nil
}

func (c *Cache) persistResultEnvelope(ctx context.Context, snapshot *persistResultSnapshot) (PersistedResultEncoding, error) {
	if snapshot != nil && snapshot.persistedEnvelope != nil {
		return PersistedResultEncoding{
			Envelope:      *snapshot.persistedEnvelope,
			SnapshotLinks: snapshot.snapshotOwnerLinks,
		}, nil
	}
	if snapshot == nil || !snapshot.hasValue {
		return PersistedResultEncoding{
			Envelope: PersistedResultEnvelope{
				Version: 1,
				Kind:    persistedResultKindNull,
			},
		}, nil
	}
	if snapshot.self == nil {
		return PersistedResultEncoding{
			Envelope: PersistedResultEnvelope{
				Version:               2,
				Kind:                  persistedResultKindNull,
				ResultID:              uint64(snapshot.resultID),
				SessionResourceHandle: snapshot.sessionResourceHandle,
			},
		}, nil
	}
	if snapshot.frame == nil {
		if snapshot.self == nil || snapshot.self.Type() == nil || snapshot.self.Type().Name() != "Query" {
			return PersistedResultEncoding{}, fmt.Errorf("result has no call frame and no persisted envelope")
		}
		shared := &sharedResult{
			self:                  snapshot.self,
			isObject:              snapshot.isObject,
			hasValue:              snapshot.hasValue,
			id:                    snapshot.resultID,
			sessionResourceHandle: snapshot.sessionResourceHandle,
		}
		return DefaultPersistedSelfCodec.EncodeResult(context.WithoutCancel(ctx), c, Result[Typed]{shared: shared})
	}
	shared := &sharedResult{
		self:                  snapshot.self,
		isObject:              snapshot.isObject,
		hasValue:              snapshot.hasValue,
		id:                    snapshot.resultID,
		sessionResourceHandle: snapshot.sessionResourceHandle,
	}
	shared.storeResultCall(snapshot.frame)
	persistCtx := context.WithoutCancel(ctx)
	persistCtx = ContextWithCall(persistCtx, snapshot.frame)
	env, err := DefaultPersistedSelfCodec.EncodeResult(persistCtx, c, Result[Typed]{shared: shared})
	if err != nil {
		field := snapshot.frame.Field
		if field == "" {
			field = snapshot.frame.SyntheticOp
		}
		typeName := ""
		if snapshot.frame.Type != nil {
			typeName = snapshot.frame.Type.NamedType
		}
		selfType := ""
		if snapshot.self != nil {
			selfType = snapshot.self.Type().Name()
		}
		slog.Error(
			"persist result envelope encode failed",
			"resultID", snapshot.resultID,
			"recordType", snapshot.row.RecordType,
			"description", snapshot.row.Description,
			"field", field,
			"kind", snapshot.frame.Kind,
			"typeName", typeName,
			"selfType", selfType,
			"hasValue", snapshot.hasValue,
			"sessionResourceHandle", snapshot.sessionResourceHandle,
			"err", err,
		)
		return PersistedResultEncoding{}, err
	}
	return env, nil
}
