package dagql

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"
)

// StoredBundleResult is StoredBundle's answer.
type StoredBundleResult struct {
	// Bundle has the records of the roots' closure and outputs for its parts,
	// without addresses or renewal keys.
	Bundle ValueBundle
	// Donors has, for each of Bundle.Outputs, its storing entry: the Cloud
	// entry whose stored parts hold it, the closure entry itself or, for a
	// part borrowed from its class, the donor. The service names that entry
	// in the output's RenewalKey.
	Donors []uint64
	// Gone are the roots the cache has no entry for, and NoValue the roots
	// known only through holdings.
	Gone    []uint64
	NoValue []uint64
	// Held are the numbers of the entries the bundle carries as held rows.
	Held []uint64
	// CallInputs are the numbers of the entries the bundle carries as
	// roots only because a call it sends has them as inputs, and
	// UnsendableCalls counts the terms of its entries' kept calls it sends
	// no call for: none had inputs it could place.
	CallInputs      []uint64
	UnsendableCalls int
	// RootOwners has, for each of Bundle.Roots, the index among the roots it
	// was asked for of the one it travels with: itself, or, for an entry a
	// call's input needs, the first root whose call needs it. StoredSizes
	// prices each root with what travels with it.
	RootOwners []int
	// stored has, by ordinal, the declared parts of each value the bundle
	// carries compressed, as held, which its check reads in place of the
	// value's envelope (checkValueBundle).
	stored map[uint64][]declaredPart
}

// HeldBy names the entries of a blob-backed cache whose values a recipient
// engine cache already holds, for StoredBundle to carry as held rows and
// StoredSizes to price as such. An entry is held when it has a holding of
// Cache that hasn't expired at the read's clock, and isn't in Not, by
// number: the values the recipient answered stale. Only an entry with a call
// frame can be held: its recipe names it to the recipient.
type HeldBy struct {
	Cache CacheID
	Not   map[uint64]bool
}

// heldLocked reports whether the recipient holds res at nowUnix. A nil
// HeldBy holds nothing. Requires egraphMu.
func (h *HeldBy) heldLocked(res *sharedResult, nowUnix int64) bool {
	if h == nil || !res.hasCallFrameLocked() {
		return false
	}
	if h.Not[uint64(res.id)] {
		return false
	}
	return h.holdsLocked(res, nowUnix)
}

// holdsLocked reports whether res has a holding of h.Cache that hasn't
// expired at nowUnix: among its holdings of that cache only (cacheHolders),
// each judged by its own expiry. Requires egraphMu.
func (h *HeldBy) holdsLocked(res *sharedResult, nowUnix int64) bool {
	for number := range res.cacheHolders[h.Cache] {
		held := res.holders[HolderKey{Cache: h.Cache, Number: number}]
		if held != nil && (held.expiresAtUnix == 0 || held.expiresAtUnix > nowUnix) {
			return true
		}
	}
	return false
}

// StoredBundle builds a bundle from the entries of a blob-backed cache, the
// Cloud's, for a merge into an engine (design 7.5). In one hold of egraphMu it
// walks the closure of the roots, over their dependencies and the entries
// that the owners and services of their own stored parts name, and copies
// every record: a replacement of a stored record, which takes the lock for
// writing, never mixes into it. Each record goes as stored, naming itself
// and its references by the Cloud's entry numbers, with the entry's number as
// SenderNumber and its replacement count; the rest of the bundle names rows
// by ordinal.
//
// Outputs follow section 5.1 point 3:
//   - an entry's own stored parts travel with its record, expired or not, as
//     they belong to that value, with their owners and services;
//   - a part the entry's record declares and it doesn't store is borrowed
//     from an unexpired entry of its class, the lowest number first, through
//     the placement rule, with its services mapped to rows of this closure of
//     the same class. A part whose services can't be mapped is left out.
//
// An entry held says the recipient holds, a root or not, becomes a held row:
// its recipe and sending facts, with no record, no outputs, and nothing
// below it walked. Each held row's recipe is derived from the entry's call
// frame after egraphMu is released, or read from the stored body that holds
// the frame: a replacement keeps the recipe, and deriving can read the
// cache. A nil held is a bundle with no held rows.
//
// Expiry, of a donor of its class and of a holding of held's cache, is
// judged at nowUnix, the reader's clock, so a bundle agrees with what its
// reader judged of the cache at that moment.
func (c *Cache) StoredBundle(ctx context.Context, roots []uint64, nowUnix int64, held *HeldBy) (StoredBundleResult, error) {
	var out StoredBundleResult
	if !c.blobBacked {
		return out, fmt.Errorf("stored bundle: the cache has no blob store")
	}
	if err := context.Cause(ctx); err != nil {
		return out, err
	}
	out, frames, err := c.storedBundle(roots, nowUnix, held)
	if err != nil || len(out.Bundle.Values) == 0 {
		return out, err
	}
	for i := range out.Bundle.Values {
		value := &out.Bundle.Values[i]
		frame, ok := frames[value.Ordinal]
		if !ok {
			continue
		}
		if value.Held, err = frame.recipeDigest(c); err != nil {
			return StoredBundleResult{}, fmt.Errorf("stored bundle: held entry %d recipe: %w", value.SenderNumber, err)
		}
	}
	if err := validateStoredBundle(out.Bundle, out.stored); err != nil {
		return StoredBundleResult{}, fmt.Errorf("stored bundle: %w", err)
	}
	return out, nil
}

