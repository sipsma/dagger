package dagql_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/99designs/gqlgen/client"
	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/dagql/call"
	"github.com/dagger/dagger/dagql/internal/points"
	"github.com/dagger/dagger/engine"
)

type recipeReplanContextKey struct{}

func recipeReplanContext(clientID, sessionID string, dynamic int, cache *dagql.Cache) context.Context {
	ctx := engine.ContextWithClientMetadata(context.Background(), &engine.ClientMetadata{
		ClientID:  clientID,
		SessionID: sessionID,
	})
	ctx = context.WithValue(ctx, recipeReplanContextKey{}, dynamic)
	return dagql.ContextWithCache(ctx, cache)
}

func recipeReplanClient(srv *dagql.Server, cache *dagql.Cache, clientID, sessionID string, dynamic int) *client.Client {
	h := dagql.NewDefaultHandler(srv)
	return client.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := engine.ContextWithClientMetadata(r.Context(), &engine.ClientMetadata{
			ClientID:  clientID,
			SessionID: sessionID,
		})
		ctx = context.WithValue(ctx, recipeReplanContextKey{}, dynamic)
		ctx = dagql.ContextWithCache(ctx, cache)
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
}

func resultCallArg(frame *dagql.ResultCall, name string) *dagql.ResultCallArg {
	if frame == nil {
		return nil
	}
	for _, arg := range frame.ImplicitInputs {
		if arg != nil && arg.Name == name {
			return arg
		}
	}
	return nil
}

func resultCallString(frame *dagql.ResultCall, name string) string {
	arg := resultCallArg(frame, name)
	if arg == nil || arg.Value == nil {
		return ""
	}
	return arg.Value.StringValue
}

func resultCallInt(frame *dagql.ResultCall, name string) int64 {
	arg := resultCallArg(frame, name)
	if arg == nil || arg.Value == nil {
		return 0
	}
	return arg.Value.IntValue
}

func inputInt(input dagql.Input) int {
	switch value := input.(type) {
	case dagql.Int:
		return value.Int()
	case dagql.Optional[dagql.Int]:
		if value.Valid {
			return value.Value.Int()
		}
	case dagql.DynamicOptional:
		if value.Valid {
			if integer, ok := value.Value.(dagql.Int); ok {
				return integer.Int()
			}
		}
	}
	return 0
}

