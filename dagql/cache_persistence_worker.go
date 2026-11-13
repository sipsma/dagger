package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine/slog"
)

// Checkpoint saves a blob-backed cache, the Cloud's, without closing it: the
// same save as Close's, the closure of the stored roots in one transaction,
// with each value's stored parts. A crash of the process during a save keeps
// the previous one, and a blob-backed cache restores the last save however
// its process ended (NewCache). Its store is written with synchronous=FULL
// (prepareCacheDBs), so an operating-system crash or a power loss leaves it
// whole too, as of its last commit. Only a blob-backed cache checkpoints
// while it runs: it never decodes a value, so its save copies stored records
// as they are. It writes the whole cache; WriteBehind writes what changed,
// and a Checkpoint leaves the writer behind going on from what it saved.
//
// SQLite writes a save to the write-ahead log first, so the log grows to
// about the store's size, and SQLite then reuses the file without shrinking
// it. So after the save, Checkpoint copies the log into the database, which
// syncs it, and truncates the log: between saves, the store takes its own
// size on disk, not twice that. A checkpoint that another connection keeps
// from completing, with a lock it holds, leaves the log as it is: the save
// stands, and a later one truncates it.
func (c *Cache) Checkpoint(ctx context.Context) error {
	if !c.blobBacked {
		return errors.New("checkpoint: the cache has no blob store")
	}
	op, err := c.beginCacheOperation()
	if err != nil {
		return err
	}
	defer op.finish(false)
	if c.sqlDB == nil || c.pdb == nil {
		return errors.New("checkpoint: the cache has no persistence database")
	}
	c.checkpointMu.Lock()
	defer c.checkpointMu.Unlock()
	if err := c.persistCurrentState(ctx); err != nil {
		return err
	}
	var busy, logFrames, copied int
	if err := c.sqlDB.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &copied); err != nil {
		return fmt.Errorf("checkpoint: saved, but truncating the write-ahead log failed: %w", err)
	}
	if busy != 0 {
		slog.Warn("dagql checkpoint saved, but the write-ahead log is in use and stays at its size", "frames", logFrames, "copied", copied)
	}
	return nil
}

// persistOnClose saves the cache as it closes: an engine's whole, a
// blob-backed cache's by writes behind until no marks are left. Close runs
// it once nothing else uses the cache.
func (c *Cache) persistOnClose(ctx context.Context) error {
	if !c.blobBacked {
		return c.persistCurrentState(ctx)
	}
	if c.sqlDB == nil || c.pdb == nil {
		return nil
	}
	c.checkpointMu.Lock()
	defer c.checkpointMu.Unlock()
	for {
		report, err := c.writeBehind(ctx)
		if err != nil || !report.More {
			return err
		}
	}
}

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
	if snapshot.behind != nil {
		// The store now holds the snapshot: the writer behind goes on from
		// it, and the marks made since it wait for the next write.
		c.behind = snapshot.behind
	}
	step := c.lockGraphAs(ctx, "dagql.persist")
	c.persistedResults = len(snapshot.results)
	c.unlockGraphAs(step)
	return nil
}