// storedBundle builds StoredBundle's answer, with held rows' recipes left
// empty, and returns the call frame of each held row by ordinal. It reads
// the cache under egraphMu.RLock in pieces (readInPieces), letting it go
// between them, so that a writer waits for one piece, not the whole build:
// the roots, the closure's walk, the calls kept on it, the roots' expiry and
// which parts each entry's outputs carry, each entry read by its number
// once the read holds the lock for it. What it reads of each entry it takes
// as the cache holds it, which the cache replaces and never changes in
// place: the record's body or kept transfer record, the stored parts' values,
// the call frame. It copies them, clones the parts' offers and relocates
// them after it lets the lock go (finishStoredBundle).
//
// The bundle needs no one state of the cache, only each value agreeing
// with the closure it names: an entry is added once all of its current
// dependencies are in the bundle, read in the same piece as what it takes of
// it (storedBundleVisitLocked), so no value names a row the bundle lacks. An
// entry replaced after that goes as it was a moment before; an engine
// answers a held row it no longer holds stale. With no write between the
// pieces, the bundle is the one a single hold builds.
func (c *Cache) storedBundle(roots []uint64, nowUnix int64, held *HeldBy) (StoredBundleResult, map[TransferOrdinal]entryFrame, error) {
	var out StoredBundleResult
	hold := c.readInPieces()
	hold.report = c.testStoredBundleHold
	built, err := func() (*storedBundleBuild, error) {
		defer hold.done()
		var rootEntries []*sharedResult
		seenRoot := map[sharedResultID]bool{}
		for _, number := range roots {
			hold.step()
			res := c.resultsByID[sharedResultID(number)]
			switch {
			case res == nil:
				out.Gone = append(out.Gone, number)
			case res.noValueLocked():
				out.NoValue = append(out.NoValue, number)
			case !seenRoot[res.id]:
				seenRoot[res.id] = true
				rootEntries = append(rootEntries, res)
			}
		}
		if len(rootEntries) == 0 {
			return nil, nil
		}
		build := &storedBundleBuild{now: nowUnix, ordinals: map[sharedResultID]TransferOrdinal{}, heldBy: held, held: map[sharedResultID]bool{},
			owners: map[sharedResultID]int{}, ordinary: map[sharedResultID]bool{}, rootSeen: seenRoot, hold: hold, snaps: map[sharedResultID]*storedEntrySnap{}}
		build.bundle.Version = valueBundleVersion
		visiting := map[sharedResultID]bool{}
		// Root by root, as StoredSizes prices them: its closure, then the calls
		// kept on what it added, with their inputs' closures.
		var walked []*sharedResult
		for _, res := range rootEntries {
			// Its index among the bundle's roots: a root gone before it is
			// none of them.
			build.current = len(walked)
			start := len(build.order)
			gone, noValue, err := c.storedBundleRootLocked(build, res, visiting)
			switch {
			case err != nil:
				return nil, err
			case gone:
				out.Gone = append(out.Gone, uint64(res.id))
				continue
			case noValue:
				out.NoValue = append(out.NoValue, uint64(res.id))
				continue
			}
			walked = append(walked, res)
			// Only the build's own state: done with the lock let go.
			hold.unlocked(func() { build.markOrdinary(res, start) })
			if err := c.storedBundleCallsLocked(build, start); err != nil {
				return nil, fmt.Errorf("stored bundle: %w", err)
			}
		}
		if len(walked) == 0 {
			return nil, nil
		}
		build.roots = slices.Concat(walked, build.callRoots)
		// Only the build's own state: done with the lock let go.
		hold.unlocked(build.dropUnreachable)
		for _, res := range build.roots {
			hold.step()
			expiry := build.snaps[res.id].expiresAtUnix
			if edge, ok := c.persistedEdgesByResult[res.id]; ok {
				expiry = edge.expiresAtUnix
			}
			build.rootExpiry = append(build.rootExpiry, expiry)
		}
		for _, res := range build.order {
			if build.held[res.id] {
				continue
			}
			hold.step()
			c.storedBundleOutputsLocked(build, res)
		}
		return build, nil
	}()
	if err != nil || built == nil {
		return out, nil, err
	}
	frames, err := c.finishStoredBundle(built, &out)
	if err != nil {
		return StoredBundleResult{}, nil, err
	}
	return out, frames, nil
}

