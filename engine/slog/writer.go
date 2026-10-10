package slog

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
)

// Writer queues log output for a shared destination (usually stderr) and
// writes it from one background goroutine, so goroutines that log at the
// same time do not each wait for every other goroutine's write(2).
//
// Bytes reach the destination in exactly the order Write calls queued them,
// and none are dropped. Each Write is queued whole, so a log line is never
// split or interleaved with another. Queued bytes are bounded by limit: Write
// blocks while the queue is full, so a destination that cannot keep up slows
// callers down instead of losing output.
//
// Write returns once its bytes are queued, except for lines whose level is
// WARN or worse when SyncWarn is set; those wait until they are written, as
// with a plain locked writer. Flush waits for everything queued so far.
type Writer struct {
	out   io.Writer
	limit int

	// SyncWarn makes Write wait for lines at WARN level or worse.
	SyncWarn bool

	mu      sync.Mutex
	cond    sync.Cond // broadcast after each batch, for writers waiting on the limit
	pending []byte
	// pendingDone and inflightDone, when not nil, are closed once the queued
	// bytes and the batch being written are written. They are made only when
	// someone waits, and each wakes only the waiters of its own batch.
	pendingDone  chan struct{}
	inflightDone chan struct{}
	inflight     bool
	spare        []byte
	kick         chan struct{}
}

// NewWriter returns a Writer to out that queues at most limit bytes.
func NewWriter(out io.Writer, limit int) *Writer {
	w := &Writer{out: out, limit: limit, kick: make(chan struct{}, 1)}
	w.cond.L = &w.mu
	go w.drain()
	return w
}

func (w *Writer) Write(p []byte) (int, error) {
	if done := w.enqueue(p, w.SyncWarn && isWarnOrWorse(p)); done != nil {
		<-done
	}
	return len(p), nil
}

// WriteSync queues p and waits until it is written.
func (w *Writer) WriteSync(p []byte) (int, error) {
	<-w.enqueue(p, true)
	return len(p), nil
}

// enqueue queues p. With wait, it returns a channel closed once p is written.
func (w *Writer) enqueue(p []byte, wait bool) chan struct{} {
	w.mu.Lock()
	for len(w.pending) > 0 && len(w.pending)+len(p) > w.limit {
		w.cond.Wait()
	}
	wasEmpty := len(w.pending) == 0
	w.pending = append(w.pending, p...)
	var done chan struct{}
	if wait {
		if w.pendingDone == nil {
			w.pendingDone = make(chan struct{})
		}
		done = w.pendingDone
	}
	w.mu.Unlock()
	if wasEmpty {
		// The drainer empties the queue before it waits again, so a kick is
		// needed only when the queue goes from empty to non-empty.
		select {
		case w.kick <- struct{}{}:
		default:
		}
	}
	return done
}

// Flush waits until everything queued before the call has been written.
func (w *Writer) Flush() {
	w.mu.Lock()
	var done chan struct{}
	switch {
	case len(w.pending) > 0:
		// Batches are written in order, so this covers any batch in flight.
		if w.pendingDone == nil {
			w.pendingDone = make(chan struct{})
		}
		done = w.pendingDone
	case w.inflight:
		if w.inflightDone == nil {
			w.inflightDone = make(chan struct{})
		}
		done = w.inflightDone
	}
	w.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (w *Writer) drain() {
	for range w.kick {
		w.mu.Lock()
		for len(w.pending) > 0 {
			buf := w.pending
			w.pending, w.spare = w.spare[:0], nil
			w.inflight, w.inflightDone, w.pendingDone = true, w.pendingDone, nil
			w.mu.Unlock()
			// Errors are dropped like a plain log writer's: there is nowhere
			// to report them.
			_, _ = w.out.Write(buf)
			w.mu.Lock()
			w.inflight = false
			if w.inflightDone != nil {
				close(w.inflightDone)
				w.inflightDone = nil
			}
			if cap(buf) <= 2*w.limit {
				w.spare = buf[:0]
			}
			w.cond.Broadcast()
		}
		w.mu.Unlock()
	}
}

// isWarnOrWorse reports whether a formatted line is at WARN level or worse,
// from the level field slog's TextHandler and logrus's TextFormatter put
// near its start.
func isWarnOrWorse(p []byte) bool {
	i := bytes.Index(p[:min(len(p), 96)], []byte(" level="))
	if i < 0 {
		return false
	}
	lv := p[i+len(" level="):]
	for _, s := range [][]byte{[]byte("WARN"), []byte("ERROR"), []byte("warning"), []byte("error"), []byte("fatal"), []byte("panic")} {
		if bytes.HasPrefix(lv, s) {
			return true
		}
	}
	return false
}

type Handler = slog.Handler

// SyncWriter is an io.Writer whose Write waits until its bytes are written.
type SyncWriter struct{ *Writer }

func (s SyncWriter) Write(p []byte) (int, error) { return s.WriteSync(p) }

// FlushHandler waits after each record until it is written. slog's handlers
// hold their mutex across the destination Write, so waiting there would
// serialize callers again; waiting here, after Handle returns, lets
// concurrent records share one write.
type FlushHandler struct {
	slog.Handler
	W *Writer
}

func (h FlushHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.Handler.Handle(ctx, r)
	h.W.Flush()
	return err
}

func (h FlushHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return FlushHandler{h.Handler.WithAttrs(attrs), h.W}
}

func (h FlushHandler) WithGroup(name string) slog.Handler {
	return FlushHandler{h.Handler.WithGroup(name), h.W}
}
