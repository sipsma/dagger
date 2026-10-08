package snapshots_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/containerd/containerd/v2/core/leases"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/testutil"
	"github.com/stretchr/testify/require"
)

func leaseExists(t *testing.T, store *testutil.Store, id string) bool {
	t.Helper()
	all, err := store.Leases.List(context.Background(), "id=="+id)
	require.NoError(t, err)
	return len(all) == 1
}

// A dagql owner of a snapshot another dagql owner already holds borrows that
// owner's lease: no containerd write. Removing the borrower changes nothing in
// containerd; removing the lender first promotes the borrower to a lease of
// its own, holding the chain and its blobs, while the snapshot is still held.
func TestOwnerLeaseBorrowAndPromote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testutil.NewStore(t)
	parent, parentOwner := buildSnapshot(t, store, nil, "p.txt", "parent")
	child, childOwner := buildSnapshot(t, store, parent, "c.txt", "child")
	chain, err := child.ExportChain(ctx, uncompressed)
	require.NoError(t, err)
	blobs := []string{chain.Layers[0].Descriptor.Digest.String(), chain.Layers[1].Descriptor.Digest.String()}
	require.NoError(t, chain.Release(ctx))
	require.NoError(t, child.Release(ctx))
	require.NoError(t, parent.Release(ctx))

	const lender, borrower, dropped = "dagql/result/lender", "dagql/result/borrower", "dagql/result/dropped"
	require.NoError(t, store.Manager.AttachLease(ctx, lender, child.SnapshotID()))
	var commits atomic.Int64
	store.DB.RegisterMutationCallback(func(bool) { commits.Add(1) })
	require.NoError(t, store.Manager.AttachLease(ctx, borrower, child.SnapshotID()))
	require.NoError(t, store.Manager.AttachLease(ctx, dropped, child.SnapshotID()))
	require.Zero(t, commits.Load(), "borrowing writes nothing")
	require.False(t, leaseExists(t, store, borrower))

	require.NoError(t, store.Manager.RemoveLease(ctx, dropped), "a borrower's removal")
	require.NoError(t, store.Manager.RemoveLease(ctx, childOwner))
	require.NoError(t, store.Manager.RemoveLease(ctx, parentOwner))
	store.GC(t)
	require.True(t, snapshotPresent(t, store, child.SnapshotID()), "the lender holds the chain for the borrower")

	require.NoError(t, store.Manager.RemoveLease(ctx, lender))
	require.False(t, leaseExists(t, store, lender))
	_, snapshots, contents := leaseResources(t, store, borrower)
	require.ElementsMatch(t, []string{parent.SnapshotID(), child.SnapshotID()}, snapshots, "the promoted borrower holds the chain")
	require.ElementsMatch(t, blobs, contents, "and its blobs")
	require.False(t, leaseExists(t, store, dropped), "a removed borrower is not promoted")
	store.GC(t)
	require.True(t, snapshotPresent(t, store, child.SnapshotID()))
	require.True(t, snapshotPresent(t, store, parent.SnapshotID()))

	require.NoError(t, store.Manager.RemoveLease(ctx, borrower))
	store.GC(t)
	require.False(t, snapshotPresent(t, store, child.SnapshotID()))
	require.False(t, snapshotPresent(t, store, parent.SnapshotID()))
}

// Only dagql owner leases lend and borrow: an operation's lease, which callers
// write through, always gets its own containerd lease, and does not lend.
func TestOwnerLeaseBorrowOnlyBetweenOwners(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testutil.NewStore(t)
	built, buildOwner := buildSnapshot(t, store, nil, "a.txt", "owned")
	require.NoError(t, built.Release(ctx))

	// buildOwner is not a dagql owner: the first dagql owner is real.
	const first, second = "dagql/result/first", "dagql/result/second"
	require.NoError(t, store.Manager.AttachLease(ctx, first, built.SnapshotID()))
	require.True(t, leaseExists(t, store, first))
	const pin = "operation-pin"
	require.NoError(t, store.Manager.AttachLease(ctx, pin, built.SnapshotID()))
	require.True(t, leaseExists(t, store, pin), "an operation lease is always real")
	require.NoError(t, store.Manager.AttachLease(ctx, second, built.SnapshotID()))
	require.False(t, leaseExists(t, store, second))

	require.NoError(t, store.Manager.RemoveLease(ctx, first))
	require.True(t, leaseExists(t, store, second), "the pin does not lend: the borrower was promoted")
	require.NoError(t, store.Manager.RemoveLease(ctx, pin))
	require.NoError(t, store.Manager.RemoveLease(ctx, buildOwner))
	store.GC(t)
	require.True(t, snapshotPresent(t, store, built.SnapshotID()))
	require.NoError(t, store.Manager.RemoveLease(ctx, second))
	store.GC(t)
	require.False(t, snapshotPresent(t, store, built.SnapshotID()))
}

