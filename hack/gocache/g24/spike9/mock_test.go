package main

// A protocol unit-test fake, never an implementation or performance arm.
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func mockEngine(t *testing.T) *engineStore {
	t.Helper()
	var mu sync.Mutex
	files := map[string][]byte{}
	actions := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req struct {
			Query     string
			Variables map[string]string
		}
		json.NewDecoder(r.Body).Decode(&req)
		v := req.Variables
		var out any
		switch {
		case strings.Contains(req.Query, "host{file"):
			out = map[string]any{"host": map[string]any{"file": map[string]string{"id": v["path"]}}}
		case strings.Contains(req.Query, "_spike9Put"):
			b, err := os.ReadFile(v["f"])
			if err != nil {
				t.Error(err)
			}
			name := filepath.Base(v["f"])
			files[name] = b
			actions[v["k"]] = name
			out = map[string]any{"_spike9Put": engineEntry{name, name, int64(len(b))}}
		case strings.Contains(req.Query, "_spike9Get"):
			var value *engineEntry
			if name, ok := actions[v["k"]]; ok {
				value = &engineEntry{name, name, int64(len(files[name]))}
			}
			out = map[string]any{"_spike9Get": value}
		default:
			if err := os.WriteFile(v["path"], files[v["id"]], 0644); err != nil {
				t.Error(err)
			}
			out = map[string]any{"node": map[string]string{"export": v["path"]}}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": out})
	}))
	t.Cleanup(server.Close)
	return &engineStore{provider: "test/v1", toolchain: "test", endpoint: server.URL, client: server.Client()}
}
