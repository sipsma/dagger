package dagql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dagger/dagger/dagql/call"
	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"
)

// bundleTestCounts is the projection of live cache state the growth and
// isolation assertions compare: if any of these move when they must not,
// the import created or destroyed something.
type bundleTestCounts struct {
	Results        int
	Terms          int
	EqClasses      int
	Origins        int
	PersistedEdges int
}

func bundleTestSnapshotCounts(c *Cache) bundleTestCounts {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	return bundleTestCounts{
		Results:        len(c.resultsByID),
		Terms:          len(c.egraphTerms),
		EqClasses:      len(c.eqClassToDigests),
		Origins:        len(c.resultsByOrigin),
		PersistedEdges: len(c.persistedEdgesByResult),
	}
}

func bundleTestOrigins(c *Cache) map[resultOrigin]sharedResultID {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	out := make(map[resultOrigin]sharedResultID, len(c.resultsByOrigin))
	for origin, id := range c.resultsByOrigin {
		out[origin] = id
	}
	return out
}

// seedBundleTestJunk publishes persistable filler rows so the target
// cache's ID space diverges from any bundle's: every imported row must
// then land on a local ID different from its bundle join key, which makes
// a skipped rewrite observable instead of silently coincidental.
func seedBundleTestJunk(t *testing.T, ctx context.Context, c *Cache, n int) {
	t.Helper()
	for i := range n {
		key := cacheTestIntCall(fmt.Sprintf("bundle-junk-%d-%s", i, t.Name()))
		_, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
			ResultCall:    key,
			IsPersistable: true,
		}, func(context.Context) (AnyResult, error) {
			return cacheTestIntResult(key, i), nil
		})
		assert.NilError(t, err)
	}
	cacheTestReleaseSession(t, c, ctx)
}

