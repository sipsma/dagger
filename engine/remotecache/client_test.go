package remotecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/remotecache/protocol"
	"github.com/dagger/dagger/engine/server"
	"github.com/dagger/dagger/engine/snapshots"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

const (
	testToken    = "token-a"
	testInstance = "0123456789abcdef0123456789abcdef"
	testBaseURL  = "http://cache.invalid"
	blobBaseURL  = "http://blobs.invalid/put/"
)

// fakeService is an in-process http.RoundTripper that plays the service and
// the blob store. There is no listener. Every poll blocks until the test
// answers it or the request's context ends.
type fakeService struct {
	t *testing.T

	mu       sync.Mutex
	requests []string
	results  map[string]protocol.CommandResult
	reports  []protocol.SessionReport
	blobs    map[digest.Digest][]byte
	bundles  []protocol.BundleUploadRequest
	// pollAnswers feeds poll responses to waiting polls. pollAttempted is
	// signaled once per poll request that reached the service, before the
	// failure check; pollOpened once per waiting poll that passed it. A poll
	// asking for no wait is answered at once with the next page buffered in
	// zeroWaitPages, and with no commands once they are used up. pollWaits
	// records the wait each poll asked for, in order.
	pollAnswers   chan protocol.PollResponse
	pollAttempted chan struct{}
	pollOpened    chan struct{}
	zeroWaitPages chan protocol.PollResponse
	pollWaits     []int
	// pollFailures is how many polls fail with a transport error before one
	// succeeds; pollStatus, when set, answers every poll with that status.
	// pollKick wakes an open poll so it re-reads pollStatus. pollBusy is
	// how many polls, after the failures, are answered 503 with the
	// Retry-After header pollRetryAfter when it is set.
	pollFailures   atomic.Int32
	pollStatus     atomic.Int32
	pollKick       chan struct{}
	pollBusy       atomic.Int32
	pollRetryAfter string
	// pollBusyAnswered is signaled once per 503 answer, after the fake has
	// classified the attempt, so a test can sequence what the next poll
	// meets.
	pollBusyAnswered chan struct{}
	// claimAllBlobs makes the blob check answer that nothing needs
	// uploading, whatever the store holds.
	claimAllBlobs atomic.Bool
	// reportFailures is how many session reports are refused with 500
	// before one is accepted; reportAttempted is signaled per P3 request.
	reportFailures  atomic.Int32
	reportAttempted chan string
	// resultDelivered is signaled once per command result received.
	resultDelivered chan string
	// uploadGate, when set, holds every PUT until closed or the request
	// context ends. uploadsInFlight counts PUTs that have not returned.
	uploadGate      chan struct{}
	uploadsInFlight atomic.Int32
	uploadEntered   chan struct{}
	// uploadStatus answers every PUT with that status when set.
	uploadStatus atomic.Int32
}

func newFakeService(t *testing.T) *fakeService {
	return &fakeService{
		t:                t,
		results:          map[string]protocol.CommandResult{},
		blobs:            map[digest.Digest][]byte{},
		pollAnswers:      make(chan protocol.PollResponse),
		pollAttempted:    make(chan struct{}, 100),
		pollOpened:       make(chan struct{}, 100),
		zeroWaitPages:    make(chan protocol.PollResponse, 16),
		pollBusyAnswered: make(chan struct{}, 100),
		pollKick:         make(chan struct{}, 1),
		reportAttempted:  make(chan string, 100),
		resultDelivered:  make(chan string, 100),
		uploadEntered:    make(chan struct{}, 100),
	}
}

func jsonResponse(status int, body any) *http.Response {
	raw, _ := json.Marshal(body)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}
}

func emptyResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: http.NoBody}
}

func (s *fakeService) record(req *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, req.Method+" "+req.URL.Path)
	s.mu.Unlock()
}

func (s *fakeService) requestLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// The copying accessors below let a test assert with no lock held: a
// failed assertion must not leave the fake's mutex taken, because the
// client's loops take it until the harness cleanup has stopped them.
func (s *fakeService) storedBlobs() map[digest.Digest][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[digest.Digest][]byte, len(s.blobs))
	for k, v := range s.blobs {
		out[k] = v
	}
	return out
}

func (s *fakeService) storedBundles() []protocol.BundleUploadRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.BundleUploadRequest(nil), s.bundles...)
}

func (s *fakeService) storedReports() []protocol.SessionReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.SessionReport(nil), s.reports...)
}

func (s *fakeService) recordedPollWaits() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.pollWaits...)
}

func (s *fakeService) storedResults() map[string]protocol.CommandResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]protocol.CommandResult, len(s.results))
	for k, v := range s.results {
		out[k] = v
	}
	return out
}

