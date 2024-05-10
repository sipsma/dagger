package newserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/defaults"
	"github.com/containerd/containerd/diff/apply"
	"github.com/containerd/containerd/diff/walking"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/remotes/docker"
	ctdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/containerd/go-runc"
	controlapi "github.com/moby/buildkit/api/services/control"
	apitypes "github.com/moby/buildkit/api/types"
	bkcache "github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/cache/remotecache"
	"github.com/moby/buildkit/cache/remotecache/azblob"
	"github.com/moby/buildkit/cache/remotecache/gha"
	inlineremotecache "github.com/moby/buildkit/cache/remotecache/inline"
	localremotecache "github.com/moby/buildkit/cache/remotecache/local"
	registryremotecache "github.com/moby/buildkit/cache/remotecache/registry"
	s3remotecache "github.com/moby/buildkit/cache/remotecache/s3"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/frontend"
	dockerfile "github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/frontend/gateway"
	"github.com/moby/buildkit/frontend/gateway/forwarder"
	bksession "github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/grpchijack"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/bboltcachestorage"
	"github.com/moby/buildkit/solver/llbsolver/mounts"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	srcgit "github.com/moby/buildkit/source/git"
	srchttp "github.com/moby/buildkit/source/http"
	"github.com/moby/buildkit/util/archutil"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/imageutil"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/network"
	"github.com/moby/buildkit/util/network/cniprovider"
	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/util/resolver"
	"github.com/moby/buildkit/util/throttle"
	"github.com/moby/buildkit/util/winlayers"
	"github.com/moby/buildkit/version"
	bkworker "github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/base"
	wlabel "github.com/moby/buildkit/worker/label"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
	logsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc"

	"github.com/dagger/dagger/engine"
	daggercache "github.com/dagger/dagger/engine/cache"
	"github.com/dagger/dagger/engine/slog"
	"github.com/dagger/dagger/engine/sources/blob"
	"github.com/dagger/dagger/engine/sources/gitdns"
	"github.com/dagger/dagger/engine/sources/httpdns"
	"github.com/dagger/dagger/telemetry"
)

const (
	daggerCacheServiceURL = "https://api.dagger.cloud/magicache"

	workerJobKey = "dagger.worker"

	// from buildkit, cannot change
	entitlementsJobKey = "llb.entitlements"
)

// TODO: CONSIDER RENAMING THIS TO ENGINE SINCE ITS MORE THAN JUST A SERVER
// TODO: CONSIDER RENAMING THIS TO ENGINE SINCE ITS MORE THAN JUST A SERVER
// TODO: CONSIDER RENAMING THIS TO ENGINE SINCE ITS MORE THAN JUST A SERVER
// TODO: CONSIDER RENAMING THIS TO ENGINE SINCE ITS MORE THAN JUST A SERVER
// TODO: CONSIDER RENAMING THIS TO ENGINE SINCE ITS MORE THAN JUST A SERVER
// TODO: CONSIDER RENAMING THIS TO ENGINE SINCE ITS MORE THAN JUST A SERVER
type Server struct {
	engineName string

	//
	// session+client state
	//

	httpSrv *httpServer

	daggerSessions   map[string]*daggerSession // session id -> session state
	daggerSessionsMu sync.RWMutex

	//
	// state directory/db paths
	//

	rootDir           string
	solverCacheDBPath string

	workerRootDir         string
	snapshotterRootDir    string
	contentStoreRootDir   string
	containerdMetaDBPath  string
	workerCacheMetaDBPath string
	buildkitMountPoolDir  string
	executorRootDir       string

	//
	// buildkit+containerd entities/DBs
	//

	baseWorker          *base.Worker
	workerCacheMetaDB   *metadata.Store
	workerCache         bkcache.Manager
	workerSourceManager *source.Manager

	bkSessionManager *bksession.Manager

	solver        *solver.Solver
	solverCacheDB *bboltcachestorage.Store
	solverCache   daggercache.Manager

	containerdMetaBoltDB *bolt.DB
	containerdMetaDB     *ctdmetadata.DB
	localContentStore    content.Store
	contentStore         *containerdsnapshot.Store

	snapshotter     ctdsnapshot.Snapshotter
	snapshotterName string
	leaseManager    *leaseutil.Manager

	frontends map[string]frontend.Frontend

	cacheExporters map[string]remotecache.ResolveCacheExporterFunc
	cacheImporters map[string]remotecache.ResolveCacheImporterFunc

	//
	// worker/executor-specific config+state
	//

	runc             *runc.Runc
	cgroupParent     string
	networkProviders map[pb.NetMode]network.Provider
	processMode      oci.ProcessMode
	dns              *oci.DNSConfig
	apparmorProfile  string
	selinux          bool
	tracingSocket    string
	entitlements     entitlements.Set
	parallelismSem   *semaphore.Weighted
	enabledPlatforms []ocispecs.Platform
	registryHosts    docker.RegistryHosts

	running    map[string]chan error
	executorMu sync.Mutex

	//
	// telemetry config+state
	//

	telemetryPubSub *telemetry.PubSub
	buildkitLogSink io.Writer

	//
	// gc related
	//
	throttledGC func()
	gcmu        sync.Mutex
}

