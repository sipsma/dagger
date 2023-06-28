package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/core/pipeline"
	"github.com/dagger/dagger/core/schema"
	"github.com/dagger/dagger/router"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/worker"
	"github.com/vito/progrock"
	"golang.org/x/sync/errgroup"
)

const (
	DaggerFrontendName    = "dagger.v0"
	daggerFrontendOptsKey = "dagger_frontend_opts"
)

func NewFrontend(w worker.Worker) frontend.Frontend {
	return &Frontend{
		worker:   w,
		sessions: map[string]*ServerSession{},
	}
}

type Frontend struct {
	worker worker.Worker
	mu     sync.Mutex
	// server session id -> server session
	sessions map[string]*ServerSession
}

type FrontendOpts struct {
	ServerSessionID  string            `json:"id",omitempty`
	CacheConfigType  string            `json:"cache_config_type",omitempty`
	CacheConfigAttrs map[string]string `json:"cache_config_attrs",omitempty`
}

func (f FrontendOpts) ServerAddr() string {
	return fmt.Sprintf("unix://%s", f.ServerSockPath())
}

func (f FrontendOpts) ServerSockPath() string {
	return fmt.Sprintf("/run/dagger/session-%s.sock", f.ServerSessionID)
}

func (f *FrontendOpts) FromSolveOpts(opts map[string]string) error {
	strVal, ok := opts[daggerFrontendOptsKey]
	if !ok {
		return nil
	}
	err := json.Unmarshal([]byte(strVal), f)
	if err != nil {
		return err
	}
	if f.ServerSessionID == "" {
		return errors.New("missing id from frontend opts")
	}
	return nil
}

func (f FrontendOpts) ToSolveOpts() (map[string]string, error) {
	if f.ServerSessionID == "" {
		return nil, errors.New("missing id from frontend opts")
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		daggerFrontendOptsKey: string(b),
	}, nil
}

func (f *Frontend) Solve(
	ctx context.Context,
	llbBridge frontend.FrontendLLBBridge,
	opts map[string]string,
	inputs map[string]*pb.Definition,
	sid string,
	sm *session.Manager,
) (*frontend.Result, error) {
	frontendOpts := &FrontendOpts{}
	if err := frontendOpts.FromSolveOpts(opts); err != nil {
		return nil, err
	}
	if frontendOpts.ServerSessionID == "" {
		return nil, fmt.Errorf("missing id from frontend opts")
	}

	f.mu.Lock()
	sess, ok := f.sessions[frontendOpts.ServerSessionID]
	if !ok {
		sess = &ServerSession{
			FrontendOpts:     frontendOpts,
			llbBridge:        llbBridge,
			worker:           f.worker,
			sessionManager:   sm,
			connectedClients: map[string]session.Caller{},
		}
		f.sessions[frontendOpts.ServerSessionID] = sess
	}
	f.mu.Unlock()

	if err := sess.addClient(ctx, sid); err != nil {
		return nil, err
	}
	defer sess.removeClient(sid)

	// TODO: if no more clients connected, delete ServerSession from map
	return sess.Run(ctx, sid)
}

type ServerSession struct {
	*FrontendOpts
	llbBridge      frontend.FrontendLLBBridge
	worker         worker.Worker
	sessionManager *session.Manager

	startOnce sync.Once
	eg        errgroup.Group
	mu        sync.Mutex
	// client session id -> client session
	connectedClients map[string]session.Caller
}

func (s *ServerSession) Run(ctx context.Context, sessionID string) (*frontend.Result, error) {
	s.startOnce.Do(func() {
		s.eg.Go(func() error {
			clientCaller, ok := s.connectedClients[sessionID]
			if !ok {
				return fmt.Errorf("no client with id %s", sessionID)
			}
			clientConn := clientCaller.Conn()
			progClient := progrock.NewProgressServiceClient(clientConn)
			progUpdates, err := progClient.WriteUpdates(ctx)
			if err != nil {
				return err
			}
			progWriter := progrock.MultiWriter{
				&progrock.RPCWriter{Conn: clientConn, Updates: progUpdates},
				progrockLogrusWriter{},
			}

			// TODO: correct progrock labels
			go pipeline.LoadRootLabels("/", "da-engine")
			// recorder := progrock.NewRecorder(progrockLogrusWriter{}, progrock.WithLabels(labels...))
			recorder := progrock.NewRecorder(progWriter)
			ctx = progrock.RecorderToContext(ctx, recorder)
			defer func() {
				// mark all groups completed
				recorder.Complete()
				// close the recorder so the UI exits
				recorder.Close()
			}()

			// TODO: session token
			rtr := router.New("", recorder)

			coreAPIs := schema.New(schema.InitializeArgs{
				Router: rtr,
				Gateway: core.NewGatewayClient(
					s.llbBridge,
					s.worker,
					s.sessionManager,
					s.CacheConfigType,
					s.CacheConfigAttrs,
				),
				Platform:       s.worker.Platforms(true)[0],
				Worker:         s.worker,
				SessionManager: s.sessionManager,
				/* TODO: re-add
				Auth:     registryAuth,
				Secrets:  secretStore,
				OCIStore: ociStore,
				*/
			})
			if err := rtr.Add(coreAPIs...); err != nil {
				return err
			}

			serverSockPath := s.ServerSockPath()
			if err := os.MkdirAll(filepath.Dir(serverSockPath), 0700); err != nil {
				return err
			}

			// TODO:
			bklog.G(ctx).Debugf("listening on %s", serverSockPath)

			l, err := net.Listen("unix", serverSockPath)
			if err != nil {
				return err
			}
			defer l.Close()
			srv := http.Server{
				Handler:           rtr,
				ReadHeaderTimeout: 30 * time.Second,
			}
			go func() {
				<-ctx.Done()
				l.Close()
			}()
			err = srv.Serve(l)
			// if error is "use of closed network connection", it's from the context being canceled
			if err != nil && !errors.Is(err, net.ErrClosed) {
				return err
			}
			return nil
		})
	})

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- s.eg.Wait()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-waitCh:
		// TODO: re-add support for the combined result needed when upstream caching enabled
		return nil, err
	}
}

func (s *ServerSession) addClient(ctx context.Context, clientSessionID string) error {
	waitForSession := true
	caller, err := s.sessionManager.Get(ctx, clientSessionID, !waitForSession)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connectedClients[clientSessionID] = caller
	return nil
}

func (s *ServerSession) removeClient(clientSessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.connectedClients, clientSessionID)
	// TODO: need to close the caller conn here? or is that already done elsewhere?
	return nil
}

type progrockLogrusWriter struct{}

func (w progrockLogrusWriter) WriteStatus(ev *progrock.StatusUpdate) error {
	l := bklog.G(context.TODO())
	for _, vtx := range ev.Vertexes {
		l = l.WithField("vertex-"+vtx.Id, vtx)
	}
	for _, task := range ev.Tasks {
		l = l.WithField("task-"+task.Vertex, task)
	}
	for _, log := range ev.Logs {
		l = l.WithField("log-"+log.Vertex, log)
	}
	l.Trace()
	return nil
}

func (w progrockLogrusWriter) Close() error {
	return nil
}
