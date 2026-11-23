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

// oneRunCheck is a build's choice of the run of each step it carries, root
// by root: a bundle carries at most one run of each step, the recipient's
// when it holds one (ownsRunLocked), else the first its roots reach. An
// engine keeps one entry a recipe: another run's record sent to it lands on
// the entry it has, so what the bundle carries over that run would stand over
// the engine's own, and a held row and a full row of one recipe make its
// merge refuse the bundle whole. A root that would carry another run is left
// out, a miss to the recipient, which runs it as its own cache would. The
// check judges what each root's walk actually added (its closure, its kept
// calls' inputs, and what a whole holding's walk exposes), so a bundle, its
// listing and its pricing, each walking its roots in order, leave out the
// same roots. A nil check, on a cache that never kept a second run, where no
// step has two entries, passes every root.
type oneRunCheck struct {
	held   *HeldBy
	now    int64
	chosen map[digest.Digest]*sharedResult
}

func (c *Cache) newOneRunCheck(held *HeldBy, nowUnix int64) *oneRunCheck {
	if !c.blobBacked || !c.keptSecondRun.Load() {
		return nil
	}
	return &oneRunCheck{held: held, now: nowUnix, chosen: map[digest.Digest]*sharedResult{}}
}

// rootLocked reports whether a root whose walk added entries carries only
// runs the check carries, and if so takes its choices. Requires egraphMu.
func (c *Cache) oneRunRootLocked(chk *oneRunCheck, entries []*sharedResult) bool {
	if chk == nil {
		return true
	}
	var picks map[digest.Digest]*sharedResult
	for _, res := range entries {
		recipe, ok := c.contestedRecipeLocked(res)
		if !ok {
			continue
		}
		pick := chk.chosen[recipe]
		if pick == nil {
			pick = picks[recipe]
		}
		if pick == nil {
			if pick = c.recipientRunLocked(recipe, chk.held, chk.now); pick == nil {
				pick = res
			}
			if picks == nil {
				picks = map[digest.Digest]*sharedResult{}
			}
			picks[recipe] = pick
		}
		if pick != res {
			return false
		}
	}
	for recipe, pick := range picks {
		chk.chosen[recipe] = pick
	}
	return true
}

// otherRunRootLocked returns the index of the first of build's roots whose
// entries, as the build's owners name them, carry another run of a step than
// the build carries (oneRunRootLocked), or -1. The build's order has each
// root's entries after the earlier roots'. Requires egraphMu.
func (c *Cache) otherRunRootLocked(build *storedBundleBuild) int {
	chk := c.newOneRunCheck(build.heldBy, build.now)
	if chk == nil {
		return -1
	}
	for i := 0; i < len(build.order); {
		owner := build.owners[build.order[i].id]
		j := i
		for j < len(build.order) && build.owners[build.order[j].id] == owner {
			j++
		}
		if !c.oneRunRootLocked(chk, build.order[i:j]) {
			return owner
		}
		i = j
	}
	return -1
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
// if it holds one (ownsRunLocked): the run of the step a bundle for it
// carries. Requires egraphMu.
func (c *Cache) recipientRunLocked(recipe digest.Digest, held *HeldBy, nowUnix int64) *sharedResult {
	if held == nil {
		return nil
	}
	for _, res := range c.entriesOfRecipeLocked(recipe) {
		if held.ownsRunLocked(res, nowUnix) {
			return res
		}
	}
	return nil
}
