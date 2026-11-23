package dagql

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/engine/snapshots/config"
)

// secondRunTestStep returns field's result in an engine cache, of value
// text, as a step whose call doesn't declare that it follows from its
// inputs: another run of it could give another result. Every engine's
// result of field has one recipe.
func secondRunTestStep(t *testing.T, ctx context.Context, c *Cache, srv *Server, field, text string) AnyResult {
	t.Helper()
	res := persistedListTestResult(t, ctx, c, srv, field, &transferTestValue{Text: text})
	c.egraphMu.Lock()
	frame := res.cacheSharedResult().loadResultCall().clone()
	frame.FollowsFromInputs = false
	res.cacheSharedResult().storeResultCall(frame)
	c.egraphMu.Unlock()
	return res
}

// secondRunTestExport exports roots from c, with known when it is set,
// merges the bundle into cloud as from, marks the bundle's full rows known
// when the merge committed, and returns the bundle and the reply.
func secondRunTestExport(t *testing.T, ctx context.Context, c *Cache, known *KnownRows, cloud *Cache, cctx context.Context, from CacheID, roots ...AnyResult) (ValueBundle, MergeReply) {
	t.Helper()
	var bundle ValueBundle
	var rows ExportedRows
	require.NoError(t, c.WithExportedValues(ctx, ValueSelection{Roots: roots, Known: known}, config.RefConfig{}, func(_ context.Context, values *ExportedValues) error {
		bundle, rows = values.Bundle, values.Rows
		return nil
	}))
	reply, err := cloud.MergeValues(cctx, from, bundle)
	require.NoError(t, err)
	if known != nil {
		// As the engine's session does with the service's answer.
		if len(reply.Stale) == 0 {
			known.Merged(rows)
		} else {
			var stale []uint64
			for _, sent := range bundle.Values {
				if slices.Contains(reply.Stale, sent.Ordinal) {
					stale = append(stale, sent.SenderNumber)
				}
			}
			known.Stale(rows, stale)
		}
	}
	return bundle, reply
}

// secondRunTestNumber returns the Cloud's number for res's row of the
// bundle the reply answers, or 0 when the reply names none.
func secondRunTestNumber(bundle ValueBundle, reply MergeReply, res AnyResult) uint64 {
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
	return 0
}

// secondRunTestOrdinal returns the ordinal of res's row in the bundle.
func secondRunTestOrdinal(t *testing.T, bundle ValueBundle, res AnyResult) TransferOrdinal {
	t.Helper()
	for _, sent := range bundle.Values {
		if sent.SenderNumber == uint64(res.cacheSharedResult().id) {
			return sent.Ordinal
		}
	}
	require.FailNow(t, "the bundle carries no row of the result")
	return 0
}

// secondRunTestRun names res's run in its engine cache from.
func secondRunTestRun(from CacheID, res AnyResult) RunName {
	shared := res.cacheSharedResult()
	return RunName{Cache: from, Number: uint64(shared.id), Replacements: shared.replacements}
}

// secondRunTestRecipe returns the Cloud's recipe for its entry number.
func secondRunTestRecipe(t *testing.T, cloud *Cache, number uint64) digest.Digest {
	t.Helper()
	recipe, ok := cloud.EntryRecipe(number)
	require.True(t, ok)
	return recipe
}

// secondRunTestFacts returns the Cloud's facts of its entry number.
func secondRunTestFacts(t *testing.T, cloud *Cache, number uint64) EntryFacts {
	t.Helper()
	facts := cloud.EntriesFacts([]uint64{number})
	require.Len(t, facts, 1)
	return facts[0]
}

