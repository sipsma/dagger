package core

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestCachemoneyDecodeDirectoryInstallsRemoteSnapshotPlan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceManager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"dir-snapshot": cachemoneyRemotePlanTestRef("dir-snapshot", "dir-diff", "dir-blob"),
		},
	}
	sourceCache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "source.db"), sourceManager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sourceCache.Close(context.Background()))
	})
	sourceSrv, sourceQuery := cachemoneyRemotePlanTestServer(t, sourceManager)
	sourceCtx := ContextWithQuery(dagql.ContextWithCache(ctx, sourceCache), sourceQuery)

	call := cachemoneyRemotePlanTestCall("remote-plan-directory", (&Directory{}).Type())
	dir := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	dir.Dir.setValue("/")
	dir.Snapshot.setValue(sourceManager.immutableBySnapshotID["dir-snapshot"])
	_, err = sourceCache.GetOrInitCall(sourceCtx, "source-session", sourceSrv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(dir, sourceSrv, call)
	})
	require.NoError(t, err)

	destCache, destSrv, destCtx := cachemoneyRemotePlanTestImport(t, ctx, sourceCache)
	resultID := cachemoneyRemotePlanTestResultID(t, destCtx, destCache, "snapshot")
	loaded, err := destCache.LoadResultByResultID(destCtx, "", destSrv, resultID)
	require.NoError(t, err)
	loadedDir := loaded.(dagql.ObjectResult[*Directory]).Self()
	require.True(t, loadedDir.Snapshot.hasMaterializer())
	_, ok := loadedDir.Snapshot.Peek()
	require.False(t, ok)
}

func TestCachemoneyDecodeFileInstallsRemoteSnapshotPlan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceManager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"file-snapshot": cachemoneyRemotePlanTestRef("file-snapshot", "file-diff", "file-blob"),
		},
	}
	sourceCache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "source.db"), sourceManager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sourceCache.Close(context.Background()))
	})
	sourceSrv, sourceQuery := cachemoneyRemotePlanTestServer(t, sourceManager)
	sourceCtx := ContextWithQuery(dagql.ContextWithCache(ctx, sourceCache), sourceQuery)

	call := cachemoneyRemotePlanTestCall("remote-plan-file", (&File{}).Type())
	file := &File{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		File:     new(LazyAccessor[string, *File]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File]),
	}
	file.File.setValue("/out.txt")
	file.Snapshot.setValue(sourceManager.immutableBySnapshotID["file-snapshot"])
	_, err = sourceCache.GetOrInitCall(sourceCtx, "source-session", sourceSrv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(file, sourceSrv, call)
	})
	require.NoError(t, err)

	destCache, destSrv, destCtx := cachemoneyRemotePlanTestImport(t, ctx, sourceCache)
	resultID := cachemoneyRemotePlanTestResultID(t, destCtx, destCache, "snapshot")
	loaded, err := destCache.LoadResultByResultID(destCtx, "", destSrv, resultID)
	require.NoError(t, err)
	loadedFile := loaded.(dagql.ObjectResult[*File]).Self()
	require.True(t, loadedFile.Snapshot.hasMaterializer())
	_, ok := loadedFile.Snapshot.Peek()
	require.False(t, ok)
}

