package wcotel

import (
	"os"
	"slices"
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// Chunk 4 data path: cache-DAG input edges (the dag.inputs attribute, emitted
// since forever and discarded until now) and the explicit call-outcome stamp
// (design decision #5).

// TestLoaderParsesDagInputs: a call span's dag.inputs becomes Op.CacheInputs,
// byte-identical to the native InputsID encoding (cross-source parity of the
// cache-DAG edges).
func TestLoaderParsesDagInputs(t *testing.T) {
	attrs := callAttrs("xxh3:consumer")
	attrs[telemetry.DagInputsAttr] = []string{"xxh3:input-a", "xxh3:input-b"}
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "Container.withExec", 0, 50, attrs),
		otSpan(idB, idRoot, "Query.version", 50, 60, callAttrs("xxh3:bare")),
	}
	c := mustCompile(t, toJSONL(t, recs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	var otelInputs []string
	for _, op := range g.Ops {
		switch op.Ident {
		case "xxh3:consumer":
			otelInputs = op.CacheInputs
		case "xxh3:bare":
			if op.CacheInputs != nil {
				t.Fatalf("input-less call must carry no CacheInputs, got %v", op.CacheInputs)
			}
		}
	}
	if !slices.Equal(otelInputs, []string{"xxh3:input-a", "xxh3:input-b"}) {
		t.Fatalf("otel CacheInputs = %v, want the emitted dag.inputs", otelInputs)
	}

	// Native side of the parity: the same inputs through the dump's InputsID.
	nat := newNativeIR()
	nat.op(1, 0, "", "POST /query", 0, 100, wcprof.OutcomeOK.String())
	nat.events = append(nat.events, wcprof.DumpEvent{
		Type: "op", OpKind: wcprof.OpKindCall.String(), WorkType: wcprof.WorkTypeEngine.String(),
		Outcome: wcprof.OutcomeExecuted.String(), OpID: 2, ParentID: 1,
		ClassID: nat.str.intern("Container.withExec"), IdentID: nat.str.intern("xxh3:consumer"),
		InputsID: nat.str.intern(`["xxh3:input-a","xxh3:input-b"]`),
		StartNS:  0, EndNS: 50 * ms,
	})
	ng := nat.graph(t)
	var nativeInputs []string
	for _, op := range ng.Ops {
		if op.Ident == "xxh3:consumer" {
			nativeInputs = op.CacheInputs
		}
	}
	if !slices.Equal(nativeInputs, otelInputs) {
		t.Fatalf("cross-source CacheInputs diverge: native %v vs otel %v", nativeInputs, otelInputs)
	}
}

// TestLoaderExplicitCallOutcome: the wcprof.call.outcome stamp wins over the
// derived ok, failures stay authoritative over the mid-call stamp, and
// do_not_cache becomes visible to eligibility on the OTel source.
func TestLoaderExplicitCallOutcome(t *testing.T) {
	stamped := func(digest, outcome string) map[string]any {
		a := callAttrs(digest)
		a[telemetryattrs.WcprofCallOutcomeAttr] = outcome
		return a
	}
	errSpan := otSpan(idLazy, idRoot, "D.op", 30, 40, stamped("d-err", "executed"))
	errSpan["status"] = "STATUS_CODE_ERROR"
	recs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "A.op", 0, 10, stamped("d-exec", "executed")),
		otSpan(idB, idRoot, "B.op", 10, 20, stamped("d-join", "joined")),
		otSpan(idExec, idRoot, "C.op", 20, 30, stamped("d-dnc", "do_not_cache")),
		errSpan,
		otSpan("eeeeeeeeeeeeeeee", idRoot, "E.op", 40, 50, callAttrs("d-plain")),
	}
	c := mustCompile(t, toJSONL(t, recs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"d-exec":  wcprof.OutcomeExecuted.String(),
		"d-join":  wcprof.OutcomeJoined.String(),
		"d-dnc":   wcprof.OutcomeDoNotCache.String(),
		"d-err":   wcprof.OutcomeError.String(), // status is authoritative over the stamp
		"d-plain": wcprof.OutcomeOK.String(),    // no stamp: the pre-amendment derivation
	}
	for _, op := range g.Ops {
		if exp, ok := want[op.Ident]; ok && op.Outcome != exp {
			t.Fatalf("%s outcome = %q, want %q", op.Ident, op.Outcome, exp)
		}
	}

	// do_not_cache now refuses eligibility on the OTel source too.
	res := wcanalyze.ResolveCachedHypothesis(g, wcanalyze.NewCachedHypothesis([]string{"d-dnc"}, 0))
	if res.Idents[0].State != wcanalyze.IdentDoNotCache {
		t.Fatalf("d-dnc state = %v, want the do_not_cache refusal", res.Idents[0].State)
	}
}

// TestCommittedCaptureCarriesInputs: the committed capture's call spans carry
// dag.inputs (the appendix fact this chunk consumes) — the loader now
// surfaces them as CacheInputs on real data.
func TestCommittedCaptureCarriesInputs(t *testing.T) {
	f, err := os.Open("testdata/baseline-simple-noservice.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, g, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	withInputs := 0
	for _, op := range g.Ops {
		if op.Kind == wcprof.OpKindCall.String() && len(op.CacheInputs) > 0 {
			withInputs++
		}
	}
	if withInputs == 0 {
		t.Fatal("no call op in the committed capture carries CacheInputs — dag.inputs parsing regressed")
	}
}