// secondRunTestText returns the text of the value an engine gets for the
// Cloud's entry number in the stored bundle of root.
func secondRunTestText(t *testing.T, cloud *Cache, cctx context.Context, root, number uint64) string {
	t.Helper()
	sent, err := cloud.StoredBundle(cctx, []uint64{root}, time.Now().Unix(), nil)
	require.NoError(t, err)
	var ordinal TransferOrdinal
	for _, value := range sent.Bundle.Values {
		if value.SenderNumber == number {
			ordinal = value.Ordinal
		}
	}
	require.NotZero(t, ordinal, "the bundle of %d carries %d", root, number)
	ctx, c, srv := transferTestCache(t)
	reply, err := c.MergeValues(ctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	for _, value := range reply.Values {
		if value.Ordinal != ordinal {
			continue
		}
		loaded, err := c.LoadResultByResultID(ctx, "test-session", srv, value.Number)
		require.NoError(t, err)
		return loaded.Unwrap().(*transferTestValue).Text
	}
	require.FailNow(t, "the engine's merge names no row of the entry")
	return ""
}

// secondRunTestSetup merges cache-a's run of the step into a new Cloud, and
// builds cache-b's own run of the same step with a unique result over it.
// cache-b's call row of its step has already reached the Cloud, as it
// usually does before its export: its holding sits on the recipe's current
// entry, cache-a's run.
type secondRunTestSetup struct {
	cctx   context.Context
	cloud  *Cache
	first  uint64
	recipe digest.Digest

	bctx  context.Context
	b     *Cache
	bsrv  *Server
	bStep AnyResult
	bRoot AnyResult
}

func newSecondRunTestSetup(t *testing.T) *secondRunTestSetup {
	t.Helper()
	s := &secondRunTestSetup{}
	s.cctx, s.cloud = storedPartTestCache(t, WithBlobStore())
	actx, a, asrv := transferTestCache(t)
	aStep := secondRunTestStep(t, actx, a, asrv, "second-run-step", "a-run")
	bundle, reply := secondRunTestExport(t, actx, a, nil, s.cloud, s.cctx, "cache-a", aStep)
	s.first = secondRunTestNumber(bundle, reply, aStep)
	require.NotZero(t, s.first)
	s.recipe = secondRunTestRecipe(t, s.cloud, s.first)

	s.bctx, s.b, s.bsrv = transferTestCache(t)
	s.bStep = secondRunTestStep(t, s.bctx, s.b, s.bsrv, "second-run-step", "b-run")
	s.bRoot = secondRunTestStep(t, s.bctx, s.b, s.bsrv, "second-run-unique", "b-root")
	transferTestDependency(s.b, s.bctx, s.bRoot, s.bStep)
	_, err := s.cloud.AttachRemoteHolding(s.cctx, HolderKey{Cache: "cache-b", Number: uint64(s.bStep.cacheSharedResult().id)}, RemoteHolding{Recipe: s.recipe})
	require.NoError(t, err)
	return s
}

// secondRunTestReplace expires the Cloud's entry root and has cache-c
// replace it in place with its own record of the recipe field, which stands
// over nothing: the second runs below root are then under no stored result.
func secondRunTestReplace(t *testing.T, s *secondRunTestSetup, root uint64, field string) {
	t.Helper()
	s.cloud.egraphMu.Lock()
	s.cloud.resultsByID[sharedResultID(root)].expiresAtUnix = time.Now().Unix() - 10
	s.cloud.egraphMu.Unlock()
	cctx, c, csrv := transferTestCache(t)
	other := secondRunTestStep(t, cctx, c, csrv, field, "c-"+field)
	_, again := secondRunTestExport(t, cctx, c, nil, s.cloud, s.cctx, "cache-c", other)
	require.Equal(t, 1, again.Rows.Replaced, "the expired result is replaced in place")
}

// checkKeptApart checks that the Cloud keeps cache-b's run of the step as
// an entry of its own, second, with the unique result root over it.
func (s *secondRunTestSetup) checkKeptApart(t *testing.T, second, root uint64) {
	t.Helper()
	require.NotZero(t, second)
	require.NotEqual(t, s.first, second, "cache-b's run is an entry of its own")
	require.Equal(t, secondRunTestRun("cache-b", s.bStep), runTestRan(t, s.cloud, second).Run, "it names cache-b's run")
	require.Equal(t, CacheID("cache-a"), runTestRan(t, s.cloud, s.first).Run.Cache, "the first run's entry still names cache-a's run")
	require.Equal(t, s.recipe, secondRunTestRecipe(t, s.cloud, second))
	require.Subset(t, s.cloud.EquivalentEntries(s.recipe.String()), []uint64{s.first, second}, "a lookup by the recipe finds both")

	s.cloud.egraphMu.RLock()
	current := s.cloud.currentEntryForRecipeLocked(s.recipe)
	var ofRecipe []uint64
	for _, res := range s.cloud.entriesOfRecipeLocked(s.recipe) {
		ofRecipe = append(ofRecipe, uint64(res.id))
	}
	_, overSecond := s.cloud.resultsByID[sharedResultID(root)].deps[sharedResultID(second)]
	_, overFirst := s.cloud.resultsByID[sharedResultID(root)].deps[sharedResultID(s.first)]
	s.cloud.egraphMu.RUnlock()
	require.NotNil(t, current)
	require.Equal(t, s.first, uint64(current.id), "the first run stays the recipe's current entry")
	require.Equal(t, []uint64{s.first, second}, ofRecipe, "the recipe's entries: its current entry first, then the second run")
	require.True(t, overSecond, "the unique result stands over cache-b's run")
	require.False(t, overFirst, "and not over cache-a's")

	held, ok := s.cloud.HolderEntry(HolderKey{Cache: "cache-b", Number: uint64(s.bStep.cacheSharedResult().id)})
	require.True(t, ok)
	require.Equal(t, second, held, "cache-b's holding of its step sits on its own run's entry")

	facts := secondRunTestFacts(t, s.cloud, second)
	require.True(t, facts.SecondRun)
	require.True(t, facts.KeptUnder, "the unique result keeps it")
	first := secondRunTestFacts(t, s.cloud, s.first)
	require.False(t, first.SecondRun)
	require.False(t, first.KeptUnder)

	require.Equal(t, "b-run", secondRunTestText(t, s.cloud, s.cctx, root, second), "an engine served the unique result gets cache-b's run below it")
}

// When two engines ran one step, the Cloud keeps the second engine's run as
// an entry of its own beside the first, with the unique result the second
// engine built over it standing over its own run: one cache model, in which
// a recipe has one current entry and another run of it serves what was
// built over it. The second engine's holding, which its call row put on the
// first run's entry, moves to its own run's.
func TestASecondRunIsAnEntryOfItsOwnUnderTheResultBuiltOverIt(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	s.checkKeptApart(t, secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, s.bRoot))
}

// A second run is kept apart only under a result nothing else matches
// (Erik's rule, 23 Nov): exported with no such result over it in its
// bundle, cache-b's run of the step folds into the recipe's entry, as any
// copy of a step does, and cache-b's holding sits on that entry.
func TestASecondRunWithNoUniqueResultOverItFoldsIntoTheRecipesEntry(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bStep)
	require.Empty(t, reply.Stale)
	require.Equal(t, s.first, secondRunTestNumber(bundle, reply, s.bStep), "the run folds into the recipe's entry")
	require.False(t, secondRunTestFacts(t, s.cloud, s.first).SecondRun)
	held, ok := s.cloud.HolderEntry(HolderKey{Cache: "cache-b", Number: uint64(s.bStep.cacheSharedResult().id)})
	require.True(t, ok)
	require.Equal(t, s.first, held)
	s.cloud.egraphMu.RLock()
	ofRecipe := s.cloud.entriesOfRecipeLocked(s.recipe)
	s.cloud.egraphMu.RUnlock()
	require.Len(t, ofRecipe, 1, "no second entry of the recipe")
}

// The unique result usually comes in a later export than the second run
// below it, which by then has folded, and which the later export sends as a
// held row. Installed over the recipe's entry, the result would stand over
// another run than its engine's: the Cloud answers the held row stale,
// though cache-b's step hasn't reported running, since cache-b's own record
// of it folded there. Sent again in full with the result over it, the run is
// kept apart.
func TestAUniqueResultInALaterExportAsksAgainForTheRunBelowIt(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	known := NewKnownRows()
	stepBundle, stepReply := secondRunTestExport(t, s.bctx, s.b, known, s.cloud, s.cctx, "cache-b", s.bStep)
	require.Equal(t, s.first, secondRunTestNumber(stepBundle, stepReply, s.bStep), "alone, the run folds")

	bundle, reply := secondRunTestExport(t, s.bctx, s.b, known, s.cloud, s.cctx, "cache-b", s.bRoot)
	ordinal := secondRunTestOrdinal(t, bundle, s.bStep)
	require.True(t, slices.ContainsFunc(bundle.Values, func(v TransferredValue) bool { return v.Ordinal == ordinal && v.Held != "" }), "the later export sends the step as a held row")
	require.Equal(t, []TransferOrdinal{ordinal}, reply.Stale, "the held row is answered stale")
	require.False(t, reply.Committed)

	bundle, reply = secondRunTestExport(t, s.bctx, s.b, known, s.cloud, s.cctx, "cache-b", s.bRoot)
	require.Empty(t, reply.Stale)
	ordinal = secondRunTestOrdinal(t, bundle, s.bStep)
	require.True(t, slices.ContainsFunc(bundle.Values, func(v TransferredValue) bool { return v.Ordinal == ordinal && v.Held == "" }), "sent again, the step goes in full")
	s.checkKeptApart(t, secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, s.bRoot))
}

