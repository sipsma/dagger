// wcprof-otel-analyze reports wall-clock bottleneck analysis for a dagger run's
// OTel telemetry through the same wcanalyze replay the native wcprof dump path
// uses — the "OTel source" of the wcprof × OTel design
// (hack/designs/wcprof-otel-design.md §5).
//
// It ingests from either source (design §5): a local otlpdump JSONL capture (the
// dev loop) OR, with -trace, a trace fetched from the Dagger Cloud trace API (the
// production ingest, §6.6). Both feed the identical compile/replay stage. It runs
// the structural gate (design §6.1) first and exits non-zero if a hard invariant
// is violated, then renders the report.
//
// Usage:
//
//	go run ./hack/otlpdump -out /tmp/telemetry.jsonl    # capture (telemetry-capture skill)
//	go run ./cmd/wcprof-otel-analyze /tmp/telemetry.jsonl
//	go run ./cmd/wcprof-otel-analyze -trace <traceID>   # from Dagger Cloud (requires `dagger login`)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/engine/wcprof/wccloud"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
	"github.com/dagger/dagger/internal/cloud"
	"github.com/dagger/dagger/internal/cloud/auth"
)

func main() {
	var (
		topClasses = flag.Int("top", 30, "number of classes to show in rankings")
		factorsStr = flag.String("factors", "0,0.5,0.9", "comma-separated self-time scaling factors for what-if simulation")
		minSelf    = flag.Duration("min-self", time.Millisecond, "ignore classes with less total self-time than this in what-ifs")
		deadAirMin = flag.Duration("dead-air-min", 50*time.Millisecond, "minimum gap to report as dead air")
		chainDepth = flag.Int("chain-depth", 25, "max length of the blocking chain to print")
		traceID    = flag.String("trace", "", "fetch this trace id from Dagger Cloud instead of reading a file (requires `dagger login`)")
		orgID      = flag.String("org", "", "Dagger Cloud org id for -trace (default: the current logged-in org)")
	)
	flag.Parse()

	if *traceID == "" && flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "usage: wcprof-otel-analyze [flags] <otlpdump.jsonl> [more.jsonl...]\n")
		fmt.Fprintf(os.Stderr, "   or: wcprof-otel-analyze [flags] -trace <traceID>\n")
		fmt.Fprintf(os.Stderr, "each file/trace is one trace and is analyzed independently\n")
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

	var err error
	if *traceID != "" {
		err = runCloud(context.Background(), *traceID, *orgID, opts)
	} else {
		err = runFiles(flag.Args(), opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runFiles(paths []string, opts wcanalyze.ReportOptions) error {
	// The design's unit of analysis is one trace (design §10 decision 2), so each
	// file is loaded and analyzed independently rather than merged.
	var failed bool
	for _, path := range paths {
		if len(paths) > 1 {
			fmt.Printf("=== %s ===\n", path)
		}
		c, g, err := loadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		gateOK, werr := analyze(c, g, opts)
		if werr != nil {
			// A report I/O failure is distinct from a gate failure; surface it as-is.
			return fmt.Errorf("%s: %w", path, werr)
		}
		if !gateOK {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("structural gate failed (see above)")
	}
	return nil
}

// runCloud swaps the loader's input to the Dagger Cloud trace API (design §5,
// §6.6): same compile/replay stage, different source.
func runCloud(ctx context.Context, traceID, orgID string, opts wcanalyze.ReportOptions) error {
	c, g, err := loadCloud(ctx, traceID, orgID)
	if err != nil {
		return err
	}
	gateOK, werr := analyze(c, g, opts)
	if werr != nil {
		return werr // report I/O failure, distinct from a gate failure
	}
	if !gateOK {
		return fmt.Errorf("structural gate failed (see above)")
	}
	return nil
}

// analyze runs the structural gate (design §6.1) then renders the report, keeping
// the two failure modes DISTINCT: gateOK=false means the trace violated a hard
// invariant (unfaithful or incomplete data — the ranking is refused, the whole
// point of the gate); a non-nil error is a report I/O failure (the gate verdict is
// still valid and was already printed). A caller must not report a write error as a
// gate failure.
func analyze(c *wcotel.Compiled, g *wcanalyze.Graph, opts wcanalyze.ReportOptions) (gateOK bool, err error) {
	gate := wcotel.CheckStructural(c, g, wcotel.GateOptions{})
	gate.Write(os.Stderr)
	gateOK = true
	if gerr := gate.Err(); gerr != nil {
		fmt.Fprintln(os.Stderr, gerr)
		gateOK = false
	}
	if werr := wcanalyze.WriteReport(os.Stdout, g, opts); werr != nil {
		return gateOK, fmt.Errorf("write report: %w", werr)
	}
	return gateOK, nil
}

func loadFile(path string) (*wcotel.Compiled, *wcanalyze.Graph, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return wcotel.Load(f)
}

func loadCloud(ctx context.Context, traceID, orgID string) (*wcotel.Compiled, *wcanalyze.Graph, error) {
	cloudAuth, err := auth.GetCloudAuth(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("cloud auth (run `dagger login`): %w", err)
	}
	client, err := cloud.NewClient(ctx, cloudAuth)
	if err != nil {
		return nil, nil, fmt.Errorf("cloud client: %w", err)
	}
	if orgID == "" {
		if org, orgErr := auth.CurrentOrg(); orgErr == nil {
			orgID = org.ID
		}
	}
	if orgID == "" {
		return nil, nil, fmt.Errorf("no org id: pass -org or set a current org via `dagger login`")
	}
	return wccloud.Load(ctx, client, orgID, traceID)
}
