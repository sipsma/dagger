package wcotel

import (
	"strings"
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// General-rule emit rows on the OTel source (catalog V27, V32; design §4.5).

// lazyAttrs is a lazy span carrying the producer's recipe digest — the
// general-rule emit shape (beginOTelLazyOp).
func lazyAttrs(digest string) map[string]any {
	return map[string]any{
		telemetryattrs.WcprofOpKindAttr: wcprof.OpKindLazy.String(),
		telemetry.DagDigestAttr:         digest,
	}
}

// --- V27: lazy ops carry the producer digest across lazy families,
// byte-identical between the native dump and the OTel loader.
func TestLazyProducerIdentParity(t *testing.T) {
	families := []struct{ class, digest string }{
		{"Container.withExec", "xxh3:wexec"},
		{"Directory.withDirectory", "xxh3:wdir"},
		{"File.withName", "xxh3:wname"},
	}
	recs := []map[string]any{otSpan(idRoot, idNone, "POST /query", 0, 100, nil)}
	ids := []string{idA, idB, idExec}
	for i, f := range families {
		a := lazyAttrs(f.digest)
		recs = append(recs, otSpan(ids[i], idRoot, f.class, int64(i*10), int64(i*10+5), a))
	}
	c := mustCompile(t, toJSONL(t, recs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	otelIdents := map[string]string{}
	for _, op := range g.Ops {
		if op.Kind == wcprof.OpKindLazy.String() {
			otelIdents[op.Class] = op.Ident
		}
	}

	// Native side: the recorder path interns the same digest strings.
	nat := newNativeIR()
	nat.op(1, 0, "", "POST /query", 0, 100, wcprof.OutcomeOK.String())
	for i, f := range families {
		nat.opIdent(uint64(2+i), 1, wcprof.OpKindLazy.String(), f.class, f.digest, int64(i*10), int64(i*10+5), wcprof.OutcomeOK.String())
	}
	ng := nat.graph(t)
	for _, f := range families {
		if otelIdents[f.class] != f.digest {
			t.Fatalf("otel lazy %s ident = %q, want %q", f.class, otelIdents[f.class], f.digest)
		}
		found := false
		for _, op := range ng.Ops {
			if op.Kind == wcprof.OpKindLazy.String() && op.Class == f.class {
				found = true
				if op.Ident != otelIdents[f.class] {
					t.Fatalf("%s idents diverge: native %q vs otel %q", f.class, op.Ident, otelIdents[f.class])
				}
			}
		}
		if !found {
			t.Fatalf("native lazy op for %s missing", f.class)
		}
	}
}

// --- V32: a pending-production hit loads as a hit on OTel via the explicit
// hit_pending stamp; a BARE PendingAttr without a stamp deliberately stays
// "ok" (recordPending fires on misses too — core/telemetry.go:157 — so on
// pre-emit traces the bare shape is ambiguous and never guessed); CachedAttr
// still means the complete hit. Native/OTel eligibility parity asserted.
func TestPendingHitLoadsAsHit(t *testing.T) {
	stampedPending := callAttrs("d-b2")
	stampedPending[telemetryattrs.WcprofCallOutcomeAttr] = wcprof.OutcomeHitPending.String()
	stampedPending[telemetry.PendingAttr] = true
	barePending := callAttrs("d-old")
	barePending[telemetry.PendingAttr] = true
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "A.op", 0, 10, stampedPending),
		otSpan(idB, idRoot, "B.op", 10, 20, barePending),
		otSpan(idExec, idRoot, "C.op", 20, 30, cachedAttrs("d-b1")),
	}
	c := mustCompile(t, toJSONL(t, recs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"d-b2":  wcprof.OutcomeHitPending.String(),
		"d-old": wcprof.OutcomeOK.String(), // ambiguous pre-emit shape: never guessed
		"d-b1":  wcprof.OutcomeHit.String(),
	}
	for _, op := range g.Ops {
		if exp, ok := want[op.Ident]; ok && op.Outcome != exp {
			t.Fatalf("%s outcome = %q, want %q", op.Ident, op.Outcome, exp)
		}
	}

	// Hit-set extraction: the stamped B2 hit is pending, not complete.
	if got := wcanalyze.HitDigests(g); len(got) != 1 || got[0] != "d-b1" {
		t.Fatalf("complete hits = %v, want [d-b1]", got)
	}
	if got := wcanalyze.PendingHitDigests(g); len(got) != 1 || got[0] != "d-b2" {
		t.Fatalf("pending hits = %v, want [d-b2]", got)
	}

	// Parity: the same outcomes through the native dump classify identically.
	nat := newNativeIR()
	nat.op(1, 0, "", "POST /query", 0, 100, wcprof.OutcomeOK.String())
	nat.opIdent(2, 1, wcprof.OpKindCall.String(), "A.op", "d-b2", 0, 10, wcprof.OutcomeHitPending.String())
	nat.opIdent(3, 1, wcprof.OpKindCall.String(), "C.op", "d-b1", 20, 30, wcprof.OutcomeHit.String())
	ng := nat.graph(t)
	for _, g2 := range []*wcanalyze.Graph{g, ng} {
		res := wcanalyze.ResolveCachedHypothesis(g2, wcanalyze.NewCachedHypothesis([]string{"d-b2"}, 0))
		el := res.Idents[0]
		if el.State != wcanalyze.IdentAllHit || el.Hits != 1 || el.PendingHits != 1 {
			t.Fatalf("d-b2 eligibility = %+v, want all-hit with the pending split visible", el)
		}
	}
}

// Forced-evaluation links load as first-class edges: resolved target when the
// completing lazy span is in the capture, nil target when production predated
// recording (legitimate — never a gate signal).
func TestLoaderParsesForcedLinks(t *testing.T) {
	forced := func(target, digest string) map[string]any {
		return map[string]any{
			"spanId": target,
			"attrs": map[string]any{
				telemetry.LinkPurposeAttr:             telemetryattrs.LinkPurposeForced,
				telemetryattrs.WcprofForcedDigestAttr: digest,
			},
		}
	}
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "L.prod", 0, 40, lazyAttrs("d-prod")),
		otSpan(idB, idRoot, "C.consume", 50, 90, callAttrs("d-c"),
			forced(idA, "d-prod"),          // resolved target
			forced(idNone, "d-elsewhere")), // production predated recording
	}
	c := mustCompile(t, toJSONL(t, recs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.ForcedEdges) != 2 {
		t.Fatalf("forced edges = %d, want 2", len(g.ForcedEdges))
	}
	byIdent := map[string]*wcanalyze.ForcedEdge{}
	for _, fe := range g.ForcedEdges {
		byIdent[fe.Ident] = fe
	}
	if fe := byIdent["d-prod"]; fe == nil || fe.Target == nil || fe.Target.Ident != "d-prod" || fe.Forcer.Ident != "d-c" {
		t.Fatalf("resolved forced edge = %+v, want C.consume -> L.prod", byIdent["d-prod"])
	}
	if fe := byIdent["d-elsewhere"]; fe == nil || fe.Target != nil {
		t.Fatalf("unresolved forced edge = %+v, want nil target with the digest retained", byIdent["d-elsewhere"])
	}
	// The unresolved target must NOT count as a wait-loss gate signal.
	if c.UnresolvedWaitTargets != 0 {
		t.Fatalf("forced links must bypass the unresolved-wait gate, got %d", c.UnresolvedWaitTargets)
	}
}

// The emit-side ident-suppression marks flow loader → header → the shared
// what-if-cached admission gate: a trace carrying even one is REFUSED by the
// cached analysis (doctrine audit finding 1; expected 0 on real captures).
// The carrier is one targetless LINK per firing, so the tally is EXACT (two
// firings on one span count as two, unlike a bool attr) and a lost mark is a
// dropped link the structural gate refuses.
func TestLoaderSuppressedIdentRefusesCached(t *testing.T) {
	suppressed := map[string]any{
		"spanId": idNone,
		"attrs": map[string]any{
			telemetry.LinkPurposeAttr: telemetryattrs.LinkPurposeSuppressedIdent,
		},
	}
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "D.make", 0, 40, callAttrs("d-d")),
		otSpan(idB, idRoot, "L.broken", 50, 90, map[string]any{
			telemetryattrs.WcprofOpKindAttr: "lazy",
		}, suppressed, suppressed),
	}
	c := mustCompile(t, toJSONL(t, recs...))
	if c.SuppressedIdentDerivations != 2 || c.Header.SuppressedIdentDerivations != 2 {
		t.Fatalf("suppression tally must be exact per firing: compiled=%d header=%d, want 2/2",
			c.SuppressedIdentDerivations, c.Header.SuppressedIdentDerivations)
	}
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wcanalyze.RunCachedDetail(g, wcanalyze.NewCachedHypothesis([]string{"d-d"}, 0), 4); err == nil ||
		!strings.Contains(err.Error(), "derivation failure") {
		t.Fatalf("the cached analysis must refuse a suppression-marked trace, got %v", err)
	}
}
