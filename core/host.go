package core

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/dagger/dagger/core/pipeline"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	bkgw "github.com/moby/buildkit/frontend/gateway/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/vito/progrock"
)

type Host struct {
	// TODO:
	// Which sessionID this host refers to
	// SessionID string `json:"sessionID"`
}

// TODO:
/*
func NewHost(sessionID string) *Host {
	return &Host{SessionID: sessionID}
}
*/

func NewHost() *Host {
	return &Host{}
}

type HostVariable struct {
	Name string `json:"name"`
}

type CopyFilter struct {
	Exclude []string
	Include []string
}

func (host *Host) Directory(
	ctx context.Context,
	gw *GatewayClient,
	dirPath string,
	p pipeline.Path,
	pipelineNamePrefix string,
	platform specs.Platform,
	filter CopyFilter,
	clientSessionID string,
) (*Directory, error) {
	// TODO: enforcement that requester session is granted access to source session at this path

	// Create a sub-pipeline to group llb.Local instructions
	pipelineName := fmt.Sprintf("%s %s", pipelineNamePrefix, dirPath)
	ctx, subRecorder := progrock.WithGroup(ctx, pipelineName, progrock.Weak())

	localID := fmt.Sprintf("host:%s", dirPath)

	localOpts := []llb.LocalOption{
		// Custom name
		llb.WithCustomNamef("upload %s", dirPath),

		// synchronize concurrent filesyncs for the same path
		llb.SharedKeyHint(localID),

		// sync this dir from this session specifically, even if this ends up passed
		// to a different session (e.g. a project container)
		llb.SessionID(clientSessionID),
	}

	if len(filter.Exclude) > 0 {
		localOpts = append(localOpts, llb.ExcludePatterns(filter.Exclude))
	}

	if len(filter.Include) > 0 {
		localOpts = append(localOpts, llb.IncludePatterns(filter.Include))
	}

	// copy to scratch to avoid making buildkit's snapshot of the local dir immutable,
	// which makes it unable to reused, which in turn creates cache invalidations
	// TODO: this should be optional, the above issue can also be avoided w/ readonly
	// mount when possible
	st := llb.Scratch().File(
		llb.Copy(llb.Local(dirPath, localOpts...), "/", "/"),
		llb.WithCustomNamef("copy %s", dirPath),
	)

	def, err := st.Marshal(ctx, llb.Platform(platform))
	if err != nil {
		return nil, err
	}

	defPB := def.ToPB()

	// associate vertexes to the 'host.directory' sub-pipeline
	recordVertexes(subRecorder, defPB)

	_, err = gw.Solve(ctx, bkgw.SolveRequest{
		Definition: defPB,
		Evaluate:   true, // do the sync now, not lazily
	}, clientSessionID)
	if err != nil {
		return nil, fmt.Errorf("sync %s: %w", dirPath, err)
	}

	return NewDirectory(ctx, defPB, "", p, platform, nil), nil
}

func (host *Host) File(
	ctx context.Context,
	gw *GatewayClient,
	path string,
	p pipeline.Path,
	platform specs.Platform,
	clientSessionID string,
) (*File, error) {
	parentDir, err := host.Directory(ctx, gw, filepath.Dir(path), p, "host.file", platform, CopyFilter{
		Include: []string{filepath.Base(path)},
	}, clientSessionID)
	if err != nil {
		return nil, err
	}
	return parentDir.File(ctx, filepath.Base(path))
}

func (host *Host) Socket(ctx context.Context, sockPath string) (*Socket, error) {
	// TODO: enforcement that requester session is granted access to source session at this path
	return NewHostSocket(sockPath), nil
}

func (host *Host) Export(
	ctx context.Context,
	exportCfg bkclient.ExportEntry,
) error {
	panic("reimplement host export")
	/*
		if host.DisableRW {
			return ErrHostRWDisabled
		}

		ch, wg := mirrorCh(solveCh)
		defer wg.Wait()

		solveOpts.Exports = []bkclient.ExportEntry{export}

		_, err := bkClient.Build(ctx, solveOpts, "", buildFn, ch)
		return err
	*/
}
