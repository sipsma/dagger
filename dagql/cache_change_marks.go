package dagql

import (
	"cmp"
	"slices"
	"strings"
	"sync"
)

// changeMarks are the holdings and entries a blob-backed cache's writes have
// changed since the last write handed them back (takeChangesLocked), for its
// reader to ask again of only those (RemoteChange.Holdings and Entries).
//
// An entry is marked where anything a reader asks of it changes. What an
// entry's class mates answer changes too when the entry's value, record,
// stored parts or expiry change, or its class gains it: their part answers
// consider it (AvailablePart), and their classes' entries include it. Those
// marks (markEntryClassLocked) are widened to every entry of the entry's
// classes when the marks are handed back. Its holdings and recipes are its
// own (markEntryLocked). Its holdings would reach its class mates only by
// turning whether it has a value (noValueLocked), which never happens to an
// entry that stays: an entry with no value is owned by its holdings alone,
// as a prune drops only values that holdings alone keep and a value depends
// only on entries with values, so it goes with its last engine holding
// (markLeavingLocked); and one gains its first holding only as it is
// created, which marks its class. Classes only grow
// until an entry is removed, so widening then reaches every entry whose
// class the change touched, but for a removed entry's: its class mates are
// marked as it leaves its classes (markLeavingLocked), while the classes
// still name them.
//
// A holding is marked where any field of its description
// (RemoteHoldingInfo) changes, and where it is created or removed.
//
// Marks are appended, an entry or holding as often as it changes, and sorted
// and deduplicated once, when handed back: a write's marks cost an append
// each. Only a blob-backed cache marks: an engine's own cache has no reader
// to tell, and its changes is nil. Guarded by egraphMu.
//
// Each mark has a reason (markReason): selection's, for the reader a write
// hands its marks back to, or the save's, for the writer behind
// (WriteBehind), which takes them across writes. The save's marks name the
// entries whose saved rows may differ (saveEntries) and the classes whose
// identity rows may differ (saveClasses); last uses, which change under the
// read lock, wait apart (usedMarks).
type changeMarks struct {
	saveEntries []sharedResultID
	saveClasses []eqClassID
	saveTerms   []egraphTermID
	// opened are the entries whose attachment opened (attachDepsWaitCh), for
	// the save: until it closes cleanly, a whole save leaves them out.
	opened []sharedResultID

	// entries are marked as they are, and classes widened to their classes'
	// entries when handed back.
	entries  []sharedResultID
	classes  []sharedResultID
	holdings []HolderKey
	// left are the classes whose entries a leaving entry has marked, so
	// collecting a whole class walks it once, not once per entry.
	left map[eqClassID]struct{}
}

// markReason says whom a mark is for: the reader a write hands its marks
// back to (markSelection), the writer behind (markSave), or both.
type markReason uint8

const (
	markSelection markReason = 1 << iota
	markSave
	markBoth = markSelection | markSave
)

// markEntryLocked marks the entry numbered id: its own facts or holdings
// changed. Requires egraphMu for writing.
func (c *Cache) markEntryLocked(id sharedResultID, why markReason) {
	if c.changes == nil || id == 0 {
		return
	}
	if why&markSelection != 0 {
		c.changes.entries = append(c.changes.entries, id)
	}
	if why&markSave != 0 {
		c.changes.saveEntries = append(c.changes.saveEntries, id)
	}
}

// markEntryClassLocked marks the entry numbered id and, when the marks are
// handed back, every entry of its classes: its value, record, stored parts
// or expiry changed, or it joined their class. For the save, it marks the
// entry alone. Requires egraphMu for writing.
func (c *Cache) markEntryClassLocked(id sharedResultID, why markReason) {
	if c.changes == nil || id == 0 {
		return
	}
	if why&markSelection != 0 {
		c.changes.classes = append(c.changes.classes, id)
	}
	if why&markSave != 0 {
		c.changes.saveEntries = append(c.changes.saveEntries, id)
	}
}

// markSavedClassLocked marks, for the save, the class numbered id: its
// digests, its terms, its members or whether it is a root may have changed.
// Requires egraphMu for writing.
func (c *Cache) markSavedClassLocked(id eqClassID) {
	if c.changes == nil || id == 0 {
		return
	}
	c.changes.saveClasses = append(c.changes.saveClasses, id)
}

// noteAttachmentOpenedLocked notes, for the save, that the attachment of the
// entry numbered id opened: until it closes cleanly, a whole save leaves the
// entry, and every saved entry that owns it, out
// (snapshotPersistedRootClosureLocked). Requires egraphMu for writing.
func (c *Cache) noteAttachmentOpenedLocked(id sharedResultID) {
	if c.changes == nil || id == 0 {
		return
	}
	c.changes.opened = append(c.changes.opened, id)
}

// markSavedTermLocked marks term for the save: its row, or whether it is
// written, may have changed. Requires egraphMu for writing.
func (c *Cache) markSavedTermLocked(term *egraphTerm) {
	if term == nil {
		return
	}
	c.markSavedTermIDLocked(term.id)
}

