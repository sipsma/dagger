package snapshots_test

import (
	"context"
	"testing"

	"github.com/containerd/containerd/v2/pkg/labels"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/dagger/dagger/engine/snapshots/testutil"
	"github.com/dagger/dagger/internal/buildkit/util/compression"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

// A chain exported with zstd carries zstd layers with their uncompressed
// digest annotated, imports into another store through the same layer path
// as any other chain, and the exporting snapshot keeps the zstd blob for
// its next export.
func TestZstdExportChainImports(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	producer, consumer := testutil.NewStore(t), testutil.NewStore(t)
	base, _ := producer.Build(t, nil, "base.txt", "base layer")
	top, _ := producer.Build(t, base, "dir/top.txt", "top layer, compressed with zstd")
	chain, err := top.ExportChain(ctx, config.RefConfig{Compression: compression.New(compression.Zstd)})
	require.NoError(t, err)
	defer chain.Release(context.Background())
	require.Len(t, chain.Layers, 2)
	for _, layer := range chain.Layers {
		require.Equal(t, ocispecs.MediaTypeImageLayerZstd, layer.Descriptor.MediaType)
		diffID := layer.Descriptor.Annotations[labels.LabelUncompressed]
		require.NotEmpty(t, diffID, "the uncompressed digest is annotated")
		require.NotEqual(t, layer.Descriptor.Digest.String(), diffID, "and differs from the compressed blob's")
	}

	imported, err := consumer.Manager.ImportChain(ctx, &bkcache.ExportChain{Layers: chain.Layers, Provider: &testutil.Provider{InfoReaderProvider: chain.Provider}})
	require.NoError(t, err)
	testutil.CheckFile(t, imported, "base.txt", "base layer")
	testutil.CheckFile(t, imported, "dir/top.txt", "top layer, compressed with zstd")
	require.NoError(t, imported.Release(ctx))

	// The zstd path diffs through its own walking differ, so the observed
	// differ's counter does not see it; content writes do. The first export
	// wrote both layers' blobs; the second writes nothing.
	writes := 0
	producer.BeforeWrite = func([]byte) error { writes++; return nil }
	third, _ := producer.Build(t, top, "third.txt", "a third layer, to see the writes")
	first, err := third.ExportChain(ctx, config.RefConfig{Compression: compression.New(compression.Zstd)})
	require.NoError(t, err)
	require.Positive(t, writes, "a fresh zstd diff writes its blob through the observed store")
	require.NoError(t, first.Release(ctx))
	writes = 0
	again, err := top.ExportChain(ctx, config.RefConfig{Compression: compression.New(compression.Zstd)})
	require.NoError(t, err)
	require.Equal(t, chain.Layers[1].Descriptor.Digest, again.Layers[1].Descriptor.Digest, "the zstd blob is reused")
	require.Zero(t, writes, "and nothing was written for it")
	require.NoError(t, again.Release(ctx))
}