func (s *fakeService) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.String(), blobBaseURL) {
		return s.putBlob(req)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+testToken {
		return jsonResponse(http.StatusUnauthorized, protocol.ErrorResponse{Error: "bad token " + got}), nil
	}
	if got := req.Header.Get(protocol.HeaderEngineInstance); got != testInstance {
		return jsonResponse(http.StatusBadRequest, protocol.ErrorResponse{Error: "bad instance " + got}), nil
	}
	s.record(req)
	switch {
	case req.URL.Path == protocol.PathPoll:
		return s.poll(req)
	case strings.HasPrefix(req.URL.Path, "/v1/commands/") && strings.HasSuffix(req.URL.Path, "/result"):
		id := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/commands/"), "/result")
		var result protocol.CommandResult
		if err := json.NewDecoder(req.Body).Decode(&result); err != nil {
			return jsonResponse(http.StatusBadRequest, protocol.ErrorResponse{Error: err.Error()}), nil
		}
		s.mu.Lock()
		s.results[id] = result
		s.mu.Unlock()
		s.resultDelivered <- id
		return emptyResponse(http.StatusNoContent), nil
	case req.URL.Path == protocol.PathSessionReports:
		var report protocol.SessionReport
		if err := json.NewDecoder(req.Body).Decode(&report); err != nil {
			return jsonResponse(http.StatusBadRequest, protocol.ErrorResponse{Error: err.Error()}), nil
		}
		defer func() { s.reportAttempted <- report.SessionID }()
		if s.reportFailures.Load() > 0 {
			s.reportFailures.Add(-1)
			return jsonResponse(http.StatusInternalServerError, protocol.ErrorResponse{Error: "report refused"}), nil
		}
		s.mu.Lock()
		s.reports = append(s.reports, report)
		s.mu.Unlock()
		return emptyResponse(http.StatusAccepted), nil
	case req.URL.Path == protocol.PathBlobCheck:
		var check protocol.BlobCheckRequest
		if err := json.NewDecoder(req.Body).Decode(&check); err != nil {
			return jsonResponse(http.StatusBadRequest, protocol.ErrorResponse{Error: err.Error()}), nil
		}
		answer := protocol.BlobCheckResponse{Upload: map[digest.Digest]protocol.UploadTarget{}}
		s.mu.Lock()
		for _, blob := range check.Blobs {
			if _, ok := s.blobs[blob.Digest]; !ok && !s.claimAllBlobs.Load() {
				answer.Upload[blob.Digest] = protocol.UploadTarget{URL: blobBaseURL + blob.Digest.Encoded()}
			}
		}
		s.mu.Unlock()
		return jsonResponse(http.StatusOK, answer), nil
	case req.URL.Path == protocol.PathBundles:
		var upload protocol.BundleUploadRequest
		if err := json.NewDecoder(req.Body).Decode(&upload); err != nil {
			return jsonResponse(http.StatusBadRequest, protocol.ErrorResponse{Error: err.Error()}), nil
		}
		var bundle dagql.ValueBundle
		if err := json.Unmarshal(upload.Bundle, &bundle); err != nil {
			return jsonResponse(http.StatusBadRequest, protocol.ErrorResponse{Error: err.Error()}), nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		var missing []digest.Digest
		for _, output := range bundle.Outputs {
			if output.Chain == nil {
				continue
			}
			for _, layer := range output.Chain.Layers {
				if _, ok := s.blobs[layer.Descriptor.Digest]; !ok {
					missing = append(missing, layer.Descriptor.Digest)
				}
			}
		}
		if len(missing) > 0 {
			return jsonResponse(http.StatusConflict, protocol.ErrorResponse{Error: "layers missing", Missing: missing}), nil
		}
		s.bundles = append(s.bundles, upload)
		return jsonResponse(http.StatusOK, protocol.BundleUploadResponse{BundleID: protocol.BundleID(upload.Bundle)}), nil
	}
	return jsonResponse(http.StatusNotFound, protocol.ErrorResponse{Error: "no such path " + req.URL.Path}), nil
}

func (s *fakeService) poll(req *http.Request) (*http.Response, error) {
	var body protocol.PollRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return jsonResponse(http.StatusBadRequest, protocol.ErrorResponse{Error: err.Error()}), nil
	}
	if body.WaitSeconds < 0 || body.WaitSeconds > protocol.MaxPollWaitSeconds || body.EngineName == "" {
		return jsonResponse(http.StatusBadRequest, protocol.ErrorResponse{Error: "bad poll body"}), nil
	}
	s.mu.Lock()
	s.pollWaits = append(s.pollWaits, body.WaitSeconds)
	s.mu.Unlock()
	s.pollAttempted <- struct{}{}
	if s.pollFailures.Load() > 0 {
		s.pollFailures.Add(-1)
		return nil, errors.New("connection refused")
	}
	if s.pollBusy.Load() > 0 {
		s.pollBusy.Add(-1)
		resp := jsonResponse(http.StatusServiceUnavailable, protocol.ErrorResponse{Error: "poll again"})
		if s.pollRetryAfter != "" {
			resp.Header.Set("Retry-After", s.pollRetryAfter)
		}
		s.pollBusyAnswered <- struct{}{}
		return resp, nil
	}
	if status := s.pollStatus.Load(); status != 0 {
		return jsonResponse(int(status), protocol.ErrorResponse{Error: "unknown token"}), nil
	}
	if body.WaitSeconds == 0 {
		select {
		case answer := <-s.zeroWaitPages:
			return jsonResponse(http.StatusOK, answer), nil
		default:
			return jsonResponse(http.StatusOK, protocol.PollResponse{}), nil
		}
	}
	s.pollOpened <- struct{}{}
	select {
	case answer := <-s.pollAnswers:
		return jsonResponse(http.StatusOK, answer), nil
	case <-s.pollKick:
		return jsonResponse(int(s.pollStatus.Load()), protocol.ErrorResponse{Error: "unknown token"}), nil
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

// refuseToken answers the open poll, and every later one, with status.
func (s *fakeService) refuseToken(status int) {
	s.pollStatus.Store(int32(status))
	s.pollKick <- struct{}{}
}

func (s *fakeService) putBlob(req *http.Request) (*http.Response, error) {
	// A RoundTripper closes the request body on every path, as
	// http.Transport does; the client waits for that close.
	defer req.Body.Close()
	s.record(req)
	s.uploadsInFlight.Add(1)
	defer s.uploadsInFlight.Add(-1)
	s.uploadEntered <- struct{}{}
	if gate := s.uploadGate; gate != nil {
		select {
		case <-gate:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	if status := s.uploadStatus.Load(); status != 0 {
		return emptyResponse(int(status)), nil
	}
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != req.ContentLength {
		return emptyResponse(http.StatusBadRequest), nil
	}
	dgst := digest.FromBytes(data)
	if dgst.Encoded() != strings.TrimPrefix(req.URL.String(), blobBaseURL) {
		return emptyResponse(http.StatusBadRequest), nil
	}
	s.mu.Lock()
	s.blobs[dgst] = data
	s.mu.Unlock()
	return emptyResponse(http.StatusOK), nil
}

// memProvider serves blobs from memory: the in-memory blob reader of an
// export's chains.
type memProvider map[digest.Digest][]byte

// memReaderAt refuses reads after Close, so a transport that touches the
// export's reader after the callback released it is caught.
type memReaderAt struct {
	*bytes.Reader
	closed  atomic.Bool
	onClose func()
}

func (r *memReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if r.closed.Load() {
		return 0, errors.New("read after close")
	}
	return r.Reader.ReadAt(p, off)
}

func (r *memReaderAt) Close() error {
	r.closed.Store(true)
	if r.onClose != nil {
		r.onClose()
	}
	return nil
}

func (p memProvider) Info(_ context.Context, dgst digest.Digest) (content.Info, error) {
	data, ok := p[dgst]
	if !ok {
		return content.Info{}, fmt.Errorf("no blob %s", dgst)
	}
	return content.Info{Digest: dgst, Size: int64(len(data))}, nil
}

func (p memProvider) ReaderAt(_ context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	data, ok := p[desc.Digest]
	if !ok {
		return nil, fmt.Errorf("no blob %s", desc.Digest)
	}
	return &memReaderAt{Reader: bytes.NewReader(data)}, nil
}

func blobLayer(data []byte) snapshots.ExportLayer {
	return snapshots.ExportLayer{Descriptor: ocispecs.Descriptor{MediaType: ocispecs.MediaTypeImageLayer, Digest: digest.FromBytes(data), Size: int64(len(data))}}
}

// fakeAdapter plays the engine. Its export hands the consumer a bundle with
// one chain whose blobs come from memory.
type fakeAdapter struct {
	reports chan *server.SessionReport

	mu        sync.Mutex
	imported  []dagql.ValueBundle
	importErr error
	exported  []uint64
	exportErr error
	// blobs are the chain's layers, in order. bundleOutputs marks whether
	// the bundle describes the chain, as a real export does.
	blobs [][]byte
	// exportStarted is signaled when an export enters the consumer;
	// exportGate, when set, holds the export there until closed.
	exportStarted chan uint64
	exportGate    chan struct{}
	// importEntered is signaled when an import enters the adapter;
	// importGate, when set, holds the import there until closed, from the
	// importGateFrom-th import on (every import when zero).
	importEntered  chan struct{}
	importGate     chan struct{}
	importGateFrom int
	importsEntered atomic.Int32
	// startup is the real gate the engine's startup waits on; startupCalls
	// counts the client's StartupComplete calls.
	startup      *server.RemoteCacheStartupGate
	startupCalls atomic.Int32
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{reports: make(chan *server.SessionReport, 10), exportStarted: make(chan uint64, 100), importEntered: make(chan struct{}, 100), startup: server.NewRemoteCacheStartupGate()}
}

func (a *fakeAdapter) StartupComplete(imports int) {
	a.startupCalls.Add(1)
	a.startup.Complete(imports)
}

func (a *fakeAdapter) TakeSessionReport(ctx context.Context) (*server.SessionReport, error) {
	select {
	case report := <-a.reports:
		return report, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (a *fakeAdapter) ImportValues(ctx context.Context, bundle dagql.ValueBundle) ([]dagql.ImportedValue, error) {
	a.importEntered <- struct{}{}
	if a.importGate != nil && int(a.importsEntered.Add(1)) >= a.importGateFrom {
		select {
		case <-a.importGate:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.importErr != nil {
		return nil, a.importErr
	}
	a.imported = append(a.imported, bundle)
	// Cache.ImportValues returns one mapping per root, not per value.
	return make([]dagql.ImportedValue, len(bundle.Roots)), nil
}

func (a *fakeAdapter) calls() (imported []dagql.ValueBundle, exported []uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]dagql.ValueBundle(nil), a.imported...), append([]uint64(nil), a.exported...)
}

func (a *fakeAdapter) exportedValues() *dagql.ExportedValues {
	provider := memProvider{}
	var layers []snapshots.ExportLayer
	for _, data := range a.blobs {
		layer := blobLayer(data)
		provider[layer.Descriptor.Digest] = data
		layers = append(layers, layer)
	}
	bundle := dagql.ValueBundle{Version: 2, Roots: []dagql.TransferredRoot{{Ordinal: 1}}, Values: []dagql.TransferredValue{{Ordinal: 1, Record: dagql.PersistedRecord{ResultID: 1}}}}
	values := &dagql.ExportedValues{Bundle: bundle, Chains: &dagql.SelectedChains{}}
	if len(layers) > 0 {
		values.Bundle.Outputs = []dagql.TransferredOutput{{Ordinal: 1, Address: dagql.PersistedPartAddress{Part: "snapshot"}, State: "completed", Value: &dagql.SnapshotValue{Kind: "directory"}, Chain: &dagql.OfferedChain{Layers: layers}, Owner: &dagql.PersistedOfferOwner{}}}
		// Two entries share the same chain: the layers are still uploaded once.
		values.Chains.Entries = []dagql.SelectedChain{{Ordinal: 1, Layers: layers, Provider: provider}, {Ordinal: 1, Layers: layers[:1], Provider: provider}}
	}
	return values
}

func (a *fakeAdapter) ExportValues(ctx context.Context, root uint64, _ []uint64, consume func(context.Context, *dagql.ExportedValues) error) error {
	a.mu.Lock()
	a.exported = append(a.exported, root)
	exportErr := a.exportErr
	a.mu.Unlock()
	if exportErr != nil {
		return exportErr
	}
	a.exportStarted <- root
	if a.exportGate != nil {
		select {
		case <-a.exportGate:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	return consume(ctx, a.exportedValues())
}

type harness struct {
	svc     *fakeService
	adapter *fakeAdapter
	client  *client
	done    chan error
	cancel  context.CancelCauseFunc
}

// start runs the client on its own goroutine. stop cancels it and waits.
func start(t *testing.T) *harness {
	t.Helper()
	return startConfigured(t, nil)
}

// startConfigured is start with configure run on the fake service and
// adapter before the client's first request can reach them.
func startConfigured(t *testing.T, configure func(*harness)) *harness {
	t.Helper()
	h := &harness{svc: newFakeService(t), adapter: newFakeAdapter(), done: make(chan error, 1)}
	h.client = newClient(Config{URL: testBaseURL + "/", Token: testToken, EngineName: "engine-a", EngineVersion: "v1"}, testInstance, h.svc, h.adapter)
	if configure != nil {
		configure(h)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	h.cancel = cancel
	go func() { h.done <- h.client.run(ctx) }()
	t.Cleanup(func() {
		cancel(nil)
		h.wait(t)
	})
	return h
}

func (h *harness) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.done:
		h.done <- err
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("client run did not return")
		return nil
	}
}

func within[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting")
		var zero T
		return zero
	}
}

func (h *harness) answerPoll(t *testing.T, commands ...protocol.Command) {
	t.Helper()
	within(t, h.svc.pollOpened)
	select {
	case h.svc.pollAnswers <- protocol.PollResponse{Commands: commands}:
	case <-time.After(10 * time.Second):
		t.Fatal("no poll took the answer")
	}
}

// result returns the answer to one command, waiting for it when it has
// not arrived. The stored results are checked before every wait, so a
// notification consumed for another command is never a loss.
func (h *harness) result(t *testing.T, id string) protocol.CommandResult {
	t.Helper()
	for {
		if result, ok := h.svc.storedResults()[id]; ok {
			return result
		}
		within(t, h.svc.resultDelivered)
	}
}

func exportCommand(id string, root uint64) protocol.Command {
	return protocol.Command{ID: id, Type: protocol.CommandTypeExport, Export: &protocol.ExportCommand{Root: root, PartsOf: []uint64{root}}}
}

func TestClientExecutesAndAnswersCommands(t *testing.T) {
	t.Parallel()
	h := start(t)
	already, fresh := []byte("already in the store"), []byte("fresh bytes to upload")
	h.svc.blobs[digest.FromBytes(already)] = already
	h.adapter.blobs = [][]byte{already, fresh}
	bundle := dagql.ValueBundle{Version: 2, Roots: []dagql.TransferredRoot{{Ordinal: 1}}, Values: []dagql.TransferredValue{{Ordinal: 1, Record: dagql.PersistedRecord{ResultID: 1}}}}
	h.answerPoll(t,
		protocol.Command{ID: "c-1", Type: protocol.CommandTypeImport, Import: &protocol.ImportCommand{BundleID: "sha256:x", Bundle: bundle}},
		exportCommand("c-2", 58),
	)
	require.Equal(t, protocol.CommandResult{OK: true}, h.result(t, "c-1"))
	exported := h.result(t, "c-2")
	require.True(t, exported.OK, exported.Error)
	require.Equal(t, protocol.ExportStatusExported, exported.Status)

	imported, exportedRoots := h.adapter.calls()
	require.Equal(t, []dagql.ValueBundle{bundle}, imported)
	require.Equal(t, []uint64{58}, exportedRoots)

	// Only the blob the service asked for was uploaded, and the bundle was
	// posted last, with the ID the service computed over the posted bytes.
	require.Equal(t, fresh, h.svc.storedBlobs()[digest.FromBytes(fresh)])
	bundles := h.svc.storedBundles()
	require.Len(t, bundles, 1)
	require.Equal(t, "c-2", bundles[0].CommandID)
	require.Equal(t, protocol.BundleID(bundles[0].Bundle), exported.BundleID)
	var posted dagql.ValueBundle
	require.NoError(t, json.Unmarshal(bundles[0].Bundle, &posted))
	require.Len(t, posted.Outputs, 1)
	require.Empty(t, posted.Outputs[0].Chain.Addresses)
	// The import worker's answer runs concurrently with the export, so only
	// the export's own requests are ordered: check, one PUT, the bundle
	// last, then the answer.
	log := h.svc.requestLog()
	require.Contains(t, log, "POST /v1/commands/c-1/result")
	require.Equal(t, []string{
		"POST /v1/blobs/check",
		"PUT /put/" + digest.FromBytes(fresh).Encoded(),
		"POST /v1/bundles",
		"POST /v1/commands/c-2/result",
	}, only(log, "POST /v1/blobs/check", "PUT ", "POST /v1/bundles", "POST /v1/commands/c-2/result"), "one PUT, the bundle last")
}

// only keeps the log entries that start with one of the prefixes.
func only(log []string, prefixes ...string) []string {
	var out []string
	for _, item := range log {
		for _, prefix := range prefixes {
			if strings.HasPrefix(item, prefix) {
				out = append(out, item)
				break
			}
		}
	}
	return out
}

func TestClientNextPollOpensDuringExport(t *testing.T) {
	t.Parallel()
	h := start(t)
	h.adapter.exportGate = make(chan struct{})
	h.answerPoll(t, exportCommand("c-1", 1))
	within(t, h.adapter.exportStarted)
	// The export is held inside the adapter. The next poll is already open.
	within(t, h.svc.pollOpened)
	require.Empty(t, h.svc.storedResults())
	close(h.adapter.exportGate)
	require.Equal(t, protocol.ExportStatusExported, h.result(t, "c-1").Status)
}

func TestClientTwentyExportsInOrder(t *testing.T) {
	t.Parallel()
	h := start(t)
	var commands []protocol.Command
	for i := range 20 {
		commands = append(commands, exportCommand(fmt.Sprintf("c-%02d", i), uint64(100+i)))
	}
	h.answerPoll(t, commands...)
	for _, cmd := range commands {
		require.Equal(t, protocol.ExportStatusExported, h.result(t, cmd.ID).Status, cmd.ID)
	}
	_, exportedRoots := h.adapter.calls()
	require.Len(t, exportedRoots, 20)
	for i, root := range exportedRoots {
		require.Equal(t, uint64(100+i), root, "executed in the order received")
	}
}

func TestClientPollBackoff(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// recv bounds a wait in virtual time, generously above the 30-second
		// backoff cap, so a stuck loop fails the test instead of hanging it.
		recv := func(ch <-chan struct{}) {
			t.Helper()
			select {
			case <-ch:
			case <-time.After(5 * time.Minute):
				t.Fatal("timed out waiting")
			}
		}
		svc, adapter := newFakeService(t), newFakeAdapter()
		svc.pollFailures.Store(5)
		c := newClient(Config{URL: testBaseURL, Token: testToken, EngineName: "engine-a", EngineVersion: "v1"}, testInstance, svc, adapter)
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		done := make(chan error, 1)
		go func() { done <- c.run(ctx) }()
		var attempts []time.Time
		attempt := func() { recv(svc.pollAttempted); attempts = append(attempts, time.Now()) }
		for range 6 {
			attempt()
		}
		// Five failures, five waits: 1s, 2s, 4s, 8s, 16s. The sixth poll
		// passed the failure check; as the first answered poll it asked for
		// no wait and was answered at once, so the seventh opened at once
		// and waits for its answer.
		require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}, gaps(attempts))
		attempt()
		require.Equal(t, time.Duration(0), attempts[6].Sub(attempts[5]))
		require.Equal(t, []int{0, 0, 0, 0, 0, 0, pollWaitSeconds}, svc.recordedPollWaits(), "no wait until a poll is answered")
		send := func() {
			t.Helper()
			select {
			case svc.pollAnswers <- protocol.PollResponse{}:
			case <-time.After(5 * time.Minute):
				t.Fatal("no poll took the answer")
			}
		}
		recv(svc.pollOpened)
		send()
		// The next poll opens at once; it too waits for an answer. Only then
		// is the counter changed, so the poll being answered stays a success
		// and a new failure starts over at 1s.
		attempt()
		require.Equal(t, time.Duration(0), attempts[7].Sub(attempts[6]))
		recv(svc.pollOpened)
		svc.pollFailures.Store(7)
		send()
		first := len(attempts)
		for range 7 {
			attempt()
		}
		require.Equal(t, []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}, gaps(attempts[first-1:]), "doubles up to thirty seconds")
		cancel(nil)
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Minute):
			t.Fatal("run did not return")
		}
	})
}

