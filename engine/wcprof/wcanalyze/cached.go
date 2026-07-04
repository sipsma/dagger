package wcanalyze

import (
	"slices"
	"sort"

	"github.com/dagger/dagger/engine/wcprof"
)

// This file is the what-if-cached elision engine (design:
// hack/designs/whatif-cached-design.md §3): simulate a recorded run under the
// counterfactual that a set of recipe digests had been cache hits.
//
// A hypothesis is digest-level: every call op of a cached ident — executor and
// joiners alike — becomes a local warm hit (finish = simStart + pullCost; v1
// pull cost 0), and the producing subtree under each such call is elided as a
// unit. Elision is resolved STATICALLY here, before the replay runs: a
// finish()-time skip cannot remove a subtree from the schedule (any outside
// wait into it resurrects it through the replay's prefix anchoring), and
// per-reference runtime guards would make anchors evaluation-order-dependent —
// the order-dependence the replay treats as a correctness failure
// (SimStartConflicts). A region anything live still demands is NOT partially
// elided: it is kept whole, replays exactly as recorded, and is reported
// (whole-region elide-or-keep, loud residuals).

// CachedHypothesis is a what-if-cached hypothesis: the recipe digests (wcprof
// Idents of call ops) assumed present in the cache, plus the simulated cost of
// a hit (v1: 0 — the local-warm-hit model; the pull-cost seam).
type CachedHypothesis struct {
	Idents     map[string]struct{}
	PullCostNS int64
}

// NewCachedHypothesis builds a hypothesis over the given digests at pull cost
// pullCostNS.
func NewCachedHypothesis(idents []string, pullCostNS int64) CachedHypothesis {
	set := make(map[string]struct{}, len(idents))
	for _, d := range idents {
		set[d] = struct{}{}
	}
	return CachedHypothesis{Idents: set, PullCostNS: pullCostNS}
}

// IdentState classifies a hypothesized ident's eligibility (design §3.2).
// Ineligible idents are reported loudly and contribute nothing to the
// simulation: their calls replay exactly as the baseline.
type IdentState uint8

const (
	// IdentEligible: the ident has at least one successful non-hit call; its
	// calls short-circuit to hits and its producing regions are elision
	// candidates.
	IdentEligible IdentState = iota
	// IdentNotFound: no call op with this ident exists in the trace.
	IdentNotFound
	// IdentDoNotCache: the engine refused to cache this call; simulating it
	// cached is fiction, refused loudly.
	IdentDoNotCache
	// IdentOpen: the ident has call ops, or producing-region ops, still open
	// at dump time — the production is not fully recorded, so its removal is
	// not determinable.
	IdentOpen
	// IdentUnknownOutcome: an ended call op carries no recorded outcome —
	// suspect data. Hypothesizing an outcome-less call into a hit would be
	// guessing, so the ident is refused loudly (doctrine §0.2). Never occurs
	// on either real source (both always stamp call outcomes).
	IdentUnknownOutcome
	// IdentFailedOnly: every non-hit call errored or was canceled; failed
	// results are not cached, so simulating them cached is fiction.
	IdentFailedOnly
	// IdentAllHit: every call was already a recorded cache hit — caching this
	// ident is a no-op, noted rather than warned.
	IdentAllHit
)

func (s IdentState) String() string {
	switch s {
	case IdentEligible:
		return "eligible"
	case IdentNotFound:
		return "not found in trace"
	case IdentDoNotCache:
		return "do_not_cache (engine refuses to cache it)"
	case IdentOpen:
		return "open at dump time (production not fully recorded)"
	case IdentUnknownOutcome:
		return "an ended call has no recorded outcome (suspect data, refused)"
	case IdentFailedOnly:
		return "all executions errored/canceled (failed results aren't cached)"
	case IdentAllHit:
		return "already all hits (no-op)"
	default:
		return "invalid"
	}
}

