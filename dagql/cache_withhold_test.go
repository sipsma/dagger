package dagql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// withholdTestCloud merges an export of a root R over P from cache-a into a
// new Cloud, P's frame declaring that it follows from its inputs only when
// follows is set. It returns the Cloud's numbers for R and P and the
// holding of P's run in cache-a, at its replacement count.
func withholdTestCloud(t *testing.T, follows bool) (context.Context, *Cache, uint64, uint64, HolderKey, uint64) {
	t.Helper()
	return withholdTestCloudFrame(t, func(frame *ResultCall) { frame.Follows = testFollows(follows) })
}

// withholdTestCloudFrame is withholdTestCloud with edit applied to P's frame.
func withholdTestCloudFrame(t *testing.T, edit func(*ResultCall)) (context.Context, *Cache, uint64, uint64, HolderKey, uint64) {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "withhold-root", &transferTestValue{Text: "root"})
	p := persistedListTestResult(t, actx, a, asrv, "withhold-dep", &transferTestValue{Text: "dep"})
	transferTestDependency(a, actx, r, p)
	a.egraphMu.Lock()
	frame := p.cacheSharedResult().loadResultCall().clone()
	edit(frame)
	p.cacheSharedResult().storeResultCall(frame)
	a.egraphMu.Unlock()
	bundle := exportTestBundle(t, actx, a, r)
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	var rNumber, pNumber uint64
	var pKey HolderKey
	var pReplacements uint64
	for _, value := range reply.Values {
		for _, sent := range bundle.Values {
			if sent.Ordinal != value.Ordinal {
				continue
			}
			switch sent.SenderNumber {
			case uint64(r.cacheSharedResult().id):
				rNumber = value.Number
			case uint64(p.cacheSharedResult().id):
				pNumber = value.Number
				pKey = HolderKey{Cache: "cache-a", Number: sent.SenderNumber}
				pReplacements = sent.SenderReplacements
			}
		}
	}
	require.NotZero(t, rNumber)
	require.NotZero(t, pNumber)
	return ctx, cloud, rNumber, pNumber, pKey, pReplacements
}

// withholdTestRan reports P's run as having run: its holding's call
// executed and reported its snapshot complete, with follows as the row's
// declaration.
func withholdTestRan(t *testing.T, ctx context.Context, cloud *Cache, p uint64, key HolderKey, replacements uint64, follows bool) {
	t.Helper()
	recipe, ok := cloud.EntryRecipe(p)
	require.True(t, ok)
	_, err := cloud.AttachRemoteHolding(ctx, key, RemoteHolding{
		Recipe:            recipe,
		Parts:             []PersistedPartAddress{testLiveOffer().Address},
		Replacements:      replacements,
		Executed:          true,
		FollowsFromInputs: follows,
		CallRow:           true,
	})
	require.NoError(t, err)
	require.True(t, runTestRan(t, cloud, p).Ran)
}

