// Package wcotel compiles the engine's OTel telemetry (the traces that flow to
// Dagger Cloud) into the same wcprof IR the native recorder produces, so the
// unchanged wcanalyze replay can rank wall-clock bottlenecks from a trace.
//
// It is the "OTel source" half of the wcprof × OTel design
// (hack/designs/wcprof-otel-design.md). The loader does *only* mechanical
// translation — zero causal inference (design §5): spans become ops, the
// engine's explicit wait-edge links (design §3.0) become wait events, and the
// causal parent of an op is the engine-emitted wcprof.parent override if
// present, else the span's parentId (design §1.1, Invariant E). Any cycle or
// impossible structure in the loaded graph is a bug in the *emit* side, made
// loud by the structural gate (gate.go, design §6.1) — never papered over here.
//
// Chunk 1 reads the dev-loop front-end: otlpdump JSONL (the telemetry-capture
// skill). The production front-end (the Dagger Cloud trace API) is a later
// chunk; it produces the same neutral Span values and reuses Compile unchanged.
package wcotel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/dagql/call/callpbv1"
	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// Span is the neutral, front-end-agnostic representation of one OTel span the
// loader compiles. The otlpdump JSONL front-end (this file) and the future
// Cloud trace-API front-end both produce these; Compile consumes them.
type Span struct {
	// TraceID is the lower-hex OTel trace id (32 hex chars). The loader's unit
	// of analysis is one trace (design §10 decision 2); Compile rejects a span
	// set spanning more than one trace.
	TraceID string
	// SpanID and ParentID are lower-hex OTel ids (16 hex chars). An empty or
	// all-zero ParentID means the span is a trace root.
	SpanID   string
	ParentID string
	Name     string
	// StartUnixNS/EndUnixNS are absolute Unix nanoseconds. EndUnixNS == 0 marks
	// a span exported on start but not yet on end (in-flight / live-only).
	StartUnixNS uint64
	EndUnixNS   uint64
	Attrs       map[string]any
	// StatusError is true when the span's OTel status code is ERROR.
	StatusError bool
	Links       []Link
	// DroppedLinks is the OTLP Span.DroppedLinksCount: links the SDK evicted
	// because the span exceeded its LinkCountLimit. Surfaced by the structural
	// gate (design §6.1) because dropped wait links silently under-serialize.
	DroppedLinks int
}

// Link is one OTel span link on a Span.
type Link struct {
	// SpanID is the link target's span id (lower-hex).
	SpanID string
	Attrs  map[string]any
	// DroppedAttrs is the OTLP per-link DroppedAttributesCount.
	DroppedAttrs int
}

