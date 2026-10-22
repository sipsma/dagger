package dagql

// The nearest-term search on a structural miss: which known entry is the
// same operation over different inputs, or the same field over the same
// inputs with different arguments, and how far the bounded search went.

import (
	"context"
	"fmt"
	"testing"

	"github.com/opencontainers/go-digest"
	"gotest.tools/v3/assert"

	"github.com/dagger/dagger/dagql/call"
)

// nearTestFrame is field over one structural input per witness, with an
// int argument n.
func nearTestFrame(field string, n int64, witnesses ...digest.Digest) *ResultCall {
	frame := cacheTestIntCall(field)
	frame.Args = []*ResultCallArg{{
		Name:  "n",
		Value: &ResultCallLiteral{Kind: ResultCallLiteralKindInt, IntValue: n},
	}}
	for i, dig := range witnesses {
		frame.Args = append(frame.Args, &ResultCallArg{
			Name: fmt.Sprintf("src%d", i),
			Value: &ResultCallLiteral{
				Kind:                 ResultCallLiteralKindDigestedString,
				DigestedStringValue:  "witnessed",
				DigestedStringDigest: dig,
			},
		})
	}
	return frame
}

// nearTestKnown gives dig a class with no term over it: a result carrying it
// as an extra digest.
func nearTestKnown(t testing.TB, ctx context.Context, c *Cache, dig digest.Digest) {
	t.Helper()
	teacher := cacheTestIntCall("near-teacher-"+dig.Encoded()[:12], call.ExtraDigest{Label: call.ExtraDigestLabelContent, Digest: dig})
	_, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{ResultCall: teacher}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(teacher, 0), nil
	})
	assert.NilError(t, err)
}

func nearTestPublish(t testing.TB, ctx context.Context, c *Cache, frame *ResultCall) AnyResult {
	t.Helper()
	res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{ResultCall: frame}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(frame, 1), nil
	})
	assert.NilError(t, err)
	return res
}

// nearTestMiss runs frame, armed when armed is set, and asserts it missed.
func nearTestMiss(t *testing.T, ctx context.Context, c *Cache, frame *ResultCall, armed bool) *CacheDecision {
	t.Helper()
	req := &CallRequest{ResultCall: frame}
	if armed {
		req.CacheEvidence = NewCacheDecision()
	}
	calls := 0
	res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, req, func(context.Context) (AnyResult, error) {
		calls++
		return cacheTestIntResult(frame, 2), nil
	})
	assert.NilError(t, err)
	assert.Equal(t, 1, calls)
	assert.Assert(t, !res.HitCache())
	if armed {
		assert.Equal(t, CacheOutcomeExecuted, req.CacheEvidence.Outcome)
	}
	return req.CacheEvidence
}

// The same operation over a different second input: the neighbour is the
// entry over the first input, and the second slot is the one that differs.
func TestCacheEvidenceNearInputs(t *testing.T) {
	t.Parallel()
	ctx, c := cacheTestEvidenceEnv(t)
	a, b, cDig := digest.FromString("near-a"), digest.FromString("near-b"), digest.FromString("near-c")
	nearTestKnown(t, ctx, c, cDig)
	neighbour := nearTestPublish(t, ctx, c, nearTestFrame("near-op", 1, a, b))

	ev := nearTestMiss(t, ctx, c, nearTestFrame("near-op", 1, a, cDig), true)
	assert.DeepEqual(t, []int{1}, ev.MissFreshInputs)
	assert.Equal(t, CacheNearSearchFound, ev.NearSearch)
	assert.Equal(t, CacheNearKindInputs, ev.NearKind)
	assert.DeepEqual(t, []int{1}, ev.NearInputsDiffer)
	assert.Equal(t, uint64(neighbour.cacheSharedResult().id), ev.NearResultID)
	assert.Assert(t, !ev.NearRemote)
	assert.Assert(t, ev.NearScanned >= 1)
}

