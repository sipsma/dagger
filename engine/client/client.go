package client

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
	"time"

	"github.com/Khan/genqlient/graphql"
	"github.com/dagger/dagger/auth"
	"github.com/dagger/dagger/engine/server"
	"github.com/dagger/dagger/telemetry"
	"github.com/docker/cli/cli/config"
	controlapi "github.com/moby/buildkit/api/services/control"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/connhelper"
	"github.com/moby/buildkit/identity"
	bksession "github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/session/grpchijack"
	"github.com/moby/buildkit/session/sshforward"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"github.com/vito/progrock"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const OCIStoreName = "dagger-oci"

// TODO: dumb hack to avoid importing server, fix
const DaggerFrontendName = "dagger.v0"
const SessionIDHeader = "X-Dagger-Session-ID"
const LocalDirExportDestPathMetaKey = "dagger-local-dir-export-dest-path"
const DaggerFrontendSessionIDLabel = "dagger-frontend-session-id"
const daggerFrontendOptsKey = "dagger_frontend_opts"

type SessionParams struct {
	// The id of the frontend server to connect to, or if blank a new one
	// will be started.
	ServerID string

	// TODO: re-add support
	SecretToken string

	// TODO: the difference between these two makes sense if you really think about it, but you shouldn't need to think about it
	RunnerHost string // host of dagger engine runner serving buildkit apis
	DaggerHost string // host of existing dagger graphql server to connect to (optional)
	UserAgent  string

	DisableHostRW bool

	JournalFile        string
	ProgrockWriter     progrock.Writer
	EngineNameCallback func(string)
	CloudURLCallback   func(string)
}

type Session struct {
	SessionParams

	Recorder *progrock.Recorder

	eg             *errgroup.Group
	internalCancel context.CancelFunc

	httpClient *DoerWithHeaders
	bkClient   *bkClient
	bkSession  *bksession.Session
}

