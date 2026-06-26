package wcanalyze

import (
	"testing"
	"time"

	"github.com/dagger/dagger/engine/wcprof"
)

// buildGraph is a tiny helper: assemble a graph from dump events.
func buildGraph(t *testing.T, s *fixtureStrings, events []wcprof.DumpEvent) *Graph {
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

// --- (a) config-parse: the out-of-order anchor must replay the parent's
// prefix WITH the counterfactual factor, not freeze the child at its recorded
// offset. Scaling a pre-spawn child's class to 0 must pull a later sibling
// (referenced out of order) earlier by the saved amount.
//
//	root (session.query) [0,500]
//	├── Lib.load   (call) [0,500] ── waits on App.compile [50,500]   (out-of-order ref)
//	└── App.build  (call) [0,500]
//	     ├── Config.parse (call_exec) [0,100]   self 100ms  (the scaled class)
//	     └── App.compile  (call_exec) [100,500] self 400ms  (spawned after Config.parse)
//	          App.build waits on App.compile [100,500]
//
// Lib.load (lower ID) is joined first, so App.compile is first reached out of
// order via Lib.load's wait → spawnTo(App.build, App.compile) replays
// App.build's prefix (Config.parse's self) up to App.compile's spawn. Freezing
// App.compile at its recorded offset (the rejected startOf model) would save
// 0ms; the prefix replay saves the full 100ms.
func buildConfigParseGraph(t *testing.T) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		opEvent(s, 2, 1, "call", "Lib.load", "lib", "executed", 0, 500*ms),
		waitEvent(s, 2, 7, "", "call_exec", 50*ms, 500*ms),
		opEvent(s, 5, 1, "call", "App.build", "app", "executed", 0, 500*ms),
		opEvent(s, 6, 5, "call_exec", "Config.parse", "cfg", "ok", 0, 100*ms),
		opEvent(s, 7, 5, "call_exec", "App.compile", "app", "ok", 100*ms, 500*ms),
		waitEvent(s, 5, 7, "", "call_exec", 100*ms, 500*ms),
	}
	return buildGraph(t, s, events)
}

func TestWhatIfConfigParsePrefixAnchor(t *testing.T) {
	g := buildConfigParseGraph(t)

	base := NewSimulation(g, nil)
	baseMakespan, err := base.Run()
	if err != nil {
		t.Fatal(err)
	}
	if baseMakespan != 500*ms {
		t.Fatalf("baseline makespan = %v, want 500ms", time.Duration(baseMakespan))
	}
	if base.CycleWarnings != 0 || base.FallbackAnchors != 0 || base.SimStartConflicts != 0 {
		t.Fatalf("baseline diagnostics: cycles=%d fallbacks=%d conflicts=%d, want all 0",
			base.CycleWarnings, base.FallbackAnchors, base.SimStartConflicts)
	}
	if base.PrefixAnchors == 0 {
		t.Fatalf("expected at least one out-of-order prefix anchor, got 0 (the fixture must exercise spawnTo)")
	}

	scaled := NewSimulation(g, map[ClassKey]float64{
		{Kind: "call_exec", Class: "Config.parse"}: 0,
	})
	makespan, err := scaled.Run()
	if err != nil {
		t.Fatal(err)
	}
	if saved := baseMakespan - makespan; saved != 100*ms {
		t.Fatalf("eliminating Config.parse saved %v, want 100ms (a frozen recorded-offset anchor would save 0)",
			time.Duration(saved))
	}
	if scaled.SimStartConflicts != 0 {
		t.Fatalf("scaled run had %d start conflicts, want 0 (anchor must stay order-independent under a factor)",
			scaled.SimStartConflicts)
	}
}

