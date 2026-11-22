package dagql

import (
	"math/bits"
	"sync/atomic"
)

// walkCount counts the calls of one walk over the Cloud cache's graph and
// the entries each read: their total, the most one read, and how many read
// up to each power of two, from which a reader reads the median. Each call
// adds once, with atomics, so a walk under the read lock counts as one
// under the write lock does.
type walkCount struct {
	calls, entries, most atomic.Uint64
	// upTo[i] counts the calls that read at most 2^i entries, and the last
	// those that read more.
	upTo [21]atomic.Uint64
}

func (w *walkCount) add(entries int) {
	n := uint64(entries)
	w.calls.Add(1)
	w.entries.Add(n)
	for {
		most := w.most.Load()
		if n <= most || w.most.CompareAndSwap(most, n) {
			break
		}
	}
	i := 0
	if n > 1 {
		i = bits.Len64(n - 1)
	}
	if i >= len(w.upTo) {
		i = len(w.upTo) - 1
	}
	w.upTo[i].Add(1)
}

// WalkCount is a walk's counts since the cache opened (walkCount).
type WalkCount struct {
	Calls, Entries, Most uint64
	// Median is the power of two the median call read at most, 0 with no
	// call, and OverOne the calls that read more than one.
	Median, OverOne uint64
}

func (w *walkCount) read() WalkCount {
	out := WalkCount{Calls: w.calls.Load(), Entries: w.entries.Load(), Most: w.most.Load()}
	// The median and the calls over one are read from one load of the
	// buckets, which a call adds to apart from the totals: a read between
	// a call's adds would otherwise mix two moments.
	var upTo [len(w.upTo)]uint64
	var calls uint64
	for i := range w.upTo {
		upTo[i] = w.upTo[i].Load()
		calls += upTo[i]
	}
	if calls == 0 {
		return out
	}
	out.OverOne = calls - upTo[0]
	var seen uint64
	for i, n := range upTo {
		seen += n
		if seen*2 >= calls {
			out.Median = 1 << i
			break
		}
	}
	return out
}

// RunWalkStats are, on the Cloud, the walks one run per stored record makes
// over the graph, under the cache's lock, since the cache opened:
//   - RecipesUnder: markRecipesUnderLocked, at a merge's commit, a run's
//     first report that it ran, and an adoption, under the write lock: the
//     entries a call walked, for each call;
//   - EngineRanOver: ownEngineRanOverLocked, which canAdoptRunLocked reaches
//     from a stored part's write and from a part answer: the records over
//     the entry a call read;
//   - BundleWalks: storedBundleWholeWalkLocked's walks of a build, a call a
//     build, under the read lock: a build that walks once counts 1;
//   - CacheGone: NoteCacheGone, at an engine cache's last disconnect, under
//     the write lock: the cache's entries it walked, and CacheGoneHold its
//     hold, in microseconds.
type RunWalkStats struct {
	RecipesUnder, EngineRanOver, BundleWalks, CacheGone, CacheGoneHold WalkCount
}

// RunWalkStats returns the walks' counts (RunWalkStats).
func (c *Cache) RunWalkStats() RunWalkStats {
	return RunWalkStats{
		RecipesUnder:  c.recipesUnderWalks.read(),
		EngineRanOver: c.engineRanOverWalks.read(),
		BundleWalks:   c.bundleWalks.read(),
		CacheGone:     c.cacheGoneWalks.read(),
		CacheGoneHold: c.cacheGoneHold.read(),
	}
}
