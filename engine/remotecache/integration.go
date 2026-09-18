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
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/dagger/dagger/engine/server"
	"github.com/dagger/dagger/internal/buildkit/util/compression"
)

// The environment variables cmd/engine reads. With EnvURL unset the engine
// has no remote cache integration and nothing changes. With EnvURL set,
// EnvToken must be set too, or startup fails. EnvStartupWait, a Go
// duration, bounds how long the engine delays opening its API listeners
// for the registration backlog's imports; unset means DefaultStartupWait, "0" means
// no delay, and anything else unparseable or negative fails startup.
// EnvCompression selects the compression of the blobs an export writes for
// snapshots that have no blob yet: "uncompressed" (the default when unset)
// or "zstd"; anything else fails startup. Blobs a snapshot already has are
// reused as they are.
const (
	EnvURL         = "_EXPERIMENTAL_DAGGER_REMOTE_CACHE_URL"
	EnvToken       = "_EXPERIMENTAL_DAGGER_REMOTE_CACHE_TOKEN"
	EnvStartupWait = "_EXPERIMENTAL_DAGGER_REMOTE_CACHE_STARTUP_WAIT"
	EnvCompression = "_EXPERIMENTAL_DAGGER_REMOTE_CACHE_COMPRESSION"
)

// DefaultStartupWait is the listener delay when EnvStartupWait is unset:
// long enough for one poll and a few bundle imports on a cold engine,
// short enough that an unreachable service costs a pipeline little.
const DefaultStartupWait = 10 * time.Second

// Config is what the integration needs to reach the service.
type Config struct {
	// URL is the service's base URL. Every request path is appended to it.
	URL string
	// Token is sent as the bearer token of every request.
	Token string
	// EngineName and EngineVersion are reported in every poll.
	EngineName    string
	EngineVersion string
	// StartupWait bounds the engine's listener delay for the registration
	// backlog's imports. Zero means no delay.
	StartupWait time.Duration
	// ExportCompression is the compression of newly written export blobs:
	// compression.Uncompressed or compression.Zstd; nil means uncompressed.
	ExportCompression compression.Type
}

// IntegrationFromEnv builds the server option from the environment, read
// through getenv. It returns nil with no error when EnvURL is unset.
func IntegrationFromEnv(getenv func(string) string, engineName, engineVersion string) (*server.RemoteCacheIntegrationConfig, error) {
	base := getenv(EnvURL)
	if base == "" {
		return nil, nil
	}
	startupWait := DefaultStartupWait
	if raw := getenv(EnvStartupWait); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("remote cache: %s is not a duration: %w", EnvStartupWait, err)
		}
		if parsed < 0 {
			return nil, fmt.Errorf("remote cache: %s must not be negative, got %s", EnvStartupWait, raw)
		}
		startupWait = parsed
	}
	exportCompression, err := parseExportCompression(getenv(EnvCompression))
	if err != nil {
		return nil, err
	}
	return NewIntegration(Config{URL: base, Token: getenv(EnvToken), EngineName: engineName, EngineVersion: engineVersion, StartupWait: startupWait, ExportCompression: exportCompression})
}

// parseExportCompression reads EnvCompression's value.
func parseExportCompression(raw string) (compression.Type, error) {
	switch raw {
	case "", compression.Uncompressed.String():
		return compression.Uncompressed, nil
	case compression.Zstd.String():
		return compression.Zstd, nil
	default:
		return nil, fmt.Errorf("remote cache: %s must be %q or %q, got %q", EnvCompression, compression.Uncompressed, compression.Zstd, raw)
	}
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
	if cfg.StartupWait < 0 {
		return nil, fmt.Errorf("remote cache: startup wait must not be negative, got %s", cfg.StartupWait)
	}
	if cfg.ExportCompression == nil {
		cfg.ExportCompression = compression.Uncompressed
	}
	if cfg.ExportCompression != compression.Uncompressed && cfg.ExportCompression != compression.Zstd {
		return nil, fmt.Errorf("remote cache: export compression must be %q or %q, got %q", compression.Uncompressed, compression.Zstd, cfg.ExportCompression)
	}
	return &server.RemoteCacheIntegrationConfig{
		Run: func(ctx context.Context, adapter *server.RemoteCacheAdapter) error {
			return run(ctx, cfg, instanceID, adapter)
		},
		StartupWait:       cfg.StartupWait,
		ExportCompression: cfg.ExportCompression,
	}, nil
}

func newEngineInstanceID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("remote cache: engine instance ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// run is the integration's Run: the channel client, under the server's
// lifetime context. It returns once that context ends and every loop and
// upload has returned.
func run(ctx context.Context, cfg Config, instanceID string, adapter *server.RemoteCacheAdapter) error {
	slog.Info("remote cache integration starting", "url", cfg.URL, "engineInstance", instanceID, "engineName", cfg.EngineName, "engineVersion", cfg.EngineVersion, "startupWait", cfg.StartupWait, "exportCompression", cfg.ExportCompression)
	return newClient(cfg, instanceID, nil, adapter).run(ctx)
}
