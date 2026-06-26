package wcanalyze

import (
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"
)

// The replay simulator re-executes the recorded op graph as a discrete-event
// schedule under counterfactual hypotheses ("class X's self-time scaled by
// f"), assuming unlimited resources (never CPU/disk bound).
//
// Each op is replayed as its chronological timeline of actions:
//
//   - self segments: advance the op's clock by the (scaled) duration
//   - child spawns: anchor the child's simulated start at the current clock
//   - waits: clock = max(clock, simulated finish of the target); waits on
//     named resources (locks etc.) are kept as fixed delays; waits that
//     ended before their target's recorded end were abandoned
//     (cancellation) or mis-resolved and contribute nothing. A JOIN wait is
//     sequenced at its recorded END, so it gates the op's own finish but does
//     NOT gate a child the op spawned while still waiting (the spawn precedes
//     the wait's end): that concurrency is preserved, not serialized, and the
//     child's anchored start is the same whether the child is reached in order
//     or out of order. A fixed-delay (lock) wait is modelled as a non-scalable
//     segment that runs concurrently with the op's other work: its completion
//     is max(clock, clock-when-it-began + duration), so a child spawned or
//     finished while the lock was held stays concurrent with it rather than
//     serialized behind it.
//   - implicit joins: whenever the op reaches an action at original time t,
//     it first joins every child that had originally ended by t. This bakes
//     the observed ordering in as a constraint, which correctly models
//     synchronous child calls (no explicit wait edge exists for plain
//     function calls) and is conservatively safe for async children.
//
// Roots are chained by preserving original idle gaps between strictly
// sequential roots (e.g. successive queries from the CLI). The simulation
// runs in the original trace's time frame (the first root keeps its
// recorded start). An op reached out of order through a cross-tree wait
// target is anchored by replaying its producer's timeline up to (and only
// up to) its spawn under the same factor (see finish/spawnTo), so its start
// tracks the counterfactual rather than freezing at the recorded time; only
// a genuine inversion — an ancestor still mid-replay, or a cross-root
// forward reference whose own root is unscheduled — falls back to the
// recorded offset, and every such fallback is counted (FallbackAnchors),
// never silent.
//
// The timeline of every op is factor-independent, so it is compiled once
// per graph into a flat action program (replayProgram); each simulation is
// then a pure array-based DP over that program, cheap enough to run
// hundreds of counterfactuals over multi-million-op traces.

const joinEpsilonNS = int64(time.Millisecond)

// action kinds in the compiled program.
const (
	actSelf uint8 = iota
	actSpawn
	actWaitJoin
	// actWaitNoop is an abandoned wait on a known target: it contributes no
	// time (the op moved on at its own pace) but still marks an action point
	// for implicit joins.
	actWaitNoop
	// A fixed delay (lock / named resource) is a non-scalable segment that runs
	// concurrently with the op's other work, so it compiles to a pair: at its
	// start, actWaitFixedStart records the op's current sim clock X (in the
	// fixedWaitClock slot named by ref); at its end, actWaitFixedEnd raises the
	// clock to X+dur (a max, like a join — NOT clock+=dur, which would
	// over-serialize a child that ran during the delay).
	actWaitFixedStart
	actWaitFixedEnd
)

type action struct {
	// at is the recorded time the action is sequenced at: a self segment's or
	// spawn's start, a noop's or a fixed delay's-start-marker's start, or — for
	// a max-gate (a JOIN wait, or a fixed delay's end) — the wait's recorded
	// END, so the gate applies only to actions at or after that instant.
	at   int64
	dur  int64 // self duration or fixed-wait duration (unscaled)
	ref  int32 // child / wait-target op index, or fixed-wait clock slot
	kind uint8
}

