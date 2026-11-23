package dagql

import (
	"time"

	"github.com/opencontainers/go-digest"
)

// A write's plan says, reading only, which kinds of state engines share the
// write would change if it ran now (SharedWrites): the same checks the
// write makes, in its order, with nothing written. A holder of the Cloud
// cache's lock for reading can then tell, before it writes, whether the
// write needs the lock to itself. The plan stays true until the write while
// the holder keeps its hold, as state engines share changes only under the
// lock held exclusively; the write's own engine cache's state changes only
// under that engine cache's lock, which its writes take one at a time. A
// write's change names at most what its plan named (RemoteChange.Shared).

// PlanAttach is what AttachPreparedRemoteHolding of key, desc and prepared
// would change of state engines share, read under the cache's lock.
func (c *Cache) PlanAttach(key HolderKey, desc RemoteHolding, prepared PreparedRemoteHolding) SharedWrites {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	defer c.rlockEngineLocked(key.Cache).runlock()
	return c.attachSharesLocked(key, desc, prepared)
}

// PlanUpdate is what UpdatePreparedRemoteHolding of key, update and
// prepared would change of state engines share, read under the cache's lock.
func (c *Cache) PlanUpdate(key HolderKey, update RemoteHoldingUpdate, prepared PreparedRemoteHoldingUpdate) SharedWrites {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	defer c.rlockEngineLocked(key.Cache).runlock()
	return c.updateSharesLocked(key, update, prepared)
}

// attachSharesLocked is attachRemoteHoldingLocked's plan. A description that
// doesn't prepare changes nothing. Requires egraphMu, and key's engine
// cache's lock when egraphMu is held for reading.
func (c *Cache) attachSharesLocked(key HolderKey, desc RemoteHolding, prepared PreparedRemoteHolding) SharedWrites {
	if prepared.err != nil {
		return 0
	}
	if c.egraphDigestToClass == nil {
		// The e-graph is made by the cache's first write (initEgraphLocked),
		// and its first entry with it.
		return SharedNewEntry
	}
	var w SharedWrites
	entry, h := c.holdingLocked(key)
	if h == nil {
		if c.remoteCaches[key.Cache] == nil {
			w |= SharedNewEngineCache
		}
		entry = c.resultsByID[c.entriesByRecipe[desc.Recipe]]
		if entry == nil {
			// A new entry starts empty: what the write does to it is
			// planned against an entry with nothing yet.
			w |= SharedNewEntry
			entry = &sharedResult{}
		}
	}
	w |= c.teachSharesLocked(entry, desc.Recipe, desc.Request, desc.ContentDigest, desc.Term)
	w |= c.keepCallSharesLocked(entry, desc, prepared.kept)
	// The holding's value state teaches its content digest again
	// (applyHeldValueStateLocked): once the call's teach changes nothing,
	// that changes nothing either.
	if desc.Executed && ranPartsReported(prepared.parts) {
		w |= c.runRanSharesLocked(key, entry, desc.Replacements)
	}
	return w
}

// updateSharesLocked is updateRemoteHoldingLocked's plan. Requires egraphMu,
// and key's engine cache's lock when egraphMu is held for reading.
func (c *Cache) updateSharesLocked(key HolderKey, update RemoteHoldingUpdate, prepared PreparedRemoteHoldingUpdate) SharedWrites {
	if prepared.err != nil {
		return 0
	}
	entry, h := c.holdingLocked(key)
	if h == nil {
		return 0
	}
	var w SharedWrites
	if update.ContentDigest != "" && string(update.ContentDigest) != handleString(h.contentDigest) {
		w |= c.teachSharesLocked(entry, "", "", update.ContentDigest, nil)
	}
	if update.Evaluated && ranPartsReported(prepared.parts) {
		w |= c.runRanSharesLocked(key, entry, update.Replacements)
	}
	return w
}

// teachSharesLocked is teachRemoteIdentityLocked's plan: an entry's taught
// content digests grown, or given to its frame as carried, and an identity
// the e-graph lacks. Queuing the entry for the body worker writes only the
// worker's queue, under its own lock. Requires egraphMu.
func (c *Cache) teachSharesLocked(entry *sharedResult, recipe, request, content digest.Digest, term *RemoteTerm) SharedWrites {
	var w SharedWrites
	if content != "" && entry != nil && entry.id != 0 {
		if n := len(entry.taught); n == 0 || entry.taught[n-1] != content {
			w |= SharedTaughtContent
		} else if carried, _ := c.taughtContentStepLocked(entry); carried {
			w |= SharedTaughtContent
		}
	}
	if !c.remoteIdentityTeachesNothingLocked(entry, recipe, request, content, term) {
		w |= SharedIdentity
	}
	return w
}

// keepCallSharesLocked is keepCallLocked's plan: the entry's expiry, and a
// call new to it. Requires egraphMu.
func (c *Cache) keepCallSharesLocked(entry *sharedResult, desc RemoteHolding, kept *keptCall) SharedWrites {
	if desc.Request == "" || desc.Request == desc.Recipe {
		return 0
	}
	// A call expired by now is expired at the write too, which comes after.
	if desc.ExpiresAtUnix != 0 && desc.ExpiresAtUnix <= time.Now().Unix() {
		return 0
	}
	var w SharedWrites
	if mergeSharedResultExpiryUnix(entry.expiresAtUnix, desc.ExpiresAtUnix) != entry.expiresAtUnix {
		w |= SharedExpiry
	}
	if kept != nil {
		if _, ok := entry.calls[kept.id.recipe]; !ok {
			w |= SharedKeptCall
		}
	}
	return w
}

// runRanSharesLocked is noteHoldingRanLocked's plan: the entry's stored
// record's run noted as having run, when it names key's run at
// replacements and hasn't yet. Requires egraphMu.
func (c *Cache) runRanSharesLocked(key HolderKey, entry *sharedResult, replacements uint64) SharedWrites {
	if !c.blobBacked || entry == nil || entry.run == nil || entry.runRan != runNotRan {
		return 0
	}
	if run := *entry.run; key.Cache != run.Cache || key.Number != run.Number || replacements != run.Replacements {
		return 0
	}
	return SharedRunRan
}

// mergedRepliesSharesLocked is a merged reply's plan, for the targets of
// the reply prepared to apply, from the next: a reply lands records on the
// Cloud's entries, which it never creates, and its engine cache's state is
// created by its first holding there. Requires egraphMu.
func (c *Cache) mergedReplySharesLocked(a *MergedReplyApply) SharedWrites {
	if c.egraphDigestToClass == nil {
		return SharedNewEntry
	}
	if c.remoteCaches[a.cache] != nil {
		return 0
	}
	for _, t := range a.targets[a.next:] {
		if c.resultsByID[a.cloudEntries[t.value.Ordinal]] != nil {
			return SharedNewEngineCache
		}
	}
	return 0
}
