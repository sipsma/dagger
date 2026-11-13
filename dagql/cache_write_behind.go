package dagql

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
)

// A blob-backed cache, the Cloud's, writes its store behind: each write
// (WriteBehind) writes the rows of whatever changed since the last one, as
// the save's marks name it (cache_change_marks.go), so that the store holds
// what a whole save of the cache would write, without a copy of the whole
// cache and without rewriting it. A write is one transaction, so a crash
// keeps the last write whole.
//
// The rows fall in three kinds, each written by its own number:
//   - an entry's, while it is saved: its result row, dependencies, output
//     classes, kept calls and persisted edge. An entry is saved while a
//     stored root reaches it, the closure a whole save selects
//     (snapshotPersistedRootClosureLocked). The writer counts each entry's
//     stored owners, its edge and each saved entry that owns it, and moves
//     the counts by what changed in the marked entries' edges and children,
//     so it knows the closure without walking it. A change of an entry's
//     last use alone updates that field.
//   - a term's, while its output class is retained: the term and its inputs,
//     named by their classes' roots. A class is retained while a saved
//     entry's output is in it.
//   - a class's, by its root: the class and its digests, while it is
//     retained or a written term takes it as input.
//
// The writer keeps, for each class, the saved entries whose outputs name it
// and the written terms whose rows name it, so whether a class is written
// costs a lookup. Class numbers outlive compactions
// (compactEqClassesModeLocked), so the rows stay right until a mark names
// them again.
//
// A write takes at most a budget of marks (writeBehindBudget), with whatever
// joins or leaves the saved set because of them, and says when marks are
// left; the next write takes them. So that the store restores after any
// write, a write also rewrites every row that names what it changes: the
// stored owners of an entry that leaves, and the saved entries and written
// terms that name a class that is no longer the root it was when they were
// written, joined into another or its number taken by a new class. Every
// row the store holds then names entries it holds and classes that are the
// cache's own roots, as a whole save's rows do; once the marks drain, the
// store holds what a whole save writes.

// writeBehindBudget is how many marked entries and terms one write takes.
// Planning holds the cache's read lock for about 15 µs per changed entry, as
// measured on a whole pull request's store, so a write of a thousand holds
// it for about 15 ms, which writers waiting behind it can bear; a push of
// several thousand entries is written in a few writes, one after the other.
// It counts marks and not time, so what one write takes doesn't depend on
// the machine.
const writeBehindBudget = 1000

// writeBehindState is what the writer behind knows of the store: the counts
// that make the saved closure, and what the store holds of each saved
// entry's outputs, each written term and each written class. It belongs to
// the save: Checkpoint and WriteBehind change it under checkpointMu, and
// read the cache for it under egraphMu.
type writeBehindState struct {
	// edges are the entries whose persisted edge the store holds, and
	// ownedBy, for each entry, the saved entries that own it (children): its
	// stored owners. An entry is saved while it has one, is registered, and
	// isn't invalid.
	edges   map[sharedResultID]struct{}
	ownedBy map[sharedResultID]map[sharedResultID]struct{}
	// children are, for each saved entry, the entries it owns in the store.
	children map[sharedResultID][]sharedResultID
	// outputs are each saved entry's output classes, as the store holds them,
	// and entriesNaming, for each class, the saved entries whose outputs in
	// the store name it; retained are the classes some named, as of the last
	// write.
	outputs       map[sharedResultID][]eqClassID
	entriesNaming map[eqClassID]map[sharedResultID]struct{}
	retained      map[eqClassID]struct{}
	// terms are the written terms, with the classes the store's rows for
	// them name, their inputs and output; termsNaming are, for each class,
	// the written terms whose rows name it.
	terms       map[egraphTermID][]eqClassID
	termsNaming map[eqClassID]map[egraphTermID]struct{}
	// births are, for each class the store's rows name, how often its number
	// had been taken again when they were written (Cache.eqClassReuses): a
	// number taken since names another class.
	births map[eqClassID]uint32
	// classes are the written classes.
	classes map[eqClassID]struct{}
	// unclean are the entries whose attachment opened and hasn't been seen
	// to close cleanly (noteAttachmentOpenedLocked). invalid are the ones it
	// left failed and every entry that owns one of them, which a whole save
	// leaves out (snapshotPersistedRootClosureLocked): none of them is saved.
	unclean map[sharedResultID]struct{}
	invalid map[sharedResultID]struct{}
	// nextResultID is the next result number the store holds.
	nextResultID sharedResultID
	// broken is set when a write failed after the state moved: the next
	// write saves the cache whole, which starts the state afresh.
	broken bool
}

