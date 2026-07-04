package wcanalyze

import (
	"testing"
	"time"

	"github.com/dagger/dagger/engine/wcprof"
)

// What-if-cached validation catalog rows V1–V16 (design:
// hack/designs/whatif-cached-design.md §4.5). Every test states its expected
// outcome derived by reason BEFORE the simulator runs, then asserts it
// exactly (doctrine §0.4). Fixtures are synthetic graphs in the established
// style; recorded times are chosen so the baseline replay reproduces them
// with zero drift, keeping every expectation derivable by hand.

func cachedRes(t *testing.T, g *Graph, pullNS int64, idents ...string) *CachedResolution {
	t.Helper()
	return ResolveCachedHypothesis(g, NewCachedHypothesis(idents, pullNS))
}

func runSim(t *testing.T, sim *Simulation) int64 {
	t.Helper()
	m, err := sim.Run()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// assertFaithful asserts all four data-faithfulness gate signals are zero —
// required on every faithful fixture, cached or not.
func assertFaithful(t *testing.T, sim *Simulation) {
	t.Helper()
	if sim.CycleWarnings != 0 || sim.UnschedulableOps != 0 || sim.SimStartConflicts != 0 || sim.ElidedOpDemanded != 0 {
		t.Fatalf("faithfulness signals nonzero: cycles=%d unschedulable=%d conflicts=%d elided-demanded=%d, want all 0",
			sim.CycleWarnings, sim.UnschedulableOps, sim.SimStartConflicts, sim.ElidedOpDemanded)
	}
}

// assertSameSchedule asserts two completed simulations produced bit-for-bit
// identical per-op schedules.
func assertSameSchedule(t *testing.T, g *Graph, a, b *Simulation) {
	t.Helper()
	for _, op := range g.Ops {
		as, af := a.SimTimes(op)
		bs, bf := b.SimTimes(op)
		if as != bs || af != bf {
			t.Fatalf("op %d (%s %s): schedules diverge: [%v,%v] vs [%v,%v]",
				op.ID, op.Kind, op.Class,
				time.Duration(as), time.Duration(af), time.Duration(bs), time.Duration(bf))
		}
	}
}

func identElig(t *testing.T, res *CachedResolution, ident string) *IdentEligibility {
	t.Helper()
	for i := range res.Idents {
		if res.Idents[i].Ident == ident {
			return &res.Idents[i]
		}
	}
	t.Fatalf("ident %s not in resolution: %+v", ident, res.Idents)
	return nil
}

// --- V1: empty CachedSet on any graph is bit-for-bit the baseline. Nothing
// was hypothesized, so nothing may differ. An unknown digest resolves to
// IdentNotFound and is equally a no-op.
func TestCachedBaselineInvariance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T) *Graph
	}{
		{"sequential", sequentialFixture},
		{"service-shared", buildExternalDemandGraph},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.build(t)
			base := NewSimulation(g, nil)
			baseMakespan := runSim(t, base)

			for _, idents := range [][]string{nil, {"no-such-digest"}} {
				res := cachedRes(t, g, 0, idents...)
				if !res.Noop() {
					t.Fatalf("hypothesis %v must resolve to a no-op, got %+v", idents, res)
				}
				sim := NewCachedSimulation(g, res)
				if m := runSim(t, sim); m != baseMakespan {
					t.Fatalf("hypothesis %v: makespan %v != baseline %v", idents, time.Duration(m), time.Duration(baseMakespan))
				}
				assertSameSchedule(t, g, base, sim)
				assertFaithful(t, sim)
				if len(idents) == 1 {
					if el := identElig(t, res, idents[0]); el.State != IdentNotFound {
						t.Fatalf("unknown digest state = %v, want not-found", el.State)
					}
				}
			}
		})
	}
}

// --- V2: leaf digest cached — one executed call whose subtree is a single
// self-time block on the critical path.
//
//	R [0,1000]
//	├── C1 call d1 (executed) [0,400] ── waits E1
//	│    └── E1 call_exec Container.withExec [0,400] self 400
//	└── C2 call d2 (executed) [400,1000] ── waits E2
//	     └── E2 call_exec Container.from [400,1000] self 600
//
// Caching d1: C1 hits at 0, E1 is elided, C2 is pulled left to 0 by the
// implicit join → makespan 600, saved exactly the 400ms block (no slack). For
// a true leaf this must equal the factor-0 answer — the one case where the
// old proxy was honest.
func TestCachedLeafDigest(t *testing.T) {
	g := sequentialFixture(t)
	baseline := runSim(t, NewSimulation(g, nil))

	res := cachedRes(t, g, 0, "d1")
	if el := identElig(t, res, "d1"); el.State != IdentEligible || el.ShortCircuited != 1 {
		t.Fatalf("d1 eligibility = %+v, want eligible with 1 short-circuited call", el)
	}
	if res.ElidedOps != 1 || res.ElidedSelfNS != 400*ms {
		t.Fatalf("elided = %d ops / %v, want 1 op / 400ms", res.ElidedOps, time.Duration(res.ElidedSelfNS))
	}
	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - makespan; saved != 400*ms {
		t.Fatalf("saved %v, want 400ms", time.Duration(saved))
	}

	// The hit call finishes at simStart + pullCost (= its start).
	if start, finish := sim.SimTimes(g.Ops[2]); start != 0 || finish != 0 {
		t.Fatalf("hit call sim = [%v,%v], want [0,0]", time.Duration(start), time.Duration(finish))
	}
	// The elided producing op is never reached.
	if _, _, ok := sim.simTimesOK(g.Ops[3]); ok {
		t.Fatal("elided call_exec was replayed; it must never run")
	}

	// Leaf equality with factor-0 on the class.
	f0 := NewSimulation(g, map[ClassKey]float64{{Kind: "call_exec", Class: "Container.withExec"}: 0})
	if m0 := runSim(t, f0); m0 != makespan {
		t.Fatalf("factor-0 makespan %v != cached makespan %v (must agree for a true leaf)",
			time.Duration(m0), time.Duration(makespan))
	}
}

