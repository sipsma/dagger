// wcprof-analyze reads a wcprof dump (from the engine's /debug/wcprof/dump
// endpoint) and reports wall-clock bottleneck analysis: per-class self time,
// counterfactual what-if rankings, blocking chains, and dead air.
//
// Usage:
//
//	curl -s http://localhost:6060/debug/wcprof/dump > /tmp/wcprof.dump
//	go run ./cmd/wcprof-analyze /tmp/wcprof.dump
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

func main() {
	var (
		topClasses = flag.Int("top", 30, "number of classes to show in rankings")
		factorsStr = flag.String("factors", "0,0.5,0.9", "comma-separated self-time scaling factors for what-if simulation")
		minSelf    = flag.Duration("min-self", time.Millisecond, "ignore classes with less total self-time than this in what-ifs")
		deadAirMin = flag.Duration("dead-air-min", 50*time.Millisecond, "minimum gap to report as dead air")
		chainDepth = flag.Int("chain-depth", 25, "max length of the blocking chain to print")
	)
	var execGroups multiFlag
	flag.Var(&execGroups, "exec-group", "offline exec grouping rule '<match>=<label>' (repeatable; prefix the match with 'contains:' for a substring match)")
	var cachedDigests, cachedClasses, cachedExecs multiFlag
	flag.Var(&cachedDigests, "cached", "what-if-cached: recipe digest to simulate as a cache hit, or '@file' manifest with one digest per line (repeatable)")
	flag.Var(&cachedClasses, "cached-class", "what-if-cached: cache every executed digest of this call class, e.g. 'Container.withExec' (repeatable)")
	flag.Var(&cachedExecs, "cached-exec", "what-if-cached: cache the digests owning user execs matching this argv pattern (boundary-aware prefix; 'contains:' for substring; repeatable)")
	allowPartial := flag.Bool("allow-partial-selection", false, "what-if-cached: proceed when a -cached-exec pattern's matches only partly resolve to owning call digests (the partial coverage is printed; without this flag it is an error)")
	cachedPull := flag.Duration("cached-pull-cost", 0, "what-if-cached: simulated cost of each hit (the pull-cost seam; 0 = local warm hit)")
	cachedFromRun := flag.String("cached-from-run", "", "what-if-cached calibration: path to a WARM run's wcprof dump — simulate this (cold) run under the warm run's actual hit set and report drift vs its actual makespan (exclusive with the other -cached* selectors)")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: wcprof-analyze [flags] <dump-file> [more-dump-files...]\n")
		fmt.Fprintf(os.Stderr, "multiple dumps from periodic drains of the same engine run are merged\n")
		flag.PrintDefaults()
		os.Exit(2)
	}

	var factors []float64
	for _, part := range strings.Split(*factorsStr, ",") {
		f, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid factor %q: %v\n", part, err)
			os.Exit(2)
		}
		factors = append(factors, f)
	}

	rules, err := wcanalyze.ParseExecGroupRules(execGroups)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	// Exclusivity is a flag-shape check: test it on the RAW selectors, before
	// manifest expansion can fail with a misleading error.
	if *cachedFromRun != "" && len(cachedDigests)+len(cachedClasses)+len(cachedExecs) > 0 {
		fmt.Fprintln(os.Stderr, "-cached-from-run is exclusive with the other -cached* selectors")
		os.Exit(2)
	}
	digests, err := wcanalyze.ExpandCachedArgs(cachedDigests)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	sel := wcanalyze.CachedSelection{
		Digests:               digests,
		Classes:               cachedClasses,
		ExecPatterns:          cachedExecs,
		AllowPartialSelection: *allowPartial,
		PullCostNS:            int64(*cachedPull),
	}

	if err := run(flag.Args(), rules, sel, *cachedFromRun, wcanalyze.ReportOptions{
		TopClasses:     *topClasses,
		WhatIfFactors:  factors,
		MinClassSelfNS: int64(*minSelf),
		DeadAirMinNS:   int64(*deadAirMin),
		ChainDepth:     *chainDepth,
	}); err != nil {
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

func run(paths []string, rules []wcanalyze.ExecGroupRule, sel wcanalyze.CachedSelection, cachedFromRun string, opts wcanalyze.ReportOptions) error {
	readers := make([]io.Reader, 0, len(paths))
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		readers = append(readers, f)
	}

	graph, err := wcanalyze.LoadMulti(readers)
	if err != nil {
		return fmt.Errorf("load dumps: %w", err)
	}
	// Decompose user execs into per-command classes (applying any --exec-group
	// rules) BEFORE the report's first simulation compiles (and memoizes) the replay
	// program, so the class table and the what-if savings agree (design §4.4).
	wcanalyze.ClassifyExecs(graph, rules)
	if err := wcanalyze.WriteReport(os.Stdout, graph, opts); err != nil {
		return err
	}
	if cachedFromRun != "" {
		// Cold/warm calibration (design §3.5 gate 4): simulate THIS run under
		// the warm capture's actual hit set and report drift vs its actual
		// makespan.
		wf, err := os.Open(cachedFromRun)
		if err != nil {
			return fmt.Errorf("open warm capture: %w", err)
		}
		defer wf.Close()
		warmG, err := wcanalyze.Load(wf)
		if err != nil {
			return fmt.Errorf("load warm capture: %w", err)
		}
		cal, err := wcanalyze.RunCachedCalibration(graph, warmG, sel.PullCostNS, opts.ChainDepth)
		if err != nil {
			return err
		}
		cal.Write(os.Stdout)
		return cal.Detail.GateErr()
	}
	// The explicit-set what-if-cached detail section (design §3.4 mode 2); a
	// gate violation surfaces as a non-zero exit, distinct from report I/O.
	return wcanalyze.WriteCachedSelectionDetail(os.Stdout, graph, sel, opts.ChainDepth)
}
