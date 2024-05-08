package newserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	bkcache "github.com/moby/buildkit/cache"
	bkcacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/cache/remotecache"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	bkfrontend "github.com/moby/buildkit/frontend"
	bkgw "github.com/moby/buildkit/frontend/gateway/client"
	bkcontainer "github.com/moby/buildkit/frontend/gateway/container"
	"github.com/moby/buildkit/identity"
	bksession "github.com/moby/buildkit/session"
	bksolver "github.com/moby/buildkit/solver"
	bksolverpb "github.com/moby/buildkit/solver/pb"
	solverresult "github.com/moby/buildkit/solver/result"
	"github.com/moby/buildkit/util/bklog"
	bkworker "github.com/moby/buildkit/worker"
	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/metadata"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/engine/newserver/llbdefinition"
	dagsession "github.com/dagger/dagger/engine/session"
)

type ResolveCacheExporterFunc func(ctx context.Context, g bksession.Group) (remotecache.Exporter, error)

func (srv *Server) Solve(ctx context.Context, req bkgw.SolveRequest) (core.Result, error) {
	res, err := srv.solve(ctx, req)
	if err != nil {
		return nil, err
	}
	return res.SingleRef()
}

func (srv *Server) solve(ctx context.Context, req bkgw.SolveRequest) (_ *result, rerr error) {
	client, err := srv.clientFromContext(ctx)
	if err != nil {
		return nil, err
	}
	ctx = withOutgoingContext(ctx)

	sess := client.daggerSession
	bkSessionID := client.buildkitSession.ID()

	// include upstream cache imports, if any
	req.CacheImports = sess.cacheImporterCfgs

	var llbRes *bkfrontend.Result
	switch {
	case req.Definition != nil && req.Definition.Def != nil:
		llbRes, err = client.llbBridge.Solve(ctx, req, bkSessionID)
		if err != nil {
			return nil, wrapError(ctx, err, bkSessionID)
		}
	case req.Frontend != "":
		// HACK: don't force evaluation like this, we can write custom frontend
		// wrappers (for dockerfile.v0 and gateway.v0) that read from ctx to
		// replace the llbBridge it knows about.
		// This current implementation may be limited when it comes to
		// implement provenance/etc.

		f, ok := srv.frontends[req.Frontend]
		if !ok {
			return nil, fmt.Errorf("invalid frontend: %s", req.Frontend)
		}

		gw := newFilterGateway(client.llbBridge, req)
		gw.secretTranslator = ctx.Value("secret-translator").(func(string) (string, error))

		llbRes, err = f.Solve(
			ctx,
			gw,
			client.worker, // also implements Executor
			req.FrontendOpt,
			req.FrontendInputs,
			bkSessionID,
			srv.bkSessionManager,
		)
		if err != nil {
			return nil, err
		}
		if req.Evaluate {
			err = llbRes.EachRef(func(ref bksolver.ResultProxy) error {
				_, err := ref.Result(ctx)
				return err
			})
			if err != nil {
				return nil, err
			}
		}
	default:
		llbRes = &bkfrontend.Result{}
	}

	res, err := solverresult.ConvertResult(llbRes, func(rp bksolver.ResultProxy) (*ref, error) {
		return srv.newRef(rp, llbRes.Metadata), nil
	})
	if err != nil {
		llbRes.EachRef(func(rp bksolver.ResultProxy) error {
			return rp.Release(context.Background())
		})
		return nil, err
	}

	sess.refsMu.Lock()
	defer sess.refsMu.Unlock()
	if res.Ref != nil {
		sess.refs[res.Ref] = struct{}{}
	}
	for _, rf := range res.Refs {
		sess.refs[rf] = struct{}{}
	}
	return res, nil
}

func (srv *Server) ResolveImageConfig(ctx context.Context, ref string, opt sourceresolver.Opt) (string, digest.Digest, []byte, error) {
	client, err := srv.clientFromContext(ctx)
	if err != nil {
		return "", "", nil, err
	}
	ctx = withOutgoingContext(ctx)

	imr := sourceresolver.NewImageMetaResolver(client.llbBridge)
	return imr.ResolveImageConfig(ctx, ref, opt)
}

