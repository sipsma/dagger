package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

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
	if err := c.applyPersistStateSnapshot(ctx, snapshot); err != nil {
		return err
	}

	// The per-boot result counts are the self-check that importing and
	// re-exporting a store adds no rows: a flush of an untouched boot must
	// show total == imported with nothing executed.
	counts := map[string]int64{
		persistdb.MetaKeyResultsTotal:            int64(len(snapshot.results)),
		persistdb.MetaKeyResultsImported:         c.importedResultCount,
		persistdb.MetaKeyResultsExecutedThisBoot: c.freshResultCount.Load(),
	}
	for key, value := range counts {
		if err := c.pdb.UpsertMeta(ctx, key, strconv.FormatInt(value, 10)); err != nil {
			return fmt.Errorf("write %s metadata: %w", key, err)
		}
	}
	if err := c.pdb.UpsertMeta(ctx, persistdb.MetaKeyMaxResultID, strconv.FormatUint(uint64(snapshot.maxAllocatedResultID), 10)); err != nil {
		return fmt.Errorf("write %s metadata: %w", persistdb.MetaKeyMaxResultID, err)
	}
	return nil
}

func (c *Cache) snapshotPersistState(ctx context.Context) (persistStateSnapshot, error) {
	snapshot, err := c.copyOutPersistState()
	if err != nil {
		return persistStateSnapshot{}, err
	}
	for i := range snapshot.results {
		if err := c.encodeSnapshotResultRow(ctx, &snapshot.results[i]); err != nil {
			return persistStateSnapshot{}, err
		}
	}
	return snapshot, nil
}

// copyOutPersistState copies the retained in-memory cache state into a
// detached snapshot, holding the graph lock only for the copy. Result rows
// come back un-encoded; encodeSnapshotResultRow fills each row's envelope
// bytes. Local flush encodes every row and fails on the first error; the
// bundle writer encodes its filtered closure and degrades per-row.
//
//nolint:gocyclo // intrinsically long state machine; refactoring would hurt clarity
func (c *Cache) copyOutPersistState() (persistStateSnapshot, error) {
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
			OutputEqClassID: int64(outputEqID),
		})
	}

	resultIDs := make([]sharedResultID, 0, len(c.resultsByID))
	for resultID := range c.resultsByID {
		resultIDs = append(resultIDs, resultID)
	}
	slices.Sort(resultIDs)
	for _, resultID := range resultIDs {
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		// A dropped result's servability is over; its row must not outlive
		// this boot.
		if res.dropped {
			continue
		}

		depIDs := make([]sharedResultID, 0, len(res.deps))
		for depID := range res.deps {
			depIDs = append(depIDs, depID)
		}
		slices.Sort(depIDs)
		resultDeps := make([]persistdb.MirrorResultDep, 0, len(depIDs))
		for _, depID := range depIDs {
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
		// Locally-minted rows that never went through publication minting
		// (persistence-disabled windows cannot occur here: the store UUID is
		// set before any allocation) still get their deterministic local
		// origin — identical to what publication would have minted.
		origin := res.origin
		if origin.isZero() {
			origin = resultOrigin{storeUUID: c.storeUUID, resultID: uint64(resultID)}
		}
		snapshot.results = append(snapshot.results, persistResultSnapshot{
			resultID: resultID,
			origin: persistdb.MirrorResultOrigin{
				ResultID:        int64(resultID),
				OriginStoreUUID: origin.storeUUID,
				OriginResultID:  int64(origin.resultID),
			},
			frame:                 res.loadResultCall().clone(),
			self:                  payload.self,
			isObject:              payload.isObject,
			realized:              payload.realized,
			sessionResourceHandle: res.sessionResourceHandle,
			persistedEnvelope:     payload.persistedEnvelope,
			snapshotOwnerLinks:    payload.snapshotOwnerLinks,
			lazyFragment:          res.loadLazyFragment(),
			row: persistdb.MirrorResult{
				ID:                 int64(resultID),
				ExpiresAtUnix:      res.expiresAtUnix,
				CreatedAtUnixNano:  payload.createdAtUnixNano,
				LastUsedAtUnixNano: payload.lastUsedAtUnixNano,
				RecordType:         res.recordType,
				Description:        res.description,
			},
			resultDeps: resultDeps,
		})
	}

	snapshot.maxAllocatedResultID = c.maxAllocatedResultID

	persistedResultIDs := make([]sharedResultID, 0, len(c.persistedEdgesByResult))
	for resultID := range c.persistedEdgesByResult {
		persistedResultIDs = append(persistedResultIDs, resultID)
	}
	slices.Sort(persistedResultIDs)
	for _, resultID := range persistedResultIDs {
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

	return snapshot, nil
}

// encodeSnapshotResultRow fills one copied-out result row's persisted
// bytes: the envelope payload, the frame JSON, and the snapshot link rows.
func (c *Cache) encodeSnapshotResultRow(ctx context.Context, resultSnapshot *persistResultSnapshot) error {
	if resultSnapshot.frame == nil {
		if resultSnapshot.self == nil || resultSnapshot.self.Type() == nil || resultSnapshot.self.Type().Name() != "Query" {
			return fmt.Errorf("persist result %d: missing result call frame", resultSnapshot.resultID)
		}
	}

	encoding, err := c.persistResultEnvelope(ctx, resultSnapshot)
	switch {
	case errors.Is(err, ErrPersistStateNotReady):
		return err
	case err != nil:
		return fmt.Errorf("persist result %d envelope: %w", resultSnapshot.resultID, err)
	}

	payload, err := json.Marshal(encoding.Envelope)
	if err != nil {
		return fmt.Errorf("persist result %d payload JSON: %w", resultSnapshot.resultID, err)
	}
	if resultSnapshot.frame != nil {
		callFrameJSON, err := json.Marshal(resultSnapshot.frame)
		if err != nil {
			return fmt.Errorf("persist result %d call frame JSON: %w", resultSnapshot.resultID, err)
		}
		resultSnapshot.row.CallFrameJSON = string(callFrameJSON)
	}
	resultSnapshot.row.SelfPayload = payload
	resultSnapshot.resultSnapshotLinks = resultSnapshotLinkRows(resultSnapshot.resultID, encoding.SnapshotLinks)
	return nil
}

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
	if err := insertPersistStateSnapshotRows(ctx, q, snapshot); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit persistence mirror tx: %w", err)
	}
	return nil
}

