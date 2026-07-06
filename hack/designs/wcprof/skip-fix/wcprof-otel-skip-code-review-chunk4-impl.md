# Code review — skip fix `c18b17fc53` (merge gate) — chunk4 implementer

Reviewer: chunk4 implementer. Reviewed the actual diff + resulting files against
`4585bf413d..c18b17fc53` (not pushed). Biased to finding real issues; verified
every claim against code (file:line in the coder worktree). I proposed the frame-
homing in round 2 — I verified it's realized correctly rather than assuming.

## Verdict

**Landable in substance — the implementation is correct, and notably better than
the design in one spot. Two design-planned, load-bearing unit tests are MISSING
(lazy path; distinct-from-invalid); add them before merge.** Nothing else rises to
a blocker. Severity-ranked issues below; the bulk of the change verified clean.

---

## Verified correct (the load-bearing claims actually hold)

- **[item 6] Zero loader/replay change — CONFIRMED.** `git diff --name-only`
  touches no `wcanalyze`/`wcotel` file. The no-inference principle is preserved
  structurally: the engine emits a smaller graph; the analysis is untouched.
- **Frame-homing (my round-2 proposal) — realized correctly.**
  - `ProfileSkip bool json:"profileSkip,omitempty"` on `ResultCall`
    (`result_call_frame.go:202`), copied in **both** `clone()` (`:238`) and
    `fork()` (`:273`). Those are the only two frame-copy primitives, so the bit
    travels everywhere the recipe does — the ~10-site provenance audit from v2 is
    genuinely dissolved (zero `initCompletedResult` changes needed).
  - **Digest-excluded — verified, not just claimed.** No digest path
    JSON/proto-marshals the frame (grep: no `json.Marshal`/`proto.Marshal` in
    `result_call_frame.go`), so the JSON tag cannot leak into a digest. The
    exclusion test (`result_call_frame_profileskip_test.go:31`) asserts differing
    `ProfileSkip` → identical `recipeDigest`/`contentPreferredDigest`/`selfDigest`.
    `callPB`/`recipeID` are untested but structurally safe (the `callpbv1` wire
    schema has no such field). This matters: a digest leak would silently break
    singleflight sharing and cache identity.
  - **JSON round-trip closes the import gap** (`:58`) — `true` persists/restores,
    `false` omits. The round-2 open item (a) is resolved the clean way.
- **Predicate (`core/telemetry.go`).** `profileSkip(receiverTypeName, field)`
  (`:449`): reflection-type set ∪ Query-root set, debug-independent, **separate**
  from `introspectionInfo` (which is left byte-for-byte unchanged except the
  shared `introspectionRootFields` extraction — UI behavior preserved). Stamped on
  `req.ResultCall.ProfileSkip` in `AroundFunc` **before** the `IsSkipped` early
  return (`:40`), so inherited-skip descendants are classified by their own recipe.
- **Schema-name audit — COMPLETE.** I checked every reflection type's `Type()`
  `NamedType` in `core/typedef.go`: all 11 match the listed names **except**
  `EnumMemberTypeDef` → schema name `"EnumValueTypeDef"` (`:2171`, legacy) — which
  the implementer caught (the §9 catch) and lists **both** ways. No other legacy
  mismatch exists. This was the highest-risk completeness item; it's clean.
- **Name trap — handled and tested.** `FunctionCall` (`returnValue`/`returnError`
  are real `DoNotCache` work), `SourceMap`, `FunctionCallArgValue` are deliberately
  excluded (`:404` comment) and asserted *profiled* in
  `TestProfileSkipClassifier` (`telemetry_skip_test.go:52`). Exactly the task's
  `FunctionCall.returnValue` concern, covered.
- **The `ReceiverTypeName` call-site stamp is better than the design.** Instead of
  the design's "one immediate `ReceiverCall` hop" (an `egraphMu.RLock`), the
  receiver type is stamped *lookup-free* at `objects.go:602`
  (`r.class.inner.Type().Name()` — `r` IS the receiver), carried on
  `CallRequest.ReceiverTypeName`, and read in `AroundFunc`. This dissolves the
  lock-safety concern AND my round-2 memo-key nit entirely — no resolution, no
  memo. Clean.
- **Gating completeness — all sites present and consistent.** N1 outer
  `OpKindCall` (`cache.go:3605`, via the existing nil-profOp early return — no
  nil-deref), native `execOp` (`:3768`), OTel `call_exec` (`:3783`), `oc.profSkip`
  snapshot (`:3808`), singleflight native+OTel waits (`:3995`/`:4013`); lazy
  joiner (`:2995`/`:3006`), lazy native op/OTel span/leader wait
  (`:3038`/`:3057`/`:3138`). `publishResult` (native+OTel) follows for free
  (`profOpID==0` / `execSpanCtx` invalid) — correctly untouched.
- **Lazy leader/joiner read the SAME frame — verified.** Joiner:
  `shared.profileSkip()` = `frameProfileSkip(shared.loadResultCall())` (`:2990`).
  Leader: `resultCall := shared.loadResultCall()` (`:3015`) then
  `frameProfileSkip(resultCall)` (`:3038`). Same frame ⇒ leader and joiner gate
  identically. The N3 cross-recipe target-flag gating is implemented right, and the
  comments (`:2985`, `:3032`) correctly mark it load-bearing and warn against
  "simplifying" to the waiter's bit.
