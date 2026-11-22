package dagql

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// otherRunTestBundle has engine a compute a root R over P, its Cloud merge
// them, and returns the Cloud's stored bundle of R, the ordinal of P's row in
// it, and a's identity as the Cloud names it. P's frame declares that it
// follows from its inputs only when follows is set.
func otherRunTestBundle(t *testing.T, follows bool) (actx context.Context, a *Cache, bundle ValueBundle, pOrdinal uint64) {
	t.Helper()
	return otherRunTestBundleFrame(t, func(frame *ResultCall) { frame.FollowsFromInputs = follows })
}

// otherRunTestBundleFrame is otherRunTestBundle with edit applied to P's
// frame.
func otherRunTestBundleFrame(t *testing.T, edit func(*ResultCall)) (actx context.Context, a *Cache, bundle ValueBundle, pOrdinal uint64) {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	from := CacheID(a.Identity().ID)
	require.NotEmpty(t, from, "the test engine has an identity, as an engine does")
	r := persistedListTestResult(t, actx, a, asrv, "other-run-root", &transferTestValue{Text: "root"})
	p := persistedListTestResult(t, actx, a, asrv, "other-run-dep", &transferTestValue{Text: "dep"})
	transferTestDependency(a, actx, r, p)
	a.egraphMu.Lock()
	frame := p.cacheSharedResult().loadResultCall().clone()
	edit(frame)
	p.cacheSharedResult().storeResultCall(frame)
	a.egraphMu.Unlock()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	exported := exportTestBundle(t, actx, a, r)
	reply, err := cloud.MergeValues(ctx, from, exported)
	require.NoError(t, err)
	var rNumber uint64
	for _, value := range reply.Values {
		for _, sent := range exported.Values {
			if sent.Ordinal == value.Ordinal && sent.SenderNumber == uint64(r.cacheSharedResult().id) {
				rNumber = value.Number
			}
		}
	}
	require.NotZero(t, rNumber)
	built, err := cloud.StoredBundle(ctx, []uint64{rNumber}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Len(t, built.Bundle.Values, 2)
	for _, value := range built.Bundle.Values {
		require.NotNil(t, value.Run, "the Cloud sends each value's run")
		require.Equal(t, from, value.Run.Cache, "named after the engine that ran it")
		if value.Record.Call != nil && value.Record.Call.Field == "other-run-dep" {
			pOrdinal = uint64(value.Ordinal)
		}
	}
	require.NotZero(t, pOrdinal)
	return actx, a, built.Bundle, pOrdinal
}

// An engine that holds one run of a step refuses a value over another run of
// it: merge would attach the value over the engine's own run, which it
// wasn't built over. The merge commits nothing and names the row. It takes
// the same bundle where it holds nothing of the step, where the run is its
// own, and where the step follows from its inputs.
func TestAMergeRefusesAValueOverAnotherRunOfAStepTheEngineHolds(t *testing.T) {
	t.Parallel()

	t.Run("an engine that ran the step itself", func(t *testing.T) {
		t.Parallel()
		_, _, bundle, p := otherRunTestBundle(t, false)
		bctx, b, bsrv := transferTestCache(t)
		persistedListTestResult(t, bctx, b, bsrv, "other-run-dep", &transferTestValue{Text: "dep, b's run"})
		reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
		require.NoError(t, err)
		require.Equal(t, []TransferOrdinal{TransferOrdinal(p)}, reply.OtherRun)
		require.False(t, reply.Committed, "nothing of the bundle is installed")
	})
	t.Run("an engine that holds nothing of the step", func(t *testing.T) {
		t.Parallel()
		_, _, bundle, _ := otherRunTestBundle(t, false)
		bctx, b, _ := transferTestCache(t)
		reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
		require.NoError(t, err)
		require.Empty(t, reply.OtherRun)
		require.True(t, reply.Committed)
	})
	t.Run("the engine whose run it is", func(t *testing.T) {
		t.Parallel()
		actx, a, bundle, _ := otherRunTestBundle(t, false)
		reply, err := a.MergeValues(actx, cloudCacheID, bundle)
		require.NoError(t, err)
		require.Empty(t, reply.OtherRun, "its own run comes back to it")
	})
	t.Run("a step that follows from its inputs", func(t *testing.T) {
		t.Parallel()
		_, _, bundle, _ := otherRunTestBundle(t, true)
		bctx, b, bsrv := transferTestCache(t)
		persistedListTestResult(t, bctx, b, bsrv, "other-run-dep", &transferTestValue{Text: "dep, b's run"})
		reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
		require.NoError(t, err)
		require.Empty(t, reply.OtherRun, "any run of it is the same")
	})
	t.Run("an engine that holds another run it was served", func(t *testing.T) {
		t.Parallel()
		_, _, first, _ := otherRunTestBundle(t, false)
		_, _, second, p := otherRunTestBundle(t, false)
		bctx, b, _ := transferTestCache(t)
		reply, err := b.MergeValues(bctx, cloudCacheID, first)
		require.NoError(t, err)
		require.True(t, reply.Committed)
		reply, err = b.MergeValues(bctx, cloudCacheID, second)
		require.NoError(t, err)
		require.Equal(t, []TransferOrdinal{TransferOrdinal(p)}, reply.OtherRun, "the run it was served is not the second bundle's")
	})
}

// A bundle for an engine that answered it holds another run of a step
// withholds what is over that step, and names why; for another engine it
// carries it.
func TestAStoredBundleWithholdsAValueOverARunTheRecipientHoldsAnotherOf(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p, _, _ := withholdTestCloud(t, false)
	now := time.Now().Unix()
	got, err := cloud.StoredBundle(ctx, []uint64{r}, now, &HeldBy{Cache: "cache-b", OtherRun: map[uint64]bool{p: true}})
	require.NoError(t, err)
	require.Equal(t, []uint64{r}, got.Withheld)
	require.Equal(t, []uint64{p}, got.WithheldOn)
	require.Equal(t, []string{"withhold-dep/otherRun"}, got.WithheldOnField)
	got, err = cloud.StoredBundle(ctx, []uint64{r}, now, &HeldBy{Cache: "cache-c"})
	require.NoError(t, err)
	require.Empty(t, got.Withheld, "another engine holds no other run of it")
}

// secondRunTestFollows makes the Cloud's entry number's record say that its
// step follows from its inputs.
func secondRunTestFollows(t *testing.T, cloud *Cache, number uint64) {
	t.Helper()
	cloud.egraphMu.Lock()
	defer cloud.egraphMu.Unlock()
	res := cloud.resultsByID[sharedResultID(number)]
	require.NotNil(t, res)
	if call := res.loadResultCall(); call != nil {
		frame := call.clone()
		frame.FollowsFromInputs = true
		res.storeResultCall(frame)
	}
	if res.stored != nil {
		res.stored.follows = true
	}
	require.False(t, cloud.entryCanDifferLocked(res))
}

// A bundle for an engine that holds one run of a step withholds what was
// built over another run of it, before any merge has to refuse it: the
// engine would keep its own run (mergeKeep) and land the value over it.
// The value goes to an engine that holds that run, or no run of the step,
// and to anyone once the step follows from its inputs.
func TestAStoredBundleWithholdsAValueOverAnotherRunOfAStepItsRecipientHolds(t *testing.T) {
	t.Parallel()
	for _, follows := range []bool{false, true} {
		t.Run(map[bool]string{false: "can differ", true: "follows from its inputs"}[follows], func(t *testing.T) {
			t.Parallel()
			s := newSecondRunTestSetup(t)
			bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
			second := secondRunTestNumber(bundle, reply, s.bStep)
			root := secondRunTestNumber(bundle, reply, s.bRoot)
			require.NotZero(t, root)
			require.NotEqual(t, s.first, second, "cache-b's run is an entry of its own")
			// cache-c's own call row of the step lands on the recipe's
			// current entry: it holds the first run.
			cKey := HolderKey{Cache: "cache-c", Number: 1}
			_, err := s.cloud.AttachRemoteHolding(s.cctx, cKey, RemoteHolding{Recipe: s.recipe})
			require.NoError(t, err)
			held, ok := s.cloud.HolderEntry(cKey)
			require.True(t, ok)
			require.Equal(t, s.first, held, "cache-c holds the first run")
			if follows {
				secondRunTestFollows(t, s.cloud, s.first)
				secondRunTestFollows(t, s.cloud, second)
			}

			now := time.Now().Unix()
			got, err := s.cloud.StoredBundle(s.cctx, []uint64{root}, now, &HeldBy{Cache: "cache-c"})
			require.NoError(t, err)
			if follows {
				require.Empty(t, got.Withheld, "another run of a step that follows from its inputs is the same")
			} else {
				require.Equal(t, []uint64{root}, got.Withheld)
				require.Equal(t, []uint64{second}, got.WithheldOn)
				require.Equal(t, []string{"second-run-step/otherRun"}, got.WithheldOnField)
			}
			for _, cache := range []CacheID{"cache-b", "cache-d"} {
				got, err := s.cloud.StoredBundle(s.cctx, []uint64{root}, now, &HeldBy{Cache: cache})
				require.NoError(t, err)
				require.Empty(t, got.Withheld, "%s holds no other run of the step", cache)
			}
		})
	}
}

// heldRowTestCloud has engine a compute R over P, where P's step can
// differ, and its Cloud merge them. Engine b computes its own run of P,
// and its call row of it reaches the Cloud before any export of b's, as it
// usually does: b's holding sits on a's entry of P. It returns b and the
// Cloud's numbers for R and P.
func heldRowTestCloud(t *testing.T) (cctx context.Context, cloud *Cache, bctx context.Context, b *Cache, rNumber, pNumber uint64, bKey HolderKey) {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "held-run-root", &transferTestValue{Text: "root over a's run"})
	p := secondRunTestStep(t, actx, a, asrv, "held-run-dep", "a's run")
	transferTestDependency(a, actx, r, p)
	cctx, cloud = storedPartTestCache(t, WithBlobStore())
	bundle, reply := secondRunTestExport(t, actx, a, nil, cloud, cctx, CacheID(a.Identity().ID), r)
	rNumber, pNumber = secondRunTestNumber(bundle, reply, r), secondRunTestNumber(bundle, reply, p)
	require.NotZero(t, rNumber)
	require.NotZero(t, pNumber)

	bctx, b, bsrv := transferTestCache(t)
	bp := secondRunTestStep(t, bctx, b, bsrv, "held-run-dep", "b's run")
	bKey = HolderKey{Cache: CacheID(b.Identity().ID), Number: uint64(bp.cacheSharedResult().id)}
	_, err := cloud.AttachRemoteHolding(cctx, bKey, RemoteHolding{
		Recipe:   secondRunTestRecipe(t, cloud, pNumber),
		Parts:    []PersistedPartAddress{testLiveOffer().Address},
		Executed: true,
		CallRow:  true,
	})
	require.NoError(t, err)
	held, ok := cloud.HolderEntry(bKey)
	require.True(t, ok)
	require.Equal(t, pNumber, held, "b's call row lands on a's entry of the step")
	return cctx, cloud, bctx, b, rNumber, pNumber, bKey
}

