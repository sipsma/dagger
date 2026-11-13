package dagql

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// writeBehindTestTables reads every table a save writes from the cache's
// store, each row as one string, sorted, by table; and the saved next result
// number.
func writeBehindTestTables(t *testing.T, ctx context.Context, c *Cache) map[string][]string {
	t.Helper()
	tables := []string{
		"results", "eq_classes", "eq_class_digests", "terms", "term_inputs",
		"result_output_eq_classes", "result_deps", "result_calls", "persisted_edges",
		"result_snapshot_links", "snapshot_content_links", "snapshot_content_hashes",
		"imported_layer_blob_index", "imported_layer_diff_index",
	}
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := c.sqlDB.QueryContext(ctx, "SELECT * FROM "+table)
		require.NoError(t, err)
		cols, err := rows.Columns()
		require.NoError(t, err)
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			require.NoError(t, rows.Scan(ptrs...))
			parts := make([]string, len(values))
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				parts[i] = fmt.Sprintf("%s=%v", cols[i], v)
			}
			out[table] = append(out[table], strings.Join(parts, "|"))
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		slices.Sort(out[table])
	}
	next, _, err := c.pdb.SelectMetaValue(ctx, persistdb.MetaKeyNextResultID)
	require.NoError(t, err)
	out["meta.next_result_id"] = []string{next}
	return out
}

// writeBehindTestMatchesWholeSave writes the cache's changes behind and
// requires its store to hold exactly what a whole save of the same cache
// writes: it reads every table, saves the cache whole over the same store,
// and reads them again.
func writeBehindTestMatchesWholeSave(t *testing.T, ctx context.Context, c *Cache, step string) {
	t.Helper()
	_, err := c.WriteBehind(ctx)
	require.NoError(t, err, step)
	behind := writeBehindTestTables(t, ctx, c)
	require.NoError(t, c.Checkpoint(ctx), step)
	whole := writeBehindTestTables(t, ctx, c)
	for table, rows := range whole {
		require.Equal(t, rows, behind[table], "%s: table %s written behind differs from a whole save", step, table)
	}
	require.Len(t, behind, len(whole), step)
}

// writeBehindTestRecipe returns the recipe digest a Cloud entry is indexed
// under.
func writeBehindTestRecipe(c *Cache, number uint64) digest.Digest {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	res := c.resultsByID[sharedResultID(number)]
	if res == nil || len(res.recipeKeys) == 0 {
		return ""
	}
	return res.recipeKeys[0]
}

// writeBehindTestScenario changes a Cloud cache in each way a save writes,
// calling check after each change: stored roots with dependencies, a stored
// part, a last use, a root sharing a dependency, holdings that join stored
// entries' classes, a prune, a collection and a forced compaction.
func writeBehindTestScenario(t *testing.T, ctx context.Context, cloud *Cache, check func(step string)) {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "wb-root", &transferTestValue{Text: "root"})
	p := persistedListTestResult(t, actx, a, asrv, "wb-dep", &transferTestValue{Text: "dep"})
	transferTestDependency(a, actx, r, p)
	root := storedPersistenceTestMerge(t, ctx, cloud, exportTestBundle(t, actx, a, r))[0]
	check("a stored root with a dependency")

	stored, _, err := cloud.SetStoredPart(ctx, root, storedPruneTestPart("snapshot", map[string]int64{"wb-base": 10, "wb-top": 20}), 0)
	require.NoError(t, err)
	require.True(t, stored)
	check("a stored part")

	// A use moves the entry's last use forward, under the read lock, as a
	// lookup's hit does.
	cloud.egraphMu.RLock()
	cloud.touchLastUsed(cloud.resultsByID[sharedResultID(root)], time.Now().Add(time.Hour).UnixNano())
	cloud.egraphMu.RUnlock()
	check("a last use")

	r2 := persistedListTestResult(t, actx, a, asrv, "wb-second", &transferTestValue{Text: "second"})
	transferTestDependency(a, actx, r2, p)
	second := storedPersistenceTestMerge(t, ctx, cloud, exportTestBundle(t, actx, a, r2))[0]
	check("a second root sharing the dependency")

	other := storedPruneTestRoot(t, ctx, cloud, "wb-other")
	check("an unrelated root")

	// Two engine holdings of the same content join the classes of two
	// stored entries.
	content := testDigest("wb-shared-content")
	for i, number := range []uint64{second, other} {
		h := holdingOf(fmt.Sprintf("wb-join-%d", i))
		h.Recipe = writeBehindTestRecipe(cloud, number)
		h.Request = h.Recipe
		h.ContentDigest = content
		_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-j", Number: uint64(i + 1)}, h)
		require.NoError(t, err)
	}
	check("holdings joining two stored entries' classes")

	_, removed, _, err := cloud.removePrunedEdge(ctx, sharedResultID(root))
	require.NoError(t, err)
	require.True(t, removed)
	check("a pruned root")

	_, _, err = cloud.CollectRemoteHoldings(ctx, cloud.ReleaseRemoteCache(ctx, "cache-j").Candidates)
	require.NoError(t, err)
	check("released and collected holdings")

	_, _, err = cloud.CompactEqClasses(ctx)
	require.NoError(t, err)
	check("a forced compaction")

	again := storedPruneTestRoot(t, ctx, cloud, "wb-after-compaction")
	require.NotZero(t, again)
	check("a root stored after the compaction")
}

