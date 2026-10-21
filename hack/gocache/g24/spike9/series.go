package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type runResult struct {
	Project, Arm, Phase, Mode string
	Round                     int
	WallNS                    *int64 `json:",omitempty"`
	AnalysisNS                *int64 `json:",omitempty"`
	OutputBytes               int64
	Analysis                  *analysis
}
type runConfig struct {
	Project, Mode, Order, Materialization    string
	Rounds, Parallel, Workers, BudgetMinutes int
	Source, Out, State, Modules              string
}

func configEnv(cache, tmp, modules string, parallel int) []string {
	env := os.Environ()
	env = replaceEnv(env, "GOCACHE", cache)
	env = replaceEnv(env, "GOTMPDIR", tmp)
	env = replaceEnv(env, "TMPDIR", tmp)
	env = replaceEnv(env, "GOMODCACHE", modules)
	env = replaceEnv(env, "GOPATH", "/spike9-gopath")
	env = replaceEnv(env, "GOTOOLCHAIN", "local")
	env = replaceEnv(env, "GOFLAGS", "")
	env = replaceEnv(env, "GOENV", "off")
	env = replaceEnv(env, "GOCACHEPROG", "")
	env = replaceEnv(env, "GODEBUG", "")
	env = replaceEnv(env, "CGO_ENABLED", "0")
	env = replaceEnv(env, "GOMAXPROCS", fmt.Sprint(parallel))
	return env
}
func replaceEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, s := range env {
		if !strings.HasPrefix(s, key+"=") {
			out = append(out, s)
		}
	}
	return append(out, key+"="+value)
}
func prepareMain(args []string) error {
	f := flag.NewFlagSet("prepare", flag.ContinueOnError)
	src := f.String("source", "/seed", "source")
	modules := f.String("modules", "/modules", "download destination")
	out := f.String("out", "/prepared", "metadata directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	for _, d := range []string{*modules, *out, "/prepare-tmp", "/prepare-cache"} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	cmd := exec.Command("go", "mod", "download")
	cmd.Dir = *src
	cmd.Env = configEnv("/prepare-cache", "/prepare-tmp", *modules, 4)
	log, err := os.Create(filepath.Join(*out, "download.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	for _, name := range []string{"cmd/go/internal/cacheprog/cacheprog.go", "cmd/go/internal/cache/prog.go", "cmd/go/internal/cache/hash.go", "cmd/go/internal/work/action.go", "cmd/go/internal/work/buildid.go", "cmd/go/internal/work/exec.go", "cmd/internal/buildid/buildid.go"} {
		dst := filepath.Join(*out, "toolchain-source", name)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		// Preserve the source of the actual container toolchain for auditing
		// version-specific protocol/graph assumptions.
		if _, err := copyFile(filepath.Join(runtime.GOROOT(), "src", name), dst); err != nil {
			return err
		}
	}
	return nil
}
func seriesMain(args []string) error {
	f := flag.NewFlagSet("series", flag.ContinueOnError)
	c := runConfig{}
	f.StringVar(&c.Project, "project", "yq", "yq or dagger")
	f.StringVar(&c.Mode, "mode", "functional", "functional (no wall-time results) or timing")
	f.StringVar(&c.Order, "order", "", "comma-separated arms; default alternating plain/engine orders")
	f.StringVar(&c.Materialization, "materialization", "copy", "copy (File.export into the running client)")
	f.IntVar(&c.Rounds, "rounds", 1, "complete rounds")
	f.IntVar(&c.Parallel, "parallel", 4, "Go build -p and GOMAXPROCS")
	f.IntVar(&c.Workers, "workers", 64, "helper concurrent-request limit")
	f.IntVar(&c.BudgetMinutes, "budget-minutes", 25, "deadline for this project's series")
	f.StringVar(&c.Source, "source", "/seed", "prepared source")
	f.StringVar(&c.Out, "out", "/out", "exported results")
	f.StringVar(&c.State, "state", "/spike9-state", "private disposable state")
	f.StringVar(&c.Modules, "modules", "/modules", "prepared downloads")
	if err := f.Parse(args); err != nil {
		return err
	}
	if c.Project != "yq" && c.Project != "dagger" {
		return fmt.Errorf("unknown project")
	}
	if c.Mode != "functional" && c.Mode != "timing" {
		return fmt.Errorf("unknown mode")
	}
	if c.Rounds < 1 || c.Parallel < 1 || c.Workers < 1 || c.BudgetMinutes < 1 {
		return fmt.Errorf("invalid counts")
	}
	if c.Mode == "functional" {
		if c.Rounds != 1 || c.Order != "" {
			return fmt.Errorf("functional mode uses one plain/engine sequence")
		}
		c.Order = "plain,engine"
	}
	if c.Materialization != "copy" {
		return fmt.Errorf("invalid materialization")
	}
	// Never destroy a supplied path. Each invocation requires fresh owned roots.
	if _, err := os.Stat(c.State); !os.IsNotExist(err) {
		return fmt.Errorf("state must not exist: %s", c.State)
	}
	if err := os.MkdirAll(c.State, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(c.Out, 0755); err != nil {
		return err
	}
	meta := map[string]any{"config": c, "harness_nonce": os.Getenv("SPIKE9_NONCE"), "go_runtime": runtime.Version(), "num_cpu": runtime.NumCPU(),
		"image": os.Getenv("SPIKE9_IMAGE"), "source_commit": os.Getenv("SPIKE9_SOURCE_COMMIT"), "harness_commit": os.Getenv("SPIKE9_COMMIT"),
		"cold_policy": "fresh GOCACHE and versioned engine namespace per arm; no target compilation in preparation; downloads and helper compilation excluded",
		"functional":  "elapsed values are suppressed; helper telemetry is for protocol validation only"}
	envCmd := exec.Command("go", "env", "-json", "GOVERSION", "GOOS", "GOARCH", "GOTOOLCHAIN")
	envCmd.Env = configEnv("/prepare-cache", "/prepare-tmp", c.Modules, c.Parallel)
	if b, err := envCmd.Output(); err == nil {
		meta["go_env"] = json.RawMessage(b)
	} else {
		return err
	}
	if err := writeJSON(filepath.Join(c.Out, "meta.json"), meta); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.BudgetMinutes)*time.Minute)
	defer cancel()
	orders := [][]string{{"plain", "engine"}, {"engine", "plain"}}
	report, err := os.Create(filepath.Join(c.Out, "results.jsonl"))
	if err != nil {
		return err
	}
	defer report.Close()
	summary, err := os.Create(filepath.Join(c.Out, "summary.txt"))
	if err != nil {
		return err
	}
	defer summary.Close()
	w := io.MultiWriter(os.Stdout, summary)
	fmt.Fprintf(w, "mode=%s project=%s rounds=%d materialization=%s parallel=%d; local functional data is not timing evidence\n", c.Mode, c.Project, c.Rounds, c.Materialization, c.Parallel)
	for round := 0; round < c.Rounds; round++ {
		arms := orders[round%len(orders)]
		if c.Order != "" {
			arms = strings.Split(c.Order, ",")
		}
		for _, arm := range arms {
			delay, ok := map[string]string{"plain": "", "engine": "0ms"}[arm]
			if !ok {
				return fmt.Errorf("unknown arm %q", arm)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			armRoot := filepath.Join(c.State, fmt.Sprintf("r%d-%s", round+1, arm))
			if err := os.MkdirAll(armRoot, 0755); err != nil {
				return err
			}
			// The source path is identical in all arms. Copies and edits are
			// outside measured build brackets; only our owned workspace is reset.
			work := filepath.Join(c.State, "workspace")
			if err := os.RemoveAll(work); err != nil {
				return err
			}
			if err := os.CopyFS(work, os.DirFS(c.Source)); err != nil {
				return err
			}
			cache := filepath.Join(armRoot, "gocache")
			store := filepath.Join(armRoot, "store")
			tmp := filepath.Join(armRoot, "tmp")
			for _, d := range []string{cache, store, tmp} {
				if err := os.MkdirAll(d, 0755); err != nil {
					return err
				}
			}
			for _, phase := range []string{"first", "edit", "noop"} {
				if phase == "edit" {
					if err := applyEdit(c.Project, work); err != nil {
						return err
					}
				}
				dir := filepath.Join(c.Out, fmt.Sprintf("r%d-%s-%s", round+1, arm, phase))
				if err := os.MkdirAll(dir, 0755); err != nil {
					return err
				}
				pkg := "."
				if c.Project == "dagger" {
					pkg = "./cmd/dagger"
				}
				bin := filepath.Join(armRoot, "binary")
				cmd := exec.CommandContext(ctx, "go", "build", "-p", fmt.Sprint(c.Parallel), "-mod=readonly", "-debug-actiongraph="+filepath.Join(dir, "graph.json"), "-o", bin, pkg)
				cmd.Dir = work
				cmd.Env = configEnv(cache, tmp, c.Modules, c.Parallel)
				// Builds are offline after preparation: no network differences
				// should get silently folded into a cache latency comparison.
				cmd.Env = replaceEnv(cmd.Env, "GOPROXY", "off")
				if arm != "plain" {
					local := filepath.Join(armRoot, "local-"+phase)
					cmd.Env = replaceEnv(cmd.Env, "GOCACHEPROG", fmt.Sprintf("/spike9 helper -store %s -local %s -log %s -latency %s -workers %d -materialization %s -namespace %s -toolchain %s",
						store, local, filepath.Join(dir, "requests.jsonl"), delay, c.Workers, c.Materialization, fmt.Sprintf("gocacheprog-go1.26-v1/%s/%s/r%d", os.Getenv("SPIKE9_NONCE"), c.Project, round+1), os.Getenv("SPIKE9_IMAGE")))
				}
				log, err := os.Create(filepath.Join(dir, "build.log"))
				if err != nil {
					return err
				}
				cmd.Stdout, cmd.Stderr = log, log
				start := time.Now()
				runErr := cmd.Run()
				elapsed := time.Since(start).Nanoseconds()
				log.Close()
				if runErr != nil {
					return fmt.Errorf("%s r%d %s %s: %w (see build.log)", c.Project, round+1, arm, phase, runErr)
				}
				st, err := os.Stat(bin)
				if err != nil {
					return err
				}
				analysisStart := time.Now()
				a, err := analyze(dir)
				analysisNS := time.Since(analysisStart).Nanoseconds()
				if err != nil {
					return err
				}
				if err := checkAnalysis(a, phase, arm); err != nil {
					return err
				}
				result := runResult{Project: c.Project, Arm: arm, Phase: phase, Mode: c.Mode, Round: round + 1, OutputBytes: st.Size(), Analysis: a}
				if c.Mode == "timing" {
					result.WallNS = &elapsed
					result.AnalysisNS = &analysisNS
				}
				if err := json.NewEncoder(report).Encode(result); err != nil {
					return err
				}
				timing := "elapsed=suppressed"
				if c.Mode == "timing" {
					timing = fmt.Sprintf("wall_s=%.6f", float64(elapsed)/1e9)
				}
				gets, puts := a.ByKind["get"], a.ByKind["put"]
				if gets == nil {
					gets = &kindStats{}
				}
				if puts == nil {
					puts = &kindStats{}
				}
				fmt.Fprintf(w, "r%d %s %s %s gets=%d hits=%d misses=%d puts=%d body_bytes=%d materialized_bytes=%d mapped=%d/%d direct=%d subkey=%d commands=%d\n",
					round+1, arm, phase, timing, gets.Count, gets.Hits, gets.Misses, puts.Count, puts.BodyBytes, gets.MaterializedBytes+puts.MaterializedBytes,
					a.Visibility.DirectUnique+a.Visibility.SubkeyUnique, a.Visibility.CacheRequests, a.Visibility.DirectUnique, a.Visibility.SubkeyUnique, a.Visibility.ActionsWithCommands)
				if arm != "plain" {
					if err := os.RemoveAll(filepath.Join(armRoot, "local-"+phase)); err != nil {
						return err
					}
				}
			}
			// Verify that an edited output starts; excluded from build timing.
			b := []byte("dagger CLI compiled; not executed (only the authorized installed CLI is invoked)\n")
			if c.Project == "yq" {
				check := exec.CommandContext(ctx, filepath.Join(armRoot, "binary"), "--version")
				var err error
				b, err = check.CombinedOutput()
				if err != nil {
					return fmt.Errorf("built binary smoke check: %w: %s", err, b)
				}
			}
			if err := os.WriteFile(filepath.Join(c.Out, fmt.Sprintf("r%d-%s-binary-check.txt", round+1, arm)), b, 0644); err != nil {
				return err
			}
			if err := os.Rename(filepath.Join(armRoot, "binary"), filepath.Join(c.Out, "binary")); err != nil {
				return err
			}
			// This directory holds scratch only. Entries persist in Dagger results
			// across phases and remain subject to normal engine GC.
			if err := os.RemoveAll(armRoot); err != nil {
				return err
			}
		}
	}
	return os.WriteFile(filepath.Join(c.Out, "PASS"), []byte("all builds, protocol checks and binary smoke checks passed\n"), 0644)
}
func applyEdit(project, work string) error {
	file, old, new := "pkg/yqlib/operator_add.go", "unable to parse duration [%v]: %w", "unable to parse the duration [%v]: %w"
	if project == "dagger" {
		file = "internal/cloud/auth/auth.go"
		old = "could not acquire lock on %s: timed out"
		new = "could not acquire the lock on %s: timed out"
	}
	path := filepath.Join(work, file)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Count(string(b), old) != 1 {
		return fmt.Errorf("expected exactly one item-4 edit match in %s", file)
	}
	// Literal equivalent of g23 inner-realpp.sh's sed; avoids regexp drift.
	return os.WriteFile(path, []byte(strings.Replace(string(b), old, new, 1)), 0644)
}
