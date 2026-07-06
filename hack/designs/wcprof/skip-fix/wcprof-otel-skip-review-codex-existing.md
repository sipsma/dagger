# wcprof OTel Skip Fix Plan Review - Codex Existing

Source references are against current implementation commit
`4585bf413d5ad09af918b39b0e2b62e95ad02006` in
`wcprof-otel-implementer-chunk4-7ad02bcf-e44a5fe4`.

## Verdict

The proposed direction is correct, but only if the skip is implemented as a
closed emit-side abstraction: for intentionally unprofiled DagQL work, omit the
`call_exec` span, omit its `dagql.publishResult`, and omit every wait edge whose
target is that omitted `ongoingCall`.

That keeps the analyzer rational. The loader/replay do not need any heuristic,
fallback, or "ignore missing introspection target" rule. The retained graph is a
coarser graph: skipped schema/introspection work is charged to the nearest
retained ancestor's elapsed/self time instead of being decomposed into
`Function.*` / `TypeDef.*` rows. Erik has explicitly accepted that loss of
fine-grained profiling for this class, and it is the right tradeoff for the
telemetry volume contract.

Naive skipping is not safe. Dropping the target span while leaving parent refs
or wait links recreates the exact gate failures we just fixed: orphaned parents
and unresolved wait targets.

## Evidence

Normal telemetry suppression sits above the cache:

- `ObjectResult.call` calls `s.telemetry` only when `!field.Spec.NoTelemetry`,
  then always proceeds to `cache.GetOrInitCall` (`dagql/objects.go:655-678`).
- `FieldSpec.NoTelemetry` is documented as suppressing `AroundFunc`
  (`dagql/objects.go:916-919`).
- `core.AroundFunc` returns immediately when `dagql.IsSkipped(ctx)` is already
  set (`core/telemetry.go:32-34`).
- For introspection, `core.AroundFunc` returns `dagql.WithSkip(ctx)` and
  `NoopDone` (`core/telemetry.go:35-38`).
- `dagql.WithSkip` is only a context value; it does not remove the current
  recording span (`dagql/internal.go:32-42`).
- `introspectionInfo` classifies roots such as `__schema`, `currentTypeDefs`,
  `function`, `typeDef`, `sourceMap`, and the `__*TypeDef` helpers
  (`core/telemetry.go:354-387`), and it also suppresses the high-volume
  `Function` / `TypeDef` builder and `__*` descendants when debug baggage is not
  set (`core/telemetry.go:401-444`).

The wcprof OTel cache emit currently ignores that suppression:

- `OTelProfActive` is only `trace.SpanFromContext(ctx).IsRecording()`
  (`dagql/otelprof_hooks.go:41-43`).
- `getOrInitCallInner` starts `call_exec` whenever `OTelProfActive(callCtx)` is
  true (`dagql/cache.go:3725-3734`).
- `beginOTelCallExec` marks the span `ui.passthrough` and
  `wcprof.op.kind=call_exec` (`dagql/otelprof_hooks.go:58-65`).
- `c.wait` unconditionally calls `EmitOTelWait` after the wait
  (`dagql/cache.go:3943-3958`).
- `dagql.publishResult` is emitted whenever `oc.execSpanCtx.IsValid()`
  (`dagql/cache.go:4013-4018`).

The gate will not tolerate a half-skip:

- The loader counts a span with a non-empty recorded parent whose parent span is
  absent as `OrphanedParents` (`engine/wcprof/wcotel/loader.go:299-310`).
- The loader counts a non-lock wait link whose target span is absent as
  `UnresolvedWaitTargets` (`engine/wcprof/wcotel/loader.go:368-380`).
- The structural gate hard-fails both (`engine/wcprof/wcotel/gate.go:132-140`).

