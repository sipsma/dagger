package dagql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	writeBehindTestDrain(t, ctx, c, step, nil)
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

// Written one mark at a time, a store restores after every write, and holds
// what a whole save writes once the marks drain, through every change the
// scenario makes.
func TestWriteBehindOneMarkAtATime(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	cloud.testWriteBehindBudget = 1
	writeBehindTestScenario(t, ctx, cloud, func(step string) {
		writeBehindTestDrain(t, ctx, cloud, step, func(WriteBehindReport) {
			writeBehindTestRestorable(t, ctx, cloud, path, step)
		})
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

// writeBehindTestJoin attaches, from engine cache-j, a holding of each
// entry's recipe with the content digest content, which joins their
// classes.
func writeBehindTestJoin(t *testing.T, ctx context.Context, cloud *Cache, content digest.Digest, numbers ...uint64) {
	t.Helper()
	for _, number := range numbers {
		h := holdingOf(fmt.Sprintf("wb-join-%s-%d", content, number))
		h.Recipe = writeBehindTestRecipe(cloud, number)
		h.Request = h.Recipe
		h.ContentDigest = content
		_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-j", Number: number}, h)
		require.NoError(t, err)
	}
}

// A join moves every stored entry of the class it absorbs: each one's
// output-class rows name the class that survives, however many stored
// entries the absorbed class held.
func TestWriteBehindFollowsAJoinOfStoredClasses(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	var roots []uint64
	for i := range 4 {
		roots = append(roots, storedPruneTestRoot(t, ctx, cloud, fmt.Sprintf("wb-joined-%d", i)))
	}
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "four stored roots")
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-content-01"), roots[0], roots[1])
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-content-23"), roots[2], roots[3])
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "two classes of two stored entries each")
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-content-12"), roots[1], roots[2])
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "the two classes joined")
}

// A stored class that absorbs an engine's class takes its terms, and with
// them their input classes, which a whole save writes once a term of a
// stored class takes them.
func TestWriteBehindFollowsTermsAJoinMoves(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	stored := storedPruneTestRoot(t, ctx, cloud, "wb-stored")
	// The stored entry's class first joins another digest, so that it isn't
	// the smaller class of the join below.
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-stored-content"), stored)
	// An engine's entry, which the Cloud doesn't store, with a term over an
	// input no stored class names, and its content.
	engine := holdingOf("wb-engine")
	engine.Term.Inputs = []digest.Digest{testDigest("wb-engine-input")}
	engine.ContentDigest = testDigest("wb-engine-content")
	_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-e", Number: 1}, engine)
	require.NoError(t, err)
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored class and an engine's class")
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-engine-content"), stored)
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "the stored class absorbs the engine's")
}

// A replacement in flight leaves the store as it was: a write while the
// replaced value's release runs writes nothing of it. A replacement that
// failed takes the entry out of the store, as a whole save leaves it out.
func TestWriteBehindFollowsAReplacement(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	bundle, _ := mergeTestSource(t, "wb-replaced", "value")
	first, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	number := first.Imported()[0].ResultID
	storedPruneTestRoot(t, ctx, cloud, "wb-bystander")
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored root")

	cloud.egraphMu.Lock()
	entry := cloud.resultsByID[sharedResultID(number)]
	entry.expiresAtUnix = time.Now().Add(-time.Hour).Unix()
	releasing, release := make(chan struct{}), make(chan struct{})
	// The release is let go before the cache closes, whatever fails first.
	letGo := sync.OnceFunc(func() { close(release) })
	defer letGo()
	entry.onRelease = func(context.Context) error {
		close(releasing)
		<-release
		return errors.New("wb-release-failed")
	}
	cloud.egraphMu.Unlock()
	merged := make(chan error, 1)
	go func() {
		_, err := cloud.MergeValues(ctx, "cache-a", bundle)
		merged <- err
	}()
	<-releasing
	before := writeBehindTestTables(t, ctx, cloud)
	report, err := cloud.WriteBehind(ctx)
	require.NoError(t, err)
	require.Equal(t, WriteBehindReport{Waited: true}, report, "nothing is written while a replacement is in flight")
	require.Equal(t, before, writeBehindTestTables(t, ctx, cloud))
	letGo()
	require.ErrorContains(t, <-merged, "wb-release-failed")
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a failed replacement")
	for _, row := range writeBehindTestTables(t, ctx, cloud)["results"] {
		require.False(t, strings.HasPrefix(row, fmt.Sprintf("id=%d|", number)), "the failed entry isn't saved")
	}
}

