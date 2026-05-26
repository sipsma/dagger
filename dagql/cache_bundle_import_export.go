package dagql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"

	"github.com/dagger/dagger/dagql/call"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine/slog"
	bkcache "github.com/dagger/dagger/engine/snapshots"
)

type persistedStateRows struct {
	resultRows              []persistdb.MirrorResult
	eqClassRows             []persistdb.MirrorEqClass
	eqClassDigestRows       []persistdb.MirrorEqClassDigest
	termRows                []persistdb.MirrorTerm
	termInputRows           []persistdb.MirrorTermInput
	resultOutputEqClassRows []persistdb.MirrorResultOutputEqClass
	persistedEdgeRows       []persistdb.MirrorPersistedEdge
	resultDepRows           []persistdb.MirrorResultDep
	resultSnapshotRows      []persistdb.MirrorResultSnapshotLink
	snapshotContentRows     []persistdb.MirrorSnapshotContentLink
	importedLayerBlobRows   []persistdb.MirrorImportedLayerBlobIndex
	importedLayerDiffRows   []persistdb.MirrorImportedLayerDiffIndex
}

func (rows persistedStateRows) empty() bool {
	return len(rows.resultRows) == 0 && len(rows.eqClassRows) == 0 && len(rows.termRows) == 0
}

func loadPersistedStateRows(ctx context.Context, q *persistdb.Queries) (persistedStateRows, error) {
	var rows persistedStateRows
	var err error
	if rows.resultRows, err = q.ListMirrorResults(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror results: %w", err)
	}
	if rows.eqClassRows, err = q.ListMirrorEqClasses(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror eq_classes: %w", err)
	}
	if rows.eqClassDigestRows, err = q.ListMirrorEqClassDigests(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror eq_class_digests: %w", err)
	}
	if rows.termRows, err = q.ListMirrorTerms(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror terms: %w", err)
	}
	if rows.termInputRows, err = q.ListMirrorTermInputs(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror term_inputs: %w", err)
	}
	if rows.resultOutputEqClassRows, err = q.ListMirrorResultOutputEqClasses(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror result_output_eq_classes: %w", err)
	}
	if rows.persistedEdgeRows, err = q.ListMirrorPersistedEdges(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror persisted_edges: %w", err)
	}
	if rows.resultDepRows, err = q.ListMirrorResultDeps(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror result_deps: %w", err)
	}
	if rows.resultSnapshotRows, err = q.ListMirrorResultSnapshotLinks(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror result_snapshot_links: %w", err)
	}
	if rows.snapshotContentRows, err = q.ListMirrorSnapshotContentLinks(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror snapshot_content_links: %w", err)
	}
	if rows.importedLayerBlobRows, err = q.ListMirrorImportedLayerBlobIndex(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror imported_layer_blob_index: %w", err)
	}
	if rows.importedLayerDiffRows, err = q.ListMirrorImportedLayerDiffIndex(ctx); err != nil {
		return persistedStateRows{}, fmt.Errorf("list mirror imported_layer_diff_index: %w", err)
	}
	return rows, nil
}

func (c *Cache) importCacheBundles(ctx context.Context, importDirs []string) {
	for _, dir := range importDirs {
		if dir == "" {
			slog.Warn("skipping empty cache import dir")
			continue
		}
		bundle, err := bkcache.OpenCacheBundle(dir)
		if err != nil {
			slog.Warn("skipping invalid cache import bundle", "dir", dir, "err", err)
			continue
		}
		if _, exists := c.importSources[bundle.ID]; exists {
			slog.Warn("skipping duplicate cache import bundle source", "dir", dir, "source", bundle.ID)
			continue
		}
		source := &PersistedCacheSource{
			ID:     bundle.ID,
			Dir:    dir,
			Bundle: bundle,
		}
		if err := c.importCacheBundle(ctx, source); err != nil {
			slog.Warn("skipping cache import bundle after metadata import failure", "dir", dir, "source", bundle.ID, "err", err)
			continue
		}
		c.importSources[bundle.ID] = source
	}
}

