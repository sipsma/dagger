package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/dagger/dagger/dagql/cachemoneyproto"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	snapshotconfig "github.com/dagger/dagger/engine/snapshots/config"
	"github.com/dagger/dagger/internal/buildkit/identity"
	"github.com/dagger/dagger/internal/buildkit/util/compression"
	"github.com/opencontainers/go-digest"
	ociidentity "github.com/opencontainers/image-spec/identity"
)

var ErrCachemoneyExportSnapshotStale = errors.New("cachemoney export snapshot stale")

// cachemoneyEmptySnapshotChainID is a cachemoney-local sentinel for snapshots
// whose content-addressed export chain has no layers. OCI ChainID has no
// non-empty identity for an empty diffID list, so this intentionally does not
// use digest syntax and cannot collide with a real OCI chain ID.
const cachemoneyEmptySnapshotChainID = "cachemoney-empty-snapshot-chain-v1"

type PreparedCachemoneyExport struct {
	MetadataDBPath string
	Manifest       cachemoneyproto.BeginExportManifest
	Release        func(context.Context) error
}

// WriteCachemoneyMetadataDB writes the cache's current metadata graph to dbPath
// without attaching export leases or constructing a local snapshot manifest.
// It is intended for metadata-only merge caches, such as the cachemoney backend.
// A real engine exporting local snapshots must use PrepareCachemoneyExport so
// local snapshot refs are leased while their content-addressed chains are being
// offered and uploaded.
func (c *Cache) WriteCachemoneyMetadataDB(ctx context.Context, dbPath string) error {
	if dbPath == "" {
		return errors.New("write cachemoney metadata DB: empty path")
	}
	snapshot, err := c.snapshotPersistState(ContextWithCachemoneyExport(ctx))
	if err != nil {
		return err
	}
	return writeCachemoneyMetadataDB(ctx, dbPath, snapshot)
}

func (c *Cache) PrepareCachemoneyExport(ctx context.Context, metadataDBPath string) (*PreparedCachemoneyExport, error) {
	if metadataDBPath == "" {
		return nil, errors.New("prepare cachemoney export: empty metadata DB path")
	}

	var lastErr error
	for range 2 {
		export, err := c.prepareCachemoneyExport(ctx, metadataDBPath)
		if errors.Is(err, ErrCachemoneyExportSnapshotStale) {
			lastErr = err
			continue
		}
		if err != nil {
			return nil, c.annotateCachemoneyExportError(ctx, err)
		}
		return export, nil
	}
	if lastErr != nil {
		return nil, c.annotateCachemoneyExportError(ctx, lastErr)
	}
	return nil, c.annotateCachemoneyExportError(ctx, ErrCachemoneyExportSnapshotStale)
}

func (c *Cache) prepareCachemoneyExport(ctx context.Context, metadataDBPath string) (*PreparedCachemoneyExport, error) {
	snapshot, err := c.snapshotPersistState(ContextWithCachemoneyExport(ctx))
	if err != nil {
		return nil, err
	}

	releaseExportLease, err := c.attachTemporaryCachemoneyExportLeases(ctx, &snapshot)
	if err != nil {
		if bkcache.IsNotFound(err) {
			return nil, fmt.Errorf("%w: attach export leases: %w", ErrCachemoneyExportSnapshotStale, err)
		}
		return nil, err
	}

	manifest, err := c.buildCachemoneyExportManifest(ctx, &snapshot)
	if err != nil {
		_ = releaseExportLease(context.WithoutCancel(ctx))
		if bkcache.IsNotFound(err) {
			return nil, fmt.Errorf("%w: build export manifest: %w", ErrCachemoneyExportSnapshotStale, err)
		}
		return nil, err
	}
	if err := writeCachemoneyMetadataDB(ctx, metadataDBPath, snapshot); err != nil {
		_ = releaseExportLease(context.WithoutCancel(ctx))
		return nil, err
	}

	return &PreparedCachemoneyExport{
		MetadataDBPath: metadataDBPath,
		Manifest:       manifest,
		Release:        releaseExportLease,
	}, nil
}

type cachemoneyExportSnapshotOwner struct {
	resultID sharedResultID
	role     string
	refKey   string
}

