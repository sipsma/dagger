# wcprof OTel skip fix code review - Codex

Reviewed commit `c18b17fc53` in:

`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`

Compared against `4585bf413d..c18b17fc53` and the v2 implementation plan in the skip-implementer worktree. Review only; I did not modify the implementation branch.

## Findings

### Low: no focused unit test covers the load-bearing lazy ProfileSkip branch

The code implements the lazy rule correctly: joiner waits are gated on the producer frame via `shared.profileSkip()` (`dagql/cache.go:2993`, `dagql/cache.go:3006`), the lazy op itself is gated on the producer frame (`dagql/cache.go:3036`, `dagql/cache.go:3057`), and the leader wait follows the same producer flag (`dagql/cache.go:3138`, `dagql/cache.go:3144`). That is the right cut: the forcer recipe can differ from the producer recipe, so using the waiter's own bit would reintroduce dangling lazy waits.

The test file added by this commit exercises skipped singleflight emission and a skipped singleflight joiner (`dagql/cache_profileskip_emit_test.go:41`, `dagql/cache_profileskip_emit_test.go:94`), but it does not directly drive `ProfileSkip=true` through `Cache.Evaluate` or verify "skipped producer -> no lazy op/waits" and "non-skipped forcer on skipped producer -> wait dropped, gate clean." Existing lazy tests cover stale invalid targets and parent override composition (`dagql/otelprof_lazy_retry_test.go:41`, `dagql/otelprof_lazy_retry_test.go:177`), not the new skip branch.

This is not a code blocker if the reported live section 9 capture is accepted, because the implementation itself gates both mint and wait from the producer frame. I would still add a focused regression test for this branch soon; it is one of the load-bearing invariants.

## Verdict

Landable. I found no blocking correctness issue in the implementation. The only issue I would track is the lazy ProfileSkip unit-test gap above.

## Audit Notes

### Frame-homing correctness

`ProfileSkip` is frame-homed on `ResultCall` with JSON persistence (`dagql/result_call_frame.go:188`, `dagql/result_call_frame.go:202`). It is copied by both frame copy helpers: `clone()` copies it (`dagql/result_call_frame.go:223`, `dagql/result_call_frame.go:238`) and `fork()` copies it (`dagql/result_call_frame.go:255`, `dagql/result_call_frame.go:276`).

I checked the important frame construction/copy/import paths. They either store a frame already carrying the bit or use `clone()`/`fork()`:

- Detached results: `dagql/cache.go:1821`, `dagql/cache.go:1824`
- Attach normalization: `dagql/cache.go:1990`, `dagql/cache.go:1996`
- Nth/list derivation: `dagql/cache.go:2370`, `dagql/cache.go:2377`, plus `dagql/builtins.go:118`, `dagql/builtins.go:390`, `dagql/types.go:1374`, `dagql/cache_persistence_self.go:292`
- Result variants: `dagql/cache.go:2452`, `dagql/cache.go:2456`, `dagql/cache.go:2552`, `dagql/cache.go:2589`
- Do-not-cache detached result: `dagql/cache.go:3676`, `dagql/cache.go:3678`
- Completed-result adoption/copy/request-frame paths: canonical adoption keeps the adopted shared result (`dagql/cache.go:4191`, `dagql/cache.go:4193`); existing frames are cloned (`dagql/cache.go:4211`, `dagql/cache.go:4212`); request frames are cloned (`dagql/cache.go:4220`, `dagql/cache.go:4221`)
- E-graph frame updates: content-digest fork/store (`dagql/cache_egraph.go:1005`, `dagql/cache_egraph.go:1010`, `dagql/cache_egraph.go:1064`), request-frame clone on indexing (`dagql/cache_egraph.go:1484`, `dagql/cache_egraph.go:1485`)
- Persistence import/export: imported JSON is unmarshaled into `ResultCall` then stored (`dagql/cache_persistence_import.go:159`, `dagql/cache_persistence_import.go:175`); snapshots clone the frame (`dagql/cache_persistence_worker.go:125`, `dagql/cache_persistence_worker.go:128`) and write that frame (`dagql/cache_persistence_worker.go:437`)

The bit is excluded from cache identity as intended. The recipe digest code does not read it (`dagql/result_call_frame.go:632`, `dagql/result_call_frame.go:724`), the self digest path does not read it (`dagql/result_call_frame.go:885`, `dagql/result_call_frame.go:930`), and `CallPB` is built manually without it (`dagql/result_call_frame.go:417`, `dagql/result_call_frame.go:497`). The new tests pin digest exclusion and JSON round-trip (`dagql/result_call_frame_profileskip_test.go:31`, `dagql/result_call_frame_profileskip_test.go:58`).

