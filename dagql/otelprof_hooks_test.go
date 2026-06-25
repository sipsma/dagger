package dagql

// Emit-path regression tests (Chunk 2 review B / codex MEDIUM / chunk1-implementer
// HOLISTIC-1): the wcotel fixtures hand-build the OTel JSON shape, so they prove
// the loader/gate/oracle but not that the *real* hooks emit that shape. These
// tests drive the three real emit hooks (beginOTelCallExec / beginOTelPublishResult
// / emitOTelCallWait) against an in-memory SDK tracer, then feed the genuinely
// exported spans through the Chunk 1 loader + structural gate — so the
// fixture↔emit correspondence is machine-checked, not just code-reviewed. The
// real per-provider LinkCountLimit cap stays an empirical check (out of scope here).

import (
	"context"
	"strconv"
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

// newRecordingRoot returns an always-sampling in-memory tracer recorder plus a
// root span whose context drives Tracer(ctx) for the hooks under test.
func newRecordingRoot(name string) (*tracetest.SpanRecorder, context.Context, trace.Span) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sr),
	)
	ctx, root := tp.Tracer("wcprof-otel-test").Start(context.Background(), name)
	return sr, ctx, root
}

// toWcotelSpans converts the real exported spans into the loader's neutral Span
// shape — the same conversion the otlpdump front-end performs, exercising the
// genuine attribute/link encoding the hooks produced.
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
				SpanID:       l.SpanContext.SpanID().String(),
				Attrs:        kvMap(l.Attributes),
				DroppedAttrs: l.DroppedAttributeCount,
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

func spanByKind(t *testing.T, ended []sdktrace.ReadOnlySpan, kind string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range ended {
		for _, kv := range s.Attributes() {
			if string(kv.Key) == telemetryattrs.WcprofOpKindAttr && kv.Value.AsString() == kind {
				return s
			}
		}
	}
	t.Fatalf("no exported span with %s=%q", telemetryattrs.WcprofOpKindAttr, kind)
	return nil
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

func attrString(s sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

func attrBool(s sdktrace.ReadOnlySpan, key string) (bool, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsBool(), true
		}
	}
	return false, false
}

// TestEmitHooksProduceLoaderShape drives the real singleflight emit (one executor
// caller + call_exec + publishResult + one joiner) and asserts (1) the emitted
// span/link attributes match exactly what the wcotel fixtures assume, and (2) the
// genuinely-exported spans compile through the Chunk 1 loader + gate into the
// expected IR with resolvable targets and valid timing.
func TestEmitHooksProduceLoaderShape(t *testing.T) {
	const digest = "xxh3:0123456789abcdef"
	const class = "Container.stdout"

	sr, rootCtx, root := newRecordingRoot("POST /query")

	// executor's caller span (the AroundFunc span), then the call_exec under it.
	callerCtx, caller := Tracer(rootCtx).Start(rootCtx, class)
	execCtx, execSpan := beginOTelCallExec(callerCtx, digest, class)
	// publication brackets initCompletedResult under the call_exec context.
	pubSpan := beginOTelPublishResult(execCtx)

	// executor waits on its own call_exec; a joiner (on the caller span) waits too.
	base := time.Now().UnixNano()
	emitOTelCallWait(callerCtx, execSpan.SpanContext(), wcprof.WaitReasonCallExec, base, base+2_000_000)
	emitOTelCallWait(callerCtx, execSpan.SpanContext(), wcprof.WaitReasonSingleflight, base+500_000, base+2_000_000)

	pubSpan.End()
	execSpan.End()
	caller.End()
	root.End()
	ended := sr.Ended()

	// (1) direct shape assertions — the exact keys/values the fixtures assume.
	exec := spanByKind(t, ended, wcprof.OpKindCallExec.String())
	if exec.Name() != class {
		t.Fatalf("call_exec span name should be the call class %q, got %q", class, exec.Name())
	}
	if got, _ := attrString(exec, telemetry.DagDigestAttr); got != digest {
		t.Fatalf("call_exec dag.digest = %q, want %q", got, digest)
	}
	if pass, ok := attrBool(exec, telemetry.UIPassthroughAttr); !ok || !pass {
		t.Fatalf("call_exec must be ui.passthrough")
	}

	pub := spanByName(t, ended, publishResultSpanName)
	if got, _ := attrString(pub, telemetryattrs.WcprofOpKindAttr); got != wcprof.OpKindInternal.String() {
		t.Fatalf("publishResult op kind = %q, want internal", got)
	}
	if pass, ok := attrBool(pub, telemetry.UIPassthroughAttr); !ok || !pass {
		t.Fatalf("publishResult must be ui.passthrough")
	}

	// the caller span (not the like-named call_exec) carries the wait links.
	var callerSpan sdktrace.ReadOnlySpan
	for _, s := range ended {
		if s.SpanContext().SpanID() == caller.SpanContext().SpanID() {
			callerSpan = s
		}
	}
	if callerSpan == nil {
		t.Fatal("caller span not exported")
	}
	links := callerSpan.Links()
	if len(links) != 2 {
		t.Fatalf("want 2 wait links on the waiter, got %d", len(links))
	}
	for _, l := range links {
		la := map[string]string{}
		for _, kv := range l.Attributes {
			la[string(kv.Key)] = kv.Value.AsString()
		}
		if la[telemetry.LinkPurposeAttr] != telemetryattrs.LinkPurposeWait {
			t.Fatalf("wait link missing %s=wait: %v", telemetry.LinkPurposeAttr, la)
		}
		switch la[telemetryattrs.WcprofWaitReasonAttr] {
		case wcprof.WaitReasonCallExec.String(), wcprof.WaitReasonSingleflight.String():
		default:
			t.Fatalf("unexpected wait reason %q", la[telemetryattrs.WcprofWaitReasonAttr])
		}
		if _, err := strconv.ParseInt(la[telemetryattrs.WcprofWaitStartUnixNanoAttr], 10, 64); err != nil {
			t.Fatalf("wait start must be a decimal-string abs-ns: %v", la)
		}
		if _, err := strconv.ParseInt(la[telemetryattrs.WcprofWaitEndUnixNanoAttr], 10, 64); err != nil {
			t.Fatalf("wait end must be a decimal-string abs-ns: %v", la)
		}
		if l.SpanContext.SpanID() != execSpan.SpanContext().SpanID() {
			t.Fatalf("wait link must target the call_exec span")
		}
	}

	// (2) end-to-end: compile the REAL exported spans through the Chunk 1 loader.
	c, err := wcotel.Compile(toWcotelSpans(ended))
	if err != nil {
		t.Fatalf("compile real emit: %v", err)
	}
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gate := wcotel.CheckStructural(c, g, wcotel.GateOptions{})
	if err := gate.Err(); err != nil {
		t.Fatalf("structural gate must pass on the real emit: %v", err)
	}
	if gate.WaitEdges != 2 {
		t.Fatalf("want 2 compiled wait edges, got %d", gate.WaitEdges)
	}
	if gate.UnresolvedWaitTargets != 0 || gate.MalformedWaitTimings != 0 {
		t.Fatalf("real emit must resolve every target with valid timing: unresolved=%d malformed=%d",
			gate.UnresolvedWaitTargets, gate.MalformedWaitTimings)
	}
	// op-kind classification fires off the real wcprof.op.kind attributes.
	if k := opKindForClass(g, class, wcprof.OpKindCallExec.String()); k != wcprof.OpKindCallExec.String() {
		t.Fatalf("call_exec span must classify as call_exec, got %q", k)
	}
	if k := opKindForClass(g, publishResultSpanName, wcprof.OpKindInternal.String()); k != wcprof.OpKindInternal.String() {
		t.Fatalf("publishResult span must classify as internal, got %q", k)
	}
}