func gaps(times []time.Time) []time.Duration {
	var out []time.Duration
	for i := 1; i < len(times); i++ {
		out = append(out, times[i].Sub(times[i-1]))
	}
	return out
}

func TestClientRunReturnsAfterUpload(t *testing.T) {
	t.Parallel()
	t.Run("context ends", func(t *testing.T) {
		t.Parallel()
		h := start(t)
		h.svc.uploadGate = make(chan struct{})
		h.adapter.blobs = [][]byte{[]byte("held upload")}
		h.answerPoll(t, exportCommand("c-1", 1))
		within(t, h.svc.uploadEntered)
		require.Equal(t, int32(1), h.svc.uploadsInFlight.Load())
		h.cancel(nil)
		require.NoError(t, h.wait(t))
		require.Zero(t, h.svc.uploadsInFlight.Load(), "run returned only after the upload returned")
		require.Empty(t, h.svc.storedBundles())
	})
	t.Run("poll loop fails", func(t *testing.T) {
		t.Parallel()
		h := start(t)
		h.svc.uploadGate = make(chan struct{})
		h.adapter.blobs = [][]byte{[]byte("held upload")}
		h.answerPoll(t, exportCommand("c-1", 1))
		within(t, h.svc.uploadEntered)
		// The service now refuses the token: the poll loop stops the client,
		// which cancels the running upload and waits for it.
		h.svc.refuseToken(http.StatusUnauthorized)
		err := h.wait(t)
		require.ErrorIs(t, err, errUnrecoverable)
		var refused *serviceError
		require.ErrorAs(t, err, &refused)
		require.Equal(t, http.StatusUnauthorized, refused.status)
		require.Zero(t, h.svc.uploadsInFlight.Load())
	})
}

