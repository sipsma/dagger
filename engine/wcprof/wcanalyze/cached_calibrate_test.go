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
		"calibration: cold run under the warm run's complete-hit set — bucketed decomposition",
		"warm actual:          20.0ms",
		"2 removed cleanly",
		"1 not found in the cold capture (coverage findings)",
		"d9: not found in trace",
		"gate: PASS",
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

//
// §3.7 bucketed decomposition — catalog rows V35–V44 (reason-first: every
// expected value below is derived in the row comments before running).
//

// opEventR is opEvent plus a recorded result id (the native shared-result id
// the decomposition's cross-capture consumptions join on).
func opEventR(s *fixtureStrings, id, parent uint64, kind, class, ident, outcome string, startNS, endNS int64, rid uint64) wcprof.DumpEvent {
	ev := opEvent(s, id, parent, kind, class, ident, outcome, startNS, endNS)
	ev.ResultID = rid
	return ev
}

// decompFixtures builds the comprehensive cold/warm pair exercising every
// ledger shape at once (V35), with all expected values derivable by hand:
//
// COLD (session root [0,2000ms]; children tile [0,1100]+[1200,1350]):
//
//	d1  call executed [0,400]    rid 10 + anchored call_exec  — hit warm ⇒ REMOVED (400ms exec + 0 call self)
//	d2  call executed [400,700]  rid 20 + call_exec           — executed warm too ⇒ EXEC-BOTH (300ms)
//	d3  call executed [700,1000] rid 77 + call_exec           — absent warm ⇒ EXEC-COLD-ONLY (price 300ms), rid-paired to d3w
//	dN  call executed [750,900] + call_exec, NESTED inside d3's call_exec — absent warm ⇒ enumerated EXEC-COLD-ONLY
//	    (price 150ms), but its LEDGER seconds belong to the OUTERMOST producer d3: per-digest prices overlap
//	    (300 + 150 > the 300ms the exec-only bucket carries), the ledger never double-counts
//	d4  call do_not_cache [1000,1100] + call_exec             — DNC (100ms)
//	d0  call hit [1200,1210]     rid 30                       — cold recorded hit ⇒ HIT-LOOKUP (10ms)
//	svc service_start [1210,1250] ident svc-digest            — SERVICE (40ms)
//	d6  call error [1250,1300]                                — FAILED-ONLY (50ms)
//	d8  call executed [1300,1350] rid 44 + call_exec          — hit warm ⇒ REMOVED (50ms)
//	session self = 2000 − (1100 + 150) = 750ms; total cold self = 2000ms in 16 ops.
//
// WARM (session root [0,500ms]; children tile [0,250]):
//
//	d1     call hit [0,10]           rid 10   ⇒ HIT-LOOKUP 10ms
//	d2     call executed [10,110]    rid 21 + call_exec ⇒ EXEC-BOTH 100ms
//	d3w    call executed [110,160]   rid 77 + call_exec ⇒ EXEC-WARM-ONLY 50ms (rid 77 = cold d3's ⇒ paired)
//	d7     call hit_pending [160,170] + lazy ident d7 [170,200] ⇒ PENDING-B2 (10+30ms), excluded from the hypothesis
//	d8     call hit_pending [200,205] + call hit [205,215] rid 44 ⇒ hypothesis via the complete hit; pending self 5ms ⇒ B2
//	d9intra call hit [215,220] rid 900 (> cold max 77) ⇒ warm-only hit, intra-warm derivation
//	d9x    call hit [220,225]  rid 5  (≤ cold max 77) ⇒ warm-only hit, CROSS-RUN equivalence hit (loud, not gated)
//	dP     lazy [225,250], producer digest dP with NO call in the capture (parent-chain materialization)
//	       ⇒ CALL-LESS PRODUCTION 25ms, reported, never gated
//	session self = 500 − 250 = 250ms; total warm self = 500ms in 13 ops.
func decompFixtures(t *testing.T) (coldG, warmG *Graph) {
	t.Helper()
	cs := newFixtureStrings()
	coldG = buildGraph(t, cs, []wcprof.DumpEvent{
		opEvent(cs, 1, 0, "session_phase", "session.query", "", "ok", 0, 2000*ms),
		opEventR(cs, 2, 1, "call", "Cls.d1", "d1", "executed", 0, 400*ms, 10),
		opEventR(cs, 3, 2, "call_exec", "Cls.d1", "d1", "ok", 0, 400*ms, 10),
		opEventR(cs, 4, 1, "call", "Cls.d2", "d2", "executed", 400*ms, 700*ms, 20),
		opEvent(cs, 5, 4, "call_exec", "Cls.d2", "d2", "ok", 400*ms, 700*ms),
		opEventR(cs, 6, 1, "call", "Cls.d3", "d3", "executed", 700*ms, 1000*ms, 77),
		opEvent(cs, 7, 6, "call_exec", "Cls.d3", "d3", "ok", 700*ms, 1000*ms),
		opEvent(cs, 15, 7, "call", "Cls.dN", "dN", "executed", 750*ms, 900*ms),
		opEvent(cs, 16, 15, "call_exec", "Cls.dN", "dN", "ok", 750*ms, 900*ms),
		opEvent(cs, 8, 1, "call", "Cls.d4", "d4", "do_not_cache", 1000*ms, 1100*ms),
		opEvent(cs, 9, 8, "call_exec", "Cls.d4", "d4", "ok", 1000*ms, 1100*ms),
		opEventR(cs, 10, 1, "call", "Cls.d0", "d0", "hit", 1200*ms, 1210*ms, 30),
		opEvent(cs, 11, 1, "service_start", "svc.start", "svc-digest", "ok", 1210*ms, 1250*ms),
		opEvent(cs, 12, 1, "call", "Cls.d6", "d6", "error", 1250*ms, 1300*ms),
		opEventR(cs, 13, 1, "call", "Cls.d8", "d8", "executed", 1300*ms, 1350*ms, 44),
		opEvent(cs, 14, 13, "call_exec", "Cls.d8", "d8", "ok", 1300*ms, 1350*ms),
	})
	ws := newFixtureStrings()
	warmG = buildGraph(t, ws, []wcprof.DumpEvent{
		opEvent(ws, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
		opEventR(ws, 2, 1, "call", "Cls.d1", "d1", "hit", 0, 10*ms, 10),
		opEventR(ws, 3, 1, "call", "Cls.d2", "d2", "executed", 10*ms, 110*ms, 21),
		opEvent(ws, 4, 3, "call_exec", "Cls.d2", "d2", "ok", 10*ms, 110*ms),
		opEventR(ws, 5, 1, "call", "Cls.d3", "d3w", "executed", 110*ms, 160*ms, 77),
		opEvent(ws, 6, 5, "call_exec", "Cls.d3", "d3w", "ok", 110*ms, 160*ms),
		opEvent(ws, 7, 1, "call", "Cls.d7", "d7", "hit_pending", 160*ms, 170*ms),
		opEvent(ws, 8, 1, "lazy", "Cls.d7", "d7", "ok", 170*ms, 200*ms),
		opEvent(ws, 9, 1, "call", "Cls.d8", "d8", "hit_pending", 200*ms, 205*ms),
		opEventR(ws, 10, 1, "call", "Cls.d8", "d8", "hit", 205*ms, 215*ms, 44),
		opEventR(ws, 11, 1, "call", "Cls.d9", "d9intra", "hit", 215*ms, 220*ms, 900),
		opEventR(ws, 12, 1, "call", "Cls.d9", "d9x", "hit", 220*ms, 225*ms, 5),
		opEvent(ws, 13, 1, "lazy", "Cls.dP", "dP", "ok", 225*ms, 250*ms),
	})
	return coldG, warmG
}

func ledgerLine(t *testing.T, l *CalibLedger, b calibBucket, wantOps int, wantSelf int64, what string) {
	t.Helper()
	got := l.Lines[b]
	if got.Ops != wantOps || got.SelfNS != wantSelf {
		t.Fatalf("%s: bucket %q = %d ops / %v, want %d ops / %v",
			what, b, got.Ops, time.Duration(got.SelfNS), wantOps, time.Duration(wantSelf))
	}
}

// V35: ledger totality/exactness — every op in exactly one bucket per side,
// bucket sums equal to the capture total EXACTLY, nested production owned by
// the outermost producer.
func TestCalibDecompLedgerTotality(t *testing.T) {
	coldG, warmG := decompFixtures(t)
	cal, err := RunCachedCalibration(coldG, warmG, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	d := cal.Decomp

	// Warm side, derived in the fixture comment.
	ledgerLine(t, d.WarmLedger, calibBucketHitLookup, 4, 30*ms, "warm")
	ledgerLine(t, d.WarmLedger, calibBucketPendingB2, 3, 45*ms, "warm")
	ledgerLine(t, d.WarmLedger, calibBucketExecBoth, 2, 100*ms, "warm")
	ledgerLine(t, d.WarmLedger, calibBucketExecOnly, 2, 50*ms, "warm")
	ledgerLine(t, d.WarmLedger, calibBucketCallLessProduction, 1, 25*ms, "warm")
	ledgerLine(t, d.WarmLedger, calibBucketSession, 1, 250*ms, "warm")
	ledgerLine(t, d.WarmLedger, calibBucketRemainder, 0, 0, "warm")
	if d.WarmLedger.TotalOps != 13 || d.WarmLedger.TotalSelfNS != 500*ms {
		t.Fatalf("warm totals = %d ops / %v, want 13 / 500ms", d.WarmLedger.TotalOps, time.Duration(d.WarmLedger.TotalSelfNS))
	}

	// Cold side. The exec-only bucket carries d3's WHOLE region (nested dN
	// included, outermost-producer ownership): 4 ops, 300ms — while the
	// per-digest PRICES of d3 (300ms) and dN (150ms) overlap and sum to 450ms.
	ledgerLine(t, d.ColdLedger, calibBucketRemoved, 4, 450*ms, "cold")
	ledgerLine(t, d.ColdLedger, calibBucketExecBoth, 2, 300*ms, "cold")
	ledgerLine(t, d.ColdLedger, calibBucketExecOnly, 4, 300*ms, "cold")
	ledgerLine(t, d.ColdLedger, calibBucketDNC, 2, 100*ms, "cold")
	ledgerLine(t, d.ColdLedger, calibBucketHitLookup, 1, 10*ms, "cold")
	ledgerLine(t, d.ColdLedger, calibBucketServiceStart, 1, 40*ms, "cold")
	ledgerLine(t, d.ColdLedger, calibBucketFailedOnly, 1, 50*ms, "cold")
	ledgerLine(t, d.ColdLedger, calibBucketSession, 1, 750*ms, "cold")
	ledgerLine(t, d.ColdLedger, calibBucketRemainder, 0, 0, "cold")
	if d.ColdLedger.TotalOps != 16 || d.ColdLedger.TotalSelfNS != 2000*ms {
		t.Fatalf("cold totals = %d ops / %v, want 16 / 2s", d.ColdLedger.TotalOps, time.Duration(d.ColdLedger.TotalSelfNS))
	}

	// The exactness identity itself: bucket sums == totals, both sides.
	for side, l := range map[string]*CalibLedger{"warm": d.WarmLedger, "cold": d.ColdLedger} {
		var ops int
		var self int64
		for b := calibBucket(0); b < calibBucketCount; b++ {
			ops += l.Lines[b].Ops
			self += l.Lines[b].SelfNS
		}
		if ops != l.TotalOps || self != l.TotalSelfNS {
			t.Fatalf("%s ledger not exact: lines sum to %d ops / %v, totals %d / %v",
				side, ops, time.Duration(self), l.TotalOps, time.Duration(l.TotalSelfNS))
		}
	}

	if err := cal.GateErr(); err != nil {
		t.Fatal(err)
	}
}

// V36: clean case — the warm complete-hit set exactly covers the cold run's
// executed digests: everything removed, all cross-run enumerations empty,
// remainders 0, gate PASS, and the rendered decomposition block contains NO
// percentage anywhere (makespans are context lines, never a ratio).
func TestCalibDecompCleanCaseNoPercentage(t *testing.T) {
	coldG := sequentialFixture(t)
	s := newFixtureStrings()
	warmG := buildGraph(t, s, []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 20*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "d1", "hit", 0, 10*ms),
		opEvent(s, 3, 1, "call", "Container.from", "d2", "hit", 10*ms, 20*ms),
	})
	cal, err := RunCachedCalibration(coldG, warmG, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	d := cal.Decomp
	if d.RemovedCleanly != 2 || len(d.KeptDigests) != 0 || len(d.IneligibleFindings) != 0 || len(d.NotFoundInCold) != 0 {
		t.Fatalf("graded verdicts = removed %d kept %v inelig %v notfound %v, want 2/none/none/none",
			d.RemovedCleanly, d.KeptDigests, d.IneligibleFindings, d.NotFoundInCold)
	}
	if len(d.ExecBoth)+len(d.ExecWarmOnly)+len(d.ExecColdOnly) != 0 {
		t.Fatalf("cross-run enumerations must be empty on the clean case: %v %v %v",
			d.ExecBoth, d.ExecWarmOnly, d.ExecColdOnly)
	}
	if err := cal.GateErr(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cal.Write(&buf)
	out := buf.String()
	block := out[strings.Index(out, "calibration:"):]
	if strings.Contains(block, "%") {
		t.Fatalf("the decomposition block must contain no percentage:\n%s", block)
	}
	if !strings.Contains(block, "gate: PASS") {
		t.Fatalf("expected gate PASS:\n%s", block)
	}
}

// V37 + V38 + V43(native): bucket b carries the true digest join with both
// sides' prices AND outcome tallies; the side-local pair is reported (never
// gated) with prominent totals, neutral labeling, and the recorded result-id
// pairing where the captures record one.
func TestCalibDecompBucketBAndSideLocals(t *testing.T) {
	coldG, warmG := decompFixtures(t)
	cal, err := RunCachedCalibration(coldG, warmG, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	d := cal.Decomp
	if !d.RIDJoinAvailable {
		t.Fatal("native captures must have the rid join available")
	}
	if len(d.ExecBoth) != 1 || d.ExecBoth[0].Ident != "d2" ||
		d.ExecBoth[0].ColdPriceNS != 300*ms || d.ExecBoth[0].WarmPriceNS != 100*ms {
		t.Fatalf("bucket b = %+v, want d2 cold 300ms / warm 100ms", d.ExecBoth)
	}
	if d.ExecBoth[0].ColdTally != "executed×1" || d.ExecBoth[0].WarmTally != "executed×1" {
		t.Fatalf("bucket b outcome tallies = %q / %q, want executed×1 both", d.ExecBoth[0].ColdTally, d.ExecBoth[0].WarmTally)
	}
	if len(d.ExecWarmOnly) != 1 || d.ExecWarmOnly[0].Ident != "d3w" ||
		d.ExecWarmOnly[0].WarmPriceNS != 50*ms ||
		d.ExecWarmOnly[0].PairedIdent != "d3" || d.ExecWarmOnly[0].PairedRID != 77 {
		t.Fatalf("warm-only = %+v, want d3w 50ms paired to d3 via rid 77", d.ExecWarmOnly)
	}
	if len(d.ExecColdOnly) != 2 || d.ExecColdOnly[0].Ident != "d3" ||
		d.ExecColdOnly[0].ColdPriceNS != 300*ms ||
		d.ExecColdOnly[0].PairedIdent != "d3w" || d.ExecColdOnly[0].PairedRID != 77 {
		t.Fatalf("cold-only = %+v, want [d3 300ms paired to d3w via rid 77, dN]", d.ExecColdOnly)
	}
	// The nested dN is enumerated with its own (overlapping) price and no
	// recorded pairing — never guessed.
	if d.ExecColdOnly[1].Ident != "dN" || d.ExecColdOnly[1].ColdPriceNS != 150*ms || d.ExecColdOnly[1].PairedIdent != "" {
		t.Fatalf("nested cold-only = %+v, want unpaired dN at 150ms", d.ExecColdOnly[1])
	}

	var buf bytes.Buffer
	cal.Write(&buf)
	out := buf.String()
	for _, want := range []string{
		// neutral labeling + the §8.2 upgrade statement (ruling conditions 1–2)
		"is not verifiable from recorded data until the scope kind is recorded",
		"executed only in WARM: 1 digest(s), 50.0ms producing self — the number the §8.2 scope recording will convert to per-digest-verified",
		"executed only in COLD (surviving the counterfactual at cold prices): 2 digest(s), 450.0ms producing self",
		// bucket b prints outcomes alongside prices (ruling condition 3)
		"cold 300.0ms [executed×1] vs warm 100.0ms [executed×1]",
		// the recorded pairing, with the pair's price variance on one line
		"same recorded result as d3 (rid 77, cold price 300.0ms)",
		"same recorded result as d3w (rid 77, warm price 50.0ms)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("decomposition output missing %q:\n%s", want, out)
		}
	}
	// Neutral labeling: no per-digest "scoped" claim anywhere.
	block := out[strings.Index(out, "executed only in one capture"):]
	if strings.Contains(strings.ToLower(block), "scoped work") {
		t.Fatalf("per-digest scope claims are forbidden until §8.2 exists:\n%s", block)
	}
}

// V39: pending-hit placement — a pending-only digest is excluded from the
// hypothesis and its lookup + forced production land in the B2 bucket; a
// digest with BOTH pending and complete hits stays in the hypothesis via its
// complete hit (its pending lookup still tallies B2).
func TestCalibDecompPendingPlacement(t *testing.T) {
	coldG, warmG := decompFixtures(t)
	cal, err := RunCachedCalibration(coldG, warmG, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	d := cal.Decomp
	if len(d.PendingOnlyExcluded) != 1 || d.PendingOnlyExcluded[0] != "d7" {
		t.Fatalf("pending-only exclusions = %v, want [d7]", d.PendingOnlyExcluded)
	}
	if d.HypothesisDigests != 4 {
		t.Fatalf("hypothesis size = %d, want 4 (d1, d8, d9intra, d9x)", d.HypothesisDigests)
	}
	// d8 (mixed pending+complete) must have been graded via its complete hit:
	// both eligible digests removed cleanly.
	if d.RemovedCleanly != 2 {
		t.Fatalf("removed cleanly = %d, want 2 (d1 and the mixed d8)", d.RemovedCleanly)
	}
	// B2 = d7 lookup (10ms) + d7 forced lazy production (30ms) + d8 pending
	// lookup (5ms).
	ledgerLine(t, d.WarmLedger, calibBucketPendingB2, 3, 45*ms, "warm B2")
}

// V40: a warm complete-hit digest whose cold region is KEPT (third-party
// demand) is a listed per-digest finding — neither a silent pass nor a gate
// failure — and its seconds sit in the cold ledger's kept bucket.
func TestCalibDecompKeptRegionVerdict(t *testing.T) {
	cs := newFixtureStrings()
	// R session [0,1000]: d1's production [0,400] contains nested call dE
	// [100,300]; third-party dT [500,900] records a gating wait on dE's call —
	// live demand into d1's region, so the whole region is kept (design §3.3).
	coldG := buildGraph(t, cs, []wcprof.DumpEvent{
		opEvent(cs, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(cs, 2, 1, "call", "Cls.d1", "d1", "executed", 0, 400*ms),
		opEvent(cs, 3, 2, "call_exec", "Cls.d1", "d1", "ok", 0, 400*ms),
		opEvent(cs, 4, 3, "call", "Cls.dE", "dE", "executed", 100*ms, 300*ms),
		opEvent(cs, 5, 4, "call_exec", "Cls.dE", "dE", "ok", 100*ms, 300*ms),
		opEvent(cs, 6, 1, "call", "Cls.dT", "dT", "executed", 500*ms, 900*ms),
		waitEvent(cs, 6, 4, "", "lazy", 500*ms, 510*ms),
	})
	ws := newFixtureStrings()
	warmG := buildGraph(t, ws, []wcprof.DumpEvent{
		opEvent(ws, 1, 0, "session_phase", "session.query", "", "ok", 0, 50*ms),
		opEvent(ws, 2, 1, "call", "Cls.d1", "d1", "hit", 0, 10*ms),
	})
	cal, err := RunCachedCalibration(coldG, warmG, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	d := cal.Decomp
	if len(d.KeptDigests) != 1 || d.KeptDigests[0] != "d1" || d.RemovedCleanly != 0 {
		t.Fatalf("graded = removed %d kept %v, want 0 removed / [d1] kept", d.RemovedCleanly, d.KeptDigests)
	}
	// Kept bucket: d1 call (self 0) + call_exec (400−200=200ms) + dE call (0)
	// + dE call_exec (200ms), root inclusive.
	ledgerLine(t, d.ColdLedger, calibBucketKept, 4, 400*ms, "cold kept")
	// The keep is a finding, not a failure.
	if err := cal.GateErr(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cal.Write(&buf)
	if !strings.Contains(buf.String(), "1 kept with reason") {
		t.Fatalf("kept finding missing from the graded section:\n%s", buf.String())
	}
}

// V41: the remainder gate FIRES — an op the classification cannot place (a
// kind-carrying non-session root outside every region) fails the calibration,
// enumerated. Both directions: once in the warm capture, once in the cold.
func TestCalibDecompRemainderGateFires(t *testing.T) {
	rogue := func(s *fixtureStrings, id uint64) wcprof.DumpEvent {
		return opEvent(s, id, 0, "exec", "rogue.exec", "", "ok", 600*ms, 700*ms)
	}
	mkCold := func(withRogue bool) *Graph {
		s := newFixtureStrings()
		events := []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 500*ms),
			opEvent(s, 2, 1, "call", "Cls.d1", "d1", "executed", 0, 400*ms),
			opEvent(s, 3, 2, "call_exec", "Cls.d1", "d1", "ok", 0, 400*ms),
		}
		if withRogue {
			events = append(events, rogue(s, 9))
		}
		return buildGraph(t, s, events)
	}
	mkWarm := func(withRogue bool) *Graph {
		s := newFixtureStrings()
		events := []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 50*ms),
			opEvent(s, 2, 1, "call", "Cls.d1", "d1", "hit", 0, 10*ms),
		}
		if withRogue {
			events = append(events, rogue(s, 9))
		}
		return buildGraph(t, s, events)
	}

	for _, tc := range []struct {
		name       string
		cold, warm *Graph
		wantSide   string
	}{
		{"warm-side", mkCold(false), mkWarm(true), "warm-capture"},
		{"cold-side", mkCold(true), mkWarm(false), "cold-capture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cal, err := RunCachedCalibration(tc.cold, tc.warm, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			gerr := cal.GateErr()
			if gerr == nil || !strings.Contains(gerr.Error(), tc.wantSide) || !strings.Contains(gerr.Error(), "land in no bucket") {
				t.Fatalf("remainder gate must fire on the %s rogue op, got %v", tc.wantSide, gerr)
			}
			var buf bytes.Buffer
			cal.Write(&buf)
			if !strings.Contains(buf.String(), "gate: FAILED") {
				t.Fatalf("rendered verdict must be FAILED:\n%s", buf.String())
			}
		})
	}
}

// V42: the contradiction gates FIRE — each recorded-data state cache
// semantics forbid, one fixture each: (i) hit-warm digest do_not_cache-only
// in cold; (ii) hit-warm digest failed-only in cold; (iii) warm production
// attributed to a pure-complete-hit warm digest.
func TestCalibDecompContradictionGates(t *testing.T) {
	warmHitOnly := func() *Graph {
		s := newFixtureStrings()
		return buildGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 20*ms),
			opEvent(s, 2, 1, "call", "Cls.d1", "d1", "hit", 0, 10*ms),
		})
	}
	coldWith := func(outcome string) *Graph {
		s := newFixtureStrings()
		return buildGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			opEvent(s, 2, 1, "call", "Cls.d1", "d1", outcome, 0, 50*ms),
		})
	}

	t.Run("do_not_cache-only-in-cold", func(t *testing.T) {
		cal, err := RunCachedCalibration(coldWith("do_not_cache"), warmHitOnly(), 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		gerr := cal.GateErr()
		if gerr == nil || !strings.Contains(gerr.Error(), "do_not_cache-only") {
			t.Fatalf("dnc contradiction must fire, got %v", gerr)
		}
	})
	t.Run("failed-only-in-cold", func(t *testing.T) {
		cal, err := RunCachedCalibration(coldWith("error"), warmHitOnly(), 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		gerr := cal.GateErr()
		if gerr == nil || !strings.Contains(gerr.Error(), "failed-only") {
			t.Fatalf("failed-only contradiction must fire, got %v", gerr)
		}
	})
	t.Run("production-under-pure-hit", func(t *testing.T) {
		cs := newFixtureStrings()
		cold := buildGraph(t, cs, []wcprof.DumpEvent{
			opEvent(cs, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			opEvent(cs, 2, 1, "call", "Cls.d1", "d1", "executed", 0, 50*ms),
			opEvent(cs, 3, 2, "call_exec", "Cls.d1", "d1", "ok", 0, 50*ms),
		})
		ws := newFixtureStrings()
		warm := buildGraph(t, ws, []wcprof.DumpEvent{
			opEvent(ws, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			opEvent(ws, 2, 1, "call", "Cls.d1", "d1", "hit", 0, 10*ms),
			// production of d1 recorded in the SAME warm capture whose every
			// d1 call hit complete — forbidden by cache semantics
			opEvent(ws, 3, 1, "lazy", "Cls.d1", "d1", "ok", 20*ms, 80*ms),
		})
		cal, err := RunCachedCalibration(cold, warm, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if l := cal.Decomp.WarmLedger.Lines[calibBucketHitProduction]; l.Ops != 1 || l.SelfNS != 60*ms {
			t.Fatalf("hit-production bucket = %+v, want 1 op / 60ms", l)
		}
		gerr := cal.GateErr()
		if gerr == nil || !strings.Contains(gerr.Error(), "STARTING AFTER the digest's complete hit ended") {
			t.Fatalf("hit-production contradiction must fire, got %v", gerr)
		}
	})
	// Production recorded BEFORE the digest's complete hit is the legitimate
	// forced-earlier-this-capture shape (a parent-chain materialization ran,
	// after which the call hit complete): a REPORTED bucket, never a
	// contradiction (reviewer finding 1).
	t.Run("production-before-hit-is-legitimate", func(t *testing.T) {
		cs := newFixtureStrings()
		cold := buildGraph(t, cs, []wcprof.DumpEvent{
			opEvent(cs, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			opEvent(cs, 2, 1, "call", "Cls.d1", "d1", "executed", 0, 50*ms),
			opEvent(cs, 3, 2, "call_exec", "Cls.d1", "d1", "ok", 0, 50*ms),
		})
		ws := newFixtureStrings()
		warm := buildGraph(t, ws, []wcprof.DumpEvent{
			opEvent(ws, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			// d1's deferred production forced first (e.g. via a child recipe
			// materializing its parent), THEN the call hits complete.
			opEvent(ws, 2, 1, "lazy", "Cls.d1", "d1", "ok", 0, 50*ms),
			opEvent(ws, 3, 1, "call", "Cls.d1", "d1", "hit", 60*ms, 70*ms),
		})
		cal, err := RunCachedCalibration(cold, warm, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if l := cal.Decomp.WarmLedger.Lines[calibBucketPreHitProduction]; l.Ops != 1 || l.SelfNS != 50*ms {
			t.Fatalf("pre-hit production bucket = %+v, want 1 op / 50ms", l)
		}
		if err := cal.GateErr(); err != nil {
			t.Fatalf("production before the hit must not gate: %v", err)
		}
	})
	// The contradiction scan is independent of ledger nesting: post-hit
	// production NESTED inside another digest's region still fires the gate
	// even though outermost-wins assigns its seconds to the outer producer
	// (reviewer finding 2).
	t.Run("nested-post-hit-production-still-fires", func(t *testing.T) {
		cs := newFixtureStrings()
		cold := buildGraph(t, cs, []wcprof.DumpEvent{
			opEvent(cs, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
			opEvent(cs, 2, 1, "call", "Cls.d1", "d1", "executed", 0, 50*ms),
			opEvent(cs, 3, 2, "call_exec", "Cls.d1", "d1", "ok", 0, 50*ms),
			opEvent(cs, 4, 1, "call", "Cls.dD", "dD", "executed", 60*ms, 200*ms),
			opEvent(cs, 5, 4, "call_exec", "Cls.dD", "dD", "ok", 60*ms, 200*ms),
		})
		ws := newFixtureStrings()
		warm := buildGraph(t, ws, []wcprof.DumpEvent{
			opEvent(ws, 1, 0, "session_phase", "session.query", "", "ok", 0, 300*ms),
			opEvent(ws, 2, 1, "call", "Cls.d1", "d1", "hit", 0, 10*ms),
			opEvent(ws, 3, 1, "call", "Cls.dD", "dD", "executed", 20*ms, 100*ms),
			opEvent(ws, 4, 3, "call_exec", "Cls.dD", "dD", "ok", 20*ms, 100*ms),
			// d1's production nested inside dD's region, AFTER d1's hit ended
			opEvent(ws, 5, 4, "lazy", "Cls.d1", "d1", "ok", 30*ms, 80*ms),
		})
		cal, err := RunCachedCalibration(cold, warm, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		// Outermost-wins: the nested lazy's seconds belong to dD's bucket, so
		// the hit-production LEDGER bucket stays empty…
		if l := cal.Decomp.WarmLedger.Lines[calibBucketHitProduction]; l.Ops != 0 {
			t.Fatalf("nested production must stay with the outer producer's bucket, got %+v", l)
		}
		// …but the per-digest scan fires the gate regardless.
		gerr := cal.GateErr()
		if gerr == nil || !strings.Contains(gerr.Error(), "STARTING AFTER the digest's complete hit ended") {
			t.Fatalf("nested post-hit contradiction must fire, got %v", gerr)
		}
	})
	// Open production of a PURE-HIT digest: the open-at-capture
	// classification wins over the pre-hit/post-hit production split — an
	// incomplete producing interval supports no timing claim (delta-review
	// finding 1).
	t.Run("open-production-of-pure-hit-digest-classifies-open", func(t *testing.T) {
		cs := newFixtureStrings()
		cold := buildGraph(t, cs, []wcprof.DumpEvent{
			opEvent(cs, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			opEvent(cs, 2, 1, "call", "Cls.d1", "d1", "executed", 0, 50*ms),
			opEvent(cs, 3, 2, "call_exec", "Cls.d1", "d1", "ok", 0, 50*ms),
		})
		ws := newFixtureStrings()
		wsEvents := []wcprof.DumpEvent{
			opEvent(ws, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			opEvent(ws, 2, 1, "call", "Cls.d1", "d1", "hit", 60*ms, 70*ms),
		}
		header := &wcprof.DumpHeader{
			SchemaVersion:  wcprof.DumpSchemaVersion,
			DumpedUnixNano: 100 * ms,
			Strings:        ws.values,
			EventCount:     len(wsEvents),
			OpenOps: []wcprof.DumpOpenOp{
				// d1's production open across the hit — incomplete interval
				{OpID: 3, ParentID: 1, Kind: "lazy", ClassID: ws.id("Cls.d1"), IdentID: ws.id("d1"), StartNS: 0},
			},
		}
		warm, err := Build(header, wsEvents)
		if err != nil {
			t.Fatal(err)
		}
		cal, err := RunCachedCalibration(cold, warm, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if l := cal.Decomp.WarmLedger.Lines[calibBucketOpenAtCapture]; l.Ops != 1 {
			t.Fatalf("open production of a pure-hit digest must classify open-at-capture, got %+v", l)
		}
		if l := cal.Decomp.WarmLedger.Lines[calibBucketPreHitProduction]; l.Ops != 0 {
			t.Fatalf("no timing claim may be made on an open producing interval, got %+v", l)
		}
		// The open lazy STARTED at 0, before the hit ended: no contradiction.
		if err := cal.GateErr(); err != nil {
			t.Fatal(err)
		}
	})
	// A same-ident call_exec under a HIT call is structurally forbidden at
	// ANY recorded time (a hit returns before any execution op is minted) —
	// the scan must not exempt it the way region construction exempts
	// anchored call_execs under their executor calls, and the timing rule
	// must not launder an OVERLAPPING child into "legitimate pre-hit"
	// production (delta-review findings 2 and 6).
	t.Run("call-exec-under-hit-call-fires", func(t *testing.T) {
		for _, tc := range []struct {
			name               string
			execStart, execEnd int64
		}{
			{"after-the-hit", 10 * ms, 20 * ms},
			{"overlapping-the-hit", 0, 10 * ms},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cs := newFixtureStrings()
				cold := buildGraph(t, cs, []wcprof.DumpEvent{
					opEvent(cs, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
					opEvent(cs, 2, 1, "call", "Cls.d1", "d1", "executed", 0, 50*ms),
					opEvent(cs, 3, 2, "call_exec", "Cls.d1", "d1", "ok", 0, 50*ms),
				})
				ws := newFixtureStrings()
				warm := buildGraph(t, ws, []wcprof.DumpEvent{
					opEvent(ws, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
					opEvent(ws, 2, 1, "call", "Cls.d1", "d1", "hit", 0, 10*ms),
					opEvent(ws, 3, 2, "call_exec", "Cls.d1", "d1", "ok", tc.execStart, tc.execEnd),
				})
				cal, err := RunCachedCalibration(cold, warm, 0, 10)
				if err != nil {
					t.Fatal(err)
				}
				// The ledger owns its seconds loudly — never the session
				// fallback, never the "legitimate pre-hit" label…
				if l := cal.Decomp.WarmLedger.Lines[calibBucketHitProduction]; l.Ops != 1 || l.SelfNS != 10*ms {
					t.Fatalf("hit-production bucket = %+v, want the rogue call_exec (1 op / 10ms)", l)
				}
				// …and the scan fires the gate on the structural relation.
				gerr := cal.GateErr()
				if gerr == nil || !strings.Contains(gerr.Error(), "same-ident call_exec child") {
					t.Fatalf("call_exec under a hit call must fire the contradiction, got %v", gerr)
				}
			})
		}
	})
	// Open attributed production folds into the digest classification
	// (reviewer finding 3): a digest with an ended executed call but an OPEN
	// production region is not fully recorded — open-at-capture, not
	// executed.
	t.Run("open-production-classifies-open", func(t *testing.T) {
		cs := newFixtureStrings()
		cold := buildGraph(t, cs, []wcprof.DumpEvent{
			opEvent(cs, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			opEvent(cs, 2, 1, "call", "Cls.d1", "d1", "executed", 0, 50*ms),
			opEvent(cs, 3, 2, "call_exec", "Cls.d1", "d1", "ok", 0, 50*ms),
			opEvent(cs, 4, 1, "call", "Cls.dO", "dO", "executed", 50*ms, 60*ms),
		})
		ws := newFixtureStrings()
		wsEvents := []wcprof.DumpEvent{
			opEvent(ws, 1, 0, "session_phase", "session.query", "", "ok", 0, 100*ms),
			opEvent(ws, 2, 1, "call", "Cls.d1", "d1", "hit", 0, 10*ms),
			opEvent(ws, 3, 1, "call", "Cls.dO", "dO", "executed", 20*ms, 30*ms),
		}
		header := &wcprof.DumpHeader{
			SchemaVersion:  wcprof.DumpSchemaVersion,
			DumpedUnixNano: 100 * ms,
			Strings:        ws.values,
			EventCount:     len(wsEvents),
			OpenOps: []wcprof.DumpOpenOp{
				// dO's production still running at dump time
				{OpID: 4, ParentID: 3, Kind: "lazy", ClassID: ws.id("Cls.dO"), IdentID: ws.id("dO"), StartNS: 30 * ms},
			},
		}
		warm, err := Build(header, wsEvents)
		if err != nil {
			t.Fatal(err)
		}
		cal, err := RunCachedCalibration(cold, warm, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		// dO executed in both captures, but its warm production is open: the
		// warm ledger classifies it open-at-capture (call + open lazy), never
		// exec-both with a half-recorded price.
		if l := cal.Decomp.WarmLedger.Lines[calibBucketOpenAtCapture]; l.Ops != 2 {
			t.Fatalf("open-at-capture = %+v, want the dO call + its open production (2 ops)", l)
		}
		if l := cal.Decomp.WarmLedger.Lines[calibBucketExecBoth]; l.Ops != 0 {
			t.Fatalf("a digest with open production must not classify executed, got %+v", l)
		}
		if err := cal.GateErr(); err != nil {
			t.Fatal(err)
		}
	})
}

// V44: warm-only hit provenance (native) — a hit on a result that existed
// before the warm run began (rid ≤ cold max: monotonic allocation, imports
// at engine start) is reported LOUDLY and expected 0; a rid beyond the cold
// capture's recorded range is CONSISTENT WITH an intra-warm derivation but
// not proven (imported persisted results can carry such ids). Neither gates.
func TestCalibDecompWarmOnlyHitProvenance(t *testing.T) {
	coldG, warmG := decompFixtures(t)
	cal, err := RunCachedCalibration(coldG, warmG, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	d := cal.Decomp
	if d.WarmOnlyHitsIntraWarm != 1 || len(d.WarmOnlyHitsCrossRun) != 1 || d.WarmOnlyHitsCrossRun[0] != "d9x" || d.WarmOnlyHitsNoRID != 0 {
		t.Fatalf("provenance split = intra %d cross %v norid %d, want 1 / [d9x] / 0",
			d.WarmOnlyHitsIntraWarm, d.WarmOnlyHitsCrossRun, d.WarmOnlyHitsNoRID)
	}
	if err := cal.GateErr(); err != nil {
		t.Fatal(err) // loud, never gated: simplification #1 territory
	}
	var buf bytes.Buffer
	cal.Write(&buf)
	if !strings.Contains(buf.String(), "hits on results that existed BEFORE the warm run began (rid within the cold capture's recorded range): 1") {
		t.Fatalf("cross-run equivalence line missing:\n%s", buf.String())
	}
}
