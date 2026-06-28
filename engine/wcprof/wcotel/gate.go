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

// GateOptions is reserved for future soft thresholds. Under the rational root
// model the structural invariants are HARD — 0 by construction on faithful data —
// so there is nothing to tune; an empty value is the norm. (The former
// MaxFallbackAnchors tolerance was removed: an unschedulable op is an unfaithful
// EMIT to fix, never a quantity to tolerate.)
type GateOptions struct{}

// GateReport is the outcome of the structural gate.
type GateReport struct {
	OpCount     int
	RootCount   int
	OpenOps     int
	WaitEdges   int
	MakespanNS  int64
	TraceSpanNS int64

	// Hard invariants — over-serialization.
	ReplayErr      error
	Cycles         int
	SelfGtMakespan []*wcanalyze.Op
	IntervalGtSpan []*wcanalyze.Op

	// Hard invariants — wait-edge loss (under-serialization). A faithful
	// augmented trace must compile every wait edge or fail loudly; a lost wait
	// silently degrades a join into a fixed delay and drops counterfactual
	// propagation to the target class (design §6.1).
	UnresolvedWaitTargets int // non-lock waits with no resolvable target
	MalformedWaitTimings  int // waits with missing/unparseable timing
	OrphanedParents       int // ops whose recorded parent span is absent (capture loss)

	// Completeness checksum (design §6.1, leaf-drop detection). A dropped LEAF span
	// breaks no edge, so the signals above miss it; the producer declares its emitted
	// engine-span total — an EXACT count stamped once at session teardown on the
	// wcprof.session_complete carrier — and the loader reconciles. MissingSpans =
	// declared − received (> 0 ⇒ dropped spans ⇒ hard-fail). Because the declared total
	// is the exact upper bound, received > declared is impossible unless emit-side
	// quiescence regressed; that is ALSO hard-failed, as a distinct invariant violation
	// (defense-in-depth: it makes a future regression fail loud instead of re-opening
	// the masking window silently). SessionMarkerPresent is whether the declared total
	// was found; absent ⇒ unverifiable ⇒ hard-fail (fail-by-default — an
	// unstamped/pre-checksum trace, or one whose carrier dropped, is refused).
	MissingSpans         int
	SessionMarkerPresent bool
	DeclaredEngineSpans  int
	ReceivedEngineSpans  int

	// Dropped-link signal (otlpdump path only — Cloud cannot report it). On a
	// wait-carrying (augmented) trace any dropped link/link-attr is treated as
	// wait loss and fails; on an un-augmented trace (no wait edges) a dropped
	// non-wait link is benign and stays report-only, so un-augmented baselines
	// captured from a stock 128-cap engine never false-positive.
	TotalDroppedLinks       int
	TotalDroppedLinkAttrs   int
	WaitBearingDroppedLinks int // diagnostic: drops on a span that kept ≥1 wait
	WaitLinkDroppedAttrs    int // diagnostic: dropped attrs on surviving wait links

	// UnschedulableOps is a hard faithfulness signal (0 on faithful data): ops the
	// recorded causal structure cannot schedule — an inverted reference (an op
	// referenced before its ancestor spawns it) or a child its parent never spawns.
	// Under the rational root model this is 0 by construction; any non-zero count is
	// an unfaithful EMIT to fix at the choke point, never tolerated.
	UnschedulableOps int
	SkippedNoSpanID  int
	// SimStartConflicts counts anchored-start disagreements in the baseline
	// replay: the end-ordered gating model anchors a child identically whether
	// it is reached in order or out of order, so this should stay 0. A non-zero
	// count is a residual order-dependence (e.g. an in-flight fallback corner),
	// reported as a regression metric.
	SimStartConflicts int

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
		UnresolvedWaitTargets:   c.UnresolvedWaitTargets,
		MalformedWaitTimings:    c.MalformedWaitTimings,
		OrphanedParents:         c.OrphanedParents,
		TotalDroppedLinks:       c.TotalDroppedLinks,
		TotalDroppedLinkAttrs:   c.TotalDroppedLinkAttrs,
		WaitBearingDroppedLinks: c.WaitBearingDroppedLinks,
		WaitLinkDroppedAttrs:    c.WaitLinkDroppedAttrs,
		SkippedNoSpanID:         c.SkippedNoSpanID,
		MissingSpans:            c.MissingSpans,
		SessionMarkerPresent:    c.SessionMarkerPresent,
		DeclaredEngineSpans:     c.DeclaredEngineSpans,
		ReceivedEngineSpans:     c.ReceivedEngineSpans,
	}

	// Reuse the replay's own cycle/unschedulable signal (design §6.1).
	sim := wcanalyze.NewSimulation(g, nil)
	if _, err := sim.Run(); err != nil {
		r.ReplayErr = err
		r.violations = append(r.violations, fmt.Sprintf("replay failed: %v", err))
	}
	r.Cycles = sim.CycleWarnings
	r.UnschedulableOps = sim.UnschedulableOps
	r.SimStartConflicts = sim.SimStartConflicts

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
	if r.UnresolvedWaitTargets > 0 {
		r.violations = append(r.violations, fmt.Sprintf("%d non-lock wait(s) with an unresolved target span — Invariant T regression or a truncated/lost target; the join degrades to a fixed delay (design §3.0.1, §6.1)", r.UnresolvedWaitTargets))
	}
	if r.MalformedWaitTimings > 0 {
		r.violations = append(r.violations, fmt.Sprintf("%d wait(s) with missing/unparseable wcprof.wait.*_unix_ns timing — a malformed emit (design §3.0)", r.MalformedWaitTimings))
	}
	if r.OrphanedParents > 0 {
		r.violations = append(r.violations, fmt.Sprintf("%d op(s) with a recorded parent span ABSENT from the graph — the parent id is SET (the op had a parent), but its span is missing, surfacing the op as a false root and losing the saving that should cross its parent edge. The data is INCOMPLETE. This is distinct from a true independent root (empty parent) and from an emit-side parentless bug (the id is set). It is observed reproducibly on local otlpdump captures; the exact loss point — capture instrument, export pipeline, ingest, or an emit bug that set a bad id — is NOT yet pinned. Do not trust this ranking until the source is verified complete (design §6.1)", r.OrphanedParents))
	}
	// Dropped-link wait-loss: only meaningful when the trace carries wait edges.
	// This subsumes the surviving-wait predicate (a span that kept a wait but
	// dropped links) and also catches a span that lost ALL its waits or a
	// dropped link.purpose attribute — both invisible to that predicate.
	if r.WaitEdges > 0 && (r.TotalDroppedLinks > 0 || r.TotalDroppedLinkAttrs > 0) {
		r.violations = append(r.violations, fmt.Sprintf("%d dropped link(s) / %d dropped link-attr(s) on a wait-carrying trace — wait edges may have been silently evicted (raise LinkCountLimit, design §3.0)", r.TotalDroppedLinks, r.TotalDroppedLinkAttrs))
	}
	if r.UnschedulableOps > 0 {
		r.violations = append(r.violations, fmt.Sprintf("%d op(s) the recorded causal structure cannot schedule — an inverted reference (an op referenced before its ancestor spawns it) or a malformed nesting; both are impossible in a faithful synchronous nesting, so this is an unfaithful EMIT to fix at the choke point, never papered over (design §6.1)", r.UnschedulableOps))
	}
	// Completeness checksum (design §6.1, leaf-drop detection): a dropped LEAF span
	// breaks no edge, so everything above misses it. Refuse a trace whose engine span
	// count cannot be confirmed equal to what the producer declared — faithful data or
	// refuse, never a silently-wrong ranking.
	if !r.SessionMarkerPresent {
		r.violations = append(r.violations, "incomplete-or-unverifiable trace: no engine span-count declaration (the engine's wcprof.session_complete carrier span is absent) — a dropped LEAF span leaves no edge to catch, so completeness cannot be confirmed and the trace is refused. An old/unstamped capture, or one whose teardown carrier itself dropped, fails by default; re-capture from an engine that stamps the count (design §6.1)")
	} else if r.MissingSpans > 0 {
		r.violations = append(r.violations, fmt.Sprintf("incomplete trace: %d engine span(s) dropped (declared %d, received %d) — a dropped LEAF span breaks no edge and is invisible to the orphaned-parent / unresolved-wait signals, so this checksum is the only thing that catches it; the ranking would be silently wrong, so the trace is refused (design §6.1)", r.MissingSpans, r.DeclaredEngineSpans, r.ReceivedEngineSpans))
	} else if r.ReceivedEngineSpans > r.DeclaredEngineSpans {
		// Impossible under the invariant — the teardown count is the EXACT upper bound,
		// read after the trace's spans have quiesced. Receiving more means a counted
		// engine span was created after the final count was read, re-opening the window
		// where post-count padding could mask a concurrent drop. Fail loud rather than
		// silently trust a count that is no longer authoritative.
		r.violations = append(r.violations, fmt.Sprintf("completeness invariant violated: received %d > declared %d — the teardown count is meant to be the exact upper bound on engine spans, so receiving more means emit-side quiescence has regressed (a counted span was created after the final count was read); the trace is refused (design §6.1)", r.ReceivedEngineSpans, r.DeclaredEngineSpans))
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
	fmt.Fprintf(w, "  wait-loss: unresolved-targets=%d  malformed-timing=%d\n",
		r.UnresolvedWaitTargets, r.MalformedWaitTimings)
	fmt.Fprintf(w, "  capture-loss: orphaned-parents=%d (dropped parent spans → false roots)\n", r.OrphanedParents)
	fmt.Fprintf(w, "  completeness: missing-spans=%d (declared=%d received=%d marker=%v; hard-fail if missing>0, received>declared, or marker absent)\n",
		r.MissingSpans, r.DeclaredEngineSpans, r.ReceivedEngineSpans, r.SessionMarkerPresent)
	fmt.Fprintf(w, "  unschedulable-ops=%d (hard-fail if >0)  start-conflicts=%d\n", r.UnschedulableOps, r.SimStartConflicts)
	fmt.Fprintf(w, "  dropped-links: total=%d (%d attrs) wait-bearing=%d wait-link-attrs=%d\n",
		r.TotalDroppedLinks, r.TotalDroppedLinkAttrs, r.WaitBearingDroppedLinks, r.WaitLinkDroppedAttrs)
	if r.SkippedNoSpanID > 0 {
		fmt.Fprintf(w, "  skipped (no span id)=%d\n", r.SkippedNoSpanID)
	}
	for _, v := range r.violations {
		fmt.Fprintf(w, "  ! %s\n", v)
	}
}
