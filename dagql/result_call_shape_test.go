package dagql

// The call shape and its literal digests (telemetryattrs.CacheShapeDigestAttr,
// CacheArgDigestsAttr, CacheArgCountAttr): what masks a literal, what changes
// a shape, redaction, nesting and the bounded fold.

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/zeebo/xxh3"
	"gotest.tools/v3/assert"

	"github.com/dagger/dagger/dagql/call"
)

func shapeTestString(s string) *ResultCallLiteral {
	return &ResultCallLiteral{Kind: ResultCallLiteralKindString, StringValue: s}
}

func shapeTestStrings(ss ...string) *ResultCallLiteral {
	list := &ResultCallLiteral{Kind: ResultCallLiteralKindList}
	for _, s := range ss {
		list.ListItems = append(list.ListItems, shapeTestString(s))
	}
	return list
}

// shapeTestExec is a withExec on a container, with argv as its args.
func shapeTestExec(argv ...string) *ResultCall {
	return &ResultCall{
		Kind:  ResultCallKindField,
		Type:  &ResultCallType{NamedType: "Container", NonNull: true},
		Field: "withExec",
		Receiver: &ResultCallRef{Call: &ResultCall{
			Kind:  ResultCallKindField,
			Type:  &ResultCallType{NamedType: "Container", NonNull: true},
			Field: "container",
		}},
		Args: []*ResultCallArg{{Name: "args", Value: shapeTestStrings(argv...)}},
	}
}

func shapeTestOf(t testing.TB, frame *ResultCall) callShape {
	t.Helper()
	shape, err := callShapeOf(frame)
	assert.NilError(t, err)
	return shape
}

// shapeTestEqual asserts a and b are the same shape and digests.
func shapeTestEqual(t testing.TB, a, b callShape) {
	t.Helper()
	assert.Equal(t, a.digest, b.digest)
	assert.Equal(t, a.count, b.count)
	assert.DeepEqual(t, a.args, b.args)
}

// shapeTestDiffer returns the positions where a and b's digests differ.
func shapeTestDiffer(a, b callShape) []int {
	var out []int
	n := len(a.args)
	if len(b.args) > n {
		n = len(b.args)
	}
	for i := range n {
		if i >= len(a.args) || i >= len(b.args) || a.args[i] != b.args[i] {
			out = append(out, i)
		}
	}
	return out
}

// A build whose flags carry a different commit has the same shape, and only
// that flag's position differs.
func TestCallShapeMasksLiterals(t *testing.T) {
	t.Parallel()
	link := func(commit string) *ResultCall {
		return shapeTestExec("go", "build", "-o", "./bin/", "-ldflags", "-X main.commit="+commit, "./cmd/engine")
	}
	a, b := shapeTestOf(t, link("abc123")), shapeTestOf(t, link("def456"))
	assert.Equal(t, a.digest, b.digest)
	assert.Assert(t, strings.HasPrefix(a.digest.String(), "xxh3:"))
	assert.Equal(t, 7, a.count)
	assert.Equal(t, 7, len(a.args))
	assert.DeepEqual(t, []int{5}, shapeTestDiffer(a, b))
	for _, dig := range a.args {
		assert.Equal(t, 16, len(dig))
		assert.Equal(t, strings.ToLower(dig), dig)
	}
	// A compile of the same argv length shares the shape and differs from the
	// link at the stable positions, so a service can tell them apart.
	compile := shapeTestOf(t, shapeTestExec("go", "build", "-o", "./bin/", "-tags", "netgo", "./pkgA"))
	assert.Equal(t, a.digest, compile.digest)
	assert.DeepEqual(t, []int{4, 5, 6}, shapeTestDiffer(a, compile))
	// Deterministic.
	shapeTestEqual(t, a, shapeTestOf(t, link("abc123")))
}

