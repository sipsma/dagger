package wcotel

import (
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/dagql/call/callpbv1"
	"github.com/dagger/dagger/engine/telemetryattrs"
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

// The E1 lookup-outcome fact loads from the additive span attr (request
// entry) and from targetless lookup_outcome links (the digest-only entry),
// byte-identical to the native encoding.
func TestLoaderParsesLookupOutcome(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "root", "startNs": baseEp, "endNs": baseEnd,
			"links": []map[string]any{{
				"spanId": idNone,
				"attrs": map[string]any{
					telemetry.LinkPurposeAttr:              telemetryattrs.LinkPurposeLookupOutcome,
					telemetryattrs.WcprofLookupDigestAttr:  "xxh3:digest-only-miss",
					telemetryattrs.WcprofLookupOutcomeAttr: "digest_only expired",
				},
			}}}),
		rec(map[string]any{"spanId": idA, "parentId": idRoot, "name": "Query.thing", "startNs": baseEp + 1, "endNs": baseEnd,
			"attrs": map[string]any{
				telemetry.DagDigestAttr:                "xxh3:request-miss",
				telemetryattrs.WcprofLookupOutcomeAttr: "request input_unknown 2",
			}}),
	)
	g := buildGraphFromCompiled(t, mustCompile(t, jsonl))
	op := opByIdent(t, g, "xxh3:request-miss")
	if op.LookupEntry != "request" || op.LookupReason != "input_unknown" || op.LookupInputIdx != 2 {
		t.Fatalf("op lookup fact = %q/%q/%d, want request/input_unknown/2", op.LookupEntry, op.LookupReason, op.LookupInputIdx)
	}
	if len(g.LookupFacts) != 1 {
		t.Fatalf("want 1 digest-only fact, got %d", len(g.LookupFacts))
	}
	lf := g.LookupFacts[0]
	if lf.Digest != "xxh3:digest-only-miss" || lf.Entry != "digest_only" || lf.Reason != "expired" {
		t.Fatalf("digest-only fact = %+v", lf)
	}
	if lf.Owner == nil || lf.Owner.Class != "root" {
		t.Fatalf("the fact's owner must be the loading span's op, got %+v", lf.Owner)
	}
}

// E3a preference: the ordered-input attr wins over dag.inputs verbatim
// (byte-identical to the native encoding, module ref included) and marks
// the op ordered; without it, dag.inputs loads unordered.
func TestLoaderPrefersE3aOrderedInputs(t *testing.T) {
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "root", "startNs": baseEp, "endNs": baseEnd}),
		rec(map[string]any{"spanId": idA, "parentId": idRoot, "name": "Query.ordered", "startNs": baseEp + 1, "endNs": baseEnd,
			"attrs": map[string]any{
				telemetry.DagDigestAttr:                "xxh3:ordered",
				telemetry.DagInputsAttr:                []string{"xxh3:in-b", "xxh3:in-a"}, // deduped module-less shape
				telemetryattrs.WcprofInputsOrderedAttr: `["xxh3:in-a","xxh3:in-b","xxh3:in-a","xxh3:mod"]`,
			}}),
		rec(map[string]any{"spanId": idB, "parentId": idRoot, "name": "Query.unordered", "startNs": baseEp + 2, "endNs": baseEnd,
			"attrs": map[string]any{
				telemetry.DagDigestAttr: "xxh3:unordered",
				telemetry.DagInputsAttr: []string{"xxh3:in-a"},
			}}),
	)
	g := buildGraphFromCompiled(t, mustCompile(t, jsonl))
	g.ResultIDsCaptureLocal = true

	ord := opByIdent(t, g, "xxh3:ordered")
	want := []string{"xxh3:in-a", "xxh3:in-b", "xxh3:in-a", "xxh3:mod"}
	if len(ord.CacheInputs) != len(want) {
		t.Fatalf("E3a vector must win verbatim: %v", ord.CacheInputs)
	}
	for i := range want {
		if ord.CacheInputs[i] != want[i] {
			t.Fatalf("E3a vector[%d] = %q, want %q (occurrence-level order preserved)", i, ord.CacheInputs[i], want[i])
		}
	}
	if !ord.InputsOrdered || !g.OrderedInputs(ord) {
		t.Fatal("the E3a-sourced op must be ordered")
	}
	unord := opByIdent(t, g, "xxh3:unordered")
	if unord.InputsOrdered || g.OrderedInputs(unord) {
		t.Fatal("a dag.inputs-only op on an OTel graph must be unordered")
	}
}