func (c *Cache) importCacheBundle(ctx context.Context, source *PersistedCacheSource) error {
	if source == nil || source.Bundle == nil {
		return errors.New("import cache bundle: nil source")
	}
	db, q, err := openCacheDBReadOnly(ctx, source.Bundle.MetadataDBPath())
	if err != nil {
		return err
	}
	defer closeCacheDBs(db, q)

	schemaVersion, found, err := q.SelectMetaValue(ctx, persistdb.MetaKeySchemaVersion)
	if err != nil {
		return fmt.Errorf("read bundle schema_version metadata: %w", err)
	}
	if !found {
		return errors.New("bundle metadata missing schema_version")
	}
	if schemaVersion != cachePersistenceSchemaVersion {
		return fmt.Errorf("unsupported bundle schema_version %q", schemaVersion)
	}
	rows, err := loadPersistedStateRows(ctx, q)
	if err != nil {
		return err
	}
	return c.importCacheBundleRows(ctx, source, rows)
}

//nolint:gocyclo // importing the normalized persistence graph is intentionally explicit
func (c *Cache) importCacheBundleRows(ctx context.Context, source *PersistedCacheSource, rows persistedStateRows) error {
	if rows.empty() {
		return nil
	}
	importRunID := c.nextImportRunID()

	sourceResultToLocal := make(map[uint64]uint64, len(rows.resultRows))
	sourceEqToLocal := make(map[int64]eqClassID, len(rows.eqClassRows))
	eagerDecodeResultIDs := make([]sharedResultID, 0, len(rows.resultRows))
	importedResultIDs := make([]sharedResultID, 0, len(rows.resultRows))

	c.egraphMu.Lock()
	importErr := func() error {
		c.initEgraphLocked()
		if c.resultIDMap == nil {
			c.resultIDMap = make(map[string]map[uint64]uint64)
		}
		if c.resultIDMap[source.ID] == nil {
			c.resultIDMap[source.ID] = make(map[uint64]uint64, len(rows.resultRows))
		}

		resultRows := slices.Clone(rows.resultRows)
		slices.SortFunc(resultRows, func(a, b persistdb.MirrorResult) int {
			return cmpInt64(a.ID, b.ID)
		})
		for _, row := range resultRows {
			if row.ID == 0 {
				return errors.New("import bundle result: zero ID")
			}
			localID := c.nextSharedResultID
			c.nextSharedResultID++
			sourceResultToLocal[uint64(row.ID)] = uint64(localID)
			c.resultIDMap[source.ID][uint64(row.ID)] = uint64(localID)
			importedResultIDs = append(importedResultIDs, localID)
		}

		digestsByEq := make(map[int64][]persistdb.MirrorEqClassDigest)
		for _, row := range rows.eqClassDigestRows {
			digestsByEq[row.EqClassID] = append(digestsByEq[row.EqClassID], row)
		}
		eqClassRows := slices.Clone(rows.eqClassRows)
		slices.SortFunc(eqClassRows, func(a, b persistdb.MirrorEqClass) int {
			return cmpInt64(a.ID, b.ID)
		})
		for _, row := range eqClassRows {
			if row.ID == 0 {
				return errors.New("import bundle eq_class: zero ID")
			}
			localEqID := eqClassID(0)
			digestRows := digestsByEq[row.ID]
			slices.SortFunc(digestRows, func(a, b persistdb.MirrorEqClassDigest) int {
				if a.Digest != b.Digest {
					return cmpString(a.Digest, b.Digest)
				}
				return cmpString(a.Label, b.Label)
			})
			for _, digestRow := range digestRows {
				if digestRow.Digest == "" {
					return fmt.Errorf("import bundle eq_class_digest: empty digest for eq_class %d", row.ID)
				}
				localEqID = c.mergeImportedEqClassDigestLocked(ctx, localEqID, digestRow)
			}
			if localEqID == 0 {
				localEqID = c.createEmptyImportedEqClassLocked(ctx)
			}
			sourceEqToLocal[row.ID] = c.findEqClassLocked(localEqID)
		}

		for _, row := range resultRows {
			resultID := sharedResultID(sourceResultToLocal[uint64(row.ID)])
			var env PersistedResultEnvelope
			if len(row.SelfPayload) > 0 {
				if err := json.Unmarshal(row.SelfPayload, &env); err != nil {
					return fmt.Errorf("import bundle result %d self payload: %w", row.ID, err)
				}
			} else {
				env = PersistedResultEnvelope{
					Version: 1,
					Kind:    persistedResultKindNull,
				}
			}
			if env.Kind == "" {
				return fmt.Errorf("import bundle result %d: empty self payload kind", row.ID)
			}
			if row.CallFrameJSON == "" {
				return fmt.Errorf("import bundle result %d: empty call_frame_json", row.ID)
			}
			frame := &ResultCall{}
			if err := json.Unmarshal([]byte(row.CallFrameJSON), frame); err != nil {
				return fmt.Errorf("import bundle result %d call_frame_json: %w", row.ID, err)
			}
			if err := remapResultCallRefs(frame, sourceResultToLocal); err != nil {
				return fmt.Errorf("import bundle result %d call_frame_json refs: %w", row.ID, err)
			}

			originSourceID := row.OriginSourceID
			originResultID := uint64(row.OriginResultID)
			if originSourceID == "" || originResultID == 0 {
				originSourceID = source.ID
				originResultID = uint64(row.ID)
			}
			if c.resultIDMap[originSourceID] == nil {
				c.resultIDMap[originSourceID] = make(map[uint64]uint64)
			}
			c.resultIDMap[originSourceID][originResultID] = uint64(resultID)

			res := &sharedResult{
				id:                    resultID,
				isObject:              env.Kind == persistedResultKindObject,
				sessionResourceHandle: env.SessionResourceHandle,
				expiresAtUnix:         row.ExpiresAtUnix,
				createdAtUnixNano:     row.CreatedAtUnixNano,
				lastUsedAtUnixNano:    row.LastUsedAtUnixNano,
				description:           row.Description,
				recordType:            row.RecordType,
				persistedEnvelope:     &env,
				importSourceID:        originSourceID,
				sourceResultID:        originResultID,
			}
			res.storeResultCall(frame)
			c.traceResultCallFrameUpdated(ctx, res, "import_cache_bundle_result", nil, frame)
			if env.Kind == persistedResultKindNull {
				res.hasValue = true
				res.persistedEnvelope = nil
				c.tracePersistedPayloadImportedEager(ctx, importRunID, resultID, source.ID, "nil")
			} else {
				eagerDecodeResultIDs = append(eagerDecodeResultIDs, resultID)
			}
			c.resultsByID[resultID] = res
			c.traceImportResultLoaded(ctx, importRunID, resultID, row.CallFrameJSON)
		}

		inputsByTermID := make(map[int64][]persistdb.MirrorTermInput, len(rows.termInputRows))
		for _, row := range rows.termInputRows {
			inputsByTermID[row.TermID] = append(inputsByTermID[row.TermID], row)
		}
		termRows := slices.Clone(rows.termRows)
		slices.SortFunc(termRows, func(a, b persistdb.MirrorTerm) int {
			return cmpInt64(a.ID, b.ID)
		})
		for _, row := range termRows {
			if row.ID == 0 {
				return errors.New("import bundle term: zero ID")
			}
			inputs := inputsByTermID[row.ID]
			slices.SortFunc(inputs, func(a, b persistdb.MirrorTermInput) int {
				return cmpInt64(a.Position, b.Position)
			})
			inputEqIDs := make([]eqClassID, 0, len(inputs))
			inputProvenance := make([]egraphInputProvenanceKind, 0, len(inputs))
			for idx, input := range inputs {
				if input.Position != int64(idx) {
					return fmt.Errorf("import bundle term %d inputs: missing position %d", row.ID, idx)
				}
				inputEqID, ok := sourceEqToLocal[input.InputEqClassID]
				if !ok {
					return fmt.Errorf("import bundle term %d input %d: missing eq_class %d", row.ID, idx, input.InputEqClassID)
				}
				provenance := egraphInputProvenanceKind(input.ProvenanceKind)
				switch provenance {
				case egraphInputProvenanceKindResult, egraphInputProvenanceKindDigest:
				default:
					return fmt.Errorf("import bundle term %d input %d: unsupported provenance %q", row.ID, idx, input.ProvenanceKind)
				}
				inputEqIDs = append(inputEqIDs, c.findEqClassLocked(inputEqID))
				inputProvenance = append(inputProvenance, provenance)
			}
			outputEqID, ok := sourceEqToLocal[row.OutputEqClassID]
			if !ok {
				return fmt.Errorf("import bundle term %d: missing output eq_class %d", row.ID, row.OutputEqClassID)
			}
			selfDigest := normalizeImportedDigest(row.SelfDigest)
			if selfDigest == "" {
				return fmt.Errorf("import bundle term %d: empty self digest", row.ID)
			}
			termDigest := calcEgraphTermDigest(selfDigest, inputEqIDs)
			mergedOutputEqID := c.mergeOutputsForTermDigestLocked(ctx, termDigest, outputEqID)
			termID := c.nextEgraphTermID
			c.nextEgraphTermID++
			term := newEgraphTerm(termID, selfDigest, inputEqIDs, mergedOutputEqID)
			c.egraphTerms[termID] = term
			c.termInputProvenance[termID] = inputProvenance
			digestTerms := c.egraphTermsByTermDigest[term.termDigest]
			if digestTerms == nil {
				digestTerms = newEgraphTermIDSet()
				c.egraphTermsByTermDigest[term.termDigest] = digestTerms
			}
			digestTerms.Insert(termID)
			for _, inEqID := range term.inputEqIDs {
				if inEqID == 0 {
					continue
				}
				classTerms := c.inputEqClassToTerms[inEqID]
				if classTerms == nil {
					classTerms = make(map[egraphTermID]struct{})
					c.inputEqClassToTerms[inEqID] = classTerms
				}
				classTerms[termID] = struct{}{}
			}
			outputTerms := c.outputEqClassToTerms[mergedOutputEqID]
			if outputTerms == nil {
				outputTerms = make(map[egraphTermID]struct{})
				c.outputEqClassToTerms[mergedOutputEqID] = outputTerms
			}
			outputTerms[termID] = struct{}{}
			c.traceTermCreated(ctx, "import_bundle", importRunID, term)
		}

		for _, row := range rows.resultOutputEqClassRows {
			resultID, ok := sourceResultToLocal[uint64(row.ResultID)]
			if !ok {
				return fmt.Errorf("import bundle result_output_eq_class: missing result %d", row.ResultID)
			}
			outputEqID, ok := sourceEqToLocal[row.EqClassID]
			if !ok {
				return fmt.Errorf("import bundle result_output_eq_class: missing eq_class %d", row.EqClassID)
			}
			outputEqID = c.findEqClassLocked(outputEqID)
			outputEqClasses := c.resultOutputEqClasses[sharedResultID(resultID)]
			if outputEqClasses == nil {
				outputEqClasses = make(map[eqClassID]struct{})
				c.resultOutputEqClasses[sharedResultID(resultID)] = outputEqClasses
			}
			outputEqClasses[outputEqID] = struct{}{}
		}

		for _, row := range rows.persistedEdgeRows {
			resultID, ok := sourceResultToLocal[uint64(row.ResultID)]
			if !ok {
				return fmt.Errorf("import bundle persisted_edge: missing result %d", row.ResultID)
			}
			res := c.resultsByID[sharedResultID(resultID)]
			if res == nil {
				return fmt.Errorf("import bundle persisted_edge: missing local result %d", resultID)
			}
			if c.persistedEdgesByResult == nil {
				c.persistedEdgesByResult = make(map[sharedResultID]persistedEdge)
			}
			edge := persistedEdge{
				resultID:          sharedResultID(resultID),
				createdAtUnixNano: row.CreatedAtUnixNano,
				expiresAtUnix:     row.ExpiresAtUnix,
				unpruneable:       row.Unpruneable,
			}
			if edge.unpruneable {
				edge.expiresAtUnix = 0
				res.expiresAtUnix = 0
			}
			c.persistedEdgesByResult[sharedResultID(resultID)] = edge
			c.incrementIncomingOwnershipLocked(ctx, res)
		}

		for _, row := range rows.resultDepRows {
			parentID, ok := sourceResultToLocal[uint64(row.ParentResultID)]
			if !ok {
				return fmt.Errorf("import bundle result_dep: missing parent result %d", row.ParentResultID)
			}
			depID, ok := sourceResultToLocal[uint64(row.DepResultID)]
			if !ok {
				return fmt.Errorf("import bundle result_dep: missing dep result %d", row.DepResultID)
			}
			parent := c.resultsByID[sharedResultID(parentID)]
			dep := c.resultsByID[sharedResultID(depID)]
			if parent == nil || dep == nil {
				return fmt.Errorf("import bundle result_dep: missing local result %d -> %d", parentID, depID)
			}
			if parent.deps == nil {
				parent.deps = make(map[sharedResultID]struct{})
			}
			parent.deps[sharedResultID(depID)] = struct{}{}
			c.rememberDependencyEdgeLocked(parent, dep)
			c.incrementIncomingOwnershipLocked(ctx, dep)
			c.traceImportResultDepLoaded(ctx, importRunID, sharedResultID(parentID), sharedResultID(depID))
			c.traceExplicitDepAdded(ctx, sharedResultID(parentID), sharedResultID(depID), "import_bundle")
		}

		for _, row := range rows.resultSnapshotRows {
			resultID, ok := sourceResultToLocal[uint64(row.ResultID)]
			if !ok {
				return fmt.Errorf("import bundle result_snapshot_link: missing result %d", row.ResultID)
			}
			res := c.resultsByID[sharedResultID(resultID)]
			if res == nil {
				return fmt.Errorf("import bundle result_snapshot_link: missing local result %d", resultID)
			}
			res.payloadMu.Lock()
			res.snapshotOwnerLinks = append(res.snapshotOwnerLinks, PersistedSnapshotRefLink{
				RefKey:   row.RefKey,
				Role:     row.Role,
				SourceID: source.ID,
			})
			res.payloadMu.Unlock()
			c.traceImportResultSnapshotLinkLoaded(ctx, importRunID, sharedResultID(resultID), row.RefKey, row.Role)
		}

		for _, resultID := range importedResultIDs {
			res := c.resultsByID[resultID]
			if res != nil {
				res.onRelease = joinOnRelease(c.resultSnapshotLeaseCleanup(res), res.onRelease)
			}
			if err := c.recomputeRequiredSessionResourcesLocked(res); err != nil {
				return fmt.Errorf("recompute imported bundle required session resources for result %d: %w", resultID, err)
			}
			for outputEqID := range c.outputEqClassesForResultLocked(resultID) {
				for dig := range c.eqClassToDigests[outputEqID] {
					set := c.egraphResultsByDigest[dig]
					if set == nil {
						set = newSharedResultIDSet()
						c.egraphResultsByDigest[dig] = set
					}
					set.Insert(resultID)
				}
			}
		}

		return nil
	}()
	c.egraphMu.Unlock()
	if importErr != nil {
		return importErr
	}

	for _, resultID := range eagerDecodeResultIDs {
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		state := res.loadPayloadState()
		if state.hasValue || state.persistedEnvelope == nil {
			continue
		}
		call := res.loadResultCall()
		if call == nil {
			continue
		}
		decodeCtx := ContextWithCall(ctx, call)
		if decoded, err := DefaultPersistedSelfCodec.DecodeResult(decodeCtx, nil, uint64(resultID), call, *state.persistedEnvelope); err == nil && decoded != nil {
			res.payloadMu.Lock()
			if !res.hasValue && res.persistedEnvelope != nil {
				res.self = decoded.Unwrap()
				res.hasValue = true
				decodedShared := decoded.cacheSharedResult()
				if decodedShared != nil {
					res.sessionResourceHandle = decodedShared.sessionResourceHandle
					if decodedShared.requiredSessionResources != nil {
						res.requiredSessionResources = decodedShared.requiredSessionResources.Copy()
					} else if decodedShared.sessionResourceHandle == "" {
						res.requiredSessionResources = nil
					}
				}
				res.persistedEnvelope = nil
			}
			res.payloadMu.Unlock()
			if onReleaser, ok := UnwrapAs[OnReleaser](decoded); ok {
				res.onRelease = joinOnRelease(c.resultSnapshotLeaseCleanup(res), onReleaser.OnRelease)
			}
			if err := c.syncResultSnapshotLeases(ctx, res); err != nil {
				return err
			}
			c.tracePersistedPayloadImportedEager(ctx, importRunID, resultID, source.ID, "materialized")
		}
	}
	for _, resultID := range eagerDecodeResultIDs {
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		state := res.loadPayloadState()
		if state.persistedEnvelope == nil || state.hasValue {
			continue
		}
		c.tracePersistedPayloadImportedLazy(ctx, importRunID, resultID, source.ID, state.persistedEnvelope.Kind, state.persistedEnvelope.TypeName)
	}

	return nil
}