// opKindForClass returns the kind of the op with the given class whose kind
// matches want (there can be two ops for one class — a caller "call" and the
// "call_exec" — so disambiguate by the wanted kind).
func opKindForClass(g *wcanalyze.Graph, class, want string) string {
	for _, op := range g.Ops {
		if op.Class == class && op.Kind == want {
			return op.Kind
		}
	}
	return ""
}

// TestEmitWaitGateObservableOnMissingTarget covers review finding A: a *recording*
// waiter that joins an execution whose call_exec span was never minted (a mixed /
// cross-session untraced executor) must NOT drop its wait edge silently. The fix
// emits a targetless link the loader counts as an unresolved wait, so the §6.1
// gate fails loud — mirroring native's targetless wcprof.BeginWait.
func TestEmitWaitGateObservableOnMissingTarget(t *testing.T) {
	sr, rootCtx, root := newRecordingRoot("POST /query")
	base := time.Now().UnixNano()
	// invalid target = the executor minted no call_exec span (oc.execSpanCtx zero).
	emitOTelCallWait(rootCtx, trace.SpanContext{}, wcprof.WaitReasonSingleflight, base, base+1_000_000)
	root.End()
	ended := sr.Ended()

	rootSpan := spanByName(t, ended, "POST /query")
	if len(rootSpan.Links()) != 1 {
		t.Fatalf("a missing target on a recording waiter must still emit a gate-observable wait link, got %d links", len(rootSpan.Links()))
	}

	c, err := wcotel.Compile(toWcotelSpans(ended))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if c.UnresolvedWaitTargets != 1 {
		t.Fatalf("a missing wait target must count as 1 unresolved wait, got %d", c.UnresolvedWaitTargets)
	}
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := wcotel.CheckStructural(c, g, wcotel.GateOptions{}).Err(); err == nil {
		t.Fatal("a recording waiter on a missing target must FAIL the structural gate, not drop silently")
	}
}

// TestEmitWaitNonRecordingWaiterDrops confirms the telemetry-off path stays a
// no-op: with no recording waiter span there is no op in the graph to attribute
// the wait to, so nothing is emitted (and the call must not panic).
func TestEmitWaitNonRecordingWaiterDrops(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.NeverSample()),
		sdktrace.WithSpanProcessor(sr),
	)
	// a NeverSample span is non-recording.
	ctx, span := tp.Tracer("wcprof-otel-test").Start(context.Background(), "untraced")
	if span.IsRecording() {
		t.Fatal("precondition: NeverSample span should be non-recording")
	}
	// Use a valid target to prove it is the waiter's non-recording state, not a
	// missing target, that suppresses emission.
	target := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01},
		SpanID:     trace.SpanID{0x02},
		TraceFlags: trace.FlagsSampled,
	})
	emitOTelCallWait(ctx, target, wcprof.WaitReasonSingleflight, 1, 2)
	span.End()
	if got := len(sr.Ended()); got != 0 {
		t.Fatalf("non-recording waiter must export no span/link, got %d ended", got)
	}
}