// The same field over the same input with a different argument: the
// neighbour is the entry with the other argument.
func TestCacheEvidenceNearSelf(t *testing.T) {
	t.Parallel()
	ctx, c := cacheTestEvidenceEnv(t)
	x := digest.FromString("near-self-x")
	neighbour := nearTestPublish(t, ctx, c, nearTestFrame("near-self-op", 1, x))

	ev := nearTestMiss(t, ctx, c, nearTestFrame("near-self-op", 2, x), true)
	assert.Equal(t, 0, len(ev.MissFreshInputs))
	assert.Equal(t, CacheNearSearchFound, ev.NearSearch)
	assert.Equal(t, CacheNearKindSelf, ev.NearKind)
	assert.Equal(t, 0, len(ev.NearInputsDiffer))
	assert.Equal(t, uint64(neighbour.cacheSharedResult().id), ev.NearResultID)
}

// A neighbour whose value a Cloud merge installed is reported as remote, with
// the merge's commit time: a key mismatch against a merged entry.
func TestCacheEvidenceNearRemote(t *testing.T) {
	t.Parallel()
	ctx, c := cacheTestEvidenceEnv(t)
	a, b, cDig := digest.FromString("near-remote-a"), digest.FromString("near-remote-b"), digest.FromString("near-remote-c")
	nearTestKnown(t, ctx, c, cDig)
	neighbour := nearTestPublish(t, ctx, c, nearTestFrame("near-remote-op", 1, a, b))
	const mergedAt = int64(1760000000123456789)
	// As a merge leaves it (installMergedRecordLocked).
	c.egraphMu.Lock()
	neighbour.cacheSharedResult().imported = true
	neighbour.cacheSharedResult().mergedAtUnixNano = mergedAt
	c.egraphMu.Unlock()

	ev := nearTestMiss(t, ctx, c, nearTestFrame("near-remote-op", 1, a, cDig), true)
	assert.Equal(t, CacheNearKindInputs, ev.NearKind)
	assert.Equal(t, uint64(neighbour.cacheSharedResult().id), ev.NearResultID)
	assert.Assert(t, ev.NearRemote)
	assert.Equal(t, mergedAt, ev.NearMergedAtUnixNano)
}

// A call over an input other calls use, unlike any of them, finds none after
// visiting them all; past the bound it reports truncated.
func TestCacheEvidenceNearNoneAndTruncated(t *testing.T) {
	t.Parallel()
	t.Run("none", func(t *testing.T) {
		t.Parallel()
		ctx, c := cacheTestEvidenceEnv(t)
		x := digest.FromString("near-none-x")
		nearTestPublish(t, ctx, c, nearTestFrame("near-none-other", 1, x))
		ev := nearTestMiss(t, ctx, c, nearTestFrame("near-none-op", 1, x), true)
		assert.Equal(t, CacheNearSearchNone, ev.NearSearch)
		assert.Equal(t, 1, ev.NearScanned)
		assert.Equal(t, CacheNearKind(""), ev.NearKind)
		assert.Equal(t, uint64(0), ev.NearResultID)
	})
	t.Run("truncated", func(t *testing.T) {
		t.Parallel()
		ctx, c := cacheTestEvidenceEnv(t)
		x := digest.FromString("near-truncated-x")
		for i := range nearestTermScanLimit + 8 {
			nearTestPublish(t, ctx, c, nearTestFrame(fmt.Sprintf("near-truncated-other-%d", i), 1, x))
		}
		ev := nearTestMiss(t, ctx, c, nearTestFrame("near-truncated-op", 1, x), true)
		assert.Equal(t, CacheNearSearchTruncated, ev.NearSearch)
		assert.Equal(t, nearestTermScanLimit, ev.NearScanned)
	})
	t.Run("only fresh inputs", func(t *testing.T) {
		t.Parallel()
		ctx, c := cacheTestEvidenceEnv(t)
		x := digest.FromString("near-fresh-x")
		nearTestKnown(t, ctx, c, x)
		ev := nearTestMiss(t, ctx, c, nearTestFrame("near-fresh-op", 1, x), true)
		assert.DeepEqual(t, []int{0}, ev.MissFreshInputs)
		assert.Equal(t, CacheNearSearchNone, ev.NearSearch)
		assert.Equal(t, 0, ev.NearScanned)
	})
}

