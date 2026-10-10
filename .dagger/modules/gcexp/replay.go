package main

// Prototype: replay the go command's own build plan ("go build -n") with one
// Dagger exec per package, so Dagger caches each package compile separately.
//
// Scope: CGO_ENABLED=0, no go:embed, go build only. This is a measurement
// prototype, not a product.

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"dagger/gcexp/internal/dagger"
)

const goImage = "golang:1.26"

// noNest runs an exec without Dagger-in-Dagger. None of these execs call
// Dagger, and a latency-optimized per-package build would not start a nested
// session for them.
var noNest = dagger.ContainerWithExecOpts{DisableDaggerInDagger: true}

func goBase(salt string) *dagger.Container {
	return dag.Container().From(goImage).
		WithEnvVariable("SALT", salt).
		WithEnvVariable("CGO_ENABLED", "0").
		WithEnvVariable("GOTOOLCHAIN", "local").
		WithEnvVariable("GOFLAGS", "-mod=mod").
		WithEnvVariable("GOMODCACHE", "/gomod")
}

// modCache downloads the module graph. Its key is go.mod and go.sum only.
func modCache(src *dagger.Directory) *dagger.Directory {
	return goBase("").
		WithMountedDirectory("/src", src.Filter(dagger.DirectoryFilterOpts{Include: []string{"go.mod", "go.sum"}})).
		WithWorkdir("/src").
		WithExec([]string{"go", "mod", "download"}, noNest).
		Directory("/gomod")
}

type block struct {
	id         string // bNNN in the plan
	importPath string
	name       string // stable directory name under /work
	lines      []string
	deps       []string // other block ids
	srcFiles   []string // relative to /src
	modFiles   []string // relative to /gomod
	isMain     bool
}

var (
	reMkdir   = regexp.MustCompile(`^mkdir -p \$WORK/(b\d+)/`)
	reWorkRef = regexp.MustCompile(`\$WORK/(b\d+)`)
	// A /src or /gomod path starts a token, so /usr/local/go/src/... does not match.
	rePath    = regexp.MustCompile(`(?:^|[\s"'=])((?:/src|/gomod)(?:/[^\s"';=]*)?)`)
	reRelFile = regexp.MustCompile(`(^|\s)\./([^\s"';]+)`)
	reBuildID = regexp.MustCompile(`\s-buildid[ =]\S+`)
	reConc    = regexp.MustCompile(`\s-c=\d+`)
	reDepArchive = regexp.MustCompile(`\$WORK/(b\d+)/_pkg_\.a`)
)

func stableName(importPath string) string {
	sum := sha256.Sum256([]byte(importPath))
	return "p" + hex.EncodeToString(sum[:6])
}

func parsePlan(plan string) (map[string]*block, error) {
	blocks := map[string]*block{}
	var cur *block
	cwd := "/src"
	lines := strings.Split(plan, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if m := reMkdir.FindStringSubmatch(line); m != nil {
			b, ok := blocks[m[1]]
			if !ok {
				b = &block{id: m[1]}
				blocks[m[1]] = b
			}
			cur = b
			// The go command prints "cd" only when the directory changes, so
			// carry the current directory into each block explicitly.
			cur.lines = append(cur.lines, "mkdir -p "+cwd+" && cd "+cwd)
		}
		if cur == nil {
			continue
		}
		// Package header: "#", "# importpath", "#".
		if line == "#" && i+2 < len(lines) && strings.HasPrefix(lines[i+1], "# ") && lines[i+2] == "#" && cur.importPath == "" {
			cur.importPath = strings.TrimPrefix(lines[i+1], "# ")
		}
		switch {
		case strings.HasPrefix(line, "go tool buildid -w"),
			strings.HasPrefix(line, "mkdir -p /out"),
			strings.HasPrefix(line, "mv $WORK/"),
			strings.HasPrefix(line, "rm -rf $WORK/"):
			continue
		}
		if strings.HasPrefix(line, "cd ") {
			d := strings.TrimPrefix(line, "cd ")
			if d == "." {
				d = "/src"
			}
			cwd = d
		}
		// The go command writes embedcfg JSON without a trailing newline.
		if strings.HasSuffix(line, "}EOF") {
			cur.lines = append(cur.lines, strings.TrimSuffix(line, "EOF"), "EOF")
			continue
		}
		cur.lines = append(cur.lines, line)
		for _, m := range rePath.FindAllStringSubmatch(line, -1) {
			addSource(cur, m[1])
		}
		// Relative source paths appear on tool command lines and, for long
		// file lists, one per line in a heredoc arguments file.
		for _, m := range reRelFile.FindAllStringSubmatch(line, -1) {
			addSource(cur, path.Join(cwd, m[2]))
		}
	}
	for id, b := range blocks {
		if b.importPath == "" {
			return nil, fmt.Errorf("block %s has no package header", id)
		}
		b.name = stableName(b.importPath)
		b.isMain = id == "b001"
		seen := map[string]bool{}
		for _, l := range b.lines {
			for _, m := range reWorkRef.FindAllStringSubmatch(l, -1) {
				if m[1] != id && !seen[m[1]] {
					seen[m[1]] = true
					b.deps = append(b.deps, m[1])
				}
			}
		}
		sort.Strings(b.deps)
	}
	return blocks, nil
}

