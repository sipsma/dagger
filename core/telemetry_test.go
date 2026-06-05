package core

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/dagger/dagger/auth"
	workspacepkg "github.com/dagger/dagger/core/workspace"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine"
	engineclient "github.com/dagger/dagger/engine/client"
	"github.com/dagger/dagger/engine/clientdb"
	"github.com/dagger/dagger/engine/engineutil"
	serverresolver "github.com/dagger/dagger/engine/server/resolver"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/internal/buildkit/executor/oci"
	telemetry "github.com/dagger/otel-go"
	"github.com/moby/locker"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
)

func testResultCall(field string, typ dagql.Typed, receiver *dagql.ResultCall) *dagql.ResultCall {
	var ref *dagql.ResultCallRef
	if receiver != nil {
		ref = &dagql.ResultCallRef{Call: receiver}
	}
	return &dagql.ResultCall{
		Kind:     dagql.ResultCallKindField,
		Field:    field,
		Type:     dagql.NewResultCallType(typ.Type()),
		Receiver: ref,
	}
}

type mockServer struct {
	moduleSource   *ModuleSource
	functionCall   *FunctionCall
	env            dagql.ObjectResult[*Env]
	clientMetadata *engine.ClientMetadata
	attachables    map[string]*grpc.ClientConn
	locker         *locker.Locker
}

func (ms *mockServer) ServeHTTPToNestedClient(http.ResponseWriter, *http.Request, *engine.ClientMetadata, string, bool, dagql.AnyObjectResult, dagql.Typed, dagql.AnyObjectResult) {
}

func (ms *mockServer) ServeModule(ctx context.Context, mod dagql.ObjectResult[*Module], includeDependencies bool, entrypoint bool) error {
	return nil
}

func (ms *mockServer) CurrentModule(_ context.Context) (dagql.ObjectResult[*Module], error) {
	var zero dagql.ObjectResult[*Module]
	if ms.moduleSource == nil {
		return zero, nil
	}
	// This helper only builds test-only module results. Keep using
	// context.Background here: passing the caller ctx would not change
	// behavior, because dagql.NewServer ignores its context today and this
	// path does not use the dagql cache.
	dag, err := dagql.NewServer(context.Background(), &Query{})
	if err != nil {
		panic(err)
	}
	dag.InstallObject(dagql.NewClass(dag, dagql.ClassOpts[*ModuleSource]{Typed: &ModuleSource{}}))
	dag.InstallObject(dagql.NewClass(dag, dagql.ClassOpts[*Module]{Typed: &Module{}}))

	sourceRes, err := dagql.NewObjectResultForCall(ms.moduleSource, dag, &dagql.ResultCall{
		Kind:        dagql.ResultCallKindSynthetic,
		SyntheticOp: "mock_module_source",
		Type:        dagql.NewResultCallType(ms.moduleSource.Type()),
	})
	if err != nil {
		panic(err)
	}

	dn := dagql.Nullable[dagql.ObjectResult[*ModuleSource]]{
		Valid: true,
		Value: sourceRes,
	}
	return dagql.NewObjectResultForCall(&Module{
		Source: dn,
	}, dag, &dagql.ResultCall{
		Kind:        dagql.ResultCallKindSynthetic,
		SyntheticOp: "mock_current_module",
		Type:        dagql.NewResultCallType((&Module{}).Type()),
	})
}

func (ms *mockServer) ModuleParent(context.Context) (dagql.ObjectResult[*Module], error) {
	return dagql.ObjectResult[*Module]{}, nil
}

func (ms *mockServer) CurrentFunctionCall(context.Context) (*FunctionCall, error) {
	return ms.functionCall, nil
}

func (ms *mockServer) CurrentEnv(context.Context) (dagql.ObjectResult[*Env], error) {
	return ms.env, nil
}

func (ms *mockServer) CurrentServedDeps(context.Context) (*SchemaBuilder, error) {
	return NewSchemaBuilder(nil, nil), nil
}

func (ms *mockServer) MainClientCallerMetadata(context.Context) (*engine.ClientMetadata, error) {
	if ms.clientMetadata != nil {
		return ms.clientMetadata, nil
	}
	return &engine.ClientMetadata{}, nil
}

func (ms *mockServer) SpecificClientMetadata(context.Context, string) (*engine.ClientMetadata, error) {
	return nil, nil
}

