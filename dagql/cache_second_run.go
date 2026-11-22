package dagql

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/opencontainers/go-digest"
)

// A second run is, on the Cloud, another engine's run of a recipe whose
// current entry stores another run: two engines ran one step, and the second
// one's record came after the first's. The Cloud keeps it as one engine's
// cache keeps another value of a recipe beside its current entry
// (cache_current_entry.go): on an entry of its own (mergeBeside), which the
// recipe's index doesn't name, which serves what was built over it, and
// which the sending engine's holding of the step sits on, so that its later
// records land on it (mergeTargetLocked). It is posted under its recipe's
// digest and joins its class as any merged entry does, so a lookup by the
// recipe finds both runs (entriesOfRecipeLocked).
//
// It is owned as anything stored is: by what is built over it, through
// dependency edges, and by holdings. It has no retention of its own: an
// export root that is a second run renews its recipe's current entry's. When
// its engine ends, a second run that nothing stored stands over is released
// (ReleaseSecondRuns), as a prune drops a value only holdings keep. One that
// a recipe's current entry stands over, such as the result its engine built
// over it that no other engine ran, stays: it is kept under it (keptUnder),
// and only its content is needed.

// markKeptUnderLocked marks kept under each second run that res, a stored
// entry that is its recipe's current entry or a second run kept under one,
// stands over through second runs only, and names each entry it marks.
// Each run is marked once: the walk stops at runs already marked and at
// current entries. Requires egraphMu for writing.
func (c *Cache) markKeptUnderLocked(res *sharedResult) {
	stack := []*sharedResult{res}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for depID := range cur.deps {
			dep := c.resultsByID[depID]
			if dep == nil || dep.secondRunOf == "" || dep.keptUnder {
				continue
			}
			dep.keptUnder = true
			c.markEntryLocked(dep.id)
			stack = append(stack, dep)
		}
	}
}

// noteSecondRunContentLocked marks entry, a second run whose record names
// the run of key at count replacements, as a run that ran, when that run's
// own holding reports a part besides its metadata complete from a Cloud
// offer rather than an evaluation, and reports whether it marked it. Its
// engine's record went to the Cloud before its step ran, as the records
// stream sends a lazy chain, and the merge kept it as the engine's run; then
// the engine installed content the Cloud offered it, another run's, rather
// than running the step. That content is the engine's value of the step,
// and what it builds over the entry is built over it: the entry's run has
// content now, which the entry doesn't store, so the withholding rule
// withholds what stands over it until the entry stores the content its
// engine holds, which selection asks of that engine. A recipe's current
// entry is left as it is: an install of its own offers is no run.
// Requires egraphMu for writing.
func (c *Cache) noteSecondRunContentLocked(key HolderKey, entry *sharedResult, replacements uint64) bool {
	if !c.blobBacked || entry == nil || entry.secondRunOf == "" || entry.run == nil || entry.runRan != runNotRan {
		return false
	}
	if run := *entry.run; run.Cache != key.Cache || run.Number != key.Number || run.Replacements != replacements {
		return false
	}
	entry.runRan = runRanByOffer
	c.markEntryLocked(entry.id)
	return true
}

// holdingReportedContentLocked reports whether the holding key, at count
// replacements, has a part besides its metadata complete: its engine holds
// content of the step, whether it ran it or installed an offer. A merge
// that keeps the engine's run apart reads it, when the engine's report of
// installed content came before its export (noteSecondRunContentLocked).
// Requires egraphMu.
func (c *Cache) holdingReportedContentLocked(key HolderKey, replacements uint64) bool {
	_, h := c.holdingLocked(key)
	if h == nil || h.replacements != replacements {
		return false
	}
	for _, part := range h.parts {
		if part.state == heldPartComplete && !strings.Contains(part.key.Value(), metadataPartField) {
			return true
		}
	}
	return false
}

