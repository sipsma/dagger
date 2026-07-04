package wcanalyze

import (
	"testing"
	"time"

	"github.com/dagger/dagger/engine/wcprof"
)

// Amendment A1 validation (catalog rows V24–V26; design §3.3 A1): elision
// regions additionally include the subtrees of exec-kind ops whose ident IS
// the cached digest — the engine's explicit attribution of lazily-deferred
// production. V26 (the calibration re-run) is recorded in
// hack/designs/whatif-cached-calibration.md.

// lazyExecFixture is the withExec shape the V23 calibration exposed: the
// producer's resolver is thin, and the actual container run executes at
// Evaluate time under a LAZY op parented to the CONSUMER, with exec.run
// carrying the producer's call digest as its ident (execIdent).
//
//	R [0,900]
//	├── CW call Container.withExec dW (executed) [0,50] ── waits EW
//	│    └── EW call_exec dW [0,50] self 50                  (thin resolver)
//	└── CS call Container.stdout dS (executed) [50,900] ── waits ES
//	     └── ES call_exec dS [50,900] self [800,900]
//	          └── L lazy Container.withExec [50,800] self [50,60]
//	               └── X exec exec.run ident=execIdent [60,800]
//	                    └── P exec_phase exec.processRun [60,800] self 740
//	               L waits X (exec) [60,800]
//
// execIdent = dW (attributed) or "state-xyz" (unattributed — the pre-A1
// shape, still real for executions whose CallDigest the engine didn't know).
func lazyExecFixture(t *testing.T, execIdent string) *Graph {
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
		opEvent(s, 6, 5, "lazy", "Container.withExec", "", "ok", 50*ms, 800*ms),
		opEvent(s, 7, 6, "exec", "exec.run", execIdent, "ok", 60*ms, 800*ms),
		opEvent(s, 8, 7, "exec_phase", "exec.processRun", "state-1", "ok", 60*ms, 800*ms),
		waitEvent(s, 6, 7, "", "exec", 60*ms, 800*ms),
	})
}

// --- V24: caching the withExec digest on the lazy-exec shape. Without the
// exec-ident attribution only the thin resolver elides (saved = 50ms — the
// honest ≈0 the V23 calibration measured); with it, the deferred production
// elides too and the consumer chain collapses to its own work.
func TestCachedLazyExecAttribution(t *testing.T) {
	// Unattributed: the pre-A1 answer, still the honest answer when the
	// engine recorded no CallDigest for the execution.
	g := lazyExecFixture(t, "state-xyz")
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 900*ms {
		t.Fatalf("baseline = %v, want 900ms", time.Duration(baseline))
	}
	res := cachedRes(t, g, 0, "dW")
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 50*ms {
		t.Fatalf("unattributed saved %v, want 50ms (only the thin resolver elides)", time.Duration(saved))
	}

	// Attributed (A1): the exec region [X..P] elides; the lazy wrapper is the
	// stated remainder (its 10ms self replays); the ancestor's production
	// wait and spawn are waived, never gate-tripping.
	g = lazyExecFixture(t, "dW")
	baseline = runSim(t, NewSimulation(g, nil))
	res = cachedRes(t, g, 0, "dW")
	el := identElig(t, res, "dW")
	if el.RegionsElided != 2 || el.RegionsKept != 0 {
		t.Fatalf("dW = %+v, want 2 elided regions (resolver subtree + attributed exec)", el)
	}
	if res.ElidedOps != 3 || res.ElidedSelfNS != 790*ms {
		t.Fatalf("elided = %d ops / %v, want 3 ops (EW, X, P) / 790ms", res.ElidedOps, time.Duration(res.ElidedSelfNS))
	}
	if res.WaivedProductionWaits != 1 {
		t.Fatalf("waived production waits = %d, want 1 (the lazy wrapper's wait on X)", res.WaivedProductionWaits)
	}
	sim = NewCachedSimulation(g, res)
	m = runSim(t, sim)
	assertFaithful(t, sim) // the waivers must keep ElidedOpDemanded at 0
	if saved := baseline - m; saved != 790*ms {
		t.Fatalf("attributed saved %v, want 790ms (resolver 50 + deferred production 740)", time.Duration(saved))
	}
	// The lazy wrapper is a stated remainder: it replays its own 10ms self.
	if start, finish := sim.SimTimes(g.Ops[6]); finish-start != 10*ms {
		t.Fatalf("lazy wrapper sim duration = %v, want its 10ms self (remainder, not elided)",
			time.Duration(finish-start))
	}
}

