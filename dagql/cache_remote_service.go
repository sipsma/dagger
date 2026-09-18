package dagql

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/dagger/dagger/engine/slog"
	"github.com/opencontainers/go-digest"
)

// SessionResultEntry describes one result of a session's set, as a session
// report to the remote cache service needs it.
type SessionResultEntry struct {
	// ResultID is the result number on this engine.
	ResultID uint64
	// Type is the GraphQL type of the result's call, without non-null
	// markers: "Directory" for a Directory, "[Directory]" for a list.
	Type string
	// Field is the field name of the result's call, or the synthetic
	// operation name for a synthetic call.
	Field string
	// DependsOn lists the result numbers this result depends on directly,
	// sorted. It is never nil.
	DependsOn []uint64
	// Retained is true when the result has a retention edge.
	Retained bool
	// Imported is true when the result came from an import.
	Imported bool
	// RecipeDigest is the recipe digest of the result's call. It is set only
	// when Retained is true and Imported is false, and it is empty when the
	// digest could not be derived.
	RecipeDigest digest.Digest
}

// SessionResults describes every result in the session's set at this
// moment. It is a best-effort snapshot: a result that leaves the cache
// between its steps is skipped.
//
// The recipe digests are derived with no cache lock held, because a
// derivation can itself take the read side of egraphMu. The retained and not
// imported entries are held while their digests are derived, so they and
// their dependencies stay alive even if the session is released meanwhile.
func (c *Cache) SessionResults(ctx context.Context, sessionID string) (entries []SessionResultEntry, rerr error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session results: empty session ID")
	}
	op, err := c.beginCacheOperation()
	if err != nil {
		return nil, fmt.Errorf("session results for %q: %w", sessionID, err)
	}
	defer op.finish(false)

	c.sessionMu.Lock()
	ids := slices.Sorted(maps.Keys(c.sessionResultIDsBySession[sessionID]))
	c.sessionMu.Unlock()

	type heldEntry struct {
		index int
		res   *sharedResult
		frame *ResultCall
	}
	var held []heldEntry
	defer func() {
		if len(held) == 0 {
			return
		}
		releaseCtx := context.WithoutCancel(ctx)
		c.egraphMu.Lock()
		var queue collectionQueue
		for _, h := range held {
			q, err := c.decrementIncomingOwnershipLocked(releaseCtx, h.res, nil)
			queue = append(queue, q...)
			rerr = errors.Join(rerr, err)
		}
		releases, err := c.collectUnownedResultsLocked(releaseCtx, queue)
		c.egraphMu.Unlock()
		rerr = errors.Join(rerr, err, runOnReleaseFuncs(releaseCtx, releases))
	}()

	entries = make([]SessionResultEntry, 0, len(ids))
	c.egraphMu.Lock()
	for _, id := range ids {
		res := c.resultsByID[id]
		if res == nil {
			continue
		}
		frame := res.loadResultCall()
		if frame == nil || frame.Type == nil {
			continue
		}
		field, err := resultCallIdentityField(frame)
		if err != nil {
			continue
		}
		entry := SessionResultEntry{
			ResultID:  uint64(id),
			Type:      sessionResultTypeName(frame.Type),
			Field:     field,
			DependsOn: make([]uint64, 0, len(res.deps)),
			Imported:  res.imported,
		}
		for dep := range res.deps {
			entry.DependsOn = append(entry.DependsOn, uint64(dep))
		}
		slices.Sort(entry.DependsOn)
		_, entry.Retained = c.persistedEdgesByResult[id]
		if entry.Retained && !entry.Imported {
			c.incrementIncomingOwnershipLocked(ctx, res)
			held = append(held, heldEntry{index: len(entries), res: res, frame: frame})
		}
		entries = append(entries, entry)
	}
	c.egraphMu.Unlock()
	if hook := c.testAfterSessionResultsHeld; hook != nil {
		hook()
	}

	for _, h := range held {
		dig, err := h.frame.deriveRecipeDigest(c)
		if err != nil {
			slog.Warn("session results: recipe digest not derived", "sessionID", sessionID, "resultID", h.res.id, "err", err)
			continue
		}
		entries[h.index].RecipeDigest = dig
	}
	return entries, context.Cause(ctx)
}

// sessionResultTypeName renders a call type without non-null markers.
func sessionResultTypeName(typ *ResultCallType) string {
	if typ == nil {
		return ""
	}
	if typ.Elem != nil {
		return "[" + sessionResultTypeName(typ.Elem) + "]"
	}
	return typ.NamedType
}

// WithResultsByNumber looks up results by result number, holds every one
// that is present so it cannot be collected, and calls fn with them. found
// is parallel to numbers, with nil where the number is absent, and missing
// lists the absent numbers in order. The holds are released after fn
// returns, and anything that became unowned is collected then.
//
// This is a control operation with no client session: it makes no session
// checks and adds nothing to any session's set.
func (c *Cache) WithResultsByNumber(ctx context.Context, numbers []uint64, fn func(ctx context.Context, found []AnyResult, missing []uint64) error) (rerr error) {
	if fn == nil {
		return fmt.Errorf("results by number: nil consumer")
	}
	op, err := c.beginCacheOperation()
	if err != nil {
		return fmt.Errorf("results by number: %w", err)
	}
	defer op.finish(false)

	var rows []*sharedResult
	defer func() {
		releaseCtx := context.WithoutCancel(ctx)
		c.egraphMu.Lock()
		var queue collectionQueue
		for _, row := range rows {
			q, err := c.decrementIncomingOwnershipLocked(releaseCtx, row, nil)
			queue = append(queue, q...)
			rerr = errors.Join(rerr, err)
		}
		releases, err := c.collectUnownedResultsLocked(releaseCtx, queue)
		c.egraphMu.Unlock()
		rerr = errors.Join(rerr, err, runOnReleaseFuncs(releaseCtx, releases))
	}()

	found := make([]AnyResult, len(numbers))
	var missing []uint64
	c.egraphMu.Lock()
	heldByID := map[sharedResultID]*sharedResult{}
	for i, number := range numbers {
		id := sharedResultID(number)
		row := heldByID[id]
		if row == nil {
			row = c.resultsByID[id]
			if row == nil {
				missing = append(missing, number)
				continue
			}
			c.incrementIncomingOwnershipLocked(ctx, row)
			rows = append(rows, row)
			heldByID[id] = row
		}
		found[i] = Result[Typed]{shared: row}
	}
	c.egraphMu.Unlock()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return fn(ctx, found, missing)
}
