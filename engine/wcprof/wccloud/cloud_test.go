package wccloud

import (
	"context"
	"strconv"
	"testing"
	"time"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
	"github.com/dagger/dagger/internal/cloud"
)

// Absolute Unix-nanos epoch chosen ABOVE 2^53 (~9.0e15) so the wait-timing values
// (~1.78e18, 19 digits) cannot be represented exactly by a float64 — the whole
// reason wcprof.wait.*_unix_ns ride as decimal STRINGS (design §3.0). A test that
// used small ns would silently pass even if the value were float64-coerced.
const testEpochNS int64 = 1_782_596_726_000_000_000

const testTraceID = "0123456789abcdef0123456789abcdef"

type fakeStreamer struct{ batches [][]cloud.SpanData }

func (f *fakeStreamer) StreamSpans(_ context.Context, _, _ string, handler func([]cloud.SpanData)) error {
	for _, b := range f.batches {
		handler(b)
	}
	return nil
}

func cspan(id, parent, name string, startNS, endNS int64, attrs map[string]any, links ...cloud.SpanLink) cloud.SpanData {
	var p *string
	if parent != "" {
		pp := parent
		p = &pp
	}
	var end *time.Time
	if endNS > 0 {
		t := time.Unix(0, endNS)
		end = &t
	}
	return cloud.SpanData{
		ID:         id,
		TraceID:    testTraceID,
		Name:       name,
		ParentID:   p,
		Timestamp:  time.Unix(0, startNS),
		EndTime:    end,
		Attributes: attrs,
		Links:      links,
	}
}

func cwait(target, reason string, startNS, endNS int64) cloud.SpanLink {
	return cloud.SpanLink{
		SpanID: target,
		Attributes: map[string]any{
			telemetry.LinkPurposeAttr:                  telemetryattrs.LinkPurposeWait,
			telemetryattrs.WcprofWaitReasonAttr:        reason,
			telemetryattrs.WcprofWaitStartUnixNanoAttr: strconv.FormatInt(startNS, 10),
			telemetryattrs.WcprofWaitEndUnixNanoAttr:   strconv.FormatInt(endNS, 10),
		},
	}
}

func callExecAttrs(digest string) map[string]any {
	return map[string]any{
		telemetryattrs.WcprofOpKindAttr: wcprof.OpKindCallExec.String(),
		telemetry.DagDigestAttr:         digest,
		telemetry.UIPassthroughAttr:     true,
	}
}

// TestSpanFromCloudFieldMap pins the pure field map: ids lower-cased, parent
// dereferenced, a nil EndTime → 0 (in-flight), error status detected, links
// carried with their attributes intact (design §5).
func TestSpanFromCloudFieldMap(t *testing.T) {
	parent := "AABBCCDDAABBCCDD" // upper-case to prove lower-casing
	end := time.Unix(0, testEpochNS+90)
	sd := cloud.SpanData{
		ID:         "FF00FF00FF00FF00",
		TraceID:    "ABCDABCDABCDABCDABCDABCDABCDABCD",
		Name:       "Container.stdout",
		ParentID:   &parent,
		Timestamp:  time.Unix(0, testEpochNS+5),
		EndTime:    &end,
		Status:     cloud.SpanStatus{Code: "STATUS_CODE_ERROR"},
		Attributes: callExecAttrs("sha256:x"),
		Links:      []cloud.SpanLink{cwait("11AA11AA11AA11AA", "call_exec", testEpochNS+8, testEpochNS+80)},
	}
	got := SpanFromCloud(&sd)
	if got.SpanID != "ff00ff00ff00ff00" || got.ParentID != "aabbccddaabbccdd" {
		t.Fatalf("ids must be lower-cased: span=%q parent=%q", got.SpanID, got.ParentID)
	}
	if got.TraceID != "abcdabcdabcdabcdabcdabcdabcdabcd" {
		t.Fatalf("trace id must be lower-cased: %q", got.TraceID)
	}
	if got.StartUnixNS != uint64(testEpochNS+5) || got.EndUnixNS != uint64(testEpochNS+90) {
		t.Fatalf("timestamps: start=%d end=%d", got.StartUnixNS, got.EndUnixNS)
	}
	if !got.StatusError {
		t.Fatal("STATUS_CODE_ERROR must map to StatusError=true")
	}
	if len(got.Links) != 1 || got.Links[0].SpanID != "11aa11aa11aa11aa" {
		t.Fatalf("link target must be lower-cased: %+v", got.Links)
	}
	// a nil EndTime maps to EndUnixNS=0 (in-flight), which Compile treats as open.
	noEnd := cloud.SpanData{ID: "01", TraceID: testTraceID, Timestamp: time.Unix(0, testEpochNS)}
	if SpanFromCloud(&noEnd).EndUnixNS != 0 {
		t.Fatal("nil EndTime must map to EndUnixNS=0")
	}
}

