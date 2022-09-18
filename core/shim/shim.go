package shim

import (
	"embed"
	"io/fs"
	"path"

	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/client/llb"
	dockerfilebuilder "github.com/moby/buildkit/frontend/dockerfile/builder"
	bkgw "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"go.dagger.io/dagger/router"
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
	return state, err
}

func Build(ctx *router.Context, gw bkgw.Client) (llb.State, error) {
	shimSrc, err := shimSource()
	if err != nil {
		return llb.State{}, err
	}
	def, err := shimSrc.Marshal(ctx)
	if err != nil {
		return llb.State{}, err
	}

	opts := map[string]string{
		"platform": platforms.Format(ctx.Platform),
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
