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

// runTestExport exports a record of field from an engine cache of its own,
// merges it into cloud as from, and returns the Cloud's number for the
// record and the engine's entry for it: the run's name.
func runTestExport(t *testing.T, ctx context.Context, cloud *Cache, from CacheID, field string) (uint64, RunName) {
	t.Helper()
	bundle, _ := mergeTestSource(t, field, string(from)+" "+field)
	reply, err := cloud.MergeValues(ctx, from, bundle)
	require.NoError(t, err)
	require.Len(t, reply.Values, 1)
	sender := bundle.Values[0]
	return reply.Values[0].Number, RunName{Cache: from, Number: sender.SenderNumber, Replacements: sender.SenderReplacements}
}

// runTestEvaluated applies a lazy row of key at replacements reporting the
// snapshot part complete: an evaluation, or an offer's install.
func runTestEvaluated(t *testing.T, ctx context.Context, cloud *Cache, key HolderKey, replacements uint64, evaluated bool) RemoteChange {
	t.Helper()
	change, found, err := cloud.UpdateRemoteHolding(ctx, key, RemoteHoldingUpdate{
		Parts:        []PersistedPartAddress{testLiveOffer().Address},
		Replacements: replacements,
		Evaluated:    evaluated,
	})
	require.NoError(t, err)
	require.True(t, found)
	return change
}

// runTestRan reports what the Cloud's stored record of number says of its
// run.
func runTestRan(t *testing.T, cloud *Cache, number uint64) EntryRun {
	t.Helper()
	run, ok := cloud.EntryRun(number)
	require.True(t, ok, "entry %d stores a record that names a run", number)
	return run
}

