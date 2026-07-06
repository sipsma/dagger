package wcotel

import (
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/dagql/call/callpbv1"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// Chunk-1 loader work (invalidation-tracing design §4 "have today"): the
// recorded dag.call payload carries the call's implicit inputs — the
// engine's deliberate cache-key scoping — and the loader now preserves the
// NAMES plus value emptiness on the op. Pure parsing of recorded data; the
// full call structure stays Chunk-4 work.

func encodeCallWithImplicitInputs(t *testing.T, inputs []*callpbv1.Argument) string {
	t.Helper()
	pb := &callpbv1.Call{
		Field:          "moduleSource",
		Type:           &callpbv1.Type{NamedType: "ModuleSource"},
		Digest:         "xxh3:feedface",
		ImplicitInputs: inputs,
	}
	enc, err := pb.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func strArg(name, value string) *callpbv1.Argument {
	return &callpbv1.Argument{
		Name:  name,
		Value: &callpbv1.Literal{Value: &callpbv1.Literal_String_{String_: value}},
	}
}

func buildGraphFromCompiled(t *testing.T, c *Compiled) *wcanalyze.Graph {
	t.Helper()
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func opByIdent(t *testing.T, g *wcanalyze.Graph, ident string) *wcanalyze.Op {
	t.Helper()
	for _, op := range g.Ops {
		if op.Ident == ident {
			return op
		}
	}
	t.Fatalf("no op with ident %s", ident)
	return nil
}

// A dag.call with implicit inputs loads as ScopeInputs preserving names and
// the recorded-empty-value flag (the fromSessionScope digest-pinned shape,
// core/schema/container.go:1032-1034 — deciding data for W2's NOT-category-1
// side).
func TestLoaderParsesScopeImplicitInputs(t *testing.T) {
	enc := encodeCallWithImplicitInputs(t, []*callpbv1.Argument{
		strArg("cachePerClient", "client-1"),
		strArg("fromSessionScope", ""),
	})
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "root", "startNs": baseEp, "endNs": baseEnd}),
		rec(map[string]any{"spanId": idA, "parentId": idRoot, "name": "Query.moduleSource", "startNs": baseEp + 1, "endNs": baseEnd,
			"attrs": map[string]any{
				telemetry.DagDigestAttr: "xxh3:feedface",
				telemetry.DagCallAttr:   enc,
			}}),
	)
	c := mustCompile(t, jsonl)
	if c.MalformedDagCalls != 0 {
		t.Fatalf("well-formed dag.call must not count malformed, got %d", c.MalformedDagCalls)
	}
	g := buildGraphFromCompiled(t, c)
	op := opByIdent(t, g, "xxh3:feedface")
	want := []wcanalyze.ScopeInput{
		{Name: "cachePerClient", EmptyValue: false},
		{Name: "fromSessionScope", EmptyValue: true},
	}
	if len(op.ScopeInputs) != len(want) {
		t.Fatalf("ScopeInputs = %+v, want %+v", op.ScopeInputs, want)
	}
	for i := range want {
		if op.ScopeInputs[i] != want[i] {
			t.Fatalf("ScopeInputs[%d] = %+v, want %+v", i, op.ScopeInputs[i], want[i])
		}
	}
}

// A dag.call with NO implicit inputs is an authoritative absence: recorded
// scope structure, zero scope inputs — non-nil empty, distinct from the
// not-recorded nil.
func TestLoaderScopeRecordedEmpty(t *testing.T) {
	enc := encodeCallWithImplicitInputs(t, nil)
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "root", "startNs": baseEp, "endNs": baseEnd}),
		rec(map[string]any{"spanId": idA, "parentId": idRoot, "name": "Query.moduleSource", "startNs": baseEp + 1, "endNs": baseEnd,
			"attrs": map[string]any{
				telemetry.DagDigestAttr: "xxh3:feedface",
				telemetry.DagCallAttr:   enc,
			}}),
	)
	g := buildGraphFromCompiled(t, mustCompile(t, jsonl))
	op := opByIdent(t, g, "xxh3:feedface")
	if op.ScopeInputs == nil || len(op.ScopeInputs) != 0 {
		t.Fatalf("recorded-empty scope must be non-nil empty, got %#v", op.ScopeInputs)
	}
}

// No dag.call attribute ⇒ scope structure not recorded (nil), and a
// malformed one ⇒ counted, left unrecorded, never guessed.
func TestLoaderScopeAbsentAndMalformed(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "root", "startNs": baseEp, "endNs": baseEnd}),
		rec(map[string]any{"spanId": idA, "parentId": idRoot, "name": "Query.noCall", "startNs": baseEp + 1, "endNs": baseEnd,
			"attrs": map[string]any{telemetry.DagDigestAttr: "xxh3:aaaa"}}),
		rec(map[string]any{"spanId": idB, "parentId": idRoot, "name": "Query.badCall", "startNs": baseEp + 2, "endNs": baseEnd,
			"attrs": map[string]any{
				telemetry.DagDigestAttr: "xxh3:bbbb",
				telemetry.DagCallAttr:   "%%% not base64 %%%",
			}}),
	)
	c := mustCompile(t, jsonl)
	if c.MalformedDagCalls != 1 {
		t.Fatalf("malformed dag.call must be counted exactly once, got %d", c.MalformedDagCalls)
	}
	g := buildGraphFromCompiled(t, c)
	if op := opByIdent(t, g, "xxh3:aaaa"); op.ScopeInputs != nil {
		t.Fatalf("absent dag.call must leave scope unrecorded (nil), got %#v", op.ScopeInputs)
	}
	if op := opByIdent(t, g, "xxh3:bbbb"); op.ScopeInputs != nil {
		t.Fatalf("malformed dag.call must leave scope unrecorded (nil), got %#v", op.ScopeInputs)
	}
}
