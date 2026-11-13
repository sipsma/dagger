package dagql

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/dagger/dagger/engine/wcprof"
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
// one decode. missing are the numbers the Cloud no longer holds with a value.
type RowFetcher interface {
	FetchRows(ctx context.Context, numbers []uint64, decode bool) (bundle ValueBundle, missing []uint64, err error)
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
// for its value children too. Its error wraps errRowUnavailable.
func (c *Cache) resolveIdentityEntry(ctx context.Context, res *sharedResult, decode bool) (*sharedResult, error) {
	c.egraphMu.RLock()
	if !res.identityOnly {
		c.egraphMu.RUnlock()
		return res, nil
	}
	standIn := c.localStandInLocked(res, time.Now().Unix())
	key, h := res.cloudHoldingLocked()
	c.egraphMu.RUnlock()
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
	fetchCtx, cancel := context.WithTimeout(ctx, rowFetchTimeout)
	defer cancel()
	bundle, missing, err := fetcher.FetchRows(fetchCtx, []uint64{key.Number}, decode)
	if err != nil {
		return fail(err)
	}
	if slices.Contains(missing, key.Number) || len(bundle.Values) == 0 {
		return fail(fmt.Errorf("the Cloud no longer holds entry %d", key.Number))
	}
	if _, err := c.MergeFetchedRows(ctx, bundle); err != nil {
		return fail(err)
	}
	c.egraphMu.RLock()
	landed := !res.identityOnly
	if !landed {
		standIn = c.localStandInLocked(res, time.Now().Unix())
	}
	c.egraphMu.RUnlock()
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
	resolved, err := c.resolveIdentityEntry(ctx, res, false)
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
	p, err := c.PrepareValueMerge(ctx, cloudCacheID, bundle)
	if err != nil {
		return MergeReply{}, err
	}
	p.prep.fetched = true
	return c.CommitValueMerge(ctx, p)
}
