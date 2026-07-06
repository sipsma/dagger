# Round-2 review of `wcprof-otel-skip-impl-plan.md` (v2) — chunk4 implementer

Reviewer: chunk4 implementer. Verified every v2 change + did a fresh holistic
pass against current branch code (file:line this worktree, @ `4585bf413d`). Two
jobs: (1) did v2 correctly incorporate round 1; (2) anything missed now that the
design is concrete.

## Verdict

**Approve to implement. No new correctness hole in the cut.** v2 correctly
absorbed round 1 (predicate flip, N1, N3, distinct-flag all verified sound) and
the three pushbacks are all correct. I'd take the implementation. **One primary
holistic finding that should change the implementation:** the N2 `profSkip`-
provenance machinery is both **incomplete as audited** *and* a symptom of placing
the bit on `CallRequest` — carrying it on the **`ResultCall` frame** instead
dissolves N2 entirely and closes the import gap (open item a) for free. Plus two
smaller findings (an ineffective memo key; the §4.4 user-wait-loss needs empirical
confirmation, not assumption).

---

## Part 1 — round-1 incorporation (the changes)

- **Predicate flip — DONE and correct.** Separate, debug-independent, receiver-
  TYPE `profileSkip` in `core`, `introspectionInfo` untouched (§3). The v1 debug-
  orphan rationale is retracted with the correct loader evidence (`loader.go:251-
  254` no kind filter; `:299-310` orphan only on absent parent). Good.
  - **Bonus validation of the receiver-TYPE choice:** I spot-checked the over-cut
    risk and it cuts the *right* way — `Container.args`/`Container.withExec` (real
    exec) live on `Container`, **not** a reflection type, so they stay profiled;
    only accessors on the 11 reflection types are skipped. Keying on receiver
    *type* (not field *name*) is what dodges the name-collision trap. This is a
    real strength, not just adequacy.

- **N1 (outer native `OpKindCall`) — VERIFIED.** The gate is `cache.go:3559`
  (`if !wcprof.Enabled(ctx) || req==nil || req.ResultCall==nil`) and the op is
  minted at `:3562`. Adding `|| req.SkipProfile` takes the **existing** early-
  return that already passes `nil` profOp to the inner — so there is no nil-deref
  on the outcome block at `:3566-3578` (we never reach it). Clean, and the oracle-
  symmetry reasoning is right: without it a skipped miss keeps a native call op
  while OTel has none.

