package schema

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/router"
	"github.com/dagger/dagger/universe"
)

type environmentSchema struct {
	*baseSchema
}

var _ router.ExecutableSchema = &environmentSchema{}

func (s *environmentSchema) Name() string {
	return "environment"
}

func (s *environmentSchema) Schema() string {
	return Environment
}

var environmentIDResolver = stringResolver(core.EnvironmentID(""))

var environmentCommandIDResolver = stringResolver(core.EnvironmentCommandID(""))

func (s *environmentSchema) Resolvers() router.Resolvers {
	return router.Resolvers{
		"EnvironmentID":        environmentIDResolver,
		"EnvironmentCommandID": environmentCommandIDResolver,
		"Query": router.ObjectResolver{
			"environment":        router.ToResolver(s.environment),
			"environmentCommand": router.ToResolver(s.environmentCommand),
		},
		"Environment": router.ObjectResolver{
			"id":               router.ToResolver(s.environmentID),
			"load":             router.ToResolver(s.load),
			"loadFromUniverse": router.ToResolver(s.loadFromUniverse),
			"name":             router.ToResolver(s.environmentName),
			"container":        router.ToResolver(s.container),
			"command":          router.ToResolver(s.command),
			"withCommand":      router.ToResolver(s.withCommand),
			"withExtension":    router.ToResolver(s.withExtension),
		},
		"EnvironmentCommand": router.ObjectResolver{
			"id":              router.ToResolver(s.commandID),
			"withName":        router.ToResolver(s.withCommandName),
			"withFlag":        router.ToResolver(s.withCommandFlag),
			"withResultType":  router.ToResolver(s.withCommandResultType),
			"withDescription": router.ToResolver(s.withCommandDescription),
			"setStringFlag":   router.ToResolver(s.setStringFlag),
			"invoke":          router.ToResolver(s.invoke),
		},
	}
}

func (s *environmentSchema) Dependencies() []router.ExecutableSchema {
	return nil
}

type environmentArgs struct {
	ID core.EnvironmentID
}

func (s *environmentSchema) environment(ctx *router.Context, parent *core.Query, args environmentArgs) (*core.Environment, error) {
	return core.NewEnvironment(args.ID, s.platform)
}

func (s *environmentSchema) environmentID(ctx *router.Context, parent *core.Environment, args any) (core.EnvironmentID, error) {
	return parent.ID()
}

func (s *environmentSchema) environmentName(ctx *router.Context, parent *core.Environment, args any) (string, error) {
	return parent.Config.Name, nil
}

func (s *environmentSchema) container(ctx *router.Context, parent *core.Environment, args any) (*core.Container, error) {
	panic("implement me")
}

type loadArgs struct {
	Source     core.DirectoryID
	ConfigPath string
}

func (s *environmentSchema) load(ctx *router.Context, parent *core.Environment, args loadArgs) (*core.Environment, error) {
	source, err := args.Source.ToDirectory()
	if err != nil {
		return nil, err
	}
	// TODO:
	// progSock := &core.Socket{HostPath: s.progSock}
	// return parent.Load(ctx, s.gw, s.router, progSock, source.Pipeline, source, args.ConfigPath)
	return parent.Load(ctx, s.gw, s.router, nil, source.Pipeline, source, args.ConfigPath, ctx.ClientSessionID)
}

type loadFromUniverseArgs struct {
	Name string
}

var loadUniverseOnce = &sync.Once{}
var universeDirID core.DirectoryID
var loadUniverseErr error

func (s *environmentSchema) loadFromUniverse(ctx *router.Context, parent *core.Environment, args loadFromUniverseArgs) (*core.Environment, error) {
	// TODO: this is total crap
	loadUniverseOnce.Do(func() {
		tempdir, err := os.MkdirTemp("", "dagger-universe")
		if err != nil {
			loadUniverseErr = err
			return
		}

		tarReader := tar.NewReader(bytes.NewReader(universe.Tar))
		for {
			header, err := tarReader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				loadUniverseErr = err
				return
			}
			if header.FileInfo().IsDir() {
				if err := os.MkdirAll(filepath.Join(tempdir, header.Name), header.FileInfo().Mode()); err != nil {
					loadUniverseErr = err
					return
				}
			} else {
				if err := os.MkdirAll(filepath.Join(tempdir, filepath.Dir(header.Name)), header.FileInfo().Mode()); err != nil {
					loadUniverseErr = err
					return
				}
				f, err := os.OpenFile(filepath.Join(tempdir, header.Name), os.O_CREATE|os.O_WRONLY, header.FileInfo().Mode())
				if err != nil {
					loadUniverseErr = err
					return
				}
				defer f.Close()
				if _, err := io.Copy(f, tarReader); err != nil {
					loadUniverseErr = err
					return
				}
			}
		}

		dir, err := core.NewHost().Directory(ctx, s.gw, tempdir, nil, "universe", s.platform, core.CopyFilter{}, ctx.ClientSessionID)
		if err != nil {
			loadUniverseErr = err
			return
		}
		universeDirID, loadUniverseErr = dir.ID()
	})
	if loadUniverseErr != nil {
		return nil, loadUniverseErr
	}

	return s.load(ctx, parent, loadArgs{
		Source: universeDirID,
		// TODO: should be by name, not path
		ConfigPath: filepath.Join("universe", args.Name),
	})
}

