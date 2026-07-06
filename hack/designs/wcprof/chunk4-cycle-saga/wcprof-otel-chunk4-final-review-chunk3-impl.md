# wcprof × OTel — FINAL cycle-fix review (by the Chunk 3 implementer)

**Analysis only — no code, no commits.** Reviewing commit `e69d1f0049` (the actual
patch in `wcprof-otel-chunk4-hardening-e69d1f0049.patch`) before it merges into the
shared/validated `wcanalyze` replay (PR #13393 / native). This is a mechanism the
council has NOT seen: **end-ordered gating waits** (sequence each gating wait at its
recorded `EndNS`) + one shared `advance(op, stopAt)` interpreter, not the
skip-predicate I reviewed in round 3. I vet the mechanism itself.

**Headline:** the end-ordering reformulation is **sound and elegant** — order-
independence holds *by construction*, which is stronger than the skip-predicate's
first-write-wins, and it **closes my round-3 silent-fallback hole**. Two things must
be corrected before it lands in validated native code, neither of which reopens the
mechanism: (1) the lead's load-bearing "parent finishes UNCHANGED" justification is
**imprecise — finishes DO change** (a correction, not a regression) for ops that join
a child spawned during a concurrent wait, and that is exactly what the −2.4% OTel
drift is; (2) `SimStartConflicts` is wired report-only and should **hard-fail the
gate** (it is a soundness/order-dependence violation).

---

## Charge 1 — Do the new counters close my round-3 hole? Is "CycleWarnings = genuine signal" preserved?

**The hole is CLOSED. The signal is preserved, but re-framed across three counters —
and the gate must check all three (one of which is mis-wired).**

My round-3 hole: `spawnTo`'s recorded-offset fallbacks broke loops *silently*
(uncounted), so a genuine cycle dissolved via a spawn-anchor re-entry would never
surface. The patch closes it:

- **Every recorded-offset fallback now counts.** `fallbackAnchor` (patch
  `replay.go:363-373`) unconditionally does `s.FallbackAnchors++` + samples, and it is
  the *only* place the recorded-offset approximation survives — reached from the
  in-flight-parent corner (`spawnTo` line 340-345), the in-flight-grandparent corner
  (335-337), the par<0 cross-root case (317-327), and the "prefix never reached the
  spawn" case (357-358). So no anchor is silent. ✔ (The commit message says exactly
  this: "every one is now counted (FallbackAnchors), never silent.")
- **`SimStartConflicts`** (setStart, `replay.go:181-196`) counts any anchor *overwrite
  with a different value* — the residual order-dependence detector. So a path that
  anchored an op differently than a later path is surfaced, not masked by
  first-write-wins. ✔
- **Genuine cycles still surface.** A genuine recorded cycle (gating waits forming a
  loop) re-enters an op while `inFlight` → `finish` line 239-244 → `CycleWarnings++`.
  Spurious over-serializations no longer reach this point (end-ordering keeps a
  concurrent wait from gating a spawn), so the comment's new claim — "CycleWarnings
  counts *genuine* cycles; spurious over-serializations … do NOT count here"
  (`replay.go:250-254`) — is accurate.

**The re-frame to be honest about:** "CycleWarnings alone = every genuine cycle" is
*not* strictly true under the new model. A genuine inversion that closes via a
*spawn-anchor* re-entry (`spawnTo` finds `par` already in-flight, line 340) is broken
by `fallbackAnchor` and counted as **FallbackAnchors**, not CycleWarnings. So the
precise, preserved property is:

> **CycleWarnings + FallbackAnchors + SimStartConflicts == 0 ⟺ the trace replayed
> cleanly** (no finish-reentry cycle, no recorded-offset inversion, no
> order-dependence). Every residual is counted; none is silent.