// An engine whose own run of a step the Cloud keeps folded under another
// run's entry holds another run of it: a bundle for it withholds what is
// over that entry, which another engine still gets.
func TestAStoredBundleWithholdsAValueOverAStepTheRecipientRanItself(t *testing.T) {
	t.Parallel()
	cctx, cloud, _, b, r, p, bKey := heldRowTestCloud(t)
	now := time.Now().Unix()
	got, err := cloud.StoredBundle(cctx, []uint64{r}, now, &HeldBy{Cache: CacheID(b.Identity().ID)})
	require.NoError(t, err)
	require.Equal(t, []uint64{r}, got.Withheld)
	require.Equal(t, []uint64{p}, got.WithheldOn)
	require.Equal(t, []string{"held-run-dep/otherRun"}, got.WithheldOnField)
	got, err = cloud.StoredBundle(cctx, []uint64{r}, now, &HeldBy{Cache: "cache-c"})
	require.NoError(t, err)
	require.Empty(t, got.Withheld, "another engine holds no run of it")

	// Once b's own run is gone, b holds no other run of the step.
	_, gone, err := cloud.CollectGoneHoldings(cctx, []HolderKey{bKey})
	require.NoError(t, err)
	require.Equal(t, []bool{true}, gone)
	got, err = cloud.StoredBundle(cctx, []uint64{r}, now, &HeldBy{Cache: CacheID(b.Identity().ID)})
	require.NoError(t, err)
	require.Empty(t, got.Withheld, "b dropped its own run")

	// Then b is served a's run: its new holding is of the run the entry
	// stores, not of b's own folded one.
	served := HolderKey{Cache: bKey.Cache, Number: bKey.Number + 1000}
	_, err = cloud.AttachRemoteHolding(cctx, served, RemoteHolding{Recipe: secondRunTestRecipe(t, cloud, p)})
	require.NoError(t, err)
	held, ok := cloud.HolderEntry(served)
	require.True(t, ok)
	require.Equal(t, p, held)
	got, err = cloud.StoredBundle(cctx, []uint64{r}, now, &HeldBy{Cache: CacheID(b.Identity().ID)})
	require.NoError(t, err)
	require.Empty(t, got.Withheld, "b holds a's run now")
}