type NewServerOpts struct {
	EngineConfig *config.Config
	EngineName   string

	TelemetryPubSub *telemetry.PubSub
	TraceSocket     string
}

func NewServer(ctx context.Context, opts *NewServerOpts) (*Server, error) {
	cfg := opts.EngineConfig
	ociCfg := cfg.Workers.OCI

	srv := &Server{
		engineName: opts.EngineName,

		daggerSessions: map[string]*daggerSession{},

		rootDir: cfg.Root,

		frontends: map[string]frontend.Frontend{},

		cgroupParent:    ociCfg.DefaultCgroupParent,
		processMode:     oci.ProcessSandbox,
		running:         make(map[string]chan error),
		apparmorProfile: ociCfg.ApparmorProfile,
		selinux:         ociCfg.SELinux,
		tracingSocket:   opts.TraceSocket,
		entitlements:    entitlements.Set{},
		dns: &oci.DNSConfig{
			Nameservers:   cfg.DNS.Nameservers,
			Options:       cfg.DNS.Options,
			SearchDomains: cfg.DNS.SearchDomains,
		},

		telemetryPubSub: opts.TelemetryPubSub,
	}
	srv.httpSrv = newHTTPServer(ctx, srv.ServeHTTP)

	//
	// setup directories and paths
	//

	var err error
	srv.rootDir, err = filepath.Abs(srv.rootDir)
	if err != nil {
		return nil, err
	}
	srv.rootDir, err = filepath.EvalSymlinks(srv.rootDir)
	if err != nil {
		return nil, err
	}
	srv.solverCacheDBPath = filepath.Join(srv.rootDir, "cache.db")

	srv.workerRootDir = filepath.Join(srv.rootDir, "worker")
	if err := os.MkdirAll(srv.workerRootDir, 0700); err != nil {
		return nil, err
	}
	srv.snapshotterRootDir = filepath.Join(srv.workerRootDir, "snapshots")
	srv.contentStoreRootDir = filepath.Join(srv.workerRootDir, "content")
	srv.containerdMetaDBPath = filepath.Join(srv.workerRootDir, "containerdmeta.db")
	srv.workerCacheMetaDBPath = filepath.Join(srv.workerRootDir, "metadata_v2.db")
	srv.buildkitMountPoolDir = filepath.Join(srv.workerRootDir, "cachemounts")

	srv.executorRootDir = filepath.Join(srv.workerRootDir, "executor")
	if err := os.MkdirAll(srv.executorRootDir, 0o711); err != nil {
		return nil, err
	}
	// clean up old hosts/resolv.conf file. ignore errors
	os.RemoveAll(filepath.Join(srv.executorRootDir, "hosts"))
	os.RemoveAll(filepath.Join(srv.executorRootDir, "resolv.conf"))

	//
	// setup config derived from engine config
	//

	for _, entStr := range cfg.Entitlements {
		ent, err := entitlements.Parse(entStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse entitlement %s: %w", entStr, err)
		}
		srv.entitlements[ent] = struct{}{}
	}

	if platformsStr := ociCfg.Platforms; len(platformsStr) != 0 {
		var err error
		srv.enabledPlatforms, err = parsePlatforms(platformsStr)
		if err != nil {
			return nil, fmt.Errorf("invalid platforms: %w", err)
		}
	}
	if len(srv.enabledPlatforms) == 0 {
		srv.enabledPlatforms = []ocispecs.Platform{platforms.Normalize(platforms.DefaultSpec())}
	}

	srv.registryHosts = resolver.NewRegistryConfig(cfg.Registries)

	if slog.Default().Enabled(ctx, slog.LevelExtraDebug) {
		srv.buildkitLogSink = os.Stderr
	}

	//
	// setup various buildkit/containerd entities and DBs
	//

	srv.bkSessionManager, err = bksession.NewManager()
	if err != nil {
		return nil, err
	}

	srv.snapshotter, srv.snapshotterName, err = newSnapshotter(srv.snapshotterRootDir, ociCfg, srv.bkSessionManager, srv.registryHosts)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshotter: %w", err)
	}

	srv.localContentStore, err = local.NewStore(srv.contentStoreRootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create content store: %w", err)
	}

	srv.containerdMetaBoltDB, err = bolt.Open(srv.containerdMetaDBPath, 0644, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to open metadata db: %w", err)
	}

	srv.containerdMetaDB = ctdmetadata.NewDB(srv.containerdMetaBoltDB, srv.localContentStore, map[string]ctdsnapshot.Snapshotter{
		srv.snapshotterName: srv.snapshotter,
	})
	if err := srv.containerdMetaDB.Init(context.TODO()); err != nil {
		return nil, fmt.Errorf("failed to init metadata db: %w", err)
	}

	srv.leaseManager = leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(srv.containerdMetaDB), "buildkit")
	srv.workerCacheMetaDB, err = metadata.NewStore(srv.workerCacheMetaDBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create metadata store: %w", err)
	}

	srv.contentStore = containerdsnapshot.NewContentStore(srv.containerdMetaDB.ContentStore(), "buildkit")

	//
	// setup worker+executor
	//

	srv.runc = &runc.Runc{
		Command:      "/usr/local/bin/dagger-shim",
		Log:          filepath.Join(srv.executorRootDir, "runc-log.json"),
		LogFormat:    runc.JSON,
		Setpgid:      true,
		PdeathSignal: syscall.SIGKILL,
	}

	var npResolvedMode string
	srv.networkProviders, npResolvedMode, err = netproviders.Providers(netproviders.Opt{
		Mode: cfg.Workers.OCI.NetworkConfig.Mode,
		CNI: cniprovider.Opt{
			Root:       srv.rootDir,
			ConfigPath: cfg.Workers.OCI.CNIConfigPath,
			BinaryDir:  cfg.Workers.OCI.CNIBinaryPath,
			PoolSize:   cfg.Workers.OCI.CNIPoolSize,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create network providers: %w", err)
	}

	if ociCfg.MaxParallelism > 0 {
		srv.parallelismSem = semaphore.NewWeighted(int64(ociCfg.MaxParallelism))
		ociCfg.Labels["maxParallelism"] = strconv.Itoa(ociCfg.MaxParallelism)
	}

	baseLabels := map[string]string{
		wlabel.Executor:       "oci",
		wlabel.Snapshotter:    srv.snapshotterName,
		wlabel.Network:        npResolvedMode,
		wlabel.OCIProcessMode: srv.processMode.String(),
		wlabel.SELinuxEnabled: strconv.FormatBool(ociCfg.SELinux),
	}
	if ociCfg.ApparmorProfile != "" {
		baseLabels[wlabel.ApparmorProfile] = ociCfg.ApparmorProfile
	}
	if hostname, err := os.Hostname(); err != nil {
		baseLabels[wlabel.Hostname] = "unknown"
	} else {
		baseLabels[wlabel.Hostname] = hostname
	}
	for k, v := range ociCfg.Labels {
		baseLabels[k] = v
	}
	workerID, err := base.ID(srv.workerRootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get worker ID: %w", err)
	}

	srv.baseWorker, err = base.NewWorker(ctx, base.WorkerOpt{
		ID:        workerID,
		Labels:    baseLabels,
		Platforms: srv.enabledPlatforms,
		GCPolicy:  getGCPolicy(ociCfg.GCConfig, srv.rootDir),
		BuildkitVersion: bkclient.BuildkitVersion{
			Package:  version.Package,
			Version:  version.Version,
			Revision: version.Revision,
		},
		NetworkProviders: srv.networkProviders,
		Executor:         nil, // not needed yet, set in clientWorker
		Snapshotter: containerdsnapshot.NewSnapshotter(
			srv.snapshotterName,
			srv.containerdMetaDB.Snapshotter(srv.snapshotterName),
			"buildkit",
			nil, // no idmapping
		),
		ContentStore:    srv.contentStore,
		Applier:         winlayers.NewFileSystemApplierWithWindows(srv.contentStore, apply.NewFileSystemApplier(srv.contentStore)),
		Differ:          winlayers.NewWalkingDiffWithWindows(srv.contentStore, walking.NewWalkingDiff(srv.contentStore)),
		ImageStore:      nil, // explicitly, because that's what upstream does too
		RegistryHosts:   srv.registryHosts,
		IdentityMapping: nil, // no idmapping
		LeaseManager:    srv.leaseManager,
		GarbageCollect:  srv.containerdMetaDB.GarbageCollect,
		ParallelismSem:  srv.parallelismSem,
		MetadataStore:   srv.workerCacheMetaDB,
		MountPoolRoot:   srv.buildkitMountPoolDir,
		ResourceMonitor: nil, // we don't use it
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create base worker: %w", err)
	}
	srv.workerCache = srv.baseWorker.CacheMgr
	srv.workerSourceManager = srv.baseWorker.SourceManager

	logrus.Infof("found worker %q, labels=%v, platforms=%v", workerID, baseLabels, formatPlatforms(srv.enabledPlatforms))
	archutil.WarnIfUnsupported(srv.enabledPlatforms)

	// registerDaggerCustomSources adds Dagger's custom sources to the worker.
	hs, err := httpdns.NewSource(httpdns.Opt{
		Opt: srchttp.Opt{
			CacheAccessor: srv.workerCache,
		},
		BaseDNSConfig: srv.dns,
	})
	if err != nil {
		return nil, err
	}
	srv.workerSourceManager.Register(hs)

	gs, err := gitdns.NewSource(gitdns.Opt{
		Opt: srcgit.Opt{
			CacheAccessor: srv.workerCache,
		},
		BaseDNSConfig: srv.dns,
	})
	if err != nil {
		return nil, err
	}
	srv.workerSourceManager.Register(gs)

	bs, err := blob.NewSource(blob.Opt{
		CacheAccessor: srv.workerCache,
	})
	if err != nil {
		return nil, err
	}
	srv.workerSourceManager.Register(bs)

	//
	// setup solver
	//

	baseWorkerController, err := asWorkerController(srv.baseWorker)
	if err != nil {
		return nil, fmt.Errorf("failed to create worker controller: %w", err)
	}
	srv.frontends["dockerfile.v0"] = forwarder.NewGatewayForwarder(baseWorkerController.Infos(), dockerfile.Build)
	srv.frontends["gateway.v0"] = gateway.NewGatewayFrontend(baseWorkerController.Infos())

	srv.solverCacheDB, err = bboltcachestorage.NewStore(srv.solverCacheDBPath)
	if err != nil {
		return nil, err
	}

	cacheServiceURL := os.Getenv("_EXPERIMENTAL_DAGGER_CACHESERVICE_URL")
	cacheServiceToken := os.Getenv("_EXPERIMENTAL_DAGGER_CACHESERVICE_TOKEN")
	// add DAGGER_CLOUD_TOKEN in a backwards compat way.
	// TODO: deprecate in a future release
	if v, ok := os.LookupEnv("DAGGER_CLOUD_TOKEN"); ok {
		cacheServiceToken = v
	}

	if cacheServiceURL == "" {
		cacheServiceURL = daggerCacheServiceURL
	}
	srv.solverCache, err = daggercache.NewManager(ctx, daggercache.ManagerConfig{
		KeyStore:     srv.solverCacheDB,
		ResultStore:  bkworker.NewCacheResultStorage(baseWorkerController),
		Worker:       srv.baseWorker,
		MountManager: mounts.NewMountManager("dagger-cache", srv.workerCache, srv.bkSessionManager),
		ServiceURL:   cacheServiceURL,
		Token:        cacheServiceToken,
		EngineID:     opts.EngineName,
	})
	if err != nil {
		return nil, err
	}

	srv.cacheExporters = map[string]remotecache.ResolveCacheExporterFunc{
		"registry": registryremotecache.ResolveCacheExporterFunc(srv.bkSessionManager, srv.registryHosts),
		"local":    localremotecache.ResolveCacheExporterFunc(srv.bkSessionManager),
		"inline":   inlineremotecache.ResolveCacheExporterFunc(),
		"gha":      gha.ResolveCacheExporterFunc(),
		"s3":       s3remotecache.ResolveCacheExporterFunc(),
		"azblob":   azblob.ResolveCacheExporterFunc(),
	}
	srv.cacheImporters = map[string]remotecache.ResolveCacheImporterFunc{
		"registry": registryremotecache.ResolveCacheImporterFunc(srv.bkSessionManager, srv.contentStore, srv.registryHosts),
		"local":    localremotecache.ResolveCacheImporterFunc(srv.bkSessionManager),
		"gha":      gha.ResolveCacheImporterFunc(),
		"s3":       s3remotecache.ResolveCacheImporterFunc(),
		"azblob":   azblob.ResolveCacheImporterFunc(),
	}

	srv.solver = solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: func(vtx solver.Vertex, builder solver.Builder) (solver.Op, error) {
			return srv.worker.ResolveOp(vtx, nil, srv.bkSessionManager)
		},
		DefaultCache: srv.solverCache,
	})

	srv.throttledGC = throttle.After(time.Minute, srv.gc)
	defer func() {
		time.AfterFunc(time.Second, srv.throttledGC)
	}()

	return srv, nil
}