// A borrower whose promotion fails keeps its lender: RemoveLease fails and the
// lender stays, in containerd and as a lender, so the snapshot stays held. The
// retry promotes the borrower and deletes the lender.
func TestOwnerLeasePromotionFailureKeepsLender(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testutil.NewStore(t)
	built, buildOwner := buildSnapshot(t, store, nil, "a.txt", "promotion fails")
	require.NoError(t, built.Release(ctx))
	require.NoError(t, store.Manager.RemoveLease(ctx, buildOwner))

	const lender, failing = "dagql/result/lender", "dagql/result/failing"
	for _, id := range []string{lender, failing} {
		require.NoError(t, store.Manager.AttachLease(ctx, id, built.SnapshotID()))
	}

	injected := errors.New("injected promotion failure")
	store.BeforeAdd = func(_ context.Context, l leases.Lease, _ leases.Resource) error {
		if l.ID == failing {
			return injected
		}
		return nil
	}
	err := store.Manager.RemoveLease(ctx, lender)
	require.ErrorIs(t, err, injected)
	require.True(t, leaseExists(t, store, lender), "the lender stays while a borrower has no lease")
	store.GC(t)
	require.True(t, snapshotPresent(t, store, built.SnapshotID()))

	store.BeforeAdd = nil
	require.NoError(t, store.Manager.RemoveLease(ctx, lender), "the retry")
	require.False(t, leaseExists(t, store, lender))
	require.True(t, leaseExists(t, store, failing), "the borrower was promoted")
	store.GC(t)
	require.True(t, snapshotPresent(t, store, built.SnapshotID()), "held for the promoted borrower")
	require.NoError(t, store.Manager.RemoveLease(ctx, failing))
	store.GC(t)
	require.False(t, snapshotPresent(t, store, built.SnapshotID()))
}

// A restart loses the borrows, which live in memory only. The lender's lease
// is in containerd, so the snapshot is held until startup attaches the
// persisted owners again, as the cache's import does before it deletes stale
// owner leases.
func TestOwnerLeaseBorrowRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testutil.NewStore(t)
	built, buildOwner := buildSnapshot(t, store, nil, "a.txt", "restart")
	require.NoError(t, built.Release(ctx))
	require.NoError(t, store.Manager.RemoveLease(ctx, buildOwner))

	const lender, borrower = "dagql/result/lender", "dagql/result/borrower"
	require.NoError(t, store.Manager.AttachLease(ctx, lender, built.SnapshotID()))
	require.NoError(t, store.Manager.AttachLease(ctx, borrower, built.SnapshotID()))
	require.False(t, leaseExists(t, store, borrower))

	store.Reload(t)
	require.NoError(t, store.Manager.LoadPersistentMetadata(bkcache.PersistentMetadataRows{}))
	store.GC(t)
	require.True(t, snapshotPresent(t, store, built.SnapshotID()), "the lender's lease survives the restart")

	// Startup attaches the persisted owners, then deletes the stale ones:
	// here the lender's result was not persisted.
	require.NoError(t, store.Manager.AttachLease(ctx, borrower, built.SnapshotID()))
	require.NoError(t, store.Manager.DeleteStaleDaggerOwnerLeases(ctx, map[string]struct{}{borrower: {}}))
	require.False(t, leaseExists(t, store, lender))
	store.GC(t)
	require.True(t, snapshotPresent(t, store, built.SnapshotID()), "the persisted borrower holds the snapshot")
	require.NoError(t, store.Manager.RemoveLease(ctx, borrower))
	store.GC(t)
	require.False(t, snapshotPresent(t, store, built.SnapshotID()))
}

// Owners attach, borrow, are promoted and are removed concurrently: the
// snapshot is held until the last owner is removed.
func TestOwnerLeaseBorrowConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testutil.NewStore(t)
	built, buildOwner := buildSnapshot(t, store, nil, "a.txt", "concurrent")
	require.NoError(t, built.Release(ctx))
	require.NoError(t, store.Manager.RemoveLease(ctx, buildOwner))

	const owners = 32
	id := func(i int) string { return fmt.Sprintf("dagql/result/borrow-%d", i) }
	require.NoError(t, store.Manager.AttachLease(ctx, "dagql/result/keep", built.SnapshotID()))
	var wg sync.WaitGroup
	errs := make(chan error, 2*owners)
	for i := range owners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.Manager.AttachLease(ctx, id(i), built.SnapshotID())
			if i%2 == 1 {
				errs <- store.Manager.RemoveLease(ctx, id(i))
			}
		}()
	}
	// Remove the first owner while the others attach and remove.
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs <- store.Manager.RemoveLease(ctx, "dagql/result/keep")
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	for i := 0; i < owners; i += 2 {
		store.GC(t)
		require.True(t, snapshotPresent(t, store, built.SnapshotID()), "held while owner %d and later remain", i)
		require.NoError(t, store.Manager.RemoveLease(ctx, id(i)))
	}
	store.GC(t)
	require.False(t, snapshotPresent(t, store, built.SnapshotID()))
	all, err := store.Leases.List(ctx)
	require.NoError(t, err)
	for _, l := range all {
		require.NotContains(t, l.ID, "dagql/result/", "no owner lease is left behind")
	}
}
