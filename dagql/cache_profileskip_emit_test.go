package dagql

import (
	"context"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/stretchr/testify/require"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
)

// countKind returns how many exported spans carry wcprof.op.kind == kind.
func countOTelKind(ended []wcotel.Span, kind string) int {
	n := 0
	for _, s := range ended {
		if k, ok := s.Attrs[telemetryattrs.WcprofOpKindAttr].(string); ok && k == kind {
			n++
		}
	}
	return n
}

func countOTelName(ended []wcotel.Span, name string) int {
	n := 0
	for _, s := range ended {
		if s.Name == name {
			n++
		}
	}
	return n
}

// TestProfileSkipGatesSingleflightEmit drives the REAL cache singleflight path
// (GetOrInitCall) under a recording span and asserts the frame-homed ProfileSkip
// bit gates the OTel call_exec + publishResult spans: a skipped reflection-class
// miss emits neither, a kept user-work miss emits both, and the loaded graph's
// structural gate stays clean (0 orphans / 0 unresolved waits) in both cases.
func TestProfileSkipGatesSingleflightEmit(t *testing.T) {
	sr, rootCtx, root := newRecordingRoot("POST /query")
	ctx := cacheTestContext(rootCtx)
	c, err := NewCache(ctx, "", nil, nil)
	require.NoError(t, err)
	ctx = ContextWithCache(ctx, c)

	// Skipped reflection-class miss: ProfileSkip set on the frame (what AroundFunc
	// stamps). Expect NO call_exec / publishResult span.
	skipCall := cacheTestIntCall("functions")
	skipCall.ProfileSkip = true
	_, err = c.GetOrInitCall(ctx, "test-session", noopTypeResolver{},
		&CallRequest{ResultCall: skipCall, ConcurrencyKey: "test-session"},
		func(context.Context) (AnyResult, error) {
			return cacheTestDetachedResult(skipCall, NewInt(1)), nil
		})
	require.NoError(t, err)

	// Kept user-work miss: ProfileSkip false. Expect a call_exec + publishResult.
	keepCall := cacheTestIntCall("withExec")
	_, err = c.GetOrInitCall(ctx, "test-session", noopTypeResolver{},
		&CallRequest{ResultCall: keepCall, ConcurrencyKey: "test-session"},
		func(context.Context) (AnyResult, error) {
			return cacheTestDetachedResult(keepCall, NewInt(2)), nil
		})
	require.NoError(t, err)

	root.End()
	spans := toWcotelSpans(sr.Ended())

	// Exactly one call_exec (the kept miss), named for the kept call; none for the
	// skipped reflection class.
	require.Equal(t, 1, countOTelKind(spans, wcprof.OpKindCallExec.String()), "only the kept miss emits call_exec")
	require.Equal(t, 1, countOTelName(spans, "Query.withExec"), "kept call_exec present")
	require.Equal(t, 0, countOTelName(spans, "Query.functions"), "skipped reflection miss must emit no call_exec")
	// Exactly one publishResult (the kept miss); skipped publishResult follows the
	// invalid call_exec span context for free.
	require.Equal(t, 1, countOTelName(spans, "dagql.publishResult"), "only the kept miss emits publishResult")

	// The loaded graph's structural gate must be clean for the mixed capture.
	cc, err := wcotel.Compile(spans)
	require.NoError(t, err)
	g, err := wcanalyze.Build(cc.Header, cc.Events)
	require.NoError(t, err)
	require.NoError(t, wcotel.CheckStructural(cc, g, wcotel.GateOptions{}).Err(), "structural gate must pass")
}

