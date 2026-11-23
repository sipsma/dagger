package dagql

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An engine cache's state exists while it has a holding: a merged reply
// whose records' Cloud entries are gone lands nothing, and leaves no state
// behind for some other engine's collection to sweep.
func TestAMergedReplyThatLandsNothingLeavesNoEngineCacheState(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	sent := ValueBundle{Values: []TransferredValue{{Ordinal: 1, SenderNumber: 999}}}
	change, err := cloud.ApplyMergedReply(ctx, "cache-c", sent, MergeReply{Values: []MergedValue{{Ordinal: 1, Number: 1}}})
	require.NoError(t, err)
	require.Empty(t, change.Holdings)
	cloud.egraphMu.RLock()
	_, has := cloud.remoteCaches["cache-c"]
	cloud.egraphMu.RUnlock()
	require.False(t, has, "cache-c holds nothing")
	usage, err := cloud.BlobCacheUsage()
	require.NoError(t, err)
	require.Zero(t, usage.Holdings)
}

// The usage's count of holdings is every engine cache's, as holdings come
// and go.
func TestBlobCacheUsageCountsEveryEngineCachesHoldings(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	holdings := func() int {
		t.Helper()
		usage, err := cloud.BlobCacheUsage()
		require.NoError(t, err)
		return usage.Holdings
	}
	for _, key := range []HolderKey{{"cache-a", 1}, {"cache-a", 2}, {"cache-b", 1}} {
		_, err := cloud.AttachRemoteHolding(ctx, key, holdingOf(fmt.Sprintf("usage-%d", key.Number)))
		require.NoError(t, err)
	}
	require.Equal(t, 3, holdings())
	_, _, err := cloud.CollectRemoteHoldings(ctx, cloud.ReleaseRemoteCache(ctx, "cache-a").Candidates, nil)
	require.NoError(t, err)
	require.Equal(t, 1, holdings())
}

