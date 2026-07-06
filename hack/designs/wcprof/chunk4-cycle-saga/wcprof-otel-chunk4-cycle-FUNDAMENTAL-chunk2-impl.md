# Chunk 4 cycle — FUNDAMENTAL re-examination (by the Chunk 2 implementer)

**Charge:** Erik ruled Approach 1 (`startOf`, recorded-offset anchor) OFF THE
TABLE — it gives a wrong counterfactual; accuracy is non-negotiable. I endorsed it
in Round 2 and rebutted the prefix-spawn alternative ("the partial replay still
joins the cross-referencer and re-enters an inFlight op"). Both are now in question.
I re-examined honestly against `engine/wcprof/wcanalyze/replay.go` in my worktree.
Analysis only; no code, no commits.

**Bottom line up front: I was wrong on both counts.** Prefix-spawn does **not**
cycle (my rebuttal wasn't airtight — it was incorrect), and `startOf` is genuinely
wrong on accuracy. I walk back my Round-2 endorsement. The fundamental fix is the
scaled prefix-to-spawn anchor.

---

## The two concerns are distinct — and the current code already nails one of them

Reading the action loop precisely:

- **In-order anchor (`actSpawn`, replay.go:377-378):** `s.setStart(a.ref, clock)` —
  a child's start is the parent's **scaled** clock at the spawn point. This is
  **already accurate**: when the parent runs less pre-spawn work under a factor, the
  child starts earlier, and that propagates. No problem here.
- **Out-of-order anchor (replay.go:317-323):** when an op `T` is reached before its
  parent `A` has replayed (e.g. a cross-tree waiter `W` hits `finish(T)`), the code
  calls `s.finish(par)` — the parent's **full** finish — to get `T`'s start as a
  side effect of `A`'s `actSpawn(T)`. This is **accurate too** (it replays `A` with
  the factor), but it **over-reaches**: `A`'s full finish also runs `A`'s entire
  `joinUpTo` past `T`'s spawn, and *that* is what re-enters an `inFlight`
  cross-referencer → the spurious cycle (replay.go:341-345 then papers it over with
  the recorded duration — a wrong number).

So the real trade is: the current full-finish anchor is **accurate but cyclic**;
`startOf` is **acyclic but wrong**. The right fix must be **both**. That is exactly
the gap between the in-order anchor (scaled, line 378) and the out-of-order anchor:
the out-of-order anchor should produce the *same* value the in-order `actSpawn`
would — the parent's scaled clock *at the child's spawn* — and nothing more.

## Charge 1 — does prefix-spawn really cycle? No. My rebuttal was wrong.

A "prefix-to-spawn" anchor replays `A`'s scaled self-segments and joins **only up to
`actSpawn(T)`**, then stops, returning the clock as `T`'s start. The question is
whether that prefix can re-enter an `inFlight` op.

**The temporal cutoff makes it impossible (for the spurious anchor cycles).** Let
`t_spawn` = `T`'s recorded spawn time. `joinUpTo(t)` only joins children whose
**recorded** end ≤ `t` (replay.go:356), and a joined child's own replay can only
reach (via its joins/wait-joins) ops whose recorded end ≤ its own end ≤ `t_spawn`.
So **`A`'s prefix-to-`t_spawn` reaches only ops ending ≤ `t_spawn`.**

Now, which op is `inFlight` and could close the loop? The anchor was triggered
because some op `X` needed `T` out-of-order — and the only way to reach `T`
out-of-order is a **wait-join on `T`** (`T` is `A`'s child, so nothing else
`joinUpTo`s it except `A` itself). A wait-join means `X` blocked until `T` finished
(`actWaitJoin` requires `w.EndNS ≥ T.EndNS − ε`, replay.go:165), so **`X` ends at
≈ `T`'s end ≥ `t_spawn`**. Therefore `X` (and every inFlight op on the stack above
`A`) ends **after** `t_spawn` and is **excluded** from `A`'s prefix reach.

