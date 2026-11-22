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
// by the call's recipe digest.
type partFetchPlan struct {
	mu      sync.Mutex
	waiting map[digest.Digest][]plannedPartFetch
}

// PlanPartFetch plans a fetch ahead of entry number's part, or of the whole
// entry when part is empty: once the call whose recipe digest is runs starts
// to run on this engine, or at once when runs is empty. A plan for an entry
// the cache no longer holds, or a part no offer serves, fetches nothing.
func (c *Cache) PlanPartFetch(ctx context.Context, number uint64, part PartKey, runs digest.Digest) {
	fetch := plannedPartFetch{id: sharedResultID(number), part: part}
	if runs == "" {
		c.fetchPlannedPart(ctx, fetch)
		return
	}
	c.partPlan.mu.Lock()
	defer c.partPlan.mu.Unlock()
	if c.partPlan.waiting == nil {
		c.partPlan.waiting = map[digest.Digest][]plannedPartFetch{}
	}
	c.partPlan.waiting[runs] = append(c.partPlan.waiting[runs], fetch)
}

// callRuns starts the planned fetches that wait for the call with recipe
// digest call, which is about to run, on a goroutine of their own when there
// are any. It takes only the plan's mutex.
func (c *Cache) callRuns(ctx context.Context, call digest.Digest) {
	c.partPlan.mu.Lock()
	fetches := c.partPlan.waiting[call]
	delete(c.partPlan.waiting, call)
	c.partPlan.mu.Unlock()
	if len(fetches) == 0 {
		return
	}
	go func() {
		for _, fetch := range fetches {
			c.fetchPlannedPart(ctx, fetch)
		}
	}()
}

// fetchPlannedPart hands fetch's part to the prefetcher's planned queue, from
// the offer that serves it, or each of the entry's parts when the read was of
// the whole entry.
func (c *Cache) fetchPlannedPart(ctx context.Context, fetch plannedPartFetch) {
	for _, served := range c.plannedPartOffers(fetch) {
		c.partOfferPlanned(ctx, served.row, served.key, served.offer)
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
// so the entry's record says where it is. A part no offer serves is left out.
func (c *Cache) plannedPartOffers(fetch plannedPartFetch) []servedPart {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	row := c.resultsByID[fetch.id]
	if row == nil || row.persistedEnvelope == nil {
		return nil
	}
	outputs, err := mapTransferredOutputs(persistedRecordOf(row))
	if err != nil {
		return nil
	}
	var addresses []PersistedPartAddress
	for _, out := range outputs {
		if fetch.part == "" || out.Address.Part == fetch.part {
			addresses = append(addresses, out.Address)
		}
	}
	var served []servedPart
	for _, address := range addresses {
		if s, ok := c.servedPartLocked(fetch.id, address); ok {
			served = append(served, s)
		}
	}
	return served
}

// servedPartLocked is the offer that serves entry id's part at address,
// following the part's delegations. Requires egraphMu.
func (c *Cache) servedPartLocked(id sharedResultID, address PersistedPartAddress) (servedPart, bool) {
	// A delegation names an unchanged part of the entry's recorded parent:
	// the chain ends at the first entry with an offer of the part, and an
	// entry seen twice ends it with none.
	for seen := map[sharedResultID]bool{}; !seen[id]; {
		seen[id] = true
		row := c.resultsByID[id]
		if row == nil {
			return servedPart{}, false
		}
		key, err := partAddressKey(address)
		if err != nil {
			return servedPart{}, false
		}
		if offer := row.partOfferLocked(key); offer != nil {
			return servedPart{row: row, key: key, offer: offer}, true
		}
		if row.persistedEnvelope == nil {
			return servedPart{}, false
		}
		route, err := routePartRecord(persistedRecordOf(row), address)
		if err != nil || route.Delegation == nil {
			return servedPart{}, false
		}
		id, address = sharedResultID(route.Delegation.ParentResultID), route.Delegation.Address
	}
	return servedPart{}, false
}

// persistedRecordOf is row's persisted record, as a restore landed it.
// Requires row.persistedEnvelope.
func persistedRecordOf(row *sharedResult) PersistedRecord {
	return PersistedRecord{ResultID: uint64(row.id), Envelope: *row.persistedEnvelope, Call: row.loadResultCall(), SnapshotLinks: row.loadSnapshotOwnerLinks()}
}