func (ms *mockServer) CurrentWorkspace(context.Context) (*Workspace, error) {
	return nil, nil
}

func (ms *mockServer) SpecificClientAttachableConn(_ context.Context, clientID string, opts SpecificClientAttachableConnOpts) (*grpc.ClientConn, bool, error) {
	conn := ms.attachables[clientID]
	if conn == nil && !opts.IfAvailable {
		return nil, false, nil
	}
	return conn, conn != nil, nil
}

func (ms *mockServer) CurrentWorkspaceLock(context.Context) (*workspacepkg.Lock, bool, error) {
	return nil, false, nil
}

func (ms *mockServer) SetCurrentWorkspaceLookup(context.Context, string, string, []any, workspacepkg.LookupResult) error {
	return nil
}

func (ms *mockServer) NonModuleParentClientMetadata(context.Context) (*engine.ClientMetadata, error) {
	return nil, nil
}
func (ms *mockServer) DefaultDeps(context.Context) (*SchemaBuilder, error) { return nil, nil }
func (ms *mockServer) Cache(context.Context) (*dagql.Cache, error)         { return nil, nil }
func (ms *mockServer) TelemetrySeenKeyStore(context.Context) (dagql.TelemetrySeenKeyStore, error) {
	return nil, nil
}
func (ms *mockServer) Server(context.Context) (*dagql.Server, error)           { return nil, nil }
func (ms *mockServer) MuxEndpoint(context.Context, string, http.Handler) error { return nil }

func (ms *mockServer) Auth(context.Context) (*auth.RegistryAuthProvider, error) { return nil, nil }

func (ms *mockServer) Engine(context.Context) (*engineutil.Client, error) { return nil, nil }

func (ms *mockServer) RegistryResolver(context.Context) (*serverresolver.Resolver, error) {
	return nil, nil
}

func (ms *mockServer) Services(context.Context) (*Services, error) { return nil, nil }

func (ms *mockServer) Platform() Platform                  { return Platform{} }
func (ms *mockServer) OCIStore() content.Store             { return nil }
func (ms *mockServer) BuiltinOCIStore() content.Store      { return nil }
func (ms *mockServer) DNS() *oci.DNSConfig                 { return nil }
func (ms *mockServer) LeaseManager() *bkcache.LeaseManager { return nil }
func (ms *mockServer) EngineLocalCacheEntries(context.Context) (*EngineCacheEntrySet, error) {
	return nil, nil
}

func (ms *mockServer) PruneEngineLocalCacheEntries(context.Context, EngineCachePruneOptions) (*EngineCacheEntrySet, error) {
	return nil, nil
}
func (ms *mockServer) EngineLocalCachePolicy() *dagql.CachePrunePolicy { return nil }
func (ms *mockServer) SnapshotManager() bkcache.SnapshotManager        { return nil }
func (ms *mockServer) Locker() *locker.Locker                          { return ms.locker }
func (ms *mockServer) SecretSalt() []byte                              { return nil }
func (ms *mockServer) FlushSessionTelemetry(context.Context) error     { return nil }
func (ms *mockServer) ClientTelemetry(ctc context.Context, sessID, clientID string) (*clientdb.DB, error) {
	return nil, nil
}
func (ms *mockServer) EngineName() string { return "mockEngine" }
func (ms *mockServer) Clients() []string  { return []string{} }

func (ms *mockServer) CloudEngineClient(context.Context, string, string, []string) (*engineclient.Client, bool, error) {
	return nil, false, nil
}

func (ms *mockServer) CleanMountNS() *os.File { return nil }

