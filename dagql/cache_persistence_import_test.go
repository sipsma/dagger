package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine"
	"github.com/opencontainers/go-digest"
	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"
)

type persistConcurrentDecodeObj struct {
	Name string
}

type persistedPersistConcurrentDecodeObj struct {
	Name string `json:"name"`
}

type persistConcurrentDecodeHook struct {
	active        atomic.Int32
	firstEntered  chan struct{}
	allowFirst    chan struct{}
	secondEntered chan struct{}
}

var persistConcurrentDecodeHooks sync.Map

func (*persistConcurrentDecodeObj) Type() *ast.Type {
	return &ast.Type{
		NamedType: "PersistConcurrentDecodeObj",
		NonNull:   true,
	}
}

func (obj *persistConcurrentDecodeObj) EncodePersistedObject(ctx context.Context, cache PersistedObjectCache) (PersistedObjectEncoding, error) {
	_ = ctx
	_ = cache
	payload, err := json.Marshal(persistedPersistConcurrentDecodeObj{Name: obj.Name})
	if err != nil {
		return PersistedObjectEncoding{}, err
	}
	return PersistedObjectEncoding{JSON: payload}, nil
}

func (*persistConcurrentDecodeObj) DecodePersistedObject(ctx context.Context, dag *Server, resultID uint64, _ *ResultCall, payload json.RawMessage, _ PersistedLazyFragment) (Typed, error) {
	_ = dag
	var persisted persistedPersistConcurrentDecodeObj
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, err
	}

	if hookAny, ok := persistConcurrentDecodeHooks.Load(resultID); ok {
		hook := hookAny.(*persistConcurrentDecodeHook)
		switch hook.active.Add(1) {
		case 1:
			close(hook.firstEntered)
			select {
			case <-hook.allowFirst:
			case <-ctx.Done():
				hook.active.Add(-1)
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
				hook.active.Add(-1)
				return nil, fmt.Errorf("first decode was never released for result %d", resultID)
			}
		case 2:
			close(hook.secondEntered)
			hook.active.Add(-1)
			return nil, fmt.Errorf("concurrent decode for result %d", resultID)
		default:
			hook.active.Add(-1)
			return nil, fmt.Errorf("unexpected concurrent decode count for result %d", resultID)
		}
		hook.active.Add(-1)
	}

	return &persistConcurrentDecodeObj{Name: persisted.Name}, nil
}

func newPersistCodecImportTestServer() *Server {
	srv, err := NewServer(context.Background(), &persistCodecRoot{})
	if err != nil {
		panic(err)
	}
	srv.InstallObject(NewClass(srv, ClassOpts[*persistCodecObj]{}))
	Fields[*persistCodecObj]{
		Func("name", func(ctx context.Context, self *persistCodecObj, _ struct{}) (String, error) {
			return String(self.Name), nil
		}),
	}.Install(srv)
	Fields[*persistCodecRoot]{
		NodeFunc("obj", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*persistCodecObj], error) {
			return newPersistCodecImportTestResult(ctx, srv)
		}).IsPersistable(),
		NodeFunc("objCanonical", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*persistCodecObj], error) {
			return newPersistCodecImportTestResult(ctx, srv)
		}).IsPersistable(),
		NodeFunc("objInner", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*persistCodecObj], error) {
			return newPersistCodecImportTestResult(ctx, srv)
		}),
		NodeFunc("objAlias", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*persistCodecObj], error) {
			var obj ObjectResult[*persistCodecObj]
			err := srv.Select(ctx, srv.root, &obj, Selector{Field: "objInner"})
			return obj, err
		}),
	}.Install(srv)
	return srv
}

func newPersistConcurrentDecodeTestServer() *Server {
	srv, err := NewServer(context.Background(), &persistCodecRoot{})
	if err != nil {
		panic(err)
	}
	srv.InstallObject(NewClass(srv, ClassOpts[*persistConcurrentDecodeObj]{}))
	Fields[*persistCodecRoot]{
		NodeFunc("objConcurrentDecode", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*persistConcurrentDecodeObj], error) {
			obj, err := NewObjectResultForCurrentCall(ctx, srv, &persistConcurrentDecodeObj{Name: "x"})
			if err != nil {
				return ObjectResult[*persistConcurrentDecodeObj]{}, err
			}
			return obj, nil
		}).IsPersistable(),
	}.Install(srv)
	return srv
}

