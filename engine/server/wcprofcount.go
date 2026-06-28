package server

import (
	"context"
	"strconv"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/dagger/dagger/engine/telemetryattrs"
)

// wcprofSpanCounter is the producer half of the wcprof completeness checksum
// (design §6.1, leaf-drop detection). A span the engine drops on the way to Cloud
// is undetectable from the trace if it is a LEAF (it breaks no parent/wait edge),
// so the reference-based structural gate cannot catch it; the engine therefore
// DECLARES how many spans it emitted and the loader refuses a trace that received
// fewer.
//
// It is an sdktrace.SpanProcessor registered on EVERY per-client tracer provider
// (main and nested module-runtime clients), all sharing this one instance, so the
// per-trace count accumulates across the whole nested-client tree of a session. At
// span creation it (a) marks the span WcprofEngineSpanAttr — the counted
// population, which lets the loader exclude CLI-shell and otelhttp/buildkit spans
// the engine never counts — and (b) increments the trace's MONOTONIC count.
//
// A single command issues MANY main-client queries (introspection, schema, the
// workload) under ONE trace, so the count must NOT be reset per query. Each main
// query's handler calls Stamp at its end to write the RUNNING total onto its own
// session-root (POST /query) span; the loader takes the MAX such marker, which is
// the final total once the last query has stamped. The session-teardown handler
// calls Reap once to drop the per-trace entry (otherwise the map would grow one int
// per trace for the engine's lifetime).
//
// Scope (the engine-vs-CLI population decision, design point 4): N counts ENGINE
// spans only (the ranking-critical class: call/call_exec/exec/lazy/service +
// user-work + module-load). CLI-shell spans are out of scope and unmarked, so they
// never skew the reconciliation. Engine spans created AFTER the last query's Stamp
// (session shutdown, async service-availability) are a documented residual: they
// are excluded from N and can only make received >= N (never a false fail); they
// cannot mask a synchronous drop in the common case.
type wcprofSpanCounter struct {
	mu     sync.Mutex
	counts map[trace.TraceID]int
}

func newWcprofSpanCounter() *wcprofSpanCounter {
	return &wcprofSpanCounter{counts: map[trace.TraceID]int{}}
}

func (c *wcprofSpanCounter) OnStart(_ context.Context, s sdktrace.ReadWriteSpan) {
	tid := s.SpanContext().TraceID()
	if !tid.IsValid() {
		return
	}
	// Mark the engine population (one cheap bool attr); the loader counts these.
	s.SetAttributes(attribute.Bool(telemetryattrs.WcprofEngineSpanAttr, true))
	c.mu.Lock()
	c.counts[tid]++
	c.mu.Unlock()
}

func (c *wcprofSpanCounter) OnEnd(sdktrace.ReadOnlySpan)      {}
func (c *wcprofSpanCounter) Shutdown(context.Context) error   { return nil }
func (c *wcprofSpanCounter) ForceFlush(context.Context) error { return nil }

// Stamp writes the RUNNING engine span total for ctx's trace onto the current span
// (a session-root POST /query span, which carries the marker for the loader to
// read). The main query handler calls it at query end, when every synchronous
// engine span up to now — this client and its nested-client subtree, all sharing
// the trace — has been created and counted. The count is NOT reset (see Reap): a
// later query stamps a larger running total and the loader keeps the max, so the
// final stamp wins. SetAttributes on the still-open session-root span is carried on
// its end-export.
func (c *wcprofSpanCounter) Stamp(ctx context.Context) {
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()
	if !sc.IsValid() || !span.IsRecording() {
		return
	}
	tid := sc.TraceID()
	c.mu.Lock()
	n := c.counts[tid]
	c.mu.Unlock()
	if n == 0 {
		return
	}
	// String-encoded like the other wcprof numeric attrs, so it survives Cloud's
	// JSON without float64 coercion (design §3.0/§6.6).
	span.SetAttributes(attribute.String(telemetryattrs.WcprofSessionSpanCountAttr, strconv.Itoa(n)))
}

// Reap drops the per-trace counter entry. The session-teardown handler calls it
// once the session (hence the trace) is done, bounding the map to live traces.
func (c *wcprofSpanCounter) Reap(tid trace.TraceID) {
	if !tid.IsValid() {
		return
	}
	c.mu.Lock()
	delete(c.counts, tid)
	c.mu.Unlock()
}
