package core

import (
	"context"
	"testing"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine"
	"github.com/stretchr/testify/require"
)

func TestCurrentModulePersistenceRoundTripsModuleRef(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	cache, err := dagql.NewCache(ctx, "", nil, nil)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, cache.Close(context.Background()))
	}()
	ctx = dagql.ContextWithCache(ctx, cache)

	root := &Query{}
	dag := newCoreDagqlServerForTest(t, root)
	dag.InstallObject(dagql.NewClass(dag, dagql.ClassOpts[*Module]{Typed: &Module{}}))

	modCall := &dagql.ResultCall{
		Kind:        dagql.ResultCallKindSynthetic,
		SyntheticOp: "current-module-persistence-module",
		Type:        dagql.NewResultCallType((&Module{}).Type()),
	}
	modAny, err := cache.GetOrInitCall(ctx, "current-module-persistence", dag, &dagql.CallRequest{
		ResultCall:    modCall,
		IsPersistable: true,
	}, func(callCtx context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCurrentCall(callCtx, dag, &Module{NameField: "persisted-current-module"})
	})
	require.NoError(t, err)
	modRes, ok := modAny.(dagql.ObjectResult[*Module])
	require.True(t, ok)

	current := &CurrentModule{Module: modRes}
	encoded, err := current.EncodePersistedObject(ctx, cache)
	require.NoError(t, err)

	decoded, err := (&CurrentModule{}).DecodePersistedObject(ctx, dag, 0, nil, encoded.JSON)
	require.NoError(t, err)
	decodedCurrent, ok := decoded.(*CurrentModule)
	require.True(t, ok)
	require.Equal(t, "persisted-current-module", decodedCurrent.Module.Self().NameField)

	attachedMod, err := dagql.NewObjectResultForCall(
		&Module{NameField: "attached-current-module"},
		dag,
		&dagql.ResultCall{
			Kind:        dagql.ResultCallKindSynthetic,
			SyntheticOp: "attached-current-module",
			Type:        dagql.NewResultCallType((&Module{}).Type()),
		},
	)
	require.NoError(t, err)
	deps, err := current.AttachDependencyResults(ctx, nil, func(res dagql.AnyResult) (dagql.AnyResult, error) {
		require.Equal(t, modRes.Self(), res.Unwrap())
		return attachedMod, nil
	})
	require.NoError(t, err)
	require.Len(t, deps, 1)
	require.Equal(t, attachedMod.Self(), deps[0].Unwrap())
	require.Equal(t, attachedMod.Self(), current.Module.Self())
}

func TestNamespaceSourceMap(t *testing.T) {
	mod := &Module{NameField: "mymod"}

	t.Run("synthesizes module-name-only source map when SDK provides none", func(t *testing.T) {
		ctx := t.Context()

		cache, err := dagql.NewCache(ctx, "", nil, nil)
		require.NoError(t, err)
		ctx = dagql.ContextWithCache(ctx, cache)

		root := &Query{}
		testSrv := &moduleObjectTestServer{
			mockServer: &mockServer{},
			cache:      cache,
			root:       root,
		}
		root.Server = testSrv
		dag := newCoreDagqlServerForTest(t, root)
		testSrv.dag = dag

		dag.InstallObject(dagql.NewClass(dag, dagql.ClassOpts[*SourceMap]{Typed: &SourceMap{}}))
		dagql.Fields[*Query]{
			dagql.Func("sourceMap", func(_ context.Context, _ *Query, args struct {
				Module   dagql.Optional[dagql.String] `internal:"true"`
				Filename string
				Line     int
				Column   int
				URL      dagql.Optional[dagql.String] `internal:"true"`
			}) (*SourceMap, error) {
				var module string
				if args.Module.Valid {
					module = string(args.Module.Value)
				}
				var url string
				if args.URL.Valid {
					url = string(args.URL.Value)
				}
				return &SourceMap{
					Module:   module,
					Filename: args.Filename,
					Line:     args.Line,
					Column:   args.Column,
					URL:      url,
				}, nil
			}),
		}.Install(dag)

		ctx = ContextWithQuery(ctx, root)
		ctx = engine.ContextWithClientMetadata(ctx, &engine.ClientMetadata{
			ClientID:  "namespace-source-map-test-client",
			SessionID: "namespace-source-map-test-session",
		})

		result, err := mod.namespaceSourceMap(ctx, "sub", dagql.Null[dagql.ObjectResult[*SourceMap]]())
		require.NoError(t, err)
		require.True(t, result.Valid)
		require.NotNil(t, result.Value.Self())
		require.Equal(t, "mymod", result.Value.Self().Module)
	})
}
