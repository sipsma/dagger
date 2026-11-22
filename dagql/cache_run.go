package dagql

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
)

// A run is one execution of an entry's operation: the process, pull or copy
// that wrote its value. The Cloud names, for each record it stores, the run
// that made it, and keeps whether that run ran the step: a record the Cloud
// learns from an engine's call row declares its parts pending, since the call
// returns its lazy result before the step runs, and is a recipe until the
// engine runs it. Engines keep no runs and send none: the Cloud names a
// record's run from what a merge already carries (mergeRunLocked), and learns
// that it ran from the run's own holding (noteHoldingRanLocked). Nothing in
// this cache decides by either; ownRunRanLocked and EntryRun read them.
//
// RunName names a run by the entry that made it, in the engine cache that ran
// the work: that cache, the entry's number there, and the entry's replacement
// count there.
type RunName struct {
	Cache        CacheID `json:"cache"`
	Number       uint64  `json:"number"`
	Replacements uint64  `json:"replacements,omitempty"`
}

// runRanBy is how the Cloud learned that a stored record's own run ran its
// step (sharedResult.runRan), or runNotRan.
type runRanBy uint8

const (
	runNotRan runRanBy = iota
	// runRanAtMerge: the run's holding had already run its value when the
	// merge installed the record.
	runRanAtMerge
	// runRanByProof: the record proves a part besides its metadata complete
	// itself, and no holding had said the run ran when the merge installed
	// it.
	runRanByProof
	// runRanByCall: a call row of the run's holding said it executed or
	// joined the call and reported a part besides its metadata complete,
	// after the merge installed the record.
	runRanByCall
	// runRanByEvaluation: a lazy row of the run's holding reported a part
	// besides its metadata complete, and the row didn't install it from a
	// Cloud offer, after the merge installed the record.
	runRanByEvaluation
)

// savedRunLocked returns a copy of the run res's stored record names, for a
// save: nil on an engine, which keeps none. Requires egraphMu.
func savedRunLocked(res *sharedResult) *RunName {
	if res.run == nil {
		return nil
	}
	run := *res.run
	return &run
}

// mergeRunLocked names, on the Cloud, the run of the record row installs
// from a merge of prep, and how the Cloud knows that run ran its step. A
// record the engine computed is its own entry's run. So is one it imported
// (its envelope's mark, valueMergePrep.senderImported) and then ran itself,
// for want of the Cloud's content: its holding ran its value at the row's
// replacement count (holdingRanLocked). Another imported record is the run
// of the Cloud entry the engine's holding of it is on, the entry the Cloud
// sent it from (ApplyMergedReply, or a restore's hand-over), with what the
// Cloud already knows of it, or, when the Cloud holds no such holding or
// its entry names no run, the engine's own.
// The run ran when its holding had run its value, or else when the record
// proves a part besides its metadata complete (valueMergePrep.completeParts).
// Requires egraphMu.
func (c *Cache) mergeRunLocked(prep *valueMergePrep, row *mergeRow) (RunName, runRanBy) {
	key := HolderKey{Cache: prep.from, Number: row.value.SenderNumber}
	run := RunName{Cache: key.Cache, Number: key.Number, Replacements: row.value.SenderReplacements}
	if prep.senderImported[row.ordinal] && !c.holdingRanLocked(key, run.Replacements) {
		if entry, _ := c.holdingLocked(key); entry != nil && entry.run != nil {
			// A copy of the entry's record is the entry's run, which may
			// have been learned to run already.
			run = *entry.run
			if entry.runRan != runNotRan {
				return run, entry.runRan
			}
		}
	}
	switch {
	case c.holdingRanLocked(HolderKey{Cache: run.Cache, Number: run.Number}, run.Replacements):
		return run, runRanAtMerge
	case ranPartsReported(prep.completeParts[row.ordinal]):
		return run, runRanByProof
	}
	return run, runNotRan
}

// holdingRanLocked reports whether the holding key ran its value at
// replacement count replacements (holding.ran): its own run of the entry.
// Another engine's holding of the same entry says nothing of it. Requires
// egraphMu.
func (c *Cache) holdingRanLocked(key HolderKey, replacements uint64) bool {
	_, h := c.holdingLocked(key)
	return h != nil && h.ran && h.replacements == replacements
}

