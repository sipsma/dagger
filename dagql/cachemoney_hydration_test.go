package dagql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/dagger/dagger/dagql/cachemoneyproto"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/opencontainers/go-digest"
	ociidentity "github.com/opencontainers/image-spec/identity"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"gotest.tools/v3/assert"
)

func TestCachemoneyBlobHTTPClientHasTransportTimeouts(t *testing.T) {
	t.Parallel()

	assert.Assert(t, cachemoneyBlobHTTPClient != http.DefaultClient)
	assert.Equal(t, cachemoneyBlobHTTPClient.Timeout, cachemoneyBlobHTTPTimeout)
	transport, ok := cachemoneyBlobHTTPClient.Transport.(*http.Transport)
	assert.Assert(t, ok)
	assert.Equal(t, transport.ResponseHeaderTimeout, cachemoneyBlobHTTPResponseHeaderTimeout)
	assert.Assert(t, transport.TLSHandshakeTimeout > 0)
	assert.Assert(t, transport.ExpectContinueTimeout > 0)
}

func TestMaterializeRemoteSnapshotSkipsLocalBlobsAndImportsChain(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	chain, blobDigest, blobBytes := cachemoneyHydrationTestChain(t, "local")
	hydratedRef := &fakeCachemoneyExportRef{snapshotID: "hydrated-local"}
	manager := &fakeSnapshotManager{
		contentByDigest: map[digest.Digest][]byte{
			blobDigest: blobBytes,
		},
		importImageResult: hydratedRef,
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"hydrated-local": hydratedRef,
		},
	}
	c := cachemoneyHydrationTestCache(manager, "source-a")

	ref, ok, err := c.MaterializeRemoteSnapshot(ctx, RemoteSnapshotMaterializationRequest{
		ResultID: 1,
		Role:     "snapshot",
		Chain:    chain,
	})
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, ref.SnapshotID(), "hydrated-local")
	assert.DeepEqual(t, manager.writeContentCalls, []digest.Digest(nil))
	assert.Equal(t, len(manager.importImageCalls), 1)
	assert.Equal(t, len(manager.importImageCalls[0].Layers), 1)
	assert.DeepEqual(t, manager.attachCalls, []struct{ LeaseID, SnapshotID string }{{
		LeaseID:    "dagql/result/1/snapshot",
		SnapshotID: "hydrated-local",
	}})
	assert.DeepEqual(t, c.resultsByID[1].loadSnapshotOwnerLinks(), []PersistedSnapshotRefLink{{
		RefKey: "hydrated-local",
		Role:   "snapshot",
	}})
}

func TestMaterializeRemoteSnapshotFetchesMissingBlob(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	chain, blobDigest, blobBytes := cachemoneyHydrationTestChain(t, "fetch")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.Method, http.MethodGet)
		_, _ = w.Write(blobBytes)
	}))
	t.Cleanup(server.Close)

	hydratedRef := &fakeCachemoneyExportRef{snapshotID: "hydrated-fetch"}
	manager := &fakeSnapshotManager{
		importImageResult: hydratedRef,
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"hydrated-fetch": hydratedRef,
		},
	}
	c := cachemoneyHydrationTestCache(manager, "source-a")
	c.storeCachemoneyBlobIndex("source-a", map[string]cachemoneyproto.BlobLocation{
		blobDigest.String(): {
			URL:       server.URL,
			Size:      int64(len(blobBytes)),
			MediaType: ocispecs.MediaTypeImageLayerZstd,
		},
	})

	ref, ok, err := c.MaterializeRemoteSnapshot(ctx, RemoteSnapshotMaterializationRequest{
		ResultID: 1,
		Role:     "snapshot",
		Chain:    chain,
	})
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, ref.SnapshotID(), "hydrated-fetch")
	assert.DeepEqual(t, manager.writeContentCalls, []digest.Digest{blobDigest})
	assert.Equal(t, len(manager.importImageCalls), 1)
}

func TestMaterializeRemoteSnapshotRejectsInvalidDescriptor(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	chain, _, _ := cachemoneyHydrationTestChain(t, "invalid")
	chain.ChainID = digest.FromString("wrong-chain").String()
	manager := &fakeSnapshotManager{
		importImageResult: &fakeCachemoneyExportRef{snapshotID: "unexpected"},
	}
	c := cachemoneyHydrationTestCache(manager, "source-a")

	_, ok, err := c.MaterializeRemoteSnapshot(ctx, RemoteSnapshotMaterializationRequest{
		ResultID: 1,
		Role:     "snapshot",
		Chain:    chain,
	})
	assert.Assert(t, !ok)
	assert.ErrorContains(t, err, "chain ID mismatch")
	assert.Equal(t, len(manager.importImageCalls), 0)
}