// IdentEligibility is the per-ident eligibility verdict plus the resolution
// tallies the report prints — nothing silently dropped or absorbed.
type IdentEligibility struct {
	Ident string
	State IdentState

	// Recorded call-op outcome counts for the ident.
	Calls           int
	Hits            int // outcome hit — untouched, they were already lookups
	Successes       int // executed / joined / ok (the OTel non-hit success)
	Failures        int // error / canceled
	DoNotCacheCalls int
	UnknownOutcomes int // outcome-less call ops: no success evidence
	OpenCalls       int

	// Resolution results (eligible idents only).
	//
	// ShortCircuited calls run as instant hits; ElidedCalls sit inside another
	// cached digest's elided region, so under the counterfactual they never
	// occur at all — which is why they carry no hitShort mark in the replay.
	// Both are SATISFIED by the hypothesis and REPORT AS HITS (catalog row
	// V12: "both roots report as hits"); the data keeps them distinct so the
	// replay state stays honest. KeptCalls replay as recorded: the hypothesis
	// could not satisfy them, and the kept-region report says why.
	ShortCircuited int // calls that become instant hits in the simulation
	ElidedCalls    int // calls covered by another cached digest's elision
	KeptCalls      int // calls inside kept regions (or kept roots), as recorded
	RegionsElided  int
	RegionsKept    int
	// UnanchoredExecs counts call_exec ops carrying this ident that sit
	// outside every producing region of the ident (an executor call op absent
	// from the data). Their work cannot be attributed to a region and keeps
	// running — a loud residual, not a silent one.
	UnanchoredExecs int
}

// KeptRegionReport describes one producing region the hypothesis could not
// elide: kept whole, replayed exactly as recorded, with the reason printed.
type KeptRegionReport struct {
	// Root is the cached call op whose producing subtree was kept; it is NOT
	// short-circuited (the region's exact replay depends on its recorded
	// timeline).
	Root  *Op
	Ident string
	// Ops and SelfNS size the kept region (strict descendants of Root; SelfNS
	// is their total self-time, each op counted once).
	Ops    int
	SelfNS int64
	Reason string
	// Demander samples the live op whose wait demanded the region (nil when
	// the keep is structural: the root sits inside another kept region).
	Demander   *Op
	DemandWait *WaitEdge
}

// CachedResolution is the statically resolved hypothesis state: per-op elision
// marks for the replay plus the eligibility/kept/residual report data.
type CachedResolution struct {
	PullCostNS int64

	// Idents holds one eligibility entry per hypothesized ident, sorted.
	Idents []IdentEligibility
	// KeptRegions lists regions external demand forced to keep, in
	// deterministic (nesting) order.
	KeptRegions []KeptRegionReport

	// Aggregate residual visibility. The elided and kept totals are unions
	// over maximal regions, so nested regions never double-count (V12);
	// KeptRegions still lists every kept region (nested included) with its
	// own reason.
	ElidedOps           int   // ops removed from the schedule (union, once each)
	ElidedSelfNS        int64 // their total self-time (union, once each)
	KeptOps             int   // ops in kept regions (union, once each)
	KeptSelfNS          int64 // their total self-time (union, once each)
	ShortCircuitedCalls int
	// HitCallSelfNS is the short-circuited calls' own recorded self-time —
	// also removed by the hypothesis (a hit replays none of the call's
	// timeline), and the ONLY removed work on captures whose producing
	// subtrees are folded into the call span (the un-augmented OTel shape).
	// Kept separate from ElidedSelfNS so region-vs-call removal stays visible.
	HitCallSelfNS int64
	ElidedRegions int
	// OrphanWaitsIntoElided counts recorded waits with no owning op that
	// target an elided op. The replay never models orphan waits (they cannot
	// gate anything), so they do not demand a keep — but the data hints at
	// unmodeled demand, so they are printed, never silent.
	OrphanWaitsIntoElided  int
	OrphanWaitNSIntoElided int64

	// Program-index-aligned replay state (nil when the hypothesis is a no-op).
	elided   []bool
	hitShort []bool
}

// Noop reports whether the resolved hypothesis changes nothing: the simulation
// is then bit-for-bit the baseline (the V1 invariance guarantee).
func (r *CachedResolution) Noop() bool {
	return r.ShortCircuitedCalls == 0 && r.ElidedOps == 0
}

