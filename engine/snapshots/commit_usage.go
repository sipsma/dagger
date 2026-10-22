package snapshots

import (
	"context"
	"os"
	"sync"

	"github.com/containerd/containerd/v2/core/snapshots"
)

// usageFirstEnv turns measuring a snapshot's disk usage before its commit off
// when set to 0.
const usageFirstEnv = "_EXPERIMENTAL_DAGGER_SNAPSHOT_USAGE_FIRST"

// UsageFirstEnabled reports whether the engine measures a snapshot's disk
// usage before its commit, for a backend snapshotter that records what was
// measured (containerd.NewUsageFirstSnapshotter).
var UsageFirstEnabled = sync.OnceValue(func() bool {
	return os.Getenv(usageFirstEnv) != "0"
})

// CommitUsage is an active snapshot's disk usage, measured by its backend
// snapshotter before the snapshot's commit, for that commit to record.
//
// containerd's metadata database commits a snapshot inside its write
// transaction, and a backend such as overlay walks the snapshot's whole
// directory for its usage there, holding the database's single writer for
// the length of the walk. Measured before, the walk holds nothing.
type CommitUsage struct {
	// Key and ID are the backend's key and its own ID for the snapshot
	// measured.
	Key, ID  string
	Usage    snapshots.Usage
	Measured bool
}

type commitUsageKey struct{}

// WithCommitUsage returns a context whose snapshot Usage measures an active
// snapshot for its commit, and the slot the measurement goes in.
func WithCommitUsage(ctx context.Context) (context.Context, *CommitUsage) {
	slot := &CommitUsage{}
	return context.WithValue(ctx, commitUsageKey{}, slot), slot
}

// CommitUsageFromContext returns the slot a commit's measurement goes in,
// nil when the commit asked for none.
func CommitUsageFromContext(ctx context.Context) *CommitUsage {
	slot, _ := ctx.Value(commitUsageKey{}).(*CommitUsage)
	return slot
}

type usageAtCommitKey struct{}

// WithUsageAtCommit marks a commit whose snapshot may still be written while
// it commits, such as a running service's mount: its usage is measured by
// the commit itself, as before, not ahead of it.
func WithUsageAtCommit(ctx context.Context) context.Context {
	return context.WithValue(ctx, usageAtCommitKey{}, true)
}

// measureUsage has the backend measure the active snapshot's disk usage
// before its commit, outside the manager's lock and the metadata database's
// transaction, and returns the context the commit records it from. The
// commit's caller has finished writing the snapshot, unless it says
// otherwise (WithUsageAtCommit), and the ref holds it. A backend that cannot
// record a measurement leaves the slot empty, and the commit measures as
// before.
func (sr *mutableRef) measureUsage(ctx context.Context) context.Context {
	if !sr.cm.measureUsageBeforeCommit || ctx.Value(usageAtCommitKey{}) != nil {
		return ctx
	}
	measured, slot := WithCommitUsage(ctx)
	if _, err := sr.cm.Snapshotter.Usage(measured, sr.SnapshotID()); err != nil || !slot.Measured {
		return ctx
	}
	return measured
}
