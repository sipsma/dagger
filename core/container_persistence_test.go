package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/stretchr/testify/require"
)

func TestContainerEncodePersistedObjectRetainedCompletedLazyUsesReadyForm(t *testing.T) {
	t.Parallel()

	srv := newCoreDagqlServerForTest(t, &Query{})
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*Container]{}))
	parent := containerPersistenceTestResult(t, srv, "container-parent", NewContainer(Platform{
		OS:           "linux",
		Architecture: "amd64",
	}))

	completedState := NewLazyState()
	completedState.LazyInitComplete = true
	ctr := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	ctr.Lazy = &ContainerWithEntrypointLazy{
		LazyState: completedState,
		Parent:    parent,
		Args:      []string{"sh"},
	}

	enc, err := ctr.EncodePersistedObject(context.Background(), &cacheVolumeTestPersistedObjectCache{resultID: 17})
	require.NoError(t, err)

	var payload persistedContainerPayload
	require.NoError(t, json.Unmarshal(enc.JSON, &payload))
	require.Equal(t, persistedContainerFormReady, payload.Form)
	require.NotEmpty(t, payload.LazyJSON)
}

func TestContainerEncodePersistedObjectPendingLazyUsesLazyForm(t *testing.T) {
	t.Parallel()

	srv := newCoreDagqlServerForTest(t, &Query{})
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*Container]{}))
	parent := containerPersistenceTestResult(t, srv, "container-parent", NewContainer(Platform{
		OS:           "linux",
		Architecture: "amd64",
	}))

	ctr := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	ctr.Lazy = &ContainerWithEntrypointLazy{
		LazyState: NewLazyState(),
		Parent:    parent,
		Args:      []string{"sh"},
	}

	enc, err := ctr.EncodePersistedObject(context.Background(), &cacheVolumeTestPersistedObjectCache{resultID: 17})
	require.NoError(t, err)

	var payload persistedContainerPayload
	require.NoError(t, json.Unmarshal(enc.JSON, &payload))
	require.Equal(t, persistedContainerFormLazy, payload.Form)
	require.NotEmpty(t, payload.LazyJSON)
}

func TestDirectoryEncodePersistedObjectSnapshotAlsoIncludesRecipe(t *testing.T) {
	t.Parallel()

	srv := newCoreDagqlServerForTest(t, &Query{})
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*Directory]{}))
	parent := directoryPersistenceTestResult(t, srv, "directory-parent", &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
	})

	completedState := NewLazyState()
	completedState.LazyInitComplete = true
	snapshot := new(LazyAccessor[bkcache.ImmutableRef, *Directory])
	snapshot.setValue(&cacheVolumeTestImmutableRef{snapshotID: "dir-snapshot"})
	dir := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Snapshot: snapshot,
		Lazy: &DirectoryWithNewFileLazy{
			LazyState: completedState,
			Parent:    parent,
			Dest:      "hello.txt",
			Content:   []byte("hello"),
		},
	}

	enc, err := dir.EncodePersistedObject(context.Background(), &cacheVolumeTestPersistedObjectCache{resultID: 17})
	require.NoError(t, err)

	var payload persistedDirectoryPayload
	require.NoError(t, json.Unmarshal(enc.JSON, &payload))
	require.Equal(t, persistedDirectoryFormSnapshot, payload.Form)
	require.Equal(t, persistedDirectoryLazyKindWithNewFile, payload.LazyKind)
	require.NotEmpty(t, payload.LazyJSON)
	require.Equal(t, []dagql.PersistedSnapshotRefLink{{
		RefKey: "dir-snapshot",
		Role:   "snapshot",
	}}, enc.SnapshotLinks)
}

func TestFileEncodePersistedObjectSnapshotAlsoIncludesRecipe(t *testing.T) {
	t.Parallel()

	srv := newCoreDagqlServerForTest(t, &Query{})
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*File]{}))
	parent := filePersistenceTestResult(t, srv, "file-parent", &File{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
	})

	completedState := NewLazyState()
	completedState.LazyInitComplete = true
	snapshot := new(LazyAccessor[bkcache.ImmutableRef, *File])
	snapshot.setValue(&cacheVolumeTestImmutableRef{snapshotID: "file-snapshot"})
	file := &File{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Snapshot: snapshot,
		Lazy: &FileWithNameLazy{
			LazyState: completedState,
			Parent:    parent,
			Filename:  "renamed.txt",
		},
	}

	enc, err := file.EncodePersistedObject(context.Background(), &cacheVolumeTestPersistedObjectCache{resultID: 17})
	require.NoError(t, err)

	var payload persistedFilePayload
	require.NoError(t, json.Unmarshal(enc.JSON, &payload))
	require.Equal(t, persistedFileFormSnapshot, payload.Form)
	require.Equal(t, persistedFileLazyKindWithName, payload.LazyKind)
	require.NotEmpty(t, payload.LazyJSON)
	require.Equal(t, []dagql.PersistedSnapshotRefLink{{
		RefKey: "file-snapshot",
		Role:   "snapshot",
	}}, enc.SnapshotLinks)
}

func containerPersistenceTestResult(t *testing.T, srv *dagql.Server, op string, ctr *Container) dagql.ObjectResult[*Container] {
	t.Helper()

	res, err := dagql.NewObjectResultForCall(ctr, srv, &dagql.ResultCall{
		Kind:        dagql.ResultCallKindSynthetic,
		SyntheticOp: op,
		Type:        dagql.NewResultCallType((&Container{}).Type()),
	})
	require.NoError(t, err)
	return res
}

func directoryPersistenceTestResult(t *testing.T, srv *dagql.Server, op string, dir *Directory) dagql.ObjectResult[*Directory] {
	t.Helper()

	res, err := dagql.NewObjectResultForCall(dir, srv, &dagql.ResultCall{
		Kind:        dagql.ResultCallKindSynthetic,
		SyntheticOp: op,
		Type:        dagql.NewResultCallType((&Directory{}).Type()),
	})
	require.NoError(t, err)
	return res
}

func filePersistenceTestResult(t *testing.T, srv *dagql.Server, op string, file *File) dagql.ObjectResult[*File] {
	t.Helper()

	res, err := dagql.NewObjectResultForCall(file, srv, &dagql.ResultCall{
		Kind:        dagql.ResultCallKindSynthetic,
		SyntheticOp: op,
		Type:        dagql.NewResultCallType((&File{}).Type()),
	})
	require.NoError(t, err)
	return res
}