func TestCachemoneyDecodeContainerInstallsIndependentRemoteSnapshotPlans(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceManager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"fs-snapshot":    cachemoneyRemotePlanTestRef("fs-snapshot", "fs-diff", "fs-blob"),
			"meta-snapshot":  cachemoneyRemotePlanTestRef("meta-snapshot", "meta-diff", "meta-blob"),
			"mount-snapshot": cachemoneyRemotePlanTestRef("mount-snapshot", "mount-diff", "mount-blob"),
		},
	}
	sourceCache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "source.db"), sourceManager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sourceCache.Close(context.Background()))
	})
	sourceSrv, sourceQuery := cachemoneyRemotePlanTestServer(t, sourceManager)
	sourceCtx := ContextWithQuery(dagql.ContextWithCache(ctx, sourceCache), sourceQuery)

	rootFS := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	rootFS.Dir.setValue("/")
	rootFS.Snapshot.setValue(sourceManager.immutableBySnapshotID["fs-snapshot"])
	mountDir := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	mountDir.Dir.setValue("/")
	mountDir.Snapshot.setValue(sourceManager.immutableBySnapshotID["mount-snapshot"])
	container := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	container.MetaSnapshot.setValue(sourceManager.immutableBySnapshotID["meta-snapshot"])
	container.FS.setValue(rootFS)
	container.Mounts = ContainerMounts{{
		Target:          "/mnt",
		DirectorySource: new(LazyAccessor[*Directory, *Container]),
	}}
	container.Mounts[0].DirectorySource.setValue(mountDir)

	call := cachemoneyRemotePlanTestCall("remote-plan-container", (&Container{}).Type())
	_, err = sourceCache.GetOrInitCall(sourceCtx, "source-session", sourceSrv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(container, sourceSrv, call)
	})
	require.NoError(t, err)

	destCache, destSrv, destCtx := cachemoneyRemotePlanTestImport(t, ctx, sourceCache)
	resultID := cachemoneyRemotePlanTestResultID(t, destCtx, destCache, "meta")
	loaded, err := destCache.LoadResultByResultID(destCtx, "", destSrv, resultID)
	require.NoError(t, err)
	loadedContainer := loaded.(dagql.ObjectResult[*Container]).Self()
	require.True(t, loadedContainer.MetaSnapshot.hasMaterializer())
	_, ok := loadedContainer.MetaSnapshot.Peek()
	require.False(t, ok)

	loadedRootFS, ok := loadedContainer.FS.Peek()
	require.True(t, ok)
	require.True(t, loadedRootFS.Snapshot.hasMaterializer())
	_, ok = loadedRootFS.Snapshot.Peek()
	require.False(t, ok)

	require.Len(t, loadedContainer.Mounts, 1)
	loadedMount, ok := loadedContainer.Mounts[0].DirectorySource.Peek()
	require.True(t, ok)
	require.True(t, loadedMount.Snapshot.hasMaterializer())
	_, ok = loadedMount.Snapshot.Peek()
	require.False(t, ok)
}

func cachemoneyRemotePlanTestServer(t *testing.T, manager bkcache.SnapshotManager) (*dagql.Server, *Query) {
	t.Helper()

	query := &Query{
		Server: &cacheVolumeTestQueryServer{
			mockServer:   &mockServer{},
			cacheManager: manager,
		},
	}
	srv := newCoreDagqlServerForTest(t, query)
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*Container]{}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*Directory]{}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*File]{}))
	return srv, query
}

func cachemoneyRemotePlanTestCall(field string, typ *ast.Type) *dagql.ResultCall {
	return &dagql.ResultCall{
		Kind:  dagql.ResultCallKindField,
		Type:  dagql.NewResultCallType(typ),
		Field: field,
	}
}

func cachemoneyRemotePlanTestImport(t *testing.T, ctx context.Context, sourceCache *dagql.Cache) (*dagql.Cache, *dagql.Server, context.Context) {
	t.Helper()

	exportPath := filepath.Join(t.TempDir(), "metadata.db")
	prepared, err := sourceCache.PrepareCachemoneyExport(ctx, exportPath)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, prepared.Release(context.Background()))
	})

	destCache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "dest.db"), &cacheVolumeTestSnapshotManager{}, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, destCache.Close(context.Background()))
	})
	require.NoError(t, destCache.ImportCachemoneyMetadata(ctx, dagql.CachemoneyImportSource{
		ID:             "remote-source",
		MetadataDBPath: exportPath,
	}))
	destSrv, destQuery := cachemoneyRemotePlanTestServer(t, nil)
	destCtx := ContextWithQuery(dagql.ContextWithCache(ctx, destCache), destQuery)
	return destCache, destSrv, destCtx
}

func cachemoneyRemotePlanTestResultID(t *testing.T, ctx context.Context, cache *dagql.Cache, role string) uint64 {
	t.Helper()

	for resultID := uint64(1); resultID <= 32; resultID++ {
		_, ok, err := cache.PersistedRemoteSnapshotChainByResultID(ctx, resultID, role)
		if err == nil && ok {
			return resultID
		}
	}
	t.Fatalf("missing imported remote snapshot chain role %q", role)
	return 0
}

func cachemoneyRemotePlanTestRef(snapshotID, diffSeed, blobSeed string) *cacheVolumeTestImmutableRef {
	diffID := digest.FromString(diffSeed)
	blobDigest := digest.FromString(blobSeed)
	return &cacheVolumeTestImmutableRef{
		id:         snapshotID + "-id",
		snapshotID: snapshotID,
		exportChain: &bkcache.ExportChain{
			Layers: []bkcache.ExportLayer{{
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobDigest,
					Size:      123,
					Annotations: map[string]string{
						labels.LabelUncompressed: diffID.String(),
					},
				},
				Description: fmt.Sprintf("test layer %s", snapshotID),
			}},
		},
	}
}
