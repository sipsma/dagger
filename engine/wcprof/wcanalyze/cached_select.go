package wcanalyze

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/dagger/dagger/engine/wcprof"
)

// What-if-cached selection (design §3.2): the CLI selectors that resolve to a
// digest-set hypothesis BEFORE the sim runs. All selectors are repeatable and
// shared verbatim by both analyzers; unknown digests and selectors matching
// nothing are loud errors, never silent no-ops (catalog row V17).

// CachedSelection carries the raw what-if-cached selector values.
type CachedSelection struct {
	// Digests are explicit recipe digests (--cached, with @file manifests
	// already expanded via ExpandCachedArgs).
	Digests []string
	// Classes select all executed digests whose call class equals the value
	// (--cached-class, e.g. "Container.withExec", "myMod:Foo.bar").
	Classes []string
	// ExecPatterns select user execs by argv predicate (--cached-exec), with
	// the exec-group matcher ergonomics (boundary-aware literal prefix, or a
	// "contains:" prefix for substring), resolved to the owning call digests.
	ExecPatterns []string
	// PullCostNS is the simulated cost of each hit (--cached-pull-cost).
	PullCostNS int64
}

// Empty reports whether no selector was given.
func (sel CachedSelection) Empty() bool {
	return len(sel.Digests) == 0 && len(sel.Classes) == 0 && len(sel.ExecPatterns) == 0
}

// ExpandCachedArgs expands --cached flag values: a plain value is one recipe
// digest; a value starting with '@' names a manifest file with one digest per
// line (blank lines and #-comments skipped) — the "hand me a candidate cache
// manifest" form for the remote-cache team.
func ExpandCachedArgs(vals []string) ([]string, error) {
	var out []string
	for _, v := range vals {
		if !strings.HasPrefix(v, "@") {
			out = append(out, v)
			continue
		}
		raw, err := os.ReadFile(strings.TrimPrefix(v, "@"))
		if err != nil {
			return nil, fmt.Errorf("read cached-digest manifest: %w", err)
		}
		n := 0
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			out = append(out, line)
			n++
		}
		if n == 0 {
			// An empty manifest would silently turn the flag into a no-op —
			// V17 demands a loud error instead.
			return nil, fmt.Errorf("cached-digest manifest %s contains no digests", v)
		}
	}
	return out, nil
}