func (srv *Server) Close() error {
	err := srv.baseWorker.Close()

	// note this *could* cause a panic in Session if it was still running, so
	// the server should be shutdown first
	srv.daggerSessionsMu.Lock()
	daggerSessions := srv.daggerSessions
	srv.daggerSessions = nil
	srv.daggerSessionsMu.Unlock()

	for _, s := range daggerSessions {
		s.Close(context.Background())
	}
	return err
}

func (srv *Server) Session(stream controlapi.Control_SessionServer) (rerr error) {
	defer func() {
		// a panic would indicate a bug, but we don't want to take down the entire server
		if err := recover(); err != nil {
			bklog.G(context.Background()).WithError(fmt.Errorf("%v", err)).Errorf("panic in session call")
			debug.PrintStack()
			rerr = fmt.Errorf("panic in session call, please report a bug: %v %s", err, string(debug.Stack()))
		}
	}()

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	bklog.G(ctx).Trace("serve client conn")
	defer bklog.G(ctx).Trace("done serving client conn")

	conn, _, _ := grpchijack.Hijack(stream)
	clientConn := newGRPCClientConn(ctx, conn, defaults.DefaultMaxSendMsgSize*95/100)

	// this ends up calling Server.ServeHTTP in session.go, in case you are following along at home
	err := srv.httpSrv.ServeConn(ctx, clientConn)
	switch {
	case err == nil, errors.Is(err, io.ErrClosedPipe), errors.Is(err, context.Canceled):
		return nil
	default:
		return fmt.Errorf("serve conn: %w", err)
	}
}

