package wcanalyze

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dagger/dagger/engine/wcprof"
)

// Cache-invalidation tracing validation rows W1, W2, W6, W7, W9 (design:
// hack/designs/cache-invalidation-tracing-design.md §9) plus the §3.1
// digest-node status rules the walk rests on. Every test derives its
// expected outcome by reason BEFORE the walk runs, then asserts it exactly
// (whatif doctrine §0.4, inherited verbatim).

// withInputs stamps a call op event with recorded CacheInputs (the canonical
// JSON-array encoding both sources emit).
func withInputs(t *testing.T, s *fixtureStrings, ev wcprof.DumpEvent, inputs []string) wcprof.DumpEvent {
	t.Helper()
	b, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	ev.InputsID = s.id(string(b))
	return ev
}

// withScope stamps a call op event with recorded scope implicit inputs (what
// the OTel loader decodes from dag.call).
func withScope(t *testing.T, s *fixtureStrings, ev wcprof.DumpEvent, scope []ScopeInput) wcprof.DumpEvent {
	t.Helper()
	b, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	ev.ScopeID = s.id(string(b))
	return ev
}

func buildWhyGraph(t *testing.T, s *fixtureStrings, events []wcprof.DumpEvent) *Graph {
	t.Helper()
	header := &wcprof.DumpHeader{
		SchemaVersion: wcprof.DumpSchemaVersion,
		Strings:       s.values,
		EventCount:    len(events),
	}
	g, err := Build(header, events)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func originByDigest(t *testing.T, rep *WhyMissReport, digest string) *WhyMissOrigin {
	t.Helper()
	for _, o := range rep.Origins {
		if o.Node.Digest == digest {
			return o
		}
	}
	t.Fatalf("no origin %s in report (origins: %d)", digest, len(rep.Origins))
	return nil
}

// whyChainFixture is the W1 three-level miss chain:
//
//	R [0,1000]
//	├── h   call d-h  hit       [0,50]                       (hit boundary)
//	├── b1  call d-b1 executed  [50,150]   inputs [d-h]      (deepest origin)
//	├── b2  call d-b2 executed  [150,250]  inputs [d-h]      (deepest origin)
//	├── a   call d-a  executed  [250,550]  inputs [d-b1,d-b2] (collateral)
//	└── x   call d-x  executed  [550,950]  inputs [d-a]       (target, collateral)
//
// Derived by reason: the walk from x descends x -> a -> {b1, b2} -> h. b1 and
// b2 each have only hit inputs, so the frontier = {b1, b2}; x and a missed
// only because inputs below them missed — Merkle collateral, listed on the
// path, never as origins. h is one hit boundary node.
func whyChainFixture(t *testing.T) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(s, 2, 1, "call", "Container.from", "d-h", "hit", 0, 50*ms),
		withInputs(t, s, opEvent(s, 3, 1, "call", "Container.withExec", "d-b1", "executed", 50*ms, 150*ms), []string{"d-h"}),
		opEvent(s, 4, 3, "call_exec", "Container.withExec", "d-b1", "ok", 50*ms, 150*ms),
		waitEvent(s, 3, 4, "", "call_exec", 50*ms, 150*ms),
		withInputs(t, s, opEvent(s, 5, 1, "call", "Container.withExec", "d-b2", "executed", 150*ms, 250*ms), []string{"d-h"}),
		opEvent(s, 6, 5, "call_exec", "Container.withExec", "d-b2", "ok", 150*ms, 250*ms),
		waitEvent(s, 5, 6, "", "call_exec", 150*ms, 250*ms),
		withInputs(t, s, opEvent(s, 7, 1, "call", "Container.withDirectory", "d-a", "executed", 250*ms, 550*ms), []string{"d-b1", "d-b2"}),
		opEvent(s, 8, 7, "call_exec", "Container.withDirectory", "d-a", "ok", 250*ms, 550*ms),
		waitEvent(s, 7, 8, "", "call_exec", 250*ms, 550*ms),
		withInputs(t, s, opEvent(s, 9, 1, "call", "Container.build", "d-x", "executed", 550*ms, 950*ms), []string{"d-a"}),
		opEvent(s, 10, 9, "call_exec", "Container.build", "d-x", "ok", 550*ms, 950*ms),
		waitEvent(s, 9, 10, "", "call_exec", 550*ms, 950*ms),
	}
	return buildWhyGraph(t, s, events)
}