// --- V3: orchestrator digest cached — the call_exec's cost lives in its
// children (call_exec self ≈ 0), the shape remote caching targets.
//
//	R [0,1000]
//	├── C1 call d1 (executed) [0,400] ── waits E1
//	│    └── E1 call_exec [0,400] self 0
//	         └── X exec [0,400] self 400
//	└── C2/E2 as in V2 [400,1000] self 600
//
// Factor-0 on the call_exec class scales a self-time of 0 → saves 0: the
// confidently-wrong answer that motivates this feature, pinned as a test.
// Caching d1 elides the whole subtree → saves the full 400ms.
func TestCachedOrchestrator(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "d1", "ok", 0, 400*ms),
		opEvent(s, 4, 3, "exec", "exec.run", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 5, 1, "call", "Container.from", "d2", "executed", 400*ms, 1000*ms),
		opEvent(s, 6, 5, "call_exec", "Container.from", "d2", "ok", 400*ms, 1000*ms),
		waitEvent(s, 5, 6, "", "call_exec", 400*ms, 1000*ms),
	}
	g := buildGraph(t, s, events)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 1000*ms {
		t.Fatalf("baseline = %v, want 1s", time.Duration(baseline))
	}

	// The motivating bug: factor-0 on the orchestrator class saves ≈ 0.
	f0 := NewSimulation(g, map[ClassKey]float64{{Kind: "call_exec", Class: "Container.withExec"}: 0})
	if m0 := runSim(t, f0); baseline-m0 != 0 {
		t.Fatalf("factor-0 saved %v, want 0 (orchestrator self-time is ~0 by construction)", time.Duration(baseline-m0))
	}

	res := cachedRes(t, g, 0, "d1")
	if res.ElidedOps != 2 || res.ElidedSelfNS != 400*ms {
		t.Fatalf("elided = %d ops / %v, want 2 ops / 400ms", res.ElidedOps, time.Duration(res.ElidedSelfNS))
	}
	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - makespan; saved != 400*ms {
		t.Fatalf("cached saved %v, want 400ms (the subtree's critical contribution)", time.Duration(saved))
	}
}

