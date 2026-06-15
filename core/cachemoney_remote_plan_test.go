package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine"
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

func TestCachemoneyHTTPStateNotModifiedHydratesRemoteSnapshot(t *testing.T) {
	t.Parallel()

	ctx := engine.ContextWithClientMetadata(context.Background(), &engine.ClientMetadata{
		ClientID:  "http-state-remote-cache-test-client",
		SessionID: "http-state-remote-cache-test-session",
	})
	seenIfNoneMatch := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenIfNoneMatch <- r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"http-etag"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(server.Close)

	sourceRef := &cacheVolumeTestImmutableRef{
		id:          "http-source-ref",
		snapshotID:  "http-source-snapshot",
		exportChain: &bkcache.ExportChain{},
	}
	sourceManager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"http-source-snapshot": sourceRef,
		},
	}
	sourceCache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "source.db"), sourceManager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sourceCache.Close(context.Background()))
	})
	sourceSrv, sourceQuery := cachemoneyRemotePlanTestServer(t, sourceManager)
	sourceCtx := ContextWithQuery(dagql.ContextWithCache(ctx, sourceCache), sourceQuery)

	state := &HTTPState{
		URL:           server.URL,
		ETag:          `"http-etag"`,
		ContentDigest: digest.FromString("http-body"),
		snapshot:      sourceRef,
	}
	call := cachemoneyRemotePlanTestCall("remote-plan-http-state", (&HTTPState{}).Type())
	_, err = sourceCache.GetOrInitCall(sourceCtx, "source-session", sourceSrv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(state, sourceSrv, call)
	})
	require.NoError(t, err)

	resolvedRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(resolvedRoot, httpStateCanonicalPath), []byte("cached http body"), 0o600))
	hydratedRef := &cacheVolumeTestImmutableRef{
		id:         "http-hydrated-ref",
		snapshotID: "http-hydrated-snapshot",
	}
	resolvedRef := &cacheVolumeTestImmutableRef{
		id:         "http-resolved-ref",
		snapshotID: "http-resolved-snapshot",
	}
	destManager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"http-hydrated-snapshot": hydratedRef,
		},
		importImageResult: hydratedRef,
		newResult: &cacheVolumeTestMutableRef{
			cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
				id:         "http-resolving-ref",
				snapshotID: "http-resolving-snapshot",
				mountDir:   resolvedRoot,
			},
			commitResult: resolvedRef,
		},
	}
	destCache, destSrv, destCtx := cachemoneyRemotePlanTestImportWithManager(t, ctx, sourceCache, destManager)
	resultID := cachemoneyRemotePlanTestResultID(t, destCtx, destCache, "snapshot")
	loaded, err := destCache.LoadResultByResultID(destCtx, "", destSrv, resultID)
	require.NoError(t, err)
	loadedState := loaded.(dagql.ObjectResult[*HTTPState])
	require.NotNil(t, loadedState.Self().snapshotMaterializer)
	require.Nil(t, loadedState.Self().snapshot)

	query, err := CurrentQuery(destCtx)
	require.NoError(t, err)
	fetched, err := loadedState.Self().Resolve(destCtx, query, loadedState.Result, dagql.Optional[dagql.String]{}, 0o600, "tini-amd64")
	require.NoError(t, err)
	require.Equal(t, `"http-etag"`, <-seenIfNoneMatch)
	require.Equal(t, digest.FromString("http-body"), fetched.ContentDigest)
	require.Len(t, destManager.importImageCalls, 1)
	require.Empty(t, destManager.importImageCalls[0].Layers)
	require.Equal(t, []struct{ leaseID, snapshotID string }{{
		leaseID:    fmt.Sprintf("dagql/result/%d/snapshot", resultID),
		snapshotID: "http-hydrated-snapshot",
	}}, destManager.attachCalls)
	require.Equal(t, []bkcache.ImmutableRef{hydratedRef}, destManager.newCalls)

	filePath, ok := fetched.File.File.Peek()
	require.True(t, ok)
	require.Equal(t, "tini-amd64", filePath)
	fileSnapshot, ok := fetched.File.Snapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "http-resolved-snapshot", fileSnapshot.SnapshotID())
}

