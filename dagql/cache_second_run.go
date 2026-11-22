package dagql

import (
	"context"
	"errors"
	"fmt"
	"slices"

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

// ReleaseSecondRuns releases, on the Cloud, what the engine cache cache kept
// apart once it has ended: each second run of its own that nothing stored
// stands over loses its value, as a prune drops a value only holdings keep
// (dropValueLocked), and so in turn does each second run that held only
// through what that drop released. A second run of an engine that live
// reports still running is left: it can still build over it. A recipe's
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
		if res := c.resultsByID[id]; res != nil && res.secondRunOf != "" && res.run != nil && res.run.Cache == cache {
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

// restoreSecondRuns marks, on a restored Cloud, its second runs, which the
// saved store doesn't name: a restored entry that stores a record and that
// its recipe's index didn't name when it was saved (indexed) is a second
// run of its own recipe, since the Cloud keeps no other value beside a
// current entry. Then each restored current entry that stores a record
// marks the second runs it stands over kept under it. Deriving a recipe
// reads other entries' frames under egraphMu, so the recipes are derived
// first, as indexRestoredEntries does.
func (c *Cache) restoreSecondRuns(indexed map[sharedResultID]struct{}) error {
	if !c.blobBacked {
		return nil
	}
	c.egraphMu.RLock()
	var rows []*sharedResult
	for id, res := range c.resultsByID {
		if _, ok := indexed[id]; !ok && res.hasOwnValueLocked() {
			rows = append(rows, res)
		}
	}
	c.egraphMu.RUnlock()
	slices.SortFunc(rows, compareSharedResults)
	recipes := make([]digest.Digest, len(rows))
	for i, res := range rows {
		frame := res.loadResultCall()
		if frame == nil {
			return fmt.Errorf("restore second run %d: missing call frame", res.id)
		}
		recipe, err := frame.deriveRecipeDigest(c)
		if err != nil {
			return fmt.Errorf("restore second run %d: %w", res.id, err)
		}
		recipes[i] = recipe
	}
	c.egraphMu.Lock()
	defer c.egraphMu.Unlock()
	for i, res := range rows {
		res.secondRunOf = recipes[i]
	}
	for id := range indexed {
		if res := c.resultsByID[id]; res != nil && res.hasOwnValueLocked() {
			c.markKeptUnderLocked(res)
		}
	}
	return nil
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
