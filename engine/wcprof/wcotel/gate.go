package wcotel

import (
	"fmt"
	"io"
	"strings"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// The structural gate (design §6.1) is the cheap, always-on check that runs
// after every Build on the OTel path and fails loudly when the loaded graph is
// impossible. It does not tune anything: on an un-augmented baseline it is
// expected to pass even though the *shape* is deliberately wrong (joiner waits
// counted as self-time, lazy work under the producer) — what it forbids is a
// graph that could only come from an unfaithful emit: a cycle, an op whose
// self-time exceeds the whole run, or (on the otlpdump path) silently dropped
// wait links. Such a failure is always an emit-side bug to fix there, never a
// thing to paper over in the loader (design §5, §6.3).

// GateOptions tunes the gate's soft thresholds.
type GateOptions struct {
	// MaxFallbackAnchors, when > 0, makes the gate fail if the baseline replay
	// fallback-anchors more ops than this. It defaults to report-only (0): the
	// un-augmented baseline legitimately fallback-anchors lazy work re-pointed
	// under its producer, so the count is a regression metric, not a hard
	// invariant, until later chunks make the shape faithful (design §6.1).
	MaxFallbackAnchors int
}

// GateReport is the outcome of the structural gate.
type GateReport struct {
	OpCount     int
	RootCount   int
	OpenOps     int
	WaitEdges   int
	MakespanNS  int64
	TraceSpanNS int64

	// Hard invariants.
	ReplayErr      error
	Cycles         int
	SelfGtMakespan []*wcanalyze.Op
	IntervalGtSpan []*wcanalyze.Op
	// Dropped-link invariants (otlpdump path; design §6.1).
	WaitBearingDroppedLinks int
	WaitLinkDroppedAttrs    int

	// Soft / regression metrics.
	FallbackAnchors      int
	FallbackBound        int // MaxFallbackAnchors, echoed; 0 = report-only
	TotalDroppedLinks    int
	MalformedWaitTimings int

	violations []string
}

// CheckStructural runs the §6.1 invariants over a freshly built graph and its
// loader provenance.
func CheckStructural(c *Compiled, g *wcanalyze.Graph, opts GateOptions) GateReport {
	r := GateReport{
		OpCount:                 len(g.Ops),
		RootCount:               len(g.Roots),
		OpenOps:                 g.OpenOps,
		WaitEdges:               c.WaitEdgeCount,
		MakespanNS:              wcanalyze.ActualMakespanNS(g),
		TraceSpanNS:             g.TraceEndNS - g.TraceStartNS,
		WaitBearingDroppedLinks: c.WaitBearingDroppedLinks,
		WaitLinkDroppedAttrs:    c.WaitLinkDroppedAttrs,
		FallbackBound:           opts.MaxFallbackAnchors,
		TotalDroppedLinks:       c.TotalDroppedLinks,
		MalformedWaitTimings:    c.MalformedWaitTimings,
	}

	// Reuse the replay's own cycle/fallback signal (design §6.1).
	sim := wcanalyze.NewSimulation(g, nil)
	if _, err := sim.Run(); err != nil {
		r.ReplayErr = err
		r.violations = append(r.violations, fmt.Sprintf("replay failed: %v", err))
	}
	r.Cycles = sim.CycleWarnings
	r.FallbackAnchors = sim.FallbackAnchors

	for _, op := range g.Ops {
		if op.SelfNS() > r.MakespanNS {
			r.SelfGtMakespan = append(r.SelfGtMakespan, op)
		}
		if op.Duration() > r.TraceSpanNS {
			r.IntervalGtSpan = append(r.IntervalGtSpan, op)
		}
	}

	if r.Cycles > 0 {
		r.violations = append(r.violations, fmt.Sprintf("%d wait/join cycle(s) in the loaded graph — an unfaithful emit (design §2.5)", r.Cycles))
	}
	if n := len(r.SelfGtMakespan); n > 0 {
		r.violations = append(r.violations, fmt.Sprintf("%d op(s) with self-time > makespan (e.g. a joiner mis-typed as self-time, design §2.2)", n))
	}
	if n := len(r.IntervalGtSpan); n > 0 {
		r.violations = append(r.violations, fmt.Sprintf("%d op(s) with interval > trace span (e.g. a service-availability span leaking self-time, design §3.4)", n))
	}
	if r.WaitBearingDroppedLinks > 0 {
		r.violations = append(r.violations, fmt.Sprintf("%d dropped link(s) on wait-bearing spans — wait edges were silently evicted (raise LinkCountLimit, design §3.0)", r.WaitBearingDroppedLinks))
	}
	if r.WaitLinkDroppedAttrs > 0 {
		r.violations = append(r.violations, fmt.Sprintf("%d dropped attribute(s) on wait links — a wait edge lost its timing/target", r.WaitLinkDroppedAttrs))
	}
	if opts.MaxFallbackAnchors > 0 && r.FallbackAnchors > opts.MaxFallbackAnchors {
		r.violations = append(r.violations, fmt.Sprintf("fallback anchors %d exceed bound %d", r.FallbackAnchors, opts.MaxFallbackAnchors))
	}

	return r
}

// Err returns a non-nil error iff a hard structural invariant was violated.
func (r GateReport) Err() error {
	if len(r.violations) == 0 {
		return nil
	}
	return fmt.Errorf("wcprof OTel structural gate failed:\n  - %s", strings.Join(r.violations, "\n  - "))
}

// Write renders a one-block human summary of the gate result.
func (r GateReport) Write(w io.Writer) {
	status := "PASS"
	if r.Err() != nil {
		status = "FAIL"
	}
	fmt.Fprintf(w, "structural gate: %s\n", status)
	fmt.Fprintf(w, "  ops=%d roots=%d open=%d wait-edges=%d\n", r.OpCount, r.RootCount, r.OpenOps, r.WaitEdges)
	fmt.Fprintf(w, "  cycles=%d  self>makespan=%d  interval>tracespan=%d\n",
		r.Cycles, len(r.SelfGtMakespan), len(r.IntervalGtSpan))
	bound := "report-only"
	if r.FallbackBound > 0 {
		bound = fmt.Sprintf("bound %d", r.FallbackBound)
	}
	fmt.Fprintf(w, "  fallback-anchors=%d (%s)\n", r.FallbackAnchors, bound)
	fmt.Fprintf(w, "  dropped-links: total=%d wait-bearing=%d wait-link-attrs=%d  malformed-waits=%d\n",
		r.TotalDroppedLinks, r.WaitBearingDroppedLinks, r.WaitLinkDroppedAttrs, r.MalformedWaitTimings)
	for _, v := range r.violations {
		fmt.Fprintf(w, "  ! %s\n", v)
	}
}
