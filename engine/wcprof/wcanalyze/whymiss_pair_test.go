package wcanalyze

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dagger/dagger/engine/wcprof"
)

// Pair-mode validation rows W3, W4, W16b (+ the §5 pairing-contract units and
// the W11 boundary semantics). Expectations derived by reason before running,
// as ever.

// pairRefFixtureWithDep builds the reference capture (A) for W3 case (a):
// d-o was EXECUTED there (produced in a previous run).
func pairRefFixtureWithDep(t *testing.T, outcome string) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		opEvent(s, 2, 1, "call", "Query.dep", "d-o", outcome, 0, 100*ms),
	}
	return buildWhyGraph(t, s, events)
}

// pairQueryFixture builds the query capture (B): target d-x executed with the
// single input d-o, itself executed (a miss in B).
func pairQueryFixture(t *testing.T) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		opEvent(s, 2, 1, "call", "Query.dep", "d-o", "executed", 0, 100*ms),
		withInputs(t, s, opEvent(s, 3, 1, "call", "Container.build", "d-x", "executed", 100*ms, 400*ms), []string{"d-o"}),
		opEvent(s, 4, 3, "call_exec", "Container.build", "d-x", "ok", 100*ms, 400*ms),
		waitEvent(s, 3, 4, "", "call_exec", 100*ms, 400*ms),
	}
	return buildWhyGraph(t, s, events)
}