// storedBundleRootLocked walks a root's closure (storedBundleVisitLocked).
// The cache may change between the pieces of the read: a root gone, or left
// with no value, meanwhile is reported so, as one found so before the build;
// a walk that fails for an entry gone or valueless below it is walked once
// more from the root, read again, and its error is the build's only if it
// fails again, as it would in a single hold of that state. What a failed
// walk added and no root reaches is dropped (dropUnreachable). Requires
// egraphMu.
func (c *Cache) storedBundleRootLocked(build *storedBundleBuild, res *sharedResult, visiting map[sharedResultID]bool) (gone, noValue bool, err error) {
	for attempt := 0; ; attempt++ {
		clear(visiting)
		err = c.storedBundleVisitLocked(build, res, visiting)
		if err == nil {
			return false, false, nil
		}
		cur := c.resultsByID[res.id]
		switch {
		case cur == nil:
			return true, false, nil
		case cur.noValueLocked():
			return false, true, nil
		case attempt > 0 || build.hold == nil:
			if errors.Is(err, errStoredEntryGone) {
				return true, false, nil
			}
			return false, false, err
		}
		res = cur
	}
}

// markOrdinary marks the rows a root's walk added from start that its
// closure reaches, through what each named when it was added
// (storedEntrySnap.children), as the bundle's ordinary closure, which a
// kept call's inputs may be placed on: a row the walk added for an entry
// replaced between the read's pieces with a record that no longer names it
// isn't one, and is dropped (dropUnreachable). It reads only the build's
// own state, so it needs no lock. With no write during the build, every row
// the walk added is reached.
func (build *storedBundleBuild) markOrdinary(root *sharedResult, start int) {
	if build.snaps == nil {
		for _, entry := range build.order[start:] {
			build.ordinary[entry.id] = true
		}
		return
	}
	added := make(map[sharedResultID]bool, len(build.order)-start)
	for _, entry := range build.order[start:] {
		added[entry.id] = true
	}
	reached := map[sharedResultID]bool{}
	var reach func(id sharedResultID)
	reach = func(id sharedResultID) {
		if reached[id] || build.ordinals[id] == 0 {
			return
		}
		reached[id] = true
		if added[id] {
			build.ordinary[id] = true
		}
		for _, child := range build.snaps[id].children {
			reach(child)
		}
	}
	reach(root.id)
}

// dropUnreachable drops from the build the rows no root reaches, through
// what each row named when it was added (storedEntrySnap.children), and
// numbers the rest again in order: a row a walk added that the entry it was
// added for no longer names, replaced between the read's pieces, or that a
// walk that failed added. With no write during the build, every row is
// reached and nothing changes. The calls kept on a dropped row go with it.
// It reads only the build's own state, so it needs no lock; it runs before
// the outputs are chosen.
func (build *storedBundleBuild) dropUnreachable() {
	if build.snaps == nil {
		return
	}
	reached := make(map[sharedResultID]bool, len(build.order))
	var reach func(id sharedResultID)
	reach = func(id sharedResultID) {
		if reached[id] || build.ordinals[id] == 0 {
			return
		}
		reached[id] = true
		for _, child := range build.snaps[id].children {
			reach(child)
		}
	}
	for _, res := range build.roots {
		reach(res.id)
	}
	if len(reached) == len(build.order) {
		return
	}
	kept := build.order[:0]
	for _, res := range build.order {
		if !reached[res.id] {
			delete(build.ordinals, res.id)
			delete(build.snaps, res.id)
			delete(build.held, res.id)
			delete(build.owners, res.id)
			continue
		}
		kept = append(kept, res)
		build.ordinals[res.id] = TransferOrdinal(len(kept))
	}
	build.order = kept
	calls := build.calls[:0]
	for _, call := range build.calls {
		if reached[call.value] {
			calls = append(calls, call)
		}
	}
	build.calls = calls
}

// errStoredEntryGone is a root the cache no longer has, found between the
// pieces of a build's read.
var errStoredEntryGone = errors.New("stored bundle: entry gone")

// storedEntrySnap is what a build reads of a closure entry under egraphMu,
// in the piece it adds the entry in, to make its value after it lets the lock
// go (finishStoredBundle): the facts a value carries, the dependencies it
// names, its record, and the stored parts its outputs carry, taken as the
// cache holds them, values it replaces whole and never changes in place.
type storedEntrySnap struct {
	id            sharedResultID
	replacements  uint64
	expiresAtUnix int64
	deps          []sharedResultID
	// frame is a held row's call frame.
	frame entryFrame
	// body, refs and parts are a compressed record's, and call the decoded
	// frame normalized after the lock if the body holds none; kept is a
	// record held whole, normalized for transfer.
	body  *storedBody
	refs  []uint64
	parts []declaredPart
	call  *ResultCall
	kept  *storedTransferRecord
	// own are its own stored parts, by key, ownKeys their keys, declared the
	// parts its record declares absent or pending, and borrowed the parts it
	// borrows from its class (storedBundleOutputsLocked); children are what
	// the build walked below it (storedClosureChildrenLocked).
	own      []storedPart
	ownKeys  map[string]bool
	declared []declaredPart
	borrowed []storedBorrowedPart
	children []sharedResultID
}

// storedBorrowedPart is a part an entry borrows from an entry of its class
// (donor), and the closure row each of its services maps to.
type storedBorrowedPart struct {
	part  storedPart
	donor sharedResultID
	rows  map[uint64]uint64
}

