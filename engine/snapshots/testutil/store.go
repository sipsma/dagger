// Package testutil builds real local snapshot and content stores for transfer
// tests. The stores need no privileges: see inplace.go for what that costs.
package testutil

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/mount"
	ctdsnapshots "github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/core/snapshots/storage"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/containerd/v2/plugins/snapshots/native"
	"github.com/containerd/containerd/v2/plugins/snapshots/overlay"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	containerdsnapshot "github.com/dagger/dagger/engine/snapshots/containerd"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

const Namespace = "snapshot-transfer-test"

type Store struct {
	Manager     bkcache.SnapshotManager
	Content     *containerdsnapshot.Store
	Snapshots   bkcache.Snapshotter
	Leases      leases.Manager
	DB          *metadata.DB
	Applies     atomic.Int64
	Diffs       atomic.Int64
	BeforeWrite func([]byte) error
	BeforeApply func(context.Context, ocispecs.Descriptor) error
	BeforeDiff  func(context.Context) error
	// BeforeSnapshotUpdate runs before each snapshot label update.
	BeforeSnapshotUpdate func(context.Context, ctdsnapshots.Info) error
	// BeforeStat runs before each snapshot Stat the manager makes.
	BeforeStat func(ctx context.Context, key string)
	// BeforeUsage and BeforeCommit run before each snapshot Usage and
	// Commit the managers make.
	BeforeUsage  func(ctx context.Context, key string)
	BeforeCommit func(ctx context.Context, name, key string)
	// Overlay and OverlayManager are the overlay snapshotter and its
	// manager, with StoreOptions.Overlay.
	Overlay        bkcache.Snapshotter
	OverlayManager bkcache.SnapshotManager
	overlay        ctdsnapshots.Snapshotter
	// LeaseCommitErr, when set, rolls back each transaction the manager
	// makes through SnapshotManagerOpt.MetadataDB and returns it.
	LeaseCommitErr error
	// Builtin, when set before the manager is (re)opened, plays the engine's
	// builtin image store for chain imports.
	Builtin     content.InfoReaderProvider
	BeforeAdd   func(context.Context, leases.Lease, leases.Resource) error
	AfterAdd    func(context.Context, leases.Lease, leases.Resource)
	AfterCreate func(leases.Lease)
	root        string

	// SharedMountLinger and MaxIdleSharedMounts, when set before the manager
	// is (re)opened, bound its idle shared read-only mounts.
	SharedMountLinger   time.Duration
	MaxIdleSharedMounts int
	// LocalMounts and LocalUnmounts count the shared read-only mounts made
	// and unmounted; they are made in place, without privileges.
	LocalMounts   atomic.Int64
	LocalUnmounts atomic.Int64
	// BeforeLocalUnmount runs before each of those unmounts.
	BeforeLocalUnmount func()
}

func NewStore(t testing.TB) *Store {
	t.Helper()
	return NewStoreWith(t, StoreOptions{})
}

// StoreOptions shape the store's metadata database.
type StoreOptions struct {
	// Bolt opens the metadata database with these options, such as the
	// engine's (EngineBoltOptions), instead of bbolt's defaults.
	Bolt *bolt.Options
	// TxTimes, when set, records each write transaction on the metadata
	// database: its caller, its wait for the single writer lock and its hold.
	TxTimes *TxTimes
	// Overlay adds an overlay snapshotter to the metadata database, with its
	// own manager (Store.OverlayManager). A snapshot of it with no parent is
	// written in its upper directory, which needs no mount; nothing applies
	// layers to it.
	Overlay bool
	// OverlayUsageFirst has the overlay manager measure a snapshot's usage
	// before its commit and its backend record the measurement, as the
	// engine does (engine/server/server.go).
	OverlayUsageFirst bool
	// NoSyncSnapshots opens the snapshotters' own metadata stores without
	// syncing, as the engine opens its overlay snapshotter's (initBoltDBs).
	// Otherwise each snapshot write in a metadata transaction also syncs the
	// snapshotter's store, which the engine's never does.
	NoSyncSnapshots bool
}

// engineSnapshotStoreOptions are the options the engine opens its overlay
// snapshotter's metadata store with (engine/server/server.go, initBoltDBs).
func engineSnapshotStoreOptions(opts *bolt.Options) error {
	opts.NoSync = true
	opts.NoFreelistSync = true
	opts.FreelistType = bolt.FreelistMapType
	opts.NoStatistics = true
	opts.NoGrowSync = true
	return nil
}

