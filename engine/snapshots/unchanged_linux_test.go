package snapshots_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/mount"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// overlayDirs prepares a snapshot on an overlay store, writes its parent's
// files with writeParent straight into the parent's own directory, commits
// it, prepares a child on it, and returns the child and the two snapshots'
// own directories, as overlayfs layers them: what a process writes through
// the child's mount lands in its upper directory. No mount is made.
func overlayDirs(t testing.TB, store *testutil.Store, writeParent func(root string)) (child bkcache.MutableRef, upper, below string) {
	t.Helper()
	ctx := context.Background()
	owner, err := store.Leases.Create(ctx, leases.WithRandomID())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Manager.RemoveLease(context.Background(), owner.ID)) })
	ctx = leases.WithLease(ctx, owner.ID)

	parentMut, err := store.Manager.New(ctx, nil)
	require.NoError(t, err)
	mounts := snapshotMounts(t, store, parentMut.SnapshotID())
	require.Len(t, mounts, 1)
	require.Equal(t, "bind", mounts[0].Type, "a snapshot with no parent is its own directory")
	below = mounts[0].Source
	if writeParent != nil {
		writeParent(below)
	}
	parent, err := parentMut.Commit(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, parent.Release(context.Background())) })

	child, err = store.Manager.New(ctx, parent)
	require.NoError(t, err)
	t.Cleanup(func() { _ = child.Release(context.Background()) })
	mounts = snapshotMounts(t, store, child.SnapshotID())
	require.Len(t, mounts, 1)
	require.Equal(t, "overlay", mounts[0].Type)
	for _, o := range mounts[0].Options {
		if dir, ok := strings.CutPrefix(o, "upperdir="); ok {
			upper = dir
		}
		if dirs, ok := strings.CutPrefix(o, "lowerdir="); ok {
			require.Equal(t, below, strings.Split(dirs, ":")[0], "the parent is the layer below")
		}
	}
	require.NotEmpty(t, upper)
	return child, upper, below
}

// snapshotMounts returns how the snapshot id would be mounted, mounting
// nothing.
func snapshotMounts(t testing.TB, store *testutil.Store, id string) []mount.Mount {
	t.Helper()
	mountable, err := store.Snapshots.Mounts(context.Background(), id)
	require.NoError(t, err)
	mounts, release, err := mountable.Mount()
	require.NoError(t, err)
	if release != nil {
		require.NoError(t, release())
	}
	return mounts
}

func unchanged(t *testing.T, ref bkcache.MutableRef) bool {
	t.Helper()
	ok, err := ref.Unchanged(context.Background())
	require.NoError(t, err)
	return ok
}