// NewCachedSimulation prepares a replay of g under the resolved what-if-cached
// hypothesis. Baseline invariance: a no-op resolution (empty or fully
// ineligible hypothesis) replays bit-for-bit as NewSimulation(g, nil).
func NewCachedSimulation(g *Graph, res *CachedResolution) *Simulation {
	s := NewSimulation(g, nil)
	if res == nil {
		return s
	}
	// The resolution's arrays are dense-op-indexed in the same deterministic
	// ID-sorted order every program compilation produces, so they stay aligned
	// even if the program was recompiled (invalidateProgram) in between.
	if res.elided != nil && len(res.elided) != len(s.p.ops) {
		panic("wcanalyze: cached resolution does not match this graph")
	}
	s.Cached = res
	s.elided = res.elided
	s.hitShort = res.hitShort
	s.pullCostNS = res.PullCostNS
	return s
}

//
// structural index (once per graph, reused across candidate resolutions)
//

// cachedIndex is the per-graph structural data hypothesis resolution needs:
// subtree intervals over the nesting forest (one Euler-tour pass), the gating
// wait edges sorted for region lookup, per-ident call/call_exec indexes, and
// per-subtree open/self-time rollups. It depends only on structure — never on
// op classes — and its dense op indexing is the same deterministic ID-sorted
// order the replay program uses, so resolutions and simulations stay aligned.
type cachedIndex struct {
	p *replayProgram

	// eulerIn/eulerOut give each op's subtree as a half-open Euler interval:
	// x is a strict descendant of c iff eulerIn[c] < eulerIn[x] <= eulerOut[c].
	// -1 marks an op unreachable from the roots (a parent cycle — degenerate
	// data the rest of the replay flags on its own); such ops join no region.
	eulerIn, eulerOut []int32
	// eulerOrder maps an euler-in position back to the op index there.
	eulerOrder []int32

	// openSub[i]: op i or any descendant was open at dump time.
	openSub []bool
	// selfSub[i]: total SelfNS of op i's subtree (op included).
	selfSub []int64

	// callsByIdent / execsByIdent index call and call_exec ops by ident, in
	// deterministic (ID-sorted) order.
	callsByIdent map[string][]int32
	execsByIdent map[string][]int32

	// demandEdges holds every wait that gates as a join in the replay
	// (joinWait — the ONE predicate shared with the program compiler), sorted
	// by the target's euler-in position for region range lookups. waiter is -1
	// for an orphan wait (no owning op: unmodeled by the replay, reported but
	// never demanding).
	demandEdges []demandEdge
}

type demandEdge struct {
	targetIn int32 // eulerIn of the target op
	target   int32
	waiter   int32 // -1 for an orphan wait
	wait     *WaitEdge
}

func (g *Graph) cachedIndexOnce() *cachedIndex {
	g.cachedIdxOnce.Do(func() {
		g.cachedIdx = buildCachedIndex(g)
	})
	return g.cachedIdx
}

