package dagql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A walk's count gives its calls, the entries they read, the most one read,
// and the power of two the median call read at most.
func TestAWalkCountReadsItsMedianAndMost(t *testing.T) {
	t.Parallel()
	var w walkCount
	require.Equal(t, WalkCount{}, w.read())
	for _, n := range []int{0, 1, 3, 3, 900} {
		w.add(n)
	}
	require.Equal(t, WalkCount{Calls: 5, Entries: 907, Most: 900, Median: 4, OverOne: 3}, w.read())
}

// The walks one run per stored record makes are counted: a merge's commit
// walks under what it installed, an adoption's check walks over the entry,
// and a bundle's build counts its walks.
func TestTheRunWalksAreCounted(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, r, p := replaceTestRun(t, ctx, cloud, "cache-a")
	require.NotZero(t, cloud.RunWalkStats().RecipesUnder.Calls, "the merge's commit walked")
	got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: p, Run: RunName{Cache: "cache-b", Number: 9}, Part: testLiveOffer()}})
	require.NoError(t, err)
	require.True(t, got[0].Stored)
	over := cloud.RunWalkStats().EngineRanOver
	require.NotZero(t, over.Calls, "adoption asked what is over the entry")
	require.GreaterOrEqual(t, over.Most, uint64(1), "A's root is over A's dep")
	_, err = cloud.StoredBundle(ctx, []uint64{r}, 0, &HeldBy{Cache: "cache-fresh"})
	require.NoError(t, err)
	require.Equal(t, uint64(1), cloud.RunWalkStats().BundleWalks.Most)
	cloud.NoteCacheGone("cache-a")
	gone := cloud.RunWalkStats().CacheGone
	require.Equal(t, uint64(1), gone.Calls)
	require.Equal(t, uint64(2), gone.Most, "cache-a's two entries")
}
