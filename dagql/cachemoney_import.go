package dagql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/dagger/dagger/dagql/cachemoneyproto"
	"github.com/dagger/dagger/dagql/call"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
)

type CachemoneyImportSource struct {
	ID             string
	MetadataDBPath string
	BlobIndex      map[string]cachemoneyproto.BlobLocation
}

type cachemoneyPersistedStateRows struct {
	resultRows              []persistdb.MirrorResult
	eqClassRows             []persistdb.MirrorEqClass
	eqClassDigestRows       []persistdb.MirrorEqClassDigest
	termRows                []persistdb.MirrorTerm
	termInputRows           []persistdb.MirrorTermInput
	resultOutputEqClassRows []persistdb.MirrorResultOutputEqClass
	persistedEdgeRows       []persistdb.MirrorPersistedEdge
	resultDepRows           []persistdb.MirrorResultDep
	resultSnapshotChainRows []persistdb.MirrorResultSnapshotChain
	snapshotChainLayerRows  []persistdb.MirrorSnapshotChainLayer
}

func (rows cachemoneyPersistedStateRows) empty() bool {
	return len(rows.resultRows) == 0 && len(rows.eqClassRows) == 0 && len(rows.termRows) == 0
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

func loadCachemoneyPersistedStateRows(ctx context.Context, q *persistdb.Queries) (cachemoneyPersistedStateRows, error) {
	var rows cachemoneyPersistedStateRows
	var err error
	if rows.resultRows, err = q.ListMirrorResults(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror results: %w", err)
	}
	if rows.eqClassRows, err = q.ListMirrorEqClasses(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror eq_classes: %w", err)
	}
	if rows.eqClassDigestRows, err = q.ListMirrorEqClassDigests(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror eq_class_digests: %w", err)
	}
	if rows.termRows, err = q.ListMirrorTerms(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror terms: %w", err)
	}
	if rows.termInputRows, err = q.ListMirrorTermInputs(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror term_inputs: %w", err)
	}
	if rows.resultOutputEqClassRows, err = q.ListMirrorResultOutputEqClasses(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror result_output_eq_classes: %w", err)
	}
	if rows.persistedEdgeRows, err = q.ListMirrorPersistedEdges(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror persisted_edges: %w", err)
	}
	if rows.resultDepRows, err = q.ListMirrorResultDeps(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror result_deps: %w", err)
	}
	if rows.resultSnapshotChainRows, err = q.ListMirrorResultSnapshotChains(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror result_snapshot_chains: %w", err)
	}
	if rows.snapshotChainLayerRows, err = q.ListMirrorSnapshotChainLayers(ctx); err != nil {
		return cachemoneyPersistedStateRows{}, fmt.Errorf("list mirror snapshot_chain_layers: %w", err)
	}
	return rows, nil
}

func (c *Cache) ImportCachemoneyMetadata(ctx context.Context, source CachemoneyImportSource) error {
	if source.ID == "" {
		return errors.New("import cachemoney metadata: empty source ID")
	}
	if source.MetadataDBPath == "" {
		return errors.New("import cachemoney metadata: empty metadata DB path")
	}

	db, q, err := openCacheDBReadOnly(ctx, source.MetadataDBPath)
	if err != nil {
		return err
	}
	defer closeCacheDBs(db, q) //nolint:errcheck

	schemaVersion, found, err := q.SelectMetaValue(ctx, persistdb.MetaKeySchemaVersion)
	if err != nil {
		return fmt.Errorf("read cachemoney metadata schema_version: %w", err)
	}
	if !found {
		return errors.New("cachemoney metadata missing schema_version")
	}
	if schemaVersion != cachePersistenceSchemaVersion {
		return fmt.Errorf("unsupported cachemoney metadata schema_version %q", schemaVersion)
	}

	rows, err := loadCachemoneyPersistedStateRows(ctx, q)
	if err != nil {
		return err
	}
	return c.importCachemoneyMetadataRows(ctx, source, rows)
}

//nolint:gocyclo // importing the normalized persistence graph is intentionally explicit
func (c *Cache) importCachemoneyMetadataRows(ctx context.Context, source CachemoneyImportSource, rows cachemoneyPersistedStateRows) error {
	if rows.empty() {
		return nil
	}
	importRunID := c.nextImportRunID()
	blobAvailability := c.cachemoneyBlobAvailability(ctx, source, rows.snapshotChainLayerRows)

	sourceResultToLocal := make(map[uint64]uint64, len(rows.resultRows))
	sourceEqToLocal := make(map[int64]eqClassID, len(rows.eqClassRows))
	importedResultIDs := make([]sharedResultID, 0, len(rows.resultRows))

	layersByChain, err := cachemoneySnapshotChainLayersByID(rows.snapshotChainLayerRows)
	if err != nil {
		return err
	}

	c.egraphMu.Lock()
	importErr := func() error {
		c.initEgraphLocked()

		resultRows := slices.Clone(rows.resultRows)
		slices.SortFunc(resultRows, func(a, b persistdb.MirrorResult) int {
			return cmpInt64(a.ID, b.ID)
		})
		for _, row := range resultRows {
			if row.ID == 0 {
				return errors.New("import cachemoney metadata result: zero ID")
			}
			localID := c.nextSharedResultID
			c.nextSharedResultID++
			sourceResultToLocal[uint64(row.ID)] = uint64(localID)
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
				return errors.New("import cachemoney metadata eq_class: zero ID")
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
					return fmt.Errorf("import cachemoney metadata eq_class_digest: empty digest for eq_class %d", row.ID)
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
			env := PersistedResultEnvelope{
				Version: 1,
				Kind:    persistedResultKindNull,
			}
			if len(row.SelfPayload) > 0 {
				if err := json.Unmarshal(row.SelfPayload, &env); err != nil {
					return fmt.Errorf("import cachemoney metadata result %d self payload: %w", row.ID, err)
				}
			}
			if env.Kind == "" {
				return fmt.Errorf("import cachemoney metadata result %d: empty self payload kind", row.ID)
			}
			if err := remapPersistedResultEnvelope(&env, sourceResultToLocal); err != nil {
				return fmt.Errorf("import cachemoney metadata result %d self payload refs: %w", row.ID, err)
			}
			if row.CallFrameJSON == "" {
				return fmt.Errorf("import cachemoney metadata result %d: empty call_frame_json", row.ID)
			}
			frame := &ResultCall{}
			if err := json.Unmarshal([]byte(row.CallFrameJSON), frame); err != nil {
				return fmt.Errorf("import cachemoney metadata result %d call_frame_json: %w", row.ID, err)
			}
			if err := remapResultCallRefs(frame, sourceResultToLocal); err != nil {
				return fmt.Errorf("import cachemoney metadata result %d call_frame_json refs: %w", row.ID, err)
			}

			originSourceID := row.OriginSourceID
			originResultID := uint64(row.OriginResultID)
			if originSourceID == "" || originResultID == 0 {
				originSourceID = source.ID
				originResultID = uint64(row.ID)
			}

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
				originSourceID:        originSourceID,
				originResultID:        originResultID,
				remoteCacheImported:   true,
				remoteCacheViable:     false,
				remoteCacheEligible:   false,
				remoteCacheReason:     remoteCacheReasonPendingViability,
			}
			res.storeResultCall(frame)
			c.traceResultCallFrameUpdated(ctx, res, "import_cachemoney_metadata_result", nil, frame)
			if env.Kind == persistedResultKindNull {
				res.hasValue = true
				res.persistedEnvelope = nil
				c.tracePersistedPayloadImportedEager(ctx, importRunID, resultID, source.ID, "nil")
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
				return errors.New("import cachemoney metadata term: zero ID")
			}
			inputs := inputsByTermID[row.ID]
			slices.SortFunc(inputs, func(a, b persistdb.MirrorTermInput) int {
				return cmpInt64(a.Position, b.Position)
			})
			inputEqIDs := make([]eqClassID, 0, len(inputs))
			inputProvenance := make([]egraphInputProvenanceKind, 0, len(inputs))
			for idx, input := range inputs {
				if input.Position != int64(idx) {
					return fmt.Errorf("import cachemoney metadata term %d inputs: missing position %d", row.ID, idx)
				}
				inputEqID, ok := sourceEqToLocal[input.InputEqClassID]
				if !ok {
					return fmt.Errorf("import cachemoney metadata term %d input %d: missing eq_class %d", row.ID, idx, input.InputEqClassID)
				}
				provenance := egraphInputProvenanceKind(input.ProvenanceKind)
				switch provenance {
				case egraphInputProvenanceKindResult, egraphInputProvenanceKindDigest:
				default:
					return fmt.Errorf("import cachemoney metadata term %d input %d: unsupported provenance %q", row.ID, idx, input.ProvenanceKind)
				}
				inputEqIDs = append(inputEqIDs, c.findEqClassLocked(inputEqID))
				inputProvenance = append(inputProvenance, provenance)
			}
			outputEqID, ok := sourceEqToLocal[row.OutputEqClassID]
			if !ok {
				return fmt.Errorf("import cachemoney metadata term %d: missing output eq_class %d", row.ID, row.OutputEqClassID)
			}
			selfDigest := normalizeImportedDigest(row.SelfDigest)
			if selfDigest == "" {
				return fmt.Errorf("import cachemoney metadata term %d: empty self digest", row.ID)
			}
			// Term digests include local canonical eq-class IDs, so the source DB's
			// term_digest is not portable after remote import remaps/merges classes.
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
			c.traceTermCreated(ctx, "import_cachemoney_metadata", importRunID, term)
		}

		for _, row := range rows.resultOutputEqClassRows {
			resultID, ok := sourceResultToLocal[uint64(row.ResultID)]
			if !ok {
				return fmt.Errorf("import cachemoney metadata result_output_eq_class: missing result %d", row.ResultID)
			}
			outputEqID, ok := sourceEqToLocal[row.EqClassID]
			if !ok {
				return fmt.Errorf("import cachemoney metadata result_output_eq_class: missing eq_class %d", row.EqClassID)
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
				return fmt.Errorf("import cachemoney metadata persisted_edge: missing result %d", row.ResultID)
			}
			res := c.resultsByID[sharedResultID(resultID)]
			if res == nil {
				return fmt.Errorf("import cachemoney metadata persisted_edge: missing local result %d", resultID)
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
				return fmt.Errorf("import cachemoney metadata result_dep: missing parent result %d", row.ParentResultID)
			}
			depID, ok := sourceResultToLocal[uint64(row.DepResultID)]
			if !ok {
				return fmt.Errorf("import cachemoney metadata result_dep: missing dep result %d", row.DepResultID)
			}
			parent := c.resultsByID[sharedResultID(parentID)]
			dep := c.resultsByID[sharedResultID(depID)]
			if parent == nil || dep == nil {
				return fmt.Errorf("import cachemoney metadata result_dep: missing local result %d -> %d", parentID, depID)
			}
			if parent.deps == nil {
				parent.deps = make(map[sharedResultID]struct{})
			}
			parent.deps[sharedResultID(depID)] = struct{}{}
			c.rememberDependencyEdgeLocked(parent, dep)
			c.incrementIncomingOwnershipLocked(ctx, dep)
			c.traceImportResultDepLoaded(ctx, importRunID, sharedResultID(parentID), sharedResultID(depID))
			c.traceExplicitDepAdded(ctx, sharedResultID(parentID), sharedResultID(depID), "import_cachemoney_metadata")
		}

		chainRows := slices.Clone(rows.resultSnapshotChainRows)
		slices.SortFunc(chainRows, func(a, b persistdb.MirrorResultSnapshotChain) int {
			switch {
			case a.ResultID < b.ResultID:
				return -1
			case a.ResultID > b.ResultID:
				return 1
			case a.Role < b.Role:
				return -1
			case a.Role > b.Role:
				return 1
			case a.ChainID < b.ChainID:
				return -1
			case a.ChainID > b.ChainID:
				return 1
			default:
				return 0
			}
		})
		for _, row := range chainRows {
			resultID, ok := sourceResultToLocal[uint64(row.ResultID)]
			if !ok {
				return fmt.Errorf("import cachemoney metadata result_snapshot_chain: missing result %d", row.ResultID)
			}
			res := c.resultsByID[sharedResultID(resultID)]
			if res == nil {
				return fmt.Errorf("import cachemoney metadata result_snapshot_chain: missing local result %d", resultID)
			}
			res.payloadMu.Lock()
			res.remoteSnapshotChains = append(res.remoteSnapshotChains, PersistedSnapshotChain{
				Role:    row.Role,
				ChainID: row.ChainID,
				Layers:  clonePersistedSnapshotChainLayers(layersByChain[row.ChainID]),
			})
			res.payloadMu.Unlock()
		}

		for _, resultID := range importedResultIDs {
			res := c.resultsByID[resultID]
			if res == nil {
				continue
			}
			if err := c.recomputeRequiredSessionResourcesLocked(res); err != nil {
				return fmt.Errorf("recompute imported cachemoney required session resources for result %d: %w", resultID, err)
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

		c.stampCachemoneyImportViabilityLocked(ctx, importRunID, importedResultIDs, blobAvailability)

		return nil
	}()
	c.egraphMu.Unlock()
	if importErr != nil {
		return importErr
	}

	for _, resultID := range importedResultIDs {
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

func clonePersistedSnapshotChainLayers(layers []PersistedSnapshotChainLayer) []PersistedSnapshotChainLayer {
	if len(layers) == 0 {
		return nil
	}
	return slices.Clone(layers)
}

func cachemoneySnapshotChainLayersByID(rows []persistdb.MirrorSnapshotChainLayer) (map[string][]PersistedSnapshotChainLayer, error) {
	rows = slices.Clone(rows)
	slices.SortFunc(rows, func(a, b persistdb.MirrorSnapshotChainLayer) int {
		switch {
		case a.ChainID < b.ChainID:
			return -1
		case a.ChainID > b.ChainID:
			return 1
		case a.Position < b.Position:
			return -1
		case a.Position > b.Position:
			return 1
		default:
			return 0
		}
	})

	byChain := make(map[string][]PersistedSnapshotChainLayer)
	for _, row := range rows {
		if row.Position < 0 {
			return nil, fmt.Errorf("snapshot chain %q has negative layer position %d", row.ChainID, row.Position)
		}
		expected := int64(len(byChain[row.ChainID]))
		if row.Position != expected {
			return nil, fmt.Errorf("snapshot chain %q missing layer position %d before %d", row.ChainID, expected, row.Position)
		}
		byChain[row.ChainID] = append(byChain[row.ChainID], PersistedSnapshotChainLayer{
			DiffID:         row.DiffID,
			BlobDigest:     row.BlobDigest,
			Size:           row.Size,
			MediaType:      row.MediaType,
			DescriptorJSON: json.RawMessage(row.DescriptorJSON),
		})
	}
	return byChain, nil
}

func remapPersistedResultEnvelope(env *PersistedResultEnvelope, resultIDMap map[uint64]uint64) error {
	if env == nil {
		return nil
	}
	if env.ResultID != 0 {
		localID, ok := resultIDMap[env.ResultID]
		if !ok {
			return fmt.Errorf("missing result ID mapping for envelope result %d", env.ResultID)
		}
		env.ResultID = localID
	}
	if len(env.ObjectJSON) > 0 {
		remapped, err := remapPersistedObjectJSONResultIDs(env.ObjectJSON, resultIDMap)
		if err != nil {
			return err
		}
		env.ObjectJSON = remapped
	}
	for i := range env.Items {
		if err := remapPersistedResultEnvelope(&env.Items[i], resultIDMap); err != nil {
			return fmt.Errorf("item %d: %w", i, err)
		}
	}
	return nil
}

func remapPersistedObjectJSONResultIDs(raw json.RawMessage, resultIDMap map[uint64]uint64) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var val any
	if err := dec.Decode(&val); err != nil {
		return nil, err
	}
	if err := remapPersistedJSONValueResultIDs(val, resultIDMap, ""); err != nil {
		return nil, err
	}
	return json.Marshal(val)
}

func remapPersistedJSONValueResultIDs(val any, resultIDMap map[uint64]uint64, path string) error {
	switch v := val.(type) {
	case map[string]any:
		for key, child := range v {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if persistedJSONKeyLooksLikeResultID(key) {
				remapped, err := remapPersistedJSONResultIDField(child, resultIDMap, childPath)
				if err != nil {
					return err
				}
				v[key] = remapped
				continue
			}
			if err := remapPersistedJSONValueResultIDs(child, resultIDMap, childPath); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range v {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if err := remapPersistedJSONValueResultIDs(child, resultIDMap, childPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func persistedJSONKeyLooksLikeResultID(key string) bool {
	lower := strings.ToLower(key)
	return lower == "resultid" ||
		strings.HasSuffix(lower, "resultid") ||
		lower == "resultids" ||
		strings.HasSuffix(lower, "resultids")
}

func remapPersistedJSONResultIDField(child any, resultIDMap map[uint64]uint64, path string) (any, error) {
	switch v := child.(type) {
	case json.Number:
		return remapPersistedJSONResultIDNumber(v, resultIDMap, path)
	case []any:
		remapped := make([]any, len(v))
		for i, item := range v {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			num, ok := item.(json.Number)
			if !ok {
				return nil, fmt.Errorf("%s: result ID array element is %T, not number", itemPath, item)
			}
			remappedItem, err := remapPersistedJSONResultIDNumber(num, resultIDMap, itemPath)
			if err != nil {
				return nil, err
			}
			remapped[i] = remappedItem
		}
		return remapped, nil
	default:
		return nil, fmt.Errorf("%s: result ID field is %T, not number or number array", path, child)
	}
}

func remapPersistedJSONResultIDNumber(num json.Number, resultIDMap map[uint64]uint64, path string) (json.Number, error) {
	id, err := parsePersistedJSONResultID(num)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if id == 0 {
		return num, nil
	}
	localID, ok := resultIDMap[id]
	if !ok {
		return "", fmt.Errorf("%s: missing result ID mapping for %d", path, id)
	}
	return json.Number(fmt.Sprintf("%d", localID)), nil
}

func parsePersistedJSONResultID(num json.Number) (uint64, error) {
	if i, err := num.Int64(); err == nil {
		if i < 0 {
			return 0, fmt.Errorf("negative result ID %d", i)
		}
		return uint64(i), nil
	}
	u, err := strconv.ParseUint(num.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer result ID %q", num.String())
	}
	return u, nil
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
