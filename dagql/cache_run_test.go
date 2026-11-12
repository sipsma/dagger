package dagql

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// testRunOf returns the run the entry with the number holds in c, for a test
// that stores a part of the entry's own run.
func testRunOf(c *Cache, number uint64) RunName {
	run, _ := c.EntryRun(number)
	return run
}

// runTestSource is cache A's directory r and its read d, computed from r: d
// depends on r. It returns A's cache and context, and the bundle of d with r
// below it.
func runTestSource(t *testing.T) ValueBundle {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "r", &transferTestValue{Text: "a's r"})
	d := persistedListTestResult(t, actx, a, asrv, "d", &transferTestValue{Text: "read of a's r"})
	transferTestDependency(a, actx, d, r)
	return exportTestBundle(t, actx, a, d)
}

// runTestRecipe returns the recipe of the bundle's record of field.
func runTestRecipe(t *testing.T, c *Cache, bundle ValueBundle, field string) string {
	t.Helper()
	for _, value := range bundle.Values {
		if value.Record.Call != nil && value.Record.Call.Field == field {
			recipe, err := value.Record.Call.deriveRecipeDigest(c)
			require.NoError(t, err)
			return recipe.String()
		}
	}
	t.Fatalf("the bundle has no record of %s", field)
	return ""
}

// An engine that ran a recipe itself keeps its own run, and a record computed
// over another engine's run of that recipe isn't merged onto it: B's own r
// stays, A's read of A's r is skipped, and B gets none of A's parts for r. The
// merge used to keep B's r and hang A's read on it, which is how a read from
// one run came to stand beside the bytes of another.
func TestMergeKeepsAReadWithItsRun(t *testing.T) {
	t.Parallel()
	bundle := runTestSource(t)
	bctx, b, bsrv := transferTestCache(t)
	r := persistedListTestResult(t, bctx, b, bsrv, "r", &transferTestValue{Text: "b's r"})

	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.Len(t, reply.Roots, 1)
	require.True(t, reply.Roots[0].OtherRun, "A's read was computed over another run of r than B holds")
	require.Empty(t, reply.Imported())

	recipe := digest.Digest(runTestRecipe(t, b, bundle, "d"))
	b.egraphMu.RLock()
	read := b.currentEntryForRecipeLocked(recipe)
	row := r.cacheSharedResult()
	imported, offers := row.imported, 0
	for range row.partOffersLocked() {
		offers++
	}
	key, _ := row.cloudHoldingLocked()
	b.egraphMu.RUnlock()
	require.Nil(t, read, "B has no read of A's r")
	require.False(t, imported, "B keeps its own r")
	require.Zero(t, offers, "and takes none of A's parts for it")
	require.Zero(t, key, "nor takes it for a copy of the Cloud's r")
	for _, value := range reply.Values {
		require.NotEqual(t, uint64(row.id), value.Number, "the reply doesn't report B's r as the Cloud's")
	}
}

// Records of the run an engine holds merge as before: B gets A's r first,
// then A's read of it, which lands beside the same run.
func TestMergeTakesAReadOfTheRunItHolds(t *testing.T) {
	t.Parallel()
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "r", &transferTestValue{Text: "a's r"})
	d := persistedListTestResult(t, actx, a, asrv, "d", &transferTestValue{Text: "read of a's r"})
	transferTestDependency(a, actx, d, r)
	first := exportTestBundle(t, actx, a, r)
	second := exportTestBundle(t, actx, a, d)

	bctx, b, _ := transferTestCache(t)
	_, err := b.MergeValues(bctx, cloudCacheID, first)
	require.NoError(t, err)
	reply, err := b.MergeValues(bctx, cloudCacheID, second)
	require.NoError(t, err)
	require.Len(t, reply.Roots, 1)
	require.False(t, reply.Roots[0].OtherRun)
	require.Len(t, reply.Imported(), 1, "B takes A's read of the r it holds")
}

// A held row stands for the recipient's own value, but only of the run it
// names: a held row naming a run B doesn't hold takes no record with it.
func TestMergeHeldRowOfAnotherRunTakesNothing(t *testing.T) {
	t.Parallel()
	bundle := runTestSource(t)
	bctx, b, bsrv := transferTestCache(t)
	persistedListTestResult(t, bctx, b, bsrv, "r", &transferTestValue{Text: "b's r"})
	recipe := runTestRecipe(t, b, bundle, "r")
	for i := range bundle.Values {
		value := &bundle.Values[i]
		if value.Record.Call != nil && value.Record.Call.Field == "r" {
			value.Held = digest.Digest(recipe)
			value.Record = PersistedRecord{ResultID: uint64(value.Ordinal)}
			value.Run = &RunName{Cache: "cache-a", Number: 1}
		}
	}

	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.Empty(t, reply.Stale, "B holds a value of the recipe")
	require.True(t, reply.Roots[0].OtherRun)
	readRecipe := digest.Digest(runTestRecipe(t, b, runTestSource(t), "d"))
	b.egraphMu.RLock()
	read := b.currentEntryForRecipeLocked(readRecipe)
	b.egraphMu.RUnlock()
	require.Nil(t, read)
}

