package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dagger.io/dagger"
	bkconfig "github.com/dagger/dagger/internal/buildkit/cmd/buildkitd/config"
	"github.com/dagger/dagger/internal/buildkit/identity"
	"github.com/dagger/dagger/internal/testutil"
	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"
)

const cachemoneyDebugHTTPTimeout = 2 * time.Minute

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
	upstream     *dagger.Service
	runnerTunnel *dagger.Service
	debugTunnel  *dagger.Service
	client       *dagger.Client
	debugURL     string
}

func startCachemoneyDebugEngine(ctx context.Context, t *testctx.T, c *dagger.Client, backend *dagger.Service, stateKey string) *cachemoneyDebugEngine {
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
			return ctr.
				WithServiceBinding("cachemoney-backend", backend).
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
		upstream:     upstream,
		runnerTunnel: runnerTunnel,
		debugTunnel:  debugTunnel,
		client:       engineClient,
		debugURL:     strings.TrimRight(debugURL, "/"),
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
	contents, err := cachemoneyRandomExecContainer(c, cacheBust).
		File("/work/random.txt").
		Contents(ctx)
	require.NoError(t, err)
	random := strings.TrimSpace(contents)
	require.NotEmpty(t, random)
	return random
}

type cachemoneyDebugExportHTTPResult struct {
	ExportID       string `json:"export_id"`
	Snapshots      int    `json:"snapshots"`
	BlobsOffered   int    `json:"blobs_offered"`
	BlobsRequested int    `json:"blobs_requested"`
	BlobsUploaded  int    `json:"blobs_uploaded"`
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
	} `json:"cachemoney"`
}

func cachemoneyDebugExport(ctx context.Context, t *testctx.T, debugURL, mode string) cachemoneyDebugExportHTTPResult {
	t.Helper()
	beginURL := "http://cachemoney-backend:8080/begin?mode=" + url.QueryEscape(mode)
	var result cachemoneyDebugExportHTTPResult
	cachemoneyDebugPostJSON(ctx, t, debugURL+"/debug/dagql/cache/export?url="+url.QueryEscape(beginURL), &result)
	return result
}

func cachemoneyDebugImport(ctx context.Context, t *testctx.T, debugURL, exportID string) cachemoneyDebugImportHTTPResult {
	t.Helper()
	importURL := "http://cachemoney-backend:8080/import/" + url.PathEscape(exportID)
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
