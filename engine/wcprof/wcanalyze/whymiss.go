package wcanalyze

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/dagger/dagger/engine/wcprof"
)

// Cache-invalidation tracing (design:
// hack/designs/cache-invalidation-tracing-design.md): answer "why didn't I
// get a cache hit?" for a recorded uncached op by walking its recorded
// cache-input digests down to the MISS FRONTIER — the deepest uncached
// digests whose own inputs are all hits or leaves — and answering each
// frontier origin with a root-cause category in the engine's own semantics.
//
// Binding framing (design §1): deliberate cache-key scoping and
// not-retained-from-a-previous-run are FIRST-CLASS CORRECT answers, never
// framed as instability or defects. Every category assignment is a pure
// function of recorded data with the deciding datum printed; where the data
// cannot decide, the answer is the stated undetermined form, never a guess
// (design §8).
//
// This file is Chunk 1: the walk over one capture, the single-capture
// categories (1 deliberately-scoped via recorded scope implicit inputs,
// 7 engine-refuses, 8 prior-attempt-failed, and the undetermined form), and
// the priced ranked frontier via the existing what-if-cached simulator.
// Pair mode (categories 2/3/4) is Chunk 2; the E1 terminal facts
// (categories 5/6/9) are Chunk 3.

// MissStatus is a digest node's TIME-AWARE cache status within one capture:
// its first recorded call's outcome in demand order — StartNS, ties broken
// by op id — deterministic, pure recorded data (design §3.1). The walk
// operates on digest nodes, not op instances.
type MissStatus uint8

const (
	// MissStatusMissed: the first recorded demand executed, joined an
	// in-flight execution, or failed — the recipe was not served from cache.
	MissStatusMissed MissStatus = iota
	// MissStatusCached: the first recorded demand was a hit (or hit_pending —
	// the recipe was cached; pending production is a nuance, not a miss).
	MissStatusCached
	// MissStatusRefused: the first recorded demand was do_not_cache — the
	// engine never looks this call up, by design (category 7).
	MissStatusRefused
	// MissStatusUnrecorded: no call op with this digest exists in the
	// capture. The status is unknown — a labeled leaf, never guessed.
	MissStatusUnrecorded
	// MissStatusUndecidable: the first recorded demand is open at capture
	// time or carries no recorded outcome — the status does not exist in the
	// data yet, so the walk refuses to assign one (labeled, not guessed).
	MissStatusUndecidable
)

func (s MissStatus) String() string {
	switch s {
	case MissStatusMissed:
		return "missed at first demand"
	case MissStatusCached:
		return "cached at first demand"
	case MissStatusRefused:
		return "do-not-cache (the engine never looks this call up)"
	case MissStatusUnrecorded:
		return "no recorded call in this capture"
	case MissStatusUndecidable:
		return "first demand open or outcome-less at capture (status undecidable)"
	default:
		return "invalid"
	}
}

// WhyMissCategory is a frontier origin's root-cause category (design §3.2).
// Chunk 1 decides the single-capture subset; the pair-mode categories
// (2 not-retained, 3 input-changed, 4 new-work) land with Chunk 2 and the
// terminal-fact categories (5 expired, 6 session-filtered, 9 hit-unusable)
// with Chunk 3's E1 emit.
type WhyMissCategory uint8

const (
	// CategoryUndetermined is the stated fallback, never a guess: with only
	// a single capture and no terminal fact, categories 2/3/4/5/6 collapse
	// to "no cached result existed under this key; cause not recorded in
	// this capture".
	CategoryUndetermined WhyMissCategory = iota
	// CategoryDeliberatelyScoped (1): scope implicit inputs are hashed into
	// the recipe digest by design — a first-class correct answer.
	CategoryDeliberatelyScoped
	// CategoryEngineRefuses (7): a do_not_cache call — never cached, never
	// looked up; an expected miss.
	CategoryEngineRefuses
	// CategoryPriorAttemptFailed (8): an earlier execution — in this capture
	// or in the paired reference capture — errored; failed executions
	// publish no result, so later demands re-executed.
	CategoryPriorAttemptFailed
	// CategoryNotRetained (2, pair mode): the digest was computed or served
	// in the reference capture; no cached result under it in this one. The
	// engine's designed lifetime mechanisms decide retention — which one
	// applied is deliberately NOT claimed (design §3.2 row 2: that would be
	// inference). A first-class expected-miss answer.
	CategoryNotRetained
	// CategoryInputChanged (3, pair mode): the deepest positionally-paired
	// node whose recipe digest differs from its counterpart while its own
	// inputs are digest-stable or pairwise attributed — the change is in the
	// call itself; parent edges carry the "input #k changed" attribution.
	CategoryInputChanged
	// CategoryNewWork (4, pair mode): the digest is absent from the
	// available history (the paired reference capture, named as such).
	CategoryNewWork
)

func (c WhyMissCategory) String() string {
	switch c {
	case CategoryUndetermined:
		return "undetermined"
	case CategoryDeliberatelyScoped:
		return "deliberately scoped (category 1)"
	case CategoryEngineRefuses:
		return "engine refuses to cache (category 7)"
	case CategoryPriorAttemptFailed:
		return "prior attempt failed (category 8)"
	case CategoryNotRetained:
		return "not retained from a previous run (category 2)"
	case CategoryInputChanged:
		return "input changed (category 3)"
	case CategoryNewWork:
		return "new work (category 4)"
	default:
		return "invalid"
	}
}