// --- W1: frontier walk on a synthetic three-level miss chain: frontier =
// the two deepest origins, collaterals listed on the path, never as origins.
func TestWhyMissW1FrontierWalk(t *testing.T) {
	g := whyChainFixture(t)
	rep, err := RunWhyUncached(g, "d-x")
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Target.Status; got != MissStatusMissed {
		t.Fatalf("target status = %v, want missed", got)
	}
	if len(rep.Origins) != 2 {
		t.Fatalf("want 2 frontier origins, got %d", len(rep.Origins))
	}
	for _, o := range rep.Origins {
		if o.Node.Digest != "d-b1" && o.Node.Digest != "d-b2" {
			t.Fatalf("unexpected origin %s: collaterals must never be origins", o.Node.Digest)
		}
		if !o.Priced {
			t.Fatalf("origin %s not priced: %s (executed eligible digest must price)", o.Node.Digest, o.PriceRefusal)
		}
		// The walk from x through a reaches this origin: x, a, and the
		// origin itself are the 3 misses it explains.
		if o.PathMisses != 3 {
			t.Fatalf("origin %s PathMisses = %d, want 3 (x, a, origin)", o.Node.Digest, o.PathMisses)
		}
	}
	if rep.Collaterals != 2 {
		t.Fatalf("collaterals = %d, want 2 (d-x, d-a)", rep.Collaterals)
	}
	if rep.HitBoundaries != 1 {
		t.Fatalf("hit boundaries = %d, want 1 (d-h)", rep.HitBoundaries)
	}
	if rep.NodesWalked != 4 {
		t.Fatalf("nodes walked = %d, want 4 (x, a, b1, b2)", rep.NodesWalked)
	}
	// The path renders target -> collateral -> origin, in that order.
	b1 := originByDigest(t, rep, "d-b1")
	path := renderPath(rep.Target, b1.Node)
	wantOrder := []string{"d-x", "d-a", "d-b1"}
	last := -1
	for _, d := range wantOrder {
		i := strings.Index(path, d)
		if i < 0 || i < last {
			t.Fatalf("path %q does not list %v in order", path, wantOrder)
		}
		last = i
	}
	// Native capture: no OTel caveats.
	if len(rep.Caveats) != 0 {
		t.Fatalf("native capture must carry no OTel caveats, got %v", rep.Caveats)
	}
}

// --- W2: category 1 — a scoped call answers with the per-scope why-text; a
// digest-pinned `from` (fromSessionScope recorded EMPTY) is NOT category 1;
// a capture with no scope structure recorded stays undetermined with the
// recording gap stated.
func TestWhyMissW2DeliberatelyScoped(t *testing.T) {
	build := func(scope []ScopeInput, record bool) *Graph {
		s := newFixtureStrings()
		hit := opEvent(s, 2, 1, "call", "Container.from", "d-h", "hit", 0, 20*ms)
		tgt := withInputs(t, s, opEvent(s, 3, 1, "call", "Query.moduleSource", "d-t", "executed", 20*ms, 200*ms), []string{"d-h"})
		if record {
			tgt = withScope(t, s, tgt, scope)
		}
		events := []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
			hit, tgt,
			opEvent(s, 4, 3, "call_exec", "Query.moduleSource", "d-t", "ok", 20*ms, 200*ms),
			waitEvent(s, 3, 4, "", "call_exec", 20*ms, 200*ms),
		}
		return buildWhyGraph(t, s, events)
	}

	// Scoped: cachePerSession recorded with a (non-empty) value. Derived:
	// the target itself is the sole origin; category 1 with the session
	// why-text — a first-class expected-miss answer.
	rep, err := RunWhyUncached(build([]ScopeInput{{Name: "cachePerSession"}}, true), "d-t")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Origins) != 1 {
		t.Fatalf("want 1 origin, got %d", len(rep.Origins))
	}
	o := rep.Origins[0]
	if o.Category != CategoryDeliberatelyScoped {
		t.Fatalf("category = %v, want deliberately scoped", o.Category)
	}
	if !strings.Contains(o.Answer, "deliberately does not cache this across sessions") {
		t.Fatalf("scoped answer missing the per-scope why-text: %q", o.Answer)
	}
	if !strings.Contains(o.ScopeNote, "cachePerSession") {
		t.Fatalf("scope note must name the deciding input: %q", o.ScopeNote)
	}

	// Digest-pinned from: fromSessionScope recorded with an EMPTY value —
	// the engine deliberately did NOT scope this path (container.go
	// :1032-1034), so claiming category 1 would be a false scoping claim.
	// Derived: undetermined form, with the empty-valued input stated.
	rep, err = RunWhyUncached(build([]ScopeInput{{Name: "fromSessionScope", EmptyValue: true}}, true), "d-t")
	if err != nil {
		t.Fatal(err)
	}
	o = rep.Origins[0]
	if o.Category != CategoryUndetermined {
		t.Fatalf("pinned-from category = %v, want undetermined (NOT category 1)", o.Category)
	}
	if o.Answer != "no cached result existed under this key; cause not recorded in this capture." {
		t.Fatalf("undetermined form must be the exact stated sentence, got %q", o.Answer)
	}
	if !strings.Contains(o.ScopeNote, "fromSessionScope") || !strings.Contains(o.ScopeNote, "deliberately not scoping") {
		t.Fatalf("pinned-from scope note must state the recorded-empty input: %q", o.ScopeNote)
	}

	// Scope structure not recorded (today's native shape): undetermined with
	// the recording gap named — never guessed either way.
	rep, err = RunWhyUncached(build(nil, false), "d-t")
	if err != nil {
		t.Fatal(err)
	}
	o = rep.Origins[0]
	if o.Category != CategoryUndetermined {
		t.Fatalf("unrecorded-scope category = %v, want undetermined", o.Category)
	}
	if !strings.Contains(o.ScopeNote, "scope structure not recorded in this capture") {
		t.Fatalf("unrecorded-scope note must name the gap: %q", o.ScopeNote)
	}

	// The rendered report carries the undetermined sentence verbatim.
	var buf bytes.Buffer
	rep.Write(&buf)
	if !strings.Contains(buf.String(), "no cached result existed under this key; cause not recorded in this capture") {
		t.Fatalf("report must print the undetermined form:\n%s", buf.String())
	}
}

