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
// came from: the first run it stores of a recipe that ran wins, and a second
// engine's own run of it can't put its bytes beside the first run's record.
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
	// A's own engine ran the step: its record is a run, not a recipe.
	replaceTestRan(t, ctx, cloud, number)

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
// recipe: the only other entries of a recipe are the runs a later run
// retired, and a bundle of the later run borrows nothing of theirs.
func TestStoredBundleLendsNoPartOfAnotherRunOfTheRecipe(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, r1, p1 := replaceTestRun(t, ctx, cloud, "cache-a")
	stored, _, err := cloud.SetStoredPart(ctx, r1, storedPartRunOf(t, cloud, r1), testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)
	replaceTestRan(t, ctx, cloud, p1)
	_, r2, _ := replaceTestRun(t, ctx, cloud, "cache-b")
	require.NotEqual(t, r1, r2, "A's root is retired")
	content := testDigest("one root, two runs")
	for _, n := range []uint64{r1, r2} {
		require.NoError(t, cloud.TeachContentDigest(ctx, Result[Typed]{shared: storedBundleTestEntry(cloud, n)}, content))
	}
	built, err := cloud.StoredBundle(ctx, []uint64{r2}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Empty(t, built.Bundle.Outputs, "B's root borrows nothing of A's run")
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

// imageTestRun merges, from cache-a, P and R over it as rows its image
// ships, P's value declaring snapshot in state, and returns P's number.
func imageTestRun(t *testing.T, ctx context.Context, cloud *Cache, state string) uint64 {
	t.Helper()
	ectx, e, esrv := transferTestCache(t)
	r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: "image root"})
	p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: "image dep", Parts: []transferTestPart{{Part: "snapshot", State: state, Kind: "directory"}}})
	transferTestDependency(e, ectx, r, p)
	bundle := exportTestBundle(t, ectx, e, r)
	prepared, err := cloud.PrepareValueMerge(ctx, "cache-a", bundle)
	require.NoError(t, err)
	for _, v := range bundle.Values {
		prepared.ImageEntries(v.Ordinal)
	}
	reply, err := cloud.CommitValueMerge(ctx, prepared)
	require.NoError(t, err)
	for _, value := range reply.Values {
		for _, sent := range bundle.Values {
			if sent.Ordinal == value.Ordinal && sent.SenderNumber == uint64(p.cacheSharedResult().id) {
				return value.Number
			}
		}
	}
	t.Fatal("no P")
	return 0
}

// An image entry whose content the image ships is the image's run: a later
// run never replaces it, nor does another run's part adopt it, over a
// restart too. One the image ships as a record alone is a recipe like any
// other: a later run's part adopts it.
func TestALaterRunNeverReplacesAnImageResult(t *testing.T) {
	t.Parallel()
	t.Run("image result", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "cloud.db")
		ctx, cloud := storedPersistenceTestCloud(t, path)
		p1 := imageTestRun(t, ctx, cloud, "completed")
		require.NoError(t, cloud.Close(ctx))
		ctx, cloud = storedPersistenceTestCloud(t, path)
		defer func() { require.NoError(t, cloud.Close(ctx)) }()
		cloud.egraphMu.RLock()
		image := cloud.resultsByID[sharedResultID(p1)].imageEntry
		cloud.egraphMu.RUnlock()
		require.True(t, image, "the mark survives a restart")
		got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: p1, Run: RunName{Cache: "cache-b", Number: 9}, Part: testLiveOffer()}})
		require.NoError(t, err)
		require.Equal(t, []StoredPartResult{{OtherRun: true}}, got, "never adopted")
	})
	t.Run("image closure record", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		p1 := imageTestRun(t, ctx, cloud, "pending")
		cloud.egraphMu.RLock()
		image := cloud.resultsByID[sharedResultID(p1)].imageEntry
		cloud.egraphMu.RUnlock()
		require.False(t, image, "a record the image ships without content isn't the image's run")
	})
}