func (c *Cache) buildCachemoneyExportManifest(ctx context.Context, snapshot *persistStateSnapshot) (cachemoneyproto.BeginExportManifest, error) {
	manifest := cachemoneyproto.BeginExportManifest{
		Version: cachemoneyproto.Version,
	}
	if c == nil || snapshot == nil {
		return manifest, nil
	}

	owners := cachemoneyExportSnapshotOwners(snapshot)
	if len(owners) == 0 {
		return manifest, nil
	}
	if c.snapshotManager == nil {
		return manifest, errors.New("build cachemoney export manifest: nil snapshot manager")
	}

	resultsByID := make(map[sharedResultID]*persistResultSnapshot, len(snapshot.results))
	for i := range snapshot.results {
		resultsByID[snapshot.results[i].resultID] = &snapshot.results[i]
	}

	chainsByID := map[string]cachemoneyproto.SnapshotChain{}
	for _, owner := range owners {
		ref, err := c.snapshotManager.GetBySnapshotID(ctx, owner.refKey, bkcache.NoUpdateLastUsed)
		if err != nil {
			return manifest, fmt.Errorf("cachemoney export snapshot %q for result %d role %q: %w", owner.refKey, owner.resultID, owner.role, err)
		}
		result := resultsByID[owner.resultID]
		if result == nil {
			return manifest, fmt.Errorf("cachemoney export snapshot owner missing result %d", owner.resultID)
		}

		chain, exportErr := ref.ExportChain(ctx, cachemoneyExportRefConfig())
		releaseErr := ref.Release(context.WithoutCancel(ctx))
		if exportErr != nil {
			return manifest, fmt.Errorf("cachemoney export snapshot %q chain: %w", owner.refKey, exportErr)
		}
		if releaseErr != nil {
			return manifest, fmt.Errorf("cachemoney export snapshot %q release: %w", owner.refKey, releaseErr)
		}

		protoChain, err := cachemoneyProtoChainFromExportChain(chain)
		if err != nil {
			return manifest, fmt.Errorf("cachemoney export snapshot %q descriptor: %w", owner.refKey, err)
		}
		chainsByID[protoChain.ChainID] = protoChain
		manifest.Snapshots = append(manifest.Snapshots, cachemoneyproto.SnapshotOffer{
			ResultID: uint64(owner.resultID),
			Role:     cachemoneyproto.SnapshotRole(owner.role),
			ChainID:  protoChain.ChainID,
		})

		result.resultSnapshotChains = replaceCachemoneyResultSnapshotChain(result.resultSnapshotChains, persistdb.MirrorResultSnapshotChain{
			ResultID: int64(owner.resultID),
			Role:     owner.role,
			ChainID:  protoChain.ChainID,
		})
	}

	chainIDs := make([]string, 0, len(chainsByID))
	for chainID := range chainsByID {
		chainIDs = append(chainIDs, chainID)
	}
	slices.Sort(chainIDs)
	for _, chainID := range chainIDs {
		chain := chainsByID[chainID]
		manifest.Chains = append(manifest.Chains, chain)
		for pos, layer := range chain.Layers {
			snapshot.snapshotChainLayers = replaceCachemoneySnapshotChainLayer(snapshot.snapshotChainLayers, persistdb.MirrorSnapshotChainLayer{
				ChainID:        chain.ChainID,
				Position:       int64(pos),
				DiffID:         layer.DiffID,
				BlobDigest:     layer.BlobDigest,
				Size:           layer.Size,
				MediaType:      layer.MediaType,
				DescriptorJSON: string(layer.DescriptorJSON),
			})
		}
	}

	return manifest, nil
}

func replaceCachemoneyResultSnapshotChain(rows []persistdb.MirrorResultSnapshotChain, row persistdb.MirrorResultSnapshotChain) []persistdb.MirrorResultSnapshotChain {
	for i := range rows {
		if rows[i].ResultID == row.ResultID && rows[i].Role == row.Role {
			rows[i] = row
			return rows
		}
	}
	return append(rows, row)
}

func replaceCachemoneySnapshotChainLayer(rows []persistdb.MirrorSnapshotChainLayer, row persistdb.MirrorSnapshotChainLayer) []persistdb.MirrorSnapshotChainLayer {
	for i := range rows {
		if rows[i].ChainID == row.ChainID && rows[i].Position == row.Position {
			rows[i] = row
			return rows
		}
	}
	return append(rows, row)
}

func cachemoneyExportSnapshotOwners(snapshot *persistStateSnapshot) []cachemoneyExportSnapshotOwner {
	if snapshot == nil {
		return nil
	}
	seen := map[cachemoneyExportSnapshotOwner]struct{}{}
	for _, result := range snapshot.results {
		for _, link := range result.resultSnapshotLinks {
			if link.RefKey == "" {
				continue
			}
			owner := cachemoneyExportSnapshotOwner{
				resultID: result.resultID,
				role:     link.Role,
				refKey:   link.RefKey,
			}
			seen[owner] = struct{}{}
		}
	}

	owners := make([]cachemoneyExportSnapshotOwner, 0, len(seen))
	for owner := range seen {
		owners = append(owners, owner)
	}
	slices.SortFunc(owners, func(a, b cachemoneyExportSnapshotOwner) int {
		switch {
		case a.resultID < b.resultID:
			return -1
		case a.resultID > b.resultID:
			return 1
		case a.role < b.role:
			return -1
		case a.role > b.role:
			return 1
		case a.refKey < b.refKey:
			return -1
		case a.refKey > b.refKey:
			return 1
		default:
			return 0
		}
	})
	return owners
}