// The Cloud's stored record names the run that made it: the engine entry
// that exported it, at its replacement count. No bundle it sends carries it,
// and an engine names none.
func TestTheCloudsStoredRecordNamesItsRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	number, run := runTestExport(t, ctx, cloud, "cache-a", "named")
	got := runTestRan(t, cloud, number)
	require.Equal(t, run, got.Run)
	require.False(t, got.Ran, "a record from an export alone is a recipe until its run runs")

	sent, err := cloud.StoredBundle(ctx, []uint64{number}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.NotContains(t, string(mustTransferJSON(t, sent.Bundle.Values)), `"run"`, "the Cloud sends no run")
	bctx, b, _ := transferTestCache(t)
	breply, err := b.MergeValues(bctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	_, ok := b.EntryRun(breply.Values[0].Number)
	require.False(t, ok, "an engine names no run")
}

// A record an engine imported is the run of the Cloud entry its holding of
// it is on, unless the engine ran its copy itself; with no such holding, the
// Cloud names the engine's entry.
func TestAnImportedRecordIsTheRunOfTheEntryItCameFrom(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	number, run := runTestExport(t, ctx, cloud, "cache-a", "imported")
	sent, err := cloud.StoredBundle(ctx, []uint64{number}, time.Now().Unix(), nil)
	require.NoError(t, err)
	bctx, b, bsrv := transferTestCache(t)
	breply, err := b.MergeValues(bctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	bNumber := breply.Imported()[0].ResultID
	imported, err := b.LoadResultByResultID(bctx, "test-session", bsrv, bNumber)
	require.NoError(t, err)

	recipe, ok := cloud.EntryRecipe(number)
	require.True(t, ok)
	_, err = cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-b", Number: bNumber}, RemoteHolding{Recipe: recipe})
	require.NoError(t, err)
	held := exportTestBundle(t, bctx, b, imported)
	require.True(t, held.Values[0].Record.Envelope.Imported)
	// A's record has expired, so B's copy is installed in its place.
	cloud.egraphMu.Lock()
	cloud.resultsByID[sharedResultID(number)].expiresAtUnix = time.Now().Unix() - 10
	cloud.egraphMu.Unlock()
	reply, err := cloud.MergeValues(ctx, "cache-b", held)
	require.NoError(t, err)
	require.Equal(t, 1, reply.Rows.Replaced, "B's copy replaces A's expired record")
	require.Equal(t, run, runTestRan(t, cloud, reply.Values[0].Number).Run, "B's copy is A's run")

	// B ran its copy itself, for want of A's content: its copy is B's run.
	bKey := HolderKey{Cache: "cache-b", Number: bNumber}
	runTestEvaluated(t, ctx, cloud, bKey, held.Values[0].SenderReplacements, true)
	cloud.egraphMu.Lock()
	cloud.resultsByID[sharedResultID(reply.Values[0].Number)].expiresAtUnix = time.Now().Unix() - 10
	cloud.egraphMu.Unlock()
	reply, err = cloud.MergeValues(ctx, "cache-b", held)
	require.NoError(t, err)
	require.Equal(t, 1, reply.Rows.Replaced)
	require.Equal(t, EntryRun{Run: RunName{Cache: "cache-b", Number: bNumber, Replacements: held.Values[0].SenderReplacements}, Ran: true}, runTestRan(t, cloud, reply.Values[0].Number), "B evaluated its copy: B's run, which ran")

	// A second Cloud, which holds no holding of B's, names B's entry.
	ctx2, other := storedPartTestCache(t, WithBlobStore())
	unheld, err := other.MergeValues(ctx2, "cache-b", held)
	require.NoError(t, err)
	require.Equal(t, RunName{Cache: "cache-b", Number: held.Values[0].SenderNumber, Replacements: held.Values[0].SenderReplacements}, runTestRan(t, other, unheld.Values[0].Number).Run)
}

// A copy of a record the Cloud sent, installed in place of the record it was
// sent from, keeps what the Cloud knew of that record's run: the run ran,
// though its holding has since moved on to a later value.
func TestAnImportedCopyKeepsWhatTheCloudKnewOfItsRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	number, run := runTestExport(t, ctx, cloud, "cache-a", "kept-ran")
	aKey := HolderKey{Cache: run.Cache, Number: run.Number}
	runTestEvaluated(t, ctx, cloud, aKey, run.Replacements, true)
	_, found, err := cloud.UpdateRemoteHolding(ctx, aKey, RemoteHoldingUpdate{Replacements: run.Replacements + 1})
	require.NoError(t, err)
	require.True(t, found)

	sent, err := cloud.StoredBundle(ctx, []uint64{number}, time.Now().Unix(), nil)
	require.NoError(t, err)
	bctx, b, bsrv := transferTestCache(t)
	breply, err := b.MergeValues(bctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	bNumber := breply.Imported()[0].ResultID
	imported, err := b.LoadResultByResultID(bctx, "test-session", bsrv, bNumber)
	require.NoError(t, err)
	recipe, ok := cloud.EntryRecipe(number)
	require.True(t, ok)
	_, err = cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-b", Number: bNumber}, RemoteHolding{Recipe: recipe})
	require.NoError(t, err)
	held := exportTestBundle(t, bctx, b, imported)
	cloud.egraphMu.Lock()
	cloud.resultsByID[sharedResultID(number)].expiresAtUnix = time.Now().Unix() - 10
	cloud.egraphMu.Unlock()
	reply, err := cloud.MergeValues(ctx, "cache-b", held)
	require.NoError(t, err)
	require.Equal(t, 1, reply.Rows.Replaced, "B's copy replaces A's expired record")
	require.Equal(t, EntryRun{Run: run, Ran: true}, runTestRan(t, cloud, reply.Values[0].Number), "B's copy is A's run, which ran")
	require.Equal(t, 1, cloud.StoredRuns().RanByEvaluation, "learned as A's run was")
}

// A stored record's run ran once the run's own holding reports a part
// complete, from an executed call or an evaluation, at the run's replacement
// count, whether before the merge or after, or the record proves such a part
// complete itself. The write that learns it names the entry. A record that
// proves only its metadata, an executed call of a lazy result, which reports its parts
// pending, an install from a Cloud offer, another engine's evaluation, and an
// evaluation of a later value say nothing of the run.
func TestTheCloudLearnsThatARecordsRunRan(t *testing.T) {
	t.Parallel()
	t.Run("an evaluation after the merge", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		number, run := runTestExport(t, ctx, cloud, "cache-a", "evaluated")
		key := HolderKey{Cache: run.Cache, Number: run.Number}
		change := runTestEvaluated(t, ctx, cloud, key, run.Replacements, true)
		require.True(t, runTestRan(t, cloud, number).Ran)
		require.Contains(t, change.Entries, number, "the write names the entry, for readers that keep their answers")
		require.True(t, change.Saved, "a save writes what the record learned")
		require.Equal(t, 1, cloud.StoredRuns().RanByEvaluation)
	})
	t.Run("an evaluation of the run after a later value's report", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		number, run := runTestExport(t, ctx, cloud, "cache-a", "late")
		key := HolderKey{Cache: run.Cache, Number: run.Number}
		_, found, err := cloud.UpdateRemoteHolding(ctx, key, RemoteHoldingUpdate{Replacements: run.Replacements + 1})
		require.NoError(t, err)
		require.True(t, found)
		runTestEvaluated(t, ctx, cloud, key, run.Replacements, true)
		require.True(t, runTestRan(t, cloud, number).Ran, "the delayed report is of the stored run")
	})
	t.Run("an executed call after the merge", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		number, run := runTestExport(t, ctx, cloud, "cache-a", "executed")
		recipe, ok := cloud.EntryRecipe(number)
		require.True(t, ok)
		change, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: run.Cache, Number: run.Number}, RemoteHolding{
			Recipe:       recipe,
			Parts:        []PersistedPartAddress{testLiveOffer().Address},
			Replacements: run.Replacements,
			Executed:     true,
		})
		require.NoError(t, err)
		require.True(t, runTestRan(t, cloud, number).Ran)
		require.Contains(t, change.Entries, number)
		require.True(t, change.Saved)
		require.Equal(t, 1, cloud.StoredRuns().RanByCall)
	})
	t.Run("an executed call from before the last drop", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		number, run := runTestExport(t, ctx, cloud, "cache-a", "before-drop")
		recipe, ok := cloud.EntryRecipe(number)
		require.True(t, ok)
		key := HolderKey{Cache: run.Cache, Number: run.Number}
		_, err := cloud.AttachRemoteHolding(ctx, key, RemoteHolding{
			Recipe:         recipe,
			Parts:          []PersistedPartAddress{testLiveOffer().Address},
			Replacements:   run.Replacements,
			Executed:       true,
			BeforeLastDrop: true,
		})
		require.NoError(t, err)
		require.True(t, runTestRan(t, cloud, number).Ran, "the run ran, whenever its row came")
		cloud.egraphMu.RLock()
		defer cloud.egraphMu.RUnlock()
		_, h := cloud.holdingLocked(key)
		require.False(t, h.executed, "a computation from before the drop doesn't bring a dropped value back")
	})
	t.Run("an executed call of a lazy result", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		number, run := runTestExport(t, ctx, cloud, "cache-a", "lazy")
		recipe, ok := cloud.EntryRecipe(number)
		require.True(t, ok)
		_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: run.Cache, Number: run.Number}, RemoteHolding{Recipe: recipe, Replacements: run.Replacements, Executed: true})
		require.NoError(t, err)
		require.False(t, runTestRan(t, cloud, number).Ran, "the call returned the step before it ran")
		require.Equal(t, 1, cloud.StoredRuns().NotRan)
	})
	t.Run("an evaluation before the merge", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		bundle, recipe := mergeTestSource(t, "before", "before")
		sender := bundle.Values[0]
		key := HolderKey{Cache: "cache-a", Number: sender.SenderNumber}
		_, err := cloud.AttachRemoteHolding(ctx, key, RemoteHolding{Recipe: recipe, Replacements: sender.SenderReplacements})
		require.NoError(t, err)
		runTestEvaluated(t, ctx, cloud, key, sender.SenderReplacements, true)
		reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
		require.NoError(t, err)
		require.True(t, runTestRan(t, cloud, reply.Values[0].Number).Ran)
		require.Equal(t, 1, cloud.StoredRuns().RanAtMerge)
	})
	t.Run("an offer's install", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		number, run := runTestExport(t, ctx, cloud, "cache-a", "installed")
		change := runTestEvaluated(t, ctx, cloud, HolderKey{Cache: run.Cache, Number: run.Number}, run.Replacements, false)
		require.False(t, runTestRan(t, cloud, number).Ran, "the run's engine installed the part: it never evaluated the step")
		require.False(t, change.Saved, "nothing a save writes changed")
		counts := cloud.StoredRuns()
		require.Equal(t, 1, counts.NotRan)
		recipe, ok := cloud.EntryRecipe(number)
		require.True(t, ok)
		require.Equal(t, []NotRanRecord{{Number: number, Recipe: recipe, Run: run}}, counts.NotRanRecords, "a reader finds the run among those that didn't")
	})
	t.Run("a record that proves a part complete", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		ectx, e, esrv := transferTestCache(t)
		r := persistedListTestResult(t, ectx, e, esrv, "proves", &transferTestValue{Text: "proves", Parts: []transferTestPart{{Part: "snapshot", State: "completed", Kind: "directory"}}})
		reply, err := cloud.MergeValues(ctx, "cache-a", exportTestBundle(t, ectx, e, r))
		require.NoError(t, err)
		require.Len(t, reply.Values, 1)
		require.True(t, runTestRan(t, cloud, reply.Values[0].Number).Ran, "its run ran before any telemetry says so")
		counts := cloud.StoredRuns()
		require.Equal(t, 1, counts.RanByProof)
		require.Empty(t, counts.NotRanRecords)
	})
	t.Run("a record that proves only its metadata", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		ectx, e, esrv := transferTestCache(t)
		r := persistedListTestResult(t, ectx, e, esrv, "metadata", &transferTestValue{Text: "metadata", Parts: []transferTestPart{{Part: "metadata", State: "metadata"}, {Part: "snapshot", State: "pending", Kind: "directory"}}})
		reply, err := cloud.MergeValues(ctx, "cache-a", exportTestBundle(t, ectx, e, r))
		require.NoError(t, err)
		require.Len(t, reply.Values, 1)
		require.False(t, runTestRan(t, cloud, reply.Values[0].Number).Ran, "its step's content is still pending")
	})
	t.Run("another engine's evaluation", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		number, _ := runTestExport(t, ctx, cloud, "cache-a", "other")
		recipe, ok := cloud.EntryRecipe(number)
		require.True(t, ok)
		other := HolderKey{Cache: "cache-b", Number: 7}
		_, err := cloud.AttachRemoteHolding(ctx, other, RemoteHolding{Recipe: recipe, Executed: true})
		require.NoError(t, err)
		runTestEvaluated(t, ctx, cloud, other, 0, true)
		require.False(t, runTestRan(t, cloud, number).Ran, "B's run of the entry isn't A's")
	})
	t.Run("an evaluation of a later value", func(t *testing.T) {
		t.Parallel()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		number, run := runTestExport(t, ctx, cloud, "cache-a", "later")
		runTestEvaluated(t, ctx, cloud, HolderKey{Cache: run.Cache, Number: run.Number}, run.Replacements+1, true)
		require.False(t, runTestRan(t, cloud, number).Ran, "the engine ran a value that replaced the run's")
	})
}