func (srv *Server) ResolveSourceMetadata(ctx context.Context, op *bksolverpb.SourceOp, opt sourceresolver.Opt) (*sourceresolver.MetaResponse, error) {
	client, err := srv.clientFromContext(ctx)
	if err != nil {
		return nil, err
	}
	ctx = withOutgoingContext(ctx)

	return client.llbBridge.ResolveSourceMetadata(ctx, op, opt)
}

func (srv *Server) NewContainer(ctx context.Context, req bkgw.NewContainerRequest) (bkgw.Container, error) {
	client, err := srv.clientFromContext(ctx)
	if err != nil {
		return nil, err
	}
	ctx = withOutgoingContext(ctx)

	ctrReq := bkcontainer.NewContainerRequest{
		ContainerID: identity.NewID(),
		NetMode:     req.NetMode,
		Hostname:    req.Hostname,
		Mounts:      make([]bkcontainer.Mount, len(req.Mounts)),
	}

	extraHosts, err := bkcontainer.ParseExtraHosts(req.ExtraHosts)
	if err != nil {
		return nil, err
	}
	ctrReq.ExtraHosts = extraHosts

	// get the input mounts in parallel in case they need to be evaluated, which can be expensive
	eg, egctx := errgroup.WithContext(ctx)
	for i, m := range req.Mounts {
		i, m := i, m
		eg.Go(func() error {
			var workerRef *bkworker.WorkerRef
			if m.Ref != nil {
				ref, ok := m.Ref.(*ref)
				if !ok {
					return fmt.Errorf("dagger: unexpected ref type: %T", m.Ref)
				}
				if ref != nil { // TODO(vito): apparently this is possible. scratch?
					res, err := ref.resultProxy.Result(egctx)
					if err != nil {
						return fmt.Errorf("result: %w", err)
					}
					workerRef, ok = res.Sys().(*bkworker.WorkerRef)
					if !ok {
						return fmt.Errorf("invalid res: %T", res.Sys())
					}
				}
			}
			ctrReq.Mounts[i] = bkcontainer.Mount{
				WorkerRef: workerRef,
				Mount: &bksolverpb.Mount{
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
		return nil, fmt.Errorf("wait: %w", err)
	}

	// using context.Background so it continues running until exit or when the session ends
	ctr, err := bkcontainer.NewContainer(
		context.Background(),
		srv.workerCache,
		client.worker, // also implements Executor
		srv.bkSessionManager,
		bksession.NewGroup(client.buildkitSession.ID()),
		ctrReq,
	)
	if err != nil {
		return nil, err
	}

	client.containersMu.Lock()
	defer client.containersMu.Unlock()
	if client.containers == nil {
		if err := ctr.Release(context.Background()); err != nil {
			return nil, fmt.Errorf("release after close: %w", err)
		}
		return nil, errors.New("client closed")
	}
	client.containers[ctr] = struct{}{}
	return ctr, nil
}

// combinedResult returns a buildkit result with all the refs solved by the session so far.
// This is useful for constructing a result for upstream remote caching.
func (srv *Server) combinedResult(ctx context.Context) (*result, error) {
	client, err := srv.clientFromContext(ctx)
	if err != nil {
		return nil, err
	}
	ctx = withOutgoingContext(ctx)

	sess := client.daggerSession

	sess.refsMu.Lock()
	mergeInputs := make([]llb.State, 0, len(sess.refs))
	for r := range sess.refs {
		state, err := r.ToState()
		if err != nil {
			sess.refsMu.Unlock()
			return nil, err
		}
		mergeInputs = append(mergeInputs, state)
	}
	sess.refsMu.Unlock()
	llbdef, err := llb.Merge(mergeInputs, llb.WithCustomName("combined session result")).Marshal(ctx)
	if err != nil {
		return nil, err
	}
	return srv.solve(ctx, bkgw.SolveRequest{
		Definition: llbdef.ToPB(),
	})
}

func (srv *Server) UpstreamCacheExport(ctx context.Context, cacheExportFuncs []ResolveCacheExporterFunc) error {
	client, err := srv.clientFromContext(ctx)
	if err != nil {
		return err
	}
	ctx = withOutgoingContext(ctx)

	if len(cacheExportFuncs) == 0 {
		return nil
	}
	bklog.G(ctx).Debugf("exporting %d caches", len(cacheExportFuncs))

	combinedResult, err := srv.combinedResult(ctx)
	if err != nil {
		return err
	}
	cacheRes, err := ConvertToWorkerCacheResult(ctx, combinedResult)
	if err != nil {
		return fmt.Errorf("failed to convert result: %w", err)
	}
	bklog.G(ctx).Debugf("converting to solverRes")
	solverRes, err := solverresult.ConvertResult(combinedResult, func(rf *ref) (bksolver.CachedResult, error) {
		return rf.resultProxy.Result(ctx)
	})
	if err != nil {
		return fmt.Errorf("failed to convert result: %w", err)
	}

	sessionGroup := bksession.NewGroup(client.buildkitSession.ID())
	eg, ctx := errgroup.WithContext(ctx)
	// TODO: send progrock statuses for cache export progress
	for _, exporterFunc := range cacheExportFuncs {
		exporterFunc := exporterFunc
		eg.Go(func() error {
			bklog.G(ctx).Debugf("getting exporter")
			exporter, err := exporterFunc(ctx, sessionGroup)
			if err != nil {
				return err
			}
			bklog.G(ctx).Debugf("exporting cache with %T", exporter)
			compressionCfg := exporter.Config().Compression
			err = solverresult.EachRef(solverRes, cacheRes, func(res bksolver.CachedResult, ref bkcache.ImmutableRef) error {
				bklog.G(ctx).Debugf("exporting cache for %s", ref.ID())
				ctx := withDescHandlerCacheOpts(ctx, ref)
				bklog.G(ctx).Debugf("calling exporter")
				_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, exporter, bksolver.CacheExportOpt{
					ResolveRemotes: func(ctx context.Context, res bksolver.Result) ([]*bksolver.Remote, error) {
						ref, ok := res.Sys().(*bkworker.WorkerRef)
						if !ok {
							return nil, fmt.Errorf("invalid result: %T", res.Sys())
						}
						bklog.G(ctx).Debugf("getting remotes for %s", ref.ID())
						defer bklog.G(ctx).Debugf("got remotes for %s", ref.ID())
						return ref.GetRemotes(ctx, true, bkcacheconfig.RefConfig{Compression: compressionCfg}, false, sessionGroup)
					},
					Mode:           bksolver.CacheExportModeMax,
					Session:        sessionGroup,
					CompressionOpt: &compressionCfg,
				})
				return err
			})
			if err != nil {
				return err
			}
			bklog.G(ctx).Debugf("finalizing exporter")
			defer bklog.G(ctx).Debugf("finalized exporter")
			_, err = exporter.Finalize(ctx)
			return err
		})
	}
	bklog.G(ctx).Debugf("waiting for cache export")
	defer bklog.G(ctx).Debugf("waited for cache export")
	return eg.Wait()
}

// TODO: this should be in a diff file
// TODO: this should be in a diff file
// TODO: this should be in a diff file
func (srv *Server) ListenHostToContainer(
	ctx context.Context,
	hostListenAddr, proto, upstream string,
) (*dagsession.ListenResponse, func() error, error) {
	client, err := srv.clientFromContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	ctx = withOutgoingContext(ctx)
	ctx, cancel := context.WithCancel(ctx)

	clientCaller, err := srv.bkSessionManager.Get(ctx, client.buildkitSession.ID(), true)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("failed to get requester session: %w", err)
	}

	conn := clientCaller.Conn()

	tunnelClient := dagsession.NewTunnelListenerClient(conn)

	listener, err := tunnelClient.Listen(ctx)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("failed to listen: %w", err)
	}

	err = listener.Send(&dagsession.ListenRequest{
		Addr:     hostListenAddr,
		Protocol: proto,
	})
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("failed to send listen request: %w", err)
	}

	listenRes, err := listener.Recv()
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("failed to receive listen response: %w", err)
	}

	conns := map[string]net.Conn{}
	connsL := &sync.Mutex{}
	sendL := &sync.Mutex{}

	wg := new(sync.WaitGroup)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			res, err := listener.Recv()
			if err != nil {
				bklog.G(ctx).Warnf("listener recv err: %s", err)
				return
			}

			connID := res.GetConnId()
			if connID == "" {
				continue
			}

			connsL.Lock()
			conn, found := conns[connID]
			connsL.Unlock()

			if !found {
				conn, err := client.dialer.Dial(proto, upstream)
				if err != nil {
					bklog.G(ctx).Warnf("failed to dial %s %s: %s", proto, upstream, err)
					return
				}

				connsL.Lock()
				conns[connID] = conn
				connsL.Unlock()

				wg.Add(1)
				go func() {
					defer wg.Done()

					data := make([]byte, 32*1024)
					for {
						n, err := conn.Read(data)
						if err != nil {
							return
						}

						sendL.Lock()
						err = listener.Send(&dagsession.ListenRequest{
							ConnId: connID,
							Data:   data[:n],
						})
						sendL.Unlock()
						if err != nil {
							return
						}
					}
				}()
			}

			if res.Data != nil {
				_, err = conn.Write(res.Data)
				if err != nil {
					return
				}
			}
		}
	}()

	return listenRes, func() error {
		defer cancel()
		sendL.Lock()
		err := listener.CloseSend()
		sendL.Unlock()
		connsL.Lock()
		for _, conn := range conns {
			conn.Close()
		}
		clear(conns)
		connsL.Unlock()
		if err == nil {
			wg.Wait()
		}
		return err
	}, nil
}

