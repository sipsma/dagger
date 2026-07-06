# wcprof OTel skip implementation plan review - Codex forensics

Reviewer: Codex, forensic investigator

Scope: council review only. I read
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-implementer-a7daa7c9-239fd90a/hack/designs/wcprof-otel-skip-impl-plan.md`
in full and checked the claims against the current branch code. No product code
changes.

## Verdict

Green on the central design: a static, work-keyed profiling skip stamped before
the cache/lazy emit sites is the right shape. It keeps the loader and replay
unchanged, preserves the no-inference principle, avoids a dynamic `IsSkipped(ctx)`
singleflight race, and has a real lock-safety advantage over a cache-side
predicate.

I would require three design edits before implementation:

1. Do not use the plan's debug-orphan rationale. It is wrong. Prefer a
   debug-independent profiler predicate unless the team explicitly accepts the
   volume amplifier returning in debug mode.
2. Do not extend `introspectionInfo` as part of this profiler fix unless the team
   wants a normal/UI telemetry change. A separate profiler predicate is cleaner.
3. Make `sharedResult.profSkip` propagation explicit across result adoption,
   copying, persistence import, and lazy registration paths. Setting it only in
   `initCompletedResult` from the current `req.SkipProfile` is not obviously
   sufficient.

With those edits, this remains a simple emit-side fix. The analysis side stays
strict and unchanged.

## What The Plan Gets Right

Static cut: correct.

The important correction from the previous dynamic idea is that the skip decision
must be tied to the work key/recipe, not to the current caller's inherited
`dagql.IsSkipped(ctx)` state. Current singleflight sharing is keyed by
`callKey` and `req.ConcurrencyKey` at `dagql/cache.go:3674-3677`, and an
existing in-flight call is joined at `dagql/cache.go:3694-3703` without any
telemetry-visibility dimension. A dynamic caller-state cut can make the claimer
hidden and the joiner visible for the same work. A static recipe cut avoids that
class of dangling wait target.

Zero analysis changes: correct.

The loader maps every deduped span to an op before kind classification:
`engine/wcprof/wcotel/loader.go:217-254`. Parent resolution is mechanical:
`wcprof.parent` if present, otherwise `parentId`, at
`engine/wcprof/wcotel/loader.go:468-475`. Orphans are counted only when the
recorded parent span is absent from the capture at
`engine/wcprof/wcotel/loader.go:299-310`, and unresolved non-lock waits are
counted at `engine/wcprof/wcotel/loader.go:356-380`. The gate hard-fails those
signals at `engine/wcprof/wcotel/gate.go:132-140`. The plan does not require
weakening any of this.

Lock-safety: correct and important.

`introspectionInfo` can call `ResultCall.ReceiverCall` at
`dagql/result_call_frame.go:575-603`, which can call
`Cache.resultCallByResultID` and take `egraphMu.RLock` at
`dagql/result_call_frame.go:1377-1387`. Doing that under `callsMu` near the
`call_exec` mint at `dagql/cache.go:3689-3734` or under `lazyMu` near the lazy
mint at `dagql/cache.go:2936-3025` would add avoidable lock nesting. Stamping a
bool before `GetOrInitCall` is cleaner.

Separate "intentionally skipped" from "invalid target": correct.

`dagql/otelprof_hooks.go:111-126` deliberately emits invalid-target wait links
so mixed recorded/unrecorded work fails loud. The plan's separate bool preserves
that detector. Do not infer "skipped" from `execSpanCtx.IsValid()` or
`lazyEvalSpanCtx.IsValid()`.

Native and OTel symmetry: acceptable.

Gating native wcprof with the same bit is fine for cross-source oracle
comparability, provided the validation still spot-checks non-skipped user-work
self-time against pre-skip native ground truth. The plan includes that in §9.4.

## Required Corrections

### A. Debug gating

The plan's debug-orphan argument is unfounded.

It says a debug-independent predicate could skip `call_exec` while normal
debug `dag.call` records, causing a kept child to parent to a "non-op span" and
trip `OrphanedParents`. The loader does not work that way. It creates
`opIDBySpan` for every deduped span without filtering by kind at
`engine/wcprof/wcotel/loader.go:251-254`, and only counts an orphan when the
parent span id is set but absent at `engine/wcprof/wcotel/loader.go:299-310`.
A normal debug `dag.call` span is present, so a child parented to it is not an
orphan.

That removes the strongest reason to reuse the debug-gated `introspectionInfo`.
I recommend a debug-independent profiler predicate. It is a purer recipe
function, removes the plan's own "debug baggage is the one non-recipe input"
caveat, and prevents the wcprof `call_exec`/`publishResult` amplifier from
returning in debug mode.

Nuance: debug-independent profiler skip does not make debug traces as small as
non-debug traces, because normal debug `dag.call` spans may still record. It does
stop the wcprof multiplier from doubling that volume. If the team wants debug
mode to profile introspection anyway, that is a policy choice, not an orphan
correctness requirement.

### B. Keep Normal Telemetry Separate

Extending `core.introspectionInfo` changes ordinary telemetry behavior. That
function is used by `core.AroundFunc` to suppress normal spans at
`core/telemetry.go:35-38`, and `ObjectResult.call` invokes that telemetry hook
before cache execution at `dagql/objects.go:655-665`. Adding
`Function.args`, `Function.sourceModuleName`, `TypeDef.as*`, or broader
reflection fields there means a directly-called schema accessor stops emitting a
normal `dag.call` span.

That may be a reasonable UI cleanup, but it is not necessary to fix the wcprof
volume regression. I recommend a separate profiling predicate, e.g.
`profileSkipInfo`, that can call/reuse shared helper logic but is not identical
to normal UI telemetry suppression. That also lets the profiler predicate be
debug-independent while leaving the current debug behavior of normal telemetry
alone.

### C. Field Completeness

The named §3.2a extension is likely incomplete. In the current no-drop
measurement DB
`/tmp/wcprof-volume-current-clientdbs/clientdbs/qz9egyf79tktoc1oii1yslrbu.db`,
classifying a `call_exec` as "suppressed proxy" when no ordinary span has the
same `dagger.io/dag.digest` gives these high-volume names:

```text
Query.sourceMap              1673
ObjectTypeDef.__withFunction 1431
Function.__withArg           1011
Function.args                 985
Function.returnType           985
Function.sourceModuleName     985
FunctionArg.typeDef           892
FunctionArg.__withSourceMap   774
Function.withArg              566
Query.__functionArg           518
Function.__withSourceMap      472
Function.withDescription      367
TypeDef.withFunction          289
ObjectTypeDef.fields          115
ObjectTypeDef.constructor     115
ObjectTypeDef.functions       115
FieldTypeDef.typeDef           78
ListTypeDef.elementTypeDef     43
EnumTypeDef.members            17
```

Some of these are already covered by current `introspectionInfo` when their
receiver chain resolves through a listed introspection root or an existing
`__*`/`with*` rule (`core/telemetry.go:363-387`,
`core/telemetry.go:401-444`). But several are not named in §3.2a, and the plan
itself says the observed field list is workload-specific.

For a profiler-only predicate, I prefer the robust receiver-type cut: classify
all accessors on the reflection/schema metadata types (`Function`, `TypeDef`,
`FunctionArg`, `ObjectTypeDef`, `InterfaceTypeDef`, `InputTypeDef`,
`FieldTypeDef`, `ListTypeDef`, `EnumTypeDef`, `EnumMemberTypeDef`,
`ScalarTypeDef`) as profile-skipped, ideally constrained as much as the current
call metadata allows to the core reflection schema. The field definitions in
`core/schema/module.go:471-490` and `core/schema/module.go:581-646` are all
type-system metadata accessors, not user work.

The risk is broader type-name-based classification, especially if a user module
can expose a type named `Function` or `TypeDef`. Existing `introspectionInfo`
already has some type-name collision risk at `core/telemetry.go:401-444`, but a
receiver-type-wide predicate makes the blast radius bigger. This is another
reason to keep it profiler-only: a false positive loses fine profiling
granularity, not normal UI telemetry.

### D. Performance

Stamping before the `dagql.IsSkipped(ctx)` early return means
`introspectionInfo` now runs for inherited-skip descendants that currently return
at `core/telemetry.go:32-34`. That resolver walk can hit the cache through
`ReceiverCall` and `resultCallByResultID` as described above. It is outside cache
locks in the proposed design, which is good, but it is still work on the hot
module-load path.

Add a cheap guard and measure it. Because the same bit gates native wcprof too,
the guard should be "no OTel recording and no native wcprof profiling," not just
`!trace.SpanFromContext(ctx).IsRecording()`. If neither profile sink is active,
there is no need to stamp a profile-only bit before the existing `IsSkipped`
return. If normal telemetry still needs its current decision, keep the existing
path unchanged.

### E. Exec-Split And Service-Start Assumption

The plan's "no gate needed" assumption for exec/service emitters is plausible
but must be verified. Exec spans are minted from executor code at
`engine/engineutil/executor.go:121-142` and split at
`engine/engineutil/executor_spec.go:1405-1430`; service-start spans and waits are
minted in `core/services.go:1021-1035` and `core/services.go:995-1012`. These
paths are not schema metadata accessors and have no `CallRequest.SkipProfile`.

I agree they should not be gated in v1, but §9 needs a hard capture assertion:
no skipped-class span originates as `exec`, `exec_phase`, or `service_start`, and
no wait from those paths targets an intentionally skipped op.

## Additional Implementation Notes

### `sharedResult.profSkip` Must Survive Result Lifecycle

The lazy plan is right to key lazy skip on the work/result, not the forcer. But
the implementation must be more explicit about how the bit follows
`sharedResult`.

There are many `sharedResult` construction/copy paths:

- New detached result at `dagql/cache.go:1799-1810`.
- Fork/copy paths at `dagql/cache.go:2431-2440` and `dagql/cache.go:2531-2540`.
- Do-not-cache detached result at `dagql/cache.go:3625-3629`.
- Imported persisted results at `dagql/cache_persistence_import.go:164-176`.
- Persistence snapshots at `dagql/cache_persistence_worker.go:421-440`.
- `initCompletedResult` can adopt an existing cache-backed result at
  `dagql/cache.go:4127-4147`, or copy an existing result call at
  `dagql/cache.go:4148-4162`.

Lazy evaluation is registered later through `registerLazyEvaluation` at
`dagql/cache.go:2779-2792`, including for cache hits at `dagql/cache.go:2000`,
fresh completed results at `dagql/cache.go:4449`, and imported results at
`dagql/cache_persistence_import.go:587-594`.

Required rule: whenever a `sharedResult` carries or stores a `resultCall` that
can later be lazy-evaluated, its `profSkip` must match that `resultCall`'s
profile predicate, or be copied from the source shared result when preserving the
same result call. Do not unconditionally overwrite an adopted existing result's
flag with the current request's `req.SkipProfile` unless the adopted result's
stored `resultCall` is also being replaced by that request. Otherwise lazy
profile decisions can drift from the work class they claim to represent.

This should be covered by tests around imported/persisted lazy results and
cache-backed result adoption, not only the direct miss path.

### Expected Volume After Fix

The plan should not require "back to main exactly" as the success metric. The
wcprof OTel source intentionally still emits bounded spans for kept user work.
From the current no-drop measurement:

```text
current total distinct spans        36807
ordinary spans                       3293
call_exec total                     16589
suppressed-proxy call_exec          15740
visible-proxy call_exec               849
other wcprof spans (lazy/exec/etc.)   336
```

If the skip removes all suppressed-proxy `call_exec`s and their paired
`dagql.publishResult`s, a rough expected post-fix total is:

```text
3293 ordinary
+ 849 visible call_exec
+ 849 visible publishResult
+ 336 lazy/exec/exec_phase
= about 5327 spans
```

That is far below 36.8k and should remove the BSP burst amplifier, but it is not
identical to main's 3362 spans. The acceptance target should be: schema amplifier
gone, residual `call_exec` names are real kept work, structural gate is clean,
and BSP dropped spans go to zero or near-zero. Exact equality with main would
contradict the OTel profiling design.

### Tests To Add

I agree with most of §8. I would add or sharpen:

- Debug-independent predicate test: debug baggage does not disable profiler skip,
  while normal telemetry debug behavior remains whatever `AroundFunc` decides.
- Direct schema accessor test for profiler-only skip without changing ordinary
  `dag.call`, if the separate predicate recommendation is accepted.
- Receiver-type completeness tests for `Function.returnType`,
  `FunctionArg.typeDef`, `ObjectTypeDef.fields/functions/constructor`,
  `FieldTypeDef.typeDef`, `ListTypeDef.elementTypeDef`, and `EnumTypeDef.members`.
- `sharedResult.profSkip` propagation tests for adopted existing shared results,
  copied/forked shared results, and imported lazy results.
- Negative tests that a non-skipped target with an invalid span context still
  emits a gate-observable unresolved wait. This preserves the detector at
  `dagql/otelprof_hooks.go:111-126`.

## Lead Points

A. Debug gating: I agree with the lead. The orphan argument collapses under the
loader code. Prefer debug-independent profiler skip.

B. Extending `introspectionInfo`: I agree with the lead. Keep this profiler fix
isolated unless a UI/normal-telemetry suppression change is explicitly desired.

C. Field-set completeness: I agree with the lead. A separate receiver-type-based
profile predicate is more robust than chasing the module-load field list. Add
collision-aware tests and validate residual names in capture.

D. Performance: I agree with the lead with one adjustment. The guard should
consider both OTel recording and native wcprof profiling, because the same bit
gates both sources in the plan.

E. Exec/service assumption: I agree with the lead. The code paths are separate
and probably fine, but the §9 capture must prove it.

## Final Recommendation

Implement the static `SkipProfile` design, but with a separate, debug-independent
profile predicate and explicit shared-result flag propagation. Gate
`call_exec`, `dagql.publishResult` by consequence, native/OTel waits, and lazy
native/OTel ops from that one stored decision. Do not change loader/replay, do
not add inference, and do not hide invalid-target waits except when the target is
explicitly profile-skipped.

Land only after measurement proves:

- schema metadata `call_exec`/`dagql.publishResult` volume is gone,
- residual wcprof spans are kept user/shared work,
- `OrphanedParents == 0` and `UnresolvedWaitTargets == 0`,
- default BSP drops disappear on the module-load workload,
- exec/service/lazy paths have no skipped-set dangling edges.

