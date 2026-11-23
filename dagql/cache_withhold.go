package dagql

import (
	"slices"
	"strings"

	"github.com/opencontainers/go-digest"
)

// runPartsLocked returns the parts of res's run: every part besides its
// metadata that its stored record declares complete or pending (partFacts),
// inherited ones included, sorted by key. A run is stored whole when the
// entry stores each of them; the withholding rule and selection's storing
// whole both read them here. known is false when the Cloud keeps no record of
// res, so nothing says what its run made. Requires egraphMu.
func (c *Cache) runPartsLocked(res *sharedResult) (parts []PersistedPartAddress, known bool) {
	type keyed struct {
		key     string
		address PersistedPartAddress
	}
	var all []keyed
	known = c.eachRunPartLocked(res, func(key string, address PersistedPartAddress) bool {
		all = append(all, keyed{key, address})
		return true
	})
	slices.SortFunc(all, func(a, b keyed) int { return strings.Compare(a.key, b.key) })
	all = slices.CompactFunc(all, func(a, b keyed) bool { return a.key == b.key })
	for _, part := range all {
		parts = append(parts, part.address)
	}
	return parts, known
}

// eachRunPartLocked calls fn with each part of res's run (runPartsLocked), by
// its address key, until fn returns false; a part its record both declares
// pending and proves complete comes twice. It reports whether the Cloud keeps
// a record of res. Requires egraphMu.
func (c *Cache) eachRunPartLocked(res *sharedResult, fn func(key string, address PersistedPartAddress) bool) bool {
	if res.stored == nil && res.persistedEnvelope == nil {
		return false
	}
	for _, part := range res.partFacts.declaredParts() {
		if part.state == "pending" && part.address.Part != "metadata" && !fn(part.key, part.address) {
			return true
		}
	}
	for _, key := range res.partFacts.completeParts().list() {
		if addresses := appendPartAddress(nil, key); len(addresses) == 1 && addresses[0].Part != "metadata" && !fn(key, addresses[0]) {
			return true
		}
	}
	return true
}

// runNotStoredWholeLocked reports whether res stores its run only in part:
// some part of its run (runPartsLocked) isn't among the parts the entry
// stores. Requires egraphMu.
func (c *Cache) runNotStoredWholeLocked(res *sharedResult) bool {
	partial := false
	c.eachRunPartLocked(res, func(key string, _ PersistedPartAddress) bool {
		_, stored := res.storedParts[key]
		partial = !stored
		return stored
	})
	return partial
}

// withholdBuild decides, for one stored bundle's build, which entries the
// bundle withholds: an entry whose closure, itself included, holds a step
// that can differ (entryCanDifferLocked), whose run ran
// (ownRunRanLocked), and that stores that run only in part
// (runNotStoredWholeLocked); or an entry an engine built over its own run of
// a step the Cloud keeps folded under another run's record, directly or
// through steps that follow from their inputs (overFoldedRunLocked), whose
// closure names content it wasn't built over. A value over such a step goes out only with the
// run whole: an engine that took it and then ran the step itself, for want
// of its content, would hold one run's values over another run's bytes. The
// closure is the one a bundle walks (storedClosureChildrenLocked), whatever
// the recipient holds.
type withholdBuild struct {
	c *Cache
	// held is what the bundle's recipient holds: an entry it holds another
	// run of is withheld with what is over it (recipientHoldsOtherRunLocked).
	held *HeldBy
	// now is the bundle's time, by which the recipient's holdings expire.
	now int64
	// carried is what the bundle carries of the steps that can differ
	// (otherRunCarriedLocked).
	carried carriedRuns
	// memo has, for each entry read, the first entry of its closure found
	// that withholds it, or 0.
	memo map[sharedResultID]sharedResultID
	// reach has, for each entry read, the engines whose own runs are folded
	// at it or below it through entries that follow from their inputs
	// (foldedReachLocked).
	reach map[sharedResultID][]CacheID
}

// newWithholdBuild returns a withholding judgment for one stored bundle's
// build, or for one walk that prices or lists what such a bundle carries
// without building it (StoredSizes, StoredClosure, StoredLowerHalf), so
// that each withholds what the bundle would, for the recipient held, its
// holdings judged at nowUnix.
func (c *Cache) newWithholdBuild(held *HeldBy, nowUnix int64) *withholdBuild {
	return &withholdBuild{c: c, held: held, now: nowUnix, memo: map[sharedResultID]sharedResultID{}, reach: map[sharedResultID][]CacheID{}}
}

