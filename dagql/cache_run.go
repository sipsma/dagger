package dagql

import (
	"slices"
	"strings"

	"github.com/opencontainers/go-digest"
)

// A run is one execution of an entry's operation: the process, pull or copy
// that wrote its value. Two runs of a recipe are interchangeable, any one of
// them, but one run's bytes never go beside another run's record. On one
// cache that holds by construction: an entry keeps its own execution's value
// and parts for as long as it lives. Across caches the Cloud keeps it: each
// record it stores names the run that made it, and it stores a part only on a
// record of the part's run (SetStoredParts), so the first run it stores of a
// recipe wins, and a stored bundle lends no part of one run to another run of
// the same recipe (storedPartDonorLocked). Engines keep no runs and send
// none: the Cloud names a record's run from what a merge already carries
// (mergeRunLocked).
//
// RunName names a run by the entry that made it, in the engine cache that ran
// the work: that cache, the entry's number there, and the entry's replacement
// count there.
type RunName struct {
	Cache        CacheID `json:"cache"`
	Number       uint64  `json:"number"`
	Replacements uint64  `json:"replacements,omitempty"`
}

// runOfLocked returns the run res holds: on the Cloud, the one its stored
// record names; for a value this cache computed, this cache's own entry at
// its replacement count. Requires egraphMu.
func (c *Cache) runOfLocked(res *sharedResult) RunName {
	if res.run != nil {
		return *res.run
	}
	return RunName{Cache: CacheID(c.Identity().ID), Number: uint64(res.id), Replacements: res.replacements}
}

// savedRunLocked returns a copy of the run res's stored record names, for a
// save: nil on an engine, which keeps none. Requires egraphMu.
func savedRunLocked(res *sharedResult) *RunName {
	if res.run == nil {
		return nil
	}
	run := *res.run
	return &run
}