// WhyMissNode is one digest node in the walk.
type WhyMissNode struct {
	Digest string
	Class  string
	Status MissStatus

	// Calls are the digest's recorded call ops in demand order (StartNS,
	// ties by op id). FirstCall is Calls[0] (nil for MissStatusUnrecorded).
	Calls     []*Op
	FirstCall *Op

	// ContextDependent: cached at first demand, but a LATER call missed —
	// the recipe stopped being served within the run. Reported as such and
	// walked as a miss (design §3.1); the mechanism (in-run
	// release/collection, expiry, session filtering) is not recorded in this
	// capture — E1 upgrades some of these to exact causes in Chunk 3.
	ContextDependent bool
	// ReExecutedAfterSuccess: a successful non-hit execution was followed by
	// another non-hit demand that started after it ended — the published
	// result stopped being served within the run. Same mechanism-unrecorded
	// treatment as ContextDependent.
	ReExecutedAfterSuccess bool
	// FailedBeforeReExecution: a failed call ended before a later non-hit
	// call started — the category-8 evidence shape (failures are not
	// cached). The deciding ops are FailedCall / ReDemandCall.
	FailedBeforeReExecution bool
	FailedCall              *Op
	ReDemandCall            *Op

	// Nuances (never origins, design §3.2): hit_pending (recipe cached,
	// first materialization owed) and joined (in-flight dedupe).
	HitPendingCalls int
	JoinedCalls     int

	// InputsFrom is the call op whose recorded CacheInputs the walk
	// descended (the first call in demand order carrying them); nil when no
	// call recorded inputs — the walk then cannot descend and says so.
	InputsFrom *Op
	// Inputs are the distinct walked input digests, in recorded order.
	Inputs []*WhyMissNode

	// Pair-mode facts (design §5). StableInA: the digest is present in the
	// reference capture — its A-side outcomes answer directly (category 2/8
	// family) and the walk does not descend (every digest below a stable
	// digest is stable by Merkle construction; descending would only repeat
	// the same answer). PairedWith: the A-side digest this node pairs with
	// positionally (a changed pair under a paired parent, or the root
	// partner). PairConflict: distinct A-side occurrences claimed this B
	// digest — the partner is ambiguous, so the pairing is VOIDED and stated
	// (never first-wins-classified).
	StableInA    bool
	PairedWith   string
	PairConflict bool
	aSide        *calibSide // A-side recorded facts when StableInA

	// Pairing deltas for this node's OWN input vectors (filled when the §5
	// pairing ran on them): the concrete divergence a category-3 answer must
	// name — falsely claiming "the call itself changed" while inputs were
	// removed/added would misattribute the invalidation.
	pairCompared    bool
	pairUnavailable string // why the vectors could not be compared ("" when they were)
	pairRemoved     []string
	pairAdded       []string
	pairRefused     int // gaps refused as not pairwise attributable
	pairChanged     int // changed-pair edges emitted

	// walk bookkeeping
	walkParent *WhyMissNode // discovery parent (deterministic BFS), for path rendering
	walked     bool
}

// WalkedAsMiss reports whether the walk treats this node as uncached:
// a plain miss, a context-dependent reversal, or a do-not-cache refusal.
func (n *WhyMissNode) WalkedAsMiss() bool {
	switch n.Status {
	case MissStatusMissed, MissStatusRefused:
		return true
	case MissStatusCached:
		return n.ContextDependent
	default:
		return false
	}
}

// WhyMissOrigin is one ranked frontier origin: a deepest uncached digest with
// its category, answer text (deciding datum included), and priced impact.
type WhyMissOrigin struct {
	Node     *WhyMissNode
	Category WhyMissCategory
	// Answer is the category's answer text with the deciding datum printed
	// (design §8: every assignment shows what decided it).
	Answer string
	// ScopeNote annotates the origin's recorded scope evidence regardless of
	// category (active scope inputs, recorded-empty ones, or the recording
	// gap) — category-1 evidence rides along even when a stronger category
	// decided (design §3.2 undetermined form).
	ScopeNote string
	// Notes carry the node's within-run annotations (context-dependent,
	// re-executed, nuance tallies, inputs-not-recorded shallowness).
	Notes []string

	// Priced impact (design §3.3): the origin's digest hypothesized cached,
	// re-simulated by the what-if-cached engine at pull cost 0. Priced is
	// false when the simulator refuses (ineligible ident — e.g. simulating a
	// do_not_cache call cached is fiction, V14) or the gate failed;
	// PriceRefusal then says why and no number renders (GateErr doctrine).
	SavedNS      int64
	Priced       bool
	PriceRefusal string
	PriceGateBad bool
	// PathMisses counts the walked miss nodes (target included) from which
	// this origin is reachable through missed-input edges: the misses on the
	// walk this origin explains.
	PathMisses int
}

// WhyMissReport is the walk result for one target digest.
type WhyMissReport struct {
	Target *WhyMissNode
	// Origins is the ranked frontier (priced impact descending; unpriced
	// last). There can be several independent origins; the output is never a
	// single guessed one.
	Origins []*WhyMissOrigin
	// Collaterals counts walked miss nodes between the target and the
	// frontier: their digests changed because an input's digest changed
	// (Merkle collateral) — shown as the path, not the cause.
	Collaterals int
	// HitBoundaries / UnrecordedLeaves / UndecidableLeaves count the walk's
	// non-miss leaves; the report names them so absence statements stay
	// scoped to the history actually searched (this capture).
	// PendingHitBoundaries is the subset of HitBoundaries whose first demand
	// was hit_pending — the recipe was cached with first materialization
	// owed: a nuance rendered on the boundary line, never a miss (W6).
	HitBoundaries        int
	PendingHitBoundaries int
	UnrecordedLeaves     int
	UndecidableLeaves    int
	NodesWalked          int
	// Caveats are the source-fidelity caveats (design §3.1): OTel captures
	// carry first-emission-only per-digest evidence and record no module-ref
	// edges, so the walk may be shallow for module-provided calls pre-E3.
	Caveats []string

	// PairMode marks a report classified against a reference capture
	// (design §5); PairLines carry the pairwise evidence lines — removed
	// inputs, changed-input attributions, and the §5 refusal lines
	// ("structural change, not pairwise attributable") — in deterministic
	// discovery order.
	PairMode  bool
	PairLines []string

	BaselineNS int64

	// priceGateErr aggregates per-origin ElidedOpDemanded gate failures —
	// unfaithful data, surfaced as a command failure like the detail mode's
	// GateErr.
	priceGateErr error
}

// GateErr returns a non-nil error iff any origin's pricing simulation
// violated the unreachability assertion — the recorded structure contradicts
// the static resolution (unfaithful data), so the affected prices are
// invalid and the command must fail loudly.
func (r *WhyMissReport) GateErr() error {
	return r.priceGateErr
}

// RunWhyUncached walks one target digest to its miss frontier over a single
// capture. It refuses captures the what-if-cached admission gate refuses
// (dropped events, suppressed idents — design §8: the walk depends on
// recorded calls and demand evidence, any of which a dropped event could
// have been), per row W9.
func RunWhyUncached(g *Graph, target string) (*WhyMissReport, error) {
	return runWhyUncached(g, nil, target)
}

