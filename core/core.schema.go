package core

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/docker/distribution/reference"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"go.dagger.io/dagger/core/filesystem"
	"go.dagger.io/dagger/router"
)

var _ router.ExecutableSchema = &coreSchema{}

type coreSchema struct {
	*baseSchema
	workdirID string
}

func (r *coreSchema) Name() string {
	return "core"
}

func (r *coreSchema) Schema() string {
	return `
extend type Query {
	"Core API"
	core: Core!

	"Host API"
	host: Host!
}

"Core API"
type Core {
	"Fetch an OCI image"
	image(ref: String!): Filesystem!

	"Push a multiplatform image"
	pushMultiplatformImage(ref: String!, filesystems: [FSID!]!): Boolean!
}

"Interactions with the user's host filesystem"
type Host {
	"Fetch the client's workdir"
	workdir: LocalDir!

	"Fetch a client directory"
	dir(id: String!): LocalDir!
}

"A directory on the user's host filesystem"
type LocalDir {
	"Read the contents of the directory"
	read: Filesystem!

	"Write the provided filesystem to the directory"
	write(contents: FSID!, path: String): Boolean!
}
`
}

func (r *coreSchema) Resolvers() router.Resolvers {
	return router.Resolvers{
		"Query": router.ObjectResolver{
			r.Name(): router.PassthroughResolver,
			"host":   router.PassthroughResolver,
		},
		"Core": router.ObjectResolver{
			"image":                  router.ToResolver(r.image),
			"pushMultiplatformImage": router.ToResolver(r.pushMultiplatformImage),
		},
		"Host": router.ObjectResolver{
			"workdir": router.ToResolver(r.workdir),
			"dir":     router.ToResolver(r.dir),
		},
		"LocalDir": router.ObjectResolver{
			"read":  router.ToResolver(r.localDirRead),
			"write": router.ToResolver(r.localDirWrite),
		},
	}
}

func (r *coreSchema) Dependencies() []router.ExecutableSchema {
	return nil
}

type imageArgs struct {
	Ref string
}

func (r *coreSchema) image(ctx *router.Context, parent any, args imageArgs) (*filesystem.Filesystem, error) {
	st := llb.Image(args.Ref)
	// TODO:(sipsma) just a temporary hack to help out with issues like this while we continue to
	// flesh out the new api: https://github.com/dagger/dagger/issues/3170
	refName, err := reference.ParseNormalizedNamed(args.Ref)
	if err != nil {
		return nil, err
	}
	ref := reference.TagNameOnly(refName).String()
	_, cfgBytes, err := r.gw.ResolveImageConfig(ctx, ref, llb.ResolveImageConfigOpt{
		Platform:    &ctx.Session.Platform,
		ResolveMode: llb.ResolveModeDefault.String(),
	})
	if err != nil {
		return nil, err
	}
	var imgSpec specs.Image
	if err := json.Unmarshal(cfgBytes, &imgSpec); err != nil {
		return nil, err
	}
	img, err := filesystem.ImageFromStateAndConfig(ctx, st, imgSpec.Config, llb.Platform(ctx.Session.Platform))
	if err != nil {
		return nil, err
	}
	return img.ToFilesystem()
}

type localDir struct {
	ID string `json:"id"`
}

func (r *coreSchema) workdir(ctx *router.Context, parent any, args any) (localDir, error) {
	return localDir{r.workdirID}, nil
}

type dirArgs struct {
	ID string
}

func (r *coreSchema) dir(ctx *router.Context, parent any, args dirArgs) (localDir, error) {
	return localDir(args), nil
}

func (r *coreSchema) localDirRead(ctx *router.Context, parent localDir, args any) (*filesystem.Filesystem, error) {
	// copy to scratch to avoid making buildkit's snapshot of the local dir immutable,
	// which makes it unable to reused, which in turn creates cache invalidations
	// TODO: this should be optional, the above issue can also be avoided w/ readonly
	// mount when possible
	st := llb.Scratch().File(llb.Copy(llb.Local(
		parent.ID,
		// TODO: better shared key hint?
		llb.SharedKeyHint(parent.ID),
		// FIXME: should not be hardcoded
		llb.ExcludePatterns([]string{"**/node_modules"}),
	), "/", "/"))

	return r.Solve(ctx, st, r.hostPlatform, llb.LocalUniqueID(parent.ID))
}

// FIXME:(sipsma) have to make a new session to do a local export, need either gw support for exports or actually working session sharing to keep it all in the same session
type localDirWriteArgs struct {
	Contents filesystem.FSID
	Path     string
}

func (r *coreSchema) localDirWrite(ctx *router.Context, parent localDir, args localDirWriteArgs) (bool, error) {
	fs := filesystem.Filesystem{ID: args.Contents}

	workdir, err := filepath.Abs(r.solveOpts.LocalDirs[r.workdirID])
	if err != nil {
		return false, err
	}

	dest, err := filepath.Abs(filepath.Join(workdir, args.Path))
	if err != nil {
		return false, err
	}

	// Ensure the destination is a sub-directory of the workdir
	dest, err = filepath.EvalSymlinks(dest)
	if err != nil {
		return false, err
	}
	if !strings.HasPrefix(dest, workdir) {
		return false, fmt.Errorf("path %q is outside workdir", args.Path)
	}

	if err := r.Export(ctx, &fs, r.hostPlatform, bkclient.ExportEntry{
		Type:      bkclient.ExporterLocal,
		OutputDir: dest,
	}); err != nil {
		return false, err
	}
	return true, nil
}

type pushMultiplatformImageArgs struct {
	Ref         string
	Filesystems []filesystem.FSID
}

func (r *coreSchema) pushMultiplatformImage(ctx *router.Context, parent any, args pushMultiplatformImageArgs) (bool, error) {
	filesystems := make([]*filesystem.Filesystem, 0, len(args.Filesystems))
	for _, fsid := range args.Filesystems {
		filesystems = append(filesystems, &filesystem.Filesystem{ID: fsid})
	}

	if err := r.ExportMultiplatformImage(ctx, filesystems, bkclient.ExportEntry{
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
