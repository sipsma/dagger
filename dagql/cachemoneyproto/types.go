package cachemoneyproto

import ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

const (
	Version = 1

	ImportManifestName = "cachemoney-import.json"
)

type ExportResponse struct {
	ExportID    string       `json:"exportID"`
	CompleteURL string       `json:"completeURL"`
	Uploads     []UploadTask `json:"uploads"`
}

type UploadTask struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Method string `json:"method"`
	URL    string `json:"url"`
}

type ImportManifest struct {
	Version          int            `json:"version"`
	MetadataSourceID string         `json:"metadataSourceID"`
	Sources          []RemoteSource `json:"sources"`
}

type RemoteSource struct {
	ID        string           `json:"id"`
	Snapshots []RemoteSnapshot `json:"snapshots"`
}

type RemoteSnapshot struct {
	RefKey  string        `json:"refKey"`
	ChainID string        `json:"chainID,omitempty"`
	Layers  []RemoteLayer `json:"layers"`
}

type RemoteLayer struct {
	Descriptor ocispecs.Descriptor `json:"descriptor"`
	URL        string              `json:"url"`
}
