package schema

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/dagql/call"
	"github.com/dagger/dagger/engine"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// moduleDefinitionTestCache is a cache and server with no snapshot store
// and the real _moduleDefinition declaration installed over a counting
// resolver, so what the tests check is the field's identity as installed.
type moduleDefinitionTestResolver struct {
	runs atomic.Int32
	// typedefs, when set, supplies the definition's object typedefs.
	typedefs func() dagql.ObjectResultArray[*core.TypeDef]
}

func moduleDefinitionTestCache(t *testing.T, path, session string, stub *moduleDefinitionTestResolver) (context.Context, *dagql.Cache, *dagql.Server) {
	t.Helper()
	server := &currentTypeDefsTestServer{platform: core.Platform{OS: "linux", Architecture: "arm64"}}
	query := core.NewRoot(server)
	ctx := core.ContextWithQuery(t.Context(), query)
	ctx = engine.ContextWithClientMetadata(ctx, &engine.ClientMetadata{ClientID: session, SessionID: session})
	cache, err := dagql.NewCache(ctx, path, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.CloseDiscardingPersistence()) })
	ctx = dagql.ContextWithCache(ctx, cache)
	srv, err := dagql.NewServer(ctx, query)
	require.NoError(t, err)
	server.dag = srv
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*core.ModuleSource]{Typed: &core.ModuleSource{}}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*core.Module]{Typed: &core.Module{}}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*core.Container]{}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*core.File]{}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*core.TypeDef]{}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*core.ObjectTypeDef]{}))
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*core.SourceMap]{}))
	resolver := func(ctx context.Context, src dagql.ObjectResult[*core.ModuleSource], args moduleDefinitionArgs) (dagql.ObjectResult[*core.Module], error) {
		stub.runs.Add(1)
		runtime, err := args.Runtime.Load(ctx, srv)
		if err != nil {
			return dagql.ObjectResult[*core.Module]{}, err
		}
		def := &core.Module{NameField: args.ModuleName, Description: "definition of " + args.ModuleName, Runtime: dagql.NonNull(runtime)}
		if stub.typedefs != nil {
			def.ObjectDefs = stub.typedefs()
		}
		return dagql.NewObjectResultForCurrentCall(ctx, srv, def)
	}
	dagql.Fields[*core.ModuleSource]{moduleDefinitionField(resolver)}.Install(srv)
	return ctx, cache, srv
}

func withClient(ctx context.Context, client, session string) context.Context {
	return engine.ContextWithClientMetadata(ctx, &engine.ClientMetadata{ClientID: client, SessionID: session})
}

func attachDefinitionTestResult[T dagql.Typed](t *testing.T, ctx context.Context, cache *dagql.Cache, srv *dagql.Server, session, field string, value T) dagql.ObjectResult[T] {
	t.Helper()
	frame := &dagql.ResultCall{Kind: dagql.ResultCallKindField, Field: field, Type: dagql.NewResultCallType(value.Type())}
	res, err := cache.GetOrInitCall(ctx, session, srv, &dagql.CallRequest{ResultCall: frame, IsPersistable: true}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(value, srv, frame)
	})
	require.NoError(t, err)
	return res.(dagql.ObjectResult[T])
}

type definitionTestInputs struct {
	source  dagql.ObjectResult[*core.ModuleSource]
	runtime dagql.ObjectResult[*core.Container]
	schema  dagql.ObjectResult[*core.File]
}

// definitionTestInputsFor builds the three inputs of a definition: a source
// row named by sourceField carrying the content digest scoped, and runtime
// and schema rows named by their fields.
func definitionTestInputsFor(t *testing.T, ctx context.Context, cache *dagql.Cache, srv *dagql.Server, session, sourceField string, scoped digest.Digest, runtimeField, schemaField string) definitionTestInputs {
	t.Helper()
	src := attachDefinitionTestResult(t, ctx, cache, srv, session, sourceField, &core.ModuleSource{Kind: core.ModuleSourceKindDir, ModuleName: "demo", ModuleOriginalName: "demo"})
	src, err := src.WithContentDigest(ctx, scoped, call.ExtraDigestLabelRemoteCache)
	require.NoError(t, err)
	runtime := attachDefinitionTestResult(t, ctx, cache, srv, session, runtimeField, &core.Container{Platform: core.Platform{OS: "linux", Architecture: "arm64"}, FS: new(core.LazyAccessor[*core.Directory, *core.Container]), MetaSnapshot: new(core.LazyAccessor[bkcache.ImmutableRef, *core.Container])})
	schema := attachDefinitionTestResult(t, ctx, cache, srv, session, schemaField, &core.File{Platform: core.Platform{OS: "linux", Architecture: "arm64"}, File: new(core.LazyAccessor[string, *core.File]), Snapshot: new(core.LazyAccessor[bkcache.ImmutableRef, *core.File]), Lazy: &core.FileBlobLazy{LazyState: core.NewLazyState(), Filename: schemaField + ".json", Contents: []byte("{}")}})
	return definitionTestInputs{source: src, runtime: runtime, schema: schema}
}

