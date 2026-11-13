package dagql

import (
	"context"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/opencontainers/go-digest"
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

// A cache export waits for a row that never frees until its context ends:
// the context is the only end of its waiting.
func TestExportBundleWaitsUntilItsContextEnds(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	rows := busyBundleRows(t, ctx, c, srv)

	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	err := c.ExportBundle(ctx, srv, []AnyResult{rows.first}, config.RefConfig{}, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the export waited until its context ended")
}

// A cache export reads its results' records before it captures them, to
// find their parts and what they hold, and those reads wait for a busy row
// too: a result busy when the export starts is exported once it's free.
func TestExportBundleSetupWaitsForABusyResult(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	rows := newLeftOutTestRows(t, ctx, c, srv)
	res := rows.ready.cacheSharedResult()
	attaching := make(chan struct{})
	c.egraphMu.Lock()
	res.attachDepsMu.Lock()
	res.attachDepsWaitCh = attaching
	res.attachDepsMu.Unlock()
	c.egraphMu.Unlock()
	freed := time.AfterFunc(200*time.Millisecond, func() { close(attaching) })
	defer freed.Stop()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var bundle *ValueBundle
	err := c.ExportBundle(ctx, srv, []AnyResult{rows.ready}, config.RefConfig{}, bundleCatcher{&bundle})
	require.NoError(t, err)
	require.NotNil(t, bundle)
	require.Len(t, bundle.Roots, 1, "the result is exported")
}

// bundleCatcher keeps the bundle an export writes.
type bundleCatcher struct{ bundle **ValueBundle }

func (b bundleCatcher) WriteBundle(_ context.Context, bundle ValueBundle) error {
	*b.bundle = &bundle
	return nil
}

func (bundleCatcher) WriteBlob(context.Context, digest.Digest, content.ReaderAt) error {
	return nil
}
