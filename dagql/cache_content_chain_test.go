package dagql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
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
	return chain.bkSnapshotChain()
}

// seedChainVettingStoreBoth seeds a both-forms row (snapshot link
// "mat-home-snap" + lazy fragment) carrying a content-chain source, and
// flushes.
func seedChainVettingStoreBoth(t *testing.T, ctx context.Context, dbPath string, chains []PersistedResultContentChain) uint64 {
	t.Helper()
	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)

	bothRes, err := srv.root.Select(rootCtx, srv, Selector{Field: "bothObj"})
	assert.NilError(t, err)
	shared := bothRes.cacheSharedResult()
	if len(chains) > 0 {
		shared.storeContentChains(chains)
	}
	id := uint64(shared.id)

	cacheTestReleaseSession(t, cache, rootCtx)
	assert.NilError(t, cache.persistCurrentState(ctx))
	assert.NilError(t, cache.Close(context.Background()))
	return id
}

// TestContentChainRealizesImportedRow is the walk-level warm proof (the
// dagql form of T-S4's serving half): a restored row whose refKeys are gone
// but whose chain survives realizes through the chain arm — the chain
// materializes into a local snapshot, the ordinary decode serves it, and the
// row afterwards has a real local-snapshot source that survives another
// restart.
func TestContentChainRealizesImportedRow(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	chains := chainTestChains()
	uploadID := seedChainVettingStore(t, ctx, dbPath, chains)

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{
		"upload-snap": {},
	}}
	manager.materializeChainFunc = func(ctx context.Context, ownerLeaseID string, chain bkcache.SnapshotChain, src bkcache.BlobSource) (string, error) {
		assert.Equal(t, chains[0].ChainID, chain.ChainID.String())
		assert.NilError(t, manager.AttachLease(ctx, ownerLeaseID, "chain-mat-snap"))
		return "chain-mat-snap", nil
	}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	assert.Assert(t, res.HitCache(), "chain realization serves a hit, not a recompute")
	obj, ok := UnwrapAs[*matSnapOnlyObj](res.Unwrap())
	assert.Assert(t, ok)
	assert.Equal(t, "upload", obj.Name)

	counters := cache.serveStats.byOutcome()
	assert.Equal(t, int64(1), counters[cacheServeFromContentChain]["uploadObj"])
	assert.Equal(t, int64(1), counters[cacheChainFetchOK]["uploadObj"])
	assert.Equal(t, int64(1), counters[cacheServeHitRestored]["uploadObj"])
	assert.Equal(t, int64(0), counters[cacheServeDemotedToMiss]["uploadObj"])
	assert.Equal(t, 1, manager.materializeChainCallCount())

	// Content arrived once: the home now has a real local-snapshot source
	// ahead of the chain.
	shared := res.cacheSharedResult()
	links := shared.loadSnapshotOwnerLinks()
	assert.Equal(t, 1, len(links))
	assert.Equal(t, "chain-mat-snap", links[0].RefKey)
	assert.DeepEqual(t,
		[]retainedSourceKind{sourceLocalSnapshot, sourceContentChain},
		shared.loadPayloadState().sourceKinds)
	cacheTestReleaseSession(t, cache, rootCtx)

	// A local restart keeps both: the fresh snapshot and the chain.
	assert.NilError(t, cache.persistCurrentState(ctx))
	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	var linkCount, chainCount int64
	assert.NilError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM result_snapshot_links WHERE result_id = ?`, uploadID).Scan(&linkCount))
	assert.NilError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM result_content_chains WHERE result_id = ?`, uploadID).Scan(&chainCount))
	assert.Equal(t, int64(1), linkCount)
	assert.Equal(t, int64(1), chainCount)
	assert.NilError(t, closeCacheDBs(db, q))
}