// --- (b) minimal false cycle: out-of-order references whose anchor, under the
// old full-parent-finish model, pulled in a LATER cross-reference wait and
// closed a spurious ring. Prefix-to-spawn stops before that wait, so no cycle.
//
//	root [0,600]
//	├── pA (call) [0,400] → A (call_exec) [100,200]   A waits X [150,180]
//	├── pB (call) [0,500] → B (call_exec) [100,400]   B spawns X [150,180]; B waits D [200,350]
//	│                        └── X (call_exec) [150,180]
//	└── pC (call) [0,600] → C (call_exec) [100,400]   C spawns D [150,350]
//	                         └── D (call_exec) [150,350]   D waits A [160,200]
//
// Anchoring X (referenced by A's wait) used to finish B fully, processing B's
// wait on D (200,350) → finish(D) → D waits A → finish(A) → A waits X →
// in-flight B → false cycle. B's wait on D follows X's spawn (150), so the
// prefix walk to X's spawn never touches it.
func buildMinCycleGraph(t *testing.T) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 600*ms),

		opEvent(s, 2, 1, "call", "Pa.call", "pa", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "A.work", "a", "ok", 100*ms, 200*ms),
		waitEvent(s, 3, 6, "", "call_exec", 150*ms, 180*ms), // A waits X

		opEvent(s, 4, 1, "call", "Pb.call", "pb", "executed", 0, 500*ms),
		opEvent(s, 5, 4, "call_exec", "B.work", "b", "ok", 100*ms, 400*ms),
		opEvent(s, 6, 5, "call_exec", "X.work", "x", "ok", 150*ms, 180*ms), // X is B's child
		waitEvent(s, 5, 9, "", "call_exec", 200*ms, 350*ms),                // B waits D (after X's spawn)

		opEvent(s, 7, 1, "call", "Pc.call", "pc", "executed", 0, 600*ms),
		opEvent(s, 8, 7, "call_exec", "C.work", "c", "ok", 100*ms, 400*ms),
		opEvent(s, 9, 8, "call_exec", "D.work", "d", "ok", 150*ms, 350*ms), // D is C's child
		waitEvent(s, 9, 3, "", "call_exec", 160*ms, 200*ms),                // D waits A
	}
	return buildGraph(t, s, events)
}

func TestMinCycleNoFalseCycle(t *testing.T) {
	g := buildMinCycleGraph(t)
	sim := NewSimulation(g, nil)
	makespan, err := sim.Run()
	if err != nil {
		t.Fatal(err)
	}
	// The whole structure is internally consistent, so a faithful replay
	// reproduces the recorded 600ms makespan with no broken cycles.
	if sim.CycleWarnings != 0 {
		t.Fatalf("CycleWarnings = %d, want 0 (the prefix anchor must not re-close the spurious ring)", sim.CycleWarnings)
	}
	if makespan != 600*ms {
		t.Fatalf("makespan = %v, want 600ms (a cycle-break would corrupt the finishes)", time.Duration(makespan))
	}
	if sim.SimStartConflicts != 0 {
		t.Fatalf("SimStartConflicts = %d, want 0", sim.SimStartConflicts)
	}
	// B is reached out of order while its grandparent (root) is mid-replay, so
	// it anchors at its recorded offset — counted, not silent. The recorded
	// offset is exact here (no shift), so it does not corrupt anything.
	t.Logf("diagnostics: prefix-anchors=%d fallback-anchors=%d", sim.PrefixAnchors, sim.FallbackAnchors)
}

