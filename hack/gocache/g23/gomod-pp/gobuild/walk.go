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
	"time"

	"dagger/gobuild/internal/dagger"
)

// toolsDir is where package execs that need it find the pack tool, which
// the go command runs in-process.
const toolsDir = "/gobuild/tools"

// node is one exec: a block rendered for the walk that runs it.
type node struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	ImportPath string   `json:"importPath"`
	Deps       []string `json:"deps"`
	SrcFiles   []string `json:"srcFiles,omitempty"`
	ModFiles   []string `json:"modFiles,omitempty"`
	// Tiers names the tiers whose output the package mounts whole.
	Tiers  []string `json:"tiers,omitempty"`
	Tools  bool     `json:"tools,omitempty"`
	Output string   `json:"output"`
	Script string   `json:"script"`
	// Group is std, dep or ws, and DepNames every dependency by name, for
	// the timeline.
	Group    string   `json:"group"`
	DepNames []string `json:"depNames"`
}

// toNode renders a block for a walk; outside holds the blocks tiers built,
// by tier. A package reads its walked dependencies from /work/lib and those a
// tier built from the tier's output, mounted whole at /work/t/<tier>: one mount,
// not a copy of each archive. A dependency bump changes the tier and so every
// package's key, which is fine: bumps are rare next to edits.
func toNode(b *block, blocks map[string]*block, outside map[string]string, conc string) *node {
	n := &node{ID: b.id, Name: b.name, ImportPath: b.importPath, Deps: b.deps,
		SrcFiles: b.srcFiles, ModFiles: b.modFiles, Output: b.output(), Group: b.group,
		Script: b.script(blocks, outside, conc)}
	seen := map[string]bool{}
	for _, d := range b.deps {
		n.DepNames = append(n.DepNames, blocks[d].name)
		if k, ok := outside[d]; ok && !seen[k] {
			seen[k] = true
			n.Tiers = append(n.Tiers, k)
		}
	}
	sort.Strings(n.Tiers)
	n.Tools = strings.Contains(n.Script, toolsDir+"/")
	return n
}

// walker runs a set of nodes, one exec each, each after its dependencies in
// the set. Dependencies outside the set come from tiers.
type walker struct {
	base  *dagger.Container
	tools *dagger.Directory
	src   *dagger.Directory
	// mods is the module cache, evaluated: see fileTree.
	mods        *dagger.Directory
	roots       roots
	tiers       map[string]*tierResult
	concurrency int
	// timeline prints, per package, when it became ready, started and
	// finished its exec, and how long its inputs took to assemble, synced
	// on their own after it took an exec slot. That adds requests and
	// changes when inputs are evaluated: for diagnosis, not timing.
	timeline bool
}

// tierResult is a tier's archives, once its exec is done.
type tierResult struct {
	dir  *dagger.Directory
	err  error // set before done closes
	done chan struct{}
}

type result struct {
	file *dagger.File
	err  error // set before done closes; dependents read it
	done chan struct{}

	ready, started, execDone, finished time.Time
	libDur, srcDur, modDur             time.Duration
}

