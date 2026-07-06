package wccloud

import (
	"context"
	"strings"
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/internal/cloud"
)

// Row W8: the Cloud-trace path end-to-end through the wccloud front-end —
// the same fake-streamer ingestion shape the production path uses, the
// unchanged wcotel.Compile / wcanalyze.Build stage, then the why-uncached
// walk. Single-capture mode works with the OTel caveats; pair mode answers
// digest-stable origins straight off two Cloud-loaded graphs, and
// positional pairing is gated on the E3a ordered-input attr exactly as on
// the otlpdump path. Expectations reason-derived before running.

func loadFakeTrace(t *testing.T, spans []cloud.SpanData) *wcanalyze.Graph {
	t.Helper()
	_, g, err := Load(context.Background(), &fakeStreamer{batches: [][]cloud.SpanData{spans}}, "org", testTraceID)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestWhyMissW8CloudPath(t *testing.T) {
	// Fixture: target d-x (executed) with inputs [d-o (executed), d-s
	// (hit)]; the reference trace executed d-o under the same digest and
	// never called d-x. Derived: single-capture — d-o misses and is an
	// origin (no inputs recorded on it), d-s is a hit boundary, d-x is
	// collateral (its non-stable... in single mode any walked-miss input
	// explains the parent); pair mode — d-o is digest-stable and answers
	// category 2 with the mechanism-unclaimed text.
	mkTrace := func(withTarget bool) []cloud.SpanData {
		spans := []cloud.SpanData{
			cspan("1111111111111111", "", "session", testEpochNS, testEpochNS+900e6, map[string]any{}),
			cspan("2222222222222222", "1111111111111111", "Query.dep", testEpochNS+1e6, testEpochNS+100e6, map[string]any{
				telemetry.DagDigestAttr: "xxh3:d-o",
			}),
		}
		if withTarget {
			spans = append(spans,
				cspan("3333333333333333", "1111111111111111", "Query.hit", testEpochNS+2e6, testEpochNS+10e6, map[string]any{
					telemetry.DagDigestAttr: "xxh3:d-s",
					telemetry.CachedAttr:    true,
				}),
				cspan("4444444444444444", "1111111111111111", "Container.build", testEpochNS+100e6, testEpochNS+800e6, map[string]any{
					telemetry.DagDigestAttr:                "xxh3:d-x",
					telemetry.DagInputsAttr:                []string{"xxh3:d-o", "xxh3:d-s"},
					telemetryattrs.WcprofInputsOrderedAttr: `["xxh3:d-o","xxh3:d-s"]`,
				}),
			)
		}
		return spans
	}

	// Single-capture mode over the Cloud-loaded graph.
	g := loadFakeTrace(t, mkTrace(true))
	if !g.ResultIDsCaptureLocal {
		t.Fatal("a Cloud-loaded graph must carry the OTel-source marker")
	}
	rep, err := wcanalyze.RunWhyUncached(g, "xxh3:d-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Caveats) < 2 {
		t.Fatalf("the OTel source caveats must print on the Cloud path, got %v", rep.Caveats)
	}
	if rep.HitBoundaries != 1 {
		t.Fatalf("d-s must be a hit boundary, got %d", rep.HitBoundaries)
	}
	foundOrigin := false
	for _, o := range rep.Origins {
		if o.Node.Digest == "xxh3:d-o" {
			foundOrigin = true
		}
	}
	if !foundOrigin {
		t.Fatal("d-o must be an origin on the single-capture Cloud walk")
	}

	// Pair mode over two Cloud-loaded graphs: d-o digest-stable → category
	// 2, straight off the Cloud path. The E3a attr on d-x marks its vector
	// ordered (verified via the graph), so pairing soundness is available
	// where recorded.
	var target *wcanalyze.Op
	for _, op := range g.Ops {
		if op.Ident == "xxh3:d-x" {
			target = op
		}
	}
	if target == nil || !g.OrderedInputs(target) {
		t.Fatal("the E3a attr must mark the Cloud-loaded target's vector ordered")
	}
	gRef := loadFakeTrace(t, mkTrace(false))
	rep, err = wcanalyze.RunWhyUncachedPair(g, gRef, "xxh3:d-x")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range rep.Origins {
		if o.Node.Digest == "xxh3:d-o" {
			found = true
			if !strings.Contains(o.Answer, "which one applied here is not recorded") {
				t.Fatalf("category-2 answer on the Cloud path: %q", o.Answer)
			}
		}
	}
	if !found {
		t.Fatal("d-o must be a stable origin on the Cloud pair")
	}
}
