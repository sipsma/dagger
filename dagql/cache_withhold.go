package dagql

// runNotStoredWholeLocked reports whether res stores its run only in part:
// a part besides its metadata that its record declares complete or pending
// isn't among the parts the entry stores. Requires egraphMu.
func (c *Cache) runNotStoredWholeLocked(res *sharedResult) (bool, error) {
	var declared []declaredPart
	var complete *partKeySet
	switch {
	case res.stored != nil:
		var err error
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
	missing := func(key string, address PersistedPartAddress) bool {
		if address.Part == "metadata" {
			return false
		}
		_, stored := res.storedParts[key]
		return !stored
	}
	for _, part := range declared {
		if part.state == "pending" && missing(part.key, part.address) {
			return true, nil
		}
	}
	for _, key := range complete.list() {
		addresses := appendPartAddress(nil, key)
		if len(addresses) == 1 && missing(key, addresses[0]) {
			return true, nil
		}
	}
	return false, nil
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
