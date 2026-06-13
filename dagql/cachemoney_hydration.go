package dagql

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/labels"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/dagger/dagger/dagql/cachemoneyproto"
	"github.com/dagger/dagger/engine/slog"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/internal/buildkit/client"
	"github.com/opencontainers/go-digest"
	ociidentity "github.com/opencontainers/image-spec/identity"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

const cachemoneyHydrationFetchParallelism = 4

type RemoteSnapshotMaterializationRequest struct {
	ResultID uint64
	Role     string
	Chain    PersistedSnapshotChain
	Owner    AnyResult
}

type cachemoneyContentBlobWriter interface {
	WriteContentBlob(context.Context, ocispecs.Descriptor, io.Reader) error
}

func (c *Cache) MaterializeRemoteSnapshot(ctx context.Context, req RemoteSnapshotMaterializationRequest) (bkcache.ImmutableRef, bool, error) {
	if c == nil {
		return nil, false, fmt.Errorf("remote snapshot materialize: nil cache")
	}
	if c.snapshotManager == nil {
		return nil, false, fmt.Errorf("remote snapshot materialize result %d role %q: missing snapshot manager", req.ResultID, req.Role)
	}
	if req.ResultID == 0 {
		return nil, false, fmt.Errorf("remote snapshot materialize: zero result ID")
	}
	if req.Role == "" {
		return nil, false, fmt.Errorf("remote snapshot materialize result %d: empty role", req.ResultID)
	}

	originSourceID, err := c.cachemoneyOriginSourceID(ctx, req.ResultID)
	if err != nil {
		return nil, false, err
	}
	if originSourceID == "" {
		return nil, false, fmt.Errorf("remote snapshot materialize result %d role %q: missing origin source", req.ResultID, req.Role)
	}

	key := originSourceID + "\x00" + req.Chain.ChainID
	snapshotID, shared, err := c.cachemoneyHydrationGroup.Do(ctx, key, func(ctx context.Context) (string, error) {
		return c.hydrateRemoteSnapshot(ctx, originSourceID, req)
	})
	if err != nil {
		slog.WarnContext(ctx, "remote cache snapshot hydration failed",
			"result", req.ResultID,
			"role", req.Role,
			"source", originSourceID,
			"chain", req.Chain.ChainID,
			"shared", shared,
			"err", err,
		)
		return nil, false, err
	}
	ref, err := c.snapshotManager.GetBySnapshotID(ctx, snapshotID, bkcache.NoUpdateLastUsed)
	if err != nil {
		return nil, false, fmt.Errorf("remote snapshot materialize result %d role %q: reopen hydrated snapshot %q: %w", req.ResultID, req.Role, snapshotID, err)
	}
	return ref, true, nil
}

func (c *Cache) hydrateRemoteSnapshot(ctx context.Context, sourceID string, req RemoteSnapshotMaterializationRequest) (_ string, rerr error) {
	descs, err := cachemoneyDescriptorsFromPersistedChain(req.Chain)
	if err != nil {
		return "", fmt.Errorf("validate remote snapshot chain: %w", err)
	}
	if err := c.ensureRemoteSnapshotBlobs(ctx, sourceID, descs); err != nil {
		return "", err
	}
	ref, err := c.snapshotManager.ImportImage(ctx, &bkcache.ImportedImage{
		Ref:    fmt.Sprintf("cachemoney/%s/%s", sourceID, req.Chain.ChainID),
		Layers: descs,
	}, bkcache.ImportImageOpts{
		ImageRef:   fmt.Sprintf("cachemoney/%s/%s", sourceID, req.Chain.ChainID),
		RecordType: client.UsageRecordTypeRegular,
	})
	if err != nil {
		return "", fmt.Errorf("import remote snapshot chain %s: %w", req.Chain.ChainID, err)
	}
	defer func() {
		if ref != nil {
			_ = ref.Release(context.WithoutCancel(ctx))
		}
	}()
	if err := c.attachHydratedRemoteSnapshot(ctx, req.ResultID, req.Role, ref); err != nil {
		return "", err
	}
	return ref.SnapshotID(), nil
}

func (c *Cache) ensureRemoteSnapshotBlobs(ctx context.Context, sourceID string, descs []ocispecs.Descriptor) error {
	blobIndex := c.cachemoneyBlobIndex(sourceID)
	writer, _ := c.snapshotManager.(cachemoneyContentBlobWriter)

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(cachemoneyHydrationFetchParallelism)
	for _, desc := range descs {
		desc := desc
		present, err := c.cachemoneyContentPresent(egCtx, desc.Digest)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if writer == nil {
			return fmt.Errorf("remote snapshot blob %s: snapshot manager cannot write content", desc.Digest)
		}
		location, ok := blobIndex[desc.Digest.String()]
		if !ok || location.URL == "" {
			return fmt.Errorf("remote snapshot blob %s: missing blob index location", desc.Digest)
		}
		if err := cachemoneyValidateBlobLocation(desc, location); err != nil {
			return err
		}

		eg.Go(func() error {
			return cachemoneyFetchBlob(egCtx, writer, desc, location)
		})
	}
	return eg.Wait()
}

