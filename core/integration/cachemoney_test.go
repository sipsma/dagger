package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dagger.io/dagger"
	bkconfig "github.com/dagger/dagger/internal/buildkit/cmd/buildkitd/config"
	"github.com/dagger/dagger/internal/buildkit/identity"
	"github.com/dagger/dagger/internal/testutil"
	"github.com/dagger/testctx"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/require"
)

const cachemoneyDebugHTTPTimeout = 2 * time.Minute

func (CachePersistenceSuite) TestCachemoneyD0RealBackendRoundTrip(ctx context.Context, t *testctx.T) {
	cachemoneyD0LoadEnvFile(t)
	if os.Getenv("CACHEMONEY_D0") != "1" {
		t.Skip("set CACHEMONEY_D0=1 to run the cross-repo D0 cachemoney validation")
	}
	backendSource := cachemoneyD0BackendSourceConfig(t)

	c := connect(ctx, t, dagger.WithLogOutput(testutil.NewTWriter(t)))
	minio := startCachemoneyD0MinIO(ctx, t, c)
	backendLeaf := startCachemoneyD0Backend(ctx, t, c, minio, backendSource, "")
	backendAll := startCachemoneyD0Backend(ctx, t, c, minio, backendSource, "all")
	backendMetadataOnly := startCachemoneyD0Backend(ctx, t, c, minio, backendSource, "metadata-only")

	hostDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "input.txt"), []byte("cachemoney d0 filesync input\n"), 0o644))
	moduleDir := cachemoneyD0ModuleDir(t)
	httpSource := startCachemoneyD0HTTPSource(ctx, t, c)
	httpBinding := cachemoneyDebugServiceBinding{Hostname: "cachemoney-http", Service: httpSource}

	gitSvc, gitRepoURL := gitService(ctx, t, c, c.Directory().WithNewFile("README.md", "cachemoney d0 git input\n"))
	parsedGitRepoURL, err := url.Parse(gitRepoURL)
	require.NoError(t, err)
	gitBinding := cachemoneyDebugServiceBinding{Hostname: parsedGitRepoURL.Hostname(), Service: gitSvc}

	workload := cachemoneyD0Workload{
		CacheBust:      "cachemoney-d0-stable-input",
		HostDir:        hostDir,
		ModuleDir:      moduleDir,
		CacheVolumeKey: "cachemoney-d0-source-cache-" + identity.NewID(),
		GitRepoURL:     gitRepoURL,
	}
	source := startCachemoneyDebugEngine(ctx, t, c, backendLeaf, "cachemoney-d0-source-state-"+identity.NewID(),
		cachemoneyDebugServiceBinding{Hostname: "cachemoney-backend-all", Service: backendAll},
		cachemoneyDebugServiceBinding{Hostname: "cachemoney-backend-meta", Service: backendMetadataOnly},
		cachemoneyDebugServiceBinding{Hostname: "minio", Service: minio},
		gitBinding,
		httpBinding,
	)
	cachemoneyD0DevelopModule(ctx, t, source, workload)
	sourceWorkload := cachemoneyD0WorkloadContainersFor(source.client, workload)
	cachemoneyD0PrimeWorkload(ctx, t, sourceWorkload)
	cachemoneyD0PrimeModuleRandom(ctx, t, source, workload)
	sourceHTTPStats := cachemoneyD0FetchHTTPStats(ctx, t, c, httpSource)
	require.Greater(t, sourceHTTPStats.OK, 0)

	exportLeaf := cachemoneyDebugExportToURL(ctx, t, source.debugURL, "http://cachemoney-backend:8080/cachemoney/v1/exports")
	require.True(t, exportLeaf.Completed)
	require.NotEmpty(t, exportLeaf.ExportID)
	require.Greater(t, exportLeaf.Snapshots, 0)
	require.Greater(t, exportLeaf.BlobsOffered, 0)
	require.Greater(t, exportLeaf.BlobsRequested, 0)
	require.Greater(t, exportLeaf.BlobsUploaded, 0, "%+v", exportLeaf)
	require.Zero(t, exportLeaf.BlobsFailed, "%+v", exportLeaf)
	leafState := cachemoneyD0FetchBackendDebugState(ctx, t, c, backendLeaf)
	require.Equal(t, 1, leafState.Summary.SourceCount)
	require.Greater(t, leafState.Summary.BlobCount, 0)
	require.Zero(t, leafState.Summary.PendingExportCount)
	cachemoneyD0RequireEmptySnapshotSentinel(t, leafState)

	exportWithBlobs := cachemoneyDebugExportToURL(ctx, t, source.debugURL, "http://cachemoney-backend-all:8080/cachemoney/v1/exports")
	require.True(t, exportWithBlobs.Completed)
	require.NotEmpty(t, exportWithBlobs.ExportID)
	require.Greater(t, exportWithBlobs.Snapshots, 0)
	require.Greater(t, exportWithBlobs.BlobsOffered, 0)
	require.Greater(t, exportWithBlobs.BlobsRequested, 0)
	require.Greater(t, exportWithBlobs.BlobsUploaded, 0, "%+v", exportWithBlobs)
	require.Zero(t, exportWithBlobs.BlobsFailed, "%+v", exportWithBlobs)
	allState := cachemoneyD0FetchBackendDebugState(ctx, t, c, backendAll)
	require.Equal(t, 1, allState.Summary.SourceCount)
	require.Greater(t, allState.Summary.BlobCount, 0)
	require.Zero(t, allState.Summary.PendingExportCount)
	cachemoneyD0RequireEmptySnapshotSentinel(t, allState)
	cachemoneyD0RequireSnapshotRole(t, allState, "mount_dir:0")
	cachemoneyD0RequireSnapshotRole(t, allState, "mount_file:0")

	exportMetadataOnly := cachemoneyDebugExportToURL(ctx, t, source.debugURL, "http://cachemoney-backend-meta:8080/cachemoney/v1/exports")
	require.True(t, exportMetadataOnly.Completed)
	require.NotEmpty(t, exportMetadataOnly.ExportID)
	require.Greater(t, exportMetadataOnly.Snapshots, 0)
	require.Greater(t, exportMetadataOnly.BlobsOffered, 0)
	require.Zero(t, exportMetadataOnly.BlobsFailed, "%+v", exportMetadataOnly)
	metadataOnlyState := cachemoneyD0FetchBackendDebugState(ctx, t, c, backendMetadataOnly)
	require.Equal(t, 1, metadataOnlyState.Summary.SourceCount)
	require.Zero(t, metadataOnlyState.Summary.PendingExportCount)
	cachemoneyD0RequireEmptySnapshotSentinel(t, metadataOnlyState)

	sourceOutput := cachemoneyD0ReadWorkload(ctx, t, sourceWorkload)
	sourceModuleRandom := cachemoneyD0ReadModuleRandom(ctx, t, source, workload)
	source.stop(ctx, t)

	hydrateAll := startCachemoneyDebugEngine(ctx, t, c, backendAll, "cachemoney-d0-hydrate-all-state-"+identity.NewID(),
		cachemoneyDebugServiceBinding{Hostname: "minio", Service: minio},
		gitBinding,
		httpBinding,
	)
	importHydrateAll := cachemoneyDebugImportFromURL(ctx, t, hydrateAll.debugURL, "http://cachemoney-backend:8080/cachemoney/v1/import")
	require.True(t, importHydrateAll.Imported)
	require.Greater(t, importHydrateAll.BlobLocations, 0)
	hydratedAllOutput := cachemoneyD0RunWorkload(ctx, t, hydrateAll.client, workload)
	require.Equal(t, sourceOutput, hydratedAllOutput, "all-policy D0 cache hit should hydrate every mutable-source dependent workload")
	httpBeforeHydrateAll := cachemoneyD0FetchHTTPStats(ctx, t, c, httpSource)
	hydratedModuleRandom := cachemoneyD0ReadModuleRandom(ctx, t, hydrateAll, workload)
	require.Equal(t, sourceModuleRandom, hydratedModuleRandom, "all-policy D0 cache hit should hydrate the module-loaded HTTP/container file workload")
	httpAfterHydrateAll := cachemoneyD0FetchHTTPStats(ctx, t, c, httpSource)
	require.Equal(t, httpBeforeHydrateAll.OK, httpAfterHydrateAll.OK, "hydrate-all module workload should serve from imported cache without fetching a fresh 200")
	hydrateAllStats := cachemoneyDebugStats(ctx, t, hydrateAll.debugURL)
	require.Greater(t, cachemoneyMaterializationOutcomeTotal(hydrateAllStats, "hydrated"), uint64(0))
	require.Zero(t, cachemoneyStatsTotal(hydrateAllStats.Cachemoney.HydrationFailures), "%+v", hydrateAllStats.Cachemoney.HydrationFailures)
	require.Zero(t, hydrateAllStats.Cachemoney.RecomputeReasons["index_miss"], "%+v", hydrateAllStats.Cachemoney.RecomputeReasons)
	hydrateAll.stop(ctx, t)

	hydrate := startCachemoneyDebugEngine(ctx, t, c, backendLeaf, "cachemoney-d0-hydrate-state-"+identity.NewID(),
		cachemoneyDebugServiceBinding{Hostname: "minio", Service: minio},
		gitBinding,
		httpBinding,
	)
	importHydrate := cachemoneyDebugImportFromURL(ctx, t, hydrate.debugURL, "http://cachemoney-backend:8080/cachemoney/v1/import")
	require.True(t, importHydrate.Imported)
	require.Greater(t, importHydrate.BlobLocations, 0)
	hydratedOutput := cachemoneyD0RunWorkload(ctx, t, hydrate.client, workload)
	require.Equal(t, sourceOutput.Random, hydratedOutput.Random, "default-policy blob-backed D0 cache hit should hydrate the source snapshot")
	hydrateStats := cachemoneyDebugStats(ctx, t, hydrate.debugURL)
	require.Greater(t, cachemoneyMaterializationOutcomeTotal(hydrateStats, "hydrated"), uint64(0))
	hydrate.stop(ctx, t)

	recompute := startCachemoneyDebugEngine(ctx, t, c, backendMetadataOnly, "cachemoney-d0-recompute-state-"+identity.NewID(),
		cachemoneyDebugServiceBinding{Hostname: "minio", Service: minio},
		gitBinding,
		httpBinding,
	)
	importRecompute := cachemoneyDebugImportFromURL(ctx, t, recompute.debugURL, "http://cachemoney-backend:8080/cachemoney/v1/import")
	require.True(t, importRecompute.Imported)
	recomputedOutput := cachemoneyD0RunWorkload(ctx, t, recompute.client, workload)
	require.NotEqual(t, sourceOutput.Random, recomputedOutput.Random, "metadata-only D0 cache hit should recompute when content blobs are missing")
	require.NotEqual(t, sourceOutput.MountedChangesetPatch, recomputedOutput.MountedChangesetPatch, "metadata-only D0 changeset should recompute when mounted source blobs are missing")
	recomputeStats := cachemoneyDebugStats(ctx, t, recompute.debugURL)
	require.Greater(t, cachemoneyMaterializationOutcomeTotal(recomputeStats, "recomputed_remote_miss"), uint64(0))
	require.Greater(t, recomputeStats.Cachemoney.RecomputeReasons["index_miss"], uint64(0))
	recompute.stop(ctx, t)
}

