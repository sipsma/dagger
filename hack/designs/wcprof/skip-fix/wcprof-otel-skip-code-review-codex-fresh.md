# wcprof OTel skip implementation code review - Codex fresh

Reviewed commit: `c18b17fc536efbc3f73fdd78ad4564792302629b` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`.

Compared against `4585bf413d`. I reviewed the diff and resulting files. Focused tests run:

```bash
go test ./core ./dagql -run 'Test(ProfileSkip|ResultCallProfileSkip|AroundFuncStampsProfileSkip|IntrospectionRootFields|FrameProfileSkip)'
```

Result: pass.

## Verdict

Landable. I did not find a merge-blocking correctness issue in the implemented skip fix.

The important properties hold in code: the profiling cut is debug-independent and separate from normal telemetry, `ProfileSkip` is homed on `ResultCall` and carried by `clone()`/`fork()`/JSON, the skip bit is excluded from recipe identity, the singleflight and lazy waits gate on the target/producer decision rather than the caller's current visibility, native's outer `OpKindCall` is gated, invalid non-skipped targets still emit targetless waits, and the diff does not touch loader/replay (`wcanalyze`/`wcotel` unchanged).

I have two non-blocking reservations: the new unit tests do not directly exercise the lazy skip gates or invalid-target loud-fail path, and recipe-ID rehydration still defaults `ProfileSkip=false` because the bit is intentionally not in the ID/protobuf representation. Those are not regressions in the reviewed path, but they are worth tracking so the implementation is not over-claimed.

## Findings

### No Blocking Findings

I did not find a real code issue that should block merge.

### Medium Test Gap: Lazy and Invalid-Target Semantics Are Mostly Covered by Code/Live Validation, Not Unit Tests

The unit tests cover the classifier (`core/telemetry_skip_test.go:14`), pre-`IsSkipped` stamping (`core/telemetry_skip_test.go:105`), frame copy/digest/JSON behavior (`dagql/result_call_frame_profileskip_test.go:18`, `:31`, `:58`), and two OTel singleflight emit cases (`dagql/cache_profileskip_emit_test.go:41`, `:94`). They do not directly unit-test:

- a skipped lazy producer suppressing the lazy op and both leader/joiner waits;
- a kept lazy producer with an invalid span target still producing a gate-observable targetless wait;
- native outer `OpKindCall` suppression under `wcprof.Enabled`;
- the `Result.LoadNthValue` inherited-frame case.

The code for these paths looks correct, and the implementer's live §9 report says the lazy/introspection residuals and structural gate are clean. I would not block on adding these tests before merge if that live validation artifact is accepted, but these are the load-bearing paths most likely to be broken by a future "simplification." A small follow-up test suite would be useful.

### Low Caveat: Recipe-ID Rehydration Cannot Preserve `ProfileSkip`

`ProfileSkip` is JSON-persisted and therefore survives normal persisted cache import (`dagql/cache_persistence_import.go:159-175`) after this commit. That closes the important warm-run import path going forward.

It is also intentionally excluded from call identity. As a result, frames reconstructed from recipe IDs/protobuf cannot carry the bit unless recomputed: `loadedResultCallFromRecipeID` constructs a fresh `ResultCall` at `dagql/server.go:1529-1537`, and `resultCallFromRecipeIDInput` does the same at `dagql/call_request_input.go:118-126`. This is consistent with excluding the bit from digests, and it is self-consistent because default false means "emit the op" rather than "dangle an edge." It can still re-profile reflection work in unusual ID-rehydration corners. I consider that a bounded volume caveat, not a correctness blocker, but the review should not claim every possible imported/rehydrated frame preserves the bit.

## Detailed Checks

### Frame-Homing

`ResultCall.ProfileSkip` is added at `dagql/result_call_frame.go:188-202` with `json:"profileSkip,omitempty"`. It is copied in both frame copy primitives:

- `clone()` copies it at `dagql/result_call_frame.go:223-239`;
- `fork()` copies it at `dagql/result_call_frame.go:255-276`.

Those are the important copy paths. The places I checked that set or rebuild stored frames go through those primitives or preserve the same pointer:

- `newDetachedResult` clones the explicit frame (`dagql/cache.go:1800-1810`);
- `attachDependencyResult` clones before normalizing and storing (`dagql/cache.go:1986-1997`);
- `Result.LoadNthValue` uses `parentCall.fork()` (`dagql/cache.go:2370-2375`), which answers my round-2 concern: the Nth synthetic frame inherits the producer skip bit rather than recomputing from the list receiver;
- content-digest wrapping forks the frame (`dagql/cache.go:2440-2457`);
- session-resource wrapping keeps/forks the frame (`dagql/cache.go:2538-2589`);
- DoNotCache detached results clone the request frame (`dagql/cache.go:3676-3679`);
- result materialization clones existing frames or request frames (`dagql/cache.go:4210-4222`);
- egraph indexing fills missing frames via `requestFrame.clone()` (`dagql/cache_egraph.go:1483-1486`);
- persistence export clones stored frames (`dagql/cache_persistence_worker.go:125-129`) and import unmarshals the JSON frame (`dagql/cache_persistence_import.go:159-175`).

The bit is not included in the identity functions I checked: `callPB` does not serialize it (`dagql/result_call_frame.go:425-497`), `recipeDigestWithVisiting` hashes receiver/type/field/args/module/nth/view/effects but not `ProfileSkip` (`dagql/result_call_frame.go:632-720` and following), `contentPreferredDigestWithVisiting` likewise excludes it (`dagql/result_call_frame.go:729-820` and following), and `selfDigestAndInputRefs` excludes it (`dagql/result_call_frame.go:838-930`). The tests pin recipe/content/self digest exclusion (`dagql/result_call_frame_profileskip_test.go:31-55`).

### Stamping and Gating Completeness

The normal object-call path stamps `ReceiverTypeName` cheaply from the receiver object at `dagql/objects.go:583-602`, then `AroundFunc` stamps `req.ResultCall.ProfileSkip` before the inherited skip early return (`core/telemetry.go:29-42`). That is the right order.

The known direct `GetOrInitCall` production path is `Result.LoadNthValue`; it is covered by frame-homing because the request frame is forked from the parent (`dagql/cache.go:2370-2375`) and `fork()` carries `ProfileSkip` (`dagql/result_call_frame.go:255-276`).

The native/OTel gates are complete for the reviewed emit sites:

- outer native `OpKindCall`: `dagql/cache.go:3605-3612`;
- native `call_exec`: `dagql/cache.go:3767-3775`;
- OTel `call_exec`: `dagql/cache.go:3782-3785`;
- in-flight target snapshot: `dagql/cache.go:3795-3808`;
- singleflight native/OTel waits gated on `oc.profSkip`: `dagql/cache.go:3990-4018`;
- native/OTel publishResult follows the emitted target: native only if `oc.profOpID != 0`, OTel only if `oc.execSpanCtx.IsValid()` (`dagql/cache.go:4056-4069`);
- lazy producer gate: `dagql/cache.go:3036-3058`;
- lazy joiner wait gate on producer frame: `dagql/cache.go:2972-3008`;
- lazy leader wait gate: `dagql/cache.go:3134-3149`.

I specifically checked the distinct-from-invalid invariant. For non-skipped singleflight work, `EmitOTelWait` still runs even if `oc.execSpanCtx` is invalid (`dagql/cache.go:4013-4018`), so a genuine mixed-recording invalid target remains a targetless wait and the structural gate fails loud. For lazy, non-skipped joiners also still call `EmitOTelWait` with the stored `lazyEvalSpanCtx` (`dagql/cache.go:3006-3008`); if the leader did not mint a span, that remains gate-observable.

### Predicate and Over-Cut Audit

The predicate is separate from `introspectionInfo` and debug-independent (`core/telemetry.go:449-461`). The root set is shared with `introspectionInfo` (`core/telemetry.go:367-391`, `:471-475`), which avoids root drift.

The reflection type set is complete for the schema names I checked. The `EnumValueTypeDef` addition is necessary: `EnumMemberTypeDef.Type()` reports the legacy schema name `"EnumValueTypeDef"` (`core/typedef.go:2167-2173`). The other listed reflection Go types report their expected schema names: `Function` (`core/typedef.go:72-76`), `FunctionArg` (`:635-641`), `TypeDef` (`:823-827`), `ObjectTypeDef` (`:1189-1193`), `FieldTypeDef` (`:1473-1477`), `InterfaceTypeDef` (`:1594-1598`), `ScalarTypeDef` (`:1764-1768`), `ListTypeDef` (`:1811-1815`), `InputTypeDef` (`:1880-1884`), and `EnumTypeDef` (`:1988-1992`).

I did not find another schema-name mismatch in the reflection set. I also agree with the name trap: `FunctionCall` is not in the set, and should not be, because its `returnValue`/`returnError` path is real active-call work. The classifier test pins this (`core/telemetry_skip_test.go:61-67`).

The audited reflection fields in `core/schema/module.go:418-651` are schema metadata accessors/builders. The slow module-load path remains on non-reflection receivers (`Query.moduleSource`, `ModuleSource.asModule`), which `profileSkip` leaves profiled (`core/telemetry_skip_test.go:54-58`).

### Loader/Replay Principle

The diff touches only:

- `core/telemetry.go`
- `core/telemetry_skip_test.go`
- `dagql/cache.go`
- `dagql/cache_profileskip_emit_test.go`
- `dagql/call_request.go`
- `dagql/objects.go`
- `dagql/result_call_frame.go`
- `dagql/result_call_frame_profileskip_test.go`

There is no `engine/wcprof/wcanalyze` or `engine/wcprof/wcotel` change. The analysis remains a rational function of the emitted data; the engine emits less data, and it gates edges at the source rather than teaching the loader/replay to infer or ignore.

### §9 Caveats

The reported §9 evidence is strong enough for this merge gate if the raw artifacts are available to the lead: structural gate clean, zero residual reflection/introspection `call_exec`/lazy, native dump loads with zero reflection-class ops, and `dropped_events=0`.

The caveats are acceptable for this commit:

- no side-by-side `main` capture: acceptable because the regression mechanism and post-fix residual count are directly tied to `call_exec`/`publishResult`, not ordinary `dag.call`;
- no direct BSP `DroppedSpans` counter: acceptable as a merge caveat given structural gate clean and lower volume, but the BSP backpressure/drop-counter work should remain separate;
- adopted/imported not separately live-captured: mostly covered by frame-homing and JSON tests, with the recipe-ID/old-persistence caveat above.

## Final Recommendation

Landable with no required code changes from my review. I recommend follow-up tests for lazy skip behavior, invalid-target loud failure under `ProfileSkip=false`, native outer-call suppression, and the `LoadNthValue` inherited skip case, but I do not consider those blockers given the code and reported live validation.