// A copy of the record the Cloud served an engine is the first run itself:
// it lands on the first run's entry, and a result built over the copy
// stands over the first run.
func TestACopyOfTheServedRunLandsOnItsEntry(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	sent, err := s.cloud.StoredBundle(s.cctx, []uint64{s.first}, time.Now().Unix(), nil)
	require.NoError(t, err)
	cctx, c, csrv := transferTestCache(t)
	creply, err := c.MergeValues(cctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	_, err = s.cloud.ApplyMergedReply(s.cctx, "cache-c", sent.Bundle, creply)
	require.NoError(t, err)
	copied, err := c.LoadResultByResultID(cctx, "test-session", csrv, creply.Imported()[0].ResultID)
	require.NoError(t, err)
	root := secondRunTestStep(t, cctx, c, csrv, "second-run-copy-root", "c-root")
	transferTestDependency(c, cctx, root, copied)

	bundle, reply := secondRunTestExport(t, cctx, c, nil, s.cloud, s.cctx, "cache-c", root)
	require.Equal(t, s.first, secondRunTestNumber(bundle, reply, copied), "the copy is the first run")
	number := secondRunTestNumber(bundle, reply, root)
	s.cloud.egraphMu.RLock()
	_, over := s.cloud.resultsByID[sharedResultID(number)].deps[sharedResultID(s.first)]
	s.cloud.egraphMu.RUnlock()
	require.True(t, over)
	require.False(t, secondRunTestFacts(t, s.cloud, s.first).SecondRun)
}

// A step that follows from its inputs gives the same result over the same
// input entries, so another engine's run of it over those inputs lands on
// the first run's entry. Over another run of an input it is another value,
// kept apart as that input is.
func TestAStepThatFollowsFromItsInputsIsKeptApartOnlyOverAnotherRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	actx, a, asrv := transferTestCache(t)
	aStep := secondRunTestStep(t, actx, a, asrv, "second-run-step", "a-run")
	aCopy := persistedListTestResult(t, actx, a, asrv, "second-run-copy", &transferTestValue{Text: "a-copy"})
	transferTestDependency(a, actx, aCopy, aStep)
	aBundle, aReply := secondRunTestExport(t, actx, a, nil, cloud, ctx, "cache-a", aCopy)
	firstCopy := secondRunTestNumber(aBundle, aReply, aCopy)

	bctx, b, bsrv := transferTestCache(t)
	bStep := secondRunTestStep(t, bctx, b, bsrv, "second-run-step", "b-run")
	bCopy := persistedListTestResult(t, bctx, b, bsrv, "second-run-copy", &transferTestValue{Text: "b-copy"})
	transferTestDependency(b, bctx, bCopy, bStep)
	bRoot := secondRunTestStep(t, bctx, b, bsrv, "second-run-unique", "b-root")
	transferTestDependency(b, bctx, bRoot, bCopy)
	bundle, reply := secondRunTestExport(t, bctx, b, nil, cloud, ctx, "cache-b", bRoot)
	secondCopy := secondRunTestNumber(bundle, reply, bCopy)
	require.NotEqual(t, firstCopy, secondCopy, "over another run of its input, the copy is another value")
	require.True(t, secondRunTestFacts(t, cloud, secondCopy).KeptUnder)
	require.True(t, secondRunTestFacts(t, cloud, secondRunTestNumber(bundle, reply, bStep)).KeptUnder, "kept through the copy over it")

	// cache-c took cache-a's run of the step, and copied it itself.
	sent, err := cloud.StoredBundle(ctx, []uint64{secondRunTestNumber(aBundle, aReply, aStep)}, time.Now().Unix(), nil)
	require.NoError(t, err)
	cctx, c, csrv := transferTestCache(t)
	creply, err := c.MergeValues(cctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	_, err = cloud.ApplyMergedReply(ctx, "cache-c", sent.Bundle, creply)
	require.NoError(t, err)
	taken, err := c.LoadResultByResultID(cctx, "test-session", csrv, creply.Imported()[0].ResultID)
	require.NoError(t, err)
	cCopy := persistedListTestResult(t, cctx, c, csrv, "second-run-copy", &transferTestValue{Text: "a-copy"})
	transferTestDependency(c, cctx, cCopy, taken)
	cBundle, cReply := secondRunTestExport(t, cctx, c, nil, cloud, ctx, "cache-c", cCopy)
	require.Equal(t, firstCopy, secondRunTestNumber(cBundle, cReply, cCopy), "over the same input entry, the copy is the first run's value")
}

// A result is never installed over an entry that isn't its engine's run of
// the step below it. An engine that took the first run and then ran the
// step itself holds its own run under the first run's entry; a result it
// builds over that, whose export sends the step as a held row, is refused
// as stale, so the engine sends the step in full.
func TestAResultOverAnotherRunsEntryIsRefusedAsStale(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	sent, err := s.cloud.StoredBundle(s.cctx, []uint64{s.first}, time.Now().Unix(), nil)
	require.NoError(t, err)
	cctx, c, csrv := transferTestCache(t)
	creply, err := c.MergeValues(cctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	_, err = s.cloud.ApplyMergedReply(s.cctx, "cache-c", sent.Bundle, creply)
	require.NoError(t, err)
	known := NewKnownRows()
	rows := ExportedRows{Full: map[uint64]RowRevision{}}
	for _, value := range creply.Values {
		rows.Full[value.Number] = value.Revision
	}
	known.Merged(rows)
	taken, err := c.LoadResultByResultID(cctx, "test-session", csrv, creply.Imported()[0].ResultID)
	require.NoError(t, err)
	// cache-c ran the step itself, for want of the first run's content.
	key := HolderKey{Cache: "cache-c", Number: uint64(taken.cacheSharedResult().id)}
	runTestEvaluated(t, s.cctx, s.cloud, key, taken.cacheSharedResult().replacements, true)

	root := secondRunTestStep(t, cctx, c, csrv, "second-run-own-root", "c-root")
	transferTestDependency(c, cctx, root, taken)
	bundle, reply := secondRunTestExport(t, cctx, c, known, s.cloud, s.cctx, "cache-c", root)
	ordinal := secondRunTestOrdinal(t, bundle, taken)
	require.True(t, slices.ContainsFunc(bundle.Values, func(v TransferredValue) bool { return v.Ordinal == ordinal && v.Held != "" }))
	require.Equal(t, []TransferOrdinal{ordinal}, reply.Stale, "the step's held row is another run's entry")
	require.Empty(t, reply.Values)
}

// When an engine ends, the Cloud drops each of its second runs that nothing
// stored stands over, down a chain of them, as a prune drops a value that
// only holdings keep. A second run is kept only under a result nothing else
// matches, so it comes to that when the result over it goes: here cache-c
// replaces it in place on expiry. A second run a recipe's current entry
// stands over stays, and so does every current entry. A second run has no
// retention of its own.
func TestAnEnginesEndReleasesItsSecondRunsThatNothingStoredStandsOver(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	second := secondRunTestNumber(bundle, reply, s.bStep)
	root := secondRunTestNumber(bundle, reply, s.bRoot)

	// cache-a also ran a step over its run; cache-b's run of it, over
	// cache-b's own run of a third step, is a chain of second runs with
	// nothing stored over it.
	actx, a, asrv := transferTestCache(t)
	aBase := secondRunTestStep(t, actx, a, asrv, "second-run-base", "a-base")
	aOver := secondRunTestStep(t, actx, a, asrv, "second-run-over", "a-over")
	transferTestDependency(a, actx, aOver, aBase)
	aBundle, aReply := secondRunTestExport(t, actx, a, nil, s.cloud, s.cctx, "cache-a", aOver)
	firstOver, firstBase := secondRunTestNumber(aBundle, aReply, aOver), secondRunTestNumber(aBundle, aReply, aBase)
	bBase := secondRunTestStep(t, s.bctx, s.b, s.bsrv, "second-run-base", "b-base")
	bOver := secondRunTestStep(t, s.bctx, s.b, s.bsrv, "second-run-over", "b-over")
	bTop := secondRunTestStep(t, s.bctx, s.b, s.bsrv, "second-run-chain-unique", "b-top")
	transferTestDependency(s.b, s.bctx, bOver, bBase)
	transferTestDependency(s.b, s.bctx, bTop, bOver)
	chainBundle, chainReply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", bTop)
	secondOver, secondBase := secondRunTestNumber(chainBundle, chainReply, bOver), secondRunTestNumber(chainBundle, chainReply, bBase)
	require.NotEqual(t, firstOver, secondOver)
	require.NotEqual(t, firstBase, secondBase)
	secondRunTestReplace(t, s, secondRunTestNumber(chainBundle, chainReply, bTop), "second-run-chain-unique")
	s.cloud.egraphMu.RLock()
	_, secondRetained := s.cloud.persistedEdgesByResult[sharedResultID(secondOver)]
	_, firstRetained := s.cloud.persistedEdgesByResult[sharedResultID(firstOver)]
	s.cloud.egraphMu.RUnlock()
	require.False(t, secondRetained, "a second run has no retention of its own")
	require.True(t, firstRetained)

	released, _, err := s.cloud.ReleaseSecondRuns(s.cctx, "cache-b", func(CacheID) bool { return false })
	require.NoError(t, err)
	require.Equal(t, 2, released.Dropped, "the chain of two second runs")
	require.Positive(t, released.Held, "the release says how long it held the cache")
	for _, number := range []uint64{secondOver, secondBase} {
		_, ok := s.cloud.EntryRun(number)
		require.False(t, ok, "entry %d's value is dropped", number)
	}
	for _, number := range []uint64{second, root, s.first, firstOver, firstBase} {
		_, ok := s.cloud.EntryRun(number)
		require.True(t, ok, "entry %d keeps its value", number)
	}
	require.Equal(t, "b-run", secondRunTestText(t, s.cloud, s.cctx, root, second))
}

// A second run whose engine is still running isn't released by another
// engine's end, even when the release reaches it.
func TestAnEnginesEndLeavesALiveEnginesSecondRuns(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	second := secondRunTestNumber(bundle, reply, s.bStep)
	secondRunTestReplace(t, s, secondRunTestNumber(bundle, reply, s.bRoot), "second-run-unique")
	_, _, err := s.cloud.ReleaseSecondRuns(s.cctx, "cache-a", func(id CacheID) bool { return id == "cache-b" })
	require.NoError(t, err)
	_, ok := s.cloud.EntryRun(second)
	require.True(t, ok, "cache-b still runs")
	_, _, err = s.cloud.ReleaseSecondRuns(s.cctx, "cache-b", func(CacheID) bool { return false })
	require.NoError(t, err)
	_, ok = s.cloud.EntryRun(second)
	require.False(t, ok, "released at cache-b's end")
}

// A holding the Cloud learns of other than by its engine's call row, such as
// a set file's handover, names the Cloud entry its engine got: a second
// run's holding sits on the second run, not on its recipe's current entry,
// so that what the engine builds over it later lands over the run it holds.
func TestAHoldingThatNamesASecondRunSitsOnIt(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	second := secondRunTestNumber(bundle, reply, s.bStep)
	key := HolderKey{Cache: "cache-c", Number: 7}
	_, err := s.cloud.AttachRemoteHolding(s.cctx, key, RemoteHolding{Recipe: secondRunTestRecipe(t, s.cloud, second), Entry: second})
	require.NoError(t, err)
	held, ok := s.cloud.HolderEntry(key)
	require.True(t, ok)
	require.Equal(t, second, held)

	// One that names an entry of another recipe sits on its own recipe's.
	other := HolderKey{Cache: "cache-c", Number: 8}
	_, err = s.cloud.AttachRemoteHolding(s.cctx, other, RemoteHolding{Recipe: s.recipe, Entry: secondRunTestNumber(bundle, reply, s.bRoot)})
	require.NoError(t, err)
	held, ok = s.cloud.HolderEntry(other)
	require.True(t, ok)
	require.Equal(t, s.first, held)
}

// A restored Cloud keeps a second run as one, beside its recipe's current
// entry and kept under the result over it, though nothing saved says so: on
// the Cloud, an entry that stores a record that no recipe index names is a
// second run of its own recipe.
func TestARestoredCloudKeepsASecondRunAsOne(t *testing.T) {
	t.Parallel()
	for _, compress := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "compressed"}[compress], func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "cloud.db")
			s := newSecondRunTestSetup(t)
			s.cctx, s.cloud = storedPersistenceTestCloud(t, path)
			actx, a, asrv := transferTestCache(t)
			aStep := secondRunTestStep(t, actx, a, asrv, "second-run-step", "a-run")
			aBundle, aReply := secondRunTestExport(t, actx, a, nil, s.cloud, s.cctx, "cache-a", aStep)
			s.first = secondRunTestNumber(aBundle, aReply, aStep)
			bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
			second, root := secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, s.bRoot)
			require.True(t, secondRunTestFacts(t, s.cloud, second).KeptUnder)
			if compress {
				storedTestCompress(s.cloud)
			}
			require.NoError(t, s.cloud.Close(s.cctx))

			s.cctx, s.cloud = storedPersistenceTestCloud(t, path)
			defer func() { require.NoError(t, s.cloud.Close(s.cctx)) }()
			facts := secondRunTestFacts(t, s.cloud, second)
			require.True(t, facts.SecondRun)
			require.True(t, facts.KeptUnder)
			require.False(t, secondRunTestFacts(t, s.cloud, s.first).SecondRun)
			require.Equal(t, s.recipe, secondRunTestRecipe(t, s.cloud, second))
			s.cloud.egraphMu.RLock()
			current := s.cloud.currentEntryForRecipeLocked(s.recipe)
			s.cloud.egraphMu.RUnlock()
			require.Equal(t, s.first, uint64(current.id))
			require.Equal(t, "b-run", secondRunTestText(t, s.cloud, s.cctx, root, second))
		})
	}
}

