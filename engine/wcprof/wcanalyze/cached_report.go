package wcanalyze

import (
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// What-if-cached reporting (design §3.4): the default-on ranking table that
// mirrors the existing what-if table, and the explicit-set detail section for
// --cached hypotheses. Savings are measured by re-simulation, never by
// subtracting elided durations (elided work off the critical path saves
// nothing; on it, a second chain can take over).

// WhatIfCachedRow is one ranking row: a candidate digest group simulated as
// cached.
type WhatIfCachedRow struct {
	Label   string
	Digests int
	// RemovedSelfNS is the self-time the hypothesis removes from the schedule:
	// elided-region work PLUS the short-circuited calls' own self (the split
	// is visible in the detail section's residual line). KeptSelfNS is work
	// something live demanded, kept as recorded.
	RemovedSelfNS int64
	KeptSelfNS    int64
	// SavedNS is baseline minus the candidate's simulated makespan.
	SavedNS int64
	// GateBad marks a simulation that demanded an elided op — unfaithful data
	// (never expected; rendered loudly, not dropped).
	GateBad bool
	// DegradedEvidence marks a row whose hypothesis was touched by degraded
	// forced-fact evidence: an unrecorded-target fact's containment keep
	// altered the row, or an orphan fact (unknown forcer) names production
	// this row elides — rendered loudly on the row, never just in the detail
	// section.
	DegradedEvidence bool
}

// cachedRefusalErr is the what-if-cached admission gate over a graph's
// capture provenance (doctrine: refuse, never answer on data that may have
// silently lost the facts the answer depends on). DroppedEvents means the
// recorder evicted events — any of them could have been the demand evidence
// that keeps a region — and SuppressedIdentDerivations means the emit
// omitted lazy idents/facts it should have carried (expected 0; nonzero is
// a broken emit to fix). The general report may still render with warnings;
// the cached sections refuse. Uninstrumented-forcer suppressions do NOT
// refuse — they are a declared model boundary — and print as a caveat.
func cachedRefusalErr(g *Graph) error {
	var clauses []string
	if g.DroppedEvents > 0 {
		clauses = append(clauses, fmt.Sprintf("%d recorder event(s) dropped — elision demand evidence may be missing (this capture records %d orphan wait(s) and %d orphan forced fact(s), demand whose owner is already unknown); recapture with a larger wcprof buffer",
			g.DroppedEvents, len(g.OrphanWaits), len(g.OrphanForcedFacts)))
	}
	if g.SuppressedIdentDerivations > 0 {
		clauses = append(clauses, fmt.Sprintf("%d lazy ident derivation failure(s) at emit time — lazy idents and forced facts were omitted, so elision sourcing and demand evidence are incomplete; this counter is expected to be 0 (the digest is memoized from the cache lookup), so a nonzero value is an emit bug to fix, never data to analyze around",
			g.SuppressedIdentDerivations))
	}
	if len(clauses) == 0 {
		return nil
	}
	// Every violated condition in ONE refusal — the first problem must not
	// hide the rest — and the declared-boundary caveat rides along when it
	// fired too (the refusal path never reaches the section that prints it).
	msg := "what-if-cached REFUSED: " + strings.Join(clauses, "; AND ")
	if cav := cachedForcerCaveat(g); cav != "" {
		msg += "; additionally, " + cav
	}
	return fmt.Errorf("%s", msg)
}

// cachedForcerCaveat renders the declared-boundary caveat for forced facts
// not emitted because the forcer was outside the instrumented op graph:
// savings for regions such a forcer demanded may be overstated. Printed in
// every what-if-cached section when nonzero; empty string otherwise.
func cachedForcerCaveat(g *Graph) string {
	if g.SuppressedUninstrumentedForcers == 0 {
		return ""
	}
	return fmt.Sprintf("CAVEAT: %d forced-evaluation fact(s) were not recorded because the forcing context carried no instrumented op (demand from outside the recorded op graph — a declared model boundary): savings may be overstated for production such forcers demanded.",
		g.SuppressedUninstrumentedForcers)
}

// maxWhatIfCachedDigests bounds the individual-digest candidates (the
// per-class groups are bounded by the class count, itself bounded like
// maxWhatIfClasses).
const maxWhatIfCachedDigests = 15

// RunWhatIfCached ranks candidate digest groups — one group per call class
// with executed digests, plus the top individual executed digests by
// producing wall-clock — by simulated makespan saving at pull cost 0.
// baselineNS is the already-computed baseline makespan. Simulations run in
// parallel; every row is a full re-simulation of one hypothesis.
func RunWhatIfCached(g *Graph, baselineNS int64) []WhatIfCachedRow {
	executed := executedIdents(g)
	if len(executed) == 0 {
		return nil
	}

	idents := make([]string, 0, len(executed))
	for d := range executed {
		idents = append(idents, d)
	}
	slices.Sort(idents)

	// Per-class groups.
	byClass := make(map[string][]string)
	for _, d := range idents {
		c := executed[d].class
		byClass[c] = append(byClass[c], d)
	}
	classes := make([]string, 0, len(byClass))
	for c := range byClass {
		classes = append(classes, c)
	}
	// Order classes by total producing weight (bounded like the what-if table).
	classWeight := make(map[string]int64, len(classes))
	for c, ds := range byClass {
		for _, d := range ds {
			classWeight[c] += executed[d].weightNS
		}
	}
	slices.SortFunc(classes, func(a, b string) int {
		if classWeight[a] != classWeight[b] {
			return int(classWeight[b] - classWeight[a])
		}
		return strings.Compare(a, b)
	})
	if len(classes) > maxWhatIfClasses {
		classes = classes[:maxWhatIfClasses]
	}

	// Top individual digests by producing wall-clock.
	top := slices.Clone(idents)
	slices.SortFunc(top, func(a, b string) int {
		if executed[a].weightNS != executed[b].weightNS {
			return int(executed[b].weightNS - executed[a].weightNS)
		}
		return strings.Compare(a, b)
	})
	if len(top) > maxWhatIfCachedDigests {
		top = top[:maxWhatIfCachedDigests]
	}

	type candidate struct {
		label  string
		idents []string
	}
	var cands []candidate
	for _, c := range classes {
		ds := byClass[c]
		label := fmt.Sprintf("%s (all %d executed digests)", c, len(ds))
		if len(ds) == 1 {
			label = fmt.Sprintf("%s (1 executed digest)", c)
		}
		cands = append(cands, candidate{label: label, idents: ds})
	}
	for _, d := range top {
		cands = append(cands, candidate{
			label:  fmt.Sprintf("%s %s", executed[d].class, d),
			idents: []string{d},
		})
	}

	rows := make([]WhatIfCachedRow, len(cands))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(runtime.GOMAXPROCS(0), 8); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				res := ResolveCachedHypothesis(g, NewCachedHypothesis(cands[i].idents, 0))
				sim := NewCachedSimulation(g, res)
				makespan, err := sim.Run()
				row := WhatIfCachedRow{
					Label:            cands[i].label,
					Digests:          len(cands[i].idents),
					RemovedSelfNS:    res.ElidedSelfNS + res.HitCallSelfNS,
					KeptSelfNS:       res.KeptSelfNS,
					GateBad:          err != nil || sim.ElidedOpDemanded > 0,
					DegradedEvidence: res.UnresolvedContainmentKeeps > 0 || res.OrphanForcedFactsIntoElided > 0,
				}
				if err == nil {
					row.SavedNS = baselineNS - makespan
				}
				rows[i] = row
			}
		}()
	}
	for i := range cands {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	slices.SortStableFunc(rows, func(a, b WhatIfCachedRow) int {
		if a.SavedNS != b.SavedNS {
			return int(b.SavedNS - a.SavedNS)
		}
		return strings.Compare(a.Label, b.Label)
	})
	return rows
}

