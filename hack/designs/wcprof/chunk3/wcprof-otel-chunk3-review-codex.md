# wcprof x OTel Chunk 3 Code Review

Reviewed Chunk 3 commit `a460633b0f` against Chunk 2 base `b0e7cd9931`
and upstream base `b442cd2533`.

## Overall Verdict

Chunk 3 is close, but I would not build Chunk 4 on it until the stale lazy wait
target below is fixed. The `wcprof.parent` stamping processor and the
direct-child discriminator are sound; the main lazy re-home model composes with
the Chunk 1 loader and Chunk 2 wait-link wire format. The implementation has one
high-severity state-lifetime bug in the new `shared.lazyEvalSpanCtx` field.

After that fix, the Chunks 1-3 trajectory still looks sound: the loader remains
mechanical, replay is unchanged, lazy work is causally under the consumer-side
lazy op instead of the already-ended producer, and the producer is not
double-charged.

## REAL Issues

### HIGH - `lazyEvalSpanCtx` can survive a failed traced eval and mis-target a later untraced retry

Chunk 3 adds `shared.lazyEvalSpanCtx` as the OTel analog of
`lazyEvalProfOpID` (`dagql/cache.go:1518` to `dagql/cache.go:1523`). The leader
sets it only when the leader context is recording:
`otelProfActive(evalCtx)` gates `beginOTelLazyOp`, then stores
`shared.lazyEvalSpanCtx = lazySpan.SpanContext()` (`dagql/cache.go:2996` to
`dagql/cache.go:2999`). Immediately afterward it publishes
`shared.lazyEvalWaitCh` (`dagql/cache.go:3000` to `dagql/cache.go:3003`).

The problem is that the field is never reset on a new evaluation attempt where
telemetry is off, and it is not cleared during completion cleanup. The goroutine
records `lazyEvalErr` / `lazyEvalComplete` and clears `lazyEvalWaitCh`,
`lazyEvalCancel`, and `lazyEvalErr` in the `clearState` branch, but leaves
`lazyEvalSpanCtx` untouched (`dagql/cache.go:3053` to `dagql/cache.go:3065`).
The waiter-side cleanup does the same: it clears `lazyEvalWaitCh`,
`lazyEvalCancel`, and `lazyEvalErr`, but not the span context
(`dagql/cache.go:2827` to `dagql/cache.go:2834`). Joiners unconditionally read
whatever `shared.lazyEvalSpanCtx` currently contains when `lazyEvalWaitCh != nil`
(`dagql/cache.go:2947` to `dagql/cache.go:2955`) and emit a lazy wait to that
target (`dagql/cache.go:2965`).

Concrete failure shape:

1. A traced lazy evaluation starts, stores a valid `lazyEvalSpanCtx`, and fails.
   Failed lazy evals are retryable: `lazyEvalComplete` is set only on `err == nil`
   (`dagql/cache.go:3054` to `dagql/cache.go:3058`).
2. A later retry is led by an untraced/non-recording context, so
   `otelProfActive(evalCtx)` is false and the field is not overwritten.
3. A traced concurrent joiner observes the new `lazyEvalWaitCh`, reads the stale
   span context, and emits a wait link to the old lazy op instead of an invalid
   target.

That violates the design's explicit mixed-recording rule: when a recording
waiter joins work whose owner did not mint a target, the emitter must attach a
targetless wait so the loader counts `UnresolvedWaitTargets` and the §6.1 gate
fails loud (`hack/designs/wcprof-otel-design.md:391` to
`hack/designs/wcprof-otel-design.md:407`). A stale but resolvable old target can
silently under-serialize the retry and credit the wrong lazy attempt. If the old
target is outside the captured trace the gate may fail, but if it is in the same
trace this can compile cleanly and be wrong.

Recommended fix: under `lazyMu`, clear `shared.lazyEvalSpanCtx` to an invalid
`trace.SpanContext{}` before each new evaluation attempt, before publishing
`lazyEvalWaitCh`; then overwrite it only when `beginOTelLazyOp` actually minted a
span. Clearing it when the in-flight attempt is fully cleaned up is also fine,
but the pre-publish reset is the load-bearing part. Add a regression that fails
without the reset: traced failed attempt, untraced retry leader, traced joiner,
and assert the second joiner's emitted wait is targetless/gate-observable rather
than linked to the first attempt's lazy span.

## NOISE / Verified Fine

- **The direct-child stamping discriminator is sound.** The processor reads the
  lazy override from the SDK `OnStart` parent context and stamps only when
  `s.Parent().SpanID() == ov.producerSpanID` (`dagql/otelprof_lazy.go:88` to
  `dagql/otelprof_lazy.go:100`). The checked-in test drives the real
  `resumedCallbackSpan` path and verifies the direct work span keeps UI
  `parentId = producer`, carries `wcprof.parent = lazy`, and the descendant is
  unstamped (`dagql/otelprof_lazy_test.go:123` to
  `dagql/otelprof_lazy_test.go:136`).
- **`resumedCallbackSpan` remains the only parent-diverging span wrapper.** The
  only production `SpanContext()` override in the source search is
  `resumedCallbackSpan` (`dagql/cache.go:2809` to `dagql/cache.go:2821`), and
  the other `ContextWithSpanContext` uses re-anchor to explicit causes/self
  rather than installing this lazy override. Guardrail 1 still holds.
