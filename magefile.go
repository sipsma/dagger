//go:build mage
// +build mage

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"dagger.io/dagger"
	"github.com/dagger/dagger/codegen/generator"
	"github.com/google/go-cmp/cmp"
	"github.com/magefile/mage/mg" // mg contains helpful utility functions, like Deps
)

type Lint mg.Namespace

// All runs all lint targets
func (t Lint) All(ctx context.Context) error {
	mg.Deps(
		t.Codegen,
		t.Markdown,
	)
	return nil
}

// Markdown lints the markdown files
func (Lint) Markdown(ctx context.Context) error {
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer c.Close()

	workdir := c.Host().Workdir()

	_, err = c.Container().
		From("tmknom/markdownlint:0.31.1").
		WithMountedDirectory("/src", workdir).
		WithMountedFile("/src/.markdownlint.yaml", workdir.File(".markdownlint.yaml")).
		WithWorkdir("/src").
		Exec(dagger.ContainerExecOpts{
			Args: []string{
				"-c",
				".markdownlint.yaml",
				"--",
				"./docs",
				"README.md",
			},
		}).ExitCode(ctx)
	return err
}

// Codegen ensure the SDK code was re-generated
func (Lint) Codegen(ctx context.Context) error {
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer c.Close()

	generated, err := generator.IntrospectAndGenerate(ctx, c, generator.Config{
		Package: "dagger",
	})
	if err != nil {
		return err
	}

	// grab the file from the repo
	src, err := c.
		Host().
		Workdir().
		File("sdk/go/api.gen.go").
		Contents(ctx)
	if err != nil {
		return err
	}

	// compare the two
	diff := cmp.Diff(string(generated), src)
	if diff != "" {
		return fmt.Errorf("generated api mismatch. please run `go generate ./...`:\n%s", diff)
	}

	return nil
}

// Build builds the binary
func Build(ctx context.Context) error {
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer c.Close()

	src := c.Host().Workdir()

	// Create a directory containing only `go.{mod,sum}` files.
	goMods := c.Directory()
	for _, f := range []string{"go.mod", "go.sum", "sdk/go/go.mod", "sdk/go/go.sum"} {
		goMods = goMods.WithFile(f, src.File(f))
	}

	build := c.Container().
		From("golang:1.19-alpine").
		WithEnvVariable("CGO_ENABLED", "0").
		WithEnvVariable("GOOS", runtime.GOOS).
		WithEnvVariable("GOARCH", runtime.GOARCH).
		WithWorkdir("/app").
		// run `go mod download` with only go.mod files (re-run only if mod files have changed)
		WithMountedDirectory("/app", goMods).
		Exec(dagger.ContainerExecOpts{
			Args: []string{"go", "mod", "download"},
		}).
		// run `go build` with all source
		WithMountedDirectory("/app", src).
		Exec(dagger.ContainerExecOpts{
			Args: []string{"go", "build", "-o", "./bin/cloak", "-ldflags", "-s -w", "/app/cmd/cloak"},
		})

	ok, err := build.Directory("./bin").Export(ctx, "./bin")
	if err != nil {
		return err
	}

	if !ok {
		return errors.New("HostDirectoryWrite not ok")
	}
	return nil
}

// TODO: dedupe
func buildCloak(c *dagger.Client, platforms ...dagger.Platform) []*dagger.Container {
	src := c.Host().Workdir()

	// Create a directory containing only `go.{mod,sum}` files.
	goMods := c.Directory()
	for _, f := range []string{"go.mod", "go.sum", "sdk/go/go.mod", "sdk/go/go.sum"} {
		goMods = goMods.WithFile(f, src.File(f))
	}

	var ctrs []*dagger.Container
	for _, p := range platforms {
		build := c.Container().
			From("golang:1.19-alpine").
			WithEnvVariable("CGO_ENABLED", "0").
			WithEnvVariable("GOOS", runtime.GOOS).
			WithEnvVariable("GOARCH", runtime.GOARCH).
			WithWorkdir("/app").
			// run `go mod download` with only go.mod files (re-run only if mod files have changed)
			WithMountedDirectory("/app", goMods).
			Exec(dagger.ContainerExecOpts{
				Args: []string{"go", "mod", "download"},
			}).
			// run `go build` with all source
			WithMountedDirectory("/app", src).
			WithMountedDirectory("/output", c.Directory()).
			Exec(dagger.ContainerExecOpts{
				Args: []string{"go", "build", "-o", "/output/cloak", "-ldflags", "-s -w", "/app/cmd/cloak"},
			}).
			Directory("/output")

		ctr := c.Container(dagger.ContainerOpts{Platform: p}).WithFS(build)
		ctrs = append(ctrs, ctr)
	}
	return ctrs
}

