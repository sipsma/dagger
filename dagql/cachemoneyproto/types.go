package cachemoneyproto

import "encoding/json"

const (
	Version = 2

	MetadataDBName = "dagql-cache.db"
	ManifestName   = "cachemoney-v2.json"

	MultipartMetadataField = "metadata"
	MultipartManifestField = "manifest"
)

type SnapshotRole string

type BeginExportManifest struct {
	Version   int             `json:"version"`
	Snapshots []SnapshotOffer `json:"snapshots,omitempty"`
}

type SnapshotOffer struct {
	ResultID uint64        `json:"resultID"`
	Role     SnapshotRole  `json:"role"`
	Chain    SnapshotChain `json:"chain"`
}

type SnapshotChain struct {
	ChainID string          `json:"chainID"`
	Layers  []SnapshotLayer `json:"layers,omitempty"`
}

type SnapshotLayer struct {
	DiffID         string          `json:"diffID"`
	BlobDigest     string          `json:"blobDigest"`
	Size           int64           `json:"size"`
	MediaType      string          `json:"mediaType"`
	DescriptorJSON json.RawMessage `json:"descriptorJSON,omitempty"`
}

type BeginExportResponse struct {
	Version        int      `json:"version"`
	ExportID       string   `json:"exportID"`
	RequestedBlobs []string `json:"requestedBlobs,omitempty"`
	UploadURL      string   `json:"uploadURL"`
	CompleteURL    string   `json:"completeURL"`
}

type BlobUploadRequest struct {
	ExportID   string `json:"exportID"`
	BlobDigest string `json:"blobDigest"`
	Size       int64  `json:"size"`
	MediaType  string `json:"mediaType"`
}

type BlobUploadResponse struct {
	Method        string `json:"method,omitempty"`
	URL           string `json:"url,omitempty"`
	AlreadyExists bool   `json:"alreadyExists,omitempty"`
}

type CompleteExportRequest struct {
	Version  int      `json:"version"`
	ExportID string   `json:"exportID"`
	Blobs    []string `json:"blobs,omitempty"`
}

type CompleteExportResponse struct {
	Version  int    `json:"version"`
	ExportID string `json:"exportID"`
}

type ImportManifest struct {
	Version          int                     `json:"version"`
	MetadataSourceID string                  `json:"metadataSourceID"`
	BlobIndex        map[string]BlobLocation `json:"blobIndex,omitempty"`
}

type BlobLocation struct {
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}
