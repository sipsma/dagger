package dagql

import (
	"context"
	"fmt"
	"maps"
	"slices"
)

// StoredRows builds a blob-backed cache's answer to an engine's fetch of rows
// a bundle sent it as identity rows, which something on the engine needs now
// (the remote cache's FetchRows): each entry asked that the cache has a
// stored value of, as a root, with its record, and the entries its record
// references as identity rows, as StoredBundle carries a root. With decode,
// the answer also carries each entry's child rows, the entries its value
// names and its stored parts' owners and services, followed down, as their
// records, so one answer serves a decode of the value; the entries their
// calls reference stay identity rows. With frames, those identity rows are
// frame rows, and so are the rows their calls reference, followed down: the
// entries' whole call closure, each row carrying its call frame and no
// value, so one answer serves a recipe walk. An entry the requesting engine
// (cache) holds an unexpired holding of, which an engine's merged replies and
// rows report, and isn't asked, goes as an identity row, with nothing below
// it walked: the engine has its value. The answer records no holding of what
// it carries. Missing are the entries asked that the cache has no entry for,
// or no value of.
//
// The child rows and the call closure are found a level at a time
// (storedChildClosure, storedFrameClosure), each level read under
// egraphMu.RLock and its call frames decoded after the lock is released; the
// answer is then built as a stored bundle, in one hold over the rows it
// carries.
func (c *Cache) StoredRows(ctx context.Context, cache CacheID, entries []uint64, nowUnix int64, decode, frames bool) (StoredBundleResult, []uint64, error) {
	var out StoredBundleResult
	if !c.blobBacked {
		return out, nil, fmt.Errorf("stored rows: the cache has no blob store")
	}
	if err := context.Cause(ctx); err != nil {
		return out, nil, err
	}
	var full map[sharedResultID]bool
	if decode {
		var err error
		if full, err = c.storedChildClosure(ctx, cache, nowUnix, entries); err != nil {
			return out, nil, err
		}
	}
	var frameRows map[sharedResultID]frameRow
	if frames {
		var err error
		if frameRows, err = c.storedFrameClosure(ctx, cache, nowUnix, entries, full); err != nil {
			return out, nil, err
		}
	}
	out, heldFrames, err := c.storedBundle(entries, nowUnix, full, frameRows)
	if err != nil {
		return StoredBundleResult{}, nil, err
	}
	missing := slices.Concat(out.Gone, out.NoValue)
	out, err = c.finishStoredBundle(out, heldFrames)
	return out, missing, err
}

// frameRow is an entry's call frame as a frame row carries it, naming its
// references by the cache's entry numbers (normalizeTransferCall), and the
// entries it references, sorted.
type frameRow struct {
	call *ResultCall
	refs []sharedResultID
}

// storedFrameClosure returns, for the entries and the rows carried in full
// with them (full), the frame rows of their call closure: each entry their
// call frames reference, followed down through the frames of those, but the
// entries themselves and the full rows, which carry their calls in their
// records, and those the requester holds (requesterHoldsLocked), which have
// their frames there. It reads one level at a time under egraphMu.RLock,
// and decodes the level's frames after releasing it. An entry whose frame
// can't be decoded, or the requester holds, goes as a plain identity row,
// with nothing below it.
func (c *Cache) storedFrameClosure(ctx context.Context, cache CacheID, nowUnix int64, entries []uint64, full map[sharedResultID]bool) (map[sharedResultID]frameRow, error) {
	rows := map[sharedResultID]frameRow{}
	carried := make(map[sharedResultID]bool, len(entries)+len(full))
	seen := map[sharedResultID]bool{}
	var frontier []sharedResultID
	for _, number := range entries {
		carried[sharedResultID(number)] = true
	}
	for id := range full {
		carried[id] = true
	}
	for _, id := range slices.Sorted(maps.Keys(carried)) {
		seen[id] = true
		frontier = append(frontier, id)
	}
	for len(frontier) > 0 {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		level := make([]entryFrame, len(frontier))
		c.egraphMu.RLock()
		for i, id := range frontier {
			// An entry the requester holds has its frame there: it goes as
			// a plain identity row, with nothing below it.
			if res := c.resultsByID[id]; res != nil && (carried[id] || !requesterHoldsLocked(res, cache, nowUnix)) {
				level[i] = res.entryFrameLocked()
			}
		}
		c.egraphMu.RUnlock()
		var next []sharedResultID
		for i, id := range frontier {
			frame, err := level[i].decode()
			if err != nil || frame == nil {
				continue
			}
			call, err := normalizeTransferCall(frame)
			if err != nil {
				continue
			}
			var refs []sharedResultID
			_ = visitResultCallReferences(call, func(ref *PersistedRef) error {
				if ref.RecipeID == nil && ref.ResultID != 0 {
					refs = append(refs, sharedResultID(ref.ResultID))
				}
				return nil
			})
			slices.Sort(refs)
			refs = slices.Compact(refs)
			if !carried[id] {
				rows[id] = frameRow{call: call, refs: refs}
			}
			for _, ref := range refs {
				if !seen[ref] {
					seen[ref] = true
					next = append(next, ref)
				}
			}
		}
		frontier = next
	}
	return rows, nil
}

