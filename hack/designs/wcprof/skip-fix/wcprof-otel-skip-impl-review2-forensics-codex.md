# wcprof OTel skip implementation plan v2 - Codex round 2 review

Reviewed v2 of `hack/designs/wcprof-otel-skip-impl-plan.md` from the implementer worktree, plus the current branch code in this worktree. This is review only; I did not modify product code.

## Verdict

The v2 architecture is the right one: static per-recipe profiler skip, stamped on `CallRequest`, applied symmetrically to native and OTel, with no loader or replay changes. It preserves the zero-inference rule because the loader still analyzes exactly the spans emitted; the engine just emits a smaller, self-consistent graph.

I would not rubber-stamp the exact plan without four amendments:

1. `sharedResult.profSkip` needs a central, synchronized ownership model tied to `resultCall`; otherwise N2 is too easy to implement incompletely or with a Go data race.
2. The receiver-type audit must include metadata descendants outside the 11 listed reflection types, especially `SourceMap`, `FunctionCall`, and `FunctionCallArgValue`, or the zero-residual check can miss real residual introspection spans.
3. The `(receiverTypeName, field)` memoization proposed for performance does not avoid the expensive part of classification, which is resolving the receiver type.
4. Imported lazy results can default false without dangling, but that is a deliberate volume leak. I would accept it only with an explicit measurement; recomputation needs a dependency-safe design because the predicate lives in `core` and import lives in `dagql`.

## Changes Review

### N1 outer native call: correctly incorporated

Confirmed. `getOrInitCall` currently starts the outer native `OpKindCall` before entering the inner cache path at `dagql/cache.go:3559-3577`. Gating only the inner `execOp` would leave native profiler ops for the skipped class and break the native/OTel oracle. Adding `|| req.SkipProfile` to the early return at `dagql/cache.go:3559` is the right small fix. The inner path already tolerates a nil `profOp`: native call-exec starts separately at `dagql/cache.go:3717-3724`, waits use `oc.profOpID` at `dagql/cache.go:3940-3942`, and publish follows `oc.profOpID != 0` at `dagql/cache.go:4003-4011`.

### N2 result-adoption provenance: right principle, incomplete implementation spec

The v2 principle is correct: lazy profiling must follow the stored producer result, not the current caller's `req`. Lazy waits use target state, not caller state: joiners read `shared.lazyEvalProfOpID` and `shared.lazyEvalSpanCtx` at `dagql/cache.go:2951-2973`, leaders mint lazy ops from `shared.loadResultCall()` at `dagql/cache.go:2980-3019`, and waiters must gate on the target's skip flag.

The plan under-specifies the mutation surface. `sharedResult.resultCall` is not only assigned in the three `initCompletedResult` cases. Current code also sets or replaces frames at:

- `dagql/cache.go:1800-1809` (`newDetachedResult`, direct field assignment)
- `dagql/cache.go:1965-1976` (`attachResult` normalized frame)
- `dagql/cache.go:2349-2356` (nth-value detached result frame)
- `dagql/cache.go:2431-2436` and `dagql/cache.go:2531-2568` (shared result replacement/copy paths)
- `dagql/cache.go:4130-4161` (canonical adoption, copied existing value frame, request frame)
- `dagql/cache_egraph.go:1064` (teach content digest)
- `dagql/cache_egraph.go:1484-1486` (index wait result request frame)
- `dagql/cache_egraph.go:1652-1655` (remove clears frame)
- `dagql/cache_persistence_import.go:164-176` (persisted import)
- `dagql/cache_persistence_worker.go:430-437` (persistence encode helper)

Because `resultCall` is guarded by `resultCallMu` (`dagql/cache.go:1444-1452`), `profSkip` should be guarded with the same ownership or be atomic. If lazy code reads `shared.profSkip` under `lazyMu` while frame-update paths write it under `resultCallMu` or no lock, the implementation will have a data race. The clean implementation is a central helper or paired accessor that stores `(resultCall, profSkip, profSkipKnown)` together and loads them together. Do not chase ad hoc assignments.

For the main `initCompletedResult` cases, the desired provenance is:

- canonical existing result at `dagql/cache.go:4130-4143`: keep the adopted shared result's existing flag;
- copied existing value frame at `dagql/cache.go:4148-4152`: copy the source shared result's flag with the frame;
- request-frame fallback at `dagql/cache.go:4159-4161`: use `req.SkipProfile`.

