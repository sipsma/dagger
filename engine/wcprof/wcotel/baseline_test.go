package wcotel

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// TestBaselineFixture is the Chunk 1 definition-of-done check (design §6.1,
// impl-plan Chunk 1): a captured un-augmented, simple, no-service otlpdump
// trace compiles with no error and renders a report, and the structural gate
// passes on it — even though the shape is deliberately wrong (no wait edges
// yet, lazy work folded into producer self-time, withExec mapped to a bare
// exec). The fixture was captured via the telemetry-capture workflow
// (`go run ./hack/otlpdump` + a `container | from alpine | with-exec | stdout`
// run with OTEL_EXPORTER_OTLP_* pointed at it).
func TestBaselineFixture(t *testing.T) {
	const fixture = "testdata/baseline-simple-noservice.jsonl"
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	c, g, err := Load(f)
	if err != nil {
		t.Fatalf("loader must compile the baseline with no error: %v", err)
	}
	if len(g.Ops) == 0 || len(g.Roots) == 0 {
		t.Fatalf("expected a non-empty graph: ops=%d roots=%d", len(g.Ops), len(g.Roots))
	}

	// Live-span dedup must have collapsed the start/end exports: far fewer ops
	// than raw span lines (the capture exports each span at least twice).
	if c.SpanCount >= rawSpanLines(t, fixture) {
		t.Fatalf("dedup did not collapse live exports: deduped=%d raw=%d", c.SpanCount, rawSpanLines(t, fixture))
	}

	// The structural gate must pass on the baseline (design §6.1 DoD).
	gate := CheckStructural(c, g, GateOptions{})
	if err := gate.Err(); err != nil {
		var sb strings.Builder
		gate.Write(&sb)
		t.Fatalf("structural gate must pass on the un-augmented baseline:\n%s\n%v", sb.String(), err)
	}
	if gate.Cycles != 0 {
		t.Fatalf("baseline must have no cycles, got %d", gate.Cycles)
	}

	// The un-augmented baseline emits no wait edges yet (Chunk 2+), so the
	// dropped-link gate signal — which is otlpdump-observable via the fields
	// Chunk 1 added — is necessarily clean.
	if gate.WaitEdges != 0 {
		t.Fatalf("un-augmented baseline should carry no wait edges, got %d", gate.WaitEdges)
	}
	if gate.TotalDroppedLinks != 0 || gate.WaitBearingDroppedLinks != 0 {
		t.Fatalf("baseline should have no dropped links: total=%d wait-bearing=%d",
			gate.TotalDroppedLinks, gate.WaitBearingDroppedLinks)
	}

	// Classification precedence on real spans: the withExec call span has no
	// call_exec child yet, so the structural fallback maps it to a bare exec,
	// while a plain DagQL call stays "call" (impl-plan Chunk 1 precedence).
	assertOpKind(t, g, "Container.withExec", wcprof.OpKindExec.String())
	assertOpKind(t, g, "Container.from", wcprof.OpKindCall.String())

	// The report renders without error and produces analysis output.
	var report bytes.Buffer
	if err := wcanalyze.WriteReport(&report, g, wcanalyze.ReportOptions{}); err != nil {
		t.Fatalf("WriteReport failed: %v", err)
	}
	if !strings.Contains(report.String(), "wcprof analysis") {
		t.Fatalf("report did not render the expected header; got:\n%s", report.String())
	}
}

func assertOpKind(t *testing.T, g *wcanalyze.Graph, class, wantKind string) {
	t.Helper()
	for _, op := range g.Ops {
		if op.Class == class {
			if op.Kind != wantKind {
				t.Fatalf("op %q: want kind %q, got %q", class, wantKind, op.Kind)
			}
			return
		}
	}
	t.Fatalf("no op found for class %q", class)
}

func rawSpanLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, `"kind":"span"`) {
			n++
		}
	}
	return n
}
