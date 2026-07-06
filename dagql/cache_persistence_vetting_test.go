package dagql

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"
)

// matSnapOnlyObj is upload-shaped: its persisted form is a snapshot link and
// nothing else — no lazy fragment to be re-made from, like a client upload
// whose recipe lives on the client machine.
type matSnapOnlyObj struct {
	Name string
}

type persistedMatSnapOnlyObj struct {
	Name string `json:"name"`
}

func (*matSnapOnlyObj) Type() *ast.Type {
	return &ast.Type{
		NamedType: "MatSnapOnlyObj",
		NonNull:   true,
	}
}

func (obj *matSnapOnlyObj) EncodePersistedObject(ctx context.Context, cache PersistedObjectCache) (PersistedObjectEncoding, error) {
	_ = ctx
	_ = cache
	payload, err := json.Marshal(persistedMatSnapOnlyObj{Name: obj.Name})
	if err != nil {
		return PersistedObjectEncoding{}, err
	}
	return PersistedObjectEncoding{
		JSON: payload,
		SnapshotLinks: []PersistedSnapshotRefLink{
			{RefKey: "upload-snap", Role: "snapshot"},
		},
	}, nil
}

func (*matSnapOnlyObj) DecodePersistedObject(ctx context.Context, dag *Server, _ uint64, _ *ResultCall, payload json.RawMessage, _ PersistedLazyFragment) (Typed, error) {
	_ = ctx
	_ = dag
	var persisted persistedMatSnapOnlyObj
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, err
	}
	return &matSnapOnlyObj{Name: persisted.Name}, nil
}

// newVettingTestServer serves a both-forms object (snapshot link + lazy
// fragment), an upload-shaped object (snapshot link only) with a chained
// field on it (a dependent row), and installs both types for decode.
func newVettingTestServer() *Server {
	srv, err := NewServer(context.Background(), &persistCodecRoot{})
	if err != nil {
		panic(err)
	}
	srv.InstallObject(NewClass(srv, ClassOpts[*matHomeObj]{}))
	srv.InstallObject(NewClass(srv, ClassOpts[*matSnapOnlyObj]{}))
	Fields[*matSnapOnlyObj]{
		Func("name", func(ctx context.Context, self *matSnapOnlyObj, _ struct{}) (String, error) {
			return String(self.Name), nil
		}).IsPersistable(),
	}.Install(srv)
	Fields[*persistCodecRoot]{
		NodeFunc("bothObj", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*matHomeObj], error) {
			return NewObjectResultForCurrentCall(ctx, srv, &matHomeObj{Name: "both"})
		}).IsPersistable(),
		NodeFunc("uploadObj", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*matSnapOnlyObj], error) {
			return NewObjectResultForCurrentCall(ctx, srv, &matSnapOnlyObj{Name: "upload"})
		}).IsPersistable(),
	}.Install(srv)
	return srv
}

func vettingRootCtx(ctx context.Context, cache *Cache, srv *Server) context.Context {
	rootCtx := ContextWithCall(ctx, &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistCodecRoot{}).Type()),
		Field: "vetting-root",
	})
	rootCtx = ContextWithCache(rootCtx, cache)
	return srvToContext(rootCtx, srv)
}

// seedVettingStore publishes: a both-forms object, an upload-shaped object,
// a chained field on the upload object (its dependent), and an independent
// scalar row. Returns the upload object's shared result ID.
func seedVettingStore(t *testing.T, ctx context.Context, dbPath string) (uploadID, dependentID uint64) {
	t.Helper()
	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)

	_, err = srv.root.Select(rootCtx, srv, Selector{Field: "bothObj"})
	assert.NilError(t, err)
	uploadRes, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	uploadID = uint64(uploadRes.cacheSharedResult().id)
	var name String
	assert.NilError(t, srv.Select(rootCtx, srv.root, &name, Selector{Field: "uploadObj"}, Selector{Field: "name"}))
	assert.Equal(t, String("upload"), name)

	intKey := cacheTestIntCall("vetting-independent")
	_, err = cache.GetOrInitCall(rootCtx, "test-session", srv, &CallRequest{
		ResultCall:    intKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(intKey, 7), nil
	})
	assert.NilError(t, err)

	// Find the chained field's row: the one whose deps include the upload row.
	snap := cache.DebugEGraphSnapshot()
	for _, res := range snap.Results {
		for _, dep := range res.ExplicitDeps {
			if dep == uploadID {
				dependentID = res.SharedResultID
			}
		}
	}
	assert.Assert(t, dependentID != 0, "seed did not record a dependent of the upload row")

	cacheTestReleaseSession(t, cache, rootCtx)
	assert.NilError(t, cache.persistCurrentState(ctx))
	assert.NilError(t, cache.Close(context.Background()))
	return uploadID, dependentID
}

