package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"

	"dagger.io/dagger"
	"github.com/dagger/dagger/engine/config"
	"github.com/dagger/dagger/internal/buildkit/identity"
	"github.com/dagger/dagger/internal/testutil"
)

// The in-repo cache-service integration gate (service design §12 rung 2):
// dev engines wired to the in-repo test cache service as Dagger services,
// exercising the real transport end to end — boot import, serving-time
// chain fetch, admin-triggered export — with every proof carried by
// counters and output equality, never by green exit codes alone.

type CacheServiceSuite struct{}

func TestCacheService(t *testing.T) {
	testctx.New(t, Middleware()...).RunTests(CacheServiceSuite{})
}

//
// Harness.
//

type cacheServiceHarness struct {
	c     *dagger.Client
	id    string
	token string
	scope string

	storageVol *dagger.CacheVolume
	svc        *dagger.Service
	gc         func(*dagger.Container) *dagger.Container
}

// testCacheServiceBinPath locates the prebuilt test-service binary the
// engine-dev test harness ships next to engine.tar.
func testCacheServiceBinPath() string {
	if v, ok := os.LookupEnv("_DAGGER_TESTS_CACHESERVICE_BIN"); ok {
		return v
	}
	if v, ok := os.LookupEnv("_DAGGER_TESTS_ENGINE_TAR"); ok {
		return filepath.Join(filepath.Dir(v), "test-cacheservice")
	}
	return "./bin/test-cacheservice"
}

func newCacheServiceHarness(ctx context.Context, t *testctx.T, c *dagger.Client) *cacheServiceHarness {
	t.Helper()
	id := identity.NewID()
	h := &cacheServiceHarness{
		c:          c,
		id:         id,
		token:      "it-cachesvc-token-" + id,
		scope:      "it-scope-" + id,
		storageVol: c.CacheVolume("cachesvc-storage-" + id),
	}
	const gcThreshold = "1000000000000000"
	h.gc = engineWithConfig(ctx, t,
		engineConfigWithEnabled(true),
		engineConfigWithGC(gcThreshold, "0", gcThreshold, "0"),
	)

	h.svc = c.Container().From(alpineImage).
		WithFile("/bin/test-cacheservice", c.Host().File(testCacheServiceBinPath())).
		WithMountedCache("/data", h.storageVol).
		WithEnvVariable("CACHE_BUST", id).
		WithExposedPort(8080).
		AsService(dagger.ContainerAsServiceOpts{
			Args: []string{"/bin/test-cacheservice", "--addr", ":8080", "--root", "/data", "--token", h.token},
		})
	return h
}

// engineConfigWithCacheService points an engine at the harness service.
func (h *cacheServiceHarness) engineConfigWithCacheService() func(context.Context, *testctx.T, config.Config) config.Config {
	return func(_ context.Context, _ *testctx.T, cfg config.Config) config.Config {
		cfg.CacheService = &config.CacheServiceConfig{
			URL:   "http://cachesvc:8080",
			Token: h.token,
			Scope: h.scope,
		}
		return cfg
	}
}

type cacheServiceEngine struct {
	stateKey      string
	upstreamSvc   *dagger.Service
	engineSvc     *dagger.Service
	endpoint      string
	debugSvc      *dagger.Service
	debugEndpoint string
	client        *dagger.Client
}

func (e *cacheServiceEngine) stop(ctx context.Context, t *testctx.T) {
	t.Helper()
	if e.client != nil {
		require.NoError(t, e.client.Close())
		e.client = nil
	}
	if e.debugSvc != nil {
		_, _ = e.debugSvc.Stop(ctx)
		e.debugSvc = nil
	}
	if e.upstreamSvc != nil {
		// Graceful: clean shutdown flushes local persistence and the stats
		// file the assertions read.
		_, err := e.upstreamSvc.Stop(ctx)
		require.NoError(t, err)
		e.upstreamSvc = nil
	}
	if e.engineSvc != nil {
		_, err := e.engineSvc.Stop(ctx, dagger.ServiceStopOpts{Kill: true})
		require.NoError(t, err)
		e.engineSvc = nil
	}
}

