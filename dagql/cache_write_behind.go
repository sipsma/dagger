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
// The rows fall in two kinds:
//   - an entry's, while it is saved: its result row, dependencies, output
//     classes, kept calls and persisted edge. An entry is saved while a
//     stored root reaches it, the closure a whole save selects
//     (snapshotPersistedRootClosureLocked). The writer counts each entry's
//     stored owners, its edge and each saved entry that owns it, and moves
//     the counts by what changed in the marked entries' edges and children,
//     so it knows the closure without walking it.
//   - a class's, by its root: the class and its digests while it is written,
//     and the terms whose output it is while it is retained. A class is
//     retained while a saved entry's output is in it, and written while it
//     is retained or a retained class's term takes it as input. Class numbers
//     outlive compactions (compactEqClassesModeLocked), so a class's rows
//     stay right until a mark names it again.

// writeBehindState is what the writer behind knows of the store: the counts
// that make the saved closure, each saved entry's output classes in the
// store, and each written class's state there. It belongs to the save:
// Checkpoint and WriteBehind change it under checkpointMu, and read the
// cache for it under egraphMu.
type writeBehindState struct {
	// owners counts each entry's stored owners: its persisted edge, and each
	// saved entry that owns it (children). An entry is saved while it has
	// one and is registered.
	owners map[sharedResultID]int32
	// edges are the entries whose edge owners counts.
	edges map[sharedResultID]struct{}
	// children are, for each saved entry, the entries it counts as owner of.
	children map[sharedResultID][]sharedResultID
	// outputs are each saved entry's output classes, as the store holds them.
	outputs map[sharedResultID][]eqClassID
	// classes are the written classes, as the store holds them.
	classes map[eqClassID]writtenClass
	// unclean are the entries whose attachment opened and hasn't been seen
	// to close cleanly (noteAttachmentOpenedLocked). invalid are the ones it
	// left failed and every entry that owns one of them, which a whole save
	// leaves out (snapshotPersistedRootClosureLocked): none of them is saved.
	unclean map[sharedResultID]struct{}
	invalid map[sharedResultID]struct{}
	// nextResultID is the next result number the store holds.
	nextResultID sharedResultID
	// broken is set when a write failed after the counts moved: the next
	// write saves the cache whole, which starts the state afresh.
	broken bool
}

// writtenClass is a written class's state in the store: whether it is
// retained, and the terms the store holds with it as their output.
type writtenClass struct {
	retained bool
	terms    []egraphTermID
}

// WriteBehindReport is what one write behind wrote: how many entries,
// classes and terms it wrote or deleted the rows of. Whole is set when it
// saved the cache whole instead, after a failed write. Waited is set when it
// wrote nothing because a replacement was in flight: what changed waits for
// the next write.
type WriteBehindReport struct {
	Entries int  `json:"entries"`
	// Used is how many entries' last use alone it updated.
	Used    int  `json:"used,omitempty"`
	Classes int  `json:"classes"`
	Terms   int  `json:"terms"`
	Whole   bool `json:"whole,omitempty"`
	Waited  bool `json:"waited,omitempty"`
}

