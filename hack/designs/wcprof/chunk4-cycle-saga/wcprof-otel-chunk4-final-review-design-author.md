# Chunk 4 cycle fix — FINAL review (design author), commit e69d1f0049

Review of the *end-ordered gating* reformulation (not the skip-predicate I
reviewed in round 3) against the patch `wcprof-otel-chunk4-hardening-e69d1f0049.patch`
and my worktree's `engine/wcprof/wcanalyze/replay.go`. This is the last gate before
it merges into the shared/validated replay (PR #13393 / native).

## Plain answers up front

1. **Landable?** **Yes — the mechanism is correct and finish-invariant — with one
   blocker: the now-stale `replay.go` "anchored at original times / mixing frames"
   comment (the exact language that misled the council in round 2) is left
   unmodified by the patch and is now *wrong*; it must be rewritten before this
   lands in validated native code.** One gate-policy decision (SimStartConflicts
   hard-fail vs report) and the §6.2/§6.4 jaccard re-spec are also owed but are not
   landing blockers for the fix itself.
2. **Jaccard-bar verdict:** the implementer's *conclusion* is sound — a raw
   `jaccard ≥ 0.80` is the wrong acceptance gate for buildkit-heavy workloads and
   the 0.15 is **not** a cycle-fix regression — but its *supporting argument* ("the
   replay can't move jaccard") is imprecise. The real reason is the §3.5 leaf-I/O
   seam + the 2nd-source class taxonomy, which is the Chunk-3 scope-matching
   reconcile. §6.2/§6.4 must apply the bar to **class-filtered comparable classes**,
   not raw keys; the decisive pre/post-fix check confirms it.
3. **§3.1 self-time faithfulness verdict:** **faithful.** The folded `call_exec`
   (op#250) is **not** under-credited vs native — if anything it is *over*-credited
   by a tiny lookup-time sliver — and the `Host.directory` drift is genuinely
   *attribution* (the §3.5 buildkit-bucketing seam), not error: the work is captured
   and the total agrees.

I verified the suite-passing and basic invariants the lead already checked; below is
the deeper vetting of the new mechanism.

## The mechanism is correct — and finish-invariance is rigorous, not asserted

**End-ordering = the skip-predicate, expressed as action order.** A gating wait is
sequenced at `a.at = w.EndNS` (patch `compileProgram`, the `actWaitJoin`/`actWaitFixed`
cases set `a.at = w.EndNS`); `actionRank` makes gating-wait(0) < self(1) < spawn(2)
< noop(3). So at a spawn's recorded time, a wait whose end is **after** the spawn
sorts after it (not yet applied → doesn't gate); a wait ending **before/at** the
spawn sorts before (gates). That is exactly "a wait gates an action iff it completed
by that action's recorded time" — the skip-predicate I endorsed, but achieved by
*ordering* so a single `advance(op, stopAt)` serves both the full finish (`stopAt<0`)
and the prefix anchor (`stopAt=child`), and the two are byte-identical up to the
spawn **by construction**. This is cleaner and more robust than the skip-predicate,
and it also fixes the round-3 exactness nit: `a.at = w.EndNS` uses the **wait's own
recorded end**, not the target-end proxy (test (d) `own-end-past-spawn` proves the
proxy is retired). Items #1/#2/#3 genuinely collapse into one mechanism.

**Finish-invariance holds — proven, not just measured.** The load-bearing fact:
`SelfSegments` subtracts every wait interval `{w.StartNS,w.EndNS}` from the op
interval (`graph.go:381-398`), so **no self-segment ever lies inside a wait
interval.** Partition an op's self-segments around a wait W into B (entirely before
`W.StartNS`) and A (entirely after `W.EndNS`) — there is nothing in between. Old
order applies W's `clock = max(clock, finish(target))` at `W.StartNS` (after B,
before A); new order applies it at `W.EndNS` (still after B, before A, because A's
segments start `> W.EndNS`). Either way the result is `max(ΣB, finish(target)) + ΣA`
— **identical**. With multiple overlapping waits, `max` commutes, so their relative
reordering is also invariant; with self-segments between *non-overlapping* waits,
each stays between the same two waits. And it holds **under any factor** (the
ordering keys on recorded times, which are factor-independent; only ΣB/ΣA scale,
identically on both paths). So end-ordering changes **only spawn anchoring** (a
spawn during a wait is no longer gated) and **never** an op's finish — which is why
native makespan is −0.1% bit-for-bit. The counterfactual the design depends on
(RunWhatIfs savings, computed from finishes) is therefore **preserved** where the
old anchor was correct and **corrected** only where it was buggy (cycles, frozen
anchors). The OTel −0.3%→−2.4% shift is *throwaway-vs-hardened* (the skip-predicate
still over-served; end-ordering compresses correctly toward native's 5.86 s), not a
before/after-end-ordering change.

**Order-independence is self-checked.** `setStart` keeps first-write-wins but counts
any *disagreeing* overwrite as `SimStartConflicts`; it is **0** on native 86k and
OTel 11k. That is the empirical proof that the "by construction" claim actually
holds in the real graphs — a genuinely good safety invariant to ship with the
change.

**Cycle handling is unified and correct.** `spawnTo` sets `inFlight[par]` during its
prefix `advance`, so a genuine dependency cycle reached *through the prefix*
re-enters `finish` while in-flight → the existing recorded-duration break +
`CycleWarnings` (now documented as "genuine cycle: i's own dependency chain
re-entered it"). Spurious concurrent-wait cycles can't form because end-ordering
keeps the concurrent wait *after* the spawn in the prefix walk. So `CycleWarnings`
becomes a clean unfaithful-emit signal (5→0 both sources) — the §6.1 invariant made
*correct*, not relaxed. The recorded-offset fallback survives only for genuine
inversions (in-flight ancestor / cross-root forward ref), each counted via
`FallbackAnchors` — never silent.

**Test coverage is excellent and addresses every round-3 ask:** config-parse
*correctness* (a, saves 100 ms — the exact thing `startOf` failed), minimal
false-cycle (b, 0 cycles), dual-semantics + order-independence run **both** join
orders (c), the wait-end gating boundary incl. own-end-vs-target-end-proxy (d),
fixed-wait overlap (e), cross-root (f), and a 64-wide fan-out (g). This is the
regression battery I recommended, and more.

*(Correcting my own round-3 prediction: I expected `TestGateFallbackAnchorsReportOnly…`
to break. The lead verified it passes **unmodified** — the repurposed
`FallbackAnchors` still counts the cross-root recorded-offset anchors the test
constructs. I was wrong; the prediction doesn't hold.)*

## Q1 — the jaccard / oracle-bar question (my lead)

**Conclusion: the implementer is right that raw `jaccard ≥ 0.80` is the wrong gate
for buildkit-heavy workloads, and the 0.15 is not a fix regression — but the
*reason* is the §3.5 leaf-I/O seam, not "the replay can't move jaccard."**

- **Claim (a) is imprecise.** The Chunk-2 oracle's `TopBottlenecks` ranks the top-N
  by `RunWhatIfs` **SavedNS** (replay-computed), filtered by a self-time threshold —
  *not* by self-time directly (`oracle.go`, unchanged by this patch). So the replay
  **can** move which classes land in the top-N, hence jaccard. "The replay change
  can't move jaccard" is not strictly true.
- **Claim (b) is the real driver, and it's correct.** The jaccard *floor* is the
  **class-key divergence**: OTel buckets engine/leaf-I/O work by the buildkit span
  name (`:uploading` = filesync, `:stdout`), native by the wcprof semantic class
  (`Host.directory`, `exec.processRun`). Different keys ⇒ not "shared" ⇒ low jaccard,
  **regardless of the replay or this fix.** And this is precisely the §3.5 leaf-I/O
  seam the design *deliberately deferred* (OTel doesn't yet semantically class
  buildkit/leaf-I/O spans). So on a buildkit-heavy workload the top bottlenecks
  *are* the un-semantically-classed leaf-I/O, and a raw all-class jaccard measures
  the taxonomy gap, not ranking faithfulness.
- **Decisive check (run it):** jaccard on the **same** workload **pre vs post fix.**
  If ~identical, the fix is jaccard-neutral and the 0.15 is the taxonomy floor —
  confirming the cycle fix must not be gated on it. The handoff reports drift
  pre/post but not jaccard pre/post; ask for it to close this cleanly.
- **What the oracle SHOULD assert for this source (the §6.2/§6.4 reconcile, building
  on Chunk 3):** apply the convergence bar to **scope-matched, class-filtered
  comparable classes** — `call`/`call_exec`/`lazy`/`exec`/`service`/`session_phase`,
  the work both sources class the same — and exclude the §3.5-deferred buildkit/
  leaf-I/O classes (or, when §3.5 lands, map buildkit span-names → semantic classes
  so the taxonomies align). On the comparable subset the 0.80 bar is meaningful; on
  raw keys it is not. **Crucially, this fix lands on §6.1 (now PASS) + the test suite
  — the jaccard is a §6.2 validation, a separate reconcile — so the jaccard debate
  does not block the fix.**

So: not a rationalization, but stated via the wrong mechanism. Don't gate the fix on
jaccard; re-spec §6.2/§6.4 to a class-filtered bar; run the pre/post check for the
record.

## Q2 — §3.1 self-time faithfulness on the folded `call_exec` (op#250)

**Faithful — op#250 is not under-credited.** op#250's `call_exec` carries the
suppressed-caller wait per §3.1 (ancestor-homing). `SelfSegments` subtracts that
wait interval `[start_wait, 205]` from op#250's self. Native instead has a separate
child `call` op for that caller, interval `[start_CC, 205]`, subtracted from op#250's
self. The chunk-2 argument holds: `start_CC ≲ start_wait` (the native call op starts
at the sub-call invocation, a hair *before* the wait starts, across the cache
lookup), so native subtracts a *slightly larger* interval. ⇒ op#250's self is, if
anything, **slightly larger** in OTel (a sub-ms lookup sliver), never smaller. No
under-crediting. This is consistent with the spot-check ("pure user `call_exec`
classes match native exactly"; the folded case differs only by that sliver). And the
end-ordering reinforces it: the suppressed-caller wait, sequenced at its end (205),
gates op#250's **finish** (≤ op#250's end) but **not** op#250's concurrent spawns —
so concurrent sub-work isn't spuriously serialized into op#250 either.

**"Drifts are attribution not error" — correct.** `Host.directory` native 781 ms vs
OTel `call_exec` 6.8 ms: OTel puts the 720 ms of filesync work in the child
`:uploading` buildkit span; native folds it into the `Host.directory` class. The
work is **captured** by OTel (720 + 6.8 ≈ native's 781), just **bucketed**
differently. That is attribution (which class), not error (no work lost or
double-counted), and it is the **same §3.5 leaf-I/O seam** that drives the jaccard —
so the two findings are one root cause, not two. Faithful, with a known, designed
bucketing gap.

## Finish-invariance verdict (does it change a counterfactual the design relies on?)

**No regression; only the intended correction.** End-ordering provably preserves
every op's finish (the SelfSegments-subtracts-waits invariant), so makespan and all
finish-derived what-if savings are unchanged wherever the old anchor was already
correct. It changes spawn anchoring only — which is exactly the bug — making the
cross-tree counterfactual *correct* (config-parse 100 ms; `ModuleSource.asModule`
save==self, structurally matching native). The design's counterfactual model is
preserved and corrected, not altered.

## Doc / code reconciles this forces (specify; I do not edit)

1. **[BLOCKER — code comment in validated native code] Rewrite `replay.go:29-34`.**
   The patch updated the "waits:" bullet but left the "*Roots are chained … mixing
   frames would corrupt the schedule because ops reached through cross-tree wait
   targets are anchored at original times when their own root has not been replayed
   yet*" comment unmodified. That is now **false**: cross-tree targets are anchored
   by replaying the producer's prefix **with the factor** (counterfactual frame);
   recorded-offset survives only as the rare, counted in-flight/cross-root fallback.
   This is the exact language that misled the council in round 2 — leaving it in PR
   #13393 ships wrong documentation. Rewrite to the prefix-anchor model.
2. **"Reuse the validated native replay UNCHANGED" premise → dead.** Replace with
   "reuse the native replay, fixing the genuine shared bug it surfaced (Chunk 4: the
   anchor over-reach), landed in shared `wcanalyze`."
3. **§3.1 ↔ replay interaction note.** A suppressed-caller wait homed on the
   ancestor's `call_exec` gates the ancestor's **finish** but not its concurrent
   **spawns** — now *enforced by construction* by end-ordering (the wait sequenced at
   its end sorts after any earlier spawn). §3.1's ancestor-homing stays as designed.
4. **New `start-conflicts` (`SimStartConflicts`) §6.1 signal.** Add it as the
   order-independence invariant ("must stay 0"). **Decide its disposition:** it is
   currently *report-only* (surfaced in gate + report, no violation). Given it is a
   correctness invariant, I recommend the §6.4 standing gate **alert/fail on any
   non-zero** (it would catch a real residual order-dependence), while accepting that
   the rare in-flight/cross-root fallback corner is the one place it could
   legitimately trip — so at minimum it must never be ignored. Make this an explicit
   decision, not a default.
5. **`FallbackAnchors` repurposed (not retired).** It now counts genuine inversions
   (in-flight ancestor / cross-root forward ref), and `PrefixAnchors` is the new
   informational normal-path counter. Update §6.1's description accordingly.
6. **§6.2/§6.4 jaccard bar → class-filtered comparable classes** (Q1) — the
   buildkit/leaf-I/O §3.5 seam must be excluded or mapped; raw all-class jaccard is
   not the gate for exec workloads.

## Blockers vs watch-items

**Blockers (before merge to validated native):**
- Rewrite the stale `replay.go:29-34` cross-tree-anchor comment (#1 above).
- Decide the `SimStartConflicts` gate disposition (hard-fail/alert vs report-only) —
  don't ship a "must stay 0" invariant as a silent metric without a conscious call.

**Watch / non-blocking:**
- **Perf at scale:** `spawnTo` re-walks a parent's prefix per out-of-order reference
  (and again at the parent's full finish). Bounded in practice — child *finishes*
  are memoized (`finished[]`), so a re-walk is O(parent's own actions × references),
  not O(subtree) — and measured negligible (86k ops 0.28 s; 64-fan-out fine). But
  the replay's stated target is multi-million ops; if a pathological high-fan-out op
  appears, memoize the per-(op,factor) prefix clock. Flag, don't block.
- **Run the pre/post-fix jaccard** on the module workload to close Q1 on the record.
- Fold the doc reconciles (premise, §3.1 note, start-conflicts, FallbackAnchors
  description, jaccard bar).
- **Separate, still owed:** the §3.4 `service.start` self-time erasure (round 2) —
  not touched by this change.

## Bottom line

The end-ordered gating reformulation is the right fundamental fix and a better one
than what I reviewed in round 3: it is **finish-invariant** (rigorously — self
never overlaps a wait, so the wait's `max` lands in the same place), **order-
independent by construction** (one `advance` interpreter; `SimStartConflicts=0`
proves it), it **corrects** the cross-tree counterfactual (config-parse 100 ms)
while **preserving** native makespan bit-for-bit, kills the false cycles on both
sources, threads the wait's own recorded end (retiring the proxy), and ships an
excellent regression battery covering every round-3 concern. §3.1 self-time is
faithful (the fold doesn't under-credit; the `Host.directory` drift is the §3.5
attribution seam, not error). The jaccard push-back lands on the right conclusion
(don't gate the fix on raw 0.80) via a slightly wrong argument (the replay *can*
move jaccard; the §3.5 taxonomy floors it) — re-spec §6.2/§6.4 to a class-filtered
bar and run the pre/post check. **Landable once the stale `replay.go:29-34` comment
is rewritten and the `SimStartConflicts` gate disposition is decided.**
```
