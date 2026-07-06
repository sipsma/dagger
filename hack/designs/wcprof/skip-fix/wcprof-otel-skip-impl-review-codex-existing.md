# wcprof OTel skip implementation plan review - Codex existing

Reviewed plan: `/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-implementer-a7daa7c9-239fd90a/hack/designs/wcprof-otel-skip-impl-plan.md` at `4585bf413d`.

## Verdict

The core design is the right shape: stamp a static work-keyed profiling skip bit before the cache, store it on the shared work record, and gate `call_exec`, `dagql.publishResult`, and all native/OTel waits from that stored target-side decision. That keeps the emitted graph self-consistent and requires zero loader/replay change, which is exactly the no-inference principle.

I would not implement the plan exactly as written. The architecture is sound, but the proposed v1 classifier and the debug-gated rationale have real problems. In particular, the recommended named-field extension under-catches known high-volume schema-walk spans from the measured workload, and the plan's "debug-independent skip would orphan children" argument is false against the current loader.

## REAL ISSUE 1 - HIGH - The recommended 3.2a field list does not actually cut the known volume class

The plan recommends shipping the narrow list in §3.2a: add only `Function.args`, `Function.sourceModuleName`, and `TypeDef.as*` to `introspectionInfo` (`wcprof-otel-skip-impl-plan.md:83-95`). That is not enough for the exact captured module-load workload that motivated the fix.

I re-counted the existing branch capture `/tmp/wcprofvol-branch.jsonl` and applied an approximation of current `introspectionInfo` plus the proposed new names. It catches 12,973 of 16,367 `call_exec` spans and leaves 3,394 `call_exec` spans. Some of the remainder is real work that should stay, but multiple top residuals are plainly reflection/schema metadata:

- `Function.returnType`: 952
- `FunctionArg.typeDef`: 845
- `ObjectTypeDef.functions`: 114
- `ObjectTypeDef.constructor`: 114
- `ObjectTypeDef.fields`: 114
- `TypeDef.withField`: 102
- `FieldTypeDef.typeDef`: 78
- `ListTypeDef.elementTypeDef`: 43

The current classifier only catches specific root fields and specific receiver-type cases (`core/telemetry.go:359-445`). The proposed additions at plan lines 83-86 do not cover these measured names. Because every remaining missed call also carries a paired `publishResult`, this is thousands of spans still left in the burst. That is too much to leave to a "ship 3.2a, validate, then broaden" loop when the misses are already known.

Recommendation: do not ship the narrow list as v1. Either choose §3.2b receiver-type classification now, or define a complete explicit schema-walk list from the measured set before implementation. If §3.2b is considered too broad for normal telemetry, use a separate profiling predicate instead of extending `introspectionInfo` directly.

## REAL ISSUE 2 - MEDIUM/HIGH - The debug-gated orphan rationale is wrong

The plan says a debug-independent predicate would skip `call_exec` while a normal debug span records, causing a kept child to parent to a "non-op span" and trip `OrphanedParents` (`wcprof-otel-skip-impl-plan.md:293-296`). That does not match the loader.

The loader assigns an op id to every deduped span, without filtering by `wcprof.op.kind` (`engine/wcprof/wcotel/loader.go:251-254`). It increments `OrphanedParents` only when the recorded parent span id is absent from the capture (`engine/wcprof/wcotel/loader.go:299-310`). A present normal `dag.call` span is therefore a resolvable parent, not an orphan.

There is a real debug policy question here, but it is not an orphan/gate question:

- If debug normal telemetry records these schema spans, the loader will classify any span with `dagger.io/dag.digest` as an ordinary `call` (`engine/wcprof/wcotel/loader.go:444-446`). So a debug-independent skip of only `call_exec`/`publishResult` may still leave normal debug `dag.call` spans in the analysis.
- If the intent is "debug mode may be high-volume and may profile introspection again", the debug-gated choice is defensible, but it should be documented as a product/diagnostic tradeoff, not as required for graph correctness.
- If the intent is "wcprof profiling stays volume-safe even in debug", then reusing debug-gated `introspectionInfo` is the wrong default.

Recommendation: fix the rationale and make the debug policy explicit. I prefer a debug-independent profiling predicate for the wcprof skip, with a conscious decision about whether normal debug `dag.call` spans should remain analyzable.

## REAL ISSUE 3 - MEDIUM - Extending `introspectionInfo` couples a profiler-volume fix to normal telemetry/UI behavior

The plan acknowledges that extending `introspectionInfo` changes normal telemetry for directly-called schema accessors (`wcprof-otel-skip-impl-plan.md:95`, `:296`). That is a real product-visible change because normal `AroundFunc` returns `WithSkip`/`NoopDone` when `introspectionInfo` is true (`core/telemetry.go:35-38`), and `AroundFunc` is the normal telemetry hook registered on engine/schema servers (`core/modtree.go:589`, `core/schema_build.go:107`).

This may be acceptable, but it is not required by the wcprof fix. A separate `SkipProfile` predicate would isolate the profiler volume decision from dagui/normal span visibility. The caveat is that separate profiling skip does not suppress a normal `dag.call` span if normal telemetry decides to emit one; the loader will still classify that span as `call` via `dag.digest` (`engine/wcprof/wcotel/loader.go:444-446`). For the observed non-debug module-load regression, those normal spans are absent, so this caveat does not block an isolated predicate.

