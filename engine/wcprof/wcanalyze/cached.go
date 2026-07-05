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
// elided: it is kept whole, replays as recorded (modulo waived production
// waits the hypothesis itself satisfies), and is reported (whole-region
// elide-or-keep, loud residuals).

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
	// PendingHits counts the Hits whose recorded outcome was hit_pending
	// (lazy-semantics state B2: the recipe was cached but production had not
	// run at hit time) — a subset of Hits, reported so B1/B2 stay visible.
	PendingHits int
}

// KeptRegionReport describes one producing region the hypothesis could not
// elide: kept whole, replayed as recorded, with the reason printed. The one
// deformation a kept region can see is a wait the hypothesis itself
// satisfies: a kept op joining an ELIDED lazy region's root (another cached
// digest's production) has that join waived, like any live forcer's.
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
	// WaivedProductionWaits counts waits into elided attributed regions that
	// are the deferred production being counterfactually removed: ancestor
	// waits (Amendment A1) plus live waits targeting an elided LAZY region's
	// root (a concurrent forcer joining production the hypothesis satisfies).
	// Waived in the replay — never a keep, never an ElidedOpDemanded — and
	// printed here. Waiters that are themselves elided need no waiver (their
	// waits never replay) and are not counted.
	WaivedProductionWaits int
	// Forced-evaluation fact provenance (doctrine §0: every degraded-data
	// path is counted and visible, never a silent default). ForcedFacts is
	// the total consumed by the keep test; ForcedFactsUnresolved is the
	// subset whose completing lazy op predated recording — an EXPLICIT
	// emit-time state (native TargetID=0; OTel zeroed link span id), not
	// capture loss (the structural gate separately refuses captures with
	// missing spans or dropped links on fact-carrying traces). Their demand
	// test degrades from target position to recorded-ident containment —
	// still a pure function of recorded data, direction conservative (can
	// only ADD keeps, never enable an elision) — and every keep it produces
	// carries its own reason string. OrphanForcedFacts counts facts whose
	// FORCER op is unknown: like orphan waits they demand nothing (no
	// liveness to test); OrphanForcedFactsIntoElided flags those whose
	// digest still names non-service_start attributed production inside an
	// elided region — an unmodeled demand hint, printed, never silent.
	ForcedFacts                 int
	ForcedFactsUnresolved       int
	OrphanForcedFacts           int
	OrphanForcedFactsIntoElided int
	// UnresolvedContainmentKeeps counts kept regions whose keep came from an
	// unrecorded-target fact's recorded-ident containment test (the degraded
	// demand path) — the answer-altering firings, surfaced per ranking row
	// (DegradedEvidence) as well as in the kept-region reasons.
	UnresolvedContainmentKeeps int

	// Program-index-aligned replay state (nil when the hypothesis is a no-op).
	// spawnWaived marks elided exec-region roots whose recorded parent's
	// spawn is production launch (skipped without tripping the gate);
	// waivedJoins holds the (waiter, target) production waits waived
	// likewise: ancestor waits plus live joins on elided lazy roots.
	elided      []bool
	hitShort    []bool
	spawnWaived []bool
	waivedJoins map[uint64]struct{}
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
	s.spawnWaived = res.spawnWaived
	s.waivedJoins = res.waivedJoins
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

	// callsByIdent indexes call ops by ident; attributedByIdent indexes every
	// NON-call op carrying an ident — the kind-agnostic deferred-production
	// attribution surface (lazy-semantics §4.1): call_exec (callKey,
	// cache.go:3791), exec.run (execIdent, executor.go:130-138), lazy ops
	// (producer recipe digest, cache.go:3045 — the general-rule emit), and
	// service_start (content-preferred digest, services.go:524 — indexed,
	// but its ident attributes runtime READINESS, never production: it can
	// neither root a region nor stand as in-region production evidence,
	// V33). Kinds whose idents are not call digests (exec phases carry
	// execution state ids) simply never match a hypothesis.
	callsByIdent      map[string][]int32
	attributedByIdent map[string][]int32

	// demandEdges holds every wait that gates as a join in the replay
	// (joinWait — the ONE predicate shared with the program compiler), sorted
	// by the target's euler-in position for region range lookups. waiter is -1
	// for an orphan wait (no owning op: unmodeled by the replay, reported but
	// never demanding).
	demandEdges []demandEdge

	// forcedEdges holds the recorded forced-evaluation facts (lazy-semantics
	// §4.4) with resolved targets, sorted by the target's euler position;
	// forcedUnresolved holds those whose completing lazy op predated
	// recording — their demand test degrades to the digest's recorded
	// attributed ops, and their keeps carry a distinct reason string.
	// forcedOrphanIdents holds the digests of facts whose FORCER op is
	// unknown: like orphan waits they can demand nothing (no liveness to
	// test), but they are counted and reported, never silently dropped.
	// Facts, never replay actions.
	forcedEdges        []forcedFact
	forcedUnresolved   []forcedFact
	forcedOrphanIdents []string
}