// nearTestManyResults publishes frame n times, expiring each value before the
// next while the session still uses it, so the publication retires it and the
// term gains a result each time. It returns the term's results.
func nearTestManyResults(t testing.TB, ctx context.Context, c *Cache, frame *ResultCall, n int) []sharedResultID {
	t.Helper()
	var ids []sharedResultID
	for i := range n {
		res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{ResultCall: frame, TTL: 3600}, func(context.Context) (AnyResult, error) {
			return cacheTestIntResult(frame, i), nil
		})
		assert.NilError(t, err)
		ids = append(ids, res.cacheSharedResult().id)
		currentEntryTestExpire(c, res)
	}
	return ids
}

// A neighbour with many results is chosen among a bounded few of them: the
// search inspects at most nearestResultLimit results to pick it, however many
// the term has.
func TestCacheEvidenceNearBoundsResultInspection(t *testing.T) {
	t.Parallel()
	ctx, c := cacheTestEvidenceEnv(t)
	a, b, cDig := digest.FromString("near-many-a"), digest.FromString("near-many-b"), digest.FromString("near-many-c")
	nearTestKnown(t, ctx, c, cDig)
	ids := nearTestManyResults(t, ctx, c, nearTestFrame("near-many-op", 1, a, b), 4*nearestResultLimit)
	var near nearTerm
	c.testAfterNearSearch = func(n nearTerm) { near = n }

	ev := nearTestMiss(t, ctx, c, nearTestFrame("near-many-op", 1, a, cDig), true)
	assert.Equal(t, CacheNearKindInputs, ev.NearKind)
	assert.DeepEqual(t, []int{1}, ev.NearInputsDiffer)
	c.egraphMu.RLock()
	termResults := len(c.termResults[near.term])
	c.egraphMu.RUnlock()
	assert.Assert(t, termResults > nearestResultLimit, "the neighbour term has %d results", termResults)
	assert.Assert(t, near.inspected <= nearestResultLimit, "inspected %d results", near.inspected)
	assert.Assert(t, ev.NearResultID != 0)
	found := false
	for _, id := range ids {
		found = found || uint64(id) == ev.NearResultID
	}
	assert.Assert(t, found, "the result is one of the neighbour's")
}

// Same-input terms of other fields cost a frame load each to rule out, and the
// search loads at most nearestFieldCheckLimit frames. A same-input term the
// bound leaves unchecked makes the search truncated, not none: it could have
// been the neighbour.
func TestCacheEvidenceNearBoundsFrameLoads(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		others int
		want   CacheNearSearch
	}{
		{others: nearestFieldCheckLimit, want: CacheNearSearchNone},
		{others: nearestFieldCheckLimit + 1, want: CacheNearSearchTruncated},
		{others: nearestTermScanLimit, want: CacheNearSearchTruncated},
	} {
		t.Run(fmt.Sprint(tc.others), func(t *testing.T) {
			t.Parallel()
			ctx, c := cacheTestEvidenceEnv(t)
			x := digest.FromString("near-frames-x")
			for i := range tc.others {
				nearTestManyResults(t, ctx, c, nearTestFrame(fmt.Sprintf("near-frames-other-%d", i), 1, x), 2)
			}
			var near nearTerm
			c.testAfterNearSearch = func(n nearTerm) { near = n }
			ev := nearTestMiss(t, ctx, c, nearTestFrame("near-frames-op", 1, x), true)
			assert.Equal(t, tc.want, ev.NearSearch)
			assert.Equal(t, tc.others, ev.NearScanned)
			assert.Equal(t, nearestFieldCheckLimit, near.frames)
			assert.Assert(t, near.inspected <= nearestFieldCheckLimit*nearestResultLimit, "inspected %d results", near.inspected)
		})
	}
}

