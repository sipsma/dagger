# Skip-fix code review (merge gate) — design author, commit `c18b17fc53`

Reviewed the diff (`git diff 4585bf413d..c18b17fc53`) and resulting files in the coder
worktree. Bias to finding real issues; everything below is grounded in file:line and I
verified the implementer's summary rather than trusting it.

## Verdict

**Landable — no correctness bugs found.** The implementation is faithful to the v2
design + the four round-2 foldings, and the one notable deviation (ProfileSkip on the
**frame** rather than `CallRequest.SkipProfile` + `sharedResult.profSkip`) is a genuine
**improvement** that I verified is sound. Two **MEDIUM** test gaps are worth closing
before merge (distinct-from-invalid emit; lazy emit-path); the rest are accepted
caveats. Zero loader/replay change confirmed; UI-isolation and the call-site stamp (my
round-2 calls) are correctly realized; no conflict with the canonical design/invariants.

## What I verified (the load-bearing claims)

- **Digest exclusion — VERIFIED, the critical one.** `recipeDigestWithVisiting`
  (result_call_frame.go:632) is **field-explicit hashing** (Receiver, Type, identity
  field, Args, ImplicitInputs) — it does **not** marshal the frame, so a new struct
  field can't leak in. A full `ProfileSkip` grep shows it referenced **only** in the
  frame def/clone/fork, the `AroundFunc` stamp, and the cache gates — **never** in any
  digest/`callPB`/`selfDigest`/`recipeID` method. So `callKey = callDigest`
  (cache.go:3669) excludes it → it never splits the cache or the `{callKey,SessionID}`
  singleflight key. The `json:"profileSkip,omitempty"` tag affects only JSON persistence,
  a separate encoding. `TestResultCallProfileSkipExcludedFromDigests` locks this. ✓
- **Frame-homing — complete.** `clone()` (result_call_frame.go:238) and `fork()` (:276)
  both carry it; I audited every other frame-construction site: fresh `&ResultCall{}` at
  objects.go:584 is stamped by `AroundFunc`; the import site (cache_persistence_import.go:159)
  is `json.Unmarshal`ed (:160) so the tag round-trips; call_request.go:35 is a nil-receiver
  fallback (correct default false); call_request_input.go:118 is input reconstruction
  (LOW edge, below). The "ProfileSkip travels with the frame" invariant holds. ✓
- **Import round-trip — real.** `json.Unmarshal([]byte(row.CallFrameJSON), frame)`
  (cache_persistence_import.go:160) + the JSON tag means *new* persisted reflection
  frames re-import as skipped. This dissolves my round-2 open-item-(a) more cleanly than
  "recompute at import." (Old pre-fix persisted data has no `profileSkip` key → default
  false → a bounded, transitional volume edge until re-persisted — LOW.)
- **Gating — all sites correct and dangle-safe by construction.**
  - Singleflight: `call_exec`/native `execOp` gate on `req.ResultCall.ProfileSkip`
    (cache.go:3768/3783); `oc.profSkip` snapshots it at claim under `callsMu` (:3808);
    both waits gate on `oc.profSkip` (the **target** flag, :3995/:4013). Span and waits
    keyed on the **same** flag ⇒ present-or-absent together ⇒ no dangle. The N1 outer
    `OpKindCall` is gated in the early return (:3610), and skipped sub-calls re-home (nil
    profOp + parent ctx).
  - Lazy: both the op and every wait gate on the **producer** frame
    (`frameProfileSkip(resultCall)` / `shared.profileSkip()`, cache.go:3036/2995/3006/
    3038/3057/3138) — N3 done right, with an explicit load-bearing comment warning
    against "simplifying" to the waiter's bit. Producer-frame is set at result creation
    and read at force time (creation precedes force), so leader and joiner read the same
    flag. ✓
  - **Distinct-from-invalid PRESERVED** (cache.go:4013 comment + code): the gate is on
    the `profSkip` bool, never on `execSpanCtx.IsValid()`, so a *non-skipped* target with
    a genuinely invalid span still emits the targetless wait → `UnresolvedWaitTargets` →
    gate fails loud. ✓