// WriteBehindReport is what one write behind wrote: how many entries,
// classes and terms it wrote or deleted the rows of, and how many entries'
// last use alone it updated. Whole is set when it saved the cache whole
// instead, after a failed write. Waited is set when it wrote nothing because
// a replacement was in flight: what changed waits for the next write. More
// is set when marks are left past the write's budget: the next write takes
// them.
type WriteBehindReport struct {
	Entries int  `json:"entries"`
	Used    int  `json:"used,omitempty"`
	Classes int  `json:"classes"`
	Terms   int  `json:"terms"`
	Whole   bool `json:"whole,omitempty"`
	Waited  bool `json:"waited,omitempty"`
	More    bool `json:"more,omitempty"`
}

// WriteBehind writes to a blob-backed cache's store the rows of whatever
// changed since its last write, in one transaction, so that the store holds
// what a whole save of the cache would write once no marks are left. It
// takes at most a budget of marks, and reports More when some are left. It
// holds egraphMu for reading only while it copies the changed rows.
func (c *Cache) WriteBehind(ctx context.Context) (WriteBehindReport, error) {
	if !c.blobBacked {
		return WriteBehindReport{}, errors.New("write behind: the cache has no blob store")
	}
	op, err := c.beginCacheOperation()
	if err != nil {
		return WriteBehindReport{}, err
	}
	defer op.finish(false)
	if c.sqlDB == nil || c.pdb == nil {
		return WriteBehindReport{}, errors.New("write behind: the cache has no persistence database")
	}
	c.checkpointMu.Lock()
	defer c.checkpointMu.Unlock()
	return c.writeBehind(ctx)
}

// writeBehind is WriteBehind, under checkpointMu.
func (c *Cache) writeBehind(ctx context.Context) (WriteBehindReport, error) {
	if c.behind == nil || c.behind.broken {
		if err := c.persistCurrentState(ctx); err != nil {
			return WriteBehindReport{}, err
		}
		return WriteBehindReport{Whole: true}, nil
	}
	step := c.readLockGraphAs(ctx, "dagql.persist.behind")
	plan, err := c.planWriteBehindLocked()
	c.runlockGraphAs(step)
	if err != nil {
		c.behind.broken = true
		return WriteBehindReport{}, err
	}
	if plan.waited {
		return WriteBehindReport{Waited: true}, nil
	}
	if plan.empty() {
		return WriteBehindReport{More: plan.more}, nil
	}
	for _, rows := range plan.entries {
		if rows == nil {
			continue
		}
		if result := &rows.result; result.frame == nil && !result.stored.holdsFrame() {
			if result.self == nil || result.self.Type() == nil || result.self.Type().Name() != "Query" {
				c.behind.broken = true
				return WriteBehindReport{}, fmt.Errorf("persist result %d: missing result call frame", result.resultID)
			}
		}
		if err := c.encodePersistedResultRow(ctx, &rows.result); err != nil {
			c.behind.broken = true
			return WriteBehindReport{}, err
		}
	}
	if err := c.applyWriteBehindPlan(ctx, plan); err != nil {
		c.behind.broken = true
		return WriteBehindReport{}, err
	}
	b := c.behind
	b.nextResultID = plan.nextResultID
	step = c.lockGraphAs(ctx, "dagql.persist.behind.count")
	c.persistedResults = len(b.outputs)
	c.unlockGraphAs(step)
	return WriteBehindReport{
		Entries: len(plan.entries),
		Used:    len(plan.used),
		Classes: len(plan.classes),
		Terms:   len(plan.terms),
		More:    plan.more,
	}, nil
}

