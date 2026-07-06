# wcprof × OTel — cycle fix re-confirm (replay owner)

**Reviewer:** replay owner. I caught the fixed-wait finish-invariance bug in my
final review; the implementer fixed it via a **max-based** model I hadn't seen
(and had argued against). I verified the net change `e69d1f0049..692aaabd3f`
against the code and re-examined my own position honestly. No code, no commits.

## First — I was wrong about the fix, and I'll own it precisely

I caught the bug (good), but **my recommended fix was itself incomplete and my
argument against the max-based approach was wrong.** Specifically:
- I recommended option (a) "leave `actWaitFixed` at `StartNS`." That reverts the
  finish-inside-lock bug (95) **but leaves spawn-during-lock at the wrong 400
  (correct 300)** — a real unsoundness. I even wrote it off as "pre-existing,
  smaller, not a cycle." **That is the exact reasoning-pattern error Erik
  rejected** (drawing the bug boundary around "what my change touched" and letting
  "pre-existing" mean "not mine to fix"). I should hold myself to the same standard
  I'd apply to the implementer: a wrong number is a wrong number.
- I argued the max-based model was "more native-semantics change." **It is the
  opposite** — it is finish-*invariant* versus the original `+= dur` model (max ≡
  += for the finish, see below), so it does **not** change native makespan; it only
  *corrects* spawn-during-lock. My "more native change" objection was simply
  incorrect.

So: the max-based model — which I argued against — is the right fix, and I endorse
it. The bug catch stands; the prescription was mine to get wrong, and I did.

## The max-based fixed-wait model — verified correct

A fixed delay now compiles to a **pair** (replay.go `compileProgram`):
`actWaitFixedStart` at `w.StartNS` records the sim clock into `fixedWaitClock[slot]`
(no clock change); `actWaitFixedEnd` at `w.EndNS` does `clock = max(clock,
fixedWaitClock[slot] + dur)`. I checked each property:

- **Finish-invariant vs. the original `+= dur`.** The original additive applied
  `clock += dur` at the delay's start (after self/spawn, the old rank-2 position),
  i.e. it set `clock = startClock + dur`, after which later child-joins `max` it up
  → final `= max(startClock + dur, child finishes)`. The max-based records
  `startClock`, lets the in-between joins `max` the clock, then at the end does
  `max(clock, startClock + dur)` → final `= max(startClock + dur, child finishes)`.
  **Identical.** So the op's finish is unchanged whether or not a child overlaps —
  not merely "when none overlaps." That's why the 86k native trace is bit-unchanged
  (its 12 fixed waits don't overlap a concurrent child, so it's trivially `max ≡
  +=`, but the invariance is the stronger statement). ✔ **My counterexample is
  fixed: `TestFixedWaitConcurrentChild/child-finishes-inside` asserts `finish ==
  95ms`** — the correct value, not the broken 125. ✔
- **Fixes spawn-during-lock.** `actWaitFixedStart` is non-gating (rank 3, *after*
  spawn) and doesn't advance the clock, and `actWaitFixedEnd` isn't reached until
  the lock's end — so a child spawned during the lock is anchored at the pre-lock
  clock (concurrent, ungated). Case 2 asserts 300, was 400. ✔
- **`actionRank` correct:** `actWaitJoin, actWaitFixedEnd = 0` (max-gates at the
  end, apply before same-instant self/spawn so those are gated); `self = 1`,
  `spawn = 2`; `actWaitFixedStart, actWaitNoop = 3` (start markers, non-gating,
  after spawn). This is exactly "start-marker non-gating after spawn; end-gate a max
  like a join." ✔
- **Order-independent for fixed waits too** (closing my round-3 in-order-asymmetry
  worry for the lock case): because `actWaitFixedStart` doesn't change the clock, a
  child spawned during the lock anchors at the pre-lock clock on **both** the
  out-of-order prefix walk (which stops at the spawn, before the end-gate) and the
  in-order finish (end-gate not yet reached at the spawn). Same value either way.
  `TestConcurrentWaitOrderIndependent` exercises the join case; the fixed case
  follows by the same construction. ✔