// TestContentChainLocalSnapshotWins pins the ratified order's first step: a
// row with a live local snapshot never touches its chain.
func TestContentChainLocalSnapshotWins(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	seedChainVettingStore(t, ctx, dbPath, chainTestChains())

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{}}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	assert.Assert(t, res.HitCache())

	counters := cache.serveStats.byOutcome()
	assert.Equal(t, int64(1), counters[cacheServeFromSnapshot]["uploadObj"])
	assert.Equal(t, int64(0), counters[cacheServeFromContentChain]["uploadObj"])
	assert.Equal(t, 0, manager.materializeChainCallCount())
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestContentChainMissingBlobFallsThroughToFragment is T-S6's walk half: a
// permanently missing chain blob marks the source non-viable for the boot
// and the fragment serves — still a hit, no demote, identity retained for
// the next boot's retry.
func TestContentChainMissingBlobFallsThroughToFragment(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	seedChainVettingStoreBoth(t, ctx, dbPath, chainTestChains())

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{
		"mat-home-snap": {},
	}}
	manager.materializeChainFunc = func(context.Context, string, bkcache.SnapshotChain, bkcache.BlobSource) (string, error) {
		return "", fmt.Errorf("blob sha256:2222: %w", bkcache.ErrBlobNotFound)
	}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "bothObj"})
	assert.NilError(t, err)
	assert.Assert(t, res.HitCache(), "fragment fall-through is still a hit")

	counters := cache.serveStats.byOutcome()
	assert.Equal(t, int64(1), counters[cacheChainFetchMissing]["bothObj"])
	assert.Equal(t, int64(1), counters[cacheServeFromLazyForm]["bothObj"])
	assert.Equal(t, int64(0), counters[cacheServeDemotedToMiss]["bothObj"])

	// Marked non-viable for this boot; the identity survives for the next.
	shared := res.cacheSharedResult()
	assert.Equal(t, 0, len(shared.loadViableContentChains()))
	assert.Equal(t, 1, len(shared.loadContentChains()))
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestContentChainMissingBlobNoFragmentDemotes: with no fragment behind the
// missing blob, the walk exhausts permanently — demote, drop, heal.
func TestContentChainMissingBlobNoFragmentDemotes(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	uploadID := seedChainVettingStore(t, ctx, dbPath, chainTestChains())

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{
		"upload-snap": {},
	}}
	manager.materializeChainFunc = func(context.Context, string, bkcache.SnapshotChain, bkcache.BlobSource) (string, error) {
		return "", fmt.Errorf("blob sha256:2222: %w", bkcache.ErrBlobNotFound)
	}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	assert.Assert(t, !res.HitCache(), "the demoted invocation executes live")

	counters := cache.serveStats.byOutcome()
	assert.Equal(t, int64(1), counters[cacheChainFetchMissing]["uploadObj"])
	assert.Equal(t, int64(1), counters[cacheServeDemotedToMiss]["uploadObj"])

	// True exhaustion is terminal for the row.
	cache.egraphMu.RLock()
	old := cache.resultsByID[sharedResultID(uploadID)]
	dropped := old != nil && old.dropped
	cache.egraphMu.RUnlock()
	assert.Assert(t, old == nil || dropped, "a permanently exhausted row must drop")

	// The heal: the live publication serves the follow-up.
	res2, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	assert.Assert(t, res2.HitCache())
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestContentChainTransientFailureDemotesWithoutDrop: a transport-shaped
// chain failure on a chain-only row demotes this use to an honest live
// execution but drops nothing — nothing permanent was learned, the row
// stays for the next walk to retry.
func TestContentChainTransientFailureDemotesWithoutDrop(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	uploadID := seedChainVettingStore(t, ctx, dbPath, chainTestChains())

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{
		"upload-snap": {},
	}}
	manager.materializeChainFunc = func(context.Context, string, bkcache.SnapshotChain, bkcache.BlobSource) (string, error) {
		return "", fmt.Errorf("dial cas: connection refused")
	}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	assert.Assert(t, !res.HitCache(), "the demoted invocation executes live")

	counters := cache.serveStats.byOutcome()
	assert.Equal(t, int64(1), counters[cacheChainFetchError]["uploadObj"])
	assert.Equal(t, int64(1), counters[cacheServeDemotedToMiss]["uploadObj"])

	// The row survives with its chain source unmarked: the next walk
	// retries.
	cache.egraphMu.RLock()
	old := cache.resultsByID[sharedResultID(uploadID)]
	cache.egraphMu.RUnlock()
	assert.Assert(t, old != nil)
	assert.Assert(t, !old.dropped, "a transiently starved row must not drop")
	assert.Equal(t, 1, len(old.loadViableContentChains()))
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestContentChainConcurrentForcingSingleflights is T-S8: N concurrent
// forcings of one chain-backed imported row produce exactly one chain
// materialization; everyone else waits and shares the outcome (run with
// -race).
func TestContentChainConcurrentForcingSingleflights(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	seedChainVettingStore(t, ctx, dbPath, chainTestChains())

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{
		"upload-snap": {},
	}}
	manager.materializeChainFunc = func(ctx context.Context, ownerLeaseID string, _ bkcache.SnapshotChain, _ bkcache.BlobSource) (string, error) {
		time.Sleep(30 * time.Millisecond)
		assert.NilError(t, manager.AttachLease(ctx, ownerLeaseID, "chain-mat-snap"))
		return "chain-mat-snap", nil
	}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()

	const demanders = 16
	var wg sync.WaitGroup
	values := make([]string, demanders)
	errs := make([]error, demanders)
	for i := 0; i < demanders; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			srv := newVettingTestServer()
			rootCtx := vettingRootCtx(ctx, cache, srv)
			res, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
			if err != nil {
				errs[worker] = err
				return
			}
			obj, ok := UnwrapAs[*matSnapOnlyObj](res.Unwrap())
			if !ok {
				errs[worker] = fmt.Errorf("unexpected value type")
				return
			}
			values[worker] = obj.Name
		}(i)
	}
	wg.Wait()

	for worker := 0; worker < demanders; worker++ {
		assert.NilError(t, errs[worker], "worker %d", worker)
		assert.Equal(t, "upload", values[worker], "worker %d", worker)
	}
	assert.Equal(t, 1, manager.materializeChainCallCount(),
		"exactly one chain materialization for N concurrent forcings")
	counters := cache.serveStats.byOutcome()
	assert.Equal(t, int64(1), counters[cacheChainFetchOK]["uploadObj"])
	assert.Equal(t, int64(1), counters[cacheServeFromContentChain]["uploadObj"])
	assert.Equal(t, int64(0), counters[cacheServeDemotedToMiss]["uploadObj"])
	cacheTestReleaseSession(t, cache, ctx)
}

