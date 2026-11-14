package dagql

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dagger/dagger/dagql/call"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/opencontainers/go-digest"
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
	held := entry.entryFrameLocked()
	cloud.egraphMu.RUnlock()
	// The Cloud may hold the frame in the entry's stored body.
	frame, err := held.decode()
	require.NoError(t, err)
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

// An entry that stands in for an identity-only one is owned through it, so
// it outlives its own session as long as what needed it does.
func TestIdentityEntryOwnsItsStandIn(t *testing.T) {
	t.Parallel()
	bctx, b, bsrv, rEntry, pEntry, _, _, _ := identityTestEngine(t)
	own := heldTestSessionValue(t, bctx, b, bsrv)
	_, err := Result[Typed]{shared: rEntry}.RecipeID(bctx)
	require.NoError(t, err)
	b.egraphMu.RLock()
	defer b.egraphMu.RUnlock()
	require.Contains(t, pEntry.deps, sharedResultID(own), "the identity-only entry owns its stand-in")
}

// A fetched record lands on the identity-only entry that asked for it, even
// when the recipe's current entry has become another, and stays: its
// dependent owns that entry.
func TestFetchedRowLandsOnTheEntryThatAsked(t *testing.T) {
	t.Parallel()
	bctx, b, _, rEntry, pEntry, ctx, cloud, p := identityTestEngine(t)
	b.SetRowFetcher(&identityTestFetcher{fetch: func(numbers []uint64, _, _ bool) (ValueBundle, []uint64, error) {
		got, err := cloud.StoredBundle(ctx, numbers, time.Now().Unix())
		return got.Bundle, nil, err
	}})
	pRecipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, p))
	b.egraphMu.Lock()
	delete(b.entriesByRecipe, pRecipe) // as when a value computed here retired it
	b.egraphMu.Unlock()
	_, err := Result[Typed]{shared: rEntry}.RecipeID(bctx)
	require.NoError(t, err)
	b.egraphMu.RLock()
	defer b.egraphMu.RUnlock()
	require.False(t, pEntry.identityOnly, "the record landed on the entry that asked")
	require.Equal(t, pEntry, b.resultsByID[pEntry.id])
}

// An export whose shared recipe would carry an identity-only row in full,
// beside a value of its recipe computed here, stays here instead of
// capturing again forever.
func TestExportOfAnIdentityEntryBesideItsRecipesValueEnds(t *testing.T) {
	t.Parallel()
	bctx, b, bsrv, rEntry, _, _, _, _ := identityTestEngine(t)
	own := heldTestSessionValue(t, bctx, b, bsrv)
	b.egraphMu.RLock()
	ownEntry := b.resultsByID[sharedResultID(own)]
	b.egraphMu.RUnlock()
	done := make(chan error, 1)
	go func() {
		done <- b.WithExportedValues(bctx, ValueSelection{Roots: []AnyResult{Result[Typed]{shared: rEntry}, Result[Typed]{shared: ownEntry}}}, config.RefConfig{}, func(context.Context, *ExportedValues) error { return nil })
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the export didn't end")
	}
}

// An identity-only entry with a frame owns its frame's references; when its
// full record lands on it later, it owns each of them still once, not twice.
func TestFillingAFrameRowOwnsItsReferencesOnce(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, _ := heldTestChainedCloud(t)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	rRecipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, r))
	framed, err := cloneValueBundle(got.Bundle)
	require.NoError(t, err)
	for i := range framed.Values {
		if framed.Values[i].SenderNumber == r {
			v := framed.Values[i]
			framed.Values[i] = TransferredValue{Ordinal: v.Ordinal, SenderNumber: r, Record: PersistedRecord{ResultID: r}, Held: rRecipe, DependencyIDs: v.DependencyIDs, ExpiresAtUnix: v.ExpiresAtUnix}
		}
	}
	framed.Outputs = nil
	identityTestFrameRow(t, cloud, &framed, r)

	bctx, b, _ := transferTestCache(t)
	reply, err := b.MergeValues(bctx, cloudCacheID, framed)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	b.egraphMu.RLock()
	rEntry := b.resultsByID[b.entriesByRecipe[rRecipe]]
	require.NotNil(t, rEntry)
	require.True(t, rEntry.identityOnly)
	require.Len(t, rEntry.deps, 1)
	var pID sharedResultID
	for id := range rEntry.deps {
		pID = id
	}
	pEntry := b.resultsByID[pID]
	before := pEntry.incomingOwnershipCount.Load()
	b.egraphMu.RUnlock()

	_, err = b.MergeFetchedRows(bctx, got.Bundle)
	require.NoError(t, err)
	b.egraphMu.RLock()
	defer b.egraphMu.RUnlock()
	require.False(t, rEntry.identityOnly, "R took its record")
	require.Equal(t, before, pEntry.incomingOwnershipCount.Load(), "P is owned by R once, as before")
}