// A stored record whose own run never ran is a recipe: a part of another
// run, one that ran, is stored onto it, and the record takes that run.
// Nothing is retired. A record with a stored record over it from its own
// engine that ran, so its run did happen though no row said so, keeps
// refusing it.
func TestARecipeRecordTakesTheRunOfTheFirstPartStored(t *testing.T) {
	t.Parallel()
	partOf := func(t *testing.T, cloud *Cache, number uint64, run RunName) StoredPartResult {
		t.Helper()
		got, _, err := cloud.SetStoredParts(t.Context(), []StoredPartWrite{{Number: number, Run: run, Part: testLiveOffer()}})
		require.NoError(t, err)
		return got[0]
	}
	later := RunName{Cache: "cache-b", Number: 9}

	t.Run("recipe", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		_, r, p := replaceTestRun(t, ctx, cloud, "cache-other")
		// The root over it is another engine's: nothing of p's own run.
		cloud.egraphMu.Lock()
		cloud.resultsByID[sharedResultID(r)].run = &RunName{Cache: "cache-third", Number: 1}
		cloud.egraphMu.Unlock()
		require.Equal(t, StoredPartResult{Stored: true}, partOf(t, cloud, p, later), "stored onto the recipe")
		run, ok := cloud.EntryRun(p)
		require.True(t, ok)
		require.Equal(t, later, run, "the record takes the part's run")
		require.Equal(t, StoredPartResult{OtherRun: true}, partOf(t, cloud, p, RunName{Cache: "cache-c", Number: 4}), "and keeps it")
	})
	t.Run("its own engine's record over it ran", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		_, r, p := replaceTestRun(t, ctx, cloud, "cache-a")
		replaceTestRan(t, ctx, cloud, r)
		require.Equal(t, StoredPartResult{OtherRun: true}, partOf(t, cloud, p, later), "cache-a's root over it ran, so cache-a's run of it happened")
	})
	t.Run("its own engine's record over it never ran", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		_, _, p := replaceTestRun(t, ctx, cloud, "cache-a")
		require.Equal(t, StoredPartResult{Stored: true}, partOf(t, cloud, p, later), "a recipe over a recipe says nothing of it")
	})
	t.Run("ran", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		bundle, _ := mergeTestSource(t, "root", "value")
		reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
		require.NoError(t, err)
		n := reply.Imported()[0].ResultID
		replaceTestRan(t, ctx, cloud, n)
		require.Equal(t, StoredPartResult{OtherRun: true}, partOf(t, cloud, n, later))
	})
}

// replaceTestProving merges from cache-a P, whose record proves snapshot
// complete, and R over it, holding them whole or compressed, and returns P.
func replaceTestProving(t *testing.T, compressed bool) (context.Context, *Cache, uint64) {
	t.Helper()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	if !compressed {
		storedTestHoldWhole(cloud)
	}
	ectx, e, esrv := transferTestCache(t)
	r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: "root"})
	p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: "dep", Parts: []transferTestPart{{Part: "snapshot", State: "completed", Kind: "directory"}}})
	transferTestDependency(e, ectx, r, p)
	bundle := exportTestBundle(t, ectx, e, r)
	reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	if compressed {
		storedTestCompress(cloud)
	}
	for _, value := range reply.Values {
		for _, sent := range bundle.Values {
			if sent.Ordinal == value.Ordinal && sent.SenderNumber == uint64(p.cacheSharedResult().id) {
				return ctx, cloud, value.Number
			}
		}
	}
	t.Fatal("no P")
	return nil, nil, 0
}

// A record that proves a part complete is its run's, whole or compressed:
// another run's part is refused onto it before any telemetry says it ran.
func TestARecordThatProvesItsRunIsNoRecipe(t *testing.T) {
	t.Parallel()
	for _, compressed := range []bool{false, true} {
		t.Run(storedTestState(compressed), func(t *testing.T) {
			t.Parallel()
			ctx, cloud, p := replaceTestProving(t, compressed)
			got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: p, Run: RunName{Cache: "cache-b", Number: 9}, Part: testLiveOffer()}})
			require.NoError(t, err)
			require.Equal(t, []StoredPartResult{{OtherRun: true}}, got)
		})
	}
}

