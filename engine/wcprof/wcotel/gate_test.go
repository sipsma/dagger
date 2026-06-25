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

// waitLinkRaw builds a purpose=wait link with caller-supplied attrs (for
// malformed/edge cases). reason and any timing attrs are the caller's to set.
func waitLinkRaw(target string, attrs map[string]any) map[string]any {
	a := map[string]any{telemetry.LinkPurposeAttr: telemetryattrs.LinkPurposeWait}
	for k, v := range attrs {
		a[k] = v
	}
	return map[string]any{"spanId": target, "attrs": a}
}

// TestGateFailsOnUnresolvedWaitTarget: a non-lock wait whose target span is not
// in the trace is a wait-edge loss (Invariant T regression / truncation) and
// must fail loudly — replay would otherwise silently degrade it to a fixed
// delay (design §6.1).
func TestGateFailsOnUnresolvedWaitTarget(t *testing.T) {
	const missing = "9999999999999999"
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idA, "parentId": idNone, "name": "Container.stdout", "startNs": baseEp, "endNs": baseEnd,
			"attrs": map[string]any{telemetry.DagDigestAttr: "sha256:z"},
			"links": []any{waitLink(missing, "singleflight", baseEp, baseEnd)}}),
	)
	c, g := mustLoad(t, jsonl)
	if c.UnresolvedWaitTargets != 1 {
		t.Fatalf("want 1 unresolved wait target, got %d", c.UnresolvedWaitTargets)
	}
	if CheckStructural(c, g, GateOptions{}).Err() == nil {
		t.Fatal("an unresolved non-lock wait target must fail the gate")
	}
}

// TestGateFailsOnMalformedWaitTiming: missing/unparseable wcprof.wait.*_unix_ns
// is a malformed emit and must fail (design §6.1) — it would otherwise remove a
// real wait from replay while reporting PASS.
func TestGateFailsOnMalformedWaitTiming(t *testing.T) {
	cases := map[string]map[string]any{
		"missing start": {
			telemetryattrs.WcprofWaitReasonAttr:      "singleflight",
			telemetryattrs.WcprofWaitEndUnixNanoAttr: strconv.Itoa(baseEnd),
		},
		"missing end": {
			telemetryattrs.WcprofWaitReasonAttr:        "singleflight",
			telemetryattrs.WcprofWaitStartUnixNanoAttr: strconv.Itoa(baseEp),
		},
		"unparseable": {
			telemetryattrs.WcprofWaitReasonAttr:        "singleflight",
			telemetryattrs.WcprofWaitStartUnixNanoAttr: "not-a-number",
			telemetryattrs.WcprofWaitEndUnixNanoAttr:   strconv.Itoa(baseEnd),
		},
	}
	for name, attrs := range cases {
		t.Run(name, func(t *testing.T) {
			jsonl := toJSONL(t,
				// resolvable target, so the only fault is the timing
				rec(map[string]any{"spanId": idExec, "parentId": idNone, "name": "Container.withExec", "startNs": baseEp, "endNs": baseEnd,
					"attrs": map[string]any{telemetryattrs.WcprofOpKindAttr: "call_exec"}}),
				rec(map[string]any{"spanId": idA, "parentId": idNone, "name": "Container.stdout", "startNs": baseEp, "endNs": baseEnd,
					"links": []any{waitLinkRaw(idExec, attrs)}}),
			)
			c, g := mustLoad(t, jsonl)
			if c.MalformedWaitTimings != 1 {
				t.Fatalf("want 1 malformed wait timing, got %d", c.MalformedWaitTimings)
			}
			if c.UnresolvedWaitTargets != 0 {
				t.Fatalf("target resolves; want 0 unresolved, got %d", c.UnresolvedWaitTargets)
			}
			if CheckStructural(c, g, GateOptions{}).Err() == nil {
				t.Fatal("a malformed wait timing must fail the gate")
			}
		})
	}
}

// TestGateFailsWhenSpanLosesAllWaits closes the surviving-wait-predicate blind
// spot: a span that dropped links but kept no surviving wait link is invisible
// to WaitBearingDroppedLinks, yet on a wait-carrying trace it is wait loss.
func TestGateFailsWhenSpanLosesAllWaits(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idExec, "parentId": idNone, "name": "Container.withExec", "startNs": baseEp, "endNs": baseEnd,
			"attrs": map[string]any{telemetryattrs.WcprofOpKindAttr: "call_exec"}}),
		// keeps a surviving wait, so the trace is augmented (WaitEdges>0)
		rec(map[string]any{"spanId": idA, "parentId": idNone, "name": "Container.stdout", "startNs": baseEp, "endNs": baseEnd,
			"links": []any{waitLink(idExec, "singleflight", baseEp, baseEnd)}}),
		// dropped links but NO surviving wait link → invisible to WaitBearing
		rec(map[string]any{"spanId": idB, "parentId": idNone, "name": "Container.sync", "startNs": baseEp, "endNs": baseEnd,
			"droppedLinks": 3}),
	)
	c, g := mustLoad(t, jsonl)
	if c.WaitBearingDroppedLinks != 0 {
		t.Fatalf("the lost-all-waits span has no surviving wait; want WaitBearing=0, got %d", c.WaitBearingDroppedLinks)
	}
	if CheckStructural(c, g, GateOptions{}).Err() == nil {
		t.Fatal("dropped links on a wait-carrying trace must fail even with no surviving wait on that span")
	}
}

// TestGateUnaugmentedDroppedLinkPasses guards against a false positive: an
// un-augmented trace (no wait edges, e.g. captured from a stock 128-cap engine)
// with a benign dropped non-wait link must stay report-only, not fail.
func TestGateUnaugmentedDroppedLinkPasses(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idA, "parentId": idNone, "name": "Container.from", "startNs": baseEp, "endNs": baseEnd,
			"droppedLinks": 5,
			"attrs":        map[string]any{telemetry.DagDigestAttr: "sha256:a"}}),
	)
	c, g := mustLoad(t, jsonl)
	if c.TotalDroppedLinks != 5 || c.WaitEdgeCount != 0 {
		t.Fatalf("setup: want dropped=5 waits=0, got dropped=%d waits=%d", c.TotalDroppedLinks, c.WaitEdgeCount)
	}
	if err := CheckStructural(c, g, GateOptions{}).Err(); err != nil {
		t.Fatalf("a benign dropped link on an un-augmented trace must not fail: %v", err)
	}
}

// TestGateLockWaitNoTargetPasses: a lock wait is intentionally targetless and
// must not be counted as an unresolved target.
func TestGateLockWaitNoTargetPasses(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idA, "parentId": idNone, "name": "Container.withExec", "startNs": baseEp, "endNs": baseEnd,
			"links": []any{waitLinkRaw(idNone, map[string]any{
				telemetryattrs.WcprofWaitReasonAttr:        "lock",
				telemetryattrs.WcprofWaitIdentAttr:         "cachevol:/data",
				telemetryattrs.WcprofWaitStartUnixNanoAttr: strconv.Itoa(baseEp),
				telemetryattrs.WcprofWaitEndUnixNanoAttr:   strconv.Itoa(baseEnd),
			})}}),
	)
	c, g := mustLoad(t, jsonl)
	if c.UnresolvedWaitTargets != 0 {
		t.Fatalf("a lock wait is intentionally targetless; want 0 unresolved, got %d", c.UnresolvedWaitTargets)
	}
	if err := CheckStructural(c, g, GateOptions{}).Err(); err != nil {
		t.Fatalf("a lock wait must not fail the gate: %v", err)
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