func (c *Cache) exportCurrentState(ctx context.Context) error {
	if c.exportDir == "" {
		return nil
	}
	snapshot, err := c.snapshotPersistState(ctx)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(c.exportDir); err != nil {
		return fmt.Errorf("remove existing cache export dir: %w", err)
	}
	writer, err := bkcache.CreateCacheBundle(c.exportDir)
	if err != nil {
		return err
	}
	if err := c.populateCacheBundleSnapshots(ctx, writer, &snapshot); err != nil {
		return err
	}
	return writeCacheBundleDB(ctx, writer.MetadataDBPath(), snapshot)
}

func (c *Cache) populateCacheBundleSnapshots(ctx context.Context, writer *bkcache.CacheBundleWriter, snapshot *persistStateSnapshot) error {
	if snapshot == nil {
		return nil
	}
	type exportKey struct {
		sourceID string
		refKey   string
	}
	exported := make(map[exportKey]struct{})
	for resultIdx := range snapshot.results {
		result := &snapshot.results[resultIdx]
		for linkIdx := range result.resultSnapshotLinks {
			link := &result.resultSnapshotLinks[linkIdx]
			key := exportKey{sourceID: link.SourceID, refKey: link.RefKey}
			if _, ok := exported[key]; !ok {
				if err := c.addCacheBundleSnapshot(ctx, writer, key.sourceID, key.refKey); err != nil {
					return err
				}
				exported[key] = struct{}{}
			}
			link.SourceID = ""
		}
	}
	return nil
}