// snapStoredEntryLocked reads what a value of res needs (storedEntrySnap).
// A record held whole is normalized here, once for each of its versions
// (storedTransferLocked); a compressed one isn't. Requires egraphMu.
func (c *Cache) snapStoredEntryLocked(build *storedBundleBuild, res *sharedResult) (*storedEntrySnap, error) {
	snap := &storedEntrySnap{id: res.id, replacements: res.replacements, expiresAtUnix: res.expiresAtUnix}
	if build.held[res.id] {
		snap.frame = res.entryFrameLocked()
		return snap, nil
	}
	for dep := range res.deps {
		snap.deps = append(snap.deps, dep)
	}
	slices.Sort(snap.deps)
	if body := res.stored; body != nil {
		refs, parts, err := res.storedFactsLocked()
		if err != nil {
			return nil, fmt.Errorf("stored bundle: entry %d: %w", res.id, err)
		}
		snap.body, snap.refs, snap.parts = body, refs, parts
		if body.frame == nil {
			snap.call = res.loadResultCall()
		}
	} else {
		kept, err := c.storedTransferLocked(res)
		if err != nil {
			return nil, fmt.Errorf("stored bundle: entry %d: %w", res.id, err)
		}
		snap.kept, snap.refs = kept, kept.refs
	}
	declared, err := res.declaredPartsLocked()
	if err != nil {
		return nil, fmt.Errorf("stored bundle: entry %d: %w", res.id, err)
	}
	snap.declared = declared
	snap.ownKeys = make(map[string]bool, len(res.storedParts))
	for _, key := range slices.Sorted(maps.Keys(res.storedParts)) {
		snap.own = append(snap.own, res.storedParts[key])
		snap.ownKeys[key] = true
	}
	snap.children = storedClosureChildrenLocked(res)
	return snap, nil
}

// finishStoredBundle makes the bundle of what the build read (storedBundle),
// with no lock: each value of its entry's snapshot, the roots, and the
// outputs, their offers cloned and relocated to ordinals. It returns each
// held row's call frame by ordinal.
func (c *Cache) finishStoredBundle(build *storedBundleBuild, out *StoredBundleResult) (map[TransferOrdinal]entryFrame, error) {
	var frames map[TransferOrdinal]entryFrame
	for _, res := range build.order {
		snap := build.snaps[res.id]
		ordinal := build.ordinals[res.id]
		if build.held[res.id] {
			build.bundle.Values = append(build.bundle.Values, TransferredValue{Ordinal: ordinal, SenderNumber: uint64(res.id), SenderReplacements: snap.replacements, Record: PersistedRecord{ResultID: uint64(res.id)}, ExpiresAtUnix: snap.expiresAtUnix})
			if frames == nil {
				frames = map[TransferOrdinal]entryFrame{}
			}
			frames[ordinal] = snap.frame
			out.Held = append(out.Held, uint64(res.id))
			continue
		}
		if err := build.addSnapValue(snap); err != nil {
			return nil, err
		}
	}
	for _, call := range build.calls {
		build.bundle.Calls = append(build.bundle.Calls, TransferredCall{Call: call.encoded, Value: build.ordinals[call.value]})
	}
	for i, res := range build.roots {
		build.bundle.Roots = append(build.bundle.Roots, TransferredRoot{Ordinal: build.ordinals[res.id], ExpiresAtUnix: build.rootExpiry[i]})
	}
	for _, res := range build.order {
		if build.held[res.id] {
			continue
		}
		snap := build.snaps[res.id]
		for _, part := range snap.own {
			if err := build.addOutput(res.id, part.offer(), res.id); err != nil {
				return nil, err
			}
		}
		for _, b := range snap.borrowed {
			placed, ok := placeOwnedPart(b.part.offer(), func(service uint64) (uint64, bool) {
				row, ok := b.rows[service]
				return row, ok
			})
			if !ok {
				continue
			}
			if err := build.addOutput(res.id, placed, b.donor); err != nil {
				return nil, err
			}
		}
	}
	out.Bundle, out.Donors, out.stored = build.bundle, build.donors, build.stored
	out.CallInputs, out.UnsendableCalls = build.callInputs, build.unsendableCalls
	for _, res := range build.roots {
		out.RootOwners = append(out.RootOwners, build.owners[res.id])
	}
	return frames, nil
}