// A snapshot prepared on a parent and left alone holds no change; any entry
// in its own directory, or a root that differs from its parent's, is one.
func TestUnchanged(t *testing.T) {
	t.Parallel()
	lowerFile := func(root string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, "kept"), []byte("kept"), 0o644))
		require.NoError(t, os.Mkdir(filepath.Join(root, "dir"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "dir", "inner"), []byte("inner"), 0o644))
	}

	t.Run("left alone", func(t *testing.T) {
		t.Parallel()
		child, _, _ := overlayDirs(t, testutil.NewOverlayStore(t), lowerFile)
		require.True(t, unchanged(t, child))
	})

	t.Run("a file added", func(t *testing.T) {
		t.Parallel()
		child, upper, _ := overlayDirs(t, testutil.NewOverlayStore(t), lowerFile)
		require.NoError(t, os.WriteFile(filepath.Join(upper, "new"), []byte("new"), 0o644))
		require.False(t, unchanged(t, child))
	})

	t.Run("a file deleted from below", func(t *testing.T) {
		t.Parallel()
		store := testutil.NewOverlayStore(t)
		if err := store.CanMakeWhiteouts(); err != nil {
			t.Skipf("the store can't hold whiteouts: %v", err)
		}
		child, upper, _ := overlayDirs(t, store, lowerFile)
		require.NoError(t, unix.Mknod(filepath.Join(upper, "kept"), unix.S_IFCHR, 0))
		require.False(t, unchanged(t, child))
	})

	t.Run("a directory replaced", func(t *testing.T) {
		t.Parallel()
		store := testutil.NewOverlayStore(t)
		if err := store.CanMakeOpaque(); err != nil {
			t.Skipf("the store can't mark a directory opaque: %v", err)
		}
		child, upper, _ := overlayDirs(t, store, lowerFile)
		require.NoError(t, os.Mkdir(filepath.Join(upper, "dir"), 0o755))
		require.NoError(t, unix.Setxattr(filepath.Join(upper, "dir"), "trusted.overlay.opaque", []byte("y"), 0))
		require.False(t, unchanged(t, child))
	})

	t.Run("the root's mode changed", func(t *testing.T) {
		t.Parallel()
		child, upper, below := overlayDirs(t, testutil.NewOverlayStore(t), lowerFile)
		fi, err := os.Stat(below)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(upper, fi.Mode().Perm()^0o070))
		require.False(t, unchanged(t, child))
	})

	t.Run("the root's owner changed", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() != 0 {
			t.Skip("changing an owner takes root")
		}
		child, upper, below := overlayDirs(t, testutil.NewOverlayStore(t), lowerFile)
		var st unix.Stat_t
		require.NoError(t, unix.Lstat(below, &st))
		require.NoError(t, os.Lchown(upper, int(st.Uid)+1, int(st.Gid)))
		require.False(t, unchanged(t, child))
	})

	t.Run("an attribute set on the root", func(t *testing.T) {
		t.Parallel()
		child, upper, _ := overlayDirs(t, testutil.NewOverlayStore(t), lowerFile)
		if err := unix.Setxattr(upper, "user.dagger.test", []byte("set"), 0); errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM) {
			t.Skipf("the store can't hold user attributes: %v", err)
		} else {
			require.NoError(t, err)
		}
		require.False(t, unchanged(t, child))
	})

	// An attribute set on the root with no value is set all the same.
	t.Run("an empty attribute set on the root", func(t *testing.T) {
		t.Parallel()
		child, upper, _ := overlayDirs(t, testutil.NewOverlayStore(t), lowerFile)
		if err := unix.Setxattr(upper, "user.dagger.test", nil, 0); errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM) {
			t.Skipf("the store can't hold user attributes: %v", err)
		} else {
			require.NoError(t, err)
		}
		require.False(t, unchanged(t, child))
	})

	// Only the mount's own namespace is overlay bookkeeping: without
	// userxattr, a user.overlay attribute is a process's, and so is an
	// escaped trusted.overlay.overlay one.
	for _, name := range []string{"user.overlay.note", "trusted.overlay.overlay.note"} {
		t.Run("the root's "+name, func(t *testing.T) {
			t.Parallel()
			store := testutil.NewOverlayStore(t)
			child, upper, _ := overlayDirs(t, store, lowerFile)
			for _, o := range snapshotMounts(t, store, child.SnapshotID())[0].Options {
				if o == "userxattr" {
					t.Skip("the store's mounts keep their bookkeeping in user attributes")
				}
			}
			if err := unix.Setxattr(upper, name, []byte("set"), 0); errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EPERM) {
				t.Skipf("the store can't hold %s: %v", name, err)
			} else {
				require.NoError(t, err)
			}
			require.False(t, unchanged(t, child))
		})
	}

	t.Run("the root opaque", func(t *testing.T) {
		t.Parallel()
		store := testutil.NewOverlayStore(t)
		if err := store.CanMakeOpaque(); err != nil {
			t.Skipf("the store can't mark a directory opaque: %v", err)
		}
		child, upper, _ := overlayDirs(t, store, lowerFile)
		require.NoError(t, unix.Setxattr(upper, "trusted.overlay.opaque", []byte("y"), 0))
		require.False(t, unchanged(t, child))
	})

	// A file made and removed at the root leaves nothing in the snapshot's
	// own directory, only its root's times changed: unchanged, as its layer
	// and the content digests, which leave the root's times out, say.
	t.Run("a file made and removed at the root", func(t *testing.T) {
		t.Parallel()
		child, upper, _ := overlayDirs(t, testutil.NewOverlayStore(t), lowerFile)
		tmp := filepath.Join(upper, "tmp")
		require.NoError(t, os.WriteFile(tmp, []byte("tmp"), 0o644))
		require.NoError(t, os.Remove(tmp))
		require.NoError(t, os.Chtimes(upper, time.Now(), time.Now().Add(time.Hour)))
		require.True(t, unchanged(t, child))
	})

	// One made and removed in a directory below leaves that directory's
	// copy in the snapshot's own directory: changed, as it is committed
	// today, without walking what the snapshot holds.
	t.Run("a file made and removed in a directory", func(t *testing.T) {
		t.Parallel()
		child, upper, below := overlayDirs(t, testutil.NewOverlayStore(t), lowerFile)
		fi, err := os.Stat(filepath.Join(below, "dir"))
		require.NoError(t, err)
		require.NoError(t, os.Mkdir(filepath.Join(upper, "dir"), fi.Mode().Perm()))
		require.False(t, unchanged(t, child))
	})

	t.Run("no parent", func(t *testing.T) {
		t.Parallel()
		store := testutil.NewOverlayStore(t)
		ctx := context.Background()
		owner, err := store.Leases.Create(ctx, leases.WithRandomID())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Manager.RemoveLease(context.Background(), owner.ID)) })
		mut, err := store.Manager.New(leases.WithLease(ctx, owner.ID), nil)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, mut.Release(context.Background())) })
		require.False(t, unchanged(t, mut))
	})

	// The native snapshotter copies the parent into the snapshot, so it
	// can't tell: it reports a change, and the snapshot is committed.
	t.Run("native snapshotter", func(t *testing.T) {
		t.Parallel()
		store := testutil.NewStore(t)
		parent, owner := store.Build(t, nil, "kept", "kept")
		ctx := leases.WithLease(context.Background(), owner)
		mut, err := store.Manager.New(ctx, parent)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, mut.Release(context.Background())) })
		require.False(t, unchanged(t, mut))
	})
}

