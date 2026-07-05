package wcanalyze

import (
	"strings"
	"testing"
	"time"

	"github.com/dagger/dagger/engine/wcprof"
)

// General-rule validation (catalog rows V28–V31, V33; design §4.5): elision
// regions sourced kind-agnostically from deferred-production attribution —
// the lazy-op ident emit — with the exec-kind sourcing retained as the
// pre-emit-trace fallback, and the forced-evaluation facts feeding the keep
// test. Expected values derived by reason before running (doctrine §0.4).

// generalRuleFixture is the post-emit withExec shape: the LAZY op carries the
// producer digest (lazyIdent = dW), and the wrapper phases live inside it.
//
//	R [0,900]
//	├── CW call dW (executed) [0,50] ── waits EW
//	│    └── EW call_exec dW [0,50] self 50                (thin resolver)
//	└── CS call dS (executed) [50,900] ── waits ES
//	     └── ES call_exec dS [50,900] self [800,900]
//	          └── L lazy ident=lazyIdent [50,800] self [50,60]
//	               ├── PM exec_phase withExec.prepareMounts [60,100] self 40
//	               ├── X exec exec.run ident dW [100,700]
//	               │    └── P exec_phase exec.processRun [100,700] self 600
//	               └── AO exec_phase withExec.applyOutputs [700,800] self 100
//	               L waits X (exec) [100,700]
func generalRuleFixture(t *testing.T, lazyIdent string) *Graph {
	t.Helper()
	s := newFixtureStrings()
	return buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "dW", "executed", 0, 50*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "dW", "ok", 0, 50*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 50*ms),
		opEvent(s, 4, 1, "call", "Container.stdout", "dS", "executed", 50*ms, 900*ms),
		opEvent(s, 5, 4, "call_exec", "Container.stdout", "dS", "ok", 50*ms, 900*ms),
		waitEvent(s, 4, 5, "", "call_exec", 50*ms, 900*ms),
		opEvent(s, 6, 5, "lazy", "Container.withExec", lazyIdent, "ok", 50*ms, 800*ms),
		opEvent(s, 7, 6, "exec_phase", "withExec.prepareMounts", "", "ok", 60*ms, 100*ms),
		opEvent(s, 8, 6, "exec", "exec.run", "dW", "ok", 100*ms, 700*ms),
		opEvent(s, 9, 8, "exec_phase", "exec.processRun", "state-1", "ok", 100*ms, 700*ms),
		opEvent(s, 10, 6, "exec_phase", "withExec.applyOutputs", "", "ok", 700*ms, 800*ms),
		waitEvent(s, 6, 8, "", "exec", 100*ms, 700*ms),
	})
}

// --- V28 + V29 (fallback half): the general rule elides the whole lazy
// wrapper via the lazy op's producer ident; a pre-emit trace (ident-less lazy
// op) falls back to A1's exec-sourced answer unchanged. The two differ by
// EXACTLY the wrapper's critical contribution.
func TestCachedGeneralRuleWrapperElision(t *testing.T) {
	// Post-emit shape: lazy op carries dW. Regions: {EW} + the lazy region
	// [L..AO] (the nested exec region is inside it). L's own production wait
	// on X is internal; ES's spawn/join of L is the waived launch. Everything
	// dW elides: EW(50) + wrapper (L 10 + PM 40 + AO 100) + production (600).
	// CS's chain collapses to its own 100ms tail at 0 → makespan 100.
	g := generalRuleFixture(t, "dW")
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 900*ms {
		t.Fatalf("baseline = %v, want 900ms", time.Duration(baseline))
	}
	res := cachedRes(t, g, 0, "dW")
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim)
	savedGeneral := baseline - m
	if savedGeneral != 800*ms {
		t.Fatalf("general-rule saved %v, want 800ms", time.Duration(savedGeneral))
	}

	// Pre-emit trace (V29 fallback half): the lazy op has no ident, so only
	// {EW} and the exec region [X..P] elide; the wrapper replays. The lazy
	// op compresses to self 10 + PM 40 (joined at 50) + AO 100 → its chain
	// contributes 150ms before ES's tail: makespan 250, saved 650.
	g = generalRuleFixture(t, "")
	baseline = runSim(t, NewSimulation(g, nil))
	res = cachedRes(t, g, 0, "dW")
	sim = NewCachedSimulation(g, res)
	m = runSim(t, sim)
	assertFaithful(t, sim)
	savedFallback := baseline - m
	if savedFallback != 650*ms {
		t.Fatalf("exec-fallback saved %v, want 650ms", time.Duration(savedFallback))
	}

	// V28's arithmetic: the difference is exactly the wrapper's critical
	// contribution (L self 10 + prepareMounts 40 + applyOutputs 100).
	if savedGeneral-savedFallback != 150*ms {
		t.Fatalf("general - fallback = %v, want exactly the 150ms wrapper contribution",
			time.Duration(savedGeneral-savedFallback))
	}
}