- **Reflection-set completeness — independently audited.** Every reflection Go type's
  `Type().NamedType` (core/typedef.go) is in `reflectionTypeNames`: Function/FunctionArg/
  TypeDef/ObjectTypeDef/FieldTypeDef/InterfaceTypeDef/ScalarTypeDef/ListTypeDef/
  InputTypeDef/EnumTypeDef all match their Go name; **`EnumMemberTypeDef` → schema
  `"EnumValueTypeDef"` is the only mismatch, and it's handled** (both names listed). No
  other Go≠schema holes exist. `FunctionCall` (returnValue/returnError = real work) is
  correctly **excluded** (name-trap comment + test). ✓
- **Over-cut — supported.** No reflection-type field returns Container/Directory/File or
  does exec/`asModule`/evaluate (grep + the field set is all metadata accessors/builders/
  `__internal`); module-load (`Query.moduleSource`/`ModuleSource.asModule`/SDK exec) is
  on non-reflection receivers → stays profiled. ✓
- **UI-isolation — realized (my round-2 call).** `introspectionInfo` is behavior-
  preserved: the only change is refactoring its root-list `switch` into the shared
  `introspectionRootFields` map (same set); the debug-gated receiver-type switch is
  untouched. `profileSkip` is a separate function. So normal `dag.call` emission is
  unchanged **by construction**, and the intended profiler ⊋ UI divergence holds
  (directly-called accessor keeps its `dag.call`, profiler skips its `call_exec`). ✓
- **Call-site stamp — realized (my round-2 call).** `ReceiverTypeName =
  r.class.inner.Type().Name()` at objects.go:602 (lookup-free), read by `AroundFunc`
  (telemetry.go:40) — no per-call `egraphMu` receiver resolution. Resolves D and removes
  the eviction/determinism caveat. ✓
- **Zero loader/replay change** — `git diff --name-only` touches no `wcanalyze`/`wcotel`. ✓

## The deviation (frame field) — assessed, and better than v2

Putting `ProfileSkip` on the `ResultCall` frame (vs v2's `CallRequest.SkipProfile` +
`sharedResult.profSkip`) **dissolves N2 entirely**: the flag travels with the frame
through clone/fork/persistence, so adopted/copied/derived/imported results carry the
producer's decision with **no per-site provenance audit** (the v2 risk surface). Combined
with digest-exclusion (verified) and the JSON round-trip, it's simpler and closes the
import gap. The new dependency it introduces — "all frame copy paths carry it" — I
audited and it holds. Good call by the implementer; I endorse it.

## Findings, severity-ranked (real vs noise)

**No blockers / no correctness bugs.**

