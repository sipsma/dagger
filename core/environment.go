package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/dagger/dagger/core/pipeline"
	"github.com/dagger/dagger/router"
	"github.com/iancoleman/strcase"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
)

const (
	schemaPath = "/schema.graphql"

	inputMountPath = "/inputs"
	inputFile      = "/dagger.json"

	outputMountPath = "/outputs"
	outputFile      = "/dagger.json"
)

type EnvironmentSDK string

const (
	EnvironmentSDKGo     EnvironmentSDK = "go"
	EnvironmentSDKPython EnvironmentSDK = "python"
)

type EnvironmentID string

func (id EnvironmentID) String() string {
	return string(id)
}

func (id EnvironmentID) ToEnvironment() (*Environment, error) {
	var environment Environment
	if id == "" {
		return &environment, nil
	}
	if err := decodeID(&environment, id); err != nil {
		return nil, err
	}
	return &environment, nil
}

type EnvironmentCommandID string

func (id EnvironmentCommandID) String() string {
	return string(id)
}

func (id EnvironmentCommandID) ToEnvironmentCommand() (*EnvironmentCommand, error) {
	var environmentCommand EnvironmentCommand
	if id == "" {
		return &environmentCommand, nil
	}
	if err := decodeID(&environmentCommand, id); err != nil {
		return nil, err
	}
	return &environmentCommand, nil
}

type Environment struct {
	// The environment's root directory
	Directory *Directory `json:"directory"`
	// Path to the environment's config file relative to the root directory
	ConfigPath string `json:"configPath"`
	// The parsed environment config
	Config EnvironmentConfig `json:"config"`
	// The graphql schema for the environment
	Schema string `json:"schema"`
	// The environment's platform
	Platform specs.Platform `json:"platform,omitempty"`
	// TODO:
	Commands []*EnvironmentCommand `json:"commands,omitempty"`
}

type EnvironmentConfig struct {
	Root string `json:"root"`
	Name string `json:"name"`
	SDK  string `json:"sdk,omitempty"`
}

func NewEnvironment(id EnvironmentID, platform specs.Platform) (*Environment, error) {
	environment, err := id.ToEnvironment()
	if err != nil {
		return nil, err
	}
	environment.Platform = platform
	return environment, nil
}

func (env *Environment) ID() (EnvironmentID, error) {
	return encodeID[EnvironmentID](env)
}

func (env *Environment) Clone() *Environment {
	cp := *env
	if env.Directory != nil {
		cp.Directory = env.Directory.Clone()
	}
	return &cp
}

func (env *Environment) Load(
	ctx context.Context,
	gw *GatewayClient,
	r *router.Router,
	progSock *Socket,
	pipeline pipeline.Path,
	source *Directory,
	configPath string,
	sessionID string,
) (*Environment, error) {
	env = env.Clone()
	env.Directory = source

	configPath = env.normalizeConfigPath(configPath)
	env.ConfigPath = configPath

	configFile, err := source.File(ctx, env.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load environment config at path %q: %w", configPath, err)
	}
	cfgBytes, err := configFile.Contents(ctx, gw, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to read environment config at path %q: %w", configPath, err)
	}
	if err := json.Unmarshal(cfgBytes, &env.Config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal environment config: %w", err)
	}

	ctr, err := env.runtime(ctx, gw, progSock, pipeline, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get runtime container for schema: %w", err)
	}
	ctr, err = ctr.WithMountedDirectory(ctx, gw, outputMountPath, NewScratchDirectory(pipeline, env.Platform), "", sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to mount output directory: %w", err)
	}

	ctr, err = ctr.WithExec(ctx, gw, progSock, env.Platform, ContainerExecOpts{
		Args:                          []string{"-schema"},
		ExperimentalPrivilegedNesting: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to exec schema command: %w", err)
	}
	f, err := ctr.File(ctx, gw, "/outputs/envid", sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get envid file: %w", err)
	}
	newEnvID, err := f.Contents(ctx, gw, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to read envid file: %w", err)
	}
	newEnv, err := EnvironmentID(newEnvID).ToEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to decode envid: %w", err)
	}
	// stitch in the schema to this session too
	for _, cmd := range newEnv.Commands {
		env, err = env.WithCommand(ctx, gw, r, progSock, cmd, sessionID)
		if err != nil {
			return nil, fmt.Errorf("failed to stitch command %q: %w", cmd.Name, err)
		}
	}
	return env, nil
}

