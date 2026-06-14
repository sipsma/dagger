package dagql

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/dagger/dagger/dagql/cachemoneyproto"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	bkconfig "github.com/dagger/dagger/engine/snapshots/config"
	"github.com/opencontainers/go-digest"
	ociidentity "github.com/opencontainers/image-spec/identity"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"
)

type fakeCachemoneyExportRef struct {
	snapshotID   string
	chain        *bkcache.ExportChain
	exportConfig bkconfig.RefConfig
	releaseCalls int
}

func (r *fakeCachemoneyExportRef) ID() string {
	return r.snapshotID
}

func (r *fakeCachemoneyExportRef) SnapshotID() string {
	return r.snapshotID
}

func (r *fakeCachemoneyExportRef) Release(ctx context.Context) error {
	_ = ctx
	r.releaseCalls++
	return nil
}

func (*fakeCachemoneyExportRef) Size(context.Context) (int64, error) {
	panic("unexpected Size call")
}

func (*fakeCachemoneyExportRef) Mount(context.Context, bool) (bkcache.MountableRef, error) {
	panic("unexpected Mount call")
}

func (r *fakeCachemoneyExportRef) ExportChain(ctx context.Context, cfg bkconfig.RefConfig) (*bkcache.ExportChain, error) {
	_ = ctx
	r.exportConfig = cfg
	return r.chain, nil
}

type cachemoneyDiagUnpersistableA struct{}

func (*cachemoneyDiagUnpersistableA) Type() *ast.Type {
	return &ast.Type{
		NamedType: "CachemoneyDiagUnpersistableA",
		NonNull:   true,
	}
}

type cachemoneyDiagUnpersistableB struct{}

func (*cachemoneyDiagUnpersistableB) Type() *ast.Type {
	return &ast.Type{
		NamedType: "CachemoneyDiagUnpersistableB",
		NonNull:   true,
	}
}

func TestPrepareCachemoneyExportAggregatesDiagnostics(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	snapshotManager := &fakeSnapshotManager{
		snapshotMetadata: map[string]bkcache.SnapshotRecordMetadata{
			"mutable-snapshot": {Mutable: true},
		},
	}

	dbPath := filepath.Join(t.TempDir(), "cache.db")
	c, err := NewCache(ctx, dbPath, snapshotManager, nil)
	assert.NilError(t, err)
	defer func() {
		// This test intentionally leaves unpersistable objects in cache so the
		// export path can report every class in one diagnostic.
		_ = c.Close(context.Background())
	}()

	srv := newDagqlServerForTest(t, cacheTestQuery{})
	srv.InstallObject(NewClass(srv, ClassOpts[*cachemoneyDiagUnpersistableA]{}))
	srv.InstallObject(NewClass(srv, ClassOpts[*cachemoneyDiagUnpersistableB]{}))
	srv.InstallObject(NewClass(srv, ClassOpts[*persistSnapshotValue]{}))

	frameA := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&cachemoneyDiagUnpersistableA{}).Type()),
		Field: "diag-a",
	}
	_, err = c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    frameA,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestDetachedObjectResult(frameA, srv, &cachemoneyDiagUnpersistableA{}), nil
	})
	assert.NilError(t, err)

	frameB := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&cachemoneyDiagUnpersistableB{}).Type()),
		Field: "diag-b",
	}
	_, err = c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    frameB,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestDetachedObjectResult(frameB, srv, &cachemoneyDiagUnpersistableB{}), nil
	})
	assert.NilError(t, err)

	frameMutableLink := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistSnapshotValue{}).Type()),
		Field: "diag-mutable-link",
	}
	_, err = c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    frameMutableLink,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestDetachedObjectResult(frameMutableLink, srv, &persistSnapshotValue{
			Name:       "mutable",
			SnapshotID: "mutable-snapshot",
		}), nil
	})
	assert.NilError(t, err)

	_, err = c.PrepareCachemoneyExport(ctx, filepath.Join(t.TempDir(), cachemoneyproto.MetadataDBName))
	assert.Assert(t, err != nil)
	msg := err.Error()
	for _, want := range []string{
		"cachemoney export diagnostics",
		"unpersistable object payloads (2)",
		`type="CachemoneyDiagUnpersistableA"`,
		`type="CachemoneyDiagUnpersistableB"`,
		"mutable snapshot links (1)",
		`role="snapshot"`,
		`ref="mutable-snapshot"`,
	} {
		assert.Assert(t, strings.Contains(msg, want), "expected %q in %s", want, msg)
	}
}