// --- The concurrent-forcer shape under the general rule (review catch): a
// SECOND consumer joining the in-flight lazy evaluation records a lazy wait
// targeting the lazy ROOT. It is not an ancestor, but under B1 it would hit
// the materialized payload — its wait is production demand for the
// hypothesized digest, waived (counted), never a keep. Without the waiver
// the live joiner's replay would also trip ElidedOpDemanded on the elided
// root, so this pins both the demand rule and the replay skip.
func TestCachedGeneralRuleConcurrentForcer(t *testing.T) {
	s := newFixtureStrings()
	g := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "dW", "executed", 0, 50*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "dW", "ok", 0, 50*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 50*ms),
		opEvent(s, 4, 1, "call", "Container.stdout", "dS", "executed", 50*ms, 900*ms),
		opEvent(s, 5, 4, "call_exec", "Container.stdout", "dS", "ok", 50*ms, 900*ms),
		waitEvent(s, 4, 5, "", "call_exec", 50*ms, 900*ms),
		opEvent(s, 6, 5, "lazy", "Container.withExec", "dW", "ok", 50*ms, 800*ms),
		opEvent(s, 7, 6, "exec", "exec.run", "dW", "ok", 100*ms, 700*ms),
		opEvent(s, 8, 7, "exec_phase", "exec.processRun", "state-1", "ok", 100*ms, 700*ms),
		waitEvent(s, 6, 7, "", "exec", 100*ms, 700*ms),
		// the second, concurrent consumer: joins the SAME evaluation via a
		// lazy wait on the wrapper (the region root)
		opEvent(s, 9, 1, "call", "Container.file", "dS2", "executed", 50*ms, 900*ms),
		opEvent(s, 10, 9, "call_exec", "Container.file", "dS2", "ok", 50*ms, 900*ms),
		waitEvent(s, 9, 10, "", "call_exec", 50*ms, 900*ms),
		waitEvent(s, 10, 6, "", "lazy", 60*ms, 800*ms),
	})
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 900*ms {
		t.Fatalf("baseline = %v, want 900ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "dW")
	if len(res.KeptRegions) != 0 {
		t.Fatalf("the concurrent forcer's lazy wait must not keep the region, got %+v", res.KeptRegions)
	}
	if res.WaivedProductionWaits != 1 {
		t.Fatalf("waived = %d, want 1 (the joiner's root-targeted lazy wait)", res.WaivedProductionWaits)
	}
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim) // the waived join must not trip ElidedOpDemanded
	if saved := baseline - m; saved != 790*ms {
		t.Fatalf("saved %v, want 790ms (both consumers collapse to their own tails)", time.Duration(saved))
	}
	// The joiner unblocks at its anchor and runs only its own 110ms.
	if _, f := sim.SimTimes(g.Ops[10]); f != 110*ms {
		t.Fatalf("joining consumer finish = %v, want 110ms", time.Duration(f))
	}
}

