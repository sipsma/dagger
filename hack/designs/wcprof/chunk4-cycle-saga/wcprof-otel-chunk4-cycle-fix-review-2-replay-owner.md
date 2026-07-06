# wcprof × OTel — cycle fix round-3 review (replay owner)

**Reviewer:** replay owner (Chunk 1; I know `engine/wcprof/wcanalyze/replay.go`).
Verified the `spawnTo` proposal in the implementer's writeup against my worktree's
`replay.go` (the unchanged shared replay) line-by-line. No code, no commits.

## Up front — I was partly wrong again, and the evidence says so

The implementer's empirical work corrects two more of my claims, and I concede
both:
- **My round-2/3 residual prediction was INCOMPLETE.** I predicted the only
  residual would be the "parent-in-flight inversion" hitting the existing
  recorded-back-edge. The implementer found a **second, distinct** residual — a
  *concurrent non-gating pre-spawn wait* — that the existing back-edge does **not**
  correctly handle and that needs the new skip-predicate. There are two residual
  classes, not one.
- **My round-3 "throwaway re-run is idempotent" claim was WRONG.** With the
  skip-predicate the prefix and the later full `finish(par)` compute *different*
  child starts (skip vs no-skip); correctness rests on **first-write-wins**, not
  idempotency. Details in §4.

So I am not defending priors here — I'm reviewing the fix as found.

## Q1 — emit-vs-replay: legitimate general replay fix, with a real caveat

**Verdict: fix belongs in the REPLAY; the skip-predicate is a legitimate
general-model correction, not a forbidden emit-paper-over — BUT it is applied
asymmetrically and leaves a residual in-order over-serialization I'll flag.**

The over-serialization is a **genuine replay-model gap**, independent of OTel:
the model derives a child's spawn time from the parent's clock at the `actSpawn`
action, and an *earlier-starting* wait action is processed first (its `at =
w.StartNS`, replay.go:163; the action loop runs `actWaitJoin` before the later
`actSpawn`, lines 378-381). So **any** op that holds a wait which *started before*
but *ended after* it spawned a child serializes that spawn behind the wait — even
though the recording shows they were concurrent. The model already uses recorded
*end* times to classify wait gating (`actWaitJoin` needs `waitEnd ≥ targetEnd−ε`,
line 165); it simply never applied that same recorded-time logic to **spawn**
gating. The skip-predicate closes exactly that gap and is consistent with the
model's own existing principle. So this is **not** "fixing an OTel emit bug inside
the replay."