// A value goes out only with the run of each step below it whole, where the
// step can differ: an engine that took it and then ran a step itself, for
// want of its content, would hold one run's values over another run's
// bytes. So a stored bundle withholds a root over a step that ran, can
// differ and stores its run only in part, and carries it once that run is
// stored whole, or when the step follows from its inputs, or its run never
// ran.
func TestStoredBundleWithholdsAValueOverARunStoredInPart(t *testing.T) {
	t.Parallel()
	now := time.Now().Unix()

	t.Run("a step that ran, can differ and stores its run in part", func(t *testing.T) {
		t.Parallel()
		ctx, cloud, r, p, key, replacements := withholdTestCloud(t, false)
		withholdTestRan(t, ctx, cloud, p, key, replacements, false)
		got, err := cloud.StoredBundle(ctx, []uint64{r, p}, now, nil)
		require.NoError(t, err)
		require.Equal(t, []uint64{r, p}, got.Withheld)
		require.Equal(t, []uint64{p, p}, got.WithheldOn, "each waits on P's run")
		require.Equal(t, []string{"withhold-dep/undeclared", "withhold-dep/undeclared"}, got.WithheldOnField)
		require.Empty(t, got.Bundle.Values)
		require.Equal(t, []bool{false}, cloud.ChangedSince([]uint64{p}, got.Position), "the answer is as of the cache's count, though it carries nothing")

		stored, _, err := cloud.SetStoredPart(ctx, p, testLiveOffer(), 0)
		require.NoError(t, err)
		require.True(t, stored)
		got, err = cloud.StoredBundle(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Empty(t, got.Withheld, "the run is stored whole")
		require.Len(t, got.Bundle.Values, 2)
	})
	t.Run("a step whose record declares it follows from its inputs", func(t *testing.T) {
		t.Parallel()
		ctx, cloud, r, p, key, replacements := withholdTestCloud(t, true)
		withholdTestRan(t, ctx, cloud, p, key, replacements, true)
		got, err := cloud.StoredBundle(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Empty(t, got.Withheld)
		require.Len(t, got.Bundle.Values, 2)
	})
	t.Run("a step whose compressed record declares it follows from its inputs", func(t *testing.T) {
		t.Parallel()
		ctx, cloud, r, p, key, replacements := withholdTestCloud(t, true)
		withholdTestRan(t, ctx, cloud, p, key, replacements, true)
		storedTestCompress(cloud)
		cloud.egraphMu.RLock()
		body := storedBundleTestEntry(cloud, p).stored
		cloud.egraphMu.RUnlock()
		require.NotNil(t, body)
		require.NotNil(t, body.frame, "the body holds the frame")
		got, err := cloud.StoredBundle(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Empty(t, got.Withheld)
	})
	t.Run("a record and a holding that disagree", func(t *testing.T) {
		t.Parallel()
		ctx, cloud, r, p, key, replacements := withholdTestCloud(t, true)
		withholdTestRan(t, ctx, cloud, p, key, replacements, false)
		got, err := cloud.StoredBundle(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Equal(t, []uint64{r}, got.Withheld, "it can differ")
	})
	t.Run("a step whose run never ran", func(t *testing.T) {
		t.Parallel()
		ctx, cloud, r, _, _, _ := withholdTestCloud(t, false)
		got, err := cloud.StoredBundle(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Empty(t, got.Withheld, "the engine that runs it makes the only run")
		require.Len(t, got.Bundle.Values, 2)
	})
}

// The parts of a run are the ones its stored record declares, besides its
// metadata: the rule withholds over a run until the entry stores each, and
// selection reads the same parts, on the entry's facts and its holdings', to
// store the run whole. They don't change with what the entry stores, or with
// the record's compression.
func TestARunsPartsAreTheOnesItsRecordDeclares(t *testing.T) {
	t.Parallel()
	ctx, cloud, _, p, key, replacements := withholdTestCloud(t, false)
	withholdTestRan(t, ctx, cloud, p, key, replacements, false)
	want := []PersistedPartAddress{testLiveOffer().Address}
	check := func(when string) {
		t.Helper()
		info, ok := cloud.EntryInfo(p)
		require.True(t, ok)
		require.True(t, info.RunPartsKnown, when)
		require.Equal(t, want, info.RunParts, when)
		facts, ok := cloud.HoldingFacts(key)
		require.True(t, ok)
		require.True(t, facts.RunPartsKnown, when)
		require.Equal(t, want, facts.RunParts, when)
		held, ok := cloud.RemoteHoldingInfo(key)
		require.True(t, ok)
		require.Equal(t, want, held.RunParts, when)
		entries := cloud.EntriesFacts([]uint64{p})
		require.Len(t, entries, 1)
		require.Equal(t, want, entries[0].RunParts, when)
	}
	check("stored in part")
	stored, _, err := cloud.SetStoredPart(ctx, p, testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)
	check("stored whole")
	storedTestCompress(cloud)
	check("compressed")
}

// A call goes with its input, which an engine installs from the bundle: a
// call whose input's run ran and is stored only in part isn't sent, though
// the value it reaches, which isn't over that input, still is. So too where
// the recipient holds the input: its holding may be a record of a step it
// would run itself.
func TestStoredBundleSendsNoCallOverAWithheldInput(t *testing.T) {
	t.Parallel()
	for name, held := range map[string]func(*receiverCallTest) *HeldBy{
		"an input the bundle adds": func(*receiverCallTest) *HeldBy { return nil },
		"an input the recipient holds": func(test *receiverCallTest) *HeldBy {
			return &HeldBy{Cache: "cache-x", Also: func(number uint64) bool { return number == test.receiverNumber }}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			withholdTestCallOverAWithheldInput(t, held)
		})
	}
}

func withholdTestCallOverAWithheldInput(t *testing.T, held func(*receiverCallTest) *HeldBy) {
	test := newReceiverCallTest(t)
	run, ok := test.cloud.EntryRun(test.receiverNumber)
	require.True(t, ok)
	recipe, ok := test.cloud.EntryRecipe(test.receiverNumber)
	require.True(t, ok)
	_, err := test.cloud.AttachRemoteHolding(test.ctx, HolderKey{Cache: run.Run.Cache, Number: run.Run.Number}, RemoteHolding{
		Recipe:       recipe,
		Parts:        []PersistedPartAddress{testLiveOffer().Address},
		Replacements: run.Run.Replacements,
		Executed:     true,
	})
	require.NoError(t, err)
	require.True(t, runTestRan(t, test.cloud, test.receiverNumber).Ran)

	built, err := test.cloud.StoredBundle(test.ctx, []uint64{test.number}, time.Now().Unix(), held(test))
	require.NoError(t, err)
	require.Empty(t, built.Withheld, "the value isn't over the call's input")
	require.Empty(t, built.Bundle.Calls)
	require.Empty(t, built.CallInputs)
	require.Equal(t, 1, built.UnsendableCalls)
}

// What StoredClosure and StoredLowerHalf list of a bundle without building
// it, and what StoredSizes prices when asked to withhold, is what
// StoredBundle carries: nothing of a root it withholds, and no input of a
// call it doesn't send. A set file cuts a root priced over its part bound
// into parts of the records StoredClosure lists below it, ahead of the
// root's own part; a root that part then withholds would leave those records
// with nothing the file retains over them, which an engine that restores the
// file drops. Plain pricing, placement's, counts them.
func TestStoredListingsAndWithholdingPricesCountNothingABundleWithholds(t *testing.T) {
	t.Parallel()
	t.Run("a withheld root", func(t *testing.T) {
		t.Parallel()
		ctx, cloud, r, p, key, replacements := withholdTestCloud(t, false)
		withholdTestRan(t, ctx, cloud, p, key, replacements, false)
		now := time.Now().Unix()
		built, err := cloud.StoredBundle(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Equal(t, []uint64{r}, built.Withheld)

		closure, err := cloud.StoredClosure(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Empty(t, closure.Entries)
		sizes, err := cloud.StoredSizes(ctx, []uint64{r}, now, nil, StoredSizesWithholding())
		require.NoError(t, err)
		require.Equal(t, StoredSize{}, sizes[0], "priced as withheld, the root adds nothing")
		sizes, err = cloud.StoredSizes(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Equal(t, 2, sizes[0].Records, "plain pricing counts R and P")
		lower, carried, err := cloud.StoredLowerHalf(ctx, r, now, nil)
		require.NoError(t, err)
		require.Empty(t, lower)
		require.Zero(t, carried)

		stored, _, err := cloud.SetStoredPart(ctx, p, testLiveOffer(), 0)
		require.NoError(t, err)
		require.True(t, stored)
		built, err = cloud.StoredBundle(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Empty(t, built.Withheld, "once P's run is stored whole, the root goes")
		closure, err = cloud.StoredClosure(ctx, []uint64{r}, now, nil)
		require.NoError(t, err)
		require.Len(t, closure.Entries, len(built.Bundle.Values))
		sizes, err = cloud.StoredSizes(ctx, []uint64{r}, now, nil, StoredSizesWithholding())
		require.NoError(t, err)
		require.Equal(t, len(built.Bundle.Values), sizes[0].Records)
	})
	t.Run("a call over a withheld input", func(t *testing.T) {
		t.Parallel()
		test := newReceiverCallTest(t)
		run, ok := test.cloud.EntryRun(test.receiverNumber)
		require.True(t, ok)
		recipe, ok := test.cloud.EntryRecipe(test.receiverNumber)
		require.True(t, ok)
		_, err := test.cloud.AttachRemoteHolding(test.ctx, HolderKey{Cache: run.Run.Cache, Number: run.Run.Number}, RemoteHolding{
			Recipe:       recipe,
			Parts:        []PersistedPartAddress{testLiveOffer().Address},
			Replacements: run.Run.Replacements,
			Executed:     true,
		})
		require.NoError(t, err)
		now := time.Now().Unix()
		built, err := test.cloud.StoredBundle(test.ctx, []uint64{test.number}, now, nil)
		require.NoError(t, err)
		require.Empty(t, built.Withheld)
		require.Empty(t, built.CallInputs, "the call over the withheld input isn't sent")

		sizes, err := test.cloud.StoredSizes(test.ctx, []uint64{test.number}, now, nil, StoredSizesWithholding())
		require.NoError(t, err)
		require.Equal(t, len(built.Bundle.Values), sizes[0].Records, "priced as withheld, the input isn't counted")
		closure, err := test.cloud.StoredClosure(test.ctx, []uint64{test.number}, now, nil)
		require.NoError(t, err)
		require.Empty(t, closure.CallInputs)
		require.Len(t, closure.Entries, len(built.Bundle.Values), "the input isn't listed")
		_, carried, err := test.cloud.StoredLowerHalf(test.ctx, test.number, now, nil)
		require.NoError(t, err)
		require.Equal(t, len(built.Bundle.Values), carried)
	})
}

// A saved call's inputs join the bundle as roots of their own, so they too
// carry one run of a step: a call whose input is another run of a step the
// bundle already carries isn't sent, though the value it reaches still is.
func TestStoredBundleSendsNoCallOverAnotherRunTheBundleCarries(t *testing.T) {
	t.Parallel()
	test := newReceiverCallTest(t)
	bctx, b, bsrv := transferTestCache(t)
	bReceiver, err := b.GetOrInitCall(bctx, "test-session", bsrv, &CallRequest{ResultCall: receiverTestFrame("a"), IsPersistable: true},
		func(context.Context) (AnyResult, error) {
			return NewResultForCall(&transferTestValue{Text: "receiver, b's run"}, receiverTestFrame("a"))
		})
	require.NoError(t, err)
	bRoot := secondRunTestStep(t, bctx, b, bsrv, "over-b-receiver", "b-root")
	transferTestDependency(b, bctx, bRoot, bReceiver)
	bundle, reply := secondRunTestExport(t, bctx, b, nil, test.cloud, test.ctx, "cache-b", bRoot)
	second, root := secondRunTestNumber(bundle, reply, bReceiver), secondRunTestNumber(bundle, reply, bRoot)
	require.NotZero(t, root)
	require.NotEqual(t, test.receiverNumber, second, "b's receiver is a run of its own")
	now := time.Now().Unix()

	alone, err := test.cloud.StoredBundle(test.ctx, []uint64{test.number}, now, nil)
	require.NoError(t, err)
	require.Len(t, alone.Bundle.Calls, 1, "alone, the call goes with a's receiver")

	built, err := test.cloud.StoredBundle(test.ctx, []uint64{root, test.number}, now, nil)
	require.NoError(t, err)
	require.Empty(t, built.Withheld, "neither value is over the other run")
	require.Empty(t, built.Bundle.Calls, "the call's input is another run of b's receiver")
	require.Empty(t, built.CallInputs)
	require.Equal(t, 1, built.UnsendableCalls)
}

// testFollows is the claim a test's frame makes: that it follows from its
// inputs when follows is set, and nothing otherwise.
func testFollows(follows bool) FollowsClaim {
	if follows {
		return FollowsInputs
	}
	return ""
}