// writeWhatIfCachedRanking renders the ranking table (default-on when any
// executed calls exist — the report section wired into WriteReport).
func writeWhatIfCachedRanking(w io.Writer, rows []WhatIfCachedRow, topRows int) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(w, "what-if-cached: makespan saved if these results had been cache hits\n")
	fmt.Fprintf(w, "(digest-level: every caller of the digest hits and its producing subtree elides\n")
	fmt.Fprintf(w, " whole, or is kept when externally demanded; savings are NOT additive across rows;\n")
	fmt.Fprintf(w, " removed-self = elided-region self + hit calls' own self; per-class rows plus the\n")
	fmt.Fprintf(w, " top %d individual digests by producing wall-clock)\n\n", maxWhatIfCachedDigests)
	fmt.Fprintf(w, "%-70s %8s %12s %12s %13s\n", "class/digest", "digests", "removed-self", "kept-self", "save@pull=0")
	shown := 0
	for _, row := range rows {
		if shown >= topRows {
			break
		}
		if row.SavedNS <= 0 && shown > 5 {
			continue
		}
		gate := ""
		if row.GateBad {
			gate = "  GATE-FAILED (elided op demanded: unfaithful data)"
		}
		if row.DegradedEvidence {
			gate += "  DEGRADED-EVIDENCE (unrecorded-target/orphan forced facts touch this hypothesis)"
		}
		fmt.Fprintf(w, "%-70s %8d %12s %12s %13s%s\n",
			truncate(row.Label, 70), row.Digests,
			fmtDur(row.RemovedSelfNS), fmtDur(row.KeptSelfNS), fmtDur(row.SavedNS), gate)
		shown++
	}
	fmt.Fprintf(w, "\n")
}

