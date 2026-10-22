package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdklog "go.opentelemetry.io/otel/sdk/log"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/engineutil"
	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/internal/buildkit/solver/pb"
	"github.com/dagger/dagger/internal/buildkit/util/network"
)

func TestGracefulStopReturnsEarlierShutdownErrors(t *testing.T) {
	srv := newGracefulStopServer(t, filepath.Join(t.TempDir(), "cache.db"))
	// GracefulStop used to collect this close error and then drop it.
	srv.engineUtilOpts = &engineutil.Opts{NetworkProviders: map[pb.NetMode]network.Provider{pb.NetMode_UNSET: failingNetworkProvider{}}}
	err := srv.GracefulStop(boundedContext(t))
	require.ErrorIs(t, err, errNetworkProviderClose)
}

var errNetworkProviderClose = errors.New("network provider close failed")

type failingNetworkProvider struct{}

func (failingNetworkProvider) New(context.Context, string) (network.Namespace, error) {
	return nil, errors.New("unused")
}
func (failingNetworkProvider) Close() error { return errNetworkProviderClose }

// The stop's tail (closing the dagql cache, then engine.stop and the prune
// events) keeps a fifth of what is left of the budget, at least 15s and at
// most a minute. The clean-up gets the rest.
func TestShutdownTailReserve(t *testing.T) {
	for _, tc := range []struct{ remaining, reserve time.Duration }{
		{10 * time.Second, 15 * time.Second},
		{50 * time.Second, 15 * time.Second},
		{2 * time.Minute, 24 * time.Second},
		{5 * time.Minute, time.Minute},
		{10 * time.Minute, time.Minute},
	} {
		require.Equal(t, tc.reserve, shutdownTailReserve(tc.remaining), "remaining %s", tc.remaining)
	}
}

// The clean-up's context ends a tail reserve before the stop's deadline. A
// stop without a deadline has a clean-up without one.
func TestShutdownCleanupContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	cleanup, done := shutdownCleanupContext(ctx)
	defer done()
	cut, ok := cleanup.Deadline()
	require.True(t, ok)
	require.InDelta(t, 15*time.Second, deadline.Sub(cut), float64(time.Second))

	unbounded, done2 := shutdownCleanupContext(context.Background())
	defer done2()
	_, ok = unbounded.Deadline()
	require.False(t, ok)
}

// A shutdown GC that would outlast the budget is cut at the clean-up's
// deadline, so the tail keeps its reserve: the stop still closes the cache
// cleanly and exports engine.stop before its deadline, and a cut GC isn't an
// error of the stop.
func TestGracefulStopCutsGCForTheTail(t *testing.T) {
	srv := newGracefulStopServer(t, filepath.Join(t.TempDir(), "cache.db"))
	exporter := &engineEventTestExporter{}
	export := &engineEventTestExport{provider: sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))}
	srv.engineName = "engine-a"
	srv.engineEvents = newEngineEventEmitter(export.Logger())
	srv.engineEventExport = export
	srv.engineEventShutdownBudget = engineEventShutdownTimeout
	srv.localCacheGCEnabled = true

	// 16s left: the tail keeps 15s, so the clean-up gets about 1s.
	ctx, cancel := context.WithTimeout(t.Context(), 16*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var (
		gcDeadline time.Time
		bounded    bool
	)
	srv.testBeforeShutdownGC = func(gcCtx context.Context) {
		gcDeadline, bounded = gcCtx.Deadline()
		<-gcCtx.Done() // a GC that would run past the budget
	}
	require.NoError(t, srv.GracefulStop(ctx))
	stopped := time.Now()

	require.True(t, bounded, "the shutdown GC runs under the clean-up's deadline")
	require.InDelta(t, 15*time.Second, deadline.Sub(gcDeadline), float64(time.Second))
	require.True(t, stopped.Before(deadline), "the tail finished inside the budget")
	var stops []telemetryattrs.EngineStopEvent
	for _, event := range exporter.events(t) {
		if event.kind == telemetryattrs.EngineEventStop {
			stops = append(stops, decodeEngineEvent[telemetryattrs.EngineStopEvent](t, event))
		}
	}
	require.Len(t, stops, 1, "engine.stop is exported")
	require.True(t, stops[0].Clean, "the cache was saved for the next start")
}

