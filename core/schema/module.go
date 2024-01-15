package schema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/core/modules"
	"github.com/dagger/dagger/dagql"
	"golang.org/x/sync/errgroup"
)

type moduleSchema struct {
	dag *dagql.Server
}

var _ SchemaResolvers = &moduleSchema{}

func (s *moduleSchema) Install() {
	dagql.Fields[*core.Query]{
		dagql.Func("module", s.module).
			Doc(`Create a new module.`),

		dagql.Func("typeDef", s.typeDef).
			Doc(`Create a new TypeDef.`),

		dagql.Func("generatedCode", s.generatedCode).
			Doc(`Create a code generation result, given a directory containing the generated code.`),

		// TODO:
		// TODO:
		// TODO: doc
		dagql.Func("moduleRef", s.moduleRef).
			Doc(`TODO`),

		dagql.Func("function", s.function).
			Doc(`Creates a function.`).
			ArgDoc("name", `Name of the function, in its original format from the implementation language.`).
			ArgDoc("returnType", `Return type of the function.`),

		dagql.Func("currentModule", s.currentModule).
			Impure(`Changes depending on which module is calling it.`).
			Doc(`The module currently being served in the session, if any.`),

		dagql.Func("currentTypeDefs", s.currentTypeDefs).
			Impure(`Changes depending on which modules are currently installed.`).
			Doc(`The TypeDef representations of the objects currently being served in the session.`),

		dagql.Func("currentFunctionCall", s.currentFunctionCall).
			Impure(`Changes depending on which function calls it.`).
			Doc(`The FunctionCall context that the SDK caller is currently executing in.`,
				`If the caller is not currently executing in a function, this will
				return an error.`),
	}.Install(s.dag)

	dagql.Fields[*core.Directory]{
		dagql.NodeFunc("asModule", s.directoryAsModule).
			Doc(`Load the directory as a Dagger module`).
			ArgDoc("sourceSubpath",
				`An optional subpath of the directory which contains the module's source code.`,
				`This is needed when the module code is in a subdirectory but requires
				parent directories to be loaded in order to execute. For example, the
				module source code may need a go.mod, project.toml, package.json, etc.
				file from a parent directory.`,
				`If not set, the module source code is loaded from the root of the directory.`),
	}.Install(s.dag)

	dagql.Fields[*core.FunctionCall]{
		dagql.Func("returnValue", s.functionCallReturnValue).
			Impure(`Updates internal engine state with the given value.`).
			Doc(`Set the return value of the function call to the provided value.`).
			ArgDoc("value", `JSON serialization of the return value.`),
	}.Install(s.dag)

	dagql.Fields[*core.ModuleRef]{
		// TODO: arg doc
		// TODO: doc
		dagql.NodeFunc("loadModule", s.moduleRefLoadModule).
			Doc(`TODO`),
	}.Install(s.dag)

	dagql.Fields[*core.ModuleDependency]{}.Install(s.dag)

	dagql.Fields[*core.ModuleConfig]{
		dagql.Func("withName", s.moduleConfigWithName).
			Doc(`Set the name of the module in the configuration.`).
			ArgDoc("name", `The name of the module.`),

		dagql.Func("withSDK", s.moduleConfigWithSDK).
			Doc(`Set the SDK of the module in the configuration.`).
			ArgDoc("sdk", `The ref of the SDK.`),

		dagql.NodeFunc("directory", s.moduleConfigDirectory).
			Doc(`The directory this module config was loaded from plus any configuration changes and generated module source code`),
	}.Install(s.dag)

	dagql.Fields[*core.Module]{
		dagql.NodeFunc("withSource", s.moduleWithSource).
			Doc(`Retrieves the module with basic configuration loaded, ready for initialization.`).
			ArgDoc("directory", `The directory containing the module's configuration and source code (possibly as subdirs).`).
			ArgDoc("sourceSubpath",
				`The subpath of the directory which contains the module's source code.`),

		dagql.NodeFunc("initialize", s.moduleInitialize).
			Doc(`Retrieves the module with the objects loaded via its SDK.`),

		dagql.Func("withObject", s.moduleWithObject).
			Doc(`This module plus the given Object type and associated functions.`),

		dagql.Func("withInterface", s.moduleWithInterface).
			Doc(`This module plus the given Interface type and associated functions`),

		dagql.NodeFunc("serve", s.moduleServe).
			Impure(`Mutates the calling session's global schema.`).
			Doc(`Serve a module's API in the current session.`,
				`Note: this can only be called once per session. In the future, it could return a stream or service to remove the side effect.`),
	}.Install(s.dag)

	dagql.Fields[*core.Function]{
		dagql.Func("withDescription", s.functionWithDescription).
			Doc(`Returns the function with the given doc string.`).
			ArgDoc("description", `The doc string to set.`),

		dagql.Func("withArg", s.functionWithArg).
			Doc(`Returns the function with the provided argument`).
			ArgDoc("name", `The name of the argument`).
			ArgDoc("typeDef", `The type of the argument`).
			ArgDoc("description", `A doc string for the argument, if any`).
			ArgDoc("defaultValue", `A default value to use for this argument if not explicitly set by the caller, if any`),
	}.Install(s.dag)

	dagql.Fields[*core.FunctionArg]{}.Install(s.dag)

	dagql.Fields[*core.FunctionCallArgValue]{}.Install(s.dag)

	dagql.Fields[*core.TypeDef]{
		dagql.Func("withOptional", s.typeDefWithOptional).
			Doc(`Sets whether this type can be set to null.`),

		dagql.Func("withKind", s.typeDefWithKind).
			Doc(`Sets the kind of the type.`),

		dagql.Func("withListOf", s.typeDefWithListOf).
			Doc(`Returns a TypeDef of kind List with the provided type for its elements.`),

		dagql.Func("withObject", s.typeDefWithObject).
			Doc(`Returns a TypeDef of kind Object with the provided name.`,
				`Note that an object's fields and functions may be omitted if the
				intent is only to refer to an object. This is how functions are able to
				return their own object, or any other circular reference.`),

		dagql.Func("withInterface", s.typeDefWithInterface).
			Doc(`Returns a TypeDef of kind Interface with the provided name.`),

		dagql.Func("withField", s.typeDefWithObjectField).
			Doc(`Adds a static field for an Object TypeDef, failing if the type is not an object.`).
			ArgDoc("name", `The name of the field in the object`).
			ArgDoc("typeDef", `The type of the field`).
			ArgDoc("description", `A doc string for the field, if any`),

		dagql.Func("withFunction", s.typeDefWithFunction).
			Doc(`Adds a function for an Object or Interface TypeDef, failing if the type is not one of those kinds.`),

		dagql.Func("withConstructor", s.typeDefWithObjectConstructor).
			Doc(`Adds a function for constructing a new instance of an Object TypeDef, failing if the type is not an object.`),
	}.Install(s.dag)

	dagql.Fields[*core.ObjectTypeDef]{}.Install(s.dag)
	dagql.Fields[*core.InterfaceTypeDef]{}.Install(s.dag)
	dagql.Fields[*core.FieldTypeDef]{}.Install(s.dag)
	dagql.Fields[*core.ListTypeDef]{}.Install(s.dag)

	dagql.Fields[*core.GeneratedCode]{
		dagql.Func("withVCSIgnoredPaths", s.generatedCodeWithVCSIgnoredPaths).
			Doc(`Set the list of paths to ignore in version control.`),
		dagql.Func("withVCSGeneratedPaths", s.generatedCodeWithVCSGeneratedPaths).
			Doc(`Set the list of paths to mark generated in version control.`),
	}.Install(s.dag)
}