// A term can hold results with different frames: a request's term also holds
// the values its calls returned. A self-kind neighbour reports the result whose
// frame named the call's field, never another result of the same term.
//
// The returned frames are over another input, so the request's term is the only
// one the search can reach. Which of its two results it checks first follows
// map order, so the scenario repeats until the field matched in at least one
// run: with the older, lower-numbered result framed as another field, the
// matched result is never the one a lowest-number choice would report.
func TestCacheEvidenceNearSelfReportsTheMatchedResult(t *testing.T) {
	t.Parallel()
	const field = "near-mixed-f"
	matched := 0
	for i := range 64 {
		ctx, c := cacheTestEvidenceEnv(t)
		x, y := digest.FromString("near-mixed-x"), digest.FromString("near-mixed-y")
		request := nearTestFrame("near-mixed-h", 1, x)
		// The request's term gets a result framed as another field, then,
		// with that one expired and still held, one framed as field.
		for _, returned := range []*ResultCall{nearTestFrame("near-mixed-g", 1, y), nearTestFrame(field, 1, y)} {
			res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{ResultCall: request, TTL: 3600}, func(context.Context) (AnyResult, error) {
				return cacheTestIntResult(returned, i), nil
			})
			assert.NilError(t, err)
			currentEntryTestExpire(c, res)
		}
		ev := nearTestMiss(t, ctx, c, nearTestFrame(field, 2, x), true)
		assert.Equal(t, 1, ev.NearScanned, "run %d: only the request's term is reachable", i)
		if ev.NearKind != CacheNearKindSelf {
			// The other-field result was checked first.
			assert.Equal(t, CacheNearSearchNone, ev.NearSearch)
			continue
		}
		matched++
		assert.Assert(t, ev.NearResultID != 0)
		c.egraphMu.RLock()
		res := c.resultsByID[sharedResultID(ev.NearResultID)]
		c.egraphMu.RUnlock()
		assert.Assert(t, res != nil)
		assert.Equal(t, field, res.loadResultCall().Field, "run %d: the reported result's frame names the call's field", i)
		if matched == 4 {
			break
		}
	}
	assert.Assert(t, matched > 0, "the field never matched: the mixed term's path didn't run")
}

// The search doesn't run when the structural lookup was impossible or the
// call has no structural inputs.
func TestCacheEvidenceNearNotRun(t *testing.T) {
	t.Parallel()
	ctx, c := cacheTestEvidenceEnv(t)
	ev := nearTestMiss(t, ctx, c, nearTestFrame("near-unknown-op", 1, digest.FromString("near-unknown")), true)
	assert.Equal(t, 0, ev.MissUnknownInputIndex)
	assert.Equal(t, CacheNearSearch(""), ev.NearSearch)
	ev = nearTestMiss(t, ctx, c, cacheTestIntCall("near-no-inputs"), true)
	assert.Equal(t, CacheNearSearch(""), ev.NearSearch)
}

// The search never changes a decision: the same calls with no carrier miss
// and execute the same way, and an unarmed miss leaves the next lookup's
// outcome as it was.
func TestCacheEvidenceNearChangesNoDecision(t *testing.T) {
	t.Parallel()
	for _, armed := range []bool{false, true} {
		ctx, c := cacheTestEvidenceEnv(t)
		a, b, cDig := digest.FromString("near-decision-a"), digest.FromString("near-decision-b"), digest.FromString("near-decision-c")
		nearTestKnown(t, ctx, c, cDig)
		neighbour := nearTestPublish(t, ctx, c, nearTestFrame("near-decision-op", 1, a, b))
		nearTestMiss(t, ctx, c, nearTestFrame("near-decision-op", 1, a, cDig), armed)
		// The neighbour is still served to its own call.
		req := &CallRequest{ResultCall: nearTestFrame("near-decision-op", 1, a, b)}
		res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, req, func(context.Context) (AnyResult, error) {
			t.Fatal("the neighbour's own call must hit")
			return nil, nil
		})
		assert.NilError(t, err)
		assert.Equal(t, neighbour.cacheSharedResult().id, res.cacheSharedResult().id)
	}
}