That is sound, but it must be mechanically enforced across every store/clear path above.

### N3 lazy closure: correctly incorporated, but measure the named loss

V2 correctly fixes the round-1 proof. Singleflight waiters share the target recipe through `ongoingCalls` keyed by call/concurrency key at `dagql/cache.go:3674-3677`, but lazy forcers do not share the producer recipe. Lazy closure is therefore target-flag based, and that is load-bearing. The relevant sites are the lazy joiner wait at `dagql/cache.go:2964-2973`, lazy leader op/span at `dagql/cache.go:2998-3019`, and leader wait at `dagql/cache.go:3094-3103`.

The one thing I would add to validation is a count and duration for "non-skipped waiter/forcer suppressed because target `shared.profSkip` is true." The design's named loss is self-consistent, not a loader heuristic, but if it is non-trivial it can hide user-visible waiting as self-time. If that measurement is material, the cleaner emit-side alternative would be to mint/profile a lazy target when a non-skipped consumer first observes it. That is a larger design, so the current plan is acceptable only if the named loss is shown to be negligible.

### N4 stamp coverage: mostly confirmed

The production object call path builds the request, calls the Around hook, then passes the same request to cache: `dagql/objects.go:655-665` and `dagql/objects.go:678`. The current core registrations are present at `core/schema_build.go:107`, `core/schema/coremod.go:47`, `core/sdk/module.go:77`, and `core/modtree.go:589`; `dagql/server.go:903` is the hook setter.

I agree with the v2 backstop: keep the zero-residual-introspection capture assertion. Direct non-test `GetOrInitCall` bypasses are not obvious from search, but tests and future lower-level callers can still construct `CallRequest` manually. The validation should fail if any recording engine path reaches `getOrInitCall` unstamped.

### Receiver-type predicate and over-cut audit: directionally right, expand the set/audit

The separate, debug-independent predicate is correct. The existing normal telemetry path is still `core.AroundFunc`: `dagql.IsSkipped` returns at `core/telemetry.go:32-34`, `introspectionInfo` returns `WithSkip` at `core/telemetry.go:35-38`, and the existing root/receiver logic lives at `core/telemetry.go:354-452`. The loader also confirms the v1 debug-orphan argument was false: every deduped span gets an op id at `engine/wcprof/wcotel/loader.go:217-254`, and an orphan is counted only when the parent span is absent at `engine/wcprof/wcotel/loader.go:299-310`.

The 11 receiver types are genuine schema metadata surfaces:

- explicit fields in `core/schema/module.go:471-494` and `core/schema/module.go:581-651`;
- struct-tag fields in `core/typedef.go`, for example `Function` at `core/typedef.go:21-33`, `FunctionArg` at `core/typedef.go:505-515`, and the type-def family throughout that file.

I did not find evidence that those fields do container/exec/module work. `Query.moduleSource`/`ModuleSource.asModule` are outside the receiver set and should stay profiled. So the over-cut risk for the 11 named types looks acceptable.

However, the audit should not stop at those 11. The root set includes `sourceMap` and `currentFunctionCall` (`core/telemetry.go:363-387`). Their descendants are metadata too, but they are not caught by the proposed receiver set:

- `dagql.Fields[*core.SourceMap]{}` is installed at `core/schema/module.go:494`, and `SourceMap` has fields at `core/typedef.go:2541-2546`.
- `FunctionCall` has fields at `core/typedef.go:2387-2391`.
- `FunctionCallArgValue` has fields at `core/typedef.go:2500-2502`, and `dagql.Fields[*core.FunctionCallArgValue]{}` is installed at `core/schema/module.go:492`.

If those are selected under an inherited skipped context, v2 can still emit `call_exec`/`publishResult` for introspection-style metadata. That is not a graph-consistency hole, but it is a volume/completeness hole and could make the "zero residual introspection" assertion under-match. The cleanest fix is either to include these metadata receiver types in the profiler skip set or explicitly prove they are `DoNotCache`/negligible and update the residual detector to flag them by name.

### Pushbacks

1. `!IsRecording` short-circuit: I agree it does not solve the production problem because production captures are recording. A guard can still be harmless for telemetry-off/native-off sessions, but it is not the mitigation.

