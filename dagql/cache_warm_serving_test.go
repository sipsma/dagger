package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// testSharedResultByID fetches the live shared result for a persisted row so
// surface tests can hand the cache a result handle directly.
func testSharedResultByID(c *Cache, id uint64) *sharedResult {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	return c.resultsByID[sharedResultID(id)]
}

func pollMaterializeWaiters(t *testing.T, res *sharedResult, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		res.materializeMu.Lock()
		got := res.materializeWaiters
		res.materializeMu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d materialize waiters (have %d)", want, got)
		}
		time.Sleep(time.Millisecond)
	}
}

func pollMaterializeIdle(t *testing.T, res *sharedResult) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		res.materializeMu.Lock()
		idle := res.materializeWaitCh == nil
		res.materializeMu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the materialize runner to go idle")
		}
		time.Sleep(time.Millisecond)
	}
}

// matGateObj's decode blocks on a per-name gate so cancellation tests can
// hold a decode in flight and observe the runner's context.
type matGateObj struct {
	Name string
}

type matGateHooks struct {
	started chan struct{}
	release chan struct{}
	runs    atomic.Int32
	cancels atomic.Int32
}

var matGateRegistry sync.Map // object name -> *matGateHooks

func (*matGateObj) Type() *ast.Type {
	return &ast.Type{
		NamedType: "MatGateObj",
		NonNull:   true,
	}
}

func (obj *matGateObj) EncodePersistedObject(ctx context.Context, cache PersistedObjectCache) (PersistedObjectEncoding, error) {
	_ = ctx
	_ = cache
	payload, err := json.Marshal(persistedMatHomeObj{Name: obj.Name})
	if err != nil {
		return PersistedObjectEncoding{}, err
	}
	return PersistedObjectEncoding{JSON: payload}, nil
}

func (*matGateObj) DecodePersistedObject(ctx context.Context, dag *Server, resultID uint64, _ *ResultCall, payload json.RawMessage, _ PersistedLazyFragment) (Typed, error) {
	_ = dag
	_ = resultID
	var persisted persistedMatHomeObj
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, err
	}
	if h, ok := matGateRegistry.Load(persisted.Name); ok {
		hooks := h.(*matGateHooks)
		hooks.runs.Add(1)
		select {
		case hooks.started <- struct{}{}:
		default:
		}
		select {
		case <-hooks.release:
		case <-ctx.Done():
			hooks.cancels.Add(1)
			return nil, context.Cause(ctx)
		}
	}
	return &matGateObj{Name: persisted.Name}, nil
}

func newMatGateTestServer(name string) *Server {
	srv, err := NewServer(context.Background(), &persistCodecRoot{})
	if err != nil {
		panic(err)
	}
	srv.InstallObject(NewClass(srv, ClassOpts[*matGateObj]{}))
	Fields[*persistCodecRoot]{
		NodeFunc("gateObj", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*matGateObj], error) {
			return NewObjectResultForCurrentCall(ctx, srv, &matGateObj{Name: name})
		}).IsPersistable(),
	}.Install(srv)
	return srv
}

func seedGateStore(t *testing.T, ctx context.Context, dbPath, name string) uint64 {
	t.Helper()
	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srv := newMatGateTestServer(name)
	rootCtx := vettingRootCtx(ctx, cache, srv)
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "gateObj"})
	assert.NilError(t, err)
	id := uint64(res.cacheSharedResult().id)
	cacheTestReleaseSession(t, cache, rootCtx)
	assert.NilError(t, cache.persistCurrentState(ctx))
	assert.NilError(t, cache.Close(context.Background()))
	return id
}

