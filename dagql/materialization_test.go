package dagql

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"
)

func TestMaterializationStateFallThroughOrder(t *testing.T) {
	t.Parallel()

	links := []PersistedSnapshotRefLink{{RefKey: "snap-1", Role: "snapshot"}}

	// Lazy form recorded first, snapshot links arriving later: the snapshot
	// source must still come first in fall-through order.
	var m materializationState
	m.ensureSource(sourceLazyValue)
	m.setLocalSnapshotSource(links)
	assert.DeepEqual(t, []retainedSourceKind{sourceLocalSnapshot, sourceLazyValue}, m.sourceKinds())

	// And the reverse write order produces the identical list.
	var m2 materializationState
	m2.setLocalSnapshotSource(links)
	m2.ensureSource(sourceLazyValue)
	assert.DeepEqual(t, m.sourceKinds(), m2.sourceKinds())

	// Re-adding an existing source must not duplicate it.
	m.ensureSource(sourceLazyValue)
	m.setLocalSnapshotSource(links)
	assert.DeepEqual(t, []retainedSourceKind{sourceLocalSnapshot, sourceLazyValue}, m.sourceKinds())

	// Appending links accumulates on the one snapshot source.
	m.appendLocalSnapshotLink(PersistedSnapshotRefLink{RefKey: "snap-2", Role: "meta"})
	assert.DeepEqual(t, []PersistedSnapshotRefLink{
		{RefKey: "snap-1", Role: "snapshot"},
		{RefKey: "snap-2", Role: "meta"},
	}, m.localSnapshotLinks())

	// Clearing the links removes the snapshot source; the lazy source keeps
	// its position.
	m.setLocalSnapshotSource(nil)
	assert.DeepEqual(t, []retainedSourceKind{sourceLazyValue}, m.sourceKinds())
	assert.Assert(t, m.localSnapshotLinks() == nil)
}

func TestMaterializationStateZeroSourcesRefusal(t *testing.T) {
	t.Parallel()

	// Model-level check of servable(): a state with no realized value, no
	// envelope, and no sources reports it has nothing to deliver. The
	// consumers that enforce this at restore vetting and at serving arrive
	// with the persistence and warm-lookup work; nothing consults it yet.
	var m materializationState
	assert.Assert(t, !m.servable())

	// Each ingredient alone makes the state servable again.
	m.realized = true
	assert.Assert(t, m.servable())
	m.realized = false

	m.envelope = &PersistedResultEnvelope{Version: 2, Kind: persistedResultKindObject}
	assert.Assert(t, m.servable())
	m.envelope = nil

	m.setLocalSnapshotSource([]PersistedSnapshotRefLink{{RefKey: "snap-1", Role: "snapshot"}})
	assert.Assert(t, m.servable())

	// Removing the last source drops the state back to refusal.
	m.setLocalSnapshotSource(nil)
	assert.Assert(t, !m.servable())
}

func TestMaterializationStateCloneSharesNoSlices(t *testing.T) {
	t.Parallel()

	var m materializationState
	m.setLocalSnapshotSource([]PersistedSnapshotRefLink{{RefKey: "snap-1", Role: "snapshot"}})
	m.ensureSource(sourceLazyValue)

	cp := m.clone()
	m.appendLocalSnapshotLink(PersistedSnapshotRefLink{RefKey: "snap-2", Role: "meta"})
	m.sources[0].snapshotLinks[0].RefKey = "mutated"

	assert.DeepEqual(t, []PersistedSnapshotRefLink{{RefKey: "snap-1", Role: "snapshot"}}, cp.localSnapshotLinks())
	assert.DeepEqual(t, []retainedSourceKind{sourceLocalSnapshot, sourceLazyValue}, cp.sourceKinds())
}

// matHomeObj is a persistable object that persists both a snapshot link and
// a lazy fragment, so a restored result derives both retained sources.
type matHomeObj struct {
	Name string
}

type persistedMatHomeObj struct {
	Name string `json:"name"`
}

func (*matHomeObj) Type() *ast.Type {
	return &ast.Type{
		NamedType: "MatHomeObj",
		NonNull:   true,
	}
}

