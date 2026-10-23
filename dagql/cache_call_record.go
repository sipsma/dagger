package dagql

import (
	"context"
	"errors"
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
// decided to retain returned, as an entry of its own whose value is
// returned, retained with the edge expiry given. A call whose recipe is one
// of returned's, its own value, records nothing. The call's entry is its
// recipe's current entry, under the rules every entry follows
// (cache_current_entry.go): another value of the recipe stays as it is; a
// live entry of the call stays, and takes the retention if it names
// returned; an expired one, whose own expiry or whose value's has passed,
// takes the new value in place if nothing uses it, and is retired for a new
// entry otherwise. It returns the call's entry it recorded or retained, nil when
// none, and the entries a replaced value's dependencies may leave unowned
// and the replaced value's release, for the caller to collect and run after
// unlocking. Requires egraphMu for writing.
func (c *Cache) recordCallLocked(ctx context.Context, returned *sharedResult, call *callRecordIntent, edgeExpiresAtUnix int64) (*sharedResult, collectionQueue, []OnReleaseFunc) {
	if returned == nil || returned.id == 0 || c.resultsByID[returned.id] != returned || call.recipe == "" || call.self == "" {
		return nil, nil, nil
	}
	if returned.returnedID != 0 || slices.Contains(returned.recipeKeys, call.recipe) {
		return nil, nil, nil
	}
	now := time.Now()
	cur := c.currentEntryForRecipeLocked(call.recipe)
	if cur != nil {
		if cur.returnedID == 0 {
			return nil, nil, nil
		}
		if !c.resultExpiredAtLocked(cur, now.Unix()) {
			if cur.returnedID == returned.id {
				c.upsertPersistedEdgeLocked(ctx, cur, edgeExpiresAtUnix, false)
				return cur, nil, nil
			}
			return nil, nil, nil
		}
		if c.resultInUseLocked(cur) {
			c.retireResultLocked(cur)
			cur = nil
		}
	}
	frame := call.frame.clone()
	deps, err := resultCallDependencies(frame, 0)
	if err != nil {
		slog.WarnContext(ctx, "call not recorded", "field", frame.Field, "error", err)
		return nil, nil, nil
	}
	// A function can return its receiver or an argument: the entry owns it
	// once, as it does every entry it names.
	if !slices.ContainsFunc(deps, func(dep resultCallDep) bool { return dep.resultID == returned.id }) {
		deps = append(deps, resultCallDep{resultID: returned.id, path: "returned"})
	}
	for _, dep := range deps {
		if c.resultsByID[dep.resultID] == nil {
			slog.WarnContext(ctx, "call not recorded: it names a result the cache doesn't have", "field", frame.Field, "result", uint64(dep.resultID), "path", dep.path)
			return nil, nil, nil
		}
	}
	provenance, err := c.inputProvenanceForRefs(call.inputRefs)
	if err != nil {
		slog.WarnContext(ctx, "call not recorded", "field", frame.Field, "error", err)
		return nil, nil, nil
	}

	value := &sharedResult{
		returnedID:         returned.id,
		recordType:         frame.Field,
		description:        frame.Field,
		createdAtUnixNano:  now.UnixNano(),
		lastUsedAtUnixNano: now.UnixNano(),
	}
	value.storeResultCall(frame)
	var (
		res      *sharedResult
		replaced replacedValue
		queue    collectionQueue
	)
	if cur != nil {
		value.persistedEnvelope = returnedEnvelope(cur.id, returned.id)
		var err error
		if replaced, queue, err = c.replaceResultValueInPlaceLocked(ctx, cur, value, edgeExpiresAtUnix); err != nil {
			slog.WarnContext(ctx, "call record's replaced value", "field", frame.Field, "error", err)
		}
		res = cur
	} else {
		value.id = c.nextSharedResultID
		c.nextSharedResultID++
		value.persistedEnvelope = returnedEnvelope(value.id, returned.id)
		res = value
		c.putResultLocked(res)
		c.traceResultCreated(ctx, res)
	}
	res.expiresAtUnix = edgeExpiresAtUnix
	c.applyPreparedResultIdentityLocked(ctx, res, frame, call.recipe, call.self, call.inputs, provenance, call.recipe)
	c.joinReturnedClassLocked(ctx, res, returned)
	if cur == nil {
		c.indexRecipeLocked(call.recipe, res)
	}
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
	if cur == nil {
		return res, nil, nil
	}
	// The new value's dependency edges are in place: the old value's can go
	// now without collecting a dependency both share.
	queue, err = c.releaseReplacedDependenciesLocked(ctx, replaced, queue)
	if err != nil {
		slog.WarnContext(ctx, "call record's replaced dependencies", "field", frame.Field, "error", err)
	}
	return res, queue, []OnReleaseFunc{replaced.release}
}

// returnedEnvelope is the stored value of the call's entry id, whose value is
// the entry returned.
func returnedEnvelope(id, returned sharedResultID) *PersistedResultEnvelope {
	return &PersistedResultEnvelope{
		Version:    persistedResultEnvelopeVersion,
		Kind:       persistedResultKindReturned,
		ResultID:   uint64(id),
		ReturnedID: uint64(returned),
	}
}

// servedEntryLocked returns the entry a load of res serves: the returned
// entry of a call's entry, res itself otherwise. Requires egraphMu.
func (c *Cache) servedEntryLocked(res *sharedResult) *sharedResult {
	if res != nil && res.returnedID != 0 {
		return c.resultsByID[res.returnedID]
	}
	return res
}

// recordCall records oc's call as an entry of its own (recordCallLocked),
// once its publication has decided to retain the returned result, and
// returns that entry, nil when none. Recording can join classes, as identity
// teaching does, and its notifications are flushed before the unlock; what a
// replaced value leaves is collected and released after it. A failure is
// logged: the call itself succeeded.
func (c *Cache) recordCall(ctx context.Context, oc *ongoingCall) *sharedResult {
	c.egraphMu.Lock()
	notify, notifyOwner := c.beginShareNotificationsLocked()
	entry, queue, releases := c.recordCallLocked(ctx, oc.res, oc.callRecord, oc.persistedEdgeExpiresAtUnix)
	c.flushShareNotificationsLocked(ctx, notify, notifyOwner)
	duplicates := c.takeShareDuplicateHoldsLocked()
	collected, err := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
	c.egraphMu.Unlock()
	c.releaseShareDuplicateHolds(ctx, duplicates)
	if err = errors.Join(err, runOnReleaseFuncs(context.WithoutCancel(ctx), append(releases, collected...))); err != nil {
		slog.WarnContext(ctx, "call record's replaced value release", "error", err)
	}
	return entry
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
