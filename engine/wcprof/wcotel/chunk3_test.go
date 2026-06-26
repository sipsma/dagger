package wcotel

// Chunk 3 fixture (design §6.5 lazy re-point fidelity + §6.2 oracle): the lazy /
// deferred-evaluation choke point. It builds otlpdump-shaped spans mirroring
// EXACTLY what the engine emit (dagql beginOTelLazyOp + the wcprof.parent stamping
// processor + emitOTelWait) produces for a pending result forced by concurrent
// consumers — a lazy op under the consumer, the deferred work re-pointed under the
// producer for the UI but carrying wcprof.parent = the lazy op, and joiner/leader
// wait links — runs them through the loader + structural gate + replay, and
// compares against the equivalent native wcprof IR.
//
// The five §6.5 assertions: (1) UI parentage unchanged, (2) causal re-home on the
// DIRECT child only, (3) no double-count, (4) the consumer's critical path
// includes the eval, (5) Invariant T. (1) and (2)-at-the-span-level are also
// machine-checked end-to-end against the *real* emit in dagql's
// otelprof_lazy_test.go; here we exercise the loaded-graph + replay + oracle.

import (
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// span ids beyond the loader_test.go set.
const (
	idWExec = "eeeeeeeeeeeeeeee"
	idC2    = "ffffffffffffffff"
)

// lazyOpAttrs are the `lazy` op span attributes beginOTelLazyOp emits (the resume
// span in the producer case, or the hidden span in the no-producer case).
func lazyOpAttrs() map[string]any {
	return map[string]any{
		telemetryattrs.WcprofOpKindAttr: wcprof.OpKindLazy.String(),
		telemetry.UIPassthroughAttr:     true,
	}
}

// repointedCallAttrs are a deferred-work direct child's attributes: its parentId
// is the producer (the UI re-point, unchanged), and wcprof.parent is the lazy op
// stamped by the processor (the causal parent the loader uses).
func repointedCallAttrs(digest, lazyOpSpanID string) map[string]any {
	return map[string]any{
		telemetry.DagDigestAttr:         digest,
		telemetryattrs.WcprofParentAttr: lazyOpSpanID,
	}
}

// TestChunk3LazyRepointFidelity is the load-bearing §6.5 fixture. A pending
// Directory produced by O is forced by a leader consumer C1 and a joiner C2; the
// deferred work (a withNewFile call whose call_exec is the real cost) renders
// under O but is causally re-homed to the lazy op L. It asserts all five §6.5
// properties on the loaded graph + replay, and that the OTel source agrees with
// the native IR (§6.2).
func TestChunk3LazyRepointFidelity(t *testing.T) {
	const (
		dO  = "sha256:producer"
		dW  = "sha256:deferred-work"
		dC1 = "sha256:consumer-export"
		dC2 = "sha256:consumer-digest"
	)
	// Timings (ms): a sequential producer (O) then the consumer chain
	// C1 -> L(resume) -> WCALL(withNewFile) -> WEXEC(call_exec, the 114ms cost);
	// C2 joins the in-flight eval. The waiters are pure waits (≈0 self) so the
	// deferred work is counted exactly once.
	otelRecs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 132, nil),
		// producer O: returned the pending result and ended early (10ms).
		otSpan(idA, idRoot, "Query.directory", 0, 10, callAttrs(dO)),
		// leader consumer C1: triggers the eval and blocks on the lazy op.
		otSpan(idB, idRoot, "Directory.export", 10, 130, callAttrs(dC1),
			otWait(idLazy, "lazy", 10, 130)),
		// the lazy op L (the resume span): nests under the consumer, passthrough.
		otSpan(idLazy, idB, "resume directory", 11, 129, lazyOpAttrs()),
		// deferred work direct child WCALL: parentId = producer O (UI re-point),
		// but wcprof.parent = the lazy op L (causal).
		otSpan(idExec, idA, "Directory.withNewFile", 12, 128, repointedCallAttrs(dW, idLazy)),
		// the work's call_exec WEXEC: a DESCENDANT (parentId = WCALL, no
		// wcprof.parent) — it must NOT re-home to L. The 114ms bottleneck.
		otSpan(idWExec, idExec, "Directory.withNewFile", 13, 127, callExecAttrs(dW)),
		// joiner consumer C2: joins the in-flight eval, blocks on the lazy op.
		otSpan(idC2, idRoot, "Directory.digest", 15, 130, callAttrs(dC2),
			otWait(idLazy, "lazy", 15, 130)),
	}

	c := mustCompile(t, toJSONL(t, otelRecs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("otel build: %v", err)
	}

	// (5) Invariant T + §6.1 gate: every lazy wait resolves to the lazy op (no
	// unresolved/malformed targets), no cycle, no self>makespan. A joiner that
	// arrived before the goroutine would have created the resume span still gets a
	// valid target because the engine mints it under lazyMu before publishing
	// lazyEvalWaitCh; here that shows up as both waits resolving.
	gate := mustGate(t, c, g)
	if gate.WaitEdges != 2 {
		t.Fatalf("want 2 lazy wait edges (leader + joiner), got %d", gate.WaitEdges)
	}
	if gate.UnresolvedWaitTargets != 0 || gate.MalformedWaitTimings != 0 {
		t.Fatalf("Invariant T: every lazy wait must resolve with valid timing; unresolved=%d malformed=%d",
			gate.UnresolvedWaitTargets, gate.MalformedWaitTimings)
	}
	if gate.Cycles != 0 {
		t.Fatalf("the lazy cycle risk (design §2.5) must be closed: CycleWarnings=%d", gate.Cycles)
	}

	lazyOp := opByClassKind(g, wcprof.OpKindLazy.String(), "resume directory")
	work := opByClassKind(g, wcprof.OpKindCall.String(), "Directory.withNewFile")
	workExec := opByClassKind(g, wcprof.OpKindCallExec.String(), "Directory.withNewFile")
	producer := opByClassKind(g, wcprof.OpKindCall.String(), "Query.directory")
	if lazyOp == nil || work == nil || workExec == nil || producer == nil {
		t.Fatalf("missing ops: lazy=%v work=%v workExec=%v producer=%v", lazyOp, work, workExec, producer)
	}

	// (2) causal re-home — DIRECT child only.
	if work.Parent != lazyOp {
		t.Fatalf("the direct work span must re-home to the lazy op via wcprof.parent; got parent %v", work.Parent)
	}
	if workExec.Parent != work {
		t.Fatalf("the descendant call_exec must stay under the work span by parentId (not re-homed to the lazy op); got parent %v", workExec.Parent)
	}

	// (1) UI parentage unchanged: the loader read wcprof.parent (=lazy op), NOT
	// the deferred work's UI parentId (=producer). So the producer has the work as
	// neither a causal child here, while the *input* span kept parentId=producer
	// (the dagql emit-path test asserts the unchanged parentId on real spans).
	if len(producer.Children) != 0 {
		t.Fatalf("(no double-count) producer must have no causal children — the work re-homed to the lazy op; got %d", len(producer.Children))
	}

	// (3) no double-count.
	const msNS = int64(ms)
	if producer.SelfNS() != 10*msNS {
		t.Fatalf("producer self must be its own ~10ms, excluding the 114ms work; got %dns", producer.SelfNS())
	}
	if workExec.SelfNS() < 100*msNS {
		t.Fatalf("the deferred work's call_exec must carry the real cost (~114ms); got %dns", workExec.SelfNS())
	}
	if lazyOp.SelfNS() > 10*msNS {
		t.Fatalf("the lazy op's self-time must be ~0 (the work is its child, not inflated into it); got %dns", lazyOp.SelfNS())
	}
	if lazyOp.SelfNS()*5 >= workExec.SelfNS() {
		t.Fatalf("lazy op self (%dns) must be far smaller than the work it contains (%dns)", lazyOp.SelfNS(), workExec.SelfNS())
	}
	var sumSelf int64
	for _, op := range g.Ops {
		sumSelf += op.SelfNS()
	}
	if makespan := wcanalyze.ActualMakespanNS(g); sumSelf > makespan {
		t.Fatalf("(no double-count) total self-time %dns must not exceed makespan %dns", sumSelf, makespan)
	}

	// (4) consumer critical path includes the eval: scaling the work's REAL class
	// (the call_exec it ran) shortens the consumer's finish; scaling a generic
	// lazy class (self ≈ 0) does not.
	_, ranked, err := TopBottlenecks(g, 0.5, 0)
	if err != nil {
		t.Fatalf("what-ifs: %v", err)
	}
	execSaved := savedForKey(ranked, wcanalyze.ClassKey{Kind: wcprof.OpKindCallExec.String(), Class: "Directory.withNewFile"})
	lazySaved := savedForKey(ranked, wcanalyze.ClassKey{Kind: wcprof.OpKindLazy.String(), Class: "resume directory"})
	if execSaved <= 0 {
		t.Fatalf("scaling the work's real (call_exec) class must shorten the consumer critical path; saved=%dns", execSaved)
	}
	if lazySaved*5 >= execSaved {
		t.Fatalf("scaling the generic lazy class (self ≈ 0) must save far less than the real work; lazy=%dns exec=%dns", lazySaved, execSaved)
	}

	// (§6.2) cross-source oracle: the native ground-truth IR for the same run —
	// native re-points the work under the lazy op via its own context (not OTel's
	// re-point), classes the lazy op by the producing field, and records the same
	// leader+joiner lazy waits.
	nat := newNativeIR()
	const (
		nRoot  uint64 = 1
		nO     uint64 = 2
		nC1    uint64 = 3
		nLazy  uint64 = 4
		nWCall uint64 = 5
		nWExec uint64 = 6
		nC2    uint64 = 7
	)
	nat.op(nRoot, 0, "", "POST /query", 0, 132, wcprof.OutcomeOK.String())
	nat.op(nO, nRoot, wcprof.OpKindCall.String(), "Query.directory", 0, 10, wcprof.OutcomeExecuted.String())
	nat.op(nC1, nRoot, wcprof.OpKindCall.String(), "Directory.export", 10, 130, wcprof.OutcomeExecuted.String())
	// native classes the lazy op by the producing field, NOT "resume directory" —
	// the benign class divergence (§3.2): it has ~0 self and never ranks.
	nat.op(nLazy, nC1, wcprof.OpKindLazy.String(), "Directory.directory", 11, 129, wcprof.OutcomeOK.String())
	nat.op(nWCall, nLazy, wcprof.OpKindCall.String(), "Directory.withNewFile", 12, 128, wcprof.OutcomeExecuted.String())
	nat.op(nWExec, nWCall, wcprof.OpKindCallExec.String(), "Directory.withNewFile", 13, 127, wcprof.OutcomeExecuted.String())
	nat.op(nC2, nRoot, wcprof.OpKindCall.String(), "Directory.digest", 15, 130, wcprof.OutcomeJoined.String())
	nat.wait(nC1, nLazy, wcprof.WaitReasonLazy.String(), 10, 130)
	nat.wait(nC2, nLazy, wcprof.WaitReasonLazy.String(), 15, 130)
	nativeG := nat.graph(t)

	// filter the ~0-self lazy op (whose class label differs by construction) so the
	// ranking compares the bottleneck classes both sources agree on.
	const minSelf = 5 * int64(ms)
	cmp, err := Oracle(nativeG, g, 0.5, 5, minSelf)
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	if !cmp.Agrees(1.0, 0.01) {
		t.Fatalf("native↔OTel must converge on the lazy-heavy ranking: jaccard=%.2f drift=%.2f native-only=%v otel-only=%v",
			cmp.JaccardTopN(), cmp.MaxRelDrift(), cmp.NativeOnly, cmp.OTelOnly)
	}
	// identity-level: the deferred work's call_exec self-time matches native.
	if nWork := opByClassKind(nativeG, wcprof.OpKindCallExec.String(), "Directory.withNewFile"); nWork == nil || nWork.SelfNS() != workExec.SelfNS() {
		t.Fatalf("identity-level: native vs OTel call_exec self-time must match; native=%v otel=%dns", nWork, workExec.SelfNS())
	}
}

func savedForKey(ranked []ClassImpact, key wcanalyze.ClassKey) int64 {
	for _, r := range ranked {
		if r.Key == key {
			return r.SavedNS
		}
	}
	return 0
}
