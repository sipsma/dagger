package dagql

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCachePersistenceWorkerMirrorsRetainedPersistableResult(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	key := cacheTestIntCall("persist-worker-retained")
	res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 42), nil
	})
	assert.NilError(t, err)

	shared := res.cacheSharedResult()
	assert.Assert(t, shared != nil)
	sharedID := shared.id

	cacheTestReleaseSession(t, cacheIface, ctx)
	assert.NilError(t, c.persistCurrentState(ctx))

	var rowCount int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM results WHERE id = ?`, sharedID).Scan(&rowCount)
	assert.NilError(t, err)
	assert.Equal(t, 1, rowCount)

	var storedCallFrameJSON string
	err = c.sqlDB.QueryRowContext(ctx, `SELECT call_frame_json FROM results WHERE id = ?`, sharedID).Scan(&storedCallFrameJSON)
	assert.NilError(t, err)
	assert.Check(t, cmp.Contains(storedCallFrameJSON, `"field":"persist-worker-retained"`))
}

func TestCachePersistenceWorkerMirrorsUnpruneablePersistedEdge(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	key := cacheTestIntCall("persist-worker-unpruneable")
	res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall: key,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 42), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, c.MakeResultUnpruneable(ctx, res))
	cacheTestReleaseSession(t, cacheIface, ctx)
	assert.NilError(t, c.persistCurrentState(ctx))

	rows, err := c.pdb.ListMirrorPersistedEdges(ctx)
	assert.NilError(t, err)
	assert.Equal(t, 1, len(rows))
	assert.Assert(t, rows[0].Unpruneable)
	assert.Equal(t, int64(0), rows[0].ExpiresAtUnix)
}

func TestCachePersistenceDoesNotWriteDuringRuntime(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	key := cacheTestIntCall("persist-runtime-no-write")
	_, err = c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 42), nil
	})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cacheIface, ctx)

	var rowCount int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&rowCount)
	assert.NilError(t, err)
	assert.Equal(t, 0, rowCount)
}

func TestCachePersistenceWorkerMirrorsPrunedStateAfterRelease(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	key := cacheTestIntCall("persist-worker-pruned")
	_, err = c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{ResultCall: key}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 99), nil
	})
	assert.NilError(t, err)

	cacheTestReleaseSession(t, cacheIface, ctx)
	assert.NilError(t, c.persistCurrentState(ctx))

	var rowCount int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&rowCount)
	assert.NilError(t, err)
	assert.Equal(t, 0, rowCount)
}

func TestCachePersistenceWorkerMirrorsAuthoritativeEgraphState(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	sourceKey := cacheTestIntCall("persist-worker-source")
	sourceRes, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    sourceKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(sourceKey, 11), nil
	})
	assert.NilError(t, err)

	rootKey := &ResultCall{
		Kind:     ResultCallKindField,
		Type:     NewResultCallType(Int(0).Type()),
		Field:    "persist-worker-root",
		Receiver: &ResultCallRef{ResultID: uint64(sourceRes.cacheSharedResult().id)},
	}
	_, err = c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    rootKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(NewInt(22)), nil
	})
	assert.NilError(t, err)

	cacheTestReleaseSession(t, cacheIface, ctx)
	assert.NilError(t, c.persistCurrentState(ctx))

	var resultsCount int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&resultsCount)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(resultsCount, 2))

	var termsCount int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM terms`).Scan(&termsCount)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(termsCount, 2))

	var resultOutputEqClassesCount int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM result_output_eq_classes`).Scan(&resultOutputEqClassesCount)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(resultOutputEqClassesCount, 2))

	var resultInputCount int
	err = c.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM term_inputs WHERE provenance_kind = ?`, string(egraphInputProvenanceKindResult)).Scan(&resultInputCount)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(resultInputCount, 1))
}

