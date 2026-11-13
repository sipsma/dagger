package dagql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// queuedPushes returns the results queued for a push now, without waiting.
func queuedPushes(t *testing.T, bridge *RemoteCacheBridge) []uint64 {
	t.Helper()
	if !bridge.RemotePushesQueued() {
		return nil
	}
	numbers, err := bridge.TakeRemotePushes(t.Context())
	require.NoError(t, err)
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
	_, err = bridge.TakeRemotePushes(t.Context())
	require.ErrorIs(t, err, ErrRemoteCacheBridgeClosed)
}

// Results put back go ahead of those queued since, in their order, and a
// result waits once.
func TestRemotePushRequeueGoesFirst(t *testing.T) {
	t.Parallel()
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil)
	require.NoError(t, err)
	bridge, _, err := c.AttachRemoteCacheBridge()
	require.NoError(t, err)
	bridge.pushes.add(3, 4)
	bridge.RequeueRemotePushes([]uint64{1, 2, 3})
	require.Equal(t, []uint64{1, 2, 3, 4}, queuedPushes(t, bridge))

	// A take waits for the first result queued.
	got := make(chan []uint64, 1)
	go func() {
		numbers, _ := bridge.TakeRemotePushes(context.WithoutCancel(t.Context()))
		got <- numbers
	}()
	bridge.pushes.add(5)
	require.Equal(t, []uint64{5}, <-got)
}
