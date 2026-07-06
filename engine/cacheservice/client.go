package cacheservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"

	digest "github.com/opencontainers/go-digest"
)

// ErrBlobNotFound reports a blob the service does not have (HTTP 404).
// Permanent for this boot: retrying the same service cannot help. Every
// other transport failure is transient.
var ErrBlobNotFound = errors.New("blob not found in cache service")

const defaultRequestTimeout = 30 * time.Second

// Client speaks the /v1 cache service protocol. JSON API calls are bounded
// by RequestTimeout; streaming transfers (publish, blob up/download) are
// bounded only by their caller's context, since their legitimate duration
// scales with bytes.
type Client struct {
	baseURL *url.URL
	token   string
	scope   string

	requestTimeout time.Duration
	httpClient     *http.Client
	// streamClient never follows redirects itself: the 307 blob indirection
	// must re-issue the fetch WITHOUT the org token (presigned URLs carry
	// their own authorization, and e.g. S3 rejects requests carrying both).
	streamClient *http.Client
}

type ClientOptions struct {
	// RequestTimeout bounds each JSON API call. Zero means the default.
	RequestTimeout time.Duration
	// HTTPTransport overrides the transport (tests). Nil means the default.
	HTTPTransport http.RoundTripper
}

func NewClient(serviceURL, token, scope string, opts ClientOptions) (*Client, error) {
	if serviceURL == "" {
		return nil, errors.New("cache service client: empty service URL")
	}
	if token == "" {
		return nil, errors.New("cache service client: empty token")
	}
	if scope == "" {
		return nil, errors.New("cache service client: empty scope")
	}
	base, err := url.Parse(serviceURL)
	if err != nil {
		return nil, fmt.Errorf("cache service client: parse service URL: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("cache service client: service URL %q must be http(s)", serviceURL)
	}
	timeout := opts.RequestTimeout
	if timeout == 0 {
		timeout = defaultRequestTimeout
	}
	transport := opts.HTTPTransport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Client{
		baseURL:        base,
		token:          token,
		scope:          scope,
		requestTimeout: timeout,
		httpClient:     &http.Client{Transport: transport},
		streamClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Scope() string { return c.scope }

func (c *Client) endpoint(elem ...string) *url.URL {
	u := *c.baseURL
	u = *u.JoinPath(elem...)
	return &u
}

func (c *Client) bundlesURL() *url.URL {
	return c.endpoint("v1", "scopes", c.scope, "bundles")
}

// doJSON performs one authenticated JSON API call. A nil out means the
// response body is discarded after the status check.
func (c *Client) doJSON(ctx context.Context, method string, u *url.URL, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpStatusError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func httpStatusError(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("cache service: %s %s: %s: %s",
		resp.Request.Method, resp.Request.URL.Path, resp.Status, bytes.TrimSpace(msg))
}

// SelectBundles asks the selection endpoint for bundles matching this
// engine's versions, newest stores first (§10 D3 semantics live
// service-side; the client only asks).
func (c *Client) SelectBundles(ctx context.Context, schemaVersion string, bundleFormat, limit int) ([]BundleSummary, error) {
	u := c.bundlesURL()
	q := u.Query()
	q.Set("schemaVersion", schemaVersion)
	q.Set("bundleFormat", strconv.Itoa(bundleFormat))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	u.RawQuery = q.Encode()
	var resp ListBundlesResponse
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Bundles, nil
}

// DownloadBundle streams a selected bundle's archive. The download URL is
// pre-authorized by the service (presigned/URL-signed) and fetched with no
// org token; a relative URL resolves against the service base.
func (c *Client) DownloadBundle(ctx context.Context, downloadURL string) (io.ReadCloser, error) {
	u, err := url.Parse(downloadURL)
	if err != nil {
		return nil, fmt.Errorf("parse bundle download URL: %w", err)
	}
	resolved := c.baseURL.ResolveReference(u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, httpStatusError(resp)
	}
	return resp.Body, nil
}

// PublishBundle streams the bundle archive with its manifest as the
// two-part multipart publish (S6 step: metadata becomes durable and listed
// pending the moment this returns). manifestJSON must be the archive's
// manifest bytes verbatim.
func (c *Client) PublishBundle(ctx context.Context, manifestJSON []byte, archive io.Reader) (string, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			manifestPart, err := mw.CreateFormField(MultipartManifestField)
			if err != nil {
				return err
			}
			if _, err := manifestPart.Write(manifestJSON); err != nil {
				return err
			}
			archivePart, err := mw.CreateFormFile(MultipartArchiveField, "bundle.tar.zst")
			if err != nil {
				return err
			}
			if _, err := io.Copy(archivePart, archive); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.bundlesURL().String(), pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.streamClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", httpStatusError(resp)
	}
	var publishResp PublishBundleResponse
	if err := json.NewDecoder(resp.Body).Decode(&publishResp); err != nil {
		return "", fmt.Errorf("decode publish response: %w", err)
	}
	if publishResp.BundleID == "" {
		return "", errors.New("cache service: publish returned no bundle ID")
	}
	return publishResp.BundleID, nil
}

// CompleteBundle marks a published bundle blobs-complete, with the upload
// tally the service whitelists into its event log.
func (c *Client) CompleteBundle(ctx context.Context, bundleID string, tally *BundleUploadTally) error {
	u := c.endpoint("v1", "scopes", c.scope, "bundles", bundleID, "complete")
	var body any
	if tally != nil {
		body = tally
	}
	return c.doJSON(ctx, http.MethodPost, u, body, nil)
}

// StatMissingBlobs returns the subset of digests the service does NOT hold
// verified, batching under the protocol's stat limit.
func (c *Client) StatMissingBlobs(ctx context.Context, digests []string) ([]string, error) {
	var missing []string
	for start := 0; start < len(digests); start += MaxStatDigests {
		batch := digests[start:min(start+MaxStatDigests, len(digests))]
		var resp BlobStatResponse
		if err := c.doJSON(ctx, http.MethodPost, c.endpoint("v1", "blobs", "stat"),
			BlobStatRequest{Digests: batch}, &resp); err != nil {
			return nil, err
		}
		missing = append(missing, resp.Missing...)
	}
	return missing, nil
}

// PrepareBlobUpload asks for one blob's upload target. AlreadyExists means
// the CAS holds these bytes verified and nothing uploads.
func (c *Client) PrepareBlobUpload(ctx context.Context, dgst string, size int64, mediaType string) (BlobUploadResponse, error) {
	var resp BlobUploadResponse
	err := c.doJSON(ctx, http.MethodPost, c.endpoint("v1", "blobs", "uploads"),
		BlobUploadRequest{Digest: dgst, Size: size, MediaType: mediaType}, &resp)
	return resp, err
}

// PutBlob streams one blob's bytes to its prepared upload target. The URL
// is pre-authorized; no org token is attached.
func (c *Client) PutBlob(ctx context.Context, prep BlobUploadResponse, r io.Reader, size int64) error {
	if prep.URL == "" {
		return errors.New("cache service: blob upload target has no URL")
	}
	u, err := url.Parse(prep.URL)
	if err != nil {
		return fmt.Errorf("parse blob upload URL: %w", err)
	}
	method := prep.Method
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.ResolveReference(u).String(), r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	resp, err := c.streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpStatusError(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// CompleteBlobUploads verifies-and-records uploaded blobs, batching under
// the protocol's completion limit. The per-blob outcomes concatenate across
// batches.
func (c *Client) CompleteBlobUploads(ctx context.Context, blobs []BlobUploadCompletion) (CompleteBlobUploadsResponse, error) {
	var combined CompleteBlobUploadsResponse
	for start := 0; start < len(blobs); start += MaxUploadBatch {
		batch := blobs[start:min(start+MaxUploadBatch, len(blobs))]
		var resp CompleteBlobUploadsResponse
		if err := c.doJSON(ctx, http.MethodPost, c.endpoint("v1", "blobs", "uploads", "complete"),
			CompleteBlobUploadsRequest{Blobs: batch}, &resp); err != nil {
			return combined, err
		}
		combined.Verified = append(combined.Verified, resp.Verified...)
		combined.AlreadyExists = append(combined.AlreadyExists, resp.AlreadyExists...)
		combined.Failed = append(combined.Failed, resp.Failed...)
	}
	return combined, nil
}

// OpenBlob fetches one blob's bytes: an authenticated GET that the service
// answers with a 307 to a pre-authorized URL, which is then fetched clean
// (no org token). A 404 anywhere is ErrBlobNotFound — permanent for this
// boot; every other failure is transient (§9.3).
func (c *Client) OpenBlob(ctx context.Context, dgst digest.Digest, size int64) (io.ReadCloser, error) {
	if err := dgst.Validate(); err != nil {
		return nil, fmt.Errorf("invalid blob digest %q: %w", dgst, err)
	}
	u := c.endpoint("v1", "blobs", dgst.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		// A service may also answer bytes directly.
		return resp.Body, nil
	case http.StatusTemporaryRedirect, http.StatusFound, http.StatusMovedPermanently:
		location := resp.Header.Get("Location")
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if location == "" {
			return nil, fmt.Errorf("cache service: blob %s: redirect without location", dgst)
		}
		locURL, err := url.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("cache service: blob %s: parse redirect location: %w", dgst, err)
		}
		redirectReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.ResolveReference(locURL).String(), nil)
		if err != nil {
			return nil, err
		}
		redirectResp, err := c.streamClient.Do(redirectReq)
		if err != nil {
			return nil, err
		}
		if redirectResp.StatusCode == http.StatusNotFound {
			defer redirectResp.Body.Close()
			return nil, fmt.Errorf("blob %s: %w", dgst, ErrBlobNotFound)
		}
		if redirectResp.StatusCode != http.StatusOK {
			defer redirectResp.Body.Close()
			return nil, httpStatusError(redirectResp)
		}
		return redirectResp.Body, nil
	case http.StatusNotFound:
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("blob %s: %w", dgst, ErrBlobNotFound)
	default:
		defer resp.Body.Close()
		return nil, httpStatusError(resp)
	}
}