func withDescHandlerCacheOpts(ctx context.Context, ref bkcache.ImmutableRef) context.Context {
	return bksolver.WithCacheOptGetter(ctx, func(_ bool, keys ...interface{}) map[interface{}]interface{} {
		vals := make(map[interface{}]interface{})
		for _, k := range keys {
			if key, ok := k.(bkcache.DescHandlerKey); ok {
				if handler := ref.DescHandler(digest.Digest(key)); handler != nil {
					vals[k] = handler
				}
			}
		}
		return vals
	})
}

func withOutgoingContext(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	return ctx
}

// filteringGateway is a helper gateway that filters+converts various
// operations for the frontend
type filteringGateway struct {
	bkfrontend.FrontendLLBBridge

	// secretTranslator is a function to convert secret ids. Frontends may
	// attempt to access secrets by raw IDs, but they may be keyed differently
	// in the secret store.
	secretTranslator func(string) (string, error)

	// skipInputs specifies op digests that were part of the request inputs and
	// so shouldn't be processed.
	skipInputs map[digest.Digest]struct{}
}

func newFilterGateway(bridge bkfrontend.FrontendLLBBridge, req bkgw.SolveRequest) *filteringGateway {
	inputs := map[digest.Digest]struct{}{}
	for _, inp := range req.FrontendInputs {
		for _, def := range inp.Def {
			inputs[digest.FromBytes(def)] = struct{}{}
		}
	}

	return &filteringGateway{
		FrontendLLBBridge: bridge,
		skipInputs:        inputs,
	}
}

func (gw *filteringGateway) Solve(ctx context.Context, req bkfrontend.SolveRequest, sid string) (*bkfrontend.Result, error) {
	if req.Definition != nil && req.Definition.Def != nil {
		dag, err := llbdefinition.DefToDAG(req.Definition)
		if err != nil {
			return nil, err
		}
		if err := dag.Walk(func(dag *llbdefinition.OpDAG) error {
			if _, ok := gw.skipInputs[*dag.OpDigest]; ok {
				return llbdefinition.SkipInputs
			}

			execOp, ok := dag.AsExec()
			if !ok {
				return nil
			}

			for _, secret := range execOp.ExecOp.GetSecretenv() {
				secret.ID, err = gw.secretTranslator(secret.ID)
				if err != nil {
					return err
				}
			}
			for _, mount := range execOp.ExecOp.GetMounts() {
				if mount.MountType != bksolverpb.MountType_SECRET {
					continue
				}
				secret := mount.SecretOpt
				secret.ID, err = gw.secretTranslator(secret.ID)
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}

		newDef, err := dag.Marshal()
		if err != nil {
			return nil, err
		}
		req.Definition = newDef
	}

	return gw.FrontendLLBBridge.Solve(ctx, req, sid)
}