// --- (c) dual semantics + order-independence: a wait concurrent with a child
// spawn gates the parent's FINISH but NOT the child's spawn, and the child's
// anchored start is identical whether the child is reached in order or out of
// order.
//
//	root [0,300]
//	├── A (call) [0,300]            A waits U [50,250]   (references U out of order)
//	└── P (call) [0,300]
//	     ├── W (call_exec) [50,200]   P waits W [50,200]  (open at U's spawn)
//	     └── U (call_exec) [150,250]  (spawned at 150, while W is still waiting)
//
// refFirst chooses the root-join order: A before P (U reached out of order via
// spawnTo) or P before A (U reached in order). Both must anchor U identically.
func buildConcurrentWaitGraph(t *testing.T, refFirst bool) *Graph {
	t.Helper()
	var aID, pID, wID, uID uint64
	if refFirst {
		aID, pID, wID, uID = 2, 5, 6, 7 // A joins first
	} else {
		pID, wID, uID, aID = 2, 3, 4, 5 // P joins first
	}
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(s, aID, 1, "call", "A.call", "a", "executed", 0, 300*ms),
		waitEvent(s, aID, uID, "", "call_exec", 50*ms, 250*ms), // A waits U
		opEvent(s, pID, 1, "call", "P.call", "p", "executed", 0, 300*ms),
		opEvent(s, wID, pID, "call_exec", "W.work", "w", "ok", 50*ms, 200*ms),
		opEvent(s, uID, pID, "call_exec", "U.work", "u", "ok", 150*ms, 250*ms),
		waitEvent(s, pID, wID, "", "call_exec", 50*ms, 200*ms), // P waits W (concurrent with U's spawn)
	}
	return buildGraph(t, s, events)
}

func TestConcurrentWaitOrderIndependent(t *testing.T) {
	var starts [2]int64
	for i, refFirst := range []bool{true, false} {
		g := buildConcurrentWaitGraph(t, refFirst)
		sim := NewSimulation(g, nil)
		if _, err := sim.Run(); err != nil {
			t.Fatal(err)
		}
		if sim.CycleWarnings != 0 || sim.SimStartConflicts != 0 {
			t.Fatalf("refFirst=%v: cycles=%d conflicts=%d, want 0/0", refFirst, sim.CycleWarnings, sim.SimStartConflicts)
		}
		uStart, _ := simByClass(sim, g, "U.work")
		_, pFinish := simByClass(sim, g, "P.call")
		// Dual semantics: U spawned at 150 while W [50,200] was still waiting,
		// so W does NOT gate U — U anchors at P's pre-W clock (50ms), not at
		// W's finish (200ms).
		if uStart != 50*ms {
			t.Fatalf("refFirst=%v: U start = %v, want 50ms (the concurrent wait W must not gate U's spawn)",
				refFirst, time.Duration(uStart))
		}
		// ...but W DOES gate P's own finish: P blocks on W until 200ms, then
		// finishes at 250ms. Without the gate P would finish at 150ms.
		if pFinish != 250*ms {
			t.Fatalf("refFirst=%v: P finish = %v, want 250ms (the concurrent wait W must gate P's finish)",
				refFirst, time.Duration(pFinish))
		}
		starts[i] = uStart
	}
	if starts[0] != starts[1] {
		t.Fatalf("U start depends on join order: out-of-order=%v in-order=%v (must be identical)",
			time.Duration(starts[0]), time.Duration(starts[1]))
	}
}