// TestWarmServingDecodeSurvivesFirstDemanderCancel pins the runner's
// independence from any one demander: the demander that claimed the decode
// cancels while others wait, the runner keeps going, and the remaining
// demanders receive the decoded value.
func TestWarmServingDecodeSurvivesFirstDemanderCancel(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	id := seedGateStore(t, ctx, dbPath, "gate-survive")

	hooks := &matGateHooks{
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	matGateRegistry.Store("gate-survive", hooks)
	defer matGateRegistry.Delete("gate-survive")

	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	shared := testSharedResultByID(cache, id)
	assert.Assert(t, shared != nil)

	demand := func(demandCtx context.Context, errCh chan<- error) {
		srv := newMatGateTestServer("gate-survive")
		rootCtx := vettingRootCtx(demandCtx, cache, srv)
		res, err := srv.root.Select(rootCtx, srv, Selector{Field: "gateObj"})
		if err == nil {
			obj, ok := UnwrapAs[*matGateObj](res.Unwrap())
			if !ok || obj.Name != "gate-survive" {
				err = fmt.Errorf("unexpected decoded value")
			}
		}
		errCh <- err
	}

	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	firstErrCh := make(chan error, 1)
	go demand(firstCtx, firstErrCh)
	<-hooks.started

	const others = 3
	otherErrCh := make(chan error, others)
	for i := 0; i < others; i++ {
		go demand(ctx, otherErrCh)
	}
	pollMaterializeWaiters(t, shared, 1+others)

	cancelFirst()
	assert.Assert(t, errors.Is(<-firstErrCh, context.Canceled), "the departing demander gets its own cancellation")

	close(hooks.release)
	for i := 0; i < others; i++ {
		assert.NilError(t, <-otherErrCh, "demander %d", i)
	}
	assert.Equal(t, int32(1), hooks.runs.Load(), "one demander leaving must not restart or kill the decode")
	assert.Equal(t, int32(0), hooks.cancels.Load(), "the runner must survive a non-final demander's cancellation")
	cacheTestReleaseSession(t, cache, ctx)
}

// TestWarmServingDecodeLastDemanderCancelStopsRunner pins the other half of
// the cancellation contract: when every demander gives up, the last one out
// cancels the runner, and a later demand retries the decode fresh.
func TestWarmServingDecodeLastDemanderCancelStopsRunner(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	id := seedGateStore(t, ctx, dbPath, "gate-stop")

	hooks := &matGateHooks{
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	matGateRegistry.Store("gate-stop", hooks)
	defer matGateRegistry.Delete("gate-stop")

	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	shared := testSharedResultByID(cache, id)
	assert.Assert(t, shared != nil)

	onlyCtx, cancelOnly := context.WithCancel(ctx)
	defer cancelOnly()
	errCh := make(chan error, 1)
	go func() {
		srv := newMatGateTestServer("gate-stop")
		rootCtx := vettingRootCtx(onlyCtx, cache, srv)
		_, err := srv.root.Select(rootCtx, srv, Selector{Field: "gateObj"})
		errCh <- err
	}()
	<-hooks.started

	cancelOnly()
	assert.Assert(t, errors.Is(<-errCh, context.Canceled))
	pollMaterializeIdle(t, shared)
	assert.Equal(t, int32(1), hooks.cancels.Load(), "the last demander out must cancel the runner")

	// A fresh demand retries the decode and succeeds through the open gate.
	close(hooks.release)
	srv := newMatGateTestServer("gate-stop")
	rootCtx := vettingRootCtx(ctx, cache, srv)
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "gateObj"})
	assert.NilError(t, err)
	obj, ok := UnwrapAs[*matGateObj](res.Unwrap())
	assert.Assert(t, ok)
	assert.Equal(t, "gate-stop", obj.Name)
	assert.Equal(t, int32(2), hooks.runs.Load(), "the retry must run a fresh decode")
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestWarmServingExhaustedAttachDropsAndNormalizes pins the attach surface's
// exhaustion rule: attaching an exhausted restored result drops it (healing
// future lookups) and returns an honest error — never the internal sentinel.
func TestWarmServingExhaustedAttachDropsAndNormalizes(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	uploadID, _ := seedVettingStore(t, ctx, dbPath)

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{}}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	manager.missingSnapshots["upload-snap"] = struct{}{}

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	corpse := testSharedResultByID(cache, uploadID)
	assert.Assert(t, corpse != nil)

	_, err = cache.AttachResult(rootCtx, "attach-session", srv, Result[Typed]{shared: corpse})
	assert.Assert(t, err != nil)
	assert.Assert(t, !errors.Is(err, errSourcesExhausted), "the exhaustion sentinel must not escape the cache")
	assert.ErrorContains(t, err, "dropped from the cache")

	// The drop healed the store: the same recipe now misses and executes
	// live — no demote, because no hit was ever served.
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	assert.Assert(t, !res.HitCache())
	counters := cache.serveStats.byOutcome()
	assert.Equal(t, int64(0), counters[cacheServeDemotedToMiss]["uploadObj"])
	cacheTestReleaseSession(t, cache, rootCtx)
}