// A third engine that was served a second run and then ran the step itself,
// for want of its content, has a run of its own: a result it builds over its
// run stands over a new entry of its run, never over the second run it was
// served, and what was built over that second run stays over it.
func TestARebuildOfAServedSecondRunIsARunOfItsOwn(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	second, root := secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, s.bRoot)

	sent, err := s.cloud.StoredBundle(s.cctx, []uint64{root}, time.Now().Unix(), nil)
	require.NoError(t, err)
	cctx, c, csrv := transferTestCache(t)
	creply, err := c.MergeValues(cctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	_, err = s.cloud.ApplyMergedReply(s.cctx, "cache-c", sent.Bundle, creply)
	require.NoError(t, err)
	var taken AnyResult
	for _, value := range sent.Bundle.Values {
		if value.SenderNumber != second {
			continue
		}
		for _, merged := range creply.Values {
			if merged.Ordinal == value.Ordinal {
				taken, err = c.LoadResultByResultID(cctx, "test-session", csrv, merged.Number)
				require.NoError(t, err)
			}
		}
	}
	require.NotNil(t, taken, "cache-c was served cache-b's run")
	key := HolderKey{Cache: "cache-c", Number: uint64(taken.cacheSharedResult().id)}
	held, ok := s.cloud.HolderEntry(key)
	require.True(t, ok)
	require.Equal(t, second, held, "cache-c's holding sits on the run it was served")
	// cache-c ran the step itself, for want of the second run's content.
	runTestEvaluated(t, s.cctx, s.cloud, key, taken.cacheSharedResult().replacements, true)

	cRoot := secondRunTestStep(t, cctx, c, csrv, "second-run-c-root", "c-root")
	transferTestDependency(c, cctx, cRoot, taken)
	cBundle, cReply := secondRunTestExport(t, cctx, c, nil, s.cloud, s.cctx, "cache-c", cRoot)
	require.Empty(t, cReply.Stale)
	third := secondRunTestNumber(cBundle, cReply, taken)
	require.NotEqual(t, second, third, "cache-c's run is an entry of its own")
	require.NotEqual(t, s.first, third)
	require.Equal(t, RunName{Cache: "cache-c", Number: key.Number, Replacements: taken.cacheSharedResult().replacements}, runTestRan(t, s.cloud, third).Run)
	s.cloud.egraphMu.RLock()
	_, cOverThird := s.cloud.resultsByID[sharedResultID(secondRunTestNumber(cBundle, cReply, cRoot))].deps[sharedResultID(third)]
	_, bOverSecond := s.cloud.resultsByID[sharedResultID(root)].deps[sharedResultID(second)]
	s.cloud.egraphMu.RUnlock()
	require.True(t, cOverThird, "cache-c's result stands over cache-c's run")
	require.True(t, bOverSecond, "cache-b's result still stands over cache-b's run")
	require.Equal(t, secondRunTestRun("cache-b", s.bStep), runTestRan(t, s.cloud, second).Run)
}

