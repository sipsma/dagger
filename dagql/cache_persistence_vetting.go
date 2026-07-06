package dagql

import (
	"context"
	"encoding/json"
	"fmt"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine/slog"
	bkcache "github.com/dagger/dagger/engine/snapshots"
)

// restoreDropReason names why a restored row was dropped at boot vetting.
// Vetting decisions are binary keep/drop on these criteria alone; damaged
// data is never repaired.
type restoreDropReason string

const (
	// The row's call frame or payload envelope does not parse.
	restoreDropMalformed restoreDropReason = "malformed"
	// The row's snapshots are gone from the local store and it has neither a
	// content chain nor a lazy fragment to be re-made from.
	restoreDropSnapshotMissing restoreDropReason = "snapshot_missing"
	// The row references a dependency row that does not exist in the store.
	restoreDropMissingDep restoreDropReason = "missing_dep"
	// A row this one depends on was dropped; drops cascade forward.
	restoreDropDependent restoreDropReason = "dependent_of_dropped"
	// The row participates in (or depends on) a dependency cycle. Honest
	// stores are acyclic, so a cycle is corruption and every row involved
	// drops together.
	restoreDropDependencyCycle restoreDropReason = "dependency_cycle"
)

// restoredResultRow is one persisted results row, parsed and vetted. Kept
// rows carry the state the import builds the in-memory result from; links
// may have been cleared when the backing snapshots turned out to be gone
// but the row survived on its lazy fragment.
type restoredResultRow struct {
	id     sharedResultID
	row    persistdb.MirrorResult
	frame  *ResultCall
	env    PersistedResultEnvelope
	links  []PersistedSnapshotRefLink
	chains []PersistedResultContentChain
	deps   []sharedResultID
	origin resultOrigin
}

// CacheRestoreDroppedResult records one row dropped at boot vetting.
type CacheRestoreDroppedResult struct {
	SharedResultID uint64 `json:"shared_result_id"`
	Reason         string `json:"reason"`
}

// CacheRestoreSummary is the boot-restore outcome: how many rows survived
// vetting, how many dropped and why, or that the store was wiped wholesale.
type CacheRestoreSummary struct {
	Kept           int                         `json:"kept"`
	Dropped        int                         `json:"dropped"`
	Wiped          bool                        `json:"wiped"`
	Reason         string                      `json:"reason,omitempty"`
	DroppedResults []CacheRestoreDroppedResult `json:"dropped_results,omitempty"`
}

