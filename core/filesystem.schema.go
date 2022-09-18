package core

import (
	"fmt"

	"github.com/graphql-go/graphql/language/ast"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"go.dagger.io/dagger/core/filesystem"
	"go.dagger.io/dagger/router"
)

var fsIDResolver = router.ScalarResolver{
	Serialize: func(value any) any {
		switch v := value.(type) {
		case filesystem.FSID, string:
			return v
		default:
			panic(fmt.Sprintf("unexpected fsid type %T", v))
		}
	},
	ParseValue: func(value any) any {
		switch v := value.(type) {
		case string:
			return filesystem.FSID(v)
		default:
			panic(fmt.Sprintf("unexpected fsid value type %T: %+v", v, v))
		}
	},
	ParseLiteral: func(valueAST ast.Value) any {
		switch valueAST := valueAST.(type) {
		case *ast.StringValue:
			return filesystem.FSID(valueAST.Value)
		default:
			panic(fmt.Sprintf("unexpected fsid literal type: %T", valueAST))
		}
	},
}

var _ router.ExecutableSchema = &filesystemSchema{}

type filesystemSchema struct {
	*baseSchema
}

func (s *filesystemSchema) Name() string {
	return "filesystem"
}

func (s *filesystemSchema) Schema() string {
	return `
scalar FSID

"""
A reference to a filesystem tree.

For example:
	- The root filesystem of a container
	- A source code repository
	- A directory containing binary artifacts

Rule of thumb: if it fits in a tar archive, it fits in a Filesystem.
"""
type Filesystem {
	id: FSID!

	"read a file at path"
	file(path: String!, lines: Int): String

	"copy from a filesystem"
	copy(
		from: FSID!,
		srcPath: String,
		destPath: String,
		include: [String!]
		exclude: [String!]
	): Filesystem

	"push a filesystem as an image to a registry"
	pushImage(ref: String!): Boolean!
}

extend type Core {
	"Look up a filesystem by its ID"
	filesystem(id: FSID!): Filesystem!
}
`
}

func (s *filesystemSchema) Resolvers() router.Resolvers {
	return router.Resolvers{
		"FSID": fsIDResolver,
		"Core": router.ObjectResolver{
			"filesystem": router.ToResolver(s.filesystem),
		},
		"Filesystem": router.ObjectResolver{
			"file":      router.ToResolver(s.file),
			"copy":      router.ToResolver(s.copy),
			"pushImage": router.ToResolver(s.pushImage),
		},
	}
}

func (s *filesystemSchema) Dependencies() []router.ExecutableSchema {
	return nil
}

type filesystemArgs struct {
	ID filesystem.FSID
}

func (s *filesystemSchema) filesystem(ctx *router.Context, parent struct{}, args filesystemArgs) (*filesystem.Filesystem, error) {
	return filesystem.New(args.ID), nil
}

type fileArgs struct {
	Path  string
	Lines *int
}

func (s *filesystemSchema) file(ctx *router.Context, parent *filesystem.Filesystem, args fileArgs) (string, error) {
	output, err := parent.ReadFile(ctx, s.gw, args.Path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return truncate(string(output), args.Lines), nil
}

type copyArgs struct {
	From     filesystem.FSID
	SrcPath  string
	DestPath string
	Include  []string
	Exclude  []string
}

func (s *filesystemSchema) copy(ctx *router.Context, parent *filesystem.Filesystem, args copyArgs) (*filesystem.Filesystem, error) {
	st, err := parent.ToState()
	if err != nil {
		return nil, err
	}

	contents, err := filesystem.New(args.From).ToState()
	if err != nil {
		return nil, err
	}

	st = st.File(llb.Copy(contents, args.SrcPath, args.DestPath, &llb.CopyInfo{
		CopyDirContentsOnly: true,
		CreateDestPath:      true,
		AllowWildcard:       true,
		IncludePatterns:     args.Include,
		ExcludePatterns:     args.Exclude,
	}))
	return s.Solve(ctx, st)
}

type pushImageArgs struct {
	Ref string
}

func (s *filesystemSchema) pushImage(ctx *router.Context, parent *filesystem.Filesystem, args pushImageArgs) (bool, error) {
	if args.Ref == "" {
		return false, fmt.Errorf("ref is required for pushImage")
	}

	if err := s.Export(ctx, parent, bkclient.ExportEntry{
		Type: bkclient.ExporterImage,
		Attrs: map[string]string{
			"name": args.Ref,
			"push": "true",
		},
	}); err != nil {
		return false, err
	}
	return true, nil
}
