package snapshots

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/v2/core/content"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/dagger/dagger/dagql/cachemoneyproto"
	"github.com/dagger/dagger/internal/buildkit/client"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type RemoteCacheSource struct {
	ID        string
	client    *http.Client
	snapshots map[string]cachemoneyproto.RemoteSnapshot
}

func NewRemoteCacheSource(id string, snapshots []cachemoneyproto.RemoteSnapshot, client *http.Client) *RemoteCacheSource {
	source := &RemoteCacheSource{
		ID:        id,
		client:    client,
		snapshots: make(map[string]cachemoneyproto.RemoteSnapshot, len(snapshots)),
	}
	if source.client == nil {
		source.client = http.DefaultClient
	}
	for _, snapshot := range snapshots {
		if snapshot.RefKey == "" {
			continue
		}
		source.snapshots[snapshot.RefKey] = snapshot
	}
	return source
}

func (s *RemoteCacheSource) HydrateSnapshot(ctx context.Context, refKey string, sm SnapshotManager) (ImmutableRef, error) {
	if s == nil {
		return nil, errors.New("hydrate remote cache snapshot: nil source")
	}
	if sm == nil {
		return nil, errors.New("hydrate remote cache snapshot: nil snapshot manager")
	}
	snapshot, ok := s.snapshots[refKey]
	if !ok {
		return nil, fmt.Errorf("hydrate remote cache snapshot %q: missing snapshot index entry", refKey)
	}
	manager, ok := sm.(*snapshotManager)
	if !ok {
		return nil, fmt.Errorf("hydrate remote cache snapshot %q: unsupported snapshot manager %T", refKey, sm)
	}
	layers := make([]ocispecs.Descriptor, 0, len(snapshot.Layers))
	for _, layer := range snapshot.Layers {
		desc := layer.Descriptor
		if err := s.copyBlobToContentStore(ctx, manager.ContentStore, layer); err != nil {
			return nil, fmt.Errorf("hydrate remote cache snapshot %q layer %s: %w", refKey, desc.Digest, err)
		}
		layers = append(layers, desc)
	}
	return manager.ImportImage(ctx, &ImportedImage{
		Ref:    "dagger-cachemoney:" + s.ID + "/" + refKey,
		Layers: layers,
	}, ImportImageOpts{
		ImageRef:   "dagger-cachemoney:" + s.ID + "/" + refKey,
		RecordType: client.UsageRecordTypeRegular,
	})
}

func (s *RemoteCacheSource) AddSnapshotToBundle(ctx context.Context, writer *CacheBundleWriter, refKey string) (BundleSnapshot, error) {
	if s == nil {
		return BundleSnapshot{}, errors.New("add remote cache snapshot: nil source")
	}
	if writer == nil {
		return BundleSnapshot{}, errors.New("add remote cache snapshot: nil writer")
	}
	snapshot, ok := s.snapshots[refKey]
	if !ok {
		return BundleSnapshot{}, fmt.Errorf("add remote cache snapshot %q: missing snapshot index entry", refKey)
	}
	bundleSnapshot := BundleSnapshot{
		RefKey:  snapshot.RefKey,
		ChainID: snapshot.ChainID,
		Layers:  make([]ocispecs.Descriptor, 0, len(snapshot.Layers)),
	}
	for _, layer := range snapshot.Layers {
		if err := s.copyBlobToBundle(ctx, writer, layer); err != nil {
			return BundleSnapshot{}, fmt.Errorf("add remote cache snapshot %q layer %s: %w", refKey, layer.Descriptor.Digest, err)
		}
		bundleSnapshot.Layers = append(bundleSnapshot.Layers, layer.Descriptor)
	}
	writer.snapshots[bundleSnapshot.RefKey] = bundleSnapshot
	if err := writer.writeSnapshotIndex(); err != nil {
		return BundleSnapshot{}, err
	}
	return bundleSnapshot, nil
}

func (s *RemoteCacheSource) copyBlobToContentStore(ctx context.Context, store content.Store, layer cachemoneyproto.RemoteLayer) error {
	rc, err := s.openLayer(ctx, layer)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := content.WriteBlob(ctx, store, layer.Descriptor.Digest.String(), rc, layer.Descriptor); err != nil {
		if cerrdefs.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

func (s *RemoteCacheSource) copyBlobToBundle(ctx context.Context, writer *CacheBundleWriter, layer cachemoneyproto.RemoteLayer) error {
	path, err := cacheBundleBlobPath(writer.Dir, layer.Descriptor.Digest)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rc, err := s.openLayer(ctx, layer)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".blob-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := copyAndVerifyDescriptor(tmp, rc, layer.Descriptor); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *RemoteCacheSource) openLayer(ctx context.Context, layer cachemoneyproto.RemoteLayer) (io.ReadCloser, error) {
	if layer.URL == "" {
		return nil, fmt.Errorf("remote cache layer %s has empty URL", layer.Descriptor.Digest)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, layer.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: status %d", layer.URL, resp.StatusCode)
	}
	return resp.Body, nil
}
