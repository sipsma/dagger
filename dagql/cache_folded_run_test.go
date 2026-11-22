package dagql

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/dagql/call"
)

// Two engines each run a step that can give different bytes. The second
// builds a result only it makes, T, over its own run of the step. The Cloud
// folds the second's run of the step into the first's entry, since their
// recipe is the same, so T's record names that entry. A third engine asking
// for T must not be answered over the first engine's run: T was built over
// the second's. That holds across a save of the Cloud's records, whole or
// compressed, and a restart.
func TestAResultOverAFoldedRunIsNotAnsweredOverAnotherRun(t *testing.T) {
	t.Parallel()
	for _, saved := range []string{"unsaved", "saved whole", "saved compressed"} {
		t.Run(saved, func(t *testing.T) {
			t.Parallel()
			foldedRunTest(t, saved)
		})
	}
}

func foldedRunTest(t *testing.T, saved string) {
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)

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
	// b ran its own run of S: its holding of the folded entry ran its value.
	foldedRunTestHolding(t, ctx, cloud, sNumber, HolderKey{Cache: fromB, Number: uint64(sB.cacheSharedResult().id)}, true)

	if saved != "unsaved" {
		if saved == "saved compressed" {
			storedTestCompress(cloud)
		}
		require.NoError(t, cloud.Close(ctx))
		ctx, cloud = storedPersistenceTestCloud(t, path)
	}
	defer func() { require.NoError(t, cloud.Close(ctx)) }()

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

// Two engines' runs of a step under different recipes, which an extra
// digest joins into one class though their bytes differ. The second engine
// builds a result only it makes over its run, whose own content the Cloud
// doesn't store, while the first's is stored whole. The result must not be
// answered with the first run's content borrowed across the class.
func TestAResultIsNotAnsweredOverAClassMatesRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	bridge := call.ExtraDigest{Digest: digest.FromString("bridged-step"), Label: "aux"}

	actx, a, asrv := transferTestCache(t)
	fromA := CacheID(a.Identity().ID)
	sA := persistedListTestResult(t, actx, a, asrv, "class-step-a", &transferTestValue{Text: "a's bytes"})
	bridged(t, a, sA, bridge)
	exportedA := exportTestBundle(t, actx, a, sA)
	replyA, err := cloud.MergeValues(ctx, fromA, exportedA)
	require.NoError(t, err)
	sANumber := cloudNumberOf(t, exportedA, replyA, sA)

	bctx, b, bsrv := transferTestCache(t)
	fromB := CacheID(b.Identity().ID)
	sB := persistedListTestResult(t, bctx, b, bsrv, "class-step-b", &transferTestValue{Text: "b's bytes"})
	bridged(t, b, sB, bridge)
	top := persistedListTestResult(t, bctx, b, bsrv, "class-top", &transferTestValue{Text: "built over b's bytes"})
	transferTestDependency(b, bctx, top, sB)
	exportedB := exportTestBundle(t, bctx, b, top)
	replyB, err := cloud.MergeValues(ctx, fromB, exportedB)
	require.NoError(t, err)
	sBNumber := cloudNumberOf(t, exportedB, replyB, sB)
	tNumber := cloudNumberOf(t, exportedB, replyB, top)
	require.NotEqual(t, sANumber, sBNumber, "two recipes, two entries")

	cloud.egraphMu.RLock()
	joined := false
	for root := range cloud.outputEqClassRootsLocked(sharedResultID(sBNumber)) {
		if _, ok := cloud.outputEqClassResults[root][sharedResultID(sANumber)]; ok {
			joined = true
		}
	}
	cloud.egraphMu.RUnlock()
	require.True(t, joined, "the extra digest joins the two runs in one class")

	for _, run := range []struct {
		number uint64
		key    HolderKey
	}{{sANumber, HolderKey{Cache: fromA, Number: uint64(sA.cacheSharedResult().id)}}, {sBNumber, HolderKey{Cache: fromB, Number: uint64(sB.cacheSharedResult().id)}}} {
		withholdTestRan(t, ctx, cloud, run.number, run.key, 0, false)
	}
	stored, _, err := cloud.SetStoredPart(ctx, sANumber, testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored, "a's run is stored whole; b's isn't")

	built, err := cloud.StoredBundle(ctx, []uint64{tNumber}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Contains(t, built.Withheld, tNumber, "T waits for b's own run to be stored")
	require.Equal(t, []uint64{sBNumber}, built.WithheldOn)
}

