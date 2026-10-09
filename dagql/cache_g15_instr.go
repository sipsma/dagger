package dagql

// Measurement only, not for merge: cross-checks the canonical equivalent
// search against the old digest-posting scan on every call, and records how
// often the pick differs from the requested result. Read at /debug/vars.

import (
	"expvar"
	"fmt"
	"sync/atomic"

	set "github.com/hashicorp/go-set/v3"
	"github.com/opencontainers/go-digest"
)

var (
	g15CanonCalls      = expvar.NewInt("g15_canon_calls")
	g15CanonChecked    = expvar.NewInt("g15_canon_checked")
	g15CanonMismatch   = expvar.NewInt("g15_canon_mismatch")
	g15CanonCandidates = expvar.NewInt("g15_canon_candidates")
	g15CanonFallback   = expvar.NewInt("g15_canon_fallback_requested")
	g15CanonSame       = expvar.NewInt("g15_canon_picked_requested")
	g15CanonSwitched   = expvar.NewInt("g15_canon_switched")
	// switched although the requested result was itself eligible: the calls
	// whose answer "return the requested result" would change.
	g15CanonSwitchedReqEligible = expvar.NewInt("g15_canon_switched_requested_eligible")
	g15CanonCases               = expvar.NewMap("g15_canon_cases")
	g15CanonMismatches          = expvar.NewMap("g15_canon_mismatches")
	g15CanonCaseN               atomic.Int64
	g15CanonMismatchN           atomic.Int64
)

func (c *Cache) g15CanonCrossCheckLocked(sessionID string, res *sharedResult, raw *set.TreeSet[*sharedResult], eligible *set.TreeSet[*sharedResult], picked *sharedResult, nowUnix int64) {
	g15CanonCalls.Add(1)
	g15CanonCandidates.Add(int64(raw.Size()))

	old := newSharedResultSet()
	for outputEqID := range c.outputEqClassRootsLocked(res.id) {
		for dig := range c.eqClassToDigests[outputEqID] {
			c.appendDigestResultsLocked(old, digest.Digest(dig), nowUnix, nil)
		}
	}
	g15CanonChecked.Add(1)
	if !old.Equal(raw) {
		g15CanonMismatch.Add(1)
		if n := g15CanonMismatchN.Add(1); n <= 20 {
			g15CanonMismatches.Add(fmt.Sprintf("%03d res=%d new=%v old=%v", n, res.id, g15IDs(raw), g15IDs(old)), 1)
		}
	}

	switch {
	case picked == nil:
		g15CanonFallback.Add(1)
		return
	case picked == res:
		g15CanonSame.Add(1)
		return
	}
	g15CanonSwitched.Add(1)
	reqEligible := eligible.Contains(res) && c.sessionSatisfiesResourceRequirementsLocked(sessionID, res)
	if reqEligible {
		g15CanonSwitchedReqEligible.Add(1)
	}
	if n := g15CanonCaseN.Add(1); n <= 50 {
		g15CanonCases.Add(fmt.Sprintf("%03d req{%s} picked{%s} reqEligible=%t reqExpired=%t candidates=%v",
			n, g15Describe(res), g15Describe(picked), reqEligible, c.resultExpiredAtLocked(res, nowUnix), g15IDs(raw)), 1)
	}
}

func g15Describe(res *sharedResult) string {
	var field string
	var content digest.Digest
	if frame := res.loadResultCall(); frame != nil {
		field = frame.Field
		content = frame.ContentDigest()
	}
	return fmt.Sprintf("id=%d type=%s field=%s attach=%d noValue=%t deps=%d hasValue=%t content=%.19s",
		res.id, res.recordType, field, res.attachmentState(), res.noValueLocked(), len(res.deps), res.hasValue, content)
}

func g15IDs(results *set.TreeSet[*sharedResult]) []sharedResultID {
	ids := make([]sharedResultID, 0, results.Size())
	for res := range results.Items() {
		ids = append(ids, res.id)
	}
	return ids
}
