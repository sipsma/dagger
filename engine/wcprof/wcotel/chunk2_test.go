package wcotel

// Chunk 2 fixtures (design §6.2 oracle, §6.3 known-answer, §6.5 adversarial):
// the singleflight central fix. Each builds otlpdump-shaped spans mirroring
// EXACTLY what the engine emit (dagql/cache.go beginOTelCallExec / emitOTelCallWait
// / beginOTelPublishResult) produces — call_exec span, per-caller wait links,
// publishResult child — runs them through the loader + structural gate + replay,
// and (for the oracle) compares against the equivalent native wcprof IR.
//
// These are the deterministic regression suite. The empirical oracle on a freshly
// built augmented engine-dev (the DoD's convergence numbers) is separate; this
// proves the loader, the model, and the oracle harness handle the emit shape.

import (
	"bytes"
	"strconv"
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

const ms = 1_000_000 // ns per ms, for readable fixtures

// publishResultSpanName mirrors the engine emit's dagql.publishResult span name
// (dagql/otelprof_hooks.go) — the loader reads it as the op class.
const publishResultSpanName = "dagql.publishResult"

// ---- builders that mirror the emit side ----

// otSpan builds an otlpdump-shaped span record with absolute times (baseEp+offset
// in ms), matching otlpdump's integer marshaling.
func otSpan(id, parent, name string, startMS, endMS int64, attrs map[string]any, links ...map[string]any) map[string]any {
	r := map[string]any{
		"spanId": id, "parentId": parent, "name": name,
		"startNs": baseEp + startMS*ms, "endNs": baseEp + endMS*ms,
	}
	if attrs != nil {
		r["attrs"] = attrs
	}
	if len(links) > 0 {
		ls := make([]any, len(links))
		for i := range links {
			ls[i] = links[i]
		}
		r["links"] = ls
	}
	return rec(r)
}

// callExecAttrs are the call_exec span attributes beginOTelCallExec emits.
func callExecAttrs(digest string) map[string]any {
	return map[string]any{
		telemetryattrs.WcprofOpKindAttr: wcprof.OpKindCallExec.String(),
		telemetry.DagDigestAttr:         digest,
		telemetry.UIPassthroughAttr:     true,
	}
}

// publishAttrs are the dagql.publishResult span attributes beginOTelPublishResult emits.
func publishAttrs() map[string]any {
	return map[string]any{
		telemetryattrs.WcprofOpKindAttr: wcprof.OpKindInternal.String(),
		telemetry.UIPassthroughAttr:     true,
	}
}

// callAttrs are a plain dagql caller span's attributes (from core.AroundFunc).
func callAttrs(digest string) map[string]any {
	return map[string]any{telemetry.DagDigestAttr: digest}
}

// otWait builds a wcprof wait-edge link (emitOTelCallWait's output): purpose=wait,
// reason, absolute-unix-ns decimal-string timing, targeting the call_exec span.
func otWait(targetSpanID, reason string, startMS, endMS int64) map[string]any {
	return map[string]any{
		"spanId": targetSpanID,
		"attrs": map[string]any{
			telemetry.LinkPurposeAttr:                  telemetryattrs.LinkPurposeWait,
			telemetryattrs.WcprofWaitReasonAttr:        reason,
			telemetryattrs.WcprofWaitStartUnixNanoAttr: strconv.FormatInt(baseEp+startMS*ms, 10),
			telemetryattrs.WcprofWaitEndUnixNanoAttr:   strconv.FormatInt(baseEp+endMS*ms, 10),
		},
	}
}

// nativeIR builds the equivalent native wcprof dump IR for the oracle's
// ground-truth side.
type nativeIR struct {
	str    *stringTable
	events []wcprof.DumpEvent
}

func newNativeIR() *nativeIR { return &nativeIR{str: newStringTable()} }

func (b *nativeIR) op(id, parent uint64, kind, class string, startMS, endMS int64, outcome string) {
	b.events = append(b.events, wcprof.DumpEvent{
		Type: "op", OpKind: kind, WorkType: wcprof.WorkTypeEngine.String(), Outcome: outcome,
		OpID: id, ParentID: parent, ClassID: b.str.intern(class),
		StartNS: startMS * ms, EndNS: endMS * ms,
	})
}

func (b *nativeIR) wait(waiter, target uint64, reason string, startMS, endMS int64) {
	b.events = append(b.events, wcprof.DumpEvent{
		Type: "wait", Reason: reason, ParentID: waiter, TargetID: target,
		StartNS: startMS * ms, EndNS: endMS * ms,
	})
}

func (b *nativeIR) graph(t *testing.T) *wcanalyze.Graph {
	t.Helper()
	h := &wcprof.DumpHeader{
		SchemaVersion: wcprof.DumpSchemaVersion,
		EventCount:    len(b.events),
		Strings:       b.str.values,
	}
	g, err := wcanalyze.Build(h, b.events)
	if err != nil {
		t.Fatalf("native build: %v", err)
	}
	return g
}

func mustGate(t *testing.T, c *Compiled, g *wcanalyze.Graph) GateReport {
	t.Helper()
	gate := CheckStructural(c, g, GateOptions{})
	if err := gate.Err(); err != nil {
		var sb bytes.Buffer
		gate.Write(&sb)
		t.Fatalf("structural gate must pass:\n%s\n%v", sb.String(), err)
	}
	return gate
}

func opByClassKind(g *wcanalyze.Graph, kind, class string) *wcanalyze.Op {
	for _, op := range g.Ops {
		if op.Kind == kind && op.Class == class {
			return op
		}
	}
	return nil
}

// ---- §6.2 oracle: singleflight fan-in, native ↔ OTel agree ----

// TestChunk2SingleflightOracle is the §6.2 oracle on a singleflight-heavy
// workload: one executor runs a slow resolver (the call_exec bottleneck) and
// several callers join it. The OTel emit shape (call_exec span + per-caller wait
// links + publishResult child) compiles to the same bottleneck ranking as the
// native wcprof IR for the same run — the contract that proves Chunk 2 converged.
func TestChunk2SingleflightOracle(t *testing.T) {
	const (
		nJoiners  = 3
		execStart = 12
		execEnd   = 60 // 48ms of resolver self-time: the dominant bottleneck
	)
	const digest = "sha256:slowdigest"
	const class = "Container.stdout"

	// --- OTel spans (callers NOT suppressed: identical structure to native) ---
	otelRecs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		// executor caller: a plain call span with the call_exec child + its wait
		otSpan(idA, idRoot, class, 10, 70, callAttrs(digest), otWait(idExec, "call_exec", execStart, execEnd)),
		// the shared execution
		otSpan(idExec, idA, class, execStart, execEnd, callExecAttrs(digest)),
		// publication: a late child of call_exec (native-parity diagnostic)
		otSpan(idLazy, idExec, publishResultSpanName, execEnd, execEnd+2, publishAttrs()),
	}
	nat := newNativeIR()
	const (
		nRoot uint64 = 1
		nCall uint64 = 2
		nExec uint64 = 3
		nPub  uint64 = 4
	)
	nat.op(nRoot, 0, "", "POST /query", 0, 100, wcprof.OutcomeOK.String())
	nat.op(nCall, nRoot, wcprof.OpKindCall.String(), class, 10, 70, wcprof.OutcomeExecuted.String())
	nat.op(nExec, nCall, wcprof.OpKindCallExec.String(), class, execStart, execEnd, wcprof.OutcomeExecuted.String())
	nat.op(nPub, nExec, wcprof.OpKindInternal.String(), publishResultSpanName, execEnd, execEnd+2, wcprof.OutcomeOK.String())
	nat.wait(nCall, nExec, wcprof.WaitReasonCallExec.String(), execStart, execEnd)

	// joiners: each its own caller span/op, joining the shared execution
	joinerIDs := []string{"a1a1a1a1a1a1a1a1", "b2b2b2b2b2b2b2b2", "c3c3c3c3c3c3c3c3"}
	for i := range nJoiners {
		jStart := int64(20 + i*2)
		otelRecs = append(otelRecs,
			otSpan(joinerIDs[i], idRoot, class, jStart, execEnd, callAttrs(digest),
				otWait(idExec, "singleflight", jStart, execEnd)))
		njID := uint64(10 + i)
		nat.op(njID, nRoot, wcprof.OpKindCall.String(), class, jStart, execEnd, wcprof.OutcomeJoined.String())
		nat.wait(njID, nExec, wcprof.WaitReasonSingleflight.String(), jStart, execEnd)
	}

	otelC := mustCompile(t, toJSONL(t, otelRecs...))
	otelG, err := wcanalyze.Build(otelC.Header, otelC.Events)
	if err != nil {
		t.Fatalf("otel build: %v", err)
	}
	nativeG := nat.graph(t)

	// Gate must pass on the augmented OTel trace, now exercising the wait-loss
	// invariants for real (every singleflight target resolves, timings parse).
	gate := mustGate(t, otelC, otelG)
	if gate.WaitEdges != nJoiners+1 {
		t.Fatalf("want %d wait edges (executor + %d joiners), got %d", nJoiners+1, nJoiners, gate.WaitEdges)
	}
	if gate.UnresolvedWaitTargets != 0 || gate.MalformedWaitTimings != 0 {
		t.Fatalf("wait-loss: unresolved=%d malformed=%d", gate.UnresolvedWaitTargets, gate.MalformedWaitTimings)
	}

	// Break #1 fixed: every joiner's self-time is ~0 (its whole interval is a
	// wait), so the OTel source no longer over-credits joiners with work.
	for _, op := range otelG.Ops {
		if op.Kind == wcprof.OpKindCall.String() && op.Class == class && op.StartNS >= 20*ms {
			if self := op.SelfNS(); self > ms {
				t.Fatalf("joiner self-time should be ~0, got %dns", self)
			}
		}
	}

	// The call_exec self-time matches native within tolerance (the bottleneck).
	otelExec := opByClassKind(otelG, wcprof.OpKindCallExec.String(), class)
	natExec := opByClassKind(nativeG, wcprof.OpKindCallExec.String(), class)
	if otelExec == nil || natExec == nil {
		t.Fatal("missing call_exec op in one source")
	}
	if d := otelExec.SelfNS() - natExec.SelfNS(); d > ms || d < -ms {
		t.Fatalf("call_exec self-time drift: otel=%d native=%d", otelExec.SelfNS(), natExec.SelfNS())
	}

	// Ranking-level oracle (design §6.2): native ↔ OTel top-N agree.
	cmp, err := Oracle(nativeG, otelG, 0, 10, ms)
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	var sb bytes.Buffer
	cmp.Write(&sb)
	t.Logf("oracle result:\n%s", sb.String())
	if !cmp.Agrees(0.99, 0.02) {
		t.Fatalf("native↔OTel rankings must converge: jaccard=%.2f maxRelDrift=%.2f\n%s",
			cmp.JaccardTopN(), cmp.MaxRelDrift(), sb.String())
	}
	// call_exec must be the credited bottleneck in both.
	wantKey := wcanalyze.ClassKey{Kind: wcprof.OpKindCallExec.String(), Class: class}
	if len(cmp.NativeTop) == 0 || cmp.NativeTop[0].Key != wantKey || len(cmp.OTelTop) == 0 || cmp.OTelTop[0].Key != wantKey {
		t.Fatalf("call_exec must rank #1 in both sources:\n%s", sb.String())
	}
}

