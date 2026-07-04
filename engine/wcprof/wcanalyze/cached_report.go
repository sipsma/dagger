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
					Label:         cands[i].label,
					Digests:       len(cands[i].idents),
					RemovedSelfNS: res.ElidedSelfNS + res.HitCallSelfNS,
					KeptSelfNS:    res.KeptSelfNS,
					GateBad:       err != nil || sim.ElidedOpDemanded > 0,
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
	// ChainSim exposes the cached simulation's per-op times for rendering the
	// chain.
	sim *Simulation
	// ElidedOpDemanded > 0 fails the section's gate (design §3.5).
	ElidedOpDemanded int
}

// RunCachedDetail resolves and simulates one explicit hypothesis.
func RunCachedDetail(g *Graph, hyp CachedHypothesis, chainDepth int) (*CachedDetail, error) {
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
		ElidedOpDemanded: sim.ElidedOpDemanded,
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
func (d *CachedDetail) Write(w io.Writer) {
	res := d.Resolution
	fmt.Fprintf(w, "what-if-cached: explicit hypothesis (%d digest(s), pull cost %s)\n\n",
		len(res.Idents), fmtDur(res.PullCostNS))
	saved := d.BaselineNS - d.MakespanNS
	pct := float64(0)
	if d.BaselineNS > 0 {
		pct = 100 * float64(saved) / float64(d.BaselineNS)
	}
	fmt.Fprintf(w, "baseline makespan: %s   counterfactual: %s   saved: %s (%.1f%%)\n\n",
		fmtDur(d.BaselineNS), fmtDur(d.MakespanNS), fmtDur(saved), pct)

	fmt.Fprintf(w, "eligibility:\n")
	for i := range res.Idents {
		el := &res.Idents[i]
		fmt.Fprintf(w, "  %s: %s", el.Ident, el.State)
		if el.State == IdentEligible {
			fmt.Fprintf(w, " — %d call(s) (%d hit, %d success, %d failed) -> %d hit-reported (%d instant, %d covered by elision), %d kept as recorded; %d region(s) elided, %d kept",
				el.Calls, el.Hits, el.Successes, el.Failures,
				el.ShortCircuited+el.ElidedCalls, el.ShortCircuited, el.ElidedCalls,
				el.KeptCalls, el.RegionsElided, el.RegionsKept)
			if el.UnanchoredExecs > 0 {
				fmt.Fprintf(w, "; %d call_exec(s) outside every region (still run)", el.UnanchoredExecs)
			}
		}
		fmt.Fprintf(w, "\n")
	}
	fmt.Fprintf(w, "\n")

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
	if res.OrphanWaitsIntoElided > 0 {
		fmt.Fprintf(w, "; %d orphan wait(s) into elided ops (%s) — unmodeled demand hint",
			res.OrphanWaitsIntoElided, fmtDur(res.OrphanWaitNSIntoElided))
	}
	fmt.Fprintf(w, "\n\n")

	if err := d.GateErr(); err != nil {
		fmt.Fprintf(w, "%v\n\n", err)
	}

	if len(d.Chain) > 1 {
		fmt.Fprintf(w, "counterfactual blocking chain (what the new bottleneck would be):\n\n")
		for _, op := range d.Chain {
			start, finish := d.sim.SimTimes(op)
			fmt.Fprintf(w, "  %-12s %-50s sim=[%s..%s]\n",
				op.Kind, truncate(op.Class, 50), fmtDur(start), fmtDur(finish))
		}
		fmt.Fprintf(w, "\n")
	}
}
