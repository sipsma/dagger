package cacheservice_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	enginecacheservice "github.com/dagger/dagger/engine/cacheservice"
	"github.com/dagger/dagger/engine/snapshots"
	testservice "github.com/dagger/dagger/internal/testutil/cacheservice"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

const testToken = "test-org-token"

// authRecorder captures the Authorization header per request path so tests
// can pin the client's credential discipline: org token on /v1, never on
// the pre-authorized data paths.
type authRecorder struct {
	mu       sync.Mutex
	requests []recordedRequest
	next     http.Handler
}

type recordedRequest struct {
	path string
	auth string
}

func (rec *authRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec.mu.Lock()
	rec.requests = append(rec.requests, recordedRequest{path: r.URL.Path, auth: r.Header.Get("Authorization")})
	rec.mu.Unlock()
	rec.next.ServeHTTP(w, r)
}

func (rec *authRecorder) recorded() []recordedRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]recordedRequest(nil), rec.requests...)
}

func startTestService(t *testing.T) (*enginecacheservice.Client, *authRecorder) {
	t.Helper()
	svc, err := testservice.New(t.TempDir(), testToken)
	require.NoError(t, err)
	rec := &authRecorder{next: svc.Handler()}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)
	client, err := enginecacheservice.NewClient(server.URL, testToken, "test-scope", enginecacheservice.ClientOptions{})
	require.NoError(t, err)
	return client, rec
}

func manifestJSON(storeUUID string) []byte {
	return fmt.Appendf(nil,
		`{"bundleFormat":1,"schemaVersion":"19","engineVersion":"v0.0.0-test","storeUUID":%q,"counts":{"results":3}}`,
		storeUUID)
}

func TestClientBundleLifecycle(t *testing.T) {
	ctx := context.Background()
	client, _ := startTestService(t)

	archive := []byte("opaque-bundle-archive-bytes")
	bundleID, err := client.PublishBundle(ctx, manifestJSON("store-a"), bytes.NewReader(archive))
	require.NoError(t, err)
	require.NotEmpty(t, bundleID)

	// Freshly published: listed pending, downloadable byte-identical.
	bundles, err := client.SelectBundles(ctx, "19", 1, 0)
	require.NoError(t, err)
	require.Len(t, bundles, 1)
	require.Equal(t, bundleID, bundles[0].BundleID)
	require.Equal(t, "store-a", bundles[0].StoreUUID)
	require.Equal(t, enginecacheservice.BundleStatusPending, bundles[0].Status)
	require.NotEmpty(t, bundles[0].DownloadURL)

	rc, err := client.DownloadBundle(ctx, bundles[0].DownloadURL)
	require.NoError(t, err)
	downloaded, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.Equal(t, archive, downloaded)

	// Complete with a tally; selection reflects the status flip.
	require.NoError(t, client.CompleteBundle(ctx, bundleID, &enginecacheservice.BundleUploadTally{
		Uploaded: 2, AlreadyPresent: 1, Bytes: 1234, DurationMS: 5,
	}))
	bundles, err = client.SelectBundles(ctx, "19", 1, 0)
	require.NoError(t, err)
	require.Len(t, bundles, 1)
	require.Equal(t, enginecacheservice.BundleStatusComplete, bundles[0].Status)

	// Version filtering is exact-match: a different schema version sees
	// nothing.
	bundles, err = client.SelectBundles(ctx, "18", 1, 0)
	require.NoError(t, err)
	require.Empty(t, bundles)

	// Completing an unknown bundle is a loud error, not a silent no-op.
	err = client.CompleteBundle(ctx, "00000000-0000-4000-8000-000000000000", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "404")
}

