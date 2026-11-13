package dagql

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// storedBundleIn builds a stored bundle with a read of pieces steps a piece,
// and returns it encoded whole: the bundle as a message carries it, and what
// else the build reports.
func storedBundleIn(t *testing.T, ctx context.Context, cloud *Cache, pieces int, roots []uint64, now int64, held *HeldBy) []byte {
	t.Helper()
	cloud.readPieceSize = pieces
	defer func() { cloud.readPieceSize = 0 }()
	built, err := cloud.StoredBundle(ctx, roots, now, held)
	require.NoError(t, err)
	encoded, err := json.Marshal(struct {
		Bundle                                  ValueBundle
		Donors, Gone, NoValue, Held, CallInputs []uint64
		RootOwners                              []int
		UnsendableCalls                         int
	}{built.Bundle, built.Donors, built.Gone, built.NoValue, built.Held, built.CallInputs, built.RootOwners, built.UnsendableCalls})
	require.NoError(t, err)
	return encoded
}

// requireSameInPieces requires a build read in pieces of one step, letting
// the lock go after every step, to give the bundle a single hold gives.
func requireSameInPieces(t *testing.T, ctx context.Context, cloud *Cache, roots []uint64, now int64, held *HeldBy) {
	t.Helper()
	whole := storedBundleIn(t, ctx, cloud, 1<<30, roots, now, held)
	require.Equal(t, string(whole), string(storedBundleIn(t, ctx, cloud, 1, roots, now, held)), "the bundle read in pieces is the single hold's")
}

