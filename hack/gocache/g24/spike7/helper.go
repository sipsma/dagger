package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// The protocol fields match cmd/go/internal/cacheprog in Go 1.26.
// stdout is exclusively the cache protocol. Telemetry goes to a separate file.
type request struct {
	ID       int64
	Command  string
	ActionID []byte
	OutputID []byte
	BodySize int64
}
type response struct {
	ID            int64
	Err           string     `json:",omitempty"`
	KnownCommands []string   `json:",omitempty"`
	Miss          bool       `json:",omitempty"`
	OutputID      []byte     `json:",omitempty"`
	Size          int64      `json:",omitempty"`
	Time          *time.Time `json:",omitempty"`
	DiskPath      string     `json:",omitempty"`
}
type entry struct {
	OutputID []byte
	Size     int64
}
type event struct {
	ID                int64  `json:"id"`
	Command           string `json:"command"`
	ActionID          string `json:"action_id,omitempty"`
	OutputID          string `json:"output_id,omitempty"`
	Miss              bool   `json:"miss,omitempty"`
	Error             string `json:"error,omitempty"`
	RequestBytes      int64  `json:"request_bytes"`
	BodyBytes         int64  `json:"body_bytes"`
	ResponseBytes     int64  `json:"response_bytes"`
	BodyReadNS        int64  `json:"body_read_ns"`
	QueueNS           int64  `json:"queue_ns"`
	DelayNS           int64  `json:"delay_ns"`
	StoreNS           int64  `json:"store_ns"`
	MaterializeNS     int64  `json:"materialize_ns"`
	MaterializedBytes int64  `json:"materialized_bytes"`
	StoreReadBytes    int64  `json:"store_read_bytes"`
	StoreWrittenBytes int64  `json:"store_written_bytes"`
	ResponseWaitNS    int64  `json:"response_wait_ns"`
	ResponseWriteNS   int64  `json:"response_write_ns"`
	HelperNS          int64  `json:"helper_ns"`
	ReceivedUnixNS    int64  `json:"received_unix_ns"`
	RespondedUnixNS   int64  `json:"responded_unix_ns"`
}
type helper struct {
	store, local, materialization string
	latency                       time.Duration
	workers                       int
	out                           io.Writer
	log                           *bufio.Writer
	outMu, logMu                  sync.Mutex
	actionMu                      sync.Mutex
	responseBytes                 atomic.Int64
	// Same-output requests share one invocation-local materialization.
	matMu sync.Mutex
	mats  map[string]*materialized
}
type materialized struct {
	done chan struct{}
	path string
	err  error
}

func helperMain(args []string) error {
	f := flag.NewFlagSet("helper", flag.ContinueOnError)
	h := &helper{out: os.Stdout, mats: map[string]*materialized{}}
	var logPath string
	f.StringVar(&h.store, "store", "", "local immutable backing store")
	f.StringVar(&h.local, "local", "", "per-build materialized files; retained until close")
	f.StringVar(&logPath, "log", "", "per-request JSONL")
	f.StringVar(&h.materialization, "materialization", "copy", "copy or direct")
	f.DurationVar(&h.latency, "latency", 0, "delay once per request, concurrently")
	f.IntVar(&h.workers, "workers", 64, "maximum concurrent requests")
	if err := f.Parse(args); err != nil {
		return err
	}
	if h.store == "" || h.local == "" || logPath == "" || h.workers < 1 || h.latency < 0 {
		return fmt.Errorf("store, local, log, positive workers and nonnegative latency required")
	}
	if h.materialization != "copy" && h.materialization != "direct" {
		return fmt.Errorf("unknown materialization")
	}
	for _, dir := range []string{h.store, h.local, filepath.Join(h.store, "objects"), filepath.Join(h.store, "actions"), filepath.Join(h.store, "incoming"), filepath.Dir(logPath)} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer log.Close()
	h.log = bufio.NewWriterSize(log, 128<<10)
	defer h.log.Flush()
	in := &countingReader{r: os.Stdin}
	err = h.serve(in)
	if flushErr := h.log.Flush(); err == nil {
		err = flushErr
	}
	statsErr := writeJSON(logPath+".wire.json", map[string]int64{"stdin_bytes": in.n, "stdout_bytes": h.responseBytes.Load()})
	if err != nil {
		return err
	}
	return statsErr
}

type countingReader struct {
	r io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}