// A second run exported as a root, beside the unique result over it, keeps
// no retention of its own, and exported again it keeps none either: the
// export asks for its recipe, whose current entry stores it. Kept under the
// unique result, it outlives its engine's end.
func TestASecondRunsRootExportedAgainKeepsNoRetention(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	known := NewKnownRows()
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, known, s.cloud, s.cctx, "cache-b", s.bRoot, s.bStep)
	second := secondRunTestNumber(bundle, reply, s.bStep)
	require.NotEqual(t, s.first, second)
	for _, k := range []*KnownRows{nil, known} {
		again, reply := secondRunTestExport(t, s.bctx, s.b, k, s.cloud, s.cctx, "cache-b", s.bStep)
		require.Empty(t, reply.Stale)
		if n := secondRunTestNumber(again, reply, s.bStep); n != 0 {
			require.Equal(t, second, n, "it lands on its own run's entry again")
		}
	}
	s.cloud.egraphMu.RLock()
	_, retained := s.cloud.persistedEdgesByResult[sharedResultID(second)]
	_, firstRetained := s.cloud.persistedEdgesByResult[sharedResultID(s.first)]
	s.cloud.egraphMu.RUnlock()
	require.False(t, retained, "the second run has no retention of its own")
	require.True(t, firstRetained)

	_, _, err := s.cloud.ReleaseSecondRuns(s.cctx, "cache-b", func(CacheID) bool { return false })
	require.NoError(t, err)
	_, ok := s.cloud.EntryRun(second)
	require.True(t, ok, "the unique result over it keeps it past cache-b's end")
}

// An engine's record of a step goes to the Cloud before the step runs, as
// the records stream sends a lazy chain, and the Cloud keeps it as the
// engine's own run, under the result the engine built over it. Then the
// engine's lookup of the step is answered with the first run: its merge
// keeps its own lazy value, with the first run's offers, and it installs
// that content rather than running the step. What it builds over the step
// is built over that content: the entry is marked as having run, so that
// selection asks that engine for the content it holds.
func TestASecondRunWhoseEngineInstalledOtherContentHasRun(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	stored, _, err := s.cloud.SetStoredPart(s.cctx, s.first, testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)

	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	second, root := secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, s.bRoot)
	require.False(t, runTestRan(t, s.cloud, second).Ran, "cache-b's step hasn't run")

	// cache-b's lookup of the step is answered with the first run.
	sent, err := s.cloud.StoredBundle(s.cctx, []uint64{s.first}, time.Now().Unix(), nil)
	require.NoError(t, err)
	breply, err := s.b.MergeValues(s.bctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	_, err = s.cloud.ApplyMergedReply(s.cctx, "cache-b", sent.Bundle, breply)
	require.NoError(t, err)
	bStep := s.bStep.cacheSharedResult()
	key := HolderKey{Cache: "cache-b", Number: uint64(bStep.id)}
	held, ok := s.cloud.HolderEntry(key)
	require.True(t, ok)
	require.Equal(t, second, held, "cache-b's holding stays on its own entry")
	// cache-b installs the first run's offered content, running nothing.
	runTestEvaluated(t, s.cctx, s.cloud, key, bStep.replacements, false)
	require.True(t, runTestRan(t, s.cloud, second).Ran, "cache-b's entry has the content cache-b installed")
	require.NotZero(t, root)
}