// Compiled is the loader's output: the wcprof IR (ready for wcanalyze.Build)
// plus the provenance the structural gate needs but the Graph does not carry
// (dropped-link counts, malformed-wait counts).
type Compiled struct {
	Header *wcprof.DumpHeader
	Events []wcprof.DumpEvent

	SpanCount       int
	OpenSpanCount   int
	WaitEdgeCount   int
	ForcedEdgeCount int
	// SuppressedIdentDerivations counts the emit-side ident-suppression
	// links, one per firing — an exact tally (also carried in the
	// synthesized Header, where the shared what-if-cached admission gate
	// refuses on nonzero; a lost mark shows as a dropped link, gated).
	SuppressedIdentDerivations int
	// SkippedNoSpanID counts records dropped in dedup for lacking a span id
	// (unmappable; surfaced so the skip is never silent).
	SkippedNoSpanID int

	// Dropped-count provenance (otlpdump path; design §6.1). The Cloud path
	// cannot report these, so they are engineered out via LinkCountLimit there.
	TotalDroppedLinks       int
	TotalDroppedLinkAttrs   int
	WaitBearingDroppedLinks int // dropped links on spans that carry ≥1 wait link
	WaitLinkDroppedAttrs    int // dropped attributes on wait links specifically

	// MalformedDagCalls counts spans whose dag.call attribute failed to
	// decode (base64/proto). The scope implicit inputs for such a call are
	// left unrecorded (Op.ScopeInputs nil — "scope not recorded", the same
	// honest degradation as their absence), never guessed; the count keeps
	// the degradation visible. Expected 0: the engine emits the attribute
	// from a successful Encode.
	MalformedDagCalls int

	// UnresolvedWaitTargets counts non-lock wait links whose target span id did
	// not resolve to an op (a missing/truncated target — Invariant T regression,
	// front-end loss, or typo). Build leaves such waits targetless and replay
	// degrades them from a join to a fixed delay, losing counterfactual
	// propagation to the target class; the gate makes this loud (design §6.1).
	UnresolvedWaitTargets int

	// MalformedWaitTimings counts wait links whose wcprof.wait.*_unix_ns
	// attributes were missing or unparseable (a malformed emit; conservatively
	// recorded as a zero-duration wait at the waiter's start, and failed by the gate).
	MalformedWaitTimings int

	// OrphanedParents counts ops whose recorded causal parent span id (wcprof.parent
	// or parentId) is NON-EMPTY but whose parent span is ABSENT from the graph, so the
	// op surfaces as a FALSE root. The data is incomplete: the op had a parent and it
	// is missing. This is distinct from a TRUE root (empty parent — an independent
	// session) and from an emit-side parentless bug (the id is set, so a parent
	// existed). The replay would treat the false root as independent and miss savings
	// that should propagate through its lost parent edge, so the gate fails.
	//
	// Observed reproducibly on local otlpdump captures (e.g. ~330–540 on the exec
	// workload). The exact loss point is NOT pinned — it could be the local capture
	// instrument, the export pipeline, ingest, or an emit bug that set a bad id; do
	// not state a mechanism the evidence does not support. (The unresolved-WAIT-target
	// check does NOT catch this — a dropped parent need not be anyone's wait target,
	// as the exec capture's 330 orphans passing that check showed.)
	OrphanedParents      int
	OrphanedParentSample []string

	// Completeness checksum (design §6.1, leaf-drop detection). A dropped LEAF span
	// breaks no edge, so it is invisible to OrphanedParents/UnresolvedWaitTargets; on
	// a large Cloud trace with the residual CLI→Cloud export drop that means a
	// silently-incomplete-but-gate-passing trace. The producer declares the exact total
	// it emitted (WcprofSessionSpanCountAttr, stamped at session teardown on the
	// wcprof.session_complete carrier span); the loader counts the distinct engine spans
	// it received (WcprofEngineSpanAttr) and reconciles.
	//
	// SessionMarkerPresent is whether any received span carried the declared total.
	// Absent ⇒ unverifiable ⇒ the gate hard-fails (fail-by-default; an unstamped or
	// pre-checksum trace is refused). DeclaredEngineSpans is that total;
	// ReceivedEngineSpans is the distinct received count; MissingSpans = declared −
	// received (0 on a complete trace; > 0 ⇒ the gate hard-fails).
	SessionMarkerPresent bool
	DeclaredEngineSpans  int
	ReceivedEngineSpans  int
	MissingSpans         int
}

// Load parses an otlpdump JSONL stream, compiles it to the wcprof IR, and
// builds the analyzer graph.
func Load(r io.Reader) (*Compiled, *wcanalyze.Graph, error) {
	spans, err := ParseOTLPDumpJSONL(r)
	if err != nil {
		return nil, nil, err
	}
	c, err := Compile(spans)
	if err != nil {
		return nil, nil, err
	}
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		return nil, nil, fmt.Errorf("build graph: %w", err)
	}
	// Op.ResultID on this source is a per-capture intern of dag.output (see
	// the intern above) — never comparable across captures, unlike the native
	// recorder's engine-global shared-result ids.
	g.ResultIDsCaptureLocal = true
	return c, g, nil
}

