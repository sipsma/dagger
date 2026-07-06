package dagql

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/dagger/dagger/engine/slog"
)

// Warm-serving outcomes, classified exactly once at the lookup and serving
// terminal paths. Together with the restore summary they are the evidence
// that reuse actually happened: proofs assert these counters per named
// field and would fail loudly if serving silently recomputed instead.
const (
	// A lookup served a result produced by work executed this boot.
	cacheServeHitLive = "hit_live"
	// A lookup served a result restored from persisted state.
	cacheServeHitRestored = "hit_restored"
	// No cached result could serve; the call executed.
	cacheServeMissFirst = "miss_first"
	// A restored value materialized by decoding its local snapshot.
	cacheServeFromSnapshot = "served_from_snapshot"
	// A restored value materialized by fetching and applying its content
	// chain, which installed a local snapshot the decode then used.
	cacheServeFromContentChain = "served_from_content_chain"
	// A restored value materialized from its lazy fragment instead.
	cacheServeFromLazyForm = "served_from_lazy_form"
	// A hit's retained sources were exhausted; the call executed live.
	cacheServeDemotedToMiss = "demoted_to_miss"

	// Chain-fetch outcomes, one per blob attempt during chain realization
	// (§9.3's typed failure vocabulary; the field key is the call being
	// served).
	cacheChainFetchOK = "chain_fetch_ok"
	// A required blob is absent from the CAS: permanent for this boot, the
	// chain source marks non-viable.
	cacheChainFetchMissing = "chain_fetch_missing"
	// Transport-shaped failure (network, 5xx, timeout, apply): transient,
	// nothing marks, the next walk retries the chain.
	cacheChainFetchError = "chain_fetch_error"
	// Fetched bytes did not match the blob digest: permanent and loud, the
	// bytes are discarded and the chain source marks non-viable.
	cacheChainFetchCorrupt = "chain_fetch_corrupt"
	// What chain realization actually moved: blobs fetched from the CAS
	// into the content store, and their bytes. Tallied exactly once per
	// realization attempt (success or failure — transfers before a failure
	// are real, and stay ingested, so a retry never re-moves them).
	cacheChainFetchBlobs = "chain_fetch_blobs"
	cacheChainFetchBytes = "chain_fetch_bytes"
)

type cacheStatKey struct {
	outcome string
	field   string
}

// cacheServeStats holds the engine-lifetime warm-serving counters, keyed by
// (outcome, field). Field keying is what makes strong assertions safe: a
// warm engine legitimately first-misses session-scoped calls, so proofs
// assert per named field rather than globally.
type cacheServeStats struct {
	mu       sync.Mutex
	counters map[cacheStatKey]int64
}

func (s *cacheServeStats) inc(outcome, field string) {
	s.add(outcome, field, 1)
}

func (s *cacheServeStats) add(outcome, field string, n int64) {
	if n == 0 {
		return
	}
	s.mu.Lock()
	if s.counters == nil {
		s.counters = make(map[cacheStatKey]int64)
	}
	s.counters[cacheStatKey{outcome: outcome, field: field}] += n
	s.mu.Unlock()
}

// byOutcome returns the counters grouped outcome → field → count.
func (s *cacheServeStats) byOutcome() map[string]map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	grouped := make(map[string]map[string]int64, len(s.counters))
	for key, count := range s.counters {
		fields := grouped[key.outcome]
		if fields == nil {
			fields = make(map[string]int64)
			grouped[key.outcome] = fields
		}
		fields[key.field] = count
	}
	return grouped
}

// statFieldName names the call for counter keying: the schema field when the
// call has one, else its synthetic operation.
func statFieldName(frame *ResultCall) string {
	if frame == nil {
		return "unknown"
	}
	if frame.Field != "" {
		return frame.Field
	}
	if frame.SyntheticOp != "" {
		return frame.SyntheticOp
	}
	return "unknown"
}

// classifyServeOutcome records one warm-serving outcome for one call, with
// a debug line as the diagnosis path when a count assertion fails.
func (c *Cache) classifyServeOutcome(ctx context.Context, outcome string, frame *ResultCall, resultID sharedResultID) {
	field := statFieldName(frame)
	c.serveStats.inc(outcome, field)
	slog.DebugContext(ctx, "cache serve outcome",
		"outcome", outcome, "field", field, "sharedResultID", resultID)
}

// CacheServeStatsFile is the shutdown stats payload: the warm-serving
// counters plus the boot-restore summary — the file-based assertion vehicle
// for reuse proofs.
type CacheServeStatsFile struct {
	Counters            map[string]map[string]int64 `json:"counters"`
	RestoreSummary      *CacheRestoreSummary        `json:"restore_summary,omitempty"`
	BundleImportSummary *CacheBundleBootSummary     `json:"bundle_import_summary,omitempty"`
}

// cacheStatsFilePath derives the stats file path from the persisted store's
// path, so the file lands next to the database in the engine's state
// directory.
func cacheStatsFilePath(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	return strings.TrimSuffix(dbPath, ".db") + "-stats.json"
}

// writeServeStatsFile writes the stats file at shutdown. Failures are
// logged, not fatal: stats must never block a clean shutdown.
func (c *Cache) writeServeStatsFile() {
	if c.statsFilePath == "" {
		return
	}
	c.egraphMu.RLock()
	restoreSummary := c.restoreSummary
	bundleBootSummary := c.bundleBootSummary
	c.egraphMu.RUnlock()
	payload := CacheServeStatsFile{
		Counters:            c.serveStats.byOutcome(),
		RestoreSummary:      restoreSummary,
		BundleImportSummary: bundleBootSummary,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		slog.Warn("failed to encode cache serve stats", "err", err)
		return
	}
	if err := os.WriteFile(c.statsFilePath, encoded, 0o600); err != nil {
		slog.Warn("failed to write cache serve stats file", "path", c.statsFilePath, "err", err)
	}
}
