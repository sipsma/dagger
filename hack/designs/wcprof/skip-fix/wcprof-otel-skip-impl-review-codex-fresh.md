# wcprof OTel skip implementation plan review - Codex fresh

Reviewed plan:
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-implementer-a7daa7c9-239fd90a/hack/designs/wcprof-otel-skip-impl-plan.md`

Branch/code checked: skip implementer worktree at
`4585bf413d5ad09af918b39b0e2b62e95ad02006`.

## Verdict

The high-level direction is right: a static, recipe-keyed emit cut for the
introspection/schema-building class is the clean way to remove the volume amplifier
without changing the loader or replay. The singleflight wait-edge acid test is sound if
the predicate is genuinely a function of the work key. That keeps the analysis
zero-inference: the engine emits a smaller but still self-consistent causal graph.

I would not implement the plan exactly as written. I found two concrete holes:

1. **Native symmetry is incomplete.** The plan gates native `call_exec`/publish/waits,
   but not the outer native `OpKindCall` created at `dagql/cache.go:3562`-`3565`. In
   non-debug OTel, skipped introspection has no normal `dag.call` span, so native would
   still contain skipped call ops while OTel would not. That contradicts the plan's
   oracle-symmetry claim (`wcprof-otel-skip-impl-plan.md:211`-`220`, `:276`-`:287`).
   Either gate the outer native call op too, or explicitly accept/filter the native
   divergence. I recommend gating it.
2. **`sharedResult.profSkip = req.SkipProfile` is not always the producer's recipe.**
   `initCompletedResult` can adopt or wrap an existing shared result
   (`dagql/cache.go:4130`-`4153`), whose stored `resultCall` may not be the current
   request. Lazy skip must follow the actual pending result's producing call, not the
   wrapper request. Otherwise a skipped schema accessor returning a real existing/lazy
   result can suppress the wrong lazy op, or a visible wrapper can re-enable an
   introspection-created lazy op. Copy/preserve the flag from an existing shared result
   when adopting it, and only set from `req.SkipProfile` when the shared result's
   `resultCall` is actually set from `req.ResultCall` (`dagql/cache.go:4159`-`4162`).

With those fixed, and with the classifier tightened as below, the design remains
moderate in scope and aligned with the no-inference principle.

## What I Agree With

The current regression mechanism is verified. Normal telemetry suppression happens in
`core.AroundFunc`: inherited skip returns `NoopDone` (`core/telemetry.go:32`-`34`),
introspection returns `dagql.WithSkip(ctx), NoopDone` (`core/telemetry.go:35`-`38`),
and normal de-dupe is `ShouldEmitTelemetry` (`core/telemetry.go:53`-`65`). `WithSkip`
is only a context value (`dagql/internal.go:32`-`42`), while the wcprof OTel gate is
only `trace.SpanFromContext(ctx).IsRecording()` (`dagql/otelprof_hooks.go:41`-`43`).
So `call_exec` currently starts for every recording cache miss
(`dagql/cache.go:3713`-`3734`), and `publishResult` follows from the valid
`execSpanCtx` (`dagql/cache.go:3999`-`4029`).

The lock-safety reason for stamping in `AroundFunc` is real. `introspectionInfo` walks
receiver calls (`core/telemetry.go:393`-`448`), and `ReceiverCall` can reach
`Cache.resultCallByResultID` (`dagql/result_call_frame.go:575`-`607`), which takes
`egraphMu.RLock` (`dagql/result_call_frame.go:1377`-`1387`). Calling that under
`callsMu` at `dagql/cache.go:3689` or under `lazyMu` at `dagql/cache.go:2934` would add
unnecessary lock nesting. Stamping the decision on `*dagql.CallRequest` before cache
entry is the right seam (`dagql/objects.go:655`-`678`).

The singleflight acid test is right for a pure recipe predicate. `ongoingCalls` are keyed
by `callKey` and `ConcurrencyKey` (`dagql/cache.go:1347`-`1350`,
`dagql/cache.go:3674`-`3677`); a waiter joining an `ongoingCall` is waiting on the same
recipe. If skip is a deterministic function of that recipe, a kept waiter cannot target
a skipped `call_exec`. Gating waits on a stored target flag also correctly keeps
"intentionally skipped" separate from "invalid target"; `EmitOTelWait` must continue to
emit targetless waits for non-skipped invalid targets (`dagql/otelprof_hooks.go:103`-
`135`), and the loader/gate must continue to fail those (`engine/wcprof/wcotel/loader.go:356`-
`405`, `engine/wcprof/wcotel/gate.go:132`-`140`).

Parent re-homing is also correctly described. If `call_exec` is not started, no absent
skipped span is pushed into the context; child spans parent to the nearest existing
recording ancestor. The loader counts an orphan only when the recorded parent span ID is
absent from the capture (`engine/wcprof/wcotel/loader.go:299`-`310`), not when the parent
is a normal span or has no wcprof kind. That supports the plan's correction of the
older "open subtree orphans children" rationale.

Excluding `ShouldEmitTelemetry` from the profiling cut is correct. It is mutable
seen-state (`dagql/telemetry.go:48`-`64`), not a recipe property. Including it would
make shared-work visibility depend on who observed the digest first and could drop real
repeated cache-miss executions.

Excluding `isMeta` is also correct. `sync` can force real evaluation and is
user-work-adjacent; it should not be hidden just because the UI treats it as meta
(`core/telemetry.go:39`-`43`, `core/telemetry.go:456` onward).

## Findings

### High: Native `OpKindCall` Is Not Skipped

The plan says native and OTel skip from one shared decision
(`wcprof-otel-skip-impl-plan.md:16`, `:211`-`:220`) and that the skipped class is absent
in both oracle sources (`wcprof-otel-skip-impl-plan.md:276`, `:287`). But the listed
native gates only cover the shared execution op, publication op, waits, and lazy
(`wcprof-otel-skip-impl-plan.md:184`-`:209`, `:245`-`:247`).

The native recorder also starts an outer `OpKindCall` before `getOrInitCallInner`:

- `dagql/cache.go:3559`-`3565`: when `wcprof.Enabled(ctx)`, it calls
  `wcprof.BeginOp(ctx, wcprof.OpKindCall, ...)`.
- `dagql/cache.go:3565`-`3577`: it ends that op after the inner cache call.

If only the plan's gates are implemented, a skipped introspection cache miss still has a
native call op. In non-debug OTel, that same call has no normal `dag.call` span and no
`call_exec`; it is absent. That makes "native and OTel drop the same class" false.

Clean fix: gate the outer native call as well, probably by changing the wrapper condition
to treat `req.SkipProfile` like "profiling disabled" for that call:

```go
if !wcprof.Enabled(ctx) || req == nil || req.ResultCall == nil || req.SkipProfile {
    return c.getOrInitCallInner(ctx, sessionID, resolver, req, fn, nil)
}
```

The nil-op methods are safe (`engine/wcprof/record.go:63`-`65`,
`engine/wcprof/record.go:124`-`139`, `engine/wcprof/record.go:175`-`178`), so the inner
code can continue to call `profOp.SetIdent`, `profOp.SetOutcomeHint`, etc.

If the owner decides to keep native outer call ops, then the plan needs to drop the
"no oracle filtering" claim and add explicit oracle filtering for the skipped class. I
do not recommend that; it adds validation complexity for no user value.

### High: Lazy Skip Must Follow The Shared Result's Actual Producer

The plan says to set `sharedResult.profSkip = req.SkipProfile` in
`initCompletedResult` (`wcprof-otel-skip-impl-plan.md:137`, `:179`-`:180`,
`:241`-`:243`). That is only correct when the shared result's `resultCall` is created
from that same `req`.

Current code has paths where the result's provenance can come from an existing shared
result:

- `dagql/cache.go:4130`-`4146`: if the resolver returns an existing cache-backed
  result, `oc.res` is replaced with `canonicalEquivalentSharedResultLocked(...)`.
- `dagql/cache.go:4149`-`4153`: if the returned value has a shared result with a stored
  frame, the new result stores `frame.clone()`.
- Only if no existing frame is present does it store `req.ResultCall.clone()`
  (`dagql/cache.go:4159`-`4162`).

Lazy classification is supposed to be keyed by the work that created the pending result,
not by whichever wrapper call returned it later. So the implementation must not blindly
overwrite an adopted shared result's `profSkip` with the current request's bit.

Required adjustment:

- Add `profSkip` to `sharedResult`.
- When adopting an existing shared result, preserve/copy the existing shared result's
  `profSkip`.
- When copying an existing result frame, also copy the existing `profSkip`.
- Only set from `req.SkipProfile` when storing `req.ResultCall.clone()` as the shared
  result's own frame.
- Consider persisted/imported shared results: if a shared result can be lazy after
  import and `profSkip` defaults false, introspection lazy work can reappear. Either
  recompute from the stored `resultCall` at import/registration time in a lock-safe
  place, or explicitly prove persisted lazy introspection cannot happen.

Without this, the lazy path can suppress real user/dependency lazy work merely because a
skipped schema accessor returned the result, or profile skipped introspection lazy work
because a visible wrapper touched it.

### Medium: Debug-Gated Predicate Is Not Pure, And The Orphan Rationale Is False

I agree with the lead's pushback. The plan's debug argument
(`wcprof-otel-skip-impl-plan.md:293`-`:300`) says a debug-independent profiling
predicate would skip `call_exec` while a normal span records, causing a child to parent
to a "non-op span" and count as `OrphanedParents`. That is not how the loader works.

The loader builds `opIDBySpan` for every deduped span, with no kind filter
(`engine/wcprof/wcotel/loader.go:217`-`254`). It counts an orphan only when the causal
parent span ID is non-empty and not present in that map
(`engine/wcprof/wcotel/loader.go:299`-`310`). A normal debug `dag.call` span is present;
even a span with no wcprof kind is present. So this does not orphan.

The debug-gated classifier also weakens the static-cut proof. The receiver-type branch
depends on `slog.IsDebug(ctx)` (`core/telemetry.go:401`-`402`), not only on the recipe.
The plan stores the target's flag, which prevents dangling edges, but a debug mismatch
can still make a visible waiter on hidden work drop its wait or a hidden waiter on
visible work emit one. That is self-consistent, but not necessarily faithful.

Recommendation: make the profiling predicate debug-independent and separate from normal
UI/debug telemetry. If debug normal spans exist, they are present parents and do not
orphan. If debug wants to see introspection spans in the UI, fine; the profiling
amplifier should still stay off unless there is a profiling-specific opt-in with an
explicit volume warning.

### Medium: Do Not Extend `introspectionInfo` For This Unless UI Suppression Is Wanted

The plan's §3.2a extends `core.introspectionInfo` directly
(`wcprof-otel-skip-impl-plan.md:81`-`:96`). That changes ordinary user-facing telemetry,
not just wcprof profiling: directly-called `Function.args`, `Function.sourceModuleName`,
and `TypeDef.as*` would stop emitting normal `dag.call` spans because `AroundFunc` would
return `WithSkip(ctx), NoopDone` (`core/telemetry.go:35`-`38`).

That may be acceptable, but it is a separate UI/product change. It is not necessary for
the profiling fix. A cleaner plan is:

- keep `introspectionInfo` as the normal telemetry/UI predicate;
- add a separate `profileSkipInfo` in `core` that reuses `introspectionInfo` plus
  profiler-only schema-walk classification;
- stamp only `CallRequest.SkipProfile` from that predicate.

This also makes the debug-independent choice easier: normal telemetry can keep its
existing debug behavior while wcprof profiling stays volume-safe.

### Medium: Field-List Mode Is Likely Under-Complete

The plan's own §3.2 notes that 3.2a is workload-specific
(`wcprof-otel-skip-impl-plan.md:90`-`:95`). I would not start with field chasing. In my
branch capture, besides the named `Function.*` and `TypeDef.as*` rows, schema/reflection
names included `ObjectTypeDef.functions`, `ObjectTypeDef.constructor`,
`ObjectTypeDef.fields`, and more. Some `ObjectTypeDef.__*` fields are already covered by
the existing `__` rule (`core/telemetry.go:434`-`444`), but the non-`__` accessors are
not.

Given Erik is fine losing this class and the goal is volume safety, use the robust
receiver-type predicate from the start for profiling only: any field whose receiver type
is one of the schema/reflection types is profile-skipped. That is simpler to reason
about than iterating workload captures, and it better preserves the "static recipe cut"
invariant.

### Medium: Performance Needs A V1 Check, Not "Optimize Later"

The plan moves the receiver-chain walk before the inherited `IsSkipped` return
(`wcprof-otel-skip-impl-plan.md:113`-`:127`, `:300`). That means all inherited-skip
descendants now pay `introspectionInfo`, and `ReceiverCall` can take `egraphMu.RLock`
(`dagql/result_call_frame.go:575`-`607`, `dagql/result_call_frame.go:1377`-`1387`).
The walk is outside cache locks, so this is not a correctness problem, but it is on the
same high-volume path that triggered this work.

I would include the performance check in v1 validation. A short-circuit is fine only if
it does not break native profiling. A naive `if !OTelProfActive(ctx) { skip stamp }`
would skip stamping when native `wcprof.Enabled` is active but OTel is not; that matters
if native is also supposed to skip. Either measure first, or introduce a safe "profiling
interest" signal that covers both OTel and native before short-circuiting.

### Medium: Validation Must Prove Exec/Service Assumption

I agree with the plan not to blanket-change `OTelProfActive`; `exec.run` and
`processRun` are the user-work-first-class path (`engine/engineutil/otelprof.go:46`-
`117`) and service waits have their own target semantics (`core/services.go:995`-
`1045`). But the claim that exec/service never emit a skipped-class span is an
assumption until measured (`wcprof-otel-skip-impl-plan.md:249`). The §9 capture should
make this a hard assertion: no `wcprof.op.kind=exec`, `exec_phase`, or `service_start`
descends from a skipped profiling class unexpectedly, and user `processRun` self-time is
unchanged.

## Lead Point Evaluation

A. **Debug gating:** I agree with the lead. The debug-orphan argument is unfounded
because the loader maps all present spans to op IDs (`engine/wcprof/wcotel/loader.go:217`-
`254`) and orphans only absent parent spans (`engine/wcprof/wcotel/loader.go:299`-`310`).
A debug-independent profiling predicate is cleaner and restores the pure recipe property.

B. **Extending `introspectionInfo`:** I agree with the lead. Extending it couples a
profiling-volume fix to normal telemetry/UI suppression. Use a separate profiling
predicate unless the product owner explicitly wants the UI behavior change.

C. **Completeness:** I agree with the lead. 3.2b, as a separate profiling predicate over
reflection receiver types, is the robust option. The named field list is likely to leave
residual schema-walk spans and require iterative capture chasing.

D. **Performance:** I agree in substance. The lock-safe stamp location is right, but the
cost should be measured in v1. Any short-circuit must account for native profiling, not
only OTel recording.

E. **Exec/service validation:** I agree. Do not gate exec/service preemptively, but the
post-fix capture must prove they are not part of the skipped-class volume and that user
work remains first-class.

## Answers To The Review Questions

- **Correctness:** Correct direction, with blockers above. Static recipe skip is the
  right cut for singleflight. Fix native outer call symmetry and lazy `profSkip`
  provenance before implementation.
- **Performance:** Likely acceptable, but not "free"; moving receiver-chain
  classification before inherited skip needs measurement. Prefer a separate broad
  profile predicate to avoid repeated field-list churn.
- **Simplicity:** Moderate. `CallRequest.SkipProfile` is a good seam. The plan gets more
  complex only if it tries to preserve debug-gated UI behavior and oracle symmetry at
  the same time.
- **Volume regression:** Solves it if the classifier is complete. 3.2a may under-catch;
  a separate receiver-type profiling predicate is more likely to return span volume to
  baseline immediately.
- **Canonical design / invariants:** Aligned if kept emit-only. No loader/replay change
  is needed. The hard gate remains meaningful because skipped waits are deliberate and
  non-skipped invalid targets still emit targetless waits.
- **No-inference principle:** Preserved. The analysis still consumes only emitted data.
  The main caution is that "target flag avoids dangling" is not enough if the predicate
  is not truly recipe-pure; it can hide a real wait. Make the predicate debug-independent
  to remove that caveat.

## Recommended Plan Revision

1. Keep `CallRequest.SkipProfile`, stamped in `AroundFunc` before `IsSkipped`.
2. Use a **separate, debug-independent** profiling predicate:
   `profileSkipInfo = introspectionInfoWithoutDebugForProfiling || reflectionReceiverType`.
   Do not change normal `introspectionInfo` unless UI suppression is explicitly desired.
3. Gate OTel `call_exec`, `publishResult`, and cache waits exactly as proposed.
4. Gate native **outer call**, native `call_exec`, publish, cache waits, lazy op, and
   lazy waits from the same bit, or explicitly abandon native/OTel full-oracle symmetry.
5. Add `ongoingCall.profSkip`.
6. Add `sharedResult.profSkip`, but derive/copy it from the actual stored producer
   result, not blindly from the current request on every `initCompletedResult` path.
7. Keep `profSkip` distinct from invalid span contexts; do not change loader/replay.
8. Make validation fail unless span volume is near baseline, structural gate is clean,
   user `processRun` attribution is unchanged, and native/OTel oracle comparison handles
   the skipped class intentionally.