// Many engine caches write their holdings at once, on recipes they share,
// while readers walk one engine cache's graph and closures, describe whole
// entries across engine caches, and build bundles for one: run under the
// race detector, every read of an engine cache's state or holdings is under
// a lock that excludes that engine's writes. Each write and read keeps to
// the cache's own rules: the test checks that every engine's holdings end
// collected, and the usage count with them.
func TestEngineCachesWriteAndAreReadAtOnce(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	const (
		engines = 8
		rows    = 200
		shared  = 40
	)
	recipe := func(e, i int) string {
		if i < shared {
			return fmt.Sprintf("locks-shared-%d", i)
		}
		return fmt.Sprintf("locks-%d-%d", e, i)
	}
	cacheOf := func(e int) CacheID { return CacheID(fmt.Sprintf("cache-%02d", e)) }

	stop := make(chan struct{})
	var readers sync.WaitGroup
	read := func(f func(int)) {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				f(n)
			}
		}()
	}
	read(func(n int) {
		cache := cacheOf(n % engines)
		cloud.HolderGraph(cache, []uint64{1, 2, 3, uint64(rows)})
		cloud.HolderClosure(HolderKey{Cache: cache, Number: uint64(rows)})
	})
	read(func(n int) {
		key := HolderKey{Cache: cacheOf(n % engines), Number: uint64(n%rows + 1)}
		cloud.RemoteEntryInfo(key)
		cloud.RemoteHoldingInfos([]HolderKey{key, {Cache: cacheOf((n + 1) % engines), Number: key.Number}})
		cloud.HoldingFacts(key)
		cloud.HoldingRuns([]HolderKey{key})
		if number, ok := cloud.HolderEntry(key); ok {
			cloud.EntryInfo(number)
			cloud.EntriesHeld([]uint64{number}, 4)
			cloud.CacheHoldings(key.Cache, []uint64{number})
			_, _ = cloud.StoredBundle(ctx, []uint64{number}, time.Now().Unix(), &HeldBy{Cache: key.Cache})
		}
		_, _ = cloud.BlobCacheUsage()
	})

	var writers sync.WaitGroup
	for e := range engines {
		writers.Add(1)
		go func() {
			defer writers.Done()
			cache := cacheOf(e)
			session := fmt.Sprintf("s-%d", e)
			for i := range rows {
				key := HolderKey{Cache: cache, Number: uint64(i + 1)}
				var deps []uint64
				if i > 0 {
					deps = []uint64{uint64(i)}
				}
				desc := holdingOf(recipe(e, i), deps...)
				if i%3 == 0 {
					desc.ContentDigest = testDigest(fmt.Sprintf("locks-content-%d", i))
				}
				_, err := cloud.AttachRemoteHolding(ctx, key, desc)
				assert.NoError(t, err)
				_, err = cloud.AddRemoteHold(ctx, key, session)
				assert.NoError(t, err)
				if i%5 == 0 {
					_, _, err = cloud.UpdateRemoteHolding(ctx, key, RemoteHoldingUpdate{Parts: []PersistedPartAddress{{Part: "fs"}}})
					assert.NoError(t, err)
				}
			}
			ended := cloud.ReleaseRemoteSession(ctx, cache, session)
			_, _, err := cloud.CollectRemoteHoldings(ctx, ended.Candidates, nil)
			assert.NoError(t, err)
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()

	usage, err := cloud.BlobCacheUsage()
	require.NoError(t, err)
	require.Zero(t, usage.Holdings, "every engine's holdings ended collected")
	cloud.egraphMu.RLock()
	defer cloud.egraphMu.RUnlock()
	require.Empty(t, cloud.remoteCaches, "and their states with them")
}

// Two engine caches' holds of their own writes hold the cache at once, and
// each writes its holdings of an entry they share while the other's hold
// is open; a hold that needs the cache to itself waits for both.
func TestEngineWriteHoldsRunBesideEachOther(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	for _, key := range []HolderKey{{"cache-a", 1}, {"cache-b", 1}} {
		_, err := cloud.AttachRemoteHolding(ctx, key, holdingOf("beside-1"))
		require.NoError(t, err)
	}
	a := cloud.HoldEngineWrites("cache-a")
	b := cloud.HoldEngineWrites("cache-b")
	require.True(t, a.Shared())
	desc := holdingOf("beside-1")
	require.Zero(t, a.PlanAttach(HolderKey{"cache-a", 2}, desc, PrepareRemoteHolding(HolderKey{"cache-a", 2}, desc)), "a second holding of a shared entry changes only A's own")
	_, err := a.AttachRemoteHolding(ctx, HolderKey{"cache-a", 2}, desc)
	require.NoError(t, err)
	_, err = b.AddRemoteHold(ctx, HolderKey{"cache-b", 1}, "s-b")
	require.NoError(t, err)
	_, err = b.AttachRemoteHolding(ctx, HolderKey{"cache-a", 3}, desc)
	require.ErrorIs(t, err, errNeedsWholeCache, "B's hold writes B's holdings alone")
	_, err = a.AttachRemoteHolding(ctx, HolderKey{"cache-a", 9}, holdingOf("beside-new"))
	require.NoError(t, err, "a write the hold wasn't told needs the cache is made, and counted")
	require.Equal(t, uint64(1), cloud.SharedHoldMisses(), "the new entry, written in a shared hold")

	whole := make(chan struct{})
	go func() {
		w := cloud.HoldRemoteWrites()
		close(whole)
		_ = w.Release(ctx)
	}()
	select {
	case <-whole:
		t.Fatal("a hold of the whole cache began beside engine caches' holds")
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, a.Release(ctx))
	require.NoError(t, b.Release(ctx))
	<-whole
	held, ok := cloud.RemoteHoldingInfo(HolderKey{"cache-b", 1})
	require.True(t, ok)
	require.Equal(t, []string{"s-b"}, held.Holding.Sessions)
}

// A shared hold's writes of an engine cache with no state, such as a late
// update of a holding the engine no longer has, find nothing and change
// nothing, as they do with the cache held to itself; an attach, which would
// create the state, is refused, and creates none.
func TestSharedHoldWritesOfAnEngineCacheWithNoStateFindNothing(t *testing.T) {
	t.Parallel()
	key := HolderKey{"cache-a", 1}
	writes := func(hold func(*Cache) *RemoteWrites) []string {
		t.Helper()
		ctx, cloud := storedPartTestCache(t, WithBlobStore())
		_, err := cloud.AttachRemoteHolding(ctx, HolderKey{"cache-b", 1}, holdingOf("no-state-1"))
		require.NoError(t, err)
		w := hold(cloud)
		var got []string
		note := func(what string, change RemoteChange, found bool, err error) {
			got = append(got, fmt.Sprintf("%s: holdings %v shared %v found %v err %v", what, change.Holdings, change.Shared, found, err))
		}
		change, found, err := w.UpdateRemoteHolding(ctx, key, RemoteHoldingUpdate{Parts: []PersistedPartAddress{{Part: "fs"}}})
		note("update", change, found, err)
		change, err = w.AddRemoteHold(ctx, key, "s-a")
		note("hold", change, false, err)
		change, err = w.ObserveRemoteRetention(ctx, key, RetentionObservation{Retained: true})
		note("retention", change, false, err)
		change, found, err = w.ApplyHeldValueState(ctx, key, HeldValueState{Replacements: 1})
		note("value state", change, found, err)
		note("release", w.ReleaseRemoteSession(ctx, key.Cache, "s-a"), false, nil)
		require.NoError(t, w.Release(ctx))
		require.Zero(t, cloud.SharedHoldMisses())
		cloud.egraphMu.RLock()
		_, has := cloud.remoteCaches[key.Cache]
		cloud.egraphMu.RUnlock()
		require.False(t, has, "cache-a holds nothing")
		return got
	}
	whole := writes(func(c *Cache) *RemoteWrites { return c.HoldRemoteWrites() })
	shared := writes(func(c *Cache) *RemoteWrites { return c.HoldEngineWrites(key.Cache) })
	require.Equal(t, whole, shared)

	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	w := cloud.HoldEngineWrites(key.Cache)
	desc := holdingOf("no-state-1")
	require.NotZero(t, w.PlanAttach(key, desc, PrepareRemoteHolding(key, desc))&SharedNewEngineCache)
	_, err := w.AttachRemoteHolding(ctx, key, desc)
	require.ErrorIs(t, err, errNeedsWholeCache)
	require.NoError(t, w.Release(ctx))
	cloud.egraphMu.RLock()
	defer cloud.egraphMu.RUnlock()
	require.Empty(t, cloud.remoteCaches, "the refused attach created no state")
}

// Many engine caches each apply their rows as the service does: in a hold
// of their own writes, each write planned first, the hold taken to itself
// for a write whose plan names shared state and turned shared again when
// something waits, and collections with the cache to themselves; while
// readers read across them. Under the race detector, no write in a shared
// hold changes state engines share, and every holding ends collected.
func TestEngineWritesInSharedHoldsAtOnce(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	const (
		engines = 8
		rows    = 150
		shared  = 30
	)
	cacheOf := func(e int) CacheID { return CacheID(fmt.Sprintf("cache-%02d", e)) }
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			key := HolderKey{Cache: cacheOf(n % engines), Number: uint64(n%rows + 1)}
			cloud.RemoteEntryInfo(key)
			cloud.HolderGraph(key.Cache, []uint64{key.Number})
			if number, ok := cloud.HolderEntry(key); ok {
				_, _ = cloud.StoredBundle(ctx, []uint64{number}, time.Now().Unix(), &HeldBy{Cache: key.Cache})
			}
		}
	}()
	var writers sync.WaitGroup
	for e := range engines {
		writers.Add(1)
		go func() {
			defer writers.Done()
			cache := cacheOf(e)
			session := fmt.Sprintf("s-%d", e)
			w := cloud.HoldEngineWrites(cache)
			for i := range rows {
				key := HolderKey{Cache: cache, Number: uint64(i + 1)}
				recipe := fmt.Sprintf("shared-hold-%d-%d", e, i)
				if i < shared {
					recipe = fmt.Sprintf("shared-hold-%d", i)
				}
				var deps []uint64
				if i > 0 {
					deps = []uint64{uint64(i)}
				}
				desc := holdingOf(recipe, deps...)
				if i%4 == 0 {
					desc.ContentDigest = testDigest(fmt.Sprintf("shared-hold-content-%d", i%shared))
				}
				prepared := PrepareRemoteHolding(key, desc)
				// The hold goes back to shared before the plan, never
				// between the plan and its write: anything can change
				// while it lets go.
				if !w.Shared() && w.Contended() {
					w.Share()
				}
				if w.PlanAttach(key, desc, prepared) != 0 {
					w.Exclusive()
				}
				_, err := w.AttachPreparedRemoteHolding(ctx, key, desc, prepared)
				assert.NoError(t, err)
				_, err = w.AddRemoteHold(ctx, key, session)
				assert.NoError(t, err)
				if i%5 == 0 {
					update := RemoteHoldingUpdate{Parts: []PersistedPartAddress{{Part: "fs"}}}
					preparedUpdate := PrepareRemoteHoldingUpdate(key, update)
					if w.PlanUpdate(key, update, preparedUpdate) != 0 {
						w.Exclusive()
					}
					_, _, err = w.UpdatePreparedRemoteHolding(ctx, key, update, preparedUpdate)
					assert.NoError(t, err)
				}
			}
			ended := w.ReleaseRemoteSession(ctx, cache, session)
			w.Exclusive()
			_, _, err := w.CollectRemoteHoldings(ctx, ended.Candidates, nil)
			assert.NoError(t, err)
			assert.NoError(t, w.Release(ctx))
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()
	require.Zero(t, cloud.SharedHoldMisses(), "every write in a shared hold changed only its engine cache's own")
	usage, err := cloud.BlobCacheUsage()
	require.NoError(t, err)
	require.Zero(t, usage.Holdings)
}