// preseedSecretSalt writes the shared 32-byte secret salt into an engine's
// state volume before its first boot — the sanctioned harness workaround
// for the per-engine salt partition (S10).
func (h *cacheServiceHarness) preseedSecretSalt(ctx context.Context, t *testctx.T, stateKey, salt string) {
	t.Helper()
	require.Len(t, salt, 32, "the engine requires exactly 32 salt bytes")
	_, err := h.c.Container().From(alpineImage).
		WithMountedCache("/var/lib/dagger", h.c.CacheVolume(stateKey)).
		WithEnvVariable("CACHE_BUST", identity.NewID()).
		WithExec([]string{"sh", "-ec", fmt.Sprintf(`printf %%s %q > /var/lib/dagger/secret-salt`, salt)}).
		Sync(ctx)
	require.NoError(t, err)
}

func (h *cacheServiceHarness) startEngine(ctx context.Context, t *testctx.T, stateKey string) *cacheServiceEngine {
	t.Helper()

	engineCtr := devEngineContainerWithStateKey(h.c, stateKey, h.gc, func(ctr *dagger.Container) *dagger.Container {
		return engineWithConfig(ctx, t, h.engineConfigWithCacheService())(ctr)
	})
	deviceName, cidr := testutil.GetUniqueNestedEngineNetwork()
	engineCtr = engineCtr.
		WithServiceBinding("cachesvc", h.svc).
		WithExposedPort(6060).
		WithDefaultArgs([]string{
			"--addr", "tcp://0.0.0.0:1234",
			"--debugaddr", "0.0.0.0:6060",
			"--network-name", deviceName,
			"--network-cidr", cidr,
		})

	e := &cacheServiceEngine{stateKey: stateKey}
	e.upstreamSvc = devEngineContainerAsService(engineCtr)
	var err error
	e.engineSvc, err = h.c.Host().Tunnel(e.upstreamSvc, dagger.HostTunnelOpts{
		Ports: []dagger.PortForward{{Backend: 1234, Protocol: dagger.NetworkProtocolTcp}},
	}).Start(ctx)
	require.NoError(t, err)
	e.endpoint, err = e.engineSvc.Endpoint(ctx, dagger.ServiceEndpointOpts{Scheme: "tcp"})
	require.NoError(t, err)
	e.client, err = dagger.Connect(ctx,
		dagger.WithRunnerHost(e.endpoint),
		dagger.WithLogOutput(testutil.NewTWriter(t)))
	require.NoError(t, err)

	e.debugSvc, err = h.c.Host().Tunnel(e.upstreamSvc, dagger.HostTunnelOpts{
		Ports: []dagger.PortForward{{Backend: 6060, Protocol: dagger.NetworkProtocolTcp}},
	}).Start(ctx)
	require.NoError(t, err)
	e.debugEndpoint, err = e.debugSvc.Endpoint(ctx, dagger.ServiceEndpointOpts{Scheme: "http"})
	require.NoError(t, err)

	t.Cleanup(func() { e.stop(context.WithoutCancel(ctx), t) })
	return e
}

// cacheExportSummary mirrors the admin endpoint's response (§11's export
// summary).
type cacheExportSummary struct {
	BundleID            string           `json:"bundle_id"`
	Rows                int              `json:"rows"`
	Roots               int              `json:"roots"`
	Chains              int              `json:"chains"`
	BlobsOffered        int              `json:"blobs_offered"`
	BlobsUploaded       int              `json:"blobs_uploaded"`
	BlobsAlreadyPresent int              `json:"blobs_already_present"`
	BlobsSkipped        int              `json:"blobs_skipped"`
	BytesUploaded       int64            `json:"bytes_uploaded"`
	MetadataOnly        bool             `json:"metadata_only"`
	PhaseDurationsMS    map[string]int64 `json:"phase_durations_ms"`
}