// TestCacheBundleRoundTrip is T-S1's core: a seeded store exports a
// bundle; a fresh store with a deliberately offset ID space imports it;
// key derivation projects identically onto digests, warm lookups hit
// without re-executing, and every origin crosses verbatim.
func TestCacheBundleRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()

	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), nil, nil)
	assert.NilError(t, err)
	srvA, objCallsA, nameCallsA := newR16TestServer()
	rootCtxA := r16RootCtx(ctx, cacheA, srvA)
	var nameA String
	assert.NilError(t, srvA.Select(rootCtxA, srvA.root, &nameA, Selector{Field: "r16Obj"}, Selector{Field: "name"}))
	assert.Equal(t, String("r16"), nameA)
	assert.Equal(t, int32(1), objCallsA.Load())
	assert.Equal(t, int32(1), nameCallsA.Load())
	cacheTestReleaseSession(t, cacheA, rootCtxA)

	canonicalA, _ := r16CanonicalTerms(t, cacheA)
	originsA := bundleTestOrigins(cacheA)

	var bundle bytes.Buffer
	exportSummary, err := cacheA.ExportBundle(ctx, &bundle, CacheBundleExportOptions{Scope: "test/scope"})
	assert.NilError(t, err)
	assert.Assert(t, exportSummary.Results > 0)
	assert.Equal(t, 0, exportSummary.ExcludedNoPortableContent)
	assert.Equal(t, 0, exportSummary.ExcludedEncodeFailed)
	assert.NilError(t, cacheA.Close(context.Background()))

	cacheB, err := NewCache(ctx, filepath.Join(dir, "b.db"), nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheB.Close(context.Background()))
	}()
	seedBundleTestJunk(t, ctx, cacheB, 5)
	preImport := bundleTestSnapshotCounts(cacheB)

	importSummary, err := cacheB.ImportBundle(ctx, bytes.NewReader(bundle.Bytes()))
	assert.NilError(t, err)
	assert.Equal(t, exportSummary.Results, importSummary.ResultsInBundle)
	assert.Equal(t, exportSummary.Results, importSummary.RowsImported)
	assert.Equal(t, 0, importSummary.RowsDedupedByOrigin)
	assert.Equal(t, 0, importSummary.RowsDroppedVetting)
	assert.Equal(t, 0, importSummary.RowsDroppedRewrite)
	postImport := bundleTestSnapshotCounts(cacheB)
	assert.Equal(t, preImport.Results+importSummary.RowsImported, postImport.Results)

	// Origins cross verbatim: every origin pair A held is now present in
	// B, mapped onto a live local row whose ID differs from the bundle's
	// join key (the junk offset guarantees the spaces are disjoint).
	originsB := bundleTestOrigins(cacheB)
	for origin, localIDA := range originsA {
		localIDB, present := originsB[origin]
		assert.Assert(t, present, "origin %v missing after import", origin)
		assert.Assert(t, localIDB != localIDA, "imported row for origin %v kept the exporter's ID %d; the ID spaces were built to differ", origin, localIDA)
		cacheB.egraphMu.RLock()
		res := cacheB.resultsByID[localIDB]
		cacheB.egraphMu.RUnlock()
		assert.Assert(t, res != nil)
		assert.Equal(t, origin, res.origin)
		// The envelope's structural self-ID was rewritten to the local row.
		res.payloadMu.RLock()
		env := res.materialization.envelope
		res.payloadMu.RUnlock()
		if env != nil && env.ResultID != 0 {
			assert.Equal(t, uint64(localIDB), env.ResultID)
		}
	}

	// Frame references rewrote into the local ID space (§6.3 row a): every
	// imported frame's refs resolve to local rows whose origins are A's —
	// a skipped frame walk leaves them pointing into B's junk rows.
	localIDsForAOrigins := make(map[sharedResultID]struct{}, len(originsA))
	for origin := range originsA {
		localIDsForAOrigins[originsB[origin]] = struct{}{}
	}
	for origin := range originsA {
		cacheB.egraphMu.RLock()
		res := cacheB.resultsByID[originsB[origin]]
		cacheB.egraphMu.RUnlock()
		frame := res.loadResultCall()
		if frame == nil {
			continue
		}
		assert.NilError(t, cacheB.WalkResultCall(frame, func(ref *ResultCallRef, _ *ResultCall) error {
			if ref.ResultID == 0 {
				return nil
			}
			if _, imported := localIDsForAOrigins[sharedResultID(ref.ResultID)]; !imported {
				return fmt.Errorf("imported frame ref %d does not point at an imported row's local ID", ref.ResultID)
			}
			return nil
		}))
	}

	// Identical key derivation, as digest projections: A's canonical terms
	// are a subset of B's (B also holds its junk rows' terms).
	canonicalB, _ := r16CanonicalTerms(t, cacheB)
	bSet := make(map[string]struct{}, len(canonicalB))
	for _, term := range canonicalB {
		bSet[term] = struct{}{}
	}
	for _, term := range canonicalA {
		_, ok := bSet[term]
		assert.Assert(t, ok, "imported store lost term %q", term)
	}

	// Identical hit behavior: the same chain served warm, nothing
	// re-executed. This exercises the rewritten frame refs — the chained
	// field's receiver reference must resolve to the locally-assigned row.
	srvB, objCallsB, nameCallsB := newR16TestServer()
	rootCtxB := r16RootCtx(ctx, cacheB, srvB)
	var nameB String
	assert.NilError(t, srvB.Select(rootCtxB, srvB.root, &nameB, Selector{Field: "r16Obj"}, Selector{Field: "name"}))
	assert.Equal(t, String("r16"), nameB)
	assert.Equal(t, int32(0), objCallsB.Load(), "warm import re-executed the object field")
	assert.Equal(t, int32(0), nameCallsB.Load(), "warm import re-executed the chained field")
	cacheTestReleaseSession(t, cacheB, rootCtxB)
}

// bundleTokenObj is a persisted object whose payload embeds both payload
// ref tokens: a $dagqlResultRef and a handle-form $dagqlCallID naming the
// same dependency. Its rows make the two token-rewrite mechanisms directly
// observable at import.
type bundleTokenObj struct {
	Name string
	Dep  AnyResult
}

type bundleTokenObjPayload struct {
	Name    string             `json:"name"`
	DepRef  PersistedResultRef `json:"depRef,omitempty"`
	DepCall PersistedCallID    `json:"depCall,omitempty"`
}

func (*bundleTokenObj) Type() *ast.Type {
	return &ast.Type{NamedType: "BundleTokenObj", NonNull: true}
}

func (obj *bundleTokenObj) AttachDependencyResults(ctx context.Context, self AnyResult, attach func(AnyResult) (AnyResult, error)) ([]AnyResult, error) {
	if obj.Dep == nil {
		return nil, nil
	}
	attached, err := attach(obj.Dep)
	if err != nil {
		return nil, err
	}
	obj.Dep = attached
	return []AnyResult{attached}, nil
}

func (obj *bundleTokenObj) EncodePersistedObject(ctx context.Context, cache PersistedObjectCache) (PersistedObjectEncoding, error) {
	payload := bundleTokenObjPayload{Name: obj.Name}
	if obj.Dep != nil {
		depID, err := cache.PersistedResultID(obj.Dep)
		if err != nil {
			return PersistedObjectEncoding{}, err
		}
		payload.DepRef = NewPersistedResultRef(depID)
		handleID := call.NewEngineResultID(depID, call.NewType(obj.Dep.Type()))
		encoded, err := handleID.Encode()
		if err != nil {
			return PersistedObjectEncoding{}, err
		}
		payload.DepCall = NewPersistedCallID(encoded)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return PersistedObjectEncoding{}, err
	}
	return PersistedObjectEncoding{JSON: raw}, nil
}

