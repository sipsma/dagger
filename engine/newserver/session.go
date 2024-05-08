package newserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/koron-go/prefixw"
	"github.com/moby/buildkit/cache/remotecache"
	bkclient "github.com/moby/buildkit/client"
	bkfrontend "github.com/moby/buildkit/frontend"
	bkgw "github.com/moby/buildkit/frontend/gateway/client"
	bksession "github.com/moby/buildkit/session"
	bksolver "github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/progress/progressui"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/dagger/dagger/analytics"
	"github.com/dagger/dagger/auth"
	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/engine"
	"github.com/dagger/dagger/engine/buildkit"
	"github.com/dagger/dagger/engine/cache"
	"github.com/dagger/dagger/engine/slog"
	"github.com/dagger/dagger/telemetry"
)

type daggerSession struct {
	sessionID          string
	mainClientCallerID string
	traceID            trace.TraceID

	state   daggerSessionState
	stateMu sync.RWMutex

	clients  map[string]*daggerClient // clientID -> client
	clientMu sync.RWMutex

	// the http endpoints being served (as a map since APIs like shellEndpoint can add more)
	endpoints  map[string]http.Handler
	endpointMu sync.RWMutex

	services *core.Services

	analytics analytics.Tracker

	secretStore  *core.SecretStore
	authProvider *auth.RegistryAuthProvider

	cacheExporterCfgs []bkgw.CacheOptionsEntry
	cacheImporterCfgs []bkgw.CacheOptionsEntry

	refs   map[*ref]struct{}
	refsMu sync.RWMutex
}

type daggerSessionState string

const (
	sessionStateUninitialized daggerSessionState = ""
	sessionStateInitialized   daggerSessionState = "initialized"
	sessionStateRemoved       daggerSessionState = "removed"
)

type daggerClient struct {
	daggerSession *daggerSession
	clientID      string
	secretToken   string

	state   daggerClientState
	stateMu sync.RWMutex

	dagqlRoot *core.Query

	// the DAG of modules being served to this client
	deps *core.ModDeps

	// If the client is itself from a function call in a user module, this is set with the
	// metadata of that ongoing function call
	fnCall *core.FunctionCall

	// buildkit job-related state/config
	worker          *worker
	buildkitSession *bksession.Session
	job             *bksolver.Job
	llbSolver       *llbsolver.Solver
	llbBridge       bkfrontend.FrontendLLBBridge
	dialer          *net.Dialer
	spanCtx         trace.SpanContext

	// TODO: this might make more sense in the session state
	// TODO: this might make more sense in the session state
	// TODO: this might make more sense in the session state
	containers   map[bkgw.Container]struct{}
	containersMu sync.Mutex
}

type daggerClientState string

const (
	clientStateUninitialized daggerClientState = ""
	clientStateInitialized   daggerClientState = "initialized"
)