func (w *walker) walk(ctx context.Context, nodes map[string]*node) (map[string]*result, error) {
	results := map[string]*result{}
	for id := range nodes {
		results[id] = &result{done: make(chan struct{})}
	}
	sem := make(chan struct{}, w.concurrency)
	var wg sync.WaitGroup
	for id, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := results[id]
			defer close(r.done)
			ctr := w.base
			var depFiles []*dagger.File
			for _, d := range n.Deps {
				dr, ok := results[d]
				if !ok {
					continue
				}
				<-dr.done
				if dr.err != nil {
					r.err = fmt.Errorf("%s: %w", nodes[d].ImportPath, dr.err)
					return
				}
				depFiles = append(depFiles, dr.file)
			}
			var libDir, srcDir, modDir *dagger.Directory
			if len(depFiles) > 0 {
				libDir = w.assemble(depFiles)
				ctr = ctr.WithMountedDirectory("/work/lib", libDir)
			}
			for _, k := range n.Tiers {
				t := w.tiers[k]
				<-t.done
				if t.err != nil {
					r.err = fmt.Errorf("tier %s: %w", k, t.err)
					return
				}
				ctr = ctr.WithMountedDirectory("/work/t/"+k, t.dir, dagger.ContainerWithMountedDirectoryOpts{ReadOnly: true})
			}
			if n.Tools {
				ctr = ctr.WithMountedDirectory(toolsDir, w.tools, dagger.ContainerWithMountedDirectoryOpts{ReadOnly: true})
			}
			if len(n.SrcFiles) > 0 {
				var at string
				srcDir, at = selected(w.src, n.SrcFiles)
				ctr = ctr.WithMountedDirectory(path.Join(w.roots.Src, at), srcDir)
			}
			if len(n.ModFiles) > 0 {
				modDir = fileTree(w.mods, n.ModFiles)
				ctr = ctr.WithMountedDirectory(w.roots.Mod, modDir)
			}
			r.ready = time.Now()
			defer func() { r.finished = time.Now() }()
			// A script past the kernel's argument-size limit (the link's
			// import config at a large project's scale) goes in as a file.
			var ran *dagger.Container
			if len(n.Script) > 64<<10 {
				ran = ctr.WithNewFile("/gobuild/run.sh", n.Script).WithExec([]string{"sh", "/gobuild/run.sh"}, noNest)
			} else {
				ran = ctr.WithExec([]string{"sh", "-c", n.Script}, noNest)
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			if w.timeline {
				timed := func(d *dagger.Directory) (time.Duration, error) {
					if d == nil {
						return 0, nil
					}
					t := time.Now()
					_, err := d.Sync(ctx)
					return time.Since(t), err
				}
				var err error
				if r.libDur, err = timed(libDir); err == nil {
					if r.srcDur, err = timed(srcDir); err == nil {
						r.modDur, err = timed(modDir)
					}
				}
				if err != nil {
					r.err = fmt.Errorf("%s: inputs: %w", n.ImportPath, err)
					return
				}
			}
			r.started = time.Now()
			// Evaluate the output directory first, then read the file from
			// it: only a file read from an evaluated directory is keyed by its
			// content, so a package whose archive came out unchanged leaves
			// its dependents' execs cached.
			out, err := ran.Directory("/work/" + n.Name).Sync(ctx)
			if err != nil {
				r.err = err
				return
			}
			r.execDone = time.Now()
			r.file, r.err = out.File(n.Output).Sync(ctx)
		}()
	}
	wg.Wait()
	if w.timeline {
		printTimeline(nodes, results)
	}
	var errs []string
	for _, n := range nodes {
		if r := results[n.ID]; r.err != nil {
			errs = append(errs, r.err.Error())
		}
	}
	if len(errs) > 0 {
		// Report each failure once: the package that failed, not every
		// dependent that waited on it.
		sort.Strings(errs)
		return nil, fmt.Errorf("%s", shortest(errs))
	}
	return results, nil
}

// printTimeline writes one "TL <json>" line per package to stderr, with
// times in Unix milliseconds, so walks in separate calls line up.
func printTimeline(nodes map[string]*node, results map[string]*result) {
	type line struct {
		Pkg      string   `json:"pkg"`
		Name     string   `json:"name"`
		Group    string   `json:"group"`
		Deps     []string `json:"deps"`
		Ready    int64    `json:"ready"`
		Started  int64    `json:"started"`
		ExecDone int64    `json:"execDone"`
		Finished int64    `json:"finished"`
		LibMs    int64    `json:"libMs"`
		SrcMs    int64    `json:"srcMs"`
		ModMs    int64    `json:"modMs"`
	}
	for id, n := range nodes {
		r := results[id]
		pkg := n.ImportPath
		if n.Output != n.Name+".a" {
			pkg += " (link)"
		}
		j, err := json.Marshal(line{pkg, n.Name, n.Group, n.DepNames, r.ready.UnixMilli(), r.started.UnixMilli(),
			r.execDone.UnixMilli(), r.finished.UnixMilli(), r.libDur.Milliseconds(), r.srcDur.Milliseconds(), r.modDur.Milliseconds()})
		if err == nil {
			fmt.Fprintln(os.Stderr, "TL "+string(j))
		}
	}
}

// shortest is the failure at the bottom of a chain: a dependent's error
// wraps its dependency's, so the shortest message is the original.
func shortest(errs []string) string {
	s := errs[0]
	for _, e := range errs[1:] {
		if len(e) < len(s) {
			s = e
		}
	}
	return s
}

// assembleMax is how many separate files one withFiles directory holds. Before
// Dagger v1.0.0-beta.17, withFiles chained one snapshot per file, and past
// roughly 400 the overlay mount's options grew too long for the kernel.
const assembleMax = 300