- **N3 (lazy ≠ singleflight) — CORRECT and important.** I confirmed the lazy op
  represents the *producer* recipe (`shared.loadResultCall()`), while the forcer
  waiting at `cache.go:2964`/`:2973` is a different recipe, so §4.2's "waiter
  shares the recipe" genuinely does **not** cover lazy. Re-labeling target-flag
  gating on `shared.profSkip` as **load-bearing** (not "robustness") is the right
  call. The §4.4 named user-wait-loss is honest (see holistic #3).

- **Distinct-flag (§5.5), re-homing (§4.1), `wcprof.parent` non-crossing (§4.3),
  native symmetry (§5.4) — all still sound.** No regressions from v1.

**Verdict on changes:** correctly incorporated, with one real gap in N2's audit
**completeness** (below) — which is a volume-completeness issue, not a dangle.

---

## Part 2 — the three pushbacks (all correct)

1. **`!IsRecording` short-circuit is useless in production — AGREE.** Production
   records, and the ~33k skipped descendants are *recording* (that's the bug), so
   neither `!IsRecording` nor `!IsRecording && !wcprof.Enabled` touches the hot
   path. Replacing it with the cheaper receiver-type predicate + memoization is
   right (but the memo key is wrong — holistic #2).
2. **Not fixing chunk3's parentless-`publishResult` here — AGREE, and honestly
   bounded.** It concerns *kept survivors'* parentage, which this fix never
   touches; §5.6 correctly refuses to over-claim "gate clean" (the internal-kind-
   root signal would still fire). Keep them independent.
3. **profiler-skip ⊋ UI-suppress by design — AGREE and well-stated.** The profiler
   ADDS spans; it must not SUBTRACT UI spans. A directly-called `TypeDef.asObject`
   keeping its `dag.call` span while the profiler skips it is correct.

---

## Part 3 — holistic findings (fresh pass)

### [PRIMARY] N2 is incomplete *and* avoidable: put `SkipProfile` on the `ResultCall` frame

**The audit is incomplete.** v2's invariant ("`profSkip` travels with
`resultCall`") is sound, but its enumerated site list is not. Beyond the named
`initCompletedResult` paths (`:4132/4151/4160`) and `:1799/2431/2531/3625`, there
are at least **three more** sites that set a `sharedResult`'s `resultCall`:
- `cache.go:1975` — `shared.storeResultCall(req.ResultCall)` (`attach_result_normalized`),
- `cache.go:2356` — `shared.storeResultCall(req.ResultCall.clone())` (Nth/element),
- `cache.go:2568` — `r.shared.storeResultCall(frame.fork())`.

And the audit doesn't distinguish `clone()` from **`fork()`** (`:2435`, `:2568`),
which are *different* copy primitives. So a hand-maintained parallel
`sharedResult.profSkip` has ~10 set-sites across two copy verbs — and v2 already
missed three. (Safety note: a missed site defaults `profSkip=false` ⇒ the producer
is *profiled* ⇒ self-consistent, no dangle. So this is a **volume-completeness**
gap that undercuts the §9.6 target and the "complete cut" claim, **not** a
correctness/dangle hole. Don't merge-block on it — but don't ship the manual audit
either.)

**The fix dissolves the whole problem.** `SkipProfile` is a *pure function of the
recipe* (receiver type + field) — i.e. a property of the **`ResultCall` frame**,
not of the per-request `CallRequest`. Carry it there:
- `clone()` (`result_call_frame.go:207`) and `fork()` (`:238`) are field-by-field
  literal reconstructions, so a new frame field is copied by updating **exactly
  those two** functions — the same two sites *every* frame field already depends
  on, vs the ~10 scattered `profSkip` assignments. The bit then travels with the
  frame through every `storeResultCall`/adopt/copy path **by construction** —
  including the three sites v2 missed.
- The recipe digest hashes **selected** fields (`recipeDigestWithVisiting`,
  e.g. `h.WithString(curType.NamedType)` at `:1084`), so a new struct field is
  **not** in the digest unless explicitly added — the "must not enter `callKey`"
  constraint is satisfied by default (and `SkipProfile` is anyway derivable from
  receiver+field already in the digest, so even an accidental inclusion wouldn't
  split equivalence classes — but exclude it).
- **It closes open-item (a) for free.** A `json:"skipProfile"` field round-trips
  through persistence, so imported/persisted lazy producers carry the real bit —
  no lock-unsafe "recompute at import," no default-false volume edge. (Or tag it
  `json:"-"` to deliberately accept default-false; your call — but persisting is
  strictly less work than the recompute the open item contemplates.)
- Plumbing is otherwise unchanged: still stamp in `AroundFunc` (now
  `req.ResultCall.SkipProfile = profileSkip(...)`), still outside cache locks
  (lock-safety basis intact: receiver resolution can hit `egraphMu.RLock` via
  `refCall`→`resultCallByResultID`, `:1377`, and `ResultCallRef` carries **no**
  inline receiver type, so on-frame storage does not let you compute it lock-free
  at the gate — the stamp is still required). The singleflight gate reads
  `req.ResultCall.SkipProfile`; the lazy gate reads
  `shared.loadResultCall().SkipProfile`, consistent by construction.

This is a *refinement* of v2's architecture (move the stored bit onto the thing it
must track), not a redesign. It removes §5.1's N2 rule, the import open item, and
the fork/clone footgun in one move. **Strongly recommend.**

### [MINOR] §5.7 memoization key can't save the cost it targets

Memoizing `profileSkip` by `(receiverTypeName, field)` does **not** avoid the
expensive step. The cost is resolving the receiver to get `receiverTypeName`
(`refCall` → possibly `resultCallByResultID`/`egraphMu.RLock`); once you have
`receiverTypeName`, the set-membership test is free. Keying the memo on the value
you had to do the expensive work to obtain saves nothing. Key it on a **pre-
resolution** input instead — the receiver ref's `ResultID` (inline on
`ResultCallRef`, `result_call_frame.go:63`) or the call's recipe digest — so a
cache hit skips the resolution. Minor, but the §5.7 mitigation as written is a
no-op for the actual hot cost. (Caveat to also measure: for *live* req frames the
receiver ref often carries `ref.Call`/`ref.shared` inline, so the lock fallback
may be cold and the whole concern small — but confirm in §9.9, and fix the key
before relying on the memo.)

### [GOAL-RELEVANT] §4.4 user-wait-loss: confirm "rare," don't assume it

This is the *one* place the fix touches real user work: a non-introspection forcer
that blocks on an introspection-*produced* pending value loses that wait edge
(gated by the target's flag), folding the stall into the forcer's self-time. It is
self-consistent (no dangle) and the doc names it as a decision — good. But "rare
(introspection metadata is eagerly computed, seldom pending) and cheap" is an
**empirical claim about real user wall-time**, and the governing principle says
don't assume — measure. Make §9 prove it: on the lazy/service capture, assert the
count *and* the total blocked-ns of `WaitReasonLazy` edges dropped because
`shared.profSkip` is approximately zero. If introspection-produced lazies turn out
common or long, this becomes a user-work-first-class violation (inflated forcer
self-time hiding the real cause) and argues for a coarse single-node aggregate of
the introspection subtree rather than full drop — future scope, but the
measurement is the trigger, so wire it now.

### Endorsed honesty (don't let these get "simplified" away)

- **§9.6 — volume will NOT return to main**, and the doc says so: the residual is
  the legitimate per-real-miss `call_exec`+`publishResult` pair (the always-on
  second-source doubling, ~1.9k). Removing the 33k *amplifier* is what matters for
  the BSP overflow; §9.7 (`DroppedSpans → 0`) is the decisive "actually-solves-it"
  proof, correctly empirical rather than assumed. Hold that as the merge bar.
- **§9 as the correctness centerpiece** is the right posture now that "self-
  consistent by construction" leans on plumbing completeness (N1/N2/N4 + lazy
  target-flag). With the frame-field change, N2 moves back toward by-construction
  and §9.1's adopted/imported-path assertions get easier to actually pass.
- **Over-cut (N6/§9.4)** remains the load-bearing assumption (an over-cut silently
  coarsens *real* work — a goal violation, worse than a dangle). The schema audit
  of every `dagql.Fields[*core.<ReflectionType>]` block is mandatory before merge;
  my spot check was reassuring but not exhaustive.

---

## Bottom line

v2 is correct, the cut has no new hole, round 1 is properly incorporated, and the
three pushbacks hold. Implement it — with one change and two riders:
1. **Carry `SkipProfile` on the `ResultCall` frame, not `CallRequest`** — it is a
   recipe property, it travels via the two existing copy primitives (`clone`/`fork`)
   instead of ~10 hand-audited sites (three of which v2 missed), it's digest-
   excluded by default, and it closes the import gap via persistence. This
   dissolves N2 and open-item (a).
2. **Fix the memo key** to a pre-resolution input (receiver `ResultID`/digest).
3. **Make §9 measure** the §4.4 dropped-user-wait ns (prove "rare"), the BSP
   `DroppedSpans→0`, and the over-cut schema audit — these are the real merge bar.

I'd build it on those terms.