func addSource(b *block, p string) {
	ext := path.Ext(p)
	switch ext {
	case ".go", ".s", ".h", ".syso":
	default:
		return
	}
	if rel, ok := strings.CutPrefix(p, "/src/"); ok {
		b.srcFiles = appendUniq(b.srcFiles, rel)
	} else if rel, ok := strings.CutPrefix(p, "/gomod/"); ok {
		b.modFiles = appendUniq(b.modFiles, rel)
	}
}

func appendUniq(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// script renders a block as a standalone shell script with stable paths.
// Dependency archives live in one flat directory, /work/lib, except those of
// blocks in tier, which come from the tier directory mounted at /work/tier.
func (b *block) script(blocks map[string]*block, tier map[string]bool) string {
	// The script runs as few processes as possible: one mkdir for every
	// directory it needs, heredocs written with the printf builtin instead of
	// cat, and the package archive compiled straight to its final name.
	dirs := []string{"/src", "/work/" + b.name}
	var body strings.Builder
	var heredoc []string
	heredocTarget := ""
	for _, l := range b.lines {
		l = reBuildID.ReplaceAllString(l, "")
		l = reConc.ReplaceAllString(l, " -c=4")
		// The go command packs archives in-process; "go tool pack" would
		// build the pack tool from source in every fresh container.
		l = strings.Replace(l, "go tool pack ", "/usr/local/bin/gopack ", 1)
		l = reDepArchive.ReplaceAllStringFunc(l, func(s string) string {
			id := reDepArchive.FindStringSubmatch(s)[1]
			if id == b.id {
				if b.isMain {
					return s
				}
				return "/work/" + b.name + "/" + b.name + ".a"
			}
			if tier[id] {
				return "/work/tier/" + blocks[id].name + ".a"
			}
			return "/work/lib/" + blocks[id].name + ".a"
		})
		l = reWorkRef.ReplaceAllStringFunc(l, func(s string) string {
			return "/work/" + blocks[strings.TrimPrefix(s, "$WORK/")].name
		})
		if heredocTarget != "" {
			if l == "EOF" {
				body.WriteString("printf '%s\\n'")
				for _, h := range heredoc {
					body.WriteString(" " + shQuote(h))
				}
				body.WriteString(" > " + heredocTarget + "\n")
				heredoc, heredocTarget = nil, ""
				continue
			}
			heredoc = append(heredoc, l)
			continue
		}
		if m := reHeredoc.FindStringSubmatch(l); m != nil {
			heredocTarget = m[1]
			continue
		}
		if m := reMkdirLine.FindStringSubmatch(l); m != nil {
			dirs = append(dirs, m[1])
			if m[2] != "" {
				body.WriteString(m[2] + "\n")
			}
			continue
		}
		body.WriteString(l + "\n")
	}
	return "set -e\nmkdir -p " + strings.Join(dirs, " ") + "\n" + body.String()
}

var (
	// cat >TARGET << 'EOF' [# internal]
	reHeredoc = regexp.MustCompile(`^cat >(\S+) << 'EOF'( # internal)?$`)
	// mkdir -p DIR [&& REST]
	reMkdirLine = regexp.MustCompile(`^mkdir -p (\S+)(?: && (.*))?$`)
)

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type result struct {
	file     *dagger.File
	stamp    string
	err      error // set before done closes; dependents read it
	stampErr error // set after done closes
	done     chan struct{}

	ready, started, execDone, finished time.Duration
}

// packTool builds the pack tool that package execs with assembly use. The go
// command packs archives in-process; per-package execs need a binary, and
// "go tool pack" would build it from source in every fresh container.
func packTool() *dagger.File {
	return goBase("").WithExec([]string{"go", "build", "-o", "/gopack", "cmd/pack"}, noNest).File("/gopack")
}

// PackTool builds the pack tool and the header tool and reports their sizes.
// They are built once per engine and cached; harnesses call it before
// measuring so a measured first build excludes them.
func (m *Gcexp) PackTool(ctx context.Context) (string, error) {
	size, err := packTool().Size(ctx)
	if err != nil {
		return "", err
	}
	hdrSize, err := hdrTool().Size(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pack tool: %d bytes, header tool: %d bytes", size, hdrSize), nil
}

// g23 prototype modes, comma-separated in Replay's mode argument:
//
//	memo    the standard library and dependency packages (the "tier": every
//	        package outside the workspace whose dependencies are also
//	        outside it) are built by one cached call, Tier, that walks them
//	        per package inside. Unchanged, the whole tier is one cache hit.
//	coarse  like memo, but Tier builds the tier in one exec, running the
//	        plan's own per-package scripts under make -j.
//	lazy    declare every package lazily and evaluate only the final binary;
//	        no per-package sync, so no early cutoff between packages.
//	hdr     plan from a header pack of the source (each .go file up to its
//	        imports plus go:embed lines) instead of the whole source, so a
//	        body-only edit makes the plan a cache hit.
//	prio    give free exec slots to the ready package with the longest chain
//	        of dependents above it first, instead of in arrival order.
//	stdonly build only the plan's standard-library packages, per package,
//	        and stop: a later build with the same salt then starts with a warm
//	        standard library, as if another build with this toolchain had run.
type modeOpts struct{ memo, coarse, lazy, hdr, prio, stdonly bool }

func parseMode(s string) (modeOpts, error) {
	var o modeOpts
	for _, n := range strings.Split(s, ",") {
		switch strings.TrimSpace(n) {
		case "":
		case "memo":
			o.memo = true
		case "coarse":
			o.coarse = true
		case "lazy":
			o.lazy = true
		case "hdr":
			o.hdr = true
		case "prio":
			o.prio = true
		case "stdonly":
			o.stdonly = true
		default:
			return o, fmt.Errorf("unknown mode %q", n)
		}
	}
	if o.memo && o.coarse {
		return o, fmt.Errorf("memo and coarse are exclusive")
	}
	return o, nil
}

const planCmd = "go build -n -trimpath -buildvcs=false -o /out/bin . 2> /plan.txt"

// fullPlan plans from the whole source: any edit re-runs it.
func fullPlan(ctx context.Context, src, mods *dagger.Directory, salt string) (string, error) {
	return goBase(salt).
		WithMountedDirectory("/gomod", mods).
		WithMountedDirectory("/src", src).
		WithWorkdir("/src").
		WithEnvVariable("GOCACHE", "/tmp/emptycache").
		WithExec([]string{"sh", "-c", planCmd}, noNest).
		File("/plan.txt").Contents(ctx)
}

// headerPlan plans from a header pack of the source. The pack exec re-runs on
// every edit, but its output is read through an evaluated directory, so it is
// content-addressed: a body-only edit leaves it byte-identical and the plan
// exec is a cache hit. Build IDs in the plan then come from the headers, not
// the real files; the per-package scripts drop them anyway.
func headerPlan(ctx context.Context, src, mods *dagger.Directory, salt string) (string, error) {
	hdr := hdrTool()
	packed, err := goBase("").
		WithMountedFile("/usr/local/bin/gohdr", hdr).
		WithMountedDirectory("/src", src).
		WithExec([]string{"gohdr", "pack", "/src", "/hdr/src.hdr"}, noNest).
		Directory("/hdr").Sync(ctx)
	if err != nil {
		return "", err
	}
	return goBase(salt).
		WithMountedFile("/usr/local/bin/gohdr", hdr).
		WithMountedDirectory("/gomod", mods).
		WithMountedFile("/src.hdr", packed.File("src.hdr")).
		WithWorkdir("/src").
		WithEnvVariable("GOCACHE", "/tmp/emptycache").
		WithExec([]string{"sh", "-c", "gohdr unpack /src.hdr /src && " + planCmd}, noNest).
		File("/plan.txt").Contents(ctx)
}

// PlanCheck compares the plan made from the whole source with the plan made
// from its header pack, ignoring build IDs. It is the hdr mode's correctness
// check: the two must render the same per-package scripts.
func (m *Gcexp) PlanCheck(ctx context.Context, src *dagger.Directory) (string, error) {
	mods, err := modCache(src).Sync(ctx)
	if err != nil {
		return "", err
	}
	full, err := fullPlan(ctx, src, mods, "")
	if err != nil {
		return "", err
	}
	hdr, err := headerPlan(ctx, src, mods, "")
	if err != nil {
		return "", err
	}
	a := strings.Split(reBuildID.ReplaceAllString(full, ""), "\n")
	b := strings.Split(reBuildID.ReplaceAllString(hdr, ""), "\n")
	if len(a) == len(b) {
		diff := 0
		first := ""
		for i := range a {
			if a[i] != b[i] {
				if diff == 0 {
					first = fmt.Sprintf("line %d:\n  full: %s\n  hdr:  %s", i+1, a[i], b[i])
				}
				diff++
			}
		}
		if diff == 0 {
			return fmt.Sprintf("same plan: %d lines", len(a)), nil
		}
		return fmt.Sprintf("plans differ on %d of %d lines; first %s", diff, len(a), first), nil
	}
	return fmt.Sprintf("plans differ in length: full %d lines, hdr %d lines", len(a), len(b)), nil
}

// node is one package's exec, with its script already rendered.
type node struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	ImportPath string   `json:"importPath"`
	Deps       []string `json:"deps"`
	SrcFiles   []string `json:"srcFiles,omitempty"`
	ModFiles   []string `json:"modFiles,omitempty"`
	IsMain     bool     `json:"isMain,omitempty"`
	Script     string   `json:"script"`
	tierDeps   bool
}

func (n *node) output() string {
	if n.IsMain {
		return "exe/a.out"
	}
	return n.Name + ".a"
}

type walker struct {
	base        *dagger.Container
	packTool    *dagger.File
	src, mods   *dagger.Directory
	tierDir     *dagger.Directory
	concurrency int
	lazy        bool
	prio        bool
	stamps      bool
	start       time.Time
}

// slots bounds concurrent execs. With priorities, a freed slot goes to the
// waiting package with the highest priority; otherwise to whoever asks first.
type slots struct {
	mu      sync.Mutex
	free    int
	seq     int
	waiters waitHeap
}

type waiter struct {
	prio, seq int
	ch        chan struct{}
}

type waitHeap []waiter

func (h waitHeap) Len() int { return len(h) }
func (h waitHeap) Less(i, j int) bool {
	if h[i].prio != h[j].prio {
		return h[i].prio > h[j].prio
	}
	return h[i].seq < h[j].seq
}
func (h waitHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *waitHeap) Push(x any)   { *h = append(*h, x.(waiter)) }
func (h *waitHeap) Pop() any {
	old := *h
	w := old[len(old)-1]
	*h = old[:len(old)-1]
	return w
}

func (s *slots) acquire(prio int) {
	s.mu.Lock()
	if s.free > 0 && len(s.waiters) == 0 {
		s.free--
		s.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	s.seq++
	heap.Push(&s.waiters, waiter{prio, s.seq, ch})
	s.mu.Unlock()
	<-ch
}

func (s *slots) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.waiters) > 0 {
		close(heap.Pop(&s.waiters).(waiter).ch)
		return
	}
	s.free++
}

