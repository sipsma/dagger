package dagql

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
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

// withContent puts, on bundle's rows of results, an output of their snapshot
// part: the export carries the run's content of them.
func withContent(t *testing.T, bundle ValueBundle, results ...AnyResult) ValueBundle {
	t.Helper()
	return withParts(t, bundle, []string{"snapshot"}, results...)
}

// withParts puts, on bundle's rows of results, an output of each of parts.
func withParts(t *testing.T, bundle ValueBundle, parts []string, results ...AnyResult) ValueBundle {
	t.Helper()
	out := bundle
	out.Outputs = slices.Clone(bundle.Outputs)
	for _, res := range results {
		found := false
		for _, v := range bundle.Values {
			if v.SenderNumber != uint64(res.cacheSharedResult().id) {
				continue
			}
			for _, name := range parts {
				part := storedPruneTestPart(name, map[string]int64{fmt.Sprintf("content-%d-%s-%s", v.SenderNumber, name, t.Name()): 10})
				out.Outputs = append(out.Outputs, TransferredOutput{Ordinal: v.Ordinal, Address: part.Address, State: "completed", Value: &part.Value, Chain: &part.Chain, Owner: &part.Owner})
			}
			found = true
		}
		require.True(t, found, "the bundle carries entry %d", res.cacheSharedResult().id)
	}
	return out
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
	bundle := withContent(t, exportTestBundle(t, ectx, e, r), r, p)
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
		require.NoError(t, cloud.MarkPartUploading(n, testLiveOffer().Address))
	}
	reply, r2, p2 := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Zero(t, reply.Rows.RunsReplaced)
	require.Equal(t, []uint64{r1, p1}, []uint64{r2, p2}, "B's records land on A's entries")

	// The uploads end, their claims given back, with nothing stored.
	_, err := cloud.EndPartUploads([]PartUpload{{Number: p1, Address: testLiveOffer().Address}, {Number: r1, Address: testLiveOffer().Address}})
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
		reply, _, p2 := replaceTestRun(t, ctx, cloud, "cache-b")
		require.Zero(t, reply.Rows.RunsReplaced, "never replaced")
		require.Equal(t, p1, p2)
	})
	t.Run("image closure record", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		p1 := imageTestRun(t, ctx, cloud, "pending")
		cloud.egraphMu.RLock()
		image := cloud.resultsByID[sharedResultID(p1)].imageEntry
		cloud.egraphMu.RUnlock()
		require.False(t, image, "a record the image ships without content isn't the image's run")
		got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: p1, Run: RunName{Cache: "cache-b", Number: 9}, Part: testLiveOffer()}})
		require.NoError(t, err)
		require.Equal(t, []StoredPartResult{{Stored: true, Adopted: true}}, got, "a recipe: the first run stored adopts it")
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
		require.Equal(t, StoredPartResult{Stored: true, Adopted: true}, partOf(t, cloud, p, later), "stored onto the recipe")
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
		require.Equal(t, StoredPartResult{Stored: true, Adopted: true}, partOf(t, cloud, p, later), "a recipe over a recipe says nothing of it")
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

// heldTestRun has engine B, of its own cache, compute P and R over it, and
// merge its full export into cloud as cache-b. It returns B's cache, its P
// and R, the full bundle, and the Cloud's numbers for B's P and R.
func heldTestRun(t *testing.T, ctx context.Context, cloud *Cache) (*Cache, AnyResult, AnyResult, ValueBundle, MergeReply) {
	t.Helper()
	bctx, b, bsrv := transferTestCache(t)
	heldTestContexts.Store(b, bctx)
	r := persistedListTestResult(t, bctx, b, bsrv, "replace-root", &transferTestValue{Text: "b root"})
	p := persistedListTestResult(t, bctx, b, bsrv, "replace-dep", &transferTestValue{Text: "b dep"})
	transferTestDependency(b, bctx, r, p)
	full := withContent(t, exportTestBundle(t, bctx, b, r), r, p)
	reply, err := cloud.MergeValues(ctx, "cache-b", full)
	require.NoError(t, err)
	return b, p, r, full, reply
}