The replacement performance story needs tightening. Memoizing by `(receiverTypeName, field)` only helps after the receiver type has already been resolved. The expensive operation is the `ReceiverCall` lookup, which can hit `resultCallByResultID` under `egraphMu.RLock` (`dagql/result_call_frame.go:575-603`, `dagql/cache.go:1377-1387`). If measurement shows cost, cache by something available before receiver resolution, or stamp/pass the receiver type from the object call site where `r.class.inner.Type().Name()` is already known (`dagql/objects.go:644-648`). A `(receiverTypeName, field)` map alone will not remove the lock traffic.

2. Parentless surviving `publishResult`: I agree this should not be fixed in the skip patch. Current `publishResult` follows the kept `oc.execSpanCtx` path at `dagql/cache.go:4013-4018`; parentless kept survivors are orthogonal to removing the introspection amplifier. Do not claim full future gate cleanliness until the separate explicit-parent fix lands.

3. Profiler-skip superset of UI-suppress: I agree. A directly-called reflection accessor can keep its normal `dag.call` span while profiler internals are skipped. That is faithful emitted data, not loader inference. It also keeps the UI/normal telemetry predicate decoupled from the profiler-volume predicate.

### Open items

Imported lazy results: default-false is self-consistent but a volume leak. Cache hits do not emit `call_exec`, so the main risk is imported lazy evaluation. The code imports frames in `dagql/cache_persistence_import.go:164-176` and registers lazy after payload load at `dagql/cache_persistence_import.go:580-595`.

I would resolve this as follows: accept default-false for v1 only if section 9 proves imported introspection lazy volume is negligible. If it is not negligible, recompute via a dependency-safe mechanism, probably a registered callback invoked off hot cache locks or a persisted profile-skip bit. Do not make `dagql` import `core` just to recompute; that would invert the package boundary that motivated stamping `CallRequest.SkipProfile` in the first place.

Over-cut schema audit: passed for the 11 named reflection types, pending the metadata descendants called out above. The audit must include both explicit `dagql.Fields[*core.X]` registrations and struct-tag `field:"true"` declarations in `core/typedef.go`, not only the explicit `dagql.Fields` blocks.

## Holistic Assessment

Correctness: approved with the N2 synchronization/provenance amendment and metadata-descendant audit. Parent edges are safe by re-homing because skipped calls mint no span; the loader only treats absent parent spans as orphaned. Singleflight waits are safe because the cut is static per recipe. Lazy waits are safe only because target-flag gating is used everywhere.

Zero inference/no heuristics: approved. There is no loader/replay change. `EmitOTelWait` must remain unchanged for non-skipped invalid targets; it currently emits a targetless wait so the gate fails loud at `dagql/otelprof_hooks.go:103-135`, and unresolved non-lock targets are counted at `engine/wcprof/wcotel/loader.go:356-380`.

Goal preservation: approved if validation shows the skipped class is only schema/reflection metadata and the named lazy wait loss is negligible. The slow user work surfaces should stay outside the receiver set.

Simplicity: moderate, not massive. The predicate and gates are simple. The complexity is in provenance plumbing: `sharedResult.profSkip` must travel with `resultCall` across adoption/copy/import paths and must be synchronized correctly. That is the place to be disciplined, preferably through helpers.

Actually solves the volume regression: likely yes for the observed module-load amplifier. The plan skips the `call_exec`/`publishResult` pair at `dagql/cache.go:3732-3734` and `dagql/cache.go:4016-4018`, plus waits at `dagql/cache.go:3958`, for the class that normal telemetry suppressed. Final proof still has to be the capture: gate `0/0`, no residual metadata `call_exec` with `dag.call=0`, and BSP drops near zero.

## Merge Bar I Would Require

Before landing, I would require:

1. A race-safe central accessor/helper for `sharedResult.resultCall` plus `profSkip`, with tests covering canonical adoption, copied frames, request-frame fallback, clear/remove, and import/default behavior.
2. A classifier test that includes the residual classes named in v2 and also `SourceMap.*`, `FunctionCall.*`, and `FunctionCallArgValue.*`, or a documented reason those are not in scope plus a capture proving zero residuals.
3. A module-load capture proving volume collapse, BSP drops gone or near zero, `OrphanedParents == 0`, and `UnresolvedWaitTargets == 0`.
4. A lazy capture with explicit counts for suppressed non-skipped-waiter/skipped-target lazy waits.
5. A performance measurement of `profileSkip`, including receiver lookup cost, before relying on memoization.

With those amendments, my recommendation is to implement v2.
