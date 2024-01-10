package modules

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"dagger.io/dagger"
	"github.com/vektah/gqlparser/v2/ast"
)

// Filename is the name of the module config file.
const Filename = "dagger.json"

// Config is the dagger.json configuration of modules.
type Config struct {
	// TODO: doc
	Modules []*ModuleConfig `json:"modules,omitempty"`
}

// Use adds the given module references to the module's dependencies.
func (cfg *Config) Use(
	ctx context.Context,
	dag *dagger.Client,
	// the ref of the module in this config to add dependencies to
	dependerModRef *Ref,
	// the refs of the modules to add as dependencies
	depRefs ...string,
) error {
	cfgPath, err := dependerModRef.ConfigPath(ctx, dag)
	if err != nil {
		return fmt.Errorf("failed to get module config path for depender ref %q: %w", dependerModRef, err)
	}
	cfgDir := filepath.Dir(cfgPath)

	_, modSrcRelPath, err := dependerModRef.ModuleConfig(ctx, dag)
	if err != nil {
		return fmt.Errorf("failed to get module config: %w", err)
	}

	for _, modCfg := range cfg.Modules {
		if modCfg.Source != modSrcRelPath {
			continue
		}

		var deps []string
		deps = append(deps, modCfg.Dependencies...)
		deps = append(deps, depRefs...)
		depSet := make(map[string]*Ref)
		for _, dep := range deps {
			depRef, err := ResolveMovingRef(ctx, dag, dependerModRef, dep)
			if err != nil {
				return fmt.Errorf("failed to get dep ref: %w", err)
			}
			depSet[depRef.Symbolic()] = depRef
		}

		modCfg.Dependencies = nil
		for _, dep := range depSet {
			switch {
			case dep.Local != nil:
				depRelPath, err := filepath.Rel(cfgDir, dep.Local.ModuleSourcePath)
				if err != nil {
					return fmt.Errorf("failed to get module path relative to config: %w", err)
				}

				modCfg.Dependencies = append(modCfg.Dependencies, depRelPath)
			case dep.Git != nil:
				modCfg.Dependencies = append(modCfg.Dependencies, dep.String())
			default:
				return fmt.Errorf("invalid module dependency %q", dep)
			}
		}
		sort.Strings(modCfg.Dependencies)

		return nil
	}

	return fmt.Errorf("no module config for path %q from ref %q", modSrcRelPath, dependerModRef)
}

func (cfg *Config) ModuleConfigByPath(sourcePath string) (*ModuleConfig, error) {
	sourcePath = strings.TrimPrefix(sourcePath, "./")
	for _, modCfg := range cfg.Modules {
		if modCfg.Source == sourcePath {
			return modCfg, nil
		}
	}
	return nil, fmt.Errorf("no module config for path %q", sourcePath)
}

type ModuleConfig struct {
	Name string `json:"name" field:"true" doc:"The name of the module."`

	Source string `json:"source" field:"true" doc:"The directory containing the module's source code, relative to dagger.json"`

	SDK string `json:"sdk" field:"true" doc:"Either the name of a built-in SDK ('go', 'python', etc.) OR a module reference pointing to the SDK's module implementation."`

	Include []string `json:"include,omitempty" field:"true" doc:"Include only these file globs when loading the module root."`

	Exclude []string `json:"exclude,omitempty" field:"true" doc:"Exclude these file globs when loading the module root."`

	Dependencies []string `json:"dependencies,omitempty" field:"true" doc:"Modules that this module depends on."`

	Root string `json:"root,omitempty"`
}

func (modCfg *ModuleConfig) Type() *ast.Type {
	return &ast.Type{
		NamedType: "ModuleConfig",
		NonNull:   true,
	}
}

func (cfg *Config) TypeDescription() string {
	return "Static configuration for a module as parsed from its entry in a dagger.json config"
}

// NormalizeConfigPath appends /dagger.json to the given path if it is not
// already present.
func NormalizeConfigPath(configPath string) string {
	// figure out if we were passed a path to a dagger.json file
	// or a parent dir that may contain such a file
	baseName := filepath.Base(configPath)
	if baseName == Filename {
		return configPath
	}
	return filepath.Join(configPath, Filename)
}