// TestProfileSkipStaticCutSingleflightJoiner exercises the load-bearing property:
// because the cut is STATIC per recipe, a joiner of a skipped key is itself
// skipped, so the executor mints no call_exec and the joiner emits no wait — the
// gate stays 0/0 with no dangling singleflight wait target. (A dynamic per-caller
// cut could leave a non-skipped joiner waiting on a skipped target; this asserts we
// do not.)
func TestProfileSkipStaticCutSingleflightJoiner(t *testing.T) {
	sr, rootCtx, root := newRecordingRoot("POST /query")
	ctx := cacheTestContext(rootCtx)
	c, err := NewCache(ctx, "", nil, nil)
	require.NoError(t, err)
	ctx = ContextWithCache(ctx, c)

	fnEntered := make(chan struct{})
	fnRelease := make(chan struct{})

	// Executor: a skipped reflection-class call that parks inside fn so a joiner can
	// attach to the same ongoing call.
	execCall := cacheTestIntCall("functions")
	execCall.ProfileSkip = true
	execDone := make(chan error, 1)
	go func() {
		_, e := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{},
			&CallRequest{ResultCall: execCall, ConcurrencyKey: "test-session"},
			func(context.Context) (AnyResult, error) {
				close(fnEntered)
				<-fnRelease
				return cacheTestDetachedResult(execCall, NewInt(1)), nil
			})
		execDone <- e
	}()
	<-fnEntered

	// Joiner: same recipe (same digest) + concurrency key → joins the in-flight call.
	// The static cut makes it skipped too, so it emits no wait.
	joinCall := cacheTestIntCall("functions")
	joinCall.ProfileSkip = true
	joinDone := make(chan error, 1)
	go func() {
		_, e := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{},
			&CallRequest{ResultCall: joinCall, ConcurrencyKey: "test-session"},
			func(context.Context) (AnyResult, error) {
				return cacheTestDetachedResult(joinCall, NewInt(1)), nil
			})
		joinDone <- e
	}()

	close(fnRelease)
	require.NoError(t, <-execDone)
	require.NoError(t, <-joinDone)
	root.End()

	spans := toWcotelSpans(sr.Ended())
	require.Equal(t, 0, countOTelKind(spans, wcprof.OpKindCallExec.String()), "skipped executor mints no call_exec")

	cc, err := wcotel.Compile(spans)
	require.NoError(t, err)
	g, err := wcanalyze.Build(cc.Header, cc.Events)
	require.NoError(t, err)
	res := wcotel.CheckStructural(cc, g, wcotel.GateOptions{})
	require.Equal(t, 0, res.UnresolvedWaitTargets, "no dangling singleflight wait from the skipped join")
	require.Equal(t, 0, res.OrphanedParents, "no orphaned parents")
	require.NoError(t, res.Err(), "structural gate must pass for the skipped singleflight join")
}

// countOTelWaitLinks counts wait links (across all spans) carrying the given reason.
func countOTelWaitLinks(ended []wcotel.Span, reason string) int {
	n := 0
	for _, s := range ended {
		for _, l := range s.Links {
			if p, _ := l.Attrs[telemetry.LinkPurposeAttr].(string); p != telemetryattrs.LinkPurposeWait {
				continue
			}
			if r, _ := l.Attrs[telemetryattrs.WcprofWaitReasonAttr].(string); r == reason {
				n++
			}
		}
	}
	return n
}

func newLazySkipRecorder() (*tracetest.SpanRecorder, trace.Tracer) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(NewWcprofLazyParentProcessor()),
		sdktrace.WithSpanProcessor(sr),
	)
	return sr, tp.Tracer("wcprof-otel-skip-lazy-test")
}

// TestProfileSkipGatesLazyEmit drives the REAL lazy-eval path (Cache.Evaluate ->
// evaluateOne) under a recording span and asserts the producer frame's ProfileSkip
// gates the OTel lazy span + waits: a skipped producer mints no lazy OTel span (and
// no OTel lazy wait); a kept producer mints one and its waits resolve. The gate
// stays clean either way. (Native lazy ops are unaffected; wcprof is not active in
// this test, so only the OTel side is observed.)
func TestProfileSkipGatesLazyEmit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		profileSkip bool
		wantLazy    bool
	}{
		{"skipped producer emits no lazy OTel span", true, false},
		{"kept producer emits a lazy OTel span", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr, tr := newLazySkipRecorder()
			baseCtx := cacheTestContext(t.Context())
			c, err := NewCache(baseCtx, "", nil, nil)
			require.NoError(t, err)
			baseCtx = ContextWithCache(baseCtx, c)
			srv := cacheTestServer(t)
			sessionID := cacheTestSessionID(t, baseCtx)

			frame := &ResultCall{
				Kind:        ResultCallKindField,
				Type:        NewResultCallType((&cacheTestObject{}).Type()),
				Field:       "lazy-skip-test",
				ProfileSkip: tc.profileSkip,
			}
			resAny, err := c.GetOrInitCall(baseCtx, sessionID, srv, &CallRequest{ResultCall: frame},
				func(context.Context) (AnyResult, error) {
					return cacheTestObjectResultWithValue(t, srv, frame, &cacheTestObject{
						Value:    1,
						lazyEval: func(context.Context) error { return nil },
					}), nil
				})
			require.NoError(t, err)
			result := resAny.(ObjectResult[*cacheTestObject])
			require.Equal(t, tc.profileSkip, result.cacheSharedResult().loadResultCall().ProfileSkip,
				"the producer frame must carry the skip decision (frame-homing)")

			leaderCtx, leaderSpan := tr.Start(baseCtx, "lazy-leader")
			require.NoError(t, c.Evaluate(leaderCtx, result))
			leaderSpan.End()

			spans := toWcotelSpans(sr.Ended())
			lazyCount := countOTelKind(spans, wcprof.OpKindLazy.String())
			lazyWaits := countOTelWaitLinks(spans, wcprof.WaitReasonLazy.String())
			if tc.wantLazy {
				require.Greater(t, lazyCount, 0, "kept producer must emit a lazy OTel span")
				require.Greater(t, lazyWaits, 0, "kept producer's lazy waits must be emitted")
			} else {
				require.Equal(t, 0, lazyCount, "skipped producer must emit no lazy OTel span")
				require.Equal(t, 0, lazyWaits, "skipped producer must emit no lazy OTel wait")
			}
			cc, err := wcotel.Compile(spans)
			require.NoError(t, err)
			g, err := wcanalyze.Build(cc.Header, cc.Events)
			require.NoError(t, err)
			require.NoError(t, wcotel.CheckStructural(cc, g, wcotel.GateOptions{}).Err(),
				"structural gate must pass")
		})
	}
}

