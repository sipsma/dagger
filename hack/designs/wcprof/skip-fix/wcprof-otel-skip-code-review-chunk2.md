# Skip fix — code review (merge gate), Chunk 2 implementer

Reviewed commit `c18b17fc53` in the `…-skip-coder-daa3a9d2` worktree against `4585bf413d`. Read the
full diff + the resulting files + the digest/persistence/construction paths. File:line are that
worktree. I built the emit and proposed the frame-homing; I verified the code realizes both.

## Verdict: **LANDABLE.** One MED gap (no lazy-path unit test) I'd close before/with merge; everything else is LOW/cosmetic or an acceptable caveat.

The implementation faithfully realizes the v2 design + the four round-2 foldings. The load-bearing
claims I most doubted — **digest exclusion**, **frame-homing dissolving the N2 provenance audit**, and
**distinct-from-invalid survival** — all check out in code, not just in prose. The governing principle
holds: the diff touches **no** `wcanalyze`/`wcotel` (verified `git diff --name-only`), so loader/replay
are untouched and the engine just emits a smaller, self-consistent graph.

## What I verified correct (grounded)

**1. Zero loader/replay change** — diff is 8 files, all `core/`+`dagql/`; no wcanalyze/wcotel. ✓

**2. Frame-homing + digest exclusion (the critical one).** `ProfileSkip` (`result_call_frame.go:202`,
`json:"profileSkip,omitempty"`) is referenced in exactly four places: the field, `clone()` (`:238`),
`fork()` (`:276`), and the comment — and in **no digest method**. `recipeDigestWithVisiting` (`:632`)
hashes Receiver/Type/field/Args field-by-field via `hashutil` (no whole-struct marshal), and there is
no `json.Marshal`/`MarshalJSON` of the frame in any digest path. So `ProfileSkip` cannot enter
`callKey`/`callDigest`/`concurrencyKey`; same-recipe frames still dedup; no cache invalidation.
`TestResultCallProfileSkipExcludedFromDigests` locks this against all three digests. ✓ This is the
linchpin and it holds.

**3. clone()/fork() are the ONLY copy methods** (grep: no other `clone/fork/dup/copy/normalize`), both
copy `ProfileSkip`, and the round-2 "missed" provenance sites are genuinely moot now:
- `cache_egraph.go:1485` (index-wait backfill) and `cache.go:1975/2568` (normalize/fork-copy) all clone
  or fork a *stamped* source frame → carry the bit.
- `cache.go:2356` (LoadNthValue) forks → inherits; its `getOrInitCall` (`cache.go:2390`) is the one
  non-`objects.go` emit caller and it carries the inherited bit. Inheritance is **correct** here: the
  element's own receiver becomes the list (non-reflection), so *recomputing* would wrongly profile a
  reflection-list walk; inheriting the producer's skip is right.
- **Import** (`cache_persistence_import.go:160`) `json.Unmarshal`s the frame → restores `ProfileSkip`
  for new persisted data (old pre-fix rows → default-false = bounded volume edge, never a dangle).
So frame-homing does what I proposed in round 2 — provenance travels with the thing it describes.

**4. Reflection-set is complete; the schema-name audit is clean.** I checked every reflection Go type's
`Type().Name()` in `core/typedef.go`: 10 of 11 match their Go names; the **only** mismatch is
`EnumMemberTypeDef`→`"EnumValueTypeDef"` (`:2171`), and the set lists both (`telemetry.go:419-420`).
`FunctionCall`→`"FunctionCall"` (`:2420`, real work), `FunctionCallArgValue`, and `SourceMap` are
correctly **excluded** (name traps). No other under-cut. `TestProfileSkipClassifier` asserts the
`EnumValueTypeDef` skip and the `FunctionCall` non-skip.

**5. Gating is complete and gates on the TARGET flag everywhere.** N1 outer `OpKindCall`
(`cache.go:3602`, `|| req.ResultCall.ProfileSkip`, nil-profOp-safe), native execOp (`:3768`), OTel
call_exec (`:3783`), `oc.profSkip` snapshot **under callsMu in the struct literal before publish**
(`:3808` — Invariant-T for the flag, same as `execSpanCtx`), singleflight native+OTel waits
(`:3995`/`:4013`, on `oc.profSkip`), lazy joiner native+OTel (`:2995`/`:3006`, on
`shared.profileSkip()`), lazy op/span/leader-wait (`:3038`/`:3057`/`:3138`, on
`frameProfileSkip(resultCall)`). The lazy comments correctly call out *producer*-flag gating as
load-bearing (forcer ≠ producer recipe). ✓