// otlpSpan/otlpLink mirror the otlpdump JSONL wire shape (hack/otlpdump). Note
// the integer timestamp fields are typed (uint64), so encoding/json parses them
// exactly — decoding into interface{}/map[string]any would coerce them to
// float64 and lose nanosecond precision above 2^53. The wait-edge timestamps
// (carried in link attributes, a map[string]any) dodge the same trap by being
// decimal *strings* on the wire (design §3.0).
type otlpSpan struct {
	Kind         string         `json:"kind"`
	TraceID      string         `json:"traceId"`
	SpanID       string         `json:"spanId"`
	ParentID     string         `json:"parentId"`
	Name         string         `json:"name"`
	StartNs      uint64         `json:"startNs"`
	EndNs        uint64         `json:"endNs"`
	Attrs        map[string]any `json:"attrs"`
	Status       string         `json:"status"`
	Links        []otlpLink     `json:"links"`
	DroppedLinks int            `json:"droppedLinks"`
}

type otlpLink struct {
	SpanID       string         `json:"spanId"`
	Attrs        map[string]any `json:"attrs"`
	DroppedAttrs int            `json:"droppedAttrs"`
}

// ParseOTLPDumpJSONL reads an otlpdump JSONL capture and returns its spans
// (log and metric lines are ignored).
func ParseOTLPDumpJSONL(r io.Reader) ([]Span, error) {
	sc := bufio.NewScanner(r)
	// otlpdump lines (a span with a large dag.call attribute) can be long.
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	var spans []Span
	for line := 0; sc.Scan(); line++ {
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var s otlpSpan
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("otlpdump line %d: %w", line+1, err)
		}
		if s.Kind != "span" {
			continue
		}
		links := make([]Link, 0, len(s.Links))
		for _, l := range s.Links {
			links = append(links, Link{SpanID: l.SpanID, Attrs: l.Attrs, DroppedAttrs: l.DroppedAttrs})
		}
		spans = append(spans, Span{
			TraceID:      s.TraceID,
			SpanID:       s.SpanID,
			ParentID:     s.ParentID,
			Name:         s.Name,
			StartUnixNS:  s.StartNs,
			EndUnixNS:    s.EndNs,
			Attrs:        s.Attrs,
			StatusError:  isErrorStatus(s.Status),
			Links:        links,
			DroppedLinks: s.DroppedLinks,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read otlpdump: %w", err)
	}
	return spans, nil
}

// Compile maps a set of one trace's spans to the wcprof IR (design §5),
// performing only mechanical translation.
func Compile(spans []Span) (*Compiled, error) {
	c := &Compiled{}

	// Step 1: dedup live-exported duplicates — keep the ended copy (max end).
	// While iterating, reject input that spans more than one trace: the unit of
	// analysis is one trace (design §10 decision 2), and otlpdump appends across
	// runs, so two runs in one file must not silently merge into a multi-root
	// graph.
	bySpan := make(map[string]Span, len(spans))
	traceID := ""
	for _, s := range spans {
		if s.SpanID == "" {
			c.SkippedNoSpanID++
			continue
		}
		if s.TraceID != "" {
			if traceID == "" {
				traceID = s.TraceID
			} else if s.TraceID != traceID {
				return nil, fmt.Errorf("otlpdump input spans more than one trace (%s, %s); the loader analyzes one trace (design §10) — capture each run to a fresh -out file", traceID, s.TraceID)
			}
		}
		if prev, ok := bySpan[s.SpanID]; !ok || s.EndUnixNS > prev.EndUnixNS {
			bySpan[s.SpanID] = s
		}
	}
	deduped := make([]Span, 0, len(bySpan))
	for _, s := range bySpan {
		deduped = append(deduped, s)
	}
	c.SpanCount = len(deduped)
	if len(deduped) == 0 {
		return nil, fmt.Errorf("no spans to compile")
	}

	// Completeness checksum (design §6.1, leaf-drop detection): reconcile the engine's
	// declared engine-span total against the distinct engine spans received. A dropped
	// leaf is invisible to the reference-based gate signals, so without this a large
	// trace with the residual export drop could gate-pass while silently incomplete.
	// Counts distinct WcprofEngineSpanAttr spans; the declared total rides on the
	// teardown wcprof.session_complete carrier (WcprofSessionSpanCountAttr, string-
	// encoded). The gate fails on received < declared OR an absent marker
	// (fail-by-default); received > declared is impossible under the exact-count
	// invariant and is hard-failed too (see gate.go).
	for i := range deduped {
		if attrBool(deduped[i].Attrs, telemetryattrs.WcprofEngineSpanAttr) {
			c.ReceivedEngineSpans++
		}
		if v := attrStr(deduped[i].Attrs, telemetryattrs.WcprofSessionSpanCountAttr); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > c.DeclaredEngineSpans {
				c.SessionMarkerPresent = true
				c.DeclaredEngineSpans = n
			}
		}
	}
	if c.SessionMarkerPresent && c.DeclaredEngineSpans > c.ReceivedEngineSpans {
		c.MissingSpans = c.DeclaredEngineSpans - c.ReceivedEngineSpans
	}

	// Drop the teardown count carrier (design §6.1) from the compiled ops: it rode in
	// only to carry the EXACT declared total (read just above) and is not a unit of
	// work, so excluding it keeps the graph/replay untouched. It is not marked
	// WcprofEngineSpanAttr either, so it never counted toward received.
	filtered := deduped[:0]
	for _, s := range deduped {
		if attrBool(s.Attrs, telemetryattrs.WcprofSessionCompleteAttr) {
			continue
		}
		filtered = append(filtered, s)
	}
	deduped = filtered
	c.SpanCount = len(deduped)
	if len(deduped) == 0 {
		return nil, fmt.Errorf("no spans to compile")
	}

	// Deterministic op-id assignment: sort by (start, span id) and number 1..N.
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].StartUnixNS != deduped[j].StartUnixNS {
			return deduped[i].StartUnixNS < deduped[j].StartUnixNS
		}
		return deduped[i].SpanID < deduped[j].SpanID
	})
	opIDBySpan := make(map[string]uint64, len(deduped))
	for i, s := range deduped {
		opIDBySpan[s.SpanID] = uint64(i + 1)
	}

	// Epoch = min span start; trace end = max ended-span end. Op intervals (and
	// wait intervals) are rebased to the epoch, matching the dump's relative-ns
	// convention (design §5 step 2; wcprof/dump.go).
	epoch := int64(deduped[0].StartUnixNS) // sorted, so this is the min start
	var traceEnd int64
	for _, s := range deduped {
		if s.EndUnixNS > 0 {
			if e := int64(s.EndUnixNS); e > traceEnd {
				traceEnd = e
			}
		}
	}
	// Dump time must not precede any span's start: a late in-flight span (one
	// that starts after every ended span) would otherwise get a zero/negative
	// open-op duration. deduped is start-sorted, so the last entry is the max
	// start.
	if maxStart := int64(deduped[len(deduped)-1].StartUnixNS); maxStart > traceEnd {
		traceEnd = maxStart
	}
	if traceEnd < epoch {
		traceEnd = epoch
	}

	// Identify which spans host a call_exec child, so the structural
	// withExec⇒exec fallback can be suppressed once the corrected shape exists
	// (design §5 step 2; impl-plan Chunk 1 op-kind precedence). A call_exec
	// span's causal parent is the caller call span.
	hasCallExecChild := make(map[uint64]bool)
	for _, s := range deduped {
		if attrStr(s.Attrs, telemetryattrs.WcprofOpKindAttr) == wcprof.OpKindCallExec.String() {
			if pid := opIDBySpan[causalParentSpanID(s)]; pid != 0 {
				hasCallExecChild[pid] = true
			}
		}
	}

	str := newStringTable()
	resultIDs := newU64Interner()
	var (
		events  []wcprof.DumpEvent
		openOps []wcprof.DumpOpenOp
	)

	for _, s := range deduped {
		opID := opIDBySpan[s.SpanID]
		cpSpan := causalParentSpanID(s)
		parentID := opIDBySpan[cpSpan]
		if cpSpan != "" && parentID == 0 {
			// Recorded a causal parent, but its span is absent from the capture:
			// a dropped parent (capture loss), surfacing this op as a false root.
			c.OrphanedParents++
			if len(c.OrphanedParentSample) < 10 {
				c.OrphanedParentSample = append(c.OrphanedParentSample, s.SpanID)
			}
		}
		if parentID == opID {
			parentID = 0 // never self-parent
		}
		kind := classifyKind(s, hasCallExecChild[opID])
		class := s.Name
		ident := attrStr(s.Attrs, telemetry.DagDigestAttr)
		workType := attrStr(s.Attrs, telemetryattrs.WcprofWorkTypeAttr)
		if workType == "" {
			workType = wcprof.WorkTypeEngine.String()
		}
		// The exec argv rides as a scalar JSON-array string; intern it verbatim
		// into MetaID — the SAME dump field the native recorder uses — so the one
		// shared wcanalyze.Build path decodes Op.Argv for both sources. Zero
		// inference: the string is mapped through untouched (absent ⇒ "" ⇒ id 0).
		argv := attrStr(s.Attrs, telemetryattrs.WcprofExecArgvAttr)
		// cache-DAG edges (what-if-cached design Chunk 4): the call span's
		// dag.inputs — the structural input recipe digests, emitted since
		// forever and discarded until now — re-encoded to the canonical
		// scalar JSON-array string the native dump uses, so the one shared
		// Build path decodes Op.CacheInputs identically for both sources.
		var inputsJSON string
		if inputs := attrStrSlice(s.Attrs, telemetry.DagInputsAttr); len(inputs) > 0 {
			if b, err := json.Marshal(inputs); err == nil {
				inputsJSON = string(b)
			}
		}
		var resultID uint64
		if out := attrStr(s.Attrs, telemetry.DagOutputAttr); out != "" {
			resultID = resultIDs.intern(out)
		}
		// Scope implicit inputs (invalidation-tracing design §4, Chunk-1
		// loader work): the dag.call payload — recorded since forever,
		// discarded until now — carries the call's implicit inputs
		// (callpbv1.Call.implicitInputs), the engine's deliberate cache-key
		// scoping. Parse ONLY the implicit-input names + value emptiness
		// (category-1 evidence); the full call structure is Chunk-4 work.
		var scopeJSON string
		if enc := attrStr(s.Attrs, telemetry.DagCallAttr); enc != "" {
			if scope, ok := decodeScopeInputs(enc); ok {
				if b, err := json.Marshal(scope); err == nil {
					scopeJSON = string(b)
				}
			} else {
				// Recorded but undecodable: counted AND marked on the op via
				// the sentinel, so the analyzer labels corrupted evidence as
				// corrupted — never as absent.
				c.MalformedDagCalls++
				scopeJSON = wcprof.ScopeMalformedSentinel
			}
		}

		if s.EndUnixNS == 0 {
			// In-flight at capture: an open op (Build ends it at dump time).
			c.OpenSpanCount++
			openOps = append(openOps, wcprof.DumpOpenOp{
				OpID:     opID,
				ParentID: parentID,
				Kind:     kind,
				WorkType: workType,
				ClassID:  str.intern(class),
				IdentID:  str.intern(ident),
				MetaID:   str.intern(argv),
				ScopeID:  str.intern(scopeJSON),
				StartNS:  int64(s.StartUnixNS) - epoch,
			})
			continue
		}

		events = append(events, wcprof.DumpEvent{
			Type:     "op",
			OpKind:   kind,
			WorkType: workType,
			Outcome:  computeOutcome(s),
			OpID:     opID,
			ParentID: parentID,
			ResultID: resultID,
			ClassID:  str.intern(class),
			IdentID:  str.intern(ident),
			MetaID:   str.intern(argv),
			InputsID: str.intern(inputsJSON),
			ScopeID:  str.intern(scopeJSON),
			StartNS:  int64(s.StartUnixNS) - epoch,
			EndNS:    int64(s.EndUnixNS) - epoch,
		})
	}

	// Step 3: wait-edge links → wait events, attributed to the waiter span.
	// Forced-evaluation links (lazy-semantics §4.4) map alongside: purely
	// factual link events (kind "forced"), never wait-shaped — a missing
	// target is LEGITIMATE there (production predated recording), so they
	// deliberately bypass the unresolved-wait gate signals.
	for _, s := range deduped {
		waiterID := opIDBySpan[s.SpanID]
		spanHasWait := false
		for _, l := range s.Links {
			if attrStr(l.Attrs, telemetry.LinkPurposeAttr) == telemetryattrs.LinkPurposeSuppressedIdent {
				// One link per emit-side ident-suppression firing: the tally
				// is exact, and a lost mark shows as a dropped link (gated).
				c.SuppressedIdentDerivations++
				continue
			}
			if attrStr(l.Attrs, telemetry.LinkPurposeAttr) == telemetryattrs.LinkPurposeForced {
				c.ForcedEdgeCount++
				events = append(events, wcprof.DumpEvent{
					Type:     "link",
					LinkKind: wcprof.LinkKindForced.String(),
					ParentID: waiterID,
					TargetID: opIDBySpan[normalizeSpanID(l.SpanID)],
					IdentID:  str.intern(attrStr(l.Attrs, telemetryattrs.WcprofForcedDigestAttr)),
				})
				continue
			}
			if attrStr(l.Attrs, telemetry.LinkPurposeAttr) != telemetryattrs.LinkPurposeWait {
				continue
			}
			spanHasWait = true
			c.WaitEdgeCount++
			c.WaitLinkDroppedAttrs += l.DroppedAttrs

			reason := attrStr(l.Attrs, telemetryattrs.WcprofWaitReasonAttr)
			ident := attrStr(l.Attrs, telemetryattrs.WcprofWaitIdentAttr)
			var targetID uint64
			if reason != wcprof.WaitReasonLock.String() {
				targetID = opIDBySpan[normalizeSpanID(l.SpanID)]
				if targetID == 0 {
					// A non-lock wait must name a resolvable target span
					// (Invariant T). A miss means the target was truncated or
					// lost; the wait is still emitted (targetless ⇒ a fixed
					// delay in replay) so the report renders, but the gate fails
					// on it loudly (design §6.1).
					c.UnresolvedWaitTargets++
				}
			}

			startAbs, okS := parseUnixNS(l.Attrs, telemetryattrs.WcprofWaitStartUnixNanoAttr)
			endAbs, okE := parseUnixNS(l.Attrs, telemetryattrs.WcprofWaitEndUnixNanoAttr)
			var startNS, endNS int64
			if okS && okE {
				startNS = startAbs - epoch
				endNS = endAbs - epoch
			} else {
				// Malformed emit: keep the causal target but a zero-duration,
				// no-op interval (replay classifies it as an abandoned wait).
				c.MalformedWaitTimings++
				startNS = int64(s.StartUnixNS) - epoch
				endNS = startNS
			}

			events = append(events, wcprof.DumpEvent{
				Type:     "wait",
				Reason:   reason,
				ParentID: waiterID,
				TargetID: targetID,
				IdentID:  str.intern(ident),
				StartNS:  startNS,
				EndNS:    endNS,
			})
		}
		if spanHasWait && s.DroppedLinks > 0 {
			c.WaitBearingDroppedLinks += s.DroppedLinks
		}
		c.TotalDroppedLinks += s.DroppedLinks
		for _, l := range s.Links {
			c.TotalDroppedLinkAttrs += l.DroppedAttrs
		}
	}

	c.Header = &wcprof.DumpHeader{
		SchemaVersion:              wcprof.DumpSchemaVersion,
		EpochUnixNano:              epoch,
		DumpedUnixNano:             traceEnd,
		EventCount:                 len(events),
		Strings:                    str.values,
		OpenOps:                    openOps,
		SuppressedIdentDerivations: uint64(c.SuppressedIdentDerivations),
	}
	c.Events = events
	return c, nil
}