func TestCachemoneyHTTPStateNotModifiedWithoutSnapshotChainKeepsHardError(t *testing.T) {
	t.Parallel()

	ctx := engine.ContextWithClientMetadata(context.Background(), &engine.ClientMetadata{
		ClientID:  "http-state-no-chain-test-client",
		SessionID: "http-state-no-chain-test-session",
	})
	seenIfNoneMatch := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenIfNoneMatch <- r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"http-etag"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(server.Close)

	manager := &cacheVolumeTestSnapshotManager{}
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx = ContextWithQuery(ctx, query)
	state := &HTTPState{
		URL:           server.URL,
		ETag:          `"http-etag"`,
		ContentDigest: digest.FromString("http-body"),
	}
	owner, err := dagql.NewObjectResultForCall(state, srv, cachemoneyRemotePlanTestCall("remote-plan-http-state-no-chain", (&HTTPState{}).Type()))
	require.NoError(t, err)

	fetched, err := state.Resolve(ctx, query, owner.Result, dagql.Optional[dagql.String]{}, 0o600, "tini-amd64")
	require.Error(t, err)
	require.Nil(t, fetched)
	require.Contains(t, err.Error(), "returned 304 without a cached snapshot")
	require.Equal(t, `"http-etag"`, <-seenIfNoneMatch)
	require.Empty(t, manager.importImageCalls)
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

	require.True(t, loadedContainer.FS.hasMaterializer())
	_, ok = loadedContainer.FS.Peek()
	require.False(t, ok)

	require.Len(t, loadedContainer.Mounts, 1)
	require.True(t, loadedContainer.Mounts[0].DirectorySource.hasMaterializer())
	_, ok = loadedContainer.Mounts[0].DirectorySource.Peek()
	require.False(t, ok)
}

func TestCachemoneyDecodeContainerRetainedRecipeFallbackKeepsLazy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceManager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"fs-snapshot": cachemoneyRemotePlanTestRef("fs-snapshot", "fs-diff", "fs-blob"),
		},
	}
	sourceCache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "source.db"), sourceManager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sourceCache.Close(context.Background()))
	})
	sourceSrv, sourceQuery := cachemoneyRemotePlanTestServer(t, sourceManager)
	sourceCtx := ContextWithQuery(dagql.ContextWithCache(ctx, sourceCache), sourceQuery)

	parentCall := cachemoneyRemotePlanTestCall("retained-recipe-parent", (&Container{}).Type())
	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parentAny, err := sourceCache.GetOrInitCall(sourceCtx, "source-session", sourceSrv, &dagql.CallRequest{
		ResultCall:    parentCall,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(parent, sourceSrv, parentCall)
	})
	require.NoError(t, err)
	parentRes := parentAny.(dagql.ObjectResult[*Container])

	rootFS := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	rootFS.Dir.setValue("/")
	rootFS.Snapshot.setValue(sourceManager.immutableBySnapshotID["fs-snapshot"])

	completedState := NewLazyState()
	completedState.LazyInitComplete = true
	childCall := cachemoneyRemotePlanTestCall("withDefaultArgs", (&Container{}).Type())
	child := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	child.FS.setValue(rootFS)
	child.Lazy = &ContainerWithDefaultArgsLazy{
		LazyState: completedState,
		Parent:    parentRes,
		Args:      []string{"sh"},
	}
	_, err = sourceCache.GetOrInitCall(sourceCtx, "source-session", sourceSrv, &dagql.CallRequest{
		ResultCall:    childCall,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(child, sourceSrv, childCall)
	})
	require.NoError(t, err)

	destCache, destSrv, destCtx := cachemoneyRemotePlanTestImport(t, ctx, sourceCache)
	resultID := cachemoneyRemotePlanTestResultID(t, destCtx, destCache, "fs")
	loaded, err := destCache.LoadResultByResultID(destCtx, "", destSrv, resultID)
	require.NoError(t, err)
	loadedContainer := loaded.(dagql.ObjectResult[*Container]).Self()
	require.True(t, loadedContainer.FS.hasMaterializer())
	require.NotNil(t, loadedContainer.Lazy)
	require.True(t, lazyPending(loadedContainer.Lazy))
}

