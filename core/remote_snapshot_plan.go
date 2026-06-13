package core

import (
	"context"
	"slices"

	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
)

type remoteSnapshotAccessorPlan[T dagql.Typed] struct {
	resultID uint64
	role     string
	chain    dagql.PersistedSnapshotChain
}

func newRemoteSnapshotAccessorPlan[T dagql.Typed](resultID uint64, role string, chain dagql.PersistedSnapshotChain) *remoteSnapshotAccessorPlan[T] {
	return &remoteSnapshotAccessorPlan[T]{
		resultID: resultID,
		role:     role,
		chain:    clonePersistedSnapshotChain(chain),
	}
}

func (p *remoteSnapshotAccessorPlan[T]) Materialize(ctx context.Context, owner dagql.Result[T]) (bkcache.ImmutableRef, bool, error) {
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return nil, false, err
	}
	return cache.MaterializeRemoteSnapshot(ctx, dagql.RemoteSnapshotMaterializationRequest{
		ResultID: p.resultID,
		Role:     p.role,
		Chain:    clonePersistedSnapshotChain(p.chain),
		Owner:    owner,
	})
}

func (p *remoteSnapshotAccessorPlan[T]) ObserveMaterializationFallback(ctx context.Context, owner dagql.Result[T], materializerErr error, valueSet bool) {
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return
	}
	cache.RecordRemoteSnapshotMaterializationFallback(ctx, dagql.RemoteSnapshotMaterializationRequest{
		ResultID: p.resultID,
		Role:     p.role,
		Chain:    clonePersistedSnapshotChain(p.chain),
		Owner:    owner,
	}, materializerErr, valueSet)
}

func clonePersistedSnapshotChain(chain dagql.PersistedSnapshotChain) dagql.PersistedSnapshotChain {
	chain.Layers = append([]dagql.PersistedSnapshotChainLayer(nil), chain.Layers...)
	return chain
}

type remoteContainerDirectoryAccessorPlan struct {
	resultID uint64
	role     string
	chain    dagql.PersistedSnapshotChain
	dirPath  string
	platform Platform
	services ServiceBindings
}

func newRemoteContainerDirectoryAccessorPlan(resultID uint64, role string, dir *Directory, chain dagql.PersistedSnapshotChain) *remoteContainerDirectoryAccessorPlan {
	plan := &remoteContainerDirectoryAccessorPlan{
		resultID: resultID,
		role:     role,
		chain:    clonePersistedSnapshotChain(chain),
	}
	if dir != nil {
		if dirPath, ok := dir.Dir.Peek(); ok {
			plan.dirPath = dirPath
		}
		plan.platform = dir.Platform
		plan.services = slices.Clone(dir.Services)
	}
	return plan
}

func (p *remoteContainerDirectoryAccessorPlan) Materialize(ctx context.Context, owner dagql.Result[*Container]) (*Directory, bool, error) {
	ref, ok, err := p.materializeSnapshot(ctx, owner)
	if err != nil || !ok {
		return nil, ok, err
	}
	dir := &Directory{
		Platform: p.platform,
		Services: slices.Clone(p.services),
		Dir:      new(LazyAccessor[string, *Directory]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *Directory]),
	}
	if p.dirPath != "" {
		dir.Dir.setValue(p.dirPath)
	}
	dir.Snapshot.setValue(ref)
	return dir, true, nil
}

func (p *remoteContainerDirectoryAccessorPlan) ObserveMaterializationFallback(ctx context.Context, owner dagql.Result[*Container], materializerErr error, valueSet bool) {
	p.observeFallback(ctx, owner, materializerErr, valueSet)
}

func (p *remoteContainerDirectoryAccessorPlan) materializeSnapshot(ctx context.Context, owner dagql.Result[*Container]) (bkcache.ImmutableRef, bool, error) {
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return nil, false, err
	}
	return cache.MaterializeRemoteSnapshot(ctx, dagql.RemoteSnapshotMaterializationRequest{
		ResultID: p.resultID,
		Role:     p.role,
		Chain:    clonePersistedSnapshotChain(p.chain),
		Owner:    owner,
	})
}

func (p *remoteContainerDirectoryAccessorPlan) observeFallback(ctx context.Context, owner dagql.Result[*Container], materializerErr error, valueSet bool) {
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return
	}
	cache.RecordRemoteSnapshotMaterializationFallback(ctx, dagql.RemoteSnapshotMaterializationRequest{
		ResultID: p.resultID,
		Role:     p.role,
		Chain:    clonePersistedSnapshotChain(p.chain),
		Owner:    owner,
	}, materializerErr, valueSet)
}

type remoteContainerFileAccessorPlan struct {
	resultID uint64
	role     string
	chain    dagql.PersistedSnapshotChain
	filePath string
	platform Platform
	services ServiceBindings
}

func newRemoteContainerFileAccessorPlan(resultID uint64, role string, file *File, chain dagql.PersistedSnapshotChain) *remoteContainerFileAccessorPlan {
	plan := &remoteContainerFileAccessorPlan{
		resultID: resultID,
		role:     role,
		chain:    clonePersistedSnapshotChain(chain),
	}
	if file != nil {
		if filePath, ok := file.File.Peek(); ok {
			plan.filePath = filePath
		}
		plan.platform = file.Platform
		plan.services = slices.Clone(file.Services)
	}
	return plan
}

func (p *remoteContainerFileAccessorPlan) Materialize(ctx context.Context, owner dagql.Result[*Container]) (*File, bool, error) {
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return nil, false, err
	}
	ref, ok, err := cache.MaterializeRemoteSnapshot(ctx, dagql.RemoteSnapshotMaterializationRequest{
		ResultID: p.resultID,
		Role:     p.role,
		Chain:    clonePersistedSnapshotChain(p.chain),
		Owner:    owner,
	})
	if err != nil || !ok {
		return nil, ok, err
	}
	file := &File{
		Platform: p.platform,
		Services: slices.Clone(p.services),
		File:     new(LazyAccessor[string, *File]),
		Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File]),
	}
	if p.filePath != "" {
		file.File.setValue(p.filePath)
	}
	file.Snapshot.setValue(ref)
	return file, true, nil
}

func (p *remoteContainerFileAccessorPlan) ObserveMaterializationFallback(ctx context.Context, owner dagql.Result[*Container], materializerErr error, valueSet bool) {
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return
	}
	cache.RecordRemoteSnapshotMaterializationFallback(ctx, dagql.RemoteSnapshotMaterializationRequest{
		ResultID: p.resultID,
		Role:     p.role,
		Chain:    clonePersistedSnapshotChain(p.chain),
		Owner:    owner,
	}, materializerErr, valueSet)
}