// writeBehindPlan is what one write writes: for each changed entry, class
// and term its rows, nil to delete them; the last uses of entries whose last
// use alone changed; and the next result number.
type writeBehindPlan struct {
	entries      map[sharedResultID]*persistResultRows
	edges        map[sharedResultID]persistdb.MirrorPersistedEdge
	used         map[sharedResultID]int64
	classes      map[eqClassID]*classRows
	terms        map[egraphTermID]*persistTermRows
	nextResultID sharedResultID
	nextChanged  bool
	// waited is set when a replacement in flight made the write wait, and
	// more when marks are left past the budget.
	waited bool
	more   bool
}

func (p *writeBehindPlan) empty() bool {
	return len(p.entries) == 0 && len(p.used) == 0 && len(p.classes) == 0 && len(p.terms) == 0 && !p.nextChanged
}

// classRows are a written class's rows: the class and its digests.
type classRows struct {
	class   persistdb.MirrorEqClass
	digests []persistdb.MirrorEqClassDigest
}

// owned reports whether the store holds an owner of the entry numbered id:
// its persisted edge, or a saved entry that owns it.
func (b *writeBehindState) owned(id sharedResultID) bool {
	_, edge := b.edges[id]
	return edge || len(b.ownedBy[id]) > 0
}

// savedLocked reports whether the entry numbered id is saved: a stored root
// reaches it through entries that are all clean. Requires egraphMu for
// reading.
func (c *Cache) savedLocked(b *writeBehindState, id sharedResultID) bool {
	if _, invalid := b.invalid[id]; invalid {
		return false
	}
	return b.owned(id) && c.resultsByID[id] != nil
}

// settleUncleanLocked brings the unclean and invalid entries up to date, and
// returns the entries whose being invalid turned, which the counts must look
// at again. It reports open when an attachment is still open: the write
// waits for it, as a whole save would leave out the entry and every entry
// that owns it only until it closes. Requires egraphMu for reading.
func (c *Cache) settleUncleanLocked(b *writeBehindState) (turned []sharedResultID, open bool) {
	if c.changes != nil {
		for _, id := range c.changes.opened {
			b.unclean[id] = struct{}{}
		}
		c.changes.opened = nil
	}
	var failed []sharedResultID
	for id := range b.unclean {
		res := c.resultsByID[id]
		if res == nil {
			delete(b.unclean, id)
			continue
		}
		switch res.attachmentState() {
		case resultAttachmentOpen:
			open = true
		case resultAttachmentClean:
			// Closing cleanly changes what the store keeps only for an
			// entry the closure left out, which the invalid set's change
			// below names.
			delete(b.unclean, id)
		default:
			failed = append(failed, id)
		}
	}
	if open {
		return nil, true
	}
	invalid := c.ownersOfLocked(failed)
	for id := range invalid {
		if _, was := b.invalid[id]; !was {
			turned = append(turned, id)
		}
	}
	for id := range b.invalid {
		if _, is := invalid[id]; !is {
			turned = append(turned, id)
		}
	}
	b.invalid = invalid
	return turned, false
}

// ownersOfLocked returns the entries numbered ids and every entry that
// depends on one of them, transitively. Requires egraphMu for reading.
func (c *Cache) ownersOfLocked(ids []sharedResultID) map[sharedResultID]struct{} {
	out := map[sharedResultID]struct{}{}
	queue := slices.Clone(ids)
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if _, seen := out[id]; seen {
			continue
		}
		out[id] = struct{}{}
		if res := c.resultsByID[id]; res != nil && res.depParents != nil {
			for parent := range res.depParents.Items() {
				queue = append(queue, parent)
			}
		}
	}
	return out
}