I also ran a post-hoc closure check over the existing full no-drop branch capture
`/tmp/wcprofvol-branch.jsonl`. Removing obvious introspection-class
`call_exec` spans plus their direct `dagql.publishResult` children, while
leaving existing links alone, leaves 13,494 wait links into removed targets and
one remaining parent ref into the removed set. That post-hoc deletion is not the
same as a correct emit change, because children would parent differently if the
target span had never been minted. But it proves the acid-test point: the fix
must suppress target spans and their wait edges coherently.

## Acid Test

### Parent Edges

For introspection roots and descendants, the subtree is effectively closed in
normal DagQL control flow. `ObjectResult.call` replaces `ctx` with the
`AroundFunc` return value before invoking the resolver (`dagql/objects.go:655-679`).
Once `core.AroundFunc` returns `dagql.WithSkip(ctx)` for an introspection root,
descendant calls inherit that context, and later `AroundFunc` invocations return
`NoopDone` at the `IsSkipped` check (`core/telemetry.go:32-38`). There is no
public "unskip" operation in `dagql/internal.go`.

So if `call_exec` is not minted for a skipped call, ordinary child spans created
inside that call will parent to the nearest existing ancestor span, not to a
missing `call_exec`. Descendant DagQL calls should also be skipped if the cache
emit checks the skip context.

`NoTelemetry` is not a subtree marker. The current uses are entrypoint proxy
fields, and all three are also `DoNotCache` pure-routing fields
(`core/object.go:1161-1169`, `core/object.go:1214-1219`,
`core/object.go:1259-1265`). The cache's `DoNotCache` path returns before the
`call_exec` emit site (`dagql/cache.go:3601-3625`), so those current proxies are
already not the source of the volume. The inner real calls intentionally run via
`WithNonInternalTelemetry` and should remain profile-visible
(`core/object.go:1224-1249`, `core/object.go:1271-1285`).

If the implementation wants future-proof "same as ordinary telemetry" semantics
for `NoTelemetry`, it should carry that as a per-request flag, not as a subtree
`WithSkip`.

### Wait Edges

This is the real hazard. `ongoingCalls` is keyed by semantic call key plus
session concurrency key (`dagql/cache.go:3674-3677`), and each `ongoingCall`
stores exactly one `execSpanCtx` wait target (`dagql/cache.go:1775-1783`,
`dagql/cache.go:3755-3758`). Waiters link to that stored target
(`dagql/cache.go:3921-3958`).

Therefore, if the leader is skipped and no `call_exec` is minted, a later waiter
must not emit a wait link to `oc.execSpanCtx` unless that target exists. Emitting
a zero/invalid target is correct for accidental non-uniform recording, but it is
wrong for intentional suppression: it would make the gate fail a trace that is
complete at the chosen coarser abstraction.

The clean emit rule is:

- If an `ongoingCall` is intentionally unprofiled, record that fact explicitly
  on `ongoingCall`.
- For such an `ongoingCall`, do not emit `call_exec`, do not emit
  `dagql.publishResult`, and do not emit any `call_exec` / `singleflight` wait
  link from any consumer.
- Preserve the existing "invalid target => gate-observable unresolved wait"
  behavior for accidental cases where the target should have existed but did
  not.

That requires distinguishing "invalid because intentionally suppressed" from
"invalid because telemetry was mixed/lost." The current code has only
`execSpanCtx.IsValid()` and cannot tell those cases apart.

## Recommended Shape

I would not implement "emit the singleflighted node iff a non-suppressed
consumer observes the work" for v1. It sounds attractive but is not simple in
this architecture:

- The leader publishes `ongoingCall` after the target span is minted
  (`dagql/cache.go:3725-3763`), which is Invariant T.
- A future visible waiter is unknowable when the skipped leader starts.
- Minting the `call_exec` later would lose the true start time and, more
  importantly, any resolver sub-call spans that already started would not be
  causally parented under it.
- Pre-minting and later deciding whether to keep/export it would require a much
  larger custom buffering/export shape and would undermine the volume win.