// --- W3: category 2 vs category 4 — the same B-side miss classifies as
// not-retained when the reference capture recorded the digest, and as new
// work when it did not; both answers name the searched history and never
// claim a lifetime mechanism (row W16's guard).
func TestWhyMissW3NotRetainedVsNewWork(t *testing.T) {
	gB := pairQueryFixture(t)

	// (a) A executed d-o → category 2. The digest-stable node is the origin
	// and the walk does not descend it; d-x is Merkle collateral.
	rep, err := RunWhyUncachedPair(gB, pairRefFixtureWithDep(t, "executed"), "d-x")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.PairMode {
		t.Fatal("report must be marked pair mode")
	}
	if len(rep.Origins) != 1 {
		t.Fatalf("want 1 origin, got %d", len(rep.Origins))
	}
	o := rep.Origins[0]
	if o.Node.Digest != "d-o" || o.Category != CategoryNotRetained {
		t.Fatalf("origin %s category %v, want d-o as not-retained (2)", o.Node.Digest, o.Category)
	}
	if !strings.Contains(o.Answer, "computed in a previous run") {
		t.Fatalf("category-2 answer must lead with the previous-run fact: %q", o.Answer)
	}
	if !strings.Contains(o.Answer, "which one applied here is not recorded") {
		t.Fatalf("category-2 answer must never claim a lifetime mechanism (W16): %q", o.Answer)
	}
	if !strings.Contains(o.Answer, "History searched: the one paired reference capture") {
		t.Fatalf("category-2 answer must name the searched history (W16): %q", o.Answer)
	}
	if rep.Collaterals != 1 {
		t.Fatalf("collaterals = %d, want 1 (d-x)", rep.Collaterals)
	}

	// (b) d-o absent from A → category 4, absence stated over exactly the
	// searched history.
	sA := newFixtureStrings()
	gA := buildWhyGraph(t, sA, []wcprof.DumpEvent{
		opEvent(sA, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
		opEvent(sA, 2, 1, "call", "Other.thing", "d-other", "executed", 0, 50*ms),
	})
	rep, err = RunWhyUncachedPair(gB, gA, "d-x")
	if err != nil {
		t.Fatal(err)
	}
	o = originByDigest(t, rep, "d-o")
	if o.Category != CategoryNewWork {
		t.Fatalf("absent-from-reference origin category %v, want new work (4)", o.Category)
	}
	if !strings.Contains(o.Answer, "first appearance") || !strings.Contains(o.Answer, "absent from the paired reference capture") {
		t.Fatalf("category-4 answer must state first appearance over the named history: %q", o.Answer)
	}

	// (c) A recorded d-o as failed-only → category 8 from the A side.
	rep, err = RunWhyUncachedPair(gB, pairRefFixtureWithDep(t, "error"), "d-x")
	if err != nil {
		t.Fatal(err)
	}
	o = originByDigest(t, rep, "d-o")
	if o.Category != CategoryPriorAttemptFailed {
		t.Fatalf("failed-only reference origin category %v, want prior-attempt-failed (8)", o.Category)
	}
	if !strings.Contains(o.Answer, "failures are not cached") {
		t.Fatalf("A-side category-8 answer must state the mechanism: %q", o.Answer)
	}
}

// pairedParentsFixtures builds the W4 shape: parent class P.build in both
// captures with different digests (p-A vs p-B), sharing the stable input d-s
// and differing in the C.dep input (d-cA vs d-cB). Vector order per side is
// the test's parameter, so reordering variants reuse it.
func pairedParentsFixtures(t *testing.T, vA, vB []string) (gB, gA *Graph) {
	t.Helper()
	sA := newFixtureStrings()
	eventsA := []wcprof.DumpEvent{
		opEvent(sA, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
		opEvent(sA, 2, 1, "call", "S.stable", "d-s", "executed", 0, 50*ms),
		opEvent(sA, 3, 1, "call", "C.dep", "d-cA", "executed", 50*ms, 150*ms),
		opEvent(sA, 4, 1, "call", "D.dep", "d-dA", "executed", 150*ms, 200*ms),
		withInputs(t, sA, opEvent(sA, 5, 1, "call", "P.build", "p-A", "executed", 200*ms, 700*ms), vA),
	}
	gA = buildWhyGraph(t, sA, eventsA)

	sB := newFixtureStrings()
	eventsB := []wcprof.DumpEvent{
		opEvent(sB, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
		opEvent(sB, 2, 1, "call", "S.stable", "d-s", "hit", 0, 10*ms),
		withInputs(t, sB, opEvent(sB, 3, 1, "call", "C.dep", "d-cB", "executed", 10*ms, 110*ms), []string{"d-s"}),
		opEvent(sB, 4, 1, "call", "D.dep", "d-dB", "executed", 110*ms, 160*ms),
		withInputs(t, sB, opEvent(sB, 5, 1, "call", "P.build", "p-B", "executed", 160*ms, 700*ms), vB),
		opEvent(sB, 6, 5, "call_exec", "P.build", "p-B", "ok", 160*ms, 700*ms),
		waitEvent(sB, 5, 6, "", "call_exec", 160*ms, 700*ms),
	}
	gB = buildWhyGraph(t, sB, eventsB)
	return gB, gA
}

// --- W4: category 3 — paired parents, input #k differs, the walk descends
// into it; positional pairing is asserted against deliberate reordering and
// class mismatch (structural-change report, never a guessed pair).
func TestWhyMissW4InputChanged(t *testing.T) {
	// In-order variant: vA=[d-cA, d-s], vB=[d-cB, d-s]. Derived: d-s anchors
	// by digest; the leftovers pair one-to-one by class (C.dep); the walk
	// descends into d-cB with pairA=d-cA; d-cB's own input d-s is a hit
	// boundary, so d-cB is the deepest changed node → category 3 naming the
	// divergence. p-B is collateral. The root partner is unambiguous (p-A is
	// the only P.build digest in A absent from B).
	gB, gA := pairedParentsFixtures(t, []string{"d-cA", "d-s"}, []string{"d-cB", "d-s"})
	rep, err := RunWhyUncachedPair(gB, gA, "p-B")
	if err != nil {
		t.Fatal(err)
	}
	o := originByDigest(t, rep, "d-cB")
	if o.Category != CategoryInputChanged {
		t.Fatalf("changed-input origin category %v, want input-changed (3)", o.Category)
	}
	if !strings.Contains(o.Answer, "d-cA -> d-cB") || !strings.Contains(o.Answer, "the change is in the call itself") {
		t.Fatalf("category-3 answer must name the concrete divergence: %q", o.Answer)
	}
	if o.Node.PairedWith != "d-cA" {
		t.Fatalf("d-cB paired with %q, want d-cA", o.Node.PairedWith)
	}
	changedLine := false
	for _, l := range rep.PairLines {
		if strings.Contains(l, "input #1 changed: d-cA -> d-cB") {
			changedLine = true
		}
	}
	if !changedLine {
		t.Fatalf("the parent's changed-input attribution must render, got %v", rep.PairLines)
	}

	// Reordering variant: vA=[d-s, d-cA], vB=[d-cB, d-s]. Derived: d-s still
	// anchors (order-preserving, unique); the leftovers now sit in DIFFERENT
	// gaps (d-cB before the anchor on B, d-cA after it on A), so no pair
	// forms across the anchor: d-cA reports as removed, d-cB as added → d-cB
	// classifies category 4, never category 3.
	gB, gA = pairedParentsFixtures(t, []string{"d-s", "d-cA"}, []string{"d-cB", "d-s"})
	rep, err = RunWhyUncachedPair(gB, gA, "p-B")
	if err != nil {
		t.Fatal(err)
	}
	o = originByDigest(t, rep, "d-cB")
	if o.Category != CategoryInputChanged && o.Category != CategoryNewWork {
		t.Fatalf("reordered variant: unexpected category %v", o.Category)
	}
	if o.Category == CategoryInputChanged {
		t.Fatalf("reordered inputs must NOT pair across the digest anchor (crossing): got category 3")
	}
	removed, added := false, false
	for _, l := range rep.PairLines {
		if strings.Contains(l, "input removed vs the reference capture: d-cA") {
			removed = true
		}
		if strings.Contains(l, "input added vs the reference capture: d-cB") {
			added = true
		}
	}
	if !removed || !added {
		t.Fatalf("reordering must report removal+addition, got %v", rep.PairLines)
	}

	// Class-mismatch variant: vA=[d-cA(C.dep), d-dA(D.dep)] vs
	// vB=[d-dB(D.dep), d-cB(C.dep)] — no digest anchors, equal leftover
	// counts, classes disagree pairwise → the §5 refusal line, and the
	// B-side inputs descend unpaired (category 4), never a guessed pair.
	gB, gA = pairedParentsFixtures(t, []string{"d-cA", "d-dA"}, []string{"d-dB", "d-cB"})
	rep, err = RunWhyUncachedPair(gB, gA, "p-B")
	if err != nil {
		t.Fatal(err)
	}
	refusal := false
	for _, l := range rep.PairLines {
		if strings.Contains(l, "structural change, not pairwise attributable") {
			refusal = true
		}
	}
	if !refusal {
		t.Fatalf("pairwise class mismatch must refuse into the structural-change line, got %v", rep.PairLines)
	}
	for _, o := range rep.Origins {
		if o.Category == CategoryInputChanged {
			t.Fatalf("no positional pair may form under a class mismatch; got category 3 for %s", o.Node.Digest)
		}
	}
}

// --- W16b: the duplicate-class counterexample — A=[C:d1, C:d2, D:d3] vs
// B=[C:d2, D:d3]. Digest anchors (d2, d3) win: d1 is exposed as the
// deletion, never paired to d2 by its class.
func TestWhyMissW16bDigestAnchorsBeatClassPairing(t *testing.T) {
	sA := newFixtureStrings()
	gA := buildWhyGraph(t, sA, []wcprof.DumpEvent{
		opEvent(sA, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
		opEvent(sA, 2, 1, "call", "C.dep", "d1", "executed", 0, 50*ms),
		opEvent(sA, 3, 1, "call", "C.dep", "d2", "executed", 50*ms, 100*ms),
		opEvent(sA, 4, 1, "call", "D.dep", "d3", "executed", 100*ms, 150*ms),
		withInputs(t, sA, opEvent(sA, 5, 1, "call", "P.build", "p-A", "executed", 150*ms, 700*ms), []string{"d1", "d2", "d3"}),
	})
	sB := newFixtureStrings()
	gB := buildWhyGraph(t, sB, []wcprof.DumpEvent{
		opEvent(sB, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
		opEvent(sB, 2, 1, "call", "C.dep", "d2", "hit", 0, 10*ms),
		opEvent(sB, 3, 1, "call", "D.dep", "d3", "hit", 10*ms, 20*ms),
		withInputs(t, sB, opEvent(sB, 4, 1, "call", "P.build", "p-B", "executed", 20*ms, 700*ms), []string{"d2", "d3"}),
		opEvent(sB, 5, 4, "call_exec", "P.build", "p-B", "ok", 20*ms, 700*ms),
		waitEvent(sB, 4, 5, "", "call_exec", 20*ms, 700*ms),
	})

	rep, err := RunWhyUncachedPair(gB, gA, "p-B")
	if err != nil {
		t.Fatal(err)
	}
	// Derived: p-B pairs with p-A (sole absent P.build in A); d2, d3 anchor
	// by digest and are hit boundaries in B; the pre-anchor gap has
	// A-leftover [d1] and empty B-leftover → d1 is a deletion line. p-B is
	// therefore the deepest changed node: origin, category 3.
	deletion := false
	for _, l := range rep.PairLines {
		if strings.Contains(l, "input removed vs the reference capture: d1") {
			deletion = true
		}
	}
	if !deletion {
		t.Fatalf("d1 must report as a deletion, got %v", rep.PairLines)
	}
	for _, o := range rep.Origins {
		if o.Node.Digest == "d2" || o.Node.Digest == "d3" {
			t.Fatalf("%s hit in B — a boundary, never an origin", o.Node.Digest)
		}
	}
	o := originByDigest(t, rep, "p-B")
	if o.Category != CategoryInputChanged {
		t.Fatalf("p-B category %v, want input-changed (3): its input delta IS the d1 deletion", o.Category)
	}
	if o.Node.PairedWith != "p-A" {
		t.Fatalf("p-B paired with %q, want p-A", o.Node.PairedWith)
	}
}

// --- §5 pairing-contract units: occurrence-level LCS uniqueness.
func TestLCSAnchorsOccurrenceUniqueness(t *testing.T) {
	// A=[X,X] vs B=[X]: two maximal anchor sets ((0,0) or (1,0)) — the
	// evidence cannot say which duplicate was removed → ambiguous, refused.
	if _, amb := lcsAnchorsUnique([]string{"X", "X"}, []string{"X"}); !amb {
		t.Fatal("repeated-digest partial overlap must be occurrence-ambiguous")
	}
	// A=[x,y] vs B=[y,x]: two distinct maximal subsequences (x or y) →
	// ambiguous.
	if _, amb := lcsAnchorsUnique([]string{"x", "y"}, []string{"y", "x"}); !amb {
		t.Fatal("two distinct maximal subsequences must be ambiguous")
	}
	// W16b's vectors: unique anchors (d2, d3).
	anchors, amb := lcsAnchorsUnique([]string{"d1", "d2", "d3"}, []string{"d2", "d3"})
	if amb || len(anchors) != 2 || anchors[0] != [2]int{1, 0} || anchors[1] != [2]int{2, 1} {
		t.Fatalf("W16b anchors = %v (amb=%v), want unique [(1,0),(2,1)]", anchors, amb)
	}
	// Identical vectors: fully anchored, unique.
	anchors, amb = lcsAnchorsUnique([]string{"a", "b"}, []string{"a", "b"})
	if amb || len(anchors) != 2 {
		t.Fatalf("identical vectors must anchor fully and uniquely, got %v amb=%v", anchors, amb)
	}
	// Repeated digests, equal counts: [X,X] vs [X,X] — the embedding is
	// forced (both anchor), unique.
	anchors, amb = lcsAnchorsUnique([]string{"X", "X"}, []string{"X", "X"})
	if amb || len(anchors) != 2 {
		t.Fatalf("equal repeated vectors must be unique, got %v amb=%v", anchors, amb)
	}
}

// The ambiguity refusal reaches the report: a paired parent whose input
// vectors are occurrence-ambiguous refuses the whole pairing with the stated
// line and descends unpaired.
func TestWhyMissPairAmbiguityRefusal(t *testing.T) {
	gB, gA := pairedParentsFixtures(t,
		[]string{"d-s", "d-s", "d-cA"}, // A: d-s twice
		[]string{"d-s", "d-cB"},        // B: d-s once → which duplicate was removed?
	)
	rep, err := RunWhyUncachedPair(gB, gA, "p-B")
	if err != nil {
		t.Fatal(err)
	}
	refused := false
	for _, l := range rep.PairLines {
		if strings.Contains(l, "ambiguous at occurrence level") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("occurrence ambiguity must refuse the pairing, got %v", rep.PairLines)
	}
	for _, o := range rep.Origins {
		if o.Category == CategoryInputChanged && o.Node.Digest != "p-B" {
			t.Fatalf("no positional pairs may form under an ambiguous anchor set (got one for %s)", o.Node.Digest)
		}
	}
}

// --- W11 (formal pinning; the first-demand rules were pinned with Chunk 1):
// all-hit and all-hit_pending digests are boundaries, pending rendered as a
// nuance — and the same digest-node semantics hold unchanged in pair mode.
func TestWhyMissW11Boundaries(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		opEvent(s, 2, 1, "call", "A.allhit", "d-allhit", "hit", 0, 10*ms),
		opEvent(s, 3, 1, "call", "A.allhit", "d-allhit", "hit", 20*ms, 30*ms),
		opEvent(s, 4, 1, "call", "B.allpend", "d-allpend", "hit_pending", 30*ms, 40*ms),
		withInputs(t, s, opEvent(s, 5, 1, "call", "T.target", "d-t", "executed", 50*ms, 400*ms), []string{"d-allhit", "d-allpend"}),
		opEvent(s, 6, 5, "call_exec", "T.target", "d-t", "ok", 50*ms, 400*ms),
		waitEvent(s, 5, 6, "", "call_exec", 50*ms, 400*ms),
	}
	g := buildWhyGraph(t, s, events)
	rep, err := RunWhyUncached(g, "d-t")
	if err != nil {
		t.Fatal(err)
	}
	// Derived: both inputs were cached at first demand — boundaries; the
	// target is the sole origin; the pending boundary renders its nuance.
	if len(rep.Origins) != 1 || rep.Origins[0].Node.Digest != "d-t" {
		t.Fatalf("want the target as sole origin, got %d origins", len(rep.Origins))
	}
	if rep.HitBoundaries != 2 || rep.PendingHitBoundaries != 1 {
		t.Fatalf("boundaries = %d (pending %d), want 2 (1 pending)", rep.HitBoundaries, rep.PendingHitBoundaries)
	}
	var buf bytes.Buffer
	rep.Write(&buf)
	if !strings.Contains(buf.String(), "hit_pending: recipe cached, first materialization owed") {
		t.Fatalf("pending boundary nuance must render:\n%s", buf.String())
	}
}

// Pair-mode admission: an incomplete REFERENCE capture refuses the walk —
// absence claims are only as good as the reference's completeness.
func TestWhyMissPairRefusesGatedReference(t *testing.T) {
	gB := pairQueryFixture(t)
	sA := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(sA, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
	}
	header := &wcprof.DumpHeader{
		SchemaVersion: wcprof.DumpSchemaVersion,
		Strings:       sA.values,
		EventCount:    len(events),
		DroppedEvents: 3,
	}
	gA, err := Build(header, events)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunWhyUncachedPair(gB, gA, "d-x"); err == nil ||
		!strings.Contains(err.Error(), "reference capture") || !strings.Contains(err.Error(), "REFUSED") {
		t.Fatalf("gated reference must refuse the pair walk, got %v", err)
	}
}

// OTel pairs refuse positional pairing (the §5 soundness boundary): stable
// analysis still answers, the caveat states the refusal, and no positional
// pair forms.
func TestWhyMissOTelPairRefusesPositional(t *testing.T) {
	gB, gA := pairedParentsFixtures(t, []string{"d-cA", "d-s"}, []string{"d-cB", "d-s"})
	gB.ResultIDsCaptureLocal = true // mark the query side OTel-sourced
	rep, err := RunWhyUncachedPair(gB, gA, "p-B")
	if err != nil {
		t.Fatal(err)
	}
	caveat := false
	for _, c := range rep.Caveats {
		if strings.Contains(c, "positional pairing REFUSED") {
			caveat = true
		}
	}
	if !caveat {
		t.Fatalf("OTel pair must carry the positional-refusal caveat, got %v", rep.Caveats)
	}
	for _, o := range rep.Origins {
		if o.Category == CategoryInputChanged {
			t.Fatalf("no positional pair may form on an OTel pair, got category 3 for %s", o.Node.Digest)
		}
		if o.Node.Digest == "d-cB" && o.Category != CategoryNewWork {
			t.Fatalf("d-cB should classify by digest identity only (absent → 4), got %v", o.Category)
		}
	}
}
