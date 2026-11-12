package dagql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTransferRoundTripState carries the stored bundle benchmarks' pool from
// an engine through the Cloud to a second engine, which takes one root's
// closure first and then the whole batch, so the second bundle carries held
// rows. It logs the Cloud's and the second engine's state, entry by entry,
// with times left out, as lines "STATE <cache> <entry> <hash>": two versions
// of the transfer format that merge into the same state log the same lines.
func TestTransferRoundTripState(t *testing.T) {
	ctx, cloud, batch := storedBenchCloud(t)

	bctx := cacheTestContext(context.Background())
	b, err := NewCache(bctx, filepath.Join(t.TempDir(), "b.db"), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, b.CloseDiscardingPersistence()) })
	bsrv := newDagqlServerForTest(t, &persistCodecRoot{})
	bsrv.InstallObject(NewClass(bsrv, ClassOpts[*storedBenchValue]{}))
	bctx = srvToContext(ContextWithCache(bctx, b), bsrv)

	place := func(roots []uint64, held *HeldBy) StoredBundleResult {
		got, err := cloud.StoredBundle(ctx, roots, held)
		require.NoError(t, err)
		reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
		require.NoError(t, err)
		require.True(t, reply.Committed)
		_, err = cloud.ApplyMergedReply(ctx, "cache-b", got.Bundle, reply)
		require.NoError(t, err)
		return got
	}
	place(batch[:1], nil)
	second := place(batch, &HeldBy{Cache: "cache-b", Now: time.Now().Unix()})
	require.NotEmpty(t, second.Held, "the second bundle carries held rows")

	lines := append(roundTripStateLines(t, "cloud", cloud), roundTripStateLines(t, "b", b)...)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	for _, line := range lines {
		t.Log(line)
	}
	t.Logf("STATE all %x (%d lines, %d held rows)", sum, len(lines), len(second.Held))
}

// roundTripStateLines is c's state: each entry's dependencies, recipe, value
// envelope and stored parts, then each section of its debug snapshot, with
// fields of time and the run's own identity left out.
func roundTripStateLines(t *testing.T, name string, c *Cache) []string {
	t.Helper()
	hash := func(v any) string {
		data, err := json.Marshal(v)
		require.NoError(t, err)
		return fmt.Sprintf("%x", sha256.Sum256(data))[:16]
	}
	var lines []string
	c.egraphMu.RLock()
	ids := make([]sharedResultID, 0, len(c.resultsByID))
	for id := range c.resultsByID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		res := c.resultsByID[id]
		deps := make([]sharedResultID, 0, len(res.deps))
		for dep := range res.deps {
			deps = append(deps, dep)
		}
		slices.Sort(deps)
		recipe, err := res.loadResultCall().deriveRecipeDigest(c)
		require.NoError(t, err)
		lines = append(lines, fmt.Sprintf("STATE %s entry %d deps=%v recipe=%s envelope=%s parts=%s replacements=%d", name, id, deps, recipe, hash(res.persistedEnvelope), hash(storedPartTestOffers(res.storedParts)), res.replacements))
	}
	c.egraphMu.RUnlock()

	var buf bytes.Buffer
	require.NoError(t, c.WriteDebugCacheSnapshot(&buf))
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &snapshot))
	keys := make([]string, 0, len(snapshot))
	for key := range snapshot {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if key == "boot_id" || key == "captured_at_time" {
			continue // the run's own
		}
		lines = append(lines, fmt.Sprintf("STATE %s snapshot %s %s", name, key, hash(roundTripTimeless(snapshot[key]))))
	}
	return lines
}

// roundTripTimeless is v without the fields that hold times.
func roundTripTimeless(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, value := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "unix") || strings.HasSuffix(lower, "_at") || strings.Contains(lower, "time") {
				continue
			}
			out[key] = roundTripTimeless(value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			out[i] = roundTripTimeless(value)
		}
		return out
	default:
		return v
	}
}