func (s *moduleSchema) typeDef(ctx context.Context, _ *core.Query, args struct{}) (*core.TypeDef, error) {
	return &core.TypeDef{}, nil
}

func (s *moduleSchema) typeDefWithOptional(ctx context.Context, def *core.TypeDef, args struct {
	Optional bool
}) (*core.TypeDef, error) {
	return def.WithOptional(args.Optional), nil
}

func (s *moduleSchema) typeDefWithKind(ctx context.Context, def *core.TypeDef, args struct {
	Kind core.TypeDefKind
}) (*core.TypeDef, error) {
	return def.WithKind(args.Kind), nil
}

func (s *moduleSchema) typeDefWithListOf(ctx context.Context, def *core.TypeDef, args struct {
	ElementType core.TypeDefID
}) (*core.TypeDef, error) {
	elemType, err := args.ElementType.Load(ctx, s.dag)
	if err != nil {
		return nil, fmt.Errorf("failed to decode element type: %w", err)
	}
	return def.WithListOf(elemType.Self), nil
}

func (s *moduleSchema) typeDefWithObject(ctx context.Context, def *core.TypeDef, args struct {
	Name        string
	Description string `default:""`
}) (*core.TypeDef, error) {
	if args.Name == "" {
		return nil, fmt.Errorf("object type def must have a name")
	}
	return def.WithObject(args.Name, args.Description), nil
}

func (s *moduleSchema) typeDefWithInterface(ctx context.Context, def *core.TypeDef, args struct {
	Name        string
	Description string `default:""`
}) (*core.TypeDef, error) {
	return def.WithInterface(args.Name, args.Description), nil
}

