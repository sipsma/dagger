package router

import (
	"github.com/containerd/containerd/platforms"
)

type rootSchema struct {
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

func (r *rootSchema) withArchitecture(ctx *Context, parent any, args struct {
	Architecture string `json:"architecture,omitempty"`
}) (struct{}, error) {
	if args.Architecture == "host" {
		ctx.Platform = ctx.HostPlatform
	} else {
		pl, err := platforms.Parse("linux/" + args.Architecture)
		if err != nil {
			return struct{}{}, err
		}
		ctx.Platform = pl
	}
	return struct{}{}, nil
}

func (r *rootSchema) architecture(ctx *Context, parent, args any) (string, error) {
	return ctx.Platform.Architecture, nil
}
