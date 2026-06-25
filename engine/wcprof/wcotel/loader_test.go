package wcotel

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
)

// rec builds one otlpdump-shaped span record. Timestamps are passed as ints so
// the JSON carries integer literals, mirroring otlpdump's uint64 marshaling.
func rec(m map[string]any) map[string]any {
	m["kind"] = "span"
	return m
}

func toJSONL(t *testing.T, recs ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, r := range recs {
		bs, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(bs)
		b.WriteByte('\n')
	}
	return b.String()
}

func mustCompile(t *testing.T, jsonl string) *Compiled {
	t.Helper()
	spans, err := ParseOTLPDumpJSONL(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c, err := Compile(spans)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return c
}

func findOp(c *Compiled, class string) *wcprof.DumpEvent {
	for i := range c.Events {
		if c.Events[i].Type == "op" && c.Header.Strings[c.Events[i].ClassID] == class {
			return &c.Events[i]
		}
	}
	return nil
}

func findWait(c *Compiled, reason string) *wcprof.DumpEvent {
	for i := range c.Events {
		if c.Events[i].Type == "wait" && c.Events[i].Reason == reason {
			return &c.Events[i]
		}
	}
	return nil
}

const (
	idRoot  = "1111111111111111"
	idA     = "aaaaaaaaaaaaaaaa"
	idB     = "bbbbbbbbbbbbbbbb"
	idExec  = "cccccccccccccccc"
	idLazy  = "dddddddddddddddd"
	idNone  = "0000000000000000"
	baseEp  = 1_700_000_000_000_000_000 // > 2^53: exercises the float64 trap
	baseEnd = 1_700_000_001_000_000_000
)

// TestParseDedupKeepsEndedCopy: a live-exported span appears on start (end=0)
// and on end; the loader keeps the ended copy (design §5 step 1).
func TestParseDedupKeepsEndedCopy(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "Query.x", "startNs": baseEp, "endNs": 0}),
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "Query.x", "startNs": baseEp, "endNs": baseEnd}),
	)
	c := mustCompile(t, jsonl)
	if c.SpanCount != 1 {
		t.Fatalf("want 1 deduped span, got %d", c.SpanCount)
	}
	if c.OpenSpanCount != 0 {
		t.Fatalf("ended copy must win: want 0 open, got %d", c.OpenSpanCount)
	}
	op := findOp(c, "Query.x")
	if op == nil {
		t.Fatal("missing op for Query.x")
	}
	if got := op.EndNS - op.StartNS; got != baseEnd-baseEp {
		t.Fatalf("duration mismatch: got %d want %d", got, baseEnd-baseEp)
	}
}

// TestTimestampExactnessAboveFloat53: op and wait intervals above 2^53 ns are
// exact — proving the typed-uint64 span fields and decimal-string wait attrs
// dodge the float64 coercion (design §3.0).
func TestTimestampExactnessAboveFloat53(t *testing.T) {
	// 500_000_001 ns is not representable exactly as a float64 delta of two
	// ~1.7e18 values, so a float64 round-trip would perturb it.
	const opEnd = baseEp + 500_000_001
	waitStart := strconv.Itoa(baseEp + 123_456_789)
	waitEnd := strconv.Itoa(baseEp + 250_000_001)
	jsonl := toJSONL(t,
		rec(map[string]any{
			"spanId": idRoot, "parentId": idNone, "name": "Query.x",
			"startNs": baseEp, "endNs": opEnd,
		}),
		rec(map[string]any{
			"spanId": idA, "parentId": idRoot, "name": "Container.from",
			"startNs": baseEp, "endNs": opEnd,
			"links": []any{map[string]any{
				"spanId": idRoot,
				"attrs": map[string]any{
					telemetry.LinkPurposeAttr:                  telemetryattrs.LinkPurposeWait,
					telemetryattrs.WcprofWaitReasonAttr:        "singleflight",
					telemetryattrs.WcprofWaitStartUnixNanoAttr: waitStart,
					telemetryattrs.WcprofWaitEndUnixNanoAttr:   waitEnd,
				},
			}},
		}),
	)
	c := mustCompile(t, jsonl)
	op := findOp(c, "Query.x")
	if op.EndNS-op.StartNS != 500_000_001 {
		t.Fatalf("op duration not exact: got %d want 500000001", op.EndNS-op.StartNS)
	}
	w := findWait(c, "singleflight")
	if w == nil {
		t.Fatal("missing singleflight wait")
	}
	// Rebased to epoch (baseEp).
	if w.StartNS != 123_456_789 {
		t.Fatalf("wait start not exact: got %d want 123456789", w.StartNS)
	}
	if w.EndNS != 250_000_001 {
		t.Fatalf("wait end not exact: got %d want 250000001", w.EndNS)
	}
}