func (s *moduleSchema) typeDefWithObjectField(ctx context.Context, def *core.TypeDef, args struct {
	Name        string
	TypeDef     core.TypeDefID
	Description string `default:""`
}) (*core.TypeDef, error) {
	fieldType, err := args.TypeDef.Load(ctx, s.dag)
	if err != nil {
		return nil, fmt.Errorf("failed to decode element type: %w", err)
	}
	return def.WithObjectField(args.Name, fieldType.Self, args.Description)
}

func (s *moduleSchema) typeDefWithFunction(ctx context.Context, def *core.TypeDef, args struct {
	Function core.FunctionID
}) (*core.TypeDef, error) {
	fn, err := args.Function.Load(ctx, s.dag)
	if err != nil {
		return nil, fmt.Errorf("failed to decode element type: %w", err)
	}
	return def.WithFunction(fn.Self)
}

func (s *moduleSchema) typeDefWithObjectConstructor(ctx context.Context, def *core.TypeDef, args struct {
	Function core.FunctionID
}) (*core.TypeDef, error) {
	inst, err := args.Function.Load(ctx, s.dag)
	if err != nil {
		return nil, fmt.Errorf("failed to decode element type: %w", err)
	}
	fn := inst.Self.Clone()
	// Constructors are invoked by setting the ObjectName to the name of the object its constructing and the
	// FunctionName to "", so ignore the name of the function.
	fn.Name = ""
	fn.OriginalName = ""
	return def.WithObjectConstructor(fn)
}

func (s *moduleSchema) generatedCode(ctx context.Context, _ *core.Query, args struct {
	Code core.DirectoryID
}) (*core.GeneratedCode, error) {
	dir, err := args.Code.Load(ctx, s.dag)
	if err != nil {
		return nil, err
	}
	return core.NewGeneratedCode(dir.Self), nil
}

func (s *moduleSchema) generatedCodeWithVCSIgnoredPaths(ctx context.Context, code *core.GeneratedCode, args struct {
	Paths []string
}) (*core.GeneratedCode, error) {
	return code.WithVCSIgnoredPaths(args.Paths), nil
}

func (s *moduleSchema) generatedCodeWithVCSGeneratedPaths(ctx context.Context, code *core.GeneratedCode, args struct {
	Paths []string
}) (*core.GeneratedCode, error) {
	return code.WithVCSGeneratedPaths(args.Paths), nil
}

func (s *moduleSchema) module(ctx context.Context, query *core.Query, _ struct{}) (*core.Module, error) {
	return query.NewModule(), nil
}

func (s *moduleSchema) function(ctx context.Context, _ *core.Query, args struct {
	Name       string
	ReturnType core.TypeDefID
}) (*core.Function, error) {
	returnType, err := args.ReturnType.Load(ctx, s.dag)
	if err != nil {
		return nil, fmt.Errorf("failed to decode return type: %w", err)
	}
	return core.NewFunction(args.Name, returnType.Self), nil
}

func (s *moduleSchema) functionWithDescription(ctx context.Context, fn *core.Function, args struct {
	Description string
}) (*core.Function, error) {
	return fn.WithDescription(args.Description), nil
}

func (s *moduleSchema) functionWithArg(ctx context.Context, fn *core.Function, args struct {
	Name         string
	TypeDef      core.TypeDefID
	Description  string    `default:""`
	DefaultValue core.JSON `default:""`
}) (*core.Function, error) {
	argType, err := args.TypeDef.Load(ctx, s.dag)
	if err != nil {
		return nil, fmt.Errorf("failed to decode arg type: %w", err)
	}
	return fn.WithArg(args.Name, argType.Self, args.Description, args.DefaultValue), nil
}

type moduleWithSourceArgs struct {
	Directory           core.DirectoryID
	SourceDirectoryPath string
}