func (*bundleTokenObj) DecodePersistedObject(ctx context.Context, dag *Server, _ uint64, _ *ResultCall, payload json.RawMessage, _ PersistedLazyFragment) (Typed, error) {
	var persisted bundleTokenObjPayload
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, err
	}
	return &bundleTokenObj{Name: persisted.Name}, nil
}

// TestCacheBundlePayloadTokenRewrite is the portable-ref red test for the
// payload-token locations (§6.3 rows c and d): after import into an
// offset ID space, the payload's $dagqlResultRef and handle-form
// $dagqlCallID both name the locally-assigned dependency row — a skipped
// rewrite leaves them on the exporter's IDs and fails here.
func TestCacheBundlePayloadTokenRewrite(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()

	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), nil, nil)
	assert.NilError(t, err)

	srvA, err := NewServer(context.Background(), &persistCodecRoot{})
	assert.NilError(t, err)
	srvA.InstallObject(NewClass(srvA, ClassOpts[*bundleTokenObj]{}))
	depKey := cacheTestIntCall("bundle-token-dep")
	Fields[*persistCodecRoot]{
		NodeFunc("tokenObj", func(fieldCtx context.Context, _ ObjectResult[*persistCodecRoot], _ struct{}) (ObjectResult[*bundleTokenObj], error) {
			cache, err := EngineCache(fieldCtx)
			if err != nil {
				return ObjectResult[*bundleTokenObj]{}, err
			}
			dep, err := cache.GetOrInitCall(fieldCtx, "test-session", noopTypeResolver{}, &CallRequest{
				ResultCall:    depKey,
				IsPersistable: true,
			}, func(context.Context) (AnyResult, error) {
				return cacheTestIntResult(depKey, 7), nil
			})
			if err != nil {
				return ObjectResult[*bundleTokenObj]{}, err
			}
			return NewObjectResultForCurrentCall(fieldCtx, srvA, &bundleTokenObj{Name: "tok", Dep: dep})
		}).IsPersistable(),
	}.Install(srvA)

	rootCtxA := r16RootCtx(ctx, cacheA, srvA)
	var tokenObj ObjectResult[*bundleTokenObj]
	assert.NilError(t, srvA.Select(rootCtxA, srvA.root, &tokenObj, Selector{Field: "tokenObj"}))
	cacheTestReleaseSession(t, cacheA, rootCtxA)

	originsA := bundleTestOrigins(cacheA)
	tokenRowID := tokenObj.cacheSharedResult().id
	depRowID := tokenObj.Self().Dep.cacheSharedResult().id
	tokenOrigin := resultOrigin{storeUUID: cacheA.storeUUID, resultID: uint64(tokenRowID)}
	depOrigin := resultOrigin{storeUUID: cacheA.storeUUID, resultID: uint64(depRowID)}
	_, ok := originsA[tokenOrigin]
	assert.Assert(t, ok)

	var bundle bytes.Buffer
	exportSummary, err := cacheA.ExportBundle(ctx, &bundle, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.Equal(t, 0, exportSummary.ExcludedNoPortableContent+exportSummary.ExcludedEncodeFailed)
	assert.NilError(t, cacheA.Close(context.Background()))

	cacheB, err := NewCache(ctx, filepath.Join(dir, "b.db"), nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheB.Close(context.Background()))
	}()
	seedBundleTestJunk(t, ctx, cacheB, 4)

	_, err = cacheB.ImportBundle(ctx, bytes.NewReader(bundle.Bytes()))
	assert.NilError(t, err)

	originsB := bundleTestOrigins(cacheB)
	localTokenID := originsB[tokenOrigin]
	localDepID := originsB[depOrigin]
	assert.Assert(t, localTokenID != 0)
	assert.Assert(t, localDepID != 0)
	assert.Assert(t, uint64(localDepID) != uint64(depRowID), "ID spaces must differ for the rewrite to be observable")

	cacheB.egraphMu.RLock()
	res := cacheB.resultsByID[localTokenID]
	cacheB.egraphMu.RUnlock()
	assert.Assert(t, res != nil)
	res.payloadMu.RLock()
	env := res.materialization.envelope
	res.payloadMu.RUnlock()
	assert.Assert(t, env != nil)

	var payload bundleTokenObjPayload
	assert.NilError(t, json.Unmarshal(env.ObjectJSON, &payload))
	assert.Equal(t, uint64(localDepID), payload.DepRef.ResultID(),
		"$dagqlResultRef token still speaks the exporter's ID space")
	var depCallID call.ID
	assert.NilError(t, depCallID.Decode(payload.DepCall.Encoded()))
	assert.Assert(t, depCallID.IsHandle())
	assert.Equal(t, uint64(localDepID), depCallID.EngineResultID(),
		"$dagqlCallID handle-form token still speaks the exporter's ID space")
}

