// Spike5 measures transport, not compilation. Synthetic immutable archives
// have independently owned snapshots; all payload reads are included equally.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dagger/spike5/internal/dagger"
)

type Spike5 struct{}

var noNest = dagger.ContainerWithExecOpts{DisableDaggerInDagger: true}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (*Spike5) ReadFile(ctx context.Context, salt string, spike5Bind bool, spike5File *dagger.File) (string, error) {
	p := "/.spike5/file"
	if !spike5Bind {
		if _, err := spike5File.Export(ctx, p); err != nil {
			return "", err
		}
	}
	return hashFile(p)
}

func (*Spike5) ReadDir(ctx context.Context, salt string, spike5Bind bool, spike5Dir *dagger.Directory) (string, error) {
	p := "/.spike5/input"
	if !spike5Bind {
		if _, err := spike5Dir.Export(ctx, p); err != nil {
			return "", err
		}
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return "", err
	}
	var hashes []string
	for _, e := range entries {
		h, err := hashFile(filepath.Join(p, e.Name()))
		if err != nil {
			return "", err
		}
		hashes = append(hashes, h)
	}
	return strings.Join(hashes, ":"), nil
}

func (*Spike5) Emit(ctx context.Context, salt string, size int, spike5Output bool) (*dagger.File, error) {
	name := "result"
	if spike5Output {
		name = "/.spike5/output/result"
	}
	b := make([]byte, size)
	copy(b, salt)
	if err := os.WriteFile(name, b, 0644); err != nil {
		return nil, err
	}
	value := `"__spike5_sealed_file__"`
	if !spike5Output {
		id, err := dag.CurrentModule().WorkdirFile(name).ID(ctx)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(id)
		if err != nil {
			return nil, err
		}
		value = string(encoded)
	}
	if err := dag.CurrentFunctionCall().ReturnValue(ctx, dagger.JSON(value)); err != nil {
		return nil, err
	}
	// Both arms acknowledge then exit, so skipped SDK cleanup is not a saving
	// attributed to sealing. A real SDK must carry the token through dispatch.
	os.Exit(0)
	return nil, fmt.Errorf("unreachable")
}

// SealProof tests the lifecycle: the final write happens AFTER ReturnValue's
// acknowledgement. Invalid output and process failure must not publish a file.
func (*Spike5) SealProof(ctx context.Context, salt string, spike5Output bool, kind string) (*dagger.File, error) {
	if err := dag.CurrentFunctionCall().ReturnValue(ctx, dagger.JSON(`"__spike5_sealed_file__"`)); err != nil {
		return nil, err
	}
	p := "/.spike5/output/result"
	switch kind {
	case "ok":
		if err := os.WriteFile(p, []byte("after-return:"+salt), 0644); err != nil {
			panic(err)
		}
	case "symlink":
		if err := os.Symlink("/etc/passwd", p); err != nil {
			panic(err)
		}
	case "fail":
		os.Exit(23)
	case "missing":
	default:
		panic("bad proof")
	}
	os.Exit(0)
	return nil, nil
}

func (*Spike5) Readonly(ctx context.Context, spike5Bind bool, spike5Dir *dagger.Directory, spike5File *dagger.File) (bool, error) {
	for _, p := range []string{"/.spike5/input/new", "/.spike5/input/p0000.a", "/.spike5/file"} {
		if err := os.WriteFile(p, []byte("bad"), 0644); err == nil {
			return false, fmt.Errorf("writable: %s", p)
		}
	}
	return true, nil
}

