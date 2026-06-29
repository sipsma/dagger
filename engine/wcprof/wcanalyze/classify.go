package wcanalyze

import (
	"fmt"
	"path"
	"strings"
)

// ExecGroupRule is one offline user grouping rule (design §4.6): a pattern tested
// against a user exec's space-joined argv that, on a match, relabels the exec to a
// chosen class. Rules are applied purely offline in ClassifyExecs, so a captured
// trace can be re-grouped without re-running the build.
type ExecGroupRule struct {
	// Match is the literal pattern tested against strings.Join(op.Argv, " ").
	Match string
	// Label is the class assigned to a matching exec.
	Label string
	// Contains switches Match from the default boundary-aware literal prefix to a
	// substring match — the form needed for shell-wrapped commands like
	// `sh -c "cd x && go build"`, where a fixed prefix cannot reach the real program.
	Contains bool
}

// matches reports whether the rule applies to a space-joined argv. The default is a
// boundary-aware literal prefix: it matches the whole command or a command that
// continues after a space, so "go build" matches "go build ./..." but NOT
// "go buildx ..." (no filepath.Match glob — it special-cases '/', which pervades
// argv, design §4.6). contains: switches to a plain substring match.
func (r ExecGroupRule) matches(joined string) bool {
	if r.Contains {
		return strings.Contains(joined, r.Match)
	}
	return joined == r.Match || strings.HasPrefix(joined, r.Match+" ")
}

// ParseExecGroupRule parses one --exec-group spec "<match>=<label>" (design §4.6).
// The match part may carry a leading "contains:" to request substring matching. The
// "=" is split on its FIRST occurrence (the label is everything after it), so a
// pattern that itself needs a literal "=" is not expressible in this simple form
// (an accepted limitation — command prefixes rarely contain "=").
func ParseExecGroupRule(spec string) (ExecGroupRule, error) {
	matchPart, label, ok := strings.Cut(spec, "=")
	if !ok {
		return ExecGroupRule{}, fmt.Errorf("exec-group rule %q must be <match>=<label>", spec)
	}
	contains := false
	if rest, found := strings.CutPrefix(matchPart, "contains:"); found {
		contains, matchPart = true, rest
	}
	if matchPart == "" {
		return ExecGroupRule{}, fmt.Errorf("exec-group rule %q has an empty match pattern", spec)
	}
	if label == "" {
		return ExecGroupRule{}, fmt.Errorf("exec-group rule %q has an empty label", spec)
	}
	return ExecGroupRule{Match: matchPart, Label: label, Contains: contains}, nil
}

// ParseExecGroupRules parses a list of --exec-group specs in flag order (first match
// wins at classification time). Returns nil for no specs.
func ParseExecGroupRules(specs []string) ([]ExecGroupRule, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	rules := make([]ExecGroupRule, 0, len(specs))
	for _, s := range specs {
		r, err := ParseExecGroupRule(s)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// ClassifyExecs relabels every user-exec op (one carrying argv, len(op.Argv) > 0)
// with a per-command Class derived PURELY from its explicit emitted argv — never by
// parsing a span name or shell-parsing an `sh -c` string (zero inference, design §2).
// Ops without argv are left untouched and stay the aggregated exec.processRun blob:
// the relation len(Argv) > 0 ⇒ user-process op is one-directional (sufficient to
// relabel safely), and "no argv" must never be read as "not an exec" (design §4.1d).
//
// Because op.Key() reads op.Class and every replay/report consumer reads op.Key(),
// this relabel-in-place re-buckets the whole analysis — what-if candidate selection
// and the class table — with NO change to the replay or aggregation logic (design
// §4.7). It keys off the immutable op.Argv, so it is idempotent under re-grouping.
//
// It MUST run before any simulation or the OTel structural gate compiles the replay
// program (which memoizes class buckets once), or the what-if savings would be
// computed on the stale pre-classify blob class while the report re-buckets live — a
// silent, self-contradicting headline (design §4.4, B2). Callers run it first; as
// belt-and-suspenders it also invalidates the memoized program after relabeling, so
// the pass is order-independent (a caller that simulated first cannot defeat it).
func ClassifyExecs(g *Graph, rules []ExecGroupRule) {
	relabeled := false
	for _, op := range g.Ops {
		if len(op.Argv) == 0 {
			continue
		}
		op.Class = groupExec(op.Argv, rules)
		relabeled = true
	}
	if relabeled {
		g.invalidateProgram()
	}
}

// groupExec returns an exec's class: the first matching user rule's label (in flag
// order), else the default per-command projection. Rules are a partition (first
// match wins, total via the default), so each exec gets exactly one class — mapping
// 1:1 onto the existing ClassKey model with no replay change (design §4.6).
func groupExec(argv []string, rules []ExecGroupRule) string {
	if len(rules) > 0 {
		joined := strings.Join(argv, " ")
		for _, r := range rules {
			if r.matches(joined) {
				return r.Label
			}
		}
	}
	return defaultExecClass(argv)
}

// defaultExecClass projects an exec's argv to its per-command class (design §4.3):
// basename(argv[0]), plus the first argv[1] when it is not a flag. A pure,
// deterministic projection of explicit data — e.g. "go build", "git clone",
// "npm install", "pytest tests/", and "sh" for `sh -c …` (since -c is a flag).
// Recovering the real program from a shell wrapper would require shell-parsing =
// inference = forbidden; a user --exec-group contains: rule (Stage 2) targets that.
//
// argv[0] is a container path (always forward-slash), so path.Base — not the
// OS-dependent filepath.Base — is the correct basename.
func defaultExecClass(argv []string) string {
	prog := path.Base(argv[0])
	if len(argv) >= 2 && !strings.HasPrefix(argv[1], "-") {
		return prog + " " + argv[1]
	}
	return prog
}