Recommendation: decide this explicitly. If the council wants no UI/normal telemetry change, use a separate profiling predicate and accept that directly-called visible schema accessors can still appear as ordinary normal calls. If the council wants those direct calls suppressed everywhere, extending `introspectionInfo` is fine, but it should be treated as a deliberate normal-telemetry change.

## REAL ISSUE 4 - MEDIUM - The validation plan should hard-check the "not touched" exec/service assumption

The plan says executor split and service start need no skip gate because introspection never triggers them (`wcprof-otel-skip-impl-plan.md:249`). That is probably true for pure schema metadata, but the current branch has exec/service emit sites outside `dagql`: `emitOTelExecSplit` gates only on `dagql.OTelProfActive` (`engine/engineutil/otelprof.go:77-80`), `exec.run` starts when the executor context is recording (`engine/engineutil/executor.go:133-142`), and `service.start`/installer waits also gate only on recording (`core/services.go:995-1011`, `:1027-1035`).

This is not a reason to gate them blindly. Real work under a hidden schema path, such as module typedef container setup, may be exactly the kind of coarse retained work the plan wants. But it means validation must not only inspect residual `call_exec` names (`wcprof-otel-skip-impl-plan.md:284`). It should also assert that any residual `exec.run`, `exec.containerStart`, `exec.processRun`, and `service.start` spans are intentional real work, not skipped-class metadata leaking through a non-`dagql` hook.

Recommendation: add this as a hard validation check in §9, not a casual "verify".

## REAL ISSUE 5 - LOW/MEDIUM - Classification cost moved before inherited skip should be measured in v1

The plan moves `introspectionInfo` before `dagql.IsSkipped` so inherited-skip descendants get their own recipe classification (`wcprof-otel-skip-impl-plan.md:113-127`). That ordering is necessary for the static cut. The lock-safety argument for doing it in `AroundFunc` is also real: `ReceiverCall` can resolve through the cache (`dagql/result_call_frame.go:575-603`), and `resultCallByResultID` takes `egraphMu.RLock` (`dagql/result_call_frame.go:1377-1387`), so calling this under `callsMu`/`lazyMu` would be a bad lock-ordering surface.

But this does mean every inherited-skip descendant now does the receiver-chain walk. The plan notes this as minor and suggests optimizing later (`wcprof-otel-skip-impl-plan.md:300`). I would make the v1 validation measure it, because this workload is exactly the ~33k-call case and the classifier walks result references. This is not a correctness blocker.

## NOISE / Confirmed OK

- Static target-side wait gating is the right cut. Current `c.wait` emits both native waits and OTel wait links for every caller (`dagql/cache.go:3935-3958`), and `EmitOTelWait` deliberately emits targetless waits when the target is invalid so the gate can fail loud (`dagql/otelprof_hooks.go:103-134`). Adding a distinct `oc.profSkip` and gating waits on the target's stored skip bit, not `execSpanCtx.IsValid`, is the correct way to avoid dangling waits without hiding real mixed-recording loss.
- Parent re-homing is safe when `call_exec` and `publishResult` are skipped together. If `beginOTelCallExec` is not called, `callCtx` is not reassigned to a missing span (`dagql/cache.go:3713-3734`), so children naturally parent to the nearest recording ancestor. The loader only treats a parent as orphaned if that parent span is absent (`engine/wcprof/wcotel/loader.go:299-310`).
- The `CallRequest.SkipProfile` plumbing is the right layer. `ObjectResult.call` invokes `s.telemetry(ctx, req)` before `cache.GetOrInitCall` with the same request pointer (`dagql/objects.go:655-678`), and `CallRequest` already carries request-only policy (`dagql/call_request.go:8-19`). Adding the bit to `Clone` is necessary.
- Native symmetry is correct. `wcprof.Op` explicitly permits nil handles, and `(*Op).ID()` returns 0 for nil (`engine/wcprof/record.go:63-65`, `:124-130`), so skipped native `execOp` causing `profOpID == 0` is safe.
- Excluding `ShouldEmitTelemetry` from the skip predicate is correct. It is session/seen-state dependent (`dagql/telemetry.go:48-64`), so using it for profile skip would make shared work's profiled-ness depend on caller order and could drop real repeated executions.
- Excluding current `NoTelemetry` fields is fine for the stated reason. Normal telemetry is skipped before `AroundFunc` when `field.Spec.NoTelemetry` is set (`dagql/objects.go:655-656`), and current `NoTelemetry` fields are also `DoNotCache` proxy fields (`core/object.go:1161-1169`, `:1214-1219`, `:1259-1265`), so they do not hit the cache miss emit path anyway.
- Lazy skip keyed by the shared result is the right extension. The lazy wait/joiner path currently emits native and OTel waits from the shared lazy state (`dagql/cache.go:2951-2973`), and the lazy op is minted before `lazyEvalWaitCh` is published (`dagql/cache.go:2997-3022`). A `sharedResult.profSkip` set from the original request keeps the lazy decision a property of the work, not the forcing caller.

## Bottom line

The plan is directionally sound and keeps the analysis rational: no loader changes, no replay changes, no inferred parents or invented wait targets. The implementation should proceed only after tightening the classifier scope and debug/UI policy. My strongest recommendation is to choose a complete reflection/schema-walk profiling predicate for v1, store/gate it exactly as proposed, and make the validation prove both volume regression removal and structural gate cleanliness on a fresh capture.
