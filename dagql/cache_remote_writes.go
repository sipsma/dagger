package dagql

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/dagger/dagger/engine/slog"
	"github.com/opencontainers/go-digest"
)

// RemoteWrites is a Cloud cache held for writing (HoldRemoteWrites), for a
// run of operations on its holdings applied in one hold of its lock. Each
// method is the Cache's method of the same name, with the same rules and the
// same change: a run of them changes the cache as the Cache's methods called
// one by one in the same order do, and each returns what it changed. What
// the run saves is the waits: a reader of the cache, such as a bundle's
// build, holds its lock for as long as it reads, and a writer waiting for the
// lock stops new readers until it has had it, so each of the Cache's methods
// waits for every read already in progress, and the run waits for them once.
//
// A hold of one engine cache's writes (HoldEngineWrites) holds the cache's
// lock for reading, beside the cache's readers and other engine caches'
// holds: each of its writes takes that engine cache's own lock, and changes
// only that engine cache's state, which no other engine's write touches. A
// write that would change state engines share (its plan says so: PlanAttach)
// needs the cache to itself: its holder turns the hold exclusive first
// (Exclusive), before anything else of its own is locked, and can turn it
// shared again after (Share). The holder decides by the plan, before the
// write and before any lock of its own: a write inside a shared hold can't
// wait for the cache to itself, as the hold's own read would keep it out.
//
// The holder calls nothing else of the cache until Release: the cache's own
// methods wait for the hold. Only the Cloud uses it, to apply a read of its
// engines' telemetry and a step's merged replies.
type RemoteWrites struct {
	c *Cache
	// releases are what the hold's collections released, run once the hold
	// ends, as CollectRemoteHoldings runs them once its own hold ends.
	releases []OnReleaseFunc
	// engine is the engine cache whose writes a hold of one engine cache's
	// writes is for, empty for a hold of the whole cache; shared is whether
	// the hold holds the cache's lock for reading now.
	engine CacheID
	shared bool
	// one is the engine cache's hold lock a hold of its writes holds for
	// its life (HoldEngineWrites).
	one *sync.Mutex
}

// HoldRemoteWrites takes the cache's lock for writing, until the returned
// hold's Release.
func (c *Cache) HoldRemoteWrites() *RemoteWrites {
	c.egraphMu.Lock()
	return &RemoteWrites{c: c}
}

// HoldEngineWrites takes the cache's lock for reading, for writes of the
// engine cache's holdings beside the cache's readers and other engine
// caches' writes, until the returned hold's Release. One hold of an engine
// cache's writes is held at a time: a second waits for the first's
// Release, before it takes the cache's lock, so that nothing changes the
// engine cache's state between a write's plan and the write but the hold's
// own writes.
func (c *Cache) HoldEngineWrites(engine CacheID) *RemoteWrites {
	one := c.engineHoldLock(engine)
	one.Lock()
	c.egraphMu.RLock()
	return &RemoteWrites{c: c, engine: engine, shared: true, one: one}
}

// engineHoldLock returns the lock that lets one hold of the engine cache's
// writes be held at a time (HoldEngineWrites), creating it.
func (c *Cache) engineHoldLock(engine CacheID) *sync.Mutex {
	c.engineHoldsMu.Lock()
	defer c.engineHoldsMu.Unlock()
	if c.engineHolds == nil {
		c.engineHolds = map[CacheID]*sync.Mutex{}
	}
	one := c.engineHolds[engine]
	if one == nil {
		one = &sync.Mutex{}
		c.engineHolds[engine] = one
	}
	return one
}

// Shared reports whether the hold holds the cache's lock for reading now:
// it is a hold of one engine cache's writes, not turned exclusive.
func (w *RemoteWrites) Shared() bool {
	return w.shared
}

