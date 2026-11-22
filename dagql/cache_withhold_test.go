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
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "withhold-root", &transferTestValue{Text: "root"})
	p := persistedListTestResult(t, actx, a, asrv, "withhold-dep", &transferTestValue{Text: "dep"})
	transferTestDependency(a, actx, r, p)
	a.egraphMu.Lock()
	frame := p.cacheSharedResult().loadResultCall().clone()
	frame.FollowsFromInputs = follows
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