func TestPrepareCachemoneyExportWritesContentAddressedManifestAndMetadata(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	diffA := digest.FromString("diff-a")
	diffB := digest.FromString("diff-b")
	blobA := digest.FromString("blob-a")
	blobB := digest.FromString("blob-b")
	chainID := ociidentity.ChainID([]digest.Digest{diffA, diffB}).String()

	exportRef := &fakeCachemoneyExportRef{
		snapshotID: "snapshot-a",
		chain: &bkcache.ExportChain{
			Layers: []bkcache.ExportLayer{{
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobA,
					Size:      10,
					Annotations: map[string]string{
						labels.LabelUncompressed: diffA.String(),
					},
				},
			}, {
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobB,
					Size:      20,
					Annotations: map[string]string{
						labels.LabelUncompressed: diffB.String(),
					},
				},
			}},
		},
	}
	snapshotManager := &fakeSnapshotManager{
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"snapshot-a": exportRef,
		},
	}

	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, snapshotManager, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	key := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistSnapshotValue{}).Type()),
		Field: "cachemoney-export-snapshot",
	}
	res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(&persistSnapshotValue{
			Name:       "x",
			SnapshotID: "snapshot-a",
		}), nil
	})
	assert.NilError(t, err)
	resultID := uint64(res.cacheSharedResult().id)

	metadataDBPath := filepath.Join(t.TempDir(), cachemoneyproto.MetadataDBName)
	export, err := c.PrepareCachemoneyExport(ctx, metadataDBPath)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, export.Release(context.Background()))
	}()

	assert.Equal(t, export.MetadataDBPath, metadataDBPath)
	assert.DeepEqual(t, export.Manifest.Snapshots, []cachemoneyproto.SnapshotOffer{{
		ResultID: resultID,
		Role:     "snapshot",
		ChainID:  chainID,
	}})
	assert.Equal(t, len(export.Manifest.Chains), 1)
	assert.Equal(t, export.Manifest.Chains[0].ChainID, chainID)
	assert.Equal(t, len(export.Manifest.Chains[0].Layers), 2)
	assert.Equal(t, export.Manifest.Chains[0].Layers[0].DiffID, diffA.String())
	assert.Equal(t, export.Manifest.Chains[0].Layers[0].BlobDigest, blobA.String())
	assert.Equal(t, export.Manifest.Chains[0].Layers[1].DiffID, diffB.String())
	assert.Equal(t, export.Manifest.Chains[0].Layers[1].BlobDigest, blobB.String())
	assert.Assert(t, len(export.Manifest.Chains[0].Layers[0].DescriptorJSON) > 0)
	assert.Assert(t, exportRef.exportConfig.Compression.Force)
	assert.Equal(t, exportRef.releaseCalls, 1)
	assert.Assert(t, len(snapshotManager.attachCalls) >= 1)
	assert.Equal(t, snapshotManager.attachCalls[len(snapshotManager.attachCalls)-1].SnapshotID, "snapshot-a")

	db, q, err := prepareCacheDBs(ctx, metadataDBPath)
	assert.NilError(t, err)
	defer closeCacheDBs(db, q) //nolint:errcheck

	chainRows, err := q.ListMirrorResultSnapshotChains(ctx)
	assert.NilError(t, err)
	assert.DeepEqual(t, chainRows, []persistdb.MirrorResultSnapshotChain{{
		ResultID: int64(resultID),
		Role:     "snapshot",
		ChainID:  chainID,
	}})
	layerRows, err := q.ListMirrorSnapshotChainLayers(ctx)
	assert.NilError(t, err)
	assert.DeepEqual(t, layerRows, []persistdb.MirrorSnapshotChainLayer{{
		ChainID:        chainID,
		Position:       0,
		DiffID:         diffA.String(),
		BlobDigest:     blobA.String(),
		Size:           10,
		MediaType:      ocispecs.MediaTypeImageLayerZstd,
		DescriptorJSON: string(export.Manifest.Chains[0].Layers[0].DescriptorJSON),
	}, {
		ChainID:        chainID,
		Position:       1,
		DiffID:         diffB.String(),
		BlobDigest:     blobB.String(),
		Size:           20,
		MediaType:      ocispecs.MediaTypeImageLayerZstd,
		DescriptorJSON: string(export.Manifest.Chains[0].Layers[1].DescriptorJSON),
	}})
}