// replayProgram is the per-graph compiled form of every op's timeline.
type replayProgram struct {
	ops     []*Op
	idxByID map[uint64]int32

	classKeys []ClassKey
	classOf   []int32

	startNS []int64
	endNS   []int64
	parent  []int32 // -1 when none

	// actions[actOff[i]:actOff[i+1]] is op i's timeline, sorted by
	// (at, actionRank): at equal times a max-gate (a join wait, or a fixed
	// delay's end — both sequenced at their end) applies first, then spawn, then
	// self, then a fixed delay's start-marker / noop.
	actions []action
	actOff  []int32

	// numFixedWaits is the count of fixed delays across the graph; each owns a
	// fixedWaitClock slot, named by its start/end actions' ref.
	numFixedWaits int32

	// pendIdx[pendOff[i]:pendOff[i+1]] is op i's children sorted by
	// (EndNS, ID): the implicit-join order.
	pendIdx []int32
	pendOff []int32

	roots []int32
}

func (g *Graph) program() *replayProgram {
	g.progOnce.Do(func() {
		g.prog = compileProgram(g)
	})
	return g.prog
}

func compileProgram(g *Graph) *replayProgram {
	n := len(g.Ops)
	p := &replayProgram{
		ops:     make([]*Op, 0, n),
		idxByID: make(map[uint64]int32, n),
		classOf: make([]int32, n),
		startNS: make([]int64, n),
		endNS:   make([]int64, n),
		parent:  make([]int32, n),
		actOff:  make([]int32, n+1),
		pendOff: make([]int32, n+1),
	}

	// deterministic dense indexing by op ID
	for _, op := range g.Ops {
		p.ops = append(p.ops, op)
	}
	slices.SortFunc(p.ops, func(a, b *Op) int {
		return int(a.ID - b.ID)
	})
	for i, op := range p.ops {
		p.idxByID[op.ID] = int32(i)
	}

	classIdx := make(map[ClassKey]int32)
	totalActions := 0
	totalPend := 0
	for i, op := range p.ops {
		p.startNS[i] = op.StartNS
		p.endNS[i] = op.EndNS
		p.parent[i] = -1
		if op.Parent != nil {
			if pi, ok := p.idxByID[op.Parent.ID]; ok {
				p.parent[i] = pi
			}
		}
		key := op.Key()
		ci, ok := classIdx[key]
		if !ok {
			ci = int32(len(p.classKeys))
			classIdx[key] = ci
			p.classKeys = append(p.classKeys, key)
		}
		p.classOf[i] = ci
		totalActions += len(op.SelfSegments()) + len(op.Children) + len(op.Waits)
		totalPend += len(op.Children)
	}

	p.actions = make([]action, 0, totalActions)
	p.pendIdx = make([]int32, 0, totalPend)
	// Ordering at equal recorded times:
	//   0: max-gate — a JOIN wait or a fixed delay's END (both sequenced at their
	//      recorded end). Applies first, so post-gate work at that instant is
	//      gated (a spawn the wait did not outlast sorts earlier by its smaller
	//      recorded time and so is not gated).
	//   1: spawn — anchors a child at the (post-gate) clock. BEFORE self so a
	//      child spawned at the same instant a self segment starts is anchored
	//      concurrently with that self, not serialized after it. (Only a
	//      zero-duration child can share a spawn instant with a self-segment
	//      start; a normal child's interval carves the self out of that point.)
	//   2: self — advances the clock by its (scaled) duration.
	//   3: a fixed delay's start-marker and a noop, which only record/mark.
	actionRank := func(kind uint8) int {
		switch kind {
		case actWaitJoin, actWaitFixedEnd:
			return 0
		case actSpawn:
			return 1
		case actSelf:
			return 2
		default: // actWaitFixedStart, actWaitNoop — start markers, non-gating
			return 3
		}
	}
	for i, op := range p.ops {
		p.actOff[i] = int32(len(p.actions))
		for _, seg := range op.SelfSegments() {
			p.actions = append(p.actions, action{at: seg.Start, kind: actSelf, dur: seg.End - seg.Start})
		}
		for _, c := range op.Children {
			p.actions = append(p.actions, action{at: c.StartNS, kind: actSpawn, ref: p.idxByID[c.ID]})
		}
		for _, w := range op.Waits {
			switch {
			case w.Target != nil && w.Target != op && w.EndNS >= w.Target.EndNS-joinEpsilonNS:
				// Sequence the gate at the wait's OWN recorded end (not the
				// target-end proxy, and not the start): a join gates an action
				// iff it completed by that action's recorded time. So a child
				// the op spawned before this wait ended is left ungated in both
				// the out-of-order prefix anchor and the in-order finish — the
				// child's start is order-independent by construction.
				p.actions = append(p.actions, action{at: w.EndNS, kind: actWaitJoin, ref: p.idxByID[w.Target.ID]})
			case w.Target == nil:
				// Fixed delay (lock / named resource): a non-scalable segment
				// that runs concurrently with the op's other work. Compile it to
				// a start-marker (records the sim clock when the op reaches the
				// delay) and an end-gate (raises the clock to that + dur, a max).
				// Modelling it as a max — not an additive clock += dur — keeps a
				// child spawned or finished DURING the delay concurrent with it,
				// instead of stacking the delay on top of that child's join.
				slot := p.numFixedWaits
				p.numFixedWaits++
				p.actions = append(p.actions,
					action{at: w.StartNS, kind: actWaitFixedStart, ref: slot},
					action{at: w.EndNS, kind: actWaitFixedEnd, ref: slot, dur: w.Duration()})
			default:
				// Abandoned wait: no time, only a join action point; left at its
				// start because it never gates anything.
				p.actions = append(p.actions, action{at: w.StartNS, kind: actWaitNoop})
			}
		}
		span := p.actions[p.actOff[i]:]
		slices.SortStableFunc(span, func(a, b action) int {
			if a.at != b.at {
				if a.at < b.at {
					return -1
				}
				return 1
			}
			return actionRank(a.kind) - actionRank(b.kind)
		})

		p.pendOff[i] = int32(len(p.pendIdx))
		pend := slices.Clone(op.Children)
		slices.SortFunc(pend, func(a, b *Op) int {
			if a.EndNS != b.EndNS {
				return int(a.EndNS - b.EndNS)
			}
			return int(a.ID - b.ID)
		})
		for _, c := range pend {
			p.pendIdx = append(p.pendIdx, p.idxByID[c.ID])
		}
	}
	p.actOff[n] = int32(len(p.actions))
	p.pendOff[n] = int32(len(p.pendIdx))

	p.roots = make([]int32, 0, len(g.Roots))
	for _, r := range g.Roots {
		p.roots = append(p.roots, p.idxByID[r.ID])
	}
	return p
}