func runWhyUncached(g *Graph, pair *whyPairState, target string) (*WhyMissReport, error) {
	if err := cachedRefusalErr(g); err != nil {
		return nil, fmt.Errorf("why-uncached %s", err)
	}
	if pair != nil {
		// Absence claims (category 4, and the stable/absent split itself)
		// are only as good as the reference capture's completeness: a
		// dropped event there could have been the very call whose absence
		// the answer asserts.
		if err := cachedRefusalErr(pair.gA); err != nil {
			return nil, fmt.Errorf("why-uncached (reference capture) %s", err)
		}
	}
	w := newWhyMissWalk(g)
	w.pair = pair
	tn := w.node(target)
	if tn.Status == MissStatusUnrecorded {
		return nil, fmt.Errorf("why-uncached: digest %s has no recorded call in this capture", target)
	}

	rep := &WhyMissReport{Target: tn, PairMode: pair != nil}
	if g.ResultIDsCaptureLocal {
		// The OTel source caveats (design §3.1, round-3 findings). Native
		// captures are demand-complete and carry the ordered, module-ref-
		// inclusive input vector; the OTel source is neither.
		rep.Caveats = append(rep.Caveats,
			"OTel capture: repeated same-digest call spans are deliberately suppressed at emit (seen-key suppression, dagql/telemetry.go), so per-digest evidence is first-emission-only; first-demand status is computed from what is recorded",
			"OTel capture: module-ref edges are not recorded in dag.inputs — a module-caused miss cannot be walked to its true frontier; the frontier may be shallow for module-provided calls (E3 closes this; per-node refusal lands with Chunk 4)",
		)
	}
	if pair != nil && pair.refusePositional {
		rep.Caveats = append(rep.Caveats,
			"OTel capture pair: positional pairing REFUSED — dag.inputs is a deduplicated, module-less digest list, unsound for the §5 ordered pairing contract (E3a unlocks it); digest-stable analysis only (categories 2/8 by digest identity; changed nodes stay single-capture-classified)")
	}

	if !tn.WalkedAsMiss() {
		// Nothing to trace: the target was served from cache at first demand
		// (or its status is undecidable, which the report states as-is).
		return rep, nil
	}

	// Root pairing: an absent-from-reference target needs an A-side partner
	// for its inputs to pair positionally. The partner must be unambiguous —
	// the single reference digest of the target's class that is itself
	// absent from this capture — else pairing is refused with the reason
	// stated (never guessed).
	if pair != nil && !pair.refusePositional && !pair.stable(tn.Digest) {
		if pa, why := pair.rootPartner(w, tn); pa != "" {
			tn.PairedWith = pa
		} else if why != "" {
			rep.PairLines = append(rep.PairLines, why)
		}
	}

	// Deterministic BFS over recorded cache-input digests: input order is the
	// recorded order, node identity is the digest (visited once).
	queue := []*WhyMissNode{tn}
	tn.walked = true
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		rep.NodesWalked++
		if n.Status == MissStatusRefused {
			// A do-not-cache node terminates its path: the refusal is an
			// unconditional, first-class complete answer for this node's
			// miss — nothing below it can change that (category 7).
			continue
		}
		if pair != nil && pair.stable(n.Digest) {
			// Digest-stable in the reference: the A-side outcome answers
			// directly (design §5 bullet 1 — category 2/8 family). No
			// descent: every digest below a stable digest is stable by
			// Merkle construction, so descending repeats the same answer.
			n.StableInA = true
			n.aSide = pair.side(n.Digest)
			continue
		}
		var edges []whyMissEdge
		if pair != nil && !pair.refusePositional && n.PairedWith != "" {
			edges = w.pairedInputEdges(rep, n)
		} else {
			for _, dig := range w.inputsOf(n) {
				edges = append(edges, whyMissEdge{b: dig})
			}
		}
		seenEdge := map[string]bool{}
		for _, e := range edges {
			in, seen := w.nodes[e.b]
			if !seen {
				in = w.node(e.b)
			}
			// Pairing reconciliation runs on EVERY occurrence (dedup below is
			// only for walk edges): two distinct A occurrences claiming one B
			// digest make the partner ambiguous — the pairing is voided and
			// stated, never first-wins-classified (review round 1, finding 3).
			if e.pairA != "" && !in.PairConflict {
				switch {
				case in.PairedWith == "":
					in.PairedWith = e.pairA
				case in.PairedWith != e.pairA:
					rep.PairLines = append(rep.PairLines, fmt.Sprintf(
						"digest %s claimed by two distinct reference pairings (%s and %s) — the partner is ambiguous, so the pairing is VOIDED for this node (classified by digest identity only)",
						in.Digest, in.PairedWith, e.pairA))
					in.PairedWith = ""
					in.PairConflict = true
				}
			}
			if seenEdge[e.b] {
				continue
			}
			seenEdge[e.b] = true
			n.Inputs = append(n.Inputs, in)
			if in.WalkedAsMiss() && !in.walked {
				in.walked = true
				in.walkParent = n
				queue = append(queue, in)
			}
		}
	}

	// Frontier extraction: origins are walked miss nodes no input of which
	// EXPLAINS their miss; everything walked between the target and the
	// frontier is Merkle collateral. In single-capture mode any walked-miss
	// input explains its parent (the Merkle notion: the parent's key embeds
	// the input subgraph). In pair mode a DIGEST-STABLE missed input does
	// NOT explain a changed parent — a stable digest is an unchanged input
	// ref, so it cannot have changed the parent's key; its own miss
	// (not-retained, category 2) is an independent origin, and the changed
	// parent's own divergence must still be reported (review round 1,
	// finding 2). Leaves are tallied by kind.
	var origins []*WhyMissNode
	counted := map[string]bool{}
	for _, n := range w.order {
		if !n.walked {
			continue
		}
		missedInputs := 0
		for _, in := range n.Inputs {
			if in.WalkedAsMiss() {
				if pair == nil || !in.StableInA {
					missedInputs++
				}
				continue
			}
			if counted[in.Digest] {
				continue
			}
			counted[in.Digest] = true
			switch in.Status {
			case MissStatusCached:
				rep.HitBoundaries++
				if in.FirstCall != nil && in.FirstCall.Outcome == wcprof.OutcomeHitPending.String() {
					rep.PendingHitBoundaries++
				}
			case MissStatusUnrecorded:
				rep.UnrecordedLeaves++
			case MissStatusUndecidable:
				rep.UndecidableLeaves++
			}
		}
		if n.Status == MissStatusRefused || missedInputs == 0 {
			origins = append(origins, n)
		} else {
			rep.Collaterals++
		}
	}

	// Classify, price, and rank the frontier.
	baseSim := NewSimulation(g, nil)
	baseline, err := baseSim.Run()
	if err != nil {
		return nil, fmt.Errorf("why-uncached: baseline simulation: %w", err)
	}
	rep.BaselineNS = baseline
	var gateClauses []string
	for _, n := range origins {
		o := w.classifyOrigin(n)
		o.PathMisses = w.countPathMisses(n)
		priceOrigin(g, baseline, o)
		if o.PriceGateBad {
			gateClauses = append(gateClauses, n.Digest)
		}
		rep.Origins = append(rep.Origins, o)
	}
	if len(gateClauses) > 0 {
		rep.priceGateErr = fmt.Errorf("why-uncached price gate FAILED: elided op demanded while pricing origin(s) %s — the recorded structure contradicts the static resolution (unfaithful data); the affected priced impacts are not valid", strings.Join(gateClauses, ", "))
	}
	slices.SortStableFunc(rep.Origins, func(a, b *WhyMissOrigin) int {
		if a.Priced != b.Priced {
			if a.Priced {
				return -1
			}
			return 1
		}
		if a.SavedNS != b.SavedNS {
			if b.SavedNS > a.SavedNS {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Node.Digest, b.Node.Digest)
	})
	return rep, nil
}