func TestPrepareCachemoneyExportPrefersLocalChainOverStaleImportedChain(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	diffLocal := digest.FromString("diff-local")
	blobLocal := digest.FromString("blob-local")
	localChainID := ociidentity.ChainID([]digest.Digest{diffLocal}).String()
	diffStale := digest.FromString("diff-stale")
	blobStale := digest.FromString("blob-stale")
	staleChainID := ociidentity.ChainID([]digest.Digest{diffStale}).String()

	exportRef := &fakeCachemoneyExportRef{
		snapshotID: "snapshot-local",
		chain: &bkcache.ExportChain{
			Layers: []bkcache.ExportLayer{{
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobLocal,
					Size:      10,
					Annotations: map[string]string{
						labels.LabelUncompressed: diffLocal.String(),
					},
				},
			}},
		},
	}
	snapshotManager := &fakeSnapshotManager{
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"snapshot-local": exportRef,
		},
	}

	dbPath := filepath.Join(t.TempDir(), "cache.db")
	c, err := NewCache(ctx, dbPath, snapshotManager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall: &ResultCall{
			Kind:  ResultCallKindField,
			Type:  NewResultCallType((&persistSnapshotValue{}).Type()),
			Field: "cachemoney-export-stale-chain",
		},
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(&persistSnapshotValue{
			Name:       "x",
			SnapshotID: "snapshot-local",
		}), nil
	})
	assert.NilError(t, err)
	shared := res.cacheSharedResult()
	shared.payloadMu.Lock()
	shared.remoteSnapshotChains = []PersistedSnapshotChain{{
		Role:    "snapshot",
		ChainID: staleChainID,
		Layers: []PersistedSnapshotChainLayer{{
			DiffID:     diffStale.String(),
			BlobDigest: blobStale.String(),
			Size:       20,
			MediaType:  ocispecs.MediaTypeImageLayerZstd,
		}},
	}}
	shared.payloadMu.Unlock()

	metadataDBPath := filepath.Join(t.TempDir(), cachemoneyproto.MetadataDBName)
	export, err := c.PrepareCachemoneyExport(ctx, metadataDBPath)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, export.Release(context.Background()))
	}()

	db, q, err := prepareCacheDBs(ctx, metadataDBPath)
	assert.NilError(t, err)
	defer closeCacheDBs(db, q) //nolint:errcheck

	chainRows, err := q.ListMirrorResultSnapshotChains(ctx)
	assert.NilError(t, err)
	assert.DeepEqual(t, chainRows, []persistdb.MirrorResultSnapshotChain{{
		ResultID: int64(shared.id),
		Role:     "snapshot",
		ChainID:  localChainID,
	}})
}

func TestCachemoneyProtoChainFromExportChainRequiresDiffID(t *testing.T) {
	t.Parallel()

	blob := digest.FromString("blob")
	_, err := cachemoneyProtoChainFromExportChain(&bkcache.ExportChain{
		Layers: []bkcache.ExportLayer{{
			Descriptor: ocispecs.Descriptor{
				MediaType: ocispecs.MediaTypeImageLayerZstd,
				Digest:    blob,
				Size:      10,
			},
		}},
	})
	assert.ErrorContains(t, err, "missing diff ID annotation")
}