// --- V31: the G1 scenario. D's production (a lazy region) nests E's
// production; a LATER consumer forces the already-complete E, which the
// Evaluate fast path records only as a forced fact. Without the fact the
// region elides and the simulator silently removes E's production a survivor
// needed (the pre-fix overstatement, pinned); with it, the region is kept and
// reported. A forced fact whose digest is itself hypothesized demands
// nothing.
//
//	R [0,1000]
//	├── CE call dE (executed) [0,50] → EE self 50 (waits)      E's sync half
//	├── CD call dD (executed) [50,100] → ED self 50 (waits)    D's sync half
//	├── CSD call dSD (executed) [100,600] ── waits ESD
//	│    └── ESD call_exec dSD [100,600] self [550,600]
//	│         └── LD lazy ident dD [100,550] self [100,110]+[500,550]
//	│              └── LE lazy ident dE [110,500] self 390     (D's production forces E's)
//	│              LD waits LE (lazy) [110,500]
//	└── CC call dC (executed) [600,1000] ── waits EC
//	     └── EC call_exec dC [600,1000] self 400
//	         forced fact: EC forced LE (ident dE), already complete
//
// factForcer picks who recorded the fact on LE (0 = none): 11 (EC, the
// sibling-chain consumer) or 7 (ESD, a strict ANCESTOR of LD's region root —
// facts are fast-path-only, so an ancestor's fact is post-completion
// consumption like any other, never the launch join).
func g1Fixture(t *testing.T, factForcer int32) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(s, 2, 1, "call", "E.make", "dE", "executed", 0, 50*ms),
		opEvent(s, 3, 2, "call_exec", "E.make", "dE", "ok", 0, 50*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 50*ms),
		opEvent(s, 4, 1, "call", "D.make", "dD", "executed", 50*ms, 100*ms),
		opEvent(s, 5, 4, "call_exec", "D.make", "dD", "ok", 50*ms, 100*ms),
		waitEvent(s, 4, 5, "", "call_exec", 50*ms, 100*ms),
		opEvent(s, 6, 1, "call", "D.consume", "dSD", "executed", 100*ms, 600*ms),
		opEvent(s, 7, 6, "call_exec", "D.consume", "dSD", "ok", 100*ms, 600*ms),
		waitEvent(s, 6, 7, "", "call_exec", 100*ms, 600*ms),
		opEvent(s, 8, 7, "lazy", "D.make", "dD", "ok", 100*ms, 550*ms),
		opEvent(s, 9, 8, "lazy", "E.make", "dE", "ok", 110*ms, 500*ms),
		waitEvent(s, 8, 9, "", "lazy", 110*ms, 500*ms),
		opEvent(s, 10, 1, "call", "C.consume", "dC", "executed", 600*ms, 1000*ms),
		opEvent(s, 11, 10, "call_exec", "C.consume", "dC", "ok", 600*ms, 1000*ms),
		waitEvent(s, 10, 11, "", "call_exec", 600*ms, 1000*ms),
	}
	if factForcer != 0 {
		events = append(events, wcprof.DumpEvent{
			Type: "link", LinkKind: "forced",
			ParentID: uint64(factForcer), TargetID: 9, IdentID: s.id("dE"),
		})
	}
	return buildGraph(t, s, events)
}

