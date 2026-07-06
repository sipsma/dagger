package snapshots

import (
	"context"
	"io"
	"os"
	"path/filepath"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// DirectoryCAS is a digest-addressed blob store on a filesystem directory:
// one file per blob at <root>/blobs/<algorithm>/<hex>. It is the file
// transport's BlobSource implementation and the export side's blob sink for
// the same layout — enough for chain round-trips over a shared directory
// with no network.
type DirectoryCAS struct {
	Root string
}

var _ BlobSource = DirectoryCAS{}

func (c DirectoryCAS) blobPath(dgst digest.Digest) (string, error) {
	if err := dgst.Validate(); err != nil {
		return "", errors.Wrapf(err, "invalid blob digest %q", dgst)
	}
	return filepath.Join(c.Root, "blobs", dgst.Algorithm().String(), dgst.Encoded()), nil
}

func (c DirectoryCAS) OpenBlob(ctx context.Context, dgst digest.Digest, size int64) (io.ReadCloser, error) {
	_ = ctx
	_ = size
	path, err := c.blobPath(dgst)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.Wrapf(ErrBlobNotFound, "blob %s", dgst)
		}
		return nil, errors.Wrapf(err, "open blob %s", dgst)
	}
	return f, nil
}

// PutBlob writes one blob, verifying the bytes against the digest before
// the file becomes visible (temp file + rename). Re-putting an existing
// blob is a no-op: blobs are immutable by address.
func (c DirectoryCAS) PutBlob(ctx context.Context, dgst digest.Digest, r io.Reader) (rerr error) {
	_ = ctx
	path, err := c.blobPath(dgst)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errors.Wrapf(err, "create blob dir for %s", dgst)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".ingest-*")
	if err != nil {
		return errors.Wrapf(err, "create blob ingest file for %s", dgst)
	}
	defer func() {
		if rerr != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	digester := dgst.Algorithm().Digester()
	if _, err := io.Copy(io.MultiWriter(tmp, digester.Hash()), r); err != nil {
		_ = tmp.Close()
		return errors.Wrapf(err, "write blob %s", dgst)
	}
	if err := tmp.Close(); err != nil {
		return errors.Wrapf(err, "close blob ingest file for %s", dgst)
	}
	if computed := digester.Digest(); computed != dgst {
		return errors.Errorf("blob digest mismatch: got %s, want %s", computed, dgst)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return errors.Wrapf(err, "commit blob %s", dgst)
	}
	return nil
}