// requires that sess.stateMu is held
func (srv *Server) initializeDaggerSession(
	ctx context.Context,
	clientMetadata *engine.ClientMetadata,
	sess *daggerSession,
	failureCleanups *cleanups,
) error {
	slog.ExtraDebug("initializing new session", "session", sess.sessionID)
	defer slog.ExtraDebug("initialized new session", "session", sess.sessionID)

	sess.sessionID = clientMetadata.ServerID
	sess.mainClientCallerID = clientMetadata.ClientID
	sess.clients = map[string]*daggerClient{}
	sess.endpoints = map[string]http.Handler{}
	sess.services = core.NewServices()
	sess.secretStore = core.NewSecretStore()
	sess.authProvider = auth.NewRegistryAuthProvider()
	sess.refs = map[*ref]struct{}{}

	if traceID := trace.SpanContextFromContext(ctx).TraceID(); traceID.IsValid() {
		sess.traceID = traceID
	} else {
		slog.Warn("invalid traceID", "traceID", traceID.String())
	}

	sess.analytics = analytics.New(analytics.Config{
		DoNotTrack: clientMetadata.DoNotTrack || analytics.DoNotTrack(),
		Labels: clientMetadata.Labels.
			WithEngineLabel(srv.engineName).
			WithServerLabels(
				engine.Version,
				runtime.GOOS,
				runtime.GOARCH,
				srv.solverCache.ID() != cache.LocalCacheID,
			),
		CloudToken: clientMetadata.CloudToken,
	})
	failureCleanups.add(sess.analytics.Close)

	cacheImporterCfgs := make([]bkgw.CacheOptionsEntry, 0, len(clientMetadata.UpstreamCacheImportConfig))
	for _, cacheImportCfg := range clientMetadata.UpstreamCacheImportConfig {
		_, ok := srv.cacheImporters[cacheImportCfg.Type]
		if !ok {
			return fmt.Errorf("unknown cache importer type %q", cacheImportCfg.Type)
		}
		cacheImporterCfgs = append(cacheImporterCfgs, bkgw.CacheOptionsEntry{
			Type:  cacheImportCfg.Type,
			Attrs: cacheImportCfg.Attrs,
		})
	}
	for _, cacheExportCfg := range clientMetadata.UpstreamCacheExportConfig {
		_, ok := srv.cacheExporters[cacheExportCfg.Type]
		if !ok {
			return fmt.Errorf("unknown cache exporter type %q", cacheExportCfg.Type)
		}
		sess.cacheExporterCfgs = append(sess.cacheExporterCfgs, bkgw.CacheOptionsEntry{
			Type:  cacheExportCfg.Type,
			Attrs: cacheExportCfg.Attrs,
		})
	}

	sess.state = sessionStateInitialized
	return nil
}

// requires that sess.stateMu is held
func (srv *Server) removeDaggerSession(ctx context.Context, sess *daggerSession) error {
	slog.ExtraDebug("session closing; stopping client services and flushing",
		"session", sess.sessionID,
		"trace", sess.traceID,
	)
	defer slog.ExtraDebug("session closed",
		"session", sess.sessionID,
		"trace", sess.traceID,
	)

	srv.daggerSessionsMu.Lock()
	delete(srv.daggerSessions, sess.sessionID)
	srv.daggerSessionsMu.Unlock()

	var errs error

	// in theory none of this should block very long, but add a safeguard just in case
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()

	if err := sess.services.StopClientServices(ctx, sess.sessionID); err != nil {
		errs = errors.Join(errs, fmt.Errorf("stop client services: %w", err))
	}

	var clientReleaseGroup errgroup.Group
	for _, client := range sess.clients {
		client := client
		clientReleaseGroup.Go(func() error {
			var errs error
			client.job.Discard()
			client.job.CloseProgress()

			client.containersMu.Lock()
			var containerReleaseGroup errgroup.Group
			for ctr := range client.containers {
				if ctr := ctr; ctr != nil {
					containerReleaseGroup.Go(func() error {
						return ctr.Release(ctx)
					})
				}
			}
			errs = errors.Join(errs, containerReleaseGroup.Wait())
			client.containers = nil
			client.containersMu.Unlock()

			if client.llbSolver != nil {
				errs = errors.Join(errs, client.llbSolver.Close())
				client.llbSolver = nil
			}

			if client.buildkitSession != nil {
				errs = errors.Join(errs, client.buildkitSession.Close())
				client.buildkitSession = nil
			}

			return errs
		})
	}
	errs = errors.Join(errs, clientReleaseGroup.Wait())

	sess.refsMu.Lock()
	var refReleaseGroup errgroup.Group
	for rf := range sess.refs {
		if rf != nil {
			rf := rf
			refReleaseGroup.Go(func() error {
				return rf.resultProxy.Release(ctx)
			})
		}
	}
	errs = errors.Join(errs, refReleaseGroup.Wait())
	sess.refs = nil
	sess.refsMu.Unlock()

	errs = errors.Join(errs, sess.analytics.Close())
	telemetry.Flush(ctx)

	return errs
}