func (c *Cache) addCacheBundleSnapshot(ctx context.Context, writer *bkcache.CacheBundleWriter, sourceID string, refKey string) error {
	if refKey == "" {
		return errors.New("export cache bundle snapshot: empty ref key")
	}
	if sourceID == "" {
		if c.snapshotManager == nil {
			return errors.New("export cache bundle snapshot: nil snapshot manager")
		}
		ref, err := c.snapshotManager.GetBySnapshotID(ctx, refKey, bkcache.NoUpdateLastUsed)
		if err != nil {
			return fmt.Errorf("export local snapshot %q: %w", refKey, err)
		}
		defer ref.Release(context.WithoutCancel(ctx))
		if _, err := writer.AddSnapshot(ctx, ref); err != nil {
			return err
		}
		return nil
	}
	c.egraphMu.RLock()
	source := c.importSources[sourceID]
	c.egraphMu.RUnlock()
	if source == nil || source.Bundle == nil {
		return fmt.Errorf("export external snapshot %q: unknown cache import source %q", refKey, sourceID)
	}
	_, err := writer.AddSnapshotFromBundle(ctx, source.Bundle, refKey)
	return err
}

func writeCacheBundleDB(ctx context.Context, dbPath string, snapshot persistStateSnapshot) error {
	db, q, err := prepareCacheDBs(ctx, dbPath)
	if err != nil {
		return err
	}
	defer closeCacheDBs(db, q)
	if err := applyPersistStateSnapshot(ctx, db, q, snapshot); err != nil {
		return err
	}
	if err := q.UpsertMeta(ctx, persistdb.MetaKeySchemaVersion, cachePersistenceSchemaVersion); err != nil {
		return fmt.Errorf("set cache bundle schema version: %w", err)
	}
	if err := q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"); err != nil {
		return fmt.Errorf("set cache bundle clean shutdown: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpoint cache bundle db: %w", err)
	}
	return nil
}