// WriteCachedSelectionDetail resolves the what-if-cached selectors (no-op for
// an empty selection) and renders the explicit-set detail section — the one
// shared CLI entry point for both analyzers. A resolution failure (unknown
// digest, empty match — V17) or a gate violation (ElidedOpDemanded, §3.5) is
// returned as an error so the command exits non-zero: the counterfactual
// number is then invalid, per the never-compensate doctrine.
func WriteCachedSelectionDetail(w io.Writer, g *Graph, sel CachedSelection, chainDepth int) error {
	if sel.Empty() {
		return nil
	}
	hyp, notes, err := sel.Resolve(g)
	if err != nil {
		return err
	}
	for _, n := range notes {
		fmt.Fprintln(w, n)
	}
	fmt.Fprintln(w)
	detail, err := RunCachedDetail(g, hyp, chainDepth)
	if err != nil {
		return err
	}
	detail.Write(w)
	return detail.GateErr()
}

// CachedCalibration is the cold/warm calibration comparison (design §3.5
// gate 4): the cold run simulated under the warm run's ACTUAL hit set,
// against the warm run's ACTUAL makespan. The drift number is the product —
// the honest answer to whether this tool can replace the empirical cold/warm
// A/B loop — and no threshold is enforced (v1 reports; humans judge).
type CachedCalibration struct {
	Detail *CachedDetail
	// WarmHitDigests is the warm run's extracted hit-set size; FoundInRun of
	// those exist as call idents in the analyzed (cold) run — the rest are
	// listed not-found by the eligibility section, never silently dropped.
	WarmHitDigests int
	FoundInRun     int
	// WarmPendingHits counts warm digests with only PENDING-production hits
	// (lazy-semantics B2): excluded from the CachedSet — they do not witness
	// a materialized payload — and printed, never silent.
	WarmPendingHits int
	WarmActualNS    int64
}