### Gating completeness

The production object call path stamps `ReceiverTypeName` from the immediate receiver at request construction (`dagql/objects.go:583`, `dagql/objects.go:602`), then runs telemetry before cache entry (`dagql/objects.go:659`, `dagql/objects.go:682`). `AroundFunc` stamps `req.ResultCall.ProfileSkip` before the inherited `IsSkipped` early return (`core/telemetry.go:29`, `core/telemetry.go:40`, `core/telemetry.go:41`).

Production paths into `GetOrInitCall` are covered: `objects.go` uses the stamped request (`dagql/objects.go:682`), and `Result.LoadNthValue` derives from the producer frame with `fork()` (`dagql/cache.go:2370`, `dagql/cache.go:2390`). I found no other non-test production call path into `GetOrInitCall`.

Singleflight/native/OTel gates are complete:

- Outer native `OpKindCall` gated by the frame bit (`dagql/cache.go:3610`)
- Native `call_exec` gated (`dagql/cache.go:3768`)
- OTel `call_exec` gated (`dagql/cache.go:3783`)
- In-flight target snapshot stored on `ongoingCall` (`dagql/cache.go:3800`, `dagql/cache.go:3808`)
- Native wait gated on target flag (`dagql/cache.go:3995`)
- OTel wait gated on target flag (`dagql/cache.go:4013`)
- `publishResult` follows the target op/span: native via `oc.profOpID != 0` (`dagql/cache.go:4064`, `dagql/cache.go:4072`), OTel via `oc.execSpanCtx.IsValid()` (`dagql/cache.go:4077`, `dagql/cache.go:4079`)

Lazy gates use the producer frame rather than the forcer/waiter recipe, which is the critical rule:

- Lazy joiner reads producer skip from the stored frame (`dagql/cache.go:2993`)
- Lazy joiner native and OTel waits are gated (`dagql/cache.go:2995`, `dagql/cache.go:3006`)
- Lazy leader op/span are gated on `frameProfileSkip(resultCall)` (`dagql/cache.go:3036`, `dagql/cache.go:3038`, `dagql/cache.go:3057`)
- Lazy leader wait follows (`dagql/cache.go:3138`, `dagql/cache.go:3144`)

### Reflection set and over-cut audit

The predicate is separate, static, and debug-independent (`core/telemetry.go:449`). It skips Query/root introspection fields via the shared root set (`core/telemetry.go:367`, `core/telemetry.go:455`) and skips all receiver types in `reflectionTypeNames` (`core/telemetry.go:410`, `core/telemetry.go:422`).

The enum-name deviation is real and correctly handled. `EnumMemberTypeDef` is the Go type, but its schema type name is the legacy `EnumValueTypeDef` (`core/typedef.go:2167`, `core/typedef.go:2171`), and the predicate includes both names (`core/telemetry.go:421`, `core/telemetry.go:422`). I audited the other reflection type `Type()` names and found no other mismatch: `Function`, `FunctionArg`, `TypeDef`, `ObjectTypeDef`, `FieldTypeDef`, `InterfaceTypeDef`, `ScalarTypeDef`, `ListTypeDef`, `InputTypeDef`, and `EnumTypeDef` all report the matching schema names (`core/typedef.go:72`, `core/typedef.go:638`, `core/typedef.go:825`, `core/typedef.go:1191`, `core/typedef.go:1475`, `core/typedef.go:1596`, `core/typedef.go:1766`, `core/typedef.go:1813`, `core/typedef.go:1882`, `core/typedef.go:1990`).

The "name trap" is handled. `FunctionCall` is intentionally excluded (`core/telemetry.go:407`), and that is correct because `returnValue`/`returnError` are stateful real work (`core/schema/module.go:251`, `core/schema/module.go:265`; implementation at `core/typedef.go:2449`, `core/typedef.go:2486`). The classifier test pins this (`core/telemetry_skip_test.go:56`, `core/telemetry_skip_test.go:70`).

I did not find a reflection-type field that forces container/exec/module-load work. Some builders perform metadata subselects and ID loads, for example `typeDefWithObject` selects root `__objectTypeDef` (`core/schema/module.go:1036`, `core/schema/module.go:1055`) and `functionWithArg` loads a `TypeDef` then selects `__functionArg` (`core/schema/module.go:1557`, `core/schema/module.go:1638`). Those are still schema-construction/reflection work, not user container/exec/module execution. The module-load work remains on non-reflection receivers such as `Query.moduleSource` and `ModuleSource.asModule`, which the predicate leaves profiled (`core/telemetry_skip_test.go:56`, `core/telemetry_skip_test.go:60`).

