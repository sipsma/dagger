package wcanalyze

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// Cross-run pair mode for cache-invalidation tracing (design §5): the same
// frontier walk over a CAPTURE PAIR — this capture (B, the one whose miss is
// being explained) against one reference capture (A). Digest-stable nodes
// align by digest and answer from the A side (category 2 not-retained /
// category 8 prior-attempt-failed); changed nodes pair POSITIONALLY under a
// paired parent per the §5 contract — digest-anchored occurrence-unique LCS
// first, then one-to-one class matching between consecutive anchors — and
// every ambiguity is REFUSED into a stated report line, never guessed.
// Positional pairing is sound on NATIVE pairs only (ordered, module-ref-
// inclusive CacheInputs); OTel pairs refuse it (deduplicated module-less
// dag.inputs) until E3a, keeping digest-stable analysis only.

// whyPairState is the reference-capture side of a pair walk.
type whyPairState struct {
	gA     *Graph
	aSides map[string]*calibSide
	// refusePositional: at least one capture in the pair is OTel-sourced,
	// whose input vectors are unsound for ordered pairing (design §5).
	refusePositional bool
}

func newWhyPairState(gB, gA *Graph) *whyPairState {
	return &whyPairState{
		gA:               gA,
		aSides:           calibSideInfo(gA),
		refusePositional: gA.ResultIDsCaptureLocal || gB.ResultIDsCaptureLocal,
	}
}

func (p *whyPairState) stable(digest string) bool {
	_, ok := p.aSides[digest]
	return ok
}

func (p *whyPairState) side(digest string) *calibSide {
	return sideOr(p.aSides, digest)
}

// aClassOf returns the recorded class of an A-side digest ("" when the
// reference capture recorded no call for it — an unknown class, which the
// §5 gap rule treats as unpairable rather than guessing).
func (p *whyPairState) aClassOf(digest string) string {
	if s, ok := p.aSides[digest]; ok {
		return s.Class
	}
	return ""
}

// aInputsVector returns the A-side digest's raw recorded cache-input vector
// (first call in demand order carrying one; empties and self-refs removed,
// duplicates kept — occurrence-level, mirroring whyMissWalk.inputsVector).
func (p *whyPairState) aInputsVector(digest string) []string {
	idx := p.gA.cachedIndexOnce()
	calls := make([]*Op, 0, len(idx.callsByIdent[digest]))
	for _, ci := range idx.callsByIdent[digest] {
		calls = append(calls, idx.p.ops[ci])
	}
	sortCallsDemandOrder(calls)
	for _, c := range calls {
		if len(c.CacheInputs) == 0 {
			continue
		}
		out := make([]string, 0, len(c.CacheInputs))
		for _, d := range c.CacheInputs {
			if d == "" || d == digest {
				continue
			}
			out = append(out, d)
		}
		return out
	}
	return nil
}

// rootPartner resolves the reference-side partner for an absent-from-A
// target: the SINGLE A-side digest of the target's class that is itself
// absent from B (present-in-both digests are unchanged calls, not the
// target's counterpart). Zero or multiple candidates refuse with the reason
// stated — a guessed root pairing would poison every positional pair below
// it.
func (p *whyPairState) rootPartner(w *whyMissWalk, tn *WhyMissNode) (partner, refusal string) {
	var cands []string
	for d, s := range p.aSides {
		if s.Class != tn.Class || tn.Class == "" {
			continue
		}
		if len(w.idx.callsByIdent[d]) > 0 {
			continue // present in B too: an unchanged call, not a counterpart
		}
		cands = append(cands, d)
	}
	switch len(cands) {
	case 1:
		return cands[0], ""
	case 0:
		return "", "" // plain absence — category 4 territory, no refusal line needed
	default:
		return "", fmt.Sprintf(
			"target %s not positionally pairable against the reference: %d reference digests of class %s are absent from this capture — the evidence cannot say which one is the counterpart (refused, not guessed)",
			tn.Digest, len(cands), tn.Class)
	}
}

