package wcotel

import (
	"encoding/json"
	"strconv"
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

func argvJSON(argv []string) string {
	b, err := json.Marshal(argv)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func graphClasses(g *wcanalyze.Graph) map[string]bool {
	m := map[string]bool{}
	for _, op := range g.Ops {
		m[op.Key().String()] = true
	}
	return m
}

// otelExecWorkloadJSONL is the §6 known-answer workload as an augmented OTel trace:
// a session that runs a slow `go build` (140ns user) then a `git clone` (120ns),
// each a processRun under its own exec.run, all sharing one session root.
func otelExecWorkloadJSONL(t *testing.T) string {
	t.Helper()
	return toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "session.query", "startNs": baseEp, "endNs": baseEp + 260}),
		rec(map[string]any{
			"spanId": idA, "parentId": idRoot, "name": "exec.run", "startNs": baseEp, "endNs": baseEp + 140,
			"attrs": map[string]any{telemetryattrs.WcprofOpKindAttr: "exec", telemetry.DagDigestAttr: "exec-go"},
		}),
		rec(map[string]any{
			"spanId": idB, "parentId": idA, "name": "exec.processRun", "startNs": baseEp, "endNs": baseEp + 140,
			"attrs": map[string]any{
				telemetryattrs.WcprofOpKindAttr:   "exec_phase",
				telemetryattrs.WcprofWorkTypeAttr: "user",
				telemetryattrs.WcprofExecArgvAttr: argvJSON([]string{"go", "build", "./..."}),
			},
		}),
		rec(map[string]any{
			"spanId": idExec, "parentId": idRoot, "name": "exec.run", "startNs": baseEp + 140, "endNs": baseEp + 260,
			"attrs": map[string]any{telemetryattrs.WcprofOpKindAttr: "exec", telemetry.DagDigestAttr: "exec-git"},
		}),
		rec(map[string]any{
			"spanId": idLazy, "parentId": idExec, "name": "exec.processRun", "startNs": baseEp + 140, "endNs": baseEp + 260,
			"attrs": map[string]any{
				telemetryattrs.WcprofOpKindAttr:   "exec_phase",
				telemetryattrs.WcprofWorkTypeAttr: "user",
				telemetryattrs.WcprofExecArgvAttr: argvJSON([]string{"git", "clone", "https://example.com/r"}),
			},
		}),
	)
}

// nativeExecWorkloadGraph mirrors otelExecWorkloadJSONL as a native dump (epoch 0,
// the same offsets the loader rebases the OTel trace to), so the two graphs are
// structurally identical and the cross-source oracle can demand exact agreement.
func nativeExecWorkloadGraph(t *testing.T) *wcanalyze.Graph {
	t.Helper()
	vals := []string{""}
	byVal := map[string]uint32{"": 0}
	id := func(v string) uint32 {
		if v == "" {
			return 0
		}
		if x, ok := byVal[v]; ok {
			return x
		}
		x := uint32(len(vals))
		vals = append(vals, v)
		byVal[v] = x
		return x
	}
	op := func(opID, parent uint64, kind, work, class string, argv []string, start, end int64) wcprof.DumpEvent {
		var meta uint32
		if len(argv) > 0 {
			meta = id(argvJSON(argv))
		}
		return wcprof.DumpEvent{
			Type: "op", OpKind: kind, WorkType: work, Outcome: "ok",
			OpID: opID, ParentID: parent, ClassID: id(class), IdentID: id("ident-" + class + "-" + strconv.FormatUint(opID, 10)), MetaID: meta,
			StartNS: start, EndNS: end,
		}
	}
	events := []wcprof.DumpEvent{
		op(1, 0, "session_phase", "engine", "session.query", nil, 0, 260),
		op(2, 1, "exec", "engine", "exec.run", nil, 0, 140),
		op(3, 2, "exec_phase", "user", "exec.processRun", []string{"go", "build", "./..."}, 0, 140),
		op(4, 1, "exec", "engine", "exec.run", nil, 140, 260),
		op(5, 4, "exec_phase", "user", "exec.processRun", []string{"git", "clone", "https://example.com/r"}, 140, 260),
	}
	header := &wcprof.DumpHeader{SchemaVersion: wcprof.DumpSchemaVersion, DumpedUnixNano: 260, EventCount: len(events), Strings: vals}
	g, err := wcanalyze.Build(header, events)
	if err != nil {
		t.Fatalf("build native: %v", err)
	}
	return g
}

