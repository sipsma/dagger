# Code review — wcprof skip-the-reflection-class implementation (v2-spec author, 9th reviewer / merge gate)

**Reviewer:** author of the v2 design plan (`wcprof-otel-skip-impl-plan.md`).
**Commit:** `c18b17fc53` (unpushed) in `wcprof-otel-skip-coder-daa3a9d2`. Reviewed `git show` + `git diff 4585bf413d..c18b17fc53` + the resulting files. Every claim re-verified against the code (file:line are the coder worktree). Review only — I did not touch the implementer's branch.

---

## Verdict

**NOT landable as-is — one BLOCKER (a late Erik ruling that overrides the spec), then landable.** The code is *correct and faithful to the v2 spec I reviewed against* — zero loader/replay change, every gating site present and correct, a complete/pure reflection-type predicate, and frame-homing closing N2 + the LoadNthValue bypass more cleanly than my v2 spec did. I found **no correctness bug, no race, no nil-deref, no missed gate.**

**BLOCKER-0 (direction, not a code defect): native must be UN-gated.** After I drafted this review, Erik issued a ruling that **overrides the "skip both native + OTel from one decision" decision** (which was the lead's lean, carried into my v2 spec and round-2 folding #4 — *not* Erik's instruction): **only the OTel source may skip the reflection class; the native recorder must emit it exactly as at baseline `4585bf413d`.** Rationale (Erik): native wcprof is opt-in/dev-only, off the volume-constrained always-on path, and its value is *full detail*; stripping reflection ops from it serves no benefit, is actively harmful, and needlessly modifies validated PR #13393. This commit gates native (its §9 line "native … 0 reflection-class ops" **is** the harm Erik names), so the native-side gating must be reverted (§ BLOCKER-0 below). This does **not** touch the OTel correctness, which is the substance of the fix — it is a contained revert of the native gates, leaving the (already-correct) OTel skip intact. I verified the native gating was implemented correctly *for skip-both*; that verification stands, but the decision under it is overruled.

The remaining REAL findings are **test-coverage gaps** (MED→LOW) and validation caveats. Beyond BLOCKER-0, the one I'd want before merge is **a lazy-path regression test** — the lazy producer-flag gating (N3) is the subtlest, explicitly "do-not-simplify" invariant with only whole-capture (§9) coverage today.

---

## What I verified is correct (the confirmed core — do not relitigate)

### Predicate (`core/telemetry.go`)
- `profileSkip(receiverTypeName, field)` (`:449`) is **debug-independent** (no `ctx` param — structurally cannot read debug baggage) and **separate** from `introspectionInfo` (left intact; the `:469` refactor only swaps the inline root `switch` for the shared `introspectionRootFields` map — behavior-preserving). ✓ This is exactly the v2 flip; my v1 debug-gating is gone.
- **Reflection set is complete and the schema names are right.** I cross-checked every entry against `core/typedef.go` `Type().NamedType`: `Function`(74), `FunctionArg`(638), `TypeDef`(825), `ObjectTypeDef`(1191), `FieldTypeDef`(1475), `InterfaceTypeDef`(1596), `ScalarTypeDef`(1766), `ListTypeDef`(1813), `InputTypeDef`(1882), `EnumTypeDef`(1990) — all match. The enum-member type's **live schema name is `EnumValueTypeDef`** (typedef.go:2171), not its Go name `EnumMemberTypeDef`; the set lists **both** (`:421-422`) and the call-site stamp carries the schema name, so it is caught. The only other walkable object types in `typedef.go` are `FunctionCall`(2420), `FunctionCallArgValue`(2512), `SourceMap`(2554) — all **correctly excluded** (the name trap). `FunctionCachePolicy`(488) and `TypeDefKind`(2370) are **enums** (arg/return values, never a receiver), so correctly omitted. There is no 13th reflection object type lurking.
- **Over-cut audit holds.** No field on a reflection type does real work: the new accessors (`as*`/`args`/`returnType`/`typeDef`/`fields`/`functions`/`members`/`elementTypeDef`) are pure metadata reads; the builders (`with*`/`__*`) are in-memory schema construction (already UI-suppressed today). The only exec/load-shaped match in a reflection-type field grep was `__loadInputTypeDef`, a Query root field that loads a *typedef* (metadata), not real work. Module loading (`Query.moduleSource`, `ModuleSource.asModule`) lives on non-reflection receivers → stays profiled.

