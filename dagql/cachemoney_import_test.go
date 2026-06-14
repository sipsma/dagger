package dagql

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/dagger/dagger/dagql/cachemoneyproto"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"gotest.tools/v3/assert"
)

func TestRemapPersistedObjectJSONResultIDs(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"parentResultID": 1,
		"nested": {
			"childResultID": 2,
			"items": [{"serviceResultID": 0}, {"socketResultID": 3}],
			"argResultIDs": [4, 5]
		},
		"notAResult": 4
	}`)
	remapped, err := remapPersistedObjectJSONResultIDs(raw, map[uint64]uint64{
		1: 101,
		2: 102,
		3: 103,
		4: 104,
		5: 105,
	})
	assert.NilError(t, err)

	var got map[string]any
	assert.NilError(t, json.Unmarshal(remapped, &got))
	assert.Equal(t, got["parentResultID"].(float64), float64(101))
	nested := got["nested"].(map[string]any)
	assert.Equal(t, nested["childResultID"].(float64), float64(102))
	items := nested["items"].([]any)
	assert.Equal(t, items[0].(map[string]any)["serviceResultID"].(float64), float64(0))
	assert.Equal(t, items[1].(map[string]any)["socketResultID"].(float64), float64(103))
	assert.DeepEqual(t, nested["argResultIDs"], []any{float64(104), float64(105)})
	assert.Equal(t, got["notAResult"].(float64), float64(4))
}

func TestImportCachemoneyMetadataImportsSnapshotChainsWithoutRefLinks(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	diffID := digest.FromString("diff")
	blobDigest := digest.FromString("blob")
	exportRef := &fakeCachemoneyExportRef{
		snapshotID: "source-snapshot",
		chain: &bkcache.ExportChain{
			Layers: []bkcache.ExportLayer{{
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobDigest,
					Size:      123,
					Annotations: map[string]string{
						labels.LabelUncompressed: diffID.String(),
					},
				},
			}},
		},
	}
	sourceManager := &fakeSnapshotManager{
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"source-snapshot": exportRef,
		},
	}
	sourceDBPath := filepath.Join(t.TempDir(), "source.db")
	sourceCache, err := NewCache(ctx, sourceDBPath, sourceManager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, sourceCache.Close(context.Background()))
	}()

	key := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistSnapshotValue{}).Type()),
		Field: "cachemoney-source-snapshot",
	}
	sourceRes, err := sourceCache.GetOrInitCall(ctx, "source-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(&persistSnapshotValue{
			Name:       "x",
			SnapshotID: "source-snapshot",
		}), nil
	})
	assert.NilError(t, err)
	sourceResultID := sourceRes.cacheSharedResult().id

	exportPath := filepath.Join(t.TempDir(), "metadata.db")
	prepared, err := sourceCache.PrepareCachemoneyExport(ctx, exportPath)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, prepared.Release(context.Background()))
	}()

	destDBPath := filepath.Join(t.TempDir(), "dest.db")
	destCache, err := NewCache(ctx, destDBPath, &fakeSnapshotManager{}, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, destCache.Close(context.Background()))
	}()
	assert.NilError(t, destCache.ImportCachemoneyMetadata(ctx, CachemoneyImportSource{
		ID:             "source-a",
		MetadataDBPath: exportPath,
	}))

	imported := cachemoneyImportedResultByOrigin(destCache, "source-a", uint64(sourceResultID))
	assert.Assert(t, imported != nil)
	assert.Assert(t, imported.remoteCacheImported)
	assert.Assert(t, !imported.remoteCacheViable)
	assert.Assert(t, !imported.remoteCacheEligible)
	assert.Equal(t, imported.remoteCacheReason, remoteCacheReasonMissingBlobNoFallback)
	assert.DeepEqual(t, imported.loadSnapshotOwnerLinks(), []PersistedSnapshotRefLink(nil))
	chains := imported.loadRemoteSnapshotChains()
	assert.Equal(t, len(chains), 1)
	assert.Equal(t, chains[0].Role, "snapshot")
	assert.Equal(t, len(chains[0].Layers), 1)
	assert.Equal(t, chains[0].Layers[0].DiffID, diffID.String())
	assert.Equal(t, chains[0].Layers[0].BlobDigest, blobDigest.String())

	resolvedChain, ok, err := destCache.PersistedRemoteSnapshotChainByResultID(ctx, uint64(imported.id), "snapshot")
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.DeepEqual(t, resolvedChain, chains[0])
}

func TestWriteCachemoneyMetadataDBWritesImportedMergeCache(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	diffID := digest.FromString("diff")
	blobDigest := digest.FromString("blob")
	exportRef := &fakeCachemoneyExportRef{
		snapshotID: "source-snapshot",
		chain: &bkcache.ExportChain{
			Layers: []bkcache.ExportLayer{{
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobDigest,
					Size:      123,
					Annotations: map[string]string{
						labels.LabelUncompressed: diffID.String(),
					},
				},
			}},
		},
	}
	sourceManager := &fakeSnapshotManager{
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"source-snapshot": exportRef,
		},
	}
	sourceDBPath := filepath.Join(t.TempDir(), "source.db")
	sourceCache, err := NewCache(ctx, sourceDBPath, sourceManager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, sourceCache.Close(context.Background()))
	}()

	key := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistSnapshotValue{}).Type()),
		Field: "cachemoney-source-snapshot-for-merge-cache",
	}
	sourceRes, err := sourceCache.GetOrInitCall(ctx, "source-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(&persistSnapshotValue{
			Name:       "x",
			SnapshotID: "source-snapshot",
		}), nil
	})
	assert.NilError(t, err)
	sourceResultID := sourceRes.cacheSharedResult().id

	exportPath := filepath.Join(t.TempDir(), "metadata.db")
	prepared, err := sourceCache.PrepareCachemoneyExport(ctx, exportPath)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, prepared.Release(context.Background()))
	}()
	assert.Equal(t, len(prepared.Manifest.Snapshots), 1)
	chainID := prepared.Manifest.Snapshots[0].ChainID
	assert.Equal(t, len(prepared.Manifest.Chains), 1)
	assert.Equal(t, len(prepared.Manifest.Chains[0].Layers), 1)
	layer := prepared.Manifest.Chains[0].Layers[0]

	mergeCache, err := NewCache(ctx, "", nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, mergeCache.Close(context.Background()))
	}()
	assert.NilError(t, mergeCache.ImportCachemoneyMetadata(ctx, CachemoneyImportSource{
		ID:             "source-a",
		MetadataDBPath: exportPath,
	}))
	imported := cachemoneyImportedResultByOrigin(mergeCache, "source-a", uint64(sourceResultID))
	assert.Assert(t, imported != nil)

	mergedPath := filepath.Join(t.TempDir(), "merged.db")
	assert.NilError(t, mergeCache.WriteCachemoneyMetadataDB(ctx, mergedPath))

	db, q, err := openCacheDBReadOnly(ctx, mergedPath)
	assert.NilError(t, err)
	defer closeCacheDBs(db, q) //nolint:errcheck

	results, err := q.ListMirrorResults(ctx)
	assert.NilError(t, err)
	var foundImported bool
	for _, row := range results {
		if row.ID != int64(imported.id) {
			continue
		}
		foundImported = true
		assert.Equal(t, row.OriginSourceID, "source-a")
		assert.Equal(t, row.OriginResultID, int64(sourceResultID))
	}
	assert.Assert(t, foundImported)

	chainRows, err := q.ListMirrorResultSnapshotChains(ctx)
	assert.NilError(t, err)
	assert.DeepEqual(t, chainRows, []persistdb.MirrorResultSnapshotChain{{
		ResultID: int64(imported.id),
		Role:     "snapshot",
		ChainID:  chainID,
	}})

	layerRows, err := q.ListMirrorSnapshotChainLayers(ctx)
	assert.NilError(t, err)
	assert.DeepEqual(t, layerRows, []persistdb.MirrorSnapshotChainLayer{{
		ChainID:        chainID,
		Position:       0,
		DiffID:         diffID.String(),
		BlobDigest:     blobDigest.String(),
		Size:           123,
		MediaType:      ocispecs.MediaTypeImageLayerZstd,
		DescriptorJSON: string(layer.DescriptorJSON),
	}})

	cleanShutdown, found, err := q.SelectMetaValue(ctx, persistdb.MetaKeyCleanShutdown)
	assert.NilError(t, err)
	assert.Assert(t, found)
	assert.Equal(t, cleanShutdown, "1")
}

func TestImportCachemoneyMetadataSnapshotBlobIndexStampsHydrationEligible(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	diffID := digest.FromString("diff")
	blobDigest := digest.FromString("blob")
	exportRef := &fakeCachemoneyExportRef{
		snapshotID: "source-snapshot",
		chain: &bkcache.ExportChain{
			Layers: []bkcache.ExportLayer{{
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobDigest,
					Size:      123,
					Annotations: map[string]string{
						labels.LabelUncompressed: diffID.String(),
					},
				},
			}},
		},
	}
	sourceManager := &fakeSnapshotManager{
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"source-snapshot": exportRef,
		},
	}
	sourceDBPath := filepath.Join(t.TempDir(), "source.db")
	sourceCache, err := NewCache(ctx, sourceDBPath, sourceManager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, sourceCache.Close(context.Background()))
	}()

	key := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistSnapshotValue{}).Type()),
		Field: "cachemoney-source-snapshot-with-blob-index",
	}
	sourceRes, err := sourceCache.GetOrInitCall(ctx, "source-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(&persistSnapshotValue{
			Name:       "x",
			SnapshotID: "source-snapshot",
		}), nil
	})
	assert.NilError(t, err)
	sourceResultID := sourceRes.cacheSharedResult().id

	exportPath := filepath.Join(t.TempDir(), "metadata.db")
	prepared, err := sourceCache.PrepareCachemoneyExport(ctx, exportPath)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, prepared.Release(context.Background()))
	}()

	destDBPath := filepath.Join(t.TempDir(), "dest.db")
	destCache, err := NewCache(ctx, destDBPath, &fakeSnapshotManager{}, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, destCache.Close(context.Background()))
	}()
	assert.NilError(t, destCache.ImportCachemoneyMetadata(ctx, CachemoneyImportSource{
		ID:             "source-with-blob-index",
		MetadataDBPath: exportPath,
		BlobIndex: map[string]cachemoneyproto.BlobLocation{
			blobDigest.String(): {
				URL:       "https://cache.example/blobs/" + blobDigest.Encoded(),
				Size:      123,
				MediaType: ocispecs.MediaTypeImageLayerZstd,
			},
		},
	}))

	imported := cachemoneyImportedResultByOrigin(destCache, "source-with-blob-index", uint64(sourceResultID))
	assert.Assert(t, imported != nil)
	assert.Assert(t, imported.remoteCacheImported)
	assert.Assert(t, imported.remoteCacheViable)
	assert.Assert(t, imported.remoteCacheEligible)
	assert.Equal(t, imported.remoteCacheReason, remoteCacheReasonRemoteSnapshotBlobs)
}

func TestImportCachemoneyMetadataRemapsRefsAndEnablesDirectPayloadLookup(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	sourceDBPath := filepath.Join(t.TempDir(), "source.db")
	sourceCache, err := NewCache(ctx, sourceDBPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, sourceCache.Close(context.Background()))
	}()

	childKey := cacheTestIntCall("cachemoney-source-child")
	childRes, err := sourceCache.GetOrInitCall(ctx, "source-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    childKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(childKey, 11), nil
	})
	assert.NilError(t, err)
	sourceChildID := childRes.cacheSharedResult().id

	parentKey := &ResultCall{
		Kind:     ResultCallKindField,
		Type:     NewResultCallType(Int(0).Type()),
		Field:    "cachemoney-source-parent",
		Receiver: &ResultCallRef{ResultID: uint64(sourceChildID)},
	}
	parentRes, err := sourceCache.GetOrInitCall(ctx, "source-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    parentKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(parentKey, 22), nil
	})
	assert.NilError(t, err)
	sourceParentID := parentRes.cacheSharedResult().id

	exportPath := filepath.Join(t.TempDir(), "metadata.db")
	prepared, err := sourceCache.PrepareCachemoneyExport(ctx, exportPath)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, prepared.Release(context.Background()))
	}()

	destDBPath := filepath.Join(t.TempDir(), "dest.db")
	destCache, err := NewCache(ctx, destDBPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, destCache.Close(context.Background()))
	}()

	preexistingKey := cacheTestIntCall("cachemoney-preexisting")
	_, err = destCache.GetOrInitCall(ctx, "dest-session", noopTypeResolver{}, &CallRequest{
		ResultCall: preexistingKey,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(preexistingKey, 1), nil
	})
	assert.NilError(t, err)

	assert.NilError(t, destCache.ImportCachemoneyMetadata(ctx, CachemoneyImportSource{
		ID:             "source-b",
		MetadataDBPath: exportPath,
	}))
	importedChild := cachemoneyImportedResultByOrigin(destCache, "source-b", uint64(sourceChildID))
	importedParent := cachemoneyImportedResultByOrigin(destCache, "source-b", uint64(sourceParentID))
	assert.Assert(t, importedChild != nil)
	assert.Assert(t, importedParent != nil)
	assert.Assert(t, importedParent.remoteCacheImported)
	assert.Assert(t, importedParent.remoteCacheViable)
	assert.Assert(t, importedParent.remoteCacheEligible)

	remappedParentFrame := importedParent.loadResultCall()
	assert.Assert(t, remappedParentFrame != nil)
	assert.Assert(t, remappedParentFrame.Receiver != nil)
	assert.Equal(t, remappedParentFrame.Receiver.ResultID, uint64(importedChild.id))
	assert.Assert(t, remappedParentFrame.Receiver.ResultID != uint64(sourceChildID))

	requestFrame := remappedParentFrame.clone()
	resultID, err := destCache.resultIDForCall(requestFrame)
	assert.NilError(t, err)
	assert.Equal(t, resultID, importedParent.id)
}

func TestRemoteCacheEligibilitySkipsNonViableCandidate(t *testing.T) {
	t.Parallel()

	candidates := newSharedResultSet()
	nonViable := &sharedResult{
		id:                       1,
		remoteCacheImported:      true,
		remoteCacheViable:        false,
		remoteCacheEligible:      false,
		remoteCacheReason:        remoteCacheReasonMissingBlobNoFallback,
		requiredSessionResources: nil,
	}
	viable := &sharedResult{
		id:                  2,
		remoteCacheImported: true,
		remoteCacheViable:   true,
		remoteCacheEligible: true,
		remoteCacheReason:   remoteCacheReasonDirectPayload,
	}
	candidates.Insert(nonViable)
	candidates.Insert(viable)

	got := (&Cache{}).selectLookupCandidateForSessionLocked("session", candidates)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.id, viable.id)
}

func TestRemoteCacheEligibilityEnablesRetainedRecipeSnapshotAfterSlotPlans(t *testing.T) {
	t.Parallel()

	c := &Cache{
		resultsByID: map[sharedResultID]*sharedResult{
			1: {
				id:                  1,
				remoteCacheImported: true,
				persistedEnvelope: &PersistedResultEnvelope{
					Kind:       persistedResultKindObject,
					ObjectJSON: json.RawMessage(`{"form":"snapshot","lazyKind":"directory.withNewFile","lazyJSON":{"parentResultID":0}}`),
				},
				remoteSnapshotChains: []PersistedSnapshotChain{{
					Role:    "snapshot",
					ChainID: "chain-a",
					Layers: []PersistedSnapshotChainLayer{{
						DiffID:     digest.FromString("diff-a").String(),
						BlobDigest: digest.FromString("blob-a").String(),
					}},
				}},
			},
		},
	}

	facts, err := cachemoneyImportFactsForEnvelope(c.resultsByID[1].persistedEnvelope)
	assert.NilError(t, err)
	viability := c.cachemoneyResultViabilityLocked(
		1,
		nil,
		map[sharedResultID]cachemoneyResultImportFacts{1: facts},
		map[sharedResultID]cachemoneyRemoteViability{},
		map[sharedResultID]struct{}{},
	)
	assert.Assert(t, viability.viable)
	assert.Assert(t, viability.eligible)
	assert.Equal(t, viability.reason, remoteCacheReasonRetainedRecipeFallback)
}

func TestRemoteCacheEligibilityPrefersBlobBackedSnapshotOverRetainedRecipe(t *testing.T) {
	t.Parallel()

	blobDigest := digest.FromString("blob-a").String()
	env := &PersistedResultEnvelope{
		Kind:       persistedResultKindObject,
		ObjectJSON: json.RawMessage(`{"form":"snapshot","lazyKind":"directory.withNewFile","lazyJSON":{"parentResultID":0}}`),
	}
	c := &Cache{
		resultsByID: map[sharedResultID]*sharedResult{
			1: {
				id:                  1,
				remoteCacheImported: true,
				persistedEnvelope:   env,
				remoteSnapshotChains: []PersistedSnapshotChain{{
					Role:    "snapshot",
					ChainID: "chain-a",
					Layers: []PersistedSnapshotChainLayer{{
						DiffID:     digest.FromString("diff-a").String(),
						BlobDigest: blobDigest,
					}},
				}},
			},
		},
	}

	facts, err := cachemoneyImportFactsForEnvelope(env)
	assert.NilError(t, err)
	viability := c.cachemoneyResultViabilityLocked(
		1,
		map[string]bool{blobDigest: true},
		map[sharedResultID]cachemoneyResultImportFacts{1: facts},
		map[sharedResultID]cachemoneyRemoteViability{},
		map[sharedResultID]struct{}{},
	)
	assert.Assert(t, viability.viable)
	assert.Assert(t, viability.eligible)
	assert.Equal(t, viability.reason, remoteCacheReasonRemoteSnapshotBlobs)
}

func TestRemoteCacheViabilityIgnoresNestedRetainedRecipeForOwnerFallback(t *testing.T) {
	t.Parallel()

	env := &PersistedResultEnvelope{
		Kind: persistedResultKindObject,
		ObjectJSON: json.RawMessage(`{
			"form": "ready",
			"fs": {
				"form": "snapshot",
				"lazyKind": "directory.withNewFile",
				"lazyJSON": {"parentResultID": 0}
			}
		}`),
	}
	facts, err := cachemoneyImportFactsForEnvelope(env)
	assert.NilError(t, err)
	c := &Cache{
		resultsByID: map[sharedResultID]*sharedResult{
			1: {
				id:                  1,
				remoteCacheImported: true,
				persistedEnvelope:   env,
				remoteSnapshotChains: []PersistedSnapshotChain{{
					Role:    "fs",
					ChainID: "chain-a",
					Layers: []PersistedSnapshotChainLayer{{
						DiffID:     digest.FromString("diff-a").String(),
						BlobDigest: digest.FromString("blob-a").String(),
					}},
				}},
			},
		},
	}

	viability := c.cachemoneyResultViabilityLocked(
		1,
		nil,
		map[sharedResultID]cachemoneyResultImportFacts{1: facts},
		map[sharedResultID]cachemoneyRemoteViability{},
		map[sharedResultID]struct{}{},
	)
	assert.Assert(t, !viability.viable)
	assert.Assert(t, !viability.eligible)
	assert.Equal(t, viability.reason, remoteCacheReasonMissingBlobNoFallback)
}

func cachemoneyImportedResultByOrigin(c *Cache, sourceID string, originResultID uint64) *sharedResult {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	for _, res := range c.resultsByID {
		if res != nil && res.originSourceID == sourceID && res.originResultID == originResultID {
			return res
		}
	}
	return nil
}
