package containerd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/core/snapshots/storage"
	"github.com/containerd/continuity/fs"
	cerrdefs "github.com/containerd/errdefs"
	bksnapshots "github.com/dagger/dagger/engine/snapshots"
)

// NewUsageFirstSnapshotter wraps an overlay snapshotter, rooted at root, whose
// metadata store ms the engine opened and passed it (overlay.WithMetaStore),
// so that a snapshot's commit records the disk usage measured before it.
//
// containerd's metadata database commits a snapshot by committing its
// backend inside the database's write transaction, and overlay's commit
// walks the snapshot's whole upper directory for its usage there. That holds
// the database's single writer, which every lease, snapshot and blob write in
// the engine needs, for the length of the walk. The engine measures a
// snapshot before its commit (bksnapshots.CommitUsage) through Usage, which
// walks the same directory as overlay's Usage does but outside any
// transaction; Commit then records that measurement as overlay's commit
// records its own. A commit with no measurement for its snapshot measures
// as overlay's does.
func NewUsageFirstSnapshotter(sn snapshots.Snapshotter, ms *storage.MetaStore, root string) snapshots.Snapshotter {
	return &usageFirstSnapshotter{Snapshotter: sn, ms: ms, root: root}
}

type usageFirstSnapshotter struct {
	snapshots.Snapshotter
	ms   *storage.MetaStore
	root string
}

// upperPath is overlay's directory for a snapshot's own files
// (plugins/snapshots/overlay, upperPath).
func (s *usageFirstSnapshotter) upperPath(id string) string {
	return filepath.Join(s.root, "snapshots", id, "fs")
}

// Usage measures an active snapshot for its commit when the context asks,
// as overlay's Usage measures one, and keeps the measurement in the
// context's slot.
func (s *usageFirstSnapshotter) Usage(ctx context.Context, key string) (snapshots.Usage, error) {
	slot := bksnapshots.CommitUsageFromContext(ctx)
	if slot == nil || slot.Measured {
		return s.Snapshotter.Usage(ctx, key)
	}
	var (
		id    string
		info  snapshots.Info
		usage snapshots.Usage
	)
	if err := s.ms.WithTransaction(ctx, false, func(ctx context.Context) (err error) {
		id, info, usage, err = storage.GetInfo(ctx, key)
		return err
	}); err != nil {
		return snapshots.Usage{}, err
	}
	if info.Kind != snapshots.KindActive {
		return usage, nil
	}
	du, err := fs.DiskUsage(ctx, s.upperPath(id))
	if err != nil {
		return snapshots.Usage{}, err
	}
	*slot = bksnapshots.CommitUsage{Key: key, ID: id, Usage: snapshots.Usage(du), Measured: true}
	return slot.Usage, nil
}

// Commit commits as overlay's Commit does, recording the usage measured
// before it when the context holds one for this snapshot.
func (s *usageFirstSnapshotter) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) error {
	slot := bksnapshots.CommitUsageFromContext(ctx)
	if slot == nil || !slot.Measured || slot.Key != key {
		return s.Snapshotter.Commit(ctx, name, key, opts...)
	}
	return s.ms.WithTransaction(ctx, true, func(ctx context.Context) error {
		id, _, _, err := storage.GetInfo(ctx, key)
		if err != nil {
			return err
		}
		// Overlay's walk fails a canceled commit; so does a measured one.
		if err := ctx.Err(); err != nil {
			return err
		}
		usage := slot.Usage
		if id != slot.ID {
			du, err := fs.DiskUsage(ctx, s.upperPath(id))
			if err != nil {
				return err
			}
			usage = snapshots.Usage(du)
		}
		if _, err := storage.CommitActive(ctx, key, name, usage, opts...); err != nil {
			return fmt.Errorf("failed to commit snapshot %s: %w", key, err)
		}
		return nil
	})
}

// Cleanup keeps overlay's removal of the directories of removed snapshots,
// which the metadata database's garbage collection calls.
func (s *usageFirstSnapshotter) Cleanup(ctx context.Context) error {
	if c, ok := s.Snapshotter.(snapshots.Cleaner); ok {
		return c.Cleanup(ctx)
	}
	return cerrdefs.ErrNotImplemented
}
