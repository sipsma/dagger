package modules

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dagger.io/dagger"
	"github.com/moby/buildkit/identity"
)

var (
	// The digest-pinned ref of an address that can run 'git'
	// FIXME: make this image smaller
	gitImageRef = "index.docker.io/alpine/git@sha256:1031f50b5bdda7eee6167e362cff09b8c809889cd43e5306abc5bf6438e68890"
)

// Ref contains all of the information we're able to learn about a provided
// module ref.
type Ref struct {
	// The following are mutually exclusive depending on the type of ref.
	Local *LocalRef
	Git   *GitRef
}

type LocalRef struct {
	// ModuleSourcePath is the path to the module's source code directory (which may or may not.
	// be the same directory containing dagger.json).
	//
	// It is always an absolute path.
	//
	// For a local ref specified as a dependency in a dagger.json config from a parent git ref,
	// it is the absolute path of the source directory in the parent git repo.
	//
	// Otherwise, it is the path specified by the caller on their local filesystem.
	ModuleSourcePath string
}

type GitRef struct {
	// ModuleSourcePath is the path to the module's source code directory within the git repo.
	// It is always an absolute path.
	ModuleSourcePath string

	// URLParent is the prefix of the URL to the git repo (i.e. "github.com/someOrg/someRepo").
	URLParent string

	// Version is the provided version for the module.
	Version string

	// Commit is the commit to check out.
	Commit string
}

func (gitRef *GitRef) String() string {
	return fmt.Sprintf("%s/%s@%s", gitRef.URLParent, gitRef.ModuleSourcePath, gitRef.Version)
}

func (gitRef *GitRef) CloneURL() string {
	return "https://" + gitRef.URLParent
}

func (gitRef *GitRef) HTMLURL() string {
	return "https://" + gitRef.URLParent + "/tree" + gitRef.Version + "/" + gitRef.ModuleSourcePath
}

func (ref *Ref) String() string {
	switch {
	case ref.Local != nil:
		return ref.Local.ModuleSourcePath
	case ref.Git != nil:
		return ref.Git.String()
	default:
		panic("invalid module ref")
	}
}

func (ref *Ref) Symbolic() string {
	switch {
	case ref.Local != nil:
		return ref.Local.ModuleSourcePath
	case ref.Git != nil:
		return filepath.Join(ref.Git.CloneURL(), ref.Git.ModuleSourcePath)
	default:
		panic("invalid module ref")
	}
}

// ConfigPath is the path to the dagger.json config file for the module source code this
// ref points to.
func (ref *Ref) ConfigPath(ctx context.Context, c *dagger.Client) (string, error) {
	switch {
	case ref.Local != nil:
		configPath, err := findLocalModuleConfigPath(ref.Local.ModuleSourcePath)
		if err != nil {
			return "", fmt.Errorf("failed to find config file from local ref path %s: %w", ref.Local.ModuleSourcePath, err)
		}
		return configPath, nil

	case ref.Git != nil:
		configPath, err := findLocalModuleConfigPath(ref.Local.ModuleSourcePath)
		if err != nil {
			return "", fmt.Errorf("failed to find config file from git ref path %s: %w", ref.Git.ModuleSourcePath, err)
		}
		return configPath, nil

	default:
		return "", fmt.Errorf("invalid module ref")
	}
}

// ModuleSourceRelPath returns the path of the module source code relative to the dagger.json config file.
func (ref *Ref) ModuleSourceRelPath(ctx context.Context, c *dagger.Client) (string, error) {
	cfgPath, err := ref.ConfigPath(ctx, c)
	if err != nil {
		return "", fmt.Errorf("failed to get module config: %w", err)
	}
	cfgDir := filepath.Dir(cfgPath)

	var moduleSourcePath string
	switch {
	case ref.Local != nil:
		moduleSourcePath = ref.Local.ModuleSourcePath
	case ref.Git != nil:
		moduleSourcePath = ref.Git.ModuleSourcePath
	default:
		return "", fmt.Errorf("invalid module ref")
	}

	sourceRelPath, err := filepath.Rel(cfgDir, moduleSourcePath)
	if err != nil {
		return "", fmt.Errorf("failed to get module source path: %w", err)
	}
	return sourceRelPath, nil
}