// decodeScopeInputs extracts the scope implicit inputs from an encoded
// dag.call attribute: the base64 proto of the span's callpbv1.Call, whose
// implicitInputs field carries the engine-computed inputs hashed into the
// recipe digest (deliberate cache-key scoping — invalidation-tracing design
// §3.2 category 1). Only the NAMES plus a recorded-empty-value flag are
// kept: the emptiness is deciding data (an empty value is the engine
// deliberately NOT scoping, e.g. fromSessionScope on a digest-pinned ref),
// while the values themselves (session/client ids) decide nothing more. A
// literal that is not a plain string (impossible for in-tree scope inputs
// today) is conservatively non-empty: the input contributes SOMETHING to the
// key, which is the fact that matters. ok=false means the attribute failed
// to decode — counted by the caller, never guessed around.
func decodeScopeInputs(encoded string) (scope []wcanalyze.ScopeInput, ok bool) {
	var pbCall callpbv1.Call
	if err := pbCall.Decode(encoded); err != nil {
		return nil, false
	}
	scope = make([]wcanalyze.ScopeInput, 0, len(pbCall.ImplicitInputs))
	for _, in := range pbCall.ImplicitInputs {
		if in == nil || in.Name == "" {
			continue
		}
		si := wcanalyze.ScopeInput{Name: in.Name}
		if lit, isStr := in.Value.GetValue().(*callpbv1.Literal_String_); isStr {
			si.EmptyValue = lit.String_ == ""
		}
		scope = append(scope, si)
	}
	return scope, true
}

