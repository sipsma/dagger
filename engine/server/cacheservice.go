package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine"
	cacheservice "github.com/dagger/dagger/engine/cacheservice"
	"github.com/dagger/dagger/engine/config"
	"github.com/dagger/dagger/engine/slog"
	digest "github.com/opencontainers/go-digest"
)

// The engine side of the remote cache service (service design §7/§8):
// bundle import inside the boot window, the export pipeline behind the
// operator endpoint and the graceful-shutdown hook, and the content-chain
// blob source for serving-time fetches. Everything here is an accelerator:
// no failure on any of these paths may fail a boot, a build, or a shutdown
// (S4) — failures degrade, loudly counted.

// initCacheServiceClient resolves configuration and builds the client.
// Misconfiguration disables the feature with a loud log line — never a
// boot failure.
func (srv *Server) initCacheServiceClient(cfg *config.Config) {
	var section *config.CacheServiceConfig
	if cfg != nil {
		section = cfg.CacheService
	}
	settings, enabled, err := cacheservice.ResolveSettings(section)
	if err != nil {
		slog.Warn("cache service configuration invalid; remote cache disabled", "error", err)
		return
	}
	if !enabled {
		return
	}
	client, err := cacheservice.NewClientFromSettings(settings)
	if err != nil {
		slog.Warn("cache service client construction failed; remote cache disabled", "error", err)
		return
	}
	srv.cacheServiceSettings = settings
	srv.cacheServiceClient = client
	slog.Info("cache service configured",
		"url", settings.URL, "scope", settings.Scope,
		"importBudget", settings.ImportBudget, "exportOnShutdown", settings.ExportOnShutdown,
		"metadataOnly", settings.MetadataOnly)
}

// cacheServiceBootImport is the §8 boot inflow: selection, download, and
// per-bundle merge, inside the pre-serving boot window, bounded by the
// import budget with monotone degradation — fewer bundles, then none, never
// a boot failure. The outcome lands as the bundle boot summary (stats file
// + debug snapshots).
func (srv *Server) cacheServiceBootImport(ctx context.Context) {
	if srv.cacheServiceClient == nil || srv.engineCache == nil {
		return
	}

	// The chain blob source serves this whole boot: freshly imported rows
	// and chain-only rows restored from local persistence both realize
	// through it (T-S13's shape).
	srv.engineCache.SetContentChainBlobSource(srv.cacheServiceClient.BlobSource())

	summary := &dagql.CacheBundleBootSummary{SkippedByReason: map[string]int{}}
	defer func() {
		srv.engineCache.SetBundleBootSummary(summary)
		slog.Info("cache service boot import finished",
			"offered", summary.BundlesOffered, "fetched", summary.BundlesFetched,
			"merged", summary.BundlesMerged, "skipped", summary.SkippedByReason,
			"rowsImported", summary.RowsImported, "rowsDedupedByOrigin", summary.RowsDedupedByOrigin,
			"budgetExhausted", summary.ImportBudgetExhausted)
	}()

	ctx, cancel := context.WithTimeout(ctx, srv.cacheServiceSettings.ImportBudget)
	defer cancel()

	bundles, err := srv.cacheServiceClient.SelectBundles(ctx,
		srv.engineCache.PersistenceSchemaVersion(), dagql.CacheBundleFormatVersion,
		srv.cacheServiceSettings.ImportLimit)
	if err != nil {
		summary.ImportBudgetExhausted = ctx.Err() != nil
		summary.SkippedByReason["selection_failed"]++
		slog.Warn("cache service bundle selection failed; starting cold", "error", err)
		return
	}
	summary.BundlesOffered = len(bundles)

	// Bundles merge oldest→newest (stable tie-break on bundle ID) so
	// metadata refreshes deterministically favor the newest observation;
	// row existence is order-independent by origin dedup (§8.1).
	slices.SortFunc(bundles, func(a, b cacheservice.BundleSummary) int {
		if cmp := a.CreatedAt.Compare(b.CreatedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.BundleID, b.BundleID)
	})

	for _, bundle := range bundles {
		if ctx.Err() != nil {
			summary.ImportBudgetExhausted = true
			break
		}
		rc, err := srv.cacheServiceClient.DownloadBundle(ctx, bundle.DownloadURL)
		if err != nil {
			if ctx.Err() != nil {
				summary.ImportBudgetExhausted = true
				break
			}
			summary.SkippedByReason["download_failed"]++
			slog.Warn("cache service bundle download failed", "bundle", bundle.BundleID, "error", err)
			continue
		}
		summary.BundlesFetched++
		importSummary, err := srv.engineCache.ImportBundle(ctx, rc)
		rc.Close()
		if err != nil {
			if ctx.Err() != nil {
				summary.ImportBudgetExhausted = true
				break
			}
			if skip, ok := errors.AsType[*dagql.CacheBundleSkipError](err); ok {
				summary.SkippedByReason[skip.Reason]++
			} else {
				summary.SkippedByReason["import_error"]++
			}
			slog.Warn("cache service bundle import skipped", "bundle", bundle.BundleID, "error", err)
			continue
		}
		summary.BundlesMerged++
		summary.RowsImported += importSummary.RowsImported
		summary.RowsDedupedByOrigin += importSummary.RowsDedupedByOrigin
	}
}