// TestCausalParentOverride: wcprof.parent overrides parentId as the causal
// parent; parentId stays the UI parent (design §3.0.2, §5).
func TestCausalParentOverride(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "Query.root", "startNs": baseEp, "endNs": baseEnd}),
		rec(map[string]any{"spanId": idLazy, "parentId": idRoot, "name": "Container.eval", "startNs": baseEp, "endNs": baseEnd}),
		// Work span: UI parent = idRoot, causal parent override = idLazy.
		rec(map[string]any{
			"spanId": idA, "parentId": idRoot, "name": "Container.work",
			"startNs": baseEp, "endNs": baseEnd,
			"attrs": map[string]any{telemetryattrs.WcprofParentAttr: idLazy},
		}),
	)
	c := mustCompile(t, jsonl)
	work := findOp(c, "Container.work")
	lazy := findOp(c, "Container.eval")
	if work.ParentID != lazy.OpID {
		t.Fatalf("causal parent should be the override (lazy op %d), got %d", lazy.OpID, work.ParentID)
	}
}

// TestOpKindPrecedence covers the impl-plan Chunk 1 classification precedence.
func TestOpKindPrecedence(t *testing.T) {
	t.Run("explicit kind wins", func(t *testing.T) {
		jsonl := toJSONL(t, rec(map[string]any{
			"spanId": idA, "parentId": idNone, "name": "Container.withExec",
			"startNs": baseEp, "endNs": baseEnd,
			"attrs": map[string]any{
				telemetryattrs.WcprofOpKindAttr: "call_exec",
				telemetry.DagDigestAttr:         "sha256:x",
			},
		}))
		if got := findOp(mustCompile(t, jsonl), "Container.withExec").OpKind; got != "call_exec" {
			t.Fatalf("explicit wcprof.op.kind must win: got %q", got)
		}
	})

	t.Run("call_exec child suppresses withExec exec fallback", func(t *testing.T) {
		jsonl := toJSONL(t,
			rec(map[string]any{
				"spanId": idA, "parentId": idNone, "name": "Container.withExec",
				"startNs": baseEp, "endNs": baseEnd,
				"attrs": map[string]any{telemetry.DagDigestAttr: "sha256:x"},
			}),
			// the call_exec child (parented to the withExec call span)
			rec(map[string]any{
				"spanId": idExec, "parentId": idA, "name": "Container.withExec",
				"startNs": baseEp, "endNs": baseEnd,
				"attrs": map[string]any{telemetryattrs.WcprofOpKindAttr: "call_exec"},
			}),
		)
		c := mustCompile(t, jsonl)
		// The parent withExec call span must classify as "call", not "exec".
		var parent *wcprof.DumpEvent
		for i := range c.Events {
			e := &c.Events[i]
			if e.Type == "op" && e.OpKind == "call" {
				parent = e
			}
		}
		if parent == nil {
			t.Fatal("withExec with a call_exec child must classify as call")
		}
	})

	t.Run("withExec becomes exec only un-augmented", func(t *testing.T) {
		jsonl := toJSONL(t, rec(map[string]any{
			"spanId": idA, "parentId": idNone, "name": "Container.withExec",
			"startNs": baseEp, "endNs": baseEnd,
			"attrs": map[string]any{telemetry.DagDigestAttr: "sha256:x"},
		}))
		if got := findOp(mustCompile(t, jsonl), "Container.withExec").OpKind; got != "exec" {
			t.Fatalf("un-augmented withExec must fall back to exec: got %q", got)
		}
	})

	t.Run("dag.digest call and unclassified", func(t *testing.T) {
		jsonl := toJSONL(t,
			rec(map[string]any{
				"spanId": idA, "parentId": idNone, "name": "Container.from",
				"startNs": baseEp, "endNs": baseEnd,
				"attrs": map[string]any{telemetry.DagDigestAttr: "sha256:y"},
			}),
			rec(map[string]any{
				"spanId": idB, "parentId": idNone, "name": "POST /query",
				"startNs": baseEp, "endNs": baseEnd,
			}),
		)
		c := mustCompile(t, jsonl)
		if got := findOp(c, "Container.from").OpKind; got != "call" {
			t.Fatalf("dag.digest span must be call: got %q", got)
		}
		if got := findOp(c, "POST /query").OpKind; got != "" {
			t.Fatalf("non-dag span must be unclassified: got %q", got)
		}
	})
}