func (CachePersistenceSuite) TestCachemoneyExportImportWarmHit(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	backend := startCachemoneyBackend(ctx, t, c)

	const cacheBust = "cachemoney-integration-stable-input"
	source := startCachemoneyDebugEngine(ctx, t, c, backend, "cachemoney-source-state-"+identity.NewID())
	sourceCtr := cachemoneyRandomExecContainer(source.client, cacheBust)
	_, err := sourceCtr.Sync(ctx)
	require.NoError(t, err)

	exportWithBlobs := cachemoneyDebugExport(ctx, t, source.debugURL, "all")
	require.True(t, exportWithBlobs.Completed)
	require.NotEmpty(t, exportWithBlobs.ExportID)
	require.Greater(t, exportWithBlobs.Snapshots, 0)
	require.Greater(t, exportWithBlobs.BlobsOffered, 0)
	require.Greater(t, exportWithBlobs.BlobsUploaded, 0)

	exportMetadataOnly := cachemoneyDebugExport(ctx, t, source.debugURL, "metadata-only")
	require.True(t, exportMetadataOnly.Completed)
	require.NotEmpty(t, exportMetadataOnly.ExportID)
	require.Greater(t, exportMetadataOnly.Snapshots, 0)
	require.Greater(t, exportMetadataOnly.BlobsOffered, 0)
	require.Zero(t, exportMetadataOnly.BlobsRequested)
	require.Zero(t, exportMetadataOnly.BlobsUploaded)

	sourceContents, err := sourceCtr.File("/work/random.txt").Contents(ctx)
	require.NoError(t, err)
	sourceRandom := strings.TrimSpace(sourceContents)
	require.NotEmpty(t, sourceRandom)
	source.stop(ctx, t)

	hydrate := startCachemoneyDebugEngine(ctx, t, c, backend, "cachemoney-hydrate-state-"+identity.NewID())
	importHydrate := cachemoneyDebugImport(ctx, t, hydrate.debugURL, exportWithBlobs.ExportID)
	require.True(t, importHydrate.Imported)
	require.Greater(t, importHydrate.BlobLocations, 0)
	hydratedRandom := cachemoneyRandomExecFileContents(ctx, t, hydrate.client, cacheBust)
	require.Equal(t, sourceRandom, hydratedRandom, "blob-backed remote cache hit should hydrate the source snapshot")
	hydrateStats := cachemoneyDebugStats(ctx, t, hydrate.debugURL)
	require.Greater(t, cachemoneyMaterializationOutcomeTotal(hydrateStats, "hydrated"), uint64(0))
	reExportHydrate := cachemoneyDebugExport(ctx, t, hydrate.debugURL, "all")
	require.True(t, reExportHydrate.Completed)
	hydrate.stop(ctx, t)

	recompute := startCachemoneyDebugEngine(ctx, t, c, backend, "cachemoney-recompute-state-"+identity.NewID())
	importRecompute := cachemoneyDebugImport(ctx, t, recompute.debugURL, exportMetadataOnly.ExportID)
	require.True(t, importRecompute.Imported)
	require.Zero(t, importRecompute.BlobLocations)
	recomputedRandom := cachemoneyRandomExecFileContents(ctx, t, recompute.client, cacheBust)
	require.NotEqual(t, sourceRandom, recomputedRandom, "metadata-only remote cache hit should recompute when snapshot blobs are missing")
	recomputeStats := cachemoneyDebugStats(ctx, t, recompute.debugURL)
	require.Greater(t, cachemoneyMaterializationOutcomeTotal(recomputeStats, "recomputed_remote_miss"), uint64(0))
	require.Greater(t, recomputeStats.Cachemoney.RecomputeReasons["index_miss"], uint64(0))
	reExportRecompute := cachemoneyDebugExport(ctx, t, recompute.debugURL, "all")
	require.True(t, reExportRecompute.Completed)
	recompute.stop(ctx, t)
}