func TestCachedForcedFactKeepsForeignProduction(t *testing.T) {
	// Pre-fix overstatement, pinned as the motivating assertion: without the
	// fact, hypothesizing {dD} elides LD's whole region INCLUDING E's 390ms
	// production that C later needed — saved 500ms (ED 50 + CD's 50ms slot +
	// the 400ms consumer-chain compression the elision enables).
	g := g1Fixture(t, 0)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 1000*ms {
		t.Fatalf("baseline = %v, want 1s", time.Duration(baseline))
	}
	res := cachedRes(t, g, 0, "dD")
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 500*ms {
		t.Fatalf("pre-fix saved %v, want the overstated 500ms", time.Duration(saved))
	}
	if len(res.KeptRegions) != 0 {
		t.Fatalf("without the fact nothing can demand a keep, got %+v", res.KeptRegions)
	}

	// With the fact: EC's recorded force of LE (digest dE, NOT hypothesized)
	// is live external demand into LD's region → kept whole, reported; only
	// D's sync half elides. Saved exactly the 50ms the CD hit removes.
	g = g1Fixture(t, 11)
	res = cachedRes(t, g, 0, "dD")
	if len(res.KeptRegions) != 1 || res.KeptRegions[0].Root.ID != 8 {
		t.Fatalf("kept regions = %+v, want LD's region kept", res.KeptRegions)
	}
	kept := res.KeptRegions[0]
	if kept.Reason != "externally forced (post-completion demand)" || kept.Demander == nil || kept.Demander.ID != 11 {
		t.Fatalf("kept = reason %q demander %+v, want the forced-fact keep by EC (id 11)", kept.Reason, kept.Demander)
	}
	sim = NewCachedSimulation(g, res)
	m = runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 50*ms {
		t.Fatalf("post-fix saved %v, want 50ms (E's production survives for its later consumer)", time.Duration(saved))
	}

	// Same-digest rule: hypothesize {dD, dE} — the fact's digest is now
	// eligible, so its forcer would hit the materialized payload under B1 and
	// demands nothing; everything elides.
	res = cachedRes(t, g, 0, "dD", "dE")
	if len(res.KeptRegions) != 0 {
		t.Fatalf("a hypothesized-digest force must not demand, got %+v", res.KeptRegions)
	}
	sim = NewCachedSimulation(g, res)
	m = runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 550*ms {
		t.Fatalf("joint saved %v, want 550ms", time.Duration(saved))
	}
}

// A forced fact whose forcer is a strict ANCESTOR of the region root is
// demand like any other: facts are emitted only on the Evaluate fast path
// (post-completion consumption) — the launch join takes the slow path and
// never emits one, and a lazy op launched by an ancestor of the root would be
// parented under that ancestor, outside the region. So there is no
// launch-chain case to waive (unlike A1's ancestor WAITS), and an ancestor
// exclusion would silently drop a survivor's demand for non-hypothesized
// nested production. Here ESD (LD's own parent, live, self [550,600] after
// LE completes at 500) recorded the fact on LE: hypothesizing {dD} must keep
// LD's region exactly as the sibling-consumer fact does.
func TestCachedForcedFactFromAncestorKeeps(t *testing.T) {
	g := g1Fixture(t, 7)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 1000*ms {
		t.Fatalf("baseline = %v, want 1s", time.Duration(baseline))
	}
	res := cachedRes(t, g, 0, "dD")
	if len(res.KeptRegions) != 1 || res.KeptRegions[0].Root.ID != 8 {
		t.Fatalf("kept regions = %+v, want LD's region kept by the ancestor's fact", res.KeptRegions)
	}
	kept := res.KeptRegions[0]
	if kept.Reason != "externally forced (post-completion demand)" || kept.Demander == nil || kept.Demander.ID != 7 {
		t.Fatalf("kept = reason %q demander %+v, want the forced-fact keep by ESD (id 7)", kept.Reason, kept.Demander)
	}
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 50*ms {
		t.Fatalf("saved %v, want 50ms (only D's sync half elides; E's production survives)", time.Duration(saved))
	}

	// Same-digest rule unchanged: with dE also hypothesized the ancestor's
	// fact demands nothing and everything elides.
	res = cachedRes(t, g, 0, "dD", "dE")
	if len(res.KeptRegions) != 0 {
		t.Fatalf("a hypothesized-digest force must not demand, got %+v", res.KeptRegions)
	}
	sim = NewCachedSimulation(g, res)
	m = runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 550*ms {
		t.Fatalf("joint saved %v, want 550ms", time.Duration(saved))
	}
}

