package dagql

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"gotest.tools/v3/assert"
)

func TestCachePersistenceImportRoundTripAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	cA := cacheA.(*cache)

	key := cacheTestID("persist-import-roundtrip")
	resA, err := cA.GetOrInitCall(ctx, CacheKey{
		ID:            key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 123).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, !resA.HitCache())
	assert.NilError(t, resA.Release(ctx))
	assert.NilError(t, cA.flushPersistenceWorker(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	cacheB, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	cB := cacheB.(*cache)
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()

	resB, err := cB.GetOrInitCall(ctx, CacheKey{
		ID:            key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return nil, errors.New("unexpected initializer call")
	})
	assert.NilError(t, err)
	assert.Assert(t, resB.HitCache())
	assert.Equal(t, 123, cacheTestUnwrapInt(t, resB))
	assert.NilError(t, resB.Release(ctx))
}

func TestCachePersistenceUncleanMarkerWipesStore(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	cA := cacheA.(*cache)

	key := cacheTestID("persist-import-unclean-wipe")
	resA, err := cA.GetOrInitCall(ctx, CacheKey{
		ID:            key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 7).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, resA.Release(ctx))
	assert.NilError(t, cA.flushPersistenceWorker(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "0"))
	assert.NilError(t, closeCacheDBs(db, q))

	cacheB, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	cB := cacheB.(*cache)
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()

	resB, err := cB.GetOrInitCall(ctx, CacheKey{
		ID:            key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 8).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, !resB.HitCache())
	assert.Equal(t, 8, cacheTestUnwrapInt(t, resB))
	assert.NilError(t, resB.Release(ctx))
}

func TestCachePersistenceImportFailureWipesStore(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	cA := cacheA.(*cache)

	key := cacheTestID("persist-import-corrupt-wipe")
	resA, err := cA.GetOrInitCall(ctx, CacheKey{
		ID:            key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 50).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, resA.Release(ctx))
	assert.NilError(t, cA.flushPersistenceWorker(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	_, err = db.Exec(`UPDATE results SET self_payload = x'7B6E6F742D6A736F6E'`)
	assert.NilError(t, err)
	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"))
	assert.NilError(t, closeCacheDBs(db, q))

	cacheB, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	cB := cacheB.(*cache)
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()

	resB, err := cB.GetOrInitCall(ctx, CacheKey{
		ID:            key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 51).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, !resB.HitCache())
	assert.Equal(t, 51, cacheTestUnwrapInt(t, resB))
	assert.NilError(t, resB.Release(ctx))
}
