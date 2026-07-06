# Chunk 4 cycle fix — re-confirm review (design author), `git diff e69d1f0049 HEAD`

Net change since the commit I reviewed: the max-based fixed-wait model
(`692aaabd3f`), the rewritten comments, the honest finish-restatement, and the
`whatIfConflicts` plumbing. Verified against the patch and `replay.go`.

## First — owning my error

My final review asserted finish-invariance was "PROVEN." It was not. My proof wrote
only `clock = max(clock, finish(target))` for "gating waits" and generalized from the
JOIN case to *all* gating waits without checking the algebra of the other case:
`actWaitFixed` did `clock += dur` (additive). End-ordering an additive contribution
past a child-join `max` stacks the duration on top of the child (`max(c,f)+dur` ≠
`max(c+dur,f)`), over-serializing the finish. The replay owner and fresh Codex were
right; the four of us who blessed it (me included) reasoned only about the
max-commutative case. The lesson is concrete: when proving an invariant over a
`switch`, enumerate every arm's algebra — don't pattern-match one arm and quantify
over "all." The restatement below is the honest version, and the `692aaabd3f` fix is
what actually makes the clean invariant true.

## Charge 1 — comments honest? finish-justification honest and complete?

**Comments: correct and honest.**
- `replay.go:38-45` (the round-2-misleading "anchored at original times / mixing
  frames"): now states the **prefix-anchor** model — out-of-order ops are anchored
  by replaying the producer's timeline up to their spawn *under the same factor*, so
  the start tracks the counterfactual; recorded-offset only on a genuine inversion,
  counted. This is the correct model and the misleading language is gone. ✓
- Actions-sort comment + `action.at` comment + the `actWaitFixedStart/End` const
  comment: all describe the new mechanism accurately (max-gate = JOIN or fixed-end at
  recorded END applies first; fixed-start/noop at start, non-gating). ✓
- `actionRank`: `actWaitJoin, actWaitFixedEnd → 0`; self → 1; spawn → 2;
  `actWaitFixedStart, actWaitNoop → 3`. Correct: the fixed-end gates like a join; the
  fixed-start only records the clock, so it must sort *after* a same-instant spawn so
  that spawn stays concurrent. ✓

**Finish-justification: honest and complete.** The restatement — "self + wait-target
contributions invariant (self never overlaps a wait); a JOIN-child spawned during a
concurrent wait is *de-serialized* (start moves earlier); net native −0.1%, OTel
−2.4%; a de-serialized child can only move earlier" — correctly retracts "unchanged."
I checked it is *complete* by enumerating every source of finish movement old→new:
1. self-segments — invariant (never overlap a wait, scaled identically);
2. JOIN-wait max — commutes under the reorder; only moves if the *target's* finish
   moved, which is case 4 propagating;
3. fixed-wait — now `max(X+dur)` (X = clock at lock-start); X can only *decrease* if
   an upstream child de-serialized, so this only moves finishes *earlier*;
4. child-spawn de-serialization — the intended correction, strictly earlier.
Every arm is either invariant or monotone-earlier, so "can only move earlier" is
exactly right and there is no fifth source. With the fixed wait now also `max`-shaped,
my original partition argument is *finally* valid for the finish — which is the point:
`692aaabd3f` didn't paper the bug, it made the system match the clean invariant I'd
claimed prematurely. The −2.4% is the de-serialization correcting spurious
serialization (native replay is −0.1%, so the model is sound; the OTel gap is the
2nd-source graph, not over-correction — the `asModule save==self` structural match to
native is good evidence).

## Charge 3 — the max-based fixed-wait model: correct, backward-compatible

Verified the mechanism and both cases:
- **child-finishes-inside** (O self[0,5]+[40,100], c[5,35], lock[10,40] dur30):
  self→5; spawn c@5; fixed-start@10 records X=5; fixed-end@40 first `joinUpTo(40)`
  joins c → `max(5,35)=35`, then `max(35, X+dur=35)=35`; self[40,100]→**95**. (Additive
  gave 65→125.) ✓
- **child-spawned-during** (P self[0,50]+[200,300], U@100, lock[50,200] dur150):
  self→50; fixed-start@50 records X=50; U-spawn@100 anchors at clock=50 (fixed-end not
  yet reached) → **U@50**; fixed-end@200 → `max(50,200)=200`; self[200,300]→**P=300**.
  (Serializing U behind the lock gave 400.) ✓
- **Backward-compatible:** when no child is concurrent with the lock, nothing changes
  the clock between start and end, so `max(X, X+dur)=X+dur` — identical to the old
  `+= dur`. The 12 live fixed waits on the 86k native trace are all in this case →
  makespan bit-unchanged, which the lead confirmed. ✓
- **Re-entrancy/slot safety:** each fixed wait gets a unique compile-time `slot`
  (`numFixedWaits++`), `fixedWaitClock` is per-`Simulation`, and a slot is touched
  only by its own op's start/end actions — so nested locks and recursive `finish`
  don't collide. A prefix walk (`spawnTo` stopping mid-lock) records X but skips the
  end max; the later full `finish` re-walks and re-records the same X before applying
  the end, so the partial state is harmless. ✓

It is the *same principle* as the cycle fix (a wait gates the op's finish but must not
serialize a concurrently-spawned child), now applied to the fixed-delay kind — so it's
the consistent fix, not a special case. This is the right call and Erik was right to
reject the StartNS-revert (which only fixed finishes-inside, leaving spawn-during at
the wrong 400).

## Charge 2 — gate policy

### Q1: `FallbackAnchors > 0` → HARD FAIL? **Agree with Erik's lean: yes.**

`FallbackAnchors > 0` means the replay used the **recorded-offset (startOf-style)
approximation** for ≥1 anchor — the exact model Erik ruled off the table as a wrong
counterfactual. Design-gate reasoning:
- **It is always a real "we approximated" signal, never spurious.** Every fallback
  arm is either a genuine approximation (cross-root forward ref, in-flight ancestor)
  or malformed data (parent doesn't spawn its claimed child) — all of which *should*
  fail. There is **no faithful trace where `FallbackAnchors > 0` is definitely
  sound**, so a hard-fail has **zero false-positives in the soundness sense**.
- **Observing is insufficient; you must enforce.** `whatIfConflicts == 0` does *not*
  prove the corner is harmless — it only means none of the *tried* factors exposed it;
  an untried factor could. So the soundness guarantee has to key on the *precondition*
  (a fallback exists), not on whether a sampled factor happened to trip it.
- **Free in practice.** It's 0 on every real trace post-fix, so enforcing costs
  nothing now and prevents a future trace from silently shipping an order-dependent
  saving.
- **One honest caveat + the principled end-state:** the cross-root forward-ref arm
  *can* arise on a faithful trace (concurrent roots). Hard-failing rejects that trace
  wholesale even though only its cross-root classes are approximate. That's
  acceptable — failing loud beats a silent wrong saving — but it flags the *last
  remaining anchor gap*: the cross-root target could itself be anchored by replaying
  its own root's prefix on demand, eliminating the fallback entirely (the same
  prefix-replay principle, across roots). So hard-fail now, and treat the cross-root
  anchor as the next fundamental fix rather than a permanent accommodation.

### Q2: `SimStartConflicts` posture — **keep report-only** (the implementer is right)

Given Q1 hard-fails on `FallbackAnchors`:
- The **harmful** `SimStartConflicts` case *by definition* pairs with
  `FallbackAnchors > 0` (a recorded-offset anchor disagreeing under a factor — the
  `TestCrossRootAnchor` even asserts "a conflict must pair with a fallback anchor").
  So Q1 **already enforces** every harmful case; a `SimStartConflicts` hard-fail adds
  no soundness coverage.
- The **benign** case (zero-dur child whose end coincides with a gating wait's end:
  join-anchored before its spawn re-anchors it; finish provably absorbed) would
  **false-positive** a hard gate. The patch handles it exactly right — documents it
  and pins it with `TestZeroDurChildAtJoinWaitEnd` asserting `== 1`, so a future
  change that removes it updates the test deliberately.
- Keep it **report-only as defense-in-depth**: it's the self-check of the
  order-independence claim, and would catch a *new* order-dependence that somehow
  doesn't go through the fallback path. Making it "clean + enforced" (excluding the
  zero-dur case) is possible but redundant with Q1 — not worth the extra
  classification code.

