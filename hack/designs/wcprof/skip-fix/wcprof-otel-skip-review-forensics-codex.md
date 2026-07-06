# wcprof OTel skip fix review - Codex forensics

Reviewer: Codex, forensic investigator

Scope: review only. I verified current branch code and existing no-drop trace
artifacts. I did not change product code.

## Verdict

The proposed direction is correct, but only if implemented as a coherent emit-side
projection. "Do not start `call_exec` for suppressed calls" by itself has a real
hole: `dagql/cache.go:3958` would still call `EmitOTelWait` with an invalid
`oc.execSpanCtx`, and `dagql/otelprof_hooks.go:111-126` deliberately emits that
invalid-target wait so the loader/gate fail loud. That would recreate the same
hard failures as missing data, but now self-inflicted.

The clean fix is still simple: carry an explicit "wcprof OTel intentionally
suppressed" decision for the cache execution, do not emit `call_exec`, do not emit
`dagql.publishResult`, and do not emit waits whose caller or target is inside that
intentionally hidden projection. Leave the loader/gate strict. Do not teach the
loader to infer around missing nodes.

With that condition, this keeps the analysis zero-inference: the trace faithfully
contains the projected graph we chose to emit, and the loader remains a rational
function of that data. The cost is intentional loss of fine-grained profiling for
introspection/schema-building work, which Erik says is acceptable. User work
remains first-class because ordinary/user spans and non-suppressed wcprof spans
are retained; hidden schema work may be charged coarsely to visible ancestors if a
visible op waits on it, unless we choose the more complex consumer-promoted option
below.

## Premise rechecked

I rechecked the no-drop measurement DBs directly:

```text
current branch top-level DB: /tmp/wcprof-volume-current-clientdbs/clientdbs/qz9egyf79tktoc1oii1yslrbu.db
main top-level DB:           /tmp/wcprof-volume-main-clientdbs/clientdbs/gl0yrjzji8tb09e5fbpm3lx57.db
pre-emit top-level DB:       /tmp/wcprof-volume-preemit-clientdbs/clientdbs/psvm0m0ih59ngcifneb2obaq0.db
```

Distinct span counts:

```text
current: 36807
main:     3362
preemit:  3571
```

Current branch wcprof span kinds:

```text
internal   16589
call_exec  16589
lazy         276
exec_phase    40
exec          20
```

The high-volume names are present in current as `wcprof.op.kind=call_exec`, not as
ordinary `dag.call` spans:

```text
Query.sourceMap              1673
ObjectTypeDef.__withFunction 1431
Function.__withArg           1011
Function.sourceModuleName     985
Function.args                 985
Function.withArg              566
TypeDef.asScalar              190
TypeDef.asObject              190
TypeDef.asList                190
TypeDef.asInterface           190
TypeDef.asInput               190
TypeDef.asEnum                190
```

Main emitted none of those names in the top-level DB for the same workload.

## Code facts

Normal call telemetry is gated before cache execution:

- `dagql/objects.go:655-665` calls `s.telemetry(ctx, req)` only when
  `!field.Spec.NoTelemetry`.
- `core/telemetry.go:32-38` returns `NoopDone` for an already skipped context, and
  returns `dagql.WithSkip(ctx), NoopDone` for introspection-classified calls.
- `core/telemetry.go:39-43` suppresses meta calls.
- `core/telemetry.go:60-64` suppresses repeated non-DoNotCache call digests.
- `dagql/telemetry.go:48-64` implements that repeated-key predicate.

The introspection classifier covers the problem class:

- `core/telemetry.go:363-387` marks roots such as `__schema`,
  `currentTypeDefs`, `function`, `typeDef`, `sourceMap`, `__function`,
  `__functionArg`, `__objectTypeDef`, `__scalarTypeDef`, etc.
- `core/telemetry.go:401-444` marks receiver-chain schema builders on
  `Function`, `TypeDef`, `FunctionArg`, `ObjectTypeDef`, and related type-def
  objects, including `Function.__*`, `Function.withArg`, `TypeDef.with*`, and
  other `__*` fields when not in debug mode.

The wcprof OTel cache emit does not honor that predicate today:

- `dagql/objects.go:583-599` builds `CallRequest` with `DoNotCache`,
  `IsPersistable`, and `PassthroughTelemetry`, but no `NoTelemetry` or
  telemetry-suppressed bit.
- `dagql/call_request.go:8-19` confirms `CallRequest` currently has no such bit.
- `dagql/cache.go:3601-3650` returns early for `req.DoNotCache`.
- `dagql/cache.go:3679-3687` returns early on cache hit.
- `dagql/cache.go:3731-3734` starts `beginOTelCallExec` on every cache miss under
  `OTelProfActive(callCtx)`.
