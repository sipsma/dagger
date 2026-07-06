# wcprof × OTel — skip-fix impl-plan v2 review, ROUND 2 (Chunk 1 / loader+gate owner)

**Reviewer:** Chunk 1 owner (loader + §6.1 gate). Re-verified v2 and the changes
against the branch at HEAD `4585bf413d` (implementer worktree). No code, no commits.

## Verdict

**v2 correctly incorporated round 1, and I can now certify the property that matters
most from my seat: the gate cannot be re-tripped by this fix.** N1, N3, the
predicate flip, and the three pushbacks are correct against the code. Two things to fix
before landing, both *volume/goal* fidelity (not gate correctness): the **N2 audit
*method* is incomplete** (it misses real `storeResultCall` sites), and **§4.2's framing
mislabels what is load-bearing**. The two open items resolve cleanly. **Landable after
the §9 completeness assertions pass, plus the two corrections below.**

---

## The property I certify: this fix is dangle-proof by construction

This is the round-2 headline and it re-grades several "HIGH" risks. **Every wait gates
on the *target's stored flag*, and the target's span-mint gates on the *same* flag** —
verified:

- **Singleflight:** `call_exec` minted iff `!req.SkipProfile` (`cache.go:3732`);
  `oc.profSkip` stored from that same `req.SkipProfile` (`:3744`); the wait gates on
  `oc.profSkip` (`:3958`, `EmitOTelWait(ctx, oc.execSpanCtx, …)`). Same value.
- **Lazy:** span minted iff `!shared.profSkip` (`:3017`); joiner wait (`:2973`,
  targeting `shared.lazyEvalSpanCtx`) and leader wait (`:3103`) gate on the **same**
  `shared.profSkip`.
- The flag is **set once** at finalization, before the eval reads it, so there is no
  torn read and the only possible transition is default-false → set-value.

So **span-minted ⟺ wait-emitted, for any value of the flag** — and the one dangerous
direction (span *skipped* while a wait is *emitted* → `UnresolvedWaitTargets`) requires
a true→false flip that never happens. **Consequence: no `profSkip`
provenance/import/timing bug can produce an `OrphanedParents`/`UnresolvedWaitTargets`
violation.** The worst case of *any* such bug is a **volume edge** (profiled something
that could have been skipped) or a **real-work coarsening** (skipped something real) —
both caught by the §9 *measurements*, never by the gate.

This means:
- **N2 (provenance) is mislabeled "HIGH (correctness)" — it is volume/goal fidelity.**
  Get it right for volume accuracy, but a missed path is not a gate-correctness blocker
  and cannot refuse a good trace. (The doc already reaches this for the import case;
  it holds for *all* of N2.)
- **The import default-false gap (open item a) is safe** — exactly as the doc argues.
- **The single thing that WOULD reopen a dangle is gating a wait on the *waiter's* bit
  instead of the *target's*** — which the doc correctly forbids in §4.2b. That comment
  is the load-bearing invariant; keep it loud in the code.

---

## Changes — verified against the code

- **Predicate flip — correct.** Separate, debug-independent, receiver-type
  `profileSkip` with `introspectionInfo` untouched (§3.2); the v1 debug-orphan
  rationale is retracted and matches my round-1 settlement (the loader builds an op for
  every span, `loader.go:251-254`; orphan only on an *absent* parent span, `:299-310`).
  Extracting `introspectionRootSet` as a shared constant (DRY, both classifiers) is
  clean. ✓
- **N1 (outer native `OpKindCall`) — correct.** `getOrInitCall` gates the wrapping
  `wcprof.BeginOp(OpKindCall)` at `cache.go:3559` and passes a **nil** `profOp` to the
  inner on the gated path (`:3560`), and the inner is nil-safe — so adding
  `|| req.SkipProfile` is a clean one-liner and genuinely restores oracle symmetry
  (without it a skipped miss keeps a native call op while OTel has none). ✓