func selectDefinition(t *testing.T, ctx context.Context, srv *dagql.Server, in definitionTestInputs, name string) dagql.ObjectResult[*core.Module] {
	t.Helper()
	runtimeID, err := in.runtime.ID()
	require.NoError(t, err)
	schemaID, err := in.schema.ID()
	require.NoError(t, err)
	var def dagql.ObjectResult[*core.Module]
	require.NoError(t, srv.Select(ctx, in.source, &def, dagql.Selector{
		Field: "_moduleDefinition",
		Args: []dagql.NamedInput{
			{Name: "runtime", Value: dagql.NewID[*core.Container](runtimeID)},
			{Name: "introspectionJson", Value: dagql.NewID[*core.File](schemaID)},
			{Name: "moduleName", Value: dagql.String(name)},
		},
	}))
	return def
}

// The definition is keyed on the scoped source, the runtime, the schema
// file and the loaded name, with no additional per-client input.
func TestModuleDefinitionIdentity(t *testing.T) {
	t.Parallel()
	stub := &moduleDefinitionTestResolver{}
	runs := &stub.runs
	ctx, cache, srv := moduleDefinitionTestCache(t, "", "s1", stub)
	scoped := digest.FromString("scoped source")
	in := definitionTestInputsFor(t, ctx, cache, srv, "s1", "source", scoped, "runtime", "schema")

	first := selectDefinition(t, ctx, srv, in, "demo")
	require.EqualValues(t, 1, runs.Load())
	require.Equal(t, "definition of demo", first.Self().Description)

	// A second client in a second session with the same inputs gets the
	// same row without a run.
	ctx2 := withClient(ctx, "c2", "s2")
	second := selectDefinition(t, ctx2, srv, in, "demo")
	require.EqualValues(t, 1, runs.Load(), "no per-client input: the second client hits")
	require.Same(t, first.Unwrap(), second.Unwrap())

	// The loaded name is part of the key.
	selectDefinition(t, ctx, srv, in, "renamed")
	require.EqualValues(t, 2, runs.Load())

	// A different runtime alone, and a different schema file alone, each
	// change the key.
	otherRuntime := definitionTestInputsFor(t, ctx, cache, srv, "s1", "source", scoped, "runtime-2", "schema")
	selectDefinition(t, ctx, srv, otherRuntime, "demo")
	require.EqualValues(t, 3, runs.Load(), "a changed runtime is a new definition")
	otherSchema := definitionTestInputsFor(t, ctx, cache, srv, "s1", "source", scoped, "runtime", "schema-2")
	selectDefinition(t, ctx, srv, otherSchema, "demo")
	require.EqualValues(t, 4, runs.Load(), "a changed schema file is a new definition")

	// A source with a different content digest, the effect of a source
	// edit, is a new definition; the same digest under another recipe is
	// not, which is the cross-client and cross-engine case.
	edited := definitionTestInputsFor(t, ctx, cache, srv, "s1", "source-edited", digest.FromString("edited source"), "runtime", "schema")
	selectDefinition(t, ctx, srv, edited, "demo")
	require.EqualValues(t, 5, runs.Load(), "a changed source digest is a new definition")
	equivalent := definitionTestInputsFor(t, ctx, cache, srv, "s1", "source-other-recipe", scoped, "runtime", "schema")
	same := selectDefinition(t, ctx, srv, equivalent, "demo")
	require.EqualValues(t, 5, runs.Load(), "the same scoped digest under another recipe hits structurally")
	require.Same(t, first.Unwrap(), same.Unwrap())
}