// TestProfileSkipLazyCrossRecipeForcerStaysClean is the load-bearing N3 case: a
// skipped producer's pending value forced by a TRACED (recording) joiner — a
// DIFFERENT recipe than the producer. The OTel lazy waits gate on the PRODUCER's
// stored flag, so the joiner emits no wait into the (deliberately) absent producer
// span and the gate stays 0/0. Were the gate keyed on the joiner's own (non-skipped)
// bit, the joiner would emit a targetless wait → UnresolvedWaitTargets.
func TestProfileSkipLazyCrossRecipeForcerStaysClean(t *testing.T) {
	sr, tr := newLazySkipRecorder()
	baseCtx := cacheTestContext(t.Context())
	c, err := NewCache(baseCtx, "", nil, nil)
	require.NoError(t, err)
	baseCtx = ContextWithCache(baseCtx, c)
	srv := cacheTestServer(t)
	sessionID := cacheTestSessionID(t, baseCtx)

	leaderReady := make(chan struct{})
	release := make(chan struct{})
	frame := &ResultCall{
		Kind:        ResultCallKindField,
		Type:        NewResultCallType((&cacheTestObject{}).Type()),
		Field:       "xrecipe-skipped-producer",
		ProfileSkip: true, // skipped producer
	}
	resAny, err := c.GetOrInitCall(baseCtx, sessionID, srv, &CallRequest{ResultCall: frame},
		func(context.Context) (AnyResult, error) {
			return cacheTestObjectResultWithValue(t, srv, frame, &cacheTestObject{
				Value: 1,
				lazyEval: func(context.Context) error {
					close(leaderReady)
					<-release
					return nil
				},
			}), nil
		})
	require.NoError(t, err)
	result := resAny.(ObjectResult[*cacheTestObject])
	shared := result.cacheSharedResult()
	require.True(t, shared.loadResultCall().ProfileSkip, "producer frame must carry the skip bit")

	// One shared root trace (the loader analyzes a single trace); leader + joiner are
	// children of it.
	rootCtx, root := tr.Start(baseCtx, "POST /query")

	// Leader (traced): parks in the callback so the joiner can join the pending eval.
	leaderCtx, leaderSpan := tr.Start(rootCtx, "lazy-leader")
	leaderDone := make(chan error, 1)
	go func() { leaderDone <- c.Evaluate(leaderCtx, result) }()
	select {
	case <-leaderReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the lazy leader to publish lazyEvalWaitCh")
	}

	// Joiner (traced) — the non-skipped forcer of the skipped producer.
	jCtx, jSpan := tr.Start(rootCtx, "lazy-joiner")
	jDone := make(chan error, 1)
	go func() { jDone <- c.Evaluate(jCtx, result) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		shared.lazyMu.Lock()
		joined := shared.lazyEvalWaiters >= 2
		shared.lazyMu.Unlock()
		if joined {
			break
		}
		require.False(t, time.Now().After(deadline), "timed out waiting for the joiner to join the eval")
		time.Sleep(time.Millisecond)
	}

	close(release)
	require.NoError(t, <-leaderDone)
	require.NoError(t, <-jDone)
	jSpan.End()
	leaderSpan.End()
	root.End()

	spans := toWcotelSpans(sr.Ended())
	require.Equal(t, 0, countOTelKind(spans, wcprof.OpKindLazy.String()),
		"skipped producer emits no lazy OTel span")
	require.Equal(t, 0, countOTelWaitLinks(spans, wcprof.WaitReasonLazy.String()),
		"a non-skipped forcer of a skipped producer must emit no lazy OTel wait")
	cc, err := wcotel.Compile(spans)
	require.NoError(t, err)
	g, err := wcanalyze.Build(cc.Header, cc.Events)
	require.NoError(t, err)
	res := wcotel.CheckStructural(cc, g, wcotel.GateOptions{})
	require.Equal(t, 0, res.UnresolvedWaitTargets, "no cross-recipe lazy dangle")
	require.NoError(t, res.Err(), "structural gate must pass for the cross-recipe forcer")
}

