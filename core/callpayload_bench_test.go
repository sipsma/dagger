package core

import (
	"context"
	"testing"

	telemetry "github.com/dagger/otel-go"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/dagger/dagger/dagql"
)

// Timing harness for the call payload walk's no-failure path. The A and B
// sides of the series share this file verbatim.

func benchPayloadCtx() context.Context {
	ctx := telemetry.WithLoggerProvider(context.Background(), sdklog.NewLoggerProvider())
	return dagql.ContextWithCache(ctx, nil)
}

// BenchmarkCallPayloadExtendCoveredChain extends a 200-step chain whose
// closure an earlier walk claimed by one new call per op: the path #14578
// made O(1) per call.
func BenchmarkCallPayloadExtendCoveredChain(b *testing.B) {
	ctx := benchPayloadCtx()
	const depth = 200
	frames := chainCall(depth)
	prefix := frames[depth-1]
	keys := newTestClosureKeys()
	prefixDigest, err := prefix.RecipeDigest(ctx)
	if err != nil {
		b.Fatal(err)
	}
	recordCallPayloads(ctx, keys, prefixDigest.String(), prefix)
	i := 0
	for b.Loop() {
		top := testResultCall("top", &Void{}, prefix)
		top.Args = []*dagql.ResultCallArg{{
			Name:  "n",
			Value: &dagql.ResultCallLiteral{Kind: dagql.ResultCallLiteralKindInt, IntValue: int64(i)},
		}}
		i++
		topDigest, err := top.RecipeDigest(ctx)
		if err != nil {
			b.Fatal(err)
		}
		recordCallPayloads(ctx, keys, topDigest.String(), top)
	}
}
