# wcprof × OTel — Chunk 4 cycle, the FUNDAMENTAL fix (replay owner's analysis)

**Reviewer:** I wrote Chunk 1 and know `engine/wcprof/wcanalyze/replay.go` best.
The owner has moved the bar: accuracy is non-negotiable, so any "anchor in the
recorded frame" approach that gives a wrong counterfactual is off the table. I
re-examined my two Round-2 positions against the concrete example and the DP, and
**I was wrong on both.** This is the honest re-derivation. Analysis only — no code.

## Headline (I'm changing my mind, the evidence warrants it)

1. **Approach 1 (`startOf`, recorded-offset anchor) is WRONG on accuracy.** The
   concrete example proves it gives the wrong what-if answer. My Round-2
   endorsement under-weighted the counterfactual divergence as "second-order"; it
   is **first-order** — it zeroes exactly the cross-tree saving the profiler exists
   to find. Off the table, agreed.
2. **My prefix-spawn rebuttal ("it still cycles") was WRONG.** A back-referencer
   waits on the target, so it **ends after the target spawns**; a prefix-replay of
   the parent that stops *at* the target's spawn never joins it. Prefix-spawn does
   **not** cycle in the structural case. The owner's charge-1 reasoning is correct.
3. **The fundamental fix is the scaled prefix-to-spawn anchor.** It gives the
   correct scaled start AND terminates; genuine residual loops (rare) fall to the
   existing recorded-back-edge. The owner's seed hypothesis is essentially right.

## The concrete example, traced through the actual DP

Setup (recorded): `R`=POST/query[0,300] spawns `A`=app[0,300] and `L`=lib[0,300].
`A` does config-parse self `[0,100]`, then spawns `T`=load-foo[100,300] (T's parent
is A). `L` spawns `W`; `W` dedups onto `T` ⇒ `W` waits on `T` (cross-tree
`actWaitJoin`, `W.waitEnd≈T.end=300`). What-if: scale A's config-parse class → 0.
**TRUE answer:** T spawns at 0, runs 0→200, W unblocks at 200 ⇒ config-parse must
be credited 100 ms.

The result hinges on the order `R`'s final `joinUpTo(300)` (replay.go:389) visits
its children `pendIdx` (sorted by `EndNS,ID`, compile lines 188-194). Both end at
300, so it's ID order — i.e. **arbitrary**.

- **A-before-L:** `finish(A)` runs in-order — config-parse scaled→0, so at
  `actSpawn(T)` (line 378) `A.clock=0` ⇒ `setStart(T,0)`. T runs 0→200. Then
  `finish(L)` → W's `actWaitJoin(T)` (line 379) gets 200. **Correct, and the anchor
  never fires** (T already started in-order).