// From a holding's side, the Cloud says whose run its entry's stored record
// is: the holding's own, at its replacement count, or another's; whether that
// run ran; and whether the run's own holding is still held at the run's
// count. A holding that moves on to a later value no longer holds the run.
func TestHoldingRunsSayWhoseRunAHoldingsRecordIs(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	number, run := runTestExport(t, ctx, cloud, "cache-a", "holding-runs")
	a := HolderKey{Cache: run.Cache, Number: run.Number}
	recipe, ok := cloud.EntryRecipe(number)
	require.True(t, ok)
	b := HolderKey{Cache: "cache-b", Number: 7}
	_, err := cloud.AttachRemoteHolding(ctx, b, RemoteHolding{Recipe: recipe})
	require.NoError(t, err)
	unknown := HolderKey{Cache: "cache-c", Number: 9}

	require.Equal(t, []HoldingRun{
		{Stored: true, Run: run, Own: true, RunHeld: true},
		{Stored: true, Run: run, RunHeld: true},
		{},
	}, cloud.HoldingRuns([]HolderKey{a, b, unknown}))

	runTestEvaluated(t, ctx, cloud, a, run.Replacements, true)
	require.Equal(t, []HoldingRun{{Stored: true, Run: run, Ran: true, RunHeld: true}}, cloud.HoldingRuns([]HolderKey{b}))

	_, found, err := cloud.UpdateRemoteHolding(ctx, a, RemoteHoldingUpdate{Replacements: run.Replacements + 1})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []HoldingRun{
		{Stored: true, Run: run, Ran: true},
		{Stored: true, Run: run, Ran: true},
	}, cloud.HoldingRuns([]HolderKey{a, b}), "A's engine moved on to a later value")
}

// What a stored record says of its run is saved with it, whole or
// compressed, and comes back over a restart; a prune of the record takes it
// with the value.
func TestARecordsRunIsSavedAndGoesWithItsValue(t *testing.T) {
	t.Parallel()
	for _, compress := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "compressed"}[compress], func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "cloud.db")
			ctx, cloud := storedPersistenceTestCloud(t, path)
			ran, run := runTestExport(t, ctx, cloud, "cache-a", "saved-ran")
			runTestEvaluated(t, ctx, cloud, HolderKey{Cache: run.Cache, Number: run.Number}, run.Replacements, true)
			recipe, recipeRun := runTestExport(t, ctx, cloud, "cache-b", "saved-recipe")
			if compress {
				storedTestCompress(cloud)
			}
			require.NoError(t, cloud.Close(ctx))

			ctx, cloud = storedPersistenceTestCloud(t, path)
			defer func() { require.NoError(t, cloud.Close(ctx)) }()
			require.Equal(t, EntryRun{Run: run, Ran: true}, runTestRan(t, cloud, ran))
			require.Equal(t, EntryRun{Run: recipeRun}, runTestRan(t, cloud, recipe))
			counts := cloud.StoredRuns()
			require.Equal(t, 1, counts.RanByEvaluation)
			require.Equal(t, 1, counts.NotRan)
		})
	}
}

