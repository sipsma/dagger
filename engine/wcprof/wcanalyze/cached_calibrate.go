package wcanalyze

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/dagger/dagger/engine/wcprof"
)

// The cold/warm calibration decomposition (design §3.7; design page §7.4):
// the cold run simulated under the warm run's COMPLETE-hit set, compared
// against the warm run per recipe digest and presented as two op-partitioned
// total-recorded-time LEDGERS plus per-digest enumerations — never as a
// single cross-run percentage. A makespan is a schedule property: attributing
// the difference of two makespans (two different schedules) to per-item
// buckets is non-unique no matter how it is done, so the accounting identity
// this file implements is over recorded self-time, where every op lands in
// exactly one named bucket per capture and bucket sums equal the capture's
// total recorded self-time exactly, by construction. The four makespans print
// as context lines only.
//
// The simulator is graded only on claims it makes (structurally, digest by
// digest); another run's prices are nobody's claim (reported, never graded);
// and the gate fires only on conditions whose ≈0 expectation is derivable by
// reason — the ledger remainder and the recorded-data contradictions — never
// on the engine's own scope semantics (the side-local executed-only
// populations, reported loudly with their data-path upgrade named).

// calibBucket names one ledger bucket. Every op of a capture is assigned to
// exactly one bucket; the assignment is a pure function of recorded
// properties (kind, outcome, digest classification, containment).
type calibBucket int8

const (
	// calibBucketRemainder is the GATED remainder: an op classifiable into no
	// bucket — no digest region contains it, it is not a call, and it has no
	// session_phase/service_start ancestor. Derivably ≈0 post-emits; nonzero
	// is a recording/classification gap and FAILS the calibration.
	calibBucketRemainder calibBucket = iota
	// calibBucketRemoved (cold side only): work the hypothesis removed —
	// elided regions plus short-circuited hit-call self.
	calibBucketRemoved
	// calibBucketKept (cold side only): kept regions (roots included) — work
	// the hypothesis could not remove, replaying as recorded.
	calibBucketKept
	// calibBucketHitLookup: complete-hit calls' own self outside any
	// production region — the price of the hits themselves (B1 lookups; the
	// v1 simulation claims 0 at pull cost 0, a stated simplification).
	calibBucketHitLookup
	// calibBucketPendingB2: pending-hit lookups plus their recorded forced
	// production (lazy-semantics B2) — separated, never B1 evidence.
	calibBucketPendingB2
	// calibBucketExecBoth: producing work of digests with non-hit successful
	// calls in BOTH captures — real-world price variance, reported per
	// digest, never graded.
	calibBucketExecBoth
	// calibBucketExecOnly: producing work of digests with non-hit successful
	// calls in only THIS capture — the cross-run identity boundary (see
	// CalibrationDecomposition.ExecWarmOnly/ExecColdOnly).
	calibBucketExecOnly
	// calibBucketDNC: digests the engine refuses to cache.
	calibBucketDNC
	// calibBucketFailedOnly: digests whose non-hit calls all failed.
	calibBucketFailedOnly
	// calibBucketOpenAtCapture: digests with only open (or open-region) work —
	// not fully recorded, reported, not fed to any population.
	calibBucketOpenAtCapture
	// calibBucketSession: session/setup phases (session_phase subtrees outside
	// any digest region) — per-run overhead present in both runs.
	calibBucketSession
	// calibBucketServiceStart: service starts — per-run readiness a warm run
	// re-pays (V33), never production.
	calibBucketServiceStart
	// calibBucketHitProduction is GATED: production ops attributed to a digest
	// whose calls in the same capture are ALL complete hits, recorded STARTING
	// AFTER a complete-hit call of the digest ended — cache semantics forbid
	// it (production complete at the hit cannot run again afterwards), so any
	// occurrence is a recorded-data contradiction. Production recorded BEFORE
	// the digest's hits is the legitimate deferred-materialization shape and
	// lands in calibBucketPreHitProduction instead.
	calibBucketHitProduction
	// calibBucketPreHitProduction: production attributed to a digest whose
	// calls in this capture are all complete hits, recorded BEFORE those hits
	// — the digest's deferred production was forced earlier in this same
	// capture (e.g. through a child recipe materializing its parent), after
	// which the call legitimately hit complete. Real first-materialization
	// work this capture paid; reported, never gated.
	calibBucketPreHitProduction
	// calibBucketCallLessProduction: attributed production of a digest with NO
	// recorded call in this capture — the legitimate nested/parent-chain
	// materialization shape (a deferred recipe materializes its parent, whose
	// digest the run never called directly, design page §3.2). Usually nested
	// inside the forcing digest's region (outermost-wins absorbs it there);
	// this bucket holds the instances that surface outside every region.
	// Reported, never gated.
	calibBucketCallLessProduction

	calibBucketCount
)

func (b calibBucket) String() string {
	switch b {
	case calibBucketRemainder:
		return "UNASSIGNED REMAINDER (gated)"
	case calibBucketRemoved:
		return "removed by the hypothesis (elided + hit-call self)"
	case calibBucketKept:
		return "kept regions (demanded by survivors)"
	case calibBucketHitLookup:
		return "recorded-hit lookups (B1)"
	case calibBucketPendingB2:
		return "pending hits + their materialization (B2)"
	case calibBucketExecBoth:
		return "executed in both captures"
	case calibBucketExecOnly:
		return "executed only in this capture"
	case calibBucketDNC:
		return "engine-refuses-to-cache (do_not_cache)"
	case calibBucketFailedOnly:
		return "failed-only digests"
	case calibBucketOpenAtCapture:
		return "open at capture (not fully recorded)"
	case calibBucketSession:
		return "session/setup phases"
	case calibBucketServiceStart:
		return "service starts (per-run readiness)"
	case calibBucketHitProduction:
		return "production recorded AFTER a digest's complete hit (CONTRADICTION, gated)"
	case calibBucketPreHitProduction:
		return "production preceding the digest's complete hits (forced this capture)"
	case calibBucketCallLessProduction:
		return "production of digests with no recorded call (parent-chain materializations)"
	}
	return "invalid"
}

// CalibLedgerLine is one bucket's exact totals.
type CalibLedgerLine struct {
	Ops    int
	SelfNS int64
}

// CalibLedger is one capture's op-partitioned ledger: every op in exactly one
// bucket, bucket sums equal to the capture total exactly.
type CalibLedger struct {
	Lines           [calibBucketCount]CalibLedgerLine
	TotalOps        int
	TotalSelfNS     int64
	RemainderSample []*Op
}

