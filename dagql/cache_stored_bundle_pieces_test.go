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
