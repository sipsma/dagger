package dagql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/opencontainers/go-digest"
)

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

// incomingCall is the call of row's record as the merge prepared it: after
// preparation a row's record is read from prep.records, never the bundle's
// row. A held row has none.
func (commit *valueMergeCommit) incomingCall(row *mergeRow) *ResultCall {
	if commit.prep.held[row.ordinal] {
		return nil
	}
	if rec, ok := commit.prep.records[row.ordinal]; ok && rec.Call != nil {
		return rec.Call
	}
	if row.value != nil {
		return row.value.Record.Call
	}
	return nil
}

// A plain record is a value its call made whole that is entirely its record
// and the entries it names: a scalar, or an object, with no list items, no
// snapshot links, no stored or offered parts and no session binding. Two
// such records of one recipe are the same result when their values encode
// the same with each entry they name read as the entry it is in the
// receiving cache, and every entry an incoming one names is itself the same
// as the receiver's (samePlainValueLocked). Module sources and type defs are
// such objects.

// plainRecord reports whether rec is a plain record.
func plainRecord(rec PersistedRecord) bool {
	env := rec.Envelope
	switch {
	case env.Kind == persistedResultKindScalar && len(env.ScalarJSON) > 0 && len(env.ObjectJSON) == 0:
	case env.Kind == persistedResultKindObject && len(env.ObjectJSON) > 0:
	default:
		return false
	}
	return len(env.Items) == 0 && len(rec.SnapshotLinks) == 0 && len(env.StoredParts) == 0 && len(env.PendingOffers) == 0 &&
		env.SessionResourceHandle == "" && env.BoundSession == ""
}

// plainKey encodes a plain record's value for comparison, with its reference
// to itself, which names the entry it is in, left out.
func plainKey(rec PersistedRecord) ([]byte, bool) {
	selfless, err := visitValueReferences(rec, func(ref *PersistedRef) error {
		if ref.RecipeID == nil && ref.Kind == PersistedRefSelf {
			ref.ResultID = 1
		}
		return nil
	})
	if err != nil {
		return nil, false
	}
	env := selfless.Envelope
	key, err := json.Marshal(struct {
		Kind, TypeName, ObjectCodec string
		ObjectJSON, ScalarJSON      json.RawMessage
	}{env.Kind, env.TypeName, env.ObjectCodec, env.ObjectJSON, env.ScalarJSON})
	if err != nil {
		return nil, false
	}
	return key, true
}

// visitValueReferences rewrites the references in rec's value only, not in
// the call that produced it: a scalar's call names its receiver, but its
// value names nothing. rec's call is left as it is.
func visitValueReferences(rec PersistedRecord, visit PersistedRefVisitor) (PersistedRecord, error) {
	scopes, err := partitionSnapshotLinks(rec.Envelope, cloneSnapshotRefLinks(rec.SnapshotLinks))
	if err != nil {
		return PersistedRecord{}, err
	}
	env, err := visitPersistedEnvelope(rec.Envelope, rec.Call, scopes, nil, true, visit)
	if err != nil {
		return PersistedRecord{}, err
	}
	out := rec
	out.Envelope = env
	return out, nil
}

// incomingPlain is a plain record of the bundle as preparation read it: its
// entry references named by the rows' ordinals, and, when it names no other
// entry, its key.
type incomingPlain struct {
	rec   PersistedRecord
	names bool
	key   []byte
	// named are the ordinals of the rows its value names.
	named []uint64
}

// notePlainRecords records, during preparation, each plain record of the
// bundle with the entries it names read as the bundle's rows: a row its
// recipe's entry keeps is prepared from its call alone (keptMergeRecords), so
// its value is read here, from the bundle, while preparation still has it. A
// record naming an entry outside the bundle isn't recorded.
func (prep *valueMergePrep) notePlainRecords() {
	for _, id := range prep.order {
		if prep.held[id] {
			continue
		}
		row := prep.index[id]
		if row == nil || !plainRecord(row.Record) {
			continue
		}
		var named []uint64
		byOrdinal, err := visitValueReferences(row.Record, func(ref *PersistedRef) error {
			if ref.RecipeID != nil || ref.Kind == PersistedRefSelf {
				return nil
			}
			ordinal, ok := prep.rows[ref.ResultID]
			if !ok {
				return errNotPlain
			}
			named = append(named, ordinal)
			ref.ResultID = ordinal
			return nil
		})
		if err != nil {
			continue
		}
		names := len(named) > 0
		plain := incomingPlain{rec: byOrdinal, names: names, named: named}
		if !names {
			key, ok := plainKey(byOrdinal)
			if !ok {
				continue
			}
			plain.key = key
		}
		if prep.plain == nil {
			prep.plain = map[uint64]incomingPlain{}
		}
		prep.plain[id] = plain
	}
}

