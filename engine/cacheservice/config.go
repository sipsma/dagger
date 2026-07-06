package cacheservice

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dagger/dagger/engine/config"
)

// Settings is the resolved cache-service configuration the engine acts on:
// the engine config file layered under the DAGGER_CACHE_SERVICE_*
// environment (environment wins, so env-only provisioning works — §12.2).
type Settings struct {
	URL   string
	Token string
	Scope string

	// ImportBudget bounds the whole boot inflow (§8 D4).
	ImportBudget time.Duration
	// ImportLimit is the selection K; zero asks for the service default.
	ImportLimit int

	// ExportOnShutdown opts into the graceful-shutdown export (§7 D1),
	// bounded by ExportBudget.
	ExportOnShutdown bool
	ExportBudget     time.Duration

	// MetadataOnly skips chains and blob upload on export (§7 D3's lever).
	MetadataOnly bool

	// ExportSecret guards the export admin endpoint when set.
	ExportSecret string
}

const (
	defaultImportBudget = time.Minute
	defaultExportBudget = 2 * time.Minute
)

// ResolveSettings layers the environment over the engine config section and
// applies defaults. enabled reports whether the feature is configured at
// all; a configured-but-unusable state (URL without token or scope) returns
// an error the caller degrades on (log + run cold) — cache config must
// never fail a boot (S4).
func ResolveSettings(cfg *config.CacheServiceConfig) (settings Settings, enabled bool, err error) {
	if cfg != nil {
		settings.URL = cfg.URL
		settings.Token = cfg.Token
		settings.Scope = cfg.Scope
		settings.ImportBudget = time.Duration(cfg.ImportBudget.Duration)
		settings.ImportLimit = cfg.ImportLimit
		settings.ExportOnShutdown = cfg.ExportOnShutdown
		settings.ExportBudget = time.Duration(cfg.ExportBudget.Duration)
		settings.MetadataOnly = cfg.MetadataOnly
		settings.ExportSecret = cfg.ExportSecret
		if cfg.TokenFile != "" && settings.Token == "" {
			tokenBytes, readErr := os.ReadFile(cfg.TokenFile)
			if readErr != nil {
				return Settings{}, true, fmt.Errorf("cache service: read token file: %w", readErr)
			}
			settings.Token = strings.TrimSpace(string(tokenBytes))
		}
	}

	if v := os.Getenv("DAGGER_CACHE_SERVICE_URL"); v != "" {
		settings.URL = v
	}
	if v := os.Getenv("DAGGER_CACHE_SERVICE_TOKEN"); v != "" {
		settings.Token = v
	}
	if v := os.Getenv("DAGGER_CACHE_SERVICE_TOKEN_FILE"); v != "" {
		tokenBytes, readErr := os.ReadFile(v)
		if readErr != nil {
			return Settings{}, true, fmt.Errorf("cache service: read token file: %w", readErr)
		}
		settings.Token = strings.TrimSpace(string(tokenBytes))
	}
	if v := os.Getenv("DAGGER_CACHE_SERVICE_SCOPE"); v != "" {
		settings.Scope = v
	}
	if v := os.Getenv("DAGGER_CACHE_SERVICE_IMPORT_BUDGET"); v != "" {
		d, parseErr := time.ParseDuration(v)
		if parseErr != nil {
			return Settings{}, true, fmt.Errorf("cache service: parse DAGGER_CACHE_SERVICE_IMPORT_BUDGET: %w", parseErr)
		}
		settings.ImportBudget = d
	}
	if v := os.Getenv("DAGGER_CACHE_SERVICE_IMPORT_LIMIT"); v != "" {
		n, parseErr := strconv.Atoi(v)
		if parseErr != nil {
			return Settings{}, true, fmt.Errorf("cache service: parse DAGGER_CACHE_SERVICE_IMPORT_LIMIT: %w", parseErr)
		}
		settings.ImportLimit = n
	}
	if v := os.Getenv("DAGGER_CACHE_SERVICE_EXPORT_ON_SHUTDOWN"); v != "" {
		b, parseErr := strconv.ParseBool(v)
		if parseErr != nil {
			return Settings{}, true, fmt.Errorf("cache service: parse DAGGER_CACHE_SERVICE_EXPORT_ON_SHUTDOWN: %w", parseErr)
		}
		settings.ExportOnShutdown = b
	}
	if v := os.Getenv("DAGGER_CACHE_SERVICE_EXPORT_BUDGET"); v != "" {
		d, parseErr := time.ParseDuration(v)
		if parseErr != nil {
			return Settings{}, true, fmt.Errorf("cache service: parse DAGGER_CACHE_SERVICE_EXPORT_BUDGET: %w", parseErr)
		}
		settings.ExportBudget = d
	}
	if v := os.Getenv("DAGGER_CACHE_SERVICE_METADATA_ONLY"); v != "" {
		b, parseErr := strconv.ParseBool(v)
		if parseErr != nil {
			return Settings{}, true, fmt.Errorf("cache service: parse DAGGER_CACHE_SERVICE_METADATA_ONLY: %w", parseErr)
		}
		settings.MetadataOnly = b
	}
	if v := os.Getenv("DAGGER_CACHE_SERVICE_EXPORT_SECRET"); v != "" {
		settings.ExportSecret = v
	}

	if settings.URL == "" {
		return Settings{}, false, nil
	}
	if settings.Token == "" {
		return Settings{}, true, fmt.Errorf("cache service: URL configured without a token")
	}
	if settings.Scope == "" {
		return Settings{}, true, fmt.Errorf("cache service: URL configured without a scope")
	}
	if settings.ImportBudget <= 0 {
		settings.ImportBudget = defaultImportBudget
	}
	if settings.ExportBudget <= 0 {
		settings.ExportBudget = defaultExportBudget
	}
	return settings, true, nil
}

// NewClientFromSettings builds the protocol client for resolved settings.
func NewClientFromSettings(settings Settings) (*Client, error) {
	return NewClient(settings.URL, settings.Token, settings.Scope, ClientOptions{})
}
