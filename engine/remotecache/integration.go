// Package remotecache is the engine's client for the remote cache service:
// the integration that cmd/engine configures from two environment variables,
// and the channel that carries commands, results, session reports and
// uploads between the engine and the service. The wire types are in the
// protocol subpackage.
package remotecache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/dagger/dagger/engine/server"
)

// The two environment variables cmd/engine reads. With EnvURL unset the
// engine has no remote cache integration and nothing changes. With EnvURL
// set, EnvToken must be set too, or startup fails.
const (
	EnvURL   = "_EXPERIMENTAL_DAGGER_REMOTE_CACHE_URL"
	EnvToken = "_EXPERIMENTAL_DAGGER_REMOTE_CACHE_TOKEN"
)

// Config is what the integration needs to reach the service.
type Config struct {
	// URL is the service's base URL. Every request path is appended to it.
	URL string
	// Token is sent as the bearer token of every request.
	Token string
	// EngineName and EngineVersion are reported in every poll.
	EngineName    string
	EngineVersion string
}

// IntegrationFromEnv builds the server option from the environment, read
// through getenv. It returns nil with no error when EnvURL is unset.
func IntegrationFromEnv(getenv func(string) string, engineName, engineVersion string) (*server.RemoteCacheIntegrationConfig, error) {
	base := getenv(EnvURL)
	if base == "" {
		return nil, nil
	}
	return NewIntegration(Config{URL: base, Token: getenv(EnvToken), EngineName: engineName, EngineVersion: engineVersion})
}

// NewIntegration validates cfg and returns the server option whose Run is
// the channel client. The engine instance ID, 16 random bytes hex encoded,
// is created here, once per engine process.
func NewIntegration(cfg Config) (*server.RemoteCacheIntegrationConfig, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("remote cache: %s is empty", EnvURL)
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("remote cache: %s is not a URL: %w", EnvURL, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("remote cache: %s must be an http or https URL with a host, got %q", EnvURL, cfg.URL)
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("remote cache: %s is set but %s is empty", EnvURL, EnvToken)
	}
	instanceID, err := newEngineInstanceID()
	if err != nil {
		return nil, err
	}
	return &server.RemoteCacheIntegrationConfig{Run: func(ctx context.Context, adapter *server.RemoteCacheAdapter) error {
		return run(ctx, cfg, instanceID, adapter)
	}}, nil
}

func newEngineInstanceID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("remote cache: engine instance ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// run is the integration's Run. Until the channel client exists it only
// logs that the integration is configured and waits for the engine to shut
// down. It holds no cache operation.
func run(ctx context.Context, cfg Config, instanceID string, _ *server.RemoteCacheAdapter) error {
	slog.Info("remote cache integration configured", "url", cfg.URL, "engineInstance", instanceID, "engineName", cfg.EngineName, "engineVersion", cfg.EngineVersion)
	<-ctx.Done()
	err := context.Cause(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
