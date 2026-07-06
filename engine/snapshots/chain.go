package snapshots

import (
	"context"
	"io"
	"slices"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/pkg/labels"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/dagger/dagger/internal/buildkit/client"
	"github.com/dagger/dagger/internal/buildkit/util/bklog"
	"github.com/dagger/dagger/internal/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	imagespecidentity "github.com/opencontainers/image-spec/identity"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// A content chain is the portable identity of one immutable snapshot: the
// ordered list of layers (uncompressed diff digest + compressed blob) that
// reconstructs it from a content-addressed blob store, identified by its
// containerd chainID. Chains are computed at export time only — nothing
// hashes layers during normal builds — and realized on demand at serving
// time by fetching missing blobs and applying them with the same machinery
// image pulls use.

// ChainLayer is one layer of a content chain.
type ChainLayer struct {
	DiffID    digest.Digest
	Blob      digest.Digest
	Size      int64
	MediaType string
}

// SnapshotChain is a full chain for one snapshot. A snapshot with no
// exportable layers (empty content) has zero layers and the deterministic
// EmptyChainID; materializing it produces an empty snapshot. An empty
// chainID is malformed everywhere — importers reject it — so every content
// promise has a real name.
type SnapshotChain struct {
	ChainID digest.Digest
	Layers  []ChainLayer
}

// EmptyChainID names the zero-layer chain (empty content).
var EmptyChainID = digest.FromString("dagger:empty-content-chain")

// ChainFetchStats tallies what a chain materialization actually moved:
// blobs fetched from the source into the content store (layers resolved by
// prefix reuse or already-present content fetch nothing) and their bytes.
type ChainFetchStats struct {
	Blobs int
	Bytes int64
}

func (s *ChainFetchStats) add(other ChainFetchStats) {
	s.Blobs += other.Blobs
	s.Bytes += other.Bytes
}

// chainExportCompression is the one pinned compression for chain blobs.
// The CAS dedups by blob digest, so per-engine compression variance would
// silently halve dedup; Force converts pre-existing variants (e.g. gzip
// blobs from image pulls) so every engine's chains name the same bytes.
// Changing the pin is a bundle-format version bump.
func chainExportCompression() compression.Config {
	return config.RefConfig{Compression: compression.New(compression.Zstd).SetForce(true)}.Compression
}

// BlobSource provides content-chain blobs by digest. Implementations map
// their "no such blob" onto ErrBlobNotFound; any other failure is treated
// as transient by the caller. The file/dir CAS implementation lives here;
// the cache-service HTTP client is another implementation of exactly this
// interface.
type BlobSource interface {
	OpenBlob(ctx context.Context, dgst digest.Digest, size int64) (io.ReadCloser, error)
}

var (
	// ErrBlobNotFound reports a blob a BlobSource does not have. Permanent
	// for this boot: retrying the same source cannot help.
	ErrBlobNotFound = errors.New("blob not found in source")
	// ErrChainBlobCorrupt reports fetched bytes that did not match the
	// blob's digest. The bytes are discarded; no repair is attempted.
	ErrChainBlobCorrupt = errors.New("chain blob digest mismatch")
)

// ChainForSnapshot computes (or looks up) the content chain for one
// immutable snapshot. First computation pays the diff/compress cost via
// ensureExportBlob; the resulting blob descriptors persist in the content
// store and the chain memoizes on the manager, so re-exports are lookups.
func (cm *snapshotManager) ChainForSnapshot(ctx context.Context, snapshotID string) (SnapshotChain, error) {
	if snapshotID == "" {
		return SnapshotChain{}, errors.New("chain for snapshot: empty snapshot ID")
	}

	cm.mu.Lock()
	if chain, memoized := cm.snapshotChains[snapshotID]; memoized {
		cm.mu.Unlock()
		return chain, nil
	}
	cm.mu.Unlock()

	// Holding the ref pins the record for the duration of the computation;
	// it also refuses non-immutable snapshots up front (mutable-owner
	// snapshots never produce chains).
	ref, err := cm.GetBySnapshotID(ctx, snapshotID, NoUpdateLastUsed)
	if err != nil {
		return SnapshotChain{}, errors.Wrapf(err, "chain for snapshot %s", snapshotID)
	}
	defer func() {
		_ = ref.Release(context.WithoutCancel(ctx))
	}()
	if _, ok := ref.(*immutableRef); !ok {
		return SnapshotChain{}, errors.Errorf("chain for snapshot %s: unexpected ref type %T", snapshotID, ref)
	}

	if leaseID, hasLease := leases.FromContext(ctx); !hasLease || leaseID == "" {
		ctx, err = EnsureLease(ctx)
		if err != nil {
			return SnapshotChain{}, err
		}
	}
	if leaseID, hasLease := leases.FromContext(ctx); !hasLease || leaseID == "" {
		leaseCtx, done, err := WithLease(ctx, cm.LeaseManager, MakeTemporary)
		if err != nil {
			return SnapshotChain{}, err
		}
		defer done(context.WithoutCancel(leaseCtx))
		ctx = leaseCtx
	}

	snapshotIDs := []string{}
	for currentID := snapshotID; currentID != ""; {
		snapshotIDs = append(snapshotIDs, currentID)
		info, err := cm.Snapshotter.Stat(ctx, currentID)
		if err != nil {
			return SnapshotChain{}, errors.Wrapf(err, "chain for snapshot %s: stat %s", snapshotID, currentID)
		}
		currentID = info.Parent
	}
	slices.Reverse(snapshotIDs)

	comp := chainExportCompression()
	var (
		chain            SnapshotChain
		diffIDs          []digest.Digest
		parentSnapshotID string
	)
	for _, currentID := range snapshotIDs {
		if parentSnapshotID == "" && isScratchSnapshotID(currentID) {
			parentSnapshotID = currentID
			continue
		}
		opened, err := cm.GetBySnapshotID(ctx, currentID, NoUpdateLastUsed)
		if err != nil {
			return SnapshotChain{}, errors.Wrapf(err, "chain for snapshot %s: open layer %s", snapshotID, currentID)
		}
		layer := opened.(*immutableRef)

		desc, hasLayer, err := cm.ensureExportBlob(ctx, parentSnapshotID, layer, comp)
		diffID := layer.md.getDiffID()
		if releaseErr := layer.Release(context.WithoutCancel(ctx)); releaseErr != nil && err == nil {
			err = releaseErr
		}
		if err != nil {
			return SnapshotChain{}, errors.Wrapf(err, "chain for snapshot %s: export layer %s", snapshotID, currentID)
		}

		if hasLayer {
			desc = exportDescriptor(desc, false)
			if diffID == "" {
				if fromAnnotation, parseErr := diffIDFromDescriptor(desc); parseErr == nil {
					diffID = fromAnnotation
				}
			}
			if diffID == "" {
				return SnapshotChain{}, errors.Errorf("chain for snapshot %s: layer %s has no diffID", snapshotID, currentID)
			}
			chain.Layers = append(chain.Layers, ChainLayer{
				DiffID:    diffID,
				Blob:      desc.Digest,
				Size:      desc.Size,
				MediaType: desc.MediaType,
			})
			diffIDs = append(diffIDs, diffID)
		}
		parentSnapshotID = currentID
	}
	if len(diffIDs) > 0 {
		chain.ChainID = imagespecidentity.ChainID(diffIDs)
	} else {
		chain.ChainID = EmptyChainID
	}

	cm.mu.Lock()
	cm.snapshotChains[snapshotID] = chain
	cm.mu.Unlock()
	return chain, nil
}

// MaterializeChain reconstructs a chain's snapshot locally: the longest
// prefix that already exists is reused via the imported-layer indexes, the
// remaining layers' blobs are fetched from src into the content store
// (digest-verified by the store's commit), and each layer applies with the
// image-pull machinery. The final snapshot is pinned under ownerLeaseID
// before the call returns, and its chain identity is recorded at arrival so
// a future export finds it memoized. The fetch stats report what actually
// moved — on failure too, since blobs fetched before the failure are real
// transfers (and stay ingested, so a retry does not move them again).
func (cm *snapshotManager) MaterializeChain(ctx context.Context, ownerLeaseID string, chain SnapshotChain, src BlobSource) (_ string, _ ChainFetchStats, rerr error) {
	var stats ChainFetchStats
	if ownerLeaseID == "" {
		return "", stats, errors.New("materialize chain: empty owner lease ID")
	}

	// A temporary lease keeps intermediate snapshots and ingested blobs
	// alive until the owner lease pins the final snapshot.
	leaseCtx, done, err := WithLease(ctx, cm.LeaseManager, MakeTemporary)
	if err != nil {
		return "", stats, err
	}
	defer done(context.WithoutCancel(leaseCtx))
	ctx = leaseCtx

	var current ImmutableRef
	defer func() {
		if current != nil {
			_ = current.Release(context.WithoutCancel(ctx))
		}
	}()

	opts := ImportImageOpts{RecordType: client.UsageRecordTypeRegular}
	for _, layer := range chain.Layers {
		desc := ocispecs.Descriptor{
			MediaType: layer.MediaType,
			Digest:    layer.Blob,
			Size:      layer.Size,
			Annotations: map[string]string{
				labels.LabelUncompressed: layer.DiffID.String(),
			},
		}

		// Fetch only when the layer will not resolve through the
		// imported-layer indexes. A stale index entry just means the apply
		// below fails transiently and the retry re-fetches.
		parentSnapshotID := ""
		if current != nil {
			parentSnapshotID = current.SnapshotID()
		}
		cm.mu.Lock()
		_, blobHit := cm.importedLayerByBlob[ImportedLayerBlobKey{ParentSnapshotID: parentSnapshotID, BlobDigest: layer.Blob}]
		_, diffHit := cm.importedLayerByDiff[ImportedLayerDiffKey{ParentSnapshotID: parentSnapshotID, DiffID: layer.DiffID}]
		cm.mu.Unlock()
		if !blobHit && !diffHit {
			fetched, err := cm.ensureChainBlob(ctx, desc, src)
			if fetched {
				stats.Blobs++
				stats.Bytes += desc.Size
			}
			if err != nil {
				return "", stats, err
			}
		}

		next, err := cm.importImageLayer(ctx, desc, current, opts)
		if err != nil {
			return "", stats, errors.Wrapf(err, "materialize chain: apply layer %s", layer.Blob)
		}
		if current != nil {
			_ = current.Release(context.WithoutCancel(ctx))
		}
		current = next
	}

	if current == nil {
		// A zero-layer chain is empty content: an empty snapshot, same as
		// an empty imported image rootfs.
		mut, err := cm.New(ctx, nil, nil,
			WithRecordType(opts.RecordType),
			WithDescription("materialized empty content chain"),
		)
		if err != nil {
			return "", stats, err
		}
		ref, err := mut.Commit(ctx)
		if err != nil {
			_ = mut.Release(context.WithoutCancel(ctx))
			return "", stats, err
		}
		current = ref
	}

	snapshotID := current.SnapshotID()
	if err := cm.AttachLease(ctx, ownerLeaseID, snapshotID); err != nil {
		return "", stats, errors.Wrapf(err, "materialize chain: pin snapshot %s", snapshotID)
	}

	// Identity recorded at arrival: a future export of this snapshot is a
	// lookup, never a re-hash.
	cm.mu.Lock()
	cm.snapshotChains[snapshotID] = chain
	cm.mu.Unlock()

	return snapshotID, stats, nil
}

// ensureChainBlob makes the blob present in the content store, fetching it
// from src when absent (fetched reports whether a transfer happened). The
// content store's commit verifies size and digest, so corrupt bytes never
// land: they surface as ErrChainBlobCorrupt and the partial ingest is
// discarded.
func (cm *snapshotManager) ensureChainBlob(ctx context.Context, desc ocispecs.Descriptor, src BlobSource) (fetched bool, _ error) {
	_, err := cm.ContentStore.Info(ctx, desc.Digest)
	if err == nil {
		return false, nil
	}
	if !cerrdefs.IsNotFound(err) {
		return false, errors.Wrapf(err, "stat chain blob %s", desc.Digest)
	}
	if src == nil {
		return false, errors.Wrapf(ErrBlobNotFound, "chain blob %s: no blob source configured", desc.Digest)
	}

	rc, err := src.OpenBlob(ctx, desc.Digest, desc.Size)
	if err != nil {
		return false, errors.Wrapf(err, "fetch chain blob %s", desc.Digest)
	}
	defer rc.Close()

	ref := "dagql-chain-blob-" + desc.Digest.String()
	if err := content.WriteBlob(ctx, cm.ContentStore, ref, rc, desc); err != nil {
		// A failed write leaves a resumable ingest under this ref, and a
		// retry would resume it — re-committing the SAME poisoned bytes (or
		// demanding a seek the blob source cannot honor). Discard means
		// discard: abort the ingest so the next attempt starts clean. The
		// fresh context matters — the failure may itself be a cancellation.
		abortCtx := context.WithoutCancel(ctx)
		if aerr := cm.ContentStore.Abort(abortCtx, ref); aerr != nil && !cerrdefs.IsNotFound(aerr) {
			bklog.G(ctx).WithError(aerr).Warnf("failed to abort chain blob ingest %q", ref)
		}
		// The store rejects a commit whose bytes do not match the expected
		// digest or size with a failed-precondition error: corrupt data,
		// dumbest treatment — discard and report.
		if cerrdefs.IsFailedPrecondition(err) {
			return false, errors.Wrapf(ErrChainBlobCorrupt, "chain blob %s: %v", desc.Digest, err)
		}
		return false, errors.Wrapf(err, "ingest chain blob %s", desc.Digest)
	}
	return true, nil
}

// OpenBlob opens a blob from the local content store for reading — the
// export-side counterpart of BlobSource, used to move chain blobs into a
// CAS.
func (cm *snapshotManager) OpenBlob(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
	ra, err := cm.ContentStore.ReaderAt(ctx, ocispecs.Descriptor{Digest: dgst})
	if err != nil {
		return nil, errors.Wrapf(err, "open blob %s", dgst)
	}
	return &readerAtCloser{Reader: content.NewReader(ra), closer: ra}, nil
}

type readerAtCloser struct {
	io.Reader
	closer interface{ Close() error }
}

func (r *readerAtCloser) Close() error {
	return r.closer.Close()
}