// RunCachedCalibration extracts the warm run's hit digests and simulates the
// cold run under them at the given pull cost.
func RunCachedCalibration(coldG, warmG *Graph, pullCostNS int64, chainDepth int) (*CachedCalibration, error) {
	// The cold graph's admission gate fires inside RunCachedDetail; the warm
	// capture is gated here for the same reason — a dropped warm event could
	// have been a hit call, silently shrinking the extracted CachedSet.
	if warmG.DroppedEvents > 0 {
		return nil, fmt.Errorf("what-if-cached calibration REFUSED: the WARM capture dropped %d recorder event(s) — the extracted hit set may be silently incomplete; recapture with a larger wcprof buffer", warmG.DroppedEvents)
	}
	hits := HitDigests(warmG)
	// Pending-production hits (B2) do not witness a materialized payload:
	// counted and printed, never asserted into the B1 CachedSet (V30). A
	// digest with BOTH outcomes stays in via its complete hit.
	pendingOnly := 0
	inHits := make(map[string]struct{}, len(hits))
	for _, d := range hits {
		inHits[d] = struct{}{}
	}
	for _, d := range PendingHitDigests(warmG) {
		if _, ok := inHits[d]; !ok {
			pendingOnly++
		}
	}
	if len(hits) == 0 {
		return nil, fmt.Errorf("the warm capture records no complete cache-hit calls — not a warm run, or hits were not recorded")
	}
	detail, err := RunCachedDetail(coldG, NewCachedHypothesis(hits, pullCostNS), chainDepth)
	if err != nil {
		return nil, err
	}
	found := 0
	for i := range detail.Resolution.Idents {
		if detail.Resolution.Idents[i].State != IdentNotFound {
			found++
		}
	}
	return &CachedCalibration{
		Detail:          detail,
		WarmHitDigests:  len(hits),
		FoundInRun:      found,
		WarmPendingHits: pendingOnly,
		WarmActualNS:    ActualMakespanNS(warmG),
	}, nil
}

// Write renders the detail section followed by the calibration block.
func (c *CachedCalibration) Write(w io.Writer) {
	c.Detail.Write(w)
	fmt.Fprintf(w, "calibration: cold run simulated under the warm run's hit set\n\n")
	fmt.Fprintf(w, "  warm-run hit digests:              %d (%d found as call idents in this run)\n", c.WarmHitDigests, c.FoundInRun)
	if c.WarmPendingHits > 0 {
		fmt.Fprintf(w, "  pending-production hits excluded:  %d (recipe cached, production had not run — B2)\n", c.WarmPendingHits)
	}
	fmt.Fprintf(w, "  cold run baseline (simulated):     %s\n", fmtDur(c.Detail.BaselineNS))
	fmt.Fprintf(w, "  simulated counterfactual makespan: %s\n", fmtDur(c.Detail.MakespanNS))
	fmt.Fprintf(w, "  warm run actual makespan:          %s\n", fmtDur(c.WarmActualNS))
	if c.WarmActualNS > 0 {
		drift := 100 * float64(c.Detail.MakespanNS-c.WarmActualNS) / float64(c.WarmActualNS)
		fmt.Fprintf(w, "  drift (sim vs warm actual):        %+.1f%%\n", drift)
	}
	fmt.Fprintf(w, "  (known gap sources: unlimited-resource scheduling, uninstrumented I/O,\n")
	fmt.Fprintf(w, "   warm-run lazy decode, run-specific digests absent from the cold run)\n\n")
}

// CachedDetail is the explicit-set what-if-cached result: one hypothesis,
// full residual visibility, and the counterfactual blocking chain.
type CachedDetail struct {
	Resolution *CachedResolution
	BaselineNS int64
	MakespanNS int64
	// Chain is the counterfactual blocking chain: the constraint walk from
	// the last-finishing root of the CACHED simulation (what the new
	// bottleneck would be).
	Chain []*Op
	// sim exposes the cached simulation's per-op times for rendering the
	// chain; traceStartNS rebases them to the report's trace-relative
	// convention.
	sim          *Simulation
	traceStartNS int64
	// ElidedOpDemanded > 0 fails the section's gate (design §3.5).
	ElidedOpDemanded int
	// forcerCaveat is the declared-boundary caveat for uninstrumented-forcer
	// suppressions (empty when none); printed prominently by Write.
	forcerCaveat string
}

// RunCachedDetail resolves and simulates one explicit hypothesis.
func RunCachedDetail(g *Graph, hyp CachedHypothesis, chainDepth int) (*CachedDetail, error) {
	if err := cachedRefusalErr(g); err != nil {
		return nil, err
	}
	baseSim := NewSimulation(g, nil)
	baseline, err := baseSim.Run()
	if err != nil {
		return nil, err
	}
	res := ResolveCachedHypothesis(g, hyp)
	sim := NewCachedSimulation(g, res)
	makespan, err := sim.Run()
	if err != nil {
		return nil, err
	}
	d := &CachedDetail{
		Resolution:       res,
		BaselineNS:       baseline,
		MakespanNS:       makespan,
		sim:              sim,
		traceStartNS:     g.TraceStartNS,
		ElidedOpDemanded: sim.ElidedOpDemanded,
		forcerCaveat:     cachedForcerCaveat(g),
	}
	// Counterfactual blocking chain from the last-finishing root.
	var last *Op
	var lastFinish int64 = -1
	for _, r := range g.Roots {
		if _, f, ok := sim.simTimesOK(r); ok && f > lastFinish {
			last, lastFinish = r, f
		}
	}
	if last != nil {
		d.Chain = sim.ExplainFinish(last, chainDepth)
	}
	return d, nil
}

