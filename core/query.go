package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/containerd/containerd/content"
	bkcache "github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	bkgw "github.com/moby/buildkit/frontend/gateway/client"
	bksolver "github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver/provenance"
	bksolverpb "github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	fsutiltypes "github.com/tonistiigi/fsutil/types"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/dagger/dagger/auth"
	"github.com/dagger/dagger/core/pipeline"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/dagql/call"
	dagsession "github.com/dagger/dagger/engine/session"
)

// Query forms the root of the DAG and houses all necessary state and
// dependencies for evaluating queries.
type Query struct {
	Engine
	// The current pipeline.
	Pipeline pipeline.Path
}

var ErrNoCurrentModule = fmt.Errorf("no current module")

// TODO: doc
// TODO: doc
// TODO: doc
// TODO: maybe this could actually go in the engine package? that's importable by everything
// TODO: maybe this could actually go in the engine package? that's importable by everything
// TODO: maybe this could actually go in the engine package? that's importable by everything
type Engine interface {
	MainClientCallerID(ctx context.Context) string

	RegisterCaller(ctx context.Context, call *FunctionCall) (string, error)
	MuxEndpoint(ctx context.Context, path string, handler http.Handler) error
	ServeModule(ctx context.Context, mod *Module) error
	CurrentModule(ctx context.Context) (*Module, error)
	CurrentFunctionCall(ctx context.Context) (*FunctionCall, error)
	CurrentServedDeps(ctx context.Context) (*ModDeps, error)
	DefaultDeps(ctx context.Context) *ModDeps

	Services(ctx context.Context) *Services
	Secrets(ctx context.Context) *SecretStore
	AuthProvider(ctx context.Context) *auth.RegistryAuthProvider
	OCIStore(ctx context.Context) content.Store
	OCIStoreName(ctx context.Context) string
	BuiltinContentOCIStoreName(ctx context.Context) string
	LeaseManager(ctx context.Context) *leaseutil.Manager
	Platform(ctx context.Context) Platform

	Solve(ctx context.Context, req bkgw.SolveRequest) (Result, error)
	ResolveImageConfig(ctx context.Context, ref string, opt sourceresolver.Opt) (string, digest.Digest, []byte, error)
	NewContainer(ctx context.Context, req bkgw.NewContainerRequest) (bkgw.Container, error)

	PublishContainerImage(
		ctx context.Context,
		inputByPlatform map[string]ContainerExport,
		opts map[string]string,
	) (map[string]string, error)
	ExportContainerImage(
		ctx context.Context,
		inputByPlatform map[string]ContainerExport,
		destPath string,
		opts map[string]string,
	) (map[string]string, error)
	ContainerImageToTarball(
		ctx context.Context,
		engineHostPlatform specs.Platform,
		fileName string,
		inputByPlatform map[string]ContainerExport,
		opts map[string]string,
	) (*bksolverpb.Definition, error)

	LocalImport(
		ctx context.Context,
		platform specs.Platform,
		srcPath string,
		excludePatterns []string,
		includePatterns []string,
	) (*bksolverpb.Definition, specs.Descriptor, error)
	LocalDirExport(
		ctx context.Context,
		def *bksolverpb.Definition,
		destPath string,
		merge bool,
	) error
	LocalFileExport(
		ctx context.Context,
		def *bksolverpb.Definition,
		destPath string,
		filePath string,
		allowParentDirPath bool,
	) error
	IOReaderExport(ctx context.Context, r io.Reader, destPath string, destMode os.FileMode) error
	StatCallerHostPath(ctx context.Context, path string, returnAbsPath bool) (*fsutiltypes.Stat, error)
	ReadCallerHostFile(ctx context.Context, path string) ([]byte, error)
	EngineContainerLocalImport(
		ctx context.Context,
		platform specs.Platform,
		srcPath string,
		excludePatterns []string,
		includePatterns []string,
	) (*bksolverpb.Definition, specs.Descriptor, error)
	DefToBlob(
		ctx context.Context,
		pbDef *bksolverpb.Definition,
	) (*bksolverpb.Definition, specs.Descriptor, error)

	ListenHostToContainer(
		ctx context.Context,
		hostListenAddr, proto, upstream string,
	) (*dagsession.ListenResponse, func() error, error)
}

type Result interface {
	bkgw.Reference
	AddDependencyBlobs(ctx context.Context, blobs map[digest.Digest]*ocispecs.Descriptor) error
	Result(ctx context.Context) (bksolver.CachedResult, error)
	CacheRef(ctx context.Context) (bkcache.ImmutableRef, error)
	Metadata() map[string][]byte
	Provenance() *provenance.Capture
}

type ContainerExport struct {
	Definition *bksolverpb.Definition
	Config     specs.ImageConfig
}

func NewRoot(e Engine) *Query {
	return &Query{Engine: e}
}

func (*Query) Type() *ast.Type {
	return &ast.Type{
		NamedType: "Query",
		NonNull:   true,
	}
}

func (*Query) TypeDescription() string {
	return "The root of the DAG."
}

func (q Query) Clone() *Query {
	return &q
}

func (q *Query) WithPipeline(name, desc string) *Query {
	q = q.Clone()
	q.Pipeline = q.Pipeline.Add(pipeline.Pipeline{
		Name:        name,
		Description: desc,
	})
	return q
}

func (q *Query) NewContainer(platform Platform) *Container {
	return &Container{
		Query:    q,
		Platform: platform,
	}
}

func (q *Query) NewSecret(name string, accessor string) *Secret {
	return &Secret{
		Query:    q,
		Name:     name,
		Accessor: accessor,
	}
}

func (q *Query) NewHost() *Host {
	return &Host{
		Query: q,
	}
}

func (q *Query) NewModule() *Module {
	return &Module{
		Query: q,
	}
}