// TestCacheBundleEnvelopeSelfIDRewrite covers the envelope's structural
// self-IDs (§6.3 row b): the top-level ResultID and every list item's.
func TestCacheBundleEnvelopeSelfIDRewrite(t *testing.T) {
	t.Parallel()

	env := &PersistedResultEnvelope{
		Version:  2,
		Kind:     persistedResultKindList,
		ResultID: 3,
		Items: []PersistedResultEnvelope{
			{Version: 2, Kind: persistedResultKindScalar, ResultID: 4, ScalarJSON: json.RawMessage(`1`)},
			{Version: 2, Kind: persistedResultKindScalar, ResultID: 5, ScalarJSON: json.RawMessage(`2`)},
		},
	}
	remap := map[uint64]uint64{3: 103, 4: 104, 5: 105}
	remapRef := func(id uint64) (uint64, error) {
		mapped, ok := remap[id]
		if !ok {
			return 0, fmt.Errorf("no remap for %d", id)
		}
		return mapped, nil
	}
	identityCallID := func(encoded string) (string, error) { return encoded, nil }

	rewritten, err := rewriteBundleEnvelope(env, 103, remapRef, identityCallID)
	assert.NilError(t, err)
	assert.Equal(t, uint64(103), rewritten.ResultID)
	assert.Equal(t, uint64(104), rewritten.Items[0].ResultID)
	assert.Equal(t, uint64(105), rewritten.Items[1].ResultID)
	// The original is untouched (staging must not mutate vetted rows).
	assert.Equal(t, uint64(3), env.ResultID)

	// An item whose self-ID misses the remap drops the row.
	env.Items[1].ResultID = 9999
	_, err = rewriteBundleEnvelope(env, 103, remapRef, identityCallID)
	assert.Assert(t, err != nil)
}

// TestCacheBundleCallIDForms is the §6.3 row-d audit pinned as a test:
// handle-form call IDs rewrite their embedded engine result ID; recipe-form
// IDs carry no engine result IDs (the recipe wire format has no such leaf)
// and cross byte-identical.
func TestCacheBundleCallIDForms(t *testing.T) {
	t.Parallel()

	cache, err := NewCache(t.Context(), "", nil, nil)
	assert.NilError(t, err)
	rewrite := cache.rewriteBundleCallID(func(id uint64) (uint64, error) {
		if id != 42 {
			return 0, fmt.Errorf("unexpected id %d", id)
		}
		return 142, nil
	})

	intType := call.NewType(String("").Type())
	handle := call.NewEngineResultID(42, intType)
	handleEncoded, err := handle.Encode()
	assert.NilError(t, err)
	rewritten, err := rewrite(handleEncoded)
	assert.NilError(t, err)
	var decoded call.ID
	assert.NilError(t, decoded.Decode(rewritten))
	assert.Assert(t, decoded.IsHandle())
	assert.Equal(t, uint64(142), decoded.EngineResultID())

	recipe := call.New().Append(String("").Type(), "someField")
	recipeEncoded, err := recipe.Encode()
	assert.NilError(t, err)
	unchanged, err := rewrite(recipeEncoded)
	assert.NilError(t, err)
	assert.Equal(t, recipeEncoded, unchanged, "recipe-form IDs must cross unchanged")

	garbage, err := rewrite("not-base64-!!!")
	assert.Assert(t, err != nil)
	assert.Equal(t, "", garbage)
}

