package dagql

import "slices"

// A cache never puts another run of a recipe beside a value it computed
// itself. On one engine that holds by construction: a result keeps the exact
// entries it was computed over, and a recipe's entry keeps the value its own
// execution made. Across caches, a stored record of a recipe is a run of it
// too, made on another engine, and a merge that put it on an entry this cache
// computed, or installed a record computed over such an entry, would leave a
// value of one run beside bytes or values of another: a file made from one
// run's key beside a certificate signed with another's.
//
// So a merge keeps a cache's own runs. A row whose entry holds a value this
// cache computed (computedHereLocked), with a run of the cache's own at or
// below it (readOwnRuns), is kept apart, unless the row is shown a copy of
// it by content (sameContentLocked): it teaches the entry nothing,
// gives it no part and no Cloud holding, and the reply names it kept apart
// and leaves it out of its values. A record the merge would
// install that was computed over a row kept apart (its dependencies, and the
// owners of its offers) is dropped, with whatever was computed over it, and a
// root dropped this way is skipped. A row that lands on an entry the cache
// took from another cache keeps landing as it does: telling one stored run
// from another needs runs named by the service, which this rule doesn't do.

// computedHereLocked reports whether res holds a value this cache computed
// itself: one its own call made, evaluated or still lazy, and not a record a
// merge or a restore gave it. An entry a merge is still creating is no entry
// of the cache's yet. Requires egraphMu.
func (c *Cache) computedHereLocked(res *sharedResult) bool {
	if res == nil || c.resultsByID[res.id] != res || res.noValueLocked() {
		return false
	}
	res.payloadMu.RLock()
	imported := res.imported
	res.payloadMu.RUnlock()
	return !imported
}

// sameContentLocked reports whether row's record is shown a copy of res's
// value by content: both name a content digest, and it is the same.
// Requires egraphMu.
func sameContentLocked(row *mergeRow, res *sharedResult) bool {
	if row.value == nil || row.value.Record.Call == nil {
		return false
	}
	incoming := row.value.Record.Call.ContentDigest()
	if incoming == "" {
		return false
	}
	frame := res.loadResultCall()
	if frame == nil {
		return false
	}
	return frame.ContentDigest() == incoming
}

// ownLanding is how a merge row landed on an entry the cache computed
// itself without being kept apart.
type ownLanding uint8

const (
	ownNone ownLanding = iota
	// ownSameContent: the row is shown a copy of the entry's value by
	// content.
	ownSameContent
	// ownNotRun: neither the entry nor anything of the cache's own below it
	// has run yet.
	ownNotRun
)

// readOwnRuns notes, for each current entry of the bundle's recipes that
// this cache computed, whether a run of its own is at or below it
// (ranHere), for keepOwnRunsLocked. The entries and the edges between them
// are read under egraphMu for reading, and their runs after it is released
// (ranHere never runs under egraphMu). An entry that becomes a target only
// after this read is taken as run. Its cost is one recipe lookup per record
// and the region of entries the cache computed below its targets.
func (c *Cache) readOwnRuns(prep *valueMergePrep) {
	prep.ownRuns = nil
	if c.blobBacked {
		return
	}
	var targets []*sharedResult
	below := map[*sharedResult][]*sharedResult{}
	c.egraphMu.RLock()
	for _, plan := range prep.plans {
		res := c.currentEntryForRecipeLocked(plan.recipe)
		if res == nil || !c.computedHereLocked(res) {
			continue
		}
		if _, seen := below[res]; seen {
			continue
		}
		targets = append(targets, res)
		stack := []*sharedResult{res}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, seen := below[cur]; seen {
				continue
			}
			var own []*sharedResult
			for dep := range cur.deps {
				if dep := c.resultsByID[dep]; dep != nil && c.computedHereLocked(dep) {
					own = append(own, dep)
					stack = append(stack, dep)
				}
			}
			below[cur] = own
		}
	}
	c.egraphMu.RUnlock()
	if len(targets) == 0 {
		return
	}
	// A run at or below each entry, over the edges read: entries are a
	// DAG, so each is decided once.
	runs := make(map[*sharedResult]bool, len(below))
	var runAtOrBelow func(*sharedResult) bool
	runAtOrBelow = func(res *sharedResult) bool {
		if run, done := runs[res]; done {
			return run
		}
		run := res.ranHere()
		for _, dep := range below[res] {
			if run {
				break
			}
			run = runAtOrBelow(dep)
		}
		runs[res] = run
		return run
	}
	prep.ownRuns = make(map[*sharedResult]bool, len(targets))
	for _, res := range targets {
		prep.ownRuns[res] = runAtOrBelow(res)
	}
}