// Simulation replays the compiled program under per-class self-time factors.
type Simulation struct {
	g *Graph
	p *replayProgram
	// Factors scales self-time per class; missing keys mean 1.0.
	Factors map[ClassKey]float64

	factorOf []float64

	started   []bool
	finished  []bool
	inFlight  []bool
	simStart  []int64
	simFinish []int64

	// fixedWaitClock[slot] holds the sim clock recorded at a fixed delay's start,
	// read at its end to raise the clock by the delay's duration (a max).
	fixedWaitClock []int64

	// CycleWarnings counts genuine wait/join cycles broken during replay: an
	// op whose own dependency chain re-enters it while in flight. Spurious
	// over-serializations (a concurrent wait that did not gate a spawn) are
	// resolved by the end-ordered gating model and do NOT count here.
	CycleWarnings int
	// FallbackAnchors counts ops anchored at their recorded offset because the
	// parent's prefix replay could not reach their spawn — the parent (or an
	// ancestor) was itself mid-replay, or the target was referenced from a root
	// not yet scheduled. This is the only place the recorded-offset
	// approximation survives; on a faithful trace it is rare and is reported,
	// never silent.
	FallbackAnchors int
	// FallbackAnchorOps holds a sample of fallback-anchored ops.
	FallbackAnchorOps []*Op
	// PrefixAnchors counts ops whose start was anchored by replaying their
	// parent's timeline up to (and only up to) their spawn — the normal
	// out-of-order path. Informational: large counts just mean many cross-tree
	// references, not a problem.
	PrefixAnchors int
	// SimStartConflicts counts setStart calls that tried to overwrite an
	// already-anchored op with a DIFFERENT start. The end-ordered gating model
	// makes a child's anchored start independent of whether it is reached in
	// order or out of order, so on the normal path this stays 0. Two distinct
	// sources can make it non-zero — which is why it is surfaced, not
	// hard-failed:
	//   - benign: a zero-duration child whose end coincides with a gating
	//     wait's end is implicitly join-anchored (at the pre-wait clock) before
	//     its own spawn action re-anchors it. Its finish is absorbed (a
	//     zero-duration finish can't exceed the wait it sits at), so the op's
	//     finish is unaffected — a harmless coincidence, not a wrong answer.
	//   - real: a recorded-offset fallback anchor (so it pairs with
	//     FallbackAnchors > 0) disagrees with the shifted full-finish value
	//     under a what-if factor that moves the schedule — an order-dependent
	//     saving for that cross-root / in-flight-anchored class. The baseline
	//     (factor 1, no shift) cannot see this; RunWhatIfs surfaces it.
	SimStartConflicts int
	// SimStartConflictOps holds a sample of conflicting ops.
	SimStartConflictOps []*Op
}

