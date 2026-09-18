package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

// unretainedTestResult is persistedListTestResult without a retention edge:
// it is held by its session only, so it is collected when the session ends.
func unretainedTestResult(t *testing.T, ctx context.Context, cache *Cache, srv *Server, sessionID, field string, value Typed) AnyResult {
	t.Helper()
	frame := &ResultCall{Kind: ResultCallKindField, Field: field, Type: NewResultCallType(value.Type())}
	res, err := cache.GetOrInitCall(ctx, sessionID, srv, &CallRequest{ResultCall: frame}, func(context.Context) (AnyResult, error) {
		return NewResultForCall(value, frame)
	})
	require.NoError(t, err)
	return res
}

func ownershipCount(c *Cache, res AnyResult) int64 {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	return res.cacheSharedResult().incomingOwnershipCount
}

func TestSessionResults(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	_, err := c.SessionResults(ctx, "")
	require.Error(t, err)
	entries, err := c.SessionResults(ctx, "never-seen")
	require.NoError(t, err)
	require.Empty(t, entries)

	// base <- exec <- stdout, where stdout is not retained; imported comes
	// from another cache and is loaded into the same session.
	base := persistedListTestResult(t, ctx, c, srv, "from", &transferTestValue{Text: "base"})
	exec := persistedListTestResult(t, ctx, c, srv, "withExec", &transferTestValue{Text: "exec"})
	transferTestDependency(c, ctx, exec, base)
	stdout := unretainedTestResult(t, ctx, c, srv, "test-session", "stdout", String("out"))
	transferTestDependency(c, ctx, stdout, exec)
	list := persistedListTestResult(t, ctx, c, srv, "list", DynamicResultArrayOutput{Elem: String(""), Values: []AnyResult{stdout}})

	octx, other, osrv := transferTestCache(t)
	foreign := persistedListTestResult(t, octx, other, osrv, "foreign", &transferTestValue{Text: "foreign"})
	mapping, err := c.ImportValues(ctx, exportTestBundle(t, octx, other, foreign))
	require.NoError(t, err)
	imported, err := c.LoadResultByResultID(ctx, "test-session", srv, mapping[0].ResultID)
	require.NoError(t, err)
	require.True(t, IsImportedResult(imported))

	before := map[string]int64{}
	for name, res := range map[string]AnyResult{"base": base, "exec": exec, "stdout": stdout, "list": list, "imported": imported} {
		before[name] = ownershipCount(c, res)
	}

	entries, err = c.SessionResults(ctx, "test-session")
	require.NoError(t, err)
	byID := map[uint64]SessionResultEntry{}
	for i, entry := range entries {
		byID[entry.ResultID] = entry
		if i > 0 {
			require.Less(t, entries[i-1].ResultID, entry.ResultID, "entries are sorted by result number")
		}
	}
	id := func(res AnyResult) uint64 { return uint64(res.cacheSharedResult().id) }
	require.Len(t, entries, 5)

	baseEntry := byID[id(base)]
	require.Equal(t, SessionResultEntry{ResultID: id(base), Type: "TransferTestValue", Field: "from", DependsOn: []uint64{}, Retained: true, RecipeDigest: baseEntry.RecipeDigest}, baseEntry)
	require.NotEmpty(t, baseEntry.RecipeDigest)
	expected, err := base.cacheSharedResult().loadResultCall().deriveRecipeDigest(c)
	require.NoError(t, err)
	require.Equal(t, expected, baseEntry.RecipeDigest)

	execEntry := byID[id(exec)]
	require.Equal(t, []uint64{id(base)}, execEntry.DependsOn)
	require.True(t, execEntry.Retained)
	require.False(t, execEntry.Imported)
	require.NotEmpty(t, execEntry.RecipeDigest)
	require.NotEqual(t, baseEntry.RecipeDigest, execEntry.RecipeDigest)

	stdoutEntry := byID[id(stdout)]
	require.Equal(t, SessionResultEntry{ResultID: id(stdout), Type: "String", Field: "stdout", DependsOn: []uint64{id(exec)}, Retained: false, Imported: false}, stdoutEntry, "an unretained result carries no recipe digest")

	listEntry := byID[id(list)]
	require.Equal(t, "[String]", listEntry.Type)
	require.Equal(t, []uint64{id(stdout)}, listEntry.DependsOn)

	importedEntry := byID[id(imported)]
	require.True(t, importedEntry.Imported)
	require.True(t, importedEntry.Retained, "an imported root has a retention edge")
	require.Empty(t, importedEntry.RecipeDigest, "an imported result carries no recipe digest")
	require.Equal(t, "foreign", importedEntry.Field)

	for name, res := range map[string]AnyResult{"base": base, "exec": exec, "stdout": stdout, "list": list, "imported": imported} {
		require.Equal(t, before[name], ownershipCount(c, res), "%s: holds are released", name)
	}
	require.Zero(t, c.activeGlobalOperations.Load())

	// A result whose row left the cache between the snapshot and the walk is
	// skipped, not reported.
	require.NoError(t, c.ReleaseSession(ctx, "test-session"))
	waitSessionRelease(t, ctx, c, "test-session")
	entries, err = c.SessionResults(ctx, "test-session")
	require.NoError(t, err)
	require.Empty(t, entries, "a released session has no set")
}