type cachemoneyDebugEngine struct {
	upstream       *dagger.Service
	runnerTunnel   *dagger.Service
	debugTunnel    *dagger.Service
	client         *dagger.Client
	runnerEndpoint string
	debugURL       string
}

type cachemoneyDebugServiceBinding struct {
	Hostname string
	Service  *dagger.Service
}

func startCachemoneyDebugEngine(ctx context.Context, t *testctx.T, c *dagger.Client, backend *dagger.Service, stateKey string, extraBindings ...cachemoneyDebugServiceBinding) *cachemoneyDebugEngine {
	t.Helper()

	engineWithPersistenceTestGC := engineWithConfig(
		ctx,
		t,
		engineConfigWithEnabled(true),
		engineConfigWithGC(
			"1000000000000000",
			"0",
			"1000000000000000",
			"0",
		),
	)
	engineWithDebugAddr := engineWithBkConfig(ctx, t, func(ctx context.Context, t *testctx.T, cfg bkconfig.Config) bkconfig.Config {
		t.Helper()
		cfg.GRPC.DebugAddress = "0.0.0.0:9090"
		return cfg
	})
	engineCtr := devEngineContainerWithStateKey(
		c,
		stateKey,
		engineWithPersistenceTestGC,
		engineWithDebugAddr,
		func(ctr *dagger.Container) *dagger.Container {
			ctr = ctr.
				WithServiceBinding("cachemoney-backend", backend)
			for _, binding := range extraBindings {
				ctr = ctr.WithServiceBinding(binding.Hostname, binding.Service)
			}
			return ctr.
				WithExposedPort(9090, dagger.ContainerWithExposedPortOpts{
					Protocol: dagger.NetworkProtocolTcp,
				})
		},
	)
	upstream := devEngineContainerAsService(engineCtr)
	runnerTunnel, err := c.Host().Tunnel(upstream, dagger.HostTunnelOpts{
		Ports: []dagger.PortForward{{Backend: 1234}},
	}).Start(ctx)
	require.NoError(t, err)
	debugTunnel, err := c.Host().Tunnel(upstream, dagger.HostTunnelOpts{
		Ports: []dagger.PortForward{{Backend: 9090}},
	}).Start(ctx)
	require.NoError(t, err)

	runnerEndpoint, err := runnerTunnel.Endpoint(ctx, dagger.ServiceEndpointOpts{Scheme: "tcp"})
	require.NoError(t, err)
	debugURL, err := debugTunnel.Endpoint(ctx, dagger.ServiceEndpointOpts{Scheme: "http"})
	require.NoError(t, err)
	engineClient, err := dagger.Connect(ctx,
		dagger.WithRunnerHost(runnerEndpoint),
		dagger.WithLogOutput(testutil.NewTWriter(t)),
	)
	require.NoError(t, err)

	engine := &cachemoneyDebugEngine{
		upstream:       upstream,
		runnerTunnel:   runnerTunnel,
		debugTunnel:    debugTunnel,
		client:         engineClient,
		runnerEndpoint: runnerEndpoint,
		debugURL:       strings.TrimRight(debugURL, "/"),
	}
	t.Cleanup(func() {
		engine.stop(ctx, t)
	})
	return engine
}

func (e *cachemoneyDebugEngine) stop(ctx context.Context, t *testctx.T) {
	t.Helper()
	if e.client != nil {
		require.NoError(t, e.client.Close())
		e.client = nil
	}
	if e.upstream != nil {
		_, err := e.upstream.Stop(ctx)
		require.NoError(t, err)
		e.upstream = nil
	}
	if e.debugTunnel != nil {
		_, err := e.debugTunnel.Stop(ctx, dagger.ServiceStopOpts{Kill: true})
		require.NoError(t, err)
		e.debugTunnel = nil
	}
	if e.runnerTunnel != nil {
		_, err := e.runnerTunnel.Stop(ctx, dagger.ServiceStopOpts{Kill: true})
		require.NoError(t, err)
		e.runnerTunnel = nil
	}
}

func startCachemoneyBackend(ctx context.Context, t *testctx.T, c *dagger.Client) *dagger.Service {
	t.Helper()
	backend, err := c.Container().
		From(alpineImage).
		WithExec([]string{"apk", "add", "--no-cache", "python3"}).
		WithNewFile("/cachemoney-backend.py", cachemoneyBackendScript).
		WithExposedPort(8080, dagger.ContainerWithExposedPortOpts{Protocol: dagger.NetworkProtocolTcp}).
		WithDefaultArgs([]string{"python3", "/cachemoney-backend.py"}).
		AsService().
		Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := backend.Stop(ctx)
		require.NoError(t, err)
	})
	_, err = c.Container().
		From(alpineImage).
		WithExec([]string{"apk", "add", "--no-cache", "curl"}).
		WithServiceBinding("cachemoney-backend", backend).
		WithExec([]string{"curl", "-fsS", "http://cachemoney-backend:8080/health"}).
		Sync(ctx)
	require.NoError(t, err)
	return backend
}

func startCachemoneyD0MinIO(ctx context.Context, t *testctx.T, c *dagger.Client) *dagger.Service {
	t.Helper()

	accessKey := cachemoneyD0Env("CACHEMONEY_D0_MINIO_ACCESS_KEY", "minioadmin")
	secretKey := cachemoneyD0Env("CACHEMONEY_D0_MINIO_SECRET_KEY", "minioadmin")
	minioImage := cachemoneyD0Env("CACHEMONEY_D0_MINIO_IMAGE", "minio/minio:latest")
	mcImage := cachemoneyD0Env("CACHEMONEY_D0_MC_IMAGE", "minio/mc:latest")
	bucket := cachemoneyD0Env("CACHEMONEY_D0_S3_BUCKET", "cachemoney-d0")

	minio, err := c.Container().
		From(minioImage).
		WithEnvVariable("MINIO_ROOT_USER", accessKey).
		WithEnvVariable("MINIO_ROOT_PASSWORD", secretKey).
		WithExposedPort(9000, dagger.ContainerWithExposedPortOpts{Protocol: dagger.NetworkProtocolTcp}).
		WithDefaultArgs([]string{"minio", "server", "/data", "--address", ":9000"}).
		AsService().
		Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := minio.Stop(ctx, dagger.ServiceStopOpts{Kill: true})
		require.NoError(t, err)
	})

	initScript := fmt.Sprintf(`set -eu
for i in $(seq 1 60); do
  if mc alias set local http://minio:9000 %[1]q %[2]q >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
mc alias set local http://minio:9000 %[1]q %[2]q
mc mb --ignore-existing local/%[3]q
mc ls local/%[3]q >/dev/null
`, accessKey, secretKey, bucket)
	_, err = c.Container().
		From(mcImage).
		WithEntrypoint([]string{}).
		WithEnvVariable("CACHEMONEY_D0_BUCKET_INIT", identity.NewID()).
		WithServiceBinding("minio", minio).
		WithExec([]string{"sh", "-ec", initScript}).
		Sync(ctx)
	require.NoError(t, err)
	return minio
}