// A bundle sends a step its recipient holds as a held row, which lands on
// the recipient's own entry of the step. The Cloud names the run it
// believes that is, so an engine that holds another run refuses the value
// over it, even when the Cloud doesn't know yet.
func TestAMergeRefusesAHeldRowOfAnotherRun(t *testing.T) {
	t.Parallel()
	cctx, cloud, bctx, b, r, _, _ := heldRowTestCloud(t)
	// As though the Cloud didn't know yet that b ran the step itself: only
	// the engine can tell.
	cloud.egraphMu.Lock()
	for _, res := range cloud.resultsByID {
		res.foldedRuns = nil
	}
	cloud.egraphMu.Unlock()
	sent, err := cloud.StoredBundle(cctx, []uint64{r}, time.Now().Unix(), &HeldBy{Cache: CacheID(b.Identity().ID)})
	require.NoError(t, err)
	require.Empty(t, sent.Withheld)
	var held TransferOrdinal
	for _, value := range sent.Bundle.Values {
		if value.Held != "" {
			held = value.Ordinal
			require.NotNil(t, value.Run, "the Cloud names the held row's run")
		}
	}
	require.NotZero(t, held, "the step goes as a held row")
	reply, err := b.MergeValues(bctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	require.Equal(t, []TransferOrdinal{held}, reply.OtherRun)
	require.False(t, reply.Committed, "nothing of the bundle is installed")
}

// An engine's image entries are no run of its own: it installed them at
// start, and the Cloud names the run of the engine that sent it the same
// records. An installed entry whose run the engine doesn't know refuses
// nothing, after the engine restarts as before.
func TestAMergeOverAnImageEntryIsNotRefused(t *testing.T) {
	t.Parallel()
	for _, restarted := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "after a restart"}[restarted], func(t *testing.T) {
			t.Parallel()
			actx, a, asrv := transferTestCache(t)
			r := persistedListTestResult(t, actx, a, asrv, "image-root", &transferTestValue{Text: "root"})
			p := secondRunTestStep(t, actx, a, asrv, "image-dep", "the image's run")
			transferTestDependency(a, actx, r, p)
			cctx, cloud := storedPartTestCache(t, WithBlobStore())
			bundle, reply := secondRunTestExport(t, actx, a, nil, cloud, cctx, CacheID(a.Identity().ID), r)
			root := secondRunTestNumber(bundle, reply, r)
			require.NotZero(t, root)

			path := filepath.Join(t.TempDir(), "b.db")
			bctx, b := reopenTransferTestCache(t, path)
			installed, err := b.MergeValues(bctx, imageCacheID, exportTestBundle(t, actx, a, p))
			require.NoError(t, err)
			require.True(t, installed.Committed, "the image's entry installs")
			if restarted {
				require.NoError(t, b.Close(context.Background()))
				bctx, b = reopenTransferTestCache(t, path)
			}
			t.Cleanup(func() { _ = b.Close(context.Background()) })
			sent, err := cloud.StoredBundle(cctx, []uint64{root}, time.Now().Unix(), nil)
			require.NoError(t, err)
			merged, err := b.MergeValues(bctx, cloudCacheID, sent.Bundle)
			require.NoError(t, err)
			require.Empty(t, merged.OtherRun)
			require.True(t, merged.Committed)
		})
	}
}