func (h *helper) serve(in io.Reader) error {
	hello, _ := json.Marshal(response{KnownCommands: []string{"get", "put", "close"}})
	hello = append(hello, '\n')
	n, err := h.out.Write(hello)
	h.responseBytes.Add(int64(n))
	if err != nil {
		return err
	}
	br := bufio.NewReaderSize(in, 64<<10)
	sem := make(chan struct{}, h.workers)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	fail := func(err error) {
		if err != nil {
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
		}
	}
	for {
		// Go sends newline-delimited headers, followed by a quoted base64 line
		// only for nonempty puts. Do not charge idle time waiting for a header.
		line, err := br.ReadBytes('\n')
		if err != nil {
			wg.Wait()
			if err == io.EOF {
				return fmt.Errorf("protocol EOF without close")
			}
			return err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		start := time.Now()
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			wg.Wait()
			return err
		}
		ev := event{ID: req.ID, Command: req.Command, ActionID: hex.EncodeToString(req.ActionID),
			OutputID: hex.EncodeToString(req.OutputID), RequestBytes: int64(len(line)), ReceivedUnixNS: start.UnixNano()}
		var staged string
		if req.Command == "put" {
			t := time.Now()
			// Go 1.26 emits an extra newline after the request header.
			// Accept arbitrary JSON whitespace before a nonempty body.
			if req.BodySize > 0 {
				for {
					b, e := br.ReadByte()
					if e != nil {
						wg.Wait()
						return e
					}
					if b == ' ' || b == '\n' || b == '\r' || b == '\t' {
						ev.RequestBytes++
						continue
					}
					if e := br.UnreadByte(); e != nil {
						wg.Wait()
						return e
					}
					break
				}
			}
			staged, ev.BodyBytes, err = h.receiveBody(br, req)
			ev.BodyReadNS = time.Since(t).Nanoseconds()
			if req.BodySize > 0 {
				ev.RequestBytes += int64(base64.StdEncoding.EncodedLen(int(req.BodySize))) + 3
			}
			ev.StoreWrittenBytes += ev.BodyBytes
			if err != nil {
				wg.Wait()
				return err
			}
		}
		if req.Command == "close" {
			wg.Wait()
			fail(h.respond(req, &ev, start, staged))
			return firstErr
		}
		t := time.Now()
		sem <- struct{}{}
		ev.QueueNS = time.Since(t).Nanoseconds()
		wg.Add(1)
		go func(req request, ev event, staged string, start time.Time) {
			defer wg.Done()
			defer func() { <-sem }()
			fail(h.respond(req, &ev, start, staged))
		}(req, ev, staged, start)
	}
}

func (h *helper) receiveBody(br *bufio.Reader, req request) (string, int64, error) {
	if req.BodySize < 0 || req.BodySize > 8<<30 {
		return "", 0, fmt.Errorf("invalid body size %d", req.BodySize)
	}
	f, err := os.CreateTemp(filepath.Join(h.store, "incoming"), "put-")
	if err != nil {
		return "", 0, err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(f.Name())
		}
	}()
	sum := sha256.New()
	var n int64
	if req.BodySize > 0 {
		q, err := br.ReadByte()
		if err != nil || q != '"' {
			return "", 0, fmt.Errorf("missing body quote: %v", err)
		}
		wireLen := int64(base64.StdEncoding.EncodedLen(int(req.BodySize)))
		n, err = io.Copy(io.MultiWriter(f, sum), base64.NewDecoder(base64.StdEncoding, io.LimitReader(br, wireLen)))
		if err != nil {
			return "", n, err
		}
		tail := make([]byte, 2)
		if _, err := io.ReadFull(br, tail); err != nil || string(tail) != "\"\n" {
			return "", n, fmt.Errorf("invalid body tail: %v", err)
		}
	}
	if n != req.BodySize || !bytes.Equal(sum.Sum(nil), req.OutputID) {
		return "", n, fmt.Errorf("put size/output hash mismatch")
	}
	if err := f.Close(); err != nil {
		return "", n, err
	}
	ok = true
	return f.Name(), n, nil
}

