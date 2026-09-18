package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRemoteCacheStartupGateOutcomes(t *testing.T) {
	t.Parallel()
	ctx := boundedContext(t)

	gate := NewRemoteCacheStartupGate()
	gate.Complete(3)
	gate.Complete(5)
	outcome, imports := gate.Wait(ctx, time.Minute, nil)
	require.Equal(t, RemoteCacheStartupImportsDone, outcome)
	require.Equal(t, 3, imports, "the first completion wins")

	gate = NewRemoteCacheStartupGate()
	gate.Complete(0)
	outcome, _ = gate.Wait(ctx, time.Minute, nil)
	require.Equal(t, RemoteCacheStartupNoImports, outcome)

	gate = NewRemoteCacheStartupGate()
	outcome, _ = gate.Wait(ctx, 20*time.Millisecond, nil)
	require.Equal(t, RemoteCacheStartupBoundExpired, outcome)

	gate = NewRemoteCacheStartupGate()
	outcome, _ = gate.Wait(ctx, 0, nil)
	require.Equal(t, RemoteCacheStartupDisabled, outcome)

	exited := make(chan struct{})
	close(exited)
	gate = NewRemoteCacheStartupGate()
	outcome, _ = gate.Wait(ctx, time.Minute, exited)
	require.Equal(t, RemoteCacheStartupIntegrationExited, outcome)
	gate.Complete(2)
	outcome, imports = gate.Wait(ctx, time.Minute, exited)
	require.Equal(t, RemoteCacheStartupImportsDone, outcome, "a completion counts even after the exit")
	require.Equal(t, 2, imports)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	gate = NewRemoteCacheStartupGate()
	outcome, _ = gate.Wait(canceled, time.Minute, nil)
	require.Equal(t, RemoteCacheStartupShutdown, outcome)
}

// The server's wait: nothing without an integration; the client's signal
// through the adapter; Run's return; the bound from the config.
func TestServerWaitRemoteCacheStartup(t *testing.T) {
	t.Parallel()
	ctx := boundedContext(t)
	require.ErrorContains(t, validateRemoteCacheIntegration(&RemoteCacheIntegrationConfig{Run: func(context.Context, *RemoteCacheAdapter) error { return nil }, StartupWait: -time.Second}), "must not be negative")

	t.Run("no integration", func(t *testing.T) {
		t.Parallel()
		srv := &Server{engineCache: newGCTestCache(t), shutdownCtx: t.Context()}
		require.NoError(t, srv.startRemoteCacheIntegration(nil))
		require.Equal(t, RemoteCacheStartupOutcome(""), srv.WaitRemoteCacheStartup(ctx))
	})
	t.Run("imports done", func(t *testing.T) {
		t.Parallel()
		srv := &Server{engineCache: newGCTestCache(t), shutdownCtx: t.Context()}
		require.NoError(t, srv.startRemoteCacheIntegration(&RemoteCacheIntegrationConfig{StartupWait: time.Minute, Run: func(ctx context.Context, adapter *RemoteCacheAdapter) error {
			adapter.StartupComplete(2)
			<-ctx.Done()
			return nil
		}}))
		defer func() { require.NoError(t, srv.stopRemoteCacheIntegration(ctx)) }()
		require.Equal(t, RemoteCacheStartupImportsDone, srv.WaitRemoteCacheStartup(ctx))
	})
	t.Run("integration exited", func(t *testing.T) {
		t.Parallel()
		srv := &Server{engineCache: newGCTestCache(t), shutdownCtx: t.Context()}
		require.NoError(t, srv.startRemoteCacheIntegration(&RemoteCacheIntegrationConfig{StartupWait: time.Minute, Run: func(context.Context, *RemoteCacheAdapter) error {
			return errors.New("token refused")
		}}))
		defer func() { require.NoError(t, srv.stopRemoteCacheIntegration(ctx)) }()
		require.Equal(t, RemoteCacheStartupIntegrationExited, srv.WaitRemoteCacheStartup(ctx))
	})
	t.Run("bound expires", func(t *testing.T) {
		t.Parallel()
		srv := &Server{engineCache: newGCTestCache(t), shutdownCtx: t.Context()}
		require.NoError(t, srv.startRemoteCacheIntegration(&RemoteCacheIntegrationConfig{StartupWait: 20 * time.Millisecond, Run: func(ctx context.Context, _ *RemoteCacheAdapter) error {
			<-ctx.Done()
			return nil
		}}))
		defer func() { require.NoError(t, srv.stopRemoteCacheIntegration(ctx)) }()
		require.Equal(t, RemoteCacheStartupBoundExpired, srv.WaitRemoteCacheStartup(ctx))
	})
	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		srv := &Server{engineCache: newGCTestCache(t), shutdownCtx: t.Context()}
		require.NoError(t, srv.startRemoteCacheIntegration(&RemoteCacheIntegrationConfig{Run: func(ctx context.Context, _ *RemoteCacheAdapter) error {
			<-ctx.Done()
			return nil
		}}))
		defer func() { require.NoError(t, srv.stopRemoteCacheIntegration(ctx)) }()
		require.Equal(t, RemoteCacheStartupDisabled, srv.WaitRemoteCacheStartup(ctx))
	})
}