// countOwnersLocked brings the owners up to date for entry id: its edge, and
// the children it owns while it is saved. It returns the entries to look at
// again: those whose being saved may have turned, an entry no longer saved
// that lost an owner, and, when id isn't saved, its stored owners, whose
// rows name it and are written again in the same write. It also reports
// whether id gained a child while it had a stored owner when the write
// began (owned). Requires egraphMu for reading.
func (c *Cache) countOwnersLocked(b *writeBehindState, id sharedResultID, owned ownedAtStart) (turned []sharedResultID, gained bool) {
	res := c.resultsByID[id]
	if _, edge := c.persistedEdgesByResult[id]; edge && res != nil {
		b.edges[id] = struct{}{}
	} else {
		delete(b.edges, id)
	}
	saved := c.savedLocked(b, id)
	var want []sharedResultID
	if saved {
		for child := range c.ownedResultIDsLocked(res) {
			want = append(want, child)
		}
	} else {
		for owner := range b.ownedBy[id] {
			turned = append(turned, owner)
		}
	}
	had := b.children[id]
	wanted := make(map[sharedResultID]struct{}, len(want))
	for _, child := range want {
		wanted[child] = struct{}{}
	}
	for _, child := range had {
		if _, kept := wanted[child]; kept {
			delete(wanted, child)
			continue
		}
		owned.note(b, child)
		before := b.owned(child)
		owners := b.ownedBy[child]
		delete(owners, id)
		if len(owners) == 0 {
			delete(b.ownedBy, child)
		}
		if before != b.owned(child) || !c.savedLocked(b, child) {
			turned = append(turned, child)
		}
	}
	gained = len(wanted) > 0 && owned.had(b, id)
	for child := range wanted {
		owned.note(b, child)
		before := b.owned(child)
		owners := b.ownedBy[child]
		if owners == nil {
			owners = map[sharedResultID]struct{}{}
			b.ownedBy[child] = owners
		}
		owners[id] = struct{}{}
		if !before {
			turned = append(turned, child)
		}
	}
	if len(want) == 0 {
		delete(b.children, id)
	} else {
		b.children[id] = want
	}
	return turned, gained
}

// ownedAtStart records, for the entries whose stored owners a write changes,
// whether they had any when the write began.
type ownedAtStart map[sharedResultID]bool

// note records whether the entry numbered id has a stored owner, the first
// time the write changes its owners.
func (o ownedAtStart) note(b *writeBehindState, id sharedResultID) {
	if _, noted := o[id]; !noted {
		o[id] = len(b.ownedBy[id]) > 0
	}
}

// had reports whether the entry numbered id had a stored owner when the
// write began.
func (o ownedAtStart) had(b *writeBehindState, id sharedResultID) bool {
	if had, noted := o[id]; noted {
		return had
	}
	return len(b.ownedBy[id]) > 0
}

// classTermsLocked returns the terms whose output is the class rooted at
// root, sorted. Requires egraphMu for reading.
func (c *Cache) classTermsLocked(root eqClassID) []egraphTermID {
	var ids []egraphTermID
	for termID := range c.outputEqClassToTerms[root] {
		term := c.egraphTerms[termID]
		if term == nil || c.eqClassRootLocked(term.outputEqID) != root {
			continue
		}
		ids = append(ids, termID)
	}
	slices.Sort(ids)
	return ids
}

// retainedRootLocked reports whether eqID is a root that is retained: a
// saved entry's output, as the store holds it, is in it. Requires egraphMu
// for reading.
func (c *Cache) retainedRootLocked(b *writeBehindState, eqID eqClassID) bool {
	return eqID != 0 && len(b.entriesNaming[eqID]) > 0 && c.eqClassRootLocked(eqID) == eqID
}

// named reports whether a row the store holds names the class eqID.
func (b *writeBehindState) named(eqID eqClassID) bool {
	return len(b.entriesNaming[eqID]) > 0 || len(b.termsNaming[eqID]) > 0
}

// moveNames moves key, whose rows named the classes old, to name the classes
// new instead, and adds every class it moved to touched.
func moveNames[K comparable](names map[eqClassID]map[K]struct{}, key K, old, new []eqClassID, touched map[eqClassID]struct{}) {
	for _, eqID := range old {
		if eqID == 0 {
			continue
		}
		if set := names[eqID]; set != nil {
			delete(set, key)
			if len(set) == 0 {
				delete(names, eqID)
			}
		}
		touched[eqID] = struct{}{}
	}
	for _, eqID := range new {
		if eqID == 0 {
			continue
		}
		set := names[eqID]
		if set == nil {
			set = map[K]struct{}{}
			names[eqID] = set
		}
		set[key] = struct{}{}
		touched[eqID] = struct{}{}
	}
}

// takeMarks takes the distinct marks from the front of marks, at most n of
// them, and returns the marks left.
func takeMarks[K comparable](marks []K, n int) (map[K]struct{}, []K) {
	taken := map[K]struct{}{}
	i := 0
	for ; i < len(marks); i++ {
		if _, dup := taken[marks[i]]; !dup && len(taken) >= n {
			break
		}
		taken[marks[i]] = struct{}{}
	}
	return taken, marks[i:]
}