// E3b: the full canonical self structure loads from dag.call — field,
// receiver, nth/view, module, args and implicit inputs with bounded
// renderings.
func TestLoaderParsesCallSelf(t *testing.T) {
	pb := &callpbv1.Call{
		Field:          "withExec",
		ReceiverDigest: "xxh3:recv",
		View:           "v1",
		Nth:            2,
		Type:           &callpbv1.Type{NamedType: "Container"},
		Module:         &callpbv1.Module{CallDigest: "xxh3:mod", Name: "myMod", Ref: "github.com/x/m", Pin: "abc"},
		Args: []*callpbv1.Argument{
			strArg("platform", "linux/amd64"),
			{Name: "count", Value: &callpbv1.Literal{Value: &callpbv1.Literal_Int{Int: 3}}},
			{Name: "list", Value: &callpbv1.Literal{Value: &callpbv1.Literal_List{List: &callpbv1.List{Values: []*callpbv1.Literal{
				{Value: &callpbv1.Literal_Bool{Bool: true}},
				{Value: &callpbv1.Literal_CallDigest{CallDigest: "xxh3:refarg"}},
			}}}}},
		},
		ImplicitInputs: []*callpbv1.Argument{strArg("cachePerSession", "sess-1")},
	}
	enc, err := pb.Encode()
	if err != nil {
		t.Fatal(err)
	}
	jsonl := toJSONL(t,
		rec(map[string]any{"spanId": idRoot, "parentId": idNone, "name": "root", "startNs": baseEp, "endNs": baseEnd}),
		rec(map[string]any{"spanId": idA, "parentId": idRoot, "name": "Container.withExec", "startNs": baseEp + 1, "endNs": baseEnd,
			"attrs": map[string]any{
				telemetry.DagDigestAttr: "xxh3:self",
				telemetry.DagCallAttr:   enc,
			}}),
	)
	g := buildGraphFromCompiled(t, mustCompile(t, jsonl))
	op := opByIdent(t, g, "xxh3:self")
	cs := op.CallSelf
	if cs == nil {
		t.Fatal("CallSelf must load from dag.call")
	}
	if cs.Field != "withExec" || cs.Receiver != "xxh3:recv" || cs.View != "v1" || cs.Nth != 2 {
		t.Fatalf("self fields: %+v", cs)
	}
	if cs.Module == nil || cs.Module.Ref != "github.com/x/m" || cs.Module.Pin != "abc" {
		t.Fatalf("module: %+v", cs.Module)
	}
	if len(cs.Args) != 3 || cs.Args[0].Value != `"linux/amd64"` || cs.Args[1].Value != "3" ||
		cs.Args[2].Value != "[true,xxh3:refarg]" {
		t.Fatalf("args renderings: %+v", cs.Args)
	}
	if len(cs.Implicit) != 1 || cs.Implicit[0].Name != "cachePerSession" || cs.Implicit[0].Value != `"sess-1"` {
		t.Fatalf("implicit renderings: %+v", cs.Implicit)
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
	if op := opByIdent(t, g, "xxh3:aaaa"); op.ScopeInputs != nil || op.ScopeCorrupt {
		t.Fatalf("absent dag.call must leave scope unrecorded (nil, not corrupt), got %#v corrupt=%v", op.ScopeInputs, op.ScopeCorrupt)
	}
	// Malformed is DISTINCT from absent: the sentinel survives the dump
	// round-trip and marks the op corrupt (review round 1, finding 3).
	if op := opByIdent(t, g, "xxh3:bbbb"); op.ScopeInputs != nil || !op.ScopeCorrupt {
		t.Fatalf("malformed dag.call must mark ScopeCorrupt with nil inputs, got %#v corrupt=%v", op.ScopeInputs, op.ScopeCorrupt)
	}
}
