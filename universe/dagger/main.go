package main

import (
	"dagger.io/dagger"
	"golang.org/x/sync/errgroup"
)

func main() {
	ctx := dagger.DefaultContext()
	ctx.Client().CurrentEnvironment().
		WithCommand(Targets.Lint).
		WithCommand(Targets.EngineLint).
		WithCommand(Targets.Cli).
		WithCommand(PythonTargets.PythonLint).
		WithCommand(NodejsTargets.NodejsLint).
		// Merge in all the entrypoints from the Go SDK too under the "go" namespace
		WithExtension(ctx.Client().Environment().LoadFromUniverse("dagger/gosdk"), "go").
		Serve(ctx)
}

type Targets struct {
	// If set, the git repo to pull the dagger repo source code from
	Repo string
	// If set, the branch of the --repo setting
	Branch string
}

func (t Targets) srcDir(ctx dagger.Context) *dagger.Directory {
	srcDir := ctx.Client().Host().Directory(".")
	if t.Repo != "" {
		srcDir = ctx.Client().Git(t.Repo).Branch(t.Branch).Tree()
	}
	return srcDir
}

// Lint everything (engine, sdks, etc)
func (t Targets) Lint(ctx dagger.Context) (string, error) {
	var eg errgroup.Group
	eg.Go(func() error {
		_, err := ctx.Client().Environment().
			LoadFromUniverse("dagger/gosdk").
			Command("lint").
			SetStringFlag("repo", t.Repo).
			SetStringFlag("branch", t.Branch).
			Invoke().String(ctx)
		return err
	})
	eg.Go(func() error {
		_, err := t.EngineLint(ctx)
		return err
	})
	eg.Go(func() error {
		_, err := PythonTargets{t}.PythonLint(ctx)
		return err
	})
	eg.Go(func() error {
		_, err := NodejsTargets{t}.NodejsLint(ctx)
		return err
	})
	return "", eg.Wait()
}