// vetRestoredResults decides, per persisted row, whether it can still honor
// a cache hit: its frame and envelope parse, every row it depends on was
// kept, and it retains at least one way to deliver content — its snapshots
// (verified present by attaching their owner leases), its persisted content
// chain (blob availability deliberately unchecked: the runtime fall-through
// owns that), or its lazy fragment.
// Rows are vetted dependencies-first so a drop cascades to dependents,
// never backwards; rows left unprocessed by that order sit on a dependency
// cycle, which honest data cannot contain, and drop wholesale.
//
// Owner leases are attached here, only for rows whose verdict is keep — a
// dropped row must never pin content. The lease attach doubles as the
// snapshot-presence check: attaching to a pruned snapshot fails with the
// store's not-found error, which demotes the row to its lazy fragment or
// drops it. Any other attach failure is infrastructure, not row damage, and
// aborts the import.
func (c *Cache) vetRestoredResults(
	ctx context.Context,
	resultRows []persistdb.MirrorResult,
	resultDepRows []persistdb.MirrorResultDep,
	resultSnapshotRows []persistdb.MirrorResultSnapshotLink,
	resultOriginRows []persistdb.MirrorResultOrigin,
	resultContentChainRows []persistdb.MirrorResultContentChain,
) (map[sharedResultID]*restoredResultRow, *CacheRestoreSummary, error) {
	rows, malformed, err := parseRestoredResultRows(resultRows)
	if err != nil {
		return nil, nil, err
	}

	// Every row must carry exactly one origin: a row without one cannot be
	// exported or deduped honestly, which is per-row damage, not store
	// damage.
	originsByResult := make(map[sharedResultID]resultOrigin, len(resultOriginRows))
	for _, row := range resultOriginRows {
		originsByResult[sharedResultID(row.ResultID)] = resultOrigin{
			storeUUID: row.OriginStoreUUID,
			resultID:  uint64(row.OriginResultID),
		}
	}
	for id, restored := range rows {
		origin, hasOrigin := originsByResult[id]
		if !hasOrigin || origin.storeUUID == "" || origin.resultID == 0 {
			malformed[id] = struct{}{}
			continue
		}
		restored.origin = origin
	}

	missingDep := make(map[sharedResultID]struct{})
	dependents := make(map[sharedResultID][]sharedResultID)
	undecidedDeps := make(map[sharedResultID]int, len(rows))
	for _, row := range resultDepRows {
		parentID := sharedResultID(row.ParentResultID)
		depID := sharedResultID(row.DepResultID)
		parent, parentExists := rows[parentID]
		if !parentExists {
			continue
		}
		if _, depExists := rows[depID]; !depExists {
			missingDep[parentID] = struct{}{}
			continue
		}
		parent.deps = append(parent.deps, depID)
		dependents[depID] = append(dependents[depID], parentID)
		undecidedDeps[parentID]++
	}

	for _, row := range resultSnapshotRows {
		id := sharedResultID(row.ResultID)
		restored, exists := rows[id]
		if !exists {
			continue
		}
		restored.links = append(restored.links, PersistedSnapshotRefLink{
			RefKey: row.RefKey,
			Role:   row.Role,
		})
	}

	for _, row := range resultContentChainRows {
		id := sharedResultID(row.ResultID)
		restored, exists := rows[id]
		if !exists {
			continue
		}
		chain, err := contentChainFromRow(row)
		if err != nil {
			// Per-chain damage: the chain is absent, the row is not. If the
			// row's survival depended on it, the fallback rule below drops
			// the row — the same binary keep/drop shape, one level up.
			slog.Warn("dropping unparseable persisted content chain",
				"sharedResultID", id, "role", row.Role, "err", err)
			continue
		}
		restored.chains = append(restored.chains, chain)
	}

	kept := make(map[sharedResultID]*restoredResultRow, len(rows))
	droppedReasons := make(map[sharedResultID]restoreDropReason)
	drop := func(id sharedResultID, reason restoreDropReason) {
		droppedReasons[id] = reason
		slog.Warn("dropping persisted result at restore vetting",
			"sharedResultID", id, "reason", string(reason))
		c.traceRestoreResultDropped(ctx, id, reason)
	}

	queue := make([]sharedResultID, 0, len(rows))
	for id := range rows {
		if undecidedDeps[id] == 0 {
			queue = append(queue, id)
		}
	}
	vetOne := func(restored *restoredResultRow) (*restoreDropReason, error) {
		if _, isMalformed := malformed[restored.id]; isMalformed {
			reason := restoreDropMalformed
			return &reason, nil
		}
		if _, hasMissingDep := missingDep[restored.id]; hasMissingDep {
			reason := restoreDropMissingDep
			return &reason, nil
		}
		for _, depID := range restored.deps {
			if _, depKept := kept[depID]; !depKept {
				reason := restoreDropDependent
				return &reason, nil
			}
		}
		if len(restored.links) > 0 && c.snapshotManager != nil {
			present, err := c.attachRestoredOwnerLeases(ctx, restored)
			if err != nil {
				return nil, err
			}
			if !present {
				// The row's claimed content is gone from the local store. It
				// survives on a re-make fallback: its persisted content chain
				// (the walk re-fetches from the CAS on first use) or its lazy
				// fragment (the walk re-executes). With neither, the promise
				// cannot be honored and the row drops.
				restored.links = nil
				if len(restored.env.LazyJSON) == 0 && len(restored.chains) == 0 {
					reason := restoreDropSnapshotMissing
					return &reason, nil
				}
			}
		}
		return nil, nil
	}

	decided := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		decided++
		restored := rows[id]

		reason, err := vetOne(restored)
		if err != nil {
			return nil, nil, err
		}
		if reason != nil {
			drop(id, *reason)
		} else {
			kept[id] = restored
		}
		for _, dependentID := range dependents[id] {
			undecidedDeps[dependentID]--
			if undecidedDeps[dependentID] == 0 {
				queue = append(queue, dependentID)
			}
		}
	}

	// Rows the dependencies-first order never reached sit on a dependency
	// cycle (or depend on one). Honest stores are acyclic.
	if decided < len(rows) {
		for id := range rows {
			if _, wasDecided := droppedReasons[id]; wasDecided {
				continue
			}
			if _, wasKept := kept[id]; wasKept {
				continue
			}
			drop(id, restoreDropDependencyCycle)
		}
	}

	summary := &CacheRestoreSummary{
		Kept:    len(kept),
		Dropped: len(droppedReasons),
	}
	for id, reason := range droppedReasons {
		summary.DroppedResults = append(summary.DroppedResults, CacheRestoreDroppedResult{
			SharedResultID: uint64(id),
			Reason:         string(reason),
		})
	}
	return kept, summary, nil
}

