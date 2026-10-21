// A private helper of the go module: it builds a main package with one exec
// per package, so Dagger caches each package's compile separately and an
// edit re-runs only what it reaches.
//
// It replays the go command's own plan, "go build -n", made from a header
// pack of the source, so an edit inside function bodies doesn't re-plan.
// Packages outside the source (the standard library and dependencies) are
// built as one tier, in one exec, so an unchanged dependency graph is one
// cache hit and a cold one costs one container. Workspace packages are
// built one exec each, and a package whose archive comes out unchanged
// leaves its dependents cached.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"dagger/gobuild/internal/dagger"
)

type Gobuild struct{}

// toolImage builds gohdr, which runs in whatever image the build uses.
const toolImage = "golang:1.26-alpine"

// noNest runs an exec without Dagger-in-Dagger: none of these execs call
// Dagger, and starting a nested session for each would cost every package.
var noNest = dagger.ContainerWithExecOpts{DisableDaggerInDagger: true}

// Binary builds the main package pkg as "go build" would and returns the
// binary, named output, without a Go build ID. A build it can't replay per
// package yet (cgo, PGO, flags such as -race or -toolexec) is an error.
func (m *Gobuild) Binary(
	ctx context.Context,
	// The Go container to build in: its image supplies the toolchain, and
	// its environment (GOOS, GOARCH, GOFLAGS, GOPROXY...) applies. Per
	// package, its mounts, such as Go cache volumes, are left out.
	base *dagger.Container,
	// The image base was made from. Set, package execs run in that image
	// with base's environment alone, so their keys don't carry how base was
	// made (a workdir at the project's mount path, its cache mounts), and
	// projects with the same image and settings share them.
	// +optional
	image string,
	// The source tree.
	source *dagger.Directory,
	// The header pack of source's Go files and go.mod files, from the go
	// module's scan: what the plan is made from, with source's other files.
	headers *dagger.File,
	// Where source is mounted. Paths in the binary's debug information
	// name it, unless flags hold -trimpath.
	mountPath string,
	// The directory to run go build in, relative to mountPath.
	// +default="."
	workdir string,
	// The go build package argument, e.g. "." or "./cmd/foo".
	pkg string,
	// The binary's file name.
	output string,
	// Flags for go build, before the package.
	// +optional
	flags []string,
	// Which packages separately cached calls build: none, outside (the
	// standard library and dependency modules, as one tier) or split (the
	// standard library, then dependency modules).
	// +default="outside"
	tiers string,
	// How many package execs run at once.
	// +default=16
	concurrency int,
	// Only fetch what precedes the package builds, the module downloads and
	// the helper tools, and return an empty file. For benchmarks, whose
	// first build counts from a warm download like today's build has.
	// +default=false
	prepare bool,
	// Print a per-package timeline to stderr ("TL" lines). For diagnosis:
	// it adds requests, so a timed build leaves it off.
	// +default=false
	timeline bool,
	// Set, it goes into every package exec's environment and the plan's,
	// so they run again instead of hitting the cache: a cold build, with
	// downloads and helper tools still cached.
	// +optional
	salt string,
) (*dagger.File, error) {
	cwd := path.Join(mountPath, workdir)
	if err := checkFlags(flags); err != nil {
		return nil, err
	}
	b, err := newBuild(ctx, base, image, source, mountPath, cwd)
	if err != nil {
		return nil, err
	}
	if prepare {
		if _, err := hdrTool().Sync(ctx); err != nil {
			return nil, err
		}
		if _, err := b.tools.Sync(ctx); err != nil {
			return nil, err
		}
		return dag.Directory().WithNewFile(output, "").File(output), nil
	}
	if salt != "" {
		if b.base, err = b.base.WithEnvVariable("GOBUILD_SALT", salt).Sync(ctx); err != nil {
			return nil, err
		}
	}
	plan, err := b.plan(ctx, headers, flags, pkg)
	if err != nil {
		return nil, err
	}
	blocks, err := parsePlan(plan, b.roots)
	if err != nil {
		return nil, err
	}
	parts, err := partition(tiers, blocks)
	if err != nil {
		return nil, err
	}
	std, outsideWS := stdSet(blocks), outsideSet(blocks)
	for id, bl := range blocks {
		switch {
		case std[id]:
			bl.group = "std"
		case outsideWS[id]:
			bl.group = "dep"
		default:
			bl.group = "ws"
		}
	}
	if err := expandHeaders(ctx, b.mods, blocks); err != nil {
		return nil, err
	}
	// Go 1.25 and later run "go tool pack" for packages with assembly, the
	// runtime among the first: build the tool now, not when the runtime
	// waits for it. An error shows again where it's used.
	if needsTools(blocks) {
		go b.tools.Sync(ctx)
	}
	w := &walker{base: b.base, tools: b.tools, src: source, mods: b.mods, roots: b.roots, concurrency: concurrency, timeline: timeline}
	outside := map[string]int{}
	for k, part := range parts {
		var lower *dagger.Directory
		if k > 0 {
			lower = w.tiers[k-1]
		}
		dir, err := b.tier(ctx, blocks, part, outside, lower, timeline)
		if err != nil {
			return nil, fmt.Errorf("tier %d: %w", k, err)
		}
		w.tiers = append(w.tiers, dir)
		for id := range part {
			outside[id] = k
		}
	}
	nodes := map[string]*node{}
	for id, bl := range blocks {
		if _, ok := outside[id]; !ok {
			nodes[id] = toNode(bl, blocks, outside)
		}
	}
	results, err := w.walk(ctx, nodes)
	if err != nil {
		return nil, err
	}
	return results[mainID].file.WithName(output), nil
}