// An engine's own value that replaces an image entry in place is its own run,
// though the entry it replaced was a run it couldn't tell: it refuses a value
// over another run of the step.
func TestAnEnginesOwnRunInPlaceOfAnImageEntryRefusesAnotherRun(t *testing.T) {
	t.Parallel()
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "image-root", &transferTestValue{Text: "root"})
	p := secondRunTestStep(t, actx, a, asrv, "image-dep", "the image's run")
	transferTestDependency(a, actx, r, p)
	cctx, cloud := storedPartTestCache(t, WithBlobStore())
	bundle, reply := secondRunTestExport(t, actx, a, nil, cloud, cctx, CacheID(a.Identity().ID), r)
	root := secondRunTestNumber(bundle, reply, r)
	require.NotZero(t, root)

	bctx, b, bsrv := transferTestCache(t)
	installed, err := b.MergeValues(bctx, imageCacheID, exportTestBundle(t, actx, a, p))
	require.NoError(t, err)
	require.True(t, installed.Committed)
	image := installed.Imported()[0].ResultID
	b.egraphMu.RLock()
	entry := b.resultsByID[sharedResultID(image)]
	b.egraphMu.RUnlock()
	mergeTestExpire(b, entry)
	own := secondRunTestStep(t, bctx, b, bsrv, "image-dep", "b's own run")
	require.Equal(t, image, uint64(own.cacheSharedResult().id), "b's own value replaces the expired image entry in place")

	sent, err := cloud.StoredBundle(cctx, []uint64{root}, time.Now().Unix(), nil)
	require.NoError(t, err)
	merged, err := b.MergeValues(bctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	require.NotEmpty(t, merged.OtherRun, "the value is over a's run, not b's")
	require.False(t, merged.Committed)
}
