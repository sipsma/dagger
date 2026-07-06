package conformance

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

const conformanceSchemaVersion = "conformance-schema-1"

func (c *conformer) testAuth(t *testing.T) {
	scope := c.newScope()
	// Every org-token endpoint rejects a missing and a wrong token with
	// 401, before doing any work.
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/scopes/" + scope + "/bundles"},
		{http.MethodGet, "/v1/scopes/" + scope + "/bundles?schemaVersion=x&bundleFormat=1"},
		{http.MethodPost, "/v1/scopes/" + scope + "/bundles/some-id/complete"},
		{http.MethodPost, "/v1/blobs/stat"},
		{http.MethodPost, "/v1/blobs/uploads"},
		{http.MethodPost, "/v1/blobs/uploads/complete"},
		{http.MethodGet, "/v1/blobs/sha256:" + randomHex(32)},
	}
	for _, endpoint := range paths {
		req, err := http.NewRequest(endpoint.method, c.baseURL+endpoint.path, nil)
		require.NoError(t, err)
		resp := c.do(t, req, false)
		readBody(t, resp)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"%s %s without a token", endpoint.method, endpoint.path)

		req, err = http.NewRequest(endpoint.method, c.baseURL+endpoint.path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer not-the-token-"+randomHex(4))
		resp = c.do(t, req, false)
		readBody(t, resp)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"%s %s with a wrong token", endpoint.method, endpoint.path)
	}
}

func (c *conformer) testPublishValidation(t *testing.T) {
	scope := c.newScope()
	archive := []byte("conformance-archive")

	cases := []struct {
		name  string
		parts []multipartPart
	}{
		{"missing manifest", []multipartPart{{name: "archive", content: archive}}},
		{"missing archive", []multipartPart{{name: "manifest", content: manifestJSON("s", conformanceSchemaVersion, 1)}}},
		{"unknown part", []multipartPart{
			{name: "manifest", content: manifestJSON("s", conformanceSchemaVersion, 1)},
			{name: "archive", content: archive},
			{name: "surprise", content: []byte("x")},
		}},
		{"duplicate manifest", []multipartPart{
			{name: "manifest", content: manifestJSON("s", conformanceSchemaVersion, 1)},
			{name: "manifest", content: manifestJSON("s", conformanceSchemaVersion, 1)},
			{name: "archive", content: archive},
		}},
		{"undecodable manifest", []multipartPart{
			{name: "manifest", content: []byte("not-json{")},
			{name: "archive", content: archive},
		}},
		{"invalid bundleFormat", []multipartPart{
			{name: "manifest", content: manifestJSON("s", conformanceSchemaVersion, 0)},
			{name: "archive", content: archive},
		}},
		{"missing storeUUID", []multipartPart{
			{name: "manifest", content: manifestJSON("", conformanceSchemaVersion, 1)},
			{name: "archive", content: archive},
		}},
	}
	for _, tc := range cases {
		resp := c.do(t, c.publishRequest(t, scope, tc.parts...), true)
		body := readBody(t, resp)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode,
			"publish with %s must be a 400: %s", tc.name, body)
	}

	// Not multipart at all.
	req := c.jsonRequest(t, http.MethodPost, "/v1/scopes/"+url.PathEscape(scope)+"/bundles",
		map[string]any{"not": "multipart"})
	resp := c.do(t, req, true)
	readBody(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "non-multipart publish must be a 400")
}

