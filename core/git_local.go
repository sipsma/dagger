package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/continuity/fs"
	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	bkclient "github.com/dagger/dagger/internal/buildkit/client"
	"github.com/dagger/dagger/util/gitutil"
)

type LocalGitRepository struct {
	Directory dagql.ObjectResult[*Directory]
}

var _ GitRepositoryBackend = (*LocalGitRepository)(nil)

type LocalGitRef struct {
	*gitutil.Ref
	repo *LocalGitRepository
}

var _ GitRefBackend = (*LocalGitRef)(nil)

func (repo *LocalGitRepository) Get(ctx context.Context, ref *gitutil.Ref) (GitRefBackend, error) {
	return &LocalGitRef{
		Ref:  ref,
		repo: repo,
	}, nil
}

func (repo *LocalGitRepository) Remote(ctx context.Context) (*gitutil.Remote, error) {
	var remote *gitutil.Remote
	err := repo.mount(ctx, 0, false, nil, func(git *gitutil.GitCLI) error {
		gitURL, err := git.URL(ctx)
		if err != nil {
			return err
		}
		remote, err = gitutil.NewGitCLI().LsRemote(ctx, gitURL)
		return err
	})
	if err != nil {
		return nil, err
	}
	return remote, nil
}

func (repo *LocalGitRepository) File(ctx context.Context, filename string) (*File, error) {
	var gitDir string
	err := repo.mount(ctx, 0, false, nil, func(git *gitutil.GitCLI) error {
		dir, err := git.GitDir(ctx)
		if err != nil {
			return err
		}
		if filepath.IsAbs(dir) {
			dir, err = filepath.Rel(dir, git.Dir())
			if err != nil {
				return err
			}
		}
		gitDir = dir
		return nil
	})
	if err != nil {
		return nil, err
	}

	return repo.Directory.Self().Subfile(ctx, repo.Directory, filepath.Join(gitDir, filename))
}

func (repo *LocalGitRepository) Dirty(ctx context.Context) (inst dagql.ObjectResult[*Directory], rerr error) {
	return repo.Directory, nil
}

func (repo *LocalGitRepository) Cleaned(ctx context.Context) (inst dagql.ObjectResult[*Directory], rerr error) {
	srv := dagql.CurrentDagqlServer(ctx)
	query, err := CurrentQuery(ctx)
	if err != nil {
		return inst, err
	}
	dir := &Directory{Platform: query.Platform(), Dir: new(LazyAccessor[string, *Directory]), Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory])}
	unchanged, err := repo.cleanedInto(ctx, dir)
	if err != nil {
		return inst, err
	}
	if unchanged {
		return repo.Directory, nil
	}
	inst, err = dagql.NewObjectResultForCurrentCall(ctx, srv, dir)
	if err != nil {
		return inst, errors.Join(err, dir.OnRelease(context.WithoutCancel(ctx)))
	}
	return inst, nil
}

