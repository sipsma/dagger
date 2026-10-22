package dagql

import (
	"context"
	"errors"
	"testing"

	"gotest.tools/v3/assert"
)

// A prune cancelled between drops stops there. It reports exactly the drops it
// made, says it was cancelled, and returns the cancellation's cause, so the
// engine's prune event names every edge that went and no other. The edges it
// didn't reach stay, and a later run drops them.
func TestCachePruneStopsBetweenDropsWhenCanceled(t *testing.T) {
	t.Parallel()
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil)
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, c.CloseDiscardingPersistence()) })

	rootFrame := cacheTestIntCall("prune-cancel-root")
	root, err := c.GetOrInitCall(ctx, "s", noopTypeResolver{}, &CallRequest{ResultCall: rootFrame}, ValueFunc(cacheTestIntResult(rootFrame, 0)))
	assert.NilError(t, err)
	assert.NilError(t, c.MakeResultUnpruneable(ctx, root))
	for _, field := range []string{"prune-cancel-a", "prune-cancel-b", "prune-cancel-c"} {
		frame := cacheTestIntCall(field)
		res, err := c.GetOrInitCall(ctx, "s", noopTypeResolver{}, &CallRequest{ResultCall: frame, IsPersistable: true}, ValueFunc(cacheTestIntResult(frame, 1)))
		assert.NilError(t, err)
		assert.NilError(t, c.AddExplicitDependency(ctx, root, res, "test"))
	}
	assert.NilError(t, c.ReleaseSession(ctx, "s"))

	// The stop budget runs out right after the run's first drop.
	pruneCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	spent := errors.New("the clean-up's share of the stop budget is spent")
	c.testAfterRetentionDrop = func(sharedResultID) { cancel(spent) }
	report, err := c.Prune(pruneCtx, []CachePrunePolicy{{All: true}})
	assert.ErrorIs(t, err, spent)
	assert.Assert(t, report.Canceled)
	assert.Equal(t, 1, len(report.DroppedEdges), "only the drop made before the cut")
	assert.Equal(t, 1, len(report.Entries))

	c.testAfterRetentionDrop = nil
	again, err := c.Prune(ctx, []CachePrunePolicy{{All: true}})
	assert.NilError(t, err)
	assert.Assert(t, !again.Canceled)
	assert.Equal(t, 2, len(again.DroppedEdges), "the edges the cut run didn't reach")
	for _, drop := range again.DroppedEdges {
		assert.Assert(t, drop.ResultID != report.DroppedEdges[0].ResultID)
	}
}

// With the cut switched off, a prune runs its whole plan whatever its context,
// as it did before: no drop is skipped and the run isn't marked cancelled.
func TestCachePruneRunsWholePlanWithTheCutOff(t *testing.T) {
	t.Setenv(ShutdownGCCutEnv, "0")
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil)
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, c.CloseDiscardingPersistence()) })

	rootFrame := cacheTestIntCall("prune-cut-off-root")
	root, err := c.GetOrInitCall(ctx, "s", noopTypeResolver{}, &CallRequest{ResultCall: rootFrame}, ValueFunc(cacheTestIntResult(rootFrame, 0)))
	assert.NilError(t, err)
	assert.NilError(t, c.MakeResultUnpruneable(ctx, root))
	for _, field := range []string{"prune-cut-off-a", "prune-cut-off-b", "prune-cut-off-c"} {
		frame := cacheTestIntCall(field)
		res, err := c.GetOrInitCall(ctx, "s", noopTypeResolver{}, &CallRequest{ResultCall: frame, IsPersistable: true}, ValueFunc(cacheTestIntResult(frame, 1)))
		assert.NilError(t, err)
		assert.NilError(t, c.AddExplicitDependency(ctx, root, res, "test"))
	}
	assert.NilError(t, c.ReleaseSession(ctx, "s"))

	pruneCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.testAfterRetentionDrop = func(sharedResultID) { cancel() }
	report, err := c.Prune(pruneCtx, []CachePrunePolicy{{All: true}})
	assert.NilError(t, err)
	assert.Assert(t, !report.Canceled)
	assert.Equal(t, 3, len(report.DroppedEdges), "the whole plan")
}