// A prune that drops a stored record's value drops its run with it.
func TestAPruneDropsARecordsRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	number, run := runTestExport(t, ctx, cloud, "cache-a", "pruned")
	runTestEvaluated(t, ctx, cloud, HolderKey{Cache: run.Cache, Number: run.Number}, run.Replacements, true)
	require.Equal(t, EntryRun{Run: run, Ran: true}, runTestRan(t, cloud, number), "before the prune")
	cloud.egraphMu.Lock()
	res := cloud.resultsByID[sharedResultID(number)]
	_, err := cloud.dropValueLocked(ctx, res)
	cloud.egraphMu.Unlock()
	require.NoError(t, err)
	_, ok := cloud.EntryRun(number)
	require.False(t, ok, "the run went with the value")
	cloud.egraphMu.RLock()
	defer cloud.egraphMu.RUnlock()
	require.Nil(t, res.run)
	require.Equal(t, runNotRan, res.runRan)
	require.False(t, slices.Contains([]runRanBy{runRanAtMerge, runRanByCall, runRanByEvaluation}, res.runRan))
}

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
	reply, err := mergeExport(t, ctx, cloud, "cache-a", bundle)
	require.NoError(t, err)
	number := reply.Imported()[0].ResultID
	run, ok := entryRunName(cloud, number)
	require.True(t, ok)
	require.Equal(t, ownRun("cache-a", bundle.Values[0]), run, "the entry holds the run of the engine that made it")
	require.Equal(t, run, reply.Values[0].Run, "the reply names it")
	// A's own engine ran the step: its record is a run, not a recipe.
	replaceTestRan(t, ctx, cloud, number)

	// Engine B ran the recipe itself and exports its own run: its record
	// lands on A's entry, which keeps A's record.
	second, _ := mergeTestSource(t, "root", "another value")
	breply, err := mergeExport(t, ctx, cloud, "cache-b", second)
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
	reply, err := mergeExport(t, ctx, cloud, "cache-a", bundle)
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
	unheld, err := mergeExport(t, ctx, cloud, "cache-b", exported)
	require.NoError(t, err)
	require.Equal(t, ownRun("cache-b", exported.Values[0]), unheld.Values[0].Run)

	// With B's holding on the entry B was sent, B's record is A's run.
	recipe, ok := cloud.EntryRecipe(number)
	require.True(t, ok)
	_, err = cloud.AttachRemoteHolding(ctx, HolderKey{Cache: "cache-c", Number: bNumber}, RemoteHolding{Recipe: recipe})
	require.NoError(t, err)
	again := exportTestBundle(t, bctx, b, imported)
	held, err := mergeExport(t, ctx, cloud, "cache-c", again)
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
	_, ok := entryRunName(cloud, r)
	require.False(t, ok, "no value, no run")

	actx, a, asrv := transferTestCache(t)
	root := persistedListTestResult(t, actx, a, asrv, "bundle-root", &transferTestValue{Text: "root"})
	dep := persistedListTestResult(t, actx, a, asrv, "bundle-dep", &transferTestValue{Text: "dep"})
	transferTestDependency(a, actx, root, dep)
	bundle := exportTestBundle(t, actx, a, root)
	reply, err := mergeExport(t, ctx, cloud, "cache-c", bundle)
	require.NoError(t, err)
	require.Equal(t, r, reply.Imported()[0].ResultID, "the same entry stores again")
	run, ok := entryRunName(cloud, r)
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
		run, ok := entryRunName(cloud, number)
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
			first, err := mergeExport(t, ctx, cloud, "cache-a", source("cache-a", tc.a, ""))
			require.NoError(t, err)
			// A's input has its content, so B's run of it doesn't replace
			// it (replaceRunsLocked).
			p := first.Imported()[0].ResultID
			stored, _, err := cloud.SetStoredPart(ctx, p, storedPartRunOf(t, cloud, p), testLiveOffer(), 0)
			require.NoError(t, err)
			require.True(t, stored)
			reply, err := mergeExport(t, ctx, cloud, "cache-b", source("cache-b", tc.b, "over-r"))
			require.NoError(t, err)
			got := MergeRowCounts{OverOtherRunEqual: reply.Rows.OverOtherRunEqual, OverOtherRunDifferent: reply.Rows.OverOtherRunDifferent, OverOtherRunUnknown: reply.Rows.OverOtherRunUnknown}
			require.Equal(t, tc.want, got)
		})
	}
}

// mergeExport merges an engine's export into cloud as a remote cache service
// does: its commit marks the uploads the export carries
// (MergeReply.Uploading), and the service ends them once it has taken its
// own claims, which a test that stores no part takes none of.
func mergeExport(t *testing.T, ctx context.Context, cloud *Cache, from CacheID, bundle ValueBundle) (MergeReply, error) {
	t.Helper()
	reply, err := cloud.MergeValues(ctx, from, bundle)
	if _, endErr := cloud.EndPartUploads(reply.Uploading); endErr != nil {
		return reply, endErr
	}
	return reply, err
}

