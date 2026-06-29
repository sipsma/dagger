package wcanalyze

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/dagger/dagger/engine/wcprof"
)

// strTab is a tiny test-side string interner mirroring the dump's interned table
// (id 0 = empty), so fixtures can carry MetaID/Class/Ident exactly as a real dump.
type strTab struct {
	byVal map[string]uint32
	vals  []string
}

func newStrTab() *strTab { return &strTab{byVal: map[string]uint32{"": 0}, vals: []string{""}} }

func (s *strTab) id(v string) uint32 {
	if v == "" {
		return 0
	}
	if id, ok := s.byVal[v]; ok {
		return id
	}
	id := uint32(len(s.vals))
	s.vals = append(s.vals, v)
	s.byVal[v] = id
	return id
}

// execOp builds an op event, encoding argv exactly as the native recorder does (the
// canonical scalar JSON-array string interned as MetaID). ident is per-op unique.
func (s *strTab) execOp(id, parent uint64, kind, work, class string, argv []string, start, end int64) wcprof.DumpEvent {
	var meta uint32
	if len(argv) > 0 {
		b, _ := json.Marshal(argv)
		meta = s.id(string(b))
	}
	return wcprof.DumpEvent{
		Type: "op", OpKind: kind, WorkType: work, Outcome: "ok",
		OpID: id, ParentID: parent,
		ClassID: s.id(class), IdentID: s.id(fmt.Sprintf("ident-%d", id)), MetaID: meta,
		StartNS: start, EndNS: end,
	}
}

// workloadGraph builds the §6 known-answer workload: a session that runs a slow
// `go build` (140ns user self), a `git clone` (120ns), and a repeated `echo hi`
// (2x2ns) — each as a processRun under its own exec.run, all under one session so
// the makespan is their sequential sum.
func workloadGraph(t *testing.T) *Graph {
	t.Helper()
	st := newStrTab()
	const (
		eng  = "engine"
		usr  = "user"
		exec = "exec"
		ph   = "exec_phase"
		sess = "session_phase"
	)
	events := []wcprof.DumpEvent{
		st.execOp(1, 0, sess, eng, "session.query", nil, 0, 264),
		// go build: exec.run [0,140] wrapping processRun [0,140], 140ns user self.
		st.execOp(2, 1, exec, eng, "exec.run", nil, 0, 140),
		st.execOp(3, 2, ph, usr, "exec.processRun", []string{"go", "build", "./..."}, 0, 140),
		// git clone: [140,260], 120ns user self.
		st.execOp(4, 1, exec, eng, "exec.run", nil, 140, 260),
		st.execOp(5, 4, ph, usr, "exec.processRun", []string{"git", "clone", "https://example.com/r"}, 140, 260),
		// echo hi x2 (drill-down / repeat): tiny self, distinct invocations.
		st.execOp(6, 1, exec, eng, "exec.run", nil, 260, 262),
		st.execOp(7, 6, ph, usr, "exec.processRun", []string{"echo", "hi"}, 260, 262),
		st.execOp(8, 1, exec, eng, "exec.run", nil, 262, 264),
		st.execOp(9, 8, ph, usr, "exec.processRun", []string{"echo", "hi"}, 262, 264),
	}
	header := &wcprof.DumpHeader{
		SchemaVersion: wcprof.DumpSchemaVersion,
		EpochUnixNano: 0, DumpedUnixNano: 264, EventCount: len(events), Strings: st.vals,
	}
	g, err := Build(header, events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return g
}

func classSet(g *Graph) map[string]bool {
	out := map[string]bool{}
	for _, st := range AggregateClasses(g) {
		out[st.Key.String()] = true
	}
	return out
}

// TestDefaultExecClass is the per-command projection unit (design §4.3): a pure
// function of explicit argv, never a name/shell parse.
func TestDefaultExecClass(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"go", "build", "./..."}, "go build"},
		{[]string{"git", "clone", "url"}, "git clone"},
		{[]string{"npm", "install"}, "npm install"},
		{[]string{"/usr/local/go/bin/go", "test"}, "go test"}, // basename of a path arg[0]
		{[]string{"sh", "-c", "go build"}, "sh"},              // -c is a flag → no subcommand
		{[]string{"pytest"}, "pytest"},                        // single token
		{[]string{"ls", "-la"}, "ls"},                         // flag arg[1] dropped
	}
	for _, c := range cases {
		if got := defaultExecClass(c.argv); got != c.want {
			t.Errorf("defaultExecClass(%v) = %q, want %q", c.argv, got, c.want)
		}
	}
}

