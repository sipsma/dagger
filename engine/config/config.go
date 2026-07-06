package config

import (
	"encoding/json"
	"fmt"
	"strings"

	bkconfig "github.com/dagger/dagger/internal/buildkit/cmd/buildkitd/config"
	"github.com/dagger/dagger/internal/buildkit/util/disk"
	"github.com/invopop/jsonschema"
	"github.com/sirupsen/logrus"

	"github.com/dagger/dagger/engine/slog"
)

// NOTE: when modifying the config struct:
// - try and keep the top-level clean, and group related configs together
// - avoid unnecessary breaking changes, but prefer a neat config structure
//   and good docs over supporting legacy configs
// - prefer creating new keys instead of modifying the behavior of existing keys
// - add explicit error messages if a key is no longer supported

type Config struct {
	// LogLevel defines the engine's logging level.
	LogLevel LogLevel `json:"logLevel,omitempty" jsonschema:"enum=error,enum=warn,enum=info,enum=debug,enum=debugextra,enum=trace"`

	// GC configures the engine's garbage collector.
	GC GCConfig `json:"gc,omitempty"`

	// Security allows configuring various security settings for the engine.
	Security *Security `json:"security,omitempty"`

	// Registries configures custom registry mirrors, root CAs, and
	// insecure/HTTP access.
	Registries map[string]RegistryConfig `json:"registries,omitempty"`

	// CacheService configures the remote cache service integration: bundle
	// import at boot, content-chain blob fetch at serving time, and cache
	// export. Absent (and with no DAGGER_CACHE_SERVICE_* environment
	// overrides) the whole feature is disabled.
	CacheService *CacheServiceConfig `json:"cacheService,omitempty"`
}

// CacheServiceConfig wires the engine to a remote cache service. Every
// field can also be injected through the environment
// (DAGGER_CACHE_SERVICE_URL, _TOKEN, _TOKEN_FILE, _SCOPE, _IMPORT_BUDGET,
// _IMPORT_LIMIT, _EXPORT_ON_SHUTDOWN, _EXPORT_BUDGET, _METADATA_ONLY,
// _EXPORT_SECRET); environment values win over the file, so provisioning
// systems that can only inject env vars can fully configure the feature.
type CacheServiceConfig struct {
	// URL is the cache service base URL. Empty disables the feature.
	URL string `json:"url,omitempty"`

	// Token is the org-scoped service token, inline. Prefer TokenFile or the
	// environment for production credentials.
	Token string `json:"token,omitempty"`

	// TokenFile is a path the token is read from at boot.
	TokenFile string `json:"tokenFile,omitempty"`

	// Scope is the cache namespace within the org (by convention the
	// repository identity for CI fleets).
	Scope string `json:"scope,omitempty"`

	// ImportBudget bounds the whole boot-time bundle inflow (selection,
	// downloads, merges). Exhaustion degrades to fewer bundles, then none —
	// never a boot failure. Default 1m.
	ImportBudget Duration `json:"importBudget,omitempty"`

	// ImportLimit is the maximum number of bundles requested at boot.
	// Zero means the service default.
	ImportLimit int `json:"importLimit,omitempty"`

	// ExportOnShutdown opts into a budget-bounded cache export during
	// graceful shutdown (after sessions drain and prune runs).
	ExportOnShutdown bool `json:"exportOnShutdown,omitempty"`

	// ExportBudget bounds a shutdown export so shutdown cannot hang; an
	// exhausted budget costs blobs, never correctness. Default 2m. Exports
	// triggered through the admin API are bounded by their caller instead.
	ExportBudget Duration `json:"exportBudget,omitempty"`

	// MetadataOnly makes exports skip content chains and blob upload:
	// importers get lookup warmth and recompute content via lazy forms.
	MetadataOnly bool `json:"metadataOnly,omitempty"`

	// ExportSecret optionally guards the engine's export admin endpoint: when
	// set, POST /v1/cache/export requires it in the X-Dagger-Export-Secret
	// header (defense in depth on the operator listener).
	ExportSecret string `json:"exportSecret,omitempty"`
}

type LogLevel string

const (
	LevelError      LogLevel = "error"
	LevelWarn       LogLevel = "warn"
	LevelInfo       LogLevel = "info"
	LevelDebug      LogLevel = "debug"
	LevelExtraDebug LogLevel = "debugextra"
	LevelTrace      LogLevel = "trace"
)

func (ll LogLevel) ToSlogLevel() (slog.Level, error) {
	ll = LogLevel(strings.ToLower(string(ll)))
	switch ll {
	case LevelError:
		return slog.LevelError, nil
	case LevelWarn:
		return slog.LevelWarn, nil
	case LevelInfo:
		return slog.LevelInfo, nil
	case LevelDebug:
		return slog.LevelDebug, nil
	case LevelExtraDebug:
		return slog.LevelExtraDebug, nil
	case LevelTrace:
		return slog.LevelTrace, nil
	}
	return slog.Level(0), fmt.Errorf("unknown log level %q", ll)
}