// tier builds a tier's packages in one exec: gohdr runs their scripts in
// dependency order, as many at once as the machine has CPUs, the way go build
// runs its own actions, so the tier costs one container, not one per package.
// It returns the tier's archives, each named <name>.a, along with those of
// lower, the tier below, which it builds on.
func (b *build) tier(ctx context.Context, blocks map[string]*block, part map[string]bool, outside map[string]int, lower *dagger.Directory, timeline bool) (*dagger.Directory, error) {
	type runNode struct {
		Name, Pkg, Group, Output, Script string
		Deps                             []string
	}
	spec := struct {
		Lib   string
		Nodes []runNode
	}{Lib: "/work/lib"}
	mods, tools := false, false
	for id := range part {
		n := toNode(blocks[id], blocks, outside)
		spec.Nodes = append(spec.Nodes, runNode{Name: n.Name, Pkg: n.ImportPath, Group: n.Group,
			Output: n.Output, Script: n.Script, Deps: n.DepNames})
		mods = mods || len(n.ModFiles) > 0
		tools = tools || n.Tools
	}
	// Sorted, so the same tier is the same exec.
	sort.Slice(spec.Nodes, func(i, j int) bool { return spec.Nodes[i].Name < spec.Nodes[j].Name })
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	ctr := b.base.WithMountedFile("/gobuild/gohdr", hdrTool())
	if mods {
		ctr = ctr.WithMountedDirectory(b.roots.Mod, b.mods)
	}
	if tools {
		ctr = ctr.WithMountedDirectory(toolsDir, b.tools, dagger.ContainerWithMountedDirectoryOpts{ReadOnly: true})
	}
	if lower != nil {
		ctr = ctr.WithMountedDirectory("/work/lib", lower)
	}
	if timeline {
		ctr = ctr.WithEnvVariable("TL", "1")
	}
	ran := ctr.WithNewFile("/gobuild/tier.json", string(data)).
		WithExec([]string{"/gobuild/gohdr", "run", "/gobuild/tier.json"}, noNest)
	dir, err := ran.Directory("/work/lib").Sync(ctx)
	if err != nil {
		return nil, err
	}
	if timeline {
		out, err := ran.Stdout(ctx)
		if err != nil {
			return nil, err
		}
		fmt.Fprint(os.Stderr, out)
	}
	return dir, nil
}

// build is what a build's execs share.
type build struct {
	// base is the caller's container without its mounts, so no cache volume
	// shadows the module sources mounted per package.
	base  *dagger.Container
	src   *dagger.Directory
	mods  *dagger.Directory
	tools *dagger.Directory
	roots roots
}

