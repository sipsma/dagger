package wcanalyze

import (
	"strings"
	"testing"

	"github.com/dagger/dagger/engine/wcprof"
)

// Chunk-4 rows W13 (arg-level change attribution) and W16c (module-blind
// per-node refusal). Expectations reason-derived before running.

func withSelf(t *testing.T, s *fixtureStrings, ev wcprof.DumpEvent, cs *wcprof.CallSelf) wcprof.DumpEvent {
	t.Helper()
	enc := wcprof.EncodeCallSelf(cs)
	if enc == "" {
		t.Fatal("EncodeCallSelf failed")
	}
	ev.SelfID = s.id(enc)
	return ev
}

// diffCallSelf's component matrix: every divergence class names itself
// deterministically; identical structures produce no lines.
func TestDiffCallSelfMatrix(t *testing.T) {
	base := func() *wcprof.CallSelf {
		return &wcprof.CallSelf{
			Field: "build", Receiver: "xxh3:recv", View: "v1", Nth: 0,
			Module:   &wcprof.CallModule{CallDigest: "xxh3:mod", Name: "m", Ref: "github.com/x/m", Pin: "abc"},
			Args:     []wcprof.CallArg{{Name: "platform", Value: `"linux/amd64"`}, {Name: "cache", Value: "true"}},
			Implicit: []wcprof.CallArg{{Name: "cachePerSession", Value: `"sess-a"`}},
		}
	}
	if diffs := diffCallSelf(base(), base()); len(diffs) != 0 {
		t.Fatalf("identical structures must produce no diffs, got %v", diffs)
	}

	b := base()
	b.Args[0].Value = `"linux/arm64"`
	diffs := diffCallSelf(base(), b)
	if len(diffs) != 1 || !strings.Contains(diffs[0], `arg "platform" differed: "linux/amd64" -> "linux/arm64"`) {
		t.Fatalf("scalar arg change must be named exactly, got %v", diffs)
	}

	b = base()
	b.Nth = 2
	b.View = "v2"
	b.Module.Pin = "def"
	b.Args = append(b.Args, wcprof.CallArg{Name: "extra", Value: "1"})
	b.Implicit[0].Value = `"sess-b"`
	diffs = diffCallSelf(base(), b)
	joined := strings.Join(diffs, "\n")
	for _, want := range []string{
		"nth differed: 0 -> 2",
		`view differed: "v1" -> "v2"`,
		"providing module differed",
		`arg "extra" added`,
		`implicit (scope) input "cachePerSession" differed`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, diffs)
		}
	}

	b = base()
	b.Args = b.Args[:1]
	diffs = diffCallSelf(base(), b)
	if len(diffs) != 1 || !strings.Contains(diffs[0], `arg "cache" removed`) {
		t.Fatalf("removed arg must be named, got %v", diffs)
	}
}