That is the property I cared about (no silent dissolution), and it holds. **But the
gate must enforce all three** — and here is the gap: `gate.go` (patch lines 791-823)
**adds `SimStartConflicts` to the report and the `Write` output but does NOT add a
violation** — only `CycleWarnings` hard-fails; `FallbackAnchors` is report-only
(default `MaxFallbackAnchors=0`); `SimStartConflicts` is report-only too. So a future
trace with `SimStartConflicts>0` (a real order-dependence — the invariant the whole
reformulation rests on) would **PASS the gate with a printed number**. **Blocker:
`SimStartConflicts>0` must be a hard gate failure**, the same tier as a cycle — it
means the replay's order-independence invariant broke, so its numbers are unreliable.
(Measured 0 on native 86k + OTel 11k today, so this is future-proofing the invariant,
not a current failure — but it is the one invariant that *defines* the new model.)

---

## Charge 2 — End-ordering soundness: does it preserve the parent's FINISH?

**No — the lead's "parent finishes unchanged" is imprecise. Finishes DO change for
ops that join a child spawned during a concurrent wait. The change is a CORRECTION
(de-serialization), not a regression, but the justification must be restated, because
it is the load-bearing argument for touching validated native code.**

The lead's argument: `SelfSegments` subtracts every wait interval, so self-segments
never overlap a wait, so moving a wait to `EndNS` can't move a self-segment across it
→ finish-invariant. **That argument is correct for SELF and WAIT-TARGET contributions
but does not cover the implicit JOIN of a child spawned during the wait** — and
children *do* overlap waits (that is the whole point).

**Finish-changing counterexample** (factor=1 baseline; all valid recorded structure):

```
op [0,35]   self segments [0,5] and [30,35]   (10ms self total)
  ├ wait on T  [5,20]   (gating join, finish(T)=20)
  └ child  C   [10,30]  (spawned at 10, DURING the wait; op implicitly joins it)
```

`SelfSegments(op) = [0,35] − [5,20] − [10,30] = [0,5]+[30,35]` ✓. Trace `advance`:

- **start-ordering (OLD, wait at `at`=5):** self→clock 5; wait@5 → clock max(5,20)=20;
  spawn C@10 at clock 20 → **C anchored at 20** (gated); join C (finish 40); self →
  **op finish 45**.
- **end-ordering (NEW, wait at `at`=20):** self→clock 5; spawn C@10 at clock 5 → **C
  anchored at 5** (concurrent, ungated); wait@20 → clock max(5,20)=20; join C (finish
  25); self → **op finish 30**.

So the op's finish moves **45 → 30** between OLD and NEW — a real finish change. The
*recorded* finish is 35, so NEW (30, 5ms under) is closer than OLD (45, 10ms over),
but **neither is exact**: the replay can't anchor C at its true recorded spawn (10,
"5 ms into the wait") because it models a wait as atomic. OLD over-anchors (post-wait
clock); NEW under-anchors (pre-wait clock).

**Implications, precisely:**

- The new model is **a net improvement** (it de-serializes a concurrent child instead
  of falsely gating it — the same defect class as the cycle), and it is
  **order-independent**. But "finishes unchanged" is the wrong justification; the
  correct one is **"self + wait-target contributions are invariant; a
  concurrently-spawned joined child is de-serialized (corrected), with a bounded
  *under-anchoring* residual (it lands at the pre-wait clock, not the exact intra-wait
  point)."**
- **This is what the −2.4% OTel drift IS.** §3.1's suppressed-caller-wait-on-`call_exec`
  attribution creates many "an op has a wait concurrent with a child spawn" cases; end-
  ordering de-serializes them all, under-anchoring each slightly → the OTel baseline
  under-estimates the recorded OTel actual by 2.4% (native only −0.1% because its
  finer per-caller ops have far fewer such folds). So the implementer's "correct
  compression" framing is **half right**: it correctly removes the invented
  serialization, **but it also under-anchors**, and the drift vs the *OTel source's own
  recorded actual* grew (−0.3% throwaway → −2.4% hardened). Under Erik's accuracy bar
  this should not be hand-waved — it should be validated that (a) the −2.4% is
  concentrated in §3.1 suppressed-caller folds, and (b) it does not move the bottleneck
  **ranking** (the implementer claims `ModuleSource.asModule` now saves==self, matching
  native — if the *rankings* are correct despite the makespan-magnitude drift, the
  product goal holds, since the deliverable is the bottleneck identification, not the
  absolute makespan).
