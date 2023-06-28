package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/Khan/genqlient/graphql"
	"github.com/adrg/xdg"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/dagger/dagger/auth"
	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/internal/engine"
	"github.com/dagger/dagger/router"
	"github.com/dagger/dagger/secret"
	"github.com/dagger/dagger/telemetry"
	"github.com/docker/cli/cli/config"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/connhelper"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	sessioncontent "github.com/moby/buildkit/session/content"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/session/grpchijack"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"github.com/vito/progrock"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ClientSession struct {
	ServerSessionID string
	SecretToken     string

	RunnerHost string
	UserAgent  string

	DisableHostRW bool

	// TODO: these are all accepted but ignored atm, re-add support
	JournalFile        string
	ProgrockWriter     progrock.Writer
	EngineNameCallback func(string)
	CloudURLCallback   func(string)

	eg             *errgroup.Group
	internalCancel context.CancelFunc

	httpClient *DoerWithHeaders
	bkClient   *bkclient.Client
}

func (s *ClientSession) Connect(ctx context.Context) (rerr error) {
	if s.ServerSessionID == "" {
		return fmt.Errorf("server session id is empty")
	}
	if s.RunnerHost == "" {
		return fmt.Errorf("must specify runner host")
	}

	internalCtx, internalCancel := context.WithCancel(context.Background())
	defer func() {
		if rerr != nil {
			internalCancel()
		}
	}()
	s.internalCancel = internalCancel
	s.eg, internalCtx = errgroup.WithContext(internalCtx)

	remote, err := url.Parse(s.RunnerHost)
	if err != nil {
		return fmt.Errorf("parse runner host: %w", err)
	}

	internalEngineClient, err := engine.NewClient(ctx, remote, s.UserAgent)
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	s.bkClient = internalEngineClient.BuildkitClient

	bkSessionName := identity.NewID() // TODO: does this affect anything?
	sharedKey := ""

	bkSession, err := session.NewSession(ctx, bkSessionName, sharedKey)
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}

	// filesync
	if !s.DisableHostRW {
		bkSession.Allow(filesync.NewFSSyncProvider(AnyDirSource{}))
		bkSession.Allow(AnyDirTarget{})
	}

	// secrets
	secretStore := secret.NewStore()
	bkSession.Allow(secretsprovider.NewSecretProvider(secretStore))
	// TODO: secretStore.SetGatewayAndSession(...)

	// sockets
	bkSession.Allow(SocketProvider{
		EnableHostNetworkAccess: !s.DisableHostRW,
	})

	// registry auth
	bkSession.Allow(auth.NewRegistryAuthProvider(config.LoadDefaultConfigFile(os.Stderr)))

	// oci stores
	ociStoreDir := filepath.Join(xdg.CacheHome, "dagger", "oci")
	ociStore, err := local.NewStore(ociStoreDir)
	if err != nil {
		return fmt.Errorf("new local oci store: %w", err)
	}
	bkSession.Allow(sessioncontent.NewAttachable(map[string]content.Store{
		// the "oci:" prefix is actually interpreted by buildkit, not just for show
		"oci:" + core.OCIStoreName: ociStore,
	}))

	// TODO: more export attachables

	// progress
	progMultiW := progrock.MultiWriter{}
	if s.ProgrockWriter != nil {
		progMultiW = append(progMultiW, s.ProgrockWriter)
	}
	if s.JournalFile != "" {
		fw, err := newProgrockFileWriter(s.JournalFile)
		if err != nil {
			return err
		}

		progMultiW = append(progMultiW, fw)
	}

	tel := telemetry.New()
	var cloudURL string
	if tel.Enabled() {
		cloudURL = tel.URL()
		progMultiW = append(progMultiW, telemetry.NewWriter(tel))
	}
	if s.CloudURLCallback != nil && cloudURL != "" {
		s.CloudURLCallback(cloudURL)
	}
	if s.EngineNameCallback != nil && internalEngineClient.EngineName != "" {
		s.EngineNameCallback(internalEngineClient.EngineName)
	}

	// forward updates from the session server too
	bkSession.Allow(progRockAttachable{progMultiW})
	recorder := progrock.NewRecorder(progMultiW)
	defer func() {
		// mark all groups completed
		recorder.Complete()
		// close the recorder so the UI exits
		recorder.Close()
	}()

	solveCh := make(chan *bkclient.SolveStatus)
	s.eg.Go(func() error {
		for ev := range solveCh {
			if err := recorder.Record(bk2progrock(ev)); err != nil {
				return fmt.Errorf("record: %w", err)
			}
		}
		return nil
	})

	// run the client session
	s.eg.Go(func() error {
		return bkSession.Run(internalCtx, grpchijack.Dialer(s.bkClient.ControlClient()))
	})

	var allowedEntitlements []entitlements.Entitlement
	if internalEngineClient.PrivilegedExecEnabled {
		// NOTE: this just allows clients to set this if they want. It also needs
		// to be set in the ExecOp LLB and enabled server-side in order for privileged
		// execs to actually run.
		allowedEntitlements = append(allowedEntitlements, entitlements.EntitlementSecurityInsecure)
	}

	frontendOptMap, err := s.FrontendOpts().ToSolveOpts()
	if err != nil {
		return fmt.Errorf("frontend opts: %w", err)
	}

	// start the session server frontend if it's not already running
	s.eg.Go(func() error {
		_, err := s.bkClient.Solve(internalCtx, nil, bkclient.SolveOpt{
			Frontend:              DaggerFrontendName,
			FrontendAttrs:         frontendOptMap,
			AllowedEntitlements:   allowedEntitlements,
			SharedSession:         bkSession,
			SessionPreInitialized: true,
			Internal:              true, // disables history recording, which we don't need
			// TODO:
			// CacheExports:
			// CacheImports:
		}, solveCh)
		return err
	})

	// Try connecting to the session server to make sure it's running
	s.httpClient = &DoerWithHeaders{
		inner: &http.Client{
			Transport: &http.Transport{
				DialContext: s.DialContext,
			},
		},
		headers: http.Header{
			router.SessionIDHeader: []string{bkSession.ID()},
		},
	}

	maxAttempts := 12
	timePerAttempt := 5 * time.Second
	for i := 0; i < maxAttempts; i++ {
		waitCtx, waitCancel := context.WithTimeout(ctx, timePerAttempt)
		defer waitCancel()
		err := s.Do(waitCtx, `{defaultPlatform}`, "", nil, nil)
		if err == nil {
			return nil
		}
		if i == maxAttempts-1 {
			s.httpClient = nil
			return fmt.Errorf("connect: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("connect: %w", ctx.Err())
		case <-waitCtx.Done():
		}
	}
	// TODO: cleanup
	panic("unreachable")
}