I tried hard to break this, including steelmanning my own Round-2 claim ("a
cross-referencing sibling spawned before `T`"): let `X` be `A`'s own child spawned
before `T`. If `X` `actWaitJoin`s `T`, then `X` ends ≈ `T`'s end > `t_spawn` →
prefix cutoff excludes it. If `X` ends ≤ `t_spawn`, then `X`'s wait on `T` (which
ends later) ended early → it is `actWaitNoop` (replay.go:172,385) → `finish(X)`
never recurses into `T` → `X` is not the trigger. **Either branch contradicts a
cycle.** So a prefix-to-spawn anchor cannot close the loop. **My Round-2 rebuttal
conflated "concurrent sibling spawned before `T`" with the real "cross-tree waiter
that ends after `T`'s spawn"; the cutoff at `T`'s spawn is exactly what excludes the
back-referencers. The rebuttal was not airtight — it was wrong.**

This also matches the implementer's empirical Exp 2 ("no-anchor SCC = 0"): the
trace's 5 cycles are **all** spurious anchor over-reach, none genuine. Prefix-to-
spawn removes precisely the over-reach → 0 cycles, with no recorded-duration
paper-over needed for this trace.

## Charge 2 — walk back `startOf`? Yes. It is wrong on Erik's example.

Erik's anchor, traced through the code:

- **TRUE answer (config-parse → 0):** `A` runs config-parse `[0,100]→[0,0]`, spawns
  `T` at clock 0, `T` runs `[0,200]`, `W` unblocks at 200. config-parse **saves
  100ms across the cross-tree wait** and a correct profiler must credit it.
- **Current full-finish anchor:** `finish(T)→finish(A)`; `A` replays *with the
  factor*, so `actSpawn(T)` (line 378) sets `T`'s start = `A`'s scaled clock = **0**
  → `W` at 200 → **correct** (it just risks the over-reach cycle).
- **`startOf` (recorded offset, replay.go:331 generalized):** `T` = `simStart[A] +
  (startNS[T] − startNS[A])` = `0 + 100` = **100**, *factor ignored* → `W` at 300 →
  **saving missed → wrong.**

So `startOf` is not "the necessary price of an acyclic anchor" as I argued in Round
2 — that claim was false. The full prefix is *recoverable accurately and acyclically*
(Charge 1). `startOf` trades away a real, load-bearing counterfactual (cross-tree
savings — which on module loads is most of the interesting signal). **Off the table;
endorsement withdrawn.**

## Charge 3 — the fundamental core: right in spirit, one correction

> *"three finish() paths — anchor, implicit-join, wait-join — but only wait-join is
> cycle-aware; the anchor over-reaches AND lacks cycle handling; unify so anchor =
> minimal scaled prefix-to-spawn, residual cycles handled by the one back-edge
> mechanism."*

- **The three paths are real:** anchor `finish(par)` (replay.go:319), implicit join
  `joinUpTo→finish` (replay.go:360), explicit wait-join `actWaitJoin→finish`
  (replay.go:380).
- **One correction:** "only wait-join is cycle-aware" is imprecise. The cycle-break
  is the `inFlight` guard at the **top of `finish()`** (replay.go:341-345) and is
  **path-agnostic** — the anchor path *does* trigger it (that's where today's
  `CycleWarnings` come from). The true defect is not "the anchor lacks cycle
  handling"; it is that the **anchor's over-reach manufactures *spurious* cycles**,
  and the cycle-break then resolves them with a *wrong* recorded-duration number. So
  the cycle-break isn't missing — it's being abused to paper over an anchor bug.
- **The unification is otherwise right.** Make the anchor **minimal** (scaled
  prefix-to-spawn, producing exactly what `actSpawn` produces in-order), which
  eliminates the spurious cycles by construction; then the **single** cycle-break
  (recorded back-edge, the §1.5 mechanism) is reserved for *genuine* recorded cycles
  — real mutual `actWaitJoin`s, which have no well-defined counterfactual and where
  "assume recorded behavior" is the principled answer Erik already sanctioned. After
  the fix, the in-order and out-of-order anchors are the *same computation* (parent's
  scaled clock at the spawn), which is the real unification.

## The proposed fundamental fix

**Out-of-order anchor of `T` = the parent `A`'s scaled clock at `actSpawn(T)`,
computed by replaying `A`'s prefix (scaled self-segments + `joinUpTo` strictly up to
`T`'s spawn) and stopping there.** Accurate (scaling propagates across cross-tree
waits, matching the in-order `actSpawn`) and acyclic (Charge 1). Genuine residual
cycles → the existing recorded-duration cycle-break.

**Implementation reality (the cost of doing it right):** today `finish()` is
all-or-nothing, and `actSpawn` sets the start as a side effect of the *full* replay.
A correct prefix-to-spawn needs one of:

