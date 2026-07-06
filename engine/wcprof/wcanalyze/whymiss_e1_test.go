package wcanalyze

import (
	"strings"
	"testing"

	"github.com/dagger/dagger/engine/wcprof"
)

// Analyzer-side rows W5, W14 (consumption half), W15 for the E1 lookup-
// outcome facts (invalidation-tracing design §4, categories 5/6/9).
// Expectations reason-derived before running, as ever.

func withLookup(t *testing.T, s *fixtureStrings, ev wcprof.DumpEvent, entry, reason string, idx int) wcprof.DumpEvent {
	t.Helper()
	ev.LookupID = s.id(wcprof.EncodeLookupOutcome(entry, reason, idx))
	return ev
}

// --- W5: E1 fixtures answer categories 5/6 (and 9) EXACTLY; the same
// fixtures WITHOUT the fact answer the undetermined form — never a guessed
// 5/6.
func TestWhyMissW5TerminalFacts(t *testing.T) {
	build := func(reason string, withFact bool) *Graph {
		s := newFixtureStrings()
		outcome := "executed"
		if reason == wcprof.LookupReasonPersistedLoadFailed {
			// The hit-unusable arm errors on this engine build.
			outcome = "error"
		}
		tgt := opEvent(s, 2, 1, "call", "Container.build", "d-t", outcome, 0, 200*ms)
		if withFact {
			tgt = withLookup(t, s, tgt, wcprof.LookupEntryRequest, reason, -1)
		}
		return buildWhyGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
			tgt,
		})
	}

	for _, tc := range []struct {
		reason   string
		category WhyMissCategory
		text     string
	}{
		{wcprof.LookupReasonExpired, CategoryExpired, "TTL had expired"},
		{wcprof.LookupReasonSessionFiltered, CategorySessionFiltered, "session resources"},
		{wcprof.LookupReasonPersistedLoadFailed, CategoryHitUnusable, "persisted payload could not be loaded"},
	} {
		rep, err := RunWhyUncached(build(tc.reason, true), "d-t")
		if err != nil {
			t.Fatal(err)
		}
		o := rep.Origins[0]
		if o.Category != tc.category {
			t.Fatalf("%s: category %v, want %v", tc.reason, o.Category, tc.category)
		}
		if !strings.Contains(o.Answer, tc.text) || !strings.Contains(o.Answer, "call op 2") {
			t.Fatalf("%s: answer must state the terminal and the deciding op: %q", tc.reason, o.Answer)
		}

		// The SAME shape without the fact: undetermined, never guessed.
		rep, err = RunWhyUncached(build(tc.reason, false), "d-t")
		if err != nil {
			t.Fatal(err)
		}
		o = rep.Origins[0]
		if o.Category == tc.category {
			t.Fatalf("%s without E1 must not claim the exact category", tc.reason)
		}
		if o.Category != CategoryUndetermined {
			t.Fatalf("%s without E1: category %v, want undetermined", tc.reason, o.Category)
		}
	}
}