// heights is, per node, the number of packages on the longest chain from it
// up to a package nothing in nodes depends on: how much waits on it.
func heights(nodes map[string]*node) map[string]int {
	dependents := map[string][]string{}
	for id, n := range nodes {
		for _, d := range n.Deps {
			dependents[d] = append(dependents[d], id)
		}
	}
	h := map[string]int{}
	var height func(id string) int
	height = func(id string) int {
		if v, ok := h[id]; ok {
			return v
		}
		v := 1
		for _, up := range dependents[id] {
			if u := 1 + height(up); u > v {
				v = u
			}
		}
		h[id] = v
		return v
	}
	for id := range nodes {
		height(id)
	}
	return h
}

// walk runs one exec per node, each after its dependencies inside nodes.
// Dependencies outside nodes are tier packages, read from the tier directory.
func (w *walker) walk(ctx context.Context, nodes map[string]*node) (map[string]*result, error) {
	results := map[string]*result{}
	for id := range nodes {
		results[id] = &result{done: make(chan struct{})}
	}
	sem := &slots{free: w.concurrency}
	prio := map[string]int{}
	if w.prio {
		prio = heights(nodes)
	}
	var wg sync.WaitGroup
	for id, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := results[id]
			doneOnce := sync.OnceFunc(func() { close(r.done) })
			defer doneOnce()
			ctr := w.base.WithMountedFile("/usr/local/bin/gopack", w.packTool)
			var depFiles []*dagger.File
			for _, d := range n.Deps {
				dr, ok := results[d]
				if !ok {
					continue
				}
				<-dr.done
				if dr.err != nil {
					r.err = fmt.Errorf("dep %s: %w", nodes[d].ImportPath, dr.err)
					return
				}
				depFiles = append(depFiles, dr.file)
			}
			if len(depFiles) > 0 {
				ctr = ctr.WithMountedDirectory("/work/lib", dag.Directory().WithFiles(".", depFiles))
			}
			if n.tierDeps {
				ctr = ctr.WithMountedDirectory("/work/tier", w.tierDir, dagger.ContainerWithMountedDirectoryOpts{ReadOnly: true})
			}
			r.ready = time.Since(w.start)
			defer func() { r.finished = time.Since(w.start) }()
			if len(n.SrcFiles) > 0 {
				ctr = ctr.WithMountedDirectory("/src", w.src.Filter(dagger.DirectoryFilterOpts{Include: n.SrcFiles}))
			}
			if len(n.ModFiles) > 0 {
				ctr = ctr.WithMountedDirectory("/gomod", w.mods.Filter(dagger.DirectoryFilterOpts{Include: n.ModFiles}))
			}
			script := n.Script
			if w.stamps && !w.lazy {
				// Shell builtins only: a random id and the script's own run time.
				script = "read t0 _ < /proc/uptime\n" + script +
					"read u < /proc/sys/kernel/random/uuid\nv=${u#*-}\nread t1 _ < /proc/uptime\n" +
					"t0=${t0%.*}${t0#*.}\nt1=${t1%.*}${t1#*.}\n" +
					"echo \"${u%%-*}${v%%-*} $(( (t1-t0)*10 ))ms\" > /stamp\n"
			}
			ran := ctr.WithExec([]string{"sh", "-c", script}, noNest)
			if w.lazy {
				// Declared only: the engine evaluates it when the binary is read.
				r.file = ran.Directory("/work/" + n.Name).File(n.output())
				r.started, r.execDone = r.ready, r.ready
				return
			}
			sem.acquire(prio[id])
			defer sem.release()
			r.started = time.Since(w.start)
			out, err := ran.Directory("/work/" + n.Name).Sync(ctx)
			if err != nil {
				r.err = fmt.Errorf("%s: %w", n.ImportPath, err)
				return
			}
			r.execDone = time.Since(w.start)
			r.file = out.File(n.output())
			if _, err := r.file.Sync(ctx); err != nil {
				r.err = fmt.Errorf("%s: output: %w", n.ImportPath, err)
				return
			}
			// Dependents only need the output; release them before the
			// stamp read.
			doneOnce()
			if w.stamps {
				r.stamp, r.stampErr = ran.File("/stamp").Contents(ctx)
			}
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil {
			return results, r.err
		}
	}
	return results, nil
}

