// wcprof-otel-analyze reads an otlpdump JSONL capture of a dagger run's OTel
// telemetry and reports wall-clock bottleneck analysis through the same
// wcanalyze replay the native wcprof dump path uses — the "OTel source" of the
// wcprof × OTel design (hack/designs/wcprof-otel-design.md §5).
//
// It runs the structural gate (design §6.1) first and exits non-zero if a hard
// invariant is violated, then renders the report. On an un-augmented engine the
// report is deliberately wrong (the four faithfulness breaks of design §2 are
// all present) but the gate still passes — that baseline is the measuring stick
// for the emit-side fixes in later chunks.
//
// Usage:
//
//	go run ./hack/otlpdump -out /tmp/telemetry.jsonl    # capture (telemetry-capture skill)
//	go run ./cmd/wcprof-otel-analyze /tmp/telemetry.jsonl
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
)

func main() {
	var (
		topClasses   = flag.Int("top", 30, "number of classes to show in rankings")
		factorsStr   = flag.String("factors", "0,0.5,0.9", "comma-separated self-time scaling factors for what-if simulation")
		minSelf      = flag.Duration("min-self", time.Millisecond, "ignore classes with less total self-time than this in what-ifs")
		deadAirMin   = flag.Duration("dead-air-min", 50*time.Millisecond, "minimum gap to report as dead air")
		chainDepth   = flag.Int("chain-depth", 25, "max length of the blocking chain to print")
		maxFallbacks = flag.Int("max-fallback-anchors", 0, "fail the structural gate above this many fallback anchors (0 = report-only)")
	)
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: wcprof-otel-analyze [flags] <otlpdump.jsonl> [more.jsonl...]\n")
		fmt.Fprintf(os.Stderr, "each file is one captured trace and is analyzed independently\n")
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

	opts := wcanalyze.ReportOptions{
		TopClasses:     *topClasses,
		WhatIfFactors:  factors,
		MinClassSelfNS: int64(*minSelf),
		DeadAirMinNS:   int64(*deadAirMin),
		ChainDepth:     *chainDepth,
	}
	if err := run(flag.Args(), opts, wcotel.GateOptions{MaxFallbackAnchors: *maxFallbacks}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(paths []string, opts wcanalyze.ReportOptions, gateOpts wcotel.GateOptions) error {
	// The design's unit of analysis is one trace (design §10 decision 2), so
	// each file is loaded and analyzed independently rather than merged.
	var failed bool
	for _, path := range paths {
		if len(paths) > 1 {
			fmt.Printf("=== %s ===\n", path)
		}
		c, g, err := loadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		gate := wcotel.CheckStructural(c, g, gateOpts)
		gate.Write(os.Stderr)
		if err := gate.Err(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed = true
		}

		if err := wcanalyze.WriteReport(os.Stdout, g, opts); err != nil {
			return err
		}
	}
	if failed {
		return fmt.Errorf("structural gate failed (see above)")
	}
	return nil
}

func loadFile(path string) (*wcotel.Compiled, *wcanalyze.Graph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return wcotel.Load(f)
}