// --- (d) wait-end gating boundary: a wait gates a spawn iff the wait's OWN
// recorded end is <= the spawn's recorded time (not the target-end proxy, and
// the boundary is inclusive). The residual ring that motivated this involved a
// boundary spawn, so precision here matters.
//
//	root [0,300]
//	├── S (call) [0,300] → W (call_exec) [50,wEnd]   (W is the wait TARGET)
//	└── P (call) [0,300] → U (call_exec) [100,150]   P self [0,50]; P waits W [50,waitEnd]
//
// W is a sibling's child (not P's), so only the explicit wait — never an
// implicit child-join — can gate U. P's pre-wait clock at U's spawn is 50ms; a
// gating wait pulls U to W's finish instead.
func TestWaitEndGatingBoundary(t *testing.T) {
	cases := []struct {
		name      string
		wEnd      int64 // W's recorded end == W's finish (anchored at 50)
		waitEnd   int64 // the wait edge's OWN recorded end
		wantU     int64
		wantGated bool
	}{
		// A wait that is still open at the spawn must NOT drag U to W's late
		// finish (250ms): U stays concurrent at 50ms. This is the core item:
		// the target-end proxy would also catch this, but the danger is the
		// late finish leaking into the spawn.
		{"concurrent-late-finish", 250 * ms, 250 * ms, 50 * ms, false},
		// waitEnd == spawn time (100ms): inclusive boundary → gated; U = W's
		// finish (100ms).
		{"end-at-spawn-inclusive", 100 * ms, 100 * ms, 100 * ms, true},
		// The wait's OWN end (100.5ms) follows the spawn (100ms) even though the
		// TARGET end (100ms) coincides with it: own-end wins → NOT gated, U=50.
		// The retired target-end proxy would have (wrongly) gated this.
		{"own-end-past-spawn", 100 * ms, 100*ms + 500*int64(time.Microsecond), 50 * ms, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFixtureStrings()
			events := []wcprof.DumpEvent{
				opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
				opEvent(s, 2, 1, "call", "S.call", "s", "executed", 0, 300*ms),
				opEvent(s, 3, 2, "call_exec", "W.work", "w", "ok", 50*ms, tc.wEnd),
				opEvent(s, 4, 1, "call", "P.call", "p", "executed", 0, 300*ms),
				opEvent(s, 5, 4, "call_exec", "U.work", "u", "ok", 100*ms, 150*ms),
				waitEvent(s, 4, 3, "", "call_exec", 50*ms, tc.waitEnd), // P waits W
			}
			g := buildGraph(t, s, events)
			sim := NewSimulation(g, nil)
			if _, err := sim.Run(); err != nil {
				t.Fatal(err)
			}
			if sim.CycleWarnings != 0 || sim.SimStartConflicts != 0 {
				t.Fatalf("cycles=%d conflicts=%d, want 0/0", sim.CycleWarnings, sim.SimStartConflicts)
			}
			uStart, _ := simByClass(sim, g, "U.work")
			if uStart != tc.wantU {
				t.Fatalf("U start = %v, want %v (gated=%v)", time.Duration(uStart), time.Duration(tc.wantU), tc.wantGated)
			}
		})
	}
}

// --- (d2) zero-duration child exactly at a gating join wait's end. joinUpTo
// runs before the action tie-break, so without care the child (EndNS == the
// wait's end) would be implicitly anchored at the PRE-wait clock before the
// wait's own gate. That is a wrong start, and when the child is itself a wait
// target it propagates (see TestZeroDurWaitTargetPropagation). joinUpTo must
// instead DEFER a child whose spawn is still pending at this instant, so the
// gate (the wait end) raises the clock first and the spawn anchors it correctly.
//
//	root [0,300]
//	├── S (call) [0,300] → T (call_exec) [50,100]   (wait target, finish 100)
//	└── P (call) [0,100]   self [0,50]; waits T [50,100]; zero-dur child Z [100,100]
func TestZeroDurChildAtJoinWaitEnd(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(s, 2, 1, "call", "S.call", "sv", "executed", 0, 300*ms),
		opEvent(s, 3, 2, "call_exec", "T.work", "tt", "ok", 50*ms, 100*ms),
		opEvent(s, 4, 1, "call", "P.call", "p", "executed", 0, 100*ms),
		opEvent(s, 5, 4, "call_exec", "Z.work", "z", "ok", 100*ms, 100*ms),
		waitEvent(s, 4, 3, "", "call_exec", 50*ms, 100*ms),
	}
	g := buildGraph(t, s, events)
	sim := NewSimulation(g, nil)
	if _, err := sim.Run(); err != nil {
		t.Fatal(err)
	}
	if sim.CycleWarnings != 0 {
		t.Fatalf("CycleWarnings = %d, want 0", sim.CycleWarnings)
	}
	// Z spawns AFTER P's wait on T (which gates to 100), so Z anchors at the
	// gated clock 100, not the pre-wait 50 — deferred past the gate, no conflict.
	zStart, _ := simByClass(sim, g, "Z.work")
	if zStart != 100*ms {
		t.Fatalf("Z start = %v, want 100ms (gated by the wait, not anchored early at the pre-wait clock)", time.Duration(zStart))
	}
	_, pFinish := simByClass(sim, g, "P.call")
	if pFinish != 100*ms {
		t.Fatalf("P finish = %v, want 100ms", time.Duration(pFinish))
	}
	// The early-anchor/re-anchor that produced this conflict is now eliminated,
	// so SimStartConflicts is a clean signal here.
	if sim.SimStartConflicts != 0 {
		t.Fatalf("SimStartConflicts = %d, want 0 (the join-before-spawn early anchor is fixed)", sim.SimStartConflicts)
	}
}