func TestMaterializeRemoteSnapshotDedupesConcurrentChainHydration(t *testing.T) {
	t.Parallel()

	ctx := cacheTestContext(t.Context())
	chain, blobDigest, blobBytes := cachemoneyHydrationTestChain(t, "dedupe")
	hydratedRef := &fakeCachemoneyExportRef{snapshotID: "hydrated-dedupe"}
	started := make(chan struct{})
	release := make(chan struct{})
	callGate := make(chan struct{})
	ready := make(chan struct{}, 2)
	var importCalls atomic.Int32
	manager := &fakeSnapshotManager{
		contentByDigest: map[digest.Digest][]byte{
			blobDigest: blobBytes,
		},
		refsBySnapshotID: map[string]bkcache.ImmutableRef{
			"hydrated-dedupe": hydratedRef,
		},
		importImageFunc: func(context.Context, *bkcache.ImportedImage, bkcache.ImportImageOpts) (bkcache.ImmutableRef, error) {
			if importCalls.Add(1) == 1 {
				close(started)
			}
			<-release
			return hydratedRef, nil
		},
	}
	c := cachemoneyHydrationTestCache(manager, "source-a")

	eg, egCtx := errgroup.WithContext(ctx)
	for i := 0; i < 2; i++ {
		eg.Go(func() error {
			ready <- struct{}{}
			<-callGate
			ref, ok, err := c.MaterializeRemoteSnapshot(egCtx, RemoteSnapshotMaterializationRequest{
				ResultID: 1,
				Role:     "snapshot",
				Chain:    chain,
			})
			if err != nil {
				return err
			}
			if !ok {
				t.Fatal("expected hydration hit")
			}
			if ref.SnapshotID() != "hydrated-dedupe" {
				t.Fatalf("unexpected snapshot %s", ref.SnapshotID())
			}
			return nil
		})
	}
	<-ready
	<-ready
	close(callGate)
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	assert.NilError(t, eg.Wait())
	assert.Equal(t, importCalls.Load(), int32(1))
	assert.Equal(t, len(manager.importImageCalls), 1)
}

func TestSnapshotOwnerLinkRoleUpdatesPreserveConcurrentRolesAndClearRemoteChains(t *testing.T) {
	t.Parallel()

	res := &sharedResult{
		id: 1,
		remoteSnapshotChains: []PersistedSnapshotChain{{
			Role:    "fs",
			ChainID: "fs-remote",
		}, {
			Role:    "meta",
			ChainID: "meta-remote",
		}, {
			Role:    "mount_dir:0",
			ChainID: "mount-remote",
		}},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, update := range []PersistedSnapshotRefLink{{
		RefKey: "fs-local",
		Role:   "fs",
	}, {
		RefKey: "meta-local",
		Role:   "meta",
	}} {
		update := update
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res.setSnapshotOwnerLinkForRole(update.Role, update.RefKey)
		}()
	}
	close(start)
	wg.Wait()

	links := res.loadSnapshotOwnerLinks()
	sort.Slice(links, func(i, j int) bool {
		return links[i].Role < links[j].Role
	})
	assert.DeepEqual(t, links, []PersistedSnapshotRefLink{{
		RefKey: "fs-local",
		Role:   "fs",
	}, {
		RefKey: "meta-local",
		Role:   "meta",
	}})
	assert.DeepEqual(t, res.loadRemoteSnapshotChains(), []PersistedSnapshotChain{{
		Role:    "mount_dir:0",
		ChainID: "mount-remote",
	}})
}

func cachemoneyHydrationTestCache(manager *fakeSnapshotManager, sourceID string) *Cache {
	return &Cache{
		snapshotManager: manager,
		resultsByID: map[sharedResultID]*sharedResult{
			1: {
				id:                  1,
				originSourceID:      sourceID,
				remoteCacheImported: true,
			},
		},
	}
}

func cachemoneyHydrationTestChain(t *testing.T, seed string) (PersistedSnapshotChain, digest.Digest, []byte) {
	t.Helper()

	diffID := digest.FromString("diff-" + seed)
	blobBytes := []byte("blob-" + seed)
	blobDigest := digest.FromBytes(blobBytes)
	desc := ocispecs.Descriptor{
		MediaType: ocispecs.MediaTypeImageLayerZstd,
		Digest:    blobDigest,
		Size:      int64(len(blobBytes)),
		Annotations: map[string]string{
			labels.LabelUncompressed: diffID.String(),
		},
	}
	descJSON, err := json.Marshal(desc)
	assert.NilError(t, err)

	return PersistedSnapshotChain{
		Role:    "snapshot",
		ChainID: ociidentity.ChainID([]digest.Digest{diffID}).String(),
		Layers: []PersistedSnapshotChainLayer{{
			DiffID:         diffID.String(),
			BlobDigest:     blobDigest.String(),
			Size:           desc.Size,
			MediaType:      desc.MediaType,
			DescriptorJSON: descJSON,
		}},
	}, blobDigest, blobBytes
}