// heldTestContexts are the contexts of heldTestRun's engine caches.
var heldTestContexts sync.Map

// bctxOf is the context of heldTestRun's engine cache b.
func bctxOf(t *testing.T, b *Cache) context.Context {
	t.Helper()
	ctx, ok := heldTestContexts.Load(b)
	require.True(t, ok)
	return ctx.(context.Context)
}

// heldTestComputed records that B's holding of res executed its call, as an
// executed call row of B's says.
func heldTestComputed(t *testing.T, ctx context.Context, cloud *Cache, b *Cache, res AnyResult) {
	t.Helper()
	recipe, err := res.cacheSharedResult().loadResultCall().deriveRecipeDigest(b)
	require.NoError(t, err)
	_, err = cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-b", Number: uint64(res.cacheSharedResult().id)}, RemoteHolding{Recipe: recipe, Executed: true})
	require.NoError(t, err)
}

// The first trial's refusals, in one engine's exports. B's full record of a
// step lands on A's record while A's run hasn't yet reported running, and
// is kept: B's engine counts the row as one the Cloud holds. A's run then
// reports it ran, and its content never comes. B's next export sends the row
// held. A full record of B's own run, which B computed, would replace A's,
// so the Cloud doesn't hold what B believes: the merge answers B's held row
// stale; so it does a held row of B's on A's record over it, which the
// replacement would retire. B sends them in
// full, its run replaces A's, and B's content is stored as B's run.
func TestAHeldRowOfAnEnginesOwnRunOverARunWithoutContentIsStale(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	b, p, r, full, first := heldTestRun(t, ctx, cloud)
	require.Zero(t, first.Rows.RunsReplaced, "A's run hasn't reported running: B's record is kept")
	replaceTestRan(t, ctx, cloud, pA)
	heldTestComputed(t, ctx, cloud, b, p)

	held := engineHeldTestBundle(t, b, full, p)
	reply, err := cloud.MergeValues(ctx, "cache-b", held)
	require.NoError(t, err)
	require.False(t, reply.Committed)
	require.Len(t, reply.Stale, 1, "B's held row on A's record is stale")

	// With P in full and R held over it, R's held row lands on A's record
	// over P, which the replacement would retire: stale too.
	both := withContent(t, exportTestBundle(t, bctxOf(t, b), b, r, p), r, p)
	reply, err = cloud.MergeValues(ctx, "cache-b", engineHeldTestBundle(t, b, both, r))
	require.NoError(t, err)
	require.False(t, reply.Committed)
	require.Len(t, reply.Stale, 1, "B's held row on A's record over it is stale")

	again, err := cloud.MergeValues(ctx, "cache-b", full)
	require.NoError(t, err)
	require.Equal(t, 1, again.Rows.RunsReplaced, "B's run replaces A's")
	var pB uint64
	var run RunName
	for _, v := range again.Values {
		for _, sent := range full.Values {
			if sent.Ordinal == v.Ordinal && sent.SenderNumber == uint64(p.cacheSharedResult().id) {
				pB, run = v.Number, v.Run
			}
		}
	}
	require.NotEqual(t, pA, pB)
	got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: pB, Run: run, Part: testLiveOffer()}})
	require.NoError(t, err)
	require.Equal(t, []StoredPartResult{{Stored: true}}, got, "B's content is stored as B's run")
}

// A held row whose sender took the Cloud's copy of the step, without
// computing it, isn't stale: the Cloud holds the run it holds.
func TestAHeldRowOfACopyTakenFromTheCloudIsntStale(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	b, p, _, full, _ := heldTestRun(t, ctx, cloud)
	replaceTestRan(t, ctx, cloud, pA)
	reply, err := cloud.MergeValues(ctx, "cache-b", engineHeldTestBundle(t, b, full, p))
	require.NoError(t, err)
	require.Empty(t, reply.Stale)
	require.True(t, reply.Committed)
}