- **Invariant T ordering is implemented, modulo the stale-field bug above.** The
  leader starts the OTel lazy op while holding `lazyMu` (`dagql/cache.go:2930`,
  `dagql/cache.go:2996` to `dagql/cache.go:2999`) and only then publishes
  `lazyEvalWaitCh` (`dagql/cache.go:3000`). Once the stale-field reset is added,
  joiners that observe the primitive will either get the current valid target or
  an intentionally invalid target that the gate catches.
- **Producer and no-producer existence cases match the approved design.** In the
  producer case, `beginOTelLazyOp` starts the passthrough resume span under the
  consumer, wraps the callback with `resumedCallbackSpan`, and carries the lazy
  override (`dagql/otelprof_lazy.go:141` to `dagql/otelprof_lazy.go:167`). In
  the no-producer case it creates a passthrough lazy span under the consumer and
  uses normal parentage with no override (`dagql/otelprof_lazy.go:170` to
  `dagql/otelprof_lazy.go:176`).
- **Provider coverage is correctly implemented.** The stamping processor is
  registered first in the per-client tracer provider options
  (`engine/server/session.go:692` to `engine/server/session.go:705`), before the
  client's own `LiveSpanProcessor` and before parent export processors are
  appended (`engine/server/session.go:730` to `engine/server/session.go:747`).
  `TestWcprofLazyParentProcessorStampsAllExports` covers own and parent exports
  (`dagql/otelprof_lazy_test.go:257` to `dagql/otelprof_lazy_test.go:307`).
- **The loader composes without changes.** Chunk 1 already uses
  `causalParentSpanID(s)` when assigning parent IDs and when detecting
  `call_exec` children (`engine/wcprof/wcotel/loader.go:262` to
  `engine/wcprof/wcotel/loader.go:288`), so `wcprof.parent ?? parentId` applies
  to lazy work while descendants keep their ordinary parent IDs.
- **The leader wait link is redundant but harmless.** The leader emits a lazy
  wait to its own child lazy span (`dagql/cache.go:3070` to
  `dagql/cache.go:3079`). Replay joins are `max`-style (`replay.go:379` to
  `replay.go:381`), and the implicit child join already serializes the lazy op,
  so this does not add false serialization; it just preserves native/oracle
  parity.
- **The producer-case lazy class-label divergence is acceptable for this chunk.**
  The OTel producer case names the lazy op `"resume <field>"`
  (`dagql/otelprof_lazy.go:144` to `dagql/otelprof_lazy.go:160`) while native
  classes the lazy op by `profCallClass`. That can only affect the lazy op's own
  self-time; the real deferred work is re-homed under it and keeps its real
  classes. The fixture asserts the lazy op self-time is small relative to the
  deferred `call_exec` work and filters that known label mismatch in the
  deterministic oracle (`engine/wcprof/wcotel/chunk3_test.go:145` to
  `engine/wcprof/wcotel/chunk3_test.go:149`,
  `engine/wcprof/wcotel/chunk3_test.go:202` to
  `engine/wcprof/wcotel/chunk3_test.go:211`). If lazy self ever becomes hot,
  the standing oracle should catch it as a class-label issue, not a replay-model
  issue.
- **The low empirical top-N jaccard is not evidence of Chunk 3 unfaithfulness, but
  it also is not a proof for unmatched classes.** The current oracle compares raw
  class keys from both graphs (`engine/wcprof/wcotel/oracle.go:112` to
  `engine/wcprof/wcotel/oracle.go:125`), so native-global vs OTel-client scope
  differences can dominate top-N. The deterministic matched-scope fixture proving
  lazy-heavy convergence is the right Chunk 3 signal. The disposition to make the
  §6.4 standing gate scope-matched or class-filtered is correct; do not treat raw
  cross-scope jaccard as a release criterion.
- **No degenerate performance shape found.** The new hot-path work is constant per
  lazy eval and per waiter: one lazy span start/end plus one wait link per waiter.
  Starting the lazy span under `lazyMu` does synchronously invoke span processors,
  but the Dagger live processor's `OnStart` just snapshots into a batch processor
  (`github.com/dagger/otel-go` `live.go:25` to `live.go:30`). I did not find a
  concrete cache lock inversion from the new `lazyMu -> session/egraph lookup`
  order in `beginOTelLazyOp`.

## Holistic Trajectory

Chunks 1-3 still fit the approved design after the stale-target fix. The OTel
graph now has the two central causality repairs: shared `call_exec` work with
per-caller waits, and lazy deferred work causally under the consumer-side lazy op
while preserving producer-side UI parentage. The unchanged replay's implicit
join and wait-join mechanics match that structure.

The remaining large goal gaps are the planned ones: Chunk 4 must split
container/user process work, and later chunks/fixtures must resolve the
scope-matched empirical oracle story. I do not see a new design-level problem.

## Tests Run

Against detached worktree at `a460633b0f`:

```sh
go test ./dagql ./engine/wcprof/wcotel ./cmd/wcprof-otel-analyze ./cmd/wcprof-oracle
```

Result: pass.
