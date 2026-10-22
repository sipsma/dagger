package core

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/dagger/dagger/dagql"
)

// BenchmarkStampedCall times one cache-hit call whose span records, from
// AroundFunc starting the span and arming the cache-evidence carrier, through
// GetOrInitCall deriving the call's identities (self, pairing and shape
// digests) and hitting, to the done callback stamping and ending the span. Its
// argv is BenchmarkCallShape's withExec-24, so the shape pass's ns/op there
// reads as a share of a stamped call. The engine's log lines are discarded, as
// formatting them to a test's stderr would inflate the call and shrink that
// share.
func BenchmarkStampedCall(b *testing.B) {
	previous, previousWriter, previousFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	b.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, root := tp.Tracer("stamped-call-bench").Start(context.Background(), "root")
	defer root.End()
	cache, err := dagql.NewCache(ctx, "", nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cache.CloseDiscardingPersistence() })
	ctx = dagql.ContextWithCache(ctx, cache)

	frame := evidenceTestFrame("withExec")
	argv := &dagql.ResultCallLiteral{Kind: dagql.ResultCallLiteralKindList}
	for i := range 24 {
		argv.ListItems = append(argv.ListItems, &dagql.ResultCallLiteral{Kind: dagql.ResultCallLiteralKindString, StringValue: fmt.Sprintf("--flag-%d=value-%d", i, i)})
	}
	frame.Args = []*dagql.ResultCallArg{{Name: "args", Value: argv}}
	fn := func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewResultForCall(dagql.NewInt(1), frame)
	}
	if _, err := cache.GetOrInitCall(ctx, "bench-session", evidenceTestTypeResolver{}, &dagql.CallRequest{ResultCall: frame}, fn); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		req := &dagql.CallRequest{ResultCall: frame, ReceiverTypeName: "Container"}
		callCtx, done := AroundFunc(ctx, req)
		if req.CacheEvidence == nil {
			b.Fatal("the call's span doesn't record")
		}
		res, err := cache.GetOrInitCall(callCtx, "bench-session", evidenceTestTypeResolver{}, req, fn)
		if err != nil {
			b.Fatal(err)
		}
		if req.CacheEvidence.Outcome != dagql.CacheOutcomeHit || req.CacheEvidence.ShapeDigest == "" || req.CacheEvidence.ArgCount != 24 {
			b.Fatalf("want an armed hit with a 24-literal shape, got outcome %q shape %q count %d", req.CacheEvidence.Outcome, req.CacheEvidence.ShapeDigest, req.CacheEvidence.ArgCount)
		}
		var rerr error
		done(res, true, &rerr)
	}
}