// nativeDefaultsMu serializes the swap of bbolt's default options that opens
// a native snapshotter's store with the engine's options: the native
// snapshotter takes no options and copies the defaults when it is made.
var nativeDefaultsMu sync.Mutex

func newNativeSnapshotter(t testing.TB, root string, noSync bool) ctdsnapshots.Snapshotter {
	t.Helper()
	if noSync {
		nativeDefaultsMu.Lock()
		saved := *bolt.DefaultOptions
		require.NoError(t, engineSnapshotStoreOptions(bolt.DefaultOptions))
		defer func() {
			*bolt.DefaultOptions = saved
			nativeDefaultsMu.Unlock()
		}()
	}
	sn, err := native.NewSnapshotter(root)
	require.NoError(t, err)
	return sn
}

func NewStoreWith(t testing.TB, opts StoreOptions) *Store {
	t.Helper()
	s := &Store{root: t.TempDir()}
	rawContent, err := local.NewStore(filepath.Join(s.root, "content"))
	require.NoError(t, err)
	rawSnapshots := newNativeSnapshotter(t, filepath.Join(s.root, "snapshots"), opts.NoSyncSnapshots)
	db, err := bolt.Open(filepath.Join(s.root, "metadata.db"), 0600, opts.Bolt)
	require.NoError(t, err)
	var tx metadata.Transactor = db
	if opts.TxTimes != nil {
		tx = timedTransactor{DB: db, times: opts.TxTimes}
	}
	// As the engine builds its metadata database (engine/server/server.go).
	backend := rawContent
	if containerdsnapshot.ContentPrecommitEnabled() {
		backend = containerdsnapshot.NewPrecommitStore(backend)
	}
	backends := map[string]ctdsnapshots.Snapshotter{"native": rawSnapshots}
	if opts.Overlay {
		backends["overlayfs"] = s.openOverlay(t, opts.OverlayUsageFirst, opts.NoSyncSnapshots)
	}
	s.DB = metadata.NewDB(tx, backend, backends)
	require.NoError(t, s.DB.Init(context.Background()))
	s.Leases = bkcache.NewLeaseManager(metadata.NewLeaseManager(s.DB), Namespace)
	s.Content = containerdsnapshot.NewContentStore(s.DB.ContentStore(), Namespace)
	s.Snapshots = containerdsnapshot.NewSnapshotter("native", s.DB.Snapshotter("native"), Namespace)
	s.openManager(t)
	if opts.Overlay {
		s.Overlay = containerdsnapshot.NewSnapshotter("overlayfs", s.DB.Snapshotter("overlayfs"), Namespace)
		s.OverlayManager, err = bkcache.NewSnapshotManager(bkcache.SnapshotManagerOpt{
			Snapshotter:   &observedSnapshotter{Snapshotter: s.Overlay, store: s},
			ContentStore:  s.Content,
			LeaseManager:  s.Leases,
			MountPoolRoot: filepath.Join(s.root, "overlay-mounts"),
			MetadataDB:    observedTransactor{DB: s.DB, store: s},
			// As the engine sets it for its overlay backend.
			MeasureUsageBeforeCommit: opts.OverlayUsageFirst,
		})
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		require.NoError(t, s.Manager.Close())
		if s.OverlayManager != nil {
			require.NoError(t, s.OverlayManager.Close())
		}
		require.NoError(t, rawSnapshots.Close())
		if s.overlay != nil {
			require.NoError(t, s.overlay.Close())
		}
		require.NoError(t, db.Close())
	})
	return s
}

// openOverlay opens an overlay snapshotter on a metadata store the store
// opens itself, as the engine does, wrapped as the engine wraps it when
// usageFirst is set.
func (s *Store) openOverlay(t testing.TB, usageFirst, noSync bool) ctdsnapshots.Snapshotter {
	t.Helper()
	root := filepath.Join(s.root, "overlay")
	require.NoError(t, os.MkdirAll(root, 0o700))
	var msOpts []storage.Opt
	if noSync {
		msOpts = append(msOpts, engineSnapshotStoreOptions)
	}
	ms, err := storage.NewMetaStore(filepath.Join(root, "metadata.db"), msOpts...)
	require.NoError(t, err)
	// userxattr is named so that construction does not probe a mount.
	s.overlay, err = overlay.NewSnapshotter(root, overlay.WithMetaStore(ms), overlay.WithMountOptions([]string{"userxattr"}))
	require.NoError(t, err)
	if usageFirst {
		return containerdsnapshot.NewUsageFirstSnapshotter(s.overlay, ms, root)
	}
	return s.overlay
}

