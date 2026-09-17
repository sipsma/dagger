package remotecache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mapGetenv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestIntegrationFromEnv(t *testing.T) {
	t.Parallel()
	t.Run("url unset leaves the option nil", func(t *testing.T) {
		t.Parallel()
		cfg, err := IntegrationFromEnv(mapGetenv(nil), "engine-a", "v1")
		require.NoError(t, err)
		require.Nil(t, cfg)
		cfg, err = IntegrationFromEnv(mapGetenv(map[string]string{EnvToken: "secret"}), "engine-a", "v1")
		require.NoError(t, err)
		require.Nil(t, cfg, "a token alone configures nothing")
	})
	t.Run("url set with an empty token fails", func(t *testing.T) {
		t.Parallel()
		_, err := IntegrationFromEnv(mapGetenv(map[string]string{EnvURL: "http://cache:8080"}), "engine-a", "v1")
		require.ErrorContains(t, err, EnvToken+" is empty")
		require.ErrorContains(t, err, EnvURL+" is set")
	})
	t.Run("bad url fails", func(t *testing.T) {
		t.Parallel()
		for _, bad := range []string{"cache:8080", "ftp://cache", "http://", "://x"} {
			_, err := IntegrationFromEnv(mapGetenv(map[string]string{EnvURL: bad, EnvToken: "secret"}), "engine-a", "v1")
			require.Error(t, err, bad)
			require.ErrorContains(t, err, EnvURL)
		}
	})
	t.Run("both set builds the option", func(t *testing.T) {
		t.Parallel()
		cfg, err := IntegrationFromEnv(mapGetenv(map[string]string{EnvURL: "http://cache:8080", EnvToken: "secret"}), "engine-a", "v1")
		require.NoError(t, err)
		require.NotNil(t, cfg)
		require.NotNil(t, cfg.Run)
	})
}

func TestNewIntegrationInstanceIDs(t *testing.T) {
	t.Parallel()
	a, err := newEngineInstanceID()
	require.NoError(t, err)
	b, err := newEngineInstanceID()
	require.NoError(t, err)
	require.Len(t, a, 32)
	require.NotEqual(t, a, b)
}

// Until the channel client exists, Run only waits for the engine to shut
// down. It returns nil for a plain cancellation and the cause otherwise.
func TestRunReturnsOnShutdown(t *testing.T) {
	t.Parallel()
	cfg, err := NewIntegration(Config{URL: "http://cache:8080", Token: "secret", EngineName: "engine-a", EngineVersion: "v1"})
	require.NoError(t, err)
	ctx, cancel := context.WithCancelCause(t.Context())
	done := make(chan error, 1)
	go func() { done <- cfg.Run(ctx, nil) }()
	cancel(nil)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	cause := errors.New("engine shutting down")
	ctx, cancel = context.WithCancelCause(t.Context())
	go func() { done <- cfg.Run(ctx, nil) }()
	cancel(cause)
	select {
	case err := <-done:
		require.ErrorIs(t, err, cause)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation with a cause")
	}
}
