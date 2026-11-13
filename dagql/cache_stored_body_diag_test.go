package dagql

// A diagnostic that never ships: it measures a saved store of the service's,
// STOREDIAG_DB, in the format before compressed records, as today's line
// holds it and as compressed records hold it.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dagger/dagger/dagql/persistdb"
	"github.com/stretchr/testify/require"
)

func TestStoredBodyDiagSavedStore(t *testing.T) {
	src := os.Getenv("STOREDIAG_DB")
	if src == "" {
		t.Skip("STOREDIAG_DB names no store")
	}
	path := filepath.Join(t.TempDir(), "cloud.db")
	in, err := os.Open(src)
	require.NoError(t, err)
	out, err := os.Create(path)
	require.NoError(t, err)
	_, err = io.Copy(out, in)
	require.NoError(t, err)
	require.NoError(t, in.Close())
	require.NoError(t, out.Close())
	ctx := cacheTestContext(context.Background())
	// The store's rows are in the line's format, which a compressed-records
	// cache reads as whole envelopes once the marker says it may.
	setCacheTestMeta(t, ctx, path, persistdb.MetaKeySchemaVersion, cachePersistenceSchemaVersion+"+"+blobBackedStoreFormat)

	heap := func() int64 {
		runtime.GC()
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return int64(stats.HeapAlloc)
	}
	base := heap()
	start := time.Now()
	cloud, err := NewCache(ctx, path, nil, nil, WithBlobStore())
	require.NoError(t, err)
	require.Equal(t, CachePersistenceResetNone, cloud.PersistenceResetReason())
	// The worker would compress as soon as it could: stop it, to measure
	// the line's holding first.
	cloud.closeStoredBodies()
	cloud.egraphMu.RLock()
	records := int64(len(cloud.resultsByID))
	var roots []uint64
	var encoded int64
	for id, res := range cloud.resultsByID {
		encoded += res.storedRecordBytes
		if _, ok := cloud.persistedEdgesByResult[id]; ok {
			roots = append(roots, uint64(id))
		}
	}
	cloud.egraphMu.RUnlock()
	t.Logf("DIAG restore (line format, whole): %d entries, %d stored roots, %s; records encoded %d bytes, %d a record", records, len(roots), time.Since(start), encoded, encoded/records)
	h := heap() - base
	t.Logf("DIAG heap restored whole (the line's restore): %d bytes, %d a record", h, h/records)

	start = time.Now()
	_, err = cloud.StoredSizes(ctx, roots, nil)
	require.NoError(t, err)
	t.Logf("DIAG price every root, cold: %s", time.Since(start))
	h = heap() - base
	t.Logf("DIAG heap whole with kept transfer copies (the line live): %d bytes, %d a record", h, h/records)

	start = time.Now()
	cloud.egraphMu.RLock()
	var entries []*sharedResult
	for _, res := range cloud.resultsByID {
		entries = append(entries, res)
	}
	cloud.egraphMu.RUnlock()
	cloud.storeBodies(entries)
	t.Logf("DIAG compress every record: %s, %d µs a record", time.Since(start), time.Since(start).Microseconds()/records)
	cloud.egraphMu.RLock()
	var compressed, bodyBytes int64
	for _, res := range cloud.resultsByID {
		if res.stored != nil {
			compressed++
			bodyBytes += int64(len(res.stored.envelope))
		}
	}
	cloud.egraphMu.RUnlock()
	h = heap() - base
	t.Logf("DIAG heap compressed (B at rest): %d bytes, %d a record; %d of %d compressed, bodies %d bytes, %d a record", h, h/records, compressed, records, bodyBytes, bodyBytes/max(compressed, 1))

	start = time.Now()
	_, err = cloud.StoredSizes(ctx, roots, nil)
	require.NoError(t, err)
	t.Logf("DIAG price every root, cold, compressed: %s", time.Since(start))

	start = time.Now()
	require.NoError(t, cloud.Checkpoint(ctx))
	t.Logf("DIAG save (whole rewrite, compressed rows): %s", time.Since(start))
	if info, err := os.Stat(path); err == nil {
		t.Logf("DIAG saved file %d bytes", info.Size())
	}
	require.NoError(t, cloud.Close(ctx))
	cloud = nil

	base = heap()
	start = time.Now()
	restored, err := NewCache(ctx, path, nil, nil, WithBlobStore())
	require.NoError(t, err)
	require.Equal(t, CachePersistenceResetNone, restored.PersistenceResetReason())
	took := time.Since(start)
	h = heap() - base
	restored.egraphMu.RLock()
	n := int64(len(restored.resultsByID))
	restored.egraphMu.RUnlock()
	t.Logf("DIAG restore compressed: %d entries in %s; heap %d bytes, %d a record", n, took, h, h/n)
	// What the decoded call frames hold: the heap each gives back when no
	// entry keeps it.
	restored.closeStoredBodies()
	restored.egraphMu.Lock()
	for _, res := range restored.resultsByID {
		res.resultCallMu.Lock()
		res.resultCall = nil
		res.resultCallMu.Unlock()
		if res.stored != nil {
			res.stored.call = nil
		}
	}
	restored.egraphMu.Unlock()
	without := heap() - base
	t.Logf("DIAG heap without call frames: %d bytes, %d a record; frames held %d a record", without, without/n, (h-without)/n)
	runtime.KeepAlive(restored)
}