// TestClassifyExecsDecomposesBlob is the §6 native analyze half: Build reconstructs
// Op.Argv exactly, ClassifyExecs replaces the single exec.processRun blob with
// per-command classes, the slow `go build` ranks #1 by what-if saving, and no class
// is headlined by an engine shim.
func TestClassifyExecsDecomposesBlob(t *testing.T) {
	g := workloadGraph(t)

	// Argv round-trips through the dump exactly (the MetaID seam).
	var goBuild *Op
	for _, op := range g.Ops {
		if op.ID == 3 {
			goBuild = op
		}
	}
	if goBuild == nil || !slices.Equal(goBuild.Argv, []string{"go", "build", "./..."}) {
		t.Fatalf("Op.Argv did not round-trip: %+v", goBuild)
	}

	ClassifyExecs(g, nil)

	classes := classSet(g)
	for _, want := range []string{"exec_phase:go build", "exec_phase:git clone", "exec_phase:echo hi"} {
		if !classes[want] {
			t.Errorf("missing per-command class %q; have %v", want, classes)
		}
	}
	if classes["exec_phase:exec.processRun"] {
		t.Error("the exec.processRun blob must be gone after decomposition")
	}
	// Regression guard (B1+B4): a per-command class is never an engine shim — the
	// argv[0] must be the user's program, not /.init or the QEMU emulator.
	for c := range classes {
		if c == "exec_phase:.init" || c == "exec_phase:dagger_qemu_emulator" {
			t.Errorf("a per-command class is an engine shim: %q (capture sits below a shim)", c)
		}
	}

	// `go build` ranks #1 by what-if saving (it has the most scalable user self-time
	// on the critical path).
	_, results, _, err := RunWhatIfs(g, []float64{0}, 0)
	if err != nil {
		t.Fatalf("what-ifs: %v", err)
	}
	slices.SortFunc(results, func(a, b WhatIfResult) int { return int(b.SavedNS[0] - a.SavedNS[0]) })
	if len(results) == 0 || results[0].Key != (ClassKey{Kind: "exec_phase", Class: "go build"}) {
		t.Fatalf("go build must rank #1 by what-if saving; got %+v", results)
	}
	if results[0].SavedNS[0] <= 0 {
		t.Fatalf("go build what-if saving must be > 0, got %d", results[0].SavedNS[0])
	}
}

// TestClassifyExecsMemoReset is the order regression (design §4.4 / B2): compiling
// the replay program BEFORE classifying (as the gate does) must not freeze the
// what-if savings on the stale blob — ClassifyExecs invalidates the memo, so the
// per-command class still gets a non-zero saving.
func TestClassifyExecsMemoReset(t *testing.T) {
	g := workloadGraph(t)

	// Force the program to compile on the pre-classify BLOB classes (what the OTel
	// gate's NewSimulation does).
	if _, err := NewSimulation(g, nil).Run(); err != nil {
		t.Fatalf("baseline sim: %v", err)
	}

	// Now classify; this must reset the memo so the next simulation re-buckets.
	ClassifyExecs(g, nil)

	_, results, _, err := RunWhatIfs(g, []float64{0}, 0)
	if err != nil {
		t.Fatalf("what-ifs: %v", err)
	}
	var saving int64 = -1
	for _, r := range results {
		if r.Key == (ClassKey{Kind: "exec_phase", Class: "go build"}) {
			saving = r.SavedNS[0]
		}
	}
	if saving <= 0 {
		t.Fatalf("after a pre-classify program compile, go build must still save > 0 (memo reset); got %d", saving)
	}
}