// TestCacheBundleOriginCollisionAfterPruneRestart is the pinned origin
// red test: seed → export → prune the ID tail → clean restart → new work
// → export. Without the persisted allocator high-water mark the restart
// re-allocates the pruned ID and one origin pair names two different
// results across the two bundles.
func TestCacheBundleOriginCollisionAfterPruneRestart(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "a.db")

	cacheA, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	for i := range 3 {
		key := cacheTestIntCall(fmt.Sprintf("origin-collision-%d", i))
		_, err := cacheA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
			ResultCall:    key,
			IsPersistable: true,
		}, func(context.Context) (AnyResult, error) {
			return cacheTestIntResult(key, i), nil
		})
		assert.NilError(t, err)
	}
	cacheTestReleaseSession(t, cacheA, ctx)

	var bundle1 bytes.Buffer
	_, err = cacheA.ExportBundle(ctx, &bundle1, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.NilError(t, cacheA.persistCurrentState(ctx))
	assert.NilError(t, cacheA.Close(context.Background()))

	// Prune the ID tail directly in the store: the highest row goes away,
	// exactly what disk-pressure pruning does between boots.
	db, q, err := prepareCacheDBs(ctx, dbPath)
	assert.NilError(t, err)
	var maxID int64
	assert.NilError(t, db.QueryRowContext(ctx, `SELECT MAX(id) FROM results`).Scan(&maxID))
	for _, stmt := range []string{
		`DELETE FROM result_origins WHERE result_id = ?1`,
		`DELETE FROM persisted_edges WHERE result_id = ?1`,
		`DELETE FROM result_deps WHERE parent_result_id = ?1 OR dep_result_id = ?1`,
		`DELETE FROM result_output_eq_classes WHERE result_id = ?1`,
		`DELETE FROM results WHERE id = ?1`,
	} {
		_, err := db.ExecContext(ctx, stmt, maxID)
		assert.NilError(t, err)
	}
	assert.NilError(t, closeCacheDBs(db, q))

	// Clean restart: allocation must resume above the high-water mark,
	// not above the surviving maximum.
	cacheA2, err := NewCache(ctx, dbPath, nil, nil)
	assert.NilError(t, err)
	assert.Equal(t, CachePersistenceResetNone, cacheA2.PersistenceResetReason())
	newKey := cacheTestIntCall("origin-collision-new-work")
	newRes, err := cacheA2.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    newKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(newKey, 99), nil
	})
	assert.NilError(t, err)
	newID := newRes.cacheSharedResult().id
	assert.Assert(t, int64(newID) > maxID,
		"new work re-used pruned result ID %d (allocated %d); the high-water mark did not hold", maxID, newID)
	cacheTestReleaseSession(t, cacheA2, ctx)

	var bundle2 bytes.Buffer
	_, err = cacheA2.ExportBundle(ctx, &bundle2, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.NilError(t, cacheA2.Close(context.Background()))

	// No origin pair may ever name two different results: any pair present
	// in both bundles must carry the same call frame.
	framesByOrigin := func(data []byte) map[resultOrigin]string {
		t.Helper()
		tmp := t.TempDir()
		_, metadataPath, err := readCacheBundleArchive(bytes.NewReader(data), tmp)
		assert.NilError(t, err)
		rows, err := readBundleMetadataRows(ctx, metadataPath)
		assert.NilError(t, err)
		frames := make(map[int64]string, len(rows.results))
		for _, row := range rows.results {
			frames[row.ID] = row.CallFrameJSON
		}
		out := make(map[resultOrigin]string, len(rows.resultOrigins))
		for _, row := range rows.resultOrigins {
			out[resultOrigin{storeUUID: row.OriginStoreUUID, resultID: uint64(row.OriginResultID)}] = frames[row.ResultID]
		}
		return out
	}
	origins1 := framesByOrigin(bundle1.Bytes())
	origins2 := framesByOrigin(bundle2.Bytes())
	for origin, frame2 := range origins2 {
		frame1, sharedOrigin := origins1[origin]
		if !sharedOrigin {
			continue
		}
		assert.Equal(t, frame1, frame2,
			"origin %v names two different results across exports", origin)
	}
	newOrigin := resultOrigin{storeUUID: origins2FirstStoreUUID(t, origins2), resultID: uint64(newID)}
	_, collides := origins1[newOrigin]
	assert.Assert(t, !collides, "the new work's origin %v already existed in the pre-prune export", newOrigin)
}

func origins2FirstStoreUUID(t *testing.T, origins map[resultOrigin]string) string {
	t.Helper()
	for origin := range origins {
		return origin.storeUUID
	}
	t.Fatal("no origins in bundle")
	return ""
}

