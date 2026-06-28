package wcotel

import (
	"strconv"
	"testing"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

const idCarrier = "9999999999999999"

// withCarrier returns the carrier form the live engine emits (design §6.1): every
// real span marked an engine span, plus a SEPARATE session-teardown carrier span —
// NOT an engine span — that declares the EXACT total (= number of real spans). The
// loader reads the count off the carrier and drops the carrier from the compiled
// ops. This is the EXACT declaration, distinct from markComplete's count-on-root
// "complete by construction" form used by the simpler fixtures.
func withCarrier(recs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(recs)+1)
	for _, r := range recs {
		cp := map[string]any{}
		for k, v := range r {
			cp[k] = v
		}
		attrs, _ := cp["attrs"].(map[string]any)
		na := map[string]any{}
		for k, v := range attrs {
			na[k] = v
		}
		na[telemetryattrs.WcprofEngineSpanAttr] = true
		cp["attrs"] = na
		out = append(out, cp)
	}
	out = append(out, otSpan(idCarrier, idRoot, "wcprof.session_complete", 99, 100, map[string]any{
		telemetryattrs.WcprofSessionCompleteAttr:  true,
		telemetryattrs.WcprofSessionSpanCountAttr: strconv.Itoa(len(recs)),
	}))
	return out
}

// dropSpan removes every record of a span id (modelling a span the BSP dropped on
// the way to Cloud).
func dropSpan(recs []map[string]any, id string) []map[string]any {
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		if sid, _ := r["spanId"].(string); sid == id {
			continue
		}
		out = append(out, r)
	}
	return out
}

// TestCompletenessGateCatchesDroppedLeaf is the key proof for the §6.1 leaf-drop
// hole. A dropped LEAF span (a user-work exec.processRun that nothing references)
// breaks no edge — no orphaned parent, no unresolved wait — so the reference-based
// gate signals are blind to it and the trace would gate-PASS while silently
// incomplete (a wrong ranking). The engine span-count checksum is the only thing
// that catches it. This test builds a complete trace, confirms it passes, then drops
// the leaf and confirms the gate — which previously passed — now hard-fails, while
// the orphan/unresolved-wait signals stay clean.
func TestCompletenessGateCatchesDroppedLeaf(t *testing.T) {
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "Container.withExec", 5, 95, callAttrs("sha256:x"), otWait(idExec, "call_exec", 8, 92)),
		otSpan(idExec, idA, "Container.withExec", 8, 92, callExecAttrs("sha256:x")),
		otSpan(idERun, idExec, "exec.run", 10, 90, execRunAttrs("sha256:x")),
		otSpan(idPRun, idERun, "exec.processRun", 12, 90, execPhaseAttrs("sha256:x", true)), // the user-work LEAF
	}

	// complete: the gate passes and the checksum reconciles.
	cFull := mustCompile(t, toJSONL(t, recs...))
	gFull, err := wcanalyze.Build(cFull.Header, cFull.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gateFull := CheckStructural(cFull, gFull, GateOptions{})
	if err := gateFull.Err(); err != nil {
		t.Fatalf("complete trace must pass the gate: %v", err)
	}
	if !gateFull.SessionMarkerPresent || gateFull.MissingSpans != 0 ||
		gateFull.DeclaredEngineSpans != 5 || gateFull.ReceivedEngineSpans != 5 {
		t.Fatalf("complete: marker=%v missing=%d declared=%d received=%d",
			gateFull.SessionMarkerPresent, gateFull.MissingSpans, gateFull.DeclaredEngineSpans, gateFull.ReceivedEngineSpans)
	}

	// drop the user-work leaf: declared stays 5 (on the root), received drops to 4.
	marked := markComplete(recs)
	dropped := marked[:len(marked)-1] // remove exec.processRun (the leaf)
	cDrop := mustCompile(t, toJSONLRaw(t, dropped...))
	gDrop, err := wcanalyze.Build(cDrop.Header, cDrop.Events)
	if err != nil {
		t.Fatalf("build dropped: %v", err)
	}
	gateDrop := CheckStructural(cDrop, gDrop, GateOptions{})

	// the reference-based signals are STILL clean — the dropped leaf broke no edge.
	if gateDrop.OrphanedParents != 0 || gateDrop.UnresolvedWaitTargets != 0 {
		t.Fatalf("precondition: a dropped leaf must break no edge; orphaned=%d unresolved=%d",
			gateDrop.OrphanedParents, gateDrop.UnresolvedWaitTargets)
	}
	// ...but the completeness checksum catches it, and the gate now FAILS.
	if gateDrop.MissingSpans != 1 || gateDrop.DeclaredEngineSpans != 5 || gateDrop.ReceivedEngineSpans != 4 {
		t.Fatalf("dropped leaf must be caught: missing=%d declared=%d received=%d",
			gateDrop.MissingSpans, gateDrop.DeclaredEngineSpans, gateDrop.ReceivedEngineSpans)
	}
	if gateDrop.Err() == nil {
		t.Fatal("the gate MUST hard-fail on a dropped leaf (it previously passed) — closing the §6.1 leaf-drop hole")
	}
}