// mergeHeld merges an engine's export into cloud as a remote cache service's
// commit does (PrepareValueMerge, CommitValueMerge), leaving the uploads the
// commit marked (MergeReply.Uploading) for the caller to end: the export's
// claims would take them over.
func mergeHeld(t *testing.T, ctx context.Context, cloud *Cache, from CacheID, bundle ValueBundle) (MergeReply, error) {
	t.Helper()
	p, err := cloud.PrepareValueMerge(ctx, from, bundle)
	if err != nil {
		return MergeReply{}, err
	}
	return cloud.CommitValueMerge(ctx, p)
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
	reply, err := mergeExport(t, ctx, cloud, from, bundle)
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
		run, ok := entryRunName(cloud, n)
		require.True(t, ok)
		require.Equal(t, CacheID("cache-b"), run.Cache, "entry %d is B's run", n)
	}
	retired := func(cloud *Cache, when string) {
		t.Helper()
		require.Equal(t, []uint64{p2}, cloud.EquivalentEntries(pRecipe.String()), "%s: a lookup of the dep finds B's alone", when)
		require.Equal(t, []uint64{r2}, cloud.EquivalentEntries(rRecipe.String()), "%s: a lookup of the root finds B's alone", when)
		require.True(t, cloud.HasEntry(r1), "%s: the cache has A's retired root", when)
		_, ok := cloud.EntryRecipe(r1)
		require.False(t, ok, "%s: though no recipe names it", when)
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
// run doesn't replace it while the upload's claim holds (StartPartUploads).
func TestALaterRunDoesntReplaceARecordWhoseContentIsUploading(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, r1, p1 := replaceTestRun(t, ctx, cloud, "cache-a")
	for _, n := range []uint64{r1, p1} {
		replaceTestRan(t, ctx, cloud, n)
		require.NoError(t, runTestStartUpload(cloud, n, testLiveOffer().Address))
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
// replaceTestRanOnly makes the stored record of the entry numbered number
// one whose own run ran: its run's holding reports, from a call it
// executed, a part complete (noteHoldingRanLocked).
func replaceTestRanOnly(t *testing.T, ctx context.Context, cloud *Cache, number uint64) {
	t.Helper()
	run, ok := entryRunName(cloud, number)
	require.True(t, ok)
	recipe, ok := cloud.EntryRecipe(number)
	require.True(t, ok)
	_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: run.Cache, Number: run.Number}, RemoteHolding{Recipe: recipe, Replacements: run.Replacements, Executed: true, Parts: []PersistedPartAddress{testLiveOffer().Address}})
	require.NoError(t, err)
}

// replaceTestRan makes the stored record of the entry numbered number one
// whose own run ran (replaceTestRanOnly) and whose content won't come, as
// the service notes it (NoteRunContent): a later run may replace it.
func replaceTestRan(t *testing.T, ctx context.Context, cloud *Cache, number uint64) {
	t.Helper()
	replaceTestRanOnly(t, ctx, cloud, number)
	run, ok := entryRunName(cloud, number)
	require.True(t, ok)
	cloud.NoteRunContent(number, run, true)
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
		run, ok := entryRunName(cloud, p)
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
		reply, err := mergeExport(t, ctx, cloud, "cache-a", bundle)
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
	reply, err := mergeExport(t, ctx, cloud, "cache-a", bundle)
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
	reply, err := mergeExport(t, ctx, cloud, "cache-b", held)
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
	reply, err := mergeExport(t, ctx, cloud, "cache-a", bundle)
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
	before, ok := entryRunName(cloud, p)
	require.True(t, ok)
	cloud.egraphMu.Lock()
	for parentID := range cloud.resultsByID[sharedResultID(p)].depParents {
		cloud.resultsByID[parentID].run = &RunName{Cache: "cache-third", Number: 1}
	}
	cloud.egraphMu.Unlock()
	got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: p, Run: RunName{Cache: "cache-b", Number: 9}, Part: testLiveOffer(), CopyExpiresAtUnix: 1}})
	require.NoError(t, err)
	require.False(t, got[0].Stored, "the expired copy is refused")
	run, ok := entryRunName(cloud, p)
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
	reply, err := mergeExport(t, ctx, cloud, "cache-b", full)
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

	// B's export carries P's content, which a commit would mark.
	held := engineHeldTestBundle(t, b, full, p)
	reply, err := cloud.MergeValues(ctx, "cache-b", held)
	require.NoError(t, err)
	require.False(t, reply.Committed)
	require.Len(t, reply.Stale, 1, "B's held row on A's record is stale")
	require.Empty(t, reply.Uploading, "a stale merge commits nothing, and marks nothing")

	// With P in full and R held over it, R's held row lands on A's record
	// over P, which the replacement would retire: stale too.
	both := withContent(t, exportTestBundle(t, bctxOf(t, b), b, r, p), r, p)
	reply, err = mergeExport(t, ctx, cloud, "cache-b", engineHeldTestBundle(t, b, both, r))
	require.NoError(t, err)
	require.False(t, reply.Committed)
	require.Len(t, reply.Stale, 1, "B's held row on A's record over it is stale")

	again, err := mergeExport(t, ctx, cloud, "cache-b", full)
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
	reply, err := mergeExport(t, ctx, cloud, "cache-b", engineHeldTestBundle(t, b, full, p))
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
	require.NoError(t, runTestStartUpload(cloud, pA, testLiveOffer().Address))
	held := engineHeldTestBundle(t, b, full, p)
	reply, err := mergeExport(t, ctx, cloud, "cache-b", held)
	require.NoError(t, err)
	require.Empty(t, reply.Stale, "A's content is on its way")

	_, err = cloud.EndPartUploads([]PartUpload{{Number: pA, Address: testLiveOffer().Address}})
	require.NoError(t, err)
	reply, err = mergeExport(t, ctx, cloud, "cache-b", held)
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
	reply, err := mergeExport(t, ctx, cloud, "cache-b", engineHeldTestBundle(t, b, full, p))
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
		reply, err := mergeExport(t, ctx, cloud, from, bundle)
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

// Two engines that both ran a step, each exporting its whole content
// before its upload starts, don't replace each other's records in turn:
// each merge's marks, which its caller holds until its own claims are
// taken, say the content of the run the Cloud holds is on its way. Over A,
// B, A and B's exports, a run replaces another's record at most once.
func TestTwoRunsWithContentOnItsWayDontReplaceEachOther(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	export := func(from CacheID) MergeReply {
		t.Helper()
		ectx, e, esrv := transferTestCache(t)
		r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: string(from) + " root"})
		p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: string(from) + " dep"})
		transferTestDependency(e, ectx, r, p)
		reply, err := mergeHeld(t, ctx, cloud, from, withContent(t, exportTestBundle(t, ectx, e, r), p))
		require.NoError(t, err)
		for _, v := range reply.Values {
			if len(v.Deps) == 0 {
				// The engine's run of the step ran: its holding reports
				// the part complete, as its telemetry does.
				replaceTestRan(t, ctx, cloud, v.Number)
			}
		}
		return reply
	}
	replaced := 0
	for i, from := range []CacheID{"cache-a", "cache-b", "cache-a", "cache-b"} {
		reply := export(from)
		replaced += reply.Rows.RunsReplaced
		t.Logf("export %d from %s: %d replaced, %d stale", i, from, reply.Rows.RunsReplaced, len(reply.Stale))
	}
	require.LessOrEqual(t, replaced, 1, "two runs whose content is on its way replace each other's records in turn")
}