// planWriteBehindLocked takes the save's marks, at most a budget of marked
// entries and terms, and plans the write, moving the writer's state as it
// goes: the rows that name a class that is no longer theirs, then the
// counts of the closure, then the changed entries' rows and the classes
// their outputs name, then the terms marked or whose output class turned
// retained or not, then the classes those name. Requires egraphMu for
// reading; only the save changes its marks under it. A failed write leaves
// the state broken, and the next saves whole.
//
//nolint:gocyclo // one step per kind of row, in the order each depends on the last; splitting hides the order of the steps
func (c *Cache) planWriteBehindLocked() (*writeBehindPlan, error) {
	b := c.behind
	plan := &writeBehindPlan{
		entries:      map[sharedResultID]*persistResultRows{},
		edges:        map[sharedResultID]persistdb.MirrorPersistedEdge{},
		used:         map[sharedResultID]int64{},
		classes:      map[eqClassID]*classRows{},
		terms:        map[egraphTermID]*persistTermRows{},
		nextResultID: c.nextSharedResultID,
		nextChanged:  c.nextSharedResultID != b.nextResultID,
	}
	turnedInvalid, open := c.settleUncleanLocked(b)
	if open {
		// The marks wait for the next write.
		plan.nextChanged, plan.waited = false, true
		return plan, nil
	}
	used := c.usedMarks.take()
	budget := writeBehindBudget
	if c.testWriteBehindBudget > 0 {
		budget = c.testWriteBehindBudget
	}
	pending := map[sharedResultID]struct{}{}
	terms := map[egraphTermID]struct{}{}
	var markedClasses []eqClassID
	if c.changes != nil {
		var left []sharedResultID
		pending, left = takeMarks(c.changes.saveEntries, budget)
		var leftTerms []egraphTermID
		terms, leftTerms = takeMarks(c.changes.saveTerms, budget-len(pending))
		markedClasses = c.changes.saveClasses
		c.changes.saveEntries, c.changes.saveTerms, c.changes.saveClasses = left, leftTerms, nil
		plan.more = len(left) > 0 || len(leftTerms) > 0
	}
	for _, id := range turnedInvalid {
		pending[id] = struct{}{}
	}

	// The rows that name a marked class that is no longer theirs, joined
	// into another class or its number taken by a new one, are written
	// again, however many: no row the store holds names a class that isn't
	// the cache's root.
	classes := map[eqClassID]struct{}{}
	for _, eqID := range markedClasses {
		if !b.named(eqID) {
			continue
		}
		if c.eqClassRootLocked(eqID) == eqID && c.eqClassReuses[eqID] == b.births[eqID] {
			continue
		}
		for id := range b.entriesNaming[eqID] {
			pending[id] = struct{}{}
		}
		for termID := range b.termsNaming[eqID] {
			terms[termID] = struct{}{}
		}
		classes[eqID] = struct{}{}
	}

	// The counts: each entry once, and each entry whose being saved turns,
	// again each time it turns, until none turns.
	//
	// The rows of marks left for later are older than the rows this write
	// writes, so the two could name a dependency cycle no cache ever had: an
	// entry gains a child that reaches, through older rows, an owner that
	// names the entry. So when an entry that had a stored owner as the write
	// began gains a child, the write takes every mark left, as a write
	// without a budget does. Without that, the store's dependencies stay
	// acyclic. Every cycle in it holds an older row, since this write's rows
	// all come from one look at the cache; past an older row comes an entry
	// that had a stored owner as the write began, so it gained no child and
	// each row it names was the store's already; and so on, entry by entry,
	// around the cycle, which would then have been in the store before.
	changed := map[sharedResultID]struct{}{}
	owned := ownedAtStart{}
	for len(pending) > 0 {
		for id := range pending {
			delete(pending, id)
			changed[id] = struct{}{}
			turned, gained := c.countOwnersLocked(b, id, owned)
			for _, more := range turned {
				pending[more] = struct{}{}
			}
			if gained && plan.more {
				for _, more := range c.changes.saveEntries {
					pending[more] = struct{}{}
				}
				for _, more := range c.changes.saveTerms {
					terms[more] = struct{}{}
				}
				c.changes.saveEntries, c.changes.saveTerms, plan.more = nil, nil, false
			}
		}
	}

	// The entries: a changed one's rows, and the classes its outputs name
	// in the store, before and after.
	saved := func(id sharedResultID) bool { return c.savedLocked(b, id) }
	for _, id := range slices.Sorted(maps.Keys(changed)) {
		var outputs []eqClassID
		if saved(id) {
			rows, err := c.persistResultRowsLocked(c.resultsByID[id], saved)
			if err != nil {
				return nil, err
			}
			// Non-nil, so the entry counts as held with no outputs too.
			outputs = make([]eqClassID, 0, len(rows.outputs))
			for _, row := range rows.outputs {
				outputs = append(outputs, eqClassID(row.EqClassID))
			}
			plan.entries[id] = &rows
			if _, edge := c.persistedEdgesByResult[id]; edge {
				plan.edges[id] = c.persistEdgeRowLocked(id)
			}
		} else if _, held := b.outputs[id]; held {
			plan.entries[id] = nil
		}
		moveNames(b.entriesNaming, id, b.outputs[id], outputs, classes)
		if outputs == nil {
			delete(b.outputs, id)
		} else {
			b.outputs[id] = outputs
		}
	}
	// A last use alone: the store's row has everything else already.
	for _, id := range used {
		if _, rewritten := changed[id]; rewritten || !saved(id) {
			continue
		}
		plan.used[id] = c.resultsByID[id].loadPayloadState().lastUsedAtUnixNano
	}

	// The classes whose being retained turned: their terms are written or
	// deleted with them.
	for _, eqID := range markedClasses {
		classes[eqID] = struct{}{}
		if root := c.eqClassRootLocked(eqID); root != 0 {
			classes[root] = struct{}{}
		}
	}
	for eqID := range classes {
		now := c.retainedRootLocked(b, eqID)
		if _, was := b.retained[eqID]; was == now {
			continue
		}
		if now {
			b.retained[eqID] = struct{}{}
		} else {
			delete(b.retained, eqID)
		}
		for _, termID := range c.classTermsLocked(eqID) {
			terms[termID] = struct{}{}
		}
		for termID := range c.outputEqClassToTerms[eqID] {
			terms[termID] = struct{}{}
		}
	}

	// The terms: each written while its output class is retained, naming
	// that class and the classes it takes as input.
	for _, termID := range slices.Sorted(maps.Keys(terms)) {
		var named []eqClassID
		term := c.egraphTerms[termID]
		written := term != nil && c.retainedRootLocked(b, c.eqClassRootLocked(term.outputEqID))
		if written {
			rows, err := c.persistTermRowsLocked(termID, term)
			if err != nil {
				return nil, err
			}
			named = append(named, eqClassID(rows.term.OutputEqClassID))
			for _, input := range rows.inputs {
				if eqID := eqClassID(input.InputEqClassID); !slices.Contains(named, eqID) {
					named = append(named, eqID)
				}
			}
			plan.terms[termID] = &rows
		} else if _, held := b.terms[termID]; held {
			plan.terms[termID] = nil
		}
		moveNames(b.termsNaming, termID, b.terms[termID], named, classes)
		if written {
			b.terms[termID] = named
		} else {
			delete(b.terms, termID)
		}
	}

	// The classes: each written while it is a root the store's rows name,
	// as the class its number names now.
	for _, eqID := range slices.Sorted(maps.Keys(classes)) {
		if eqID == 0 {
			continue
		}
		if b.named(eqID) {
			b.births[eqID] = c.eqClassReuses[eqID]
		} else {
			delete(b.births, eqID)
		}
		written := c.eqClassRootLocked(eqID) == eqID && b.named(eqID)
		_, held := b.classes[eqID]
		switch {
		case written:
			rows := &classRows{}
			rows.class, rows.digests = c.persistEqClassRowsLocked(eqID)
			plan.classes[eqID] = rows
			b.classes[eqID] = struct{}{}
		case held:
			plan.classes[eqID] = nil
			delete(b.classes, eqID)
		}
	}
	return plan, nil
}