// --- V33: service starts are NEVER elidable production, pinned from both
// directions. Startup is per-session runtime READINESS (ServiceKey is
// session-scoped, core/services.go:473-477): a real warm run re-starts the
// service even with every result cached, so eliding it would remove work
// warm reality re-pays. This holds regardless of whether the service_start
// ident (the content-preferred digest, services.go:524) coincides with the
// hypothesized recipe digest — the coincidence must not create a region any
// more than the mismatch may.
func TestCachedServiceStartDigestCaveat(t *testing.T) {
	build := func(t *testing.T, svcStartIdent string) *Graph {
		t.Helper()
		s := newFixtureStrings()
		return buildGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 600*ms),
			opEvent(s, 2, 1, "call", "Svc.up", "dSvc", "executed", 0, 100*ms),
			opEvent(s, 3, 2, "call_exec", "Svc.up", "dSvc", "ok", 0, 100*ms),
			waitEvent(s, 2, 3, "", "call_exec", 0, 100*ms),
			opEvent(s, 4, 1, "call", "Client.run", "dCl", "executed", 100*ms, 600*ms),
			opEvent(s, 5, 4, "call_exec", "Client.run", "dCl", "ok", 100*ms, 600*ms),
			waitEvent(s, 4, 5, "", "call_exec", 100*ms, 600*ms),
			opEvent(s, 6, 5, "service_start", "service.start", svcStartIdent, "ok", 100*ms, 500*ms),
		})
	}

	for _, ident := range []string{"content:xyz", "dSvc"} {
		g := build(t, ident)
		baseline := runSim(t, NewSimulation(g, nil))
		if baseline != 600*ms {
			t.Fatalf("baseline = %v, want 600ms", time.Duration(baseline))
		}
		res := cachedRes(t, g, 0, "dSvc")
		if el := identElig(t, res, "dSvc"); el.RegionsElided != 1 {
			t.Fatalf("ident=%s: dSvc = %+v, want only the sync region (starts never root regions)", ident, el)
		}
		sim := NewCachedSimulation(g, res)
		m := runSim(t, sim)
		assertFaithful(t, sim)
		if saved := baseline - m; saved != 100*ms {
			t.Fatalf("ident=%s: saved %v, want 100ms — the 400ms start survives in BOTH digest cases (warm runs re-start services)",
				ident, time.Duration(saved))
		}
	}
}

// An UNRESOLVED forced fact (production predated recording) falls back to
// "any attributed op of the digest inside the region" as demand evidence.
// A service_start match is NOT evidence — its ident attributes per-session
// readiness, never production (V33), so a fact digest matching only a
// service_start inside the region must not keep it. An anchored call_exec
// match IS evidence (it is the digest's executing subtree): dropping it
// would silently over-elide production a survivor demanded.
func buildUnresolvedFactFixture(t *testing.T, innerKind string) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 600*ms),
		opEvent(s, 2, 1, "call", "D.make", "dD", "executed", 0, 100*ms),
		opEvent(s, 3, 2, "call_exec", "D.make", "dD", "ok", 0, 100*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 100*ms),
		opEvent(s, 6, 1, "call", "C.consume", "dC", "executed", 100*ms, 600*ms),
		opEvent(s, 7, 6, "call_exec", "C.consume", "dC", "ok", 100*ms, 600*ms),
		waitEvent(s, 6, 7, "", "call_exec", 100*ms, 600*ms),
		// The unresolved fact: C's exec consumed dE, whose completing
		// lazy op predated recording.
		{Type: "link", LinkKind: "forced", ParentID: 7, TargetID: 0, IdentID: s.id("dE")},
	}
	switch innerKind {
	case "service_start":
		events = append(events,
			opEvent(s, 4, 3, "service_start", "service.start", "dE", "ok", 10*ms, 90*ms))
	case "call_exec":
		events = append(events,
			opEvent(s, 4, 3, "call", "E.make", "dE", "executed", 10*ms, 90*ms),
			opEvent(s, 5, 4, "call_exec", "E.make", "dE", "ok", 10*ms, 90*ms),
			waitEvent(s, 4, 5, "", "call_exec", 10*ms, 90*ms))
	}
	return buildGraph(t, s, events)
}

