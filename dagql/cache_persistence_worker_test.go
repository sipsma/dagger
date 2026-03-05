package dagql

import (
	"context"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestCachePersistenceWorkerPersistsPersistableResult(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	c := cacheIface.(*cache)
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	key := cacheTestID("persist-worker-root")
	res, err := c.GetOrInitCall(ctx, CacheKey{
		ID:            key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 42).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, res.Release(ctx))
	assert.NilError(t, c.flushPersistenceWorker(ctx))

	var deleted int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT deleted FROM results WHERE result_key = ?`, key.Digest().String()).Scan(&deleted)
	assert.NilError(t, err)
	assert.Equal(t, 0, deleted)
}

func TestCachePersistenceWorkerTombstonesPrunedPersistedResult(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	c := cacheIface.(*cache)
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	key := cacheTestID("persist-worker-prune")
	res, err := c.GetOrInitCall(ctx, CacheKey{
		ID:            key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 99).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, res.Release(ctx))
	assert.NilError(t, c.flushPersistenceWorker(ctx))

	_, err = c.Prune(ctx, []CachePrunePolicy{{All: true}})
	assert.NilError(t, err)
	assert.NilError(t, c.flushPersistenceWorker(ctx))

	var deleted int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT deleted FROM results WHERE result_key = ?`, key.Digest().String()).Scan(&deleted)
	assert.NilError(t, err)
	assert.Equal(t, 1, deleted)
}
