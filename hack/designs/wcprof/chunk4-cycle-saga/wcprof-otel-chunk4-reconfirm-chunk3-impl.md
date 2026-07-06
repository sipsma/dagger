# wcprof × OTel — cycle-fix RE-CONFIRM review (by the Chunk 3 implementer)

**Analysis only — no code, no commits.** Reviewing the net diff since the commit I
reviewed (`e69d1f0049 .. HEAD`, in `wcprof-otel-chunk4-postreview.patch`): the
max-based fixed-wait model (`692aaabd3f`) + the SimStartConflicts-across-what-ifs
surfacing. The gate-posture questions are my wheelhouse (I own the property
`CycleWarnings + FallbackAnchors + SimStartConflicts == 0 ⟺ clean replay`).

**First, an honest acknowledgement:** I was one of the 4 who passed `e69d1f0049` and
**missed the fixed-wait additive bug**. My round-final counterexample reasoned about
the *join*-wait kind (the child-join de-serialization, which I did find) but I did not
generalize it to the *fixed*-delay kind — where `clock += dur` doesn't commute with the
child-join `max`. The two dissenters (replay owner + fresh Codex) were right, and the
implementer's meta-lesson ("don't draw the bug boundary around what your change touched
— fix the principle") applies to reviewers too: I should have asked "does the
de-serialization argument hold for *every* wait kind," not just joins. Noted.

**Headline:** the max-based fixed-wait model is **correct, backward-compatible, and the
right fix**; the clean-signal property survives (fixed waits are inert for
cycles/anchors); my round-3 silent-fallback hole stays closed. On the two open
questions I **disagree with Erik's lean to hard-fail `FallbackAnchors`** (it
false-positives a genuinely faithful trace) and instead give a precise policy:
**make `SimStartConflicts` clean and HARD-FAIL it across the what-if sweep; report
`FallbackAnchors`.** That gives the property real teeth on the *actual* unsoundness
without rejecting sound baselines.

---

## Charge 3 / 1 — the max-based fixed-wait model: CORRECT

The model compiles a fixed delay to two markers (`replay.go`):
- `actWaitFixedStart` @ `w.StartNS`, rank 3 → records `fixedWaitClock[slot] = clock`
  (the sim clock when the op reaches the lock), AFTER any self/spawn at that instant.
- `actWaitFixedEnd` @ `w.EndNS`, rank 0 → `clock = max(clock, fixedWaitClock[slot] +
  dur)`, BEFORE any self/spawn at that instant.

I traced both fixtures and they are right:
- **child-finishes-inside** (lock [10,40] dur 30, child c [5,35]): fixedStart records 5;
  joinUpTo(40) joins c (finish 35) → clock 35; fixedEnd `max(35, 5+30=35)=35`; trailing
  self 60 → **95** (additive would give `35 + 30 = 65 → 125`). ✔
- **child-spawned-during** (lock [50,200] dur 150, child U [100,200]): fixedStart records
  50; spawn U@100 → U anchored at 50 (concurrent); fixedEnd `max(join(U)=150, 50+150=200)
  =200`; trailing self 100 → **300**, U=50 (additive would give `200 + 100 → 400`). ✔

**Backward-compatible — verified by reasoning, matching the lead's 86k measurement.**
`max(clock, startClock + dur) == clock + dur` exactly when `clock == startClock` at the
delay's end, i.e. when no concurrent join raised the clock between the lock's start and
end. Since self never overlaps a wait (so no self runs *inside* the lock) and there is
no concurrent child to join, `clock` is unchanged across the lock ⇒ `max == additive`.
The lead's "12 real fixed waits on native, all ≡ +=, makespan bit-unchanged" is exactly
this case. So native is preserved on the common path and only *corrected* where a child
was concurrent with a lock — the same de-serialization principle as the join fix, now
consistently applied to the fixed-delay kind (which is precisely the inconsistency I
missed).