func startCachemoneyD0HTTPSource(ctx context.Context, t *testctx.T, c *dagger.Client) *dagger.Service {
	t.Helper()

	httpSource, err := c.Container().
		From(alpineImage).
		WithExec([]string{"apk", "add", "--no-cache", "python3"}).
		WithNewFile("/cachemoney-d0-http.py", `from http.server import BaseHTTPRequestHandler, HTTPServer
import json

ETAG = '"cachemoney-d0-tool"'
BODY = b'cachemoney d0 tool body\n'
stats = {
    "ok": 0,
    "notModified": 0,
    "ifNoneMatch": 0,
}

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def do_GET(self):
        if self.path == "/_stats":
            body = json.dumps(stats, sort_keys=True).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return

        if self.path != "/tool.bin":
            self.send_response(404)
            self.end_headers()
            return

        if_none_match = self.headers.get("If-None-Match", "")
        if if_none_match:
            stats["ifNoneMatch"] += 1
        if if_none_match == ETAG:
            stats["notModified"] += 1
            self.send_response(304)
            self.send_header("ETag", ETAG)
            self.end_headers()
            return

        stats["ok"] += 1
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(len(BODY)))
        self.send_header("ETag", ETAG)
        self.end_headers()
        self.wfile.write(BODY)

HTTPServer(("", 8080), Handler).serve_forever()
`).
		WithExposedPort(8080, dagger.ContainerWithExposedPortOpts{Protocol: dagger.NetworkProtocolTcp}).
		WithDefaultArgs([]string{"python3", "/cachemoney-d0-http.py"}).
		AsService().
		Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := httpSource.Stop(ctx, dagger.ServiceStopOpts{Kill: true})
		require.NoError(t, err)
	})

	_, err = c.Container().
		From(alpineImage).
		WithExec([]string{"apk", "add", "--no-cache", "curl"}).
		WithServiceBinding("cachemoney-http", httpSource).
		WithExec([]string{"curl", "-fsS", "http://cachemoney-http:8080/_stats"}).
		Sync(ctx)
	require.NoError(t, err)
	return httpSource
}

type cachemoneyD0BackendSource struct {
	engineRoot  string
	backendRoot string
}

func cachemoneyD0LoadEnvFile(t *testctx.T) {
	t.Helper()

	env, err := godotenv.Read("/dagger.env")
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	for name, value := range env {
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		require.NoError(t, os.Setenv(name, value))
	}
}

func cachemoneyD0BackendSourceConfig(t *testctx.T) cachemoneyD0BackendSource {
	t.Helper()

	engineRoot := cachemoneyD0EngineRoot(t)
	backendRoot, ok := os.LookupEnv("CACHEMONEY_D0_BACKEND_SRC")
	if !ok || strings.TrimSpace(backendRoot) == "" {
		t.Skip("set CACHEMONEY_D0_BACKEND_SRC to a dagger.io checkout with the cachemoney dev server")
	}
	backendRoot, err := filepath.Abs(backendRoot)
	require.NoError(t, err)
	info, err := os.Stat(backendRoot)
	require.NoError(t, err, "CACHEMONEY_D0_BACKEND_SRC must point to a dagger.io checkout")
	require.True(t, info.IsDir(), "CACHEMONEY_D0_BACKEND_SRC must point to a dagger.io checkout directory")
	require.FileExists(t, filepath.Join(backendRoot, "api", "cmd", "cachemoney-dev-server", "main.go"))

	goWorkPath := filepath.Join(backendRoot, "go.work")
	goWork, err := os.ReadFile(goWorkPath)
	require.NoError(t, err, "D0 requires the dagger.io go.work that points at the local v2 engine checkout")
	require.Contains(t, string(goWork), engineRoot, "dagger.io go.work must point at the engine checkout running this test")

	return cachemoneyD0BackendSource{
		engineRoot:  engineRoot,
		backendRoot: backendRoot,
	}
}

func cachemoneyD0EngineRoot(t *testctx.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	wd, err = filepath.Abs(wd)
	require.NoError(t, err)
	for dir := wd; ; dir = filepath.Dir(dir) {
		mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(mod), "module github.com/dagger/dagger") {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "could not find github.com/dagger/dagger module root from %s", wd)
	}
}

func cachemoneyD0SourceExcludes() []string {
	return []string{
		".git",
		".git/**",
		"bin",
		"bin/**",
		"node_modules",
		"node_modules/**",
		"**/node_modules",
		"**/node_modules/**",
		".next",
		".next/**",
		"out",
		"out/**",
		"venv",
		"venv/**",
		".terraform",
		".terraform/**",
		"**/.terraform",
		"**/.terraform/**",
	}
}

func startCachemoneyD0Backend(ctx context.Context, t *testctx.T, c *dagger.Client, minio *dagger.Service, source cachemoneyD0BackendSource, uploadPolicy string) *dagger.Service {
	t.Helper()

	bucket := cachemoneyD0Env("CACHEMONEY_D0_S3_BUCKET", "cachemoney-d0")
	accessKey := cachemoneyD0Env("CACHEMONEY_D0_MINIO_ACCESS_KEY", "minioadmin")
	secretKey := cachemoneyD0Env("CACHEMONEY_D0_MINIO_SECRET_KEY", "minioadmin")
	backendBuildImage := cachemoneyD0Env("CACHEMONEY_D0_BACKEND_BUILD_IMAGE", "golang:1.26.1-bookworm")
	sourceOpts := dagger.HostDirectoryOpts{Exclude: cachemoneyD0SourceExcludes()}

	backendCtr := c.Container().
		From(backendBuildImage).
		WithMountedDirectory(source.backendRoot, c.Host().Directory(source.backendRoot, sourceOpts)).
		WithMountedDirectory(source.engineRoot, c.Host().Directory(source.engineRoot, sourceOpts)).
		WithMountedCache("/go/pkg/mod", c.CacheVolume("cachemoney-d0-backend-go-mod")).
		WithMountedCache("/root/.cache/go-build", c.CacheVolume("cachemoney-d0-backend-go-build")).
		WithWorkdir(filepath.Join(source.backendRoot, "api")).
		WithEnvVariable("GOTOOLCHAIN", "local").
		WithExec([]string{"go", "build", "-tags", "cachemoney", "-o", "/usr/local/bin/cachemoney-dev-server", "./cmd/cachemoney-dev-server"}).
		WithServiceBinding("minio", minio).
		WithEnvVariable("AWS_ACCESS_KEY_ID", accessKey).
		WithEnvVariable("AWS_SECRET_ACCESS_KEY", secretKey).
		WithEnvVariable("AWS_REGION", "us-east-1").
		WithEnvVariable("API_CACHEMONEY_STORAGE_BACKEND", "s3").
		WithEnvVariable("API_CACHEMONEY_S3_BUCKET", bucket).
		WithEnvVariable("API_CACHEMONEY_S3_REGION", "us-east-1").
		WithEnvVariable("API_CACHEMONEY_S3_ENDPOINT", "http://minio:9000").
		WithEnvVariable("API_CACHEMONEY_S3_USE_PATH_STYLE", "true").
		WithExposedPort(8080, dagger.ContainerWithExposedPortOpts{Protocol: dagger.NetworkProtocolTcp}).
		WithDefaultArgs([]string{"/usr/local/bin/cachemoney-dev-server", "-addr", ":8080"})
	if uploadPolicy != "" {
		backendCtr = backendCtr.WithEnvVariable("API_CACHEMONEY_UPLOAD_POLICY", uploadPolicy)
	}

	backend, err := backendCtr.
		AsService().
		Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := backend.Stop(ctx, dagger.ServiceStopOpts{Kill: true})
		require.NoError(t, err)
	})

	_, err = c.Container().
		From(alpineImage).
		WithExec([]string{"apk", "add", "--no-cache", "curl"}).
		WithServiceBinding("cachemoney-backend", backend).
		WithExec([]string{"curl", "-fsS", "http://cachemoney-backend:8080/health"}).
		Sync(ctx)
	require.NoError(t, err)
	return backend
}

