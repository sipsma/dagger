package dagql

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine/slog"
)

type CacheBundleExportOptions struct {
	// Scope is the cache-scope string stamped into the manifest; the
	// engine does not interpret it.
	Scope string
	// EngineVersion is stamped into the manifest.
	EngineVersion string
}

// CacheBundleExportSummary reports what an export emitted and, loudly, what
// it excluded and why. Exclusion is degradation, never an export failure.
type CacheBundleExportSummary struct {
	Results   int
	Roots     int
	EqClasses int
	Terms     int

	// Chains/Blobs/BlobBytes mirror the manifest counts; BlobIndex is every
	// blob digest the bundle's chains reference — what the caller offers to
	// (and existence-checks against) a CAS after the metadata publishes.
	Chains    int
	Blobs     int
	BlobBytes int64
	BlobIndex []string

	// ExcludedNoPortableContent counts rows whose only content source is a
	// local snapshot with no cross-boundary re-make path (no computable
	// content chain, no lazy fragment, not a sanctioned identity-only
	// type): exporting them would promise content the importer can never
	// obtain. Their in-bundle dependents drop at import by the missing-dep
	// rule.
	ExcludedNoPortableContent int
	// ChainComputeFailed counts rows whose content chain failed to compute
	// at export time (e.g. the snapshot vanished mid-export). The row
	// degrades per-result: it still exports when a lazy fragment remains,
	// else it is excluded (counted above) — never an export failure.
	ChainComputeFailed int
	// ExcludedEncodeFailed counts rows whose envelope failed to encode at
	// export time; the row stays local, the bundle just doesn't carry it.
	ExcludedEncodeFailed int
}

