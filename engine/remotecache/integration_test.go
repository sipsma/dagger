package remotecache

import (
	"testing"
	"time"

	"github.com/dagger/dagger/internal/buildkit/util/compression"
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
		require.Equal(t, DefaultStartupWait, cfg.StartupWait)
	})
	t.Run("export compression", func(t *testing.T) {
		t.Parallel()
		env := func(value string) func(string) string {
			return mapGetenv(map[string]string{EnvURL: "http://cache:8080", EnvToken: "secret", EnvCompression: value})
		}
		cfg, err := IntegrationFromEnv(env(""), "engine-a", "v1")
		require.NoError(t, err)
		require.Equal(t, compression.Uncompressed, cfg.ExportCompression, "unset means uncompressed")
		cfg, err = IntegrationFromEnv(env("uncompressed"), "engine-a", "v1")
		require.NoError(t, err)
		require.Equal(t, compression.Uncompressed, cfg.ExportCompression)
		cfg, err = IntegrationFromEnv(env("zstd"), "engine-a", "v1")
		require.NoError(t, err)
		require.Equal(t, compression.Zstd, cfg.ExportCompression)
		for _, bad := range []string{"gzip", "ZSTD", "estargz", "yes"} {
			_, err = IntegrationFromEnv(env(bad), "engine-a", "v1")
			require.ErrorContains(t, err, EnvCompression, bad)
		}
		_, err = NewIntegration(Config{URL: "http://cache:8080", Token: "secret", ExportCompression: compression.Gzip})
		require.ErrorContains(t, err, "export compression must be")
		cfg, err = NewIntegration(Config{URL: "http://cache:8080", Token: "secret"})
		require.NoError(t, err)
		require.Equal(t, compression.Uncompressed, cfg.ExportCompression, "a nil type is uncompressed")
	})
	t.Run("startup wait", func(t *testing.T) {
		t.Parallel()
		env := func(wait string) func(string) string {
			return mapGetenv(map[string]string{EnvURL: "http://cache:8080", EnvToken: "secret", EnvStartupWait: wait})
		}
		cfg, err := IntegrationFromEnv(env("2s"), "engine-a", "v1")
		require.NoError(t, err)
		require.Equal(t, 2*time.Second, cfg.StartupWait)
		cfg, err = IntegrationFromEnv(env("0"), "engine-a", "v1")
		require.NoError(t, err)
		require.Zero(t, cfg.StartupWait, "zero disables the delay")
		_, err = IntegrationFromEnv(env("soon"), "engine-a", "v1")
		require.ErrorContains(t, err, EnvStartupWait+" is not a duration")
		_, err = IntegrationFromEnv(env("-1s"), "engine-a", "v1")
		require.ErrorContains(t, err, EnvStartupWait+" must not be negative")
		_, err = NewIntegration(Config{URL: "http://cache:8080", Token: "secret", StartupWait: -time.Second})
		require.ErrorContains(t, err, "must not be negative")
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