func TestParseCallerCalleeRefs(t *testing.T) {
	call := &dagql.ResultCall{
		Kind:  dagql.ResultCallKindField,
		Field: "VersionedGitSSH.hello",
		Type:  dagql.NewResultCallType((&Void{}).Type()),
		Module: &dagql.ResultCallModule{
			Name: "versioned_git_ssh",
			Ref:  "git@github.com:dagger/dagger-test-modules/versioned@main",
			Pin:  "0cabe03cc0a9079e738c92b2c589d81fd560011f",
		},
	}

	// Set up mock server with Git source for the caller
	mockSrv := &mockServer{
		moduleSource: &ModuleSource{
			Kind: ModuleSourceKindGit,
			Git: &GitModuleSource{
				CloneRef: "git@github.com:dagger/dagger-test-modules/caller",
				Version:  "v1.0.0",
			},
		},
		functionCall: &FunctionCall{
			Name: "callerFunction",
		},
	}

	callerRef, calleeRef := parseCallerCalleeRefs(t.Context(), &Query{Server: mockSrv}, call)

	require.NotNil(t, callerRef)
	require.Equal(t, "github.com/dagger/dagger-test-modules/caller", callerRef.ref)
	require.Equal(t, "v1.0.0", callerRef.version)
	require.Equal(t, "callerFunction", callerRef.functionName)

	require.NotNil(t, calleeRef)
	require.Equal(t, "github.com/dagger/dagger-test-modules/versioned", calleeRef.ref)
	require.Equal(t, "0cabe03cc0a9079e738c92b2c589d81fd560011f", calleeRef.version)
	require.Equal(t, "VersionedGitSSH.hello", calleeRef.functionName)
}

func TestAroundFuncMarksIntrospectionRootAsSkipped(t *testing.T) {
	req := &dagql.CallRequest{
		ResultCall: testResultCall("currentTypeDefs", dagql.String(""), nil),
	}

	ctx, _ := AroundFunc(t.Context(), req)
	require.True(t, dagql.IsSkipped(ctx))
}

func TestAroundFuncSkipsIntrospectionDescendantsViaContext(t *testing.T) {
	rootReq := &dagql.CallRequest{
		ResultCall: testResultCall("currentTypeDefs", dagql.String(""), nil),
	}
	rootCtx, _ := AroundFunc(t.Context(), rootReq)
	require.True(t, dagql.IsSkipped(rootCtx))

	childReq := &dagql.CallRequest{
		ResultCall: testResultCall(
			"name",
			dagql.String(""),
			rootReq.ResultCall,
		),
	}
	childCtx, _ := AroundFunc(rootCtx, childReq)
	require.True(t, dagql.IsSkipped(childCtx))
}

type dynamicInputTelemetryRoot struct{}

func (dynamicInputTelemetryRoot) Type() *ast.Type {
	return &ast.Type{
		NamedType: "Query",
		NonNull:   true,
	}
}

func TestDynamicInputTelemetrySpan(t *testing.T) {
	cache, err := dagql.NewCache(t.Context(), "", nil, nil)
	require.NoError(t, err)

	srv, err := dagql.NewServer(t.Context(), dynamicInputTelemetryRoot{})
	require.NoError(t, err)
	srv.Around(AroundFunc)

	dagql.Fields[dynamicInputTelemetryRoot]{
		dagql.NodeFuncWithDynamicInputs(
			"rewrittenDynamicTelemetry",
			func(_ context.Context, _ dagql.ObjectResult[dynamicInputTelemetryRoot], args struct{ Val int }) (dagql.Int, error) {
				return dagql.Int(args.Val), nil
			},
			func(ctx context.Context, _ dagql.ObjectResult[dynamicInputTelemetryRoot], _ struct{ Val int }, req *dagql.CallRequest) error {
				return req.SetArgInput(ctx, "val", dagql.Int(7), false)
			},
		),
	}.Install(srv)

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	defer func() {
		require.NoError(t, tracerProvider.Shutdown(t.Context()))
	}()

	ctx := engine.ContextWithClientMetadata(context.Background(), &engine.ClientMetadata{
		ClientID:  "dagql-test-client",
		SessionID: "dagql-test-session",
	})
	ctx = dagql.ContextWithCache(ctx, cache)
	ctx, rootSpan := tracerProvider.Tracer("dagger.io/test").Start(ctx, "root")

	var result dagql.Int
	err = srv.Select(ctx, srv.Root(), &result, dagql.Selector{
		Field: "rewrittenDynamicTelemetry",
		Args: []dagql.NamedInput{{
			Name:  "val",
			Value: dagql.Int(1),
		}},
	})
	rootSpan.End()
	require.NoError(t, err)
	require.Equal(t, dagql.Int(7), result)

	spans := spanRecorder.Ended()
	dynamicSpan := requireSpanNamed(t, spans, "resolve dynamic inputs")
	callSpan := requireSpanNamed(t, spans, "Query.rewrittenDynamicTelemetry")

	require.Equal(t, "dynamic", requireSpanStringAttr(t, dynamicSpan, dagInputPhaseAttr))
	require.Equal(t, "rewrittenDynamicTelemetry", requireSpanStringAttr(t, dynamicSpan, dagInputTargetFieldAttr))
	require.Equal(t, true, requireSpanBoolAttr(t, dynamicSpan, telemetry.UIInternalAttr))
	require.Equal(t, []string{"val"}, requireSpanStringSliceAttr(t, dynamicSpan, dagInputChangedArgsAttr))

	targetDigest := requireSpanStringAttr(t, dynamicSpan, dagInputTargetDigestAttr)
	require.NotEmpty(t, targetDigest)
	require.Equal(t, requireSpanStringAttr(t, callSpan, telemetry.DagDigestAttr), targetDigest)

	require.NotEqual(t, dynamicSpan.SpanContext().SpanID(), callSpan.Parent().SpanID())
}