func openCacheDBReadOnly(ctx context.Context, dbPath string) (*sql.DB, *persistdb.Queries, error) {
	connURL := &url.URL{
		Scheme: "file",
		Path:   dbPath,
		RawQuery: url.Values{
			"mode":    []string{"ro"},
			"_pragma": []string{"busy_timeout=10000"},
			"_txlock": []string{"deferred"},
		}.Encode(),
	}
	db, err := sql.Open("sqlite", connURL.String())
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", connURL, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("ping %s: %w", connURL, err)
	}
	q, err := persistdb.Prepare(ctx, db)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("prepare persistence queries: %w", err)
	}
	return db, q, nil
}

func (c *Cache) createEmptyImportedEqClassLocked(ctx context.Context) eqClassID {
	id := c.nextEgraphClassID
	c.nextEgraphClassID++
	c.egraphParents = append(c.egraphParents, id)
	c.egraphRanks = append(c.egraphRanks, 0)
	if c.eqClassToDigests[id] == nil {
		c.eqClassToDigests[id] = make(map[string]struct{})
	}
	c.traceEqClassCreated(ctx, id, "")
	return id
}

func (c *Cache) mergeImportedEqClassDigestLocked(ctx context.Context, eqID eqClassID, row persistdb.MirrorEqClassDigest) eqClassID {
	dig := normalizeImportedDigest(row.Digest)
	digEq := c.ensureEqClassForDigestLocked(ctx, dig.String())
	if eqID == 0 {
		eqID = digEq
	} else {
		eqID = c.mergeEqClassesLocked(ctx, eqID, digEq)
	}
	root := c.findEqClassLocked(eqID)
	if row.Label != "" {
		extras := c.eqClassExtraDigests[root]
		if extras == nil {
			extras = make(map[call.ExtraDigest]struct{})
			c.eqClassExtraDigests[root] = extras
		}
		extras[call.ExtraDigest{
			Digest: dig,
			Label:  row.Label,
		}] = struct{}{}
	}
	return root
}