func (s *ClientSession) FrontendOpts() FrontendOpts {
	return FrontendOpts{
		ServerSessionID: s.ServerSessionID,
		// TODO: cache configs
	}
}

func (s *ClientSession) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	remote, err := url.Parse(s.RunnerHost)
	if err != nil {
		return nil, fmt.Errorf("parse runner host: %w", err)
	}

	queryStrs := remote.Query()
	queryStrs.Add("addr", s.FrontendOpts().ServerAddr())
	remote.RawQuery = queryStrs.Encode()
	connHelper, err := connhelper.GetConnectionHelper(remote.String())
	if err != nil {
		return nil, fmt.Errorf("get connection helper: %w", err)
	}

	return connHelper.ContextDialer(ctx, "")
}

func (s *ClientSession) Do(
	ctx context.Context,
	query string,
	opName string,
	variables map[string]any,
	data any,
) error {
	if s.httpClient == nil {
		return fmt.Errorf("session not connected")
	}
	gqlClient := graphql.NewClient("http://dagger/query", s.httpClient)

	req := &graphql.Request{
		Query:     query,
		Variables: variables,
		OpName:    opName,
	}
	resp := &graphql.Response{}

	err := gqlClient.MakeRequest(ctx, req, resp)
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}
	if resp.Errors != nil {
		errs := make([]error, len(resp.Errors))
		for i, err := range resp.Errors {
			errs[i] = err
		}
		return errors.Join(errs...)
	}

	if data != nil {
		dataBytes, err := json.Marshal(resp.Data)
		if err != nil {
			return fmt.Errorf("marshal data: %w", err)
		}
		err = json.Unmarshal(dataBytes, data)
		if err != nil {
			return fmt.Errorf("unmarshal data: %w", err)
		}
	}
	return nil
}