// pairedInputEdges applies the §5 pairing contract to a paired parent's two
// input vectors and returns the walk edges for the B side, appending the
// pairwise evidence lines (removed inputs, changed-input attributions,
// refusals) to the report.
func (w *whyMissWalk) pairedInputEdges(rep *WhyMissReport, n *WhyMissNode) []whyMissEdge {
	vB := w.inputsVector(n)
	vA := w.pair.aInputsVector(n.PairedWith)
	if vB == nil || vA == nil {
		side := "this capture"
		if vB != nil {
			side = "the reference capture"
		}
		rep.PairLines = append(rep.PairLines, fmt.Sprintf(
			"%s ~ %s: positional pairing unavailable — no cache-input vector recorded on %s; descending unpaired",
			n.Digest, n.PairedWith, side))
		var out []whyMissEdge
		for _, d := range vB {
			out = append(out, whyMissEdge{b: d})
		}
		return out
	}

	res := pairInputVectors(vA, vB,
		func(d string) string { return w.pair.aClassOf(d) },
		func(d string) string { return w.node(d).Class },
	)
	ctx := fmt.Sprintf("%s ~ %s", n.Digest, n.PairedWith)
	if res.ambiguous {
		rep.PairLines = append(rep.PairLines, fmt.Sprintf(
			"%s: pairing REFUSED — the digest anchor set is ambiguous at occurrence level (repeated equal digests admit more than one maximal anchor set; the evidence cannot say which duplicate was removed); descending unpaired",
			ctx))
		var out []whyMissEdge
		for _, d := range vB {
			out = append(out, whyMissEdge{b: d})
		}
		return out
	}
	for _, l := range res.lines {
		rep.PairLines = append(rep.PairLines, ctx+": "+l)
	}
	return res.edges
}

// pairResult is the outcome of pairing one (vA, vB) input-vector pair.
type pairResult struct {
	edges     []whyMissEdge
	lines     []string
	ambiguous bool
}

// pairInputVectors implements the §5 pairing contract over two raw input
// vectors (A = reference side, B = this capture's side):
//
//  1. Anchor by digest equality — an order-preserving LCS over the two
//     digest vectors. The anchor set must be UNIQUE at occurrence level:
//     when repeated equal digests admit more than one maximal anchor set,
//     the pairing refuses. (The design's byte-identical-interval exception
//     is provably vacuous: if every position were anchored the embedding
//     would be forced, so any ambiguity leaves unanchored positions, which
//     generate report lines — hence "yields no report either way" cannot
//     hold. Ambiguity therefore always refuses.)
//  2. Between consecutive anchors, leftover positions pair one-to-one by
//     class in order ONLY when the leftover counts are equal and the
//     classes agree pairwise (a complete order-preserving matching of two
//     equal-length sequences is positional — any other complete matching
//     would cross). A side with an empty leftover makes the other side's
//     entries removed/added inputs, reported as such. Any other shape —
//     unequal non-empty leftovers, an unknown class (no recorded call),
//     a pairwise class mismatch — is the refusal line: "structural change,
//     not pairwise attributable".
func pairInputVectors(vA, vB []string, classA, classB func(string) string) pairResult {
	anchors, ambiguous := lcsAnchorsUnique(vA, vB)
	if ambiguous {
		return pairResult{ambiguous: true}
	}

	var res pairResult
	ai, bi := 0, 0
	emitGap := func(aGap, bGap []string, aStart, bStart int) {
		switch {
		case len(aGap) == 0 && len(bGap) == 0:
			return
		case len(bGap) == 0:
			for k, d := range aGap {
				res.lines = append(res.lines, fmt.Sprintf(
					"input removed vs the reference capture: %s (%s) at reference position %d",
					d, orUnknownClass(classA(d)), aStart+k+1))
			}
		case len(aGap) == 0:
			for _, d := range bGap {
				res.edges = append(res.edges, whyMissEdge{b: d})
				res.lines = append(res.lines, fmt.Sprintf(
					"input added vs the reference capture: %s (%s)", d, orUnknownClass(classB(d))))
			}
		default:
			classesOK := len(aGap) == len(bGap)
			if classesOK {
				for k := range aGap {
					ca, cb := classA(aGap[k]), classB(bGap[k])
					if ca == "" || cb == "" || ca != cb {
						classesOK = false
						break
					}
				}
			}
			if classesOK {
				for k := range aGap {
					res.edges = append(res.edges, whyMissEdge{b: bGap[k], pairA: aGap[k]})
					res.lines = append(res.lines, fmt.Sprintf(
						"input #%d changed: %s -> %s (%s); the walk descends into it",
						bStart+k+1, aGap[k], bGap[k], classB(bGap[k])))
				}
			} else {
				res.lines = append(res.lines, fmt.Sprintf(
					"structural change, not pairwise attributable: reference inputs [%s] vs this capture's [%s] between digest anchors (unequal counts, unknown classes, or a pairwise class mismatch); the B-side inputs descend unpaired",
					renderVecWithClasses(aGap, classA), renderVecWithClasses(bGap, classB)))
				for _, d := range bGap {
					res.edges = append(res.edges, whyMissEdge{b: d})
				}
			}
		}
	}
	for _, anc := range anchors {
		emitGap(vA[ai:anc[0]], vB[bi:anc[1]], ai, bi)
		res.edges = append(res.edges, whyMissEdge{b: vB[anc[1]]})
		ai, bi = anc[0]+1, anc[1]+1
	}
	emitGap(vA[ai:], vB[bi:], ai, bi)
	return res
}