func (srv *Server) DiskUsage(ctx context.Context, r *controlapi.DiskUsageRequest) (*controlapi.DiskUsageResponse, error) {
	resp := &controlapi.DiskUsageResponse{}
	du, err := srv.baseWorker.DiskUsage(ctx, bkclient.DiskUsageInfo{
		Filter: r.Filter,
	})
	if err != nil {
		return nil, err
	}
	for _, r := range du {
		resp.Record = append(resp.Record, &controlapi.UsageRecord{
			ID:          r.ID,
			Mutable:     r.Mutable,
			InUse:       r.InUse,
			Size_:       r.Size,
			Parents:     r.Parents,
			UsageCount:  int64(r.UsageCount),
			Description: r.Description,
			CreatedAt:   r.CreatedAt,
			LastUsedAt:  r.LastUsedAt,
			RecordType:  string(r.RecordType),
			Shared:      r.Shared,
		})
	}
	return resp, nil
}

func (srv *Server) Prune(req *controlapi.PruneRequest, stream controlapi.Control_PruneServer) error {
	eg, ctx := errgroup.WithContext(stream.Context())

	srv.daggerSessionsMu.RLock()
	cancelLeases := len(srv.daggerSessions) == 0
	srv.daggerSessionsMu.RUnlock()
	if cancelLeases {
		imageutil.CancelCacheLeases()
	}

	didPrune := false
	defer func() {
		if didPrune {
			if srv, ok := srv.solverCache.(interface {
				ReleaseUnreferenced(context.Context) error
			}); ok {
				if err := srv.ReleaseUnreferenced(ctx); err != nil {
					bklog.G(ctx).Errorf("failed to release cache metadata: %+v", err)
				}
			}
		}
	}()

	ch := make(chan bkclient.UsageInfo, 32)

	eg.Go(func() error {
		defer close(ch)
		return srv.baseWorker.Prune(ctx, ch, bkclient.PruneInfo{
			Filter:       req.Filter,
			All:          req.All,
			KeepDuration: time.Duration(req.KeepDuration),
			KeepBytes:    req.KeepBytes,
		})
	})

	eg.Go(func() error {
		defer func() {
			// drain channel on error
			for range ch {
			}
		}()
		for r := range ch {
			didPrune = true
			if err := stream.Send(&controlapi.UsageRecord{
				ID:          r.ID,
				Mutable:     r.Mutable,
				InUse:       r.InUse,
				Size_:       r.Size,
				Parents:     r.Parents,
				UsageCount:  int64(r.UsageCount),
				Description: r.Description,
				CreatedAt:   r.CreatedAt,
				LastUsedAt:  r.LastUsedAt,
				RecordType:  string(r.RecordType),
				Shared:      r.Shared,
			}); err != nil {
				return err
			}
		}
		return nil
	})

	return eg.Wait()
}