// The same, with the engine's report of the installed content reaching the
// Cloud before its pending export does, as telemetry rows can apply while an
// export is on its way: the merge that keeps the run apart recovers what the
// engine's holding already reported.
func TestASecondRunWhoseEngineInstalledOtherContentBeforeItsExportHasRun(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	aRun := runTestRan(t, s.cloud, s.first).Run
	stored, _, err := s.cloud.SetStoredPart(s.cctx, s.first, testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)

	// cache-b's pending export, captured before anything else.
	pending := exportTestBundle(t, s.bctx, s.b, s.bRoot)

	// cache-b's lookup of the step is answered with the first run, and it
	// installs the first run's offered content, while its holding, which
	// its call row put there, still sits on the first run's entry.
	sent, err := s.cloud.StoredBundle(s.cctx, []uint64{s.first}, time.Now().Unix(), nil)
	require.NoError(t, err)
	breply, err := s.b.MergeValues(s.bctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	_, err = s.cloud.ApplyMergedReply(s.cctx, "cache-b", sent.Bundle, breply)
	require.NoError(t, err)
	bStep := s.bStep.cacheSharedResult()
	key := HolderKey{Cache: "cache-b", Number: uint64(bStep.id)}
	held, ok := s.cloud.HolderEntry(key)
	require.True(t, ok)
	require.Equal(t, s.first, held)
	runTestEvaluated(t, s.cctx, s.cloud, key, bStep.replacements, false)
	require.Equal(t, aRun, runTestRan(t, s.cloud, s.first).Run, "the first run's entry is still the first run's")

	reply, err := s.cloud.MergeValues(s.cctx, "cache-b", pending)
	require.NoError(t, err)
	second, root := secondRunTestNumber(pending, reply, s.bStep), secondRunTestNumber(pending, reply, s.bRoot)
	require.NotEqual(t, s.first, second)
	require.True(t, runTestRan(t, s.cloud, second).Ran, "the run kept apart has the content its engine reported")
	require.NotZero(t, root)
}

// A second run is kept under a stored current entry only while one stands
// over it. When the result over it is replaced in place on expiry, by a
// record that isn't over it, as the Cloud replaces an expired value, the
// mark goes with the edge: selection no longer asks the run's content, and
// its engine's end releases it.
func TestAKeptUnderMarkGoesWithItsLastStoredParentsEdge(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	second, root := secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, s.bRoot)
	require.True(t, secondRunTestFacts(t, s.cloud, second).KeptUnder)

	s.cloud.egraphMu.Lock()
	s.cloud.resultsByID[sharedResultID(root)].expiresAtUnix = time.Now().Unix() - 10
	s.cloud.egraphMu.Unlock()
	cctx, c, csrv := transferTestCache(t)
	other := secondRunTestStep(t, cctx, c, csrv, "second-run-unique", "c-root")
	_, again := secondRunTestExport(t, cctx, c, nil, s.cloud, s.cctx, "cache-c", other)
	require.Equal(t, 1, again.Rows.Replaced, "the expired result is replaced in place")
	require.False(t, secondRunTestFacts(t, s.cloud, second).KeptUnder, "nothing stored stands over the second run")

	_, _, err := s.cloud.ReleaseSecondRuns(s.cctx, "cache-b", func(CacheID) bool { return false })
	require.NoError(t, err)
	_, ok := s.cloud.EntryRun(second)
	require.False(t, ok, "released at cache-b's end")
}

// A result that comes in a later export, over a copy of a step that folded
// with the step below it, meets the copy as a held row whose own step
// follows from its inputs: it is another value all the same, over the
// engine's own run of the step below. The Cloud asks for the copy again,
// then for the step under it, and keeps both apart, the result over them.
func TestALaterUniqueResultOverAFoldedCopyAsksForTheRunsBelowIt(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	actx, a, asrv := transferTestCache(t)
	aStep := secondRunTestStep(t, actx, a, asrv, "second-run-step", "a-run")
	aCopy := persistedListTestResult(t, actx, a, asrv, "second-run-copy", &transferTestValue{Text: "a-copy"})
	transferTestDependency(a, actx, aCopy, aStep)
	aBundle, aReply := secondRunTestExport(t, actx, a, nil, cloud, ctx, "cache-a", aCopy)
	firstStep, firstCopy := secondRunTestNumber(aBundle, aReply, aStep), secondRunTestNumber(aBundle, aReply, aCopy)

	bctx, b, bsrv := transferTestCache(t)
	bStep := secondRunTestStep(t, bctx, b, bsrv, "second-run-step", "b-run")
	bCopy := persistedListTestResult(t, bctx, b, bsrv, "second-run-copy", &transferTestValue{Text: "b-copy"})
	transferTestDependency(b, bctx, bCopy, bStep)
	bRoot := secondRunTestStep(t, bctx, b, bsrv, "second-run-unique", "b-root")
	transferTestDependency(b, bctx, bRoot, bCopy)
	known := NewKnownRows()
	copyBundle, copyReply := secondRunTestExport(t, bctx, b, known, cloud, ctx, "cache-b", bCopy)
	require.Equal(t, firstCopy, secondRunTestNumber(copyBundle, copyReply, bCopy), "alone, the copy folds")
	require.Equal(t, firstStep, secondRunTestNumber(copyBundle, copyReply, bStep), "and so does the step")

	var bundle ValueBundle
	var reply MergeReply
	asked := 0
	for range 4 {
		bundle, reply = secondRunTestExport(t, bctx, b, known, cloud, ctx, "cache-b", bRoot)
		if len(reply.Stale) == 0 {
			break
		}
		require.False(t, reply.Committed)
		asked++
	}
	require.Empty(t, reply.Stale, "asked again, the runs below come in full")
	require.Positive(t, asked, "the result isn't installed over the first run's copy")
	secondStep, secondCopy, root := secondRunTestNumber(bundle, reply, bStep), secondRunTestNumber(bundle, reply, bCopy), secondRunTestNumber(bundle, reply, bRoot)
	require.NotEqual(t, firstStep, secondStep)
	require.NotEqual(t, firstCopy, secondCopy)
	cloud.egraphMu.RLock()
	_, rootOverCopy := cloud.resultsByID[sharedResultID(root)].deps[sharedResultID(secondCopy)]
	_, copyOverStep := cloud.resultsByID[sharedResultID(secondCopy)].deps[sharedResultID(secondStep)]
	cloud.egraphMu.RUnlock()
	require.True(t, rootOverCopy, "the result stands over cache-b's copy")
	require.True(t, copyOverStep, "which stands over cache-b's run of the step")
}

