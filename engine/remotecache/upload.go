package remotecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/remotecache/protocol"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

type uploadOutcome struct {
	bundleID      string
	results       int
	blobsUploaded int
	bytesUploaded int64
}

// exportLayer is one blob of an export: its descriptor and the provider
// that reads it. The readers are valid only inside the export callback.
type exportLayer struct {
	descriptor ocispecs.Descriptor
	provider   content.InfoReaderProvider
}

// upload runs inside the export callback. It checks every layer with the
// service, uploads the blobs the service lacks, at most four at a time, and
// posts the bundle last. Any failure fails the export; blobs already
// uploaded stay in the store, harmless because they are named by digest.
func (c *client) upload(ctx context.Context, commandID string, values *dagql.ExportedValues) (uploadOutcome, error) {
	outcome := uploadOutcome{results: len(values.Bundle.Values)}
	layers := collectLayers(values)
	if len(layers) > 0 {
		check := protocol.BlobCheckRequest{Blobs: make([]protocol.BlobDescriptor, 0, len(layers))}
		for _, layer := range layers {
			check.Blobs = append(check.Blobs, protocol.BlobDescriptor{Digest: layer.descriptor.Digest, Size: layer.descriptor.Size})
		}
		var answer protocol.BlobCheckResponse
		if _, err := c.do(ctx, requestTimeout, http.MethodPost, protocol.PathBlobCheck, check, &answer); err != nil {
			return outcome, fmt.Errorf("blob check: %w", err)
		}
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(maxConcurrentUploads)
		var uploadedBlobs int
		var uploadedBytes int64
		var mu sync.Mutex
		for _, layer := range layers {
			target, ok := answer.Upload[layer.descriptor.Digest]
			if !ok {
				continue
			}
			group.Go(func() error {
				if err := c.uploadBlob(groupCtx, target.URL, layer); err != nil {
					return fmt.Errorf("upload blob %s: %w", layer.descriptor.Digest, err)
				}
				mu.Lock()
				uploadedBlobs++
				uploadedBytes += layer.descriptor.Size
				mu.Unlock()
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return outcome, err
		}
		outcome.blobsUploaded, outcome.bytesUploaded = uploadedBlobs, uploadedBytes
	}
	raw, err := json.Marshal(values.Bundle)
	if err != nil {
		return outcome, fmt.Errorf("encode bundle: %w", err)
	}
	var posted protocol.BundleUploadResponse
	if _, err := c.do(ctx, requestTimeout, http.MethodPost, protocol.PathBundles, protocol.BundleUploadRequest{CommandID: commandID, Bundle: raw}, &posted); err != nil {
		var refused *serviceError
		if errors.As(err, &refused) && refused.status == http.StatusConflict {
			return outcome, fmt.Errorf("bundle refused, blobs missing from the store: %v: %w", refused.body.Missing, err)
		}
		return outcome, fmt.Errorf("post bundle: %w", err)
	}
	if posted.BundleID == "" {
		return outcome, errors.New("post bundle: the service answered no bundle ID")
	}
	outcome.bundleID = posted.BundleID
	return outcome, nil
}

// collectLayers lists every layer of every chain once, in first-seen order.
func collectLayers(values *dagql.ExportedValues) []exportLayer {
	if values.Chains == nil {
		return nil
	}
	seen := map[digest.Digest]bool{}
	var layers []exportLayer
	for _, chain := range values.Chains.Entries {
		for _, layer := range chain.Layers {
			if seen[layer.Descriptor.Digest] {
				continue
			}
			seen[layer.Descriptor.Digest] = true
			layers = append(layers, exportLayer{descriptor: layer.Descriptor, provider: chain.Provider})
		}
	}
	return layers
}

// uploadBlob sends one blob with one PUT of exactly the layer's size. It has
// no time limit of its own; only ctx ends it.
func (c *client) uploadBlob(ctx context.Context, url string, layer exportLayer) error {
	readerAt, err := layer.provider.ReaderAt(ctx, layer.descriptor)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer readerAt.Close()
	if readerAt.Size() != layer.descriptor.Size {
		return fmt.Errorf("blob is %d bytes, descriptor says %d", readerAt.Size(), layer.descriptor.Size)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, content.NewReader(readerAt))
	if err != nil {
		return err
	}
	req.ContentLength = layer.descriptor.Size
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
