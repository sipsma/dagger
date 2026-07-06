// Package cacheservice is the engine's client for the remote cache service
// (service design §10.1): bundle publish/selection/download, blob
// stat/upload/fetch. The wire shapes are the service's as-built protocol
// (dagger.io api/server/cacheservice); the engine conforms to them — any
// endpoint speaking this protocol works, which the in-repo test service
// (internal/testutil/cacheservice) proves.
package cacheservice

import (
	"encoding/json"
	"time"
)

const (
	// Multipart field names for bundle publish.
	MultipartManifestField = "manifest"
	MultipartArchiveField  = "archive"

	// Batch limits the service enforces; the client chunks its batches to
	// stay under them.
	MaxStatDigests       = 4096
	MaxUploadBatch       = 1024
	MaxSelectionLimit    = 32
	BundleStatusPending  = "pending"
	BundleStatusComplete = "complete"
)

// PublishBundleResponse is the publish result: the service-minted bundle ID.
type PublishBundleResponse struct {
	BundleID string `json:"bundleID"`
}

// BundleUploadTally is the exporter's blob-upload summary attached to bundle
// completion. The service whitelists exactly these fields into its event
// log.
type BundleUploadTally struct {
	Uploaded       int64 `json:"uploaded,omitempty"`
	AlreadyPresent int64 `json:"alreadyPresent,omitempty"`
	Failed         int64 `json:"failed,omitempty"`
	Skipped        int64 `json:"skipped,omitempty"`
	Bytes          int64 `json:"bytes,omitempty"`
	DurationMS     int64 `json:"durationMS,omitempty"`
}

// BundleSummary is one selectable bundle, as the selection endpoint lists
// it. DownloadURL is pre-authorized (presigned or URL-signed): the client
// fetches it with no org token attached.
type BundleSummary struct {
	BundleID    string          `json:"bundleID"`
	StoreUUID   string          `json:"storeUUID"`
	CreatedAt   time.Time       `json:"createdAt"`
	Status      string          `json:"status"`
	DownloadURL string          `json:"downloadURL"`
	Counts      json.RawMessage `json:"counts,omitempty"`
}

type ListBundlesResponse struct {
	Bundles []BundleSummary `json:"bundles"`
}

type BlobStatRequest struct {
	Digests []string `json:"digests"`
}

type BlobStatResponse struct {
	Missing []string `json:"missing"`
}

type BlobUploadRequest struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType,omitempty"`
}

// BlobUploadResponse is the upload target for one blob. UploadID is the
// service's opaque completion authority (a signed token service-side);
// clients pass it back verbatim on completion. URL is pre-authorized: the
// PUT carries no org token.
type BlobUploadResponse struct {
	AlreadyExists bool   `json:"alreadyExists,omitempty"`
	Method        string `json:"method,omitempty"`
	URL           string `json:"url,omitempty"`
	UploadID      string `json:"uploadID,omitempty"`
}

type CompleteBlobUploadsRequest struct {
	Blobs []BlobUploadCompletion `json:"blobs"`
}

type BlobUploadCompletion struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType,omitempty"`
	UploadID  string `json:"uploadID,omitempty"`
}

type CompleteBlobUploadsResponse struct {
	Verified      []string            `json:"verified,omitempty"`
	AlreadyExists []string            `json:"alreadyExists,omitempty"`
	Failed        []BlobUploadFailure `json:"failed,omitempty"`
}

type BlobUploadFailure struct {
	Digest string `json:"digest"`
	Error  string `json:"error"`
}