// A bundle can carry two records of one recipe: an expired P that a live R
// depends on, and the current P', which comes after R in the bundle's
// order. The recipe is decided by its best record, P', kept apart as
// another run under the result U over R; R, which follows from its inputs,
// is then another value too, and U stands over it, whatever order the rows
// came in.
func TestARowOverARecipeDecidedByALaterRecordFollowsThatDecision(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	actx, a, asrv := transferTestCache(t)
	aP := secondRunTestStep(t, actx, a, asrv, "two-values", "a value")
	aR := persistedListTestResult(t, actx, a, asrv, "two-values-parent", DynamicResultArrayOutput{Elem: &transferTestValue{}, Values: []AnyResult{aP}})
	aBundle, aReply := secondRunTestExport(t, actx, a, nil, cloud, ctx, "cache-a", aR)
	firstR := secondRunTestNumber(aBundle, aReply, aR)
	require.NotZero(t, firstR)

	bctx, b, bsrv := transferTestCache(t)
	old := secondRunTestStep(t, bctx, b, bsrv, "two-values", "old value")
	r := persistedListTestResult(t, bctx, b, bsrv, "two-values-parent", DynamicResultArrayOutput{Elem: &transferTestValue{}, Values: []AnyResult{old}})
	u := secondRunTestStep(t, bctx, b, bsrv, "two-values-unique", "b-root")
	transferTestDependency(b, bctx, u, r)
	mergeTestExpire(b, old.cacheSharedResult())
	frame := old.cacheSharedResult().loadResultCall().clone()
	fresh, err := b.GetOrInitCall(bctx, "fresh-session", bsrv, &CallRequest{ResultCall: frame, IsPersistable: true}, func(context.Context) (AnyResult, error) {
		return NewResultForCall(&transferTestValue{Text: "fresh value"}, frame)
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, b.ReleaseSession(bctx, "fresh-session")) })
	bundle := cloudTestBundle(t, bctx, b, u, fresh)
	require.Len(t, bundle.Values, 4)
	require.Equal(t, mergeTestOrdinalOf(t, bundle, fresh.cacheSharedResult()), bundle.Values[3].Ordinal, "P' comes after R")

	reply, err := cloud.MergeValues(ctx, "cache-b", bundle)
	require.NoError(t, err)
	require.Empty(t, reply.Stale)
	secondP := mergeTestValueOf(t, reply, mergeTestOrdinalOf(t, bundle, fresh.cacheSharedResult())).Number
	secondR := mergeTestValueOf(t, reply, mergeTestOrdinalOf(t, bundle, r.cacheSharedResult())).Number
	root := mergeTestValueOf(t, reply, mergeTestOrdinalOf(t, bundle, u.cacheSharedResult())).Number
	require.True(t, secondRunTestFacts(t, cloud, secondP).SecondRun, "P' is kept apart")
	require.NotEqual(t, firstR, secondR, "R over it is another value")
	cloud.egraphMu.RLock()
	_, rootOverR := cloud.resultsByID[sharedResultID(root)].deps[sharedResultID(secondR)]
	cloud.egraphMu.RUnlock()
	require.True(t, rootOverR, "U stands over cache-b's R")
}

// A bundle carries one run of a step. Asked for the first run of a step and
// a result over its second run, it carries the first, which its roots reach
// first, and leaves the result out: an engine keeps one entry a recipe, and
// sent both it would refuse the bundle, or land the second run on the first.
// The cache's listing and pricing of the same roots agree. Asked for the
// result alone, the bundle carries it over its own run, as before.
func TestABundleCarriesOneRunOfAStep(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	second, root := secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, s.bRoot)
	s.checkKeptApart(t, second, root)
	now := time.Now().Unix()

	both, err := s.cloud.StoredBundle(s.cctx, []uint64{s.first, root}, now, nil)
	require.NoError(t, err)
	require.Equal(t, []uint64{root}, both.OtherRun, "the result over the second run is left out")
	var numbers []uint64
	for _, value := range both.Bundle.Values {
		numbers = append(numbers, value.SenderNumber)
	}
	require.Contains(t, numbers, s.first)
	require.NotContains(t, numbers, second, "one run of the step")
	require.NotContains(t, numbers, root)

	listed, err := s.cloud.StoredClosure(s.cctx, []uint64{s.first, root}, now, nil)
	require.NoError(t, err)
	for _, entry := range listed.Entries {
		require.NotContains(t, []uint64{second, root}, entry.Number, "the listing leaves out what the bundle does")
	}
	sizes, err := s.cloud.StoredSizes(s.cctx, []uint64{s.first, root}, now, nil)
	require.NoError(t, err)
	require.Positive(t, sizes[0].Records)
	require.Zero(t, sizes[1].Records, "the pricing carries nothing of it")

	alone, err := s.cloud.StoredBundle(s.cctx, []uint64{root}, now, nil)
	require.NoError(t, err)
	require.Empty(t, alone.OtherRun)
	numbers = numbers[:0]
	for _, value := range alone.Bundle.Values {
		numbers = append(numbers, value.SenderNumber)
	}
	require.Contains(t, numbers, second, "alone, the result goes over its own run")
}

// A bundle for an engine that holds one run of a step carries that run of
// it, and leaves out a result over another run: the engine would land the
// other run's record on its own entry, and the result would stand over its
// own run. To it the result is a miss, which it runs over its own run.
func TestABundleForAnEngineHoldingARunLeavesOutResultsOverAnother(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	second, root := secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, s.bRoot)
	s.checkKeptApart(t, second, root)
	_, err := s.cloud.AttachRemoteHolding(s.cctx, HolderKey{Cache: "cache-c", Number: 7}, RemoteHolding{Recipe: s.recipe})
	require.NoError(t, err)
	held, ok := s.cloud.HolderEntry(HolderKey{Cache: "cache-c", Number: 7})
	require.True(t, ok)
	require.Equal(t, s.first, held, "cache-c holds the first run")

	sent, err := s.cloud.StoredBundle(s.cctx, []uint64{root}, time.Now().Unix(), &HeldBy{Cache: "cache-c"})
	require.NoError(t, err)
	require.Equal(t, []uint64{root}, sent.OtherRun)
	require.Empty(t, sent.Bundle.Values)

	other, err := s.cloud.StoredBundle(s.cctx, []uint64{root}, time.Now().Unix(), &HeldBy{Cache: "cache-d"})
	require.NoError(t, err)
	require.Empty(t, other.OtherRun, "an engine holding neither run is sent the result over its run")
}

