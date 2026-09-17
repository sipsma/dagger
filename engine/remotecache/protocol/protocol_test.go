package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dagger/dagger/dagql"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/engine/snapshots"
)

// roundTrip encodes value, decodes the bytes into a fresh value of the same
// type with unknown fields refused, and requires the two encodings to be
// equal.
func roundTrip[T any](t *testing.T, value T) {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded T
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&decoded))
	again, err := json.Marshal(decoded)
	require.NoError(t, err)
	require.JSONEq(t, string(encoded), string(again))
}

func exampleBundle() dagql.ValueBundle {
	layer := snapshots.ExportLayer{Descriptor: ocispecs.Descriptor{MediaType: ocispecs.MediaTypeImageLayer, Digest: digest.FromString("layer"), Size: 5}}
	return dagql.ValueBundle{
		Version: 2,
		Roots:   []dagql.TransferredRoot{{Ordinal: 1}},
		Values: []dagql.TransferredValue{{
			Ordinal: 1,
			Record:  dagql.PersistedRecord{ResultID: 1, Call: &dagql.ResultCall{Kind: dagql.ResultCallKindField, Field: "directory", Type: &dagql.ResultCallType{NamedType: "Directory", NonNull: true}}},
		}},
		Outputs: []dagql.TransferredOutput{{
			Ordinal: 1,
			Address: dagql.PersistedPartAddress{Part: "snapshot"},
			State:   "completed",
			Value:   &dagql.SnapshotValue{Kind: "directory"},
			Chain:   &dagql.OfferedChain{Layers: []snapshots.ExportLayer{layer}, Addresses: map[digest.Digest]dagql.BlobAddress{layer.Descriptor.Digest: {URL: "http://blobs:5000/remote-cache/blobs/sha256/x"}}},
			Owner:   &dagql.PersistedOfferOwner{},
		}},
	}
}

func TestTypesRoundTrip(t *testing.T) {
	bundle := exampleBundle()
	bundleJSON, err := json.Marshal(bundle)
	require.NoError(t, err)

	roundTrip(t, PollRequest{EngineName: "engine-a", EngineVersion: "v1.0.0-dev", WaitSeconds: 25})
	roundTrip(t, PollResponse{Commands: []Command{
		{ID: "c-0001", Type: CommandTypeImport, Import: &ImportCommand{BundleID: BundleID(bundleJSON), Bundle: bundle}},
		{ID: "c-0002", Type: CommandTypeExport, Export: &ExportCommand{Root: 58, PartsOf: []uint64{58, 57}}},
	}})
	roundTrip(t, PollResponse{Commands: []Command{}})
	roundTrip(t, CommandResult{OK: true})
	roundTrip(t, CommandResult{OK: false, Error: "import failed"})
	roundTrip(t, CommandResult{OK: true, Status: ExportStatusExported, BundleID: BundleID(bundleJSON)})
	roundTrip(t, CommandResult{OK: false, Status: ExportStatusNotFound, Error: "result 58 is not in the cache"})
	roundTrip(t, CommandResult{OK: false, Status: ExportStatusNotReady, Error: "changing"})
	roundTrip(t, CommandResult{OK: false, Status: ExportStatusFailed, Error: "upload failed"})
	roundTrip(t, SessionReport{SessionID: "s1", Results: []ReportedResult{
		{Result: 58, Type: "Directory", Field: "directory", DependsOn: []uint64{57}, Retained: true, RecipeDigest: "xxh3:1f0c"},
		{Result: 60, Type: "String", Field: "stdout", DependsOn: []uint64{}, Retained: false},
		{Result: 12, Type: "Container", Field: "from", DependsOn: []uint64{}, Retained: true, Imported: true},
	}})
	roundTrip(t, BlobCheckRequest{Blobs: []BlobDescriptor{{Digest: digest.FromString("a"), Size: 1}, {Digest: digest.FromString("b"), Size: 2048}}})
	roundTrip(t, BlobCheckResponse{Upload: map[digest.Digest]UploadTarget{digest.FromString("b"): {URL: "http://blobs:5000/put"}}})
	roundTrip(t, BlobCheckResponse{Upload: map[digest.Digest]UploadTarget{}})
	roundTrip(t, BundleUploadRequest{CommandID: "c-0002", Bundle: bundleJSON})
	roundTrip(t, BundleUploadResponse{BundleID: BundleID(bundleJSON)})
	roundTrip(t, ErrorResponse{Error: "unknown token"})
	roundTrip(t, ErrorResponse{Error: "missing layers", Missing: []digest.Digest{digest.FromString("b")}})
}