func TestNodeRecipeReplanIsExplicitAndRecomputesWholeChain(t *testing.T) {
	srv := newExternalDagqlServerForTest(t, Query{})
	cache := newCache(t)
	points.Install[Query](srv)

	var baseCalls, middleCalls, finalCalls atomic.Int64
	var dynamicCalls atomic.Int64
	var baseClients, middleSessions, finalPerCalls []string
	var finalExplicit, observedDynamic []int

	contextDynamic := dagql.ImplicitInput{
		Name: "contextDynamic",
		Resolver: func(_ context.Context, args map[string]dagql.Input) (dagql.Input, error) {
			value := inputInt(args["dynamic"])
			observedDynamic = append(observedDynamic, value)
			return dagql.NewInt(value), nil
		},
	}

	dagql.Fields[Query]{
		dagql.NodeFunc("replanBase", func(ctx context.Context, _ dagql.ObjectResult[Query], args struct {
			Seed int
		}) (*points.Point, error) {
			baseCalls.Add(1)
			baseClients = append(baseClients, resultCallString(dagql.CurrentCall(ctx), dagql.PerClientInput.Name))
			return &points.Point{X: args.Seed}, nil
		}).WithInput(dagql.PerClientInput),
	}.Install(srv)
	dagql.Fields[*points.Point]{
		dagql.NodeFunc("replanMiddle", func(ctx context.Context, self dagql.ObjectResult[*points.Point], args struct {
			Add int
		}) (*points.Point, error) {
			middleCalls.Add(1)
			middleSessions = append(middleSessions, resultCallString(dagql.CurrentCall(ctx), dagql.PerSessionInput.Name))
			return &points.Point{X: self.Self().X + args.Add}, nil
		}).WithInput(dagql.PerSessionInput),
		dagql.NodeFuncWithDynamicInputs(
			"replanFinal",
			func(ctx context.Context, self dagql.ObjectResult[*points.Point], args struct {
				Keep    int
				Dynamic int `default:"0"`
			}) (*points.Point, error) {
				finalCalls.Add(1)
				finalExplicit = append(finalExplicit, args.Keep)
				finalPerCalls = append(finalPerCalls, resultCallString(dagql.CurrentCall(ctx), dagql.PerCallInput.Name))
				require.Equal(t, int64(args.Dynamic), resultCallInt(dagql.CurrentCall(ctx), contextDynamic.Name))
				return &points.Point{X: self.Self().X + args.Keep + args.Dynamic}, nil
			},
			func(ctx context.Context, _ dagql.ObjectResult[*points.Point], _ struct {
				Keep    int
				Dynamic int `default:"0"`
			}, req *dagql.CallRequest) error {
				dynamicCalls.Add(1)
				dynamic, _ := ctx.Value(recipeReplanContextKey{}).(int)
				return req.SetArgInput(ctx, "dynamic", dagql.NewInt(dynamic), false)
			},
		).WithInput(dagql.PerCallInput, contextDynamic),
	}.Install(srv)

	ctxA := recipeReplanContext("client-a", "session-a", 10, cache)
	var original dagql.ObjectResult[*points.Point]
	require.NoError(t, srv.Select(ctxA, srv.Root(), &original,
		dagql.Selector{Field: "replanBase", Args: []dagql.NamedInput{{Name: "seed", Value: dagql.NewInt(5)}}},
		dagql.Selector{Field: "replanMiddle", Args: []dagql.NamedInput{{Name: "add", Value: dagql.NewInt(3)}}},
		dagql.Selector{Field: "replanFinal", Args: []dagql.NamedInput{{Name: "keep", Value: dagql.NewInt(7)}}},
	))
	require.Equal(t, 25, original.Self().X)
	savedID := mustRecipeID(t, ctxA, original)

	ctxB := recipeReplanContext("client-b", "session-b", 20, cache)
	assertOriginal := func(loaded dagql.AnyObjectResult) {
		t.Helper()
		var x int
		require.NoError(t, srv.Select(ctxB, loaded, &x, dagql.Selector{Field: "x"}))
		require.Equal(t, 25, x)
		require.Equal(t, int64(1), baseCalls.Load())
		require.Equal(t, int64(1), middleCalls.Load())
		require.Equal(t, int64(1), finalCalls.Load())
	}

	loaded, err := srv.Load(ctxB, savedID)
	require.NoError(t, err)
	assertOriginal(loaded)
	loadedType, err := srv.LoadType(ctxB, savedID)
	require.NoError(t, err)
	loaded, err = srv.ToSelectable(ctxB, loadedType)
	require.NoError(t, err)
	assertOriginal(loaded)

	encoded, err := savedID.Encode()
	require.NoError(t, err)
	gql := recipeReplanClient(srv, cache, "client-b", "session-b", 20)
	var defaultLoad struct {
		Loaded struct{ X int }
	}
	require.NoError(t, gql.Post(fmt.Sprintf(`query { loaded: node(id: %q) { ... on Point { x } } }`, encoded), &defaultLoad))
	require.Equal(t, 25, defaultLoad.Loaded.X)
	require.Equal(t, int64(1), finalCalls.Load(), "node's omitted option must preserve recorded loading")

	handleEncoded, err := mustID(t, original).Encode()
	require.NoError(t, err)
	handleGQL := recipeReplanClient(srv, cache, "client-b", "session-a", 20)
	var handleLoad struct {
		Loaded struct{ X int }
	}
	require.NoError(t, handleGQL.Post(fmt.Sprintf(`query { loaded: node(id: %q, recomputeImplicitInputs: true) { ... on Point { x } } }`, handleEncoded), &handleLoad))
	require.Equal(t, 25, handleLoad.Loaded.X)
	require.Equal(t, int64(1), finalCalls.Load(), "handle IDs must remain on their recorded load path")

	var replanned struct {
		Loaded struct{ X int }
	}
	require.NoError(t, gql.Post(fmt.Sprintf(`query { loaded: node(id: %q, recomputeImplicitInputs: true) { ... on Point { x } } }`, encoded), &replanned))
	require.Equal(t, 35, replanned.Loaded.X)
	require.Equal(t, int64(2), baseCalls.Load())
	require.Equal(t, int64(2), middleCalls.Load())
	require.Equal(t, int64(2), finalCalls.Load())
	require.Equal(t, int64(2), dynamicCalls.Load(), "dynamic inputs must rerun during replan")
	require.Equal(t, []int{7, 7}, finalExplicit, "recorded explicit arguments must be preserved")
	require.Equal(t, []string{"client-a", "client-b"}, baseClients)
	require.Equal(t, []string{"session-a", "session-b"}, middleSessions)
	require.Len(t, finalPerCalls, 2)
	require.NotEqual(t, finalPerCalls[0], finalPerCalls[1], "PerCall must be recomputed")
	require.Equal(t, []int{0, 10, 10, 20}, observedDynamic,
		"implicit inputs must be recomputed after the dynamic input rewrite")
}