func TestClientExportFailures(t *testing.T) {
	t.Parallel()
	t.Run("failed upload fails the export and posts no bundle", func(t *testing.T) {
		t.Parallel()
		h := start(t)
		h.svc.uploadStatus.Store(http.StatusInternalServerError)
		h.adapter.blobs = [][]byte{[]byte("refused")}
		h.answerPoll(t, exportCommand("c-1", 1))
		result := h.result(t, "c-1")
		require.False(t, result.OK)
		require.Equal(t, protocol.ExportStatusFailed, result.Status)
		require.Contains(t, result.Error, "status 500")
		require.Empty(t, h.svc.storedBundles())
	})
	t.Run("conflict on the bundle fails the export", func(t *testing.T) {
		t.Parallel()
		h := start(t)
		// The check claims the store holds the blob, so nothing is
		// uploaded; the bundle post then finds it absent and answers 409.
		h.svc.claimAllBlobs.Store(true)
		h.adapter.blobs = [][]byte{[]byte("claimed but absent")}
		h.answerPoll(t, exportCommand("c-1", 1))
		result := h.result(t, "c-1")
		require.False(t, result.OK)
		require.Equal(t, protocol.ExportStatusFailed, result.Status)
		require.Contains(t, result.Error, "409")
	})
	t.Run("statuses", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			err    error
			status protocol.ExportStatus
		}{
			{fmt.Errorf("%w: result 7", server.ErrRemoteCacheResultNotFound), protocol.ExportStatusNotFound},
			{fmt.Errorf("%w: row 7 ownership changed", dagql.ErrPersistStateNotReady), protocol.ExportStatusNotReady},
			{errors.New("transfer capture: row 7 has no transferable frame"), protocol.ExportStatusFailed},
		} {
			h := start(t)
			h.adapter.exportErr = tc.err
			h.answerPoll(t, exportCommand("c-1", 7))
			result := h.result(t, "c-1")
			require.False(t, result.OK)
			require.Equal(t, tc.status, result.Status)
			require.Equal(t, tc.err.Error(), result.Error)
		}
	})
	t.Run("failed import", func(t *testing.T) {
		t.Parallel()
		h := start(t)
		h.adapter.importErr = errors.New("expired transfer root 1")
		h.answerPoll(t, protocol.Command{ID: "c-1", Type: protocol.CommandTypeImport, Import: &protocol.ImportCommand{BundleID: "sha256:x"}})
		require.Equal(t, protocol.CommandResult{OK: false, Error: "expired transfer root 1"}, h.result(t, "c-1"))
	})
}

