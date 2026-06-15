package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/dagql/cachemoneyproto"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/opencontainers/go-digest"
	ociidentity "github.com/opencontainers/image-spec/identity"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type mutableSourceDependentSnapshot struct {
	Name       string
	Source     dagql.ObjectResult[*RemoteGitMirror]
	SnapshotID string
}

func (*mutableSourceDependentSnapshot) Type() *ast.Type {
	return &ast.Type{
		NamedType: "MutableSourceDependentSnapshot",
		NonNull:   true,
	}
}

type persistedMutableSourceDependentSnapshot struct {
	Name           string `json:"name"`
	SourceResultID uint64 `json:"sourceResultID"`
}

func (v *mutableSourceDependentSnapshot) EncodePersistedObject(ctx context.Context, cache dagql.PersistedObjectCache) (dagql.PersistedObjectEncoding, error) {
	_ = ctx
	if v == nil {
		return dagql.PersistedObjectEncoding{}, fmt.Errorf("encode mutable source dependent snapshot: nil value")
	}
	sourceResultID, err := encodePersistedObjectRef(cache, v.Source, "mutable source dependent source")
	if err != nil {
		return dagql.PersistedObjectEncoding{}, err
	}
	payload, err := json.Marshal(persistedMutableSourceDependentSnapshot{
		Name:           v.Name,
		SourceResultID: sourceResultID,
	})
	if err != nil {
		return dagql.PersistedObjectEncoding{}, err
	}
	encoding := dagql.PersistedObjectEncoding{JSON: payload}
	if v.SnapshotID != "" {
		encoding.SnapshotLinks = []dagql.PersistedSnapshotRefLink{{
			RefKey: v.SnapshotID,
			Role:   "snapshot",
		}}
	}
	return encoding, nil
}

func (v *mutableSourceDependentSnapshot) AttachDependencyResults(
	ctx context.Context,
	self dagql.AnyResult,
	attach func(dagql.AnyResult) (dagql.AnyResult, error),
) ([]dagql.AnyResult, error) {
	_ = ctx
	_ = self
	if v == nil || v.Source.Self() == nil {
		return nil, nil
	}
	attached, err := attach(v.Source)
	if err != nil {
		return nil, err
	}
	source, ok := attached.(dagql.ObjectResult[*RemoteGitMirror])
	if !ok {
		return nil, fmt.Errorf("mutable source dependent source resolved to %T", attached)
	}
	v.Source = source
	return []dagql.AnyResult{attached}, nil
}