// mergeRunLocked names, on the Cloud, the run of a record that a merge from
// engine cache from carries. A record the engine computed is its own entry's
// run. One it imported (its envelope's mark, which imported names) is the run of the Cloud entry the engine's holding of
// it is on: the entry the Cloud sent it from (ApplyMergedReply, or a
// restore's hand-over). An imported record the Cloud holds no holding of is
// named after the engine's entry, as one it computed: a single run, if not
// the name the store first gave it. So is a held row, which carries no
// record to say whether the engine ran it: a part the engine uploads for it
// is refused unless the entry holds the engine's own run, which can refuse
// the Cloud's own bytes back but never stores another run's. Requires
// egraphMu.
func (c *Cache) mergeRunLocked(from CacheID, value *TransferredValue, imported bool) RunName {
	own := RunName{Cache: from, Number: value.SenderNumber, Replacements: value.SenderReplacements}
	if !imported {
		return own
	}
	entry, _ := c.holdingLocked(HolderKey{Cache: from, Number: value.SenderNumber})
	if entry == nil || entry.run == nil {
		return own
	}
	return *entry.run
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

// A stored record whose run left no content is replaced by a later run that
// is whole from it up (replaceRunsLocked). Refusing every other run's bytes
// alone would leave such a record without content for good: any later run's
// content is another run's. So a merge from an engine that ran the record's
// call itself takes its place, as an engine's own cache takes an expired
// value's (cache_current_entry.go): the old entry is retired, and every
// entry over it with it, whatever content those hold, since they were made
// over the old run; the sender's records register beside them as the
// recipes' current entries. A retired entry leaves the recipe index and is
// no lookup's, offer's or class mate's again (runReplaced). It keeps its
// value and stored parts for what names it already.

// replaceRunsLocked decides, on the Cloud, the stored runs commit's merge
// replaces. A best row (isBest) replaces the entry it lands on when the
// sender computed the row itself and its image doesn't ship it, and the
// entry holds a live value of another run whose stored record isn't an image
// entry's, whose own run ran (ownRunRanLocked) and whose content isn't
// stored (storedWithoutContentLocked).
// Every entry over a replaced one is retired with it: a row of the merge
// that lands on one, of another run than it holds, takes a new entry
// (mergeRetire); the commit retires the others (retireOver). Its counts go
// to commit.overOtherRun. Requires egraphMu.
func (c *Cache) replaceRunsLocked(commit *valueMergeCommit, isBest func(*mergeRow) bool) {
	var replaced []*sharedResult
	for _, row := range commit.rows {
		cur := row.target
		// Only a live record is replaced here: an expired one the merge
		// replaces as it does today (decideMergeRowLocked). A row the
		// sender imported, an image entry's among them, replaces nothing.
		if !isBest(row) || row.action != mergeKeep || row.expired || row.value.Held != "" || commit.prep.senderImported[row.ordinal] || cur == nil || cur.runReplaced {
			continue
		}
		// Only a record whose own run ran, with its content not stored, is
		// replaced: a record whose run never ran is a recipe, and the run of
		// an image entry whose content the image ships is the image's.
		if cur.noValueLocked() || cur.imageEntry || c.runOfLocked(cur) == row.run || !c.storedWithoutContentLocked(cur) || !c.ownRunRanLocked(cur) {
			continue
		}
		replaced = append(replaced, cur)
	}
	if len(replaced) == 0 {
		return
	}
	over := entriesOverLocked(c, replaced)
	for _, row := range commit.rows {
		if _, ok := over[row.target.idOrZero()]; ok && row.value.Held != "" {
			// A held row stands for the record it lands on, which the
			// replacement would retire: it carries no record to install
			// in its place. The merge replaces nothing; a later merge of
			// the run's records whole does.
			return
		}
	}
	for _, row := range commit.rows {
		cur := row.target
		if !isBest(row) || cur == nil || row.expired {
			continue
		}
		if _, ok := over[cur.id]; !ok || c.runOfLocked(cur) == row.run {
			continue
		}
		row.action, row.res = mergeRetire, &sharedResult{id: sharedResultID(commit.prep.firstID + row.ordinal - 1)}
		delete(over, cur.id)
	}
	commit.overOtherRun.RunsReplaced = len(replaced)
	commit.overOtherRun.RetiredOver = len(over)
	commit.retireOver = make([]*sharedResult, 0, len(over))
	for _, res := range over {
		commit.retireOver = append(commit.retireOver, res)
	}
	slices.SortFunc(commit.retireOver, compareSharedResults)
}

// canAdoptRunLocked reports whether res's stored record may take the run of
// a part being stored onto it: its own run never ran, so the record is a
// recipe, which no run's content can mix with. It may not when the record's
// run ran (ownRunRanLocked), it stores content of its own, it is an image
// entry's, or a stored record over it that its own engine made ran
// (ownEngineRanOverLocked): running a step evaluates its inputs, so that
// engine's run of res happened, though no row said so, and a value of it
// over another run's record would mix the two. A record of that engine over
// res that never ran itself, such as a list over its lazy items, says
// nothing of it. Requires egraphMu.
func (c *Cache) canAdoptRunLocked(res *sharedResult) bool {
	if !c.blobBacked || res.run == nil || res.imageEntry || c.ownRunRanLocked(res) {
		return false
	}
	for _, part := range res.storedPartsLocked() {
		if part.address().Part != "metadata" {
			return false
		}
	}
	return !c.ownEngineRanOverLocked(res)
}

// adoptRunLocked gives res's stored record run, the run of a part just
// stored onto it, which canAdoptRunLocked allowed. Nothing is retired:
// nothing over a recipe was built from its content. Requires egraphMu for
// writing.
func (c *Cache) adoptRunLocked(res *sharedResult, run RunName) {
	adopted := run
	res.run, res.runRan = &adopted, true
	c.markEntryLocked(res.id)
}

// ownEngineRanOverLocked reports whether a stored record over res, directly
// or through any others, was made by res's own engine and its own run ran.
// A record of another engine in between doesn't stop the walk: an engine's
// record over it can land on another engine's entry, as an equal input
// does. It walks every record over res once. Requires egraphMu.
func (c *Cache) ownEngineRanOverLocked(res *sharedResult) bool {
	engine := res.run.Cache
	seen := map[sharedResultID]bool{res.id: true}
	queue := []*sharedResult{res}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for parentID := range cur.depParents {
			if seen[parentID] {
				continue
			}
			seen[parentID] = true
			parent := c.resultsByID[parentID]
			if parent == nil {
				continue
			}
			if parent.run != nil && parent.run.Cache == engine && !parent.noValueLocked() && c.ownRunRanLocked(parent) {
				return true
			}
			queue = append(queue, parent)
		}
	}
	return false
}

