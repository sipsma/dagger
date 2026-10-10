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
	rePath       = regexp.MustCompile(`(?:^|[\s"'=])((?:/src|/gomod)(?:/[^\s"';=]*)?)`)
	reRelFile    = regexp.MustCompile(`(^|\s)\./([^\s"';]+)`)
	reBuildID    = regexp.MustCompile(`\s-buildid[ =]\S+`)
	reConc       = regexp.MustCompile(`\s-c=\d+`)
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
// the pack tool at packPath and dependency archives in libDir. With
// roInputs, /src and /gomod are read-only mounts holding only this
// package's files, so a working directory carried over from the previous
// block is entered only if it exists there, never created.
func (b *block) script(blocks map[string]*block, packPath, libDir string, roInputs bool) string {
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
			if roInputs && m[2] == "cd "+m[1] && (strings.HasPrefix(m[1], "/src/") || strings.HasPrefix(m[1], "/gomod/")) {
				body.WriteString("if [ -d " + m[1] + " ]; then cd " + m[1] + "; fi\n")
				continue
			}
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

// samePlan reports whether two "go build -n" plans differ only in build IDs,
// which the per-package scripts drop anyway.
func samePlan(a, b string) bool {
	return reBuildID.ReplaceAllString(a, "") == reBuildID.ReplaceAllString(b, "")
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
	// package uses it; "noinit" runs package execs without the injected
	// init; "spec" starts the walk from a plan keyed on the source headers
	// while the real plan is verified; "specbad" (test only) makes that
	// plan wrong. Empty runs the first prototype unchanged.
	// +optional
	knobs string,
) (string, error) {
	start := time.Now()
	k := map[string]bool{}
	for _, n := range strings.Split(knobs, ",") {
		switch n = strings.TrimSpace(n); n {
		case "":
		case "ro", "pack", "noinit", "spec", "specbad":
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
	if k["spec"] {
		go syncBase()
	}
	mods, err := modCache(src).Sync(ctx)
	if err != nil {
		return "", err
	}
	const planCmd = "go build -n -trimpath -buildvcs=false -o /out/bin . 2> /plan.txt"
	type planResult struct {
		text string
		err  error
	}
	realPlan := make(chan planResult, 1)
	go func() {
		text, err := goBase(salt).
			WithMountedDirectory("/gomod", mods).
			WithMountedDirectory("/src", src).
			WithWorkdir("/src").
			WithEnvVariable("GOCACHE", "/tmp/emptycache").
			WithExec([]string{"sh", "-c", planCmd}, noNest).
			File("/plan.txt").Contents(ctx)
		realPlan <- planResult{text, err}
	}()
	// With "spec", a second plan is made from the source headers only. Its
	// input does not change when a function body does, so after such an
	// edit it is a cache hit and the walk starts without waiting for the
	// real plan. The real plan still decides: see below.
	var specPlan chan planResult
	if k["spec"] {
		specPlan = make(chan planResult, 1)
		go func() {
			hdr := hdrTool()
			packCmd := "pack"
			if k["specbad"] {
				packCmd = "pack-bad"
			}
			packed, err := goBase("").
				WithMountedFile("/usr/local/bin/gohdr", hdr).
				WithMountedDirectory("/src", src).
				WithExec([]string{"gohdr", packCmd, "/src", "/hdr/src.hdr"}, noNest).
				Directory("/hdr").Sync(ctx)
			if err != nil {
				specPlan <- planResult{"", err}
				return
			}
			// Content-addressed: an unchanged pack is the same file.
			packFile, err := packed.File("src.hdr").Sync(ctx)
			if err != nil {
				specPlan <- planResult{"", err}
				return
			}
			text, err := goBase(salt).
				WithMountedFile("/usr/local/bin/gohdr", hdr).
				WithMountedDirectory("/gomod", mods).
				WithMountedFile("/src.hdr", packFile).
				WithWorkdir("/src").
				WithEnvVariable("GOCACHE", "/tmp/emptycache").
				WithExec([]string{"sh", "-c", "gohdr unpack /src.hdr /src && " + planCmd}, noNest).
				File("/plan.txt").Contents(ctx)
			specPlan <- planResult{text, err}
		}()
	} else {
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
	walk := func(ctx context.Context, blocks map[string]*block) (map[string]*result, error) {
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
				script := b.script(blocks, packPath, libDir, k["ro"])
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
				if len(depFiles) > 0 {
					ctr = ctr.WithMountedDirectory("/work/lib", dag.Directory().WithFiles(".", depFiles), mountOpts)
				}
				r.ready = time.Since(start)
				defer func() { r.finished = time.Since(start) }()
				if len(b.srcFiles) > 0 {
					sort.Strings(b.srcFiles)
					ctr = ctr.WithMountedDirectory("/src", src.Filter(dagger.DirectoryFilterOpts{Include: b.srcFiles}), mountOpts)
				}
				if len(b.modFiles) > 0 {
					sort.Strings(b.modFiles)
					ctr = ctr.WithMountedDirectory("/gomod", mods.Filter(dagger.DirectoryFilterOpts{Include: b.modFiles}), mountOpts)
				}
				sem <- struct{}{}
				defer func() { <-sem }()
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
		for _, r := range results {
			if r.err != nil {
				return results, r.err
			}
		}
		return results, nil
	}

	// Choose the plan the walk starts from. Without "spec" it is the real
	// plan. With it, a speculative plan that arrives first starts the walk;
	// when the real plan arrives, the two must match apart from build IDs,
	// or the speculative walk is cancelled and redone from the real plan.
	var blocks map[string]*block
	var results map[string]*result
	var planDur time.Duration
	specState := "off"
	walkReal := func(real planResult) error {
		if real.err != nil {
			return real.err
		}
		var err error
		if blocks, err = parsePlan(real.text); err != nil {
			return err
		}
		results, err = walk(ctx, blocks)
		return err
	}
	if specPlan == nil {
		real := <-realPlan
		planDur = time.Since(start)
		if err := walkReal(real); err != nil {
			return "", err
		}
	} else {
		var real, spec planResult
		gotReal := false
		if k["specbad"] {
			spec = <-specPlan
		} else {
			select {
			case real = <-realPlan:
				gotReal = true
			case spec = <-specPlan:
			}
		}
		planDur = time.Since(start)
		var specBlocks map[string]*block
		if !gotReal && spec.err == nil {
			specBlocks, spec.err = parsePlan(spec.text)
		}
		if gotReal || spec.err != nil {
			// No speculative plan in time. Walk from the real one, and let
			// the speculative plan finish so it is cached for the next run.
			specState = "late"
			if !gotReal {
				specState = "failed"
				real = <-realPlan
				planDur = time.Since(start)
			}
			if err := walkReal(real); err != nil {
				return "", err
			}
			if gotReal {
				<-specPlan
			}
		} else {
			type walkResult struct {
				results map[string]*result
				err     error
			}
			specCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			specWalk := make(chan walkResult, 1)
			go func() {
				r, err := walk(specCtx, specBlocks)
				specWalk <- walkResult{r, err}
			}()
			var done *walkResult
			if k["specbad"] {
				// Test only: let the wrong walk run to its end first.
				w := <-specWalk
				done = &w
			}
			real = <-realPlan
			if real.err != nil {
				return "", real.err
			}
			if samePlan(spec.text, real.text) {
				specState = "hit"
				if done == nil {
					w := <-specWalk
					done = &w
				}
				if done.err != nil {
					return "", done.err
				}
				blocks, results = specBlocks, done.results
			} else {
				specState = "mismatch"
				cancel()
				if done == nil {
					<-specWalk
				}
				if err := walkReal(real); err != nil {
					return "", err
				}
			}
		}
	}
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
			(r.execDone-r.started).Round(time.Millisecond), r.ready.Round(time.Millisecond),
			(r.execDone-r.started).Round(time.Millisecond), (r.finished-r.execDone).Round(time.Millisecond),
			len(blocks[id].deps), blocks[id].importPath))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(timing)))
	if len(timing) > 12 {
		timing = timing[:12]
	}
	header := fmt.Sprintf("TIMING sumQueue=%s sumExec=%s sumPost=%s sumInsideContainer=%dms spec=%s stamps=%t knobs=%q", sumQueue.Round(time.Millisecond), sumExec.Round(time.Millisecond), sumPost.Round(time.Millisecond), sumInside, specState, stamps, knobs)
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
			return fmt.Sprintf("srcFiles=%v\nmodFiles=%v\ndeps=%d\n%s", b.srcFiles, b.modFiles, len(b.deps), b.script(blocks, "/usr/local/bin/gopack", "/work/lib", false)), nil
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