// storedBundleBuild is a stored bundle under construction: the closure in
// dependency order, each entry's ordinal, and the outputs' storing entries.
type storedBundleBuild struct {
	now      int64
	order    []*sharedResult
	ordinals map[sharedResultID]TransferOrdinal
	bundle   ValueBundle
	donors   []uint64
	// heldBy says which entries the recipient holds, and held are the
	// closure's entries that become held rows.
	heldBy *HeldBy
	held   map[sharedResultID]bool
	// rootSeen are the roots' entries, and callRoots the entries added as
	// roots for the inputs of calls the bundle sends, callInputs their
	// numbers, and unsendableCalls the terms it sends no call for
	// (storedBundleCallsLocked).
	rootSeen        map[sharedResultID]bool
	callRoots       []*sharedResult
	callInputs      []uint64
	unsendableCalls int
	// owners has, for each entry the build added, the index of the root it
	// was asked for that added it, or whose call's input it is (current, as
	// it's added); ordinary are those the roots' closures added, not their
	// calls' inputs.
	owners   map[sharedResultID]int
	current  int
	ordinary map[sharedResultID]bool
	// stored has, by ordinal, the declared parts of each value the build
	// carries compressed (addStoredValue).
	stored map[uint64][]declaredPart
	// hold reads the cache in pieces, or nil in a single hold; snaps are what
	// it read of each entry it added (storedEntrySnap), nil when the build
	// copies each value under the lock as it goes; roots and rootExpiry are
	// the bundle's roots, and each one's expiry, as read.
	hold  *readPieces
	snaps map[sharedResultID]*storedEntrySnap
	// unnormalized are the records whose normalizing failed with the lock
	// let go (normalizeUnlocked).
	unnormalized map[sharedResultID]bool
	// calls are the kept calls the bundle sends, each with the entry it is
	// kept on, numbered by ordinal as the bundle is finished.
	calls      []storedBundleCall
	roots      []*sharedResult
	rootExpiry []int64
}

// storedBundleCall is a kept call a bundle sends, encoded, and the entry it
// is kept on.
type storedBundleCall struct {
	encoded string
	value   sharedResultID
}

// relocate rewrites a reference to an entry of the closure to its ordinal.
func (build *storedBundleBuild) relocate(ref *PersistedRef) error {
	if ref.RecipeID != nil || ref.ResultID == 0 {
		return nil
	}
	ordinal := build.ordinals[sharedResultID(ref.ResultID)]
	if ordinal == 0 {
		return fmt.Errorf("stored bundle: reference to entry %d outside the closure", ref.ResultID)
	}
	ref.ResultID = uint64(ordinal)
	return nil
}

// storedBundleVisitLocked adds res's closure to the build in dependency
// order: its dependencies, and the entries its own stored parts' owners and
// services name, before it. An entry the recipient holds is added alone, as
// a held row. With a read in pieces (build.hold), an entry is read by its
// number once the read holds the lock for it, as the pointer it was reached
// by may be from before a piece let the lock go, and is added only once
// every one of its current children is in the build, read in the piece it is
// added in, with what its value needs (snapStoredEntryLocked): an entry that
// gained a child while its children were walked has that child walked too.
// A root gone meanwhile is errStoredEntryGone. Requires egraphMu.
func (c *Cache) storedBundleVisitLocked(build *storedBundleBuild, res *sharedResult, visiting map[sharedResultID]bool) error {
	if build.ordinals[res.id] != 0 {
		return nil
	}
	if visiting[res.id] {
		return fmt.Errorf("stored bundle: dependency cycle through entry %d", res.id)
	}
	if build.hold != nil {
		build.hold.step()
		if res = c.resultsByID[res.id]; res == nil {
			return errStoredEntryGone
		}
	}
	if build.heldBy.heldLocked(res, build.now) {
		build.held[res.id] = true
		return build.add(c, res)
	}
	if res.noValueLocked() || res.persistedEnvelope == nil {
		return fmt.Errorf("stored bundle: entry %d in the closure has no stored record", res.id)
	}
	visiting[res.id] = true
	for {
		for _, id := range storedClosureChildrenLocked(res) {
			child := c.resultsByID[id]
			if child == nil {
				return fmt.Errorf("stored bundle: entry %d names missing entry %d", res.id, id)
			}
			if err := c.storedBundleVisitLocked(build, child, visiting); err != nil {
				if errors.Is(err, errStoredEntryGone) {
					return fmt.Errorf("stored bundle: entry %d names missing entry %d", res.id, id)
				}
				return err
			}
		}
		if build.hold == nil {
			break
		}
		// The entry's own reading, after its children, is a step of its
		// own: a chain deeper than a piece is read on the way back up in
		// pieces too. It is read again by its number, as a piece may have
		// let the lock go, and added only if every one of its children is
		// in the build now, in this piece; a child it gained meanwhile is
		// walked first.
		build.hold.step()
		if res = c.resultsByID[res.id]; res == nil {
			return errStoredEntryGone
		}
		if res.noValueLocked() || res.persistedEnvelope == nil {
			return fmt.Errorf("stored bundle: entry %d in the closure has no stored record", res.id)
		}
		missing := false
		for _, id := range storedClosureChildrenLocked(res) {
			if build.ordinals[id] == 0 {
				missing = true
				break
			}
		}
		if missing {
			continue
		}
		// A record held whole is normalized with the lock let go, then read
		// again.
		if c.normalizeUnlocked(build, res) {
			continue
		}
		break
	}
	delete(visiting, res.id)
	return build.add(c, res)
}