func (l *CalibLedger) add(b calibBucket, op *Op) {
	l.Lines[b].Ops++
	l.Lines[b].SelfNS += op.SelfNS()
	l.TotalOps++
	l.TotalSelfNS += op.SelfNS()
	if b == calibBucketRemainder && len(l.RemainderSample) < 10 {
		l.RemainderSample = append(l.RemainderSample, op)
	}
}

// calibOutcomeTally is one digest's recorded call-outcome counts in one
// capture. Printed wherever the digest is enumerated, so no classification
// ever hides an outcome.
type calibOutcomeTally struct {
	Calls, Hits, PendingHits, Successes, Failures, DoNotCache, Open, Unknown int
}

func (t calibOutcomeTally) String() string {
	var parts []string
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s×%d", label, n))
		}
	}
	add(t.Successes, "executed")
	add(t.Hits, "hit")
	add(t.PendingHits, "hit_pending")
	add(t.DoNotCache, "do_not_cache")
	add(t.Failures, "failed")
	add(t.Open, "open")
	add(t.Unknown, "unknown")
	if len(parts) == 0 {
		return "no calls"
	}
	return strings.Join(parts, ", ")
}

// calibSide is one digest's recorded facts in one capture.
type calibSide struct {
	Present bool
	Class   string
	Tally   calibOutcomeTally
	// PriceNS is the digest's producing price: total self-time over the union
	// of its non-hit call subtrees (root inclusive) and its attributed
	// non-call production regions — the elision-region definition applied as
	// a measure. Nested foreign-digest work is included, so per-digest prices
	// overlap across digests and are NOT additive (the ledger, which is, uses
	// outermost-producer attribution instead).
	PriceNS int64
	// HitSelfNS / PendSelfNS are the digest's complete-hit / pending-hit
	// calls' own self-time (lookup prices).
	HitSelfNS  int64
	PendSelfNS int64
	// MinHitEndNS is the earliest recorded END of the digest's complete-hit
	// calls (MaxInt64 when none): the moment a complete hit witnessed the
	// production as done. Attributed production STARTING at or after it is a
	// recorded-data contradiction; production before it is the legitimate
	// forced-earlier-this-capture shape.
	MinHitEndNS int64
	// OpenProduction marks a digest with open ops anywhere inside its price
	// intervals (call subtrees or attributed regions): its production is not
	// fully recorded, mirroring the elision engine's region-inclusive open
	// check.
	OpenProduction bool
	// ExecRIDs / HitRIDs are the nonzero recorded result ids of the digest's
	// non-hit successful / complete-hit calls (sorted, deduped). Only
	// meaningful on native captures (engine-global shared-result ids); the
	// OTel loader's ids are per-capture interns.
	ExecRIDs []uint64
	HitRIDs  []uint64
}

// pureCompleteHit reports whether every recorded call of the digest in this
// capture is a complete hit — the state in which recorded production of the
// same digest is a cache-semantics contradiction.
func (s *calibSide) pureCompleteHit() bool {
	return s.Present && s.Tally.Hits > 0 &&
		s.Tally.PendingHits == 0 && s.Tally.Successes == 0 &&
		s.Tally.DoNotCache == 0 && s.Tally.Failures == 0 &&
		s.Tally.Open == 0 && s.Tally.Unknown == 0
}

// calibSideInfo computes the per-digest recorded facts for one capture.
func calibSideInfo(g *Graph) map[string]*calibSide {
	idx := g.cachedIndexOnce()
	p := idx.p
	out := make(map[string]*calibSide)

	idents := make([]string, 0, len(idx.callsByIdent))
	for d := range idx.callsByIdent {
		idents = append(idents, d)
	}
	slices.Sort(idents)

	for _, d := range idents {
		s := &calibSide{Present: true, MinHitEndNS: int64(1)<<62 - 1}
		type iv struct{ lo, out, root int32 }
		var ivs []iv
		execRIDs := map[uint64]struct{}{}
		hitRIDs := map[uint64]struct{}{}
		for _, ci := range idx.callsByIdent[d] {
			op := p.ops[ci]
			if s.Class == "" {
				s.Class = op.Class
			}
			s.Tally.Calls++
			isHit := false
			if op.Open {
				s.Tally.Open++
			} else {
				switch op.Outcome {
				case wcprof.OutcomeHit.String():
					s.Tally.Hits++
					s.HitSelfNS += op.SelfNS()
					s.MinHitEndNS = min(s.MinHitEndNS, op.EndNS)
					if op.ResultID != 0 {
						hitRIDs[op.ResultID] = struct{}{}
					}
					isHit = true
				case wcprof.OutcomeHitPending.String():
					s.Tally.PendingHits++
					s.PendSelfNS += op.SelfNS()
					isHit = true
				case wcprof.OutcomeExecuted.String(), wcprof.OutcomeJoined.String(), wcprof.OutcomeOK.String():
					s.Tally.Successes++
					if op.ResultID != 0 {
						execRIDs[op.ResultID] = struct{}{}
					}
				case wcprof.OutcomeError.String(), wcprof.OutcomeCanceled.String():
					s.Tally.Failures++
				case wcprof.OutcomeDoNotCache.String():
					s.Tally.DoNotCache++
				default:
					s.Tally.Unknown++
				}
			}
			if !isHit && idx.eulerIn[ci] >= 0 {
				ivs = append(ivs, iv{idx.eulerIn[ci], idx.eulerOut[ci], ci})
			}
		}
		for _, ai := range idx.attributedByIdent[d] {
			if idx.eulerIn[ai] < 0 || nonRegionAttribution(p.ops[ai]) {
				continue
			}
			ivs = append(ivs, iv{idx.eulerIn[ai], idx.eulerOut[ai], ai})
		}
		// Producing price: self over the union of the digest's own intervals.
		// Subtree intervals are laminar, so keeping outermost-only is exact.
		slices.SortFunc(ivs, func(a, b iv) int {
			if a.lo != b.lo {
				return int(a.lo - b.lo)
			}
			return int(b.out - a.out)
		})
		var maxOut int32 = -1
		for _, v := range ivs {
			if idx.openSub[v.root] {
				s.OpenProduction = true
			}
			if v.lo > maxOut {
				s.PriceNS += idx.selfSub[v.root]
				maxOut = v.out
			}
		}
		s.ExecRIDs = sortedRIDs(execRIDs)
		s.HitRIDs = sortedRIDs(hitRIDs)
		out[d] = s
	}
	return out
}