// TestCacheBundleImportIdempotent is T-S2: the same bundle imported twice
// in one boot AND across a restart adds zero rows anywhere.
func TestCacheBundleImportIdempotent(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()

	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), nil, nil)
	assert.NilError(t, err)
	srvA, _, _ := newR16TestServer()
	rootCtxA := r16RootCtx(ctx, cacheA, srvA)
	var nameA String
	assert.NilError(t, srvA.Select(rootCtxA, srvA.root, &nameA, Selector{Field: "r16Obj"}, Selector{Field: "name"}))
	cacheTestReleaseSession(t, cacheA, rootCtxA)
	var bundle bytes.Buffer
	_, err = cacheA.ExportBundle(ctx, &bundle, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.NilError(t, cacheA.Close(context.Background()))

	bPath := filepath.Join(dir, "b.db")
	cacheB, err := NewCache(ctx, bPath, nil, nil)
	assert.NilError(t, err)
	seedBundleTestJunk(t, ctx, cacheB, 3)

	first, err := cacheB.ImportBundle(ctx, bytes.NewReader(bundle.Bytes()))
	assert.NilError(t, err)
	assert.Assert(t, first.RowsImported > 0)
	afterFirst := bundleTestSnapshotCounts(cacheB)

	// Same boot, same bundle again: zero growth, everything dedups by
	// origin, no terms teach twice.
	second, err := cacheB.ImportBundle(ctx, bytes.NewReader(bundle.Bytes()))
	assert.NilError(t, err)
	assert.Equal(t, 0, second.RowsImported)
	assert.Equal(t, first.RowsImported, second.RowsDedupedByOrigin)
	assert.Equal(t, 0, second.TermsImported)
	assert.DeepEqual(t, afterFirst, bundleTestSnapshotCounts(cacheB))

	// Across boots: the imported rows flushed locally (with their foreign
	// origins), restored, and the bundle re-imported — still zero growth.
	assert.NilError(t, cacheB.persistCurrentState(ctx))
	assert.NilError(t, cacheB.Close(context.Background()))
	cacheB2, err := NewCache(ctx, bPath, nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheB2.Close(context.Background()))
	}()
	assert.Equal(t, CachePersistenceResetNone, cacheB2.PersistenceResetReason())
	restored := bundleTestSnapshotCounts(cacheB2)
	assert.Equal(t, afterFirst.Results, restored.Results)
	assert.Equal(t, afterFirst.Origins, restored.Origins)

	third, err := cacheB2.ImportBundle(ctx, bytes.NewReader(bundle.Bytes()))
	assert.NilError(t, err)
	assert.Equal(t, 0, third.RowsImported)
	assert.Equal(t, first.RowsImported, third.RowsDedupedByOrigin)
	afterThird := bundleTestSnapshotCounts(cacheB2)
	assert.Equal(t, restored.Results, afterThird.Results)
	assert.Equal(t, restored.Origins, afterThird.Origins)
	assert.Equal(t, restored.PersistedEdges, afterThird.PersistedEdges)
}

// TestCacheBundleOverlappingBundlesUnionByOrigin: two overlapping exports
// of the same store merge as the union of their origins, never per-bundle
// copies.
func TestCacheBundleOverlappingBundlesUnionByOrigin(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()

	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), nil, nil)
	assert.NilError(t, err)
	seed := func(field string, v int) {
		key := cacheTestIntCall(field)
		_, err := cacheA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
			ResultCall:    key,
			IsPersistable: true,
		}, func(context.Context) (AnyResult, error) {
			return cacheTestIntResult(key, v), nil
		})
		assert.NilError(t, err)
	}
	seed("overlap-1", 1)
	cacheTestReleaseSession(t, cacheA, ctx)
	var bundle1 bytes.Buffer
	summary1, err := cacheA.ExportBundle(ctx, &bundle1, CacheBundleExportOptions{})
	assert.NilError(t, err)

	seed("overlap-2", 2)
	cacheTestReleaseSession(t, cacheA, ctx)
	var bundle2 bytes.Buffer
	summary2, err := cacheA.ExportBundle(ctx, &bundle2, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.Equal(t, summary1.Results+1, summary2.Results)
	assert.NilError(t, cacheA.Close(context.Background()))

	cacheB, err := NewCache(ctx, filepath.Join(dir, "b.db"), nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheB.Close(context.Background()))
	}()

	first, err := cacheB.ImportBundle(ctx, bytes.NewReader(bundle1.Bytes()))
	assert.NilError(t, err)
	assert.Equal(t, summary1.Results, first.RowsImported)
	second, err := cacheB.ImportBundle(ctx, bytes.NewReader(bundle2.Bytes()))
	assert.NilError(t, err)
	assert.Equal(t, 1, second.RowsImported, "only the genuinely new row crosses")
	assert.Equal(t, summary1.Results, second.RowsDedupedByOrigin)
}