// errNotPlain stops reading a record that isn't plain.
var errNotPlain = errors.New("not a plain record")

// samePlainValueLocked reports whether row's record and res's own value are
// plain records (plainRecord) that encode the same: row's as preparation read
// it, with each entry it names read as the entry that entry's row lands on,
// and res's as readOwnPlainValues captured it. Each entry row's record names
// must be the receiver's own already: its row shown a copy of the entry's
// value, or landing on an entry the cache took from another. Requires
// egraphMu.
func samePlainValueLocked(commit *valueMergeCommit, row *mergeRow, res *sharedResult) bool {
	own, ok := commit.prep.ownPlain[res]
	if !ok || !own.current(res) {
		return false
	}
	incoming, ok := commit.prep.plain[row.ordinal]
	if !ok {
		return false
	}
	key := incoming.key
	if incoming.names {
		final, err := visitValueReferences(incoming.rec, func(ref *PersistedRef) error {
			if ref.RecipeID != nil || ref.Kind == PersistedRefSelf {
				return nil
			}
			named := commit.byOrdinal[ref.ResultID]
			if named == nil || named.otherRun || named.own != ownSameContent && (named.action.installs() || commitComputedHere(named)) {
				return errNotPlain
			}
			ref.ResultID = uint64(named.res.id)
			return nil
		})
		if err != nil {
			return false
		}
		if key, ok = plainKey(final); !ok {
			return false
		}
	}
	return bytes.Equal(key, own.value)
}

// commitComputedHere reports whether row landed on an entry the cache
// computed itself: decideOwnRowsLocked gave it a landing, or kept it apart.
func commitComputedHere(row *mergeRow) bool {
	return row.own != ownNone || row.otherRun
}

// capturedPlain is an entry's plain value as readOwnPlainValues captured it,
// with the entry's replacement count and payload revision then: a value the
// entry replaced since isn't compared.
type capturedPlain struct {
	value                         []byte
	replacements, payloadRevision uint64
}

// current reports whether res still holds the captured value. Requires
// egraphMu.
func (p capturedPlain) current(res *sharedResult) bool {
	res.payloadMu.RLock()
	revision := res.payloadRevision
	res.payloadMu.RUnlock()
	return res.replacements == p.replacements && revision == p.payloadRevision
}

// sameContentLocked reports whether row's record is shown a copy of res's
// value by content: both name a content digest, and it is the same.
// Requires egraphMu.
func sameContentLocked(commit *valueMergeCommit, row *mergeRow, res *sharedResult) bool {
	call := commit.incomingCall(row)
	if call == nil {
		return false
	}
	incoming := call.ContentDigest()
	if incoming == "" {
		return false
	}
	frame := res.loadResultCall()
	if frame == nil {
		return false
	}
	return frame.ContentDigest() == incoming
}

// apartCloudCopy is a Cloud entry whose record a merge kept apart from an
// entry the engine computed while the entry had no content digest yet and
// the record had one: a git tree, for one, is given its digest only once it
// has been evaluated. When the entry is taught that digest, the two are the
// same result (admitApartCloudCopyLocked).
type apartCloudCopy struct {
	number        uint64
	contentDigest digest.Digest
	expiresAtUnix int64
}

// sameHeldContentLocked reports whether a held row names its record's
// content digest (TransferredValue.HeldContent) and res's own value has it.
// Requires egraphMu.
func sameHeldContentLocked(row *mergeRow, res *sharedResult) bool {
	if row.value == nil || row.value.HeldContent == "" {
		return false
	}
	frame := res.loadResultCall()
	return frame != nil && frame.ContentDigest() == row.value.HeldContent
}

// knownContentDigestLocked returns the content digest of res's frame when it
// is known without decoding a stored body: the last decoded frame's, or the
// frame's own. A frame a restored body holds that nothing has decoded yet
// gives none. Requires egraphMu.
func (res *sharedResult) knownContentDigestLocked() digest.Digest {
	res.resultCallMu.RLock()
	known, content, call := res.frameContentKnown, res.frameContent, res.resultCall
	res.resultCallMu.RUnlock()
	if known {
		return content
	}
	if call != nil {
		return call.ContentDigest()
	}
	return ""
}

