# wcprof x OTel Chunk 3 Review - Codex Fresh

Scope: Chunk 3 commit `a460633b0f` in isolation (`b0e7cd9931..a460633b0f`) plus a holistic Chunks 1+2+3 pass (`b442cd2533..a460633b0f`). Review only; no code changes.

## Findings

1. Medium: `lazyEvalSpanCtx` can carry a stale wait target across a failed lazy-eval retry.

`lazyEvalSpanCtx` is stored on the shared result as the current OTel lazy target (`dagql/cache.go:1518-1523`) and joiners read it while `lazyEvalWaitCh` is active (`dagql/cache.go:2947-2965`). The leader overwrites it only when `otelProfActive(evalCtx)` is true (`dagql/cache.go:2996-2999`). When an eval finishes, the cleanup clears `lazyEvalWaitCh`, `lazyEvalCancel`, and `lazyEvalErr`, but never clears `lazyEvalSpanCtx` (`dagql/cache.go:3053-3064`). On failure, `lazyEvalComplete` stays false and `lazyEval` remains installed (`dagql/cache.go:3053-3058`), so the same result can be evaluated again.

That creates this bad sequence: a traced lazy eval fails and leaves a valid `lazyEvalSpanCtx`; later an untraced/non-recording leader starts a retry and publishes `lazyEvalWaitCh` without overwriting the span context; then a recording joiner observes the retry and emits a `lazy` wait to the stale previous span instead of a targetless, gate-observable wait. If the old span is still in the same trace, the loader can resolve the stale target and the §6.1 gate will pass while the wait points at the wrong eval. This violates the Chunk 2/3 rule that mixed-recording missing targets should fail loud, not silently attach to old work.

The minimal fix is to reset the current lazy OTel target under `lazyMu` for every new eval before publication: set it to zero when telemetry is inactive, overwrite it when telemetry is active, and preferably clear it when the in-flight state is cleared. The same audit is worth doing for `lazyEvalProfOpID` so native and OTel stay aligned under non-uniform recording.

2. Medium validation risk: the empirical `jaccard=0.23` run is not a full top-N faithfulness proof yet.

I agree the low empirical top-N overlap is plausibly a native-global vs OTel-client scope mismatch rather than evidence that the lazy re-point fix is wrong. The matched-scope deterministic lazy oracle converges, and the overlapping resolver work reportedly has `drift=0.00`. But the low-jaccard run is not airtight: the non-overlapping classes are exactly where a real unfaithful parent, missing wait, or unmodeled scope boundary could hide. The current oracle CLI loads both graphs and compares raw top-N classes with no scope or class filtering (`cmd/wcprof-oracle/main.go:55-91`), so it cannot distinguish "expected source-scope mismatch" from "source-specific unfaithfulness" in those non-overlap buckets.

Right disposition: do not treat that empirical run as a failing Chunk 3 gate, but also do not count it as proof of holistic top-N faithfulness. The §6.4 standing gate needs either scope-matched sources or explicit class/source filters before it can be a release-quality complex-workload signal.

## Chunk 3 Assessment

The core lazy emit design is implemented faithfully for the normal uniform-recording path. The leader mints the OTel `lazy` span while holding `lazyMu`, stashes its `SpanContext`, and only then publishes `lazyEvalWaitCh` (`dagql/cache.go:2930-3004`), which satisfies Invariant T. Producer-context eval reuses the existing resume span shape and leaves `resumedCallbackSpan` behavior intact (`dagql/otelprof_lazy.go:141-168`); no-producer eval creates a hidden passthrough lazy op under the consumer (`dagql/otelprof_lazy.go:170-176`). Joiners emit `reason=lazy` wait links to the stashed target (`dagql/cache.go:2947-2965`), and the leader emits the redundant native-parity wait (`dagql/cache.go:3070-3080`).

The stamping processor matches §3.0.2: it reads the override from the start context, stamps only when the recorded parent is exactly the producer span, and leaves descendants unstamped (`dagql/otelprof_lazy.go:88-101`). Registration is before every live export processor on the per-client provider (`engine/server/session.go:694-709`, parent exports appended at `engine/server/session.go:730-747`). The in-memory SDK tests are meaningful here: they verify direct-child stamping, no-producer nesting, and own+parent export coverage on real exported spans.

I do not see degenerate complexity. The new leader path does extra work under `lazyMu`: session lazy-span lookup, install-span lookup, link construction, and `Tracer.Start`. That is bounded by install-span/dependency counts and not quadratic in span count, but it is now in the shared lazy leader critical section, so keep it in perf watch if traces show contention.

## Holistic Chunks 1+2+3 Assessment

Chunks 1+2+3 compose well architecturally. The loader and gate are unchanged in Chunk 3, which is exactly the intended contract: emit `wcprof.parent` and wait links at the engine choke point, then let the same mechanical loader and unchanged replay do the analysis. Chunk 2's `call_exec` is also a prerequisite for lazy work's sub-calls, and the Chunk 3 fixture exercises that composition.

The project is still heading toward the north star, but it is not there yet. Singleflight and lazy attribution are now structurally modeled; user work is still not first-class until Chunk 4's exec split and service modeling land. The low empirical complex-workload jaccard should be treated as a validation-scope problem to solve before the standing gate, not as evidence that the design is drifting.

## Divergence Verdicts

Lazy-op class label: acceptable with a caveat. Keeping `"resume <field>"` in the producer case preserves the UI-visible resume span and is benign while lazy-op self-time remains near zero. If lazy self-time becomes material, the native-vs-OTel class mismatch will become a real oracle/reporting issue, so the existing lazy-heavy oracle should keep a threshold that would expose that.

Leader wait link: justified. It is redundant because the lazy op nests under the leader, but it is idempotent in replay, matches native's leader wait, and mirrors Chunk 2's executor wait.

Provider-coverage wording: agree with the implementation correction. The code has one tracer provider per client and multiple export processors; one prepended stamping processor on that provider covers the client's own DB and parent exports. The dedicated all-exports test covers the important behavior.

## Verdict

Chunk 3 is sound enough to build Chunk 4 on for the main uniform-recording lazy path, but I would not close the Chunk 3 robustness story until the stale `lazyEvalSpanCtx` retry case is fixed or explicitly ruled out. Holistically, Chunks 1-3 remain on-goal and the design is still working: the risk is now validation/scope hygiene and a specific lazy retry edge, not a failure of the unchanged replay approach.

## Verification Run

- `git diff --check b0e7cd9931..a460633b0f`
- `go test ./engine/wcprof/... ./cmd/wcprof-otel-analyze ./cmd/wcprof-oracle ./hack/otlpdump`
- `go test ./dagql`
- `go test ./engine/server`
- `go test ./dagql -run 'TestCacheLazyEvaluation|TestLazyEmit|TestWcprofLazyParent|TestEmitWait' -count=1 -v`
- `go test ./engine/wcprof/wcotel -run 'TestChunk3LazyRepointFidelity' -count=1 -v`
- `go vet ./dagql` reports only the known pre-existing `lostcancel` warning in `cache.go`