// tierSet is every block outside the workspace whose dependencies are all
// outside it too: the standard library and dependency modules. A block is in
// the workspace when it reads /src.
func tierSet(blocks map[string]*block) map[string]bool {
	ws := map[string]bool{}
	var inWS func(id string) bool
	memo := map[string]bool{}
	inWS = func(id string) bool {
		if v, ok := memo[id]; ok {
			return v
		}
		memo[id] = false
		b := blocks[id]
		v := len(b.srcFiles) > 0 || b.isMain
		for _, d := range b.deps {
			if inWS(d) {
				v = true
			}
		}
		memo[id] = v
		return v
	}
	tier := map[string]bool{}
	for id := range blocks {
		if inWS(id) {
			ws[id] = true
		} else {
			tier[id] = true
		}
	}
	return tier
}

// stdSet is the plan's standard-library packages: blocks that read neither
// the workspace nor the module cache, and whose dependencies are the same.
func stdSet(blocks map[string]*block) map[string]bool {
	memo := map[string]bool{}
	var isStd func(id string) bool
	isStd = func(id string) bool {
		if v, ok := memo[id]; ok {
			return v
		}
		memo[id] = false
		b := blocks[id]
		v := len(b.srcFiles) == 0 && len(b.modFiles) == 0 && !b.isMain
		for _, d := range b.deps {
			if !isStd(d) {
				v = false
			}
		}
		memo[id] = v
		return v
	}
	std := map[string]bool{}
	for id := range blocks {
		if isStd(id) {
			std[id] = true
		}
	}
	return std
}