func Connect(ctx context.Context, params SessionParams) (_ *Session, rerr error) {
	s := &Session{SessionParams: params}

	if s.ServerID == "" {
		s.ServerID = identity.NewID()
	}

	// TODO: this is only needed temporarily to work around issue w/
	// `dagger do` and `dagger environment` not picking up env set by nesting
	// (which impacts environment tests). Remove ASAP
	if v, ok := os.LookupEnv("_DAGGER_SERVER_ID"); ok {
		s.ServerID = v
	}
	if v, ok := os.LookupEnv("_EXPERIMENTAL_DAGGER_HOST"); ok {
		s.DaggerHost = v
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
		return nil, fmt.Errorf("parse runner host: %w", err)
	}

	bkClient, err := newBuildkitClient(ctx, remote, s.UserAgent)
	if err != nil {
		return nil, fmt.Errorf("new client: %w", err)
	}
	s.bkClient = bkClient
	defer func() {
		if rerr != nil {
			s.bkClient.Close()
		}
	}()

	bkSessionName := identity.NewID() // TODO: does this affect anything?
	sharedKey := ""

	bkSession, err := bksession.NewSession(ctx, bkSessionName, sharedKey)
	if err != nil {
		return nil, fmt.Errorf("new s: %w", err)
	}
	s.bkSession = bkSession
	defer func() {
		if rerr != nil {
			s.bkSession.Close()
		}
	}()

	// filesync
	if !s.DisableHostRW {
		bkSession.Allow(filesync.NewFSSyncProvider(AnyDirSource{}))
		bkSession.Allow(AnyDirTarget{})
	}

	// secrets
	/* TODO: importing core currently causes darwin builds to fail, need to deal with when re-adding secrets
	secretStore := core.NewSecretStore()
	bkSession.Allow(secretsprovider.NewSecretProvider(secretStore))
	// TODO: secretStore.SetGateway(...)
	*/

	// sockets
	bkSession.Allow(MergedSocketProvider{
		// TODO: enforce this in the session stream proxy
		// EnableHostNetworkAccess: !s.DisableHostRW,
	})

	// registry auth
	bkSession.Allow(auth.NewRegistryAuthProvider(config.LoadDefaultConfigFile(os.Stderr)))

	// TODO: more export attachables

	// progress
	progMultiW := progrock.MultiWriter{}
	if s.ProgrockWriter != nil {
		progMultiW = append(progMultiW, s.ProgrockWriter)
	}
	if s.JournalFile != "" {
		fw, err := newProgrockFileWriter(s.JournalFile)
		if err != nil {
			return nil, err
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
	if s.EngineNameCallback != nil && bkClient.EngineName != "" {
		s.EngineNameCallback(bkClient.EngineName)
	}

	bkSession.Allow(progRockAttachable{progMultiW})
	recorder := progrock.NewRecorder(progMultiW)
	s.Recorder = recorder

	solveCh := make(chan *bkclient.SolveStatus)
	s.eg.Go(func() error {
		return RecordBuildkitStatus(recorder, solveCh)
	})

	// run the client s
	s.eg.Go(func() error {
		return bkSession.Run(internalCtx, grpchijack.Dialer(s.bkClient.ControlClient()))
	})

	var allowedEntitlements []entitlements.Entitlement
	if bkClient.PrivilegedExecEnabled {
		// NOTE: this just allows clients to set this if they want. It also needs
		// to be set in the ExecOp LLB and enabled server-side in order for privileged
		// execs to actually run.
		allowedEntitlements = append(allowedEntitlements, entitlements.EntitlementSecurityInsecure)
	}

	frontendOptMap, err := s.FrontendOpts().ToSolveOpts()
	if err != nil {
		return nil, fmt.Errorf("frontend opts: %w", err)
	}

	// start the session server frontend if it's not already running
	solveRef := identity.NewID()
	s.eg.Go(func() error {
		_, err := s.bkClient.ControlClient().Solve(internalCtx, &controlapi.SolveRequest{
			Ref:           solveRef,
			Session:       s.bkClient.DaggerFrontendSessionID,
			Frontend:      DaggerFrontendName,
			FrontendAttrs: frontendOptMap,
			Entitlements:  allowedEntitlements,
			Internal:      true, // disables history recording, which we don't need
			// TODO:
			// Cache: (for upstream remotecache)
		})
		return err
	})

	// connect to the progress stream from buildkit
	// TODO: upstream has a hardcoded 3 second sleep before cancelling this one's context, needed here?
	s.eg.Go(func() error {
		defer close(solveCh)
		stream, err := s.bkClient.ControlClient().Status(internalCtx, &controlapi.StatusRequest{
			Ref: solveRef,
		})
		if err != nil {
			return fmt.Errorf("failed to get status: %w", err)
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return fmt.Errorf("failed to receive status: %w", err)
			}
			solveCh <- bkclient.NewSolveStatus(resp)
		}
	})

	// Try connecting to the session server to make sure it's running
	s.httpClient = &DoerWithHeaders{
		inner: &http.Client{
			Transport: &http.Transport{
				DialContext: s.DialContext,
			},
		},
		headers: http.Header{
			SessionIDHeader: []string{bkSession.ID()},
		},
	}

	maxAttempts := 12
	timePerAttempt := 5 * time.Second
	for i := 0; i < maxAttempts; i++ {
		waitCtx, waitCancel := context.WithTimeout(ctx, timePerAttempt)
		defer waitCancel()
		err := s.Do(waitCtx, `{defaultPlatform}`, "", nil, nil)
		if err == nil {
			return s, nil
		}
		if i == maxAttempts-1 {
			return nil, fmt.Errorf("connect: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect: %w", ctx.Err())
		case <-waitCtx.Done():
		}
	}
	// TODO: cleanup
	panic("unreachable")
}

func (s *Session) FrontendOpts() server.FrontendOpts {
	return server.FrontendOpts{
		ServerID:        s.ServerID,
		ClientSessionID: s.bkSession.ID(),
		// TODO: cache configs
	}
}

func (s *Session) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	host := s.RunnerHost
	if s.DaggerHost != "" {
		host = s.DaggerHost
	}

	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse runner host: %w", err)
	}
	switch u.Scheme {
	case "tcp":
		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(10 * time.Second)
		}
		return net.DialTimeout("tcp", u.Host, time.Until(deadline))
	case "unix":
		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(10 * time.Second)
		}
		return net.DialTimeout("unix", u.Path, time.Until(deadline))
	default:
	}

	queryStrs := u.Query()
	// TODO: queryStrs.Add("addr", s.FrontendOpts().ServerAddr())
	queryStrs.Add("addr", fmt.Sprintf("unix:///run/dagger/server-%s.sock", s.ServerID))
	u.RawQuery = queryStrs.Encode()
	connHelper, err := connhelper.GetConnectionHelper(u.String())
	if err != nil {
		return nil, fmt.Errorf("get connection helper: %w", err)
	}
	if connHelper == nil {
		return nil, fmt.Errorf("unsupported scheme in %s", u.String())
	}
	return connHelper.ContextDialer(ctx, "")
}

