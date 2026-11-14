package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine"
	"github.com/dagger/dagger/engine/config"
	"github.com/dagger/dagger/engine/remotecache"
	"github.com/dagger/dagger/engine/server"
)

// envRemoteCacheURL sets the remote cache service's URL. It wins over the
// engine config's remoteCache.url.
const envRemoteCacheURL = "_EXPERIMENTAL_DAGGER_REMOTE_CACHE_URL"

// remoteCacheSettings returns the connection to the remote cache service, and
// whether it is on: when the service URL is set, by the environment or the
// engine config, and DAGGER_CLOUD_TOKEN holds an engine token. A token alone
// does nothing, since clients forward it into every engine they provision.
// DAGGER_CLOUD_TOKEN=oidc fetches an OIDC token, which is not an engine
// token, so the connection stays off.
func remoteCacheSettings(cfg config.Config) (remotecache.Config, bool) {
	url := os.Getenv(envRemoteCacheURL)
	if url == "" && cfg.RemoteCache != nil {
		url = cfg.RemoteCache.URL
	}
	if url == "" {
		return remotecache.Config{}, false
	}
	token := os.Getenv("DAGGER_CLOUD_TOKEN")
	switch token {
	case "":
		slog.Warn("remote cache off: DAGGER_CLOUD_TOKEN holds no engine token")
		return remotecache.Config{}, false
	case "oidc":
		slog.Warn("remote cache off: an OIDC token is not an engine token")
		return remotecache.Config{}, false
	}
	return remotecache.Config{
		URL:           url,
		Token:         token,
		EngineVersion: engine.Version,
		EngineName:    engineName,
		Lookups:       true,
	}, true
}

// newRemoteCacheIntegration returns the server's remote cache integration, or
// nil when it is off. When it is on, the engine exports its cache events too:
// the service learns the engine's cache from them.
func newRemoteCacheIntegration(cfg *config.Config) *server.RemoteCacheIntegrationConfig {
	settings, ok := remoteCacheSettings(*cfg)
	if !ok {
		return nil
	}
	cfg.Telemetry.EngineEvents = true
	return &server.RemoteCacheIntegrationConfig{Run: func(ctx context.Context, adapter *server.RemoteCacheAdapter) error {
		// The engine's logger is set once its config is loaded, before Run.
		settings.Logger = slog.Default()
		judgeMergeGraphs(settings.Logger)
		return remotecache.Run(ctx, adapter, settings)
	}}
}

// judgeMergeGraphs has every merge's cycle check decided both by the
// narrowed search and by the whole one it replaced, and logs each merge's two
// decisions, what each visited and how long it took: a judging build for a
// stack, never on a line.
func judgeMergeGraphs(logger *slog.Logger) {
	dagql.JudgeMergeGraphs(func(_ context.Context, j dagql.MergeGraphJudgement) {
		level := slog.LevelInfo
		if j.Disagrees() {
			level = slog.LevelWarn
		}
		logger.Log(context.Background(), level, "merge graph judged",
			"caller", "engine", "from", string(j.From), "disagrees", j.Disagrees(),
			"narrow_err", errString(j.Narrow), "whole_err", errString(j.Whole),
			"narrow_visits", j.NarrowVisits, "narrow_reach", j.NarrowReach, "whole_visits", j.WholeVisits,
			"narrow_time", j.NarrowTime, "whole_time", j.WholeTime)
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