//
// walk state
//

type whyMissWalk struct {
	g     *Graph
	idx   *cachedIndex
	nodes map[string]*WhyMissNode
	order []*WhyMissNode // creation order — deterministic iteration
	pair  *whyPairState  // nil in single-capture mode
}

// whyMissEdge is one walk edge: a B-side input digest, plus the A-side
// digest it pairs with positionally when the §5 contract attributed one.
type whyMissEdge struct {
	b     string
	pairA string
}

func newWhyMissWalk(g *Graph) *whyMissWalk {
	return &whyMissWalk{g: g, idx: g.cachedIndexOnce(), nodes: map[string]*WhyMissNode{}}
}

// node builds (once) the digest node: demand-ordered calls, first-demand
// status, and the within-run annotations — all pure functions of recorded
// outcomes (design §3.1).
func (w *whyMissWalk) node(digest string) *WhyMissNode {
	if n, ok := w.nodes[digest]; ok {
		return n
	}
	n := &WhyMissNode{Digest: digest}
	w.nodes[digest] = n
	w.order = append(w.order, n)

	callIdxs := w.idx.callsByIdent[digest]
	if len(callIdxs) == 0 {
		n.Status = MissStatusUnrecorded
		return n
	}
	calls := make([]*Op, 0, len(callIdxs))
	for _, ci := range callIdxs {
		calls = append(calls, w.idx.p.ops[ci])
	}
	slices.SortStableFunc(calls, func(a, b *Op) int {
		if a.StartNS != b.StartNS {
			return int(a.StartNS - b.StartNS)
		}
		return int(a.ID - b.ID)
	})
	n.Calls = calls
	n.FirstCall = calls[0]
	n.Class = calls[0].Class

	first := calls[0]
	switch {
	case first.Open, first.Outcome == "":
		n.Status = MissStatusUndecidable
	case first.Outcome == wcprof.OutcomeHit.String(), first.Outcome == wcprof.OutcomeHitPending.String():
		n.Status = MissStatusCached
	case first.Outcome == wcprof.OutcomeDoNotCache.String():
		n.Status = MissStatusRefused
	default:
		// executed / joined / ok / error / canceled: the first demand was
		// not served from cache.
		n.Status = MissStatusMissed
	}

	isNonHitDemand := func(o string) bool {
		switch o {
		case wcprof.OutcomeExecuted.String(), wcprof.OutcomeJoined.String(), wcprof.OutcomeOK.String(),
			wcprof.OutcomeError.String(), wcprof.OutcomeCanceled.String():
			return true
		}
		return false
	}
	for _, c := range calls {
		if c.Open {
			continue
		}
		switch c.Outcome {
		case wcprof.OutcomeHitPending.String():
			n.HitPendingCalls++
		case wcprof.OutcomeJoined.String():
			n.JoinedCalls++
		}
		if n.Status == MissStatusCached && c != first && isNonHitDemand(c.Outcome) {
			// Reversal: the recipe was cached at first demand, then a later
			// demand missed (design §3.1) — context-dependent within the run.
			n.ContextDependent = true
		}
		if !isNonHitDemand(c.Outcome) {
			continue
		}
		// Category-8 evidence (per-digest summary, review round 1): this
		// demand's miss is failure-explained ONLY when everything resolved
		// before it started was a failure — at least one failed call ended
		// before it, and NO successful execution or hit did. An intervening
		// success or hit means a published/cached result existed, so
		// "failures are not cached" is not derivable for this miss: that
		// shape is the mechanism-unrecorded reversal family instead.
		var latestFailedBefore *Op
		goodBefore := false
		for _, p := range calls {
			if p == c || p.Open || p.EndNS > c.StartNS {
				continue
			}
			switch p.Outcome {
			case wcprof.OutcomeError.String(), wcprof.OutcomeCanceled.String():
				if latestFailedBefore == nil || p.EndNS > latestFailedBefore.EndNS {
					latestFailedBefore = p
				}
			case wcprof.OutcomeExecuted.String(), wcprof.OutcomeOK.String():
				goodBefore = true
				n.ReExecutedAfterSuccess = true
			case wcprof.OutcomeHit.String(), wcprof.OutcomeHitPending.String():
				goodBefore = true
			}
		}
		if latestFailedBefore != nil && !goodBefore && !n.FailedBeforeReExecution {
			n.FailedBeforeReExecution = true
			n.FailedCall = latestFailedBefore
			n.ReDemandCall = c
		}
	}
	return n
}