The cleaner rule is "this semantic class of work is intentionally unprofiled;
all waits to it are also unprofiled." That is a faithful coarse emit, not loader
inference. If a visible high-level operation is slow because it is doing schema
construction, that time remains in the retained ancestor's elapsed/self time
instead of being explained as `Function.args` or `TypeDef.asObject`.

Concrete implementation direction:

1. Add an explicit request/ctx predicate for wcprof OTel visibility. It should
   cover `dagql.IsSkipped(ctx)` and, if desired, `FieldSpec.NoTelemetry` as a
   per-request field on `CallRequest`.
2. Do not blindly use every `AroundFunc` no-op as the wcprof predicate.
   In particular, `ShouldEmitTelemetry` is an ordinary-span de-duplication
   policy (`dagql/telemetry.go:48-63`, `core/telemetry.go:58-65`); treating it
   as a profiling data suppression rule would throw away cache-miss execution
   data for repeated visible work. The volume issue under review is the
   introspection/schema-building class, not normal repeated visible calls.
3. Store an explicit `otelProfileSuppressed` (name not important) on
   `ongoingCall` when no `call_exec` is minted due intentional suppression.
4. In `c.wait`, skip `EmitOTelWait` when `oc.otelProfileSuppressed` is true.
   If it is false and the current span is recording but `execSpanCtx` is invalid,
   keep the existing gate-observable unresolved-target behavior.
5. Keep `dagql.publishResult` tied to a valid `execSpanCtx`, as it is today
   (`dagql/cache.go:4013-4018`).
6. Decide explicitly whether lazy/deferred eval should follow the same skip
   boundary. `evaluateOne` currently mints `lazy` spans and lazy wait links based
   only on `OTelProfActive` (`dagql/cache.go:2951-2973`,
   `dagql/cache.go:3006-3020`, `dagql/cache.go:3094-3104`). That was not the
   main volume spike, but if the goal is "skip this subtree," lazy needs the same
   intentional-suppression distinction to avoid a smaller version of the same
   dangling-target problem.

## Questions Answered

### Correctness

Skipping this class is correct if it is coherent. It is not a free one-line
change. The hole is wait-target closure: `ongoingCall` can have no target if the
leader was skipped, and callers currently emit waits to whatever `execSpanCtx`
is stored. That must become an intentional no-op, not an unresolved wait.

### Zero Inference

Yes, the analysis can remain zero-inference. The loader/replay should be
unchanged. The engine should emit a smaller, closed graph. No loader-side
"ignore introspection orphan" or "treat missing wait target as skipped" rule
should be added.

### Goal Preservation

Yes, with the accepted loss of granularity. User work remains first-class:
`exec.run`, `processRun`, services, lazy work, and visible DagQL calls are still
profiled. Hidden schema/introspection work becomes coarse ancestor self-time.
That may make module loading show up as `ModuleSource.asModule` / `POST /query`
rather than `Function.args`, but it will not hide user container/process work.

### Simplicity

Moderate and local if done as above. It is mostly a DagQL cache/request plumbing
change plus tests. It is not a replay/loader change.

The massive alternative is "emit only if a future visible consumer appears,"
which would require delayed/cancellable span export or late synthetic targets
with backdated timestamps and repaired parentage. I do not recommend that.

## Test Expectations

Before landing, I would require focused emit-path tests:

- Literal `__schema` emits no wcprof `call_exec` or `dagql.publishResult`.
- A synthetic skipped root with skipped descendants emits no `call_exec`,
  `publishResult`, or wait links, and the loader gate reports zero orphaned
  parents / unresolved targets.
- A synthetic skipped leader plus skipped joiner on the same `ongoingCall`
  emits no wait link on the retained ancestor span.
- A synthetic intentionally-suppressed `ongoingCall` does not call
  `EmitOTelWait`, while an accidentally invalid target still produces a
  gate-observable unresolved wait.
- Existing visible singleflight tests still emit `call_exec`, `publishResult`,
  and wait links.
