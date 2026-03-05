package dagql

import (
	"context"
	"fmt"

	"github.com/dagger/dagger/dagql/call"
)

type cachePersistedObjectResolver struct {
	cache *cache
}

func (c *cache) persistedObjectResolver() PersistedObjectResolver {
	return cachePersistedObjectResolver{cache: c}
}

func (r cachePersistedObjectResolver) LoadPersistedResult(ctx context.Context, id *call.ID) (AnyResult, error) {
	if r.cache == nil {
		return nil, fmt.Errorf("load persisted result: nil cache")
	}
	if id == nil {
		return nil, fmt.Errorf("load persisted result: nil ID")
	}

	res, err := r.cache.persistedSharedResultByID(id)
	if err != nil {
		return nil, err
	}

	wrapped, err := persistedResultForShared(res, id)
	if err != nil {
		return nil, err
	}

	ctx = ContextWithPersistedObjectResolver(ctx, r)
	return r.cache.ensurePersistedHitValueLoaded(ctx, wrapped)
}

func (r cachePersistedObjectResolver) LoadPersistedObject(ctx context.Context, id *call.ID) (AnyObjectResult, error) {
	res, err := r.LoadPersistedResult(ctx, id)
	if err != nil {
		return nil, err
	}
	obj, ok := res.(AnyObjectResult)
	if !ok {
		return nil, fmt.Errorf("load persisted object %q: result is %T", id.Digest(), res)
	}
	return obj, nil
}

func (r cachePersistedObjectResolver) PersistedSnapshotLinks(_ context.Context, id *call.ID) ([]PersistedSnapshotRefLink, error) {
	if r.cache == nil {
		return nil, fmt.Errorf("persisted snapshot links: nil cache")
	}
	if id == nil {
		return nil, fmt.Errorf("persisted snapshot links: nil ID")
	}

	res, err := r.cache.persistedSharedResultByID(id)
	if err != nil {
		return nil, err
	}

	r.cache.egraphMu.RLock()
	links := append([]PersistedSnapshotRefLink(nil), res.persistedSnapshotLinks...)
	r.cache.egraphMu.RUnlock()
	return links, nil
}

func (c *cache) persistedSharedResultByID(id *call.ID) (*sharedResult, error) {
	if c == nil {
		return nil, fmt.Errorf("resolve persisted result %q: nil cache", id.Digest())
	}
	if id == nil {
		return nil, fmt.Errorf("resolve persisted result: nil ID")
	}

	resKey, err := derivePersistResultKey(id)
	if err != nil {
		return nil, fmt.Errorf("resolve persisted result %q: %w", id.Digest(), err)
	}

	c.egraphMu.RLock()
	resID, ok := c.resultsByPersistKey[resKey]
	if !ok {
		c.egraphMu.RUnlock()
		return nil, fmt.Errorf("resolve persisted result %q: missing persisted result key %q", id.Digest(), resKey)
	}
	res := c.resultsByID[resID]
	c.egraphMu.RUnlock()
	if res == nil {
		return nil, fmt.Errorf("resolve persisted result %q: missing shared result %d", id.Digest(), resID)
	}
	return res, nil
}

func persistedResultForShared(res *sharedResult, requestedID *call.ID) (AnyResult, error) {
	if res == nil {
		return nil, fmt.Errorf("wrap persisted shared result: nil result")
	}
	if requestedID == nil {
		return nil, fmt.Errorf("wrap persisted shared result: nil requested ID")
	}

	retID := requestedID
	for _, extra := range res.outputExtraDigests {
		if extra.Digest == "" {
			continue
		}
		retID = retID.With(call.WithExtraDigest(extra))
	}
	retID = retID.AppendEffectIDs(res.outputEffectIDs...)

	retRes := Result[Typed]{
		shared:   res,
		id:       retID,
		hitCache: true,
	}
	if res.objType == nil {
		return retRes, nil
	}
	objRes, err := res.objType.New(retRes)
	if err != nil {
		return nil, fmt.Errorf("wrap persisted shared result %q: %w", requestedID.Digest(), err)
	}
	return objRes, nil
}