// TestLoaderArgvToClasses (§6 test 3): the loader reconstructs Op.Argv from the
// scalar JSON-array attr, the structural gate still passes, and ClassifyExecs yields
// the per-command classes with the blob gone.
func TestLoaderArgvToClasses(t *testing.T) {
	c := mustCompile(t, otelExecWorkloadJSONL(t))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckStructural(c, g, GateOptions{}).Err(); err != nil {
		t.Fatalf("structural gate must pass on the augmented argv trace: %v", err)
	}
	wcanalyze.ClassifyExecs(g, nil)
	classes := graphClasses(g)
	for _, want := range []string{"exec_phase:go build", "exec_phase:git clone"} {
		if !classes[want] {
			t.Errorf("missing per-command class %q; have %v", want, classes)
		}
	}
	if classes["exec_phase:exec.processRun"] {
		t.Error("the exec.processRun blob must be gone on the OTel source too")
	}
}

// TestCrossSourceOracleSameGroups (§6 test 4): native and OTel sources of the SAME
// workload, classified with the SAME rules, produce the SAME per-command groups and
// identical savings — the strongest faithfulness check (design §1.5, §6.2).
func TestCrossSourceOracleSameGroups(t *testing.T) {
	nativeG := nativeExecWorkloadGraph(t)
	c := mustCompile(t, otelExecWorkloadJSONL(t))
	otelG, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}

	wcanalyze.ClassifyExecs(nativeG, nil)
	wcanalyze.ClassifyExecs(otelG, nil)

	// Same per-command classes on both sources.
	nc, oc := graphClasses(nativeG), graphClasses(otelG)
	for _, want := range []string{"exec_phase:go build", "exec_phase:git clone"} {
		if !nc[want] || !oc[want] {
			t.Fatalf("class %q must appear on BOTH sources (native=%v otel=%v)", want, nc[want], oc[want])
		}
	}

	cmp, err := Oracle(nativeG, otelG, 0, 15, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Agrees(1.0, 0.0) {
		t.Fatalf("sources must agree exactly: jaccard=%.2f max-rel-drift=%.4f\n%+v", cmp.JaccardTopN(), cmp.MaxRelDrift(), cmp)
	}
	// Sanity: the agreement is over the real per-command bottlenecks, not an empty set.
	if len(cmp.Shared) < 2 {
		t.Fatalf("expected >=2 shared per-command bottleneck classes, got %d", len(cmp.Shared))
	}
}

// TestGateThenReportOrderFullPath (§6 test 2): drive ClassifyExecs → CheckStructural
// → RunWhatIfs in the real CLI order and confirm a per-command class gets a NON-ZERO
// what-if saving — i.e. the savings are computed on the relabeled classes, not the
// frozen blob (the B2 fix on the actual gate path).
func TestGateThenReportOrderFullPath(t *testing.T) {
	c := mustCompile(t, otelExecWorkloadJSONL(t))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}

	// Order matches cmd/wcprof-otel-analyze: classify, THEN gate (which compiles the
	// replay program), THEN report's what-ifs.
	wcanalyze.ClassifyExecs(g, nil)
	if err := CheckStructural(c, g, GateOptions{}).Err(); err != nil {
		t.Fatalf("gate: %v", err)
	}
	_, results, _, err := wcanalyze.RunWhatIfs(g, []float64{0}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var saving int64 = -1
	for _, r := range results {
		if r.Key == (wcanalyze.ClassKey{Kind: "exec_phase", Class: "go build"}) {
			saving = r.SavedNS[0]
		}
	}
	if saving <= 0 {
		t.Fatalf("go build must have a non-zero what-if saving after the gate compiled the program; got %d", saving)
	}
}

