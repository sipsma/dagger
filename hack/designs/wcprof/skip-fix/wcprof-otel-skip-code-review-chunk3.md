# Code review (merge gate) — skip-fix commit `c18b17fc53` — Chunk 3 implementer (lazy / wcprof.parent owner)

**Reviewed:** the actual diff `4585bf413d..c18b17fc53` + resulting files in the coder
worktree. Bias to real issues. file:line are the coder worktree. Review only — did not modify
the branch.

## Verdict

**Landable after one test addition (lazy emit). No correctness blockers found.** The
implementation is high quality: it correctly realizes the v2 design + all four round-2
foldings, and I could not break it on the lazy path, the digest path, the reflection set, or
the singleflight collapse. The one real gap is **test coverage of the lazy path** (my domain,
the subtlest logic, explicitly listed in the mandate) — it has zero unit/emit coverage and a
"do NOT simplify this" fragility comment, so it should be pinned before merge. A
distinct-from-invalid test is the secondary gap.

Severity-ranked findings below; most of the eight review areas pass clean.

---

## What I verified PASS (with evidence)

**1. Frame-homing correctness — PASS.** `ProfileSkip` is copied in **both** copy chokepoints —
`clone()` (result_call_frame.go:238) and `fork()` (:276) — and those are the only two
frame-copy paths (audited every `storeResultCall`/`&sharedResult` site: the rest receive a
clone/fork or are lookup-only). **Digest-excluded — verified, not just claimed:** `callPB`
(result_call_frame.go:425) enumerates explicit fields and `ProfileSkip` is not among them, and
the field appears in **no** digest function (grep: only struct def, clone, fork, cache.go
gates). The test pins it directly (`TestResultCallProfileSkipExcludedFromDigests` covers
recipe + content-preferred + self digests). **JSON round-trips** (`json:"profileSkip,omitempty"`,
tested) — which is what actually closes the round-2 import edge: a persisted skipped frame
carries the bit, so warm/imported results don't re-leak (open-item-a resolved by persistence,
not just default-false).

**2a. N1 (outer native `OpKindCall`) — PASS.** cache.go:3610 adds `|| req.ResultCall.ProfileSkip`
to the early return that already passes a **nil** profOp to the inner (nil-safe) — exactly the
clean shape. ✓

**2b. Gating completeness — PASS.** Every gate reads the **target/producer** frame flag, never
a waiter's own bit: native execOp (:3768), OTel call_exec (:3783), `oc.profSkip` snapshot at
claim under callsMu before publish (:3808, Invariant T preserved), singleflight native+OTel
waits on `oc.profSkip` (:3995/:4013). **No unstamped profiling path:** `ProfileSkip` is set in
`AroundFunc` (telemetry.go:40) which runs at objects.go:656 *before* `GetOrInitCall` at :682
on the **same** req; the other req sites inherit it — nth-element via `parentCall.fork()`
(cache.go:2370), attach/normalize via `frame.clone()` (:1990) — and server.go:1463 is
**lookup-only** (no profiling emit; misses fall through to the stamped Select path). The §9
"native dump 0 reflection-class" is the empirical backstop. ✓

**2c. Lazy path (my domain) — PASS, correct in every interleaving I checked.** Lazy gates on
the **producer** frame's flag (`shared.profileSkip()` / `frameProfileSkip(resultCall)`,
cache.go:1567/3036) — the N3 fix, with the load-bearing comment intact (cache.go:2986-2992).
I traced the dangle conditions: a dangle needs `producerSkip==false` (joiner emits a wait)
**and** an invalid lazy target. The only way the leader leaves an invalid target while
`producerSkip==false` is the genuine untraced-leader (mixed-recording) case — which *should*
emit a targetless wait and fail the gate loud (distinct-from-invalid, preserved). A
*skip*-induced invalid target always implies `producerSkip==true` (recipe-stable flag), so the
joiner emits no wait → **no skip-induced dangle.** The `wcprof.parent` override is only set
when the producer is kept (`beginOTelLazyOp` gated at :3057), so it can never point into the
skipped set (§4.3 holds). The reset-per-attempt `lazyEvalSpanCtx`/`lazyEvalProfOpID` (:3030)
is untouched and still gates the mixed-recording detector. ✓

**3. Reflection-set completeness — PASS, audited.** I checked every reflection Go type's
**schema** name in core/typedef.go: all 10 match their Go name, and the only mismatch is
`EnumMemberTypeDef` → `"EnumValueTypeDef"` (typedef.go:2171) — the legacy name the implementer
caught, and it is in the set (plus the defensive Go name). `ReceiverTypeName` carries the
**schema** name (`r.class.inner.Type().Name()`, objects.go:602), so the live name is what's
matched. **No other mismatch exists.** The **name traps are correctly excluded**: `FunctionCall`
(typedef.go:2420, real DoNotCache work), `FunctionCallArgValue`, `TypeDefKind`,
`FunctionCachePolicy`, `SourceMap` are all absent from `reflectionTypeNames`, and the test
asserts `FunctionCall.returnValue/returnError` stay profiled.

**4. Distinct-from-invalid preserved — PASS (in code).** Both wait gates emit on `!profSkip`
**not** on target validity (cache.go:4010-4015 comment + the lazy joiner :3003-3007), so a
non-skipped target with an invalid span context still emits a targetless wait →
`UnresolvedWaitTargets` → loud gate failure. The skip change did not blind the mixed-recording
detector. (But it is not *tested* — see Gap-2.)

