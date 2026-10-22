package snapshots_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/leases"
	"github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/testutil"
	"github.com/stretchr/testify/require"
)

// A mutable snapshot's commit, with the backend recording a measurement, has
// the snapshot's usage measured first: outside the manager's lock, so that
// other refs are not held up by the walk, and handed to the backend's
// commit. The committed snapshot's usage is the whole snapshot either way.
//
// A commit whose snapshot may still be written while it commits, as a
// running service's is, is measured by the commit itself.
func TestMutableCommitMeasuresUsageFirst(t *testing.T) {
	for _, tc := range []struct {
		name          string
		usageFirst    bool
		usageAtCommit bool
		measured      bool
	}{
		{name: "usage first off"},
		{name: "usage first", usageFirst: true, measured: true},
		{name: "usage at commit", usageFirst: true, usageAtCommit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testutil.NewStoreWith(t, testutil.StoreOptions{Overlay: true, OverlayUsageFirst: tc.usageFirst})
			owner, err := store.Leases.Create(context.Background(), leases.WithRandomID())
			require.NoError(t, err)
			ctx := leases.WithLease(context.Background(), owner.ID)

			var measuredOutsideLock, measurementCommitted bool
			store.BeforeUsage = func(ctx context.Context, _ string) {
				if snapshots.CommitUsageFromContext(ctx) == nil {
					return
				}
				// A call that takes the manager's lock completes meanwhile.
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, _ = store.OverlayManager.Get(context.Background(), "no-such-snapshot")
				}()
				select {
				case <-done:
					measuredOutsideLock = true
				case <-time.After(10 * time.Second):
				}
			}
			store.BeforeCommit = func(ctx context.Context, _, _ string) {
				slot := snapshots.CommitUsageFromContext(ctx)
				measurementCommitted = slot != nil && slot.Measured
			}

			mut, err := store.OverlayManager.New(ctx, nil)
			require.NoError(t, err)
			mounted, err := mut.Mount(ctx, false)
			require.NoError(t, err)
			mounter := snapshots.LocalMounter(mounted)
			root, err := mounter.Mount()
			require.NoError(t, err)
			for i := range 10 {
				require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprint(i)), make([]byte, 5000), 0o644))
			}
			require.NoError(t, mounter.Unmount())
			commitCtx := ctx
			if tc.usageAtCommit {
				commitCtx = snapshots.WithUsageAtCommit(ctx)
			}
			ref, err := mut.Commit(commitCtx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, ref.Release(context.Background())) })

			require.Equal(t, tc.measured, measuredOutsideLock, "measured before the commit, outside the manager's lock")
			require.Equal(t, tc.measured, measurementCommitted, "the measurement reaches the backend's commit")
			usage, err := store.Overlay.Usage(ctx, ref.SnapshotID())
			require.NoError(t, err)
			require.EqualValues(t, 11, usage.Inodes, "ten files and their directory")
			require.GreaterOrEqual(t, usage.Size, int64(10*5000))
		})
	}
}
