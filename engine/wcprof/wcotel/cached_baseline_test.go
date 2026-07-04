package wcotel

import (
	"os"
	"testing"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// TestCachedBaselineInvarianceOnCapture is the OTel-capture half of what-if-
// cached catalog row V1 (hack/designs/whatif-cached-design.md §4.5): an empty
// CachedSet on a REAL committed capture must reproduce the baseline
// simulation bit-for-bit — same makespan, same per-op sim times, zero new
// counters. Nothing was hypothesized, so nothing may differ.
func TestCachedBaselineInvarianceOnCapture(t *testing.T) {
	f, err := os.Open("testdata/baseline-simple-noservice.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	_, g, err := Load(f)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	base := wcanalyze.NewSimulation(g, nil)
	baseMakespan, err := base.Run()
	if err != nil {
		t.Fatal(err)
	}

	res := wcanalyze.ResolveCachedHypothesis(g, wcanalyze.CachedHypothesis{})
	if !res.Noop() {
		t.Fatalf("empty hypothesis must resolve to a no-op, got %+v", res)
	}
	sim := wcanalyze.NewCachedSimulation(g, res)
	makespan, err := sim.Run()
	if err != nil {
		t.Fatal(err)
	}
	if makespan != baseMakespan {
		t.Fatalf("makespan %d != baseline %d", makespan, baseMakespan)
	}
	for _, op := range g.Ops {
		bs, bf := base.SimTimes(op)
		cs, cf := sim.SimTimes(op)
		if bs != cs || bf != cf {
			t.Fatalf("op %d (%s %s): schedule diverged under an empty hypothesis: [%d,%d] vs [%d,%d]",
				op.ID, op.Kind, op.Class, bs, bf, cs, cf)
		}
	}
	if sim.ElidedOpDemanded != 0 {
		t.Fatalf("ElidedOpDemanded = %d, want 0", sim.ElidedOpDemanded)
	}
}