1. **Resumable parent replay** — process `A`'s actions incrementally, pause at
   `actSpawn(T)` (save clock / `pendCur` / action index), set `T`'s start, resume
   `A`'s remaining actions when `A` is later finished. No duplicated work; the
   cleanest, but it restructures the core loop.
2. **A memoized `spawnClockOf(parent, spawnPoint)`** that re-replays the parent's
   prefix on demand and caches it. Smaller change; some recomputation; must use the
   *identical* factor/join logic as the full replay so the anchored start is
   consistent with `A`'s eventual full finish, and must memoize per spawn-point to
   stay cheap on deep cross-tree chains.

Either is more work than `startOf`; per Erik's ruling, accuracy is the constraint,
so the work is justified. I lean (2) for a minimal, reviewable change, (1) if the
recomputation proves hot.

**Don't assume even this is the whole story — other alternatives:**

- **Discrete-event scheduler (the most fundamental).** Replace lazy `finish()`
  recursion with a global-clock ready-queue: every op starts when its parent's
  sim-clock reaches its recorded spawn offset; cross-tree waits are normal queue
  dependencies. This *eliminates the anchor concept entirely* (no out-of-order
  problem, no over-reach), and genuine cycles are detected as queue deadlocks and
  broken by the same recorded-duration rule. It is the principled model but a
  substantial rewrite of the shared replay (PR #13393). Worth flagging as the "if
  the anchor keeps biting" escalation; prefix-to-spawn is the targeted fix within
  the existing model.
- **Topological / two-pass replay** (replay targets before waiters): defeated by
  genuine cross-tree cycles and a poor fit for the lazy memoized structure — I would
  not pursue it.

## Verification notes / what I could not check

- All line references are my worktree's `replay.go` (Chunk 2 base `b0e7cd9931`); the
  charge's numbers (e.g. 384-388, 441) are from a branch with the `startOf` prototype
  applied — I reasoned about the *mechanism* in the committed code, which is what
  matters.
- I could **not** access the raw 9.3MB trace / native dump. My Charge-1 conclusion
  (prefix-spawn doesn't cycle) is a *proof from the replay semantics + the
  `actWaitJoin` ε-rule*, not from the trace, so it does not depend on that access.
  The implementer's empirical Exp 2/3/4 are consistent with it. If the owner wants
  the prefix-to-spawn fix validated empirically before landing, the implementer
  should prototype it and report cycles/fallbacks/makespan + the Erik-example
  counterfactual (config-parse → 0 must credit 100ms) on the real trace.

---

## Summary

- **Does prefix-spawn actually cycle?** **No.** A back-referencer wait-joins `T`, so
  it ends at/after `T`'s spawn; a prefix-replay stopping at `T`'s spawn only reaches
  ops ending ≤ `T`'s spawn, which provably excludes it (and I could not construct any
  counterexample). **My Round-2 rebuttal was wrong**, not merely non-airtight.
- **Verdict on Approach 1 (`startOf`):** **withdrawn — it is genuinely wrong.** On
  Erik's example it pins `T` at the recorded offset and misses the 100ms cross-tree
  saving. Accuracy is not negotiable; `startOf` sacrifices it. Off the table.
- **Proposed fundamental fix:** out-of-order anchor = **scaled prefix-to-spawn**
  (the parent's scaled clock at the child's `actSpawn`, replaying the parent's prefix
  and stopping there) — accurate *and* acyclic, unifying the in-order and
  out-of-order anchors; the existing recorded-duration cycle-break is reserved for
  *genuine* recorded cycles only. Charge 3's hypothesis is right in spirit (one
  correction: the cycle-break is already path-agnostic; the bug is the anchor's
  over-reach manufacturing spurious cycles, not a missing cycle handler).
- **What the owner must decide:** (1) confirm prefix-to-spawn as the fix and bury
  `startOf`; (2) pick the implementation shape — resumable parent replay vs memoized
  `spawnClockOf` — accepting the added complexity as the price of accuracy; (3)
  confirm the recorded-duration cycle-break is the accepted resolution for *genuine*
  cycles (it is the existing §1.5 behavior); (4) accept that this is a **shared-
  replay change touching native** (the design's "unchanged replay" premise is dead —
  reconcile it); (5) decide scope — targeted prefix-to-spawn now vs. a larger
  discrete-event-scheduler rewrite if anchors keep biting.