func TestDirectoryWithDirectoryMaterializesRemoteBaseSnapshotAccessor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manager := &cacheVolumeTestSnapshotManager{}
	cache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "cache.db"), manager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, cache.Close(context.Background()))
	})
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx = ContextWithQuery(dagql.ContextWithCache(ctx, cache), query)

	parentRef := &cacheVolumeTestImmutableRef{id: "parent-ref", snapshotID: "parent-snapshot"}
	parentMaterializer := &lazyAccessorTestMaterializer[bkcache.ImmutableRef, *Directory]{
		value: parentRef,
		ok:    true,
	}
	parent := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	parent.Dir.setValue("/")
	parent.Snapshot.setMaterializer(parentMaterializer)
	parentCall := cachemoneyRemotePlanTestCall("remote-base-parent", (&Directory{}).Type())
	parentAny, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    parentCall,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(parent, srv, parentCall)
	})
	require.NoError(t, err)
	parentRes := parentAny.(dagql.ObjectResult[*Directory])

	sourceRef := &cacheVolumeTestImmutableRef{id: "source-ref", snapshotID: "source-snapshot"}
	source := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	source.Dir.setValue("/")
	source.Snapshot.setValue(sourceRef)
	sourceCall := cachemoneyRemotePlanTestCall("remote-base-source", (&Directory{}).Type())
	sourceAny, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    sourceCall,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(source, srv, sourceCall)
	})
	require.NoError(t, err)
	sourceRes := sourceAny.(dagql.ObjectResult[*Directory])

	err = (&Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}).WithDirectory(ctx, parentRes, "/copied", sourceRes, CopyFilter{}, "", nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "materialized directory: missing snapshot")
	require.Contains(t, err.Error(), "snapshotmanager.New failed")
	require.Equal(t, 1, parentMaterializer.calls)
	require.Len(t, manager.newCalls, 1)
	require.Equal(t, "parent-snapshot", manager.newCalls[0].SnapshotID())
}

func TestContainerWithMountedFileMaterializesRemoteSourceSnapshotAccessor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceRef := &cacheVolumeTestImmutableRef{id: "file-ref", snapshotID: "file-snapshot"}
	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"file-snapshot": sourceRef,
		},
	}
	cache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "cache.db"), manager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, cache.Close(context.Background()))
	})
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx = ContextWithQuery(dagql.ContextWithCache(ctx, cache), query)

	sourceMaterializer := &lazyAccessorTestMaterializer[bkcache.ImmutableRef, *File]{
		value: sourceRef,
		ok:    true,
	}
	source := &File{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		File:     new(LazyAccessor[string, *File]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File]),
	}
	source.File.setValue("/tool.bin")
	source.Snapshot.setMaterializer(sourceMaterializer)
	sourceCall := cachemoneyRemotePlanTestCall("remote-mounted-file-source", (&File{}).Type())
	sourceAny, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    sourceCall,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(source, srv, sourceCall)
	})
	require.NoError(t, err)
	sourceRes := sourceAny.(dagql.ObjectResult[*File])

	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parentCall := cachemoneyRemotePlanTestCall("remote-mounted-file-parent", (&Container{}).Type())
	parentAny, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    parentCall,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(parent, srv, parentCall)
	})
	require.NoError(t, err)
	parentRes := parentAny.(dagql.ObjectResult[*Container])

	container := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	_, err = container.WithMountedFile(ctx, parentRes, "/mounted/tool.bin", sourceRes, "", false)
	require.NoError(t, err)

	require.Equal(t, 1, sourceMaterializer.calls)
	require.Len(t, container.Mounts, 1)
	mounted, ok := container.Mounts[0].FileSource.Peek()
	require.True(t, ok)
	require.NotNil(t, mounted)
	filePath, ok := mounted.File.Peek()
	require.True(t, ok)
	require.Equal(t, "/tool.bin", filePath)
	snapshot, ok := mounted.Snapshot.Peek()
	require.True(t, ok)
	require.NotNil(t, snapshot)
	require.Equal(t, "file-snapshot", snapshot.SnapshotID())
	require.Equal(t, []string{"file-snapshot"}, manager.getBySnapshotIDCalls)
}