// inputsVector returns the node's RAW recorded cache-input vector (empties
// and self-references removed, duplicates KEPT — the §5 pairing contract is
// occurrence-level), from the first call in demand order that carries one
// (on OTel captures, seen-key suppression means only the first emission
// exists; on native every call records the same structural vector). Sets
// InputsFrom for the report; nil with a nil InputsFrom means the capture
// recorded no inputs for this call — the walk says so rather than
// descending blind.
func (w *whyMissWalk) inputsVector(n *WhyMissNode) []string {
	for _, c := range n.Calls {
		if len(c.CacheInputs) == 0 {
			continue
		}
		n.InputsFrom = c
		out := make([]string, 0, len(c.CacheInputs))
		for _, d := range c.CacheInputs {
			if d == "" || d == n.Digest {
				continue
			}
			out = append(out, d)
		}
		return out
	}
	return nil
}

// inputsOf returns the node's distinct recorded cache-input digests in
// recorded order (the walk-edge view of inputsVector: node identity is the
// digest, so duplicates collapse).
func (w *whyMissWalk) inputsOf(n *WhyMissNode) []string {
	raw := w.inputsVector(n)
	if raw == nil {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, d := range raw {
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// countPathMisses counts the walked miss nodes (the target included) from
// which the origin is reachable through the walk's input edges — reverse
// reachability over the walk DAG (discovery parents alone would undercount
// shared collaterals: a node reached first through one parent can also feed
// another). Deterministic fixpoint over the walk's creation-ordered nodes.
func (w *whyMissWalk) countPathMisses(origin *WhyMissNode) int {
	reaches := map[*WhyMissNode]bool{origin: true}
	for {
		grew := false
		for _, n := range w.order {
			if !n.walked || reaches[n] {
				continue
			}
			for _, in := range n.Inputs {
				if reaches[in] {
					reaches[n] = true
					grew = true
					break
				}
			}
		}
		if !grew {
			break
		}
	}
	count := 0
	for n := range reaches {
		if n.WalkedAsMiss() {
			count++
		}
	}
	return count
}

//
// classification (single-capture categories)
//

// scopeOf returns the node's recorded scope implicit inputs: the first call
// in demand order whose ScopeInputs are recorded (non-nil). recorded=false
// means no call recorded the scope structure (native captures pre-E2; OTel
// spans without dag.call); corrupt=true means at least one call RECORDED the
// structure but it was undecodable at load and no call carries a decodable
// one — corrupted evidence, labeled distinctly from absence (review round 1).
func scopeOf(n *WhyMissNode) (scope []ScopeInput, recorded, corrupt bool) {
	for _, c := range n.Calls {
		if c.ScopeInputs != nil {
			return c.ScopeInputs, true, false
		}
		if c.ScopeCorrupt {
			corrupt = true
		}
	}
	return nil, false, corrupt
}

// scopeWhyText is the per-scope why-text (design §3.2 category 1): each names
// the engine mechanism in the engine's own semantics — deliberate, correct
// behavior, never framed as instability.
func scopeWhyText(name string) string {
	switch name {
	case "cachePerClient":
		return "scoped per client: the calling client's ID is hashed into the cache key, so another client's result never serves this call (dagql.PerClientInput, dagql/cache_inputs.go)"
	case "cachePerSession":
		return "scoped per session: the session ID is hashed into the cache key — the engine deliberately does not cache this across sessions (dagql.PerSessionInput, dagql/cache_inputs.go)"
	case "cachePerCall":
		return "never cached across invocations: a fresh random value is hashed into the cache key on every call (dagql.PerCallInput, dagql/cache_inputs.go)"
	case "cachePerSchema":
		return "scoped to the server schema: the schema digest is hashed into the cache key, so a schema change re-keys the call (dagql.PerSchemaInput/CurrentSchemaInput, dagql/cache_inputs.go)"
	case "cachePerCallerModule":
		return "scoped per caller module: the calling module's content digest is hashed into the cache key (core.CachePerCallerModule)"
	case "fromSessionScope":
		return "tag-addressed image ref: tag-to-digest resolution is cached within, not across, sessions — a digest-pinned ref would cache across sessions (container.from fromSessionScope, core/schema/container.go)"
	default:
		if arg, found := strings.CutPrefix(name, "cacheAsRequested:"); found {
			return fmt.Sprintf("scoping chosen by the request's %q argument: per-call when caching is declined, per-client otherwise (dagql.RequestedCacheInput)", arg)
		}
		return fmt.Sprintf("engine-computed implicit input %q is hashed into the cache key (deliberate scoping)", name)
	}
}

// scopeNote renders the origin's scope evidence line: active scope inputs,
// recorded-empty ones (the engine deliberately NOT scoping on that path —
// e.g. a digest-pinned from ref), or the recording gap, each stated as what
// the data records.
func scopeNote(n *WhyMissNode) (note string, activeScopes []ScopeInput) {
	scope, recorded, corrupt := scopeOf(n)
	if corrupt {
		return "scope structure was RECORDED for this call but is malformed (undecodable at load; counted as MalformedDagCalls) — the scope evidence is lost, so classification stays undetermined rather than guessed", nil
	}
	if !recorded {
		return "scope structure not recorded in this capture (native captures do not record scope inputs today; OTel captures record them in dag.call)", nil
	}
	var active, empty []string
	for _, si := range scope {
		if si.EmptyValue {
			empty = append(empty, si.Name)
			continue
		}
		active = append(active, si.Name)
		activeScopes = append(activeScopes, si)
	}
	var parts []string
	if len(active) > 0 {
		parts = append(parts, "scope inputs recorded: "+strings.Join(active, ", "))
	}
	if len(empty) > 0 {
		parts = append(parts, "recorded with empty value (deliberately not scoping on this path): "+strings.Join(empty, ", "))
	}
	if len(parts) == 0 {
		return "the recorded call structure carries no scope inputs", nil
	}
	return strings.Join(parts, "; "), activeScopes
}

func (w *whyMissWalk) classifyOrigin(n *WhyMissNode) *WhyMissOrigin {
	o := &WhyMissOrigin{Node: n}
	note, active := scopeNote(n)
	o.ScopeNote = note

	switch {
	case n.Status == MissStatusRefused:
		o.Category = CategoryEngineRefuses
		o.Answer = fmt.Sprintf(
			"this call is never cached (do-not-cache): the engine executes it inline without a cache lookup, by design — an expected miss. Deciding datum: recorded do_not_cache outcome on call op %d.",
			n.FirstCall.ID)
	case n.FailedBeforeReExecution:
		o.Category = CategoryPriorAttemptFailed
		o.Answer = fmt.Sprintf(
			"a previous execution of this call errored in this capture; failed executions publish no result (failures are not cached), so the later demand re-executed. Deciding data: call op %d (%s) ended before call op %d re-demanded the digest.",
			n.FailedCall.ID, n.FailedCall.Outcome, n.ReDemandCall.ID)
	case n.StableInA:
		// Digest-stable against the reference capture (design §5 bullet 1):
		// the A-side outcomes answer directly. The answer never claims WHICH
		// lifetime mechanism applied (design §3.2 row 2 — that would be
		// inference) and names the history actually searched (row W16).
		a := n.aSide
		switch {
		case a.Tally.Successes == 0 && a.Tally.Failures > 0 &&
			a.Tally.Hits == 0 && a.Tally.PendingHits == 0:
			o.Category = CategoryPriorAttemptFailed
			o.Answer = fmt.Sprintf(
				"the reference capture's execution of this call errored; failed executions publish no result (failures are not cached). Deciding data: the reference capture records only failures for this digest (%s).",
				a.Tally)
		case a.Tally.Successes > 0 || a.Tally.Hits > 0 || a.Tally.PendingHits > 0:
			lead := "computed in a previous run"
			if a.Tally.Successes == 0 {
				lead = "served from cache in a previous run"
			}
			o.Category = CategoryNotRetained
			o.Answer = fmt.Sprintf(
				"%s; no cached result under this key in this capture. The engine's designed lifetime mechanisms (session release, pruning, persistence policy/reset) decide retention — which one applied here is not recorded. An expected miss for anything the engine does not retain across runs. Deciding data: the reference capture records this digest as %s. History searched: the one paired reference capture.",
				lead, a.Tally)
		default:
			// Present in the reference only as do_not_cache/open/unknown —
			// the mixed shapes whose per-digest summary rules land with the
			// E1 chunk (row W15). Stated, never guessed.
			o.Category = CategoryUndetermined
			o.Answer = fmt.Sprintf(
				"the digest exists in the reference capture but with no usable outcome evidence (%s); cause not recorded. History searched: the one paired reference capture.",
				a.Tally)
		}
	case len(active) > 0:
		o.Category = CategoryDeliberatelyScoped
		texts := make([]string, 0, len(active))
		for _, si := range active {
			texts = append(texts, scopeWhyText(si.Name))
		}
		o.Answer = "not cached across the recorded scope, by design: " + strings.Join(texts, "; AND ") +
			". An expected miss — a result keyed under another scope value cannot serve this call."
		if w.pair != nil {
			o.Answer += " In pair mode: the digest is absent from the reference capture — scope values are hashed into the recipe digest (dagql/result_call_frame.go), so each scope instance mints its own digest by design; this is the expected cross-run shape of a scoped call."
		}
	case w.pair != nil && n.PairedWith != "":
		// The deepest positionally-paired changed node: the answer names the
		// CONCRETE divergence the pairing found (§5 bullet 3) — falsely
		// claiming a self change while inputs were removed/added would
		// misattribute the invalidation (review round 1, finding 1). Digest
		// granularity is the native pair-mode contract (design §4 E3: a full
		// native call-structure emit is refused on volume grounds; arg-level
		// attribution arrives with OTel E3b).
		o.Category = CategoryInputChanged
		lead := fmt.Sprintf("this call's recipe digest differs from its positionally-paired counterpart in the reference capture (%s -> %s). ", n.PairedWith, n.Digest)
		var deltas []string
		if len(n.pairRemoved) > 0 {
			deltas = append(deltas, fmt.Sprintf("%d input(s) removed vs the reference (%s)", len(n.pairRemoved), strings.Join(boundList(n.pairRemoved, 4), ", ")))
		}
		if len(n.pairAdded) > 0 {
			deltas = append(deltas, fmt.Sprintf("%d input(s) added (%s)", len(n.pairAdded), strings.Join(boundList(n.pairAdded, 4), ", ")))
		}
		if n.pairChanged > 0 {
			deltas = append(deltas, fmt.Sprintf("%d input(s) changed pairwise (walked)", n.pairChanged))
		}
		if n.pairRefused > 0 {
			deltas = append(deltas, fmt.Sprintf("%d input gap(s) not pairwise attributable (see pair evidence)", n.pairRefused))
		}
		switch {
		case !n.pairCompared:
			reason := n.pairUnavailable
			if reason == "" {
				reason = "input vectors not compared"
			}
			o.Answer = lead + fmt.Sprintf(
				"The input-level divergence could not be decomposed (%s), so the change is reported at whole-call granularity only.", reason)
		case len(deltas) > 0:
			o.Answer = lead + "Divergence: " + strings.Join(deltas, "; ") +
				". Reported at digest granularity on native captures; arg-level detail available on OTel captures once E3b lands."
		default:
			o.Answer = lead +
				"Its recorded input vector is identical to the counterpart's (all inputs digest-anchored), so the change is in the call itself: arguments, nth/view, module ref, or scope input values. Reported at digest granularity on native captures; arg-level detail available on OTel captures once E3b lands."
		}
	case w.pair != nil:
		o.Category = CategoryNewWork
		o.Answer = "first appearance of this call in the available history: the digest is absent from the paired reference capture (the history actually searched — absence is stated over that one capture, nothing broader)."
	default:
		o.Category = CategoryUndetermined
		o.Answer = "no cached result existed under this key; cause not recorded in this capture."
	}

	if n.PairConflict {
		o.Notes = append(o.Notes, "positional pairing VOIDED for this node: distinct reference occurrences claimed this digest (see pair evidence); classified by digest identity only")
	}
	if n.ContextDependent {
		o.Notes = append(o.Notes, "context-dependent within the run: cached at first demand, later demand(s) missed — a cached result stopped being served; which lifetime mechanism applied is not recorded in this capture")
	}
	if n.ReExecutedAfterSuccess {
		o.Notes = append(o.Notes, "re-executed after a successful execution within this run — the published result stopped being served; which lifetime mechanism applied is not recorded in this capture")
	}
	if n.HitPendingCalls > 0 {
		o.Notes = append(o.Notes, fmt.Sprintf("%d hit_pending call(s): the recipe was cached with first materialization owed (nuance, not a miss)", n.HitPendingCalls))
	}
	if n.JoinedCalls > 0 {
		o.Notes = append(o.Notes, fmt.Sprintf("%d joined call(s): in-flight dedupe (nuance, not an origin)", n.JoinedCalls))
	}
	if n.InputsFrom == nil && n.Status != MissStatusRefused && !n.StableInA {
		// Both sources record inputs only when the structural vector is
		// non-empty, so a zero-input root call and an inputs-not-recorded
		// capture are indistinguishable here — the note states both readings
		// rather than picking one (never guessed).
		o.Notes = append(o.Notes, "no cache inputs recorded for this call in this capture — either the call has no structural inputs (a root call) or the capture does not record them; the walk does not descend below this node")
	}
	return o
}

// priceOrigin prices one origin by the existing what-if-cached simulator
// (design §3.3): hypothesize the origin's digest cached at pull cost 0,
// re-simulate, and report the makespan saving. The pricing inherits the
// simulator's answer contract verbatim: ineligible idents are refusals
// (simulating the engine caching what it refuses is fiction, V14) and an
// ElidedOpDemanded gate failure invalidates the number (no priced value
// renders — GateErr propagation). Row W7 pins equality with the detail run.
func priceOrigin(g *Graph, baselineNS int64, o *WhyMissOrigin) {
	res := ResolveCachedHypothesis(g, NewCachedHypothesis([]string{o.Node.Digest}, 0))
	if len(res.Idents) == 1 && res.Idents[0].State != IdentEligible {
		o.PriceRefusal = fmt.Sprintf("not priced: %s (the simulator refuses to price this hypothesis)", res.Idents[0].State)
		return
	}
	sim := NewCachedSimulation(g, res)
	makespan, err := sim.Run()
	if err != nil {
		o.PriceRefusal = fmt.Sprintf("not priced: simulation failed: %v", err)
		return
	}
	if sim.ElidedOpDemanded > 0 {
		o.PriceGateBad = true
		o.PriceRefusal = fmt.Sprintf("price gate FAILED: %d demand(s) of elided ops (unfaithful data); no priced impact renders", sim.ElidedOpDemanded)
		return
	}
	o.SavedNS = baselineNS - makespan
	o.Priced = true
}

//
// report rendering
//

// Write renders the walk report for one target.
func (r *WhyMissReport) Write(w io.Writer) {
	t := r.Target
	fmt.Fprintf(w, "why-uncached: %s — %s\n", t.Digest, t.Class)
	if r.PairMode {
		fmt.Fprintf(w, "pair mode: classified against one reference capture (categories 2/3/4 decidable there; every absence statement is scoped to that single capture)\n")
	}
	fmt.Fprintf(w, "status: %s", t.Status)
	if t.FirstCall != nil {
		fmt.Fprintf(w, " (first demand: call op %d, outcome %s; %d recorded call(s))",
			t.FirstCall.ID, orDash(t.FirstCall.Outcome), len(t.Calls))
	}
	fmt.Fprintf(w, "\n")
	for _, c := range r.Caveats {
		fmt.Fprintf(w, "caveat: %s\n", c)
	}

	switch {
	case t.Status == MissStatusCached && !t.ContextDependent:
		fmt.Fprintf(w, "this digest WAS served from cache at first demand — nothing to trace")
		if t.HitPendingCalls > 0 {
			fmt.Fprintf(w, " (%d hit_pending: recipe cached, first materialization owed)", t.HitPendingCalls)
		}
		fmt.Fprintf(w, "\n\n")
		return
	case t.Status == MissStatusUndecidable:
		fmt.Fprintf(w, "the first recorded demand is open or outcome-less at capture time: the miss status does not exist in the data, so the walk refuses to assign one\n\n")
		return
	}

	fmt.Fprintf(w, "frontier: %d origin(s); %d Merkle-collateral miss(es) on the path; %d hit boundary(ies)",
		len(r.Origins), r.Collaterals, r.HitBoundaries)
	if r.PendingHitBoundaries > 0 {
		fmt.Fprintf(w, " (%d hit_pending: recipe cached, first materialization owed — a nuance, not a miss)", r.PendingHitBoundaries)
	}
	if r.UnrecordedLeaves > 0 {
		fmt.Fprintf(w, "; %d input digest(s) with no recorded call in this capture (status unknown, labeled leaves)", r.UnrecordedLeaves)
	}
	if r.UndecidableLeaves > 0 {
		fmt.Fprintf(w, "; %d input digest(s) undecidable (open/outcome-less first demand)", r.UndecidableLeaves)
	}
	fmt.Fprintf(w, "\n\n")

	for i, o := range r.Origins {
		n := o.Node
		fmt.Fprintf(w, "origin %d: %s — %s\n", i+1, n.Digest, n.Class)
		fmt.Fprintf(w, "  category: %s\n", o.Category)
		fmt.Fprintf(w, "  answer: %s\n", o.Answer)
		if o.ScopeNote != "" {
			fmt.Fprintf(w, "  scope evidence: %s\n", o.ScopeNote)
		}
		for _, note := range o.Notes {
			fmt.Fprintf(w, "  note: %s\n", note)
		}
		if o.Priced {
			fmt.Fprintf(w, "  priced impact: save@pull=0 = %s (what-if-cached simulation over this capture; explains %d miss(es) on the walk from the target)\n",
				fmtDur(o.SavedNS), o.PathMisses)
		} else {
			fmt.Fprintf(w, "  priced impact: %s\n", o.PriceRefusal)
		}
		if path := renderPath(r.Target, n); path != "" {
			fmt.Fprintf(w, "  path: %s\n", path)
		}
		fmt.Fprintf(w, "\n")
	}

	if len(r.PairLines) > 0 {
		fmt.Fprintf(w, "pair evidence (positional pairing per the §5 contract; refusals stated, never guessed):\n")
		for _, l := range r.PairLines {
			fmt.Fprintf(w, "  %s\n", l)
		}
		fmt.Fprintf(w, "\n")
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// boundList truncates a list for answer text, stating the elision — never a
// silent cap.
func boundList(v []string, n int) []string {
	if len(v) <= n {
		return v
	}
	out := append([]string(nil), v[:n]...)
	return append(out, fmt.Sprintf("… %d more", len(v)-n))
}

// renderPath renders the discovery path target → … → origin (Merkle
// collateral shown as the path, never as the cause).
func renderPath(target, origin *WhyMissNode) string {
	if origin == target {
		return "the target itself is a frontier origin"
	}
	var chain []*WhyMissNode
	for n := origin; n != nil; n = n.walkParent {
		chain = append(chain, n)
		if n == target {
			break
		}
	}
	slices.Reverse(chain)
	parts := make([]string, 0, len(chain))
	for _, n := range chain {
		label := n.Digest
		if n.Class != "" {
			label += " (" + n.Class + ")"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, " -> ")
}

//
// CLI surface (minimal, Chunk 1): target selection + the shared entry point
//

// WhyUncachedSelection carries the why-uncached target selectors, mirroring
// the what-if-cached selector ergonomics (digest / class / argv — design
// §6.1). All resolution errors are loud (V17 spirit): a selector matching
// nothing must not silently answer a smaller question.
type WhyUncachedSelection struct {
	Digests      []string
	Classes      []string
	ExecPatterns []string
}

// Empty reports whether no selector was given.
func (sel WhyUncachedSelection) Empty() bool {
	return len(sel.Digests) == 0 && len(sel.Classes) == 0 && len(sel.ExecPatterns) == 0
}

// maxWhyUncachedTargetsPerSelector bounds how many digests one class/exec
// selector expands to — a stated budget, printed, never silent: the note
// names the cap and how to reach the rest (explicit digests).
const maxWhyUncachedTargetsPerSelector = 5

// ResolveTargets maps the selection to target digests plus resolution notes.
func (sel WhyUncachedSelection) ResolveTargets(g *Graph) ([]string, []string, error) {
	idx := g.cachedIndexOnce()
	var targets []string
	var notes []string
	seen := map[string]bool{}
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			targets = append(targets, d)
		}
	}

	for _, d := range sel.Digests {
		if len(idx.callsByIdent[d]) == 0 {
			return nil, nil, fmt.Errorf("-why-uncached digest %s not found as a call ident in this capture", d)
		}
		add(d)
	}

	if len(sel.Classes) > 0 || len(sel.ExecPatterns) > 0 {
		// Non-hit call digests with their producing weight, for the stated
		// per-selector budget ordering.
		type cand struct {
			digest   string
			weightNS int64
		}
		nonHitByClass := map[string][]cand{}
		for ident, calls := range idx.callsByIdent {
			var weight int64
			class := ""
			nonHit := false
			for _, ci := range calls {
				op := idx.p.ops[ci]
				if class == "" {
					class = op.Class
				}
				if op.Open {
					continue
				}
				switch op.Outcome {
				case wcprof.OutcomeHit.String(), wcprof.OutcomeHitPending.String(), "":
					continue
				}
				nonHit = true
				weight = max(weight, op.Duration())
			}
			if nonHit {
				nonHitByClass[class] = append(nonHitByClass[class], cand{ident, weight})
			}
		}
		for _, class := range sel.Classes {
			cands := nonHitByClass[class]
			if len(cands) == 0 {
				return nil, nil, fmt.Errorf("-why-uncached-class %q matches no uncached call digest in this capture (calls of the class either all hit or are absent)", class)
			}
			slices.SortFunc(cands, func(a, b cand) int {
				if a.weightNS != b.weightNS {
					if b.weightNS > a.weightNS {
						return 1
					}
					return -1
				}
				return strings.Compare(a.digest, b.digest)
			})
			shown := len(cands)
			if shown > maxWhyUncachedTargetsPerSelector {
				shown = maxWhyUncachedTargetsPerSelector
				notes = append(notes, fmt.Sprintf("-why-uncached-class %s: matched %d uncached digest(s); analyzing the top %d by producing wall-clock (pass explicit digests for the rest)", class, len(cands), shown))
			} else {
				notes = append(notes, fmt.Sprintf("-why-uncached-class %s: %d uncached digest(s)", class, len(cands)))
			}
			for _, c := range cands[:shown] {
				add(c.digest)
			}
		}
		for _, pat := range sel.ExecPatterns {
			digests, matchedExecs, unresolved := resolveExecPattern(g, pat)
			if matchedExecs == 0 {
				return nil, nil, fmt.Errorf("-why-uncached-exec %q matches no user exec (argv-bearing op) in this capture", pat)
			}
			if len(digests) == 0 {
				return nil, nil, fmt.Errorf("-why-uncached-exec %q matched %d exec(s) but none resolve to an owning call digest", pat, matchedExecs)
			}
			shown := len(digests)
			if shown > maxWhyUncachedTargetsPerSelector {
				shown = maxWhyUncachedTargetsPerSelector
			}
			note := fmt.Sprintf("-why-uncached-exec %s: %d exec(s) -> %d owning digest(s)", pat, matchedExecs, len(digests))
			if shown < len(digests) {
				note += fmt.Sprintf("; analyzing the first %d (pass explicit digests for the rest)", shown)
			}
			if unresolved > 0 {
				note += fmt.Sprintf(" (%d exec(s) resolve to no owning digest — stated, not silent)", unresolved)
			}
			notes = append(notes, note)
			for _, d := range digests[:shown] {
				add(d)
			}
		}
	}
	sort.Strings(targets)
	return targets, notes, nil
}

// WriteWhyUncached resolves the why-uncached selectors (no-op for an empty
// selection), runs the walk per target, and renders each report — the one
// shared CLI entry point for both analyzers. Refusals (gated capture,
// unknown target) and price-gate violations return as errors so the command
// exits non-zero.
func WriteWhyUncached(w io.Writer, g *Graph, sel WhyUncachedSelection) error {
	if sel.Empty() {
		return nil
	}
	targets, notes, err := sel.ResolveTargets(g)
	if err != nil {
		return err
	}
	for _, n := range notes {
		fmt.Fprintln(w, n)
	}
	if len(notes) > 0 {
		fmt.Fprintln(w)
	}
	var gateErr error
	for _, target := range targets {
		rep, err := RunWhyUncached(g, target)
		if err != nil {
			return err
		}
		rep.Write(w)
		if gerr := rep.GateErr(); gerr != nil && gateErr == nil {
			gateErr = gerr
		}
	}
	return gateErr
}