// What the shape covers, and what it leaves out.
func TestCallShapeCovers(t *testing.T) {
	t.Parallel()
	base := func() *ResultCall { return shapeTestExec("go", "build", "./x") }
	ref := func(id uint64) *ResultCallLiteral {
		return &ResultCallLiteral{Kind: ResultCallLiteralKindResultRef, ResultRef: &ResultCallRef{ResultID: id}}
	}
	witness := func(dig string) *ResultCallLiteral {
		return &ResultCallLiteral{Kind: ResultCallLiteralKindDigestedString, DigestedStringValue: "w", DigestedStringDigest: digest.FromString(dig)}
	}
	baseShape := shapeTestOf(t, base())
	for _, tc := range []struct {
		name    string
		mutate  func(*ResultCall)
		changes bool
	}{
		{name: "argv length", mutate: func(f *ResultCall) { f.Args[0].Value = shapeTestStrings("go", "build") }, changes: true},
		{name: "arg name", mutate: func(f *ResultCall) { f.Args[0].Name = "argv" }, changes: true},
		{name: "field", mutate: func(f *ResultCall) { f.Field = "withExecOther" }, changes: true},
		{name: "result type", mutate: func(f *ResultCall) { f.Type = &ResultCallType{NamedType: "Directory", NonNull: true} }, changes: true},
		{name: "receiver type", mutate: func(f *ResultCall) { f.Receiver.Call.Type = &ResultCallType{NamedType: "Directory", NonNull: true} }, changes: true},
		{name: "value kind", mutate: func(f *ResultCall) {
			f.Args[0].Value.ListItems[2] = &ResultCallLiteral{Kind: ResultCallLiteralKindInt, IntValue: 3}
		}, changes: true},
		{name: "module name", mutate: func(f *ResultCall) { f.Module = &ResultCallModule{Name: "other", Ref: "r", Pin: "p"} }, changes: true},
		{name: "nth", mutate: func(f *ResultCall) { f.Nth = 2 }, changes: true},
		{name: "view", mutate: func(f *ResultCall) { f.View = call.View("v2") }, changes: true},
		{name: "literal value", mutate: func(f *ResultCall) { f.Args[0].Value.ListItems[2].StringValue = "./y" }},
		{name: "receiver identity", mutate: func(f *ResultCall) { f.Receiver.Call.Field = "from" }},
		{name: "implicit inputs", mutate: func(f *ResultCall) {
			f.ImplicitInputs = []*ResultCallArg{{Name: "cachePerSession", Value: shapeTestString("session-1")}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := base()
			tc.mutate(f)
			got := shapeTestOf(t, f)
			assert.Equal(t, tc.changes, got.digest != baseShape.digest)
		})
	}

	// A result ref's identity and a digest witness's digest are structural
	// inputs: kind only, and no literal position.
	withInputs := func(id uint64, dig string) *ResultCall {
		f := base()
		f.Args = append(f.Args,
			&ResultCallArg{Name: "source", Value: ref(id)},
			&ResultCallArg{Name: "witness", Value: witness(dig)},
		)
		return f
	}
	a, b := shapeTestOf(t, withInputs(1, "a")), shapeTestOf(t, withInputs(2, "b"))
	assert.Equal(t, a.digest, b.digest)
	assert.DeepEqual(t, a.args, b.args)
	assert.Equal(t, baseShape.count, a.count)
	assert.Assert(t, a.digest != baseShape.digest, "the extra args are in the shape")

	// The module's ref and pin aren't, though its name is.
	module := func(ref, pin string) *ResultCall {
		f := base()
		f.Module = &ResultCallModule{Name: "build", Ref: ref, Pin: pin, ResultRef: &ResultCallRef{ResultID: 9}}
		return f
	}
	assert.Equal(t, shapeTestOf(t, module("github.com/x/y", "abc")).digest, shapeTestOf(t, module("github.com/x/y", "def")).digest)
}

// A sensitive argument's value never reaches the digests: it's redacted
// before the walk, as for the self digest. A secret-typed argument is a result
// ref, so it has no literal position at all.
func TestCallShapeRedactsSensitiveArgs(t *testing.T) {
	t.Parallel()
	withSecret := func(plaintext string) *ResultCall {
		f := shapeTestExec("sh", "-c", "true")
		f.Args = append(f.Args, &ResultCallArg{Name: "token", IsSensitive: true, Value: shapeTestString(plaintext)})
		return f
	}
	a, b := shapeTestOf(t, withSecret("hunter2")), shapeTestOf(t, withSecret("correct horse"))
	assert.Equal(t, a.digest, b.digest)
	assert.DeepEqual(t, a.args, b.args)
	// The position holds the redaction's digest: the same as a plain "***".
	redacted := shapeTestExec("sh", "-c", "true")
	redacted.Args = append(redacted.Args, &ResultCallArg{Name: "token", Value: shapeTestString("***")})
	assert.DeepEqual(t, shapeTestOf(t, redacted).args, a.args)
	// And no position holds the plaintext's digest.
	plain := shapeTestExec("sh", "-c", "true")
	plain.Args = append(plain.Args, &ResultCallArg{Name: "token", Value: shapeTestString("hunter2")})
	leaked := shapeTestOf(t, plain).args[3]
	for _, dig := range a.args {
		assert.Assert(t, dig != leaked)
	}

	// A sensitive list or object is redacted whole.
	withSecretList := func(vals ...string) *ResultCall {
		f := shapeTestExec("sh")
		f.Args = append(f.Args, &ResultCallArg{Name: "secrets", IsSensitive: true, Value: shapeTestStrings(vals...)})
		return f
	}
	shapeTestEqual(t, shapeTestOf(t, withSecretList("a", "b")), shapeTestOf(t, withSecretList("c", "d", "e")))

	// A secret-typed argument: an object ID, a result ref.
	withSecretRef := func(id uint64) *ResultCall {
		f := shapeTestExec("sh")
		f.Args = append(f.Args, &ResultCallArg{Name: "secret", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindResultRef, ResultRef: &ResultCallRef{ResultID: id}}})
		return f
	}
	s1, s2 := shapeTestOf(t, withSecretRef(1)), shapeTestOf(t, withSecretRef(2))
	shapeTestEqual(t, s1, s2)
	assert.Equal(t, 1, s1.count)
}