func (s *Store) openManager(t testing.TB) {
	t.Helper()
	var err error
	// The in-place applier and differ read and write through the same
	// observed wrapper the manager uses, so BeforeWrite sees a fallback
	// diff's blob writes as it sees the manager's own.
	observed := observedContent{Store: s.Content, owner: s}
	s.Manager, err = bkcache.NewSnapshotManager(bkcache.SnapshotManagerOpt{
		Snapshotter: &observedSnapshotter{Snapshotter: s.Snapshots, store: s}, ContentStore: observed,
		LeaseManager:   &observedLeases{Manager: s.Leases, store: s},
		Applier:        &observedApplier{Applier: inPlaceApplier{store: observed}, store: s},
		Differ:         &observedDiffer{Comparer: inPlaceDiffer{store: observed}, store: s},
		MountPoolRoot:  filepath.Join(s.root, "mounts"),
		BuiltinContent: s.Builtin,
		MetadataDB:     observedTransactor{DB: s.DB, store: s},
		LocalMounter: func(ms []mount.Mount) bkcache.Mounter {
			return &inPlaceMounter{store: s, mounts: ms}
		},
		SharedMountLinger:   s.SharedMountLinger,
		MaxIdleSharedMounts: s.MaxIdleSharedMounts,
	})
	require.NoError(t, err)
}

// WithBuiltin reopens the manager with provider as the builtin image store,
// keeping the persistent metadata.
func (s *Store) WithBuiltin(t testing.TB, provider content.InfoReaderProvider) {
	t.Helper()
	s.Builtin = provider
	s.Reload(t)
}

// WithSharedMounts reopens the manager with the given bounds on its idle
// shared read-only mounts, keeping the persistent metadata.
func (s *Store) WithSharedMounts(t testing.TB, linger time.Duration, maxIdle int) {
	t.Helper()
	s.SharedMountLinger, s.MaxIdleSharedMounts = linger, maxIdle
	s.Reload(t)
}

func (s *Store) Reload(t testing.TB) {
	t.Helper()
	rows := s.Manager.PersistentMetadataRows()
	require.NoError(t, s.Manager.Close())
	s.openManager(t)
	require.NoError(t, s.Manager.LoadPersistentMetadata(rows))
}

func (s *Store) GC(t testing.TB) {
	t.Helper()
	_, err := s.DB.GarbageCollect(namespaces.WithNamespace(context.Background(), Namespace))
	require.NoError(t, err)
}

// Build writes through an ordinary mutable ref, commits, and keeps an ordinary
// owner lease. The caller can release that owner independently of returned refs.
func (s *Store) Build(t testing.TB, parent bkcache.ImmutableRef, name, value string) (bkcache.ImmutableRef, string) {
	t.Helper()
	ctx := context.Background()
	owner, err := s.Leases.Create(ctx, leases.WithRandomID())
	require.NoError(t, err)
	ctx = leases.WithLease(ctx, owner.ID)
	mut, err := s.Manager.New(ctx, parent)
	require.NoError(t, err)
	mounted, err := mut.Mount(ctx, false)
	require.NoError(t, err)
	mounter := bkcache.LocalMounter(mounted)
	root, err := mounter.Mount()
	require.NoError(t, err)
	if name != "" {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(value), 0640))
		require.NoError(t, os.Chtimes(path, FileTime, FileTime))
	}
	require.NoError(t, mounter.Unmount())
	var ref bkcache.ImmutableRef
	if name == "" {
		// No filesystem changes were made. Exercise the ordinary commit path
		// with known empty usage, as snapshot diff application can supply.
		ref, err = mut.CommitWithUsage(ctx, ctdsnapshots.Usage{})
	} else {
		ref, err = mut.Commit(ctx)
	}
	require.NoError(t, err)
	require.NoError(t, s.Manager.AttachLease(ctx, owner.ID, ref.SnapshotID()))
	t.Cleanup(func() {
		require.NoError(t, ref.Release(context.Background()))
		require.NoError(t, s.Manager.RemoveLease(context.Background(), owner.ID))
	})
	return ref, owner.ID
}

var FileTime = time.Unix(1700000000, 0).UTC()

func CheckFile(t testing.TB, ref bkcache.ImmutableRef, name, want string) {
	t.Helper()
	root := Root(t, ref)
	data, err := os.ReadFile(filepath.Join(root, name))
	require.NoError(t, err)
	require.Equal(t, want, string(data))
	info, err := os.Stat(filepath.Join(root, name))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0640), info.Mode().Perm())
	require.Equal(t, FileTime, info.ModTime().UTC())
}