Is §3.1's attribution itself faithful? **Yes, faithful-but-lossy.** Putting a
suppressed caller's wait on the nearest recording ancestor (op#250's `call_exec`)
is design-endorsed (§3.1: "that ancestor is the op that actually blocked") and the
time *does* land in the right subtree. What it loses is the *concurrency* that
native preserves by recording a separate `call` op per caller — so native's
op#250 `call_exec` carries only the spawn (no concurrent wait) and never trips
this. Emitting separate ops in OTel would re-introduce exactly the volume §4.1
exists to avoid, so the **lean emit should stay** and the replay should recover
the concurrency from recorded times. That's the right layering: §3.1 is a faithful
lean choice, the replay's spawn-gating was the latent bug, fix it in the replay
(which fixes native's latent gap too).

**The caveat (real, surface it): the fix is prefix-path-only.** The skip-predicate
lives in `spawnTo`. The **in-order** `finish()` (line 378-381) still serializes a
spawn behind a concurrent wait. So:
- For the **cycle** (the showstopper) — complete: the cycle only forms via the
  out-of-order anchor/prefix path, which `spawnTo` now handles.
- For **cross-tree-referenced** spawn times (the signal that matters for a
  what-if) — complete: those ops *are* reached out-of-order, so `spawnTo` runs and
  first-write-wins keeps the correct prefix start.
- For an op spawned behind a concurrent wait but **not** cross-tree-referenced —
  the in-order path still over-serializes its start (an OTel-§3.1-specific,
  pre-existing-shaped accuracy residual). It is mostly self-cancelling in a
  what-if (baseline and scaled both carry it) *except* when the what-if scales the
  concurrent wait's own class.

Given "accuracy is non-negotiable," the owner should decide: (a) accept this
bounded in-order residual (it doesn't affect the cycle or the cross-tree signal),
or (b) make spawn-gating consistent in *both* paths — i.e. the in-order `actSpawn`
should also use the parent's clock *excluding waits not yet completed at the
spawn*. (b) is a larger change — it needs a separate "spawn clock" because the
op's own `finish` legitimately *is* gated by the wait — so I'd not block the cycle
fix on it, but it should be a tracked follow-up, not silently dropped.

## Q2 — skip-predicate soundness: sound, no genuine-gate misclassification

**`endNS[target] ≤ startNS[spawn]` is the correct gating test, within the model's
existing assumptions.** Reasoning:

- **It cannot skip a genuine gating dependency.** If a wait genuinely gated the
  spawn, the spawn was blocked until the wait completed, so (recorded)
  `spawn.start ≥ wait.end ≈ targetEnd` ⇒ `endNS[target] ≤ startNS[spawn]` ⇒ **not
  skipped**. The predicate only fires when `endNS[target] > startNS[spawn]`, i.e.
  the spawn provably preceded the wait's completion ⇒ the wait did *not* gate it.
  I tried to construct a misclassification (skip a real gate) and **could not** —
  the recorded order forecloses it.
- **Under counterfactuals:** it keys on recorded times, which is the *same*
  invariance the entire replay already assumes ("bakes the observed ordering in as
  a constraint," replay.go:24-27; `actWaitJoin`/`actWaitNoop` are also fixed from
  recorded times). So it introduces **no new** soundness assumption — if a
  counterfactual could re-order gating vs concurrency, the whole model (not just
  this predicate) would be unsound. It's the same family, genuinely (though a
  *new* classification — "does this wait gate this spawn" — not literally
  `actWaitNoop`; call it what it is).
- **One edge — the ε boundary.** The predicate uses `≤` with **no** ε, while
  `actWaitJoin` uses `≥ targetEnd−ε`. A genuine gate recorded as ending a sub-ms
  *after* the spawn (clock noise) would be wrongly skipped → a hair of
  under-serialization → over-crediting a speedup. The real residual is far from the
  boundary (D ends 15 ms after the spawn), so it doesn't bite here, but for
  consistency I'd **either** use `endNS[target] ≤ startNS[spawn] + ε` (conservative:
  treat boundary as gating) **or** thread the real `waitEnd` through the compiled
  action instead of the `targetEnd` proxy (the implementer offered this in their
  decision #3 — I'd take it; it removes the proxy *and* lets you apply the same ε
  as `actWaitJoin`).

Net: sound, no genuine-gate misclassification, one ε-consistency nit to close.

## Q3 — residual taxonomy: two distinct, complete classes; no masquerading

There are **two** residual classes and they are cleanly separated by the
recorded-time test:

| class | what it is | recorded signature | resolution |
|---|---|---|---|
| **concurrent non-gating wait** | a pre-spawn wait that ended *after* the spawn | `endNS[target] > startNS[spawn]` | **skip** (no `finish`, no serialize) |
| **genuine inversion** | a *gating* dependency that is itself in-flight (mutual/self-reference), or the parent is in-flight | `endNS[target] ≤ startNS[spawn]` ∧ target/parent in-flight | **break** (recorded back-edge / recorded-offset) |