// keptStoredRootsLocked returns the entries of roots that a bundle of them
// keeps (keptRootsLocked): a walk that prices or lists what such a bundle
// carries walks only these. A root the cache has no value for is none.
// Requires egraphMu.
func (w *withholdBuild) keptStoredRootsLocked(roots []uint64) map[sharedResultID]bool {
	var entries []*sharedResult
	seen := map[sharedResultID]bool{}
	for _, number := range roots {
		if res := w.c.resultsByID[sharedResultID(number)]; res != nil && !res.noValueLocked() && !seen[res.id] {
			seen[res.id] = true
			entries = append(entries, res)
		}
	}
	kept := make(map[sharedResultID]bool, len(entries))
	for _, res := range w.keptRootsLocked(entries, &StoredBundleResult{}) {
		kept[res.id] = true
	}
	return kept
}

// callOverWithheldLocked reports whether a bundle sends no call chosen,
// because an input it names is withheld: every input, the recipient's own
// included, since a holding of one may be a record whose step the
// recipient would run. The call goes once its input's run is stored whole.
// Requires egraphMu.
func (w *withholdBuild) callOverWithheldLocked(chosen *keptCall) bool {
	for i, recipe := range chosen.id.inputs {
		input := w.c.currentEntryForRecipeLocked(recipe)
		if chosen.id.witnesses[i] || input == nil {
			continue
		}
		if withheld, _ := w.withheldLocked(input); withheld {
			return true
		}
	}
	return false
}

// withheldLocked reports whether the bundle withholds res, and the entry it
// found that does: the first in its closure, depth first, whose run is
// stored only in part. Requires egraphMu.
func (w *withholdBuild) withheldLocked(res *sharedResult) (bool, sharedResultID) {
	if by, ok := w.memo[res.id]; ok {
		return by != 0, by
	}
	// A cycle is the walk's to report; inside one, nothing is withheld yet.
	w.memo[res.id] = 0
	var by sharedResultID
	if w.recipientHoldsOtherRunLocked(res) || w.overFoldedRunLocked(res) || w.c.entryCanDifferLocked(res) && w.c.ownRunRanLocked(res) && w.c.runNotStoredWholeLocked(res) {
		by = res.id
	}
	// Below an entry whose content is its own whatever runs its inputs are
	// (runsBelowDontShowLocked), nothing changes what a value over it holds.
	for _, id := range storedClosureChildrenLocked(res) {
		if by != 0 || w.runsBelowDontShowLocked(res) {
			break
		}
		child := w.c.resultsByID[id]
		if child == nil {
			continue
		}
		_, by = w.withheldLocked(child)
	}
	w.memo[res.id] = by
	return by != 0, by
}

// keptRootsLocked returns, in order, the roots the bundle keeps, and adds
// each one it withholds to out, with the entry that withholds it and why:
// the rule (withheldLocked), then, so that only roots the bundle keeps
// count as carried, another run of a step the bundle carries
// (otherRunCarriedLocked). Requires egraphMu.
func (w *withholdBuild) keptRootsLocked(roots []*sharedResult, out *StoredBundleResult) []*sharedResult {
	c := w.c
	kept := make([]*sharedResult, 0, len(roots))
	for _, res := range roots {
		if withheld, by := w.withheldLocked(res); withheld {
			out.Withheld = append(out.Withheld, uint64(res.id))
			out.WithheldOn = append(out.WithheldOn, uint64(by))
			_, why := c.entryCanDifferWhyLocked(c.resultsByID[by])
			switch {
			case w.recipientHoldsOtherRunLocked(c.resultsByID[by]):
				why = "otherRun"
			case w.overFoldedRunLocked(c.resultsByID[by]):
				why = "overFoldedRun"
			}
			out.WithheldOnField = append(out.WithheldOnField, entryFieldLocked(c.resultsByID[by])+"/"+why)
			continue
		}
		if withheld, by := w.otherRunCarriedLocked(res); withheld {
			out.Withheld = append(out.Withheld, uint64(res.id))
			out.WithheldOn = append(out.WithheldOn, uint64(by))
			out.WithheldOnField = append(out.WithheldOnField, entryFieldLocked(c.resultsByID[by])+"/otherRun")
			continue
		}
		kept = append(kept, res)
	}
	return kept
}

// recipientHoldsFoldedRunLocked reports whether the recipient still holds
// its own run of res's step that the Cloud keeps folded under res: an
// unexpired holding of res at that run's number and replacement count. A
// holding it has since been served res's own run by is another number.
// Requires egraphMu.
func (w *withholdBuild) recipientHoldsFoldedRunLocked(res *sharedResult) bool {
	if w.held.Not[uint64(res.id)] {
		return false
	}
	for _, run := range res.foldedRuns {
		if run.Cache != w.held.Cache {
			continue
		}
		for _, ref := range res.cacheHolderRefsLocked(run.Cache) {
			if ref.key.Number == run.Number && ref.h.replacements == run.Replacements && (ref.h.expiresAtUnix == 0 || ref.h.expiresAtUnix > w.now) {
				return true
			}
		}
	}
	return false
}

