package snapshots_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/pkg/namespaces"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/dagger/dagger/engine/snapshots/testutil"
	"github.com/dagger/dagger/internal/buildkit/util/compression"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	exportFilePasswd = "auser:x:1234:4321::/home/auser:/bin/sh\n"
	exportFileGroup  = "agroup:x:4321:\n"
)

// writeExportFile writes name under root as testutil.Build does, so
// testutil.CheckFile can check its mode and time.
func writeExportFile(t *testing.T, root, name, value string) {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(value), 0640))
	require.NoError(t, os.Chtimes(path, testutil.FileTime, testutil.FileTime))
}

// A file's chain is one layer holding the file, under parents with the
// source's metadata, and the account databases a named chown of it reads:
// none of the rest of the filesystem it came from.
func TestExportFileChainHoldsTheFileAlone(t *testing.T) {
	producer, consumer := testutil.NewStore(t), testutil.NewStore(t)
	base, _ := producer.BuildWith(t, nil, func(root string) {
		writeExportFile(t, root, "etc/passwd", exportFilePasswd)
		writeExportFile(t, root, "etc/group", exportFileGroup)
		writeExportFile(t, root, "src/output/keep.txt", "sibling bytes")
	})
	tree, _ := producer.BuildWith(t, base, func(root string) {
		writeExportFile(t, root, "src/big.bin", strings.Repeat("x", 1<<20))
		writeExportFile(t, root, "src/go.mod", "module example\n")
		require.NoError(t, os.Chmod(filepath.Join(root, "src"), 0750))
		require.NoError(t, os.Chtimes(filepath.Join(root, "src"), testutil.FileTime, testutil.FileTime))
	})
	require.Len(t, exportChain(t, tree).Layers, 2)

	chain, err := tree.ExportFileChain(context.Background(), transferConfig, "/src/go.mod")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, chain.Release(context.Background())) })
	require.Len(t, chain.Layers, 1)

	imported := importChain(t, consumer, supplied(chain, chain.Provider))
	testutil.CheckFile(t, imported, "src/go.mod", "module example\n")
	testutil.CheckFile(t, imported, "etc/passwd", exportFilePasswd)
	testutil.CheckFile(t, imported, "etc/group", exportFileGroup)
	root := testutil.Root(t, imported)
	src, err := os.Stat(filepath.Join(root, "src"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0750), src.Mode().Perm())
	require.Equal(t, testutil.FileTime, src.ModTime().UTC())
	require.NoFileExists(t, filepath.Join(root, "src/big.bin"))
	require.NoDirExists(t, filepath.Join(root, "src/output"))
}

// A file reached through a symlink keeps its whole chain.
func TestExportFileChainThroughASymlinkIsWhole(t *testing.T) {
	store := testutil.NewStore(t)
	base, _ := store.BuildWith(t, nil, func(root string) {
		writeExportFile(t, root, "real/go.mod", "module example\n")
		require.NoError(t, os.Symlink("real", filepath.Join(root, "src")))
	})
	tree, _ := store.Build(t, base, "other.txt", "other bytes")

	chain, err := tree.ExportFileChain(context.Background(), transferConfig, "/src/go.mod")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, chain.Release(context.Background())) })
	require.Len(t, chain.Layers, 2)
}

// A file that travels as its own content reports its own size. One reached
// through a symlink travels as its whole chain, and reports none.
func TestOwnFileSize(t *testing.T) {
	store := testutil.NewStore(t)
	sizer, ok := store.Manager.(interface {
		OwnFileSize(ctx context.Context, snapshotID, filePath string) (int64, bool, error)
	})
	require.True(t, ok, "the snapshot manager reports own file sizes")
	tree, _ := store.BuildWith(t, nil, func(root string) {
		writeExportFile(t, root, "src/go.mod", "module example\n")
		writeExportFile(t, root, "real/big.bin", strings.Repeat("x", 1<<20))
		require.NoError(t, os.Symlink("real", filepath.Join(root, "link")))
	})
	size, own, err := sizer.OwnFileSize(context.Background(), tree.SnapshotID(), "/src/go.mod")
	require.NoError(t, err)
	require.True(t, own)
	require.EqualValues(t, len("module example\n"), size)
	_, own, err = sizer.OwnFileSize(context.Background(), tree.SnapshotID(), "/link/big.bin")
	require.NoError(t, err)
	require.False(t, own, "a file reached through a symlink travels as its chain")
}

