package wcotel

import (
	"strconv"
	"strings"
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

func mustLoad(t *testing.T, jsonl string) (*Compiled, *wcanalyze.Graph) {
	t.Helper()
	c, g, err := Load(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c, g
}

// waitLink builds a purpose=wait link record over [start,end] absolute nanos.
func waitLink(target string, reason string, start, end int) map[string]any {
	return map[string]any{
		"spanId": target,
		"attrs": map[string]any{
			telemetry.LinkPurposeAttr:                  telemetryattrs.LinkPurposeWait,
			telemetryattrs.WcprofWaitReasonAttr:        reason,
			telemetryattrs.WcprofWaitStartUnixNanoAttr: strconv.Itoa(start),
			telemetryattrs.WcprofWaitEndUnixNanoAttr:   strconv.Itoa(end),
		},
	}
}

// TestGatePassesCleanGraph: a plain root+child graph violates nothing.
func TestGatePassesCleanGraph(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "POST /query", "startNs": baseEp, "endNs": baseEnd}),
		rec(map[string]any{"spanId": idA, "parentId": idRoot, "name": "Container.from", "startNs": baseEp + 100, "endNs": baseEp + 200,
			"attrs": map[string]any{telemetry.DagDigestAttr: "sha256:a"}}),
	)
	c, g := mustLoad(t, jsonl)
	gate := CheckStructural(c, g, GateOptions{})
	if err := gate.Err(); err != nil {
		t.Fatalf("clean graph should pass: %v", err)
	}
	if gate.Cycles != 0 || len(gate.SelfGtMakespan) != 0 || len(gate.IntervalGtSpan) != 0 {
		t.Fatalf("unexpected violations: %+v", gate)
	}
}

// TestGateFailsOnCycle: two roots that mutually wait-join each other form the
// self-wait cycle the design says never happens in a faithful run (design §2.5).
func TestGateFailsOnCycle(t *testing.T) {
	const end = baseEp + 1000
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idA, "parentId": idNone, "name": "A", "startNs": baseEp, "endNs": end,
			"links": []any{waitLink(idB, "call_exec", baseEp, end)}}),
		rec(map[string]any{"spanId": idB, "parentId": idNone, "name": "B", "startNs": baseEp, "endNs": end,
			"links": []any{waitLink(idA, "call_exec", baseEp, end)}}),
	)
	c, g := mustLoad(t, jsonl)
	gate := CheckStructural(c, g, GateOptions{})
	if gate.Cycles == 0 {
		t.Fatal("expected a cycle to be detected")
	}
	if gate.Err() == nil {
		t.Fatal("a cycle must fail the gate")
	}
}

// TestGateFailsOnDroppedWaitLinks: a wait-bearing span whose links were evicted
// (DroppedLinksCount>0) fails the gate — silently dropped waits under-serialize.
func TestGateFailsOnDroppedWaitLinks(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idExec, "parentId": idNone, "name": "Container.withExec", "startNs": baseEp, "endNs": baseEnd,
			"attrs": map[string]any{telemetryattrs.WcprofOpKindAttr: "call_exec"}}),
		rec(map[string]any{"spanId": idA, "parentId": idNone, "name": "Container.stdout", "startNs": baseEp, "endNs": baseEnd,
			"droppedLinks": 4,
			"links":        []any{waitLink(idExec, "singleflight", baseEp, baseEnd)}}),
	)
	c, g := mustLoad(t, jsonl)
	if c.WaitBearingDroppedLinks != 4 {
		t.Fatalf("want 4 wait-bearing dropped links, got %d", c.WaitBearingDroppedLinks)
	}
	gate := CheckStructural(c, g, GateOptions{})
	if gate.Err() == nil {
		t.Fatal("dropped wait links must fail the gate")
	}
}

// TestGateFallbackAnchorsReportOnlyAndThreshold: a root that wait-joins two
// other roots before they replay produces fallback anchors. They are
// report-only by default (design §6.1), but a bound makes them fail.
func TestGateFallbackAnchorsReportOnlyAndThreshold(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idA, "parentId": idNone, "name": "A", "startNs": baseEp, "endNs": baseEp + 300,
			"links": []any{
				waitLink(idB, "call_exec", baseEp, baseEp+200),
				waitLink(idExec, "call_exec", baseEp, baseEp+250),
			}}),
		rec(map[string]any{"spanId": idB, "parentId": idNone, "name": "B", "startNs": baseEp + 50, "endNs": baseEp + 200}),
		rec(map[string]any{"spanId": idExec, "parentId": idNone, "name": "C", "startNs": baseEp + 60, "endNs": baseEp + 250}),
	)
	c, g := mustLoad(t, jsonl)

	reportOnly := CheckStructural(c, g, GateOptions{})
	if reportOnly.FallbackAnchors == 0 {
		t.Fatal("expected fallback anchors from cross-root wait-joins")
	}
	if err := reportOnly.Err(); err != nil {
		t.Fatalf("fallback anchors must be report-only by default: %v", err)
	}

	bounded := CheckStructural(c, g, GateOptions{MaxFallbackAnchors: reportOnly.FallbackAnchors - 1})
	if bounded.Err() == nil {
		t.Fatal("a fallback-anchor bound below the count must fail")
	}
}