// assemble returns a directory holding files: one withFiles, or, past
// assembleMax, chunks that one exec copies into a single directory.
func (w *walker) assemble(files []*dagger.File) *dagger.Directory {
	if len(files) <= assembleMax {
		return dag.Directory().WithFiles(".", files)
	}
	ctr := w.base
	var cmd strings.Builder
	cmd.WriteString("set -e; mkdir -p /gobuild/lib")
	for i := 0; i*assembleMax < len(files); i++ {
		end := min((i+1)*assembleMax, len(files))
		dir := fmt.Sprintf("/gobuild/in/%d", i)
		ctr = ctr.WithMountedDirectory(dir, dag.Directory().WithFiles(".", files[i*assembleMax:end]),
			dagger.ContainerWithMountedDirectoryOpts{ReadOnly: true})
		cmd.WriteString("; cp -a " + dir + "/. /gobuild/lib/")
	}
	return ctr.WithExec([]string{"sh", "-c", cmd.String()}, noNest).Directory("/gobuild/lib")
}

// selected is the part of dir that files name, and where under the root to
// mount it: their deepest common directory, filtered to them. A package's
// files share its own directory as a rule, so selecting costs per package,
// not per tree. A pattern such as "**/*.h" selects from the whole tree.
func selected(dir *dagger.Directory, files []string) (*dagger.Directory, string) {
	at := commonDir(files)
	if at == "" {
		return dir.Filter(dagger.DirectoryFilterOpts{Include: files}), ""
	}
	rel := make([]string, len(files))
	for i, f := range files {
		rel[i] = strings.TrimPrefix(f, at+"/")
	}
	return dir.Directory(at).Filter(dagger.DirectoryFilterOpts{Include: rel}), at
}

