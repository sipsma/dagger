package core

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"

	"dagger.io/dagger"
	"github.com/dagger/dagger/internal/buildkit/identity"
	"github.com/dagger/dagger/internal/testutil"
)

type CacheBundleTransportSuite struct{}

func TestCacheBundleTransport(t *testing.T) {
	testctx.New(t, Middleware()...).RunTests(CacheBundleTransportSuite{})
}

// TestCrossEngineWarmViaChainFetch is the file-transport warm proof: engine
// A runs a random-marker pipeline and exports its cache (bundle + chain
// blobs into a directory CAS on a shared volume); a FRESH engine B with a
// different state key imports the bundle at boot and runs the identical
// pipeline. Outputs must be equal — the random marker would differ on any
// recompute — and the counters must show content-chain serving with zero
// demotes.
func (CacheBundleTransportSuite) TestCrossEngineWarmViaChainFetch(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	h := newBundleTransportHarness(ctx, t, c)

	// Cold seed on engine A.
	engineA := h.startEngine(ctx, t, "chainproof-a-state-"+h.id, nil)
	outA := h.runPipeline(ctx, t, engineA.client)
	require.NoError(t, engineA.client.Close())
	engineA.client = nil
	h.exportBundle(ctx, t, engineA)
	engineA.stop(ctx, t)

	// Warm run on a fresh engine B.
	stateKeyB := "chainproof-b-state-" + h.id
	engineB := h.startEngine(ctx, t, stateKeyB, h.importEnv())
	outB := h.runPipeline(ctx, t, engineB.client)
	require.Equal(t, outA, outB,
		"the warm engine must serve engine A's results — the random marker recomputes on any miss")
	engineB.stop(ctx, t)

	counters := readServeStatsCounters(ctx, t, c, stateKeyB)
	var chainServes, chainFetchOK int64
	for _, count := range counters["served_from_content_chain"] {
		chainServes += count
	}
	for _, count := range counters["chain_fetch_ok"] {
		chainFetchOK += count
	}
	require.GreaterOrEqual(t, chainServes, int64(1),
		"warm serving must realize through the content chain; counters: %v", counters)
	require.GreaterOrEqual(t, chainFetchOK, int64(1))
	require.GreaterOrEqual(t, counters["hit_restored"]["withExec"], int64(1),
		"imported withExec rows must serve as restored hits; counters: %v", counters)
	require.Empty(t, counters["demoted_to_miss"],
		"a fully-available CAS must serve without demotes; counters: %v", counters)
}

// TestSparseCASFallsThroughAndHeals is T-S6: after seeding, one chain blob
// is deleted from the CAS. The warm run must still be green — the affected
// chains fail typed (chain_fetch_missing) and fall through to the lazy form
// or the demote floor — and a second identical run on the same engine hits
// what the first run realized (the heal).
func (CacheBundleTransportSuite) TestSparseCASFallsThroughAndHeals(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	h := newBundleTransportHarness(ctx, t, c)

	engineA := h.startEngine(ctx, t, "sparse-a-state-"+h.id, nil)
	_ = h.runPipeline(ctx, t, engineA.client)
	require.NoError(t, engineA.client.Close())
	engineA.client = nil
	h.exportBundle(ctx, t, engineA)
	engineA.stop(ctx, t)

	// Delete the largest blob in the CAS — a base layer, part of every
	// forced rootfs chain.
	_, err := c.Container().
		From(alpineImage).
		WithMountedCache("/transport", h.transportVol).
		WithEnvVariable("CACHE_BUST", h.id).
		WithExec([]string{"sh", "-ec",
			`biggest=$(ls -S /transport/cas/blobs/sha256 | head -n1); rm "/transport/cas/blobs/sha256/$biggest"`}).
		Sync(ctx)
	require.NoError(t, err)

	stateKeyB := "sparse-b-state-" + h.id
	engineB := h.startEngine(ctx, t, stateKeyB, h.importEnv())
	out1 := h.runPipeline(ctx, t, engineB.client)
	// The heal: the first run's honest computation (or fall-through) is
	// served back on the second identical run.
	out2 := h.runPipeline(ctx, t, engineB.client)
	require.Equal(t, out1, out2, "the second run must hit what the first realized")
	engineB.stop(ctx, t)

	counters := readServeStatsCounters(ctx, t, c, stateKeyB)
	var missing int64
	for _, count := range counters["chain_fetch_missing"] {
		missing += count
	}
	require.GreaterOrEqual(t, missing, int64(1),
		"the deleted blob must surface as a typed missing fetch; counters: %v", counters)
}

