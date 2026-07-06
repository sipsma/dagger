package dagql

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"
)

// A cache bundle is one engine's retained cache state in the same encoding
// local persistence writes — a tar.zst archive of:
//
//	manifest.json  — small service-readable header (the only part bundle
//	                 selection ever touches)
//	metadata.db    — SQLite in the local mirror schema; the engine-local
//	                 tables (result_snapshot_links, snapshot_content_links,
//	                 imported_layer_*) are present in the shared DDL but
//	                 never populated and never read from a bundle
//
// Integer IDs inside metadata.db are intra-bundle join keys only; every
// row's durable identity is its origin pair in result_origins.
const (
	CacheBundleFormatVersion = 1

	cacheBundleManifestName = "manifest.json"
	cacheBundleMetadataName = "metadata.db"
)

// CacheBundleManifest is the bundle's header. Chains and the blob index
// stay empty until content chains ship (they are declared now so the
// format does not bump for them).
type CacheBundleManifest struct {
	BundleFormat  int       `json:"bundleFormat"`
	SchemaVersion string    `json:"schemaVersion"`
	EngineVersion string    `json:"engineVersion,omitempty"`
	StoreUUID     string    `json:"storeUUID"`
	Scope         string    `json:"scope,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`

	Counts CacheBundleCounts `json:"counts"`

	// Roots are the persisted-edge root result IDs, as intra-bundle join
	// keys.
	Roots []uint64 `json:"roots,omitempty"`

	Chains       []CacheBundleChain       `json:"chains,omitempty"`
	ResultChains []CacheBundleResultChain `json:"resultChains,omitempty"`
	BlobIndex    []string                 `json:"blobIndex,omitempty"`
}

type CacheBundleCounts struct {
	Results   int   `json:"results"`
	Chains    int   `json:"chains"`
	Blobs     int   `json:"blobs"`
	BlobBytes int64 `json:"blobBytes"`
}

type CacheBundleChain struct {
	ChainID string                  `json:"chainID"`
	Layers  []CacheBundleChainLayer `json:"layers"`
}