// metadataPartField is the metadata part's field in an address key
// (partAddressKey), at any output path.
const metadataPartField = `"part":"metadata"`

// ranPartsLocked reports whether h reports a part complete besides its
// metadata: its cache ran the step. Requires egraphMu.
func ranPartsLocked(h *holding) bool {
	for _, part := range h.parts {
		if part.state >= heldPartComplete && !strings.Contains(handleString(part.key), metadataPartField) {
			return true
		}
	}
	return false
}

// runHoldingRanLocked reports, on the Cloud, whether the holding run names,
// the engine entry that made the run at its replacement count, reports a
// part complete besides its metadata. Another engine's holding of the same
// entry says nothing of this run. Requires egraphMu.
func (c *Cache) runHoldingRanLocked(run RunName) bool {
	_, h := c.holdingLocked(HolderKey{Cache: run.Cache, Number: run.Number})
	return h != nil && h.replacements == run.Replacements && ranPartsLocked(h)
}

// noteRunRanLocked marks, on the Cloud, entry's stored record as having run
// (sharedResult.runRan) when key is its run's holding and h now reports a
// part complete besides its metadata. Requires egraphMu for writing.
func (c *Cache) noteRunRanLocked(entry *sharedResult, key HolderKey, h *holding) {
	if !c.blobBacked || entry == nil || entry.runRan || entry.run == nil {
		return
	}
	if run := *entry.run; key.Cache != run.Cache || key.Number != run.Number || h.replacements != run.Replacements || !ranPartsLocked(h) {
		return
	}
	entry.runRan = true
	c.markEntryLocked(entry.id)
}

// ownRunRanLocked reports whether res's stored record's own run ran: its
// run's holding reported a part complete besides its metadata
// (sharedResult.runRan), or the record proves one complete. A record whose
// run never evaluated its step is a recipe, whatever another engine did with
// the same entry. Requires egraphMu.
func (c *Cache) ownRunRanLocked(res *sharedResult) bool {
	return res.runRan || c.recordProvesContentLocked(res)
}

// recordProvesContentLocked reports whether res's stored record proves a
// part besides its metadata complete, whether the Cloud holds the record
// whole or compressed. Requires egraphMu.
func (c *Cache) recordProvesContentLocked(res *sharedResult) bool {
	if res.stored != nil {
		_, _, complete, err := res.storedFactsLocked()
		if err != nil || complete == nil {
			return false
		}
		for _, address := range complete.addresses() {
			if address.Part != "metadata" {
				return true
			}
		}
		return false
	}
	rec, err := c.storedRecordLocked(res)
	if err != nil {
		return false
	}
	probes, err := describeMemoPartRecord(rec, payloadMemos{})
	if err != nil {
		return false
	}
	for _, probe := range probes {
		if probe.LocalComplete && probe.Descriptor.Address.Part != "metadata" {
			return true
		}
	}
	return false
}

// idOrZero is res's number, or 0 for no entry.
func (res *sharedResult) idOrZero() sharedResultID {
	if res == nil {
		return 0
	}
	return res.id
}