- **For landing in NATIVE:** native finishes change too, wherever it has a joined-child-
  spawned-during-a-wait (corrections, by construction — de-serialization can only move a
  child *earlier*). The −0.1% net + the full native suite passing + `replay_test.go`
  unmodified bound this — but the justification on record must be **"native finishes are
  corrected, net −0.1%,"** not "unchanged," so a future reviewer of PR #13393 isn't
  misled.

**Does end-ordering SUPPRESS a genuine cycle the start-ordered version would surface?
No.** A gating wait (join/fixed) has `waitEnd ≥ targetEnd−ε` and the op blocks until
the target finishes, so the wait's `EndNS ≤ op.end` — it is still *processed within the
op's interval*, just sequenced later. So `finish(target)` is still invoked; a genuine
cycle through it still re-enters and is caught at `finish:239-244`. Re-ordering only
moves *when* a non-clock-advancing `actSpawn` happens relative to the wait; it cannot
remove the wait's `finish(target)` call. The `actionRank` (gating=0 < self=1 < spawn=2
< noop=3, patch lines 95-108) makes `waitEnd == spawn ⇒ gated` (inclusive) — the right
boundary, and test (d) `own-end-past-spawn` (lines 636-637) proves the wait's **own**
end wins over the retired target-end proxy at the boundary. So no genuine cycle is
suppressed; the suite even exercises the zero-duration/boundary case the real residual
had.

---

## Charge 3 — Clock skew: does end-ordering change the exposure I flagged?

**It inherits the same pre-existing skew assumption — and is actually *better* than the
skip-predicate I reviewed.** End-ordering sequences the gate at the wait's **own**
recorded `EndNS` (patch `replay.go:118-127`), which is on the **waiter op's** clock —
the same clock as that op's self/spawn actions. The gating comparison is "wait.EndNS
(waiter clock) vs the spawn's recorded `at` (child clock)," exactly the cross-client
recorded-time comparison the existing implicit join already makes (`joinUpTo` compares
`child.endNS` vs `action.at`). So the model's skew exposure (§9 epoch/skew seam) is
**unchanged**, not widened. This is strictly cleaner than my round-3 skip-predicate,
which compared `endNS[target]` (the wait *target's* clock — a potentially *different*
client than the waiter) against the spawn — the patch's commit message and the
`own-end-vs-target-end` test (d) both confirm it threads the wait's own end, not the
target-end proxy. So Charge-3: same assumption, no regression, and a real improvement
over what I reviewed.

---

## Jaccard 0.80 pushback + the −2.4% framing