func TestRecipeReplanDropsRecordedImplicitDependenciesEffectsAndExtraDigests(t *testing.T) {
	srv := newExternalDagqlServerForTest(t, Query{})
	cache := newCache(t)
	ctx := dagql.ContextWithCache(testContext(), cache)
	points.Install[Query](srv)

	var staleDependencyCalls atomic.Int64
	var targetCalls atomic.Int64
	var seenEffects []string
	var seenExtras []call.ExtraDigest
	var sawStaleImplicit bool
	dagql.Fields[Query]{
		dagql.NodeFunc("staleDependency", func(context.Context, dagql.ObjectResult[Query], struct{}) (*points.Point, error) {
			staleDependencyCalls.Add(1)
			return &points.Point{X: -1}, nil
		}),
		dagql.NodeFunc("trackedReplanTarget", func(ctx context.Context, _ dagql.ObjectResult[Query], args struct {
			Seed int
		}) (*points.Point, error) {
			targetCalls.Add(1)
			frame := dagql.CurrentCall(ctx)
			seenEffects = slices.Clone(frame.EffectIDs)
			seenExtras = slices.Clone(frame.ExtraDigests)
			sawStaleImplicit = resultCallArg(frame, "stale") != nil
			return &points.Point{X: args.Seed}, nil
		}).WithInput(dagql.PerCallInput),
	}.Install(srv)

	var decoy dagql.ObjectResult[*points.Point]
	require.NoError(t, srv.Select(ctx, srv.Root(), &decoy, dagql.Selector{
		Field: "point",
		Args: []dagql.NamedInput{
			{Name: "x", Value: dagql.NewInt(99)},
			{Name: "y", Value: dagql.NewInt(0)},
		},
	}))
	decoyID := mustRecipeID(t, ctx, decoy)
	staleDependency := call.New().Append((&points.Point{}).Type(), "staleDependency")
	targetID := call.New().Append(
		(&points.Point{}).Type(),
		"trackedReplanTarget",
		call.WithArgs(call.NewArgument("seed", call.NewLiteralInt(5), false)),
		call.WithImplicitInputs(call.NewArgument("stale", call.NewLiteralID(staleDependency), false)),
		call.WithEffectIDs([]string{"stale-effect"}),
		call.WithContentDigest(decoyID.Digest()),
	)

	loaded, err := srv.LoadWithRecomputedImplicitInputs(ctx, targetID)
	require.NoError(t, err)
	var x int
	require.NoError(t, srv.Select(ctx, loaded, &x, dagql.Selector{Field: "x"}))
	require.Equal(t, 5, x, "recorded content equivalence must not return the decoy")
	require.Equal(t, int64(1), targetCalls.Load())
	require.Equal(t, int64(0), staleDependencyCalls.Load(), "recorded implicit-only dependencies must not load")
	require.Empty(t, seenEffects, "stale effect IDs must not enter the fresh call")
	require.Empty(t, seenExtras, "stale extra digests must not enter the fresh call")
	require.False(t, sawStaleImplicit, "recorded implicit inputs must not enter the fresh call")
}

