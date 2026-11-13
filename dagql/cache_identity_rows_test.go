package dagql

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/stretchr/testify/require"
)

// identityTestFetcher answers fetches with a function, counting them.
type identityTestFetcher struct {
	fetch func(numbers []uint64, decode, frames bool) (ValueBundle, []uint64, error)
	calls int
}

func (f *identityTestFetcher) FetchRows(_ context.Context, numbers []uint64, decode, frames bool) (ValueBundle, []uint64, error) {
	f.calls++
	return f.fetch(numbers, decode, frames)
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
	fetcher := &identityTestFetcher{fetch: func([]uint64, bool, bool) (ValueBundle, []uint64, error) {
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
	fetcher := &identityTestFetcher{fetch: func(numbers []uint64, decode, frames bool) (ValueBundle, []uint64, error) {
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
		fetch func([]uint64, bool, bool) (ValueBundle, []uint64, error)
	}{
		{"error", func([]uint64, bool, bool) (ValueBundle, []uint64, error) {
			return ValueBundle{}, nil, errors.New("connection lost")
		}},
		{"missing", func(numbers []uint64, _, _ bool) (ValueBundle, []uint64, error) {
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
	require.Nil(t, b.servableCandidateLocked(pEntry.id, time.Now().Unix(), nil))
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
	b.SetRowFetcher(&identityTestFetcher{fetch: func([]uint64, bool, bool) (ValueBundle, []uint64, error) {
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

// An engine's export reaching an identity-only entry names it as a held
// row, by the Cloud entry's recipe: the engine has no record of it to send,
// and the Cloud has its own.
func TestExportNamesAnIdentityEntryAsAHeldRow(t *testing.T) {
	t.Parallel()
	bctx, b, _, rEntry, pEntry, ctx, cloud, p := identityTestEngine(t)
	bundle := exportTestBundle(t, bctx, b, Result[Typed]{shared: rEntry})
	pRecipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, p))
	_ = ctx
	var held, full int
	for _, value := range bundle.Values {
		switch {
		case value.SenderNumber == uint64(pEntry.id):
			require.Equal(t, pRecipe, value.Held, "P goes as a held row of the Cloud's recipe")
			held++
		case value.SenderNumber == uint64(rEntry.id):
			require.Empty(t, value.Held)
			full++
		}
	}
	require.Equal(t, 1, held)
	require.Equal(t, 1, full)
}

// Once the Cloud has refused an identity-only row as a stale held row, no
// value of it left there, an export that reaches it is blocked there: the
// engine can't send it in full.
func TestExportBlocksAtARefusedIdentityEntry(t *testing.T) {
	t.Parallel()
	bctx, b, _, rEntry, pEntry, _, _, _ := identityTestEngine(t)
	known := NewKnownRows()
	known.Stale(ExportedRows{Held: []uint64{uint64(pEntry.id)}}, []uint64{uint64(pEntry.id)})
	var bundle *ValueBundle
	err := b.WithExportedValues(bctx, ValueSelection{Roots: []AnyResult{Result[Typed]{shared: rEntry}}, Known: known}, config.RefConfig{}, func(_ context.Context, values *ExportedValues) error {
		if len(values.Bundle.Values) > 0 {
			bundle = &values.Bundle
		}
		return nil
	})
	require.Nil(t, bundle, "R stays here")
	_ = err
}

// identityTestFrameRow makes the bundle's held row for the Cloud entry
// number a frame row: it carries the Cloud entry's call frame, with its
// references by sender number, and their rows as dependencies.
func identityTestFrameRow(t *testing.T, cloud *Cache, bundle *ValueBundle, number uint64) {
	t.Helper()
	entry := storedBundleTestEntry(cloud, number)
	cloud.egraphMu.RLock()
	frame := entry.loadResultCall()
	cloud.egraphMu.RUnlock()
	require.NotNil(t, frame)
	call, err := normalizeTransferCall(frame)
	require.NoError(t, err)
	for i := range bundle.Values {
		if bundle.Values[i].SenderNumber == number {
			require.NotEmpty(t, bundle.Values[i].Held)
			bundle.Values[i].Record.Call = call
		}
	}
}

// A frame row, a held row that carries its call frame, lands on an engine
// that holds no value of it as an identity-only entry that has the frame: a
// recipe walk reads it with no fetch, and lookups still never serve it.
func TestFrameRowLandsWithItsFrame(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := heldTestChainedCloud(t)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	identityTestFrameRow(t, cloud, &got.Bundle, p)

	bctx, b, _ := transferTestCache(t)
	fetcher := &identityTestFetcher{fetch: func([]uint64, bool, bool) (ValueBundle, []uint64, error) {
		return ValueBundle{}, nil, errors.New("not expected")
	}}
	b.SetRowFetcher(fetcher)
	reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	require.Len(t, reply.Values, 1, "the frame row holds no value")
	rMerged := heldTestMerged(t, reply, heldTestRow(t, got.Bundle, r).Ordinal)
	b.egraphMu.RLock()
	rEntry := b.resultsByID[sharedResultID(rMerged.Number)]
	pEntry := b.resultsByID[sharedResultID(rMerged.Deps[0])]
	require.True(t, pEntry.identityOnly)
	require.NotNil(t, pEntry.loadResultCall(), "it has the frame")
	require.True(t, pEntry.noValueLocked())
	b.egraphMu.RUnlock()

	_, err = Result[Typed]{shared: rEntry}.RecipeID(bctx)
	require.NoError(t, err)
	require.Zero(t, fetcher.calls, "the frame was there")
	require.Zero(t, b.IdentityRowCounts().RecipeWalks)
}

// A frame row that names an identity-only entry the engine already has,
// with no frame, gives it the frame.
func TestFrameRowGivesAnIdentityEntryItsFrame(t *testing.T) {
	t.Parallel()
	bctx, b, _, rEntry, pEntry, ctx, cloud, p := identityTestEngine(t)
	b.egraphMu.RLock()
	require.Nil(t, pEntry.loadResultCall())
	b.egraphMu.RUnlock()
	got, err := cloud.StoredBundle(ctx, []uint64{p}, time.Now().Unix())
	require.NoError(t, err)
	// P's own row as a frame row, not in full.
	for i := range got.Bundle.Values {
		if got.Bundle.Values[i].SenderNumber == p {
			got.Bundle.Values[i] = TransferredValue{Ordinal: got.Bundle.Values[i].Ordinal, SenderNumber: p, Record: PersistedRecord{ResultID: p}, Held: storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, p)), ExpiresAtUnix: got.Bundle.Values[i].ExpiresAtUnix}
		}
	}
	got.Bundle.Outputs = nil
	identityTestFrameRow(t, cloud, &got.Bundle, p)
	_, err = b.MergeFetchedRows(bctx, got.Bundle)
	require.NoError(t, err)
	b.egraphMu.RLock()
	require.True(t, pEntry.identityOnly)
	require.NotNil(t, pEntry.loadResultCall(), "the entry took the frame")
	b.egraphMu.RUnlock()
	_, err = Result[Typed]{shared: rEntry}.RecipeID(bctx)
	require.NoError(t, err)
}
