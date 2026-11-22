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
// (runNotStoredWholeLocked); or an entry built over a run the Cloud's merge
// folded into another run's entry (sharedResult.overFoldedRun), whose
// closure names content its value wasn't built over. A value over such a step goes out only with the
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
	if res.overFoldedRun || w.c.entryCanDifferLocked(res) && w.c.ownRunRanLocked(res) && w.c.runNotStoredWholeLocked(res) {
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

// foldedRunRowsLocked returns, on the Cloud, the rows of commit whose
// records stand over a folded run: a row of a step that can differ that the
// merge keeps onto an entry of its recipe whose run isn't the sender's own
// (mergeKeep), so that the rows above it, which the sender built over its
// own run, would name content they weren't built over; and every row above
// such a row in the bundle. The kept row itself installs nothing. A held row
// is the sender's own run only when its holding ran its value; otherwise
// the sender holds the entry's run, which the Cloud sent it. Requires
// egraphMu.
func (c *Cache) foldedRunRowsLocked(commit *valueMergeCommit) map[uint64]bool {
	if !c.blobBacked {
		return nil
	}
	prep := commit.prep
	over := map[uint64]bool{}
	for _, row := range commit.rows {
		folded := false
		if row.action == mergeKeep && row.target != nil && row.target.run != nil && c.entryCanDifferLocked(row.target) {
			if prep.held[row.ordinal] {
				key := HolderKey{Cache: prep.from, Number: row.value.SenderNumber}
				own := RunName{Cache: key.Cache, Number: key.Number, Replacements: row.value.SenderReplacements}
				folded = c.holdingRanLocked(key, own.Replacements) && own != *row.target.run
			} else {
				run, _ := c.mergeRunLocked(prep, row)
				folded = run != *row.target.run
			}
		}
		for _, dep := range row.value.DependencyIDs {
			if folded {
				break
			}
			folded = over[dep]
		}
		if folded {
			over[row.ordinal] = true
		}
	}
	return over
}