type commandArgs struct {
	Name string
}

func (s *environmentSchema) command(ctx *router.Context, parent *core.Environment, args commandArgs) (*core.EnvironmentCommand, error) {
	for _, cmd := range parent.Commands {
		if cmd.Name == args.Name {
			return cmd, nil
		}
	}
	return nil, nil
}

type withCommandArgs struct {
	ID core.EnvironmentCommandID
}

func (s *environmentSchema) withCommand(ctx *router.Context, parent *core.Environment, args withCommandArgs) (*core.Environment, error) {
	cmd, err := args.ID.ToEnvironmentCommand()
	if err != nil {
		return nil, err
	}
	// TODO:
	// progSock := &core.Socket{HostPath: s.progSock}
	// return parent.WithCommand(ctx, s.gw, s.router, progSock, cmd)
	return parent.WithCommand(ctx, s.gw, s.router, nil, cmd, ctx.ClientSessionID)
}

type withExtensionArgs struct {
	ID        core.EnvironmentID
	Namespace string
}

func (s *environmentSchema) withExtension(ctx *router.Context, parent *core.Environment, args withExtensionArgs) (*core.Environment, error) {
	// TODO:
	panic("implement me")
}

type environmentCommandArgs struct {
	ID core.EnvironmentCommandID
}

func (s *environmentSchema) environmentCommand(ctx *router.Context, parent *core.Query, args environmentCommandArgs) (*core.EnvironmentCommand, error) {
	return core.NewEnvironmentCommand(args.ID)
}

func (s *environmentSchema) commandID(ctx *router.Context, parent *core.EnvironmentCommand, args any) (core.EnvironmentCommandID, error) {
	return parent.ID()
}

type withCommandNameArgs struct {
	Name string
}

func (s *environmentSchema) withCommandName(ctx *router.Context, parent *core.EnvironmentCommand, args withCommandNameArgs) (*core.EnvironmentCommand, error) {
	return parent.WithName(args.Name), nil
}

type withCommandFlagArgs struct {
	Name        string
	Description string
}

func (s *environmentSchema) withCommandFlag(ctx *router.Context, parent *core.EnvironmentCommand, args withCommandFlagArgs) (*core.EnvironmentCommand, error) {
	return parent.WithFlag(core.EnvironmentCommandFlag{
		Name:        args.Name,
		Description: args.Description,
	}), nil
}

type withCommandResultTypeArgs struct {
	Name string
}

func (s *environmentSchema) withCommandResultType(ctx *router.Context, parent *core.EnvironmentCommand, args withCommandResultTypeArgs) (*core.EnvironmentCommand, error) {
	return parent.WithResultType(args.Name), nil
}

type withCommandDescriptionArgs struct {
	Description string
}

func (s *environmentSchema) withCommandDescription(ctx *router.Context, parent *core.EnvironmentCommand, args withCommandDescriptionArgs) (*core.EnvironmentCommand, error) {
	return parent.WithDescription(args.Description), nil
}

type setStringFlagArgs struct {
	Name  string
	Value string
}

func (s *environmentSchema) setStringFlag(ctx *router.Context, parent *core.EnvironmentCommand, args setStringFlagArgs) (*core.EnvironmentCommand, error) {
	return parent.SetStringFlag(args.Name, args.Value)
}

func (s *environmentSchema) invoke(ctx *router.Context, parent *core.EnvironmentCommand, args any) (any, error) {
	// TODO:
	// progSock := &core.Socket{HostPath: s.progSock}

	var res any
	var err error
	switch parent.ResultType {
	case "String", "":
		// TODO: res, err = core.Invoke[string](ctx, s.gw, s.router, progSock, nil, parent)
		res, err = core.Invoke[string](ctx, s.gw, s.router, nil, nil, parent, ctx.ClientSessionID)
	case "File":
		// TODO: res, err = core.Invoke[*core.File](ctx, s.gw, s.router, progSock, nil, parent)
		res, err = core.Invoke[*core.File](ctx, s.gw, s.router, nil, nil, parent, ctx.ClientSessionID)
	case "Directory":
		// TODO: res, err = core.Invoke[*core.Directory](ctx, s.gw, s.router, progSock, nil, parent)
		res, err = core.Invoke[*core.Directory](ctx, s.gw, s.router, nil, nil, parent, ctx.ClientSessionID)
	default:
		return nil, fmt.Errorf("unsupported result type: %s", parent.ResultType)
	}
	if err != nil {
		return nil, err
	}

	return map[string]any{
		strings.ToLower(parent.ResultType): res,
	}, nil
}