func TestCachemoneyExportKeepsMutableSourceIdentityAndDependentSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	diffID := digest.FromString("diff-dependent")
	blobDigest := digest.FromString("blob-dependent")
	chainID := ociidentity.ChainID([]digest.Digest{diffID}).String()
	dependentRef := &cacheVolumeTestImmutableRef{
		id:         "dependent-ref",
		snapshotID: "dependent-snapshot",
		exportChain: &bkcache.ExportChain{
			Layers: []bkcache.ExportLayer{{
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobDigest,
					Size:      12,
					Annotations: map[string]string{
						labels.LabelUncompressed: diffID.String(),
					},
				},
			}},
		},
	}
	snapshotManager := &cacheVolumeTestSnapshotManager{
		immutableBySnapshotID: map[string]bkcache.ImmutableRef{
			"dependent-snapshot": dependentRef,
		},
	}

	dagCache, err := dagql.NewCache(ctx, filepath.Join(t.TempDir(), "cache.db"), snapshotManager, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, dagCache.Close(context.Background()))
	})
	ctx = dagql.ContextWithCache(ctx, dagCache)

	query := &Query{Server: &cacheVolumeTestQueryServer{mockServer: &mockServer{}, cacheManager: snapshotManager}}
	srv := newCoreDagqlServerForTest(t, query)
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*RemoteGitMirror]{}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*mutableSourceDependentSnapshot]{}))

	sourceFrame := &dagql.ResultCall{
		Kind:  dagql.ResultCallKindField,
		Type:  dagql.NewResultCallType((&RemoteGitMirror{}).Type()),
		Field: "remote-git-mirror-source",
	}
	source, err := dagCache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    sourceFrame,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		mirror := NewRemoteGitMirror("https://example.com/repo.git")
		mirror.snapshot = &cacheVolumeTestMutableRef{
			cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
				id:         "git-mutable",
				snapshotID: "git-mutable-snapshot",
			},
		}
		return dagql.NewObjectResultForCall(mirror, srv, sourceFrame)
	})
	require.NoError(t, err)
	sourceID, err := dagCache.PersistedResultID(source)
	require.NoError(t, err)
	sourceObj, ok := source.(dagql.ObjectResult[*RemoteGitMirror])
	require.True(t, ok)

	dependentFrame := &dagql.ResultCall{
		Kind:     dagql.ResultCallKindField,
		Type:     dagql.NewResultCallType((&mutableSourceDependentSnapshot{}).Type()),
		Field:    "dependent-snapshot",
		Receiver: &dagql.ResultCallRef{ResultID: sourceID},
	}
	dependent, err := dagCache.GetOrInitCall(ctx, "session", srv, &dagql.CallRequest{
		ResultCall:    dependentFrame,
		IsPersistable: true,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(&mutableSourceDependentSnapshot{
			Name:       "dependent",
			Source:     sourceObj,
			SnapshotID: "dependent-snapshot",
		}, srv, dependentFrame)
	})
	require.NoError(t, err)
	dependentID, err := dagCache.PersistedResultID(dependent)
	require.NoError(t, err)

	metadataDBPath := filepath.Join(t.TempDir(), cachemoneyproto.MetadataDBName)
	export, err := dagCache.PrepareCachemoneyExport(ctx, metadataDBPath)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, export.Release(context.Background()))
	})
	require.Equal(t, []cachemoneyproto.SnapshotOffer{{
		ResultID: dependentID,
		Role:     "snapshot",
		ChainID:  chainID,
	}}, export.Manifest.Snapshots)

	db, q := openMutableSourcePersistenceDB(t, ctx, metadataDBPath)
	defer func() {
		require.NoError(t, q.Close())
		require.NoError(t, db.Close())
	}()

	results, err := q.ListMirrorResults(ctx)
	require.NoError(t, err)
	require.Len(t, results, 2)
	resultRows := map[int64]persistdb.MirrorResult{}
	for _, row := range results {
		resultRows[row.ID] = row
	}
	sourceRow, ok := resultRows[int64(sourceID)]
	require.True(t, ok)
	require.Contains(t, sourceRow.CallFrameJSON, "remote-git-mirror-source")
	dependentRow, ok := resultRows[int64(dependentID)]
	require.True(t, ok)
	require.Contains(t, dependentRow.CallFrameJSON, `"resultID":`+fmt.Sprint(sourceID))
	require.NotContains(t, dependentRow.CallFrameJSON, "cachemoney.snapshot")

	var depPayload dagql.PersistedResultEnvelope
	require.NoError(t, json.Unmarshal(dependentRow.SelfPayload, &depPayload))
	require.Equal(t, "MutableSourceDependentSnapshot", depPayload.TypeName)
	require.Contains(t, string(depPayload.ObjectJSON), fmt.Sprintf(`"sourceResultID":%d`, sourceID))

	deps, err := q.ListMirrorResultDeps(ctx)
	require.NoError(t, err)
	require.Equal(t, []persistdb.MirrorResultDep{{
		ParentResultID: int64(dependentID),
		DepResultID:    int64(sourceID),
	}}, deps)

	links, err := q.ListMirrorResultSnapshotLinks(ctx)
	require.NoError(t, err)
	require.Equal(t, []persistdb.MirrorResultSnapshotLink{{
		ResultID: int64(dependentID),
		RefKey:   "dependent-snapshot",
		Role:     "snapshot",
	}}, links)

	chains, err := q.ListMirrorResultSnapshotChains(ctx)
	require.NoError(t, err)
	require.Equal(t, []persistdb.MirrorResultSnapshotChain{{
		ResultID: int64(dependentID),
		Role:     "snapshot",
		ChainID:  chainID,
	}}, chains)
}

func openMutableSourcePersistenceDB(t *testing.T, ctx context.Context, dbPath string) (*sql.DB, *persistdb.Queries) {
	t.Helper()

	connURL := &url.URL{
		Scheme: "file",
		Path:   dbPath,
		RawQuery: url.Values{
			"mode":    []string{"ro"},
			"_pragma": []string{"busy_timeout=10000"},
			"_txlock": []string{"deferred"},
		}.Encode(),
	}
	db, err := sql.Open("sqlite", connURL.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			_ = db.Close()
		}
	})
	require.NoError(t, db.PingContext(ctx))
	q, err := persistdb.Prepare(ctx, db)
	require.NoError(t, err)
	t.Cleanup(func() {
		if t.Failed() {
			_ = q.Close()
		}
	})
	return db, q
}

