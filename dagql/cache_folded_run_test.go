package dagql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Two engines each run a step that can give different bytes. The second
// builds a result only it makes, T, over its own run of the step. The Cloud
// folds the second's run of the step into the first's entry, since their
// recipe is the same, so T's record names that entry. A third engine asking
// for T must not be answered over the first engine's run: T was built over
// the second's.
func TestAResultOverAFoldedRunIsNotAnsweredOverAnotherRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())

	// Engine a runs the step S and exports it.
	actx, a, asrv := transferTestCache(t)
	fromA := CacheID(a.Identity().ID)
	sA := persistedListTestResult(t, actx, a, asrv, "folded-step", &transferTestValue{Text: "a's bytes"})
	canDiffer(t, a, sA)
	exportedA := exportTestBundle(t, actx, a, sA)
	replyA, err := cloud.MergeValues(ctx, fromA, exportedA)
	require.NoError(t, err)
	sNumber := cloudNumberOf(t, exportedA, replyA, sA)

	// Engine b runs S itself, with other bytes, and builds T over its run.
	bctx, b, bsrv := transferTestCache(t)
	fromB := CacheID(b.Identity().ID)
	sB := persistedListTestResult(t, bctx, b, bsrv, "folded-step", &transferTestValue{Text: "b's bytes"})
	canDiffer(t, b, sB)
	top := persistedListTestResult(t, bctx, b, bsrv, "folded-top", &transferTestValue{Text: "built over b's bytes"})
	transferTestDependency(b, bctx, top, sB)
	exportedB := exportTestBundle(t, bctx, b, top)
	replyB, err := cloud.MergeValues(ctx, fromB, exportedB)
	require.NoError(t, err)
	tNumber := cloudNumberOf(t, exportedB, replyB, top)
	require.Equal(t, sNumber, cloudNumberOf(t, exportedB, replyB, sB), "the Cloud folds b's run of S into a's entry")

	// a's run of S ran and is stored whole.
	cloud.egraphMu.RLock()
	sRun := *cloud.resultsByID[sharedResultID(sNumber)].run
	cloud.egraphMu.RUnlock()
	require.Equal(t, fromA, sRun.Cache, "S's entry names a's run")
	withholdTestRan(t, ctx, cloud, sNumber, HolderKey{Cache: fromA, Number: uint64(sA.cacheSharedResult().id)}, sRun.Replacements, false)
	stored, _, err := cloud.SetStoredPart(ctx, sNumber, testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)

	// A third engine asks for T.
	built, err := cloud.StoredBundle(ctx, []uint64{tNumber}, time.Now().Unix(), nil)
	require.NoError(t, err)
	carriedOverA := false
	for _, value := range built.Bundle.Values {
		if value.SenderNumber == sNumber {
			carriedOverA = true
		}
	}
	require.False(t, carriedOverA && !containsUint64(built.Withheld, tNumber), "T, built over b's run of S, is answered over a's run of S")
}

// canDiffer clears res's declaration that it follows from its inputs: its
// step can give different bytes run again.
func canDiffer(t *testing.T, c *Cache, res AnyResult) {
	t.Helper()
	c.egraphMu.Lock()
	defer c.egraphMu.Unlock()
	frame := res.cacheSharedResult().loadResultCall().clone()
	frame.FollowsFromInputs = false
	res.cacheSharedResult().storeResultCall(frame)
}

// cloudNumberOf returns the Cloud's number for res, which bundle sent and
// reply answered.
func cloudNumberOf(t *testing.T, bundle ValueBundle, reply MergeReply, res AnyResult) uint64 {
	t.Helper()
	for _, sent := range bundle.Values {
		if sent.SenderNumber != uint64(res.cacheSharedResult().id) {
			continue
		}
		for _, value := range reply.Values {
			if value.Ordinal == sent.Ordinal {
				return value.Number
			}
		}
	}
	t.Fatalf("no Cloud number for entry %d", res.cacheSharedResult().id)
	return 0
}

func containsUint64(xs []uint64, x uint64) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
