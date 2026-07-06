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

func TestEnvelopeCarriesLazyPayload(t *testing.T) {
	t.Parallel()

	obj := func(objectJSON string) *PersistedResultEnvelope {
		return &PersistedResultEnvelope{
			Version:    2,
			Kind:       persistedResultKindObject,
			TypeName:   "Test",
			ObjectJSON: json.RawMessage(objectJSON),
		}
	}

	assert.Assert(t, obj(`{"form":"lazy","lazyJSON":{"kind":"test"}}`).carriesLazyPayload())
	assert.Assert(t, !obj(`{"form":"snapshot"}`).carriesLazyPayload())
	// Corrupt payloads simply do not yield a source.
	assert.Assert(t, !obj(`{not json`).carriesLazyPayload())
	assert.Assert(t, !(&PersistedResultEnvelope{Version: 2, Kind: persistedResultKindScalar, ScalarJSON: json.RawMessage(`1`)}).carriesLazyPayload())

	list := &PersistedResultEnvelope{
		Version: 2,
		Kind:    persistedResultKindList,
		Items: []PersistedResultEnvelope{
			*obj(`{"form":"snapshot"}`),
			*obj(`{"form":"lazy","lazyJSON":{"kind":"test"}}`),
		},
	}
	assert.Assert(t, list.carriesLazyPayload())
}

// matHomeObj is a persistable object whose encoded payload carries both a
// snapshot link and a lazy form, so a restored result derives both retained
// sources.
type matHomeObj struct {
	Name string
}

type persistedMatHomeObj struct {
	Name     string          `json:"name"`
	LazyJSON json.RawMessage `json:"lazyJSON,omitempty"`
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
	payload, err := json.Marshal(persistedMatHomeObj{
		Name:     obj.Name,
		LazyJSON: json.RawMessage(`{"kind":"mat-home-test"}`),
	})
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

func (*matHomeObj) DecodePersistedObject(ctx context.Context, dag *Server, _ uint64, _ *ResultCall, payload json.RawMessage) (Typed, error) {
	_ = ctx
	_ = dag
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
	// envelope. (Snapshot-link state at publication is maintained by the
	// owner-lease sync, which requires a snapshot manager; with none
	// configured the source list stays empty, as before this change.)
	sharedA := resA.cacheSharedResult()
	stateA := sharedA.loadPayloadState()
	assert.Assert(t, stateA.realized)
	assert.Assert(t, stateA.persistedEnvelope == nil)

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