type CacheBundleChainLayer struct {
	DiffID    string `json:"diffID"`
	Blob      string `json:"blob"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}

type CacheBundleResultChain struct {
	ResultID uint64 `json:"resultID"`
	Role     string `json:"role"`
	ChainID  string `json:"chainID"`
}

// CacheBundleSkipError says a bundle could not be imported and was skipped
// whole. It is the ONLY failure type bundle import produces for store-level
// damage: it deliberately shares nothing with the local restore's
// import-failure vocabulary, so no bundle — however corrupt — can reach the
// local wipe/reset machinery (engine/server's reset loop consumes only
// local-restore errors).
type CacheBundleSkipError struct {
	Reason string
	Err    error
}

func (e *CacheBundleSkipError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("cache bundle skipped: %s", e.Reason)
	}
	return fmt.Sprintf("cache bundle skipped: %s: %v", e.Reason, e.Err)
}

func (e *CacheBundleSkipError) Unwrap() error {
	return e.Err
}

func bundleSkip(reason string, err error) *CacheBundleSkipError {
	return &CacheBundleSkipError{Reason: reason, Err: err}
}

// Bundle-skip reasons, counted by import summaries.
const (
	CacheBundleSkipUnreadableArchive  = "unreadable_archive"
	CacheBundleSkipManifestMismatch   = "manifest_mismatch"
	CacheBundleSkipUnreadableMetadata = "unreadable_metadata"
	CacheBundleSkipBrokenIdentity     = "broken_identity"
	CacheBundleSkipMalformedChains    = "malformed_chains"
)

// contentlessPersistedTypeNames are the persisted object types whose rows
// may cross an engine boundary carrying identity only: their decoders
// tolerate absent snapshot links and re-acquire content lazily on first
// use. Every other row that claims local snapshot content must carry a
// re-make fallback to be exported. Registration happens from init()
// functions in the packages that own the types.
var contentlessPersistedTypeNames = map[string]struct{}{}

// RegisterContentlessPersistedType marks a persisted object type name as
// safe to cross engine boundaries identity-only. Must be called from
// init(); the set is read-only afterward.
func RegisterContentlessPersistedType(typeName string) {
	contentlessPersistedTypeNames[typeName] = struct{}{}
}

func isContentlessPersistedType(typeName string) bool {
	_, ok := contentlessPersistedTypeNames[typeName]
	return ok
}

// PersistenceSchemaVersion is the engine's cache persistence schema
// version, as bundle manifests carry it — what a booting engine sends to
// bundle selection so only byte-compatible bundles are offered.
func (c *Cache) PersistenceSchemaVersion() string {
	return cachePersistenceSchemaVersion
}

// ReadCacheBundleManifest reads only the manifest from a bundle archive
// stream. The manifest is the archive's first entry by construction
// (writeCacheBundleArchive), so this never decompresses the metadata DB. It
// returns the raw manifest bytes alongside the parsed form so a caller
// re-sending the manifest (the multipart publish part) stays byte-identical
// with the archive's copy.
func ReadCacheBundleManifest(r io.Reader) (CacheBundleManifest, []byte, error) {
	var manifest CacheBundleManifest
	zr, err := zstd.NewReader(r)
	if err != nil {
		return manifest, nil, fmt.Errorf("open bundle zstd reader: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	hdr, err := tr.Next()
	if err != nil {
		return manifest, nil, fmt.Errorf("read bundle first entry: %w", err)
	}
	if hdr.Name != cacheBundleManifestName {
		return manifest, nil, fmt.Errorf("bundle first entry is %q, want %q", hdr.Name, cacheBundleManifestName)
	}
	manifestJSON, err := io.ReadAll(tr)
	if err != nil {
		return manifest, nil, fmt.Errorf("read bundle manifest: %w", err)
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return manifest, nil, fmt.Errorf("parse bundle manifest: %w", err)
	}
	return manifest, manifestJSON, nil
}

// CacheBundleBootSummary is the boot-time bundle inflow's outcome (§8 D4's
// degradation made diagnosable): what selection offered, what actually
// fetched and merged, what was skipped and why, and what the merges did.
// It lands in the stats file and the debug snapshots next to the restore
// summary — a cold start that should have been warm is answerable from the
// engine's own evidence.
type CacheBundleBootSummary struct {
	BundlesOffered        int            `json:"bundles_offered"`
	BundlesFetched        int            `json:"bundles_fetched"`
	BundlesMerged         int            `json:"bundles_merged"`
	SkippedByReason       map[string]int `json:"bundles_skipped_by_reason,omitempty"`
	RowsImported          int            `json:"rows_imported"`
	RowsDedupedByOrigin   int            `json:"rows_deduped_by_origin"`
	ImportBudgetExhausted bool           `json:"import_budget_exhausted"`
}

// SetBundleBootSummary records the boot inflow outcome for the stats file
// and debug snapshots. Called once, during the boot window.
func (c *Cache) SetBundleBootSummary(summary *CacheBundleBootSummary) {
	if c == nil {
		return
	}
	c.egraphMu.Lock()
	c.bundleBootSummary = summary
	c.egraphMu.Unlock()
}

// writeCacheBundleArchive streams manifest.json + metadata.db as tar.zst.
func writeCacheBundleArchive(w io.Writer, manifest CacheBundleManifest, metadataDBPath string) (rerr error) {
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bundle manifest: %w", err)
	}

	zw, err := zstd.NewWriter(w)
	if err != nil {
		return fmt.Errorf("open bundle zstd writer: %w", err)
	}
	defer func() {
		if cerr := zw.Close(); cerr != nil && rerr == nil {
			rerr = fmt.Errorf("close bundle zstd writer: %w", cerr)
		}
	}()
	tw := tar.NewWriter(zw)
	defer func() {
		if cerr := tw.Close(); cerr != nil && rerr == nil {
			rerr = fmt.Errorf("close bundle tar writer: %w", cerr)
		}
	}()

	if err := tw.WriteHeader(&tar.Header{
		Name: cacheBundleManifestName,
		Mode: 0o644,
		Size: int64(len(manifestJSON)),
	}); err != nil {
		return fmt.Errorf("write bundle manifest header: %w", err)
	}
	if _, err := tw.Write(manifestJSON); err != nil {
		return fmt.Errorf("write bundle manifest: %w", err)
	}

	metadataFile, err := os.Open(metadataDBPath)
	if err != nil {
		return fmt.Errorf("open bundle metadata db: %w", err)
	}
	defer metadataFile.Close()
	metadataStat, err := metadataFile.Stat()
	if err != nil {
		return fmt.Errorf("stat bundle metadata db: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: cacheBundleMetadataName,
		Mode: 0o644,
		Size: metadataStat.Size(),
	}); err != nil {
		return fmt.Errorf("write bundle metadata header: %w", err)
	}
	if _, err := io.Copy(tw, metadataFile); err != nil {
		return fmt.Errorf("write bundle metadata: %w", err)
	}
	return nil
}

// readCacheBundleArchive unpacks a bundle stream into dir, returning the
// parsed manifest and the extracted metadata DB path. Any structural
// problem is a skip error. V1 is strict: exactly the two known entries.
func readCacheBundleArchive(r io.Reader, dir string) (CacheBundleManifest, string, error) {
	var manifest CacheBundleManifest

	zr, err := zstd.NewReader(r)
	if err != nil {
		return manifest, "", bundleSkip(CacheBundleSkipUnreadableArchive, fmt.Errorf("open zstd reader: %w", err))
	}
	defer zr.Close()
	tr := tar.NewReader(zr)

	var haveManifest, haveMetadata bool
	metadataPath := filepath.Join(dir, cacheBundleMetadataName)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest, "", bundleSkip(CacheBundleSkipUnreadableArchive, fmt.Errorf("read tar entry: %w", err))
		}
		switch hdr.Name {
		case cacheBundleManifestName:
			manifestJSON, err := io.ReadAll(tr)
			if err != nil {
				return manifest, "", bundleSkip(CacheBundleSkipUnreadableArchive, fmt.Errorf("read manifest: %w", err))
			}
			if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
				return manifest, "", bundleSkip(CacheBundleSkipUnreadableArchive, fmt.Errorf("parse manifest: %w", err))
			}
			haveManifest = true
		case cacheBundleMetadataName:
			f, err := os.OpenFile(metadataPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return manifest, "", fmt.Errorf("create bundle metadata file: %w", err)
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return manifest, "", bundleSkip(CacheBundleSkipUnreadableArchive, fmt.Errorf("extract metadata db: %w", err))
			}
			haveMetadata = true
		default:
			return manifest, "", bundleSkip(CacheBundleSkipUnreadableArchive, fmt.Errorf("unexpected bundle entry %q", hdr.Name))
		}
	}
	if !haveManifest || !haveMetadata {
		return manifest, "", bundleSkip(CacheBundleSkipUnreadableArchive, fmt.Errorf("bundle missing required entries (manifest=%t, metadata=%t)", haveManifest, haveMetadata))
	}
	return manifest, metadataPath, nil
}