func (repo *LocalGitRepository) cleanedInto(ctx context.Context, dst *Directory) (unchanged bool, rerr error) {
	if err := validateProducedDirectoryReceiver(dst); err != nil {
		return false, err
	}
	query, err := CurrentQuery(ctx)
	if err != nil {
		return false, err
	}
	cache := query.SnapshotManager()

	parent, err := repo.Directory.Self().Snapshot.GetOrEval(ctx, repo.Directory.Result)
	if err != nil {
		return false, fmt.Errorf("get git directory snapshot: %w", err)
	}
	repoDirPath, err := repo.Directory.Self().Dir.GetOrEval(ctx, repo.Directory.Result)
	if err != nil {
		return false, fmt.Errorf("get git directory path: %w", err)
	}

	bkref, err := cache.New(ctx, parent,
		bkcache.WithRecordType(bkclient.UsageRecordTypeRegular),
		bkcache.WithDescription("git cleaned worktree"))

	if err != nil {
		return false, err
	}
	defer func() {
		if bkref != nil {
			rerr = errors.Join(rerr, bkref.Release(context.WithoutCancel(ctx)))
		}
	}()
	skip := false
	err = MountRef(ctx, bkref, func(parentRoot string, _ *mount.Mount) error {
		src, err := fs.RootPath(parentRoot, repoDirPath)
		if err != nil {
			return err
		}

		git := gitutil.NewGitCLI(gitutil.WithDir(src))
		worktree, err := git.WorkTree(ctx)
		if err != nil {
			return err
		}
		if worktree == "" {
			skip = true // no worktree, no changes
			return nil
		}
		gitDir, err := git.GitDir(ctx)
		if err != nil {
			return err
		}

		idx, err := os.Open(filepath.Join(gitDir, "index"))
		if err != nil {
			return err
		}
		defer idx.Close()

		// NOTE: apply the index to a temp file because "git restore --staged"
		// re-writes the index which we don't want to show up as a changed file
		// in the final result
		tmp, err := os.CreateTemp("", "dagger-git-index-")
		if err != nil {
			return err
		}
		return withTemporaryGitIndex(idx, tmp, func(indexPath string) error {
			git = git.New(gitutil.WithIndexFile(indexPath))

			// reset index to HEAD
			// NOTE: we cannot use "git reset --hard" because it writes every file,
			// which *kills* performance on overlayfs
			_, err = git.Run(ctx, "restore", "--staged", ".")
			if err != nil {
				return err
			}
			_, err = git.Run(ctx, "restore", ".")
			if err != nil {
				return err
			}
			_, err = git.Run(ctx, "clean", "-fd")
			if err != nil {
				return err
			}

			return nil
		})
	})
	if err != nil {
		return false, err
	}
	if skip {
		return true, nil
	}

	snap, err := bkref.Commit(ctx)
	if err != nil {
		return false, err
	}
	bkref = nil
	dst.SetPath(repoDirPath)
	dst.Services = slices.Clone(repo.Directory.Self().Services)
	dst.SetSnapshot(snap)
	return false, nil
}

// withTemporaryGitIndex owns the newly created index until the Git commands finish.
func withTemporaryGitIndex(idx io.Reader, tmp *os.File, run func(string) error) error {
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, idx); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return run(tmp.Name())
}

func (repo *LocalGitRepository) mount(ctx context.Context, depth int, includeTags bool, refs []GitRefBackend, fn func(*gitutil.GitCLI) error) error {
	query, err := CurrentQuery(ctx)
	if err != nil {
		return err
	}
	svcs, err := query.Services(ctx)
	if err != nil {
		return fmt.Errorf("failed to get services: %w", err)
	}
	detach, _, err := svcs.StartBindings(ctx, repo.Directory.Self().Services)
	if err != nil {
		return err
	}
	defer detach()

	ref, err := repo.Directory.Self().Snapshot.GetOrEval(ctx, repo.Directory.Result)
	if err != nil {
		return err
	}
	repoDirPath, err := repo.Directory.Self().Dir.GetOrEval(ctx, repo.Directory.Result)
	if err != nil {
		return err
	}

	return MountRef(ctx, ref, func(root string, _ *mount.Mount) error {
		src, err := fs.RootPath(root, repoDirPath)
		if err != nil {
			return err
		}

		git := gitutil.NewGitCLI(gitutil.WithDir(src))
		return fn(git)
	}, mountRefAsReadOnly)
}

func (ref *LocalGitRef) mount(ctx context.Context, depth int, includeTags bool, fn func(*gitutil.GitCLI) error) error {
	return ref.repo.mount(ctx, depth, includeTags, []GitRefBackend{ref}, fn)
}

