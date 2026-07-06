package wcotel

import (
	"bytes"
	"strings"
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// Catalog row V43, OTel half (design md §3.7.3): on this source Op.ResultID
// is a per-capture INTERN of the dag.output attribute (loader.go), so numeric
// equality across two captures is meaningless — the fixture below makes the
// trap concrete: the same dag.output strings intern to the SAME small
// integers in both captures, and a numeric join would happily pair them. The
// loader marks the graph, the calibration disables the pairing/provenance
// consumptions, and the report says so. Everything else about the
// decomposition (buckets, remainder gate, the kind-less OTel session root
// feeding the session bucket) works identically to native.
func TestCalibDecompRIDJoinDisabledOnOTel(t *testing.T) {
	outAttr := func(base map[string]any, out string) map[string]any {
		base[telemetry.DagOutputAttr] = out
		return base
	}
	coldJSONL := toJSONL(t,
		otSpan(idRoot, idNone, "POST /query", 0, 700, nil),
		otSpan(idA, idRoot, "Cls.d1", 0, 400, outAttr(callAttrs("d1"), "outA")),
		otSpan(idExec, idA, "Cls.d1", 0, 400, callExecAttrs("d1")),
		otSpan(idB, idRoot, "Cls.d3", 400, 700, outAttr(callAttrs("d3"), "outB")),
		otSpan(idLazy, idB, "Cls.d3", 400, 700, callExecAttrs("d3")),
	)
	warmJSONL := toJSONL(t,
		otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
		otSpan(idA, idRoot, "Cls.d1", 0, 10, outAttr(cachedAttrs("d1"), "outA")),
		otSpan(idB, idRoot, "Cls.d3", 10, 60, outAttr(callAttrs("d3w"), "outB")),
		otSpan(idLazy, idB, "Cls.d3", 10, 60, callExecAttrs("d3w")),
	)
	_, coldG, err := Load(strings.NewReader(coldJSONL))
	if err != nil {
		t.Fatalf("load cold: %v", err)
	}
	_, warmG, err := Load(strings.NewReader(warmJSONL))
	if err != nil {
		t.Fatalf("load warm: %v", err)
	}
	if !coldG.ResultIDsCaptureLocal || !warmG.ResultIDsCaptureLocal {
		t.Fatal("the OTel loader must mark its result ids capture-local")
	}

	cal, err := wcanalyze.RunCachedCalibration(coldG, warmG, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	d := cal.Decomp
	if d.RIDJoinAvailable {
		t.Fatal("the rid join must be disabled when either capture is OTel-sourced")
	}
	// The would-be false pair: cold d3 and warm d3w both carry dag.output
	// "outB", interned to equal numeric ids — no pairing may appear.
	if len(d.ExecWarmOnly) != 1 || d.ExecWarmOnly[0].Ident != "d3w" || d.ExecWarmOnly[0].PairedIdent != "" {
		t.Fatalf("warm-only = %+v, want unpaired d3w (numeric rid equality is meaningless across OTel captures)", d.ExecWarmOnly)
	}
	if len(d.ExecColdOnly) != 1 || d.ExecColdOnly[0].Ident != "d3" || d.ExecColdOnly[0].PairedIdent != "" {
		t.Fatalf("cold-only = %+v, want unpaired d3", d.ExecColdOnly)
	}

	// The rest of the decomposition behaves exactly as on native: d1 removed,
	// ledgers total (the kind-less OTel session root feeds the session
	// bucket, so nothing false-fires the remainder), gate PASS.
	if d.RemovedCleanly != 1 {
		t.Fatalf("removed cleanly = %d, want 1 (d1)", d.RemovedCleanly)
	}
	if err := cal.GateErr(); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cal.Write(&buf)
	out := buf.String()
	if !strings.Contains(out, "per-capture interns") {
		t.Fatalf("the native-only boundary must be stated:\n%s", out)
	}
	if strings.Contains(out, "same recorded result as") {
		t.Fatalf("no pairing line may render on OTel captures:\n%s", out)
	}
}