// A merge that would retire an entry a held row of it stands for replaces
// nothing: the held row carries no record to install in its place.
func TestAHeldRowOverAReplacedRunStopsTheReplacement(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, r1, p1 := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRan(t, ctx, cloud, p1)
	ectx, e, esrv := transferTestCache(t)
	r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: "b root"})
	p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: "b dep"})
	transferTestDependency(e, ectx, r, p)
	held := engineHeldTestBundle(t, e, exportTestBundle(t, ectx, e, r, p), r)
	reply, err := cloud.MergeValues(ctx, "cache-b", held)
	require.NoError(t, err)
	require.Zero(t, reply.Rows.RunsReplaced)
	recipe, ok := cloud.EntryRecipe(r1)
	require.True(t, ok)
	require.Equal(t, []uint64{r1}, cloud.EquivalentEntries(recipe.String()), "A's root stays")
}

// A record's own engine's record over it that ran says the recipe's run
// happened, though a record of another engine sits between them.
func TestARecipeKeepsItsRunUnderItsEnginesRecordThroughAnothers(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	ectx, e, esrv := transferTestCache(t)
	r := persistedListTestResult(t, ectx, e, esrv, "chain-r", &transferTestValue{Text: "r"})
	q := persistedListTestResult(t, ectx, e, esrv, "chain-q", &transferTestValue{Text: "q"})
	p := persistedListTestResult(t, ectx, e, esrv, "chain-p", &transferTestValue{Text: "p"})
	transferTestDependency(e, ectx, r, q)
	transferTestDependency(e, ectx, q, p)
	bundle := exportTestBundle(t, ectx, e, r)
	reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	numbers := map[uint64]uint64{}
	for _, value := range reply.Values {
		for _, sent := range bundle.Values {
			if sent.Ordinal == value.Ordinal {
				numbers[sent.SenderNumber] = value.Number
			}
		}
	}
	cp, cq, cr := numbers[uint64(p.cacheSharedResult().id)], numbers[uint64(q.cacheSharedResult().id)], numbers[uint64(r.cacheSharedResult().id)]
	cloud.egraphMu.Lock()
	cloud.resultsByID[sharedResultID(cq)].run = &RunName{Cache: "cache-b", Number: 5}
	cloud.egraphMu.Unlock()
	replaceTestRan(t, ctx, cloud, cr)
	got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: cp, Run: RunName{Cache: "cache-c", Number: 9}, Part: testLiveOffer()}})
	require.NoError(t, err)
	require.Equal(t, []StoredPartResult{{OtherRun: true}}, got, "cache-a's R over B's Q ran, so cache-a's P ran")
}

// A recipe takes a part's run only with the part: a part refused for its
// expired copy leaves the recipe as it was, to take a later run's.
func TestARefusedPartLeavesTheRecipesRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, p := replaceTestRun(t, ctx, cloud, "cache-other")
	before, ok := cloud.EntryRun(p)
	require.True(t, ok)
	cloud.egraphMu.Lock()
	for parentID := range cloud.resultsByID[sharedResultID(p)].depParents {
		cloud.resultsByID[parentID].run = &RunName{Cache: "cache-third", Number: 1}
	}
	cloud.egraphMu.Unlock()
	got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: p, Run: RunName{Cache: "cache-b", Number: 9}, Part: testLiveOffer(), CopyExpiresAtUnix: 1}})
	require.NoError(t, err)
	require.False(t, got[0].Stored, "the expired copy is refused")
	run, ok := cloud.EntryRun(p)
	require.True(t, ok)
	require.Equal(t, before, run, "the recipe keeps its run")
}
