package dagql

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// ownRun is the run of a record its sender computed: the sender's own entry.
func ownRun(from CacheID, value TransferredValue) RunName {
	return RunName{Cache: from, Number: value.SenderNumber, Replacements: value.SenderReplacements}
}

// The Cloud stores a part only on an entry whose record is the run the part
// came from: the first run it stores of a recipe wins, and a second engine's
// own run of it can't put its bytes beside the first run's record.
func TestSetStoredPartTakesOnlyTheEntrysRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	bundle, _ := mergeTestSource(t, "root", "value")
	reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	number := reply.Imported()[0].ResultID
	run, ok := cloud.EntryRun(number)
	require.True(t, ok)
	require.Equal(t, ownRun("cache-a", bundle.Values[0]), run, "the entry holds the run of the engine that made it")
	require.Equal(t, run, reply.Values[0].Run, "the reply names it")

	// Engine B ran the recipe itself and exports its own run: its record
	// lands on A's entry, which keeps A's record.
	second, _ := mergeTestSource(t, "root", "another value")
	breply, err := cloud.MergeValues(ctx, "cache-b", second)
	require.NoError(t, err)
	require.Equal(t, ownRun("cache-b", second.Values[0]), breply.Values[0].Run, "B's record is B's run")
	got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: number, Run: breply.Values[0].Run, Part: testLiveOffer()}})
	require.NoError(t, err)
	require.Equal(t, []StoredPartResult{{OtherRun: true}}, got, "B's part isn't stored beside A's record")
	av, err := cloud.AvailablePart(number, testLiveOffer().Address, 0)
	require.NoError(t, err)
	require.NotEqual(t, PartStored, av.State)

	got, _, err = cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: number, Run: run, Part: testLiveOffer()}})
	require.NoError(t, err)
	require.Equal(t, []StoredPartResult{{Stored: true}}, got, "A's part is")
}

// An engine keeps no run and sends none. The Cloud names the run of a record
// an engine took from it by the engine's holding: a part the engine uploads
// for it is the Cloud's run, and is stored.
func TestTheCloudNamesAnImportedRecordsRunByItsHolding(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	bundle, _ := mergeTestSource(t, "root", "value")
	reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	number := reply.Imported()[0].ResultID
	sent, err := cloud.StoredBundle(ctx, []uint64{number}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.NotContains(t, string(mustTransferJSON(t, sent.Bundle.Values)), `"run"`, "the Cloud sends no run")

	bctx, b, bsrv := transferTestCache(t)
	breply, err := b.MergeValues(bctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	bNumber := breply.Imported()[0].ResultID
	require.Zero(t, breply.Values[0].Run, "an engine names no run")
	imported, err := b.LoadResultByResultID(bctx, "test-session", bsrv, bNumber)
	require.NoError(t, err)
	exported := exportTestBundle(t, bctx, b, imported)
	require.NotContains(t, string(mustTransferJSON(t, exported.Values)), `"run"`, "an engine sends no run")
	require.True(t, exported.Values[0].Record.Envelope.Imported)

	// Without a holding of B's, the Cloud can't tell which run B took: it
	// names B's entry.
	unheld, err := cloud.MergeValues(ctx, "cache-b", exported)
	require.NoError(t, err)
	require.Equal(t, ownRun("cache-b", exported.Values[0]), unheld.Values[0].Run)

	// With B's holding on the entry B was sent, B's record is A's run.
	recipe, ok := cloud.EntryRecipe(number)
	require.True(t, ok)
	_, err = cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-c", Number: bNumber}, RemoteHolding{Recipe: recipe})
	require.NoError(t, err)
	again := exportTestBundle(t, bctx, b, imported)
	held, err := cloud.MergeValues(ctx, "cache-c", again)
	require.NoError(t, err)
	require.Equal(t, ownRun("cache-a", bundle.Values[0]), held.Values[0].Run, "the run of the entry it was sent")
	got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: number, Run: held.Values[0].Run, Part: testLiveOffer()}})
	require.NoError(t, err)
	require.Equal(t, []StoredPartResult{{Stored: true}}, got, "its upload of A's bytes is stored")
}