// CacheServiceExportSummary is the export outcome (§11's export summary):
// returned to the admin caller, logged at shutdown, and consumed by the
// growth gates.
type CacheServiceExportSummary struct {
	BundleID string `json:"bundle_id"`

	Rows      int `json:"rows"`
	Roots     int `json:"roots"`
	EqClasses int `json:"eq_classes"`
	Terms     int `json:"terms"`
	Chains    int `json:"chains"`

	// Per-row export degradations (loud, never failures — §7 D4 step 2).
	RowsExcludedNoPortableContent int `json:"rows_excluded_no_portable_content,omitempty"`
	RowsChainComputeFailed        int `json:"rows_chain_compute_failed,omitempty"`
	RowsExcludedEncodeFailed      int `json:"rows_excluded_encode_failed,omitempty"`

	BlobsOffered        int   `json:"blobs_offered"`
	BlobsUploaded       int   `json:"blobs_uploaded"`
	BlobsAlreadyPresent int   `json:"blobs_already_present"`
	BlobsSkipped        int   `json:"blobs_skipped"`
	BytesUploaded       int64 `json:"bytes_uploaded"`

	MetadataOnly bool `json:"metadata_only,omitempty"`

	PhaseDurationsMS map[string]int64 `json:"phase_durations_ms"`
}

type cacheExportFlight struct {
	done    chan struct{}
	summary *CacheServiceExportSummary
	err     error
}