// While A's upload of its content holds its claim, the content is on its way:
// B's held row isn't stale. Once the upload ends with nothing stored, its
// claim given back, it is.
func TestAHeldRowIsntStaleWhileTheRunsUploadHoldsItsClaim(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	b, p, _, full, _ := heldTestRun(t, ctx, cloud)
	replaceTestRan(t, ctx, cloud, pA)
	heldTestComputed(t, ctx, cloud, b, p)
	require.NoError(t, cloud.MarkPartUploading(pA, testLiveOffer().Address))
	held := engineHeldTestBundle(t, b, full, p)
	reply, err := cloud.MergeValues(ctx, "cache-b", held)
	require.NoError(t, err)
	require.Empty(t, reply.Stale, "A's content is on its way")

	_, err = cloud.EndPartUploads([]PartUpload{{Number: pA, Address: testLiveOffer().Address}})
	require.NoError(t, err)
	reply, err = cloud.MergeValues(ctx, "cache-b", held)
	require.NoError(t, err)
	require.Len(t, reply.Stale, 1, "A's upload ended with nothing stored")
}

// A held row over a record whose run ran and has its content isn't stale:
// the first run stored wins, and B's part is refused, as before.
func TestAHeldRowOverARunWithContentIsntStale(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	b, p, _, full, _ := heldTestRun(t, ctx, cloud)
	replaceTestRan(t, ctx, cloud, pA)
	stored, _, err := cloud.SetStoredPart(ctx, pA, storedPartRunOf(t, cloud, pA), testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)
	heldTestComputed(t, ctx, cloud, b, p)
	reply, err := cloud.MergeValues(ctx, "cache-b", engineHeldTestBundle(t, b, full, p))
	require.NoError(t, err)
	require.Empty(t, reply.Stale)
	require.True(t, reply.Committed)
}

// Two engines that both ran a step and left no content don't replace each
// other's records: a run replaces another's record only with its content of
// the step, which its export carries. Once one exports it, its run replaces
// the other's, once.
func TestTwoRunsWithoutContentDontReplaceEachOther(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	export := func(from CacheID, content bool, pending ...bool) MergeReply {
		t.Helper()
		ectx, e, esrv := transferTestCache(t)
		r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: string(from) + " root"})
		p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: string(from) + " dep"})
		transferTestDependency(e, ectx, r, p)
		bundle := exportTestBundle(t, ectx, e, r)
		if content {
			bundle = withContent(t, bundle, p)
		}
		if len(pending) > 0 && pending[0] {
			// An output of the pending part, as an export selects one: it
			// names the part and carries nothing.
			for _, v := range bundle.Values {
				if v.SenderNumber == uint64(p.cacheSharedResult().id) {
					bundle.Outputs = append(bundle.Outputs, TransferredOutput{Ordinal: v.Ordinal, Address: PersistedPartAddress{Part: "snapshot"}, State: "pending"})
				}
			}
		}
		reply, err := cloud.MergeValues(ctx, from, bundle)
		require.NoError(t, err)
		return reply
	}
	first := export("cache-a", false)
	var pA uint64
	for _, v := range first.Values {
		if len(v.Deps) == 0 {
			pA = v.Number
		}
	}
	require.NotZero(t, pA)
	replaceTestRan(t, ctx, cloud, pA)
	second := export("cache-b", false)
	require.Zero(t, second.Rows.RunsReplaced, "B brings no content of the step: nothing to replace A's with")
	require.Empty(t, second.Stale)
	pendingOnly := export("cache-d", false, true)
	require.Zero(t, pendingOnly.Rows.RunsReplaced, "D's output of the pending part carries nothing either")
	require.Empty(t, pendingOnly.Stale)
	third := export("cache-c", true)
	require.Equal(t, 1, third.Rows.RunsReplaced, "C brings its content: its run replaces A's, once")
}

