package dagql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// takenTestResult is a value of field as an engine holds one it took from
// another cache: another cache computes it, and c merges that cache's
// record of it. A cache keeps its own runs (keepOwnRunsLocked), so another
// cache's parts or records for an entry c computed itself are refused; a
// test of offers' or merges' own mechanics gives or merges them onto an
// entry like this one.
func takenTestResult(t *testing.T, ctx context.Context, c *Cache, srv *Server, field string, value Typed) AnyResult {
	t.Helper()
	sctx, source, ssrv := transferTestCache(t)
	sent := persistedListTestResult(t, sctx, source, ssrv, field, value)
	reply, err := c.MergeValues(ctx, cloudCacheID, exportTestBundle(t, sctx, source, sent))
	require.NoError(t, err)
	imported := reply.Imported()
	require.Len(t, imported, 1, "c takes the record of %s", field)
	res, err := c.LoadResultByResultID(ctx, "test-session", srv, imported[0].ResultID)
	require.NoError(t, err)
	return res
}

// ownRunTestLazy is a value whose computation is deferred until something
// evaluates it, as a lazy core value's is.
func ownRunTestLazy(text string) *transferTestValue {
	return &transferTestValue{Text: text, lazy: func(context.Context) error { return nil }}
}

// The publish check's failure on 13 Nov: the engine ran the CA's signing
// exec itself, and the stored run's part of a call over it, not yet read,
// was offered to the engine's entry for that call. The part would put the
// stored run's CA beside the engine's own key: the offer is refused.
func TestOfferPartsRefusedOverAnOwnRun(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	ca := persistedListTestResult(t, ctx, c, srv, "ca", String("ca"))
	require.True(t, ca.cacheSharedResult().ranHere(), "the engine ran the CA itself")
	file := persistedListTestResult(t, ctx, c, srv, "ca-file", &transferTestValue{Text: "pending"})
	require.NoError(t, c.AddExplicitDependency(ctx, file, ca, "test"))
	out, err := c.testOfferParts(ctx, file, []PersistedPartOffer{testLiveOffer()})
	require.NoError(t, err)
	require.Equal(t, OfferUnavailable, out[0].Outcome)
	require.True(t, out[0].OwnRunBelow)
	require.Zero(t, mergeTestOffers(c, file.cacheSharedResult()))
	require.Equal(t, OwnRunStats{OffersRefused: 1}, c.OwnRunStats())
}

// An entry the engine computed over inputs it took from the Cloud has no run
// of the engine's own below it, and the Cloud's part of it is still lent.
func TestOfferPartsLendsOverInputsTakenFromTheCloud(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	base := takenTestResult(t, ctx, c, srv, "base", String("base"))
	entry := persistedListTestResult(t, ctx, c, srv, "over-base", &transferTestValue{Text: "pending"})
	require.NoError(t, c.AddExplicitDependency(ctx, entry, base, "test"))
	out, err := c.testOfferParts(ctx, entry, []PersistedPartOffer{testLiveOffer()})
	require.NoError(t, err)
	require.Equal(t, OfferAccepted, out[0].Outcome)
	require.False(t, out[0].OwnRunBelow)
	require.Equal(t, 1, mergeTestOffers(c, entry.cacheSharedResult()))
	require.Equal(t, OwnRunStats{OffersLent: 1}, c.OwnRunStats())
}

// A run of the engine's own anywhere below the entry, here under a lazy
// entry the engine computed but hasn't evaluated, refuses the offer as one
// just below does.
func TestOfferPartsRefusedOverAnOwnRunDeepBelow(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	ran := persistedListTestResult(t, ctx, c, srv, "deep-ran", String("ran"))
	middle := persistedListTestResult(t, ctx, c, srv, "deep-middle", ownRunTestLazy("middle"))
	top := persistedListTestResult(t, ctx, c, srv, "deep-top", &transferTestValue{Text: "pending"})
	require.NoError(t, c.AddExplicitDependency(ctx, middle, ran, "test"))
	require.NoError(t, c.AddExplicitDependency(ctx, top, middle, "test"))
	require.False(t, middle.cacheSharedResult().ranHere(), "the entry between hasn't run")
	out, err := c.testOfferParts(ctx, top, []PersistedPartOffer{testLiveOffer()})
	require.NoError(t, err)
	require.Equal(t, OfferUnavailable, out[0].Outcome)
	require.True(t, out[0].OwnRunBelow)
	require.Zero(t, mergeTestOffers(c, top.cacheSharedResult()))
	require.Equal(t, OwnRunStats{OffersRefused: 1}, c.OwnRunStats())
}