func vettingSummaryReasons(summary *CacheRestoreSummary) map[uint64]string {
	reasons := make(map[uint64]string, len(summary.DroppedResults))
	for _, dropped := range summary.DroppedResults {
		reasons[dropped.SharedResultID] = dropped.Reason
	}
	return reasons
}

// TestCachePersistencePrunedSnapshotKeepsStore is the prune-between-boots
// case: the row whose only retained form was the pruned snapshot drops with
// exactly its dependents, the both-forms row survives on its lazy fragment,
// everything else still hits, and no wipe happens.
func TestCachePersistencePrunedSnapshotKeepsStore(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	uploadID, dependentID := seedVettingStore(t, ctx, dbPath)

	// Both rows' snapshots are pruned: the upload-shaped row has nothing
	// else and drops; the both-forms row falls back to its lazy fragment.
	manager := &fakeSnapshotManager{
		missingSnapshots: map[string]struct{}{
			"upload-snap":   {},
			"mat-home-snap": {},
		},
	}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

	snap := cache.DebugEGraphSnapshot()
	assert.Assert(t, snap.RestoreSummary != nil)
	assert.Assert(t, !snap.RestoreSummary.Wiped)
	assert.Equal(t, 2, snap.RestoreSummary.Dropped)
	reasons := vettingSummaryReasons(snap.RestoreSummary)
	assert.Equal(t, string(restoreDropSnapshotMissing), reasons[uploadID])
	assert.Equal(t, string(restoreDropDependent), reasons[dependentID])

	// The both-forms row survived, demoted to its lazy fragment alone.
	var bothSources []string
	for _, res := range snap.Results {
		if res.TypeName == "MatHomeObj" {
			bothSources = res.Sources
		}
		assert.Assert(t, res.SharedResultID != uploadID, "pruned upload row must not be restored")
		assert.Assert(t, res.SharedResultID != dependentID, "the pruned row's dependent must not be restored")
	}
	assert.DeepEqual(t, []string{"lazy_value"}, bothSources)

	// Both rows' snapshots were pruned, so nothing survived with a
	// snapshot source: the stale-lease sweep's keep set must be empty and
	// no dropped row can pin content.
	assert.Assert(t, manager.deleteStaleCallSeen)
	keptLeases := make([]string, 0, len(manager.deleteStaleKeep))
	for leaseID := range manager.deleteStaleKeep {
		keptLeases = append(keptLeases, leaseID)
	}
	assert.Equal(t, 0, len(keptLeases), "the both-forms row lost no snapshot but has none on this store; no leases expected: %v", keptLeases)

	// Everything else still hits warm.
	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	bothRes, err := srv.root.Select(rootCtx, srv, Selector{Field: "bothObj"})
	assert.NilError(t, err)
	assert.Assert(t, bothRes.HitCache())
	intKey := cacheTestIntCall("vetting-independent")
	intRes, err := cache.GetOrInitCall(rootCtx, "test-session", srv, &CallRequest{
		ResultCall:    intKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(intKey, 8), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, intRes.HitCache())
	assert.Equal(t, 7, cacheTestUnwrapInt(t, intRes))
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestCachePersistenceCorruptRowDropsExactlyItsDependents is the
// corrupt-one-row case on a store with unrelated healthy rows: the damaged
// row and exactly its dependents drop, everything else hits, no wipe.
func TestCachePersistenceCorruptRowDropsExactlyItsDependents(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	uploadID, dependentID := seedVettingStore(t, ctx, dbPath)

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	_, err = db.Exec(`UPDATE results SET self_payload = x'7B6E6F742D6A736F6E' WHERE id = ?`, uploadID)
	assert.NilError(t, err)
	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"))
	assert.NilError(t, closeCacheDBs(db, q))

	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

	snap := cache.DebugEGraphSnapshot()
	assert.Assert(t, snap.RestoreSummary != nil)
	assert.Assert(t, !snap.RestoreSummary.Wiped)
	assert.Equal(t, 2, snap.RestoreSummary.Dropped)
	reasons := vettingSummaryReasons(snap.RestoreSummary)
	assert.Equal(t, string(restoreDropMalformed), reasons[uploadID])
	assert.Equal(t, string(restoreDropDependent), reasons[dependentID])
	assert.Equal(t, snap.RestoreSummary.Kept, len(snap.Results))

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	bothRes, err := srv.root.Select(rootCtx, srv, Selector{Field: "bothObj"})
	assert.NilError(t, err)
	assert.Assert(t, bothRes.HitCache())
	intKey := cacheTestIntCall("vetting-independent")
	intRes, err := cache.GetOrInitCall(rootCtx, "test-session", srv, &CallRequest{
		ResultCall:    intKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(intKey, 8), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, intRes.HitCache())
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestCachePersistenceStoreLevelCorruptionStillWipes pins the one import
// failure class that stays a wholesale wipe: damage to the identity tables,
// which no single result's blast radius can scope.
func TestCachePersistenceStoreLevelCorruptionStillWipes(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	seedVettingStore(t, ctx, dbPath)

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	_, err = db.Exec(`UPDATE eq_class_digests SET eq_class_id = 999999`)
	assert.NilError(t, err)
	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"))
	assert.NilError(t, closeCacheDBs(db, q))

	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetImportFailure, cache.PersistenceResetReason())
	snap := cache.DebugEGraphSnapshot()
	assert.Assert(t, snap.RestoreSummary != nil)
	assert.Assert(t, snap.RestoreSummary.Wiped)
}

// TestCachePersistenceZeroDeltaFlush is the self-check that importing and
// re-exporting a store adds no rows: boot a seeded store, flush it
// untouched, and both the persisted counts and the actual row count must
// show a zero delta.
func TestCachePersistenceZeroDeltaFlush(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	seedVettingStore(t, ctx, dbPath)

	countRows := func() int64 {
		db, q, err := prepareCacheDBs(ctx, dbPath)
		assert.NilError(t, err)
		var count int64
		assert.NilError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&count))
		assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"))
		assert.NilError(t, closeCacheDBs(db, q))
		return count
	}
	seededRows := countRows()
	assert.Assert(t, seededRows > 0)

	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())
	assert.NilError(t, cache.persistCurrentState(ctx))

	readCount := func(key string) int64 {
		val, found, err := cache.pdb.SelectMetaValue(ctx, key)
		assert.NilError(t, err)
		assert.Assert(t, found, "missing %s meta", key)
		parsed, err := strconv.ParseInt(val, 10, 64)
		assert.NilError(t, err)
		return parsed
	}
	total := readCount(persistdb.MetaKeyResultsTotal)
	imported := readCount(persistdb.MetaKeyResultsImported)
	executed := readCount(persistdb.MetaKeyResultsExecutedThisBoot)
	assert.Equal(t, seededRows, total)
	assert.Equal(t, seededRows, imported)
	assert.Equal(t, int64(0), executed)

	snap := cache.DebugEGraphSnapshot()
	assert.Equal(t, seededRows, snap.ResultCounts.Imported)
	assert.Equal(t, int64(0), snap.ResultCounts.ExecutedThisBoot)
	assert.NilError(t, cache.Close(context.Background()))

	assert.Equal(t, seededRows, countRows())
}

