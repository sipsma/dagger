package main

// Prototype: replay the go command's own build plan ("go build -n") with one
// Dagger exec per package, so Dagger caches each package compile separately.
//
// Scope: CGO_ENABLED=0, no go:embed, go build only. This is a measurement
// prototype, not a product.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// script renders a block as a standalone shell script with stable paths:
// the pack tool at packPath and dependency archives in libDir.
func (b *block) script(blocks map[string]*block, packPath, libDir string) string {
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
		l = strings.Replace(l, "go tool pack ", packPath+" ", 1)
		// Dependency archives live in one flat directory, libDir.
		l = reDepArchive.ReplaceAllStringFunc(l, func(s string) string {
			id := reDepArchive.FindStringSubmatch(s)[1]
			if id == b.id {
				if b.isMain {
					return s
				}
				return "/work/" + b.name + "/" + b.name + ".a"
			}
			return libDir + "/" + blocks[id].name + ".a"
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

// PackTool builds the pack tool and reports its size. It is built once per
// engine and cached; harnesses call it before measuring so a measured first
// build excludes it.
func (m *Gcexp) PackTool(ctx context.Context) (string, error) {
	size, err := packTool().Size(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pack tool: %d bytes", size), nil
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
	// Comma-separated driver options to measure (prototype): "ro" mounts each
	// package's inputs read-only; "pack" mounts the pack tool only where the
	// package uses it; "slot" frees the concurrency slot when the exec is
	// done; "overlap" builds the base container while planning; "redirect"
	// runs the plan exec without a shell; "noinit" runs package execs
	// without the injected init. Empty runs the first prototype unchanged.
	// "merge" puts dependency archives inside the package's source mount.
	// +optional
	knobs string,
) (string, error) {
	start := time.Now()
	k := map[string]bool{}
	for _, n := range strings.Split(knobs, ",") {
		switch n = strings.TrimSpace(n); n {
		case "":
		case "ro", "pack", "slot", "overlap", "redirect", "noinit", "merge":
			k[n] = true
		default:
			return "", fmt.Errorf("unknown knob %q", n)
		}
	}
	packTool := packTool()
	// Build the base container once: each package then starts from its ID
	// instead of re-sending the from/withEnvVariable chain. The pack tool is
	// mounted per package, so syncing the base never waits for its build.
	var base *dagger.Container
	baseErr := make(chan error, 1)
	syncBase := func() {
		var err error
		base, err = goBase(salt).Sync(ctx)
		baseErr <- err
	}
	if k["overlap"] {
		go syncBase()
	}
	mods, err := modCache(src).Sync(ctx)
	if err != nil {
		return "", err
	}
	planCtr := goBase(salt).
		WithMountedDirectory("/gomod", mods).
		WithMountedDirectory("/src", src).
		WithWorkdir("/src").
		WithEnvVariable("GOCACHE", "/tmp/emptycache")
	if k["redirect"] {
		planCtr = planCtr.WithExec([]string{"go", "build", "-n", "-trimpath", "-buildvcs=false", "-o", "/out/bin", "."},
			dagger.ContainerWithExecOpts{DisableDaggerInDagger: true, RedirectStderr: "/plan.txt"})
	} else {
		planCtr = planCtr.WithExec([]string{"sh", "-c", "go build -n -trimpath -buildvcs=false -o /out/bin . 2> /plan.txt"}, noNest)
	}
	plan, err := planCtr.File("/plan.txt").Contents(ctx)
	if err != nil {
		return "", err
	}
	planDur := time.Since(start)
	blocks, err := parsePlan(plan)
	if err != nil {
		return "", err
	}
	if !k["overlap"] {
		syncBase()
	}
	if err := <-baseErr; err != nil {
		return "", err
	}
	execOpts := noNest
	execOpts.NoInit = k["noinit"]
	mountOpts := dagger.ContainerWithMountedDirectoryOpts{ReadOnly: k["ro"]}
	// withMountedFile has no read-only option, so with "ro" the pack tool
	// comes in a read-only directory mount instead.
	packDir := dag.Directory().WithFile("gopack", packTool)
	results := map[string]*result{}
	for id := range blocks {
		results[id] = &result{done: make(chan struct{})}
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for id, b := range blocks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := results[id]
			doneOnce := sync.OnceFunc(func() { close(r.done) })
			defer doneOnce()
			packPath, libDir := "/usr/local/bin/gopack", "/work/lib"
			if k["ro"] {
				packPath = "/opt/gopack/gopack"
			}
			// With "merge", a package whose sources come from one mount gets
			// its dependency archives inside that mount: one input mount
			// instead of two.
			mergeInto := ""
			if k["merge"] && len(b.deps) > 0 {
				switch {
				case len(b.srcFiles) > 0 && len(b.modFiles) == 0:
					mergeInto = "/src"
				case len(b.modFiles) > 0 && len(b.srcFiles) == 0:
					mergeInto = "/gomod"
				}
				if mergeInto != "" {
					libDir = mergeInto + "/_gcexp_lib"
				}
			}
			script := b.script(blocks, packPath, libDir)
			ctr := base
			if !k["pack"] || strings.Contains(script, packPath) {
				if k["ro"] {
					ctr = ctr.WithMountedDirectory("/opt/gopack", packDir, mountOpts)
				} else {
					ctr = ctr.WithMountedFile("/usr/local/bin/gopack", packTool)
				}
			}
			var depFiles []*dagger.File
			for _, d := range b.deps {
				dr := results[d]
				<-dr.done
				if dr.err != nil {
					r.err = fmt.Errorf("dep %s: %w", blocks[d].importPath, dr.err)
					return
				}
				depFiles = append(depFiles, dr.file)
			}
			if len(depFiles) > 0 && mergeInto == "" {
				ctr = ctr.WithMountedDirectory("/work/lib", dag.Directory().WithFiles(".", depFiles), mountOpts)
			}
			r.ready = time.Since(start)
			defer func() { r.finished = time.Since(start) }()
			if len(b.srcFiles) > 0 {
				sort.Strings(b.srcFiles)
				in := src.Filter(dagger.DirectoryFilterOpts{Include: b.srcFiles})
				if mergeInto == "/src" {
					in = in.WithFiles("_gcexp_lib", depFiles)
				}
				ctr = ctr.WithMountedDirectory("/src", in, mountOpts)
			}
			if len(b.modFiles) > 0 {
				sort.Strings(b.modFiles)
				in := mods.Filter(dagger.DirectoryFilterOpts{Include: b.modFiles})
				if mergeInto == "/gomod" {
					in = in.WithFiles("_gcexp_lib", depFiles)
				}
				ctr = ctr.WithMountedDirectory("/gomod", in, mountOpts)
			}
			sem <- struct{}{}
			freeSlot := sync.OnceFunc(func() { <-sem })
			defer freeSlot()
			r.started = time.Since(start)
			if stamps {
				// Shell builtins only: a random id and the script's own run time.
				script = "read t0 _ < /proc/uptime\n" + script +
					"read u < /proc/sys/kernel/random/uuid\nv=${u#*-}\nread t1 _ < /proc/uptime\n" +
					"t0=${t0%.*}${t0#*.}\nt1=${t1%.*}${t1#*.}\n" +
					"echo \"${u%%-*}${v%%-*} $(( (t1-t0)*10 ))ms\" > /stamp\n"
			}
			ran := ctr.WithExec([]string{"sh", "-c", script}, execOpts)
			out, err := ran.Directory("/work/" + b.name).Sync(ctx)
			if err != nil {
				r.err = fmt.Errorf("%s: %w", b.importPath, err)
				return
			}
			if k["slot"] {
				freeSlot()
			}
			r.execDone = time.Since(start)
			if b.isMain {
				r.file = out.File("exe/a.out")
			} else {
				r.file = out.File(b.name + ".a")
			}
			if _, err := r.file.Sync(ctx); err != nil {
				r.err = fmt.Errorf("%s: output: %w", b.importPath, err)
				return
			}
			// Dependents only need the output; release them before the
			// stamp read.
			doneOnce()
			if stamps {
				r.stamp, r.stampErr = ran.File("/stamp").Contents(ctx)
			}
		}()
	}
	wg.Wait()
	var lines []string
	var timing []string
	var sumExec, sumPost, sumQueue time.Duration
	var sumInside int
	for id, r := range results {
		if r.err != nil {
			return "", r.err
		}
		if r.stampErr != nil {
			return "", fmt.Errorf("%s: stamp: %w", blocks[id].importPath, r.stampErr)
		}
		if stamps {
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
	header := fmt.Sprintf("TIMING sumQueue=%s sumExec=%s sumPost=%s sumInsideContainer=%dms stamps=%t knobs=%q", sumQueue.Round(time.Millisecond), sumExec.Round(time.Millisecond), sumPost.Round(time.Millisecond), sumInside, stamps, knobs)
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
			return fmt.Sprintf("srcFiles=%v\nmodFiles=%v\ndeps=%d\n%s", b.srcFiles, b.modFiles, len(b.deps), b.script(blocks, "/usr/local/bin/gopack", "/work/lib")), nil
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