func (srv *Server) gc() {
	srv.gcmu.Lock()
	defer srv.gcmu.Unlock()

	ch := make(chan bkclient.UsageInfo)
	eg, ctx := errgroup.WithContext(context.TODO())

	var size int64
	eg.Go(func() error {
		for ui := range ch {
			size += ui.Size
		}
		return nil
	})

	eg.Go(func() error {
		defer close(ch)
		if policy := srv.baseWorker.GCPolicy(); len(policy) > 0 {
			return srv.baseWorker.Prune(ctx, ch, policy...)
		}
		return nil
	})

	err := eg.Wait()
	if err != nil {
		bklog.G(ctx).Errorf("gc error: %+v", err)
	}
	if size > 0 {
		bklog.G(ctx).Debugf("gc cleaned up %d bytes", size)
	}
}

func (srv *Server) Info(ctx context.Context, r *controlapi.InfoRequest) (*controlapi.InfoResponse, error) {
	return &controlapi.InfoResponse{
		BuildkitVersion: &apitypes.BuildkitVersion{
			Package:  engine.Package,
			Version:  engine.Version,
			Revision: srv.engineName,
		},
	}, nil
}

func (srv *Server) ListWorkers(ctx context.Context, r *controlapi.ListWorkersRequest) (*controlapi.ListWorkersResponse, error) {
	resp := &controlapi.ListWorkersResponse{
		Record: []*apitypes.WorkerRecord{{
			ID:        srv.baseWorker.ID(),
			Labels:    srv.baseWorker.Labels(),
			Platforms: pb.PlatformsFromSpec(srv.enabledPlatforms),
		}},
	}
	return resp, nil
}