func (env *Environment) WithCommand(
	ctx context.Context,
	gw *GatewayClient,
	r *router.Router,
	progSock *Socket,
	cmd *EnvironmentCommand,
	sessionID string,
) (*Environment, error) {
	env = env.Clone()
	env.Commands = append(env.Commands, cmd)

	// TODO: dedupe/cache these calls

	runtime, err := env.runtime(ctx, gw, progSock, nil, sessionID)
	if err != nil {
		return nil, err
	}
	runtimeID, err := runtime.ID()
	if err != nil {
		return nil, err
	}
	cmd.Runtime = runtimeID

	if cmd.ResultType == "" {
		// TODO: shouldn't this be caught earlier? or just allowed?
		return nil, fmt.Errorf("command %q has no result type", cmd.Name)
	}
	fieldDef := &ast.FieldDefinition{
		Name:        cmd.Name,
		Description: cmd.Description,
		Type: &ast.Type{
			NamedType: cmd.ResultType,
			NonNull:   true,
		},
	}
	for _, flag := range cmd.Flags {
		fieldDef.Arguments = append(fieldDef.Arguments, &ast.ArgumentDefinition{
			Name: flag.Name,
			// Type is always string for now
			Type: &ast.Type{
				NamedType: "String",
				NonNull:   true,
			},
		})
	}

	buf := &bytes.Buffer{}
	formatter.NewFormatter(buf).FormatSchemaDocument(&ast.SchemaDocument{
		Extensions: ast.DefinitionList{
			&ast.Definition{
				// TODO: we need some namespace
				// TODO:
				// Name:   "Extensions",
				Name:   "Query",
				Kind:   ast.Object,
				Fields: ast.FieldList{fieldDef},
			},
		},
	})
	newSchemaString := buf.String()

	fieldResolver := router.ToResolver(func(ctx *router.Context, parent any, args any) (any, error) {
		return cmd.resolver(
			ctx, gw, r, progSock, nil,
			parent, args,
			ctx.ResolveParams.Info.ParentType.Name(),
			ctx.ResolveParams.Info.FieldName,
			sessionID,
		)
	})
	if err := r.Add(router.StaticSchema(router.StaticSchemaParams{
		Name:   env.Config.Name,
		Schema: newSchemaString,
		Resolvers: router.Resolvers{
			"Query": router.ObjectResolver{
				cmd.Name: fieldResolver,
			},
		},
	})); err != nil {
		return nil, fmt.Errorf("failed to install environment schema: %w", err)
	}

	return env, nil
}

// figure out if we were passed a path to a dagger.json file or a parent dir that may contain such a file
func (env *Environment) normalizeConfigPath(configPath string) string {
	baseName := path.Base(configPath)
	if baseName == "dagger.json" {
		return configPath
	}
	return path.Join(configPath, "dagger.json")
}

func (env *Environment) runtime(ctx context.Context, gw *GatewayClient, progSock *Socket, pipeline pipeline.Path, sessionID string) (*Container, error) {
	switch EnvironmentSDK(env.Config.SDK) {
	case EnvironmentSDKGo:
		return env.goRuntime(ctx, gw, progSock, pipeline, sessionID)
	case EnvironmentSDKPython:
		return env.pythonRuntime(ctx, gw, progSock, pipeline, sessionID)
	default:
		return nil, fmt.Errorf("unknown sdk %q", env.Config.SDK)
	}
}

func convertOutput(output any, resultTypeName string, r *router.Router) (any, error) {
	for objectName, resolver := range r.Resolvers() {
		if objectName != resultTypeName {
			continue
		}
		resolver, ok := resolver.(router.IDableObjectResolver)
		if !ok {
			continue
		}

		// ID-able dagger objects are serialized as their ID string across the wire
		// between the session and environment container.
		outputStr, ok := output.(string)
		if !ok {
			return nil, fmt.Errorf("expected id string output for %s", objectName)
		}
		return resolver.FromID(outputStr)
	}
	return output, nil
}

type EnvironmentCommand struct {
	Name        string                   `json:"name"`
	Flags       []EnvironmentCommandFlag `json:"flags"`
	ResultType  string                   `json:"resultType"`
	Description string                   `json:"description"`
	Runtime     ContainerID              `json:"runtime"`
}

type EnvironmentCommandFlag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SetValue    string `json:"setValue"`
}

