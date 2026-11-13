package dagql

import (
	"context"
	"testing"
	"time"

	"github.com/dagger/dagger/dagql/call"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// heldTestHold records, on the Cloud, a holding of engine cache's entry
// number on the Cloud entry cloudNumber, as a call span would report it.
func heldTestHold(t *testing.T, ctx context.Context, cloud *Cache, cache CacheID, number, cloudNumber uint64, expiresAtUnix int64) {
	t.Helper()
	entry := storedBundleTestEntry(cloud, cloudNumber)
	cloud.egraphMu.RLock()
	recipe := entry.recipeKeys[0]
	cloud.egraphMu.RUnlock()
	_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: cache, Number: number}, RemoteHolding{Recipe: recipe, Request: recipe, Field: entry.description, ExpiresAtUnix: expiresAtUnix})
	require.NoError(t, err)
	cloud.egraphMu.RLock()
	defer cloud.egraphMu.RUnlock()
	require.Equal(t, sharedResultID(cloudNumber), cloud.holderEntriesForTest()[HolderKey{Cache: cache, Number: number}], "the holding sits on the Cloud entry")
}

// heldTestRow returns the bundle's row sent for the Cloud entry number.
func heldTestRow(t *testing.T, bundle ValueBundle, number uint64) TransferredValue {
	t.Helper()
	for _, value := range bundle.Values {
		if value.SenderNumber == number {
			return value
		}
	}
	require.FailNow(t, "no row for the entry", "entry %d", number)
	return TransferredValue{}
}

// heldTestMerged returns the reply's value for the ordinal.
func heldTestMerged(t *testing.T, reply MergeReply, ordinal TransferOrdinal) MergedValue {
	t.Helper()
	for _, value := range reply.Values {
		if value.Ordinal == ordinal {
			return value
		}
	}
	require.FailNow(t, "no merged value for the ordinal", "ordinal %d", ordinal)
	return MergedValue{}
}

// heldTestEntries counts the engine's entries.
func heldTestEntries(c *Cache) int {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	return len(c.resultsByID)
}

// heldTestChainedCloud merges an export of a root R, whose call takes P as
// its receiver, into a new Cloud cache, and returns the Cloud's numbers for R
// and P: R's recipe derives from P's.
func heldTestChainedCloud(t *testing.T) (context.Context, *Cache, uint64, uint64) {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	p := persistedListTestResult(t, actx, a, asrv, "bundle-dep", &transferTestValue{Text: "dep"})
	frame := &ResultCall{Kind: ResultCallKindField, Field: "held-chained", Type: NewResultCallType((&transferTestValue{}).Type()), Receiver: &ResultCallRef{ResultID: uint64(p.cacheSharedResult().id)}}
	r, err := a.GetOrInitCall(actx, "test-session", asrv, &CallRequest{ResultCall: frame, IsPersistable: true}, func(context.Context) (AnyResult, error) {
		return NewResultForCall(&transferTestValue{Text: "chained"}, frame)
	})
	require.NoError(t, err)
	bundle := exportTestBundle(t, actx, a, r)
	require.Len(t, bundle.Values, 2, "R and its receiver")
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
	rNumber, pNumber := numbers[uint64(r.cacheSharedResult().id)], numbers[uint64(p.cacheSharedResult().id)]
	require.NotZero(t, rNumber)
	require.NotZero(t, pNumber)
	return ctx, cloud, rNumber, pNumber
}

// heldTestSessionValue computes the value of P's call in the engine's
// "test-session", not persistable: only the session holds it.
func heldTestSessionValue(t *testing.T, ctx context.Context, c *Cache, srv *Server) uint64 {
	t.Helper()
	frame := &ResultCall{Kind: ResultCallKindField, Field: "bundle-dep", Type: NewResultCallType((&transferTestValue{}).Type())}
	res, err := c.GetOrInitCall(ctx, "test-session", srv, &CallRequest{ResultCall: frame}, func(context.Context) (AnyResult, error) {
		return NewResultForCall(&transferTestValue{Text: "dep"}, frame)
	})
	require.NoError(t, err)
	return uint64(res.cacheSharedResult().id)
}