- **Complete:** every path that reaches `finish(inFlight)` is either via a
  concurrent wait (now skipped before `finish` is called) or via a genuine gating
  dependency (processed; if in-flight → the existing break at replay.go:341-344).
  `joinUpTo` inside `spawnTo` is bounded — it only joins children ending ≤ the
  spawn cutoff, and a child ending ≤ cutoff cannot wait on the post-cutoff
  back-referencer (its wait targets end ≤ `cutoff+ε`), so it cannot re-reach the
  ring. Termination is therefore guaranteed (the in-flight break is the backstop,
  and the false cycles are removed before they reach it); the empirical "0 residual
  rings" confirms it.
- **No masquerading:** a genuine cycle's gating waits have `endNS[target] ≤
  startNS[spawn]` (that's what "gating" means) ⇒ they are **not** skipped ⇒ a real
  cycle is never hidden by the skip. Conversely a concurrent wait is skipped before
  `finish`, so it never produces a false break. The two classes can't swap.

So the two-tier policy (skip concurrent; break+count genuine) is correct and
complete. **My earlier "only the back-edge is needed" was wrong**: routing the
concurrent class through the back-edge would `finish(D)` → cycle → break →
`CycleWarnings++` (gate fails on a *non*-genuine cycle) **and** serialize the spawn
behind D (wrong start). The skip is **necessary**, and it is the right mechanism —
not a substitute for the back-edge but a sibling to it.

## Q4-mine — idempotency / first-write-wins: correct but fragile; throwaway required

The skip-predicate makes the throwaway re-run **non-idempotent**, and that's
fine *because of an intentional asymmetry*, which the owner should see explicitly:

- `spawnTo` (prefix, **skip**) computes op#251's start *excluding* the concurrent
  D-wait ⇒ the **correct** scaled spawn time. It writes it via `setStart` (once-only).
- The later in-order `finish(op#250)` (full, **no skip**, line 379) processes the
  D-wait (correct — op#250's *own* finish **is** gated by D) and at its `actSpawn`
  would set op#251's start to the *later, over-serialized* clock — but `setStart`
  is once-only, so **first-write-wins keeps the prefix value**. ✔

This is correct, but it is correct *by construction*, resting on three things: (1)
the out-of-order reference (prefix) always runs before the in-order finish — true,
because being a wait target *is* what triggers the early reference; (2) `setStart`
is once-only (replay.go:299-304); (3) the asymmetry is deliberate (child-start
excludes concurrent waits; parent-finish includes them). It is **fragile to future
edits** — anyone who makes `finish` also skip, or makes `setStart` overwrite, or
reorders, silently breaks it. So it must be **commented loudly** at both sites.

**This sharpens my throwaway-vs-resumable call and flips the reasoning.** I
previously said "throwaway is fine because idempotent." Now: **throwaway is
*required*** precisely because it is *not* idempotent. A resumable `finish(par)`
that continued *after* the spawn would never re-process the concurrent D-wait
(it's before the resume point) ⇒ op#250's own finish would lose its D gating ⇒
wrong. The throwaway full re-run is what lets the parent's finish include the wait
(for itself) while the child keeps the skip value (for its spawn). So: **stay with
throwaway**, and do *not* "optimize" to resumable — it would be incorrect, not just
slower.

## The `spawnTo` code — partial-state / in-flight handling

Reads correctly to me:
- **`par < 0`** (target's parent is a root not yet in `Run`'s chain) → recorded
  start. This is the one spot that inherits the *original*-frame concern the doc
  comment (replay.go:30-34) raised — a cross-root out-of-order target anchored at
  its recorded start before its root is chained. It's rare and pre-existing, but
  **verify it against `Run`'s root chaining** (lines 277-285): if that root later
  gets a chain shift, this target won't. List as a Q4 item.
- **`inFlight[par]`** (parent mid-replay, my predicted genuine inversion) →
  recorded-offset. This *is* the `startOf`-shaped answer the owner ruled out — but
  it is now confined to the **rare genuine inversion**, which has no clean scaled
  answer (it's a real self-referential structure). That's the legitimate place for
  it, exactly parallel to the existing cycle-break's recorded-duration assumption.
  It **must be counted** (this is the home for the repurposed `FallbackAnchors`
  signal — see Q4).
- **`defer inFlight[par]=false`** fires on the early `return` at the spawn too
  (Go defers run on all returns), so `par` is correctly un-marked. ✔
- **`joinUpTo` calls `finish(c)`** (full) for pre-spawn children — bounded and safe
  per Q3.

## Q4 — remaining blockers / watch-items before it lands in native

1. **Perf — measure on the *native* scale.** `spawnTo` re-walks a parent's prefix
   per out-of-order reference; the throwaway re-run means a parent's prefix can be
   walked once per out-of-order child + once in-order. 0.18 s on 11k OTel ops is
   fine, but the **native dump is 86k ops** and Cloud traces are larger. Measure on
   86k and on a deliberately adversarial shape (one deep parent with many
   out-of-order children). Memoize spawn-clocks if it bites. Not a correctness
   blocker; a scaling one.
2. **ε / zero-duration boundary.** The real residual involved a **zero-duration**
   op (op#251 `[190,190]`). Add a targeted test at the gating boundary, and adopt
   the `+ε` (or real-`waitEnd`) form from Q2.
3. **In-order spawn over-serialization (Q1 caveat).** Decide accept vs. make
   spawn-gating consistent in both paths. Track it explicitly; don't let it vanish.
4. **Cross-root anchoring (`par<0`).** Verify consistency with `Run`'s chain shift.
5. **`FallbackAnchors` → genuine-residual counter.** Increment only in the
   in-flight/self-ref fallback inside `spawnTo` (the genuine residual), *not* on the
   common prefix anchor. Re-point my `TestGateFallbackAnchorsReportOnlyAndThreshold`
   to construct a genuine inversion; its current failure is expected.
6. **Native per-test regression.** Since this lands in PR #13393's validated
   replay, confirm `engine/wcprof/wcanalyze/replay_test.go` passes **per-test**, not
   just in aggregate — that's the native-behavior guard, and the makespan-preserved
   /ranking-shift-toward-correctness numbers should be reproduced from a clean tree.

## Summary

- **Emit-vs-replay:** fix in the **replay**. The skip-predicate is a legitimate
  general replay-model correction (spawn-gating should use recorded end-times, like
  wait-gating already does), **not** a forbidden emit-paper-over — §3.1's attribution
  is faithful-but-lossy by lean-emit design, and the replay correctly recovers the
  concurrency. **Caveat:** the fix is prefix-path-only; a bounded in-order
  spawn-over-serialization remains (OTel-§3.1-specific, mostly self-cancelling) —
  surface it, decide accept-vs-make-consistent, don't drop it.
- **Skip-predicate soundness:** **sound**; it cannot skip a genuine gate (recorded
  order forecloses it); no misclassification case exists within the model's
  premises; one ε-boundary nit (use `+ε` or thread the real `waitEnd`).
- **Back-edge vs new predicate:** the **new predicate is necessary and correct** for
  the concurrent class; the existing back-edge would wrongly fail the gate +
  over-serialize. Two distinct, complete, non-masquerading residual classes. (My
  earlier "back-edge only" prediction was incomplete — conceded.)
- **Idempotency / first-write-wins:** **not** idempotent (I was wrong); correct via
  a deliberate skip/no-skip asymmetry + first-write-wins; **fragile**, comment both
  sites loudly; **throwaway re-run is now *required*** (resumable would be
  *incorrect*, not just slower).
- **Remaining blockers:** perf at 86k+/adversarial shape; ε+zero-duration test;
  the in-order-path consistency decision; cross-root `par<0` anchoring; the
  repurposed genuine-residual counter; native `replay_test.go` per-test green.
  None blocks the *direction* (adopt prefix-spawn + skip-predicate); all should be
  closed before it lands in the shared/native replay.
