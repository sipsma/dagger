package dagql

import (
	"context"
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
	require.Empty(t, sent.Withheld)
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
	_, overSecond := s.cloud.resultsByID[sharedResultID(root)].deps[sharedResultID(second)]
	_, overFirst := s.cloud.resultsByID[sharedResultID(root)].deps[sharedResultID(s.first)]
	s.cloud.egraphMu.RUnlock()
	require.NotNil(t, current)
	require.Equal(t, s.first, uint64(current.id), "the first run stays the recipe's current entry")
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

// The unique result usually comes in a later export than the second run
// below it, which the later export sends as a held row: the Cloud kept the
// run apart when it first came, and the held row lands on it.
func TestAUniqueResultInALaterExportLandsOnTheSecondRunBelowIt(t *testing.T) {
	t.Parallel()
	s := newSecondRunTestSetup(t)
	known := NewKnownRows()
	stepBundle, stepReply := secondRunTestExport(t, s.bctx, s.b, known, s.cloud, s.cctx, "cache-b", s.bStep)
	second := secondRunTestNumber(stepBundle, stepReply, s.bStep)
	require.NotZero(t, second)
	require.NotEqual(t, s.first, second, "the run is kept apart as it comes")
	facts := secondRunTestFacts(t, s.cloud, second)
	require.True(t, facts.SecondRun)
	require.False(t, facts.KeptUnder, "nothing stands over it yet")

	bundle, reply := secondRunTestExport(t, s.bctx, s.b, known, s.cloud, s.cctx, "cache-b", s.bRoot)
	require.Empty(t, reply.Stale)
	ordinal := secondRunTestOrdinal(t, bundle, s.bStep)
	require.True(t, slices.ContainsFunc(bundle.Values, func(v TransferredValue) bool { return v.Ordinal == ordinal && v.Held != "" }), "the later export sends the step as a held row")
	s.checkKeptApart(t, second, secondRunTestNumber(bundle, reply, s.bRoot))
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
// only holdings keep. A second run a recipe's current entry stands over
// stays, and so does every current entry. A second run's own export root
// renews its recipe's current entry's retention, and has none of its own.
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
	transferTestDependency(s.b, s.bctx, bOver, bBase)
	chainBundle, chainReply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", bOver)
	secondOver, secondBase := secondRunTestNumber(chainBundle, chainReply, bOver), secondRunTestNumber(chainBundle, chainReply, bBase)
	require.NotEqual(t, firstOver, secondOver)
	require.NotEqual(t, firstBase, secondBase)
	s.cloud.egraphMu.RLock()
	_, secondRetained := s.cloud.persistedEdgesByResult[sharedResultID(secondOver)]
	_, firstRetained := s.cloud.persistedEdgesByResult[sharedResultID(firstOver)]
	s.cloud.egraphMu.RUnlock()
	require.False(t, secondRetained, "a second run has no retention of its own")
	require.True(t, firstRetained)

	_, err := s.cloud.ReleaseSecondRuns(s.cctx, "cache-b", func(CacheID) bool { return false })
	require.NoError(t, err)
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
	known := NewKnownRows()
	bundle, reply := secondRunTestExport(t, s.bctx, s.b, known, s.cloud, s.cctx, "cache-b", s.bStep)
	second := secondRunTestNumber(bundle, reply, s.bStep)
	_, err := s.cloud.ReleaseSecondRuns(s.cctx, "cache-a", func(id CacheID) bool { return id == "cache-b" })
	require.NoError(t, err)
	_, ok := s.cloud.EntryRun(second)
	require.True(t, ok, "cache-b still runs")
	_, err = s.cloud.ReleaseSecondRuns(s.cctx, "cache-b", func(CacheID) bool { return false })
	require.NoError(t, err)
	_, ok = s.cloud.EntryRun(second)
	require.False(t, ok, "released at cache-b's end")

	// cache-b's later export over its released run sends the step held:
	// refused as stale, it comes in full and is stored again.
	rootBundle, rootReply := secondRunTestExport(t, s.bctx, s.b, known, s.cloud, s.cctx, "cache-b", s.bRoot)
	require.Equal(t, []TransferOrdinal{secondRunTestOrdinal(t, rootBundle, s.bStep)}, rootReply.Stale)
	rootBundle, rootReply = secondRunTestExport(t, s.bctx, s.b, known, s.cloud, s.cctx, "cache-b", s.bRoot)
	require.Empty(t, rootReply.Stale)
	s.checkKeptApart(t, secondRunTestNumber(rootBundle, rootReply, s.bStep), secondRunTestNumber(rootBundle, rootReply, s.bRoot))
}
