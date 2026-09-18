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
	// +private
	Ws *dagger.Workspace
	// VCS info resolved to scalars so that module-to-module calls do not
	// carry the session-scoped workspace into their cache keys (same as
	// engine-dev).
	VCSCommit string // +private
	VCSDirty  bool   // +private
}

func New(
	ctx context.Context,
	ws *dagger.Workspace,
	// +default="golang:1.26-alpine"
	baseImageAddress string,
) *NamespaceDemo {
	commit, dirty := vcsInfo(ctx, ws)
	return &NamespaceDemo{
		BaseImageAddress: baseImageAddress,
		Source: ws.Directory("/", dagger.WorkspaceDirectoryOpts{
			Include: []string{"go.mod", "go.sum", "cmd/dnsname/**"},
		}),
		Ws:        ws,
		VCSCommit: commit,
		VCSDirty:  dirty,
	}
}

func vcsInfo(ctx context.Context, ws *dagger.Workspace) (commit string, dirty bool) {
	if ws == nil {
		return "", false
	}
	git := ws.Git()
	commit, err := git.Head().Commit(ctx)
	if err != nil {
		return "", false
	}
	if clean, err := git.Uncommitted().IsEmpty(ctx); err == nil {
		dirty = !clean
	}
	return commit, dirty
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

// Build the dagger CLI for linux/amd64 with the cli module and write it into
// an initially empty /out mount.
func (m *NamespaceDemo) CliArtifact() *dagger.Directory {
	cli := dag.CliDev(dagger.CliDevOpts{VcsCommit: m.VCSCommit, VcsDirty: m.VCSDirty, Ws: m.Ws}).
		Binary(dagger.CliDevBinaryOpts{Platform: "linux/amd64"})
	return dag.Container().
		From("alpine:3.21").
		WithMountedDirectory("/out", dag.Directory()).
		WithFile("/out/dagger", cli).
		WithExec([]string{"ls", "-l", "/out/dagger"}).
		Directory("/out")
}

// Build the dagger CLI for linux/amd64 and verify the binary is there.
//
// +check
func (m *NamespaceDemo) BuildCli(ctx context.Context) error {
	entries, err := m.CliArtifact().Entries(ctx)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0] != "dagger" {
		return fmt.Errorf("unexpected artifact contents: %s", strings.Join(entries, ", "))
	}
	fmt.Println("artifact:", strings.Join(entries, ", "))
	return nil
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
