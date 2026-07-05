package dagql

import (
	"bytes"
	"context"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"

	"github.com/dagger/dagger/engine/wcprof"
)

// Native emit-side coverage for the four approved recording changes
// (whatif-cached lazy-semantics §4.2, §4.4; catalog rows V27/V30/V31 emit
// halves): the lazy op carries the producer's recipe digest, the Evaluate
// fast path leaves deduped forced-evaluation facts, hits distinguish
// complete (B1) from pending-production (B2), and attribution loss degrades
// to omission without altering evaluation behavior.
//
// NOT t.Parallel(): the wcprof recorder is engine-global; this test flushes
// it and must not interleave with another profiled test.
func TestLazyProfEmitAndForcedFacts(t *testing.T) {
	baseCtx := cacheTestContext(t.Context())
	cacheIface, err := NewCache(baseCtx, "", nil, nil)
	assert.NilError(t, err)
	ctx := ContextWithCache(baseCtx, cacheIface)
	c := cacheIface
	srv := cacheTestServer(t)

	wcprof.EnsureRecorder()
	profCtx := wcprof.ContextWithProfiling(ctx)
	// Drain anything a prior profiled flow left in the global recorder so the
	// assertions below see only this test's events.
	assert.NilError(t, wcprof.Active().WriteDump(&bytes.Buffer{}, true))

	prodCall := &ResultCall{
		Type: NewResultCallType(&ast.Type{
			NamedType: "CacheTestObject",
			NonNull:   true,
		}),
		Field: "lazyProducer",
	}
	prodEvals := 0
	prodRes, err := c.GetOrInitCall(profCtx, "test-session", srv, &CallRequest{ResultCall: prodCall}, func(context.Context) (AnyResult, error) {
		return cacheTestObjectResultWithValue(t, srv, prodCall, &cacheTestObject{
			Value: 1,
			lazyEval: func(context.Context) error {
				prodEvals++
				return nil
			},
		}), nil
	})
	assert.NilError(t, err)

	// A hit BEFORE production runs: lazy-semantics state B2 → hit_pending.
	_, err = c.GetOrInitCall(profCtx, "test-session", srv, &CallRequest{ResultCall: prodCall}, func(context.Context) (AnyResult, error) {
		t.Fatal("must be a cache hit")
		return nil, nil
	})
	assert.NilError(t, err)

	// First Evaluate under forcer1: the leader path — mints the lazy op.
	f1Ctx, f1 := wcprof.BeginOp(profCtx, wcprof.OpKindCall, "forcer1", wcprof.OpOpts{})
	assert.NilError(t, c.Evaluate(f1Ctx, prodRes))
	f1.End(wcprof.OutcomeOK)
	assert.Equal(t, prodEvals, 1)

	// A hit AFTER production completed: state B1 → plain hit.
	_, err = c.GetOrInitCall(profCtx, "test-session", srv, &CallRequest{ResultCall: prodCall}, func(context.Context) (AnyResult, error) {
		t.Fatal("must be a cache hit")
		return nil, nil
	})
	assert.NilError(t, err)

	// Post-completion forces: forcer2 twice (deduped to ONE fact) and
	// forcer3 once. Production never re-runs.
	f2Ctx, f2 := wcprof.BeginOp(profCtx, wcprof.OpKindCall, "forcer2", wcprof.OpOpts{})
	assert.NilError(t, c.Evaluate(f2Ctx, prodRes))
	assert.NilError(t, c.Evaluate(f2Ctx, prodRes))
	f2.End(wcprof.OutcomeOK)
	f3Ctx, f3 := wcprof.BeginOp(profCtx, wcprof.OpKindCall, "forcer3", wcprof.OpOpts{})
	assert.NilError(t, c.Evaluate(f3Ctx, prodRes))
	f3.End(wcprof.OutcomeOK)
	assert.Equal(t, prodEvals, 1)

	var buf bytes.Buffer
	assert.NilError(t, wcprof.Active().WriteDump(&buf, true))
	header, events, err := wcprof.ReadDump(bytes.NewReader(buf.Bytes()))
	assert.NilError(t, err)
	str := func(id uint32) string { return header.Strings[id] }

	// The three call events for the producer recipe: executed, then the
	// pending-production hit (B2), then the complete hit (B1).
	var wantDigest string
	var callOutcomes []string
	var forcerIDs = map[string]uint64{}
	for _, ev := range events {
		if ev.Type != "op" {
			continue
		}
		switch {
		case ev.OpKind == wcprof.OpKindCall.String() && str(ev.ClassID) == "forcer1",
			ev.OpKind == wcprof.OpKindCall.String() && str(ev.ClassID) == "forcer2",
			ev.OpKind == wcprof.OpKindCall.String() && str(ev.ClassID) == "forcer3":
			forcerIDs[str(ev.ClassID)] = ev.OpID
		case ev.OpKind == wcprof.OpKindCall.String() && str(ev.IdentID) != "":
			wantDigest = str(ev.IdentID)
			callOutcomes = append(callOutcomes, ev.Outcome)
		}
	}
	assert.Assert(t, wantDigest != "", "the producer call must carry a recipe digest")
	assert.DeepEqual(t, callOutcomes, []string{
		wcprof.OutcomeExecuted.String(),
		wcprof.OutcomeHitPending.String(),
		wcprof.OutcomeHit.String(),
	})

	// The lazy op carries the SAME digest as its ident (the general-rule
	// emit), plus the shared result id.
	var lazyOpID uint64
	for _, ev := range events {
		if ev.Type == "op" && ev.OpKind == wcprof.OpKindLazy.String() {
			assert.Equal(t, str(ev.IdentID), wantDigest, "lazy op must carry the producer digest")
			lazyOpID = ev.OpID
		}
	}
	assert.Assert(t, lazyOpID != 0, "expected the lazy op event")

	// Forced facts: exactly one per distinct post-completion forcer (forcer2
	// deduped across its two Evaluates, forcer3 once), targeting the
	// completing lazy op and carrying the producer digest. forcer1 led the
	// evaluation and leaves none.
	var facts []wcprof.DumpEvent
	for _, ev := range events {
		if ev.Type == "link" && ev.LinkKind == wcprof.LinkKindForced.String() {
			facts = append(facts, ev)
		}
	}
	assert.Equal(t, len(facts), 2, "one fact per distinct forcer, duplicates deduped")
	seenForcers := map[uint64]bool{}
	for _, f := range facts {
		assert.Equal(t, str(f.IdentID), wantDigest)
		assert.Equal(t, f.TargetID, lazyOpID)
		seenForcers[f.ParentID] = true
	}
	assert.Assert(t, seenForcers[forcerIDs["forcer2"]], "forcer2's fact missing")
	assert.Assert(t, seenForcers[forcerIDs["forcer3"]], "forcer3's fact missing")
	assert.Assert(t, !seenForcers[forcerIDs["forcer1"]], "the evaluation leader must not leave a forced fact")
}