func TestCachedUnresolvedFactEvidenceKinds(t *testing.T) {
	// service_start inside dD's region: not production evidence — elides.
	g := buildUnresolvedFactFixture(t, "service_start")
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 600*ms {
		t.Fatalf("baseline = %v, want 600ms", time.Duration(baseline))
	}
	res := cachedRes(t, g, 0, "dD")
	if len(res.KeptRegions) != 0 {
		t.Fatalf("a service_start match is readiness, not production; want no keep, got %+v", res.KeptRegions)
	}
	// Doctrine counters: the degraded-data path is visible even when it
	// demands nothing.
	if res.ForcedFacts != 1 || res.ForcedFactsUnresolved != 1 || res.OrphanForcedFacts != 0 {
		t.Fatalf("fact provenance = %d/%d/%d, want 1 consumed, 1 unrecorded-target, 0 orphan",
			res.ForcedFacts, res.ForcedFactsUnresolved, res.OrphanForcedFacts)
	}
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 100*ms {
		t.Fatalf("saved %v, want 100ms (the region elides)", time.Duration(saved))
	}

	// Anchored call_exec of dE inside dD's region: real production evidence
	// — the fact keeps the region whole, under the containment-rule reason
	// string (every firing of the degraded demand test names itself).
	g = buildUnresolvedFactFixture(t, "call_exec")
	res = cachedRes(t, g, 0, "dD")
	if len(res.KeptRegions) != 1 || res.KeptRegions[0].Root.ID != 2 {
		t.Fatalf("kept regions = %+v, want dD's region kept by the unresolved fact", res.KeptRegions)
	}
	kept := res.KeptRegions[0]
	if kept.Reason != "externally forced (unrecorded target; demand matched by recorded ident containment)" ||
		kept.Demander == nil || kept.Demander.ID != 7 {
		t.Fatalf("kept = reason %q demander %+v, want the containment-rule keep by C's exec (id 7)", kept.Reason, kept.Demander)
	}
	sim = NewCachedSimulation(g, res)
	m = runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 0 {
		t.Fatalf("saved %v, want 0 (kept whole, root not short-circuited)", time.Duration(saved))
	}
}

// An ORPHAN forced fact (its forcer op is unknown) can demand nothing — there
// is no forcer whose liveness the keep test could judge, exactly the orphan
// WAIT rationale — but it is never silently dropped: it is retained in the
// graph, counted in the resolution, and when its digest names attributed
// production that was elided, the unmodeled-demand hint is flagged for the
// report. Doctrine §0: counted + visible, never a silent default; the
// underlying data loss (a missing forcer span) is separately refused by the
// OTel structural gate, so on gate-passing captures this counter staying 0 is
// the expected state.
func TestCachedOrphanForcedFactCountedNotDemanding(t *testing.T) {
	s := newFixtureStrings()
	g := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 600*ms),
		opEvent(s, 2, 1, "call", "D.make", "dD", "executed", 0, 100*ms),
		opEvent(s, 3, 2, "call_exec", "D.make", "dD", "ok", 0, 100*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 100*ms),
		opEvent(s, 4, 3, "call", "E.make", "dE", "executed", 10*ms, 90*ms),
		opEvent(s, 5, 4, "call_exec", "E.make", "dE", "ok", 10*ms, 90*ms),
		waitEvent(s, 4, 5, "", "call_exec", 10*ms, 90*ms),
		// Forcer op 99 does not exist; target unresolved too.
		{Type: "link", LinkKind: "forced", ParentID: 99, TargetID: 0, IdentID: s.id("dE")},
	})
	if len(g.OrphanForcedFacts) != 1 || len(g.ForcedEdges) != 0 {
		t.Fatalf("graph facts = %d resolved / %d orphan, want 0 / 1",
			len(g.ForcedEdges), len(g.OrphanForcedFacts))
	}
	baseline := runSim(t, NewSimulation(g, nil))
	res := cachedRes(t, g, 0, "dD")
	if len(res.KeptRegions) != 0 {
		t.Fatalf("an orphan fact must not demand a keep, got %+v", res.KeptRegions)
	}
	if res.OrphanForcedFacts != 1 || res.OrphanForcedFactsIntoElided != 1 {
		t.Fatalf("orphan facts = %d (%d into elided), want 1 (1) — the hint must be visible",
			res.OrphanForcedFacts, res.OrphanForcedFactsIntoElided)
	}
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 100*ms {
		t.Fatalf("saved %v, want 100ms (elision proceeds; the hint is ink, not a gate)", time.Duration(saved))
	}
}