// --- W14 (consumption half; the terminal-derivation precedence is pinned
// engine-side in dagql): input_unknown names the authoritative next hop;
// no_live_candidate upgrades the reversal note to the recorded terminal;
// digest-only facts classify when unambiguous and are labeled when
// conflicting.
func TestWhyMissW14FactConsumption(t *testing.T) {
	// input_unknown(k): the note names input #k's digest.
	s := newFixtureStrings()
	tgt := withInputs(t, s,
		withLookup(t, s, opEvent(s, 2, 1, "call", "Container.build", "d-t", "executed", 0, 200*ms),
			wcprof.LookupEntryRequest, wcprof.LookupReasonInputUnknown, 1),
		[]string{"d-a", "d-b"})
	g := buildWhyGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		tgt,
	})
	rep, err := RunWhyUncached(g, "d-t")
	if err != nil {
		t.Fatal(err)
	}
	// d-a and d-b have no recorded calls → unrecorded leaves → the target is
	// the origin, with the next-hop note naming input #1 = d-b.
	o := originByDigest(t, rep, "d-t")
	hop := false
	for _, n := range o.Notes {
		if strings.Contains(n, "input #1") && strings.Contains(n, "d-b") && strings.Contains(n, "authoritative next hop") {
			hop = true
		}
	}
	if !hop {
		t.Fatalf("input_unknown must name the next hop, got %v", o.Notes)
	}

	// no_live_candidate on a re-executed digest: the recorded terminal
	// upgrades the note; the mechanism text stays unrecorded (design §3.1).
	s = newFixtureStrings()
	g = buildWhyGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 800*ms),
		opEvent(s, 2, 1, "call", "A.a", "d-r", "executed", 0, 100*ms),
		withLookup(t, s, opEvent(s, 3, 1, "call", "A.a", "d-r", "executed", 300*ms, 400*ms),
			wcprof.LookupEntryRequest, wcprof.LookupReasonNoLiveCandidate, -1),
	})
	rep, err = RunWhyUncached(g, "d-r")
	if err != nil {
		t.Fatal(err)
	}
	o = rep.Origins[0]
	terminal := false
	for _, n := range o.Notes {
		if strings.Contains(n, "no-live-candidate") && strings.Contains(n, "which applied is not recorded") {
			terminal = true
		}
	}
	if !terminal {
		t.Fatalf("no_live_candidate must upgrade the reversal note, got %v", o.Notes)
	}
	if o.Category != CategoryUndetermined {
		t.Fatalf("no_live_candidate is a hint, not a category: got %v", o.Category)
	}

	// Digest-only facts: unambiguous → exact category; conflicting → labeled,
	// never picked from.
	s = newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(s, 2, 1, "call", "Container.build", "d-t", "executed", 0, 200*ms),
		{Type: "link", LinkKind: wcprof.LinkKindLookupOutcome.String(), ParentID: 1,
			IdentID: s.id("d-t"), MetaID: s.id(wcprof.EncodeLookupOutcome(wcprof.LookupEntryDigestOnly, wcprof.LookupReasonExpired, -1))},
	}
	rep, err = RunWhyUncached(buildWhyGraph(t, s, events), "d-t")
	if err != nil {
		t.Fatal(err)
	}
	o = rep.Origins[0]
	if o.Category != CategoryExpired || !strings.Contains(o.Answer, "digest-only lookup fact") {
		t.Fatalf("unambiguous digest-only fact must classify with its datum named: %v %q", o.Category, o.Answer)
	}

	s = newFixtureStrings()
	events = []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(s, 2, 1, "call", "Container.build", "d-t", "executed", 0, 200*ms),
		{Type: "link", LinkKind: wcprof.LinkKindLookupOutcome.String(), ParentID: 1,
			IdentID: s.id("d-t"), MetaID: s.id(wcprof.EncodeLookupOutcome(wcprof.LookupEntryDigestOnly, wcprof.LookupReasonExpired, -1))},
		{Type: "link", LinkKind: wcprof.LinkKindLookupOutcome.String(), ParentID: 1,
			IdentID: s.id("d-t"), MetaID: s.id(wcprof.EncodeLookupOutcome(wcprof.LookupEntryDigestOnly, wcprof.LookupReasonSessionFiltered, -1))},
	}
	rep, err = RunWhyUncached(buildWhyGraph(t, s, events), "d-t")
	if err != nil {
		t.Fatal(err)
	}
	o = rep.Origins[0]
	if o.Category != CategoryUndetermined {
		t.Fatalf("conflicting digest-only facts must not classify, got %v", o.Category)
	}
	labeled := false
	for _, n := range o.Notes {
		if strings.Contains(n, "CONFLICTING") {
			labeled = true
		}
	}
	if !labeled {
		t.Fatalf("conflicting facts must be labeled, got %v", o.Notes)
	}
}