func (c *conformer) testBundleLifecycle(t *testing.T) {
	scope := c.newScope()
	archive := []byte("lifecycle-archive-" + randomHex(8))
	bundleID := c.publish(t, scope, "store-lifecycle", conformanceSchemaVersion, 1, archive)

	// Freshly published bundles list as pending with a working download URL.
	bundles := c.list(t, scope, conformanceSchemaVersion, 1, "")
	require.Len(t, bundles, 1)
	require.Equal(t, bundleID, bundles[0].BundleID)
	require.Equal(t, "pending", bundles[0].Status)
	require.NotEmpty(t, bundles[0].DownloadURL)

	// The download URL is pre-authorized: it must serve the archive bytes
	// verbatim WITHOUT the org token.
	downloadURL, err := url.Parse(bundles[0].DownloadURL)
	require.NoError(t, err)
	base, err := url.Parse(c.baseURL)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, base.ResolveReference(downloadURL).String(), nil)
	require.NoError(t, err)
	resp := c.do(t, req, false)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, archive, body, "downloaded archive must be byte-identical")

	// Completion flips the status; a tally body is accepted; completing
	// again is idempotent; a garbage tally is rejected.
	resp = c.complete(t, scope, bundleID, map[string]any{"uploaded": 3, "bytes": 42})
	readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	bundles = c.list(t, scope, conformanceSchemaVersion, 1, "")
	require.Len(t, bundles, 1)
	require.Equal(t, "complete", bundles[0].Status)

	resp = c.complete(t, scope, bundleID, nil)
	readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "completion must be idempotent")

	req, err = http.NewRequest(http.MethodPost,
		c.baseURL+"/v1/scopes/"+url.PathEscape(scope)+"/bundles/"+bundleID+"/complete",
		bytes.NewReader([]byte("not-json{")))
	require.NoError(t, err)
	resp = c.do(t, req, true)
	readBody(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "a garbage tally must be a 400")

	// Completing a bundle that does not exist is a 404.
	resp = c.complete(t, scope, "00000000-0000-4000-8000-000000000000", nil)
	readBody(t, resp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func (c *conformer) testSelectionValidation(t *testing.T) {
	scope := c.newScope()
	cases := []struct {
		name  string
		query string
	}{
		{"missing schemaVersion", "bundleFormat=1"},
		{"missing bundleFormat", "schemaVersion=x"},
		{"non-integer bundleFormat", "schemaVersion=x&bundleFormat=abc"},
		{"zero bundleFormat", "schemaVersion=x&bundleFormat=0"},
		{"non-integer limit", "schemaVersion=x&bundleFormat=1&limit=abc"},
		{"zero limit", "schemaVersion=x&bundleFormat=1&limit=0"},
	}
	for _, tc := range cases {
		req := c.jsonRequest(t, http.MethodGet,
			"/v1/scopes/"+url.PathEscape(scope)+"/bundles?"+tc.query, nil)
		resp := c.do(t, req, true)
		readBody(t, resp)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "selection with %s must be a 400", tc.name)
	}
}

func (c *conformer) testSelectionSemantics(t *testing.T) {
	scope := c.newScope()
	archive := []byte("selection-archive")

	// Store A: two bundles, older completed, newer left pending — the
	// completed one must win (the transient-upload-window rule).
	aOld := c.publish(t, scope, "store-a", conformanceSchemaVersion, 1, archive)
	require.Equal(t, http.StatusOK, c.completeAndDrain(t, scope, aOld))
	aNew := c.publish(t, scope, "store-a", conformanceSchemaVersion, 1, archive)

	// Store B: one pending bundle — pending is offered when it is all the
	// store has.
	bOnly := c.publish(t, scope, "store-b", conformanceSchemaVersion, 1, archive)

	// Store C: two completed — the newest completed wins.
	c.publish(t, scope, "store-c", conformanceSchemaVersion, 1, archive)
	cNew := c.publish(t, scope, "store-c", conformanceSchemaVersion, 1, archive)
	require.Equal(t, http.StatusOK, c.completeAndDrain(t, scope, cNew))

	// A different schema version in the same scope must never be offered.
	c.publish(t, scope, "store-other-version", "other-schema", 1, archive)
	// A different bundle format either.
	c.publish(t, scope, "store-other-format", conformanceSchemaVersion, 2, archive)

	bundles := c.list(t, scope, conformanceSchemaVersion, 1, "")
	byStore := map[string]bundleSummary{}
	for _, bundle := range bundles {
		_, dup := byStore[bundle.StoreUUID]
		require.False(t, dup, "selection must return at most one bundle per store: %+v", bundles)
		byStore[bundle.StoreUUID] = bundle
	}
	require.Len(t, byStore, 3, "exactly the three version-matching stores: %+v", bundles)
	require.Equal(t, aOld, byStore["store-a"].BundleID,
		"a store's newest COMPLETE bundle wins over its newer pending one")
	_ = aNew
	require.Equal(t, bOnly, byStore["store-b"].BundleID, "a store with only pending offers its newest pending")
	require.Equal(t, cNew, byStore["store-c"].BundleID, "the newest complete bundle wins within a store")

	// The limit caps the store count, newest stores first: store-c
	// published last, so limit=1 returns store-c's bundle.
	limited := c.list(t, scope, conformanceSchemaVersion, 1, "1")
	require.Len(t, limited, 1)
	require.Equal(t, "store-c", limited[0].StoreUUID, "newest store first under a limit")
}