// CacheServiceExport runs one export through the §7 D4 pipeline. Concurrent
// calls coalesce onto the in-flight export — one export at a time per
// engine; joiners receive the in-flight run's summary.
func (srv *Server) CacheServiceExport(ctx context.Context, metadataOnly bool) (*CacheServiceExportSummary, error) {
	if srv.cacheServiceClient == nil {
		return nil, errors.New("cache service not configured")
	}
	if srv.engineCache == nil {
		return nil, errors.New("dagql cache not available")
	}

	srv.cacheExportMu.Lock()
	if flight := srv.cacheExportFlight; flight != nil {
		srv.cacheExportMu.Unlock()
		select {
		case <-flight.done:
			return flight.summary, flight.err
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
	flight := &cacheExportFlight{done: make(chan struct{})}
	srv.cacheExportFlight = flight
	srv.cacheExportMu.Unlock()

	summary, err := srv.runCacheServiceExport(ctx, metadataOnly)
	flight.summary, flight.err = summary, err
	srv.cacheExportMu.Lock()
	srv.cacheExportFlight = nil
	srv.cacheExportMu.Unlock()
	close(flight.done)
	return summary, err
}

func (srv *Server) runCacheServiceExport(ctx context.Context, metadataOnly bool) (rsummary *CacheServiceExportSummary, rerr error) {
	summary := &CacheServiceExportSummary{
		MetadataOnly:     metadataOnly,
		PhaseDurationsMS: map[string]int64{},
	}
	phase := func(name string) func() {
		start := time.Now()
		return func() { summary.PhaseDurationsMS[name] = time.Since(start).Milliseconds() }
	}

	// SNAPSHOT + ENCODE: the bundle lands in a scratch file so publish can
	// stream it with a known manifest.
	encodeDone := phase("encode")
	tmp, err := os.CreateTemp("", "dagger-cache-export-*.tar.zst")
	if err != nil {
		return summary, fmt.Errorf("cache export: scratch file: %w", err)
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	exportSummary, err := srv.engineCache.ExportBundle(ctx, tmp, dagql.CacheBundleExportOptions{
		Scope:         srv.cacheServiceSettings.Scope,
		EngineVersion: engine.Version,
		MetadataOnly:  metadataOnly,
	})
	if err != nil {
		return summary, fmt.Errorf("cache export: encode bundle: %w", err)
	}
	summary.Rows = exportSummary.Results
	summary.Roots = exportSummary.Roots
	summary.EqClasses = exportSummary.EqClasses
	summary.Terms = exportSummary.Terms
	summary.Chains = exportSummary.Chains
	summary.RowsExcludedNoPortableContent = exportSummary.ExcludedNoPortableContent
	summary.RowsChainComputeFailed = exportSummary.ChainComputeFailed
	summary.RowsExcludedEncodeFailed = exportSummary.ExcludedEncodeFailed
	summary.BlobsOffered = len(exportSummary.BlobIndex)
	encodeDone()

	// The manifest part must be byte-identical with the archive's copy; the
	// blob size/mediaType index rides in it for the upload phase.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return summary, fmt.Errorf("cache export: rewind bundle: %w", err)
	}
	manifest, manifestJSON, err := dagql.ReadCacheBundleManifest(tmp)
	if err != nil {
		return summary, fmt.Errorf("cache export: read back manifest: %w", err)
	}
	type blobInfo struct {
		size      int64
		mediaType string
	}
	blobInfos := make(map[string]blobInfo)
	for _, chain := range manifest.Chains {
		for _, layer := range chain.Layers {
			blobInfos[layer.Blob] = blobInfo{size: layer.Size, mediaType: layer.MediaType}
		}
	}

	// PUBLISH METADATA (atomic): from here the export has durable value;
	// everything after is enrichment (S6).
	publishDone := phase("publish")
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return summary, fmt.Errorf("cache export: rewind bundle: %w", err)
	}
	bundleID, err := srv.cacheServiceClient.PublishBundle(ctx, manifestJSON, tmp)
	if err != nil {
		return summary, fmt.Errorf("cache export: publish bundle: %w", err)
	}
	summary.BundleID = bundleID
	publishDone()

	// BLOB DELTA: existence-checked against the CAS; only the missing
	// subset moves. Per-blob failures retry once, then skip — a skipped
	// blob is sparseness, not failure (S5).
	uploadStart := time.Now()
	statDone := phase("stat")
	var missing []string
	if len(exportSummary.BlobIndex) > 0 {
		missing, err = srv.cacheServiceClient.StatMissingBlobs(ctx, exportSummary.BlobIndex)
		if err != nil {
			// The bundle is already published; blob enrichment failing whole
			// is sparseness with a tally, not an export failure.
			slog.Warn("cache export: blob stat failed; bundle stays sparse", "bundle", bundleID, "error", err)
			summary.BlobsSkipped = len(exportSummary.BlobIndex)
			missing = nil
		}
	}
	statDone()
	summary.BlobsAlreadyPresent = summary.BlobsOffered - len(missing) - summary.BlobsSkipped

	uploadDone := phase("upload")
	attempted := 0
	for _, rawDigest := range missing {
		if ctx.Err() != nil {
			break
		}
		attempted++
		info := blobInfos[rawDigest]
		uploaded, alreadyPresent := false, false
		var lastErr error
		for retry := 0; retry < 2 && ctx.Err() == nil; retry++ {
			uploaded, alreadyPresent, lastErr = srv.uploadOneBlob(ctx, rawDigest, info.size, info.mediaType)
			if lastErr == nil {
				break
			}
		}
		switch {
		case lastErr != nil:
			summary.BlobsSkipped++
			slog.Warn("cache export: blob upload skipped", "bundle", bundleID, "blob", rawDigest, "error", lastErr)
		case alreadyPresent:
			summary.BlobsAlreadyPresent++
		case uploaded:
			summary.BlobsUploaded++
			summary.BytesUploaded += info.size
		}
	}
	// Budget exhausted mid-delta: everything unattempted is sparseness the
	// next export completes (S6).
	summary.BlobsSkipped += len(missing) - attempted
	uploadDone()

	// COMPLETE: the tally the service whitelists into its event log.
	completeDone := phase("complete")
	err = srv.cacheServiceClient.CompleteBundle(ctx, bundleID, &cacheservice.BundleUploadTally{
		Uploaded:       int64(summary.BlobsUploaded),
		AlreadyPresent: int64(summary.BlobsAlreadyPresent),
		Skipped:        int64(summary.BlobsSkipped),
		Bytes:          summary.BytesUploaded,
		DurationMS:     time.Since(uploadStart).Milliseconds(),
	})
	if err != nil {
		// The bundle stays pending server-side: selectable, just ranked
		// behind complete siblings. Loud, not fatal.
		slog.Warn("cache export: bundle completion failed; bundle stays pending", "bundle", bundleID, "error", err)
	}
	completeDone()

	slog.Info("cache service export finished",
		"bundle", bundleID, "rows", summary.Rows, "chains", summary.Chains,
		"blobsOffered", summary.BlobsOffered, "uploaded", summary.BlobsUploaded,
		"alreadyPresent", summary.BlobsAlreadyPresent, "skipped", summary.BlobsSkipped,
		"bytes", summary.BytesUploaded, "metadataOnly", metadataOnly,
		"phasesMS", summary.PhaseDurationsMS)
	return summary, nil
}

// uploadOneBlob moves one chain blob from the local content store into the
// CAS: prepare (may short-circuit on existence), streamed PUT, completion
// verification.
func (srv *Server) uploadOneBlob(ctx context.Context, rawDigest string, size int64, mediaType string) (uploaded, alreadyPresent bool, rerr error) {
	dgst, err := digest.Parse(rawDigest)
	if err != nil {
		return false, false, fmt.Errorf("invalid blob digest %q: %w", rawDigest, err)
	}
	prep, err := srv.cacheServiceClient.PrepareBlobUpload(ctx, rawDigest, size, mediaType)
	if err != nil {
		return false, false, fmt.Errorf("prepare upload: %w", err)
	}
	if prep.AlreadyExists {
		return false, true, nil
	}
	rc, err := srv.workerCache.OpenBlob(ctx, dgst)
	if err != nil {
		return false, false, fmt.Errorf("open local blob: %w", err)
	}
	putErr := srv.cacheServiceClient.PutBlob(ctx, prep, rc, size)
	if cerr := rc.Close(); putErr == nil {
		putErr = cerr
	}
	if putErr != nil {
		return false, false, fmt.Errorf("put blob: %w", putErr)
	}
	resp, err := srv.cacheServiceClient.CompleteBlobUploads(ctx, []cacheservice.BlobUploadCompletion{{
		Digest:    rawDigest,
		Size:      size,
		MediaType: mediaType,
		UploadID:  prep.UploadID,
	}})
	if err != nil {
		return false, false, fmt.Errorf("complete upload: %w", err)
	}
	switch {
	case len(resp.Verified) == 1:
		return true, false, nil
	case len(resp.AlreadyExists) == 1:
		return false, true, nil
	case len(resp.Failed) == 1:
		return false, false, fmt.Errorf("upload verification failed: %s", resp.Failed[0].Error)
	default:
		return false, false, fmt.Errorf("upload completion returned no outcome for %s", rawDigest)
	}
}

// cacheServiceShutdownExport is the opt-in GracefulStop hook (§7 D1):
// runs after sessions drain and prune, bounded by the export budget, and
// never fails the shutdown — an exhausted budget costs blobs (S5/S6).
func (srv *Server) cacheServiceShutdownExport(ctx context.Context) {
	if srv.cacheServiceClient == nil || !srv.cacheServiceSettings.ExportOnShutdown {
		return
	}
	exportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), srv.cacheServiceSettings.ExportBudget)
	defer cancel()
	if _, err := srv.CacheServiceExport(exportCtx, srv.cacheServiceSettings.MetadataOnly); err != nil {
		slog.Warn("cache service shutdown export failed", "error", err)
	}
}

// HandleCacheExport serves POST /v1/cache/export on the engine's operator
// listener: the productized export trigger (§7 D1). The response is the
// export summary. When an export secret is configured, callers present it
// in X-Dagger-Export-Secret.
func (srv *Server) HandleCacheExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if srv == nil || srv.cacheServiceClient == nil {
		http.Error(w, "cache service not configured", http.StatusServiceUnavailable)
		return
	}
	if secret := srv.cacheServiceSettings.ExportSecret; secret != "" {
		if r.Header.Get("X-Dagger-Export-Secret") != secret {
			http.Error(w, "invalid export secret", http.StatusForbidden)
			return
		}
	}
	metadataOnly := srv.cacheServiceSettings.MetadataOnly
	if raw := r.URL.Query().Get("metadataOnly"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			http.Error(w, "metadataOnly must be a boolean", http.StatusBadRequest)
			return
		}
		metadataOnly = parsed
	}

	summary, err := srv.CacheServiceExport(r.Context(), metadataOnly)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(summary); err != nil {
		slog.Warn("cache export: encode summary response failed", "error", err)
	}
}