// GateErr returns a non-nil error iff the detail simulation violated the
// unreachability assertion (design §3.5): the hypothesis result is invalid
// and the caller must fail loudly.
func (d *CachedDetail) GateErr() error {
	if d.ElidedOpDemanded > 0 {
		return fmt.Errorf("what-if-cached gate FAILED: %d demand(s) of elided ops — the recorded structure contradicts the static resolution (unfaithful data); the counterfactual makespan is not valid", d.ElidedOpDemanded)
	}
	return nil
}

// Write renders the explicit-set detail section.
// eligibilityListLimit is the ident count above which the eligibility section
// summarizes per state instead of listing every ident — the calibration form,
// where hypotheses carry a whole warm run's hit set. Counts stay exact and
// every non-eligible state is still listed or sampled; only the per-ident
// enumeration is elided, and the summary says how much.
const eligibilityListLimit = 24

func writeEligibility(w io.Writer, res *CachedResolution) {
	fmt.Fprintf(w, "eligibility:\n")
	if len(res.Idents) > eligibilityListLimit {
		counts := map[IdentState]int{}
		for i := range res.Idents {
			counts[res.Idents[i].State]++
		}
		for _, state := range []IdentState{IdentEligible, IdentAllHit, IdentNotFound, IdentDoNotCache, IdentOpen, IdentUnknownOutcome, IdentFailedOnly} {
			n := counts[state]
			if n == 0 {
				continue
			}
			fmt.Fprintf(w, "  %d digest(s): %s", n, state)
			// Ineligible states beyond not-found are rare and worth naming.
			if state != IdentEligible && state != IdentNotFound && state != IdentAllHit {
				shown := 0
				for i := range res.Idents {
					if res.Idents[i].State != state || shown >= 8 {
						continue
					}
					if shown == 0 {
						fmt.Fprintf(w, " —")
					}
					fmt.Fprintf(w, " %s", res.Idents[i].Ident)
					shown++
				}
			}
			fmt.Fprintf(w, "\n")
		}
		fmt.Fprintf(w, "  (per-ident lines elided above %d digests; counts are exact)\n\n", eligibilityListLimit)
		return
	}
	for i := range res.Idents {
		el := &res.Idents[i]
		fmt.Fprintf(w, "  %s: %s", el.Ident, el.State)
		if el.State == IdentEligible {
			fmt.Fprintf(w, " — %d call(s) (%d hit, %d success, %d failed) -> %d hit-reported (%d instant, %d covered by elision), %d kept as recorded; %d region(s) elided, %d kept",
				el.Calls, el.Hits, el.Successes, el.Failures,
				el.ShortCircuited+el.ElidedCalls, el.ShortCircuited, el.ElidedCalls,
				el.KeptCalls, el.RegionsElided, el.RegionsKept)
			if el.PendingHits > 0 {
				fmt.Fprintf(w, "; %d of the hits pending-production (B2)", el.PendingHits)
			}
		}
		fmt.Fprintf(w, "\n")
	}
	fmt.Fprintf(w, "\n")
}