func sortedRIDs(m map[uint64]struct{}) []uint64 {
	if len(m) == 0 {
		return nil
	}
	out := make([]uint64, 0, len(m))
	for r := range m {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// hitAnchoredCallExec reports the structurally forbidden shape: a same-ident
// call_exec whose parent call is a COMPLETE hit. A hit returns before any
// execution op is minted (verified engine fact, design md §2.1), so this
// relation is a recorded-data contradiction at ANY recorded time — no timing
// test applies.
func hitAnchoredCallExec(op *Op) bool {
	return op.Kind == wcprof.OpKindCallExec.String() &&
		op.Parent != nil &&
		op.Parent.Kind == wcprof.OpKindCall.String() &&
		op.Parent.Ident == op.Ident &&
		!op.Parent.Open &&
		op.Parent.Outcome == wcprof.OutcomeHit.String()
}

// calibLedgerNonRegion is the LEDGER's variant of nonRegionAttribution: a
// service_start never roots a ledger region (readiness, V33), and a
// same-ident call_exec under its own call is redundant ONLY when that parent
// call itself roots a ledger region (a non-hit call). Under a HIT/pending
// parent — a shape cache semantics forbid — the call_exec's work would
// otherwise be silently absorbed by the session fallback, so it roots its
// own region (and the contradiction scan sees it). The elision engine's
// nonRegionAttribution is deliberately NOT changed: this is a ledger
// visibility rule, not a removal-semantics rule.
func calibLedgerNonRegion(op *Op) bool {
	if op.Kind == wcprof.OpKindServiceStart.String() {
		return true
	}
	if op.Kind == wcprof.OpKindCallExec.String() &&
		op.Parent != nil &&
		op.Parent.Kind == wcprof.OpKindCall.String() &&
		op.Parent.Ident == op.Ident {
		par := op.Parent
		if par.Open {
			return true // parent call region (open population) covers it
		}
		switch par.Outcome {
		case wcprof.OutcomeHit.String(), wcprof.OutcomeHitPending.String():
			return false // no parent region exists: root one here
		default:
			return true // the non-hit parent call's region covers it
		}
	}
	return false
}

// emptyCalibSide is the absent-side placeholder (Present false).
var emptyCalibSide = &calibSide{}

func sideOr(m map[string]*calibSide, d string) *calibSide {
	if s, ok := m[d]; ok {
		return s
	}
	return emptyCalibSide
}

// calibDigestBucket classifies one digest's work in one capture into its
// ledger bucket, given the other capture's side. Precedence (documented in
// design §6.5 note 15, mirroring the eligibility order where they share
// states): do_not_cache > open (calls OR production — a digest with open
// work anywhere in its price intervals is not fully recorded) > executed >
// pending-only > complete-hit-only > failed-only; unknown-only is suspect
// data and lands in the gated remainder. The row always prints ALL tallies,
// so precedence orders lines, never hides data.
func calibDigestBucket(mine, other *calibSide) calibBucket {
	switch {
	case mine.Tally.DoNotCache > 0:
		return calibBucketDNC
	case mine.Tally.Open > 0 || mine.OpenProduction:
		return calibBucketOpenAtCapture
	case mine.Tally.Successes > 0:
		if other.Present && other.Tally.Successes > 0 {
			return calibBucketExecBoth
		}
		return calibBucketExecOnly
	case mine.Tally.PendingHits > 0:
		return calibBucketPendingB2
	case mine.Tally.Hits > 0:
		return calibBucketHitLookup
	case mine.Tally.Failures > 0:
		return calibBucketFailedOnly
	default:
		return calibBucketRemainder
	}
}

// buildCalibLedger partitions one capture's ops into ledger buckets.
//
// Assignment precedence, each op exactly once (design §6.5 note 15c):
//  1. cold-side resolution marks — short-circuited or elided ops are REMOVED;
//  2. kept-region membership (root inclusive) — KEPT;
//  3. the outermost digest population region containing the op (regions are
//     the digests' non-hit call subtrees, root inclusive, plus attributed
//     production regions; nested foreign-digest production is attributed to
//     the OUTERMOST producer);
//  4. a hit call's own self — B1/B2 lookup buckets;
//  5. the nearest session_phase / service_start ancestor;
//  6. the GATED remainder.
//
// sides/others are this/other capture's per-digest facts; res and elig are
// nil/empty for the warm side. Pure-complete-hit digests' attributed
// production regions land in the gated calibBucketHitProduction.
func buildCalibLedger(g *Graph, sides, others map[string]*calibSide, res *CachedResolution, elig map[string]IdentState) *CalibLedger {
	idx := g.cachedIndexOnce()
	p := idx.p
	n := len(p.ops)
	led := &CalibLedger{}

	// Population regions, deterministic digest order.
	type region struct {
		lo, out int32
		bucket  calibBucket
	}
	var regions []region
	idents := make([]string, 0, len(sides))
	for d := range sides {
		idents = append(idents, d)
	}
	slices.Sort(idents)
	for _, d := range idents {
		s := sides[d]
		// An in-hypothesis ELIGIBLE digest's cold work is owned entirely by
		// the resolution (removed marks + kept regions); population regions
		// for it would fight that ownership.
		if state, ok := elig[d]; ok && state == IdentEligible {
			continue
		}
		b := calibDigestBucket(s, sideOr(others, d))
		// The pure-hit production split applies only when the production is
		// fully recorded: with open ops anywhere in the digest's intervals,
		// the open-at-capture classification (already in b) wins — an
		// incomplete producing interval supports no timing claim.
		pureHit := s.pureCompleteHit() && !s.OpenProduction
		for _, ci := range idx.callsByIdent[d] {
			op := p.ops[ci]
			if !op.Open && (op.Outcome == wcprof.OutcomeHit.String() || op.Outcome == wcprof.OutcomeHitPending.String()) {
				continue // hit lookups are per-op, not regions
			}
			if idx.eulerIn[ci] < 0 {
				continue // unreachable: falls to the remainder below
			}
			regions = append(regions, region{idx.eulerIn[ci], idx.eulerOut[ci], b})
		}
		for _, ai := range idx.attributedByIdent[d] {
			if idx.eulerIn[ai] < 0 || calibLedgerNonRegion(p.ops[ai]) {
				continue
			}
			rb := b
			if pureHit {
				// Production of a pure-complete-hit digest, split by recorded
				// time: BEFORE the digest's earliest complete hit ended is the
				// legitimate forced-earlier shape (reported); starting AFTER
				// it is the gated contradiction (production complete at the
				// hit cannot run again). The gate itself is the per-digest
				// scan in computeCalibrationDecomposition — independent of
				// this ledger assignment, which outermost-wins can override
				// for nested shapes.
				rb = calibBucketPreHitProduction
				if p.ops[ai].StartNS >= s.MinHitEndNS {
					rb = calibBucketHitProduction
				}
			}
			if hitAnchoredCallExec(p.ops[ai]) {
				// Structurally forbidden at any time — never the "legitimate
				// pre-hit" label.
				rb = calibBucketHitProduction
			}
			regions = append(regions, region{idx.eulerIn[ai], idx.eulerOut[ai], rb})
		}
	}
	// Attributed production of digests with no recorded call in this capture
	// (the parent-chain materialization shape): named regions of their own, so
	// instances not absorbed by a surrounding producer's region stay visible
	// instead of falling to the session fallback or the gated remainder.
	attrIdents := make([]string, 0, len(idx.attributedByIdent))
	for d := range idx.attributedByIdent {
		if len(idx.callsByIdent[d]) == 0 {
			attrIdents = append(attrIdents, d)
		}
	}
	slices.Sort(attrIdents)
	for _, d := range attrIdents {
		for _, ai := range idx.attributedByIdent[d] {
			if idx.eulerIn[ai] < 0 || nonRegionAttribution(p.ops[ai]) {
				continue
			}
			regions = append(regions, region{idx.eulerIn[ai], idx.eulerOut[ai], calibBucketCallLessProduction})
		}
	}
	slices.SortFunc(regions, func(a, b region) int {
		if a.lo != b.lo {
			return int(a.lo - b.lo)
		}
		return int(b.out - a.out)
	})

	// Outermost-wins position assignment (intervals are laminar).
	posBucket := make([]calibBucket, n)
	for i := range posBucket {
		posBucket[i] = -1
	}
	var maxOut int32 = -1
	for _, r := range regions {
		if r.lo > maxOut {
			for pos := r.lo; pos <= r.out; pos++ {
				posBucket[pos] = r.bucket
			}
			maxOut = r.out
		}
	}
	// Kept regions overlay (cold side): explicit findings whose seconds the
	// reader must find in the kept bucket, root inclusive, wherever they sit.
	if res != nil {
		for _, kr := range res.KeptRegions {
			ki, ok := p.idxByID[kr.Root.ID]
			if !ok || idx.eulerIn[ki] < 0 {
				continue
			}
			for pos := idx.eulerIn[ki]; pos <= idx.eulerOut[ki]; pos++ {
				posBucket[pos] = calibBucketKept
			}
		}
	}

	// Session/service ancestry fallback, parents before children (euler order).
	fb := make([]calibBucket, n)
	for i := range fb {
		fb[i] = -1
	}
	for pos := 0; pos < n; pos++ {
		// eulerOrder is filled only for reachable positions; position pos
		// holds a real op iff some op has eulerIn == pos.
		op := idx.eulerOrder[pos]
		if idx.eulerIn[op] != int32(pos) {
			break // past the filled prefix (unreachable ops are not toured)
		}
		switch p.ops[op].Kind {
		case wcprof.OpKindSessionPhase.String():
			fb[op] = calibBucketSession
		case wcprof.OpKindServiceStart.String():
			fb[op] = calibBucketServiceStart
		default:
			if par := p.parent[op]; par >= 0 {
				fb[op] = fb[par]
			} else if p.ops[op].Kind == "" {
				// The OTel loader deliberately leaves the session/query root
				// span unclassified (wcotel classifyKind: "session root, leaf
				// I/O"); native roots are always kind-stamped session phases,
				// so a kind-less ROOT is the session root by construction.
				fb[op] = calibBucketSession
			}
		}
	}

	for i := int32(0); i < int32(n); i++ {
		op := p.ops[i]
		if res != nil && ((res.hitShort != nil && res.hitShort[i]) || (res.elided != nil && res.elided[i])) {
			led.add(calibBucketRemoved, op)
			continue
		}
		if pos := idx.eulerIn[i]; pos >= 0 && posBucket[pos] >= 0 {
			led.add(posBucket[pos], op)
			continue
		}
		if op.Kind == wcprof.OpKindCall.String() && !op.Open {
			switch op.Outcome {
			case wcprof.OutcomeHit.String():
				led.add(calibBucketHitLookup, op)
				continue
			case wcprof.OutcomeHitPending.String():
				led.add(calibBucketPendingB2, op)
				continue
			}
		}
		if idx.eulerIn[i] >= 0 && fb[i] >= 0 {
			led.add(fb[i], op)
			continue
		}
		led.add(calibBucketRemainder, op)
	}
	return led
}

// CalibDigestLine is one enumerated digest with both captures' recorded facts.
type CalibDigestLine struct {
	Ident, Class             string
	ColdPresent, WarmPresent bool
	ColdTally, WarmTally     string
	ColdPriceNS, WarmPriceNS int64
	// PairedIdent/PairedRID name the other capture's digest that produced the
	// SAME recorded result (native shared-result-id equality; empty when the
	// captures record no pairing), and PairedPriceNS carries that digest's
	// producing price so the pair's price variance reads off one line.
	// Zero-inference: pure equality on recorded values.
	PairedIdent   string
	PairedRID     uint64
	PairedPriceNS int64
}

// CalibrationDecomposition is the §3.7 deliverable: the graded per-digest
// verdicts, the two ledgers, the cross-run enumerations, and the gate.
type CalibrationDecomposition struct {
	// GRADED — the warm complete-hit set vs the cold resolution.
	HypothesisDigests   int
	PendingOnlyExcluded []string
	RemovedCleanly      int
	KeptDigests         []string // hypothesis digests with ≥1 kept region or kept call
	IneligibleFindings  []string // "digest — state" per ineligible hypothesis digest
	NotFoundInCold      []string
	// Contradictions are recorded-data states cache semantics forbid; any
	// entry FAILS the gate.
	Contradictions []string

	WarmLedger, ColdLedger *CalibLedger

	// The cross-run enumerations (per digest, prices + outcome tallies).
	ExecBoth     []CalibDigestLine // price variance on true digest matches
	ExecWarmOnly []CalibDigestLine // the cross-run identity boundary…
	ExecColdOnly []CalibDigestLine // …reported, never gated (design §3.7.2)

	// Warm-only HIT digests, split by recorded result-id provenance (native
	// captures only): a hit on a result the cold run already created is a
	// genuine cross-run equivalence hit (expect 0, loud); a hit on a
	// warm-created result is an intra-warm derivation (normal).
	WarmOnlyHitsIntraWarm int
	WarmOnlyHitsNoRID     int
	WarmOnlyHitsCrossRun  []string

	// RIDJoinAvailable is false when either capture's result ids are
	// per-capture interns (the OTel loader) — cross-capture id equality is
	// then meaningless and the pairing/provenance consumptions are disabled.
	RIDJoinAvailable bool
}

// GateErr returns the decomposition gate verdict: nil, or one error naming
// every violated condition (design §3.7.4).
func (d *CalibrationDecomposition) GateErr() error {
	var clauses []string
	remainder := func(side string, l *CalibLedger) {
		if r := l.Lines[calibBucketRemainder]; r.Ops > 0 {
			var sample []string
			for _, op := range l.RemainderSample {
				sample = append(sample, fmt.Sprintf("%s %s", op.Kind, truncate(op.Class, 40)))
			}
			clauses = append(clauses, fmt.Sprintf("%d %s op(s) (%s self) land in no bucket — the decomposition cannot name them (recording/classification gap): %s",
				r.Ops, side, fmtDur(r.SelfNS), strings.Join(sample, "; ")))
		}
	}
	remainder("warm-capture", d.WarmLedger)
	remainder("cold-capture", d.ColdLedger)
	for _, c := range d.Contradictions {
		clauses = append(clauses, c)
	}
	if len(clauses) == 0 {
		return nil
	}
	return fmt.Errorf("what-if-cached calibration gate FAILED: %s", strings.Join(clauses, "; AND "))
}

// computeCalibrationDecomposition builds the §3.7 decomposition from the two
// graphs, the resolved detail, and the hypothesis sets. Pure function of the
// recorded graphs and the resolution.
func computeCalibrationDecomposition(coldG, warmG *Graph, detail *CachedDetail, hits, pendingOnly []string) *CalibrationDecomposition {
	coldSides := calibSideInfo(coldG)
	warmSides := calibSideInfo(warmG)

	dec := &CalibrationDecomposition{
		HypothesisDigests:   len(hits),
		PendingOnlyExcluded: slices.Clone(pendingOnly),
		RIDJoinAvailable:    !coldG.ResultIDsCaptureLocal && !warmG.ResultIDsCaptureLocal,
	}

	elig := make(map[string]IdentState, len(detail.Resolution.Idents))
	eligRow := make(map[string]*IdentEligibility, len(detail.Resolution.Idents))
	for i := range detail.Resolution.Idents {
		el := &detail.Resolution.Idents[i]
		elig[el.Ident] = el.State
		eligRow[el.Ident] = el
	}

	// GRADED verdicts per hypothesis digest, plus the cross-capture
	// contradictions cache semantics forbid.
	hitSet := make(map[string]struct{}, len(hits))
	var coldMaxRID uint64
	for _, op := range coldG.Ops {
		coldMaxRID = max(coldMaxRID, op.ResultID)
	}
	for _, d := range hits {
		hitSet[d] = struct{}{}
		cold, warm := sideOr(coldSides, d), sideOr(warmSides, d)
		switch elig[d] {
		case IdentEligible:
			el := eligRow[d]
			if el.RegionsKept > 0 || el.KeptCalls > 0 {
				dec.KeptDigests = append(dec.KeptDigests, d)
			} else {
				dec.RemovedCleanly++
			}
		case IdentNotFound:
			dec.NotFoundInCold = append(dec.NotFoundInCold, d)
			// Provenance of a hit the cold trace never named (native only):
			// its recorded result id tells whether the payload predates the
			// warm run.
			if dec.RIDJoinAvailable {
				crossRun := false
				for _, rid := range warm.HitRIDs {
					if rid <= coldMaxRID {
						crossRun = true
						break
					}
				}
				switch {
				case crossRun:
					dec.WarmOnlyHitsCrossRun = append(dec.WarmOnlyHitsCrossRun, d)
				case len(warm.HitRIDs) == 0:
					dec.WarmOnlyHitsNoRID++
				default:
					dec.WarmOnlyHitsIntraWarm++
				}
			}
		default:
			dec.IneligibleFindings = append(dec.IneligibleFindings,
				fmt.Sprintf("%s — %s", d, elig[d]))
		}
		// Contradictions (design §3.7.4 condition 2): the warm capture
		// witnessed a complete hit, so a cold capture in which the digest is
		// do_not_cache-only or failed-only contradicts cache semantics (the
		// engine caches neither).
		if cold.Present && cold.Tally.Hits == 0 && cold.Tally.PendingHits == 0 && cold.Tally.Successes == 0 && cold.Tally.Open == 0 && cold.Tally.Unknown == 0 {
			switch {
			case cold.Tally.DoNotCache > 0:
				dec.Contradictions = append(dec.Contradictions,
					fmt.Sprintf("digest %s hit warm complete but is do_not_cache-only in the cold capture", d))
			case cold.Tally.Failures > 0:
				dec.Contradictions = append(dec.Contradictions,
					fmt.Sprintf("digest %s hit warm complete but is failed-only in the cold capture (failed executions publish no result)", d))
			}
		}
	}
	// The production-after-complete-hit contradiction, scanned per digest
	// directly over the attribution index — deliberately independent of the
	// ledger's outermost-wins assignment, which can absorb a NESTED
	// contradiction into a surrounding producer's bucket. Production
	// recorded BEFORE the digest's earliest complete hit is the legitimate
	// forced-earlier-this-capture shape and never fires the TIMING rule; a
	// same-ident call_exec under a complete-hit call is structurally
	// forbidden at ANY time (a hit returns before any execution op is
	// minted) and fires regardless — including on mixed-outcome digests the
	// pure-hit timing rule does not cover.
	scanHitProduction := func(g *Graph, sides map[string]*calibSide, side string) {
		idx := g.cachedIndexOnce()
		idents := make([]string, 0, len(sides))
		for dg := range sides {
			idents = append(idents, dg)
		}
		slices.Sort(idents)
		for _, dg := range idents {
			s := sides[dg]
			pure := s.pureCompleteHit()
			for _, ai := range idx.attributedByIdent[dg] {
				op := idx.p.ops[ai]
				// Only service_start is exempt here (readiness, V33). The
				// region-redundancy exemption for anchored call_execs does
				// NOT apply: on a pure-complete-hit digest ANY call_exec is
				// production cache semantics forbid, wherever it hangs.
				if op.Kind == wcprof.OpKindServiceStart.String() {
					continue
				}
				if hitAnchoredCallExec(op) {
					dec.Contradictions = append(dec.Contradictions,
						fmt.Sprintf("digest %s: a complete-hit call in the %s capture has a same-ident call_exec child — a hit returns before any execution op is minted", dg, side))
					break
				}
				if pure && op.StartNS >= s.MinHitEndNS {
					dec.Contradictions = append(dec.Contradictions,
						fmt.Sprintf("digest %s: %s production recorded in the %s capture STARTING AFTER the digest's complete hit ended — production complete at a hit cannot run again", dg, op.Kind, side))
					break
				}
			}
		}
	}
	scanHitProduction(warmG, warmSides, "warm")
	scanHitProduction(coldG, coldSides, "cold")
	slices.Sort(dec.KeptDigests)
	slices.Sort(dec.NotFoundInCold)
	slices.Sort(dec.IneligibleFindings)
	slices.Sort(dec.WarmOnlyHitsCrossRun)
	slices.Sort(dec.Contradictions)

	// The two ledgers.
	dec.WarmLedger = buildCalibLedger(warmG, warmSides, coldSides, nil, nil)
	dec.ColdLedger = buildCalibLedger(coldG, coldSides, warmSides, detail.Resolution, elig)

	// Cross-run enumerations. The result-id pairing is a pure equality join
	// on recorded values; a missing pair stays two-sided, never guessed.
	ridIndex := func(sides map[string]*calibSide) map[uint64]string {
		out := map[uint64]string{}
		idents := make([]string, 0, len(sides))
		for d := range sides {
			idents = append(idents, d)
		}
		slices.Sort(idents)
		for _, d := range idents {
			for _, rid := range sides[d].ExecRIDs {
				if _, taken := out[rid]; !taken {
					out[rid] = d
				}
			}
		}
		return out
	}
	ridToCold, ridToWarm := map[uint64]string{}, map[uint64]string{}
	if dec.RIDJoinAvailable {
		ridToCold = ridIndex(coldSides)
		ridToWarm = ridIndex(warmSides)
	}
	line := func(d string, cold, warm *calibSide) CalibDigestLine {
		class := cold.Class
		if class == "" {
			class = warm.Class
		}
		l := CalibDigestLine{
			Ident: d, Class: class,
			ColdPresent: cold.Present, WarmPresent: warm.Present,
			ColdPriceNS: cold.PriceNS, WarmPriceNS: warm.PriceNS,
		}
		if cold.Present {
			l.ColdTally = cold.Tally.String()
		}
		if warm.Present {
			l.WarmTally = warm.Tally.String()
		}
		return l
	}
	allIdents := make(map[string]struct{}, len(coldSides)+len(warmSides))
	for d := range coldSides {
		allIdents[d] = struct{}{}
	}
	for d := range warmSides {
		allIdents[d] = struct{}{}
	}
	idents := make([]string, 0, len(allIdents))
	for d := range allIdents {
		idents = append(idents, d)
	}
	slices.Sort(idents)
	for _, d := range idents {
		cold, warm := sideOr(coldSides, d), sideOr(warmSides, d)
		coldExec, warmExec := cold.Tally.Successes > 0, warm.Tally.Successes > 0
		switch {
		case coldExec && warmExec:
			dec.ExecBoth = append(dec.ExecBoth, line(d, cold, warm))
		case warmExec:
			l := line(d, cold, warm)
			for _, rid := range warm.ExecRIDs {
				if cd, ok := ridToCold[rid]; ok && cd != d {
					l.PairedIdent, l.PairedRID = cd, rid
					l.PairedPriceNS = sideOr(coldSides, cd).PriceNS
					break
				}
			}
			dec.ExecWarmOnly = append(dec.ExecWarmOnly, l)
		case coldExec:
			if _, inHyp := hitSet[d]; inHyp {
				break // transferred: graded above, removed/kept by the resolution
			}
			l := line(d, cold, warm)
			// Name the warm digest that re-derived the same recorded result,
			// when one exists.
			for _, rid := range cold.ExecRIDs {
				if wd, ok := ridToWarm[rid]; ok && wd != d {
					l.PairedIdent, l.PairedRID = wd, rid
					l.PairedPriceNS = sideOr(warmSides, wd).PriceNS
					break
				}
			}
			dec.ExecColdOnly = append(dec.ExecColdOnly, l)
		}
	}
	byPriceDesc := func(price func(CalibDigestLine) int64) func(a, b CalibDigestLine) int {
		return func(a, b CalibDigestLine) int {
			if pa, pb := price(a), price(b); pa != pb {
				return int(pb - pa)
			}
			return strings.Compare(a.Ident, b.Ident)
		}
	}
	slices.SortFunc(dec.ExecBoth, byPriceDesc(func(l CalibDigestLine) int64 { return l.ColdPriceNS + l.WarmPriceNS }))
	slices.SortFunc(dec.ExecWarmOnly, byPriceDesc(func(l CalibDigestLine) int64 { return l.WarmPriceNS }))
	slices.SortFunc(dec.ExecColdOnly, byPriceDesc(func(l CalibDigestLine) int64 { return l.ColdPriceNS }))

	return dec
}

// CachedCalibration is the cold/warm calibration result: the explicit-set
// detail over the cold run plus the §3.7 bucketed decomposition. The
// decomposition is the deliverable; no cross-run percentage is computed
// anywhere.
type CachedCalibration struct {
	Detail *CachedDetail
	Decomp *CalibrationDecomposition
	// Legacy summary fields, kept for assertions: the extracted hit-set size,
	// how many exist as call idents in the cold run, and the pending-only
	// exclusion count.
	WarmHitDigests  int
	FoundInRun      int
	WarmPendingHits int
	ColdActualNS    int64
	WarmActualNS    int64
}

// RunCachedCalibration extracts the warm run's complete-hit digests, simulates
// the cold run under them at the given pull cost, and computes the bucketed
// decomposition.
func RunCachedCalibration(coldG, warmG *Graph, pullCostNS int64, chainDepth int) (*CachedCalibration, error) {
	// The cold graph's admission gate fires inside RunCachedDetail; the warm
	// capture is gated here for the same reasons — a dropped warm event could
	// have been a hit call (silently shrinking the hypothesis) or any op of
	// the warm ledger, and suppressed ident derivations strip the attribution
	// the warm-side classification depends on.
	if warmG.DroppedEvents > 0 {
		return nil, fmt.Errorf("what-if-cached calibration REFUSED: the WARM capture dropped %d recorder event(s) — the extracted hit set may be silently incomplete; recapture with a larger wcprof buffer", warmG.DroppedEvents)
	}
	if warmG.SuppressedIdentDerivations > 0 {
		return nil, fmt.Errorf("what-if-cached calibration REFUSED: the WARM capture records %d lazy ident derivation failure(s) — warm production attribution is incomplete, so the warm ledger cannot be trusted; this counter is expected to be 0 (an emit bug to fix, never data to analyze around)", warmG.SuppressedIdentDerivations)
	}
	hits := HitDigests(warmG)
	// Pending-production hits (B2) do not witness a materialized payload:
	// counted and printed, never asserted into the B1 hypothesis (V30). A
	// digest with BOTH outcomes stays in via its complete hit.
	inHits := make(map[string]struct{}, len(hits))
	for _, d := range hits {
		inHits[d] = struct{}{}
	}
	var pendingOnly []string
	for _, d := range PendingHitDigests(warmG) {
		if _, ok := inHits[d]; !ok {
			pendingOnly = append(pendingOnly, d)
		}
	}
	if len(hits) == 0 {
		return nil, fmt.Errorf("the warm capture records no complete cache-hit calls — not a warm run, or hits were not recorded")
	}
	detail, err := RunCachedDetail(coldG, NewCachedHypothesis(hits, pullCostNS), chainDepth)
	if err != nil {
		return nil, err
	}
	dec := computeCalibrationDecomposition(coldG, warmG, detail, hits, pendingOnly)
	return &CachedCalibration{
		Detail:          detail,
		Decomp:          dec,
		WarmHitDigests:  len(hits),
		FoundInRun:      len(hits) - len(dec.NotFoundInCold),
		WarmPendingHits: len(pendingOnly),
		ColdActualNS:    ActualMakespanNS(coldG),
		WarmActualNS:    ActualMakespanNS(warmG),
	}, nil
}

// GateErr combines the detail's replay gate with the decomposition gate: the
// calibration's result is invalid if either fails.
func (c *CachedCalibration) GateErr() error {
	derr := c.Detail.GateErr()
	gerr := c.Decomp.GateErr()
	switch {
	case derr != nil && gerr != nil:
		return fmt.Errorf("%v; AND %v", derr, gerr)
	case derr != nil:
		return derr
	default:
		return gerr
	}
}

const calibDigestLineLimit = 10

// Write renders the detail section followed by the decomposition.
func (c *CachedCalibration) Write(w io.Writer) {
	c.Detail.Write(w)
	d := c.Decomp

	fmt.Fprintf(w, "calibration: cold run under the warm run's complete-hit set — bucketed decomposition\n")
	fmt.Fprintf(w, "(the simulator is graded only on claims it makes, structurally per digest; another\n")
	fmt.Fprintf(w, " run's prices are nobody's claim; no cross-run percentage is a fidelity number)\n\n")

	fmt.Fprintf(w, "context makespans (schedule properties — context, never a grade):\n")
	fmt.Fprintf(w, "  cold actual:          %s\n", fmtDur(c.ColdActualNS))
	fmt.Fprintf(w, "  cold baseline (sim):  %s\n", fmtDur(c.Detail.BaselineNS))
	fmt.Fprintf(w, "  counterfactual (sim): %s\n", fmtDur(c.Detail.MakespanNS))
	fmt.Fprintf(w, "  warm actual:          %s\n\n", fmtDur(c.WarmActualNS))

	fmt.Fprintf(w, "GRADED — warm complete-hit digests vs the cold resolution:\n")
	fmt.Fprintf(w, "  hypothesis: %d digest(s) hit warm with production complete", d.HypothesisDigests)
	if len(d.PendingOnlyExcluded) > 0 {
		fmt.Fprintf(w, "; %d pending-only digest(s) excluded (B2, never B1 evidence)", len(d.PendingOnlyExcluded))
	}
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "  %d removed cleanly (calls hit or covered by elision; producing regions removed)\n", d.RemovedCleanly)
	writeDigestList(w, fmt.Sprintf("%d kept with reason (regions listed in the kept-regions section above)", len(d.KeptDigests)), d.KeptDigests, len(d.KeptDigests) > 0)
	writeDigestList(w, fmt.Sprintf("%d ineligible", len(d.IneligibleFindings)), d.IneligibleFindings, len(d.IneligibleFindings) > 0)
	writeDigestList(w, fmt.Sprintf("%d not found in the cold capture (coverage findings)", len(d.NotFoundInCold)), d.NotFoundInCold, len(d.NotFoundInCold) > 0)
	if len(d.Contradictions) > 0 {
		fmt.Fprintf(w, "  CONTRADICTIONS (recorded data forbids these; the gate FAILS):\n")
		for _, s := range d.Contradictions {
			fmt.Fprintf(w, "    %s\n", s)
		}
	}
	fmt.Fprintf(w, "\n")

	writeCalibLedger(w, "warm capture", d.WarmLedger, []calibBucket{
		calibBucketHitLookup, calibBucketPendingB2, calibBucketExecBoth,
		calibBucketExecOnly, calibBucketPreHitProduction,
		calibBucketCallLessProduction, calibBucketDNC, calibBucketFailedOnly,
		calibBucketOpenAtCapture, calibBucketSession, calibBucketServiceStart,
		calibBucketHitProduction, calibBucketRemainder,
	})
	writeCalibLedger(w, "cold capture under the hypothesis", d.ColdLedger, []calibBucket{
		calibBucketRemoved, calibBucketKept, calibBucketHitLookup,
		calibBucketPendingB2, calibBucketExecBoth, calibBucketExecOnly,
		calibBucketPreHitProduction, calibBucketCallLessProduction,
		calibBucketDNC, calibBucketFailedOnly, calibBucketOpenAtCapture,
		calibBucketSession, calibBucketServiceStart, calibBucketHitProduction,
		calibBucketRemainder,
	})

	fmt.Fprintf(w, "executed only in one capture — the cross-run identity boundary:\n")
	fmt.Fprintf(w, "  (the engine hashes scope values — per-client, per-session, per-call, per-schema —\n")
	fmt.Fprintf(w, "   into recipe digests [dagql/cache_inputs.go; core/schema/container.go:1015;\n")
	fmt.Fprintf(w, "   core/schema/modulesource.go:64], so scope-limited chains mint fresh digests every\n")
	fmt.Fprintf(w, "   run and CANNOT match across captures by design; which of the digests below did so\n")
	fmt.Fprintf(w, "   is not verifiable from recorded data until the scope kind is recorded (design\n")
	fmt.Fprintf(w, "   §8.2) — no per-digest claim is made here)\n")
	writeDigestPrices(w, fmt.Sprintf("executed only in WARM: %d digest(s), %s producing self — the number the §8.2 scope recording will convert to per-digest-verified", len(d.ExecWarmOnly), fmtDur(sumPrices(d.ExecWarmOnly, false))), d.ExecWarmOnly, false)
	writeDigestPrices(w, fmt.Sprintf("executed only in COLD (surviving the counterfactual at cold prices): %d digest(s), %s producing self", len(d.ExecColdOnly), fmtDur(sumPrices(d.ExecColdOnly, true))), d.ExecColdOnly, true)
	if len(d.ExecBoth) > 0 {
		fmt.Fprintf(w, "  executed in BOTH captures (same digest — the cache did not retain; outcomes shown):\n")
		for i, l := range d.ExecBoth {
			if i >= calibDigestLineLimit {
				fmt.Fprintf(w, "    … %d more\n", len(d.ExecBoth)-i)
				break
			}
			fmt.Fprintf(w, "    %s %s — cold %s [%s] vs warm %s [%s]\n",
				truncate(l.Class, 40), l.Ident, fmtDur(l.ColdPriceNS), l.ColdTally, fmtDur(l.WarmPriceNS), l.WarmTally)
		}
	} else {
		fmt.Fprintf(w, "  executed in BOTH captures: none\n")
	}
	if d.RIDJoinAvailable {
		nWarmOnlyHits := d.WarmOnlyHitsIntraWarm + d.WarmOnlyHitsNoRID + len(d.WarmOnlyHitsCrossRun)
		if nWarmOnlyHits > 0 {
			fmt.Fprintf(w, "  warm-only hit digests: %d — %d on result ids beyond the cold capture's recorded range\n", nWarmOnlyHits, d.WarmOnlyHitsIntraWarm)
			fmt.Fprintf(w, "    (intra-warm derivations on a fresh engine; an engine with imported persisted results\n")
			fmt.Fprintf(w, "     can also mint such ids, so warm-created is consistent, not proven)\n")
			if d.WarmOnlyHitsNoRID > 0 {
				fmt.Fprintf(w, "    %d with no recorded result id\n", d.WarmOnlyHitsNoRID)
			}
			fmt.Fprintf(w, "    hits on results that existed BEFORE the warm run began (rid within the cold capture's recorded range): %d — expect 0; nonzero is stated-simplification-#1 territory (equivalence-resolved hits the recipe join cannot see)\n", len(d.WarmOnlyHitsCrossRun))
			for _, dg := range d.WarmOnlyHitsCrossRun {
				fmt.Fprintf(w, "      %s\n", dg)
			}
		}
	} else {
		fmt.Fprintf(w, "  (result-id pairing and provenance unavailable: at least one capture's result ids\n")
		fmt.Fprintf(w, "   are per-capture interns — the OTel loader — never comparable across captures)\n")
	}
	fmt.Fprintf(w, "\n")

	if err := c.Decomp.GateErr(); err != nil {
		fmt.Fprintf(w, "gate: FAILED — %v\n\n", err)
	} else {
		fmt.Fprintf(w, "gate: PASS — both ledger remainders empty, no contradictions\n\n")
	}
}

func writeDigestList(w io.Writer, header string, items []string, show bool) {
	fmt.Fprintf(w, "  %s\n", header)
	if !show {
		return
	}
	for i, s := range items {
		if i >= calibDigestLineLimit {
			fmt.Fprintf(w, "    … %d more\n", len(items)-i)
			return
		}
		fmt.Fprintf(w, "    %s\n", s)
	}
}

func sumPrices(lines []CalibDigestLine, cold bool) int64 {
	var total int64
	for _, l := range lines {
		if cold {
			total += l.ColdPriceNS
		} else {
			total += l.WarmPriceNS
		}
	}
	return total
}

func writeDigestPrices(w io.Writer, header string, lines []CalibDigestLine, cold bool) {
	fmt.Fprintf(w, "  %s\n", header)
	for i, l := range lines {
		if i >= calibDigestLineLimit {
			fmt.Fprintf(w, "    … %d more\n", len(lines)-i)
			return
		}
		price, tally := l.WarmPriceNS, l.WarmTally
		otherTally, otherSide := l.ColdTally, "cold"
		if cold {
			price, tally = l.ColdPriceNS, l.ColdTally
			otherTally, otherSide = l.WarmTally, "warm"
		}
		fmt.Fprintf(w, "    %s %s — %s [%s]", truncate(l.Class, 40), l.Ident, fmtDur(price), tally)
		if otherTally != "" {
			fmt.Fprintf(w, " (other capture: %s)", otherTally)
		}
		if l.PairedIdent != "" {
			// The pair's price variance on one line: the other capture
			// re-derived the SAME recorded result under this digest, at this
			// price (pure result-id equality, native captures only).
			fmt.Fprintf(w, " — same recorded result as %s (rid %d, %s price %s)",
				l.PairedIdent, l.PairedRID, otherSide, fmtDur(l.PairedPriceNS))
		}
		fmt.Fprintf(w, "\n")
	}
}

func writeCalibLedger(w io.Writer, title string, l *CalibLedger, order []calibBucket) {
	fmt.Fprintf(w, "LEDGER — %s (every recorded second in exactly one bucket; sums exact):\n", title)
	for _, b := range order {
		line := l.Lines[b]
		if line.Ops == 0 && b != calibBucketRemainder {
			continue
		}
		fmt.Fprintf(w, "  %-68s %10s in %d op(s)\n", b.String()+":", fmtDur(line.SelfNS), line.Ops)
	}
	fmt.Fprintf(w, "  %-68s %10s in %d op(s)\n\n", "total recorded self:", fmtDur(l.TotalSelfNS), l.TotalOps)
}
