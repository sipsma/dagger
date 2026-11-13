package dagql

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/dagger/dagger/engine/slog"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/opencontainers/go-digest"
)

// An engine's identity-only entry (sharedResult.identityOnly) stands for a
// Cloud entry an identity row named: a merge carried its recipe and digests,
// not its record. When something needs what it left out, a decode that loads
// it as a child row or a walk of its call frame, the entry is resolved
// (resolveIdentityEntry): to an entry of its class that holds a value, which
// same recipe makes interchangeable, as a held row resolves; failing that, to
// itself once the Cloud's record of it lands on it, fetched by its Cloud
// number (RowFetcher). A fetch that fails is errRowUnavailable, which turns
// the hit that needed the row into a miss (lookupCacheForRequest): a pipeline
// never fails for it, and a miss never fetches.

// RowFetcher fetches the Cloud's records of entries by their Cloud numbers:
// the engine's remote cache session, which asks the service. With decode, the
// answer follows each record's value children down, so one round trip serves
// one decode; with frames, it carries each record's whole call closure as
// frame rows, so one round trip serves a recipe walk. missing are the numbers
// the Cloud no longer holds with a value.
type RowFetcher interface {
	FetchRows(ctx context.Context, numbers []uint64, decode, frames bool) (bundle ValueBundle, missing []uint64, err error)
}

// rowFetchTimeout bounds how long a hit's use waits for a fetched row before
// the hit becomes a miss.
const rowFetchTimeout = 5 * time.Second

// errRowUnavailable says an identity-only entry's row could be neither
// resolved locally nor fetched.
var errRowUnavailable = errors.New("remote cache row unavailable")

// IdentityRowCounts counts what became of identity-only entries something
// needed, for the engine's stop row.
type IdentityRowCounts struct {
	// ResolvedLocally are needs an entry of the class with a value met.
	ResolvedLocally int64
	// Fetched are needs a fetch met, and FetchFailed those it didn't.
	Fetched, FetchFailed int64
	// FetchedRows are the full rows fetches brought.
	FetchedRows int64
	// RecipeWalks are frame walks that reached an identity-only entry.
	RecipeWalks int64
	// HitsMissed are hits turned into misses by an unavailable row, and
	// PartsUnavailable parts left unavailable because a call run again for
	// them needed an unavailable row.
	HitsMissed, PartsUnavailable int64
}

type identityRowCounters struct {
	resolvedLocally, fetched, fetchFailed, fetchedRows, recipeWalks, hitsMissed, partsUnavailable atomic.Int64
}

// SetRowFetcher sets the fetcher identity-only entries' rows come from; nil
// leaves them to resolve locally or not at all.
func (c *Cache) SetRowFetcher(f RowFetcher) {
	c.rowFetcher.Store(&f)
}

func (c *Cache) loadRowFetcher() RowFetcher {
	if f := c.rowFetcher.Load(); f != nil {
		return *f
	}
	return nil
}

// IdentityRowCounts returns the counts so far.
func (c *Cache) IdentityRowCounts() IdentityRowCounts {
	n := &c.identityRows
	return IdentityRowCounts{
		ResolvedLocally:  n.resolvedLocally.Load(),
		Fetched:          n.fetched.Load(),
		FetchFailed:      n.fetchFailed.Load(),
		FetchedRows:      n.fetchedRows.Load(),
		RecipeWalks:      n.recipeWalks.Load(),
		HitsMissed:       n.hitsMissed.Load(),
		PartsUnavailable: n.partsUnavailable.Load(),
	}
}

