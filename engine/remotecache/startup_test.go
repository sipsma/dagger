package remotecache

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/remotecache/protocol"
	"github.com/dagger/dagger/engine/server"
	"github.com/stretchr/testify/require"
)

func importCommand(id string) protocol.Command {
	bundle := dagql.ValueBundle{Version: 2, Roots: []dagql.TransferredRoot{{Ordinal: 1}}, Values: []dagql.TransferredValue{{Ordinal: 1, Record: dagql.PersistedRecord{ResultID: 1}}}}
	return protocol.Command{ID: id, Type: protocol.CommandTypeImport, Import: &protocol.ImportCommand{BundleID: "sha256:" + id, Bundle: bundle}}
}

// waitStartup runs the gate's Wait the way the server does, with a bound
// far above the test's own limit when the test expects completion.
func waitStartup(t *testing.T, h *harness, bound time.Duration) (server.RemoteCacheStartupOutcome, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return h.adapter.startup.Wait(ctx, bound, nil)
}

// The first poll's imports complete the gate once they are all answered
// and before the bound; later polls never signal again.
func TestStartupWaitsForFirstPollImports(t *testing.T) {
	t.Parallel()
	h := startConfigured(t, func(h *harness) {
		h.adapter.importGate = make(chan struct{})
		h.svc.firstPoll <- protocol.PollResponse{Commands: []protocol.Command{importCommand("c-1"), exportCommand("c-2", 7), importCommand("c-3")}}
	})
	within(t, h.adapter.importEntered)
	// The first import is held inside the adapter: nothing has completed.
	require.Zero(t, h.adapter.startupCalls.Load())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	outcome, _ := h.adapter.startup.Wait(ctx, 50*time.Millisecond, nil)
	require.Equal(t, server.RemoteCacheStartupBoundExpired, outcome, "the bound passes while the import is held")

	close(h.adapter.importGate)
	outcome, imports := waitStartup(t, h, time.Minute)
	require.Equal(t, server.RemoteCacheStartupImportsDone, outcome)
	require.Equal(t, 2, imports, "the export does not count")
	results := h.svc.storedResults()
	require.Equal(t, protocol.CommandResult{OK: true}, results["c-1"], "answered before the signal")
	require.Equal(t, protocol.CommandResult{OK: true}, results["c-3"], "answered before the signal")
	require.EqualValues(t, 1, h.adapter.startupCalls.Load())

	// A later poll's import answers without another signal.
	h.answerPoll(t, importCommand("c-4"))
	require.True(t, h.result(t, "c-4").OK)
	require.EqualValues(t, 1, h.adapter.startupCalls.Load())
}

// A failed import is a completed one: the signal follows its answer.
func TestStartupFailedImportCompletes(t *testing.T) {
	t.Parallel()
	h := startConfigured(t, func(h *harness) {
		h.adapter.importErr = errors.New("bundle refused")
		h.svc.firstPoll <- protocol.PollResponse{Commands: []protocol.Command{importCommand("c-1")}}
	})
	outcome, imports := waitStartup(t, h, time.Minute)
	require.Equal(t, server.RemoteCacheStartupImportsDone, outcome)
	require.Equal(t, 1, imports)
	require.Equal(t, protocol.CommandResult{OK: false, Error: "bundle refused"}, h.svc.storedResults()["c-1"], "answered before the signal")
}

// A first poll with no import command completes the gate at once, whatever
// else it carried.
func TestStartupEmptyFirstPoll(t *testing.T) {
	t.Parallel()
	h := startConfigured(t, func(h *harness) {
		h.svc.firstPoll <- protocol.PollResponse{Commands: []protocol.Command{exportCommand("c-1", 7)}}
	})
	outcome, imports := waitStartup(t, h, time.Minute)
	require.Equal(t, server.RemoteCacheStartupNoImports, outcome)
	require.Zero(t, imports)
	require.Equal(t, protocol.ExportStatusExported, h.result(t, "c-1").Status)
	h.answerPoll(t, importCommand("c-2"))
	require.True(t, h.result(t, "c-2").OK)
	require.EqualValues(t, 1, h.adapter.startupCalls.Load())
}

// A first poll that finds nothing queued is answered at once, because the
// engine asked for no wait, and releases the gate at once: in virtual time
// the wait takes no time at all, well within a bound the ordinary 25 s
// long poll would have outlasted. Later polls ask for the long wait.
func TestStartupEmptyQueueReleasesAtOnce(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		svc, adapter := newFakeService(t), newFakeAdapter()
		c := newClient(Config{URL: testBaseURL, Token: testToken, EngineName: "engine-a", EngineVersion: "v1"}, testInstance, svc, adapter)
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		done := make(chan error, 1)
		go func() { done <- c.run(ctx) }()
		start := time.Now()
		outcome, imports := adapter.startup.Wait(ctx, DefaultStartupWait, nil)
		require.Equal(t, server.RemoteCacheStartupNoImports, outcome)
		require.Zero(t, imports)
		require.Equal(t, time.Duration(0), time.Since(start), "released without waiting for the bound")
		select {
		case <-svc.pollOpened:
		case <-time.After(5 * time.Minute):
			t.Fatal("no long poll opened after the first answer")
		}
		require.Equal(t, []int{0, pollWaitSeconds}, svc.recordedPollWaits())
		cancel(nil)
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Minute):
			t.Fatal("run did not return")
		}
	})
}

// With the service unreachable from the first request on, nothing
// signals: the bound expires and the engine serves, while the client keeps
// retrying its poll.
func TestStartupServiceDown(t *testing.T) {
	t.Parallel()
	h := startConfigured(t, func(h *harness) { h.svc.pollFailures.Store(1 << 20) })
	within(t, h.svc.pollAttempted)
	outcome, _ := waitStartup(t, h, 50*time.Millisecond)
	require.Equal(t, server.RemoteCacheStartupBoundExpired, outcome)
	require.Zero(t, h.adapter.startupCalls.Load())
	require.Empty(t, h.svc.pollOpened, "no poll got past the failure")
	require.Positive(t, int(1<<20-h.svc.pollFailures.Load()), "the poll was attempted and failed")
}

// A token refused on the first poll stops the client before any signal;
// the server's wait ends when Run returns, which the harness observes
// here.
func TestStartupRefusedToken(t *testing.T) {
	t.Parallel()
	h := startConfigured(t, func(h *harness) { h.svc.pollStatus.Store(http.StatusUnauthorized) })
	err := h.wait(t)
	require.ErrorIs(t, err, errUnrecoverable)
	require.Zero(t, h.adapter.startupCalls.Load())
	require.Empty(t, h.svc.pollOpened, "no poll got past the refusal")
}