// NewSimulation prepares a replay over g with the given per-class self-time
// factors (nil means baseline).
func NewSimulation(g *Graph, factors map[ClassKey]float64) *Simulation {
	p := g.program()
	n := len(p.ops)
	s := &Simulation{
		g:              g,
		p:              p,
		Factors:        factors,
		factorOf:       make([]float64, len(p.classKeys)),
		started:        make([]bool, n),
		finished:       make([]bool, n),
		inFlight:       make([]bool, n),
		simStart:       make([]int64, n),
		simFinish:      make([]int64, n),
		fixedWaitClock: make([]int64, p.numFixedWaits),
	}
	for i := range s.factorOf {
		s.factorOf[i] = 1
	}
	for key, f := range factors {
		for ci, ck := range p.classKeys {
			if ck == key {
				s.factorOf[ci] = f
			}
		}
	}
	return s
}

// Run replays all roots and returns the simulated makespan: the latest root
// finish minus the earliest root start.
func (s *Simulation) Run() (makespanNS int64, err error) {
	if len(s.p.roots) == 0 {
		return 0, fmt.Errorf("no root ops to simulate")
	}

	chainOrigEnd := s.p.startNS[s.p.roots[0]]
	chainSimEnd := chainOrigEnd
	firstStart := int64(-1)
	var lastFinish int64

	for _, r := range s.p.roots {
		var start int64
		if s.p.startNS[r] >= chainOrigEnd {
			// strictly after the previous chained root finished: preserve the
			// original idle gap (client think-time) but inherit any shift
			start = chainSimEnd + (s.p.startNS[r] - chainOrigEnd)
		} else {
			// overlaps the previous root: keep the same displacement
			start = s.p.startNS[r] + (chainSimEnd - chainOrigEnd)
		}
		s.setStart(r, start)
		finish := s.finish(r)
		if firstStart < 0 || s.simStart[r] < firstStart {
			firstStart = s.simStart[r]
		}
		lastFinish = max(lastFinish, finish)
		if s.p.endNS[r] >= chainOrigEnd {
			chainOrigEnd = s.p.endNS[r]
			chainSimEnd = finish
		}
	}
	return lastFinish - firstStart, nil
}

func (s *Simulation) setStart(i int32, v int64) {
	if !s.started[i] {
		s.started[i] = true
		s.simStart[i] = v
		return
	}
	// Already anchored. The end-ordered gating model computes the same start
	// whichever path reaches the op first, so a different value here is a
	// residual order-dependence worth surfacing; keep first-write-wins.
	if s.simStart[i] != v {
		s.SimStartConflicts++
		if len(s.SimStartConflictOps) < 10 {
			s.SimStartConflictOps = append(s.SimStartConflictOps, s.p.ops[i])
		}
	}
}