func newPersistCodecImportTestResult(ctx context.Context, srv *Server) (ObjectResult[*persistCodecObj], error) {
	obj, err := NewObjectResultForCurrentCall(ctx, srv, &persistCodecObj{Name: "x"})
	if err != nil {
		return ObjectResult[*persistCodecObj]{}, err
	}
	return obj.WithContentDigest(ctx, digest.FromString("persist-codec-shared-object"))
}

func TestCachePersistenceImportRoundTripAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cA := cacheA

	key := cacheTestIntCall("persist-import-roundtrip")
	resA, err := cA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 123), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, !resA.HitCache())
	cacheTestReleaseSession(t, cA, ctx)
	assert.NilError(t, cA.persistCurrentState(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	cacheB, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cB := cacheB
	assert.Equal(t, CachePersistenceResetNone, cB.PersistenceResetReason())
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()

	resB, err := cB.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return nil, errors.New("unexpected initializer call")
	})
	assert.NilError(t, err)
	assert.Assert(t, resB.HitCache())
	assert.Equal(t, 123, cacheTestUnwrapInt(t, resB))
	cacheTestReleaseSession(t, cB, ctx)
}

func TestCachePersistenceImportRoundTripObjectResult(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cA := cacheA
	srvA := newPersistCodecImportTestServer()

	rootCtxA := ContextWithCall(ctx, &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistCodecRoot{}).Type()),
		Field: "persist-import-object-root",
	})
	rootCtxA = ContextWithCache(rootCtxA, cacheA)
	rootCtxA = srvToContext(rootCtxA, srvA)

	resA, err := srvA.root.Select(rootCtxA, srvA, Selector{Field: "obj"})
	assert.NilError(t, err)
	assert.Assert(t, resA != nil)
	cacheTestReleaseSession(t, cacheA, rootCtxA)
	assert.NilError(t, cA.persistCurrentState(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	cacheB, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cB := cacheB
	assert.Equal(t, CachePersistenceResetNone, cB.PersistenceResetReason())
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()
	srvB := newPersistCodecImportTestServer()

	rootCtxB := ContextWithCall(ctx, &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistCodecRoot{}).Type()),
		Field: "persist-import-object-root",
	})
	rootCtxB = ContextWithCache(rootCtxB, cacheB)
	rootCtxB = srvToContext(rootCtxB, srvB)

	resB, err := srvB.root.Select(rootCtxB, srvB, Selector{Field: "obj"})
	assert.NilError(t, err)
	assert.Assert(t, resB != nil)
	assert.Assert(t, resB.HitCache())
	obj, ok := UnwrapAs[*persistCodecObj](resB.Unwrap())
	assert.Assert(t, ok)
	assert.Equal(t, "x", obj.Name)
	cacheTestReleaseSession(t, cacheB, rootCtxB)
}

func TestCachePersistenceImportedObjectHitWithoutServerErrors(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cA := cacheA
	srvA := newPersistCodecImportTestServer()

	rootCtxA := ContextWithCall(ctx, &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistCodecRoot{}).Type()),
		Field: "persist-import-object-root",
	})
	rootCtxA = ContextWithCache(rootCtxA, cacheA)
	rootCtxA = srvToContext(rootCtxA, srvA)

	resA, err := srvA.root.Select(rootCtxA, srvA, Selector{Field: "obj"})
	assert.NilError(t, err)
	assert.Assert(t, resA != nil)

	reqCall, err := resA.ResultCall()
	assert.NilError(t, err)

	cacheTestReleaseSession(t, cacheA, rootCtxA)
	assert.NilError(t, cA.persistCurrentState(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	cacheB, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cB := cacheB
	assert.Equal(t, CachePersistenceResetNone, cB.PersistenceResetReason())
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()

	initCalls := 0
	_, err = cB.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{ResultCall: reqCall}, func(context.Context) (AnyResult, error) {
		initCalls++
		return nil, errors.New("unexpected initializer call")
	})
	assert.Assert(t, err != nil)
	assert.Equal(t, 0, initCalls)
	assert.Assert(t, strings.Contains(err.Error(), "decode persisted hit payload"))
}