// An identity-only entry that takes a value raises the session requirements
// of the entries already above it: a merged root whose identity-only
// dependency turns out to hold a session's resource is refused to a session
// that hasn't bound it.
func TestFilledIdentityEntryRaisesItsDependentsRequirements(t *testing.T) {
	t.Parallel()
	ctx, a, srv := transferTestCache(t)
	r := persistedListTestResult(t, ctx, a, srv, "req-r", String("r"))
	sessCtx := currentEntryTestSession(ctx, a, "leaf-session")
	handle := cacheTestVolatileSessionResourceHandle("identity-fill")
	require.NoError(t, a.BindSessionResource(sessCtx, "leaf-session", "leaf-session-client", handle, "bound"))
	leaf, err := cacheTestSessionResourceLeaf(sessCtx, handle)
	require.NoError(t, err)
	attached, err := a.AttachResult(sessCtx, "leaf-session", srv, leaf)
	require.NoError(t, err)
	require.NoError(t, a.AddExplicitDependency(sessCtx, r, attached, "test"))
	leafNumber := uint64(attached.cacheSharedResult().id)
	a.egraphMu.RLock()
	leafFrame := attached.cacheSharedResult().loadResultCall()
	a.egraphMu.RUnlock()
	leafRecipe, err := leafFrame.deriveRecipeDigest(a)
	require.NoError(t, err)

	// R with its leaf as an identity row: the engine holds no value of it.
	bundle := exportTestBundle(t, ctx, a, r)
	var outputs []TransferredOutput
	for i := range bundle.Values {
		if bundle.Values[i].SenderNumber == leafNumber {
			v := bundle.Values[i]
			bundle.Values[i] = TransferredValue{Ordinal: v.Ordinal, SenderNumber: v.SenderNumber, Record: PersistedRecord{ResultID: v.SenderNumber}, Held: leafRecipe, ExpiresAtUnix: v.ExpiresAtUnix}
			for _, out := range bundle.Outputs {
				if out.Ordinal != v.Ordinal {
					outputs = append(outputs, out)
				}
			}
			bundle.Outputs = outputs
		}
	}
	bctx, b, bsrv := transferTestCache(t)
	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	rNumber := reply.Imported()[0].ResultID

	// The leaf's record lands on its identity-only entry.
	_, err = b.MergeFetchedRows(bctx, exportTestBundle(t, ctx, a, attached))
	require.NoError(t, err)
	require.NoError(t, cacheRequiredSessionResourcesError(b), "requirements follow the filled graph")
	unbound := currentEntryTestSession(bctx, b, "unbound-session")
	_, err = b.LoadResultByResultID(unbound, "unbound-session", bsrv, rNumber)
	require.ErrorContains(t, err, "has not bound the session resources")
	require.NoError(t, a.ReleaseSession(sessCtx, "leaf-session"))
}

// decodedParentTest is an engine b holding the Cloud's R as a merged record
// whose value and frame both name P, which landed with its identity alone,
// and its own value of P's recipe, computed here: P's stand-in.
type decodedParentTest struct {
	actx     context.Context
	a        *Cache
	p        AnyResult
	bctx     context.Context
	b        *Cache
	bsrv     *Server
	rNumber  uint64
	own      uint64
	identity sharedResultID
}

