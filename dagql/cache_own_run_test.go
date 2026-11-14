package dagql

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dagger/dagger/dagql/call"
	"github.com/opencontainers/go-digest"
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

// Records of a chain the engine created lazily and hasn't run, P over D,
// both land, whichever of the two the read meets first: a read that walks P
// first must still read D.
func TestMergeValuesLandsOnAnOwnChainNotYetRun(t *testing.T) {
	t.Parallel()
	actx, a, asrv := transferTestCache(t)
	sentD := persistedListTestResult(t, actx, a, asrv, "chain-d", &transferTestValue{Text: "fill d"})
	sentP := persistedListTestResult(t, actx, a, asrv, "chain-p", &transferTestValue{Text: "fill p"})
	require.NoError(t, a.AddExplicitDependency(actx, sentP, sentD, "test"))
	bundle := exportTestBundle(t, actx, a, sentP)
	for range 20 {
		ctx, c, srv := transferTestCache(t)
		d := persistedListTestResult(t, ctx, c, srv, "chain-d", ownRunTestLazy("own d"))
		p := persistedListTestResult(t, ctx, c, srv, "chain-p", ownRunTestLazy("own p"))
		require.NoError(t, c.AddExplicitDependency(ctx, p, d, "test"))
		reply, err := c.MergeValues(ctx, cloudCacheID, bundle)
		require.NoError(t, err)
		require.Empty(t, reply.OtherRun, "neither has run")
		require.Equal(t, 2, reply.Rows.OwnNotRun)
	}
}