// normalizeUnlocked normalizes res's record held whole, if its kept transfer
// record isn't of its envelope and call, with egraphMu let go
// (readPieces.unlocked): normalizing reads only the envelope and call, which
// the cache replaces and never changes in place, and decodes and validates
// every payload, which held the lock for hundreds of milliseconds on a
// fresh merge's records, until the body worker compresses them. The result
// is kept if they are still res's when the lock is taken again. It reports
// whether it let the lock go; the caller reads res again. A compressed
// record, or a read in a single hold, needs none. Requires egraphMu.
func (c *Cache) normalizeUnlocked(build *storedBundleBuild, res *sharedResult) bool {
	if build.hold == nil || build.held[res.id] || res.stored != nil || res.persistedEnvelope == nil || build.unnormalized[res.id] {
		return false
	}
	env, call := res.persistedEnvelope, res.loadResultCall()
	if kept := res.storedTransfer.Load(); kept != nil && kept.envelope == env && kept.call == call {
		return false
	}
	var normalized *storedTransferRecord
	var err error
	build.hold.unlocked(func() {
		normalized, err = normalizeStoredTransfer(res, env, call)
		if err == nil && c.testStoredTransferNormalized != nil {
			c.testStoredTransferNormalized()
		}
	})
	// A record that fails normalizing fails it again under the lock, where
	// the build reports it (snapStoredEntryLocked), and isn't tried here
	// again.
	if err != nil {
		if build.unnormalized == nil {
			build.unnormalized = map[sharedResultID]bool{}
		}
		build.unnormalized[res.id] = true
	} else {
		if cur := c.resultsByID[res.id]; cur != nil && cur.persistedEnvelope == env && cur.loadResultCall() == call {
			keepStoredTransfer(cur, normalized)
		}
	}
	return true
}

// add appends res to the build's order and gives it its ordinal, with what
// its value needs read now, in the same piece as its children
// (snapStoredEntryLocked), when the build reads in pieces. Requires
// egraphMu.
func (build *storedBundleBuild) add(c *Cache, res *sharedResult) error {
	if build.snaps != nil {
		snap, err := c.snapStoredEntryLocked(build, res)
		if err != nil {
			return err
		}
		build.snaps[res.id] = snap
	}
	build.order = append(build.order, res)
	build.ordinals[res.id] = TransferOrdinal(len(build.order))
	build.owners[res.id] = build.current
	return nil
}

// storedClosureChildrenLocked returns what a stored bundle walks below res,
// sorted: its dependencies, and the entries its own stored parts' owners and
// services name. Requires egraphMu.
func storedClosureChildrenLocked(res *sharedResult) []sharedResultID {
	var children []sharedResultID
	for dep := range res.deps {
		children = append(children, dep)
	}
	for _, part := range res.storedParts {
		for _, dep := range part.held.Owner.DependencyIDs {
			children = append(children, sharedResultID(dep))
		}
		for _, service := range part.held.Value.Services {
			children = append(children, sharedResultID(service.ServiceResultID))
		}
	}
	slices.Sort(children)
	return slices.Compact(children)
}

// storedBundleValueLocked copies res's stored record into the build,
// normalized for transfer, as stored, under egraphMu, as a build in a single
// hold does. Requires egraphMu.
func (c *Cache) storedBundleValueLocked(build *storedBundleBuild, res *sharedResult) error {
	snap, err := c.snapStoredEntryLocked(build, res)
	if err != nil {
		return err
	}
	return build.addSnapValue(snap)
}

// addSnapValue adds the value of an entry's snapshot to the build: a
// compressed record as it is held, its frame too when the body holds it, or
// a plain copy of a record held whole, which the bundle's caller may change.
func (build *storedBundleBuild) addSnapValue(snap *storedEntrySnap) error {
	id := uint64(snap.id)
	if body := snap.body; body != nil {
		// A body goes as it is held, compressed, and the bundle's encoding
		// writes it out (TransferredValue.MarshalJSON).
		if build.stored == nil {
			build.stored = map[uint64][]declaredPart{}
		}
		build.stored[uint64(build.ordinals[snap.id])] = snap.parts
		if body.frame != nil {
			// So does the frame it holds, which is its transfer form.
			if err := build.addStoredValue(snap, PersistedRecord{ResultID: id}, body.envelope); err != nil {
				return err
			}
			value := &build.bundle.Values[len(build.bundle.Values)-1]
			value.storedCall, value.storedCallBytes = body.frame, body.frameBytes
			return nil
		}
		call, err := normalizeTransferCall(snap.call)
		if err != nil {
			return fmt.Errorf("stored bundle: entry %d: %w", id, err)
		}
		return build.addStoredValue(snap, PersistedRecord{ResultID: id, Call: call}, body.envelope)
	}
	// The record goes as stored, naming itself and its references by the
	// Cloud's entry numbers, which the receiver maps (transferSenderNumbers):
	// nothing in it is decoded or rewritten. The bundle is its caller's to
	// change, so it carries a plain copy.
	rec := snap.kept.record
	var err error
	if rec.Envelope, err = clonePersistedEnvelope(rec.Envelope); err != nil {
		return fmt.Errorf("stored bundle: entry %d: %w", id, err)
	}
	rec.Call = rec.Call.clone()
	return build.addStoredValue(snap, rec, nil)
}