// finish returns the simulated completion time of op i, replaying it (and
// transitively everything it depends on) on first use.
func (s *Simulation) finish(i int32) int64 {
	if s.finished[i] {
		return s.simFinish[i]
	}

	// Make sure the op has a simulated start. spawnTo replays only the parent's
	// PREFIX up to i's spawn — never the parent's later actions — so reaching an
	// op out of order cannot pull in cross-references that follow its spawn (the
	// false-cycle the full-parent anchor used to create). The prefix replay can
	// finish i itself (i ends at its own spawn, a zero-duration child); the memo
	// re-check below returns that without re-replaying.
	if !s.started[i] {
		s.spawnTo(s.p.parent[i], i)
		if s.finished[i] {
			return s.simFinish[i]
		}
	}

	if s.inFlight[i] {
		// genuine cycle: i's own dependency chain re-entered it. Break it by
		// assuming the recorded duration from the anchored start.
		s.CycleWarnings++
		return s.simStart[i] + (s.p.endNS[i] - s.p.startNS[i])
	}
	s.inFlight[i] = true
	clock := s.advance(i, -1)
	s.simFinish[i] = clock
	s.finished[i] = true
	s.inFlight[i] = false
	return clock
}

// advance replays op's timeline under the current factors, starting from its
// anchored start. If stopAt < 0 it runs the whole timeline and returns op's
// finish (used by finish). If stopAt >= 0 it runs only until it has spawned
// stopAt — anchoring stopAt at the parent's clock there — then returns without
// finishing op (used by the out-of-order prefix anchor, spawnTo).
//
// Because gating waits are sequenced at their recorded END (compileProgram), the
// clock evolution from op's start up to any spawn is identical whether this is
// the bounded prefix walk or the full finish: a wait still open at the spawn is
// not yet reached and does not gate it. So a child's anchored start is the same
// on both paths — order-independent — and the prefix walk simply stops early.
func (s *Simulation) advance(op, stopAt int32) int64 {
	clock := s.simStart[op]
	factor := s.factorOf[s.p.classOf[op]]

	pendCur := s.p.pendOff[op]
	pendEnd := s.p.pendOff[op+1]
	joinUpTo := func(t int64) {
		for pendCur < pendEnd {
			c := s.p.pendIdx[pendCur]
			if s.p.endNS[c] > t {
				return
			}
			if !s.started[c] {
				if s.p.startNS[c] == t {
					// c's own spawn action is pending at this same instant (the
					// only way an in-range child is unstarted here: its spawn
					// and end both equal t, a zero-duration child). Do NOT anchor
					// it at the current pre-gate clock — a max-gate at t (e.g. a
					// join wait ending now) sorts before the spawn and must raise
					// the clock first. Defer (leave pendCur) so the spawn action
					// anchors c at the gated clock; the next joinUpTo joins it.
					return
				}
				// genuine orphan: spawn outside the op's reachable actions —
				// anchor at the current clock so it is not lost.
				s.setStart(c, clock)
			}
			pendCur++
			if f := s.finish(c); f > clock {
				clock = f
			}
		}
	}

	for ai := s.p.actOff[op]; ai < s.p.actOff[op+1]; ai++ {
		a := s.p.actions[ai]
		joinUpTo(a.at)
		switch a.kind {
		case actSelf:
			clock += int64(float64(a.dur) * factor)
		case actSpawn:
			s.setStart(a.ref, clock)
			if stopAt >= 0 && a.ref == stopAt {
				return clock
			}
		case actWaitJoin:
			if f := s.finish(a.ref); f > clock {
				clock = f
			}
		case actWaitFixedStart:
			// record the clock at which the op reaches the fixed delay
			s.fixedWaitClock[a.ref] = clock
		case actWaitFixedEnd:
			// the delay completes dur after it began; a max, so concurrent work
			// (a child joined during the delay) is not double-charged
			if end := s.fixedWaitClock[a.ref] + a.dur; end > clock {
				clock = end
			}
		case actWaitNoop:
			// abandoned wait: action point only
		}
	}
	joinUpTo(s.p.endNS[op])
	return clock
}

