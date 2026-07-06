# wcprof × OTel — Chunk 4 cycle: the FUNDAMENTAL fix (by the Chunk 3 implementer)

**Analysis only — no code, no commits.** Re-opened under Erik's ruling that
**accuracy IS the bar**: a bounded-but-wrong counterfactual that under-credits a
real bottleneck is not shippable; "accept an approximation / document the
limitation" is off the table short of an utter last resort; and if a *correct*
prefix-spawn still loops, that loop is a real data cycle to resolve principledly,
not a number to paper over.

Verified against the actual `engine/wcprof/wcanalyze/replay.go` at my worktree HEAD
`107ebe5c0c` (the **pre-fix** version — no `startOf`; the prompt's line numbers
`374/384-388/441/451` are the implementer's startOf-prototype frame, shifted ~+60;
I cite my worktree's real lines). The raw trace/native dump are in the
implementer's container; the load-bearing claims here are code-derivable, and I say
where a subgraph would add confirmation.

---

## Charge 1 — Under the accuracy bar, my own analysis now REJECTS startOf. Plainly: yes.

In Round 2 I wrote that startOf's error is "an *accuracy* approximation, not a
*soundness* regression … bounded, exact-in-baseline, no negative durations …
acceptable; document it." **The facts in that analysis are correct; the disposition
was wrong, and Erik's bar makes the same facts reject startOf.** I own this.

- My "offset ≥ 0 ⇒ no child-before-parent, no negative duration" argument proves the
  schedule stays **well-formed**. My "exact in baseline" point proves the *baseline*
  makespan is untouched. **Neither is sufficient for counterfactual correctness** —
  and the counterfactual *is* the product. A well-formed schedule that reports the
  wrong saving for a real bottleneck is precisely the failure mode, and that is what
  startOf produces.
- The concrete anchor makes it undeniable. `POST/query` spawns `app`,`lib`; `app`
  does 100 ms config-parse then spawns `T=load foo` ([100,300]); `lib`'s `W` dedups
  onto `T` → `W` waits on `T` cross-tree (T's parent is `app`). What-if
  `config-parse → 0`:
  - **TRUE:** `app` reaches T's spawn at 0, `T` runs [0,200], `W` unblocks at 200 →
    config-parse **saves 100 ms across the cross-tree wait**.
  - **startOf** (anchor `T` at `app.simStart + RECORDED offset = app.start + 100`,
    factor-blind): `T` pinned [100,300], `W` still 300 → **saving MISSED → a real
    bottleneck (config-parse) under-credited.** This is exactly the unshippable
    class.
- The mechanism is in the code: the correct counterfactual start of a child is set
  by the parent's replay reaching the child's `actSpawn` at the **scaled** clock
  (`replay.go:377-378`, `setStart(a.ref, clock)` after the factor was applied to the
  parent's pre-spawn `actSelf`, `:375-376`). startOf substitutes the *recorded*
  offset (`replay.go:329-333` is exactly that recorded-offset form, today's fallback)
  and so is factor-blind by construction. The replay's "original-frame anchoring"
  rationale (`replay.go:31-34`) is the *statement* of this behavior — and under
  Erik's bar, **that rationale, where it means anchoring a counterfactually-relevant
  cross-tree target at its recorded time, is itself the bug.**

**Verdict: startOf (Approach 1) is rejected.** My Round-2 recommendation to accept
it was wrong under the correct bar; the analysis I did then is exactly the argument
for fixing it now.

---

## Charge 2 — prefix-spawn: gives the right answer, and provably does NOT re-introduce the cycle.

**prefix-spawn = anchor the out-of-order child by replaying the parent's timeline
*with the factor* up to that child's `actSpawn`, anchoring the child at the
resulting clock** (then stop — do not run the parent's post-spawn joins).

### It gives the counterfactually EXACT start (no approximation)

The clock at the parent's `actSpawn(child)` is, by construction, `parent.simStart +
Σ(scaled pre-spawn actSelf) + (scaled finishes of children joined before the
spawn)` — identical to what the current `finish(parent)` computes when it reaches
`actSpawn` (`:377-378`). So prefix-spawn reproduces the current code's **correct**
anchoring (the concrete scenario: `T` at 0, `W` at 200, 100 ms saved) and drops
**only** the over-reaching part (the parent's post-spawn `joinUpTo`, `:373/389`).
Unlike startOf this is **exact**, not approximate — it is the right answer in
baseline *and* under any factor.

### It does NOT re-introduce the cycle — adjudicating the Round-2 hedge

In Round 2 I (and the implementers) hedged "prefix-spawn might still cycle if the
parent joins a cross-referencer before the spawn." **Carrying the temporal argument
through, that hedge is wrong. prefix-spawn is provably cycle-free for the
anchor-artifact class:**

1. The cycle is closed by a back-referencer `W` that **waits on `T`** (the
   singleflight join, classified `actWaitJoin` at `replay.go:165`: it ends at T's
   end, `w.EndNS ≥ w.Target.EndNS−ε`).
2. A wait on `T` cannot start before `T` exists, and ends when `T` finishes ⇒
   **`W.recordedEnd ≥ T.finish > T.spawnTime`.** So `W` *necessarily* ends **after**
   `T`'s spawn.
3. prefix-spawn replays the parent only up to `T`'s spawn, so its `joinUpTo` joins
   only the parent's children with `recordedEnd ≤ T.spawnTime` (`replay.go:353-369`,
   `if s.p.endNS[c] > t { return }`). `W` (ending after T's spawn) is **never
   joined** — whether `W` is a sibling of `T` or cross-tree. The back-edge is never
   traversed. (Contrast the bug: the current `finish(parent)` runs `joinUpTo(endNS)`
   at `:389`, which *does* reach the post-spawn `W` → the spurious back-edge.)
4. Could a *different* child `C` that prefix-spawn *does* join (ended ≤ T's spawn)
   close a cycle? No: `C` ended before `T` spawned, so everything in `C`'s
   dependency closure (its waits/children, incl. any dedup target) finished before
   `T`'s spawn — `T`, `W`, and the in-flight stack above prefix-spawn are all
   T-related and therefore **not** in `C`'s past. `finish(C)` cannot re-enter them.

This also explains the op#54/op#94 module cycle precisely: op#54/op#94 are concurrent
siblings (both ~160 ms); op#54 (the waiter) ends at 205, op#94 spawns ~160. The
current code cycles because `finish(POST/query)` joins op#54 (ends 205) while op#94
is in-flight. prefix-spawn(POST/query **up to op#94's spawn**, 160) joins only
children ended ≤ 160 — **op#54 (205) is excluded** → no back-edge → no cycle, and
op#94 still gets its correct scaled start. The implementer's Exp-2 ("the cycle
requires the anchor edge; no-anchor SCC finds 0") is consistent: prefix-spawn keeps
the (correct) anchor *start* while removing the (spurious) anchor *join*.

**If prefix-spawn ever still loops, it is a GENUINE data cycle** (a recorded
mutual-wait `A↔B`, both `actWaitJoin`) — which in a completed run (rc=0) should not
exist, and if it does is a real circular dependency to resolve at the source / by a
principled back-edge break, exactly as Erik framed. prefix-spawn does not paper over
it; the existing `inFlight` guard (`:341-345`) would surface it as a *meaningful*
`CycleWarnings` (post-fix, a non-zero count means a real cycle, not an artifact).

### Other hazards (real, must be handled — this is harder than startOf)

prefix-spawn is the correct fix but materially more complex than the 5-line startOf;
the implementation must handle:

- **Partial parent state.** prefix-spawn replays only the parent's *prefix*; it must
  set the child's `simStart` and the parent's `simStart` **without** marking the
  parent `finished` (its full `finish` runs later). The prefix clock must be a
  **local** accumulator (not stored as the parent's finish), so the later full
  `finish(parent)` recomputes from scratch and stays consistent. (It is consistent
  by construction: the prefix is the front of the full replay, and `setStart` is
  idempotent (`:299-304`), so the child's start set early == what `actSpawn` would
  set later → the full finish's `actSpawn` is a no-op.)
- **Double replay.** The parent's prefix is replayed once to anchor + again in the
  full `finish(parent)`. Bounded (out-of-order anchoring is rare — only
  cross-referenced concurrent work), but worth memoizing if a deep
  many-out-of-order-children trace shows cost.
- **Termination / `inFlight` guard.** prefix-spawn recurses up the parent chain (to
  anchor the parent it needs the grandparent's prefix, …). The parent chain is a
  **tree** (acyclic) so it terminates at a root — but the implementation must keep
  the `inFlight` guard live across the prefix's internal `finish()` calls (the
  pre-spawn joins/waits) so a genuine data cycle still breaks rather than recursing
  forever. This is the cycle-guard I flagged missing on the startOf prototype in
  Round 2, carried forward: **whatever lands must terminate**, and prefix-spawn
  inherits termination from the acyclic parent tree + the existing `inFlight` break.

---

## Charge 3 — The fundamental core: stress-testing the seed hypothesis.

**Seed:** `finish()` is triggered from three paths — anchor (`finish(par)`,
`:319`), implicit join (`joinUpTo→finish`, `:353-369`), explicit wait-join
(`actWaitJoin→finish`, `:379-382`) — but only wait-join is cycle-aware (the recorded
back-edge); the anchor over-reaches (full parent finish vs prefix-to-spawn) and
lacks cycle handling. A fundamental fix unifies all three: anchor = minimal scaled
prefix-to-spawn; genuine residual cycles resolved by the one back-edge mechanism.

**Directionally right, with two corrections that sharpen it into the actual fix:**

1. **"Only wait-join is cycle-aware" is not literally true — the `inFlight` break at
   `:341-345` is UNIVERSAL** (every path enters `finish(i)`, which checks `inFlight`
   first). So all three paths already *break* cycles. The accurate statement is
   sharper and more useful: **the anchor and implicit-join edges are TREE edges
   (parent→child) and must never cycle; only the wait-join is a genuine back-edge
   that *can* legitimately close a cycle.** The current anchor breaks that invariant:
   `finish(par)` doesn't just walk the tree, it runs the parent's *full* replay
   including the parent's **post-spawn joins**, dragging a wait-back sibling into a
   tree-anchoring operation — turning a tree walk into a spurious back-edge. So the
   bug is not "the anchor lacks a cycle guard"; it is **"the anchor does far more
   than a tree-anchor — it does work that can only cycle via a back-edge it had no
   business touching."**
2. **Therefore the unification is: make the anchor a pure tree operation
   (scaled prefix-to-spawn, which only ever traverses parent→child→…→spawn and
   pre-spawn joins that provably can't reach the in-flight stack), and leave the
   `inFlight` break as the single mechanism for *genuine* wait back-edges.** After
   the fix the tree operations (anchor, implicit join up to a point) are acyclic by
   construction, and `CycleWarnings` becomes a true signal of recorded mutual-wait
   cycles only — which is the principled outcome Erik wants. This is exactly
   prefix-spawn + the preserved `inFlight` guard; the seed converges on it once the
   "tree-edge vs back-edge" distinction is made explicit.

**Is it complete? One more case to keep honest:** the implicit-join path
(`joinUpTo→finish`, `:365`) can *itself* reach an out-of-order target whose anchor
then needs prefix-spawn — so prefix-spawn must be the anchor used by **all** entries
into `finish` of an unstarted op, not only the `actWaitJoin` entry. That is what
"replace the anchor block (`:317-339`) with prefix-spawn" achieves (the anchor block
is the single choke point all unstarted-op entries pass through). So the fix is one
localized change at the anchor block, and it covers all three trigger paths because
they all funnel through it. Complete for the artifact class; genuine cycles → the
one back-edge break.

### Alternatives considered (none beats prefix-spawn)

- **startOf (Approach 1):** rejected (Charge 1) — wrong counterfactual.
- **Emit re-root (Exp 4):** rejected by the implementer's own data (3474 fallback
  anchors) and on principle (mutates faithful structure; and native cycles too, so
  it's not an emit problem). My Round-1/2 lazy-re-root experience confirms *why*: a
  surgical re-root to a **live** op (my §3.2 lazy fix) anchors cleanly; a wholesale
  call_exec re-root detaches ops from their live anchors and explodes the count.
- **Reorder the replay to avoid out-of-order references:** impossible — cross-
  referenced concurrent work (op#54↔op#94) has no valid topological order; out-of-
  order is inherent, so it must be *handled correctly*, which is prefix-spawn.
- **Keep `finish(par)` but skip its post-spawn joins:** that *is* prefix-spawn,
  phrased differently (stop the parent replay at the target's spawn).

So prefix-spawn is not one option among several — it is the unique fix that is both
counterfactually exact and cycle-free; everything else is either wrong (startOf),
structurally destructive (re-root), or impossible (reorder).

---

## Summary

- **Revised verdict on startOf, under the accuracy bar: REJECTED.** I own the
  reversal — my Round-2 facts were right ("startOf trades accuracy for acyclicity")
  but my disposition ("accept, document") was wrong under Erik's bar. The concrete
  scenario (config-parse→0 saving missed) is the proof. "Well-formed" and "exact in
  baseline" are necessary, not sufficient; the wrong *counterfactual* is the
  unshippable thing.
- **Does prefix-spawn cycle? No — provably not, for the anchor-artifact class.** Any
  cycle-closing waiter waits on the target and therefore ends *after* the target's
  spawn, so the prefix-to-spawn (which joins only children ended *before* the spawn)
  never traverses the back-edge. Children it *does* join ended before the target
  existed and cannot reach the in-flight stack. If a *correct* prefix-spawn ever
  loops, that is a genuine recorded data cycle (resolve at source / principled
  back-edge break via the existing `inFlight` guard), not a number to approximate.
- **Proposed fundamental fix:** replace the anchor block (`replay.go:317-339`) with a
  **scaled prefix-to-spawn anchor** — replay the parent's timeline with the factor up
  to the target child's `actSpawn`, anchor the child at that clock, recurse up the
  acyclic parent chain for the parent's own start, and **stop before the parent's
  post-spawn joins**. Keep the universal `inFlight` break (`:341-345`) as the single
  mechanism for genuine wait back-edges (post-fix, `CycleWarnings>0` means a real
  data cycle, not an artifact). Keep an `OutOfOrderAnchors` counter to preserve the
  diagnostic value the old `FallbackAnchors` had. This is counterfactually exact
  (matches the current `actSpawn` anchoring), cycle-free (proven), and terminating
  (acyclic parent tree + `inFlight` guard).
- **What the owner must decide:**
  1. **Accept prefix-spawn's implementation cost** (partial-parent-state replay,
     possible double-replay, careful flag management) as the price of an exact
     counterfactual — Erik's "accuracy is the bar" implies yes; it is materially more
     than startOf's 5 lines, and that complexity is real, not incidental.
  2. **Affirm the residual-cycle semantics:** after prefix-spawn, any `CycleWarnings`
     is a *genuine* recorded data cycle — to be investigated/resolved at the source
     (or broken by a principled back-edge), never anchored at a wrong time. (In a
     completed run none is expected; the 5 were all anchor artifacts, which Exp-5's
     startOf-prototype zeroing corroborates — prefix-spawn zeroes them too, *and*
     keeps the counterfactual exact.)
  3. **Confirm the shared-replay scope is sanctioned** (already ruled "fix the whole
     system"): this lands in `wcanalyze` and improves native's analyzer identically,
     superseding the design's "reuse native UNCHANGED" premise.
  4. **(Carried)** the `service.start` self-erasure is a separate, real §3.4 gap
     (re-root the availability span out of `service.start` in both sources, add a
     "slow start headlines" assertion) — independent of this replay fix and still
     owed.

Bottom line: Erik's inversion is correct and my Round-2 disposition was wrong;
prefix-spawn (scaled prefix-to-spawn) is the fundamental fix — exact, provably
cycle-free, terminating — and it is the unique alternative that satisfies the
accuracy bar without mutating faithful data or papering over a cycle.
