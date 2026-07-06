package snapshots

import (
	"bytes"
	"context"
	"io"
	"testing"

	local "github.com/containerd/containerd/v2/plugins/content/local"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

type blobSourceFunc func(ctx context.Context, dgst digest.Digest, size int64) (io.ReadCloser, error)

func (f blobSourceFunc) OpenBlob(ctx context.Context, dgst digest.Digest, size int64) (io.ReadCloser, error) {
	return f(ctx, dgst, size)
}

// TestEnsureChainBlobCorruptIngestDiscarded: a corrupt fetch must not
// poison retries. Without the abort, the failed commit leaves a resumable
// ingest holding the corrupt bytes, and the NEXT attempt — even against a
// healed source — resumes and re-commits them, failing forever. Discard
// means discard: corrupt, then correct, must succeed, against the real
// content store.
func TestEnsureChainBlobCorruptIngestDiscarded(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	cm := &snapshotManager{ContentStore: store}

	payload := []byte("the true chain blob bytes")
	corrupt := []byte("not those bytes, same len")
	require.Equal(t, len(payload), len(corrupt), "same length so the digest check, not the size check, trips")
	dgst := digest.FromBytes(payload)
	desc := ocispecs.Descriptor{
		MediaType: ocispecs.MediaTypeImageLayer,
		Digest:    dgst,
		Size:      int64(len(payload)),
	}

	corruptSource := blobSourceFunc(func(context.Context, digest.Digest, int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(corrupt)), nil
	})
	_, err = cm.ensureChainBlob(ctx, desc, corruptSource)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrChainBlobCorrupt), "corrupt bytes must report typed: %v", err)

	// The healed source now serves the true bytes; the earlier corruption
	// must have left nothing behind.
	healedSource := blobSourceFunc(func(context.Context, digest.Digest, int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	})
	fetched, err := cm.ensureChainBlob(ctx, desc, healedSource)
	require.NoError(t, err)
	require.True(t, fetched, "the healed attempt is a real transfer")

	info, err := store.Info(ctx, dgst)
	require.NoError(t, err)
	require.Equal(t, int64(len(payload)), info.Size)
}
