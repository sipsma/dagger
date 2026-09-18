// Package protocol holds the wire types of the remote cache service
// protocol: the request and response bodies the engine exchanges with the
// cache service over HTTP with JSON bodies.
//
// The engine opens every connection. The service never connects to an
// engine. Every request carries the Authorization header with the bearer
// token from the engine's token environment variable, and the
// HeaderEngineInstance header with the engine instance ID.
//
// This package imports dagql for the bundle type and nothing from
// engine/server, so the cache service can import it without linking the
// engine server.
//
// The examples directory beside this package holds one JSON message per
// endpoint. The round-trip test decodes each one into its type with unknown
// fields refused and encodes it again, so the examples are the shared
// contract of the engine and the service.
package protocol

import (
	"encoding/json"

	"github.com/dagger/dagger/dagql"
	"github.com/opencontainers/go-digest"
)

// HeaderEngineInstance carries the engine instance ID: 16 random bytes, hex
// encoded, created once when the engine's integration starts. A restarted
// engine process has a new engine instance ID.
const HeaderEngineInstance = "Dagger-Engine-Instance"

// Request paths, relative to the service's base URL.
const (
	// PathPoll is P1. The engine keeps exactly one poll request open.
	PathPoll = "/v1/poll"
	// PathSessionReports is P3. Sent when a session has ended. The service
	// answers 202.
	PathSessionReports = "/v1/session-reports"
	// PathBlobCheck is P4. Sent before the blobs of one export are uploaded.
	PathBlobCheck = "/v1/blobs/check"
	// PathBundles is P5. Sent after every blob of one export is uploaded.
	PathBundles = "/v1/bundles"
)

// CommandResultPath is the path of P2 for one command: the engine has
// finished or given up on that command. The service answers 204.
func CommandResultPath(commandID string) string {
	return "/v1/commands/" + commandID + "/result"
}

// MaxPollWaitSeconds is the longest wait the service honors in a poll.
const MaxPollWaitSeconds = 30

// MaxCommandsPerPoll is the most commands one poll response carries.
const MaxCommandsPerPoll = 16

// PollRequest is the body of P1.
type PollRequest struct {
	EngineName    string `json:"engineName"`
	EngineVersion string `json:"engineVersion"`
	// WaitSeconds is how long the service may hold the request open when it
	// has no command queued. The service caps it at MaxPollWaitSeconds.
	// Zero asks for an immediate answer with whatever is queued; the engine
	// sends it while starting up, page after page until one is empty, which
	// its startup waits on.
	WaitSeconds int `json:"waitSeconds"`
}

// PollResponse is the response of P1. Commands is empty when the wait
// passed with nothing queued. A command is delivered at most once.
type PollResponse struct {
	Commands []Command `json:"commands"`
}

// CommandType names what a command asks the engine to do.
type CommandType string

const (
	CommandTypeImport CommandType = "import"
	CommandTypeExport CommandType = "export"
)

// Command is one message from the service to one engine instance. Exactly
// one of Import and Export is set, matching Type.
type Command struct {
	ID     string         `json:"id"`
	Type   CommandType    `json:"type"`
	Import *ImportCommand `json:"import,omitempty"`
	Export *ExportCommand `json:"export,omitempty"`
}

// ImportCommand asks the engine to import one bundle. Bundle is complete and
// inline, with every chain's Addresses filled in by the service. The engine
// does not fetch it from the blob store.
type ImportCommand struct {
	BundleID string            `json:"bundleId"`
	Bundle   dagql.ValueBundle `json:"bundle"`
}

// ExportCommand asks the engine to export one result. Root is a result
// number from a session report that this same engine instance sent. PartsOf
// lists result numbers whose completed parts the engine uploads; every one
// of them is the root or a result the root depends on, directly or
// indirectly. A PartsOf number the engine no longer holds means fewer
// uploads, not a failure.
type ExportCommand struct {
	Root    uint64   `json:"root"`
	PartsOf []uint64 `json:"partsOf"`
}

// ExportStatus is the outcome of an export command.
type ExportStatus string