// The discovery scope of a definition covers the definition's inputs, so
// two definitions of one source that differ in runtime, schema file or
// name never share a scoped module. ScopeModuleForSDKOperation keys the
// attached module on the operation name and the source digest only
// (core/sdk/utils.go), and attachment returns an existing match; the name
// is where the inputs must go.
func TestModuleDefinitionScopeCoversInputs(t *testing.T) {
	t.Parallel()
	stub := &moduleDefinitionTestResolver{}
	ctx, cache, srv := moduleDefinitionTestCache(t, "", "s1", stub)
	scoped := digest.FromString("scoped source")
	base := definitionTestInputsFor(t, ctx, cache, srv, "s1", "source", scoped, "runtime", "schema")
	otherRuntime := definitionTestInputsFor(t, ctx, cache, srv, "s1", "source", scoped, "runtime-2", "schema")
	otherSchema := definitionTestInputsFor(t, ctx, cache, srv, "s1", "source", scoped, "runtime", "schema-2")
	op := func(in definitionTestInputs, name string) string {
		op, err := moduleDefinitionScopeOp(ctx, in.runtime, in.schema, name)
		require.NoError(t, err)
		return op
	}
	require.Equal(t, op(base, "demo"), op(base, "demo"), "the same inputs name the same scope")
	require.NotEqual(t, op(base, "demo"), op(otherRuntime, "demo"), "a different runtime names a different scope")
	require.NotEqual(t, op(base, "demo"), op(otherSchema, "demo"), "a different schema file names a different scope")
	require.NotEqual(t, op(base, "demo"), op(base, "renamed"), "a different name names a different scope")
	require.NotEqual(t, "getModDef", op(base, "demo"), "the cached path never uses the bare operation name")
}

