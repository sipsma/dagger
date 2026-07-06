package dagql

import (
	"context"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
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