// --- V4: executor + joiner of one digest; downstream work gated on the
// joiner's result.
//
//	R [0,500]
//	├── C1 call d1 (executed) [0,400] ── waits E
//	│    └── E call_exec d1 [0,400] self 400
//	├── A call dA (executed) [0,500] ── waits EP  (A joins before P — lower ID
//	│       at the same end — so the wait reaches EP out of order and the hit
//	│       joiner is anchored through a prefix walk)
//	└── P call dP (executed) [0,500] ── waits EP
//	     └── EP call_exec dP [0,500] self [400,500]
//	          └── C2 call d1 (joined) [0,400] ── waits E (singleflight)
//
// Caching d1: both callers hit at their anchored starts (+pullCost 0); EP's
// join on C2 unblocks at 0 so its 100ms tail runs [0,100]; the elided E is
// never demanded. Saved 400ms.
func TestCachedExecutorJoinerDownstream(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 1, "call", "A.call", "dA", "executed", 0, 500*ms),
		waitEvent(s, 4, 6, "", "call_exec", 0, 500*ms),
		opEvent(s, 5, 1, "call", "P.call", "dP", "executed", 0, 500*ms),
		opEvent(s, 6, 5, "call_exec", "P.call", "dP", "ok", 0, 500*ms),
		waitEvent(s, 5, 6, "", "call_exec", 0, 500*ms),
		opEvent(s, 7, 6, "call", "Container.withExec", "d1", "joined", 0, 400*ms),
		waitEvent(s, 7, 3, "", "singleflight", 0, 400*ms),
	}
	g := buildGraph(t, s, events)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 500*ms {
		t.Fatalf("baseline = %v, want 500ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "d1")
	if el := identElig(t, res, "d1"); el.ShortCircuited != 2 {
		t.Fatalf("d1 short-circuited = %d, want 2 (executor AND joiner)", el.ShortCircuited)
	}
	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - makespan; saved != 400*ms {
		t.Fatalf("saved %v, want 400ms", time.Duration(saved))
	}
	if _, f := sim.SimTimes(g.Ops[2]); f != 0 {
		t.Fatalf("executor call finish = %v, want 0 (hit)", time.Duration(f))
	}
	if _, f := sim.SimTimes(g.Ops[7]); f != 0 {
		t.Fatalf("joiner call finish = %v, want 0 (hit at joiner.start + pullCost)", time.Duration(f))
	}
	// The joiner's waiter (EP) unblocks immediately: its 100ms tail runs at 0.
	if _, f := sim.SimTimes(g.Ops[6]); f != 100*ms {
		t.Fatalf("EP finish = %v, want 100ms", time.Duration(f))
	}
	if sim.PrefixAnchors == 0 {
		t.Fatal("expected an out-of-order prefix anchor (A's wait must reach EP before P's join)")
	}
}

// --- V5: the same digest executed twice (the DupExecuted case). Digest-level
// semantics has no "first" caller: both executions' regions elide, both
// callers hit.
//
//	R: C1 d1 (executed) [0,400]→E1 self 400 · C2 d1 (executed) [400,800]→E2
//	self 400 · C3 d2 (executed) [800,1000]→E3 self 200, all sequential.
func TestCachedDupExecuted(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(s, 2, 1, "call", "Mod.build", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Mod.build", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 1, "call", "Mod.build", "d1", "executed", 400*ms, 800*ms),
		opEvent(s, 5, 4, "call_exec", "Mod.build", "d1", "ok", 400*ms, 800*ms),
		waitEvent(s, 4, 5, "", "call_exec", 400*ms, 800*ms),
		opEvent(s, 6, 1, "call", "Mod.test", "d2", "executed", 800*ms, 1000*ms),
		opEvent(s, 7, 6, "call_exec", "Mod.test", "d2", "ok", 800*ms, 1000*ms),
		waitEvent(s, 6, 7, "", "call_exec", 800*ms, 1000*ms),
	}
	g := buildGraph(t, s, events)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 1000*ms {
		t.Fatalf("baseline = %v, want 1s", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "d1")
	el := identElig(t, res, "d1")
	if el.ShortCircuited != 2 || el.RegionsElided != 2 {
		t.Fatalf("d1 = %+v, want 2 short-circuited calls and 2 elided regions", el)
	}
	if res.ElidedOps != 2 || res.ElidedSelfNS != 800*ms {
		t.Fatalf("elided = %d ops / %v, want 2 ops / 800ms", res.ElidedOps, time.Duration(res.ElidedSelfNS))
	}
	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - makespan; saved != 800*ms {
		t.Fatalf("saved %v, want 800ms (both duplicate executions elide)", time.Duration(saved))
	}
}

// --- V6: an ident with one executed and one already-hit call. The recorded
// hit was already a lookup: it replays untouched (its recorded 10ms lookup
// time is data, not hypothesis); the executed call short-circuits; the report
// notes the mixed ident.
func TestCachedMixedHitExecuted(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(s, 2, 1, "call", "Mod.build", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Mod.build", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 1, "call", "Mod.build", "d1", "hit", 400*ms, 410*ms),
		opEvent(s, 5, 1, "call", "Mod.test", "d2", "executed", 410*ms, 1000*ms),
		opEvent(s, 6, 5, "call_exec", "Mod.test", "d2", "ok", 410*ms, 1000*ms),
		waitEvent(s, 5, 6, "", "call_exec", 410*ms, 1000*ms),
	}
	g := buildGraph(t, s, events)
	baseline := runSim(t, NewSimulation(g, nil))

	res := cachedRes(t, g, 0, "d1")
	el := identElig(t, res, "d1")
	if el.State != IdentEligible || el.Hits != 1 || el.Successes != 1 || el.ShortCircuited != 1 {
		t.Fatalf("mixed ident = %+v, want eligible, 1 hit + 1 success, 1 short-circuited", el)
	}
	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - makespan; saved != 400*ms {
		t.Fatalf("saved %v, want 400ms", time.Duration(saved))
	}
	// The recorded hit keeps its recorded 10ms lookup duration, shifted left.
	if start, finish := sim.SimTimes(g.Ops[4]); finish-start != 10*ms || start != 0 {
		t.Fatalf("recorded hit sim = [%v,%v], want [0,10ms] (untouched duration)",
			time.Duration(start), time.Duration(finish))
	}
}

// --- V7: an elided region containing a fixed-delay (lock) wait. The delay's
// owner never runs, so the delay charges nobody and the makespan reflects its
// absence.
//
//	R [0,800]: C1 d1 (executed) [0,500] → E1 self [0,100] + lock [100,500]
//	           C2 d2 (executed) [500,800] → E2 self 300
func TestCachedElidedLock(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 800*ms),
		opEvent(s, 2, 1, "call", "Mod.build", "d1", "executed", 0, 500*ms),
		opEvent(s, 3, 2, "call_exec", "Mod.build", "d1", "ok", 0, 500*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 500*ms),
		waitEvent(s, 3, 0, "some-lock", "lock", 100*ms, 500*ms),
		opEvent(s, 4, 1, "call", "Mod.test", "d2", "executed", 500*ms, 800*ms),
		opEvent(s, 5, 4, "call_exec", "Mod.test", "d2", "ok", 500*ms, 800*ms),
		waitEvent(s, 4, 5, "", "call_exec", 500*ms, 800*ms),
	}
	g := buildGraph(t, s, events)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 800*ms {
		t.Fatalf("baseline = %v, want 800ms", time.Duration(baseline))
	}

	sim := NewCachedSimulation(g, cachedRes(t, g, 0, "d1"))
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	// E1's 100ms self AND its 400ms lock delay both vanish: C2 runs [0,300].
	if saved := baseline - makespan; saved != 500*ms {
		t.Fatalf("saved %v, want 500ms (the elided lock delay charges nobody)", time.Duration(saved))
	}
}

// --- V8: simplification #2 pinned — lock contention is NOT relieved. An
// outside op's own recorded delay on the same named resource an elided op
// waited on stays unchanged: the data names no holder, so no relief is
// granted. This test exists so the limitation can never silently "improve".
//
//	R [0,600]
//	├── C d1 (executed) [0,600] → E: self [0,100] + lock res-x [100,600]
//	└── O d2 (executed) [0,500] → EO: lock res-x [0,300] + self [300,500]
//
// Caching d1 removes the E branch (600ms), but EO's recorded 300ms delay on
// res-x is unchanged even though the plausible holder (E) vanished: makespan
// 600 → 500, saved exactly 100ms. Granting relief would give 200ms makespan.
func TestCachedOutsideLockUnrelieved(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 600*ms),
		opEvent(s, 2, 1, "call", "Mod.build", "d1", "executed", 0, 600*ms),
		opEvent(s, 3, 2, "call_exec", "Mod.build", "d1", "ok", 0, 600*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 600*ms),
		waitEvent(s, 3, 0, "res-x", "lock", 100*ms, 600*ms),
		opEvent(s, 4, 1, "call", "Mod.other", "d2", "executed", 0, 500*ms),
		opEvent(s, 5, 4, "call_exec", "Mod.other", "d2", "ok", 0, 500*ms),
		waitEvent(s, 4, 5, "", "call_exec", 0, 500*ms),
		waitEvent(s, 5, 0, "res-x", "lock", 0, 300*ms),
	}
	g := buildGraph(t, s, events)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 600*ms {
		t.Fatalf("baseline = %v, want 600ms", time.Duration(baseline))
	}

	sim := NewCachedSimulation(g, cachedRes(t, g, 0, "d1"))
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if makespan != 500*ms {
		t.Fatalf("makespan = %v, want 500ms (the outside waiter's lock delay must stay)", time.Duration(makespan))
	}
}

