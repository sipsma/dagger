# wcprof × OTel — skip-fix CONVERGENCE re-review (code review round 2, Chunk 1 / loader+gate owner)

**Reviewer:** Chunk 1 owner (loader + §6.1 gate). Reviewed the amended commit
`4921d53662` (unpushed) against baseline `4585bf413d`. No code modified, no commits.

## SIGN-OFF ✅

**The gate stays 0/0 and I sign off on convergence.** Both round-1 required changes
landed correctly: native is fully reverted to baseline (OTel-only skip, per Erik's
ruling), the OTel dangle-proof property is intact, and my two round-1 MED test asks are
closed with substantive tests that drive the **real** loader/gate. No remaining blocker.

A nice side effect worth stating: **the OTel-only ruling resolves my round-1 holistic
concern** ("run the gate on a post-skip *native* capture too"). Native is now byte-for-byte
the validated PR #13393 baseline — there is no modified native emit left to re-validate.
And native was never the BSP-volume problem (it writes its own dump, not the 2048-slot
BSP queue — §9 shows 53,433 reflection-class native ops with `dropped_events=0`), so
un-gating it costs nothing on the regression while restoring validated behavior.

## (a) Native un-gate — COMPLETE; every native gate is baseline

Cleanest proof first: `git diff 4585bf413d..4921d53662 -- dagql/cache.go` contains **no
native-gate change at all** — only OTel-side gates + the `oc.profSkip`/`producerSkip`/
helper plumbing. A reverted native gate matches baseline and therefore cannot appear in
a baseline diff; none does. I then confirmed each named site directly in the current file:

| native site | current code | baseline? |
|---|---|---|
| outer `OpKindCall` (cache.go:3611) | `if !wcprof.Enabled(ctx) \|\| req == nil \|\| req.ResultCall == nil` | ✓ no `ProfileSkip` |
| `execOp` (:3769) | `if wcprof.Enabled(ctx)` | ✓ |
| singleflight `BeginWait` (:3998) | `if wcprof.Enabled(ctx)` → `:3999` | ✓ |
| lazy `BeginOp(OpKindLazy)` (:3044) | `if wcprof.Enabled(evalCtx)` → `:3045` | ✓ |
| lazy joiner `BeginWait` (:2988) | `profWait := wcprof.BeginWait(...)` (unconditional `:=`) | ✓ |
| lazy leader `BeginWait` (:3141) | `profWait := wcprof.BeginWait(...)` (unconditional `:=`) | ✓ |
| `pubOp` | unchanged | ✓ |

The lazy joiner/leader `BeginWait`s reverted from round-1's `if !producerSkip { ... }`
guard back to an unconditional `:=`. That is safe precisely because native is un-gated:
`lazyOp` is now always minted (when `wcprof.Enabled`), so `BeginWait(lazyOp.ID())` never
sees a nil op — the round-1 `BeginWait(0)` targetless-wait hazard is gone, not papered
over. The `producerSkip`/comment lines that sit just above `:2988`/`:3141` feed only the
OTel side (verified — they are not the `BeginWait` guards).

## (b) OTel side unchanged + dangle-proof intact

The OTel gates are exactly as I verified them in round 1, and still read the **same
flag** for mint and wait — the property that makes a dangle impossible:

- Singleflight: call_exec gated `OTelProfActive(callCtx) && !req.ResultCall.ProfileSkip`
  (:3784); `oc.profSkip = req.ResultCall.ProfileSkip` snapshot (:3809); OTel wait gated
  `if !oc.profSkip` (:4016). **Mint ⟺ wait, same flag.**
- Lazy: OTel span gated `OTelProfActive(evalCtx) && !producerSkip` (:3059); OTel joiner
  wait gated `if !producerSkip` (:3003-region); leader OTel wait guarded `if lazySpan
  != nil`. **Mint ⟺ wait, same `producerSkip`.**

Crucially, the un-gate did **not** introduce the one dangerous asymmetry I watch for —
an OTel *wait* left ungated while its OTel *mint* stays gated (which would emit a wait
into a never-minted target → `UnresolvedWaitTargets`). The OTel waits stayed gated on the
same flag as the OTel mints. The new native/OTel asymmetry is the *safe* direction
(native mints everything; OTel mint and OTel wait move together).