// TestVetRestoredResultsCycleDropsWholeComponent pins the corruption rule
// for dependency cycles: honest stores are acyclic, so every row on a cycle
// — and everything depending on it — drops together, while unrelated rows
// survive.
func TestVetRestoredResultsCycleDropsWholeComponent(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	uploadID, dependentID := seedVettingStore(t, ctx, dbPath)

	// Manufacture a cycle: the upload row also depends on its dependent.
	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	_, err = db.Exec(`INSERT INTO result_deps (parent_result_id, dep_result_id) VALUES (?, ?)`, uploadID, dependentID)
	assert.NilError(t, err)
	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"))
	assert.NilError(t, closeCacheDBs(db, q))

	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

	snap := cache.DebugEGraphSnapshot()
	assert.Assert(t, snap.RestoreSummary != nil)
	reasons := vettingSummaryReasons(snap.RestoreSummary)
	droppedIDs := make([]uint64, 0, len(reasons))
	for id := range reasons {
		droppedIDs = append(droppedIDs, id)
	}
	sort.Slice(droppedIDs, func(i, j int) bool { return droppedIDs[i] < droppedIDs[j] })
	assert.DeepEqual(t, []uint64{uploadID, dependentID}, droppedIDs)
	assert.Equal(t, string(restoreDropDependencyCycle), reasons[uploadID])
	assert.Equal(t, string(restoreDropDependencyCycle), reasons[dependentID])
	assert.Equal(t, snap.RestoreSummary.Kept, len(snap.Results))
}