- **The 0.15 cross-source jaccard is NOT a cycle-fix regression — agreed,** and it is
  the by-design 2nd-source scope mismatch I and the design author established in earlier
  rounds (OTel buckets engine work under buildkit span-names `:uploading`/`:stdout`;
  native uses semantic classes). The `0.80` bar is the wrong gate for a buildkit-heavy
  exec workload; the §6.4 standing gate must scope-match / class-filter (the design
  author's §6.2/§6.4 reconcile). The implementer is right to push back.
- **"The replay change CANNOT move jaccard" is *roughly* right but imprecise.** Jaccard
  ranks top-N by `RunWhatIfs` `SavedNS` (oracle.go via the replay), **not** raw
  self-time, so the replay change *can* in principle shift a class's `SavedNS` in/out of
  the top-N. What is true is that the top-N class *set* here is dominated by which
  classes are large, and end-ordering moves `SavedNS` *magnitudes*, not which classes
  dominate; combined with `SimStartConflicts=0` and the matched self-time spot-checks,
  the set is stable. **Clean confirmation (recommended, I can't run it): report the
  native↔OTel jaccard BEFORE and AFTER the replay fix on the same captured trace.** If
  identical, it nails "the replay change didn't move it" empirically rather than by
  argument.

---

## Landable? Blockers?

**Landable in mechanism — the end-ordering reformulation is the right fix and is
cleaner than everything that preceded it** (order-independent by construction;
first-write-wins no longer load-bearing; the recorded-offset approximation confined to
counted corners; the residual cycle resolved structurally, not by a per-wait
classification). The regression suite (config-parse what-if 100 ms, min-cycle 0,
order-independence both join orders, wait-end boundary, fixed-wait overlap, cross-root,
64-wide fan-out) is genuinely good and covers the cases I'd have asked for.

**Blockers / must-fix before merging into validated native:**

1. **`SimStartConflicts > 0` must HARD-FAIL the gate** (currently report-only,
   `gate.go` adds the field + print but no violation). It is the single invariant the
   new model rests on; a silent pass on a future order-dependence defeats the purpose.
2. **Restate the native-safety justification precisely:** finishes are **corrected
   (de-serialized) for joined-child-during-wait cases, net −0.1%**, NOT "unchanged."
   The "self-segments never overlap a wait" argument covers self + wait-target only; the
   child-join change is real (counterexample above) and is the source of the −2.4% OTel
   drift. Land it on the strength of "all native tests pass + the change can only move a
   concurrent child *earlier* (a correction) + −0.1% net," not on an over-strong
   invariance claim.
3. **Validate the −2.4% OTel drift** is (a) concentrated in §3.1 suppressed-caller folds
   and (b) does not move the bottleneck **ranking** (the implementer's `asModule
   save==self == native` claim — confirm the post-fix §6.2 oracle ranking on the module
   workload agrees with native, which also discharges item-2's "corrections only").
4. **Confirm jaccard identical pre/post replay-fix** on the same trace (cheap; closes
   the "replay can't move it" argument empirically).

**Should-do (not blockers):** decide whether `FallbackAnchors > 0` (the surviving
recorded-offset corner — a `startOf`-style wrong counterfactual if a what-if scales the
in-flight ancestor) should be loud/validated rather than report-only, given Erik's bar;
a `PrefixAnchors`-large note is correctly informational.

**Verified-fine:** the silent-fallback hole is closed (every fallback counts); genuine
cycles still surface (`finish` re-entry); end-ordering does not suppress a genuine
cycle; clock-skew unchanged (and better than the skip-predicate); the dual semantics
(concurrent wait gates the parent finish, not the child spawn) is correct and tested;
`actionRank` inclusive `waitEnd==spawn ⇒ gated` boundary is right.

**(Carried, separate):** the **service.start §3.4 self-erasure re-root** is still owed
in both sources (re-root the availability span out of `service.start`, add a "slow start
headlines" assertion) — independent of this replay fix.

---

## Summary

- **Do the new counters close my round-3 hole? YES.** `fallbackAnchor` counts every
  recorded-offset anchor and `SimStartConflicts` counts every order-dependent overwrite,
  so no loop is silently dissolved.
- **Does "CycleWarnings = genuine signal" survive end-ordering? YES, re-framed.**
  Spurious over-serializations no longer reach the cycle-break (end-ordering keeps a
  concurrent wait from gating a spawn); genuine cycles still hit `finish`'s `inFlight`
  break. The full property is **CycleWarnings + FallbackAnchors + SimStartConflicts == 0
  ⟺ clean replay** — which requires the gate to **hard-fail on `SimStartConflicts`
  (currently report-only — Blocker 1)**.
- **End-ordering soundness:** order-independent by construction (better than the
  skip-predicate), genuine cycles preserved, clock-skew unchanged. **But "finishes
  unchanged" is imprecise** — finishes are *corrected* (de-serialized) for
  joined-child-during-wait cases; that correction (with a bounded under-anchoring
  residual) IS the −2.4% OTel drift, and it touches native too (net −0.1%). Restate the
  justification (Blocker 2) and validate the drift doesn't move the ranking (Blocker 3).
- **Landable?** Yes in mechanism; merge into validated native after the 4 blockers
  (hard-fail SimStartConflicts; precise finish-correction justification; validate
  −2.4%/ranking; confirm jaccard pre/post). service.start §3.4 still owed separately.
