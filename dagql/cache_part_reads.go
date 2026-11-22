package dagql

import (
	"context"
	"slices"
	"sync"
	"time"
)

// partReadFlushInterval is the longest a read waits to be reported.
const partReadFlushInterval = time.Second

// PartRead is a part of an entry that work read; Part is empty when it read
// the whole entry. Session and At are set on a read that counts
// (partReadScope): the session, and when. They are empty on the engine-wide
// report of the entry.
type PartRead struct {
	ResultID uint64
	Part     PartKey
	Session  string
	At       time.Time
	// Trial only, for telemetry: Under is set on a read made under a cached
	// call (partReadScope.under): 1 when no other cached call stands between
	// it and the outermost one, 2 when one does, -1 when it can't be told.
	// Top is the outermost call's recipe on a read with Under 1, or empty
	// when that is an evaluation.
	Under int
	Top   string
}

// WithPartReadReport makes the cache report the parts of entries that work
// reads: those it asks Evaluate and EvaluateParts for, from when it asks,
// though they may still be being made, and those a delegated part is
// installed from. When work reads a part of an entry for
// the first time, the cache reports every part of the entry read so far, or
// the whole entry once work read it whole, so that each report about an
// entry is complete: a reader that missed earlier ones loses nothing. Reports
// go in batches, at most partReadFlushInterval after the read, and at the
// cache's close. report runs with no cache lock held and must not block.
//
// A read that counts (partReadScope) is also reported once per part and
// session, with the session and its time, in the same batches.
func WithPartReadReport(report func([]PartRead)) CacheOption {
	return func(c *Cache) {
		c.partReads = &partReadLog{report: report, seen: map[sharedResultID]*partReadsSeen{}, pending: map[sharedResultID]struct{}{}, sessions: map[string]map[sessionPartRead]struct{}{}}
	}
}

// partReadLog is what WithPartReadReport keeps: the parts of each live entry
// read so far, and the entries with reads not reported yet, in the order
// they were first read. A nil log records nothing. reporting is held through
// each flush, so reports go in the order they were taken, and a flush, as
// the cache's close makes, returns only once an earlier one, such as the
// timer's, has reported.
type partReadLog struct {
	report    func([]PartRead)
	reporting sync.Mutex
	mu        sync.Mutex
	seen      map[sharedResultID]*partReadsSeen
	pending   map[sharedResultID]struct{}
	order     []sharedResultID
	timer     *time.Timer
	// sessions are, per session, the parts its reads that count have read
	// so far, the whole entry as an empty part; sessionReads are those not
	// reported yet, in the order they were read.
	sessions     map[string]map[sessionPartRead]struct{}
	sessionReads []PartRead
}

// sessionPartRead is a part, or with no part the whole, of an entry that a
// session's read that counts read.
type sessionPartRead struct {
	id    sharedResultID
	part  PartKey
	under int
}

type partReadsSeen struct {
	whole bool
	parts []PartKey
}

// record notes that work read parts of entry id, or the whole of it when
// parts is empty, and arranges for the entry to be reported if that's new.
func (l *partReadLog) record(id sharedResultID, parts []PartKey) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := l.seen[id]
	if seen == nil {
		seen = &partReadsSeen{}
		l.seen[id] = seen
	}
	if seen.whole {
		return
	}
	added := false
	if len(parts) == 0 {
		seen.whole, seen.parts, added = true, nil, true
	}
	for _, part := range parts {
		if seen.whole || slices.Contains(seen.parts, part) {
			continue
		}
		seen.parts = append(seen.parts, part)
		added = true
	}
	if !added {
		return
	}
	if _, ok := l.pending[id]; !ok {
		l.pending[id] = struct{}{}
		l.order = append(l.order, id)
	}
	if l.timer == nil {
		l.timer = time.AfterFunc(partReadFlushInterval, l.flush)
	}
}

// recordPartRead notes that work read parts of entry id, or the whole of it
// when parts is empty: for the engine-wide report (partReadLog.record), and,
// when the read counts (partReadScope), once per part and session with the
// session and the time.
func (c *Cache) recordPartRead(ctx context.Context, id sharedResultID, parts []PartKey) {
	c.partReads.record(id, parts)
	if c.partReads == nil {
		return
	}
	scope := readScope(ctx)
	session, err := partSession(ctx)
	if err != nil {
		return
	}
	if !scope.counts {
		under, top := min(scope.under, 2), ""
		if under == 1 {
			top = scope.top.String()
		}
		c.partReads.recordSessionUnder(session, time.Now(), id, parts, under, top)
		return
	}
	c.partReads.recordSession(session, time.Now(), id, parts)
}

// recordSession notes a read of parts of entry id, or of the whole of it when
// parts is empty, that counted for session, and arranges for each part the
// session hasn't read so to be reported.
func (l *partReadLog) recordSession(session string, at time.Time, id sharedResultID, parts []PartKey) {
	l.recordSessionUnder(session, at, id, parts, 0, "")
}

// recordSessionUnder is recordSession for a read under a cached call, of
// class under and outermost call top (PartRead.Under), or a read that counts
// with under 0: once per part, session and class. Trial only.
func (l *partReadLog) recordSessionUnder(session string, at time.Time, id sharedResultID, parts []PartKey, under int, top string) {
	if l == nil || session == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := l.sessions[session]
	if seen == nil {
		seen = map[sessionPartRead]struct{}{}
		l.sessions[session] = seen
	}
	if _, whole := seen[sessionPartRead{id: id, under: under}]; whole {
		return
	}
	if len(parts) == 0 {
		parts = []PartKey{""}
	}
	added := false
	for _, part := range parts {
		key := sessionPartRead{id: id, part: part, under: under}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		l.sessionReads = append(l.sessionReads, PartRead{ResultID: uint64(id), Part: part, Session: session, At: at, Under: under, Top: top})
		added = true
	}
	if added && l.timer == nil {
		l.timer = time.AfterFunc(partReadFlushInterval, l.flush)
	}
}

// forgetSession drops what a released session read, once its reads are
// reported or never will be.
func (l *partReadLog) forgetSession(session string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sessions, session)
}

// forget drops what was read of an entry the cache no longer holds.
// Requires nothing; safe under the cache's locks.
func (l *partReadLog) forget(id sharedResultID) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.seen, id)
	delete(l.pending, id)
}

// flush reports, for each entry with reads not reported yet, every part of
// it read so far.
func (l *partReadLog) flush() {
	if l == nil {
		return
	}
	l.reporting.Lock()
	defer l.reporting.Unlock()
	l.mu.Lock()
	var reads []PartRead
	for _, id := range l.order {
		if _, ok := l.pending[id]; !ok {
			continue
		}
		seen := l.seen[id]
		if seen.whole {
			reads = append(reads, PartRead{ResultID: uint64(id)})
			continue
		}
		for _, part := range seen.parts {
			reads = append(reads, PartRead{ResultID: uint64(id), Part: part})
		}
	}
	reads = append(reads, l.sessionReads...)
	l.sessionReads = nil
	clear(l.pending)
	l.order = nil
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	l.mu.Unlock()
	if len(reads) > 0 {
		l.report(reads)
	}
}
