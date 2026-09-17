package remotecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/remotecache/protocol"
	"github.com/dagger/dagger/engine/server"
	"golang.org/x/sync/errgroup"
)

// engineAdapter is what the client needs from the engine. The server's
// RemoteCacheAdapter is the only production implementation.
type engineAdapter interface {
	TakeSessionReport(context.Context) (*server.SessionReport, error)
	ImportValues(context.Context, dagql.ValueBundle) ([]dagql.ImportedValue, error)
	ExportValues(context.Context, uint64, []uint64, func(context.Context, *dagql.ExportedValues) error) error
}

var _ engineAdapter = (*server.RemoteCacheAdapter)(nil)

const (
	// pollWaitSeconds is what the engine asks the service to hold a poll
	// open for. Below the service's cap, so an answer is never late.
	pollWaitSeconds = 25
	// pollBackoffMin and pollBackoffMax bound the retry delay after a
	// failed poll: it starts at the minimum and doubles up to the maximum.
	pollBackoffMin = time.Second
	pollBackoffMax = 30 * time.Second
	// requestTimeout bounds every request except the poll and the blob
	// uploads.
	requestTimeout = 30 * time.Second
	// pollTimeout bounds a poll: the service's wait plus the same margin,
	// so a dead connection cannot hold the poll loop forever.
	pollTimeout = pollWaitSeconds*time.Second + requestTimeout
	// maxConcurrentUploads bounds the blob uploads of one export.
	maxConcurrentUploads = 4
)

// errUnrecoverable marks a failure after which the client stops: the
// service refused the engine's token.
var errUnrecoverable = errors.New("remote cache client stopped")

// client is the channel between one engine and the service.
type client struct {
	cfg        Config
	baseURL    string
	instanceID string
	http       *http.Client
	adapter    engineAdapter
	log        *slog.Logger

	imports, exports *commandQueue
}

func newClient(cfg Config, instanceID string, transport http.RoundTripper, adapter engineAdapter) *client {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &client{
		cfg:        cfg,
		baseURL:    strings.TrimRight(cfg.URL, "/"),
		instanceID: instanceID,
		http:       &http.Client{Transport: transport},
		adapter:    adapter,
		log:        slog.With("component", "remote-cache", "engineInstance", instanceID),
		imports:    newCommandQueue(),
		exports:    newCommandQueue(),
	}
}

// run starts the four loops and returns only after all of them, and every
// upload they started, have returned. When ctx ends every loop stops. When
// one loop fails with an unrecoverable error, the others are canceled and
// that error is returned.
func (c *client) run(ctx context.Context) error {
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error { return c.pollLoop(ctx) })
	group.Go(func() error { return c.importWorker(ctx) })
	group.Go(func() error { return c.exportWorker(ctx) })
	group.Go(func() error { return c.reportLoop(ctx) })
	err := group.Wait()
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// commandQueue is an in-memory list with no size limit. A command is never
// refused because the engine is busy.
type commandQueue struct {
	mu    sync.Mutex
	items []protocol.Command
	wake  chan struct{}
}

func newCommandQueue() *commandQueue {
	return &commandQueue{wake: make(chan struct{}, 1)}
}

func (q *commandQueue) push(cmd protocol.Command) {
	q.mu.Lock()
	q.items = append(q.items, cmd)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// next returns the oldest command, waiting for one when the list is empty.
func (q *commandQueue) next(ctx context.Context) (protocol.Command, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			cmd := q.items[0]
			q.items[0] = protocol.Command{}
			q.items = q.items[1:]
			q.mu.Unlock()
			return cmd, nil
		}
		q.mu.Unlock()
		select {
		case <-q.wake:
		case <-ctx.Done():
			return protocol.Command{}, context.Cause(ctx)
		}
	}
}

