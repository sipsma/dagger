# wcprof OTel Skip Fix Code Review 2 - Codex Fresh

Reviewed commit: `4921d53662` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`
against baseline `4585bf413d`.

Line numbers below refer to the checked-out `4921d53662` tree.

## Verdict

SIGN OFF on convergence. I found no remaining blocker.

The amended commit resolves the round-1 blocker: native wcprof emission is no longer gated by `ProfileSkip`, while the OTel second-source emit still skips the reflection/introspection class. The new lazy cross-recipe and invalid-target tests cover the two important edge cases added in this round. The loader/replay remain untouched, so the analysis side stays a rational function of the emitted data.

## Checks

### Native un-gate is complete

I re-audited every native-side site that was previously at risk:

- Outer native `OpKindCall` is back to the baseline condition: it only checks `wcprof.Enabled(ctx)`, `req != nil`, and `req.ResultCall != nil`; it does not read `ProfileSkip` (`dagql/cache.go:3606`).
- Native `execOp` is emitted under `wcprof.Enabled(ctx)` only; `ProfileSkip` does not gate it (`dagql/cache.go:3768`).
- Native singleflight waits still emit when native wcprof is enabled, with no `ProfileSkip` check (`dagql/cache.go:3996`).
- Native `dagql.publishResult` still follows `oc.profOpID != 0`, not the skip bit (`dagql/cache.go:4067`).
- Lazy native op and lazy waits are full-detail again: the lazy op is gated only on `wcprof.Enabled(evalCtx)` (`dagql/cache.go:3042`), the joiner wait is unconditional on the producer skip bit (`dagql/cache.go:2988`), and the leader wait is also unconditional on the producer skip bit (`dagql/cache.go:3141`).

The remaining `profSkip` / `producerSkip` state is only used to gate OTel emit. I did not find a remaining native gate.

### OTel skip still works and remains dangle-proof

The OTel gates are still placed on the target/producer side, which is the required self-consistent cut:

- `call_exec` is skipped at mint time with `OTelProfActive(callCtx) && !req.ResultCall.ProfileSkip` (`dagql/cache.go:3784`).
- `oc.profSkip` snapshots the target call's skip bit when the in-flight call is published (`dagql/cache.go:3810`).
- Singleflight OTel waits gate on `!oc.profSkip`, so a skipped target does not create a dangling wait from a joiner (`dagql/cache.go:4016`).
- `publishResult` follows the presence of a valid `execSpanCtx`, so a skipped `call_exec` also suppresses its paired `dagql.publishResult` span (`dagql/cache.go:4079`).
- Lazy OTel emit gates on the producer frame, not the forcing caller: joiner wait at `dagql/cache.go:3008`, lazy span at `dagql/cache.go:3061`, and leader wait by `lazySpan != nil` at `dagql/cache.go:3145`.

That keeps the OTel graph self-consistent without adding loader/replay inference. `git diff --name-only 4585bf413d..4921d53662 -- engine/wcprof cmd/wcprof-otel-analyze` is empty.

### New tests are adequate

The added tests cover the round-2 gaps:

- `TestProfileSkipGatesLazyEmit` exercises the real lazy path and verifies skipped producers emit no OTel lazy span/wait while kept producers do, with the structural gate passing (`dagql/cache_profileskip_emit_test.go:192`).
- `TestProfileSkipLazyCrossRecipeForcerStaysClean` is the N3 case: a traced, different-recipe forcer joins a skipped producer and still emits no OTel lazy wait into the absent producer span; it asserts `UnresolvedWaitTargets == 0` and gate success (`dagql/cache_profileskip_emit_test.go:258`).
- `TestProfileSkipDoesNotBlindInvalidTargetDetector` covers the distinct-from-invalid rule: a non-skipped target with no valid OTel target span still emits a targetless wait, producing `UnresolvedWaitTargets > 0` and a gate error (`dagql/cache_profileskip_emit_test.go:349`).

I ran:

```sh
go test -race ./dagql -run 'TestProfileSkip(GatesSingleflightEmit|StaticCutSingleflightJoiner|GatesLazyEmit|LazyCrossRecipeForcerStaysClean|DoesNotBlindInvalidTargetDetector)'
go test ./core ./dagql -run 'Test(ProfileSkip|ResultCallProfileSkip|AroundFuncStampsProfileSkip|IntrospectionRootFields|ProfileSkipDoes)'
```

Both passed.

### Comments and docs in the diff are corrected

The comments now state the intended OTel-only behavior:

- `core/telemetry.go:426` explicitly says native keeps full detail and is not gated by the predicate.
- `dagql/result_call_frame.go:188` describes `ProfileSkip` as an OTel second-source skip bit and says native ignores it.
- `dagql/cache.go:3606`, `dagql/cache.go:3771`, and `dagql/cache.go:3998` document native full-detail behavior.
- The remaining cross-source caveat is precise: directly-called reflection accessors may keep a coarse OTel `call` op while the profiler skips `call_exec`, and source parity is not claimed for reflection classes (`core/telemetry.go:449`).

I searched the modified code for stale "both sources drop the same class" / source-symmetry language and did not find any. The only minor non-blocker is a long wrapped comment line at `dagql/result_call_frame.go:191`.

## Remaining Caveats

I did not re-run the full live dev-engine capture from §9 in this review. For merge-gate purposes, the focused code audit plus the targeted tests are enough: the native un-gate is mechanically visible, the OTel gates remain in the intended places, and the new tests cover the cross-recipe lazy and invalid-target failure modes that could otherwise reintroduce silent data loss.

No blocker remains from this convergence pass.