**MEDIUM — recommend before merge (regression guards for load-bearing behavior the code
gets right but doesn't lock down):**
1. **No deterministic distinct-from-invalid *emit* test.** v2 §8.5 called for it: a
   *non-skipped* target with an artificially invalid span must still emit a gate-
   observable targetless wait. The code is correct (gate on `profSkip`, not validity), and
   `TestProfileSkipGatesSingleflightEmit` covers the *skipped*-no-wait side, but nothing
   guards against a future "simplify the gate to `execSpanCtx.IsValid()`" that would
   silently swallow real mixed-recording loss. Add the positive case.
2. **Lazy path is the least-validated.** The emit tests
   (cache_profileskip_emit_test.go) cover singleflight only; the **lazy** N3 path (the
   subtlest — cross-recipe forcer≠producer, where a wrong gate dangles) has only a unit
   assertion that `sharedResult.profileSkip()` reads the producer frame, not an emit-path
   test (skipped producer ⇒ 0 lazy op/waits, gate 0/0; the §4.4 forcer-wait-loss). The
   §9 summary doesn't clearly confirm a lazy/service **live** capture passed the gate
   (it foregrounds module-load + the "adopted/imported not separately LIVE-captured"
   caveat). The code is correct by review, but I'd want either a deterministic lazy
   emit-path test **or** a confirmed live lazy/service gate-0/0 before relying on it.

**LOW — note/accept (bounded volume edges, never dangles):**
- **`fork()` inheritance reverse case:** a *non-reflection* producer forking
  *reflection-type lazy* elements (e.g. a `[ObjectTypeDef]` from a non-reflection
  receiver) inherits `false`, so those elements' lazy ops would be profiled. Narrow,
  volume-only, self-consistent. The §9.4 residual check should catch it if material.
- **Input-reconstructed frames** (call_request_input.go:118) default `ProfileSkip=false`
  — if a reflection call is passed as an argument and that frame becomes a producer, it's
  profiled (volume edge, not dangle). Rare; note.
- **Old persisted data** (pre-fix JSON, no `profileSkip` key) imports as `false` →
  transitional volume edge until re-persisted. Bounded; acceptable.
- **`dag.call=1706` is uncalibrated** (no same-workload `main` baseline; main forensics
  showed ~820–853). "UI untouched" is nonetheless sound — proven *by construction*
  (`introspectionInfo` behavior-preserved) + the divergence=0 check, not by the count.
- **BSP `DroppedSpans` proxied** (not directly instrumented) — corroborated by gate-0/0 +
  native `dropped_events=0`, acceptable.
- **telemetry-off + native-on dev corner** — `SkipProfile` stays false, native
  re-profiles introspection; dev-only, self-consistent. Not a merge concern.

## Answers to the eight charges

1. **Frame-homing:** correct — clone+fork copy it (only in-process copy paths; other
   fresh constructions are stamped or JSON-imported); digest-EXCLUDED (verified
   field-explicit hashing + full grep); JSON round-trips. ✓
2. **Gating completeness:** every recording path carries the flag — `AroundFunc` stamps
   the frame, which travels (incl. nth-element via `fork`, so `LoadNthValue`-style
   derivations inherit it); lazy gates the producer frame; N1 outer call gated. The one
   residual dependency (a recording path that bypasses `AroundFunc`) is backstopped by
   §9.2 zero-residual. ✓
3. **UI-isolation:** `introspectionInfo` behavior-preserved (root-list refactor only);
   stamp uses the right receiver type; divergence=0. ✓
4. **Reflection-set completeness:** audited all 11 schema names — `EnumValueTypeDef` is
   the lone mismatch and is handled; no others; `FunctionCall` correctly excluded; no
   reflection field forces real/lazy work. ✓
5. **Distinct-from-invalid:** preserved in code (cache.go:4013) — but add the emit test
   (MEDIUM #1).
6. **Tests:** strong on classifier/name-trap/EnumValue/debug-independence/stamp-order/
   digest-exclusion/JSON/clone-fork/singleflight-joiner-collapse (`UnresolvedWaitTargets==0`).
   Gaps: distinct-from-invalid emit, lazy emit (MEDIUM #1/#2).
7. **Zero loader/replay + canonical alignment:** confirmed; no `wcanalyze`/`wcotel`
   touched; consistent with the rational-function principle and invariants. ✓
8. **§9 + caveats:** the hard merge gates (structural 0/0, zero-residual, divergence=0,
   native 0-reflection + dropped_events=0 + loads in wcanalyze) are the right criteria and
   pass. The caveats (no main baseline, BSP proxied, adopted/imported + lazy not
   separately live-captured) are acceptable **because** the un-captured paths are covered
   by code audit + unit tests — except the **lazy** path, which I'd back with MEDIUM #2.

## Bottom line

**Landable.** The implementation is correct, faithful, and the frame-field deviation is a
real improvement (dissolves N2 + the import gap, with digest-exclusion verified). I found
no bugs. Before merge I recommend the two MEDIUM tests — the distinct-from-invalid emit
guard and a deterministic lazy emit-path test (or a confirmed live lazy/service gate-0/0)
— because the lazy cross-recipe path is the subtlest and is currently validated only by
code review. The LOW items are bounded volume edges, never dangles, and acceptable. UI-
isolation and the call-site stamp (my round-2 calls) are correctly realized, and there is
no conflict with the canonical design or the no-inference principle.