Net gate spec: **hard-fail `FallbackAnchors > 0`** (and existing `CycleWarnings > 0`,
`self > makespan`); **report `SimStartConflicts` and `whatIfConflicts`** (the latter
is a good addition — it names *which* savings are order-dependent for the human,
even though Q1 already blocks the trace).

## Doc reconciles — what changes vs my prior spec

- **Premise dead** — unchanged.
- **`replay.go` cross-tree-anchor comment** — now *implemented* correctly; the design
  doc should describe the prefix-anchor model (no longer an open blocker).
- **§3.1 ↔ replay note — generalize.** There are now **two** concurrency-preservation
  mechanisms: JOIN waits (end-ordered) *and* fixed/lock waits (max-segment). The note
  should read: "a wait — join *or* fixed/lock — gates the op's finish but never
  serializes a concurrently-spawned child," enforced by end-ordering and the
  max-segment model respectively.
- **§6.1 gate — record the enforcement decision:** `FallbackAnchors > 0` is now a
  **hard failure** (not report-only); add `whatIfConflicts` (order-dependent-savings,
  report) and `SimStartConflicts` (report-only, benign-case documented). Document the
  **fixed-delay max-segment** as replay semantics (locks are concurrent non-scalable
  segments, modeled as `max`, not additive).
- **§6.2/§6.4 jaccard — the empirical result strengthens the reconcile.** Native↔OTel
  jaccard went **0.20 → 0.15** *because of* the correctness fix (OTel now ranks
  `:uploading` 720 ms into the top-15, pushing a tiny class out). A correctness
  improvement **lowering** the acceptance metric is itself proof the raw all-class bar
  is wrong; the bar must be class-filtered to comparable classes (the §3.5
  buildkit-vs-semantic divergence is the floor). It also confirms my correction that
  the replay *does* move jaccard (the implementer's "can't" was wrong, now conceded).

## Verdict

- **Comments/justification honest now?** Yes. The comments describe the real
  prefix-anchor + max-segment model; the finish-restatement correctly retracts
  "unchanged" and is complete (every movement source is invariant or
  monotone-earlier).
- **`FallbackAnchors` hard-fail (design-gate lens)?** **Agree — hard-fail.** It is
  always a real approximation signal with no sound-trace false-positives;
  `whatIfConflicts==0` can't prove soundness, so enforce on the precondition; it's
  empirically 0 so it's free; the cross-root corner is the next fundamental fix, not
  a reason to relax.
- **`SimStartConflicts` posture?** **Report-only.** Its harmful case is already
  enforced via Q1 (it pairs with `FallbackAnchors`); its benign zero-dur case would
  false-positive; keep it as documented defense-in-depth + the new `whatIfConflicts`
  as the human-facing "these savings are order-dependent" line.
- **Max-based fixed-wait model correct?** Yes — fixes both cases (95 / 300),
  backward-compatible (`≡ += dur` with no concurrent child; 12 live waits
  bit-unchanged), markers' ranks correct, slot mechanism re-entrancy-safe. It's the
  same concurrency principle as the cycle fix, applied consistently.
- **Landable now + blockers?** **Landable.** No code blockers remain — the two I
  raised last round are resolved (the stale comment is rewritten correctly; the
  `SimStartConflicts` disposition is now a conscious, defensible decision). The only
  remaining *action* is the gate-policy wiring Erik is deciding: make
  `FallbackAnchors > 0` a hard gate failure in `wcotel/gate.go` (currently it's
  surfaced but, per the e69d1f0049 review, not a violation) — that's the one change I'd
  require before merge, and it's a few lines. Squash the 3 commits, fold the doc
  reconciles, and it's ready for the PR. Separate, still owed: §3.4 `service.start`.
```
