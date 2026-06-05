package parallel

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestRunEmitsFanoutGroupSpan(t *testing.T) {
	spanRecorder, ctx, rootSpan := setupParallelTelemetry(t)

	err := New().
		WithLimit(1).
		WithJob("one", nil).
		WithJob("two", nil).
		Run(ctx)
	rootSpan.End()
	require.NoError(t, err)

	spans := spanRecorder.Ended()
	groupSpan := requireParallelSpanNamed(t, spans, "parallel jobs")
	oneSpan := requireParallelSpanNamed(t, spans, "one")
	twoSpan := requireParallelSpanNamed(t, spans, "two")

	require.Equal(t, rootSpan.SpanContext().SpanID(), groupSpan.Parent().SpanID())
	require.Equal(t, groupSpan.SpanContext().SpanID(), oneSpan.Parent().SpanID())
	require.Equal(t, groupSpan.SpanContext().SpanID(), twoSpan.Parent().SpanID())
	require.Equal(t, "parallel_jobs", requireParallelSpanStringAttr(t, groupSpan, fanoutKindAttr))
	require.Equal(t, int64(2), requireParallelSpanIntAttr(t, groupSpan, fanoutCountAttr))
	require.Equal(t, "wait_all", requireParallelSpanStringAttr(t, groupSpan, fanoutJoinAttr))
	require.Equal(t, int64(1), requireParallelSpanIntAttr(t, groupSpan, fanoutLimitAttr))
	require.Equal(t, true, requireParallelSpanBoolAttr(t, groupSpan, uiInternalAttr))
}

func TestRunEmitsFailFastFanoutGroupSpan(t *testing.T) {
	spanRecorder, ctx, rootSpan := setupParallelTelemetry(t)
	expectedErr := errors.New("boom")

	err := New().
		WithFailFast(true).
		WithJob("fail", func(context.Context) error {
			return expectedErr
		}).
		WithJob("also-started", nil).
		Run(ctx)
	rootSpan.End()
	require.ErrorIs(t, err, expectedErr)

	groupSpan := requireParallelSpanNamed(t, spanRecorder.Ended(), "parallel jobs")
	require.Equal(t, "parallel_jobs", requireParallelSpanStringAttr(t, groupSpan, fanoutKindAttr))
	require.Equal(t, int64(2), requireParallelSpanIntAttr(t, groupSpan, fanoutCountAttr))
	require.Equal(t, "fail_fast", requireParallelSpanStringAttr(t, groupSpan, fanoutJoinAttr))
	require.Equal(t, codes.Error, groupSpan.Status().Code)
}

func TestRunSkipsFanoutGroupSpanForSingleJob(t *testing.T) {
	spanRecorder, ctx, rootSpan := setupParallelTelemetry(t)

	err := New().
		WithJob("one", nil).
		Run(ctx)
	rootSpan.End()
	require.NoError(t, err)

	require.Empty(t, parallelSpansNamed(spanRecorder.Ended(), "parallel jobs"))
	require.Len(t, parallelSpansNamed(spanRecorder.Ended(), "one"), 1)
}

func setupParallelTelemetry(t *testing.T) (*tracetest.SpanRecorder, context.Context, trace.Span) {
	t.Helper()

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	previousTracerProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tracerProvider)
	t.Cleanup(func() {
		require.NoError(t, tracerProvider.Shutdown(context.Background()))
		otel.SetTracerProvider(previousTracerProvider)
	})

	ctx, rootSpan := tracerProvider.Tracer("dagger.io/test").Start(context.Background(), "root")
	return spanRecorder, ctx, rootSpan
}

func parallelSpansNamed(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	var matched []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == name {
			matched = append(matched, span)
		}
	}
	return matched
}

func requireParallelSpanNamed(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	matched := parallelSpansNamed(spans, name)
	require.Len(t, matched, 1)
	return matched[0]
}

func requireParallelSpanStringAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) string {
	t.Helper()
	return requireParallelSpanAttr(t, span, key).AsString()
}

func requireParallelSpanIntAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) int64 {
	t.Helper()
	return requireParallelSpanAttr(t, span, key).AsInt64()
}

func requireParallelSpanBoolAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) bool {
	t.Helper()
	return requireParallelSpanAttr(t, span, key).AsBool()
}

func requireParallelSpanAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) attribute.Value {
	t.Helper()
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value
		}
	}
	require.Failf(t, "span attribute not found", "missing attr %q on span %q", key, span.Name())
	return attribute.Value{}
}
