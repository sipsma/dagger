package metadata

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	bolt "go.etcd.io/bbolt"
)

// ioCheckStore is a content store whose file work checks that no write
// transaction of db is open: bbolt has one writer, so a write transaction
// started from here completes only if none is.
type ioCheckStore struct {
	content.Store
	t          *testing.T
	db         *bolt.DB
	writerErr  error
	checked    int
	insideTxns []string
	// onWriter and onCommit, when set, run in the backing store's Writer
	// before it creates the writer and in its Commit once the content is
	// committed. onWriter's error, if any, fails the writer.
	onWriter func() error
	onCommit func()
}

func (s *ioCheckStore) check(op string) {
	s.checked++
	done := make(chan struct{})
	go func() {
		_ = s.db.Update(func(*bolt.Tx) error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		s.insideTxns = append(s.insideTxns, op)
	}
}

func (s *ioCheckStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	s.check("Info")
	return s.Store.Info(ctx, dgst)
}

func (s *ioCheckStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	s.check("Writer")
	if s.writerErr != nil {
		return nil, s.writerErr
	}
	if hook := s.onWriter; hook != nil {
		s.onWriter = nil
		if err := hook(); err != nil {
			return nil, err
		}
	}
	w, err := s.Store.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &ioCheckWriter{Writer: w, s: s}, nil
}

type ioCheckWriter struct {
	content.Writer
	s *ioCheckStore
}

func (w *ioCheckWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	w.s.check("Commit")
	if err := w.Writer.Commit(ctx, size, expected, opts...); err != nil {
		return err
	}
	if hook := w.s.onCommit; hook != nil {
		w.s.onCommit = nil
		hook()
	}
	return nil
}

func newIOCheckStore(t *testing.T) (context.Context, *ioCheckStore, content.Store) {
	t.Helper()
	root := t.TempDir()
	backing, err := local.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(filepath.Join(root, "metadata.db"), 0660, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &ioCheckStore{Store: backing, t: t, db: db}
	return namespaces.WithNamespace(context.Background(), "io"), s, NewDB(db, s, nil).ContentStore()
}

// Writing a blob holds no write transaction of the metadata database across
// the backing store's file work: the stat of shared content, creating the
// ingest, and committing it.
func TestContentWriteHoldsNoTransactionAcrossIO(t *testing.T) {
	ctx, s, cs := newIOCheckStore(t)
	blob := []byte("a layer")
	desc := ocispec.Descriptor{Size: int64(len(blob)), Digest: digest.FromBytes(blob)}
	if err := content.WriteBlob(ctx, cs, "layer", bytes.NewReader(blob), desc); err != nil {
		t.Fatal(err)
	}
	if s.checked < 3 {
		t.Fatalf("the backing store's file work ran %d times, want Info, Writer and Commit", s.checked)
	}
	if len(s.insideTxns) != 0 {
		t.Fatalf("ran inside a write transaction: %v", s.insideTxns)
	}
	info, err := cs.Info(ctx, desc.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != desc.Size {
		t.Fatalf("size %d, want %d", info.Size, desc.Size)
	}
}

// A writer the backing store fails to create leaves no ingest, as when the
// transaction recording it rolled back.
func TestContentWriterFailureLeavesNoIngest(t *testing.T) {
	ctx, s, cs := newIOCheckStore(t)
	s.writerErr = errors.New("disk failed")
	if _, err := cs.Writer(ctx, content.WithRef("layer")); err == nil {
		t.Fatal("writer created")
	}
	if _, err := cs.Status(ctx, "layer"); !errdefs.IsNotFound(err) {
		t.Fatalf("status of the failed ingest: %v, want not found", err)
	}
	statuses, err := cs.ListStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("ingests left: %v", statuses)
	}
}

// A second writer of a ref whose writer is open is refused, and leaves the
// first writer's ingest to it.
func TestContentWriterOfOpenRefKeepsItsIngest(t *testing.T) {
	ctx, _, cs := newIOCheckStore(t)
	w, err := cs.Writer(ctx, content.WithRef("layer"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Writer(ctx, content.WithRef("layer")); !errdefs.IsUnavailable(err) {
		t.Fatalf("second writer: %v, want unavailable", err)
	}
	if _, err := cs.Status(ctx, "layer"); err != nil {
		t.Fatalf("status of the open ingest: %v", err)
	}
	blob := []byte("a layer")
	if _, err := w.Write(blob); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(ctx, int64(len(blob)), digest.FromBytes(blob)); err != nil {
		t.Fatal(err)
	}
}

// whileRunning starts op and returns whether it was still running after
// a while, and a channel with its error.
func whileRunning(op func() error) (bool, <-chan error) {
	done := make(chan error, 1)
	go func() { done <- op() }()
	select {
	case err := <-done:
		done <- err
		return false, done
	case <-time.After(300 * time.Millisecond):
		return true, done
	}
}

// A writer of a ref whose content is being committed waits for the commit
// to be recorded, and then finds the content: it neither shares the ingest
// the commit removes nor leaves one of its own.
func TestContentWriterWaitsForCommitOfItsRef(t *testing.T) {
	ctx, s, cs := newIOCheckStore(t)
	blob := []byte("a layer")
	desc := ocispec.Descriptor{Size: int64(len(blob)), Digest: digest.FromBytes(blob)}
	w, err := cs.Writer(ctx, content.WithRef("layer"), content.WithDescriptor(desc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(blob); err != nil {
		t.Fatal(err)
	}
	var waited bool
	var second <-chan error
	s.onCommit = func() {
		waited, second = whileRunning(func() error {
			w2, err := cs.Writer(ctx, content.WithRef("layer"), content.WithDescriptor(desc))
			if err == nil {
				w2.Close()
			}
			return err
		})
	}
	if err := w.Commit(ctx, desc.Size, desc.Digest); err != nil {
		t.Fatal(err)
	}
	if !waited {
		t.Fatal("the second writer opened while the commit of its ref was being recorded")
	}
	if err := <-second; !errdefs.IsAlreadyExists(err) {
		t.Fatalf("second writer: %v, want already exists", err)
	}
	statuses, err := cs.ListStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Fatalf("ingests left: %v", statuses)
	}
}

// A writer of a ref whose writer is failing waits for the failure's cleanup,
// and keeps its own ingest.
func TestContentWriterWaitsForFailedWriterOfItsRef(t *testing.T) {
	ctx, s, cs := newIOCheckStore(t)
	var waited bool
	var second <-chan error
	var w2 content.Writer
	s.onWriter = func() error {
		waited, second = whileRunning(func() error {
			var err error
			w2, err = cs.Writer(ctx, content.WithRef("layer"))
			return err
		})
		return errors.New("disk failed")
	}
	if _, err := cs.Writer(ctx, content.WithRef("layer")); err == nil {
		t.Fatal("first writer created")
	}
	if !waited {
		t.Fatal("the second writer opened while the first was failing")
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	if _, err := cs.Status(ctx, "layer"); err != nil {
		t.Fatalf("status of the second writer's ingest: %v", err)
	}
}