func newDecodedParentTest(t *testing.T) *decodedParentTest {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	p := persistedListTestResult(t, actx, a, asrv, "bundle-dep", &transferTestValue{Text: "dep"})
	pNumber := uint64(p.cacheSharedResult().id)
	// R's receiver is P too, as a lazy container's saved parent is.
	frame := &ResultCall{Kind: ResultCallKindField, Field: "child-parent", Type: NewResultCallType((&transferTestValue{}).Type()), Receiver: &ResultCallRef{ResultID: pNumber}}
	r, err := a.GetOrInitCall(actx, "test-session", asrv, &CallRequest{ResultCall: frame, IsPersistable: true}, func(context.Context) (AnyResult, error) {
		return NewResultForCall(&transferTestValue{Text: "parent", Child: pNumber}, frame)
	})
	require.NoError(t, err)
	bundle := exportTestBundle(t, actx, a, r)
	pRecipe, err := p.cacheSharedResult().loadResultCall().deriveRecipeDigest(a)
	require.NoError(t, err)
	for i := range bundle.Values {
		if bundle.Values[i].SenderNumber == pNumber {
			v := bundle.Values[i]
			bundle.Values[i] = TransferredValue{Ordinal: v.Ordinal, SenderNumber: v.SenderNumber, Record: PersistedRecord{ResultID: v.SenderNumber}, Held: pRecipe, ExpiresAtUnix: v.ExpiresAtUnix}
		}
	}
	var outputs []TransferredOutput
	for _, out := range bundle.Outputs {
		if out.Ordinal != heldTestRow(t, bundle, pNumber).Ordinal {
			outputs = append(outputs, out)
		}
	}
	bundle.Outputs = outputs

	bctx, b, bsrv := transferTestCache(t)
	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.NoError(t, err)
	rNumber := reply.Imported()[0].ResultID
	own := heldTestSessionValue(t, bctx, b, bsrv)
	b.egraphMu.RLock()
	var identity sharedResultID
	for id := range b.resultsByID[sharedResultID(rNumber)].deps {
		identity = id
	}
	b.egraphMu.RUnlock()
	return &decodedParentTest{actx: actx, a: a, p: p, bctx: bctx, b: b, bsrv: bsrv, rNumber: rNumber, own: own, identity: identity}
}

// decode loads R, whose decode loads its child twice, as a list naming one
// row at two positions does, running between before the second load; the
// value names the row the first load returned.
func (d *decodedParentTest) decode(t *testing.T, between func()) {
	t.Helper()
	transferDecodeHooks.Store(d.rNumber, func(ctx context.Context, dec *PersistDecodeContext, value *transferTestValue) error {
		child, err := dec.ResultRef(ctx, value.Child)
		if err != nil {
			return err
		}
		if between != nil {
			between()
		}
		second, err := dec.ResultRef(ctx, value.Child)
		if err != nil {
			return err
		}
		if between != nil {
			child = second
		}
		value.Child = uint64(child.cacheSharedResult().id)
		return nil
	})
	t.Cleanup(func() { transferDecodeHooks.Delete(d.rNumber) })
	_, err := d.b.LoadResultByResultID(d.bctx, "", d.bsrv, d.rNumber)
	require.NoError(t, err)
}

// exportsWhole checks that R's export carries nothing held.
func (d *decodedParentTest) exportsWhole(t *testing.T) ValueBundle {
	t.Helper()
	d.b.egraphMu.RLock()
	rEntry := d.b.resultsByID[sharedResultID(d.rNumber)]
	d.b.egraphMu.RUnlock()
	exported := exportTestBundle(t, d.bctx, d.b, Result[Typed]{shared: rEntry})
	require.NotEmpty(t, exported.Values, "R exports")
	for _, value := range exported.Values {
		require.Empty(t, value.Held, "nothing held of the child's recipe")
	}
	return exported
}

// A value that names an identity-only child, decoded through a local stand-in
// of the child's recipe, depends on the stand-in from then on, not on the
// identity-only entry, so an export of the live value carries the stand-in
// and nothing held of the same recipe.
func TestDecodedParentExportsItsStandIn(t *testing.T) {
	t.Parallel()
	d := newDecodedParentTest(t)
	d.decode(t, nil)
	d.b.egraphMu.RLock()
	rEntry := d.b.resultsByID[sharedResultID(d.rNumber)]
	require.Contains(t, rEntry.deps, sharedResultID(d.own), "R owns the stand-in")
	require.Len(t, rEntry.deps, 1, "and no longer the identity-only entry")
	d.b.egraphMu.RUnlock()
	require.Len(t, d.exportsWhole(t).Values, 2)
}

// The same with a stand-in whose frame hasn't derived its recipe yet, as
// after a clone: settlement derives it.
func TestDecodedParentExportsAStandInWithNoRecipeDerived(t *testing.T) {
	t.Parallel()
	d := newDecodedParentTest(t)
	d.b.egraphMu.Lock()
	ownEntry := d.b.resultsByID[sharedResultID(d.own)]
	ownEntry.storeResultCall(ownEntry.loadResultCall().clone())
	d.b.egraphMu.Unlock()
	d.decode(t, nil)
	d.b.egraphMu.RLock()
	require.Len(t, d.b.resultsByID[sharedResultID(d.rNumber)].deps, 1, "R settled onto the stand-in")
	d.b.egraphMu.RUnlock()
	d.exportsWhole(t)
}

