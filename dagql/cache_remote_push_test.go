package dagql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// queuedPushes takes the results queued for a push now, without waiting,
// and says they were pushed.
func queuedPushes(t *testing.T, bridge *RemoteCacheBridge) []uint64 {
	t.Helper()
	if idle, _ := bridge.RemotePushesIdle(); idle {
		return nil
	}
	numbers, _, err := bridge.TakeRemotePushes(t.Context())
	require.NoError(t, err)
	bridge.RemotePushesDone(len(numbers))
	return numbers
}

// A result the engine computes is queued for a push to the Cloud as its
// retention edge is created, once, in the order results are retained, and
// only while a remote cache integration is attached. A hit on a retained
// result queues nothing again, and nor does a result a merge from the Cloud
// brought.
func TestRemotePushQueuesComputedResultsAsTheyAreRetained(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	persistedListTestResult(t, ctx, c, srv, "before", String("before"))

	bridge, created, err := c.AttachRemoteCacheBridge()
	require.NoError(t, err)
	require.True(t, created)
	require.Empty(t, queuedPushes(t, bridge), "a result retained before the integration attached isn't queued")

	first := persistedListTestResult(t, ctx, c, srv, "first", String("first"))
	second := persistedListTestResult(t, ctx, c, srv, "second", String("second"))
	persistedListTestResult(t, ctx, c, srv, "first", String("first"))
	require.Equal(t, []uint64{uint64(first.cacheSharedResult().id), uint64(second.cacheSharedResult().id)}, queuedPushes(t, bridge))
	require.Empty(t, queuedPushes(t, bridge), "a take empties the queue")

	cctx, cloud, _, p := storedBundleTestCloud(t, "root")
	alone, err := cloud.StoredBundle(cctx, []uint64{p}, time.Now().Unix(), nil)
	require.NoError(t, err)
	reply, err := c.MergeValues(ctx, cloudCacheID, alone.Bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	require.Empty(t, queuedPushes(t, bridge), "a merged result is the Cloud's own")

	require.True(t, c.DetachRemoteCacheBridge(bridge))
	_, _, err = bridge.TakeRemotePushes(t.Context())
	require.ErrorIs(t, err, ErrRemoteCacheBridgeClosed)
}

// Results put back go ahead of those queued since, in their order, and a
// result waits once. A busy result waits for a take that has something else
// to push, or that a flush asks for. The queue is idle only once every
// result taken has been pushed or dropped.
func TestRemotePushQueueTracksEveryResultUntilPushed(t *testing.T) {
	t.Parallel()
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil)
	require.NoError(t, err)
	bridge, _, err := c.AttachRemoteCacheBridge()
	require.NoError(t, err)
	idle := func() bool {
		idle, _ := bridge.RemotePushesIdle()
		return idle
	}
	require.True(t, idle())

	bridge.pushes.add(1, 2, 3)
	numbers, flushed, err := bridge.TakeRemotePushes(ctx)
	require.NoError(t, err)
	require.False(t, flushed)
	require.Equal(t, []uint64{1, 2, 3}, numbers)
	require.False(t, idle(), "taken and not yet pushed")
	bridge.pushes.add(4)
	bridge.RequeueRemotePushes([]uint64{1, 2, 3})
	numbers, _, err = bridge.TakeRemotePushes(ctx)
	require.NoError(t, err)
	require.Equal(t, []uint64{1, 2, 3, 4}, numbers, "put back first, in order, each once")

	bridge.WaitRemotePushesBusy([]uint64{2})
	bridge.RemotePushesDone(3)
	require.False(t, idle(), "a busy result still waits")
	bridge.pushes.add(5)
	numbers, flushed, err = bridge.TakeRemotePushes(ctx)
	require.NoError(t, err)
	require.False(t, flushed)
	require.Equal(t, []uint64{2, 5}, numbers, "a busy result goes with the next take")

	bridge.WaitRemotePushesBusy([]uint64{2})
	bridge.RemotePushesDone(1)
	_, changed := bridge.RemotePushesIdle()
	type take struct {
		numbers []uint64
		flushed bool
	}
	got := make(chan take, 1)
	go func() {
		numbers, flushed, _ := bridge.TakeRemotePushes(context.WithoutCancel(ctx))
		got <- take{numbers, flushed}
	}()
	bridge.FlushRemotePushes()
	require.Equal(t, take{[]uint64{2}, true}, <-got, "a flush takes the busy results alone")
	bridge.RemotePushesDone(1)
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("the queue's change wakes its waiters")
	}
	require.True(t, idle())
}
