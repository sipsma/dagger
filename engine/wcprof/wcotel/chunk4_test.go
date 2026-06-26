package wcotel

// Chunk 4 fixtures (design §3.3 exec engine/user split + §3.4 services; §6.5
// fixtures + §6.2 oracle). They build otlpdump-shaped spans mirroring EXACTLY
// what the engine emit produces — the exec.run + containerStart/processRun split
// (engineutil/otelprof.go) and the service.start span + installer wait edge
// (core/services.go) — run them through the loader + structural gate + replay,
// and compare against the equivalent native wcprof IR.
//
// The shape↔emit correspondence is separately machine-checked against the REAL
// emit in engine/engineutil/otelprof_test.go and core/otelprof_services_test.go;
// here we exercise the loaded-graph + replay + oracle (the counterfactual
// ranking the product ships).

import (
	"testing"

	telemetry "github.com/dagger/otel-go"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// span ids beyond the loader_test.go / chunk3_test.go sets.
const (
	idERun    = "1010101010101010" // exec.run
	idCStart  = "2020202020202020" // exec.containerStart
	idPRun    = "3030303030303030" // exec.processRun
	idInstA   = "4040404040404040" // first service installer
	idSvcStart = "5050505050505050" // service.start
	idSvcSpan = "6060606060606060" // long-lived service availability span (OTel-only)
	idDRun    = "7070707070707070" // daemon exec.run
	idDPRun   = "8080808080808080" // daemon exec.processRun (idle)
	idInstB   = "9090909090909090" // second installer (joiner)
	idCExec   = "a1a1a1a1a1a1a1a1" // consumer call_exec (on-path user work)
)

// execRunAttrs are the exec.run span attributes beginOTelExecRun emits.
func execRunAttrs(ident string) map[string]any {
	return map[string]any{
		telemetryattrs.WcprofOpKindAttr: wcprof.OpKindExec.String(),
		telemetry.DagDigestAttr:         ident,
		telemetry.UIPassthroughAttr:     true,
	}
}

// execPhaseAttrs are an exec phase span's attributes (emitOTelExecPhase): user
// adds work_type=user (the processRun phase); engine omits it (containerStart).
func execPhaseAttrs(ident string, user bool) map[string]any {
	m := map[string]any{
		telemetryattrs.WcprofOpKindAttr: wcprof.OpKindExecPhase.String(),
		telemetry.DagDigestAttr:         ident,
		telemetry.UIPassthroughAttr:     true,
	}
	if user {
		m[telemetryattrs.WcprofWorkTypeAttr] = wcprof.WorkTypeUser.String()
	}
	return m
}

// serviceStartAttrs are the service.start span attributes beginOTelServiceStart emits.
func serviceStartAttrs(ident string) map[string]any {
	return map[string]any{
		telemetryattrs.WcprofOpKindAttr: wcprof.OpKindServiceStart.String(),
		telemetry.DagDigestAttr:         ident,
		telemetry.UIPassthroughAttr:     true,
	}
}

// opUser appends a user-work native op (the processRun phase carries
// WorkType=user; nativeIR.op defaults to engine).
func (b *nativeIR) opUser(id, parent uint64, kind, class string, startMS, endMS int64, outcome string) {
	b.events = append(b.events, wcprof.DumpEvent{
		Type: "op", OpKind: kind, WorkType: wcprof.WorkTypeUser.String(), Outcome: outcome,
		OpID: id, ParentID: parent, ClassID: b.str.intern(class),
		StartNS: startMS * ms, EndNS: endMS * ms,
	})
}

// TestChunk4ExecSplitFidelity is the §6.5 withExec delayed-setup-vs-runtime
// fixture. A withExec runs under a call_exec; its exec.run is split into engine
// containerStart and user processRun. Two sub-cases (slow user process vs slow
// engine setup) assert the headline correctly fingers whichever is slow — the
// north-star being a slow USER process headlining as work_type=user — and the
// OTel ranking converges with the native IR (§6.2).
func TestChunk4ExecSplitFidelity(t *testing.T) {
	const dExec = "sha256:withexec-digest"
	cases := []struct {
		name      string
		setupEnd  int64 // containerStart = [10, setupEnd]; processRun = [setupEnd, 90]
		wantClass string
		wantUser  bool
	}{
		// the north star: a slow user process (sleep/go build) headlines as user work.
		{"slow user process headlines as user/processRun", 20, "exec.processRun", true},
		// the inverse: slow engine container-setup headlines as engine overhead.
		{"slow engine setup headlines as containerStart", 80, "exec.containerStart", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			otelRecs := []map[string]any{
				otSpan(idRoot, idNone, "POST /query", 0, 100, nil),
				// the visible withExec caller, blocked on its shared execution.
				otSpan(idA, idRoot, "Container.withExec", 5, 95, callAttrs(dExec), otWait(idExec, "call_exec", 8, 92)),
				// the shared execution (Chunk 2).
				otSpan(idExec, idA, "Container.withExec", 8, 92, callExecAttrs(dExec)),
				// the executor run, under call_exec (Chunk 4 §3.3).
				otSpan(idERun, idExec, "exec.run", 10, 90, execRunAttrs(dExec)),
				// engine container-setup phase.
				otSpan(idCStart, idERun, "exec.containerStart", 10, tc.setupEnd, execPhaseAttrs(dExec, false)),
				// user process phase (work_type=user).
				otSpan(idPRun, idERun, "exec.processRun", tc.setupEnd, 90, execPhaseAttrs(dExec, true)),
			}
			c := mustCompile(t, toJSONL(t, otelRecs...))
			g, err := wcanalyze.Build(c.Header, c.Events)
			if err != nil {
				t.Fatalf("otel build: %v", err)
			}
			gate := mustGate(t, c, g)
			if gate.WaitEdges != 1 {
				t.Fatalf("want 1 call_exec wait edge, got %d", gate.WaitEdges)
			}

			// the loaded split carries the right kinds + work_type.
			cs := opByClassKind(g, wcprof.OpKindExecPhase.String(), "exec.containerStart")
			pr := opByClassKind(g, wcprof.OpKindExecPhase.String(), "exec.processRun")
			run := opByClassKind(g, wcprof.OpKindExec.String(), "exec.run")
			if cs == nil || pr == nil || run == nil {
				t.Fatalf("missing exec ops: containerStart=%v processRun=%v exec.run=%v", cs, pr, run)
			}
			if cs.Parent != run || pr.Parent != run {
				t.Fatal("containerStart/processRun must nest under exec.run")
			}
			if pr.WorkType != wcprof.WorkTypeUser.String() {
				t.Fatalf("processRun work_type = %q, want user", pr.WorkType)
			}
			if cs.WorkType == wcprof.WorkTypeUser.String() {
				t.Fatal("containerStart must stay engine, not user")
			}

			// the headline: the slow phase tops RunWhatIfs.
			_, ranked, err := TopBottlenecks(g, 0.5, 0)
			if err != nil {
				t.Fatalf("what-ifs: %v", err)
			}
			if len(ranked) == 0 {
				t.Fatal("no bottleneck classes ranked")
			}
			top := ranked[0]
			if top.Key.Kind != wcprof.OpKindExecPhase.String() || top.Key.Class != tc.wantClass {
				t.Fatalf("headline must be the slow phase {exec_phase,%s}; got {%s,%s} (saved=%dns)",
					tc.wantClass, top.Key.Kind, top.Key.Class, top.SavedNS)
			}
			// the north-star: when the user process is slow, the headline op is user work.
			if tc.wantUser {
				headOp := opByClassKind(g, wcprof.OpKindExecPhase.String(), tc.wantClass)
				if headOp.WorkType != wcprof.WorkTypeUser.String() {
					t.Fatalf("user-work-first-class: the headline processRun must carry work_type=user, got %q", headOp.WorkType)
				}
			}

			// (§6.2) cross-source oracle: the native IR for the same run.
			nat := newNativeIR()
			const (
				nRoot  uint64 = 1
				nCall  uint64 = 2
				nExec  uint64 = 3
				nERun  uint64 = 4
				nCS    uint64 = 5
				nPR    uint64 = 6
			)
			nat.op(nRoot, 0, "", "POST /query", 0, 100, wcprof.OutcomeOK.String())
			nat.op(nCall, nRoot, wcprof.OpKindCall.String(), "Container.withExec", 5, 95, wcprof.OutcomeExecuted.String())
			nat.op(nExec, nCall, wcprof.OpKindCallExec.String(), "Container.withExec", 8, 92, wcprof.OutcomeExecuted.String())
			nat.op(nERun, nExec, wcprof.OpKindExec.String(), "exec.run", 10, 90, wcprof.OutcomeOK.String())
			nat.op(nCS, nERun, wcprof.OpKindExecPhase.String(), "exec.containerStart", 10, tc.setupEnd, wcprof.OutcomeOK.String())
			nat.opUser(nPR, nERun, wcprof.OpKindExecPhase.String(), "exec.processRun", tc.setupEnd, 90, wcprof.OutcomeOK.String())
			nat.wait(nCall, nExec, wcprof.WaitReasonCallExec.String(), 8, 92)
			nativeG := nat.graph(t)

			cmp, err := Oracle(nativeG, g, 0.5, 5, 0)
			if err != nil {
				t.Fatalf("oracle: %v", err)
			}
			if !cmp.Agrees(1.0, 0.01) {
				t.Fatalf("native↔OTel must converge on the exec-split ranking: jaccard=%.2f drift=%.2f native-only=%v otel-only=%v",
					cmp.JaccardTopN(), cmp.MaxRelDrift(), cmp.NativeOnly, cmp.OTelOnly)
			}
			// identity-level: the slow phase's self-time matches native.
			natPhase := opByClassKind(nativeG, wcprof.OpKindExecPhase.String(), tc.wantClass)
			otelPhase := opByClassKind(g, wcprof.OpKindExecPhase.String(), tc.wantClass)
			if natPhase.SelfNS() != otelPhase.SelfNS() {
				t.Fatalf("identity-level: native vs OTel %s self-time must match; native=%dns otel=%dns",
					tc.wantClass, natPhase.SelfNS(), otelPhase.SelfNS())
			}
		})
	}
}