type idLoadTelemetryRoot struct{}

func (idLoadTelemetryRoot) Type() *ast.Type {
	return &ast.Type{
		NamedType: "Query",
		NonNull:   true,
	}
}

type idLoadTelemetrySource struct {
	Name dagql.String `field:"true"`
}

func (*idLoadTelemetrySource) Type() *ast.Type {
	return &ast.Type{
		NamedType: "IDLoadTelemetrySource",
		NonNull:   true,
	}
}

func TestIDLoadTelemetrySpan(t *testing.T) {
	cache, err := dagql.NewCache(t.Context(), "", nil, nil)
	require.NoError(t, err)

	srv, err := dagql.NewServer(t.Context(), idLoadTelemetryRoot{})
	require.NoError(t, err)
	srv.Around(AroundFunc)

	dagql.Fields[*idLoadTelemetrySource]{}.Install(srv)
	dagql.Fields[idLoadTelemetryRoot]{
		dagql.Func("sourceForIDLoadTelemetry", func(context.Context, idLoadTelemetryRoot, struct{}) (*idLoadTelemetrySource, error) {
			return &idLoadTelemetrySource{Name: dagql.String("source")}, nil
		}),
		dagql.Func("useIDLoadTelemetrySource", func(ctx context.Context, _ idLoadTelemetryRoot, args struct {
			Source dagql.ID[*idLoadTelemetrySource]
		}) (dagql.String, error) {
			source, err := args.Source.Load(ctx, srv)
			if err != nil {
				return "", err
			}
			return source.Self().Name, nil
		}),
	}.Install(srv)

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	defer func() {
		require.NoError(t, tracerProvider.Shutdown(t.Context()))
	}()

	ctx := engine.ContextWithClientMetadata(context.Background(), &engine.ClientMetadata{
		ClientID:  "dagql-test-client",
		SessionID: "dagql-test-session",
	})
	ctx = dagql.ContextWithCache(ctx, cache)
	ctx, rootSpan := tracerProvider.Tracer("dagger.io/test").Start(ctx, "root")

	var source dagql.ObjectResult[*idLoadTelemetrySource]
	err = srv.Select(ctx, srv.Root(), &source, dagql.Selector{
		Field: "sourceForIDLoadTelemetry",
	})
	require.NoError(t, err)
	sourceID, err := source.ID()
	require.NoError(t, err)

	// A direct load under a non-call span should not emit resolver-internal
	// input telemetry.
	_, err = dagql.NewID[*idLoadTelemetrySource](sourceID).Load(ctx, srv)
	require.NoError(t, err)

	var loadedName dagql.String
	err = srv.Select(ctx, srv.Root(), &loadedName, dagql.Selector{
		Field: "useIDLoadTelemetrySource",
		Args: []dagql.NamedInput{{
			Name:  "source",
			Value: dagql.NewID[*idLoadTelemetrySource](sourceID),
		}},
	})
	rootSpan.End()
	require.NoError(t, err)
	require.Equal(t, dagql.String("source"), loadedName)

	spans := spanRecorder.Ended()
	loadSpans := spansNamed(spans, "load input ID")
	require.Len(t, loadSpans, 1)
	loadSpan := loadSpans[0]
	sourceSpan := requireSpanNamed(t, spans, "Query.sourceForIDLoadTelemetry")
	useSpan := requireSpanNamed(t, spans, "Query.useIDLoadTelemetrySource")

	require.Equal(t, useSpan.SpanContext().SpanID(), loadSpan.Parent().SpanID())
	require.Equal(t, "id_load", requireSpanStringAttr(t, loadSpan, dagql.DagInputPhaseAttr))
	require.Equal(t, "handle", requireSpanStringAttr(t, loadSpan, dagql.DagInputIDModeAttr))
	require.Equal(t, "IDLoadTelemetrySource", requireSpanStringAttr(t, loadSpan, dagql.DagInputIDTypeAttr))
	require.Equal(t, "useIDLoadTelemetrySource", requireSpanStringAttr(t, loadSpan, dagql.DagInputTargetFieldAttr))
	require.Equal(t, true, requireSpanBoolAttr(t, loadSpan, telemetry.UIInternalAttr))
	require.Equal(t, requireSpanStringAttr(t, useSpan, telemetry.DagDigestAttr), requireSpanStringAttr(t, loadSpan, dagql.DagInputTargetDigestAttr))
	require.Equal(t, requireSpanStringAttr(t, sourceSpan, telemetry.DagDigestAttr), requireSpanStringAttr(t, loadSpan, dagql.DagInputDigestAttr))
}