type demandEdge struct {
	targetIn int32 // eulerIn of the target op
	target   int32
	waiter   int32 // -1 for an orphan wait
	wait     *WaitEdge
}

type forcedFact struct {
	targetIn int32 // eulerIn of the completing lazy op; -1 when unresolved
	target   int32 // -1 when unresolved
	forcer   int32
	ident    string // the producer recipe digest
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
		p:                 p,
		eulerIn:           make([]int32, n),
		eulerOut:          make([]int32, n),
		eulerOrder:        make([]int32, n),
		openSub:           make([]bool, n),
		selfSub:           make([]int64, n),
		callsByIdent:      make(map[string][]int32),
		attributedByIdent: make(map[string][]int32),
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

	for i := int32(0); i < int32(n); i++ {
		op := p.ops[i]
		if op.Ident == "" {
			continue
		}
		if op.Kind == wcprof.OpKindCall.String() {
			idx.callsByIdent[op.Ident] = append(idx.callsByIdent[op.Ident], i)
		} else {
			idx.attributedByIdent[op.Ident] = append(idx.attributedByIdent[op.Ident], i)
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

	// Forced-evaluation facts, resolved to dense indices (event order is
	// deterministic; the resolved list re-sorts by target position). A
	// forcer that misses the dense index joins the orphan idents — counted,
	// never silently dropped.
	for _, fe := range g.OrphanForcedFacts {
		idx.forcedOrphanIdents = append(idx.forcedOrphanIdents, fe.Ident)
	}
	for _, fe := range g.ForcedEdges {
		fi, ok := p.idxByID[fe.Forcer.ID]
		if !ok {
			idx.forcedOrphanIdents = append(idx.forcedOrphanIdents, fe.Ident)
			continue
		}
		f := forcedFact{targetIn: -1, target: -1, forcer: fi, ident: fe.Ident}
		if fe.Target != nil {
			if ti, tok := p.idxByID[fe.Target.ID]; tok && idx.eulerIn[ti] >= 0 {
				f.target, f.targetIn = ti, idx.eulerIn[ti]
			}
		}
		if f.target >= 0 {
			idx.forcedEdges = append(idx.forcedEdges, f)
		} else {
			idx.forcedUnresolved = append(idx.forcedUnresolved, f)
		}
	}
	slices.SortStableFunc(idx.forcedEdges, func(a, b forcedFact) int {
		if a.targetIn != b.targetIn {
			return int(a.targetIn - b.targetIn)
		}
		return int(a.forcer - b.forcer)
	})

	return idx
}

//
// resolution: eligibility, regions, external-demand keep fixpoint
//

// cachedRegion is one candidate elision region as a closed Euler interval
// [lo, outPos]. Two shapes exist:
//
//   - a CALL region: the strict-descendant subtree of a cached call op
//     (lo = eulerIn[root]+1 — the root survives as the hit);
//   - an EXEC region (Amendment A1): the subtree INCLUDING the root of an
//     exec-kind op whose ident IS the cached digest — the engine's explicit
//     attribution of lazily-deferred production (executor.go execIdent =
//     execMD.CallDigest; the OTel exec.run span's dag.digest is the same
//     value). Under the local-warm-hit model a cached result's deferred
//     production does not run, so the whole attributed subtree elides.
type cachedRegion struct {
	root       int32
	identIdx   int
	lo         int32
	outPos     int32
	attributed bool // a deferred-production-attributed region (root-inclusive)

	keep       bool
	reason     string
	demander   int32 // -1 when structural / none
	demandWait *WaitEdge
}

// ops returns the region's op count (root included for exec regions).
func (r *cachedRegion) ops() int { return int(r.outPos - r.lo + 1) }

// isStrictAncestor reports whether a is a strict ancestor of b in the
// nesting forest (Euler interval containment).
func (idx *cachedIndex) isStrictAncestor(a, b int32) bool {
	ain, bin := idx.eulerIn[a], idx.eulerIn[b]
	return ain >= 0 && bin >= 0 && ain < bin && bin <= idx.eulerOut[a]
}

// nonRegionAttribution reports whether an attributed op must NOT root an
// elision region, for one of two reasoned causes:
//
//   - REDUNDANT: a call_exec nested directly under its own same-ident
//     executor call — the anchored shape whose subtree IS the call region
//     already; rooting a second (nested, identical) region would only
//     duplicate reports and fixpoint work. A call_exec WITHOUT that parent
//     (the executor call op missing from the data) roots its own region,
//     which is what covers the formerly-"unanchored" production.
//   - NOT PRODUCTION: a service_start op. Service startup is per-session
//     runtime READINESS, not result production — ServiceKey is
//     session-scoped (core/services.go:473-477), so a real warm run
//     re-starts the service even when every result is cached. Eliding it
//     under a cached hypothesis would remove work warm reality re-pays,
//     systematically overstating savings. Its ident (the content-preferred
//     digest, services.go:524) stays in the index for reporting, never for
//     regions.
//
// Other attributed kinds (lazy, exec) always root regions: a lazy op can
// legitimately hang under a same-ident HIT call (a pending hit forcing its
// own production), which is not a region root.
func nonRegionAttribution(op *Op) bool {
	if op.Kind == wcprof.OpKindServiceStart.String() {
		return true
	}
	return op.Kind == wcprof.OpKindCallExec.String() &&
		op.Parent != nil &&
		op.Parent.Kind == wcprof.OpKindCall.String() &&
		op.Parent.Ident == op.Ident
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
	res.ForcedFacts = len(idx.forcedEdges) + len(idx.forcedUnresolved)
	res.ForcedFactsUnresolved = len(idx.forcedUnresolved)
	res.OrphanForcedFacts = len(idx.forcedOrphanIdents)

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
		regions     []cachedRegion
		shortCands  = make([][]int32, 0, len(idents)) // per ident: non-hit calls
		cachedCall  = make(map[int32]bool)            // union of all shortCands
		eligibleSet = make(map[string]struct{})       // ELIGIBLE hypothesized digests
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
				case wcprof.OutcomeHitPending.String():
					// lazy-semantics B2: cached recipe, production not yet
					// run at hit time — a hit for eligibility, tallied so
					// the B1/B2 split stays visible
					el.Hits++
					el.PendingHits++
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
		// Attributed production regions are production too — open ops inside
		// them equally mean the production is not fully recorded. (The
		// anchored call_exec shape needs no separate check: it sits inside
		// the call subtree checked above.)
		for _, ei := range idx.attributedByIdent[d] {
			if idx.eulerIn[ei] >= 0 && !nonRegionAttribution(p.ops[ei]) && idx.openSub[ei] {
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
			eligibleSet[d] = struct{}{}
			identIdx := len(res.Idents)
			for _, ci := range calls {
				if o := p.ops[ci].Outcome; o == wcprof.OutcomeHit.String() || o == wcprof.OutcomeHitPending.String() {
					// A recorded hit was already a lookup: untouched (V6).
					// A pending hit's forced production, if recorded, is a
					// lazy op carrying this ident — its own attributed
					// region, handled below.
					continue
				}
				cands = append(cands, ci)
				cachedCall[ci] = true
				if idx.eulerIn[ci] >= 0 && idx.eulerOut[ci] > idx.eulerIn[ci] {
					regions = append(regions, cachedRegion{
						root:     ci,
						identIdx: identIdx,
						lo:       idx.eulerIn[ci] + 1,
						outPos:   idx.eulerOut[ci],
						demander: -1,
					})
				}
			}
			// Deferred-production-attributed regions (root-inclusive; the
			// general rule, lazy-semantics §4.1): every non-call op carrying
			// this ident, except the anchored call_exec shape whose subtree
			// is already the call region above.
			for _, ei := range idx.attributedByIdent[d] {
				if idx.eulerIn[ei] < 0 || nonRegionAttribution(p.ops[ei]) {
					continue
				}
				regions = append(regions, cachedRegion{
					root:       ei,
					identIdx:   identIdx,
					lo:         idx.eulerIn[ei],
					outPos:     idx.eulerOut[ei],
					attributed: true,
					demander:   -1,
				})
			}
		}
		shortCands = append(shortCands, cands)
		res.Idents = append(res.Idents, el)
	}

	// Deterministic region order (nesting/document order; wider first on the
	// same start, so the union scans stay maximal-first) — fixpoint reasons
	// and reports never depend on map iteration.
	slices.SortFunc(regions, func(a, b cachedRegion) int {
		if a.lo != b.lo {
			return int(a.lo - b.lo)
		}
		return int(b.outPos - a.outPos)
	})

	// 2. Keep fixpoint (design §3.3). Op status given the current region
	// states — elided: inside any elide-state region (it never runs);
	// short-circuited: a cached call outside every region that is not itself
	// a kept region's root (a pure hit, its recorded waits vanish); live:
	// everything else, INCLUDING ops and cached calls inside kept regions AND
	// kept regions' roots — a kept region replays as recorded (its one
	// deformation: production waits waived when the hypothesis satisfies
	// them), which requires that nothing inside it is removed and its root's
	// recorded timeline (which spawns and gates the region) runs unmodified.
	// Elide → keep is the only flip, so the live set only grows: the fixpoint
	// is monotone and terminates in <= len(regions) rounds.
	keptRoots := make(map[int32]bool)
	// coveredByExcl is coveredBy with one region excluded from consideration —
	// the structural-demand test must judge a region's ROOT while ignoring the
	// candidate's own interval (an A1 exec region covers its root, which would
	// otherwise mask the root's position inside a surrounding kept region and
	// let an elided exec region pierce that kept region's exact replay).
	coveredByExcl := func(x int32, keep bool, exclude int) bool {
		xin := idx.eulerIn[x]
		if xin < 0 {
			return false
		}
		for ri := range regions {
			if ri == exclude {
				continue
			}
			if regions[ri].keep == keep && regions[ri].lo <= xin && xin <= regions[ri].outPos {
				return true
			}
		}
		return false
	}
	coveredBy := func(x int32, keep bool) bool {
		return coveredByExcl(x, keep, -1)
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
			// Structural demand: the region's root sits inside a SURROUNDING
			// kept region and is not elided by any OTHER region — the kept
			// region's exact replay spawns and gates this one, so it must be
			// kept too. Judged excluding the candidate's own interval: a
			// root-inclusive exec region covers its root, which would
			// otherwise mask this exact case (the Chunk-4 review blocker).
			if !coveredByExcl(r.root, false, ri) && coveredByExcl(r.root, true, ri) {
				r.keep = true
				keptRoots[r.root] = true
				r.reason = "root inside a kept region (replays as recorded)"
				changed = true
				continue
			}
			// External demand: a live op's gating wait targets an op inside
			// the region.
			lo := sort.Search(len(idx.demandEdges), func(i int) bool {
				return idx.demandEdges[i].targetIn >= r.lo
			})
			for j := lo; j < len(idx.demandEdges) && idx.demandEdges[j].targetIn <= r.outPos; j++ {
				e := idx.demandEdges[j]
				if e.waiter < 0 {
					// Orphan wait: the replay cannot model it (no waiter op to
					// gate), so it does not demand a keep; it is reported
					// against the elided set below instead.
					continue
				}
				if r.attributed && idx.isStrictAncestor(e.waiter, r.root) {
					// A1: a wait from the region root's own ancestor chain (the
					// lazy wrapper / consumer that spawned the deferred
					// production) IS the production wait being counterfactually
					// removed — it does not keep the region. It is waived in
					// the replay at materialize time, counted, never silent.
					continue
				}
				if r.attributed && e.target == r.root && p.ops[r.root].Kind == wcprof.OpKindLazy.String() {
					// A wait ON a lazy region's root — a concurrent forcer
					// joining this digest's in-flight production — is equally
					// production demand: under B1 that joiner hits the
					// materialized payload, ancestor or not. (Waits on an
					// EXEC root stay demand: a third party waiting on the
					// execution wants its side effects, V25b.) Waived at
					// materialize time, counted.
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
			if r.keep {
				continue
			}
			// Forced-evaluation demand (lazy-semantics §4.4): a LIVE op's
			// recorded post-completion force of production inside the region
			// keeps it. Two exclusions, both reasoned: a fact whose digest is
			// itself an ELIGIBLE hypothesized digest demands nothing (under
			// B1 its forcer would hit the materialized payload — Erik's
			// same-digest rule, generalized to the whole eligible set); and a
			// non-live forcer's demand vanishes with the forcer. There is NO
			// ancestor exclusion, deliberately — the A1 waiver symmetry does
			// not apply to facts: a fact is emitted only on the Evaluate fast
			// path (post-completion consumption), the production launch takes
			// the slow path and never emits one, and a target inside this
			// region cannot have been launched by an ancestor of the region
			// root (its lazy op would be parented under that ancestor,
			// OUTSIDE the region). So every ancestor-forcer fact is a real
			// consumer of a non-hypothesized nested production, and skipping
			// it would silently remove work a survivor demands. Facts never
			// gate the replay, so no replay-side waiver bookkeeping exists
			// for them.
			keepForced := func(f forcedFact, reason string) bool {
				if _, hyp := eligibleSet[f.ident]; hyp {
					return false
				}
				if !live(f.forcer) {
					return false
				}
				r.keep = true
				keptRoots[r.root] = true
				r.reason = reason
				r.demander = f.forcer
				changed = true
				return true
			}
			flo := sort.Search(len(idx.forcedEdges), func(i int) bool {
				return idx.forcedEdges[i].targetIn >= r.lo
			})
			for j := flo; j < len(idx.forcedEdges) && idx.forcedEdges[j].targetIn <= r.outPos; j++ {
				if keepForced(idx.forcedEdges[j], "externally forced (post-completion demand)") {
					break
				}
			}
			if r.keep {
				continue
			}
			// Unresolved-target facts (production predated recording): the
			// digest still names the dependency — demand iff any attributed
			// op of that digest sits inside the region. A service_start
			// match is NOT production evidence (its ident attributes
			// readiness, V33) and is skipped; an anchored call_exec IS the
			// digest's executing subtree and stays — dropping it would trade
			// this conservative keep for a silent over-elision.
			for _, f := range idx.forcedUnresolved {
				inRegion := false
				for _, ai := range idx.attributedByIdent[f.ident] {
					if p.ops[ai].Kind == wcprof.OpKindServiceStart.String() {
						continue
					}
					if xin := idx.eulerIn[ai]; xin >= r.lo && xin <= r.outPos {
						inRegion = true
						break
					}
				}
				if inRegion && keepForced(f, "externally forced (unrecorded target; demand matched by recorded ident containment)") {
					res.UnresolvedContainmentKeeps++
					break
				}
			}
		}
	}

	// 3. Materialize. Elided = union of elide-state regions; maximal regions
	// only, so every op (and its duration) is counted exactly once (V12).
	regionSelf := func(r *cachedRegion) int64 {
		if r.attributed {
			return idx.selfSub[r.root] // root-inclusive
		}
		return idx.selfSub[r.root] - p.ops[r.root].SelfNS()
	}
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
				Ops:        r.ops(),
				SelfNS:     regionSelf(r),
				Reason:     r.reason,
				DemandWait: r.demandWait,
			})
			if r.demander >= 0 {
				res.KeptRegions[len(res.KeptRegions)-1].Demander = p.ops[r.demander]
			}
			// Aggregate over maximal kept regions only (regions are sorted by
			// lo; subtree intervals nest or are disjoint, so containment is
			// exactly outPos <= the running max).
			if r.outPos > maxKeptOut {
				res.KeptOps += r.ops()
				res.KeptSelfNS += regionSelf(r)
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
		if r.attributed {
			// Production waivers for an ELIDED attributed region: its
			// recorded parent's spawn of the root, its ancestors' waits into
			// it, and live joins on a LAZY root (concurrent forcers the
			// hypothesis satisfies) are the deferred production being
			// counterfactually removed. The replay skips exactly these
			// without tripping ElidedOpDemanded; the count is printed, never
			// silent.
			if res.spawnWaived == nil {
				res.spawnWaived = make([]bool, n)
			}
			res.spawnWaived[r.root] = true
			lo := sort.Search(len(idx.demandEdges), func(i int) bool {
				return idx.demandEdges[i].targetIn >= r.lo
			})
			rootIsLazy := p.ops[r.root].Kind == wcprof.OpKindLazy.String()
			for j := lo; j < len(idx.demandEdges) && idx.demandEdges[j].targetIn <= r.outPos; j++ {
				e := idx.demandEdges[j]
				if e.waiter < 0 {
					continue
				}
				// A waiter that is itself elided never replays its wait: no
				// waiver needed, and counting it would inflate the printed
				// residual with waits that cannot occur.
				if coveredBy(e.waiter, false) {
					continue
				}
				// The two waived production-demand shapes, mirroring the
				// fixpoint's skips: ancestor waits (the launch chain) and —
				// for lazy roots — concurrent forcers joining the production
				// itself (B1 satisfies them, ancestor or not).
				if !idx.isStrictAncestor(e.waiter, r.root) && !(rootIsLazy && e.target == r.root) {
					continue
				}
				if res.waivedJoins == nil {
					res.waivedJoins = make(map[uint64]struct{})
				}
				key := uint64(uint32(e.waiter))<<32 | uint64(uint32(e.target))
				if _, dup := res.waivedJoins[key]; !dup {
					res.waivedJoins[key] = struct{}{}
					res.WaivedProductionWaits++
				}
			}
		}
		if r.outPos <= maxOut {
			continue // nested inside an already-materialized elided region
		}
		for pos := max(r.lo, maxOut+1); pos <= r.outPos; pos++ {
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
		// NOTE: the pre-general-rule UnanchoredExecs residual ("attributed
		// production outside every region of its ident") is gone BY
		// CONSTRUCTION: every non-call op carrying the ident now roots its
		// own attributed region, so unattributable production of an eligible
		// ident no longer exists.
	}

	// Orphan waits targeting elided ops: unmodeled demand hints, printed.
	if res.elided != nil {
		for _, e := range idx.demandEdges {
			if e.waiter < 0 && res.elided[e.target] {
				res.OrphanWaitsIntoElided++
				res.OrphanWaitNSIntoElided += e.wait.Duration()
			}
		}
		// Orphan forced facts whose digest names attributed production that
		// was elided: the same unmodeled-demand hint (service_start matches
		// are readiness, not production — excluded as everywhere, V33).
		for _, ident := range idx.forcedOrphanIdents {
			for _, ai := range idx.attributedByIdent[ident] {
				if p.ops[ai].Kind == wcprof.OpKindServiceStart.String() {
					continue
				}
				if res.elided[ai] {
					res.OrphanForcedFactsIntoElided++
					break
				}
			}
		}
	}

	return res
}
