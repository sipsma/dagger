package main

import (
	"fmt"
	"strings"

	"dagger.io/dagger"
	"golang.org/x/sync/errgroup"
)

const (
	pythonPath    = "/root/.local/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	venv          = "/opt/venv"
	pythonAppDir  = "sdk/python"
	pythonVersion = "3.11"
)

type PythonTargets struct {
	Targets
}

func (t PythonTargets) sdkSrcDir(ctx dagger.Context) *dagger.Directory {
	return t.srcDir(ctx).Directory(pythonAppDir)
}

func (t PythonTargets) baseImage(ctx dagger.Context) *dagger.Container {
	// We mirror the same dir structure from the repo because of the
	// relative paths in ruff (for docs linting).
	mountPath := fmt.Sprintf("/%s", pythonAppDir)

	base := ctx.Client().Container().
		From(fmt.Sprintf("python:%s-alpine", pythonVersion)).
		WithEnvVariable("PATH", pythonPath).
		WithExec([]string{"apk", "add", "-U", "--no-cache", "gcc", "musl-dev", "libffi-dev"}).
		WithExec([]string{"pip", "install", "--user", "poetry==1.3.1", "poetry-dynamic-versioning"}).
		WithExec([]string{"python", "-m", "venv", venv}).
		WithEnvVariable("VIRTUAL_ENV", venv).
		WithEnvVariable("PATH", fmt.Sprintf("%s/bin:%s", venv, pythonPath)).
		WithEnvVariable("POETRY_VIRTUALENVS_CREATE", "false").
		WithWorkdir(mountPath)

	// FIXME: Use single `poetry.lock` directly with `poetry install --no-root`
	// 	when able: https://github.com/python-poetry/poetry/issues/1301
	reqFile := fmt.Sprintf("%s/requirements.txt", mountPath)
	requirements := base.
		WithMountedDirectory(mountPath, t.sdkSrcDir(ctx)).
		WithExec([]string{
			"poetry", "export",
			"--with", "test,lint,dev",
			"--without-hashes",
			"-o", "requirements.txt",
		}).
		File(reqFile)

	deps := base.
		WithRootfs(base.Rootfs().WithFile(reqFile, requirements)).
		WithExec([]string{"pip", "install", "-r", "requirements.txt"})

	return deps.
		WithRootfs(base.Rootfs().WithDirectory(mountPath, t.sdkSrcDir(ctx))).
		WithExec([]string{"poetry", "install", "--without", "docs"})
}

// Lint the Dagger Python SDK
func (t PythonTargets) PythonLint(ctx dagger.Context) (string, error) {
	// TODO: would be cool to write this in python... need support for mixed
	// languages in single project (or project nesting type thing)

	// TODO: pipeline should be automatically set
	c := ctx.Client().Pipeline("sdk").Pipeline("python").Pipeline("lint")

	eg, gctx := errgroup.WithContext(ctx)

	var poeLintOut string
	eg.Go(func() error {
		var err error
		poeLintOut, err = t.baseImage(ctx).
			WithExec([]string{"poe", "lint"}).
			Stdout(gctx)
		return err
	})

	var poeLintDocsOut string
	eg.Go(func() error {
		path := "docs/current/sdk/python/snippets"
		snippets := c.Directory().
			WithDirectory("/", t.srcDir(ctx).Directory(path))
		var err error
		poeLintDocsOut, err = t.baseImage(ctx).
			WithMountedDirectory(fmt.Sprintf("/%s", path), snippets).
			WithExec([]string{"poe", "lint-docs"}).
			Stdout(gctx)
		return err
	})

	// TODO: test generated code too

	return strings.Join([]string{
		poeLintOut,
		poeLintDocsOut,
	}, "\n"), eg.Wait()
}
