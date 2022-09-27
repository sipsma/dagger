package router

import (
	"github.com/containerd/containerd/platforms"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	hostPlatformKey = "host"
)

type rootSchema struct {
	hostPlatform specs.Platform
}

func (r *rootSchema) Name() string {
	return "root"
}

func (r *rootSchema) Schema() string {
	return `
	type Query {
		"use the provided architecture as the default in the rest of the query"
		withArchitecture(architecture: String!): Query!

		"return the current default architecture for the query"
		architecture: String!
	}
	`
}

func (r *rootSchema) Operations() string {
	return ""
}

func (r *rootSchema) Resolvers() Resolvers {
	return Resolvers{
		"Query": ObjectResolver{
			"withArchitecture": ToResolver(r.withArchitecture),
			"architecture":     ToResolver(r.architecture),
		},
	}
}

func (r *rootSchema) Dependencies() []ExecutableSchema {
	return nil
}

type withArchitectureArgs struct {
	Architecture string
}

func (r *rootSchema) withArchitecture(ctx *Context, parent any, args withArchitectureArgs) (struct{}, error) {
	if args.Architecture == hostPlatformKey {
		ctx.Session.Platform = r.hostPlatform
	} else {
		pl, err := platforms.Parse("linux/" + args.Architecture)
		if err != nil {
			return struct{}{}, err
		}
		ctx.Session.Platform = pl
	}

	return struct{}{}, nil
}

func (r *rootSchema) architecture(ctx *Context, parent any, args any) (string, error) {
	return ctx.Session.Platform.Architecture, nil
}
