package core

import (
	"context"
	"fmt"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/dagql/call"
	bkcache "github.com/dagger/dagger/engine/snapshots"
)

func encodePersistedCallID(id *call.ID) (string, error) {
	if id == nil {
		return "", fmt.Errorf("encode persisted call ID: nil ID")
	}
	return id.Encode()
}

func decodePersistedCallID(raw string) (*call.ID, error) {
	if raw == "" {
		return nil, nil
	}
	var id call.ID
	if err := id.Decode(raw); err != nil {
		return nil, fmt.Errorf("decode persisted call ID: %w", err)
	}
	return &id, nil
}

func loadPersistedObjectResult[T dagql.Typed](ctx context.Context, resolver dagql.PersistedObjectResolver, rawID string, label string) (dagql.ObjectResult[T], error) {
	if rawID == "" {
		return dagql.ObjectResult[T]{}, nil
	}
	id, err := decodePersistedCallID(rawID)
	if err != nil {
		return dagql.ObjectResult[T]{}, fmt.Errorf("load persisted %s ID: %w", label, err)
	}

	if resolver != nil {
		obj, err := resolver.LoadPersistedObject(ctx, id)
		if err == nil {
			typed, ok := obj.(dagql.ObjectResult[T])
			if !ok {
				return dagql.ObjectResult[T]{}, fmt.Errorf("load persisted %s: unexpected object result %T", label, obj)
			}
			return typed, nil
		}
	}

	srv, err := CurrentDagqlServer(ctx)
	if err != nil {
		return dagql.ObjectResult[T]{}, fmt.Errorf("load persisted %s object: %w", label, err)
	}
	loaded, err := dagql.NewID[T](id).Load(ctx, srv)
	if err != nil {
		return dagql.ObjectResult[T]{}, fmt.Errorf("load persisted %s object: %w", label, err)
	}
	return loaded, nil
}

func loadPersistedSnapshotLink(ctx context.Context, resolver dagql.PersistedObjectResolver, id *call.ID, role string) (dagql.PersistedSnapshotRefLink, error) {
	if resolver == nil {
		return dagql.PersistedSnapshotRefLink{}, fmt.Errorf("load persisted snapshot link: nil resolver")
	}
	if id == nil {
		return dagql.PersistedSnapshotRefLink{}, fmt.Errorf("load persisted snapshot link: nil ID")
	}
	links, err := resolver.PersistedSnapshotLinks(ctx, id)
	if err != nil {
		return dagql.PersistedSnapshotRefLink{}, err
	}
	for _, link := range links {
		if link.Role == role {
			return link, nil
		}
	}
	return dagql.PersistedSnapshotRefLink{}, fmt.Errorf("missing persisted snapshot link role %q for %s", role, id.Digest())
}

func loadPersistedImmutableSnapshot(ctx context.Context, resolver dagql.PersistedObjectResolver, id *call.ID, role string) (bkcache.ImmutableRef, dagql.PersistedSnapshotRefLink, error) {
	link, err := loadPersistedSnapshotLink(ctx, resolver, id, role)
	if err != nil {
		return nil, dagql.PersistedSnapshotRefLink{}, err
	}
	query, err := CurrentQuery(ctx)
	if err != nil {
		return nil, dagql.PersistedSnapshotRefLink{}, err
	}
	ref, err := query.BuildkitCache().GetBySnapshotID(ctx, link.RefKey, bkcache.NoUpdateLastUsed)
	if err != nil {
		return nil, dagql.PersistedSnapshotRefLink{}, fmt.Errorf("load persisted immutable snapshot %q: %w", link.RefKey, err)
	}
	return ref, link, nil
}

func loadPersistedMutableSnapshot(ctx context.Context, resolver dagql.PersistedObjectResolver, id *call.ID, role string) (bkcache.MutableRef, dagql.PersistedSnapshotRefLink, error) {
	link, err := loadPersistedSnapshotLink(ctx, resolver, id, role)
	if err != nil {
		return nil, dagql.PersistedSnapshotRefLink{}, err
	}
	query, err := CurrentQuery(ctx)
	if err != nil {
		return nil, dagql.PersistedSnapshotRefLink{}, err
	}
	ref, err := query.BuildkitCache().GetMutableBySnapshotID(ctx, link.RefKey, bkcache.NoUpdateLastUsed)
	if err != nil {
		return nil, dagql.PersistedSnapshotRefLink{}, fmt.Errorf("load persisted mutable snapshot %q: %w", link.RefKey, err)
	}
	return ref, link, nil
}

func retainImmutableRefChain(ctx context.Context, ref bkcache.ImmutableRef) error {
	if ref == nil {
		return nil
	}
	if err := ref.SetCachePolicyRetain(); err != nil {
		return err
	}
	chain := ref.LayerChain()
	defer chain.Release(context.WithoutCancel(ctx))
	for _, layer := range chain {
		if layer == nil {
			continue
		}
		if err := layer.SetCachePolicyRetain(); err != nil {
			return err
		}
	}
	return nil
}