func TestSessionResultsHoldsSurviveRelease(t *testing.T) {
	t.Parallel()
	// Between the walk under the graph lock and the digest derivation, the
	// session can be released and a retention edge can go. The holds keep
	// the retained entries and what they depend on alive until the digests
	// are derived, and release them afterwards.
	ctx, c, srv := transferTestCache(t)
	dep := unretainedTestResult(t, ctx, c, srv, "test-session", "dep", String("dep"))
	root := persistedListTestResult(t, ctx, c, srv, "root", &transferTestValue{Text: "root"})
	transferTestDependency(c, ctx, root, dep)
	depID, rootID := dep.cacheSharedResult().id, root.cacheSharedResult().id
	// A fresh frame, so the derivation inside SessionResults is real and not
	// a memoized read. The expected digest comes from a separate clone, since
	// the root is collected before SessionResults returns.
	root.cacheSharedResult().storeResultCall(root.cacheSharedResult().loadResultCall().clone())
	expected, err := root.cacheSharedResult().loadResultCall().clone().deriveRecipeDigest(c)
	require.NoError(t, err)
	require.NotEmpty(t, expected)
	hookRan := false
	c.testAfterSessionResultsHeld = func() {
		hookRan = true
		require.NoError(t, c.ReleaseSession(ctx, "test-session"))
		waitSessionRelease(t, ctx, c, "test-session")
		removed, err := c.removePersistedEdge(ctx, rootID)
		require.NoError(t, err)
		require.True(t, removed)
		c.egraphMu.RLock()
		defer c.egraphMu.RUnlock()
		require.NotNil(t, c.resultsByID[rootID], "held by SessionResults after the session and the retention edge are gone")
		require.NotNil(t, c.resultsByID[depID], "kept by the held root")
		require.Equal(t, int64(1), c.resultsByID[rootID].incomingOwnershipCount)
	}
	entries, err := c.SessionResults(ctx, "test-session")
	require.NoError(t, err)
	require.True(t, hookRan)
	require.Len(t, entries, 2)
	require.Equal(t, uint64(rootID), entries[1].ResultID)
	require.True(t, entries[1].Retained, "retained at the time of the walk")
	require.Equal(t, expected, entries[1].RecipeDigest)
	rootRow, depRow := rowsByID(c, rootID, depID)
	require.Nil(t, rootRow, "released and collected after the digests")
	require.Nil(t, depRow)
	require.Zero(t, c.activeGlobalOperations.Load())
}

// rowsByID reads two rows under the graph lock and returns them, so the
// caller asserts with no lock held: a failed assertion must not leave the
// lock taken, because the test's cleanup closes the cache under it.
func rowsByID(c *Cache, a, b sharedResultID) (*sharedResult, *sharedResult) {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	return c.resultsByID[a], c.resultsByID[b]
}