// classifyKind picks the op kind for a span (design §5 step 2; impl-plan
// Chunk 1 precedence): an explicit wcprof.op.kind always wins; otherwise a
// DagQL call span stays "call". The structural withExec⇒exec fallback exists
// only to give the intentionally-wrong un-augmented baseline some shape and is
// suppressed the moment the corrected shape exists (a call_exec child or a
// wcprof.op.kind), so the class table converges to native rather than drifting.
func classifyKind(s Span, hasCallExecChild bool) string {
	if k := attrStr(s.Attrs, telemetryattrs.WcprofOpKindAttr); k != "" {
		return k
	}
	if hasCallExecChild {
		return wcprof.OpKindCall.String()
	}
	if s.Name == "Container.withExec" {
		return wcprof.OpKindExec.String()
	}
	if attrStr(s.Attrs, telemetry.DagDigestAttr) != "" {
		return wcprof.OpKindCall.String()
	}
	return "" // unclassified (session root, leaf I/O, …) — future seams
}

// computeOutcome maps the available status/cache attributes to a wcprof
// outcome (design §5 step 2), precedence: status > stamp > cached/pending >
// ok. Failures are authoritative (the stamp below is written at the decision
// point, the status at span end — a call stamped "executed" that later
// failed IS a failure, matching native's error-over-hint rule). Then the
// explicit wcprof.call.outcome stamp (executed/joined/do_not_cache from the
// decision-#5 emit, plus hit_pending from the hit-production-state emit —
// lazy-semantics state B2) wins over the derived forms. CachedAttr maps to
// the complete hit as before (its emit is gated on production completeness,
// core/telemetry.go recordStatus).
//
// A bare PendingAttr without a stamp stays "ok", deliberately: recordPending
// (core/telemetry.go:157) runs at the end of EVERY call span, hit or miss,
// so on pre-stamp traces "pending ∧ ¬cached" is ambiguous between a
// pending-production hit and a cold miss that returned a lazy result —
// mapping it to a hit would corrupt hit sets on every lazy-producing miss.
// Post-emit traces disambiguate via the stamp (pending hits carry
// hit_pending; every non-hit success carries executed/joined), so the
// ambiguous shape only occurs on traces predating the emits, where the
// generic ok is the honest answer.
func computeOutcome(s Span) string {
	switch {
	case attrBool(s.Attrs, telemetry.CanceledAttr):
		return wcprof.OutcomeCanceled.String()
	case s.StatusError:
		return wcprof.OutcomeError.String()
	}
	if o := attrStr(s.Attrs, telemetryattrs.WcprofCallOutcomeAttr); o != "" {
		return o
	}
	if attrBool(s.Attrs, telemetry.CachedAttr) {
		return wcprof.OutcomeHit.String()
	}
	return wcprof.OutcomeOK.String()
}

