// Namespace demo module: one check that compiles a Go program from this
// repository and writes the binary into a mount that started empty, so the
// artifact's snapshot has a short ancestry.
package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/namespace-demo/internal/dagger"
)

type NamespaceDemo struct {
	// Image the build runs on.
	BaseImageAddress string

	// +private
	Source *dagger.Directory
}

func New(
	ws *dagger.Workspace,
	// +default="golang:1.26-alpine"
	baseImageAddress string,
) *NamespaceDemo {
	return &NamespaceDemo{
		BaseImageAddress: baseImageAddress,
		Source: ws.Directory("/", dagger.WorkspaceDirectoryOpts{
			Include: []string{"go.mod", "go.sum", "cmd/dnsname/**"},
		}),
	}
}

// A container with the source mounted, ready to build on.
func (m *NamespaceDemo) Container() *dagger.Container {
	return dag.Container().
		From(m.BaseImageAddress).
		WithDirectory("/src", m.Source).
		WithWorkdir("/src").
		WithEnvVariable("CGO_ENABLED", "0")
}

// Compile cmd/dnsname into an initially empty /out mount and return it.
func (m *NamespaceDemo) Artifact() *dagger.Directory {
	return m.Container().
		WithMountedDirectory("/out", dag.Directory()).
		WithExec([]string{"go", "build", "-o", "/out/dnsname", "./cmd/dnsname"}).
		Directory("/out")
}

// Build the artifact and verify it is there.
//
// +check
func (m *NamespaceDemo) Build(ctx context.Context) error {
	entries, err := m.Artifact().Entries(ctx)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0] != "dnsname" {
		return fmt.Errorf("unexpected artifact contents: %s", strings.Join(entries, ", "))
	}
	fmt.Println("artifact:", strings.Join(entries, ", "))
	return nil
}
