package dagql

import (
	"context"
	"sync"

	"github.com/opencontainers/go-digest"
)

// A planned part fetch is a part the service sent to fetch ahead, because
// the job's earlier runs read it when a call ran (PartRead): once that call
// runs here, or at once when the reads had no caller. The engine downloads
// the part's content and unpacks it ahead of its first use (partPrefetcher's
// planned queue), from the offer that serves it.

// plannedPartFetch is one part to fetch ahead: entry id's part, as a read
// names it, or the whole entry when part is empty.
type plannedPartFetch struct {
	id   sharedResultID
	part PartKey
}

// partFetchPlan holds the planned fetches that wait for their call to run,
// by the call's recipe digest, and those whose call ran or that wait for
// none, for its worker to find their offers, one at a time, off the paths
// that fire them: a restore's merge and a call's start.
type partFetchPlan struct {
	mu      sync.Mutex
	waiting map[digest.Digest][]plannedPartFetch
	ready   []plannedPartFetch
	working bool
}

// PlanPartFetch plans a fetch ahead of entry number's part, or of the whole
// entry when part is empty: once the call whose recipe digest is runs starts
// to run on this engine, or at once when runs is empty. A plan for an entry
// the cache no longer holds, or a part no offer serves, fetches nothing. It
// never blocks on the fetch.
func (c *Cache) PlanPartFetch(ctx context.Context, number uint64, part PartKey, runs digest.Digest) {
	fetch := plannedPartFetch{id: sharedResultID(number), part: part}
	if runs == "" {
		c.firePlannedFetches(ctx, fetch)
		return
	}
	c.partPlan.mu.Lock()
	defer c.partPlan.mu.Unlock()
	if c.partPlan.waiting == nil {
		c.partPlan.waiting = map[digest.Digest][]plannedPartFetch{}
	}
	c.partPlan.waiting[runs] = append(c.partPlan.waiting[runs], fetch)
}

// callRuns fires the planned fetches that wait for the call with recipe
// digest call, which is about to run. It takes only the plan's mutex.
func (c *Cache) callRuns(ctx context.Context, call digest.Digest) {
	c.partPlan.mu.Lock()
	fetches := c.partPlan.waiting[call]
	delete(c.partPlan.waiting, call)
	c.partPlan.mu.Unlock()
	c.firePlannedFetches(ctx, fetches...)
}

// firePlannedFetches hands fetches to the plan's worker, starting it when it
// isn't running.
func (c *Cache) firePlannedFetches(ctx context.Context, fetches ...plannedPartFetch) {
	if len(fetches) == 0 {
		return
	}
	c.partPlan.mu.Lock()
	c.partPlan.ready = append(c.partPlan.ready, fetches...)
	start := !c.partPlan.working
	c.partPlan.working = true
	c.partPlan.mu.Unlock()
	if start {
		go c.workPlannedFetches(context.WithoutCancel(ctx))
	}
}

// workPlannedFetches hands each fired fetch's part to the prefetcher's
// planned queue, in the order they fired, until none is left.
func (c *Cache) workPlannedFetches(ctx context.Context) {
	for {
		c.partPlan.mu.Lock()
		if len(c.partPlan.ready) == 0 {
			c.partPlan.ready = nil
			c.partPlan.working = false
			c.partPlan.mu.Unlock()
			return
		}
		fetch := c.partPlan.ready[0]
		c.partPlan.ready = c.partPlan.ready[1:]
		c.partPlan.mu.Unlock()
		for _, served := range c.plannedPartOffers(ctx, fetch) {
			c.partOfferPlanned(ctx, served.row, served.key, served.offer)
		}
	}
}

// servedPart is an offer that serves a planned part: the row that holds it
// and the part's key there.
type servedPart struct {
	row   *sharedResult
	key   string
	offer *partOffer
}

// plannedPartOffers are the offers that serve fetch's part at each of the
// entry's output paths that has it, or every part of the entry when its part
// is empty: each the entry's own offer of the part, or, when the part is an
// unchanged part of the entry's recorded parent, the offer that serves that,
// as an install's delegation finds it. A read names a part by its key alone,
// so the entry's record says where it is. A part no offer serves is left
// out. A row's record is captured as an install captures it
// (capturePartRecord), from its payload as it stands, whether its value is
// still encoded or decoded.
func (c *Cache) plannedPartOffers(ctx context.Context, fetch plannedPartFetch) []servedPart {
	row := c.resultRow(fetch.id)
	if row == nil {
		return nil
	}
	record, err := c.plannedRowRecord(ctx, row)
	if err != nil {
		return nil
	}
	outputs, err := mapTransferredOutputs(record)
	if err != nil {
		return nil
	}
	var served []servedPart
	for _, out := range outputs {
		if fetch.part != "" && out.Address.Part != fetch.part {
			continue
		}
		if s, ok := c.servedPart(ctx, row, record, out.Address); ok {
			served = append(served, s)
		}
	}
	return served
}

// servedPart is the offer that serves row's part at address, whose record is
// record, following the part's delegations. A delegation names an unchanged
// part of the entry's recorded parent: the chain ends at the first entry with
// an offer of the part, and an entry seen twice ends it with none.
func (c *Cache) servedPart(ctx context.Context, row *sharedResult, record PersistedRecord, address PersistedPartAddress) (servedPart, bool) {
	for seen := map[sharedResultID]bool{}; !seen[row.id]; {
		seen[row.id] = true
		key, err := partAddressKey(address)
		if err != nil {
			return servedPart{}, false
		}
		c.egraphMu.RLock()
		offer := row.partOfferLocked(key)
		c.egraphMu.RUnlock()
		if offer != nil {
			return servedPart{row: row, key: key, offer: offer}, true
		}
		route, err := routePartRecord(record, address)
		if err != nil || route.Delegation == nil {
			return servedPart{}, false
		}
		if row = c.resultRow(sharedResultID(route.Delegation.ParentResultID)); row == nil {
			return servedPart{}, false
		}
		if record, err = c.plannedRowRecord(ctx, row); err != nil {
			return servedPart{}, false
		}
		address = route.Delegation.Address
	}
	return servedPart{}, false
}

// resultRow is the row the cache holds as id, or nil.
func (c *Cache) resultRow(id sharedResultID) *sharedResult {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	return c.resultsByID[id]
}

// plannedRowRecord is row's record, as an install captures it.
func (c *Cache) plannedRowRecord(ctx context.Context, row *sharedResult) (PersistedRecord, error) {
	var version capturedRowRevision
	return c.capturePartRecord(ctx, row, row.imported, nil, &version)
}