// TestZeroDurWaitTargetPropagation is the showstopper the above guards against:
// a zero-duration child Z that is itself a WAIT TARGET, anchored early, feeds a
// wrong finish into a downstream wait chain → wrong makespan, with no
// FallbackAnchor to flag it. P waits T (gates to 100) then spawns zero-dur Z at
// 100; B waits Z; A waits B then runs 200ms self; root reaches B before P so Z is
// anchored out of order. The bug anchored Z at P's pre-wait clock (50) → B=50,
// A=250, makespan 250. Correct: Z=100, B=100, A=300, makespan 300.
func TestZeroDurWaitTargetPropagation(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(s, 2, 1, "call", "A.crit", "a", "executed", 0, 300*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 100*ms), // A waits B
		opEvent(s, 3, 1, "call", "B.mid", "b", "executed", 0, 100*ms),
		waitEvent(s, 3, 6, "", "call_exec", 0, 100*ms), // B waits Z
		opEvent(s, 5, 1, "call", "P.parent", "p", "executed", 0, 100*ms),
		opEvent(s, 6, 5, "call_exec", "Z.zero", "z", "ok", 100*ms, 100*ms), // zero-dur child of P
		waitEvent(s, 5, 7, "", "call_exec", 50*ms, 100*ms),                 // P waits T
		opEvent(s, 7, 1, "call_exec", "T.work", "tt", "ok", 0, 100*ms),     // real self so finish(T)=100 > P's pre-wait 50
	}
	g := buildGraph(t, s, events)
	sim := NewSimulation(g, nil)
	makespan, err := sim.Run()
	if err != nil {
		t.Fatal(err)
	}
	if sim.CycleWarnings != 0 || sim.SimStartConflicts != 0 {
		t.Fatalf("cycles=%d conflicts=%d, want 0/0", sim.CycleWarnings, sim.SimStartConflicts)
	}
	zFinish := func(c string) int64 { _, f := simByClass(sim, g, c); return f }
	if z := zFinish("Z.zero"); z != 100*ms {
		t.Fatalf("Z finish = %v, want 100ms (the wait-target zero-dur child must not anchor early)", time.Duration(z))
	}
	if b := zFinish("B.mid"); b != 100*ms {
		t.Fatalf("B finish = %v, want 100ms (Z's wrong-early finish must not propagate)", time.Duration(b))
	}
	if makespan != 300*ms {
		t.Fatalf("makespan = %v, want 300ms (the early-anchor bug gives 250ms)", time.Duration(makespan))
	}
}

