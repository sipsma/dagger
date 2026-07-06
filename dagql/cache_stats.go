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
	// A restored value materialized from its lazy fragment instead.
	cacheServeFromLazyForm = "served_from_lazy_form"
	// A hit's retained sources were exhausted; the call executed live.
	cacheServeDemotedToMiss = "demoted_to_miss"
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
	s.mu.Lock()
	if s.counters == nil {
		s.counters = make(map[cacheStatKey]int64)
	}
	s.counters[cacheStatKey{outcome: outcome, field: field}]++
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
	Counters       map[string]map[string]int64 `json:"counters"`
	RestoreSummary *CacheRestoreSummary        `json:"restore_summary,omitempty"`
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
	c.egraphMu.RUnlock()
	payload := CacheServeStatsFile{
		Counters:       c.serveStats.byOutcome(),
		RestoreSummary: restoreSummary,
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
