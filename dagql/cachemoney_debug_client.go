package dagql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dagger/dagger/dagql/cachemoneyproto"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type CachemoneyDebugExportResult struct {
	BeginURL        string `json:"begin_url"`
	ExportID        string `json:"export_id,omitempty"`
	Snapshots       int    `json:"snapshots"`
	BlobsOffered    int    `json:"blobs_offered"`
	BlobsRequested  int    `json:"blobs_requested"`
	BlobsUploaded   int    `json:"blobs_uploaded"`
	BlobsFailed     int    `json:"blobs_failed"`
	BlobsSkipped    int    `json:"blobs_skipped"`
	Completed       bool   `json:"completed"`
	MetadataDBBytes int64  `json:"metadata_db_bytes,omitempty"`
}

type CachemoneyDebugImportResult struct {
	URL             string `json:"url"`
	SourceID        string `json:"source_id"`
	BlobLocations   int    `json:"blob_locations"`
	MetadataDBBytes int64  `json:"metadata_db_bytes,omitempty"`
	Imported        bool   `json:"imported"`
}

type cachemoneyContentBlobReader interface {
	ReadContentBlob(context.Context, ocispecs.Descriptor) (io.ReadCloser, error)
}

func (c *Cache) DebugCachemoneyExport(ctx context.Context, beginURL string) (_ *CachemoneyDebugExportResult, rerr error) {
	if c == nil {
		return nil, errors.New("debug cachemoney export: nil cache")
	}
	if beginURL == "" {
		return nil, errors.New("debug cachemoney export: empty url")
	}
	reader, ok := c.snapshotManager.(cachemoneyContentBlobReader)
	if !ok {
		return nil, errors.New("debug cachemoney export: snapshot manager cannot read content blobs")
	}
	c.recordCachemoneyExportStarted()

	metadataDBPath, err := cachemoneyTempDBPath()
	if err != nil {
		return nil, err
	}
	defer wipeSQLiteFiles(metadataDBPath) //nolint:errcheck

	prepared, err := c.PrepareCachemoneyExport(ctx, metadataDBPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if releaseErr := prepared.Release(context.WithoutCancel(ctx)); releaseErr != nil && rerr == nil {
			rerr = releaseErr
		}
	}()

	blobDescs, err := cachemoneyManifestBlobDescriptors(prepared.Manifest)
	if err != nil {
		return nil, err
	}
	c.recordCachemoneyExportOffer(len(prepared.Manifest.Snapshots), len(blobDescs))

	result := &CachemoneyDebugExportResult{
		BeginURL:     beginURL,
		Snapshots:    len(prepared.Manifest.Snapshots),
		BlobsOffered: len(blobDescs),
	}
	if info, err := os.Stat(prepared.MetadataDBPath); err == nil {
		result.MetadataDBBytes = info.Size()
	}

	beginResp, err := cachemoneyPostBeginExport(ctx, beginURL, prepared)
	if err != nil {
		return nil, err
	}
	result.ExportID = beginResp.ExportID
	result.BlobsRequested = len(beginResp.RequestedBlobs)
	c.recordCachemoneyExportBeginAccepted(len(prepared.Manifest.Snapshots), len(beginResp.RequestedBlobs))

	uploaded := make([]string, 0, len(beginResp.RequestedBlobs))
	for _, blobDigest := range beginResp.RequestedBlobs {
		desc, ok := blobDescs[blobDigest]
		if !ok {
			return nil, fmt.Errorf("debug cachemoney export: backend requested unknown blob %s", blobDigest)
		}
		alreadyExists, err := cachemoneyUploadBlob(ctx, reader, beginResp.UploadURL, desc)
		if err != nil {
			result.BlobsFailed++
			c.recordCachemoneyBlobUploadFailed()
			continue
		}
		c.recordCachemoneyBlobUpload(alreadyExists)
		if alreadyExists {
			result.BlobsSkipped++
			continue
		}
		result.BlobsUploaded++
		uploaded = append(uploaded, blobDigest)
	}

	if err := cachemoneyCompleteExport(ctx, beginResp, uploaded); err != nil {
		return nil, err
	}
	result.Completed = true
	c.recordCachemoneyExportCompleted()
	return result, nil
}

