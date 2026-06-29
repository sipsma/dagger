package wcanalyze

import (
	"path"
	"strings"
)

// ExecGroupRule is one offline user grouping rule (design §4.6): a pattern matched
// against a user exec's argv that, on a match, relabels the exec's class. Its fields
// and matching land with the --exec-group flag (Stage 2); Stage 1 needs only the
// default per-command projection, so ClassifyExecs(g, nil) is the no-config path.
type ExecGroupRule struct{}

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
		// Stage 2 consults the user rules here (first match wins) before falling
		// through to the default; Stage 1 is the default per-command projection.
		op.Class = defaultExecClass(op.Argv)
		relabeled = true
	}
	if relabeled {
		g.invalidateProgram()
	}
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