- **N2 invariant + the three `initCompletedResult` paths — match the code exactly:**
  adopt-canonical (`:4132`, leave the adopted result's flag intact), copy-frame
  (`:4150-4151`, copy `shared.profSkip`), request-frame (`:4159-4160`, the only correct
  use of `req.SkipProfile`). The invariant ("`profSkip` travels with `resultCall`") is
  right. **But the audit method is incomplete — see below.**
- **N3 (lazy target-flag gating) — correct and genuinely load-bearing.** The forcer is
  a different recipe than the producer (`resultCall = shared.loadResultCall()`,
  `:2980`), so §4.2's shared-recipe argument does not cover lazy; gating the joiner
  wait (`:2973`) on the *producer's* `shared.profSkip` is what closes it, and the
  §4.4 user-wait-loss is the correct, named consequence. The §4.2b instruction to
  comment this so no one "simplifies" it to the waiter's bit is exactly right — that is
  the dangle-reopening move (per the certification above). ✓
- **N4 (stamp coverage) — the right backstop.** The `CallRequest` seam relies on
  `AroundFunc` running on every recording path; making §9.2 an affirmative
  zero-residual-introspection assertion is the correct way to prove completeness
  empirically rather than by inspection. ✓

## Correction 1 — N2's audit *method* is incomplete (volume, not dangle)

The doc enumerates "the other `sharedResult` construction/copy sites (`cache.go:1799`,
`:2431`, `:2531`)". That list **misses real `storeResultCall` sites** — grepping every
caller finds:

- **`cache.go:2356`** — `shared.storeResultCall(req.ResultCall.clone())` on a detached
  element/nth result (`shared.id == 0`). Sets `resultCall`; per the invariant needs
  `profSkip = req.SkipProfile`. **Not enumerated.**
- **`cache.go:2568`** — `r.shared.storeResultCall(frame.fork())` on a copy/derivation
  path. **Uses `.fork()`, not `.clone()`** — the invariant's "set or `clone()`d"
  phrasing omits the other copy primitive. **Not enumerated.**
- **`cache.go:1975`** — `shared.storeResultCall(req.ResultCall)` *re-stores* a
  normalized frame on the **same** `shared` (its work identity is unchanged), so
  `profSkip` is already correct there — benign, but only by luck of being the same
  object.

Per the certification these are **volume/coarsening, not dangle** — and several
(element/nth, copy) may never be lazy producers, so the volume impact may be nil. But
the *method* (enumerate a subset) is fragile against exactly the multi-path
result-adoption surface this codebase is known for (canonicalize / frame-copy / fork /
import). **Fix: make N2 a grep-driven audit over *every* `storeResultCall` caller and
*both* copy primitives (`.clone()` AND `.fork()`), not an enumerated list**, and state
the invariant as "wherever `resultCall` is set, copied, or forked." The §9.1 capture
across adopted/imported paths is the empirical backstop, but it proves volume, not
completeness — close the method gap so the volume numbers are trustworthy.

## Correction 2 — §4.2 mislabels what prevents the dangle (clarity, load-bearing)