// --- V25a: a same-ident exec of an UNCACHED digest changes nothing. The
// attribution seam must never leak elision onto digests outside the
// hypothesis.
func TestCachedExecAttributionUncachedIdent(t *testing.T) {
	s := newFixtureStrings()
	g := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 950*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "dW", "executed", 0, 50*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "dW", "ok", 0, 50*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 50*ms),
		opEvent(s, 4, 1, "call", "Container.stdout", "dS", "executed", 50*ms, 900*ms),
		opEvent(s, 5, 4, "call_exec", "Container.stdout", "dS", "ok", 50*ms, 900*ms),
		waitEvent(s, 4, 5, "", "call_exec", 50*ms, 900*ms),
		opEvent(s, 6, 5, "lazy", "Container.withExec", "", "ok", 50*ms, 800*ms),
		opEvent(s, 7, 6, "exec", "exec.run", "dW", "ok", 60*ms, 800*ms),
		opEvent(s, 8, 7, "exec_phase", "exec.processRun", "state-1", "ok", 60*ms, 800*ms),
		waitEvent(s, 6, 7, "", "exec", 60*ms, 800*ms),
		// an unrelated cacheable branch, the actual hypothesis target
		opEvent(s, 9, 1, "call", "Other.op", "dY", "executed", 900*ms, 950*ms),
		opEvent(s, 10, 9, "call_exec", "Other.op", "dY", "ok", 900*ms, 950*ms),
		waitEvent(s, 9, 10, "", "call_exec", 900*ms, 950*ms),
	})
	base := NewSimulation(g, nil)
	runSim(t, base)

	res := cachedRes(t, g, 0, "dY")
	if res.ElidedOps != 1 {
		t.Fatalf("elided ops = %d, want 1 (EY only; dW's attributed exec is NOT in the hypothesis)", res.ElidedOps)
	}
	sim := NewCachedSimulation(g, res)
	runSim(t, sim)
	assertFaithful(t, sim)
	// The withExec branch — call, lazy wrapper, exec, phase — is untouched.
	for _, id := range []uint64{2, 3, 6, 7, 8} {
		bs, bf := base.SimTimes(g.Ops[id])
		cs, cf := sim.SimTimes(g.Ops[id])
		if bs != cs || bf != cf {
			t.Fatalf("op %d shifted under an unrelated hypothesis: [%v,%v] vs [%v,%v]",
				id, time.Duration(bs), time.Duration(bf), time.Duration(cs), time.Duration(cf))
		}
	}
}

