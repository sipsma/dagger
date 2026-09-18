package server

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// RemoteCacheStartupOutcome says why the startup wait ended.
type RemoteCacheStartupOutcome string

const (
	// RemoteCacheStartupImportsDone: the import commands of the startup
	// phase's polls, taken until one carried no command, have all been
	// answered.
	RemoteCacheStartupImportsDone RemoteCacheStartupOutcome = "imports-done"
	// RemoteCacheStartupNoImports: the startup phase's polls carried no
	// import command.
	RemoteCacheStartupNoImports RemoteCacheStartupOutcome = "no-imports"
	// RemoteCacheStartupBoundExpired: the bound passed first; the service
	// was unreachable or its imports were still running.
	RemoteCacheStartupBoundExpired RemoteCacheStartupOutcome = "bound-expired"
	// RemoteCacheStartupIntegrationExited: Run returned before signaling,
	// for example because the service refused the engine's token.
	RemoteCacheStartupIntegrationExited RemoteCacheStartupOutcome = "integration-exited"
	// RemoteCacheStartupShutdown: the engine is shutting down.
	RemoteCacheStartupShutdown RemoteCacheStartupOutcome = "shutdown"
	// RemoteCacheStartupDisabled: the bound is zero, so nothing waited.
	RemoteCacheStartupDisabled RemoteCacheStartupOutcome = "disabled"
)

// RemoteCacheStartupGate is the one signal from the integration's channel
// client to the engine's startup: the registration backlog's import
// commands are done. Complete is called once by the client; Wait is called
// once by the server before it opens the API listeners.
type RemoteCacheStartupGate struct {
	once    sync.Once
	done    chan struct{}
	imports int
}

func NewRemoteCacheStartupGate() *RemoteCacheStartupGate {
	return &RemoteCacheStartupGate{done: make(chan struct{})}
}

// Complete records that the startup phase's import commands have been
// answered (success or failure), or that it carried none. Later calls do
// nothing.
func (g *RemoteCacheStartupGate) Complete(imports int) {
	g.once.Do(func() {
		g.imports = imports
		close(g.done)
	})
}

// Wait returns when Complete has been called, when exited closes (the
// integration's Run returned), when bound has elapsed, or when ctx ends,
// whichever comes first. A zero bound returns at once. The imports count is
// meaningful for RemoteCacheStartupImportsDone only.
func (g *RemoteCacheStartupGate) Wait(ctx context.Context, bound time.Duration, exited <-chan struct{}) (outcome RemoteCacheStartupOutcome, imports int) {
	if bound <= 0 {
		return RemoteCacheStartupDisabled, 0
	}
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-g.done:
	case <-exited:
		// A completion that raced the exit still counts.
		select {
		case <-g.done:
		default:
			return RemoteCacheStartupIntegrationExited, 0
		}
	case <-timer.C:
		return RemoteCacheStartupBoundExpired, 0
	case <-ctx.Done():
		return RemoteCacheStartupShutdown, 0
	}
	if g.imports == 0 {
		return RemoteCacheStartupNoImports, 0
	}
	return RemoteCacheStartupImportsDone, g.imports
}

// StartupComplete is the channel client's signal that the startup phase's
// import commands are done, or that there were none.
func (a *RemoteCacheAdapter) StartupComplete(imports int) {
	a.startup.Complete(imports)
}

// WaitRemoteCacheStartup delays the caller, meant to be the API listener
// start, until the remote cache integration has applied the import
// commands of its registration backlog, or the configured bound has elapsed,
// whichever comes first, and logs the outcome. With no integration it
// returns at once and logs nothing. A service that cannot be reached, or
// one still importing when the bound expires, never holds the engine
// beyond the bound.
func (srv *Server) WaitRemoteCacheStartup(ctx context.Context) RemoteCacheStartupOutcome {
	adapter := srv.remoteCacheAdapter
	if adapter == nil {
		return ""
	}
	start := time.Now()
	outcome, imports := adapter.startup.Wait(ctx, adapter.startupWait, adapter.runDone)
	slog.Info("remote cache startup wait ended", "outcome", string(outcome), "imports", imports, "elapsed", time.Since(start).Round(time.Millisecond), "bound", adapter.startupWait)
	return outcome
}