// bundleTransportHarness wires two dev engines around a shared transport
// volume: engine A exports a bundle + directory CAS through the test-only
// debug endpoint; engine B imports them at boot through the test-only env
// wiring. This is the file transport the chunk-C service client replaces.
type bundleTransportHarness struct {
	c            *dagger.Client
	id           string
	transportVol *dagger.CacheVolume
	gc           func(*dagger.Container) *dagger.Container
}

const (
	bundleTransportBundlePath = "/transport/bundle.tar.zst"
	bundleTransportCASDir     = "/transport/cas"
)

func newBundleTransportHarness(ctx context.Context, t *testctx.T, c *dagger.Client) *bundleTransportHarness {
	t.Helper()
	id := identity.NewID()
	const gcThreshold = "1000000000000000"
	return &bundleTransportHarness{
		c:            c,
		id:           id,
		transportVol: c.CacheVolume("bundle-transport-" + id),
		gc: engineWithConfig(ctx, t,
			engineConfigWithEnabled(true),
			engineConfigWithGC(gcThreshold, "0", gcThreshold, "0"),
		),
	}
}

type bundleTransportEngine struct {
	upstreamSvc   *dagger.Service
	engineSvc     *dagger.Service
	debugSvc      *dagger.Service
	debugEndpoint string
	client        *dagger.Client
}

func (e *bundleTransportEngine) stop(ctx context.Context, t *testctx.T) {
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

// importEnv is engine B's boot wiring: import the bundle and fetch chain
// blobs from the shared CAS.
func (h *bundleTransportHarness) importEnv() map[string]string {
	return map[string]string{
		"_DAGGER_TESTONLY_IMPORT_CACHE_BUNDLE": bundleTransportBundlePath,
	}
}

func (h *bundleTransportHarness) startEngine(ctx context.Context, t *testctx.T, stateKey string, extraEnv map[string]string) *bundleTransportEngine {
	t.Helper()

	engineCtr := devEngineContainerWithStateKey(h.c, stateKey, h.gc)
	deviceName, cidr := testutil.GetUniqueNestedEngineNetwork()
	engineCtr = engineCtr.
		WithMountedCache("/transport", h.transportVol).
		WithEnvVariable("_DAGGER_TESTONLY_CACHE_TRANSPORT", "1").
		WithEnvVariable("_DAGGER_TESTONLY_CHAIN_CAS_DIR", bundleTransportCASDir).
		WithExposedPort(6060).
		WithDefaultArgs([]string{
			"--addr", "tcp://0.0.0.0:1234",
			"--debugaddr", "0.0.0.0:6060",
			"--network-name", deviceName,
			"--network-cidr", cidr,
		})
	for key, value := range extraEnv {
		engineCtr = engineCtr.WithEnvVariable(key, value)
	}

	e := &bundleTransportEngine{}
	e.upstreamSvc = devEngineContainerAsService(engineCtr)
	var err error
	e.engineSvc, err = h.c.Host().Tunnel(e.upstreamSvc, dagger.HostTunnelOpts{
		Ports: []dagger.PortForward{{Backend: 1234, Protocol: dagger.NetworkProtocolTcp}},
	}).Start(ctx)
	require.NoError(t, err)
	endpoint, err := e.engineSvc.Endpoint(ctx, dagger.ServiceEndpointOpts{Scheme: "tcp"})
	require.NoError(t, err)
	e.client, err = dagger.Connect(ctx,
		dagger.WithRunnerHost(endpoint),
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

// runPipeline is the random-marker pipeline: the deterministic exec proves
// broad reuse, the random exec makes any silent recompute change the
// output.
func (h *bundleTransportHarness) runPipeline(ctx context.Context, t *testctx.T, client *dagger.Client) string {
	t.Helper()
	out, err := client.
		Container().
		From(alpineImage).
		WithExec([]string{"sh", "-ec", "echo chainproof-deterministic > /out.txt"}).
		WithExec([]string{"sh", "-ec", "head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 >> /out.txt; cat /out.txt"}).
		Stdout(ctx)
	require.NoError(t, err)
	return out
}

// exportBundle drives the engine's test-only export endpoint: metadata
// bundle + chain blobs into the shared CAS.
func (h *bundleTransportHarness) exportBundle(ctx context.Context, t *testctx.T, e *bundleTransportEngine) {
	t.Helper()
	exportURL := e.debugEndpoint + "/debug/testonly/export-cache-bundle?bundle=" +
		url.QueryEscape(bundleTransportBundlePath) + "&cas=" + url.QueryEscape(bundleTransportCASDir)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exportURL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "export bundle: %s", string(body))
}