func (h *helper) respond(req request, ev *event, start time.Time, staged string) error {
	if staged != "" {
		defer os.Remove(staged)
	}
	t := time.Now()
	if h.latency > 0 {
		time.Sleep(h.latency)
	}
	ev.DelayNS = time.Since(t).Nanoseconds()
	res := response{ID: req.ID}
	t = time.Now()
	var ent entry
	var err error
	switch req.Command {
	case "get":
		if len(req.ActionID) != sha256.Size {
			err = fmt.Errorf("invalid action ID")
			break
		}
		var data []byte
		data, err = os.ReadFile(filepath.Join(h.store, "actions", ev.ActionID))
		ev.StoreReadBytes += int64(len(data))
		if os.IsNotExist(err) {
			res.Miss = true
			err = nil
			break
		}
		if err == nil {
			err = json.Unmarshal(data, &ent)
		}
	case "put":
		if len(req.ActionID) != sha256.Size || len(req.OutputID) != sha256.Size {
			err = fmt.Errorf("invalid ID")
			break
		}
		ent = entry{req.OutputID, req.BodySize}
		// A hard link publishes a complete immutable blob without replacing an
		// existing blob; action metadata is published by atomic rename.
		dst := filepath.Join(h.store, "objects", ev.OutputID)
		if err = os.Link(staged, dst); os.IsExist(err) {
			err = nil
		}
		if err == nil {
			data, marshalErr := json.Marshal(ent)
			err = marshalErr
			if err == nil {
				h.actionMu.Lock()
				path := filepath.Join(h.store, "actions", ev.ActionID)
				old, readErr := os.ReadFile(path)
				ev.StoreReadBytes += int64(len(old))
				if readErr == nil && !bytes.Equal(old, data) {
					err = fmt.Errorf("conflicting publication for action %s", ev.ActionID)
				} else if readErr != nil && !os.IsNotExist(readErr) {
					err = readErr
				} else if os.IsNotExist(readErr) {
					err = atomicWrite(path, data)
					ev.StoreWrittenBytes += int64(len(data))
				}
				h.actionMu.Unlock()
			}
		}
	case "close":
	default:
		err = fmt.Errorf("unknown command %q", req.Command)
	}
	ev.StoreNS = time.Since(t).Nanoseconds()
	if err == nil && req.Command != "close" && !res.Miss {
		res.OutputID, res.Size = ent.OutputID, ent.Size
		ev.OutputID = hex.EncodeToString(ent.OutputID)
		t = time.Now()
		var n int64
		res.DiskPath, n, err = h.materialize(ev.OutputID, ent.Size)
		ev.MaterializeNS = time.Since(t).Nanoseconds()
		ev.MaterializedBytes = n
		ev.StoreReadBytes += n
	}
	if err != nil {
		res.Err = err.Error()
		ev.Error = res.Err
	}
	ev.Miss = res.Miss
	data, encodeErr := json.Marshal(res)
	if encodeErr != nil {
		return encodeErr
	}
	data = append(data, '\n')
	t = time.Now()
	h.outMu.Lock()
	ev.ResponseWaitNS = time.Since(t).Nanoseconds()
	t = time.Now()
	n, writeErr := h.out.Write(data)
	h.responseBytes.Add(int64(n))
	ev.ResponseWriteNS = time.Since(t).Nanoseconds()
	ev.RespondedUnixNS = time.Now().UnixNano()
	ev.HelperNS = time.Since(start).Nanoseconds()
	h.outMu.Unlock()
	ev.ResponseBytes = int64(len(data))
	h.logMu.Lock()
	logErr := json.NewEncoder(h.log).Encode(ev)
	h.logMu.Unlock()
	if writeErr != nil {
		return writeErr
	}
	if logErr != nil {
		return logErr
	}
	return err
}

func (h *helper) materialize(id string, size int64) (string, int64, error) {
	src := filepath.Join(h.store, "objects", id)
	if h.materialization == "direct" {
		st, err := os.Stat(src)
		if err == nil && st.Size() != size {
			err = fmt.Errorf("stored size mismatch")
		}
		return src, 0, err
	}
	h.matMu.Lock()
	if m := h.mats[id]; m != nil {
		h.matMu.Unlock()
		<-m.done
		return m.path, 0, m.err
	}
	m := &materialized{done: make(chan struct{}), path: filepath.Join(h.local, id)}
	h.mats[id] = m
	h.matMu.Unlock()
	n, err := copyFile(src, m.path)
	if err == nil && n != size {
		err = fmt.Errorf("materialized size mismatch")
	}
	m.err = err
	close(m.done)
	return m.path, n, err
}

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "publish-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return n, err
	}
	return n, closeErr
}