// A fetch that fills the identity-only entry between two loads of one
// decode leaves the value naming that entry: it stays R's dependency.
func TestDecodedParentKeepsAChildFilledMidDecode(t *testing.T) {
	t.Parallel()
	d := newDecodedParentTest(t)
	pNumber := uint64(d.p.cacheSharedResult().id)
	d.decode(t, func() {
		_, err := d.b.mergeFetchedRowsInto(d.bctx, exportTestBundle(t, d.actx, d.a, d.p), map[uint64]sharedResultID{pNumber: d.identity})
		require.NoError(t, err)
	})
	d.b.egraphMu.RLock()
	rEntry := d.b.resultsByID[sharedResultID(d.rNumber)]
	require.Contains(t, rEntry.deps, d.identity, "the value names the filled entry, which R keeps")
	require.False(t, d.b.resultsByID[d.identity].identityOnly)
	d.b.egraphMu.RUnlock()
	d.exportsWhole(t)
}

// A decode that fails after resolving a stand-in leaves no substitution
// behind: the retry, with the stand-in gone and the row fetched, keeps the
// row its installed value names.
func TestDecodeRetryKeepsTheRowItsValueNames(t *testing.T) {
	t.Parallel()
	d := newDecodedParentTest(t)
	pNumber := uint64(d.p.cacheSharedResult().id)
	failed := false
	transferDecodeHooks.Store(d.rNumber, func(ctx context.Context, dec *PersistDecodeContext, value *transferTestValue) error {
		child, err := dec.ResultRef(ctx, value.Child)
		if err != nil {
			return err
		}
		if !failed {
			failed = true
			return errors.New("first attempt fails after resolving its child")
		}
		value.Child = uint64(child.cacheSharedResult().id)
		return nil
	})
	t.Cleanup(func() { transferDecodeHooks.Delete(d.rNumber) })
	_, err := d.b.LoadResultByResultID(d.bctx, "", d.bsrv, d.rNumber)
	require.Error(t, err)

	// The stand-in goes; the retry fetches the row itself.
	d.b.egraphMu.RLock()
	ownEntry := d.b.resultsByID[sharedResultID(d.own)]
	d.b.egraphMu.RUnlock()
	mergeTestExpire(d.b, ownEntry)
	d.b.SetRowFetcher(&identityTestFetcher{fetch: func(numbers []uint64, _, _ bool) (ValueBundle, []uint64, error) {
		require.Equal(t, []uint64{pNumber}, numbers)
		return exportTestBundle(t, d.actx, d.a, d.p), nil, nil
	}})
	_, err = d.b.LoadResultByResultID(d.bctx, "", d.bsrv, d.rNumber)
	require.NoError(t, err)
	d.b.egraphMu.RLock()
	require.Contains(t, d.b.resultsByID[sharedResultID(d.rNumber)].deps, d.identity, "R keeps the row its value names")
	d.b.egraphMu.RUnlock()
	d.exportsWhole(t)
}

// A stand-in of another recipe, in the class only through a shared content
// digest, serves the decode, but R keeps the identity-only entry its frame
// names, and its frame and recipe index stay as they were.
func TestStandInOfAnotherRecipeLeavesTheFrame(t *testing.T) {
	t.Parallel()
	d := newDecodedParentTest(t)
	content := digest.FromString("dep content")
	d.b.egraphMu.Lock()
	identityEntry := d.b.resultsByID[d.identity]
	d.b.teachHeldRowDigestsLocked(d.bctx, identityEntry, identityEntry.identityRecipe, []call.ExtraDigest{{Digest: content, Label: call.ExtraDigestLabelContent}})
	ownEntry := d.b.resultsByID[sharedResultID(d.own)]
	d.b.egraphMu.Unlock()
	mergeTestExpire(d.b, ownEntry)
	// Another recipe, the same content.
	frame := &ResultCall{Kind: ResultCallKindField, Field: "bundle-dep-elsewhere", Type: NewResultCallType((&transferTestValue{}).Type())}
	other, err := d.b.GetOrInitCall(d.bctx, "test-session", d.bsrv, &CallRequest{ResultCall: frame}, func(context.Context) (AnyResult, error) {
		return NewResultForCall(&transferTestValue{Text: "dep"}, frame)
	})
	require.NoError(t, err)
	require.NoError(t, d.b.TeachContentDigest(d.bctx, other, content))
	d.b.egraphMu.RLock()
	rEntry := d.b.resultsByID[sharedResultID(d.rNumber)]
	rFrame := rEntry.loadResultCall()
	d.b.egraphMu.RUnlock()
	rRecipe, err := rFrame.deriveRecipeDigest(d.b)
	require.NoError(t, err)

	d.decode(t, nil)
	d.b.egraphMu.RLock()
	require.Contains(t, rEntry.deps, d.identity, "R keeps the identity-only entry its frame names")
	require.Contains(t, rEntry.deps, other.cacheSharedResult().id, "and owns the stand-in its value names")
	after := rEntry.loadResultCall()
	d.b.egraphMu.RUnlock()
	got, err := after.deriveRecipeDigest(d.b)
	require.NoError(t, err)
	require.Equal(t, rRecipe, got, "R's recipe is unchanged")
}