// A whole save while a replacement is in flight leaves out the replaced
// entry and the stored root that depends on it. Once the replacement ends
// cleanly, the writes behind bring both back, as a whole save would.
func TestWriteBehindAfterAWholeSaveDuringAReplacement(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "wb-parent", &transferTestValue{Text: "parent"})
	p := persistedListTestResult(t, actx, a, asrv, "wb-child", &transferTestValue{Text: "child"})
	transferTestDependency(a, actx, r, p)
	root := storedPersistenceTestMerge(t, ctx, cloud, exportTestBundle(t, actx, a, r))[0]
	child := storedPartTestDeps(cloud, root)[0]
	// The child alone is sent again, so that the replacement marks the child
	// and nothing marks its parent.
	childBundle := exportTestBundle(t, actx, a, p)
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored root with a dependency")

	cloud.egraphMu.Lock()
	entry := cloud.resultsByID[sharedResultID(child)]
	entry.expiresAtUnix = time.Now().Add(-time.Hour).Unix()
	releasing, release := make(chan struct{}), make(chan struct{})
	letGo := sync.OnceFunc(func() { close(release) })
	defer letGo()
	entry.onRelease = func(context.Context) error {
		close(releasing)
		<-release
		return nil
	}
	cloud.egraphMu.Unlock()
	merged := make(chan error, 1)
	go func() {
		_, err := cloud.MergeValues(ctx, "cache-a", childBundle)
		merged <- err
	}()
	<-releasing
	require.NoError(t, cloud.Checkpoint(ctx), "a whole save while the replacement is in flight")
	letGo()
	require.NoError(t, <-merged)
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "the replacement ended")
	tables := writeBehindTestTables(t, ctx, cloud)
	var ids []string
	for _, row := range tables["results"] {
		ids = append(ids, strings.SplitN(row, "|", 2)[0])
	}
	require.Contains(t, ids, fmt.Sprintf("id=%d", root), "the stored root is back")
	require.Contains(t, ids, fmt.Sprintf("id=%d", child), "its dependency is back")
}

// A write rewrites what changed, not what shares its class: a new term of a
// stored class with many terms is written alone.
func TestWriteBehindWritesATermAlone(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	stored := storedPruneTestRoot(t, ctx, cloud, "wb-many-terms")
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-many-terms-content"), stored)
	attach := func(i int) {
		h := holdingOf(fmt.Sprintf("wb-term-%d", i))
		h.ContentDigest = testDigest("wb-many-terms-content")
		_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-t", Number: uint64(i + 1)}, h)
		require.NoError(t, err)
	}
	for i := range 30 {
		attach(i)
	}
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored class with thirty engines' terms")
	require.GreaterOrEqual(t, len(writeBehindTestTables(t, ctx, cloud)["terms"]), 30)
	attach(30)
	report, err := cloud.WriteBehind(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, report.Terms, "the new term, not its class's thirty")
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "one more term")
}

// A change of last use alone updates that one field of its entry's row.
func TestWriteBehindUpdatesALastUseAlone(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	root := storedPruneTestRoot(t, ctx, cloud, "wb-used")
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored root")
	cloud.egraphMu.RLock()
	cloud.touchLastUsed(cloud.resultsByID[sharedResultID(root)], time.Now().Add(time.Hour).UnixNano())
	cloud.egraphMu.RUnlock()
	report, err := cloud.WriteBehind(ctx)
	require.NoError(t, err)
	require.Equal(t, WriteBehindReport{Used: 1}, report)
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a last use")
}