// TestTwoZeroDurCoEnding: two zero-duration children co-ending at a gating wait's
// end, where one waits on the other. The defer returns out of joinUpTo at the
// first, but both are re-joined after their spawns, so both anchor at the gated
// clock (100, not the pre-wait clock) and the Z2→Z1 dependency is honored. Guards
// the low/theoretical residual the replay owner flagged.
func TestTwoZeroDurCoEnding(t *testing.T) {
	s := newFixtureStrings()
	g := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(s, 2, 1, "call", "S.call", "sv", "executed", 0, 300*ms),
		opEvent(s, 3, 2, "call_exec", "T.work", "tt", "ok", 0, 100*ms),
		opEvent(s, 4, 1, "call", "P.call", "p", "executed", 0, 100*ms),
		waitEvent(s, 4, 3, "", "call_exec", 50*ms, 100*ms), // P waits T (gates to 100)
		opEvent(s, 5, 4, "call_exec", "Z1.zero", "z1", "ok", 100*ms, 100*ms),
		opEvent(s, 6, 4, "call_exec", "Z2.zero", "z2", "ok", 100*ms, 100*ms),
		waitEvent(s, 6, 5, "", "call_exec", 100*ms, 100*ms), // Z2 waits Z1
	})
	sim := NewSimulation(g, nil)
	if _, err := sim.Run(); err != nil {
		t.Fatal(err)
	}
	z1, _ := simByClass(sim, g, "Z1.zero")
	z2, _ := simByClass(sim, g, "Z2.zero")
	if z1 != 100*ms || z2 != 100*ms {
		t.Fatalf("Z1=%v Z2=%v, want both 100ms (gated, not pre-wait-anchored)", time.Duration(z1), time.Duration(z2))
	}
	if sim.SimStartConflicts != 0 || sim.CycleWarnings != 0 || sim.FallbackAnchors != 0 {
		t.Fatalf("faithfulness signals nonzero: conflicts=%d cycles=%d fallbacks=%d, want 0/0/0",
			sim.SimStartConflicts, sim.CycleWarnings, sim.FallbackAnchors)
	}
}

// TestZeroDurChildAtSelfStart is the second face of the same fix: a zero-duration
// child spawned at the START of a self segment (not a wait) must anchor
// concurrently with that self, not serialized after it. Only a zero-duration
// child can share a spawn instant with a self-segment start (a normal child's
// interval carves the self out of that point), so the spawn-before-self rank
// only changes this case.
//
//	P (call) [0,200]
//	├── C (call_exec) [0,100]       carves [0,100] so self starts at 100
//	├── self [100,200]
//	└── Z (call_exec) [100,100]     zero-dur, spawned at the self start
//
// Z is concurrent with the self → anchors at 100. The old self-before-spawn rank
// (with the deferral) anchored it after the self at 200.
func TestZeroDurChildAtSelfStart(t *testing.T) {
	s := newFixtureStrings()
	g := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 200*ms),
		opEvent(s, 2, 1, "call", "P.call", "p", "executed", 0, 200*ms),
		opEvent(s, 3, 2, "call_exec", "C.work", "c", "ok", 0, 100*ms),
		opEvent(s, 4, 2, "call_exec", "Z.zero", "z", "ok", 100*ms, 100*ms),
	})
	sim := NewSimulation(g, nil)
	if _, err := sim.Run(); err != nil {
		t.Fatal(err)
	}
	if sim.CycleWarnings != 0 || sim.SimStartConflicts != 0 {
		t.Fatalf("cycles=%d conflicts=%d, want 0/0", sim.CycleWarnings, sim.SimStartConflicts)
	}
	zStart, _ := simByClass(sim, g, "Z.zero")
	if zStart != 100*ms {
		t.Fatalf("Z start = %v, want 100ms (concurrent with the self segment, not serialized after it)", time.Duration(zStart))
	}
}