func (d *CachedDetail) Write(w io.Writer) {
	res := d.Resolution
	fmt.Fprintf(w, "what-if-cached: explicit hypothesis (%d digest(s), pull cost %s)\n\n",
		len(res.Idents), fmtDur(res.PullCostNS))
	if d.forcerCaveat != "" {
		fmt.Fprintf(w, "%s\n\n", d.forcerCaveat)
	}
	saved := d.BaselineNS - d.MakespanNS
	pct := float64(0)
	if d.BaselineNS > 0 {
		pct = 100 * float64(saved) / float64(d.BaselineNS)
	}
	fmt.Fprintf(w, "baseline makespan: %s   counterfactual: %s   saved: %s (%.1f%%)\n\n",
		fmtDur(d.BaselineNS), fmtDur(d.MakespanNS), fmtDur(saved), pct)

	writeEligibility(w, res)

	if len(res.KeptRegions) > 0 {
		fmt.Fprintf(w, "kept regions (work the hypothesis could NOT remove):\n")
		for _, kr := range res.KeptRegions {
			fmt.Fprintf(w, "  kept: %s %s — %d ops, %s self — %s", kr.Root.Class, kr.Ident, kr.Ops, fmtDur(kr.SelfNS), kr.Reason)
			if kr.Demander != nil {
				reason := ""
				if kr.DemandWait != nil {
					reason = fmt.Sprintf(" (wait: %s)", kr.DemandWait.Reason)
				}
				fmt.Fprintf(w, " by %s %s%s", kr.Demander.Kind, truncate(kr.Demander.Class, 50), reason)
			}
			fmt.Fprintf(w, "\n")
		}
		fmt.Fprintf(w, "\n")
	}

	fmt.Fprintf(w, "residuals: %d op(s) elided (%s self)", res.ElidedOps, fmtDur(res.ElidedSelfNS))
	if res.HitCallSelfNS > 0 {
		fmt.Fprintf(w, "; %s hit-call self removed", fmtDur(res.HitCallSelfNS))
	}
	if res.WaivedProductionWaits > 0 {
		fmt.Fprintf(w, "; %d production wait(s) waived (deferred production removed, A1)", res.WaivedProductionWaits)
	}
	if res.OrphanWaitsIntoElided > 0 {
		fmt.Fprintf(w, "; %d orphan wait(s) into elided ops (%s) — unmodeled demand hint",
			res.OrphanWaitsIntoElided, fmtDur(res.OrphanWaitNSIntoElided))
	}
	if res.ForcedFacts > 0 {
		fmt.Fprintf(w, "; %d forced fact(s) consumed", res.ForcedFacts)
		if res.ForcedFactsUnresolved > 0 {
			fmt.Fprintf(w, " (%d unrecorded-target, demand by recorded-ident containment)", res.ForcedFactsUnresolved)
		}
	}
	if res.OrphanForcedFacts > 0 {
		fmt.Fprintf(w, "; %d orphan forced fact(s), no known forcer", res.OrphanForcedFacts)
		if res.OrphanForcedFactsIntoElided > 0 {
			fmt.Fprintf(w, " (%d naming elided production — unmodeled demand hint)", res.OrphanForcedFactsIntoElided)
		}
	}
	fmt.Fprintf(w, "\n\n")

	if err := d.GateErr(); err != nil {
		fmt.Fprintf(w, "%v\n\n", err)
	}
	// The counterfactual sim's data-faithfulness diagnostics, printed with
	// the same visibility the baseline report gives its own (zero on
	// faithful data; non-zero means order-dependent or fallback-anchored
	// savings for the affected ops).
	if d.sim.UnschedulableOps > 0 || d.sim.SimStartConflicts > 0 || d.sim.CycleWarnings > 0 {
		fmt.Fprintf(w, "counterfactual sim diagnostics: %d broken cycles, %d unschedulable ops, %d start conflicts\n\n",
			d.sim.CycleWarnings, d.sim.UnschedulableOps, d.sim.SimStartConflicts)
	}

	if len(d.Chain) > 1 {
		fmt.Fprintf(w, "counterfactual blocking chain (what the new bottleneck would be):\n\n")
		for _, op := range d.Chain {
			start, finish := d.sim.SimTimes(op)
			// trace-relative, matching the report's blocking-chain convention
			fmt.Fprintf(w, "  %-12s %-50s sim=[%s..%s]\n",
				op.Kind, truncate(op.Class, 50), fmtDur(start-d.traceStartNS), fmtDur(finish-d.traceStartNS))
		}
		fmt.Fprintf(w, "\n")
	}
}
