package dagql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A fetch's answer carries each entry asked as a root, with its record, and
// the entries it references as identity rows. Decoding, it carries the
// entry's child rows too, followed down, as records: here P's dependency Q,
// which P's call doesn't reference. An entry the Cloud doesn't have is
// missing.
func TestStoredRowsCarriesTheRowsAskedAndTheirChildren(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p, q := storedBundleTestChain(t)

	got, missing, err := cloud.StoredRows(ctx, "", []uint64{p, 12345}, time.Now().Unix(), false, false)
	require.NoError(t, err)
	require.Equal(t, []uint64{12345}, missing)
	rows := storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 2)
	require.Empty(t, rows[p].Held, "P as its record")
	require.NotEmpty(t, rows[q].Held, "Q as an identity row")
	require.NotContains(t, rows, r, "nothing above P")
	require.Len(t, got.Bundle.Roots, 1)

	got, missing, err = cloud.StoredRows(ctx, "", []uint64{r}, time.Now().Unix(), true, false)
	require.NoError(t, err)
	require.Empty(t, missing)
	rows = storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 3)
	for _, number := range []uint64{r, p, q} {
		require.Empty(t, rows[number].Held, "entry %d travels as its record", number)
	}
	require.Len(t, got.Bundle.Roots, 1, "only the entry asked is a root")
}

// Decoding, an entry's call references stay identity rows: here R's call
// takes P as its receiver, so P isn't a child row of R's value.
func TestStoredRowsLeavesCallReferencesAsIdentityRows(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := heldTestChainedCloud(t)
	got, _, err := cloud.StoredRows(ctx, "", []uint64{r}, time.Now().Unix(), true, false)
	require.NoError(t, err)
	rows := storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 2)
	require.Empty(t, rows[r].Held)
	require.NotEmpty(t, rows[p].Held, "R's receiver as an identity row")
}

// storedRowsTestCallChain merges an export of R, whose call takes P as its
// receiver, whose call takes Q as its receiver, into a new Cloud cache, and
// returns the Cloud's numbers for R, P and Q.
func storedRowsTestCallChain(t *testing.T) (context.Context, *Cache, uint64, uint64, uint64) {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	q := persistedListTestResult(t, actx, a, asrv, "call-chain-leaf", &transferTestValue{Text: "leaf"})
	on := func(field string, receiver AnyResult) AnyResult {
		frame := &ResultCall{Kind: ResultCallKindField, Field: field, Type: NewResultCallType((&transferTestValue{}).Type()), Receiver: &ResultCallRef{ResultID: uint64(receiver.cacheSharedResult().id)}}
		res, err := a.GetOrInitCall(actx, "test-session", asrv, &CallRequest{ResultCall: frame, IsPersistable: true}, func(context.Context) (AnyResult, error) {
			return NewResultForCall(&transferTestValue{Text: field}, frame)
		})
		require.NoError(t, err)
		return res
	}
	p := on("call-chain-mid", q)
	r := on("call-chain-top", p)
	bundle := exportTestBundle(t, actx, a, r)
	require.Len(t, bundle.Values, 3, "R, its receiver P and P's receiver Q")
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	numbers := map[uint64]uint64{}
	for _, value := range reply.Values {
		for _, sent := range bundle.Values {
			if sent.Ordinal == value.Ordinal {
				numbers[sent.SenderNumber] = value.Number
			}
		}
	}
	rn, pn, qn := numbers[uint64(r.cacheSharedResult().id)], numbers[uint64(p.cacheSharedResult().id)], numbers[uint64(q.cacheSharedResult().id)]
	require.NotZero(t, rn)
	require.NotZero(t, pn)
	require.NotZero(t, qn)
	return ctx, cloud, rn, pn, qn
}

// With frames, the rows an entry's call references, followed down, travel as
// frame rows: identity rows that carry their call frames and depend on the
// rows those reference. An engine merging the answer walks the entry's
// recipe without fetching anything. Without frames, only the receiver
// travels, as a plain identity row.
func TestStoredRowsWithFramesCarriesTheCallClosureAsFrameRows(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p, q := storedRowsTestCallChain(t)

	got, _, err := cloud.StoredRows(ctx, "", []uint64{r}, time.Now().Unix(), false, false)
	require.NoError(t, err)
	rows := storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 2, "R and its receiver P")
	require.Nil(t, rows[p].Record.Call, "P as a plain identity row")

	got, missing, err := cloud.StoredRows(ctx, "", []uint64{r}, time.Now().Unix(), false, true)
	require.NoError(t, err)
	require.Empty(t, missing)
	rows = storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 3)
	require.Empty(t, rows[r].Held, "R as its record")
	for _, number := range []uint64{p, q} {
		require.NotEmpty(t, rows[number].Held, "entry %d as an identity row", number)
		require.NotNil(t, rows[number].Record.Call, "entry %d with its call frame", number)
	}
	require.Equal(t, []uint64{uint64(rows[q].Ordinal)}, rows[p].DependencyIDs, "P's frame row depends on its receiver's")
	require.Empty(t, rows[q].DependencyIDs)
	require.Len(t, got.Bundle.Roots, 1)

	bctx, b, _ := transferTestCache(t)
	fetcher := &identityTestFetcher{fetch: func([]uint64, bool, bool) (ValueBundle, []uint64, error) {
		return ValueBundle{}, nil, errors.New("not expected")
	}}
	b.SetRowFetcher(fetcher)
	reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	require.Len(t, reply.Values, 1, "the frame rows hold no value")
	rMerged := heldTestMerged(t, reply, rows[r].Ordinal)
	b.egraphMu.RLock()
	rEntry := b.resultsByID[sharedResultID(rMerged.Number)]
	b.egraphMu.RUnlock()
	_, err = Result[Typed]{shared: rEntry}.RecipeID(bctx)
	require.NoError(t, err)
	require.Zero(t, fetcher.calls, "every frame the walk reads was there")
}