// Each archive is copied ONCE during preparation to an independent snapshot.
// Per-consumer withFiles then copies these; the view only binds them.
func prepare(ctx context.Context, count, size int) ([]*dagger.File, *dagger.Directory, error) {
	seed := dag.Container().From("alpine:3.20").WithExec([]string{"sh", "-ec", fmt.Sprintf("mkdir /seed; head -c %d /dev/zero > /seed/archive", size)}, noNest).File("/seed/archive")
	files := make([]*dagger.File, count)
	err := parallel(count, 16, func(i int) error {
		name := fmt.Sprintf("p%04d.a", i)
		d, err := dag.Directory().WithFile(name, seed).Sync(ctx)
		if err != nil {
			return err
		}
		f, err := d.File(name).Sync(ctx)
		files[i] = f
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	dir, err := dag.Directory().WithFiles("/", files).Sync(ctx)
	return files, dir, err
}

func parallel(n, c int, fn func(int) error) error {
	sem := make(chan struct{}, c)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) { defer wg.Done(); defer func() { <-sem }(); errs[i] = fn(i) }(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func view(ctr *dagger.Container, files []*dagger.File, dir string) *dagger.Container {
	paths := make([]string, len(files))
	for i := range paths {
		paths[i] = fmt.Sprintf("%s/p%04d.a", dir, i)
	}
	return ctr.WithSpike5FileView(files, paths)
}

// Check runs only correctness checks. It emits no timing results.
func (m *Spike5) Check(ctx context.Context, salt string) (string, error) {
	files, dir, err := prepare(ctx, 5, 1024)
	if err != nil {
		return "", err
	}
	a, err := dag.Spike5().ReadDir(ctx, salt, false, dir)
	if err != nil {
		return "", err
	}
	b, err := dag.Spike5().ReadDir(ctx, salt, true, dir)
	if err != nil || a != b {
		return "", fmt.Errorf("direct directory mismatch: %v", err)
	}
	a, err = dag.Spike5().ReadFile(ctx, salt, false, files[0])
	if err != nil {
		return "", err
	}
	b, err = dag.Spike5().ReadFile(ctx, salt, true, files[0])
	if err != nil || a != b {
		return "", fmt.Errorf("direct file mismatch: %v", err)
	}
	if _, err := dag.Spike5().Readonly(ctx, true, dir, files[0]); err != nil {
		return "", err
	}
	changed := dag.Directory().WithNewFile("changed", "different").File("changed")
	c, err := dag.Spike5().ReadFile(ctx, salt, true, changed)
	if err != nil || c == b {
		return "", fmt.Errorf("changed input not observed: %v", err)
	}
	for _, sealed := range []bool{false, true} {
		f := dag.Spike5().Emit(salt, 1048576, sealed)
		content, err := f.Contents(ctx)
		if err != nil || len(content) != 1048576 || !strings.HasPrefix(content, salt) {
			return "", fmt.Errorf("emit mismatch: %v", err)
		}
	}
	for _, kind := range []string{"ok", "symlink", "missing", "fail"} {
		got, err := dag.Spike5().SealProof(salt, true, kind).Contents(ctx)
		if kind == "ok" {
			if err != nil || got != "after-return:"+salt {
				return "", fmt.Errorf("post-exit capture: %v", err)
			}
		} else if err == nil {
			return "", fmt.Errorf("accepted %s output", kind)
		}
	}
	base := dag.Container().From("alpine:3.20")
	for _, count := range []int{5, 30, 260, 1400} {
		fs, _, err := prepare(ctx, count, 1024)
		if err != nil {
			return "", err
		}
		p := "/.spike5/view/check"
		cmd := []string{"sh", "-ec", fmt.Sprintf("test $(find %s -type f | wc -l) = %d; cat %s/* | sha256sum", p, count, p)}
		a, err := view(base, fs, p).WithExec(cmd, noNest).Stdout(ctx)
		if err != nil {
			return "", err
		}
		b, err := base.WithMountedDirectory("/.spike5/view", dag.Directory().WithFiles("check", fs), dagger.ContainerWithMountedDirectoryOpts{ReadOnly: true}).WithExec(cmd, noNest).Stdout(ctx)
		if err != nil || a != b {
			return "", fmt.Errorf("view %d mismatch: %v", count, err)
		}
	}
	if _, err := view(base, files, "/.spike5/view/ro").WithExec([]string{"sh", "-ec", "! echo bad > /.spike5/view/ro/p0000.a"}, noNest).Sync(ctx); err != nil {
		return "", err
	}
	if _, err := base.WithSpike5FileView(files[:2], []string{"/.spike5/view/x", "/.spike5/view/x"}).Sync(ctx); err == nil {
		return "", fmt.Errorf("duplicate accepted")
	}
	if _, err := base.WithSpike5FileView(files[:1], []string{"/etc/passwd"}).Sync(ctx); err == nil {
		return "", fmt.Errorf("invalid path accepted")
	}
	if _, err := m.Run(ctx, "sealed", 4, 2, 1, 1024, salt+"-fan", false); err != nil {
		return "", err
	}
	return "PASS: independent snapshots; 5/30/260/1400 views; equal bytes; read-only inputs; changed input; 1MiB outputs; post-ReturnValue write; reject failed/missing/symlink outputs; fan-out", nil
}

type report struct {
	Sample    string  `json:"sample"`
	Kind      string  `json:"kind"`
	N         int     `json:"n"`
	C         int     `json:"concurrency"`
	Count     int     `json:"archives"`
	Size      int     `json:"bytes_per_archive"`
	WallMS    float64 `json:"wall_ms,omitempty"`
	MeanMS    float64 `json:"mean_ms,omitempty"`
	P50MS     float64 `json:"p50_ms,omitempty"`
	P95MS     float64 `json:"p95_ms,omitempty"`
	PrepareMS float64 `json:"prepare_ms,omitempty"`
	OK        bool    `json:"ok"`
}

// Run includes construction, evaluation, publication and consumer reads.
// Preparation is separate; functional mode suppresses all elapsed times.
func (*Spike5) Run(ctx context.Context, kind string, n, c, count, size int, salt string, timing bool) (string, error) {
	if n < 1 || c < 1 || count < 1 || size < 1 {
		return "", fmt.Errorf("positive sizes/counts required")
	}
	if c > n {
		c = n
	}
	t := time.Now()
	files, dir, err := prepare(ctx, count, size)
	if err != nil {
		return "", err
	}
	base, err := dag.Container().From("alpine:3.20").Sync(ctx)
	if err != nil {
		return "", err
	}
	prep := time.Since(t)
	durs := make([]time.Duration, n)
	t = time.Now()
	err = parallel(n, c, func(i int) error {
		start := time.Now()
		defer func() { durs[i] = time.Since(start) }()
		key := fmt.Sprintf("%s-%d", salt, i)
		switch kind {
		case "view", "withfiles":
			p := "/.spike5/view/" + key
			ctr := base
			if kind == "view" {
				ctr = view(ctr, files, p)
			} else {
				ctr = ctr.WithMountedDirectory("/.spike5/view", dag.Directory().WithFiles(key, files), dagger.ContainerWithMountedDirectoryOpts{ReadOnly: true})
			}
			_, err := ctr.WithExec([]string{"sh", "-ec", "cat " + p + "/* | sha256sum"}, noNest).Stdout(ctx)
			return err
		case "export-dir", "bind-dir":
			_, err := dag.Spike5().ReadDir(ctx, key, kind == "bind-dir", dir)
			return err
		case "export-file", "bind-file":
			_, err := dag.Spike5().ReadFile(ctx, key, kind == "bind-file", files[0])
			return err
		case "workdir", "sealed":
			_, err := dag.Spike5().Emit(key, size, kind == "sealed").Sync(ctx)
			return err
		case "exec-two", "exec-one":
			ctr := base.WithEnvVariable("SALT", key).WithExec([]string{"sh", "-ec", fmt.Sprintf("mkdir /out; head -c %d /dev/zero > /out/result", size)}, noNest)
			if kind == "exec-one" {
				_, err := ctr.File("/out/result").Sync(ctx)
				return err
			}
			d, err := ctr.Directory("/out").Sync(ctx)
			if err != nil {
				return err
			}
			_, err = d.File("result").Sync(ctx)
			return err
		default:
			return fmt.Errorf("unknown kind %s", kind)
		}
	})
	if err != nil {
		return "", err
	}
	r := report{Sample: salt, Kind: kind, N: n, C: c, Count: count, Size: size, OK: true}
	if timing {
		r.WallMS = float64(time.Since(t).Microseconds()) / 1000
		r.PrepareMS = float64(prep.Microseconds()) / 1000
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		for _, d := range durs {
			r.MeanMS += float64(d.Microseconds()) / 1000 / float64(n)
		}
		r.P50MS = float64(durs[n/2].Microseconds()) / 1000
		r.P95MS = float64(durs[(n-1)*95/100].Microseconds()) / 1000
	}
	b, err := json.Marshal(r)
	return string(b), err
}