// addStoredValue adds an entry's record to the build, with its envelope
// compressed in storedEnvelope when it is held so. The bundle is validated by
// shape (validateStoredBundle), so each reference is checked against the
// record's dependencies here, from what normalizing the record found it names
// (snap.refs).
func (build *storedBundleBuild) addStoredValue(snap *storedEntrySnap, rec PersistedRecord, storedEnvelope []byte) error {
	for _, ref := range snap.refs {
		if _, ok := slices.BinarySearch(snap.deps, sharedResultID(ref)); !ok {
			return fmt.Errorf("stored bundle: entry %d: reference to %d is not a direct dependency", snap.id, ref)
		}
	}
	value := TransferredValue{Ordinal: build.ordinals[snap.id], SenderNumber: uint64(snap.id), SenderReplacements: snap.replacements, Record: rec, ExpiresAtUnix: snap.expiresAtUnix, storedEnvelope: storedEnvelope}
	for _, dep := range snap.deps {
		value.DependencyIDs = append(value.DependencyIDs, uint64(build.ordinals[dep]))
	}
	slices.Sort(value.DependencyIDs)
	build.bundle.Values = append(build.bundle.Values, value)
	return nil
}

// validateStoredBundle checks a stored bundle by its shape
// (validateValueBundleShape): every check of the bundle as a whole, and of
// each record without its payloads, a record it carries compressed by its
// call and declared parts (checkValueBundle's stored). The rest of a record's
// checks are made as the bundle is built: its payloads are validated by
// their codecs and its outputs mapped once for each of its versions, when it
// is normalized (normalizeStoredTransfer) or compressed (newStoredBody), and
// each reference is checked against its dependencies (addStoredValue).
func validateStoredBundle(bundle ValueBundle, stored map[uint64][]declaredPart) error {
	_, err := checkValueBundle(bundle, true, stored)
	return err
}

// storedTransferRecord is an entry's stored record normalized for transfer,
// without offers, and the envelope and call it was normalized from.
type storedTransferRecord struct {
	envelope *PersistedResultEnvelope
	call     *ResultCall
	record   PersistedRecord
	// refs are the entries the record's child and call references name,
	// which a bundle checks against the entry's dependencies, and parts the
	// parts it declares absent or pending.
	refs  []uint64
	parts []declaredPart
	// size is the entry's value's encoded size in a bundle, once StoredSizes
	// has measured it (measureStoredValue), with what else it read.
	size atomic.Pointer[storedValueSize]
}

// storedValueSize is a measured value size, and the entry's dependency
// revision, replacement count and expiry it was measured at: a value carries
// its dependencies, which a stored part can add to without replacing the
// record, and the other two.
type storedValueSize struct {
	// call is the frame measured with a compressed record (storedBody),
	// whose decoded call can be replaced while its body stays; nil for a
	// body that holds its frame, which is replaced with the body.
	call               *ResultCall
	dependencyRevision uint64
	replacements       uint64
	expiresAtUnix      int64
	bytes              int64
}

// storedTransferRecordLocked returns res's stored record normalized for
// transfer, without offers, and checked as validateStoredBundle relies on.
// Normalizing reads only the record's envelope and
// call, which the cache replaces and never changes in place, so the entry
// keeps the result with them and normalizes again once either is replaced.
// The kept record holds both, so neither can be freed and another take its
// address while it is kept. The result is shared: callers copy what they
// change. Requires egraphMu.
func (c *Cache) storedTransferRecordLocked(res *sharedResult) (PersistedRecord, error) {
	kept, err := c.storedTransferLocked(res)
	if err != nil {
		return PersistedRecord{}, err
	}
	return kept.record, nil
}

// storedTransferLocked is storedTransferRecordLocked's kept record whole,
// with the references it names. Requires egraphMu.
func (c *Cache) storedTransferLocked(res *sharedResult) (*storedTransferRecord, error) {
	env, call := res.persistedEnvelope, res.loadResultCall()
	if kept := res.storedTransfer.Load(); kept != nil && kept.envelope == env && kept.call == call {
		return kept, nil
	}
	normalized, err := normalizeStoredTransfer(res, env, call)
	if err != nil {
		return nil, err
	}
	if c.testStoredTransferNormalized != nil {
		c.testStoredTransferNormalized()
	}
	return keepStoredTransfer(res, normalized), nil
}

// keepStoredTransfer keeps normalized as res's transfer record and returns
// it, unless a record of the same envelope and call is kept already, which it
// returns instead: readers normalize at once under egraphMu.RLock, and a
// record kept first, with its measured size (StoredSizes), stays the one kept.
// The caller has checked, under egraphMu, that the envelope and call are
// res's.
func keepStoredTransfer(res *sharedResult, normalized *storedTransferRecord) *storedTransferRecord {
	for {
		kept := res.storedTransfer.Load()
		if kept != nil && kept.envelope == normalized.envelope && kept.call == normalized.call {
			return kept
		}
		if res.storedTransfer.CompareAndSwap(kept, normalized) {
			return normalized
		}
	}
}