// requires that client.stateMu is held
func (srv *Server) initializeDaggerClient(
	ctx context.Context,
	clientMetadata *engine.ClientMetadata,
	client *daggerClient,
	failureCleanups *cleanups,
) error {
	client.clientID = clientMetadata.ClientID
	client.secretToken = clientMetadata.ClientSecretToken
	client.worker = srv.newWorkerForClient(client)
	client.containers = map[bkgw.Container]struct{}{}

	wc, err := asWorkerController(client.worker)
	if err != nil {
		return err
	}
	client.llbSolver, err = llbsolver.New(llbsolver.Opt{
		WorkerController: wc,
		Frontends:        srv.frontends,
		CacheManager:     srv.solverCache,
		SessionManager:   srv.bkSessionManager,
		CacheResolvers:   srv.cacheImporters,
		Entitlements:     toEntitlementStrings(srv.entitlements),
	})
	if err != nil {
		return fmt.Errorf("failed to create llbsolver: %w", err)
	}
	failureCleanups.add(client.llbSolver.Close)

	client.buildkitSession, err = srv.newBuildkitSession()
	if err != nil {
		return fmt.Errorf("failed to create buildkit session: %w", err)
	}
	failureCleanups.add(client.buildkitSession.Close)

	client.job, err = srv.solver.NewJob(client.buildkitSession.ID())
	if err != nil {
		return fmt.Errorf("failed to create buildkit job: %w", err)
	}
	failureCleanups.add(client.job.Discard)
	failureCleanups.addNoErr(client.job.CloseProgress)

	client.job.SessionID = client.buildkitSession.ID()
	client.job.SetValue(entitlementsJobKey, srv.entitlements)

	// add the worker to the job so the op resolver (currently configured
	// in buildkitcontroller.go) can access and use it for resolving ops
	client.job.SetValue(workerJobKey, client.worker)

	br := client.llbSolver.Bridge(client.job)
	client.llbBridge = br

	client.dialer = &net.Dialer{}
	client.dialer.Resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if len(srv.dns.Nameservers) == 0 {
				return nil, errors.New("no nameservers configured")
			}

			var errs []error
			for _, ns := range srv.dns.Nameservers {
				conn, err := client.dialer.DialContext(ctx, network, net.JoinHostPort(ns, "53"))
				if err != nil {
					errs = append(errs, err)
					continue
				}

				return conn, nil
			}

			return nil, errors.Join(errs...)
		},
	}

	// write progress for extra debugging if configured
	go func() {
		statusCh := make(chan *bkclient.SolveStatus, 8)

		bkLogsW := srv.buildkitLogSink
		if bkLogsW == nil || bkLogsW == io.Discard {
			// flush the chan so nothing is buffered internally unnecessarily
			// TODO: worth checking if this is necessary or if we can just not call job.Status
			go func() {
				for range statusCh {
				}
			}()
		} else {
			prefix := fmt.Sprintf("[buildkit] [trace=%s] [client=%s] ", client.spanCtx.TraceID(), client.clientID)
			bkLogsW = prefixw.New(bkLogsW, prefix)
			statusCh := make(chan *bkclient.SolveStatus, 8)
			pw, err := progressui.NewDisplay(bkLogsW, progressui.PlainMode)
			if err != nil {
				bklog.G(ctx).WithError(err).Error("failed to initialize progress writer")
				return
			}
			go pw.UpdateFrom(ctx, statusCh)
		}

		err = client.job.Status(ctx, statusCh)
		if err != nil && !errors.Is(err, context.Canceled) {
			bklog.G(ctx).WithError(err).Error("failed to write status updates")
		}
	}()

	// TODO: need different behavior for main client caller and others
	// TODO: need different behavior for main client caller and others
	// TODO: need different behavior for main client caller and others

	root := core.NewRoot(srv)

	// TODO:
	// TODO:
	// TODO:
	// TODO:
	/*
		dag := dagql.NewServer(root)

		// stash away the cache so we can share it between other servers
		root.Cache = dag.Cache

		dag.Around(telemetry.AroundFunc)

		coreMod := &schema.CoreMod{Dag: dag}
		root.DefaultDeps = core.NewModDeps(root, []core.Mod{coreMod})
		if err := coreMod.Install(ctx, dag); err != nil {
			return nil, err
		}

		// the main client caller starts out with the core API loaded
		sess.clientCallContext[sess.mainClientCallerID] = &core.ClientCallContext{
			Deps: root.DefaultDeps,
			Root: root,
		}
	*/

	client.state = clientStateInitialized
	return nil
}

