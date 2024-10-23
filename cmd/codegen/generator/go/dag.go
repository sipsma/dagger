package gogenerator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"dagger.io/dagger"
	"github.com/dagger/dagger/engine/distconsts"
	"github.com/opencontainers/go-digest"
)

/* TODO: not handling some cases mentioned in x/tools/go/packages

// Work around https://golang.org/issue/33157:
// go list -e, when given an absolute path, will find the package contained at
// that directory. But when no package exists there, it will return a fake package
// with an error and the ImportPath set to the absolute path provided to go list.
// Try to convert that absolute path to what its package path would be if it's
// contained in a known module or GOPATH entry. This will allow the package to be
// properly "reclaimed" when overlays are processed.
if filepath.IsAbs(p.ImportPath) && p.Error != nil {
	pkgPath, ok, err := state.getPkgPath(p.ImportPath)
	if err != nil {
		return nil, err
	}
	if ok {
		p.ImportPath = pkgPath
	}
}

Import loops are an error but not handled here

// If one version of the package has an error, and the other doesn't, assume
// that this is a case where go list is reporting a fake dependency variant
// of the imported package: When a package tries to invalidly import another
// package, go list emits a variant of the imported package (with the same
// import path, but with an error on it, and the package will have a
// DepError set on it). An example of when this can happen is for imports of
// main packages: main packages can not be imported, but they may be
// separately matched and listed by another pattern.
// See golang.org/issue/36188 for more details.
//
// The plan is that eventually, hopefully in Go 1.15, the error will be
// reported on the importing package rather than the duplicate "fake"
// version of the imported package. Once all supported versions of Go
// have the new behavior this logic can be deleted.
// TODO(matloob): delete the workaround logic once all supported versions of
// Go return the errors on the proper package.
//
// There should be exactly one version of a package that doesn't have an
// error.

*/

// TODO: make sure that callers tolerate errors here and just continue on:
// it's highly likely we don't handle everything perfectly, but the goal is for obsure
// cases like that to just result in worse performance rather than errors
func NewGoPackageDag(ctx context.Context, rootPkgDir string) (*GoPackageDag, error) {
	listCmd := exec.CommandContext(ctx, "go", "list",
		"-e",    // tolerate errors in packages
		"-deps", // include the whole DAG of deps
		"-json=Dir,ImportPath,Imports,Errors,Module",
	)
	listCmd.Dir = rootPkgDir
	copy(listCmd.Env, os.Environ())
	// TODO: prevents anything from being writen to the build cache, desireable?
	// TODO: also GODEBUG is a system env var, so probably should update it if already set rather than overwrite
	listCmd.Env = append(listCmd.Env, "GODEBUG=goindex=0")
	// TODO: this should be set on the container already..
	listCmd.Env = append(listCmd.Env, "GOPATH=/go")

	stdout := bytes.NewBuffer(nil)
	listCmd.Stdout = stdout
	stderr := bytes.NewBuffer(nil)
	listCmd.Stderr = stderr

	if err := listCmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run go list: %w: %s", err, stderr.String())
	}

	stderrStr := stderr.String()
	if stderrStr != "" {
		return nil, fmt.Errorf("go list failed: %s", stderrStr)
	}

	pkgDag := &GoPackageDag{
		RootPkgDir: rootPkgDir,
		pkgs:       make(map[string]*goPackage),
	}

	for decoder := json.NewDecoder(stdout); decoder.More(); {
		listPkg := new(goListPackage)
		if err := decoder.Decode(listPkg); err != nil {
			return nil, fmt.Errorf("failed to decode go list output: %w", err)
		}

		if err := pkgDag.addPackage(listPkg); err != nil {
			return nil, fmt.Errorf("failed to add package %s: %w", listPkg.ImportPath, err)
		}
	}

	if pkgDag.rootPkg == nil {
		return nil, fmt.Errorf("root package not found")
	}

	return pkgDag, nil
}

type GoPackageDag struct {
	RootPkgDir string

	// ImportPath -> package
	pkgs map[string]*goPackage

	rootPkg *goPackage
}

func (d *GoPackageDag) CacheBuilds(ctx context.Context, c *dagger.Client) error {
	base := c.BuiltinContainer(os.Getenv(distconsts.GoSDKManifestDigestEnvName)).
		// TODO: need a way to avoid hardcoding these caches (or remove the cache entirely I guess, but bigger effort)
		// TODO: not setting relevant system env vars here either...
		WithMountedCache("/go/pkg/mod", c.CacheVolume("modgomodcache")).
		WithMountedCache("/root/.cache/go-build", c.CacheVolume("modgobuildcache"))

	_, err := d.rootPkg.buildContainer(c, base).Sync(ctx)
	return err
}

func (d *GoPackageDag) addPackage(listPkg *goListPackage) error {
	pkg := d.getOrInitPkg(listPkg.ImportPath)
	if pkg.complete {
		return fmt.Errorf("package %s already added", listPkg.ImportPath)
	}
	pkg.complete = true

	pkg.Dir = listPkg.Dir
	if pkg.Dir == "" {
		return fmt.Errorf("package %s has empty Dir", listPkg.ImportPath)
	}
	if pkg.Dir == d.RootPkgDir {
		d.rootPkg = pkg
		pkg.IsRoot = true
	}

	for _, depImportPath := range listPkg.Imports {
		depPkg := d.getOrInitPkg(depImportPath)
		pkg.Deps[depImportPath] = depPkg
	}

	return nil
}

func (d *GoPackageDag) getOrInitPkg(importPath string) *goPackage {
	pkg, ok := d.pkgs[importPath]
	if !ok {
		pkg = &goPackage{
			ImportPath: importPath,
			Deps:       make(map[string]*goPackage),
		}
		d.pkgs[importPath] = pkg
	}
	return pkg
}

type goListPackage struct {
	Dir        string
	ImportPath string
	Imports    []string
}

type goPackage struct {
	Dir        string
	ImportPath string

	Deps map[string]*goPackage

	IsRoot bool

	complete            bool
	buildContainerCache *dagger.Container
}

func (p *goPackage) buildContainer(c *dagger.Client, base *dagger.Container) *dagger.Container {
	if p.buildContainerCache != nil {
		return p.buildContainerCache
	}

	const dummyOutputDir = "/out"

	ctr := base.
		WithMountedDirectory(dummyOutputDir, c.Directory())

	var sortedDeps []*goPackage
	for _, dep := range p.Deps {
		sortedDeps = append(sortedDeps, dep)
	}
	slices.SortFunc(sortedDeps, func(a, b *goPackage) int {
		return strings.Compare(a.ImportPath, b.ImportPath)
	})
	for _, dep := range sortedDeps {
		ctr = ctr.WithMountedDirectory(
			"/mnt/"+digest.FromString(dep.ImportPath).String(),
			dep.buildContainer(c, base).Directory(dummyOutputDir),
		)
	}

	if p.IsRoot {
		ctr = ctr.WithExec([]string{"true"})
	} else {
		ctr = ctr.
			WithEnvVariable("PWD", p.Dir).
			WithWorkdir(p.Dir).
			WithExec([]string{"go", "build",
				"-buildmode", "archive",
				p.Dir,
			})
	}

	p.buildContainerCache = ctr
	return ctr
}
