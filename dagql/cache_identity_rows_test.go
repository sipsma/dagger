package dagql

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// identityTestFetcher answers fetches with a function, counting them.
type identityTestFetcher struct {
	fetch func(numbers []uint64, decode bool) (ValueBundle, []uint64, error)
	calls int
}

func (f *identityTestFetcher) FetchRows(_ context.Context, numbers []uint64, decode bool) (ValueBundle, []uint64, error) {
	f.calls++
	return f.fetch(numbers, decode)
}

// identityTestEngine merges the Cloud's R, whose receiver is P, into a new
// engine that holds no P, so P lands with its identity alone, and returns
// the engine, R's and P's entries there, and the Cloud's numbers for P.
func identityTestEngine(t *testing.T) (context.Context, *Cache, *Server, *sharedResult, *sharedResult, context.Context, *Cache, uint64) {
	t.Helper()
	ctx, cloud, r, p := heldTestChainedCloud(t)
	heldTestHold(t, ctx, cloud, "cache-b", 7, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	bctx, b, bsrv := transferTestCache(t)
	reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	rMerged := heldTestMerged(t, reply, heldTestRow(t, got.Bundle, r).Ordinal)
	require.Len(t, rMerged.Deps, 1)
	b.egraphMu.RLock()
	rEntry := b.resultsByID[sharedResultID(rMerged.Number)]
	pEntry := b.resultsByID[sharedResultID(rMerged.Deps[0])]
	b.egraphMu.RUnlock()
	require.True(t, pEntry.identityOnly)
	return bctx, b, bsrv, rEntry, pEntry, ctx, cloud, p
}

// A lookup never serves an entry an identity row made, by recipe or by
// structure: P's call, asked on the engine, runs.
func TestIdentityEntryIsNeverServed(t *testing.T) {
	t.Parallel()
	bctx, b, bsrv, _, pEntry, _, _, _ := identityTestEngine(t)
	ran := false
	frame := &ResultCall{Kind: ResultCallKindField, Field: "bundle-dep", Type: NewResultCallType((&transferTestValue{}).Type())}
	res, err := b.GetOrInitCall(bctx, "test-session", bsrv, &CallRequest{ResultCall: frame}, func(context.Context) (AnyResult, error) {
		ran = true
		return NewResultForCall(&transferTestValue{Text: "dep"}, frame)
	})
	require.NoError(t, err)
	require.True(t, ran, "the call ran: the identity-only entry wasn't served")
	require.NotEqual(t, pEntry.id, res.cacheSharedResult().id)
	b.egraphMu.RLock()
	defer b.egraphMu.RUnlock()
	require.True(t, pEntry.identityOnly, "the call's own value didn't land on it")
}

// A frame walk that reaches an identity-only entry uses an entry of its
// class that holds a value: R's recipe ID builds on the engine's own P.
func TestIdentityEntryResolvesToALocalValue(t *testing.T) {
	t.Parallel()
	bctx, b, bsrv, rEntry, pEntry, _, _, _ := identityTestEngine(t)
	fetcher := &identityTestFetcher{fetch: func([]uint64, bool) (ValueBundle, []uint64, error) {
		return ValueBundle{}, nil, errors.New("not expected")
	}}
	b.SetRowFetcher(fetcher)
	own := heldTestSessionValue(t, bctx, b, bsrv)
	require.NotEqual(t, uint64(pEntry.id), own)

	id, err := Result[Typed]{shared: rEntry}.RecipeID(bctx)
	require.NoError(t, err)
	require.NotNil(t, id.Receiver(), "the receiver came from the engine's own P")
	require.Zero(t, fetcher.calls)
	counts := b.IdentityRowCounts()
	require.Equal(t, int64(1), counts.RecipeWalks)
	require.Equal(t, int64(1), counts.ResolvedLocally)
}

// With no entry of its class here, an identity-only entry's record is
// fetched by its Cloud number and lands on it, unretained: its dependent
// owns it.
func TestIdentityEntryFetchedFromTheCloud(t *testing.T) {
	t.Parallel()
	bctx, b, _, rEntry, pEntry, ctx, cloud, p := identityTestEngine(t)
	fetcher := &identityTestFetcher{fetch: func(numbers []uint64, decode bool) (ValueBundle, []uint64, error) {
		require.Equal(t, []uint64{p}, numbers)
		got, err := cloud.StoredBundle(ctx, numbers, time.Now().Unix())
		return got.Bundle, nil, err
	}}
	b.SetRowFetcher(fetcher)

	id, err := Result[Typed]{shared: rEntry}.RecipeID(bctx)
	require.NoError(t, err)
	require.NotNil(t, id.Receiver())
	require.Equal(t, 1, fetcher.calls)
	b.egraphMu.RLock()
	defer b.egraphMu.RUnlock()
	require.False(t, pEntry.identityOnly, "the Cloud's record landed on P's entry")
	_, retained := b.persistedEdgesByResult[pEntry.id]
	require.False(t, retained, "a fetched row isn't retained as a root")
	counts := b.IdentityRowCounts()
	require.Equal(t, int64(1), counts.Fetched)
	require.Equal(t, int64(1), counts.FetchedRows)
}

// A fetch that fails, or that the Cloud answers missing, makes the row
// unavailable; nothing lands, and the walk's caller sees errRowUnavailable.
func TestIdentityEntryFetchFailsUnavailable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		fetch func([]uint64, bool) (ValueBundle, []uint64, error)
	}{
		{"error", func([]uint64, bool) (ValueBundle, []uint64, error) {
			return ValueBundle{}, nil, errors.New("connection lost")
		}},
		{"missing", func(numbers []uint64, _ bool) (ValueBundle, []uint64, error) {
			return ValueBundle{}, numbers, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bctx, b, _, rEntry, pEntry, _, _, _ := identityTestEngine(t)
			b.SetRowFetcher(&identityTestFetcher{fetch: tc.fetch})
			_, err := Result[Typed]{shared: rEntry}.RecipeID(bctx)
			require.ErrorIs(t, err, errRowUnavailable)
			b.egraphMu.RLock()
			require.True(t, pEntry.identityOnly)
			b.egraphMu.RUnlock()
			require.Equal(t, int64(1), b.IdentityRowCounts().FetchFailed)
		})
	}
}