// TestWarmServingExhaustedAdoptionDropsAndNormalizes pins the wait surface:
// a call's function returns an existing cache-backed result that turns out
// to be an exhausted restored row, publication adopts it, and the post-
// completion normalization drops it with an honest error instead of leaking
// the sentinel or leaving the corpse indexed.
func TestWarmServingExhaustedAdoptionDropsAndNormalizes(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	uploadID, _ := seedVettingStore(t, ctx, dbPath)

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{}}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	manager.missingSnapshots["upload-snap"] = struct{}{}

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	corpse := testSharedResultByID(cache, uploadID)
	assert.Assert(t, corpse != nil)

	corpseClass := NewClass(srv, ClassOpts[*matSnapOnlyObj]{})
	Fields[*persistCodecRoot]{
		NodeFunc("corpseHandle", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*matSnapOnlyObj], error) {
			return ObjectResult[*matSnapOnlyObj]{
				Result: Result[*matSnapOnlyObj]{shared: corpse},
				class:  corpseClass,
			}, nil
		}).IsPersistable(),
	}.Install(srv)

	_, err = srv.root.Select(rootCtx, srv, Selector{Field: "corpseHandle"})
	assert.Assert(t, err != nil)
	assert.Assert(t, !errors.Is(err, errSourcesExhausted), "the exhaustion sentinel must not escape the cache")
	assert.ErrorContains(t, err, "dropped from the cache")

	// The drop healed the store for the recipe that owns the content.
	res, err := srv.root.Select(rootCtx, srv, Selector{Field: "uploadObj"})
	assert.NilError(t, err)
	assert.Assert(t, !res.HitCache())
	cacheTestReleaseSession(t, cache, rootCtx)
}

// matRetireProbeObj records, per decode attempt, whether the walk stated
// that its snapshot source is retired — the fact content decoders gate
// fragment rebuilds on.
type matRetireProbeObj struct {
	Name string
}

type retireProbeRecorder struct {
	mu    sync.Mutex
	flags []bool
}

func (r *retireProbeRecorder) record(v bool) {
	r.mu.Lock()
	r.flags = append(r.flags, v)
	r.mu.Unlock()
}

func (r *retireProbeRecorder) snapshot() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.flags...)
}

var matRetireProbeRegistry sync.Map // object name -> *retireProbeRecorder

func (*matRetireProbeObj) Type() *ast.Type {
	return &ast.Type{
		NamedType: "MatRetireProbeObj",
		NonNull:   true,
	}
}

func (obj *matRetireProbeObj) EncodePersistedObject(ctx context.Context, cache PersistedObjectCache) (PersistedObjectEncoding, error) {
	_ = ctx
	_ = cache
	payload, err := json.Marshal(persistedMatHomeObj{Name: obj.Name})
	if err != nil {
		return PersistedObjectEncoding{}, err
	}
	return PersistedObjectEncoding{
		JSON: payload,
		SnapshotLinks: []PersistedSnapshotRefLink{
			{RefKey: "probe-snap-" + obj.Name, Role: "snapshot"},
		},
	}, nil
}

func (*matRetireProbeObj) EncodePersistedLazyFragment(ctx context.Context, cache PersistedObjectCache) (*PersistedLazyFragment, error) {
	_ = ctx
	_ = cache
	return &PersistedLazyFragment{
		Kind: "retire-probe-test",
		JSON: json.RawMessage(`{"kind":"retire-probe-test"}`),
	}, nil
}

func (*matRetireProbeObj) DecodePersistedObject(ctx context.Context, dag *Server, resultID uint64, _ *ResultCall, payload json.RawMessage, lazy PersistedLazyFragment) (Typed, error) {
	_ = dag
	var persisted persistedMatHomeObj
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, err
	}
	if r, ok := matRetireProbeRegistry.Load(persisted.Name); ok {
		r.(*retireProbeRecorder).record(SnapshotSourceRetired(ctx, resultID))
	}
	if _, err := openTestSnapshotSource(ctx, resultID, lazy); err != nil {
		return nil, err
	}
	return &matRetireProbeObj{Name: persisted.Name}, nil
}

func newRetireProbeServer(name string) *Server {
	srv, err := NewServer(context.Background(), &persistCodecRoot{})
	if err != nil {
		panic(err)
	}
	srv.InstallObject(NewClass(srv, ClassOpts[*matRetireProbeObj]{}))
	Fields[*persistCodecRoot]{
		NodeFunc("probeObj", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*matRetireProbeObj], error) {
			return NewObjectResultForCurrentCall(ctx, srv, &matRetireProbeObj{Name: name})
		}).IsPersistable(),
	}.Install(srv)
	return srv
}

func seedRetireProbeStore(t *testing.T, ctx context.Context, dbPath, name string) {
	t.Helper()
	cache, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srv := newRetireProbeServer(name)
	rootCtx := vettingRootCtx(ctx, cache, srv)
	_, err = srv.root.Select(rootCtx, srv, Selector{Field: "probeObj"})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cache, rootCtx)
	assert.NilError(t, cache.persistCurrentState(ctx))
	assert.NilError(t, cache.Close(context.Background()))
}