### Distinct-from-invalid preserved

The implementation does not conflate "skipped" with "invalid target." Wait emission is skipped only when the target is intentionally skipped (`dagql/cache.go:4013`; lazy analog at `dagql/cache.go:3006`). For non-skipped work with an invalid span context, `EmitOTelWait` is still called, and that function deliberately emits attributed targetless links for structural-gate failure (`dagql/otelprof_hooks.go:103`, `dagql/otelprof_hooks.go:134`). Existing tests cover the gate-observable invalid target behavior (`dagql/otelprof_hooks_test.go:254`, `dagql/otelprof_hooks_test.go:279`) and lazy retry invalid-target behavior (`dagql/otelprof_lazy_retry_test.go:41`, `dagql/otelprof_lazy_retry_test.go:168`).

### Loader/replay untouched

The commit changes only:

- `core/telemetry.go`
- `core/telemetry_skip_test.go`
- `dagql/cache.go`
- `dagql/cache_profileskip_emit_test.go`
- `dagql/call_request.go`
- `dagql/objects.go`
- `dagql/result_call_frame.go`
- `dagql/result_call_frame_profileskip_test.go`

No `engine/wcprof/wcotel`, `wcanalyze`, loader, gate, or replay code is touched. The fix stays on the emit side, so it preserves the no-inference analysis invariant.

### Test and validation status

I ran:

```sh
go test ./core ./dagql -count=1
go test -race ./core ./dagql -run 'Test(ProfileSkip|ResultCallProfileSkip|FrameProfileSkip|AroundFuncStamps|IntrospectionRootFields|OTelWait|LazyEmitRetry|LazyEmitNested)' -count=1
```

Both passed.

The added tests cover the classifier, debug independence, inherited-skip stamping, digest exclusion, JSON round-trip, direct call_exec/publishResult gating, and skipped singleflight joiner collapse (`core/telemetry_skip_test.go:14`, `core/telemetry_skip_test.go:83`, `core/telemetry_skip_test.go:105`, `dagql/result_call_frame_profileskip_test.go:18`, `dagql/result_call_frame_profileskip_test.go:31`, `dagql/result_call_frame_profileskip_test.go:58`, `dagql/cache_profileskip_emit_test.go:41`, `dagql/cache_profileskip_emit_test.go:94`).

The tests do not directly assert native outer `OpKindCall` disappearance or the new lazy `ProfileSkip=true` branch. The code is straightforward and the implementer's reported section 9 native dump / live capture covers those, but a small future unit test would make the load-bearing lazy invariant harder to regress.

I did not re-run the live dev-engine section 9 capture in this review. Given the reported live results (gate 0/0, zero residual reflection-class call_exec/lazy, native dump with zero reflection class, dropped_events=0, loadable by wcanalyze), the caveats are acceptable for merge:

- No fresh side-by-side `main` baseline is not a blocker; the code structurally removes the known reflection amplifier while preserving real-miss call_exec/publishResult.
- BSP drops are proxied rather than directly instrumented, but post-fix volume is below the default queue and the structural/native checks are clean. Direct BSP `DroppedSpans` instrumentation remains a good backstop, not a merge blocker for this fix.
- Adopted/imported paths were not separately live-captured, but frame-homing plus clone/fork/JSON/import coverage makes the propagation rule mechanical. Old persisted rows still default false if they predate the JSON field, which is a bounded volume edge, not a graph-consistency issue.
- Telemetry-off/native-on can re-profile the class because `AroundFunc` is not stamping in that dev-only mode. That is self-consistent and outside the OTel-source regression.

### Race/performance/simplicity

I found no new race. The bit is stamped before cache entry (`core/telemetry.go:40`, `dagql/objects.go:659`), copied with immutable-ish frames, and read from `ongoingCall` or the stored producer frame (`dagql/cache.go:3808`, `dagql/cache.go:1567`). The focused `-race` run passed.

The performance shape is better than the v2 predicate originally feared: `ReceiverTypeName` is stamped at the object call site (`dagql/objects.go:602`), so `profileSkip` is just map lookups (`core/telemetry.go:449`) and does not take the `egraphMu` receiver-resolution path.

The frame-homed deviation from the plan is cleaner than a separate `CallRequest.SkipProfile` plus `sharedResult.profSkip`: one persisted/copyable policy bit travels with the producer frame, while `ongoingCall.profSkip` remains only the in-flight snapshot needed before `oc.res` exists (`dagql/cache.go:1800`, `dagql/cache.go:1804`).