func (ll LogLevel) ToLogrusLevel() (logrus.Level, error) {
	ll = LogLevel(strings.ToLower(string(ll)))
	switch ll {
	case LevelError:
		return logrus.ErrorLevel, nil
	case LevelWarn:
		return logrus.WarnLevel, nil
	case LevelInfo:
		return logrus.InfoLevel, nil
	case LevelDebug, LevelExtraDebug:
		return logrus.DebugLevel, nil
	case LevelTrace:
		return logrus.TraceLevel, nil
	}
	return logrus.Level(0), fmt.Errorf("unknown log level %q", ll)
}

type RegistryConfig struct {
	Mirrors   []string `json:"mirrors"`
	PlainHTTP *bool    `json:"http"`
	Insecure  *bool    `json:"insecure"`
	RootCAs   []string `json:"ca"`
}

type GCConfig struct {
	// Enabled controls whether the garbage collector is enabled - it is
	// switched on by default (and generally shouldn't be turned off, except
	// for very short-lived dagger instances).
	Enabled *bool `json:"enabled,omitempty"`

	// GCSpace is the amount of space to allow for the entire dagger engine,
	// only used in computing the default Policies.
	GCSpace

	// Policies are a list of manually configured policies - if not specified,
	// an automatic default will be generated from the top-level disk space
	// parameters.
	Policies []GCPolicy `json:"policies,omitempty"`
}

type GCPolicy struct {
	// All matches every cache record.
	All bool `json:"all,omitempty"`

	// Filters are a list of containerd filters to match specific cache
	// records. The available filters are: "id", "parents", "description",
	// "inuse", "mutable", "immutable", "type", "shared", and "private".
	Filters []string `json:"filters,omitempty"`

	// KeepDuration specifies the minimum amount of time to keep records in
	// this policy.
	KeepDuration Duration `json:"keepDuration,omitempty"`

	// GCSpace is the amount of space to allow for this policy.
	GCSpace
}

type GCSpace struct {
	// ReservedSpace is the minimum amount of disk space this policy is guaranteed to retain.
	// Any usage below this threshold will not be reclaimed during garbage collection.
	ReservedSpace DiskSpace `json:"reservedSpace,omitempty"`

	// MaxUsedSpace is the maximum amount of disk space this policy is allowed to use.
	// Any usage exceeding this limit will be cleaned up during a garbage collection sweep.
	MaxUsedSpace DiskSpace `json:"maxUsedSpace,omitempty"`

	// MinFreeSpace is the target amount of free disk space the garbage collector will attempt to leave.
	// However, it will never let the available space fall below ReservedSpace.
	MinFreeSpace DiskSpace `json:"minFreeSpace,omitempty"`

	// SweepSize is the minimum amount of space to sweep during a single gc pass.
	// Either an absolute number of bytes, or a percentage of the "allowed
	// space" between reserved and max.
	SweepSize DiskSpace `json:"sweepSize,omitempty"`
}

func (space *GCSpace) IsUnset() bool {
	return space.ReservedSpace == DiskSpace{} &&
		space.MaxUsedSpace == DiskSpace{} &&
		space.MinFreeSpace == DiskSpace{} &&
		space.SweepSize == DiskSpace{}
}

type DiskSpace bkconfig.DiskSpace

func (space DiskSpace) MarshalJSON() ([]byte, error) {
	if space.Bytes != 0 {
		return fmt.Appendf(nil, `%d`, space.Bytes), nil
	}
	if space.Percentage != 0 {
		return fmt.Appendf(nil, `"%d%%"`, space.Percentage), nil
	}
	return []byte("0"), nil
}

func (space *DiskSpace) UnmarshalJSON(data []byte) error {
	return (*bkconfig.DiskSpace)(space).UnmarshalText(data)
}

func (space DiskSpace) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: `DiskSpace is either an integer number of bytes (e.g. 512000000), a string with a byte unit suffix (e.g. "512MB"), or a string percentage of the total disk space (e.g. "10%").`,
		AnyOf: []*jsonschema.Schema{
			{
				// human-readable bytes representation
				Type:    "string",
				Pattern: `^[0-9][0-9.]*([kKmMgGtTpP][iI]?)?[bB]?$`,
			},
			{
				// percentage
				Type:    "string",
				Pattern: `^[0-9]+%$`,
			},
			{
				// standalone number
				Type: "number",
			},
		},
	}
}

func (space DiskSpace) AsBytes(dstat disk.DiskStat) int64 {
	return (bkconfig.DiskSpace)(space).AsBytes(dstat)
}

type Duration bkconfig.Duration

func (duration Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(duration.Duration)
}

func (duration *Duration) UnmarshalJSON(data []byte) error {
	return (*bkconfig.Duration)(duration).UnmarshalText(data)
}

func (duration Duration) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: `Duration is either an integer number of seconds (e.g. 3600), or a string representation of the time (e.g. "1h30m").`,
		AnyOf: []*jsonschema.Schema{
			{
				Type:    "string",
				Pattern: `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`,
			},
			{
				// standalone number of seconds (as a string)
				Type:    "string",
				Pattern: `^[0-9]+$`,
			},
			{
				// standalone number of seconds
				Type: "number",
			},
		},
	}
}

type Security struct {
	// InsecureRootCapabilities controls whether the argument of the same name
	// is permitted in Container.withExec - it is allowed by default.
	// Disabling this option ensures that dagger build containers do not run as
	// privileged, and is a basic form of security hardening.
	InsecureRootCapabilities *bool `json:"insecureRootCapabilities,omitempty"`
}
