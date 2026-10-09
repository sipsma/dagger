package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"dagger.io/dagger/core"

	"dagger.io/dagger"
	"github.com/dagger/testctx"
)

// Give a clean shutdown time to write its persistence checkpoint under load.
// Each step gets this full bound, independent of earlier steps and of test
// cancellation; a failed step must not prevent the remaining cleanup.
const fixtureEngineStopTimeout = 2 * time.Minute

// closeClientBounded joins Close, which takes no context, under a fresh
// deadline. The buffered result lets the closer finish after a timeout.
func closeClientBounded(ctx context.Context, client *dagger.Client) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fixtureEngineStopTimeout)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			return fmt.Errorf("client close: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("client close did not return: %w", context.Cause(ctx))
	}
}

// stopNestedEngine attempts every shutdown step under its own deadline and
// reports each failed step by name. Services are forgotten only after a
// successful stop, so cleanup can retry; a client's Close is called only once.
func stopNestedEngine(ctx context.Context, client **dagger.Client, upstream, tunnel **core.Service) error {
	return shutDownNestedEngine(ctx, client, upstream, tunnel, false)
}

// discardNestedEngine is stopNestedEngine for an engine whose state is never
// read again: it kills the engine instead of waiting for a clean shutdown.
func discardNestedEngine(ctx context.Context, client **dagger.Client, upstream, tunnel **core.Service) error {
	return shutDownNestedEngine(ctx, client, upstream, tunnel, true)
}

func shutDownNestedEngine(ctx context.Context, client **dagger.Client, upstream, tunnel **core.Service, kill bool) error {
	step := func(name string, run func(context.Context) error) error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fixtureEngineStopTimeout)
		defer cancel()
		if err := run(ctx); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	var errs error
	if client != nil && *client != nil {
		c := *client
		*client = nil
		errs = errors.Join(errs, closeClientBounded(ctx, c))
	}
	if upstream != nil && *upstream != nil {
		svc := *upstream
		err := step("nested engine stop", func(ctx context.Context) error {
			_, err := svc.Stop(ctx, core.ServiceStopOpts{Kill: kill})
			return err
		})
		if err == nil {
			*upstream = nil
		}
		errs = errors.Join(errs, err)
	}
	if tunnel != nil && *tunnel != nil {
		svc := *tunnel
		err := step("nested tunnel stop", func(ctx context.Context) error {
			_, err := svc.Stop(ctx, core.ServiceStopOpts{Kill: true})
			return err
		})
		if err == nil {
			*tunnel = nil
		}
		errs = errors.Join(errs, err)
	}
	return errs
}

// dumpStuckNestedEngine logs the goroutines and processes of a nested engine
// whose graceful stop has been running for the given time.
func dumpStuckNestedEngine(t *testctx.T, debugURL string, after time.Duration) {
	client := &http.Client{Timeout: 20 * time.Second}
	for _, path := range []string{"/debug/pprof/goroutine?debug=2", "/debug/processes"} {
		var body []byte
		resp, err := client.Get(debugURL + path)
		if err == nil {
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		if err != nil {
			body = []byte(err.Error())
		}
		t.Logf("nested engine still stopping after %s; %s:\n%s", after, path, body)
	}
}
