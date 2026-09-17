package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/dagger/dagger/internal/buildkit/util/compression"
)

// RemoteCacheIntegrationConfig injects an in-process remote cache integration.
// Ordinary engines leave it nil. This is not a schema field, listener or
// service; Run owns the integration's channel consumer and transport.
type RemoteCacheIntegrationConfig struct {
	// Run is called once, after local cache initialization, under the server
	// lifetime context. It must return once that context is canceled, including
	// for delivered requests' Done signals, and must not hold a cache operation
	// while waiting for future requests.
	Run func(context.Context, *RemoteCacheAdapter) error
	// StartupWait bounds how long the engine delays opening its API
	// listeners for the first poll's imports (WaitRemoteCacheStartup).
	// Zero means no delay.
	StartupWait time.Duration
}

var ErrRemoteCacheAdapterClosed = errors.New("remote cache adapter closed")

// ErrRemoteCacheResultNotFound is returned by ExportValues when the root's
// result number is not in the cache.
var ErrRemoteCacheResultNotFound = errors.New("remote cache result not found")

// maxQueuedSessionReports bounds the adapter's report list. When it is full,
// the oldest report is dropped.
const maxQueuedSessionReports = 16

// SessionReport describes the results one ended session touched. The engine
// server queues one at session removal, before the cache releases the
// session, when at least one result is retained and not imported.
type SessionReport struct {
	SessionID string
	Results   []dagql.SessionResultEntry
}

var errRemoteCacheIntegrationExited = errors.New("remote cache integration exited")

// RemoteCacheAdapter forwards control calls to its cache and renewal calls to
// the one bridge attached for it, never to a later attachment.
type RemoteCacheAdapter struct {
	cache   *dagql.Cache
	bridge  *dagql.RemoteCacheBridge
	stopped atomic.Bool
	cancel  context.CancelCauseFunc
	// runDone is owned by the server and closes when Run returns.
	runDone  chan struct{}
	stopOnce sync.Once
	stopErr  error

	// reportsMu guards reports and the stopped check made under it, so a
	// report queued after close is dropped and never left for nobody.
	reportsMu sync.Mutex
	reports   []*SessionReport
	// reportReady carries one wake-up per queued report, at most one
	// pending. It is never closed. stopCh is closed once by close.
	reportReady chan struct{}
	stopCh      chan struct{}
	stopChOnce  sync.Once
	// startup is signaled by the client after the first poll's imports;
	// startupWait is the server's bound on waiting for it.
	startup     *RemoteCacheStartupGate
	startupWait time.Duration
	// testBeforeReportWait runs in TakeSessionReport right before it waits
	// with an empty list.
	testBeforeReportWait func()
}

func newRemoteCacheAdapter(cache *dagql.Cache, bridge *dagql.RemoteCacheBridge) *RemoteCacheAdapter {
	return &RemoteCacheAdapter{cache: cache, bridge: bridge, cancel: func(error) {}, runDone: make(chan struct{}), reportReady: make(chan struct{}, 1), stopCh: make(chan struct{}), startup: NewRemoteCacheStartupGate()}
}

// queueSessionReport adds a report to the list, dropping the oldest when the
// list is full. A stopped adapter drops the report and reports false.
func (a *RemoteCacheAdapter) queueSessionReport(report *SessionReport) bool {
	a.reportsMu.Lock()
	defer a.reportsMu.Unlock()
	if a.stopped.Load() {
		return false
	}
	if len(a.reports) >= maxQueuedSessionReports {
		a.reports = slices.Delete(a.reports, 0, 1)
	}
	a.reports = append(a.reports, report)
	select {
	case a.reportReady <- struct{}{}:
	default:
	}
	return true
}

