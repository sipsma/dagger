package router

import (
	"github.com/containerd/containerd/platforms"
	"github.com/graphql-go/graphql"
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
			"withArchitecture": r.withArchitecture,
			"architecture":     r.architecture,
		},
	}
}

func (r *rootSchema) Dependencies() []ExecutableSchema {
	return nil
}

func (r *rootSchema) withArchitecture(p graphql.ResolveParams) (any, error) {
	arch := p.Args["architecture"].(string)

	sessionCtx, err := SessionContextOf(p)
	if err != nil {
		return nil, err
	}

	if arch == hostPlatformKey {
		sessionCtx.Platform = r.hostPlatform
	} else {
		pl, err := platforms.Parse("linux/" + arch)
		if err != nil {
			return struct{}{}, err
		}
		sessionCtx.Platform = pl
	}

	if err := SetSessionContext(p, sessionCtx); err != nil {
		return nil, err
	}

	return struct{}{}, nil
}

func (r *rootSchema) architecture(p graphql.ResolveParams) (any, error) {
	sessionCtx, err := SessionContextOf(p)
	if err != nil {
		return nil, err
	}
	return sessionCtx.Platform.Architecture, nil
}