// exportViaAdmin drives the productized export endpoint on the engine's
// operator listener and returns the export summary.
func (h *cacheServiceHarness) exportViaAdmin(ctx context.Context, t *testctx.T, e *cacheServiceEngine, metadataOnly bool) cacheExportSummary {
	t.Helper()
	exportURL := e.debugEndpoint + "/v1/cache/export"
	if metadataOnly {
		exportURL += "?metadataOnly=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exportURL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "cache export: %s", string(body))
	var summary cacheExportSummary
	require.NoError(t, json.Unmarshal(body, &summary), "export summary: %s", string(body))
	t.Logf("export summary (%s): %s", e.stateKey, string(body))
	return summary
}

// cacheServiceBootSummary mirrors the §8 boot summary in the stats file.
type cacheServiceBootSummary struct {
	BundlesOffered        int            `json:"bundles_offered"`
	BundlesFetched        int            `json:"bundles_fetched"`
	BundlesMerged         int            `json:"bundles_merged"`
	SkippedByReason       map[string]int `json:"bundles_skipped_by_reason"`
	RowsImported          int            `json:"rows_imported"`
	RowsDedupedByOrigin   int            `json:"rows_deduped_by_origin"`
	ImportBudgetExhausted bool           `json:"import_budget_exhausted"`
}

type cacheServiceStatsFile struct {
	Counters            map[string]map[string]int64 `json:"counters"`
	BundleImportSummary *cacheServiceBootSummary    `json:"bundle_import_summary"`
}

// readCacheServiceStats reads the full stats file the engine flushed at
// clean shutdown — counters plus the bundle boot summary.
func readCacheServiceStats(ctx context.Context, t *testctx.T, c *dagger.Client, stateKey string) cacheServiceStatsFile {
	t.Helper()
	out, err := c.
		Container().
		From(alpineImage).
		WithMountedCache("/var/lib/dagger", c.CacheVolume(stateKey)).
		WithEnvVariable("CACHE_BUST", identity.NewID()).
		WithExec([]string{"sh", "-ec", `cat "$(find /var/lib/dagger -maxdepth 4 -name dagql-cache-stats.json | head -n 1)"`}).
		Stdout(ctx)
	require.NoError(t, err)
	t.Logf("cache stats file (%s): %s", stateKey, out)
	var payload cacheServiceStatsFile
	require.NoError(t, json.Unmarshal([]byte(out), &payload), "stats file: %s", out)
	return payload
}

func sumCounter(counters map[string]map[string]int64, outcome string) int64 {
	var total int64
	for _, count := range counters[outcome] {
		total += count
	}
	return total
}

// runWarmPipeline is the random-marker pipeline: the deterministic exec
// proves broad reuse; the random exec makes any silent recompute change
// the output.
func (h *cacheServiceHarness) runWarmPipeline(ctx context.Context, t *testctx.T, client *dagger.Client) string {
	t.Helper()
	out, err := client.
		Container().
		From(alpineImage).
		WithExec([]string{"sh", "-ec", "echo cachesvc-deterministic > /out.txt"}).
		WithExec([]string{"sh", "-ec", "head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 >> /out.txt; cat /out.txt"}).
		Stdout(ctx)
	require.NoError(t, err)
	return out
}

// runSecretPipeline is the salt-partition workload: an exec that requires
// a session secret, with a random marker so any recompute is visible in
// the output. The secret name and plaintext are fixed so both engines
// derive the same handle iff their salts match.
func (h *cacheServiceHarness) runSecretPipeline(ctx context.Context, t *testctx.T, client *dagger.Client) string {
	t.Helper()
	secret := client.SetSecret("cachesvc-salt-proof", "salt-proof-plaintext")
	out, err := client.
		Container().
		From(alpineImage).
		WithSecretVariable("PROOF_SECRET", secret).
		WithExec([]string{"sh", "-ec",
			`test -n "$PROOF_SECRET"; head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1`}).
		Stdout(ctx)
	require.NoError(t, err)
	return out
}

//
// The proofs.
//

// TestCrossEngineWarmViaService is the T-S4 warm proof on the real
// transport: engine A seeds the service (boot import, admin export); a
// FRESH engine B with a different state key imports at boot and serves A's
// results, with the content chain fetched through the service client.
func (CacheServiceSuite) TestCrossEngineWarmViaService(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	h := newCacheServiceHarness(ctx, t, c)

	engineA := h.startEngine(ctx, t, "cachesvc-warm-a-"+h.id)
	outA := h.runWarmPipeline(ctx, t, engineA.client)
	require.NoError(t, engineA.client.Close())
	engineA.client = nil
	exportSummary := h.exportViaAdmin(ctx, t, engineA, false)
	require.Greater(t, exportSummary.Rows, 0, "the seed export must carry rows")
	require.Greater(t, exportSummary.Chains, 0, "the seed export must carry content chains")
	require.Greater(t, exportSummary.BlobsUploaded, 0, "a cold CAS must receive the chain blobs")
	engineA.stop(ctx, t)

	stateKeyB := "cachesvc-warm-b-" + h.id
	engineB := h.startEngine(ctx, t, stateKeyB)
	outB := h.runWarmPipeline(ctx, t, engineB.client)
	require.Equal(t, outA, outB,
		"the warm engine must serve engine A's results — the random marker recomputes on any miss")
	engineB.stop(ctx, t)

	stats := readCacheServiceStats(ctx, t, c, stateKeyB)
	require.NotNil(t, stats.BundleImportSummary, "a service-configured boot must record the import summary")
	require.Equal(t, 1, stats.BundleImportSummary.BundlesMerged, "engine A's bundle must merge at boot")
	require.Greater(t, stats.BundleImportSummary.RowsImported, 0)
	require.False(t, stats.BundleImportSummary.ImportBudgetExhausted)

	counters := stats.Counters
	require.GreaterOrEqual(t, sumCounter(counters, "served_from_content_chain"), int64(1),
		"warm serving must realize through the content chain; counters: %v", counters)
	require.GreaterOrEqual(t, sumCounter(counters, "chain_fetch_ok"), int64(1))
	require.GreaterOrEqual(t, sumCounter(counters, "chain_fetch_blobs"), int64(1),
		"realization must tally the blobs it moved; counters: %v", counters)
	require.Greater(t, sumCounter(counters, "chain_fetch_bytes"), int64(0))
	require.GreaterOrEqual(t, counters["hit_restored"]["withExec"], int64(1),
		"imported withExec rows must serve as restored hits; counters: %v", counters)
	require.Empty(t, counters["demoted_to_miss"],
		"a fully-available CAS must serve without demotes; counters: %v", counters)
}

// TestSparseBlobsFallThroughAndHeal is T-S6 on the real transport: after
// seeding, one chain blob is deleted from the service's CAS. The warm run
// stays green — the affected chain fails typed and falls through — and a
// second identical run hits what the first realized.
func (CacheServiceSuite) TestSparseBlobsFallThroughAndHeal(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	h := newCacheServiceHarness(ctx, t, c)

	engineA := h.startEngine(ctx, t, "cachesvc-sparse-a-"+h.id)
	_ = h.runWarmPipeline(ctx, t, engineA.client)
	require.NoError(t, engineA.client.Close())
	engineA.client = nil
	h.exportViaAdmin(ctx, t, engineA, false)
	engineA.stop(ctx, t)

	// Delete the largest blob in the service's CAS — a base layer, part of
	// every forced rootfs chain.
	_, err := c.Container().
		From(alpineImage).
		WithMountedCache("/data", h.storageVol).
		WithEnvVariable("CACHE_BUST", identity.NewID()).
		WithExec([]string{"sh", "-ec",
			`biggest=$(ls -S /data/blobs | head -n1); rm "/data/blobs/$biggest"`}).
		Sync(ctx)
	require.NoError(t, err)

	stateKeyB := "cachesvc-sparse-b-" + h.id
	engineB := h.startEngine(ctx, t, stateKeyB)
	out1 := h.runWarmPipeline(ctx, t, engineB.client)
	// The heal: the first run's honest computation (or fall-through) is
	// served back on the second identical run.
	out2 := h.runWarmPipeline(ctx, t, engineB.client)
	require.Equal(t, out1, out2, "the second run must hit what the first realized")
	engineB.stop(ctx, t)

	stats := readCacheServiceStats(ctx, t, c, stateKeyB)
	require.GreaterOrEqual(t, sumCounter(stats.Counters, "chain_fetch_missing"), int64(1),
		"the deleted blob must surface as a typed missing fetch; counters: %v", stats.Counters)
}

// TestReimportAcrossBootsZeroGrowth is T-S2's integration form: the same
// bundle imports on two consecutive boots of one engine, and the second
// import creates nothing — every row dedups by origin.
func (CacheServiceSuite) TestReimportAcrossBootsZeroGrowth(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	h := newCacheServiceHarness(ctx, t, c)

	engineA := h.startEngine(ctx, t, "cachesvc-reimport-a-"+h.id)
	_ = h.runWarmPipeline(ctx, t, engineA.client)
	require.NoError(t, engineA.client.Close())
	engineA.client = nil
	h.exportViaAdmin(ctx, t, engineA, false)
	engineA.stop(ctx, t)

	// Boot 1: everything in the bundle is new.
	stateKeyB := "cachesvc-reimport-b-" + h.id
	engineB := h.startEngine(ctx, t, stateKeyB)
	_ = h.runWarmPipeline(ctx, t, engineB.client)
	engineB.stop(ctx, t)
	statsBoot1 := readCacheServiceStats(ctx, t, c, stateKeyB)
	require.NotNil(t, statsBoot1.BundleImportSummary)
	require.Equal(t, 1, statsBoot1.BundleImportSummary.BundlesMerged)
	require.Greater(t, statsBoot1.BundleImportSummary.RowsImported, 0)
	require.Equal(t, 0, statsBoot1.BundleImportSummary.RowsDedupedByOrigin,
		"a fresh store has nothing to dedup against")

	// Boot 2, same state: local restore already holds every imported row;
	// the same bundle re-imports and every row dedups by origin — zero
	// growth, proven by the counters (S2).
	engineB2 := h.startEngine(ctx, t, stateKeyB)
	out := h.runWarmPipeline(ctx, t, engineB2.client)
	require.NotEmpty(t, out)
	engineB2.stop(ctx, t)
	statsBoot2 := readCacheServiceStats(ctx, t, c, stateKeyB)
	require.NotNil(t, statsBoot2.BundleImportSummary)
	require.Equal(t, 1, statsBoot2.BundleImportSummary.BundlesMerged)
	require.Equal(t, 0, statsBoot2.BundleImportSummary.RowsImported,
		"re-importing the same bundle must create zero rows; summary: %+v", statsBoot2.BundleImportSummary)
	require.Equal(t, statsBoot1.BundleImportSummary.RowsImported, statsBoot2.BundleImportSummary.RowsDedupedByOrigin,
		"every row the first boot staged must dedup by origin on the second pass")
}

// TestMultiCycleGrowthBound is T-S3: at least three consecutive
// export/import cycles over one workload — with a full local prune inside
// the third cycle — holding result-row counts flat modulo genuinely new
// work. Pruned rows return from still-selected bundles at most once
// (churn), never compounding (growth). Evidence: the per-cycle export
// summaries and stats files logged by the helpers.
func (CacheServiceSuite) TestMultiCycleGrowthBound(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	h := newCacheServiceHarness(ctx, t, c)

	runCycle := func(idx int, prune bool) (cacheExportSummary, cacheServiceStatsFile) {
		stateKey := fmt.Sprintf("cachesvc-growth-%d-%s", idx, h.id)
		e := h.startEngine(ctx, t, stateKey)
		_ = h.runWarmPipeline(ctx, t, e.client)
		require.NoError(t, e.client.Close())
		e.client = nil
		if prune {
			// The full prune, from a fresh session so the workload session's
			// async cleanup cannot pin its rows: every pruneable row goes,
			// and the next cycle proves the churn bound rather than trusting
			// it.
			h.pruneEverything(ctx, t, e)
		}
		summary := h.exportViaAdmin(ctx, t, e, false)
		e.stop(ctx, t)
		stats := readCacheServiceStats(ctx, t, c, stateKey)
		return summary, stats
	}

	cold, _ := runCycle(1, false)
	require.Greater(t, cold.Rows, 0)
	require.Greater(t, cold.BlobsUploaded, 0, "the cold cycle seeds the CAS")

	warm2, stats2 := runCycle(2, false)
	require.NotNil(t, stats2.BundleImportSummary)
	require.Equal(t, 1, stats2.BundleImportSummary.BundlesMerged)
	require.GreaterOrEqual(t, sumCounter(stats2.Counters, "hit_restored"), int64(1),
		"cycle 2 must actually reuse cycle 1's work")
	require.Equal(t, cold.Rows, warm2.Rows,
		"a fully-warm cycle must re-export exactly the rows it imported — flat, no growth")
	require.Equal(t, 0, warm2.BlobsUploaded,
		"a fully-warm cycle offers only blobs the CAS already holds (§6.6's +0 row)")

	warm3, stats3 := runCycle(3, true)
	require.NotNil(t, stats3.BundleImportSummary)
	require.Equal(t, 2, stats3.BundleImportSummary.BundlesMerged,
		"cycle 3 sees the two prior stores' newest bundles")
	require.Greater(t, stats3.BundleImportSummary.RowsDedupedByOrigin, 0,
		"overlapping bundles must collapse by origin, never per-bundle copies")
	require.Less(t, warm3.Rows, warm2.Rows,
		"the full prune must propagate into cycle 3's export — pruning travels through export (§10 D3)")

	warm4, stats4 := runCycle(4, false)
	require.NotNil(t, stats4.BundleImportSummary)
	require.Equal(t, 3, stats4.BundleImportSummary.BundlesMerged,
		"cycle 4 sees all three prior stores' newest bundles")
	require.Greater(t, stats4.BundleImportSummary.RowsDedupedByOrigin, 0)
	require.GreaterOrEqual(t, sumCounter(stats4.Counters, "hit_restored"), int64(1),
		"pruned rows must RETURN from the older stores' still-selected bundles — churn, not loss")
	require.Equal(t, warm2.Rows, warm4.Rows,
		"counts must be flat across the prune cycle: pruned rows return exactly once, nothing compounds")
	require.Equal(t, 0, warm4.BlobsUploaded,
		"returned rows re-offer existing blobs; nothing re-uploads")
}

// pruneEverything connects a fresh session and prunes with the everything
// policy, retrying while the prior session's async cleanup settles (the
// same retry discipline the local-cache suite uses).
func (h *cacheServiceHarness) pruneEverything(ctx context.Context, t *testctx.T, e *cacheServiceEngine) {
	t.Helper()
	pruneClient, err := dagger.Connect(ctx,
		dagger.WithRunnerHost(e.endpoint),
		dagger.WithLogOutput(testutil.NewTWriter(t)))
	require.NoError(t, err)
	defer func() { require.NoError(t, pruneClient.Close()) }()

	var lastUsed int
	for attempt := range 10 {
		require.NoError(t, pruneClient.Engine().LocalCache().Prune(ctx, dagger.EngineCachePruneOpts{}))
		entries := pruneClient.Engine().LocalCache().EntrySet()
		used, err := entries.DiskSpaceBytes(ctx)
		require.NoError(t, err)
		lastUsed = used
		if used == 0 {
			return
		}
		t.Logf("prune attempt %d: %d bytes still retained, retrying", attempt+1, used)
		time.Sleep(time.Second)
	}
	t.Logf("prune settled with %d bytes retained (in-use remnants); the export reflects whatever was actually pruned", lastUsed)
}

// TestMetadataOnlyExportServesViaLazyForms is T-S5: a metadata-only export
// carries no chains and no blobs; the warm engine's lookups still hit, and
// every materialization rides the lazy form — the §7 D3 recompute lever,
// proven by counters.
func (CacheServiceSuite) TestMetadataOnlyExportServesViaLazyForms(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	h := newCacheServiceHarness(ctx, t, c)

	engineA := h.startEngine(ctx, t, "cachesvc-mdonly-a-"+h.id)
	_ = h.runWarmPipeline(ctx, t, engineA.client)
	require.NoError(t, engineA.client.Close())
	engineA.client = nil
	summary := h.exportViaAdmin(ctx, t, engineA, true)
	require.True(t, summary.MetadataOnly)
	require.Greater(t, summary.Rows, 0, "metadata still crosses")
	require.Equal(t, 0, summary.Chains, "metadata-only exports carry no chains")
	require.Equal(t, 0, summary.BlobsOffered, "metadata-only exports offer no blobs")
	engineA.stop(ctx, t)

	// The warm run is green with lookups hitting and content re-made via
	// lazy forms; outputs are deliberately NOT compared — recompute is the
	// point of this lever.
	stateKeyB := "cachesvc-mdonly-b-" + h.id
	engineB := h.startEngine(ctx, t, stateKeyB)
	out := h.runWarmPipeline(ctx, t, engineB.client)
	require.NotEmpty(t, out)
	engineB.stop(ctx, t)

	stats := readCacheServiceStats(ctx, t, c, stateKeyB)
	require.NotNil(t, stats.BundleImportSummary)
	require.Equal(t, 1, stats.BundleImportSummary.BundlesMerged)
	require.Greater(t, stats.BundleImportSummary.RowsImported, 0)
	counters := stats.Counters
	require.GreaterOrEqual(t, sumCounter(counters, "hit_restored"), int64(1),
		"metadata warmth must produce restored hits; counters: %v", counters)
	require.GreaterOrEqual(t, sumCounter(counters, "served_from_lazy_form"), int64(1),
		"content must re-make through the lazy form; counters: %v", counters)
	require.Zero(t, sumCounter(counters, "served_from_content_chain"),
		"nothing may serve from chains that were never exported; counters: %v", counters)
	for _, chainCounter := range []string{"chain_fetch_ok", "chain_fetch_missing", "chain_fetch_error", "chain_fetch_corrupt"} {
		require.Zero(t, sumCounter(counters, chainCounter),
			"a chainless bundle must trigger zero chain fetches (%s); counters: %v", chainCounter, counters)
	}
}

// TestSaltPartitionScopesReuse is T-S7's partition half. Engines with
// different secret salts derive different session-resource handles from
// the same secret plaintext, and the handle participates in downstream
// call identity (content-digest scoping — deliberate, per the reset
// design). The partition therefore surfaces as a scoped identity miss: the
// secret-dependent exec recomputes on the warm engine while the non-secret
// workload still hits.
//
// AS-BUILT NOTE (deviation from the T-S9 table's literal text, reported in
// the chunk log): the design expected the partition to surface as
// candidate_ineligible_session_resources at the eligibility filter. As
// built, salted handles are baked into recipe identity, so a different-salt
// engine finds NO candidates for the salted subgraph — the eligibility gate
// never sees them. The typed counter exists and is emitted at the filter
// (unit-pinned in dagql); this proof asserts the partition through its
// as-built signature instead: scoped recompute beside non-secret reuse.
func (CacheServiceSuite) TestSaltPartitionScopesReuse(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	h := newCacheServiceHarness(ctx, t, c)

	// No pre-seeded salt: each engine mints its own at first boot.
	engineA := h.startEngine(ctx, t, "cachesvc-saltpart-a-"+h.id)
	warmOutA := h.runWarmPipeline(ctx, t, engineA.client)
	secretOutA := h.runSecretPipeline(ctx, t, engineA.client)
	require.NoError(t, engineA.client.Close())
	engineA.client = nil
	h.exportViaAdmin(ctx, t, engineA, false)
	engineA.stop(ctx, t)

	stateKeyB := "cachesvc-saltpart-b-" + h.id
	engineB := h.startEngine(ctx, t, stateKeyB)
	warmOutB := h.runWarmPipeline(ctx, t, engineB.client)
	secretOutB := h.runSecretPipeline(ctx, t, engineB.client)
	engineB.stop(ctx, t)

	require.Equal(t, warmOutA, warmOutB,
		"the non-secret workload must transfer — the partition is scoped, not engine-wide")
	require.NotEqual(t, secretOutA, secretOutB,
		"the secret-dependent exec must recompute across differing salts — matching would leak the partition")

	stats := readCacheServiceStats(ctx, t, c, stateKeyB)
	require.NotNil(t, stats.BundleImportSummary)
	require.Equal(t, 1, stats.BundleImportSummary.BundlesMerged)
	require.GreaterOrEqual(t, stats.Counters["hit_restored"]["withExec"], int64(1),
		"non-secret withExec rows must hit; counters: %v", stats.Counters)
	require.GreaterOrEqual(t, stats.Counters["miss_first"]["withExec"], int64(1),
		"the salted withExec must miss and execute; counters: %v", stats.Counters)
}

// TestSharedSaltCrossEngineHit is T-S7's second half: with the secret salt
// pre-seeded identically into both engines' state volumes, the same secret
// plaintext derives the same handle, identity aligns, the session holds
// the handle, and the secret-dependent exec HITS across engines — the
// random marker transfers verbatim.
func (CacheServiceSuite) TestSharedSaltCrossEngineHit(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	h := newCacheServiceHarness(ctx, t, c)

	sharedSalt := "0123456789abcdef0123456789abcdef"
	stateKeyA := "cachesvc-sharedsalt-a-" + h.id
	stateKeyB := "cachesvc-sharedsalt-b-" + h.id
	h.preseedSecretSalt(ctx, t, stateKeyA, sharedSalt)
	h.preseedSecretSalt(ctx, t, stateKeyB, sharedSalt)

	engineA := h.startEngine(ctx, t, stateKeyA)
	secretOutA := h.runSecretPipeline(ctx, t, engineA.client)
	require.NoError(t, engineA.client.Close())
	engineA.client = nil
	h.exportViaAdmin(ctx, t, engineA, false)
	engineA.stop(ctx, t)

	engineB := h.startEngine(ctx, t, stateKeyB)
	secretOutB := h.runSecretPipeline(ctx, t, engineB.client)
	engineB.stop(ctx, t)

	require.Equal(t, secretOutA, secretOutB,
		"with a shared salt the secret-dependent exec must transfer — the random marker recomputes on any miss")

	stats := readCacheServiceStats(ctx, t, c, stateKeyB)
	require.NotNil(t, stats.BundleImportSummary)
	require.Equal(t, 1, stats.BundleImportSummary.BundlesMerged)
	require.GreaterOrEqual(t, stats.Counters["hit_restored"]["withExec"], int64(1),
		"the salted subgraph must serve as restored hits under a shared salt; counters: %v", stats.Counters)
	require.Zero(t, sumCounter(stats.Counters, "candidate_ineligible_session_resources"),
		"a shared salt with a bound secret must not trip the eligibility gate; counters: %v", stats.Counters)
}