// contentlessProbeObj is a registered identity-only type: it owns a mutable
// snapshot locally, so it exports with neither chain nor refKeys — identity
// alone crosses, and the importing engine's decoder re-acquires lazily.
type contentlessProbeObj struct {
	Name       string
	SnapshotID string
}

func (*contentlessProbeObj) Type() *ast.Type {
	return &ast.Type{NamedType: "ContentlessProbeObj", NonNull: true}
}

func (v *contentlessProbeObj) EncodePersistedObject(ctx context.Context, cache PersistedObjectCache) (PersistedObjectEncoding, error) {
	_ = ctx
	_ = cache
	payload, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: v.Name})
	if err != nil {
		return PersistedObjectEncoding{}, err
	}
	return PersistedObjectEncoding{
		JSON:          payload,
		SnapshotLinks: v.PersistedSnapshotRefLinks(),
	}, nil
}

func (v *contentlessProbeObj) PersistedSnapshotRefLinks() []PersistedSnapshotRefLink {
	if v == nil || v.SnapshotID == "" {
		return nil
	}
	return []PersistedSnapshotRefLink{{RefKey: v.SnapshotID, Role: "snapshot"}}
}

func init() {
	RegisterContentlessPersistedType("ContentlessProbeObj")
}

// TestCacheBundleContentlessTypeExportsIdentityOnly: a registered
// mutable-owner type's snapshot-linked row crosses identity-only — no chain
// is attempted (mutable-owner snapshots never produce chains), no refKeys
// cross, and the row is not excluded.
func TestCacheBundleContentlessTypeExportsIdentityOnly(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()

	// A fake manager with no computable chains: an unregistered type would
	// count a chain-compute failure; the registered type must not even try.
	manager := &fakeSnapshotManager{}
	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheA.Close(context.Background()))
	}()

	probeKey := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&contentlessProbeObj{}).Type()),
		Field: "contentless-probe",
	}
	probeRes, err := cacheA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    probeKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(&contentlessProbeObj{Name: "probe", SnapshotID: "probe-mutable-snap"}), nil
	})
	assert.NilError(t, err)
	probeRowID := probeRes.cacheSharedResult().id
	cacheTestReleaseSession(t, cacheA, ctx)

	var bundle bytes.Buffer
	summary, err := cacheA.ExportBundle(ctx, &bundle, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.Equal(t, 0, summary.ExcludedNoPortableContent)
	assert.Equal(t, 0, summary.ChainComputeFailed)
	assert.Equal(t, 0, summary.Chains)

	tmp := t.TempDir()
	manifest, metadataPath, err := readCacheBundleArchive(bytes.NewReader(bundle.Bytes()), tmp)
	assert.NilError(t, err)
	assert.Equal(t, 0, len(manifest.ResultChains))
	rows, err := readBundleMetadataRows(ctx, metadataPath)
	assert.NilError(t, err)
	found := false
	for _, row := range rows.results {
		if sharedResultID(row.ID) == probeRowID {
			found = true
		}
	}
	assert.Assert(t, found, "the identity-only row must cross")
	db, q, err := prepareCacheDBs(ctx, metadataPath)
	assert.NilError(t, err)
	var linkCount int64
	assert.NilError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM result_snapshot_links`).Scan(&linkCount))
	assert.Equal(t, int64(0), linkCount)
	assert.NilError(t, closeCacheDBs(db, q))
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