func TestContainerWithMountedDirectoryMaterializesRemoteSourceSnapshotAccessor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceRef := &cacheVolumeTestImmutableRef{id: "dir-ref", snapshotID: "dir-snapshot"}
	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"dir-snapshot": sourceRef,
		},
	}
	cache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "cache.db"), manager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, cache.Close(context.Background()))
	})
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx = ContextWithQuery(dagql.ContextWithCache(ctx, cache), query)

	sourceMaterializer := &lazyAccessorTestMaterializer[bkcache.ImmutableRef, *Directory]{
		value: sourceRef,
		ok:    true,
	}
	source := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	source.Dir.setValue("/")
	source.Snapshot.setMaterializer(sourceMaterializer)
	sourceCall := cachemoneyRemotePlanTestCall("remote-mounted-directory-source", (&Directory{}).Type())
	sourceAny, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    sourceCall,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(source, srv, sourceCall)
	})
	require.NoError(t, err)
	sourceRes := sourceAny.(dagql.ObjectResult[*Directory])

	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parentCall := cachemoneyRemotePlanTestCall("remote-mounted-directory-parent", (&Container{}).Type())
	parentAny, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    parentCall,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(parent, srv, parentCall)
	})
	require.NoError(t, err)
	parentRes := parentAny.(dagql.ObjectResult[*Container])

	container := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	_, err = container.WithMountedDirectory(ctx, parentRes, "/mounted", sourceRes, "", false)
	require.NoError(t, err)

	require.Equal(t, 1, sourceMaterializer.calls)
	require.Len(t, container.Mounts, 1)
	mounted, ok := container.Mounts[0].DirectorySource.Peek()
	require.True(t, ok)
	require.NotNil(t, mounted)
	dirPath, ok := mounted.Dir.Peek()
	require.True(t, ok)
	require.Equal(t, "/", dirPath)
	snapshot, ok := mounted.Snapshot.Peek()
	require.True(t, ok)
	require.NotNil(t, snapshot)
	require.Equal(t, "dir-snapshot", snapshot.SnapshotID())
	require.Equal(t, []string{"dir-snapshot"}, manager.getBySnapshotIDCalls)
}

func TestCloneContainerMountsPreservesFileSourceMaterializer(t *testing.T) {
	t.Parallel()

	srv, _ := cachemoneyRemotePlanTestServer(t, &cacheVolumeTestSnapshotManager{})
	mountFile := &File{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		File:     new(LazyAccessor[string, *File]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File]),
	}
	mountFile.File.setValue("/schema.json")
	sourceMaterializer := &lazyAccessorTestMaterializer[*File, *Container]{
		value: mountFile,
		ok:    true,
	}
	mounts := ContainerMounts{{
		Target:     "/schema.json",
		FileSource: new(LazyAccessor[*File, *Container]),
	}}
	mounts[0].FileSource.setMaterializer(sourceMaterializer)

	cloned, err := CloneContainerMounts(context.Background(), mounts)
	require.NoError(t, err)
	require.Len(t, cloned, 1)
	require.NotNil(t, cloned[0].FileSource)
	_, ok := cloned[0].FileSource.Peek()
	require.False(t, ok)
	require.True(t, cloned[0].FileSource.hasMaterializer())

	res, err := dagql.NewObjectResultForCall(NewContainer(Platform{OS: "linux", Architecture: "amd64"}), srv, cachemoneyRemotePlanTestCall("materializer-owner", (&Container{}).Type()))
	require.NoError(t, err)
	materialized, err := cloned[0].FileSource.GetOrEval(context.Background(), res.Result)
	require.NoError(t, err)
	require.Same(t, mountFile, materialized)
	require.Equal(t, 1, sourceMaterializer.calls)
}

func TestMaterializeContainerStateFromParentResolvesClonedFileMountMaterializer(t *testing.T) {
	t.Parallel()

	srv, _ := cachemoneyRemotePlanTestServer(t, &cacheVolumeTestSnapshotManager{})
	mountFile := &File{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		File:     new(LazyAccessor[string, *File]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File]),
	}
	mountFile.File.setValue("/schema.json")
	sourceMaterializer := &lazyAccessorTestMaterializer[*File, *Container]{
		value: mountFile,
		ok:    true,
	}
	mounts := ContainerMounts{{
		Target:     "/schema.json",
		FileSource: new(LazyAccessor[*File, *Container]),
	}}
	mounts[0].FileSource.setMaterializer(sourceMaterializer)

	cloned, err := CloneContainerMounts(context.Background(), mounts)
	require.NoError(t, err)
	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parent.Mounts = cloned
	parentRes, err := dagql.NewObjectResultForCall(parent, srv, cachemoneyRemotePlanTestCall("materializer-parent", (&Container{}).Type()))
	require.NoError(t, err)

	dst := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	require.NoError(t, materializeContainerStateFromParent(context.Background(), dst, parentRes))
	require.Len(t, dst.Mounts, 1)
	materialized, ok := dst.Mounts[0].FileSource.Peek()
	require.True(t, ok)
	filePath, ok := materialized.File.Peek()
	require.True(t, ok)
	require.Equal(t, "/schema.json", filePath)
	require.Equal(t, 1, sourceMaterializer.calls)
}

