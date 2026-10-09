package snapshots

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	ctdsnapshots "github.com/containerd/containerd/v2/core/snapshots"
	"github.com/moby/locker"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func newOwnerIndexTestManager(tb testing.TB) *snapshotManager {
	tb.Helper()
	return &snapshotManager{
		records:                map[string]*cacheRecord{},
		Snapshotter:            newApplySnapshotDiffTestSnapshotter(),
		LeaseManager:           newApplySnapshotDiffTestLeaseManager(),
		metadataStore:          newMetadataStore(),
		snapshotContentDigests: map[string]map[digest.Digest]struct{}{},
		importedLayerByBlob:    map[ImportedLayerBlobKey]string{},
		importedLayerByDiff:    map[ImportedLayerDiffKey]string{},
		snapshotOwnerLeases:    map[string]map[string]struct{}{},
		ownerLeaseSnapshots:    map[string]map[string]struct{}{},
		ownerLeaseLocker:       locker.New(),
	}
}

func addOwnerIndexTestSnapshot(cm *snapshotManager, snapshotID, parent string) {
	cm.Snapshotter.(*applySnapshotDiffTestSnapshotter).snapshots[snapshotID] = ctdsnapshots.Info{Name: snapshotID, Parent: parent}
}

// RemoveLease drops the lease as an owner of exactly its own snapshots: a
// snapshot another lease also owns keeps that owner, and unrelated owners are
// untouched.
func TestRemoveLeaseDropsOnlyItsOwnership(t *testing.T) {
	ctx := context.Background()
	cm := newOwnerIndexTestManager(t)
	addOwnerIndexTestSnapshot(cm, "base", "")
	addOwnerIndexTestSnapshot(cm, "top", "base")
	require.NoError(t, cm.AttachLease(ctx, "dagql/result/removed", "top"))
	require.NoError(t, cm.AttachLease(ctx, "dagql/result/kept", "base"))
	for i := range 100 {
		id := "other-" + strconv.Itoa(i)
		addOwnerIndexTestSnapshot(cm, id, "")
		require.NoError(t, cm.AttachLease(ctx, "dagql/result/"+id, id))
	}

	require.NoError(t, cm.RemoveLease(ctx, "dagql/result/removed"))

	require.NotContains(t, cm.snapshotOwnerLeases, "top", "top had no other owner")
	require.Equal(t, map[string]struct{}{"dagql/result/kept": {}}, cm.snapshotOwnerLeases["base"])
	require.NotContains(t, cm.ownerLeaseSnapshots, "dagql/result/removed")
	require.Equal(t, map[string]struct{}{"base": {}}, cm.ownerLeaseSnapshots["dagql/result/kept"])
	for i := range 100 {
		id := "other-" + strconv.Itoa(i)
		require.Equal(t, map[string]struct{}{"dagql/result/" + id: {}}, cm.snapshotOwnerLeases[id])
		require.Equal(t, map[string]struct{}{id: {}}, cm.ownerLeaseSnapshots["dagql/result/"+id])
	}

	require.NoError(t, cm.RemoveLease(ctx, "dagql/result/kept"))
	require.NotContains(t, cm.snapshotOwnerLeases, "base")
	require.NoError(t, cm.RemoveLease(ctx, "dagql/result/never-attached"), "removing an unknown lease is a no-op")
}

// RemoveLease of one owner among many: a session release removes every
// result's owner leases, so its cost must not grow with the number of other
// owners in the engine.
func BenchmarkRemoveLeaseAmongOwners(b *testing.B) {
	for _, owners := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("owners=%d", owners), func(b *testing.B) {
			ctx := context.Background()
			cm := newOwnerIndexTestManager(b)
			for i := range owners {
				id := "snapshot-" + strconv.Itoa(i)
				addOwnerIndexTestSnapshot(cm, id, "")
				require.NoError(b, cm.AttachLease(ctx, "dagql/result/"+id, id))
			}
			addOwnerIndexTestSnapshot(cm, "target", "")
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				require.NoError(b, cm.AttachLease(ctx, "dagql/result/target", "target"))
				b.StartTimer()
				require.NoError(b, cm.RemoveLease(ctx, "dagql/result/target"))
			}
		})
	}
}