// recheckKeptUnderLocked clears res's kept-under mark when no stored entry
// that is its recipe's current entry, or a second run kept under one, still
// stands over it through a dependency edge (depParents), and does the same,
// in turn, for each second run below it that held its mark only through it:
// a result replaced in place on expiry, or dropped, takes its edges, and the
// runs below it are then needed by nothing stored. Each entry it clears is
// named. Requires egraphMu for writing.
func (c *Cache) recheckKeptUnderLocked(res *sharedResult) {
	stack := []*sharedResult{res}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == nil || cur.secondRunOf == "" || !cur.keptUnder || c.keptUnderByParentLocked(cur) {
			continue
		}
		cur.keptUnder = false
		c.markEntryLocked(cur.id)
		for depID := range cur.deps {
			stack = append(stack, c.resultsByID[depID])
		}
	}
}

// keptUnderByParentLocked reports whether a stored entry that is its
// recipe's current entry, or a second run kept under one, depends on res.
// Requires egraphMu.
func (c *Cache) keptUnderByParentLocked(res *sharedResult) bool {
	for parentID := range res.depParents {
		parent := c.resultsByID[parentID]
		if parent != nil && (parent.secondRunOf == "" || parent.keptUnder) && parent.hasOwnValueLocked() {
			return true
		}
	}
	return false
}

// ReleaseSecondRuns releases, on the Cloud, what the engine cache cache kept
// apart once it has ended: each second run it holds that nothing stored
// stands over loses its value, as a prune drops a value only holdings keep
// (dropValueLocked), and so in turn does each second run that held only
// through what that drop released. A second run whose record names the run
// of an engine that live reports still running is left: that engine can
// still build over it. A recipe's
// current entry is never touched. The entries left with no owner at all are
// collected. Only a blob-backed cache keeps second runs.
func (c *Cache) ReleaseSecondRuns(ctx context.Context, cache CacheID, live func(CacheID) bool) (RemoteChange, error) {
	c.egraphMu.Lock()
	var change RemoteChange
	collect, rerr := c.releaseSecondRunsLocked(ctx, cache, live)
	releases, err := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), collect)
	rerr = errors.Join(rerr, err)
	c.takeChangesLocked(&change)
	c.egraphMu.Unlock()
	return change, errors.Join(rerr, runOnReleaseFuncs(ctx, releases))
}

// releaseSecondRunsLocked is ReleaseSecondRuns' cascade. It returns the
// entries it left with no owner. Requires egraphMu for writing.
func (c *Cache) releaseSecondRunsLocked(ctx context.Context, cache CacheID, live func(CacheID) bool) ([]*sharedResult, error) {
	state := c.remoteCaches[cache]
	if !c.blobBacked || state == nil {
		return nil, nil
	}
	var queue []*sharedResult
	for _, id := range state.entries {
		if res := c.resultsByID[id]; res != nil && res.secondRunOf != "" && res.run != nil {
			queue = append(queue, res)
		}
	}
	slices.SortFunc(queue, compareSharedResults)
	var (
		collect []*sharedResult
		rerr    error
	)
	for len(queue) > 0 {
		res := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if c.resultsByID[res.id] != res || res.secondRunOf == "" || res.run == nil {
			continue
		}
		if res.run.Cache != cache && (live == nil || live(res.run.Cache)) {
			continue
		}
		if res.incomingOwnershipCount.Load() > res.engineHoldingsLocked() {
			// Something stored stands over it.
			continue
		}
		deps, err := c.dropValueLocked(ctx, res)
		rerr = errors.Join(rerr, err)
		queue = append(queue, deps...)
		if res.incomingOwnershipCount.Load() == 0 {
			collect = append(collect, res)
		}
	}
	return collect, rerr
}

// entriesOfRecipeLocked returns, on the Cloud, every entry of recipe: the
// recipe's current entry first, then its second runs and any other entry
// posted under the recipe's digest, by number. Requires egraphMu.
func (c *Cache) entriesOfRecipeLocked(recipe digest.Digest) []*sharedResult {
	current := c.currentEntryForRecipeLocked(recipe)
	var out []*sharedResult
	if current != nil {
		out = append(out, current)
	}
	posting := c.egraphResultsByDigest[recipe.String()]
	if posting == nil {
		return out
	}
	var others []*sharedResult
	for id := range posting.Items() {
		if res := c.resultsByID[id]; res != nil && res != current {
			others = append(others, res)
		}
	}
	slices.SortFunc(others, compareSharedResults)
	return append(out, others...)
}
