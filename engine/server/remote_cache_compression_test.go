package server

import (
	"context"
	"testing"

	"github.com/dagger/dagger/internal/buildkit/util/compression"
	"github.com/stretchr/testify/require"
)

// The export compression reaches the adapter's export configuration:
// uncompressed by default, zstd when configured, nothing else accepted.
func TestRemoteCacheExportCompression(t *testing.T) {
	t.Parallel()
	ctx := boundedContext(t)
	run := func(ctx context.Context, _ *RemoteCacheAdapter) error {
		<-ctx.Done()
		return nil
	}
	require.ErrorContains(t, validateRemoteCacheIntegration(&RemoteCacheIntegrationConfig{Run: run, ExportCompression: compression.Gzip}), "must be uncompressed or zstd")

	for _, tc := range []struct {
		name string
		cfg  compression.Type
		want compression.Type
	}{
		{name: "default", cfg: nil, want: compression.Uncompressed},
		{name: "uncompressed", cfg: compression.Uncompressed, want: compression.Uncompressed},
		{name: "zstd", cfg: compression.Zstd, want: compression.Zstd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := &Server{engineCache: newGCTestCache(t), shutdownCtx: t.Context()}
			require.NoError(t, srv.startRemoteCacheIntegration(&RemoteCacheIntegrationConfig{Run: run, ExportCompression: tc.cfg}))
			defer func() { require.NoError(t, srv.stopRemoteCacheIntegration(ctx)) }()
			cfg := srv.remoteCacheAdapter.exportRefConfig()
			require.Equal(t, tc.want, cfg.Compression.Type)
			require.False(t, cfg.Compression.Force, "existing blobs are reused as they are")
		})
	}
}
