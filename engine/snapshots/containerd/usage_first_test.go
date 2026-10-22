package containerd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/core/snapshots/storage"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/containerd/v2/plugins/snapshots/overlay"
	bksnapshots "github.com/dagger/dagger/engine/snapshots"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

type usageFirstTestEnv struct {
	ctx  context.Context
	db   *metadata.DB
	sn   bksnapshots.Snapshotter
	root string
}

const usageFirstTestNS = "usage-first-test"

// remove removes a snapshot through the metadata database's own
// snapshotter: the engine's refuses removals.
func (env *usageFirstTestEnv) remove(t *testing.T, key string) {
	t.Helper()
	require.NoError(t, env.db.Snapshotter("overlayfs").Remove(namespaces.WithNamespace(env.ctx, usageFirstTestNS), key))
}

// newUsageFirstTestEnv opens overlay on a metadata store of its own, as the
// engine does, under a containerd metadata database; its backend wrapped by
// NewUsageFirstSnapshotter when wrap is set. Overlay's operations name
// mounts without making them, so nothing here needs privileges.
func newUsageFirstTestEnv(t *testing.T, wrap bool) *usageFirstTestEnv {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "overlay")
	require.NoError(t, os.MkdirAll(root, 0o700))
	ms, err := storage.NewMetaStore(filepath.Join(root, "metadata.db"))
	require.NoError(t, err)
	// userxattr is named so that construction does not probe a mount.
	ov, err := overlay.NewSnapshotter(root, overlay.WithMetaStore(ms), overlay.AsynchronousRemove, overlay.WithMountOptions([]string{"userxattr"}))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ov.Close()) })
	backend := ov
	if wrap {
		backend = NewUsageFirstSnapshotter(ov, ms, root)
	}
	store, err := local.NewStore(filepath.Join(dir, "content"))
	require.NoError(t, err)
	db, err := bolt.Open(filepath.Join(dir, "metadata.db"), 0o600, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	mdb := metadata.NewDB(db, store, map[string]snapshots.Snapshotter{"overlayfs": backend})
	ctx := context.Background()
	require.NoError(t, mdb.Init(ctx))
	return &usageFirstTestEnv{ctx: ctx, db: mdb, sn: NewSnapshotter("overlayfs", mdb.Snapshotter("overlayfs"), usageFirstTestNS), root: root}
}

// prepare makes an active snapshot with no parent and writes files in it.
func (env *usageFirstTestEnv) prepare(t *testing.T, key string, files int) string {
	t.Helper()
	require.NoError(t, env.sn.Prepare(env.ctx, key, ""))
	mountable, err := env.sn.Mounts(env.ctx, key)
	require.NoError(t, err)
	mounts, release, err := mountable.Mount()
	require.NoError(t, err)
	require.NoError(t, release())
	require.Len(t, mounts, 1)
	require.Equal(t, "bind", mounts[0].Type)
	upper := mounts[0].Source
	writeUsageFiles(t, upper, "f", files)
	return upper
}

func writeUsageFiles(t *testing.T, dir, prefix string, files int) {
	t.Helper()
	for i := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s%d", prefix, i)), make([]byte, 5000), 0o644))
	}
}

func (env *usageFirstTestEnv) usage(t *testing.T, key string) snapshots.Usage {
	t.Helper()
	usage, err := env.sn.Usage(env.ctx, key)
	require.NoError(t, err)
	return usage
}

// overlay's commit walks the snapshot's directory for its usage inside the
// metadata database's transaction. Through the wrapper, a snapshot measured
// before its commit is recorded with that measurement, so the commit walks
// nothing: a file written after the measurement is not counted, where
// overlay's own commit counts it.
func TestUsageFirstCommitRecordsTheMeasurement(t *testing.T) {
	for _, wrap := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped=%v", wrap), func(t *testing.T) {
			env := newUsageFirstTestEnv(t, wrap)
			upper := env.prepare(t, "active", 10)
			measuring, slot := bksnapshots.WithCommitUsage(env.ctx)
			measured, err := env.sn.Usage(measuring, "active")
			require.NoError(t, err)
			require.Equal(t, wrap, slot.Measured, "only the wrapper keeps the measurement")
			require.Equal(t, env.usage(t, "active"), measured, "the measurement is overlay's")

			writeUsageFiles(t, upper, "late", 1)
			require.NoError(t, env.sn.Commit(measuring, "committed", "active"))
			committed := env.usage(t, "committed")
			if wrap {
				require.Equal(t, slot.Usage, committed, "the commit records the measurement")
			} else {
				require.Equal(t, measured.Inodes+1, committed.Inodes, "overlay's commit walks again")
			}
		})
	}
}

