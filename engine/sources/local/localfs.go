package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/continuity/sysx"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/contenthash"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/bklog"
	digest "github.com/opencontainers/go-digest"
	"github.com/tonistiigi/fsutil"
	fscopy "github.com/tonistiigi/fsutil/copy"
	"github.com/tonistiigi/fsutil/types"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
)

type localFSSharedState struct {
	rootPath string
	g        CachedSingleFlightGroup[string, *ChangeWithStat]
}

type ChangeWithStat struct {
	kind ChangeKind
	stat *HashedStatInfo
}

type localFS struct {
	*localFSSharedState

	subdir string

	filterFS fsutil.FS
	includes []string
	excludes []string
}

func NewLocalFS(sharedState *localFSSharedState, subdir string, includes, excludes []string) (*localFS, error) {
	baseFS, err := fsutil.NewFS(filepath.Join(sharedState.rootPath, subdir))
	if err != nil {
		return nil, fmt.Errorf("failed to create base fs: %w", err)
	}
	filterFS, err := fsutil.NewFilterFS(baseFS, &fsutil.FilterOpt{
		IncludePatterns: includes,
		ExcludePatterns: excludes,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create filter fs: %w", err)
	}

	return &localFS{
		localFSSharedState: sharedState,
		subdir:             subdir,
		filterFS:           filterFS,
		includes:           includes,
		excludes:           excludes,
	}, nil
}

// NOTE: This currently does not handle resetting parent dir modtimes to match the client. This matches
// upstream for now and avoids extra performance overhead + complication until a need for those modtimes
// matching the client's is proven.
func (local *localFS) Sync(
	ctx context.Context,
	remote ReadFS,
	cacheManager cache.Accessor,
	session session.Group,
	// TODO: ugly af
	forParents bool,
) (_ cache.ImmutableRef, rerr error) {
	var newCopyRef cache.MutableRef
	var cacheCtx contenthash.CacheContext
	if !forParents {
		var err error
		newCopyRef, err = cacheManager.New(ctx, nil, nil) // TODO: any opts? description? don't forget to set Retain once known
		if err != nil {
			return nil, fmt.Errorf("failed to create new copy ref: %w", err)
		}
		defer func() {
			ctx := context.WithoutCancel(ctx)
			if newCopyRef != nil {
				if err := newCopyRef.Release(ctx); err != nil {
					rerr = errors.Join(rerr, fmt.Errorf("failed to release copy ref: %w", err))
				}
			}
		}()

		cacheCtx, err = contenthash.GetCacheContext(ctx, newCopyRef)
		if err != nil {
			return nil, fmt.Errorf("failed to get cache context: %w", err)
		}
	}

	eg, egCtx := errgroup.WithContext(ctx)

	doubleWalkDiff(egCtx, eg, local, remote, func(kind ChangeKind, path string, lowerStat, upperStat *types.Stat) error {
		var appliedChange *ChangeWithStat
		var err error
		switch kind {
		case ChangeKindAdd, ChangeKindModify:
			switch {
			case upperStat.IsDir():
				appliedChange, err = local.Mkdir(egCtx, kind, path, upperStat)

			case upperStat.Mode&uint32(os.ModeDevice) != 0 || upperStat.Mode&uint32(os.ModeNamedPipe) != 0:
				// TODO:

			case upperStat.Mode&uint32(os.ModeSymlink) != 0:
				appliedChange, err = local.Symlink(egCtx, kind, path, upperStat)

			case upperStat.Linkname != "":
				appliedChange, err = local.Hardlink(egCtx, kind, path, upperStat)

			default:
				eg.Go(func() error {
					// TODO: DOUBLE CHECK IF YOU NEED TO COPY STAT OBJS SINCE THIS IS ASYNC
					appliedChange, err := local.WriteFile(egCtx, kind, path, upperStat, remote)
					if err != nil {
						return err
					}
					if cacheCtx != nil {
						if err := cacheCtx.HandleChange(appliedChange.kind, path, appliedChange.stat, nil); err != nil {
							return fmt.Errorf("failed to handle change in content hasher: %w", err)
						}
					}

					return nil
				})
				return nil
			}

		case ChangeKindDelete:
			_, err = local.RemoveAll(egCtx, path)
			if err != nil {
				return err
			}
			// no need to apply removals to the cacheCtx
			return nil

		case ChangeKindNone:
			appliedChange, err = local.GetPreviousChange(egCtx, path, lowerStat)

		default:
			return fmt.Errorf("unsupported change kind: %s", kind)
		}
		if err != nil {
			return err
		}
		if cacheCtx != nil {
			if err := cacheCtx.HandleChange(appliedChange.kind, path, appliedChange.stat, nil); err != nil {
				return fmt.Errorf("failed to handle change in content hasher: %w", err)
			}
		}

		return nil
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	if forParents {
		return nil, nil
	}

	// TODO: should probably provide ref impl that just errors if mount is attempted; should never be needed
	dgst, err := cacheCtx.Checksum(ctx, newCopyRef, "/", contenthash.ChecksumOpts{}, session)
	if err != nil {
		return nil, fmt.Errorf("failed to checksum: %w", err)
	}

	sis, err := SearchContentHash(ctx, cacheManager, dgst)
	if err != nil {
		return nil, fmt.Errorf("failed to search content hash: %w", err)
	}
	for _, si := range sis {
		finalRef, err := cacheManager.Get(ctx, si.ID(), nil)
		if err == nil {
			// TODO:
			// TODO:
			// TODO:
			bklog.G(ctx).Debugf("REUSING COPY REF: %s", finalRef.ID())
			return finalRef, nil
		} else {
			bklog.G(ctx).Debugf("failed to get cache ref: %v", err)
		}
	}

	copyRefMntable, err := newCopyRef.Mount(ctx, false, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get mountable: %w", err)
	}
	copyRefMnter := snapshot.LocalMounter(copyRefMntable)
	copyRefMntPath, err := copyRefMnter.Mount()
	if err != nil {
		return nil, fmt.Errorf("failed to mount: %w", err)
	}
	defer func() {
		if copyRefMnter != nil {
			if err := copyRefMnter.Unmount(); err != nil {
				rerr = errors.Join(rerr, fmt.Errorf("failed to unmount: %w", err))
			}
		}
	}()

	copyOpts := []fscopy.Opt{
		func(ci *fscopy.CopyInfo) {
			ci.IncludePatterns = local.includes
			ci.ExcludePatterns = local.excludes
			ci.CopyDirContents = true
		},
		fscopy.WithXAttrErrorHandler(func(dst, src, key string, err error) error {
			bklog.G(ctx).Debugf("xattr error during local import copy: %v", err)
			return nil
		}),
	}

	if err := fscopy.Copy(ctx,
		local.rootPath, local.subdir,
		copyRefMntPath, "/",
		copyOpts...,
	); err != nil {
		return nil, fmt.Errorf("failed to copy %q: %w", local.subdir, err)
	}

	if err := copyRefMnter.Unmount(); err != nil {
		copyRefMnter = nil
		return nil, fmt.Errorf("failed to unmount: %w", err)
	}
	copyRefMnter = nil

	finalRef, err := newCopyRef.Commit(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}
	defer func() {
		if rerr != nil {
			if finalRef != nil {
				ctx := context.WithoutCancel(ctx)
				if err := finalRef.Release(ctx); err != nil {
					rerr = errors.Join(rerr, fmt.Errorf("failed to release: %w", err))
				}
			}
		}
	}()

	if err := finalRef.Finalize(ctx); err != nil {
		return nil, fmt.Errorf("failed to finalize: %w", err)
	}
	if err := contenthash.SetCacheContext(ctx, finalRef, cacheCtx); err != nil {
		return nil, fmt.Errorf("failed to set cache context: %w", err)
	}
	if err := (CacheRefMetadata{finalRef}).SetContentHashKey(dgst); err != nil {
		return nil, fmt.Errorf("failed to set content hash key: %w", err)
	}
	if err := finalRef.SetCachePolicyRetain(); err != nil {
		return nil, fmt.Errorf("failed to set cache policy: %w", err)
	}

	// NOTE: this MUST be after setting cache policy retain or bk cache manager decides to
	// remove finalRef...
	if err := newCopyRef.Release(ctx); err != nil {
		newCopyRef = nil
		return nil, fmt.Errorf("failed to release: %w", err)
	}
	newCopyRef = nil

	return finalRef, nil
}

// the full absolute path on the local filesystem
func (local *localFS) toFullPath(path string) string {
	return filepath.Join(local.rootPath, local.subdir, path)
}

// the absolute path under local.rootPath
func (local *localFS) toRootPath(path string) string {
	return filepath.Join(local.subdir, path)
}

// TODO: comment on the logic here, a bit subtle (i.e. why we don't verifyExpectedChange)
func (local *localFS) GetPreviousChange(ctx context.Context, path string, stat *types.Stat) (*ChangeWithStat, error) {
	rootPath := local.toRootPath(path)
	return local.g.Do(ctx, rootPath, func(_ context.Context) (*ChangeWithStat, error) {
		fullPath := local.toFullPath(path)

		isRegular := stat.Mode&uint32(os.ModeType) == 0
		if isRegular {
			dgstBytes, err := sysx.Getxattr(fullPath, "user.daggerContentHash")
			if err != nil {
				return nil, fmt.Errorf("failed to get content hash xattr: %w", err)
			}
			return &ChangeWithStat{
				kind: ChangeKindNone,
				stat: &HashedStatInfo{
					StatInfo: StatInfo{stat},
					dgst:     digest.Digest(dgstBytes),
				},
			}, nil
		}

		return &ChangeWithStat{
			kind: ChangeKindNone,
			stat: &HashedStatInfo{
				StatInfo: StatInfo{stat},
				dgst:     digest.NewDigest(XXH3, newHashFromStat(stat)),
			},
		}, nil
	})
}

func (local *localFS) RemoveAll(ctx context.Context, path string) (*ChangeWithStat, error) {
	appliedChange, err := local.g.Do(ctx, local.toRootPath(path), func(ctx context.Context) (*ChangeWithStat, error) {
		fullPath := local.toFullPath(path)
		if err := os.RemoveAll(fullPath); err != nil {
			return nil, err
		}
		return &ChangeWithStat{kind: ChangeKindDelete}, nil
	})
	if err != nil {
		return nil, err
	}

	if err := verifyExpectedChange(path, appliedChange, ChangeKindDelete, nil); err != nil {
		return nil, err
	}
	return appliedChange, nil
}

func (local *localFS) Mkdir(ctx context.Context, expectedChangeKind ChangeKind, path string, upperStat *types.Stat) (*ChangeWithStat, error) {
	appliedChange, err := local.g.Do(ctx, local.toRootPath(path), func(ctx context.Context) (*ChangeWithStat, error) {
		fullPath := local.toFullPath(path)

		lowerStat, err := os.Lstat(fullPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to stat existing path: %w", err)
		}

		isNewDir := lowerStat == nil
		replacesNonDir := lowerStat != nil && !lowerStat.IsDir()

		if replacesNonDir {
			if err := os.Remove(fullPath); err != nil {
				return nil, fmt.Errorf("failed to remove existing file: %w", err)
			}
		}

		if isNewDir || replacesNonDir {
			if err := os.Mkdir(fullPath, os.FileMode(upperStat.Mode)&os.ModePerm); err != nil {
				return nil, fmt.Errorf("failed to create directory: %w", err)
			}
		}

		if err := rewriteMetadata(fullPath, upperStat); err != nil {
			return nil, fmt.Errorf("failed to rewrite directory metadata: %w", err)
		}

		return &ChangeWithStat{
			kind: expectedChangeKind,
			stat: &HashedStatInfo{
				StatInfo: StatInfo{upperStat},
				dgst:     digest.NewDigest(XXH3, newHashFromStat(upperStat)),
			},
		}, nil
	})
	if err != nil {
		return nil, err
	}

	if err := verifyExpectedChange(path, appliedChange, expectedChangeKind, upperStat); err != nil {
		return nil, err
	}
	return appliedChange, nil
}

func (local *localFS) Symlink(ctx context.Context, expectedChangeKind ChangeKind, path string, upperStat *types.Stat) (*ChangeWithStat, error) {
	appliedChange, err := local.g.Do(ctx, local.toRootPath(path), func(ctx context.Context) (*ChangeWithStat, error) {
		fullPath := local.toFullPath(path)

		lowerStat, err := os.Lstat(fullPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to stat existing path: %w", err)
		}

		isNewSymlink := lowerStat == nil
		replacesNonSymlink := lowerStat != nil && lowerStat.Mode()&fs.ModeSymlink == 0

		if replacesNonSymlink {
			if err := os.RemoveAll(fullPath); err != nil {
				return nil, fmt.Errorf("failed to remove existing file: %w", err)
			}
		}

		if isNewSymlink || replacesNonSymlink {
			if err := os.Symlink(upperStat.Linkname, fullPath); err != nil {
				return nil, fmt.Errorf("failed to create symlink: %w", err)
			}
		}

		return &ChangeWithStat{
			kind: expectedChangeKind,
			stat: &HashedStatInfo{
				StatInfo: StatInfo{upperStat},
				dgst:     digest.NewDigest(XXH3, newHashFromStat(upperStat)),
			},
		}, nil
	})
	if err != nil {
		return nil, err
	}

	if err := verifyExpectedChange(path, appliedChange, expectedChangeKind, upperStat); err != nil {
		return nil, err
	}
	return appliedChange, nil
}

func (local *localFS) Hardlink(ctx context.Context, expectedChangeKind ChangeKind, path string, upperStat *types.Stat) (*ChangeWithStat, error) {
	appliedChange, err := local.g.Do(ctx, local.toRootPath(path), func(ctx context.Context) (*ChangeWithStat, error) {
		fullPath := local.toFullPath(path)

		lowerStat, err := os.Lstat(fullPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to stat existing path: %w", err)
		}

		replacesExisting := lowerStat != nil

		if replacesExisting {
			if err := os.RemoveAll(fullPath); err != nil {
				return nil, fmt.Errorf("failed to remove existing file: %w", err)
			}
		}

		if err := os.Link(local.toFullPath(upperStat.Linkname), fullPath); err != nil {
			return nil, fmt.Errorf("failed to create hardlink: %w", err)
		}

		return &ChangeWithStat{
			kind: expectedChangeKind,
			stat: &HashedStatInfo{
				StatInfo: StatInfo{upperStat},
				dgst:     digest.NewDigest(XXH3, newHashFromStat(upperStat)),
			},
		}, nil
	})
	if err != nil {
		return nil, err
	}

	if err := verifyExpectedChange(path, appliedChange, expectedChangeKind, upperStat); err != nil {
		return nil, err
	}
	return appliedChange, nil
}

var copyBufferPool = &sync.Pool{
	New: func() interface{} {
		buffer := make([]byte, 32*1024)
		return &buffer
	},
}

func (local *localFS) WriteFile(ctx context.Context, expectedChangeKind ChangeKind, path string, upperStat *types.Stat, upperFS ReadFS) (*ChangeWithStat, error) {
	appliedChange, err := local.g.Do(ctx, local.toRootPath(path), func(ctx context.Context) (*ChangeWithStat, error) {
		reader, err := upperFS.ReadFile(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("failed to read file %q: %w", path, err)
		}
		defer reader.Close()

		fullPath := local.toFullPath(path)

		lowerStat, err := os.Lstat(fullPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to stat existing path: %w", err)
		}

		replacesExisting := lowerStat != nil

		if replacesExisting {
			if err := os.RemoveAll(fullPath); err != nil {
				return nil, fmt.Errorf("failed to remove existing file: %w", err)
			}
		}

		f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(upperStat.Mode)&os.ModePerm)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		h := newHashFromStat(upperStat)

		copyBuf := copyBufferPool.Get().(*[]byte)
		_, err = io.CopyBuffer(io.MultiWriter(f, h), reader, *copyBuf)
		copyBufferPool.Put(copyBuf)
		if err != nil {
			return nil, fmt.Errorf("failed to copy contents: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("failed to close file: %w", err)
		}

		if err := rewriteMetadata(fullPath, upperStat); err != nil {
			return nil, fmt.Errorf("failed to rewrite file metadata: %w", err)
		}

		dgst := digest.NewDigest(XXH3, h)
		// TODO: dedupe; constify
		if err := sysx.Setxattr(fullPath, "user.daggerContentHash", []byte(dgst.String()), 0); err != nil {
			return nil, fmt.Errorf("failed to set content hash xattr: %w", err)
		}

		return &ChangeWithStat{
			kind: expectedChangeKind,
			stat: &HashedStatInfo{
				StatInfo: StatInfo{upperStat},
				dgst:     dgst,
			},
		}, nil
	})
	if err != nil {
		return nil, err
	}

	if err := verifyExpectedChange(path, appliedChange, expectedChangeKind, upperStat); err != nil {
		return nil, err
	}
	return appliedChange, nil
}

func (local *localFS) Walk(ctx context.Context, path string, walkFn fs.WalkDirFunc) error {
	return local.filterFS.Walk(ctx, path, walkFn)
}

type StatInfo struct {
	*types.Stat
}

func (s *StatInfo) Name() string {
	return filepath.Base(s.Stat.Path)
}

func (s *StatInfo) Size() int64 {
	return s.Stat.Size_
}

func (s *StatInfo) Mode() os.FileMode {
	return os.FileMode(s.Stat.Mode)
}

func (s *StatInfo) ModTime() time.Time {
	return time.Unix(s.Stat.ModTime/1e9, s.Stat.ModTime%1e9)
}

func (s *StatInfo) IsDir() bool {
	return s.Mode().IsDir()
}

func (s *StatInfo) Sys() interface{} {
	return s.Stat
}

func (s *StatInfo) Type() fs.FileMode {
	return fs.FileMode(s.Stat.Mode)
}

func (s *StatInfo) Info() (fs.FileInfo, error) {
	return s, nil
}

func fsStatFromOs(fullPath string, fi os.FileInfo) (*types.Stat, error) {
	var link string
	if fi.Mode()&os.ModeSymlink != 0 {
		var err error
		link, err = os.Readlink(fullPath)
		if err != nil {
			return nil, err
		}
	}

	stat := &types.Stat{
		Mode:     uint32(fi.Mode()),
		Size_:    fi.Size(),
		ModTime:  fi.ModTime().UnixNano(),
		Linkname: link,
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		stat.Mode = stat.Mode | 0777
	}

	if err := setUnixOpt(fullPath, fi, stat); err != nil {
		return nil, err
	}

	return stat, nil
}

func setUnixOpt(path string, fi os.FileInfo, stat *types.Stat) error {
	s := fi.Sys().(*syscall.Stat_t)

	stat.Uid = s.Uid
	stat.Gid = s.Gid

	if !fi.IsDir() {
		if s.Mode&syscall.S_IFLNK == 0 && (s.Mode&syscall.S_IFBLK != 0 ||
			s.Mode&syscall.S_IFCHR != 0) {
			stat.Devmajor = int64(unix.Major(uint64(s.Rdev)))
			stat.Devminor = int64(unix.Minor(uint64(s.Rdev)))
		}
	}

	attrs, err := sysx.LListxattr(path)
	if err != nil {
		return err
	}
	if len(attrs) > 0 {
		stat.Xattrs = map[string][]byte{}
		for _, attr := range attrs {
			v, err := sysx.LGetxattr(path, attr)
			if err == nil {
				stat.Xattrs[attr] = v
			}
		}
	}
	return nil
}

type HashedStatInfo struct {
	StatInfo
	dgst digest.Digest
}

func (s *HashedStatInfo) Digest() digest.Digest {
	return s.dgst
}

func rewriteMetadata(p string, upperStat *types.Stat) error {
	for key, value := range upperStat.Xattrs {
		sysx.Setxattr(p, key, value, 0)
	}

	if err := os.Lchown(p, int(upperStat.Uid), int(upperStat.Gid)); err != nil {
		return fmt.Errorf("failed to change owner: %w", err)
	}

	if os.FileMode(upperStat.Mode)&os.ModeSymlink == 0 {
		if err := os.Chmod(p, os.FileMode(upperStat.Mode)); err != nil {
			return fmt.Errorf("failed to change mode: %w", err)
		}
	}

	if err := chtimes(p, upperStat.ModTime); err != nil {
		return fmt.Errorf("failed to change mod time: %w", err)
	}

	return nil
}

func chtimes(path string, un int64) error {
	var utimes [2]unix.Timespec
	utimes[0] = unix.NsecToTimespec(un)
	utimes[1] = utimes[0]

	if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, utimes[0:], unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("failed to call utimes: %w", err)
	}

	return nil
}

// TODO: need to handle .String() for ChangeKindNone
func verifyExpectedChange(path string, appliedChange *ChangeWithStat, expectedKind ChangeKind, expectedStat *types.Stat) error {
	if appliedChange.kind == ChangeKindDelete || expectedKind == ChangeKindDelete {
		if appliedChange.kind != expectedKind {
			return &ErrConflict{Path: path, FieldName: "change kind", OldVal: appliedChange.kind.String(), NewVal: expectedKind.String()}
		}
		// nothing else to compare for deletes
		return nil
	}

	if uint32(appliedChange.stat.Mode()) != expectedStat.Mode {
		return &ErrConflict{Path: path, FieldName: "mode", OldVal: fmt.Sprintf("%o", appliedChange.stat.Mode()), NewVal: fmt.Sprintf("%o", expectedStat.Mode)}
	}
	if appliedChange.stat.Uid != expectedStat.Uid {
		return &ErrConflict{Path: path, FieldName: "uid", OldVal: fmt.Sprintf("%d", appliedChange.stat.Uid), NewVal: fmt.Sprintf("%d", expectedStat.Uid)}
	}
	if appliedChange.stat.Gid != expectedStat.Gid {
		return &ErrConflict{Path: path, FieldName: "gid", OldVal: fmt.Sprintf("%d", appliedChange.stat.Gid), NewVal: fmt.Sprintf("%d", expectedStat.Gid)}
	}
	if appliedChange.stat.Size_ != expectedStat.Size_ {
		return &ErrConflict{Path: path, FieldName: "size", OldVal: fmt.Sprintf("%d", appliedChange.stat.Size()), NewVal: fmt.Sprintf("%d", expectedStat.Size_)}
	}
	if appliedChange.stat.Linkname != expectedStat.Linkname {
		return &ErrConflict{Path: path, FieldName: "linkname", OldVal: appliedChange.stat.Linkname, NewVal: expectedStat.Linkname}
	}
	if appliedChange.stat.Devmajor != expectedStat.Devmajor {
		return &ErrConflict{Path: path, FieldName: "devmajor", OldVal: fmt.Sprintf("%d", appliedChange.stat.Devmajor), NewVal: fmt.Sprintf("%d", expectedStat.Devmajor)}
	}
	if appliedChange.stat.Devminor != expectedStat.Devminor {
		return &ErrConflict{Path: path, FieldName: "devminor", OldVal: fmt.Sprintf("%d", appliedChange.stat.Devminor), NewVal: fmt.Sprintf("%d", expectedStat.Devminor)}
	}

	// Match the differ logic by only comparing modtime for regular files (as a heuristic to
	// expensive avoid content comparisons for ever file that appears in a diff, using the
	// modtime as a proxy instead).
	//
	// We don't want to compare modtimes for directories right now since we explicitly don't
	// attempt to reset parent dir modtimes when a dirent is synced in or removed.
	if appliedChange.stat.Mode().IsRegular() {
		if appliedChange.stat.ModTime().UnixNano() != expectedStat.ModTime {
			return &ErrConflict{Path: path, FieldName: "mod time", OldVal: fmt.Sprintf("%d", appliedChange.stat.ModTime().UnixNano()), NewVal: fmt.Sprintf("%d", expectedStat.ModTime)}
		}
	}

	return nil
}

type ErrConflict struct {
	Path      string
	FieldName string
	OldVal    string
	NewVal    string
}

func (e *ErrConflict) Error() string {
	return fmt.Sprintf("conflict at %s: %s changed from %s to %s", e.Path, e.FieldName, e.OldVal, e.NewVal)
}