// heldTestEngineWithP merges the Cloud's P alone into a new engine cache,
// and returns the engine and P's number there.
func heldTestEngineWithP(t *testing.T, ctx context.Context, cloud *Cache, p uint64) (context.Context, *Cache, uint64) {
	t.Helper()
	alone, err := cloud.StoredBundle(ctx, []uint64{p}, time.Now().Unix())
	require.NoError(t, err)
	bctx, b, _ := transferTestCache(t)
	reply, err := b.MergeValues(bctx, cloudCacheID, alone.Bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	return bctx, b, reply.Imported()[0].ResultID
}
// The Cloud carries an entry a root references as a held row, an identity
// row: its recipe and sending facts, with no record and nothing below it.
func TestStoredBundleCarriesAHeldEntryAsAHeldRow(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := storedBundleTestCloud(t, "root")
	now := time.Now().Unix()

	got, err := cloud.StoredBundle(ctx, []uint64{r}, now)
	require.NoError(t, err)
	require.Equal(t, []uint64{p}, got.Held)
	require.Len(t, got.Bundle.Values, 2)
	held := heldTestRow(t, got.Bundle, p)
	recipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, p))
	require.Equal(t, recipe, held.Held, "the held row names the entry's recipe")
	require.Equal(t, PersistedRecord{ResultID: held.SenderNumber}, held.Record, "no record")
	require.Empty(t, held.DependencyIDs)
	require.Equal(t, []uint64{uint64(held.Ordinal)}, heldTestRow(t, got.Bundle, r).DependencyIDs, "R depends on the held row")

}

// A held row lands on the engine's own entry of its recipe, and the record
// whose call takes it as receiver derives the identity the full record would:
// the Cloud's recipe, and merging the full bundle afterwards creates nothing
// and names the same entries.
func TestMergeHeldRowLandsOnTheEnginesEntry(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := heldTestChainedCloud(t)
	bctx, b, pb := heldTestEngineWithP(t, ctx, cloud, p)
	heldTestHold(t, ctx, cloud, "cache-b", pb, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	require.Equal(t, []uint64{p}, got.Held)

	reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	require.Empty(t, reply.Stale)
	heldOrdinal := heldTestRow(t, got.Bundle, p).Ordinal
	require.Equal(t, pb, heldTestMerged(t, reply, heldOrdinal).Number, "the held row is the engine's P")
	rMerged := heldTestMerged(t, reply, heldTestRow(t, got.Bundle, r).Ordinal)
	require.Equal(t, []uint64{pb}, rMerged.Deps, "R depends on the engine's P")
	require.Equal(t, []ImportedValue{{Ordinal: heldTestRow(t, got.Bundle, r).Ordinal, ResultID: rMerged.Number}}, reply.Imported())
	cloudRecipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, r))
	b.egraphMu.RLock()
	require.Equal(t, sharedResultID(rMerged.Number), b.entriesByRecipe[cloudRecipe], "R is indexed under the Cloud's recipe")
	b.egraphMu.RUnlock()

	entries := heldTestEntries(b)
	full, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	again, err := b.MergeValues(bctx, cloudCacheID, full.Bundle)
	require.NoError(t, err)
	require.Equal(t, entries, heldTestEntries(b), "the full bundle creates nothing")
	require.Equal(t, rMerged.Number, heldTestMerged(t, again, heldTestRow(t, full.Bundle, r).Ordinal).Number, "R's identity is the full record's")
	require.Equal(t, pb, heldTestMerged(t, again, heldTestRow(t, full.Bundle, p).Ordinal).Number)
}

// An engine that doesn't hold a held row's value lands the row with its
// identity alone: an entry with the Cloud entry's recipe, its extra digests
// and the Cloud's holding, and no value. The root derives its identity from
// that recipe, depends on the entry, and is the only row the reply lists, so
// the engine marks known to the Cloud only what holds a value.
func TestMergeHeldRowTheEngineLacksLandsWithItsIdentity(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := heldTestChainedCloud(t)
	heldTestHold(t, ctx, cloud, "cache-b", 7, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	require.Equal(t, []uint64{p}, got.Held)
	content := digest.FromString("p's content")
	for i := range got.Bundle.Values {
		if got.Bundle.Values[i].SenderNumber == p {
			got.Bundle.Values[i].Extra = []call.ExtraDigest{{Digest: content, Label: call.ExtraDigestLabelContent}}
		}
	}

	bctx, b, _ := transferTestCache(t)
	entries := heldTestEntries(b)
	reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	require.Empty(t, reply.Stale)
	require.Equal(t, entries+2, heldTestEntries(b), "R and an entry for P")
	rOrdinal := heldTestRow(t, got.Bundle, r).Ordinal
	require.Len(t, reply.Values, 1, "the reply lists only what holds a value")
	rMerged := heldTestMerged(t, reply, rOrdinal)
	require.Len(t, rMerged.Deps, 1, "R depends on the entry for P")

	pRecipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, p))
	rRecipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, r))
	b.egraphMu.RLock()
	defer b.egraphMu.RUnlock()
	pEntry := b.resultsByID[sharedResultID(rMerged.Deps[0])]
	require.NotNil(t, pEntry)
	require.True(t, pEntry.identityOnly)
	require.True(t, pEntry.noValueLocked(), "lookups pass it by")
	require.Equal(t, pRecipe, pEntry.identityRecipe)
	require.Equal(t, pEntry.id, b.entriesByRecipe[pRecipe])
	key, h := pEntry.cloudHoldingLocked()
	require.NotNil(t, h)
	require.Equal(t, p, key.Number, "it names the Cloud's entry")
	require.Equal(t, sharedResultID(rMerged.Number), b.entriesByRecipe[rRecipe], "R's identity derives from P's recipe")
	require.Equal(t, b.eqClassRootLocked(b.egraphDigestToClass[pRecipe.String()]), b.eqClassRootLocked(b.egraphDigestToClass[content.String()]), "P's recipe and content are one class")
	require.Nil(t, b.servableCandidateLocked(pEntry.id, time.Now().Unix(), nil))
}