func (c *conformer) completeAndDrain(t *testing.T, scope, bundleID string) int {
	t.Helper()
	resp := c.complete(t, scope, bundleID, nil)
	readBody(t, resp)
	return resp.StatusCode
}

func (c *conformer) testScopeIsolation(t *testing.T) {
	scopeA := c.newScope()
	scopeB := c.newScope()
	archive := []byte("scope-isolation-archive")

	bundleID := c.publish(t, scopeA, "store-iso", conformanceSchemaVersion, 1, archive)

	// Another scope sees nothing and cannot complete the bundle.
	require.Empty(t, c.list(t, scopeB, conformanceSchemaVersion, 1, ""))
	resp := c.complete(t, scopeB, bundleID, nil)
	readBody(t, resp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"a bundle must not be completable through a different scope")
}

func (c *conformer) testBlobStat(t *testing.T) {
	content, dgst := randomBlob(64)

	// Unknown blob: missing. Invalid digest: 400.
	require.Equal(t, []string{dgst.String()}, c.statMissing(t, []string{dgst.String()}))
	req := c.jsonRequest(t, http.MethodPost, "/v1/blobs/stat",
		map[string]any{"digests": []string{"not-a-digest"}})
	resp := c.do(t, req, true)
	readBody(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Bytes PUT without completed verification stay MISSING: presence
	// means verified, never merely uploaded.
	target, errResp := c.prepareUpload(t, dgst.String(), int64(len(content)), "")
	require.Nil(t, errResp)
	c.putBlobTo(t, target, content)
	require.Equal(t, []string{dgst.String()}, c.statMissing(t, []string{dgst.String()}),
		"unverified bytes must not stat as present")

	// After completion: present.
	outcome := c.completeUploads(t, []map[string]any{{
		"digest": dgst.String(), "size": len(content), "uploadID": target.UploadID,
	}})
	require.Equal(t, []string{dgst.String()}, outcome.Verified)
	require.Empty(t, c.statMissing(t, []string{dgst.String()}))
}

func (c *conformer) testBlobUploadLifecycle(t *testing.T) {
	content, dgst := randomBlob(128)

	// Invalid digest and non-positive size are rejected up front.
	_, errResp := c.prepareUpload(t, "garbage", 1, "")
	require.NotNil(t, errResp)
	readBody(t, errResp)
	require.Equal(t, http.StatusBadRequest, errResp.StatusCode)

	c.uploadBlob(t, content, dgst)

	// Idempotence at every step: re-prepare short-circuits; re-completing
	// answers already-exists.
	target, errResp := c.prepareUpload(t, dgst.String(), int64(len(content)), "")
	require.Nil(t, errResp)
	require.True(t, target.AlreadyExists, "verified blobs must short-circuit at prepare")
	outcome := c.completeUploads(t, []map[string]any{{
		"digest": dgst.String(), "size": len(content), "uploadID": "stale-or-empty",
	}})
	require.Equal(t, []string{dgst.String()}, outcome.AlreadyExists,
		"completing an already-verified blob must answer alreadyExists: %+v", outcome)

	// A size conflict against the verified record is a 409 at prepare.
	_, errResp = c.prepareUpload(t, dgst.String(), int64(len(content))+1, "")
	require.NotNil(t, errResp)
	readBody(t, errResp)
	require.Equal(t, http.StatusConflict, errResp.StatusCode)
}

func (c *conformer) testBlobUploadVerification(t *testing.T) {
	content, dgst := randomBlob(96)

	// Corrupt bytes (right size, wrong digest) fail verification, stay
	// absent, and are re-uploadable.
	corrupt := append([]byte(nil), content...)
	corrupt[0] ^= 0xff
	target, errResp := c.prepareUpload(t, dgst.String(), int64(len(content)), "")
	require.Nil(t, errResp)
	c.putBlobTo(t, target, corrupt)
	outcome := c.completeUploads(t, []map[string]any{{
		"digest": dgst.String(), "size": len(content), "uploadID": target.UploadID,
	}})
	require.Len(t, outcome.Failed, 1, "corrupt bytes must fail verification: %+v", outcome)
	require.Equal(t, dgst.String(), outcome.Failed[0].Digest)
	require.Equal(t, []string{dgst.String()}, c.statMissing(t, []string{dgst.String()}),
		"failed verification must leave the blob absent")

	// A completion whose token belongs to nothing fails per-blob, not
	// per-request.
	outcome = c.completeUploads(t, []map[string]any{{
		"digest": dgst.String(), "size": len(content), "uploadID": "forged-" + randomHex(8),
	}})
	require.Len(t, outcome.Failed, 1)

	// The honest re-upload succeeds.
	c.uploadBlob(t, content, dgst)
}

func (c *conformer) testBlobGet(t *testing.T) {
	content, dgst := randomBlob(80)

	// Unknown: 404. Invalid: 400.
	req := c.jsonRequest(t, http.MethodGet, "/v1/blobs/"+dgst.String(), nil)
	resp := c.do(t, req, true)
	readBody(t, resp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	req = c.jsonRequest(t, http.MethodGet, "/v1/blobs/not-a-digest", nil)
	resp = c.do(t, req, true)
	readBody(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	c.uploadBlob(t, content, dgst)

	// Present: a 307 whose Location serves the bytes WITHOUT the org token.
	req = c.jsonRequest(t, http.MethodGet, "/v1/blobs/"+dgst.String(), nil)
	resp = c.do(t, req, true)
	readBody(t, resp)
	require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode,
		"a present blob must answer with the redirect indirection")
	location := resp.Header.Get("Location")
	require.NotEmpty(t, location)

	locURL, err := url.Parse(location)
	require.NoError(t, err)
	base, err := url.Parse(c.baseURL)
	require.NoError(t, err)
	req, err = http.NewRequest(http.MethodGet, base.ResolveReference(locURL).String(), nil)
	require.NoError(t, err)
	resp = c.do(t, req, false)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, content, body, "redirect target must serve the blob bytes verbatim, token-free")
}

func (c *conformer) testLimits(t *testing.T) {
	// Stat batches over 4096 digests are rejected whole.
	digests := make([]string, 4097)
	for i := range digests {
		digests[i] = fmt.Sprintf("sha256:%064x", i)
	}
	req := c.jsonRequest(t, http.MethodPost, "/v1/blobs/stat", map[string]any{"digests": digests})
	resp := c.do(t, req, true)
	readBody(t, resp)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode,
		"stat batches over the limit must be a 413")

	// Completion batches over 1024 blobs are rejected whole.
	blobs := make([]map[string]any, 1025)
	for i := range blobs {
		blobs[i] = map[string]any{"digest": fmt.Sprintf("sha256:%064x", i), "size": 1, "uploadID": "x"}
	}
	req = c.jsonRequest(t, http.MethodPost, "/v1/blobs/uploads/complete", map[string]any{"blobs": blobs})
	resp = c.do(t, req, true)
	readBody(t, resp)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode,
		"completion batches over the limit must be a 413")

	// JSON bodies over 1MiB are rejected as too large, not as bad JSON.
	huge := bytes.Repeat([]byte("a"), (1<<20)+256)
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/v1/blobs/stat",
		io.NopCloser(bytes.NewReader(fmt.Appendf(nil, `{"digests":[%q]}`, huge))))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp = c.do(t, req, true)
	readBody(t, resp)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode,
		"oversized JSON bodies must be a 413")
}
