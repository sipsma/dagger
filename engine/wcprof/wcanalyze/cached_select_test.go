package wcanalyze

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dagger/dagger/engine/wcprof"
)

// Catalog row V17: selector resolution. Each selector resolves to the
// reasoned digest set; unknown digests and empty matches are loud errors,
// never silent no-ops.

// opEventArgv is opEvent plus the interned argv metadata (the exec-decomp
// wire form: a scalar JSON-array string in MetaID).
func opEventArgv(t *testing.T, s *fixtureStrings, id, parent uint64, kind, class, ident, outcome string, startNS, endNS int64, argv []string) wcprof.DumpEvent {
	t.Helper()
	raw, err := json.Marshal(argv)
	if err != nil {
		t.Fatal(err)
	}
	ev := opEvent(s, id, parent, kind, class, ident, outcome, startNS, endNS)
	ev.MetaID = s.id(string(raw))
	return ev
}

// selectionFixture: two executed withExec digests with real exec subtrees
// (exec.run carrying the owning call digest, an argv-bearing processRun
// phase), one hit-only digest, and an exec whose owner chain carries no call
// digest (unresolvable).
//
//	R [0,1000]
//	├── C1 call Container.withExec dW1 (executed) [0,400]
//	│    └── E1 call_exec dW1 → X1 exec exec.run ident dW1
//	│         └── P1 exec_phase argv "go build ./..." (ident = exec state id)
//	├── C2 call Container.withExec dW2 (executed) [400,700]
//	│    └── E2 call_exec dW2 → X2 exec exec.run ident dW2
//	│         └── P2 exec_phase argv "npm install"
//	├── C3 call Container.from dF (hit) [700,710]
//	└── C4 call Mod.fn dM (executed) [710,1000]
//	     └── E4 call_exec dM → X3 exec exec.run ident "state-xyz" (no call digest)
//	          └── P3 exec_phase argv "go test ./..."
func selectionFixture(t *testing.T) *Graph {
	t.Helper()
	s := newFixtureStrings()
	events := []wcprof.DumpEvent{
		opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 1000*ms),
		opEvent(s, 2, 1, "call", "Container.withExec", "dW1", "executed", 0, 400*ms),
		opEvent(s, 3, 2, "call_exec", "Container.withExec", "dW1", "ok", 0, 400*ms),
		waitEvent(s, 2, 3, "", "call_exec", 0, 400*ms),
		opEvent(s, 4, 3, "exec", "exec.run", "dW1", "ok", 50*ms, 390*ms),
		opEventArgv(t, s, 5, 4, "exec_phase", "exec.processRun", "exec-state-1", "ok", 60*ms, 380*ms, []string{"go", "build", "./..."}),
		opEvent(s, 6, 1, "call", "Container.withExec", "dW2", "executed", 400*ms, 700*ms),
		opEvent(s, 7, 6, "call_exec", "Container.withExec", "dW2", "ok", 400*ms, 700*ms),
		waitEvent(s, 6, 7, "", "call_exec", 400*ms, 700*ms),
		opEvent(s, 8, 7, "exec", "exec.run", "dW2", "ok", 410*ms, 690*ms),
		opEventArgv(t, s, 9, 8, "exec_phase", "exec.processRun", "exec-state-2", "ok", 420*ms, 680*ms, []string{"npm", "install"}),
		opEvent(s, 10, 1, "call", "Container.from", "dF", "hit", 700*ms, 710*ms),
		opEvent(s, 11, 1, "call", "Mod.fn", "dM", "executed", 710*ms, 1000*ms),
		opEvent(s, 12, 11, "call_exec", "Mod.fn", "dM", "ok", 710*ms, 1000*ms),
		waitEvent(s, 11, 12, "", "call_exec", 710*ms, 1000*ms),
		opEvent(s, 13, 12, "exec", "exec.run", "state-xyz", "ok", 720*ms, 900*ms),
		opEventArgv(t, s, 14, 13, "exec_phase", "exec.processRun", "exec-state-3", "ok", 730*ms, 890*ms, []string{"go", "test", "./..."}),
	}
	return buildGraph(t, s, events)
}