func (c *Cache) DebugCachemoneyImport(ctx context.Context, url string) (*CachemoneyDebugImportResult, error) {
	if c == nil {
		return nil, errors.New("debug cachemoney import: nil cache")
	}
	if url == "" {
		return nil, errors.New("debug cachemoney import: empty url")
	}
	c.recordCachemoneyImportStarted()

	metadataDBPath, err := cachemoneyTempDBPath()
	if err != nil {
		return nil, err
	}
	defer wipeSQLiteFiles(metadataDBPath) //nolint:errcheck

	manifest, metadataBytes, err := cachemoneyFetchImport(ctx, url, metadataDBPath)
	if err != nil {
		return nil, err
	}
	if manifest.MetadataSourceID == "" {
		return nil, errors.New("debug cachemoney import: missing metadata source ID")
	}
	if err := c.ImportCachemoneyMetadata(ctx, CachemoneyImportSource{
		ID:             manifest.MetadataSourceID,
		MetadataDBPath: metadataDBPath,
		BlobIndex:      manifest.BlobIndex,
	}); err != nil {
		return nil, err
	}
	c.recordCachemoneyImportCompleted()
	return &CachemoneyDebugImportResult{
		URL:             url,
		SourceID:        manifest.MetadataSourceID,
		BlobLocations:   len(manifest.BlobIndex),
		MetadataDBBytes: metadataBytes,
		Imported:        true,
	}, nil
}

func cachemoneyTempDBPath() (string, error) {
	f, err := os.CreateTemp("", "dagql-cachemoney-*.db")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func cachemoneyPostBeginExport(ctx context.Context, beginURL string, prepared *PreparedCachemoneyExport) (cachemoneyproto.BeginExportResponse, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, beginURL, pr)
	if err != nil {
		_ = pw.Close()
		return cachemoneyproto.BeginExportResponse{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	go func() {
		err := writeCachemoneyBeginExportMultipart(mw, prepared)
		if closeErr := mw.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return cachemoneyproto.BeginExportResponse{}, err
	}
	defer resp.Body.Close()
	if err := cachemoneyCheckHTTPStatus(resp); err != nil {
		return cachemoneyproto.BeginExportResponse{}, err
	}
	var out cachemoneyproto.BeginExportResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return cachemoneyproto.BeginExportResponse{}, fmt.Errorf("decode begin export response: %w", err)
	}
	if out.Version != cachemoneyproto.Version {
		return cachemoneyproto.BeginExportResponse{}, fmt.Errorf("begin export version %d != %d", out.Version, cachemoneyproto.Version)
	}
	if out.ExportID == "" {
		return cachemoneyproto.BeginExportResponse{}, errors.New("begin export response missing export ID")
	}
	if len(out.RequestedBlobs) > 0 && out.UploadURL == "" {
		return cachemoneyproto.BeginExportResponse{}, errors.New("begin export response requested blobs without upload URL")
	}
	if out.CompleteURL == "" {
		return cachemoneyproto.BeginExportResponse{}, errors.New("begin export response missing complete URL")
	}
	return out, nil
}

func writeCachemoneyBeginExportMultipart(mw *multipart.Writer, prepared *PreparedCachemoneyExport) error {
	manifestPart, err := mw.CreateFormFile(cachemoneyproto.MultipartManifestField, cachemoneyproto.ManifestName)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(manifestPart).Encode(prepared.Manifest); err != nil {
		return err
	}

	metadataFile, err := os.Open(prepared.MetadataDBPath)
	if err != nil {
		return err
	}
	defer metadataFile.Close()
	metadataPart, err := mw.CreateFormFile(cachemoneyproto.MultipartMetadataField, cachemoneyproto.MetadataDBName)
	if err != nil {
		return err
	}
	_, err = io.Copy(metadataPart, metadataFile)
	return err
}

func cachemoneyUploadBlob(ctx context.Context, reader cachemoneyContentBlobReader, uploadURL string, desc ocispecs.Descriptor) (bool, error) {
	if uploadURL == "" {
		return false, errors.New("debug cachemoney export: empty upload URL")
	}
	reqBody, err := json.Marshal(cachemoneyproto.BlobUploadRequest{
		BlobDigest: desc.Digest.String(),
		Size:       desc.Size,
		MediaType:  desc.MediaType,
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(reqBody))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if err := cachemoneyCheckHTTPStatus(resp); err != nil {
		return false, err
	}
	var uploadResp cachemoneyproto.BlobUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&uploadResp); err != nil {
		return false, fmt.Errorf("decode blob upload response: %w", err)
	}
	if uploadResp.AlreadyExists {
		return true, nil
	}
	if uploadResp.URL == "" {
		return false, fmt.Errorf("blob upload response for %s missing URL", desc.Digest)
	}
	method := uploadResp.Method
	if method == "" {
		method = http.MethodPut
	}

	blobReader, err := reader.ReadContentBlob(ctx, desc)
	if err != nil {
		return false, fmt.Errorf("read export blob %s: %w", desc.Digest, err)
	}
	defer blobReader.Close()
	uploadReq, err := http.NewRequestWithContext(ctx, method, uploadResp.URL, blobReader)
	if err != nil {
		return false, err
	}
	if desc.MediaType != "" {
		uploadReq.Header.Set("Content-Type", desc.MediaType)
	}
	if desc.Size > 0 {
		uploadReq.ContentLength = desc.Size
	}
	uploadHTTPResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		return false, err
	}
	defer uploadHTTPResp.Body.Close()
	if err := cachemoneyCheckHTTPStatus(uploadHTTPResp); err != nil {
		return false, fmt.Errorf("upload blob %s: %w", desc.Digest, err)
	}
	return false, nil
}

