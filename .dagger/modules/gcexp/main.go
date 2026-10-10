// Experiments for the fine-grained Go build caching research.
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"dagger/gcexp/internal/dagger"
)

type Gcexp struct{}

func base() *dagger.Container {
	return dag.Container().From("alpine:3.21")
}

// Fanout runs n independent execs in parallel and reads each output file.
// It reports the wall time inside the function.
func (m *Gcexp) Fanout(ctx context.Context, n int, salt string, nonce string) (string, error) {
	start := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = base().
				WithExec([]string{"sh", "-c", fmt.Sprintf("echo %d-%s > /o", i, salt)}).
				File("/o").Contents(ctx)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("fanout n=%d: %s", n, time.Since(start).Round(time.Millisecond)), nil
}

// Chain runs n execs in sequence; each one mounts the previous output file.
// Only the final file is read, so the engine sees one dependent chain.
func (m *Gcexp) Chain(ctx context.Context, n int, salt string, nonce string) (string, error) {
	start := time.Now()
	f := dag.Directory().WithNewFile("seed", salt).File("seed")
	for i := 0; i < n; i++ {
		f = base().
			WithMountedFile("/in", f).
			WithExec([]string{"sh", "-c", fmt.Sprintf("cat /in > /o; echo %d >> /o", i)}).
			File("/o")
	}
	if _, err := f.Contents(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("chain n=%d: %s", n, time.Since(start).Round(time.Millisecond)), nil
}

// Cutoff checks whether a consumer exec is reused when its input file comes
// from a different producer recipe that wrote identical bytes.
func (m *Gcexp) Cutoff(ctx context.Context, salt string) (string, error) {
	producer := func(v string) *dagger.Container {
		return base().
			WithEnvVariable("V", v).
			WithExec([]string{"sh", "-c", "mkdir -p /out; echo same-" + salt + " > /out/x"})
	}
	consumer := func(f *dagger.File) (string, error) {
		out, err := base().
			WithEnvVariable("SALT", salt).
			WithMountedFile("/in", f).
			WithExec([]string{"sh", "-c", "cat /in >/dev/null; head -c 8 /dev/urandom | od -An -tx1 > /stamp"}).
			File("/stamp").Contents(ctx)
		return strings.TrimSpace(out), err
	}
	var lines []string

	// Mode 1: Container.file on the exec output.
	s1, err := consumer(producer("v1-ctrfile").File("/out/x"))
	if err != nil {
		return "", err
	}
	s2, err := consumer(producer("v2-ctrfile").File("/out/x"))
	if err != nil {
		return "", err
	}
	lines = append(lines, fmt.Sprintf("container.file:            reused=%v (%s vs %s)", s1 == s2, s1, s2))

	// Mode 2: Directory.file on an already-evaluated (synced) output directory.
	synced := func(v string) (*dagger.File, error) {
		d, err := producer(v).Directory("/out").Sync(ctx)
		if err != nil {
			return nil, err
		}
		return d.File("x"), nil
	}
	f1, err := synced("v1-synced")
	if err != nil {
		return "", err
	}
	s3, err := consumer(f1)
	if err != nil {
		return "", err
	}
	f2, err := synced("v2-synced")
	if err != nil {
		return "", err
	}
	s4, err := consumer(f2)
	if err != nil {
		return "", err
	}
	lines = append(lines, fmt.Sprintf("synced directory.file:     reused=%v (%s vs %s)", s3 == s4, s3, s4))
	return strings.Join(lines, "\n"), nil
}

// MountScale runs one exec with n mounted files, each a distinct small file.
func (m *Gcexp) MountScale(ctx context.Context, n int, salt string, nonce string) (string, error) {
	start := time.Now()
	ctr := base().WithEnvVariable("SALT", salt)
	for i := 0; i < n; i++ {
		f := dag.Directory().WithNewFile("f", fmt.Sprintf("%s-%d", salt, i)).File("f")
		ctr = ctr.WithMountedFile(fmt.Sprintf("/m/%d", i), f)
	}
	out, err := ctr.WithExec([]string{"sh", "-c", "ls /m | wc -l"}).Stdout(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("mounts n=%d: %s (%s)", n, time.Since(start).Round(time.Millisecond), strings.TrimSpace(out)), nil
}

// GoChain is Chain on the golang image instead of alpine.
func (m *Gcexp) GoChain(ctx context.Context, n int, salt string, nonce string) (string, error) {
	start := time.Now()
	f := dag.Directory().WithNewFile("seed", salt).File("seed")
	for i := 0; i < n; i++ {
		f = dag.Container().From("golang:1.26").
			WithMountedFile("/in", f).
			WithExec([]string{"sh", "-c", fmt.Sprintf("cat /in > /o; echo %d >> /o", i)}).
			File("/o")
	}
	if _, err := f.Contents(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("go chain n=%d: %s", n, time.Since(start).Round(time.Millisecond)), nil
}

// DirScale gathers n distinct files with one withFiles call and mounts the
// resulting directory once.
func (m *Gcexp) DirScale(ctx context.Context, n int, salt string, nonce string) (string, error) {
	start := time.Now()
	files := make([]*dagger.File, n)
	for i := 0; i < n; i++ {
		files[i] = dag.Directory().WithNewFile(fmt.Sprintf("f%d", i), fmt.Sprintf("%s-%d", salt, i)).File(fmt.Sprintf("f%d", i))
	}
	dir := dag.Directory().WithFiles(".", files)
	out, err := base().WithEnvVariable("SALT", salt).WithMountedDirectory("/m", dir).
		WithExec([]string{"sh", "-c", "ls /m | wc -l"}).Stdout(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("withFiles n=%d: %s (%s)", n, time.Since(start).Round(time.Millisecond), strings.TrimSpace(out)), nil
}

// LazyCutoff checks early cutoff when nothing is synced between producer and
// consumer. Each case builds a consumer over a file from a producer recipe
// that differs from the reference's but writes identical bytes, and reports
// whether the consumer's exec was reused.
func (m *Gcexp) LazyCutoff(ctx context.Context, salt string) (string, error) {
	producer := func(v string) *dagger.Container {
		return base().
			WithEnvVariable("V", v).
			WithExec([]string{"sh", "-c", "mkdir -p /out; echo same-" + salt + " > /out/x"})
	}
	consumer := func(f *dagger.File) (string, error) {
		out, err := base().
			WithEnvVariable("SALT", salt).
			WithMountedFile("/in", f).
			WithExec([]string{"sh", "-c", "cat /in >/dev/null; head -c 8 /dev/urandom | od -An -tx1 > /stamp"}).
			File("/stamp").Contents(ctx)
		return strings.TrimSpace(out), err
	}
	var lines []string
	// Reference: the barrier pattern (directory synced, then the file).
	d, err := producer("ref").Directory("/out").Sync(ctx)
	if err != nil {
		return "", err
	}
	ref, err := consumer(d.File("x"))
	if err != nil {
		return "", err
	}
	// Case 1: the file is selected from an unevaluated directory and the
	// consumer is evaluated directly.
	s1, err := consumer(producer("lazy1").Directory("/out").File("x"))
	if err != nil {
		return "", err
	}
	lines = append(lines, fmt.Sprintf("lazy file, consumer forced:          reused=%v", s1 == ref))
	// Case 2: the file is selected lazily, then its producer's directory is
	// synced separately before the consumer is evaluated.
	p2 := producer("lazy2")
	f2 := p2.Directory("/out").File("x")
	if _, err := p2.Directory("/out").Sync(ctx); err != nil {
		return "", err
	}
	s2, err := consumer(f2)
	if err != nil {
		return "", err
	}
	lines = append(lines, fmt.Sprintf("lazy file, producer synced first:    reused=%v", s2 == ref))
	// Case 3: the same, but the file itself is synced before the consumer.
	p3 := producer("lazy3")
	f3, err := p3.Directory("/out").File("x").Sync(ctx)
	if err != nil {
		return "", err
	}
	s3, err := consumer(f3)
	if err != nil {
		return "", err
	}
	lines = append(lines, fmt.Sprintf("lazy file, file synced first:        reused=%v", s3 == ref))
	// Case 4: the barrier pattern with a different recipe (control: expected reused).
	d4, err := producer("barrier4").Directory("/out").Sync(ctx)
	if err != nil {
		return "", err
	}
	s4, err := consumer(d4.File("x"))
	if err != nil {
		return "", err
	}
	lines = append(lines, fmt.Sprintf("barrier (dir synced, then file):     reused=%v", s4 == ref))
	return strings.Join(lines, "\n"), nil
}
