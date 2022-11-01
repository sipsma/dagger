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

	src, err := workdir.ID(ctx)
	if err != nil {
		return err
	}

	cfg, err := workdir.File(".markdownlint.yaml").ID(ctx)
	if err != nil {
		return err
	}

	_, err = c.Container().
		From("tmknom/markdownlint:0.31.1").
		WithMountedDirectory("/src", src).
		WithMountedFile("/src/.markdownlint.yaml", cfg).
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

	bindir, err := build(ctx, c)
	if err != nil {
		return err
	}

	ok, err := bindir.Export(ctx, "./bin")
	if err != nil {
		return err
	}

	if !ok {
		return errors.New("HostDirectoryWrite not ok")
	}
	return nil
}

func build(ctx context.Context, c *dagger.Client) (*dagger.Directory, error) {
	workdir := c.Host().Workdir()
	builder := c.Container().
		From("golang:1.19-alpine").
		WithEnvVariable("CGO_ENABLED", "0").
		WithEnvVariable("GOOS", runtime.GOOS).
		WithEnvVariable("GOARCH", runtime.GOARCH).
		WithWorkdir("/app")

	// install dependencies
	modules := c.Directory()
	for _, f := range []string{"go.mod", "go.sum", "sdk/go/go.mod", "sdk/go/go.sum"} {
		fileID, err := workdir.File(f).ID(ctx)
		if err != nil {
			return nil, err
		}

		modules = modules.WithCopiedFile(f, fileID)
	}
	modID, err := modules.ID(ctx)
	if err != nil {
		return nil, err
	}
	builder = builder.
		WithMountedDirectory("/app", modID).
		Exec(dagger.ContainerExecOpts{
			Args: []string{"go", "mod", "download"},
		})

	src, err := workdir.ID(ctx)
	if err != nil {
		return nil, err
	}

	builder = builder.
		WithMountedDirectory("/app", src).WithWorkdir("/app").
		Exec(dagger.ContainerExecOpts{
			Args: []string{"go", "build", "-o", "./bin/cloak", "-ldflags", "-s -w", "/app/cmd/cloak"},
		})

	return builder.Directory("./bin"), nil
}

func DeployDaggerEngine(ctx context.Context) error {
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer c.Close()

	cloakBindir, err := build(ctx, c)
	if err != nil {
		return err
	}
	cloakBindirID, err := cloakBindir.ID(ctx)
	if err != nil {
		return err
	}

	buildkitRepo, err := c.Git("github.com/moby/buildkit").Branch("master").Tree().ID(ctx)
	if err != nil {
		return err
	}

	buildkitCtr := c.Container().Build(buildkitRepo)
	rootfs, err := buildkitCtr.FS().
		WithDirectory(cloakBindirID, "/usr/bin/").
		ID(ctx)
	if err != nil {
		return err
	}

	tmpdir, err := os.MkdirTemp("", "dagger")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)
	tmppath := filepath.Join(tmpdir, "rootfs.tar")

	ctr := buildkitCtr.WithFS(rootfs).
		WithEnvVariable("BUILDKIT_HOST", "unix:///run/buildkit/buildkitd.sock")

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
		"-v", "/:/host", // TODO: handles submounts?
		"--name", "dagger-engine",
		"--privileged",
		"localhost/dagger-engine:latest",
	).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %w: %s", err, output)
	}
	return nil
}