func (s *ClientSession) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.httpClient == nil {
		panic("session not connected")
	}
	newReq := &http.Request{
		Method: r.Method,
		URL: &url.URL{
			Scheme: "http",
			Host:   "dagger",
			Path:   r.URL.Path,
		},
		Header: r.Header,
		Body:   r.Body,
	}
	resp, err := s.httpClient.Do(newReq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("http do: " + err.Error()))
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("io copy: " + err.Error()))
		return
	}
}

func (s *ClientSession) Close() error {
	if s.internalCancel != nil {
		s.internalCancel()
		s.bkClient.Close()
		return s.eg.Wait()
	}
	return nil
}

type DoerWithHeaders struct {
	inner   *http.Client
	headers http.Header
}

func (c DoerWithHeaders) Do(req *http.Request) (*http.Response, error) {
	for k, v := range c.headers {
		req.Header[k] = v
	}
	return c.inner.Do(req)
}

type AnyDirSource struct{}

func (AnyDirSource) LookupDir(name string) (filesync.SyncedDir, bool) {
	return filesync.SyncedDir{
		Dir: name,
		Map: func(p string, st *fstypes.Stat) fsutil.MapResult {
			st.Uid = 0
			st.Gid = 0
			return fsutil.MapResultKeep
		},
	}, true
}

type AnyDirTarget struct{}

func (t AnyDirTarget) Register(server *grpc.Server) {
	filesync.RegisterFileSendServer(server, t)
}

func (AnyDirTarget) DiffCopy(stream filesync.FileSend_DiffCopyServer) error {
	opts, _ := metadata.FromIncomingContext(stream.Context()) // if no metadata continue with empty object

	// TODO: use const, import from other package
	destVal, ok := opts["x-dagger-local-export-dest"]
	if !ok {
		return fmt.Errorf("missing x-dagger-local-export-dest")
	}
	if len(destVal) != 1 {
		return fmt.Errorf("expected exactly one x-dagger-local-export-dest")
	}
	dest := destVal[0]

	if err := os.MkdirAll(dest, 0700); err != nil {
		return fmt.Errorf("failed to create synctarget dest dir %s: %w", dest, err)
	}
	return fsutil.Receive(stream.Context(), stream, dest, fsutil.ReceiveOpt{
		Merge: true,
		Filter: func() func(string, *fstypes.Stat) bool {
			uid := os.Getuid()
			gid := os.Getgid()
			return func(p string, st *fstypes.Stat) bool {
				st.Uid = uint32(uid)
				st.Gid = uint32(gid)
				return true
			}
		}(),
	})
}

type progRockAttachable struct {
	writer progrock.Writer
}

func (a progRockAttachable) Register(srv *grpc.Server) {
	progrock.RegisterProgressServiceServer(srv, &RPCReceiver{w: a.writer})
}

// TODO: if this is used, upstream to progrock, just need a public Writer field
type RPCReceiver struct {
	w progrock.Writer

	progrock.UnimplementedProgressServiceServer
}

func (recv *RPCReceiver) WriteUpdates(srv progrock.ProgressService_WriteUpdatesServer) error {
	for {
		update, err := srv.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return srv.SendAndClose(&emptypb.Empty{})
			}
			return err
		}
		if err := recv.w.WriteStatus(update); err != nil {
			return err
		}
	}
}