func newBuild(ctx context.Context, ctr *dagger.Container, image string, source *dagger.Directory, mountPath, cwd string) (*build, error) {
	mounts, err := ctr.Mounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range mounts {
		ctr = ctr.WithoutMount(m)
	}
	if image != "" {
		if ctr, err = canonical(ctx, ctr, image); err != nil {
			return nil, err
		}
	}
	gomodcache, err := ctr.EnvVariable(ctx, "GOMODCACHE")
	if err != nil {
		return nil, err
	}
	if gomodcache == "" {
		out, err := ctr.WithExec([]string{"go", "env", "GOMODCACHE"}, noNest).Stdout(ctx)
		if err != nil {
			return nil, err
		}
		gomodcache = strings.TrimSpace(out)
	}
	// Synced once, so each package exec refers to it instead of repeating
	// how it was made.
	base, err := ctr.Sync(ctx)
	if err != nil {
		return nil, err
	}
	b := &build{base: base, src: source, roots: roots{Src: mountPath, Mod: gomodcache, Cwd: cwd}}
	// The module cache, downloaded from every go.mod and go.sum in the
	// source: the root's, and those a local replace or go.work points at.
	b.mods, err = base.
		WithMountedDirectory(mountPath, source.Filter(dagger.DirectoryFilterOpts{
			Include: []string{"**/go.mod", "**/go.sum", "go.work", "go.work.sum"},
		})).
		WithWorkdir(cwd).
		WithExec([]string{"go", "mod", "download"}, noNest).
		Directory(gomodcache).Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("go mod download: %w", err)
	}
	// Built for the machine the execs run on, by the build's own toolchain.
	b.tools = base.
		WithoutEnvVariable("GOOS").
		WithoutEnvVariable("GOARCH").
		WithEnvVariable("GOFLAGS", "").
		WithEnvVariable("GOCACHE", "/gobuild/cache").
		WithExec([]string{"go", "build", "-o", toolsDir + "/", "cmd/pack"}, noNest).
		Directory(toolsDir)
	return b, nil
}

// canonical is image with ctr's environment and nothing else.
func canonical(ctx context.Context, ctr *dagger.Container, image string) (*dagger.Container, error) {
	vars, err := ctr.EnvVariables(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(vars))
	values := make([]string, len(vars))
	errs := make([]error, len(vars))
	var wg sync.WaitGroup
	for i, v := range vars {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if names[i], errs[i] = v.Name(ctx); errs[i] == nil {
				values[i], errs[i] = v.Value(ctx)
			}
		}()
	}
	wg.Wait()
	out := dag.Container().From(image)
	for i := range vars {
		if errs[i] != nil {
			return nil, errs[i]
		}
		out = out.WithEnvVariable(names[i], values[i])
	}
	return out, nil
}

// plan is go build's plan for pkg, made from headers, the header pack of the
// source's Go files, and the source's other files whole. Both are keyed by
// content, so an edit inside function bodies leaves the plan exec a cache hit.
func (b *build) plan(ctx context.Context, headers *dagger.File, flags []string, pkg string) (string, error) {
	cmd := []string{"go", "build", "-n"}
	cmd = append(cmd, flags...)
	cmd = append(cmd, "-o", planOut+"/out", pkg)
	quoted := make([]string, len(cmd))
	for i, c := range cmd {
		quoted[i] = shQuote(c)
	}
	return b.base.
		WithMountedFile("/gobuild/gohdr", hdrTool()).
		WithMountedDirectory(b.roots.Mod, b.mods).
		WithMountedDirectory(b.roots.Src, b.src.Filter(dagger.DirectoryFilterOpts{Exclude: []string{"**/*.go"}})).
		WithMountedFile("/gobuild/src.hdr", headers).
		// An empty build cache, so the plan holds every step.
		WithEnvVariable("GOCACHE", "/gobuild/plancache").
		WithExec([]string{"sh", "-c", "/gobuild/gohdr unpack /gobuild/src.hdr " + shQuote(b.roots.Src) +
			" && cd " + shQuote(b.roots.Cwd) + " && " + strings.Join(quoted, " ") + " 2> /gobuild/plan.txt"}, noNest).
		File("/gobuild/plan.txt").Contents(ctx)
}

func needsTools(blocks map[string]*block) bool {
	for _, b := range blocks {
		for _, l := range b.lines {
			if strings.Contains(l, "go tool pack ") {
				return true
			}
		}
	}
	return false
}

// checkFlags rejects go build flags whose plan this build can't replay:
// those that run other tools, change what is linked, or print instead of
// building.
func checkFlags(flags []string) error {
	for _, f := range flags {
		name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(f, "-"), "-"), "=")
		switch name {
		case "a", "n", "x", "work", "race", "msan", "asan", "cover", "covermode", "coverpkg",
			"toolexec", "linkshared", "buildmode", "pgo", "overlay", "modfile", "compiler", "gccgoflags":
			return fmt.Errorf("%w: flag %s", errUnsupported, f)
		}
	}
	return nil
}
