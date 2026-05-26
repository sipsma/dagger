package snapshots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/dagger/dagger/internal/buildkit/client"
	"github.com/dagger/dagger/internal/buildkit/identity"
	"github.com/dagger/dagger/internal/buildkit/util/compression"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	cacheBundleVersion = 1
	CacheBundleDBName  = "dagql-cache.db"
)

type CacheBundle struct {
	Dir      string
	ID       string
	snapshot map[string]BundleSnapshot
}

type CacheBundleWriter struct {
	Dir       string
	ID        string
	snapshots map[string]BundleSnapshot
}

type cacheBundleManifest struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
}

type cacheBundleSnapshotIndex struct {
	Snapshots []BundleSnapshot `json:"snapshots"`
}

type BundleSnapshot struct {
	RefKey  string                `json:"refKey"`
	ChainID string                `json:"chainID,omitempty"`
	Layers  []ocispecs.Descriptor `json:"layers"`
}

func OpenCacheBundle(dir string) (*CacheBundle, error) {
	manifest, err := readCacheBundleManifest(dir)
	if err != nil {
		return nil, err
	}
	index, err := readCacheBundleSnapshotIndex(dir)
	if err != nil {
		return nil, err
	}
	bundle := &CacheBundle{
		Dir:      dir,
		ID:       manifest.ID,
		snapshot: make(map[string]BundleSnapshot, len(index.Snapshots)),
	}
	for _, snapshot := range index.Snapshots {
		if snapshot.RefKey == "" {
			return nil, fmt.Errorf("open cache bundle %s: snapshot index contains empty ref key", dir)
		}
		bundle.snapshot[snapshot.RefKey] = snapshot
	}
	return bundle, nil
}

func (b *CacheBundle) MetadataDBPath() string {
	if b == nil {
		return ""
	}
	return filepath.Join(b.Dir, CacheBundleDBName)
}

func (w *CacheBundleWriter) MetadataDBPath() string {
	if w == nil {
		return ""
	}
	return filepath.Join(w.Dir, CacheBundleDBName)
}

