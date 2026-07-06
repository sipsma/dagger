package dagql

import (
	"context"
	"encoding/json"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	telemetry "github.com/dagger/otel-go"
)

// OTel emission for the wcprof × OTel profiling source (design §3.1). These
// mirror, on the engine's ordinary OTel spans, the shared-execution op and the
// per-caller wait edges the native wcprof recorder records inline
// (wcprof_hooks.go) — so the offline loader (engine/wcprof/wcotel) can compile a
// Dagger Cloud trace into the same wcprof IR and the unchanged replay can rank
// wall-clock bottlenecks.
//
// Unlike the native hooks, these are gated only on telemetry being active, NOT
// on wcprof.Enabled: the OTel source must be reconstructable from a Cloud trace
// alone, with no native recorder running, so the call_exec span / wait links /
// publishResult span are part of the engine's normal telemetry whenever a
// recording span is present. The cost is bounded to executed calls (cache
// misses) — two extra passthrough spans plus one tiny link per caller that
// blocked (design §3.1, §4.1); cache hits emit nothing new.

const publishResultSpanName = "dagql.publishResult"

// OTelProfActive reports whether OTel profiling spans should be emitted for work
// under ctx: true exactly when ctx carries a live recording span (the engine's
// telemetry is on). Mirrors how core.AroundFunc only emits under an active
// tracer and keeps the telemetry-off path allocation-free.
//
// Exported so the choke points that live outside this package can gate on the
// same condition: the executor exec-split (engine/engineutil, design §3.3) and
// service start (core, design §3.4). One definition keeps "is the OTel source
// recording here?" answered identically everywhere.
func OTelProfActive(ctx context.Context) bool {
	return trace.SpanFromContext(ctx).IsRecording()
}

// beginOTelCallExec starts the call_exec span for a resolver execution on the
// call's detached context, mirroring native's execOp (cache.go getOrInitCall).
// The returned context carries the span, so the resolver's sub-call spans nest
// under it regardless of whether AroundFunc emitted (or suppressed) a caller
// span — fixing Break #3 structurally (design §3.1). The span is marked
// ui.passthrough so dagui keeps showing the caller span, not this internal one;
// its name is the call class (native's execOp class) so the cross-source
// oracle's per-class table lines up. The caller ends the returned span when the
// resolver finishes.
//
// Invariant T (design §3.0.1): the caller mints this span under callsMu, before
// publishing the ongoingCall, and stashes its SpanContext there — so every
// joiner that observes the ongoingCall has a valid wait target.
func beginOTelCallExec(callCtx context.Context, callKey, class string) (context.Context, trace.Span) {
	return Tracer(callCtx).Start(callCtx, class,
		telemetry.Passthrough(),
		trace.WithAttributes(
			attribute.String(telemetryattrs.WcprofOpKindAttr, wcprof.OpKindCallExec.String()),
			attribute.String(telemetry.DagDigestAttr, callKey),
		),
	)
}

// beginOTelPublishResult starts the dagql.publishResult span as a child of the
// call_exec span carried by ctx (design §3.1). It is a native-parity diagnostic,
// not a counterfactual-attribution fix: publication runs after call_exec and the
// caller wait both close, so the replay charges it to the caller class — but
// native emits the same row, so the OTel per-class table must too. The caller
// passes a context derived from the shared-work context (which carries the
// already-ended call_exec span) and ends the returned span when publication
// finishes.
func beginOTelPublishResult(ctx context.Context) trace.Span {
	_, span := Tracer(ctx).Start(ctx, publishResultSpanName,
		telemetry.Passthrough(),
		trace.WithAttributes(
			attribute.String(telemetryattrs.WcprofOpKindAttr, wcprof.OpKindInternal.String()),
		),
	)
	return span
}

// EmitOTelForced records, as a span link on the forcer's current span, that
// the forcer demanded an ALREADY-COMPLETE lazy result (the Evaluate fast
// path; whatif-cached lazy-semantics §4.4) — the zero-duration fact the fast
// path otherwise erases. target is the completing lazy span when this engine
// run recorded it (an invalid zero context when production predated
// recording — the digest attribute still carries the fact, and the SDK
// retains attributed links regardless). Never gates anything; consumed only
// by the offline what-if-cached keep test.
func EmitOTelForced(ctx context.Context, target trace.SpanContext, producerDigest string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.AddLink(trace.Link{
		SpanContext: target,
		Attributes: []attribute.KeyValue{
			attribute.String(telemetry.LinkPurposeAttr, telemetryattrs.LinkPurposeForced),
			attribute.String(telemetryattrs.WcprofForcedDigestAttr, producerDigest),
		},
	})
}

// stampOTelCallOutcome stamps the caller's current span with the call's cache
// outcome at the point the engine decides it — the outcomes ordinary
// telemetry cannot distinguish ("executed", "joined", "do_not_cache"; design
// decision #5, batched into what-if-cached Chunk 4). Hits and failures are
// NOT stamped: the standard cached attribute and the span status carry them
// at span end, and the loader treats those as authoritative over this
// mid-call stamp (a call stamped "executed" that later fails loads as the
// failure, matching native's error-over-hint outcome rule).
func stampOTelCallOutcome(ctx context.Context, outcome wcprof.Outcome) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(attribute.String(telemetryattrs.WcprofCallOutcomeAttr, outcome.String()))
}