func TestCachePersistenceImportedObjectAliasSupportsChainedSelect(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srvA := newPersistCodecImportTestServer()

	rootCtxA := ContextWithCall(ctx, &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistCodecRoot{}).Type()),
		Field: "persist-import-object-alias-root",
	})
	rootCtxA = ContextWithCache(rootCtxA, cacheA)
	rootCtxA = srvToContext(rootCtxA, srvA)

	var seed ObjectResult[*persistCodecObj]
	err = srvA.Select(rootCtxA, srvA.root, &seed, Selector{Field: "objCanonical"})
	assert.NilError(t, err)

	cacheTestReleaseSession(t, cacheA, rootCtxA)
	assert.NilError(t, cacheA.persistCurrentState(ctx))
	assert.NilError(t, cacheA.Close(context.Background()))

	cacheB, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheB.Close(context.Background()))
	}()
	srvB := newPersistCodecImportTestServer()

	rootCtxB := ContextWithCall(ctx, &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistCodecRoot{}).Type()),
		Field: "persist-import-object-alias-root",
	})
	rootCtxB = ContextWithCache(rootCtxB, cacheB)
	rootCtxB = srvToContext(rootCtxB, srvB)

	var name String
	err = srvB.Select(rootCtxB, srvB.root, &name,
		Selector{Field: "objAlias"},
		Selector{Field: "name"},
	)
	assert.NilError(t, err)
	assert.Equal(t, String("x"), name)

	cacheTestReleaseSession(t, cacheB, rootCtxB)
}

func TestCachePersistenceImportedObjectLoadSerializesPersistedDecode(t *testing.T) {
	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cA := cacheA
	srvA := newPersistConcurrentDecodeTestServer()

	rootCtxA := ContextWithCall(ctx, &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistCodecRoot{}).Type()),
		Field: "persist-import-concurrent-decode-root",
	})
	rootCtxA = ContextWithCache(rootCtxA, cacheA)
	rootCtxA = srvToContext(rootCtxA, srvA)

	var seed ObjectResult[*persistConcurrentDecodeObj]
	err = srvA.Select(rootCtxA, srvA.root, &seed, Selector{Field: "objConcurrentDecode"})
	assert.NilError(t, err)
	assert.Assert(t, seed.cacheSharedResult() != nil)
	resultID := uint64(seed.cacheSharedResult().id)
	assert.Assert(t, resultID != 0)

	cacheTestReleaseSession(t, cacheA, rootCtxA)
	assert.NilError(t, cA.persistCurrentState(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	cacheB, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cB := cacheB
	assert.Equal(t, CachePersistenceResetNone, cB.PersistenceResetReason())
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()
	srvB := newPersistConcurrentDecodeTestServer()

	hook := &persistConcurrentDecodeHook{
		firstEntered:  make(chan struct{}),
		allowFirst:    make(chan struct{}),
		secondEntered: make(chan struct{}),
	}
	persistConcurrentDecodeHooks.Store(resultID, hook)
	defer persistConcurrentDecodeHooks.Delete(resultID)

	loadCtx := func(sessionID string) context.Context {
		loadCtx := engine.ContextWithClientMetadata(ctx, &engine.ClientMetadata{
			ClientID:  sessionID + "-client",
			SessionID: sessionID,
		})
		loadCtx = ContextWithCache(loadCtx, cB)
		return srvToContext(loadCtx, srvB)
	}

	type loadResult struct {
		ctx context.Context
		err error
	}
	firstResultCh := make(chan loadResult, 1)
	secondResultCh := make(chan loadResult, 1)

	const firstSessionID = "persist-concurrent-decode-session-a"
	const secondSessionID = "persist-concurrent-decode-session-b"

	firstCtx := loadCtx(firstSessionID)
	go func() {
		_, err := cB.LoadResultByResultID(firstCtx, firstSessionID, srvB, resultID)
		firstResultCh <- loadResult{ctx: firstCtx, err: err}
	}()

	select {
	case <-hook.firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first persisted decode entry")
	}

	secondCtx := loadCtx(secondSessionID)
	go func() {
		_, err := cB.LoadResultByResultID(secondCtx, secondSessionID, srvB, resultID)
		secondResultCh <- loadResult{ctx: secondCtx, err: err}
	}()

	select {
	case <-hook.secondEntered:
	case <-time.After(50 * time.Millisecond):
	}
	close(hook.allowFirst)

	firstResult := <-firstResultCh
	secondResult := <-secondResultCh

	assert.NilError(t, cB.ReleaseSession(firstResult.ctx, firstSessionID))
	assert.NilError(t, cB.ReleaseSession(secondResult.ctx, secondSessionID))

	assert.NilError(t, firstResult.err)
	assert.NilError(t, secondResult.err)
}

func TestCachePersistenceUncleanMarkerWipesStore(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cA := cacheA

	key := cacheTestIntCall("persist-import-unclean-wipe")
	_, err = cA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 7), nil
	})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cA, ctx)
	assert.NilError(t, cA.persistCurrentState(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "0"))
	assert.NilError(t, closeCacheDBs(db, q))

	cacheB, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cB := cacheB
	assert.Equal(t, CachePersistenceResetUncleanShutdown, cB.PersistenceResetReason())
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()

	resB, err := cB.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 8), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, !resB.HitCache())
	assert.Equal(t, 8, cacheTestUnwrapInt(t, resB))
	cacheTestReleaseSession(t, cB, ctx)
}

