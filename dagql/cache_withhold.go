package dagql

import (
	"slices"
	"strings"
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
	// held is what the bundle's recipient holds: an entry it answered it
	// holds another run of (HeldBy.OtherRun) is withheld with what is over
	// it.
	held *HeldBy
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
// that each withholds what the bundle would, for the recipient held.
func (c *Cache) newWithholdBuild(held *HeldBy) *withholdBuild {
	return &withholdBuild{c: c, held: held, memo: map[sharedResultID]sharedResultID{}, reach: map[sharedResultID][]CacheID{}}
}

// keptRootsLocked returns, in order, the roots a bundle of them keeps, and
// adds each one it withholds to out, with the entry that withholds it and
// why (withheldLocked). Requires egraphMu.
func (w *withholdBuild) keptRootsLocked(roots []*sharedResult, out *StoredBundleResult) []*sharedResult {
	c := w.c
	kept := make([]*sharedResult, 0, len(roots))
	for _, res := range roots {
		if withheld, by := w.withheldLocked(res); withheld {
			out.Withheld = append(out.Withheld, uint64(res.id))
			out.WithheldOn = append(out.WithheldOn, uint64(by))
			_, why := c.entryCanDifferWhyLocked(c.resultsByID[by])
			switch {
			case w.held != nil && w.held.OtherRun[uint64(by)]:
				why = "otherRun"
			case w.overFoldedRunLocked(c.resultsByID[by]):
				why = "overFoldedRun"
			}
			out.WithheldOnField = append(out.WithheldOnField, entryFieldLocked(c.resultsByID[by])+"/"+why)
			continue
		}
		kept = append(kept, res)
	}
	return kept
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
	if w.held != nil && w.held.OtherRun[uint64(res.id)] || w.overFoldedRunLocked(res) || w.c.entryCanDifferLocked(res) && w.c.ownRunRanLocked(res) && w.c.runNotStoredWholeLocked(res) {
		by = res.id
	}
	for _, id := range storedClosureChildrenLocked(res) {
		if by != 0 {
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
	if res.run == nil {
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
	if !w.c.entryCanDifferLocked(res) {
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
