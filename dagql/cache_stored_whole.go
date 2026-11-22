package dagql

// StoredWhole reports, for each of numbers, whether a stored bundle of the
// entry would carry it: the cache has the entry, it stores a value, and the
// withholding rule doesn't withhold it (withholdBuild), whatever the
// recipient holds. A lookup asks it of the stored entries of a miss's class
// when there is more than one, to root the miss on one whose chain is whole
// rather than on the lowest, which a bundle would withhold. One walk under
// one hold answers every entry, each entry of a closure read once.
func (c *Cache) StoredWhole(numbers []uint64) []bool {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	out := make([]bool, len(numbers))
	withhold := &withholdBuild{c: c, memo: map[sharedResultID]sharedResultID{}, reach: map[sharedResultID][]CacheID{}}
	for i, number := range numbers {
		res := c.resultsByID[sharedResultID(number)]
		if res == nil || res.noValueLocked() {
			continue
		}
		withheld, _ := withhold.withheldLocked(res)
		out[i] = !withheld
	}
	return out
}
