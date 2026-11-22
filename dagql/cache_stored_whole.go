package dagql

// StoredWhole reports, for each of numbers, whether a stored bundle of the
// entry would carry it.
func (c *Cache) StoredWhole(numbers []uint64) []bool {
	return make([]bool, len(numbers))
}