func toNode(b *block, blocks map[string]*block, tier map[string]bool) *node {
	n := &node{ID: b.id, Name: b.name, ImportPath: b.importPath, Deps: b.deps, IsMain: b.isMain,
		SrcFiles: append([]string(nil), b.srcFiles...), ModFiles: append([]string(nil), b.modFiles...),
		Script: b.script(blocks, tier)}
	sort.Strings(n.SrcFiles)
	sort.Strings(n.ModFiles)
	for _, d := range b.deps {
		if tier[d] {
			n.tierDeps = true
		}
	}
	return n
}

// tierSpec is the tier's nodes as stable JSON, keyed by package name rather
// than plan position, so the same tier gives the same Tier call.
func tierSpec(blocks map[string]*block, tier map[string]bool) (string, error) {
	var ns []*node
	for id := range tier {
		n := toNode(blocks[id], blocks, nil)
		// Dependencies by name, not by plan position.
		deps := make([]string, len(n.Deps))
		for i, d := range n.Deps {
			deps[i] = blocks[d].name
		}
		sort.Strings(deps)
		n.ID, n.Deps = n.Name, deps
		ns = append(ns, n)
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].Name < ns[j].Name })
	b, err := json.Marshal(ns)
	return string(b), err
}

// Tier builds the packages in spec (the standard library and dependency
// modules of a build) and returns a directory with one archive per package,
// named <name>.a. It is a module function, so the whole tier is one cached
// call: unchanged, a build pays one cache hit for it instead of walking it.
func (m *Gcexp) Tier(ctx context.Context, spec string, mods *dagger.Directory, salt string,
	// +default=16
	concurrency int,
	// Build the tier in one exec under make -j instead of one exec per package.
	// +default=false
	coarse bool,
) (*dagger.Directory, error) {
	var ns []*node
	if err := json.Unmarshal([]byte(spec), &ns); err != nil {
		return nil, err
	}
	base, err := goBase(salt).Sync(ctx)
	if err != nil {
		return nil, err
	}
	if coarse {
		return coarseTier(ctx, base, mods, ns)
	}
	nodes := map[string]*node{}
	for _, n := range ns {
		nodes[n.ID] = n
	}
	w := &walker{base: base, packTool: packTool(), mods: mods, concurrency: concurrency, start: time.Now()}
	results, err := w.walk(ctx, nodes)
	if err != nil {
		return nil, err
	}
	var files []*dagger.File
	for _, n := range ns {
		files = append(files, results[n.ID].file)
	}
	return dag.Directory().WithFiles(".", files).Sync(ctx)
}

