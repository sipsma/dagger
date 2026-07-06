# wcprof OTel skip fix plan review - Codex fresh

Branch/code checked: `4585bf413d5ad09af918b39b0e2b62e95ad02006` in
`wcprof-otel-implementer-chunk4-7ad02bcf-e44a5fe4`.

## Verdict

The fix direction is correct, but it is not a free one-line gate. The branch must stop
emitting `call_exec` / `dagql.publishResult` for DAGQL calls normal telemetry
deliberately suppresses. That can keep the loader/replay zero-inference if the emitted
cut is self-consistent. The naive version has a real hole: `ongoingCalls` are keyed only
by call digest + concurrency key, so a skipped owner and a non-skipped waiter can share
one in-flight execution. If the owner does not emit `call_exec` but the visible waiter
emits a wait edge, the wait target dangles and the hard gate correctly fails.

Cleanest plan: make the skip an emit-side visibility boundary for the cache profiler,
and prevent in-flight singleflight from crossing that boundary. Concretely, compute a
per-call "profile this DAGQL cache execution" decision before `GetOrInitCall`; use it
for `call_exec`, `publishResult`, and that call's cache wait edge; and include that
visibility bit in the in-flight `ongoingCalls` key, or an equivalent mechanism, so a
visible waiter never joins a hidden owner with no `execSpanCtx`.

No loader or replay heuristic is needed. If the singleflight boundary is not handled,
the plan recreates exactly the orphan/unresolved-target class we just made the gate
reject.

## Verified Current Mechanism

My local warmed workload measurement matches the stated diagnosis. For:

`dagger --progress=plain -c 'container | from alpine | with-exec sleep 0 | stdout'`

de-duped by `(traceId, spanId)`:

- base `b442cd2533`: `1417` unique spans, `820` with `dagger.io/dag.call`, `0`
  `wcprof.op.kind`.
- pre-emit `71b69f1f16`: `1417` unique spans, `820` with `dagger.io/dag.call`, `0`
  `wcprof.op.kind`.
- branch `4585bf413d`: `12018` unique spans, `829` with `dagger.io/dag.call`,
  `9857` with `wcprof.op.kind`.

The named high-volume schema/introspection calls are branch-only `call_exec` spans in
that capture, with zero ordinary `dag.call` spans:

- `Function.args`: `291 call_exec`
- `Function.__withArg`: `366 call_exec`
- `Function.sourceModuleName`: `294 call_exec`
- `TypeDef.asScalar/asInterface/asInput/asEnum/asList/asObject`:
  `98/98/95/94/88/34 call_exec`
- `ObjectTypeDef.__withFunction`: `297 call_exec`
- `Query.sourceMap`: `294 call_exec`

The code explains it:

- Normal DAGQL telemetry only calls `s.telemetry(ctx, req)` when
  `!field.Spec.NoTelemetry` (`dagql/objects.go:655`-`665`).
- `core.AroundFunc` returns no span for `dagql.IsSkipped(ctx)`, and returns
  `dagql.WithSkip(ctx), NoopDone` for introspection-classified calls
  (`core/telemetry.go:32`-`38`).
- `core.AroundFunc` also applies ordinary DAGQL telemetry de-dupe
  (`core/telemetry.go:53`-`65`).
- The introspection predicate covers the literal roots and receiver families that
  explain the measured names (`core/telemetry.go:354`-`452`).
- The skip bit is only a context value (`dagql/internal.go:32`-`42`); it does not
  remove the current recording span.
- The wcprof cache emit path starts `call_exec` whenever `OTelProfActive(callCtx)` is
  true (`dagql/cache.go:3713`-`3734`), and `OTelProfActive` is only
  `trace.SpanFromContext(ctx).IsRecording()` (`dagql/otelprof_hooks.go:41`-`43`).
- `publishResult` is emitted whenever `oc.execSpanCtx.IsValid()`
  (`dagql/cache.go:3999`-`4029`).
- Cache wait edges are emitted from the same layer with no skip predicate
  (`dagql/cache.go:3921`-`3958`).

## Parent Edges

For ordinary DAGQL calls inside an introspection/skipped subtree, parent-edge closure is
mostly favorable. `ObjectResult.call` replaces `ctx` with the telemetry-returned context
before entering `GetOrInitCall` (`dagql/objects.go:655`-`665`), and `WithSkip` propagates
as a context value. Descendant DAGQL calls therefore see `dagql.IsSkipped(ctx)` and
normal telemetry no-ops again.

If `call_exec` is not started for a skipped call, there is no skipped span id for a later
child to accidentally parent to. The context still carries the nearest existing
recording ancestor, not a removed skipped span. `context.WithoutCancel` preserves values,
and the cache's lease helpers do not rebuild from `context.Background`
(`dagql/operation_lease.go:26`-`31`, `dagql/cache.go:3713`-`3735`). So simple parent
orphans are not the main danger.

Two caveats:

- `field.Spec.NoTelemetry` is not currently represented as a skip context. `objects.go`
  just avoids calling `s.telemetry` (`dagql/objects.go:655`). Today the visible
  `NoTelemetry` proxy fields I checked are also `DoNotCache` and therefore return before
  the wcprof `call_exec` path (`core/object.go:1161`-`1169`,
  `core/object.go:1214`-`1219`, `core/object.go:1259`-`1265`,
  `dagql/cache.go:3601`-`3651`), but the fix should still plumb an explicit
  "telemetry suppressed" bit or bool if `NoTelemetry` is part of the policy.