func cachemoneyCompleteExport(ctx context.Context, beginResp cachemoneyproto.BeginExportResponse, uploaded []string) error {
	body, err := json.Marshal(cachemoneyproto.CompleteExportRequest{
		Version:  cachemoneyproto.Version,
		ExportID: beginResp.ExportID,
		Blobs:    uploaded,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, beginResp.CompleteURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cachemoneyCheckHTTPStatus(resp); err != nil {
		return err
	}
	var out cachemoneyproto.CompleteExportResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode complete export response: %w", err)
	}
	if out.Version != cachemoneyproto.Version {
		return fmt.Errorf("complete export version %d != %d", out.Version, cachemoneyproto.Version)
	}
	if out.ExportID != beginResp.ExportID {
		return fmt.Errorf("complete export ID %q != %q", out.ExportID, beginResp.ExportID)
	}
	return nil
}

func cachemoneyFetchImport(ctx context.Context, url, metadataDBPath string) (cachemoneyproto.ImportManifest, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return cachemoneyproto.ImportManifest{}, 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return cachemoneyproto.ImportManifest{}, 0, err
	}
	defer resp.Body.Close()
	if err := cachemoneyCheckHTTPStatus(resp); err != nil {
		return cachemoneyproto.ImportManifest{}, 0, err
	}
	mediaType, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return cachemoneyproto.ImportManifest{}, 0, fmt.Errorf("parse import content type: %w", err)
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return cachemoneyproto.ImportManifest{}, 0, fmt.Errorf("import response content type %q is not multipart", mediaType)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return cachemoneyproto.ImportManifest{}, 0, errors.New("import response missing multipart boundary")
	}

	mr := multipart.NewReader(resp.Body, boundary)
	var manifest cachemoneyproto.ImportManifest
	var sawManifest bool
	var metadataBytes int64
	var sawMetadata bool
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return cachemoneyproto.ImportManifest{}, 0, err
		}
		switch part.FormName() {
		case cachemoneyproto.MultipartManifestField:
			if err := json.NewDecoder(part).Decode(&manifest); err != nil {
				_ = part.Close()
				return cachemoneyproto.ImportManifest{}, 0, fmt.Errorf("decode import manifest: %w", err)
			}
			sawManifest = true
		case cachemoneyproto.MultipartMetadataField:
			n, err := writeCachemoneyImportMetadataPart(metadataDBPath, part)
			if err != nil {
				_ = part.Close()
				return cachemoneyproto.ImportManifest{}, 0, err
			}
			metadataBytes = n
			sawMetadata = true
		}
		_ = part.Close()
	}
	if !sawManifest {
		return cachemoneyproto.ImportManifest{}, 0, errors.New("import response missing manifest part")
	}
	if manifest.Version != cachemoneyproto.Version {
		return cachemoneyproto.ImportManifest{}, 0, fmt.Errorf("import manifest version %d != %d", manifest.Version, cachemoneyproto.Version)
	}
	if !sawMetadata {
		return cachemoneyproto.ImportManifest{}, 0, errors.New("import response missing metadata part")
	}
	return manifest, metadataBytes, nil
}

func writeCachemoneyImportMetadataPart(metadataDBPath string, part io.Reader) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(metadataDBPath), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(metadataDBPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, part)
	if err != nil {
		return n, err
	}
	return n, f.Sync()
}

func cachemoneyManifestBlobDescriptors(manifest cachemoneyproto.BeginExportManifest) (map[string]ocispecs.Descriptor, error) {
	descs := map[string]ocispecs.Descriptor{}
	for _, chain := range manifest.Chains {
		for i, layer := range chain.Layers {
			desc, _, err := cachemoneyDescriptorFromPersistedLayer(i, PersistedSnapshotChainLayer{
				DiffID:         layer.DiffID,
				BlobDigest:     layer.BlobDigest,
				Size:           layer.Size,
				MediaType:      layer.MediaType,
				DescriptorJSON: layer.DescriptorJSON,
			})
			if err != nil {
				return nil, fmt.Errorf("chain %s layer %d: %w", chain.ChainID, i, err)
			}
			descs[desc.Digest.String()] = desc
		}
	}
	return descs, nil
}

func cachemoneyCheckHTTPStatus(resp *http.Response) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if len(body) == 0 {
		return fmt.Errorf("%s %s returned %s", resp.Request.Method, resp.Request.URL, resp.Status)
	}
	return fmt.Errorf("%s %s returned %s: %s", resp.Request.Method, resp.Request.URL, resp.Status, strings.TrimSpace(string(body)))
}
