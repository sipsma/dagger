package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine/slog"
)

const (
	cachePersistBatchMaxEvents = 128
	cachePersistFlushInterval  = 250 * time.Millisecond
)

func (c *cache) startPersistenceWorker() {
	if c.sqlDB == nil || c.pdb == nil {
		return
	}

	c.persistMu.Lock()
	defer c.persistMu.Unlock()

	if c.persistDone != nil {
		return
	}

	c.persistNotify = make(chan struct{}, 1)
	c.persistFlushRequests = make(chan chan error)
	c.persistStop = make(chan struct{})
	c.persistDone = make(chan struct{})
	c.persistClosed = false
	c.persistErr = nil

	go c.persistenceWorker()
}

func (c *cache) flushAndStopPersistenceWorker(ctx context.Context) error {
	c.persistMu.Lock()
	stopCh := c.persistStop
	doneCh := c.persistDone
	if doneCh == nil {
		c.persistMu.Unlock()
		return nil
	}
	c.persistClosed = true
	c.persistMu.Unlock()

	flushErr := c.flushPersistenceWorker(ctx)

	close(stopCh)
	select {
	case <-doneCh:
	case <-ctx.Done():
		return ctx.Err()
	}

	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	workerErr := c.persistErr
	c.persistNotify = nil
	c.persistFlushRequests = nil
	c.persistStop = nil
	c.persistDone = nil
	c.persistQueue = nil
	return errors.Join(flushErr, workerErr)
}