// Nested lists and objects list their literals in pre-order.
func TestCallShapeNestedPositions(t *testing.T) {
	t.Parallel()
	obj := func(name string, n int64) *ResultCallLiteral {
		return &ResultCallLiteral{Kind: ResultCallLiteralKindObject, ObjectFields: []*ResultCallArg{
			{Name: "name", Value: shapeTestString(name)},
			{Name: "n", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindInt, IntValue: n}},
		}}
	}
	frame := func(second string) *ResultCall {
		f := shapeTestExec("a")
		f.Args = append(f.Args, &ResultCallArg{Name: "items", Value: &ResultCallLiteral{
			Kind:      ResultCallLiteralKindList,
			ListItems: []*ResultCallLiteral{obj("x", 1), obj(second, 2)},
		}})
		return f
	}
	a, b := shapeTestOf(t, frame("y")), shapeTestOf(t, frame("z"))
	assert.Equal(t, 5, a.count)
	assert.Equal(t, a.digest, b.digest)
	assert.DeepEqual(t, []int{3}, shapeTestDiffer(a, b))
	// Equal bytes of different kinds have different digests.
	asString, asEnum := shapeTestExec("1"), shapeTestExec("1")
	asEnum.Args[0].Value.ListItems[0] = &ResultCallLiteral{Kind: ResultCallLiteralKindEnum, EnumValue: "1"}
	assert.Assert(t, shapeTestOf(t, asString).args[0] != shapeTestOf(t, asEnum).args[0])
}

// shapeTestLeaf is a literal's digest computed independently of the walker,
// from the documented encoding: xxh3-64(BE64(xxh3-64(value)) || kind).
func shapeTestLeaf(kind byte, value []byte) uint64 {
	var buf [9]byte
	binary.BigEndian.PutUint64(buf[:8], xxh3.Hash(value))
	buf[8] = kind
	return xxh3.Hash(buf[:])
}

func shapeTestHex(v uint64) string {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], v)
	return hex.EncodeToString(raw[:])
}

func shapeTestBE(v uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, v)
}