// keepOwnRunsLocked keeps a merge from putting another run beside a value
// this cache computed (see above). The rows are decided
// (decideMergeRowsLocked) and nothing has changed yet. A row on an entry
// with no run of the cache's own at or below it yet lands as on any kept
// value: nothing has run that it could be put beside. It runs on every
// cache: the Cloud computes nothing, so on it no row is kept apart. Requires
// egraphMu.
func (c *Cache) keepOwnRunsLocked(commit *valueMergeCommit) {
	apart := map[uint64]bool{}
	for _, row := range commit.rows {
		if row.action.installs() || commit.prep.held[row.ordinal] {
			// An installed record lands on an entry with no value of its own,
			// or one whose expired value it replaces; a held row is the
			// entry's own value.
			continue
		}
		if !c.computedHereLocked(row.res) {
			continue
		}
		switch run, read := commit.prep.ownRuns[row.res]; {
		case sameContentLocked(row, row.res):
			row.own = ownSameContent
		case read && !run:
			row.own = ownNotRun
		default:
			row.otherRun = true
			apart[row.ordinal] = true
		}
	}
	if len(apart) == 0 {
		return
	}
	// Rows are in dependency order, so one pass drops every row it must. A
	// dropped row that decides its recipe takes the recipe's other rows,
	// which land on its entry, with it, and they can have dependents of
	// their own: pass again until nothing more drops.
	installing := map[*sharedResult]*mergeRow{}
	for _, row := range commit.rows {
		if row.action.installs() {
			installing[row.res] = row
		}
	}
	dropped := map[uint64]bool{}
	for changed := true; changed; {
		changed = false
		for _, row := range commit.rows {
			if dropped[row.ordinal] || row.otherRun {
				continue
			}
			deps := slices.Clone(row.value.DependencyIDs)
			for _, offer := range row.value.Record.Envelope.PendingOffers {
				deps = append(deps, offer.Owner.DependencyIDs...)
			}
			for _, dep := range deps {
				if !dropped[dep] && !apart[dep] {
					continue
				}
				if row.action.installs() {
					dropped[row.ordinal] = true
				} else {
					// A record landing on an entry that keeps its value
					// installs nothing, but its offers would give the entry
					// parts made over another run: it gives none.
					row.otherRun, row.own = true, ownNone
				}
				changed = true
				break
			}
		}
		for _, row := range commit.rows {
			if !dropped[row.ordinal] || installing[row.res] != row {
				continue
			}
			for _, other := range commit.rows {
				if other.res == row.res && !dropped[other.ordinal] {
					dropped[other.ordinal] = true
					changed = true
				}
			}
		}
	}
	kept := commit.rows[:0]
	for _, row := range commit.rows {
		if dropped[row.ordinal] {
			delete(commit.byOrdinal, row.ordinal)
			continue
		}
		kept = append(kept, row)
	}
	commit.rows = kept
	// A root kept apart or dropped is skipped: the cache's own entry doesn't
	// become the stored root's, and the cache doesn't retain it for it.
	for i := range commit.roots {
		ordinal := uint64(commit.roots[i].Ordinal)
		if commit.roots[i].Expired {
			continue
		}
		if row := commit.byOrdinal[ordinal]; dropped[ordinal] || row != nil && row.otherRun {
			commit.roots[i].OtherRun = true
		}
	}
}