func TestClientBlobUploadAndFetch(t *testing.T) {
	ctx := context.Background()
	client, _ := startTestService(t)

	blob := []byte("chain-layer-blob-bytes")
	dgst := digest.FromBytes(blob)
	size := int64(len(blob))

	// Absent blob: stat reports it missing, OpenBlob types it not-found.
	missing, err := client.StatMissingBlobs(ctx, []string{dgst.String()})
	require.NoError(t, err)
	require.Equal(t, []string{dgst.String()}, missing)
	_, err = client.OpenBlob(ctx, dgst, size)
	require.ErrorIs(t, err, enginecacheservice.ErrBlobNotFound)

	// Upload: prepare → PUT → complete-verify.
	prep, err := client.PrepareBlobUpload(ctx, dgst.String(), size, "application/octet-stream")
	require.NoError(t, err)
	require.False(t, prep.AlreadyExists)
	require.NotEmpty(t, prep.URL)
	require.NotEmpty(t, prep.UploadID)
	require.NoError(t, client.PutBlob(ctx, prep, bytes.NewReader(blob), size))
	completeResp, err := client.CompleteBlobUploads(ctx, []enginecacheservice.BlobUploadCompletion{{
		Digest: dgst.String(), Size: size, MediaType: "application/octet-stream", UploadID: prep.UploadID,
	}})
	require.NoError(t, err)
	require.Equal(t, []string{dgst.String()}, completeResp.Verified)
	require.Empty(t, completeResp.Failed)

	// Present means verified: stat clears, re-prepare short-circuits, fetch
	// rides the 307 indirection back to the bytes.
	missing, err = client.StatMissingBlobs(ctx, []string{dgst.String()})
	require.NoError(t, err)
	require.Empty(t, missing)
	prep, err = client.PrepareBlobUpload(ctx, dgst.String(), size, "application/octet-stream")
	require.NoError(t, err)
	require.True(t, prep.AlreadyExists)
	rc, err := client.OpenBlob(ctx, dgst, size)
	require.NoError(t, err)
	fetched, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.Equal(t, blob, fetched)
}

// closeCountingReader records closes, standing in for a content-store
// reader whose lifetime the caller owns.
type closeCountingReader struct {
	io.Reader
	closes int
}

func (r *closeCountingReader) Close() error {
	r.closes++
	if r.closes > 1 {
		return errors.New("file already closed")
	}
	return nil
}

// TestClientPutBlobLeavesBodyOwnershipWithCaller pins the upload-path
// ownership contract: PutBlob must not let the HTTP client close a
// ReadCloser body, so the caller's own Close is the first and only one.
// (The engine streams blobs from the content store as ReadClosers; a
// double-close surfaced as every blob "failing" upload.)
func TestClientPutBlobLeavesBodyOwnershipWithCaller(t *testing.T) {
	ctx := context.Background()
	client, _ := startTestService(t)

	blob := []byte("ownership-proof-blob")
	dgst := digest.FromBytes(blob)
	size := int64(len(blob))

	prep, err := client.PrepareBlobUpload(ctx, dgst.String(), size, "")
	require.NoError(t, err)
	body := &closeCountingReader{Reader: bytes.NewReader(blob)}
	require.NoError(t, client.PutBlob(ctx, prep, body, size))
	require.NoError(t, body.Close(), "the caller's close must be the first close")
	require.Equal(t, 1, body.closes)

	resp, err := client.CompleteBlobUploads(ctx, []enginecacheservice.BlobUploadCompletion{{
		Digest: dgst.String(), Size: size, UploadID: prep.UploadID,
	}})
	require.NoError(t, err)
	require.Equal(t, []string{dgst.String()}, resp.Verified)
}

func TestClientUploadVerificationRejectsCorruptBytes(t *testing.T) {
	ctx := context.Background()
	client, _ := startTestService(t)

	blob := []byte("the-honest-bytes")
	dgst := digest.FromBytes(blob)
	size := int64(len(blob))

	prep, err := client.PrepareBlobUpload(ctx, dgst.String(), size, "")
	require.NoError(t, err)
	corrupt := []byte("the-c0rrupt-byte")
	require.Len(t, corrupt, int(size), "corrupt bytes must match the declared size so only the digest differs")
	require.NoError(t, client.PutBlob(ctx, prep, bytes.NewReader(corrupt), size))
	completeResp, err := client.CompleteBlobUploads(ctx, []enginecacheservice.BlobUploadCompletion{{
		Digest: dgst.String(), Size: size, UploadID: prep.UploadID,
	}})
	require.NoError(t, err)
	require.Empty(t, completeResp.Verified)
	require.Len(t, completeResp.Failed, 1)
	require.Equal(t, dgst.String(), completeResp.Failed[0].Digest)

	// Failed verification means absent: unverified bytes are never present.
	missing, err := client.StatMissingBlobs(ctx, []string{dgst.String()})
	require.NoError(t, err)
	require.Equal(t, []string{dgst.String()}, missing)
}

