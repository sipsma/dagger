package dagql

import (
	"fmt"

	"github.com/dagger/dagger/dagql/call"
)

// cachePersistResultKey is the durable identity for a persisted sharedResult.
//
// Contract:
// - never derived from process-local IDs (sharedResultID).
// - derived from sharedResult.originalRequestID digest.
type cachePersistResultKey string

func derivePersistResultKey(id *call.ID) (cachePersistResultKey, error) {
	if id == nil {
		return "", fmt.Errorf("derive persist result key: nil request ID")
	}
	dig := id.Digest().String()
	if dig == "" {
		return "", fmt.Errorf("derive persist result key: empty request digest")
	}
	return cachePersistResultKey(dig), nil
}

func (res *sharedResult) persistResultKey() (cachePersistResultKey, error) {
	if res == nil {
		return "", fmt.Errorf("derive persist result key: nil shared result")
	}
	return derivePersistResultKey(res.originalRequestID)
}

// cachePersistSnapshotRefKey is the durable global key for persisted snapshot
// refs. It is intentionally process-agnostic and never derived from in-memory
// pointer identity.
type cachePersistSnapshotRefKey string

func (k cachePersistSnapshotRefKey) validate() error {
	if k == "" {
		return fmt.Errorf("empty snapshot ref key")
	}
	return nil
}

// cachePersistEqFactRow is the durable row form for eq_facts.
//
// Ownership semantics:
// - OwnerResultKey denotes lifecycle ownership only (not canonicality).
// - The same digest pair may exist for multiple owners.
// - Deleting one owner tombstones only that owner's rows.
//
// Ordering semantics:
// - lhs/rhs are canonicalized (lhs <= rhs) for deterministic dedupe.
type cachePersistEqFactRow struct {
	OwnerResultKey cachePersistResultKey
	LHSDigest      string
	RHSDigest      string
}

func newPersistEqFactRow(owner cachePersistResultKey, lhsDigest, rhsDigest string) (cachePersistEqFactRow, error) {
	row := cachePersistEqFactRow{
		OwnerResultKey: owner,
		LHSDigest:      lhsDigest,
		RHSDigest:      rhsDigest,
	}
	if row.RHSDigest < row.LHSDigest {
		row.LHSDigest, row.RHSDigest = row.RHSDigest, row.LHSDigest
	}
	if err := row.validate(); err != nil {
		return cachePersistEqFactRow{}, err
	}
	return row, nil
}

func (row cachePersistEqFactRow) validate() error {
	if row.OwnerResultKey == "" {
		return fmt.Errorf("eq fact row has empty owner result key")
	}
	if row.LHSDigest == "" || row.RHSDigest == "" {
		return fmt.Errorf("eq fact row has empty digest(s)")
	}
	if row.LHSDigest > row.RHSDigest {
		return fmt.Errorf("eq fact row digest ordering is not canonical: lhs=%q rhs=%q", row.LHSDigest, row.RHSDigest)
	}
	return nil
}

// cachePersistBatch is the emitter->worker boundary payload for disk
// persistence.
//
// IMPORTANT:
//   - This is summarized table-state payload (upsert/tombstone intent), not an
//     event-log transport format.
//   - Worker applies rows directly to normalized store tables and must remain
//     dumb/idempotent; it does not replay e-graph logic.
//   - Tombstones must only be emitted for rows known to have been previously
//     persisted.
type cachePersistBatch struct {
	EqFactUpserts    []cachePersistEqFactRow
	EqFactTombstones []cachePersistEqFactRow
}