// TestProfileSkipDoesNotBlindInvalidTargetDetector (§8.5): a NON-skipped target
// whose call_exec span is genuinely invalid (a mixed/untraced executor) must STILL
// emit a targetless OTel wait, so the loader reports UnresolvedWaitTargets and the
// gate fails loud. Proves the skip bit did not blind the detector — the OTel wait
// gates on oc.profSkip, never on execSpanCtx validity — locking out a future
// "gate on execSpanCtx.IsValid()" refactor.
func TestProfileSkipDoesNotBlindInvalidTargetDetector(t *testing.T) {
	sr, tr := newLazySkipRecorder()
	baseCtx := cacheTestContext(t.Context())
	c, err := NewCache(baseCtx, "", nil, nil)
	require.NoError(t, err)
	baseCtx = ContextWithCache(baseCtx, c)
	srv := cacheTestServer(t)
	sessionID := cacheTestSessionID(t, baseCtx)

	leaderReady := make(chan struct{})
	release := make(chan struct{})
	// NON-skipped producer: the OTel joiner wait is NOT gated off, so it must still
	// be emitted even when the target span is invalid.
	frame := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&cacheTestObject{}).Type()),
		Field: "invalid-target-lazy",
	}
	resAny, err := c.GetOrInitCall(baseCtx, sessionID, srv, &CallRequest{ResultCall: frame},
		func(context.Context) (AnyResult, error) {
			return cacheTestObjectResultWithValue(t, srv, frame, &cacheTestObject{
				Value: 1,
				lazyEval: func(context.Context) error {
					close(leaderReady)
					<-release
					return nil
				},
			}), nil
		})
	require.NoError(t, err)
	result := resAny.(ObjectResult[*cacheTestObject])
	shared := result.cacheSharedResult()
	require.False(t, shared.loadResultCall().ProfileSkip, "producer is intentionally NOT skipped")

	// UNTRACED leader (baseCtx): runs the eval but OTelProfActive(evalCtx) is false,
	// so it mints no OTel lazy span → lazyEvalSpanCtx stays invalid (a genuine
	// mixed/untraced recording). Parks so the traced joiner can join.
	leaderDone := make(chan error, 1)
	go func() { leaderDone <- c.Evaluate(baseCtx, result) }()
	select {
	case <-leaderReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the untraced lazy leader")
	}

	// TRACED joiner: reads the invalid lazyEvalSpanCtx and, because the producer is
	// NOT profile-skipped, STILL emits its OTel wait (targetless) — exactly what the
	// gate must catch. (If the gate keyed on lazyEvalSpanCtx validity instead of the
	// producer flag, this loss would be silently hidden.)
	jCtx, jSpan := tr.Start(baseCtx, "traced-joiner")
	jDone := make(chan error, 1)
	go func() { jDone <- c.Evaluate(jCtx, result) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		shared.lazyMu.Lock()
		joined := shared.lazyEvalWaiters >= 2
		shared.lazyMu.Unlock()
		if joined {
			break
		}
		require.False(t, time.Now().After(deadline), "timed out waiting for the joiner to join")
		time.Sleep(time.Millisecond)
	}

	close(release)
	require.NoError(t, <-leaderDone)
	require.NoError(t, <-jDone)
	jSpan.End()

	spans := toWcotelSpans(sr.Ended())
	cc, err := wcotel.Compile(spans)
	require.NoError(t, err)
	g, err := wcanalyze.Build(cc.Header, cc.Events)
	require.NoError(t, err)
	res := wcotel.CheckStructural(cc, g, wcotel.GateOptions{})
	require.Greater(t, res.UnresolvedWaitTargets, 0,
		"a non-skipped target with a genuinely invalid span must still produce a gate-observable unresolved wait")
	require.Error(t, res.Err(), "the structural gate must fail loud on the genuine invalid-target loss")
}