// A merge marks in progress, in its commit, the upload of each part its
// export carries on a record of the sender's own run, and names the marks in
// its reply: until its caller ends them, the record isn't replaced, and once
// they end without the content arriving, it is. A record of the sender's run
// kept on another run's entry is marked nothing.
func TestAMergeMarksTheUploadsItsExportCarries(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	export := func(from CacheID) MergeReply {
		t.Helper()
		ectx, e, esrv := transferTestCache(t)
		r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: string(from) + " root"})
		p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: string(from) + " dep"})
		transferTestDependency(e, ectx, r, p)
		reply, err := mergeHeld(t, ctx, cloud, from, withContent(t, exportTestBundle(t, ectx, e, r), p))
		require.NoError(t, err)
		for _, v := range reply.Values {
			if len(v.Deps) == 0 {
				replaceTestRan(t, ctx, cloud, v.Number)
			}
		}
		return reply
	}
	a := export("cache-a")
	var pA uint64
	for _, v := range a.Values {
		if len(v.Deps) == 0 {
			pA = v.Number
		}
	}
	require.Equal(t, []PartUpload{{Number: pA, Address: PersistedPartAddress{Part: "snapshot"}}}, a.Uploading, "A's merge marks the part it carries")
	require.True(t, cloud.PartUploading(pA, PersistedPartAddress{Part: "snapshot"}))
	require.Contains(t, a.Change.Entries, pA, "and names the entry, whose answers count the mark")

	b := export("cache-b")
	require.Zero(t, b.Rows.RunsReplaced, "A's content is on its way: B doesn't replace A's record")
	require.Empty(t, b.Uploading, "B's record kept on A's entry is marked nothing")

	_, err := cloud.EndPartUploads(a.Uploading)
	require.NoError(t, err)
	require.False(t, cloud.PartUploading(pA, PersistedPartAddress{Part: "snapshot"}), "A's marks end")
	c := export("cache-c")
	require.Equal(t, 1, c.Rows.RunsReplaced, "A's content never came: C's run replaces A's record")
	var replaced bool
	for _, v := range c.Values {
		replaced = replaced || v.Replaced
	}
	require.True(t, replaced, "C's reply says which records the replacement installed")
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
	reply, err := mergeExport(t, ctx, cloud, "cache-b", held)
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
						reply, err := mergeExport(t, ctx, cloud, from, bundle)
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

// entryRunName is the run the Cloud's stored record of the entry numbered
// number names (EntryRun), or false.
func entryRunName(c *Cache, number uint64) (RunName, bool) {
	run, ok := c.EntryRun(number)
	return run.Run, ok
}

// A record whose own run ran and stored nothing isn't replaced while its
// run's content may still come: records reach the Cloud before their
// content. Once the service notes it won't (NoteRunContent), a later run's
// whole export replaces it, also after a restart, since the fact is saved.
func TestARecordIsReplacedOnlyOnceItsRunsContentWontCome(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cloud.db")
	ctx, cloud := storedPersistenceTestCloud(t, path)
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRanOnly(t, ctx, cloud, pA)
	reply, _, _ := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Zero(t, reply.Rows.RunsReplaced, "A's content may still come")
	run, ok := entryRunName(cloud, pA)
	require.True(t, ok)
	change := cloud.NoteRunContent(pA, run, true)
	require.True(t, change.Saved)
	require.Contains(t, change.Entries, pA, "a logged entry change, for kept inputs")
	require.Empty(t, cloud.NoteRunContent(pA, RunName{Cache: "cache-b", Number: run.Number}, true).Entries, "another run's note changes nothing")
	require.NoError(t, cloud.Close(ctx))

	ctx, cloud = storedPersistenceTestCloud(t, path)
	defer func() { require.NoError(t, cloud.Close(ctx)) }()
	// The holding isn't saved: a restarted Cloud learns it again, from the
	// run's engine's rows.
	replaceTestRanOnly(t, ctx, cloud, pA)
	reply, _, _ = replaceTestRun(t, ctx, cloud, "cache-c")
	require.Equal(t, 1, reply.Rows.RunsReplaced, "the fact is saved: C's run replaces A's")
}

// The run's own export carrying its content says the content is on its
// way: a fact that it won't come is cleared in that merge's commit.
func TestARunsOwnContentClearsTheFactThatItWontCome(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRan(t, ctx, cloud, pA)
	cloud.egraphMu.RLock()
	require.True(t, cloud.resultsByID[sharedResultID(pA)].runWontCome)
	cloud.egraphMu.RUnlock()
	// A's engine exports its run again, with the step's content.
	again, _, pA2 := replaceTestRun(t, ctx, cloud, "cache-a")
	require.Equal(t, pA, pA2, "A's own run lands on its own record")
	require.Zero(t, again.Rows.RunsReplaced)
	cloud.egraphMu.RLock()
	require.False(t, cloud.resultsByID[sharedResultID(pA)].runWontCome, "A's content is on its way")
	cloud.egraphMu.RUnlock()
	reply, _, _ := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Zero(t, reply.Rows.RunsReplaced, "so B's run doesn't replace A's")
}

// A record whose run's holding has moved on to another value is replaced
// without a note: what that engine would send now is another run's.
func TestARecordWhoseRunsHoldingMovedOnIsReplaced(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRanOnly(t, ctx, cloud, pA)
	run, ok := entryRunName(cloud, pA)
	require.True(t, ok)
	_, _, err := cloud.ApplyHeldValueState(ctx, HolderKey{Cache: run.Cache, Number: run.Number}, HeldValueState{Replacements: run.Replacements + 1})
	require.NoError(t, err)
	reply, _, _ := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Equal(t, 1, reply.Rows.RunsReplaced)
}

// An engine's connection ending notes, for each record of its runs that
// stores nothing and has no upload in progress, that its content won't
// come; a record with its content stored, or uploading, keeps no such note.
func TestAnEnginesEndNotesItsUnstoredRunsContentWontCome(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, rA, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	stored, _, err := cloud.SetStoredPart(ctx, rA, storedPartRunOf(t, cloud, rA), testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)
	change := cloud.NoteCacheGone("cache-a")
	require.True(t, change.Saved)
	require.Contains(t, change.Entries, pA)
	cloud.egraphMu.RLock()
	require.True(t, cloud.resultsByID[sharedResultID(pA)].runWontCome, "P stores nothing")
	require.False(t, cloud.resultsByID[sharedResultID(rA)].runWontCome, "R stores its content")
	cloud.egraphMu.RUnlock()
	require.Empty(t, cloud.NoteCacheGone("cache-a").Entries, "noted once")
}