func TestCachePersistenceSchemaMismatchWipesStore(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cA := cacheA

	key := cacheTestIntCall("persist-import-schema-mismatch-wipe")
	_, err = cA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 70), nil
	})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cA, ctx)
	assert.NilError(t, cA.persistCurrentState(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeySchemaVersion, "old-schema"))
	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"))
	assert.NilError(t, closeCacheDBs(db, q))

	cacheB, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cB := cacheB
	assert.Equal(t, CachePersistenceResetSchemaMismatch, cB.PersistenceResetReason())
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()

	resB, err := cB.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 71), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, !resB.HitCache())
	assert.Equal(t, 71, cacheTestUnwrapInt(t, resB))
	cacheTestReleaseSession(t, cB, ctx)
}

func TestCacheCloseDiscardingPersistenceDoesNotMarkClean(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	assert.NilError(t, cacheA.CloseDiscardingPersistence())

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	cleanShutdownVal, found, err := q.SelectMetaValue(ctx, persistdb.MetaKeyCleanShutdown)
	assert.NilError(t, err)
	assert.Assert(t, found)
	assert.Equal(t, "0", cleanShutdownVal)
	assert.NilError(t, closeCacheDBs(db, q))
}

// TestCachePersistenceCorruptRowDropsWithoutWipe pins per-result vetting:
// row damage costs that row (and its dependents), never the store.
func TestCachePersistenceCorruptRowDropsWithoutWipe(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cA := cacheA

	key := cacheTestIntCall("persist-import-corrupt-drop")
	_, err = cA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 50), nil
	})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cA, ctx)
	assert.NilError(t, cA.persistCurrentState(ctx))
	assert.NilError(t, cA.Close(context.Background()))

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	_, err = db.Exec(`UPDATE results SET self_payload = x'7B6E6F742D6A736F6E'`)
	assert.NilError(t, err)
	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"))
	assert.NilError(t, closeCacheDBs(db, q))

	cacheB, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	cB := cacheB
	assert.Equal(t, CachePersistenceResetNone, cB.PersistenceResetReason())
	defer func() {
		assert.NilError(t, cB.Close(context.Background()))
	}()
	snap := cB.DebugEGraphSnapshot()
	assert.Assert(t, snap.RestoreSummary != nil)
	assert.Assert(t, !snap.RestoreSummary.Wiped)
	assert.Equal(t, 0, snap.RestoreSummary.Kept)
	assert.Equal(t, 1, snap.RestoreSummary.Dropped)
	assert.Equal(t, 1, len(snap.RestoreSummary.DroppedResults))
	assert.Equal(t, string(restoreDropMalformed), snap.RestoreSummary.DroppedResults[0].Reason)

	resB, err := cB.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 51), nil
	})
	assert.NilError(t, err)
	assert.Assert(t, !resB.HitCache())
	assert.Equal(t, 51, cacheTestUnwrapInt(t, resB))
	cacheTestReleaseSession(t, cB, ctx)
}

// newR16TestServer builds a server whose seeded store carries a term with a
// result-backed input class: a persistable object field plus a persistable
// field selected on that object. The returned counters record initializer
// executions so warm boots can prove they served hits without re-executing.
func newR16TestServer() (*Server, *atomic.Int32, *atomic.Int32) {
	srv, err := NewServer(context.Background(), &persistCodecRoot{})
	if err != nil {
		panic(err)
	}
	objCalls := &atomic.Int32{}
	nameCalls := &atomic.Int32{}
	srv.InstallObject(NewClass(srv, ClassOpts[*persistCodecObj]{}))
	Fields[*persistCodecObj]{
		Func("name", func(ctx context.Context, self *persistCodecObj, _ struct{}) (String, error) {
			nameCalls.Add(1)
			return String(self.Name), nil
		}).IsPersistable(),
	}.Install(srv)
	Fields[*persistCodecRoot]{
		NodeFunc("r16Obj", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*persistCodecObj], error) {
			objCalls.Add(1)
			obj, err := NewObjectResultForCurrentCall(ctx, srv, &persistCodecObj{Name: "r16"})
			if err != nil {
				return ObjectResult[*persistCodecObj]{}, err
			}
			return obj.WithContentDigest(ctx, digest.FromString("r16-obj-content"))
		}).IsPersistable(),
	}.Install(srv)
	return srv, objCalls, nameCalls
}