func (ref *LocalGitRef) Tree(ctx context.Context, srv *dagql.Server, discardGitDir bool, depth int, includeTags bool) (_ *Directory, rerr error) {
	query, err := CurrentQuery(ctx)
	if err != nil {
		return nil, err
	}
	cache := query.SnapshotManager()

	bkref, err := cache.New(ctx, nil,
		bkcache.WithRecordType(bkclient.UsageRecordTypeRegular),
		bkcache.WithDescription(fmt.Sprintf("git local checkout (%s %s)", ref.Ref.Name, ref.Ref.SHA)))
	if err != nil {
		return nil, err
	}
	defer func() {
		if rerr != nil && bkref != nil {
			bkref.Release(context.WithoutCancel(ctx))
		}
	}()

	err = ref.mount(ctx, depth, includeTags, func(git *gitutil.GitCLI) error {
		gitURL, err := git.URL(ctx)
		if err != nil {
			return fmt.Errorf("could not find git url: %w", err)
		}

		return MountRef(ctx, bkref, func(checkoutDir string, _ *mount.Mount) error {
			checkoutDirGit := filepath.Join(checkoutDir, ".git")
			if err := os.MkdirAll(checkoutDir, 0711); err != nil {
				return err
			}
			checkoutGit := git.New(
				gitutil.WithDir(checkoutDir),
				gitutil.WithWorkTree(checkoutDir),
				gitutil.WithGitDir(checkoutDirGit),
			)
			return doGitCheckout(ctx, checkoutGit, "", gitURL, ref.Ref, depth, discardGitDir)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to checkout %s: %w", ref.Ref.Name, err)
	}

	snap, err := bkref.Commit(ctx)
	if err != nil {
		return nil, err
	}
	bkref = nil
	dir := &Directory{
		Platform: query.Platform(),
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	dir.SetPath("/")
	dir.SetSnapshot(snap)
	return dir, nil
}

const persistedDirectoryLazyKindGitCleaned = "gitCleaned"

type DirectoryGitCleanedLazy struct {
	LazyState
	Repo dagql.ObjectResult[*GitRepository]
}

type persistedDirectoryGitCleanedLazy struct {
	RepoResultID uint64 `json:"repoResultID"`
}

func (p *persistedDirectoryGitCleanedLazy) validate() error {
	if p.RepoResultID == 0 {
		return fmt.Errorf("DirectoryGitCleanedLazy: missing repoResultID")
	}
	return nil
}

var errGitCleanedUnchanged = errors.New("git cleaned input has no worktree")

func (lazy *DirectoryGitCleanedLazy) Evaluate(ctx context.Context, dir *Directory) error {
	err := lazy.evaluate(ctx, dir)
	if errors.Is(err, errGitCleanedUnchanged) {
		return fmt.Errorf("git cleaned operation: saved input has no worktree")
	}
	return err
}

// EvaluateForCall preserves the no-worktree alias before publishing this shell.
func (lazy *DirectoryGitCleanedLazy) EvaluateForCall(ctx context.Context, dir *Directory) (bool, error) {
	err := lazy.evaluate(ctx, dir)
	if errors.Is(err, errGitCleanedUnchanged) {
		return true, nil
	}
	return false, err
}

func (lazy *DirectoryGitCleanedLazy) evaluate(ctx context.Context, dir *Directory) error {
	return dir.evaluateLazy(ctx, &lazy.LazyState, "GitRepository.__cleaned", func(ctx context.Context) error {
		if err := validateProducedDirectoryReceiver(dir); err != nil {
			return err
		}
		if lazy.Repo.Self() == nil {
			return fmt.Errorf("git cleaned operation: missing Repo")
		}
		local, ok := lazy.Repo.Self().Backend.(*LocalGitRepository)
		if !ok {
			return fmt.Errorf("git cleaned operation: Repo is not local")
		}
		unchanged, err := local.cleanedInto(ctx, dir)
		if err != nil {
			return err
		}
		if unchanged {
			return errGitCleanedUnchanged
		}
		return nil
	})
}
func (lazy *DirectoryGitCleanedLazy) AttachDependencies(ctx context.Context, attach func(dagql.AnyResult) (dagql.AnyResult, error)) ([]dagql.AnyResult, error) {
	repo, err := attachCompletedProducerInput(attach, lazy.Repo, "DirectoryGitCleanedLazy.Repo")
	if err != nil {
		return nil, err
	}
	lazy.Repo = repo
	return []dagql.AnyResult{repo}, nil
}
func (lazy *DirectoryGitCleanedLazy) EncodePersisted(ctx context.Context, enc *dagql.PersistEncodeContext) (json.RawMessage, error) {
	repoID, err := encodePersistedObjectRef(enc, lazy.Repo, "DirectoryGitCleanedLazy.Repo")
	if err != nil {
		return nil, err
	}
	return json.Marshal(persistedDirectoryGitCleanedLazy{RepoResultID: repoID})
}
func decodeDirectoryGitCleanedLazy(ctx context.Context, dec *dagql.PersistDecodeContext, payload json.RawMessage) (Lazy[*Directory], error) {
	var p persistedDirectoryGitCleanedLazy
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("decode DirectoryGitCleanedLazy: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	repo, err := loadPersistedObjectResultByResultID[*GitRepository](ctx, dec, p.RepoResultID, "DirectoryGitCleanedLazy.Repo")
	if err != nil {
		return nil, err
	}
	return &DirectoryGitCleanedLazy{LazyState: NewLazyState(), Repo: repo}, nil
}