// frameRowCarriedLocked reports whether a build can carry every row a frame
// row's call references: as an identity row, or with its stored record.
// Requires egraphMu.
func (c *Cache) frameRowCarriedLocked(build *storedBundleBuild, row frameRow) bool {
	for _, ref := range row.refs {
		res := c.resultsByID[ref]
		if res == nil {
			return false
		}
		if !build.identity.heldLocked(res) && (res.noValueLocked() || res.persistedEnvelope == nil) {
			return false
		}
	}
	return true
}

// storedChildClosure returns the child rows of the entries, followed down:
// of each entry with a stored record, what a stored bundle walks below it
// (storedClosureChildrenLocked) but the entries only its call frame
// references. An entry its value names as a child (PersistedRefChild) is a
// child though its call takes it too: a lazy container's parent is its
// receiver and a child of its value. A child the requester holds
// (requesterHoldsLocked) isn't one, nor is anything only it reaches. It
// reads one level at a time under egraphMu.RLock, and decodes the level's
// frames and records after releasing it. An entry whose frame or record
// can't be decoded counts every entry below it as a child.
func (c *Cache) storedChildClosure(ctx context.Context, cache CacheID, nowUnix int64, entries []uint64) (map[sharedResultID]bool, error) {
	type reached struct {
		id       sharedResultID
		children []sharedResultID
		frame    entryFrame
		envelope *PersistedResultEnvelope
		body     *storedBody
	}
	full := map[sharedResultID]bool{}
	seen := make(map[sharedResultID]bool, len(entries))
	var frontier []sharedResultID
	for _, number := range entries {
		id := sharedResultID(number)
		if !seen[id] {
			seen[id] = true
			frontier = append(frontier, id)
		}
	}
	asked := maps.Clone(seen)
	for len(frontier) > 0 {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		level := make([]reached, 0, len(frontier))
		c.egraphMu.RLock()
		for _, id := range frontier {
			res := c.resultsByID[id]
			if res == nil || res.noValueLocked() || res.persistedEnvelope == nil {
				// No record to carry: it goes as an identity row, if
				// anything.
				delete(full, id)
				continue
			}
			if !asked[id] && requesterHoldsLocked(res, cache, nowUnix) {
				// The requester has its value: an identity row names it.
				delete(full, id)
				continue
			}
			level = append(level, reached{id: id, children: storedClosureChildrenLocked(res), frame: res.entryFrameLocked(), envelope: res.persistedEnvelope, body: res.stored})
		}
		c.egraphMu.RUnlock()
		var next []sharedResultID
		for _, r := range level {
			callOnly := map[uint64]bool{}
			if call, err := r.frame.decode(); err == nil && call != nil {
				_ = visitResultCallReferences(call, func(ref *PersistedRef) error {
					if ref.RecipeID == nil && ref.ResultID != 0 {
						callOnly[ref.ResultID] = true
					}
					return nil
				})
				if len(callOnly) > 0 {
					valueChildren, err := storedValueChildren(r.id, call, r.envelope, r.body)
					if err != nil {
						clear(callOnly)
					}
					for _, child := range valueChildren {
						delete(callOnly, child)
					}
				}
			}
			for _, child := range r.children {
				if callOnly[uint64(child)] || seen[child] {
					continue
				}
				seen[child] = true
				full[child] = true
				next = append(next, child)
			}
		}
		frontier = next
	}
	return full, nil
}

// storedValueChildren returns the entries an entry's stored record names as
// its value's children (PersistedRefChild), from its envelope or, held
// compressed, its body, with its decoded call. Neither changes in place, so
// it needs no lock.
func storedValueChildren(id sharedResultID, call *ResultCall, envelope *PersistedResultEnvelope, body *storedBody) ([]uint64, error) {
	rec := PersistedRecord{ResultID: uint64(id), Envelope: *envelope, Call: call}
	if body != nil {
		var err error
		if rec, err = body.record(uint64(id)); err != nil {
			return nil, err
		}
		rec.Call = call
	}
	var children []uint64
	_, err := VisitEncodedReferences(rec, func(ref *PersistedRef) error {
		if ref.Kind == PersistedRefChild && ref.RecipeID == nil && ref.ResultID != 0 && ref.ResultID != uint64(id) {
			children = append(children, ref.ResultID)
		}
		return nil
	})
	return children, err
}

// requesterHoldsLocked reports whether the engine whose cache is cache holds
// an unexpired holding of res, as its merged replies and rows report: it has
// res's value, and a fetch's answer names res by identity alone. An empty
// cache holds nothing. Requires egraphMu.
func requesterHoldsLocked(res *sharedResult, cache CacheID, nowUnix int64) bool {
	if cache == "" {
		return false
	}
	for _, ref := range res.cacheHolderRefsLocked(cache) {
		if ref.h != nil && (ref.h.expiresAtUnix == 0 || ref.h.expiresAtUnix > nowUnix) {
			return true
		}
	}
	return false
}