// stampOTelOrderedInputs stamps the E3a ordered-input parity attr on the
// current call span: the NATIVE-PARITY ordered structural input vector
// (module ref included) as the same canonical scalar JSON-array string the
// native recorder interns (both sides json.Marshal the identical slice) —
// byte-identical, so positional pairing means the same thing against either
// source. Gated on the wcprof OTel source being active: this attr exists
// for the profiling pipeline, and the marshal must not ride on unprofiled
// runs.
func stampOTelOrderedInputs(ctx context.Context, inputs []string) {
	if !OTelProfActive(ctx) {
		return
	}
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	b, err := json.Marshal(inputs)
	if err != nil {
		return
	}
	span.SetAttributes(attribute.String(telemetryattrs.WcprofInputsOrderedAttr, string(b)))
}

// stampOTelLookupOutcome stamps the E1 lookup-outcome fact on the current
// call span (the canonical wcprof.EncodeLookupOutcome encoding) — the
// additive OTel attr half of the emit; the native half is
// Op.SetLookupOutcome. One value per call span: a call performs at most one
// lookup.
func stampOTelLookupOutcome(ctx context.Context, encoded string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(attribute.String(telemetryattrs.WcprofLookupOutcomeAttr, encoded))
}

// stampOTelLookupOutcomeLink records the E1 fact for a DIGEST-ONLY lookup
// (which has no call span of its own) as a targetless link on the current
// span, carrying the looked-up digest and the canonical encoding — the same
// exact-tally link shape the suppression counter uses (a lost mark shows as
// a dropped link, gated).
func stampOTelLookupOutcomeLink(ctx context.Context, digestStr, encoded string) {
	if !OTelProfActive(ctx) {
		return
	}
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.AddLink(trace.Link{
		Attributes: []attribute.KeyValue{
			attribute.String(telemetry.LinkPurposeAttr, telemetryattrs.LinkPurposeLookupOutcome),
			attribute.String(telemetryattrs.WcprofLookupDigestAttr, digestStr),
			attribute.String(telemetryattrs.WcprofLookupOutcomeAttr, encoded),
		},
	})
}

// stampOTelSuppressedIdent records, on the current span, that a lazy
// evaluation under it could not derive its producer digest, so the ident and
// any forced fact were omitted — the OTel half of the suppression counter.
// One targetless span LINK per firing: the loader's tally is then exact
// (doctrine: every firing counted), and a lost mark is visible as a dropped
// link, which the structural gate refuses (a span attr would drop with no
// loss signal). The SDK retains invalid-context links that carry attributes,
// the same behavior the unresolved forced-fact link relies on.
func stampOTelSuppressedIdent(ctx context.Context) {
	if !OTelProfActive(ctx) {
		return
	}
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.AddLink(trace.Link{
		Attributes: []attribute.KeyValue{
			attribute.String(telemetry.LinkPurposeAttr, telemetryattrs.LinkPurposeSuppressedIdent),
		},
	})
}

// EmitOTelWait records, as a span link on the waiter's current span, that the
// waiter blocked on a target op over [startNS,endNS] (design §3.0) — the OTel
// analog of native's wcprof.BeginWait. It is shared by every choke point that
// blocks on shared work: the cache singleflight (reason "call_exec"/"singleflight",
// §3.1), lazy evaluation (reason "lazy", §3.2) and service start (reason
// "service", §3.4 — emitted from core, hence exported). The waiter is the current
// span in ctx: the caller's own span, or — if that caller was telemetry-suppressed —
// the ancestor span that actually blocked, which is the correct place for the
// time to land. Attaching to the waiter (never fanning links onto the target) is
// what keeps a high-fan-in target under the link cap (design §3.0). One
// implementation so every source's wait edge is byte-identical on the wire and
// the loader/gate read them uniformly.
//
// Timestamps are absolute Unix nanoseconds as decimal strings: the engine only
// knows wall-clock at emit time (the trace epoch is unknowable until ingest, so
// the loader rebases), and decimal strings round-trip exactly through Cloud's
// map[string]any JSON decode where a number would lose precision above 2^53.
func EmitOTelWait(ctx context.Context, target trace.SpanContext, reason wcprof.WaitReason, startNS, endNS int64) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		// No recording waiter to attach the edge to: this caller has no op in the
		// loaded graph, so there is no self-time to over-credit and nothing to
		// under-serialize. Telemetry-off path; allocation-free.
		return
	}
	// Attach the wait edge even when target is invalid. In the always-on model
	// the work owner and every waiter record uniformly, so a recording waiter's
	// target (oc.execSpanCtx for call_exec, shared.lazyEvalSpanCtx for lazy) is
	// always valid (Invariant T). The only way it is invalid here is a non-uniform
	// / mixed-recording trace — e.g. a recording waiter joining shared work started
	// by an *untraced* session (ongoingCalls / lazy state are shared across
	// sessions, not session-keyed). We must not drop the edge silently: a
	// never-emitted wait is the under-serialization the
	// §6.1 gate exists to catch. Emitting it with a zero target still carries
	// attributes, so the SDK retains the link (recordingSpan.AddLink keeps any
	// attributed link), the loader resolves no target and counts an unresolved
	// wait, and the structural gate fails loud — exactly mirroring native, whose
	// targetless wcprof.BeginWait the gate also sees as unresolved. Such a trace
	// mixes recorded and unrecorded in-flight work and cannot be faithfully
	// analyzed anyway, so failing loud is the correct outcome.
	span.AddLink(trace.Link{
		SpanContext: target,
		Attributes: []attribute.KeyValue{
			attribute.String(telemetry.LinkPurposeAttr, telemetryattrs.LinkPurposeWait),
			attribute.String(telemetryattrs.WcprofWaitReasonAttr, reason.String()),
			attribute.String(telemetryattrs.WcprofWaitStartUnixNanoAttr, strconv.FormatInt(startNS, 10)),
			attribute.String(telemetryattrs.WcprofWaitEndUnixNanoAttr, strconv.FormatInt(endNS, 10)),
		},
	})
}
