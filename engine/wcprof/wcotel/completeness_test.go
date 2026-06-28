package wcotel

import (
	"testing"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

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