// coarseTier runs every tier package's own script in one exec, ordered and
// parallelised by make, and returns the archives.
func coarseTier(ctx context.Context, base *dagger.Container, mods *dagger.Directory, ns []*node) (*dagger.Directory, error) {
	var mk strings.Builder
	mk.WriteString("SHELL := /bin/sh\n.SHELLFLAGS := -ec\n.ONESHELL:\nall:")
	for _, n := range ns {
		mk.WriteString(" " + n.Name)
	}
	mk.WriteString("\n")
	for _, n := range ns {
		mk.WriteString(n.Name + ":")
		for _, d := range n.Deps {
			mk.WriteString(" " + d)
		}
		mk.WriteString("\n")
		for _, l := range strings.Split(strings.TrimRight(n.Script, "\n"), "\n") {
			mk.WriteString("\t" + strings.ReplaceAll(l, "$", "$$") + "\n")
		}
		mk.WriteString("\tln -f /work/" + n.Name + "/" + n.Name + ".a /work/lib/" + n.Name + ".a\n")
	}
	return base.
		WithMountedFile("/usr/local/bin/gopack", packTool()).
		WithMountedDirectory("/gomod", mods, dagger.ContainerWithMountedDirectoryOpts{ReadOnly: true}).
		WithNewFile("/tier.mk", mk.String()).
		WithExec([]string{"sh", "-c", "mkdir -p /work/lib && make -s -f /tier.mk -j$(nproc) all"}, noNest).
		Directory("/work/lib").Sync(ctx)
}