// A Cloud merge still refuses a held row it doesn't hold: the Cloud is the
// store other engines are served from, so nothing could fetch the row later.
func TestCloudStillRefusesAHeldRowItLacks(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := storedBundleTestCloud(t, "root")
	heldTestHold(t, ctx, cloud, "cache-b", 7, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	octx, other := storedPartTestCache(t, WithBlobStore())
	entries := heldTestEntries(other)
	reply, err := other.MergeValues(octx, "cache-b", got.Bundle)
	require.NoError(t, err, "a stale merge is answered, not failed")
	require.False(t, reply.Committed)
	require.Equal(t, []TransferOrdinal{heldTestRow(t, got.Bundle, p).Ordinal}, reply.Stale)
	require.Equal(t, entries, heldTestEntries(other), "nothing committed")
}

// A held row whose engine entry has expired, while the row hasn't, lands
// with its identity alone on a new entry: the expired value isn't served,
// and a held row carries no record to replace it with.
func TestMergeHeldRowOverAnExpiredValueLandsWithItsIdentity(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := storedBundleTestCloud(t, "root")
	bctx, b, pb := heldTestEngineWithP(t, ctx, cloud, p)
	heldTestHold(t, ctx, cloud, "cache-b", pb, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	b.egraphMu.RLock()
	entry := b.resultsByID[sharedResultID(pb)]
	b.egraphMu.RUnlock()
	mergeTestExpire(b, entry)

	reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	rMerged := heldTestMerged(t, reply, heldTestRow(t, got.Bundle, r).Ordinal)
	require.Len(t, rMerged.Deps, 1)
	require.NotEqual(t, pb, rMerged.Deps[0], "not the expired value")
	b.egraphMu.RLock()
	defer b.egraphMu.RUnlock()
	require.True(t, b.resultsByID[sharedResultID(rMerged.Deps[0])].identityOnly)
}

// A held row can name a value the engine computed in a session, which only
// the session holds: the merged root depends on it, so it outlives the
// session. Without the merge, it goes with the session.
func TestMergeHeldRowKeepsASessionValueAlive(t *testing.T) {
	t.Parallel()
	cctx, control, csrv := transferTestCache(t)
	alone := heldTestSessionValue(t, cctx, control, csrv)
	require.NoError(t, control.ReleaseSession(cctx, "test-session"))
	control.egraphMu.RLock()
	left := control.resultsByID[sharedResultID(alone)]
	gone := left == nil || left.noValueLocked()
	control.egraphMu.RUnlock()
	require.True(t, gone, "only the session held it")

	ctx, cloud, r, p := storedBundleTestCloud(t, "root")
	bctx, b, bsrv := transferTestCache(t)
	pb := heldTestSessionValue(t, bctx, b, bsrv)
	heldTestHold(t, ctx, cloud, "cache-b", pb, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	require.Equal(t, []uint64{p}, got.Held)

	reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	require.Empty(t, reply.Stale)
	rMerged := heldTestMerged(t, reply, heldTestRow(t, got.Bundle, r).Ordinal)
	require.Equal(t, []uint64{pb}, rMerged.Deps)

	require.NoError(t, b.ReleaseSession(bctx, "test-session"))
	b.egraphMu.RLock()
	defer b.egraphMu.RUnlock()
	kept := b.resultsByID[sharedResultID(pb)]
	require.NotNil(t, kept, "the session's value outlives it under the merged root")
	require.False(t, kept.noValueLocked())
	require.NotNil(t, b.resultsByID[sharedResultID(rMerged.Number)])
}
// A held row holds only where the recipient has its recipe's value: another
// Cloud without it answers the bundle stale. A held row carries no record,
// though it can carry dependencies.
func TestMergeHeldRowRefusals(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := storedBundleTestCloud(t, "root")
	heldTestHold(t, ctx, cloud, "cache-b", 7, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)

	cctx, other := storedPartTestCache(t, WithBlobStore())
	reply, err := other.MergeValues(cctx, "cache-a", got.Bundle)
	require.NoError(t, err)
	require.False(t, reply.Committed)
	require.NotEmpty(t, reply.Stale)

	bad, err := cloneValueBundle(got.Bundle)
	require.NoError(t, err)
	for i := range bad.Values {
		if bad.Values[i].Held != "" {
			bad.Values[i].Record.Envelope = heldTestRow(t, bad, r).Record.Envelope
		}
	}
	bctx, b, _ := transferTestCache(t)
	_, err = b.MergeValues(bctx, cloudCacheID, bad)
	require.ErrorContains(t, err, "held row carries a record")
}

// A held row may name its entry's extra digests; a full row may not, and one
// that isn't a labelled digest is refused.
func TestMergeHeldRowExtraDigests(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := storedBundleTestCloud(t, "root")
	heldTestHold(t, ctx, cloud, "cache-b", 7, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	_, err = validateValueBundleShape(got.Bundle)
	require.NoError(t, err)

	withExtra := func(extra call.ExtraDigest, held bool) ValueBundle {
		bad, err := cloneValueBundle(got.Bundle)
		require.NoError(t, err)
		for i := range bad.Values {
			if (bad.Values[i].Held != "") == held {
				bad.Values[i].Extra = []call.ExtraDigest{extra}
			}
		}
		return bad
	}
	_, err = validateValueBundleShape(withExtra(call.ExtraDigest{Digest: digest.FromString("content"), Label: call.ExtraDigestLabelContent}, true))
	require.NoError(t, err)
	_, err = validateValueBundleShape(withExtra(call.ExtraDigest{Digest: "not a digest", Label: call.ExtraDigestLabelContent}, true))
	require.ErrorContains(t, err, "is not a labelled digest")
	_, err = validateValueBundleShape(withExtra(call.ExtraDigest{Digest: digest.FromString("content")}, true))
	require.ErrorContains(t, err, "is not a labelled digest")
	_, err = validateValueBundleShape(withExtra(call.ExtraDigest{Digest: digest.FromString("content"), Label: call.ExtraDigestLabelContent}, false))
	require.ErrorContains(t, err, "extra digests on a row that isn't held")
}

// A bundle that carries a held row and a record of the same recipe is
// malformed, and the merge refuses it, committing nothing: the record could
// replace the entry the held row was checked against.
func TestMergeHeldRowRefusesARecordOfTheSameRecipe(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := storedBundleTestCloud(t, "root")
	bctx, b, _ := heldTestEngineWithP(t, ctx, cloud, p)
	full, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	recipe := storedTestRecipe(t, cloud, storedBundleTestEntry(cloud, p))
	ordinal := TransferOrdinal(len(full.Bundle.Values) + 1)
	bundle := full.Bundle
	// A number of its own: a sender names each of its entries once.
	number := p + 1<<20
	bundle.Values = append(bundle.Values, TransferredValue{Ordinal: ordinal, SenderNumber: number, Record: PersistedRecord{ResultID: number}, Held: recipe})
	bundle.Roots = append(bundle.Roots, TransferredRoot{Ordinal: ordinal})

	entries := heldTestEntries(b)
	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.ErrorContains(t, err, "same recipe")
	require.False(t, reply.Committed)
	require.Equal(t, entries, heldTestEntries(b), "nothing committed")
}

// A merge's reply counts a held row as held, apart from the records it
// creates; the full bundle afterwards keeps both.
func TestMergeHeldRowCountsAsHeld(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := heldTestChainedCloud(t)
	bctx, b, pb := heldTestEngineWithP(t, ctx, cloud, p)
	heldTestHold(t, ctx, cloud, "cache-b", pb, p, 0)
	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	require.Equal(t, []uint64{p}, got.Held)
	reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
	require.NoError(t, err)
	require.Equal(t, MergeRowCounts{Held: 1, Created: 1}, reply.Rows)

	full, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	again, err := b.MergeValues(bctx, cloudCacheID, full.Bundle)
	require.NoError(t, err)
	require.Equal(t, MergeRowCounts{Kept: 2}, again.Rows)
}