// A stored bundle's build reads the cache in pieces, letting egraphMu go
// between them (readInPieces), so that a writer waits for one piece, not the
// whole bundle: a build of 1,280 entries, with their parts, at 64 steps a
// piece, lets it go many times.
func TestStoredBundleLetsTheLockGoBetweenPieces(t *testing.T) {
	t.Parallel()
	ctx, cloud, batch := storedBenchCloud(t)
	var yielded atomic.Int64
	cloud.testReadPieceYielded = func() { yielded.Add(1) }
	cloud.readPieceSize = 64
	built, err := cloud.StoredBundle(ctx, batch, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Len(t, built.Bundle.Values, storedBenchChain+storedBenchRoots*(storedBenchOwn+1))
	require.GreaterOrEqual(t, yielded.Load(), int64(len(built.Bundle.Values)/64), "the lock let go between pieces")
}

// A build normalizes a record held whole, a fresh merge's before the body
// worker compresses it, with egraphMu let go: normalizing decodes and checks
// every payload, and reads only the record's envelope and call, which the
// cache never changes in place.
func TestStoredBundleNormalizesWithTheLockLetGo(t *testing.T) {
	t.Parallel()
	ctx, cloud, batch := storedBenchCloud(t)
	var locked, unlocked atomic.Int64
	cloud.testStoredTransferNormalized = func() {
		if cloud.egraphMu.TryLock() {
			cloud.egraphMu.Unlock()
			unlocked.Add(1)
		} else {
			locked.Add(1)
		}
	}
	_, err := cloud.StoredBundle(ctx, batch, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Zero(t, locked.Load(), "no record normalized under the lock")
	require.Positive(t, unlocked.Load(), "the records held whole were normalized")
}

// With no write between its pieces, a build read in pieces gives, byte for
// byte, the bundle a single hold gives: a closure of 1,280 entries with their
// own parts, with some of it held by the recipient; a part borrowed through
// the placement rule; and a call kept on an entry.
func TestStoredBundleInPiecesIsTheSingleHoldsBundle(t *testing.T) {
	t.Parallel()
	t.Run("closure", func(t *testing.T) {
		t.Parallel()
		ctx, cloud, batch := storedBenchCloud(t)
		now := time.Now().Unix()
		requireSameInPieces(t, ctx, cloud, batch, now, nil)
		// The recipient holds every tenth entry of the closure.
		for i, number := range batch {
			if i%10 == 0 {
				heldTestHold(t, ctx, cloud, "cache-b", uint64(1000+i), number, 0)
			}
		}
		requireSameInPieces(t, ctx, cloud, batch, now, &HeldBy{Cache: "cache-b"})
	})
	t.Run("borrowed part", func(t *testing.T) {
		t.Parallel()
		ctx, cloud, r, p := storedBundleTestCloud(t, "root")
		member := storedBundleTestClassMember(t, ctx, cloud, r, "bundle-member")
		part := testLiveOffer()
		part.Value.Services = []TransferredServiceBinding{{ServiceResultID: p, Hostname: "svc"}}
		part.Owner.DependencyIDs = []uint64{p}
		stored, _, err := cloud.SetStoredPart(ctx, member, part, 0)
		require.NoError(t, err)
		require.True(t, stored)
		requireSameInPieces(t, ctx, cloud, []uint64{r}, time.Now().Unix(), nil)
	})
	t.Run("kept call", func(t *testing.T) {
		t.Parallel()
		test := newReceiverCallTest(t)
		requireSameInPieces(t, test.ctx, test.cloud, []uint64{test.number}, time.Now().Unix(), &HeldBy{Cache: "cache-fresh"})
	})
}

// Builds read in pieces stay whole while the cache is written between and
// during their pieces: rows attach and hold the recipient's holdings, which
// changes what is held; the source's bundle is merged again; stored parts are
// set again and dropped. Every bundle passes validateStoredBundle (or the
// build fails), every value's dependencies are rows of the bundle, and the
// race detector sees no part read after the lock that a writer changes.
func TestStoredBundleStaysWholeUnderWritesBetweenPieces(t *testing.T) {
	t.Parallel()
	ctx, cloud, batch, source := storedBenchCloudBundle(t)
	cloud.readPieceSize = 8
	done := make(chan struct{})
	var writers sync.WaitGroup
	write := func(name string, f func(i int) error) {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; ; i++ {
				select {
				case <-done:
					return
				default:
				}
				if err := f(i); err != nil {
					t.Errorf("%s: %v", name, err)
					return
				}
			}
		}()
	}
	write("rows", func(i int) error {
		number := batch[i%len(batch)]
		entry := storedBundleTestEntry(cloud, number)
		cloud.egraphMu.RLock()
		recipe := entry.recipeKeys[0]
		cloud.egraphMu.RUnlock()
		key := HolderKey{Cache: "cache-b", Number: uint64(5000 + i%len(batch))}
		if _, err := cloud.AttachRemoteHolding(ctx, key, RemoteHolding{Recipe: recipe, Request: recipe, Field: "held"}); err != nil {
			return err
		}
		_, err := cloud.AddRemoteHold(ctx, key, fmt.Sprintf("session-%d", i%7))
		return err
	})
	write("merged", func(i int) error {
		_, err := cloud.MergeValues(ctx, CacheID(fmt.Sprintf("cache-m%d", i%3)), source)
		return err
	})
	write("parts", func(i int) error {
		number := batch[i%len(batch)]
		if _, _, err := cloud.SetStoredPart(ctx, number, storedBenchPart(uint64(i)), 0); err != nil {
			return err
		}
		_, _, err := cloud.DropStoredParts(digest.FromString(fmt.Sprintf("stored-bench-layer-%d-%d", i/2, 0)))
		return err
	})
	for i := range 6 {
		var held *HeldBy
		if i%2 == 1 {
			held = &HeldBy{Cache: "cache-b"}
		}
		built, err := cloud.StoredBundle(ctx, batch, time.Now().Unix(), held)
		require.NoError(t, err, "build %d", i)
		require.NotEmpty(t, built.Bundle.Values)
		rows := len(built.Bundle.Values)
		for _, value := range built.Bundle.Values {
			for _, dep := range value.DependencyIDs {
				require.True(t, dep >= 1 && dep <= uint64(rows), "build %d: value %d names a row the bundle lacks", i, value.SenderNumber)
			}
		}
	}
	close(done)
	writers.Wait()
}

// storedBundleWriteAt builds roots read one step a piece, with write run once,
// holding egraphMu, at the read's nth letting it go.
func storedBundleWriteAt(t *testing.T, ctx context.Context, cloud *Cache, roots []uint64, n int, write func(res func(uint64) *sharedResult)) (StoredBundleResult, error) {
	t.Helper()
	var yields int
	cloud.readPieceSize = 1
	cloud.testReadPieceYielded = func() {
		yields++
		if yields != n {
			return
		}
		cloud.egraphMu.Lock()
		defer cloud.egraphMu.Unlock()
		write(func(number uint64) *sharedResult { return cloud.resultsByID[sharedResultID(number)] })
	}
	defer func() { cloud.readPieceSize, cloud.testReadPieceYielded = 0, nil }()
	return cloud.StoredBundle(ctx, roots, time.Now().Unix(), nil)
}

// requireOwnersOfRoots requires each of a bundle's roots to travel with a
// root it was asked for that it carries: an index among the roots asked for
// but those gone or with no value, as the service counts them.
func requireOwnersOfRoots(t *testing.T, built StoredBundleResult, asked int, msg string) {
	t.Helper()
	carried := asked - len(built.Gone) - len(built.NoValue)
	require.Len(t, built.RootOwners, len(built.Bundle.Roots), msg)
	for _, owner := range built.RootOwners {
		require.True(t, owner >= 0 && owner < carried, "%s: owner %d of %d roots carried", msg, owner, carried)
	}
}

// A root the cache loses between the pieces of a build's read, at any point
// of it, is reported gone, as one lost before the build, and the other root
// goes whole, numbered among the roots the bundle carries.
func TestStoredBundleRootGoneBetweenPieces(t *testing.T) {
	t.Parallel()
	for n := 1; n <= 30; n++ {
		ctx, cloud, r, _ := storedBundleTestCloud(t, "root")
		member := storedBundleTestClassMember(t, ctx, cloud, r, "bundle-member")
		built, err := storedBundleWriteAt(t, ctx, cloud, []uint64{member, r}, n, func(res func(uint64) *sharedResult) {
			delete(cloud.resultsByID, sharedResultID(member))
		})
		msg := fmt.Sprintf("gone at letting go %d", n)
		require.NoError(t, err, msg)
		requireOwnersOfRoots(t, built, 2, msg)
	}
}

// A root that loses its value between the pieces of a build's read, pruned
// while an engine still holds it, is reported with no value, as one pruned
// before the build, and the other root goes whole.
func TestStoredBundleRootPrunedBetweenPieces(t *testing.T) {
	t.Parallel()
	for n := 1; n <= 30; n++ {
		ctx, cloud, r, _ := storedBundleTestCloud(t, "root")
		member := storedBundleTestClassMember(t, ctx, cloud, r, "bundle-member")
		heldTestHold(t, ctx, cloud, "cache-e", 9, member, 0)
		built, err := storedBundleWriteAt(t, ctx, cloud, []uint64{member, r}, n, func(res func(uint64) *sharedResult) {
			if entry := res(member); entry != nil {
				_, err := cloud.dropValueLocked(ctx, entry)
				require.NoError(t, err)
			}
		})
		msg := fmt.Sprintf("pruned at letting go %d", n)
		require.NoError(t, err, msg)
		requireOwnersOfRoots(t, built, 2, msg)
	}
}

// An entry replaced between the pieces of a build's read with a record that
// no longer depends on what was walked below it leaves no row outside the
// roots' closure: what no root reaches is dropped, and the bundle is valid at
// whatever point the replacement lands.
func TestStoredBundleEntryReplacedBetweenPieces(t *testing.T) {
	t.Parallel()
	for n := 1; n <= 30; n++ {
		ctx, cloud, r, _ := storedBundleTestCloud(t, "root")
		built, err := storedBundleWriteAt(t, ctx, cloud, []uint64{r}, n, func(res func(uint64) *sharedResult) {
			if entry := res(r); entry != nil {
				entry.deps = map[sharedResultID]struct{}{}
				entry.dependencyOwnershipRevision++
			}
		})
		msg := fmt.Sprintf("replaced at letting go %d", n)
		require.NoError(t, err, msg)
		require.NotEmpty(t, built.Bundle.Values, msg)
	}
}
