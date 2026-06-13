package dagql

import (
	"context"

	bkcache "github.com/dagger/dagger/engine/snapshots"
)

type RemoteSnapshotMaterializationRequest struct {
	ResultID uint64
	Role     string
	Chain    PersistedSnapshotChain
	Owner    AnyResult
}

func (c *Cache) MaterializeRemoteSnapshot(ctx context.Context, req RemoteSnapshotMaterializationRequest) (bkcache.ImmutableRef, bool, error) {
	return nil, false, nil
}
