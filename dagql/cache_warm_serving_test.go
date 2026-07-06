package dagql

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"
)

// matConcObj counts its decode runs so concurrency tests can prove the
// materialization singleflight: one runner, no matter how many demanders.
type matConcObj struct {
	Name string
}

var matConcDecodeRuns atomic.Int32

func (*matConcObj) Type() *ast.Type {
	return &ast.Type{
		NamedType: "MatConcObj",
		NonNull:   true,
	}
}

func (obj *matConcObj) EncodePersistedObject(ctx context.Context, cache PersistedObjectCache) (PersistedObjectEncoding, error) {
	_ = ctx
	_ = cache
	payload, err := json.Marshal(persistedMatHomeObj{Name: obj.Name})
	if err != nil {
		return PersistedObjectEncoding{}, err
	}
	return PersistedObjectEncoding{JSON: payload}, nil
}

func (*matConcObj) DecodePersistedObject(ctx context.Context, dag *Server, resultID uint64, _ *ResultCall, payload json.RawMessage, _ PersistedLazyFragment) (Typed, error) {
	_ = ctx
	_ = dag
	_ = resultID
	matConcDecodeRuns.Add(1)
	var persisted persistedMatHomeObj
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, err
	}
	return &matConcObj{Name: persisted.Name}, nil
}

func newMatConcTestServer() *Server {
	srv, err := NewServer(context.Background(), &persistCodecRoot{})
	if err != nil {
		panic(err)
	}
	srv.InstallObject(NewClass(srv, ClassOpts[*matConcObj]{}))
	Fields[*persistCodecRoot]{
		NodeFunc("concObj", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*matConcObj], error) {
			return NewObjectResultForCurrentCall(ctx, srv, &matConcObj{Name: "conc"})
		}).IsPersistable(),
	}.Install(srv)
	return srv
}

// TestWarmServingConcurrentDemandSingleflights is the concurrency proof: N
// sessions force the same unrealized restored result at once; exactly one
// decode runs, everyone gets the same value, nothing tears (run with -race).
func TestWarmServingConcurrentDemandSingleflights(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srvA := newMatConcTestServer()
	rootCtxA := vettingRootCtx(ctx, cacheA, srvA)
	_, err = srvA.root.Select(rootCtxA, srvA, Selector{Field: "concObj"})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cacheA, rootCtxA)
	assert.NilError(t, cacheA.persistCurrentState(ctx))
	assert.NilError(t, cacheA.Close(context.Background()))

	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()

	matConcDecodeRuns.Store(0)
	const demanders = 16
	var wg sync.WaitGroup
	values := make([]string, demanders)
	errs := make([]error, demanders)
	for i := 0; i < demanders; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			srv := newMatConcTestServer()
			rootCtx := vettingRootCtx(ctx, cache, srv)
			res, err := srv.root.Select(rootCtx, srv, Selector{Field: "concObj"})
			if err != nil {
				errs[worker] = err
				return
			}
			obj, ok := UnwrapAs[*matConcObj](res.Unwrap())
			if !ok {
				errs[worker] = context.Canceled // marker; asserted below via value
				return
			}
			values[worker] = obj.Name
		}(i)
	}
	wg.Wait()

	for worker := 0; worker < demanders; worker++ {
		assert.NilError(t, errs[worker], "worker %d", worker)
		assert.Equal(t, "conc", values[worker], "worker %d", worker)
	}
	assert.Equal(t, int32(1), matConcDecodeRuns.Load(), "the materialization walk must run exactly once")
	cacheTestReleaseSession(t, cache, ctx)
}