// applyWriteBehindPlan writes the plan in one transaction: every delete
// first, then every insert.
//
//nolint:gocyclo // one statement per table, deletes then inserts; splitting hides the order of the tables
func (c *Cache) applyWriteBehindPlan(ctx context.Context, plan *writeBehindPlan) (rerr error) {
	tx, err := c.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write behind: begin: %w", err)
	}
	defer func() {
		if rerr != nil {
			_ = tx.Rollback()
		}
	}()
	q := c.pdb.WithTx(tx)
	for _, id := range slices.Sorted(maps.Keys(plan.entries)) {
		if err := q.DeleteMirrorResultRows(ctx, int64(id)); err != nil {
			return fmt.Errorf("write behind: delete result %d: %w", id, err)
		}
	}
	for _, eqID := range slices.Sorted(maps.Keys(plan.classes)) {
		if err := q.DeleteMirrorEqClassRows(ctx, int64(eqID)); err != nil {
			return fmt.Errorf("write behind: delete eq_class %d: %w", eqID, err)
		}
	}
	for _, termID := range slices.Sorted(maps.Keys(plan.terms)) {
		if err := q.DeleteMirrorTermRows(ctx, int64(termID)); err != nil {
			return fmt.Errorf("write behind: delete term %d: %w", termID, err)
		}
	}
	for _, eqID := range slices.Sorted(maps.Keys(plan.classes)) {
		rows := plan.classes[eqID]
		if rows == nil {
			continue
		}
		if err := q.InsertMirrorEqClass(ctx, rows.class); err != nil {
			return fmt.Errorf("write behind: insert eq_class %d: %w", eqID, err)
		}
		for _, row := range rows.digests {
			if err := q.InsertMirrorEqClassDigest(ctx, row); err != nil {
				return fmt.Errorf("write behind: insert eq_class_digest (%d,%s,%s): %w", row.EqClassID, row.Digest, row.Label, err)
			}
		}
	}
	for _, termID := range slices.Sorted(maps.Keys(plan.terms)) {
		term := plan.terms[termID]
		if term == nil {
			continue
		}
		if err := q.InsertMirrorTerm(ctx, term.term); err != nil {
			return fmt.Errorf("write behind: insert term %d: %w", term.term.ID, err)
		}
		for _, input := range term.inputs {
			if err := q.InsertMirrorTermInput(ctx, input); err != nil {
				return fmt.Errorf("write behind: insert term_input (%d,%d): %w", input.TermID, input.Position, err)
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(plan.entries)) {
		rows := plan.entries[id]
		if rows == nil {
			continue
		}
		if err := q.InsertMirrorResult(ctx, rows.result.row); err != nil {
			return fmt.Errorf("write behind: insert result %d: %w", id, err)
		}
		for _, row := range rows.result.resultDeps {
			if err := q.InsertMirrorResultDep(ctx, row); err != nil {
				return fmt.Errorf("write behind: insert result_dep (%d,%d): %w", row.ParentResultID, row.DepResultID, err)
			}
		}
		for _, row := range rows.outputs {
			if err := q.InsertMirrorResultOutputEqClass(ctx, row); err != nil {
				return fmt.Errorf("write behind: insert result_output_eq_class (%d,%d): %w", row.ResultID, row.EqClassID, err)
			}
		}
		for _, row := range rows.calls {
			if err := q.InsertMirrorResultCall(ctx, row); err != nil {
				return fmt.Errorf("write behind: insert result_call (%d,%s): %w", row.ResultID, row.RequestDigest, err)
			}
		}
		for _, row := range rows.result.resultSnapshotLinks {
			if err := q.InsertMirrorResultSnapshotLink(ctx, row); err != nil {
				return fmt.Errorf("write behind: insert result_snapshot_link (%d,%s,%s): %w", row.ResultID, row.RefKey, row.Role, err)
			}
		}
		if edge, ok := plan.edges[id]; ok {
			if err := q.InsertMirrorPersistedEdge(ctx, edge); err != nil {
				return fmt.Errorf("write behind: insert persisted_edge (%d): %w", id, err)
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(plan.used)) {
		if err := q.UpdateMirrorResultLastUsed(ctx, int64(id), plan.used[id]); err != nil {
			return fmt.Errorf("write behind: update result %d last use: %w", id, err)
		}
	}
	if plan.nextChanged || len(plan.entries) > 0 {
		if err := q.UpsertMeta(ctx, persistdb.MetaKeyNextResultID, strconv.FormatUint(uint64(plan.nextResultID), 10)); err != nil {
			return fmt.Errorf("write behind: save next result ID: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("write behind: commit: %w", err)
	}
	return nil
}

// newWriteBehindStateLocked is the writer's state for a store that holds
// what a whole save of the cache writes now: the counts of the closure
// selected, the entries it left out as invalid, and the outputs, terms and
// classes that save writes. Requires egraphMu for reading.
func (c *Cache) newWriteBehindStateLocked(selected, roots, invalid map[sharedResultID]struct{}) *writeBehindState {
	b := &writeBehindState{
		edges:         map[sharedResultID]struct{}{},
		ownedBy:       map[sharedResultID]map[sharedResultID]struct{}{},
		children:      map[sharedResultID][]sharedResultID{},
		outputs:       map[sharedResultID][]eqClassID{},
		entriesNaming: map[eqClassID]map[sharedResultID]struct{}{},
		retained:      map[eqClassID]struct{}{},
		terms:         map[egraphTermID][]eqClassID{},
		termsNaming:   map[eqClassID]map[egraphTermID]struct{}{},
		births:        map[eqClassID]uint32{},
		classes:       map[eqClassID]struct{}{},
		unclean:       map[sharedResultID]struct{}{},
		invalid:       map[sharedResultID]struct{}{},
		nextResultID:  c.nextSharedResultID,
	}
	// The closure left out each entry whose attachment was open or failed,
	// and every entry that owns one: they start out of the saved set, as the
	// closure had them, whatever closed since. The next write sees each
	// attachment as it is then, and brings back what one that closed cleanly
	// left out (settleUncleanLocked).
	for id := range invalid {
		b.invalid[id] = struct{}{}
		if res := c.resultsByID[id]; res != nil && res.attachmentState() != resultAttachmentClean {
			b.unclean[id] = struct{}{}
		}
	}
	for id := range roots {
		b.edges[id] = struct{}{}
	}
	touched := map[eqClassID]struct{}{}
	for id := range selected {
		res := c.resultsByID[id]
		if res == nil {
			continue
		}
		var children []sharedResultID
		for child := range c.ownedResultIDsLocked(res) {
			children = append(children, child)
			owners := b.ownedBy[child]
			if owners == nil {
				owners = map[sharedResultID]struct{}{}
				b.ownedBy[child] = owners
			}
			owners[id] = struct{}{}
		}
		if len(children) > 0 {
			b.children[id] = children
		}
		var outputs []eqClassID
		for eqID := range c.outputEqClassRootsLocked(id) {
			eqID = c.eqClassRootLocked(eqID)
			if eqID != 0 && !slices.Contains(outputs, eqID) {
				outputs = append(outputs, eqID)
			}
		}
		slices.Sort(outputs)
		b.outputs[id] = outputs
		moveNames(b.entriesNaming, id, nil, outputs, touched)
	}
	for root := range b.entriesNaming {
		b.retained[root] = struct{}{}
		for _, termID := range c.classTermsLocked(root) {
			term := c.egraphTerms[termID]
			named := []eqClassID{root}
			for _, input := range term.inputEqIDs {
				if input = c.eqClassRootLocked(input); !slices.Contains(named, input) {
					named = append(named, input)
				}
			}
			b.terms[termID] = named
			moveNames(b.termsNaming, termID, nil, named, touched)
		}
	}
	for eqID := range touched {
		if b.named(eqID) {
			b.classes[eqID] = struct{}{}
			b.births[eqID] = c.eqClassReuses[eqID]
		}
	}
	return b
}

// resetWriteBehindLocked starts the writer's state afresh from the cache,
// for a store that holds what a whole save of it writes now, as after a
// restore. Requires egraphMu for reading.
func (c *Cache) resetWriteBehindLocked() {
	if !c.blobBacked || c.sqlDB == nil {
		return
	}
	selected, roots, invalid := c.persistedRootClosureLocked()
	c.behind = c.newWriteBehindStateLocked(selected, roots, invalid)
}
