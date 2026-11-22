package dagql

// runNotStoredWholeLocked reports whether res stores its run only in part:
// a part besides its metadata that its record declares complete or pending
// (partFacts) isn't among the parts the entry stores. Requires egraphMu.
func (c *Cache) runNotStoredWholeLocked(res *sharedResult) bool {
	missing := func(key string, address PersistedPartAddress) bool {
		if address.Part == "metadata" {
			return false
		}
		_, stored := res.storedParts[key]
		return !stored
	}
	for _, part := range res.partFacts.declaredParts() {
		if part.state == "pending" && missing(part.key, part.address) {
			return true
		}
	}
	for _, key := range res.partFacts.completeParts().list() {
		addresses := appendPartAddress(nil, key)
		if len(addresses) == 1 && missing(key, addresses[0]) {
			return true
		}
	}
	return false
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
func (w *withholdBuild) withheldLocked(res *sharedResult) (bool, sharedResultID) {
	if by, ok := w.memo[res.id]; ok {
		return by != 0, by
	}
	// A cycle is the walk's to report; inside one, nothing is withheld yet.
	w.memo[res.id] = 0
	var by sharedResultID
	if w.c.entryCanDifferLocked(res) && w.c.ownRunRanLocked(res) && w.c.runNotStoredWholeLocked(res) {
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