type cachemoneyD0BackendDebugState struct {
	Summary struct {
		BlobCount          int `json:"blobCount"`
		SourceCount        int `json:"sourceCount"`
		PendingExportCount int `json:"pendingExportCount"`
	} `json:"summary"`
	Results []struct {
		ID            uint64 `json:"id"`
		Label         string `json:"label"`
		Detail        string `json:"detail"`
		SnapshotLinks []struct {
			RefKey string `json:"refKey"`
			Role   string `json:"role"`
		} `json:"snapshotLinks"`
	} `json:"results"`
	Snapshots []struct {
		ChainID    string `json:"chainId"`
		LayerCount int    `json:"layerCount"`
		Role       string `json:"role"`
	} `json:"snapshots"`
}

type cachemoneyD0HTTPStats struct {
	OK          int `json:"ok"`
	NotModified int `json:"notModified"`
	IfNoneMatch int `json:"ifNoneMatch"`
}

func cachemoneyD0FetchBackendDebugState(ctx context.Context, t *testctx.T, c *dagger.Client, backend *dagger.Service) cachemoneyD0BackendDebugState {
	t.Helper()

	body, err := c.Container().
		From(alpineImage).
		WithExec([]string{"apk", "add", "--no-cache", "curl"}).
		WithServiceBinding("cachemoney-backend", backend).
		WithExec([]string{"sh", "-ec", "curl -fsS http://cachemoney-backend:8080/cachemoney/debug/state >/tmp/cachemoney-debug-state.json"}).
		File("/tmp/cachemoney-debug-state.json").
		Contents(ctx)
	require.NoError(t, err)
	var state cachemoneyD0BackendDebugState
	require.NoError(t, json.Unmarshal([]byte(body), &state))
	return state
}

func cachemoneyD0FetchHTTPStats(ctx context.Context, t *testctx.T, c *dagger.Client, httpSource *dagger.Service) cachemoneyD0HTTPStats {
	t.Helper()

	body, err := c.Container().
		From(alpineImage).
		WithExec([]string{"apk", "add", "--no-cache", "curl"}).
		WithServiceBinding("cachemoney-http", httpSource).
		WithExec([]string{"sh", "-ec", "curl -fsS http://cachemoney-http:8080/_stats >/tmp/cachemoney-d0-http-stats.json"}).
		File("/tmp/cachemoney-d0-http-stats.json").
		Contents(ctx)
	require.NoError(t, err)
	var stats cachemoneyD0HTTPStats
	require.NoError(t, json.Unmarshal([]byte(body), &stats))
	return stats
}

func cachemoneyD0RequireEmptySnapshotSentinel(t *testctx.T, state cachemoneyD0BackendDebugState) {
	t.Helper()

	for _, snapshot := range state.Snapshots {
		if snapshot.ChainID != "cachemoney-empty-snapshot-chain-v1" {
			continue
		}
		require.Zero(t, snapshot.LayerCount, "empty snapshot sentinel must not carry layers")
		return
	}
	require.Failf(t, "missing empty snapshot sentinel", "state did not include cachemoney-empty-snapshot-chain-v1: %+v", state.Snapshots)
}

func cachemoneyD0RequireSnapshotRole(t *testctx.T, state cachemoneyD0BackendDebugState, role string) {
	t.Helper()

	for _, snapshot := range state.Snapshots {
		if snapshot.Role != role {
			continue
		}
		require.Greater(t, snapshot.LayerCount, 0, "snapshot role %q must carry layers", role)
		return
	}
	require.Failf(t, "missing snapshot role", "state did not include snapshot role %q: %+v", role, state.Snapshots)
}

func cachemoneyD0Env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type cachemoneyD0Workload struct {
	CacheBust      string
	HostDir        string
	ModuleDir      string
	CacheVolumeKey string
	GitRepoURL     string
}

type cachemoneyD0WorkloadOutput struct {
	Random                string
	Filesync              string
	SourceCache           string
	Git                   string
	MountedFile           string
	MountedDirectory      string
	MountedChangesetPatch string
	WithDirectory         string
	EmptyDir              string
}

func cachemoneyD0RunWorkload(ctx context.Context, t *testctx.T, c *dagger.Client, workload cachemoneyD0Workload) cachemoneyD0WorkloadOutput {
	t.Helper()
	return cachemoneyD0ReadWorkload(ctx, t, cachemoneyD0WorkloadContainersFor(c, workload))
}

func cachemoneyD0ModuleDir(t *testctx.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dagger.json"), []byte(`{
  "name": "cachemoneyremote",
  "sdk": "go",
  "source": ".",
  "codegen": {
    "automaticGitignore": false
  }
}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import (
	"context"

	"dagger/cachemoneyremote/internal/dagger"
)

type Cachemoneyremote struct{}

