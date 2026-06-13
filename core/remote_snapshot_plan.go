package core

import (
	"context"

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
