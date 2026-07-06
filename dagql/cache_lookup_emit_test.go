package dagql

import (
	"bytes"
	"context"
	"testing"

	set "github.com/hashicorp/go-set/v3"
	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"

	"github.com/dagger/dagger/engine/wcprof"
	"github.com/opencontainers/go-digest"
)

// Native emit-side coverage for the invalidation-tracing Chunk-3 additions
// (design §4): the E1 lookup-outcome fact at both entries, the do-not-cache
// ident micro-emit, and the E2 scope-input fact. Rows W14 (terminal
// precedence — derivation half here, analyzer half in wcanalyze) and the
// emit halves of W5/W2-native.
//
// NOT t.Parallel(): the wcprof recorder is engine-global; this test flushes
// it and must not interleave with another profiled test.

// classifyLookupMiss precedence (W14's derivation half): exactly one reason
// per miss, precedence = first terminal reached on the design-§2 decision
// path. Each expectation is derived from the path order before running:
// session filtering is the SELECTION terminal (candidates survived
// collection), expired skips fire at collection (step 2, before the
// structural stage), the structural abort (input_unknown) precedes the
// term-level distinctions, and a matched term with no live results is
// no_live_candidate, else nothing matched at all.
func TestClassifyLookupMissPrecedence(t *testing.T) {
	mk := func(expired, missing, termSet int, survived bool) lookupMissInfo {
		return classifyLookupMiss(lookupMatch{
			expiredSkipped:    expired,
			missingInputIndex: missing,
			termSetSize:       termSet,
		}, survived)
	}
	// Selection terminal wins whenever candidates survived collection —
	// even if expired skips also occurred along the way.
	assert.Equal(t, mk(3, -1, 0, true).reason, wcprof.LookupReasonSessionFiltered)
	// Empty candidates: expired (step 2) beats the structural abort (step 3).
	assert.Equal(t, mk(1, 2, 0, false).reason, wcprof.LookupReasonExpired)
	// Structural abort: input_unknown with the aborting index.
	got := mk(0, 2, 0, false)
	assert.Equal(t, got.reason, wcprof.LookupReasonInputUnknown)
	assert.Equal(t, got.inputIdx, 2)
	// Term matched, results gone.
	assert.Equal(t, mk(0, -1, 3, false).reason, wcprof.LookupReasonNoLiveCandidate)
	// Nothing matched at all.
	assert.Equal(t, mk(0, -1, 0, false).reason, wcprof.LookupReasonNoMatchingTerm)
}