// TestWarmServingRetirementFactReachesDecoders pins the explicit threading
// of "the snapshot source was retired" from the walk into content decoders,
// for both retirement moments: a snapshot lost at runtime (first attempt
// decodes links unmarked, the retry after retirement is marked) and one
// pruned by boot vetting (the only attempt is marked from the start).
func TestWarmServingRetirementFactReachesDecoders(t *testing.T) {
	t.Parallel()

	t.Run("runtime retirement marks the retry", func(t *testing.T) {
		t.Parallel()
		ctx := cacheTestContext(t.Context())
		dbPath := filepath.Join(t.TempDir(), "cache.db")
		seedRetireProbeStore(t, ctx, dbPath, "rt")

		recorder := &retireProbeRecorder{}
		matRetireProbeRegistry.Store("rt", recorder)
		defer matRetireProbeRegistry.Delete("rt")

		manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{}}
		cache, err := NewCache(ctx, dbPath, manager, nil)
		assert.NilError(t, err)
		defer func() {
			assert.NilError(t, cache.Close(context.Background()))
		}()
		manager.missingSnapshots["probe-snap-rt"] = struct{}{}

		srv := newRetireProbeServer("rt")
		rootCtx := vettingRootCtx(ctx, cache, srv)
		res, err := srv.root.Select(rootCtx, srv, Selector{Field: "probeObj"})
		assert.NilError(t, err)
		assert.Assert(t, res.HitCache())
		assert.DeepEqual(t, []bool{false, true}, recorder.snapshot())
		cacheTestReleaseSession(t, cache, rootCtx)
	})

	t.Run("boot-vetted retirement marks the first attempt", func(t *testing.T) {
		t.Parallel()
		ctx := cacheTestContext(t.Context())
		dbPath := filepath.Join(t.TempDir(), "cache.db")
		seedRetireProbeStore(t, ctx, dbPath, "boot")

		recorder := &retireProbeRecorder{}
		matRetireProbeRegistry.Store("boot", recorder)
		defer matRetireProbeRegistry.Delete("boot")

		manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{
			"probe-snap-boot": {},
		}}
		cache, err := NewCache(ctx, dbPath, manager, nil)
		assert.NilError(t, err)
		defer func() {
			assert.NilError(t, cache.Close(context.Background()))
		}()

		srv := newRetireProbeServer("boot")
		rootCtx := vettingRootCtx(ctx, cache, srv)
		res, err := srv.root.Select(rootCtx, srv, Selector{Field: "probeObj"})
		assert.NilError(t, err)
		assert.Assert(t, res.HitCache())
		assert.DeepEqual(t, []bool{true}, recorder.snapshot())
		cacheTestReleaseSession(t, cache, rootCtx)
	})
}

// TestWarmServingDroppedRowRefusesLaterTouches pins the drop's completeness:
// result-ID handles still resolve a dropped row directly (deindexing only
// removes recipe candidacy), so the drop must leave nothing servable — a
// later touch flows through the exhaustion machinery and gets the honest
// dropped error, never a doomed decode of the corpse's leftovers.
func TestWarmServingDroppedRowRefusesLaterTouches(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")
	uploadID, _ := seedVettingStore(t, ctx, dbPath)

	manager := &fakeSnapshotManager{missingSnapshots: map[string]struct{}{}}
	cache, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cache.Close(context.Background()))
	}()
	manager.missingSnapshots["upload-snap"] = struct{}{}

	srv := newVettingTestServer()
	rootCtx := vettingRootCtx(ctx, cache, srv)
	corpse := testSharedResultByID(cache, uploadID)
	assert.Assert(t, corpse != nil)

	// First touch: exhaustion drops the row.
	_, err = cache.AttachResult(rootCtx, "drop-touch-1", srv, Result[Typed]{shared: corpse})
	assert.Assert(t, err != nil)
	assert.ErrorContains(t, err, "dropped from the cache")

	// Second touch, through the same direct handle: the corpse must refuse
	// with the same honest shape — not attempt a decode against the cleared
	// sources and surface an unclassified error.
	_, err = cache.AttachResult(rootCtx, "drop-touch-2", srv, Result[Typed]{shared: corpse})
	assert.Assert(t, err != nil)
	assert.Assert(t, !errors.Is(err, errSourcesExhausted), "the exhaustion sentinel must not escape the cache")
	assert.ErrorContains(t, err, "dropped from the cache")
	cacheTestReleaseSession(t, cache, rootCtx)
}
