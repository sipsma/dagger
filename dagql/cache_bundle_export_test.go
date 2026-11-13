package dagql

import (
	"context"
	"testing"
	"time"

	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/stretchr/testify/require"
)

// busyBundleRows publishes the left-out test rows with the shared row's
// evaluation running, for as long as the test runs, and returns them.
func busyBundleRows(t *testing.T, ctx context.Context, c *Cache, srv *Server) leftOutTestRows {
	t.Helper()
	rows := newLeftOutTestRows(t, ctx, c, srv)
	running := rows.shared.cacheSharedResult()
	running.lazyMu.Lock()
	running.lazyWhole.attempt = &lazyEvalAttempt{done: make(chan struct{}), waiters: 1}
	running.lazyMu.Unlock()
	t.Cleanup(func() {
		running.lazyMu.Lock()
		running.lazyWhole.attempt = nil
		running.lazyMu.Unlock()
	})
	return rows
}

// A cache export waits only for rows busy with something that ends on its
// own. A result whose closure also reaches a row busy until a later
// evaluation fails the export at once, though a running row comes first.
func TestExportBundleFailsOnARowBusyUntilALaterEvaluation(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	rows := busyBundleRows(t, ctx, c, srv)
	late := persistedListTestResult(t, ctx, c, srv, "busy-late", String("late"))
	transferTestDependency(c, ctx, rows.first, late)
	stuck := late.cacheSharedResult()
	require.Less(t, rows.shared.cacheSharedResult().id, stuck.id, "the running row is the one the root names")
	stuck.lazyMu.Lock()
	stuck.lazyWhole.syncPending = true
	stuck.lazyMu.Unlock()
	t.Cleanup(func() {
		stuck.lazyMu.Lock()
		stuck.lazyWhole.syncPending = false
		stuck.lazyMu.Unlock()
	})

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := c.ExportBundle(ctx, srv, []AnyResult{rows.first}, config.RefConfig{}, nil)
	require.ErrorIs(t, err, ErrPersistStateNotReady)
	require.NoError(t, ctx.Err(), "the export failed at once, without waiting")
	require.ErrorContains(t, err, "export bundle: 1 of 1 results left out")
}

// A cache export's wait for busy results has a limit: a row that never
// frees fails the export with the busy error once the limit has passed,
// rather than hanging it until its context ends. It isn't parallel: it
// lowers the package's limit.
func TestExportBundleStopsWaitingAtItsLimit(t *testing.T) {
	limit := bundleBusyLimit
	bundleBusyLimit = 300 * time.Millisecond
	t.Cleanup(func() { bundleBusyLimit = limit })
	ctx, c, srv := transferTestCache(t)
	rows := busyBundleRows(t, ctx, c, srv)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := c.ExportBundle(ctx, srv, []AnyResult{rows.first}, config.RefConfig{}, nil)
	require.ErrorIs(t, err, ErrPersistStateNotReady)
	require.NoError(t, ctx.Err(), "the export stopped at its limit, not at its context's end")
	require.ErrorContains(t, err, "export bundle: 1 of 1 results left out")
}
