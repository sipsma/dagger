package core

import (
	"context"
	"strings"
	"testing"

	"dagger.io/dagger"
	"github.com/dagger/dagger/internal/buildkit/identity"
	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"
)

type RemoteCacheStartupSuite struct{}

func TestRemoteCacheStartupSuite(t *testing.T) {
	testctx.New(t, Middleware()...).RunTests(RemoteCacheStartupSuite{})
}

// With no remote cache URL the engine opens its listeners as before: a
// client connects and is served, and the engine's own log shows the
// listener start with no startup wait.
func (RemoteCacheStartupSuite) TestListenerOpensWithoutURL(ctx context.Context, t *testctx.T) {
	outer := connect(ctx, t)
	logs := outer.CacheVolume("remote-cache-startup-log-" + identity.NewID())
	engine := devEngineContainerAsService(devEngineContainer(outer, func(ctr *dagger.Container) *dagger.Container {
		return ctr.WithoutEnvVariable("_EXPERIMENTAL_DAGGER_REMOTE_CACHE_URL").
			WithMountedCache("/engine-log", logs).
			WithEntrypoint([]string{"sh", "-c", `exec /usr/local/bin/dagger-entrypoint.sh "$@" 2>>/engine-log/engine.log`, "dagger-engine"})
	}))
	tunnel, err := outer.Host().Tunnel(engine).Start(ctx)
	require.NoError(t, err)
	defer func() {
		_, _ = tunnel.Stop(ctx, dagger.ServiceStopOpts{Kill: true})
		_, _ = engine.Stop(ctx)
	}()
	endpoint, err := tunnel.Endpoint(ctx, dagger.ServiceEndpointOpts{Scheme: "tcp"})
	require.NoError(t, err)
	client, err := dagger.Connect(ctx, dagger.WithRunnerHost(endpoint))
	require.NoError(t, err)
	version, err := client.Version(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, version)
	require.NoError(t, client.Close())

	log, err := outer.Container().From(alpineImage).WithMountedCache("/engine-log", logs).
		WithEnvVariable("READ", identity.NewID()).
		WithExec([]string{"cat", "/engine-log/engine.log"}).Stdout(ctx)
	require.NoError(t, err)
	require.Contains(t, log, "running server on", "the listener opened")
	require.NotContains(t, log, "remote cache startup wait", "no wait happened without the URL")
	require.NotContains(t, log, "remote cache integration starting")
	require.Positive(t, strings.Count(log, "running server on"))
}