// TestWarmServingSnapshotLossFallsThroughToFragment pins the runtime
// fall-through: a restored both-forms result whose snapshot vanished after
// boot serves from its lazy fragment — still a hit, no demote — and the
// dead snapshot source is retired.
func TestWarmServingSnapshotLossFallsThroughToFragment(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	seedVettingStore(t, ctx, dbPath)

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{}}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

	// The snapshot vanishes AFTER boot vetting attached its lease: external
	// loss, the case boot vetting cannot promise against.
	manager.missingSnapshots["mat-home-snap"] = struct{}{}

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "bothObj"})
	assert.NilError(t, err)
	assert.Assert(t, res.HitCache(), "fall-through serving is still a hit")

	counters := cache.serveStats.byOutcome()
	assert.Equal(t, int64(1), counters[cacheServeFromLazyForm]["bothObj"])
	assert.Equal(t, int64(0), counters[cacheServeDemotedToMiss]["bothObj"])
	assert.Equal(t, int64(1), counters[cacheServeHitRestored]["bothObj"])

	// The dead snapshot source was retired: links cleared, lease removed.
	shared := res.cacheSharedResult()
	assert.Equal(t, 0, len(shared.loadSnapshotOwnerLinks()))
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestWarmServingExhaustionDemotesAndHeals is the demote floor end to end:
// a restored snapshot-only result loses its snapshot after boot; the hit
// cannot deliver, the same invocation executes live, and the fresh
// publication heals the store so the follow-up call hits again.
func TestWarmServingExhaustionDemotesAndHeals(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	uploadID, dependentID := seedVettingStore(t, ctx, dbPath)

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{}}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

	// Out-of-band loss after boot.
	manager.missingSnapshots["upload-snap"] = struct{}{}

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)

	// The call still succeeds: the demoted invocation executed live.
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	assert.Assert(t, !res.HitCache(), "the demoted invocation executes live, not as a hit")
	obj, ok := UnwrapAs[*matSnapOnlyObj](res.Unwrap())
	assert.Assert(t, ok)
	assert.Equal(t, "upload", obj.Name)

	counters := cache.serveStats.byOutcome()
	assert.Equal(t, int64(1), counters[cacheServeDemotedToMiss]["uploadObj"])
	assert.Equal(t, int64(1), counters[cacheServeMissFirst]["uploadObj"])

	// The exhausted row and its dependent are gone from servability.
	snap := cache.DebugEGraphSnapshot()
	for _, dres := range snap.Results {
		assert.Assert(t, dres.SharedResultID != dependentID, "the exhausted row's dependent must drop with it")
	}
	_ = uploadID

	// The heal: the fresh publication serves the follow-up as a live hit.
	res2, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	assert.Assert(t, res2.HitCache(), "the healed store must serve the follow-up call as a hit")
	counters = cache.serveStats.byOutcome()
	assert.Equal(t, int64(1), counters[cacheServeHitLive]["uploadObj"])
	assert.Equal(t, int64(1), counters[cacheServeDemotedToMiss]["uploadObj"], "demote must not repeat once healed")
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestWarmServingSparseHydration pins demand-bounded materialization at the
// store level: a warm boot restores many results, and using one realizes
// only that one — the rest stay undecoded envelopes.
func TestWarmServingSparseHydration(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	seedVettingStore(t, ctx, dbPath)

	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "bothObj"})
	assert.NilError(t, err)
	assert.Assert(t, res.HitCache())

	// Scalar rows decode eagerly at import (cheap, serverless); the
	// demand-bounded claim is about the content-bearing object rows: only
	// the demanded one may realize.
	realizedObjects := 0
	realizedID := uint64(0)
	snap := cache.DebugEGraphSnapshot()
	for _, dres := range snap.Results {
		if dres.TypeName != "MatHomeObj" && dres.TypeName != "MatSnapOnlyObj" {
			continue
		}
		if dres.Realized {
			realizedObjects++
			realizedID = dres.SharedResultID
		}
	}
	assert.Equal(t, 1, realizedObjects, "only the demanded object result may realize")
	assert.Equal(t, uint64(res.cacheSharedResult().id), realizedID)
	cacheTestReleaseSession(t, cache, rootCtx)
}