// insertPersistStateSnapshotRows writes a snapshot's rows through the given
// query handle. It is the one row-writing path shared by local flush and
// the bundle writer (R9's one-encoding rule): the bundle writer feeds it a
// closure-filtered snapshot against a fresh metadata DB.
//
//nolint:gocyclo // intrinsically long state machine; refactoring would hurt clarity
func insertPersistStateSnapshotRows(ctx context.Context, q *persistdb.Queries, snapshot persistStateSnapshot) error {
	for _, row := range snapshot.eqClasses {
		if err := q.InsertMirrorEqClass(ctx, row); err != nil {
			return fmt.Errorf("insert eq_class %d: %w", row.ID, err)
		}
	}
	for _, row := range snapshot.eqClassDigests {
		if err := q.InsertMirrorEqClassDigest(ctx, row); err != nil {
			return fmt.Errorf("insert eq_class_digest (%d,%s,%s): %w", row.EqClassID, row.Digest, row.Label, err)
		}
	}
	for _, result := range snapshot.results {
		if err := q.InsertMirrorResult(ctx, result.row); err != nil {
			return fmt.Errorf("insert result %d: %w", result.resultID, err)
		}
	}
	for _, row := range snapshot.terms {
		if err := q.InsertMirrorTerm(ctx, row); err != nil {
			return fmt.Errorf("insert term %d: %w", row.ID, err)
		}
	}
	for _, row := range snapshot.termInputs {
		if err := q.InsertMirrorTermInput(ctx, row); err != nil {
			return fmt.Errorf("insert term_input (%d,%d): %w", row.TermID, row.Position, err)
		}
	}
	for _, row := range snapshot.resultOutputEqClasses {
		if err := q.InsertMirrorResultOutputEqClass(ctx, row); err != nil {
			return fmt.Errorf("insert result_output_eq_class (%d,%d): %w", row.ResultID, row.EqClassID, err)
		}
	}
	for _, row := range snapshot.persistedEdges {
		if err := q.InsertMirrorPersistedEdge(ctx, row); err != nil {
			return fmt.Errorf("insert persisted_edge (%d): %w", row.ResultID, err)
		}
	}
	for _, result := range snapshot.results {
		if err := q.InsertMirrorResultOrigin(ctx, result.origin); err != nil {
			return fmt.Errorf("insert result_origin (%d,%s,%d): %w", result.origin.ResultID, result.origin.OriginStoreUUID, result.origin.OriginResultID, err)
		}
		for _, row := range result.resultDeps {
			if err := q.InsertMirrorResultDep(ctx, row); err != nil {
				return fmt.Errorf("insert result_dep (%d,%d): %w", row.ParentResultID, row.DepResultID, err)
			}
		}
		for _, row := range result.resultSnapshotLinks {
			if err := q.InsertMirrorResultSnapshotLink(ctx, row); err != nil {
				return fmt.Errorf("insert result_snapshot_link (%d,%s,%s): %w", row.ResultID, row.RefKey, row.Role, err)
			}
		}
	}
	for _, row := range snapshot.snapshotContentLinks {
		if err := q.InsertMirrorSnapshotContentLink(ctx, row); err != nil {
			return fmt.Errorf("insert snapshot_content_link (%s,%s): %w", row.SnapshotID, row.Digest, err)
		}
	}
	for _, row := range snapshot.importedLayerByBlob {
		if err := q.InsertMirrorImportedLayerBlobIndex(ctx, row); err != nil {
			return fmt.Errorf("insert imported_layer_blob_index (%s,%s,%s): %w", row.ParentSnapshotID, row.BlobDigest, row.SnapshotID, err)
		}
	}
	for _, row := range snapshot.importedLayerByDiff {
		if err := q.InsertMirrorImportedLayerDiffIndex(ctx, row); err != nil {
			return fmt.Errorf("insert imported_layer_diff_index (%s,%s,%s): %w", row.ParentSnapshotID, row.DiffID, row.SnapshotID, err)
		}
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

func (c *Cache) persistResultEnvelope(ctx context.Context, snapshot *persistResultSnapshot) (PersistedResultEncoding, error) {
	if snapshot != nil && snapshot.persistedEnvelope != nil {
		return PersistedResultEncoding{
			Envelope:      *snapshot.persistedEnvelope,
			SnapshotLinks: snapshot.snapshotOwnerLinks,
		}, nil
	}
	if snapshot == nil || !snapshot.realized {
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
			materialization:       materializationState{realized: snapshot.realized},
			id:                    snapshot.resultID,
			sessionResourceHandle: snapshot.sessionResourceHandle,
		}
		return DefaultPersistedSelfCodec.EncodeResult(context.WithoutCancel(ctx), c, Result[Typed]{shared: shared})
	}
	shared := &sharedResult{
		self:                  snapshot.self,
		isObject:              snapshot.isObject,
		materialization:       materializationState{realized: snapshot.realized},
		id:                    snapshot.resultID,
		sessionResourceHandle: snapshot.sessionResourceHandle,
	}
	shared.storeResultCall(snapshot.frame)
	persistCtx := context.WithoutCancel(ctx)
	persistCtx = ContextWithCall(persistCtx, snapshot.frame)
	env, err := DefaultPersistedSelfCodec.EncodeResult(persistCtx, c, Result[Typed]{shared: shared})
	if err == nil {
		// A realized value's recipe was destroyed by realization; the
		// fragment captured at publication takes its place on the envelope.
		// Values still carrying live deferred work serialized it during
		// encode above.
		if len(env.Envelope.LazyJSON) == 0 && snapshot.lazyFragment != nil {
			env.Envelope.LazyKind = snapshot.lazyFragment.Kind
			env.Envelope.LazyJSON = snapshot.lazyFragment.JSON
		}
		if len(env.Envelope.LazyJSON) == 0 {
			if hl, ok := snapshot.self.(HasLazyEvaluation); ok && hl.LazyEvalFunc() != nil {
				err = fmt.Errorf("%w: result %d has pending deferred work but no lazy fragment to persist", ErrPersistStateNotReady, snapshot.resultID)
			}
		}
	}
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
			"realized", snapshot.realized,
			"sessionResourceHandle", snapshot.sessionResourceHandle,
			"err", err,
		)
		return PersistedResultEncoding{}, err
	}
	return env, nil
}
