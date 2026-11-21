package dagql

import (
	"context"
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
			first, err := cloud.MergeValues(ctx, "cache-a", source("cache-a", tc.a, ""))
			require.NoError(t, err)
			// A's input has its content, so B's run of it doesn't replace
			// it (replaceRunsLocked).
			p := first.Imported()[0].ResultID
			stored, _, err := cloud.SetStoredPart(ctx, p, storedPartRunOf(t, cloud, p), testLiveOffer(), 0)
			require.NoError(t, err)
			require.True(t, stored)
			reply, err := cloud.MergeValues(ctx, "cache-b", source("cache-b", tc.b, "over-r"))
			require.NoError(t, err)
			got := MergeRowCounts{OverOtherRunEqual: reply.Rows.OverOtherRunEqual, OverOtherRunDifferent: reply.Rows.OverOtherRunDifferent, OverOtherRunUnknown: reply.Rows.OverOtherRunUnknown}
			require.Equal(t, tc.want, got)
		})
	}
}

// replaceTestRun exports a run of P and of R over it from an engine cache of
// its own, P's and R's values naming the run, and merges it into cloud as
// from. It returns the merge's reply and the Cloud's numbers for R and P.
func replaceTestRun(t *testing.T, ctx context.Context, cloud *Cache, from CacheID) (MergeReply, uint64, uint64) {
	t.Helper()
	ectx, e, esrv := transferTestCache(t)
	r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: string(from) + " root"})
	p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: string(from) + " dep"})
	transferTestDependency(e, ectx, r, p)
	bundle := exportTestBundle(t, ectx, e, r)
	reply, err := cloud.MergeValues(ctx, from, bundle)
	require.NoError(t, err)
	var rNumber, pNumber uint64
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
			}
		}
	}
	require.NotZero(t, rNumber)
	require.NotZero(t, pNumber)
	return reply, rNumber, pNumber
}

// A stored record whose run left no content is replaced by a later engine's
// own run of it: the old entry and every entry over it are retired, whatever
// content they hold, and the later run's records become the recipes'
// entries. A retired entry is no lookup's or class mate's, keeps its stored
// parts for what names it already, and stays retired over a restart.
func TestALaterRunReplacesARecordStoredWithoutContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	_, r1, p1 := replaceTestRun(t, ctx, cloud, "cache-a")
	stored, _, err := cloud.SetStoredPart(ctx, r1, storedPartRunOf(t, cloud, r1), testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored, "A's root has its content")
	cloud.egraphMu.RLock()
	require.True(t, cloud.storedWithoutContentLocked(cloud.resultsByID[sharedResultID(p1)]), "A's dep has none")
	cloud.egraphMu.RUnlock()
	replaceTestRan(t, ctx, cloud, p1)
	pRecipe, ok := cloud.EntryRecipe(p1)
	require.True(t, ok)
	rRecipe, ok := cloud.EntryRecipe(r1)
	require.True(t, ok)

	reply, r2, p2 := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Equal(t, 1, reply.Rows.RunsReplaced, "B's dep replaces A's")
	require.NotEqual(t, p1, p2, "B's dep is a new entry")
	require.NotEqual(t, r1, r2, "and so is B's root, over it")
	for _, n := range []uint64{p2, r2} {
		run, ok := cloud.EntryRun(n)
		require.True(t, ok)
		require.Equal(t, CacheID("cache-b"), run.Cache, "entry %d is B's run", n)
	}
	retired := func(cloud *Cache, when string) {
		t.Helper()
		require.Equal(t, []uint64{p2}, cloud.EquivalentEntries(pRecipe.String()), "%s: a lookup of the dep finds B's alone", when)
		require.Equal(t, []uint64{r2}, cloud.EquivalentEntries(rRecipe.String()), "%s: a lookup of the root finds B's alone", when)
		class, ok := cloud.EntryClassEntries(r1)
		require.True(t, ok, "%s: A's root stays for what names it", when)
		require.Equal(t, []uint64{r1}, class.Entries, "%s: and is no class mate of B's", when)
		av, err := cloud.AvailablePart(r1, testLiveOffer().Address, 0)
		require.NoError(t, err)
		require.Equal(t, PartStored, av.State, "%s: A's root keeps its content", when)
	}
	retired(cloud, "after the merge")
	require.NoError(t, cloud.Close(ctx))

	ctx, cloud = storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	retired(cloud, "after a restart")
}

// A stored record with content isn't replaced: the first run stored stays,
// and a later engine's own run of it lands on it, its part refused.
func TestALaterRunDoesntReplaceARecordWithContent(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, r1, p1 := replaceTestRun(t, ctx, cloud, "cache-a")
	for _, n := range []uint64{r1, p1} {
		stored, _, err := cloud.SetStoredPart(ctx, n, storedPartRunOf(t, cloud, n), testLiveOffer(), 0)
		require.NoError(t, err)
		require.True(t, stored)
	}
	reply, r2, p2 := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Zero(t, reply.Rows.RunsReplaced)
	require.Equal(t, []uint64{r1, p1}, []uint64{r2, p2}, "B's records land on A's entries")
	var run RunName
	for _, value := range reply.Values {
		if value.Number == p1 {
			run = value.Run
		}
	}
	got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: p1, Run: run, Part: testLiveOffer()}})
	require.NoError(t, err)
	require.Equal(t, []StoredPartResult{{OtherRun: true}}, got)
}

