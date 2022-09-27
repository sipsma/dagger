package core

import (
	"github.com/containerd/containerd/platforms"
	"github.com/graphql-go/graphql"
	dockerfilebuilder "github.com/moby/buildkit/frontend/dockerfile/builder"
	bkgw "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"go.dagger.io/dagger/core/filesystem"
	"go.dagger.io/dagger/router"
)

var _ router.ExecutableSchema = &dockerBuildSchema{}

type dockerBuildSchema struct {
	*baseSchema
}

func (s *dockerBuildSchema) Name() string {
	return "dockerbuild"
}

func (s *dockerBuildSchema) Schema() string {
	return `
extend type Filesystem {
	"docker build using this filesystem as context"
	dockerbuild(dockerfile: String): Filesystem!
}
	`
}

func (s *dockerBuildSchema) Resolvers() router.Resolvers {
	return router.Resolvers{
		"Filesystem": router.ObjectResolver{
			"dockerbuild": s.dockerbuild,
		},
	}
}

func (s *dockerBuildSchema) Dependencies() []router.ExecutableSchema {
	return nil
}

func (s *dockerBuildSchema) dockerbuild(p graphql.ResolveParams) (any, error) {
	plt, err := router.PlatformOf(p)
	if err != nil {
		return nil, err
	}

	obj, err := filesystem.FromSource(p.Source)
	if err != nil {
		return nil, err
	}

	def, err := obj.ToDefinition()
	if err != nil {
		return nil, err
	}

	opts := map[string]string{
		"platform": platforms.Format(plt),
	}
	if dockerfile, ok := p.Args["dockerfile"].(string); ok {
		opts["filename"] = dockerfile
	}
	inputs := map[string]*pb.Definition{
		dockerfilebuilder.DefaultLocalNameContext:    def,
		dockerfilebuilder.DefaultLocalNameDockerfile: def,
	}
	res, err := s.gw.Solve(p.Context, bkgw.SolveRequest{
		Frontend:       "dockerfile.v0",
		FrontendOpt:    opts,
		FrontendInputs: inputs,
	})
	if err != nil {
		return nil, err
	}

	bkref, err := res.SingleRef()
	if err != nil {
		return nil, err
	}
	st, err := bkref.ToState()
	if err != nil {
		return nil, err
	}

	return filesystem.FromState(p.Context, st, plt)
}
