package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProtocol(t *testing.T) {
	for _, mode := range []string{"copy", "direct"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			for _, d := range []string{"store/objects", "store/actions", "store/incoming", "local"} {
				if err := os.MkdirAll(filepath.Join(root, d), 0755); err != nil {
					t.Fatal(err)
				}
			}
			input, send := io.Pipe()
			output, recv := io.Pipe()
			var logs bytes.Buffer
			h := &helper{store: filepath.Join(root, "store"), local: filepath.Join(root, "local"), materialization: mode,
				latency: time.Millisecond, workers: 4, out: recv, log: bufio.NewWriter(&logs), mats: map[string]*materialized{}}
			done := make(chan error, 1)
			go func() { err := h.serve(input); h.log.Flush(); recv.Close(); done <- err }()
			dec := json.NewDecoder(output)
			var hello response
			if err := dec.Decode(&hello); err != nil {
				t.Fatal(err)
			}
			if len(hello.KnownCommands) != 3 {
				t.Fatal("missing capabilities")
			}
			var id int64
			call := func(cmd string, key, body []byte) response {
				t.Helper()
				id++
				r := request{ID: id, Command: cmd, ActionID: key}
				if cmd == "put" {
					hash := sha256.Sum256(body)
					r.OutputID = hash[:]
					r.BodySize = int64(len(body))
				}
				var wire bytes.Buffer
				if err := json.NewEncoder(&wire).Encode(r); err != nil {
					t.Fatal(err)
				}
				// Actual Go writer emits this extra newline.
				if _, err := fmt.Fprintln(&wire); err != nil {
					t.Fatal(err)
				}
				if len(body) > 0 {
					if _, err := fmt.Fprintf(&wire, "\"%s\"\n", base64.StdEncoding.EncodeToString(body)); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := send.Write(wire.Bytes()); err != nil {
					t.Fatal(err)
				}
				var res response
				if err := dec.Decode(&res); err != nil {
					t.Fatal(err)
				}
				if res.Err != "" || res.ID != id {
					t.Fatalf("bad response %+v", res)
				}
				return res
			}
			for _, body := range [][]byte{nil, []byte("immutable archive\x00with bytes")} {
				k := sha256.Sum256(append([]byte("key"), body...))
				if !call("get", k[:], nil).Miss {
					t.Fatal("fresh key hit")
				}
				put := call("put", k[:], body)
				get := call("get", k[:], nil)
				if get.Miss || get.DiskPath != put.DiskPath {
					t.Fatal("miss became sticky or wrong path")
				}
				got, err := os.ReadFile(get.DiskPath)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("materialization %q %v", got, err)
				}
				call("put", k[:], body) // idempotent publication
			}
			call("close", nil, nil)
			send.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var events []event
			sc := bufio.NewScanner(&logs)
			for sc.Scan() {
				var ev event
				if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
					t.Fatal(err)
				}
				events = append(events, ev)
			}
			if len(events) != 9 {
				t.Fatalf("got %d events", len(events))
			}
			var materialized int64
			for _, ev := range events {
				if ev.DelayNS <= 0 || ev.HelperNS < ev.DelayNS {
					t.Fatal("missing delay accounting")
				}
				materialized += ev.MaterializedBytes
			}
			if mode == "copy" && materialized != int64(len("immutable archive\x00with bytes")) {
				t.Fatalf("duplicate copies: %d", materialized)
			}
			if mode == "direct" && materialized != 0 {
				t.Fatal("direct mode copied")
			}
		})
	}
}

func TestHashMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "incoming"), 0755); err != nil {
		t.Fatal(err)
	}
	h := &helper{store: dir}
	_, _, err := h.receiveBody(bufio.NewReader(bytes.NewBufferString("\"eA==\"\n")), request{BodySize: 1, OutputID: make([]byte, 32)})
	if err == nil {
		t.Fatal("accepted corrupt body")
	}
}

func TestGraphCorrelation(t *testing.T) {
	root := t.TempDir()
	key := sha256.Sum256([]byte("a"))
	acts := []action{{ID: 4, Mode: "build", Package: "example/p", ActionID: graphPrefix(key[:]), Deps: []int{9}}}
	if err := writeJSON(filepath.Join(root, "graph.json"), acts); err != nil {
		t.Fatal(err)
	}
	unknown := sha256.Sum256([]byte("unmapped"))
	events := []event{
		{ID: 1, Command: "get", ActionID: fmt.Sprintf("%x", key), Miss: true},
		{ID: 2, Command: "put", ActionID: fmt.Sprintf("%x", subkey(key[:], "stdout"))},
		{ID: 3, Command: "get", ActionID: fmt.Sprintf("%x", unknown), Miss: true},
		{ID: 4, Command: "close"},
	}
	f, err := os.Create(filepath.Join(root, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if err := json.NewEncoder(f).Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	a, err := analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Visibility.DirectUnique != 1 || a.Visibility.SubkeyUnique != 1 || a.Visibility.Unmapped != 1 || a.Visibility.CacheRequests != 3 {
		t.Fatalf("%+v", a.Visibility)
	}
}