// A holding that teaches the cache nothing it didn't know, as every engine
// that holds a stored entry reports it again, changes no identity row, and
// the write after it writes none.
func TestWriteBehindWritesNoIdentityAHoldingDoesntChange(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	stored := storedPruneTestRoot(t, ctx, cloud, "wb-held-again")
	h := holdingOf("wb-held-again-holding")
	h.Recipe = writeBehindTestRecipe(cloud, stored)
	h.Request = h.Recipe
	h.ContentDigest = testDigest("wb-held-again-content")
	_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-h", Number: 1}, h)
	require.NoError(t, err)
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored entry an engine holds")
	for i := 2; i <= 4; i++ {
		_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: CacheID(fmt.Sprintf("cache-h%d", i)), Number: 1}, h)
		require.NoError(t, err)
	}
	report, err := cloud.WriteBehind(ctx)
	require.NoError(t, err)
	require.Zero(t, report.Classes, "no class changed")
	require.Zero(t, report.Terms, "no term changed")
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "three more engines hold it")
}

// A term's inputs' provenance is saved with it: when an entry's association
// changes it, the write behind writes it, as a whole save does.
func TestWriteBehindFollowsATermsProvenance(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	stored := storedPruneTestRoot(t, ctx, cloud, "wb-provenance")
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-provenance-content"), stored)
	h := holdingOf("wb-provenance-term")
	h.Term.Inputs = []digest.Digest{testDigest("wb-provenance-input")}
	h.ContentDigest = testDigest("wb-provenance-content")
	_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-p", Number: 1}, h)
	require.NoError(t, err)
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored class with a term of one input")

	cloud.egraphMu.Lock()
	var termID egraphTermID
	var provenance []egraphInputProvenanceKind
	for id, kinds := range cloud.termInputProvenance {
		if len(kinds) == 1 {
			termID = id
			provenance = []egraphInputProvenanceKind{egraphInputProvenanceKindResult}
			if kinds[0] == egraphInputProvenanceKindResult {
				provenance = []egraphInputProvenanceKind{egraphInputProvenanceKindDigest}
			}
		}
	}
	require.NotZero(t, termID)
	cloud.associateResultWithTermLocked(ctx, cloud.resultsByID[sharedResultID(stored)], termID, provenance)
	cloud.egraphMu.Unlock()
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "the term's provenance changed")
}

// A taught content digest replaces an entry's call frame, held in its body
// once compressed: the write behind writes the new frame, as a whole save
// does.
func TestWriteBehindFollowsATaughtContentDigest(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPersistenceTestCloud(t, filepath.Join(t.TempDir(), "cloud.db"))
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	root := storedPruneTestRoot(t, ctx, cloud, "wb-taught")
	storedTestCompress(cloud)
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored root, compressed")
	require.NoError(t, cloud.TeachContentDigest(ctx, Result[Typed]{shared: storedBundleTestEntry(cloud, root)}, testDigest("wb-taught-content")))
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a content digest taught")
}

// writeBehindTestDrain writes behind until no marks are left, calling each,
// if set, after every write.
func writeBehindTestDrain(t *testing.T, ctx context.Context, c *Cache, step string, each func(WriteBehindReport)) {
	t.Helper()
	for range 10_000 {
		report, err := c.WriteBehind(ctx)
		require.NoError(t, err, step)
		if each != nil {
			each(report)
		}
		if !report.More {
			return
		}
	}
	t.Fatalf("%s: marks never drained", step)
}