§4.2 frames the **static cut** as the dangle-preventer ("a non-skipped waiter on a
skipped target cannot occur"). True for the static cut — but the *dangle protection is
the target-flag gating, not the static cut*: with waits gated on the target's stored
flag, even a **dynamic** per-caller cut is dangle-proof (a non-skipped joiner of a
skipped key has its wait gated *off* by the target's flag, so no dangle). What the
dynamic cut actually does wrong is **coarsen real shared work non-deterministically**
(clone/dep-load under `hideCtx` is real work a claimer's `IsSkipped` would skip,
race-decided) — a *goal* violation, not a dangle. So: **target-flag gating = the
dangle-proof invariant (gate correctness); static recipe cut = no-coarsening of real
shared work (goal correctness).** They protect different invariants. The doc gets this
right for lazy (§4.2b) but conflates them for singleflight (§4.2). Sharpen it so a
future reader cannot conclude "the static cut is what keeps the gate clean" and then
relax the target-flag gating that actually does.

## Pushbacks — all three correct

1. **`!IsRecording` short-circuit useless in production — correct.** Production records,
   so neither `!IsRecording` nor `!IsRecording && !wcprof.Enabled` ever fires; the
   receiver-type predicate (one immediate `ReceiverCall` hop) + `(receiverTypeName,
   field)` memoization is the real mitigation. Confirmed (this is sharper than my
   round-1 "weak"; I accept the correction). The §9.9 cost measurement is the right
   gate on whether memoization is needed.
2. **Do not fix chunk3's parentless `publishResult` here — correct, and the gate proves
   it's safe to defer.** Verified: a parentless survivor has an **empty** `cpSpan`, so
   `loader.go:303` (`cpSpan != "" && parentID == 0`) does **not** count it —
   `OrphanedParents` stays 0, so §9.2 can pass with survivors present. The fix is
   genuinely orthogonal (it touches *kept* survivors' parentage, which skipping never
   changes). The cross-reference (§5.6) correctly bounds "gate clean": clean under
   *this* gate, not under chunk3's proposed internal-kind-root signal. Good honesty.
3. **profiler-skip ⊋ UI-suppress by design — correct and worth stating.** A
   directly-called reflection accessor keeps its `dag.call` UI span while the profiler
   skips it. This is the point-B separation; recording it prevents a future reader from
   "fixing" a non-bug.

## Open items — resolved

- **(a) Recompute `profSkip` at import vs accept default-false →** *accept default-false
  and let §9 measure it.* Per the certification it can only be a bounded **volume**
  edge (imported introspection-lazy work profiled), never a dangle, so recomputation is
  a fidelity nice-to-have, not a correctness requirement. Resolve it the cheap way:
  ship default-false, and recompute from the stored `resultCall` at import **only if**
  §9.1/§9.6 show imported-introspection-lazy volume is non-negligible (the doc predicts
  it's near-zero — introspection metadata is computed eagerly — but the measurement,
  not the prediction, decides).
- **(b) Over-cut schema audit →** *well-scoped and low-risk; approve as the §9.4
  discharge, with one nuance.* The audit target is concrete: the
  `dagql.Fields[*core.<ReflectionType>]` blocks (`core/schema/module.go:418-648` for
  Function/TypeDef/ObjectTypeDef, plus the sibling reflection types). Spot-checked: the
  regression-list fields (`as*`, `args`, `returnType`, `typeDef`, `functions`,
  `fields`, `with*`, `__*`) are type-system metadata; the real loaders
  (`ModuleSource.asModule`, `Query.moduleSource`) and the `directory`/`file` producers
  (receiver `*core.Directory`) are **non-reflection receivers → stay profiled**. Nuance
  for the audit: confirm not just "is metadata" but "does not **force** real/lazy work"
  (a reflection accessor that forced a pending real value would coarsen it). Risk is
  low; the §9.4 capture check + this one-time read discharge it.

## Holistic fresh pass — a few additions

- **Run the gate on a post-skip *native* capture too, not only OTel.** §5.4 gates
  native (incl. the N1 outer call) on the same flag — this modifies the validated PR
  #13393 native emit (introspection ops vanish from native dumps). Native is dangle-
  proof by the same target-flag argument (native `BeginWait` gates on the same
  `oc.profSkip`/`shared.profSkip`, `:3941`/`:3094`/`:2964`), but the native dump is
  analyzed by `wcanalyze`'s own structural checks, not this loader's gate. **Add a §9
  item: the native structural checks are clean on a post-skip native capture** — so the
  "shared native code" change is proven, not assumed. The oracle alignment (§9.8) is
  necessary but doesn't substitute for running native's own gate.
- **Oracle stays aligned *by construction*, which is stronger than "tables match."**
  Both sources gate on the identical stored flag, so they drop the *exact same set* —
  worth stating as the reason §9.8 will pass, and a reason the oracle remains a valid
  cross-check (it isn't masking the dropped class differently per source).
- **Minor: predicate volume-determinism under `ReceiverCall` eviction.** If the
  immediate receiver can't be resolved mid-flight, `profileSkip` returns false →
  profiled. Can't dangle (certification), but it's a *volume* non-determinism the
  §9.10 determinism check (which watches `UnresolvedWaitTargets`) won't see. Negligible
  (the receiver is normally resolvable during its own call), but note it so a
  run-to-run residual-volume wobble isn't mistaken for an incomplete predicate.
- **Confirm the predicate's two rules tile the whole introspection tree.** Receiver-
  type (all reflection accessors) + introspection-root (Query entry points that
  *return* reflection objects) should leave no introspection call with a non-reflection
  receiver and a non-root field. §9.2's zero-residual assertion is the empirical proof;
  it is the right check and should be treated as a hard merge gate, not a spot-check.

## Bottom line

**Approve v2; it answered round 1.** From the loader/gate seat I certify the fix is
**dangle-proof by construction** (target-flag gating + single-set flag), so no
`profSkip` provenance/import/timing bug can re-trip `OrphanedParents`/
`UnresolvedWaitTargets` — which re-grades N2 and the import gap from "HIGH correctness"
to volume/goal fidelity. Land after: (1) **N2 as a grep-driven audit of every
`storeResultCall` caller + `.clone()`/`.fork()`** (it currently misses `:2356` and
`:2568`); (2) **§4.2 reworded** so target-flag gating (gate correctness) and the static
cut (goal/no-coarsening) are named as the distinct invariants they are; (3) the §9
completeness assertions — gate `0/0` across singleflight/lazy/adopted/imported, zero
residual introspection, exec/service hard check, **native gate clean**, over-cut audit,
and the cost/volume/oracle/determinism numbers — **all pass before landing**. The three
pushbacks are correct; open item (a) accept-default-false-and-measure, (b) approve the
scoped audit.
