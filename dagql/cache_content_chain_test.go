package dagql

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"gotest.tools/v3/assert"

	bkcache "github.com/dagger/dagger/engine/snapshots"
)

// chainTestChains builds a deterministic content-chain identity for one
// "snapshot" role, shaped like a real two-layer chain.
func chainTestChains() []PersistedResultContentChain {
	return []PersistedResultContentChain{{
		Role:    "snapshot",
		ChainID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Layers: []PersistedContentChainLayer{
			{
				DiffID:    "sha256:1111111111111111111111111111111111111111111111111111111111111111",
				Blob:      "sha256:2222222222222222222222222222222222222222222222222222222222222222",
				Size:      42,
				MediaType: "application/vnd.oci.image.layer.v1.tar+zstd",
			},
			{
				DiffID:    "sha256:3333333333333333333333333333333333333333333333333333333333333333",
				Blob:      "sha256:4444444444444444444444444444444444444444444444444444444444444444",
				Size:      7,
				MediaType: "application/vnd.oci.image.layer.v1.tar+zstd",
			},
		},
	}}
}

// seedChainVettingStore publishes an upload-shaped row (snapshot link, no
// lazy fragment), attaches a content-chain source to it, and flushes — the
// exact shape of a bundle-imported row that hydrated locally and then
// persisted both its refKeys and its chain.
func seedChainVettingStore(t *testing.T, ctx context.Context, dbPath string, chains []PersistedResultContentChain) (uploadID uint64) {
	t.Helper()
	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)

	uploadRes, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	shared := uploadRes.cacheSharedResult()
	uploadID = uint64(shared.id)
	if len(chains) > 0 {
		shared.storeContentChains(chains)
	}

	cacheTestReleaseSession(t, cache, rootCtx)
	assert.NilError(t, cache.persistCurrentState(ctx))
	assert.NilError(t, cache.Close(context.Background()))
	return uploadID
}

// TestContentChainFlushRestoreRoundTrip pins §8 D4's local persistence: a
// row's chain source flushes into result_content_chains and restores
// verbatim, so a locally-restarted warm engine keeps its remote sources
// without re-downloading bundles.
func TestContentChainFlushRestoreRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	chains := chainTestChains()
	uploadID := seedChainVettingStore(t, ctx, dbPath, chains)

	cache, err := NewCache(ctx, dbPath, &fakeSnapshotManager{}, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

	cache.egraphMu.RLock()
	res := cache.resultsByID[sharedResultID(uploadID)]
	cache.egraphMu.RUnlock()
	assert.Assert(t, res != nil, "the seeded row must restore")

	restored := res.loadContentChains()
	assert.Equal(t, len(chains), len(restored))
	assert.Equal(t, chains[0].Role, restored[0].Role)
	assert.Equal(t, chains[0].ChainID, restored[0].ChainID)
	assert.DeepEqual(t, chains[0].Layers, restored[0].Layers)

	// The chain slots between the local snapshot and the lazy form: the
	// ratified fall-through order, checkable on the kinds list.
	kinds := res.loadPayloadState().sourceKinds
	assert.DeepEqual(t, []retainedSourceKind{sourceLocalSnapshot, sourceContentChain}, kinds)
}

