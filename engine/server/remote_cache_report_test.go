package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dagger/dagger/analytics"
	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine"
	"github.com/dagger/dagger/engine/clientdb"
	"github.com/dagger/dagger/engine/snapshots/config"
	bkgw "github.com/dagger/dagger/internal/buildkit/frontend/gateway/client"
	"github.com/dagger/dagger/internal/buildkit/util/compression"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

// newReportTestServer is a teardown test server with a started integration
// whose Run waits for cancellation, so the adapter exists and is not stopped.
func newReportTestServer(t *testing.T) (*Server, *RemoteCacheAdapter) {
	t.Helper()
	srv := newTeardownTestServer(t)
	srv.clientDBs = clientdb.NewDBs(t.TempDir())
	srv.telemetryPubSub = NewPubSub(srv)
	ctx, cancel := context.WithCancelCause(context.Background())
	srv.shutdownCtx, srv.shutdownCancel = ctx, cancel
	t.Cleanup(func() { cancel(nil) })
	require.NoError(t, srv.startRemoteCacheIntegration(&RemoteCacheIntegrationConfig{Run: func(ctx context.Context, _ *RemoteCacheAdapter) error {
		<-ctx.Done()
		return context.Cause(ctx)
	}}))
	t.Cleanup(func() {
		// t.Context() is already canceled inside Cleanup, so the stop
		// deadline comes from a fresh context.
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, srv.stopRemoteCacheIntegration(stopCtx))
	})
	return srv, srv.remoteCacheAdapter
}

// newReportTestSession is a session that removeDaggerSession can tear down
// in-process, the shape TestSessionTeardownFlushesTraceTelemetryAfterMetricShutdown uses.
func newReportTestSession(t *testing.T, srv *Server, sessionID string) *daggerSession {
	t.Helper()
	md := &engine.ClientMetadata{SessionID: sessionID, ClientID: "main"}
	client := &clientRuntime{clientRecord: &clientRecord{clientID: md.ClientID, clientMetadata: md, metadataSealed: true, accepting: true, shutdownCh: make(chan struct{})}, state: clientStateInitialized, lifecycleLeases: make(map[uint64]clientLifecycleLeaseRecord)}
	sess := &daggerSession{
		sessionID:          md.SessionID,
		mainClientCallerID: md.ClientID,
		clientRuntimes:     map[string]*clientRuntime{client.clientID: client},
		services:           core.NewServices(),
		analytics:          analytics.New(analytics.Config{DoNotTrack: true}),
		containers:         map[bkgw.Container]struct{}{},
		shutdownCh:         make(chan struct{}),
		telemetryPubSub:    srv.telemetryPubSub,
	}
	client.daggerSession = sess
	installTestClientRecords(sess)
	sess.dagqlCond = sync.NewCond(&sess.dagqlMu)
	sess.closingCtx, sess.cancelClosing = context.WithCancelCause(context.Background())
	sess.state.Store(sessionStateInitialized)
	srv.initializeSessionTelemetry(sess, false)
	srv.daggerSessions[sessionID] = sess
	return sess
}

// reportTestRoot is the root of a dagql server that can decode the core
// scalars, which is all loading an imported Int by number needs.
type reportTestRoot struct{}

func (reportTestRoot) Type() *ast.Type { return &ast.Type{NamedType: "Query", NonNull: true} }

func newReportTestDagqlServer(t *testing.T) *dagql.Server {
	t.Helper()
	srv, err := dagql.NewServer(t.Context(), reportTestRoot{})
	require.NoError(t, err)
	return srv
}

// takeNow returns the queued report, or nil when the list is empty, without
// waiting: the list is checked before any wait, and the context is already
// canceled.
func takeNow(t *testing.T, adapter *RemoteCacheAdapter) *SessionReport {
	t.Helper()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := adapter.TakeSessionReport(canceled)
	if err != nil {
		require.ErrorIs(t, err, context.Canceled)
		return nil
	}
	return report
}

