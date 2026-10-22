package server

import (
	"compress/gzip"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/dagger/dagger/internal/buildkit/util/compression"
)

// envExportCompression is a measurement switch, not a setting: it overrides
// how exported layers are compressed, so that one engine build can be
// compared across compressors. Its value is gzip or zstd, with an optional
// level after a colon, such as zstd:7; gzip alone is the engine's default
// gzip. Unset, exports use exportRefConfig's compressor.
const envExportCompression = "_EXPERIMENTAL_DAGGER_REMOTE_CACHE_EXPORT_COMPRESSION"

func init() {
	value := os.Getenv(envExportCompression)
	if value == "" {
		return
	}
	cfg, err := parseExportCompression(value)
	if err != nil {
		slog.Warn("remote cache: ignoring the export compression override", "value", value, "error", err)
		return
	}
	exportRefConfig.Compression = cfg
	slog.Info("remote cache: export compression overridden", "value", value)
}

// parseExportCompression parses envExportCompression's value.
func parseExportCompression(value string) (compression.Config, error) {
	name, level, hasLevel := strings.Cut(value, ":")
	var typ compression.Type
	var maxLevel int
	switch name {
	case "gzip":
		typ, maxLevel = compression.Gzip, gzip.BestCompression
	case "zstd":
		typ, maxLevel = compression.Zstd, 22
	default:
		return compression.Config{}, fmt.Errorf("unsupported export compression %q", name)
	}
	cfg := compression.New(typ)
	if !hasLevel {
		return cfg, nil
	}
	n, err := strconv.Atoi(level)
	if err != nil || n < 1 || n > maxLevel {
		return compression.Config{}, fmt.Errorf("invalid %s level %q", name, level)
	}
	return cfg.SetLevel(n), nil
}