func TestLookupOutcomeAndScopeEmit(t *testing.T) {
	baseCtx := cacheTestContext(t.Context())
	cacheIface, err := NewCache(baseCtx, "", nil, nil)
	assert.NilError(t, err)
	ctx := ContextWithCache(baseCtx, cacheIface)
	c := cacheIface
	srv := cacheTestServer(t)

	wcprof.EnsureRecorder()
	profCtx := wcprof.ContextWithProfiling(ctx)
	assert.NilError(t, wcprof.Active().WriteDump(&bytes.Buffer{}, true))

	mkCall := func(field string, implicit []*ResultCallArg) *ResultCall {
		return &ResultCall{
			Type: NewResultCallType(&ast.Type{
				NamedType: "CacheTestObject",
				NonNull:   true,
			}),
			Field:          field,
			ImplicitInputs: implicit,
		}
	}

	// (1) First demand of a fresh recipe: performs a lookup, nothing matches
	// anywhere → E1 "request no_matching_term"; the E2 scope fact records
	// the authoritative absence ("[]").
	missCall := mkCall("lookupMissTarget", nil)
	res1, err := c.GetOrInitCall(profCtx, "test-session", srv, &CallRequest{ResultCall: missCall}, func(context.Context) (AnyResult, error) {
		return cacheTestObjectResultWithValue(t, srv, missCall, &cacheTestObject{Value: 1}), nil
	})
	assert.NilError(t, err)

	// (2) Second demand: a usable hit → NO lookup-outcome fact.
	_, err = c.GetOrInitCall(profCtx, "test-session", srv, &CallRequest{ResultCall: missCall}, func(context.Context) (AnyResult, error) {
		t.Fatal("must be a cache hit")
		return nil, nil
	})
	assert.NilError(t, err)

	// (3) Expire the published result (white-box: TTL terminal), then
	// re-demand: candidate collection skips it, none remain → "request
	// expired" and the call re-executes.
	shared1 := res1.cacheSharedResult()
	assert.Assert(t, shared1 != nil)
	c.egraphMu.Lock()
	shared1.expiresAtUnix = 1 // long past
	c.egraphMu.Unlock()
	reExecuted := false
	_, err = c.GetOrInitCall(profCtx, "test-session", srv, &CallRequest{ResultCall: missCall}, func(context.Context) (AnyResult, error) {
		reExecuted = true
		return cacheTestObjectResultWithValue(t, srv, missCall, &cacheTestObject{Value: 2}), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, reExecuted, "an expired result must not serve")

	// (4) Session filtering: publish under one session, require a resource
	// handle the OTHER session lacks (white-box), demand from that other
	// session → candidates survive collection, none selectable → "request
	// session_filtered".
	sfCall := mkCall("lookupSessionFiltered", nil)
	resSF, err := c.GetOrInitCall(profCtx, "session-a", srv, &CallRequest{ResultCall: sfCall}, func(context.Context) (AnyResult, error) {
		return cacheTestObjectResultWithValue(t, srv, sfCall, &cacheTestObject{Value: 3}), nil
	})
	assert.NilError(t, err)
	sfShared := resSF.cacheSharedResult()
	assert.Assert(t, sfShared != nil)
	c.egraphMu.Lock()
	reqs := set.NewTreeSet(compareSessionResourceHandles)
	reqs.Insert(SessionResourceHandle("secret:only-session-a-has-this"))
	sfShared.requiredSessionResources = reqs
	c.egraphMu.Unlock()
	sfExecuted := false
	_, err = c.GetOrInitCall(profCtx, "session-b", srv, &CallRequest{ResultCall: sfCall}, func(context.Context) (AnyResult, error) {
		sfExecuted = true
		return cacheTestObjectResultWithValue(t, srv, sfCall, &cacheTestObject{Value: 4}), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, sfExecuted, "a session-filtered result must not serve session-b")

	// (5) do_not_cache: the micro-emit derives the ident best-effort, the
	// outcome is do_not_cache, and NO lookup-outcome fact exists (the call
	// never looks up). The result is a plain scalar: the do-not-cache path
	// refuses lazy and OnReleaser results by design.
	dncCall := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType(&ast.Type{NamedType: "String", NonNull: true}),
		Field: "lookupDNC",
	}
	_, err = c.GetOrInitCall(profCtx, "test-session", srv, &CallRequest{ResultCall: dncCall, DoNotCache: true}, func(context.Context) (AnyResult, error) {
		res, rerr := NewResultForCall(NewString("dnc-value"), dncCall)
		assert.NilError(t, rerr)
		return res, nil
	})
	assert.NilError(t, err)

	// (6) E2 scope inputs: a scoped call records names + emptiness; the
	// digest-pinned shape (empty value) is recorded as such.
	scopedCall := mkCall("lookupScoped", []*ResultCallArg{
		{Name: "cachePerSession", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindString, StringValue: "sess-1"}},
		{Name: "fromSessionScope", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindString, StringValue: ""}},
	})
	_, err = c.GetOrInitCall(profCtx, "test-session", srv, &CallRequest{ResultCall: scopedCall}, func(context.Context) (AnyResult, error) {
		return cacheTestObjectResultWithValue(t, srv, scopedCall, &cacheTestObject{Value: 6}), nil
	})
	assert.NilError(t, err)

	// (7) The digest-only entry: a never-seen digest → a lookup_outcome LINK
	// fact "digest_only no_matching_term" (this entry has no call op).
	_, ok, err := c.lookupCacheForDigests(profCtx, "test-session", srv, digest.Digest("xxh3:never-seen-digest"), nil)
	assert.NilError(t, err)
	assert.Assert(t, !ok)

	var buf bytes.Buffer
	assert.NilError(t, wcprof.Active().WriteDump(&buf, true))
	header, events, err := wcprof.ReadDump(bytes.NewReader(buf.Bytes()))
	assert.NilError(t, err)
	str := func(id uint32) string {
		if int(id) >= len(header.Strings) {
			return ""
		}
		return header.Strings[id]
	}

	// Collect the emitted facts per call class, in event order.
	type callFact struct {
		outcome string
		lookup  string
		scope   string
		ident   string
	}
	factsByClass := map[string][]callFact{}
	for _, ev := range events {
		if ev.Type != "op" || ev.OpKind != wcprof.OpKindCall.String() {
			continue
		}
		factsByClass[str(ev.ClassID)] = append(factsByClass[str(ev.ClassID)], callFact{
			outcome: ev.Outcome,
			lookup:  str(ev.LookupID),
			scope:   str(ev.ScopeID),
			ident:   str(ev.IdentID),
		})
	}

	target := factsByClass["Query.lookupMissTarget"]
	assert.Equal(t, len(target), 3, "executed, hit, re-executed-after-expiry")
	assert.Equal(t, target[0].outcome, wcprof.OutcomeExecuted.String())
	assert.Equal(t, target[0].lookup, "request no_matching_term")
	assert.Equal(t, target[0].scope, "[]", "E2 absence must be authoritative")
	assert.Equal(t, target[1].outcome, wcprof.OutcomeHit.String())
	assert.Equal(t, target[1].lookup, "", "a usable hit records no lookup-outcome fact")
	assert.Equal(t, target[2].outcome, wcprof.OutcomeExecuted.String())
	assert.Equal(t, target[2].lookup, "request expired", "the TTL terminal must be exact")

	sf := factsByClass["Query.lookupSessionFiltered"]
	assert.Equal(t, len(sf), 2)
	assert.Equal(t, sf[1].lookup, "request session_filtered")

	dnc := factsByClass["Query.lookupDNC"]
	assert.Equal(t, len(dnc), 1)
	assert.Equal(t, dnc[0].outcome, wcprof.OutcomeDoNotCache.String())
	assert.Assert(t, dnc[0].ident != "", "the micro-emit must make do_not_cache digest-addressable")
	assert.Equal(t, dnc[0].lookup, "", "do_not_cache never looks up")

	scoped := factsByClass["Query.lookupScoped"]
	assert.Equal(t, len(scoped), 1)
	assert.Equal(t, scoped[0].scope, `[{"n":"cachePerSession"},{"n":"fromSessionScope","e":true}]`,
		"E2 must record names + empty-value flags in the canonical encoding")

	// The digest-only fact link.
	var digestOnly []wcprof.DumpEvent
	for _, ev := range events {
		if ev.Type == "link" && ev.LinkKind == wcprof.LinkKindLookupOutcome.String() {
			digestOnly = append(digestOnly, ev)
		}
	}
	assert.Equal(t, len(digestOnly), 1)
	assert.Equal(t, str(digestOnly[0].IdentID), "xxh3:never-seen-digest")
	assert.Equal(t, str(digestOnly[0].MetaID), "digest_only no_matching_term")

	// The suppression counter stayed zero: every do_not_cache ident derived.
	assert.Equal(t, header.SuppressedDoNotCacheIdents, uint64(0))
}
