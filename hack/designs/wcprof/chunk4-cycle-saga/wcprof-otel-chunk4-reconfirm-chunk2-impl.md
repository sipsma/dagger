# Chunk 4 cycle fix — RE-CONFIRM after the fixed-wait fix (by the Chunk 2 implementer)

**Owning the gap first.** My final review declared finish-invariance "PROVEN — I
could not break it," resting on "every wait does `clock = max(clock, finish(target))`,
hence order-independent." **That premise was false for the model I reviewed**:
`actWaitFixed` did `clock += a.dur` (additive) — I quoted that very line in my own
code-review section and still folded fixed waits into the "all max" claim. All three
counterexamples I tried were JOIN waits (trivially max-commutative), so I never
exercised the additive kind. The replay owner + fresh Codex caught it; Erik forced
the fix. The lesson is mine: a commutativity proof must enumerate the operation kinds,
not assume them. I verified the new model against the patch; analysis only, no code.

## 1. The max-based fixed-wait model (`692aaabd3f`) — correct, and it closes my gap

**Mechanism.** A fixed delay now compiles to two markers (patch lines 151-155):
`actWaitFixedStart` at `StartNS` (rank 3) records `X = clock` into a per-delay slot
(`fixedWaitClock[ref]`); `actWaitFixedEnd` at `EndNS` (rank 0) does `clock =
max(clock, X+dur)`. So a fixed delay is a **non-scalable segment `[X, X+dur]`**,
max-composed like a join instead of stacked additively.

**Both cases verified by hand-tracing the patch** (matching the handoff's targets):
- **child-finishes-inside → 95** (was 125): the lock `[X=5, 5+30=35]` and child c
  (joins at 35) are both maxed at the END → 35, not 35+30; +60 self → 95. The OLD
  additive `+= dur` stacked the 30ms lock on top of c's join → 65 → 125.
- **child-spawned-during → 300** (was 400), U anchored at 50: U's spawn (at 100, < the
  END at 200) anchors U at the pre-lock clock 50; at the END, `max(U-join 150, X+dur
  200) = 200`; +100 self → 300. Additive stacked 150 on top of U's 150 → 300 → 400.

**Backward-compatible — and I can see *why*, not just that it tests green.** When no
child runs concurrently with the lock, nothing raises the clock between the START and
END markers (self never overlaps a wait; no concurrent child join), so `clock-at-end
== X`, and `max(X, X+dur) == X+dur` — **identical to the old `+= dur`**. That is why
the native trace's 12 real fixed waits (none overlapping a concurrent child) leave the
makespan bit-unchanged: the model is `≡ +=` on the common path by construction.

**Counterfactually correct too** (not just the baseline): under a what-if, `X` tracks
the factor (it is the *simulated* pre-lock clock) while `dur` stays fixed (a lock is
not scalable user work) — exactly right. A bonus consequence: scaling a child that
runs *inside* the lock now correctly saves **0** (the lock dominates), where the OLD
additive model would have *over-credited* the scaling (125→110). So the additive bug
corrupted what-if *rankings*, not only baselines — the fix improves both.

