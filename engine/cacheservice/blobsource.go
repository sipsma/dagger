package cacheservice

import (
	"context"
	"errors"
	"fmt"
	"io"

	snapshots "github.com/dagger/dagger/engine/snapshots"
	digest "github.com/opencontainers/go-digest"
)

// blobSource adapts the client to the snapshot manager's chain-fetch seam.
// The one translation is failure typing (§9.3): the service's 404 becomes
// the seam's permanent-per-boot sentinel; everything else passes through as
// transient. Digest verification stays where it lives — the content store's
// commit inside MaterializeChain.
type blobSource struct {
	client *Client
}

// BlobSource returns the client as the content-chain blob source wired into
// the cache during the boot window.
func (c *Client) BlobSource() snapshots.BlobSource {
	return blobSource{client: c}
}

func (s blobSource) OpenBlob(ctx context.Context, dgst digest.Digest, size int64) (io.ReadCloser, error) {
	rc, err := s.client.OpenBlob(ctx, dgst, size)
	if err != nil {
		if errors.Is(err, ErrBlobNotFound) {
			return nil, fmt.Errorf("%w: %v", snapshots.ErrBlobNotFound, err)
		}
		return nil, err
	}
	return rc, nil
}