// TestChunk4ServicesFidelity is the §3.4 services fixture. A service-dependent
// workload: a first installer triggers a service start; a second installer blocks
// on it and credits a `service` wait edge to the service.start span; the daemon
// then idles for most of the run; a consumer's own resolver (call_exec) is the
// real on-critical-path work. It asserts the installer wait resolves to
// service.start (Invariant T / §6.1), the idle daemon does NOT rank despite having
// the most self-time in the graph, the OTel-only long-lived availability span does
// not inflate, and the OTel ranking converges with the native IR (§6.2).
func TestChunk4ServicesFidelity(t *testing.T) {
	const (
		dSvc      = "sha256:service-digest"
		dDaemon   = "sha256:daemon-exec"
		dConsumer = "sha256:consumer-digest"
	)
	// idle daemon: exec.processRun [16,121] = 105ms, the single largest self-time.
	// consumer call_exec: [62,127] = 65ms, the real bottleneck (installer waited
	// for the service to be up, then ran this). makespan = root end (130).
	otelRecs := []map[string]any{
		otSpan(idRoot, idNone, "POST /query", 0, 130, nil),
		// installer A triggers + is synchronously blocked in the start (nesting, no wait).
		otSpan(idInstA, idRoot, "Container.asService", 5, 64, callAttrs(dSvc)),
		// the service.start op: the start + health-check window.
		otSpan(idSvcStart, idInstA, "service.start", 8, 60, serviceStartAttrs(dSvc)),
		// the OTel-only long-lived availability span (exec <args>, passthrough):
		// its child daemon run absorbs the idle, so its own self-time is tiny.
		otSpan(idSvcSpan, idSvcStart, "exec daemon-cmd", 10, 122, map[string]any{telemetry.UIPassthroughAttr: true}),
		// the daemon container: idle for most of the run.
		otSpan(idDRun, idSvcSpan, "exec.run", 11, 121, execRunAttrs(dDaemon)),
		otSpan(idDPRun, idDRun, "exec.processRun", 16, 121, execPhaseAttrs(dDaemon, true)),
		// installer B joins the in-flight start: blocks on service.start, then runs.
		otSpan(idInstB, idRoot, "Container.withServiceBinding", 40, 128, callAttrs(dConsumer),
			otWait(idSvcStart, "service", 45, 60)),
		// the consumer's own resolver: the real on-critical-path user work.
		otSpan(idCExec, idInstB, "Container.stdout", 62, 127, callExecAttrs(dConsumer)),
	}
	c := mustCompile(t, toJSONL(t, otelRecs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("otel build: %v", err)
	}

	// §6.1 + Invariant T: the installer's service wait resolves to service.start.
	gate := mustGate(t, c, g)
	if gate.WaitEdges != 1 {
		t.Fatalf("want 1 service wait edge, got %d", gate.WaitEdges)
	}
	if gate.UnresolvedWaitTargets != 0 || gate.MalformedWaitTimings != 0 {
		t.Fatalf("the installer service wait must resolve to service.start: unresolved=%d malformed=%d",
			gate.UnresolvedWaitTargets, gate.MalformedWaitTimings)
	}
	svcStart := opByClassKind(g, wcprof.OpKindServiceStart.String(), "service.start")
	instB := opByClassKind(g, wcprof.OpKindCall.String(), "Container.withServiceBinding")
	if svcStart == nil || instB == nil {
		t.Fatalf("missing ops: service.start=%v instB=%v", svcStart, instB)
	}
	if len(instB.Waits) != 1 || instB.Waits[0].Target != svcStart {
		t.Fatalf("installer B must carry one service wait targeting service.start; got %v", instB.Waits)
	}

	// the idle daemon has the most self-time in the graph...
	daemonPR := opByClassKind(g, wcprof.OpKindExecPhase.String(), "exec.processRun")
	consumer := opByClassKind(g, wcprof.OpKindCallExec.String(), "Container.stdout")
	if daemonPR == nil || consumer == nil {
		t.Fatalf("missing ops: daemonProcessRun=%v consumer=%v", daemonPR, consumer)
	}
	if daemonPR.SelfNS() <= consumer.SelfNS() {
		t.Fatalf("precondition: the idle daemon (%dns) should have more self-time than the consumer (%dns)",
			daemonPR.SelfNS(), consumer.SelfNS())
	}

	// ...yet it does NOT rank: nothing waits for its end and it is not
	// makespan-defining, so scaling it saves no makespan, while the consumer's
	// on-critical-path work does. This is the "total time ≠ bottleneck" proof.
	_, ranked, err := TopBottlenecks(g, 0.5, 0)
	if err != nil {
		t.Fatalf("what-ifs: %v", err)
	}
	daemonSaved := savedForKey(ranked, wcanalyze.ClassKey{Kind: wcprof.OpKindExecPhase.String(), Class: "exec.processRun"})
	consumerSaved := savedForKey(ranked, wcanalyze.ClassKey{Kind: wcprof.OpKindCallExec.String(), Class: "Container.stdout"})
	if daemonSaved != 0 {
		t.Fatalf("the idle daemon must NOT rank (off critical path); got saved=%dns", daemonSaved)
	}
	if consumerSaved <= 0 {
		t.Fatalf("the consumer's on-path work must rank; got saved=%dns", consumerSaved)
	}
	// the OTel-only long-lived availability span must not inflate into a bottleneck.
	svcSpanSaved := savedForKey(ranked, wcanalyze.ClassKey{Kind: "", Class: "exec daemon-cmd"})
	if svcSpanSaved != 0 {
		t.Fatalf("the long-lived service availability span must not rank; got saved=%dns", svcSpanSaved)
	}

	// (§6.2) cross-source oracle: native has the same shape MINUS the OTel-only
	// availability span (native nests the daemon exec.run directly under
	// service.start). Scope to the meaningful bottlenecks via minSelf so the tiny
	// start/installer ops (and the OTel-only span) don't enter the comparison —
	// the irreducible native-vs-OTel structural difference (§6.2 scope-matching).
	nat := newNativeIR()
	const (
		nRoot uint64 = 1
		nInstA uint64 = 2
		nSvc  uint64 = 3
		nDRun uint64 = 4
		nDPR  uint64 = 5
		nInstB uint64 = 6
		nCExec uint64 = 7
	)
	nat.op(nRoot, 0, "", "POST /query", 0, 130, wcprof.OutcomeOK.String())
	nat.op(nInstA, nRoot, wcprof.OpKindCall.String(), "Container.asService", 5, 64, wcprof.OutcomeExecuted.String())
	nat.op(nSvc, nInstA, wcprof.OpKindServiceStart.String(), "service.start", 8, 60, wcprof.OutcomeOK.String())
	nat.op(nDRun, nSvc, wcprof.OpKindExec.String(), "exec.run", 11, 121, wcprof.OutcomeOK.String())
	nat.opUser(nDPR, nDRun, wcprof.OpKindExecPhase.String(), "exec.processRun", 16, 121, wcprof.OutcomeOK.String())
	nat.op(nInstB, nRoot, wcprof.OpKindCall.String(), "Container.withServiceBinding", 40, 128, wcprof.OutcomeExecuted.String())
	nat.op(nCExec, nInstB, wcprof.OpKindCallExec.String(), "Container.stdout", 62, 127, wcprof.OutcomeExecuted.String())
	nat.wait(nInstB, nSvc, wcprof.WaitReasonService.String(), 45, 60)
	nativeG := nat.graph(t)

	const minSelf = 20 * int64(ms)
	cmp, err := Oracle(nativeG, g, 0.5, 5, minSelf)
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	if !cmp.Agrees(1.0, 0.01) {
		t.Fatalf("native↔OTel must converge on the service-using ranking: jaccard=%.2f drift=%.2f native-only=%v otel-only=%v",
			cmp.JaccardTopN(), cmp.MaxRelDrift(), cmp.NativeOnly, cmp.OTelOnly)
	}
}
