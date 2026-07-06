# Code review round 2 (convergence) — wcprof skip-the-reflection-class (v2-spec author, merge gate)

**Reviewer:** v2 design-plan author (raised BLOCKER-0 + MED-1 in round 1).
**Commit:** `4921d53662` (amended over `c18b17fc53`, unpushed) in `wcprof-otel-skip-coder-daa3a9d2`. Reviewed `git diff c18b17fc53..4921d53662` (the focused delta), the final state of every touched site, and the new tests. Review only — branch untouched. File:line are the coder worktree.

---

## Verdict: **SIGN OFF on convergence.** Both my round-1 findings are resolved; no code blocker remains.

I verified the native revert is **complete and correct**, the OTel side is **unchanged and still faithful** (including a new asymmetric interaction I had to prove safe), and the three new tests are **adequate and high-quality**. I am flagging **one MEDIUM, non-blocking consequence of Erik's native-keeps-detail ruling** — it is "working as ruled," not a code defect, but it changes the cross-source oracle methodology and the round-2 §9 did not (and now cannot *naively*) run that oracle. It does not block this merge.

---

## (a) BLOCKER-0 — native un-gated, COMPLETE and correct ✓

Every native gate I named in round 1 is reverted to baseline `4585bf413d`; `ProfileSkip` no longer reaches any native gate. Verified at file:line in the final commit:

| native site | round-1 (gated) | round-2 (reverted to baseline) |
|---|---|---|
| outer `OpKindCall` `getOrInitCall:3611` | `… \|\| req.ResultCall.ProfileSkip` | `if !wcprof.Enabled(ctx) \|\| req==nil \|\| req.ResultCall==nil` ✓ |
| native `execOp` `:3768` | `&& !req.ResultCall.ProfileSkip` | `if wcprof.Enabled(ctx)` ✓ |
| native singleflight `BeginWait` `:3994` | `&& !oc.profSkip` | `if wcprof.Enabled(ctx)` ✓ |
| native lazy `lazyOp` `:3041` | `&& !producerSkip` | `if wcprof.Enabled(evalCtx)` ✓ |
| native lazy joiner `BeginWait` | `if !producerSkip {…}` | unconditional `profWait := wcprof.BeginWait(…)` ✓ |
| native lazy leader `BeginWait` `:3141` | `if !producerSkip {…}` | unconditional `profWait := wcprof.BeginWait(…)` ✓ |
| native `pubOp` | (followed `profOpID!=0`) | restored for free — `execOp` un-gated ⇒ `profOpID!=0` for reflection ⇒ `pubOp` emits ✓ |

§9 confirms the intent: the native dump now **contains** the reflection class (53,433 reflection-class ops) and still loads in `wcanalyze`. Native is back to full baseline detail — exactly Erik's ruling. ✓

## (b) OTel side still faithful — un-gating native did NOT perturb it ✓

The OTel gates are byte-identical to round 1 (the revert touched only native). Confirmed present in the final code:
- OTel `call_exec` `:3784`: `if OTelProfActive(callCtx) && !req.ResultCall.ProfileSkip`. ✓
- OTel singleflight wait `:4016`: `if !oc.profSkip { EmitOTelWait(…) }` (distinct-from-invalid comment intact). ✓
- OTel lazy span `:3060`: `if OTelProfActive(evalCtx) && !producerSkip`. ✓
- OTel `publishResult` `:4080`: `if oc.execSpanCtx.IsValid()` (follows). ✓
- `oc.profSkip` / `producerSkip` retained, now read **only** by these OTel gates. ✓

**The new asymmetric case (native ON, OTel OFF for the same skipped call) — proven safe.** This is genuinely new: at baseline native+OTel were always both-on or both-off; now, for a skipped reflection call, native `wcprof.BeginOp` mints an op (reassigning `callCtx`/`evalCtx`) while OTel mints nothing. I checked whether that native reassignment could capture OTel children. It cannot: `wcprof.BeginOp` (`engine/wcprof/record.go:93`) returns `ContextWithOpID(ctx, op.id)` — it stores **only a wcprof op id**; `record.go` has **zero** OTel imports (no `trace.`/`Tracer`/`ContextWithSpan`/`SpanFromContext`). So for a skipped call the OTel current span in `callCtx`/`evalCtx` stays the parent's recording span, OTel `call_exec`/`lazy` are not minted, and OTel sub-spans **re-home** to the nearest kept ancestor — while the native op records the full reflection subtree via the independent op-id chain. The two recorders use disjoint context mechanisms; the dangle-proof + re-homing arguments hold unchanged. ✓
**And the volume fix is intact:** native ops go to the in-process recorder, never the OTel `BatchSpanProcessor` — so 53,433 native reflection ops do **not** touch the queue that overflowed. Only OTel feeds the always-on path, and it stays clean (§9: 1,646 `call_exec` + 1,646 `publishResult`, 0 residual reflection/introspection, gate 0/0). ✓ (Native is `wcprof.Enabled`-gated = opt-in/dev-only, off in production, so its full detail is free.)

## (c) MED-1 (+ §8.5) tests — adequate, and exactly the load-bearing ones ✓