// markSavedTermIDLocked marks the term numbered id for the save. Requires
// egraphMu for writing.
func (c *Cache) markSavedTermIDLocked(id egraphTermID) {
	if c.changes == nil || id == 0 {
		return
	}
	c.changes.saveTerms = append(c.changes.saveTerms, id)
}

// markLeavingLocked marks an entry that is leaving its classes, before it
// leaves them: the entry, and every other entry of its classes, whose part
// answers it may have served. Requires egraphMu for writing.
func (c *Cache) markLeavingLocked(id sharedResultID) {
	if c.changes == nil {
		return
	}
	c.changes.entries = append(c.changes.entries, id)
	c.changes.saveEntries = append(c.changes.saveEntries, id)
	for eqID := range c.resultOutputEqClasses[id] {
		c.changes.saveClasses = append(c.changes.saveClasses, eqID)
	}
	for eqID := range c.resultOutputEqClasses[id] {
		root := c.eqClassRootLocked(eqID)
		if root == 0 {
			continue
		}
		if _, ok := c.changes.left[root]; ok {
			continue
		}
		if c.changes.left == nil {
			c.changes.left = map[eqClassID]struct{}{}
		}
		c.changes.left[root] = struct{}{}
		for member := range c.outputEqClassResults[root] {
			c.changes.entries = append(c.changes.entries, member)
		}
	}
}

// markHoldingLocked marks the holding key. Requires egraphMu for writing.
func (c *Cache) markHoldingLocked(key HolderKey) {
	if c.changes == nil {
		return
	}
	c.changes.holdings = append(c.changes.holdings, key)
}

// takeChangesLocked hands the marks back in change, sorted, and clears them:
// each marked holding and entry, and every entry of each class-marked
// entry's classes. A write takes them at its end, under the hold that made
// them, so each is handed back by the write that made it. Requires egraphMu
// for writing.
func (c *Cache) takeChangesLocked(change *RemoteChange) {
	if !c.changesPendingLocked() {
		return
	}
	marks := c.changes
	change.Entries = slices.Grow(change.Entries, len(marks.entries)+len(marks.classes))
	for _, id := range marks.entries {
		change.Entries = append(change.Entries, uint64(id))
	}
	var widened map[eqClassID]struct{}
	for _, id := range marks.classes {
		change.Entries = append(change.Entries, uint64(id))
		for eqID := range c.resultOutputEqClasses[id] {
			root := c.eqClassRootLocked(eqID)
			if root == 0 {
				continue
			}
			if _, ok := widened[root]; ok {
				continue
			}
			if widened == nil {
				widened = map[eqClassID]struct{}{}
			}
			widened[root] = struct{}{}
			for member := range c.outputEqClassResults[root] {
				change.Entries = append(change.Entries, uint64(member))
			}
		}
	}
	slices.Sort(change.Entries)
	change.Entries = slices.Compact(change.Entries)
	change.Holdings = append(change.Holdings, marks.holdings...)
	slices.SortFunc(change.Holdings, compareChangedHoldings)
	change.Holdings = slices.CompactFunc(change.Holdings, func(a, b HolderKey) bool { return a == b })
	marks.entries, marks.classes, marks.holdings = marks.entries[:0], marks.classes[:0], marks.holdings[:0]
	marks.left = nil
}

// compareChangedHoldings orders a change's holdings: by number, then cache.
// Most keys differ in their numbers, and an integer compares faster than a
// cache's name.
func compareChangedHoldings(a, b HolderKey) int {
	return cmp.Or(cmp.Compare(a.Number, b.Number), strings.Compare(string(a.Cache), string(b.Cache)))
}

// clearChangesLocked drops every mark, for a cache that has just opened: no
// reader keeps answers from before an open. Requires egraphMu for writing.
func (c *Cache) clearChangesLocked() {
	if c.changes != nil {
		*c.changes = changeMarks{}
	}
	c.usedMarks.take()
}

// changesPendingLocked reports whether any mark waits to be handed back.
// Requires egraphMu.
func (c *Cache) changesPendingLocked() bool {
	return c.changes != nil && len(c.changes.entries)+len(c.changes.classes)+len(c.changes.holdings) > 0
}

// usedMarks are entries whose last use changed, for the writer behind. A
// last use changes under the read lock (touchLastUsed), so they are guarded
// by their own mutex, not egraphMu. Only a blob-backed cache marks them.
type usedMarks struct {
	mu  sync.Mutex
	on  bool
	ids []sharedResultID
}

// mark marks the entry numbered id.
func (m *usedMarks) mark(id sharedResultID) {
	m.mu.Lock()
	if m.on && id != 0 {
		m.ids = append(m.ids, id)
	}
	m.mu.Unlock()
}

// take returns the marked entries and clears them.
func (m *usedMarks) take() []sharedResultID {
	m.mu.Lock()
	ids := m.ids
	m.ids = nil
	m.mu.Unlock()
	return ids
}
