package dagql

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

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
func (c *Cache) ReleaseSecondRuns(ctx context.Context, cache CacheID, live func(CacheID) bool) (ReleasedSecondRuns, RemoteChange, error) {
	c.egraphMu.Lock()
	held := time.Now()
	var change RemoteChange
	collect, dropped, rerr := c.releaseSecondRunsLocked(ctx, &change, cache, live)
	releases, err := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), collect)
	rerr = errors.Join(rerr, err)
	c.takeChangesLocked(&change)
	released := ReleasedSecondRuns{Dropped: dropped, Collected: len(collect), Held: time.Since(held)}
	c.egraphMu.Unlock()
	return released, change, errors.Join(rerr, runOnReleaseFuncs(ctx, releases))
}

// ReleasedSecondRuns is what one ReleaseSecondRuns did: the second runs whose
// value it dropped, the entries it left with no owner and collected, and how
// long it held the cache's lock for writing, the cascade and the collection
// in one hold.
type ReleasedSecondRuns struct {
	Dropped, Collected int
	Held               time.Duration
}

// releaseSecondRunsLocked is ReleaseSecondRuns' cascade. It returns the
// entries it left with no owner, and how many values it dropped. Requires
// egraphMu for writing.
func (c *Cache) releaseSecondRunsLocked(ctx context.Context, change *RemoteChange, cache CacheID, live func(CacheID) bool) ([]*sharedResult, int, error) {
	state := c.remoteCaches[cache]
	if !c.blobBacked || state == nil {
		return nil, 0, nil
	}
	var queue []*sharedResult
	for _, held := range state.entries {
		if res := held.entry; res != nil && c.resultsByID[res.id] == res && res.secondRunOf != "" && res.run != nil {
			queue = append(queue, res)
		}
	}
	slices.SortFunc(queue, compareSharedResults)
	var (
		collect []*sharedResult
		dropped int
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
		deps, err := c.dropValueLocked(ctx, change, res)
		rerr = errors.Join(rerr, err)
		dropped++
		queue = append(queue, deps...)
		if res.incomingOwnershipCount.Load() == 0 {
			collect = append(collect, res)
		}
	}
	return collect, dropped, rerr
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

// oneRunRootsLocked returns, of roots in their order, those a bundle for
// held's recipient leaves out because each would carry another run of a step
// than the one the bundle carries. A bundle carries at most one run of each
// step: the recipient's, when it holds one (heldLocked), else the first the
// roots' dependency closures reach. An engine keeps one entry a recipe:
// another run's record sent to it lands on the entry it has, so what the
// bundle carries over that run would stand over the engine's own, and a
// held row and a full row of one recipe make its merge refuse the bundle
// whole. A root left out is a miss to the recipient, which runs it as its own
// cache would. The walk follows dependencies and stops at an entry the
// recipient holds, as a bundle carries nothing below a held row. It runs only
// on a cache that has kept a second run: on any other, no step has two
// entries. Requires egraphMu.
func (c *Cache) oneRunRootsLocked(roots []*sharedResult, held *HeldBy, nowUnix int64) map[sharedResultID]bool {
	if !c.blobBacked || !c.keptSecondRun.Load() {
		return nil
	}
	chosen := map[digest.Digest]*sharedResult{}
	var left map[sharedResultID]bool
	for _, root := range roots {
		picks := map[digest.Digest]*sharedResult{}
		visited := map[sharedResultID]bool{}
		stack := []*sharedResult{root}
		other := false
		for len(stack) > 0 && !other {
			res := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if res == nil || visited[res.id] {
				continue
			}
			visited[res.id] = true
			if recipe, ok := c.contestedRecipeLocked(res); ok {
				pick := chosen[recipe]
				if pick == nil {
					pick = picks[recipe]
				}
				if pick == nil {
					if pick = c.recipientRunLocked(recipe, held, nowUnix); pick == nil {
						pick = res
					}
					picks[recipe] = pick
				}
				if pick != res {
					other = true
					continue
				}
			}
			if held.heldLocked(res, nowUnix) {
				continue
			}
			for dep := range res.deps {
				stack = append(stack, c.resultsByID[dep])
			}
		}
		if other {
			if left == nil {
				left = map[sharedResultID]bool{}
			}
			left[root.id] = true
			continue
		}
		for recipe, pick := range picks {
			chosen[recipe] = pick
		}
	}
	return left
}

// contestedRecipeLocked returns the recipe of res when the cache stores
// another entry of it beside res: res is a second run of it (secondRunOf), or
// is the entry a second run posted under its recipe is beside. Requires
// egraphMu.
func (c *Cache) contestedRecipeLocked(res *sharedResult) (digest.Digest, bool) {
	if res.secondRunOf != "" {
		return res.secondRunOf, true
	}
	for _, recipe := range res.recipeKeys {
		posting := c.egraphResultsByDigest[recipe.String()]
		if posting == nil || posting.Size() < 2 {
			continue
		}
		for id := range posting.Items() {
			if other := c.resultsByID[id]; other != nil && other != res && other.secondRunOf == recipe {
				return recipe, true
			}
		}
	}
	return "", false
}

// recipientRunLocked returns the entry of recipe held's recipient holds,
// if it holds one (heldLocked): the run of the step a bundle for it carries.
// Requires egraphMu.
func (c *Cache) recipientRunLocked(recipe digest.Digest, held *HeldBy, nowUnix int64) *sharedResult {
	if held == nil {
		return nil
	}
	for _, res := range c.entriesOfRecipeLocked(recipe) {
		if held.heldLocked(res, nowUnix) {
			return res
		}
	}
	return nil
}

// oneRunRootsOfLocked is oneRunRootsLocked of roots named by number, as the
// cache's listings and pricing take them: the numbers it leaves out, of
// those the cache has a value for. Requires egraphMu.
func (c *Cache) oneRunRootsOfLocked(roots []uint64, held *HeldBy, nowUnix int64) map[sharedResultID]bool {
	if !c.blobBacked || !c.keptSecondRun.Load() {
		return nil
	}
	entries := make([]*sharedResult, 0, len(roots))
	seen := make(map[sharedResultID]bool, len(roots))
	for _, number := range roots {
		res := c.resultsByID[sharedResultID(number)]
		if res == nil || res.noValueLocked() || seen[res.id] {
			continue
		}
		seen[res.id] = true
		entries = append(entries, res)
	}
	return c.oneRunRootsLocked(entries, held, nowUnix)
}

// oneRunLeftOut is oneRunRootsOfLocked in a hold of its own, for the cache's
// listings and pricing, which walk their roots in pieces: the roots a
// StoredBundle of the same roots for the same recipient leaves out
// (StoredBundleResult.OtherRun), which they then carry nothing of.
func (c *Cache) oneRunLeftOut(roots []uint64, held *HeldBy, nowUnix int64) map[sharedResultID]bool {
	if !c.blobBacked || !c.keptSecondRun.Load() {
		return nil
	}
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	if held != nil {
		defer c.rlockEngineLocked(held.Cache).runlock()
	}
	return c.oneRunRootsOfLocked(roots, held, nowUnix)
}

// leaveOutOtherRunsLocked returns roots without those a bundle for held's
// recipient leaves out for another run of a step (oneRunRootsLocked), and
// those, by number, in their order (StoredBundleResult.OtherRun). Requires
// egraphMu.
func (c *Cache) leaveOutOtherRunsLocked(roots []*sharedResult, held *HeldBy, nowUnix int64) ([]*sharedResult, []uint64) {
	left := c.oneRunRootsLocked(roots, held, nowUnix)
	if len(left) == 0 {
		return roots, nil
	}
	kept := make([]*sharedResult, 0, len(roots)-len(left))
	var out []uint64
	for _, res := range roots {
		if left[res.id] {
			out = append(out, uint64(res.id))
			continue
		}
		kept = append(kept, res)
	}
	return kept, out
}
