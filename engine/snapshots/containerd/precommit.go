package containerd

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	cerrdefs "github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
)

// contentPrecommitEnv turns committing blobs before the metadata database's
// transaction off when set to 0.
const contentPrecommitEnv = "_EXPERIMENTAL_DAGGER_CONTENT_PRECOMMIT"

// ContentPrecommitEnabled reports whether the engine wraps its local content
// store with NewPrecommitStore.
var ContentPrecommitEnabled = sync.OnceValue(func() bool {
	return os.Getenv(contentPrecommitEnv) != "0"
})

// NewPrecommitStore wraps the local content store a containerd metadata
// database is built on, so that a blob's commit does its disk work before
// the database's write transaction instead of inside it.
//
// The metadata database commits a blob in two steps. First it syncs the
// local store's writer, holding only the content store's lock, which keeps
// garbage collection out. Then, in one write transaction, it commits the
// local writer and records the blob. The local commit fsyncs the blob,
// renames it into place and fsyncs the blob directory; inside the
// transaction that holds the database's single writer, which every lease,
// snapshot and blob write in the engine needs, so one slow fsync stalls them
// all.
//
// Here the sync step makes the local commit when the writer holds exactly
// the bytes it was opened for: the size and digest its descriptor declared,
// which Store.Writer passes down. The commit in the transaction then reports
// that commit's outcome, and a successful one is recorded under the same
// content store lock, so garbage collection never sees it unrecorded. Any
// other writer, such as one of unknown size or whose bytes are not the
// declared digest, commits in the transaction as before.
func NewPrecommitStore(store content.Store) content.Store {
	s := precommitStore{Store: store}
	if walker, ok := store.(statusRefWalker); ok {
		// Garbage collection aborts unreferenced ingests through this walk
		// when the store has it.
		return precommitWalkingStore{precommitStore: s, walker: walker}
	}
	return s
}

type statusRefWalker interface {
	WalkStatusRefs(context.Context, func(string) error) error
}

type precommitStore struct {
	content.Store
}

type precommitWalkingStore struct {
	precommitStore
	walker statusRefWalker
}

func (s precommitWalkingStore) WalkStatusRefs(ctx context.Context, fn func(string) error) error {
	return s.walker.WalkStatusRefs(ctx, fn)
}

type precommitDigestKey struct{}

// withPrecommitDigest records the digest a writer opened with ctx declares.
// The metadata database opens its local writer without it.
func withPrecommitDigest(ctx context.Context, dgst digest.Digest) context.Context {
	return context.WithValue(ctx, precommitDigestKey{}, dgst)
}

func (s precommitStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	w, err := s.Store.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}
	declared, _ := ctx.Value(precommitDigestKey{}).(digest.Digest)
	return &precommitWriter{Writer: w, ctx: ctx, declared: declared}, nil
}

type precommitWriter struct {
	content.Writer
	// ctx is the writer's, for the commit Sync makes.
	ctx context.Context
	// declared is the digest the writer was opened for, if known.
	declared digest.Digest
	// committed is set once Sync committed the ingest: err is that commit's
	// outcome, and size and digest are what it committed.
	committed bool
	err       error
	size      int64
	digest    digest.Digest
}

// Sync commits an ingest that holds exactly its declared size and digest.
// Any other ingest is only synced.
func (w *precommitWriter) Sync() error {
	if w.committed {
		return nil
	}
	status, err := w.Writer.Status()
	if err != nil || w.declared == "" || status.Total <= 0 || status.Offset != status.Total || w.Writer.Digest() != w.declared {
		if s, ok := w.Writer.(content.Syncer); ok {
			return s.Sync()
		}
		return nil
	}
	w.committed = true
	w.size, w.digest = status.Offset, w.declared
	w.err = w.Writer.Commit(w.ctx, w.size, w.digest)
	return nil
}

// Commit reports the outcome of the commit Sync made, checked against size
// and expected as the local store checks them. A writer Sync did not commit
// commits now. The metadata database keeps a blob's labels itself and
// passes the local store none.
func (w *precommitWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	if !w.committed {
		return w.Writer.Commit(ctx, size, expected, opts...)
	}
	var base content.Info
	for _, opt := range opts {
		if err := opt(&base); err != nil {
			return err
		}
	}
	if size > 0 && size != w.size {
		return fmt.Errorf("unexpected commit size %d, expected %d: %w", w.size, size, cerrdefs.ErrFailedPrecondition)
	}
	if expected != "" && expected != w.digest {
		return fmt.Errorf("unexpected commit digest %s, expected %s: %w", w.digest, expected, cerrdefs.ErrFailedPrecondition)
	}
	return w.err
}