New-behavior check (refinement-3 coarse "call" op): a *directly-called* reflection
accessor keeps its normal `dag.call` span, which the OTel loader sees as a present
`"call"` op — so its children re-home to a present node and any same-recipe joiner is
itself skipped (no OTel wait). No orphan, no dangle. §9's OTel structural gate `0/0` +
`0` residual call_exec confirms this empirically. The oracle is correctly scoped to
non-reflection classes (native has the reflection detail, OTel intentionally doesn't) —
a documented validation-scope choice, not a correctness gap.

## (c) The two new tests — adequate and NON-vacuous (both pin my round-1 asks)

Both drive the real pipeline (`wcotel.Compile → wcanalyze.Build → wcotel.CheckStructural`)
and would **fail** if the invariant were violated:

- **`TestProfileSkipLazyCrossRecipeForcerStaysClean` (:258) — pins N3 (my round-1 MED).**
  Real setup, not a stub: a `ProfileSkip:true` producer (asserted on the stored frame), a
  leader parked in the lazy callback, and a **genuinely-joined** non-skipped forcer (it
  spins until `shared.lazyEvalWaiters >= 2`). Asserts `0 OpKindLazy`, `0` lazy wait links,
  and **`res.UnresolvedWaitTargets == 0`**. If the lazy gate keyed on the forcer's own bit
  (the forbidden "simplification" the code comment warns against), the non-skipped forcer
  would emit a wait into the never-minted lazy op → `UnresolvedWaitTargets > 0` → test
  fails. It genuinely locks the property.
- **`TestProfileSkipDoesNotBlindInvalidTargetDetector` (:349) — pins distinct-from-invalid
  (my round-1 MED).** A **NOT**-skipped producer, an *untraced* leader (so
  `lazyEvalSpanCtx` is genuinely invalid — a real mixed-recording loss), and a traced
  joiner that still emits its targetless wait. Asserts **`UnresolvedWaitTargets > 0`** and
  `res.Err()` fails loud. If the gating had keyed on target validity instead of the flag,
  this loss would be silently swallowed → test fails. It proves the skip gating did not
  blind my detector.

Together with the pre-existing `TestProfileSkipStaticCutSingleflightJoiner` (singleflight
collapse → `0/0` via the real gate), the three load-bearing invariants —
singleflight-collapse, lazy-cross-recipe, invalid-target-still-loud — are now
regression-locked, not merely live-validated. `TestProfileSkipGatesLazyEmit` adds the
skipped/kept lazy-emit coverage. Implementer reports all green under `-race`; the
concurrency is the standard leader-parks/joiner-joins pattern with bounded waits — plausible.

## (d) Doc/comment corrections — right and complete

Consistently OTel-only framing, no stale symmetry: cache.go:2985/3037/3138/3608/3771/3809/3998
all say "native: full detail / not profile-skip-gated"; telemetry.go:426 reframes the
predicate as "the wcprof **OTel second source** must skip"; telemetry.go:451 states
refinement-3 precisely (directly-called accessor surfaces as a coarse `"call"` op while
OTel skips only its call_exec — not source-parity). The commit subject is updated to
"…at the OTel emit." No leftover "both sources skip"/"same flag" symmetry claim remains
(the one "same flag" comment at cache.go:3037 correctly refers to the OTel mint/wait
consistency, which is exactly right).

## (e) Remaining blockers

**None.** Principle holds (`git diff --name-only 4585bf413d..4921d53662` touches no
`wcanalyze`/`wcotel`/replay file — verified for the amended commit). The round-1 LOW
caveats are unchanged and remain acceptable for merge (BSP `DroppedSpans` proxied by gate
`0/0` + native `dropped_events=0`; no side-by-side `main` capture but prior forensics +
residual breakdown suffice; construction-literal completeness empirically discharged by
§9 zero-residual on the regression workload, volume-only on others).

## Verdict

**Converged — sign off.** Native un-gate complete and clean (= validated baseline), OTel
dangle-proof intact (mint and wait read the same flag; the unsafe asymmetry is absent),
the two load-bearing invariants are now pinned by substantive real-gate tests, comments
are corrected to the OTel-only model, and zero loader/replay change holds. Ship it.
