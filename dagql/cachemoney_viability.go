package dagql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/dagger/dagger/dagql/cachemoneyproto"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/opencontainers/go-digest"
)

const (
	remoteCacheReasonPendingViability       = "pending_viability"
	remoteCacheReasonLocalResult            = "local_result"
	remoteCacheReasonDirectPayload          = "direct_payload"
	remoteCacheReasonRemoteSnapshotBlobs    = "remote_snapshot_blobs_available"
	remoteCacheReasonRetainedRecipeFallback = "retained_recipe_fallback"
	remoteCacheReasonAwaitingSlotPlans      = "awaiting_slot_plans"
	remoteCacheReasonDependencyNonViable    = "dependency_non_viable"
	remoteCacheReasonMissingResult          = "missing_result"
	remoteCacheReasonMissingBlobNoFallback  = "missing_blob_no_fallback"
	remoteCacheReasonMissingSnapshotPlan    = "missing_snapshot_plan_no_fallback"
	remoteCacheReasonInvalidPayloadRefs     = "invalid_payload_refs"
	remoteCacheReasonCyclicDependency       = "cyclic_dependency"
)

type cachemoneyRemoteViability struct {
	viable   bool
	eligible bool
	reason   string
}

type cachemoneyResultImportFacts struct {
	hasRetainedRecipe bool
	expectsSnapshot   bool
	depResultIDs      []sharedResultID
}

type cachemoneyContentInfoProvider interface {
	ContentInfo(context.Context, digest.Digest) (content.Info, error)
}

func (c *Cache) cachemoneyBlobAvailability(ctx context.Context, source CachemoneyImportSource, layers []persistdb.MirrorSnapshotChainLayer) map[string]bool {
	availability := make(map[string]bool)
	for _, layer := range layers {
		blobDigest := layer.BlobDigest
		if blobDigest == "" {
			continue
		}
		if availability[blobDigest] {
			continue
		}
		if cachemoneyBlobAvailableFromSourceIndex(source.BlobIndex, layer) {
			availability[blobDigest] = true
			continue
		}
		if c.cachemoneyBlobLocallyPresent(ctx, blobDigest) {
			availability[blobDigest] = true
			continue
		}
		availability[blobDigest] = false
	}
	return availability
}

func cachemoneyBlobAvailableFromSourceIndex(blobIndex map[string]cachemoneyproto.BlobLocation, layer persistdb.MirrorSnapshotChainLayer) bool {
	if len(blobIndex) == 0 || layer.BlobDigest == "" {
		return false
	}
	location, ok := blobIndex[layer.BlobDigest]
	if !ok {
		return false
	}
	if location.URL == "" {
		return false
	}
	if location.Size != 0 && layer.Size != 0 && location.Size != layer.Size {
		return false
	}
	if location.MediaType != "" && layer.MediaType != "" && location.MediaType != layer.MediaType {
		return false
	}
	return true
}

func (c *Cache) cachemoneyBlobLocallyPresent(ctx context.Context, blobDigest string) bool {
	if c == nil || c.snapshotManager == nil || blobDigest == "" {
		return false
	}
	provider, ok := c.snapshotManager.(cachemoneyContentInfoProvider)
	if !ok {
		return false
	}
	dgst, err := digest.Parse(blobDigest)
	if err != nil {
		return false
	}
	_, err = provider.ContentInfo(ctx, dgst)
	return err == nil
}

func (c *Cache) stampCachemoneyImportViabilityLocked(
	ctx context.Context,
	importRunID string,
	importedResultIDs []sharedResultID,
	blobAvailability map[string]bool,
	factsByResultID map[sharedResultID]cachemoneyResultImportFacts,
) {
	importedResultIDs = slices.Clone(importedResultIDs)
	slices.Sort(importedResultIDs)

	memo := make(map[sharedResultID]cachemoneyRemoteViability, len(importedResultIDs))
	visiting := make(map[sharedResultID]struct{})
	for _, resultID := range importedResultIDs {
		viability := c.cachemoneyResultViabilityLocked(resultID, blobAvailability, factsByResultID, memo, visiting)
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		res.remoteCacheViable = viability.viable
		res.remoteCacheEligible = viability.eligible
		res.remoteCacheReason = viability.reason
		c.traceCachemoneyImportViability(ctx, importRunID, res, viability)
	}
}