func hypIdents(hyp CachedHypothesis) []string {
	out := make([]string, 0, len(hyp.Idents))
	for d := range hyp.Idents {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

func TestCachedSelectorDigests(t *testing.T) {
	g := selectionFixture(t)

	hyp, notes, err := CachedSelection{Digests: []string{"dW1", "dM"}}.Resolve(g)
	if err != nil {
		t.Fatal(err)
	}
	if got := hypIdents(hyp); !slices.Equal(got, []string{"dM", "dW1"}) {
		t.Fatalf("digests = %v, want [dM dW1]", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "2 explicit digest(s)") {
		t.Fatalf("notes = %v", notes)
	}

	// Unknown digest: a loud error, never a silent no-op.
	if _, _, err := (CachedSelection{Digests: []string{"dW1", "nope"}}).Resolve(g); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown digest must error naming it, got %v", err)
	}
}

func TestCachedSelectorManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.txt")
	if err := os.WriteFile(path, []byte("dW1\n\n# candidate set\n  dW2  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	digests, err := ExpandCachedArgs([]string{"@" + path, "dM"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(digests, []string{"dW1", "dW2", "dM"}) {
		t.Fatalf("expanded = %v, want [dW1 dW2 dM]", digests)
	}
	if _, err := ExpandCachedArgs([]string{"@/no/such/file"}); err == nil {
		t.Fatal("missing manifest must error")
	}

	// A manifest with no digests would silently no-op the flag: loud error.
	empty := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(empty, []byte("# only comments\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExpandCachedArgs([]string{"@" + empty}); err == nil || !strings.Contains(err.Error(), "no digests") {
		t.Fatalf("empty manifest must error loudly, got %v", err)
	}
}

func TestCachedSelectorClass(t *testing.T) {
	g := selectionFixture(t)

	hyp, notes, err := CachedSelection{Classes: []string{"Container.withExec"}}.Resolve(g)
	if err != nil {
		t.Fatal(err)
	}
	if got := hypIdents(hyp); !slices.Equal(got, []string{"dW1", "dW2"}) {
		t.Fatalf("class digests = %v, want [dW1 dW2]", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "2 executed digest(s)") {
		t.Fatalf("notes = %v", notes)
	}

	// A class with only hits has no executed digests: loud error.
	if _, _, err := (CachedSelection{Classes: []string{"Container.from"}}).Resolve(g); err == nil {
		t.Fatal("hit-only class must error (no executed digests)")
	}
	if _, _, err := (CachedSelection{Classes: []string{"No.such"}}).Resolve(g); err == nil {
		t.Fatal("unknown class must error")
	}
}

func TestCachedSelectorExec(t *testing.T) {
	g := selectionFixture(t)

	// Boundary-aware prefix: "go build" matches "go build ./..." only.
	hyp, _, err := CachedSelection{ExecPatterns: []string{"go build"}}.Resolve(g)
	if err != nil {
		t.Fatal(err)
	}
	if got := hypIdents(hyp); !slices.Equal(got, []string{"dW1"}) {
		t.Fatalf("exec digests = %v, want [dW1]", got)
	}

	// contains: substring form.
	hyp, _, err = CachedSelection{ExecPatterns: []string{"contains:install"}}.Resolve(g)
	if err != nil {
		t.Fatal(err)
	}
	if got := hypIdents(hyp); !slices.Equal(got, []string{"dW2"}) {
		t.Fatalf("contains digests = %v, want [dW2]", got)
	}

	// "go" matches BOTH go execs; only the one whose exec.run carries a call
	// digest resolves. Partial resolution is an ERROR without the explicit
	// flag (doctrine audit finding 5: the hypothesis would silently cover
	// less than the pattern names) …
	if _, _, err := (CachedSelection{ExecPatterns: []string{"go"}}).Resolve(g); err == nil ||
		!strings.Contains(err.Error(), "allow-partial-selection") {
		t.Fatalf("partial resolution must error without the flag, got %v", err)
	}
	// … and with the flag it proceeds, stating the partial coverage loudly.
	hyp, notes, err := CachedSelection{ExecPatterns: []string{"go"}, AllowPartialSelection: true}.Resolve(g)
	if err != nil {
		t.Fatal(err)
	}
	if got := hypIdents(hyp); !slices.Equal(got, []string{"dW1"}) {
		t.Fatalf("partial digests = %v, want [dW1]", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "PARTIAL: 1 exec(s) unresolvable") {
		t.Fatalf("the partial coverage must be stated in the note, got %v", notes)
	}

	// Zero matches and zero-resolvable are loud errors.
	if _, _, err := (CachedSelection{ExecPatterns: []string{"cargo"}}).Resolve(g); err == nil {
		t.Fatal("pattern matching no exec must error")
	}
	if _, _, err := (CachedSelection{ExecPatterns: []string{"go test"}}).Resolve(g); err == nil {
		t.Fatal("pattern resolving to no owning digest must error")
	}
}