func TestContainerMetaFileContentsForResultUsesAccessorPlan(t *testing.T) {
	t.Parallel()

	metaDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(metaDir, "stdout"), []byte("remote stdout"), 0o600))
	metaRef := &cacheVolumeTestImmutableRef{
		id:         "meta-ref",
		snapshotID: "meta-snapshot",
		mountDir:   metaDir,
	}
	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"meta-snapshot": metaRef,
		},
	}
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx := ContextWithQuery(context.Background(), query)

	container := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	container.MetaSnapshot.setMaterializer(&lazyAccessorTestMaterializer[bkcache.ImmutableRef, *Container]{
		value: metaRef,
		ok:    true,
	})
	self, err := dagql.NewObjectResultForCall(container, srv, cachemoneyRemotePlanTestCall("meta-file", (&Container{}).Type()))
	require.NoError(t, err)

	got, err := container.StdoutForResult(ctx, self)
	require.NoError(t, err)
	require.Equal(t, "remote stdout", got)
	require.Equal(t, []string{"meta-snapshot"}, manager.getBySnapshotIDCalls)
}

func TestMaterializeContainerStateFromParentUsesSlotAccessors(t *testing.T) {
	t.Parallel()

	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"fs-snapshot":    &cacheVolumeTestImmutableRef{id: "fs-ref", snapshotID: "fs-snapshot"},
			"meta-snapshot":  &cacheVolumeTestImmutableRef{id: "meta-ref", snapshotID: "meta-snapshot"},
			"mount-snapshot": &cacheVolumeTestImmutableRef{id: "mount-ref", snapshotID: "mount-snapshot"},
		},
	}
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx := ContextWithQuery(context.Background(), query)

	rootFS := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	rootFS.Dir.setValue("/")
	rootFS.Snapshot.setValue(manager.immutableBySnapshotID["fs-snapshot"])
	mountDir := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	mountDir.Dir.setValue("/src")
	mountDir.Snapshot.setValue(manager.immutableBySnapshotID["mount-snapshot"])

	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parent.FS.setMaterializer(&lazyAccessorTestMaterializer[*Directory, *Container]{
		value: rootFS,
		ok:    true,
	})
	parent.MetaSnapshot.setMaterializer(&lazyAccessorTestMaterializer[bkcache.ImmutableRef, *Container]{
		value: manager.immutableBySnapshotID["meta-snapshot"],
		ok:    true,
	})
	parent.Mounts = ContainerMounts{{
		Target:          "/mnt",
		DirectorySource: new(LazyAccessor[*Directory, *Container]),
	}}
	parent.Mounts[0].DirectorySource.setMaterializer(&lazyAccessorTestMaterializer[*Directory, *Container]{
		value: mountDir,
		ok:    true,
	})
	parentRes, err := dagql.NewObjectResultForCall(parent, srv, cachemoneyRemotePlanTestCall("parent", (&Container{}).Type()))
	require.NoError(t, err)

	dst := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	require.NoError(t, materializeContainerStateFromParent(ctx, dst, parentRes))

	clonedRoot, ok := dst.FS.Peek()
	require.True(t, ok)
	clonedRootSnapshot, ok := clonedRoot.Snapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "fs-snapshot", clonedRootSnapshot.SnapshotID())
	clonedMeta, ok := dst.MetaSnapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "meta-snapshot", clonedMeta.SnapshotID())
	require.Len(t, dst.Mounts, 1)
	clonedMount, ok := dst.Mounts[0].DirectorySource.Peek()
	require.True(t, ok)
	clonedMountSnapshot, ok := clonedMount.Snapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "mount-snapshot", clonedMountSnapshot.SnapshotID())
	require.ElementsMatch(t, []string{"fs-snapshot", "meta-snapshot", "mount-snapshot"}, manager.getBySnapshotIDCalls)
}

func TestMaterializeContainerStateFromParentEvaluatesUnplannedPendingRootFS(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"fs-snapshot": &cacheVolumeTestImmutableRef{id: "fs-ref", snapshotID: "fs-snapshot"},
		},
	}
	cache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "cache.db"), manager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, cache.Close(context.Background()))
	})
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx = ContextWithQuery(dagql.ContextWithCache(ctx, cache), query)

	rootFS := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	rootFS.Dir.setValue("/")
	rootFS.Snapshot.setValue(manager.immutableBySnapshotID["fs-snapshot"])

	lazy := &containerSetRootFSTestLazy{
		LazyState: NewLazyState(),
		rootFS:    rootFS,
	}
	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parent.Lazy = lazy
	call := cachemoneyRemotePlanTestCall("pending-parent", (&Container{}).Type())
	anyParent, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(parent, srv, call)
	})
	require.NoError(t, err)
	parentRes := anyParent.(dagql.ObjectResult[*Container])

	dst := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	require.NoError(t, materializeContainerStateFromParent(ctx, dst, parentRes))
	require.Equal(t, 1, lazy.calls)

	clonedRoot, ok := dst.FS.Peek()
	require.True(t, ok)
	clonedRootSnapshot, ok := clonedRoot.Snapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "fs-snapshot", clonedRootSnapshot.SnapshotID())
	require.Equal(t, []string{"fs-snapshot"}, manager.getBySnapshotIDCalls)
}