func buildkitBase(c *dagger.Client, platforms ...dagger.Platform) []*dagger.Container {
	buildkitRepo := c.Git("github.com/moby/buildkit").Branch("master").Tree()

	var ctrs []*dagger.Container
	for _, p := range platforms {
		ctr := c.Container(dagger.ContainerOpts{Platform: p}).Build(buildkitRepo)
		ctrs = append(ctrs, ctr)
	}
	return ctrs
}

func mergeByPlatform(ctx context.Context, a []*dagger.Container, b []*dagger.Container) ([]*dagger.Container, error) {
	var ctrs []*dagger.Container
	byPlatform := map[dagger.Platform]*dagger.Container{}
	for _, ctr := range a {
		p, err := ctr.Platform(ctx)
		if err != nil {
			return nil, err
		}
		byPlatform[p] = ctr
	}
	for _, ctr := range b {
		p, err := ctr.Platform(ctx)
		if err != nil {
			return nil, err
		}
		var merged *dagger.Container
		if other, ok := byPlatform[p]; ok {
			merged = other.WithFS(other.FS().WithDirectory("/", ctr.FS()))
		} else {
			merged = ctr
		}
		ctrs = append(ctrs, merged)
	}
	return ctrs, nil
}

func DeployDaggerEngine(ctx context.Context) error {
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer c.Close()

	hostPlatform, err := c.DefaultPlatform(ctx)
	if err != nil {
		return err
	}

	ctrs, err := mergeByPlatform(ctx,
		buildkitBase(c, hostPlatform),
		buildCloak(c, hostPlatform),
	)
	if err != nil {
		return err
	}
	// only one platform atm
	ctr := ctrs[0]
	ctr = ctr.WithEnvVariable("BUILDKIT_HOST", "unix:///run/buildkit/buildkitd.sock")

	tmpdir, err := os.MkdirTemp("", "dagger")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)
	tmppath := filepath.Join(tmpdir, "rootfs.tar")

	_, err = ctr.Export(ctx, tmppath)
	if err != nil {
		return err
	}

	loadCmd := exec.CommandContext(ctx, "docker", "load", "-i", tmppath)
	output, err := loadCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker load failed: %w: %s", err, output)
	}
	_, imageID, ok := strings.Cut(string(output), "sha256:")
	if !ok {
		return fmt.Errorf("unexpected output from docker load: %s", output)
	}
	imageID = strings.TrimSpace(imageID)

	if output, err := exec.CommandContext(ctx, "docker",
		"tag",
		imageID,
		"localhost/dagger-engine:latest",
	).CombinedOutput(); err != nil {
		return fmt.Errorf("docker tag: %w: %s", err, output)
	}

	if output, err := exec.CommandContext(ctx, "docker",
		"rm",
		"-fv",
		"dagger-engine",
	).CombinedOutput(); err != nil {
		return fmt.Errorf("docker rm: %w: %s", err, output)
	}

	if output, err := exec.CommandContext(ctx, "docker",
		"run",
		"-d",
		"--restart", "always",
		"-v", "dagger-engine:/var/lib/buildkit",
		"--name", "dagger-engine",
		"--privileged",
		"localhost/dagger-engine:latest",
		"--debug",
	).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %w: %s", err, output)
	}

	return nil
}