// waitSessionRelease waits for a release with a short deadline, so a
// release regression fails this test instead of holding the package.
func waitSessionRelease(t *testing.T, ctx context.Context, c *Cache, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, c.WaitSessionRelease(ctx, sessionID))
}

func TestWithResultsByNumber(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	require.Error(t, c.WithResultsByNumber(ctx, nil, nil))

	held := unretainedTestResult(t, ctx, c, srv, "test-session", "held", String("held"))
	retained := persistedListTestResult(t, ctx, c, srv, "retained", String("retained"))
	heldID, retainedID := uint64(held.cacheSharedResult().id), uint64(retained.cacheSharedResult().id)
	heldShared := held.cacheSharedResult()
	require.Equal(t, int64(1), ownershipCount(c, held), "the session is the only owner")

	var calls int
	err := c.WithResultsByNumber(ctx, []uint64{heldID, 9999, retainedID, heldID}, func(ctx context.Context, found []AnyResult, missing []uint64) error {
		calls++
		require.Equal(t, []uint64{9999}, missing)
		require.Len(t, found, 4)
		require.Same(t, heldShared, found[0].cacheSharedResult())
		require.Nil(t, found[1])
		require.Same(t, retained.cacheSharedResult(), found[2].cacheSharedResult())
		require.Same(t, heldShared, found[3].cacheSharedResult())
		require.Equal(t, int64(2), ownershipCount(c, held), "one hold per distinct number")

		// The session ends while the result is held: it survives until fn
		// returns.
		require.NoError(t, c.ReleaseSession(ctx, "test-session"))
		waitSessionRelease(t, ctx, c, "test-session")
		c.egraphMu.RLock()
		defer c.egraphMu.RUnlock()
		require.Same(t, heldShared, c.resultsByID[sharedResultID(heldID)])
		require.Equal(t, int64(1), heldShared.incomingOwnershipCount)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	heldRow, retainedRow := rowsByID(c, sharedResultID(heldID), sharedResultID(retainedID))
	require.Nil(t, heldRow, "released after fn and collected")
	require.NotNil(t, retainedRow)
	require.Equal(t, int64(1), ownershipCount(c, retained), "the retention edge is the only owner left")
	require.Zero(t, c.activeGlobalOperations.Load())

	// fn's error is returned, and the holds are still released.
	boom := errors.New("boom")
	err = c.WithResultsByNumber(ctx, []uint64{retainedID}, func(context.Context, []AnyResult, []uint64) error { return boom })
	require.ErrorIs(t, err, boom)
	require.Equal(t, int64(1), ownershipCount(c, retained))

	// A canceled context refuses before fn runs, with nothing held.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err = c.WithResultsByNumber(canceled, []uint64{retainedID}, func(context.Context, []AnyResult, []uint64) error { t.Fatal("canceled consumer"); return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int64(1), ownershipCount(c, retained))

	// All numbers missing is not an error: fn decides.
	err = c.WithResultsByNumber(ctx, []uint64{9998, 9999}, func(_ context.Context, found []AnyResult, missing []uint64) error {
		require.Equal(t, []AnyResult{nil, nil}, found)
		require.Equal(t, []uint64{9998, 9999}, missing)
		return nil
	})
	require.NoError(t, err)
	require.Zero(t, c.activeGlobalOperations.Load())
}

// remoteServiceTestValue is a transfer value with named parts, each pending
// or completed with a snapshot. Like core's Directory, its foreign form
// carries no snapshot, so an exported completed part is described by a
// bundle output, not by the record.
type remoteServiceTestValue struct {
	Name  string            `json:"name"`
	Parts map[string]string `json:"parts,omitempty"`
}

func (*remoteServiceTestValue) Type() *ast.Type {
	return &ast.Type{NamedType: "RemoteServiceTestValue", NonNull: true}
}
func (v *remoteServiceTestValue) links() []PersistedSnapshotRefLink {
	var links []PersistedSnapshotRefLink
	for _, part := range slices.Sorted(maps.Keys(v.Parts)) {
		if v.Parts[part] != "" {
			links = append(links, PersistedSnapshotRefLink{Role: part, RefKey: v.Parts[part]})
		}
	}
	return links
}
func (v *remoteServiceTestValue) EncodePersistedObject(context.Context, *PersistEncodeContext) (PersistedObjectEncoding, error) {
	raw, err := json.Marshal(v)
	return PersistedObjectEncoding{JSON: raw, SnapshotLinks: v.links()}, err
}
func (v *remoteServiceTestValue) PersistedSnapshotRefLinks() []PersistedSnapshotRefLink {
	return v.links()
}
func (*remoteServiceTestValue) PersistedOutputRevision() (OutputRevision, error) { return 0, nil }
func (*remoteServiceTestValue) DecodePersistedObject(_ context.Context, _ *PersistDecodeContext, raw json.RawMessage) (Typed, error) {
	value := new(remoteServiceTestValue)
	return value, json.Unmarshal(raw, value)
}

type remoteServiceTestCodec struct{}

func (remoteServiceTestCodec) VisitPersistedReferences(v PersistedPayloadVisit, visit PersistedRefVisitor) (json.RawMessage, error) {
	if err := VisitPersistedSnapshotRoles(visit, PersistedRefOutputRole, v.Path, v.SnapshotLinks); err != nil {
		return nil, err
	}
	return v.Payload, nil
}
func (remoteServiceTestCodec) NormalizeForeign(v PersistedPayloadVisit) (ForeignPayload, error) {
	value := new(remoteServiceTestValue)
	if err := json.Unmarshal(v.Payload, value); err != nil {
		return ForeignPayload{}, err
	}
	for part := range value.Parts {
		value.Parts[part] = ""
	}
	raw, err := json.Marshal(value)
	return ForeignPayload{JSON: raw}, err
}
func (remoteServiceTestCodec) ValidateForeign(PersistedPayloadVisit) error { return nil }
func (remoteServiceTestCodec) MapSnapshotParts(v PersistedPayloadVisit) ([]CapturedCodecOutput, error) {
	value := new(remoteServiceTestValue)
	if err := json.Unmarshal(v.Payload, value); err != nil {
		return nil, err
	}
	var out []CapturedCodecOutput
	for _, part := range slices.Sorted(maps.Keys(value.Parts)) {
		o := CapturedCodecOutput{Address: PersistedPartAddress{OutputPath: v.Path, Part: PartKey(part)}, Role: part, State: "pending", ValueKind: "directory", Value: &SnapshotValue{Kind: "directory"}}
		for _, link := range v.SnapshotLinks {
			if link.Role == part && link.RefKey != "" {
				o.State, o.SnapshotID = "completed", link.RefKey
			}
		}
		out = append(out, o)
	}
	return out, nil
}
func init() {
	RegisterPersistedObjectFamily(PersistedObjectFamily{Name: "dagql_test.RemoteService", Typed: (*remoteServiceTestValue)(nil), Visitor: remoteServiceTestCodec{}, Transfer: remoteServiceTestCodec{}})
}

// remoteServiceTestRef is a snapshot reference with no storage behind it
// whose export chain is a fixed list of layers, so an export's chain opening
// can be checked without a real store, mounts or privileges.
type remoteServiceTestRef struct {
	id     string
	layers []snapshots.ExportLayer
}

func (r *remoteServiceTestRef) ID() string                          { return r.id }
func (r *remoteServiceTestRef) SnapshotID() string                  { return r.id }
func (r *remoteServiceTestRef) Release(context.Context) error       { return nil }
func (r *remoteServiceTestRef) Size(context.Context) (int64, error) { return 0, nil }
func (r *remoteServiceTestRef) Mount(context.Context, bool) (snapshots.MountableRef, error) {
	panic("export must not mount")
}
func (r *remoteServiceTestRef) ExportChain(context.Context, config.RefConfig) (*snapshots.ExportChain, error) {
	return &snapshots.ExportChain{Layers: r.layers}, nil
}

type remoteServiceTestManager struct {
	fakeSnapshotManager
	layers map[string][]snapshots.ExportLayer
	opened []string
}

func (m *remoteServiceTestManager) GetBySnapshotID(_ context.Context, id string, _ ...snapshots.RefOption) (snapshots.ImmutableRef, error) {
	layers, ok := m.layers[id]
	if !ok {
		return nil, errors.New("unknown snapshot " + id)
	}
	m.opened = append(m.opened, id)
	return &remoteServiceTestRef{id: id, layers: layers}, nil
}

func testLayer(name string) snapshots.ExportLayer {
	return snapshots.ExportLayer{Descriptor: ocispecs.Descriptor{MediaType: ocispecs.MediaTypeImageLayer, Digest: digest.FromString(name), Size: int64(len(name))}}
}

func TestValueSelectionOutputsOf(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	srv.InstallObject(NewClass(srv, ClassOpts[*remoteServiceTestValue]{}))
	manager := &remoteServiceTestManager{layers: map[string][]snapshots.ExportLayer{
		"snap-root-a": {testLayer("root-a-1"), testLayer("root-a-2")},
		"snap-dep-c":  {testLayer("dep-c-1")},
	}}
	c.snapshotManager = manager
	// root has one completed part and one pending part. dep has one
	// completed part and is not named in OutputsOf at first.
	dep := persistedListTestResult(t, ctx, c, srv, "dep", &remoteServiceTestValue{Name: "dep", Parts: map[string]string{"c": "snap-dep-c"}})
	root := persistedListTestResult(t, ctx, c, srv, "root", &remoteServiceTestValue{Name: "root", Parts: map[string]string{"a": "snap-root-a", "b": ""}})
	transferTestDependency(c, ctx, root, dep)
	outside := persistedListTestResult(t, ctx, c, srv, "outside", String("outside"))

	export := func(selection ValueSelection) (ValueBundle, []SelectedChain, error) {
		var bundle ValueBundle
		var chains []SelectedChain
		err := c.WithExportedValues(ctx, selection, config.RefConfig{}, func(_ context.Context, values *ExportedValues) error {
			bundle = values.Bundle
			chains = values.Chains.Entries
			return nil
		})
		return bundle, chains, err
	}

	bundle, chains, err := export(ValueSelection{Roots: []AnyResult{root}, OutputsOf: []AnyResult{root}})
	require.NoError(t, err)
	require.Len(t, bundle.Outputs, 1, "exactly the completed parts of the named result")
	require.Equal(t, PartKey("a"), bundle.Outputs[0].Address.Part)
	require.Equal(t, "completed", bundle.Outputs[0].State)
	require.NotNil(t, bundle.Outputs[0].Chain)
	require.Equal(t, manager.layers["snap-root-a"], bundle.Outputs[0].Chain.Layers)
	require.Empty(t, bundle.Outputs[0].Chain.Addresses)
	require.Len(t, chains, 1)
	require.Equal(t, []string{"snap-root-a"}, manager.opened)

	// Naming dep too adds its completed part. Naming a part in Outputs that
	// OutputsOf also covers selects it once.
	manager.opened = nil
	bundle, chains, err = export(ValueSelection{
		Roots:     []AnyResult{root},
		Outputs:   []SelectedValueOutput{{Result: root, Address: PersistedPartAddress{Part: "a"}}},
		OutputsOf: []AnyResult{root, dep},
	})
	require.NoError(t, err)
	require.Len(t, bundle.Outputs, 2)
	require.Len(t, chains, 2)
	require.ElementsMatch(t, []string{"snap-root-a", "snap-dep-c"}, manager.opened)

	// Without OutputsOf nothing is selected, as before.
	bundle, chains, err = export(ValueSelection{Roots: []AnyResult{root}})
	require.NoError(t, err)
	require.Empty(t, bundle.Outputs)
	require.Empty(t, chains)

	// A result outside the captured closure is refused, and nothing stays held.
	before := ownershipCount(c, outside)
	_, _, err = export(ValueSelection{Roots: []AnyResult{root}, OutputsOf: []AnyResult{outside}})
	require.ErrorContains(t, err, "outside captured closure")
	require.Equal(t, before, ownershipCount(c, outside))
	require.Zero(t, c.activeGlobalOperations.Load())
}