func (c *Cache) CachemoneyResultUsesRetainedRecipeFallback(ctx context.Context, resultID uint64) (bool, error) {
	if c == nil || resultID == 0 {
		return false, nil
	}
	res, _, _, err := c.sharedResultByResultID(ctx, "", sharedResultID(resultID), sharedResultLookupExact)
	if err != nil {
		return false, err
	}
	return res.remoteCacheImported && res.remoteCacheReason == remoteCacheReasonRetainedRecipeFallback, nil
}

func (c *Cache) cachemoneyResultViabilityLocked(
	resultID sharedResultID,
	blobAvailability map[string]bool,
	factsByResultID map[sharedResultID]cachemoneyResultImportFacts,
	memo map[sharedResultID]cachemoneyRemoteViability,
	visiting map[sharedResultID]struct{},
) cachemoneyRemoteViability {
	if resultID == 0 {
		return cachemoneyRemoteViability{reason: remoteCacheReasonMissingResult}
	}
	if viability, ok := memo[resultID]; ok {
		return viability
	}
	if _, ok := visiting[resultID]; ok {
		return cachemoneyRemoteViability{reason: remoteCacheReasonCyclicDependency}
	}

	res := c.resultsByID[resultID]
	if res == nil {
		return cachemoneyRemoteViability{reason: remoteCacheReasonMissingResult}
	}
	if !res.remoteCacheImported {
		return cachemoneyRemoteViability{
			viable:   true,
			eligible: true,
			reason:   remoteCacheReasonLocalResult,
		}
	}

	facts, ok := factsByResultID[resultID]
	if !ok {
		viability := cachemoneyRemoteViability{reason: remoteCacheReasonInvalidPayloadRefs}
		memo[resultID] = viability
		return viability
	}

	visiting[resultID] = struct{}{}
	defer delete(visiting, resultID)

	deps, err := c.cachemoneyResultDependencyIDsLocked(res, facts)
	if err != nil {
		viability := cachemoneyRemoteViability{reason: remoteCacheReasonInvalidPayloadRefs}
		memo[resultID] = viability
		return viability
	}
	for _, depID := range deps {
		depViability := c.cachemoneyResultViabilityLocked(depID, blobAvailability, factsByResultID, memo, visiting)
		if !depViability.viable {
			viability := cachemoneyRemoteViability{
				reason: remoteCacheReasonDependencyNonViable + ":" + depViability.reason,
			}
			memo[resultID] = viability
			return viability
		}
	}

	state := res.loadPayloadState()
	chains := res.loadRemoteSnapshotChains()
	hasRemoteChains := len(chains) > 0
	hasRetainedRecipe := facts.hasRetainedRecipe
	expectsSnapshot := hasRemoteChains || len(state.snapshotOwnerLinks) > 0 || facts.expectsSnapshot

	var viability cachemoneyRemoteViability
	switch {
	case hasRemoteChains && cachemoneySnapshotChainsAvailable(chains, blobAvailability):
		viability = cachemoneyRemoteViability{
			viable:   true,
			eligible: true,
			reason:   remoteCacheReasonRemoteSnapshotBlobs,
		}
	case hasRemoteChains && hasRetainedRecipe:
		viability = cachemoneyRemoteViability{
			viable:   true,
			eligible: true,
			reason:   remoteCacheReasonRetainedRecipeFallback,
		}
	case hasRemoteChains:
		viability = cachemoneyRemoteViability{
			reason: remoteCacheReasonMissingBlobNoFallback,
		}
	case expectsSnapshot && hasRetainedRecipe:
		viability = cachemoneyRemoteViability{
			viable:   true,
			eligible: true,
			reason:   remoteCacheReasonRetainedRecipeFallback,
		}
	case expectsSnapshot:
		viability = cachemoneyRemoteViability{
			reason: remoteCacheReasonMissingSnapshotPlan,
		}
	default:
		viability = cachemoneyRemoteViability{
			viable:   true,
			eligible: true,
			reason:   remoteCacheReasonDirectPayload,
		}
	}

	memo[resultID] = viability
	return viability
}

