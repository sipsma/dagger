package shim

import (
	"context"
	"embed"
	"io/fs"
	"path"

	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	dockerfilebuilder "github.com/moby/buildkit/frontend/dockerfile/builder"
	bkgw "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

//go:embed cmd/*
var cmd embed.FS

const Path = "/_shim"

func shimSource() (llb.State, error) {
	entries, err := fs.ReadDir(cmd, "cmd")
	if err != nil {
		return llb.State{}, err
	}

	state := llb.Scratch()
	for _, e := range entries {
		contents, err := fs.ReadFile(cmd, path.Join("cmd", e.Name()))
		if err != nil {
			return llb.State{}, err
		}

		state = state.File(llb.Mkfile(e.Name(), e.Type().Perm(), contents))
		e.Name()
	}
	return state, nil
}

func Build(ctx context.Context, gw bkgw.Client, p specs.Platform) (llb.State, error) {
	// It's not incredibly efficient to call shimSource every time, but llb.State in general
	// is not designed to handle being marshalled multiple times with different args (e.g. platform)
	// and will fail in surprising ways.
	shimSrc, err := shimSource()
	if err != nil {
		return llb.State{}, err
	}
	def, err := shimSrc.Marshal(ctx, llb.Platform(p))
	if err != nil {
		return llb.State{}, err
	}

	opts := map[string]string{
		"platform": platforms.Format(p),
	}
	inputs := map[string]*pb.Definition{
		dockerfilebuilder.DefaultLocalNameContext:    def.ToPB(),
		dockerfilebuilder.DefaultLocalNameDockerfile: def.ToPB(),
	}
	res, err := gw.Solve(ctx, bkgw.SolveRequest{
		Frontend:       "dockerfile.v0",
		FrontendOpt:    opts,
		FrontendInputs: inputs,
	})
	if err != nil {
		return llb.State{}, err
	}

	bkref, err := res.SingleRef()
	if err != nil {
		return llb.State{}, err
	}

	return bkref.ToState()
}