// The what-if-cached admission gates and the declared-boundary caveat
// (doctrine audit findings 1 and 4): a capture whose provenance may have
// silently lost demand evidence is REFUSED — dropped recorder events (any
// could have been a wait/fact) and emit-side ident-derivation failures
// (idents AND facts omitted; expected 0) — while the uninstrumented-forcer
// count is a declared model boundary that prints as a prominent caveat, not
// a refusal. The header→Graph plumbing is asserted alongside.
func TestCachedAdmissionGatesAndCaveat(t *testing.T) {
	build := func(t *testing.T) *Graph {
		t.Helper()
		s := newFixtureStrings()
		return buildGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 200*ms),
			opEvent(s, 2, 1, "call", "D.make", "dD", "executed", 0, 100*ms),
			opEvent(s, 3, 2, "call_exec", "D.make", "dD", "ok", 0, 100*ms),
			waitEvent(s, 2, 3, "", "call_exec", 0, 100*ms),
		})
	}

	// Header plumbing: the counters must survive Build.
	s := newFixtureStrings()
	hdr := &wcprof.DumpHeader{
		SchemaVersion:                   wcprof.DumpSchemaVersion,
		Strings:                         s.values,
		DroppedEvents:                   2,
		SuppressedIdentDerivations:      3,
		SuppressedUninstrumentedForcers: 4,
	}
	pg, err := Build(hdr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pg.DroppedEvents != 2 || pg.SuppressedIdentDerivations != 3 || pg.SuppressedUninstrumentedForcers != 4 {
		t.Fatalf("header counters lost in Build: %d/%d/%d", pg.DroppedEvents, pg.SuppressedIdentDerivations, pg.SuppressedUninstrumentedForcers)
	}

	// Dropped events refuse the detail (and thus the calibration's cold
	// side); the refusal surfaces the orphan-demand counts.
	g := build(t)
	g.DroppedEvents = 5
	if _, err := RunCachedDetail(g, NewCachedHypothesis([]string{"dD"}, 0), 4); err == nil ||
		!strings.Contains(err.Error(), "REFUSED") || !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("dropped events must refuse the cached analysis loudly, got %v", err)
	}
	// A warm capture with dropped events refuses the calibration too.
	cold := build(t)
	if _, err := RunCachedCalibration(cold, g, 0, 4); err == nil || !strings.Contains(err.Error(), "WARM") {
		t.Fatalf("a lossy warm capture must refuse the calibration, got %v", err)
	}

	// Emit-side ident-derivation failures refuse likewise.
	g = build(t)
	g.SuppressedIdentDerivations = 1
	if _, err := RunCachedDetail(g, NewCachedHypothesis([]string{"dD"}, 0), 4); err == nil ||
		!strings.Contains(err.Error(), "derivation failure") {
		t.Fatalf("ident suppression must refuse the cached analysis loudly, got %v", err)
	}

	// Uninstrumented forcers: no refusal, but the detail prints the caveat.
	g = build(t)
	g.SuppressedUninstrumentedForcers = 7
	detail, err := RunCachedDetail(g, NewCachedHypothesis([]string{"dD"}, 0), 4)
	if err != nil {
		t.Fatalf("the declared boundary must not refuse: %v", err)
	}
	var out strings.Builder
	detail.Write(&out)
	if !strings.Contains(out.String(), "CAVEAT: 7 forced-evaluation fact(s)") {
		t.Fatalf("the boundary caveat must print prominently, got:\n%s", out.String())
	}

	// ALL conditions firing at once: one aggregated refusal names every
	// violated condition AND carries the caveat — the first problem must not
	// hide the rest.
	g = build(t)
	g.DroppedEvents = 5
	g.SuppressedIdentDerivations = 1
	g.SuppressedUninstrumentedForcers = 7
	_, err = RunCachedDetail(g, NewCachedHypothesis([]string{"dD"}, 0), 4)
	if err == nil {
		t.Fatal("all-firing capture must refuse")
	}
	for _, want := range []string{"5 recorder event(s) dropped", "1 lazy ident derivation failure(s)", "CAVEAT: 7 forced-evaluation fact(s)"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("aggregated refusal must contain %q, got: %v", want, err)
		}
	}
}