// TestCloudFetchDedupGateClean drives the full front-end: a fake Cloud stream
// (with a live start/end duplicate, as spansUpdated emits) → Fetch → Load. The
// compile/replay stage is the UNCHANGED wcotel stage, so the result must be a
// clean §6.1 gate with the wait edge resolved.
func TestCloudFetchDedupGateClean(t *testing.T) {
	e := testEpochNS
	ended := []cloud.SpanData{
		cspan("a0a0a0a0a0a0a0a0", "", "POST /query", e, e+100, nil),
		cspan("b0b0b0b0b0b0b0b0", "a0a0a0a0a0a0a0a0", "Container.withExec", e+5, e+95, callAttrs("sha256:exec"),
			cwait("c0c0c0c0c0c0c0c0", "call_exec", e+8, e+92)),
		cspan("c0c0c0c0c0c0c0c0", "b0b0b0b0b0b0b0b0", "Container.withExec", e+8, e+92, callExecAttrs("sha256:exec")),
	}
	// a live "start" snapshot of the caller (EndTime nil) in an earlier batch; the
	// ended copy in the next batch must win the dedup (Compile keeps max end).
	startSnapshot := cspan("b0b0b0b0b0b0b0b0", "a0a0a0a0a0a0a0a0", "Container.withExec", e+5, 0, callAttrs("sha256:exec"))
	fake := &fakeStreamer{batches: [][]cloud.SpanData{{startSnapshot}, ended}}

	c, g, err := Load(context.Background(), fake, "org", "trace")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.SpanCount != 3 {
		t.Fatalf("dedup must collapse the live start snapshot: got %d spans, want 3", c.SpanCount)
	}
	gate := wcotel.CheckStructural(c, g, wcotel.GateOptions{})
	if gate.WaitEdges != 1 || gate.UnresolvedWaitTargets != 0 || gate.OrphanedParents != 0 {
		t.Fatalf("gate: waits=%d unresolved=%d orphaned=%d", gate.WaitEdges, gate.UnresolvedWaitTargets, gate.OrphanedParents)
	}
	if err := gate.Err(); err != nil {
		t.Fatalf("structural gate must pass for a complete Cloud trace: %v", err)
	}
}

func callAttrs(digest string) map[string]any {
	return map[string]any{telemetry.DagDigestAttr: digest}
}

// TestCloudCapStressThousandsOfWaitLinks is the §6.6 cap-stress at the converter
// level: a single span carrying a suppressed-sibling wait fan-in in the THOUSANDS
// (the §6.5 shape). Every wait link must survive the converter + Compile and
// resolve to its target — validating the loader handles far more links per span
// than a toy, the local half of the LinkCountLimit=16384 guarantee (the Cloud-side
// truncation half needs a live workload at this fan-in, noted in the round-trip
// test).
func TestCloudCapStressThousandsOfWaitLinks(t *testing.T) {
	const fanIn = 3000
	e := testEpochNS
	spans := []cloud.SpanData{
		cspan("d0d0d0d0d0d0d0d0", "", "POST /query", e, e+10_000, nil),
	}
	// fanIn distinct target call_exec spans...
	waiterLinks := make([]cloud.SpanLink, 0, fanIn)
	for i := 0; i < fanIn; i++ {
		tid := "e" + leftPad(strconv.FormatInt(int64(i), 16), 15)
		spans = append(spans, cspan(tid, "d0d0d0d0d0d0d0d0", "Container.stdout", e+int64(i), e+int64(i)+5, callExecAttrs("sha256:sib")))
		waiterLinks = append(waiterLinks, cwait(tid, "singleflight", e+int64(i), e+int64(i)+5))
	}
	// ...all waited on by ONE span.
	spans = append(spans, cspan("f0f0f0f0f0f0f0f0", "d0d0d0d0d0d0d0d0", "Container.withServiceBinding", e+1, e+9_000, callAttrs("sha256:waiter"), waiterLinks...))

	c, g, err := Load(context.Background(), &fakeStreamer{batches: [][]cloud.SpanData{spans}}, "org", "trace")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	gate := wcotel.CheckStructural(c, g, wcotel.GateOptions{})
	if gate.WaitEdges != fanIn {
		t.Fatalf("all %d wait links must survive: got %d", fanIn, gate.WaitEdges)
	}
	if gate.UnresolvedWaitTargets != 0 {
		t.Fatalf("every wait must resolve to its target: %d unresolved", gate.UnresolvedWaitTargets)
	}
	if err := gate.Err(); err != nil {
		t.Fatalf("structural gate must pass at %d-way fan-in: %v", fanIn, err)
	}
}

// TestCloudWaitNSBitExactThroughConverter is the data-level float64-dodge proof: a
// wait-timing value of 19 digits (> 2^53) arrives as a Go string (as Cloud's JSON
// decode yields for a string attribute) and must reach the compiled wait event
// bit-exact. A float64 path would truncate the low digits.
func TestCloudWaitNSBitExactThroughConverter(t *testing.T) {
	startNS := testEpochNS + 123_456_789 // 1782596726123456789
	endNS := testEpochNS + 987_654_321
	e := testEpochNS
	spans := []cloud.SpanData{
		cspan("aa", "", "POST /query", e, e+1_000_000_000, nil),
		cspan("bb", "aa", "Container.withExec", e, e+1_000_000_000, callAttrs("sha256:x"), cwait("cc", "call_exec", startNS, endNS)),
		cspan("cc", "bb", "Container.withExec", e, e+1_000_000_000, callExecAttrs("sha256:x")),
	}
	c, _, err := Load(context.Background(), &fakeStreamer{batches: [][]cloud.SpanData{spans}}, "org", "trace")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// find the wait event and assert its rebased interval equals the exact integer
	// arithmetic (epoch-rebased), proving no float64 rounding entered.
	var found bool
	for _, ev := range c.Events {
		if ev.Type == "wait" {
			found = true
			wantStart := startNS - e
			wantEnd := endNS - e
			if ev.StartNS != wantStart || ev.EndNS != wantEnd {
				t.Fatalf("wait ns truncated: got [%d,%d] want [%d,%d] (a float64 path would lose the low digits)",
					ev.StartNS, ev.EndNS, wantStart, wantEnd)
			}
		}
	}
	if !found {
		t.Fatal("no wait event compiled")
	}
	if c.MalformedWaitTimings != 0 {
		t.Fatalf("wait timing must parse cleanly, got %d malformed", c.MalformedWaitTimings)
	}
}

func leftPad(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}