func TestClientSessionReports(t *testing.T) {
	t.Parallel()
	h := start(t)
	h.svc.reportFailures.Store(1)
	h.adapter.reports <- &server.SessionReport{SessionID: "dropped", Results: []dagql.SessionResultEntry{{ResultID: 1, Type: "Int", Field: "one", Retained: true}}}
	h.adapter.reports <- &server.SessionReport{SessionID: "kept", Results: []dagql.SessionResultEntry{
		{ResultID: 58, Type: "Directory", Field: "directory", DependsOn: []uint64{57}, Retained: true, RecipeDigest: digest.Digest("xxh3:1f0c")},
		{ResultID: 60, Type: "String", Field: "stdout", DependsOn: []uint64{57}},
	}}
	// The first report is refused and dropped; the loop goes on and the
	// second is accepted. The adapter's channel is drained in order.
	require.Equal(t, "dropped", within(t, h.svc.reportAttempted))
	require.Equal(t, "kept", within(t, h.svc.reportAttempted))
	reports := h.svc.storedReports()
	require.Len(t, reports, 1)
	require.Equal(t, protocol.SessionReport{SessionID: "kept", Results: []protocol.ReportedResult{
		{Result: 58, Type: "Directory", Field: "directory", DependsOn: []uint64{57}, Retained: true, RecipeDigest: "xxh3:1f0c"},
		{Result: 60, Type: "String", Field: "stdout", DependsOn: []uint64{57}},
	}}, reports[0])
	raw, err := json.Marshal(sessionReportBody(&server.SessionReport{SessionID: "s", Results: []dagql.SessionResultEntry{{ResultID: 1}}}))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"dependsOn":[]`, "an empty dependency list is an array, not null")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// gatedReaderAt is an export reader whose reads can be held by the test,
// and which refuses reads after Close.
type gatedReaderAt struct {
	*bytes.Reader
	gate        chan struct{}
	readStarted chan struct{}
	closed      atomic.Bool
	reads       atomic.Int32
}

func (r *gatedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if r.closed.Load() {
		return 0, errors.New("read after close")
	}
	r.reads.Add(1)
	select {
	case r.readStarted <- struct{}{}:
	default:
	}
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-time.After(5 * time.Minute):
			return 0, errors.New("gated read: gate never opened")
		}
	}
	return r.Reader.ReadAt(p, off)
}

func (r *gatedReaderAt) Close() error {
	r.closed.Store(true)
	return nil
}

type fixedProvider struct {
	layer  snapshots.ExportLayer
	reader *gatedReaderAt
}

func (p fixedProvider) Info(context.Context, digest.Digest) (content.Info, error) {
	return content.Info{Digest: p.layer.Descriptor.Digest, Size: p.layer.Descriptor.Size}, nil
}

func (p fixedProvider) ReaderAt(context.Context, ocispecs.Descriptor) (content.ReaderAt, error) {
	return p.reader, nil
}

func uploadTestLayer(data []byte, gate chan struct{}) (exportLayer, *gatedReaderAt) {
	layer := blobLayer(data)
	reader := &gatedReaderAt{Reader: bytes.NewReader(data), gate: gate, readStarted: make(chan struct{}, 1)}
	return exportLayer{descriptor: layer.Descriptor, provider: fixedProvider{layer: layer, reader: reader}}, reader
}

// waitFor bounds a wait inside a worker goroutine, where t.Fatal cannot be
// used, and returns an error the test checks afterwards.
func waitFor(ch <-chan struct{}, what string) error {
	select {
	case <-ch:
		return nil
	case <-time.After(5 * time.Minute):
		return errors.New("timed out waiting for " + what)
	}
}

// recvWithin bounds a channel receive in the bubble's virtual time.
func recvWithin[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Minute):
		t.Fatal("timed out waiting")
		var zero T
		return zero
	}
}

// A production client bounds an upload's response headers on its transport,
// counted by Go from the moment the whole request has been written, and
// leaves the API client's transport unbounded, since every API request is
// bounded by its context. A scripted transport is shared by both.
func TestNewClientTransports(t *testing.T) {
	t.Parallel()
	c := newClient(Config{URL: testBaseURL, Token: testToken}, testInstance, nil, newFakeAdapter())
	uploads, ok := c.uploads.Transport.(*http.Transport)
	require.True(t, ok)
	require.Equal(t, requestTimeout, uploads.ResponseHeaderTimeout)
	api, ok := c.http.Transport.(*http.Transport)
	require.True(t, ok)
	require.Zero(t, api.ResponseHeaderTimeout)
	require.NotSame(t, http.DefaultTransport, uploads)
	require.NotSame(t, http.DefaultTransport, api)
	require.NotSame(t, api, uploads)

	scripted := newFakeService(t)
	c = newClient(Config{URL: testBaseURL, Token: testToken}, testInstance, scripted, newFakeAdapter())
	require.Same(t, scripted, c.http.Transport)
	require.Same(t, scripted, c.uploads.Transport)
}

// A transport may go on reading the request body, and close it, after Do
// has returned. uploadBlob must not return, nor close the export's reader,
// before the transport has closed the body.
func TestUploadBlobWaitsForTheTransportToCloseTheBody(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		data := []byte("bytes the transport reads after Do returned")
		layer, reader := uploadTestLayer(data, nil)
		releaseBody := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseBody) }) }
		defer release()
		bodyClosed := make(chan struct{})
		var got []byte
		var readErr error
		transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			go func() {
				defer close(bodyClosed)
				defer req.Body.Close()
				if readErr = waitFor(releaseBody, "the body release"); readErr != nil {
					return
				}
				got, readErr = io.ReadAll(req.Body)
			}()
			return emptyResponse(http.StatusOK), nil
		})
		c := newClient(Config{URL: testBaseURL, Token: testToken}, testInstance, transport, newFakeAdapter())
		atBodyWait := make(chan struct{}, 1)
		c.testBeforeBodyWait = func() { atBodyWait <- struct{}{} }
		returned := make(chan error, 1)
		go func() { returned <- c.uploadBlob(t.Context(), blobBaseURL+"x", layer) }()
		recvWithin(t, atBodyWait)
		// Do has returned and uploadBlob is at its body wait. Every
		// goroutine is now durably blocked on the release, so the checks
		// below are deterministic.
		synctest.Wait()
		select {
		case err := <-returned:
			t.Fatalf("uploadBlob returned before the transport closed the body: %v", err)
		default:
		}
		require.False(t, reader.closed.Load(), "the export's reader is still open while the transport reads")
		release()
		recvWithin(t, bodyClosed)
		require.NoError(t, recvWithin(t, returned))
		require.NoError(t, readErr, "every read happened before the reader was closed")
		require.Equal(t, data, got)
		require.True(t, reader.closed.Load(), "the reader is closed once the transport is done")
	})
}

// Close may run while a Read is in flight. It must wait for that read to
// finish before the export's reader is closed, and refuse reads after it.
func TestUploadBodyCloseWaitsForActiveRead(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		data := []byte("bytes read while the transport gives up")
		gate := make(chan struct{})
		var gateOnce sync.Once
		open := func() { gateOnce.Do(func() { close(gate) }) }
		defer open()
		layer, reader := uploadTestLayer(data, gate)
		closeReturned, readReturned := make(chan struct{}), make(chan struct{})
		var n int
		var readErr, laterErr, transportErr error
		transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			go func() {
				defer close(readReturned)
				buf := make([]byte, 8)
				n, readErr = req.Body.Read(buf)
				// After Close, the body refuses reads.
				if laterErr = waitFor(closeReturned, "Close to return"); laterErr != nil {
					return
				}
				_, laterErr = req.Body.Read(buf)
			}()
			if transportErr = waitFor(reader.readStarted, "the read to start"); transportErr != nil {
				// Early exit: the transport still closes the body.
				close(closeReturned)
				req.Body.Close()
				return nil, transportErr
			}
			// The read is in flight, held at the gate. Close now, on the
			// transport's own goroutine, and give up on the request.
			go func() {
				defer close(closeReturned)
				req.Body.Close()
			}()
			return nil, errors.New("connection reset")
		})
		c := newClient(Config{URL: testBaseURL, Token: testToken}, testInstance, transport, newFakeAdapter())
		atBodyWait := make(chan struct{}, 1)
		c.testBeforeBodyWait = func() { atBodyWait <- struct{}{} }
		returned := make(chan error, 1)
		go func() { returned <- c.uploadBlob(t.Context(), blobBaseURL+"x", layer) }()
		recvWithin(t, atBodyWait)
		// Do returned its error; the read is still held at the gate, Close
		// waits for it, and uploadBlob waits for Close.
		synctest.Wait()
		select {
		case <-closeReturned:
			t.Fatal("Close returned while a read was in flight")
		case err := <-returned:
			t.Fatalf("uploadBlob returned while a read was in flight: %v", err)
		default:
		}
		require.False(t, reader.closed.Load())
		open()
		recvWithin(t, closeReturned)
		recvWithin(t, readReturned)
		require.ErrorContains(t, recvWithin(t, returned), "connection reset")
		require.NoError(t, transportErr)
		require.NoError(t, readErr, "the read in flight finished against an open reader")
		require.Equal(t, 8, n)
		require.ErrorContains(t, laterErr, "upload body closed")
		require.Equal(t, int32(1), reader.reads.Load(), "no read reached the reader after Close")
		require.True(t, reader.closed.Load())
	})
}