// buildExternalDemandGraph is the Figure-2 fixture shared by V1 and V9: a
// cached candidate whose region hosts a service another client waits on.
//
//	R [0,600]
//	├── C call d1 (executed) [0,500] ── waits E
//	│    └── E call_exec d1 [0,500] self [200,500]
//	│         └── S service_start [0,200] self 200
//	└── O call d2 (executed) [0,600] ── waits EO
//	     └── EO call_exec d2 [0,600]: waits service S [0,200], self [200,600]
func buildExternalDemandGraph(t *testing.T) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 600*ms),
		opEvent(s, 2, 1, "call", "Svc.start", "d1", "executed", 0, 500*ms),
		opEvent(s, 3, 2, "call_exec", "Svc.start", "d1", "ok", 0, 500*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 500*ms),
		opEvent(s, 4, 3, "service_start", "svc.health", "", "ok", 0, 200*ms),
		opEvent(s, 5, 1, "call", "Client.run", "d2", "executed", 0, 600*ms),
		opEvent(s, 6, 5, "call_exec", "Client.run", "d2", "ok", 0, 600*ms),
		waitEvent(s, 5, 6, "", "call_exec", 0, 600*ms),
		waitEvent(s, 6, 4, "", "service", 0, 200*ms),
	}
	return buildGraph(t, s, events)
}