// whyOutcomeFixture is the W6 shape:
//
//	R [0,1000]
//	├── h2   call d-h2  hit         [80,100]                   (boundary)
//	├── dnc  call d-dnc do_not_cache [0,100]   inputs [d-deep] (origin, cat 7)
//	├── deep call d-deep executed   [0,50]                     (must NOT be walked: below a refusal)
//	├── f1   call d-f   error       [100,200]  inputs [d-h2]
//	├── f2   call d-f   executed    [250,400]  inputs [d-h2]   (origin, cat 8: failed-then-re-executed)
//	├── fj   call d-f   joined      [260,400]                  (nuance)
//	├── p    call d-p   hit_pending [600,650]                  (boundary; pending is a nuance, not a miss)
//	└── x    call d-x   executed    [700,950]  inputs [d-dnc, d-f, d-p]  (target, collateral)
func whyOutcomeFixture(t *testing.T) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(s, 2, 1, "call", "Container.from", "d-h2", "hit", 80*ms, 100*ms),
		withInputs(t, s, opEvent(s, 3, 1, "call", "Query.secretValue", "d-dnc", "do_not_cache", 0, 100*ms), []string{"d-deep"}),
		opEvent(s, 4, 1, "call", "Query.deep", "d-deep", "executed", 0, 50*ms),
		withInputs(t, s, opEvent(s, 5, 1, "call", "Directory.flaky", "d-f", "error", 100*ms, 200*ms), []string{"d-h2"}),
		withInputs(t, s, opEvent(s, 6, 1, "call", "Directory.flaky", "d-f", "executed", 250*ms, 400*ms), []string{"d-h2"}),
		opEvent(s, 7, 6, "call_exec", "Directory.flaky", "d-f", "ok", 250*ms, 400*ms),
		waitEvent(s, 6, 7, "", "call_exec", 250*ms, 400*ms),
		opEvent(s, 8, 1, "call", "Directory.flaky", "d-f", "joined", 260*ms, 400*ms),
		opEvent(s, 9, 1, "call", "Container.lazyThing", "d-p", "hit_pending", 600*ms, 650*ms),
		withInputs(t, s, opEvent(s, 10, 1, "call", "Container.build", "d-x", "executed", 700*ms, 950*ms), []string{"d-dnc", "d-f", "d-p"}),
		opEvent(s, 11, 10, "call_exec", "Container.build", "d-x", "ok", 700*ms, 950*ms),
		waitEvent(s, 10, 11, "", "call_exec", 700*ms, 950*ms),
	}
	return buildWhyGraph(t, s, events)
}