func buildCachedIndex(g *Graph) *cachedIndex {
	p := g.program()
	n := len(p.ops)
	idx := &cachedIndex{
		p:            p,
		eulerIn:      make([]int32, n),
		eulerOut:     make([]int32, n),
		eulerOrder:   make([]int32, n),
		openSub:      make([]bool, n),
		selfSub:      make([]int64, n),
		callsByIdent: make(map[string][]int32),
		execsByIdent: make(map[string][]int32),
	}
	for i := range idx.eulerIn {
		idx.eulerIn[i] = -1
		idx.eulerOut[i] = -1
	}

	// Iterative Euler tour over the nesting forest (children already sorted
	// deterministically by Build), with post-order open/self rollups.
	type frame struct {
		op    int32
		child int
	}
	var stack []frame
	counter := int32(0)
	enter := func(op int32) {
		idx.eulerIn[op] = counter
		idx.eulerOrder[counter] = op
		counter++
		idx.openSub[op] = p.ops[op].Open
		idx.selfSub[op] = p.ops[op].SelfNS()
		stack = append(stack, frame{op: op, child: 0})
	}
	for _, r := range p.roots {
		enter(r)
		for len(stack) > 0 {
			f := &stack[len(stack)-1]
			op := p.ops[f.op]
			if f.child < len(op.Children) {
				c := p.idxByID[op.Children[f.child].ID]
				f.child++
				enter(c)
				continue
			}
			idx.eulerOut[f.op] = counter - 1
			stack = stack[:len(stack)-1]
			if len(stack) > 0 {
				par := stack[len(stack)-1].op
				idx.openSub[par] = idx.openSub[par] || idx.openSub[f.op]
				idx.selfSub[par] += idx.selfSub[f.op]
			}
		}
	}

	callKind := wcprof.OpKindCall.String()
	execKind := wcprof.OpKindCallExec.String()
	for i := int32(0); i < int32(n); i++ {
		op := p.ops[i]
		if op.Ident == "" {
			continue
		}
		switch op.Kind {
		case callKind:
			idx.callsByIdent[op.Ident] = append(idx.callsByIdent[op.Ident], i)
		case execKind:
			idx.execsByIdent[op.Ident] = append(idx.execsByIdent[op.Ident], i)
		}
	}

	// Gating waits, owned and orphan, indexed by target position. Iterated in
	// the deterministic dense-op order (never map order).
	for i := int32(0); i < int32(n); i++ {
		for _, w := range p.ops[i].Waits {
			if !joinWait(w, p.ops[i]) {
				continue
			}
			ti, ok := p.idxByID[w.Target.ID]
			if !ok || idx.eulerIn[ti] < 0 {
				continue
			}
			idx.demandEdges = append(idx.demandEdges, demandEdge{
				targetIn: idx.eulerIn[ti], target: ti, waiter: i, wait: w,
			})
		}
	}
	for _, w := range g.OrphanWaits {
		if !joinWait(w, nil) {
			continue
		}
		ti, ok := p.idxByID[w.Target.ID]
		if !ok || idx.eulerIn[ti] < 0 {
			continue
		}
		idx.demandEdges = append(idx.demandEdges, demandEdge{
			targetIn: idx.eulerIn[ti], target: ti, waiter: -1, wait: w,
		})
	}
	slices.SortStableFunc(idx.demandEdges, func(a, b demandEdge) int {
		if a.targetIn != b.targetIn {
			return int(a.targetIn - b.targetIn)
		}
		if a.waiter != b.waiter {
			return int(a.waiter - b.waiter)
		}
		return int(a.wait.StartNS - b.wait.StartNS)
	})

	return idx
}

//
// resolution: eligibility, regions, external-demand keep fixpoint
//

// cachedRegion is one candidate elision region: the strict-descendant subtree
// of a cached call op, as an Euler interval (inPos, outPos].
type cachedRegion struct {
	root     int32
	identIdx int
	inPos    int32
	outPos   int32

	keep       bool
	reason     string
	demander   int32 // -1 when structural / none
	demandWait *WaitEdge
}

