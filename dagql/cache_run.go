package dagql

import "slices"

// A run is one execution of an entry's operation: the process, pull or copy
// that wrote its value. Two runs of a recipe are interchangeable, any one of
// them; but a result computed from one run is never used beside another run
// of its inputs. On one cache that holds by construction, since a result
// keeps the exact entries it was computed over. Across caches it holds only if
// each value names the run that made it, and a merge never puts a record on
// top of an entry that holds another run of the same recipe.
//
// RunName names a run by the entry that made it, in the cache that ran the
// work: that cache, the entry's number there, and the entry's replacement
// count there. The name travels unchanged through every cache the value
// passes through.
type RunName struct {
	Cache        CacheID `json:"cache"`
	Number       uint64  `json:"number"`
	Replacements uint64  `json:"replacements,omitempty"`
}

// runOfLocked returns the run res holds: the one a merge gave it, or, for a
// value this cache computed, this cache's own entry at its replacement count.
// Requires egraphMu.
func (c *Cache) runOfLocked(res *sharedResult) RunName {
	if res.run != nil {
		return *res.run
	}
	return RunName{Cache: CacheID(c.Identity().ID), Number: uint64(res.id), Replacements: res.replacements}
}

// sentRunLocked returns the run a row sent for res names: none when res holds
// its own run, which the row's sender number and replacement count name
// already (TransferredRun), and res's merged run otherwise. Requires egraphMu.
func sentRunLocked(res *sharedResult) *RunName {
	if res.run == nil {
		return nil
	}
	run := *res.run
	return &run
}

// EntryRun returns the run the entry with the number holds, or false when the
// cache has no such entry or it has no value.
func (c *Cache) EntryRun(number uint64) (RunName, bool) {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	res := c.resultsByID[sharedResultID(number)]
	if res == nil || res.noValueLocked() {
		return RunName{}, false
	}
	return c.runOfLocked(res), true
}

// otherRunOfRecipeLocked reports whether a and b are two runs of one recipe:
// a recipe names both, and they hold different runs. Results of different
// recipes that the cache holds equivalent stay interchangeable, by their
// content or their calls, as on one cache; two runs of one recipe don't
// lend each other parts (cache_run.go). Requires egraphMu.
func (c *Cache) otherRunOfRecipeLocked(a, b *sharedResult) bool {
	shared := false
	for _, key := range a.recipeKeys {
		if slices.Contains(b.recipeKeys, key) {
			shared = true
			break
		}
	}
	return shared && c.runOfLocked(a) != c.runOfLocked(b)
}

// TransferredRun returns the run a row from cache from names: its Run, or,
// when the row carries none, the sending entry itself.
func TransferredRun(from CacheID, value TransferredValue) RunName {
	if value.Run != nil {
		return *value.Run
	}
	return RunName{Cache: from, Number: value.SenderNumber, Replacements: value.SenderReplacements}
}

// keepRunsApartLocked keeps a merge from putting a record beside another run
// of what it was computed over. A row whose entry keeps another run than the
// row's (a kept value or a held row of the same recipe that another run
// made) is marked otherRun: it is no copy of the entry's value, so it teaches
// it nothing, gives it no part, and is left out of the reply. A record the
// merge would install that was computed over such a row (its dependencies,
// and the owners of its offers) is dropped from the merge. Installed, it
// would reference the run the entry holds: a read would read as computed from
// it, a rebuild would run over it, and a value would read it where it keeps
// a reference, as a container's mounted files do. That holds for a record
// that came with bytes of its own too, since its bytes don't cover what its
// references read, and the Cloud can still decline to store them. A record
// computed over a dropped row is dropped too, since its dependency won't
// exist, and a recipe whose deciding row is dropped is dropped whole, since
// its other rows land on that row's entry. A row that lands on an entry of
// its own run and installs nothing is never dropped: the entry keeps its own
// dependencies, of the runs it was computed over. A dropped root is skipped; a root apart lands on its entry, whose own
// run stands for it. The rows are decided (decideMergeRowLocked) and nothing
// has changed yet. Requires egraphMu.
func (c *Cache) keepRunsApartLocked(commit *valueMergeCommit) {
	installing := map[*sharedResult]*mergeRow{}
	for _, row := range commit.rows {
		if row.action.installs() {
			installing[row.res] = row
		}
	}
	landing := func(row *mergeRow) RunName {
		if row.action.installs() {
			return row.run
		}
		if b := installing[row.res]; b != nil {
			return b.run
		}
		return c.runOfLocked(row.res)
	}
	apart := map[uint64]bool{}
	for _, row := range commit.rows {
		if landing(row) != row.run {
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
	dropped := map[uint64]bool{}
	for changed := true; changed; {
		changed = false
		for _, row := range commit.rows {
			if dropped[row.ordinal] || !row.action.installs() {
				continue
			}
			deps := slices.Clone(row.value.DependencyIDs)
			for _, offer := range row.value.Record.Envelope.PendingOffers {
				deps = append(deps, offer.Owner.DependencyIDs...)
			}
			for _, dep := range deps {
				if dropped[dep] || apart[dep] {
					dropped[row.ordinal] = true
					changed = true
					break
				}
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
	// A root apart is still a whole value of its recipe, which the entry's
	// own run stands for: it lands on the entry, retained. A dropped
	// root doesn't land at all.
	for i := range commit.roots {
		if !commit.roots[i].Expired && dropped[uint64(commit.roots[i].Ordinal)] {
			commit.roots[i].OtherRun = true
		}
	}
}
