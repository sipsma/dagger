package dagql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A recipient's holding whose closure names a dependency no holding of its
// cache has goes in full under HeldBy.Whole, its closure walked, and the
// bundle reports it; without Whole, or with a holding whose closure the
// Cloud knows whole, it goes as a held row, as before.
func TestStoredBundleWholeSendsAnIncompleteHoldingInFull(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := storedBundleTestCloud(t, "root")
	now := time.Now().Unix()
	entry := storedBundleTestEntry(cloud, p)
	cloud.egraphMu.RLock()
	recipe := entry.recipeKeys[0]
	cloud.egraphMu.RUnlock()
	// cache-b's holding of P depends on its number 99, which no holding of
	// cache-b has; cache-c's has no dependency.
	_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-b", Number: 7}, RemoteHolding{Recipe: recipe, Request: recipe, Field: entry.description, Deps: []uint64{99}})
	require.NoError(t, err)
	heldTestHold(t, ctx, cloud, "cache-c", 8, p, 0)

	got, err := cloud.StoredBundle(ctx, []uint64{r}, now, &HeldBy{Cache: "cache-b"})
	require.NoError(t, err)
	require.Equal(t, []uint64{p}, got.Held, "held without Whole")
	require.Empty(t, got.Incomplete)

	got, err = cloud.StoredBundle(ctx, []uint64{r}, now, &HeldBy{Cache: "cache-b", Whole: true})
	require.NoError(t, err)
	require.Empty(t, got.Held, "in full under Whole")
	require.Equal(t, []uint64{p}, got.Incomplete)
	require.Empty(t, heldTestRow(t, got.Bundle, p).Held)

	got, err = cloud.StoredBundle(ctx, []uint64{r}, now, &HeldBy{Cache: "cache-c", Whole: true})
	require.NoError(t, err)
	require.Equal(t, []uint64{p}, got.Held, "a holding whose closure the Cloud knows whole stays held")
	require.Empty(t, got.Incomplete)
}

// Whole's answer through a cycle of holdings is the one a walk from each
// member gives: holdings 1 and 2 depend on each other, as replacements
// observed out of order can leave them for a while, and 1 also on 3, which
// names a dependency (4) no holding of the cache has. Each of the three has
// an unknown dependency below it, whichever is asked first: a member reached
// again while its own walk is in progress gives a partial answer, which isn't
// kept.
func TestStoredBundleWholeThroughACycle(t *testing.T) {
	t.Parallel()
	for _, first := range []uint64{1, 2, 3} {
		cloud := newCloudCache(t)
		a := newCloudApplier(t, cloud)
		engine := CacheID("cache-a")
		a.call(callRow{key: HolderKey{engine, 1}, session: "s", holding: holdingOf("a", 2, 3)})
		a.call(callRow{key: HolderKey{engine, 2}, session: "s", holding: holdingOf("b", 1)})
		a.call(callRow{key: HolderKey{engine, 3}, session: "s", holding: holdingOf("d", 4)})
		require.Empty(t, a.collect())
		cloud.egraphMu.RLock()
		known := map[HolderKey]bool{}
		order := []uint64{first}
		for _, n := range []uint64{1, 2, 3} {
			if n != first {
				order = append(order, n)
			}
		}
		for _, n := range order {
			res, _ := cloud.holdingLocked(HolderKey{engine, n})
			require.NotNil(t, res)
			require.True(t, cloud.holdingsUnknownLocked(engine, res, known), "holding %d, %d asked first", n, first)
		}
		cloud.egraphMu.RUnlock()
	}
}
