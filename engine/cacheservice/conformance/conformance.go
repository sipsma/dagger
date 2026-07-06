// Package conformance is the executable definition of the /v1 cache
// service protocol (T-S9): one suite, run against every implementation —
// the in-repo test service and the real dagger.io handlers — so the two
// cannot drift silently. It speaks raw HTTP deliberately: the suite pins
// the wire contract itself, not any client's interpretation of it.
//
// The suite is self-scoping: it publishes under a random scope and uses
// random blob payloads, so it can run against a shared or pre-populated
// service instance without seeing anyone else's state.
package conformance

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// RunConformance runs the whole protocol suite against the service at
// baseURL, authenticating with token.
func RunConformance(t *testing.T, baseURL, token string) {
	c := &conformer{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		// Redirects are handled explicitly: the 307 blob indirection is
		// itself protocol surface under test.
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}

	t.Run("Auth", c.testAuth)
	t.Run("PublishValidation", c.testPublishValidation)
	t.Run("BundleLifecycle", c.testBundleLifecycle)
	t.Run("SelectionValidation", c.testSelectionValidation)
	t.Run("SelectionSemantics", c.testSelectionSemantics)
	t.Run("ScopeIsolation", c.testScopeIsolation)
	t.Run("BlobStat", c.testBlobStat)
	t.Run("BlobUploadLifecycle", c.testBlobUploadLifecycle)
	t.Run("BlobUploadVerification", c.testBlobUploadVerification)
	t.Run("BlobGet", c.testBlobGet)
	t.Run("Limits", c.testLimits)
}

type conformer struct {
	baseURL string
	token   string
	client  *http.Client
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (c *conformer) newScope() string {
	return "conformance-" + randomHex(8)
}

func randomBlob(size int) ([]byte, digest.Digest) {
	blob := make([]byte, size)
	if _, err := rand.Read(blob); err != nil {
		panic(err)
	}
	return blob, digest.FromBytes(blob)
}

func manifestJSON(storeUUID, schemaVersion string, bundleFormat int) []byte {
	return fmt.Appendf(nil,
		`{"bundleFormat":%d,"schemaVersion":%q,"engineVersion":"v0.0.0-conformance","storeUUID":%q,"counts":{"results":1},"blobIndex":[]}`,
		bundleFormat, schemaVersion, storeUUID)
}

//
// HTTP helpers. Every request path is explicit about auth so the auth
// matrix is part of the suite, not an accident of a shared helper.
//

func (c *conformer) do(t *testing.T, req *http.Request, withAuth bool) *http.Response {
	t.Helper()
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	require.NoError(t, err)
	return resp
}

func (c *conformer) jsonRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return body
}

func decodeJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(readBody(t, resp), &out),
		"response must decode as %T", out)
	return out
}

