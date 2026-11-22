package dagql

import "fmt"

// An engine holds one current run of each recipe, and merge keeps it
// (mergeKeep): the rows of a bundle above a kept row are attached over the
// engine's own value. Where the step can differ, a bundle's value built over
// another run of it would then stand over a run it wasn't built over, which
// is the mix the withholding rule exists to stop (withholdBuild). So an engine
// refuses such a merge whole, and names the rows (MergeReply.OtherRun), as it
// does stale held rows; the Cloud withholds the roots over them from it.
//
// An engine names a value's run as the Cloud does (mergeRunLocked): a record
// the Cloud sent is the run the Cloud names for it (TransferredValue.Run), and
// anything else, a value the engine computed or evaluated itself, is the
// engine's own entry's run. A value whose run can't be told is its own:
// refusing a merge it could have taken leaves roots withheld, which is safe.

// mergeOtherRunError is planValueMergeLocked's answer when a row would be
// kept onto the engine's value of another run: the merge commits nothing, and
// answers with the rows.
type mergeOtherRunError struct {
	ordinals []TransferOrdinal
}

func (e *mergeOtherRunError) Error() string {
	return fmt.Sprintf("merge values: %d rows are another run of a step the cache holds", len(e.ordinals))
}

// engineRunLocked returns the run of res's value on an engine: the run the
// Cloud named for the record it sent, or else res's own, named by this
// cache's identity, res's number and its replacement count. Requires
// egraphMu.
func (c *Cache) engineRunLocked(res *sharedResult) RunName {
	if res.run != nil {
		return *res.run
	}
	return RunName{Cache: CacheID(c.identity.ID), Number: uint64(res.id), Replacements: res.replacements}
}

// otherRunRowsLocked returns, on an engine, the rows of commit merge would
// keep onto the cache's current entry where the row's step can differ, the
// Cloud named the row's run, and the entry's run is another. Requires
// egraphMu.
func (c *Cache) otherRunRowsLocked(commit *valueMergeCommit) []TransferOrdinal {
	if c.blobBacked {
		return nil
	}
	var other []TransferOrdinal
	for _, row := range commit.rows {
		if row.action != mergeKeep || row.target == nil || row.value.Run == nil {
			continue
		}
		// A kept row is prepared light, so its frame is its plan's.
		var call *ResultCall
		if row.plan.row != nil {
			call = row.plan.row.loadResultCall()
		}
		if call == nil || call.FollowsFromInputs {
			continue
		}
		if c.engineRunLocked(row.target) != *row.value.Run {
			other = append(other, TransferOrdinal(row.ordinal))
		}
	}
	return other
}