func (c *Cache) cachemoneyResultDependencyIDsLocked(res *sharedResult, facts cachemoneyResultImportFacts) ([]sharedResultID, error) {
	seen := make(map[sharedResultID]struct{})
	add := func(id sharedResultID) {
		if id == 0 {
			return
		}
		seen[id] = struct{}{}
	}

	for depID := range res.deps {
		add(depID)
	}
	for _, depID := range facts.depResultIDs {
		add(depID)
	}
	if facts.hasRetainedRecipe || (!facts.expectsSnapshot && len(res.loadRemoteSnapshotChains()) == 0) {
		frame := res.loadResultCall()
		if err := c.cachemoneyWalkResultCallRefsLocked(frame, func(ref *ResultCallRef) error {
			if ref != nil && ref.ResultID != 0 {
				add(sharedResultID(ref.ResultID))
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	deps := make([]sharedResultID, 0, len(seen))
	for depID := range seen {
		if depID != res.id {
			deps = append(deps, depID)
		}
	}
	slices.Sort(deps)
	return deps, nil
}

func (c *Cache) cachemoneyWalkResultCallRefsLocked(rootCall *ResultCall, visit func(*ResultCallRef) error) error {
	if rootCall == nil {
		return nil
	}
	seenCalls := map[*ResultCall]struct{}{}
	seenResultIDs := map[uint64]struct{}{}

	var walkLiteral func(*ResultCallLiteral) error
	var walkRef func(*ResultCallRef) error
	var walkCall func(*ResultCall) error

	walkLiteral = func(lit *ResultCallLiteral) error {
		if lit == nil {
			return nil
		}
		switch lit.Kind {
		case ResultCallLiteralKindResultRef:
			return walkRef(lit.ResultRef)
		case ResultCallLiteralKindList:
			for _, item := range lit.ListItems {
				if err := walkLiteral(item); err != nil {
					return err
				}
			}
		case ResultCallLiteralKindObject:
			for _, field := range lit.ObjectFields {
				if field == nil {
					continue
				}
				if err := walkLiteral(field.Value); err != nil {
					return fmt.Errorf("field %q: %w", field.Name, err)
				}
			}
		}
		return nil
	}

	walkRef = func(ref *ResultCallRef) error {
		if ref == nil {
			return nil
		}
		if visit != nil {
			if err := visit(ref); err != nil {
				return err
			}
		}
		frame := ref.Call
		if frame == nil {
			if ref.ResultID == 0 {
				return nil
			}
			if _, seen := seenResultIDs[ref.ResultID]; seen {
				return nil
			}
			seenResultIDs[ref.ResultID] = struct{}{}
			res := c.resultsByID[sharedResultID(ref.ResultID)]
			if res == nil {
				return fmt.Errorf("missing result %d", ref.ResultID)
			}
			frame = res.loadResultCall()
			if frame == nil {
				return fmt.Errorf("missing result call frame for result %d", ref.ResultID)
			}
		}
		return walkCall(frame)
	}

	walkCall = func(call *ResultCall) error {
		if call == nil {
			return nil
		}
		if _, seen := seenCalls[call]; seen {
			return nil
		}
		seenCalls[call] = struct{}{}

		if call.Module != nil {
			if err := walkRef(call.Module.ResultRef); err != nil {
				return fmt.Errorf("module %q: %w", call.Module.Name, err)
			}
		}
		if err := walkRef(call.Receiver); err != nil {
			return fmt.Errorf("receiver: %w", err)
		}
		for _, arg := range call.Args {
			if arg == nil {
				continue
			}
			if err := walkLiteral(arg.Value); err != nil {
				return fmt.Errorf("arg %q: %w", arg.Name, err)
			}
		}
		for _, input := range call.ImplicitInputs {
			if input == nil {
				continue
			}
			if err := walkLiteral(input.Value); err != nil {
				return fmt.Errorf("implicit input %q: %w", input.Name, err)
			}
		}
		return nil
	}

	return walkCall(rootCall)
}

func cachemoneySnapshotChainsAvailable(chains []PersistedSnapshotChain, blobAvailability map[string]bool) bool {
	for _, chain := range chains {
		for _, layer := range chain.Layers {
			if layer.BlobDigest == "" || !blobAvailability[layer.BlobDigest] {
				return false
			}
		}
	}
	return true
}

func cachemoneyNonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s != ""
}

func cachemoneyNonEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}

func cachemoneyImportFactsForEnvelope(env *PersistedResultEnvelope) (cachemoneyResultImportFacts, error) {
	var facts cachemoneyResultImportFacts
	if env == nil {
		return facts, nil
	}
	facts.hasRetainedRecipe =
		cachemoneyJSONTopLevelHasKey(env.ObjectJSON, "lazyKind", cachemoneyNonEmptyString) ||
			cachemoneyJSONTopLevelHasKey(env.ObjectJSON, "lazyJSON", cachemoneyNonEmptyValue)
	facts.expectsSnapshot = cachemoneyJSONTopLevelHasKey(env.ObjectJSON, "form", func(v any) bool {
		s, ok := v.(string)
		return ok && s == "snapshot"
	})
	depResultIDs, err := cachemoneyPersistedEnvelopeResultIDs(env)
	if err != nil {
		return cachemoneyResultImportFacts{}, err
	}
	facts.depResultIDs = make([]sharedResultID, 0, len(depResultIDs))
	for _, depResultID := range depResultIDs {
		facts.depResultIDs = append(facts.depResultIDs, sharedResultID(depResultID))
	}
	return facts, nil
}

func cachemoneyJSONTopLevelHasKey(raw json.RawMessage, target string, pred func(any) bool) bool {
	if len(raw) == 0 {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var val any
	if err := dec.Decode(&val); err != nil {
		return false
	}
	fields, ok := val.(map[string]any)
	if !ok {
		return false
	}
	child, ok := fields[target]
	return ok && pred(child)
}

func cachemoneyPersistedEnvelopeResultIDs(env *PersistedResultEnvelope) ([]uint64, error) {
	if env == nil {
		return nil, nil
	}
	seen := map[uint64]struct{}{}
	add := func(id uint64) {
		if id != 0 {
			seen[id] = struct{}{}
		}
	}
	add(env.ResultID)
	if len(env.ObjectJSON) > 0 {
		ids, err := persistedObjectJSONResultIDs(env.ObjectJSON)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			add(id)
		}
	}
	for i := range env.Items {
		ids, err := cachemoneyPersistedEnvelopeResultIDs(&env.Items[i])
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			add(id)
		}
	}
	out := make([]uint64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

func persistedObjectJSONResultIDs(raw json.RawMessage) ([]uint64, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var val any
	if err := dec.Decode(&val); err != nil {
		return nil, err
	}
	var ids []uint64
	if err := collectPersistedJSONValueResultIDs(val, "", &ids); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return ids, nil
}

func collectPersistedJSONValueResultIDs(val any, path string, ids *[]uint64) error {
	switch v := val.(type) {
	case map[string]any:
		for key, child := range v {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if persistedJSONKeyLooksLikeResultID(key) {
				fieldIDs, err := persistedJSONResultIDFieldIDs(child, childPath)
				if err != nil {
					return err
				}
				*ids = append(*ids, fieldIDs...)
				continue
			}
			if err := collectPersistedJSONValueResultIDs(child, childPath, ids); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range v {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			if err := collectPersistedJSONValueResultIDs(child, childPath, ids); err != nil {
				return err
			}
		}
	}
	return nil
}

func persistedJSONResultIDFieldIDs(child any, path string) ([]uint64, error) {
	switch v := child.(type) {
	case json.Number:
		id, err := parsePersistedJSONResultID(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if id == 0 {
			return nil, nil
		}
		return []uint64{id}, nil
	case []any:
		ids := make([]uint64, 0, len(v))
		for i, item := range v {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			num, ok := item.(json.Number)
			if !ok {
				return nil, fmt.Errorf("%s: result ID array element is %T, not number", itemPath, item)
			}
			id, err := parsePersistedJSONResultID(num)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", itemPath, err)
			}
			if id != 0 {
				ids = append(ids, id)
			}
		}
		return ids, nil
	default:
		return nil, fmt.Errorf("%s: result ID field is %T, not number or number array", path, child)
	}
}
