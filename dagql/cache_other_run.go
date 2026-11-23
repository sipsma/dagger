package dagql

import (
	"fmt"
	"slices"
)

// An engine holds one current run of each recipe, and merge keeps it
// (mergeKeep): the rows of a bundle above a kept row are attached over the
// engine's own value. Where the step can differ, a bundle's value built over
// another run of it would then stand over a run it wasn't built over, which
// is the mix the withholding rule exists to stop (withholdBuild). So an engine
// refuses such a merge whole, and names the rows (MergeReply.OtherRun), as it
// does stale held rows; the Cloud withholds the roots over them from it.
//
// An engine names a value's run as the Cloud does (mergeRunLocked): a record
// the Cloud sent is the run the Cloud names for it (TransferredValue.Run),
// and a value the engine computed or evaluated itself is the engine's own
// entry's run. A record it installed with no run named, such as its image's
// entries, is a run it can't tell, and refuses nothing: a refusal refuses the
// whole merge, a set file's part among them, and the engine stays cold for
// it.

// mergeOtherRunError is planValueMergeLocked's answer when a row would be
// kept onto the engine's value of another run: the merge commits nothing, and
// answers with the rows.
type mergeOtherRunError struct {
	ordinals []TransferOrdinal
}

func (e *mergeOtherRunError) Error() string {
	return fmt.Sprintf("merge values: %d rows are another run of a step the cache holds", len(e.ordinals))
}

// engineRunLocked returns the run of res's value on an engine, and whether
// the engine knows it: the run the Cloud named for the record it sent, or
// res's own, named by this engine's identity, its number and its
// replacement count, for a value the engine made itself. A record it
// installed with no run named is one it doesn't know. Requires egraphMu.
func (c *Cache) engineRunLocked(res *sharedResult) (RunName, bool) {
	switch {
	case res.run != nil:
		return *res.run, true
	case res.runUnknown:
		return RunName{}, false
	}
	return RunName{Cache: CacheID(c.identity.ID), Number: uint64(res.id), Replacements: res.replacements}, true
}

// otherRunRowsLocked returns, on an engine, sorted, the rows of commit merge
// would keep onto the cache's current entry, held rows among them, where the
// row's step can differ, the Cloud named the row's run, the entry's run is
// another, and the two runs' content isn't the same as the cache knows it
// (a row whose content digest is the entry's is one run with it). A held row
// names the run the Cloud believes the engine holds, and the engine may have
// run the step itself before the Cloud learned of it. Only rows the bundle's
// values stand over count: a row reached from its roots only through a row
// whose content its key addresses (FollowsKey), or through a kept row whose
// content is the engine's own, changes nothing those values hold.
// Requires egraphMu.
func (c *Cache) otherRunRowsLocked(commit *valueMergeCommit) []TransferOrdinal {
	if c.blobBacked {
		return nil
	}
	// A row's frame is its record's; a kept row is prepared light, so its
	// frame is its plan's.
	frameOf := func(row *mergeRow) *ResultCall {
		if row == nil {
			return nil
		}
		if rec, ok := commit.prep.records[uint64(row.ordinal)]; ok && rec.Call != nil {
			return rec.Call
		}
		if row.plan.row == nil {
			return nil
		}
		return row.plan.row.loadResultCall()
	}
	sameContent := func(row *mergeRow, call *ResultCall) bool {
		if row.target == nil || call == nil {
			return false
		}
		content := call.ContentDigest()
		own := row.target.loadResultCall()
		return content != "" && own != nil && content == own.ContentDigest()
	}
	// The rows the bundle's values stand over, from its roots down, not
	// below a row whose content no run under it changes.
	reached := map[uint64]bool{}
	var stack []uint64
	for _, root := range commit.prep.bundle.Roots {
		stack = append(stack, uint64(root.Ordinal))
	}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if reached[id] {
			continue
		}
		reached[id] = true
		row := commit.byOrdinal[id]
		if call := frameOf(row); call != nil && (call.Follows == FollowsKey || row.action == mergeKeep && sameContent(row, call)) {
			continue
		}
		if value := commit.prep.index[id]; value != nil {
			stack = append(stack, value.DependencyIDs...)
		}
	}
	var other []TransferOrdinal
	for _, row := range commit.rows {
		if row.action != mergeKeep || row.target == nil || row.value.Run == nil || !reached[uint64(row.ordinal)] {
			continue
		}
		call := frameOf(row)
		// A step whose content may change is the engine's own state: it
		// keeps its copy, and the value over it says its own declaration.
		if call == nil || call.FollowsFromInputs() || call.ContentMayChange || sameContent(row, call) {
			continue
		}
		if run, known := c.engineRunLocked(row.target); known && run != *row.value.Run {
			other = append(other, TransferOrdinal(row.ordinal))
		}
	}
	slices.Sort(other)
	return other
}