// A commit through the wrapper with no measurement, or with one of another
// snapshot, measures as overlay's commit does.
func TestUsageFirstCommitWithoutItsMeasurement(t *testing.T) {
	committedUsage := func(t *testing.T, wrap bool, commit func(env *usageFirstTestEnv)) snapshots.Usage {
		env := newUsageFirstTestEnv(t, wrap)
		commit(env)
		return env.usage(t, "committed")
	}
	for name, commit := range map[string]func(t *testing.T) func(env *usageFirstTestEnv){
		"no measurement": func(t *testing.T) func(env *usageFirstTestEnv) {
			return func(env *usageFirstTestEnv) {
				env.prepare(t, "active", 10)
				require.NoError(t, env.sn.Commit(env.ctx, "committed", "active"))
			}
		},
		"another snapshot's measurement": func(t *testing.T) func(env *usageFirstTestEnv) {
			return func(env *usageFirstTestEnv) {
				env.prepare(t, "other", 3)
				measuring, _ := bksnapshots.WithCommitUsage(env.ctx)
				_, err := env.sn.Usage(measuring, "other")
				require.NoError(t, err)
				env.prepare(t, "active", 10)
				require.NoError(t, env.sn.Commit(measuring, "committed", "active"))
			}
		},
		"a measurement of the key before it was made again": func(t *testing.T) func(env *usageFirstTestEnv) {
			return func(env *usageFirstTestEnv) {
				env.prepare(t, "active", 3)
				measuring, _ := bksnapshots.WithCommitUsage(env.ctx)
				_, err := env.sn.Usage(measuring, "active")
				require.NoError(t, err)
				env.remove(t, "active")
				env.prepare(t, "active", 10)
				require.NoError(t, env.sn.Commit(measuring, "committed", "active"))
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			want := committedUsage(t, false, commit(t))
			require.Equal(t, want, committedUsage(t, true, commit(t)))
			require.EqualValues(t, 11, want.Inodes, "ten files and their directory")
		})
	}
}

// A commit canceled after its snapshot was measured fails, as overlay's walk
// fails it, and leaves the snapshot active.
func TestUsageFirstCommitCanceledAfterMeasurement(t *testing.T) {
	for _, wrap := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped=%v", wrap), func(t *testing.T) {
			env := newUsageFirstTestEnv(t, wrap)
			env.prepare(t, "active", 10)
			canceled, cancel := context.WithCancel(env.ctx)
			measuring, _ := bksnapshots.WithCommitUsage(canceled)
			_, err := env.sn.Usage(measuring, "active")
			require.NoError(t, err)
			cancel()
			require.ErrorIs(t, env.sn.Commit(measuring, "committed", "active"), context.Canceled)
			info, err := env.sn.Stat(env.ctx, "active")
			require.NoError(t, err)
			require.Equal(t, snapshots.KindActive, info.Kind)
			_, err = env.sn.Stat(env.ctx, "committed")
			require.Error(t, err)
		})
	}
}

// The metadata database's garbage collection finishes overlay's
// asynchronous removals through its Cleanup, which the wrapper keeps.
func TestUsageFirstKeepsCleanup(t *testing.T) {
	env := newUsageFirstTestEnv(t, true)
	env.prepare(t, "removed", 2)
	entries, err := os.ReadDir(filepath.Join(env.root, "snapshots"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	env.remove(t, "removed")
	_, err = env.db.GarbageCollect(env.ctx)
	require.NoError(t, err)
	entries, err = os.ReadDir(filepath.Join(env.root, "snapshots"))
	require.NoError(t, err)
	require.Empty(t, entries, "the removed snapshot's directory is cleaned up")
}