// writeBehindTestRestorable requires that the store, as it is now, restores
// as it is: a copy of it opens with nothing wiped, as after a crash.
func writeBehindTestRestorable(t *testing.T, ctx context.Context, c *Cache, path, step string) {
	t.Helper()
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "copy.db")
	for _, sfx := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + sfx)
		if sfx != "" && os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err, step)
		require.NoError(t, os.WriteFile(copyPath+sfx, data, 0o600), step)
	}
	copied, err := NewCache(ctx, copyPath, nil, nil, WithBlobStore())
	require.NoError(t, err, step)
	require.Equal(t, CachePersistenceResetNone, copied.PersistenceResetReason(), "%s: the store as written so far restores", step)
	// Each restored entry is an output of the class of its own recipe: no
	// row names a class number another class has taken since.
	copied.egraphMu.RLock()
	for id, res := range copied.resultsByID {
		if len(res.recipeKeys) == 0 {
			continue
		}
		recipe := res.recipeKeys[0].String()
		var own bool
		for eqID := range copied.outputEqClassRootsLocked(id) {
			if _, ok := copied.eqClassToDigests[copied.eqClassRootLocked(eqID)][recipe]; ok {
				own = true
			}
		}
		require.True(t, own, "%s: restored entry %d is an output of its recipe's class", step, id)
	}
	copied.egraphMu.RUnlock()
	require.NoError(t, copied.CloseDiscardingPersistence())
}

// A write takes a bounded count of marked entries and says when marks are
// left; the next takes the rest. After each budgeted write the store
// restores as it is, and once the marks drain it holds what a whole save
// writes: across a join of stored classes, and across a prune that lands
// between two budgeted writes.
func TestWriteBehindBudgetedWrites(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	cloud.testWriteBehindBudget = 2
	actx, a, asrv := transferTestCache(t)
	var roots []uint64
	for i := range 4 {
		r := persistedListTestResult(t, actx, a, asrv, fmt.Sprintf("wb-budget-%d", i), &transferTestValue{Text: fmt.Sprintf("root-%d", i)})
		p := persistedListTestResult(t, actx, a, asrv, fmt.Sprintf("wb-budget-dep-%d", i), &transferTestValue{Text: fmt.Sprintf("dep-%d", i)})
		transferTestDependency(a, actx, r, p)
		roots = append(roots, storedPersistenceTestMerge(t, ctx, cloud, exportTestBundle(t, actx, a, r))[0])
	}
	report, err := cloud.WriteBehind(ctx)
	require.NoError(t, err)
	require.True(t, report.More, "marks are left after the first budgeted write")
	require.LessOrEqual(t, report.Entries, 2*2, "two marked entries, each with what joins the saved set with it")
	writeBehindTestRestorable(t, ctx, cloud, path, "after the first budgeted write")
	writeBehindTestDrain(t, ctx, cloud, "four stored roots", func(WriteBehindReport) {
		writeBehindTestRestorable(t, ctx, cloud, path, "during the drain")
	})
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "four stored roots, drained")

	// Two classes of two stored entries join, one budgeted write lands,
	// a prune follows, and the rest drains.
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-budget-01"), roots[0], roots[1])
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-budget-23"), roots[2], roots[3])
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "two joined classes")
	writeBehindTestJoin(t, ctx, cloud, testDigest("wb-budget-12"), roots[1], roots[2])
	report, err = cloud.WriteBehind(ctx)
	require.NoError(t, err)
	require.True(t, report.More, "the join's marks take more than one write")
	writeBehindTestRestorable(t, ctx, cloud, path, "midway through a join's marks")
	_, removed, _, err := cloud.removePrunedEdge(ctx, sharedResultID(roots[3]))
	require.NoError(t, err)
	require.True(t, removed)
	writeBehindTestDrain(t, ctx, cloud, "a prune between budgeted writes", func(WriteBehindReport) {
		writeBehindTestRestorable(t, ctx, cloud, path, "draining after the prune")
	})
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "the join and the prune, drained")
}

