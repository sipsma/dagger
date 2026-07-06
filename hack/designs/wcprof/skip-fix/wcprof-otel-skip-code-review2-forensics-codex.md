# wcprof OTel skip fix code review round 2 - Codex

Reviewed amended commit `4921d53662` in:

`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`

Compared `4585bf413d..4921d53662`, `c18b17fc53..4921d53662`, and the resulting files. Review only; I did not modify the implementation branch.

## Verdict

SIGN OFF on convergence. I found no remaining blocker.

The two required round-1 changes are implemented:

- Native wcprof is ungated again and matches baseline behavior.
- The OTel skip path remains gated and now has focused lazy cross-recipe and invalid-target tests.

One non-blocking cleanup remains: `dagql/cache.go:1800-1803` still has stale wording saying `profSkip` makes a wait emitted iff the target's `call_exec/native op` was emitted. The code is correct and native no longer reads this bit, but that comment should say OTel wait/call_exec only.

## A. Native Un-Gate

Complete. I found no remaining native `ProfileSkip` gate.

The resulting native paths match baseline `4585bf413d`:

- Outer native `OpKindCall` is gated only by `wcprof.Enabled`, nil request, and nil frame (`dagql/cache.go:3606`, `dagql/cache.go:3611`), matching baseline `cache.go:3559`.
- Native `call_exec` is gated only by `wcprof.Enabled(ctx)` (`dagql/cache.go:3768`, `dagql/cache.go:3772`), matching baseline `cache.go:3717`.
- Native singleflight/executor wait is gated only by `wcprof.Enabled(ctx)` (`dagql/cache.go:3996`, `dagql/cache.go:3999`), matching baseline `cache.go:3940`.
- Native `publishResult` follows `oc.profOpID != 0` (`dagql/cache.go:4067`, `dagql/cache.go:4075`), matching baseline `cache.go:4004`.
- Native lazy joiner wait is unconditional through `wcprof.BeginWait(...)` (`dagql/cache.go:2987`, `dagql/cache.go:2988`), matching baseline `cache.go:2964`.
- Native lazy op is gated only by `wcprof.Enabled(evalCtx)` (`dagql/cache.go:3041`, `dagql/cache.go:3048`), matching baseline `cache.go:2998`.
- Native lazy leader wait is unconditional through `wcprof.BeginWait(...)` (`dagql/cache.go:3138`, `dagql/cache.go:3141`), matching baseline `cache.go:3094`.

The `profSkip` fields remain only as target flags for OTel gates: `ongoingCall.profSkip` is stored at `dagql/cache.go:3810`, and lazy reads the producer frame at `dagql/cache.go:2987` and `dagql/cache.go:3040`.

## B. OTel Side

The OTel skip behavior is unchanged and still self-consistent:

- `AroundFunc` stamps `ResultCall.ProfileSkip` before inherited `IsSkipped` returns (`core/telemetry.go:32`, `core/telemetry.go:41`, `core/telemetry.go:42`).
- OTel `call_exec` is gated by `!req.ResultCall.ProfileSkip` (`dagql/cache.go:3783`, `dagql/cache.go:3785`).
- OTel `publishResult` follows the call_exec span context (`dagql/cache.go:4079`, `dagql/cache.go:4081`), so skipped call_exec means no skipped publishResult.
- OTel singleflight/executor waits gate on the target flag `!oc.profSkip` (`dagql/cache.go:4016`, `dagql/cache.go:4021`).
- OTel lazy span gates on the producer frame flag (`dagql/cache.go:3040`, `dagql/cache.go:3061`).
- OTel lazy joiner waits gate on the producer frame flag (`dagql/cache.go:2987`, `dagql/cache.go:3008`).
- OTel lazy leader wait follows `lazySpan != nil` (`dagql/cache.go:3145`, `dagql/cache.go:3150`).

Dangle-proofing still holds. Singleflight waiters share the target recipe, and lazy waiters use the producer frame flag. A skipped OTel target does not mint the span and also does not emit a wait into the missing target. A non-skipped invalid target still emits and fails the structural gate.

No loader/replay/wcotel/wcanalyze files changed; the diff still touches only `core`, `dagql`, and tests.

## C. Tests

The new tests are adequate.

`TestProfileSkipGatesLazyEmit` drives the real `Cache.Evaluate -> evaluateOne` lazy path with `ProfileSkip=true/false`, asserts the producer frame carries the bit, asserts skipped producer emits zero lazy OTel spans/waits, asserts kept producer emits lazy OTel spans/waits, and runs the structural gate (`dagql/cache_profileskip_emit_test.go:192`, `dagql/cache_profileskip_emit_test.go:225`, `dagql/cache_profileskip_emit_test.go:232`, `dagql/cache_profileskip_emit_test.go:246`).

`TestProfileSkipLazyCrossRecipeForcerStaysClean` covers the load-bearing lazy cross-recipe case: a traced joiner forces a skipped producer's pending lazy value, confirms zero lazy OTel spans/waits, and confirms `UnresolvedWaitTargets == 0` plus gate success (`dagql/cache_profileskip_emit_test.go:258`, `dagql/cache_profileskip_emit_test.go:293`, `dagql/cache_profileskip_emit_test.go:330`, `dagql/cache_profileskip_emit_test.go:339`).

`TestProfileSkipDoesNotBlindInvalidTargetDetector` covers the invalid-target detector: a non-skipped producer is evaluated by an untraced leader, a traced joiner waits on the invalid target, and the test requires `UnresolvedWaitTargets > 0` plus gate error (`dagql/cache_profileskip_emit_test.go:349`, `dagql/cache_profileskip_emit_test.go:381`, `dagql/cache_profileskip_emit_test.go:398`, `dagql/cache_profileskip_emit_test.go:425`).

I ran:

```sh
go test ./core ./dagql -run 'Test(ProfileSkip|ResultCallProfileSkip|FrameProfileSkip|AroundFuncStamps|IntrospectionRootFields)' -count=1
go test -race ./core ./dagql -run 'Test(ProfileSkip|ResultCallProfileSkip|FrameProfileSkip|AroundFuncStamps|IntrospectionRootFields)' -count=1
go test ./core ./dagql -count=1
```

All passed.

## D. Comments And Docs

The major stale source-parity wording is corrected. `core/telemetry.go` now states the predicate is OTel-only and that native keeps full detail (`core/telemetry.go:426`, `core/telemetry.go:429`). It also states directly-called reflection accessors keep a coarse OTel `"call"` op if normal `dag.call` survives, while native keeps full detail and the oracle compares non-reflection classes only (`core/telemetry.go:448`, `core/telemetry.go:457`). `ResultCall.ProfileSkip` likewise says native ignores the bit and only OTel gates read it (`dagql/result_call_frame.go:188`, `dagql/result_call_frame.go:202`).

Remaining non-blocking stale comment: `dagql/cache.go:1800-1803` still says `profSkip` gates every singleflight wait and mentions `call_exec/native op`. The actual use at `dagql/cache.go:4016` is OTel-only. This should be cleaned before/after landing, but it is not a runtime or analysis blocker.

## E. Remaining Blockers

None.

The amended implementation preserves the governing principle: no loader/replay inference was added, and the OTel emit now produces a smaller self-consistent graph while native remains a full-detail dev/oracle source.