// normalizeStoredTransfer returns res's transfer record of env and call: the
// kept one if it is of them, or a new one, normalized and not kept. It reads
// only env and call, which the cache replaces and never changes in place, so
// it needs no lock: a caller that read them under egraphMu can normalize
// after releasing it, and keeps the record (keepStoredTransfer) under
// egraphMu again only if they are still the entry's (StoredSizes'
// publishStoredSizes).
func normalizeStoredTransfer(res *sharedResult, env *PersistedResultEnvelope, call *ResultCall) (*storedTransferRecord, error) {
	if kept := res.storedTransfer.Load(); kept != nil && kept.envelope == env && kept.call == call {
		return kept, nil
	}
	rec, refs, err := normalizeTransferRecordRefs(PersistedRecord{ResultID: uint64(res.id), Envelope: *env, Call: call})
	if err != nil {
		return nil, err
	}
	rec.Envelope.PendingOffers = nil
	// A build validates its bundle by shape, so the record's payloads are
	// checked by their codecs, and its outputs mapped, here.
	if err := validateTransferPayloads(rec); err != nil {
		return nil, err
	}
	outputs, err := mapTransferredOutputs(rec)
	if err != nil {
		return nil, err
	}
	for _, output := range outputs {
		if _, err := partAddressKey(output.Address); err != nil {
			return nil, err
		}
	}
	parts, err := declaredPartsOf(outputs)
	if err != nil {
		return nil, err
	}
	return &storedTransferRecord{envelope: env, call: call, record: rec, refs: refs, parts: parts}, nil
}

// storedBundleOutputsLocked chooses res's outputs, into its snapshot: its
// own stored parts, read with its children (snapStoredEntryLocked), then the
// pending parts its record declared then, and it didn't store then, borrowed
// from its class through the placement rule, each with the closure row each of its services maps to. The offers
// are cloned and relocated after the lock (finishStoredBundle). res is read
// again by its number, as a piece may have let the lock go. Requires
// egraphMu.
func (c *Cache) storedBundleOutputsLocked(build *storedBundleBuild, res *sharedResult) {
	snap := build.snaps[res.id]
	if cur := c.resultsByID[res.id]; cur != nil {
		res = cur
	}
	// The record's declarations and own parts as they were read with it
	// (snapStoredEntryLocked), so that a part it carries as its own isn't
	// borrowed too.
	for _, part := range snap.declared {
		key := part.key
		if part.state != "pending" {
			continue
		}
		if snap.ownKeys[key] {
			continue
		}
		donor, stored := c.storedPartDonorLocked(res, key, build.now)
		if donor == nil {
			continue
		}
		rows := map[uint64]uint64{}
		mapped := true
		for _, binding := range stored.held.Value.Services {
			row := c.closureRowOfClassLocked(sharedResultID(binding.ServiceResultID), build.ordinals)
			if row == 0 {
				mapped = false
				break
			}
			rows[binding.ServiceResultID] = uint64(row)
		}
		if !mapped {
			continue
		}
		snap.borrowed = append(snap.borrowed, storedBorrowedPart{part: stored, donor: donor.id, rows: rows})
	}
}

// addOutput adds a part of the entry id to the bundle, relocated to
// ordinals and without addresses, with donor as its storing entry.
func (build *storedBundleBuild) addOutput(id sharedResultID, part PersistedPartOffer, donor sharedResultID) error {
	part.Chain.Addresses, part.Chain.RenewalKey = nil, ""
	if err := visitPersistedPartOffer(&part, nil, build.relocate); err != nil {
		return fmt.Errorf("stored bundle: entry %d part: %w", id, err)
	}
	build.bundle.Outputs = append(build.bundle.Outputs, TransferredOutput{Ordinal: build.ordinals[id], Address: part.Address, State: "completed", Value: &part.Value, Chain: &part.Chain, Owner: &part.Owner})
	build.donors = append(build.donors, uint64(donor))
	return nil
}

// storedPartDonorLocked returns the unexpired entry of res's class, other
// than res, with the lowest number that stores the part, and the part as it
// stores it, a value the cache replaces whole: its offer is cloned after the
// lock (storedPart.offer). Requires egraphMu.
func (c *Cache) storedPartDonorLocked(res *sharedResult, key string, now int64) (*sharedResult, storedPart) {
	var best *sharedResult
	for root := range c.outputEqClassRootsLocked(res.id) {
		for id := range c.outputEqClassResults[root] {
			member := c.resultsByID[id]
			if member == nil || member == res || c.resultExpiredAtLocked(member, now) {
				continue
			}
			if _, ok := member.storedParts[key]; ok && (best == nil || member.id < best.id) {
				best = member
			}
		}
	}
	if best == nil {
		return nil, storedPart{}
	}
	return best, best.storedParts[key]
}

// closureRowOfClassLocked maps a service entry to a row of the closure of
// the same class, the lowest number first: the entry itself, if the closure
// holds it, or another entry of its class. It returns 0 when the closure
// holds none; the entry numbers in the returned part are relocated to
// ordinals afterwards. Requires egraphMu.
func (c *Cache) closureRowOfClassLocked(service sharedResultID, ordinals map[sharedResultID]TransferOrdinal) sharedResultID {
	if ordinals[service] != 0 {
		return service
	}
	if c.resultsByID[service] == nil {
		return 0
	}
	var best sharedResultID
	for root := range c.outputEqClassRootsLocked(service) {
		for id := range c.outputEqClassResults[root] {
			if ordinals[id] != 0 && (best == 0 || id < best) {
				best = id
			}
		}
	}
	return best
}