- **L-before-A:** `finish(L)` → W's `actWaitJoin(T)` → `finish(T)`, but A hasn't
  replayed ⇒ T is unstarted ⇒ **the anchor (line 316-337) fires.** Here the
  approaches diverge:
  - **Old full-finish anchor (line 318 `s.finish(par)`):** replays A *with* the
    factor, reaches `actSpawn(T)` at `A.clock=0` ⇒ `setStart(T,0)`. **Correct (T=0,
    W=200).** (No cycle here — A's only child is T.)
  - **Approach 1 `startOf` (recorded offset):** `startOf(T)=startOf(A)+(T.start−
    A.start)=0+(100−0)=100`. T anchored at **100 regardless of the factor** ⇒ T
    finishes 300 ⇒ W=300 ⇒ **the 100 ms saving is MISSED. WRONG.**
  - **Approach 2 prefix-to-spawn:** replay A's prefix *with* the factor up to
    `actSpawn(T)`, stop ⇒ `A.clock=0` ⇒ `setStart(T,0)`. **Correct (T=0, W=200),**
    and no over-reach.

So the old code is *correct here but cycles elsewhere*; `startOf` is *wrong here
but terminates*; prefix-spawn is *correct here and terminates*. That is the whole
problem in one example, and it is why `startOf` must go.

**Where I went wrong in Round 2:** I called `startOf`'s divergence "baseline-exact,
second-order." Baseline-exact is true and irrelevant — the what-if *is* the
counterfactual, and on the counterfactual `startOf` discards the scaling of the
target's pre-spawn ancestry, which is precisely the cross-tree saving. That's
first-order on the signal of interest. Conceded.

## Charge 1 — does prefix-spawn actually cycle? No. (My rebuttal was not airtight.)

My Round-2 claim was "if P joins op#54 before op#94's spawn, partial replay still
cycles." Re-examined rigorously, that cannot happen for the loop-closing edge:

The back-referencer (op#54) closes the loop by **waiting on** the target (op#94).
`actWaitJoin` requires `op#54.waitEnd ≥ op#94.end − ε` (replay.go:165), and
`op#54.end ≥ op#54.waitEnd`, so **`op#54.end ≥ op#94.end − ε > op#94.spawn`** (the
target spawns before it ends). A prefix-replay of the shared parent P that stops at
`actSpawn(op#94)` runs `joinUpTo(t)` only for `t ≤ spawn_94` (line 353), which joins
P's children with **recorded end ≤ `spawn_94`**. Since `op#54.end > spawn_94`,
`op#54` is **not** joined ⇒ `finish(op#54)` is never called ⇒ **no cycle.** This is
general: *anything that transitively waits on the target ends after the target
spawns, so prefix-to-spawn can never reach it.* My rebuttal conflated "P's **full**
finish joins op#54" (true — that's the over-reach) with "P's **prefix-to-spawn**
joins op#54" (false). The distinction is the whole fix.

The cycle is therefore **caused specifically by the anchor doing P's full finish**
(`joinUpTo(P.end)`, which sweeps in op#54 whose `spawn_94 < op#54.end ≤ P.end`)
when it only needed P's progress to `spawn_94`. Prefix-to-spawn removes exactly the
over-reach interval `(spawn_94, P.end]` that contains the back-referencer.

## Charge 2 — the fundamental fix, concretely (and stress-testing the seed)

**Replace the full-finish anchor with a scaled, bounded "advance parent to this
op's spawn."** Concretely, at the anchor block `replay.go:316-337`:

- Today: `if par≥0 && !inFlight[par] { s.finish(par); … }` (line 317-319) — a
  *full* `finish(par)` whose `joinUpTo(par.end)` (line 389) and post-spawn child
  joins over-reach.
- Replace with a bounded replay `advanceToSpawn(par, i)` that runs `par`'s action
  loop (the same loop, lines 371-388) from `par.simStart` **with the factor**,
  doing `joinUpTo(a.at)` before each action, **until it processes
  `actSpawn(i)` → `setStart(i, clock)` → return** — and does **not** run par's
  final `joinUpTo(par.end)` nor mark par finished. The child's spawn action index
  is known at compile time (it's already emitted as an `actSpawn` in par's program,
  lines 159-161; record its index per child, or scan).

Why this is correct and terminating:
- **Correct (scaled):** the start it assigns is `par`'s clock at the spawn under
  the factor — identical to what the in-order `finish(par)` would set at line 378.
  At factor 1 it equals the recorded start (so no baseline change, and *no
  regression to native's validated factor-1 behavior*). Under a factor it scales
  the target's pre-spawn ancestry — the cross-tree saving — correctly.
- **Terminating:** its `joinUpTo` only reaches children ending `≤ spawn_i`, never
  the back-referencer (Charge 1). The recursion up the spine (par may itself be
  out-of-order ⇒ `advanceToSpawn(grandpar, par)`) bottoms out at a root (started in
  `Run`, line 287).
- **In-order untouched:** an in-order op is `setStart`-ed by its parent's
  `actSpawn` before any `finish` on it, so `!started[i]` (line 316) is false and
  the anchor never runs — byte-identical to today for the normal path.

**The owner's seed hypothesis — stress-tested:** "unify the three `finish` entry
paths (anchor 318, implicit join 365, wait-join 379) so the anchor does the
minimal scaled prefix-to-spawn and any genuine residual cycle is resolved by the
one recorded back-edge." **Essentially right, with one correction.** All three
paths *already* share the cycle break — it's at the *top* of `finish` (the
`inFlight` check, lines 341-344), so every entry that calls `finish` on an inFlight
op is caught. The anchor's defect is **not** "lacks cycle handling"; it's that
*full*-`finish(par)` **manufactures** the inFlight situation by descending into
par's post-spawn joins. Make the anchor minimal (advance-to-spawn) and it stops
manufacturing false cycles; the shared `inFlight` break then only ever fires on a
**genuine** residual. So: minimal anchor + the single existing recorded-back-edge
= the unification. Nothing cleaner exists — you cannot compute a scaled spawn time
without replaying the parent's pre-spawn joins (they're recursive `finish`es), so a
bounded prefix-replay is the irreducible mechanism; there is no non-replay
shortcut.

**One implementation choice the owner must pick** (correctness-equivalent):
- *Throwaway re-run* (simplest): `advanceToSpawn` leaves par unfinished; the later
  in-order `finish(par)` re-runs the prefix (idempotent — `setStart`/`finish` of
  the already-done children are no-ops, self-segments re-sum deterministically into
  a fresh local `clock`, no double-count because each `finish`/`advance` owns its
  `clock`). Cost: par's prefix replays at most twice. Clean, slightly redundant.
- *Resumable* (faster): persist par's `clock`/`pendCur` after the prefix and resume
  in the full finish. More state, more care. Optimization, not correctness.

I recommend throwaway re-run first (correctness-first, the owner's stated priority),
optimize only if a real trace shows it matters.

## Charge 3 — what topologies actually close the loop

The textbook 2-subtree case (above) provably does **not** deadlock and, traced out,
does **not** even cycle under the *old* full anchor — A's full finish joins only T
(its child), never W (L's child). So the loop needs a **shared ancestor whose
children cross-reference**:

1. **Sibling cross-join (the module-loading cycle, the common case):** `op#54` and
   `op#94` are siblings under P; `op#54` waits on `op#94`; anchoring `op#94` →
   `finish(P)` *full* → `joinUpTo(P.end)` joins `op#54` (since `spawn_94 < op#54.end
   ≤ P.end`) → `op#54` inFlight → cycle. **Prefix-to-spawn kills it** (joins only to
   `spawn_94`, before `op#54.end`). This is what Exp 2's "no-anchor SCC ⇒ 0 cycles"
   is telling us, and it is *forced* by the code: only the anchor's `finish(par)`
   reaches a non-descendant sibling.
2. **Parent in-flight pre-spawn (rare, genuine):** `finish(T)` while T's parent P
   is itself inFlight and has not yet reached `actSpawn(T)` — i.e. P's *own*
   pre-spawn work transitively waits on P's *later* child T. That is a real ordering
   inversion (a detached/concurrent structure the trace nests inside P). Prefix-spawn
   can't advance P (it's on the stack) ⇒ this **legitimately** hits the recorded
   back-edge (cycle break, line 341-344). This is the owner's "if a correct
   Approach 2 still loops, that's a real cycle to resolve principledly" — and the
   recorded back-edge *is* that principled resolution (assume the op's recorded
   duration; it's the only frame-consistent fallback). Expected to be vanishingly
   rare; should be **counted** so we can see if it's ever non-trivial.
3. **`joinUpTo` "child never anchored" (line 360-362):** a child reached by an
   implicit join that was never spawned gets `setStart(c, clock)`. This rides the
   *join* path, not the anchor; prefix-to-spawn *bounds* it (the join only reaches
   children ending ≤ the prefix cutoff), so it stops contributing to the false
   loop. Genuine never-anchored children ending before the cutoff are joined
   correctly.

So: case 1 is the cycle we see, and prefix-spawn eliminates it cleanly; case 2 is
the only genuine residual and is the *correct* place for the recorded back-edge;
case 3 is bounded by the same fix.

## Is original-frame anchoring EVER correct? Plainly: no (for this profiler).

The doc comment (replay.go:30-34) justifies original-frame anchoring —
"mixing frames would corrupt the schedule because ops reached through cross-tree
wait targets are anchored at original times when their own root has not been
replayed yet." That is **correct only for what-ifs that do not scale the target's
pre-spawn ancestry** (then recorded offset = scaled offset, trivially). The profiler
runs a what-if over *every* class, including ancestry classes, and the cross-tree
saving from scaling an ancestor is *exactly the signal*. So original-frame anchoring
is wrong for the cases that matter most — it is the design bug the owner identified.
Crucially, prefix-spawn does **not** "mix frames" in the way the comment fears: it
computes the target's start in the **target's own parent's scaled frame** (a single,
consistent frame derived from the target's real ancestry), not by importing the
consumer's frame. So the comment's actual concern (incoherent frame mixing) is
*avoided*, not ignored — and the comment should be rewritten to describe
prefix-to-spawn, since its current rationale is the bug.

## On the `FallbackAnchors` gate signal (my Chunk 1 diagnostic)

Unchanged from my last review and now sharper: keep a counter, but in the **new**
place — increment when the **case-2 residual** (parent in-flight ⇒ recorded
back-edge) fires, because *that* is now the only "we couldn't faithfully anchor
this" event. The common out-of-order anchor (case 1, prefix-spawn) is no longer a
"fallback" — it's the correct path — and should **not** count. My
`TestGateFallbackAnchorsReportOnlyAndThreshold` asserted the old semantics (it
builds out-of-order cross-root joins and expects fallback anchors); under the fix
those resolve cleanly, so it must be re-pointed to construct a genuine case-2
residual (a parent-in-flight inversion) to exercise the gate threshold. Its failure
is expected, not a defect — same conclusion as last round, now with the right
target for the counter.

## What the owner must decide

1. **Endorse prefix-to-spawn as the fix** (it changes the shared/native replay —
   justified, native shares the bug; and it *improves* native's accuracy, not just
   OTel's). Confirm `replay_test.go` (PR #13393's counterfactual tests) pass
   per-test as the native-regression guard.
2. **Residual policy:** accept the recorded back-edge as the principled resolver for
   the genuine case-2 inversion (I believe yes — it's the only frame-consistent
   fallback), and require it be **counted** so a non-trivial residual is visible.
3. **Implementation form:** throwaway re-run (recommended, correctness-first) vs
   resumable (perf). Pick correctness-first now.
4. **Rewrite the replay.go:30-34 comment** to describe prefix-to-spawn; the current
   "original frame" rationale is the bug and must not survive as documented intent.

---

## Summary

- **Approach 1 (`startOf`) accuracy verdict:** **wrong** — the concrete example
  (config-parse→0) shows it anchors T at the recorded 100 ms regardless of the
  factor, missing the 100 ms cross-tree saving. I retract my Round-2 endorsement;
  the divergence is first-order on the signal the profiler exists to measure.
- **Does prefix-spawn cycle?** **No** (structural case). The back-referencer waits
  on the target so it ends after the target's spawn; a prefix-replay stopping at the
  spawn never joins it. My Round-2 rebuttal was not airtight — it conflated full
  finish with prefix-to-spawn. Only a genuine parent-in-flight inversion (rare) can
  residually loop, and that is a real cycle for the recorded back-edge.
- **Proposed fundamental fix:** replace the full-finish anchor (replay.go:316-337)
  with a **scaled, bounded `advanceToSpawn(par, i)`** that replays par's prefix —
  with the factor — only up to `actSpawn(i)`, setting i's start to par's scaled
  clock there; recurse up the spine; the single existing recorded-back-edge
  (lines 341-344) resolves genuine residuals (parent-in-flight), counted. Correct
  under counterfactuals, terminating, in-order/baseline byte-identical, no
  approximation. No cleaner mechanism exists (a scaled spawn time requires replaying
  the parent's recursive pre-spawn joins).
- **What the owner must decide:** endorse the shared-replay change + per-test
  native-regression check; accept (and count) the recorded back-edge for the genuine
  residual; pick throwaway-re-run vs resumable (recommend throwaway); rewrite the
  replay.go:30-34 "original frame" comment (it documents the bug).
