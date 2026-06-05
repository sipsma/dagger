package dagql

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dagger/dagger/dagql/cachemoneyproto"
	"github.com/dagger/dagger/engine/slog"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/errgroup"
)

func (c *Cache) importCachemoney(ctx context.Context, importURL string) {
	if importURL == "" {
		return
	}
	if err := c.importCachemoneyErr(ctx, importURL); err != nil {
		slog.Warn("skipping cachemoney import", "url", importURL, "err", err)
	}
}

func (c *Cache) importCachemoneyErr(ctx context.Context, importURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, importURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: status %d", importURL, resp.StatusCode)
	}

	tempDir, err := os.MkdirTemp("", "dagger-cachemoney-import-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	if err := extractTar(resp.Body, tempDir); err != nil {
		return err
	}

	manifestPath := filepath.Join(tempDir, cachemoneyproto.ImportManifestName)
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read cachemoney import manifest: %w", err)
	}
	var manifest cachemoneyproto.ImportManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return fmt.Errorf("decode cachemoney import manifest: %w", err)
	}
	if manifest.Version != cachemoneyproto.Version {
		return fmt.Errorf("unsupported cachemoney import manifest version %d", manifest.Version)
	}
	for _, remoteSource := range manifest.Sources {
		if remoteSource.ID == "" {
			return errors.New("cachemoney import manifest contains empty source ID")
		}
		if err := c.RegisterCacheSource(&PersistedCacheSource{
			ID: remoteSource.ID,
			Snapshots: bkcache.NewRemoteCacheSource(
				remoteSource.ID,
				remoteSource.Snapshots,
				http.DefaultClient,
			),
		}); err != nil {
			return err
		}
	}

	metadataSourceID := manifest.MetadataSourceID
	if metadataSourceID == "" {
		metadataSourceID = "cachemoney-import"
	}
	return c.ImportCacheMetadata(ctx, &PersistedCacheSource{
		ID:             metadataSourceID,
		MetadataDBPath: filepath.Join(tempDir, bkcache.CacheBundleDBName),
	}, true)
}

func (c *Cache) ExportCachemoney(ctx context.Context) error {
	if c.cachemoneyExportURL == "" {
		return errors.New("cachemoney export URL is not configured")
	}
	return c.exportCachemoney(ctx, c.cachemoneyExportURL)
}

func (c *Cache) exportCachemoney(ctx context.Context, exportURL string) error {
	tempDir, err := os.MkdirTemp("", "dagger-cachemoney-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	bundleDir := filepath.Join(tempDir, "bundle")
	if err := c.WriteCacheBundle(ctx, bundleDir); err != nil {
		return err
	}

	metadataTarPath := filepath.Join(tempDir, "metadata.tar")
	if err := writeCachemoneyExportTar(metadataTarPath, bundleDir); err != nil {
		return err
	}
	metadataTar, err := os.Open(metadataTarPath)
	if err != nil {
		return err
	}
	defer metadataTar.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exportURL, metadataTar)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	if st, err := metadataTar.Stat(); err == nil {
		req.ContentLength = st.Size()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST %s: status %d: %s", exportURL, resp.StatusCode, bytes.TrimSpace(body))
	}
	var exportResp cachemoneyproto.ExportResponse
	if err := json.NewDecoder(resp.Body).Decode(&exportResp); err != nil {
		return fmt.Errorf("decode cachemoney export response: %w", err)
	}
	if err := uploadCachemoneyBlobs(ctx, bundleDir, exportResp.Uploads); err != nil {
		return err
	}
	if exportResp.CompleteURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, exportResp.CompleteURL, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return fmt.Errorf("POST %s: status %d: %s", exportResp.CompleteURL, resp.StatusCode, bytes.TrimSpace(body))
		}
	}
	return nil
}

func uploadCachemoneyBlobs(ctx context.Context, bundleDir string, uploads []cachemoneyproto.UploadTask) error {
	eg, ctx := errgroup.WithContext(ctx)
	eg.SetLimit(8)
	for _, upload := range uploads {
		upload := upload
		eg.Go(func() error {
			dgst, err := digest.Parse(upload.Digest)
			if err != nil {
				return err
			}
			path, err := bkcache.CacheBundleBlobPath(bundleDir, dgst)
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			method := upload.Method
			if method == "" {
				method = http.MethodPut
			}
			req, err := http.NewRequestWithContext(ctx, method, upload.URL, f)
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/octet-stream")
			if st, err := f.Stat(); err == nil {
				req.ContentLength = st.Size()
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				return fmt.Errorf("%s %s: status %d: %s", method, upload.URL, resp.StatusCode, bytes.TrimSpace(body))
			}
			return nil
		})
	}
	return eg.Wait()
}

func writeCachemoneyExportTar(dstPath string, bundleDir string) error {
	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()
	tw := tar.NewWriter(dst)
	defer tw.Close()

	for _, rel := range []string{
		"manifest.json",
		bkcache.CacheBundleDBName,
		filepath.Join("snapshots", "index.json"),
	} {
		path := filepath.Join(bundleDir, rel)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := addTarFile(tw, path, rel); err != nil {
			return err
		}
	}
	return nil
}

func addTarFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o600,
		Size: st.Size(),
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func extractTar(src io.Reader, dstDir string) error {
	tr := tar.NewReader(src)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.Clean(header.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("unsafe tar path %q", header.Name)
		}
		path := filepath.Join(dstDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, tr)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}