// C over B over A arrive, where B is an entry the engine took earlier, over
// its own A while A hadn't run, and A has run since. A's record is kept
// apart, so B's lands on B as another run, and C, computed over that run of
// B, isn't installed.
func TestMergeValuesKeepsApartWhatIsOverAKeptApartRow(t *testing.T) {
	t.Parallel()
	source := func(t *testing.T, text string, withC bool) ValueBundle {
		actx, a, asrv := transferTestCache(t)
		sentA := persistedListTestResult(t, actx, a, asrv, "kept-a", &transferTestValue{Text: text + " a"})
		sentB := persistedListTestResult(t, actx, a, asrv, "kept-b", &transferTestValue{Text: text + " b"})
		require.NoError(t, a.AddExplicitDependency(actx, sentB, sentA, "test"))
		if !withC {
			return exportTestBundle(t, actx, a, sentB)
		}
		sentC := persistedListTestResult(t, actx, a, asrv, "kept-c", &transferTestValue{Text: text + " c"})
		require.NoError(t, a.AddExplicitDependency(actx, sentC, sentB, "test"))
		return exportTestBundle(t, actx, a, sentC)
	}
	ctx, c, srv := transferTestCache(t)
	own := persistedListTestResult(t, ctx, c, srv, "kept-a", ownRunTestLazy("own a"))
	first, err := c.MergeValues(ctx, cloudCacheID, source(t, "first", false))
	require.NoError(t, err)
	require.Empty(t, first.OtherRun, "A hadn't run: B is taken over it")
	require.NoError(t, c.Evaluate(ctx, own))

	bundle := source(t, "second", true)
	reply, err := c.MergeValues(ctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.Len(t, reply.OtherRun, 2, "A, and B over it")
	require.Len(t, reply.Roots, 1)
	require.True(t, reply.Roots[0].OtherRun, "C isn't installed")
	require.Empty(t, reply.Imported())
}

// A file shown equal by content to the engine's own lands on it, though the
// exec it was read from is another run, kept apart: equal content is the
// same result.
func TestMergeValuesLandsAContentCopyOverAKeptApartRow(t *testing.T) {
	t.Parallel()
	content := digest.FromString("file content")
	actx, a, asrv := transferTestCache(t)
	sentExec := persistedListTestResult(t, actx, a, asrv, "copy-exec", String("fill exec"))
	sentFile := persistedListTestResult(t, actx, a, asrv, "copy-file", &transferTestValue{Text: "fill file"})
	require.NoError(t, a.AddExplicitDependency(actx, sentFile, sentExec, "test"))
	require.NoError(t, a.TeachContentDigest(actx, sentFile, content))
	bundle := exportTestBundle(t, actx, a, sentFile)

	ctx, c, srv := transferTestCache(t)
	exec := persistedListTestResult(t, ctx, c, srv, "copy-exec", String("own exec"))
	file := persistedListTestResult(t, ctx, c, srv, "copy-file", &transferTestValue{Text: "own file"})
	require.NoError(t, c.AddExplicitDependency(ctx, file, exec, "test"))
	require.NoError(t, c.TeachContentDigest(ctx, file, content))
	reply, err := c.MergeValues(ctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.Len(t, reply.OtherRun, 1, "the exec is kept apart")
	require.Equal(t, 1, reply.Rows.OwnSameContent, "the file lands")
	require.Equal(t, uint64(file.cacheSharedResult().id), reply.Imported()[0].ResultID)
	c.egraphMu.RLock()
	key, _ := file.cacheSharedResult().cloudHoldingLocked()
	c.egraphMu.RUnlock()
	require.NotZero(t, key, "and learns the Cloud's copy")
}

// A call the Cloud sends over an input the merge keeps apart isn't taught:
// it was answered over another run of that input than the engine's own, so
// the engine's own call over its input misses.
func TestCallRecordOverAKeptApartInputIsNotTaught(t *testing.T) {
	t.Parallel()
	test := newReceiverCallTest(t)
	built, err := test.cloud.StoredBundle(test.ctx, []uint64{test.number, test.receiverNumber}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Len(t, built.Bundle.Calls, 1)

	// The engine ran the receiver's recipe itself, with other content.
	ctx, fresh, srv := transferTestCache(t)
	ownFrame := func() *ResultCall {
		frame := receiverTestFrame("a")
		frame.ExtraDigests = []call.ExtraDigest{{Digest: digest.FromString("own receiver content"), Label: call.ExtraDigestLabelContent}}
		return frame
	}
	_, err = fresh.GetOrInitCall(ctx, "test-session", srv, &CallRequest{ResultCall: ownFrame(), IsPersistable: true},
		func(context.Context) (AnyResult, error) {
			return NewResultForCall(&transferTestValue{Text: "own receiver"}, ownFrame())
		})
	require.NoError(t, err)
	reply, err := fresh.MergeValues(ctx, CloudCacheID, built.Bundle)
	require.NoError(t, err)
	require.NotEmpty(t, reply.OtherRun, "the receiver is kept apart")
	ran := false
	_, err = fresh.GetOrInitCall(ctx, "test-session", srv, &CallRequest{ResultCall: receiverTestCall("a"), IsPersistable: true},
		func(context.Context) (AnyResult, error) {
			ran = true
			return NewResultForCall(&transferTestValue{Text: "own outer"}, receiverTestCall("a"))
		})
	require.NoError(t, err)
	require.True(t, ran, "the call over the engine's own receiver runs")
}

// The service sends R, which another engine computed over its run of P,
// with P as a held row, because this engine's rows report its own P. A held
// row carries nothing the engine can compare: where the engine's P has run,
// the held row is kept apart as a full row would be, and R isn't installed
// over it. Where the engine's P is lazy and hasn't run, the held row stands
// for it as before. (The service sends such rows: its
// TestPlacementHeldFromRows.)
func TestMergeKeepsApartAHeldRowOnAnOwnRun(t *testing.T) {
	t.Parallel()
	for _, ran := range []bool{true, false} {
		t.Run(map[bool]string{true: "own run", false: "not yet run"}[ran], func(t *testing.T) {
			t.Parallel()
			ctx, cloud, r, p := heldTestChainedCloud(t)
			bctx, b, bsrv := transferTestCache(t)
			value := &transferTestValue{Text: "own dep"}
			if !ran {
				value = ownRunTestLazy("own dep")
			}
			own := persistedListTestResult(t, bctx, b, bsrv, "bundle-dep", value)
			require.Equal(t, ran, own.cacheSharedResult().ranHere())
			pb := uint64(own.cacheSharedResult().id)
			heldTestHold(t, ctx, cloud, "cache-b", pb, p, 0)
			got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix(), &HeldBy{Cache: "cache-b"})
			require.NoError(t, err)
			require.Equal(t, []uint64{p}, got.Held)

			reply, err := b.MergeValues(bctx, cloudCacheID, got.Bundle)
			require.NoError(t, err)
			require.True(t, reply.Committed)
			heldOrdinal := heldTestRow(t, got.Bundle, p).Ordinal
			if !ran {
				require.Empty(t, reply.OtherRun)
				require.Len(t, reply.Imported(), 1, "R lands over the engine's P")
				return
			}
			require.Equal(t, []TransferOrdinal{heldOrdinal}, reply.OtherRun, "the held row is kept apart")
			require.Empty(t, reply.Imported(), "R isn't installed over the engine's run")
			require.Equal(t, 1, reply.Rows.ApartHeld)
			require.Equal(t, 1, reply.Rows.OverApartHeld)
			require.Zero(t, reply.Rows.ApartFull+reply.Rows.OverApartFull)
		})
	}
}

// A record of C carries an offer owned by B, and B was computed over A,
// which the merge keeps apart: B isn't installed, so the offer has no owner.
// The merge still commits. Where C lands as a copy of the engine's own value
// by content it lands without the offer; where it lands on a value the
// engine took from another cache, the offer would give it a part made over
// another run, so it is kept apart.
func TestMergeValuesLeavesAnOfferOwnedByADroppedRow(t *testing.T) {
	t.Parallel()
	content := digest.FromString("c content")
	actx, a, asrv := transferTestCache(t)
	// The sender's numbers aren't the bundle's ordinals.
	for i := range 5 {
		persistedListTestResult(t, actx, a, asrv, fmt.Sprintf("owner-padding-%d", i), String("padding"))
	}
	sentA := persistedListTestResult(t, actx, a, asrv, "owner-a", String("fill a"))
	sentB := persistedListTestResult(t, actx, a, asrv, "owner-b", &transferTestValue{Text: "fill b"})
	require.NoError(t, a.AddExplicitDependency(actx, sentB, sentA, "test"))
	sentC := persistedListTestResult(t, actx, a, asrv, "owner-c", &transferTestValue{Text: "fill c"})
	require.NoError(t, a.TeachContentDigest(actx, sentC, content))
	mergeTestOfferOn(t, actx, a, sentC, "/c", sentB)
	bundle := exportTestBundle(t, actx, a, sentC, sentB)

	for _, sameContent := range []bool{true, false} {
		t.Run(map[bool]string{true: "own copy by content", false: "taken value"}[sameContent], func(t *testing.T) {
			ctx, c, srv := transferTestCache(t)
			persistedListTestResult(t, ctx, c, srv, "owner-a", String("own a"))
			var own AnyResult
			if sameContent {
				own = persistedListTestResult(t, ctx, c, srv, "owner-c", &transferTestValue{Text: "own c"})
				require.NoError(t, c.TeachContentDigest(ctx, own, content))
			} else {
				own = takenTestResult(t, ctx, c, srv, "owner-c", &transferTestValue{Text: "taken c"})
			}
			reply, err := c.MergeValues(ctx, cloudCacheID, bundle)
			require.NoError(t, err, "the merge commits")
			require.True(t, reply.Committed)
			require.Zero(t, mergeTestOffers(c, own.cacheSharedResult()), "C takes no offer from a dropped owner")
			if sameContent {
				require.Equal(t, 1, reply.Rows.OwnSameContent)
			} else {
				require.Equal(t, 1, reply.Rows.ApartFull, "A")
				require.Equal(t, 2, reply.Rows.OverApartFull, "B, and C over B's offer")
			}
		})
	}
}

// The Cloud leaves out a call's input that the recipient holds, so the call
// arrives with no row for it. Where the engine ran that input itself, the
// call's answer was made over another run of it: it isn't taught, and the
// engine's own call over its input runs.
// (TestCallRecordPrefersAnInputTheRecipientHolds shows the omission.)
func TestCallRecordOverAHeldOwnInputIsNotTaught(t *testing.T) {
	t.Parallel()
	test := newReceiverCallTest(t)
	ctx, fresh, srv := transferTestCache(t)
	own, err := fresh.GetOrInitCall(ctx, "test-session", srv, &CallRequest{ResultCall: receiverTestFrame("a"), IsPersistable: true},
		func(context.Context) (AnyResult, error) {
			return NewResultForCall(&transferTestValue{Text: "own receiver"}, receiverTestFrame("a"))
		})
	require.NoError(t, err)
	require.True(t, own.cacheSharedResult().ranHere())
	recipe, err := receiverTestFrame("a").deriveRecipeDigest(nil)
	require.NoError(t, err)
	_, err = test.cloud.AttachRemoteHolding(test.ctx, HolderKey{Cache: "cache-fresh", Number: uint64(own.cacheSharedResult().id)}, RemoteHolding{Recipe: recipe, Request: recipe, Field: "receiver"})
	require.NoError(t, err)
	built, err := test.cloud.StoredBundle(test.ctx, []uint64{test.number}, time.Now().Unix(), &HeldBy{Cache: "cache-fresh"})
	require.NoError(t, err)
	require.Len(t, built.Bundle.Calls, 1)
	require.Empty(t, built.CallInputs, "the input isn't carried")

	_, err = fresh.MergeValues(ctx, CloudCacheID, built.Bundle)
	require.NoError(t, err)
	ran := false
	_, err = fresh.GetOrInitCall(ctx, "test-session", srv, &CallRequest{ResultCall: receiverTestCall("a"), IsPersistable: true},
		func(context.Context) (AnyResult, error) {
			ran = true
			return NewResultForCall(&transferTestValue{Text: "own outer"}, receiverTestCall("a"))
		})
	require.NoError(t, err)
	require.True(t, ran, "the call over the engine's own receiver runs")
}

// A merge names the rows it keeps apart directly: each one's field and type,
// how it came, why it was kept apart, and whether the engine's entry and the
// stored row each carried a content digest at that moment; and it counts
// them by field.
func TestMergeValuesNamesTheRowsItKeepsApart(t *testing.T) {
	t.Parallel()
	actx, a, asrv := transferTestCache(t)
	sentCA := persistedListTestResult(t, actx, a, asrv, "named-ca", String("fill ca"))
	sentFile := persistedListTestResult(t, actx, a, asrv, "named-file", &transferTestValue{Text: "fill file"})
	require.NoError(t, a.AddExplicitDependency(actx, sentFile, sentCA, "test"))
	require.NoError(t, a.TeachContentDigest(actx, sentCA, digest.FromString("fill ca content")))
	bundle := exportTestBundle(t, actx, a, sentFile)

	ctx, c, srv := transferTestCache(t)
	ca := persistedListTestResult(t, ctx, c, srv, "named-ca", String("own ca"))
	file := persistedListTestResult(t, ctx, c, srv, "named-file", ownRunTestLazy("own file"))
	require.NoError(t, c.AddExplicitDependency(ctx, file, ca, "test"))
	reply, err := c.MergeValues(ctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.Len(t, reply.OtherRun, 2)
	samples := map[string]OwnRunApart{}
	for _, s := range reply.ApartSamples {
		samples[s.Field] = s
	}
	require.Len(t, samples, 2, "both rows were kept apart directly")
	require.Equal(t, OwnRunApart{Field: "named-ca", Type: "String", Route: "full", Reason: "ran itself: whole at its call", StoredDigest: true, SenderNumber: uint64(sentCA.cacheSharedResult().id)}, samples["named-ca"])
	require.Equal(t, OwnRunApart{Field: "named-file", Type: "TransferTestValue", Route: "full", Reason: "a run of its own below", SenderNumber: uint64(sentFile.cacheSharedResult().id)}, samples["named-file"])
	require.Equal(t, map[string]int{"named-ca": 1, "named-file": 1}, reply.ApartFields)
}

// A value its call made whole, with no parts and nothing it names, is
// entirely its record: a stored record of the engine's own run of its
// recipe whose value encodes the same is the same result, and lands. One
// whose value differs is another run, and is kept apart.
func TestMergeValuesLandsAnEqualPlainValueOnAnOwnRun(t *testing.T) {
	t.Parallel()
	for _, equal := range []bool{true, false} {
		t.Run(map[bool]string{true: "equal", false: "different"}[equal], func(t *testing.T) {
			t.Parallel()
			actx, a, asrv := transferTestCache(t)
			sent := persistedListTestResult(t, actx, a, asrv, "plain-exists", Boolean(true))
			bundle := exportTestBundle(t, actx, a, sent)
			ctx, c, srv := transferTestCache(t)
			own := persistedListTestResult(t, ctx, c, srv, "plain-exists", Boolean(equal))
			require.True(t, own.cacheSharedResult().ranHere())
			reply, err := c.MergeValues(ctx, cloudCacheID, bundle)
			require.NoError(t, err)
			c.egraphMu.RLock()
			key, _ := own.cacheSharedResult().cloudHoldingLocked()
			c.egraphMu.RUnlock()
			if !equal {
				require.Len(t, reply.OtherRun, 1, "another run's value")
				require.Zero(t, key)
				return
			}
			require.Empty(t, reply.OtherRun)
			require.Equal(t, 1, reply.Rows.OwnSameContent)
			require.Equal(t, uint64(own.cacheSharedResult().id), reply.Imported()[0].ResultID)
			require.Equal(t, HolderKey{Cache: cloudCacheID, Number: bundle.Values[0].SenderNumber}, key, "the entry learns the Cloud's copy")
		})
	}
}

// A stored record whose content digest the engine's own run of its recipe
// lacks when the merge decides is kept apart: a git tree's digest, for one,
// is taught only once the tree has been evaluated. When the entry's digest is
// taught and is the record's, the two are the same result after all: the
// entry learns the Cloud's copy then, and a held row of that copy, which
// stands for the entry's own value, lands from then on.
func TestMergeValuesComparesAgainWhenTheDigestIsTaught(t *testing.T) {
	t.Parallel()
	content := digest.FromString("tree content")
	actx, a, asrv := transferTestCache(t)
	p := persistedListTestResult(t, actx, a, asrv, "bundle-dep", &transferTestValue{Text: "dep"})
	require.NoError(t, a.TeachContentDigest(actx, p, content))
	frame := &ResultCall{Kind: ResultCallKindField, Field: "held-chained", Type: NewResultCallType((&transferTestValue{}).Type()), Receiver: &ResultCallRef{ResultID: uint64(p.cacheSharedResult().id)}}
	r, err := a.GetOrInitCall(actx, "test-session", asrv, &CallRequest{ResultCall: frame, IsPersistable: true}, func(context.Context) (AnyResult, error) {
		return NewResultForCall(&transferTestValue{Text: "chained"}, frame)
	})
	require.NoError(t, err)
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	creply, err := cloud.MergeValues(ctx, "cache-a", exportTestBundle(t, actx, a, r))
	require.NoError(t, err)
	var cloudP, cloudR uint64
	for _, v := range creply.Values {
		switch storedTestFrame(t, cloud, storedBundleTestEntry(cloud, v.Number)).Field {
		case "bundle-dep":
			cloudP = v.Number
		case "held-chained":
			cloudR = v.Number
		}
	}
	require.NotZero(t, cloudP)
	require.NotZero(t, cloudR)

	bctx, b, bsrv := transferTestCache(t)
	own := persistedListTestResult(t, bctx, b, bsrv, "bundle-dep", &transferTestValue{Text: "dep"})
	alone, err := cloud.StoredBundle(ctx, []uint64{cloudP}, time.Now().Unix(), nil)
	require.NoError(t, err)
	reply, err := b.MergeValues(bctx, cloudCacheID, alone.Bundle)
	require.NoError(t, err)
	require.Len(t, reply.OtherRun, 1, "no digest on the engine's side yet")

	require.NoError(t, b.TeachContentDigest(bctx, own, content))
	b.egraphMu.RLock()
	key, _ := own.cacheSharedResult().cloudHoldingLocked()
	b.egraphMu.RUnlock()
	require.Equal(t, HolderKey{Cache: cloudCacheID, Number: cloudP}, key, "taught the record's digest, the entry learns the Cloud's copy")

	heldTestHold(t, ctx, cloud, "cache-b", uint64(own.cacheSharedResult().id), cloudP, 0)
	held, err := cloud.StoredBundle(ctx, []uint64{cloudR}, time.Now().Unix(), &HeldBy{Cache: "cache-b"})
	require.NoError(t, err)
	require.Equal(t, []uint64{cloudP}, held.Held)
	reply, err = b.MergeValues(bctx, cloudCacheID, held.Bundle)
	require.NoError(t, err)
	require.Empty(t, reply.OtherRun, "the held row stands for the entry's own value, shown the Cloud's copy")
	require.Len(t, reply.Imported(), 1, "R lands over it")
}
