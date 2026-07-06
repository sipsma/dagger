# wcprof × OTel — skip-fix CODE review (merge gate, Chunk 1 / loader+gate owner)

**Reviewer:** Chunk 1 owner (offline loader + §6.1 gate). Reviewed commit
`c18b17fc53` (unpushed) in the `…-skip-coder-daa3a9d2` worktree, diffed against
`4585bf413d`, and read the resulting files. No code modified, no commits.

## Verdict: LANDABLE — with two MEDIUM test additions strongly recommended first

The code **faithfully realizes the dangle-proof property I certified**, and the
**frame-homing deviation is not just acceptable — it is superior** to the v2 design the
council reviewed: it dissolves the N2 provenance audit I flagged in round 2, closes the
import gap, and eliminates the perf concern (point D), all at once. Zero loader/replay
change. The reflection-set audit is complete, the over-cut name trap is handled and
tested, and the singleflight collapse is **gate-tested end-to-end against my real
loader/gate**. I found **no correctness defect and no dangle**. The two things I'd want
before merge are **test additions** (lazy emit-path + distinct-from-invalid), because
they lock the two subtlest load-bearing invariants the code comments themselves flag —
not because the code is wrong (it isn't).

## 1. Zero loader/replay change — confirmed (the principle)

`git diff --name-only 4585bf413d..c18b17fc53` touches 8 files: `core/telemetry.go`,
`dagql/{cache,call_request,objects,result_call_frame}.go` + 3 test files. **No
`wcanalyze`/`wcotel`/loader/gate/replay file is touched.** The engine emits a smaller,
self-consistent graph; the analysis is untouched. Principle holds.

## 2. The dangle-proof property — VERIFIED in the code and gate-tested

I certified the design is dangle-proof iff every wait gates on the **target's stored
flag** and the target's span-mint gates on the **same** flag. The code does exactly
this:

- **Singleflight:** call_exec minted iff `!req.ResultCall.ProfileSkip`
  (`cache.go:3783`); `oc.profSkip` snapshotted from the **same** `req.ResultCall.ProfileSkip`
  in the same `getOrInitCallInner` (`:3805`); both waits gate on `oc.profSkip`
  (`:3995` native, `:4013` OTel). So span-minted ⟺ wait-emitted, *exactly* — the
  snapshot cannot diverge from the mint flag (same field, same req, same moment, no
  intervening mutation). The `oc.profSkip` comment states this correctly.
- **Lazy:** lazy op minted iff `!producerSkip` where
  `producerSkip = frameProfileSkip(resultCall)` / `shared.profileSkip()` (`:3036`,
  `:3038`, `:3057`); every lazy wait — joiner (`:2982`/`:3003`) and leader (`:3134`) —
  gates on the **same producer flag**. The N3 load-bearing comment ("gate on the
  PRODUCER's flag, never the joiner's own bit … Do NOT simplify … reopens a
  cross-recipe dangle") is present and correct.
- **Gate-tested:** `TestProfileSkipStaticCutSingleflightJoiner` (cache_profileskip_emit_test.go)
  drives the **real** path (`GetOrInitCall` under a recording span), compiles the
  emitted spans through `wcotel.Compile → wcanalyze.Build → wcotel.CheckStructural`,
  and asserts `UnresolvedWaitTargets == 0`, `OrphanedParents == 0`, `res.Err() == nil`
  for a skipped-claimer + same-recipe-joiner collapse. That is the strongest possible
  test of the property — it runs my actual gate.

**Conclusion: a `ProfileSkip` provenance/timing/import bug cannot re-trip my gate** —
exactly as certified. Worst case is volume/coarsening, caught by §9 measurements.

## 3. The frame-homing deviation — verified SUPERIOR, not just safe

The implementer homed the flag on `ResultCall.ProfileSkip` instead of
`CallRequest.SkipProfile` + `sharedResult.profSkip`. I scrutinized this as the biggest
departure from what the council reviewed, and it is correct **and better**:

- **Digest-EXCLUDED — verified, not just claimed.** The recipe digest is built by
  `callPB` from *explicit fields* (Type/Field/Nth/View/Receiver/Module/Args/ExtraDigests,
  result_call_frame.go:425+) — `ProfileSkip` is not among them, and there is **no
  whole-struct marshal** anywhere in the digest paths (`recipeDigest`,
  `contentPreferredDigest`, `selfDigestAndInputRefs`, `recipeID`). `ProfileSkip` is
  referenced *only* at the field decl, `clone()`, and `fork()`. And there is a
  dedicated test — `TestResultCallProfileSkipExcludedFromDigests` asserts digest
  equality across recipe/content/self for skipped vs profiled frames. So it never
  enters `callKey`/`concurrencyKey` → no cache split, no version-coupling hazard. ✓
- **N2 DISSOLVED.** My round-2 worry (provenance must be copied at every
  `storeResultCall`/`fork` site, and the enumerated audit missed `:2356`/`:2568`) is
  **gone**: the flag is intrinsic to the frame and copied in `clone()`
  (result_call_frame.go:238) and `fork()` (`:273`), so it travels with the frame
  through *every* copy/adopt/derive path with no per-site audit. `fork()` correctly
  *inherits* rather than recomputes, with a precise comment (an nth-element's immediate
  receiver is the list, so recomputing would wrongly profile a reflection-list walk).
  `TestResultCallProfileSkipTravelsThroughCloneAndFork` covers both. ✓
- **Import gap CLOSED — both sides verified.** Export `json.Marshal(frame)`
  (cache_persistence_worker.go:254) → import `json.Unmarshal` (cache_persistence_import.go:160).
  `json:"profileSkip,omitempty"` round-trips correctly (true serialized, false omitted
  → both restore correctly). So a persisted *skipped* frame stays skipped on import —
  strictly better than v2's accepted default-false volume edge. ✓
- **Perf concern (point D) ELIMINATED.** The predicate is `profileSkip(receiverTypeName,
  field)` — two map lookups, **no `egraphMu`/`ReceiverCall` walk**. `ReceiverTypeName`
  is stamped lookup-free at `objects.go:602` (`r.class.inner.Type().Name()`, the
  receiver already in hand). The 33k-descendant `egraphMu` cost I worried about in
  rounds 1–2 simply doesn't exist. ✓

## 4. Gating completeness (item 2)

Every emit site is gated on the frame-derived flag, and I checked the joins:
- N1 outer native `OpKindCall` (`cache.go:3607`, `|| req.ResultCall.ProfileSkip`),
  native execOp (`:3768`), OTel call_exec (`:3783`), native+OTel singleflight waits
  (`:3995`/`:4013`). ✓
- Lazy: native op (`:3038`), OTel span (`:3057`), joiner native+OTel waits
  (`:2982`/`:3003`), leader native wait (`:3134`). The **leader OTel wait** "follows
  for free" is correct — it is guarded by a pre-existing `if lazySpan != nil`
  (`:3144`), so a skipped producer (nil `lazySpan`) emits nothing; **no nil-deref
  panic** (I checked specifically — the argument `lazySpan.SpanContext()` is never
  evaluated when nil). ✓
- The leader native wait gate is a genuine correctness catch by the implementer:
  without it, `BeginWait(lazyOp.ID())` with a nil `lazyOp` would record a targetless
  wait (the `nil.ID() == 0` path). Gated correctly (`:3134`). ✓
- **`Result.LoadNthValue` (flagged in the mandate) is covered by frame inheritance**,
  not a fresh stamp: it forks the parent frame, which carries `ProfileSkip` — and the
  `fork()` comment is explicitly about this case. So an un-stamped derived path is not
  a hole. ✓

## 5. Reflection-set completeness + over-cut (item 3) — audit COMPLETE

- **Schema-name audit done.** Each reflection Go type's `Type().Name()` NamedType
  (core/typedef.go:74…2171) is `Function/FunctionArg/TypeDef/ObjectTypeDef/FieldTypeDef/
  InterfaceTypeDef/ScalarTypeDef/ListTypeDef/InputTypeDef/EnumTypeDef/EnumValueTypeDef`
  — **all 11 are in `reflectionTypeNames`**. The `EnumMemberTypeDef → "EnumValueTypeDef"`
  legacy-name mismatch was the **only** schema≠Go divergence, and it's handled (both
  names listed, comment explains why). No other mismatch exists. ✓
- **Over-cut name trap handled and tested.** `FunctionCall` (real `returnValue`/
  `returnError` DoNotCache work), `SourceMap`, `FunctionCallArgValue` are deliberately
  excluded (comment) and `TestProfileSkipClassifier` **asserts `FunctionCall.returnValue/
  returnError` are NOT skipped**. The real loaders (`Query.moduleSource`,
  `ModuleSource.asModule`) are non-reflection receivers and stay profiled (tested). The
  residual over-cut assumption ("no reflection-type field *forces* real work") is
  validated by §9 (processRun unchanged) — low risk; a full read of the
  `dagql.Fields[*core.<ReflectionType>]` blocks (module.go:418-648) is the
  belt-and-suspenders the implementer claims done.

## 6. Distinct-from-invalid (item 4) — preserved in code; NOT tested

The property survives: `c.wait` gates the OTel wait on `oc.profSkip` (`:4010`), **not**
on target validity — so a *non-skipped* target whose `execSpanCtx` is invalid (genuine
mixed/untraced recording) still calls `EmitOTelWait` with the invalid target → targetless
wait → `UnresolvedWaitTargets` → gate fails loud. The comment says exactly this. **But
there is no test asserting it** (see item 7). Verified by inspection; not regression-locked.

## 7. Tests (item 5) — strong, with two load-bearing GAPS

Covered well: classifier + name trap + EnumValueTypeDef dual name (telemetry_skip_test.go);
stamp-before-`IsSkipped` ordering; debug-independence; root-set match; clone/fork carry;
**digest exclusion** (3 digests); singleflight emit gating on the real path; and the
**gate-tested** skipped-claimer+joiner collapse. That is a high bar.

**Gaps — both load-bearing, neither tested hermetically:**
- **[MEDIUM] No lazy emit-path test.** N3 (cross-recipe lazy gating) is the subtlest
  invariant, and the code comment itself warns against a future "simplify to the
  waiter's bit." There is no test driving `evaluateOne`. Critically, the **§4.4 case
  (a non-skipped forcer of a skipped producer → wait dropped, `UnresolvedWaitTargets ==
  0`) may not have occurred in the §9 live capture**, so it could be entirely
  unexercised. Add a lazy test mirroring the singleflight-joiner gate test:
  (a) skipped producer → no lazy op/waits, gate 0/0; (b) non-skipped forcer of a
  skipped producer → no dangling wait, gate 0/0.
- **[MEDIUM] No distinct-from-invalid test.** The detector that keeps my gate able to
  catch genuine mixed-recording loss (item 6) is preserved but not regression-tested.
  Cheap: a non-skipped target with an artificially invalid `execSpanCtx`, assert
  `UnresolvedWaitTargets > 0`. Without it, a refactor that "optimizes" the wait to gate
  on validity would silently blind the detector and pass CI.

These are the two invariants whose code comments explicitly say "do not break this" —
exactly what merits a hermetic test. They don't block correctness (live-validated +
inspection-verified), but a merge gate should lock them.