func TestCachePersistenceSnapshotRemainsValidAfterLiveResultRemoval(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	key := cacheTestIntCall("persist-snapshot-self-contained")
	_, err = c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall: key,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 42), nil
	})
	assert.NilError(t, err)

	snapshot, err := c.snapshotPersistState(ctx)
	assert.NilError(t, err)
	assert.Equal(t, 1, len(snapshot.results))
	assert.Assert(t, snapshot.results[0].row.ID != 0)
	snapshotResultID := snapshot.results[0].row.ID

	cacheTestReleaseSession(t, cacheIface, ctx)
	assert.Equal(t, 0, c.Size())

	assert.NilError(t, c.applyPersistStateSnapshot(ctx, snapshot))

	rows, err := c.pdb.ListMirrorResults(ctx)
	assert.NilError(t, err)
	assert.Equal(t, 1, len(rows))
	assert.Equal(t, snapshotResultID, rows[0].ID)

	assert.Check(t, cmp.Contains(rows[0].CallFrameJSON, `"field":"persist-snapshot-self-contained"`))
}

func TestCachePersistenceWorkerOmitsMarkedNonPersistedObject(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()
	srv := persistWorkerObjectServer(t)

	ambientCall := persistWorkerObjectCall("persist-worker-ambient", (&persistWorkerNonPersistedObj{}).Type())
	ambient, err := c.GetOrInitCall(ctx, "test-session", srv, &CallRequest{
		ResultCall: ambientCall,
	}, func(callCtx context.Context) (AnyResult, error) {
		return NewObjectResultForCurrentCall(callCtx, srv, &persistWorkerNonPersistedObj{Name: "ambient"})
	})
	assert.NilError(t, err)
	ambientID := ambient.cacheSharedResult().id

	normalCall := cacheTestIntCall("persist-worker-normal")
	normal, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    normalCall,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(normalCall, 42), nil
	})
	assert.NilError(t, err)
	normalID := normal.cacheSharedResult().id

	snapshot, err := c.snapshotPersistState(ctx)
	assert.NilError(t, err)
	assert.Equal(t, 1, len(snapshot.results))
	assert.Equal(t, int64(normalID), snapshot.results[0].row.ID)
	assert.Equal(t, 2, len(snapshot.terms))

	for _, row := range snapshot.resultOutputEqClasses {
		assert.Assert(t, row.ResultID != int64(ambientID))
	}

	destCacheIface, err := NewCache(ctx, filepath.Join(t.TempDir(), "dest.db"), nil, nil)
	assert.NilError(t, err)
	destCache := destCacheIface
	defer func() {
		assert.NilError(t, destCache.Close(context.Background()))
	}()
	assert.NilError(t, destCache.importCachemoneyMetadataRows(ctx, CachemoneyImportSource{
		ID: "non-persisted-source",
	}, cachemoneyRowsFromPersistSnapshot(snapshot)))
	assert.Assert(t, cachemoneyImportedResultByOrigin(destCache, "non-persisted-source", uint64(normalID)) != nil)
	assert.Assert(t, cachemoneyImportedResultByOrigin(destCache, "non-persisted-source", uint64(ambientID)) == nil)
}

func TestCachePersistenceWorkerRejectsMarkedNonPersistedDependency(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer c.Close(context.Background())
	srv := persistWorkerObjectServer(t)

	ambient := persistWorkerNonPersistedResult(t, ctx, c, srv, "persist-worker-dep-ambient")
	parentCall := cacheTestIntCall("persist-worker-dep-parent")
	parent, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    parentCall,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(parentCall, 7), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, c.AddExplicitDependency(ctx, parent, ambient, "test_non_persisted_dependency"))

	_, err = c.snapshotPersistState(ctx)
	assert.ErrorContains(t, err, "dependency references non-persisted result")
}

