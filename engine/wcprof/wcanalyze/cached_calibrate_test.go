package wcanalyze

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dagger/dagger/engine/wcprof"
)

// Catalog row V22 (calibration, mechanical half): extract hit digests from a
// warm capture on fixtures where the hit set is known — exactly the digests
// whose warm outcome is hit, nothing inferred.
func TestCachedHitDigestExtraction(t *testing.T) {
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
		opEvent(s, 2, 1, "call", "A.op", "d1", "hit", 0, 10*ms),
		opEvent(s, 3, 1, "call", "A.op", "d1", "hit", 10*ms, 20*ms), // dup hit: one entry
		opEvent(s, 4, 1, "call", "B.op", "d2", "executed", 20*ms, 60*ms),
		opEvent(s, 5, 1, "call", "C.op", "d3", "do_not_cache", 60*ms, 70*ms),
		opEvent(s, 6, 1, "call", "D.op", "d4", "hit", 70*ms, 80*ms),
		opEvent(s, 7, 1, "call", "E.op", "d6", "error", 80*ms, 90*ms),
	}
	header := &wcprof.DumpHeader{
		SchemaVersion:  wcprof.DumpSchemaVersion,
		DumpedUnixNano: 100 * ms,
		Strings:        s.values,
		EventCount:     len(events),
		OpenOps: []wcprof.DumpOpenOp{
			// an open call has no outcome yet: never extracted
			{OpID: 8, ParentID: 1, Kind: "call", ClassID: s.id("F.op"), IdentID: s.id("d5"), StartNS: 90 * ms},
		},
	}
	g, err := Build(header, events)
	if err != nil {
		t.Fatal(err)
	}
	if got := HitDigests(g); !slices.Equal(got, []string{"d1", "d4"}) {
		t.Fatalf("hit digests = %v, want exactly [d1 d4] (dedup'd, sorted, nothing inferred)", got)
	}
}

// Calibration end-to-end on known fixtures: the cold run simulated under the
// warm run's hit set, drift measured against the warm run's actual makespan.
func TestCachedCalibration(t *testing.T) {
	// Cold: two sequential executions, 1s total.
	coldG := sequentialFixture(t)

	// Warm: the same two digests, both instant hits; a d9 hit for a call the
	// cold run never made (run-specific digests are a stated gap source).
	s := newFixtureStrings()
	warmG := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 20*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "d1", "hit", 0, 10*ms),
		opEvent(s, 3, 1, "call", "Container.from", "d2", "hit", 10*ms, 20*ms),
		opEvent(s, 4, 1, "call", "Warm.only", "d9", "hit", 15*ms, 20*ms),
	})

	cal, err := RunCachedCalibration(coldG, warmG, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if cal.WarmHitDigests != 3 || cal.FoundInRun != 2 {
		t.Fatalf("hit set = %d found %d, want 3 extracted / 2 found in the cold run", cal.WarmHitDigests, cal.FoundInRun)
	}
	if cal.WarmActualNS != 20*ms {
		t.Fatalf("warm actual = %v, want 20ms", time.Duration(cal.WarmActualNS))
	}
	// Both cold executions elide: everything the cold run did was production
	// of the warm run's hit set, so the counterfactual makespan is 0.
	if cal.Detail.BaselineNS != 1000*ms || cal.Detail.MakespanNS != 0 {
		t.Fatalf("baseline/counterfactual = %v/%v, want 1s/0",
			time.Duration(cal.Detail.BaselineNS), time.Duration(cal.Detail.MakespanNS))
	}
	if gerr := cal.Detail.GateErr(); gerr != nil {
		t.Fatal(gerr)
	}

	var buf bytes.Buffer
	cal.Write(&buf)
	for _, want := range []string{
		"calibration: cold run simulated under the warm run's hit set",
		"warm-run hit digests:              3 (2 found as call idents in this run)",
		"warm run actual makespan:          20.0ms",
		"drift (sim vs warm actual):        -100.0%",
		"d9: not found in trace",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("calibration output missing %q:\n%s", want, buf.String())
		}
	}

	// A warm capture with no hits is not a warm run: loud error.
	if _, err := RunCachedCalibration(coldG, coldG, 0, 10); err == nil {
		t.Fatal("hit-less warm capture must error")
	}
}

// Large hypothesis sets (the calibration form) summarize the eligibility
// section per state — counts exact, per-ident lines elided with a note.
func TestCachedEligibilitySummarizesLargeSets(t *testing.T) {
	g := sequentialFixture(t)
	idents := []string{"d1"}
	for i := 0; i < 30; i++ {
		idents = append(idents, fmt.Sprintf("warm-only-%02d", i))
	}
	detail, err := RunCachedDetail(g, NewCachedHypothesis(idents, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	detail.Write(&buf)
	out := buf.String()
	for _, want := range []string{
		"  1 digest(s): eligible",
		"  30 digest(s): not found in trace",
		"(per-ident lines elided above 24 digests; counts are exact)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summarized eligibility missing %q:\n%s", want, out)
		}
	}
}
