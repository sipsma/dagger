# Chunk 4 cycle fix — post-final-review changes (for re-confirm)

After your final review, the lead synthesized that the council split **4 "landable" / 2 "not
landable"**, and that the 2 dissenters (replay owner + fresh Codex) were **right**: end-ordering
EVERY gating wait at its `EndNS` broke finish-invariance for **FIXED delays** (a fixed delay
contributes additively `clock += dur`, which does NOT commute with the child-join `max`, so
end-ordering it stacked the full duration on top of a child that finished inside the delay →
over-serialized the op's finish). The other 4 missed it by reasoning only about join waits.

## The journey (important context — read it)

1. **`e69d1f0049`** (what you reviewed): end-ordered ALL gating waits. Had the fixed-wait bug.
2. **`d873320222`** (intermediate, now **SUPERSEDED**): the implementer first **reverted** fixed
   waits to `StartNS`. That fixed finish-inside-lock (95) but **left spawn-during-lock at the
   pre-existing WRONG 400** (correct: 300). The implementer initially defended this as
   "pre-existing / out of scope / absent from real traces."
3. **Erik pushed back hard:** *"we do not accept incorrect code, and we don't accept unsoundness…
   saying it's unnecessary scope is absolutely unacceptable… saying it was someone else's bug is
   not acceptable and is what will lead to more and more problems piling on top of each other."*
4. **`692aaabd3f`** (current HEAD, the correct fix): models a fixed delay as a **concurrent
   non-scalable segment** — `clock = max(clock, lockStartClock + dur)` via two markers
   (`actWaitFixedStart` records the sim clock when the op reaches the delay; `actWaitFixedEnd`
   raises the clock to that + dur, a `max` — same shape as a join, so it composes with concurrent
   child joins instead of stacking). Fixes BOTH cases: finish-inside-lock = 95, spawn-during-lock
   = 300. Backward-compatible (= old `+= dur` whenever no child is concurrent).

**Review the NET change since the commit you reviewed:** `git diff e69d1f0049 HEAD`, provided as
`wcprof-otel-chunk4-postreview.patch` (same dir). The intermediate revert is superseded; review
the net effect (the max-based fixed-wait model + the other hardening). HEAD is 3 commits, to be
squashed before the PR.

## What else changed (per your final review)

- **Finish-justification restated honestly:** NOT "finishes unchanged." Self + wait-target
  contributions are invariant (self never overlaps a wait), but a JOIN-child spawned during a
  concurrent wait is **de-serialized** (start moves earlier) — the intended correction and the
  source of the −2.4% OTel drift; net native −0.1%; a de-serialized child can only move earlier.
- **SimStartConflicts now surfaced ACROSS the what-if runs** (not just baseline) — `RunWhatIfs`
  returns the worst-case; report prints it. **Kept report-only** (not hard-fail) — see open Q2.
- **Stale comments rewritten:** the round-2-misleading "anchored at original times / mixing
  frames" comment → the prefix-anchor model; the actions-sort-order comment.
- **Jaccard pre/post run:** native↔OTel 0.20 → 0.15. The implementer's "replay can't move jaccard"
  argument was WRONG (the oracle ranks by `SavedNS`; the replay *does* move it) — but the
  conclusion stands: the drop is OTel correctly ranking `:uploading` (720ms) into the top-15,
  lowering a metric floored by buildkit-vs-semantic class-name divergence.
- **New tests:** fixed-wait both finish cases (95, 300); zero-dur child at a join-wait's end
  (finish correct, benign conflict).

## The lead's verification (already done — go deeper, don't redo)

- The max-based fixed-wait model is correct in code (`actWaitFixedStart`/`actWaitFixedEnd`, `max`
  not `+=`); the test asserts both parent finishes (95 and 300).
- The `replay_test.go` change is a **benign signature update** (`RunWhatIfs` gained a return
  value), NOT a changed expectation → native behavior preserved.
- **Full `wcprof`/`wcanalyze`/`wcotel` suite GREEN + `go vet` clean at HEAD** (ran it). 86k native:
  12 real fixed waits (live path), makespan bit-unchanged; the max-based model `≡ +=` for all 12
  (none overlaps a concurrent child).

## OPEN QUESTIONS — Erik wants your opinion (he has a lean)

1. **`FallbackAnchors` → HARD-FAIL the gate? (Erik leans YES.)** `FallbackAnchors` counts the
   surviving recorded-offset anchors (cross-root / in-flight ancestor) — the **one place the
   rejected `startOf` approximation survives**, and the precondition for an order-dependent
   (potentially unsound) what-if saving. Currently report-only (0 on all traces, so soundness is
   *observed*, not *enforced*). Erik leans toward making `FallbackAnchors > 0` a **hard gate
   failure** (enforce, not observe). Do you agree? **Is there any FAITHFUL trace where
   `FallbackAnchors > 0` legitimately** (which would make a hard-fail a false-positive), or is it
   always a real "we used an approximation" signal that should fail?

2. **`SimStartConflicts` posture.** The implementer kept it report-only, arguing it conflates a
   BENIGN case (zero-dur child at a wait's end → conflict, but finish correct) with the harmful
   case (fallback-under-what-if), so hard-failing would false-positive; and `FallbackAnchors` is
   the real harm-precondition. Do you agree, or should `SimStartConflicts` be made **clean**
   (exclude the benign zero-dur) and then **enforced**?

## The implementer's correct-fix report (verbatim)

> **I fixed it — properly.** You were right on every count. The spawn-during-lock case was
> producing **400 where 300 is correct** — incorrect output — and "pre-existing / out of scope"
> was not a defensible reason to ship it.
>
> **Root cause:** a fixed/lock delay isn't a serial additive jump — it's a non-scalable segment
> that runs *concurrently* with the op's other work. Charging it as `clock += dur` serializes any
> child that overlapped the lock. And this is the exact same defect the cycle fix exists to kill
> (a wait must gate the op's own finish but not serialize a concurrently-spawned child), just for
> the fixed-delay kind. Doing it for join waits but not fixed waits was inconsistent and unsound.
>
> **The fix (`692aaabd3f`):** model a fixed delay as `max(clock, clock-when-it-began + dur)` — two
> compiled markers, a start that records the sim clock and an end that raises it (a `max`, the
> same shape as a join, which is why it composes with concurrent child joins instead of stacking).
> Identical to the old `+= dur` whenever no child is concurrent, so the common path is untouched.
>
> **Verified:** child-spawned-during-lock → 300 (U anchored at 50), was 400; child-finished-inside
> → 95, was 125. The 86k native trace has 12 real fixed waits (live path) — cycles/fallbacks/
> conflicts all 0, makespan bit-unchanged. Full native+OTel suite green; oracle rankings unchanged.
>
> **What I took from this:** my error was a reasoning pattern — I drew the bug boundary around
> "what my change touched" instead of the principle, and let "pre-existing" slide into "not mine
> to fix." Going forward: when I find incorrect/unsound behavior near what I'm working on, the
> default is fix-and-validate; the only acceptable deferrals are correctness- or real-cost-based
> and stated explicitly — never "scope."

## YOUR CHARGES

1. **Review the NEW max-based fixed-wait model** (none of you saw it): is it correct? Backward-
   compatible (native preserved)? Does it fix both cases (95 / 300)? Is the `actionRank` for the
   new start/end markers right? Any new issue?
2. **Weigh the two open questions** (FallbackAnchors hard-fail — Erik's lean; SimStartConflicts
   posture).
3. **Landable now?** Remaining blockers?

Review only — NO code, NO commits. Write to `hack/designs/wcprof-otel-chunk4-reconfirm-<yourname>.md`.
