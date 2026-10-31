package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// The default base container's Wolfi packages, with all their dependencies,
// pinned to exact versions.
//
// Wolfi publishes new builds of these packages most days. Resolved live, two
// engines that build the base at different times would install different
// packages, and so build a different base and rebuild every Go package on
// top of it. Installing the locked set makes the base change only when this
// lock does.
//
//go:embed wolfi.lock.json
var wolfiLockJSON []byte

type wolfiLock struct {
	// Go version the packages were resolved for
	Go string `json:"go"`
	// The resolved packages as "name=version", by GOARCH
	Packages map[string][]string `json:"packages"`
}

// The Wolfi packages the default base container asks for, before their
// dependencies are resolved.
func wolfiPackages(version string) []string {
	return []string{
		"go-" + version,
		"ca-certificates",
		// gcc is needed to run go test -race https://github.com/golang/go/issues/9918 (???)
		"build-base",
		// adding the git CLI to inject vcs info into the go binaries
		"git",
		// Install protoc for protobug support by default
		// The specific version is dictated by Dagger's own requirement
		// FIXME: make this optional with overlay support
		"protobuf~35", // ADD: brings /usr/bin/protoc and runtime libs
		"protobuf-dev~35",
		// engine-dev's integration tests install these as extra packages.
		// Having them in every base puts them and their dependencies under the
		// lock too.
		"openssh-client",
		"curl",
	}
}

func loadWolfiLock() (wolfiLock, error) {
	var lock wolfiLock
	if err := json.Unmarshal(wolfiLockJSON, &lock); err != nil {
		return lock, fmt.Errorf("parse wolfi.lock.json: %w", err)
	}
	return lock, nil
}

// The packages to install in the default base container for the given Go
// version and GOARCH: the locked set when the lock was resolved for that Go
// version, otherwise the unresolved names.
func (lock wolfiLock) basePackages(version, arch string) ([]string, error) {
	if lock.Go != version {
		return wolfiPackages(version), nil
	}
	pkgs, ok := lock.Packages[arch]
	if !ok {
		return nil, fmt.Errorf("wolfi.lock.json has no packages for %s", arch)
	}
	return pkgs, nil
}