**6. Zero loader/replay change — PASS.** `git diff --name-only` touches no `wcanalyze`/`wcotel`
file; the 8 changed files are predicate + plumbing + gates + tests only. ✓

**8. No panic / race bug.** `(*wcprof.Wait).End()` is nil-safe (record.go:247 `if w == nil
{ return }`), so the new conditional-nil-`profWait` + unconditional-`.End()` pattern is safe in
all three sites. `oc.profSkip` is snapshotted at claim under callsMu (consistent for joiners).
**Performance: the round-2 H3 lock concern is resolved** — `ReceiverTypeName` is stamped
lookup-free at objects.go:602 (`r.class.inner.Type().Name()`), so `profileSkip` is two map
lookups with **no `egraphMu`** per call (better than the design's memoization plan).

**Round-2 foldings all addressed:** R1 frame-homing (deviation 1) ✓; R2/H2 oracle asymmetry —
acknowledged in the `profileSkip` doc-comment and measured (§9 "refinement-3 divergence=0") ✓;
H3 perf ✓; open-item-a import ✓ (JSON persistence); open-item-b over-cut → §9.4 + name-traps ✓.

---

## Real issues (severity-ranked)

### MED-HIGH — Gap-1: the lazy emit path has NO unit/emit test
`cache_profileskip_emit_test.go` covers only **singleflight** (gate-emit + the skipped
claimer/joiner collapse — both good). The **lazy path has zero unit coverage**, despite being
the subtlest logic in the change, my N3 catch, three gated sites, and carrying an explicit
"do NOT simplify this to the waiter's bit — that reopens a cross-recipe dangle" comment
(cache.go:2986). Only the coarse §9 live capture is claimed to exercise it. The mandate lists
"the LAZY variant" as a required test. The code is correct (I verified it), but its only guard
against a future regression is a prose comment. **Add before merge** an emit test mirroring the
singleflight one for the lazy path:
- (a) skipped producer → no `lazy` op/span, no joiner/leader waits, gate `0/0`;
- (b) kept producer → `lazy` op + resolved waits, re-pointed work carries `wcprof.parent` =
  the present lazy op;
- (c) **the cross-recipe case** — a *non-skipped forcer* forcing a *skipped-producer* lazy
  value emits no wait and the gate stays `0/0` (pins the N3 property and the §4.4 accepted loss).

### MED — Gap-2: distinct-from-invalid is not tested for the skip change
Item 5 lists it and round-2 flagged it load-bearing. The property is **preserved in code** (gates
on `profSkip`, not validity), but no test asserts that a *non-skipped* target with an
artificially invalid span still produces a gate-observable targetless wait *after* this change.
Confirm the pre-existing chunk2 mixed-recording test still exercises a non-skipped invalid
target (if so, note it; if not, add a one-liner) so a future "gate on validity instead of the
flag" refactor fails loudly.

### LOW — lazy joiner reads `producerSkip` after `lazyMu.Unlock()`
cache.go:2993 reads `shared.profileSkip()` *after* the unlock, while `lazyOpSpanCtx` is
snapshotted under the lock (:2982). I worked the interleavings: it is **benign** (the flag is
recipe-stable; the only racy transition, nil→introspection, makes the leader mint a valid op
the joiner then declines to wait on — a missing edge on a present op, never a dangle). Reading
`producerSkip` under `lazyMu` alongside `lazyOpSpanCtx` would make it a single consistent
snapshot and remove the need to reason about this at all. Optional, not blocking.

### LOW / NOTE — over-cut rests on a schema-semantics assumption
"No reflection-type field forces real/lazy work" is verified by spot-check (Function/FunctionArg
fields are all metadata, module.go:418-490) + the name-trap exclusions + §9 (0 reflection-class,
user `processRun` self-time unchanged). It is sound today, but it is a *schema* property that a
future field added to a reflection type could violate (silent coarsening of real work — not a
dangle). Make the §9.4 over-cut capture a **recurring documented merge gate**, not a one-time
check (round-2 open-item-b). Acceptable to land now.

---

## §9 adequacy + caveats — acceptable for merge

Gate `0/0` (orphans/unresolved), zero residual introspection, divergence=0, and especially
**native dump 0 reflection-class + dropped_events=0** are the decisive evidence the amplifier is
gone and the cut is complete and self-consistent. The caveats are all **volume-only or dev-only,
never dangle**: no side-by-side `main` baseline (fine — the design's bar is "no amplifier," not
"= main"; the 1646 `call_exec`+1646 `publishResult` are the legitimate per-miss pairs); BSP
`DroppedSpans` proxied (the `dropped_events=0` + `0/0` gate corroborate); adopted/imported not
separately LIVE-captured (the JSON round-trip closes the import edge structurally — a targeted
adopted/imported-lazy capture would strengthen but isn't blocking); telemetry-off+native-on dev
corner (ProfileSkip unstamped → native profiles introspection; self-consistent, dev-only,
honestly flagged). All acceptable.

## Bottom line

**No correctness blocker; land after adding the lazy emit test (Gap-1), ideally the
distinct-from-invalid test (Gap-2).** The deviations are improvements (frame-homing is exactly
my round-2 R1; the `EnumValueTypeDef` 12th type is a necessary catch). Digest-exclusion,
nil-safety, reflection completeness, the lazy producer-flag gating, N1, and zero loader/replay
change all check out against the code.

**(Carried, separate):** service.start §3.4 self-erasure re-root still owed in both sources.