// Two runs shown the same by content digest are one value: a part of
// either is stored onto the other's record. A run not shown the same, the
// same call only, is another value, and refused.
func TestAPartOfARunShownEqualByContentIsStored(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, p := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRanOnly(t, ctx, cloud, p)
	recipe, ok := cloud.EntryRecipe(p)
	require.True(t, ok)
	content := digest.FromString("the dep's content")
	run, ok := entryRunName(cloud, p)
	require.True(t, ok)
	_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: run.Cache, Number: run.Number}, RemoteHolding{Recipe: recipe, Replacements: run.Replacements, ContentDigest: content})
	require.NoError(t, err)
	same := RunName{Cache: "cache-b", Number: 7}
	_, err = cloud.AttachRemoteHolding(ctx, HolderKey{Cache: same.Cache, Number: same.Number}, RemoteHolding{Recipe: recipe, ContentDigest: content})
	require.NoError(t, err)
	other := RunName{Cache: "cache-c", Number: 8}
	_, err = cloud.AttachRemoteHolding(ctx, HolderKey{Cache: other.Cache, Number: other.Number}, RemoteHolding{Recipe: recipe})
	require.NoError(t, err)
	got, _, err := cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: p, Run: other, Part: testLiveOffer()}})
	require.NoError(t, err)
	require.True(t, got[0].OtherRun, "a run of the same call only is refused")
	got, _, err = cloud.SetStoredParts(ctx, []StoredPartWrite{{Number: p, Run: same, Part: testLiveOffer()}})
	require.NoError(t, err)
	require.True(t, got[0].Stored, "a run shown equal by content is stored")
	stillA, ok := entryRunName(cloud, p)
	require.True(t, ok)
	require.Equal(t, run, stillA, "and the record keeps its run")
}

// An upload is of its holding's own run once that holding ran its value,
// as an engine that imported a record and evaluated it does; else of the
// run its merge named.
func TestAnUploadIsItsHoldingsOwnRunOnceItRan(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, p := replaceTestRun(t, ctx, cloud, "cache-a")
	recipe, ok := cloud.EntryRecipe(p)
	require.True(t, ok)
	merged := RunName{Cache: "cache-a", Number: 99}
	key := HolderKey{Cache: "cache-b", Number: 5}
	_, err := cloud.AttachRemoteHolding(ctx, key, RemoteHolding{Recipe: recipe, Replacements: 2})
	require.NoError(t, err)
	require.Equal(t, merged, cloud.UploadRun(key, 2, merged), "it hasn't run its value")
	_, err = cloud.AttachRemoteHolding(ctx, key, RemoteHolding{Recipe: recipe, Replacements: 2, Executed: true, Parts: []PersistedPartAddress{testLiveOffer().Address}})
	require.NoError(t, err)
	require.Equal(t, RunName{Cache: "cache-b", Number: 5, Replacements: 2}, cloud.UploadRun(key, 2, merged))
	require.Equal(t, merged, cloud.UploadRun(key, 1, merged), "at another count it describes another value")
}

// An engine's end notes its run's content won't come even while another
// run's upload of the entry is in flight: an upload mark doesn't say whose
// run it is, and another run's bytes don't bring this one's content. The
// record isn't replaced until that upload ends, but the note stands.
func TestAnEnginesEndNotesItsRunWhileAnotherRunsUploadIsInFlight(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRanOnly(t, ctx, cloud, pA)
	// B's upload of the entry is claimed, its bytes another run's.
	require.NoError(t, runTestStartUpload(cloud, pA, testLiveOffer().Address))
	change := cloud.NoteCacheGone("cache-a")
	require.Contains(t, change.Entries, pA)
	require.True(t, cloud.RunContentWontCome(pA))
	cloud.egraphMu.RLock()
	require.False(t, cloud.replaceableLocked(cloud.resultsByID[sharedResultID(pA)], RunName{Cache: "cache-c", Number: 1}, time.Now().Unix()), "not while the upload is in flight")
	cloud.egraphMu.RUnlock()
	_, err := cloud.EndPartUploads([]PartUpload{{Number: pA, Address: testLiveOffer().Address}})
	require.NoError(t, err)
	reply, _, _ := replaceTestRun(t, ctx, cloud, "cache-c")
	require.Equal(t, 1, reply.Rows.RunsReplaced, "once it ends, a whole later run replaces A's")
}

// A record its sender imported and then ran itself is the sender's own
// run (mergeRunLocked): with the step's whole content, it replaces another
// run's record whose content won't come. A copy its sender only took
// replaces nothing.
func TestAnImportedRecordItsSenderRanReplacesARunWhoseContentWontCome(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRan(t, ctx, cloud, pA)
	recipe, ok := cloud.EntryRecipe(pA)
	require.True(t, ok)
	export := func(from CacheID, ran bool) MergeReply {
		t.Helper()
		ectx, e, esrv := transferTestCache(t)
		r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: string(from) + " root"})
		p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: string(from) + " dep"})
		transferTestDependency(e, ectx, r, p)
		bundle := withContent(t, exportTestBundle(t, ectx, e, r), p)
		for i := range bundle.Values {
			if bundle.Values[i].SenderNumber != uint64(p.cacheSharedResult().id) {
				continue
			}
			// The engine took the record from the Cloud.
			bundle.Values[i].Record.Envelope.Imported = true
			if ran {
				// Then evaluated it itself, for want of its content.
				v := bundle.Values[i]
				_, err := cloud.AttachRemoteHolding(ctx, HolderKey{Cache: from, Number: v.SenderNumber}, RemoteHolding{Recipe: recipe, Replacements: v.SenderReplacements, Executed: true, Parts: []PersistedPartAddress{testLiveOffer().Address}})
				require.NoError(t, err)
			}
		}
		reply, err := mergeExport(t, ctx, cloud, from, bundle)
		require.NoError(t, err)
		return reply
	}
	require.Zero(t, export("cache-b", false).Rows.RunsReplaced, "a copy B only took replaces nothing")
	require.Equal(t, 1, export("cache-c", true).Rows.RunsReplaced, "C's own run of the copy it took replaces A's")
}

// MergeValues takes no claims after its commit, so it ends the uploads its
// commit marked: nothing it merges is left counted as uploading.
func TestMergeValuesLeavesNoUploadMarked(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	ectx, e, esrv := transferTestCache(t)
	r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: "a root"})
	p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: "a dep"})
	transferTestDependency(e, ectx, r, p)
	reply, err := cloud.MergeValues(ctx, "cache-a", withContent(t, exportTestBundle(t, ectx, e, r), p))
	require.NoError(t, err)
	require.Empty(t, reply.Uploading)
	for _, v := range reply.Values {
		require.False(t, cloud.PartUploading(v.Number, PersistedPartAddress{Part: "snapshot"}), "entry %d", v.Number)
	}
}