// --- V25b: ancestor-vs-third-party demand. A third-party waiter (not an
// ancestor of the exec root, not elided, not a short-circuited cached call)
// keeps the attributed exec region whole — only the resolver's 50ms elides —
// while the region's internal schedule is preserved (it shifts with its
// anchors but never deforms).
func TestCachedExecRegionThirdPartyDemand(t *testing.T) {
	s := newFixtureStrings()
	g := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "dW", "executed", 0, 50*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "dW", "ok", 0, 50*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 50*ms),
		opEvent(s, 4, 1, "call", "Container.stdout", "dS", "executed", 50*ms, 900*ms),
		opEvent(s, 5, 4, "call_exec", "Container.stdout", "dS", "ok", 50*ms, 900*ms),
		waitEvent(s, 4, 5, "", "call_exec", 50*ms, 900*ms),
		opEvent(s, 6, 5, "lazy", "Container.withExec", "", "ok", 50*ms, 800*ms),
		opEvent(s, 7, 6, "exec", "exec.run", "dW", "ok", 60*ms, 800*ms),
		opEvent(s, 8, 7, "exec_phase", "exec.processRun", "state-1", "ok", 60*ms, 800*ms),
		waitEvent(s, 6, 7, "", "exec", 60*ms, 800*ms),
		// third party: waits on the exec itself (e.g. a service consumer)
		opEvent(s, 9, 1, "call", "Third.party", "dT", "executed", 0, 850*ms),
		waitEvent(s, 9, 7, "", "exec", 60*ms, 800*ms),
	})
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 900*ms {
		t.Fatalf("baseline = %v, want 900ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "dW")
	el := identElig(t, res, "dW")
	if el.RegionsElided != 1 || el.RegionsKept != 1 {
		t.Fatalf("dW = %+v, want resolver region elided + attributed exec region KEPT", el)
	}
	if len(res.KeptRegions) != 1 || res.KeptRegions[0].Root.ID != 7 || res.KeptRegions[0].Demander == nil || res.KeptRegions[0].Demander.ID != 9 {
		t.Fatalf("kept regions = %+v, want the exec region (root X id 7) demanded by the third party (id 9)", res.KeptRegions)
	}
	if res.WaivedProductionWaits != 0 {
		t.Fatalf("waived waits = %d, want 0 (a kept region's waits gate as recorded)", res.WaivedProductionWaits)
	}
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 50*ms {
		t.Fatalf("saved %v, want 50ms (only the resolver elides; kept production still runs)", time.Duration(saved))
	}
	// The kept region shifts with its anchors but never deforms internally.
	if start, finish := sim.SimTimes(g.Ops[7]); finish-start != 740*ms {
		t.Fatalf("kept exec sim duration = %v, want its recorded 740ms", time.Duration(finish-start))
	}
}

// --- A1 structural propagation (the Chunk-4 review blocker): an attributed
// exec region NESTED INSIDE A KEPT call region must be kept too — a
// root-inclusive region covers its own root, which would otherwise mask the
// root's position inside the surrounding kept region and let an elided exec
// pierce the kept region's exact replay (kept/elided overlap, waived
// spawns deforming a kept subtree).
//
//	R [0,900]
//	├── CW call dW (executed) [0,50] → EW self 50          (elides; CW hits)
//	├── CS call dS (executed) [50,900] → ES [50,900] self [800,900]
//	│    └── L lazy [50,800] self [50,60]
//	│         └── X exec exec.run ident dW [60,800] → P self 740
//	│         L waits X (exec)
//	└── T call dT (executed) [0,850]: self [0,60]+[800,850]; waits L [60,800]
//
// Caching {dS, dW}: T's wait into desc(CS) keeps it (CS not short-circuited);
// the exec region [X..P] sits inside that kept subtree and MUST be kept
// structurally. Only EW elides: saved exactly 50ms, both kept regions
// internally undeformed.
func TestCachedExecRegionInsideKeptRegion(t *testing.T) {
	s := newFixtureStrings()
	g := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "dW", "executed", 0, 50*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "dW", "ok", 0, 50*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 50*ms),
		opEvent(s, 4, 1, "call", "Container.stdout", "dS", "executed", 50*ms, 900*ms),
		opEvent(s, 5, 4, "call_exec", "Container.stdout", "dS", "ok", 50*ms, 900*ms),
		waitEvent(s, 4, 5, "", "call_exec", 50*ms, 900*ms),
		opEvent(s, 6, 5, "lazy", "Container.withExec", "", "ok", 50*ms, 800*ms),
		opEvent(s, 7, 6, "exec", "exec.run", "dW", "ok", 60*ms, 800*ms),
		opEvent(s, 8, 7, "exec_phase", "exec.processRun", "state-1", "ok", 60*ms, 800*ms),
		waitEvent(s, 6, 7, "", "exec", 60*ms, 800*ms),
		opEvent(s, 9, 1, "call", "Third.party", "dT", "executed", 0, 850*ms),
		waitEvent(s, 9, 6, "", "lazy", 60*ms, 800*ms),
	})
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 900*ms {
		t.Fatalf("baseline = %v, want 900ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "dS", "dW")
	if len(res.KeptRegions) != 2 {
		t.Fatalf("kept regions = %+v, want 2 (the demanded consumer subtree AND the nested exec region)", res.KeptRegions)
	}
	var sawStructural bool
	for _, kr := range res.KeptRegions {
		if kr.Root.ID == 7 {
			sawStructural = true
			if kr.Reason != "root inside a kept region (replays as recorded)" {
				t.Fatalf("nested exec region reason = %q, want the structural keep", kr.Reason)
			}
		}
	}
	if !sawStructural {
		t.Fatal("the nested exec region must be kept structurally, not left elided")
	}
	if res.ElidedOps != 1 || res.WaivedProductionWaits != 0 {
		t.Fatalf("elided=%d waived=%d, want 1 (EW only) and 0 (kept regions gate as recorded)",
			res.ElidedOps, res.WaivedProductionWaits)
	}
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim)
	if saved := baseline - m; saved != 50*ms {
		t.Fatalf("saved %v, want exactly the 50ms resolver (the kept subtree runs in full)", time.Duration(saved))
	}
	// Both kept regions internally undeformed.
	for _, tc := range []struct {
		id      uint64
		wantDur int64
	}{{5, 850 * ms}, {7, 740 * ms}} {
		if start, finish := sim.SimTimes(g.Ops[tc.id]); finish-start != tc.wantDur {
			t.Fatalf("kept op %d sim duration = %v, want its recorded %v",
				tc.id, time.Duration(finish-start), time.Duration(tc.wantDur))
		}
	}
}

