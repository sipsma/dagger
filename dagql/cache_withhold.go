package dagql

import (
	"slices"
	"strings"
)

// runPartsLocked returns the parts of res's run: every part besides its
// metadata that its stored record declares complete or pending, inherited
// ones included, sorted by key. A run is stored whole when the entry stores
// each of them; the withholding rule and selection's storing whole both read
// them here. known is false when the Cloud keeps no record of res, so nothing
// says what its run made. Requires egraphMu.
func (c *Cache) runPartsLocked(res *sharedResult) (parts []PersistedPartAddress, known bool, err error) {
	type keyed struct {
		key     string
		address PersistedPartAddress
	}
	var all []keyed
	known, err = c.eachRunPartLocked(res, func(key string, address PersistedPartAddress) bool {
		all = append(all, keyed{key, address})
		return true
	})
	if err != nil {
		return nil, false, err
	}
	slices.SortFunc(all, func(a, b keyed) int { return strings.Compare(a.key, b.key) })
	all = slices.CompactFunc(all, func(a, b keyed) bool { return a.key == b.key })
	for _, part := range all {
		parts = append(parts, part.address)
	}
	return parts, known, nil
}

// eachRunPartLocked calls fn with each part of res's run (runPartsLocked), by
// its address key, until fn returns false; a part its record both declares
// pending and proves complete comes twice. Requires egraphMu.
func (c *Cache) eachRunPartLocked(res *sharedResult, fn func(key string, address PersistedPartAddress) bool) (known bool, err error) {
	var declared []declaredPart
	var complete *partKeySet
	switch {
	case res.stored != nil:
		if _, declared, complete, err = res.storedFactsLocked(); err != nil {
			return false, err
		}
	case res.persistedEnvelope != nil:
		kept, err := c.storedTransferLocked(res)
		if err != nil {
			return false, err
		}
		declared, complete = kept.parts, kept.complete
	default:
		return false, nil
	}
	for _, part := range declared {
		if part.state == "pending" && part.address.Part != "metadata" && !fn(part.key, part.address) {
			return true, nil
		}
	}
	for _, key := range complete.list() {
		if addresses := appendPartAddress(nil, key); len(addresses) == 1 && addresses[0].Part != "metadata" && !fn(key, addresses[0]) {
			return true, nil
		}
	}
	return true, nil
}

// runNotStoredWholeLocked reports whether res stores its run only in part:
// some part of its run (runPartsLocked) isn't among the parts the entry
// stores. Requires egraphMu.
func (c *Cache) runNotStoredWholeLocked(res *sharedResult) (bool, error) {
	partial := false
	_, err := c.eachRunPartLocked(res, func(key string, _ PersistedPartAddress) bool {
		_, stored := res.storedParts[key]
		partial = !stored
		return stored
	})
	return partial, err
}

// entryRunPartsLocked is runPartsLocked for the Cloud's readers of an entry's
// facts: a record it can't read says nothing, as no record does. Requires
// egraphMu.
func (c *Cache) entryRunPartsLocked(res *sharedResult) ([]PersistedPartAddress, bool) {
	parts, known, err := c.runPartsLocked(res)
	if err != nil {
		return nil, false
	}
	return parts, known
}

// withholdBuild decides, for one stored bundle's build, which entries the
// bundle withholds: an entry whose closure, itself included, holds a step
// that can differ (entryCanDifferLocked), whose run ran
// (ownRunRanLocked), and that stores that run only in part
// (runNotStoredWholeLocked). A value over such a step goes out only with the
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
func (w *withholdBuild) withheldLocked(res *sharedResult) (bool, sharedResultID, error) {
	if by, ok := w.memo[res.id]; ok {
		return by != 0, by, nil
	}
	// A cycle is the walk's to report; inside one, nothing is withheld yet.
	w.memo[res.id] = 0
	var by sharedResultID
	if w.c.entryCanDifferLocked(res) && w.c.ownRunRanLocked(res) {
		partial, err := w.c.runNotStoredWholeLocked(res)
		if err != nil {
			return false, 0, err
		}
		if partial {
			by = res.id
		}
	}
	for _, id := range storedClosureChildrenLocked(res) {
		if by != 0 {
			break
		}
		child := w.c.resultsByID[id]
		if child == nil {
			continue
		}
		_, below, err := w.withheldLocked(child)
		if err != nil {
			return false, 0, err
		}
		by = below
	}
	w.memo[res.id] = by
	return by != 0, by, nil
}