// --- (e) a fixed (lock / named-resource) delay is a non-scalable segment that
// runs CONCURRENTLY with the op's other work. Modelling it as clock += dur (an
// additive jump) instead of max(clock, start-clock + dur) serializes a child
// that ran during the delay behind it — over-serializing the op's finish. Both
// the child-finishes-inside and child-spawned-during cases must stay concurrent.
func TestFixedWaitConcurrentChild(t *testing.T) {
	// Case 1 — child spawned BEFORE the lock, finishes INSIDE it.
	//   O (session) [0,100]   self [0,5] + [40,100]
	//   ├── c (call_exec) [5,35]
	//   └── lock [10,40]   (dur 30)
	// O blocks on the lock [10,40] while c runs concurrently to 35, then resumes:
	// 5 + 30 (lock, overlapping c) + 60 = 95. An additive clock += dur after c's
	// join would give 125.
	t.Run("child-finishes-inside", func(t *testing.T) {
		s := newFixtureStrings()
		g := buildGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			opEvent(s, 2, 1, "call_exec", "C.work", "c", "ok", 5*ms, 35*ms),
			waitEvent(s, 1, 0, "some-lock", "lock", 10*ms, 40*ms),
		})
		sim := NewSimulation(g, nil)
		finish, err := sim.Run()
		if err != nil {
			t.Fatal(err)
		}
		if sim.CycleWarnings != 0 || sim.SimStartConflicts != 0 {
			t.Fatalf("cycles=%d conflicts=%d, want 0/0", sim.CycleWarnings, sim.SimStartConflicts)
		}
		if finish != 95*ms {
			t.Fatalf("O finish = %v, want 95ms", time.Duration(finish))
		}
	})

	// Case 2 — child SPAWNED during the lock (concurrent fan-out while blocked).
	//   P (call) [0,300]   self [0,50] + [200,300]
	//   ├── U (call_exec) [100,200]   spawned at 100, during the lock
	//   └── lock [50,200]   (dur 150)
	// U is concurrent with the lock: it anchors at P's pre-lock clock (50ms) and
	// finishes at 150, well before P's post-lock self. P's critical path is
	// self(50) → lock(150)=200 → self(100) = 300. Serializing U behind the lock
	// (anchor 200 → finish 300 → +100 self) would give the wrong 400.
	t.Run("child-spawned-during", func(t *testing.T) {
		s := newFixtureStrings()
		g := buildGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
			opEvent(s, 2, 1, "call", "P.call", "p", "executed", 0, 300*ms),
			opEvent(s, 3, 2, "call_exec", "U.work", "u", "ok", 100*ms, 200*ms),
			waitEvent(s, 2, 0, "some-lock", "lock", 50*ms, 200*ms),
		})
		sim := NewSimulation(g, nil)
		if _, err := sim.Run(); err != nil {
			t.Fatal(err)
		}
		if sim.CycleWarnings != 0 || sim.SimStartConflicts != 0 {
			t.Fatalf("cycles=%d conflicts=%d, want 0/0", sim.CycleWarnings, sim.SimStartConflicts)
		}
		uStart, _ := simByClass(sim, g, "U.work")
		_, pFinish := simByClass(sim, g, "P.call")
		if uStart != 50*ms {
			t.Fatalf("U start = %v, want 50ms (U must stay concurrent with the lock, not serialized behind it)", time.Duration(uStart))
		}
		if pFinish != 300*ms {
			t.Fatalf("P finish = %v, want 300ms (serializing U behind the lock gives the wrong 400ms)", time.Duration(pFinish))
		}
	})
}