// A value that keeps naming the identity-only entry after its decode loaded
// the stand-in, as a transferred container keeps its record's payload, keeps
// that entry: settlement goes by what the value's encoding names, not by
// what its decode loaded, so nothing the value names is collected.
func TestDecodedParentKeepsTheRowItsPayloadStillNames(t *testing.T) {
	t.Parallel()
	d := newDecodedParentTest(t)
	transferDecodeHooks.Store(d.rNumber, func(ctx context.Context, dec *PersistDecodeContext, value *transferTestValue) error {
		child, err := dec.ResultRef(ctx, value.Child)
		if err != nil {
			return err
		}
		require.Equal(t, sharedResultID(d.own), child.cacheSharedResult().id, "the decode loads the stand-in")
		// value.Child stays the record's: the payload is kept as it came.
		return nil
	})
	t.Cleanup(func() { transferDecodeHooks.Delete(d.rNumber) })
	_, err := d.b.LoadResultByResultID(d.bctx, "", d.bsrv, d.rNumber)
	require.NoError(t, err)
	d.b.egraphMu.RLock()
	defer d.b.egraphMu.RUnlock()
	rEntry := d.b.resultsByID[sharedResultID(d.rNumber)]
	require.Contains(t, rEntry.deps, d.identity, "R keeps the entry its payload names")
	require.NotNil(t, d.b.resultsByID[d.identity], "which stays on the engine")
	require.Contains(t, rEntry.deps, sharedResultID(d.own), "and owns the stand-in its live value uses")
}

// A decoded value that can't be encoded keeps every row: settlement can't
// tell what it names.
func TestDecodedParentKeepsItsRowsWhenItsValueCannotBeEncoded(t *testing.T) {
	t.Parallel()
	d := newDecodedParentTest(t)
	transferDecodeHooks.Store(d.rNumber, func(ctx context.Context, dec *PersistDecodeContext, value *transferTestValue) error {
		child, err := dec.ResultRef(ctx, value.Child)
		if err != nil {
			return err
		}
		value.Child = uint64(child.cacheSharedResult().id)
		value.busy.Store(true)
		return nil
	})
	t.Cleanup(func() { transferDecodeHooks.Delete(d.rNumber) })
	_, err := d.b.LoadResultByResultID(d.bctx, "", d.bsrv, d.rNumber)
	require.NoError(t, err)
	d.b.egraphMu.RLock()
	defer d.b.egraphMu.RUnlock()
	rEntry := d.b.resultsByID[sharedResultID(d.rNumber)]
	require.Contains(t, rEntry.deps, d.identity, "R keeps the identity-only entry")
	require.Contains(t, rEntry.deps, sharedResultID(d.own), "and the stand-in")
}

// Every frame walk reads a referenced entry's frame the same way, so each
// one that reaches an identity-only entry resolves it as a recipe walk does,
// rather than failing for a frame the entry never had: a session's ID load
// walking R's frame (WalkResultCall), and the module IDs of R's call.
func TestFrameWalksResolveAnIdentityEntry(t *testing.T) {
	t.Parallel()
	bctx, b, bsrv, rEntry, pEntry, _, _, _ := identityTestEngine(t)
	b.SetRowFetcher(&identityTestFetcher{fetch: func([]uint64, bool, bool) (ValueBundle, []uint64, error) {
		return ValueBundle{}, nil, errors.New("not expected")
	}})
	own := heldTestSessionValue(t, bctx, b, bsrv)
	b.egraphMu.RLock()
	ownFrame := b.resultsByID[sharedResultID(own)].loadResultCall()
	b.egraphMu.RUnlock()

	var reached *ResultCall
	require.NoError(t, b.WalkResultCall(rEntry.loadResultCall(), func(ref *ResultCallRef, frame *ResultCall) error {
		if ref.ResultID == uint64(pEntry.id) {
			reached = frame
		}
		return nil
	}))
	require.NotNil(t, reached, "the walk reached P")
	require.Equal(t, ownFrame.Field, reached.Field, "through the engine's own P")
	_, _, err := b.moduleResultIDs(rEntry.loadResultCall(), newModuleIDsRebuild(bctx, 1))
	require.NoError(t, err)
}