func TestCachePersistenceWorkerRejectsMarkedNonPersistedCallFrameRef(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer c.Close(context.Background())
	srv := persistWorkerObjectServer(t)

	ambient := persistWorkerNonPersistedResult(t, ctx, c, srv, "persist-worker-call-ref-ambient")
	ambientID := ambient.cacheSharedResult().id
	parentCall := &ResultCall{
		Kind:     ResultCallKindField,
		Type:     NewResultCallType(Int(0).Type()),
		Field:    "persist-worker-call-ref-parent",
		Receiver: &ResultCallRef{ResultID: uint64(ambientID)},
	}
	parent, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    parentCall,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(parentCall, 8), nil
	})
	assert.NilError(t, err)

	// Call-frame refs are normally mirrored into deps; remove that duplicate
	// channel here so this regression exercises the call-frame guard directly.
	c.egraphMu.Lock()
	delete(c.resultsByID[parent.cacheSharedResult().id].deps, ambientID)
	c.egraphMu.Unlock()

	_, err = c.snapshotPersistState(ctx)
	assert.ErrorContains(t, err, "call frame refs: receiver: references non-persisted result")
}

func TestCachePersistenceWorkerRejectsMarkedNonPersistedCallFrameArgRef(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer c.Close(context.Background())
	srv := persistWorkerObjectServer(t)

	ambient := persistWorkerNonPersistedResult(t, ctx, c, srv, "persist-worker-call-arg-ambient")
	ambientID := ambient.cacheSharedResult().id
	parentCall := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType(Int(0).Type()),
		Field: "persist-worker-call-arg-parent",
		Args: []*ResultCallArg{{
			Name: "input",
			Value: &ResultCallLiteral{
				Kind:      ResultCallLiteralKindResultRef,
				ResultRef: &ResultCallRef{ResultID: uint64(ambientID)},
			},
		}},
	}
	parent, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    parentCall,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(parentCall, 9), nil
	})
	assert.NilError(t, err)

	// Call-frame refs are normally mirrored into deps; remove that duplicate
	// channel here so this regression exercises the call-frame guard directly.
	c.egraphMu.Lock()
	delete(c.resultsByID[parent.cacheSharedResult().id].deps, ambientID)
	c.egraphMu.Unlock()

	_, err = c.snapshotPersistState(ctx)
	assert.ErrorContains(t, err, `call frame refs: arg "input": references non-persisted result`)
}

func TestCachePersistenceWorkerRejectsMarkedNonPersistedPayloadRef(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer c.Close(context.Background())
	srv := persistWorkerObjectServer(t)

	ambient := persistWorkerNonPersistedResult(t, ctx, c, srv, "persist-worker-payload-ambient")
	ambientID := ambient.cacheSharedResult().id
	payloadCall := persistWorkerObjectCall("persist-worker-payload-parent", (&persistWorkerPayloadRefObj{}).Type())
	_, err = c.GetOrInitCall(ctx, "test-session", srv, &CallRequest{
		ResultCall:    payloadCall,
		IsPersistable: true,
	}, func(callCtx context.Context) (AnyResult, error) {
		return NewObjectResultForCurrentCall(callCtx, srv, &persistWorkerPayloadRefObj{ResultID: uint64(ambientID)})
	})
	assert.NilError(t, err)

	_, err = c.snapshotPersistState(ctx)
	assert.ErrorContains(t, err, "payload refs: references non-persisted result")
}

func TestCachePersistenceWorkerRejectsUnmarkedUnpersistableObject(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface
	defer c.Close(context.Background())
	srv := persistWorkerObjectServer(t)

	unmarkedCall := persistWorkerObjectCall("persist-worker-unmarked", (&persistWorkerUnmarkedObj{}).Type())
	_, err = c.GetOrInitCall(ctx, "test-session", srv, &CallRequest{
		ResultCall:    unmarkedCall,
		IsPersistable: true,
	}, func(callCtx context.Context) (AnyResult, error) {
		return NewObjectResultForCurrentCall(callCtx, srv, &persistWorkerUnmarkedObj{Name: "unmarked"})
	})
	assert.NilError(t, err)

	_, err = c.snapshotPersistState(ctx)
	assert.ErrorContains(t, err, `type "PersistWorkerUnmarkedObj" does not implement persisted object encoding`)
}