// TestCacheBundleTokenWalkLoudOnUnknownShapes: the payload walk is a
// contract — a reserved key in any non-token shape fails loudly, data
// without reserved keys passes byte-verbatim.
func TestCacheBundleTokenWalkLoudOnUnknownShapes(t *testing.T) {
	t.Parallel()

	remapRef := func(id uint64) (uint64, error) { return id + 100, nil }
	identityCallID := func(encoded string) (string, error) { return encoded, nil }

	rewrite := func(raw string) (string, error) {
		out, err := rewritePersistedPayloadRefs(json.RawMessage(raw), remapRef, identityCallID)
		return string(out), err
	}

	// Happy path: nested tokens rewrite, everything else survives.
	out, err := rewrite(`{"a":{"$dagqlResultRef":5},"b":[{"$dagqlResultRef":6},"x"],"c":{"deep":{"$dagqlResultRef":7}},"n":1.25}`)
	assert.NilError(t, err)
	var parsed struct {
		A PersistedResultRef `json:"a"`
		B []json.RawMessage  `json:"b"`
		C map[string]PersistedResultRef
		N json.Number `json:"n"`
	}
	assert.NilError(t, json.Unmarshal([]byte(out), &parsed))
	assert.Equal(t, uint64(105), parsed.A.ResultID())
	assert.Equal(t, "1.25", parsed.N.String())

	// Unknown shapes around the reserved keys fail loudly.
	for _, malformed := range []string{
		`{"$dagqlResultRef":"not-a-number"}`,
		`{"$dagqlResultRef":5,"extra":true}`,
		`{"nested":{"$dagqlResultRef":{}}}`,
		`{"$dagqlCallID":42}`,
		`{"$dagqlCallID":"x","extra":1}`,
	} {
		_, err := rewrite(malformed)
		assert.Assert(t, err != nil, "malformed token %q was silently accepted", malformed)
	}

	// Data without reserved keys is untouched, byte-for-byte.
	verbatim := `{"z":[1,2,{"deep":"value"}],"f":0.30000000000000004}`
	out, err = rewrite(verbatim)
	assert.NilError(t, err)
	assert.Equal(t, verbatim, out)
}

// TestCacheBundleForwardClosureFilter: mid-serve exports carry exactly the
// forward dependency closure of the persisted-edge roots — a session-only
// row (retained by a live session, no persisted edge) stays home.
func TestCacheBundleForwardClosureFilter(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()

	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheA.Close(context.Background()))
	}()

	persistedKey := cacheTestIntCall("closure-persisted")
	_, err = cacheA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    persistedKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(persistedKey, 1), nil
	})
	assert.NilError(t, err)
	sessionKey := cacheTestIntCall("closure-session-only")
	sessionRes, err := cacheA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall: sessionKey,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(sessionKey, 2), nil
	})
	assert.NilError(t, err)
	sessionRowID := sessionRes.cacheSharedResult().id

	// The session is still live: the session-only row is in-flight state,
	// not durable cache, and must not cross.
	var bundle bytes.Buffer
	_, err = cacheA.ExportBundle(ctx, &bundle, CacheBundleExportOptions{})
	assert.NilError(t, err)

	tmp := t.TempDir()
	_, metadataPath, err := readCacheBundleArchive(bytes.NewReader(bundle.Bytes()), tmp)
	assert.NilError(t, err)
	rows, err := readBundleMetadataRows(ctx, metadataPath)
	assert.NilError(t, err)
	for _, row := range rows.results {
		assert.Assert(t, sharedResultID(row.ID) != sessionRowID,
			"session-only row crossed into the bundle")
	}
	cacheTestReleaseSession(t, cacheA, ctx)
}

// TestCacheBundleUnpruneableStripped: the unpruneable bit is
// engine-lifetime state and never crosses; bundle edges arrive
// ordinary-pruneable.
func TestCacheBundleUnpruneableStripped(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()

	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheA.Close(context.Background()))
	}()
	key := cacheTestIntCall("unpruneable-root")
	res, err := cacheA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 1), nil
	})
	assert.NilError(t, err)
	assert.NilError(t, cacheA.MakeResultUnpruneable(ctx, res))
	cacheTestReleaseSession(t, cacheA, ctx)

	var bundle bytes.Buffer
	_, err = cacheA.ExportBundle(ctx, &bundle, CacheBundleExportOptions{})
	assert.NilError(t, err)

	tmp := t.TempDir()
	_, metadataPath, err := readCacheBundleArchive(bytes.NewReader(bundle.Bytes()), tmp)
	assert.NilError(t, err)
	rows, err := readBundleMetadataRows(ctx, metadataPath)
	assert.NilError(t, err)
	assert.Assert(t, len(rows.persistedEdges) > 0)
	for _, edge := range rows.persistedEdges {
		assert.Assert(t, !edge.Unpruneable, "unpruneable bit crossed the boundary on edge %d", edge.ResultID)
	}
}

