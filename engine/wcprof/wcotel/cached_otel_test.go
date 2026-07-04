package wcotel

import (
	"bytes"
	"os"
	"strings"
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// What-if-cached catalog rows V19–V20 (hack/designs/whatif-cached-design.md
// §4.5): cross-source parity and the OTel end-to-end path.

// opIdent extends the nativeIR builder with an ident-carrying op (call /
// call_exec ops need their recipe digest for what-if-cached hypotheses).
func (b *nativeIR) opIdent(id, parent uint64, kind, class, ident string, startMS, endMS int64, outcome string) {
	b.events = append(b.events, wcprof.DumpEvent{
		Type: "op", OpKind: kind, WorkType: wcprof.WorkTypeEngine.String(), Outcome: outcome,
		OpID: id, ParentID: parent, ClassID: b.str.intern(class), IdentID: b.str.intern(ident),
		StartNS: startMS * ms, EndNS: endMS * ms,
	})
}

// --- V19: cross-source parity. The same singleflight workload expressed as
// native events and as OTel spans (the established dual-fixture style), under
// the same CachedSet, must produce IDENTICAL elision decisions and savings:
// digest identity is the same key on both sources (native callKey ==
// dag.digest — design §3.6), and executed-vs-joined (native-only) is
// irrelevant under digest-level elision. Any divergence is a loader bug, not
// a model choice.
func TestCachedCrossSourceParity(t *testing.T) {
	const digest = "xxh3:paritydigest"
	const class = "Container.stdout"
	joinerIDs := []string{"a1a1a1a1a1a1a1a1", "b2b2b2b2b2b2b2b2", "c3c3c3c3c3c3c3c3"}

	// OTel spans: root [0,70], executor caller [10,70] + call_exec [12,60] +
	// publish [60,62], three joiners [20+2i,60] with singleflight waits.
	otelRecs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 70, nil),
		otSpan(idA, idRoot, class, 10, 70, callAttrs(digest), otWait(idExec, "call_exec", 12, 60)),
		otSpan(idExec, idA, class, 12, 60, callExecAttrs(digest)),
		otSpan(idLazy, idExec, publishResultSpanName, 60, 62, publishAttrs()),
	}
	// Native IR: the same intervals, with native outcomes (executed/joined vs
	// the OTel generic ok — the asymmetry that must not matter).
	nat := newNativeIR()
	nat.op(1, 0, "", "POST /query", 0, 70, wcprof.OutcomeOK.String())
	nat.opIdent(2, 1, wcprof.OpKindCall.String(), class, digest, 10, 70, wcprof.OutcomeExecuted.String())
	nat.opIdent(3, 2, wcprof.OpKindCallExec.String(), class, digest, 12, 60, wcprof.OutcomeOK.String())
	nat.op(4, 3, wcprof.OpKindInternal.String(), publishResultSpanName, 60, 62, wcprof.OutcomeOK.String())
	nat.wait(2, 3, wcprof.WaitReasonCallExec.String(), 12, 60)
	for i := range joinerIDs {
		jStart := int64(20 + i*2)
		otelRecs = append(otelRecs,
			otSpan(joinerIDs[i], idRoot, class, jStart, 60, callAttrs(digest),
				otWait(idExec, "singleflight", jStart, 60)))
		njID := uint64(10 + i)
		nat.opIdent(njID, 1, wcprof.OpKindCall.String(), class, digest, jStart, 60, wcprof.OutcomeJoined.String())
		nat.wait(njID, 3, wcprof.WaitReasonSingleflight.String(), jStart, 60)
	}

	otelC := mustCompile(t, toJSONL(t, otelRecs...))
	otelG, err := wcanalyze.Build(otelC.Header, otelC.Events)
	if err != nil {
		t.Fatalf("otel build: %v", err)
	}
	nativeG := nat.graph(t)
	mustGate(t, otelC, otelG)

	type outcome struct {
		baselineNS, savedNS                      int64
		shortCircuited, elidedOps, kept, regions int
		elidedSelfNS                             int64
	}
	measure := func(t *testing.T, g *wcanalyze.Graph) outcome {
		t.Helper()
		base := wcanalyze.NewSimulation(g, nil)
		baseline, err := base.Run()
		if err != nil {
			t.Fatal(err)
		}
		res := wcanalyze.ResolveCachedHypothesis(g, wcanalyze.NewCachedHypothesis([]string{digest}, 0))
		sim := wcanalyze.NewCachedSimulation(g, res)
		makespan, err := sim.Run()
		if err != nil {
			t.Fatal(err)
		}
		if sim.ElidedOpDemanded != 0 || sim.SimStartConflicts != 0 || sim.CycleWarnings != 0 || sim.UnschedulableOps != 0 {
			t.Fatalf("faithfulness signals nonzero: elided-demanded=%d conflicts=%d cycles=%d unschedulable=%d",
				sim.ElidedOpDemanded, sim.SimStartConflicts, sim.CycleWarnings, sim.UnschedulableOps)
		}
		el := res.Idents[0]
		return outcome{
			baselineNS:     baseline,
			savedNS:        baseline - makespan,
			shortCircuited: el.ShortCircuited,
			elidedOps:      res.ElidedOps,
			kept:           len(res.KeptRegions),
			regions:        res.ElidedRegions,
			elidedSelfNS:   res.ElidedSelfNS,
		}
	}

	no := measure(t, nativeG)
	oo := measure(t, otelG)
	if no != oo {
		t.Fatalf("cross-source divergence:\n native: %+v\n otel:   %+v", no, oo)
	}
	if no.savedNS <= 0 {
		t.Fatalf("parity fixture must show a positive saving, got %v", no.savedNS)
	}
	if no.shortCircuited != 4 || no.elidedOps != 2 || no.kept != 0 {
		t.Fatalf("expected 4 hits (executor + 3 joiners), 2 elided ops, 0 kept; got %+v", no)
	}
}