func TestMaterializeContainerStateFromParentEvaluatesUnplannedPendingMountSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"fs-snapshot":    &cacheVolumeTestImmutableRef{id: "fs-ref", snapshotID: "fs-snapshot"},
			"mount-snapshot": &cacheVolumeTestImmutableRef{id: "mount-ref", snapshotID: "mount-snapshot"},
		},
	}
	cache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "cache.db"), manager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, cache.Close(context.Background()))
	})
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx = ContextWithQuery(dagql.ContextWithCache(ctx, cache), query)

	rootFS := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	rootFS.Dir.setValue("/")
	rootFS.Snapshot.setValue(manager.immutableBySnapshotID["fs-snapshot"])
	mountDir := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	mountDir.Dir.setValue("/")
	mountDir.Snapshot.setValue(manager.immutableBySnapshotID["mount-snapshot"])

	lazy := &containerSetMountSourceTestLazy{
		LazyState: NewLazyState(),
		mountDir:  mountDir,
	}
	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parent.FS.setValue(rootFS)
	parent.Mounts = ContainerMounts{{
		Target:          "/mnt",
		DirectorySource: new(LazyAccessor[*Directory, *Container]),
	}}
	parent.Lazy = lazy
	call := cachemoneyRemotePlanTestCall("pending-mount-parent", (&Container{}).Type())
	anyParent, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(parent, srv, call)
	})
	require.NoError(t, err)
	parentRes := anyParent.(dagql.ObjectResult[*Container])

	dst := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	require.NoError(t, materializeContainerStateFromParent(ctx, dst, parentRes))
	require.Equal(t, 1, lazy.calls)

	require.Len(t, dst.Mounts, 1)
	clonedMount, ok := dst.Mounts[0].DirectorySource.Peek()
	require.True(t, ok)
	clonedMountSnapshot, ok := clonedMount.Snapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "mount-snapshot", clonedMountSnapshot.SnapshotID())
	require.ElementsMatch(t, []string{"fs-snapshot", "mount-snapshot"}, manager.getBySnapshotIDCalls)
}

func TestMaterializeContainerStateFromParentEvaluatesUnplannedPendingFileMountSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"fs-snapshot":   &cacheVolumeTestImmutableRef{id: "fs-ref", snapshotID: "fs-snapshot"},
			"file-snapshot": &cacheVolumeTestImmutableRef{id: "file-ref", snapshotID: "file-snapshot"},
		},
	}
	cache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "cache.db"), manager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, cache.Close(context.Background()))
	})
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx = ContextWithQuery(dagql.ContextWithCache(ctx, cache), query)

	rootFS := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	rootFS.Dir.setValue("/")
	rootFS.Snapshot.setValue(manager.immutableBySnapshotID["fs-snapshot"])
	mountFile := &File{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		File:     new(LazyAccessor[string, *File]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File]),
	}
	mountFile.File.setValue("/schema.json")
	mountFile.Snapshot.setValue(manager.immutableBySnapshotID["file-snapshot"])

	lazy := &containerSetFileMountSourceTestLazy{
		LazyState: NewLazyState(),
		mountFile: mountFile,
	}
	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parent.FS.setValue(rootFS)
	parent.Mounts = ContainerMounts{{
		Target:     "/schema.json",
		FileSource: new(LazyAccessor[*File, *Container]),
	}}
	parent.Lazy = lazy
	call := cachemoneyRemotePlanTestCall("pending-file-mount-parent", (&Container{}).Type())
	anyParent, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(parent, srv, call)
	})
	require.NoError(t, err)
	parentRes := anyParent.(dagql.ObjectResult[*Container])

	dst := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	require.NoError(t, materializeContainerStateFromParent(ctx, dst, parentRes))
	require.Equal(t, 1, lazy.calls)

	require.Len(t, dst.Mounts, 1)
	clonedMount, ok := dst.Mounts[0].FileSource.Peek()
	require.True(t, ok)
	clonedMountSnapshot, ok := clonedMount.Snapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "file-snapshot", clonedMountSnapshot.SnapshotID())
	require.ElementsMatch(t, []string{"fs-snapshot", "file-snapshot"}, manager.getBySnapshotIDCalls)
}

