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
	var execGroups multiFlag
	flag.Var(&execGroups, "exec-group", "offline exec grouping rule '<match>=<label>' (repeatable; prefix the match with 'contains:' for a substring match)")
	var cachedDigests, cachedClasses, cachedExecs multiFlag
	flag.Var(&cachedDigests, "cached", "what-if-cached: recipe digest (dag.digest) to simulate as a cache hit, or '@file' manifest with one digest per line (repeatable)")
	flag.Var(&cachedClasses, "cached-class", "what-if-cached: cache every executed digest of this call class, e.g. 'Container.withExec' (repeatable)")
	flag.Var(&cachedExecs, "cached-exec", "what-if-cached: cache the digests owning user execs matching this argv pattern (boundary-aware prefix; 'contains:' for substring; repeatable)")
	allowPartial := flag.Bool("allow-partial-selection", false, "what-if-cached: proceed when a -cached-exec pattern's matches only partly resolve to owning call digests (the partial coverage is printed; without this flag it is an error)")
	cachedPull := flag.Duration("cached-pull-cost", 0, "what-if-cached: simulated cost of each hit (the pull-cost seam; 0 = local warm hit)")
	cachedFromRun := flag.String("cached-from-run", "", "what-if-cached calibration: path to a WARM run's otlpdump capture — simulate this (cold) trace under the warm run's actual hit set and report drift vs its actual makespan (exclusive with the other -cached* selectors; not supported with -trace)")
	var whyDigests, whyClasses, whyExecs multiFlag
	flag.Var(&whyDigests, "why-uncached", "cache-invalidation tracing: walk this uncached recipe digest (dag.digest) to its miss frontier and answer each origin's root cause (repeatable)")
	flag.Var(&whyClasses, "why-uncached-class", "cache-invalidation tracing: trace the uncached digests of this call class, e.g. 'Container.withExec' (repeatable; top digests by producing wall-clock, budget printed)")
	flag.Var(&whyExecs, "why-uncached-exec", "cache-invalidation tracing: trace the digests owning user execs matching this argv pattern (boundary-aware prefix; 'contains:' for substring; repeatable)")
	whyVs := flag.String("why-uncached-vs", "", "cache-invalidation tracing pair mode: path to a REFERENCE run's otlpdump capture — origins classify against it by digest identity (positional pairing is refused on OTel pairs pre-E3a, stated in the report; not supported with -trace)")
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
	whySel := wcanalyze.WhyUncachedSelection{
		Digests:      whyDigests,
		Classes:      whyClasses,
		ExecPatterns: whyExecs,
	}

	if *cachedFromRun != "" && *traceID != "" {
		fmt.Fprintln(os.Stderr, "-cached-from-run is not supported with -trace yet (capture the warm run locally)")
		os.Exit(2)
	}
	if *whyVs != "" && whySel.Empty() {
		fmt.Fprintln(os.Stderr, "-why-uncached-vs requires a -why-uncached* target selector")
		os.Exit(2)
	}
	if *whyVs != "" && *traceID != "" {
		fmt.Fprintln(os.Stderr, "-why-uncached-vs is not supported with -trace yet (capture the reference run locally)")
		os.Exit(2)
	}

	if *traceID != "" {
		err = runCloud(context.Background(), *traceID, *orgID, rules, sel, whySel, opts)
	} else {
		err = runFiles(flag.Args(), rules, sel, whySel, *whyVs, *cachedFromRun, opts)
	}
	if err != nil {
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

func runFiles(paths []string, rules []wcanalyze.ExecGroupRule, sel wcanalyze.CachedSelection, whySel wcanalyze.WhyUncachedSelection, whyVs string, cachedFromRun string, opts wcanalyze.ReportOptions) error {
	// The design's unit of analysis is one trace (design §10 decision 2), so each
	// file is loaded and analyzed independently rather than merged.
	var (
		failed bool
		warmG  *wcanalyze.Graph
		whyRef *wcanalyze.Graph
	)
	if whyVs != "" {
		// The why-uncached reference capture must itself pass the structural
		// gate: absence claims (category 4, the stable/absent split) are only
		// as good as the reference's completeness — refuse instead of
		// classifying against silently-incomplete history.
		refC, rg, err := loadFile(whyVs)
		if err != nil {
			return fmt.Errorf("reference capture %s: %w", whyVs, err)
		}
		refGate := wcotel.CheckStructural(refC, rg, wcotel.GateOptions{})
		if gerr := refGate.Err(); gerr != nil {
			fmt.Fprintf(os.Stderr, "reference capture %s:\n", whyVs)
			refGate.Write(os.Stderr)
			return fmt.Errorf("why-uncached reference capture failed the structural gate: %w", gerr)
		}
		whyRef = rg
	}
	if cachedFromRun != "" {
		// Calibration warm capture (design §3.5 gate 4). It must pass the
		// structural gate itself: an incomplete warm capture would silently
		// under-extract the hit set and skew the drift number — refuse instead.
		warmC, wg, err := loadFile(cachedFromRun)
		if err != nil {
			return fmt.Errorf("warm capture %s: %w", cachedFromRun, err)
		}
		warmGate := wcotel.CheckStructural(warmC, wg, wcotel.GateOptions{})
		if gerr := warmGate.Err(); gerr != nil {
			fmt.Fprintf(os.Stderr, "warm capture %s:\n", cachedFromRun)
			warmGate.Write(os.Stderr)
			return fmt.Errorf("warm capture failed the structural gate: %w", gerr)
		}
		warmG = wg
	}
	for _, path := range paths {
		if len(paths) > 1 {
			fmt.Printf("=== %s ===\n", path)
		}
		c, g, err := loadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		gateOK, werr := analyze(c, g, rules, sel, whySel, whyRef, opts)
		if werr != nil {
			// A report I/O, selector, or what-if-cached gate failure — each
			// carries its own explicit message; surface it as-is.
			return fmt.Errorf("%s: %w", path, werr)
		}
		if !gateOK {
			failed = true
		}
		if warmG != nil {
			cal, err := wcanalyze.RunCachedCalibration(g, warmG, sel.PullCostNS, opts.ChainDepth)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			cal.Write(os.Stdout)
			if gerr := cal.GateErr(); gerr != nil {
				return fmt.Errorf("%s: %w", path, gerr)
			}
		}
	}
	if failed {
		return fmt.Errorf("structural gate failed (see above)")
	}
	return nil
}

// runCloud swaps the loader's input to the Dagger Cloud trace API (design §5,
// §6.6): same compile/replay stage, different source.
func runCloud(ctx context.Context, traceID, orgID string, rules []wcanalyze.ExecGroupRule, sel wcanalyze.CachedSelection, whySel wcanalyze.WhyUncachedSelection, opts wcanalyze.ReportOptions) error {
	c, g, err := loadCloud(ctx, traceID, orgID)
	if err != nil {
		return err
	}
	gateOK, werr := analyze(c, g, rules, sel, whySel, nil, opts)
	if werr != nil {
		return werr // report I/O / selector / cached-gate failure, distinct from the structural gate
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
func analyze(c *wcotel.Compiled, g *wcanalyze.Graph, rules []wcanalyze.ExecGroupRule, sel wcanalyze.CachedSelection, whySel wcanalyze.WhyUncachedSelection, whyRef *wcanalyze.Graph, opts wcanalyze.ReportOptions) (gateOK bool, err error) {
	// Decompose user execs into per-command classes (applying any --exec-group
	// rules) BEFORE the structural gate compiles (and memoizes) the replay program,
	// so the gate, the class table, and the what-if savings all see the same
	// relabeled classes (design §4.4). The gate's verdict is class-independent, so
	// classifying first cannot change it.
	wcanalyze.ClassifyExecs(g, rules)
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
	// The explicit-set what-if-cached detail section (design §3.4 mode 2),
	// applied after ClassifyExecs like everything selector-shaped. A selector
	// or cached-gate failure returns as an error with its own explicit
	// message — a distinct failure mode from the structural gate above.
	if werr := wcanalyze.WriteCachedSelectionDetail(os.Stdout, g, sel, opts.ChainDepth); werr != nil {
		return gateOK, werr
	}
	// Cache-invalidation tracing (why-uncached mode): walk the selected
	// digests to their miss frontier. Refusals and price-gate violations
	// return as errors — the same distinct-failure-mode contract as the
	// cached detail section above. A structural-gate failure REFUSES the
	// walk outright (design §8: the analyzer refuses captures the existing
	// gates refuse — the walk's first-demand statuses and input edges are
	// exactly what an incomplete trace silently corrupts).
	if !whySel.Empty() && !gateOK {
		fmt.Fprintln(os.Stdout, "why-uncached REFUSED: this capture failed the structural gate (see above) — first-demand statuses and cache-input edges cannot be trusted on incomplete or unfaithful traces")
		return gateOK, nil
	}
	if whyRef != nil {
		if werr := wcanalyze.WriteWhyUncachedPair(os.Stdout, g, whyRef, whySel); werr != nil {
			return gateOK, werr
		}
		return gateOK, nil
	}
	if werr := wcanalyze.WriteWhyUncached(os.Stdout, g, whySel); werr != nil {
		return gateOK, werr
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