// WriteBehind writes to a blob-backed cache's store the rows of whatever
// changed since its last write, in one transaction, so that the store holds
// what a whole save of the cache would write. It holds egraphMu for reading
// only while it copies the changed rows.
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
		return WriteBehindReport{}, nil
	}
	for _, rows := range plan.entries {
		if rows == nil {
			continue
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
	for id, outputs := range plan.outputs {
		if outputs == nil {
			delete(b.outputs, id)
		} else {
			b.outputs[id] = outputs
		}
	}
	for id, class := range plan.classes {
		if class == nil {
			delete(b.classes, id)
		} else {
			b.classes[id] = writtenClass{retained: class.retained, terms: class.termIDs()}
		}
	}
	b.nextResultID = plan.nextResultID
	step = c.lockGraphAs(ctx, "dagql.persist.behind.count")
	c.persistedResults = len(b.outputs)
	c.unlockGraphAs(step)
	report := WriteBehindReport{Entries: len(plan.entries), Classes: len(plan.classes)}
	for _, class := range plan.classes {
		if class != nil {
			report.Terms += len(class.terms)
		}
	}
	return report, nil
}

// writeBehindPlan is what one write writes: for each changed entry its rows,
// nil to delete them; for each changed class its rows, nil to delete them,
// with the terms the store held with it; and the next result number.
type writeBehindPlan struct {
	entries      map[sharedResultID]*persistResultRows
	edges        map[sharedResultID]persistdb.MirrorPersistedEdge
	outputs      map[sharedResultID][]eqClassID
	classes      map[eqClassID]*classRows
	oldTerms     map[eqClassID][]egraphTermID
	nextResultID sharedResultID
	nextChanged  bool
	// waited is set when a replacement in flight made the write wait.
	waited bool
}

func (p *writeBehindPlan) empty() bool {
	return len(p.entries) == 0 && len(p.classes) == 0 && len(p.oldTerms) == 0 && !p.nextChanged
}

// classRows are a written class's rows: the class, its digests, and, while
// it is retained, the terms whose output it is.
type classRows struct {
	retained bool
	class    persistdb.MirrorEqClass
	digests  []persistdb.MirrorEqClassDigest
	terms    []persistTermRows
}

func (r *classRows) termIDs() []egraphTermID {
	ids := make([]egraphTermID, 0, len(r.terms))
	for _, term := range r.terms {
		ids = append(ids, egraphTermID(term.term.ID))
	}
	return ids
}

// savedLocked reports whether the entry numbered id is saved: a stored root
// reaches it through entries that are all clean. Requires egraphMu for
// reading.
func (c *Cache) savedLocked(b *writeBehindState, id sharedResultID) bool {
	if _, invalid := b.invalid[id]; invalid {
		return false
	}
	return b.owners[id] > 0 && c.resultsByID[id] != nil
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
			delete(b.unclean, id)
			turned = append(turned, id)
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

// countOwnersLocked brings the counts up to date for entry id: its edge, and
// the children it counts while it is saved. It returns the entries whose
// being saved may have turned. Requires egraphMu for reading.
func (c *Cache) countOwnersLocked(b *writeBehindState, id sharedResultID) []sharedResultID {
	res := c.resultsByID[id]
	_, edgeNow := c.persistedEdgesByResult[id]
	edgeNow = edgeNow && res != nil
	if _, counted := b.edges[id]; counted != edgeNow {
		if edgeNow {
			b.edges[id] = struct{}{}
			b.owners[id]++
		} else {
			delete(b.edges, id)
			b.owners[id]--
		}
	}
	var want []sharedResultID
	if c.savedLocked(b, id) {
		for child := range c.ownedResultIDsLocked(res) {
			want = append(want, child)
		}
	}
	delta := map[sharedResultID]int32{}
	for _, child := range b.children[id] {
		delta[child]--
	}
	for _, child := range want {
		delta[child]++
	}
	var turned []sharedResultID
	for child, d := range delta {
		if d == 0 {
			continue
		}
		before := b.owners[child] > 0
		b.owners[child] += d
		if b.owners[child] <= 0 {
			delete(b.owners, child)
		}
		if before != (b.owners[child] > 0) {
			turned = append(turned, child)
		}
	}
	if len(want) == 0 {
		delete(b.children, id)
	} else {
		b.children[id] = want
	}
	if b.owners[id] <= 0 {
		delete(b.owners, id)
	}
	return turned
}

// retainedLocked reports whether the class rooted at root is retained: a
// saved entry's output is in it. Requires egraphMu for reading.
func (c *Cache) retainedLocked(b *writeBehindState, root eqClassID) bool {
	for id := range c.outputEqClassResults[root] {
		if c.savedLocked(b, id) {
			return true
		}
	}
	return false
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

// planWriteBehindLocked takes the save's marks and plans the write: the
// counts move, and the changed entries' and classes' rows are copied.
// Requires egraphMu for reading; only the save changes its marks under it.
func (c *Cache) planWriteBehindLocked() (*writeBehindPlan, error) {
	b := c.behind
	plan := &writeBehindPlan{
		entries:      map[sharedResultID]*persistResultRows{},
		edges:        map[sharedResultID]persistdb.MirrorPersistedEdge{},
		outputs:      map[sharedResultID][]eqClassID{},
		classes:      map[eqClassID]*classRows{},
		oldTerms:     map[eqClassID][]egraphTermID{},
		nextResultID: c.nextSharedResultID,
		nextChanged:  c.nextSharedResultID != b.nextResultID,
	}
	turnedInvalid, open := c.settleUncleanLocked(b)
	if open {
		// The marks wait for the next write.
		plan.nextChanged, plan.waited = false, true
		return plan, nil
	}
	marked := append(c.usedMarks.take(), turnedInvalid...)
	var markedClasses []eqClassID
	if c.changes != nil {
		marked = append(marked, c.changes.saveEntries...)
		markedClasses = c.changes.saveClasses
		c.changes.saveEntries, c.changes.saveClasses = nil, nil
	}

	// The counts: each marked entry, and each entry whose being saved turns,
	// until none turns.
	changed := map[sharedResultID]struct{}{}
	queue := slices.Clone(marked)
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		changed[id] = struct{}{}
		queue = append(queue, c.countOwnersLocked(b, id)...)
	}

	saved := func(id sharedResultID) bool { return c.savedLocked(b, id) }
	classes := map[eqClassID]struct{}{}
	for _, id := range slices.Sorted(maps.Keys(changed)) {
		for _, eqID := range b.outputs[id] {
			classes[eqID] = struct{}{}
		}
		if !saved(id) {
			plan.entries[id] = nil
			plan.outputs[id] = nil
			continue
		}
		rows, err := c.persistResultRowsLocked(c.resultsByID[id], saved)
		if err != nil {
			return nil, err
		}
		outputs := make([]eqClassID, 0, len(rows.outputs))
		for _, row := range rows.outputs {
			outputs = append(outputs, eqClassID(row.EqClassID))
			classes[eqClassID(row.EqClassID)] = struct{}{}
		}
		plan.entries[id] = &rows
		plan.outputs[id] = outputs
		if _, edge := c.persistedEdgesByResult[id]; edge {
			plan.edges[id] = c.persistEdgeRowLocked(id)
		}
	}

	// The classes: each marked or touched one, and its root; and the input
	// classes of the terms of each class whose being retained turns, whose
	// being written may turn with it.
	queueClasses := slices.Clone(markedClasses)
	for eqID := range classes {
		queueClasses = append(queueClasses, eqID)
	}
	classes = map[eqClassID]struct{}{}
	for len(queueClasses) > 0 {
		eqID := queueClasses[len(queueClasses)-1]
		queueClasses = queueClasses[:len(queueClasses)-1]
		if eqID == 0 {
			continue
		}
		if _, seen := classes[eqID]; seen {
			continue
		}
		classes[eqID] = struct{}{}
		root := c.eqClassRootLocked(eqID)
		if root != eqID {
			if root != 0 {
				queueClasses = append(queueClasses, root)
			}
			continue
		}
		// A class whose being retained turned, or whose terms changed, as a
		// join moves them, changes which of its terms' input classes a
		// retained term takes.
		terms := c.classTermsLocked(root)
		old := b.classes[root]
		if c.retainedLocked(b, root) == old.retained && slices.Equal(terms, old.terms) {
			continue
		}
		for _, termID := range append(terms, old.terms...) {
			if term := c.egraphTerms[termID]; term != nil {
				queueClasses = append(queueClasses, term.inputEqIDs...)
			}
		}
	}

	for _, eqID := range slices.Sorted(maps.Keys(classes)) {
		if old, ok := b.classes[eqID]; ok {
			plan.oldTerms[eqID] = old.terms
		}
		var rows *classRows
		if root := c.eqClassRootLocked(eqID); root == eqID {
			retained := c.retainedLocked(b, root)
			written := retained
			if !written {
				for termID := range c.inputEqClassToTerms[root] {
					term := c.egraphTerms[termID]
					if term != nil && c.retainedLocked(b, c.eqClassRootLocked(term.outputEqID)) {
						written = true
						break
					}
				}
			}
			if written {
				rows = &classRows{retained: retained}
				rows.class, rows.digests = c.persistEqClassRowsLocked(root)
				if retained {
					for _, termID := range c.classTermsLocked(root) {
						termRows, err := c.persistTermRowsLocked(termID, c.egraphTerms[termID])
						if err != nil {
							return nil, err
						}
						rows.terms = append(rows.terms, termRows)
					}
				}
			}
		}
		if rows != nil || plan.oldTerms[eqID] != nil {
			plan.classes[eqID] = rows
		} else if _, held := b.classes[eqID]; held {
			plan.classes[eqID] = nil
		}
	}
	return plan, nil
}

// applyWriteBehindPlan writes the plan in one transaction: every delete
// first, so a term or class that moved is never deleted after its new rows
// are written, then every insert.
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
		for _, termID := range plan.oldTerms[eqID] {
			if err := q.DeleteMirrorTermRows(ctx, int64(termID)); err != nil {
				return fmt.Errorf("write behind: delete term %d: %w", termID, err)
			}
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
		for _, term := range rows.terms {
			if err := q.InsertMirrorTerm(ctx, term.term); err != nil {
				return fmt.Errorf("write behind: insert term %d: %w", term.term.ID, err)
			}
			for _, input := range term.inputs {
				if err := q.InsertMirrorTermInput(ctx, input); err != nil {
					return fmt.Errorf("write behind: insert term_input (%d,%d): %w", input.TermID, input.Position, err)
				}
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
// selected, the entries it left out as invalid, and the classes and outputs
// that save writes. Requires egraphMu for reading.
func (c *Cache) newWriteBehindStateLocked(selected, roots, invalid map[sharedResultID]struct{}) *writeBehindState {
	b := &writeBehindState{
		owners:       map[sharedResultID]int32{},
		edges:        map[sharedResultID]struct{}{},
		children:     map[sharedResultID][]sharedResultID{},
		outputs:      map[sharedResultID][]eqClassID{},
		classes:      map[eqClassID]writtenClass{},
		unclean:      map[sharedResultID]struct{}{},
		invalid:      map[sharedResultID]struct{}{},
		nextResultID: c.nextSharedResultID,
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
		b.owners[id]++
	}
	for id := range selected {
		res := c.resultsByID[id]
		if res == nil {
			continue
		}
		var children []sharedResultID
		for child := range c.ownedResultIDsLocked(res) {
			children = append(children, child)
			b.owners[child]++
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
		for _, eqID := range outputs {
			b.classes[eqID] = writtenClass{retained: true}
		}
	}
	for _, root := range slices.Collect(maps.Keys(b.classes)) {
		terms := c.classTermsLocked(root)
		b.classes[root] = writtenClass{retained: true, terms: terms}
		for _, termID := range terms {
			for _, input := range c.egraphTerms[termID].inputEqIDs {
				input = c.eqClassRootLocked(input)
				if _, ok := b.classes[input]; !ok && input != 0 {
					b.classes[input] = writtenClass{}
				}
			}
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
