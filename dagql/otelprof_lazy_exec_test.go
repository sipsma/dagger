package dagql

// Chunk 4 review (Chunk 3 implementer, LOW): a committed emit-path test for the
// "lazy-triggered exec composes for free" claim — the composition the live module
// workload exercises. A withExec that returns a pending result runs its executor
// during lazy evaluation, so the exec subtree's spans are created under the lazy
// callback ctx: exec.run is a DIRECT re-pointed child of the producer (so the
// §3.0.2 stamping processor stamps it wcprof.parent = the lazy op), while its
// containerStart/processRun phases are exec.run's children (descendants) and stay
// unstamped, following their stamped ancestor. This locks down that the Chunk 4
// exec spans re-home under the lazy op via the (kind-agnostic) stamping with no
// change to the processor, loads cleanly, and the §6.1 gate stays green.

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine"
	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
)

// The exec.run / phase spans below mirror the exact shape engineutil's
// beginOTelExecRun / emitOTelExecPhase emit (engine/engineutil/otelprof.go) —
// passthrough, kind exec / exec_phase, work_type=user on processRun only — so this
// composition test exercises the real Chunk 4 exec wire shape against the Chunk 3
// stamping (the real beginOTelExecRun is separately covered in engineutil).
func TestLazyTriggeredExecComposesUnderLazyOp(t *testing.T) {
	const sessionID = "sess-exec"
	const resultID = sharedResultID(7)

	sr, rootCtx, root := newLazyRecordingRoot("POST /query")

	// producer O: the withExec call that returned a pending Container and ended.
	_, producer := Tracer(rootCtx).Start(rootCtx, "Container.withExec")
	producer.End()
	producerSC := producer.SpanContext()

	// consumer C: forces evaluation (e.g. .stdout); the lazy op nests under it.
	consumerCtx, consumer := Tracer(rootCtx).Start(rootCtx, "Container.stdout")

	c := &Cache{
		sessionLazySpansBySession: map[string]map[sharedResultID]trace.SpanContext{
			sessionID: {resultID: producerSC},
		},
	}
	evalCtx := engine.ContextWithClientMetadata(consumerCtx, &engine.ClientMetadata{
		SessionID: sessionID,
		ClientID:  "client-exec",
	})
	callbackCtx, lazySpan, isResume := c.beginOTelLazyOp(evalCtx, resultID, &ResultCall{Field: "withExec"}, "")
	if !isResume {
		t.Fatal("producer context captured ⇒ must be the resume re-point case")
	}
	lazyID := lazySpan.SpanContext().SpanID()

	// the deferred exec runs in the lazy callback: exec.run is the DIRECT re-pointed
	// child (parent = producer via resumedCallbackSpan); its phases are exec.run's
	// children (descendants). This is exactly the live module-workload shape.
	execCtx, execRun := Tracer(callbackCtx).Start(callbackCtx, "exec.run",
		telemetry.Passthrough(),
		trace.WithAttributes(
			attribute.String(telemetryattrs.WcprofOpKindAttr, wcprof.OpKindExec.String()),
			attribute.String(telemetry.DagDigestAttr, "xxh3:exec-ident"),
		),
	)
	_, cStart := Tracer(execCtx).Start(execCtx, "exec.containerStart",
		telemetry.Passthrough(),
		trace.WithAttributes(attribute.String(telemetryattrs.WcprofOpKindAttr, wcprof.OpKindExecPhase.String())),
	)
	cStart.End()
	_, pRun := Tracer(execCtx).Start(execCtx, "exec.processRun",
		telemetry.Passthrough(),
		trace.WithAttributes(
			attribute.String(telemetryattrs.WcprofOpKindAttr, wcprof.OpKindExecPhase.String()),
			attribute.String(telemetryattrs.WcprofWorkTypeAttr, wcprof.WorkTypeUser.String()),
		),
	)
	pRun.End()
	execRun.End()
	lazySpan.End()
	consumer.End()
	root.End()

	ended := sr.Ended()
	execRunSp := spanBySpanID(t, ended, execRun.SpanContext().SpanID())
	cStartSp := spanBySpanID(t, ended, cStart.SpanContext().SpanID())
	pRunSp := spanBySpanID(t, ended, pRun.SpanContext().SpanID())

	// (1) UI parentage unchanged: exec.run still parents to the producer.
	if execRunSp.Parent().SpanID() != producerSC.SpanID() {
		t.Fatalf("exec.run UI parent must stay the producer; got %s want %s",
			execRunSp.Parent().SpanID(), producerSC.SpanID())
	}
	// (2) causal re-home — exec.run (DIRECT child) is stamped; its phases are NOT.
	if got, ok := attrString(execRunSp, telemetryattrs.WcprofParentAttr); !ok || got != lazyID.String() {
		t.Fatalf("exec.run (direct re-pointed child) must carry wcprof.parent=%s; got %q ok=%v", lazyID, got, ok)
	}
	if _, ok := attrString(cStartSp, telemetryattrs.WcprofParentAttr); ok {
		t.Fatal("exec.containerStart (descendant) must NOT be stamped")
	}
	if _, ok := attrString(pRunSp, telemetryattrs.WcprofParentAttr); ok {
		t.Fatal("exec.processRun (descendant) must NOT be stamped")
	}

	// (3) loader: the whole exec subtree re-homes under the lazy op; phases keep
	// nesting under exec.run; work_type=user survives; §6.1 gate green.
	cc, err := wcotel.Compile(toWcotelSpans(ended))
	if err != nil {
		t.Fatalf("compile real lazy-exec emit: %v", err)
	}
	g, err := wcanalyze.Build(cc.Header, cc.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := wcotel.CheckStructural(cc, g, wcotel.GateOptions{}).Err(); err != nil {
		t.Fatalf("structural gate must pass on the lazy-triggered exec emit: %v", err)
	}
	lazyOp := opByClass(t, g, "resume withExec")
	execOp := opByClass(t, g, "exec.run")
	prOp := opByClass(t, g, "exec.processRun")
	if execOp.Kind != wcprof.OpKindExec.String() {
		t.Fatalf("exec.run loaded kind = %q, want exec", execOp.Kind)
	}
	if execOp.Parent != lazyOp {
		t.Fatalf("exec.run must causally re-home under the lazy op; got parent %v", execOp.Parent)
	}
	if prOp.Parent != execOp {
		t.Fatalf("exec.processRun must stay under exec.run by parentId; got parent %v", prOp.Parent)
	}
	if prOp.WorkType != wcprof.WorkTypeUser.String() {
		t.Fatalf("exec.processRun work_type must survive the lazy re-home; got %q", prOp.WorkType)
	}
}
