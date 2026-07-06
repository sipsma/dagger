# Convergence re-review — skip fix `4921d53662` (amended) — chunk4 implementer

Reviewer: chunk4 implementer. Verified the amend delta (`c18b17fc53..4921d53662`)
and the resulting code against the round-1 requirements. Ran the tests.

## VERDICT: SIGN OFF — converged. No remaining blocker.

Both round-1 must-changes are in and correct, and Erik's native-un-gate ruling is
implemented cleanly and completely. I compiled the touched packages and ran the
profileskip tests under `-race` — all green. One non-blocking downstream note
(oracle) and one cosmetic nit, below.

---

## (a) Native un-gate — correct AND complete ✓

Every native gate reverted to baseline `4585bf413d`; **no native gate reads the
skip flag**. Verified each in the diff:
- outer `OpKindCall` `cache.go:3611` — `|| req.ResultCall.ProfileSkip` removed →
  `if !wcprof.Enabled(ctx) || req==nil || req.ResultCall==nil`.
- native `execOp` `:3769` → back to `if wcprof.Enabled(ctx)`.
- native singleflight wait `:3996` → back to `if wcprof.Enabled(ctx)`.
- lazy native op `:3041` → back to `if wcprof.Enabled(evalCtx)`.
- lazy native joiner wait `:2995` and leader wait `:3138` → back to unconditional
  `profWait := wcprof.BeginWait(...)`.
- native `pubOp` follows `oc.profOpID != 0` (unchanged) → minted for reflection
  again, since `execOp` is now minted.

**The native/OTel split is automatically clean** — they key on independent state:
native via `wcprof.Enabled` + `profOpID`/`execOp`; OTel via `OTelProfActive` +
`ProfileSkip` + `execSpanCtx`. For a skipped reflection call: native mints
`execOp` (so `profOpID≠0` → native `pubOp` mints), OTel skips `call_exec` (so
`execSpanCtx` invalid → OTel `publishResult` skips). No shared gate, no
cross-contamination. `oc.profSkip`/`producerSkip` are retained but now read by
**OTel gates only** — confirmed by reading every use.

Frame-homing and digest exclusion are **untouched** — the amend's
`result_call_frame.go` change is comment-only (the `ProfileSkip` field + `clone`/
`fork` copies + json tag are unchanged), and `telemetry.go`'s change is the
predicate doc + the `AroundFunc` comment only (the `profileSkip` body and the
stamp line are unchanged context).

§9 corroborates: native dump now carries the reflection class (53,433 ops,
`dropped_events=0` — native is in-memory, never on the BSP path, so it cannot
suffer the capture loss the OTel skip exists to prevent).

## (b) OTel side — unchanged and still correct ✓

The OTel gates are exactly as in `c18b17fc53` (which I reviewed clean): `call_exec`
`:3784`, OTel singleflight wait `:4016`, lazy OTel span `:3059`, lazy OTel joiner
wait `if !producerSkip`. The lazy OTel **leader** wait is correctly gated on
`lazySpan != nil` (`:3145`), which the OTel lazy-span gate already keyed on
`producerSkip` — so a skipped producer emits no OTel leader wait either. Un-gating
native did not perturb any OTel condition. §9: OTel 1646 `call_exec`, 0 residual
reflection, structural gate `0/0`. The volume regression (the OTel amplifier) is
removed; the OTel graph is self-consistent.

## (c) The two new tests — adequate (and a bonus) ✓

All three are **real-path** (drive `GetOrInitCall`→`Evaluate`→`evaluateOne`),
concurrent, and assert through the actual `wcotel.Compile`/`wcanalyze.Build`/
`CheckStructural`. I ran them under `-race`: pass.
- **`TestProfileSkipGatesLazyEmit`** — the round-1 lazy emit-path ask. Skipped
  producer → 0 lazy OTel spans + 0 lazy waits; kept → both present and resolve;
  asserts the producer frame carries the bit (frame-homing); gate clean both ways.
- **`TestProfileSkipLazyCrossRecipeForcerStaysClean`** — the load-bearing **N3**
  case (stronger than I asked): a recording joiner forces a *skipped* producer;
  because the OTel lazy wait gates on the **producer's** stored flag, the joiner
  emits no wait → `UnresolvedWaitTargets==0`. Would fail if keyed on the waiter's
  bit — exactly the cross-recipe dangle N3 prevents.
- **`TestProfileSkipDoesNotBlindInvalidTargetDetector`** — the §8.5 distinct-from-
  invalid ask. An *untraced* leader yields a genuinely invalid target; a *non-
  skipped* traced joiner **still** emits a targetless wait → `UnresolvedWaitTargets>0`
  and `gate.Err()!=nil`. This locks out the future `execSpanCtx.IsValid()`-blinding
  refactor my round-1 review warned about — the precise regression guard.

The pre-existing OTel singleflight tests still validate that path (they observe the
OTel side only — `wcprof` is not enabled in unit tests — so the native un-gate
doesn't affect them).

## (d) Doc/comment corrections — right ✓

`AroundFunc` comment, `profileSkip` doc, and the `ProfileSkip` field comment are
all corrected to "only the OTel emit gates read it; native keeps full detail."
Refinement-3 is stated precisely: a directly-called reflection accessor keeps its
`dag.call` span — a **coarse OTel "call" op**, *not* source-parity with native's
`call_exec` — and the cross-source oracle compares only non-reflection classes;
noted as empirically nil since such accessors are ~always under a `hideCtx`.
Accurate.

---

## Non-blocking note (downstream, for whoever re-runs the cross-source oracle)

This is a consequence of Erik's ruling, not a code defect, and does not block the
merge. With native full and OTel reflection-dropped, the oracle's prior
"user-work self-time matches native **exactly**" no longer holds *per-op* for ops
with reflection descendants: OTel folds the (cheap) reflection self-time up into
the nearest kept ancestor (e.g. the `ModuleSource.asModule`/module-load op), while
native keeps it as separate child ops. For "what's slow" both still surface the
same ancestor, so the **goal is preserved** and the OTel side is independently
self-consistent (`0/0`). But the oracle comparison should now be done at subtree
totals, or exclude reflection-adjacent ops — don't expect exact per-op self-time
equality where reflection children exist. The §9 run validated each source in
isolation; it did not run the cross-source oracle, so this re-frame is owed before
re-asserting cross-source parity. (Native ground truth itself is unchanged from the
validated PR #13393 baseline, so its half is solid.)

## Cosmetic nit (not blocking)

`result_call_frame.go:188` — the inserted parenthetical produced a long run-on
comment line ("…keeps full detail — it ignores this bit.) It is set once, before
the frame is"). Purely prose; `gofmt`-clean. Tidy if convenient.

---

## Bottom line

Converged. (a) native un-gate is a complete, clean revert to the validated
baseline with no flag leakage into native; (b) the OTel side is unchanged and
self-consistent (`0/0`, 0 residual); (c) both round-1 test gaps are filled with
strong real-path `-race` tests plus the N3 cross-recipe guard; (d) the comments
are accurate. Zero loader/replay change still holds. **Sign off — landable.** The
oracle re-frame is a downstream follow-up, not a merge condition.