func TestCachePersistenceCleanShutdownToggleOnClose(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	c := cacheIface

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

type persistWorkerNonPersistedObj struct {
	Name string
}

func (*persistWorkerNonPersistedObj) Type() *ast.Type {
	return &ast.Type{
		NamedType: "PersistWorkerNonPersistedObj",
		NonNull:   true,
	}
}

func (*persistWorkerNonPersistedObj) NonPersistedObject() {}

type persistWorkerPayloadRefObj struct {
	ResultID uint64
}

func (*persistWorkerPayloadRefObj) Type() *ast.Type {
	return &ast.Type{
		NamedType: "PersistWorkerPayloadRefObj",
		NonNull:   true,
	}
}

func (obj *persistWorkerPayloadRefObj) EncodePersistedObject(context.Context, PersistedObjectCache) (PersistedObjectEncoding, error) {
	payload, err := json.Marshal(struct {
		ResultID uint64 `json:"resultID"`
	}{
		ResultID: obj.ResultID,
	})
	if err != nil {
		return PersistedObjectEncoding{}, err
	}
	return PersistedObjectEncoding{JSON: payload}, nil
}

func (*persistWorkerPayloadRefObj) DecodePersistedObject(context.Context, *Server, uint64, *ResultCall, json.RawMessage) (Typed, error) {
	return &persistWorkerPayloadRefObj{}, nil
}

type persistWorkerUnmarkedObj struct {
	Name string
}

func (*persistWorkerUnmarkedObj) Type() *ast.Type {
	return &ast.Type{
		NamedType: "PersistWorkerUnmarkedObj",
		NonNull:   true,
	}
}

func persistWorkerObjectServer(t testing.TB) *Server {
	t.Helper()
	srv := newDagqlServerForTest(t, cacheTestQuery{})
	srv.InstallObject(NewClass(srv, ClassOpts[*persistWorkerNonPersistedObj]{}))
	srv.InstallObject(NewClass(srv, ClassOpts[*persistWorkerPayloadRefObj]{}))
	srv.InstallObject(NewClass(srv, ClassOpts[*persistWorkerUnmarkedObj]{}))
	return srv
}

func persistWorkerObjectCall(field string, typ *ast.Type) *ResultCall {
	return &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType(typ),
		Field: field,
	}
}

func persistWorkerNonPersistedResult(t testing.TB, ctx context.Context, c *Cache, srv *Server, field string) AnyResult {
	t.Helper()
	ambientCall := persistWorkerObjectCall(field, (&persistWorkerNonPersistedObj{}).Type())
	ambient, err := c.GetOrInitCall(ctx, "test-session", srv, &CallRequest{
		ResultCall: ambientCall,
	}, func(callCtx context.Context) (AnyResult, error) {
		return NewObjectResultForCurrentCall(callCtx, srv, &persistWorkerNonPersistedObj{Name: field})
	})
	assert.NilError(t, err)
	return ambient
}

func cachemoneyRowsFromPersistSnapshot(snapshot persistStateSnapshot) cachemoneyPersistedStateRows {
	rows := cachemoneyPersistedStateRows{
		eqClassRows:             snapshot.eqClasses,
		eqClassDigestRows:       snapshot.eqClassDigests,
		termRows:                snapshot.terms,
		termInputRows:           snapshot.termInputs,
		resultOutputEqClassRows: snapshot.resultOutputEqClasses,
		persistedEdgeRows:       snapshot.persistedEdges,
		snapshotChainLayerRows:  snapshot.snapshotChainLayers,
	}
	for _, result := range snapshot.results {
		rows.resultRows = append(rows.resultRows, result.row)
		rows.resultDepRows = append(rows.resultDepRows, result.resultDeps...)
		rows.resultSnapshotChainRows = append(rows.resultSnapshotChainRows, result.resultSnapshotChains...)
	}
	return rows
}