// TestCacheBundleCorruptBundleSkips is the chunk-A slice of T-S11: a
// corrupted bundle imports as a typed skip and the local store is exactly
// what it was — never a wipe, never a reset reason.
func TestCacheBundleCorruptBundleSkips(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()

	// A valid bundle to corrupt in targeted ways.
	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), nil, nil)
	assert.NilError(t, err)
	seedBundleTestJunk(t, ctx, cacheA, 2)
	var valid bytes.Buffer
	_, err = cacheA.ExportBundle(ctx, &valid, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.NilError(t, cacheA.Close(context.Background()))

	cacheB, err := NewCache(ctx, filepath.Join(dir, "b.db"), nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheB.Close(context.Background()))
	}()
	seedBundleTestJunk(t, ctx, cacheB, 2)
	before := bundleTestSnapshotCounts(cacheB)

	assertSkipped := func(data []byte, wantReason string) {
		t.Helper()
		_, err := cacheB.ImportBundle(ctx, bytes.NewReader(data))
		var skip *CacheBundleSkipError
		assert.Assert(t, errors.As(err, &skip), "expected a bundle skip, got %v", err)
		if wantReason != "" {
			assert.Equal(t, wantReason, skip.Reason)
		}
		assert.DeepEqual(t, before, bundleTestSnapshotCounts(cacheB))
		assert.Equal(t, CachePersistenceResetNone, cacheB.PersistenceResetReason())
	}

	// Garbage bytes: not an archive at all.
	assertSkipped([]byte("not a bundle"), CacheBundleSkipUnreadableArchive)

	// Truncated archive.
	assertSkipped(valid.Bytes()[:len(valid.Bytes())/2], "")

	// Version mismatch: rebuild the archive with a foreign schema version.
	tmp := t.TempDir()
	manifest, metadataPath, err := readCacheBundleArchive(bytes.NewReader(valid.Bytes()), tmp)
	assert.NilError(t, err)
	mismatched := manifest
	mismatched.SchemaVersion = "some-other-schema"
	var mismatchBundle bytes.Buffer
	assert.NilError(t, writeCacheBundleArchive(&mismatchBundle, mismatched, metadataPath))
	assertSkipped(mismatchBundle.Bytes(), CacheBundleSkipManifestMismatch)

	// Broken identity references inside the metadata DB.
	db, q, err := prepareCacheDBs(ctx, metadataPath)
	assert.NilError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO eq_class_digests (eq_class_id, digest, label) VALUES (999999, 'sha256:dead', '')`)
	assert.NilError(t, err)
	_, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	assert.NilError(t, err)
	assert.NilError(t, closeCacheDBs(db, q))
	var brokenBundle bytes.Buffer
	assert.NilError(t, writeCacheBundleArchive(&brokenBundle, manifest, metadataPath))
	assertSkipped(brokenBundle.Bytes(), CacheBundleSkipBrokenIdentity)

	// After all of that abuse, a valid bundle still imports.
	summary, err := cacheB.ImportBundle(ctx, bytes.NewReader(valid.Bytes()))
	assert.NilError(t, err)
	assert.Assert(t, summary.RowsImported > 0)
}

// TestCacheBundleExportExcludesSnapshotOnlyRows: a row whose only content
// source is a local snapshot (no lazy fragment, no chain yet in this
// phase) cannot keep its promise across the boundary and is excluded with
// a typed counter; rows with a lazy fallback cross.
func TestCacheBundleExportExcludesSnapshotOnlyRows(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	dir := t.TempDir()

	manager := &fakeSnapshotManager{}
	cacheA, err := NewCache(ctx, filepath.Join(dir, "a.db"), manager, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, cacheA.Close(context.Background()))
	}()

	snapKey := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistSnapshotValue{}).Type()),
		Field: "bundle-snapshot-only",
	}
	snapRes, err := cacheA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    snapKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(&persistSnapshotValue{Name: "snap", SnapshotID: "snapshot-x"}), nil
	})
	assert.NilError(t, err)
	snapRowID := snapRes.cacheSharedResult().id

	intKey := cacheTestIntCall("bundle-portable-int")
	_, err = cacheA.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    intKey,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(intKey, 5), nil
	})
	assert.NilError(t, err)
	cacheTestReleaseSession(t, cacheA, ctx)

	var bundle bytes.Buffer
	summary, err := cacheA.ExportBundle(ctx, &bundle, CacheBundleExportOptions{})
	assert.NilError(t, err)
	assert.Equal(t, 1, summary.ExcludedNoPortableContent)

	tmp := t.TempDir()
	_, metadataPath, err := readCacheBundleArchive(bytes.NewReader(bundle.Bytes()), tmp)
	assert.NilError(t, err)
	rows, err := readBundleMetadataRows(ctx, metadataPath)
	assert.NilError(t, err)
	for _, row := range rows.results {
		assert.Assert(t, sharedResultID(row.ID) != snapRowID, "snapshot-only row crossed without a portable content source")
	}
	// And no snapshotter refKeys cross, ever.
	db, q, err := prepareCacheDBs(ctx, metadataPath)
	assert.NilError(t, err)
	var linkCount int64
	assert.NilError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM result_snapshot_links`).Scan(&linkCount))
	assert.Equal(t, int64(0), linkCount)
	assert.NilError(t, closeCacheDBs(db, q))
}