type Provider struct {
	content.InfoReaderProvider
	Reads      atomic.Int64
	BeforeRead func(context.Context, ocispecs.Descriptor) error
}

func (p *Provider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	p.Reads.Add(1)
	if p.BeforeRead != nil {
		if err := p.BeforeRead(ctx, desc); err != nil {
			return nil, err
		}
	}
	return p.InfoReaderProvider.ReaderAt(ctx, desc)
}

type observedTransactor struct {
	*metadata.DB
	store *Store
}

func (t observedTransactor) Update(fn func(*bolt.Tx) error) error {
	return t.DB.Update(func(tx *bolt.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return t.store.LeaseCommitErr
	})
}

type observedSnapshotter struct {
	bkcache.Snapshotter
	store *Store
}

func (s *observedSnapshotter) Stat(ctx context.Context, key string) (ctdsnapshots.Info, error) {
	if s.store.BeforeStat != nil {
		s.store.BeforeStat(ctx, key)
	}
	return s.Snapshotter.Stat(ctx, key)
}

func (s *observedSnapshotter) Usage(ctx context.Context, key string) (ctdsnapshots.Usage, error) {
	if s.store.BeforeUsage != nil {
		s.store.BeforeUsage(ctx, key)
	}
	return s.Snapshotter.Usage(ctx, key)
}

func (s *observedSnapshotter) Commit(ctx context.Context, name, key string, opts ...ctdsnapshots.Opt) error {
	if s.store.BeforeCommit != nil {
		s.store.BeforeCommit(ctx, name, key)
	}
	return s.Snapshotter.Commit(ctx, name, key, opts...)
}

func (s *observedSnapshotter) Update(ctx context.Context, info ctdsnapshots.Info, fieldpaths ...string) (ctdsnapshots.Info, error) {
	if s.store.BeforeSnapshotUpdate != nil {
		if err := s.store.BeforeSnapshotUpdate(ctx, info); err != nil {
			return ctdsnapshots.Info{}, err
		}
	}
	return s.Snapshotter.Update(ctx, info, fieldpaths...)
}

type observedApplier struct {
	diff.Applier
	store *Store
}

func (a *observedApplier) Apply(ctx context.Context, desc ocispecs.Descriptor, mounts []mount.Mount, opts ...diff.ApplyOpt) (ocispecs.Descriptor, error) {
	a.store.Applies.Add(1)
	if a.store.BeforeApply != nil {
		if err := a.store.BeforeApply(ctx, desc); err != nil {
			return ocispecs.Descriptor{}, err
		}
	}
	return a.Applier.Apply(ctx, desc, mounts, opts...)
}

type observedDiffer struct {
	diff.Comparer
	store *Store
}

func (d *observedDiffer) Compare(ctx context.Context, lower, upper []mount.Mount, opts ...diff.Opt) (ocispecs.Descriptor, error) {
	d.store.Diffs.Add(1)
	if d.store.BeforeDiff != nil {
		if err := d.store.BeforeDiff(ctx); err != nil {
			return ocispecs.Descriptor{}, err
		}
	}
	return d.Comparer.Compare(ctx, lower, upper, opts...)
}

type observedLeases struct {
	leases.Manager
	store *Store
}

func (l *observedLeases) AddResource(ctx context.Context, lease leases.Lease, resource leases.Resource) error {
	if l.store.BeforeAdd != nil {
		if err := l.store.BeforeAdd(ctx, lease, resource); err != nil {
			return err
		}
	}
	if err := l.Manager.AddResource(ctx, lease, resource); err != nil {
		return err
	}
	if l.store.AfterAdd != nil {
		l.store.AfterAdd(ctx, lease, resource)
	}
	return nil
}

func (l *observedLeases) Create(ctx context.Context, opts ...leases.Opt) (leases.Lease, error) {
	lease, err := l.Manager.Create(ctx, opts...)
	if err == nil && l.store.AfterCreate != nil {
		l.store.AfterCreate(lease)
	}
	return lease, err
}

// observedContent retains the actual content store and injects only a writer
// boundary failure, so import tests exercise the real copy and cleanup path.
type observedContent struct {
	content.Store
	owner *Store
}

func (s observedContent) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	writer, err := s.Store.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return observedWriter{Writer: writer, owner: s.owner}, nil
}

type observedWriter struct {
	content.Writer
	owner *Store
}

func (w observedWriter) Write(p []byte) (int, error) {
	if w.owner.BeforeWrite != nil {
		if err := w.owner.BeforeWrite(p); err != nil {
			return 0, err
		}
	}
	return w.Writer.Write(p)
}