// --- V9: an external wait into a candidate region (Figure 2). The whole
// region is kept, the recorded schedule is preserved EXACTLY, the cached root
// is not short-circuited, the residual is reported, and savings are 0 — v1
// refuses to guess where the demanded work would have run.
func TestCachedExternalDemandKeepsRegion(t *testing.T) {
	g := buildExternalDemandGraph(t)
	base := NewSimulation(g, nil)
	baseline := runSim(t, base)
	if baseline != 600*ms {
		t.Fatalf("baseline = %v, want 600ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "d1")
	el := identElig(t, res, "d1")
	if el.State != IdentEligible || el.ShortCircuited != 0 || el.KeptCalls != 1 || el.RegionsKept != 1 {
		t.Fatalf("d1 = %+v, want eligible with the call kept, not short-circuited", el)
	}
	if len(res.KeptRegions) != 1 {
		t.Fatalf("kept regions = %d, want 1", len(res.KeptRegions))
	}
	kept := res.KeptRegions[0]
	if kept.Ident != "d1" || kept.Root.ID != 2 || kept.Ops != 2 || kept.SelfNS != 500*ms {
		t.Fatalf("kept region = %+v, want root C (id 2), 2 ops, 500ms self", kept)
	}
	if kept.Reason != "externally demanded" || kept.Demander == nil || kept.Demander.ID != 6 {
		t.Fatalf("kept reason = %q demander = %+v, want externally demanded by EO (id 6)", kept.Reason, kept.Demander)
	}
	if res.ElidedOps != 0 {
		t.Fatalf("elided ops = %d, want 0 (whole-region keep, no partial elision)", res.ElidedOps)
	}

	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if makespan != baseline {
		t.Fatalf("makespan = %v, want the baseline %v (savings for the kept ident are 0)",
			time.Duration(makespan), time.Duration(baseline))
	}
	assertSameSchedule(t, g, base, sim)
}

// --- V10: the keep fixpoint chain. An external service waiter keeps region A;
// that revives A's inner joiner of digest d2, whose singleflight wait then
// keeps region B in the SECOND fixpoint round (B is tested first each round —
// its subtree comes earlier — so round one leaves it elidable). Keep-decisions
// only flip toward keep; everything ends kept and the schedule is exactly the
// baseline.
//
//	R [0,600]
//	├── C_B call d2 (executed) [0,400] ── waits E_B
//	│    └── E_B call_exec d2 [0,400] self 400            ← region B
//	├── C_A call d1 (executed) [0,500] ── waits E_A
//	│    └── E_A call_exec d1 [0,500] self [400,500]      ← region A
//	│         ├── S service_start [0,100] self 100
//	│         └── W call d2 (joined) [100,400] ── waits E_B (singleflight)
//	└── O call d3 (executed) [0,600] ── waits EO
//	     └── EO call_exec d3 [0,600]: waits service S [0,100], self [100,600]
func TestCachedKeepFixpointChain(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 600*ms),
		opEvent(s, 2, 1, "call", "Dep.build", "d2", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Dep.build", "d2", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 1, "call", "Svc.start", "d1", "executed", 0, 500*ms),
		opEvent(s, 5, 4, "call_exec", "Svc.start", "d1", "ok", 0, 500*ms),
		waitEvent(s, 4, 5, "", "call_exec", 0, 500*ms),
		opEvent(s, 6, 5, "service_start", "svc.health", "", "ok", 0, 100*ms),
		opEvent(s, 7, 5, "call", "Dep.build", "d2", "joined", 100*ms, 400*ms),
		waitEvent(s, 7, 3, "", "singleflight", 100*ms, 400*ms),
		opEvent(s, 8, 1, "call", "Client.run", "d3", "executed", 0, 600*ms),
		opEvent(s, 9, 8, "call_exec", "Client.run", "d3", "ok", 0, 600*ms),
		waitEvent(s, 8, 9, "", "call_exec", 0, 600*ms),
		waitEvent(s, 9, 6, "", "service", 0, 100*ms),
	}
	g := buildGraph(t, s, events)
	base := NewSimulation(g, nil)
	baseline := runSim(t, base)
	if baseline != 600*ms {
		t.Fatalf("baseline = %v, want 600ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "d1", "d2")
	if len(res.KeptRegions) != 2 {
		t.Fatalf("kept regions = %d, want 2 (the fixpoint must propagate A's keep into B)", len(res.KeptRegions))
	}
	for _, kept := range res.KeptRegions {
		switch kept.Ident {
		case "d1":
			if kept.Demander == nil || kept.Demander.ID != 9 {
				t.Fatalf("region A demander = %+v, want EO (id 9)", kept.Demander)
			}
		case "d2":
			if kept.Demander == nil || kept.Demander.ID != 7 {
				t.Fatalf("region B demander = %+v, want the revived joiner W (id 7)", kept.Demander)
			}
		default:
			t.Fatalf("unexpected kept region ident %q", kept.Ident)
		}
	}
	if el := identElig(t, res, "d2"); el.ShortCircuited != 0 || el.KeptCalls != 2 {
		t.Fatalf("d2 = %+v, want 0 short-circuited and 2 kept calls (root + joiner inside kept A)", el)
	}
	if res.ElidedOps != 0 || !res.Noop() {
		t.Fatalf("elided ops = %d, want 0 (everything kept)", res.ElidedOps)
	}

	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if makespan != baseline {
		t.Fatalf("makespan = %v, want baseline %v", time.Duration(makespan), time.Duration(baseline))
	}
	assertSameSchedule(t, g, base, sim)
}

// --- V11: the singleflight norm — every wait into the region comes from a
// same-digest caller, each itself a short-circuited hit, so no live demand
// exists and the region elides. The common case must not be blocked by its
// own joiners.
func TestCachedSingleflightNorm(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		opEvent(s, 2, 1, "call", "Mod.build", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Mod.build", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 1, "call", "Mod.build", "d1", "joined", 0, 400*ms),
		waitEvent(s, 4, 3, "", "singleflight", 0, 400*ms),
		opEvent(s, 5, 1, "call", "Mod.build", "d1", "joined", 50*ms, 400*ms),
		waitEvent(s, 5, 3, "", "singleflight", 50*ms, 400*ms),
		opEvent(s, 6, 1, "call", "Mod.test", "d2", "executed", 400*ms, 500*ms),
		opEvent(s, 7, 6, "call_exec", "Mod.test", "d2", "ok", 400*ms, 500*ms),
		waitEvent(s, 6, 7, "", "call_exec", 400*ms, 500*ms),
	}
	g := buildGraph(t, s, events)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 500*ms {
		t.Fatalf("baseline = %v, want 500ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "d1")
	el := identElig(t, res, "d1")
	if el.ShortCircuited != 3 || el.RegionsElided != 1 || el.RegionsKept != 0 {
		t.Fatalf("d1 = %+v, want 3 hits, 1 elided region, 0 kept", el)
	}
	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - makespan; saved != 400*ms {
		t.Fatalf("saved %v, want 400ms", time.Duration(saved))
	}
}

// --- V12: nested cached digests — the inner digest's region sits strictly
// inside the outer's. The union elides once (no double-counting in the
// residual), the outer root hits, and the inner call simply vanishes with the
// region it sits in. Caching {outer, inner} saves exactly what caching
// {outer} alone saves.
//
//	R [0,900]
//	├── C1 call d1 (executed) [0,700] ── waits E1
//	│    └── E1 call_exec d1 [0,700] self [0,100]+[400,700]
//	│         └── C2 call d2 (executed) [100,400] ── waits E2
//	│              └── E2 call_exec d2 [100,400] self 300
//	└── C3 call d3 (executed) [700,900] → E3 self 200
func TestCachedNestedDigests(t *testing.T) {
	build := func(t *testing.T) *Graph {
		s := newFixtureStrings()
		return buildGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
			opEvent(s, 2, 1, "call", "Outer.build", "d1", "executed", 0, 700*ms),
			opEvent(s, 3, 2, "call_exec", "Outer.build", "d1", "ok", 0, 700*ms),
			waitEvent(s, 2, 3, "", "call_exec", 0, 700*ms),
			opEvent(s, 4, 3, "call", "Inner.dep", "d2", "executed", 100*ms, 400*ms),
			opEvent(s, 5, 4, "call_exec", "Inner.dep", "d2", "ok", 100*ms, 400*ms),
			waitEvent(s, 4, 5, "", "call_exec", 100*ms, 400*ms),
			opEvent(s, 6, 1, "call", "Down.stream", "d3", "executed", 700*ms, 900*ms),
			opEvent(s, 7, 6, "call_exec", "Down.stream", "d3", "ok", 700*ms, 900*ms),
			waitEvent(s, 6, 7, "", "call_exec", 700*ms, 900*ms),
		})
	}

	savedWith := func(t *testing.T, idents ...string) (int64, *CachedResolution) {
		g := build(t)
		baseline := runSim(t, NewSimulation(g, nil))
		res := cachedRes(t, g, 0, idents...)
		sim := NewCachedSimulation(g, res)
		makespan := runSim(t, sim)
		assertFaithful(t, sim)
		return baseline - makespan, res
	}

	savedOuter, _ := savedWith(t, "d1")
	savedBoth, resBoth := savedWith(t, "d1", "d2")
	savedInner, _ := savedWith(t, "d2")

	// Outer alone elides {E1, C2, E2} = 400+0+300 = 700ms of self-time.
	if savedOuter != 700*ms {
		t.Fatalf("saved(outer) = %v, want 700ms", time.Duration(savedOuter))
	}
	// Inner alone elides only E2's 300ms.
	if savedInner != 300*ms {
		t.Fatalf("saved(inner) = %v, want 300ms", time.Duration(savedInner))
	}
	// The union elides once: caching both == caching the outer.
	if savedBoth != savedOuter {
		t.Fatalf("saved(both) = %v, want %v (the nested union must not double-elide)",
			time.Duration(savedBoth), time.Duration(savedOuter))
	}
	if resBoth.ElidedOps != 3 || resBoth.ElidedSelfNS != 700*ms {
		t.Fatalf("joint residual = %d ops / %v, want 3 ops / 700ms counted once",
			resBoth.ElidedOps, time.Duration(resBoth.ElidedSelfNS))
	}
	// "Both roots report as hits" (the row's letter): the inner call is
	// SATISFIED — covered by the outer elision, so under the counterfactual it
	// never occurs at all. It reports as a hit (ShortCircuited + ElidedCalls
	// is the hit-reported total); the data keeps the distinction because the
	// replay honestly never runs it.
	if el := identElig(t, resBoth, "d2"); el.ShortCircuited+el.ElidedCalls != 1 || el.ElidedCalls != 1 || el.KeptCalls != 0 {
		t.Fatalf("inner ident = %+v, want its one call hit-reported via the covering elision", el)
	}
	if el := identElig(t, resBoth, "d1"); el.ShortCircuited != 1 {
		t.Fatalf("outer ident = %+v, want its root short-circuited to a hit", el)
	}
}

// --- V13: critical-path shift. The elided chain (500ms) is only slightly
// longer than a parallel non-elided chain (400ms): the saving is the
// DIFFERENCE of the chains, not the elided duration — savings come from
// re-simulation, never subtraction.
func TestCachedCriticalPathShift(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		opEvent(s, 2, 1, "call", "X.chain", "dX", "executed", 0, 500*ms),
		opEvent(s, 3, 2, "call_exec", "X.chain", "dX", "ok", 0, 500*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 500*ms),
		opEvent(s, 4, 1, "call", "Y.chain", "dY", "executed", 0, 400*ms),
		opEvent(s, 5, 4, "call_exec", "Y.chain", "dY", "ok", 0, 400*ms),
		waitEvent(s, 4, 5, "", "call_exec", 0, 400*ms),
	}
	g := buildGraph(t, s, events)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 500*ms {
		t.Fatalf("baseline = %v, want 500ms", time.Duration(baseline))
	}

	sim := NewCachedSimulation(g, cachedRes(t, g, 0, "dX"))
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - makespan; saved != 100*ms {
		t.Fatalf("saved %v, want 100ms — the chain difference, NOT the 500ms elided duration", time.Duration(saved))
	}
}

// --- V14: the ineligibility matrix. Each ineligible ident carries its
// specific reason and contributes nothing: the simulation is bit-for-bit the
// baseline. Simulating what the engine refuses to cache (or what failed, or
// what is still running) is fiction, and fiction is refused loudly.
func TestCachedIneligibilityMatrix(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 400*ms),
		// do_not_cache call running inline work as a direct child
		opEvent(s, 2, 1, "call", "Inline.op", "d_dnc", "do_not_cache", 0, 100*ms),
		opEvent(s, 3, 2, "internal", "inline.work", "", "ok", 0, 100*ms),
		// error-only execution
		opEvent(s, 4, 1, "call", "Fail.op", "d_err", "error", 100*ms, 200*ms),
		opEvent(s, 5, 4, "call_exec", "Fail.op", "d_err", "error", 100*ms, 200*ms),
		waitEvent(s, 4, 5, "", "call_exec", 100*ms, 200*ms),
		// canceled-only call
		opEvent(s, 6, 1, "call", "Gone.op", "d_cnc", "canceled", 200*ms, 300*ms),
		// eligible call whose region contains an OPEN op (recorded via header)
		opEvent(s, 7, 1, "call", "Part.op", "d_open2", "executed", 300*ms, 350*ms),
		// an eligible control ident, to prove the matrix doesn't over-block
		opEvent(s, 8, 1, "call", "Ok.op", "d_ok", "executed", 300*ms, 400*ms),
		opEvent(s, 9, 8, "call_exec", "Ok.op", "d_ok", "ok", 300*ms, 400*ms),
		waitEvent(s, 8, 9, "", "call_exec", 300*ms, 400*ms),
		// an ident with a successful execution AND an ENDED outcome-less call:
		// suspect data — refused, never guessed into a hit
		opEvent(s, 12, 1, "call", "Unk.op", "d_unk", "executed", 350*ms, 380*ms),
		opEvent(s, 13, 12, "call_exec", "Unk.op", "d_unk", "ok", 350*ms, 380*ms),
		waitEvent(s, 12, 13, "", "call_exec", 350*ms, 380*ms),
		opEvent(s, 14, 1, "call", "Unk.op", "d_unk", "", 380*ms, 400*ms),
	}
	header := &wcprof.DumpHeader{
		SchemaVersion:  wcprof.DumpSchemaVersion,
		EpochUnixNano:  0,
		DumpedUnixNano: 400 * ms,
		Strings:        s.values,
		EventCount:     len(events),
		OpenOps: []wcprof.DumpOpenOp{
			// a call op still open at dump time
			{OpID: 10, ParentID: 1, Kind: "call", ClassID: s.id("Hung.op"), IdentID: s.id("d_open"), StartNS: 300 * ms},
			// an open op INSIDE the (otherwise ended) d_open2 call's region
			{OpID: 11, ParentID: 7, Kind: "call_exec", ClassID: s.id("Part.op"), IdentID: s.id("d_open2"), StartNS: 310 * ms},
		},
	}
	g, err := Build(header, events)
	if err != nil {
		t.Fatal(err)
	}
	base := NewSimulation(g, nil)
	baseline := runSim(t, base)

	res := cachedRes(t, g, 0, "d_dnc", "d_err", "d_cnc", "d_open", "d_open2", "d_unk")
	want := map[string]IdentState{
		"d_dnc":   IdentDoNotCache,
		"d_err":   IdentFailedOnly,
		"d_cnc":   IdentFailedOnly,
		"d_open":  IdentOpen,
		"d_open2": IdentOpen,
		"d_unk":   IdentUnknownOutcome,
	}
	for ident, state := range want {
		if el := identElig(t, res, ident); el.State != state {
			t.Fatalf("%s state = %v, want %v", ident, el.State, state)
		}
	}
	if !res.Noop() {
		t.Fatalf("all-ineligible hypothesis must be a no-op, got %+v", res)
	}
	sim := NewCachedSimulation(g, res)
	if m := runSim(t, sim); m != baseline {
		t.Fatalf("makespan %v != baseline %v", time.Duration(m), time.Duration(baseline))
	}
	assertSameSchedule(t, g, base, sim)
	assertFaithful(t, sim)

	// The eligible control still works on the same graph.
	res = cachedRes(t, g, 0, "d_ok")
	if el := identElig(t, res, "d_ok"); el.State != IdentEligible || el.ShortCircuited != 1 {
		t.Fatalf("control ident = %+v, want eligible", el)
	}
}

