package core

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/containerd/containerd/platforms"
	cacheutil "github.com/moby/buildkit/cache/util"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend"
	bkgw "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/gateway/container"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver"
	solvererror "github.com/moby/buildkit/solver/errdefs"
	llberror "github.com/moby/buildkit/solver/llbsolver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	solverresult "github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/worker"
	"github.com/opencontainers/go-digest"
	fstypes "github.com/tonistiigi/fsutil/types"
	"github.com/vito/progrock"
	"golang.org/x/sync/errgroup"
)

const (
	// Exec errors will only include the last this number of bytes of output.
	MaxExecErrorOutputBytes = 100 * 1024

	// TruncationMessage is the message that will be prepended to truncated output.
	TruncationMessage = "[omitting %d bytes]..."

	// MaxFileContentsChunkSize sets the maximum chunk size for ReadFile calls
	// Equals around 95% of the max message size (16777216) in
	// order to keep space for any Protocol Buffers overhead:
	MaxFileContentsChunkSize = 15938355

	// MaxFileContentsSize sets the limit of the maximum file size
	// that can be retrieved using File.Contents, currently set to 128MB:
	MaxFileContentsSize = 128 << 20
)

// TODO: rename this to something else now? Gateway is confusing name, not accurate anymore too
// TODO: also, this really belongs in the engine package if possible
// GatewayClient wraps the standard buildkit gateway client with a few extensions:
//
// * Errors include the output of execs when they fail.
// * Vertexes are joined to the Progrock group using the recorder from ctx.
// * Cache imports can be configured across all Solves.
// * All Solved results can be retrieved for cache exports.
type GatewayClient struct {
	llbBridge        frontend.FrontendLLBBridge
	worker           worker.Worker
	sessionManager   *session.Manager
	refs             map[*ref]struct{}
	cacheConfigType  string
	cacheConfigAttrs map[string]string
	mu               sync.Mutex
}

func NewGatewayClient(
	baseClient frontend.FrontendLLBBridge,
	worker worker.Worker,
	sessionManager *session.Manager,
	cacheConfigType string,
	cacheConfigAttrs map[string]string,
) *GatewayClient {
	return &GatewayClient{
		// Wrap the client with recordingGateway just so we can separate concerns a
		// tiny bit.
		llbBridge:        recordingGateway{baseClient},
		worker:           worker,
		sessionManager:   sessionManager,
		cacheConfigType:  cacheConfigType,
		cacheConfigAttrs: cacheConfigAttrs,
		refs:             make(map[*ref]struct{}),
	}
}

type Result = solverresult.Result[*ref]