// BenchmarkExecOutputCommit times turning an exec's writable output into
// its result, as it was (commit) and as it is (the check, then the input
// reopened or a commit), for an output left alone and one the exec wrote
// many files to. The check reads one entry of the output's own directory
// and the two roots, whatever was written.
func BenchmarkExecOutputCommit(b *testing.B) {
	for _, files := range []int{0, 100_000} {
		for _, check := range []bool{false, true} {
			b.Run(fmt.Sprintf("files=%d/check=%v", files, check), func(b *testing.B) {
				store := testutil.NewOverlayStore(b)
				ctx := context.Background()
				b.ResetTimer()
				for range b.N {
					b.StopTimer()
					child, upper, _ := overlayDirs(b, store, nil)
					if files > 0 {
						dir := filepath.Join(upper, "out")
						require.NoError(b, os.Mkdir(dir, 0o755))
						for i := range files {
							require.NoError(b, os.WriteFile(filepath.Join(dir, strconv.Itoa(i)), nil, 0o644))
						}
					}
					b.StartTimer()
					var out bkcache.ImmutableRef
					var err error
					if check {
						var same bool
						same, err = child.Unchanged(ctx)
						require.NoError(b, err)
						if same {
							parentID := snapshotParent(b, store, child.SnapshotID())
							require.NoError(b, child.Release(ctx))
							out, err = store.Manager.GetBySnapshotID(ctx, parentID)
						} else {
							out, err = child.Commit(ctx)
						}
					} else {
						out, err = child.Commit(ctx)
					}
					require.NoError(b, err)
					b.StopTimer()
					require.NoError(b, out.Release(ctx))
					b.StartTimer()
				}
			})
		}
	}
}

func snapshotParent(t testing.TB, store *testutil.Store, id string) string {
	t.Helper()
	info, err := store.Snapshots.Stat(context.Background(), id)
	require.NoError(t, err)
	return info.Parent
}