func TestRemoteGitMirrorEncodeKeepsSnapshotLinkForLocalPersistence(t *testing.T) {
	t.Parallel()

	mirror := NewRemoteGitMirror("https://example.com/repo.git")
	mirror.snapshot = &cacheVolumeTestMutableRef{
		cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
			id:         "git-mutable",
			snapshotID: "git-snapshot",
		},
	}

	encoding, err := mirror.EncodePersistedObject(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, []dagql.PersistedSnapshotRefLink{{
		RefKey: "git-snapshot",
		Role:   "bare_repo",
	}}, encoding.SnapshotLinks)
}

func TestRemoteGitMirrorEncodeOmitsSnapshotLinkForCachemoneyExport(t *testing.T) {
	t.Parallel()

	mirror := NewRemoteGitMirror("https://example.com/repo.git")
	mirror.snapshot = &cacheVolumeTestMutableRef{
		cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
			id:         "git-mutable",
			snapshotID: "git-snapshot",
		},
	}

	encoding, err := mirror.EncodePersistedObject(dagql.ContextWithCachemoneyExport(context.Background()), nil)
	require.NoError(t, err)
	require.Empty(t, encoding.SnapshotLinks)

	decoded, err := new(RemoteGitMirror).DecodePersistedObject(context.Background(), nil, 0, nil, encoding.JSON)
	require.NoError(t, err)
	decodedMirror := decoded.(*RemoteGitMirror)
	require.Equal(t, "https://example.com/repo.git", decodedMirror.RemoteURL)
	require.Nil(t, decodedMirror.snapshot)

	ref := &cacheVolumeTestMutableRef{
		cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
			id:         "new-git-mutable",
			snapshotID: "new-git-snapshot",
		},
	}
	manager := &cacheVolumeTestSnapshotManager{newResult: ref}
	query := &Query{Server: &cacheVolumeTestQueryServer{mockServer: &mockServer{}, cacheManager: manager}}
	require.NoError(t, decodedMirror.EnsureCreated(context.Background(), query))
	require.Same(t, ref, decodedMirror.snapshot)
	require.Len(t, manager.newCalls, 1)
	require.Nil(t, manager.newCalls[0])
}

func TestClientFilesyncMirrorEncodeKeepsSnapshotLinkForLocalPersistence(t *testing.T) {
	t.Parallel()

	mirror := &ClientFilesyncMirror{
		StableClientID: "client-123",
		Drive:          "/work",
		snapshot: &cacheVolumeTestMutableRef{
			cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
				id:         "filesync-mutable",
				snapshotID: "filesync-snapshot",
			},
		},
	}

	encoding, err := mirror.EncodePersistedObject(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, []dagql.PersistedSnapshotRefLink{{
		RefKey: "filesync-snapshot",
		Role:   "snapshot",
	}}, encoding.SnapshotLinks)
}

func TestClientFilesyncMirrorEncodeOmitsSnapshotLinkForCachemoneyExport(t *testing.T) {
	t.Parallel()

	mirror := &ClientFilesyncMirror{
		StableClientID: "client-123",
		Drive:          "/work",
		snapshot: &cacheVolumeTestMutableRef{
			cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
				id:         "filesync-mutable",
				snapshotID: "filesync-snapshot",
			},
		},
	}

	encoding, err := mirror.EncodePersistedObject(dagql.ContextWithCachemoneyExport(context.Background()), nil)
	require.NoError(t, err)
	require.Empty(t, encoding.SnapshotLinks)

	decoded, err := new(ClientFilesyncMirror).DecodePersistedObject(context.Background(), nil, 0, nil, encoding.JSON)
	require.NoError(t, err)
	decodedMirror := decoded.(*ClientFilesyncMirror)
	require.Equal(t, "client-123", decodedMirror.StableClientID)
	require.Equal(t, "/work", decodedMirror.Drive)
	require.Nil(t, decodedMirror.snapshot)

	ref := &cacheVolumeTestMutableRef{
		cacheVolumeTestImmutableRef: cacheVolumeTestImmutableRef{
			id:         "new-filesync-mutable",
			snapshotID: "new-filesync-snapshot",
		},
	}
	manager := &cacheVolumeTestSnapshotManager{newResult: ref}
	query := &Query{Server: &cacheVolumeTestQueryServer{mockServer: &mockServer{}, cacheManager: manager}}
	require.NoError(t, decodedMirror.EnsureCreated(context.Background(), query))
	require.Same(t, ref, decodedMirror.snapshot)
	require.Len(t, manager.newCalls, 1)
	require.Nil(t, manager.newCalls[0])
}