- Do not globally redefine `OTelProfActive` as `recording && !dagql.IsSkipped(ctx)`.
  `dagql.WithSkip` is also used to hide internal module/runtime plumbing
  (`core/modfunc.go:866`-`875`, `core/sdk.go:253`-`305`,
  `core/sdk/module_typedefs.go:99`-`120`). Some of that plumbing leads to the exec path
  that makes user process time first-class. The safe scope for this fix is the DAGQL
  cache `call_exec` / `publishResult` / associated cache wait edge, not a blanket
  suppression of executor/service/lazy spans.

## Wait Edges

This is the real acid-test risk.

`ongoingCalls` are shared by `callConcurrencyKeys{callKey, concurrencyKey}` only
(`dagql/cache.go:1347`-`1350`, `dagql/cache.go:3674`-`3677`). They are not keyed by
telemetry visibility, skipped state, session, or whether a `call_exec` was emitted.
The `ongoingCall` stores exactly one `execSpanCtx` as the wait target
(`dagql/cache.go:1756`-`1783`). Joiners see that object under `callsMu` and call
`c.wait(...)` (`dagql/cache.go:3689`-`3704`).

`EmitOTelWait` intentionally emits a link even when the target span context is invalid,
so the loader reports an unresolved target and the gate fails
(`dagql/otelprof_hooks.go:103`-`134`,
`engine/wcprof/wcotel/loader.go:356`-`405`,
`engine/wcprof/wcotel/gate.go:132`-`140`). That behavior is correct for incomplete data.
It means the skip fix cannot leave a visible waiter pointing at an un-emitted hidden
owner.

I do not think "this can never happen" is established. For introspection-classified
calls whose classification is a pure function of the `ResultCall`, the skipped set is
likely closed: the same call digest should classify the same way for every caller. But
`dagql.IsSkipped(ctx)` is broader than introspection. The codebase uses `WithSkip` as an
external context policy for internal SDK/module plumbing (`core/modfunc.go:866`,
`core/sdk.go:253`, `core/sdk/module_typedefs.go:99`). That means the same underlying
cacheable call can plausibly be reached once under a hidden context and once under a
visible context while sharing the same `callKey`.

Therefore:

- If a hidden caller owns the `ongoingCall` and no `call_exec` is emitted, a visible
  joiner must not emit a wait to `oc.execSpanCtx == invalid`.
- Silently dropping that visible wait is not a rational-data fix; it under-serializes
  visible blocked time and hides a real dependency.
- Retroactively emitting the hidden owner's `call_exec` when the visible joiner arrives
  is also not clean: child spans may already have started under the old context, so the
  late span cannot restore faithful nesting.

The clean emit-side rule is to prevent hidden and visible cache executions from sharing
the same in-flight singleflight record. Add the profile-visibility bit to
`callConcurrencyKeys`, or use an equivalent split, while leaving the persistent cache
identity alone. Then:

- hidden owner + hidden waiter: no `call_exec`, no `publishResult`, no wait edge.
- visible owner + visible waiter: normal profiled graph with resolvable wait target.
- visible owner + hidden waiter: the hidden wait edge is suppressed; the visible graph
  stays self-consistent.
- hidden owner + visible waiter: they do not join the same in-flight `ongoingCall`; the
  visible side either starts its own profiled execution or later observes a cache hit.

That is an emission/engine-behavior boundary, not loader inference. It may cause rare
duplicate in-flight work across the hidden/visible boundary, but `getOrInitCall` already
accepts occasional redundant execution to avoid tighter locking (`dagql/cache.go:3707`-
`3711`), and the alternative is either dangling edges or unfaithful dropped waits.

## De-dupe Is Not The Same As Skip

One point needs tightening in the plan wording. "Same suppression decision as ordinary
telemetry" should mean the visibility/suppression decision (`IsSkipped`,
introspection-classified, `NoTelemetry`), not ordinary DAGQL span de-dupe via
`ShouldEmitTelemetry`.

For wcprof, a visible cache miss is a real execution node and may be a wait target. If
`ShouldEmitTelemetry` suppresses a later visible execution because the normal UI already
saw the digest, dropping `call_exec` would lose causal data for real work. That is a
profiling correctness issue, not just a UI-volume decision. Keep normal `dag.call`
de-dupe and wcprof execution-node visibility separate unless there is a proof that the
de-duped visible call cannot execute or be waited on.

## Goal And Simplicity

This preserves the north star if scoped correctly. Erik is fine losing fine-grained
introspection/schema-building profiling, and those calls are not the user-work headline.
The executor split can still make slow user commands first-class as long as this fix does
not globally suppress executor spans under every `WithSkip` context.

This is not massive, but it is more than changing `if OTelProfActive(callCtx)`.
Minimum clean shape:

1. Compute/cache a per-call wcprof-OTel visibility decision before cache evaluation,
   including `NoTelemetry` plumbing because `NoTelemetry` currently has no context bit.
2. Use that decision for `call_exec`, `publishResult`, and that call's cache wait edge.
3. Split in-flight `ongoingCalls` by that decision, or otherwise guarantee visible
   waiters never target hidden un-emitted owners.
4. Do not change loader/replay to infer around missing nodes; keep the hard gate.
5. Add a regression test for the hidden-owner/visible-waiter case, plus a volume test or
   fixture assertion that the named schema/introspection classes no longer appear as
   `wcprof.op.kind=call_exec`.

Final answer to the council questions:

- Correctness: correct direction, but naive caller-only skipping has a dangling-wait
  hole. Clean if the in-flight singleflight boundary is part of the cut.
- Zero inference: yes, if fixed in emission as above. No loader/replay accommodation is
  needed or acceptable.
- Goal: preserved, provided the fix is scoped to DAGQL cache profiling and does not
  globally suppress exec/user-work spans under `dagql.WithSkip`.
- Simplicity: moderate, not massive. The extra piece is the visibility bit/plumbing and
  the `ongoingCalls` split; without that, the plan is not sound.