func (obj *matHomeObj) EncodePersistedObject(ctx context.Context, cache PersistedObjectCache) (PersistedObjectEncoding, error) {
	_ = ctx
	_ = cache
	payload, err := json.Marshal(persistedMatHomeObj{Name: obj.Name})
	if err != nil {
		return PersistedObjectEncoding{}, err
	}
	return PersistedObjectEncoding{
		JSON: payload,
		SnapshotLinks: []PersistedSnapshotRefLink{
			{RefKey: "mat-home-snap", Role: "snapshot"},
		},
	}, nil
}

func (obj *matHomeObj) EncodePersistedLazyFragment(ctx context.Context, cache PersistedObjectCache) (*PersistedLazyFragment, error) {
	_ = ctx
	_ = cache
	return &PersistedLazyFragment{
		Kind: "mat-home-test",
		JSON: json.RawMessage(`{"kind":"mat-home-test"}`),
	}, nil
}

func (*matHomeObj) DecodePersistedObject(ctx context.Context, dag *Server, resultID uint64, _ *ResultCall, payload json.RawMessage, lazy PersistedLazyFragment) (Typed, error) {
	_ = dag
	if err := openTestSnapshotSource(ctx, resultID, lazy); err != nil {
		return nil, err
	}
	var persisted persistedMatHomeObj
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, err
	}
	return &matHomeObj{Name: persisted.Name}, nil
}

func newMatHomeTestServer() *Server {
	srv, err := NewServer(context.Background(), &persistCodecRoot{})
	if err != nil {
		panic(err)
	}
	srv.InstallObject(NewClass(srv, ClassOpts[*matHomeObj]{}))
	Fields[*persistCodecRoot]{
		NodeFunc("matHomeObj", func(ctx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*matHomeObj], error) {
			return NewObjectResultForCurrentCall(ctx, srv, &matHomeObj{Name: "x"})
		}).IsPersistable(),
	}.Install(srv)
	return srv
}

func matHomeRootCtx(ctx context.Context, cache *Cache, srv *Server) context.Context {
	rootCtx := ContextWithCall(ctx, &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistCodecRoot{}).Type()),
		Field: "mat-home-root",
	})
	rootCtx = ContextWithCache(rootCtx, cache)
	return srvToContext(rootCtx, srv)
}

func matHomeDebugResult(t *testing.T, c *Cache) EGraphDebugResult {
	t.Helper()
	snap := c.DebugEGraphSnapshot()
	for _, res := range snap.Results {
		if res.TypeName == "MatHomeObj" {
			return res
		}
	}
	t.Fatalf("MatHomeObj result not found in debug snapshot")
	return EGraphDebugResult{}
}