### Plumbing / frame-homing (`call_request.go`, `objects.go`, `result_call_frame.go`)
- `ReceiverTypeName` stamped at `objects.go:602` from `r.class.inner.Type().Name()` — **the identical expression already used at `objects.go:650/672/675`** in the existing `ObjectResult.call`, so it is established-safe on the same `r`; no new nil-deref/panic risk on the hot path. Copied in `CallRequest.Clone()` (`:44`). ✓ (This is round-2 folding #2 — the lookup-free receiver type, replacing the ineffective memo, and it also sidesteps the `egraphMu` lock that a `ReceiverCall`-based predicate would take.)
- `AroundFunc` stamps `req.ResultCall.ProfileSkip = profileSkip(req.ReceiverTypeName, req.Field)` (`:40`) **before** the `IsSkipped` early return — so an inherited-skip descendant under a `hideCtx` is classified by its own recipe. Directly tested (`TestAroundFuncStampsProfileSkipBeforeInheritedSkip`): reflection work under `hideCtx` → skipped; **real work under the same `hideCtx` → stays profiled** (the over-cut guard). ✓
- **Digest exclusion verified two ways:** (1) `recipeDigestWithVisiting` (`:632`) and `callPB` (`:417`) build from explicit fields/`callpbv1.Call`, never whole-struct JSON, so `ProfileSkip` cannot leak in; (2) `TestResultCallProfileSkipExcludedFromDigests` asserts equal recipe/content-preferred/self digests for skipped-vs-profiled frames. So it never enters `callKey`/`callDigest`/`concurrencyKey`. ✓
- **JSON round-trip verified:** persistence does `json.Marshal(frame)` (`cache_persistence_worker.go:254`) / `json.Unmarshal(...frame)` (`cache_persistence_import.go:160`), and `ProfileSkip json:"profileSkip,omitempty"` round-trips (`TestResultCallProfileSkipRoundTripsThroughJSON`). **This genuinely CLOSES the N2 import gap** — better than my v2's "bounded volume edge / default-false": an imported reflection-lazy result carries the producer's skip. The two serializations (explicit-field digest vs JSON persistence) differ, so digest-exclude and persist-include do not conflict. ✓
- `ProfileSkip` copied in **both** `clone()` (`:238`) and `fork()` (`:276`) — the only frame copy paths — tested (`TestResultCallProfileSkipTravelsThroughCloneAndFork`). `fork()`'s inheritance comment is correct: an nth-element's immediate receiver is the list, so *recomputing* would wrongly un-skip a reflection-list walk; inheriting the producer's bit is right. ✓

### Why frame-homing is strictly better than my v2 spec (verified)
My v2 put the bit on `CallRequest` + `sharedResult` and required an N2 per-site provenance audit of `initCompletedResult` (adopt/copy/req paths). The implementer instead homes it on the **frame**, so:
- `initCompletedResult` needs **zero** changes: whatever frame it stores (adopt at `cache.go:4132`, copy at `:4151`, req at `:4160`) already carries the producer's `ProfileSkip` via `clone()`. The N2 audit is obviated **by construction**.
- **The LoadNthValue bypass is handled for free.** `Result[T].NthValue` (`cache.go:2319`) builds the element `CallRequest{ResultCall: parentCall.fork()}` (`:2371`) and calls `GetOrInitCall` **directly at `:2390` — bypassing `AroundFunc`**, with `ReceiverTypeName` unset. A `CallRequest.SkipProfile` (my v2) would default false there → reflection elements wrongly profiled. The frame-homed bit, carried by `fork()`, is read by the emit gates instead, and `AroundFunc` is *not* re-invoked, so the inherited bit is **not** clobbered to `profileSkip("", field)=false`. Correct, and a real strengthening of the design. ✓

### Gating (`cache.go`) — every site present, target-flag, distinct-from-invalid
- **N1 outer native `OpKindCall`** gated: `... || req.ResultCall.ProfileSkip` at `:3607` (short-circuits before the deref; `req`/`req.ResultCall` nil-checked first). ✓ — the gap my v2 named; without it native keeps a call op the OTel side drops → oracle skew.
- Inner native `execOp` `:3768` (`&& !req.ResultCall.ProfileSkip`); OTel `call_exec` `:3783` (same). `oc.profSkip` snapshotted at claim `:3808` (under `callsMu`, before publish — Invariant T). ✓
- Singleflight waits gate on the **target's** `oc.profSkip`: native `:3995`, OTel `:4013`. **Distinct-from-invalid preserved** — the gate is `if !oc.profSkip { EmitOTelWait(...) }`, NOT on `execSpanCtx.IsValid()`, so a *non-skipped* target with a genuinely invalid span still emits the targetless wait → `UnresolvedWaitTargets` → gate fails loud (the mixed-recording detector). ✓
- Lazy gates on the **producer frame** (N3, load-bearing): joiner native `:2999`/OTel `:3006` on `shared.profileSkip()`; leader native `lazyOp` `:3039`/OTel span `:3057`/native leader wait `:3138` on `frameProfileSkip(resultCall)`; OTel leader wait follows for free (`lazySpan==nil`). The code carries the explicit "do NOT simplify to the waiter's bit — cross-recipe dangle" comment. ✓
- `publishResult`/`pubOp` **follow for free** (unchanged): skipped → `execSpan` never minted → `execSpanCtx` invalid + `profOpID==0` → neither emits. Confirmed by `TestProfileSkipGatesSingleflightEmit` (exactly 1 publishResult, for the kept miss). `(*wcprof.Op)(nil).ID()==0` (record.go:124) and `(*wcprof.Wait)(nil).End()` nil-safe (record.go:247) both verified, so the new `var profWait *wcprof.Wait; if ... ; profWait.End()` lazy pattern cannot panic.
- **Lock-safety verified:** the leader reads `frameProfileSkip(resultCall)` (lock-free field read on the already-loaded, immutable frame) — no lock acquired under `lazyMu`. The joiner reads `shared.profileSkip()` (→ `resultCallMu.RLock`) **after** `lazyMu.Unlock()` — no `lazyMu`→`resultCallMu` nesting. `ProfileSkip` is set-once-before-store and never mutated, so the lock-free/RLock reads are race-free. ✓

> **All the gates above are correctly implemented *for skip-both* — but per BLOCKER-0 the NATIVE ones must be reverted** (N1 outer `OpKindCall` `:3607`; native `execOp` `:3768`; native singleflight `BeginWait` `:3995`; native lazy `lazyOp` `:3039`; native lazy `BeginWait` `:2999`/`:3138`; native `pubOp` un-gates for free once `execOp` does). The **OTel** gates (`call_exec` `:3783`, OTel waits `:4013`/`:3006`, OTel lazy span `:3057`) **stay** — they are the substance of the fix. The frame-homed predicate stays (it now feeds OTel only).

### Zero loader/replay change
`git diff --stat` under `engine/wcprof/` and `engine/wcanalyze/` is **empty**. The analysis stays a rational function of faithful data; the engine just emits a smaller, self-consistent graph. ✓

---

## Findings (severity-ranked)

### BLOCKER-0 — Native must NOT skip the reflection class (Erik ruling overrides skip-both)
**This is a direction reversal, not an implementation defect** — the code correctly implements skip-both, which is what the v2 spec and round-2 folding #4 told it to do. But Erik has since ruled that **only the OTel source skips**; the native recorder must emit reflection ops exactly as at baseline `4585bf413d`. Required change (a contained native-side revert; OTel untouched):
- **Un-gate every native emit**, restoring baseline:
  - `cache.go:3607` — outer native `OpKindCall`: drop the `|| req.ResultCall.ProfileSkip` clause (back to `!wcprof.Enabled || req==nil || req.ResultCall==nil`). *(My v2 "N1" finding is hereby obsolete — native keeps its outer call.)*
  - `cache.go:3768` — native `execOp`: drop `&& !req.ResultCall.ProfileSkip` (back to `if wcprof.Enabled(ctx)`). This auto-restores native `pubOp` (its `profOpID != 0` gate).
  - `cache.go:3995` — native singleflight `BeginWait`: drop `&& !oc.profSkip` (back to `if wcprof.Enabled(ctx)`).
  - `cache.go:3039` — native lazy `lazyOp`: drop `&& !producerSkip` (back to `if wcprof.Enabled(evalCtx)`).
  - `cache.go:2999` and `:3138` — native lazy `BeginWait` (joiner + leader): restore unconditional `wcprof.BeginWait(...)`.
- **Keep all OTel gates and the frame-homed predicate.** `oc.profSkip` / `producerSkip` are still computed and consumed by the OTel gates; native simply no longer reads them.
- **Oracle scope (already true, now explicit):** the cross-source oracle compares **non-reflection** classes only — native carries reflection detail OTel intentionally lacks. That was always the oracle's real scope (user-work self-time parity), so no validation power is lost.
- **§9 native assertion flips:** the reported "native dump has 0 reflection-class ops" must become "native is unchanged from baseline (reflection ops present); only OTel drops them." Re-run the native-dump check post-revert to confirm native == baseline.
Severity BLOCKER because the merge target (per Erik) is the opposite of what this commit does on the native side, and it touches the validated PR #13393 recorder. The fix is small and isolated.

### MED-1 — No lazy-path emit/unit test for the N3 producer-flag gating
The lazy gating is the subtlest, most fragile invariant in the change (cross-recipe forcer; gate on the **producer's** stored flag, never the waiter's bit; the code itself shouts "Do NOT simplify this"). Yet the test suite drives only the **singleflight** path (`cache_profileskip_emit_test.go`). The lazy path has **no** unit/emit-path coverage — only the whole-workload §9 capture ("zero residual lazy ops"), which is not a regression guard. A later refactor could "simplify" `shared.profileSkip()` to the joiner's own bit and silently reopen the cross-recipe dangle, with green unit tests.
**Recommend (before merge or immediate fast-follow):** an `evaluateOne`-driven test: (a) a **skipped producer** → no `lazy` op, no lazy waits, gate `0/0`; (b) a **non-skipped (real-work) producer forced by a skipped introspection forcer** → lazy op present, re-homes, gate `0/0`; (c) ideally the §4.4 case — a **non-skipped forcer of a skipped-producer** value → its wait is dropped (folds into self-time), **no dangle** — pinning the accepted coarsening so it can't silently become a dangle.

### LOW-2 — No dedicated "ProfileSkip didn't blind the distinct-from-invalid detector" test
My v2 §8 flagged this as load-bearing. The **code** preserves the detector (gate is `!oc.profSkip`; the non-skipped path is byte-identical to before, and the pre-existing `otelprof_hooks_test.go` covers `EmitOTelWait`'s targetless emission on an invalid target). So the behavior is covered transitively. But there is no single test asserting "`oc.profSkip==false` + invalid `execSpanCtx` ⇒ `UnresolvedWaitTargets>0`" after the skip change. Low risk; a 10-line guard would make the intent explicit.

### LOW-3 — §9 validation caveats (implementer-flagged; acceptable, one worth closing)
- **BSP `DroppedSpans` proxied, not directly instrumented.** My v2 §9.7 called the before/after `DroppedSpans→0` the *decisive* proof that removing the amplifier closes the orphan loss. The amplifier removal (16,589 → 1,646 `call_exec`) + gate `0/0` on a fresh capture is strong indirect evidence, but the direct counter is the cleanest causal proof; close it if cheap.
- **No side-by-side `main` baseline** for the validation workload (volume is compared against the regression, not `main`). Fine given the v2 success metric is "amplifier gone + residual is real kept work," not "equal to `main`" (the reported 1,646 `call_exec` + 1,646 `publishResult` is the legitimate always-on per-real-miss pair).
- **Adopted/imported results not separately LIVE-captured.** Covered at unit level (clone/fork + JSON round-trip + digest-exclusion tests) and correct by construction, so acceptable — but a warm/import capture asserting gate `0/0` would fully retire the N2 thread.

### INFO — telemetry-off + native-on dev corner
If `s.telemetry==nil` (no `AroundFunc`) but `wcprof.Enabled`, `ProfileSkip` stays false and native re-profiles the reflection class. Dev-only, self-consistent (no dangle), correctly flagged by the implementer. No action.

---

## Things I explicitly checked and found NOT to be problems (so the council needn't re-open them)
- **Digest leak** of `ProfileSkip` → no (explicit-field digests + test).
- **Import gap** (N2) → CLOSED by JSON round-trip (not merely bounded).
- **`r.class.inner.Type().Name()` panic** at :602 → no (same expression already live at :650/672/675 on the same `r`).
- **Reflection-set under-cut** (other schema-name≠Go-name mismatches; other walkable metadata types) → none; set is complete.
- **Over-cut** (a reflection-type field doing real work) → none found; module-load slow path is on non-reflection receivers.
- **nth-element / `fork()` under/over-cut** → correct: reflection-parent elements inherit skip; non-reflection-parent elements stay profiled (negligible, real-ish list indexing), and calls *on* an element re-classify via `AroundFunc`.
- **Nil derefs / short-circuit ordering** at `:3607`/`:3768`/`:3808` → safe (nil checks precede the `.ProfileSkip` read).
- **Races on `ProfileSkip`** → none (set-once before store, immutable after).
- **Precision refinement #3** → correctly realized: directly-called accessors keep a surviving `dag.call` "call" op in OTel while native has none, so "both sources drop the same class" is appropriately qualified; §9 measured this divergence = 0 on the workload (no top-level accessor calls outside a `hideCtx`), and it is documented as can-be-nonzero by design (profiler skips ⊋ UI suppresses).
- **All gating sites** from v2 §6 present (N1 + native `OpKindCall`/`execOp`/waits/lazy + OTel `call_exec`/waits/lazy + symmetry); none missed.

---

## Answers to the merge-gate questions
1. **Faithful to the v2 spec + four foldings?** Yes to the spec as written (predicate, plumbing, every gating site, native+OTel symmetry, distinct-from-invalid, all four foldings; frame-homing improves on it). **But the spec's native+OTel symmetry is now overruled by Erik (BLOCKER-0)** — faithful-to-spec here means the native side must be reverted to satisfy the new (correct) direction.
2. **Frame-homing correct?** Yes — copied in `clone()` + `fork()` (the only copy paths), digest-excluded, JSON-persisted/round-tripped; `initCompletedResult` changes genuinely unnecessary.
3. **Gating completeness?** Yes — incl. the `Result.NthValue`/`GetOrInitCall` direct path (handled by `fork()` inheritance, no `AroundFunc` clobber). (The OTel gates are complete; the native gates are present but, per BLOCKER-0, are to be removed, not kept.)
4. **Reflection-set completeness / over-cut?** Complete (incl. the `EnumValueTypeDef` catch); no other mismatch; no reflection-type field forces real/lazy work; `FunctionCall.returnValue/returnError` correctly profiled.
5. **Distinct-from-invalid + precision #3?** Preserved/realized correctly.
6. **Tests adequate / zero loader-replay change?** Loader/replay untouched. Classifier, name-trap, stamp-before-IsSkipped, frame clone/fork/JSON/digest, and singleflight emit + static-cut joiner collapse are all strong. **Gaps: lazy emit-path test (MED-1), dedicated distinct-from-invalid test (LOW-2).**
7. **§9 adequate?** Adequate for the core correctness bar (gate 0/0, zero residual reflection/introspection on the OTel side, divergence 0). The "native dropped reflection" line is the BLOCKER-0 harm and must flip to "native == baseline" after the revert. Caveats (LOW-3) acceptable; the BSP `DroppedSpans` direct measurement is the one worth closing.

## Bottom line
**Do not merge as-is; merge after one required revert + one recommended test.** (1) **BLOCKER-0** — revert the native-side gating so only OTel skips (Erik's ruling, ~6 one-line reversions in `cache.go`, OTel side untouched); re-run the native-dump §9 check to confirm native == baseline. (2) **MED-1** — add the lazy-path regression test (guards the one invariant most likely to be silently broken later). Everything else is correct, faithful to the spec, and improves on it (frame-homing closes N2 + the LoadNthValue bypass); the **OTel skip — the substance of the fix — needs no change**, and the loader/replay are untouched. With those two, ship it.