// Replay builds the main package at the root of src with one exec per package.
// It returns the wall time and, per package, a stamp that changes only when
// that package's exec actually ran.
func (m *Gcexp) Replay(ctx context.Context, src *dagger.Directory, nonce string, salt string,
	// +default=32
	concurrency int,
	// Write and read a per-package stamp that changes only when that
	// package's exec ran. Off, the replay does only what a real build does;
	// verify re-runs from exec counts instead.
	// +default=true
	stamps bool,
	// g23 prototype modes, comma-separated: memo, coarse, lazy, hdr (see
	// modeOpts). Empty runs driver v1 unchanged.
	// +optional
	mode string,
) (string, error) {
	start := time.Now()
	opts, err := parseMode(mode)
	if err != nil {
		return "", err
	}
	mods, err := modCache(src).Sync(ctx)
	if err != nil {
		return "", err
	}
	var plan string
	if opts.hdr {
		plan, err = headerPlan(ctx, src, mods, salt)
	} else {
		plan, err = fullPlan(ctx, src, mods, salt)
	}
	if err != nil {
		return "", err
	}
	planDur := time.Since(start)
	blocks, err := parsePlan(plan)
	if err != nil {
		return "", err
	}
	// Build the base container once: each package then starts from its ID
	// instead of re-sending the from/withEnvVariable chain. The pack tool is
	// mounted per package, so syncing the base never waits for its build.
	base, err := goBase(salt).Sync(ctx)
	if err != nil {
		return "", err
	}
	w := &walker{base: base, packTool: packTool(), src: src, mods: mods, concurrency: concurrency,
		lazy: opts.lazy, prio: opts.prio, stamps: stamps, start: start}
	var tier map[string]bool
	var tierDur time.Duration
	if opts.memo || opts.coarse {
		tier = tierSet(blocks)
		spec, err := tierSpec(blocks, tier)
		if err != nil {
			return "", err
		}
		w.tierDir, err = dag.Gcexp().Tier(spec, mods, salt, dagger.GcexpTierOpts{Concurrency: concurrency, Coarse: opts.coarse}).Sync(ctx)
		if err != nil {
			return "", fmt.Errorf("tier: %w", err)
		}
		tierDur = time.Since(start)
	}
	nodes := map[string]*node{}
	for id, b := range blocks {
		if !tier[id] {
			nodes[id] = toNode(b, blocks, tier)
		}
	}
	if opts.stdonly {
		std := stdSet(blocks)
		for id := range nodes {
			if !std[id] {
				delete(nodes, id)
			}
		}
		if _, err := w.walk(ctx, nodes); err != nil {
			return "", err
		}
		return fmt.Sprintf("stdonly: %d of %d packages, total %s", len(nodes), len(blocks), time.Since(start).Round(time.Millisecond)), nil
	}
	results, err := w.walk(ctx, nodes)
	if err != nil {
		return "", err
	}
	var lines []string
	var timing []string
	var sumExec, sumPost, sumQueue time.Duration
	var sumInside int
	for id, r := range results {
		if r.stampErr != nil {
			return "", fmt.Errorf("%s: stamp: %w", blocks[id].importPath, r.stampErr)
		}
		if stamps && !opts.lazy {
			lines = append(lines, fmt.Sprintf("%s %s", r.stamp, blocks[id].importPath))
		}
		sumQueue += r.started - r.ready
		if f := strings.Fields(r.stamp); len(f) == 2 {
			var ms int
			fmt.Sscanf(f[1], "%dms", &ms)
			sumInside += ms
		}
		sumExec += r.execDone - r.started
		sumPost += r.finished - r.execDone
		timing = append(timing, fmt.Sprintf("%8s ready=%-8s exec=%-8s post=%-8s deps=%-3d %s",
			(r.execDone - r.started).Round(time.Millisecond), r.ready.Round(time.Millisecond),
			(r.execDone - r.started).Round(time.Millisecond), (r.finished - r.execDone).Round(time.Millisecond),
			len(blocks[id].deps), blocks[id].importPath))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(timing)))
	if len(timing) > 12 {
		timing = timing[:12]
	}
	header := fmt.Sprintf("TIMING sumQueue=%s sumExec=%s sumPost=%s sumInsideContainer=%dms stamps=%t mode=%q tier=%d tierDone=%s",
		sumQueue.Round(time.Millisecond), sumExec.Round(time.Millisecond), sumPost.Round(time.Millisecond), sumInside, stamps,
		mode, len(tier), tierDur.Round(time.Millisecond))
	lines = append(timing, lines...)
	sort.Slice(lines, func(i, j int) bool { return strings.Fields(lines[i])[1] < strings.Fields(lines[j])[1] })
	size, err := results["b001"].file.Size(ctx)
	if err != nil {
		return "", err
	}
	version, err := goBase("").WithMountedFile("/bin/built", results["b001"].file).
		WithExec([]string{"/bin/built", "--version"}, noNest).Stdout(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("replay: %d packages, plan %s, total %s, binary %d bytes, runs: %s\n%s\n%s",
		len(blocks), planDur.Round(time.Millisecond), time.Since(start).Round(time.Millisecond), size, strings.TrimSpace(version), header,
		strings.Join(lines, "\n")), nil
}