// --- W6: categories 7/8 decided from recorded outcomes; hit_pending and
// joined render as nuances, never as origins; a do-not-cache refusal
// terminates its path (the answer is complete — nothing below can change it).
func TestWhyMissW6OutcomeCategories(t *testing.T) {
	g := whyOutcomeFixture(t)
	rep, err := RunWhyUncached(g, "d-x")
	if err != nil {
		t.Fatal(err)
	}
	// Derived: origins = {d-dnc (7), d-f (8)}; x is collateral; d-p and
	// d-h2 are boundaries; d-deep is below a refusal and never walked.
	if len(rep.Origins) != 2 {
		t.Fatalf("want 2 origins, got %d", len(rep.Origins))
	}

	dnc := originByDigest(t, rep, "d-dnc")
	if dnc.Category != CategoryEngineRefuses {
		t.Fatalf("d-dnc category = %v, want engine-refuses (7)", dnc.Category)
	}
	if !strings.Contains(dnc.Answer, "never cached (do-not-cache)") || !strings.Contains(dnc.Answer, "expected miss") {
		t.Fatalf("category-7 answer must state the refusal as an expected miss: %q", dnc.Answer)
	}
	if dnc.Priced {
		t.Fatalf("category-7 origin must not price: simulating a do_not_cache call cached is fiction (V14)")
	}
	if !strings.Contains(dnc.PriceRefusal, "do_not_cache") {
		t.Fatalf("price refusal must name the reason: %q", dnc.PriceRefusal)
	}

	f := originByDigest(t, rep, "d-f")
	if f.Category != CategoryPriorAttemptFailed {
		t.Fatalf("d-f category = %v, want prior-attempt-failed (8)", f.Category)
	}
	if !strings.Contains(f.Answer, "failures are not cached") {
		t.Fatalf("category-8 answer must state the mechanism: %q", f.Answer)
	}
	if !strings.Contains(f.Answer, "call op 5") || !strings.Contains(f.Answer, "call op 6") {
		t.Fatalf("category-8 answer must print the deciding ops: %q", f.Answer)
	}
	joinedNote := false
	for _, n := range f.Notes {
		if strings.Contains(n, "joined") && strings.Contains(n, "nuance") {
			joinedNote = true
		}
	}
	if !joinedNote {
		t.Fatalf("joined call must render as a nuance note, got %v", f.Notes)
	}
	if !f.Priced {
		t.Fatalf("d-f has a successful execution, must price: %s", f.PriceRefusal)
	}

	// d-deep is below the do-not-cache refusal: never walked, never an origin.
	if rep.NodesWalked != 3 {
		t.Fatalf("nodes walked = %d, want 3 (x, dnc, f): a refusal terminates its path", rep.NodesWalked)
	}
	// d-p (pending hit) and d-h2 are hit boundaries; d-p's pending state is
	// a nuance rendered on the boundary line (review round 1, finding 4).
	if rep.HitBoundaries != 2 {
		t.Fatalf("hit boundaries = %d, want 2 (d-p pending nuance + d-h2)", rep.HitBoundaries)
	}
	if rep.PendingHitBoundaries != 1 {
		t.Fatalf("pending-hit boundaries = %d, want 1 (d-p)", rep.PendingHitBoundaries)
	}
	var buf bytes.Buffer
	rep.Write(&buf)
	if !strings.Contains(buf.String(), "hit_pending: recipe cached, first materialization owed") {
		t.Fatalf("pending boundary nuance must render:\n%s", buf.String())
	}
	if rep.Collaterals != 1 {
		t.Fatalf("collaterals = %d, want 1 (d-x)", rep.Collaterals)
	}
}

// Category-8 negative case (review round 1, finding 1): a failure followed by
// a SUCCESSFUL execution followed by a re-execution is NOT category 8 — a
// published result existed before the re-demand, so "failures are not
// cached" is not derivable; the honest answer is the mechanism-unrecorded
// re-execution note on an undetermined origin.
func TestWhyMissCategory8RequiresNoInterveningSuccess(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		// Demand order: success [0,100], failure [150,250], re-exec [300,400].
		// The re-exec's predecessors include a SUCCESS, so category 8 must
		// not fire anywhere on this digest.
		opEvent(s, 2, 1, "call", "A.flaky", "d-sfr", "executed", 0, 100*ms),
		opEvent(s, 3, 1, "call", "A.flaky", "d-sfr", "error", 150*ms, 250*ms),
		opEvent(s, 4, 1, "call", "A.flaky", "d-sfr", "executed", 300*ms, 400*ms),
	}
	g := buildWhyGraph(t, s, events)
	rep, err := RunWhyUncached(g, "d-sfr")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Origins) != 1 {
		t.Fatalf("want 1 origin, got %d", len(rep.Origins))
	}
	o := rep.Origins[0]
	if o.Category == CategoryPriorAttemptFailed {
		t.Fatalf("category 8 fired despite an intervening successful publish — not derivable from the data")
	}
	if o.Category != CategoryUndetermined {
		t.Fatalf("category = %v, want undetermined", o.Category)
	}
	if !o.Node.ReExecutedAfterSuccess {
		t.Fatalf("the re-execution after a successful publish must be flagged (mechanism-unrecorded note)")
	}
}