## 8. §9 adequacy + caveats (item 7) — acceptable for merge

- **Gate 0/0 + zero residual introspection** is the load-bearing proof and it's the
  right one (my gate is precisely what drop-induced loss trips). The zero-residual
  result *also* empirically discharges the construction-literal completeness on the
  regression workload (a reflection frame reaching an emit gate with `ProfileSkip=false`
  would have surfaced as residual). ✓
- **No side-by-side `main` baseline:** acceptable — the prior 3-capture forensics
  established `main ≈ 3.4k`, and the post-fix residual (1646 call_exec + 1646
  publishResult real-miss pairs) is accounted for. The remaining `~10.5k` vs `main` gap
  is the **always-on second-source per-miss cost**, a separate scaling question (out of
  scope; correctly flagged).
- **BSP `DroppedSpans` proxied, not instrumented:** acceptable, because the **gate
  0/0** is the actual correctness criterion (dropped parent/target spans *are* what the
  gate catches), but instrumenting the counter is a cheap future strengthening.
- **Adopted/imported not live-captured:** acceptable — frame-homing makes them
  by-construction correct (clone/fork/JSON all verified), and `clone/fork` + the
  export→import round-trip are unit/inspection covered.
- **telemetry-off + native-on dev corner:** `ProfileSkip` stays false → native
  re-profiles introspection. Dev-only, self-consistent (no dangle per the
  certification). Flagged, acceptable.