// TestWaitEdgeMapping: a target-bearing wait resolves its target op; a lock
// wait carries an ident and no target (design §3.0, §5 step 3).
func TestWaitEdgeMapping(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idExec, "parentId": idNone, "name": "Container.withExec", "startNs": baseEp, "endNs": baseEnd,
			"attrs": map[string]any{telemetryattrs.WcprofOpKindAttr: "call_exec"}}),
		rec(map[string]any{
			"spanId": idA, "parentId": idNone, "name": "Container.stdout",
			"startNs": baseEp, "endNs": baseEnd,
			"attrs": map[string]any{telemetry.DagDigestAttr: "sha256:z"},
			"links": []any{
				map[string]any{ // singleflight wait → target op
					"spanId": idExec,
					"attrs": map[string]any{
						telemetry.LinkPurposeAttr:                  telemetryattrs.LinkPurposeWait,
						telemetryattrs.WcprofWaitReasonAttr:        "singleflight",
						telemetryattrs.WcprofWaitStartUnixNanoAttr: strconv.Itoa(baseEp),
						telemetryattrs.WcprofWaitEndUnixNanoAttr:   strconv.Itoa(baseEnd),
					},
				},
				map[string]any{ // lock wait → no target, ident only
					"spanId": idNone,
					"attrs": map[string]any{
						telemetry.LinkPurposeAttr:                  telemetryattrs.LinkPurposeWait,
						telemetryattrs.WcprofWaitReasonAttr:        "lock",
						telemetryattrs.WcprofWaitIdentAttr:         "cachevol:/data",
						telemetryattrs.WcprofWaitStartUnixNanoAttr: strconv.Itoa(baseEp),
						telemetryattrs.WcprofWaitEndUnixNanoAttr:   strconv.Itoa(baseEnd),
					},
				},
				map[string]any{ // a non-wait link (cause) is ignored
					"spanId": idExec,
					"attrs":  map[string]any{telemetry.LinkPurposeAttr: telemetry.LinkPurposeCause},
				},
			},
		}),
	)
	c := mustCompile(t, jsonl)
	if c.WaitEdgeCount != 2 {
		t.Fatalf("want 2 wait edges (cause ignored), got %d", c.WaitEdgeCount)
	}
	sf := findWait(c, "singleflight")
	exec := findOp(c, "Container.withExec")
	if sf.TargetID != exec.OpID {
		t.Fatalf("singleflight wait target should be the call_exec op %d, got %d", exec.OpID, sf.TargetID)
	}
	if sf.IdentID != 0 {
		t.Fatalf("singleflight wait should have no ident, got id %d", sf.IdentID)
	}
	lock := findWait(c, "lock")
	if lock.TargetID != 0 {
		t.Fatalf("lock wait must have no target, got %d", lock.TargetID)
	}
	if c.Header.Strings[lock.IdentID] != "cachevol:/data" {
		t.Fatalf("lock wait ident mismatch: got %q", c.Header.Strings[lock.IdentID])
	}
}

// TestHeaderAndRebasing: epoch is trace-min start, dump time is trace-max end,
// and op intervals are rebased so the earliest start is zero (design §5).
func TestHeaderAndRebasing(t *testing.T) {
	const aStart = baseEp + 10
	const aEnd = baseEp + 100
	const bStart = baseEp + 50
	const bEnd = baseEp + 200
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idA, "parentId": idNone, "name": "A", "startNs": aStart, "endNs": aEnd}),
		rec(map[string]any{"spanId": idB, "parentId": idNone, "name": "B", "startNs": bStart, "endNs": bEnd}),
	)
	c := mustCompile(t, jsonl)
	if c.Header.SchemaVersion != wcprof.DumpSchemaVersion {
		t.Fatalf("schema version mismatch: %d", c.Header.SchemaVersion)
	}
	if c.Header.EpochUnixNano != aStart {
		t.Fatalf("epoch should be min start %d, got %d", aStart, c.Header.EpochUnixNano)
	}
	if c.Header.DumpedUnixNano != bEnd {
		t.Fatalf("dump time should be max end %d, got %d", bEnd, c.Header.DumpedUnixNano)
	}
	if a := findOp(c, "A"); a.StartNS != 0 || a.EndNS != 90 {
		t.Fatalf("A rebased wrong: start=%d end=%d", a.StartNS, a.EndNS)
	}
	if b := findOp(c, "B"); b.StartNS != 40 || b.EndNS != 190 {
		t.Fatalf("B rebased wrong: start=%d end=%d", b.StartNS, b.EndNS)
	}
}

// TestOpenSpanRoutedToOpenOps: an in-flight span (end=0) becomes an open op in
// the header, not an "op" event (so Build ends it at dump time).
func TestOpenSpanRoutedToOpenOps(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "Query.done", "startNs": baseEp, "endNs": baseEnd}),
		rec(map[string]any{"spanId": idA, "parentId": idRoot, "name": "Container.inflight", "startNs": baseEp, "endNs": 0}),
	)
	c := mustCompile(t, jsonl)
	if c.OpenSpanCount != 1 || len(c.Header.OpenOps) != 1 {
		t.Fatalf("want 1 open op, got count=%d header=%d", c.OpenSpanCount, len(c.Header.OpenOps))
	}
	if findOp(c, "Container.inflight") != nil {
		t.Fatal("open span must not be emitted as an op event")
	}
	if c.Header.OpenOps[0].ParentID == 0 {
		t.Fatal("open op should retain its parent")
	}
}