func (q *Query) NewContainerService(ctr *Container) *Service {
	return &Service{
		Query:     q,
		Container: ctr,
	}
}

func (q *Query) NewTunnelService(upstream dagql.Instance[*Service], ports []PortForward) *Service {
	return &Service{
		Query:          q,
		TunnelUpstream: &upstream,
		TunnelPorts:    ports,
	}
}

func (q *Query) NewHostService(upstream string, ports []PortForward, sessionID string) *Service {
	return &Service{
		Query:         q,
		HostUpstream:  upstream,
		HostPorts:     ports,
		HostSessionID: sessionID,
	}
}

// IDDeps loads the module dependencies of a given ID.
//
// The returned ModDeps extends the inner DefaultDeps with all modules found in
// the ID, loaded by using the DefaultDeps schema.
func (q *Query) IDDeps(ctx context.Context, id *call.ID) (*ModDeps, error) {
	defaultDeps, err := q.DefaultDeps(ctx)
	if err != nil {
		return nil, fmt.Errorf("default deps: %w", err)
	}
	bootstrap, err := defaultDeps.Schema(ctx)
	if err != nil {
		return nil, fmt.Errorf("bootstrap schema: %w", err)
	}
	deps := defaultDeps
	for _, modID := range id.Modules() {
		mod, err := dagql.NewID[*Module](modID.ID()).Load(ctx, bootstrap)
		if err != nil {
			return nil, fmt.Errorf("load source mod: %w", err)
		}
		deps = deps.Append(mod.Self)
	}
	return deps, nil
}

/* TODO: rm
func (q *Query) ServeModule(ctx context.Context, mod *Module) error {
	clientMetadata, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return err
	}

	q.ClientCallMu.Lock()
	defer q.ClientCallMu.Unlock()
	callCtx, ok := q.ClientCallContext[clientMetadata.ClientID]
	if !ok {
		return fmt.Errorf("client call not found")
	}
	callCtx.Deps = callCtx.Deps.Append(mod)
	return nil
}

func (q *Query) RegisterCaller(ctx context.Context, call *FunctionCall) (string, error) {
	if call == nil {
		call = &FunctionCall{}
	}
	callCtx := &ClientCallContext{
		FnCall: call,
	}

	currentID := dagql.CurrentID(ctx)
	clientIDInputs := []string{currentID.Digest().String()}
	if !call.Cache {
		// use the ServerID so that we bust cache once-per-session
		clientMetadata, err := engine.ClientMetadataFromContext(ctx)
		if err != nil {
			return "", err
		}
		clientIDInputs = append(clientIDInputs, clientMetadata.ServerID)
	}
	clientIDDigest := digest.FromString(strings.Join(clientIDInputs, " "))

	// only use encoded part of digest because this ID ends up becoming a buildkit Session ID
	// and buildkit has some ancient internal logic that splits on a colon to support some
	// dev mode logic: https://github.com/moby/buildkit/pull/290
	// also trim it to 25 chars as it ends up becoming part of service URLs
	clientID := clientIDDigest.Encoded()[:25]

	slog.ExtraDebug("registering nested caller",
		"client_id", clientID,
		"op", currentID.Display(),
	)

	if call.Module == nil {
		callCtx.Deps = q.DefaultDeps
	} else {
		callCtx.Deps = call.Module.Deps
		// By default, serve both deps and the module's own API to itself. But if SkipSelfSchema is set,
		// only serve the APIs of the deps of this module. This is currently only needed for the special
		// case of the function used to get the definition of the module itself (which can't obviously
		// be served the API its returning the definition of).
		if !call.SkipSelfSchema {
			callCtx.Deps = callCtx.Deps.Append(call.Module)
		}
	}

	q.ClientCallMu.Lock()
	defer q.ClientCallMu.Unlock()
	_, ok := q.ClientCallContext[clientID]
	if ok {
		return clientID, nil
	}

	var err error
	callCtx.Root, err = NewRoot(ctx, q.QueryOpts)
	if err != nil {
		return "", err
	}

	q.ClientCallContext[clientID] = callCtx
	return clientID, nil
}

func (q *Query) CurrentModule(ctx context.Context) (*Module, error) {
	clientMetadata, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if clientMetadata.ClientID == q.MainClientCallerID {
		return nil, fmt.Errorf("%w: main client caller has no current module", ErrNoCurrentModule)
	}

	q.ClientCallMu.RLock()
	defer q.ClientCallMu.RUnlock()
	callCtx, ok := q.ClientCallContext[clientMetadata.ClientID]
	if !ok {
		return nil, fmt.Errorf("client call %s not found", clientMetadata.ClientID)
	}
	if callCtx.FnCall.Module == nil {
		return nil, ErrNoCurrentModule
	}
	return callCtx.FnCall.Module, nil
}

func (q *Query) CurrentFunctionCall(ctx context.Context) (*FunctionCall, error) {
	clientMetadata, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if clientMetadata.ClientID == q.MainClientCallerID {
		return nil, fmt.Errorf("%w: main client caller has no function", ErrNoCurrentModule)
	}

	q.ClientCallMu.RLock()
	defer q.ClientCallMu.RUnlock()
	callCtx, ok := q.ClientCallContext[clientMetadata.ClientID]
	if !ok {
		return nil, fmt.Errorf("client call %s not found", clientMetadata.ClientID)
	}

	return callCtx.FnCall, nil
}

func (q *Query) CurrentServedDeps(ctx context.Context) (*ModDeps, error) {
	clientMetadata, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return nil, err
	}
	callCtx, ok := q.ClientCallContext[clientMetadata.ClientID]
	if !ok {
		return nil, fmt.Errorf("client call %s not found", clientMetadata.ClientID)
	}
	return callCtx.Deps, nil
}
*/
