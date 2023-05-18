package main

import (
	"path/filepath"

	"dagger.io/dagger"
)

func main() {
	dagger.ServeCommands(
		Build,
		Test,
	)
}

// Build the go binary from the project at the given subpath.
func Build(ctx dagger.Context, subpath string) (string, error) {
	binpath := filepath.Join("./bin", filepath.Base(subpath))
	return baseContainer(ctx.Client()).
		WithExec([]string{"go", "build", "-v", "-x", "-o", binpath, "./" + subpath}).
		Stderr(ctx)

	// TODO: export it to the caller
	/*
		bin := baseContainer(ctx.Client()).
			WithExec([]string{"go", "build", "-v", "-x", "-o", binpath, "./" + subpath}).
			File(binpath)
		err := outputPath.ExportFile(ctx, bin)
	*/
}

// Test the go binary from the project at the given subpath
func Test(ctx dagger.Context, subpath string) (string, error) {
	args := []string{"go", "test", "-v", "./" + subpath}
	return baseContainer(ctx.Client()).
		WithExec(args).
		Stdout(ctx)
}

func baseContainer(c *dagger.Client) *dagger.Container {
	mntPath := "/src"
	return c.Container().
		From("golang:1.20-alpine").
		WithExec([]string{"apk", "add", "--no-cache", "file", "git", "openssh-client", "gcc", "build-base"}).
		WithMountedCache("/go/pkg/mod", c.CacheVolume("go-mod")).
		WithMountedCache("/root/.cache/go-build", c.CacheVolume("go-build")).
		WithMountedDirectory(mntPath, c.Host().Directory("/src")).
		WithWorkdir(mntPath)
}

/*
func Test(ctx dagger.Context, subpath string, race string, name string) (string, error) {
	args := []string{"go", "test", "-v"}
	cgoEnabled := "0"
	if race == "true" {
		args = append(args, "-race")
		cgoEnabled = "1"
	}
	if name != "" {
		args = append(args, "-run", name)
	}
	args = append(args, "./"+subpath)
	return baseContainer(ctx.Client()).
		WithEnvVariable("CGO_ENABLED", cgoEnabled).
		WithExec(args).
		Stdout(ctx)
}
*/
