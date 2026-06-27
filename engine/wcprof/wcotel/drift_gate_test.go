package wcotel

import (
	"math"
	"os"
	"testing"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// TestStandingDriftGate is the §6.4 standing regression gate: on a representative
// complex workload, the SIMULATED baseline makespan must track the ACTUAL recorded
// makespan within a tight band — a faithful emit replays to the same end time, the
// closing proof of the Chunk-3 jaccard observation now that every choke point is
// faithful — AND the §6.1 structural invariants must hold. A future engine change
// that re-introduces an unfaithful nesting (false serialization, a leaked
// self-time) would push the drift out of band or trip the gate.
//
// It runs against WCPROF_DRIFT_CAPTURE if set (CI points this at the engine-dev
// build capture — the real complex workload; scope-match native per §6.2 is the
// CI-side wiring, out of scope for the unit), else a committed module-functions
// capture so the gate always runs + passes locally. The capture must be COMPLETE
// (gate 0/0): a drift number computed over a graph with dropped parents/targets is
// meaningless, so an incomplete capture fails here as a capture/export problem, not
// a drift regression.
func TestStandingDriftGate(t *testing.T) {
	path := os.Getenv("WCPROF_DRIFT_CAPTURE")
	if path == "" {
		path = "testdata/drift-module-functions.otlpdump.jsonl"
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open drift capture %s: %v", path, err)
	}
	defer f.Close()

	c, g, err := Load(f)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}

	// §6.1 must hold first — a drift number over an impossible/incomplete graph is
	// meaningless.
	gate := CheckStructural(c, g, GateOptions{})
	if err := gate.Err(); err != nil {
		t.Fatalf("§6.1 structural gate failed on %s (capture must be complete to assert drift): %v", path, err)
	}

	// §6.4: the simulated baseline makespan vs the actual recorded makespan.
	actual := wcanalyze.ActualMakespanNS(g)
	if actual <= 0 {
		t.Fatalf("non-positive actual makespan %d on %s", actual, path)
	}
	baseline, _, conflicts, err := wcanalyze.RunWhatIfs(g, []float64{0.5}, 0)
	if err != nil {
		t.Fatalf("run baseline simulation: %v", err)
	}
	driftPct := 100 * float64(baseline-actual) / float64(actual)
	t.Logf("%s: ops=%d wait-edges=%d actual=%dns baseline=%dns drift=%.3f%% (start-conflicts=%d)",
		path, len(g.Ops), gate.WaitEdges, actual, baseline, driftPct, conflicts)

	// A faithful, complete trace replays the recorded schedule, so the baseline
	// should land essentially on the actual makespan. The band is generous enough to
	// absorb the gating model's discretization but tight enough to catch a regressed
	// nesting that serializes or leaks time.
	const bandPct = 2.0
	if math.Abs(driftPct) > bandPct {
		t.Fatalf("simulated baseline drift %.3f%% exceeds ±%.1f%% band on %s — an unfaithful nesting may have regressed (design §6.4)",
			driftPct, bandPct, path)
	}
}