func (c *cache) flushPersistenceWorker(ctx context.Context) error {
	c.persistMu.Lock()
	flushReqCh := c.persistFlushRequests
	doneCh := c.persistDone
	c.persistMu.Unlock()
	if flushReqCh == nil || doneCh == nil {
		return nil
	}

	flushAck := make(chan error, 1)
	select {
	case flushReqCh <- flushAck:
	case <-doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-flushAck:
		return err
	case <-doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *cache) persistenceWorker() {
	defer close(c.persistDone)

	ticker := time.NewTicker(cachePersistFlushInterval)
	defer ticker.Stop()

	pending := make([]cachePersistBatch, 0, cachePersistBatchMaxEvents)

	for {
		if len(pending) == 0 {
			select {
			case <-c.persistNotify:
				pending = append(pending, c.dequeuePersistBatches(cachePersistBatchMaxEvents)...)
			case ack := <-c.persistFlushRequests:
				ack <- c.applyPersistBatches(context.Background(), c.dequeuePersistBatches(0))
			case <-c.persistStop:
				err := c.applyPersistBatches(context.Background(), c.dequeuePersistBatches(0))
				c.setPersistErr(err)
				return
			}
			continue
		}

		if len(pending) >= cachePersistBatchMaxEvents {
			err := c.applyPersistBatches(context.Background(), pending)
			c.setPersistErr(err)
			pending = pending[:0]
			continue
		}

		select {
		case <-c.persistNotify:
			more := c.dequeuePersistBatches(cachePersistBatchMaxEvents - len(pending))
			pending = append(pending, more...)
		case <-ticker.C:
			err := c.applyPersistBatches(context.Background(), pending)
			c.setPersistErr(err)
			pending = pending[:0]
		case ack := <-c.persistFlushRequests:
			more := c.dequeuePersistBatches(0)
			pending = append(pending, more...)
			err := c.applyPersistBatches(context.Background(), pending)
			c.setPersistErr(err)
			pending = pending[:0]
			ack <- err
		case <-c.persistStop:
			more := c.dequeuePersistBatches(0)
			pending = append(pending, more...)
			err := c.applyPersistBatches(context.Background(), pending)
			c.setPersistErr(err)
			return
		}
	}
}

func (c *cache) setPersistErr(err error) {
	if err == nil {
		return
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	c.persistErr = errors.Join(c.persistErr, err)
}

func (c *cache) enqueuePersistBatch(batch cachePersistBatch) {
	if batch.isEmpty() {
		return
	}

	c.persistMu.Lock()
	if c.persistClosed || c.persistNotify == nil {
		c.persistMu.Unlock()
		return
	}
	c.persistQueue = append(c.persistQueue, batch)
	queueDepth := len(c.persistQueue)
	notify := c.persistNotify
	c.persistMu.Unlock()
	slog.Debug("dagql persistence enqueue", "queueDepth", queueDepth)

	select {
	case notify <- struct{}{}:
	default:
	}
}

func (c *cache) dequeuePersistBatches(limit int) []cachePersistBatch {
	c.persistMu.Lock()
	defer c.persistMu.Unlock()

	if len(c.persistQueue) == 0 {
		return nil
	}

	if limit <= 0 || limit >= len(c.persistQueue) {
		out := make([]cachePersistBatch, len(c.persistQueue))
		copy(out, c.persistQueue)
		c.persistQueue = nil
		return out
	}

	out := make([]cachePersistBatch, limit)
	copy(out, c.persistQueue[:limit])
	c.persistQueue = c.persistQueue[limit:]
	return out
}

func (batch cachePersistBatch) isEmpty() bool {
	return len(batch.SnapshotRefUpserts) == 0 &&
		len(batch.SnapshotRefTombstones) == 0 &&
		len(batch.ResultUpserts) == 0 &&
		len(batch.ResultTombstones) == 0 &&
		len(batch.ResultSnapshotRefUpserts) == 0 &&
		len(batch.ResultSnapshotRefTombs) == 0 &&
		len(batch.EqFactUpserts) == 0 &&
		len(batch.EqFactTombstones) == 0 &&
		len(batch.TermUpserts) == 0 &&
		len(batch.TermTombstones) == 0 &&
		len(batch.TermResultUpserts) == 0 &&
		len(batch.TermResultTombstones) == 0 &&
		len(batch.DepUpserts) == 0 &&
		len(batch.DepTombstones) == 0
}

func (c *cache) applyPersistBatches(ctx context.Context, batches []cachePersistBatch) error {
	var err error
	for _, batch := range batches {
		if batch.isEmpty() {
			continue
		}
		if applyErr := c.applyPersistBatch(ctx, batch); applyErr != nil {
			err = errors.Join(err, applyErr)
		}
	}
	return err
}

func (c *cache) applyPersistBatch(ctx context.Context, batch cachePersistBatch) error {
	if c.sqlDB == nil || c.pdb == nil {
		return nil
	}
	if batch.RootSafeToPersistSet && !batch.RootSafeToPersist {
		return nil
	}
	start := time.Now()

	tx, err := c.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin persistence transaction: %w", err)
	}
	commit := false
	defer func() {
		if !commit {
			_ = tx.Rollback()
		}
	}()

	q := c.pdb.WithTx(tx)

	for _, row := range batch.SnapshotRefUpserts {
		if err := q.UpsertSnapshotRef(ctx, row); err != nil {
			return fmt.Errorf("upsert snapshot_ref %q: %w", row.RefKey, err)
		}
	}
	for _, row := range batch.ResultUpserts {
		if err := q.UpsertResult(ctx, row); err != nil {
			return fmt.Errorf("upsert result %q: %w", row.ResultKey, err)
		}
	}
	for _, row := range batch.ResultSnapshotRefUpserts {
		if err := q.UpsertResultSnapshotRef(ctx, row); err != nil {
			return fmt.Errorf("upsert result_snapshot_ref %q->%q: %w", row.ResultKey, row.RefKey, err)
		}
	}
	for _, row := range batch.EqFactUpserts {
		if err := q.UpsertEqFact(ctx, persistdb.EqFact{
			OwnerResultKey:    string(row.OwnerResultKey),
			LHSDigest:         row.LHSDigest,
			RHSDigest:         row.RHSDigest,
			CreatedAtUnixNano: time.Now().UnixNano(),
			Deleted:           false,
			DeletedAtUnixNano: 0,
		}); err != nil {
			return fmt.Errorf("upsert eq_fact (%q,%q,%q): %w", row.OwnerResultKey, row.LHSDigest, row.RHSDigest, err)
		}
	}
	for _, row := range batch.TermUpserts {
		if err := q.UpsertTerm(ctx, row); err != nil {
			return fmt.Errorf("upsert term %q: %w", row.TermDigest, err)
		}
	}
	for _, row := range batch.TermResultUpserts {
		if err := q.UpsertTermResult(ctx, row); err != nil {
			return fmt.Errorf("upsert term_result %q->%q: %w", row.TermDigest, row.ResultKey, err)
		}
	}
	for _, row := range batch.DepUpserts {
		if err := q.UpsertDep(ctx, row); err != nil {
			return fmt.Errorf("upsert dep %q->%q: %w", row.ParentResultKey, row.DepResultKey, err)
		}
	}

	now := time.Now().UnixNano()
	for _, key := range batch.ResultTombstones {
		if err := q.TombstoneResult(ctx, string(key), now); err != nil {
			return fmt.Errorf("tombstone result %q: %w", key, err)
		}
	}
	for _, row := range batch.EqFactTombstones {
		if err := q.TombstoneEqFact(ctx, string(row.OwnerResultKey), row.LHSDigest, row.RHSDigest, now); err != nil {
			return fmt.Errorf("tombstone eq_fact (%q,%q,%q): %w", row.OwnerResultKey, row.LHSDigest, row.RHSDigest, err)
		}
	}
	for _, row := range batch.DepTombstones {
		if err := q.TombstoneDep(ctx, row.ParentResultKey, row.DepResultKey, now); err != nil {
			return fmt.Errorf("tombstone dep %q->%q: %w", row.ParentResultKey, row.DepResultKey, err)
		}
	}
	for _, row := range batch.TermResultTombstones {
		if err := q.TombstoneTermResult(ctx, row.TermDigest, row.ResultKey, now); err != nil {
			return fmt.Errorf("tombstone term_result %q->%q: %w", row.TermDigest, row.ResultKey, err)
		}
	}
	for _, row := range batch.ResultSnapshotRefTombs {
		if err := q.TombstoneResultSnapshotRef(ctx, row.ResultKey, row.RefKey, row.Role, row.Slot, now); err != nil {
			return fmt.Errorf("tombstone result_snapshot_ref %q->%q: %w", row.ResultKey, row.RefKey, err)
		}
	}
	for _, key := range batch.TermTombstones {
		if err := q.TombstoneTerm(ctx, key, now); err != nil {
			return fmt.Errorf("tombstone term %q: %w", key, err)
		}
	}
	for _, key := range batch.SnapshotRefTombstones {
		if err := q.TombstoneSnapshotRef(ctx, string(key), now); err != nil {
			return fmt.Errorf("tombstone snapshot_ref %q: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit persistence transaction: %w", err)
	}
	commit = true
	slog.Debug("dagql persistence batch applied",
		"duration", time.Since(start),
		"rootResultKey", batch.RootResultKey,
		"snapshotRefUpserts", len(batch.SnapshotRefUpserts),
		"snapshotRefTombstones", len(batch.SnapshotRefTombstones),
		"resultUpserts", len(batch.ResultUpserts),
		"resultTombstones", len(batch.ResultTombstones),
		"resultSnapshotRefUpserts", len(batch.ResultSnapshotRefUpserts),
		"resultSnapshotRefTombstones", len(batch.ResultSnapshotRefTombs),
		"eqFactUpserts", len(batch.EqFactUpserts),
		"eqFactTombstones", len(batch.EqFactTombstones),
		"termUpserts", len(batch.TermUpserts),
		"termTombstones", len(batch.TermTombstones),
		"termResultUpserts", len(batch.TermResultUpserts),
		"termResultTombstones", len(batch.TermResultTombstones),
		"depUpserts", len(batch.DepUpserts),
		"depTombstones", len(batch.DepTombstones),
	)
	return nil
}

func (c *cache) emitPersistUpsertForRootLocked(root *sharedResult) {
	batch, err := c.buildPersistUpsertBatchForRootLocked(root)
	if err != nil {
		slog.Warn("failed to build persistence upsert batch", "err", err)
		return
	}
	c.enqueuePersistBatch(batch)
}

func (c *cache) emitPersistTombstonesForRootLocked(root *sharedResult) {
	batch, err := c.buildPersistTombstoneBatchForRootLocked(root)
	if err != nil {
		slog.Warn("failed to build persistence tombstone batch", "err", err)
		return
	}
	c.enqueuePersistBatch(batch)
}

func (c *cache) buildPersistUpsertBatchForRootLocked(root *sharedResult) (cachePersistBatch, error) {
	if root == nil || root.id == 0 {
		return cachePersistBatch{}, nil
	}
	rootKey, err := root.persistResultKey()
	if err != nil {
		return cachePersistBatch{}, fmt.Errorf("derive root persist key: %w", err)
	}

	resultIDs := c.persistClosureResultIDsLocked(root.id)
	if len(resultIDs) == 0 {
		return cachePersistBatch{}, nil
	}
	slices.Sort(resultIDs)

	classDigests, classRepresentative := c.persistEqClassDigestsLocked()
	closureDigests := c.persistClosureDigestsLocked(resultIDs)
	now := time.Now().UnixNano()

	batch := cachePersistBatch{
		RootResultKey:        rootKey,
		RootSafeToPersist:    root.safeToPersistCache,
		RootSafeToPersistSet: true,
	}

	resultSeen := make(map[string]struct{}, len(resultIDs))
	termSeen := map[string]struct{}{}
	termResultSeen := map[string]struct{}{}
	depSeen := map[string]struct{}{}
	eqFactSeen := map[string]struct{}{}
	resultSnapshotSeen := map[string]struct{}{}
	snapshotRefSeen := map[string]struct{}{}

	for _, resultID := range resultIDs {
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		resKey, err := res.persistResultKey()
		if err != nil {
			return cachePersistBatch{}, fmt.Errorf("derive result persist key: %w", err)
		}
		if _, ok := resultSeen[string(resKey)]; ok {
			continue
		}
		resultSeen[string(resKey)] = struct{}{}

		env, err := c.persistResultEnvelopeLocked(res)
		if err != nil {
			return cachePersistBatch{}, fmt.Errorf("encode persisted self payload for %q: %w", resKey, err)
		}
		envJSON, err := json.Marshal(env)
		if err != nil {
			return cachePersistBatch{}, fmt.Errorf("marshal persisted self payload for %q: %w", resKey, err)
		}

		extraJSON, err := json.Marshal(res.outputExtraDigests)
		if err != nil {
			return cachePersistBatch{}, fmt.Errorf("marshal output extra digests for %q: %w", resKey, err)
		}
		effectsJSON, err := json.Marshal(res.outputEffectIDs)
		if err != nil {
			return cachePersistBatch{}, fmt.Errorf("marshal output effect IDs for %q: %w", resKey, err)
		}

		reason := "dependency_of_persisted"
		if res.id == root.id {
			reason = "persisted_root"
		}

		batch.ResultUpserts = append(batch.ResultUpserts, persistdb.Result{
			ResultKey:            string(resKey),
			IDDigest:             c.resultIDDigestLocked(res),
			OutputDigest:         res.outputDigest.String(),
			OutputExtraDigests:   string(extraJSON),
			OutputEffectIDs:      string(effectsJSON),
			SelfType:             env.Kind,
			SelfVersion:          int64(env.Version),
			SelfPayload:          envJSON,
			DepOfPersistedResult: res.depOfPersistedResult,
			SafeToPersistCache:   res.safeToPersistCache,
			UnsafeMarker:         !res.safeToPersistCache,
			PersistReason:        reason,
			CreatedAtUnixNano:    res.createdAtUnixNano,
			ExpiresAtUnix:        res.expiresAtUnix,
			RecordType:           res.recordType,
			Description:          res.description,
			Deleted:              false,
			DeletedAtUnixNano:    0,
		})

		links := c.persistedSnapshotLinksForResultLocked(res)
		for _, link := range links {
			if link.RefKey == "" {
				continue
			}
			rsrKey := fmt.Sprintf("%s\x00%s\x00%s\x00%s", resKey, link.RefKey, link.Role, link.Slot)
			if _, ok := resultSnapshotSeen[rsrKey]; !ok {
				resultSnapshotSeen[rsrKey] = struct{}{}
				batch.ResultSnapshotRefUpserts = append(batch.ResultSnapshotRefUpserts, persistdb.ResultSnapshotRef{
					ResultKey:         string(resKey),
					RefKey:            link.RefKey,
					Role:              link.Role,
					Slot:              link.Slot,
					Deleted:           false,
					DeletedAtUnixNano: 0,
				})
			}

			if _, ok := snapshotRefSeen[link.RefKey]; !ok {
				snapshotRefSeen[link.RefKey] = struct{}{}
				batch.SnapshotRefUpserts = append(batch.SnapshotRefUpserts, persistdb.SnapshotRef{
					RefKey:            link.RefKey,
					Kind:              "unknown",
					SnapshotID:        link.RefKey,
					SnapshotterName:   "",
					LeaseID:           link.RefKey,
					ContentBlobDigest: "",
					ChainID:           "",
					BlobChainID:       "",
					DiffID:            "",
					MediaType:         "",
					BlobSize:          0,
					URLsJSON:          "[]",
					RecordType:        "dagql.snapshot",
					Description:       "snapshot ref",
					CreatedAtUnixNano: now,
					SizeBytes:         0,
					Deleted:           false,
					DeletedAtUnixNano: 0,
				})
			}
		}

		termIDs := c.egraphTermIDsByResult[resultID]
		for termID := range termIDs {
			term := c.egraphTerms[termID]
			if term == nil {
				continue
			}

			if _, ok := termSeen[term.termDigest]; !ok {
				termSeen[term.termDigest] = struct{}{}
				inputDigests := make([]string, 0, len(term.inputEqIDs))
				for _, inputEqID := range term.inputEqIDs {
					rootEq := c.findEqClassLocked(inputEqID)
					rep := classRepresentative[rootEq]
					if rep == "" {
						rep = fmt.Sprintf("eqclass:%d", rootEq)
					}
					inputDigests = append(inputDigests, rep)
				}
				inputsJSON, err := json.Marshal(inputDigests)
				if err != nil {
					return cachePersistBatch{}, fmt.Errorf("marshal term input digests for %q: %w", term.termDigest, err)
				}
				batch.TermUpserts = append(batch.TermUpserts, persistdb.Term{
					TermDigest:        term.termDigest,
					SelfDigest:        term.selfDigest.String(),
					InputDigestsJSON:  string(inputsJSON),
					CreatedAtUnixNano: res.createdAtUnixNano,
					Deleted:           false,
					DeletedAtUnixNano: 0,
				})
			}

			trKey := fmt.Sprintf("%s\x00%s", term.termDigest, resKey)
			if _, ok := termResultSeen[trKey]; !ok {
				termResultSeen[trKey] = struct{}{}
				batch.TermResultUpserts = append(batch.TermResultUpserts, persistdb.TermResult{
					TermDigest:        term.termDigest,
					ResultKey:         string(resKey),
					Deleted:           false,
					DeletedAtUnixNano: 0,
				})
			}
		}

		for depID := range res.deps {
			if _, ok := closureDigests.byResultID[depID]; !ok {
				continue
			}
			depRes := c.resultsByID[depID]
			if depRes == nil {
				continue
			}
			depKey, err := depRes.persistResultKey()
			if err != nil {
				return cachePersistBatch{}, fmt.Errorf("derive dep persist key: %w", err)
			}
			key := fmt.Sprintf("%s\x00%s", resKey, depKey)
			if _, ok := depSeen[key]; ok {
				continue
			}
			depSeen[key] = struct{}{}
			batch.DepUpserts = append(batch.DepUpserts, persistdb.Dep{
				ParentResultKey:   string(resKey),
				DepResultKey:      string(depKey),
				Deleted:           false,
				DeletedAtUnixNano: 0,
			})
		}

		ownerDigest := c.resultIDDigestLocked(res)
		if ownerDigest == "" {
			continue
		}
		ownerClassID := c.findEqClassLocked(c.egraphDigestToClass[ownerDigest])
		for _, memberDigest := range classDigests[ownerClassID] {
			if _, ok := closureDigests.byDigest[memberDigest]; !ok {
				continue
			}
			row, err := newPersistEqFactRow(resKey, ownerDigest, memberDigest)
			if err != nil {
				return cachePersistBatch{}, fmt.Errorf("build eq fact row for %q: %w", resKey, err)
			}
			key := fmt.Sprintf("%s\x00%s\x00%s", row.OwnerResultKey, row.LHSDigest, row.RHSDigest)
			if _, ok := eqFactSeen[key]; ok {
				continue
			}
			eqFactSeen[key] = struct{}{}
			batch.EqFactUpserts = append(batch.EqFactUpserts, row)
		}
	}

	return batch, nil
}

func (c *cache) buildPersistTombstoneBatchForRootLocked(root *sharedResult) (cachePersistBatch, error) {
	if root == nil || root.id == 0 {
		return cachePersistBatch{}, nil
	}
	rootKey, err := root.persistResultKey()
	if err != nil {
		return cachePersistBatch{}, fmt.Errorf("derive root persist key: %w", err)
	}
	batch := cachePersistBatch{
		RootResultKey:        rootKey,
		RootSafeToPersist:    root.safeToPersistCache,
		RootSafeToPersistSet: true,
		ResultTombstones:     []cachePersistResultKey{rootKey},
	}

	ownerDigest := c.resultIDDigestLocked(root)
	if ownerDigest != "" {
		ownerClassID := c.findEqClassLocked(c.egraphDigestToClass[ownerDigest])
		classDigests, _ := c.persistEqClassDigestsLocked()
		for _, member := range classDigests[ownerClassID] {
			row, err := newPersistEqFactRow(rootKey, ownerDigest, member)
			if err != nil {
				return cachePersistBatch{}, fmt.Errorf("build tombstone eq fact row: %w", err)
			}
			batch.EqFactTombstones = append(batch.EqFactTombstones, row)
		}
	}

	termIDs := c.egraphTermIDsByResult[root.id]
	for termID := range termIDs {
		term := c.egraphTerms[termID]
		if term == nil {
			continue
		}
		batch.TermResultTombstones = append(batch.TermResultTombstones, persistdb.TermResult{
			TermDigest: term.termDigest,
			ResultKey:  string(rootKey),
		})

		otherResults := c.egraphResultsByTermID[termID]
		if len(otherResults) == 1 {
			if _, ok := otherResults[root.id]; ok {
				batch.TermTombstones = append(batch.TermTombstones, term.termDigest)
			}
		}
	}

	for depID := range root.deps {
		depRes := c.resultsByID[depID]
		if depRes == nil {
			continue
		}
		depKey, err := depRes.persistResultKey()
		if err != nil {
			return cachePersistBatch{}, fmt.Errorf("derive dep key for tombstone: %w", err)
		}
		batch.DepTombstones = append(batch.DepTombstones, persistdb.Dep{
			ParentResultKey: string(rootKey),
			DepResultKey:    string(depKey),
		})
	}
	for parentID, parent := range c.resultsByID {
		if parent == nil || parentID == root.id {
			continue
		}
		if _, ok := parent.deps[root.id]; !ok {
			continue
		}
		parentKey, err := parent.persistResultKey()
		if err != nil {
			return cachePersistBatch{}, fmt.Errorf("derive parent key for tombstone: %w", err)
		}
		batch.DepTombstones = append(batch.DepTombstones, persistdb.Dep{
			ParentResultKey: string(parentKey),
			DepResultKey:    string(rootKey),
		})
	}

	for _, link := range c.persistedSnapshotLinksForResultLocked(root) {
		if link.RefKey == "" {
			continue
		}
		batch.ResultSnapshotRefTombs = append(batch.ResultSnapshotRefTombs, persistdb.ResultSnapshotRef{
			ResultKey: string(rootKey),
			RefKey:    link.RefKey,
			Role:      link.Role,
			Slot:      link.Slot,
		})
	}

	return batch, nil
}

type persistClosureDigestSet struct {
	byDigest   map[string]struct{}
	byResultID map[sharedResultID]struct{}
}

func (c *cache) persistClosureDigestsLocked(resultIDs []sharedResultID) persistClosureDigestSet {
	out := persistClosureDigestSet{
		byDigest:   make(map[string]struct{}, len(resultIDs)*3),
		byResultID: make(map[sharedResultID]struct{}, len(resultIDs)),
	}
	for _, resultID := range resultIDs {
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		out.byResultID[resultID] = struct{}{}
		if d := c.resultIDDigestLocked(res); d != "" {
			out.byDigest[d] = struct{}{}
		}
		if d := res.outputDigest.String(); d != "" {
			out.byDigest[d] = struct{}{}
		}
		for _, extra := range res.outputExtraDigests {
			if d := extra.Digest.String(); d != "" {
				out.byDigest[d] = struct{}{}
			}
		}
	}
	return out
}

func (c *cache) persistResultEnvelopeLocked(res *sharedResult) (PersistedResultEnvelope, error) {
	if res != nil && res.persistedEnvelope != nil {
		return *res.persistedEnvelope, nil
	}
	if res == nil || !res.hasValue {
		return PersistedResultEnvelope{
			Version: 1,
			Kind:    persistedResultKindNull,
		}, nil
	}
	if res.originalRequestID == nil {
		return PersistedResultEnvelope{}, fmt.Errorf("result has nil original request ID and no persisted envelope")
	}
	typedRes := Result[Typed]{
		shared: res,
		id:     res.originalRequestID,
	}
	var anyRes AnyResult = typedRes
	if res.objType != nil {
		objRes, err := res.objType.New(typedRes)
		if err != nil {
			return PersistedResultEnvelope{}, fmt.Errorf("reconstruct object result: %w", err)
		}
		anyRes = objRes
	}
	return DefaultPersistedSelfCodec.EncodeResult(context.Background(), anyRes)
}

func (c *cache) resultIDDigestLocked(res *sharedResult) string {
	if res == nil {
		return ""
	}
	if res.idDigest != "" {
		return res.idDigest
	}
	if res.originalRequestID != nil {
		return res.originalRequestID.Digest().String()
	}
	if res.persistedResultKey != "" {
		return string(res.persistedResultKey)
	}
	return ""
}

func (c *cache) persistedSnapshotLinksForResultLocked(res *sharedResult) []PersistedSnapshotRefLink {
	if res == nil {
		return nil
	}
	typedLinks := persistedSnapshotLinksFromTyped(res.self)
	if len(typedLinks) > 0 {
		return typedLinks
	}
	if len(res.persistedSnapshotLinks) == 0 {
		return nil
	}
	links := make([]PersistedSnapshotRefLink, len(res.persistedSnapshotLinks))
	copy(links, res.persistedSnapshotLinks)
	return links
}

func (c *cache) persistClosureResultIDsLocked(rootResultID sharedResultID) []sharedResultID {
	if rootResultID == 0 {
		return nil
	}
	seen := make(map[sharedResultID]struct{})
	stack := []sharedResultID{rootResultID}
	out := make([]sharedResultID, 0, 16)
	for len(stack) > 0 {
		n := len(stack) - 1
		curID := stack[n]
		stack = stack[:n]
		if _, ok := seen[curID]; ok {
			continue
		}
		seen[curID] = struct{}{}
		res := c.resultsByID[curID]
		if res == nil {
			continue
		}
		out = append(out, curID)
		for depID := range res.deps {
			stack = append(stack, depID)
		}
	}
	return out
}

func (c *cache) persistEqClassDigestsLocked() (map[eqClassID][]string, map[eqClassID]string) {
	classDigests := make(map[eqClassID][]string)
	for dig, cls := range c.egraphDigestToClass {
		root := c.findEqClassLocked(cls)
		if root == 0 || dig == "" {
			continue
		}
		classDigests[root] = append(classDigests[root], dig)
	}
	classRepresentative := make(map[eqClassID]string, len(classDigests))
	for root, members := range classDigests {
		slices.Sort(members)
		classDigests[root] = members
		classRepresentative[root] = members[0]
	}
	return classDigests, classRepresentative
}