// Config returns the dagger.json config that contains configuration for this module (possibly amongst other modules).
// It also returns the path that config file was found (which may not always be the same as the source of the module
// itself).
func (ref *Ref) Config(ctx context.Context, c *dagger.Client) (*Config, string, error) {
	switch {
	case ref.Local != nil:
		configPath, err := ref.ConfigPath(ctx, c)
		if err != nil {
			return nil, "", fmt.Errorf("failed to find local config file from ref %q: %w", ref, err)
		}

		configBytes, err := os.ReadFile(configPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to read local config file: %w", err)
		}
		var cfg Config
		if err := json.Unmarshal(configBytes, &cfg); err != nil {
			return nil, "", fmt.Errorf("failed to parse local config file: %w", err)
		}

		return &cfg, configPath, nil

	case ref.Git != nil:
		if c == nil {
			return nil, "", fmt.Errorf("cannot load git module config with nil dagger client")
		}

		repoDir := c.Git(ref.Git.CloneURL()).Commit(ref.Git.Version).Tree()
		configPath, configStr, err := findDirModuleConfigPath(ctx, repoDir, ref.Git.ModuleSourcePath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to find git config file from ref %q: %w", ref, err)
		}

		var cfg Config
		if err := json.Unmarshal([]byte(configStr), &cfg); err != nil {
			return nil, "", fmt.Errorf("failed to parse git config file: %w", err)
		}

		return &cfg, configPath, nil

	default:
		return nil, "", fmt.Errorf("invalid module ref")
	}
}

// ModuleConfig returns the configuration for this specific module and the path of the
// source dir relative to its dagger.json config.
func (ref *Ref) ModuleConfig(ctx context.Context, c *dagger.Client) (*ModuleConfig, string, error) {
	cfg, _, err := ref.Config(ctx, c)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get config: %w", err)
	}
	sourceRelPath, err := ref.ModuleSourceRelPath(ctx, c)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get module source rel path: %w", err)
	}
	modCfg, ok := cfg.ModuleConfigByPath(sourceRelPath)
	if !ok {
		return nil, "", fmt.Errorf("failed to find module config for %q", sourceRelPath)
	}
	return modCfg, sourceRelPath, nil
}

func (ref *Ref) AsModule(ctx context.Context, c *dagger.Client) (*dagger.Module, error) {
	src, modSrcRelPath, err := ref.source(ctx, c)
	if err != nil {
		return nil, err
	}

	return src.AsModule(dagger.DirectoryAsModuleOpts{
		SourceSubpath: modSrcRelPath,
	}), nil
}

func (ref *Ref) AsUninitializedModule(ctx context.Context, c *dagger.Client) (*dagger.Module, error) {
	src, modSrcRelPath, err := ref.source(ctx, c)
	if err != nil {
		return nil, err
	}

	return c.Module().
		WithSource(src, dagger.ModuleWithSourceOpts{
			Subpath: modSrcRelPath,
		}), nil
}

func (ref *Ref) source(ctx context.Context, c *dagger.Client) (*dagger.Directory, string, error) {
	cfgPath, err := ref.ConfigPath(ctx, c)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get config: %w", err)
	}
	cfgDir := filepath.Dir(cfgPath)

	modCfg, modSrcRelPath, err := ref.ModuleConfig(ctx, c)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get module config: %w", err)
	}

	switch {
	case ref.Local != nil:
		return c.Host().Directory(cfgDir, dagger.HostDirectoryOpts{
			Include: modCfg.Include,
			Exclude: modCfg.Exclude,
		}), modSrcRelPath, nil

	case ref.Git != nil:
		return c.Git(ref.Git.CloneURL()).Commit(ref.Git.Version).Tree().Directory(cfgDir), modSrcRelPath, nil

	default:
		return nil, "", fmt.Errorf("invalid module (local=%t, git=%t)", ref.Local != nil, ref.Git != nil)
	}
}

func IsLocalRef(modQuery string) bool {
	modPath, _, hasVersion := strings.Cut(modQuery, "@")
	isGitHub := strings.HasPrefix(modPath, "github.com/")
	return !hasVersion && !isGitHub
}