func (srv *Server) clientFromContext(ctx context.Context) (*daggerClient, error) {
	clientMetadata, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get client metadata for session call: %w", err)
	}

	srv.daggerSessionsMu.RLock()
	defer srv.daggerSessionsMu.RUnlock()
	sess, ok := srv.daggerSessions[clientMetadata.ServerID]
	if !ok {
		return nil, fmt.Errorf("session %q not found", clientMetadata.ServerID)
	}

	sess.clientMu.RLock()
	defer sess.clientMu.RUnlock()
	client, ok := sess.clients[clientMetadata.ClientID]
	if !ok {
		return nil, fmt.Errorf("client %q not found", clientMetadata.ClientID)
	}

	return client, nil
}

// initialize session+client if needed, return:
// * a context to use for the rest of the call (will be canceled when the session ends)
// * the initialized client
// * a cleanup func to run when the call is done
func (srv *Server) getOrInitClient(
	ctx context.Context,
	clientMetadata *engine.ClientMetadata,
) (_ context.Context, _ *daggerClient, _ func() error, rerr error) {
	sessionID := clientMetadata.ServerID
	if sessionID == "" {
		return nil, nil, nil, fmt.Errorf("session ID is required")
	}
	clientID := clientMetadata.ClientID
	if clientID == "" {
		return nil, nil, nil, fmt.Errorf("client ID is required")
	}
	token := clientMetadata.ClientSecretToken
	if token == "" {
		return nil, nil, nil, fmt.Errorf("client secret token is required")
	}

	// cleanup to do if this method fails
	failureCleanups := &cleanups{}
	defer func() {
		if rerr != nil {
			rerr = errors.Join(rerr, failureCleanups.run())
		}
	}()

	if !clientMetadata.RegisterClient {
		// If this isn't a call to hook up buildkit session attachables, wait for that
		// to happen before continuing. This is important because we only clean up the
		// session once those attachables are disconnected, so if we didn't do this
		// there would be a corner case where a (confused/buggy) client could never
		// hook those up and we'd leak the session forever.
		//
		// This should happen very quickly usually, but to foster a spirit of generosity
		// towards clients under heavy load or similar we give them a while.
		timeoutCtx, timeoutCancel := context.WithTimeout(ctx, 30*time.Second)
		bkCaller, err := srv.bkSessionManager.Get(timeoutCtx, clientMetadata.BuildkitSessionID(), false)
		timeoutCancel()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("get buildkit session: %w", err)
		}

		// the returned context should be cancelled once the buildkit session is done
		bkSessionCtx := bkCaller.Context()
		var cancelAfterSessionDone context.CancelCauseFunc
		ctx, cancelAfterSessionDone = context.WithCancelCause(ctx)
		go func() {
			select {
			case <-ctx.Done():
			case <-bkSessionCtx.Done():
				cancelAfterSessionDone(bkSessionCtx.Err())
			}
		}()
	}

	srv.daggerSessionsMu.Lock()
	sess, sessionExists := srv.daggerSessions[sessionID]
	if !sessionExists {
		sess = &daggerSession{}
		srv.daggerSessions[sessionID] = sess

		failureCleanups.add(func() error {
			srv.daggerSessionsMu.Lock()
			delete(srv.daggerSessions, sessionID)
			srv.daggerSessionsMu.Unlock()
			return nil
		})
	}
	srv.daggerSessionsMu.Unlock()

	sess.stateMu.Lock()
	defer sess.stateMu.Unlock() // TODO: double check if this can be held for less time/that matters at all
	switch sess.state {
	case sessionStateUninitialized:
		if err := srv.initializeDaggerSession(ctx, clientMetadata, sess, failureCleanups); err != nil {
			return nil, nil, nil, fmt.Errorf("initialize session: %w", err)
		}
	case sessionStateInitialized:
		// nothing to do
	case sessionStateRemoved:
		return nil, nil, nil, fmt.Errorf("session %q removed", sess.sessionID)
	}

	sess.clientMu.Lock()
	client, clientExists := sess.clients[clientID]
	if !clientExists {
		client = &daggerClient{daggerSession: sess}
		sess.clients[clientID] = client

		failureCleanups.add(func() error {
			sess.clientMu.Lock()
			delete(sess.clients, clientID)
			sess.clientMu.Unlock()
			return nil
		})
	}
	sess.clientMu.Unlock()

	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	switch client.state {
	case clientStateUninitialized:
		if err := srv.initializeDaggerClient(ctx, clientMetadata, client, failureCleanups); err != nil {
			return nil, nil, nil, fmt.Errorf("initialize client: %w", err)
		}
	case clientStateInitialized:
		// verify token matches existing client
		if token != client.secretToken {
			return nil, nil, nil, fmt.Errorf("client %q already exists with different secret token", clientID)
		}
	}

	return ctx, client, func() error {
		// only need to do any cleanup if this was an initial registration to hook up
		// buildkit session attachables, which is closed when the client is exiting
		if !clientMetadata.RegisterClient {
			return nil
		}

		// unregister the client token
		// TODO: this code can be removed when tokens are pre-registered
		// TODO: this code can be removed when tokens are pre-registered
		// TODO: this code can be removed when tokens are pre-registered
		client.stateMu.Lock()
		client.secretToken = ""
		client.stateMu.Unlock()

		// if the main client caller disconnects, cleanup the whole session too
		if clientID != sess.mainClientCallerID {
			return nil
		}
		sess.stateMu.Lock()
		defer sess.stateMu.Unlock()
		switch sess.state {
		case sessionStateInitialized:
			return srv.removeDaggerSession(ctx, sess)
		default:
			// this should never happen unless there's a bug
			slog.Error("session state being removed not in initialized state",
				"session", sess.sessionID,
				"state", sess.state,
			)
			return nil
		}
	}, nil
}

