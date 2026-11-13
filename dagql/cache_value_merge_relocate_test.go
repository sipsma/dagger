package dagql

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// mergeTestMovingStream returns two bundles of one source cache: a child's,
// and a root's that holds the child too. Merged in that order, the root's
// child lands on the entry the first merge made, so the root's record has to
// be renumbered: its references move.
func mergeTestMovingStream(t *testing.T) (child, root ValueBundle) {
	t.Helper()
	ctx, a, srv := transferTestCache(t)
	c := persistedListTestResult(t, ctx, a, srv, "moved-child", String("child"))
	r := persistedListTestResult(t, ctx, a, srv, "moved-root", DynamicResultArrayOutput{Elem: String(""), Values: []AnyResult{c}})
	child = exportTestBundle(t, ctx, a, c)
	root = exportTestBundle(t, ctx, a, r)
	require.Len(t, root.Values, 2)
	return child, root
}

// mergeTestState is a cache's entries as merge leaves them: each entry's
// number, record and dependencies, in number order.
func mergeTestState(t *testing.T, c *Cache) []string {
	t.Helper()
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	var out []string
	for id, res := range c.resultsByID {
		var deps []uint64
		for dep := range res.deps {
			deps = append(deps, uint64(dep))
		}
		slices.Sort(deps)
		envelope, err := json.Marshal(res.persistedEnvelope)
		require.NoError(t, err)
		call, err := json.Marshal(res.loadResultCall())
		require.NoError(t, err)
		var stored string
		if res.stored != nil {
			stored = fmt.Sprintf(" stored=%x/%x", res.stored.envelope, res.stored.frame)
		}
		out = append(out, fmt.Sprintf("%d deps=%v call=%s envelope=%s%s", id, deps, call, envelope, stored))
	}
	slices.Sort(out)
	return out
}

// mergeTestStream merges the bundles in turn into a new engine cache, set up
// by setup, and returns its state.
func mergeTestStream(t *testing.T, setup func(*Cache), bundles ...ValueBundle) []string {
	t.Helper()
	ctx, c, _ := transferTestCache(t)
	return mergeTestStreamInto(t, ctx, c, cloudCacheID, setup, bundles...)
}

// mergeTestStreamInto merges the bundles in turn, from the cache from, into
// c, set up by setup, and returns its state.
func mergeTestStreamInto(t *testing.T, ctx context.Context, c *Cache, from CacheID, setup func(*Cache), bundles ...ValueBundle) []string {
	t.Helper()
	if setup != nil {
		setup(c)
	}
	for _, bundle := range bundles {
		reply, err := c.MergeValues(ctx, from, bundle)
		require.NoError(t, err)
		require.True(t, reply.Committed)
	}
	return mergeTestState(t, c)
}

// A merge renumbers its records before its commit takes the graph lock, so
// the commit, which holds the lock every check's calls wait on, doesn't
// decode and encode them again.
func TestMergeValuesRelocatesBeforeTheGraphLock(t *testing.T) {
	t.Parallel()
	child, root := mergeTestMovingStream(t)
	var under []uint64
	mergeTestStream(t, func(c *Cache) {
		c.testRelocatedUnderLock = func(ordinal uint64) { under = append(under, ordinal) }
	}, child, root)
	require.Empty(t, under, "no record is relocated under the graph lock")

	// Without the relocation before the lock, the root's record is
	// relocated under it: the stream does move a record.
	under = nil
	mergeTestStream(t, func(c *Cache) {
		c.testNoPrerelocation = true
		c.testRelocatedUnderLock = func(ordinal uint64) { under = append(under, ordinal) }
	}, child, root)
	require.Len(t, under, 1)
}

// Relocating before the lock leaves the cache as relocating under it does:
// the same entries, records and dependencies.
func TestMergeValuesRelocationBeforeTheLockLeavesTheSameCache(t *testing.T) {
	t.Parallel()
	child, root := mergeTestMovingStream(t)
	want := mergeTestStream(t, func(c *Cache) { c.testNoPrerelocation = true }, child, root)
	require.Equal(t, want, mergeTestStream(t, nil, child, root))
}