// A class that dies while a row the store holds still names it, and whose
// number compaction then gives a new class, never has the new class's
// identity in the store under the old rows: the write that takes the new
// class's mark writes again every row that names the number, however many
// marks are left.
func TestWriteBehindBudgetNeverNamesATakenClassNumber(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	cloud.testWriteBehindBudget = 1
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "wb-reuse-old", &transferTestValue{Text: "old"})
	reply, err := cloud.MergeValues(ctx, "cache-r", exportTestBundle(t, actx, a, r))
	require.NoError(t, err)
	old := reply.Imported()[0].ResultID
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored root")

	// Marks ahead of the old root's, so its leaving waits past a write.
	storedPruneTestRoot(t, ctx, cloud, "wb-reuse-ahead-1")
	storedPruneTestRoot(t, ctx, cloud, "wb-reuse-ahead-2")
	_, removed, _, err := cloud.removePrunedEdge(ctx, sharedResultID(old))
	require.NoError(t, err)
	require.True(t, removed)
	_, _, err = cloud.CollectRemoteHoldings(ctx, cloud.ReleaseRemoteCache(ctx, "cache-r").Candidates)
	require.NoError(t, err)
	cloud.egraphMu.RLock()
	require.Nil(t, cloud.resultsByID[sharedResultID(old)], "the old root is collected")
	cloud.egraphMu.RUnlock()
	_, _, err = cloud.CompactEqClasses(ctx)
	require.NoError(t, err)
	storedPruneTestRoot(t, ctx, cloud, "wb-reuse-new")
	cloud.egraphMu.RLock()
	reused := len(cloud.eqClassReuses)
	cloud.egraphMu.RUnlock()
	require.Positive(t, reused, "the new root's classes take numbers the old root's had")

	writeBehindTestDrain(t, ctx, cloud, "a taken class number", func(WriteBehindReport) {
		writeBehindTestRestorable(t, ctx, cloud, path, "draining after a class number is taken")
	})
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a taken class number, drained")
}

// An entry that leaves the store while the stored row of an entry that owned
// it still names it takes that owner's rows with it in the same write, so
// a store written one mark at a time never names an entry it doesn't hold:
// here the dependency's mark comes first, and the root that depended on it
// leaves after it.
func TestWriteBehindBudgetTakesALeavingEntrysOwners(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	cloud.testWriteBehindBudget = 1
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "wb-owner-root", &transferTestValue{Text: "root"})
	p := persistedListTestResult(t, actx, a, asrv, "wb-owner-dep", &transferTestValue{Text: "dep"})
	transferTestDependency(a, actx, r, p)
	reply, err := cloud.MergeValues(ctx, "cache-x", exportTestBundle(t, actx, a, r))
	require.NoError(t, err)
	root := reply.Imported()[0].ResultID
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a stored root with a dependency")
	cloud.egraphMu.RLock()
	var dep uint64
	for id := range cloud.resultsByID[sharedResultID(root)].deps {
		dep = uint64(id)
	}
	cloud.egraphMu.RUnlock()
	require.NotZero(t, dep)

	// The dependency is marked first, then both leave.
	stored, _, err := cloud.SetStoredPart(ctx, dep, storedPruneTestPart("snapshot", map[string]int64{"wb-owner-base": 10}), 0)
	require.NoError(t, err)
	require.True(t, stored)
	_, removed, _, err := cloud.removePrunedEdge(ctx, sharedResultID(root))
	require.NoError(t, err)
	require.True(t, removed)
	_, _, err = cloud.CollectRemoteHoldings(ctx, cloud.ReleaseRemoteCache(ctx, "cache-x").Candidates)
	require.NoError(t, err)
	cloud.egraphMu.RLock()
	require.Nil(t, cloud.resultsByID[sharedResultID(dep)], "the dependency is collected")
	cloud.egraphMu.RUnlock()

	writeBehindTestDrain(t, ctx, cloud, "a dependency leaving before its owner", func(WriteBehindReport) {
		writeBehindTestRestorable(t, ctx, cloud, path, "draining a dependency leaving before its owner")
	})
	writeBehindTestMatchesWholeSave(t, ctx, cloud, "a dependency leaving before its owner, drained")
}