// TestClassifyExecsIdempotentAndArgvlessUntouched: re-running the classifier is
// stable (keyed off the immutable argv), and an exec with no argv stays the blob
// (the len(Argv)>0 ⇒ exec relation is one-directional, design §4.1d).
func TestClassifyExecsIdempotentAndArgvlessUntouched(t *testing.T) {
	st := newStrTab()
	events := []wcprof.DumpEvent{
		st.execOp(1, 0, "session_phase", "engine", "session.query", nil, 0, 100),
		st.execOp(2, 1, "exec_phase", "user", "exec.processRun", []string{"go", "build"}, 0, 60),
		// argv-less exec (image default CMD / no resolved command): stays the blob.
		st.execOp(3, 1, "exec_phase", "user", "exec.processRun", nil, 60, 100),
	}
	header := &wcprof.DumpHeader{SchemaVersion: wcprof.DumpSchemaVersion, DumpedUnixNano: 100, EventCount: len(events), Strings: st.vals}
	g, err := Build(header, events)
	if err != nil {
		t.Fatal(err)
	}

	ClassifyExecs(g, nil)
	ClassifyExecs(g, nil) // idempotent: a second pass must not change anything

	classes := classSet(g)
	if !classes["exec_phase:go build"] {
		t.Errorf("argv-bearing exec must classify as go build; have %v", classes)
	}
	if !classes["exec_phase:exec.processRun"] {
		t.Errorf("argv-less exec must remain the exec.processRun blob; have %v", classes)
	}
}

// TestExecGroupRuleMatching: the boundary-aware literal prefix (default) matches a
// command and its space-continued extensions but respects the word boundary;
// contains: switches to substring matching (the sh -c case) (design §4.6).
func TestExecGroupRuleMatching(t *testing.T) {
	prefix := ExecGroupRule{Match: "go build", Label: "builds"}
	if !prefix.matches("go build") || !prefix.matches("go build ./...") {
		t.Error("prefix must match the exact command and a space-continued one")
	}
	if prefix.matches("go buildx thing") {
		t.Error("boundary guard: 'go build' must NOT match 'go buildx ...'")
	}
	if prefix.matches("go buil") {
		t.Error("a partial prefix must not match")
	}

	sub := ExecGroupRule{Match: "go build", Label: "builds", Contains: true}
	if !sub.matches("sh -c cd x && go build ./...") {
		t.Error("contains: must match a shell-wrapped command")
	}
	if sub.matches("sh -c echo hi") {
		t.Error("contains: must not match when the substring is absent")
	}
}

// TestParseExecGroupRule: the <match>=<label> grammar, the contains: modifier, the
// split-on-first-= rule, and the rejected empty forms (design §4.6, §9).
func TestParseExecGroupRule(t *testing.T) {
	if r, err := ParseExecGroupRule("go build=builds"); err != nil || r != (ExecGroupRule{Match: "go build", Label: "builds"}) {
		t.Fatalf("prefix rule parse: %+v err=%v", r, err)
	}
	if r, err := ParseExecGroupRule("contains:go build=builds"); err != nil || r != (ExecGroupRule{Match: "go build", Label: "builds", Contains: true}) {
		t.Fatalf("contains rule parse: %+v err=%v", r, err)
	}
	// label is everything after the FIRST '=' (a label may itself contain '=').
	if r, err := ParseExecGroupRule("go test=unit=tests"); err != nil || r.Match != "go test" || r.Label != "unit=tests" {
		t.Fatalf("first-= split: %+v err=%v", r, err)
	}
	for _, bad := range []string{"noequals", "=label", "match=", "contains:=label"} {
		if _, err := ParseExecGroupRule(bad); err == nil {
			t.Errorf("malformed spec %q must error", bad)
		}
	}
}

// TestReGroupWithoutReEmit (§6 test 5): one captured graph, analyzed twice — the
// default keeps commands separate, then a rule collapses them — with NO re-capture,
// because the raw argv lives in the IR and grouping is purely offline (design §4.6).
func TestReGroupWithoutReEmit(t *testing.T) {
	g := workloadGraph(t)

	ClassifyExecs(g, nil)
	c0 := classSet(g)
	if !c0["exec_phase:go build"] || !c0["exec_phase:git clone"] {
		t.Fatalf("default grouping must keep commands separate; have %v", c0)
	}

	// Re-group the SAME graph with different rules — no re-emit.
	rules := []ExecGroupRule{{Match: "go build", Label: "builds"}, {Match: "git clone", Label: "builds"}}
	ClassifyExecs(g, rules)
	c1 := classSet(g)
	if !c1["exec_phase:builds"] {
		t.Errorf("re-grouping must collapse the two commands into 'builds'; have %v", c1)
	}
	if c1["exec_phase:go build"] || c1["exec_phase:git clone"] {
		t.Errorf("re-grouped commands must no longer rank separately; have %v", c1)
	}
}
