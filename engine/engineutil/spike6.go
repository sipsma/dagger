package engineutil

// Experimental control plane for single-use prepared OCI containers. It is
// deliberately gated and is not a public API or a reusable container pool.
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dagger/dagger/engine/distconsts"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

var spike6Gates = struct {
	sync.Mutex
	gates map[string]*spike6Gate
}{gates: map[string]*spike6Gate{}}

type spike6Gate struct {
	readyR, readyW, releaseR, releaseW *os.File
	token                              string
	ready                              chan struct{}
	closed                             chan struct{}
	once                               sync.Once
	mu                                 sync.Mutex
	released                           bool
}

func newSpike6Gate(ctx context.Context, spec *specs.Spec, fds *initFDs) (*spike6Gate, error) {
	if os.Getenv("_DAGGER_SPIKE6") != "1" {
		return nil, nil
	}
	var token string
	env := make([]string, 0, len(spec.Process.Env))
	for _, kv := range spec.Process.Env {
		if v, ok := strings.CutPrefix(kv, "_DAGGER_SPIKE6_TOKEN="); ok {
			token = v
		} else {
			env = append(env, kv)
		}
	}
	if token == "" {
		return nil, nil
	}
	if len(spec.Process.Args) == 0 || spec.Process.Args[0] != initPath || spec.Process.Terminal {
		return nil, fmt.Errorf("spike6 requires injected init and no TTY")
	}
	spec.Process.Env = env
	g := &spike6Gate{token: token, ready: make(chan struct{}), closed: make(chan struct{})}
	var err error
	g.readyR, g.readyW, err = os.Pipe()
	if err != nil {
		return nil, err
	}
	g.releaseR, g.releaseW, err = os.Pipe()
	if err != nil {
		g.readyR.Close()
		g.readyW.Close()
		return nil, err
	}
	spike6Gates.Lock()
	if _, exists := spike6Gates.gates[token]; exists {
		spike6Gates.Unlock()
		g.closeFiles()
		return nil, fmt.Errorf("duplicate spike6 token")
	}
	spike6Gates.gates[token] = g
	spike6Gates.Unlock()
	fds.add(distconsts.Spike6ReadyFDEnv, g.readyW)
	fds.add(distconsts.Spike6ReleaseFDEnv, g.releaseR)
	go func() {
		var b [1]byte
		if _, err := io.ReadFull(g.readyR, b[:]); err == nil && b[0] == 1 {
			close(g.ready)
		}
	}()
	// Close the pipe on cancellation even if runc is waiting for init to exit.
	go func() {
		select {
		case <-ctx.Done():
			g.close()
		case <-g.closed:
		}
	}()
	return g, nil
}

func (g *spike6Gate) closeFiles() {
	g.readyR.Close()
	g.readyW.Close()
	g.releaseR.Close()
	g.releaseW.Close()
}

func (g *spike6Gate) close() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		spike6Gates.Lock()
		delete(spike6Gates.gates, g.token)
		spike6Gates.Unlock()
		g.mu.Lock()
		close(g.closed)
		g.closeFiles()
		g.mu.Unlock()
	})
}

// Spike6Debug is installed only on an explicitly enabled dev engine. Tokens
// identify pending calls, never cached results; release cannot change inputs.
func Spike6Debug(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("_DAGGER_SPIKE6") != "1" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("op") == "resources" {
		values, err := spike6Resources()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(values)
		return
	}
	token := r.URL.Query().Get("token")
	spike6Gates.Lock()
	g := spike6Gates.gates[token]
	active := len(spike6Gates.gates)
	spike6Gates.Unlock()
	if token == "" {
		json.NewEncoder(w).Encode(map[string]int{"active": active})
		return
	}
	if g == nil {
		http.Error(w, "not registered", 404)
		return
	}
	select {
	case <-g.ready:
	default:
		http.Error(w, "not ready", 409)
		return
	}
	if r.Method == http.MethodPost {
		g.mu.Lock()
		if !g.released {
			_, err := g.releaseW.Write([]byte{1})
			if err != nil {
				g.mu.Unlock()
				http.Error(w, err.Error(), 500)
				return
			}
			g.released = true
		}
		g.mu.Unlock()
	}
	json.NewEncoder(w).Encode(map[string]any{"ready": true, "active": active})
}

// Charge the parent of /engine, which includes sibling /exec groups. Failing
// closed here avoids silently treating engine-only CPU as the cost of execs.
func spike6Resources() (map[string]any, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	var path string
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "0::"); ok {
			path = v
		}
	}
	if filepath.Base(path) != "engine" {
		return nil, fmt.Errorf("need /engine cgroup with sibling exec groups; got %q", path)
	}
	root := filepath.Join("/sys/fs/cgroup", filepath.Dir(path))
	result := map[string]any{"cgroup": path, "charged_parent": root, "at_ns": time.Now().UnixNano()}
	for _, name := range []string{"cpu.stat", "memory.current", "memory.peak", "cgroup.procs"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		result[name] = string(data)
	}
	return result, nil
}