// --- §6.5 note 5 pinned: an ABANDONED wait into a candidate region does not
// demand a keep. The demand test uses exactly the predicate the program
// compiler gates on (joinWait): a wait the waiter gave up on before its
// target ended compiles to a no-op action with no target reference, so
// eliding its target cannot corrupt the waiter's schedule — the waiter
// replays identically with or without the region.
//
//	R [0,600]
//	├── C call d1 (executed) [0,400] ── waits E
//	│    └── E call_exec d1 [0,400] self 400
//	└── W call d2 (executed) [0,600] ── waits EW; ABANDONED wait on E [50,300]
//	     └── EW call_exec d2 [0,600] self 600
func TestCachedAbandonedWaitDoesNotDemand(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 600*ms),
		opEvent(s, 2, 1, "call", "Mod.build", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Mod.build", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 1, "call", "Other.op", "d2", "executed", 0, 600*ms),
		opEvent(s, 5, 4, "call_exec", "Other.op", "d2", "ok", 0, 600*ms),
		waitEvent(s, 4, 5, "", "call_exec", 0, 600*ms),
		waitEvent(s, 4, 3, "", "singleflight", 50*ms, 300*ms), // abandoned: ends before E does
	}
	g := buildGraph(t, s, events)
	base := NewSimulation(g, nil)
	baseline := runSim(t, base)
	if baseline != 600*ms {
		t.Fatalf("baseline = %v, want 600ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "d1")
	if el := identElig(t, res, "d1"); el.RegionsElided != 1 || el.RegionsKept != 0 {
		t.Fatalf("d1 = %+v, want the region elided despite the abandoned wait into it", el)
	}
	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	// The W branch dominates and is untouched; the abandoned waiter's own
	// schedule is identical with the region gone.
	if makespan != 600*ms {
		t.Fatalf("makespan = %v, want 600ms", time.Duration(makespan))
	}
	for _, id := range []uint64{4, 5} {
		bs, bf := base.SimTimes(g.Ops[id])
		cs, cf := sim.SimTimes(g.Ops[id])
		if bs != cs || bf != cf {
			t.Fatalf("op %d schedule changed under elision: [%v,%v] vs [%v,%v]",
				id, time.Duration(bs), time.Duration(bf), time.Duration(cs), time.Duration(cf))
		}
	}
}

// --- §6.5 note 6 pinned: an ORPHAN wait (no owning op) never demands a keep
// — the replay cannot model it, so the elision engine must stay consistent
// with the baseline replay's model of the same data — but it IS printed as a
// residual against the elided set, never silent.
func TestCachedOrphanWaitReportedNotDemanding(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 600*ms),
		opEvent(s, 2, 1, "call", "Mod.build", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Mod.build", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		// waiter op 999 does not exist: an orphan wait targeting the region
		waitEvent(s, 999, 3, "", "service", 0, 400*ms),
		opEvent(s, 4, 1, "call", "Mod.test", "d2", "executed", 400*ms, 600*ms),
		opEvent(s, 5, 4, "call_exec", "Mod.test", "d2", "ok", 400*ms, 600*ms),
		waitEvent(s, 4, 5, "", "call_exec", 400*ms, 600*ms),
	}
	g := buildGraph(t, s, events)
	if len(g.OrphanWaits) != 1 {
		t.Fatalf("fixture must produce exactly one orphan wait, got %d", len(g.OrphanWaits))
	}
	baseline := runSim(t, NewSimulation(g, nil))

	res := cachedRes(t, g, 0, "d1")
	if el := identElig(t, res, "d1"); el.RegionsElided != 1 || el.RegionsKept != 0 {
		t.Fatalf("d1 = %+v, want the region elided (orphan waits demand nothing)", el)
	}
	if res.OrphanWaitsIntoElided != 1 || res.OrphanWaitNSIntoElided != 400*ms {
		t.Fatalf("orphan residual = %d waits / %v, want 1 wait / 400ms printed",
			res.OrphanWaitsIntoElided, time.Duration(res.OrphanWaitNSIntoElided))
	}
	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - makespan; saved != 400*ms {
		t.Fatalf("saved %v, want 400ms", time.Duration(saved))
	}
}