**`actionRank` for the new markers is right.** START at rank 3 sits *after* self(1)/
spawn(2) at the same instant, so a child spawned exactly when the lock begins is
anchored at `X` (concurrent) and `X` is recorded after any same-instant self; END at
rank 0 sits *before* self/spawn, so post-lock work is gated. A spawn exactly at the
lock's end is gated (inclusive), consistent with the join boundary. Slot management is
sound: each delay owns a unique slot, `fixedWaitClock` is per-`Simulation` (so parallel
`RunWhatIfs` sims don't collide), and within one op the START-write/END-read never
interleave with another op (a nested `spawnTo(par,·)` hits the in-flight guard). The
`ref`/`dur` field reuse is routed by `kind`, no index collision. **No new issue.**

**Does my finish-invariance proof now hold for BOTH kinds? Yes.** Under this model
*every* wait is a `max`: a join is `max(clock, finish(target))`; a fixed delay is
`max(clock, X+dur)`. With self-segments never overlapping a wait (so reordering a wait
to its end-marker crosses no self) and all wait contributions being `max` (commutative),
the final clock is invariant to the reordering, and a concurrently-spawned child is
de-serialized identically on the prefix and full-finish paths. My proof's *premise* is
now true, so the proof is now sound — for the model I reviewed it was unsound because
I asserted a premise the code did not satisfy.

The patch also **closed the order-independence gap I flagged in my final review**:
`RunWhatIfs` now returns the worst-case `SimStartConflicts` across the counterfactual
sims (patch lines 250, 268, 280) and `report.go` surfaces it (lines 507-513) — so a
what-if-only order-dependence is no longer silent. Good.

## 2. The two gate-posture questions

### Q1 — Hard-fail `FallbackAnchors > 0`? My read: **NO** (against Erik's lean).

`FallbackAnchors` fires for a **cross-root forward reference** (a wait targeting an op
under a not-yet-scheduled root) or an **in-flight ancestor**. The key question Erik
asks — *is there a faithful trace where it legitimately fires?* — is **yes**:
concurrent/overlapping queries in one session (or, for native engine-global,
cross-session singleflight) produce a wait from one root into another, which forces the
recorded-offset anchor. These are **faithful** traces, not emit bugs. The measured
module workload had `FallbackAnchors=0`, but that is data-specific; a concurrent-query
trace would trip it. So **a hard fail would false-positive faithful traces** — and
rejecting a faithful trace is itself a soundness failure (we lose a valid analysis).

Crucially, **the baseline is still exact when `FallbackAnchors > 0`**: at factor 1 the
recorded offset *is* the recorded start, so `SimStartConflicts=0` at baseline (the
cross-root test asserts exactly this). The unsoundness is *only* that the
fallback-anchored classes' **what-if savings** are order-dependent — and that is now
**surfaced loudly** (`what-if start conflicts: N (some savings are order-dependent)`).
So the soundness goal ("no silent wrong answer") is already met by surfacing.
**`FallbackAnchors` is the *precondition* for a localized, flagged what-if
approximation, not a corruption — keep it report-only.** If Erik wants a harder bar,
the correct lever is the *manifested* harm (a what-if `SimStartConflict` paired with a
fallback), not the precondition — but even that rejects faithful concurrent-query
traces for a loudly-flagged, per-class approximation, so I would not.

### Q2 — `SimStartConflicts` posture: keep **report-only**; optionally clean it.

The implementer is right that it currently conflates two cases, and that this blocks a
clean hard-fail:
- **Benign:** a zero-duration child whose end coincides with a gating wait's end is
  join-anchored (pre-wait) then spawn-re-anchored (post-wait) → a counted conflict. I
  verified it is *truly* harmless: a zero-duration child's finish equals its start, is
  ≤ the wait it sits at (absorbed, the test asserts P=300), and self=0 so it never
  ranks in any what-if. Pairs with `FallbackAnchors=0`.
- **Real:** a recorded-offset fallback diverging under a shifting factor — pairs with
  `FallbackAnchors>0`, and is the genuine order-dependent saving.

Because the two are distinguishable by the paired `FallbackAnchors`, the cleanest
refinement is to **exclude the benign zero-dur coincidence from the counter** (so any
`SimStartConflicts>0` means a real order-dependence), which also de-risks a future
accidental conflict masquerading as benign. But I would **still not hard-fail** even
the cleaned counter, for the same reason as Q1: it fires on faithful cross-root
what-ifs. So: **surface across what-ifs (done), optionally clean (nice-to-have), do not
enforce.** The implementer's "count both, document the benign one in a test, report-
only" is acceptable for landing — the test asserting `SimStartConflicts==1` for the
benign case is a deliberate drift-catch, which is fine.

**One concrete suggestion bridging Q1/Q2:** the most useful *enforced* invariant is not
"FallbackAnchors==0" nor "SimStartConflicts==0," but the **pairing**: a
`SimStartConflict` that does *not* pair with a `FallbackAnchor` would be a real bug (an
order-dependence on the normal path, which is supposed to be impossible by
construction). That pairing — "every conflict is either benign-zero-dur or
fallback-backed" — *can* be asserted without false-positiving faithful traces, and it
is the property that actually guarantees the construction holds. Worth a test/assert.

## 3. Landable now? + blockers

**Landable: yes.** The fixed-wait fix is correct, backward-compatible (provably `≡ +=`
on the common path; native bit-unchanged), fixes both cases (95/300) and the what-if
over-crediting, and makes my finish-invariance proof actually hold for both wait kinds.
The order-independence gap I flagged is closed (conflicts surfaced across what-ifs).
The `RunWhatIfs` signature change is a benign return-value add (all four callers
updated). No correctness blocker remains.

**Remaining items (none block correctness):**
1. **Gate posture (decision, not a fix):** my recommendation is *report-only* for both
   `FallbackAnchors` and `SimStartConflicts` (hard-failing either false-positives
   faithful cross-root/concurrent-query traces; the baseline stays exact and the
   what-if approximations are already surfaced). If anything is *enforced*, enforce the
   **pairing invariant** above, not the raw counters.
2. **Optional cleanup:** exclude the benign zero-dur coincidence from `SimStartConflicts`
   so the counter is a pure order-dependence signal.
3. **Design reconcile (carryover):** the "reuse native replay UNCHANGED" premise is
   formally dead — record that the shared replay now carries a soundness fix (anchor
   over-reach + additive-fixed-wait) that also repairs a latent *native* bug.
4. **jaccard (carryover):** the 0.20→0.15 drop is OTel correctly ranking `:uploading`
   into the top-15 — a 2nd-source bucketing effect, not a regression; the implementer's
   earlier "replay can't move jaccard" was wrong (it can, via `SavedNS`) but the
   conclusion stands. Validate by structural invariants + matched-class identity oracle,
   not raw-class jaccard.

## Summary

- **Does the max-based model close the fixed-wait gap? Yes** — a fixed delay is now a
  `max(clock, X+dur)` non-scalable segment, provably `≡ clock += dur` when no child is
  concurrent (native bit-unchanged) and giving the correct 95 / 300 when one is. Every
  wait is now a `max`, so my finish-invariance proof holds for *both* kinds — the
  premise I wrongly assumed before is now actually true.
- **`FallbackAnchors` hard-fail verdict: NO.** It fires on faithful concurrent-query /
  cross-root traces, so a hard fail is a false-positive on valid analyses; the baseline
  stays exact and the per-class what-if approximation is already surfaced loudly. Keep
  report-only.
- **`SimStartConflicts` posture:** keep surfaced (across what-ifs, done) and report-
  only; optionally exclude the (verified-harmless) zero-dur coincidence; enforce the
  *pairing* invariant rather than the raw counter if you want a hard bar.
- **Landable now: yes** — no correctness blockers; the remaining items are a gate-
  posture decision and carryover documentation.