// Each literal's digest is the documented encoding of its value and kind.
func TestCallShapeLeafEncoding(t *testing.T) {
	t.Parallel()
	f := shapeTestExec("go")
	f.Args = append(f.Args,
		&ResultCallArg{Name: "n", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindInt, IntValue: -7}},
		&ResultCallArg{Name: "x", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindFloat, FloatValue: 1.5}},
		&ResultCallArg{Name: "on", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindBool, BoolValue: true}},
		&ResultCallArg{Name: "off", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindBool}},
		&ResultCallArg{Name: "mode", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindEnum, EnumValue: "FAST"}},
		&ResultCallArg{Name: "raw", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindBytes, BytesValue: []byte{0, 1, 2}}},
	)
	n := make([]byte, 8)
	negative := int64(-7)
	binary.BigEndian.PutUint64(n, uint64(negative))
	x := make([]byte, 8)
	binary.BigEndian.PutUint64(x, math.Float64bits(1.5))
	want := []string{
		shapeTestHex(shapeTestLeaf('6', []byte("go"))),
		shapeTestHex(shapeTestLeaf('4', n)),
		shapeTestHex(shapeTestLeaf('5', x)),
		shapeTestHex(shapeTestLeaf('2', []byte{1})),
		shapeTestHex(shapeTestLeaf('2', []byte{0})),
		shapeTestHex(shapeTestLeaf('3', []byte("FAST"))),
		shapeTestHex(shapeTestLeaf('A', []byte{0, 1, 2})),
	}
	got := shapeTestOf(t, f)
	assert.Equal(t, len(want), got.count)
	assert.DeepEqual(t, want, got.args)
}

// Past 32 literals, the last position folds the rest: deterministically, in
// order, and only the folded position changes when a folded literal does.
func TestCallShapeFold(t *testing.T) {
	t.Parallel()
	argv := func(n int, change map[int]string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("arg-%d", i)
			if v, ok := change[i]; ok {
				out[i] = v
			}
		}
		return out
	}
	for _, n := range []int{callShapeArgLimit - 1, callShapeArgLimit, callShapeArgLimit + 1, 40} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			t.Parallel()
			base := shapeTestOf(t, shapeTestExec(argv(n, nil)...))
			assert.Equal(t, n, base.count)
			assert.Equal(t, min(n, callShapeArgLimit), len(base.args))
			shapeTestEqual(t, base, shapeTestOf(t, shapeTestExec(argv(n, nil)...)))
			last := n - 1
			changed := shapeTestOf(t, shapeTestExec(argv(n, map[int]string{last: "changed"})...))
			assert.DeepEqual(t, []int{min(last, callShapeArgLimit-1)}, shapeTestDiffer(base, changed))
		})
	}
	// Independently computed: at exactly 32 literals the last position is its
	// own literal's digest; past 32 it is one xxh3 over the big-endian digests
	// of every literal from the 32nd on.
	leaves := func(n int) []uint64 {
		out := make([]uint64, n)
		for i, arg := range argv(n, nil) {
			out[i] = shapeTestLeaf('6', []byte(arg))
		}
		return out
	}
	exact := shapeTestOf(t, shapeTestExec(argv(callShapeArgLimit, nil)...))
	for i, leaf := range leaves(callShapeArgLimit) {
		assert.Equal(t, shapeTestHex(leaf), exact.args[i])
	}
	for _, n := range []int{callShapeArgLimit + 1, 40} {
		folded := shapeTestOf(t, shapeTestExec(argv(n, nil)...))
		all := leaves(n)
		var rest []byte
		for _, leaf := range all[callShapeArgLimit-1:] {
			rest = append(rest, shapeTestBE(leaf)...)
		}
		for i := range callShapeArgLimit - 1 {
			assert.Equal(t, shapeTestHex(all[i]), folded.args[i])
		}
		assert.Equal(t, shapeTestHex(xxh3.Hash(rest)), folded.args[callShapeArgLimit-1], "%d literals", n)
	}

	// The fold is order-sensitive: swapping two folded literals changes it.
	a := argv(callShapeArgLimit+1, nil)
	b := argv(callShapeArgLimit+1, map[int]string{callShapeArgLimit - 1: a[callShapeArgLimit], callShapeArgLimit: a[callShapeArgLimit-1]})
	sa, sb := shapeTestOf(t, shapeTestExec(a...)), shapeTestOf(t, shapeTestExec(b...))
	assert.DeepEqual(t, []int{callShapeArgLimit - 1}, shapeTestDiffer(sa, sb))
	// No literals: no digests.
	none := shapeTestOf(t, &ResultCall{Kind: ResultCallKindField, Type: &ResultCallType{NamedType: "Query"}, Field: "container"})
	assert.Equal(t, 0, none.count)
	assert.Assert(t, none.args == nil)
}