## Bugs / races / perf / simplicity (item 8)

- **Races: none.** `ProfileSkip` is written once by `AroundFunc` on the freshly-built,
  not-yet-shared frame (objects.go:584 → stamp before `GetOrInitCall`), then only
  *copied* (clone/fork) — effectively immutable after stamp. `oc.profSkip` is set under
  `callsMu` before the oc is published; joiners read it post-publish. Lazy reads go
  through `loadResultCall` under `resultCallMu`. Set-before-read everywhere; no torn
  read; and `ProfileSkip` is deterministic from the recipe, so even a frame-object swap
  preserves the value.
- **Perf: improved** (lookup-free stamp; two map lookups per call vs the egraphMu walk
  the council feared). The stamp now runs for inherited-skip descendants (before the
  `IsSkipped` return) — but it's two map lookups, negligible.
- **Simplicity: improved** over v2 (no `sharedResult.profSkip`, no `initCompletedResult`
  provenance surgery, no N2 audit). ~+150 LOC, localized, well-commented.

## Severity-ranked issues

| sev | issue | action |
|---|---|---|
| **MED** | No lazy emit-path test; §4.4 cross-recipe case possibly unexercised by §9 | Add lazy gate test (skipped producer; non-skipped forcer of skipped producer → 0 UnresolvedWaitTargets) **before merge** |
| **MED** | No distinct-from-invalid test (detector not regression-locked) | Add invalid-target test asserting `UnresolvedWaitTargets > 0` **before merge** |
| LOW | Construction-literal completeness for *other* workloads (call_request_input.go:118, server.go:1529/1948) | §9 zero-residual covers the regression workload; volume-only, not dangle — note, monitor |
| LOW | BSP `DroppedSpans` proxied not instrumented; over-cut full-field-read assumed-done | Cheap future strengthening; gate 0/0 + processRun-unchanged suffice for merge |
| NIT | `EnumMemberTypeDef` in the set is dead (live schema name is `EnumValueTypeDef`); kept defensively | Fine as-is (comment says so) |

## Bottom line

**Landable.** The fix is correct, dangle-proof (verified + gate-tested), zero
loader/replay change, with a frame-homing design that is genuinely better than the
reviewed v2 (dissolves N2, closes the import gap, kills the perf concern), and a clean
§9 PASS. **Strongly recommend adding the two MEDIUM tests (lazy emit-path + distinct-
from-invalid) before merge** — they hermetically lock the two load-bearing invariants
the code itself flags as "do not break," the only material gap between "validated on one
live capture" and "protected against regression." Everything else is acceptable caveat
or nit. No correctness blocker.
