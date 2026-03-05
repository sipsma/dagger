package dagql

import (
	"context"
	"path/filepath"
	"testing"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCachePersistenceWorkerIdempotentUpsertAndTombstone(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	c := cacheIface.(*cache)
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	key := cacheTestID("persist-worker-idempotent")
	res, err := c.GetOrInitCall(ctx, CacheKey{
		ID:            key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 17).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, res.Release(ctx))
	assert.NilError(t, c.flushPersistenceWorker(ctx))
	assert.NilError(t, c.flushPersistenceWorker(ctx))

	resultKey := key.Digest().String()
	var rowCount int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM results WHERE result_key = ?`, resultKey).Scan(&rowCount)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(rowCount, 1))

	var deleted int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT deleted FROM results WHERE result_key = ?`, resultKey).Scan(&deleted)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(deleted, 0))

	root := res.cacheSharedResult()
	assert.Assert(t, root != nil)

	c.egraphMu.Lock()
	c.emitPersistTombstonesForRootLocked(root)
	c.egraphMu.Unlock()
	assert.NilError(t, c.flushPersistenceWorker(ctx))
	assert.NilError(t, c.flushPersistenceWorker(ctx))

	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM results WHERE result_key = ?`, resultKey).Scan(&rowCount)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(rowCount, 1))

	err = c.sqlDB.QueryRowContext(ctx, `SELECT deleted FROM results WHERE result_key = ?`, resultKey).Scan(&deleted)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(deleted, 1))
}

func TestCachePersistenceWorkerEqFactTombstoneScopedByOwner(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	c := cacheIface.(*cache)
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	keyA := cacheTestID("persist-worker-eq-owner-a")
	resA, err := c.GetOrInitCall(ctx, CacheKey{
		ID:            keyA,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(keyA, 1).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, resA.Release(ctx))

	keyB := cacheTestID("persist-worker-eq-owner-b")
	resB, err := c.GetOrInitCall(ctx, CacheKey{
		ID:            keyB,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(keyB, 2).WithSafeToPersistCache(true), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, resB.Release(ctx))

	rootA := resA.cacheSharedResult()
	rootB := resB.cacheSharedResult()
	assert.Assert(t, rootA != nil)
	assert.Assert(t, rootB != nil)

	var ownerA, ownerB cachePersistResultKey
	c.egraphMu.Lock()
	digA := c.resultIDDigestLocked(rootA)
	digB := c.resultIDDigestLocked(rootB)
	classA := c.ensureEqClassForDigestLocked(digA)
	classB := c.ensureEqClassForDigestLocked(digB)
	c.mergeEqClassesLocked(classA, classB)
	ownerA, err = rootA.persistResultKey()
	assert.NilError(t, err)
	ownerB, err = rootB.persistResultKey()
	assert.NilError(t, err)
	c.egraphMu.Unlock()

	assert.NilError(t, c.flushPersistenceWorker(ctx))

	var ownerALiveBefore, ownerBLiveBefore int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM eq_facts WHERE owner_result_key = ? AND deleted = 0`, string(ownerA)).Scan(&ownerALiveBefore)
	assert.NilError(t, err)
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM eq_facts WHERE owner_result_key = ? AND deleted = 0`, string(ownerB)).Scan(&ownerBLiveBefore)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(ownerALiveBefore > 0, true))
	assert.Check(t, cmp.Equal(ownerBLiveBefore > 0, true))

	c.egraphMu.Lock()
	c.emitPersistTombstonesForRootLocked(rootA)
	c.egraphMu.Unlock()
	assert.NilError(t, c.flushPersistenceWorker(ctx))

	var ownerALiveAfter, ownerBLiveAfter int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM eq_facts WHERE owner_result_key = ? AND deleted = 0`, string(ownerA)).Scan(&ownerALiveAfter)
	assert.NilError(t, err)
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM eq_facts WHERE owner_result_key = ? AND deleted = 0`, string(ownerB)).Scan(&ownerBLiveAfter)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(ownerALiveAfter, 0))
	assert.Check(t, cmp.Equal(ownerBLiveAfter > 0, true))
}

func TestCachePersistenceCleanShutdownToggleOnClose(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath)
	assert.NilError(t, err)
	c := cacheIface.(*cache)

	val, found, err := c.pdb.SelectMetaValue(ctx, persistdb.MetaKeyCleanShutdown)
	assert.NilError(t, err)
	assert.Check(t, found)
	assert.Check(t, cmp.Equal(val, "0"))

	assert.NilError(t, c.Close(context.Background()))

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, closeCacheDBs(db, q))
	}()

	val, found, err = q.SelectMetaValue(ctx, persistdb.MetaKeyCleanShutdown)
	assert.NilError(t, err)
	assert.Check(t, found)
	assert.Check(t, cmp.Equal(val, "1"))
}