// --- (f) Case (a): concurrent cross-root singleflight dedup. Two independent
// roots start at 0; R_B loads the module, R_A dedups onto it via a RECORDED
// cross-root wait. The data is sufficient and the rational model needs no
// chaining and no fallback — both roots anchor at their recorded starts (exact),
// and the saving propagates across the root boundary through the wait edge.
//
//	R_A (session.A) [0,300]   self [0,50]; waits T [50,300]
//	R_B (session.B) [0,300]   setup self [0,100]; spawns T
//	                          └── T (call_exec ModuleLoad) [100,300]  self 200
//
// Baseline 300. Scale R_B's setup → 0: T spawns at 0, runs 0→200, R_A's wait
// unblocks at 200 → makespan 200, a 100ms saving crossing the root boundary.
// FallbackAnchors / SimStartConflicts / CycleWarnings are 0 BY CONSTRUCTION.
func TestCrossRootDedup(t *testing.T) {
	build := func() *Graph {
		s := newFixtureStrings()
		return buildGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.A", "", "ok", 0, 300*ms),
			waitEvent(s, 1, 3, "", "call_exec", 50*ms, 300*ms), // R_A waits R_B's T (cross-root)
			opEvent(s, 2, 0, "session_phase", "session.B", "", "ok", 0, 300*ms),
			opEvent(s, 3, 2, "call_exec", "ModuleLoad", "m", "ok", 100*ms, 300*ms),
		})
	}
	g := build()

	base := NewSimulation(g, nil)
	makespan, err := base.Run()
	if err != nil {
		t.Fatal(err)
	}
	if makespan != 300*ms {
		t.Fatalf("baseline makespan = %v, want 300ms", time.Duration(makespan))
	}
	if base.FallbackAnchors != 0 || base.SimStartConflicts != 0 || base.CycleWarnings != 0 {
		t.Fatalf("faithfulness signals nonzero: fallbacks=%d conflicts=%d cycles=%d, want 0/0/0 by construction",
			base.FallbackAnchors, base.SimStartConflicts, base.CycleWarnings)
	}

	scaled := NewSimulation(build(), map[ClassKey]float64{
		{Kind: "session_phase", Class: "session.B"}: 0,
	})
	m2, err := scaled.Run()
	if err != nil {
		t.Fatal(err)
	}
	if m2 != 200*ms {
		t.Fatalf("scaled makespan = %v, want 200ms (the saving must cross the root boundary through the recorded wait)", time.Duration(m2))
	}
	if scaled.FallbackAnchors != 0 || scaled.SimStartConflicts != 0 {
		t.Fatalf("scaled faithfulness signals nonzero: fallbacks=%d conflicts=%d, want 0/0",
			scaled.FallbackAnchors, scaled.SimStartConflicts)
	}
}

// --- (g) fan-out: one parent with many children each referenced out of order
// by a distinct sibling. Exercises the prefix-anchor path at width and confirms
// it neither cycles nor disagrees with the in-order finish.
func TestFanOutOutOfOrderAnchors(t *testing.T) {
	const fanout = 64
	const parentID = 9999 // higher than every ref, so the refs join first
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(s, parentID, 1, "call", "Parent.call", "parent", "executed", 0, 1000*ms),
	}
	// Parent spawns N children; N lower-ID referencer siblings each wait on one,
	// so every child is reached out of order (refs join before Parent) and
	// anchored by a fresh prefix walk of Parent — the O(refs × prefix) path.
	var id uint64 = 100
	for k := range fanout {
		childID := id
		refID := id + 1
		id += 2
		start := int64(k) * ms
		end := start + 500*ms
		events = append(events,
			opEvent(s, childID, parentID, "call_exec", "Child.work", "child", "ok", start, end),
			opEvent(s, refID, 1, "call", "Ref.call", "ref", "executed", 0, 1000*ms),
			waitEvent(s, refID, childID, "", "call_exec", 0, end),
		)
	}
	g := buildGraph(t, s, events)
	sim := NewSimulation(g, nil)
	if _, err := sim.Run(); err != nil {
		t.Fatal(err)
	}
	if sim.CycleWarnings != 0 || sim.SimStartConflicts != 0 {
		t.Fatalf("cycles=%d conflicts=%d, want 0/0", sim.CycleWarnings, sim.SimStartConflicts)
	}
	if sim.PrefixAnchors == 0 {
		t.Fatalf("expected out-of-order prefix anchors, got 0 (fixture must exercise the fan-out path)")
	}
	t.Logf("fan-out diagnostics: prefix-anchors=%d fallback-anchors=%d", sim.PrefixAnchors, sim.FallbackAnchors)
}

// simByClass returns the simulated start/finish of the (single) op with the
// given class name.
func simByClass(sim *Simulation, g *Graph, class string) (startNS, finishNS int64) {
	for _, op := range g.Ops {
		if op.Class == class {
			return sim.SimTimes(op)
		}
	}
	return 0, 0
}
