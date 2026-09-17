package remotecache

import (
	"testing"

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