// An engine saves an identity-only entry as its recipe and Cloud holding,
// and restores it identity-only: never as an absent value a lookup could
// serve. Its dependent is saved with it.
func TestIdentityEntrySavedAndRestored(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := heldTestChainedCloud(t)
	heldTestHold(t, ctx, cloud, "cache-b", 7, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "b.db")
	bctx, b, _ := persistedListTestCache(t, path)
	reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
	require.NoError(t, err)
	rMerged := heldTestMerged(t, reply, heldTestRow(t, got.Bundle, r).Ordinal)
	require.Len(t, rMerged.Deps, 1)
	require.NoError(t, b.Close(bctx))

	_, b, _ = persistedListTestCache(t, path)
	require.Equal(t, CachePersistenceResetNone, b.PersistenceResetReason())
	pRecipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, p))
	b.egraphMu.RLock()
	defer b.egraphMu.RUnlock()
	rEntry := b.resultsByID[sharedResultID(rMerged.Number)]
	require.NotNil(t, rEntry, "the root came back")
	pEntry := b.resultsByID[sharedResultID(rMerged.Deps[0])]
	require.NotNil(t, pEntry)
	require.Contains(t, rEntry.deps, pEntry.id)
	require.True(t, pEntry.identityOnly)
	require.Equal(t, pRecipe, pEntry.identityRecipe)
	require.Nil(t, b.servableCandidateLocked(pEntry.id, time.Now().Unix()))
	key, h := pEntry.cloudHoldingLocked()
	require.NotNil(t, h)
	require.Equal(t, p, key.Number)
	require.Equal(t, pEntry.id, b.entriesByRecipe[pRecipe])
}

// A hit whose value needs a row the engine holds only by identity, which
// can be neither found here nor fetched, becomes a miss: the call runs, and
// the miss and the failed fetch are counted.
func TestIdentityEntryUnavailableTurnsAHitIntoAMiss(t *testing.T) {
	t.Parallel()
	bctx, b, bsrv, rEntry, pEntry, _, _, _ := identityTestEngine(t)
	b.SetRowFetcher(&identityTestFetcher{fetch: func([]uint64, bool) (ValueBundle, []uint64, error) {
		return ValueBundle{}, nil, errors.New("connection lost")
	}})
	// R's value names P as a row its decode loads.
	transferDecodeHooks.Store(uint64(rEntry.id), func(ctx context.Context, dec *PersistDecodeContext, _ *transferTestValue) error {
		_, err := dec.ResultRef(ctx, uint64(pEntry.id))
		return err
	})
	t.Cleanup(func() { transferDecodeHooks.Delete(uint64(rEntry.id)) })

	ran := false
	frame := &ResultCall{Kind: ResultCallKindField, Field: "held-chained", Type: NewResultCallType((&transferTestValue{}).Type()), Receiver: &ResultCallRef{ResultID: uint64(pEntry.id)}}
	ev := &CacheDecision{}
	_, err := b.GetOrInitCall(bctx, "test-session", bsrv, &CallRequest{ResultCall: frame, CacheEvidence: ev}, func(context.Context) (AnyResult, error) {
		ran = true
		return NewResultForCall(&transferTestValue{Text: "chained"}, frame)
	})
	require.NoError(t, err, "the pipeline doesn't fail")
	require.True(t, ran, "the hit became a miss and the call ran")
	require.True(t, ev.MissRowUnavailable)
	counts := b.IdentityRowCounts()
	require.Equal(t, int64(1), counts.HitsMissed)
	require.Equal(t, int64(1), counts.FetchFailed)
}