// pollLoop keeps exactly one poll open. It executes nothing itself: each
// command goes on the import or export list, and the next poll opens at
// once. A failed poll is retried after a delay that starts at one second
// and doubles up to thirty seconds. A refused token stops the client.
func (c *client) pollLoop(ctx context.Context) error {
	backoff := pollBackoffMin
	registered := false
	for {
		var response protocol.PollResponse
		status, err := c.do(ctx, pollTimeout, http.MethodPost, protocol.PathPoll, protocol.PollRequest{EngineName: c.cfg.EngineName, EngineVersion: c.cfg.EngineVersion, WaitSeconds: pollWaitSeconds}, &response)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err != nil {
			if status == http.StatusUnauthorized {
				c.log.Error("remote cache service refused the engine's token; stopping the client", "error", err)
				return fmt.Errorf("%w: %w", errUnrecoverable, err)
			}
			c.log.Warn("remote cache poll failed; retrying", "error", err, "retryIn", backoff)
			if err := sleepContext(ctx, backoff); err != nil {
				return err
			}
			backoff = min(backoff*2, pollBackoffMax)
			continue
		}
		backoff = pollBackoffMin
		if !registered {
			registered = true
			c.log.Info("registered with the remote cache service", "url", c.cfg.URL, "engineName", c.cfg.EngineName)
		}
		for _, cmd := range response.Commands {
			switch cmd.Type {
			case protocol.CommandTypeImport:
				if cmd.Import == nil {
					c.log.Warn("remote cache import command without a body; ignored", "command", cmd.ID)
					continue
				}
				c.imports.push(cmd)
			case protocol.CommandTypeExport:
				if cmd.Export == nil {
					c.log.Warn("remote cache export command without a body; ignored", "command", cmd.ID)
					continue
				}
				c.exports.push(cmd)
			default:
				c.log.Warn("remote cache command of unknown type; ignored", "command", cmd.ID, "type", cmd.Type)
			}
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// importWorker takes import commands in order, one at a time.
func (c *client) importWorker(ctx context.Context) error {
	for {
		cmd, err := c.imports.next(ctx)
		if err != nil {
			return err
		}
		result := c.runImport(ctx, cmd)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		c.sendResult(ctx, cmd.ID, result)
	}
}

func (c *client) runImport(ctx context.Context, cmd protocol.Command) protocol.CommandResult {
	mapping, err := c.adapter.ImportValues(ctx, cmd.Import.Bundle)
	if err != nil {
		c.log.Error("remote cache import failed", "command", cmd.ID, "bundle", cmd.Import.BundleID, "error", err)
		return protocol.CommandResult{OK: false, Error: err.Error()}
	}
	c.log.Info("imported remote cache bundle", "command", cmd.ID, "bundle", cmd.Import.BundleID, "results", len(mapping))
	return protocol.CommandResult{OK: true}
}

// exportWorker takes export commands in order, one at a time. One export at
// a time is enough for the demo.
func (c *client) exportWorker(ctx context.Context) error {
	for {
		cmd, err := c.exports.next(ctx)
		if err != nil {
			return err
		}
		result := c.runExport(ctx, cmd)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		c.sendResult(ctx, cmd.ID, result)
	}
}

func (c *client) runExport(ctx context.Context, cmd protocol.Command) protocol.CommandResult {
	var outcome uploadOutcome
	err := c.adapter.ExportValues(ctx, cmd.Export.Root, cmd.Export.PartsOf, func(ctx context.Context, values *dagql.ExportedValues) error {
		var err error
		outcome, err = c.upload(ctx, cmd.ID, values)
		return err
	})
	if err != nil {
		result := protocol.CommandResult{OK: false, Status: protocol.ExportStatusFailed, Error: err.Error()}
		switch {
		case errors.Is(err, server.ErrRemoteCacheResultNotFound):
			result.Status = protocol.ExportStatusNotFound
		case errors.Is(err, dagql.ErrPersistStateNotReady):
			result.Status = protocol.ExportStatusNotReady
		}
		c.log.Error("remote cache export failed", "command", cmd.ID, "root", cmd.Export.Root, "status", result.Status, "error", err)
		return result
	}
	c.log.Info("exported remote cache bundle", "command", cmd.ID, "root", cmd.Export.Root, "bundle", outcome.bundleID, "results", outcome.results, "blobsUploaded", outcome.blobsUploaded, "bytesUploaded", outcome.bytesUploaded)
	return protocol.CommandResult{OK: true, Status: protocol.ExportStatusExported, BundleID: outcome.bundleID}
}

// sendResult answers one command. A failed answer is logged and dropped;
// the service then shows the command as unanswered.
func (c *client) sendResult(ctx context.Context, commandID string, result protocol.CommandResult) {
	if _, err := c.do(ctx, requestTimeout, http.MethodPost, protocol.CommandResultPath(commandID), result, nil); err != nil {
		c.log.Warn("remote cache command result not delivered", "command", commandID, "error", err)
	}
}

// reportLoop sends each session report once. A failed request drops the
// report.
func (c *client) reportLoop(ctx context.Context) error {
	for {
		report, err := c.adapter.TakeSessionReport(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			if errors.Is(err, server.ErrRemoteCacheAdapterClosed) {
				// The adapter stops only when the engine shuts down or Run
				// has already returned; the context follows.
				<-ctx.Done()
				return context.Cause(ctx)
			}
			return fmt.Errorf("%w: take session report: %w", errUnrecoverable, err)
		}
		body := sessionReportBody(report)
		if _, err := c.do(ctx, requestTimeout, http.MethodPost, protocol.PathSessionReports, body, nil); err != nil {
			c.log.Warn("remote cache session report dropped", "session", report.SessionID, "results", len(body.Results), "error", err)
			continue
		}
		c.log.Info("sent remote cache session report", "session", report.SessionID, "results", len(body.Results))
	}
}

func sessionReportBody(report *server.SessionReport) protocol.SessionReport {
	body := protocol.SessionReport{SessionID: report.SessionID, Results: make([]protocol.ReportedResult, 0, len(report.Results))}
	for _, entry := range report.Results {
		dependsOn := entry.DependsOn
		if dependsOn == nil {
			dependsOn = []uint64{}
		}
		body.Results = append(body.Results, protocol.ReportedResult{
			Result:       entry.ResultID,
			Type:         entry.Type,
			Field:        entry.Field,
			DependsOn:    dependsOn,
			Retained:     entry.Retained,
			Imported:     entry.Imported,
			RecipeDigest: entry.RecipeDigest.String(),
		})
	}
	return body
}

// serviceError is a non-2xx answer from the service.
type serviceError struct {
	status int
	body   protocol.ErrorResponse
}

func (e *serviceError) Error() string {
	if e.body.Error == "" {
		return fmt.Sprintf("remote cache service answered %d", e.status)
	}
	return fmt.Sprintf("remote cache service answered %d: %s", e.status, e.body.Error)
}

// do sends one JSON request to the service and decodes a JSON response into
// out when out is not nil. It returns the status and, for a non-2xx status,
// a serviceError. timeout bounds the whole request; zero means no bound
// beyond ctx.
func (c *client) do(ctx context.Context, timeout time.Duration, method, path string, in, out any) (int, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, timeout, fmt.Errorf("remote cache request %s %s: timed out after %s", method, path, timeout))
		defer cancel()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		serviceErr := &serviceError{status: resp.StatusCode}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if json.Unmarshal(raw, &serviceErr.body) != nil && len(raw) > 0 {
			serviceErr.body.Error = strings.TrimSpace(string(raw))
		}
		return resp.StatusCode, fmt.Errorf("%s %s: %w", method, path, serviceErr)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func (c *client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set(protocol.HeaderEngineInstance, c.instanceID)
}