// fileTree is a directory holding files from dir, the module cache, at the
// same paths. Each file is read on its own from the evaluated cache, so it
// is keyed by its content, and so is the tree. A selection made from the
// cache directory itself would be keyed by the cache's recipe, which a bump
// of any dependency changes, re-running every dependency package.
func fileTree(dir *dagger.Directory, files []string) *dagger.Directory {
	byDir := map[string][]*dagger.File{}
	for _, f := range files {
		byDir[path.Dir(f)] = append(byDir[path.Dir(f)], dir.File(f))
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	tree := dag.Directory()
	for _, d := range dirs {
		tree = tree.WithFiles(d, byDir[d])
	}
	return tree
}

// expandHeaders replaces each module header pattern ("<module>/**/*.h", for
// assembly that may include any header in its module) with the headers it
// matches, so every module file a package reads is named.
func expandHeaders(ctx context.Context, mods *dagger.Directory, blocks map[string]*block) error {
	found := map[string][]string{}
	for _, b := range blocks {
		for _, f := range b.modFiles {
			if strings.HasSuffix(f, "/**/*.h") {
				found[f] = nil
			}
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error
	for pattern := range found {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mod := strings.TrimSuffix(pattern, "/**/*.h")
			matches, err := mods.Directory(mod).Glob(ctx, "**/*.h")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("module %s: %w", mod, err))
				return
			}
			for _, m := range matches {
				found[pattern] = append(found[pattern], mod+"/"+m)
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return errs[0]
	}
	for _, b := range blocks {
		var files []string
		for _, f := range b.modFiles {
			if hs, ok := found[f]; ok {
				for _, h := range hs {
					files = appendUniq(files, h)
				}
			} else {
				files = appendUniq(files, f)
			}
		}
		sort.Strings(files)
		b.modFiles = files
	}
	return nil
}

func commonDir(files []string) string {
	var common []string
	for i, f := range files {
		parts := strings.Split(f, "/")
		parts = parts[:len(parts)-1]
		for j, p := range parts {
			if strings.ContainsAny(p, "*?[") {
				parts = parts[:j]
				break
			}
		}
		if i == 0 {
			common = parts
			continue
		}
		n := 0
		for n < len(common) && n < len(parts) && common[n] == parts[n] {
			n++
		}
		common = common[:n]
	}
	return strings.Join(common, "/")
}

// partition splits a plan's packages into tiers, each built by one exec,
// and names them; the names are stable across builds, as they appear in the
// scripts that read a tier. A tier reads the tiers its packages depend on, and
// starts once they are done; a workspace package starts once the tiers it
// imports are, so nothing waits for unrelated work. Which split is fastest is
// for measuring, so it is chosen by name and kept in this one place:
//
//	none     no tiers: one exec per package.
//	outside  one tier: every package outside the source whose dependencies
//	         are outside too, i.e. the standard library and dependency modules.
//	split    two tiers: the standard library, then the dependency modules.
//	modules  the standard library, then one tier per dependency module (or per
//	         group of modules that import each other), so a workspace package
//	         waits only for the modules it imports.
//	heavy    outside less its heavy tail, then the tail: see heavyTiers.
//
// weights are the plan's step weights, from gohdr weigh.
func partition(strategy string, blocks map[string]*block, weights map[string]int64) ([]map[string]bool, []string, error) {
	switch strategy {
	case "none":
		return nil, nil, nil
	case "outside":
		return nonEmpty([]string{"deps"}, outsideSet(blocks))
	case "split":
		std, outside := stdSet(blocks), outsideSet(blocks)
		for id := range std {
			delete(outside, id)
		}
		return nonEmpty([]string{"std", "deps"}, std, outside)
	case "modules":
		return moduleTiers(blocks)
	case "heavy":
		return heavyTiers(blocks, weights)
	}
	return nil, nil, fmt.Errorf("unknown tier strategy %q: want none, outside, split, modules or heavy", strategy)
}

// heavyTiers is the heavy split. A tier's archives come out together when
// its last package is done, so one outside tier holds every workspace
// package back until the end of its heaviest chain of dependencies, which
// often ends in a few big packages, such as a C library translated to Go.
// The heavy tier is that tail: the biggest package on the heaviest chain,
// and every outside package whose chain is longer than what precedes it.
// The light tier is the rest, which the heavy one builds on. A workspace
// package that imports nothing heavy starts when the light tier is done,
// and builds while the tail does.
//
// It splits only a tail: the heavy tier at most a quarter of the outside
// packages' weight, and its first package at least a quarter of the chain.
// Otherwise, as when the heaviest chain is mostly the standard library, it
// is the outside tier. Only the outside packages' weights decide it, so a
// workspace edit never moves a package between tiers.
func heavyTiers(blocks map[string]*block, weights map[string]int64) ([]map[string]bool, []string, error) {
	outside := outsideSet(blocks)
	chain := map[string]int64{}
	var chainOf func(id string) int64
	chainOf = func(id string) int64 {
		if v, ok := chain[id]; ok {
			return v
		}
		var below int64
		for _, d := range blocks[id].deps {
			below = max(below, chainOf(d))
		}
		chain[id] = weights[id] + below
		return chain[id]
	}
	// Ties go by import path, which, unlike a step id, an edit elsewhere in
	// the plan doesn't renumber.
	longer := func(a, b string) bool {
		if b == "" || chain[a] != chain[b] {
			return b == "" || chain[a] > chain[b]
		}
		return blocks[a].importPath < blocks[b].importPath
	}
	var total int64
	head := ""
	for id := range outside {
		chainOf(id)
		total += weights[id]
		if longer(id, head) {
			head = id
		}
	}
	big := head
	for id := head; id != ""; {
		if weights[id] > weights[big] {
			big = id
		}
		next := ""
		for _, d := range blocks[id].deps {
			if longer(d, next) {
				next = d
			}
		}
		id = next
	}
	start := chain[big] - weights[big]
	light, heavy := map[string]bool{}, map[string]bool{}
	var heavyWeight int64
	for id := range outside {
		if chain[id] <= start {
			light[id] = true
		} else {
			heavy[id] = true
			heavyWeight += weights[id]
		}
	}
	if 4*heavyWeight > total || 4*weights[big] < chain[head] {
		return nonEmpty([]string{"deps"}, outside)
	}
	return nonEmpty([]string{"light", "heavy"}, light, heavy)
}

// heightsOf is each outside package's priority in its tier exec: its weight
// plus the heaviest chain of outside packages that import it, across tiers,
// so a tier starts first what a later tier's long chain waits for. Only the
// outside packages' weights go in, so a workspace edit leaves a tier's spec,
// and its exec, as they were.
func heightsOf(blocks map[string]*block, outside map[string]bool, weights map[string]int64) map[string]int64 {
	dependents := map[string][]string{}
	for id := range outside {
		for _, d := range blocks[id].deps {
			dependents[d] = append(dependents[d], id)
		}
	}
	heights := map[string]int64{}
	var heightOf func(id string) int64
	heightOf = func(id string) int64 {
		if v, ok := heights[id]; ok {
			return v
		}
		var above int64
		for _, up := range dependents[id] {
			above = max(above, heightOf(up))
		}
		heights[id] = weights[id] + above
		return heights[id]
	}
	for id := range outside {
		heightOf(id)
	}
	return heights
}

// printTiers writes one "TIER" line per tier to stderr, for the timeline:
// its name, package count and weight, and for a tier of 30 packages or
// fewer, the packages.
func printTiers(blocks map[string]*block, parts []map[string]bool, names []string, weights map[string]int64) {
	for k, part := range parts {
		var w int64
		var pkgs []string
		for id := range part {
			w += weights[id]
			pkgs = append(pkgs, blocks[id].importPath)
		}
		sort.Strings(pkgs)
		line := fmt.Sprintf("TIER %s %d packages %d bytes", names[k], len(part), w)
		if len(pkgs) <= 30 {
			line += ": " + strings.Join(pkgs, " ")
		}
		fmt.Fprintln(os.Stderr, line)
	}
}

// moduleTiers is the modules split: the standard library, and the dependency
// packages grouped by module, merging modules whose packages import each
// other into one tier, so the tiers form a graph without cycles.
func moduleTiers(blocks map[string]*block) ([]map[string]bool, []string, error) {
	std, outside := stdSet(blocks), outsideSet(blocks)
	groupOf := map[string]string{}
	for id := range outside {
		g := "std"
		if !std[id] {
			g = "other"
			if b := blocks[id]; len(b.modFiles) > 0 {
				g = moduleRoot(b.modFiles[0])
			}
		}
		groupOf[id] = g
	}
	edges := map[string]map[string]bool{}
	for id, g := range groupOf {
		for _, d := range blocks[id].deps {
			if h, ok := groupOf[d]; ok && h != g {
				if edges[g] == nil {
					edges[g] = map[string]bool{}
				}
				edges[g][h] = true
			}
		}
	}
	var groups []string
	seen := map[string]bool{}
	for _, g := range groupOf {
		if !seen[g] {
			seen[g] = true
			groups = append(groups, g)
		}
	}
	sort.Strings(groups)
	merged := sccs(groups, edges)
	var tiers []map[string]bool
	var names []string
	for _, scc := range merged {
		members := map[string]bool{}
		for _, g := range scc {
			members[g] = true
		}
		part := map[string]bool{}
		for id, g := range groupOf {
			if members[g] {
				part[id] = true
			}
		}
		name := "std"
		if len(scc) > 1 || scc[0] != "std" {
			name = "m" + stableName(strings.Join(scc, " "))[1:]
		}
		tiers = append(tiers, part)
		names = append(names, name)
	}
	return tiers, names, nil
}

// moduleRoot is the module directory a module cache path is in:
// "example.com/m@v1.2.3" for "example.com/m@v1.2.3/sub/x.go".
func moduleRoot(f string) string {
	at := strings.Index(f, "@")
	if at < 0 {
		return f
	}
	if slash := strings.Index(f[at:], "/"); slash >= 0 {
		return f[:at+slash]
	}
	return f
}

// sccs is the strongly connected components of a graph, by Tarjan's
// algorithm, each sorted.
func sccs(nodes []string, edges map[string]map[string]bool) [][]string {
	index, low := map[string]int{}, map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var out [][]string
	next := 0
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		var ws []string
		for w := range edges[v] {
			ws = append(ws, w)
		}
		sort.Strings(ws)
		for _, w := range ws {
			if _, ok := index[w]; !ok {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var scc []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			sort.Strings(scc)
			out = append(out, scc)
		}
	}
	for _, v := range nodes {
		if _, ok := index[v]; !ok {
			visit(v)
		}
	}
	return out
}

func nonEmpty(names []string, sets ...map[string]bool) ([]map[string]bool, []string, error) {
	var out []map[string]bool
	var outNames []string
	for i, set := range sets {
		if len(set) > 0 {
			out = append(out, set)
			outNames = append(outNames, names[i])
		}
	}
	return out, outNames, nil
}

// closedSet is every block that has property and whose dependencies all
// have it too.
func closedSet(blocks map[string]*block, property func(*block) bool) map[string]bool {
	memo := map[string]bool{}
	var in func(id string) bool
	in = func(id string) bool {
		if v, ok := memo[id]; ok {
			return v
		}
		b := blocks[id]
		v := property(b)
		for _, d := range b.deps {
			if !in(d) {
				v = false
			}
		}
		memo[id] = v
		return v
	}
	set := map[string]bool{}
	for id := range blocks {
		if in(id) {
			set[id] = true
		}
	}
	return set
}

// outsideSet is the packages that read nothing from the source, down to
// their dependencies.
func outsideSet(blocks map[string]*block) map[string]bool {
	return closedSet(blocks, func(b *block) bool { return len(b.srcFiles) == 0 && !b.isLink })
}

// stdSet is the standard library: packages that read neither the source nor
// the module cache, down to their dependencies.
func stdSet(blocks map[string]*block) map[string]bool {
	return closedSet(blocks, func(b *block) bool { return len(b.srcFiles) == 0 && len(b.modFiles) == 0 && !b.isLink })
}
