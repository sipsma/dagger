package dagql

import (
	"context"
	"fmt"
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
// calls reference stay identity rows. Missing are the entries asked that the
// cache has no entry for, or no value of.
//
// The child rows are found a level at a time (storedChildClosure), each level
// read under egraphMu.RLock and its call frames decoded after the lock is
// released; the answer is then built as a stored bundle, in one hold over the
// rows it carries.
func (c *Cache) StoredRows(ctx context.Context, entries []uint64, nowUnix int64, decode bool) (StoredBundleResult, []uint64, error) {
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
		if full, err = c.storedChildClosure(ctx, entries); err != nil {
			return out, nil, err
		}
	}
	out, frames, err := c.storedBundle(entries, nowUnix, full)
	if err != nil {
		return StoredBundleResult{}, nil, err
	}
	missing := slices.Concat(out.Gone, out.NoValue)
	out, err = c.finishStoredBundle(out, frames)
	return out, missing, err
}

// storedChildClosure returns the child rows of the entries, followed down:
// of each entry with a stored record, what a stored bundle walks below it
// (storedClosureChildrenLocked) but the entries its call frame references.
// It reads one level at a time under egraphMu.RLock, and decodes the level's
// frames after releasing it. An entry whose frame can't be decoded counts
// every entry below it as a child.
func (c *Cache) storedChildClosure(ctx context.Context, entries []uint64) (map[sharedResultID]bool, error) {
	type reached struct {
		children []sharedResultID
		frame    entryFrame
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
			level = append(level, reached{children: storedClosureChildrenLocked(res), frame: res.entryFrameLocked()})
		}
		c.egraphMu.RUnlock()
		var next []sharedResultID
		for _, r := range level {
			calls := map[uint64]bool{}
			if call, err := r.frame.decode(); err == nil && call != nil {
				_ = visitResultCallReferences(call, func(ref *PersistedRef) error {
					if ref.RecipeID == nil && ref.ResultID != 0 {
						calls[ref.ResultID] = true
					}
					return nil
				})
			}
			for _, child := range r.children {
				if calls[uint64(child)] || seen[child] {
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