// Exclusive turns a shared hold into a hold of the whole cache, for writes
// that change state engines share: it lets go of the cache's lock for
// reading and takes it for writing. Anything the hold read before may have
// changed: a write after it plans nothing, and makes every check its write
// makes. Nothing for a hold that holds the cache to itself already.
func (w *RemoteWrites) Exclusive() {
	if !w.shared {
		return
	}
	w.c.egraphMu.RUnlock()
	w.c.egraphMu.Lock()
	w.shared = false
}

// Share turns a hold of one engine cache's writes, turned exclusive, back
// into a shared one: it lets go of the cache's lock for writing and takes it
// for reading. Anything can change while it lets go: a holder shares before
// it plans its next write, never between a plan and its write. Nothing for a
// shared hold, or a hold of the whole cache.
func (w *RemoteWrites) Share() {
	if w.shared || w.engine == "" {
		return
	}
	w.c.egraphMu.Unlock()
	w.c.egraphMu.RLock()
	w.shared = true
}

// Contended reports whether anything waits for the cache's lock now, to
// read the cache or to write it. A holder applying a run of writes asks it
// between two of them, and lets the hold go and takes it again only when
// something waits: a reader then waits for the run's write in progress, not
// for the whole run, and the run waits for the cache's readers again only
// when it kept one out. A shared hold keeps out only writers that need the
// cache to themselves.
func (w *RemoteWrites) Contended() bool {
	return w.c.egraphMu.Waiting()
}

// Release ends the hold, and then runs what its collections released,
// returning their error. The hold can't be used after.
func (w *RemoteWrites) Release(ctx context.Context) error {
	c, releases := w.c, w.releases
	w.c, w.releases = nil, nil
	if w.shared {
		c.egraphMu.RUnlock()
	} else {
		c.egraphMu.Unlock()
	}
	if w.one != nil {
		w.one.Unlock()
	}
	return runOnReleaseFuncs(ctx, releases)
}

// errNeedsWholeCache refuses, in a shared hold, a write a shared hold can't
// make: one of another engine cache's holdings, one that would create an
// engine cache's state, or a collection.
var errNeedsWholeCache = errors.New("remote writes: the write needs the cache to itself")

// lockWrite takes, in a shared hold, the lock of the engine cache whose
// holdings a write changes, and returns its state, which the write lets go
// of (unlockWrite); ok is false, with nothing locked, for a write of another
// engine cache's holdings, which a shared hold can't make
// (errNeedsWholeCache). An engine cache with no state holds nothing: the
// write finds no holding, and changes nothing, as it would with the cache
// held to itself, unless it would create the state, which its plan names
// (SharedNewEngineCache). A hold of the whole cache takes nothing.
func (w *RemoteWrites) lockWrite(cache CacheID) (state *remoteCacheState, ok bool) {
	if !w.shared {
		return nil, true
	}
	if cache != w.engine {
		return nil, false
	}
	state = w.c.remoteCaches[cache]
	if state != nil {
		state.mu.Lock()
	}
	return state, true
}

// unlockWrite lets go of what lockWrite took, and checks, in a shared hold,
// that the write changed nothing engines share, as its plan said: one that
// did is counted and logged, as a write the holder should have made with
// the cache to itself (Cache.SharedHoldMisses).
func (w *RemoteWrites) unlockWrite(state *remoteCacheState, change RemoteChange) {
	if state != nil {
		state.mu.Unlock()
	}
	if w.shared && change.Shared != 0 {
		w.c.sharedHoldMisses.Add(1)
		slog.Error("remote writes: a shared hold's write changed state engines share", "engine", w.engine, "kinds", change.Shared.String())
	}
}

// SharedHoldMisses counts the writes in shared holds that changed state
// engines share, which their holders should have made with the cache to
// themselves: none, while every write is planned before it is made.
func (c *Cache) SharedHoldMisses() uint64 {
	return c.sharedHoldMisses.Load()
}