// carriedRuns is what a bundle carries of the steps that can differ: the
// entries of the roots it keeps, and the run it carries of each recipe.
type carriedRuns struct {
	entries map[sharedResultID]bool
	runs    map[digest.Digest]*sharedResult
}

// otherRunCarriedLocked reports whether the closures of roots, which the
// bundle would add after what it has kept (carried), bring it a run of a
// step of which the bundle, or those closures themselves, carry another
// run, and the entry that does. An engine decides each recipe once: a bundle
// with two runs of a step lands the second run's values over the first
// run's entry (mergeKeep), with no error, as it would over a run the engine
// already held (recipientHoldsOtherRunLocked). The first run a bundle
// carries stays. When the closures bring no other run, the bundle carries
// them. The closure is the one the rule walks (storedClosureChildrenLocked).
// Requires egraphMu.
func (w *withholdBuild) otherRunCarriedLocked(roots ...*sharedResult) (bool, sharedResultID) {
	carried := &w.carried
	if carried.entries == nil {
		carried.entries, carried.runs = map[sharedResultID]bool{}, map[digest.Digest]*sharedResult{}
	}
	seen := map[sharedResultID]bool{}
	runs := map[digest.Digest]*sharedResult{}
	stack := slices.Clone(roots)
	for len(stack) > 0 {
		res := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[res.id] || carried.entries[res.id] {
			continue
		}
		seen[res.id] = true
		if res.run != nil && w.c.entryCanDifferLocked(res) {
			recipes := slices.Clip(res.recipeKeys)
			if res.secondRunOf != "" {
				recipes = append(recipes, res.secondRunOf)
			}
			for _, recipe := range recipes {
				prev, ok := carried.runs[recipe]
				if !ok {
					prev, ok = runs[recipe]
				}
				if ok && *prev.run != *res.run {
					return true, res.id
				}
				if !ok {
					runs[recipe] = res
				}
			}
		}
		for _, id := range storedClosureChildrenLocked(res) {
			if child := w.c.resultsByID[id]; child != nil {
				stack = append(stack, child)
			}
		}
	}
	for id := range seen {
		carried.entries[id] = true
	}
	for recipe, run := range runs {
		carried.runs[recipe] = run
	}
	return false, 0
}

// runsBelowDontShowLocked reports whether no run below res changes what a
// value over res holds: res's record claims its result is a function of
// hashes its own key holds (FollowsKey), or the recipient holds a run of
// res's step whose content is res's (sameContentLocked), so that what is
// over res sees its content, not its inputs' runs. The walk that withholds
// doesn't go below it. Requires egraphMu.
func (w *withholdBuild) runsBelowDontShowLocked(res *sharedResult) bool {
	if w.c.entryFollowsLocked(res) == FollowsKey {
		return true
	}
	if w.held == nil || w.c.entryContentDigestLocked(res) == "" {
		return false
	}
	for _, recipe := range entryRecipesLocked(res) {
		for _, other := range w.c.entriesOfRecipeLocked(recipe) {
			if w.c.sameContentLocked(other, res) && w.held.heldLocked(other, w.now) {
				return true
			}
		}
	}
	return false
}

// entryRecipesLocked returns the recipes res is an entry of: those it is
// indexed under, and the recipe it is a second run of. Requires egraphMu.
func entryRecipesLocked(res *sharedResult) []digest.Digest {
	recipes := slices.Clip(res.recipeKeys)
	if res.secondRunOf != "" {
		recipes = append(recipes, res.secondRunOf)
	}
	return recipes
}

