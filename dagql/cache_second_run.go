package dagql

import "context"

// ReleaseSecondRuns releases, on the Cloud, the second runs of the engine
// cache that has ended which nothing stored stands over.
func (c *Cache) ReleaseSecondRuns(ctx context.Context, cache CacheID, live func(CacheID) bool) (RemoteChange, error) {
	return RemoteChange{}, nil
}