// TestBundleUploadRequestKeepsBundleBytes checks the property the bundle ID
// rests on: the bundle member of a decoded upload request is the bytes the
// sender put there, so BundleID over it on the receiving side equals
// BundleID over the encoded bundle on the sending side.
func TestBundleUploadRequestKeepsBundleBytes(t *testing.T) {
	bundleJSON, err := json.Marshal(exampleBundle())
	require.NoError(t, err)
	body, err := json.Marshal(BundleUploadRequest{CommandID: "c-0002", Bundle: bundleJSON})
	require.NoError(t, err)
	var received BundleUploadRequest
	require.NoError(t, json.Unmarshal(body, &received))
	require.Equal(t, string(bundleJSON), string(received.Bundle))
	require.Equal(t, BundleID(bundleJSON), BundleID(received.Bundle))
	require.Equal(t, "sha256:"+digest.FromBytes(bundleJSON).Encoded(), BundleID(bundleJSON))
	var decoded dagql.ValueBundle
	require.NoError(t, json.Unmarshal(received.Bundle, &decoded))
	require.Equal(t, exampleBundle(), decoded)
}

func TestCommandResultPath(t *testing.T) {
	require.Equal(t, "/v1/commands/c-0002/result", CommandResultPath("c-0002"))
}

// TestExamplesRoundTrip decodes every example file into its type with
// unknown fields refused, encodes it again, and requires the encoding to
// equal the file. This keeps the example messages and the Go types the same
// contract.
func TestExamplesRoundTrip(t *testing.T) {
	cases := []struct {
		file string
		into func() any
	}{
		{"p1-poll-request.json", func() any { return new(PollRequest) }},
		{"p1-poll-response.json", func() any { return new(PollResponse) }},
		{"p2-import-result.json", func() any { return new(CommandResult) }},
		{"p2-import-result-failed.json", func() any { return new(CommandResult) }},
		{"p2-export-result.json", func() any { return new(CommandResult) }},
		{"p2-export-result-not-found.json", func() any { return new(CommandResult) }},
		{"p2-export-result-failed.json", func() any { return new(CommandResult) }},
		{"p3-session-report.json", func() any { return new(SessionReport) }},
		{"p4-blob-check-request.json", func() any { return new(BlobCheckRequest) }},
		{"p4-blob-check-response.json", func() any { return new(BlobCheckResponse) }},
		{"p5-bundle-upload-request.json", func() any { return new(BundleUploadRequest) }},
		{"p5-bundle-upload-response.json", func() any { return new(BundleUploadResponse) }},
		{"p5-bundle-upload-conflict.json", func() any { return new(ErrorResponse) }},
		{"error.json", func() any { return new(ErrorResponse) }},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			seen[tc.file] = true
			raw, err := os.ReadFile(filepath.Join("examples", tc.file))
			require.NoError(t, err)
			value := tc.into()
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			require.NoError(t, dec.Decode(value))
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(encoded))
		})
	}
	// The P5 request's bundle must decode as a ValueBundle with unknown
	// fields refused, and the P1 import command carries it typed already.
	t.Run("p5 bundle decodes as a ValueBundle", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("examples", "p5-bundle-upload-request.json"))
		require.NoError(t, err)
		var req BundleUploadRequest
		require.NoError(t, json.Unmarshal(raw, &req))
		dec := json.NewDecoder(bytes.NewReader(req.Bundle))
		dec.DisallowUnknownFields()
		var bundle dagql.ValueBundle
		require.NoError(t, dec.Decode(&bundle))
		require.Len(t, bundle.Outputs, 1)
		require.Empty(t, bundle.Outputs[0].Chain.Addresses, "the engine posts a bundle with no addresses")
		require.Len(t, bundle.Outputs[0].Chain.Layers, 2)
		require.Equal(t, "sha256:"+digest.FromBytes(req.Bundle).Encoded(), BundleID(req.Bundle))
	})
	t.Run("p1 import bundle has one address per layer", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join("examples", "p1-poll-response.json"))
		require.NoError(t, err)
		var resp PollResponse
		require.NoError(t, json.Unmarshal(raw, &resp))
		require.Len(t, resp.Commands, 2)
		require.Equal(t, CommandTypeImport, resp.Commands[0].Type)
		chain := resp.Commands[0].Import.Bundle.Outputs[0].Chain
		require.Len(t, chain.Addresses, len(chain.Layers))
		for _, layer := range chain.Layers {
			require.Contains(t, chain.Addresses, layer.Descriptor.Digest)
		}
		require.Equal(t, CommandTypeExport, resp.Commands[1].Type)
		require.Equal(t, uint64(58), resp.Commands[1].Export.Root)
	})
	// Every example file is covered by a case.
	entries, err := os.ReadDir("examples")
	require.NoError(t, err)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			require.True(t, seen[entry.Name()], "example %s has no round-trip case", entry.Name())
		}
	}
}