const (
	// ExportStatusExported means the bundle was posted with P5. BundleID is
	// what P5 returned.
	ExportStatusExported ExportStatus = "exported"
	// ExportStatusNotFound means the root's result number is not in the
	// engine's cache any more.
	ExportStatusNotFound ExportStatus = "not_found"
	// ExportStatusNotReady means export was refused because the result was
	// changing. The service may send the same command again later.
	ExportStatusNotReady ExportStatus = "not_ready"
	// ExportStatusFailed is any other failure. Error holds the text.
	ExportStatusFailed ExportStatus = "failed"
)

// CommandResult is the body of P2.
//
// For an import command only OK and Error are set. For an export command
// Status is always set, OK is true exactly when Status is
// ExportStatusExported, BundleID is set when OK is true, and Error holds the
// failure text otherwise.
type CommandResult struct {
	OK       bool         `json:"ok"`
	Error    string       `json:"error,omitempty"`
	Status   ExportStatus `json:"status,omitempty"`
	BundleID string       `json:"bundleId,omitempty"`
}

// SessionReport is the body of P3: the results one ended session touched on
// the sending engine. The engine sends it only when Results has at least one
// entry that is retained and not imported, and sends it once.
type SessionReport struct {
	SessionID string           `json:"sessionId"`
	Results   []ReportedResult `json:"results"`
}

// ReportedResult describes one result of a session report.
type ReportedResult struct {
	// Result is the result number on the sending engine. It identifies the
	// result inside that one engine instance only.
	Result uint64 `json:"result"`
	// Type is the GraphQL type name of the result's call.
	Type string `json:"type"`
	// Field is the field name of the result's call.
	Field string `json:"field"`
	// DependsOn lists the result numbers this result depends on directly. A
	// number here can be missing from the report's Results, because a
	// dependency need not be in the session's set. Always present, empty
	// when the result has no dependencies.
	DependsOn []uint64 `json:"dependsOn"`
	// Retained is true when the result has a retention edge, so it survives
	// its session's end until it expires or is pruned.
	Retained bool `json:"retained"`
	// Imported is true when the result came from an import.
	Imported bool `json:"imported"`
	// RecipeDigest is the digest of the result's call, the same on every
	// engine that makes the same call with the same inputs. Present only
	// when Retained is true and Imported is false.
	RecipeDigest string `json:"recipeDigest,omitempty"`
}

// BlobCheckRequest is the body of P4: every layer of every chain of one
// export, without duplicates.
type BlobCheckRequest struct {
	Blobs []BlobDescriptor `json:"blobs"`
}

// BlobDescriptor names one blob by the digest of its bytes and its size.
type BlobDescriptor struct {
	Digest digest.Digest `json:"digest"`
	Size   int64         `json:"size"`
}

// BlobCheckResponse is the response of P4. Upload has one entry for each
// listed blob the blob store does not hold. A blob not mentioned needs no
// upload.
type BlobCheckResponse struct {
	Upload map[digest.Digest]UploadTarget `json:"upload"`
}

// UploadTarget says where one blob is uploaded: one HTTP PUT of exactly the
// blob's size in bytes to URL.
type UploadTarget struct {
	URL string `json:"url"`
}

// BundleUploadRequest is the body of P5.
//
// Bundle is the JSON encoding of the dagql.ValueBundle that export produced,
// with every chain's Addresses empty. It is kept as raw bytes so that the
// bundle ID is defined on bytes both sides have seen: the service computes
// BundleID over the Bundle member exactly as it arrived, and only then
// decodes it.
type BundleUploadRequest struct {
	CommandID string          `json:"commandId"`
	Bundle    json.RawMessage `json:"bundle"`
}

// BundleUploadResponse is the response of P5.
type BundleUploadResponse struct {
	BundleID string `json:"bundleId"`
}

// BundleID computes a bundle ID: the SHA-256 digest of the bundle's JSON
// bytes as the exporting engine uploaded them, in the form "sha256:<hex>".
func BundleID(bundleJSON []byte) string {
	return digest.SHA256.FromBytes(bundleJSON).String()
}

// ErrorResponse is the body of every non-2xx response. Missing is set only
// on the 409 answer to P5, and lists the layer digests the blob store does
// not hold.
type ErrorResponse struct {
	Error   string          `json:"error"`
	Missing []digest.Digest `json:"missing,omitempty"`
}