// --- V25c: multi-consumer joiners wait on the LAZY op (live, outside the
// exec region), so they unblock at the wrapper's remainder finish — the
// consistent multi-consumer story under A1.
func TestCachedExecRegionMultiConsumer(t *testing.T) {
	s := newFixtureStrings()
	g := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "dW", "executed", 0, 50*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "dW", "ok", 0, 50*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 50*ms),
		opEvent(s, 4, 1, "call", "Container.stdout", "dS", "executed", 50*ms, 900*ms),
		opEvent(s, 5, 4, "call_exec", "Container.stdout", "dS", "ok", 50*ms, 900*ms),
		waitEvent(s, 4, 5, "", "call_exec", 50*ms, 900*ms),
		opEvent(s, 6, 5, "lazy", "Container.withExec", "", "ok", 50*ms, 800*ms),
		opEvent(s, 7, 6, "exec", "exec.run", "dW", "ok", 60*ms, 800*ms),
		opEvent(s, 8, 7, "exec_phase", "exec.processRun", "state-1", "ok", 60*ms, 800*ms),
		waitEvent(s, 6, 7, "", "exec", 60*ms, 800*ms),
		// second consumer joins the SAME in-flight evaluation: waits the lazy op
		opEvent(s, 9, 1, "call", "Container.file", "dF", "executed", 50*ms, 900*ms),
		opEvent(s, 10, 9, "call_exec", "Container.file", "dF", "ok", 50*ms, 900*ms),
		waitEvent(s, 9, 10, "", "call_exec", 50*ms, 900*ms),
		waitEvent(s, 10, 6, "", "lazy", 60*ms, 800*ms),
	})
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 900*ms {
		t.Fatalf("baseline = %v, want 900ms", time.Duration(baseline))
	}

	res := cachedRes(t, g, 0, "dW")
	sim := NewCachedSimulation(g, res)
	m := runSim(t, sim)
	assertFaithful(t, sim)
	// Both consumers collapse to their own work: the lazy wrapper's 10ms
	// remainder unblocks the first consumer's chain AND the joiner's lazy
	// wait, so each finishes at wrapper-remainder + its own 100ms tail.
	if saved := baseline - m; saved != 790*ms {
		t.Fatalf("saved %v, want 790ms", time.Duration(saved))
	}
	if _, f := sim.SimTimes(g.Ops[10]); f != 110*ms {
		t.Fatalf("joining consumer's call_exec finish = %v, want 110ms (lazy remainder 10ms + its 100ms tail)", time.Duration(f))
	}
}
