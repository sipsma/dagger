package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
)

// The mutable-owner types cross engine boundaries identity-only: their
// snapshot content never travels, so an imported (or content-stripped) row
// must decode to a contentless identity object that re-acquires lazily on
// first use. These tests persist exactly that row shape — a mirror that
// never acquired its snapshot — and prove decode + first-use re-acquisition
// on a fresh store.

func contentlessTestServer(t *testing.T, manager bkcache.SnapshotManager) (*dagql.Server, *Query) {
	t.Helper()
	query := &Query{
		Server: &cacheVolumeTestQueryServer{
			mockServer:   &mockServer{},
			cacheManager: manager,
		},
	}
	srv := newCoreDagqlServerForTest(t, query)
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*RemoteGitMirror]{}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*ClientFilesyncMirror]{}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*CacheVolume]{}))
	return srv, query
}

// seedContentlessRow persists one identity-only row for the given value and
// returns after closing the store.
func seedContentlessRow[T dagql.Typed](t *testing.T, ctx context.Context, dbPath string, call *dagql.ResultCall, value T) {
	t.Helper()
	dagCache, err := dagql.NewCache(ctx, dbPath, nil, nil)
	require.NoError(t, err)
	srv, _ := contentlessTestServer(t, nil)
	seedCtx := dagql.ContextWithCache(ctx, dagCache)

	res, err := dagql.NewObjectResultForCall(value, srv, call)
	require.NoError(t, err)
	_, err = dagCache.GetOrInitCall(seedCtx, "seed-session", srv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, dagql.ValueFunc(res))
	require.NoError(t, err)

	require.NoError(t, dagCache.ReleaseSession(seedCtx, "seed-session"))
	require.NoError(t, dagCache.Close(context.Background()))
}

// loadContentlessRow reopens the store and loads the persisted row through
// the decode path (the initializer must never run: the row is a hit).
func loadContentlessRow(t *testing.T, ctx context.Context, dbPath string, manager bkcache.SnapshotManager, call *dagql.ResultCall) (dagql.AnyResult, *Query, context.Context, func()) {
	t.Helper()
	dagCache, err := dagql.NewCache(ctx, dbPath, manager, nil)
	require.NoError(t, err)
	srv, query := contentlessTestServer(t, manager)
	loadCtx := ContextWithQuery(dagql.ContextWithCache(ctx, dagCache), query)

	loaded, err := dagCache.GetOrInitCall(loadCtx, "load-session", srv, &dagql.CallRequest{
		ResultCall:    call,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return nil, errors.New("identity-only row must decode as a hit, not re-execute")
	})
	require.NoError(t, err)
	return loaded, query, loadCtx, func() {
		require.NoError(t, dagCache.Close(context.Background()))
	}
}

func TestRemoteGitMirrorContentlessDecode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	call := &dagql.ResultCall{
		Kind:  dagql.ResultCallKindField,
		Type:  dagql.NewResultCallType((&RemoteGitMirror{}).Type()),
		Field: "gitMirrorContentless",
	}
	// The mirror never acquired its snapshot: its persisted form is
	// identity only, the same bytes an engine-boundary import carries.
	seedContentlessRow(t, ctx, dbPath, call, NewRemoteGitMirror("https://example.com/repo.git"))

	manager := &cacheVolumeTestSnapshotManager{
		newResult: &cacheVolumeTestMutableRef{
			cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
				id:         "git-mirror-fresh",
				snapshotID: "git-mirror-fresh-snap",
			},
		},
	}
	loaded, query, loadCtx, done := loadContentlessRow(t, ctx, dbPath, manager, call)
	defer done()

	mirror, ok := dagql.UnwrapAs[*RemoteGitMirror](loaded.Unwrap())
	require.True(t, ok)
	require.Equal(t, "https://example.com/repo.git", mirror.RemoteURL)
	require.Nil(t, mirror.snapshot, "an identity-only mirror decodes contentless")

	// First use re-acquires: a fresh bare repo to fetch into.
	require.NoError(t, mirror.EnsureCreated(loadCtx, query))
	require.NotNil(t, mirror.snapshot)
	require.Len(t, manager.newCalls, 1)
}

func TestClientFilesyncMirrorContentlessDecode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	call := &dagql.ResultCall{
		Kind:  dagql.ResultCallKindField,
		Type:  dagql.NewResultCallType((&ClientFilesyncMirror{}).Type()),
		Field: "filesyncMirrorContentless",
	}
	seedContentlessRow(t, ctx, dbPath, call, &ClientFilesyncMirror{
		StableClientID: "stable-client-1",
		Drive:          "",
	})

	manager := &cacheVolumeTestSnapshotManager{
		newResult: &cacheVolumeTestMutableRef{
			cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
				id:         "filesync-mirror-fresh",
				snapshotID: "filesync-mirror-fresh-snap",
			},
		},
	}
	loaded, query, loadCtx, done := loadContentlessRow(t, ctx, dbPath, manager, call)
	defer done()

	mirror, ok := dagql.UnwrapAs[*ClientFilesyncMirror](loaded.Unwrap())
	require.True(t, ok)
	require.Equal(t, "stable-client-1", mirror.StableClientID)
	require.Nil(t, mirror.snapshot, "an identity-only mirror decodes contentless")

	// First use re-acquires: a fresh snapshot for the client to sync into.
	require.NoError(t, mirror.EnsureCreated(loadCtx, query))
	require.NotNil(t, mirror.snapshot)
	require.Len(t, manager.newCalls, 1)
}

func TestCacheVolumeContentlessDecode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	call := &dagql.ResultCall{
		Kind:  dagql.ResultCallKindField,
		Type:  dagql.NewResultCallType((&CacheVolume{}).Type()),
		Field: "cacheVolumeContentless",
	}
	seedContentlessRow(t, ctx, dbPath, call,
		NewCache("contentless-key", "ns", dagql.Null[dagql.ObjectResult[*Directory]](), CacheSharingModeShared, ""))

	manager := &cacheVolumeTestSnapshotManager{
		newResult: &cacheVolumeTestMutableRef{
			cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
				id:         "cache-volume-fresh",
				snapshotID: "cache-volume-fresh-snap",
			},
		},
	}
	loaded, _, loadCtx, done := loadContentlessRow(t, ctx, dbPath, manager, call)
	defer done()

	volume, ok := dagql.UnwrapAs[*CacheVolume](loaded.Unwrap())
	require.True(t, ok)
	require.Equal(t, "contentless-key", volume.Key)
	require.Nil(t, volume.getSnapshot(), "an identity-only volume decodes contentless")

	// First use initializes fresh.
	require.NoError(t, volume.InitializeSnapshot(loadCtx))
	require.NotNil(t, volume.getSnapshot())
	require.Len(t, manager.newCalls, 1)
}