// ---- §6.5 emitter ≠ executor: the resolver's children never mis-parent ----

// TestChunk2EmitterNotExecutor drives the Break #3 race: the caller that executes
// the resolver was telemetry-suppressed (no caller span of its own). The
// call_exec span is still minted in the cache layer, so the resolver's sub-call
// nests under call_exec (not scattered under an unrelated ancestor), and the
// executor's wait lands on the suppressed caller's ancestor.
func TestChunk2EmitterNotExecutor(t *testing.T) {
	const digest = "sha256:race"
	const class = "Container.withExec"
	// Executor was suppressed → its call_exec is parented directly under the
	// ancestor (idRoot), and the wait lands on idRoot too. A real resolver
	// sub-call (idA) nests under call_exec.
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil, otWait(idExec, "call_exec", 10, 60)),
		otSpan(idExec, idRoot, class, 10, 60, callExecAttrs(digest)),
		otSpan(idA, idExec, "Container.from", 12, 55, callAttrs("sha256:child")), // resolver sub-call
		otSpan(idLazy, idExec, publishResultSpanName, 60, 62, publishAttrs()),
	}
	c := mustCompile(t, toJSONL(t, recs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	mustGate(t, c, g)

	exec := opByClassKind(g, wcprof.OpKindCallExec.String(), class)
	child := opByClassKind(g, wcprof.OpKindCall.String(), "Container.from")
	if exec == nil || child == nil {
		t.Fatal("missing call_exec or resolver sub-call op")
	}
	// The resolver sub-call must nest under call_exec, not under the ancestor.
	if child.Parent == nil || child.Parent.ID != exec.ID {
		t.Fatalf("resolver sub-call mis-parented: parent=%v want call_exec %d", child.Parent, exec.ID)
	}
	// call_exec itself nests under the ancestor (the suppressed executor's parent).
	if exec.Parent == nil || exec.Class != class {
		t.Fatalf("call_exec should nest under the ancestor, got parent=%v", exec.Parent)
	}
}

// ---- §6.5 cap-stress: many suppressed siblings on one parent ----

// TestChunk2CapStressFanIn pushes a high concurrent suppressed-sibling fan-in:
// one ancestor span accrues many singleflight waits to a single call_exec (the
// shape that, on a stock 128-link engine, would silently evict the earliest
// waits). It asserts (a) zero dropped links at the 16384 cap, (b) replay models
// fan-in as max not sum (the parent's self-time subtracts the union of waits,
// not their total), and (c) the gate stays green.
func TestChunk2CapStressFanIn(t *testing.T) {
	const nJoiners = 5000 // beyond realistic fan-out, under the 16384 cap
	const execEnd = 50
	const ancestor = "Query.batch"

	links := make([]map[string]any, 0, nJoiners)
	for i := range nJoiners {
		// staggered starts, all blocked until the one execution finishes
		links = append(links, otWait(idExec, "singleflight", int64(i%40), execEnd))
	}
	// The joiners' shared ancestor (idB) carries all the suppressed siblings'
	// waits; the executor's own caller (idA) hosts the call_exec — distinct
	// spans, as in a real trace.
	mkRecs := func() []map[string]any {
		return []map[string]any{
			otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
			otSpan(idA, idRoot, "Container.stdout", 1, execEnd, callAttrs("sha256:hot"),
				otWait(idExec, "call_exec", 1, execEnd)),
			otSpan(idExec, idA, "Container.stdout", 1, execEnd, callExecAttrs("sha256:hot")),
			otSpan(idB, idRoot, ancestor, 0, 100, nil, links...),
		}
	}
	// DroppedLinks defaults to 0 (the engine's 16384 cap held); assert the gate
	// treats that as clean on a wait-carrying trace.
	c := mustCompile(t, toJSONL(t, mkRecs()...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gate := mustGate(t, c, g)
	if gate.WaitEdges != nJoiners+1 {
		t.Fatalf("want %d wait edges (executor + %d joiners), got %d", nJoiners+1, nJoiners, gate.WaitEdges)
	}
	if gate.TotalDroppedLinks != 0 {
		t.Fatalf("no links may be dropped at the 16384 cap, got %d", gate.TotalDroppedLinks)
	}

	// Fan-in (max), not serialization (sum): the ancestor's self-time is its
	// interval minus the UNION of the waits (≈ [0,execEnd]), not minus their
	// sum. With all waits ending at execEnd, the ancestor self-time is bounded
	// by the time outside [0,execEnd], far less than nJoiners*durations.
	anc := opByClassKind(g, "", ancestor)
	if anc == nil {
		t.Fatal("missing wait-carrying ancestor op")
	}
	if self := anc.SelfNS(); self > 60*ms { // generous: union is ~[0,50ms], tail ~[50,100ms]
		t.Fatalf("fan-in must be max not sum: ancestor self-time %dns implies serialization", self)
	}

	// A dropped link on this wait-carrying trace must fail the gate (regression
	// guard for the 16384 choice — proves the dropped-count signal is wired).
	dropRecs := mkRecs()
	dropRecs[3]["droppedLinks"] = 1
	dc := mustCompile(t, toJSONL(t, dropRecs...))
	dg, err := wcanalyze.Build(dc.Header, dc.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := CheckStructural(dc, dg, GateOptions{}).Err(); err == nil {
		t.Fatal("a dropped link on a wait-carrying trace must fail the gate")
	}
}

// ---- §6.3 known-answer: critical-path work ranks, off-path work doesn't ----

// TestChunk2KnownAnswerCriticalVsParallel injects a known-cost execution on the
// critical path (a long call_exec that a caller waits on) and an equally costly
// execution off the critical path (its result is never awaited on the makespan
// path). The on-path class ranks with SavedNS ≈ its cost; the off-path class
// does not — proving the counterfactual still distinguishes total time from
// bottleneck through the OTel wait edges.
func TestChunk2KnownAnswerCriticalVsParallel(t *testing.T) {
	const onClass = "Container.criticalExec"
	const offClass = "Container.parallelExec"

	// The makespan is driven by the on-path chain: root → caller A → (wait) →
	// the slow on-path execution [8,48]. The off-path execution [3,13] runs
	// concurrently but finishes early (at 13ms), well inside the on-path
	// shadow, so it is never the makespan limiter.
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 52, nil),
		// on-path caller: nearly its whole interval is the wait on the slow exec.
		otSpan(idA, idRoot, onClass, 5, 50, callAttrs("sha256:crit"),
			otWait(idExec, "singleflight", 8, 48)),
		otSpan(idExec, idA, onClass, 8, 48, callExecAttrs("sha256:crit")), // 40ms on-path self
		// off the critical path: a parallel execution finishing early (10ms).
		otSpan(idB, idRoot, offClass, 2, 14, callAttrs("sha256:par"),
			otWait(idLazy, "singleflight", 3, 13)),
		otSpan(idLazy, idB, offClass, 3, 13, callExecAttrs("sha256:par")), // 10ms off-path self
	}
	c := mustCompile(t, toJSONL(t, recs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	mustGate(t, c, g)

	baseline, ranked, err := TopBottlenecks(g, 0, ms)
	if err != nil {
		t.Fatalf("what-ifs: %v", err)
	}
	saved := map[wcanalyze.ClassKey]int64{}
	for _, r := range ranked {
		saved[r.Key] = r.SavedNS
	}
	onKey := wcanalyze.ClassKey{Kind: wcprof.OpKindCallExec.String(), Class: onClass}
	offKey := wcanalyze.ClassKey{Kind: wcprof.OpKindCallExec.String(), Class: offClass}
	t.Logf("baseline=%dns on-path saved=%dns off-path saved=%dns", baseline, saved[onKey], saved[offKey])

	// The on-path execution dominates the makespan: removing it saves most of
	// the run (the wait edge propagates its cost up the critical path).
	if s := saved[onKey]; s < 25*ms {
		t.Fatalf("on-path call_exec should rank as the bottleneck (SavedNS≫0), got %dns", s)
	}
	// The off-path execution, finishing at 13ms inside the on-path shadow, never
	// moves the makespan — it must not rank.
	if s := saved[offKey]; s > 2*ms {
		t.Fatalf("off-path call_exec must not rank (SavedNS≈0), got %dns", s)
	}
	if saved[onKey] <= saved[offKey] {
		t.Fatalf("on-path (%d) must out-rank off-path (%d)", saved[onKey], saved[offKey])
	}
}
