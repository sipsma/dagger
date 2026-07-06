package dagql

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	// ExcludedNoPortableContent counts rows whose only content source is a
	// local snapshot (no content chain yet, no lazy fragment, not a
	// sanctioned identity-only type): exporting them would promise content
	// the importer can never obtain. Their in-bundle dependents drop at
	// import by the missing-dep rule.
	ExcludedNoPortableContent int
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
	// that claims local snapshot content and has no cross-boundary way to
	// re-make it (no chain yet in this phase, no lazy fragment) is
	// excluded unless its type is sanctioned to cross identity-only.
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
		if len(row.snapshotOwnerLinks) > 0 && !rowHasLazyFragment(row) && !isContentlessPersistedType(rowEnvelopeTypeName(row)) {
			summary.ExcludedNoPortableContent++
			slog.Debug("cache bundle export excluding row without portable content",
				"sharedResultID", row.resultID, "type", rowEnvelopeTypeName(row))
			continue
		}
		if err := c.encodeSnapshotResultRow(ctx, row); err != nil {
			summary.ExcludedEncodeFailed++
			slog.Warn("cache bundle export excluding row that failed to encode",
				"sharedResultID", row.resultID, "err", err)
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

	manifest := CacheBundleManifest{
		BundleFormat:  CacheBundleFormatVersion,
		SchemaVersion: cachePersistenceSchemaVersion,
		EngineVersion: opts.EngineVersion,
		StoreUUID:     c.storeUUID,
		Scope:         opts.Scope,
		CreatedAt:     time.Now().UTC(),
		Counts: CacheBundleCounts{
			Results: len(bundled.results),
		},
	}
	for _, edge := range bundled.persistedEdges {
		manifest.Roots = append(manifest.Roots, uint64(edge.ResultID))
	}

	if err := writeCacheBundleArchive(w, manifest, metadataPath); err != nil {
		return summary, fmt.Errorf("export bundle: %w", err)
	}
	return summary, nil
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