func TestSessionReportQueuedAtRemoval(t *testing.T) {
	t.Parallel()
	t.Run("no adapter", func(t *testing.T) {
		t.Parallel()
		srv := newTeardownTestServer(t)
		srv.clientDBs = clientdb.NewDBs(t.TempDir())
		srv.telemetryPubSub = NewPubSub(srv)
		sess := newReportTestSession(t, srv, "plain")
		addGCTestPersistable(t, srv.engineCache, sess.sessionID, "retained", dagql.NewInt(1))
		require.Nil(t, srv.remoteCacheAdapter)
		require.NoError(t, srv.removeDaggerSession(t.Context(), sess))
	})
	t.Run("retained result", func(t *testing.T) {
		t.Parallel()
		srv, adapter := newReportTestServer(t)
		sess := newReportTestSession(t, srv, "retained")
		_, res := addGCTestPersistableResult(t, srv.engineCache, sess.sessionID, "retained", dagql.NewInt(1))
		number, err := srv.engineCache.PersistedResultID(res)
		require.NoError(t, err)
		require.Nil(t, takeNow(t, adapter), "nothing queued before removal")
		require.NoError(t, srv.removeDaggerSession(t.Context(), sess))
		report := takeNow(t, adapter)
		require.NotNil(t, report, "removal queued the report")
		require.Equal(t, sess.sessionID, report.SessionID)
		require.Len(t, report.Results, 1)
		require.Equal(t, number, report.Results[0].ResultID)
		require.Equal(t, "retained", report.Results[0].Field)
		require.Equal(t, "Int", report.Results[0].Type)
		require.True(t, report.Results[0].Retained)
		require.False(t, report.Results[0].Imported)
		require.NotEmpty(t, report.Results[0].RecipeDigest)
		require.Nil(t, takeNow(t, adapter), "a report is queued once")
	})
	t.Run("no retained result", func(t *testing.T) {
		t.Parallel()
		srv, adapter := newReportTestServer(t)
		sess := newReportTestSession(t, srv, "unretained")
		ctx := addGCTestPersistable(t, srv.engineCache, "other-session", "elsewhere", dagql.NewInt(1))
		frame := &dagql.ResultCall{Kind: dagql.ResultCallKindField, Type: dagql.NewResultCallType(dagql.NewInt(0).Type()), Field: "unretained"}
		_, err := srv.engineCache.GetOrInitCall(ctx, sess.sessionID, gcTestTypeResolver{}, &dagql.CallRequest{ResultCall: frame}, func(context.Context) (dagql.AnyResult, error) {
			return dagql.NewResultForCall(dagql.NewInt(2), frame)
		})
		require.NoError(t, err)
		require.NoError(t, srv.removeDaggerSession(t.Context(), sess))
		require.Nil(t, takeNow(t, adapter), "an unretained result alone queues nothing")
	})
	t.Run("imported result only", func(t *testing.T) {
		t.Parallel()
		srv, adapter := newReportTestServer(t)
		sess := newReportTestSession(t, srv, "imported")
		other := newGCTestCache(t)
		_, foreign := addGCTestPersistableResult(t, other, "producer", "foreign", dagql.NewInt(7))
		var bundle dagql.ValueBundle
		require.NoError(t, other.WithExportedValues(t.Context(), dagql.ValueSelection{Roots: []dagql.AnyResult{foreign}}, config.RefConfig{Compression: compression.New(compression.Uncompressed)}, func(_ context.Context, values *dagql.ExportedValues) error {
			bundle = values.Bundle
			return nil
		}))
		mapping, err := adapter.ImportValues(t.Context(), bundle)
		require.NoError(t, err)
		require.Len(t, mapping, 1)
		loaded, err := srv.engineCache.LoadResultByResultID(t.Context(), sess.sessionID, newReportTestDagqlServer(t), mapping[0].ResultID)
		require.NoError(t, err)
		require.True(t, dagql.IsImportedResult(loaded))
		entries, err := srv.engineCache.SessionResults(t.Context(), sess.sessionID)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.True(t, entries[0].Imported)
		require.NoError(t, srv.removeDaggerSession(t.Context(), sess))
		require.Nil(t, takeNow(t, adapter), "an imported result alone queues nothing")
	})
}

func TestSessionReportListDropsOldest(t *testing.T) {
	t.Parallel()
	cache := newGCTestCache(t)
	bridge, created, err := cache.AttachRemoteCacheBridge()
	require.NoError(t, err)
	require.True(t, created)
	adapter := newRemoteCacheAdapter(cache, bridge)
	close(adapter.runDone)
	for i := range maxQueuedSessionReports + 1 {
		require.True(t, adapter.queueSessionReport(&SessionReport{SessionID: string(rune('a' + i))}))
	}
	adapter.reportsMu.Lock()
	require.Len(t, adapter.reports, maxQueuedSessionReports)
	adapter.reportsMu.Unlock()
	first := takeNow(t, adapter)
	require.NotNil(t, first)
	require.Equal(t, "b", first.SessionID, "the 17th report dropped the oldest")
	for i := 1; i < maxQueuedSessionReports; i++ {
		require.NotNil(t, takeNow(t, adapter))
	}
	require.Nil(t, takeNow(t, adapter))
	require.NoError(t, adapter.Stop(boundedContext(t)))
}

