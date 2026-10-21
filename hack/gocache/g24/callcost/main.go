// Callcost measures what one uncached call costs on the engine: a Go module
// self-call, the same with eight arguments, a self-call returning a file it
// wrote (through currentModule.workdirFile), and plain container execs. Every
// call gets a fresh salt, so every call is a cache miss.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"dagger/callcost/internal/dagger"
)

type Callcost struct{}

const base = "alpine:3.20"

var noNest = dagger.ContainerWithExecOpts{DisableDaggerInDagger: true}

// Noop returns its salt.
func (m *Callcost) Noop(salt string) string {
	return salt
}

// NoopArgs takes eight arguments, to show what each argument adds to a call.
func (m *Callcost) NoopArgs(salt, a1, a2, a3, a4, a5, a6, a7 string) string {
	return salt
}

// Emit writes size random bytes in the function's scratch directory and returns
// them through currentModule.workdirFile, which copies them out.
func (m *Callcost) Emit(salt string, size int) (*dagger.File, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	name := "out-" + salt
	if err := os.WriteFile(name, b, 0o644); err != nil {
		return nil, err
	}
	return dag.CurrentModule().WorkdirFile(name), nil
}

// Run makes n calls of one kind, at most c at a time (c = 1 is a serial
// chain), and returns one summary line. Kinds: selfcall, selfcall-args,
// selfcall-file, exec, exec-nest, exec-file. size is the file size for the
// -file kinds.
func (m *Callcost) Run(ctx context.Context, kind string, n int, c int, salt string, size int) (string, error) {
	one, err := caller(kind, size)
	if err != nil {
		return "", err
	}
	if n < 1 || c < 1 {
		return "", fmt.Errorf("n and c must be positive")
	}
	durs := make([]time.Duration, n)
	errs := make([]error, n)
	sem := make(chan struct{}, c)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range n {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			t := time.Now()
			errs[i] = one(ctx, fmt.Sprintf("%s-%d", salt, i))
			durs[i] = time.Since(t)
		}()
	}
	wg.Wait()
	wall := time.Since(start)
	for _, err := range errs {
		if err != nil {
			return "", err
		}
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	var sum time.Duration
	for _, d := range durs {
		sum += d
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	return fmt.Sprintf("kind=%s n=%d c=%d size=%d wall_ms=%.1f p50_ms=%.2f p90_ms=%.2f max_ms=%.2f mean_ms=%.2f",
		kind, n, c, size, ms(wall), ms(durs[n/2]), ms(durs[n*9/10]), ms(durs[n-1]), ms(sum/time.Duration(n))), nil
}

func caller(kind string, size int) (func(context.Context, string) error, error) {
	switch kind {
	case "selfcall":
		return func(ctx context.Context, s string) error {
			_, err := dag.Callcost().Noop(ctx, s)
			return err
		}, nil
	case "selfcall-args":
		return func(ctx context.Context, s string) error {
			_, err := dag.Callcost().NoopArgs(ctx, s, "a1", "a2", "a3", "a4", "a5", "a6", "a7")
			return err
		}, nil
	case "selfcall-file":
		return func(ctx context.Context, s string) error {
			_, err := dag.Callcost().Emit(s, size).Sync(ctx)
			return err
		}, nil
	case "exec":
		return func(ctx context.Context, s string) error {
			_, err := dag.Container().From(base).WithEnvVariable("SALT", s).
				WithExec([]string{"true"}, noNest).Sync(ctx)
			return err
		}, nil
	case "exec-nest":
		// Dagger-in-Dagger on, the default for withExec and how module calls run.
		return func(ctx context.Context, s string) error {
			_, err := dag.Container().From(base).WithEnvVariable("SALT", s).
				WithExec([]string{"true"}).Sync(ctx)
			return err
		}, nil
	case "exec-file":
		// The per-package module's pattern: evaluate the output directory,
		// then select the file from it and sync that.
		return func(ctx context.Context, s string) error {
			out, err := dag.Container().From(base).WithEnvVariable("SALT", s).
				WithExec([]string{"sh", "-c", fmt.Sprintf("mkdir -p /out && head -c %d /dev/urandom > /out/f", size)}, noNest).
				Directory("/out").Sync(ctx)
			if err != nil {
				return err
			}
			_, err = out.File("f").Sync(ctx)
			return err
		}, nil
	}
	return nil, fmt.Errorf("unknown kind %q", kind)
}