func cachemoneyProtoChainFromExportChain(chain *bkcache.ExportChain) (cachemoneyproto.SnapshotChain, error) {
	if chain == nil || len(chain.Layers) == 0 {
		return cachemoneyproto.SnapshotChain{
			ChainID: cachemoneyEmptySnapshotChainID,
		}, nil
	}

	diffIDs := make([]digest.Digest, 0, len(chain.Layers))
	layers := make([]cachemoneyproto.SnapshotLayer, 0, len(chain.Layers))
	for i, layer := range chain.Layers {
		desc := layer.Descriptor
		if desc.Digest == "" {
			return cachemoneyproto.SnapshotChain{}, fmt.Errorf("layer %d missing blob digest", i)
		}
		diffIDStr := desc.Annotations[labels.LabelUncompressed]
		if diffIDStr == "" {
			return cachemoneyproto.SnapshotChain{}, fmt.Errorf("layer %d %s missing diff ID annotation", i, desc.Digest)
		}
		diffID, err := digest.Parse(diffIDStr)
		if err != nil {
			return cachemoneyproto.SnapshotChain{}, fmt.Errorf("layer %d parse diff ID %q: %w", i, diffIDStr, err)
		}
		descJSON, err := json.Marshal(desc)
		if err != nil {
			return cachemoneyproto.SnapshotChain{}, fmt.Errorf("layer %d marshal descriptor: %w", i, err)
		}
		diffIDs = append(diffIDs, diffID)
		layers = append(layers, cachemoneyproto.SnapshotLayer{
			DiffID:         diffID.String(),
			BlobDigest:     desc.Digest.String(),
			Size:           desc.Size,
			MediaType:      desc.MediaType,
			DescriptorJSON: descJSON,
		})
	}

	return cachemoneyproto.SnapshotChain{
		ChainID: ociidentity.ChainID(diffIDs).String(),
		Layers:  layers,
	}, nil
}

func cachemoneyExportRefConfig() snapshotconfig.RefConfig {
	return snapshotconfig.RefConfig{
		Compression: compression.New(compression.Zstd).SetForce(true),
	}
}

func (c *Cache) attachTemporaryCachemoneyExportLeases(ctx context.Context, snapshot *persistStateSnapshot) (func(context.Context) error, error) {
	if c == nil || c.snapshotManager == nil || snapshot == nil {
		return func(context.Context) error { return nil }, nil
	}
	leaseID := "dagql/cachemoney/export/" + identity.NewID()
	seen := map[string]struct{}{}
	for _, result := range snapshot.results {
		for _, link := range result.resultSnapshotLinks {
			if link.RefKey == "" {
				continue
			}
			if _, ok := seen[link.RefKey]; ok {
				continue
			}
			seen[link.RefKey] = struct{}{}
			if err := c.snapshotManager.AttachLease(ctx, leaseID, link.RefKey); err != nil {
				_ = c.snapshotManager.RemoveLease(context.WithoutCancel(ctx), leaseID)
				return nil, err
			}
		}
	}
	return func(ctx context.Context) error {
		return c.snapshotManager.RemoveLease(ctx, leaseID)
	}, nil
}

func writeCachemoneyMetadataDB(ctx context.Context, dbPath string, snapshot persistStateSnapshot) error {
	if dbPath == "" {
		return errors.New("write cachemoney metadata DB: empty path")
	}
	if err := wipeSQLiteFiles(dbPath); err != nil {
		return fmt.Errorf("wipe cachemoney metadata DB: %w", err)
	}

	db, q, err := prepareCacheDBs(ctx, dbPath)
	if err != nil {
		return err
	}
	defer closeCacheDBs(db, q) //nolint:errcheck

	writer := &Cache{sqlDB: db, pdb: q}
	if err := writer.applyPersistStateSnapshot(ctx, snapshot); err != nil {
		return fmt.Errorf("write cachemoney metadata DB state: %w", err)
	}
	if err := q.UpsertMeta(ctx, persistdb.MetaKeySchemaVersion, cachePersistenceSchemaVersion); err != nil {
		return fmt.Errorf("write cachemoney metadata DB schema version: %w", err)
	}
	if err := q.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"); err != nil {
		return fmt.Errorf("write cachemoney metadata DB clean shutdown marker: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpoint cachemoney metadata DB: %w", err)
	}
	return nil
}
