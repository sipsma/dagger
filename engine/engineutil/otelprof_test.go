package engineutil

// Emit-path regression test for the exec engine/user split (design §3.3),
// mirroring dagql's otelprof_hooks_test.go discipline: drive the REAL emit
// helpers (beginOTelExecRun + emitOTelExecSplit) against an in-memory SDK
// tracer, then feed the genuinely-exported spans through the Chunk 1 loader +
// structural gate — so the wcotel fixtures' assumed shape is machine-checked
// against what the executor actually emits, not just code-reviewed.

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
)

// newRecordingRoot returns an always-sampling in-memory recorder plus a root span
// whose context drives Tracer(ctx) for the exec emit helpers under test.
func newRecordingRoot(name string) (*tracetest.SpanRecorder, context.Context, trace.Span) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sr),
	)
	ctx, root := tp.Tracer("wcprof-otel-test").Start(context.Background(), name)
	return sr, ctx, root
}

func opByClassKind(g *wcanalyze.Graph, kind, class string) *wcanalyze.Op {
	for _, op := range g.Ops {
		if op.Kind == kind && op.Class == class {
			return op
		}
	}
	return nil
}

func toWcotelSpans(ended []sdktrace.ReadOnlySpan) []wcotel.Span {
	kvMap := func(kvs []attribute.KeyValue) map[string]any {
		m := make(map[string]any, len(kvs))
		for _, kv := range kvs {
			m[string(kv.Key)] = kv.Value.AsInterface()
		}
		return m
	}
	out := make([]wcotel.Span, 0, len(ended))
	for _, s := range ended {
		links := make([]wcotel.Link, 0, len(s.Links()))
		for _, l := range s.Links() {
			links = append(links, wcotel.Link{
				SpanID: l.SpanContext.SpanID().String(),
				Attrs:  kvMap(l.Attributes),
			})
		}
		out = append(out, wcotel.Span{
			TraceID:     s.SpanContext().TraceID().String(),
			SpanID:      s.SpanContext().SpanID().String(),
			ParentID:    s.Parent().SpanID().String(),
			Name:        s.Name(),
			StartUnixNS: uint64(s.StartTime().UnixNano()),
			EndUnixNS:   uint64(s.EndTime().UnixNano()),
			Attrs:       kvMap(s.Attributes()),
			StatusError: s.Status().Code == codes.Error,
			Links:       links,
		})
	}
	return out
}

func spanByName(t *testing.T, ended []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range ended {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no exported span named %q", name)
	return nil
}

func attrStr(s sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

func attrBool(s sdktrace.ReadOnlySpan, key string) bool {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsBool()
		}
	}
	return false
}