// bridged makes res's step one that can differ, and gives its frame extra.
func bridged(t *testing.T, c *Cache, res AnyResult, extra call.ExtraDigest) {
	t.Helper()
	c.egraphMu.Lock()
	defer c.egraphMu.Unlock()
	frame := res.cacheSharedResult().loadResultCall().clone()
	frame.FollowsFromInputs = false
	frame.ExtraDigests = append(frame.ExtraDigests, extra)
	res.cacheSharedResult().storeResultCall(frame)
}

// foldedRunTestHolding attaches engine key's holding to the Cloud entry
// number, whose recipe it holds, as a call row reports it: executed when the
// engine ran its own run of the step, and otherwise a value it was served.
func foldedRunTestHolding(t *testing.T, ctx context.Context, cloud *Cache, number uint64, key HolderKey, executed bool) {
	t.Helper()
	recipe, ok := cloud.EntryRecipe(number)
	require.True(t, ok)
	_, err := cloud.AttachRemoteHolding(ctx, key, RemoteHolding{
		Recipe:   recipe,
		Parts:    []PersistedPartAddress{testLiveOffer().Address},
		Executed: executed,
		CallRow:  true,
	})
	require.NoError(t, err)
}

// A step's record goes to the Cloud before the step runs, as the records
// stream sends a lazy chain: engine b's step S, pending, under a result T
// only b makes. The Cloud folds b's S into a's entry, whose run is stored
// whole. Whether T stands over another run is known only once b's S is
// evaluated: if b's lookup is answered with a's run, T is built over a's
// content and is answered to a third engine; if b runs S itself, T is built
// over b's run and isn't, though T's record was installed before that was
// known and nothing installs it again.
func TestAFoldIsDecidedWhenTheSendersRunIsKnown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		ranOwn   bool
		withheld bool
	}{
		{"b is served a's run of S", false, false},
		{"b runs S itself after T's record is installed", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cloud := storedPartTestCache(t, WithBlobStore())

			actx, a, asrv := transferTestCache(t)
			fromA := CacheID(a.Identity().ID)
			sA := persistedListTestResult(t, actx, a, asrv, "decided-step", &transferTestValue{Text: "a's bytes"})
			canDiffer(t, a, sA)
			exportedA := exportTestBundle(t, actx, a, sA)
			replyA, err := cloud.MergeValues(ctx, fromA, exportedA)
			require.NoError(t, err)
			sNumber := cloudNumberOf(t, exportedA, replyA, sA)
			withholdTestRan(t, ctx, cloud, sNumber, HolderKey{Cache: fromA, Number: uint64(sA.cacheSharedResult().id)}, 0, false)
			stored, _, err := cloud.SetStoredPart(ctx, sNumber, testLiveOffer(), 0)
			require.NoError(t, err)
			require.True(t, stored)

			// b's records go up before anything of b's ran.
			bctx, b, bsrv := transferTestCache(t)
			fromB := CacheID(b.Identity().ID)
			sB := persistedListTestResult(t, bctx, b, bsrv, "decided-step", &transferTestValue{Text: "b's S"})
			canDiffer(t, b, sB)
			top := persistedListTestResult(t, bctx, b, bsrv, "decided-top", &transferTestValue{Text: "T"})
			transferTestDependency(b, bctx, top, sB)
			exportedB := exportTestBundle(t, bctx, b, top)
			replyB, err := cloud.MergeValues(ctx, fromB, exportedB)
			require.NoError(t, err)
			require.Equal(t, sNumber, cloudNumberOf(t, exportedB, replyB, sB), "the Cloud folds b's S into a's entry")
			tNumber := cloudNumberOf(t, exportedB, replyB, top)

			// Then b evaluates S, served or itself, and T over it; T's run
			// is stored whole.
			foldedRunTestHolding(t, ctx, cloud, sNumber, HolderKey{Cache: fromB, Number: uint64(sB.cacheSharedResult().id)}, tc.ranOwn)
			withholdTestRan(t, ctx, cloud, tNumber, HolderKey{Cache: fromB, Number: uint64(top.cacheSharedResult().id)}, 0, true)
			stored, _, err = cloud.SetStoredPart(ctx, tNumber, testLiveOffer(), 0)
			require.NoError(t, err)
			require.True(t, stored)

			built, err := cloud.StoredBundle(ctx, []uint64{tNumber}, time.Now().Unix(), nil)
			require.NoError(t, err)
			if tc.withheld {
				require.Contains(t, built.Withheld, tNumber, "T is built over b's own run of S")
			} else {
				require.Empty(t, built.Withheld, "T is built over a's run, which b was served")
				require.Len(t, built.Bundle.Values, 2)
			}
		})
	}
}