// entriesOverLocked returns from and every entry that depends on one of
// them, directly or through others, by number. Requires egraphMu.
func entriesOverLocked(c *Cache, from []*sharedResult) map[sharedResultID]*sharedResult {
	over := make(map[sharedResultID]*sharedResult, len(from))
	queue := slices.Clone(from)
	for _, res := range from {
		over[res.id] = res
	}
	for len(queue) > 0 {
		res := queue[0]
		queue = queue[1:]
		for parentID := range res.depParents {
			if _, seen := over[parentID]; seen {
				continue
			}
			parent := c.resultsByID[parentID]
			if parent == nil || parent.runReplaced {
				continue
			}
			over[parentID] = parent
			queue = append(queue, parent)
		}
	}
	return over
}

// storedWithoutContentLocked reports whether res's stored record declares or
// proves a part besides its metadata, and the cache stores none of its
// parts but metadata, nor has an upload of one in progress
// (MarkPartUploading): a run whose content no engine can fetch, nor is on
// its way. Requires egraphMu.
func (c *Cache) storedWithoutContentLocked(res *sharedResult) bool {
	if len(res.uploading) > 0 {
		return false
	}
	for _, part := range res.storedPartsLocked() {
		if part.address().Part != "metadata" {
			return false
		}
	}
	parts, ok := c.recordPartsLocked(res)
	if !ok {
		return false
	}
	for _, part := range parts.declared {
		if part.address.Part != "metadata" {
			return true
		}
	}
	if parts.complete != nil {
		for _, address := range parts.complete.addresses() {
			if address.Part != "metadata" {
				return true
			}
		}
	}
	for _, part := range parts.mapped {
		if part.Address.Part != "metadata" {
			return true
		}
	}
	return false
}

// countOverOtherRunLocked counts, on a blob-backed cache, the records a merge
// installs that the sender computed over another run of an input than the
// one the cache stores: an input row that lands on an entry holding another
// run. The cache stores such a record over its own run of the input all the
// same, so it can hold one run's result beside another run's input. Each is
// counted by its inputs of another run: shown equal by their content
// digests, shown different, or not comparable because a side names none.
// Requires egraphMu.
func (c *Cache) countOverOtherRunLocked(commit *valueMergeCommit) {
	// otherRun maps each input row of another run than its entry's to
	// whether the two are equal (1), different (-1) or not comparable (0).
	otherRun := map[uint64]int{}
	for _, row := range commit.rows {
		if row.action.installs() || row.res == nil || c.runOfLocked(row.res) == row.run {
			continue
		}
		otherRun[row.ordinal] = compareRunContent(commit.prep.records[row.ordinal].Call, row.res)
	}
	if len(otherRun) == 0 {
		return
	}
	counts := &commit.overOtherRun
	for _, row := range commit.rows {
		if !row.action.installs() {
			continue
		}
		over, verdict := false, 1
		for _, dep := range row.value.DependencyIDs {
			v, ok := otherRun[dep]
			if !ok {
				continue
			}
			over = true
			if v < verdict {
				verdict = v
			}
		}
		if !over {
			continue
		}
		switch verdict {
		case 1:
			counts.OverOtherRunEqual++
		case -1:
			counts.OverOtherRunDifferent++
		default:
			counts.OverOtherRunUnknown++
		}
	}
}

// compareRunContent compares an incoming row's record call with the value an
// entry holds by content digest: 1 when both name the same, -1 when both name
// one and they differ, 0 when either names none (a held row has no call).
func compareRunContent(incoming *ResultCall, res *sharedResult) int {
	var in digest.Digest
	if incoming != nil {
		in = incoming.ContentDigest()
	}
	var have digest.Digest
	res.resultCallMu.RLock()
	known, content, call := res.frameContentKnown, res.frameContent, res.resultCall
	res.resultCallMu.RUnlock()
	switch {
	case known:
		have = content
	case call != nil:
		have = call.ContentDigest()
	}
	switch {
	case in == "" || have == "":
		return 0
	case in == have:
		return 1
	default:
		return -1
	}
}