// TestArgvAttrSurvivesCloudJSONDecode (§6 test 6): the scalar JSON-array argv string
// survives a Cloud-shaped map[string]any JSON round-trip BIT-EXACT (unlike an OTLP
// array, untested through Cloud), and json.Unmarshal recovers the exact argv —
// gating the §4.1c encoding choice.
func TestArgvAttrSurvivesCloudJSONDecode(t *testing.T) {
	argv := []string{"go", "build", "-ldflags=-X main.v=1.2", "./path/to/…/pkg", `arg"with"quotes`}
	wire := argvJSON(argv)

	// As Cloud does: store the attrs as JSON, return them decoded as map[string]any.
	stored, err := json.Marshal(map[string]any{telemetryattrs.WcprofExecArgvAttr: wire})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(stored, &decoded); err != nil {
		t.Fatal(err)
	}

	// The loader reads it back as a scalar string (attrStr) — bit-exact.
	got := attrStr(decoded, telemetryattrs.WcprofExecArgvAttr)
	if got != wire {
		t.Fatalf("argv attr not bit-exact through Cloud JSON decode:\n got %q\nwant %q", got, wire)
	}
	var roundtripped []string
	if err := json.Unmarshal([]byte(got), &roundtripped); err != nil {
		t.Fatalf("recovered argv string did not unmarshal: %v", err)
	}
	if argvJSON(roundtripped) != wire {
		t.Fatalf("recovered argv != original: %v vs %v", roundtripped, argv)
	}
}

// TestCoverageBoundaryNoArgvStaysBlob (§6 test 8): an exec that carries no argv (a
// path that bypasses the core capture: Dockerfile RUN, a service start, an internal
// exec) stays the aggregated exec.processRun blob — consistent across sources and
// never a shim mislabel.
func TestCoverageBoundaryNoArgvStaysBlob(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "session.query", "startNs": baseEp, "endNs": baseEp + 100}),
		rec(map[string]any{
			"spanId": idA, "parentId": idRoot, "name": "exec.run", "startNs": baseEp, "endNs": baseEp + 100,
			"attrs": map[string]any{telemetryattrs.WcprofOpKindAttr: "exec", telemetry.DagDigestAttr: "exec-noargv"},
		}),
		// processRun WITHOUT the argv attr (e.g. a non-withExec exec path).
		rec(map[string]any{
			"spanId": idB, "parentId": idA, "name": "exec.processRun", "startNs": baseEp, "endNs": baseEp + 100,
			"attrs": map[string]any{
				telemetryattrs.WcprofOpKindAttr:   "exec_phase",
				telemetryattrs.WcprofWorkTypeAttr: "user",
			},
		}),
	)
	c := mustCompile(t, jsonl)
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	wcanalyze.ClassifyExecs(g, nil)
	if !graphClasses(g)["exec_phase:exec.processRun"] {
		t.Error("an argv-less exec must remain the exec.processRun blob (coverage boundary)")
	}
}

// TestReGroupCrossSource (§6 test 5, cross-source): the SAME --exec-group rule
// re-groups BOTH sources identically (collapsing two commands into one class) and
// the sources still agree exactly under the rule.
func TestReGroupCrossSource(t *testing.T) {
	nativeG := nativeExecWorkloadGraph(t)
	c := mustCompile(t, otelExecWorkloadJSONL(t))
	otelG, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}

	rules, err := wcanalyze.ParseExecGroupRules([]string{"go build=builds", "git clone=builds"})
	if err != nil {
		t.Fatal(err)
	}
	wcanalyze.ClassifyExecs(nativeG, rules)
	wcanalyze.ClassifyExecs(otelG, rules)

	nc, oc := graphClasses(nativeG), graphClasses(otelG)
	if !nc["exec_phase:builds"] || !oc["exec_phase:builds"] {
		t.Fatalf("both sources must collapse into 'builds' (native=%v otel=%v)", nc, oc)
	}
	for _, gone := range []string{"exec_phase:go build", "exec_phase:git clone"} {
		if nc[gone] || oc[gone] {
			t.Errorf("re-grouped class %q must be gone on both sources", gone)
		}
	}

	cmp, err := Oracle(nativeG, otelG, 0, 15, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Agrees(1.0, 0.0) {
		t.Fatalf("sources must agree exactly under the same rule: jaccard=%.2f drift=%.4f", cmp.JaccardTopN(), cmp.MaxRelDrift())
	}
}
