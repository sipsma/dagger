package dagql

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dagger/dagger/dagql/call"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine/slog"
	"github.com/opencontainers/go-digest"
)

func (c *cache) importPersistedState(ctx context.Context) error {
	if c.pdb == nil {
		return nil
	}

	start := time.Now()
	snapshotRows, err := c.pdb.ListLiveSnapshotRefs(ctx)
	if err != nil {
		return fmt.Errorf("list live snapshot refs: %w", err)
	}
	resultRows, err := c.pdb.ListLiveResults(ctx)
	if err != nil {
		return fmt.Errorf("list live results: %w", err)
	}
	if len(resultRows) == 0 {
		slog.Debug("dagql persistence import: no live results")
		return nil
	}
	resultSnapshotRows, err := c.pdb.ListLiveResultSnapshotRefs(ctx)
	if err != nil {
		return fmt.Errorf("list live result_snapshot_refs: %w", err)
	}
	eqFactRows, err := c.pdb.ListLiveEqFacts(ctx)
	if err != nil {
		return fmt.Errorf("list live eq_facts: %w", err)
	}
	termRows, err := c.pdb.ListLiveTerms(ctx)
	if err != nil {
		return fmt.Errorf("list live terms: %w", err)
	}
	termResultRows, err := c.pdb.ListLiveTermResults(ctx)
	if err != nil {
		return fmt.Errorf("list live term_results: %w", err)
	}
	depRows, err := c.pdb.ListLiveDeps(ctx)
	if err != nil {
		return fmt.Errorf("list live deps: %w", err)
	}

	type importedTerm struct {
		row          persistdb.Term
		inputDigests []string
	}
	termByDigest := make(map[string]importedTerm, len(termRows))
	for _, row := range termRows {
		if row.TermDigest == "" {
			return fmt.Errorf("import term: empty term digest")
		}
		var inputDigests []string
		if row.InputDigestsJSON != "" {
			if err := json.Unmarshal([]byte(row.InputDigestsJSON), &inputDigests); err != nil {
				return fmt.Errorf("import term %q input digests: %w", row.TermDigest, err)
			}
		}
		termByDigest[row.TermDigest] = importedTerm{
			row:          row,
			inputDigests: inputDigests,
		}
	}

	snapshotByKey := make(map[string]struct{}, len(snapshotRows))
	for _, row := range snapshotRows {
		if row.RefKey == "" {
			return fmt.Errorf("import snapshot_ref: empty ref key")
		}
		snapshotByKey[row.RefKey] = struct{}{}
	}

	c.egraphMu.Lock()
	defer c.egraphMu.Unlock()

	c.initEgraphLocked()
	resultIDByKey := make(map[string]sharedResultID, len(resultRows))

	for _, row := range resultRows {
		if row.ResultKey == "" {
			return fmt.Errorf("import result: empty result_key")
		}
		if row.IDDigest == "" {
			row.IDDigest = row.ResultKey
		}

		var env PersistedResultEnvelope
		if len(row.SelfPayload) > 0 {
			if err := json.Unmarshal(row.SelfPayload, &env); err != nil {
				return fmt.Errorf("import result %q self payload: %w", row.ResultKey, err)
			}
		} else {
			env = PersistedResultEnvelope{
				Version: 1,
				Kind:    persistedResultKindNull,
			}
		}
		if env.Kind == "" {
			return fmt.Errorf("import result %q: empty self payload kind", row.ResultKey)
		}
		var originalRequestID *call.ID
		if env.Kind != persistedResultKindNull {
			decodedID, err := decodeEnvelopeID(env)
			if err != nil {
				return fmt.Errorf("import result %q envelope ID: %w", row.ResultKey, err)
			}
			originalRequestID = decodedID
		}

		var outputExtra []call.ExtraDigest
		if row.OutputExtraDigests != "" {
			if err := json.Unmarshal([]byte(row.OutputExtraDigests), &outputExtra); err != nil {
				return fmt.Errorf("import result %q output extra digests: %w", row.ResultKey, err)
			}
		}
		var outputEffects []string
		if row.OutputEffectIDs != "" {
			if err := json.Unmarshal([]byte(row.OutputEffectIDs), &outputEffects); err != nil {
				return fmt.Errorf("import result %q output effect IDs: %w", row.ResultKey, err)
			}
		}

		resID := c.nextSharedResultID
		c.nextSharedResultID++
		res := &sharedResult{
			cache:                c,
			id:                   resID,
			persistedResultKey:   cachePersistResultKey(row.ResultKey),
			idDigest:             row.IDDigest,
			originalRequestID:    originalRequestID,
			safeToPersistCache:   row.SafeToPersistCache,
			depOfPersistedResult: row.DepOfPersistedResult,
			outputDigest:         normalizeImportedDigest(row.OutputDigest),
			outputExtraDigests:   outputExtra,
			outputEffectIDs:      outputEffects,
			expiresAtUnix:        row.ExpiresAtUnix,
			createdAtUnixNano:    row.CreatedAtUnixNano,
			lastUsedAtUnixNano:   row.CreatedAtUnixNano,
			sizeEstimateBytes:    sharedResultSizeUnknown,
			usageIdentity:        "",
			description:          row.Description,
			recordType:           row.RecordType,
			persistedEnvelope:    &env,
		}
		if res.outputDigest == "" {
			res.outputDigest = normalizeImportedDigest(row.IDDigest)
		}
		if env.Kind == persistedResultKindNull {
			res.hasValue = true
			res.persistedEnvelope = nil
		} else if env.Kind != persistedResultKindObject {
			if decoded, err := DefaultPersistedSelfCodec.DecodeResult(context.Background(), env); err == nil && decoded != nil {
				res.self = decoded.Unwrap()
				res.hasValue = true
				if objRes, ok := decoded.(AnyObjectResult); ok {
					res.objType = objRes.ObjectType()
				}
				res.persistedEnvelope = nil
			}
		}
		if res.usageIdentity == "" {
			if usageIdentity, ok := cacheUsageIdentity(res); ok {
				res.usageIdentity = usageIdentity
			}
		}
		c.resultsByID[resID] = res
		if res.persistedResultKey != "" {
			c.resultsByPersistKey[res.persistedResultKey] = resID
		}
		resultIDByKey[row.ResultKey] = resID
		c.indexResultOutputDigestsLocked(res)
	}

	for _, row := range resultSnapshotRows {
		resID, ok := resultIDByKey[row.ResultKey]
		if !ok {
			return fmt.Errorf("import result_snapshot_ref: missing result %q", row.ResultKey)
		}
		if _, ok := snapshotByKey[row.RefKey]; !ok {
			return fmt.Errorf("import result_snapshot_ref: missing snapshot_ref %q", row.RefKey)
		}
		res := c.resultsByID[resID]
		if res == nil {
			return fmt.Errorf("import result_snapshot_ref: missing in-memory result for %q", row.ResultKey)
		}
		res.persistedSnapshotLinks = append(res.persistedSnapshotLinks, PersistedSnapshotRefLink{
			RefKey: row.RefKey,
			Role:   row.Role,
			Slot:   row.Slot,
		})
	}

	for _, row := range eqFactRows {
		if row.OwnerResultKey == "" {
			return fmt.Errorf("import eq_fact: empty owner_result_key")
		}
		if _, ok := resultIDByKey[row.OwnerResultKey]; !ok {
			return fmt.Errorf("import eq_fact: missing owner result %q", row.OwnerResultKey)
		}
		if row.LHSDigest == "" || row.RHSDigest == "" {
			return fmt.Errorf("import eq_fact: empty digests for owner %q", row.OwnerResultKey)
		}
		lhs := c.ensureEqClassForDigestLocked(row.LHSDigest)
		rhs := c.ensureEqClassForDigestLocked(row.RHSDigest)
		c.mergeEqClassesLocked(lhs, rhs)
	}

	termIDByDigest := make(map[string]egraphTermID, len(termByDigest))
	getOrCreateTerm := func(termDigest string, resultEqID eqClassID) (*egraphTerm, error) {
		if termID, ok := termIDByDigest[termDigest]; ok {
			term := c.egraphTerms[termID]
			if term == nil {
				return nil, fmt.Errorf("import term %q missing in egraph index", termDigest)
			}
			term.outputEqID = c.mergeEqClassesLocked(term.outputEqID, resultEqID)
			return term, nil
		}

		imported, ok := termByDigest[termDigest]
		if !ok {
			return nil, fmt.Errorf("import term_result references unknown term %q", termDigest)
		}
		inputEqIDs := make([]eqClassID, 0, len(imported.inputDigests))
		for _, inDig := range imported.inputDigests {
			if inDig == "" {
				return nil, fmt.Errorf("import term %q has empty input digest", termDigest)
			}
			inputEqIDs = append(inputEqIDs, c.ensureEqClassForDigestLocked(inDig))
		}

		termID := c.nextEgraphTermID
		c.nextEgraphTermID++
		term := newEgraphTerm(
			termID,
			normalizeImportedDigest(imported.row.SelfDigest),
			inputEqIDs,
			resultEqID,
		)
		term.termDigest = termDigest
		c.egraphTerms[termID] = term
		termIDByDigest[termDigest] = termID

		digestSet := c.egraphTermsByDigest[termDigest]
		if digestSet == nil {
			digestSet = make(map[egraphTermID]struct{})
			c.egraphTermsByDigest[termDigest] = digestSet
		}
		digestSet[termID] = struct{}{}
		for _, inputEqID := range inputEqIDs {
			classSet := c.egraphClassTerms[inputEqID]
			if classSet == nil {
				classSet = make(map[egraphTermID]struct{})
				c.egraphClassTerms[inputEqID] = classSet
			}
			classSet[termID] = struct{}{}
		}
		return term, nil
	}

	for _, row := range termResultRows {
		resID, ok := resultIDByKey[row.ResultKey]
		if !ok {
			return fmt.Errorf("import term_result: missing result %q", row.ResultKey)
		}
		res := c.resultsByID[resID]
		if res == nil {
			return fmt.Errorf("import term_result: missing in-memory result for %q", row.ResultKey)
		}
		outputDig := res.outputDigest.String()
		if outputDig == "" {
			outputDig = c.resultIDDigestLocked(res)
		}
		if outputDig == "" {
			return fmt.Errorf("import term_result: result %q has no output digest", row.ResultKey)
		}
		resultEqID := c.ensureEqClassForDigestLocked(outputDig)
		term, err := getOrCreateTerm(row.TermDigest, resultEqID)
		if err != nil {
			return err
		}

		resultTerms := c.egraphTermIDsByResult[resID]
		if resultTerms == nil {
			resultTerms = make(map[egraphTermID]struct{})
			c.egraphTermIDsByResult[resID] = resultTerms
		}
		resultTerms[term.id] = struct{}{}

		termResults := c.egraphResultsByTermID[term.id]
		if termResults == nil {
			termResults = make(map[sharedResultID]struct{})
			c.egraphResultsByTermID[term.id] = termResults
		}
		termResults[resID] = struct{}{}
	}

	if len(termIDByDigest) != len(termByDigest) {
		var unattached []string
		for termDigest := range termByDigest {
			if _, ok := termIDByDigest[termDigest]; !ok {
				unattached = append(unattached, termDigest)
			}
		}
		slices.Sort(unattached)
		return fmt.Errorf("import terms with no result associations: %v", unattached)
	}

	for _, row := range depRows {
		parentID, ok := resultIDByKey[row.ParentResultKey]
		if !ok {
			return fmt.Errorf("import dep: missing parent result %q", row.ParentResultKey)
		}
		depID, ok := resultIDByKey[row.DepResultKey]
		if !ok {
			return fmt.Errorf("import dep: missing dep result %q", row.DepResultKey)
		}
		parent := c.resultsByID[parentID]
		if parent == nil {
			return fmt.Errorf("import dep: missing in-memory parent result for %q", row.ParentResultKey)
		}
		if parent.deps == nil {
			parent.deps = make(map[sharedResultID]struct{})
		}
		parent.deps[depID] = struct{}{}
	}

	for _, res := range c.resultsByID {
		if res == nil || !res.depOfPersistedResult {
			continue
		}
		c.markResultAsDepOfPersistedLocked(res)
	}

	slog.Info("dagql persistence import completed",
		"duration", time.Since(start),
		"results", len(resultRows),
		"snapshotRefs", len(snapshotRows),
		"resultSnapshotRefs", len(resultSnapshotRows),
		"eqFacts", len(eqFactRows),
		"terms", len(termRows),
		"termResults", len(termResultRows),
		"deps", len(depRows),
	)
	return nil
}