// HitDigests returns the distinct call idents with at least one recorded
// cache-hit call op, sorted — a warm run's actual hit set (catalog row V22:
// exactly the digests whose warm outcome is hit, nothing inferred). Open ops
// have no outcome yet and never count.
func HitDigests(g *Graph) []string {
	idx := g.cachedIndexOnce()
	var out []string
	for ident, calls := range idx.callsByIdent {
		for _, ci := range calls {
			op := idx.p.ops[ci]
			if !op.Open && op.Outcome == wcprof.OutcomeHit.String() {
				out = append(out, ident)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

// isCallSuccessOutcome reports whether a call op's recorded outcome is a
// non-hit success: executed/joined natively, the generic ok on the OTel
// source (which cannot distinguish executed from joined — irrelevant under
// digest-level elision, design §3.6).
func isCallSuccessOutcome(outcome string) bool {
	switch outcome {
	case wcprof.OutcomeExecuted.String(), wcprof.OutcomeJoined.String(), wcprof.OutcomeOK.String():
		return true
	}
	return false
}

// executedIdentInfo describes one executed call digest for selection/ranking.
type executedIdentInfo struct {
	class string
	// weightNS is the ident's producing wall-clock: the max duration over its
	// non-hit successful call ops. The caller blocks through its production,
	// so this interval bounds the producing subtree; it also includes the
	// caller's own waits (e.g. locks), which makes it an upper-bound ordering
	// proxy for the top-N candidate budget — a display budget only, since
	// every emitted row's saving is measured by full re-simulation.
	weightNS int64
}

// executedIdents returns every call ident with at least one non-hit
// successful call op, with its class and ranking weight.
func executedIdents(g *Graph) map[string]executedIdentInfo {
	idx := g.cachedIndexOnce()
	out := make(map[string]executedIdentInfo)
	for ident, calls := range idx.callsByIdent {
		info := executedIdentInfo{}
		seen := false
		for _, ci := range calls {
			op := idx.p.ops[ci]
			if op.Open || !isCallSuccessOutcome(op.Outcome) {
				continue
			}
			if !seen {
				info.class = op.Class
				seen = true
			}
			info.weightNS = max(info.weightNS, op.Duration())
		}
		if seen {
			out[ident] = info
		}
	}
	return out
}

// Resolve maps the selection to a digest-set hypothesis against g, plus
// human-readable resolution notes (one per selector) for the report preamble.
// Unknown digests, classes matching no executed digest, and exec patterns
// matching nothing resolvable are errors (V17). Partially resolvable exec
// patterns proceed with what resolved — stated in the note, never silent.
func (sel CachedSelection) Resolve(g *Graph) (CachedHypothesis, []string, error) {
	idx := g.cachedIndexOnce()
	hyp := CachedHypothesis{Idents: map[string]struct{}{}, PullCostNS: sel.PullCostNS}
	var notes []string

	var unknown []string
	for _, d := range sel.Digests {
		if len(idx.callsByIdent[d]) == 0 {
			unknown = append(unknown, d)
			continue
		}
		hyp.Idents[d] = struct{}{}
	}
	if len(unknown) > 0 {
		return hyp, nil, fmt.Errorf("--cached digest(s) not found as call idents in this trace: %s", strings.Join(unknown, ", "))
	}
	if len(sel.Digests) > 0 {
		notes = append(notes, fmt.Sprintf("--cached: %d explicit digest(s)", len(sel.Digests)))
	}

	if len(sel.Classes) > 0 || len(sel.ExecPatterns) > 0 {
		executed := executedIdents(g)
		for _, class := range sel.Classes {
			matched := 0
			for ident, info := range executed {
				if info.class == class {
					hyp.Idents[ident] = struct{}{}
					matched++
				}
			}
			if matched == 0 {
				return hyp, nil, fmt.Errorf("--cached-class %q matches no executed call digest in this trace", class)
			}
			notes = append(notes, fmt.Sprintf("--cached-class %s: %d executed digest(s)", class, matched))
		}

		for _, pat := range sel.ExecPatterns {
			digests, matchedExecs, unresolved := resolveExecPattern(g, pat)
			if matchedExecs == 0 {
				return hyp, nil, fmt.Errorf("--cached-exec %q matches no user exec (argv-bearing op) in this trace", pat)
			}
			if len(digests) == 0 {
				return hyp, nil, fmt.Errorf("--cached-exec %q matched %d exec(s) but none resolve to an owning call digest", pat, matchedExecs)
			}
			for _, d := range digests {
				// The owning digest need not be executed-classified (e.g. its
				// caller joined); it IS a known call ident by construction.
				hyp.Idents[d] = struct{}{}
			}
			note := fmt.Sprintf("--cached-exec %s: %d exec(s) -> %d owning digest(s)", pat, matchedExecs, len(digests))
			if unresolved > 0 {
				note += fmt.Sprintf(" (%d exec(s) unresolvable: no owning call digest recorded)", unresolved)
			}
			notes = append(notes, note)
		}
	}

	return hyp, notes, nil
}

// resolveExecPattern matches user execs (argv-bearing ops) against one
// --cached-exec pattern and resolves each match to its owning call digest:
// the nearest self-or-ancestor op of kind "exec" (exec.run) carries the
// owning call digest as its ident when the engine knew it (executor.go
// execIdent; the OTel exec.run span's dag.digest is the same value). A match
// whose exec-op ident does not name a known call ident is unresolvable —
// counted, never guessed (walking past exec.run into consumer frames would
// attribute the exec to a non-owner).
func resolveExecPattern(g *Graph, pat string) (digests []string, matchedExecs, unresolved int) {
	rule := ExecGroupRule{Match: pat}
	if rest, found := strings.CutPrefix(pat, "contains:"); found {
		rule = ExecGroupRule{Match: rest, Contains: true}
	}
	idx := g.cachedIndexOnce()
	execKind := wcprof.OpKindExec.String()
	set := map[string]struct{}{}
	for _, op := range idx.p.ops {
		if len(op.Argv) == 0 || !rule.matches(strings.Join(op.Argv, " ")) {
			continue
		}
		matchedExecs++
		owner := op
		for owner != nil && owner.Kind != execKind {
			owner = owner.Parent
		}
		if owner == nil || owner.Ident == "" || len(idx.callsByIdent[owner.Ident]) == 0 {
			unresolved++
			continue
		}
		set[owner.Ident] = struct{}{}
	}
	digests = make([]string, 0, len(set))
	for d := range set {
		digests = append(digests, d)
	}
	slices.Sort(digests)
	return digests, matchedExecs, unresolved
}
