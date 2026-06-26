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

// --- (e) a fixed (named-resource) wait overlapping a child spawn must not gate
// the spawn either: the child is concurrent with the delay.
//
//	root [0,300]
//	└── P (call) [0,300]
//	     ├── (fixed wait on a lock) [50,200]
//	     └── U (call_exec) [100,200]   spawned during the lock wait
func TestFixedWaitOverlapsSpawn(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
		opEvent(s, 2, 1, "call", "P.call", "p", "executed", 0, 300*ms),
		opEvent(s, 3, 2, "call_exec", "U.work", "u", "ok", 100*ms, 200*ms),
		// target 0 + a non-exec reason + an ident matching no op ⇒ fixed delay.
		waitEvent(s, 2, 0, "some-lock", "lock", 50*ms, 200*ms),
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
	// P self [0,50] → clock 50; the fixed delay [50,200] runs concurrently with
	// U's spawn at 100, so U anchors at 50ms, not behind the delay's end.
	if uStart != 50*ms {
		t.Fatalf("U start = %v, want 50ms (a fixed wait overlapping the spawn must not gate it)", time.Duration(uStart))
	}
}

// --- (f) cross-root / out-of-order root reference: a wait in one root that
// targets an op under a later, not-yet-scheduled root anchors that op at its
// recorded start (counted), and never cycles.
func TestCrossRootAnchor(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 200*ms),
		waitEvent(s, 1, 2, "", "call_exec", 50*ms, 150*ms), // root1 waits an op under root2
		opEvent(s, 2, 0, "session_phase", "session.query2", "", "ok", 0, 150*ms),
	}
	g := buildGraph(t, s, events)
	sim := NewSimulation(g, nil)
	makespan, err := sim.Run()
	if err != nil {
		t.Fatal(err)
	}
	if sim.CycleWarnings != 0 {
		t.Fatalf("CycleWarnings = %d, want 0", sim.CycleWarnings)
	}
	if makespan <= 0 {
		t.Fatalf("makespan = %v, want > 0", time.Duration(makespan))
	}
	t.Logf("cross-root diagnostics: makespan=%v fallback-anchors=%d conflicts=%d",
		time.Duration(makespan), sim.FallbackAnchors, sim.SimStartConflicts)
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