// TestEmitExecSplitProducesLoaderShape drives the real exec.run + split emit (a
// withExec with 20ms of engine container-setup and a 100ms user process), asserts
// the exported spans carry exactly the attributes/timing the wcotel fixtures
// assume, and compiles them through the Chunk 1 loader + gate. The headline check
// (a slow user process ranks as user work) lives in wcotel/chunk4_test.go; here we
// machine-check the emit↔fixture correspondence.
func TestEmitExecSplitProducesLoaderShape(t *testing.T) {
	const (
		digest = "xxh3:withexec-digest"
		stateID = "exec-state-id-0001"
	)
	sr, ctx, root := newRecordingRoot("Container.withExec")

	// the executor runs under the call_exec span (here the recording root); emit
	// exec.run + the split. ctx carries the parent span, so Tracer(ctx) nests them.
	// Small REAL elapsed time keeps the live exec.run/root spans enclosing the
	// backdated split (as production exec.run brackets the whole run): 4ms engine
	// container-setup, then 12ms user process.
	ctx, execRun := beginOTelExecRun(ctx, digest)
	start := time.Now()
	time.Sleep(4 * time.Millisecond)
	started := time.Now()
	time.Sleep(12 * time.Millisecond)
	end := time.Now()
	emitOTelExecSplit(ctx, stateID, start, started, end, nil)
	var nilErr error
	endOTelExecRun(execRun, &nilErr)
	root.End()
	ended := sr.Ended()

	// (1) exec.run shape.
	run := spanByName(t, ended, "exec.run")
	if got, _ := attrStr(run, telemetryattrs.WcprofOpKindAttr); got != wcprof.OpKindExec.String() {
		t.Fatalf("exec.run op kind = %q, want exec", got)
	}
	if got, _ := attrStr(run, telemetry.DagDigestAttr); got != digest {
		t.Fatalf("exec.run dag.digest = %q, want %q", got, digest)
	}
	if !attrBool(run, telemetry.UIPassthroughAttr) {
		t.Fatal("exec.run must be ui.passthrough")
	}

	// (2) containerStart (engine): exec_phase, no work_type, child of exec.run,
	// timing [base, started].
	cs := spanByName(t, ended, "exec.containerStart")
	if got, _ := attrStr(cs, telemetryattrs.WcprofOpKindAttr); got != wcprof.OpKindExecPhase.String() {
		t.Fatalf("containerStart op kind = %q, want exec_phase", got)
	}
	if _, ok := attrStr(cs, telemetryattrs.WcprofWorkTypeAttr); ok {
		t.Fatal("containerStart (engine) must NOT carry work_type=user")
	}
	if cs.Parent().SpanID() != run.SpanContext().SpanID() {
		t.Fatal("containerStart must nest under exec.run")
	}
	if !cs.StartTime().Equal(start) || !cs.EndTime().Equal(started) {
		t.Fatalf("containerStart interval = [%v,%v], want [%v,%v]", cs.StartTime(), cs.EndTime(), start, started)
	}

	// (3) processRun (user): exec_phase, work_type=user, child of exec.run, timing
	// [started, end] — the user-work-first-class span.
	pr := spanByName(t, ended, "exec.processRun")
	if got, _ := attrStr(pr, telemetryattrs.WcprofOpKindAttr); got != wcprof.OpKindExecPhase.String() {
		t.Fatalf("processRun op kind = %q, want exec_phase", got)
	}
	if got, ok := attrStr(pr, telemetryattrs.WcprofWorkTypeAttr); !ok || got != wcprof.WorkTypeUser.String() {
		t.Fatalf("processRun must carry work_type=user, got %q (present=%v)", got, ok)
	}
	if !attrBool(pr, telemetry.UIPassthroughAttr) {
		t.Fatal("processRun must be ui.passthrough")
	}
	if pr.Parent().SpanID() != run.SpanContext().SpanID() {
		t.Fatal("processRun must nest under exec.run")
	}
	if !pr.StartTime().Equal(started) || !pr.EndTime().Equal(end) {
		t.Fatalf("processRun interval = [%v,%v], want [%v,%v]", pr.StartTime(), pr.EndTime(), started, end)
	}

	// (4) end-to-end: compile the REAL exported spans through the Chunk 1 loader +
	// gate, and confirm the loaded ops classify correctly with work_type=user
	// surviving onto the process-run op.
	c, err := wcotel.Compile(toWcotelSpans(ended))
	if err != nil {
		t.Fatalf("compile real emit: %v", err)
	}
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := wcotel.CheckStructural(c, g, wcotel.GateOptions{}).Err(); err != nil {
		t.Fatalf("structural gate must pass on the real exec-split emit: %v", err)
	}
	runOp := opByClassKind(g, wcprof.OpKindExec.String(), "exec.run")
	csOp := opByClassKind(g, wcprof.OpKindExecPhase.String(), "exec.containerStart")
	prOp := opByClassKind(g, wcprof.OpKindExecPhase.String(), "exec.processRun")
	if runOp == nil || csOp == nil || prOp == nil {
		t.Fatalf("missing loaded ops: run=%v cs=%v pr=%v", runOp, csOp, prOp)
	}
	if csOp.Parent != runOp || prOp.Parent != runOp {
		t.Fatal("loaded containerStart/processRun must be children of exec.run")
	}
	if prOp.WorkType != wcprof.WorkTypeUser.String() {
		t.Fatalf("loaded processRun work_type = %q, want user", prOp.WorkType)
	}
	if csOp.WorkType == wcprof.WorkTypeUser.String() {
		t.Fatal("loaded containerStart must stay engine work_type, not user")
	}
	// the user process (100ms) carries far more self-time than engine setup (20ms).
	if prOp.SelfNS() <= csOp.SelfNS() {
		t.Fatalf("user processRun self (%dns) must exceed engine containerStart self (%dns)", prOp.SelfNS(), csOp.SelfNS())
	}
}

// TestEmitExecSplitSetupFailure covers the never-started case: a setup failure
// before the process starts yields a single containerStart over the whole
// interval, charged the run error, and NO processRun (mirroring native).
func TestEmitExecSplitSetupFailure(t *testing.T) {
	sr, ctx, root := newRecordingRoot("Container.withExec")
	ctx, execRun := beginOTelExecRun(ctx, "xxh3:failed-exec")

	start := time.Now()
	time.Sleep(5 * time.Millisecond)
	end := time.Now()
	runErr := errors.New("setup failed: mount error")
	// started == zero time: the process never started.
	emitOTelExecSplit(ctx, "exec-state-id-fail", start, time.Time{}, end, runErr)
	endOTelExecRun(execRun, &runErr)
	root.End()
	ended := sr.Ended()

	for _, s := range ended {
		if s.Name() == "exec.processRun" {
			t.Fatal("a never-started exec must emit no processRun span")
		}
	}
	cs := spanByName(t, ended, "exec.containerStart")
	if cs.Status().Code != codes.Error {
		t.Fatal("a setup failure must charge the error to containerStart")
	}
	if !cs.StartTime().Equal(start) || !cs.EndTime().Equal(end) {
		t.Fatalf("failed containerStart must span the whole [%v,%v], got [%v,%v]", start, end, cs.StartTime(), cs.EndTime())
	}
}
