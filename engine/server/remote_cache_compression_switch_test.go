package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/internal/buildkit/util/compression"
)

// The export compression switch names gzip or zstd, with an optional level;
// anything else is refused.
func TestParseExportCompression(t *testing.T) {
	for _, tc := range []struct {
		value string
		typ   compression.Type
		level int // 0 means no level
	}{
		{value: "gzip", typ: compression.Gzip},
		{value: "gzip:1", typ: compression.Gzip, level: 1},
		{value: "zstd", typ: compression.Zstd},
		{value: "zstd:7", typ: compression.Zstd, level: 7},
	} {
		cfg, err := parseExportCompression(tc.value)
		require.NoError(t, err, tc.value)
		require.Equal(t, tc.typ, cfg.Type, tc.value)
		if tc.level == 0 {
			require.Nil(t, cfg.Level, tc.value)
		} else {
			require.NotNil(t, cfg.Level, tc.value)
			require.Equal(t, tc.level, *cfg.Level, tc.value)
		}
	}
	for _, value := range []string{"", "lz4", "gzip:", "gzip:0", "gzip:10", "zstd:x", "zstd:23", "Zstd"} {
		_, err := parseExportCompression(value)
		require.Error(t, err, value)
	}
}