// The carrier gets the shape on every decided outcome but uncached, and the
// shape changes no decision.
func TestCacheEvidenceShape(t *testing.T) {
	t.Parallel()
	ctx, c := cacheTestEvidenceEnv(t)
	frame := cacheTestIntCall("evidence-shape")
	frame.Args = []*ResultCallArg{{Name: "argv", Value: shapeTestStrings("go", "build", "./x")}}
	want := shapeTestOf(t, frame)
	run := func(req *CallRequest) AnyResult {
		t.Helper()
		res, err := c.GetOrInitCall(ctx, "test-session", noopTypeResolver{}, req, func(context.Context) (AnyResult, error) {
			return cacheTestIntResult(frame, 1), nil
		})
		assert.NilError(t, err)
		return res
	}
	executed := cacheTestArmedRequest(frame)
	first := run(executed)
	assert.Equal(t, CacheOutcomeExecuted, executed.CacheEvidence.Outcome)
	assert.Equal(t, want.digest, executed.CacheEvidence.ShapeDigest)
	assert.DeepEqual(t, want.args, executed.CacheEvidence.ArgDigests)
	assert.Equal(t, 3, executed.CacheEvidence.ArgCount)

	hit := cacheTestArmedRequest(frame)
	second := run(hit)
	assert.Equal(t, CacheOutcomeHit, hit.CacheEvidence.Outcome)
	assert.Equal(t, want.digest, hit.CacheEvidence.ShapeDigest)
	unarmed := run(&CallRequest{ResultCall: frame})
	assert.Equal(t, first.cacheSharedResult().id, second.cacheSharedResult().id)
	assert.Equal(t, first.cacheSharedResult().id, unarmed.cacheSharedResult().id)

	uncached := cacheTestArmedRequest(cacheTestIntCall("evidence-shape-uncached"))
	uncached.DoNotCache = true
	run(uncached)
	assert.Equal(t, CacheOutcomeUncached, uncached.CacheEvidence.Outcome)
	assert.Equal(t, digest.Digest(""), uncached.CacheEvidence.ShapeDigest)
	assert.Assert(t, uncached.CacheEvidence.ArgDigests == nil)
}

// BenchmarkCallShape times the shape pass against the self-digest derivation
// on the same frames: a 24-argument withExec, a 40-literal call that folds,
// and a call with a 1 MiB string literal. The gates are the shape pass's
// ns/op at most the self digest's on each, at most 3 µs on the withExec, and
// its allocs/op.
func BenchmarkCallShape(b *testing.B) {
	ctx := cacheTestContext(b.Context())
	c, err := NewCache(ctx, "", nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	argv := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("--flag-%d=value-%d", i, i)
		}
		return out
	}
	large := shapeTestExec("sh", "-c")
	large.Args = append(large.Args, &ResultCallArg{Name: "contents", Value: shapeTestString(strings.Repeat("x", 1<<20))})
	for _, tc := range []struct {
		name  string
		frame *ResultCall
	}{
		{name: "withExec-24", frame: shapeTestExec(argv(24)...)},
		{name: "fold-40", frame: shapeTestExec(argv(40)...)},
		{name: "literal-1MiB", frame: large},
	} {
		b.Run(tc.name+"/shape", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := callShapeOf(tc.frame); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(tc.name+"/self", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := tc.frame.selfDigestAndInputRefs(c); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