`dagql/cache_profileskip_emit_test.go` (all drive the REAL `Cache.Evaluate → evaluateOne` / `GetOrInitCall` paths under a recording span; reported passing under `-race`):
- **`TestProfileSkipGatesLazyEmit`** (table) — skipped producer ⇒ 0 lazy OTel span + 0 lazy wait; kept producer ⇒ both present and resolve; gate clean both ways; asserts the **producer frame carries the bit** (frame-homing). Covers MED-1(a)/(b).
- **`TestProfileSkipLazyCrossRecipeForcerStaysClean`** — the load-bearing N3 case: a **real concurrent** traced leader + traced joiner (different recipe) on a **single shared root trace** (so the loader sees one trace), forcing a skipped producer's pending value; asserts 0 lazy span, 0 lazy wait, **`UnresolvedWaitTargets==0`**, gate passes. Its comment pins the invariant ("were the gate keyed on the joiner's own bit → targetless wait → UnresolvedWaitTargets"). This is precisely the guard I asked for; it would fail if anyone "simplifies" the lazy gate to the waiter's bit.
- **`TestProfileSkipDoesNotBlindInvalidTargetDetector`** (§8.5) — untraced leader ⇒ genuinely invalid `lazyEvalSpanCtx`; traced **non-skipped** joiner ⇒ still emits a targetless wait; asserts **`UnresolvedWaitTargets>0` AND `res.Err()!=nil`** (gate fails loud). The strongest form — it locks out a future "gate on `execSpanCtx.IsValid()`" regression. Resolves LOW-2.

The round-1 singleflight tests (`TestProfileSkipGatesSingleflightEmit`, `TestProfileSkipStaticCutSingleflightJoiner`) and the classifier/frame tests are unchanged and still strong.

## (d) Doc / comment corrections — right ✓

`profileSkip`, `AroundFunc`, `ResultCall.ProfileSkip`, `oc.profSkip`, and every cache.go gate comment now say "OTel second source skips; native keeps full detail." **Refinement-3 is stated precisely**: a directly-called reflection accessor (outside a `hideCtx`) keeps its `dag.call` span → a coarse OTel `"call"` op, while native keeps the full op — "NOT source-parity … the cross-source oracle compares only the non-reflection classes," and "in practice these accessors are ~always under a `hideCtx`, so the divergence is empirically nil." Accurate. (Trivial nit, non-blocking: the inserted sentence in the `ResultCall.ProfileSkip` doc runs onto a long line with "It is set once" — cosmetic only.)

## (e) Remaining items

### MED (NON-BLOCKING) — the asymmetric skip breaks the *naive* cross-source oracle on non-reflection ancestors of reflection work
This is a **consequence of Erik's ruling, not a code defect**, but it is worth stating because the cross-source oracle is the effort's load-bearing validation and the memory framing ("oracle compares non-reflection classes only") **understates** the needed adjustment. Concretely: a non-reflection op `U` (e.g. `ModuleSource.asModule`) that synchronously does reflection sub-work `R`:
- **OTel:** `R` is skipped (no op), so its time **folds into `U`'s self-time** (the accepted coarsening).
- **Native (now un-gated):** `R` has its own op (child of `U`), so its time is **subtracted from `U`'s self-time**.

→ `U_otel.self = U_native.self + time(R)`. So even a *non-reflection* class diverges in per-class self-time. (Round-1's skip-both *preserved* parity because both sources absorbed `R` identically; Erik's native-keeps-detail ruling necessarily breaks that exact-match.) A naive per-class self-time oracle will show spurious divergence on every non-reflection ancestor of reflection work and could be misread as "the OTel source is wrong." The fix is **methodology, not code**: before comparing, fold native's reflection ops into their nearest non-reflection ancestor (mirroring OTel's absorption), or compare at the **ranking/bottleneck** level (which still agrees) rather than per-class self-time. **Recommend** the lead/Erik adopt the absorption-matched oracle and run it on the round-2 build before treating the oracle as green — round-2 §9 confirmed *native-loads* + *OTel-clean* but did **not** run the native↔OTel comparison (and the pre-skip "user-work self-time matches native EXACTLY" bar no longer applies). This does not block the code: the OTel source is unchanged from the (validated) round-1/v2 output and is self-consistent for its own purpose; only the cross-source comparison needs the adjustment.

### LOW (carried from round 1, non-blocking) — close if cheap
- BSP `DroppedSpans` still **proxied** (`dropped_events=0`), not directly instrumented — the cleanest causal proof of the orphan-loss closure; instrument if cheap.
- No side-by-side `main` baseline (fine; the v2 success metric is amplifier-gone + residual-real, not equal-to-`main`).
- Adopted/imported results validated by unit tests (clone/fork/JSON/digest) + by-construction, not a live warm/import capture.

---

## Bottom line
**Convergence reached — sign off.** BLOCKER-0 is fully and correctly resolved (complete native revert, OTel gates intact, the new native-on/OTel-off asymmetry proven safe because `wcprof.BeginOp` pushes no OTel span), and MED-1/§8.5 are covered by real-path, race-tested guards including the exact N3 cross-recipe forcer and a gate-must-fail invalid-target test. Zero loader/replay change. The OTel volume fix stands; native is back to full baseline detail per Erik. The only open item is **validation methodology** (the absorption-matched cross-source oracle, MED non-blocking) plus the carried LOW caveats — none of which blocks merging this code. Ship it; route the oracle-methodology note to the lead for the effort's final validation.