// A record relocated before the lock with a number the commit doesn't decide
// is relocated again under the lock, so the cache ends as if nothing had been
// relocated before it.
func TestMergeValuesRelocatesAgainWhenANumberChanged(t *testing.T) {
	t.Parallel()
	child, root := mergeTestMovingStream(t)
	want := mergeTestStream(t, func(c *Cache) { c.testNoPrerelocation = true }, child, root)
	var under []uint64
	got := mergeTestStream(t, func(c *Cache) {
		c.testPrerelocated = func(used map[uint64]map[uint64]sharedResultID) {
			for _, numbers := range used {
				for provisional := range numbers {
					numbers[provisional] += 1000
				}
			}
		}
		c.testRelocatedUnderLock = func(ordinal uint64) { under = append(under, ordinal) }
	}, child, root)
	require.Equal(t, want, got)
	require.Len(t, under, 1, "the record whose numbers no longer hold is relocated under the lock")
}

// A record whose object payload names its child is relocated before the lock
// too, and lands as relocating under the lock would land it: the payload's
// own reference is renumbered, not only its call's.
func TestMergeValuesRelocatesAnObjectPayloadBeforeTheLock(t *testing.T) {
	t.Parallel()
	ctx, a, srv := transferTestCache(t)
	c := persistedListTestResult(t, ctx, a, srv, "object-child", &transferTestValue{Text: "child"})
	r := persistedListTestResult(t, ctx, a, srv, "object-root", &transferTestValue{Text: "root", Child: rowNumber(c)})
	transferTestDependency(a, ctx, r, c)
	child, root := exportTestBundle(t, ctx, a, c), exportTestBundle(t, ctx, a, r)
	require.Len(t, root.Values, 2)

	want := mergeTestStream(t, func(c *Cache) { c.testNoPrerelocation = true }, child, root)
	var under []uint64
	got := mergeTestStream(t, func(c *Cache) {
		c.testRelocatedUnderLock = func(ordinal uint64) { under = append(under, ordinal) }
	}, child, root)
	require.Equal(t, want, got)
	require.Empty(t, under, "the object's record is relocated before the lock")
}

// When the cache changes between the merge's decision before the lock and
// its commit, so that a record lands on another entry than the early
// decision gave it, the commit relocates it under the lock and the cache
// ends as if nothing had been relocated early. Here the early decision
// creates the child, so the root keeps its numbers; before the commit the
// cache takes the child from another merge, so the root lands on that entry.
func TestMergeValuesRelocatesAgainWhenTheCacheChanged(t *testing.T) {
	t.Parallel()
	child, root := mergeTestMovingStream(t)
	setup := func(noEarly bool, under *[]uint64) func(*Cache) {
		return func(c *Cache) {
			c.testNoPrerelocation = noEarly
			ctx := cacheTestContext(t.Context())
			once := false
			c.testBeforeTransferCommit = func() {
				if once {
					return
				}
				once = true
				_, err := c.MergeValues(ctx, cloudCacheID, child)
				require.NoError(t, err)
			}
			c.testRelocatedUnderLock = func(ordinal uint64) { *under = append(*under, ordinal) }
		}
	}
	var wantUnder, under []uint64
	want := mergeTestStream(t, setup(true, &wantUnder), root)
	got := mergeTestStream(t, setup(false, &under), root)
	require.Equal(t, want, got)
	require.Len(t, wantUnder, 1, "the root moves onto the child the cache took")
	require.Len(t, under, 1, "the root, not relocated early, is relocated under the lock")
}

// On the Cloud's blob-backed cache, a merge also relocates before the lock
// and leaves the same entries and records as relocating under it. The body
// worker, which compresses records in its own time, is held off, so both
// caches are read as merge left them.
func TestMergeValuesRelocationBeforeTheLockOnABlobBackedCache(t *testing.T) {
	t.Parallel()
	child, root := mergeTestMovingStream(t)
	noBodies := func(c *Cache) {
		w := c.storedBodies
		c.storedBodies = nil
		t.Cleanup(func() { c.storedBodies = w })
	}
	ctx, want := storedPartTestCache(t, WithBlobStore())
	wantState := mergeTestStreamInto(t, ctx, want, "cache-a", func(c *Cache) {
		noBodies(c)
		c.testNoPrerelocation = true
	}, child, root)
	var under []uint64
	ctx, got := storedPartTestCache(t, WithBlobStore())
	gotState := mergeTestStreamInto(t, ctx, got, "cache-a", func(c *Cache) {
		noBodies(c)
		c.testRelocatedUnderLock = func(ordinal uint64) { under = append(under, ordinal) }
	}, child, root)
	require.Equal(t, wantState, gotState)
	require.Empty(t, under, "no record is relocated under the lock")
}