// Decoding, an entry its value names as a child travels as its record though
// its call takes it too: here R's call takes P as its receiver, and R's value,
// a list, holds P, as a lazy container holds its parent.
func TestStoredRowsCarriesAChildItsCallAlsoTakes(t *testing.T) {
	t.Parallel()
	actx, a, asrv := transferTestCache(t)
	p := persistedListTestResult(t, actx, a, asrv, "overlap-child", &transferTestValue{Text: "child"})
	value := DynamicResultArrayOutput{Elem: &transferTestValue{}, Values: []AnyResult{p}}
	frame := &ResultCall{Kind: ResultCallKindField, Field: "overlap-list", Type: NewResultCallType(value.Type()), Receiver: &ResultCallRef{ResultID: uint64(p.cacheSharedResult().id)}}
	r, err := a.GetOrInitCall(actx, "test-session", asrv, &CallRequest{ResultCall: frame, IsPersistable: true}, func(context.Context) (AnyResult, error) {
		return NewResultForCall(value, frame)
	})
	require.NoError(t, err)
	bundle := exportTestBundle(t, actx, a, r)
	require.Len(t, bundle.Values, 2)
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	numbers := map[uint64]uint64{}
	for _, merged := range reply.Values {
		for _, sent := range bundle.Values {
			if sent.Ordinal == merged.Ordinal {
				numbers[sent.SenderNumber] = merged.Number
			}
		}
	}
	rn, pn := numbers[uint64(r.cacheSharedResult().id)], numbers[uint64(p.cacheSharedResult().id)]
	require.NotZero(t, rn)
	require.NotZero(t, pn)

	got, _, err := cloud.StoredRows(ctx, "", []uint64{rn}, time.Now().Unix(), true, false)
	require.NoError(t, err)
	rows := storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 2)
	require.Empty(t, rows[rn].Held)
	require.Empty(t, rows[pn].Held, "P, a child of R's value, travels as its record")
}

// A fetch's answer leaves out what the requesting engine already holds: a
// child row it has an unexpired holding of goes as an identity row, and
// nothing below it is walked. Another engine's holding changes nothing.
func TestStoredRowsSkipsWhatTheRequesterHolds(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p, q := storedBundleTestChain(t)
	recipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, p))
	_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-b", Number: 1}, RemoteHolding{Recipe: recipe, Request: recipe, Field: "chain-mid"})
	require.NoError(t, err)

	got, _, err := cloud.StoredRows(ctx, "cache-b", []uint64{r}, time.Now().Unix(), true, false)
	require.NoError(t, err)
	rows := storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 2, "R, and P by identity alone")
	require.Empty(t, rows[r].Held)
	require.NotEmpty(t, rows[p].Held, "the requester holds P")
	require.NotContains(t, rows, q, "nothing below P")

	got, _, err = cloud.StoredRows(ctx, "cache-c", []uint64{r}, time.Now().Unix(), true, false)
	require.NoError(t, err)
	rows = storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 3)
	for _, number := range []uint64{r, p, q} {
		require.Empty(t, rows[number].Held, "another engine's holding leaves entry %d in full", number)
	}

	got, _, err = cloud.StoredRows(ctx, "cache-b", []uint64{p}, time.Now().Unix(), true, false)
	require.NoError(t, err)
	rows = storedBundleTestRows(got.Bundle)
	require.Empty(t, rows[p].Held, "an entry asked goes in full though the requester holds it")

	// Several entries asked in one fetch share their rows: each goes once.
	got, missing, err := cloud.StoredRows(ctx, "cache-c", []uint64{r, p, r}, time.Now().Unix(), true, false)
	require.NoError(t, err)
	require.Empty(t, missing)
	require.Len(t, got.Bundle.Values, 3, "R, P and Q, each once")
	require.Len(t, got.Bundle.Roots, 2, "R and P, the entries asked")
}