// Corrupted scope evidence (review round 1, finding 3): a recorded-but-
// undecodable dag.call is labeled as corruption, never conflated with
// "not recorded" — and never guessed into a category.
func TestWhyMissCorruptScopeLabeled(t *testing.T) {
	s := newFixtureStrings()
	tgt := opEvent(s, 2, 1, "call", "Query.moduleSource", "d-t", "executed", 0, 100*ms)
	tgt.ScopeID = s.id(wcprof.ScopeMalformedSentinel)
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 200*ms),
		tgt,
	}
	g := buildWhyGraph(t, s, events)
	rep, err := RunWhyUncached(g, "d-t")
	if err != nil {
		t.Fatal(err)
	}
	o := rep.Origins[0]
	if o.Category != CategoryUndetermined {
		t.Fatalf("corrupt scope must stay undetermined, got %v", o.Category)
	}
	if !strings.Contains(o.ScopeNote, "malformed") || strings.Contains(o.ScopeNote, "not recorded in this capture (native") {
		t.Fatalf("corrupt scope must be labeled as corruption, not absence: %q", o.ScopeNote)
	}
}

// --- W7: priced impact — the origin's saving equals the what-if detail run
// for the same digest, exactly (the pricing IS the simulator's answer, gate
// contract included).
func TestWhyMissW7PriceEqualsDetailRun(t *testing.T) {
	g := whyChainFixture(t)
	rep, err := RunWhyUncached(g, "d-x")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range rep.Origins {
		detail, err := RunCachedDetail(g, NewCachedHypothesis([]string{o.Node.Digest}, 0), 5)
		if err != nil {
			t.Fatal(err)
		}
		if gerr := detail.GateErr(); gerr != nil {
			t.Fatal(gerr)
		}
		wantSaved := detail.BaselineNS - detail.MakespanNS
		if !o.Priced || o.SavedNS != wantSaved {
			t.Fatalf("origin %s priced %v/%d, want exactly the detail run's saving %d",
				o.Node.Digest, o.Priced, o.SavedNS, wantSaved)
		}
	}
}