- `dagql/otelprof_hooks.go:41-43` defines `OTelProfActive` solely as
  `trace.SpanFromContext(ctx).IsRecording()`.
- `dagql/cache.go:4013-4018` starts `dagql.publishResult` whenever
  `oc.execSpanCtx.IsValid()`.
- `dagql/cache.go:3943-3949` says wait edges are emitted from the cache layer
  specifically because telemetry-suppressed callers never enter `AroundFunc`.

The gate must not be weakened:

- `engine/wcprof/wcotel/gate.go:51-58` defines unresolved waits and orphaned
  parents as hard invariants.
- `engine/wcprof/wcotel/gate.go:132-140` hard-fails them.
- `engine/wcprof/wcotel/loader.go:299-310` counts an op with a present but absent
  parent span as an orphan.
- `engine/wcprof/wcotel/loader.go:356-380` counts non-lock waits whose target
  span is absent as unresolved wait targets.

## Acid test: parent edges

For introspection-root skipping, parent closure is good in the normal DagQL path.
`dagql.WithSkip` is a context value only (`dagql/internal.go:34-42`), and
`core.AroundFunc` propagates it by returning `dagql.WithSkip(ctx)` for the
classified call (`core/telemetry.go:35-38`). The cache miss path derives the
detached execution context from that context (`dagql/cache.go:3713-3715`) and the
resolver runs under `oc.sharedWorkCtx` (`dagql/cache.go:3765-3768`). Since
`context.WithoutCancel` preserves values, descendants see `dagql.IsSkipped(ctx)`
and `core.AroundFunc` returns no span at `core/telemetry.go:32-34`.

So if the skipped call's `call_exec` is not emitted, descendants of that skipped
subtree also should not emit ordinary call telemetry or wcprof `call_exec`, as long
as the wcprof suppression checks the same `dagql.IsSkipped(ctx)` state. No
non-skipped child should parent to a missing skipped `call_exec` in this path.

`NoTelemetry` is different: `dagql/objects.go:655-665` simply avoids invoking
`AroundFunc`; it does not set `dagql.WithSkip`. That is fine. A `NoTelemetry`
wrapper can intentionally have visible descendants. The module entrypoint proxies
show that pattern: they set `NoTelemetry` and `DoNotCache` at
`core/object.go:1161-1169`, `core/object.go:1214-1218`, and
`core/object.go:1259-1265`, then call inner canonical user work with
`dagql.WithNonInternalTelemetry(ctx)` at `core/object.go:1224-1248` and
`core/object.go:1271-1272`. Suppressing only the wrapper's wcprof node does not
create an orphan; visible inner work will parent to the nearest remaining recording
span.

One extra corroborating point: `core/modfunc.go:866-875` uses
`dagql.WithSkip(ctx)` only for hidden runtime-loading plumbing, then calls
`runtime.Call(ctx, ...)` with the original context. That preserves user function
execution as visible work while hiding the schema/runtime setup.

## Acid test: wait edges

This is the real risk. Current code emits wait links unconditionally after the
wait completes:

- `dagql/cache.go:3921-3958` computes the wait reason and calls
  `EmitOTelWait(ctx, oc.execSpanCtx, reason, ...)`.
- `dagql/otelprof_hooks.go:103-110` returns only when the waiter span is not
  recording.
- `dagql/otelprof_hooks.go:111-126` intentionally emits the wait even when the
  target span context is invalid, so mixed recorded/unrecorded work fails the
  structural gate instead of silently losing a wait.

Therefore a naive skip that leaves `oc.execSpanCtx` invalid and does nothing else
will create unresolved wait targets. This is not hypothetical; it follows directly
from the hook's stated behavior and the loader/gate invariants above.

I also checked the current no-drop trace for mixed-shape signals. I classified a
`call_exec` as "suppressed proxy" when it had `dagger.io/dag.digest` and no
ordinary non-wcprof span in the same trace had the same digest. Results:

```text
call_exec total                 16589
suppressed-proxy call_exec      15740
visible-proxy call_exec           849
wait links total                17196
wait links to suppressed proxy  15887
wait links to visible proxy      1023
wait links to other targets       286
```

Reasons for waits to suppressed-proxy targets:

```text
call_exec     15740
singleflight    147
```

Top waiters targeting suppressed-proxy nodes:

```text
ordinary POST /query              7445
call_exec ModuleSource.asModule   6647
call_exec Function.withArg         518
call_exec Query.currentTypeDefs    442
call_exec TypeDef.withFunction     289
call_exec TypeDef.withField        202
```

The `ordinary POST /query` rows do not prove a non-skipped DagQL call waited on
skipped work; current `EmitOTelWait` explicitly attaches a suppressed caller's
wait to the nearest recording ancestor (`dagql/otelprof_hooks.go:91-94`). The
absence of any ordinary same-digest span for the suppressed-proxy targets suggests
the measured module-load trace did not have a visible consumer of those target
calls.

But the code permits mixed ownership in principle. The singleflight key is only
`callKey` plus `req.ConcurrencyKey` (`dagql/cache.go:3674-3677`), and joining an
ongoing call at `dagql/cache.go:3694-3703` has no telemetry-visibility dimension.
A skipped owner and a non-skipped consumer can theoretically share an
`ongoingCall` if they overlap on the same call key and concurrency key. The code
does not prove that impossible.

## Recommended emit-side shape

Use an explicit suppression decision in the cache-layer OTel profile path.

Minimum shape:

1. Compute or carry a boolean such as `OTelProfileSuppressed` for the current
   call. It should cover `dagql.IsSkipped(ctx)` and `field.Spec.NoTelemetry`.
   If "same as ordinary telemetry" must include meta/repeated-key suppression too,
   expose that decision from the telemetry layer rather than duplicating
   `core.introspectionInfo` inside `dagql`.
2. On cache miss, start `beginOTelCallExec` only when telemetry is recording and
   the current call is not suppressed.
3. Store on `ongoingCall` whether the missing `execSpanCtx` is intentional
   suppression, not accidental loss or mixed-recording.
4. `dagql.publishResult` remains naturally suppressed by the existing
   `oc.execSpanCtx.IsValid()` check at `dagql/cache.go:4016-4018`.
5. In `wait`, do not call `EmitOTelWait` when the waiter context is suppressed, or
   when the target execution was intentionally suppressed. Do not change
   `EmitOTelWait` to drop all invalid targets globally; that would hide the
   legitimate mixed-recording/capture-loss failure mode its comment is protecting.

This is a small cache/telemetry interface change, not a loader or replay redesign.
The loader remains strict. The emitted graph is self-consistent because hidden
nodes and edges into hidden nodes are omitted together.

I would avoid adding telemetry visibility to `callConcurrencyKeys`. That changes
execution sharing behavior for telemetry's sake. The fix should project the
profile graph, not force duplicate work.

## If the mixed visible-consumer case matters

If we decide a visible caller waiting on hidden schema work must retain precise
wait attribution, the cleaner high-fidelity option is "consumer-promoted emit":
when a non-suppressed caller observes work first claimed by a suppressed caller,
emit an opaque/profile-only execution node for that shared work and target the
visible wait at it.

That is more complex:

- It needs stored owner timing and an emit-safe parent choice.
- The span may start after the work actually began unless we use explicit start
  timestamps.
- Children that already ran under the hidden owner will not naturally reparent to
  the promoted node.
- It creates a different emit shape than the current "one call_exec before
  publishing ongoingCall" invariant at `dagql/otelprof_hooks.go:55-57`.

I do not recommend this as the first fix unless product requires that attribution.
The measured module-load trace does not prove a visible same-digest consumer for
the suppressed targets; the main proven regression is volume from hidden
schema-building work.

## Answers to Erik's questions

1. Is skipping this data correct?

Yes, with the wait-edge caveat. It is the right fix for the self-inflicted volume
regression. Naive `call_exec`-only skipping is incorrect because it dangles waits.
Coherent emit-side projection is correct.

2. Can we skip it while keeping zero inference?

Yes. Skip at emit time and keep the loader/gate unchanged. The analysis then makes
no guesses about hidden introspection work. It analyzes the emitted projected graph.

3. Is it simple?

Mostly yes. It is a small-to-moderate emit-side change: carry a suppression bit
from `objects.go`/telemetry classification into `CallRequest` or context, gate
`beginOTelCallExec`, record intentional target suppression on `ongoingCall`, and
gate cache wait emission. It is not a replay or loader rewrite.

4. What is the principled fix?

Principled short-term fix: hide the same high-volume telemetry-suppressed class
from the wcprof OTel source and suppress all profile edges into that hidden class,
with an explicit "intentional suppression" state so genuine target loss still
fails hard.

Principled high-fidelity alternative: consumer-promoted opaque execution nodes for
visible consumers of hidden shared work. More accurate for mixed cases, but more
complex and not required by the evidence we have.