func CreateCacheBundle(dir string) (*CacheBundleWriter, error) {
	if err := os.MkdirAll(filepath.Join(dir, "snapshots"), 0o700); err != nil {
		return nil, fmt.Errorf("create cache bundle snapshots dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o700); err != nil {
		return nil, fmt.Errorf("create cache bundle blobs dir: %w", err)
	}
	writer := &CacheBundleWriter{
		Dir:       dir,
		ID:        "bundle-" + identity.NewID(),
		snapshots: make(map[string]BundleSnapshot),
	}
	if err := writer.writeManifest(); err != nil {
		return nil, err
	}
	if err := writer.writeSnapshotIndex(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *CacheBundleWriter) AddSnapshot(ctx context.Context, ref ImmutableRef) (BundleSnapshot, error) {
	if w == nil {
		return BundleSnapshot{}, errors.New("add cache bundle snapshot: nil writer")
	}
	if ref == nil {
		return BundleSnapshot{}, errors.New("add cache bundle snapshot: nil ref")
	}
	chain, err := ref.ExportChain(ctx, config.RefConfig{
		Compression: compression.New(compression.Default),
	})
	if err != nil {
		return BundleSnapshot{}, fmt.Errorf("export snapshot chain %q: %w", ref.SnapshotID(), err)
	}
	snapshot := BundleSnapshot{
		RefKey: ref.SnapshotID(),
		Layers: make([]ocispecs.Descriptor, 0, len(chain.Layers)),
	}
	for _, layer := range chain.Layers {
		desc := layer.Descriptor
		if err := w.copyBlob(ctx, chain.Provider, desc); err != nil {
			return BundleSnapshot{}, fmt.Errorf("copy snapshot %q layer %s: %w", ref.SnapshotID(), desc.Digest, err)
		}
		snapshot.Layers = append(snapshot.Layers, desc)
		snapshot.ChainID += desc.Digest.String() + "\n"
	}
	w.snapshots[snapshot.RefKey] = snapshot
	if err := w.writeSnapshotIndex(); err != nil {
		return BundleSnapshot{}, err
	}
	return snapshot, nil
}

func (w *CacheBundleWriter) AddSnapshotFromBundle(ctx context.Context, source *CacheBundle, refKey string) (BundleSnapshot, error) {
	if w == nil {
		return BundleSnapshot{}, errors.New("add cache bundle snapshot from bundle: nil writer")
	}
	if source == nil {
		return BundleSnapshot{}, errors.New("add cache bundle snapshot from bundle: nil source")
	}
	if refKey == "" {
		return BundleSnapshot{}, errors.New("add cache bundle snapshot from bundle: empty ref key")
	}
	snapshot, ok := source.snapshot[refKey]
	if !ok {
		return BundleSnapshot{}, fmt.Errorf("add cache bundle snapshot from bundle %q: missing snapshot %q", source.ID, refKey)
	}
	for _, desc := range snapshot.Layers {
		if err := w.copyBlobFromBundle(ctx, source, desc); err != nil {
			return BundleSnapshot{}, fmt.Errorf("copy source bundle %q snapshot %q layer %s: %w", source.ID, refKey, desc.Digest, err)
		}
	}
	w.snapshots[snapshot.RefKey] = snapshot
	if err := w.writeSnapshotIndex(); err != nil {
		return BundleSnapshot{}, err
	}
	return snapshot, nil
}

func (b *CacheBundle) HydrateSnapshot(ctx context.Context, refKey string, sm SnapshotManager) (ImmutableRef, error) {
	if b == nil {
		return nil, errors.New("hydrate cache bundle snapshot: nil bundle")
	}
	if sm == nil {
		return nil, errors.New("hydrate cache bundle snapshot: nil snapshot manager")
	}
	snapshot, ok := b.snapshot[refKey]
	if !ok {
		return nil, fmt.Errorf("hydrate cache bundle snapshot %q: missing snapshot index entry", refKey)
	}
	manager, ok := sm.(*snapshotManager)
	if !ok {
		return nil, fmt.Errorf("hydrate cache bundle snapshot %q: unsupported snapshot manager %T", refKey, sm)
	}
	for _, desc := range snapshot.Layers {
		if err := b.copyBlobToContentStore(ctx, manager.ContentStore, desc); err != nil {
			return nil, fmt.Errorf("hydrate cache bundle snapshot %q layer %s: %w", refKey, desc.Digest, err)
		}
	}
	return manager.ImportImage(ctx, &ImportedImage{
		Ref:    "dagger-cache-bundle:" + b.ID + "/" + refKey,
		Layers: snapshot.Layers,
	}, ImportImageOpts{
		ImageRef:   "dagger-cache-bundle:" + b.ID + "/" + refKey,
		RecordType: client.UsageRecordTypeRegular,
	})
}

func (w *CacheBundleWriter) copyBlob(ctx context.Context, provider content.Provider, desc ocispecs.Descriptor) error {
	if desc.Digest == "" {
		return errors.New("empty blob digest")
	}
	path, err := cacheBundleBlobPath(w.Dir, desc.Digest)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ra, err := provider.ReaderAt(ctx, desc)
	if err != nil {
		return err
	}
	defer ra.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".blob-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := copyAndVerifyDescriptor(tmp, content.NewReader(ra), desc); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (w *CacheBundleWriter) copyBlobFromBundle(_ context.Context, source *CacheBundle, desc ocispecs.Descriptor) error {
	if desc.Digest == "" {
		return errors.New("empty blob digest")
	}
	srcPath, err := cacheBundleBlobPath(source.Dir, desc.Digest)
	if err != nil {
		return err
	}
	dstPath, err := cacheBundleBlobPath(w.Dir, desc.Digest)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dstPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dstPath), ".blob-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := copyAndVerifyDescriptor(tmp, src, desc); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dstPath)
}

func (b *CacheBundle) copyBlobToContentStore(ctx context.Context, store content.Store, desc ocispecs.Descriptor) error {
	path, err := cacheBundleBlobPath(b.Dir, desc.Digest)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := content.WriteBlob(ctx, store, desc.Digest.String(), f, desc); err != nil {
		if cerrdefs.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

func copyAndVerifyDescriptor(dst io.Writer, src io.Reader, desc ocispecs.Descriptor) error {
	verifier := desc.Digest.Verifier()
	n, err := io.Copy(io.MultiWriter(dst, verifier), src)
	if err != nil {
		return err
	}
	if desc.Size > 0 && n != desc.Size {
		return fmt.Errorf("copied size %d does not match descriptor size %d", n, desc.Size)
	}
	if !verifier.Verified() {
		return fmt.Errorf("copied blob digest does not match descriptor digest %s", desc.Digest)
	}
	return nil
}

func readCacheBundleManifest(dir string) (cacheBundleManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return cacheBundleManifest{}, fmt.Errorf("read cache bundle manifest: %w", err)
	}
	var manifest cacheBundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return cacheBundleManifest{}, fmt.Errorf("decode cache bundle manifest: %w", err)
	}
	if manifest.Version != cacheBundleVersion {
		return cacheBundleManifest{}, fmt.Errorf("unsupported cache bundle version %d", manifest.Version)
	}
	if manifest.ID == "" {
		return cacheBundleManifest{}, errors.New("cache bundle manifest missing id")
	}
	return manifest, nil
}

func (w *CacheBundleWriter) writeManifest() error {
	return writeCacheBundleJSON(filepath.Join(w.Dir, "manifest.json"), cacheBundleManifest{
		Version: cacheBundleVersion,
		ID:      w.ID,
	})
}

func readCacheBundleSnapshotIndex(dir string) (cacheBundleSnapshotIndex, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "snapshots", "index.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cacheBundleSnapshotIndex{}, nil
		}
		return cacheBundleSnapshotIndex{}, fmt.Errorf("read cache bundle snapshot index: %w", err)
	}
	var index cacheBundleSnapshotIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		return cacheBundleSnapshotIndex{}, fmt.Errorf("decode cache bundle snapshot index: %w", err)
	}
	return index, nil
}

func (w *CacheBundleWriter) writeSnapshotIndex() error {
	index := cacheBundleSnapshotIndex{
		Snapshots: make([]BundleSnapshot, 0, len(w.snapshots)),
	}
	for _, snapshot := range w.snapshots {
		index.Snapshots = append(index.Snapshots, snapshot)
	}
	return writeCacheBundleJSON(filepath.Join(w.Dir, "snapshots", "index.json"), index)
}

func writeCacheBundleJSON(path string, val any) error {
	raw, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".json-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func cacheBundleBlobPath(dir string, dgst digest.Digest) (string, error) {
	if dgst == "" {
		return "", errors.New("empty digest")
	}
	if dgst.Algorithm() != digest.SHA256 {
		return "", fmt.Errorf("unsupported cache bundle digest algorithm %q", dgst.Algorithm())
	}
	encoded := dgst.Encoded()
	if encoded == "" || strings.Contains(encoded, string(filepath.Separator)) {
		return "", fmt.Errorf("invalid digest encoding %q", encoded)
	}
	return filepath.Join(dir, "blobs", "sha256", encoded), nil
}