// --- W13: on an E3a-ordered OTel pair whose deepest changed node has
// identical input vectors, the E3b structures name the concrete self change
// ("scalar arg ... differed") — never guessed; and when the recorded
// renderings are identical, that is stated instead.
func TestWhyMissW13ArgLevelAttribution(t *testing.T) {
	build := func(argVal string) (*Graph, *Graph) {
		mkSelf := func(v string) *wcprof.CallSelf {
			return &wcprof.CallSelf{
				Field: "dep",
				Args:  []wcprof.CallArg{{Name: "platform", Value: v}},
			}
		}
		sA := newFixtureStrings()
		gA := buildWhyGraph(t, sA, []wcprof.DumpEvent{
			opEvent(sA, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
			opEvent(sA, 2, 1, "call", "S.stable", "d-s", "executed", 0, 50*ms),
			withSelf(t, sA, withInputs(t, sA, opEvent(sA, 3, 1, "call", "C.dep", "d-cA", "executed", 50*ms, 150*ms), []string{"d-s"}), mkSelf(`"linux/amd64"`)),
			withInputs(t, sA, opEvent(sA, 4, 1, "call", "P.build", "p-A", "executed", 150*ms, 700*ms), []string{"d-cA", "d-s"}),
		})
		sB := newFixtureStrings()
		gB := buildWhyGraph(t, sB, []wcprof.DumpEvent{
			opEvent(sB, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
			opEvent(sB, 2, 1, "call", "S.stable", "d-s", "hit", 0, 10*ms),
			withSelf(t, sB, withInputs(t, sB, opEvent(sB, 3, 1, "call", "C.dep", "d-cB", "executed", 10*ms, 110*ms), []string{"d-s"}), mkSelf(argVal)),
			withInputs(t, sB, opEvent(sB, 4, 1, "call", "P.build", "p-B", "executed", 110*ms, 700*ms), []string{"d-cB", "d-s"}),
			opEvent(sB, 5, 4, "call_exec", "P.build", "p-B", "ok", 110*ms, 700*ms),
			waitEvent(sB, 4, 5, "", "call_exec", 110*ms, 700*ms),
		})
		// Both sides are OTel-sourced with E3a-ordered vectors.
		gA.ResultIDsCaptureLocal = true
		gB.ResultIDsCaptureLocal = true
		markOrderedInputs(gA)
		markOrderedInputs(gB)
		return gB, gA
	}

	gB, gA := build(`"linux/arm64"`)
	rep, err := RunWhyUncachedPair(gB, gA, "p-B")
	if err != nil {
		t.Fatal(err)
	}
	o := originByDigest(t, rep, "d-cB")
	if o.Category != CategoryInputChanged {
		t.Fatalf("category %v, want input-changed (3)", o.Category)
	}
	if !strings.Contains(o.Answer, `arg "platform" differed: "linux/amd64" -> "linux/arm64"`) {
		t.Fatalf("the answer must name the scalar arg from the recorded structures: %q", o.Answer)
	}

	// Identical renderings (the same arg value on both sides — the digest
	// difference lies below the recorded granularity): stated, not guessed.
	gB, gA = build(`"linux/amd64"`)
	rep, err = RunWhyUncachedPair(gB, gA, "p-B")
	if err != nil {
		t.Fatal(err)
	}
	o = originByDigest(t, rep, "d-cB")
	if !strings.Contains(o.Answer, "render IDENTICALLY at the recorded granularity") {
		t.Fatalf("identical renderings must be stated as such: %q", o.Answer)
	}
}

// --- W16c: a module-caused miss. Native reaches the true frontier (the
// module edge is in the ordered vector); an OTel capture WITHOUT the E3a
// attr refuses descent at the module-bearing node (per-node refusal, the
// post-Chunk-4 upgrade of the global caveat); WITH the attr it reaches the
// same frontier as native.
func TestWhyMissW16cModuleBlindWalk(t *testing.T) {
	build := func(inputs []string, withModuleSelf bool) *Graph {
		s := newFixtureStrings()
		tgt := withInputs(t, s, opEvent(s, 4, 1, "call", "myMod.build", "d-x", "executed", 200*ms, 800*ms), inputs)
		if withModuleSelf {
			tgt = withSelf(t, s, tgt, &wcprof.CallSelf{
				Field:  "build",
				Module: &wcprof.CallModule{CallDigest: "d-mod", Name: "myMod", Ref: "github.com/x/mymod"},
			})
		}
		return buildWhyGraph(t, s, []wcprof.DumpEvent{
			opEvent(s, 1, 0, "session_phase", "session.query", "", "ok", 0, 900*ms),
			opEvent(s, 2, 1, "call", "S.stable", "d-s", "hit", 0, 10*ms),
			opEvent(s, 3, 1, "call", "ModuleSource.asModule", "d-mod", "executed", 10*ms, 200*ms),
			tgt,
			opEvent(s, 5, 4, "call_exec", "myMod.build", "d-x", "ok", 200*ms, 800*ms),
			waitEvent(s, 4, 5, "", "call_exec", 200*ms, 800*ms),
		})
	}

	// Native: the ordered vector carries the module edge → the walk reaches
	// the true frontier (d-mod, the module-caused origin).
	g := build([]string{"d-s", "d-mod"}, false)
	rep, err := RunWhyUncached(g, "d-x")
	if err != nil {
		t.Fatal(err)
	}
	originByDigest(t, rep, "d-mod")
	if rep.Collaterals != 1 {
		t.Fatalf("native walk: d-x is collateral above the module origin, got %d collaterals", rep.Collaterals)
	}

	// OTel WITHOUT E3a: dag.inputs is module-less AND unordered; the parsed
	// self structure names a providing module → descent REFUSED at d-x with
	// the stated note; d-mod is never falsely presented as reached.
	g = build([]string{"d-s"}, true)
	g.ResultIDsCaptureLocal = true
	rep, err = RunWhyUncached(g, "d-x")
	if err != nil {
		t.Fatal(err)
	}
	o := originByDigest(t, rep, "d-x")
	if !o.Node.ModuleBlindRefused {
		t.Fatal("module-bearing unordered OTel node must refuse descent")
	}
	refusalNote := false
	for _, n := range o.Notes {
		if strings.Contains(n, "descent REFUSED") && strings.Contains(n, "module") {
			refusalNote = true
		}
	}
	if !refusalNote {
		t.Fatalf("the refusal must be stated on the origin, got %v", o.Notes)
	}
	for _, oo := range rep.Origins {
		if oo.Node.Digest == "d-mod" {
			t.Fatal("the module origin must NOT be claimed reached through a module-less edge set")
		}
	}

	// OTel WITH E3a: the ordered vector carries the module edge → same
	// frontier as native.
	g = build([]string{"d-s", "d-mod"}, true)
	g.ResultIDsCaptureLocal = true
	markOrderedInputs(g)
	rep, err = RunWhyUncached(g, "d-x")
	if err != nil {
		t.Fatal(err)
	}
	originByDigest(t, rep, "d-mod")
}