// A ranking row whose hypothesis was touched by degraded fact evidence — an
// unrecorded-target fact's containment keep, here — is marked on the ROW
// (doctrine audit finding 3), not just in the detail section.
func TestCachedRankingMarksDegradedEvidence(t *testing.T) {
	g := buildUnresolvedFactFixture(t, "call_exec")
	baseline := runSim(t, NewSimulation(g, nil))
	rows := RunWhatIfCached(g, baseline)
	var dRow, cRow *WhatIfCachedRow
	for i := range rows {
		if strings.Contains(rows[i].Label, "dD") {
			dRow = &rows[i]
		}
		if strings.Contains(rows[i].Label, "dC") {
			cRow = &rows[i]
		}
	}
	if dRow == nil || !dRow.DegradedEvidence {
		t.Fatalf("the dD row's keep came from the containment rule and must be marked, got %+v", dRow)
	}
	if cRow == nil || cRow.DegradedEvidence {
		t.Fatalf("the dC row is untouched by degraded evidence and must not be marked, got %+v", cRow)
	}
	var out strings.Builder
	writeWhatIfCachedRanking(&out, rows, 20)
	if !strings.Contains(out.String(), "DEGRADED-EVIDENCE") {
		t.Fatalf("the marker must render on the row, got:\n%s", out.String())
	}
}

// --- V30 (native half): hit-set extraction distinguishes complete (B1) vs
// pending (B2) hits; the calibration excludes pending-only digests from the
// CachedSet and reports them.
func TestCachedPendingHitExtraction(t *testing.T) {
	s := newFixtureStrings()
	warm := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
		opEvent(s, 2, 1, "call", "A.op", "dA", "hit", 0, 10*ms),
		opEvent(s, 3, 1, "call", "B.op", "dB", "hit_pending", 10*ms, 20*ms),
		opEvent(s, 4, 1, "call", "C.op", "dC", "hit", 20*ms, 30*ms),
		opEvent(s, 5, 1, "call", "C.op", "dC", "hit_pending", 30*ms, 40*ms),
	})
	if got := HitDigests(warm); len(got) != 2 || got[0] != "dA" || got[1] != "dC" {
		t.Fatalf("complete hits = %v, want [dA dC]", got)
	}
	if got := PendingHitDigests(warm); len(got) != 2 || got[0] != "dB" || got[1] != "dC" {
		t.Fatalf("pending hits = %v, want [dB dC]", got)
	}

	// Eligibility tallies the split.
	res := ResolveCachedHypothesis(warm, NewCachedHypothesis([]string{"dB"}, 0))
	if el := identElig(t, res, "dB"); el.State != IdentAllHit || el.Hits != 1 || el.PendingHits != 1 {
		t.Fatalf("dB = %+v, want an all-hit ident whose one hit is pending", el)
	}

	// Calibration: dB is pending-only → excluded and counted; dC stays via
	// its complete hit.
	cold := sequentialFixture(t)
	cal, err := RunCachedCalibration(cold, warm, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if cal.WarmHitDigests != 2 || cal.WarmPendingHits != 1 {
		t.Fatalf("calibration hit sets = %d complete / %d pending-only, want 2 / 1",
			cal.WarmHitDigests, cal.WarmPendingHits)
	}
}
