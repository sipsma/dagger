package engineutil

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

func TestSpike6Gate(t *testing.T) {
	t.Setenv("_DAGGER_SPIKE6", "1")
	spec := &specs.Spec{Process: &specs.Process{Args: []string{initPath, "true"}, Env: []string{"DECLARED=value", "_DAGGER_SPIKE6_TOKEN=test"}}}
	var fds initFDs
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	g, err := newSpike6Gate(ctx, spec, &fds)
	require.NoError(t, err)
	defer g.close()
	require.Equal(t, []string{"DECLARED=value"}, spec.Process.Env)
	require.Len(t, fds.files, 2)
	request := func(method string) int {
		w := httptest.NewRecorder()
		Spike6Debug(w, httptest.NewRequest(method, "/?token=test", nil))
		return w.Code
	}
	require.Equal(t, http.StatusConflict, request("GET"))
	_, err = g.readyW.Write([]byte{1})
	require.NoError(t, err)
	<-g.ready
	require.Equal(t, http.StatusOK, request("POST"))
	var b [1]byte
	_, err = io.ReadFull(g.releaseR, b[:])
	require.NoError(t, err)
	require.Equal(t, byte(1), b[0])
	// Repeated release is idempotent, and cancellation removes the control entry.
	require.Equal(t, http.StatusOK, request("POST"))
	cancel()
	<-g.closed
	require.Equal(t, http.StatusNotFound, request("GET"))
}

func TestSpike6Disabled(t *testing.T) {
	t.Setenv("_DAGGER_SPIKE6", "")
	spec := &specs.Spec{Process: &specs.Process{Args: []string{initPath, "true"}, Env: []string{"_DAGGER_SPIKE6_TOKEN=test"}}}
	var fds initFDs
	g, err := newSpike6Gate(t.Context(), spec, &fds)
	require.NoError(t, err)
	require.Nil(t, g)
	require.Empty(t, fds.files)
	require.Equal(t, []string{"_DAGGER_SPIKE6_TOKEN=test"}, spec.Process.Env)
}