// After every change, a store written behind holds exactly what a whole save
// of the same cache writes, table by table.
func TestWriteBehindWritesWhatAWholeSaveWrites(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	writeBehindTestScenario(t, ctx, cloud, func(step string) {
		writeBehindTestMatchesWholeSave(t, ctx, cloud, step)
	})
}

// A store written behind, and never saved whole, restores the same cache a
// whole save would have: after a crash with nothing pending, the restored
// entries are the ones the cache held, with their records, parts, edges,
// dependencies and times, and a pruned root's value doesn't come back.
func TestWriteBehindRestoresWhatTheCacheHeld(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	var pruned uint64
	writeBehindTestScenario(t, ctx, cloud, func(step string) {
		_, err := cloud.WriteBehind(ctx)
		require.NoError(t, err, step)
		if step == "a stored root with a dependency" {
			cloud.egraphMu.RLock()
			for id := range cloud.persistedEdgesByResult {
				pruned = uint64(id)
			}
			cloud.egraphMu.RUnlock()
		}
	})
	require.NotZero(t, pruned)
	want := writeBehindTestSavedState(cloud)
	require.NotContains(t, want, pruned, "the pruned root's value is gone before the crash")
	// The process dies: nothing saves whole or marks the store clean.
	require.NoError(t, cloud.CloseDiscardingPersistence())

	ctx, cloud = storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	require.Equal(t, CachePersistenceResetNone, cloud.PersistenceResetReason())
	require.Equal(t, want, writeBehindTestSavedState(cloud))
}

// writeBehindTestSavedState is what a save keeps of each entry in the saved
// closure: the closure a whole save selects, with each entry's state.
func writeBehindTestSavedState(c *Cache) map[uint64]storedPersistenceTestEntry {
	all := storedPersistenceTestState(c)
	c.egraphMu.RLock()
	selected, _ := c.snapshotPersistedRootClosureLocked()
	c.egraphMu.RUnlock()
	out := map[uint64]storedPersistenceTestEntry{}
	for id := range selected {
		out[uint64(id)] = all[uint64(id)]
	}
	return out
}

// A write with nothing changed since the last one writes nothing.
func TestWriteBehindWithNothingChangedWritesNothing(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	storedPruneTestRoot(t, ctx, cloud, "wb-quiet")
	first, err := cloud.WriteBehind(ctx)
	require.NoError(t, err)
	require.Positive(t, first.Entries)
	second, err := cloud.WriteBehind(ctx)
	require.NoError(t, err)
	require.Equal(t, WriteBehindReport{}, second)
}

// Only a blob-backed cache with a store writes behind.
func TestWriteBehindNeedsABlobBackedStore(t *testing.T) {
	t.Parallel()
	ctx := cacheTestContext(t.Context())
	engine, err := NewCache(ctx, filepath.Join(t.TempDir(), "engine.db"), nil, nil)
	require.NoError(t, err)
	_, err = engine.WriteBehind(ctx)
	require.ErrorContains(t, err, "no blob store")
	require.NoError(t, engine.Close(ctx))

	inMemory, err := NewCache(ctx, "", nil, nil, WithBlobStore())
	require.NoError(t, err)
	_, err = inMemory.WriteBehind(ctx)
	require.ErrorContains(t, err, "no persistence database")
	require.NoError(t, inMemory.Close(ctx))
}