// A pruned entry's run goes with its value: a record that fills it again
// names its own.
func TestAPrunedEntryTakesTheRunOfItsRefill(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, _ := storedBundleTestCloud(t, "refill")
	_, removed, _, err := cloud.removePrunedEdge(ctx, sharedResultID(r))
	require.NoError(t, err)
	require.True(t, removed)
	_, ok := cloud.EntryRun(r)
	require.False(t, ok, "no value, no run")

	actx, a, asrv := transferTestCache(t)
	root := persistedListTestResult(t, actx, a, asrv, "bundle-root", &transferTestValue{Text: "root"})
	dep := persistedListTestResult(t, actx, a, asrv, "bundle-dep", &transferTestValue{Text: "dep"})
	transferTestDependency(a, actx, root, dep)
	bundle := exportTestBundle(t, actx, a, root)
	reply, err := cloud.MergeValues(ctx, "cache-c", bundle)
	require.NoError(t, err)
	require.Equal(t, r, reply.Imported()[0].ResultID, "the same entry stores again")
	run, ok := cloud.EntryRun(r)
	require.True(t, ok)
	for _, value := range bundle.Values {
		if value.SenderNumber == uint64(root.cacheSharedResult().id) {
			require.Equal(t, ownRun("cache-c", value), run, "the refill's own run")
		}
	}
}

// A stored bundle doesn't lend a part of one run to another run of the same
// recipe: a retired run's parts stay with it.
func TestStoredBundleLendsNoPartOfAnotherRunOfTheRecipe(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	a, _ := mergeTestSource(t, "root", "a's value")
	b, _ := mergeTestSource(t, "root", "b's value")
	ra, err := cloud.MergeValues(ctx, "cache-a", a)
	require.NoError(t, err)
	na := ra.Imported()[0].ResultID
	cloud.egraphMu.Lock()
	resA := cloud.resultsByID[sharedResultID(na)]
	resB := &sharedResult{id: resA.id + 1000, recipeKeys: resA.recipeKeys}
	resB.run = &RunName{Cache: "cache-b", Number: b.Values[0].SenderNumber}
	require.True(t, cloud.otherRunOfRecipeLocked(resA, resB), "two runs of one recipe")
	resB.run = resA.run
	require.False(t, cloud.otherRunOfRecipeLocked(resA, resB), "one run")
	cloud.egraphMu.Unlock()
}

// The Cloud keeps each record's run whether it holds the record whole or
// compressed, and over a restart; it sends none.
func TestCloudKeepsItsRunsCompressedAndOverARestart(t *testing.T) {
	t.Parallel()
	bundle, _ := mergeTestSource(t, "root", "value")
	made := ownRun("cache-a", bundle.Values[0])

	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	number := storedPersistenceTestMerge(t, ctx, cloud, bundle)[0]
	runOf := func(cloud *Cache, when string) {
		t.Helper()
		run, ok := cloud.EntryRun(number)
		require.True(t, ok, when)
		require.Equal(t, made, run, when)
		sent, err := cloud.StoredBundle(ctx, []uint64{number}, time.Now().Unix(), nil)
		require.NoError(t, err)
		require.NotContains(t, string(mustTransferJSON(t, sent.Bundle.Values)), `"run"`, "%s: sent without it", when)
	}
	runOf(cloud, "whole")
	storedTestCompress(cloud)
	runOf(cloud, "compressed")
	require.NoError(t, cloud.Close(ctx))

	ctx, cloud = storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	runOf(cloud, "after a restart")
}

// A record an engine computed over its own run of an input the Cloud stores
// as another run is counted, by whether the two runs of the input are shown
// equal by content digest, shown different, or can't be compared.
func TestCloudCountsRecordsOverAnotherRunOfAnInput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		a, b string
		want MergeRowCounts
	}{
		{"no digest", "", "", MergeRowCounts{OverOtherRunUnknown: 1}},
		{"equal", "same", "same", MergeRowCounts{OverOtherRunEqual: 1}},
		{"different", "a's", "b's", MergeRowCounts{OverOtherRunDifferent: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cloud := storedPartTestCache(t, WithBlobStore())
			source := func(cache, content, top string) ValueBundle {
				sctx, s, ssrv := transferTestCache(t)
				p := persistedListTestResult(t, sctx, s, ssrv, "over-p", &transferTestValue{Text: cache + " p"})
				if content != "" {
					require.NoError(t, s.TeachContentDigest(sctx, p, digest.FromString(content)))
				}
				if top == "" {
					return exportTestBundle(t, sctx, s, p)
				}
				r := persistedListTestResult(t, sctx, s, ssrv, top, &transferTestValue{Text: cache + " r"})
				transferTestDependency(s, sctx, r, p)
				return exportTestBundle(t, sctx, s, r)
			}
			_, err := cloud.MergeValues(ctx, "cache-a", source("cache-a", tc.a, ""))
			require.NoError(t, err)
			reply, err := cloud.MergeValues(ctx, "cache-b", source("cache-b", tc.b, "over-r"))
			require.NoError(t, err)
			got := MergeRowCounts{OverOtherRunEqual: reply.Rows.OverOtherRunEqual, OverOtherRunDifferent: reply.Rows.OverOtherRunDifferent, OverOtherRunUnknown: reply.Rows.OverOtherRunUnknown}
			require.Equal(t, tc.want, got)
		})
	}
}