func remapResultCallRefs(frame *ResultCall, resultIDMap map[uint64]uint64) error {
	if frame == nil {
		return nil
	}
	if err := remapResultCallRef(frame.Receiver, resultIDMap); err != nil {
		return fmt.Errorf("receiver: %w", err)
	}
	if frame.Module != nil {
		if err := remapResultCallRef(frame.Module.ResultRef, resultIDMap); err != nil {
			return fmt.Errorf("module: %w", err)
		}
	}
	for _, arg := range frame.Args {
		argName := ""
		if arg != nil {
			argName = arg.Name
		}
		if err := remapResultCallArg(arg, resultIDMap); err != nil {
			return fmt.Errorf("arg %q: %w", argName, err)
		}
	}
	for _, arg := range frame.ImplicitInputs {
		argName := ""
		if arg != nil {
			argName = arg.Name
		}
		if err := remapResultCallArg(arg, resultIDMap); err != nil {
			return fmt.Errorf("implicit input %q: %w", argName, err)
		}
	}
	return nil
}

func remapResultCallArg(arg *ResultCallArg, resultIDMap map[uint64]uint64) error {
	if arg == nil {
		return nil
	}
	return remapResultCallLiteral(arg.Value, resultIDMap)
}

func remapResultCallLiteral(lit *ResultCallLiteral, resultIDMap map[uint64]uint64) error {
	if lit == nil {
		return nil
	}
	if err := remapResultCallRef(lit.ResultRef, resultIDMap); err != nil {
		return err
	}
	for _, item := range lit.ListItems {
		if err := remapResultCallLiteral(item, resultIDMap); err != nil {
			return err
		}
	}
	for _, field := range lit.ObjectFields {
		if err := remapResultCallArg(field, resultIDMap); err != nil {
			return err
		}
	}
	return nil
}

func remapResultCallRef(ref *ResultCallRef, resultIDMap map[uint64]uint64) error {
	if ref == nil {
		return nil
	}
	if ref.ResultID != 0 {
		localID, ok := resultIDMap[ref.ResultID]
		if !ok {
			return fmt.Errorf("missing result ID mapping for %d", ref.ResultID)
		}
		ref.ResultID = localID
	}
	ref.shared = nil
	return remapResultCallRefs(ref.Call, resultIDMap)
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