// runTestStartUpload starts an upload of the part at address for the entry
// numbered number, as a remote cache service's reader does
// (StartPartUploads), and errs unless it started.
func runTestStartUpload(cloud *Cache, number uint64, address PersistedPartAddress) error {
	got, _ := cloud.StartPartUploads([]PartStartQuery{{PartQuery: PartQuery{Number: number, Address: address}}}, time.Now().Unix())
	if got[0].Err != nil {
		return got[0].Err
	}
	if !got[0].Started {
		return fmt.Errorf("upload of %d not started: %+v", number, got[0])
	}
	return nil
}

// A retired entry is the run a later one replaced: its stored part doesn't
// answer for the entry that took its place, whose bytes are another run's.
// A's root stores its content over A's dep; B's run replaces A's dep and
// retires A's root over it; B's root, of the same call as A's, doesn't count
// A's root's part as stored.
func TestAPartAnswerCountsOnlyWhatTheEntryMayLend(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, rA, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	stored, _, err := cloud.SetStoredPart(ctx, rA, storedPartRunOf(t, cloud, rA), testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)
	replaceTestRan(t, ctx, cloud, pA)
	reply, rB, _ := replaceTestRun(t, ctx, cloud, "cache-b")
	require.Equal(t, 1, reply.Rows.RunsReplaced)
	require.NotEqual(t, rA, rB)
	av, err := cloud.AvailablePart(rB, testLiveOffer().Address, time.Now().Unix())
	require.NoError(t, err)
	require.NotEqual(t, PartStored, av.State, "A's root's bytes are another run's")
	av, err = cloud.AvailablePart(rA, testLiveOffer().Address, time.Now().Unix())
	require.NoError(t, err)
	require.Equal(t, PartStored, av.State, "A's root keeps its content for what names it")
}

// An upload of a part a holding's run may not give the entry's record isn't
// started: the store would refuse its bytes, and its count would answer the
// record's own run as uploading.
func TestAnUploadOfAnotherRunsPartIsNotStarted(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRanOnly(t, ctx, cloud, pA)
	recipe, ok := cloud.EntryRecipe(pA)
	require.True(t, ok)
	b := HolderKey{Cache: "cache-b", Number: 3}
	_, err := cloud.AttachRemoteHolding(ctx, b, RemoteHolding{Recipe: recipe, Executed: true, Parts: []PersistedPartAddress{testLiveOffer().Address}})
	require.NoError(t, err)
	got, _ := cloud.StartPartUploads([]PartStartQuery{{PartQuery: PartQuery{Number: pA, Address: testLiveOffer().Address, Holder: b}}}, time.Now().Unix())
	require.NoError(t, got[0].Err)
	require.False(t, got[0].Started, "B's bytes would be refused")
	require.Equal(t, PartOtherRun, got[0].State)
	require.False(t, cloud.PartUploading(pA, testLiveOffer().Address), "and A's own run isn't answered as uploading")
}

// A query naming a holding of another run than the entry's record, which
// may not give the record its part, is answered PartOtherRun; once the
// record is replaceable, it is answered as any other.
func TestAPartAnswerForAHoldingOfAnotherRunSaysSo(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRanOnly(t, ctx, cloud, pA)
	recipe, ok := cloud.EntryRecipe(pA)
	require.True(t, ok)
	b := HolderKey{Cache: "cache-b", Number: 3}
	_, err := cloud.AttachRemoteHolding(ctx, b, RemoteHolding{Recipe: recipe, Executed: true, Parts: []PersistedPartAddress{testLiveOffer().Address}})
	require.NoError(t, err)
	ask := func(holder HolderKey) PartAvailabilityState {
		t.Helper()
		got := cloud.AvailableParts([]PartQuery{{Number: pA, Address: testLiveOffer().Address, Holder: holder}}, time.Now().Unix())
		require.NoError(t, got[0].Err)
		return got[0].State
	}
	run, ok := entryRunName(cloud, pA)
	require.True(t, ok)
	require.Equal(t, PartUnavailable, ask(HolderKey{Cache: run.Cache, Number: run.Number}), "the record's own run is asked")
	require.Equal(t, PartOtherRun, ask(b), "B's run may not give A's record its part")
	require.Equal(t, PartUnavailable, ask(HolderKey{}), "an entry's answer names no holding")
	cloud.NoteRunContent(pA, run, true)
	require.Equal(t, PartUnavailable, ask(b), "once A's content won't come, B's whole run may replace it")
}

// The sender whose run replaces a record has its holding moved to the entry
// its own record is installed on: what it holds is its run, not the retired
// one.
func TestAReplacingSendersHoldingMovesToItsOwnRecord(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	_, _, pA := replaceTestRun(t, ctx, cloud, "cache-a")
	replaceTestRan(t, ctx, cloud, pA)
	// B's engine holds the step before its export, on A's entry.
	recipe, ok := cloud.EntryRecipe(pA)
	require.True(t, ok)
	ectx, e, esrv := transferTestCache(t)
	r := persistedListTestResult(t, ectx, e, esrv, "replace-root", &transferTestValue{Text: "cache-b root"})
	p := persistedListTestResult(t, ectx, e, esrv, "replace-dep", &transferTestValue{Text: "cache-b dep"})
	transferTestDependency(e, ectx, r, p)
	key := HolderKey{Cache: "cache-b", Number: uint64(p.cacheSharedResult().id)}
	_, err := cloud.AttachRemoteHolding(ctx, key, RemoteHolding{Recipe: recipe, Executed: true, Parts: []PersistedPartAddress{testLiveOffer().Address}})
	require.NoError(t, err)
	before, ok := cloud.HolderEntry(key)
	require.True(t, ok)
	require.Equal(t, pA, before, "B's holding sits on A's entry")
	reply, err := mergeExport(t, ctx, cloud, "cache-b", withContent(t, exportTestBundle(t, ectx, e, r), p))
	require.NoError(t, err)
	require.Equal(t, 1, reply.Rows.RunsReplaced)
	after, ok := cloud.HolderEntry(key)
	require.True(t, ok)
	require.NotEqual(t, pA, after, "B's holding moved off A's retired entry")
	run, ok := entryRunName(cloud, after)
	require.True(t, ok)
	require.Equal(t, CacheID("cache-b"), run.Cache, "to B's own record")
}
