# wcprof x OTel Chunk 2 Review - Codex Fresh

Scope: Chunk 2 commit `f127e5662b` in isolation (`71b69f1f16..f127e5662b`) plus a holistic Chunks 1+2 pass (`b442cd2533..f127e5662b`). Review only; no code changes.

## Findings

1. Medium: a recording waiter can silently lose its wait edge if it joins an execution that did not mint a `call_exec` span.

`ongoingCalls` are keyed only by `callKey` and `concurrencyKey`, not by session or trace (`dagql/cache.go:1347-1350`, constructed at `dagql/cache.go:3633-3636`). The executor mints `call_exec` only when its own detached call context is recording (`dagql/cache.go:3691-3693`, `dagql/otelprof_hooks.go:32-38`). Later, every waiter calls the OTel wait emitter (`dagql/cache.go:3917`), but `emitOTelCallWait` returns immediately when `oc.execSpanCtx` is invalid, before checking whether the waiter span is recording (`dagql/otelprof_hooks.go:95-101`).

For the normal same-trace CI path, Invariant T should hold: a traced/suppressed caller still has a recording ancestor, so the executor mints the target before publishing `oc`. The hole is mixed recording state, especially cross-session singleflight: a traced caller can join work started by an untraced executor, and then the traced span gets no wait link, no unresolved-target counter, and no structural-gate failure. If the foreign executor was traced in another trace, the link would at least exist and the loader would fail on an unresolved target; the silent case is when the target span was never minted.

This is not evidence that the central same-run Chunk 2 path is broken, but it is a real robustness gap in the "missing wait target" story. Either make this condition observable to the gate when the waiter is recording, or explicitly document/test the assumption that analyzed Cloud traces cannot wait on unrecorded in-flight executions.

2. Medium validation/DoD gap: the committed fixtures do not fully cover the Chunk 2 validation plan.

The implementation plan schedules the persisted-cache import/decode drift fixture in Chunk 2 and says it must actually run because it decides whether the persisted-decode reserve seam is needed (`hack/designs/wcprof-otel-impl-plan.md:261-272`, with the reserve seam called out at `hack/designs/wcprof-otel-impl-plan.md:474-476`). I did not find a committed wcotel/oracle fixture for that path in `f127e5662b`; the persistence tests that exist are normal dagql cache tests, not native-vs-OTel drift tests. That means the reserve-seam decision has not actually been made. This can be explicitly deferred under the plan's fallback, but then Chunk 2's DoD is not fully met as written.

The cap-stress unit test is useful, but it is also narrower than the claim "otlpdump path, zero dropped links at 16384." `TestChunk2CapStressFanIn` fabricates JSONL with 5000 wait links and no dropped count (`engine/wcprof/wcotel/chunk2_test.go:304-364`). That validates loader/replay/gate behavior, not the SDK limit configured in `engine/server/session.go:684-695` nor the otlpdump dropped-count fields in `hack/otlpdump/main.go:119-138`. If the empirical augmented-engine run covered this, fine, but there is no committed regression for the end-to-end cap behavior.

## Chunk 2 Assessment

The core Chunk 2 emit shape is correct for the intended same-trace singleflight path. `call_exec` is minted under `callsMu` before `ongoingCalls` publication (`dagql/cache.go:3648-3722`), the resolver runs under the `sharedWorkCtx` descended from that span (`dagql/cache.go:3724-3732`), the per-caller wait link is emitted from `c.wait` on the waiter/current span (`dagql/cache.go:3894-3917`), and `dagql.publishResult` is emitted as the native-parity diagnostic under the `call_exec` context (`dagql/cache.go:3958-3988`). That matches design section 3.1 and Invariant T for the path it is meant to repair.

The Chunk 1 loader composes cleanly with this without changes: explicit `wcprof.op.kind=call_exec` wins classification, `dag.digest` provides the `call_exec` ident, wait links compile into the same wait-event IR, and the unchanged replay sees the intended parent/child plus explicit wait structure. I did not see a quadratic or exponential path in the new code; the added loader/oracle work is map/sort/link-linear, and the replay cost is the existing analyzer cost.