// ResolveCachedHypothesis statically resolves hyp against g (design §3.3):
// classifies each ident's eligibility, forms the candidate elision regions
// (the nesting subtrees of the eligible idents' non-hit call ops), runs the
// external-demand keep fixpoint over the recorded wait edges, and materializes
// the per-op replay state. The result is order-independent by construction —
// it depends only on the recorded graph and the hypothesis, never on replay
// evaluation order — and every refusal (ineligible ident, kept region) is in
// the report data, never silent.
//
//nolint:gocyclo // one linear resolution flow: classify → fixpoint → materialize
func ResolveCachedHypothesis(g *Graph, hyp CachedHypothesis) *CachedResolution {
	idx := g.cachedIndexOnce()
	p := idx.p
	n := len(p.ops)
	res := &CachedResolution{PullCostNS: hyp.PullCostNS}

	idents := make([]string, 0, len(hyp.Idents))
	for d := range hyp.Idents {
		if d != "" {
			idents = append(idents, d)
		}
	}
	slices.Sort(idents)

	// 1. Eligibility per ident (design §3.2), checked loudly in a fixed
	// precedence order; only eligible idents contribute candidates.
	var (
		regions    []cachedRegion
		shortCands = make([][]int32, 0, len(idents)) // per ident: non-hit calls
		cachedCall = make(map[int32]bool)            // union of all shortCands
	)
	for _, d := range idents {
		el := IdentEligibility{Ident: d}
		calls := idx.callsByIdent[d]
		el.Calls = len(calls)
		openInRegion := false
		for _, ci := range calls {
			op := p.ops[ci]
			if op.Open {
				// An open call has no outcome BY CONSTRUCTION (it hasn't ended);
				// that is the open condition, not a missing-outcome one.
				el.OpenCalls++
			} else {
				switch op.Outcome {
				case wcprof.OutcomeHit.String():
					el.Hits++
				case wcprof.OutcomeExecuted.String(), wcprof.OutcomeJoined.String(), wcprof.OutcomeOK.String():
					el.Successes++
				case wcprof.OutcomeError.String(), wcprof.OutcomeCanceled.String():
					el.Failures++
				case wcprof.OutcomeDoNotCache.String():
					el.DoNotCacheCalls++
				default:
					// An ENDED call with no recorded outcome: suspect data,
					// never guessed into a hit — the ident is refused below.
					el.UnknownOutcomes++
				}
			}
			if idx.eulerIn[ci] >= 0 && idx.openSub[ci] {
				openInRegion = true
			}
		}
		switch {
		case el.Calls == 0:
			el.State = IdentNotFound
		case el.DoNotCacheCalls > 0:
			el.State = IdentDoNotCache
		case el.OpenCalls > 0 || openInRegion:
			el.State = IdentOpen
		case el.UnknownOutcomes > 0:
			el.State = IdentUnknownOutcome
		case el.Successes == 0 && el.Failures > 0:
			el.State = IdentFailedOnly
		case el.Successes == 0:
			el.State = IdentAllHit
		default:
			el.State = IdentEligible
		}

		var cands []int32
		if el.State == IdentEligible {
			identIdx := len(res.Idents)
			for _, ci := range calls {
				if p.ops[ci].Outcome == wcprof.OutcomeHit.String() {
					// A recorded hit was already a lookup: untouched (V6).
					continue
				}
				cands = append(cands, ci)
				cachedCall[ci] = true
				if idx.eulerIn[ci] >= 0 && idx.eulerOut[ci] > idx.eulerIn[ci] {
					regions = append(regions, cachedRegion{
						root:     ci,
						identIdx: identIdx,
						inPos:    idx.eulerIn[ci],
						outPos:   idx.eulerOut[ci],
						demander: -1,
					})
				}
			}
		}
		shortCands = append(shortCands, cands)
		res.Idents = append(res.Idents, el)
	}

	// Deterministic region order (nesting/document order) so fixpoint reasons
	// and reports never depend on map iteration.
	slices.SortFunc(regions, func(a, b cachedRegion) int {
		return int(a.inPos - b.inPos)
	})

	// 2. Keep fixpoint (design §3.3). Op status given the current region
	// states — elided: inside any elide-state region (it never runs);
	// short-circuited: a cached call outside every region that is not itself
	// a kept region's root (a pure hit, its recorded waits vanish); live:
	// everything else, INCLUDING ops and cached calls inside kept regions AND
	// kept regions' roots — a kept region replays exactly as recorded, which
	// is only possible if nothing inside it is altered and its root's
	// recorded timeline (which spawns and gates the region) runs unmodified.
	// Elide → keep is the only flip, so the live set only grows: the fixpoint
	// is monotone and terminates in <= len(regions) rounds.
	keptRoots := make(map[int32]bool)
	coveredBy := func(x int32, keep bool) bool {
		xin := idx.eulerIn[x]
		if xin < 0 {
			return false
		}
		for ri := range regions {
			if regions[ri].keep == keep && regions[ri].inPos < xin && xin <= regions[ri].outPos {
				return true
			}
		}
		return false
	}
	live := func(x int32) bool {
		if coveredBy(x, false) {
			return false // elided: never runs, its waits demand nothing
		}
		if cachedCall[x] && !keptRoots[x] && !coveredBy(x, true) {
			return false // a short-circuited hit: its recorded waits vanish
		}
		return true
	}
	for changed := true; changed; {
		changed = false
		for ri := range regions {
			r := &regions[ri]
			if r.keep {
				continue
			}
			// Structural demand: the region's own root is live (it sits inside
			// a kept region), so its recorded timeline — which spawns and joins
			// this region — replays as recorded.
			if live(r.root) {
				r.keep = true
				keptRoots[r.root] = true
				r.reason = "root inside a kept region (replays as recorded)"
				changed = true
				continue
			}
			// External demand: a live op's gating wait targets an op inside
			// the region.
			lo := sort.Search(len(idx.demandEdges), func(i int) bool {
				return idx.demandEdges[i].targetIn > r.inPos
			})
			for j := lo; j < len(idx.demandEdges) && idx.demandEdges[j].targetIn <= r.outPos; j++ {
				e := idx.demandEdges[j]
				if e.waiter < 0 {
					// Orphan wait: the replay cannot model it (no waiter op to
					// gate), so it does not demand a keep; it is reported
					// against the elided set below instead.
					continue
				}
				if !live(e.waiter) {
					continue
				}
				r.keep = true
				keptRoots[r.root] = true
				r.reason = "externally demanded"
				r.demander = e.waiter
				r.demandWait = e.wait
				changed = true
				break
			}
		}
	}

	// 3. Materialize. Elided = union of elide-state regions; maximal regions
	// only, so every op (and its duration) is counted exactly once (V12).
	var maxOut, maxKeptOut int32 = -1, -1
	anyElide := false
	for ri := range regions {
		r := &regions[ri]
		el := &res.Idents[r.identIdx]
		if r.keep {
			el.RegionsKept++
			res.KeptRegions = append(res.KeptRegions, KeptRegionReport{
				Root:       p.ops[r.root],
				Ident:      el.Ident,
				Ops:        int(r.outPos - r.inPos),
				SelfNS:     idx.selfSub[r.root] - p.ops[r.root].SelfNS(),
				Reason:     r.reason,
				DemandWait: r.demandWait,
			})
			if r.demander >= 0 {
				res.KeptRegions[len(res.KeptRegions)-1].Demander = p.ops[r.demander]
			}
			// Aggregate over maximal kept regions only (regions are sorted by
			// inPos; subtree intervals nest or are disjoint, so containment is
			// exactly outPos <= the running max).
			if r.outPos > maxKeptOut {
				res.KeptOps += int(r.outPos - r.inPos)
				res.KeptSelfNS += idx.selfSub[r.root] - p.ops[r.root].SelfNS()
				maxKeptOut = r.outPos
			}
			continue
		}
		el.RegionsElided++
		res.ElidedRegions++
		if !anyElide {
			res.elided = make([]bool, n)
			anyElide = true
		}
		if r.outPos <= maxOut {
			continue // nested inside an already-materialized elided region
		}
		for pos := max(r.inPos+1, maxOut+1); pos <= r.outPos; pos++ {
			op := idx.eulerOrder[pos]
			res.elided[op] = true
			res.ElidedOps++
			res.ElidedSelfNS += p.ops[op].SelfNS()
		}
		maxOut = max(maxOut, r.outPos)
	}

	// Short-circuits: cached calls outside every region become hits; calls
	// inside elided regions vanish with them (the nested case, V12); calls
	// inside kept regions replay as recorded and are tallied as kept.
	for identIdx, cands := range shortCands {
		el := &res.Idents[identIdx]
		if el.State != IdentEligible {
			continue
		}
		for _, ci := range cands {
			switch {
			case res.elided != nil && res.elided[ci]:
				el.ElidedCalls++
			case keptRoots[ci] || coveredBy(ci, true):
				// A kept region's root, or a call inside a kept region: not
				// short-circuited — the kept region's exact replay depends on
				// its recorded timeline.
				el.KeptCalls++
			default:
				if res.hitShort == nil {
					res.hitShort = make([]bool, n)
				}
				res.hitShort[ci] = true
				el.ShortCircuited++
				res.ShortCircuitedCalls++
				res.HitCallSelfNS += p.ops[ci].SelfNS()
			}
		}
		// call_exec ops carrying the ident outside all of its regions: an
		// executor call op is absent from the data, so that production cannot
		// be attributed to a region and keeps running. Loud, not silent.
		for _, ei := range idx.execsByIdent[el.Ident] {
			if res.elided != nil && res.elided[ei] {
				continue
			}
			inOwn := false
			for ri := range regions {
				if regions[ri].identIdx != identIdx {
					continue
				}
				if xin := idx.eulerIn[ei]; regions[ri].inPos < xin && xin <= regions[ri].outPos {
					inOwn = true
					break
				}
			}
			if !inOwn {
				el.UnanchoredExecs++
			}
		}
	}

	// Orphan waits targeting elided ops: unmodeled demand hints, printed.
	if res.elided != nil {
		for _, e := range idx.demandEdges {
			if e.waiter < 0 && res.elided[e.target] {
				res.OrphanWaitsIntoElided++
				res.OrphanWaitNSIntoElided += e.wait.Duration()
			}
		}
	}

	return res
}
