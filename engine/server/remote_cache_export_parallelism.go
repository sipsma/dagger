package server

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"golang.org/x/sync/semaphore"

	"github.com/dagger/dagger/engine/remotecache"
	"github.com/dagger/dagger/engine/snapshots/config"
)

// defaultExportParallelism is how many of an export's missing layer blobs
// build at once. Layers build independently: each is the diff of its
// snapshot against its parent's, compressed.
const defaultExportParallelism = 4

// maxExportParallelism bounds envExportParallelism.
const maxExportParallelism = 64

// envExportParallelism is a measurement switch, not a setting: it overrides
// defaultExportParallelism, so that one engine build can be compared with
// today's serial exports. 1 builds an export's layers one at a time, with no
// gate, as before.
const envExportParallelism = "_EXPERIMENTAL_DAGGER_EXPORT_PARALLELISM"

func init() {
	n := defaultExportParallelism
	if value := os.Getenv(envExportParallelism); value != "" {
		parsed, err := parseExportParallelism(value)
		if err != nil {
			slog.Warn("remote cache: ignoring the export parallelism override", "value", value, "error", err)
		} else {
			n = parsed
			slog.Info("remote cache: export parallelism overridden", "value", value)
		}
	}
	// Only the field: the compression switch sets another in its own init.
	exportRefConfig.Builds = exportLayerBuilds(n)
}

// exportLayerBuilds builds up to n layers of each export at once and, across
// all exports, up to n or remotecache.ExportsInFlight, whichever is more: the
// gate never holds exports to fewer builds than their serial export did, one
// each. Only building a blob takes the gate, so it bounds the engine's
// compression streams and layer reads. One builds serially with no gate.
func exportLayerBuilds(n int) config.LayerBuilds {
	if n <= 1 {
		return config.LayerBuilds{Workers: 1}
	}
	return config.LayerBuilds{Workers: n, Gate: semaphore.NewWeighted(int64(max(n, remotecache.ExportsInFlight)))}
}

// parseExportParallelism parses envExportParallelism's value.
func parseExportParallelism(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > maxExportParallelism {
		return 0, fmt.Errorf("invalid export parallelism %q: want 1 to %d", value, maxExportParallelism)
	}
	return n, nil
}