func TestContainerRootFSLazyUsesAccessorPlan(t *testing.T) {
	t.Parallel()

	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"fs-snapshot": &cacheVolumeTestImmutableRef{id: "fs-ref", snapshotID: "fs-snapshot"},
		},
	}
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx := ContextWithQuery(context.Background(), query)

	rootFS := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	rootFS.Dir.setValue("/")
	rootFS.Snapshot.setValue(manager.immutableBySnapshotID["fs-snapshot"])

	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parent.FS.setMaterializer(&lazyAccessorTestMaterializer[*Directory, *Container]{
		value: rootFS,
		ok:    true,
	})
	parentRes, err := dagql.NewObjectResultForCall(parent, srv, cachemoneyRemotePlanTestCall("parent-rootfs", (&Container{}).Type()))
	require.NoError(t, err)

	dir := &Directory{
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	lazy := &ContainerRootFSLazy{
		LazyState: NewLazyState(),
		Parent:    parentRes,
	}
	require.NoError(t, lazy.Evaluate(ctx, dir))

	snapshot, ok := dir.Snapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "fs-snapshot", snapshot.SnapshotID())
	require.Equal(t, []string{"fs-snapshot"}, manager.getBySnapshotIDCalls)
}

func TestContainerRootFSLazyEvaluatesUnplannedPendingRootFS(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"fs-snapshot": &cacheVolumeTestImmutableRef{id: "fs-ref", snapshotID: "fs-snapshot"},
		},
	}
	cache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "cache.db"), manager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, cache.Close(context.Background()))
	})
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx = ContextWithQuery(dagql.ContextWithCache(ctx, cache), query)

	rootFS := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	rootFS.Dir.setValue("/")
	rootFS.Snapshot.setValue(manager.immutableBySnapshotID["fs-snapshot"])

	lazy := &containerSetRootFSTestLazy{
		LazyState: NewLazyState(),
		rootFS:    rootFS,
	}
	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parent.Lazy = lazy
	call := cachemoneyRemotePlanTestCall("pending-rootfs-parent", (&Container{}).Type())
	anyParent, err := cache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(parent, srv, call)
	})
	require.NoError(t, err)
	parentRes := anyParent.(dagql.ObjectResult[*Container])

	dir := &Directory{
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	rootLazy := &ContainerRootFSLazy{
		LazyState: NewLazyState(),
		Parent:    parentRes,
	}
	require.NoError(t, rootLazy.Evaluate(ctx, dir))
	require.Equal(t, 1, lazy.calls)

	snapshot, ok := dir.Snapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "fs-snapshot", snapshot.SnapshotID())
	require.Equal(t, []string{"fs-snapshot"}, manager.getBySnapshotIDCalls)
}

