// wcprof-oracle is the cross-source oracle CLI (design §6.2): it compiles a
// native wcprof dump and an OTel otlpdump JSONL capture — both from the SAME
// augmented dev-engine run — to the wcprof IR and compares their RunWhatIfs
// bottleneck rankings. Native is ground truth; the OTel source is faithful iff
// its top-N bottleneck classes match within tolerance.
//
// Oracle runbook (impl-plan §1): on one dev-engine run with a single workload,
// enable BOTH sources — native wcprof (_DAGGER_WCPROF=1 / --profile) and the
// OTel augmentation (always on) — then:
//
//	# capture OTel
//	go run ./hack/otlpdump -out /tmp/otel.jsonl &
//	env OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:43180 OTEL_EXPORTER_OTLP_TRACES_LIVE=1 \
//	    ./hack/with-dev ./bin/dagger <singleflight-heavy workload>
//	# dump native (same run)
//	curl -s localhost:6060/debug/wcprof/dump > /tmp/native.dump
//	# compare
//	go run ./cmd/wcprof-oracle -native /tmp/native.dump -otel /tmp/otel.jsonl
//
// The two sources MUST be from the same run to be comparable (impl-plan §4.6).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
)

func main() {
	var (
		nativePath = flag.String("native", "", "native wcprof dump (curl localhost:6060/debug/wcprof/dump)")
		otelPath   = flag.String("otel", "", "OTel otlpdump JSONL capture of the same run")
		factor     = flag.Float64("factor", 0, "self-time scaling factor for the what-if comparison (0 = remove the class entirely)")
		topN       = flag.Int("top", 15, "compare the top-N bottleneck classes of each source")
		minSelfMS  = flag.Int64("min-self-ms", 1, "ignore classes with less total self-time than this (ms)")
		minJaccard = flag.Float64("min-jaccard", 0.8, "minimum top-N class overlap to call the sources converged")
		maxDrift   = flag.Float64("max-rel-drift", 0.1, "maximum per-class relative savings drift to call the sources converged")
	)
	var execGroups multiFlag
	flag.Var(&execGroups, "exec-group", "offline exec grouping rule '<match>=<label>' applied to BOTH sources (repeatable; prefix the match with 'contains:' for a substring match)")
	flag.Parse()

	if *nativePath == "" || *otelPath == "" {
		fmt.Fprintln(os.Stderr, "usage: wcprof-oracle -native <dump> -otel <otlpdump.jsonl> [flags]")
		flag.PrintDefaults()
		os.Exit(2)
	}

	rules, err := wcanalyze.ParseExecGroupRules(execGroups)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := run(*nativePath, *otelPath, rules, *factor, *topN, *minSelfMS*1_000_000, *minJaccard, *maxDrift); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// multiFlag collects a repeatable string flag, preserving flag order.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ", ") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func run(nativePath, otelPath string, rules []wcanalyze.ExecGroupRule, factor float64, topN int, minSelfNS int64, minJaccard, maxDrift float64) error {
	nf, err := os.Open(nativePath)
	if err != nil {
		return err
	}
	defer nf.Close()
	nativeG, err := wcotel.LoadNativeDump(nf)
	if err != nil {
		return fmt.Errorf("load native dump: %w", err)
	}

	of, err := os.Open(otelPath)
	if err != nil {
		return err
	}
	defer of.Close()
	otelC, otelG, err := wcotel.Load(of)
	if err != nil {
		return fmt.Errorf("load otel capture: %w", err)
	}

	// Decompose user execs on BOTH graphs with IDENTICAL rules, before the gate and
	// the comparison, so the cross-source oracle compares like-for-like per-command
	// classes (the same argv → the same ClassKey on both sources, design §1.5, §4.4).
	wcanalyze.ClassifyExecs(nativeG, rules)
	wcanalyze.ClassifyExecs(otelG, rules)

	// Run the structural gate on the OTel source first: an oracle comparison on a
	// structurally-broken trace is meaningless (design §6).
	gate := wcotel.CheckStructural(otelC, otelG, wcotel.GateOptions{})
	gate.Write(os.Stderr)
	if err := gate.Err(); err != nil {
		return fmt.Errorf("otel structural gate failed (oracle not meaningful): %w", err)
	}

	cmp, err := wcotel.Oracle(nativeG, otelG, factor, topN, minSelfNS)
	if err != nil {
		return err
	}
	cmp.Write(os.Stdout)

	if !cmp.Agrees(minJaccard, maxDrift) {
		return fmt.Errorf("sources DIVERGE: jaccard=%.2f (want >=%.2f), max-rel-drift=%.2f (want <=%.2f)",
			cmp.JaccardTopN(), minJaccard, cmp.MaxRelDrift(), maxDrift)
	}
	fmt.Printf("\nCONVERGED: jaccard=%.2f (>=%.2f), max-rel-drift=%.2f (<=%.2f)\n",
		cmp.JaccardTopN(), minJaccard, cmp.MaxRelDrift(), maxDrift)
	return nil
}