func TestLockMountedCachesRecordsResourceTelemetry(t *testing.T) {
	engineLocker := locker.New()
	query := &Query{Server: &mockServer{locker: engineLocker}}
	srv := newCoreDagqlServerForTest(t, query)
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*CacheVolume]{}))

	lockedCache := NewCache("locked-cache", "ns", dagql.Null[dagql.ObjectResult[*Directory]](), CacheSharingModeLocked, "")
	privateCache := NewCache("private-cache", "ns", dagql.Null[dagql.ObjectResult[*Directory]](), CacheSharingModePrivate, "")
	sharedCache := NewCache("shared-cache", "ns", dagql.Null[dagql.ObjectResult[*Directory]](), CacheSharingModeShared, "")

	blockedKey, err := lockedCache.lockKey()
	require.NoError(t, err)
	engineLocker.Lock(blockedKey)

	mounts := []ContainerMount{
		{
			Readonly:    true,
			CacheSource: &CacheMountSource{Volume: cacheVolumeTelemetryResult(t, srv, "readonlyLockedCache", lockedCache)},
		},
		{
			CacheSource: &CacheMountSource{Volume: cacheVolumeTelemetryResult(t, srv, "lockedCache", lockedCache)},
		},
		{
			CacheSource: &CacheMountSource{Volume: cacheVolumeTelemetryResult(t, srv, "duplicateLockedCache", lockedCache)},
		},
		{
			CacheSource: &CacheMountSource{Volume: cacheVolumeTelemetryResult(t, srv, "privateCache", privateCache)},
		},
		{
			CacheSource: &CacheMountSource{Volume: cacheVolumeTelemetryResult(t, srv, "sharedCache", sharedCache)},
		},
	}

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	defer func() {
		require.NoError(t, tracerProvider.Shutdown(t.Context()))
	}()

	ctx := ContextWithQuery(context.Background(), query)
	ctx, rootSpan := tracerProvider.Tracer("dagger.io/test").Start(ctx, "root")

	releaseCh := make(chan func(), 1)
	errCh := make(chan error, 1)
	go func() {
		release, err := lockMountedCaches(ctx, mounts)
		if err != nil {
			errCh <- err
			return
		}
		releaseCh <- release
	}()

	select {
	case release := <-releaseCh:
		release()
		require.Fail(t, "lockMountedCaches acquired a cache lock that should have been blocked")
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(20 * time.Millisecond):
	}

	require.NoError(t, engineLocker.Unlock(blockedKey))

	var release func()
	select {
	case release = <-releaseCh:
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		require.Fail(t, "timed out waiting for lockMountedCaches")
	}
	release()
	rootSpan.End()

	spans := spanRecorder.Ended()
	waitSpan := requireSpanNamed(t, spans, "wait cache volume locks")
	holdSpan := requireSpanNamed(t, spans, "hold cache volume locks")

	require.Equal(t, rootSpan.SpanContext().SpanID(), waitSpan.Parent().SpanID())
	require.Equal(t, rootSpan.SpanContext().SpanID(), holdSpan.Parent().SpanID())

	require.Equal(t, "cache_volume_lock", requireSpanStringAttr(t, waitSpan, dagResourceKindAttr))
	require.Equal(t, "acquire", requireSpanStringAttr(t, waitSpan, dagResourcePhaseAttr))
	require.Equal(t, int64(2), requireSpanIntAttr(t, waitSpan, dagResourceCountAttr))
	require.Equal(t, true, requireSpanBoolAttr(t, waitSpan, dagResourceExclusiveAttr))
	require.Equal(t, true, requireSpanBoolAttr(t, waitSpan, telemetry.UIInternalAttr))

	keyHashes := requireSpanStringSliceAttr(t, waitSpan, dagResourceLockKeyHashesAttr)
	require.Len(t, keyHashes, 2)
	require.NotContains(t, strings.Join(keyHashes, " "), "locked-cache")
	require.NotContains(t, strings.Join(keyHashes, " "), "private-cache")
	require.Equal(t, []string{"LOCKED", "PRIVATE"}, requireSpanStringSliceAttr(t, waitSpan, dagResourceLockSharingModesAttr))

	require.Equal(t, "cache_volume_lock", requireSpanStringAttr(t, holdSpan, dagResourceKindAttr))
	require.Equal(t, "hold", requireSpanStringAttr(t, holdSpan, dagResourcePhaseAttr))
	require.Equal(t, int64(2), requireSpanIntAttr(t, holdSpan, dagResourceCountAttr))
	require.Equal(t, keyHashes, requireSpanStringSliceAttr(t, holdSpan, dagResourceLockKeyHashesAttr))
	require.Equal(t, []string{"LOCKED", "PRIVATE"}, requireSpanStringSliceAttr(t, holdSpan, dagResourceLockSharingModesAttr))
}