// --- §6.5 note 3 pinned: a canceled call of an ELIGIBLE ident (one whose
// production succeeded elsewhere) also short-circuits. Derivation: the cache
// lookup precedes execution/join, and a hit cannot be canceled while waiting
// for a production that does not happen — under the hypothesis every lookup
// of the digest returns at start + pullCost.
//
//	R [0,500]
//	├── C1 call d1 (executed) [0,400] ── waits E
//	│    └── E call_exec d1 [0,400] self 400
//	└── P call dP (executed) [0,500] ── waits EP
//	     └── EP call_exec dP [0,500] self [400,500]
//	          └── C2 call d1 (CANCELED) [0,400] ── waits E (singleflight)
func TestCachedMixedSuccessAndCanceled(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		opEvent(s, 2, 1, "call", "Mod.build", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Mod.build", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 1, "call", "P.call", "dP", "executed", 0, 500*ms),
		opEvent(s, 5, 4, "call_exec", "P.call", "dP", "ok", 0, 500*ms),
		waitEvent(s, 4, 5, "", "call_exec", 0, 500*ms),
		opEvent(s, 6, 5, "call", "Mod.build", "d1", "canceled", 0, 400*ms),
		waitEvent(s, 6, 3, "", "singleflight", 0, 400*ms),
	}
	g := buildGraph(t, s, events)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 500*ms {
		t.Fatalf("baseline = %v, want 500ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "d1")
	el := identElig(t, res, "d1")
	if el.State != IdentEligible || el.Successes != 1 || el.Failures != 1 {
		t.Fatalf("d1 = %+v, want eligible with 1 success + 1 failure", el)
	}
	if el.ShortCircuited != 2 {
		t.Fatalf("d1 short-circuited = %d, want 2 (the canceled caller hits too)", el.ShortCircuited)
	}
	sim := NewCachedSimulation(g, res)
	makespan := runSim(t, sim)
	assertFaithful(t, sim)
	if _, f := sim.SimTimes(g.Ops[6]); f != 0 {
		t.Fatalf("canceled caller finish = %v, want 0 (a hit cannot be canceled waiting for nothing)", time.Duration(f))
	}
	if saved := baseline - makespan; saved != 400*ms {
		t.Fatalf("saved %v, want 400ms", time.Duration(saved))
	}
}

// --- V15: the unreachability assertion is reachable and loud. A resolution
// deliberately blinded to a live external wait (built by hand — the real
// pre-pass provably keeps this region, as V9 asserts) marks the demanded
// region elided; the replay must trip ElidedOpDemanded rather than silently
// resurrect or absorb the region.
func TestCachedElidedOpDemandedAssert(t *testing.T) {
	g := buildExternalDemandGraph(t)

	// The honest resolution keeps the region (V9). Blind it by hand: elide
	// E (id 3) and S (id 4), short-circuit C (id 2), ignoring EO's wait on S.
	p := g.program()
	blinded := &CachedResolution{
		elided:   make([]bool, len(p.ops)),
		hitShort: make([]bool, len(p.ops)),
	}
	blinded.elided[p.idxByID[3]] = true
	blinded.elided[p.idxByID[4]] = true
	blinded.hitShort[p.idxByID[2]] = true

	sim := NewCachedSimulation(g, blinded)
	if _, err := sim.Run(); err != nil {
		t.Fatal(err)
	}
	if sim.ElidedOpDemanded == 0 {
		t.Fatal("ElidedOpDemanded = 0: the assert is unreachable — a blinded elision was silently absorbed")
	}
	if len(sim.ElidedOpDemandedSample) == 0 {
		t.Fatal("expected a sample of the demanded elided ops")
	}
}

// --- V16: the pullCost seam works before any real cost model exists. On the
// V2 graph a hit on the critical path shifts finish by exactly pullCost and
// shrinks the saving by exactly pullCost; when a parallel chain provides
// slack, the saving is unchanged (slack absorbs the pull).
func TestCachedPullCost(t *testing.T) {
	// Critical path: saved shrinks by exactly the pull cost.
	g := sequentialFixture(t)
	baseline := runSim(t, NewSimulation(g, nil))

	sim0 := NewCachedSimulation(g, cachedRes(t, g, 0, "d1"))
	m0 := runSim(t, sim0)
	simP := NewCachedSimulation(g, cachedRes(t, g, 50*ms, "d1"))
	mP := runSim(t, simP)
	assertFaithful(t, sim0)
	assertFaithful(t, simP)
	if start, finish := simP.SimTimes(g.Ops[2]); finish-start != 50*ms {
		t.Fatalf("hit call duration = %v, want exactly pullCost (50ms)", time.Duration(finish-start))
	}
	if saved0, savedP := baseline-m0, baseline-mP; saved0-savedP != 50*ms {
		t.Fatalf("saving shrank by %v, want exactly pullCost (50ms): saved@0=%v saved@50ms=%v",
			time.Duration(saved0-savedP), time.Duration(saved0), time.Duration(savedP))
	}

	// Slack: a parallel 700ms branch dominates; the pull cost is absorbed.
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 1, "call", "Container.from", "d2", "executed", 400*ms, 1000*ms),
		opEvent(s, 5, 4, "call_exec", "Container.from", "d2", "ok", 400*ms, 1000*ms),
		waitEvent(s, 4, 5, "", "call_exec", 400*ms, 1000*ms),
		opEvent(s, 6, 1, "call", "P.slack", "dP", "executed", 0, 700*ms),
		opEvent(s, 7, 6, "call_exec", "P.slack", "dP", "ok", 0, 700*ms),
		waitEvent(s, 6, 7, "", "call_exec", 0, 700*ms),
	}
	gs := buildGraph(t, s, events)
	baselineS := runSim(t, NewSimulation(gs, nil))
	if baselineS != 1000*ms {
		t.Fatalf("slack baseline = %v, want 1s", time.Duration(baselineS))
	}
	mS0 := runSim(t, NewCachedSimulation(gs, cachedRes(t, gs, 0, "d1")))
	mSP := runSim(t, NewCachedSimulation(gs, cachedRes(t, gs, 50*ms, "d1")))
	if baselineS-mS0 != 300*ms || baselineS-mSP != 300*ms {
		t.Fatalf("slack savings = %v @0 / %v @50ms, want 300ms for both (slack absorbs the pull)",
			time.Duration(baselineS-mS0), time.Duration(baselineS-mSP))
	}
}