// --- W15: mixed-outcome digests decide per the PER-DIGEST summary rules,
// never by single-op sampling: a digest with any do_not_cache call is
// category 7 even when its first demand executed; failed-then-executed is
// category 8 (pinned since Chunk 1; the cross-capture flavor in W3(c)).
func TestWhyMissW15MixedOutcomeSummaryRules(t *testing.T) {
	s := newFixtureStrings()
	g := buildWhyGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		// First demand executed, a LATER call is do_not_cache: the recipe's
		// static refusal decides regardless of demand order.
		opEvent(s, 2, 1, "call", "Query.mixed", "d-m", "executed", 0, 100*ms),
		opEvent(s, 3, 1, "call", "Query.mixed", "d-m", "do_not_cache", 150*ms, 250*ms),
	})
	rep, err := RunWhyUncached(g, "d-m")
	if err != nil {
		t.Fatal(err)
	}
	o := rep.Origins[0]
	if o.Category != CategoryEngineRefuses {
		t.Fatalf("any-dnc digest must classify category 7 (per-digest rule), got %v", o.Category)
	}
	if !strings.Contains(o.Answer, "call op 3") {
		t.Fatalf("the deciding datum must be the do_not_cache op: %q", o.Answer)
	}
	// Pricing refuses the fiction (V14): the eligibility layer's own
	// any-dnc rule agrees with the walk's.
	if o.Priced {
		t.Fatalf("a do-not-cache digest must not price")
	}
}

// W15's cross-capture flavor: a digest-stable node whose REFERENCE side is
// do-not-cache-mixed decides category 7 (the static refusal), never
// category 2 — the per-digest summary rule crosses captures.
func TestWhyMissW15StableReferenceDNCMixed(t *testing.T) {
	sB := newFixtureStrings()
	gB := buildWhyGraph(t, sB, []wcprof.DumpEvent{
		opEvent(sB, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(sB, 2, 1, "call", "Query.mixed", "d-m", "executed", 0, 100*ms),
	})
	sA := newFixtureStrings()
	gA := buildWhyGraph(t, sA, []wcprof.DumpEvent{
		opEvent(sA, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(sA, 2, 1, "call", "Query.mixed", "d-m", "executed", 0, 100*ms),
		opEvent(sA, 3, 1, "call", "Query.mixed", "d-m", "do_not_cache", 120*ms, 200*ms),
	})
	rep, err := RunWhyUncachedPair(gB, gA, "d-m")
	if err != nil {
		t.Fatal(err)
	}
	o := rep.Origins[0]
	if o.Category != CategoryEngineRefuses {
		t.Fatalf("stable dnc-mixed reference must decide category 7, got %v (answer %q)", o.Category, o.Answer)
	}
	if !strings.Contains(o.Answer, "do_not_cache") {
		t.Fatalf("the answer must print the reference tally: %q", o.Answer)
	}
}

// A malformed input_unknown fact (no index) never becomes evidence: the
// decode rejects it, so the op carries no lookup fact at all.
func TestWhyMissMalformedFactRejected(t *testing.T) {
	s := newFixtureStrings()
	tgt := opEvent(s, 2, 1, "call", "Container.build", "d-t", "executed", 0, 200*ms)
	tgt.LookupID = s.id("request input_unknown") // missing required index
	g := buildWhyGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		tgt,
	})
	if op := g.Ops[2]; op.LookupReason != "" || op.LookupInputIdx != -1 {
		t.Fatalf("malformed fact must not decode: %q/%d", op.LookupReason, op.LookupInputIdx)
	}
	rep, err := RunWhyUncached(g, "d-t")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Origins[0].Category != CategoryUndetermined {
		t.Fatalf("malformed fact must leave the origin undetermined, got %v", rep.Origins[0].Category)
	}
}

// The do-not-cache ident-suppression caveat surfaces on walks (a counted
// degradation, never a refusal).
func TestWhyMissDNCSuppressionCaveat(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(s, 2, 1, "call", "Container.build", "d-t", "executed", 0, 200*ms),
	}
	header := &wcprof.DumpHeader{
		SchemaVersion:              wcprof.DumpSchemaVersion,
		Strings:                    s.values,
		EventCount:                 len(events),
		SuppressedDoNotCacheIdents: 2,
	}
	g, err := Build(header, events)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := RunWhyUncached(g, "d-t")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range rep.Caveats {
		if strings.Contains(c, "do-not-cache call(s) could not derive an ident") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the DNC suppression caveat must print, got %v", rep.Caveats)
	}
}