// TODO dedup with ResolveMovingRef
func ResolveStableRef(ctx context.Context, dag *dagger.Client, parentRef *Ref, modQuery string) (*Ref, error) {
	modPath, modVersion, hasVersion := strings.Cut(modQuery, "@")

	// TODO: figure out how to support arbitrary repos in a predictable way.
	// Maybe piggyback on whatever Go supports? (the whole <meta go-import>
	// thing)
	isGitHub := strings.HasPrefix(modPath, "github.com/")

	if !hasVersion {
		if isGitHub {
			return nil, fmt.Errorf("no version provided for remote ref: %s", modQuery)
		}

		// assume local path
		//
		// NB(vito): HTTP URLs should be supported by taking a sha256 digest as the
		// version. so it should be safe to assume no version = local path. as a
		// rule, if it's local we don't need to version it; if it's remote, we do.
		return resolveLocalRef(ctx, dag, parentRef, modPath)
	}

	ref := &Ref{Git: &GitRef{}} // assume git for now, HTTP can come later

	if !isGitHub {
		return nil, fmt.Errorf("for now, only github.com/ paths are supported: %s", modPath)
	}

	if !hasVersion {
		return nil, fmt.Errorf("no version provided for %s", modPath)
	}

	segments := strings.SplitN(modPath, "/", 4)
	if len(segments) < 3 {
		return nil, fmt.Errorf("invalid github.com path: %s", modPath)
	}

	ref.Git.URLParent = segments[0] + "/" + segments[1] + "/" + segments[2]
	ref.Git.Version = modVersion // assume commit
	ref.Git.Commit = modVersion  // assume commit

	if len(segments) == 4 {
		ref.Git.ModuleSourcePath = segments[3]
	} else {
		ref.Git.ModuleSourcePath = "/"
	}

	return ref, nil
}

func ResolveMovingRef(ctx context.Context, dag *dagger.Client, parentRef *Ref, modQuery string) (*Ref, error) {
	modPath, modVersion, hasVersion := strings.Cut(modQuery, "@")

	// TODO: figure out how to support arbitrary repos in a predictable way.
	// Maybe piggyback on whatever Go supports? (the whole <meta go-import>
	// thing)
	isGitHub := strings.HasPrefix(modPath, "github.com/")

	if !hasVersion && !isGitHub {
		// assume local path
		//
		// NB(vito): HTTP URLs should be supported by taking a sha256 digest as the
		// version. so it should be safe to assume no version = local path. as a
		// rule, if it's local we don't need to version it; if it's remote, we do.
		return resolveLocalRef(ctx, dag, parentRef, modPath)
	}

	ref := &Ref{Git: &GitRef{}} // assume git for now, HTTP can come later

	if !isGitHub {
		return nil, fmt.Errorf("for now, only github.com/ paths are supported: %q", modQuery)
	}

	segments := strings.SplitN(modPath, "/", 4)
	if len(segments) < 3 {
		return nil, fmt.Errorf("invalid github.com path: %s", modPath)
	}

	ref.Git.URLParent = segments[0] + "/" + segments[1] + "/" + segments[2]

	if !hasVersion {
		var err error
		modVersion, err = defaultBranch(ctx, dag, ref.Git.CloneURL())
		if err != nil {
			return nil, fmt.Errorf("determine default branch: %w", err)
		}
	}

	gitCommit, err := dag.Git(ref.Git.CloneURL(), dagger.GitOpts{KeepGitDir: true}).Commit(modVersion).Commit(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve git ref: %w", err)
	}

	ref.Git.Version = gitCommit // TODO preserve semver here
	ref.Git.Commit = gitCommit  // but tell the truth here

	if len(segments) == 4 {
		ref.Git.ModuleSourcePath = segments[3]
	} else {
		ref.Git.ModuleSourcePath = "/"
	}

	return ref, nil
}

