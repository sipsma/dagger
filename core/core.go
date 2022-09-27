package core

import (
	"context"
	"encoding/json"

	"github.com/containerd/containerd/platforms"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	bkgw "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"go.dagger.io/dagger/core/filesystem"
	"go.dagger.io/dagger/project"
	"go.dagger.io/dagger/router"
	"go.dagger.io/dagger/secret"
)

type InitializeArgs struct {
	Router        *router.Router
	SecretStore   *secret.Store
	SSHAuthSockID string
	WorkdirID     string
	Gateway       bkgw.Client
	BKClient      *bkclient.Client
	SolveOpts     bkclient.SolveOpt
	SolveCh       chan *bkclient.SolveStatus
	HostPlatform  specs.Platform
}

func New(params InitializeArgs) (router.ExecutableSchema, error) {
	base := &baseSchema{
		router:        params.Router,
		secretStore:   params.SecretStore,
		gw:            params.Gateway,
		bkClient:      params.BKClient,
		solveOpts:     params.SolveOpts,
		solveCh:       params.SolveCh,
		hostPlatform:  params.HostPlatform,
		sshAuthSockID: params.SSHAuthSockID,
	}
	return router.MergeExecutableSchemas("core",
		&coreSchema{base, params.WorkdirID},
		&directorySchema{base},
		&fileSchema{base},
		&gitSchema{base},
		&filesystemSchema{base},
		&projectSchema{
			baseSchema:      base,
			remoteSchemas:   make(map[string]*project.RemoteSchema),
			compiledSchemas: make(map[string]*project.CompiledRemoteSchema),
		},
		&execSchema{base},
		&dockerBuildSchema{base},
		&secretSchema{base},
	)
}

type baseSchema struct {
	router        *router.Router
	secretStore   *secret.Store
	gw            bkgw.Client
	bkClient      *bkclient.Client
	solveOpts     bkclient.SolveOpt
	solveCh       chan *bkclient.SolveStatus
	hostPlatform  specs.Platform
	sshAuthSockID string
}

func (r *baseSchema) Solve(ctx context.Context, st llb.State, platform specs.Platform, marshalOpts ...llb.ConstraintsOpt) (*filesystem.Filesystem, error) {
	marshalOpts = append([]llb.ConstraintsOpt{llb.Platform(platform)}, marshalOpts...)

	inputCfg, err := filesystem.ImageConfigFromState(ctx, st)
	if err != nil {
		return nil, err
	}

	input, err := st.Marshal(ctx, marshalOpts...)
	if err != nil {
		return nil, err
	}

	res, err := r.gw.Solve(ctx, bkgw.SolveRequest{
		Evaluate:   true,
		Definition: input.ToPB(),
	})
	if err != nil {
		return nil, err
	}

	bkref, err := res.SingleRef()
	if err != nil {
		return nil, err
	}

	resSt, err := bkref.ToState()
	if err != nil {
		return nil, err
	}

	img, err := filesystem.ImageFromStateAndConfig(ctx, resSt, inputCfg, marshalOpts...)
	if err != nil {
		return nil, err
	}

	return img.ToFilesystem()
}

func (r *baseSchema) Export(ctx context.Context, fs *filesystem.Filesystem, platform specs.Platform, export bkclient.ExportEntry) error {
	fsDef, err := fs.ToDefinition()
	if err != nil {
		return err
	}
	img, err := fs.ToImage()
	if err != nil {
		return err
	}
	cfgBytes, err := json.Marshal(specs.Image{
		Architecture: platform.Architecture,
		OS:           platform.OS,
		OSVersion:    platform.OSVersion,
		OSFeatures:   platform.OSFeatures,
		Config:       img.Config,
	})
	if err != nil {
		return err
	}

	solveOpts := r.solveOpts
	// NOTE: be careful to not overwrite any values from original shared r.solveOpts (i.e. with append).
	solveOpts.Exports = []bkclient.ExportEntry{export}

	// Mirror events from the sub-Build into the main Build event channel.
	// Build() will close the channel after completion so we don't want to use the main channel directly.
	ch := make(chan *bkclient.SolveStatus)
	go func() {
		for event := range ch {
			r.solveCh <- event
		}
	}()

	_, err = r.bkClient.Build(ctx, solveOpts, "", func(ctx context.Context, gw bkgw.Client) (*bkgw.Result, error) {
		res, err := gw.Solve(ctx, bkgw.SolveRequest{
			Evaluate:   true,
			Definition: fsDef,
		})
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterImageConfigKey, cfgBytes)
		return res, nil
	}, ch)
	if err != nil {
		return err
	}
	return nil
}

func (r *baseSchema) ExportMultiplatformImage(ctx context.Context, filesystems []*filesystem.Filesystem, export bkclient.ExportEntry) error {
	solveOpts := r.solveOpts
	solveOpts.Exports = []bkclient.ExportEntry{export}

	ch := make(chan *bkclient.SolveStatus)
	go func() {
		for event := range ch {
			r.solveCh <- event
		}
	}()

	_, err := r.bkClient.Build(ctx, solveOpts, "", func(ctx context.Context, gw bkgw.Client) (*bkgw.Result, error) {
		res := bkgw.NewResult()
		expPlatforms := &exptypes.Platforms{
			Platforms: make([]exptypes.Platform, len(filesystems)),
		}
		for i, fs := range filesystems {
			fsDef, err := fs.ToDefinition()
			if err != nil {
				return nil, err
			}
			var op pb.Op
			if err := op.Unmarshal(fsDef.Def[0]); err != nil {
				return nil, err
			}
			subres, err := gw.Solve(ctx, bkgw.SolveRequest{
				Evaluate:   true,
				Definition: fsDef,
			})
			if err != nil {
				return nil, err
			}
			ref, err := subres.SingleRef()
			if err != nil {
				return nil, err
			}
			var platformSpec specs.Platform
			if op.Platform != nil {
				platformSpec = op.Platform.Spec()
			} else {
				platformSpec = platforms.DefaultSpec()
			}
			platformKey := platforms.Format(platformSpec)
			res.AddRef(platformKey, ref)
			expPlatforms.Platforms[i] = exptypes.Platform{
				ID:       platformKey,
				Platform: platformSpec,
			}
		}
		platformBytes, err := json.Marshal(expPlatforms)
		if err != nil {
			return nil, err
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, platformBytes)
		return res, nil
	}, ch)
	if err != nil {
		return err
	}
	return nil
}