func (s *moduleSchema) moduleWithSource(
	ctx context.Context,
	self dagql.Instance[*core.Module],
	args moduleWithSourceArgs,
) (mod dagql.Instance[*core.Module], rerr error) {
	configDir, err := args.Directory.Load(ctx, s.dag)
	if err != nil {
		return mod, fmt.Errorf("failed to decode source directory: %w", err)
	}

	// make the path absolute in case it was provided as a relative path, which is
	// ok because we treat it as relative to the root of the provided dir
	args.SourceDirectoryPath = filepath.Join("/", args.SourceDirectoryPath)

	cfgPath, cfg, ok, err := findDirModuleConfig(ctx, configDir.Self, args.SourceDirectoryPath)
	if err != nil {
		return mod, fmt.Errorf("error while finding module config: %w", err)
	}
	if !ok {
		// No dagger.json found, need to initialize a new one. Default to the root.
		cfgPath = filepath.Join("/", modules.Filename)
		cfg = &core.ModulesConfig{}
	}

	cfgDirPath := filepath.Dir(cfgPath)

	// Reposition the root of the dir in case it's pointing to a subdir
	if cfgDirPath != "/" {
		err = s.dag.Select(ctx, configDir, &configDir, dagql.Selector{
			Field: "directory",
			Args: []dagql.NamedInput{
				{Name: "path", Value: dagql.String(cfgDirPath)},
			},
		})
		if err != nil {
			return mod, fmt.Errorf("failed to reroot config directory: %w", err)
		}

		srcDirRelPath, err := filepath.Rel(cfgDirPath, args.SourceDirectoryPath)
		if err != nil {
			return mod, fmt.Errorf("failed to get module source path relative to config: %w", err)
		}
		args.SourceDirectoryPath = filepath.Join("/", srcDirRelPath)
		cfgDirPath = "/"
		cfgPath = filepath.Join(cfgDirPath, modules.Filename)
	}

	modCfg, ok, err := cfg.ModuleConfigByPath(args.SourceDirectoryPath)
	if err != nil {
		return mod, fmt.Errorf("failed to get module config by path: %w", err)
	}
	if !ok {
		// no config for this module found, need to initialize a new one
		modSourceRelPath, err := filepath.Rel(cfgDirPath, args.SourceDirectoryPath)
		if err != nil {
			return mod, fmt.Errorf("failed to get module source path relative to config: %w", err)
		}
		modCfg = &core.ModuleConfig{
			Source: modSourceRelPath,
		}
	}

	mod = self
	mod.Self = mod.Self.Clone()
	mod.Self.NameField = modCfg.Name
	mod.Self.SDKConfig = modCfg.SDK
	mod.Self.DependencyConfig = modCfg.Dependencies

	//
	//
	//
	//
	//
	//

	configDir, err := args.Directory.Load(ctx, s.dag)
	if err != nil {
		return nil, fmt.Errorf("failed to decode source directory: %w", err)
	}

	args.Subpath = filepath.Join("/", args.Subpath)
	cfgPath, modCfg, ok, err := core.LoadModuleConfig(ctx, configDir, args.Subpath)
	if err != nil {
		return nil, fmt.Errorf("failed to load module config: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("no module config found at %q", cfgPath)
	}
	cfgDirPath := filepath.Dir(cfgPath)

	modSourceRelPath, err := filepath.Rel(cfgDirPath, args.Subpath)
	if err != nil {
		return nil, fmt.Errorf("failed to get module source path relative to config: %w", err)
	}

	// Reposition the root of the dir in case it's pointing to a subdir
	if cfgDirPath != "." && cfgDirPath != "/" {
		err = s.dag.Select(ctx, configDir, &configDir, dagql.Selector{
			Field: "directory",
			Args: []dagql.NamedInput{
				{Name: "path", Value: dagql.String(cfgDirPath)},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get config directory: %w", err)
		}
	}

	// TODO: in theory could just select directly using nth now probably?
	var eg errgroup.Group
	deps := make([]dagql.Instance[*core.Module], len(modCfg.Dependencies))
	for i, depRef := range modCfg.Dependencies {
		i, depRef := i, depRef
		eg.Go(func() error {
			return s.dag.Select(ctx, depRef, &deps[i],
				dagql.Selector{
					Field: "loadModule",
					Args: []dagql.NamedInput{
						{Name: "configDir", Value: dagql.NewID[*core.Directory](self.ConfigDirectory.ID())},
					},
				},
			)
		})
	}
	if err := eg.Wait(); err != nil {
		if errors.Is(err, dagql.ErrCacheMapRecursiveCall) {
			err = fmt.Errorf("module %s has a circular dependency: %w", modCfg.Name, err)
		}
		return nil, err
	}

	self = self.Clone()
	self.NameField = modCfg.Name
	self.DependencyConfig = modCfg.Dependencies
	self.SDKConfig = modCfg.SDK
	self.ConfigDirectory = configDir
	self.SourceDirectorySubpath = modSourceRelPath
	self.DependenciesField = deps

	self.Deps = core.NewModDeps(self.Query, self.Dependencies()).
		Append(self.Query.DefaultDeps.Mods...)

	sdk, err := s.sdkForModule(ctx, self.Query, modCfg.SDK, configDir)
	if err != nil {
		return nil, err
	}

	self.GeneratedCode, err = sdk.Codegen(ctx, self, configDir, modSourceRelPath)
	if err != nil {
		return nil, err
	}

	self.Runtime, err = sdk.Runtime(ctx, self, configDir, modSourceRelPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get module runtime: %w", err)
	}

	return self, nil
}

func (s *moduleSchema) moduleInitialize(ctx context.Context, inst dagql.Instance[*core.Module], args struct{}) (*core.Module, error) {
	return inst.Self.Initialize(ctx, inst, dagql.CurrentID(ctx))
}

type asModuleArgs struct {
	SourceSubpath string `default:""`
}

func (s *moduleSchema) directoryAsModule(ctx context.Context, sourceDir dagql.Instance[*core.Directory], args asModuleArgs) (inst dagql.Instance[*core.Module], rerr error) {
	rerr = s.dag.Select(ctx, s.dag.Root(), &inst, dagql.Selector{
		Field: "module",
	}, dagql.Selector{
		Field: "withSource",
		Args: []dagql.NamedInput{
			{Name: "directory", Value: dagql.NewID[*core.Directory](sourceDir.ID())},
			{Name: "subpath", Value: dagql.String(args.SourceSubpath)},
		},
	}, dagql.Selector{
		Field: "initialize",
	})
	return
}

func (s *moduleSchema) currentModule(ctx context.Context, self *core.Query, _ struct{}) (inst dagql.Instance[*core.Module], err error) {
	id, err := self.CurrentModule(ctx)
	if err != nil {
		return inst, err
	}
	return id.Load(ctx, s.dag)
}

func (s *moduleSchema) currentFunctionCall(ctx context.Context, self *core.Query, _ struct{}) (*core.FunctionCall, error) {
	return self.CurrentFunctionCall(ctx)
}

func (s *moduleSchema) moduleServe(ctx context.Context, modMeta dagql.Instance[*core.Module], _ struct{}) (dagql.Nullable[core.Void], error) {
	return dagql.Null[core.Void](), modMeta.Self.Query.ServeModuleToMainClient(ctx, modMeta)
}

func (s *moduleSchema) currentTypeDefs(ctx context.Context, self *core.Query, _ struct{}) ([]*core.TypeDef, error) {
	deps, err := self.CurrentServedDeps(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current module: %w", err)
	}
	return deps.TypeDefs(ctx)
}

func (s *moduleSchema) functionCallReturnValue(ctx context.Context, fnCall *core.FunctionCall, args struct {
	Value core.JSON
}) (dagql.Nullable[core.Void], error) {
	// TODO: error out if caller is not coming from a module
	return dagql.Null[core.Void](), fnCall.ReturnValue(ctx, args.Value)
}

func (s *moduleSchema) moduleWithObject(ctx context.Context, modMeta *core.Module, args struct {
	Object core.TypeDefID
}) (_ *core.Module, rerr error) {
	def, err := args.Object.Load(ctx, s.dag)
	if err != nil {
		return nil, err
	}
	return modMeta.WithObject(ctx, def.Self)
}

func (s *moduleSchema) moduleWithInterface(ctx context.Context, modMeta *core.Module, args struct {
	Iface core.TypeDefID
}) (_ *core.Module, rerr error) {
	def, err := args.Iface.Load(ctx, s.dag)
	if err != nil {
		return nil, err
	}
	return modMeta.WithInterface(ctx, def.Self)
}

type moduleRefArgs struct {
	Ref string
}

func (s *moduleSchema) moduleRef(ctx context.Context, query *core.Query, args moduleRefArgs) (*core.ModuleRef, error) {
	// TODO: add "stable" bool arg

	modPath, modVersion, hasVersion := strings.Cut(args.Ref, "@")

	isGitHub := strings.HasPrefix(modPath, "github.com/")

	if !hasVersion && !isGitHub {
		// assume local path
		// NB(vito): HTTP URLs should be supported by taking a sha256 digest as the
		// version. so it should be safe to assume no version = local path. as a
		// rule, if it's local we don't need to version it; if it's remote, we do.

		callerWorkdirStat, err := query.Buildkit.StatCallerHostPath(ctx, ".")
		if err != nil {
			return nil, fmt.Errorf("failed to stat caller workdir: %w", err)
		}
		callerWorkdirPath := callerWorkdirStat.Path

		if filepath.IsAbs(modPath) {
			modPath, err = filepath.Rel(callerWorkdirPath, modPath)
			if err != nil {
				return nil, fmt.Errorf("failed to make module path relative to caller workdir: %w", err)
			}
		}
		if strings.HasPrefix(modPath, "../") {
			return nil, fmt.Errorf("module path %q is outside of caller workdir %q", modPath, callerWorkdirPath)
		}

		return &core.ModuleRef{
			Kind:             core.ModuleRefKindLocal,
			ModuleSourcePath: filepath.Join("/", modPath),
		}, nil
	}

	// assume git ref
	ref := &core.ModuleRef{
		Kind: core.ModuleRefKindGit,
	}

	if !isGitHub {
		return nil, fmt.Errorf("for now, only github.com/ paths are supported: %q", args.Ref)
	}

	segments := strings.SplitN(modPath, "/", 4)
	if len(segments) < 3 {
		return nil, fmt.Errorf("invalid github.com path: %s", modPath)
	}

	ref.URLParent = segments[0] + "/" + segments[1] + "/" + segments[2]

	cloneURL, err := ref.GitCloneURL()
	if err != nil {
		return nil, fmt.Errorf("failed to get git clone url: %w", err)
	}

	if !hasVersion {
		var err error
		modVersion, err = defaultBranch(ctx, cloneURL)
		if err != nil {
			return nil, fmt.Errorf("determine default branch: %w", err)
		}
	}

	var gitCommitInst dagql.Instance[dagql.String]
	err = s.dag.Select(ctx, s.dag.Root(), &gitCommitInst,
		dagql.Selector{
			Field: "git",
			Args: []dagql.NamedInput{
				{Name: "url", Value: dagql.String(cloneURL)},
			},
		},
		dagql.Selector{
			Field: "commit",
			Args: []dagql.NamedInput{
				{Name: "id", Value: dagql.String(modVersion)},
			},
		},
		dagql.Selector{
			Field: "commit",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve git ref to commit: %w", err)
	}
	gitCommit := string(gitCommitInst.Self)

	ref.Version = gitCommit // TODO preserve semver here
	ref.Commit = gitCommit  // but tell the truth here

	if len(segments) == 4 {
		ref.ModuleSourcePath = segments[3]
	} else {
		ref.ModuleSourcePath = "/"
	}

	var gitDir dagql.Instance[*core.Directory]
	err = s.dag.Select(ctx, s.dag.Root(), &gitDir,
		dagql.Selector{
			Field: "git",
			Args: []dagql.NamedInput{
				{Name: "url", Value: dagql.String(cloneURL)},
			},
		},
		dagql.Selector{
			Field: "commit",
			Args: []dagql.NamedInput{
				{Name: "id", Value: dagql.String(modVersion)},
			},
		},
		dagql.Selector{
			Field: "tree",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load git dir: %w", err)
	}

	cfgPath, _, ok, err := findDirModuleConfig(ctx, gitDir.Self, ref.ModuleSourcePath)
	if err != nil {
		return nil, fmt.Errorf("error while finding module config: %w", err)
	}
	if !ok {
		// No dagger.json yet, for now error out because this is a git dir.
		// In theory this could be allowed if use cases for initializing modules
		// starting from existing "undaggerized" git repos come up.
		return nil, fmt.Errorf("no module config found at %q", cfgPath)
	}
	ref.ModuleConfigDirPath = filepath.Dir(cfgPath)

	return ref, nil
}

/* TODO: rm
type moduleRefLoadModuleArgs struct {
	ConfigDir dagql.Optional[core.DirectoryID]
}
*/

func (s *moduleSchema) moduleRefLoadModule(
	ctx context.Context,
	ref dagql.Instance[*core.ModuleRef],
	args struct{},
) (mod dagql.Instance[*core.Module], rerr error) {
	var dirID dagql.ID[*core.Directory]
	switch ref.Self.Kind {
	case core.ModuleRefKindLocal:
		if !args.ConfigDir.Valid {
			return mod, fmt.Errorf("must provide config directory for local module")
		}
		dirID = args.ConfigDir.Value

	case core.ModuleRefKindGit:
		cloneURL, err := ref.Self.GitCloneURL()
		if err != nil {
			return mod, fmt.Errorf("failed to get git clone url: %w", err)
		}

		var gitDir dagql.Instance[*core.Directory]
		err = s.dag.Select(ctx, s.dag.Root(), &gitDir,
			dagql.Selector{
				Field: "git",
				Args: []dagql.NamedInput{
					{Name: "url", Value: dagql.String(cloneURL)},
				},
			},
			dagql.Selector{
				Field: "commit",
				Args: []dagql.NamedInput{
					{Name: "id", Value: dagql.String(ref.Self.Version)},
				},
			},
			dagql.Selector{
				Field: "tree",
			},
		)
		if err != nil {
			return mod, fmt.Errorf("failed to load git dir: %w", err)
		}

		dirID = dagql.NewID[*core.Directory](gitDir.ID())

	default:
		return mod, fmt.Errorf("invalid module ref kind: %s", ref.Self.Kind)
	}

	rerr = s.dag.Select(ctx, s.dag.Root(), &mod,
		dagql.Selector{
			Field: "module",
		},
		dagql.Selector{
			Field: "withSource",
			Args: []dagql.NamedInput{
				{Name: "directory", Value: dirID},
				{Name: "subpath", Value: dagql.String(ref.Self.ModuleSourcePath)},
			},
		},
	)
	if rerr != nil {
		return mod, fmt.Errorf("failed to load local module ref: %w", rerr)
	}
	return mod, nil
}

/* TODO: rm
type moduleRefLoadConfigArgs struct {
	ConfigDir dagql.Optional[core.DirectoryID]
}

func (s *moduleSchema) moduleRefLoadConfig(
	ctx context.Context,
	ref dagql.Instance[*core.ModuleRef],
	args moduleRefLoadConfigArgs,
) (modCfg dagql.Instance[*core.ModuleConfig], rerr error) {
	var configDir dagql.Instance[*core.Directory]
	switch ref.Self.Kind {
	case core.ModuleRefKindLocal:
		if !args.ConfigDir.Valid {
			return modCfg, fmt.Errorf("must provide config directory for local module")
		}
		configDir, rerr = args.ConfigDir.Value.Load(ctx, s.dag)
		if rerr != nil {
			return modCfg, fmt.Errorf("failed to decode config directory: %w", rerr)
		}

	case core.ModuleRefKindGit:
		cloneURL, err := ref.Self.GitCloneURL()
		if err != nil {
			return modCfg, fmt.Errorf("failed to get git clone url: %w", err)
		}

		err = s.dag.Select(ctx, s.dag.Root(), &configDir,
			dagql.Selector{
				Field: "git",
				Args: []dagql.NamedInput{
					{Name: "url", Value: dagql.String(cloneURL)},
				},
			},
			dagql.Selector{
				Field: "commit",
				Args: []dagql.NamedInput{
					{Name: "id", Value: dagql.String(ref.Self.Version)},
				},
			},
			dagql.Selector{
				Field: "tree",
			},
		)
		if err != nil {
			return modCfg, fmt.Errorf("failed to load git module ref: %w", err)
		}

	default:
		return modCfg, fmt.Errorf("invalid module ref kind: %s", ref.Self.Kind)
	}

	return modCfg, nil

	//
	//
	//
	//
	//
	//

	dir, err := args.Directory.Load(ctx, s.dag)
	if err != nil {
		return nil, fmt.Errorf("failed to decode source directory: %w", err)
	}

	// make the path absolute in case it was provided as a relative path, which is
	// ok because we treat it as relative to the root of the provided dir
	args.SourceDirectoryPath = filepath.Join("/", args.SourceDirectoryPath)

	cfgPath, cfg, ok, err := core.LoadModuleConfig(ctx, dir, args.SourceDirectoryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load module config: %w", err)
	}
	if ok {
		return cfg, nil
	}

	// need to initialize a new empty config for the module

	var cfgDirPath string
	if cfgPath != "" {
		cfgDirPath = filepath.Dir(cfgPath)
	} else {
		// there wasn't even a dagger.json found, much less configuration for module
		// at SourceDirectoryPath. Default it to the root of the directory.
		cfgDirPath = "/"
	}

	modSourceRelPath, err := filepath.Rel(cfgDirPath, args.SourceDirectoryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get module source path relative to config: %w", err)
	}

	return &core.ModuleConfig{
		ModuleConfig: &modules.ModuleConfig{
			Source: modSourceRelPath,
		},
		Query:            query,
		LoadedDirectory:  dir,
		ConfigDirPath:    cfgDirPath,
		ModSourceDirPath: args.SourceDirectoryPath,
	}, nil
}
*/

func (s *moduleSchema) moduleConfigWithName(ctx context.Context, modCfg *core.ModuleConfig, args struct {
	Name string
}) (_ *core.ModuleConfig, rerr error) {
	modCfg = modCfg.Clone()
	if modCfg.ModuleConfig == nil {
		modCfg.ModuleConfig = &modules.ModuleConfig{}
	}
	modCfg.Name = args.Name
	return modCfg, nil
}

func (s *moduleSchema) moduleConfigWithSDK(ctx context.Context, modCfg *core.ModuleConfig, args struct {
	SDK string
}) (_ *core.ModuleConfig, rerr error) {
	modCfg = modCfg.Clone()
	if modCfg.ModuleConfig == nil {
		modCfg.ModuleConfig = &modules.ModuleConfig{}
	}
	modCfg.SDK = args.SDK
	return modCfg, nil
}

func (s *moduleSchema) moduleConfigWithDependency(ctx context.Context, modCfg dagql.Instance[*core.ModuleConfig], args struct {
	DependencyRefs []string
}) (inst dagql.Instance[*core.ModuleConfig], rerr error) {
	modCfg.Self = modCfg.Self.Clone()
	if modCfg.Self.ModuleConfig == nil {
		modCfg.Self.ModuleConfig = &modules.ModuleConfig{}
	}

	var allDepRefsStrs []string
	allDepRefsStrs = append(allDepRefsStrs, modCfg.Self.Dependencies...)
	allDepRefsStrs = append(allDepRefsStrs, args.DependencyRefs...)
	depRefStrSet := make(map[string]*modules.Ref)
	for _, depRefStr := range allDepRefsStrs {
	}

	return modCfg, nil
}

func (s *moduleSchema) moduleConfigDirectory(ctx context.Context, modCfg dagql.Instance[*core.ModuleConfig], args struct{}) (inst dagql.Instance[*core.Directory], rerr error) {
	if modCfg.Self.Name == "" {
		return inst, fmt.Errorf("module name must be set")
	}
	if modCfg.Self.SDK == "" {
		return inst, fmt.Errorf("module sdk must be set")
	}

	// write the module config file, in case there were any updates
	cfgPath := modules.NormalizeConfigPath(modCfg.Self.ConfigDirPath)

	var cfg modules.Config
	cfgFile, err := modCfg.Self.LoadedDirectory.Self.File(ctx, cfgPath)
	// err is nil if the file exists already, in which case we should update it
	if err == nil {
		cfgBytes, err := cfgFile.Contents(ctx)
		if err != nil {
			return inst, fmt.Errorf("failed to get module config file contents: %w", err)
		}
		if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
			return inst, fmt.Errorf("failed to decode module config: %w", err)
		}
	}

	var found bool
	for i, maybeModCfg := range cfg.Modules {
		if maybeModCfg.Source == modCfg.Self.Source {
			cfg.Modules[i] = modCfg.Self.ModuleConfig
			found = true
			break
		}
	}
	if !found {
		cfg.Modules = append(cfg.Modules, modCfg.Self.ModuleConfig)
	}

	updatedCfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return inst, fmt.Errorf("failed to encode module config: %w", err)
	}

	// TODO: retain prev permissions + owner (or check what happens by default for owner, may be correct already)
	err = s.dag.Select(ctx, modCfg.Self.LoadedDirectory, &inst, dagql.Selector{
		Field: "withNewFile",
		Args: []dagql.NamedInput{
			{Name: "path", Value: dagql.String(cfgPath)},
			{Name: "contents", Value: dagql.String(string(updatedCfgBytes) + "\n")},
		},
	})
	if err != nil {
		return inst, fmt.Errorf("failed to write module config file: %w", err)
	}

	// write module generated code
	var genCodeSrcDir dagql.Instance[*core.Directory]
	err = s.dag.Select(ctx, s.dag.Root(), &genCodeSrcDir,
		dagql.Selector{
			Field: "module",
		},
		dagql.Selector{
			Field: "withSource",
			Args: []dagql.NamedInput{
				{Name: "directory", Value: dagql.NewID[*core.Directory](inst.ID())},
				{Name: "subpath", Value: dagql.String(modCfg.Self.ModSourceDirPath)},
			},
		},
		dagql.Selector{
			Field: "generatedCode",
		},
		dagql.Selector{
			Field: "code",
		},
	)
	if err != nil {
		return inst, fmt.Errorf("failed to get module: %w", err)
	}

	err = s.dag.Select(ctx, inst, &inst,
		dagql.Selector{
			Field: "withDirectory",
			Args: []dagql.NamedInput{
				{Name: "directory", Value: dagql.NewID[*core.Directory](genCodeSrcDir.ID())},
				{Name: "path", Value: dagql.String(modCfg.Self.ModSourceDirPath)},
			},
		},
	)
	if err != nil {
		return inst, fmt.Errorf("failed to get module: %w", err)
	}

	return inst, nil
}

func findDirModuleConfig(
	ctx context.Context,
	dir *core.Directory,
	curSubdir string,
) (string, *core.ModulesConfig, bool, error) {
	curSubdir = filepath.Clean(curSubdir)
	if curSubdir == "." {
		curSubdir = "/"
	}
	if !filepath.IsAbs(curSubdir) {
		return "", nil, false, fmt.Errorf("relative path %q is not supported", curSubdir)
	}

	configPath := filepath.Join(curSubdir, modules.Filename)
	configFile, err := dir.File(ctx, configPath)
	if err == nil {
		configBytes, err := configFile.Contents(ctx)
		if err != nil {
			return "", nil, false, fmt.Errorf("failed to read module config file: %w", err)
		}
		var cfg core.ModulesConfig
		if err := json.Unmarshal(configBytes, &cfg); err != nil {
			return "", nil, false, fmt.Errorf("failed to decode module config: %w", err)
		}
		return configPath, &cfg, true, nil
	}

	if curSubdir == "/" {
		// we reached the module root; time to give up
		return "", nil, false, nil
	}

	return findDirModuleConfig(ctx, dir, filepath.Dir(curSubdir))
}
