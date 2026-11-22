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
// a step the Cloud keeps folded under another run's record
// (overFoldedRunLocked), whose closure names content it wasn't built over. A value over such a step goes out only with the
// run whole: an engine that took it and then ran the step itself, for want
// of its content, would hold one run's values over another run's bytes. The
// closure is the one a bundle walks (storedClosureChildrenLocked), whatever
// the recipient holds.
type withholdBuild struct {
	c *Cache
	// memo has, for each entry read, the first entry of its closure found
	// that withholds it, or 0.
	memo map[sharedResultID]sharedResultID
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
	if w.c.overFoldedRunLocked(res) || w.c.entryCanDifferLocked(res) && w.c.ownRunRanLocked(res) && w.c.runNotStoredWholeLocked(res) {
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
// stands directly over an entry that keeps that engine's own run folded
// under another run's record (sharedResult.foldedRuns): the engine built res
// over its run, which the closure doesn't name. Requires egraphMu.
func (c *Cache) overFoldedRunLocked(res *sharedResult) bool {
	if res.run == nil {
		return false
	}
	for _, id := range storedClosureChildrenLocked(res) {
		if child := c.resultsByID[id]; child != nil && slices.Contains(child.foldedRuns, res.run.Cache) {
			return true
		}
	}
	return false
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
		c.addFoldedRunLocked(target, run.Cache)
	}
}

// addFoldedRunLocked notes that res keeps cache's own run folded under
// another run's record, marks res changed, and reports whether it was new.
// Requires egraphMu for writing.
func (c *Cache) addFoldedRunLocked(res *sharedResult, cache CacheID) bool {
	if slices.Contains(res.foldedRuns, cache) {
		return false
	}
	res.foldedRuns = append(res.foldedRuns, cache)
	c.markEntryLocked(res.id)
	return true
}
