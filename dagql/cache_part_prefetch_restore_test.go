package dagql

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// restoreTestBundle exports a bundle with one root for each chain, the roots
// in the chains' order and each offering its chain's part, as a file's part
// offers its records' parts in the order they were first used.
func restoreTestBundle(t *testing.T, chains []chainFixture) ValueBundle {
	t.Helper()
	ctx, c, srv := transferTestCache(t)
	var roots []AnyResult
	for _, chain := range chains {
		roots = append(roots, persistedListTestResult(t, ctx, c, srv, "restored-"+chain.tag, &transferTestValue{Text: "pending"}))
	}
	bundle := exportTestBundle(t, ctx, c, roots...)
	require.Len(t, bundle.Roots, len(chains))
	ordered := make([]TransferredRoot, 0, len(roots))
	for i, root := range roots {
		number := uint64(root.cacheSharedResult().id)
		j := slices.IndexFunc(bundle.Values, func(v TransferredValue) bool { return v.SenderNumber == number })
		require.GreaterOrEqual(t, j, 0)
		bundle.Values[j].Record.Envelope.PendingOffers = []PersistedPartOffer{exhaustionOffer(chains[i], "", true)}
		k := slices.IndexFunc(bundle.Roots, func(r TransferredRoot) bool { return r.Ordinal == bundle.Values[j].Ordinal })
		require.GreaterOrEqual(t, k, 0)
		ordered = append(ordered, bundle.Roots[k])
	}
	bundle.Roots = ordered
	return bundle
}

// holdLargeLane accepts held's offer, whose blobs' downloads wait until the
// returned channel is closed, and waits until it takes the large lane's only
// worker.
func holdLargeLane(t *testing.T, f *exhaustionFixture, held chainFixture) chan struct{} {
	t.Helper()
	hold := make(chan struct{})
	for i := range held.blobs {
		f.transport.fault(fmt.Sprintf("https://fixed.invalid/%s/%d", held.tag, i), contentTestFault{stallAfter: -1, hold: hold})
	}
	f.acceptOffer(t, f.receiver, held)
	require.Eventually(t, func() bool {
		return f.transport.count(fmt.Sprintf("https://fixed.invalid/%s/0", held.tag)) > 0
	}, 10*time.Second, 10*time.Millisecond, "the held download takes the lane's only worker")
	return hold
}

// The offers a restore attaches are fetched ahead in the order the file
// lists them, which is first use, however many there are: a full lane drops
// none of them. An offer another merge attaches afterwards goes before
// them.
func TestPartPrefetchRestoredOffersInFirstUseOrder(t *testing.T) {
	t.Parallel()
	held := newChainFixture(t, "held")
	demand := newChainFixture(t, "demand")
	var restored []chainFixture
	for i := range 5 {
		restored = append(restored, newChainFixture(t, fmt.Sprintf("restored%d", i)))
	}
	f := newExhaustionFixture(t, append([]chainFixture{held, demand}, restored...)...)
	f.cache.partPrefetchConfig = &partPrefetchConfig{largeBlob: 1, workers: 1, smallWorkers: 1, budget: 1 << 30, ttl: time.Hour, queue: 2}
	t.Cleanup(f.cache.closePartPrefetch)
	hold := holdLargeLane(t, f, held)

	reply, err := f.cache.RestoreValues(f.ctx, restoreTestBundle(t, restored))
	require.NoError(t, err)
	require.True(t, reply.Committed)
	f.acceptOffer(t, persistedListTestResult(t, f.ctx, f.cache, f.srv, "demand-row", &transferTestValue{Text: "pending"}), demand)
	close(hold)

	urls := []string{"https://fixed.invalid/demand/0"}
	for _, chain := range restored {
		urls = append(urls, fmt.Sprintf("https://fixed.invalid/%s/0", chain.tag))
	}
	require.Eventually(t, func() bool {
		for _, url := range urls {
			if f.transport.count(url) == 0 {
				return false
			}
		}
		return true
	}, 10*time.Second, 10*time.Millisecond, "every restored offer and the later offer are fetched ahead")
	for i := 1; i < len(urls); i++ {
		require.Less(t, f.transport.first(urls[i-1]), f.transport.first(urls[i]), "%s is fetched before %s", urls[i-1], urls[i])
	}
	for _, line := range f.logs.lines(t, "remote cache part prefetched") {
		require.NotEqual(t, "skipped_queue", line.fields["outcome"], "no job is dropped")
	}
}

// A restored offer the cache retires before its turn in the backlog is never
// fetched; the next one is.
func TestPartPrefetchRestoredOfferRetiredInBacklog(t *testing.T) {
	t.Parallel()
	held := newChainFixture(t, "held")
	restored := []chainFixture{newChainFixture(t, "retired"), newChainFixture(t, "kept")}
	f := newExhaustionFixture(t, append([]chainFixture{held}, restored...)...)
	f.cache.partPrefetchConfig = &partPrefetchConfig{largeBlob: 1, workers: 1, smallWorkers: 1, budget: 1 << 30, ttl: time.Hour, queue: 2}
	t.Cleanup(f.cache.closePartPrefetch)
	hold := holdLargeLane(t, f, held)

	reply, err := f.cache.RestoreValues(f.ctx, restoreTestBundle(t, restored))
	require.NoError(t, err)
	imported := reply.Imported()
	require.Len(t, imported, len(restored))
	key, err := partAddressKey(f.address)
	require.NoError(t, err)
	f.cache.egraphMu.Lock()
	offer := f.cache.resultsByID[sharedResultID(imported[0].ResultID)].partOfferLocked(key)
	require.NotNil(t, offer)
	f.cache.partOfferGone(offer)
	f.cache.egraphMu.Unlock()
	close(hold)

	require.Eventually(t, func() bool {
		return f.transport.count("https://fixed.invalid/kept/0") > 0
	}, 10*time.Second, 10*time.Millisecond, "the kept offer is fetched")
	require.Zero(t, f.transport.count("https://fixed.invalid/retired/0"), "the retired offer is not")
}