The `dagql.publishResult` late-child shape is correctly treated as oracle parity rather than a counterfactual attribution fix. If publication becomes hot, the design's reserve seam still needs a native+OTel fix; this chunk does not overclaim it.

## Holistic Chunks 1+2 Assessment

The trajectory is still sound. Chunk 1 gives a mechanical OTel-to-wcprof loader plus a much stronger structural gate; Chunk 2 emits the central missing causality for cache misses/singleflight and brings the oracle online. Together they repair the biggest false attribution mode: joiner or ancestor self-time being mistaken for real work while the shared execution is disconnected from the critical path.

The mixed container-workload `jaccard=0` claim is not, by itself, a Chunk 2 regression. The implementation plan explicitly says cumulative state after Chunk 2 should converge on singleflight-heavy workloads while lazy-heavy and exec/service workloads still drift (`hack/designs/wcprof-otel-impl-plan.md:278-281`). If drift localization points at lazy/exec/service/leaf-I/O seams not yet implemented, that is expected. It would become suspicious only if a singleflight-isolating oracle failed or if Chunk 2's identity-level singleflight parity regressed.

The north star remains plausible but incomplete: user work is not first-class until the exec split lands, so Chunks 1+2 should not be sold as answering a full "slow go build" trace yet. They do provide the required causal substrate that later lazy/exec/service chunks need.

## Divergence Verdicts

Omitting `dag.call` on `call_exec`/`publishResult`: justified. The design lists `dag.call` for the internal span attributes (`hack/designs/wcprof-otel-design.md:469-472`), but the loader/oracle use span name, explicit op kind, and `dag.digest` (`dagql/otelprof_hooks.go:53-60`, `engine/wcprof/wcotel/loader.go:288-290`). Re-encoding `CallPB()` on the cache-miss path would add cost without improving Chunk 2 faithfulness. I would keep this as an intentional design update rather than a miss.

Always-on production telemetry posture: correct for the Cloud-trace north star, with explicit owner sign-off. The code is gated on a recording span, not `wcprof.Enabled`, and documents the volume as two passthrough spans per cache miss plus one wait link per blocked caller (`dagql/otelprof_hooks.go:22-28`), matching the design volume statement (`hack/designs/wcprof-otel-design.md:569-573`). The volume is bounded enough for this chunk, with the raised link cap and hardened gate as backstops. The remaining performance watch item is that `Tracer.Start` for `call_exec` happens while holding `callsMu`; the live span processor snapshots on start, so this should stay under empirical watch even though the design requires minting the target under the lock.

Pre-existing `lostcancel`: verified. `go vet ./dagql` reports the warning at `dagql/cache.go:3674`/`:3701` on `f127e5662b`, and the same warning exists on the Chunk 1 parent at `dagql/cache.go:3668`/`:3682`. Chunk 2 did not introduce the `WithCancelCause` path.

## Verdict

Chunk 2 is sound enough to build Chunk 3 on for the intended same-trace singleflight path. I would not call the Chunk 2 DoD fully closed until the persisted-cache drift fixture is either landed or explicitly deferred, and I would close or consciously accept the mixed-recording wait-loss edge before relying on traces from engines that can share in-flight calls across independently traced/untraced sessions.

Holistically, Chunks 1+2 still compose cleanly and remain on-goal. The emerging risks are validation coverage and trace-boundary assumptions, not a failure of the unchanged-replay architecture.

## Verification Run

- `git diff --check 71b69f1f16..f127e5662b`
- `go test ./engine/wcprof/... ./cmd/wcprof-otel-analyze ./cmd/wcprof-oracle ./hack/otlpdump`
- `go test ./dagql`
- `go test ./engine/server`
- `go vet ./dagql` on `f127e5662b` and on `71b69f1f16` to compare the `lostcancel` warning