// A File exported again builds nothing: the first export built its layer,
// and a later export of the same file of the same snapshot takes it, as a
// snapshot's own layer is taken once built.
func TestExportFileChainBuildsItsLayerOnce(t *testing.T) {
	t.Parallel()
	store := testutil.NewStore(t)
	tree, _ := store.BuildWith(t, nil, func(root string) {
		writeExportFile(t, root, "bin/tool", listingBody("tool", 1<<20))
	})
	cfg := listingsConfig(compression.New(compression.Zstd))

	first, err := tree.ExportFileChain(context.Background(), cfg, "/bin/tool")
	require.NoError(t, err)
	require.Equal(t, 1, first.Stats.LayersDiffed)
	// Its export released and the store collected, the blobs are kept by
	// what keeps the snapshot.
	require.NoError(t, first.Release(context.Background()))
	collect(t, store)
	second := exportFileChain(t, tree, cfg, "bin/tool")
	require.Zero(t, second.Stats.LayersDiffed, "a later export of the same file builds nothing")
	require.Equal(t, 1, second.Stats.LayersReused)
	require.Equal(t, chainDigests(first), chainDigests(second))
	for _, layer := range second.Layers {
		require.False(t, layer.Computed, "the later export computed nothing")
	}

	// What it exports is the file, as the first export's was.
	imported := importChain(t, testutil.NewStore(t), supplied(second, second.Provider))
	testutil.CheckFile(t, imported, "bin/tool", listingBody("tool", 1<<20))

	// Another config makes other blobs: it builds its own.
	other := exportFileChain(t, tree, transferConfig, "/bin/tool")
	require.Equal(t, 1, other.Stats.LayersDiffed)
}

// Exports of one file at once build its layer once.
func TestExportFileChainsAtOnceBuildOnce(t *testing.T) {
	t.Parallel()
	store := testutil.NewStore(t)
	tree, _ := store.BuildWith(t, nil, func(root string) {
		writeExportFile(t, root, "bin/tool", listingBody("tool", 1<<20))
	})
	cfg := listingsConfig(compression.New(compression.Zstd))
	const exports = 4
	chains := make([]*bkcache.ExportChain, exports)
	var wg sync.WaitGroup
	for i := range exports {
		wg.Go(func() {
			chain, err := tree.ExportFileChain(context.Background(), cfg, "/bin/tool")
			assert.NoError(t, err)
			chains[i] = chain
		})
	}
	wg.Wait()
	diffed := 0
	for _, chain := range chains {
		require.NotNil(t, chain)
		t.Cleanup(func() { require.NoError(t, chain.Release(context.Background())) })
		diffed += chain.Stats.LayersDiffed
		require.Equal(t, chainDigests(chains[0]), chainDigests(chain))
	}
	require.Equal(t, 1, diffed)
}

// A file whose blobs left the store is built again.
func TestExportFileChainBuildsAgainWithoutItsBlobs(t *testing.T) {
	t.Parallel()
	store := testutil.NewStore(t)
	tree, _ := store.BuildWith(t, nil, func(root string) {
		writeExportFile(t, root, "bin/tool", listingBody("tool", 1<<20))
	})
	cfg := listingsConfig(compression.New(compression.Zstd))
	first, err := tree.ExportFileChain(context.Background(), cfg, "/bin/tool")
	require.NoError(t, err)
	digests := chainDigests(first)
	require.NoError(t, first.Release(context.Background()))
	// The engine's own store refuses deletes: they are the collector's.
	ctx := namespaces.WithNamespace(context.Background(), testutil.Namespace)
	for _, layer := range first.Layers {
		require.NoError(t, store.DB.ContentStore().Delete(ctx, layer.Descriptor.Digest))
	}

	again := exportFileChain(t, tree, cfg, "/bin/tool")
	require.Equal(t, 1, again.Stats.LayersDiffed)
	require.Equal(t, digests, chainDigests(again))
}

// The chains kept for later exports go when their snapshot is collected.
func TestFileChainsForgottenWhenCollected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testutil.NewStore(t)
	tree, owner := store.BuildWith(t, nil, func(root string) {
		writeExportFile(t, root, "bin/tool", "a small tool")
	})
	chain, err := tree.ExportFileChain(ctx, transferConfig, "/bin/tool")
	require.NoError(t, err)
	require.NoError(t, chain.Release(ctx))
	require.Equal(t, 1, bkcache.MetadataMemory(store.Manager).FileChains)

	require.NoError(t, tree.Release(ctx))
	require.NoError(t, store.Manager.RemoveLease(ctx, owner))
	collect(t, store)
	_, err = store.Snapshots.Stat(ctx, tree.SnapshotID())
	require.Error(t, err, "the snapshot is collected")
	require.Zero(t, bkcache.MetadataMemory(store.Manager).FileChains)
}

func exportFileChain(t *testing.T, ref bkcache.ImmutableRef, cfg config.RefConfig, filePath string) *bkcache.ExportChain {
	t.Helper()
	chain, err := ref.ExportFileChain(context.Background(), cfg, filePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, chain.Release(context.Background())) })
	return chain
}

func chainDigests(chain *bkcache.ExportChain) []string {
	var out []string
	for _, layer := range chain.Layers {
		out = append(out, layer.Descriptor.Digest.String())
	}
	return out
}