// With the cut switched off, the shutdown GC runs under the whole stop's
// deadline, as it did before.
func TestGracefulStopWithTheGCCutOff(t *testing.T) {
	t.Setenv(dagql.ShutdownGCCutEnv, "0")
	srv := newGracefulStopServer(t, filepath.Join(t.TempDir(), "cache.db"))
	srv.localCacheGCEnabled = true
	// Under its metadata budget, so the GC, which isn't cut, runs to its end.
	srv.dagqlCacheMaxEstimatedBytes = 1 << 40
	srv.dagqlCacheTargetEstimatedBytes = 1 << 39
	ctx, cancel := context.WithTimeout(t.Context(), 16*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var gcDeadline time.Time
	srv.testBeforeShutdownGC = func(gcCtx context.Context) { gcDeadline, _ = gcCtx.Deadline() }
	require.NoError(t, srv.GracefulStop(ctx))
	require.Equal(t, deadline, gcDeadline, "the GC gets the whole budget")
}

// A GC cut at the clean-up's deadline isn't an error of the stop, but a prune
// that failed on its own still is, even when the GC returns it joined with the
// cut.
func TestGracefulStopKeepsAPruneErrorJoinedWithTheCut(t *testing.T) {
	srv := newGracefulStopServer(t, filepath.Join(t.TempDir(), "cache.db"))
	srv.localCacheGCEnabled = true
	srv.workerGCPolicies = []dagql.CachePrunePolicy{{All: true}}
	srv.dagqlCacheMaxEstimatedBytes = 1 << 40
	srv.dagqlCacheTargetEstimatedBytes = 1 << 39
	// Its release fails once the clean-up's deadline has passed, so the
	// prune's error comes back joined with the cut.
	sessionCtx := addGCTestPersistable(t, srv.engineCache, "releaseFails", "releaseFails", gcTestFailingReleaseInt{
		Int: dagql.NewInt(1),
		release: func(ctx context.Context) error {
			<-ctx.Done()
			return errGCTestReleaseFailed
		},
	})
	require.NoError(t, srv.engineCache.ReleaseSession(sessionCtx, "releaseFails"))

	// 16s left: the tail keeps 15s, so the clean-up gets about 1s.
	ctx, cancel := context.WithTimeout(t.Context(), 16*time.Second)
	defer cancel()
	err := srv.GracefulStop(ctx)
	require.ErrorIs(t, err, errGCTestReleaseFailed, "the prune's own failure is an error of the stop")
	require.NotErrorIs(t, err, context.DeadlineExceeded, "the cut isn't")
}

var errGCTestReleaseFailed = errors.New("release failed")

func TestWithoutDeadlineExceeded(t *testing.T) {
	failed := errors.New("failed")
	for _, tc := range []struct {
		name    string
		err     error
		rest    []error
		dropped bool
	}{
		{name: "nil"},
		{name: "a deadline", err: context.DeadlineExceeded, dropped: true},
		{name: "a wrapped deadline", err: fmt.Errorf("prune: %w", context.DeadlineExceeded), dropped: true},
		{name: "another failure", err: failed, rest: []error{failed}},
		{name: "a cancellation", err: context.Canceled, rest: []error{context.Canceled}},
		{name: "a failure joined with a deadline", err: errors.Join(fmt.Errorf("prune: %w", failed), context.DeadlineExceeded), rest: []error{failed}, dropped: true},
		{name: "nested joins", err: errors.Join(errors.Join(failed, fmt.Errorf("prune: %w", context.DeadlineExceeded)), context.DeadlineExceeded), rest: []error{failed}, dropped: true},
		{name: "a deadline and a failure under one wrap", err: fmt.Errorf("prune: %w", errors.Join(failed, context.DeadlineExceeded)), rest: []error{failed, context.DeadlineExceeded}},
		// As a usage measurement cut by the deadline returns it, wrapped by gc.go.
		{name: "a wrapped join of a deadline", err: fmt.Errorf("prune: %w", errors.Join(context.DeadlineExceeded, nil)), dropped: true},
		{name: "a wrapped join of deadlines only", err: fmt.Errorf("prune: %w", errors.Join(fmt.Errorf("measure: %w", context.DeadlineExceeded), errors.Join(context.DeadlineExceeded))), dropped: true},
		{name: "a wrapped join of a deadline and a failure, joined with a deadline", err: errors.Join(fmt.Errorf("prune: %w", errors.Join(context.DeadlineExceeded, failed)), context.DeadlineExceeded), rest: []error{failed, context.DeadlineExceeded}, dropped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest, dropped := withoutDeadlineExceeded(tc.err)
			require.Equal(t, tc.dropped, dropped)
			if len(tc.rest) == 0 {
				require.NoError(t, rest)
				return
			}
			keepsDeadline := false
			for _, want := range tc.rest {
				require.ErrorIs(t, rest, want)
				keepsDeadline = keepsDeadline || errors.Is(want, context.DeadlineExceeded)
			}
			if !keepsDeadline {
				require.NotErrorIs(t, rest, context.DeadlineExceeded)
			}
		})
	}
}

// gcTestFailingReleaseInt runs release when the cache releases it, and
// returns its error.
type gcTestFailingReleaseInt struct {
	dagql.Int
	release func(context.Context) error
}

func (v gcTestFailingReleaseInt) OnRelease(ctx context.Context) error {
	return v.release(ctx)
}

// gcTestReleaseHookInt runs onRelease when the cache releases it.
type gcTestReleaseHookInt struct {
	dagql.Int
	onRelease func()
}

func (v gcTestReleaseHookInt) OnRelease(context.Context) error {
	v.onRelease()
	return nil
}

// A manual prune whose request is cancelled in the middle of its plan stops
// there: the request fails with the cancellation, the prune event names
// exactly the edge that went, and a later prune drops the rest.
func TestManualPruneCanceledMidPlan(t *testing.T) {
	ctx := boundedContext(t)
	cache := newGCTestCache(t)
	srv, exporter := engineEventTestServer(cache)
	srv.rootDir = t.TempDir()
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	for _, name := range []string{"manualCancelA", "manualCancelB", "manualCancelC"} {
		sessionCtx := addGCTestPersistable(t, cache, name, name, gcTestReleaseHookInt{Int: dagql.NewInt(1), onRelease: func() { once.Do(cancel) }})
		require.NoError(t, cache.ReleaseSession(sessionCtx, name))
	}

	_, err := srv.PruneEngineLocalCacheEntries(reqCtx, core.EngineCachePruneOptions{})
	require.ErrorIs(t, err, context.Canceled)
	rest, err := srv.PruneEngineLocalCacheEntries(ctx, core.EngineCachePruneOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, rest.EntryCount, "the later prune drops what the cancelled one didn't reach")

	require.NoError(t, srv.engineEvents.Close(ctx))
	var drops []int
	for _, event := range exporter.events(t) {
		if event.kind == telemetryattrs.EngineEventPrune {
			drops = append(drops, len(decodeEngineEvent[telemetryattrs.EnginePruneEvent](t, event).Drops))
		}
	}
	require.Equal(t, []int{1, 2}, drops, "each run's event names exactly its own drops")
}