func orUnknownClass(c string) string {
	if c == "" {
		return "class unknown: no recorded call"
	}
	return c
}

func renderVecWithClasses(v []string, class func(string) string) string {
	parts := make([]string, 0, len(v))
	for _, d := range v {
		parts = append(parts, fmt.Sprintf("%s(%s)", d, orUnknownClass(class(d))))
	}
	return strings.Join(parts, " ")
}

// lcsAnchorsUnique computes the digest-anchor set for the §5 pairing
// contract: the (aIndex, bIndex) pairs of a maximal common subsequence of a
// and b, together with an EXACT occurrence-level uniqueness verdict.
// Distinct maximal embeddings are counted by first-anchor enumeration
// (every embedding is uniquely identified by its ordered anchor pairs, so
// partitioning on the first anchor counts each exactly once), saturating at
// two — ambiguous means more than one maximal anchor set exists and the
// pairing must refuse ("the evidence cannot say which duplicate was
// removed"). The returned anchors are the lexicographically least embedding
// (deterministic; only rendered when unique anyway).
func lcsAnchorsUnique(a, b []string) (anchors [][2]int, ambiguous bool) {
	n, m := len(a), len(b)
	// S[i][j] = LCS length of a[i:], b[j:].
	S := make([][]int, n+1)
	for i := range S {
		S[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				S[i][j] = S[i+1][j+1] + 1
			} else {
				S[i][j] = max(S[i+1][j], S[i][j+1])
			}
		}
	}
	total := S[0][0]
	if total == 0 {
		return nil, false
	}

	// Count distinct maximal embeddings from each state, saturating at 2.
	memo := make(map[int]int)
	var count func(i, j int) int
	count = func(i, j int) int {
		if S[i][j] == 0 {
			return 1
		}
		key := i*(m+1) + j
		if c, ok := memo[key]; ok {
			return c
		}
		rem := S[i][j]
		c := 0
		for ii := i; ii < n && c < 2; ii++ {
			for jj := j; jj < m; jj++ {
				if a[ii] == b[jj] && S[ii+1][jj+1] == rem-1 {
					c += count(ii+1, jj+1)
					if c >= 2 {
						c = 2
						break
					}
				}
			}
		}
		memo[key] = c
		return c
	}
	if count(0, 0) > 1 {
		return nil, true
	}

	// Unique: reconstruct it (leftmost greedy — the only embedding).
	anchors = make([][2]int, 0, total)
	i, j := 0, 0
	for rem := total; rem > 0; rem-- {
		found := false
		for ii := i; ii < n && !found; ii++ {
			for jj := j; jj < m; jj++ {
				if a[ii] == b[jj] && S[ii+1][jj+1] == rem-1 {
					anchors = append(anchors, [2]int{ii, jj})
					i, j = ii+1, jj+1
					found = true
					break
				}
			}
		}
		if !found {
			return nil, true // unreachable on a correct table; defensive refuse
		}
	}
	return anchors, false
}

// RunWhyUncachedPair walks one target digest of g (the capture whose miss is
// being explained) to its miss frontier, classified against the reference
// capture ref (design §5). Both captures must pass the admission gate: the
// walk's statuses need g complete, and the absence claims (category 4, the
// stable/absent split) need ref complete.
func RunWhyUncachedPair(g, ref *Graph, target string) (*WhyMissReport, error) {
	return runWhyUncached(g, newWhyPairState(g, ref), target)
}

// WriteWhyUncachedPair is the pair-mode CLI entry point: resolves the
// selectors against g, walks each target against the reference capture, and
// renders the reports. Same error contract as WriteWhyUncached.
func WriteWhyUncachedPair(w io.Writer, g, ref *Graph, sel WhyUncachedSelection) error {
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
	pair := newWhyPairState(g, ref)
	var gateErr error
	for _, target := range targets {
		rep, err := runWhyUncached(g, pair, target)
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

// sortCallsDemandOrder sorts call ops into demand order: StartNS, ties by
// op id — the same deterministic rule the walk's node construction uses.
func sortCallsDemandOrder(calls []*Op) {
	slices.SortStableFunc(calls, func(a, b *Op) int {
		if a.StartNS != b.StartNS {
			return int(a.StartNS - b.StartNS)
		}
		return int(a.ID - b.ID)
	})
}