func NewEnvironmentCommand(id EnvironmentCommandID) (*EnvironmentCommand, error) {
	environmentCmd, err := id.ToEnvironmentCommand()
	if err != nil {
		return nil, err
	}
	return environmentCmd, nil
}

func (cmd *EnvironmentCommand) ID() (EnvironmentCommandID, error) {
	return encodeID[EnvironmentCommandID](cmd)
}

func (cmd EnvironmentCommand) Clone() *EnvironmentCommand {
	cp := cmd
	cp.Flags = cloneSlice(cmd.Flags)
	return &cp
}

func (cmd *EnvironmentCommand) WithName(name string) *EnvironmentCommand {
	cmd = cmd.Clone()
	cmd.Name = name
	return cmd
}

func (cmd *EnvironmentCommand) WithFlag(flag EnvironmentCommandFlag) *EnvironmentCommand {
	cmd = cmd.Clone()
	cmd.Flags = append(cmd.Flags, flag)
	return cmd
}

func (cmd *EnvironmentCommand) WithResultType(resultType string) *EnvironmentCommand {
	cmd = cmd.Clone()
	cmd.ResultType = resultType
	return cmd
}

func (cmd *EnvironmentCommand) WithDescription(description string) *EnvironmentCommand {
	cmd = cmd.Clone()
	cmd.Description = description
	return cmd
}

func (cmd *EnvironmentCommand) SetStringFlag(name, value string) (*EnvironmentCommand, error) {
	cmd = cmd.Clone()
	for i, flag := range cmd.Flags {
		if flag.Name == name {
			cmd.Flags[i].SetValue = value
			return cmd, nil
		}
	}
	return nil, fmt.Errorf("no flag named %q", name)
}

func (cmd *EnvironmentCommand) resolver(
	ctx context.Context,
	gw *GatewayClient,
	r *router.Router,
	progSock *Socket,
	pipeline pipeline.Path,
	parent any,
	args any,
	parentTypeName string,
	fieldName string,
	sessionID string,
) (any, error) {
	ctr, err := cmd.Runtime.ToContainer()
	if err != nil {
		return nil, fmt.Errorf("failed to get runtime container for resolver: %w", err)
	}

	resolverName := fmt.Sprintf("%s.%s", parentTypeName, fieldName)
	inputMap := map[string]any{
		"resolver": resolverName,
		"args":     args,
		"parent":   parent,
	}
	inputBytes, err := json.Marshal(inputMap)
	if err != nil {
		return nil, err
	}
	ctr, err = ctr.WithNewFile(ctx, gw, path.Join(inputMountPath, inputFile), inputBytes, 0644, "", sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to mount resolver input file: %w", err)
	}

	ctr, err = ctr.WithMountedDirectory(ctx, gw, outputMountPath, NewScratchDirectory(nil, ctr.Platform), "", sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to mount resolver output directory: %w", err)
	}

	ctr, err = ctr.WithExec(ctx, gw, progSock, ctr.Platform, ContainerExecOpts{
		ExperimentalPrivilegedNesting: true,
	})
	if err != nil {
		return "", fmt.Errorf("failed to exec resolver: %w", err)
	}

	outputFile, err := ctr.File(ctx, gw, path.Join(outputMountPath, outputFile), sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get resolver output file: %w", err)
	}
	outputBytes, err := outputFile.Contents(ctx, gw, sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to read resolver output file: %w", err)
	}
	var output interface{}
	if err := json.Unmarshal(outputBytes, &output); err != nil {
		return nil, fmt.Errorf("failed to unmarshal output: %w", err)
	}
	return convertOutput(output, cmd.ResultType, r)
}

func Invoke[T any](
	ctx context.Context,
	gw *GatewayClient,
	r *router.Router,
	progSock *Socket,
	pipeline pipeline.Path,
	cmd *EnvironmentCommand,
	sessionID string,
) (T, error) {
	var zero T

	// TODO:
	parent := map[string]any{}
	// TODO:
	parentTypeName := "Query"

	args := map[string]any{}
	for _, flag := range cmd.Flags {
		args[flag.Name] = flag.SetValue
	}

	fieldName := strcase.ToLowerCamel(cmd.Name)
	res, err := cmd.resolver(
		ctx, gw, r, progSock, pipeline,
		parent, args, parentTypeName, fieldName,
		sessionID,
	)
	if err != nil {
		return zero, err
	}

	conv, ok := res.(T)
	if !ok {
		return zero, fmt.Errorf("expected %T result, got %T", conv, res)
	}
	return conv, nil
}