// rememberApartLocked remembers row's Cloud entry on the entry it was kept
// apart from, when the record names a content digest the entry doesn't have
// yet. Requires egraphMu.
func (commit *valueMergeCommit) rememberApartLocked(row *mergeRow) {
	if commit.from != cloudCacheID {
		return
	}
	call := commit.incomingCall(row)
	if call == nil || call.ContentDigest() == "" {
		return
	}
	if own := row.res.loadResultCall(); own != nil && own.ContentDigest() != "" {
		return
	}
	row.res.apartCloud = &apartCloudCopy{number: row.value.SenderNumber, contentDigest: call.ContentDigest(), expiresAtUnix: row.value.ExpiresAtUnix}
}

// admitApartCloudCopyLocked runs when res is taught contentDigest: a Cloud
// entry a merge kept apart from res for want of its digest is shown the same
// result now, and res learns it as its Cloud copy, as the merge would have.
// Later rows of it are compared by content as they come: a full row by its
// record, a held row by the content digest it names (sameHeldContentLocked).
// Requires egraphMu for writing.
func (res *sharedResult) admitApartCloudCopyLocked(contentDigest digest.Digest) {
	apart := res.apartCloud
	if apart == nil {
		return
	}
	res.apartCloud = nil
	if apart.contentDigest == contentDigest {
		res.noteCloudCopyLocked(apart.number, true, apart.expiresAtUnix)
	}
}

// OwnRunApart names a row a merge kept apart directly from a run of the
// cache's own (keepOwnRunsLocked), for the engine's log.
type OwnRunApart struct {
	// Field and Type are the row's call's field and result type.
	Field, Type string
	// Route is how it came: "full", with its record, or "held".
	Route string
	// Reason is why: the entry "ran itself: <how far>", "a run of its own
	// below", or "not read" (the entry wasn't a target when the merge read
	// its runs).
	Reason string
	// OwnDigest and StoredDigest report whether the entry's call and the
	// row's record each named a content digest when the merge decided.
	OwnDigest, StoredDigest bool
	// SenderNumber is the row's entry number in the sender's cache.
	SenderNumber uint64
}

// apartSamplesPerMerge bounds the rows a merge names (MergeReply.ApartSamples).
const apartSamplesPerMerge = 16

// noteApart names row, kept apart directly, in the commit's samples and its
// count by field. read reports whether readOwnRuns saw the row's entry.
// Requires egraphMu.
func (commit *valueMergeCommit) noteApart(row *mergeRow, held, read bool) {
	own := row.res.loadResultCall()
	frame := own
	if incoming := commit.incomingCall(row); incoming != nil {
		frame = incoming
	}
	sample := OwnRunApart{Route: "full", SenderNumber: row.value.SenderNumber}
	if held {
		sample.Route = "held"
	}
	if frame != nil {
		sample.Field = frame.Field
		if frame.Type != nil {
			sample.Type = frame.Type.NamedType
		}
	}
	switch state := commit.prep.ownStates[row.res]; {
	case !read:
		sample.Reason = "not read"
	case state != ownRunNone:
		sample.Reason = "ran itself: " + state.String()
	default:
		sample.Reason = "a run of its own below"
	}
	sample.OwnDigest = own != nil && own.ContentDigest() != ""
	sample.StoredDigest = commit.incomingCall(row) != nil && commit.incomingCall(row).ContentDigest() != ""
	if commit.apartFields == nil {
		commit.apartFields = map[string]int{}
	}
	commit.apartFields[sample.Field]++
	if len(commit.apartSamples) < apartSamplesPerMerge {
		commit.apartSamples = append(commit.apartSamples, sample)
	}
}

// ownRoute is how a row kept apart reached the cache: as a full row, with
// its record, or as a held row, which stands for the cache's own value
// without one.
type ownRoute uint8

