# wcprof × OTel — FINAL cycle-fix review (replay owner)

**Reviewer:** replay owner (Chunk 1; I know `wcanalyze/replay.go`). This is the
last gate before `e69d1f0049` lands in the validated native replay (PR #13393).
I verified the new **end-ordered gating** mechanism against the patch line-by-line
and stress-tested finish-invariance hardest. No code, no commits.

## Headline verdict

**NOT quite landable as-is — one concrete finish-invariance hole to close first;
otherwise the mechanism is correct, well-tested, and cleaner than the
skip-predicate I reviewed.** The hole is narrow and likely empirically negligible
(consistent with the reported native −0.1%), but it is a *proof gap in the
validated native replay*, which is exactly what this review exists to catch.

- **Finish-invariance: AIRTIGHT for `actWaitJoin`** (the cycle's waits) — I tried
  to break it and could not.
- **Finish-invariance: has a CONCRETE COUNTEREXAMPLE for `actWaitFixed`** (fixed
  delays / lock waits). Moving a fixed delay to `EndNS` can change an op's finish,
  because a fixed delay contributes **additively** (`clock += dur`, replay.go
  `case actWaitFixed`) and addition does **not** commute with the max-joins. See §1.
- The new mechanism resolves my round-3 in-order-asymmetry concern (gating-at-end
  lives in the one shared `advance`, so both paths are fixed) — genuinely better
  than the skip-predicate. Credit where due.

## 1. Finish-invariance — the load-bearing claim

The implementer's argument: end-ordering changes only spawns-during-waits;
parent finishes are unchanged "because self segments never overlap a wait." I
verified the self-non-overlap (graph.go:386-391 subtracts every `{w.StartNS,
w.EndNS}` from the op interval). **But self-non-overlap is necessary, not
sufficient.** The full requirement is: *every wait contribution must commute with
the child-joins (`max`) it can be reordered past.* That holds for one wait kind
and fails for the other:

**`actWaitJoin` — airtight.** Its contribution is `clock = max(clock,
finish(target))` (replay.go `case actWaitJoin`). Moving it from `StartNS` to
`EndNS` reorders it only past **spawns** (which don't touch op's clock — they
`setStart` a child) and other **joins** (also `max`). `max` is commutative, and no
self sits inside the interval, so the op's final clock is identical; only
children spawned during the wait get a different — and now *correct*, ungated —
start. I could not construct a finish-changing case. ✔ (And this is precisely the
cycle's wait kind, so the *cycle fix itself* is invariance-safe.)

**`actWaitFixed` — broken, with a counterexample.** A fixed delay contributes
`clock += a.dur` (additive), and the patch moves it to `EndNS` too (`case
w.Target == nil: a.at = w.EndNS`). Addition does **not** commute with a `max`-join.
Concrete graph:

```
O[0,100], self where free.  child c[5,35] (c is O's child, self 30).
fixed-delay lock wait F over [10,40] (dur 30, target=nil → actWaitFixed).
(SelfSegments subtracts both c and F, so O's self is [0,5]+[40,100].)
```
- **Old (F at StartNS=10):** self@0→clock 5; spawn(c)@5 (c.start=5); F@10 `+=30`→clock 35; self@40 first `joinUpTo(40)` joins c (`finish(c)=35`, `max(35,35)=35`) then `+=60`→ **finish 95**.
- **New (F at EndNS=40):** self@0→5; spawn(c)@5; at the F action `joinUpTo(40)` joins c first (`max(5,35)=35`) **then** F `+=30`→65; self@40 `+=60`→ **finish 125**.

`finish(c)` is 35 on both paths (c.start=5 either way), so this is **not** a
spawn-during-wait difference — it is a genuine **finish change, 95 → 125**, from
the additive `+=30` stacking *after* c's join instead of overlapping it. The
recorded truth is ~100 (O blocked on the lock until 40 while c ran concurrently
to 35, then self), so old (95) was the better approximation and **new
over-serializes by the lock duration.**

Why the proof missed it: "waits contribute via `max`" is true for join-waits and
silently false for fixed delays. The implementer's own regression test covers "a
fixed wait overlapping a **spawn**" (which *is* safe — a spawn doesn't change op's
clock) but **not** a fixed wait whose interval contains a child **join**, which is
the breaking case.

**Is it empirically real?** The reported native −0.1% (unchanged) means no
fixed-delay-with-concurrent-child-join occurred on the 86k native trace — plausible
(lock waits with a child finishing inside them are uncommon). But "empirically
−0.1% on one trace" is not "provably invariant," and this lands in the validated
native analyzer. **Recommendation (pick one), required before merge:**
- **(a) Minimal & invariance-preserving (my lean): leave `actWaitFixed` at
  `StartNS`.** Fixed delays are *not* the cycle cause (the rings are all
  `actWaitJoin` singleflight waits, per the implementer's own RING extraction), so
  moving them buys only fixed-delay spawn-gating consistency at the cost of the
  one finish-invariance hole. Keeping them at `StartNS` makes the proof airtight
  (only `actWaitJoin` moves, only `max` commutes) and still kills every cycle. The
  residual fixed-delay spawn over-serialization is pre-existing, smaller, and not a
  cycle.
- **(b) If fixed delays must move:** model the overlap explicitly (e.g. charge only
  the part of the delay not covered by joined children, or treat it as `max(clock,
  fixedEnd-in-sim)` rather than `+=dur`), and add the join-overlap regression test.
  More work, and it changes native semantics more — I would not do this for the
  cycle fix.

I recommend (a): it's a one-line revert of the `case w.Target == nil: a.at =
w.EndNS` back to `a.at = w.StartNS`, restoring provable native invariance while
fully fixing the cycle.

## 2. `actionRank` tie-break — correct

`actWaitJoin/actWaitFixed = 0 < actSelf = 1 < actSpawn = 2 < actWaitNoop = 3`.
At an equal recorded instant: a gating wait (rank 0) applies before a self or
spawn at the same time, so `waitEnd == spawn ⇒ gated` (inclusive boundary,
defensible/conservative) and a self-segment starting exactly at a wait's end is
gated (builds on the post-wait clock) — both correct. The `actWaitNoop` at rank 3
is immaterial (it does nothing; it's only a join action point and stays at
`StartNS`, unchanged from before). ✔ The boundary is exactly covered by the
landed test (d) "wait-end gating boundary."

## 3. `spawnTo` / `advance` refactor — terminates, fallbacks counted & rare

- **Termination:** `spawnTo` recurses up the finite parent chain; `advance`'s
  inner `finish`/`finish` calls are memoized or hit the in-flight cycle-break
  (replay.go `if s.inFlight[i]`), and the prefix walk returns at the target's
  spawn. So every path bottoms out. The 0.28 s / 86k-op measurement + the fan-out
  test corroborate no blow-up. ✔
- **Fallback corner:** the recorded-offset `fallbackAnchor` survives only for
  par-in-flight (genuine inversion) and cross-root forward references, and each is
  counted (`FallbackAnchors`). On the real traces it is **0** (18→0, 11→0) — the
  old "fallbacks" were over-reach artifacts, and genuine inversions simply don't
  occur on these traces. So the one place the `startOf`-shaped recorded offset
  still lives is genuinely rare and, at 0 occurrences, counterfactually immaterial
  here. ✔ (It *is* the right home for it — parallel to the existing cycle-break.)

## 4. `SimStartConflicts = 0` — a solid order-independence proof, conservative

It is a **real** proof, not a vacuous one: the in-order spawn re-invokes
`setStart` on an op the prefix already anchored (replay.go:349-360), so a
disagreement *is* detected (`s.simStart[i] != v`). So "0 on baseline" means every
double-anchored op matched across paths — exactly the order-independence claim. Two
honest caveats:
- It only *catches* a disagreement when an op is anchored by **both** paths. An op
  anchored once is trivially consistent (and the end-ordering makes the single
  value the path-independent one anyway), so this isn't a gap.
- The one residual it would legitimately surface is the **cross-root** fallback
  *under a what-if that shifts the root* (recorded offset vs `Run`'s chained start
  — the implementer flags this in the `par < 0` comment). So **verify
  `SimStartConflicts` stays 0 across the what-if runs, not only the baseline** —
  the reported 0 reads as baseline-only; a non-zero under some factor would be the
  known cross-root corner, which should then be confirmed bounded.
- Minor: a **zero-duration child spawning exactly at a gating wait's end** can be
  anchored by the `joinUpTo` "never-anchored" branch *before* its own spawn action,
  producing a `SimStartConflict` that is **benign** (the zero-duration child's
  finish can't exceed the wait it sits at, so the op's finish is unchanged). Worth
  a one-line test so a future non-zero conflict from this source isn't mistaken for
  a real regression. (Doesn't occur on the current traces → 0.)

The gate/report wiring is correct but **report-only** (it's a regression metric,
not a `violations` entry). Given the cross-root what-if corner can legitimately
trip it, report-only is defensible — but consider **hard-failing on a *baseline*
`SimStartConflicts > 0`** (where there is no shift, so any conflict is a real bug),
while tolerating/annotating what-if conflicts. Not a blocker.

## Shared questions

**Jaccard-0.80 pushback — sound, with a precision fix.** The implementer's
phrasing ("jaccard ranks by self-time, the replay can't move it") is imprecise —
the oracle ranks top-N by `SavedNS` (replay-computed, oracle.go `TopBottlenecks`).
But the *substance* is right: the low jaccard is **class-set disjointness from
loader-level naming**, not ranking. OTel buckets engine work under buildkit
span-names (`:uploading`/`:stdout`) while native uses semantic classes
(`Host.directory`/`exec.processRun`), so the class *keys* differ before any
what-if runs — the replay change provably cannot reconcile them, and the
implementer's own `Host.directory` 781 ms (native) vs `call_exec` 6.8 ms +
`:uploading` 720 ms (OTel) shows the **total work agrees, only the bucket label
differs.** So holding a buildkit-heavy exec workload to jaccard ≥ 0.80 against
native mismeasures a deliberately-second-source. Agreed: jaccard is the wrong
gate here; a value/total-based or class-name-reconciled comparison is what's
needed (this is the same scope/naming mismatch I flagged at Chunk 3). **Not a
cycle-fix regression.**

**OTel −2.4% "correct compression" — I buy it.** `save == self` for
`ModuleSource.asModule` on *both* sources is the right structural signature (the
class is fully on the critical path), and the throwaway's `save 376 ms > self
258 ms` was literally impossible-without-over-serialization (a class can't save
more makespan than its own self-time unless the model spuriously serializes work
behind it). Removing that false serialization shortens the OTel makespan toward
actual (5.96 s vs the throwaway's 6.09 s, against native 5.86 s), so −2.4%-vs-actual
is the *correct* direction, not a regression. I'd still **watch** that −2.4% (it's
wider than native's −0.1%, reflecting OTel's structural span-population differences)
but it's within a small band and trending right.

**§3.1 suppressed-caller-fold self-time (op#250) — real, separate, flag it.** Yes,
ancestor-homing can **under-credit** op#250's self-time vs native: §3.1 puts the
suppressed caller's concurrent wait on op#250's `call_exec`, and `SelfSegments`
subtracts that wait interval from op#250's self — but op#250 was *doing concurrent
work* during it (it spawned op#251), so its self is reduced below its true work,
where native (separate per-caller `call` op) is not. This is a **loader/§3.1
attribution residual, not a replay-fix concern** — the replay change is correct and
doesn't touch self-time (loader-computed). It's bounded (op#250's concurrent self
during the wait is small — coordination, not heavy work) and the implementer's
spot-check confirms self matches native *exactly where there's no fold*. So:
out of scope for *this* fix, but a standing §3.1 faithfulness item to track
(same root as the cycle: ancestor-homing conflating concurrency).

## Remaining blockers / watch-items

1. **BLOCKER: the `actWaitFixed` finish-invariance hole (§1).** Resolve before
   merge — I recommend reverting fixed delays to `StartNS` (one line; restores
   provable native invariance; still kills every cycle). If kept at `EndNS`, add
   the fixed-wait-overlapping-a-**join** regression test and prove the additive
   stacking is bounded.
2. **Verify `SimStartConflicts == 0 across the what-if runs**, not just baseline
   (the cross-root corner). Add the benign zero-duration-child-at-wait-end test.
3. **Native per-test green:** the handoff says `replay_test.go` is unmodified and
   passes and the full suite is green — good; for the validated replay I'd want the
   makespan-unchanged claim reproduced from a clean tree per-test, which the `−0.1%`
   already evidences. (If §1 is resolved via (a), invariance is provable, not just
   measured.)
4. Consider hard-failing the gate on a **baseline** `SimStartConflicts > 0`.
5. Standing (not this fix): the §3.1 self-time under-credit; the jaccard/scope
   instrument for buildkit-heavy oracles.

## Bottom line

**The end-ordered gating reformulation is the right mechanism — cleaner than the
skip-predicate, order-independent by construction, and the cycle fix (`actWaitJoin`
at `EndNS` + prefix-stop `advance`) is finish-invariant and well-tested.** It is
**not landable in the validated native replay as written** only because moving
**fixed delays** to `EndNS` breaks finish-invariance for the
fixed-delay-overlapping-a-child-join case (concrete counterexample, 95 → 125;
empirically masked at native −0.1% but a real proof gap). **Close that — preferably
by leaving `actWaitFixed` at `StartNS` — and confirm `SimStartConflicts` across
what-ifs, and it is landable.** Finish-invariance verdict: **airtight for the
cycle's `actWaitJoin`; counterexample for `actWaitFixed`.**