// publishRequest builds the two-part multipart publish.
func (c *conformer) publishRequest(t *testing.T, scope string, parts ...multipartPart) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, part := range parts {
		w, err := mw.CreateFormField(part.name)
		require.NoError(t, err)
		_, err = w.Write(part.content)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	req, err := http.NewRequest(http.MethodPost, c.bundlesPath(scope), &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

type multipartPart struct {
	name    string
	content []byte
}

func (c *conformer) bundlesPath(scope string) string {
	return c.baseURL + "/v1/scopes/" + url.PathEscape(scope) + "/bundles"
}

// publish is the happy-path helper used by the non-publish cases.
func (c *conformer) publish(t *testing.T, scope, storeUUID, schemaVersion string, bundleFormat int, archive []byte) string {
	t.Helper()
	req := c.publishRequest(t, scope,
		multipartPart{name: "manifest", content: manifestJSON(storeUUID, schemaVersion, bundleFormat)},
		multipartPart{name: "archive", content: archive},
	)
	resp := c.do(t, req, true)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "publish: %s", body)
	var publishResp struct {
		BundleID string `json:"bundleID"`
	}
	require.NoError(t, json.Unmarshal(body, &publishResp))
	require.NotEmpty(t, publishResp.BundleID, "publish must mint a bundle ID")
	return publishResp.BundleID
}

func (c *conformer) complete(t *testing.T, scope, bundleID string, tally any) *http.Response {
	t.Helper()
	req := c.jsonRequest(t, http.MethodPost,
		"/v1/scopes/"+url.PathEscape(scope)+"/bundles/"+url.PathEscape(bundleID)+"/complete", tally)
	return c.do(t, req, true)
}

type bundleSummary struct {
	BundleID    string `json:"bundleID"`
	StoreUUID   string `json:"storeUUID"`
	Status      string `json:"status"`
	DownloadURL string `json:"downloadURL"`
}

func (c *conformer) list(t *testing.T, scope, schemaVersion string, bundleFormat int, limit string) []bundleSummary {
	t.Helper()
	path := fmt.Sprintf("/v1/scopes/%s/bundles?schemaVersion=%s&bundleFormat=%d",
		url.PathEscape(scope), url.QueryEscape(schemaVersion), bundleFormat)
	if limit != "" {
		path += "&limit=" + limit
	}
	req := c.jsonRequest(t, http.MethodGet, path, nil)
	resp := c.do(t, req, true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeJSON[struct {
		Bundles []bundleSummary `json:"bundles"`
	}](t, resp).Bundles
}

func (c *conformer) statMissing(t *testing.T, digests []string) []string {
	t.Helper()
	req := c.jsonRequest(t, http.MethodPost, "/v1/blobs/stat", map[string]any{"digests": digests})
	resp := c.do(t, req, true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeJSON[struct {
		Missing []string `json:"missing"`
	}](t, resp).Missing
}

type uploadTarget struct {
	AlreadyExists bool   `json:"alreadyExists"`
	Method        string `json:"method"`
	URL           string `json:"url"`
	UploadID      string `json:"uploadID"`
}

func (c *conformer) prepareUpload(t *testing.T, dgst string, size int64, mediaType string) (uploadTarget, *http.Response) {
	t.Helper()
	req := c.jsonRequest(t, http.MethodPost, "/v1/blobs/uploads",
		map[string]any{"digest": dgst, "size": size, "mediaType": mediaType})
	resp := c.do(t, req, true)
	if resp.StatusCode != http.StatusOK {
		return uploadTarget{}, resp
	}
	return decodeJSON[uploadTarget](t, resp), nil
}

// putBlobTo PUTs bytes to a pre-authorized upload URL — with NO org token:
// the URL's own authorization must suffice (the presigned contract).
func (c *conformer) putBlobTo(t *testing.T, target uploadTarget, content []byte) {
	t.Helper()
	method := target.Method
	if method == "" {
		method = http.MethodPut
	}
	uploadURL, err := url.Parse(target.URL)
	require.NoError(t, err)
	base, err := url.Parse(c.baseURL)
	require.NoError(t, err)
	req, err := http.NewRequest(method, base.ResolveReference(uploadURL).String(), bytes.NewReader(content))
	require.NoError(t, err)
	resp := c.do(t, req, false)
	body := readBody(t, resp)
	require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300,
		"pre-authorized PUT must succeed without the org token: %d %s", resp.StatusCode, body)
}

type completionOutcome struct {
	Verified      []string `json:"verified"`
	AlreadyExists []string `json:"alreadyExists"`
	Failed        []struct {
		Digest string `json:"digest"`
		Error  string `json:"error"`
	} `json:"failed"`
}

func (c *conformer) completeUploads(t *testing.T, blobs []map[string]any) completionOutcome {
	t.Helper()
	req := c.jsonRequest(t, http.MethodPost, "/v1/blobs/uploads/complete", map[string]any{"blobs": blobs})
	resp := c.do(t, req, true)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeJSON[completionOutcome](t, resp)
}

// uploadBlob drives the full prepare→PUT→complete flow for one blob.
func (c *conformer) uploadBlob(t *testing.T, content []byte, dgst digest.Digest) {
	t.Helper()
	target, errResp := c.prepareUpload(t, dgst.String(), int64(len(content)), "application/octet-stream")
	require.Nil(t, errResp, "prepare must succeed")
	if target.AlreadyExists {
		return
	}
	c.putBlobTo(t, target, content)
	outcome := c.completeUploads(t, []map[string]any{{
		"digest": dgst.String(), "size": len(content), "uploadID": target.UploadID,
	}})
	require.Equal(t, []string{dgst.String()}, outcome.Verified,
		"upload completion must verify the blob: %+v", outcome)
}