func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) (rerr error) {
	ctx := r.Context()
	defer func() {
		if rerr != nil {
			bklog.G(ctx).WithError(rerr).Error("failed to serve request")
		}
	}()

	clientMetadata, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get client metadata for session call: %w", err)
	}

	ctx = bklog.WithLogger(ctx, bklog.G(ctx).
		WithField("client_id", clientMetadata.ClientID).
		WithField("client_hostname", clientMetadata.ClientHostname).
		WithField("session_id", clientMetadata.ServerID))

	{
		lg := bklog.G(ctx).WithField("register_client", clientMetadata.RegisterClient)
		lgLevel := lg.Trace
		if clientMetadata.RegisterClient {
			lgLevel = lg.Debug
		}
		lgLevel("handling session call")
		defer func() {
			if rerr != nil {
				lg.WithError(rerr).Errorf("session call failed")
			} else {
				lgLevel("session call done")
			}
		}()
	}

	ctx, client, cleanup, err := srv.getOrInitClient(ctx, clientMetadata)
	if err != nil {
		return fmt.Errorf("update session state: %w", err)
	}
	defer func() {
		err := cleanup()
		if err != nil {
			bklog.G(ctx).WithError(err).Error("cleanup failed")
			rerr = errors.Join(rerr, err)
		}
	}()

	sess := client.daggerSession

	ctx = analytics.WithContext(ctx, sess.analytics)
	// propagate span context from the client (i.e. for Dagger-in-Dagger)
	ctx = propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(r.Header))
	r = r.WithContext(ctx)

	// if this is set, we just need to serve the buildkit session attachables and then return
	if clientMetadata.RegisterClient {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return errors.New("handler does not support hijack")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return fmt.Errorf("hijack connection: %w", err)
		}

		bklog.G(ctx).Trace("session manager handling conn")
		err = srv.bkSessionManager.HandleConn(ctx, conn, clientMetadata.ToGRPCMD())
		bklog.G(ctx).WithError(err).Trace("session manager handle conn done")
		slog.Trace("session manager handle conn done", "err", err, "ctxErr", ctx.Err())
		if err != nil {
			return fmt.Errorf("handleConn: %w", err)
		}

		return nil
	}

	// we are serving our http/graphql API, set all that up

	schema, err := client.deps.Schema(ctx)
	if err != nil {
		return httpErr(http.StatusBadRequest, err)
	}

	gqlSrv := handler.NewDefaultServer(schema)
	// NB: break glass when needed:
	// gqlSrv.AroundResponses(func(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
	// 	res := next(ctx)
	// 	pl, err := json.Marshal(res)
	// 	slog.Debug("graphql response", "response", string(pl), "error", err)
	// 	return res
	// })
	mux := http.NewServeMux()

	mux.Handle("/query", gqlSrv)

	mux.Handle("/shutdown", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()

		immediate := req.URL.Query().Get("immediate") == "true"

		slog := slog.With(
			"isImmediate", immediate,
			"isMainClient", client.clientID == sess.mainClientCallerID,
			"sessionID", sess.sessionID,
			"traceID", sess.traceID,
			"clientID", client.clientID,
			"mainClientID", sess.mainClientCallerID)

		slog.Trace("shutting down server")
		defer slog.Trace("done shutting down server")

		if client.clientID == sess.mainClientCallerID {
			// Stop services, since the main client is going away, and we
			// want the client to see them stop.
			sess.services.StopClientServices(ctx, sess.sessionID)

			// Start draining telemetry
			srv.telemetryPubSub.Drain(sess.traceID, immediate)

			if len(sess.cacheExporterCfgs) > 0 {
				bklog.G(ctx).Debugf("running cache export for client %s", client.clientID)
				type resolveCacheExporterFunc func(ctx context.Context, g bksession.Group) (remotecache.Exporter, error)
				cacheExporterFuncs := make([]resolveCacheExporterFunc, len(sess.cacheExporterCfgs))
				for i, cacheExportCfg := range sess.cacheExporterCfgs {
					cacheExportCfg := cacheExportCfg
					cacheExporterFuncs[i] = func(ctx context.Context, sessionGroup bksession.Group) (remotecache.Exporter, error) {
						exporterFunc, ok := srv.cacheExporters[cacheExportCfg.Type]
						if !ok {
							return nil, fmt.Errorf("unknown cache exporter type %q", cacheExportCfg.Type)
						}
						return exporterFunc(ctx, sessionGroup, cacheExportCfg.Attrs)
					}
				}
				err := bk.UpstreamCacheExport(ctx, cacheExporterFuncs)
				if err != nil {
					bklog.G(ctx).WithError(err).Errorf("error running cache export for client %s", client.clientID)
				}
				bklog.G(ctx).Debugf("done running cache export for client %s", client.clientID)
			}
		}

		telemetry.Flush(ctx)
	}))

	sess.endpointMu.RLock()
	for path, handler := range sess.endpoints {
		mux.Handle(path, handler)
	}
	sess.endpointMu.RUnlock()

	var handler http.Handler = mux
	handler = flushAfterNBytes(buildkit.MaxFileContentsChunkSize)(handler)
	handler.ServeHTTP(w, r)
	return nil
}