// --- W9: refusals — a gated capture refuses the walk (any dropped event
// could have been the call or demand evidence the answer depends on), and
// selection errors are loud.
func TestWhyMissW9Refusals(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
		opEvent(s, 2, 1, "call", "Container.build", "d-x", "executed", 0, 100*ms),
	}
	header := &wcprof.DumpHeader{
		SchemaVersion: wcprof.DumpSchemaVersion,
		Strings:       s.values,
		EventCount:    len(events),
		DroppedEvents: 1,
	}
	g, err := Build(header, events)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunWhyUncached(g, "d-x"); err == nil || !strings.Contains(err.Error(), "REFUSED") {
		t.Fatalf("gated capture must refuse the walk, got %v", err)
	}

	// Unknown target digest: a loud error, never a silent no-op.
	g2 := whyChainFixture(t)
	if _, err := RunWhyUncached(g2, "d-nope"); err == nil || !strings.Contains(err.Error(), "no recorded call") {
		t.Fatalf("unknown target must be a loud error, got %v", err)
	}
	var buf bytes.Buffer
	err = WriteWhyUncached(&buf, g2, WhyUncachedSelection{Digests: []string{"d-nope"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("selection of an unknown digest must error, got %v", err)
	}
	err = WriteWhyUncached(&buf, g2, WhyUncachedSelection{Classes: []string{"No.suchClass"}})
	if err == nil || !strings.Contains(err.Error(), "matches no uncached call digest") {
		t.Fatalf("class matching nothing must error, got %v", err)
	}
}

// --- §3.1 digest-node status rules (the walk's foundation; formally pinned
// as W11 with pair mode in Chunk 2): first-demand ordering decides, with the
// op-id tie-break; executed-then-hit is missed-at-first-demand (the
// within-run norm, NOT context-dependent); hit-then-executed is
// context-dependent, reported, walked as a miss.
func TestWhyMissDigestNodeStatus(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		// d-eh: executed [0,100] then hit [200,210] — missed at first demand.
		opEvent(s, 2, 1, "call", "A.a", "d-eh", "executed", 0, 100*ms),
		opEvent(s, 3, 1, "call", "A.a", "d-eh", "hit", 200*ms, 210*ms),
		// d-he: hit [0,50] then executed [300,400] — context-dependent.
		opEvent(s, 4, 1, "call", "B.b", "d-he", "hit", 0, 50*ms),
		opEvent(s, 5, 1, "call", "B.b", "d-he", "executed", 300*ms, 400*ms),
		// d-ee: executed [0,100], executed again [500,600] — re-executed
		// after success (mechanism unrecorded), missed at first demand.
		opEvent(s, 6, 1, "call", "C.c", "d-ee", "executed", 0, 100*ms),
		opEvent(s, 7, 1, "call", "C.c", "d-ee", "executed", 500*ms, 600*ms),
		// d-tie: two calls with the SAME start — op id breaks the tie, so
		// the hit (lower id 8) decides: cached at first demand.
		opEvent(s, 8, 1, "call", "D.d", "d-tie", "hit", 700*ms, 710*ms),
		opEvent(s, 9, 1, "call", "D.d", "d-tie", "executed", 700*ms, 800*ms),
	}
	g := buildWhyGraph(t, s, events)
	w := newWhyMissWalk(g)

	eh := w.node("d-eh")
	if eh.Status != MissStatusMissed || eh.ContextDependent {
		t.Fatalf("executed-then-hit: status=%v ctx=%v, want missed-at-first-demand, not context-dependent", eh.Status, eh.ContextDependent)
	}
	he := w.node("d-he")
	if he.Status != MissStatusCached || !he.ContextDependent || !he.WalkedAsMiss() {
		t.Fatalf("hit-then-executed: status=%v ctx=%v walked=%v, want cached + context-dependent + walked as miss",
			he.Status, he.ContextDependent, he.WalkedAsMiss())
	}
	ee := w.node("d-ee")
	if ee.Status != MissStatusMissed || !ee.ReExecutedAfterSuccess {
		t.Fatalf("re-executed after success: status=%v reexec=%v, want missed + flagged", ee.Status, ee.ReExecutedAfterSuccess)
	}
	tie := w.node("d-tie")
	if tie.Status != MissStatusCached {
		t.Fatalf("tie-break: status=%v, want cached (op id 8 wins the equal-start tie)", tie.Status)
	}
	if !tie.ContextDependent {
		t.Fatalf("tie-break node: the later executed call reverses the first status — context-dependent")
	}
}

// A target that WAS cached at first demand (and never reversed) reports
// exactly that, with nothing to trace — the honest no-op answer.
func TestWhyMissCachedTarget(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
		opEvent(s, 2, 1, "call", "A.a", "d-hit", "hit", 0, 10*ms),
	}
	g := buildWhyGraph(t, s, events)
	rep, err := RunWhyUncached(g, "d-hit")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Target.Status != MissStatusCached || len(rep.Origins) != 0 {
		t.Fatalf("cached target: status=%v origins=%d, want cached with no origins", rep.Target.Status, len(rep.Origins))
	}
	var buf bytes.Buffer
	rep.Write(&buf)
	if !strings.Contains(buf.String(), "WAS served from cache at first demand") {
		t.Fatalf("report must state the hit:\n%s", buf.String())
	}
}

// OTel-sourced graphs carry the two §3.1 source caveats (first-emission-only
// evidence; module-blind dag.inputs) — printed on every walk, since pre-E3
// even the walk is partial there.
func TestWhyMissOTelCaveats(t *testing.T) {
	g := whyChainFixture(t)
	g.ResultIDsCaptureLocal = true // the OTel-source marker
	rep, err := RunWhyUncached(g, "d-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Caveats) != 2 {
		t.Fatalf("want the 2 OTel caveats, got %v", rep.Caveats)
	}
	joined := strings.Join(rep.Caveats, "\n")
	if !strings.Contains(joined, "first-emission-only") || !strings.Contains(joined, "module-ref edges are not recorded") {
		t.Fatalf("caveats must name seen-key suppression and module blindness: %v", rep.Caveats)
	}
}