// ownEntriesBelowLocked returns the entries this cache computed among those
// res was computed over, at any depth. The walk stops at an entry the cache
// took from another: a merge installs no record over a value of the cache's
// own (keepOwnRunsLocked), so nothing below such an entry is one. Its cost
// is the region of entries this cache computed below res. Requires egraphMu.
func (c *Cache) ownEntriesBelowLocked(res *sharedResult) []*sharedResult {
	var own []*sharedResult
	seen := map[sharedResultID]bool{res.id: true}
	stack := []*sharedResult{res}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for dep := range cur.deps {
			if seen[dep] {
				continue
			}
			seen[dep] = true
			below := c.resultsByID[dep]
			if below == nil || !c.computedHereLocked(below) {
				continue
			}
			own = append(own, below)
			stack = append(stack, below)
		}
	}
	return own
}

// anyRanHere reports whether any of entries, which this cache computed, has
// run or is running here (ranHere): a part of another cache's run on an
// entry over it would sit in one value beside that run.
func anyRanHere(entries []*sharedResult) bool {
	for _, res := range entries {
		if res.ranHere() {
			return true
		}
	}
	return false
}

// ranHere reports whether res, an entry this cache computed, has run or is
// running here: a part is complete or installed, an evaluation has started,
// or no computation of its value is left (HasPendingLazyComputation's
// answer). A value with no deferred work was made whole by its call; a lazy
// one nothing has evaluated has no run yet. It takes res's gate lock and
// lazyMu one at a time, and is never called with egraphMu held: a capture
// holds lazyMu while its encoding resolves results through egraphMu.
func (res *sharedResult) ranHere() bool {
	if gate := res.partGate.gate.Load(); gate != nil {
		gate.mu.Lock()
		started := false
		for _, out := range gate.outputs {
			started = started || out.phase == PartComplete || out.phase == PartOutputInstalled
		}
		for _, group := range gate.groups {
			started = started || group.phase == LazyEvaluationRunning || group.phase == LazyEvaluationEvaluated
		}
		gate.mu.Unlock()
		if started {
			return true
		}
	}
	res.payloadMu.RLock()
	self := res.self
	res.payloadMu.RUnlock()
	res.lazyMu.Lock()
	defer res.lazyMu.Unlock()
	if lazyEvaluationInFlightLocked(res) != nil {
		return true
	}
	if reporting, ok := UnwrapAs[HasLazyEvaluationReporting](self); ok {
		return !pendingLazyComputationLocked(res, reporting)
	}
	if res.lazyEvalComplete {
		return true
	}
	if !res.lazyWhole.settled() {
		return false
	}
	for _, group := range res.lazyPartGroups {
		if !group.settled() {
			return false
		}
	}
	lazy, ok := UnwrapAs[HasLazyEvaluation](self)
	return !ok || lazy.LazyEvalFunc() == nil
}

// OwnRunStats counts, since the cache opened, what keeping its own runs
// refused and let through: the rows its merges' commits kept apart or let
// land on entries it computed (MergeRowCounts), and the parts OfferParts
// refused or attached.
type OwnRunStats struct {
	// RowsKeptApart, RowsSameContent and RowsNotRun are the committed merge
	// rows counted as MergeRowCounts' OtherRun, OwnSameContent and OwnNotRun.
	RowsKeptApart, RowsSameContent, RowsNotRun uint64
	// OffersRefused are the parts OfferParts refused for rows with a run of
	// this cache's own below them, and OffersLent the parts it attached.
	OffersRefused, OffersLent uint64
}

// noteOwnRunRows adds a committed merge's rows to OwnRunStats.
func (c *Cache) noteOwnRunRows(rows MergeRowCounts) {
	c.ownRunRowsKeptApart.Add(uint64(rows.OtherRun))
	c.ownRunRowsSameContent.Add(uint64(rows.OwnSameContent))
	c.ownRunRowsNotRun.Add(uint64(rows.OwnNotRun))
}

// OwnRunStats reports what keeping the cache's own runs refused and let
// through.
func (c *Cache) OwnRunStats() OwnRunStats {
	return OwnRunStats{
		RowsKeptApart:   c.ownRunRowsKeptApart.Load(),
		RowsSameContent: c.ownRunRowsSameContent.Load(),
		RowsNotRun:      c.ownRunRowsNotRun.Load(),
		OffersRefused:   c.ownRunOffersRefused.Load(),
		OffersLent:      c.ownRunOffersLent.Load(),
	}
}