const (
	ownRouteFull ownRoute = iota + 1
	ownRouteHeld
)

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
func (c *Cache) readOwnRuns(ctx context.Context, prep *valueMergePrep) {
	prep.ownRuns, prep.ownStates, prep.ownPlain = nil, nil, nil
	if c.blobBacked {
		return
	}
	var targets []*sharedResult
	isTarget := map[*sharedResult]bool{}
	below := map[*sharedResult][]*sharedResult{}
	// The targets are the entries the bundle's records land on, and the
	// entries a transferred call's inputs name, which the bundle can leave
	// out when the recipient holds them (callOverOwnRunLocked).
	recipes := make([]digest.Digest, 0, len(prep.plans))
	for _, plan := range prep.plans {
		recipes = append(recipes, plan.recipe)
	}
	for _, call := range prep.calls {
		for i, input := range call.id.inputs {
			if i >= len(call.id.witnesses) || !call.id.witnesses[i] {
				recipes = append(recipes, input)
			}
		}
	}
	c.egraphMu.RLock()
	for _, recipe := range recipes {
		res := c.currentEntryForRecipeLocked(recipe)
		if res == nil || isTarget[res] || !c.computedHereLocked(res) {
			continue
		}
		// A target can already have been walked as another's dependency:
		// it is still a target.
		isTarget[res] = true
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
	states := make(map[*sharedResult]ownRunState, len(below))
	var runAtOrBelow func(*sharedResult) bool
	runAtOrBelow = func(res *sharedResult) bool {
		if run, done := runs[res]; done {
			return run
		}
		states[res] = res.runState()
		run := states[res] != ownRunNone
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
	prep.ownStates = make(map[*sharedResult]ownRunState, len(targets))
	for _, res := range targets {
		prep.ownRuns[res] = runAtOrBelow(res)
		prep.ownStates[res] = states[res]
	}
	c.readOwnPlainValues(ctx, prep)
}

// readOwnPlainValues captures, for each target made whole by its call that a
// plain record of the bundle lands on (plainValue), the target's own record,
// so the commit can compare the two values (samePlainValueLocked). It
// captures as warming does, each target held, outside egraphMu: encoding a
// value can resolve results through egraphMu. Requires no lock.
func (c *Cache) readOwnPlainValues(ctx context.Context, prep *valueMergePrep) {
	var candidates []*sharedResult
	seen := map[*sharedResult]bool{}
	c.egraphMu.RLock()
	for ordinal, plan := range prep.plans {
		res := c.currentEntryForRecipeLocked(plan.recipe)
		if res == nil || seen[res] || prep.ownStates[res] != ownRunWhole {
			continue
		}
		if _, plain := prep.plain[ordinal]; !plain {
			continue
		}
		seen[res] = true
		candidates = append(candidates, res)
	}
	c.egraphMu.RUnlock()
	if len(candidates) == 0 {
		return
	}
	op, err := c.beginCacheOperation()
	if err != nil {
		return
	}
	defer op.finish(false)
	held, release := c.holdRowsForCapture(ctx, candidates)
	prep.ownPlain = make(map[*sharedResult]capturedPlain, len(candidates))
	for _, h := range held {
		var rec PersistedRecord
		version, err := h.run(func(imported bool, offers []PersistedPartOffer, version *capturedRowRevision) error {
			var err error
			rec, err = c.captureHeldPersistedRecord(ctx, h.row, imported, offers, version)
			return err
		})
		if err != nil || version == nil {
			continue
		}
		if !plainRecord(rec) {
			continue
		}
		value, plain := plainKey(rec)
		if !plain {
			continue
		}
		c.egraphMu.RLock()
		replacements := h.row.replacements
		c.egraphMu.RUnlock()
		prep.ownPlain[h.row] = capturedPlain{value: value, replacements: replacements, payloadRevision: version.payload.payloadRevision}
	}
	// A failure to let a target go costs nothing here, as in warming.
	_ = release()
}

// keepOwnRunsLocked keeps a merge from putting another run beside a value
// this cache computed (see above). The rows are decided
// (decideMergeRowsLocked) and nothing has changed yet. A row on an entry
// with no run of the cache's own at or below it yet lands as on any kept
// value: nothing has run that it could be put beside. It runs on every
// cache: the Cloud computes nothing, so on it no row is kept apart. Requires
// egraphMu.
func (c *Cache) keepOwnRunsLocked(commit *valueMergeCommit) {
	apart := c.decideOwnRowsLocked(commit)
	if len(apart) == 0 {
		return
	}
	dropped := commit.cascadeApart(apart)
	commit.dropApartRows(dropped)
}

// commitOwnRunsLocked records what a committed merge refused: the records
// kept apart for want of the entry's digest, compared again when it is
// taught (rememberApartLocked), and the recipes kept apart or dropped, for
// OwnRunStats.RanAfterApart. A merge's preparation decides its rows too,
// before the commit reads the cache's runs, and may never commit: only the
// commit's decisions are recorded. Requires egraphMu for writing.
func (c *Cache) commitOwnRunsLocked(commit *valueMergeCommit) {
	for _, row := range commit.apartToRemember {
		commit.rememberApartLocked(row)
	}
	c.noteApartRecipes(commit.apartRecipes)
}

// apartRecipesLimit bounds the recipes a cache remembers as kept apart or
// dropped (noteApartRecipes), for OwnRunStats.RanAfterApart: past it, newer
// ones aren't counted.
const apartRecipesLimit = 1 << 20

// noteApartRecipes remembers recipes a merge kept apart or dropped from a
// run of the cache's own, so a call of one the cache runs later is counted
// (noteCallRun).
func (c *Cache) noteApartRecipes(recipes map[digest.Digest]bool) {
	if len(recipes) == 0 {
		return
	}
	c.ownRunApartMu.Lock()
	defer c.ownRunApartMu.Unlock()
	if c.ownRunApartRecipes == nil {
		c.ownRunApartRecipes = map[digest.Digest]struct{}{}
	}
	for recipe := range recipes {
		if len(c.ownRunApartRecipes) >= apartRecipesLimit {
			return
		}
		c.ownRunApartRecipes[recipe] = struct{}{}
	}
}

// noteCallRun counts a call the cache runs whose recipe a committed merge
// kept apart or dropped from a run of its own (OwnRunStats.RanAfterApart).
func (c *Cache) noteCallRun(recipe digest.Digest) {
	c.ownRunApartMu.Lock()
	_, apart := c.ownRunApartRecipes[recipe]
	c.ownRunApartMu.Unlock()
	if apart {
		c.ownRunRanAfterApart.Add(1)
	}
}

// decideOwnRowsLocked decides each row that lands on an entry the cache
// computed: it lands as a copy shown equal by content, lands on an entry
// with no run of the cache's own yet, or is kept apart. It returns the rows
// kept apart, each with its route. Requires egraphMu.
func (c *Cache) decideOwnRowsLocked(commit *valueMergeCommit) map[uint64]ownRoute {
	// apart maps each row kept apart, or dropped, to the route of the row it
	// was first kept apart for: a full row or a held one.
	apart := map[uint64]ownRoute{}
	counts := &commit.ownRoutes
	for _, row := range commit.rows {
		if row.action.installs() {
			// An installed record lands on an entry with no value of its
			// own, or one whose expired value it replaces.
			continue
		}
		if !c.computedHereLocked(row.res) {
			continue
		}
		// A held row stands for the entry's own value but carries nothing
		// the cache could compare with it but the content digest it names:
		// the sender's run of the recipe may be another. Where the entry has
		// a run of the cache's own, it is kept apart as a full row is.
		held := commit.prep.held[row.ordinal]
		switch run, read := commit.prep.ownRuns[row.res]; {
		case !held && sameContentLocked(commit, row, row.res):
			row.own = ownSameContent
		case !held && samePlainValueLocked(commit, row, row.res):
			row.own = ownSameContent
			if plain := commit.prep.plain[row.ordinal]; plain.names {
				// Shown equal through the entries its value names: equal only
				// while those survive the cascade (cascadeApart).
				if commit.namedEqual == nil {
					commit.namedEqual = map[uint64][]uint64{}
				}
				commit.namedEqual[row.ordinal] = plain.named
			}
		case held && sameHeldContentLocked(row, row.res):
			// The held row names the content digest of the record it stands
			// for, and the entry's own value has that digest: the same
			// result, compared on every merge.
			row.own = ownSameContent
		case read && !run:
			row.own = ownNotRun
		case held:
			row.otherRun = true
			apart[row.ordinal] = ownRouteHeld
			counts.ApartHeld++
			commit.noteApart(row, true, read)
		default:
			row.otherRun = true
			apart[row.ordinal] = ownRouteFull
			counts.ApartFull++
			commit.noteApart(row, false, read)
			commit.apartToRemember = append(commit.apartToRemember, row)
		}
	}
	return apart
}

// cascadeApart carries apart, the rows kept apart, to the rows over them:
// an installing row over one is dropped, with its recipe's other rows, and a
// row landing on a kept value over one is kept apart too, as are the rows
// over those. It adds each to apart under the route of the row at the
// bottom, and returns the rows dropped.
func (commit *valueMergeCommit) cascadeApart(apart map[uint64]ownRoute) map[uint64]bool {
	counts := &commit.ownRoutes
	over := func(route ownRoute) {
		if route == ownRouteHeld {
			counts.OverApartHeld++
		} else {
			counts.OverApartFull++
		}
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
			named, conditional := commit.namedEqual[row.ordinal]
			if dropped[row.ordinal] || row.otherRun || row.own == ownSameContent && !conditional {
				// A row shown a copy of the entry's value by content lands
				// as that value, whatever run its record was computed over.
				continue
			}
			var route ownRoute
			var isApart bool
			if conditional {
				// An object shown equal through the entries its value names
				// stays so while none of those is kept apart: its call's
				// other inputs, such as its receiver, aren't in its value.
				route, isApart = namesApartRow(named, apart)
			} else {
				route, isApart = overApartRow(row, apart)
			}
			if !isApart {
				continue
			}
			apart[row.ordinal] = route
			over(route)
			if row.action.installs() {
				dropped[row.ordinal] = true
			} else {
				// A record landing on an entry that keeps its value
				// installs nothing, but its offers would give the entry
				// parts made over another run: it gives none. A record
				// over it is over another run too.
				row.otherRun, row.own = true, ownNone
			}
			changed = true
		}
		for _, row := range commit.rows {
			if !dropped[row.ordinal] || installing[row.res] != row {
				continue
			}
			for _, other := range commit.rows {
				if other.res == row.res && !dropped[other.ordinal] {
					dropped[other.ordinal] = true
					if _, counted := apart[other.ordinal]; !counted {
						apart[other.ordinal] = apart[row.ordinal]
						over(apart[row.ordinal])
					}
					changed = true
				}
			}
		}
	}
	return dropped
}

// overApartRow reports whether one of row's dependencies, or one of its
// offers' owners', is a row in apart, and that row's route. Both are named
// by ordinal.
func overApartRow(row *mergeRow, apart map[uint64]ownRoute) (ownRoute, bool) {
	for _, dep := range row.value.DependencyIDs {
		if route, ok := apart[dep]; ok {
			return route, true
		}
	}
	for _, offer := range row.value.Record.Envelope.PendingOffers {
		for _, dep := range offer.Owner.DependencyIDs {
			if route, ok := apart[dep]; ok {
				return route, true
			}
		}
	}
	return 0, false
}

// namesApartRow reports whether one of the rows named is in apart, and that
// row's route.
func namesApartRow(named []uint64, apart map[uint64]ownRoute) (ownRoute, bool) {
	for _, ordinal := range named {
		if route, ok := apart[ordinal]; ok {
			return route, true
		}
	}
	return 0, false
}

// dropApartRows takes the rows dropped out of the commit, notes the recipes
// of every row kept apart or dropped, and skips the roots among them.
func (commit *valueMergeCommit) dropApartRows(dropped map[uint64]bool) {
	// A transferred call over a row kept apart or dropped would teach the
	// cache's own run of that input the stored run's answer.
	commit.apartRecipes = map[digest.Digest]bool{}
	for _, row := range commit.rows {
		if dropped[row.ordinal] || row.otherRun {
			commit.apartRecipes[row.plan.recipe] = true
		}
	}
	commit.ownDropped = dropped
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

// ownRunState is how far an entry this cache computed has run here.
type ownRunState uint8

const (
	ownRunNone ownRunState = iota
	// ownRunPart: a part is complete or installed.
	ownRunPart
	// ownRunRunning: an evaluation is running.
	ownRunRunning
	// ownRunEvaluated: its deferred computation is done.
	ownRunEvaluated
	// ownRunWhole: it has no deferred work; its call made it whole.
	ownRunWhole
)

func (s ownRunState) String() string {
	return [...]string{"not run", "a part complete", "evaluating", "evaluated", "whole at its call"}[s]
}

// ranHere reports whether res, an entry this cache computed, has run or is
// running here (runState).
func (res *sharedResult) ranHere() bool {
	return res.runState() != ownRunNone
}

// runState reports how far res, an entry this cache computed, has run here: a
// part is complete or installed, an evaluation has started, or no
// computation of its value is left (HasPendingLazyComputation's answer). A
// value with no deferred work was made whole by its call; a lazy one nothing
// has evaluated has no run yet. It takes res's gate lock and lazyMu one at a
// time, and is never called with egraphMu held: a capture holds lazyMu while
// its encoding resolves results through egraphMu.
func (res *sharedResult) runState() ownRunState {
	if gate := res.partGate.gate.Load(); gate != nil {
		gate.mu.Lock()
		state := ownRunNone
		for _, out := range gate.outputs {
			if out.phase == PartComplete || out.phase == PartOutputInstalled {
				state = ownRunPart
			}
		}
		for _, group := range gate.groups {
			if group.phase == LazyEvaluationRunning && state < ownRunRunning {
				state = ownRunRunning
			}
			if group.phase == LazyEvaluationEvaluated && state < ownRunEvaluated {
				state = ownRunEvaluated
			}
		}
		gate.mu.Unlock()
		if state != ownRunNone {
			return state
		}
	}
	res.payloadMu.RLock()
	self := res.self
	res.payloadMu.RUnlock()
	res.lazyMu.Lock()
	defer res.lazyMu.Unlock()
	if lazyEvaluationInFlightLocked(res) != nil {
		return ownRunRunning
	}
	if reporting, ok := UnwrapAs[HasLazyEvaluationReporting](self); ok {
		if pendingLazyComputationLocked(res, reporting) {
			return ownRunNone
		}
		return ownRunEvaluated
	}
	if res.lazyEvalComplete {
		return ownRunEvaluated
	}
	if !res.lazyWhole.settled() {
		return ownRunNone
	}
	for _, group := range res.lazyPartGroups {
		if !group.settled() {
			return ownRunNone
		}
	}
	if lazy, ok := UnwrapAs[HasLazyEvaluation](self); ok && lazy.LazyEvalFunc() != nil {
		return ownRunNone
	}
	return ownRunWhole
}

// OwnRunStats counts, since the cache opened, what keeping its own runs
// refused and let through: the rows its merges' commits kept apart or let
// land on entries it computed (MergeRowCounts), and the parts OfferParts
// refused or attached.
type OwnRunStats struct {
	// RowsKeptApart, RowsSameContent and RowsNotRun are the committed merge
	// rows counted as MergeRowCounts' OtherRun, OwnSameContent and OwnNotRun.
	RowsKeptApart, RowsSameContent, RowsNotRun uint64
	// ApartFull, ApartHeld, OverApartFull and OverApartHeld are the
	// committed merges' MergeRowCounts of the same names.
	ApartFull, ApartHeld, OverApartFull, OverApartHeld uint64
	// OffersRefused are the parts OfferParts refused for rows with a run of
	// this cache's own below them, and OffersLent the parts it attached.
	OffersRefused, OffersLent uint64
	// RanAfterApart are the calls the cache ran after a committed merge had
	// kept apart or dropped a stored run of their recipe from a run of its
	// own: an upper bound on the work the rule cost. A call run again for
	// another reason, such as its entry expiring, counts too.
	RanAfterApart uint64
}

// noteOwnRunRows adds a committed merge's rows to OwnRunStats.
func (c *Cache) noteOwnRunRows(rows MergeRowCounts) {
	c.ownRunRowsKeptApart.Add(uint64(rows.OtherRun))
	c.ownRunRowsSameContent.Add(uint64(rows.OwnSameContent))
	c.ownRunRowsNotRun.Add(uint64(rows.OwnNotRun))
	c.ownRunApartFull.Add(uint64(rows.ApartFull))
	c.ownRunApartHeld.Add(uint64(rows.ApartHeld))
	c.ownRunOverApartFull.Add(uint64(rows.OverApartFull))
	c.ownRunOverApartHeld.Add(uint64(rows.OverApartHeld))
}

// OwnRunStats reports what keeping the cache's own runs refused and let
// through.
func (c *Cache) OwnRunStats() OwnRunStats {
	return OwnRunStats{
		RowsKeptApart:   c.ownRunRowsKeptApart.Load(),
		RowsSameContent: c.ownRunRowsSameContent.Load(),
		RowsNotRun:      c.ownRunRowsNotRun.Load(),
		ApartFull:       c.ownRunApartFull.Load(),
		ApartHeld:       c.ownRunApartHeld.Load(),
		OverApartFull:   c.ownRunOverApartFull.Load(),
		OverApartHeld:   c.ownRunOverApartHeld.Load(),
		OffersRefused:   c.ownRunOffersRefused.Load(),
		OffersLent:      c.ownRunOffersLent.Load(),
		RanAfterApart:   c.ownRunRanAfterApart.Load(),
	}
}