// TestMaterializationStateWritePoints drives the state through its legal
// write points — publication, import, the result's own decode — across a
// real persist/boot round trip and checks that each behaves as specified,
// and that flush and plain reads leave the state untouched. It proves the
// legal writers' behavior; it cannot prove that no illegal writer exists.
func TestMaterializationStateWritePoints(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srvA := newMatHomeTestServer()
	rootCtxA := matHomeRootCtx(ctx, cacheA, srvA)

	resA, err := srvA.root.Select(rootCtxA, srvA, Selector{Field: "matHomeObj"})
	assert.NilError(t, err)

	// Publication: the freshly published result is realized with no
	// envelope, and its lazy fragment was captured right then — the value's
	// recipe would be destroyed by realization, so publication is the only
	// moment the fragment is guaranteed to exist. (Snapshot-link state at
	// publication is maintained by the owner-lease sync, which requires a
	// snapshot manager; with none configured the link list stays empty.)
	sharedA := resA.cacheSharedResult()
	stateA := sharedA.loadPayloadState()
	assert.Assert(t, stateA.realized)
	assert.Assert(t, stateA.persistedEnvelope == nil)
	assert.DeepEqual(t, []retainedSourceKind{sourceLazyValue}, stateA.sourceKinds)
	fragA := sharedA.loadLazyFragment()
	assert.Assert(t, fragA != nil)
	assert.Equal(t, "mat-home-test", fragA.Kind)

	cacheTestReleaseSession(t, cacheA, rootCtxA)
	assert.NilError(t, cacheA.persistCurrentState(ctx))
	assert.NilError(t, cacheA.Close(context.Background()))

	// Import: the restored result carries the envelope and both derived
	// sources, unrealized, before any use.
	cacheB, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheB.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetNone, cacheB.PersistenceResetReason())

	restored := matHomeDebugResult(t, cacheB)
	assert.Assert(t, !restored.Realized)
	assert.DeepEqual(t, []string{"local_snapshot", "lazy_value"}, restored.Sources)
	assert.Equal(t, "imported_lazy_envelope", restored.PayloadState)
	assert.DeepEqual(t, []PersistedSnapshotRefLink{{RefKey: "mat-home-snap", Role: "snapshot"}}, restored.SnapshotLinks)

	// Flush only reads: persisting the untouched store changes nothing.
	assert.NilError(t, cacheB.persistCurrentState(ctx))
	afterFlush := matHomeDebugResult(t, cacheB)
	assert.DeepEqual(t, restored, afterFlush)

	// Decode — the result's own materialization outcome: a warm hit loads
	// the value, marking it realized and dropping the spent envelope; the
	// sources derived at import stay put.
	srvB := newMatHomeTestServer()
	rootCtxB := matHomeRootCtx(ctx, cacheB, srvB)
	resB, err := srvB.root.Select(rootCtxB, srvB, Selector{Field: "matHomeObj"})
	assert.NilError(t, err)
	assert.Assert(t, resB.HitCache())

	decoded := matHomeDebugResult(t, cacheB)
	assert.Assert(t, decoded.Realized)
	assert.Equal(t, "materialized", decoded.PayloadState)
	assert.DeepEqual(t, []string{"local_snapshot", "lazy_value"}, decoded.Sources)
	cacheTestReleaseSession(t, cacheB, rootCtxB)
}

// TestMaterializationBothFormsSurviveDecodeAndReflush pins the §-independent
// heart of capture-at-publication: the captured fragment lives on the
// sharedResult, so decoding a restored result (which clears the envelope)
// must not lose it — a store can be booted, used, and re-flushed any number
// of times and every generation keeps both retained forms.
func TestMaterializationBothFormsSurviveDecodeAndReflush(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dbPath := filepath.Join(t.TempDir(), "cache.db")

	// Generation 0: publish and flush.
	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	srvA := newMatHomeTestServer()
	rootCtxA := matHomeRootCtx(ctx, cacheA, srvA)
	_, err = srvA.root.Select(rootCtxA, srvA, Selector{Field: "matHomeObj"})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cacheA, rootCtxA)
	assert.NilError(t, cacheA.persistCurrentState(ctx))
	assert.NilError(t, cacheA.Close(context.Background()))

	// Generations 1..2: boot, take a warm hit (decoding the envelope), and
	// flush again. Both retained sources must survive every generation.
	for generation := 1; generation <= 2; generation++ {
		cache, err := NewCache(ctx, dbPath, nil, nil)
		assert.NilError(t, err)
		assert.Equal(t, CachePersistenceResetNone, cache.PersistenceResetReason())

		restored := matHomeDebugResult(t, cache)
		assert.Assert(t, !restored.Realized, "generation %d", generation)
		assert.DeepEqual(t, []string{"local_snapshot", "lazy_value"}, restored.Sources)

		srv := newMatHomeTestServer()
		rootCtx := matHomeRootCtx(ctx, cache, srv)
		res, err := srv.root.Select(rootCtx, srv, Selector{Field: "matHomeObj"})
		assert.NilError(t, err)
		assert.Assert(t, res.HitCache(), "generation %d", generation)

		// Decode cleared the envelope; the captured fragment must remain.
		decoded := matHomeDebugResult(t, cache)
		assert.Assert(t, decoded.Realized, "generation %d", generation)
		assert.DeepEqual(t, []string{"local_snapshot", "lazy_value"}, decoded.Sources)
		frag := res.cacheSharedResult().loadLazyFragment()
		assert.Assert(t, frag != nil, "generation %d lost the captured fragment after decode", generation)
		assert.Equal(t, "mat-home-test", frag.Kind)

		cacheTestReleaseSession(t, cache, rootCtx)
		assert.NilError(t, cache.persistCurrentState(ctx))
		assert.NilError(t, cache.Close(context.Background()))
	}
}