// parseRestoredResultRows parses every persisted row's frame and envelope,
// flagging rows that fail to parse as malformed rather than failing the
// import: parse damage is per-row damage.
func parseRestoredResultRows(resultRows []persistdb.MirrorResult) (map[sharedResultID]*restoredResultRow, map[sharedResultID]struct{}, error) {
	rows := make(map[sharedResultID]*restoredResultRow, len(resultRows))
	malformed := make(map[sharedResultID]struct{})
	for _, row := range resultRows {
		id := sharedResultID(row.ID)
		if id == 0 {
			return nil, nil, fmt.Errorf("import result: zero ID")
		}
		restored := &restoredResultRow{id: id, row: row}
		rows[id] = restored

		var env PersistedResultEnvelope
		if len(row.SelfPayload) > 0 {
			if err := json.Unmarshal(row.SelfPayload, &env); err != nil {
				malformed[id] = struct{}{}
				continue
			}
		} else {
			env = PersistedResultEnvelope{
				Version: 1,
				Kind:    persistedResultKindNull,
			}
		}
		if env.Kind == "" {
			malformed[id] = struct{}{}
			continue
		}
		if row.CallFrameJSON == "" {
			malformed[id] = struct{}{}
			continue
		}
		frame := &ResultCall{}
		if err := json.Unmarshal([]byte(row.CallFrameJSON), frame); err != nil {
			malformed[id] = struct{}{}
			continue
		}
		restored.env = env
		restored.frame = frame
	}
	return rows, malformed, nil
}

// attachRestoredOwnerLeases attaches the row's snapshot owner leases,
// reporting present=false when any backing snapshot is gone from the local
// store. Partially attached leases for an absent row are cleaned up by the
// stale-lease sweep that runs after vetting, keyed off the kept rows' final
// links.
func (c *Cache) attachRestoredOwnerLeases(ctx context.Context, restored *restoredResultRow) (present bool, err error) {
	seen := make(map[snapshotOwnerKey]struct{}, len(restored.links))
	for _, link := range restored.links {
		key := snapshotOwnerKey{Role: link.Role}
		if _, alreadySeen := seen[key]; alreadySeen {
			continue
		}
		seen[key] = struct{}{}
		err := c.snapshotManager.AttachLease(
			ctx,
			resultSnapshotLeaseID(restored.id, link.Role),
			link.RefKey,
		)
		if err != nil {
			if bkcache.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("attach imported result %d owner lease %q: %w", restored.id, link.Role, err)
		}
	}
	return true, nil
}