func TestSessionReportAfterStop(t *testing.T) {
	t.Parallel()
	srv, adapter := newReportTestServer(t)
	waiting := make(chan struct{})
	taken := make(chan error, 1)
	go func() {
		close(waiting)
		_, err := adapter.TakeSessionReport(boundedContext(t))
		taken <- err
	}()
	within(t, waiting)
	require.True(t, adapter.queueSessionReport(&SessionReport{SessionID: "before"}))
	// The waiter took that one. Queue another, then stop: the list is
	// dropped, the next waiter returns closed, and every new method refuses.
	within(t, taken)
	require.True(t, adapter.queueSessionReport(&SessionReport{SessionID: "dropped"}))
	go func() {
		_, err := adapter.TakeSessionReport(boundedContext(t))
		taken <- err
	}()
	require.NoError(t, srv.stopRemoteCacheIntegration(boundedContext(t)))
	select {
	case err := <-taken:
		if err != nil {
			require.ErrorIs(t, err, ErrRemoteCacheAdapterClosed)
		}
	case <-boundedContext(t).Done():
		t.Fatal("waiting TakeSessionReport did not return after stop")
	}
	_, err := adapter.TakeSessionReport(t.Context())
	require.ErrorIs(t, err, ErrRemoteCacheAdapterClosed)
	require.False(t, adapter.queueSessionReport(&SessionReport{SessionID: "late"}))
	_, err = adapter.ImportValues(t.Context(), dagql.ValueBundle{})
	require.ErrorIs(t, err, ErrRemoteCacheAdapterClosed)
	err = adapter.ExportValues(t.Context(), 1, nil, func(context.Context, *dagql.ExportedValues) error { t.Fatal("consumer ran"); return nil })
	require.ErrorIs(t, err, ErrRemoteCacheAdapterClosed)

	// A session removed after Stop queues nothing and does not panic.
	sess := newReportTestSession(t, srv, "after-stop")
	addGCTestPersistable(t, srv.engineCache, sess.sessionID, "retained", dagql.NewInt(1))
	require.NoError(t, srv.removeDaggerSession(t.Context(), sess))
	_, err = adapter.TakeSessionReport(t.Context())
	require.ErrorIs(t, err, ErrRemoteCacheAdapterClosed)
}

func TestAdapterExportValues(t *testing.T) {
	t.Parallel()
	srv, adapter := newReportTestServer(t)
	cache := srv.engineCache
	_, child := addGCTestPersistableResult(t, cache, "s", "child", dagql.NewInt(1))
	_, root := addGCTestPersistableResult(t, cache, "s", "root", dagql.DynamicResultArrayOutput{Elem: dagql.NewInt(0), Values: []dagql.AnyResult{child}})
	childID, err := cache.PersistedResultID(child)
	require.NoError(t, err)
	rootID, err := cache.PersistedResultID(root)
	require.NoError(t, err)

	var bundle dagql.ValueBundle
	consumed := 0
	require.NoError(t, adapter.ExportValues(t.Context(), rootID, []uint64{childID, 424242}, func(_ context.Context, values *dagql.ExportedValues) error {
		consumed++
		bundle = values.Bundle
		require.Empty(t, values.Chains.Entries, "scalar rows own no parts")
		return nil
	}))
	require.Equal(t, 1, consumed)
	require.Len(t, bundle.Roots, 1)
	require.Len(t, bundle.Values, 2, "the root and its dependency")
	require.Empty(t, bundle.Outputs)

	err = adapter.ExportValues(t.Context(), 424242, []uint64{childID}, func(context.Context, *dagql.ExportedValues) error {
		t.Fatal("consumer ran for a missing root")
		return nil
	})
	require.ErrorIs(t, err, ErrRemoteCacheResultNotFound)
	require.ErrorContains(t, err, "424242")

	boom := errors.New("upload failed")
	err = adapter.ExportValues(t.Context(), rootID, nil, func(context.Context, *dagql.ExportedValues) error { return boom })
	require.ErrorIs(t, err, boom)

	// The bundle imports into another engine's cache through its adapter.
	other := newGCTestCache(t)
	obridge, created, err := other.AttachRemoteCacheBridge()
	require.NoError(t, err)
	require.True(t, created)
	oadapter := newRemoteCacheAdapter(other, obridge)
	close(oadapter.runDone)
	mapping, err := oadapter.ImportValues(t.Context(), bundle)
	require.NoError(t, err)
	require.Len(t, mapping, 1)
	loaded, err := other.LoadResultByResultID(dagql.ContextWithCache(t.Context(), other), "", newReportTestDagqlServer(t), mapping[0].ResultID)
	require.NoError(t, err)
	require.True(t, dagql.IsImportedResult(loaded))
	require.NoError(t, oadapter.Stop(boundedContext(t)))
}