// TakeSessionReport blocks until a report is queued, ctx ends, or the
// adapter stops. It holds no cache operation while it waits.
func (a *RemoteCacheAdapter) TakeSessionReport(ctx context.Context) (*SessionReport, error) {
	for {
		if a.stopped.Load() {
			return nil, ErrRemoteCacheAdapterClosed
		}
		a.reportsMu.Lock()
		if len(a.reports) > 0 {
			report := a.reports[0]
			a.reports = slices.Delete(a.reports, 0, 1)
			a.reportsMu.Unlock()
			return report, nil
		}
		a.reportsMu.Unlock()
		if hook := a.testBeforeReportWait; hook != nil {
			hook()
		}
		select {
		case <-a.reportReady:
		case <-a.stopCh:
			return nil, ErrRemoteCacheAdapterClosed
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// ImportValues registers a bundle's results in the local cache. Import needs
// no schema and no session.
func (a *RemoteCacheAdapter) ImportValues(ctx context.Context, bundle dagql.ValueBundle) ([]dagql.ImportedValue, error) {
	if a.stopped.Load() {
		return nil, ErrRemoteCacheAdapterClosed
	}
	return a.cache.ImportValues(ctx, bundle)
}

// ExportValues exports the result numbered root and every result it depends
// on, and lends consume the chains of every completed part owned by the
// results numbered in partsOf. A missing root is ErrRemoteCacheResultNotFound
// and nothing else happens. A missing partsOf number only means fewer
// uploads. Layers are exported uncompressed: a snapshot that already has a
// blob reuses it, and the others get a new uncompressed blob.
func (a *RemoteCacheAdapter) ExportValues(ctx context.Context, root uint64, partsOf []uint64, consume func(context.Context, *dagql.ExportedValues) error) error {
	if a.stopped.Load() {
		return ErrRemoteCacheAdapterClosed
	}
	numbers := append([]uint64{root}, partsOf...)
	return a.cache.WithResultsByNumber(ctx, numbers, func(ctx context.Context, found []dagql.AnyResult, _ []uint64) error {
		if found[0] == nil {
			return fmt.Errorf("%w: result %d", ErrRemoteCacheResultNotFound, root)
		}
		selection := dagql.ValueSelection{Roots: []dagql.AnyResult{found[0]}}
		for _, res := range found[1:] {
			if res != nil {
				selection.OutputsOf = append(selection.OutputsOf, res)
			}
		}
		cfg := config.RefConfig{Compression: compression.New(compression.Uncompressed)}
		return a.cache.WithExportedValues(ctx, selection, cfg, consume)
	})
}

// OfferParts is an engine control operation, not a user's GraphQL call. Its
// receiver is already registered and held by the caller.
func (a *RemoteCacheAdapter) OfferParts(ctx context.Context, receiver dagql.AnyResult, offers []dagql.PersistedPartOffer) ([]dagql.OfferDisposition, error) {
	if a.stopped.Load() {
		out := make([]dagql.OfferDisposition, len(offers))
		for i, offer := range offers {
			address := offer.Address
			address.OutputPath = slices.Clone(address.OutputPath)
			out[i] = dagql.OfferDisposition{Address: address, Outcome: dagql.OfferUnavailable, Err: ErrRemoteCacheAdapterClosed}
		}
		return out, ErrRemoteCacheAdapterClosed
	}
	return a.cache.OfferParts(ctx, receiver, offers)
}

func (a *RemoteCacheAdapter) TakeRenewalRequest(ctx context.Context) (*dagql.RenewalRequest, error) {
	if a.stopped.Load() {
		return nil, ErrRemoteCacheAdapterClosed
	}
	request, err := a.bridge.TakeRenewalRequest(ctx)
	if errors.Is(err, dagql.ErrRemoteCacheBridgeClosed) {
		err = fmt.Errorf("%w: %w", ErrRemoteCacheAdapterClosed, err)
	}
	return request, err
}

func (a *RemoteCacheAdapter) ReplyRenewal(reply dagql.RenewalReply) dagql.RenewalReplyDisposition {
	if a.stopped.Load() {
		return dagql.RenewalReplyDiscarded
	}
	return a.bridge.ReplyRenewal(reply)
}

// close refuses later control calls, detaches this adapter's bridge and
// cancels Run. It does not wait for Run.
func (a *RemoteCacheAdapter) close(cause error) {
	a.stopped.Store(true)
	a.stopChOnce.Do(func() { close(a.stopCh) })
	a.reportsMu.Lock()
	a.reports = nil
	a.reportsMu.Unlock()
	a.cache.DetachRemoteCacheBridge(a.bridge)
	a.cancel(cause)
}

// Stop closes the adapter and joins Run within ctx, the deadline engine
// shutdown already passes. It is idempotent and keeps the first result. A
// Run that ignores cancellation is reported, not terminated.
func (a *RemoteCacheAdapter) Stop(ctx context.Context) error {
	a.stopOnce.Do(func() {
		a.close(errServerShuttingDown)
		// A Run that has already returned has stopped, whatever ctx says.
		select {
		case <-a.runDone:
			return
		default:
		}
		select {
		case <-a.runDone:
		case <-ctx.Done():
			a.stopErr = fmt.Errorf("remote cache integration did not stop: %w", context.Cause(ctx))
		}
	})
	return a.stopErr
}

// startRemoteCacheIntegration attaches the bridge and starts Run for a newly
// created attachment. A nil config allocates nothing.
func (srv *Server) startRemoteCacheIntegration(cfg *RemoteCacheIntegrationConfig) error {
	if cfg == nil {
		return nil
	}
	bridge, created, err := srv.engineCache.AttachRemoteCacheBridge()
	if err != nil {
		return fmt.Errorf("attach remote cache integration: %w", err)
	}
	if !created {
		return nil
	}
	adapter := newRemoteCacheAdapter(srv.engineCache, bridge)
	adapter.startupWait = cfg.StartupWait
	ctx, cancel := context.WithCancelCause(srv.shutdownCtx)
	adapter.cancel = cancel
	srv.remoteCacheAdapter = adapter
	go func() {
		defer close(adapter.runDone)
		err := cfg.Run(ctx, adapter)
		if err != nil && ctx.Err() == nil {
			slog.Error("remote cache integration exited", "error", err)
		}
		// The engine keeps serving; pending exchanges complete as unavailable.
		adapter.close(errRemoteCacheIntegrationExited)
	}()
	return nil
}

// reportSessionResults runs at session removal, before the cache releases
// the session. It queues a session report on the adapter when one exists
// and the session's set holds a result that is retained and not imported.
// It never blocks session teardown on the network: the adapter's list is in
// memory and the channel client sends the report on its own goroutine.
func (srv *Server) reportSessionResults(ctx context.Context, sessionID string) {
	adapter := srv.remoteCacheAdapter
	if adapter == nil || adapter.stopped.Load() {
		return
	}
	entries, err := srv.engineCache.SessionResults(ctx, sessionID)
	if err != nil {
		slog.Warn("remote cache session report not built", "session", sessionID, "error", err)
		return
	}
	if !slices.ContainsFunc(entries, func(entry dagql.SessionResultEntry) bool { return entry.Retained && !entry.Imported }) {
		return
	}
	if !adapter.queueSessionReport(&SessionReport{SessionID: sessionID, Results: entries}) {
		slog.Debug("remote cache session report dropped: adapter stopped", "session", sessionID)
	}
}

func (srv *Server) stopRemoteCacheIntegration(ctx context.Context) error {
	if srv.remoteCacheAdapter == nil {
		return nil
	}
	return srv.remoteCacheAdapter.Stop(ctx)
}

func validateRemoteCacheIntegration(cfg *RemoteCacheIntegrationConfig) error {
	if cfg != nil && cfg.Run == nil {
		return errors.New("remote cache integration requires Run")
	}
	if cfg != nil && cfg.StartupWait < 0 {
		return fmt.Errorf("remote cache integration startup wait must not be negative, got %s", cfg.StartupWait)
	}
	return nil
}