// rlockRead takes, in a shared hold, the lock of the engine cache whose
// state a read of the hold reads, for reading; nothing in a hold of the
// whole cache.
func (w *RemoteWrites) rlockRead(cache CacheID) *remoteCacheState {
	if !w.shared {
		return nil
	}
	return w.c.rlockEngineLocked(cache)
}

// PlanAttach is Cache.PlanAttach, in the hold.
func (w *RemoteWrites) PlanAttach(key HolderKey, desc RemoteHolding, prepared PreparedRemoteHolding) SharedWrites {
	defer w.rlockRead(key.Cache).runlock()
	return w.c.attachSharesLocked(key, desc, prepared)
}

// PlanUpdate is Cache.PlanUpdate, in the hold.
func (w *RemoteWrites) PlanUpdate(key HolderKey, update RemoteHoldingUpdate, prepared PreparedRemoteHoldingUpdate) SharedWrites {
	defer w.rlockRead(key.Cache).runlock()
	return w.c.updateSharesLocked(key, update, prepared)
}

// PlanMergedReplyPiece is what ApplyMergedReplyPiece of a would change of
// state engines share now, from its next record on.
func (w *RemoteWrites) PlanMergedReplyPiece(a *MergedReplyApply) SharedWrites {
	return w.c.mergedReplySharesLocked(a)
}

// AttachRemoteHolding is Cache.AttachRemoteHolding, in the hold.
func (w *RemoteWrites) AttachRemoteHolding(ctx context.Context, key HolderKey, desc RemoteHolding) (RemoteChange, error) {
	return w.AttachPreparedRemoteHolding(ctx, key, desc, PrepareRemoteHolding(key, desc))
}

// AttachPreparedRemoteHolding is Cache.AttachPreparedRemoteHolding, in the
// hold: a holder that prepared key and desc before it took the hold spends
// none of that in it.
func (w *RemoteWrites) AttachPreparedRemoteHolding(ctx context.Context, key HolderKey, desc RemoteHolding, prepared PreparedRemoteHolding) (RemoteChange, error) {
	if prepared.err != nil {
		return RemoteChange{}, prepared.err
	}
	state, ok := w.lockWrite(key.Cache)
	if !ok || w.shared && state == nil {
		// An attach to an engine cache with no state creates it
		// (SharedNewEngineCache), which its holder's plan names.
		return RemoteChange{}, errNeedsWholeCache
	}
	change := w.c.attachRemoteHoldingLocked(ctx, key, desc, prepared.parts, prepared.kept)
	w.unlockWrite(state, change)
	return change, nil
}

// UpdateRemoteHolding is Cache.UpdateRemoteHolding, in the hold.
func (w *RemoteWrites) UpdateRemoteHolding(ctx context.Context, key HolderKey, update RemoteHoldingUpdate) (RemoteChange, bool, error) {
	return w.UpdatePreparedRemoteHolding(ctx, key, update, PrepareRemoteHoldingUpdate(key, update))
}

// UpdatePreparedRemoteHolding is Cache.UpdatePreparedRemoteHolding, in the
// hold.
func (w *RemoteWrites) UpdatePreparedRemoteHolding(ctx context.Context, key HolderKey, update RemoteHoldingUpdate, prepared PreparedRemoteHoldingUpdate) (RemoteChange, bool, error) {
	if prepared.err != nil {
		return RemoteChange{}, false, prepared.err
	}
	state, ok := w.lockWrite(key.Cache)
	if !ok {
		return RemoteChange{}, false, errNeedsWholeCache
	}
	change, found := w.c.updateRemoteHoldingLocked(ctx, key, update, prepared.parts)
	w.unlockWrite(state, change)
	return change, found, nil
}

// AddRemoteHold is Cache.AddRemoteHold, in the hold.
func (w *RemoteWrites) AddRemoteHold(ctx context.Context, key HolderKey, session string) (RemoteChange, error) {
	if session == "" {
		return RemoteChange{}, errEmptyRemoteHoldSession
	}
	state, ok := w.lockWrite(key.Cache)
	if !ok {
		return RemoteChange{}, errNeedsWholeCache
	}
	change, err := w.c.addRemoteHoldLocked(key, session)
	w.unlockWrite(state, change)
	return change, err
}