func (srv *Server) LogMetrics(l *logrus.Entry) *logrus.Entry {
	srv.daggerSessionsMu.RLock()
	defer srv.daggerSessionsMu.RUnlock()
	l = l.WithField("dagger-server-count", len(srv.daggerSessions))
	for _, s := range srv.daggerSessions {
		l = s.LogMetrics(l)
	}
	return l
}

func (srv *Server) Register(server *grpc.Server) {
	controlapi.RegisterControlServer(server, srv)

	traceSrv := &telemetry.TraceServer{PubSub: srv.telemetryPubSub}
	tracev1.RegisterTraceServiceServer(server, traceSrv)
	telemetry.RegisterTracesSourceServer(server, traceSrv)

	logsSrv := &telemetry.LogsServer{PubSub: srv.telemetryPubSub}
	logsv1.RegisterLogsServiceServer(server, logsSrv)
	telemetry.RegisterLogsSourceServer(server, logsSrv)

	metricsSrv := &telemetry.MetricsServer{PubSub: srv.telemetryPubSub}
	metricsv1.RegisterMetricsServiceServer(server, metricsSrv)
	telemetry.RegisterMetricsSourceServer(server, metricsSrv)
}

func (srv *Server) Solve(context.Context, *controlapi.SolveRequest) (*controlapi.SolveResponse, error) {
	return nil, fmt.Errorf("solve not implemented")
}

func (srv *Server) Status(*controlapi.StatusRequest, controlapi.Control_StatusServer) error {
	return fmt.Errorf("status not implemented")
}

func (srv *Server) ListenBuildHistory(*controlapi.BuildHistoryRequest, controlapi.Control_ListenBuildHistoryServer) error {
	return fmt.Errorf("listen build history not implemented")
}

func (srv *Server) UpdateBuildHistory(context.Context, *controlapi.UpdateBuildHistoryRequest) (*controlapi.UpdateBuildHistoryResponse, error) {
	return nil, fmt.Errorf("update build history not implemented")
}
