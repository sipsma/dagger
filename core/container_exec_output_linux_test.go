package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/leases"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/testutil"
	"github.com/stretchr/testify/require"
)

// execOutputState is an exec's writable output on store: a snapshot
// prepared on input, as the exec prepares a writable mount's, with its own
// directory, where what the exec writes lands. A nil input is a mount with
// none.
func execOutputState(t *testing.T, store *testutil.Store, dest string, input bkcache.ImmutableRef) (*execMountState, string) {
	t.Helper()
	ctx := context.Background()
	owner, err := store.Leases.Create(ctx, leases.WithRandomID())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Manager.RemoveLease(context.Background(), owner.ID)) })
	mutable, err := store.Manager.New(leases.WithLease(ctx, owner.ID), input)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mutable.Release(context.Background()) })
	mountable, err := store.Snapshots.Mounts(ctx, mutable.SnapshotID())
	require.NoError(t, err)
	mounts, release, err := mountable.Mount()
	require.NoError(t, err)
	if release != nil {
		require.NoError(t, release())
	}
	require.Len(t, mounts, 1)
	own := mounts[0].Source
	for _, o := range mounts[0].Options {
		if dir, ok := strings.CutPrefix(o, "upperdir="); ok {
			own = dir
		}
	}
	state := &execMountState{Dest: dest, OutputMutable: mutable}
	if input != nil {
		state.SourceRef = input
	}
	return state, own
}

// overlayInput is a committed snapshot of an overlay store holding name,
// written straight into its own directory.
func overlayInput(t *testing.T, store *testutil.Store, name string) bkcache.ImmutableRef {
	t.Helper()
	state, own := execOutputState(t, store, "/", nil)
	require.NoError(t, os.WriteFile(filepath.Join(own, name), []byte("input"), 0o644))
	ref, err := state.OutputMutable.Commit(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ref.Release(context.Background())) })
	return ref
}

// An exec's writable output the exec didn't change is its input, as a
// read-only mount's is; one it changed, or with no input, is a new snapshot.
func TestCommitExecOutput(t *testing.T) {
	t.Parallel()
	commit := func(t *testing.T, store *testutil.Store, state *execMountState) bkcache.ImmutableRef {
		t.Helper()
		ref, err := commitExecOutput(context.Background(), store.Manager, state)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, ref.Release(context.Background())) })
		require.Nil(t, state.OutputMutable, "the output's new snapshot is consumed")
		return ref
	}

	for _, dest := range []string{"/", "/mnt", "/.dagger-cli"} {
		t.Run("untouched "+dest, func(t *testing.T) {
			t.Parallel()
			store := testutil.NewOverlayStore(t)
			input := overlayInput(t, store, "tool")
			state, _ := execOutputState(t, store, dest, input)
			state.Selector = "/sub"
			out := commit(t, store, state)
			require.Equal(t, input.SnapshotID(), out.SnapshotID())
		})
	}

	t.Run("written", func(t *testing.T) {
		t.Parallel()
		store := testutil.NewOverlayStore(t)
		input := overlayInput(t, store, "tool")
		state, own := execOutputState(t, store, "/mnt", input)
		require.NoError(t, os.WriteFile(filepath.Join(own, "out"), []byte("written"), 0o644))
		out := commit(t, store, state)
		require.NotEqual(t, input.SnapshotID(), out.SnapshotID())
	})

	t.Run("no input", func(t *testing.T) {
		t.Parallel()
		store := testutil.NewOverlayStore(t)
		state, _ := execOutputState(t, store, "/mnt", nil)
		out := commit(t, store, state)
		require.NotEmpty(t, out.SnapshotID())
	})

	t.Run("native snapshotter", func(t *testing.T) {
		t.Parallel()
		store := testutil.NewStore(t)
		input, _ := store.Build(t, nil, "tool", "input")
		state, _ := execOutputState(t, store, "/mnt", input)
		out := commit(t, store, state)
		require.NotEqual(t, input.SnapshotID(), out.SnapshotID())
	})
}