- **nil-safety — holds.** `(*Op).ID()` (`record.go:125`) and `(*Wait).End()`
  (`:247`) both nil-guard, so every new `var profWait; if !skip {…}; profWait.End()`
  is safe — and this pattern already existed for the `wcprof`-disabled path, so
  it's not a new dependency.
- **`oc.profSkip` snapshot — no race.** Set at `oc` construction (`:3808`) before
  publish under `callsMu`, never mutated; joiners read it after finding the
  published `oc`. Since the cut is static, the snapshot equals the joiner's own
  bit; gating on the target keeps it dangle-proof.

### Non-issue I specifically chased down (so the council doesn't re-raise it)

**nth-element `fork()` inherits `ProfileSkip` instead of recomputing — NOT a bug.**
`fork()` is only reached from `NthValue` (`cache.go:2371`), which calls
`GetOrInitCall` **directly** (`:2390`), bypassing `objects.go`/`AroundFunc` — so
the nth-element frame's `ProfileSkip` is *always* the inherited value, never
recomputed. It is therefore deterministic per cache key (§4.2 holds), and
inheritance is the *correct* rule (a reflection list's elements are metadata, so a
recompute against the list receiver would wrongly profile them — the `fork()`
comment at `:269` reasons this out correctly). I verified both branches.

---

## Real issues (severity-ranked)

### MEDIUM — 1. The lazy path has NO unit test (top priority to add)

The lazy gating (N3) is the subtlest correctness argument in the whole design —
the one place the "shared-recipe" agreement does *not* hold, closed instead by
producer-flag gating. The three new test files cover the classifier, the frame,
and **singleflight** only (`cache_profileskip_emit_test.go` has two tests, both
singleflight). There is **no** deterministic test for: skipped producer → no
`lazy` op/waits; kept producer → resolved; **non-skipped forcer on a skipped
producer → wait dropped (the §4.4 named loss), 0 unresolved**. The design's own
§8 planned it. Today it's covered only by a live lazy/service capture that the
§9 caveats admit is not separately reproduced for adopted/imported paths — i.e.
the *subtlest* invariant has the *weakest* regression protection. The code is
correct as written (I verified it), so this is a coverage/regression-guard gap,
not a correctness unknown — but for a merge gate on the trickiest path it should
be closed. The singleflight tests are an excellent template (they drive the real
cache + real loader + assert `0/0`); mirror them for lazy.

### MEDIUM — 2. No distinct-from-invalid regression test

Design §8 test #5 (load-bearing) is absent. The property — a *non-skipped* target
with an invalid span still emits a targetless wait so the gate fails loud on
genuine loss — holds **by construction** here (the gate is on the `profSkip`
bool, not on `execSpanCtx.IsValid()`; `EmitOTelWait` is still called for
non-skipped targets, `cache.go:4013`). But this is exactly the invariant a future
refactor could silently break (e.g. someone "tidies" `if !oc.profSkip` into
`if oc.execSpanCtx.IsValid()`), blinding the structural gate to real capture loss.
A 15-line guard (skipped vs invalid-but-kept, assert `UnresolvedWaitTargets>0` for
the latter) protects the property the entire forensic effort depends on. Add it.

### LOW — 3. Digest-exclusion test covers 3 of the 5 claimed paths

The field comment claims exclusion from `recipeDigest`/`contentPreferredDigest`/
`selfDigestAndInputRefs`/`callPB`/`recipeID`; the test covers the first three.
`callPB`/`recipeID` are structurally safe (no whole-frame marshal; proto schema
has no such field), but cache identity is load-bearing enough that two more
assertions are cheap insurance.

### LOW — 4. §9 caveats (implementer-acknowledged) — acceptable, with one rider

- *No side-by-side `main` baseline* → the volume claim is absolute (10456;
  1646+1646 pair), not differential. Fine given the **zero-residual-introspection**
  check + `0/0` gate are the real proofs, not the raw count.
- *BSP `DroppedSpans` proxied, not directly read* → the decisive "amplifier
  removed ⇒ loss closed" proof is indirect. Acceptable since the gate is `0/0` on a
  fresh capture, but state it's a proxy.
- *Adopted/imported not separately LIVE-captured* → mitigated deterministically by
  the frame-travel + JSON round-trip unit tests (the bit can't go stale), so this
  is well-covered *if* the lazy unit test (issue 1) lands, since lazy is where an
  adopted producer's flag is actually read.
- *telemetry-off + native-on dev corner* (`SkipProfile` false → native
  re-profiles introspection): genuinely dev-only and self-consistent (no dangle).
  Flag, don't block.

---

## Noise (explicitly NOT issues — separated so they don't get re-litigated)

- `profWait.End()` on a nil `profWait` — safe (`Wait.End()` nil-guards; pre-existing
  pattern).
- `fork()` inherit-vs-recompute "divergence" — deterministic and correct (above).
- Volume not returning to `main` — expected and honest (the legitimate per-real-miss
  pair remains; out of scope).
- `Query.module`/`Query.moduleSource` "look introspection-ish" — they're NOT in
  the root set, so they stay profiled; real module loading is correctly kept.

## Bottom line

The cut is correct, the frame-homing is clean, the predicate + schema-name +
name-trap audits are complete, gating is consistent across singleflight + lazy +
native + OTel, and there is zero analysis-side change. The `ReceiverTypeName`
call-site trick is a real improvement over the approved design. **Merge bar: add
the lazy-path emit test (issue 1) and the distinct-from-invalid guard (issue 2) —
both design-planned, both guarding the subtlest invariants — then land.** Issues 3
and 4 are nice-to-haves. I'd approve once 1 and 2 are in.