func normalizeImportedDigest(raw string) digest.Digest {
	if raw == "" {
		return ""
	}
	return digest.Digest(raw)
}

func (c *cache) ensurePersistedHitValueLoaded(ctx context.Context, hit AnyResult) (AnyResult, error) {
	if hit == nil {
		return nil, nil
	}
	res := hit.cacheSharedResult()
	if res == nil {
		return hit, nil
	}

	c.egraphMu.RLock()
	hasValue := res.hasValue
	env := res.persistedEnvelope
	c.egraphMu.RUnlock()
	if hasValue || env == nil {
		return hit, nil
	}

	ctx = ContextWithPersistedObjectResolver(ctx, c.persistedObjectResolver())
	decoded, err := DefaultPersistedSelfCodec.DecodeResult(ctx, *env)
	if err != nil {
		if CurrentDagqlServer(ctx) == nil || strings.Contains(err.Error(), "unknown scalar type") {
			return nil, errPersistedHitNotDecodable
		}
		return nil, fmt.Errorf("decode persisted hit payload: %w", err)
	}

	c.egraphMu.Lock()
	if !res.hasValue && res.persistedEnvelope != nil {
		res.self = decoded.Unwrap()
		res.hasValue = true
		if objRes, ok := decoded.(AnyObjectResult); ok {
			res.objType = objRes.ObjectType()
		}
		res.persistedEnvelope = nil
	}
	objType := res.objType
	c.egraphMu.Unlock()

	ret := Result[Typed]{
		shared:   res,
		id:       hit.ID(),
		hitCache: hit.HitCache(),
	}
	if objType == nil {
		return ret, nil
	}
	objRes, err := objType.New(ret)
	if err != nil {
		return nil, fmt.Errorf("reconstruct object result from persisted hit payload: %w", err)
	}
	return objRes, nil
}
