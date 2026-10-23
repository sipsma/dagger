package dagql

import (
	"context"
	"slices"
	"time"

	"github.com/dagger/dagger/engine/slog"
	"github.com/opencontainers/go-digest"
)

// A module function call that returns another call's result, as a module's
// Wolfi.container returns the container its body built, publishes no value
// of its own: the cache adopts the returned result's entry and teaches it the
// call's identity. The call is still a result the cache records, an entry of
// its own:
//   - its frame is the call's, so its recipe and term are the call's, derived
//     from the frame by every cache that holds the entry, as for any record;
//   - its value is the returned result's entry (returnedID), stored as a
//     returned_ref envelope that names that entry's row, which the entry
//     depends on, with the frame's receiver, module and arguments;
//   - it is in the returned result's class, since its value is that result;
//   - a lookup that selects it serves the returned result
//     (servableCandidateLocked), so callers get the result the call returned,
//     as from the warm engine that ran it.
// It persists and transfers like any entry, so an engine that merges it hits
// the call the way the engine that ran it does.

// callRecordIntent is a publication's call, with the digests the publication
// derived from its frame, for recordCallLocked.
type callRecordIntent struct {
	frame     *ResultCall
	recipe    digest.Digest
	self      digest.Digest
	inputs    []digest.Digest
	inputRefs []ResultCallStructuralInputRef
}

// recordCallLocked records call, a module function call whose publication
// retained returned, as an entry of its own whose value is returned, retained
// with the edge expiry given. A call whose recipe is one of returned's, its
// own value, records nothing. A recipe that already has a current entry
// keeps it: the call's entry, which gets the retention, or another value,
// which stays as it is. Requires egraphMu for writing.
func (c *Cache) recordCallLocked(ctx context.Context, returned *sharedResult, call *callRecordIntent, edgeExpiresAtUnix int64) {
	if returned == nil || returned.id == 0 || c.resultsByID[returned.id] != returned || call.recipe == "" || call.self == "" {
		return
	}
	if returned.returnedID != 0 || slices.Contains(returned.recipeKeys, call.recipe) {
		return
	}
	if cur := c.currentEntryForRecipeLocked(call.recipe); cur != nil {
		if cur.returnedID == returned.id {
			c.upsertPersistedEdgeLocked(ctx, cur, edgeExpiresAtUnix, false)
		}
		return
	}
	frame := call.frame.clone()
	deps, err := resultCallDependencies(frame, 0)
	if err != nil {
		slog.WarnContext(ctx, "call not recorded", "field", frame.Field, "error", err)
		return
	}
	deps = append(deps, resultCallDep{resultID: returned.id, path: "returned"})
	for _, dep := range deps {
		if c.resultsByID[dep.resultID] == nil {
			slog.WarnContext(ctx, "call not recorded: it names a result the cache doesn't have", "field", frame.Field, "result", uint64(dep.resultID), "path", dep.path)
			return
		}
	}
	provenance, err := c.inputProvenanceForRefs(call.inputRefs)
	if err != nil {
		slog.WarnContext(ctx, "call not recorded", "field", frame.Field, "error", err)
		return
	}

	now := time.Now().UnixNano()
	res := &sharedResult{
		id:                 c.nextSharedResultID,
		returnedID:         returned.id,
		recordType:         frame.Field,
		description:        frame.Field,
		createdAtUnixNano:  now,
		lastUsedAtUnixNano: now,
		expiresAtUnix:      edgeExpiresAtUnix,
	}
	c.nextSharedResultID++
	res.persistedEnvelope = &PersistedResultEnvelope{
		Version:    persistedResultEnvelopeVersion,
		Kind:       persistedResultKindReturned,
		ResultID:   uint64(res.id),
		ReturnedID: uint64(returned.id),
	}
	res.storeResultCall(frame)
	c.putResultLocked(res)
	c.traceResultCreated(ctx, res)
	c.applyPreparedResultIdentityLocked(ctx, res, frame, call.recipe, call.self, call.inputs, provenance, call.recipe)
	c.joinReturnedClassLocked(ctx, res, returned)
	c.indexRecipeLocked(call.recipe, res)
	res.deps = make(map[sharedResultID]struct{}, len(deps))
	for _, dep := range deps {
		depRes := c.resultsByID[dep.resultID]
		res.deps[dep.resultID] = struct{}{}
		c.rememberDependencyEdgeLocked(res, depRes)
		c.incrementIncomingOwnershipLocked(ctx, depRes)
		c.traceResultCallDepAdded(ctx, res.id, dep.resultID, dep.path)
	}
	res.dependencyOwnershipRevision++
	if _, err := c.recomputeRequiredSessionResourcesLocked(res); err != nil {
		slog.WarnContext(ctx, "call record's session requirements", "field", frame.Field, "error", err)
	}
	c.upsertPersistedEdgeLocked(ctx, res, edgeExpiresAtUnix, false)
}

// joinReturnedClassLocked puts res, an entry whose value is returned, in
// returned's output classes: they are the same result. Requires egraphMu for
// writing.
func (c *Cache) joinReturnedClassLocked(ctx context.Context, res, returned *sharedResult) {
	var ids []eqClassID
	for id := range c.outputEqClassesForResultLocked(res.id) {
		ids = append(ids, id)
	}
	for id := range c.outputEqClassesForResultLocked(returned.id) {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}
	joined := c.mergeEqClassesLocked(ctx, ids...)
	if joined == 0 {
		return
	}
	c.addResultOutputEqClassLocked(res.id, joined)
	c.addResultOutputEqClassLocked(returned.id, joined)
}