func cachemoneyFetchBlob(ctx context.Context, writer cachemoneyContentBlobWriter, desc ocispecs.Descriptor, location cachemoneyproto.BlobLocation) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location.URL, nil)
	if err != nil {
		return fmt.Errorf("remote snapshot blob %s: build request: %w", desc.Digest, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("remote snapshot blob %s: fetch: %w", desc.Digest, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("remote snapshot blob %s: fetch status %s", desc.Digest, resp.Status)
	}
	if err := writer.WriteContentBlob(ctx, desc, resp.Body); err != nil {
		return fmt.Errorf("remote snapshot blob %s: write content: %w", desc.Digest, err)
	}
	return nil
}

func (c *Cache) cachemoneyContentPresent(ctx context.Context, dgst digest.Digest) (bool, error) {
	if c == nil || c.snapshotManager == nil {
		return false, nil
	}
	provider, ok := c.snapshotManager.(cachemoneyContentInfoProvider)
	if !ok {
		return false, nil
	}
	_, err := provider.ContentInfo(ctx, dgst)
	if err == nil {
		return true, nil
	}
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("remote snapshot blob %s: content info: %w", dgst, err)
}

func cachemoneyDescriptorsFromPersistedChain(chain PersistedSnapshotChain) ([]ocispecs.Descriptor, error) {
	if chain.ChainID == "" {
		return nil, fmt.Errorf("missing chain ID")
	}
	if len(chain.Layers) == 0 {
		return nil, fmt.Errorf("chain %s has no layers", chain.ChainID)
	}
	diffIDs := make([]digest.Digest, 0, len(chain.Layers))
	descs := make([]ocispecs.Descriptor, 0, len(chain.Layers))
	for i, layer := range chain.Layers {
		desc, diffID, err := cachemoneyDescriptorFromPersistedLayer(i, layer)
		if err != nil {
			return nil, err
		}
		diffIDs = append(diffIDs, diffID)
		descs = append(descs, desc)
	}
	if got := ociidentity.ChainID(diffIDs).String(); got != chain.ChainID {
		return nil, fmt.Errorf("chain ID mismatch: got %s from diff IDs, want %s", got, chain.ChainID)
	}
	return descs, nil
}

func cachemoneyDescriptorFromPersistedLayer(position int, layer PersistedSnapshotChainLayer) (ocispecs.Descriptor, digest.Digest, error) {
	diffID, err := digest.Parse(layer.DiffID)
	if err != nil {
		return ocispecs.Descriptor{}, "", fmt.Errorf("layer %d parse diff ID %q: %w", position, layer.DiffID, err)
	}
	blobDigest, err := digest.Parse(layer.BlobDigest)
	if err != nil {
		return ocispecs.Descriptor{}, "", fmt.Errorf("layer %d parse blob digest %q: %w", position, layer.BlobDigest, err)
	}
	if layer.Size < 0 {
		return ocispecs.Descriptor{}, "", fmt.Errorf("layer %d has negative size %d", position, layer.Size)
	}
	if !cachemoneySupportedLayerMediaType(layer.MediaType) {
		return ocispecs.Descriptor{}, "", fmt.Errorf("layer %d has unsupported media type %q", position, layer.MediaType)
	}

	desc := ocispecs.Descriptor{}
	if len(layer.DescriptorJSON) > 0 {
		if err := json.Unmarshal(layer.DescriptorJSON, &desc); err != nil {
			return ocispecs.Descriptor{}, "", fmt.Errorf("layer %d descriptor JSON: %w", position, err)
		}
	}
	if desc.Digest == "" {
		desc.Digest = blobDigest
	} else if desc.Digest != blobDigest {
		return ocispecs.Descriptor{}, "", fmt.Errorf("layer %d descriptor digest %s does not match row digest %s", position, desc.Digest, blobDigest)
	}
	if desc.Size == 0 {
		desc.Size = layer.Size
	} else if layer.Size != 0 && desc.Size != layer.Size {
		return ocispecs.Descriptor{}, "", fmt.Errorf("layer %d descriptor size %d does not match row size %d", position, desc.Size, layer.Size)
	}
	if desc.MediaType == "" {
		desc.MediaType = layer.MediaType
	} else if desc.MediaType != layer.MediaType {
		return ocispecs.Descriptor{}, "", fmt.Errorf("layer %d descriptor media type %q does not match row media type %q", position, desc.MediaType, layer.MediaType)
	}
	if desc.Annotations == nil {
		desc.Annotations = map[string]string{}
	}
	if got := desc.Annotations[labels.LabelUncompressed]; got != "" && got != diffID.String() {
		return ocispecs.Descriptor{}, "", fmt.Errorf("layer %d descriptor diff ID %q does not match row diff ID %q", position, got, diffID)
	}
	desc.Annotations[labels.LabelUncompressed] = diffID.String()
	return desc, diffID, nil
}

func cachemoneySupportedLayerMediaType(mediaType string) bool {
	switch mediaType {
	case ocispecs.MediaTypeImageLayer,
		ocispecs.MediaTypeImageLayerGzip,
		ocispecs.MediaTypeImageLayerZstd,
		ocispecs.MediaTypeImageLayerNonDistributable,
		ocispecs.MediaTypeImageLayerNonDistributableGzip,
		ocispecs.MediaTypeImageLayerNonDistributableZstd,
		images.MediaTypeDockerSchema2Layer,
		images.MediaTypeDockerSchema2LayerGzip,
		images.MediaTypeDockerSchema2LayerZstd,
		images.MediaTypeDockerSchema2LayerForeign,
		images.MediaTypeDockerSchema2LayerForeignGzip:
		return true
	default:
		return false
	}
}

func cachemoneyValidateBlobLocation(desc ocispecs.Descriptor, location cachemoneyproto.BlobLocation) error {
	if location.Size != 0 && desc.Size != 0 && location.Size != desc.Size {
		return fmt.Errorf("remote snapshot blob %s: location size %d does not match descriptor size %d", desc.Digest, location.Size, desc.Size)
	}
	if location.MediaType != "" && desc.MediaType != "" && location.MediaType != desc.MediaType {
		return fmt.Errorf("remote snapshot blob %s: location media type %q does not match descriptor media type %q", desc.Digest, location.MediaType, desc.MediaType)
	}
	return nil
}

func (c *Cache) cachemoneyOriginSourceID(ctx context.Context, resultID uint64) (string, error) {
	res, _, _, err := c.sharedResultByResultID(ctx, "", sharedResultID(resultID), sharedResultLookupExact)
	if err != nil {
		return "", err
	}
	return res.originSourceID, nil
}

func (c *Cache) attachHydratedRemoteSnapshot(ctx context.Context, resultID uint64, role string, ref bkcache.ImmutableRef) error {
	if ref == nil {
		return fmt.Errorf("remote snapshot materialize result %d role %q: nil hydrated ref", resultID, role)
	}
	res, _, _, err := c.sharedResultByResultID(ctx, "", sharedResultID(resultID), sharedResultLookupExact)
	if err != nil {
		return err
	}
	if c.snapshotManager != nil {
		if err := c.snapshotManager.AttachLease(ctx, resultSnapshotLeaseID(res.id, role), ref.SnapshotID()); err != nil {
			return fmt.Errorf("attach hydrated remote snapshot lease: %w", err)
		}
	}

	links := res.loadSnapshotOwnerLinks()
	replaced := false
	for i := range links {
		if links[i].Role == role {
			links[i].RefKey = ref.SnapshotID()
			replaced = true
			break
		}
	}
	if !replaced {
		links = append(links, PersistedSnapshotRefLink{
			RefKey: ref.SnapshotID(),
			Role:   role,
		})
	}
	res.storeSnapshotOwnerLinks(links)
	return nil
}

func (c *Cache) storeCachemoneyBlobIndex(sourceID string, blobIndex map[string]cachemoneyproto.BlobLocation) {
	if c == nil || sourceID == "" {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()

	if c.cachemoneyBlobIndexBySource == nil {
		c.cachemoneyBlobIndexBySource = map[string]map[string]cachemoneyproto.BlobLocation{}
	}
	c.cachemoneyBlobIndexBySource[sourceID] = cachemoneyCloneBlobIndex(blobIndex)
}

func (c *Cache) cachemoneyBlobIndex(sourceID string) map[string]cachemoneyproto.BlobLocation {
	if c == nil || sourceID == "" {
		return nil
	}
	c.cachemoneyMu.RLock()
	defer c.cachemoneyMu.RUnlock()

	return cachemoneyCloneBlobIndex(c.cachemoneyBlobIndexBySource[sourceID])
}

func cachemoneyCloneBlobIndex(blobIndex map[string]cachemoneyproto.BlobLocation) map[string]cachemoneyproto.BlobLocation {
	if len(blobIndex) == 0 {
		return nil
	}
	out := make(map[string]cachemoneyproto.BlobLocation, len(blobIndex))
	for dgst, location := range blobIndex {
		out[dgst] = location
	}
	return out
}