// Attribution loss degrades to omission, never behavior change: a shared
// result without a loadable ResultCall evaluates normally and its lazy op
// simply carries no ident (the omit-on-error discipline's shared path).
func TestLazyProfEmitOmitsIdentWithoutFrame(t *testing.T) {
	baseCtx := cacheTestContext(t.Context())
	cacheIface, err := NewCache(baseCtx, "", nil, nil)
	assert.NilError(t, err)
	ctx := ContextWithCache(baseCtx, cacheIface)
	c := cacheIface
	srv := cacheTestServer(t)

	wcprof.EnsureRecorder()
	profCtx := wcprof.ContextWithProfiling(ctx)
	assert.NilError(t, wcprof.Active().WriteDump(&bytes.Buffer{}, true))

	call := &ResultCall{
		Type: NewResultCallType(&ast.Type{
			NamedType: "CacheTestObject",
			NonNull:   true,
		}),
		Field: "frameless",
	}
	evals := 0
	res, err := c.GetOrInitCall(profCtx, "test-session", srv, &CallRequest{ResultCall: call}, func(context.Context) (AnyResult, error) {
		return cacheTestObjectResultWithValue(t, srv, call, &cacheTestObject{
			Value: 1,
			lazyEval: func(context.Context) error {
				evals++
				return nil
			},
		}), nil
	})
	assert.NilError(t, err)

	// White-box: blind the authoritative frame so the digest derivation has
	// nothing to work from.
	res.cacheSharedResult().storeResultCall(nil)

	suppressedBefore, _ := wcprof.Active().SuppressedCounts()
	assert.NilError(t, c.Evaluate(profCtx, res))
	assert.Equal(t, evals, 1, "evaluation behavior must be unchanged")

	// The omission is never silent: exactly this evaluation's derivation
	// failure must land in the suppression counter (doctrine — the
	// what-if-cached analysis refuses a capture with a nonzero count).
	suppressedAfter, _ := wcprof.Active().SuppressedCounts()
	assert.Equal(t, suppressedAfter-suppressedBefore, uint64(1),
		"the ident omission must be counted")

	var buf bytes.Buffer
	assert.NilError(t, wcprof.Active().WriteDump(&buf, true))
	header, events, err := wcprof.ReadDump(bytes.NewReader(buf.Bytes()))
	assert.NilError(t, err)
	sawLazy := false
	for _, ev := range events {
		if ev.Type == "op" && ev.OpKind == wcprof.OpKindLazy.String() {
			sawLazy = true
			assert.Equal(t, header.Strings[ev.IdentID], "", "attribution must degrade to omission")
		}
	}
	assert.Assert(t, sawLazy, "the lazy op must still be recorded")
	assert.Assert(t, header.SuppressedIdentDerivations >= 1,
		"the dump header must carry the suppression count for the analyzer's admission gate")
}