// A record whose content is being uploaded isn't without content: a later
// run doesn't replace it while the upload's claim holds (MarkPartUploading).
func TestALaterRunDoesntReplaceARecordWhoseContentIsUploading(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, r1, p1 := replaceTestRun(t, ctx, cloud, "cache-a")
	for _, n := range []uint64{r1, p1} {
		replaceTestRan(t, ctx, cloud, n)
		_, err := cloud.MarkPartUploading(n, testLiveOffer().Address, true)
		require.NoError(t, err)
	}
	reply, r2, p2 := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Zero(t, reply.Rows.RunsReplaced)
	require.Equal(t, []uint64{r1, p1}, []uint64{r2, p2}, "B's records land on A's entries")

	_, err := cloud.MarkPartUploading(p1, testLiveOffer().Address, false)
	require.NoError(t, err)
	_, err = cloud.MarkPartUploading(r1, testLiveOffer().Address, false)
	require.NoError(t, err)
	replaceTestRan(t, ctx, cloud, p1)
	replaceTestRan(t, ctx, cloud, r1)
	reply, _, p3 := replaceTestRun(t, ctx, cloud, "cache-c")
	require.Equal(t, 2, reply.Rows.RunsReplaced, "once the uploads end without content, a later run replaces both")
	require.NotEqual(t, p1, p3)
}

// replaceTestRan attaches the holding a stored record's run names, reporting
// a part complete: the run's own engine ran the step.
func replaceTestRan(t *testing.T, ctx context.Context, cloud *Cache, number uint64) {
	t.Helper()
	run, ok := cloud.EntryRun(number)
	require.True(t, ok)
	recipe, ok := cloud.EntryRecipe(number)
	require.True(t, ok)
	_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: run.Cache, Number: run.Number}, RemoteHolding{Recipe: recipe, Parts: []PersistedPartAddress{testLiveOffer().Address}})
	require.NoError(t, err)
}

// A record whose own run never ran its step is a recipe: a later run
// doesn't replace it, though another engine's holding of the same entry
// reports the part complete. Once its own run's holding does, it is
// replaced, and the mark survives a restart.
func TestALaterRunReplacesOnlyARecordWhoseOwnRunRan(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	_, r1, p1 := replaceTestRun(t, ctx, cloud, "cache-a")
	stored, _, err := cloud.SetStoredPart(ctx, r1, storedPartRunOf(t, cloud, r1), testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)
	cloud.egraphMu.RLock()
	require.False(t, cloud.ownRunRanLocked(cloud.resultsByID[sharedResultID(p1)]), "A's record proves no part complete")
	cloud.egraphMu.RUnlock()

	recipe, ok := cloud.EntryRecipe(p1)
	require.True(t, ok)
	_, err = cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-x", Number: 77}, RemoteHolding{Recipe: recipe, Parts: []PersistedPartAddress{testLiveOffer().Address}})
	require.NoError(t, err)
	reply, _, p2 := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Zero(t, reply.Rows.RunsReplaced, "another engine's run of the entry says nothing of A's")
	require.Equal(t, p1, p2)

	replaceTestRan(t, ctx, cloud, p1)
	require.NoError(t, cloud.Close(ctx))
	ctx, cloud = storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	reply, _, p3 := replaceTestRun(t, ctx, cloud, "cache-c")
	require.Equal(t, 1, reply.Rows.RunsReplaced, "A's run ran, its content not stored: replaced, after a restart too")
	require.NotEqual(t, p1, p3)
}

// An image entry's record is never replaced, and a sender's own run of a row
// its image ships replaces nothing; the mark survives a restart.
func TestALaterRunNeverReplacesAnImageEntry(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	ectx, e, esrv := transferTestCache(t)
	r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: "image root"})
	p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: "image dep"})
	transferTestDependency(e, ectx, r, p)
	bundle := exportTestBundle(t, ectx, e, r)
	prepared, err := cloud.PrepareValueMerge(ctx, "cache-a", bundle)
	require.NoError(t, err)
	for _, v := range bundle.Values {
		prepared.ImageEntries(v.Ordinal)
	}
	reply, err := cloud.CommitValueMerge(ctx, prepared)
	require.NoError(t, err)
	var p1 uint64
	for _, value := range reply.Values {
		for _, sent := range bundle.Values {
			if sent.Ordinal == value.Ordinal && sent.SenderNumber == uint64(p.cacheSharedResult().id) {
				p1 = value.Number
			}
		}
	}
	require.NotZero(t, p1)
	replaceTestRan(t, ctx, cloud, p1)
	require.NoError(t, cloud.Close(ctx))

	ctx, cloud = storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	cloud.egraphMu.RLock()
	image := cloud.resultsByID[sharedResultID(p1)].imageEntry
	cloud.egraphMu.RUnlock()
	require.True(t, image, "the mark survives a restart")
	reply, _, p2 := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Zero(t, reply.Rows.RunsReplaced, "an image entry's record isn't replaced")
	require.Equal(t, p1, p2)
}
