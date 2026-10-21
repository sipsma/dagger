package main

import "dagger/gobuild/internal/dagger"

// hdrTool builds gohdr, the helper's in-container tool. "gohdr unpack" writes
// out a header pack, the go module's scan of each Go file up to the end of its
// imports, for the plan. "gohdr run" builds a tier: it runs the plan's
// per-package scripts in one container, in dependency order.
func hdrTool() *dagger.File {
	// A standalone tool, so it is built with a pinned toolchain, statically,
	// and runs in whatever image the module builds with.
	return dag.Container().From(toolImage).
		WithEnvVariable("CGO_ENABLED", "0").
		WithNewFile("/gohdr-src/go.mod", "module gohdr\n\ngo 1.26\n").
		WithNewFile("/gohdr-src/main.go", hdrToolSrc).
		WithWorkdir("/gohdr-src").
		WithExec([]string{"go", "build", "-o", "/gohdr", "."}, noNest).File("/gohdr")
}

const hdrToolSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func unpack(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	r := bufio.NewReader(in)
	line := func() (string, error) {
		s, err := r.ReadString('\n')
		return strings.TrimSuffix(s, "\n"), err
	}
	if magic, err := line(); err != nil || magic != "gohdr 1" {
		return fmt.Errorf("not a gohdr pack")
	}
	for {
		rel, err := line()
		if err == io.EOF && rel == "" {
			return nil
		}
		if err != nil {
			return err
		}
		modeStr, err := line()
		if err != nil {
			return err
		}
		sizeStr, err := line()
		if err != nil {
			return err
		}
		mode, err := strconv.ParseUint(modeStr, 8, 32)
		if err != nil {
			return err
		}
		size, err := strconv.Atoi(sizeStr)
		if err != nil {
			return err
		}
		data := make([]byte, size+1)
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		p := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data[:size], fs.FileMode(mode)); err != nil {
			return err
		}
	}
}

type runNode struct {
	Name   string
	Pkg    string
	Group  string
	Deps   []string
	Output string
	Script string
}

type runSpec struct {
	Lib   string
	Nodes []runNode
}

// run builds every node of the spec in this container: each script once the
// nodes it depends on are done, as many at once as there are CPUs, the one
// with the heaviest chain of dependents first, weighing each node by the Go
// source it compiles, a stand-in for its compile time. Each output is linked into the lib directory, where dependents read
// it. With TL set in the environment it prints one "TL" timeline line per node.
func run(specPath string) error {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	var spec runSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return err
	}
	if err := os.MkdirAll(spec.Lib, 0o755); err != nil {
		return err
	}
	nodes := map[string]*runNode{}
	for i := range spec.Nodes {
		nodes[spec.Nodes[i].Name] = &spec.Nodes[i]
	}
	dependents := map[string][]string{}
	pending := map[string]int{}
	for _, n := range spec.Nodes {
		for _, d := range n.Deps {
			if _, ok := nodes[d]; ok {
				dependents[d] = append(dependents[d], n.Name)
				pending[n.Name]++
			}
		}
	}
	height := map[string]int64{}
	var heightOf func(string) int64
	heightOf = func(name string) int64 {
		if v, ok := height[name]; ok {
			return v
		}
		var above int64
		for _, up := range dependents[name] {
			above = max(above, heightOf(up))
		}
		height[name] = weight(nodes[name].Script) + above
		return height[name]
	}
	type result struct {
		name       string
		start, end time.Time
		out        []byte
		err        error
	}
	results := make(chan result)
	readyAt := map[string]time.Time{}
	var ready []string
	for name := range nodes {
		heightOf(name)
		if pending[name] == 0 {
			ready = append(ready, name)
			readyAt[name] = time.Now()
		}
	}
	timeline := os.Getenv("TL") != ""
	jobs := runtime.NumCPU()
	running, finished := 0, 0
	for finished < len(nodes) {
		sort.Slice(ready, func(i, j int) bool {
			if height[ready[i]] != height[ready[j]] {
				return height[ready[i]] > height[ready[j]]
			}
			return ready[i] < ready[j]
		})
		for running < jobs && len(ready) > 0 {
			n := nodes[ready[0]]
			ready = ready[1:]
			running++
			go func() {
				start := time.Now()
				out, err := runOne(n, spec.Lib)
				results <- result{n.Name, start, time.Now(), out, err}
			}()
		}
		r := <-results
		running--
		finished++
		n := nodes[r.name]
		if r.err != nil {
			os.Stderr.Write(r.out)
			return fmt.Errorf("%s: %v", n.Pkg, r.err)
		}
		if timeline {
			line, _ := json.Marshal(map[string]any{"pkg": n.Pkg, "name": n.Name, "group": n.Group, "deps": n.Deps,
				"ready": readyAt[n.Name].UnixMilli(), "started": r.start.UnixMilli(), "execDone": r.end.UnixMilli(),
				"finished": r.end.UnixMilli(), "libMs": 0, "srcMs": 0, "modMs": 0})
			fmt.Println("TL " + string(line))
		}
		for _, up := range dependents[r.name] {
			if pending[up]--; pending[up] == 0 {
				ready = append(ready, up)
				readyAt[up] = time.Now()
			}
		}
	}
	return nil
}

// weight is the bytes of Go source a script compiles, plus a fixed cost for
// starting the compile: the files a compile line names, each relative to the
// directory the script last changed to.
func weight(script string) int64 {
	total := int64(8 << 10)
	dir := "/"
	for _, line := range strings.Split(script, "\n") {
		if d, ok := strings.CutPrefix(line, "cd "); ok {
			dir = d
			continue
		}
		for _, f := range strings.Fields(line) {
			f = strings.Trim(f, "\"'")
			if !strings.HasSuffix(f, ".go") {
				continue
			}
			if !filepath.IsAbs(f) {
				f = filepath.Join(dir, f)
			}
			if fi, err := os.Stat(f); err == nil {
				total += fi.Size()
			}
		}
	}
	return total
}

// runOne runs one node's script and links its output into lib.
func runOne(n *runNode, lib string) ([]byte, error) {
	script := filepath.Join(os.TempDir(), n.Name+".sh")
	if err := os.WriteFile(script, []byte(n.Script), 0o644); err != nil {
		return nil, err
	}
	out, err := exec.Command("sh", script).CombinedOutput()
	if err != nil {
		return out, err
	}
	src := filepath.Join("/work", n.Name, n.Output)
	dst := filepath.Join(lib, n.Name+".a")
	if err := os.Link(src, dst); err != nil {
		data, err := os.ReadFile(src)
		if err != nil {
			return out, err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return out, err
		}
	}
	return out, nil
}

func main() {
	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 3 && args[0] == "unpack":
		err = unpack(args[1], args[2])
	case len(args) == 2 && args[0] == "run":
		err = run(args[1])
	default:
		err = fmt.Errorf("usage: gohdr unpack <pack> <dir>, or gohdr run <spec>")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gohdr:", err)
		os.Exit(1)
	}
}
`
