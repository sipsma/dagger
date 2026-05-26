package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
)

type ExternalSnapshotLazy interface {
	ExternalSnapshotLink() dagql.PersistedSnapshotRefLink
}

type RemoteDirectorySnapshotLazy struct {
	LazyState
	Link dagql.PersistedSnapshotRefLink
}

func (lazy *RemoteDirectorySnapshotLazy) ExternalSnapshotLink() dagql.PersistedSnapshotRefLink {
	if lazy == nil {
		return dagql.PersistedSnapshotRefLink{}
	}
	return lazy.Link
}

func (lazy *RemoteDirectorySnapshotLazy) Evaluate(ctx context.Context, dir *Directory) error {
	return lazy.LazyState.Evaluate(ctx, "Directory.remoteSnapshot", func(ctx context.Context) error {
		cache, err := dagql.EngineCache(ctx)
		if err != nil {
			return err
		}
		ref, err := cache.HydrateSnapshot(ctx, lazy.Link)
		if err != nil {
			return err
		}
		if dir.Snapshot == nil {
			dir.Snapshot = new(LazyAccessor[bkcache.ImmutableRef, *Directory])
		}
		dir.Snapshot.setValue(ref)
		return nil
	})
}

func (lazy *RemoteDirectorySnapshotLazy) AttachDependencies(context.Context, func(dagql.AnyResult) (dagql.AnyResult, error)) ([]dagql.AnyResult, error) {
	return nil, nil
}

func (lazy *RemoteDirectorySnapshotLazy) EncodePersisted(context.Context, dagql.PersistedObjectCache) (json.RawMessage, error) {
	return nil, fmt.Errorf("%w: remote directory snapshot lazy persists as a snapshot link", dagql.ErrPersistStateNotReady)
}

type RemoteFileSnapshotLazy struct {
	LazyState
	Link dagql.PersistedSnapshotRefLink
}

func (lazy *RemoteFileSnapshotLazy) ExternalSnapshotLink() dagql.PersistedSnapshotRefLink {
	if lazy == nil {
		return dagql.PersistedSnapshotRefLink{}
	}
	return lazy.Link
}

func (lazy *RemoteFileSnapshotLazy) Evaluate(ctx context.Context, file *File) error {
	return lazy.LazyState.Evaluate(ctx, "File.remoteSnapshot", func(ctx context.Context) error {
		cache, err := dagql.EngineCache(ctx)
		if err != nil {
			return err
		}
		ref, err := cache.HydrateSnapshot(ctx, lazy.Link)
		if err != nil {
			return err
		}
		if file.Snapshot == nil {
			file.Snapshot = new(LazyAccessor[bkcache.ImmutableRef, *File])
		}
		file.Snapshot.setValue(ref)
		return nil
	})
}

func (lazy *RemoteFileSnapshotLazy) AttachDependencies(context.Context, func(dagql.AnyResult) (dagql.AnyResult, error)) ([]dagql.AnyResult, error) {
	return nil, nil
}

func (lazy *RemoteFileSnapshotLazy) EncodePersisted(context.Context, dagql.PersistedObjectCache) (json.RawMessage, error) {
	return nil, fmt.Errorf("%w: remote file snapshot lazy persists as a snapshot link", dagql.ErrPersistStateNotReady)
}
