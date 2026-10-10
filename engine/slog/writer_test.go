package slog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder is a destination that records each write and can be slowed down.
type recorder struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	writes  int
	delay   time.Duration
	inside  atomic.Int32
	overlap atomic.Bool
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.inside.Add(1) > 1 {
		r.overlap.Store(true)
	}
	defer r.inside.Add(-1)
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	return r.buf.Write(p)
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func checkLines(t *testing.T, out string, goroutines, perG int) {
	t.Helper()
	next := make([]int, goroutines)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != goroutines*perG {
		t.Fatalf("got %d lines, want %d", len(lines), goroutines*perG)
	}
	for _, l := range lines {
		var g, i int
		if _, err := fmt.Sscanf(l, "g=%d i=%d end", &g, &i); err != nil {
			t.Fatalf("torn or malformed line %q: %v", l, err)
		}
		if i != next[g] {
			t.Fatalf("goroutine %d: line %d out of order, want %d", g, i, next[g])
		}
		next[g]++
	}
}

func runWriters(w io.Writer, goroutines, perG int) {
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perG {
				fmt.Fprintf(w, "g=%d i=%d end\n", g, i)
			}
		}()
	}
	wg.Wait()
}

func TestWriterOrderAndIntegrity(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(fmt.Sprintf("sync=%v", sync), func(t *testing.T) {
			rec := &recorder{delay: 20 * time.Microsecond}
			w := NewWriter(rec, 4096)
			var out io.Writer = w
			if sync {
				out = SyncWriter{w}
			}
			runWriters(out, 16, 500)
			w.Flush()
			checkLines(t, rec.String(), 16, 500)
			if rec.overlap.Load() {
				t.Fatal("destination written concurrently")
			}
			if rec.writes >= 16*500 {
				t.Fatalf("no batching: %d writes for %d lines", rec.writes, 16*500)
			}
		})
	}
}

// SyncWriter's Write returns only after its bytes reached the destination.
func TestWriterSyncWrittenOnReturn(t *testing.T) {
	rec := &recorder{delay: 50 * time.Microsecond}
	w := SyncWriter{NewWriter(rec, 1<<20)}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				line := fmt.Sprintf("g=%d i=%d end\n", g, i)
				w.Write([]byte(line))
				if !strings.Contains(rec.String(), line) {
					t.Errorf("line %q not written when Write returned", line)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// A slow destination blocks writers at the limit instead of growing the
// queue or dropping output.
func TestWriterAsyncBackpressure(t *testing.T) {
	rec := &recorder{delay: 2 * time.Millisecond}
	const limit = 1024
	w := NewWriter(rec, limit)
	var maxPending atomic.Int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			w.mu.Lock()
			if n := int64(len(w.pending)); n > maxPending.Load() {
				maxPending.Store(n)
			}
			w.mu.Unlock()
			time.Sleep(50 * time.Microsecond)
		}
	}()
	runWriters(w, 8, 100)
	w.Flush()
	close(stop)
	checkLines(t, rec.String(), 8, 100)
	if m := maxPending.Load(); m > limit+64 {
		t.Fatalf("queue grew to %d bytes, limit %d", m, limit)
	}
}

// Flush returns only once everything written before it is out.
func TestWriterAsyncFlush(t *testing.T) {
	rec := &recorder{delay: time.Millisecond}
	w := NewWriter(rec, 1<<20)
	runWriters(w, 4, 50)
	w.Flush()
	checkLines(t, rec.String(), 4, 50)
}

// With SyncWarn, WARN and worse lines are written when Write returns; other
// lines may still be queued.
func TestWriterSyncWarn(t *testing.T) {
	rec := &recorder{delay: 2 * time.Millisecond}
	w := NewWriter(rec, 1<<20)
	w.SyncWarn = true
	lg := slog.New(slog.NewTextHandler(w, nil))
	lgr := logrusLine("warning")
	for i := range 20 {
		lg.Info("info line", "i", i)
		lg.Warn("warn line", "i", i)
		if !strings.Contains(rec.String(), fmt.Sprintf("msg=\"warn line\" i=%d\n", i)) {
			t.Fatalf("warn line %d not written on return", i)
		}
		w.Write(lgr)
		if !strings.Contains(rec.String(), string(lgr)) {
			t.Fatal("logrus warning not written on return")
		}
	}
	if !isWarnOrWorse([]byte("time=2026-10-10T00:00:00.000Z level=ERROR msg=x\n")) ||
		isWarnOrWorse([]byte("time=2026-10-10T00:00:00.000Z level=INFO msg=\"level=WARN\"\n")) {
		t.Fatal("level detection")
	}
}

func logrusLine(level string) []byte {
	return []byte("time=\"2026-10-10T00:00:00Z\" level=" + level + " msg=\"x\"\n")
}

// FlushHandler returns only once the record is written, and concurrent
// records still share writes.
func TestFlushHandler(t *testing.T) {
	rec := &recorder{delay: 100 * time.Microsecond}
	w := NewWriter(rec, 1<<20)
	lg := slog.New(FlushHandler{slog.NewTextHandler(w, nil), w}).With("k", "v")
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				lg.InfoContext(context.Background(), "line", "g", g, "i", i)
				if !strings.Contains(rec.String(), fmt.Sprintf("g=%d i=%d\n", g, i)) {
					t.Errorf("record g=%d i=%d not written on return", g, i)
					return
				}
			}
		}()
	}
	wg.Wait()
	if rec.writes >= 1600 {
		t.Fatalf("no batching: %d writes", rec.writes)
	}
}