func (*Cachemoneyremote) ModuleRandom(ctx context.Context, cacheBust string) (*dagger.File, error) {
	name, err := dag.HTTP("http://cachemoney-http:8080/tool.bin").Name(ctx)
	if err != nil {
		return nil, err
	}
	return dag.Container().
		From("alpine:latest").
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithEnvVariable("HTTP_NAME", name).
		WithExec([]string{
			"sh",
			"-ec",
			`+"`"+`set -eu
mkdir -p /work
printf '%s\n' "$HTTP_NAME" > /work/http-name.txt
head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 > /work/module-random.txt`+"`"+`,
		}).
		File("/work/module-random.txt"), nil
}
`), 0o644))
	return dir
}

func cachemoneyD0DevelopModule(ctx context.Context, t *testctx.T, engine *cachemoneyDebugEngine, workload cachemoneyD0Workload) {
	t.Helper()

	cmd := hostDaggerCommand(ctx, t, workload.ModuleDir, "develop")
	cmd.Env = append(cmd.Env, "_EXPERIMENTAL_DAGGER_RUNNER_HOST="+engine.runnerEndpoint)
	cmd.Stderr = testutil.NewTWriter(t)
	output, err := cmd.Output()
	require.NoError(t, err, string(output))
}

func cachemoneyD0PrimeModuleRandom(ctx context.Context, t *testctx.T, engine *cachemoneyDebugEngine, workload cachemoneyD0Workload) {
	t.Helper()

	var response struct {
		ModuleRandom struct {
			Sync string `json:"sync"`
		} `json:"moduleRandom"`
	}
	cachemoneyD0RunModuleQuery(ctx, t, engine, workload, "sync", &response)
	require.NotEmpty(t, response.ModuleRandom.Sync)
}

func cachemoneyD0ReadModuleRandom(ctx context.Context, t *testctx.T, engine *cachemoneyDebugEngine, workload cachemoneyD0Workload) string {
	t.Helper()

	var response struct {
		ModuleRandom struct {
			Contents string `json:"contents"`
		} `json:"moduleRandom"`
	}
	cachemoneyD0RunModuleQuery(ctx, t, engine, workload, "contents", &response)
	random := strings.TrimSpace(response.ModuleRandom.Contents)
	require.NotEmpty(t, random)
	return random
}

func cachemoneyD0RunModuleQuery(ctx context.Context, t *testctx.T, engine *cachemoneyDebugEngine, workload cachemoneyD0Workload, selection string, out any) {
	t.Helper()

	query := fmt.Sprintf(`{
  moduleRandom(cacheBust: %q) {
    %s
  }
}
`, workload.CacheBust, selection)
	cmd := hostDaggerCommand(ctx, t, workload.ModuleDir, "query", "-m", ".")
	cmd.Env = append(cmd.Env, "_EXPERIMENTAL_DAGGER_RUNNER_HOST="+engine.runnerEndpoint)
	cmd.Stdin = strings.NewReader(query)
	cmd.Stderr = testutil.NewTWriter(t)
	output, err := cmd.Output()
	require.NoError(t, err, string(output))
	require.NoError(t, json.Unmarshal(output, out), string(output))
}

type cachemoneyD0WorkloadContainers struct {
	Random                 *dagger.Container
	Filesync               *dagger.Container
	SourceCache            *dagger.Container
	Git                    *dagger.Container
	MountedFile            *dagger.Container
	MountedDirectory       *dagger.Container
	MountedChangesetSource *dagger.Directory
	MountedChangesetBefore *dagger.Directory
	MountedChangesetAfter  *dagger.Directory
	MountedChangeset       *dagger.Changeset
	WithDirectory          *dagger.Container
	EmptyDir               *dagger.Directory
}

func cachemoneyD0WorkloadContainersFor(c *dagger.Client, workload cachemoneyD0Workload) cachemoneyD0WorkloadContainers {
	mountedChangesetSource, mountedChangesetBefore, mountedChangesetAfter, mountedChangeset := cachemoneyMountedDirectoryChangeset(c, workload.CacheBust)
	return cachemoneyD0WorkloadContainers{
		Random:                 cachemoneyRandomExecContainer(c, workload.CacheBust),
		Filesync:               cachemoneyFilesyncExecContainer(c, workload.HostDir, workload.CacheBust),
		SourceCache:            cachemoneySourceCacheExecContainer(c, workload.HostDir, workload.CacheVolumeKey, workload.CacheBust),
		Git:                    cachemoneyGitExecContainer(c, workload.GitRepoURL, workload.CacheBust),
		MountedFile:            cachemoneyMountedFileExecContainer(c, workload.CacheBust),
		MountedDirectory:       cachemoneyMountedDirectoryExecContainer(c, workload.CacheBust),
		MountedChangesetSource: mountedChangesetSource,
		MountedChangesetBefore: mountedChangesetBefore,
		MountedChangesetAfter:  mountedChangesetAfter,
		MountedChangeset:       mountedChangeset,
		WithDirectory:          cachemoneyWithDirectoryExecContainer(c, workload.CacheBust),
		EmptyDir:               c.Directory(),
	}
}

func cachemoneyD0PrimeWorkload(ctx context.Context, t *testctx.T, containers cachemoneyD0WorkloadContainers) {
	t.Helper()
	for _, ctr := range []*dagger.Container{
		containers.Random,
		containers.Filesync,
		containers.SourceCache,
		containers.Git,
		containers.MountedFile,
		containers.MountedDirectory,
		containers.WithDirectory,
	} {
		_, err := ctr.Sync(ctx)
		require.NoError(t, err)
	}
	_ = cachemoneyD0DirectoryDigest(ctx, t, containers.MountedChangesetSource)
	_ = cachemoneyD0DirectoryDigest(ctx, t, containers.MountedChangesetBefore)
	_ = cachemoneyD0DirectoryDigest(ctx, t, containers.MountedChangesetAfter)
	_ = cachemoneyD0ChangesetID(ctx, t, containers.MountedChangeset)
	_ = cachemoneyD0DirectoryID(ctx, t, containers.EmptyDir)
}

func cachemoneyD0ReadWorkload(ctx context.Context, t *testctx.T, containers cachemoneyD0WorkloadContainers) cachemoneyD0WorkloadOutput {
	t.Helper()
	return cachemoneyD0WorkloadOutput{
		Random:                cachemoneyD0ContainerFileContents(ctx, t, containers.Random, "/work/random.txt"),
		Filesync:              cachemoneyD0ContainerFileContents(ctx, t, containers.Filesync, "/work/filesync-random.txt"),
		SourceCache:           cachemoneyD0ContainerFileContents(ctx, t, containers.SourceCache, "/work/cache-random.txt"),
		Git:                   cachemoneyD0ContainerFileContents(ctx, t, containers.Git, "/work/git-random.txt"),
		MountedFile:           cachemoneyD0ContainerFileContents(ctx, t, containers.MountedFile, "/work/mounted-file-random.txt"),
		MountedDirectory:      cachemoneyD0ContainerFileContents(ctx, t, containers.MountedDirectory, "/work/mounted-directory-random.txt"),
		MountedChangesetPatch: cachemoneyD0ChangesetPatch(ctx, t, containers.MountedChangeset),
		WithDirectory:         cachemoneyD0ContainerFileContents(ctx, t, containers.WithDirectory, "/work/withdirectory-random.txt"),
		EmptyDir:              cachemoneyD0EmptyDirectoryMarker(ctx, t, containers.EmptyDir),
	}
}

func cachemoneyRandomExecContainer(c *dagger.Client, cacheBust string) *dagger.Container {
	return c.Container().
		From(alpineImage).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`mkdir -p /work
head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 > /work/random.txt`,
		})
}

func cachemoneyRandomExecFileContents(ctx context.Context, t *testctx.T, c *dagger.Client, cacheBust string) string {
	t.Helper()
	return cachemoneyD0ContainerFileContents(ctx, t, cachemoneyRandomExecContainer(c, cacheBust), "/work/random.txt")
}

func cachemoneyFilesyncExecContainer(c *dagger.Client, hostDir, cacheBust string) *dagger.Container {
	return c.Container().
		From(alpineImage).
		WithMountedDirectory("/input", c.Host().Directory(hostDir)).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
mkdir -p /work
cat /input/input.txt > /work/input.txt
head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 > /work/filesync-random.txt`,
		})
}

func cachemoneySourceCacheExecContainer(c *dagger.Client, hostDir, cacheVolumeKey, cacheBust string) *dagger.Container {
	source := c.Host().Directory(hostDir)
	return c.Container().
		From(alpineImage).
		WithMountedCache("/cache", c.CacheVolume(cacheVolumeKey, dagger.CacheVolumeOpts{Source: source})).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
mkdir -p /work
cat /cache/input.txt > /work/cache-input.txt
head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 > /work/cache-random.txt`,
		})
}

func cachemoneyGitExecContainer(c *dagger.Client, repoURL, cacheBust string) *dagger.Container {
	return c.Container().
		From(alpineImage).
		WithMountedDirectory("/repo", c.Git(repoURL).Branch("main").Tree()).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
mkdir -p /work
cat /repo/README.md > /work/git-readme.txt
head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 > /work/git-random.txt`,
		})
}

