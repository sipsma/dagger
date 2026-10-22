package containerd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	cerrdefs "github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

const precommitTestNS = "precommit-test"

// blockingCommits holds each commit in the local store, once block is set,
// until released, as a slow disk holds a commit's fsyncs.
type blockingCommits struct {
	content.Store
	block       bool
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (s *blockingCommits) unblock() {
	s.releaseOnce.Do(func() { close(s.release) })
}

// WalkStatusRefs keeps the local store's walk of ingest refs, which
// garbage collection uses when the store has it.
func (s *blockingCommits) WalkStatusRefs(ctx context.Context, fn func(string) error) error {
	return s.Store.(statusRefWalker).WalkStatusRefs(ctx, fn)
}

func (s *blockingCommits) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	w, err := s.Store.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &blockingWriter{Writer: w, store: s}, nil
}

type blockingWriter struct {
	content.Writer
	store *blockingCommits
}

func (w *blockingWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	if w.store.block {
		w.store.entered <- struct{}{}
		<-w.store.release
	}
	return w.Writer.Commit(ctx, size, expected, opts...)
}

func (w *blockingWriter) Sync() error {
	if s, ok := w.Writer.(content.Syncer); ok {
		return s.Sync()
	}
	return nil
}

type precommitTestEnv struct {
	ctx context.Context
	// local is the local store under the metadata database, and content the
	// engine's view of the database's content store.
	local   content.Store
	db      *metadata.DB
	content *Store
	slow    *blockingCommits
	root    string
}

// newPrecommitTestEnv opens a containerd metadata database over a local
// content store, its backend wrapped by wrap, as the engine opens its own.
func newPrecommitTestEnv(t *testing.T, wrap func(content.Store) content.Store) *precommitTestEnv {
	t.Helper()
	root := t.TempDir()
	store, err := local.NewStore(filepath.Join(root, "content"))
	require.NoError(t, err)
	slow := &blockingCommits{Store: store, entered: make(chan struct{}, 4), release: make(chan struct{})}
	db, err := bolt.Open(filepath.Join(root, "metadata.db"), 0o600, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	// Before the database closes: a held commit holds its transaction.
	t.Cleanup(slow.unblock)
	var backend content.Store = slow
	if wrap != nil {
		backend = wrap(slow)
	}
	mdb := metadata.NewDB(db, backend, map[string]snapshots.Snapshotter{})
	ctx := context.Background()
	require.NoError(t, mdb.Init(ctx))
	return &precommitTestEnv{ctx: ctx, local: store, db: mdb, content: NewContentStore(mdb.ContentStore(), precommitTestNS), slow: slow, root: root}
}

// ingests lists the ingest directories in the local store.
func (env *precommitTestEnv) ingests(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(env.root, "content", "ingest"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func precommitTestBlob(data string) ([]byte, ocispecs.Descriptor) {
	return []byte(data), ocispecs.Descriptor{MediaType: ocispecs.MediaTypeImageLayerGzip, Digest: digest.FromString(data), Size: int64(len(data))}
}

var precommitStores = []struct {
	name string
	wrap func(content.Store) content.Store
}{
	{name: "local store"},
	{name: "precommit store", wrap: NewPrecommitStore},
}

func receiveWithin[T any](t *testing.T, ch <-chan T, bound time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(bound):
		t.Fatalf("%s: not within %s", what, bound)
		panic("unreachable")
	}
}

// A blob's commit fsyncs the blob and its directory. Through the plain local
// store, containerd's metadata database does that inside its write
// transaction, so every other write in the engine (leases, snapshots, other
// blobs) waits for one blob's disk work. Through the precommit store the
// disk work runs before the transaction, and other writes proceed.
func TestPrecommitStoreCommitsBeforeTheTransaction(t *testing.T) {
	for _, tc := range precommitStores {
		t.Run(tc.name, func(t *testing.T) {
			env := newPrecommitTestEnv(t, tc.wrap)
			env.slow.block = true
			plain := tc.wrap == nil
			data, desc := precommitTestBlob("a layer")
			committed := make(chan error, 1)
			go func() {
				committed <- content.WriteBlob(env.ctx, env.content, "blob", bytes.NewReader(data), desc)
			}()
			receiveWithin(t, env.slow.entered, 10*time.Second, "the blob's commit reaching the local store")

			created := make(chan error, 1)
			go func() {
				_, err := metadata.NewLeaseManager(env.db).Create(withTestNamespace(env.ctx), leases.WithRandomID())
				created <- err
			}()
			if plain {
				// The plain store holds the database: the lease write is
				// still waiting a while later.
				select {
				case err := <-created:
					t.Fatalf("a lease write proceeded during a blob's commit through the plain local store: %v", err)
				case <-time.After(300 * time.Millisecond):
				}
			} else {
				require.NoError(t, receiveWithin(t, created, 10*time.Second, "a lease write during a blob's commit"))
			}

			env.slow.unblock()
			require.NoError(t, receiveWithin(t, committed, 10*time.Second, "the blob's commit"))
			if plain {
				require.NoError(t, receiveWithin(t, created, 10*time.Second, "the lease write after the commit"))
			}
			info, err := env.content.Info(env.ctx, desc.Digest)
			require.NoError(t, err)
			require.Equal(t, desc.Size, info.Size)
			read, err := content.ReadBlob(env.ctx, env.content, desc)
			require.NoError(t, err)
			require.Equal(t, data, read)
			require.Empty(t, env.ingests(t))
		})
	}
}

// The precommit store's commits end as the local store's do, in the
// metadata database and in the local store: of two writers of one blob, the
// first commits and the second finds it committed; a blob whose bytes are
// not the expected digest is refused, and its ingest kept for Abort; and an
// ingest missing bytes is refused and kept, and can be resumed.
func TestPrecommitStoreCommitOutcomes(t *testing.T) {
	for _, tc := range precommitStores {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("two writers of one blob", func(t *testing.T) {
				env := newPrecommitTestEnv(t, tc.wrap)
				data, desc := precommitTestBlob("shared layer")
				var writers []content.Writer
				for _, ref := range []string{"first", "second"} {
					w, err := env.content.Writer(env.ctx, content.WithRef(ref), content.WithDescriptor(desc))
					require.NoError(t, err)
					_, err = w.Write(data)
					require.NoError(t, err)
					writers = append(writers, w)
				}
				require.NoError(t, writers[0].Commit(env.ctx, desc.Size, desc.Digest))
				require.ErrorIs(t, writers[1].Commit(env.ctx, desc.Size, desc.Digest), cerrdefs.ErrAlreadyExists)
				for _, w := range writers {
					require.NoError(t, w.Close())
				}
				read, err := content.ReadBlob(env.ctx, env.content, desc)
				require.NoError(t, err)
				require.Equal(t, data, read)
				require.Empty(t, env.ingests(t))
			})
			t.Run("unexpected digest", func(t *testing.T) {
				env := newPrecommitTestEnv(t, tc.wrap)
				data, _ := precommitTestBlob("actual bytes")
				_, desc := precommitTestBlob("expected bytes")
				desc.Size = int64(len(data))
				w, err := env.content.Writer(env.ctx, content.WithRef("wrong"), content.WithDescriptor(desc))
				require.NoError(t, err)
				_, err = w.Write(data)
				require.NoError(t, err)
				require.ErrorIs(t, w.Commit(env.ctx, desc.Size, desc.Digest), cerrdefs.ErrFailedPrecondition)
				require.NoError(t, w.Close())
				for _, dgst := range []digest.Digest{desc.Digest, digest.FromBytes(data)} {
					_, err = env.content.Info(env.ctx, dgst)
					require.ErrorIs(t, err, cerrdefs.ErrNotFound)
					_, err = env.local.Info(env.ctx, dgst)
					require.ErrorIs(t, err, cerrdefs.ErrNotFound, "the local store has no blob")
				}
				require.Len(t, env.ingests(t), 1, "the refused ingest is kept")
				require.NoError(t, env.content.Abort(env.ctx, "wrong"))
				require.Empty(t, env.ingests(t))
			})
			t.Run("missing bytes", func(t *testing.T) {
				env := newPrecommitTestEnv(t, tc.wrap)
				data, desc := precommitTestBlob("a whole layer")
				w, err := env.content.Writer(env.ctx, content.WithRef("short"), content.WithDescriptor(desc))
				require.NoError(t, err)
				_, err = w.Write(data[:4])
				require.NoError(t, err)
				require.ErrorIs(t, w.Commit(env.ctx, desc.Size, desc.Digest), cerrdefs.ErrFailedPrecondition)
				require.NoError(t, w.Close())
				_, err = env.content.Info(env.ctx, desc.Digest)
				require.ErrorIs(t, err, cerrdefs.ErrNotFound)

				// The ingest resumes where it stopped.
				w, err = env.content.Writer(env.ctx, content.WithRef("short"), content.WithDescriptor(desc))
				require.NoError(t, err)
				status, err := w.Status()
				require.NoError(t, err)
				require.EqualValues(t, 4, status.Offset)
				require.NoError(t, content.Copy(env.ctx, w, bytes.NewReader(data), desc.Size, desc.Digest))
				require.NoError(t, w.Close())
				read, err := content.ReadBlob(env.ctx, env.content, desc)
				require.NoError(t, err)
				require.Equal(t, data, read)
			})
		})
	}
}

