package dagql

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/dagger/dagger/dagql/cachemoneyproto"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/opencontainers/go-digest"
	ociidentity "github.com/opencontainers/image-spec/identity"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/vektah/gqlparser/v2/ast"
	"gotest.tools/v3/assert"
)

func TestDebugCachemoneyExportPostsMultipartUploadsRequestedBlobsAndCompletes(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	blobBytes := []byte("debug-export-blob")
	blobDigest := digest.FromBytes(blobBytes)
	diffID := digest.FromString("debug-export-diff")
	chainID := ociidentity.ChainID([]digest.Digest{diffID}).String()
	exportRef := &fakeCachemoneyExportRef{
		snapshotID: "snapshot-debug-export",
		chain: &bkcache.ExportChain{
			Layers: []bkcache.ExportLayer{{
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobDigest,
					Size:      int64(len(blobBytes)),
					Annotations: map[string]string{
						labels.LabelUncompressed: diffID.String(),
					},
				},
			}},
		},
	}
	manager := &fakeSnapshotManager{
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"snapshot-debug-export": exportRef,
		},
		contentByDigest: map[digest.Digest][]byte{
			blobDigest: blobBytes,
		},
	}
	c, resultID := cachemoneyDebugTestCache(t, ctx, manager, "cachemoney-debug-export", "snapshot-debug-export")
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	var mu sync.Mutex
	var sawManifest bool
	var sawMetadata bool
	var uploadedBlob []byte
	var completed cachemoneyproto.CompleteExportRequest
	var backend *httptest.Server
	backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/begin":
			assert.Equal(t, r.Method, http.MethodPost)
			mr, err := r.MultipartReader()
			assert.NilError(t, err)
			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				assert.NilError(t, err)
				switch part.FormName() {
				case cachemoneyproto.MultipartManifestField:
					var manifest cachemoneyproto.BeginExportManifest
					assert.NilError(t, json.NewDecoder(part).Decode(&manifest))
					assert.Equal(t, manifest.Version, cachemoneyproto.Version)
					assert.DeepEqual(t, manifest.Snapshots, []cachemoneyproto.SnapshotOffer{{
						ResultID: resultID,
						Role:     "snapshot",
						ChainID:  chainID,
					}})
					sawManifest = true
				case cachemoneyproto.MultipartMetadataField:
					metadataBytes, err := io.ReadAll(part)
					assert.NilError(t, err)
					assert.Assert(t, len(metadataBytes) > 0)
					sawMetadata = true
				}
			}
			assert.NilError(t, json.NewEncoder(w).Encode(cachemoneyproto.BeginExportResponse{
				Version:        cachemoneyproto.Version,
				ExportID:       "debug-export",
				RequestedBlobs: []string{blobDigest.String()},
				UploadURL:      backend.URL + "/upload",
				CompleteURL:    backend.URL + "/complete",
			}))
		case "/upload":
			assert.Equal(t, r.Method, http.MethodPost)
			var req cachemoneyproto.BlobUploadRequest
			assert.NilError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, req.BlobDigest, blobDigest.String())
			assert.NilError(t, json.NewEncoder(w).Encode(cachemoneyproto.BlobUploadResponse{
				Method: http.MethodPut,
				URL:    backend.URL + "/blob",
			}))
		case "/blob":
			assert.Equal(t, r.Method, http.MethodPut)
			body, err := io.ReadAll(r.Body)
			assert.NilError(t, err)
			mu.Lock()
			uploadedBlob = append([]byte(nil), body...)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/complete":
			assert.Equal(t, r.Method, http.MethodPost)
			assert.NilError(t, json.NewDecoder(r.Body).Decode(&completed))
			assert.NilError(t, json.NewEncoder(w).Encode(cachemoneyproto.CompleteExportResponse{
				Version:  cachemoneyproto.Version,
				ExportID: "debug-export",
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(backend.Close)

	result, err := c.DebugCachemoneyExport(ctx, backend.URL+"/begin")
	assert.NilError(t, err)
	assert.Equal(t, result.ExportID, "debug-export")
	assert.Equal(t, result.Snapshots, 1)
	assert.Equal(t, result.BlobsOffered, 1)
	assert.Equal(t, result.BlobsRequested, 1)
	assert.Equal(t, result.BlobsUploaded, 1)
	assert.Assert(t, result.Completed)
	assert.Assert(t, sawManifest)
	assert.Assert(t, sawMetadata)
	assert.DeepEqual(t, uploadedBlob, blobBytes)
	assert.DeepEqual(t, completed.Blobs, []string{blobDigest.String()})

	stats := c.DebugCachemoneyStats()
	assert.Equal(t, stats.ExportsStarted, uint64(1))
	assert.Equal(t, stats.ExportsCompleted, uint64(1))
	assert.Equal(t, stats.SnapshotsOffered, uint64(1))
	assert.Equal(t, stats.SnapshotsRequested, uint64(1))
	assert.Equal(t, stats.BlobsUploadRequested, uint64(1))
	assert.Equal(t, stats.BlobsUploaded, uint64(1))
}

func TestDebugCachemoneyExportCompletesAfterPartialBlobUploadFailure(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	blobABytes := []byte("debug-export-blob-a")
	blobADigest := digest.FromBytes(blobABytes)
	diffA := digest.FromString("debug-export-diff-a")
	blobBBytes := []byte("debug-export-blob-b")
	blobBDigest := digest.FromBytes(blobBBytes)
	diffB := digest.FromString("debug-export-diff-b")
	chainID := ociidentity.ChainID([]digest.Digest{diffA, diffB}).String()
	exportRef := &fakeCachemoneyExportRef{
		snapshotID: "snapshot-debug-export-partial",
		chain: &bkcache.ExportChain{
			Layers: []bkcache.ExportLayer{{
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobADigest,
					Size:      int64(len(blobABytes)),
					Annotations: map[string]string{
						labels.LabelUncompressed: diffA.String(),
					},
				},
			}, {
				Descriptor: ocispecs.Descriptor{
					MediaType: ocispecs.MediaTypeImageLayerZstd,
					Digest:    blobBDigest,
					Size:      int64(len(blobBBytes)),
					Annotations: map[string]string{
						labels.LabelUncompressed: diffB.String(),
					},
				},
			}},
		},
	}
	manager := &fakeSnapshotManager{
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"snapshot-debug-export-partial": exportRef,
		},
		contentByDigest: map[digest.Digest][]byte{
			blobADigest: blobABytes,
			blobBDigest: blobBBytes,
		},
	}
	c, resultID := cachemoneyDebugTestCache(t, ctx, manager, "cachemoney-debug-export-partial", "snapshot-debug-export-partial")
	defer func() {
		assert.NilError(t, c.Close(context.Background()))
	}()

	var completed cachemoneyproto.CompleteExportRequest
	var uploadedBlobA []byte
	var backend *httptest.Server
	backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/begin":
			var manifest cachemoneyproto.BeginExportManifest
			mr, err := r.MultipartReader()
			assert.NilError(t, err)
			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				assert.NilError(t, err)
				if part.FormName() == cachemoneyproto.MultipartManifestField {
					assert.NilError(t, json.NewDecoder(part).Decode(&manifest))
				}
			}
			assert.DeepEqual(t, manifest.Snapshots, []cachemoneyproto.SnapshotOffer{{
				ResultID: resultID,
				Role:     "snapshot",
				ChainID:  chainID,
			}})
			assert.NilError(t, json.NewEncoder(w).Encode(cachemoneyproto.BeginExportResponse{
				Version:        cachemoneyproto.Version,
				ExportID:       "debug-export-partial",
				RequestedBlobs: []string{blobADigest.String(), blobBDigest.String()},
				UploadURL:      backend.URL + "/upload",
				CompleteURL:    backend.URL + "/complete",
			}))
		case "/upload":
			var req cachemoneyproto.BlobUploadRequest
			assert.NilError(t, json.NewDecoder(r.Body).Decode(&req))
			uploadURL := backend.URL + "/blob-a"
			if req.BlobDigest == blobBDigest.String() {
				uploadURL = backend.URL + "/blob-b"
			}
			assert.NilError(t, json.NewEncoder(w).Encode(cachemoneyproto.BlobUploadResponse{
				Method: http.MethodPut,
				URL:    uploadURL,
			}))
		case "/blob-a":
			body, err := io.ReadAll(r.Body)
			assert.NilError(t, err)
			uploadedBlobA = append([]byte(nil), body...)
			w.WriteHeader(http.StatusNoContent)
		case "/blob-b":
			http.Error(w, "simulated upload failure", http.StatusInternalServerError)
		case "/complete":
			assert.NilError(t, json.NewDecoder(r.Body).Decode(&completed))
			assert.NilError(t, json.NewEncoder(w).Encode(cachemoneyproto.CompleteExportResponse{
				Version:  cachemoneyproto.Version,
				ExportID: "debug-export-partial",
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(backend.Close)

	result, err := c.DebugCachemoneyExport(ctx, backend.URL+"/begin")
	assert.NilError(t, err)
	assert.Equal(t, result.ExportID, "debug-export-partial")
	assert.Equal(t, result.BlobsRequested, 2)
	assert.Equal(t, result.BlobsUploaded, 1)
	assert.Equal(t, result.BlobsFailed, 1)
	assert.Assert(t, result.Completed)
	assert.DeepEqual(t, uploadedBlobA, blobABytes)
	assert.DeepEqual(t, completed.Blobs, []string{blobADigest.String()})

	stats := c.DebugCachemoneyStats()
	assert.Equal(t, stats.BlobsUploadRequested, uint64(2))
	assert.Equal(t, stats.BlobsUploaded, uint64(1))
	assert.Equal(t, stats.BlobsUploadFailed, uint64(1))
	assert.Equal(t, stats.ExportsCompleted, uint64(1))
}

func TestDebugCachemoneyImportFetchesMultipartAndImportsMetadata(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	sourceDBPath := filepath.Join(t.TempDir(), "source.db")
	sourceCacheIface, err := NewCache(ctx, sourceDBPath, nil, nil)
	assert.NilError(t, err)
	sourceCache := sourceCacheIface
	defer func() {
		assert.NilError(t, sourceCache.Close(context.Background()))
	}()

	key := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType(&ast.Type{NamedType: "Int", NonNull: true}),
		Field: "cachemoney-debug-import-source",
	}
	res, err := sourceCache.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestIntResult(key, 42), nil
	})
	assert.NilError(t, err)
	sourceResultID := uint64(res.cacheSharedResult().id)

	metadataDBPath := filepath.Join(t.TempDir(), cachemoneyproto.MetadataDBName)
	prepared, err := sourceCache.PrepareCachemoneyExport(ctx, metadataDBPath)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, prepared.Release(context.Background()))
	}()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.Method, http.MethodGet)
		mw := multipart.NewWriter(w)
		w.Header().Set("Content-Type", mw.FormDataContentType())
		manifestPart, err := mw.CreateFormFile(cachemoneyproto.MultipartManifestField, cachemoneyproto.ManifestName)
		assert.NilError(t, err)
		assert.NilError(t, json.NewEncoder(manifestPart).Encode(cachemoneyproto.ImportManifest{
			Version:          cachemoneyproto.Version,
			MetadataSourceID: "debug-source",
		}))
		metadataPart, err := mw.CreateFormFile(cachemoneyproto.MultipartMetadataField, cachemoneyproto.MetadataDBName)
		assert.NilError(t, err)
		metadataFile, err := os.Open(prepared.MetadataDBPath)
		assert.NilError(t, err)
		defer metadataFile.Close()
		_, err = io.Copy(metadataPart, metadataFile)
		assert.NilError(t, err)
		assert.NilError(t, mw.Close())
	}))
	t.Cleanup(backend.Close)

	destDBPath := filepath.Join(t.TempDir(), "dest.db")
	destCacheIface, err := NewCache(ctx, destDBPath, nil, nil)
	assert.NilError(t, err)
	destCache := destCacheIface
	defer func() {
		assert.NilError(t, destCache.Close(context.Background()))
	}()

	result, err := destCache.DebugCachemoneyImport(ctx, backend.URL)
	assert.NilError(t, err)
	assert.Equal(t, result.SourceID, "debug-source")
	assert.Assert(t, result.Imported)
	imported := cachemoneyImportedResultByOrigin(destCache, "debug-source", sourceResultID)
	assert.Assert(t, imported != nil)
	assert.Assert(t, imported.remoteCacheImported)

	stats := destCache.DebugCachemoneyStats()
	assert.Equal(t, stats.ImportsStarted, uint64(1))
	assert.Equal(t, stats.ImportsCompleted, uint64(1))
}

func cachemoneyDebugTestCache(t *testing.T, ctx context.Context, manager *fakeSnapshotManager, field string, snapshotID string) (*Cache, uint64) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "cache.db")
	cacheIface, err := NewCache(ctx, dbPath, manager, nil)
	assert.NilError(t, err)
	c := cacheIface

	key := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&persistSnapshotValue{}).Type()),
		Field: field,
	}
	res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, &CallRequest{
		ResultCall:    key,
		IsPersistable: true,
	}, func(context.Context) (AnyResult, error) {
		return cacheTestPlainResult(&persistSnapshotValue{
			Name:       "x",
			SnapshotID: snapshotID,
		}), nil
	})
	assert.NilError(t, err)
	return c, uint64(res.cacheSharedResult().id)
}