// recipientHoldsOtherRunLocked reports whether the bundle's recipient holds
// another run of res's step: the recipient answered so (HeldBy.OtherRun), or
// res can differ and the recipient still holds its own run of it, folded
// under res (recipientHoldsFoldedRunLocked), or it holds another entry of one of res's recipes
// (entriesOfRecipeLocked) whose run isn't res's. A value over res
// would land on the recipient's own run of the step, whose entry its merge
// keeps (mergeKeep), with no error. What the Cloud knows of the recipient's
// holdings lags, so the engine refuses such a merge itself too
// (otherRunRowsLocked). A run the Cloud can't name says nothing. Requires
// egraphMu.
func (w *withholdBuild) recipientHoldsOtherRunLocked(res *sharedResult) bool {
	switch {
	case w.held == nil:
		return false
	case w.held.OtherRun[uint64(res.id)]:
		return true
	case res.run == nil || !w.c.entryCanDifferLocked(res):
		return false
	case w.recipientHoldsFoldedRunLocked(res):
		return true
	}
	for _, recipe := range entryRecipesLocked(res) {
		for _, other := range w.c.entriesOfRecipeLocked(recipe) {
			if other != res && other.run != nil && *other.run != *res.run && !w.c.sameContentLocked(other, res) && w.held.heldLocked(other, w.now) {
				return true
			}
		}
	}
	return false
}

// overFoldedRunLocked reports whether res, a record of an engine's run,
// stands over that engine's own run of a step the Cloud keeps folded under
// another run's record (sharedResult.foldedRuns): its closure, followed
// down through entries that follow from their inputs, reaches an entry that
// keeps that engine's run folded. An entry that follows from its inputs is
// the run of its inputs, so an engine's copy of it over its own run of a
// step below is that run too; an entry that can differ is a run of its own,
// whose foldedRuns answer for it, and the walk stops there. Requires
// egraphMu.
func (w *withholdBuild) overFoldedRunLocked(res *sharedResult) bool {
	if res.run == nil || w.c.entryFollowsLocked(res) == FollowsKey {
		return false
	}
	for _, id := range storedClosureChildrenLocked(res) {
		if child := w.c.resultsByID[id]; child != nil && slices.Contains(w.foldedReachLocked(child), res.run.Cache) {
			return true
		}
	}
	return false
}

// foldedReachLocked returns the engines whose own runs are folded at res or
// below it through entries that follow from their inputs: res's own
// foldedRuns and, unless res can differ, its closure children's. Requires
// egraphMu.
func (w *withholdBuild) foldedReachLocked(res *sharedResult) []CacheID {
	if reach, ok := w.reach[res.id]; ok {
		return reach
	}
	// A cycle is the walk's to report; inside one, nothing is reached yet.
	w.reach[res.id] = nil
	var reach []CacheID
	for _, run := range res.foldedRuns {
		if !slices.Contains(reach, run.Cache) {
			reach = append(reach, run.Cache)
		}
	}
	// Nor below a step whose content its key addresses: no run under it
	// shows in what is over it.
	if !w.c.entryCanDifferLocked(res) && w.c.entryFollowsLocked(res) != FollowsKey {
		for _, id := range storedClosureChildrenLocked(res) {
			if child := w.c.resultsByID[id]; child != nil {
				for _, cache := range w.foldedReachLocked(child) {
					if !slices.Contains(reach, cache) {
						reach = append(reach, cache)
					}
				}
			}
		}
	}
	w.reach[res.id] = reach
	return reach
}

// noteFoldedRowLocked notes, on the Cloud, a row of prep's merge kept onto
// its recipe's entry (mergeKeep) whose sender ran its own run of a step that
// can differ, which isn't the run the entry's record names: the entry now
// keeps that run folded (addFoldedRunLocked). A record's run is the
// sender's own unless the sender imported it (mergeRunLocked); a held row,
// which carries no record, is the sender's own run when its holding ran its
// value. A row whose run hasn't run yet decides nothing: the Cloud learns
// later which run the sender holds (noteHoldingRanLocked). Requires egraphMu
// for writing.
func (c *Cache) noteFoldedRowLocked(prep *valueMergePrep, row *mergeRow) {
	target := row.target
	if target == nil || target.run == nil || !c.entryCanDifferLocked(target) {
		return
	}
	var run RunName
	var ran bool
	if prep.held[row.ordinal] {
		run = RunName{Cache: prep.from, Number: row.value.SenderNumber, Replacements: row.value.SenderReplacements}
		ran = c.holdingRanLocked(HolderKey{Cache: run.Cache, Number: run.Number}, run.Replacements)
	} else {
		var by runRanBy
		run, by = c.mergeRunLocked(prep, row)
		ran = by != runNotRan
	}
	if ran && run != *target.run {
		c.addFoldedRunLocked(target, run)
	}
}

// addFoldedRunLocked notes that res keeps run, an engine's own, folded under
// another run's record, marks res changed, and reports whether it was new.
// Requires egraphMu for writing.
func (c *Cache) addFoldedRunLocked(res *sharedResult, run RunName) bool {
	if slices.Contains(res.foldedRuns, run) {
		return false
	}
	res.foldedRuns = append(res.foldedRuns, run)
	c.markEntryLocked(res.id)
	return true
}