func (g *GatewayClient) Solve(ctx context.Context, req frontend.SolveRequest, sessionID string) (_ *Result, rerr error) {
	if g.cacheConfigType != "" {
		req.CacheImports = []bkgw.CacheOptionsEntry{{
			Type:  g.cacheConfigType,
			Attrs: g.cacheConfigAttrs,
		}}
	}
	llbRes, err := g.llbBridge.Solve(ctx, req, sessionID)
	if err != nil {
		return nil, wrapError(ctx, err, sessionID)
	}
	res, err := solverresult.ConvertResult(llbRes, func(rp solver.ResultProxy) (*ref, error) {
		return newRef(rp, g, sessionID), nil
	})
	if err != nil {
		return nil, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if res.Ref != nil {
		g.refs[res.Ref] = struct{}{}
	}
	for _, rf := range res.Refs {
		g.refs[rf] = struct{}{}
	}
	return res, nil
}

func (g *GatewayClient) ResolveImageConfig(ctx context.Context, ref string, opt llb.ResolveImageConfigOpt) (digest.Digest, []byte, error) {
	return g.llbBridge.ResolveImageConfig(ctx, ref, opt)
}

func (g *GatewayClient) NewContainer(ctx context.Context, req bkgw.NewContainerRequest, sessionID string) (bkgw.Container, error) {
	ctrReq := container.NewContainerRequest{
		ContainerID: identity.NewID(), // TODO: give a meaningful name?
		NetMode:     req.NetMode,
		Hostname:    req.Hostname,
		Mounts:      make([]container.Mount, len(req.Mounts)),
	}

	extraHosts, err := container.ParseExtraHosts(req.ExtraHosts)
	if err != nil {
		return nil, err
	}
	ctrReq.ExtraHosts = extraHosts

	// get the input mounts in parallel in case they need to be evaluated, which can be expensive
	eg, ctx := errgroup.WithContext(ctx)
	for i, m := range req.Mounts {
		i, m := i, m
		eg.Go(func() error {
			ref, ok := m.Ref.(*ref)
			if !ok {
				return fmt.Errorf("unexpected ref type: %T", m.Ref)
			}
			var workerRef *worker.WorkerRef
			if ref != nil {
				res, err := ref.resultProxy.Result(ctx)
				if err != nil {
					return err
				}
				var ok bool
				workerRef, ok = res.Sys().(*worker.WorkerRef)
				if !ok {
					return fmt.Errorf("invalid res: %T", res.Sys())
				}
			}
			ctrReq.Mounts[i] = container.Mount{
				WorkerRef: workerRef,
				Mount: &pb.Mount{
					Dest:      m.Dest,
					Selector:  m.Selector,
					Readonly:  m.Readonly,
					MountType: m.MountType,
					CacheOpt:  m.CacheOpt,
					SecretOpt: m.SecretOpt,
					SSHOpt:    m.SSHOpt,
				},
			}
			return nil
		})
	}
	err = eg.Wait()
	if err != nil {
		return nil, err
	}

	ctr, err := container.NewContainer(
		ctx,
		g.worker,
		g.sessionManager,
		session.NewGroup(sessionID),
		ctrReq,
	)
	if err != nil {
		return nil, err
	}
	// TODO: cleanup containers at end of session, if that doesn't happen automatically already
	return ctr, nil
}

// CombinedResult returns a buildkit result with all the refs solved by this client so far.
// This is useful for constructing a result for remote caching.
func (g *GatewayClient) CombinedResult(ctx context.Context, sessionID string) (*Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	mergeInputs := make([]llb.State, 0, len(g.refs))
	for r := range g.refs {
		state, err := r.ToState()
		if err != nil {
			return nil, err
		}
		mergeInputs = append(mergeInputs, state)
	}
	llbdef, err := llb.Merge(mergeInputs, llb.WithCustomName("combined session result")).Marshal(ctx)
	if err != nil {
		return nil, err
	}
	return g.Solve(ctx, bkgw.SolveRequest{
		Definition: llbdef.ToPB(),
	}, sessionID)
}

func newRef(res solver.ResultProxy, gw *GatewayClient, sessionID string) *ref {
	return &ref{
		resultProxy: res,
		gw:          gw,
		sessionID:   sessionID,
	}
}

type ref struct {
	resultProxy solver.ResultProxy
	gw          *GatewayClient
	sessionID   string
}

func (r *ref) ToState() (llb.State, error) {
	return defToState(r.resultProxy.Definition())
}

func (r *ref) Evaluate(ctx context.Context) error {
	_, err := r.Result(ctx)
	if err != nil {
		return err
	}
	return nil
}

func (r *ref) ReadFile(ctx context.Context, req bkgw.ReadRequest) ([]byte, error) {
	mnt, err := r.getMountable(ctx)
	if err != nil {
		return nil, err
	}
	cacheReq := cacheutil.ReadRequest{
		Filename: req.Filename,
	}
	if r := req.Range; r != nil {
		cacheReq.Range = &cacheutil.FileRange{
			Offset: r.Offset,
			Length: r.Length,
		}
	}
	return cacheutil.ReadFile(ctx, mnt, cacheReq)
}

func (r *ref) ReadDir(ctx context.Context, req bkgw.ReadDirRequest) ([]*fstypes.Stat, error) {
	mnt, err := r.getMountable(ctx)
	if err != nil {
		return nil, err
	}
	cacheReq := cacheutil.ReadDirRequest{
		Path:           req.Path,
		IncludePattern: req.IncludePattern,
	}
	return cacheutil.ReadDir(ctx, mnt, cacheReq)
}

func (r *ref) StatFile(ctx context.Context, req bkgw.StatRequest) (*fstypes.Stat, error) {
	mnt, err := r.getMountable(ctx)
	if err != nil {
		return nil, err
	}
	return cacheutil.StatFile(ctx, mnt, req.Path)
}

func (r *ref) getMountable(ctx context.Context) (snapshot.Mountable, error) {
	res, err := r.Result(ctx)
	if err != nil {
		return nil, err
	}
	workerRef, ok := res.Sys().(*worker.WorkerRef)
	if !ok {
		return nil, fmt.Errorf("invalid ref: %T", res.Sys())
	}
	return workerRef.ImmutableRef.Mount(ctx, true, session.NewGroup(r.sessionID))
}

func (r *ref) Result(ctx context.Context) (solver.CachedResult, error) {
	res, err := r.resultProxy.Result(ctx)
	if err != nil {
		return nil, wrapError(ctx, err, r.sessionID)
	}
	return res, nil
}

func wrapError(ctx context.Context, baseErr error, sessionID string) error {
	var fileErr *llberror.FileActionError
	if errors.As(baseErr, &fileErr) {
		return solvererror.WithSolveError(baseErr, fileErr.ToSubject(), nil, nil)
	}

	var slowCacheErr *solver.SlowCacheError
	if errors.As(baseErr, &slowCacheErr) {
		// TODO: include input IDs? Or does that not matter for us?
		return solvererror.WithSolveError(baseErr, slowCacheErr.ToSubject(), nil, nil)
	}

	var execErr *llberror.ExecError
	if !errors.As(baseErr, &execErr) {
		return baseErr
	}

	var opErr *solvererror.OpError
	if !errors.As(baseErr, &opErr) {
		return baseErr
	}
	op := opErr.Op
	if op == nil || op.Op == nil {
		return baseErr
	}
	execOp, ok := op.Op.(*pb.Op_Exec)
	if !ok {
		return baseErr
	}

	// This was an exec error, we will retrieve the exec's output and include
	// it in the error message

	// get the mnt corresponding to the metadata where stdout/stderr are stored
	// TODO: support redirected stdout/stderr again too, maybe just have shim write to both?
	var metaMountResult solver.Result
	for i, mnt := range execOp.Exec.Mounts {
		if mnt.Dest == metaMountDestPath {
			metaMountResult = execErr.Mounts[i]
			break
		}
	}
	if metaMountResult == nil {
		return baseErr
	}

	workerRef, ok := metaMountResult.Sys().(*worker.WorkerRef)
	if !ok {
		return errors.Join(baseErr, fmt.Errorf("invalid ref type: %T", metaMountResult.Sys()))
	}
	mntable, err := workerRef.ImmutableRef.Mount(ctx, true, session.NewGroup(sessionID))
	if err != nil {
		return errors.Join(err, baseErr)
	}

	stdoutBytes, err := getExecMetaFile(ctx, mntable, "stdout")
	if err != nil {
		return errors.Join(err, baseErr)
	}
	stderrBytes, err := getExecMetaFile(ctx, mntable, "stderr")
	if err != nil {
		return errors.Join(err, baseErr)
	}

	exitCodeBytes, err := getExecMetaFile(ctx, mntable, "exitCode")
	if err != nil {
		return errors.Join(err, baseErr)
	}
	exitCode := -1
	if len(exitCodeBytes) > 0 {
		exitCode, err = strconv.Atoi(string(exitCodeBytes))
		if err != nil {
			return errors.Join(err, baseErr)
		}
	}

	return &ExecError{
		original: baseErr,
		Cmd:      execOp.Exec.Meta.Args,
		ExitCode: exitCode,
		Stdout:   strings.TrimSpace(string(stdoutBytes)),
		Stderr:   strings.TrimSpace(string(stderrBytes)),
	}
}

func getExecMetaFile(ctx context.Context, mntable snapshot.Mountable, fileName string) ([]byte, error) {
	filePath := path.Join(metaMountDestPath, fileName)
	stat, err := cacheutil.StatFile(ctx, mntable, filePath)
	if err != nil {
		// TODO: would be better to verify this is a "not exists" error, return err if not
		return nil, nil
	}

	req := cacheutil.ReadRequest{
		Filename: filePath,
		Range: &cacheutil.FileRange{
			Length: int(stat.Size_),
		},
	}
	if req.Range.Length > MaxExecErrorOutputBytes {
		// TODO: re-add truncation message
		req.Range.Offset = int(stat.Size_) - MaxExecErrorOutputBytes
		req.Range.Length = MaxExecErrorOutputBytes
	}
	return cacheutil.ReadFile(ctx, mntable, req)
}

type recordingGateway struct {
	llbBridge frontend.FrontendLLBBridge
}

// ResolveImageConfig records the image config resolution vertex as a member of
// the current progress group, and calls the inner ResolveImageConfig.
func (g recordingGateway) ResolveImageConfig(ctx context.Context, ref string, opt llb.ResolveImageConfigOpt) (digest.Digest, []byte, error) {
	rec := progrock.RecorderFromContext(ctx)

	// HACK(vito): this is how Buildkit determines the vertex digest. Keep this
	// in sync with Buildkit until a better way to do this arrives. It hasn't
	// changed in 5 years, surely it won't soon, right?
	id := ref
	if platform := opt.Platform; platform == nil {
		id += platforms.Format(platforms.DefaultSpec())
	} else {
		id += platforms.Format(*platform)
	}

	rec.Join(digest.FromString(id))

	return g.llbBridge.ResolveImageConfig(ctx, ref, opt)
}

// Solve records the vertexes of the definition and frontend inputs as members
// of the current progress group, and calls the inner Solve.
func (g recordingGateway) Solve(ctx context.Context, req frontend.SolveRequest, sessionID string) (*frontend.Result, error) {
	rec := progrock.RecorderFromContext(ctx)

	if req.Definition != nil {
		recordVertexes(rec, req.Definition)
	}

	for _, input := range req.FrontendInputs {
		if input == nil {
			// TODO(vito): we currently pass a nil def to Dockerfile inputs, should
			// probably change that to llb.Scratch
			continue
		}

		recordVertexes(rec, input)
	}

	return g.llbBridge.Solve(ctx, req, sessionID)
}

func (g recordingGateway) Warn(ctx context.Context, dgst digest.Digest, msg string, opts frontend.WarnOpts) error {
	return g.llbBridge.Warn(ctx, dgst, msg, opts)
}

func recordVertexes(recorder *progrock.Recorder, def *pb.Definition) {
	dgsts := []digest.Digest{}
	for dgst, meta := range def.Metadata {
		_ = meta
		if meta.ProgressGroup != nil {
			// Regular progress group, i.e. from Dockerfile; record it as a subgroup,
			// with 'weak' annotation so it's distinct from user-configured
			// pipelines.
			recorder.WithGroup(meta.ProgressGroup.Name, progrock.Weak()).Join(dgst)
		} else {
			dgsts = append(dgsts, dgst)
		}
	}

	recorder.Join(dgsts...)
}