// The Cloud keeps the first run of a recipe it stores, and a second engine's
// export of its own run, with a read computed over it, doesn't hang the read
// on the first run.
func TestCloudKeepsAReadWithItsRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	actx, a, asrv := transferTestCache(t)
	ar := persistedListTestResult(t, actx, a, asrv, "r", &transferTestValue{Text: "a's r"})
	_, err := cloud.MergeValues(ctx, "cache-a", exportTestBundle(t, actx, a, ar))
	require.NoError(t, err)

	fromB := runTestSource(t)
	reply, err := cloud.MergeValues(ctx, "cache-b", fromB)
	require.NoError(t, err)
	require.True(t, reply.Roots[0].OtherRun, "B's read was computed over B's r, and the Cloud keeps A's")
	readRecipe := digest.Digest(runTestRecipe(t, a, fromB, "d"))
	cloud.egraphMu.RLock()
	read := cloud.currentEntryForRecipeLocked(readRecipe)
	cloud.egraphMu.RUnlock()
	require.Nil(t, read)
}

// The Cloud stores a part only on an entry of the run it was uploaded from.
func TestSetStoredPartTakesOnlyTheEntrysRun(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	bundle, _ := mergeTestSource(t, "root", "value")
	reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	number := reply.Imported()[0].ResultID
	run, ok := cloud.EntryRun(number)
	require.True(t, ok)
	require.Equal(t, RunName{Cache: "cache-a", Number: bundle.Values[0].SenderNumber, Replacements: bundle.Values[0].SenderReplacements}, run, "the entry holds the run of the engine that made it")

	stored, err := cloud.SetStoredPart(ctx, number, RunName{Cache: "cache-b", Number: run.Number}, testLiveOffer(), 0)
	require.NoError(t, err)
	require.False(t, stored, "a part of another run isn't stored")
	stored, err = cloud.SetStoredPart(ctx, number, run, testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)
}

// A run travels unchanged: what B merged from the Cloud names the run of the
// engine that made it, in B's export and after B restarts.
func TestAMergedRunTravelsAndSurvivesARestart(t *testing.T) {
	t.Parallel()
	made := RunName{Cache: "cache-a", Number: 41, Replacements: 2}
	bundle, _ := mergeTestSource(t, "root", "value")
	bundle.Values[0].Run = &made

	path := filepath.Join(t.TempDir(), "cache.db")
	bctx, b, bsrv := persistedListTestCache(t, path)
	bsrv.InstallObject(NewClass(bsrv, ClassOpts[*transferTestValue]{}))
	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.NoError(t, err)
	number := reply.Imported()[0].ResultID
	run, ok := b.EntryRun(number)
	require.True(t, ok)
	require.Equal(t, made, run)

	root, err := b.LoadResultByResultID(bctx, "test-session", bsrv, number)
	require.NoError(t, err)
	exported := exportTestBundle(t, bctx, b, root)
	require.NotNil(t, exported.Values[0].Run)
	require.Equal(t, made, *exported.Values[0].Run, "B's export names the run, not B's entry")

	require.NoError(t, b.ReleaseSession(bctx, "test-session"))
	require.NoError(t, b.Close(bctx))
	_, b, _ = persistedListTestCache(t, path)
	run, ok = b.EntryRun(number)
	require.True(t, ok)
	require.Equal(t, made, run, "the run survives a restart")
}

// A result that came with its bytes is tied to its runs too: its bytes don't
// cover what its references read, as a container's mounted files are read at
// its next exec, and the Cloud can still decline to store them. B keeps its
// own r, and A's d, computed over A's r, is skipped with none of its parts.
func TestMergeTiesAResultThatCameWithItsBytes(t *testing.T) {
	t.Parallel()
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "r", &transferTestValue{Text: "a's r"})
	d := persistedListTestResult(t, actx, a, asrv, "d", &transferTestValue{Text: "pending"})
	transferTestDependency(a, actx, d, r)
	out, err := a.testOfferParts(actx, d, []PersistedPartOffer{testLiveOffer()})
	require.NoError(t, err)
	require.Equal(t, OfferAccepted, out[0].Outcome)
	bundle := exportTestBundle(t, actx, a, d)

	bctx, b, bsrv := transferTestCache(t)
	own := persistedListTestResult(t, bctx, b, bsrv, "r", &transferTestValue{Text: "b's r"})
	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.True(t, reply.Roots[0].OtherRun, "A's d was computed over another run of r")
	require.Empty(t, reply.Imported())
	require.Zero(t, mergeTestOffers(b, own.cacheSharedResult()), "and B takes none of A's parts for its own r")
}

// runTestRows returns a copy of bundle whose rows sent for the sender's
// entries with the numbers name the run: rows of a run the recipient or the
// Cloud holds, as when a cache's export reached the Cloud and comes back to
// it, or one run a sender holds on two entries.
func runTestRows(bundle ValueBundle, run RunName, senders ...uint64) ValueBundle {
	out := bundle
	out.Values = append([]TransferredValue(nil), bundle.Values...)
	for i := range out.Values {
		if slices.Contains(senders, out.Values[i].SenderNumber) {
			named := run
			out.Values[i].Run = &named
		}
	}
	return out
}

// runTestID is res's number, for runTestRows and runTestOf.
func runTestID(res AnyResult) uint64 {
	return uint64(res.cacheSharedResult().id)
}

// runTestNamed returns a copy of bundle whose rows name the runs of the cache
// from: the same records, of the same runs, passed on by another sender.
func runTestNamed(bundle ValueBundle, from CacheID) ValueBundle {
	out := bundle
	out.Values = append([]TransferredValue(nil), bundle.Values...)
	for i := range out.Values {
		run := TransferredRun(from, out.Values[i])
		out.Values[i].Run = &run
	}
	return out
}

// runTestSetRun gives the entry with the number in c the run, as when its
// value came to c from the cache that ran it.
func runTestSetRun(c *Cache, number uint64, run RunName) {
	c.egraphMu.Lock()
	defer c.egraphMu.Unlock()
	c.resultsByID[sharedResultID(number)].run = &run
}