// localStandInLocked returns an entry of res's class that holds a servable
// value, the lowest numbered, or nil. Requires egraphMu.
func (c *Cache) localStandInLocked(res *sharedResult, nowUnix int64) *sharedResult {
	classID, ok := c.egraphDigestToClass[res.identityRecipe.String()]
	if !ok {
		return nil
	}
	root := c.eqClassRootLocked(classID)
	var ids []sharedResultID
	for id := range c.outputEqClassResults[root] {
		if id != res.id {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if cand := c.servableCandidateLocked(id, nowUnix, nil); cand != nil {
			return cand
		}
	}
	return nil
}

// resolveIdentityEntry returns the entry that stands for res when something
// needs its value or frame: res itself if it isn't identity-only (a record
// has landed on it), an entry of its class that holds a value, or res once
// the Cloud's record of it, fetched, has landed on it. decode asks the fetch
// for its value children too, and frames for its whole call closure as frame
// rows. Its error wraps errRowUnavailable.
//
// owner, when set, is the entry whose decode loads res as a child: it owns
// the stand-in directly, as its live value now names it. Otherwise res owns
// it.
func (c *Cache) resolveIdentityEntry(ctx context.Context, res *sharedResult, decode, frames bool, owner *sharedResult) (*sharedResult, error) {
	wcprof.WriteLock(ctx, &c.egraphMu, egraphLockIdent)
	if !res.identityOnly {
		c.egraphMu.Unlock()
		return res, nil
	}
	// Choosing the stand-in and owning it are one decision under the lock,
	// so no collection can take the stand-in between them.
	standIn := c.localStandInLocked(res, time.Now().Unix())
	var releases []OnReleaseFunc
	if standIn != nil {
		releases = c.ownStandInLocked(ctx, res, standIn, owner)
	}
	key, h := res.cloudHoldingLocked()
	c.egraphMu.Unlock()
	_ = runOnReleaseFuncs(context.WithoutCancel(ctx), releases)
	if standIn != nil {
		c.identityRows.resolvedLocally.Add(1)
		return standIn, nil
	}
	fail := func(err error) (*sharedResult, error) {
		c.identityRows.fetchFailed.Add(1)
		return nil, fmt.Errorf("entry %d: %w: %w", res.id, errRowUnavailable, err)
	}
	fetcher := c.loadRowFetcher()
	if fetcher == nil {
		return fail(errors.New("no fetcher"))
	}
	if h == nil {
		return fail(errors.New("no Cloud entry"))
	}
	releases = nil
	fetchCtx, cancel := context.WithTimeout(ctx, rowFetchTimeout)
	defer cancel()
	bundle, missing, err := fetcher.FetchRows(fetchCtx, []uint64{key.Number}, decode, frames)
	if err != nil {
		return fail(err)
	}
	if slices.Contains(missing, key.Number) || len(bundle.Values) == 0 {
		return fail(fmt.Errorf("the Cloud no longer holds entry %d", key.Number))
	}
	if _, err := c.mergeFetchedRowsInto(ctx, bundle, map[uint64]sharedResultID{key.Number: res.id}); err != nil {
		return fail(err)
	}
	wcprof.WriteLock(ctx, &c.egraphMu, egraphLockIdent)
	landed := !res.identityOnly
	if !landed {
		standIn = c.localStandInLocked(res, time.Now().Unix())
		if standIn != nil {
			releases = c.ownStandInLocked(ctx, res, standIn, owner)
		}
	}
	c.egraphMu.Unlock()
	_ = runOnReleaseFuncs(context.WithoutCancel(ctx), releases)
	switch {
	case landed:
	case standIn != nil:
		res = standIn
	default:
		return fail(fmt.Errorf("the fetched record of Cloud entry %d didn't land", key.Number))
	}
	c.identityRows.fetched.Add(1)
	for _, value := range bundle.Values {
		if value.Held == "" {
			c.identityRows.fetchedRows.Add(1)
		}
	}
	return res, nil
}

// ownStandInLocked makes the entry that needed res own the entry that stands
// in for it, so that it keeps the stand-in alive though the stand-in's own
// session releases it: owner, the entry whose decoded value now names the
// stand-in as a child, which an export of that live value then finds among
// its dependencies; or, for a frame walk, res itself. A decoded owner whose
// frame doesn't name res stops depending on res, which its live value no
// longer names either: otherwise an export would carry res held and its
// stand-in, of the same recipe, in full. Session requirements then cascade
// from the owner. It returns the release work of anything that collects,
// for after the lock. Requires egraphMu for writing.
func (c *Cache) ownStandInLocked(ctx context.Context, res, standIn, owner *sharedResult) []OnReleaseFunc {
	if owner == nil || c.resultsByID[owner.id] != owner {
		owner = res
	}
	if owner.id == standIn.id {
		return nil
	}
	if _, ok := owner.deps[standIn.id]; !ok {
		if owner.deps == nil {
			owner.deps = map[sharedResultID]struct{}{}
		}
		owner.deps[standIn.id] = struct{}{}
		c.rememberDependencyEdgeLocked(owner, standIn)
		c.incrementIncomingOwnershipLocked(ctx, standIn)
		owner.dependencyOwnershipRevision++
	}
	if _, ok := owner.deps[res.id]; ok && owner != res && sameRecipeLocked(standIn, res.identityRecipe) {
		// The decoded owner gives up res for the stand-in only once its
		// decoded value is installed (settleStandIns): until then res
		// stays, for another reference to it in the same decode, or a
		// retry of a failed one.
		if owner.pendingStandIns == nil {
			owner.pendingStandIns = map[sharedResultID]sharedResultID{}
		}
		owner.pendingStandIns[res.id] = standIn.id
	}
	_ = c.cascadeRequiredSessionResourcesLocked([]*sharedResult{owner}, false)
	return nil
}

// settleStandIns makes an entry whose decoded value is now installed depend
// on the stand-ins its decode resolved, in place of the identity-only
// entries they stood for: its live value names the stand-ins, and its frame
// is rewritten to name them too, which same recipe makes the same frame. An
// export then finds nothing held of a stand-in's recipe beside it. An
// identity-only entry nothing else owns is collected.
func (c *Cache) settleStandIns(ctx context.Context, owner *sharedResult) {
	wcprof.WriteLock(ctx, &c.egraphMu, egraphLockIdent)
	pending := owner.pendingStandIns
	owner.pendingStandIns = nil
	if len(pending) == 0 || c.resultsByID[owner.id] != owner {
		c.egraphMu.Unlock()
		return
	}
	if frame := owner.loadResultCall(); frame != nil {
		frame = frame.clone()
		_ = walkTransferCalls(frame, func(*ResultCall) error { return nil }, func(ref *ResultCallRef) error {
			if to, ok := pending[sharedResultID(ref.ResultID)]; ok {
				ref.ResultID = uint64(to)
				ref.shared = c.resultsByID[to]
			}
			return nil
		})
		owner.storeResultCall(frame)
	}
	var queue []*sharedResult
	dropped := false
	for from, to := range pending {
		_, hasFrom := owner.deps[from]
		_, hasTo := owner.deps[to]
		res := c.resultsByID[from]
		if !hasFrom || !hasTo || res == nil {
			continue
		}
		dropped = true
		delete(owner.deps, from)
		c.forgetDependencyEdgeLocked(owner.id, from)
		owner.dependencyOwnershipRevision++
		var err error
		if queue, err = c.decrementIncomingOwnershipLocked(ctx, res, queue); err != nil {
			slog.Warn("settle stand-ins: ownership", "entry", owner.id, "error", err)
		}
	}
	if dropped {
		// The requirements the dropped entries brought go with them.
		if err := c.cascadeRequiredSessionResourcesLocked([]*sharedResult{owner}, false); err != nil {
			slog.Warn("settle stand-ins: requirements", "entry", owner.id, "error", err)
		}
	}
	releases, _ := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
	c.egraphMu.Unlock()
	_ = runOnReleaseFuncs(context.WithoutCancel(ctx), releases)
}

// clearStandIns forgets the stand-ins an earlier decode of res recorded:
// they belong to the decode whose value is installed, and a new attempt
// records its own.
func (c *Cache) clearStandIns(ctx context.Context, res *sharedResult) {
	wcprof.WriteLock(ctx, &c.egraphMu, egraphLockIdent)
	res.pendingStandIns = nil
	c.egraphMu.Unlock()
}

// sameRecipeLocked reports whether standIn's frame derives recipe, so that
// naming it in a frame in res's place leaves the frame's recipe as it was.
// It derives nothing: a frame not derived yet doesn't count. Requires
// egraphMu.
func sameRecipeLocked(standIn *sharedResult, recipe digest.Digest) bool {
	frame := standIn.loadResultCall()
	if frame == nil {
		return standIn.identityRecipe == recipe
	}
	got, derived, err := frame.derivedRecipeDigest()
	return derived && err == nil && got == recipe
}

// resolveIdentityFrame returns the call frame that stands for an
// identity-only entry's in a frame walk (a recipe-ID rebuild, a call run
// again for a part): the frame of the entry resolveIdentityEntry finds.
func (c *Cache) resolveIdentityFrame(ctx context.Context, id sharedResultID) (*ResultCall, error) {
	c.egraphMu.RLock()
	res := c.resultsByID[id]
	identity := res != nil && res.identityOnly
	c.egraphMu.RUnlock()
	if !identity {
		return nil, nil
	}
	c.identityRows.recipeWalks.Add(1)
	resolved, err := c.resolveIdentityEntry(ctx, res, false, true, nil)
	if err != nil {
		return nil, err
	}
	frame := c.resultCallByResultID(resolved.id)
	if frame == nil {
		return nil, fmt.Errorf("entry %d: %w: its stand-in %d has no call frame", id, errRowUnavailable, resolved.id)
	}
	return frame, nil
}

// markRowUnavailable records that a hit on res became a miss because its
// value needed an unavailable row: lookups and publications pass res by from
// then on (sharedResult.rowUnavailable), and the miss is counted.
func (c *Cache) markRowUnavailable(ctx context.Context, res *sharedResult) {
	c.identityRows.hitsMissed.Add(1)
	if res == nil {
		return
	}
	wcprof.WriteLock(ctx, &c.egraphMu, egraphLockIdent)
	res.rowUnavailable = true
	c.egraphMu.Unlock()
}

// MergeFetchedRows merges a fetch's answer from the Cloud: its full rows
// land on the identity-only entries that named them, as a merge's record
// lands on an entry with no value (mergeStore), and the merge retains none
// of them, since those entries' dependents own them.
func (c *Cache) MergeFetchedRows(ctx context.Context, bundle ValueBundle) (MergeReply, error) {
	return c.mergeFetchedRowsInto(ctx, bundle, nil)
}

// mergeFetchedRowsInto is MergeFetchedRows, with each fetched row whose
// sender number targets names landing on that identity-only entry.
func (c *Cache) mergeFetchedRowsInto(ctx context.Context, bundle ValueBundle, targets map[uint64]sharedResultID) (MergeReply, error) {
	p, err := c.PrepareValueMerge(ctx, cloudCacheID, bundle)
	if err != nil {
		return MergeReply{}, err
	}
	p.prep.fetched = true
	p.prep.fetchTargets = targets
	return c.CommitValueMerge(ctx, p)
}

// cascadeRequiredSessionResourcesLocked recomputes the session requirements
// of each start entry and, where they change, of every entry above it, as an
// explicit dependency added to a published entry does. With recomputed, the
// start entries' own sets are already current, so the cascade starts at
// their parents whatever the start entries' recomputation finds. Requires
// egraphMu for writing.
func (c *Cache) cascadeRequiredSessionResourcesLocked(start []*sharedResult, recomputed bool) error {
	queue := slices.Clone(start)
	forced := map[*sharedResult]bool{}
	if recomputed {
		for _, res := range start {
			forced[res] = true
		}
	}
	var errs error
	for len(queue) > 0 {
		res := queue[0]
		queue = queue[1:]
		changed, err := c.recomputeRequiredSessionResourcesLocked(res)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		if !changed && !forced[res] || res.depParents == nil {
			continue
		}
		for ancestorID := range res.depParents.Items() {
			if ancestor := c.resultsByID[ancestorID]; ancestor != nil {
				queue = append(queue, ancestor)
			}
		}
	}
	return errs
}

// decodeOwner returns the entry whose persisted value the decode on ctx is
// decoding, if any: the owner of the child rows it loads.
func decodeOwner(ctx context.Context, c *Cache) *sharedResult {
	roles, _ := ctx.Value(copiedDecodeRolesKey{}).(*copiedDecodeRoles)
	if roles == nil || roles.ResultID == 0 {
		return nil
	}
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	return c.resultsByID[sharedResultID(roles.ResultID)]
}