// --- V22 (OTel front-end): hit-digest extraction through wcotel.Load — the
// CachedAttr-derived hit outcome, deduped, with ok / error / open spans never
// counted. Exactly the digests whose warm outcome is hit, nothing inferred.
func TestCachedHitDigestExtractionOTel(t *testing.T) {
	errSpan := otSpan(idLazy, idRoot, "C.op", 60, 70, callAttrs("d-err"))
	errSpan["status"] = "STATUS_CODE_ERROR"
	openSpan := otSpan("eeeeeeeeeeeeeeee", idRoot, "D.op", 70, 0, cachedAttrs("d-open"))
	openSpan["endNs"] = 0 // exported on start only: open at capture
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "A.op", 0, 10, cachedAttrs("d-hit")),
		otSpan(idB, idRoot, "A.op", 10, 20, cachedAttrs("d-hit")), // dup: one entry
		otSpan(idExec, idRoot, "B.op", 20, 60, callAttrs("d-ok")), // ok: not a hit
		errSpan,
		openSpan,
	}
	c := mustCompile(t, toJSONL(t, recs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	got := wcanalyze.HitDigests(g)
	if len(got) != 1 || got[0] != "d-hit" {
		t.Fatalf("hit digests = %v, want exactly [d-hit]", got)
	}
}

// cachedAttrs is a call span satisfied from cache: dag.digest + the cached
// marker the loader maps to the hit outcome.
func cachedAttrs(digest string) map[string]any {
	a := callAttrs(digest)
	a[telemetry.CachedAttr] = true
	return a
}

// --- V20: OTel end-to-end on the committed testdata capture — the real
// front-end path (otlpdump JSONL through wcotel.Load), not just Build().
// Caching an executed digest visible in the capture yields an elision and a
// savings line, gate-clean; the full report carries the default-on ranking.
func TestCachedOTelEndToEndOnCapture(t *testing.T) {
	f, err := os.Open("testdata/baseline-simple-noservice.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	_, g, err := Load(f)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	// Pick the biggest executed (non-hit success) call digest that owns a
	// subtree — deterministically, so the test pins one real digest's shape.
	var target *wcanalyze.Op
	for _, op := range g.Ops {
		if op.Kind != wcprof.OpKindCall.String() || op.Ident == "" || op.Open {
			continue
		}
		if op.Outcome != wcprof.OutcomeOK.String() || len(op.Children) == 0 {
			continue
		}
		if target == nil || op.Duration() > target.Duration() ||
			(op.Duration() == target.Duration() && op.Ident < target.Ident) {
			target = op
		}
	}
	if target == nil {
		t.Fatal("capture has no executed call with a producing subtree")
	}

	detail, err := wcanalyze.RunCachedDetail(g, wcanalyze.NewCachedHypothesis([]string{target.Ident}, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if gerr := detail.GateErr(); gerr != nil {
		t.Fatalf("gate must be clean on the committed capture: %v", gerr)
	}
	if detail.Resolution.ElidedOps == 0 {
		t.Fatalf("caching %s must elide its producing subtree (%d children recorded)", target.Ident, len(target.Children))
	}
	if saved := detail.BaselineNS - detail.MakespanNS; saved < 0 {
		t.Fatalf("saving must be non-negative, got %d", saved)
	}
	var det bytes.Buffer
	detail.Write(&det)
	for _, want := range []string{"saved:", target.Ident, "eligible"} {
		if !strings.Contains(det.String(), want) {
			t.Fatalf("detail missing %q:\n%s", want, det.String())
		}
	}

	// The full report renders the default-on ranking from the same capture.
	var rep bytes.Buffer
	if err := wcanalyze.WriteReport(&rep, g, wcanalyze.ReportOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.String(), "what-if-cached: makespan saved") {
		t.Fatalf("report missing the what-if-cached ranking:\n%s", rep.String())
	}
}