func r16RootCtx(ctx context.Context, cache *Cache, srv *Server) context.Context {
	rootCtx := ContextWithCall(ctx, &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistCodecRoot{}).Type()),
		Field: "r16-root",
	})
	rootCtx = ContextWithCache(rootCtx, cache)
	return srvToContext(rootCtx, srv)
}

func r16CopyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("copy %s: %v", src, err)
	}
	assert.NilError(t, os.WriteFile(dst, data, 0o600))
}

// r16RenumberStore rewrites every eq-class and term ID in the store through
// one consistent order-reversing bijection, leaving digests untouched.
// input_eq_class_id zero (digest-provenance inputs) has no class row and
// stays zero.
func r16RenumberStore(ctx context.Context, t *testing.T, dbPath string) {
	t.Helper()

	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)

	var maxEq, maxTerm int64
	assert.NilError(t, db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM eq_classes`).Scan(&maxEq))
	assert.NilError(t, db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM terms`).Scan(&maxTerm))
	assert.Assert(t, maxEq > 1, "seed store must contain multiple eq classes for renumbering to mean anything")
	assert.Assert(t, maxTerm > 1, "seed store must contain multiple terms for renumbering to mean anything")

	// The bijection old -> (2*max + 1) - old reverses the order AND lands in
	// [max+1 .. 2*max], disjoint from the original range, so the renumbered
	// store shares no ID with the original. Two phases so intermediate
	// values never collide with live IDs: shift everything far away, then
	// map onto the target range.
	const shift = int64(1) << 30
	type renumberStmt struct {
		sql  string
		args []any
	}
	stmts := []renumberStmt{
		{`UPDATE eq_classes SET id = id + ?1`, []any{shift}},
		{`UPDATE eq_class_digests SET eq_class_id = eq_class_id + ?1`, []any{shift}},
		{`UPDATE terms SET output_eq_class_id = output_eq_class_id + ?1`, []any{shift}},
		{`UPDATE term_inputs SET input_eq_class_id = input_eq_class_id + ?1 WHERE input_eq_class_id != 0`, []any{shift}},
		{`UPDATE result_output_eq_classes SET eq_class_id = eq_class_id + ?1`, []any{shift}},
		{`UPDATE eq_classes SET id = 2 * ?2 + 1 - (id - ?1)`, []any{shift, maxEq}},
		{`UPDATE eq_class_digests SET eq_class_id = 2 * ?2 + 1 - (eq_class_id - ?1)`, []any{shift, maxEq}},
		{`UPDATE terms SET output_eq_class_id = 2 * ?2 + 1 - (output_eq_class_id - ?1)`, []any{shift, maxEq}},
		{`UPDATE term_inputs SET input_eq_class_id = 2 * ?2 + 1 - (input_eq_class_id - ?1) WHERE input_eq_class_id != 0`, []any{shift, maxEq}},
		{`UPDATE result_output_eq_classes SET eq_class_id = 2 * ?2 + 1 - (eq_class_id - ?1)`, []any{shift, maxEq}},
		{`UPDATE terms SET id = id + ?1`, []any{shift}},
		{`UPDATE term_inputs SET term_id = term_id + ?1`, []any{shift}},
		{`UPDATE terms SET id = 2 * ?2 + 1 - (id - ?1)`, []any{shift, maxTerm}},
		{`UPDATE term_inputs SET term_id = 2 * ?2 + 1 - (term_id - ?1)`, []any{shift, maxTerm}},
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("renumber (%s): %v", stmt.sql, err)
		}
	}

	// The store still has structure the renumbering exercises: at least one
	// term input backed by a real class.
	var resultInputs int64
	assert.NilError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM term_inputs WHERE input_eq_class_id != 0`).Scan(&resultInputs))
	assert.Assert(t, resultInputs > 0, "seed store must contain result-backed term inputs")

	assert.NilError(t, q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"))
	assert.NilError(t, closeCacheDBs(db, q))
}

// r16CanonicalTerms projects the booted term index down to persisted,
// content-addressed identity only: each term rendered as its self digest,
// its input classes' digest sets in position order, and its output class's
// digest set. Two stores that differ only in integer numbering must project
// identically.
func r16CanonicalTerms(t *testing.T, c *Cache) ([]string, []uint64) {
	t.Helper()
	snap := c.DebugEGraphSnapshot()

	classDigests := make(map[uint64]string, len(snap.EqClasses))
	for _, class := range snap.EqClasses {
		digests := append([]string(nil), class.Digests...)
		sort.Strings(digests)
		classDigests[class.EqClassID] = strings.Join(digests, ",")
	}
	canonClass := func(id uint64) string {
		if id == 0 {
			return "<none>"
		}
		return "{" + classDigests[id] + "}"
	}

	canon := make([]string, 0, len(snap.Terms))
	termIDs := make([]uint64, 0, len(snap.Terms))
	for _, term := range snap.Terms {
		parts := term.SelfDigest
		for _, in := range term.InputEqIDs {
			parts += "|" + canonClass(in)
		}
		parts += "=>" + canonClass(term.OutputEqID)
		canon = append(canon, parts)
		termIDs = append(termIDs, term.TermID)
	}
	sort.Strings(canon)
	sort.Slice(termIDs, func(i, j int) bool { return termIDs[i] < termIDs[j] })
	return canon, termIDs
}

// TestCachePersistenceRenumberedStoreEquivalence is the R16 determinism
// property test: rewrite a store's eq-class and term IDs through a
// consistent order-reversing bijection (digests untouched), then boot the
// original and the renumbered copy. Both must import without a wipe, derive
// term identity that projects identically onto persisted digests, and serve
// identical warm hits without re-executing anything. Only file-local integer
// numbering differs between the two stores, so any divergence means identity
// leaned on an ordinal.
func TestCachePersistenceRenumberedStoreEquivalence(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srvA, objCallsA, nameCallsA := newR16TestServer()
	rootCtxA := r16RootCtx(ctx, cacheA, srvA)

	var nameA String
	assert.NilError(t, srvA.Select(rootCtxA, srvA.root, &nameA, Selector{Field: "r16Obj"}, Selector{Field: "name"}))
	assert.Equal(t, String("r16"), nameA)
	assert.Equal(t, int32(1), objCallsA.Load())
	assert.Equal(t, int32(1), nameCallsA.Load())
	cacheTestReleaseSession(t, cacheA, rootCtxA)
	assert.NilError(t, cacheA.persistCurrentState(ctx))
	assert.NilError(t, cacheA.Close(context.Background()))

	renumberedPath := filepath.Join(dir, "renumbered.db")
	r16CopyFile(t, dbPath, renumberedPath)
	r16CopyFile(t, dbPath+"-wal", renumberedPath+"-wal")
	r16CopyFile(t, dbPath+"-shm", renumberedPath+"-shm")
	r16RenumberStore(ctx, t, renumberedPath)

	type bootOutcome struct {
		canonicalTerms []string
		termIDs        []uint64
	}
	boot := func(path string) bootOutcome {
		t.Helper()
		cache, err := NewCache(ctx, path, nil, nil)
		assert.NilError(t, err)
		defer func() {
			assert.NilError(t, cache.Close(context.Background()))
		}()
		assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

		canonicalTerms, termIDs := r16CanonicalTerms(t, cache)

		srv, objCalls, nameCalls := newR16TestServer()
		rootCtx := r16RootCtx(ctx, cache, srv)
		var name String
		assert.NilError(t, srv.Select(rootCtx, srv.root, &name, Selector{Field: "r16Obj"}, Selector{Field: "name"}))
		assert.Equal(t, String("r16"), name)
		assert.Equal(t, int32(0), objCalls.Load(), "warm boot of %s re-executed the object field", path)
		assert.Equal(t, int32(0), nameCalls.Load(), "warm boot of %s re-executed the chained field", path)
		cacheTestReleaseSession(t, cache, rootCtx)

		return bootOutcome{canonicalTerms: canonicalTerms, termIDs: termIDs}
	}

	original := boot(dbPath)
	renumbered := boot(renumberedPath)

	// Sanity: the copies really are numbered differently.
	assert.Assert(t, len(original.termIDs) > 1)
	assert.Assert(t, !slices.Equal(original.termIDs, renumbered.termIDs),
		"renumbering did not change term IDs; the test is not exercising anything")

	// Identical key derivation, expressed in the only identity that
	// persists: digests. In-memory key strings are process-local by design
	// (they embed this boot's class numbering), so the comparison projects
	// every term onto the digest sets of its classes instead.
	assert.DeepEqual(t, original.canonicalTerms, renumbered.canonicalTerms)
}