// noteHoldingRanLocked notes that h, the holding key of its cache's entry,
// ran its value at replacement count replacements: the holding's own count,
// or an older one a delayed report describes (holding.ran keeps only the
// first). It marks the stored record of entry, which the holding sits on, as
// having run when it names that run and didn't yet (sharedResult.runRan),
// marks the entry changed, for readers that keep their answers, and reports
// whether it did: a save then writes something new. Requires egraphMu for
// writing.
func (c *Cache) noteHoldingRanLocked(key HolderKey, entry *sharedResult, h *holding, replacements uint64, by runRanBy) bool {
	if replacements == h.replacements {
		h.ran = true
	}
	if !c.blobBacked || entry == nil || entry.run == nil || entry.runRan != runNotRan {
		return false
	}
	if run := *entry.run; key.Cache != run.Cache || key.Number != run.Number || replacements != run.Replacements {
		return false
	}
	entry.runRan = by
	c.markEntryLocked(entry.id)
	return true
}

// ranPartsReported reports whether parts, an observation's complete part
// keys, name a part besides the metadata part.
func ranPartsReported(parts []string) bool {
	for _, key := range parts {
		if !strings.Contains(key, metadataPartField) {
			return true
		}
	}
	return false
}

// metadataPartField is the metadata part's field in an address key
// (partAddressKey), at any output path.
const metadataPartField = `"part":"metadata"`

// ownRunRanLocked reports whether res's stored record's own run ran its
// step: the run's holding reported a part besides its metadata complete,
// from a call it executed or joined or from an evaluation that didn't
// install a Cloud offer, or the record proves such a part complete itself
// (sharedResult.runRan). Another engine's run of the same entry says nothing
// of it. Requires egraphMu.
func (c *Cache) ownRunRanLocked(res *sharedResult) bool {
	return res.run != nil && res.runRan != runNotRan
}

// EntryRun is what the Cloud's stored record of an entry says of its run.
type EntryRun struct {
	// Run is the run that made the record.
	Run RunName
	// Ran reports that the run ran its step (ownRunRanLocked).
	Ran bool
}

// EntryRun returns, on the Cloud, what the stored record of the entry
// numbered number says of its run, or false when the cache has no such entry
// or the entry stores no record that names one.
func (c *Cache) EntryRun(number uint64) (EntryRun, bool) {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	res := c.resultsByID[sharedResultID(number)]
	if res == nil || res.run == nil || res.noValueLocked() {
		return EntryRun{}, false
	}
	return EntryRun{Run: *res.run, Ran: c.ownRunRanLocked(res)}, true
}

// HoldingRun is what the Cloud's stored record of a holding's entry says of
// its run, from the holding's side (HoldingRuns).
type HoldingRun struct {
	// Stored reports that the holding's entry stores a record that names a
	// run. The rest is zero when it doesn't, or the cache has no such
	// holding.
	Stored bool
	// Run is the run the record names, and Ran whether it ran its step
	// (ownRunRanLocked).
	Run RunName
	Ran bool
	// Own reports that the run is the holding's own: the same engine cache
	// and entry, at the holding's replacement count.
	Own bool
	// RunHeld reports that the run's own holding is still held at the run's
	// replacement count: it hasn't gone, or moved on to a later value.
	RunHeld bool
}

// HoldingRuns returns, on the Cloud, what the stored record of each
// holding's entry says of its run (HoldingRun), in the order of keys, under
// one hold of the lock for reading. A write that changes a record's run or
// its ran mark names the entry (RemoteChange.Entries), and so does one that
// collects a run's own holding; one that moves that holding on to a later
// value names only the holding.
func (c *Cache) HoldingRuns(keys []HolderKey) []HoldingRun {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	out := make([]HoldingRun, len(keys))
	for i, key := range keys {
		entry, h := c.holdingLocked(key)
		if h == nil || entry == nil || entry.run == nil || entry.noValueLocked() {
			continue
		}
		run := *entry.run
		_, held := c.holdingLocked(HolderKey{Cache: run.Cache, Number: run.Number})
		out[i] = HoldingRun{
			Stored:  true,
			Run:     run,
			Ran:     c.ownRunRanLocked(entry),
			Own:     key.Cache == run.Cache && key.Number == run.Number && h.replacements == run.Replacements,
			RunHeld: held != nil && held.replacements == run.Replacements,
		}
	}
	return out
}

// RunCounts counts the Cloud's stored records by what they say of their
// runs (StoredRuns), for a run's reader to check the runs by.
type RunCounts struct {
	// Records are the entries that store a record.
	Records int `json:"records"`
	// Unnamed are those whose record names no run.
	Unnamed int `json:"unnamed"`
	// NotRan are the named ones whose run hasn't run its step, as far as
	// the Cloud knows: recipes.
	NotRan int `json:"notRan"`
	// RanAtMerge, RanByProof, RanByCall and RanByEvaluation are the named
	// ones that ran, by how the Cloud learned it (runRanBy).
	RanAtMerge      int `json:"ranAtMerge"`
	RanByProof      int `json:"ranByProof"`
	RanByCall       int `json:"ranByCall"`
	RanByEvaluation int `json:"ranByEvaluation"`
	// NotRanRecords name the NotRan records, by number: a run among them
	// that an engine's telemetry shows ran is a fault in what the Cloud
	// learned.
	NotRanRecords []NotRanRecord `json:"notRanRecords"`
}