// A definition exported inside a module-object leaf and imported into a
// cache with different row numbers is hit there by equivalent inputs
// reconstructed under that cache's own recipes, with the definition's
// typedef, its SourceMap and its runtime reference relocated.
func TestModuleDefinitionImportedHit(t *testing.T) {
	t.Parallel()
	stubA := &moduleDefinitionTestResolver{}
	ctx, a, srvA := moduleDefinitionTestCache(t, filepath.Join(t.TempDir(), "a.db"), "a", stubA)
	scoped := digest.FromString("scoped source")
	inA := definitionTestInputsFor(t, ctx, a, srvA, "a", "source", scoped, "runtime", "schema")
	// The definition's typedef and its SourceMap exist before the
	// definition is attached, as the runtime's answer does.
	sourceMap := attachDefinitionTestResult(t, ctx, a, srvA, "a", "definition-sourcemap", &core.SourceMap{Module: "demo", Filename: "main.go", Line: 3, Column: 1})
	objDef := attachDefinitionTestResult(t, ctx, a, srvA, "a", "definition-object", core.NewObjectTypeDef("Holder", "a holder", nil).WithSourceMap(sourceMap))
	typeDef := attachDefinitionTestResult(t, ctx, a, srvA, "a", "definition-typedef", (&core.TypeDef{}).WithObject(objDef))
	stubA.typedefs = func() dagql.ObjectResultArray[*core.TypeDef] { return dagql.ObjectResultArray[*core.TypeDef]{typeDef} }
	defA := selectDefinition(t, ctx, srvA, inA, "demo")
	require.EqualValues(t, 1, stubA.runs.Load())
	require.Len(t, defA.Self().ObjectDefs, 1)

	// The defining module owns the definition, as asModule's result does,
	// and a module object of that module is the leaf that gets exported.
	modA := attachDefinitionTestResult(t, ctx, a, srvA, "a", "module", &core.Module{NameField: "demo", Definition: dagql.NonNull(defA), ObjectDefs: dagql.ObjectResultArray[*core.TypeDef]{typeDef}})
	shapeA := &core.ModuleObject{Module: modA, TypeDef: objDef.Self()}
	srvA.InstallObject(dagql.NewClass(srvA, dagql.ClassOpts[*core.ModuleObject]{Typed: shapeA}))
	holderA := &core.ModuleObject{Module: modA, TypeDef: objDef.Self(), Fields: map[string]any{"label": "x"}}
	// The leaf's call frame names its module by row, as a module function's
	// call does (Module.ResultCallModule), which is what puts the module and
	// its definition in the leaf's exported closure.
	leafFrame := &dagql.ResultCall{Kind: dagql.ResultCallKindField, Field: "holder", Type: dagql.NewResultCallType(holderA.Type()), Module: &dagql.ResultCallModule{Name: "demo", ResultRef: &dagql.ResultCallRef{ResultID: persistedID(t, a, modA)}}}
	leaf, err := a.GetOrInitCall(ctx, "a", srvA, &dagql.CallRequest{ResultCall: leafFrame, IsPersistable: true}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(holderA, srvA, leafFrame)
	})
	require.NoError(t, err)
	var bundle dagql.ValueBundle
	require.NoError(t, a.WithExportedValues(ctx, dagql.ValueSelection{Roots: []dagql.AnyResult{leaf}}, config.RefConfig{}, func(_ context.Context, values *dagql.ExportedValues) error {
		bundle = values.Bundle
		return nil
	}))
	require.Len(t, bundle.Values, 9, "leaf, module, definition, typedef, object typedef, source map, source, runtime, schema")

	stubB := &moduleDefinitionTestResolver{}
	bctx, b, srvB := moduleDefinitionTestCache(t, filepath.Join(t.TempDir(), "b.db"), "b", stubB)
	for i := range 7 {
		attachDefinitionTestResult(t, bctx, b, srvB, "b", "padding", &core.Module{NameField: string(rune('p' + i))})
	}
	mapping, err := b.ImportValues(bctx, bundle)
	require.NoError(t, err)
	require.Len(t, mapping, 1)

	// B reconstructs the inputs under its own recipes: the source under a
	// different recipe with the same scoped digest, the runtime and schema
	// under the same recipes, as a cold engine does.
	inB := definitionTestInputsFor(t, bctx, b, srvB, "b", "source-on-b", scoped, "runtime", "schema")
	defB := selectDefinition(t, bctx, srvB, inB, "demo")
	require.Zero(t, stubB.runs.Load(), "B hits the imported definition without running the resolver")
	require.True(t, dagql.IsImportedResult(defB))
	require.NotEqual(t, persistedID(t, a, defA), persistedID(t, b, defB), "B's row number differs")
	require.Equal(t, "definition of demo", defB.Self().Description)
	require.Len(t, defB.Self().ObjectDefs, 1, "the definition's typedef travelled")
	objB := defB.Self().ObjectDefs[0].Self().AsObject.Value.Self()
	require.Equal(t, "Holder", objB.Name)
	require.Equal(t, "a holder", objB.Description)
	require.True(t, objB.SourceMap.Valid, "the typedef's SourceMap travelled")
	require.Equal(t, &core.SourceMap{Module: "demo", Filename: "main.go", Line: 3, Column: 1}, objB.SourceMap.Value.Self())
	require.True(t, dagql.IsImportedResult(objB.SourceMap.Value))
	require.True(t, defB.Self().Runtime.Valid, "the runtime reference relocated with the definition")
	require.True(t, dagql.IsImportedResult(defB.Self().Runtime.Value))
	require.NotEqual(t, persistedID(t, a, inA.runtime), persistedID(t, b, defB.Self().Runtime.Value))
	require.Equal(t, persistedID(t, b, inB.runtime), persistedID(t, b, defB.Self().Runtime.Value), "B's own runtime lookup is the imported runtime row")

	// The leaf decodes through its module, resolved from the leaf's call
	// frame as the engine does for module-defined results, and that module
	// is B's imported row whose definition is B's definition row.
	var resolvedModules []uint64
	srvB.SetResultServerForCall(func(ctx context.Context, call *dagql.ResultCall) (*dagql.Server, error) {
		if call.Module == nil || call.Module.ResultRef == nil {
			return srvB, nil
		}
		modAny, err := b.LoadResultByResultID(ctx, "b", srvB, call.Module.ResultRef.ResultID)
		if err != nil {
			return nil, err
		}
		modB := modAny.(dagql.ObjectResult[*core.Module])
		resolvedModules = append(resolvedModules, call.Module.ResultRef.ResultID)
		srvB.InstallObject(dagql.NewClass(srvB, dagql.ClassOpts[*core.ModuleObject]{Typed: &core.ModuleObject{Module: modB, TypeDef: modB.Self().ObjectDefs[0].Self().AsObject.Value.Self()}}))
		return srvB, nil
	})
	loaded, err := b.LoadResultByResultID(bctx, "b", srvB, mapping[0].ResultID)
	require.NoError(t, err)
	holder := loaded.Unwrap().(*core.ModuleObject)
	require.Equal(t, map[string]any{"label": "x"}, holder.Fields)
	require.Len(t, resolvedModules, 1, "the leaf's frame named its module by row")
	require.NotEqual(t, persistedID(t, a, modA), resolvedModules[0], "the module row was relocated")
	require.True(t, dagql.IsImportedResult(holder.Module))
	require.True(t, holder.Module.Self().Definition.Valid)
	require.Equal(t, persistedID(t, b, defB), persistedID(t, b, holder.Module.Self().Definition.Value), "the leaf's module references B's definition row")
}

func persistedID(t *testing.T, cache *dagql.Cache, res dagql.AnyResult) uint64 {
	t.Helper()
	id, err := cache.PersistedResultID(res)
	require.NoError(t, err)
	return id
}
