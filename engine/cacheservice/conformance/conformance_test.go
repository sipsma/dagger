package conformance

import (
	"net/http/httptest"
	"testing"

	testservice "github.com/dagger/dagger/internal/testutil/cacheservice"
	"github.com/stretchr/testify/require"
)

// TestConformanceAgainstTestService is rung 1 of T-S9: the protocol suite
// green against the in-repo reference implementation. The same suite runs
// against the real dagger.io handlers on the service side, which is what
// keeps the two implementations from drifting.
func TestConformanceAgainstTestService(t *testing.T) {
	const token = "conformance-token"
	svc, err := testservice.New(t.TempDir(), token)
	require.NoError(t, err)
	server := httptest.NewServer(svc.Handler())
	t.Cleanup(server.Close)

	RunConformance(t, server.URL, token)
}