func TestLockMountedCachesSkipsResourceTelemetryWithoutLockedCaches(t *testing.T) {
	engineLocker := locker.New()
	query := &Query{Server: &mockServer{locker: engineLocker}}
	srv := newCoreDagqlServerForTest(t, query)
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*CacheVolume]{}))

	lockedCache := NewCache("locked-cache", "ns", dagql.Null[dagql.ObjectResult[*Directory]](), CacheSharingModeLocked, "")
	sharedCache := NewCache("shared-cache", "ns", dagql.Null[dagql.ObjectResult[*Directory]](), CacheSharingModeShared, "")
	mounts := []ContainerMount{
		{
			Readonly:    true,
			CacheSource: &CacheMountSource{Volume: cacheVolumeTelemetryResult(t, srv, "readonlyLockedCache", lockedCache)},
		},
		{
			CacheSource: &CacheMountSource{Volume: cacheVolumeTelemetryResult(t, srv, "sharedCache", sharedCache)},
		},
	}

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	defer func() {
		require.NoError(t, tracerProvider.Shutdown(t.Context()))
	}()

	ctx := ContextWithQuery(context.Background(), query)
	ctx, rootSpan := tracerProvider.Tracer("dagger.io/test").Start(ctx, "root")
	release, err := lockMountedCaches(ctx, mounts)
	require.NoError(t, err)
	release()
	rootSpan.End()

	require.Empty(t, spansNamed(spanRecorder.Ended(), "wait cache volume locks"))
	require.Empty(t, spansNamed(spanRecorder.Ended(), "hold cache volume locks"))
}

func cacheVolumeTelemetryResult(t *testing.T, srv *dagql.Server, op string, cache *CacheVolume) dagql.ObjectResult[*CacheVolume] {
	t.Helper()
	res, err := dagql.NewObjectResultForCall(cache, srv, &dagql.ResultCall{
		Kind:        dagql.ResultCallKindSynthetic,
		SyntheticOp: op,
		Type:        dagql.NewResultCallType((&CacheVolume{}).Type()),
	})
	require.NoError(t, err)
	return res
}

func spansNamed(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	var matched []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == name {
			matched = append(matched, span)
		}
	}
	return matched
}

func requireSpanNamed(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	require.Failf(t, "span not found", "missing span %q", name)
	return nil
}

func requireSpanStringAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) string {
	t.Helper()
	val := requireSpanAttr(t, span, key)
	return val.AsString()
}

func requireSpanBoolAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) bool {
	t.Helper()
	val := requireSpanAttr(t, span, key)
	return val.AsBool()
}

func requireSpanIntAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) int64 {
	t.Helper()
	val := requireSpanAttr(t, span, key)
	return val.AsInt64()
}

func requireSpanStringSliceAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) []string {
	t.Helper()
	val := requireSpanAttr(t, span, key)
	return val.AsStringSlice()
}

func requireSpanAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) attribute.Value {
	t.Helper()
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key {
			return attr.Value
		}
	}
	require.Failf(t, "span attribute not found", "missing attr %q on span %q", key, span.Name())
	return attribute.Value{}
}

func TestIsIntrospectionPreservesClassification(t *testing.T) {
	cache, err := dagql.NewCache(t.Context(), "", nil, nil)
	require.NoError(t, err)
	ctx := dagql.ContextWithCache(t.Context(), cache)

	functionFrame := testResultCall("function", dagql.String(""), nil)
	functionFrame.Type = dagql.NewResultCallType((&Function{}).Type())

	tests := []struct {
		name  string
		frame *dagql.ResultCall
		want  bool
	}{
		{
			name:  "root currentTypeDefs",
			frame: testResultCall("currentTypeDefs", dagql.String(""), nil),
			want:  true,
		},
		{
			name:  "root plain field",
			frame: testResultCall("plain", dagql.String(""), nil),
			want:  false,
		},
		{
			name:  "function builder field",
			frame: testResultCall("withArg", dagql.String(""), functionFrame),
			want:  true,
		},
		{
			name:  "descendant of introspection root",
			frame: testResultCall("name", dagql.String(""), testResultCall("currentTypeDefs", dagql.String(""), nil)),
			want:  true,
		},
		{
			name:  "descendant of plain root",
			frame: testResultCall("name", dagql.String(""), testResultCall("plain", dagql.String(""), nil)),
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isIntrospection(ctx, tc.frame))
		})
	}
}

type telemetryTestNoopResolver struct{}

func (telemetryTestNoopResolver) ObjectType(string) (dagql.ObjectType, bool) { return nil, false }
func (telemetryTestNoopResolver) ScalarType(string) (dagql.ScalarType, bool) { return nil, false }

type telemetryTestSpan struct {
	trace.Span
	attrs []attribute.KeyValue
}

func (s *telemetryTestSpan) End(...trace.SpanEndOption)              {}
func (s *telemetryTestSpan) AddEvent(string, ...trace.EventOption)   {}
func (s *telemetryTestSpan) AddLink(trace.Link)                      {}
func (s *telemetryTestSpan) IsRecording() bool                       { return true }
func (s *telemetryTestSpan) RecordError(error, ...trace.EventOption) {}
func (s *telemetryTestSpan) SpanContext() trace.SpanContext          { return trace.SpanContext{} }
func (s *telemetryTestSpan) SetStatus(codes.Code, string)            {}
func (s *telemetryTestSpan) SetName(string)                          {}
func (s *telemetryTestSpan) SetAttributes(attrs ...attribute.KeyValue) {
	s.attrs = append(s.attrs, attrs...)
}
func (s *telemetryTestSpan) TracerProvider() trace.TracerProvider {
	return noop.NewTracerProvider()
}

type telemetryTestLazyString struct {
	dagql.String
}

func (telemetryTestLazyString) Type() *ast.Type {
	return dagql.String("").Type()
}

func (telemetryTestLazyString) LazyEvalFunc() dagql.LazyEvalFunc {
	return func(context.Context) error { return nil }
}

func TestRecordStatusDoesNotMarkPendingLazyResultCached(t *testing.T) {
	ctx := t.Context()
	cacheIface, err := dagql.NewCache(ctx, "", nil, nil)
	require.NoError(t, err)
	ctx = dagql.ContextWithCache(ctx, cacheIface)

	reqCall := &dagql.ResultCall{
		Kind:  dagql.ResultCallKindField,
		Field: "withExec",
		Type:  dagql.NewResultCallType(dagql.String("").Type()),
	}
	req := &dagql.CallRequest{ResultCall: reqCall}

	res, err := cacheIface.GetOrInitCall(ctx, "test-session", telemetryTestNoopResolver{}, req, func(ctx context.Context) (dagql.AnyResult, error) {
		return dagql.NewResultForCurrentCall(ctx, telemetryTestLazyString{String: dagql.String("lazy")})
	})
	require.NoError(t, err)
	require.True(t, dagql.HasPendingLazyEvaluation(res))

	span := &telemetryTestSpan{}
	recordStatus(ctx, res, span, true, reqCall)

	for _, attr := range span.attrs {
		require.NotEqual(t, telemetry.CachedAttr, string(attr.Key))
	}
}