// ExportBundle writes the store's retained cache state as a bundle to w:
// the forward dependency closure of the persisted-edge roots, in the local
// persistence encoding, minus the engine-local tables, plus origins.
// Content chains are not emitted yet; rows whose content cannot cross
// degrade per-row (counted), never fail the export.
func (c *Cache) ExportBundle(ctx context.Context, w io.Writer, opts CacheBundleExportOptions) (CacheBundleExportSummary, error) {
	var summary CacheBundleExportSummary
	if c == nil || c.pdb == nil || c.storeUUID == "" {
		return summary, errors.New("export bundle: cache has no persistence store")
	}

	snapshot, err := c.copyOutPersistState()
	if err != nil {
		return summary, fmt.Errorf("export bundle: snapshot persist state: %w", err)
	}

	// The export closure is the forward dependency closure of the
	// persisted-edge roots: each root plus everything it transitively
	// depends on — the same direction retention propagates. Rows outside it
	// are live sessions' in-flight state, not durable cache.
	rowsByID := make(map[sharedResultID]*persistResultSnapshot, len(snapshot.results))
	for i := range snapshot.results {
		rowsByID[snapshot.results[i].resultID] = &snapshot.results[i]
	}
	closure := make(map[sharedResultID]struct{})
	queue := make([]sharedResultID, 0, len(snapshot.persistedEdges))
	for _, edge := range snapshot.persistedEdges {
		rootID := sharedResultID(edge.ResultID)
		if _, seen := closure[rootID]; seen {
			continue
		}
		if _, exists := rowsByID[rootID]; !exists {
			continue
		}
		closure[rootID] = struct{}{}
		queue = append(queue, rootID)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		row := rowsByID[id]
		for _, dep := range row.resultDeps {
			depID := sharedResultID(dep.DepResultID)
			if _, seen := closure[depID]; seen {
				continue
			}
			if _, exists := rowsByID[depID]; !exists {
				continue
			}
			closure[depID] = struct{}{}
			queue = append(queue, depID)
		}
	}

	// Filter rows to the closure, applying the portability rule: a row
	// that claims local snapshot content crosses with its content chain —
	// reused verbatim when the row already carries one (imported rows,
	// hydrated arrivals), computed at the boundary otherwise (R4's
	// hash-at-export). A row with no computable chain and no lazy fragment
	// is excluded unless its type is sanctioned to cross identity-only.
	// Excluded rows' deps rows may dangle inside the bundle; the importer's
	// missing-dep rule drops the dependents there (by design — the writer
	// never guesses at content it cannot promise).
	var bundled persistStateSnapshot
	included := make(map[sharedResultID]struct{}, len(closure))
	for i := range snapshot.results {
		row := &snapshot.results[i]
		if _, inClosure := closure[row.resultID]; !inClosure {
			continue
		}
		if err := c.encodeSnapshotResultRow(ctx, row); err != nil {
			summary.ExcludedEncodeFailed++
			slog.Warn("cache bundle export excluding row that failed to encode",
				"sharedResultID", row.resultID, "err", err)
			continue
		}
		// The encoded snapshot link rows are the row's claimed content —
		// the same rows local flush writes to result_snapshot_links.
		claimedLinks := row.resultSnapshotLinks
		contentless := isContentlessPersistedType(rowEnvelopeTypeName(row))
		if contentless {
			// Mutable-owner snapshots never cross as content: the row is
			// identity-only, re-acquired lazily by the importer's decoder.
			row.contentChains = nil
		} else if len(row.contentChains) == 0 && len(claimedLinks) > 0 && c.snapshotManager != nil {
			chains, err := c.computeExportChains(ctx, claimedLinks)
			if err != nil {
				summary.ChainComputeFailed++
				slog.Warn("cache bundle export failed to compute content chain",
					"sharedResultID", row.resultID, "err", err)
			} else {
				row.contentChains = chains
			}
		}
		if len(claimedLinks) > 0 && len(row.contentChains) == 0 && !rowHasLazyFragment(row) && !contentless {
			summary.ExcludedNoPortableContent++
			slog.Debug("cache bundle export excluding row without portable content",
				"sharedResultID", row.resultID, "type", rowEnvelopeTypeName(row))
			continue
		}
		// The snapshotter refKey link rows are engine-local by definition
		// and never cross (R4/R9).
		row.resultSnapshotLinks = nil
		bundled.results = append(bundled.results, *row)
		included[row.resultID] = struct{}{}
	}

	for _, edge := range snapshot.persistedEdges {
		if _, ok := included[sharedResultID(edge.ResultID)]; !ok {
			continue
		}
		// The unpruneable bit is engine-lifetime state and does not cross:
		// imported persisted edges arrive ordinary-pruneable (expiry kept).
		edge.Unpruneable = false
		bundled.persistedEdges = append(bundled.persistedEdges, edge)
	}

	// Identity rows for the closure's digests: seed from the included
	// rows' output classes, then fixpoint over terms — a term belongs to
	// the bundle iff its output class is in the set, and its input classes
	// join the set so every kept identity reference resolves in-bundle.
	classSet := make(map[int64]struct{})
	for _, row := range snapshot.resultOutputEqClasses {
		if _, ok := included[sharedResultID(row.ResultID)]; !ok {
			continue
		}
		bundled.resultOutputEqClasses = append(bundled.resultOutputEqClasses, row)
		classSet[row.EqClassID] = struct{}{}
	}
	termInputsByTermID := make(map[int64][]persistdb.MirrorTermInput, len(snapshot.terms))
	for _, input := range snapshot.termInputs {
		termInputsByTermID[input.TermID] = append(termInputsByTermID[input.TermID], input)
	}
	includedTerms := make(map[int64]struct{})
	for {
		grew := false
		for _, term := range snapshot.terms {
			if _, done := includedTerms[term.ID]; done {
				continue
			}
			if _, ok := classSet[term.OutputEqClassID]; !ok {
				continue
			}
			includedTerms[term.ID] = struct{}{}
			grew = true
			for _, input := range termInputsByTermID[term.ID] {
				if input.InputEqClassID == 0 {
					continue
				}
				classSet[input.InputEqClassID] = struct{}{}
			}
		}
		if !grew {
			break
		}
	}
	for _, term := range snapshot.terms {
		if _, ok := includedTerms[term.ID]; !ok {
			continue
		}
		bundled.terms = append(bundled.terms, term)
		bundled.termInputs = append(bundled.termInputs, termInputsByTermID[term.ID]...)
	}
	for _, class := range snapshot.eqClasses {
		if _, ok := classSet[class.ID]; !ok {
			continue
		}
		bundled.eqClasses = append(bundled.eqClasses, class)
	}
	for _, dig := range snapshot.eqClassDigests {
		if _, ok := classSet[dig.EqClassID]; !ok {
			continue
		}
		bundled.eqClassDigests = append(bundled.eqClassDigests, dig)
	}

	// Chains cross in the manifest, never in the metadata DB (the bundle's
	// result_content_chains table stays empty like every engine-local
	// table): collect them per result, dedup chain entries by chainID, and
	// index every referenced blob — then strip them from the rows about to
	// be written.
	var (
		manifestChains       []CacheBundleChain
		manifestResultChains []CacheBundleResultChain
		blobIndex            []string
		chainSeen            = make(map[string]struct{})
		blobSeen             = make(map[string]struct{})
		blobBytes            int64
	)
	for i := range bundled.results {
		row := &bundled.results[i]
		chains := slices.Clone(row.contentChains)
		slices.SortFunc(chains, func(a, b PersistedResultContentChain) int {
			return strings.Compare(a.Role, b.Role)
		})
		for _, chain := range chains {
			manifestResultChains = append(manifestResultChains, CacheBundleResultChain{
				ResultID: uint64(row.resultID),
				Role:     chain.Role,
				ChainID:  chain.ChainID,
			})
			if _, dup := chainSeen[chain.ChainID]; dup {
				continue
			}
			chainSeen[chain.ChainID] = struct{}{}
			bundleChain := CacheBundleChain{ChainID: chain.ChainID, Layers: []CacheBundleChainLayer{}}
			for _, layer := range chain.Layers {
				bundleChain.Layers = append(bundleChain.Layers, CacheBundleChainLayer{
					DiffID:    layer.DiffID,
					Blob:      layer.Blob,
					Size:      layer.Size,
					MediaType: layer.MediaType,
				})
				if _, dup := blobSeen[layer.Blob]; dup {
					continue
				}
				blobSeen[layer.Blob] = struct{}{}
				blobIndex = append(blobIndex, layer.Blob)
				blobBytes += layer.Size
			}
			manifestChains = append(manifestChains, bundleChain)
		}
		row.contentChains = nil
	}
	slices.Sort(blobIndex)

	// Write the metadata DB (the shared row-writing path local flush uses)
	// into a scratch file, then stream the archive.
	tmpDir, err := os.MkdirTemp("", "dagger-cache-bundle-export-*")
	if err != nil {
		return summary, fmt.Errorf("export bundle: scratch dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	metadataPath := filepath.Join(tmpDir, cacheBundleMetadataName)
	if err := writeBundleMetadataDB(ctx, metadataPath, bundled); err != nil {
		return summary, fmt.Errorf("export bundle: %w", err)
	}

	summary.Results = len(bundled.results)
	summary.Roots = len(bundled.persistedEdges)
	summary.EqClasses = len(bundled.eqClasses)
	summary.Terms = len(bundled.terms)
	summary.Chains = len(manifestChains)
	summary.Blobs = len(blobIndex)
	summary.BlobBytes = blobBytes
	summary.BlobIndex = blobIndex

	manifest := CacheBundleManifest{
		BundleFormat:  CacheBundleFormatVersion,
		SchemaVersion: cachePersistenceSchemaVersion,
		EngineVersion: opts.EngineVersion,
		StoreUUID:     c.storeUUID,
		Scope:         opts.Scope,
		CreatedAt:     time.Now().UTC(),
		Counts: CacheBundleCounts{
			Results:   len(bundled.results),
			Chains:    len(manifestChains),
			Blobs:     len(blobIndex),
			BlobBytes: blobBytes,
		},
		Chains:       manifestChains,
		ResultChains: manifestResultChains,
		BlobIndex:    blobIndex,
	}
	for _, edge := range bundled.persistedEdges {
		manifest.Roots = append(manifest.Roots, uint64(edge.ResultID))
	}

	if err := writeCacheBundleArchive(w, manifest, metadataPath); err != nil {
		return summary, fmt.Errorf("export bundle: %w", err)
	}
	return summary, nil
}

// computeExportChains derives a snapshot-backed row's content chains, one
// per snapshot role, through the snapshot manager's export machinery
// (memoized per snapshot; first computation is the sanctioned
// hash-at-export cost).
func (c *Cache) computeExportChains(ctx context.Context, links []persistdb.MirrorResultSnapshotLink) ([]PersistedResultContentChain, error) {
	var chains []PersistedResultContentChain
	seenRoles := make(map[string]struct{}, len(links))
	for _, link := range links {
		if _, dup := seenRoles[link.Role]; dup {
			continue
		}
		seenRoles[link.Role] = struct{}{}
		snapChain, err := c.snapshotManager.ChainForSnapshot(ctx, link.RefKey)
		if err != nil {
			return nil, fmt.Errorf("chain for role %q snapshot %q: %w", link.Role, link.RefKey, err)
		}
		chain := PersistedResultContentChain{
			Role:    link.Role,
			ChainID: snapChain.ChainID.String(),
		}
		for _, layer := range snapChain.Layers {
			chain.Layers = append(chain.Layers, PersistedContentChainLayer{
				DiffID:    layer.DiffID.String(),
				Blob:      layer.Blob.String(),
				Size:      layer.Size,
				MediaType: layer.MediaType,
			})
		}
		chains = append(chains, chain)
	}
	return chains, nil
}

func writeBundleMetadataDB(ctx context.Context, path string, bundled persistStateSnapshot) (rerr error) {
	db, q, err := prepareCacheDBs(ctx, path)
	if err != nil {
		return fmt.Errorf("create bundle metadata db: %w", err)
	}
	defer func() {
		if cerr := closeCacheDBs(db, q); cerr != nil && rerr == nil {
			rerr = fmt.Errorf("close bundle metadata db: %w", cerr)
		}
	}()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin bundle metadata tx: %w", err)
	}
	if err := insertPersistStateSnapshotRows(ctx, q.WithTx(tx), bundled); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit bundle metadata tx: %w", err)
	}
	// SQLite WAL sidecars must fold into the main file before it is
	// archived standalone.
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpoint bundle metadata db: %w", err)
	}
	return nil
}

func rowHasLazyFragment(row *persistResultSnapshot) bool {
	if row.lazyFragment != nil && len(row.lazyFragment.JSON) > 0 {
		return true
	}
	return row.persistedEnvelope != nil && len(row.persistedEnvelope.LazyJSON) > 0
}

func rowEnvelopeTypeName(row *persistResultSnapshot) string {
	if row.persistedEnvelope != nil && row.persistedEnvelope.TypeName != "" {
		return row.persistedEnvelope.TypeName
	}
	if row.self != nil && row.self.Type() != nil {
		return row.self.Type().Name()
	}
	return ""
}
