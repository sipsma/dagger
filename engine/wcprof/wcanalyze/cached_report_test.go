package wcanalyze

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dagger/dagger/engine/wcprof"
)

// nonAdditiveFixture: two executed digests of one class on ONE dependency
// chain, plus a parallel 500ms branch that takes over the critical path.
//
//	R [0,800]
//	├── C1 call Mod.build d1 (executed) [0,400] → E1 self 400
//	├── C2 call Mod.build d2 (executed) [400,800] → E2 self 400   (sequential after C1)
//	└── P call Other.op dP (executed) [0,500] → EP self 500       (parallel)
//
// Reasoned expectations: baseline 800. Caching d1 alone pulls C2's chain to
// [0,400] → makespan 500 (P) → saved 300. Caching d2 alone leaves C1's 400ms
// → makespan 500 → saved 300. Caching BOTH removes the whole chain → makespan
// 500 → saved 300 < 300+300: joint saving is strictly sub-additive because
// the two savings overlap on the same critical chain (V18).
func nonAdditiveFixture(t *testing.T) *Graph {
	t.Helper()
	s := newFixtureStrings()
	return buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 800*ms),
		opEvent(s, 2, 1, "call", "Mod.build", "d1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Mod.build", "d1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 1, "call", "Mod.build", "d2", "executed", 400*ms, 800*ms),
		opEvent(s, 5, 4, "call_exec", "Mod.build", "d2", "ok", 400*ms, 800*ms),
		waitEvent(s, 4, 5, "", "call_exec", 400*ms, 800*ms),
		opEvent(s, 6, 1, "call", "Other.op", "dP", "executed", 0, 500*ms),
		opEvent(s, 7, 6, "call_exec", "Other.op", "dP", "ok", 0, 500*ms),
		waitEvent(s, 6, 7, "", "call_exec", 0, 500*ms),
	})
}

// --- V18: ranking non-additivity — the individual rows do not sum to the
// joint saving, and the table header says so.
func TestCachedRankingNonAdditivity(t *testing.T) {
	g := nonAdditiveFixture(t)
	baseline := runSim(t, NewSimulation(g, nil))
	if baseline != 800*ms {
		t.Fatalf("baseline = %v, want 800ms", time.Duration(baseline))
	}

	rows := RunWhatIfCached(g, baseline)
	rowSaved := func(substr string) int64 {
		t.Helper()
		for _, r := range rows {
			if strings.Contains(r.Label, substr) {
				if r.GateBad {
					t.Fatalf("row %q gate-failed", r.Label)
				}
				return r.SavedNS
			}
		}
		t.Fatalf("no ranking row matching %q in %+v", substr, rows)
		return 0
	}

	savedD1 := rowSaved("Mod.build d1")
	savedD2 := rowSaved("Mod.build d2")
	if savedD1 != 300*ms || savedD2 != 300*ms {
		t.Fatalf("individual savings = %v / %v, want 300ms each",
			time.Duration(savedD1), time.Duration(savedD2))
	}
	// The class group IS the joint set here (both digests share the class).
	savedJoint := rowSaved("Mod.build (all 2 executed digests)")
	if savedJoint != 300*ms {
		t.Fatalf("joint saving = %v, want 300ms", time.Duration(savedJoint))
	}
	if savedJoint >= savedD1+savedD2 {
		t.Fatalf("joint %v must be < sum of individuals %v (overlapping chains)",
			time.Duration(savedJoint), time.Duration(savedD1+savedD2))
	}

	// The explicit joint hypothesis agrees with the class row (same set).
	detail, err := RunCachedDetail(g, NewCachedHypothesis([]string{"d1", "d2"}, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := detail.BaselineNS - detail.MakespanNS; got != savedJoint {
		t.Fatalf("detail joint saving %v != ranking joint %v", time.Duration(got), time.Duration(savedJoint))
	}

	// The header carries the honesty note.
	var buf bytes.Buffer
	writeWhatIfCachedRanking(&buf, rows, 30)
	if !strings.Contains(buf.String(), "NOT additive") {
		t.Fatalf("ranking header must state non-additivity:\n%s", buf.String())
	}
}

// --- V21: report output is stable and deterministic — two independently
// built identical graphs render byte-identical ranking and detail sections
// (this is what catches map-order nondeterminism), and the sections carry the
// eligibility / kept-region / residual content.
func TestCachedReportDeterministicGolden(t *testing.T) {
	render := func(t *testing.T) (string, string) {
		t.Helper()
		g := buildExternalDemandGraph(t)
		baseline := runSim(t, NewSimulation(g, nil))
		var rank bytes.Buffer
		writeWhatIfCachedRanking(&rank, RunWhatIfCached(g, baseline), 30)
		detail, err := RunCachedDetail(g, NewCachedHypothesis([]string{"d1", "no-such-digest"}, 0), 10)
		if err != nil {
			t.Fatal(err)
		}
		var det bytes.Buffer
		detail.Write(&det)
		return rank.String(), det.String()
	}

	rank1, det1 := render(t)
	rank2, det2 := render(t)
	if rank1 != rank2 {
		t.Fatalf("ranking output nondeterministic:\n--- a ---\n%s--- b ---\n%s", rank1, rank2)
	}
	if det1 != det2 {
		t.Fatalf("detail output nondeterministic:\n--- a ---\n%s--- b ---\n%s", det1, det2)
	}

	for _, want := range []string{
		"what-if-cached: makespan saved",
		"save@pull=0",
	} {
		if !strings.Contains(rank1, want) {
			t.Fatalf("ranking missing %q:\n%s", want, rank1)
		}
	}
	for _, want := range []string{
		"baseline makespan: 600.0ms   counterfactual: 600.0ms   saved: 0ns (0.0%)",
		"d1: eligible — 1 call(s) (0 hit, 1 success, 0 failed) -> 0 hit-reported",
		"no-such-digest: not found in trace",
		"kept: Svc.start d1 — 2 ops, 500.0ms self — externally demanded by call_exec Client.run (wait: service)",
		"residuals: 0 op(s) elided",
		"counterfactual blocking chain",
	} {
		if !strings.Contains(det1, want) {
			t.Fatalf("detail missing %q:\n%s", want, det1)
		}
	}
}

// The full report renders the ranking section by default when executed calls
// exist (design §3.4 mode 1: default-on).
func TestReportIncludesCachedRanking(t *testing.T) {
	g := sequentialFixture(t)
	var buf bytes.Buffer
	if err := WriteReport(&buf, g, ReportOptions{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"what-if-cached: makespan saved if these results had been cache hits",
		"Container.withExec d1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}
