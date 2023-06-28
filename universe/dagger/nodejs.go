package main

import (
	"strings"

	"dagger.io/dagger"
	"golang.org/x/sync/errgroup"
)

type NodejsTargets struct {
	Targets
}

func (t NodejsTargets) sdkSrcDir(ctx dagger.Context) *dagger.Directory {
	return t.srcDir(ctx).Directory("sdk/nodejs")
}

func (t NodejsTargets) baseImage(ctx dagger.Context) *dagger.Container {
	sdkSrcDir := t.sdkSrcDir(ctx)

	base := ctx.Client().Container().
		// ⚠️  Keep this in sync with the engine version defined in package.json
		From("node:16-alpine").
		WithWorkdir("/workdir")

	deps := base.WithRootfs(
		base.
			Rootfs().
			WithFile("/workdir/package.json", sdkSrcDir.File("package.json")).
			WithFile("/workdir/yarn.lock", sdkSrcDir.File("yarn.lock")),
	).
		WithExec([]string{"yarn", "install"})

	return deps.WithRootfs(
		deps.
			Rootfs().
			WithDirectory("/workdir", sdkSrcDir),
	)
}

// Lint the Nodejs SDK
func (t NodejsTargets) NodejsLint(ctx dagger.Context) (string, error) {
	// TODO: pipeline should be automatically set
	c := ctx.Client().Pipeline("sdk").Pipeline("nodejs").Pipeline("lint")

	eg, gctx := errgroup.WithContext(ctx)

	var yarnLintOut string
	eg.Go(func() error {
		var err error
		yarnLintOut, err = t.baseImage(ctx).
			WithExec([]string{"yarn", "lint"}).
			Stdout(gctx)
		return err
	})

	var docLintOut string
	eg.Go(func() error {
		snippets := c.Directory().
			WithDirectory("/", t.srcDir(ctx).Directory("docs/current/sdk/nodejs/snippets"))
		var err error
		docLintOut, err = t.baseImage(ctx).
			WithMountedDirectory("/snippets", snippets).
			WithWorkdir("/snippets").
			WithExec([]string{"yarn", "install"}).
			WithExec([]string{"yarn", "lint"}).
			Stdout(gctx)
		return err
	})

	// TODO: test generated code too

	return strings.Join([]string{
		yarnLintOut,
		docLintOut,
	}, "\n"), eg.Wait()
}