// Plain builds the same package with one go build exec. With a non-empty
// volume name, GOCACHE lives in that cache volume.
func (m *Gcexp) Plain(ctx context.Context, src *dagger.Directory, nonce string, volume string, salt string) (string, error) {
	start := time.Now()
	mods, err := modCache(src).Sync(ctx)
	if err != nil {
		return "", err
	}
	ctr := goBase(salt).
		WithMountedDirectory("/gomod", mods).
		WithMountedDirectory("/src", src).
		WithWorkdir("/src").
		WithEnvVariable("NONCE", nonce)
	if volume != "" {
		ctr = ctr.WithMountedCache("/gocache", dag.CacheVolume(volume)).WithEnvVariable("GOCACHE", "/gocache")
	} else {
		ctr = ctr.WithEnvVariable("GOCACHE", "/tmp/gocache")
	}
	out, err := ctr.WithExec([]string{"sh", "-c", "go build -trimpath -buildvcs=false -o /out/bin . && go version"}, noNest).File("/out/bin").Size(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("plain (volume=%q): total %s, binary %d bytes", volume, time.Since(start).Round(time.Millisecond), out), nil
}

// PlainStd builds the standard library into a GOCACHE volume, as a previous
// build with the same toolchain would have, so a later Plain with that volume
// starts with a warm standard library.
func (m *Gcexp) PlainStd(ctx context.Context, volume string, salt string) (string, error) {
	start := time.Now()
	_, err := goBase(salt).
		WithMountedCache("/gocache", dag.CacheVolume(volume)).WithEnvVariable("GOCACHE", "/gocache").
		WithExec([]string{"go", "build", "-trimpath", "-buildvcs=false", "std"}, noNest).Sync(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("plain std (volume=%q): total %s", volume, time.Since(start).Round(time.Millisecond)), nil
}

// PlanDebug shows how one package's block was parsed.
func (m *Gcexp) PlanDebug(ctx context.Context, src *dagger.Directory, importPath string) (string, error) {
	mods, err := modCache(src).Sync(ctx)
	if err != nil {
		return "", err
	}
	plan, err := goBase("").
		WithMountedDirectory("/gomod", mods).
		WithMountedDirectory("/src", src).
		WithWorkdir("/src").
		WithEnvVariable("GOCACHE", "/tmp/emptycache").
		WithExec([]string{"sh", "-c", "go build -n -trimpath -buildvcs=false -o /out/bin . 2> /plan.txt"}, noNest).
		File("/plan.txt").Contents(ctx)
	if err != nil {
		return "", err
	}
	blocks, err := parsePlan(plan)
	if err != nil {
		return "", err
	}
	for _, b := range blocks {
		if b.importPath == importPath {
			return fmt.Sprintf("srcFiles=%v\nmodFiles=%v\ndeps=%d\n%s", b.srcFiles, b.modFiles, len(b.deps), b.script(blocks, nil)), nil
		}
	}
	return "not found", nil
}

// Layered builds in three steps with no engine changes:
//  1. list the non-main-module packages the build needs (reruns on any source change, cheap);
//  2. build those packages into a GOCACHE directory output, keyed on that list's
//     content plus go.mod/go.sum (a normal cached result, not a cache volume);
//  3. go build with GOCACHE seeded from step 2 (copy-on-write mount).
func (m *Gcexp) Layered(ctx context.Context, src *dagger.Directory, nonce string, salt string) (string, error) {
	start := time.Now()
	mods, err := modCache(src).Sync(ctx)
	if err != nil {
		return "", err
	}
	env := func(c *dagger.Container) *dagger.Container {
		return c.WithMountedDirectory("/gomod", mods).WithWorkdir("/src").WithEnvVariable("GOCACHE", "/gocache")
	}
	listDir, err := env(goBase(salt)).
		WithMountedDirectory("/src", src).
		WithExec([]string{"sh", "-c", "mkdir -p /list && go list -deps -f '{{if or (not .Module) (not .Module.Main)}}{{.ImportPath}}{{end}}' . | sort > /list/deps.txt"}, noNest).
		Directory("/list").Sync(ctx)
	if err != nil {
		return "", err
	}
	depsList := listDir.File("deps.txt") // content-digested: evaluated parent
	t1 := time.Since(start)
	depsCache, err := env(goBase(salt)).
		WithMountedDirectory("/src", src.Filter(dagger.DirectoryFilterOpts{Include: []string{"go.mod", "go.sum"}})).
		WithMountedFile("/deps.txt", depsList).
		WithExec([]string{"sh", "-c", "go build -trimpath -buildvcs=false $(cat /deps.txt)"}, noNest).
		Directory("/gocache").Sync(ctx)
	if err != nil {
		return "", err
	}
	t2 := time.Since(start)
	size, err := env(goBase(salt)).
		WithMountedDirectory("/src", src).
		WithMountedDirectory("/gocache", depsCache).
		WithEnvVariable("NONCE", nonce).
		WithExec([]string{"go", "build", "-trimpath", "-buildvcs=false", "-o", "/out/bin", "."}, noNest).
		File("/out/bin").Size(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("layered: list %s, deps cache %s, total %s, binary %d bytes",
		t1.Round(time.Millisecond), (t2 - t1).Round(time.Millisecond), time.Since(start).Round(time.Millisecond), size), nil
}

// PlanScripts lists, per package in src's plan, its import path and a digest
// of everything its exec depends on apart from its dependencies' outputs: the
// rendered script and its source file lists. Two projects whose lines match
// for a package build it with the same exec, given the same dependency
// outputs and source contents.
func (m *Gcexp) PlanScripts(ctx context.Context, src *dagger.Directory) (string, error) {
	mods, err := modCache(src).Sync(ctx)
	if err != nil {
		return "", err
	}
	plan, err := fullPlan(ctx, src, mods, "")
	if err != nil {
		return "", err
	}
	blocks, err := parsePlan(plan)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, b := range blocks {
		n := toNode(b, blocks, nil)
		sum := sha256.Sum256([]byte(n.Script + "\x00" + strings.Join(n.SrcFiles, ",") + "\x00" + strings.Join(n.ModFiles, ",")))
		kind := "ws"
		switch {
		case len(b.srcFiles) == 0 && len(b.modFiles) == 0:
			kind = "std"
		case len(b.srcFiles) == 0:
			kind = "dep"
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", b.importPath, kind, hex.EncodeToString(sum[:8])))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}