// spawnTo anchors target's simulated start by replaying par's prefix up to
// target's spawn (par == target's parent). It guarantees target is started on
// return. The recorded-offset fallbacks — par itself, or an ancestor, still in
// flight — are the only place the recorded approximation survives; each is
// counted (FallbackAnchors), never silent.
func (s *Simulation) spawnTo(par, target int32) {
	if par < 0 {
		// True root referenced out of order (its own root chain has not been
		// scheduled by Run yet): anchor at its recorded start, in the original
		// frame. Counted: a what-if shift would make this disagree with Run's
		// chained start, which SimStartConflicts then surfaces.
		s.setStart(target, s.p.startNS[target])
		s.FallbackAnchors++
		if len(s.FallbackAnchorOps) < 10 {
			s.FallbackAnchorOps = append(s.FallbackAnchorOps, s.p.ops[target])
		}
		return
	}

	if !s.started[par] {
		// Anchor par first (recursively), unless its own parent is mid-replay.
		if pp := s.p.parent[par]; pp >= 0 && !s.inFlight[pp] {
			s.spawnTo(pp, par)
		}
		if !s.started[par] {
			s.fallbackAnchor(par)
		}
	}

	if s.inFlight[par] {
		// par is mid-replay (a genuine inversion: target is referenced from
		// within par's own prefix before par spawns it): recorded-offset last
		// resort, counted.
		s.fallbackAnchor(target)
		return
	}

	s.inFlight[par] = true
	s.advance(par, target)
	s.inFlight[par] = false

	if s.started[target] {
		s.PrefixAnchors++
		return
	}
	// par's prefix never reached target's spawn (target is not actually par's
	// recorded child): recorded-offset last resort, counted.
	s.fallbackAnchor(target)
}

// fallbackAnchor anchors i at its recorded offset within its parent's shifted
// frame (recorded start when it has no started parent) and counts it.
func (s *Simulation) fallbackAnchor(i int32) {
	anchor := s.p.startNS[i]
	if par := s.p.parent[i]; par >= 0 && s.started[par] {
		anchor = s.simStart[par] + (s.p.startNS[i] - s.p.startNS[par])
	}
	s.setStart(i, anchor)
	s.FallbackAnchors++
	if len(s.FallbackAnchorOps) < 10 {
		s.FallbackAnchorOps = append(s.FallbackAnchorOps, s.p.ops[i])
	}
}

// SimTimes returns the simulated start/finish for an op (zero values when
// the op was not reached by the replay).
func (s *Simulation) SimTimes(op *Op) (startNS, finishNS int64) {
	i, ok := s.p.idxByID[op.ID]
	if !ok || !s.finished[i] {
		return 0, 0
	}
	return s.simStart[i], s.simFinish[i]
}

// simTimesOK is SimTimes plus whether the op completed in the replay.
func (s *Simulation) simTimesOK(op *Op) (startNS, finishNS int64, ok bool) {
	i, found := s.p.idxByID[op.ID]
	if !found || !s.finished[i] {
		return 0, 0, false
	}
	return s.simStart[i], s.simFinish[i], true
}

// ExplainFinish walks the constraint chain from op downward through the
// dependency (child/wait-target) with the latest simulated finish at each
// step. Used for debugging replay-model fidelity and as a simulated critical
// chain.
func (s *Simulation) ExplainFinish(op *Op, maxDepth int) []*Op {
	chain := []*Op{op}
	seen := map[uint64]bool{op.ID: true}
	for len(chain) < maxDepth {
		var next *Op
		var nextFinish int64
		consider := func(cand *Op) {
			if cand == nil || seen[cand.ID] {
				return
			}
			_, f, ok := s.simTimesOK(cand)
			if !ok {
				return
			}
			if f > nextFinish {
				next, nextFinish = cand, f
			}
		}
		for _, c := range op.Children {
			consider(c)
		}
		for _, w := range op.Waits {
			consider(w.Target)
		}
		if next == nil {
			break
		}
		chain = append(chain, next)
		seen[next.ID] = true
		op = next
	}
	return chain
}

// WhatIfResult is the simulated impact of scaling one class's self-time.
type WhatIfResult struct {
	Key ClassKey
	// SavedNS[f] is baseline makespan minus the makespan with the class
	// scaled by factor f.
	SavedNS map[float64]int64
}

