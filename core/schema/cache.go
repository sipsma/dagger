package schema

import (
	"context"
	"errors"
	"fmt"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/dagql"
)

type cacheSchema struct{}

var _ SchemaResolvers = &cacheSchema{}

func (s *cacheSchema) Name() string {
	return "cache"
}

func (s *cacheSchema) Install(srv *dagql.Server) {
	dagql.Fields[*core.Query]{
		dagql.NodeFunc("cacheVolume", s.cacheVolume).
			WithInput(cacheVolumeNamespaceInput).
			Doc("Constructs a cache volume for a given cache key.").
			Args(
				dagql.Arg("key").Doc(`A string identifier to target this cache volume (e.g., "modules-cache").`),
			),
	}.Install(srv)

	dagql.Fields[*core.CacheVolume]{}.Install(srv)
}

func (s *cacheSchema) Dependencies() []SchemaResolvers {
	return nil
}

type cacheArgs struct {
	Key       string
	Namespace string `internal:"true" default:""`
}

var cacheVolumeNamespaceInput = dagql.ImplicitInput{
	Name: "cacheNamespace",
	Resolver: func(ctx context.Context, args map[string]dagql.Input) (dagql.Input, error) {
		namespaceArg, err := cacheNamespaceArg(args)
		if err != nil {
			return nil, err
		}
		namespace, err := cacheNamespace(ctx, namespaceArg)
		if err != nil {
			return nil, err
		}
		return dagql.NewString(namespace), nil
	},
}

func cacheNamespaceArg(args map[string]dagql.Input) (string, error) {
	raw, ok := args["namespace"]
	if !ok || raw == nil {
		return "", nil
	}

	switch val := raw.(type) {
	case dagql.String:
		return val.String(), nil
	case dagql.Optional[dagql.String]:
		if !val.Valid {
			return "", nil
		}
		return val.Value.String(), nil
	default:
		return "", fmt.Errorf("cache namespace input must be String, got %T", raw)
	}
}

func cacheNamespace(ctx context.Context, namespaceArg string) (string, error) {
	if namespaceArg != "" {
		return namespaceArg, nil
	}
	query, err := core.CurrentQuery(ctx)
	if err != nil {
		return "", err
	}
	m, err := query.CurrentModule(ctx)
	if err != nil && !errors.Is(err, core.ErrNoCurrentModule) {
		return "", err
	}
	return namespaceFromModule(m), nil
}

func (s *cacheSchema) cacheVolume(ctx context.Context, _ dagql.ObjectResult[*core.Query], args cacheArgs) (dagql.Result[*core.CacheVolume], error) {
	var inst dagql.Result[*core.CacheVolume]

	srv, err := core.CurrentDagqlServer(ctx)
	if err != nil {
		return inst, err
	}

	namespace, err := cacheNamespace(ctx, args.Namespace)
	if err != nil {
		return inst, err
	}

	if args.Namespace != "" {
		return dagql.NewResultForCurrentID(ctx, core.NewCache(namespace+":"+args.Key))
	}

	err = srv.Select(ctx, srv.Root(), &inst, dagql.Selector{
		Field: "cacheVolume",
		Args: []dagql.NamedInput{
			{
				Name:  "key",
				Value: dagql.NewString(args.Key),
			},
			{
				Name:  "namespace",
				Value: dagql.NewString(namespace),
			},
		},
	})
	if err != nil {
		return inst, err
	}

	return inst, nil
}

func namespaceFromModule(m *core.Module) string {
	if m == nil {
		return "mainClient"
	}

	src := m.Source.Value
	name := src.Self().ModuleOriginalName

	var symbolic string
	switch src.Self().Kind {
	case core.ModuleSourceKindLocal:
		symbolic = src.Self().SourceRootSubpath
	case core.ModuleSourceKindGit:
		symbolic = src.Self().Git.Symbolic
	case core.ModuleSourceKindDir:
		symbolic = m.Source.Value.ID().Digest().String()
	}

	return "mod(" + name + symbolic + ")"
}