// TestCompletenessGateFailsByDefaultWithoutMarker: an unstamped trace (no
// wcprof.session_span_count) cannot be verified for completeness, so the gate
// refuses it rather than trusting it (a dropped leaf would otherwise be silent).
func TestCompletenessGateFailsByDefaultWithoutMarker(t *testing.T) {
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "Container.withExec", 5, 95, callExecAttrs("sha256:x")),
	}
	// toJSONLRaw: deliberately NO completeness marker.
	c := mustCompile(t, toJSONLRaw(t, recs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gate := CheckStructural(c, g, GateOptions{})
	if gate.SessionMarkerPresent {
		t.Fatal("an unstamped trace must not report a session marker")
	}
	if gate.Err() == nil {
		t.Fatal("fail-by-default: an unstamped / unverifiable trace must be refused (design §6.1)")
	}
}

// TestCompletenessCarrierExactExcludedFromOps pins the carrier form the live engine
// emits: an EXACT total declared on a separate teardown carrier span. The carrier
// reconciles against received exactly, is excluded from the compiled ops (graph
// untouched), and a dropped leaf is still caught — the count being on a separate
// carrier rather than a per-query root does not weaken individual-leaf detection.
func TestCompletenessCarrierExactExcludedFromOps(t *testing.T) {
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "Container.withExec", 5, 95, callAttrs("sha256:x"), otWait(idExec, "call_exec", 8, 92)),
		otSpan(idExec, idA, "Container.withExec", 8, 92, callExecAttrs("sha256:x")),
		otSpan(idERun, idExec, "exec.run", 10, 90, execRunAttrs("sha256:x")),
		otSpan(idPRun, idERun, "exec.processRun", 12, 90, execPhaseAttrs("sha256:x", true)), // the user-work LEAF
	}

	c := mustCompile(t, toJSONLRaw(t, withCarrier(recs)...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gate := CheckStructural(c, g, GateOptions{})
	if err := gate.Err(); err != nil {
		t.Fatalf("complete carrier-form trace must pass: %v", err)
	}
	if !gate.SessionMarkerPresent || gate.MissingSpans != 0 ||
		gate.DeclaredEngineSpans != 5 || gate.ReceivedEngineSpans != 5 {
		t.Fatalf("exact reconcile: marker=%v missing=%d declared=%d received=%d",
			gate.SessionMarkerPresent, gate.MissingSpans, gate.DeclaredEngineSpans, gate.ReceivedEngineSpans)
	}
	// The carrier is a pure declaration messenger, not a unit of work: it must not
	// appear as an op (the graph/replay must be untouched).
	if c.SpanCount != 5 {
		t.Fatalf("carrier must be excluded from ops: SpanCount=%d want 5", c.SpanCount)
	}
	for _, op := range g.Ops {
		if op.Class == "wcprof.session_complete" {
			t.Fatal("carrier span leaked into the compiled graph")
		}
	}

	// Drop the user-work leaf: the carrier still declares the exact 5, so received
	// (4) < declared (5) → caught, while the reference signals stay clean.
	dropped := dropSpan(withCarrier(recs), idPRun)
	cd := mustCompile(t, toJSONLRaw(t, dropped...))
	gd, err := wcanalyze.Build(cd.Header, cd.Events)
	if err != nil {
		t.Fatalf("build dropped: %v", err)
	}
	gateD := CheckStructural(cd, gd, GateOptions{})
	if gateD.OrphanedParents != 0 || gateD.UnresolvedWaitTargets != 0 {
		t.Fatalf("precondition: a dropped leaf breaks no edge; orphaned=%d unresolved=%d",
			gateD.OrphanedParents, gateD.UnresolvedWaitTargets)
	}
	if gateD.MissingSpans != 1 || gateD.DeclaredEngineSpans != 5 || gateD.ReceivedEngineSpans != 4 {
		t.Fatalf("dropped leaf must be caught: missing=%d declared=%d received=%d",
			gateD.MissingSpans, gateD.DeclaredEngineSpans, gateD.ReceivedEngineSpans)
	}
	if gateD.Err() == nil {
		t.Fatal("the gate must hard-fail on a dropped leaf")
	}
}

// TestCompletenessTrailingQueryDropCaught is the proof that the exact teardown count
// closes the whole-trailing-query-drop hole that the per-query running-total + loader
// MAX left open. A command runs two queries under one trace; the OLD scheme stamped a
// running floor on EACH query root (2, then 4) and the loader kept the max, so losing
// the entire FINAL query — including the root carrying "4" — reverted the max to "2"
// == the 2 surviving spans → silent pass. The exact total now rides on a SEPARATE
// teardown carrier that survives a trailing-query drop, so received (2) < declared
// (4) → caught.
func TestCompletenessTrailingQueryDropCaught(t *testing.T) {
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 40, nil),                            // query 1 root
		otSpan(idA, idRoot, "Container.from", 5, 35, callAttrs("sha256:a")),          // query 1 work
		otSpan(idB, idNone, "POST /query", 50, 95, nil),                              // query 2 root (trailing)
		otSpan(idExec, idB, "Container.withExec", 55, 90, callExecAttrs("sha256:b")), // query 2 work
	}
	// Drop the ENTIRE trailing query (both its root idB and its child), keep carrier.
	full := withCarrier(recs)
	dropped := dropSpan(dropSpan(full, idB), idExec)
	c := mustCompile(t, toJSONLRaw(t, dropped...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gate := CheckStructural(c, g, GateOptions{})
	// Nothing surviving references the dropped trailing query, so the reference
	// signals stay clean — only the exact count catches the loss.
	if gate.OrphanedParents != 0 || gate.UnresolvedWaitTargets != 0 {
		t.Fatalf("precondition: a whole-subtree drop with no surviving reference breaks no edge; orphaned=%d unresolved=%d",
			gate.OrphanedParents, gate.UnresolvedWaitTargets)
	}
	if gate.MissingSpans != 2 || gate.DeclaredEngineSpans != 4 || gate.ReceivedEngineSpans != 2 {
		t.Fatalf("trailing-query drop must be caught: missing=%d declared=%d received=%d",
			gate.MissingSpans, gate.DeclaredEngineSpans, gate.ReceivedEngineSpans)
	}
	if gate.Err() == nil {
		t.Fatal("the gate must hard-fail when an entire trailing query is dropped (design §6.1)")
	}
}

// TestCompletenessCarrierDropFailsByDefault: if the teardown carrier itself is the
// span that drops, the declaration is gone and completeness is unverifiable, so the
// gate refuses the trace by default rather than trusting the surviving spans.
func TestCompletenessCarrierDropFailsByDefault(t *testing.T) {
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "Container.withExec", 5, 95, callExecAttrs("sha256:x")),
	}
	dropped := dropSpan(withCarrier(recs), idCarrier)
	c := mustCompile(t, toJSONLRaw(t, dropped...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gate := CheckStructural(c, g, GateOptions{})
	if gate.SessionMarkerPresent {
		t.Fatal("with the carrier dropped, no completeness marker should be present")
	}
	if gate.Err() == nil {
		t.Fatal("fail-by-default: a trace whose count carrier dropped is unverifiable and must be refused")
	}
}