func cachemoneyMountedFileExecContainer(c *dagger.Client, cacheBust string) *dagger.Container {
	sourceFile := c.Container().
		From(alpineImage).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
mkdir -p /source
printf 'mounted-file:%s\n' "$CACHE_BUST" > /source/input.txt`,
		}).
		File("/source/input.txt")
	return c.Container().
		From(alpineImage).
		WithMountedFile("/input.txt", sourceFile).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
test "$(cat /input.txt)" = "mounted-file:$CACHE_BUST"
printf 'updated:%s\n' "$CACHE_BUST" >> /input.txt
mkdir -p /work
head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 > /work/mounted-file-random.txt`,
		})
}

func cachemoneyMountedDirectoryExecContainer(c *dagger.Client, cacheBust string) *dagger.Container {
	sourceDir := c.Container().
		From(alpineImage).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
mkdir -p /source
printf 'mounted-directory:%s\n' "$CACHE_BUST" > /source/input.txt`,
		}).
		Directory("/source")
	return c.Container().
		From(alpineImage).
		WithMountedDirectory("/input", sourceDir).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
test "$(cat /input/input.txt)" = "mounted-directory:$CACHE_BUST"
printf 'updated:%s\n' "$CACHE_BUST" > /input/updated.txt
mkdir -p /work
head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 > /work/mounted-directory-random.txt`,
		})
}

func cachemoneyMountedDirectoryChangeset(c *dagger.Client, cacheBust string) (*dagger.Directory, *dagger.Directory, *dagger.Directory, *dagger.Changeset) {
	sourceDir := c.Container().
		From(alpineImage).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
mkdir -p /source
printf 'mounted-changeset:%s\n' "$CACHE_BUST" > /source/input.txt
head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 > /source/random.txt`,
		}).
		Directory("/source")
	base := c.Container().
		From(alpineImage).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithWorkdir("/app").
		WithMountedDirectory(".", sourceDir)
	before := base.Directory(".")
	after := base.WithExec([]string{
		"sh",
		"-ec",
		`set -eu
test "$(cat input.txt)" = "mounted-changeset:$CACHE_BUST"
random="$(cat random.txt)"
test -n "$random"
printf 'mounted-changeset:%s\nrandom=%s\n' "$CACHE_BUST" "$random" > generated.txt
printf 'updated:%s\n' "$CACHE_BUST" >> input.txt`,
	}).Directory(".")
	return sourceDir, before, after, after.Changes(before)
}

func cachemoneyWithDirectoryExecContainer(c *dagger.Client, cacheBust string) *dagger.Container {
	base := c.Container().
		From(alpineImage).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
mkdir -p /base
printf 'base:%s\n' "$CACHE_BUST" > /base/base.txt
head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1 > /base/base-random.txt`,
		}).
		Directory("/base")
	overlay := c.Directory().WithNewFile("overlay.txt", "cachemoney d0 withDirectory overlay\n")
	combined := base.WithDirectory("overlay", overlay)
	return c.Container().
		From(alpineImage).
		WithMountedDirectory("/input", combined).
		WithEnvVariable("CACHE_BUST", cacheBust).
		WithExec([]string{
			"sh",
			"-ec",
			`set -eu
test "$(cat /input/base.txt)" = "base:$CACHE_BUST"
base_random="$(cat /input/base-random.txt)"
test -n "$base_random"
test "$(cat /input/overlay/overlay.txt)" = "cachemoney d0 withDirectory overlay"
mkdir -p /work
{
  printf 'base=%s\n' "$base_random"
  printf 'exec='
  head -c 32 /dev/urandom | sha256sum | cut -d' ' -f1
} > /work/withdirectory-random.txt`,
		})
}

func cachemoneyD0ContainerFileContents(ctx context.Context, t *testctx.T, ctr *dagger.Container, path string) string {
	t.Helper()
	contents, err := ctr.File(path).Contents(ctx)
	require.NoError(t, err)
	random := strings.TrimSpace(contents)
	require.NotEmpty(t, random)
	return random
}

