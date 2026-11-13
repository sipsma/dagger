package remotecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/remotecache/protocol"
)

// A push sends the Cloud the records of the results the engine computed, as
// each is retained (protocol.Push): the cache queues them (PushQueue), and
// the session's pusher pushes everything queued since its last push was
// answered, one push in flight, so pushes batch by their round trip. A push
// asks the Cloud first which rows of its roots' closures it lacks, naming
// every row the engine doesn't know the Cloud has (protocol.CheckRows), and
// then sends those in full and the rest as held rows. A held row the Cloud
// finds stale as it commits is sent in full in one more push. A settle waits
// until every result queued has been pushed (settles), so the Cloud has the
// records of a session's results when it settles for it.

// errPushTooLarge is a push whose message would exceed the message limit.
var errPushTooLarge = errors.New("push too large")

// pushes pushes the results the cache queues, until the connection ends. A
// result taken and not pushed when it ends is put back, for the next
// connection's pusher.
func (s *session) pushes() {
	q := s.adapter.Pushes()
	for s.ctx.Err() == nil {
		roots, flushed, err := q.TakeRemotePushes(s.ctx)
		if err != nil {
			return
		}
		s.push(q, roots, flushed)
	}
}

// push pushes roots until the Cloud has committed them, and says what
// became of each (PushQueue). A root gone from the cache, or one whose
// closure the capture refuses, is dropped; one the capture finds busy waits
// for the next push, unless a settle's flush asked for this one, when no
// session is live to finish what keeps it busy, and it is dropped too. A
// push too large for one message is split in halves; a push the Cloud
// refuses is split too, so that one bad root doesn't cost the others, and a
// root refused alone is dropped. When the connection ends, what wasn't
// pushed is put back.
func (s *session) push(q PushQueue, roots []uint64, flushed bool) {
	start := time.Now()
	rows := &dagql.PushRows{List: true}
	for round := 1; ; round++ {
		var exp Export
		var pushed protocol.Pushed
		var checked protocol.CheckedRows
		// The roots are retained as of the moment before the capture: a
		// root whose retention ends after it is observed so later.
		captured := time.Now().UnixNano()
		err := s.adapter.Push(s.ctx, roots, s.known, rows, func(ctx context.Context, e Export) error {
			exp = e
			if e.Bundle == nil {
				return nil
			}
			var typ protocol.Type
			var msg, answer any
			if rows.List {
				typ, msg, answer = protocol.TypeCheckRows, protocol.CheckRows{Rows: e.Bundle.Values}, &checked
			} else {
				typ, msg, answer = protocol.TypePush, protocol.Push{Bundle: *e.Bundle, EngineTimeUnixNano: captured}, &pushed
			}
			if data, err := json.Marshal(msg); err != nil {
				return err
			} else if len(data) > s.cfg.maxMessage {
				return errPushTooLarge
			}
			env, err := s.request(ctx, typ, msg)
			if err != nil {
				return err
			}
			return env.Decode(answer)
		})
		if s.ctx.Err() != nil {
			q.RequeueRemotePushes(roots)
			return
		}
		if err != nil {
			s.pushFailed(q, roots, flushed, err)
			return
		}
		// What the capture left out has its fate now; the rest goes on.
		var busy []uint64
		left := map[uint64]bool{}
		for _, skipped := range exp.Skipped {
			left[skipped.Number] = true
			switch {
			case skipped.Reason == protocol.SkipBusy && !flushed:
				busy = append(busy, skipped.Number)
			case skipped.Reason == protocol.SkipBusy:
				s.cfg.Logger.Info("remote cache: push dropped a root still busy at a settle", "root", skipped.Number, "message", skipped.Message)
			case skipped.Reason == protocol.SkipRefused:
				s.cfg.Logger.Warn("remote cache: push refused a root", "root", skipped.Number, "message", skipped.Message)
			}
		}
		if len(busy) > 0 {
			q.WaitRemotePushesBusy(busy)
		}
		q.RemotePushesDone(len(exp.Skipped) - len(busy))
		kept := roots[:0:0]
		for _, root := range roots {
			if !left[root] {
				kept = append(kept, root)
			}
		}
		roots = kept
		if exp.Bundle == nil {
			return
		}
		if rows.List {
			full := make(map[uint64]bool, len(checked.Lacks))
			for _, number := range checked.Lacks {
				full[number] = true
			}
			rows = &dagql.PushRows{Full: full}
			continue
		}
		if len(pushed.Stale) == 0 {
			s.known.Merged(exp.Rows)
			q.RemotePushesDone(len(roots))
			s.cfg.Logger.Debug("remote cache: pushed", "roots", len(roots), "rows", len(exp.Bundle.Values), "rounds", round, "duration", time.Since(start))
			return
		}
		// The Cloud lacks the stale rows after all: the held rows it was
		// known to have are forgotten, and the next push carries every
		// stale row in full. Each push carries more rows in full, so this
		// ends.
		s.known.Stale(exp.Rows, pushed.Stale)
		full := make(map[uint64]bool, len(rows.Full)+len(pushed.Stale))
		for number := range rows.Full {
			full[number] = true
		}
		grew := false
		for _, number := range pushed.Stale {
			if !full[number] {
				full[number] = true
				grew = true
			}
		}
		if !grew {
			s.pushFailed(q, roots, flushed, fmt.Errorf("push: the Cloud answered stale rows already sent in full: %v", pushed.Stale))
			return
		}
		rows = &dagql.PushRows{Full: full}
	}
}

// pushFailed splits a push that failed, or drops its one root, as push
// describes.
func (s *session) pushFailed(q PushQueue, roots []uint64, flushed bool, err error) {
	if len(roots) > 1 {
		half := len(roots) / 2
		s.push(q, roots[:half], flushed)
		s.push(q, roots[half:], flushed)
		return
	}
	s.cfg.Logger.Warn("remote cache: push failed; dropping its root", "roots", roots, "error", err)
	q.RemotePushesDone(len(roots))
}

// waitPushes waits until every result queued has been pushed or dropped, as
// a settle does before it asks the service: the service has the records of
// the sessions it settles for. It flushes the results waiting busy, since no
// session is live to finish what keeps them busy. It reports false when ctx
// ended first.
func (s *session) waitPushes(ctx context.Context) bool {
	q := s.adapter.Pushes()
	for {
		// A push in flight as the settle began can find a root busy after
		// it: each change flushes again, and a flushed busy root is
		// dropped, so this ends. The wait subscribes to the queue's changes
		// before it flushes, so a root parked busy after the flush wakes it.
		idle, changed := q.RemotePushesIdle()
		if idle {
			return true
		}
		q.FlushRemotePushes()
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

// logPushWait records a settle's wait for its pushes.
func logPushWait(logger *slog.Logger, period uint64, waited time.Duration) {
	if waited > 0 {
		logger.Info("remote cache: settle waited for pushes", "period", period, "waited", waited)
	}
}