func TestContainerDirectoryLazyUsesMountedSourceAccessorPlan(t *testing.T) {
	t.Parallel()

	mountRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(mountRoot, "subdir"), 0o700))
	mountRef := &cacheVolumeTestImmutableRef{
		id:         "mount-ref",
		snapshotID: "mount-snapshot",
		mountDir:   mountRoot,
	}
	manager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"mount-snapshot": mountRef,
		},
	}
	srv, query := cachemoneyRemotePlanTestServer(t, manager)
	ctx := ContextWithQuery(context.Background(), query)

	sourceDir := &Directory{
		Platform: Platform{OS: "linux", Architecture: "amd64"},
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	sourceDir.Dir.setValue("/")
	sourceDir.Snapshot.setValue(mountRef)
	parent := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	parent.Mounts = ContainerMounts{{
		Target:          "/mnt",
		DirectorySource: new(LazyAccessor[*Directory, *Container]),
	}}
	parent.Mounts[0].DirectorySource.setMaterializer(&lazyAccessorTestMaterializer[*Directory, *Container]{
		value: sourceDir,
		ok:    true,
	})
	parentRes, err := dagql.NewObjectResultForCall(parent, srv, cachemoneyRemotePlanTestCall("parent-mount", (&Container{}).Type()))
	require.NoError(t, err)

	dir := &Directory{
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	lazy := &ContainerDirectoryLazy{
		LazyState: NewLazyState(),
		Parent:    parentRes,
		Path:      "/mnt/subdir",
	}
	require.NoError(t, lazy.Evaluate(ctx, dir))

	dirPath, ok := dir.Dir.Peek()
	require.True(t, ok)
	require.Equal(t, "/subdir", dirPath)
	snapshot, ok := dir.Snapshot.Peek()
	require.True(t, ok)
	require.Equal(t, "mount-snapshot", snapshot.SnapshotID())
	require.Equal(t, []string{"mount-snapshot", "mount-snapshot"}, manager.getBySnapshotIDCalls)
}

type containerSetRootFSTestLazy struct {
	LazyState
	rootFS *Directory
	calls  int
}

func (lazy *containerSetRootFSTestLazy) Evaluate(ctx context.Context, container *Container) error {
	return lazy.LazyState.Evaluate(ctx, "Container.testSetRootFS", func(context.Context) error {
		lazy.calls++
		container.FS = new(LazyAccessor[*Directory, *Container])
		container.FS.setValue(lazy.rootFS)
		return nil
	})
}

func (lazy *containerSetRootFSTestLazy) AttachDependencies(context.Context, func(dagql.AnyResult) (dagql.AnyResult, error)) ([]dagql.AnyResult, error) {
	return nil, nil
}

func (lazy *containerSetRootFSTestLazy) EncodePersisted(context.Context, dagql.PersistedObjectCache) (json.RawMessage, error) {
	return nil, nil
}

type containerSetMountSourceTestLazy struct {
	LazyState
	mountDir *Directory
	calls    int
}

func (lazy *containerSetMountSourceTestLazy) Evaluate(ctx context.Context, container *Container) error {
	return lazy.LazyState.Evaluate(ctx, "Container.testSetMountSource", func(context.Context) error {
		lazy.calls++
		container.Mounts = ContainerMounts{{
			Target:          "/mnt",
			DirectorySource: new(LazyAccessor[*Directory, *Container]),
		}}
		container.Mounts[0].DirectorySource.setValue(lazy.mountDir)
		return nil
	})
}

func (lazy *containerSetMountSourceTestLazy) AttachDependencies(context.Context, func(dagql.AnyResult) (dagql.AnyResult, error)) ([]dagql.AnyResult, error) {
	return nil, nil
}

func (lazy *containerSetMountSourceTestLazy) EncodePersisted(context.Context, dagql.PersistedObjectCache) (json.RawMessage, error) {
	return nil, nil
}

type containerSetFileMountSourceTestLazy struct {
	LazyState
	mountFile *File
	calls     int
}

func (lazy *containerSetFileMountSourceTestLazy) Evaluate(ctx context.Context, container *Container) error {
	return lazy.LazyState.Evaluate(ctx, "Container.testSetFileMountSource", func(context.Context) error {
		lazy.calls++
		container.Mounts = ContainerMounts{{
			Target:     "/schema.json",
			FileSource: new(LazyAccessor[*File, *Container]),
		}}
		container.Mounts[0].FileSource.setValue(lazy.mountFile)
		return nil
	})
}

func (lazy *containerSetFileMountSourceTestLazy) AttachDependencies(context.Context, func(dagql.AnyResult) (dagql.AnyResult, error)) ([]dagql.AnyResult, error) {
	return nil, nil
}

func (lazy *containerSetFileMountSourceTestLazy) EncodePersisted(context.Context, dagql.PersistedObjectCache) (json.RawMessage, error) {
	return nil, nil
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
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*HTTPState]{}))
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

	return cachemoneyRemotePlanTestImportWithManager(t, ctx, sourceCache, &cacheVolumeTestSnapshotManager{})
}

func cachemoneyRemotePlanTestImportWithManager(t *testing.T, ctx context.Context, sourceCache *dagql.Cache, manager bkcache.SnapshotManager) (*dagql.Cache, *dagql.Server, context.Context) {
	t.Helper()

	exportPath := filepath.Join(t.TempDir(), "metadata.db")
	prepared, err := sourceCache.PrepareCachemoneyExport(ctx, exportPath)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, prepared.Release(context.Background()))
	})

	destCache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "dest.db"), manager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, destCache.Close(context.Background()))
	})
	require.NoError(t, destCache.ImportCachemoneyMetadata(ctx, dagql.CachemoneyImportSource{
		ID:             "remote-source",
		MetadataDBPath: exportPath,
	}))
	destSrv, destQuery := cachemoneyRemotePlanTestServer(t, manager)
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