func (s *Session) Do(
	ctx context.Context,
	query string,
	opName string,
	variables map[string]any,
	data any,
) error {
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

func (s *Session) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	newReq := &http.Request{
		Method: r.Method,
		URL: &url.URL{
			Scheme: "http",
			Host:   "dagger",
			Path:   r.URL.Path,
		},
		Header: r.Header,
		Body:   r.Body,
		// TODO:
		// Body: io.NopCloser(io.TeeReader(r.Body, os.Stderr)),
	}
	resp, err := s.httpClient.Do(newReq)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("http do: " + err.Error()))
		return
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("io copy: " + err.Error()))
		return
	}
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
	}
}

func (s *Session) Close() error {
	// mark all groups completed
	// close the recorder so the UI exits
	s.Recorder.Complete()
	s.Recorder.Close()

	if s.internalCancel != nil {
		s.internalCancel()
		s.bkSession.Close()
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

// Local dir exports
type AnyDirTarget struct{}

func (t AnyDirTarget) Register(server *grpc.Server) {
	filesync.RegisterFileSendServer(server, t)
}

func (AnyDirTarget) DiffCopy(stream filesync.FileSend_DiffCopyServer) (rerr error) {
	opts, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		return fmt.Errorf("diff copy missing metadata")
	}

	destVal, ok := opts[LocalDirExportDestPathMetaKey]
	if !ok {
		return fmt.Errorf("missing " + LocalDirExportDestPathMetaKey)
	}
	if len(destVal) != 1 {
		return fmt.Errorf("expected exactly one "+LocalDirExportDestPathMetaKey+" value, got %d", len(destVal))
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
	progrock.RegisterProgressServiceServer(srv, progrock.NewRPCReceiver(a.writer))
}

// TODO: duplicate w/ session package to avoid import, fix
// for direct (un-proxied) local dir imports
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

type MergedSocketProvider struct {
	providers []sshforward.SSHServer
	// TODO: enforce this in the session stream proxy
	// EnableHostNetworkAccess bool
}

func (m MergedSocketProvider) Register(server *grpc.Server) {
	sshforward.RegisterSSHServer(server, m)
}

func (m MergedSocketProvider) CheckAgent(ctx context.Context, req *sshforward.CheckAgentRequest) (*sshforward.CheckAgentResponse, error) {
	id := sshforward.DefaultID
	if req.ID != "" {
		id = req.ID
	}
	for _, h := range m.providers {
		resp, err := h.CheckAgent(ctx, req)
		if status.Code(err) == codes.NotFound {
			continue
		}
		return resp, err
	}
	return nil, status.Errorf(codes.NotFound, "no ssh handler for id %s", id)
}

func (m MergedSocketProvider) ForwardAgent(stream sshforward.SSH_ForwardAgentServer) error {
	id := sshforward.DefaultID
	opts, _ := metadata.FromIncomingContext(stream.Context()) // if no metadata continue with empty object
	if v, ok := opts[sshforward.KeySSHID]; ok && len(v) > 0 && v[0] != "" {
		id = v[0]
	}

	for _, h := range m.providers {
		err := h.ForwardAgent(stream)
		if status.Code(err) == codes.NotFound {
			continue
		}
		return err
	}
	return status.Errorf(codes.NotFound, "no ssh handler for id %s", id)
}

type FrontendOpts struct {
	ServerID         string            `json:"server_id,omitempty"`
	ClientSessionID  string            `json:"client_session_id,omitempty"`
	CacheConfigType  string            `json:"cache_config_type,omitempty"`
	CacheConfigAttrs map[string]string `json:"cache_config_attrs,omitempty"`
}

func (f FrontendOpts) ServerAddr() string {
	return fmt.Sprintf("unix://%s", f.ServerSockPath())
}

func (f FrontendOpts) ServerSockPath() string {
	return fmt.Sprintf("/run/dagger/server-%s.sock", f.ServerID)
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
	if f.ServerID == "" {
		return errors.New("missing server id from frontend opts")
	}
	return nil
}

func (f FrontendOpts) ToSolveOpts() (map[string]string, error) {
	if f.ServerID == "" {
		return nil, errors.New("missing server id from frontend opts")
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		daggerFrontendOptsKey: string(b),
	}, nil
}