func TestRecipeReplanUsesNormalCacheForFreshIdentity(t *testing.T) {
	srv := newExternalDagqlServerForTest(t, Query{})
	cache := newCache(t)
	points.Install[Query](srv)

	var calls atomic.Int64
	currentContext := dagql.ImplicitInput{
		Name: "currentContext",
		Resolver: func(ctx context.Context, _ map[string]dagql.Input) (dagql.Input, error) {
			value, _ := ctx.Value(recipeReplanContextKey{}).(int)
			return dagql.NewInt(value), nil
		},
	}
	dagql.Fields[Query]{
		dagql.NodeFunc("freshlyCachedReplan", func(ctx context.Context, _ dagql.ObjectResult[Query], _ struct{}) (*points.Point, error) {
			calls.Add(1)
			return &points.Point{X: int(resultCallInt(dagql.CurrentCall(ctx), currentContext.Name))}, nil
		}).WithInput(currentContext),
	}.Install(srv)

	ctxA := recipeReplanContext("client-a", "session-a", 10, cache)
	var original dagql.ObjectResult[*points.Point]
	require.NoError(t, srv.Select(ctxA, srv.Root(), &original, dagql.Selector{Field: "freshlyCachedReplan"}))
	require.Equal(t, 10, original.Self().X)
	savedID := mustRecipeID(t, ctxA, original)

	ctxB := recipeReplanContext("client-b", "session-b", 20, cache)
	for range 2 {
		loaded, err := srv.LoadWithRecomputedImplicitInputs(ctxB, savedID)
		require.NoError(t, err)
		var x int
		require.NoError(t, srv.Select(ctxB, loaded, &x, dagql.Selector{Field: "x"}))
		require.Equal(t, 20, x)
	}
	require.Equal(t, int64(2), calls.Load(), "the second replan must hit the normal cache for its fresh identity")
}

func TestRecipeReplanListMemoizationAndNth(t *testing.T) {
	srv := newExternalDagqlServerForTest(t, Query{})
	cache := newCache(t)
	ctx := dagql.ContextWithCache(testContext(), cache)
	points.Install[Query](srv)

	var leafCalls, listCalls atomic.Int64
	dagql.Fields[Query]{
		dagql.NodeFunc("replanLeaf", func(context.Context, dagql.ObjectResult[Query], struct{}) (*points.Point, error) {
			return &points.Point{X: int(leafCalls.Add(1))}, nil
		}).WithInput(dagql.PerCallInput),
		dagql.NodeFunc("collectReplanIDs", func(_ context.Context, _ dagql.ObjectResult[Query], args struct {
			Objects dagql.ArrayInput[dagql.AnyID]
		}) (*points.Point, error) {
			return &points.Point{X: len(args.Objects)}, nil
		}),
		dagql.NodeFunc("replanList", func(context.Context, dagql.ObjectResult[Query], struct{}) (dagql.Array[*points.Point], error) {
			value := int(listCalls.Add(1))
			return dagql.Array[*points.Point]{{X: value * 100}, {X: value*100 + 2}}, nil
		}).WithInput(dagql.PerCallInput),
	}.Install(srv)

	leafID := call.New().Append((&points.Point{}).Type(), "replanLeaf")
	listRecipe := call.New().Append(
		(&points.Point{}).Type(),
		"collectReplanIDs",
		call.WithArgs(call.NewArgument("objects", call.NewLiteralList(
			call.NewLiteralID(leafID),
			call.NewLiteralID(leafID),
		), false)),
	)
	collected, err := srv.LoadWithRecomputedImplicitInputs(ctx, listRecipe)
	require.NoError(t, err)
	var count int
	require.NoError(t, srv.Select(ctx, collected, &count, dagql.Selector{Field: "x"}))
	require.Equal(t, 2, count)
	require.Equal(t, int64(1), leafCalls.Load(), "a repeated DAG vertex must replan once per load")

	var originalNth dagql.ObjectResult[*points.Point]
	require.NoError(t, srv.Select(ctx, srv.Root(), &originalNth, dagql.Selector{Field: "replanList", Nth: 2}))
	require.Equal(t, 102, originalNth.Self().X)
	nthID := mustRecipeID(t, ctx, originalNth)
	replannedNth, err := srv.LoadWithRecomputedImplicitInputs(ctx, nthID)
	require.NoError(t, err)
	var nthX int
	require.NoError(t, srv.Select(ctx, replannedNth, &nthX, dagql.Selector{Field: "x"}))
	require.Equal(t, 202, nthX)
	require.Equal(t, int64(2), listCalls.Load(), "the nth parent list must be replanned")
}