// TestCacheBundleChainRoundTrip: a snapshot-backed row exports with its
// computed content chain in the manifest (never in the metadata DB), the
// importer installs the chain as the row's materialization source, and a
// re-export from the importing store emits the chain verbatim without any
// local snapshot to compute from (the growth model's +0 re-export rule).
func TestCacheBundleChainRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()
	chain := chainTestChains()[0]

	managerA := &fakeSnapshotManager{
		chainForSnapshot: map[string]bkcache.SnapshotChain{
			"snapshot-x": snapshotChainFromPersisted(chain),
		},
	}
	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), managerA, nil)
	assert.NilError(t, err)
	snapKey := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistSnapshotValue{}).Type()),
		Field: "chain-snapshot-row",
	}
	_, err = cacheA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    snapKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(&persistSnapshotValue{Name: "snap", SnapshotID: "snapshot-x"}), nil
	})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cacheA, ctx)
	originsA := bundleTestOrigins(cacheA)

	var bundle bytes.Buffer
	exportSummary, err := cacheA.ExportBundle(ctx, &bundle, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.NilError(t, cacheA.Close(context.Background()))
	assert.Equal(t, 0, exportSummary.ExcludedNoPortableContent)
	assert.Equal(t, 0, exportSummary.ChainComputeFailed)
	assert.Equal(t, 1, exportSummary.Chains)
	assert.Equal(t, 2, exportSummary.Blobs)
	assert.Equal(t, int64(49), exportSummary.BlobBytes)

	// Chains live in the manifest; the bundle's chain table stays empty
	// like every engine-local table.
	tmp := t.TempDir()
	manifest, metadataPath, err := readCacheBundleArchive(bytes.NewReader(bundle.Bytes()), tmp)
	assert.NilError(t, err)
	assert.Equal(t, 1, len(manifest.Chains))
	assert.Equal(t, chain.ChainID, manifest.Chains[0].ChainID)
	assert.Equal(t, len(chain.Layers), len(manifest.Chains[0].Layers))
	assert.Equal(t, 1, len(manifest.ResultChains))
	assert.Equal(t, "snapshot", manifest.ResultChains[0].Role)
	assert.Equal(t, 2, len(manifest.BlobIndex))
	db, q, err := prepareCacheDBs(ctx, metadataPath)
	assert.NilError(t, err)
	var chainRowCount int64
	assert.NilError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM result_content_chains`).Scan(&chainRowCount))
	assert.Equal(t, int64(0), chainRowCount)
	assert.NilError(t, closeCacheDBs(db, q))

	// Import: the chain becomes the row's materialization source.
	cacheB, err := NewCache(ctx, filepath.Join(dir, "b.db"), &fakeSnapshotManager{}, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheB.Close(context.Background()))
	}()
	seedBundleTestJunk(t, ctx, cacheB, 3)
	importSummary, err := cacheB.ImportBundle(ctx, bytes.NewReader(bundle.Bytes()))
	assert.NilError(t, err)
	assert.Equal(t, 1, importSummary.ChainSourcesInstalled)
	assert.Equal(t, 0, importSummary.ChainsSkippedMalformed)

	originsB := bundleTestOrigins(cacheB)
	var imported *sharedResult
	for origin := range originsA {
		cacheB.egraphMu.RLock()
		imported = cacheB.resultsByID[originsB[origin]]
		cacheB.egraphMu.RUnlock()
	}
	assert.Assert(t, imported != nil)
	importedChains := imported.loadContentChains()
	assert.Equal(t, 1, len(importedChains))
	assert.Equal(t, chain.Role, importedChains[0].Role)
	assert.Equal(t, chain.ChainID, importedChains[0].ChainID)
	assert.DeepEqual(t, chain.Layers, importedChains[0].Layers)
	assert.DeepEqual(t, []retainedSourceKind{sourceContentChain}, imported.loadPayloadState().sourceKinds)

	// Re-export from B: no local snapshot exists to compute from, the chain
	// crosses verbatim.
	var bundleB bytes.Buffer
	exportSummaryB, err := cacheB.ExportBundle(ctx, &bundleB, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.Equal(t, 0, exportSummaryB.ChainComputeFailed)
	assert.Equal(t, 1, exportSummaryB.Chains)
	manifestB, _, err := readCacheBundleArchive(bytes.NewReader(bundleB.Bytes()), t.TempDir())
	assert.NilError(t, err)
	assert.Equal(t, 1, len(manifestB.Chains))
	assert.Equal(t, chain.ChainID, manifestB.Chains[0].ChainID)
	assert.DeepEqual(t, manifest.Chains[0].Layers, manifestB.Chains[0].Layers)
}

// TestCacheBundleChainUnionOnDedup: a later bundle carrying a chain for an
// origin already present without one ADDS the chain source to the existing
// row — same-origin observations union sources, never replace rows.
func TestCacheBundleChainUnionOnDedup(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()
	chain := chainTestChains()[0]

	managerA := &fakeSnapshotManager{}
	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), managerA, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheA.Close(context.Background()))
	}()
	srvA := newVettingTestServer()
	rootCtxA := vettingRootCtx(ctx, cacheA, srvA)
	_, err = srvA.root.Select(rootCtxA, srvA, Selector{Field: "bothObj"})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cacheA, rootCtxA)
	originsA := bundleTestOrigins(cacheA)

	// First export: the chain fails to compute, the both-forms row crosses
	// on its lazy fragment alone.
	var bundle1 bytes.Buffer
	summary1, err := cacheA.ExportBundle(ctx, &bundle1, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.Equal(t, 1, summary1.ChainComputeFailed)
	assert.Equal(t, 0, summary1.Chains)

	// Second export: the exporting store can now produce the chain (the
	// same story as a store realizing content between exports).
	managerA.chainForSnapshot = map[string]bkcache.SnapshotChain{
		"mat-home-snap": snapshotChainFromPersisted(chain),
	}
	var bundle2 bytes.Buffer
	summary2, err := cacheA.ExportBundle(ctx, &bundle2, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.Equal(t, 0, summary2.ChainComputeFailed)
	assert.Equal(t, 1, summary2.Chains)

	cacheB, err := NewCache(ctx, filepath.Join(dir, "b.db"), &fakeSnapshotManager{}, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheB.Close(context.Background()))
	}()
	seedBundleTestJunk(t, ctx, cacheB, 2)

	summaryImport1, err := cacheB.ImportBundle(ctx, bytes.NewReader(bundle1.Bytes()))
	assert.NilError(t, err)
	assert.Equal(t, 0, summaryImport1.ChainSourcesInstalled)

	originsB := bundleTestOrigins(cacheB)
	var imported *sharedResult
	for origin := range originsA {
		cacheB.egraphMu.RLock()
		res := cacheB.resultsByID[originsB[origin]]
		cacheB.egraphMu.RUnlock()
		if res != nil && res.loadLazyFragment() != nil {
			imported = res
		}
	}
	assert.Assert(t, imported != nil)
	assert.Equal(t, 0, len(imported.loadContentChains()))

	summaryImport2, err := cacheB.ImportBundle(ctx, bytes.NewReader(bundle2.Bytes()))
	assert.NilError(t, err)
	assert.Assert(t, summaryImport2.RowsDedupedByOrigin > 0)
	assert.Equal(t, 0, summaryImport2.RowsImported)
	assert.Equal(t, 1, summaryImport2.ChainSourcesInstalled)

	unioned := imported.loadContentChains()
	assert.Equal(t, 1, len(unioned))
	assert.Equal(t, chain.ChainID, unioned[0].ChainID)
	assert.DeepEqual(t,
		[]retainedSourceKind{sourceContentChain, sourceLazyValue},
		imported.loadPayloadState().sourceKinds)
}

// snapshotChainFromPersisted converts the dagql-side chain identity into
// the snapshot manager's shape, for fake-manager wiring in tests.
func snapshotChainFromPersisted(chain PersistedResultContentChain) bkcache.SnapshotChain {
	out := bkcache.SnapshotChain{ChainID: digest.Digest(chain.ChainID)}
	for _, layer := range chain.Layers {
		out.Layers = append(out.Layers, bkcache.ChainLayer{
			DiffID:    digest.Digest(layer.DiffID),
			Blob:      digest.Digest(layer.Blob),
			Size:      layer.Size,
			MediaType: layer.MediaType,
		})
	}
	return out
}

// TestContentChainSurvivesLocalRestartVetting is T-S13: a locally-restored
// row whose refKeys are gone survives boot vetting on its persisted content
// chain — before the §8.4 vetting change such a row (no lazy fragment)
// dropped as snapshot_missing, so a locally-restarted warm engine lost every
// chain-only imported row.
func TestContentChainSurvivesLocalRestartVetting(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())

	t.Run("chain-backed row survives with links retired", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(t.TempDir(), "cache.db")
		chains := chainTestChains()
		uploadID := seedChainVettingStore(t, ctx, dbPath, chains)

		// The row's snapshot is gone before boot: the vetting lease-attach
		// presence check fails, and survival rides the persisted chain.
		manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{
			"upload-snap": {},
		}}
		cache, err := NewCache(ctx, dbPath, manager, nil)
		assert.NilError(t, err)
		defer func() {
			assert.NilError(t, cache.Close(context.Background()))
		}()
		assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

		summary := cache.restoreSummary
		assert.Assert(t, summary != nil)
		for _, dropped := range summary.DroppedResults {
			assert.Assert(t, dropped.SharedResultID != uploadID,
				"the chain-backed row must survive vetting; dropped: %+v", summary.DroppedResults)
		}

		cache.egraphMu.RLock()
		res := cache.resultsByID[sharedResultID(uploadID)]
		cache.egraphMu.RUnlock()
		assert.Assert(t, res != nil, "the chain-backed row must restore")

		// The dead snapshot source retired at vetting; the chain remains the
		// row's re-make path.
		assert.Equal(t, 0, len(res.loadSnapshotOwnerLinks()))
		assert.Equal(t, len(chains), len(res.loadContentChains()))
	})

	t.Run("row without chain or fragment still drops", func(t *testing.T) {
		t.Parallel()
		dbPath := filepath.Join(t.TempDir(), "cache.db")
		uploadID := seedChainVettingStore(t, ctx, dbPath, nil)

		manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{
			"upload-snap": {},
		}}
		cache, err := NewCache(ctx, dbPath, manager, nil)
		assert.NilError(t, err)
		defer func() {
			assert.NilError(t, cache.Close(context.Background()))
		}()

		summary := cache.restoreSummary
		assert.Assert(t, summary != nil)
		droppedUpload := false
		for _, dropped := range summary.DroppedResults {
			if dropped.SharedResultID == uploadID {
				droppedUpload = true
				assert.Equal(t, string(restoreDropSnapshotMissing), dropped.Reason)
			}
		}
		assert.Assert(t, droppedUpload, "a row with no re-make fallback must still drop")
	})
}