// BenchmarkLookupMissNear times one structural miss lookup over a receiver
// class 10,000 terms share, unarmed and armed. Every term is a different field
// over the same input, so the search's worst case for terms runs: each visited
// term has the same inputs, the first few load a frame, and the search is
// truncated. The armed overhead is armed minus unarmed ns/op. The
// many-results case is a miss whose neighbour, the same operation over
// another input, has 64 results, so picking its result runs at the result
// bound.
func BenchmarkLookupMissNear(b *testing.B) {
	ctx := cacheTestContext(b.Context())
	c, err := NewCache(ctx, "", nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	ctx = ContextWithCache(ctx, c)
	x := digest.FromString("near-bench-x")
	const terms = 10_000
	for i := range terms {
		nearTestPublish(b, ctx, c, nearTestFrame(fmt.Sprintf("near-bench-other-%d", i), 1, x))
	}
	frame := nearTestFrame("near-bench-probe", 1, x)
	probe := &CallRequest{ResultCall: frame}
	recipe, err := probe.deriveRecipeDigest(c)
	if err != nil {
		b.Fatal(err)
	}
	self, refs, err := probe.selfDigestAndInputRefs(c)
	if err != nil {
		b.Fatal(err)
	}
	inputs := make([]digest.Digest, 0, len(refs))
	for _, ref := range refs {
		dig, err := ref.inputDigest(c)
		if err != nil {
			b.Fatal(err)
		}
		inputs = append(inputs, dig)
	}
	c.egraphMu.Lock()
	shared := len(c.inputEqClassToTerms[c.findEqClassLocked(c.egraphDigestToClass[x.String()])])
	c.egraphMu.Unlock()
	if shared < terms {
		b.Fatalf("the input class is shared by %d terms, want %d", shared, terms)
	}
	// The neighbour shares an input only it uses with the probe, so the
	// search finds it at once and picks its result at the bound.
	w, y, z := digest.FromString("near-bench-w"), digest.FromString("near-bench-y"), digest.FromString("near-bench-z")
	nearTestKnown(b, ctx, c, z)
	nearTestManyResults(b, ctx, c, nearTestFrame("near-bench-many", 1, y, w), 64)
	manyFrame := nearTestFrame("near-bench-many", 1, z, w)
	manyProbe := &CallRequest{ResultCall: manyFrame}
	manyRecipe, err := manyProbe.deriveRecipeDigest(c)
	if err != nil {
		b.Fatal(err)
	}
	manySelf, manyRefs, err := manyProbe.selfDigestAndInputRefs(c)
	if err != nil {
		b.Fatal(err)
	}
	manyInputs := make([]digest.Digest, 0, len(manyRefs))
	for _, ref := range manyRefs {
		dig, err := ref.inputDigest(c)
		if err != nil {
			b.Fatal(err)
		}
		manyInputs = append(manyInputs, dig)
	}
	for _, tc := range []struct {
		name   string
		armed  bool
		frame  *ResultCall
		recipe digest.Digest
		self   digest.Digest
		inputs []digest.Digest
		refs   []ResultCallStructuralInputRef
		want   CacheNearSearch
	}{
		{name: "unarmed", frame: frame, recipe: recipe, self: self, inputs: inputs, refs: refs},
		{name: "armed", armed: true, frame: frame, recipe: recipe, self: self, inputs: inputs, refs: refs, want: CacheNearSearchTruncated},
		{name: "many-results/unarmed", frame: manyFrame, recipe: manyRecipe, self: manySelf, inputs: manyInputs, refs: manyRefs},
		{name: "many-results/armed", armed: true, frame: manyFrame, recipe: manyRecipe, self: manySelf, inputs: manyInputs, refs: manyRefs, want: CacheNearSearchFound},
	} {
		armed, frame, recipe, self, inputs, refs := tc.armed, tc.frame, tc.recipe, tc.self, tc.inputs, tc.refs
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				req := &CallRequest{ResultCall: frame}
				if armed {
					req.CacheEvidence = NewCacheDecision()
				}
				c.egraphMu.Lock()
				_, hit, _, err := c.lookupCacheForRequestLocked(ctx, "test-session", req, recipe, self, inputs, refs)
				c.egraphMu.Unlock()
				if err != nil || hit {
					b.Fatalf("want a miss: hit=%v err=%v", hit, err)
				}
				if armed && req.CacheEvidence.NearSearch != tc.want {
					b.Fatalf("want search %q, got %q", tc.want, req.CacheEvidence.NearSearch)
				}
			}
		})
	}
}
