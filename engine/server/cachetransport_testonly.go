//go:build testonly_cache_transport

package server

import (
	"context"
	"fmt"
	"os"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	digest "github.com/opencontainers/go-digest"
	"github.com/sirupsen/logrus"
)

// The test-only file transport for cache bundles: cross-engine integration
// tests move a bundle + chain blobs over a shared volume, standing in for
// the chunk-C service client. Compiled ONLY into the engine-dev test build
// (the testonly_cache_transport tag, set by toolchains/engine-dev's
// testContainer) — release and ordinary dev binaries carry none of it —
// and still env-gated at runtime as defense in depth.

// testOnlyCacheTransportBoot is the file-transport half of the bundle
// inflow: env-gated bundle import after local restore (inside the
// pre-serving boot window) plus a directory-CAS blob source for
// content-chain realization. Failures degrade like any bundle inflow —
// logged, counted at the dagql layer, never a boot failure.
func (srv *Server) testOnlyCacheTransportBoot(ctx context.Context) {
	if os.Getenv("_DAGGER_TESTONLY_CACHE_TRANSPORT") != "1" {
		return
	}
	if casDir := os.Getenv("_DAGGER_TESTONLY_CHAIN_CAS_DIR"); casDir != "" {
		srv.engineCache.SetContentChainBlobSource(bkcache.DirectoryCAS{Root: casDir})
	}
	bundlePath := os.Getenv("_DAGGER_TESTONLY_IMPORT_CACHE_BUNDLE")
	if bundlePath == "" {
		return
	}
	f, err := os.Open(bundlePath)
	if err != nil {
		logrus.WithError(err).Warn("test-only cache bundle import: open bundle")
		return
	}
	defer f.Close()
	summary, err := srv.engineCache.ImportBundle(ctx, f)
	if err != nil {
		logrus.WithError(err).Warn("test-only cache bundle import: bundle skipped")
		return
	}
	logrus.WithField("summary", fmt.Sprintf("%+v", summary)).Info("test-only cache bundle imported")
}

// TestOnlyExportCacheBundle writes the retained cache as a bundle file and
// copies its chain blobs into a directory CAS. Reachable only through the
// equally tag-gated debug endpoint.
func (srv *Server) TestOnlyExportCacheBundle(ctx context.Context, bundlePath, casDir string) (rerr error) {
	if srv.engineCache == nil {
		return fmt.Errorf("dagql cache not available")
	}
	f, err := os.Create(bundlePath)
	if err != nil {
		return fmt.Errorf("create bundle file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && rerr == nil {
			rerr = cerr
		}
	}()
	summary, err := srv.engineCache.ExportBundle(ctx, f, dagql.CacheBundleExportOptions{
		EngineVersion: engine.Version,
	})
	if err != nil {
		return err
	}
	if casDir == "" {
		return nil
	}
	cas := bkcache.DirectoryCAS{Root: casDir}
	for _, raw := range summary.BlobIndex {
		dgst := digest.Digest(raw)
		rc, err := srv.workerCache.OpenBlob(ctx, dgst)
		if err != nil {
			return fmt.Errorf("open chain blob %s: %w", dgst, err)
		}
		putErr := cas.PutBlob(ctx, dgst, rc)
		if cerr := rc.Close(); putErr == nil {
			putErr = cerr
		}
		if putErr != nil {
			return fmt.Errorf("copy chain blob %s: %w", dgst, putErr)
		}
	}
	return nil
}