// ActualMakespanNS returns the observed makespan over root ops.
func ActualMakespanNS(g *Graph) int64 {
	if len(g.Roots) == 0 {
		return 0
	}
	start := g.Roots[0].StartNS
	var end int64
	for _, r := range g.Roots {
		start = min(start, r.StartNS)
		end = max(end, r.EndNS)
	}
	return end - start
}

// maxWhatIfClasses bounds how many classes are simulated (each class costs
// one full replay per factor); the candidates are the top classes by total
// self-time.
const maxWhatIfClasses = 200

// RunWhatIfs computes baseline makespan and, for every class with total self
// time >= minSelfNS (up to maxWhatIfClasses, by total self-time), the
// makespan saving when scaling that class's self time by each factor.
// Simulations run in parallel.
func RunWhatIfs(g *Graph, factors []float64, minSelfNS int64) (baselineNS int64, results []WhatIfResult, whatIfConflicts int, err error) {
	baseSim := NewSimulation(g, nil)
	baselineNS, err = baseSim.Run()
	if err != nil {
		return 0, nil, 0, err
	}

	totalSelf := make(map[ClassKey]int64)
	for _, op := range g.Ops {
		totalSelf[op.Key()] += op.SelfNS()
	}

	keys := make([]ClassKey, 0, len(totalSelf))
	for key, self := range totalSelf {
		if self >= minSelfNS {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b ClassKey) int {
		if totalSelf[a] != totalSelf[b] {
			return int(totalSelf[b] - totalSelf[a])
		}
		if a.Kind != b.Kind {
			if a.Kind < b.Kind {
				return -1
			}
			return 1
		}
		if a.Class < b.Class {
			return -1
		}
		if a.Class > b.Class {
			return 1
		}
		return 0
	})
	if len(keys) > maxWhatIfClasses {
		keys = keys[:maxWhatIfClasses]
	}

	results = make([]WhatIfResult, len(keys))
	for ki, key := range keys {
		results[ki] = WhatIfResult{Key: key, SavedNS: make(map[float64]int64, len(factors))}
	}

	// each (class, factor) simulation is independent; bound parallelism to
	// keep per-sim state memory in check on huge traces
	type job struct{ ki, fi int }
	jobs := make(chan job)
	workers := min(runtime.GOMAXPROCS(0), 8)
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				sim := NewSimulation(g, map[ClassKey]float64{keys[j.ki]: factors[j.fi]})
				makespan, simErr := sim.Run()
				mu.Lock()
				if simErr != nil && err == nil {
					err = simErr
				} else {
					results[j.ki].SavedNS[factors[j.fi]] = baselineNS - makespan
					// Surface order-dependence that only a non-baseline factor
					// reveals: a recorded-offset fallback anchor (cross-root /
					// in-flight ancestor) disagrees with the shifted full-finish
					// value once a factor moves the schedule. Baseline (factor 1)
					// has no shift, so report.go's baseline check cannot see it.
					whatIfConflicts = max(whatIfConflicts, sim.SimStartConflicts)
				}
				mu.Unlock()
			}
		}()
	}
	for ki := range keys {
		for fi := range factors {
			jobs <- job{ki, fi}
		}
	}
	close(jobs)
	wg.Wait()
	if err != nil {
		return 0, nil, 0, err
	}
	return baselineNS, results, whatIfConflicts, nil
}

// BlockingChain walks back from the op that finishes last in the baseline
// simulation, at each step following the child or wait whose interval ends
// latest, yielding an approximate end-of-workload critical chain.
func BlockingChain(g *Graph, maxDepth int) []*Op {
	if len(g.Roots) == 0 {
		return nil
	}
	last := g.Roots[0]
	for _, r := range g.Roots {
		if r.EndNS > last.EndNS {
			last = r
		}
	}
	chain := []*Op{last}
	cur := last
	seen := map[uint64]bool{last.ID: true}
	for len(chain) < maxDepth {
		var next *Op
		var nextEnd int64
		for _, c := range cur.Children {
			if c.EndNS > nextEnd && !seen[c.ID] {
				next, nextEnd = c, c.EndNS
			}
		}
		for _, w := range cur.Waits {
			if w.Target != nil && w.Target.EndNS > nextEnd && !seen[w.Target.ID] {
				next, nextEnd = w.Target, w.Target.EndNS
			}
		}
		if next == nil {
			break
		}
		chain = append(chain, next)
		seen[next.ID] = true
		cur = next
	}
	return chain
}

