package dagql

import (
	"context"
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
	actx, a, asrv := transferTestCache(t)
	from := CacheID(a.Identity().ID)
	require.NotEmpty(t, from, "the test engine has an identity, as an engine does")
	r := persistedListTestResult(t, actx, a, asrv, "other-run-root", &transferTestValue{Text: "root"})
	p := persistedListTestResult(t, actx, a, asrv, "other-run-dep", &transferTestValue{Text: "dep"})
	transferTestDependency(a, actx, r, p)
	a.egraphMu.Lock()
	frame := p.cacheSharedResult().loadResultCall().clone()
	frame.FollowsFromInputs = follows
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
