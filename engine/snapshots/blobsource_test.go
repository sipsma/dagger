package snapshots

import (
	"bytes"
	"io"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestDirectoryCASRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cas := DirectoryCAS{Root: t.TempDir()}

	payload := []byte("chain blob bytes")
	dgst := digest.FromBytes(payload)

	// Absent blobs report the typed not-found the chain arm treats as
	// permanent.
	_, err := cas.OpenBlob(ctx, dgst, int64(len(payload)))
	require.True(t, errors.Is(err, ErrBlobNotFound))

	require.NoError(t, cas.PutBlob(ctx, dgst, bytes.NewReader(payload)))

	rc, err := cas.OpenBlob(ctx, dgst, int64(len(payload)))
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, payload, got)

	// Re-putting is a no-op, and mismatched bytes never become visible
	// under a digest they do not hash to.
	require.NoError(t, cas.PutBlob(ctx, dgst, bytes.NewReader(payload)))
	otherDigest := digest.FromString("something else")
	err = cas.PutBlob(ctx, otherDigest, strings.NewReader("not those bytes"))
	require.ErrorContains(t, err, "digest mismatch")
	_, err = cas.OpenBlob(ctx, otherDigest, 0)
	require.True(t, errors.Is(err, ErrBlobNotFound))
}
