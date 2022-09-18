package core

import (
	"fmt"
	"path/filepath"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"go.dagger.io/dagger/core/filesystem"
	"go.dagger.io/dagger/router"
)

var _ router.ExecutableSchema = &coreSchema{}

type coreSchema struct {
	*baseSchema
	sshAuthSockID string
	workdirID     string
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

	"Fetch a git repository"
	git(remote: String!, ref: String): Filesystem!

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
			"core": router.PassthroughResolver,
			"host": router.PassthroughResolver,
		},
		"Core": router.ObjectResolver{
			"image":                  router.ToResolver(r.image),
			"git":                    router.ToResolver(r.git),
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

func (r *coreSchema) image(ctx *router.Context, parent struct{}, args imageArgs) (*filesystem.Filesystem, error) {
	return r.Solve(ctx, llb.Image(args.Ref))
}

type gitArgs struct {
	Remote string
	Ref    string
}

func (r *coreSchema) git(ctx *router.Context, parent struct{}, args gitArgs) (*filesystem.Filesystem, error) {
	var opts []llb.GitOption
	if r.sshAuthSockID != "" {
		opts = append(opts, llb.MountSSHSock(r.sshAuthSockID))
	}
	st := llb.Git(args.Remote, args.Ref, opts...)
	return r.Solve(ctx, st)
}

type localDir struct {
	ID string `json:"id"`
}

func (r *coreSchema) workdir(ctx *router.Context, parent, args struct{}) (localDir, error) {
	return localDir{r.workdirID}, nil
}

type dirArgs struct {
	ID string `json:"id"`
}

func (r *coreSchema) dir(ctx *router.Context, parent struct{}, args dirArgs) (any, error) {
	return localDir{args.ID}, nil
}

func (r *coreSchema) localDirRead(ctx *router.Context, parent localDir, args struct{}) (any, error) {
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

	return r.Solve(ctx, st, llb.LocalUniqueID(parent.ID))
}

type localDirWriteArgs struct {
	Contents filesystem.FSID
	Path     string
}

// FIXME:(sipsma) have to make a new session to do a local export, need either gw support for exports or actually working session sharing to keep it all in the same session
func (r *coreSchema) localDirWrite(ctx *router.Context, parent localDir, args localDirWriteArgs) (any, error) {
	fs := filesystem.Filesystem{ID: args.Contents}

	workdir, err := filepath.Abs(r.solveOpts.LocalDirs[r.workdirID])
	if err != nil {
		return nil, err
	}

	dest, err := filepath.Abs(filepath.Join(workdir, args.Path))
	if err != nil {
		return nil, err
	}

	// Ensure the destination is a sub-directory of the workdir
	dest, err = filepath.EvalSymlinks(dest)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(dest, workdir) {
		return nil, fmt.Errorf("path %q is outside workdir", args.Path)
	}

	if err := r.Export(ctx, &fs, bkclient.ExportEntry{
		Type:      bkclient.ExporterLocal,
		OutputDir: dest,
	}); err != nil {
		return nil, err
	}
	return true, nil
}

type pushMultiplatformImageArgs struct {
	Ref   string
	FSIDs []filesystem.FSID
}

func (r *coreSchema) pushMultiplatformImage(ctx *router.Context, parent any, args pushMultiplatformImageArgs) (bool, error) {
	var filesystems []*filesystem.Filesystem
	for _, fsid := range args.FSIDs {
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