**6. Distinct-from-invalid preserved.** The OTel singleflight wait gates on `oc.profSkip`, **not**
target validity (`:4013`): `profSkip=false` + invalid `execSpanCtx` (genuine mixed/untraced recording)
still calls `EmitOTelWait` → targetless link → `UnresolvedWaitTargets` → gate fails loud. The
detector I built is intact; the skip gate didn't blind it. ✓

**7. Dangle-proof under inconsistent stamping.** Even where a frame is *unstamped* (ID-reconstruction
`server.go:1529/1948`, telemetry-off+native-on) or *fork-inherited*, the worst case is a bounded
**volume** under/over-cut, never a dangle — because every wait gates on the target's stored flag, so
target-absent ⇒ wait-absent on both sides. §9's zero-residual-introspection is the empirical backstop.
I confirmed `(*wcprof.Wait).End()` is nil-safe (`record.go:248-250`) and `(*Op).ID()==0` (round 1), so
the new nil-`profWait` paths don't panic; `go build ./dagql/... ./core/` is clean.

## Real issues (severity-ranked)

**[MED] No lazy-path unit test — the load-bearing N3 path has zero fast coverage.** The new
`cache_profileskip_emit_test.go` has exactly two tests, both singleflight. The lazy gating
(`evaluateOne`, producer-flag) is the *subtlest* path (forcer ≠ producer recipe — the whole reason
N3 exists) and the round-2 test plan's #9 (skipped producer → no lazy op/waits, `0/0`; **non-skipped
forcer on a skipped-producer value → wait dropped, no dangle**) is unimplemented. The code is verified
correct and §9's live capture reports "zero residual …/lazy," but it's unclear a *lazy-heavy* workload
was run, and a silent regression here (e.g. a future "simplify to the waiter's bit") would reopen a
cross-recipe dangle. **Add a lazy emit-path test before/with merge** — it mirrors the existing
singleflight test and is cheap. This is my one real ask.

**[LOW] No dedicated distinct-from-invalid test.** Round-2 test #5. It's *transitively* covered (the
existing `EmitOTelWait`/gate tests run with `profSkip=false` default, so the invalid-target path still
fires), so not a true gap — but a one-liner asserting `profSkip=false` + invalid target ⇒
`UnresolvedWaitTargets>0` would lock the "skip gate didn't swallow the detector" guarantee against
future edits.

**[LOW] fork() inheritance comment is imprecise.** `result_call_frame.go:272-275` says `ProfileSkip`
"is a pure function of the recipe" while the rule is actually *inherit the producer's decision, do not
recompute on the element's own receiver*. The behavior is correct (and the same comment then explains
*why* recomputing would be wrong), but "pure function of the recipe" is not literally true for forked
frames (where `ProfileSkip ≠ profileSkip(own receiver, field)`). Tighten the wording so a future reader
doesn't "fix" fork() to recompute. Cosmetic.

**[NOTE] Bounded-volume edges (acknowledged, none block):** old pre-fix persisted rows import
`ProfileSkip=false`; ID-reconstruction and telemetry-off+native-on frames are unstamped. All are
bounded volume, never dangle, dev-or-legacy-only, with §9 zero-residual as the live backstop. Fine.

## §9 caveats — acceptable for merge

- **No side-by-side `main` baseline → acceptable.** The fix's success criterion is "the amplifier is
  gone and the loss is closed," and that is shown *directly*: zero residual reflection call_exec/lazy,
  structural gate `0/0` (orphaned-parents/unresolved-targets/cycles), native `dropped_events=0`, and
  sub-2048 volume. The exact main ratio is secondary (the reflection class was ~100% of the increase
  and it's now absent). The residual 10456 (1646 call_exec + 1646 publishResult real-miss pairs) is the
  legitimate always-on second-source cost — a separate scaling question, correctly scoped out.
- **BSP `DroppedSpans` not directly instrumented → acceptable.** Strongly proxied (orphaned-parents=0
  *is* "no dropped parent spans," + native dropped_events=0 + sub-queue volume). Direct instrumentation
  is cheap future hardening, not a blocker.
- **Adopted/imported not separately live-captured → acceptable.** Covered by-construction (frame-homing:
  import JSON-unmarshals the bit, adopt keeps the canonical frame's bit) + unit tests
  (clone/fork/JSON round-trip). I verified both construction paths in code.

## Bottom line

Land it. The frame-homing design is realized correctly (digest-excluded, copied on the only two copy
paths, round-trips through JSON, dissolves the N2 audit), gating is complete and dangle-proof via
target-flag reads, the reflection set is complete (schema-name audit clean), distinct-from-invalid
survives, and the principle (zero loader/replay) holds. **The one thing I'd gate merge on is adding the
lazy-path emit test** (MED) so the load-bearing N3 path has a fast regression guard; the LOW items
(distinct-from-invalid test, fork() comment) are nice-to-haves, and the §9 caveats are acceptable.