func TestClientStatBatchesOverTheProtocolLimit(t *testing.T) {
	ctx := context.Background()
	client, _ := startTestService(t)

	count := enginecacheservice.MaxStatDigests + 100
	digests := make([]string, count)
	for i := range digests {
		digests[i] = digest.FromBytes(fmt.Appendf(nil, "blob-%d", i)).String()
	}
	missing, err := client.StatMissingBlobs(ctx, digests)
	require.NoError(t, err)
	require.Len(t, missing, count, "the client must chunk stat batches the service would reject whole")
}

func TestClientCredentialDiscipline(t *testing.T) {
	ctx := context.Background()
	client, rec := startTestService(t)

	blob := []byte("credential-discipline-blob")
	dgst := digest.FromBytes(blob)
	size := int64(len(blob))

	bundleID, err := client.PublishBundle(ctx, manifestJSON("store-cred"), bytes.NewReader([]byte("archive")))
	require.NoError(t, err)
	require.NoError(t, client.CompleteBundle(ctx, bundleID, nil))
	bundles, err := client.SelectBundles(ctx, "19", 1, 0)
	require.NoError(t, err)
	require.Len(t, bundles, 1)
	rc, err := client.DownloadBundle(ctx, bundles[0].DownloadURL)
	require.NoError(t, err)
	require.NoError(t, rc.Close())

	prep, err := client.PrepareBlobUpload(ctx, dgst.String(), size, "")
	require.NoError(t, err)
	require.NoError(t, client.PutBlob(ctx, prep, bytes.NewReader(blob), size))
	_, err = client.CompleteBlobUploads(ctx, []enginecacheservice.BlobUploadCompletion{{
		Digest: dgst.String(), Size: size, UploadID: prep.UploadID,
	}})
	require.NoError(t, err)
	rc, err = client.OpenBlob(ctx, dgst, size)
	require.NoError(t, err)
	require.NoError(t, rc.Close())

	saw := map[string]bool{}
	for _, req := range rec.recorded() {
		switch {
		case strings.HasPrefix(req.path, "/v1/"):
			require.Equal(t, "Bearer "+testToken, req.auth,
				"org-token surface %s must carry the bearer token", req.path)
			saw["v1"] = true
		case strings.HasPrefix(req.path, "/data/"):
			require.Empty(t, req.auth,
				"pre-authorized data path %s must NOT carry the org token (presigned-URL contract)", req.path)
			saw["data"] = true
		default:
			t.Fatalf("unexpected request path %s", req.path)
		}
	}
	require.True(t, saw["v1"] && saw["data"], "both surfaces must have been exercised: %+v", rec.recorded())
}

func TestClientBlobSourceFailureTyping(t *testing.T) {
	ctx := context.Background()
	client, _ := startTestService(t)
	src := client.BlobSource()

	// Absent blob: the seam's permanent-per-boot sentinel.
	_, err := src.OpenBlob(ctx, digest.FromString("nowhere"), 7)
	require.ErrorIs(t, err, snapshots.ErrBlobNotFound)

	// Dead transport: transient — anything but the not-found sentinel.
	deadClient, err := enginecacheservice.NewClient("http://127.0.0.1:1", testToken, "test-scope", enginecacheservice.ClientOptions{})
	require.NoError(t, err)
	_, err = deadClient.BlobSource().OpenBlob(ctx, digest.FromString("unreachable"), 7)
	require.Error(t, err)
	require.False(t, errors.Is(err, snapshots.ErrBlobNotFound),
		"transport failure must stay transient, never the permanent not-found type")
}

func TestClientRejectsUnauthenticatedAccess(t *testing.T) {
	ctx := context.Background()
	svc, err := testservice.New(t.TempDir(), testToken)
	require.NoError(t, err)
	server := httptest.NewServer(svc.Handler())
	t.Cleanup(server.Close)

	badClient, err := enginecacheservice.NewClient(server.URL, "wrong-token", "test-scope", enginecacheservice.ClientOptions{})
	require.NoError(t, err)
	_, err = badClient.SelectBundles(ctx, "19", 1, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")

	// Data paths reject signature-less access outright.
	resp, err := http.Get(server.URL + "/data/blobs/" + digest.FromString("x").String())
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