// causalParentSpanID is the loader's only parentage rule: the engine-emitted
// wcprof.parent override if present, else the span's parentId (design §1.1
// Invariant E, §5). The loader only reads the override; it never derives one.
func causalParentSpanID(s Span) string {
	if p := attrStr(s.Attrs, telemetryattrs.WcprofParentAttr); p != "" {
		return normalizeSpanID(p)
	}
	return normalizeSpanID(s.ParentID)
}

// normalizeSpanID treats an empty or all-zero span id as "none".
func normalizeSpanID(id string) string {
	if id == "" {
		return ""
	}
	for _, ch := range id {
		if ch != '0' {
			return id
		}
	}
	return ""
}

func isErrorStatus(status string) bool {
	// otlpdump renders Status.Code.String(), e.g. "STATUS_CODE_ERROR".
	return status == "STATUS_CODE_ERROR" || status == "Error"
}

func attrStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func attrBool(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// attrStrSlice reads a string-slice attribute (an OTLP string array arrives
// from the otlpdump JSON as []any of strings). Non-string elements are
// impossible in a faithful emit and are simply not collected — never guessed
// into strings.
func attrStrSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// parseUnixNS parses a decimal-string absolute-Unix-nanos attribute exactly
// (design §3.0: strings, not numbers, to survive the map[string]any float64
// coercion). A non-string value is also accepted defensively for forward
// compatibility, but the canonical wire form is the decimal string.
func parseUnixNS(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case string:
		got, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0, false
		}
		return got, true
	case json.Number:
		got, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return got, true
	default:
		return 0, false
	}
}

// stringTable interns class/ident strings into the dump header's table, with
// id 0 reserved for the empty string (matching wcprof's recorder convention).
type stringTable struct {
	byValue map[string]uint32
	values  []string
}

func newStringTable() *stringTable {
	return &stringTable{byValue: map[string]uint32{"": 0}, values: []string{""}}
}

func (t *stringTable) intern(s string) uint32 {
	if s == "" {
		return 0
	}
	if id, ok := t.byValue[s]; ok {
		return id
	}
	id := uint32(len(t.values))
	t.values = append(t.values, s)
	t.byValue[s] = id
	return id
}

// u64Interner assigns dense uint64 ids to strings (used for the dag.output
// result-id seam, which shares no id space with op ids).
type u64Interner struct {
	byValue map[string]uint64
}

func newU64Interner() *u64Interner {
	return &u64Interner{byValue: map[string]uint64{}}
}

func (t *u64Interner) intern(s string) uint64 {
	if id, ok := t.byValue[s]; ok {
		return id
	}
	id := uint64(len(t.byValue) + 1)
	t.byValue[s] = id
	return id
}