// A holding's executed call describes the value it computed. Once the
// engine's entry takes another value in place, an imported one, the
// holding's new value isn't computed: its held row of it isn't stale, and its
// full resend wouldn't replace anything, so it would only be sent again.
func TestAnExecutedCallDescribesOnlyTheValueItComputed(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	b, p, _, full, _ := heldTestRun(t, ctx, cloud)
	replaceTestRan(t, ctx, cloud, pA)
	heldTestComputed(t, ctx, cloud, b, p)
	key := HolderKey{Cache: "cache-b", Number: uint64(p.cacheSharedResult().id)}
	// B's entry takes an imported value in place: its replacement count
	// moves on.
	_, _, err := cloud.ApplyHeldValueState(ctx, key, HeldValueState{Replacements: 1})
	require.NoError(t, err)
	cloud.egraphMu.RLock()
	_, h := cloud.holdingLocked(key)
	executed := h.executed
	cloud.egraphMu.RUnlock()
	require.False(t, executed, "the computation was the replaced value's")

	held := engineHeldTestBundle(t, b, full, p)
	for i := range held.Values {
		if held.Values[i].SenderNumber == key.Number {
			held.Values[i].SenderReplacements = 1
		}
	}
	reply, err := cloud.MergeValues(ctx, "cache-b", held)
	require.NoError(t, err)
	require.Empty(t, reply.Stale, "B's imported value isn't its own run")
}

// A run replaces another's record only with its whole content of the step,
// on a record held whole or compressed: B's export of one of the step's two
// parts replaces nothing; of both, it replaces, once. A part the record
// declares absent asks for no content, and an empty snapshot, scratch, is
// content.
func TestARunReplacesOnlyWithItsWholeContent(t *testing.T) {
	t.Parallel()
	pending := func(part string) transferTestPart {
		return transferTestPart{Part: part, State: "pending", Kind: "directory"}
	}
	absent := transferTestPart{Part: "execMeta", State: "absent", Kind: "directory"}
	for _, held := range []struct {
		name     string
		compress bool
	}{
		{"held whole", false},
		{"held compressed", true},
	} {
		t.Run(held.name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name    string
				parts   []transferTestPart
				carried []string
				scratch bool
				want    int
			}{
				{"half of two parts", []transferTestPart{pending("snapshot"), pending("other")}, []string{"snapshot"}, false, 0},
				{"both of two parts", []transferTestPart{pending("snapshot"), pending("other")}, []string{"snapshot", "other"}, false, 1},
				{"a part beside one declared absent", []transferTestPart{pending("snapshot"), absent}, []string{"snapshot"}, false, 1},
				{"an empty snapshot", []transferTestPart{pending("snapshot")}, []string{"snapshot"}, true, 1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					ctx, cloud := storedPartTestCache(t, WithBlobStore())
					storedTestHoldWhole(cloud)
					export := func(from CacheID, carried ...string) MergeReply {
						t.Helper()
						ectx, e, esrv := transferTestCache(t)
						r := persistedListTestResult(t, ectx, e, esrv, "whole-root", &transferTestValue{Text: string(from) + " root"})
						p := persistedListTestResult(t, ectx, e, esrv, "whole-dep", &transferTestValue{Text: string(from) + " dep", Parts: tc.parts})
						transferTestDependency(e, ectx, r, p)
						bundle := withParts(t, exportTestBundle(t, ectx, e, r), carried, p)
						if tc.scratch {
							for i := range bundle.Outputs {
								if bundle.Outputs[i].Chain != nil {
									chain := *bundle.Outputs[i].Chain
									chain.Layers = nil
									bundle.Outputs[i].Chain = &chain
								}
							}
						}
						reply, err := cloud.MergeValues(ctx, from, bundle)
						require.NoError(t, err)
						return reply
					}
					first := export("cache-a")
					var pA uint64
					for _, v := range first.Values {
						if len(v.Deps) == 0 {
							pA = v.Number
						}
					}
					replaceTestRan(t, ctx, cloud, pA)
					if held.compress {
						storedTestCompress(cloud)
					}
					reply := export("cache-b", tc.carried...)
					require.Equal(t, tc.want, reply.Rows.RunsReplaced)
				})
			}
		})
	}
}