- **No new finish-invariance hole.** The only additive contributions are
  self-segments, which never overlap a wait (graph.go SelfSegments subtracts both
  join and fixed wait intervals); every wait contribution is now a `max` (join and
  fixed-end), and `max` commutes with the child-joins. So the whole model is
  finish-invariant. The additive-vs-max defect I found is fully resolved — it was
  the *last* additive contributor, now converted to `max`. ✔

**One latent edge (benign, worth a note):** a **zero-duration** fixed wait
(`StartNS == EndNS`, instant lock) sorts its end-gate (rank 0) *before* its
start-marker (rank 3), so the end reads `fixedWaitClock[slot]` before the start
writes it. Harmless because `dur == 0` ⇒ `max(clock, 0 + 0) = clock` (no effect),
and slots are fresh-zeroed per `NewSimulation`. Not a correctness issue, but a
one-line guard or comment (`if dur > 0`) would remove the read-before-write
fragility before it ever matters.

**Backward-compat / native:** the lead confirms green + bit-unchanged makespan +
12 real fixed waits behaving identically; the `RunWhatIfs` signature gained a
return value (benign) and `replay_test.go` is otherwise unmodified. I rely on the
lead's run for the suite; I verified the *mechanism* from code, which is the deeper
check, and the dedicated `TestFixedWaitConcurrentChild` asserts both 95 and 300.

## Open Q1 — FallbackAnchors as a HARD gate failure (Erik's lean: YES)

**Agree: hard-fail `FallbackAnchors > 0`. There is no "real-approximation"
false-positive.** Reasoning:

- Every fallback anchor uses the **recorded-offset** — the exact `startOf`
  approximation Erik ruled out — and it is exact *only* at baseline (factor 1, no
  shift). Under any what-if that shifts the fallback target's frame, it diverges
  (the order-dependent saving). So a fallback is **always** the rejected
  approximation, present in the analysis.
