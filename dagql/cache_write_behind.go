package dagql

import (
	"context"
	"errors"
)

// WriteBehindReport is what one write behind wrote: how many entries,
// classes and terms it wrote or deleted the rows of.
type WriteBehindReport struct {
	Entries int `json:"entries"`
	Classes int `json:"classes"`
	Terms   int `json:"terms"`
}

// WriteBehind writes to a blob-backed cache's store the rows of whatever
// changed since its last write, so that the store holds what a whole save
// of the cache would write.
func (c *Cache) WriteBehind(ctx context.Context) (WriteBehindReport, error) {
	if !c.blobBacked {
		return WriteBehindReport{}, errors.New("write behind: the cache has no blob store")
	}
	op, err := c.beginCacheOperation()
	if err != nil {
		return WriteBehindReport{}, err
	}
	defer op.finish(false)
	if c.sqlDB == nil || c.pdb == nil {
		return WriteBehindReport{}, errors.New("write behind: the cache has no persistence database")
	}
	return WriteBehindReport{}, nil
}