func resolveLocalRef(ctx context.Context, dag *dagger.Client, parentRef *Ref, localModPath string) (*Ref, error) {
	switch {
	case parentRef == nil:
		localModPath, err := filepath.Abs(localModPath)
		if err != nil {
			return nil, fmt.Errorf("failed to get absolute path: %w", err)
		}
		return &Ref{Local: &LocalRef{ModuleSourcePath: localModPath}}, nil

	case parentRef.Local != nil:
		cfgPath, err := parentRef.ConfigPath(ctx, dag)
		if err != nil {
			return nil, fmt.Errorf("failed to get config path for local parent ref: %w", err)
		}
		cfgDir := filepath.Dir(cfgPath)

		if filepath.IsAbs(localModPath) {
			// verify localModPath is a subdir of the parent ref's config dir
			if !strings.HasPrefix(localModPath, cfgDir) {
				return nil, fmt.Errorf("local module path %q is not a subdirectory of local parent ref %q", localModPath, parentRef)
			}
			return &Ref{Local: &LocalRef{ModuleSourcePath: localModPath}}, nil
		}

		// it's a path relative to the parent ref's config dir
		return &Ref{Local: &LocalRef{ModuleSourcePath: filepath.Join(cfgDir, localModPath)}}, nil

	case parentRef.Git != nil:
		cfgPath, err := parentRef.ConfigPath(ctx, dag)
		if err != nil {
			return nil, fmt.Errorf("failed to get config path for local parent ref: %w", err)
		}
		cfgDir := filepath.Dir(cfgPath)

		if filepath.IsAbs(localModPath) {
			// verify localModPath is a subdir of the parent ref's config dir
			if !strings.HasPrefix(localModPath, cfgDir) {
				return nil, fmt.Errorf("local module path %q is not a subdirectory of git parent ref %q", localModPath, parentRef)
			}
			parentCopy := *parentRef
			parentCopy.Git.ModuleSourcePath = localModPath
			return &parentCopy, nil
		}

		parentCopy := *parentRef
		parentCopy.Git.ModuleSourcePath = filepath.Join(cfgDir, localModPath)
		return &parentCopy, nil

	default:
		return nil, fmt.Errorf("invalid parent ref: %s", parentRef)
	}
}

// findLocalModuleConfigPath does a "find-up" on the provided dir, returning the
// first dagger.json found in the given dir or one of its parents.
func findLocalModuleConfigPath(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	configPath := filepath.Join(dir, Filename)
	if _, err := os.Stat(configPath); err == nil {
		return configPath, nil
	}

	var atRoot bool
	if dir == "/" {
		atRoot = true
	} else if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		atRoot = true
	}

	if atRoot {
		// we reached the module root; time to give up
		return "", errors.New("not found")
	}

	return findLocalModuleConfigPath(filepath.Dir(dir))
}

func findDirModuleConfigPath(
	ctx context.Context,
	dir *dagger.Directory,
	curSubdir string,
) (string, string, error) {
	curSubdir = filepath.Clean(curSubdir)
	if curSubdir == "." {
		curSubdir = "/"
	}
	if !filepath.IsAbs(curSubdir) {
		return "", "", fmt.Errorf("relative path %q is not supported", curSubdir)
	}

	configPath := filepath.Join(curSubdir, Filename)
	configStr, err := dir.File(configPath).Contents(ctx)
	if err == nil {
		return configPath, configStr, nil
	}

	if curSubdir == "/" {
		// we reached the module root; time to give up
		return "", "", errors.New("not found")
	}

	return findDirModuleConfigPath(ctx, dir, filepath.Dir(curSubdir))
}

func defaultBranch(ctx context.Context, dag *dagger.Client, repo string) (string, error) {
	output, err := dag.Container().
		From(gitImageRef).
		WithEnvVariable("CACHEBUSTER", identity.NewID()). // force this to always run so we don't get stale data
		WithExec([]string{"git", "ls-remote", "--symref", repo, "HEAD"}, dagger.ContainerWithExecOpts{
			SkipEntrypoint: true,
		}).
		Stdout(ctx)
	if err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(bytes.NewBufferString(output))

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}

		if fields[0] == "ref:" && fields[2] == "HEAD" {
			return strings.TrimPrefix(fields[1], "refs/heads/"), nil
		}
	}

	return "", fmt.Errorf("could not deduce default branch from output:\n%s", output)
}
