package dagql

import (
	"context"
	"fmt"

	bkcache "github.com/dagger/dagger/engine/snapshots"
)

func (c *Cache) HydrateSnapshot(ctx context.Context, link PersistedSnapshotRefLink) (bkcache.ImmutableRef, error) {
	if c == nil {
		return nil, fmt.Errorf("hydrate snapshot %q: nil cache", link.RefKey)
	}
	if link.RefKey == "" {
		return nil, fmt.Errorf("hydrate snapshot: empty ref key")
	}
	if link.IsLocal() {
		if c.snapshotManager == nil {
			return nil, fmt.Errorf("hydrate local snapshot %q: nil snapshot manager", link.RefKey)
		}
		return c.snapshotManager.GetBySnapshotID(ctx, link.RefKey, bkcache.NoUpdateLastUsed)
	}

	c.egraphMu.RLock()
	source := c.importSources[link.SourceID]
	c.egraphMu.RUnlock()
	if source == nil {
		return nil, fmt.Errorf("hydrate external snapshot %q: unknown cache import source %q", link.RefKey, link.SourceID)
	}
	if source.Bundle == nil {
		return nil, fmt.Errorf("hydrate external snapshot %q from source %q: nil cache bundle", link.RefKey, link.SourceID)
	}
	return source.Bundle.HydrateSnapshot(ctx, link.RefKey, c.snapshotManager)
}