// ObserveRemoteRetention is Cache.ObserveRemoteRetention, in the hold.
func (w *RemoteWrites) ObserveRemoteRetention(ctx context.Context, key HolderKey, obs RetentionObservation) (RemoteChange, error) {
	state, ok := w.lockWrite(key.Cache)
	if !ok {
		return RemoteChange{}, errNeedsWholeCache
	}
	change, err := w.c.observeRemoteRetentionLocked(key, obs)
	w.unlockWrite(state, change)
	return change, err
}

// ReleaseRemoteSession is Cache.ReleaseRemoteSession, in the hold. In a
// shared hold, an engine cache with no state holds nothing to release; a
// release of another engine cache's session is refused, logged, and
// releases nothing.
func (w *RemoteWrites) ReleaseRemoteSession(ctx context.Context, cache CacheID, session string) RemoteChange {
	state, ok := w.lockWrite(cache)
	if !ok {
		w.refused("release a session", cache)
		return RemoteChange{}
	}
	change := w.c.releaseRemoteSessionChangeLocked(cache, session)
	w.unlockWrite(state, change)
	return change
}

// ReleaseRemoteCache is Cache.ReleaseRemoteCache, in the hold, as
// ReleaseRemoteSession is.
func (w *RemoteWrites) ReleaseRemoteCache(ctx context.Context, cache CacheID) RemoteChange {
	state, ok := w.lockWrite(cache)
	if !ok {
		w.refused("release an engine cache", cache)
		return RemoteChange{}
	}
	change := w.c.releaseRemoteCacheLocked(cache)
	w.unlockWrite(state, change)
	return change
}

// refused logs a write a shared hold refused that has no error to report it
// with: a holder's write of another engine cache's holdings, or a merged
// reply's piece that would create its engine cache's state.
func (w *RemoteWrites) refused(what string, cache CacheID) {
	w.c.sharedHoldMisses.Add(1)
	slog.Error("remote writes: a shared hold refused a write", "engine", w.engine, "write", what, "cache", cache)
}

// CollectRemoteHoldings is Cache.CollectRemoteHoldings, in the hold. What
// the collected entries release runs at Release, whose error reports it.
func (w *RemoteWrites) CollectRemoteHoldings(ctx context.Context, candidates []HolderKey, owned func(HolderKey) bool) ([]HolderKey, RemoteChange, error) {
	if w.shared {
		// A collection can remove an entry or an engine cache's state,
		// which only its walk finds out: it takes the cache to itself.
		return nil, RemoteChange{}, errNeedsWholeCache
	}
	collected, change, releases, err := w.c.collectRemoteHoldingsLocked(ctx, candidates, owned)
	w.releases = append(w.releases, releases...)
	slices.SortFunc(collected, compareHolderKeys)
	return collected, change, err
}

// ApplyMergedReply is Cache.ApplyMergedReply, in the hold.
func (w *RemoteWrites) ApplyMergedReply(ctx context.Context, cache CacheID, sent ValueBundle, reply MergeReply) (RemoteChange, error) {
	cloudEntries, targets, err := prepareMergedReply(cache, sent, reply)
	if err != nil {
		return RemoteChange{}, err
	}
	state, ok := w.lockWrite(cache)
	if !ok || w.shared && state == nil {
		// A reply that lands a record creates the engine cache's state: a
		// shared hold applies a reply in pieces, each planned first.
		return RemoteChange{}, errNeedsWholeCache
	}
	change := w.c.applyMergedReplyLocked(ctx, cache, cloudEntries, targets, reply)
	w.unlockWrite(state, change)
	return change, nil
}