// NotRanRecord is a stored record whose named run hasn't run its step, as
// far as the Cloud knows.
type NotRanRecord struct {
	Number uint64        `json:"number"`
	Recipe digest.Digest `json:"recipe"`
	Run    RunName       `json:"run"`
}

// StoredRuns counts the cache's stored records by what they say of their
// runs (RunCounts). It reads every entry under one hold of the lock for
// reading, so a caller asks it once at a push's end, not on a hot path.
func (c *Cache) StoredRuns() RunCounts {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	counts := RunCounts{NotRanRecords: []NotRanRecord{}}
	for id, res := range c.resultsByID {
		res.payloadMu.RLock()
		stored := res.persistedEnvelope != nil || res.stored != nil
		res.payloadMu.RUnlock()
		if !stored {
			continue
		}
		counts.Records++
		if res.run == nil {
			counts.Unnamed++
			continue
		}
		switch res.runRan {
		case runRanAtMerge:
			counts.RanAtMerge++
		case runRanByProof:
			counts.RanByProof++
		case runRanByCall:
			counts.RanByCall++
		case runRanByEvaluation:
			counts.RanByEvaluation++
		default:
			counts.NotRan++
			recipe, _ := c.entryRecipeLocked(uint64(id))
			counts.NotRanRecords = append(counts.NotRanRecords, NotRanRecord{Number: uint64(id), Recipe: recipe, Run: *res.run})
		}
	}
	slices.SortFunc(counts.NotRanRecords, func(a, b NotRanRecord) int { return cmp.Compare(a.Number, b.Number) })
	return counts
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
	heldReplaces := false
	for _, row := range commit.rows {
		cur := row.target
		// Only a live record is replaced here: an expired one the merge
		// replaces as it does today (decideMergeRowLocked). A row the
		// sender imported, an image entry's among them, replaces nothing.
		if !isBest(row) || row.action != mergeKeep || row.expired || commit.prep.senderImported[row.ordinal] || cur == nil {
			continue
		}
		// A run replaces another's record only with its whole content of
		// the step, which this export carries: without it, it gains nothing
		// over the record it would replace, and two runs that both left no
		// content would replace each other's records in turn; with part of
		// it, its record would keep some content and no later run could
		// make it whole.
		if !c.carriesWholeLocked(cur, commit.prep.contentRows[row.ordinal]) {
			continue
		}
		if row.value.Held != "" {
			// A held row of the sender's own run, which it computed, would
			// replace the record if it came in full: its sender believes
			// the Cloud holds its run, and the Cloud holds another.
			if c.heldRowComputedLocked(commit.prep, row) && c.replaceableLocked(cur, row.run, commit.now) {
				replaced = append(replaced, cur)
				heldReplaces = true
			}
			continue
		}
		if c.replaceableLocked(cur, row.run, commit.now) {
			replaced = append(replaced, cur)
		}
	}
	if len(replaced) == 0 {
		return
	}
	over := entriesOverLocked(c, replaced)
	// A held row stands for the record it lands on, and carries no record to
	// install in its place: one on a record the replacement would replace or
	// retire is stale. The merge commits nothing, and its sender sends those
	// rows in full, which the replacement then decides on (KnownRows.Stale).
	var stale []TransferOrdinal
	for _, row := range commit.rows {
		if _, ok := over[row.target.idOrZero()]; ok && row.value.Held != "" {
			stale = append(stale, TransferOrdinal(row.ordinal))
		}
	}
	if len(stale) > 0 || heldReplaces {
		slices.Sort(stale)
		commit.staleRuns = stale
		return
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
		row.replacing = true
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
	// The adopted run ran: its part is stored on the record, which proves
	// it complete.
	adopted := run
	res.run, res.runRan = &adopted, runRanByProof
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

// replaceableLocked reports whether a full record of run would replace res's
// stored record (replaceRunsLocked): res is live at now, not retired, and
// stores a record of another run, whose own run ran (ownRunRanLocked), of
// which no content but metadata is stored or being uploaded
// (storedWithoutContentLocked), and which isn't an image result. Requires
// egraphMu.
func (c *Cache) replaceableLocked(res *sharedResult, run RunName, now int64) bool {
	if res == nil || res.runReplaced || res.noValueLocked() || res.imageEntry || c.resultExpiredAtLocked(res, now) {
		return false
	}
	return c.runOfLocked(res) != run && c.storedWithoutContentLocked(res) && c.ownRunRanLocked(res)
}

// ReplaceableBy reports whether a full record of run would replace the
// stored record of the entry numbered number, now (replaceableLocked): a
// remote cache service's selection asks a holding of run for the entry's
// parts only then, or when run is the record's own.
func (c *Cache) ReplaceableBy(number uint64, run RunName) bool {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	return c.replaceableLocked(c.resultsByID[sharedResultID(number)], run, time.Now().Unix())
}

// carriesWholeLocked reports whether parts, the part keys an export carries
// content of, hold every part of content res's stored record declares or
// proves (recordParts.contentKeys): the step's whole content, as a run of it
// writes. Requires egraphMu.
func (c *Cache) carriesWholeLocked(res *sharedResult, parts map[string]PersistedPartAddress) bool {
	if res == nil || len(parts) == 0 {
		return false
	}
	record, ok := c.recordPartsLocked(res)
	if !ok {
		return false
	}
	for _, key := range record.contentKeys() {
		if _, ok := parts[key]; !ok {
			return false
		}
	}
	return true
}

// markCarriedUploadsLocked marks in progress, in the commit's hold, an upload
// of each part whose content the bundle carries on each record of the
// sender's own run the commit left on an entry of that run, and names them,
// and their entries' classes, in reply (MergeReply.Uploading). An export
// carries its run's content as completed outputs and claims their uploads
// only once its merge has committed: without the mark, another run's merge
// in between would find the record without content and replace it, and the
// export's own, its record replaced in turn. Requires egraphMu for writing.
func (c *Cache) markCarriedUploadsLocked(commit *valueMergeCommit, reply *MergeReply) {
	if !c.blobBacked || len(commit.prep.contentRows) == 0 {
		return
	}
	var numbers []uint64
	for _, row := range commit.rows {
		parts := commit.prep.contentRows[row.ordinal]
		res := row.res
		if len(parts) == 0 || res == nil || c.resultsByID[res.id] != res || c.runOfLocked(res) != row.run {
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(parts)) {
			if err := c.MarkPartUploading(uint64(res.id), parts[key]); err != nil {
				continue
			}
			reply.Uploading = append(reply.Uploading, PartUpload{Number: uint64(res.id), Address: parts[key]})
		}
		numbers = append(numbers, uint64(res.id))
	}
	if len(numbers) == 0 {
		return
	}
	reply.Change.Entries = append(reply.Change.Entries, c.classChangeLocked(numbers).Entries...)
	slices.Sort(reply.Change.Entries)
	reply.Change.Entries = slices.Compact(reply.Change.Entries)
}

// contentKeys returns the keys of the record's parts that have content: every
// part it declares pending, proves complete or maps, besides its metadata and
// any it declares absent, which asks for no bytes, though the record proves
// it complete.
func (p recordParts) contentKeys() []string {
	absent := map[string]bool{}
	var keys []string
	for _, part := range p.declared {
		if part.state == "absent" {
			absent[part.key] = true
		} else if part.address.Part != "metadata" {
			keys = append(keys, part.key)
		}
	}
	if p.complete != nil {
		for _, key := range p.complete.list() {
			if !absent[key] && !strings.Contains(key, metadataPartField) {
				keys = append(keys, key)
			}
		}
	}
	for key, part := range p.mapped {
		if part.State != "absent" && part.Address.Part != "metadata" {
			keys = append(keys, key)
		}
	}
	return keys
}

// heldRowComputedLocked reports whether a held row's sender computed the
// row's step itself: its holding of the row says it executed the call, at
// the row's replacement count, or the merge's caller says the engine
// evaluated it (PreparedValueMerge.ComputedRows). A holding that took the
// Cloud's copy did neither. Requires egraphMu.
func (c *Cache) heldRowComputedLocked(prep *valueMergePrep, row *mergeRow) bool {
	if prep.computedRows[row.ordinal] {
		return true
	}
	_, h := c.holdingLocked(HolderKey{Cache: prep.from, Number: row.value.SenderNumber})
	return h != nil && h.executed && h.replacements == row.value.SenderReplacements
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
// proves a part of content (recordParts.contentKeys), and the cache stores none of its
// parts but metadata, nor has an upload of one in progress
// (MarkPartUploading, by the reader's upload claims, which an upload's end
// gives back): a run whose content no engine can fetch, nor is on its way.
// Requires egraphMu.
func (c *Cache) storedWithoutContentLocked(res *sharedResult) bool {
	if c.uploadingAny(res.id) {
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
	return len(parts.contentKeys()) > 0
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