**`actionRank` for the new markers is correct.** `actWaitFixedEnd` rank 0 (a max-gate,
like a join — applies before a same-instant self/spawn, so a child spawned exactly at
the lock's end is gated, matching the inclusive `waitEnd == spawn ⇒ gated` boundary).
`actWaitFixedStart` rank 3 (non-gating — records the clock *after* a same-instant
self/spawn, so a child spawned exactly at the lock's start stays concurrent, anchored at
the clock the lock began). ✔

**Does it preserve "CycleWarnings = genuine signal"? Yes — fixed delays are inert for
cycles.** A fixed delay has `w.Target == nil`, so neither marker calls `finish()` — the
end-marker is a pure `max(clock, slot+dur)` with no recursion. So a fixed delay can
never be an edge in a cycle, and the two-marker change doesn't touch `CycleWarnings`,
`FallbackAnchors`, or anchoring. The cycle signal is unaffected. ✔ No new issue found.

---

## Open Q1 — Should `FallbackAnchors > 0` HARD-FAIL? (Erik leans yes.)

**My answer: NO — there IS a faithful trace where `FallbackAnchors > 0` legitimately,
with an EXACT baseline, so a blanket hard-fail is a false-positive that rejects sound,
analyzable data.** The genuine unsoundness is the order-dependent *what-if saving*, not
the recorded-offset *use* — and that is caught precisely by clean `SimStartConflicts`
across the what-if sweep, not by failing the baseline.

The faithful case is the **cross-root forward reference** (`spawnTo` `par<0`,
`replay.go`), which the implementer's own test (f) `TestCrossRootAnchor` constructs and
which is real on a **native multi-session dump**: root1 (an earlier session) joins, via
cross-session singleflight, an op under root2 (a later, overlapping session) — the dagql
cache keys singleflight by call+concurrency, not session, so this is genuine recorded
sharing, not an anomaly. Run() chains root1 before root2, so when root1's replay
references root2's op, root2 is unscheduled → recorded-offset fallback. **And in the
baseline this is EXACT** (factor 1 ⇒ no chain shift ⇒ recorded offset == Run's chained
start ⇒ the test asserts `base.SimStartConflicts == 0`). So a trace with
`FallbackAnchors > 0` can have a perfectly sound baseline makespan + structure; failing
it discards a usable analysis.

What IS unsound is the **what-if** saving for the fallback-anchored class: under a factor
that shifts root1's makespan, the recorded-offset anchor disagrees with root2's *shifted*
chained start (test (f) asserts `scaled.SimStartConflicts > 0`). That order-dependence is
the real harm, and it is exactly what `whatIfConflicts` (the new RunWhatIfs return)
captures. So:

- **`FallbackAnchors > 0` is the *precondition* for harm, not the harm.** Exact in
  baseline; faithful on cross-root; the harm only manifests under a shifting what-if.
  Blanket-failing it is over-broad (Erik's "enforce, not observe" is right in spirit but
  aimed at the wrong counter).
- **The in-flight-ancestor fallback** (the other `FallbackAnchors` source, `spawnTo` line
  340) is closer to a data anomaly (it needs a wait that precedes a spawn to reference
  that spawn's descendant — a forward reference), so failing *it* is more defensible —
  but the counter conflates it with the faithful cross-root case, so a single hard-fail
  can't distinguish them.

**So: report `FallbackAnchors` loudly (precondition); enforce the actual harm via clean
`SimStartConflicts`/`whatIfConflicts`.** (If Erik wants belt-and-suspenders against a
*latent* fallback whose harm only a non-swept factor would reveal, the right move is
**not** a blanket baseline hard-fail but a targeted forced-shift what-if when
`FallbackAnchors > 0` — scale a class known to move the fallback-anchored op's
root/ancestor and assert no conflict. That distinguishes "fallback that causes
order-dependence" from "fallback that's exact," which a blanket fail cannot.)

---

## Open Q2 — `SimStartConflicts` posture: report-only, or clean-and-enforce?

**Make it CLEAN (exclude the benign zero-duration case) and ENFORCE it — that, not
report-only, is what gives the three-counter property teeth.** The implementer's reason
for report-only (the benign zero-dur false-positive) is real, but the fix is to *clean
the counter*, not to *stop enforcing it*.

- **The benign case is genuinely benign and the exclusion is sound.** Test
  `TestZeroDurChildAtJoinWaitEnd`: a zero-duration child `Z[100,100]` whose end coincides
  with a join wait's end is `joinUpTo`-anchored at the pre-wait clock, then its spawn
  action re-anchors it post-wait → `SimStartConflicts=1`, **but `P.finish=300` is
  correct**. This is benign *for a structural reason*: a zero-duration op's finish equals
  its start, and at a gating wait's end its finish is ≤ the wait's gate, so it is absorbed
  — the parent's clock at the join is already ≥ the wait gate ≥ the zero-dur op's
  post-wait anchor. So **a zero-duration op's anchor disagreement can never change a
  finish** in this pattern. Excluding zero-duration ops from `SimStartConflicts` is a
  clean, sound proxy for "this disagreement is absorbed."
- **After that exclusion, `SimStartConflicts` is exactly the harm signal** — a
  *finish-affecting* op anchored two different ways depending on traversal order = an
  unsound (order-dependent) result. That is precisely what should hard-fail, and it
  inherits Erik's "enforce, not observe" cleanly.
- **Report-only + FallbackAnchors-hard-fail (the implementer's/Erik's split) is the wrong
  division:** it hard-fails the *precondition* (over-broad, false-positives faithful
  cross-root) while leaving the *actual harm* (the order-dependent saving) merely printed.
  Cleaning + enforcing `SimStartConflicts` inverts that to the correct one (fail the harm,
  report the precondition).

---

## The precise gate policy I'd ship

Two layers, because the harm is a *what-if* property and the §6.1 structural gate is
baseline-only:

1. **§6.1 structural gate (baseline replay, cheap, always-on):**
   - `CycleWarnings > 0` → **HARD-FAIL** (genuine cycle; factor-independent, so the
     baseline catches every one). *(already enforced)*
   - **clean** `SimStartConflicts > 0` (exclude zero-duration ops) → **HARD-FAIL** (a
     baseline-manifested order-dependence). *(new — currently report-only)*
   - `FallbackAnchors > 0` → **REPORT + sample** (the recorded-offset precondition;
     baseline-exact; faithful on cross-root). *(keep report-only — do NOT blanket-fail)*
2. **§6.2 oracle / §6.4 standing gate (where `RunWhatIfs` already runs, ~0.1s on 11k):**
   - **clean** `whatIfConflicts > 0` → **HARD-FAIL** (the cross-root / in-flight
     order-dependent savings that only a shifting factor reveals — the genuine harm that
     `FallbackAnchors` was only a precondition for). *(new — the patch computes
     `whatIfConflicts` but `report.go` only prints it and the oracle discards it with
     `_`; make it a failure here, made clean by the same zero-dur exclusion.)*

Net: every *actual* unsoundness fails (a cycle, or a finish-affecting order-dependent
anchor in baseline or under a factor); the recorded-offset *precondition* is reported and
investigable; the benign zero-dur coincidence is excluded so it can't false-positive.
This is the three-counter property **with teeth** — `clean(CycleWarnings) +
clean(SimStartConflicts, baseline) + clean(whatIfConflicts) == 0 ⟺ a sound result` —
and it does not reject the faithful, baseline-exact cross-root trace that a blanket
`FallbackAnchors` fail would.

---

## Clean-signal property + round-3 hole — still good?

- **Round-3 silent-fallback hole: still closed.** Every recorded-offset anchor routes
  through `fallbackAnchor` which increments `FallbackAnchors` (no silent break); the
  fixed-wait change adds no new anchoring path (fixed delays don't anchor or recurse). ✔
- **Clean-signal property: preserved, with the one impurity now understood.** The only
  thing that made the raw counters impure was the benign zero-dur `SimStartConflicts`
  coincidence; the zero-dur exclusion cleans it, after which the three (clean) counters
  are a faithful "did the replay schedule cleanly" signal. The fixed-wait model is
  orthogonal (inert for all three counters). ✔

---

## Landable now? Blockers?

**The mechanism is landable** — the max-based fixed-wait model is correct and
backward-compatible, the journey converged on the right principle (de-serialize *every*
concurrent wait kind, not just joins), the regression suite is strong (both fixed-wait
finish cases, the zero-dur benign coincidence documented-not-hidden, cross-root
baseline-exact + what-if-conflict surfacing, fan-out), and native is preserved (bit-
unchanged makespan, suite green, `replay_test.go` only a signature update).

**Blockers before merging into validated native (all gate-policy, none reopens the
mechanism):**

1. **Make `SimStartConflicts` clean** (exclude zero-duration ops) **and HARD-FAIL it** —
   baseline (in `gate.go`, which currently only reports it) and the what-if sweep (in the
   oracle / standing gate, where `whatIfConflicts` is currently only printed/discarded).
   This is the actual teeth; report-only leaves the order-independence invariant —
   the thing the whole reformulation rests on — observed but unenforced.
2. **Do NOT hard-fail `FallbackAnchors`** (Erik's lean) — it false-positives the faithful,
   baseline-exact cross-root case (test (f) proves it can fire with `SimStartConflicts=0`
   in baseline). Report it; the harm it preconditions is caught by item 1.
3. **(Optional, if Erik wants zero latent risk)** when `FallbackAnchors > 0`, add a
   targeted forced-shift what-if to deterministically manifest any order-dependence,
   rather than a blanket precondition fail.

**Verified-fine:** max-based fixed-wait model (both cases, backward-compat, actionRank);
cycle signal unaffected by fixed waits; silent-fallback hole closed; the benign zero-dur
exclusion is sound; the finish-correction (now honestly stated, not "unchanged") and the
−2.4% OTel / −0.1% native framing from my prior review stand.

**(Carried, separate):** the **service.start §3.4 self-erasure re-root** is still owed in
both sources — independent of this replay work.

---

## Summary

- **Max-based fixed-wait model: correct, backward-compatible, fixes both 95 / 300 cases,
  actionRank right, cycle-signal-preserving (fixed delays are inert for cycles).** I own
  missing the original additive bug — I generalized the de-serialization argument to joins
  but not to fixed delays.
- **Clean-signal property preserved**; round-3 silent-fallback hole stays closed; the only
  impurity (benign zero-dur `SimStartConflicts`) is cleanly excludable.
- **Gate policy:** **HARD-FAIL** `CycleWarnings` + **clean** `SimStartConflicts` (baseline)
  + **clean** `whatIfConflicts` (what-if sweep); **REPORT** `FallbackAnchors`. **Disagree
  with hard-failing `FallbackAnchors`** — a faithful cross-root (cross-session
  singleflight) trace produces it with an exact baseline, so failing it is a
  false-positive; the real harm is the order-dependent *saving*, caught by clean
  `whatIfConflicts`.
- **Landable now?** Yes in mechanism; merge into validated native after the gate-policy
  blockers (clean + enforce `SimStartConflicts`/`whatIfConflicts`; report — don't fail —
  `FallbackAnchors`). service.start §3.4 still owed separately.