// A lookup's roots go in full whatever the engine holds of them (HeldBy.Not),
// but the run of a step an engine holds is still the one it holds: a bundle
// asked for a result over the second run and for the first run, which the
// recipient holds and is asked for in full, carries the first run and leaves
// the result out.
func TestABundleCarriesTheRunItsRecipientHoldsThoughItGoesInFull(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot)
	second, root := secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, s.bRoot)
	s.checkKeptApart(t, second, root)
	_, err := s.cloud.AttachRemoteHolding(s.cctx, HolderKey{Cache: "cache-c", Number: 7}, RemoteHolding{Recipe: s.recipe})
	require.NoError(t, err)

	sent, err := s.cloud.StoredBundle(s.cctx, []uint64{root, s.first}, time.Now().Unix(), &HeldBy{Cache: "cache-c", Not: map[uint64]bool{s.first: true}})
	require.NoError(t, err)
	require.Equal(t, []uint64{root}, sent.OtherRun, "the result over the run cache-c doesn't hold is left out")
	var numbers []uint64
	for _, value := range sent.Bundle.Values {
		require.Empty(t, value.Held)
		numbers = append(numbers, value.SenderNumber)
	}
	require.Equal(t, []uint64{s.first}, numbers, "the run cache-c holds goes, in full")
}

// A held row a recipient doesn't hold whole goes in full (HeldBy.Whole), and
// what that walk exposes counts toward the run a bundle carries: a bundle of
// a result H over a step's second run, which the recipient holds but not
// whole, and of a result U over the step's first run carries H over its run
// and leaves U out, never both runs.
func TestABundleCountsTheRunsAWholeHoldingExposes(t *testing.T) {
	t.Parallel()
	cctx, cloud := storedPartTestCache(t, WithBlobStore())
	actx, a, asrv := transferTestCache(t)
	aStep := secondRunTestStep(t, actx, a, asrv, "second-run-step", "a-run")
	aRoot := secondRunTestStep(t, actx, a, asrv, "second-run-a-root", "a-root")
	transferTestDependency(a, actx, aRoot, aStep)
	aBundle, aReply := secondRunTestExport(t, actx, a, nil, cloud, cctx, "cache-a", aRoot)
	first, u := secondRunTestNumber(aBundle, aReply, aStep), secondRunTestNumber(aBundle, aReply, aRoot)
	recipe := secondRunTestRecipe(t, cloud, first)

	bctx, b, bsrv := transferTestCache(t)
	bStep := secondRunTestStep(t, bctx, b, bsrv, "second-run-step", "b-run")
	bRoot := secondRunTestStep(t, bctx, b, bsrv, "second-run-unique", "b-root")
	transferTestDependency(b, bctx, bRoot, bStep)
	_, err := cloud.AttachRemoteHolding(cctx, HolderKey{Cache: "cache-b", Number: uint64(bStep.cacheSharedResult().id)}, RemoteHolding{Recipe: recipe})
	require.NoError(t, err)
	bBundle, bReply := secondRunTestExport(t, bctx, b, nil, cloud, cctx, "cache-b", bRoot)
	second, h := secondRunTestNumber(bBundle, bReply, bStep), secondRunTestNumber(bBundle, bReply, bRoot)
	require.NotEqual(t, first, second, "cache-b's run is kept apart")

	// cache-c holds H, with a dependency it holds nothing of: not whole.
	heldTestHoldOver(t, cloud, "cache-c", 7, h, []uint64{99})
	sent, err := cloud.StoredBundle(cctx, []uint64{h, u}, time.Now().Unix(), &HeldBy{Cache: "cache-c", Whole: true})
	require.NoError(t, err)
	require.Equal(t, []uint64{h}, sent.NotWhole)
	require.Equal(t, []uint64{u}, sent.OtherRun, "U over the other run is left out")
	var numbers []uint64
	for _, value := range sent.Bundle.Values {
		numbers = append(numbers, value.SenderNumber)
	}
	require.Contains(t, numbers, second)
	require.NotContains(t, numbers, first, "one run of the step")
}

// A kept call's inputs are carried as roots of a bundle (its call roots), and
// count toward the run of a step it carries: a bundle of a step's first run
// and of an entry K with a kept call on a receiver A built over the step's
// second run carries the first run and leaves K out, never both runs.
func TestABundleCountsTheRunsAKeptCallsInputsCarry(t *testing.T) {
	t.Parallel()
	cloudCtx, cloud := storedPartTestCache(t, WithBlobStore())
	xctx, x, xsrv := transferTestCache(t)
	xStep := secondRunTestStep(t, xctx, x, xsrv, "second-run-step", "x-run")
	xBundle, xReply := secondRunTestExport(t, xctx, x, nil, cloud, cloudCtx, "cache-x", xStep)
	first := secondRunTestNumber(xBundle, xReply, xStep)

	ctx, producer, srv := transferTestCache(t)
	test := &receiverCallTest{callRecordTest: &callRecordTest{ctx: ctx, producer: producer, frame: receiverTestCall("a"), cloud: cloud}, srv: srv}
	test.inner = persistedListTestResult(t, ctx, producer, srv, "inner", &transferTestValue{Text: "inner"})
	aStep := secondRunTestStep(t, ctx, producer, srv, "second-run-step", "a-run")
	receiver, err := producer.GetOrInitCall(ctx, "test-session", srv, &CallRequest{ResultCall: receiverTestFrame("a"), IsPersistable: true},
		func(context.Context) (AnyResult, error) {
			return NewResultForCall(&transferTestValue{Text: "receiver"}, receiverTestFrame("a"))
		})
	require.NoError(t, err)
	transferTestDependency(producer, ctx, receiver, aStep)
	_, err = producer.GetOrInitCall(ctx, "test-session", srv, &CallRequest{ResultCall: test.frame, IsPersistable: true},
		func(context.Context) (AnyResult, error) { return test.inner, nil })
	require.NoError(t, err)
	bundle := exportTestBundle(t, ctx, producer, test.inner, receiver)
	reply, err := cloud.MergeValues(cloudCtx, "cache-a", bundle)
	require.NoError(t, err)
	second := secondRunTestNumber(bundle, reply, aStep)
	test.number = secondRunTestNumber(bundle, reply, test.inner)
	require.NotEqual(t, first, second, "cache-a's run is kept apart under its receiver")
	test.attach(t, cloudCtx, test.span(t, true, 0))

	sent, err := cloud.StoredBundle(cloudCtx, []uint64{first, test.number}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Equal(t, []uint64{test.number}, sent.OtherRun, "K, whose call's receiver is over the other run, is left out")
	var numbers []uint64
	for _, value := range sent.Bundle.Values {
		numbers = append(numbers, value.SenderNumber)
	}
	require.Contains(t, numbers, first)
	require.NotContains(t, numbers, second, "one run of the step")

	alone, err := cloud.StoredBundle(cloudCtx, []uint64{test.number}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Empty(t, alone.OtherRun)
	require.Len(t, alone.Bundle.Calls, 1, "alone, K goes with its call, over the run its receiver was built over")
}
