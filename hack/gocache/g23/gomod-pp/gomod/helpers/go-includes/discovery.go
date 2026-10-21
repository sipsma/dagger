package main

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// discoveryFile names the scan's package discovery: what the go module needs
// to list and build a workspace's packages, found in this one pass over the
// files instead of an engine search, glob or field read per result.
//
//	module	<root>	<module path>	<go version>
//	package	<root>	<module-relative directory>	<kinds>
//
// There is a module line for every module the scan accepts, followed by a
// package line for each of its package directories with something to do.
// kinds holds "t" when a _test.go file there declares a test, fuzz or example
// function, "m" when a non-test file is package main without an ignore build
// tag, and "g" when a file has a go:generate line. The files are the module's
// own, as packageFiles lists them, and each line is matched on its own, as a
// search would.
const discoveryFile = "_discovery_"

var (
	testFuncLine = regexp.MustCompile(`^func (Test([^a-z(]\w*)?\(\s*\w+\s+\*testing\.T\s*\)|Fuzz([^a-z(]\w*)?\(\s*\w+\s+\*testing\.F\s*\)|Example([^a-z(]\w*)?\(\s*\))`)
	mainLine     = regexp.MustCompile(`^package main\s*(//.*)?$`)
	ignoreLine   = regexp.MustCompile(`^//(go:build| \+build) ignore\s*$`)
	generateLine = regexp.MustCompile(`^//go:generate\s.*$`)
)

func (index *localIndex) writeDiscovery(outPath string) error {
	var out strings.Builder
	for _, root := range index.goModules() {
		modPath := ""
		goModPath := path.Join(root, "go.mod")
		if data, err := os.ReadFile(filepath.Join(index.root, filepath.FromSlash(goModPath))); err == nil {
			if goMod, err := parseGoMod(goModPath, data); err == nil {
				modPath = goMod.Module.Mod.Path
			}
		}
		out.WriteString("module\t" + root + "\t" + modPath + "\t" + index.goVersion(root) + "\n")
		kinds := map[string]string{}
		for _, file := range index.packageFiles(root) {
			k, err := fileKinds(filepath.Join(index.root, filepath.FromSlash(file)), strings.HasSuffix(file, "_test.go"))
			if err != nil {
				return err
			}
			dir := path.Dir(moduleRelative(root, file))
			for _, c := range k {
				if !strings.ContainsRune(kinds[dir], c) {
					kinds[dir] += string(c)
				}
			}
		}
		dirs := make([]string, 0, len(kinds))
		for dir, k := range kinds {
			if k != "" {
				dirs = append(dirs, dir)
			}
		}
		sort.Strings(dirs)
		for _, dir := range dirs {
			k := []byte(kinds[dir])
			sort.Slice(k, func(i, j int) bool { return k[i] < k[j] })
			out.WriteString("package\t" + root + "\t" + dir + "\t" + string(k) + "\n")
		}
	}
	return os.WriteFile(outPath, []byte(out.String()), 0o644)
}

// fileKinds is what one Go file marks its directory as: "t", "m" and "g", as
// discoveryFile describes them.
func fileKinds(file string, test bool) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	tests, main, ignore, generate := false, false, false, false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case test && testFuncLine.MatchString(line):
			tests = true
		case !test && mainLine.MatchString(line):
			main = true
		case !test && ignoreLine.MatchString(line):
			ignore = true
		}
		if generateLine.MatchString(line) {
			generate = true
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	k := ""
	if tests {
		k += "t"
	}
	if main && !ignore {
		k += "m"
	}
	if generate {
		k += "g"
	}
	return k, nil
}