// snapshotPersistState copies, under egraphMu for reading, what the save
// writes. A Checkpoint runs it beside other readers, so it reads class roots
// without compressing their paths, which writes. Its rows come from the same
// helpers the writer behind uses for one entry, class or term at a time
// (persistResultRowsLocked, persistEqClassRowsLocked, persistTermRowsLocked),
// so the two write the same rows. It doesn't encode results' rows:
// applyPersistStateSnapshot encodes each as it writes it.
func (c *Cache) snapshotPersistState(ctx context.Context) (persistStateSnapshot, error) {
	var snapshot persistStateSnapshot

	step := c.readLockGraphAs(ctx, "dagql.persist.snapshot")
	snapshot.nextResultID = c.nextSharedResultID
	selectedResultIDs, persistedRootIDs, invalidResultIDs := c.persistedRootClosureLocked()

	eqClassIDs := make(map[eqClassID]struct{})
	retainedOutputEqClassIDs := make(map[eqClassID]struct{})

	resultIDs := slices.Sorted(maps.Keys(selectedResultIDs))
	for _, resultID := range resultIDs {
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		rows, err := c.persistResultRowsLocked(res, func(id sharedResultID) bool {
			_, selected := selectedResultIDs[id]
			return selected
		})
		if err != nil {
			c.runlockGraphAs(step)
			return persistStateSnapshot{}, err
		}
		for _, row := range rows.outputs {
			eqID := eqClassID(row.EqClassID)
			retainedOutputEqClassIDs[eqID] = struct{}{}
			eqClassIDs[eqID] = struct{}{}
		}
		snapshot.resultOutputEqClasses = append(snapshot.resultOutputEqClasses, rows.outputs...)
		snapshot.results = append(snapshot.results, rows.result)
		snapshot.resultCalls = append(snapshot.resultCalls, rows.calls...)
		snapshot.resultTaught = append(snapshot.resultTaught, rows.taught...)
	}

	termIDs := slices.Sorted(maps.Keys(c.egraphTerms))
	for _, termID := range termIDs {
		term := c.egraphTerms[termID]
		if term == nil {
			continue
		}
		if _, retained := retainedOutputEqClassIDs[c.eqClassRootLocked(term.outputEqID)]; !retained {
			continue
		}
		rows, err := c.persistTermRowsLocked(termID, term)
		if err != nil {
			c.runlockGraphAs(step)
			return persistStateSnapshot{}, err
		}
		eqClassIDs[eqClassID(rows.term.OutputEqClassID)] = struct{}{}
		for _, input := range rows.inputs {
			if input.InputEqClassID != 0 {
				eqClassIDs[eqClassID(input.InputEqClassID)] = struct{}{}
			}
		}
		snapshot.terms = append(snapshot.terms, rows.term)
		snapshot.termInputs = append(snapshot.termInputs, rows.inputs...)
	}

	for _, resultID := range slices.Sorted(maps.Keys(persistedRootIDs)) {
		snapshot.persistedEdges = append(snapshot.persistedEdges, c.persistEdgeRowLocked(resultID))
	}

	for _, eqID := range slices.Sorted(maps.Keys(eqClassIDs)) {
		class, digests := c.persistEqClassRowsLocked(eqID)
		snapshot.eqClasses = append(snapshot.eqClasses, class)
		snapshot.eqClassDigests = append(snapshot.eqClassDigests, digests...)
	}
	if c.blobBacked {
		snapshot.behind = c.newWriteBehindStateLocked(selectedResultIDs, persistedRootIDs, invalidResultIDs)
	}

	c.runlockGraphAs(step)

	if c.snapshotManager != nil {
		rows := c.snapshotManager.PersistentMetadataRows()
		for _, row := range rows.ContentHashes {
			snapshot.snapshotContentHashes = append(snapshot.snapshotContentHashes, persistdb.MirrorSnapshotContentHash{
				SnapshotID: row.SnapshotID,
				Data:       row.Data,
			})
		}
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

// persistResultRows are the rows a save writes for one entry, but its
// edge's: its result row (encoded later, encodePersistedResultRow), with its
// dependencies, its output classes and its kept calls.
type persistResultRows struct {
	result  persistResultSnapshot
	outputs []persistdb.MirrorResultOutputEqClass
	calls   []persistdb.MirrorResultCall
	taught  []persistdb.MirrorResultTaught
}

// persistResultRowsLocked copies the rows a save writes for res. Each of its
// dependencies must be saved too (saved), or it fails. Requires egraphMu for
// reading.
func (c *Cache) persistResultRowsLocked(res *sharedResult, saved func(sharedResultID) bool) (persistResultRows, error) {
	resultID := res.id
	var out persistResultRows

	depIDs := slices.Sorted(maps.Keys(res.deps))
	resultDeps := make([]persistdb.MirrorResultDep, 0, len(depIDs))
	for _, depID := range depIDs {
		if !saved(depID) {
			return persistResultRows{}, fmt.Errorf("persist result %d: dependency %d is outside persisted root closure", resultID, depID)
		}
		resultDeps = append(resultDeps, persistdb.MirrorResultDep{
			ParentResultID: int64(resultID),
			DepResultID:    int64(depID),
		})
	}

	outputEqIDs := make([]eqClassID, 0, len(c.resultOutputEqClasses[resultID]))
	for outputEqID := range c.outputEqClassRootsLocked(resultID) {
		outputEqID = c.eqClassRootLocked(outputEqID)
		if outputEqID == 0 || slices.Contains(outputEqIDs, outputEqID) {
			continue
		}
		outputEqIDs = append(outputEqIDs, outputEqID)
	}
	slices.Sort(outputEqIDs)
	for _, outputEqID := range outputEqIDs {
		out.outputs = append(out.outputs, persistdb.MirrorResultOutputEqClass{
			ResultID:  int64(resultID),
			EqClassID: int64(outputEqID),
		})
	}

	offers, err := res.pendingOffersLocked()
	if err != nil {
		return persistResultRows{}, err
	}
	cloudKey, cloud := res.cloudHoldingLocked()
	var cloudStored bool
	var cloudExpiresAtUnix int64
	if cloud != nil {
		cloudStored = !cloud.unstored
		cloudExpiresAtUnix = cloud.expiresAtUnix
	}
	payload := res.loadPayloadState()
	if payload.snapshotLinkIntent != nil {
		payload.snapshotOwnerLinks = cloneSnapshotRefLinks(payload.snapshotLinkIntent.Links)
	}
	out.result = persistResultSnapshot{
		resultID:              resultID,
		imported:              res.imported,
		persistDeclined:       res.persistDeclined,
		boundSession:          res.boundSession,
		pendingOffers:         offers,
		storedParts:           res.storedPartsLocked(),
		frame:                 res.loadResultCall().clone(),
		self:                  payload.self,
		isObject:              payload.isObject,
		hasValue:              payload.hasValue,
		sessionResourceHandle: res.sessionResourceHandle,
		persistedEnvelope:     payload.persistedEnvelope,
		stored:                payload.stored,
		snapshotOwnerLinks:    payload.snapshotOwnerLinks,
		row: persistdb.MirrorResult{
			ID:                        int64(resultID),
			ExpiresAtUnix:             res.expiresAtUnix,
			Replacements:              int64(res.replacements),
			Indexed:                   len(res.recipeKeys) > 0,
			CloudHoldingNumber:        int64(cloudKey.Number),
			CloudHoldingStored:        cloudStored,
			CloudHoldingExpiresAtUnix: cloudExpiresAtUnix,
			CreatedAtUnixNano:         payload.createdAtUnixNano,
			LastUsedAtUnixNano:        payload.lastUsedAtUnixNano,
			RecordType:                res.recordType,
			Description:               res.description,
		},
		resultDeps: resultDeps,
	}

	for _, request := range slices.Sorted(maps.Keys(res.calls)) {
		out.calls = append(out.calls, persistdb.MirrorResultCall{
			ResultID:      int64(resultID),
			RequestDigest: request.String(),
			Call:          res.calls[request].encoded,
		})
	}
	for i, taught := range res.taught {
		out.taught = append(out.taught, persistdb.MirrorResultTaught{
			ResultID: int64(resultID),
			Position: int64(i),
			Digest:   taught.String(),
			Given:    i < res.taughtGiven,
		})
	}
	return out, nil
}

// persistEdgeRowLocked is the row a save writes for the persisted edge of
// the entry numbered resultID. Requires egraphMu for reading.
func (c *Cache) persistEdgeRowLocked(resultID sharedResultID) persistdb.MirrorPersistedEdge {
	edge := c.persistedEdgesByResult[resultID]
	return persistdb.MirrorPersistedEdge{
		ResultID:          int64(resultID),
		CreatedAtUnixNano: edge.createdAtUnixNano,
		ExpiresAtUnix:     edge.expiresAtUnix,
		Unpruneable:       edge.unpruneable,
	}
}

// persistTermRows are the rows a save writes for one term: the term, with
// its inputs' class roots.
type persistTermRows struct {
	term   persistdb.MirrorTerm
	inputs []persistdb.MirrorTermInput
}

// persistTermRowsLocked copies the rows a save writes for term, naming its
// classes by their roots. Requires egraphMu for reading.
func (c *Cache) persistTermRowsLocked(termID egraphTermID, term *egraphTerm) (persistTermRows, error) {
	var out persistTermRows
	outputEqID := c.eqClassRootLocked(term.outputEqID)
	inputProvenance := c.termInputProvenance[termID]
	if len(inputProvenance) != len(term.inputEqIDs) {
		return persistTermRows{}, fmt.Errorf("persist term %d: input provenance len %d does not match input eq IDs len %d", termID, len(inputProvenance), len(term.inputEqIDs))
	}
	inputEqIDs := make([]eqClassID, len(term.inputEqIDs))
	for i, inputEqID := range term.inputEqIDs {
		inputEqID = c.eqClassRootLocked(inputEqID)
		inputEqIDs[i] = inputEqID
		out.inputs = append(out.inputs, persistdb.MirrorTermInput{
			TermID:         int64(termID),
			Position:       int64(i),
			InputEqClassID: int64(inputEqID),
			ProvenanceKind: string(inputProvenance[i]),
		})
	}
	out.term = persistdb.MirrorTerm{
		ID:              int64(termID),
		SelfDigest:      term.selfDigest.String(),
		TermDigest:      calcEgraphTermDigest(term.selfDigest, inputEqIDs),
		OutputEqClassID: int64(outputEqID),
	}
	return out, nil
}

// persistEqClassRowsLocked copies the rows a save writes for the class
// rooted at eqID: the class and its digests, an extra digest both bare and
// with its label. Requires egraphMu for reading.
func (c *Cache) persistEqClassRowsLocked(eqID eqClassID) (persistdb.MirrorEqClass, []persistdb.MirrorEqClassDigest) {
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
	digests := make([]persistdb.MirrorEqClassDigest, 0, len(digestRows))
	for _, key := range slices.Sorted(maps.Keys(digestRows)) {
		digests = append(digests, digestRows[key])
	}
	return persistdb.MirrorEqClass{ID: int64(eqID)}, digests
}

// encodePersistedResultRow encodes one entry's saved row from its snapshot:
// its self payload, its call frame and its snapshot links. It reads only the
// snapshot, so a save calls it for each row it writes, whichever rows those
// are. A blob-backed cache's stored record is saved as it is held
// (encodeStoredRow).
func (c *Cache) encodePersistedResultRow(ctx context.Context, resultSnapshot *persistResultSnapshot) error {
	encoding, err := c.persistResultEnvelope(ctx, resultSnapshot)
	switch {
	case errors.Is(err, ErrPersistStateNotReady):
		return err
	case err != nil:
		return fmt.Errorf("persist result %d envelope: %w", resultSnapshot.resultID, err)
	}
	var payload []byte
	if c.blobBacked && resultSnapshot.persistedEnvelope != nil {
		body := resultSnapshot.stored
		if body == nil {
			// A record the body worker hasn't compressed yet is
			// compressed here, as the worker would.
			if body, err = newStoredBody(&sharedResult{id: resultSnapshot.resultID}, resultSnapshot.persistedEnvelope, resultSnapshot.frame); err != nil {
				return fmt.Errorf("persist result %d stored record: %w", resultSnapshot.resultID, err)
			}
		}
		payload, err = encodeStoredRow(&encoding.Envelope, body)
	} else {
		payload, err = json.Marshal(encoding.Envelope)
	}
	if err != nil {
		return fmt.Errorf("persist result %d payload: %w", resultSnapshot.resultID, err)
	}
	switch {
	case resultSnapshot.frame != nil:
		callFrameJSON, err := json.Marshal(resultSnapshot.frame)
		if err != nil {
			return fmt.Errorf("persist result %d call frame JSON: %w", resultSnapshot.resultID, err)
		}
		resultSnapshot.row.CallFrameJSON = string(callFrameJSON)
	case resultSnapshot.stored.holdsFrame():
		// A frame held in the body is written out as it is held: the JSON
		// the frame itself encodes to (storedBody.withFrame).
		callFrameJSON, err := resultSnapshot.stored.frameJSON()
		if err != nil {
			return fmt.Errorf("persist result %d call frame JSON: %w", resultSnapshot.resultID, err)
		}
		resultSnapshot.row.CallFrameJSON = string(callFrameJSON)
	}
	resultSnapshot.row.SelfPayload = payload
	resultSnapshot.resultSnapshotLinks, err = resultSnapshotLinkRows(resultSnapshot.resultID, encoding.SnapshotLinks)
	return err
}

// snapshotPersistedRootClosureLocked returns every clean result reachable from
// a persisted root whose full dependency closure is clean and registered. It
// requires egraphMu for reading.
func (c *Cache) snapshotPersistedRootClosureLocked() (map[sharedResultID]struct{}, map[sharedResultID]struct{}) {
	selected, roots, _ := c.persistedRootClosureLocked()
	return selected, roots
}

// persistedRootClosureLocked is snapshotPersistedRootClosureLocked, with the
// results it left out as invalid: unclean, missing a dependency, or owning
// one that is. It requires egraphMu for reading.
func (c *Cache) persistedRootClosureLocked() (selected, persistedRoots, invalid map[sharedResultID]struct{}) {
	invalid = make(map[sharedResultID]struct{})
	parentsByDependency := make(map[sharedResultID][]sharedResultID)
	invalidQueue := make([]sharedResultID, 0)
	markInvalid := func(resultID sharedResultID) {
		if _, found := invalid[resultID]; found {
			return
		}
		invalid[resultID] = struct{}{}
		invalidQueue = append(invalidQueue, resultID)
	}

	for resultID, res := range c.resultsByID {
		if res == nil {
			continue
		}
		if res.attachmentState() != resultAttachmentClean {
			markInvalid(resultID)
		}
		for depID := range c.ownedResultIDsLocked(res) {
			parentsByDependency[depID] = append(parentsByDependency[depID], resultID)
			if c.resultsByID[depID] == nil {
				markInvalid(resultID)
			}
		}
	}
	for len(invalidQueue) > 0 {
		resultID := invalidQueue[len(invalidQueue)-1]
		invalidQueue = invalidQueue[:len(invalidQueue)-1]
		for _, parentID := range parentsByDependency[resultID] {
			markInvalid(parentID)
		}
	}

	persistedRoots = make(map[sharedResultID]struct{}, len(c.persistedEdgesByResult))
	stack := make([]sharedResultID, 0, len(c.persistedEdgesByResult))
	for resultID := range c.persistedEdgesByResult {
		if c.resultsByID[resultID] == nil {
			continue
		}
		if _, rejected := invalid[resultID]; rejected {
			continue
		}
		persistedRoots[resultID] = struct{}{}
		stack = append(stack, resultID)
	}

	selected = make(map[sharedResultID]struct{})
	for len(stack) > 0 {
		resultID := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, found := selected[resultID]; found {
			continue
		}
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		if _, rejected := invalid[resultID]; rejected {
			continue
		}
		selected[resultID] = struct{}{}
		for depID := range c.ownedResultIDsLocked(res) {
			stack = append(stack, depID)
		}
	}
	return selected, persistedRoots, invalid
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
	if err := q.UpsertMeta(ctx, persistdb.MetaKeyNextResultID, strconv.FormatUint(uint64(snapshot.nextResultID), 10)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("save next result ID: %w", err)
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
	// Each result's row is encoded just before its insert, and its encoding
	// and the snapshot's copies of its call frame and value are let go once
	// it is written: the save holds one row's encoding at a time, not every
	// row's until the commit. Its dependency and snapshot link rows, which
	// are small, are kept for their own tables below.
	for i := range snapshot.results {
		result := &snapshot.results[i]
		if result.frame == nil && !result.stored.holdsFrame() {
			if result.self == nil || result.self.Type() == nil || result.self.Type().Name() != "Query" {
				_ = tx.Rollback()
				return fmt.Errorf("persist result %d: missing result call frame", result.resultID)
			}
		}
		if err := c.encodePersistedResultRow(ctx, result); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := q.InsertMirrorResult(ctx, result.row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert result %d: %w", result.resultID, err)
		}
		result.row.SelfPayload = nil
		result.row.CallFrameJSON = ""
		result.frame = nil
		result.self = nil
		result.stored = nil
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
	for _, row := range snapshot.resultCalls {
		if err := q.InsertMirrorResultCall(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert result_call (%d,%s): %w", row.ResultID, row.RequestDigest, err)
		}
	}
	for _, row := range snapshot.resultTaught {
		if err := q.InsertMirrorResultTaught(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert result_taught (%d,%d): %w", row.ResultID, row.Position, err)
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
	}
	for _, row := range snapshot.snapshotContentLinks {
		if err := q.InsertMirrorSnapshotContentLink(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert snapshot_content_link (%s,%s): %w", row.SnapshotID, row.Digest, err)
		}
	}
	for _, row := range snapshot.snapshotContentHashes {
		if err := q.InsertMirrorSnapshotContentHash(ctx, row); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert snapshot_content_hash (%s): %w", row.SnapshotID, err)
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

func resultSnapshotLinkRows(resultID sharedResultID, links []PersistedSnapshotRefLink) ([]persistdb.MirrorResultSnapshotLink, error) {
	rows := make([]persistdb.MirrorResultSnapshotLink, 0, len(links))
	for _, link := range links {
		key, err := snapshotLinkKey(link)
		if err != nil {
			return nil, err
		}
		if link.RefKey == "" {
			return nil, fmt.Errorf("empty snapshot key")
		}
		rows = append(rows, persistdb.MirrorResultSnapshotLink{ResultID: int64(resultID), RefKey: link.RefKey, Role: key.Role, OutputPath: key.Path})
	}
	slices.SortFunc(rows, func(a, b persistdb.MirrorResultSnapshotLink) int {
		if a.OutputPath < b.OutputPath {
			return -1
		}
		if a.OutputPath > b.OutputPath {
			return 1
		}
		if a.Role < b.Role {
			return -1
		}
		if a.Role > b.Role {
			return 1
		}
		return 0
	})
	return rows, nil
}

func (c *Cache) persistResultEnvelope(ctx context.Context, snapshot *persistResultSnapshot) (encoding PersistedResultEncoding, rerr error) {
	defer func() {
		if rerr == nil && snapshot != nil {
			encoding.Envelope.Imported = snapshot.imported
			encoding.Envelope.PersistDeclined = snapshot.persistDeclined
			encoding.Envelope.BoundSession = snapshot.boundSession
			encoding.Envelope.PendingOffers, rerr = clonePartOffers(snapshot.pendingOffers)
			for _, part := range snapshot.storedParts {
				encoding.Envelope.StoredParts = append(encoding.Envelope.StoredParts, part.offer())
			}
		}
	}()
	if snapshot != nil && snapshot.persistedEnvelope != nil {
		return PersistedResultEncoding{
			Envelope:      *snapshot.persistedEnvelope,
			SnapshotLinks: snapshot.snapshotOwnerLinks,
		}, nil
	}
	if snapshot == nil {
		return PersistedResultEncoding{}, fmt.Errorf("persist result envelope: nil snapshot")
	}
	// Shutdown has drained cache operations, but diagnostic readers can still
	// briefly hold object latches. This permission belongs only to the persister,
	// never to live capture through the same codec.
	ctx = context.WithValue(ctx, quiescentPersistKey{}, true)
	if !snapshot.hasValue || snapshot.self == nil {
		// A row without a value is an attached absent value: it keeps its
		// identity and recorded call so restart restores the same row. The
		// declaration is checked here, at capture, so a contradictory row
		// fails this save by name instead of wiping the store on import.
		return encodePersistedAbsentValue(
			NewPersistEncodeContext(c, uint64(snapshot.resultID), snapshot.frame),
			uint64(snapshot.resultID),
			snapshot.sessionResourceHandle,
			true,
		)
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