// ApplyMergedReplyPiece applies the next records of a merged reply
// prepared to apply in pieces, in the hold: one at least, and then one at a
// time until stop, asked between two records, reports true or the records
// run out. With its last records it applies the reply's roots' retention.
// It returns what it changed and whether the reply is applied whole. A hold
// let go between pieces lets the cache's readers in (MergedReplyApply): a
// holder that stops when someone waits (Contended) keeps a reader waiting
// for one record, not the reply.
func (w *RemoteWrites) ApplyMergedReplyPiece(ctx context.Context, a *MergedReplyApply, stop func() bool) (change RemoteChange, done bool) {
	state, ok := w.lockWrite(a.cache)
	if !ok || w.shared && state == nil && w.c.mergedReplySharesLocked(a) != 0 {
		// A shared hold's holder plans the piece first
		// (PlanMergedReplyPiece): a piece that would create the engine
		// cache's state takes the cache to itself.
		w.refused("apply a merged reply's piece", a.cache)
		return change, false
	}
	defer func() { w.unlockWrite(state, change) }()
	defer w.c.takeChangesLocked(&change)
	for a.next < len(a.targets) {
		w.c.applyMergedTargetsLocked(ctx, a.cache, a.cloudEntries, a.targets[a.next:a.next+1], &change)
		a.next++
		if a.next < len(a.targets) && stop() {
			return change, false
		}
	}
	w.c.applyMergedRootsLocked(a.cache, a.reply, &change)
	return change, true
}

// RemoteHoldingInfo is Cache.RemoteHoldingInfo, in the hold.
func (w *RemoteWrites) RemoteHoldingInfo(key HolderKey) (RemoteHeldEntry, bool) {
	defer w.rlockRead(key.Cache).runlock()
	return w.c.remoteHoldingInfoLocked(key)
}

// HolderEntry is Cache.HolderEntry, in the hold.
func (w *RemoteWrites) HolderEntry(key HolderKey) (uint64, bool) {
	defer w.rlockRead(key.Cache).runlock()
	return w.c.holderEntryLocked(key)
}

// ApplyHeldValueState is Cache.ApplyHeldValueState, in the hold.
func (w *RemoteWrites) ApplyHeldValueState(ctx context.Context, key HolderKey, obs HeldValueState) (RemoteChange, bool, error) {
	parts, err := heldPartKeys(obs.Parts)
	if err != nil {
		return RemoteChange{}, false, fmt.Errorf("apply held value state %s/%d: %w", key.Cache, key.Number, err)
	}
	offered, err := heldPartKeys(obs.OfferedParts)
	if err != nil {
		return RemoteChange{}, false, fmt.Errorf("apply held value state %s/%d: %w", key.Cache, key.Number, err)
	}
	state, ok := w.lockWrite(key.Cache)
	if !ok {
		return RemoteChange{}, false, errNeedsWholeCache
	}
	change, found := w.c.applyHeldValueStateOfLocked(ctx, key, obs, parts, offered)
	w.unlockWrite(state, change)
	return change, found, nil
}

// EntryRecipe is Cache.EntryRecipe, in the hold.
func (w *RemoteWrites) EntryRecipe(number uint64) (digest.Digest, bool) {
	return w.c.entryRecipeLocked(number)
}

// HolderClosure is Cache.HolderClosure, in the hold.
func (w *RemoteWrites) HolderClosure(key HolderKey) []HolderKey {
	defer w.rlockRead(key.Cache).runlock()
	return w.c.holderClosureLocked(key)
}

// HolderGraph is Cache.HolderGraph, in the hold.
func (w *RemoteWrites) HolderGraph(cache CacheID, roots []uint64) map[uint64][]uint64 {
	defer w.rlockRead(cache).runlock()
	graph, _ := w.c.holderGraphLocked(cache, roots, nil)
	return graph
}

// EntryClassEntries is Cache.EntryClassEntries, in the hold.
func (w *RemoteWrites) EntryClassEntries(number uint64) (EntryClass, bool) {
	return w.c.entryClassEntriesLocked(number)
}