// A lazy entry the engine computed and nothing has evaluated has no run of
// its own yet: an entry over it, and over inputs taken from the Cloud below
// it, is still lent to. Once it has been evaluated it is a run, and the same
// offer is refused.
func TestOfferPartsLendsOverAnOwnEntryNotYetRun(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	base := takenTestResult(t, ctx, c, srv, "unrun-base", String("base"))
	middle := persistedListTestResult(t, ctx, c, srv, "unrun-middle", ownRunTestLazy("middle"))
	top := persistedListTestResult(t, ctx, c, srv, "unrun-top", &transferTestValue{Text: "pending"})
	require.NoError(t, c.AddExplicitDependency(ctx, middle, base, "test"))
	require.NoError(t, c.AddExplicitDependency(ctx, top, middle, "test"))
	require.False(t, middle.cacheSharedResult().ranHere())
	out, err := c.testOfferParts(ctx, top, []PersistedPartOffer{testLiveOffer()})
	require.NoError(t, err)
	require.Equal(t, OfferAccepted, out[0].Outcome)
	require.False(t, out[0].OwnRunBelow)

	require.NoError(t, c.Evaluate(ctx, middle))
	require.True(t, middle.cacheSharedResult().ranHere(), "evaluated, it is a run")
	other := persistedListTestResult(t, ctx, c, srv, "unrun-other", &transferTestValue{Text: "pending"})
	require.NoError(t, c.AddExplicitDependency(ctx, other, middle, "test"))
	out, err = c.testOfferParts(ctx, other, []PersistedPartOffer{testLiveOffer()})
	require.NoError(t, err)
	require.Equal(t, OfferUnavailable, out[0].Outcome)
	require.True(t, out[0].OwnRunBelow)
	require.Equal(t, OwnRunStats{OffersRefused: 1, OffersLent: 1}, c.OwnRunStats())
}

// An entry the engine created lazily and nothing has evaluated has no run of
// its own, nor anything of the engine's run below it: a stored record of its
// recipe lands on it as on any kept value. It learns the Cloud's copy and
// takes the record's offers, so when its content is needed the stored run's
// part is installed in place of running. Once it has been evaluated, the
// same record is another run and is kept apart.
func TestMergeValuesLandsOnAnOwnEntryNotYetRun(t *testing.T) {
	t.Parallel()
	for _, evaluated := range []bool{false, true} {
		t.Run(map[bool]string{false: "not yet run", true: "evaluated"}[evaluated], func(t *testing.T) {
			t.Parallel()
			bundle := mergeTestOffered(t, "lazy-root")
			ctx, c, srv := transferTestCache(t)
			own := persistedListTestResult(t, ctx, c, srv, "lazy-root", ownRunTestLazy("own"))
			row := own.cacheSharedResult()
			if evaluated {
				require.NoError(t, c.Evaluate(ctx, own))
			}
			require.Equal(t, evaluated, row.ranHere())
			reply, err := c.MergeValues(ctx, cloudCacheID, bundle)
			require.NoError(t, err)
			c.egraphMu.RLock()
			key, _ := row.cloudHoldingLocked()
			c.egraphMu.RUnlock()
			if evaluated {
				require.Equal(t, []TransferOrdinal{bundle.Values[0].Ordinal}, reply.OtherRun)
				require.Empty(t, reply.Imported())
				require.Zero(t, key)
				require.Zero(t, mergeTestOffers(c, row))
				return
			}
			require.Empty(t, reply.OtherRun)
			require.Equal(t, uint64(row.id), reply.Imported()[0].ResultID, "the record lands on the entry")
			require.Equal(t, HolderKey{Cache: cloudCacheID, Number: bundle.Values[0].SenderNumber}, key, "which learns the Cloud's copy")
			require.Equal(t, 1, mergeTestOffers(c, row), "and takes its offer")
		})
	}
}

// A record landing on an entry the engine created lazily is still kept apart
// when a run of the engine's own is below that entry: the publish check's
// shape, with the call over the CA not yet evaluated.
func TestMergeValuesKeepsApartOverAnOwnRunBelow(t *testing.T) {
	t.Parallel()
	actx, a, asrv := transferTestCache(t)
	sentCA := persistedListTestResult(t, actx, a, asrv, "below-ca", String("fill ca"))
	sentFile := persistedListTestResult(t, actx, a, asrv, "below-file", &transferTestValue{Text: "fill file"})
	require.NoError(t, a.AddExplicitDependency(actx, sentFile, sentCA, "test"))
	bundle := exportTestBundle(t, actx, a, sentFile)

	ctx, c, srv := transferTestCache(t)
	ca := persistedListTestResult(t, ctx, c, srv, "below-ca", String("own ca"))
	file := persistedListTestResult(t, ctx, c, srv, "below-file", ownRunTestLazy("own file"))
	require.NoError(t, c.AddExplicitDependency(ctx, file, ca, "test"))
	require.True(t, ca.cacheSharedResult().ranHere())
	require.False(t, file.cacheSharedResult().ranHere())
	reply, err := c.MergeValues(ctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.Len(t, reply.OtherRun, 2, "the CA and the file over it are kept apart")
	require.Empty(t, reply.Imported())
	c.egraphMu.RLock()
	key, _ := file.cacheSharedResult().cloudHoldingLocked()
	c.egraphMu.RUnlock()
	require.Zero(t, key)
}