func cachemoneyD0DirectoryID(ctx context.Context, t *testctx.T, dir *dagger.Directory) string {
	t.Helper()
	id, err := dir.ID(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	return string(id)
}

func cachemoneyD0DirectoryDigest(ctx context.Context, t *testctx.T, dir *dagger.Directory) string {
	t.Helper()
	digest, err := dir.Digest(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, digest)
	return digest
}

func cachemoneyD0ChangesetID(ctx context.Context, t *testctx.T, changes *dagger.Changeset) string {
	t.Helper()
	id, err := changes.ID(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	return string(id)
}

func cachemoneyD0ChangesetPatch(ctx context.Context, t *testctx.T, changes *dagger.Changeset) string {
	t.Helper()
	contents, err := changes.AsPatch().Contents(ctx)
	require.NoError(t, err)
	require.Contains(t, contents, "generated.txt")
	require.Contains(t, contents, "random=")
	require.Contains(t, contents, "updated:")
	return contents
}

func cachemoneyD0EmptyDirectoryMarker(ctx context.Context, t *testctx.T, dir *dagger.Directory) string {
	t.Helper()
	entries, err := dir.Entries(ctx)
	require.NoError(t, err)
	require.Empty(t, entries)
	return "empty"
}

type cachemoneyDebugExportHTTPResult struct {
	ExportID       string `json:"export_id"`
	Snapshots      int    `json:"snapshots"`
	BlobsOffered   int    `json:"blobs_offered"`
	BlobsRequested int    `json:"blobs_requested"`
	BlobsUploaded  int    `json:"blobs_uploaded"`
	BlobsFailed    int    `json:"blobs_failed"`
	BlobsSkipped   int    `json:"blobs_skipped"`
	Completed      bool   `json:"completed"`
}

type cachemoneyDebugImportHTTPResult struct {
	SourceID      string `json:"source_id"`
	BlobLocations int    `json:"blob_locations"`
	Imported      bool   `json:"imported"`
}

type cachemoneyDebugCacheSnapshot struct {
	Cachemoney struct {
		MaterializationOutcomesByRole map[string]map[string]uint64 `json:"materialization_outcomes_by_role"`
		RecomputeReasons              map[string]uint64            `json:"recompute_reasons"`
		HydrationFailures             map[string]uint64            `json:"hydration_failures"`
	} `json:"cachemoney"`
}

func cachemoneyDebugExport(ctx context.Context, t *testctx.T, debugURL, mode string) cachemoneyDebugExportHTTPResult {
	t.Helper()
	beginURL := "http://cachemoney-backend:8080/begin?mode=" + url.QueryEscape(mode)
	return cachemoneyDebugExportToURL(ctx, t, debugURL, beginURL)
}

func cachemoneyDebugExportToURL(ctx context.Context, t *testctx.T, debugURL, beginURL string) cachemoneyDebugExportHTTPResult {
	t.Helper()
	var result cachemoneyDebugExportHTTPResult
	cachemoneyDebugPostJSON(ctx, t, debugURL+"/debug/dagql/cache/export?url="+url.QueryEscape(beginURL), &result)
	return result
}

func cachemoneyDebugImport(ctx context.Context, t *testctx.T, debugURL, exportID string) cachemoneyDebugImportHTTPResult {
	t.Helper()
	importURL := "http://cachemoney-backend:8080/import/" + url.PathEscape(exportID)
	return cachemoneyDebugImportFromURL(ctx, t, debugURL, importURL)
}

func cachemoneyDebugImportFromURL(ctx context.Context, t *testctx.T, debugURL, importURL string) cachemoneyDebugImportHTTPResult {
	t.Helper()
	var result cachemoneyDebugImportHTTPResult
	cachemoneyDebugPostJSON(ctx, t, debugURL+"/debug/dagql/cache/import?url="+url.QueryEscape(importURL), &result)
	return result
}

func cachemoneyDebugStats(ctx context.Context, t *testctx.T, debugURL string) cachemoneyDebugCacheSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, cachemoneyDebugHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, debugURL+"/debug/dagql/cache", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.GreaterOrEqual(t, resp.StatusCode, http.StatusOK, string(body))
	require.Less(t, resp.StatusCode, http.StatusMultipleChoices, string(body))
	var out cachemoneyDebugCacheSnapshot
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

func cachemoneyDebugPostJSON(ctx context.Context, t *testctx.T, url string, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, cachemoneyDebugHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.GreaterOrEqual(t, resp.StatusCode, http.StatusOK, string(body))
	require.Less(t, resp.StatusCode, http.StatusMultipleChoices, string(body))
	require.NoError(t, json.Unmarshal(body, out))
}

func cachemoneyMaterializationOutcomeTotal(stats cachemoneyDebugCacheSnapshot, outcome string) uint64 {
	var total uint64
	for _, byOutcome := range stats.Cachemoney.MaterializationOutcomesByRole {
		total += byOutcome[outcome]
	}
	return total
}

func cachemoneyStatsTotal(values map[string]uint64) uint64 {
	var total uint64
	for _, value := range values {
		total += value
	}
	return total
}

const cachemoneyBackendScript = `
import json
from email.parser import BytesParser
from email.policy import default
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, quote, unquote, urlparse

BASE_URL = "http://cachemoney-backend:8080"
exports = {}
blobs = {}
next_export_id = 1

def write_json(handler, obj):
    body = json.dumps(obj).encode()
    handler.send_response(200)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(body)))
    handler.end_headers()
    handler.wfile.write(body)

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path == "/health":
            write_json(self, {"ok": True})
            return
        if parsed.path.startswith("/blob/"):
            digest = unquote(parsed.path[len("/blob/"):])
            data = blobs.get(digest)
            if data is None:
                self.send_error(404, "missing blob")
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return
        if parsed.path.startswith("/import/"):
            export_id = unquote(parsed.path[len("/import/"):])
            exp = exports.get(export_id)
            if exp is None:
                self.send_error(404, "missing export")
                return
            blob_index = {}
            for digest in exp["uploaded"]:
                meta = exp["blob_meta"].get(digest, {})
                blob_index[digest] = {
                    "url": BASE_URL + "/blob/" + quote(digest, safe=""),
                    "size": len(blobs.get(digest, b"")),
                    "mediaType": meta.get("mediaType", ""),
                }
            manifest = {
                "version": 2,
                "metadataSourceID": export_id,
                "blobIndex": blob_index,
            }
            boundary = "cachemoneyboundary"
            self.send_response(200)
            self.send_header("Content-Type", "multipart/form-data; boundary=" + boundary)
            self.end_headers()
            self.write_part(boundary, "manifest", "cachemoney-v2.json", "application/json", json.dumps(manifest).encode())
            self.write_part(boundary, "metadata", "dagql-cache.db", "application/octet-stream", exp["metadata"])
            self.wfile.write(("--" + boundary + "--\r\n").encode())
            return
        self.send_error(404)

    def do_POST(self):
        global next_export_id
        parsed = urlparse(self.path)
        if parsed.path == "/begin":
            parts = self.read_multipart_parts()
            manifest = json.loads(parts["manifest"])
            metadata = parts["metadata"]
            export_id = "export-%d" % next_export_id
            next_export_id += 1

            requested = []
            blob_meta = {}
            mode = parse_qs(parsed.query).get("mode", ["all"])[0]
            for chain in manifest.get("chains", []):
                for layer in chain.get("layers", []):
                    digest = layer.get("blobDigest", "")
                    if not digest:
                        continue
                    blob_meta[digest] = {
                        "size": layer.get("size", 0),
                        "mediaType": layer.get("mediaType", ""),
                    }
                    if mode != "metadata-only":
                        requested.append(digest)

            exports[export_id] = {
                "metadata": metadata,
                "manifest": manifest,
                "blob_meta": blob_meta,
                "uploaded": set(),
            }
            write_json(self, {
                "version": 2,
                "exportID": export_id,
                "requestedBlobs": requested,
                "uploadURL": BASE_URL + "/upload/" + quote(export_id, safe=""),
                "completeURL": BASE_URL + "/complete/" + quote(export_id, safe=""),
            })
            return
        if parsed.path.startswith("/upload/"):
            export_id = unquote(parsed.path[len("/upload/"):])
            req = json.loads(self.read_request_body())
            digest = req["blobDigest"]
            exports[export_id]["blob_meta"][digest] = {
                "size": req.get("size", 0),
                "mediaType": req.get("mediaType", ""),
            }
            write_json(self, {
                "method": "PUT",
                "url": BASE_URL + "/blob/" + quote(digest, safe=""),
            })
            return
        if parsed.path.startswith("/complete/"):
            export_id = unquote(parsed.path[len("/complete/"):])
            req = json.loads(self.read_request_body())
            for digest in req.get("blobs", []):
                exports[export_id]["uploaded"].add(digest)
            write_json(self, {
                "version": 2,
                "exportID": export_id,
            })
            return
        self.send_error(404)

    def do_PUT(self):
        parsed = urlparse(self.path)
        if parsed.path.startswith("/blob/"):
            digest = unquote(parsed.path[len("/blob/"):])
            blobs[digest] = self.read_request_body()
            self.send_response(204)
            self.end_headers()
            return
        self.send_error(404)

    def read_request_body(self):
        if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            chunks = []
            while True:
                line = self.rfile.readline()
                if not line:
                    break
                size = int(line.split(b";", 1)[0], 16)
                if size == 0:
                    while True:
                        trailer = self.rfile.readline()
                        if trailer in (b"\r\n", b"\n", b""):
                            break
                    break
                chunks.append(self.rfile.read(size))
                self.rfile.read(2)
            return b"".join(chunks)
        length = int(self.headers.get("Content-Length", "0"))
        return self.rfile.read(length)

    def read_multipart_parts(self):
        content_type = self.headers.get("Content-Type", "")
        body = self.read_request_body()
        raw = ("Content-Type: " + content_type + "\r\nMIME-Version: 1.0\r\n\r\n").encode() + body
        message = BytesParser(policy=default).parsebytes(raw)
        parts = {}
        for part in message.iter_parts():
            name = part.get_param("name", header="content-disposition")
            if name:
                parts[name] = part.get_payload(decode=True)
        return parts

    def write_part(self, boundary, name, filename, content_type, data):
        self.wfile.write(("--" + boundary + "\r\n").encode())
        self.wfile.write(('Content-Disposition: form-data; name="%s"; filename="%s"\r\n' % (name, filename)).encode())
        self.wfile.write(("Content-Type: " + content_type + "\r\n\r\n").encode())
        self.wfile.write(data)
        self.wfile.write(b"\r\n")

ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
`