// OpDrift describes how far an op's simulated schedule diverged from its
// recorded one in a baseline replay (factor 1 everywhere). Large positive
// drift indicates the replay model over-constrains that op.
type OpDrift struct {
	Op           *Op
	SimStartNS   int64
	SimFinishNS  int64
	StartDriftNS int64 // simStart - origStart
	DurDriftNS   int64 // (simFinish-simStart) - origDuration
}

// BaselineDrift replays at factor 1 and returns the ops whose simulated
// duration grew the most versus their recorded duration, plus the ops whose
// simulated start moved latest. Used to debug replay-model fidelity.
func BaselineDrift(g *Graph, topN int) (durDrift, startDrift []OpDrift) {
	sim := NewSimulation(g, nil)
	if _, err := sim.Run(); err != nil {
		return nil, nil
	}
	drifts := make([]OpDrift, 0, len(g.Ops))
	for _, op := range g.Ops {
		start, finish, ok := sim.simTimesOK(op)
		if !ok {
			continue
		}
		drifts = append(drifts, OpDrift{
			Op:           op,
			SimStartNS:   start,
			SimFinishNS:  finish,
			StartDriftNS: start - op.StartNS,
			DurDriftNS:   (finish - start) - op.Duration(),
		})
	}
	byDur := slices.Clone(drifts)
	slices.SortFunc(byDur, func(a, b OpDrift) int { return int(b.DurDriftNS - a.DurDriftNS) })
	if len(byDur) > topN {
		byDur = byDur[:topN]
	}
	byStart := drifts
	slices.SortFunc(byStart, func(a, b OpDrift) int { return int(b.StartDriftNS - a.StartDriftNS) })
	if len(byStart) > topN {
		byStart = byStart[:topN]
	}
	return byDur, byStart
}

// DriftOrigin is an op whose baseline-simulated duration inflated beyond its
// recorded duration by more than its dependencies' inflation explains: the
// place where replay-model error is introduced (rather than inherited).
type DriftOrigin struct {
	Op         *Op
	DurDriftNS int64
	// OwnDriftNS is DurDrift minus the largest drift among children and wait
	// targets.
	OwnDriftNS int64
}

// DriftOrigins finds where baseline replay error originates, comparing each
// op's simulated finish lateness (vs its recorded end) against the worst
// lateness among its dependencies.
func DriftOrigins(g *Graph, minOwnDriftNS int64, topN int) []DriftOrigin {
	sim := NewSimulation(g, nil)
	if _, err := sim.Run(); err != nil {
		return nil
	}
	finishDrift := func(op *Op) int64 {
		_, finish, ok := sim.simTimesOK(op)
		if !ok {
			return 0
		}
		return finish - op.EndNS
	}
	var origins []DriftOrigin
	for _, op := range g.Ops {
		d := finishDrift(op)
		if d < minOwnDriftNS {
			continue
		}
		var maxDep int64
		for _, c := range op.Children {
			maxDep = max(maxDep, finishDrift(c))
		}
		for _, w := range op.Waits {
			if w.Target != nil {
				maxDep = max(maxDep, finishDrift(w.Target))
			}
		}
		if op.Parent != nil {
			// start lateness inherited from the parent's anchor
			if start, _, ok := sim.simTimesOK(op); ok {
				maxDep = max(maxDep, start-op.StartNS)
			}
		}
		own := d - maxDep
		if own >= minOwnDriftNS {
			origins = append(origins, DriftOrigin{Op: op, DurDriftNS: d, OwnDriftNS: own})
		}
	}
	slices.SortFunc(origins, func(a, b DriftOrigin) int { return int(b.OwnDriftNS - a.OwnDriftNS) })
	if len(origins) > topN {
		origins = origins[:topN]
	}
	return origins
}
