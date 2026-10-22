package server

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// An export builds up to 4 layers at once by default, and all exports
// together up to 4: the most today's serial exports build at once.
func TestExportLayerBuildsDefault(t *testing.T) {
	if os.Getenv(envExportParallelism) != "" {
		t.Skipf("%s is set", envExportParallelism)
	}
	builds := exportRefConfig.Builds
	require.Equal(t, 4, builds.Workers)
	require.NotNil(t, builds.Gate)
	require.True(t, builds.Gate.TryAcquire(4))
	require.False(t, builds.Gate.TryAcquire(1))
	builds.Gate.Release(4)
}

// The parallelism switch takes a positive count: 1 is today's serial export,
// with no gate; more widens the gate to match.
func TestExportLayerBuilds(t *testing.T) {
	serial := exportLayerBuilds(1)
	require.Equal(t, 1, serial.Workers)
	require.Nil(t, serial.Gate)
	wide := exportLayerBuilds(8)
	require.Equal(t, 8, wide.Workers)
	require.True(t, wide.Gate.TryAcquire(8))
	require.False(t, wide.Gate.TryAcquire(1))
	two := exportLayerBuilds(2)
	require.Equal(t, 2, two.Workers)
	require.True(t, two.Gate.TryAcquire(4), "the gate never drops below today's 4 exports")

	for value, want := range map[string]int{"1": 1, "2": 2, "16": 16} {
		n, err := parseExportParallelism(value)
		require.NoError(t, err, value)
		require.Equal(t, want, n, value)
	}
	for _, value := range []string{"", "0", "-1", "x", "1.5", "65"} {
		_, err := parseExportParallelism(value)
		require.Error(t, err, value)
	}
}
