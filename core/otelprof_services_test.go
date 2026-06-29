package core

// Emit-path regression test for the service start emit (design §3.4), mirroring
// dagql's otelprof_hooks_test.go discipline: drive the REAL emit helpers
// (beginOTelServiceStart + dagql.EmitOTelWait) against an in-memory SDK tracer,
// then feed the genuinely-exported spans through the Chunk 1 loader + structural
// gate — so the service.start span shape and the installer wait edge the wcotel
// fixtures assume are machine-checked against what core actually emits.

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

	"github.com/dagger/dagger/dagql"
	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
)

func otelprofRecordingRoot(name string) (*tracetest.SpanRecorder, context.Context, trace.Span) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sr),
	)
	ctx, root := tp.Tracer("wcprof-otel-test").Start(context.Background(), name)
	return sr, ctx, root
}

func otelprofToWcotelSpans(ended []sdktrace.ReadOnlySpan) []wcotel.Span {
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
			links = append(links, wcotel.Link{SpanID: l.SpanContext.SpanID().String(), Attrs: kvMap(l.Attributes)})
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

// otelprofMarkSpansComplete stamps the engine completeness checksum onto
// SDK-emitted spans the way the engine's per-client span-count processor does (and
// loader_test's markComplete does for JSONL fixtures): every span is a counted
// engine span, and the exact total is declared on the first one. Without it the
// fail-by-default completeness gate (the leaf-drop checksum, a later effort)
// refuses this in-memory trace, which carries no marker — independent of the
// service-start emit under test here.
func otelprofMarkSpansComplete(spans []wcotel.Span) []wcotel.Span {
	for i := range spans {
		if spans[i].Attrs == nil {
			spans[i].Attrs = map[string]any{}
		}
		spans[i].Attrs[telemetryattrs.WcprofEngineSpanAttr] = true
	}
	if len(spans) > 0 {
		spans[0].Attrs[telemetryattrs.WcprofSessionSpanCountAttr] = strconv.Itoa(len(spans))
	}
	return spans
}

func otelprofSpanByName(t *testing.T, ended []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range ended {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no exported span named %q", name)
	return nil
}

func otelprofAttrStr(s sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

func otelprofAttrBool(s sdktrace.ReadOnlySpan, key string) bool {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsBool()
		}
	}
	return false
}

// TestEmitServiceStartProducesLoaderShape drives the real service-start emit (a
// first installer that mints + ends the service.start span, plus a joining
// installer that blocks on it and emits a service wait edge), asserts the
// service.start span and the wait link carry exactly the attributes the wcotel
// fixtures assume, and compiles the genuinely-exported spans through the Chunk 1
// loader + gate with the wait resolving to the service.start op.
func TestEmitServiceStartProducesLoaderShape(t *testing.T) {
	const digest = "sha256:service-digest"

	sr, ctx, root := otelprofRecordingRoot("POST /query")

	// installer1 runs the start: service.start is minted under its span (svcCtx
	// carries the installer span in startWithKey).
	inst1Ctx, inst1 := Tracer(ctx).Start(ctx, "Container.asService")
	_, startSpan := beginOTelServiceStart(inst1Ctx, digest)

	// installer2 joins the in-flight start and blocks on it, crediting the blocked
	// interval to the service.start span. A small real sleep keeps the wait window
	// within the live spans' timeframe (no negative rebased times).
	inst2Ctx, inst2 := Tracer(ctx).Start(ctx, "Container.withServiceBinding")
	waitStart := time.Now().UnixNano()
	time.Sleep(3 * time.Millisecond)
	waitEnd := time.Now().UnixNano()
	dagql.EmitOTelWait(inst2Ctx, startSpan.SpanContext(), wcprof.WaitReasonService, waitStart, waitEnd)

	var nilErr error
	endOTelServiceStart(startSpan, &nilErr)
	inst2.End()
	inst1.End()
	root.End()
	ended := sr.Ended()

	// (1) service.start span shape.
	start := otelprofSpanByName(t, ended, "service.start")
	if got, _ := otelprofAttrStr(start, telemetryattrs.WcprofOpKindAttr); got != wcprof.OpKindServiceStart.String() {
		t.Fatalf("service.start op kind = %q, want service_start", got)
	}
	if got, _ := otelprofAttrStr(start, telemetry.DagDigestAttr); got != digest {
		t.Fatalf("service.start dag.digest = %q, want %q", got, digest)
	}
	if !otelprofAttrBool(start, telemetry.UIPassthroughAttr) {
		t.Fatal("service.start must be ui.passthrough (the visible service span is the long-lived exec span)")
	}

	// (2) the joining installer carries the wait edge to service.start.
	var inst2Span sdktrace.ReadOnlySpan
	for _, s := range ended {
		if s.SpanContext().SpanID() == inst2.SpanContext().SpanID() {
			inst2Span = s
		}
	}
	if inst2Span == nil {
		t.Fatal("installer2 span not exported")
	}
	if len(inst2Span.Links()) != 1 {
		t.Fatalf("the blocked installer must carry exactly 1 service wait link, got %d", len(inst2Span.Links()))
	}
	link := inst2Span.Links()[0]
	la := map[string]string{}
	for _, kv := range link.Attributes {
		la[string(kv.Key)] = kv.Value.AsString()
	}
	if la[telemetry.LinkPurposeAttr] != telemetryattrs.LinkPurposeWait {
		t.Fatalf("service wait link missing %s=wait: %v", telemetry.LinkPurposeAttr, la)
	}
	if la[telemetryattrs.WcprofWaitReasonAttr] != wcprof.WaitReasonService.String() {
		t.Fatalf("service wait reason = %q, want service", la[telemetryattrs.WcprofWaitReasonAttr])
	}
	if link.SpanContext.SpanID() != startSpan.SpanContext().SpanID() {
		t.Fatal("the service wait link must target the service.start span")
	}

	// (3) end-to-end: compile the REAL exported spans through the Chunk 1 loader +
	// gate; the wait must resolve to the service.start op (Invariant T).
	c, err := wcotel.Compile(otelprofMarkSpansComplete(otelprofToWcotelSpans(ended)))
	if err != nil {
		t.Fatalf("compile real emit: %v", err)
	}
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	gate := wcotel.CheckStructural(c, g, wcotel.GateOptions{})
	if err := gate.Err(); err != nil {
		t.Fatalf("structural gate must pass on the real service-start emit: %v", err)
	}
	if gate.WaitEdges != 1 {
		t.Fatalf("want 1 compiled service wait edge, got %d", gate.WaitEdges)
	}
	if gate.UnresolvedWaitTargets != 0 || gate.MalformedWaitTimings != 0 {
		t.Fatalf("the service wait must resolve to service.start with valid timing: unresolved=%d malformed=%d",
			gate.UnresolvedWaitTargets, gate.MalformedWaitTimings)
	}
	var startOp *wcanalyze.Op
	for _, op := range g.Ops {
		if op.Kind == wcprof.OpKindServiceStart.String() {
			startOp = op
		}
	}
	if startOp == nil {
		t.Fatal("no loaded service_start op")
	}
}