// Garbage collection aborts the ingests nothing references through the local
// store's walk of ingest refs, which reads only each ingest's ref. An ingest
// whose data file is gone, as a commit leaves one that renamed the data and
// then failed, is aborted like any other; listing statuses instead would
// fail on it and abort none.
func TestPrecommitStoreKeepsIngestCollection(t *testing.T) {
	for _, tc := range precommitStores {
		t.Run(tc.name, func(t *testing.T) {
			env := newPrecommitTestEnv(t, tc.wrap)
			w, err := env.local.Writer(env.ctx, content.WithRef("leftover"))
			require.NoError(t, err)
			_, err = w.Write([]byte("renamed away"))
			require.NoError(t, err)
			require.NoError(t, w.Close())
			ingests := env.ingests(t)
			require.Len(t, ingests, 1)
			require.NoError(t, os.Remove(filepath.Join(env.root, "content", "ingest", ingests[0], "data")))

			// Deleting a blob has the next collection sweep the local store.
			data, desc := precommitTestBlob("deleted layer")
			require.NoError(t, content.WriteBlob(env.ctx, env.content, "deleted", bytes.NewReader(data), desc))
			// Through the database's own store: the engine's refuses deletes.
			require.NoError(t, env.db.ContentStore().Delete(withTestNamespace(env.ctx), desc.Digest))
			_, err = env.db.GarbageCollect(env.ctx)
			require.NoError(t, err)
			require.Empty(t, env.ingests(t), "the leftover ingest is aborted")
			_, err = env.local.Info(env.ctx, desc.Digest)
			require.ErrorIs(t, err, cerrdefs.ErrNotFound, "the deleted blob is swept")
		})
	}
}

func withTestNamespace(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, precommitTestNS)
}