- Both fallback sources confirm this: the **in-flight-ancestor** case is a genuine
  inversion (the same non-synchronous/false-cycle class — should fail), and the
  **cross-root forward reference** case anchors a not-yet-scheduled root at its
  recorded start (approximate the moment a factor shifts that root's chain).
- **Is a faithful trace ever legitimately `FallbackAnchors > 0`?** A cross-root
  forward reference (a cross-query/cross-session singleflight join, R1 waits on a
  later root R2's work) *is* a faithful structure — but it still *uses the
  approximation*, so it is not "fine." Hard-failing it is **not** a false-positive
  in the sense that matters (there genuinely is an approximation in the report);
  it's the gate doing its job. The right response when such a trace appears is to
  **make `Run`'s root scheduling handle cross-root forward references exactly**
  (e.g. pre-anchor all root chain-starts before `finish`, or schedule R2's chain on
  demand) so it never falls back — *fix the analyzer, don't relax the gate*. That
  is precisely the no-approximation discipline.
- It is **free right now**: `FallbackAnchors == 0` on every tested trace (native +
  OTel), so hard-failing costs nothing today and converts a silent-precondition
  into a loud one for the first trace that ever hits it.

So: flip it from the opt-in `MaxFallbackAnchors` bound to an unconditional
`FallbackAnchors > 0 ⇒ violation` (it's a one-line `gate.go` change). The only
caveat to record: this makes "analyze a trace with a genuine cross-root forward
reference" a *failing* case until root-scheduling is improved — which is the
correct forcing function, not a regression.

## Open Q2 — SimStartConflicts posture

**Keep `SimStartConflicts` report-only — given Q1 is enforced.** The harmful
SimStartConflict source is, by the code's own comment and my reading, *exactly*
"a recorded-offset fallback anchor disagrees with the shifted full-finish under a
what-if" — i.e. it is **preconditioned on `FallbackAnchors > 0`**. So once
`FallbackAnchors > 0` is a hard fail (Q1), every harmful SimStartConflict is
**already caught upstream**, before the conflict can matter. The only residual on a
fallback-free trace is the **benign** zero-duration-child-at-a-gating-wait's-end
coincidence (the `joinUpTo` "never-anchored" branch anchoring before the spawn
re-anchors) — which the test confirms does not corrupt the finish (`finish == 300`,
the zero-dur child absorbed). So:
- Don't hard-fail SimStartConflicts (it would false-positive on the benign zero-dur
  case).
- Don't bother making it "clean + enforced" — that would duplicate the
  `FallbackAnchors` catch for the harmful case and add machinery to exclude the
  benign case for no gain. Report-only (surfaced at baseline *and* across what-ifs,
  which the patch now does) is the right amount of visibility.

The implementer's reasoning here is sound; with Q1 enforced, SimStartConflicts is a
pure diagnostic and report-only is correct.

## Landable now + blockers

**Landable — yes, with one gate edit.** The max-based model is correct,
finish-invariant (native bit-unchanged), fixes both fixed-wait cases, has the right
`actionRank`, is order-independent, and is well-tested (the dedicated fixed-wait
test asserts 95 and 300). The cycle fix (`actWaitJoin` end-ordering + prefix-stop
`advance`/`spawnTo`) that I verified airtight last round is preserved underneath.

Remaining items before merge:
1. **Apply Q1: make `FallbackAnchors > 0` an unconditional hard gate failure** (my
   verdict + Erik's lean). One line; free now; record the cross-root-forward-ref →
   improve-root-scheduling note.
2. **Zero-duration fixed-wait latent edge** (`dur == 0` end-before-start
   read-before-write): add a `dur > 0` guard or a comment. Benign today; cheap to
   harden.
3. **Native per-test green / bit-unchanged makespan** — lead verified; for the
   validated replay I'd keep that as the explicit merge gate (the finish-invariance
   is now *provable*, not just measured, which is the stronger footing).
4. Standing (not this fix): the jaccard instrument for buildkit-heavy oracles (the
   implementer has now conceded "replay can't move jaccard" was wrong — the oracle
   ranks by `SavedNS` — but the buildkit-vs-semantic-naming conclusion stands, as I
   noted last round); and the §3.1 suppressed-caller-fold self-time under-credit.

## Summary

- **Max-based model correct?** **Yes.** Verified from code: fixed delay = `max(clock,
  startClock + dur)` via a non-gating start-marker + a max end-gate. Finish-invariant
  vs. the original `+= dur` (so native unchanged), fixes finish-inside-lock (95, my
  counterexample) *and* spawn-during-lock (300), correct `actionRank`,
  order-independent, only-benign zero-dur residual. **I concede my option-(a)
  recommendation was wrong** (it left the 400 unsoundness) **and my "more native
  change" objection was wrong** (it's invariant).
- **FallbackAnchors hard-fail verdict:** **hard-fail (agree with Erik).** No
  real-approximation false-positive — every fallback is the rejected `startOf`
  approximation. A faithful cross-root forward reference *does* use the
  approximation, so failing is correct; the fix for that trace is exact
  root-scheduling, not gate relaxation. Free now (0 on all traces).
- **SimStartConflicts posture:** **report-only** — given FallbackAnchors is enforced,
  the harmful conflict is subsumed by the FallbackAnchors hard-fail (it's
  preconditioned on a fallback), and the residual is the benign zero-dur case. No
  need to make it clean + enforced.
- **Landable now + blockers:** **landable** with (1) the FallbackAnchors hard-fail
  applied, (2) the zero-dur fixed-wait guard/comment, (3) native per-test green as
  the merge gate. The mechanism is sound and the finish-invariance is now provable.
