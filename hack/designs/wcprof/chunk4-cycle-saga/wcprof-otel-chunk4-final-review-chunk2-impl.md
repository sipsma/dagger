# Chunk 4 cycle fix — FINAL review of the end-ordered mechanism (by the Chunk 2 implementer)

**Reviewing commit e69d1f0049 (the actual patch), before it lands in the shared/
validated `wcanalyze` replay.** The landed mechanism is NOT the skip-predicate I
reviewed in Round 3 — it is **end-ordered gating waits**: a gating wait (join/fixed)
is sequenced at its recorded **EndNS** instead of StartNS, and one `advance(op,
stopAt)` interpreter serves both the full finish (`stopAt<0`) and the out-of-order
prefix anchor (`spawnTo`, `stopAt=child`). I verified against the patch +
`replay.go`. Analysis only; no code, no commits.

**Headline: this is the right mechanism, and it cleanly subsumes all three of my
Round-3 blockers.** It is landable, with a small set of non-blocking follow-ups
(chief among them: surface `SimStartConflicts` on the what-if runs, not just the
baseline).

## The mechanism, and why it is better than the skip-predicate I reviewed

The elegance is that **a wait open at a spawn sorts after the spawn by construction**.
`compileProgram` sets `a.at = w.EndNS` for a gating wait (patch lines 116-139) and
`actionRank` makes gating-wait(0) < self(1) < spawn(2) < noop(3) (lines 95-108). So
when `advance` walks an op's actions in (at, rank) order: a concurrent child spawned
at `t` (its `actSpawn` at `t`) is processed *before* a wait that is still open then
(`w.EndNS > t`), so the child anchors at the pre-wait clock and is **not gated** —
identically whether `advance` is the bounded prefix (stops at the spawn, line 292-294)
or the full finish (continues past it). This collapses my Round-3 #1 (path-dependence),
#2 (the target-end proxy / ε), and #3 (fixed waits) into one rule: it threads the
wait's **own** EndNS (not the target-end proxy), gives `actWaitFixed` the same
treatment, and is order-independent without any per-cutoff recompute. Strictly better.

## Charge 2 — End-ordering preserves the parent's finish: SOUND (I tried to break it)

The load-bearing invariant is real: `SelfSegments` subtracts every wait interval, so
**a self-segment never overlaps a wait**. Two consequences make the finish invariant:

1. **Self-accumulation is unchanged.** Moving a wait from StartNS to EndNS cannot
   cross a self-segment, because no self-segment starts inside `(StartNS, EndNS)`
   (it would overlap). The wait stays between the same pre-wait and post-wait self
   segments (the post-wait self starts ≥ EndNS; at equal EndNS the wait's rank 0
   sorts before self's rank 1, so the self is still applied *after* the clamp).
2. **Clamps are `max`, hence order-independent.** Every wait does `clock =
   max(clock, finish(target))` and every implicit join does the same; reordering
   `max`-clamps (and reordering which children `joinUpTo` absorbs at each point)
   cannot change the final clock, only the intermediate one.

I tried three counterexample families and none moves a finish:
- **`waitEnd < targetEnd` / ε:** the clamp *value* is `finish(target)` (simulated),
  the *sequencing* is `waitEnd` (recorded); the join/noop classification (the only
  place `joinEpsilonNS` lives, line 118) is unchanged, so neither the clamp value nor
  the gap-position changes → finish unchanged.
- **Overlapping/nested waits:** all `max`, commutative → same finish.
- **Scaled target (finish(T) ≫ or ≪ waitEnd):** the clamp uses the simulated finish;
  a scaled-down T makes the clamp a no-op (op proceeds at its own pace), a scaled-up T
  pushes the clock, and in both cases the post-wait self is applied after → finish
  consistent. Worked the concrete spawn-during-wait case by hand (old finish 350 =
  new finish 350; only U's anchor changes 300→50, the intended fix).

The `TestWaitEndGatingBoundary` "own-end-past-spawn" case (patch lines 636-637)
**directly verifies my Round-3 ε concern is gone**: a wait whose *target* end (100ms)
coincides with the spawn but whose *own* end (100.5ms) follows it is correctly NOT
gated — "the retired target-end proxy would have (wrongly) gated this." That is
exactly the bounded-ε misclassification I flagged in Round 3, now resolved by
construction. Native makespan −0.1% unchanged corroborates. **Finish-preservation
holds.**

## Charge 3 — Order-independence: REAL by construction for the common path; one gap

For the **normal** path it is genuinely by construction, not just measured. `spawnTo`
runs `advance(par, target)` and the full finish runs `advance(par, -1)`; both start
from `par`'s anchored start and process the *same* prefix actions in the *same* order
up to `actSpawn(target)`, so the clock at the spawn — and thus `target`'s anchored
start — is identical. This holds under **any** factor (both paths apply the same
factor to the same prefix). `SimStartConflicts=0` on native 86k + OTel 11k is a sound
runtime corroboration, and the dual-semantics test (`TestConcurrentWaitOrderIndependent`,
both join orders → U=50, P finishes 250) is the right machine-check. **My Round-3
first-write-wins worry is genuinely resolved** — first-write-wins is no longer
load-bearing for the normal path.

**The one gap (non-blocking but real): the fallback corners are NOT by-construction
order-independent, and `SimStartConflicts` is only checked on the BASELINE.** The
recorded-offset `fallbackAnchor` (cross-root `par<0`, or an in-flight ancestor; patch
lines 317-327, 340-345, 363-373) anchors at the *recorded* offset, which under a
what-if factor disagrees with the scaled full-finish value → `SimStartConflicts++`.
That is fine *if surfaced*, but:
- `report.go` (patch line 785) checks only `baseSim` (factors=nil); the `RunWhatIfs`
  sims' `SimStartConflicts` is never surfaced.
- `CheckStructural` (gate.go) runs one baseline sim; the what-ifs are not gated.
- `TestCrossRootAnchor` (case f) only **logs** `conflicts=%d`, it does not assert 0.

So a what-if that scales a class in a cross-root/in-flight-anchored op's prefix could
produce a silent order-dependence in production. The measured workloads have
`FallbackAnchors=0` (no fallback hit), which is why `SimStartConflicts=0` — but that
is data-dependent, not a guarantee. **Recommend: aggregate/surface
`SimStartConflicts` across the `RunWhatIfs` sims (or assert it is 0 there), and make
`TestCrossRootAnchor` assert 0 under a factor.** This closes the only hole in the
order-independence claim.

## Charge 1 — op#250 self-time faithfulness: approximately faithful; "attribution not error" is correct

My Round-3 analysis said op#250's self-time matches native "within the small
`start_CC − start_wait` gap." Making the direction explicit now: §3.1 homes the
*suppressed concurrent caller's* wait onto op#250's `call_exec`, so OTel subtracts the
**wait** interval `[start_wait,205]` while native subtracts the suppressed sub-call's
**child** op interval `[start_CC,205]`. Since native's child interval ⊇ the wait
interval (the sub-call starts, then waits), **OTel *over*-credits op#250's self-time
by the sub-call's pre-wait work `[start_CC, start_wait]`** — not under-credits. For
the typical suppressed concurrent caller (a singleflight *joiner*), the pre-wait work
is just digest-derivation + the e-graph lookup — small — so the over-credit is small.
The implementer's measured `Query.moduleSource` self drift of **0.03** (op#250's own
class) confirms it: op#250 matches native closely. **Verdict: §3.1 ancestor-homing is
self-time-faithful for op#250 to within the suppressed joiner's (small) pre-wait
work; my `start_CC ≈ start_wait` argument holds on the real trace.**

The implementer's **"`asModule`/`Host.directory` drifts are attribution not error" is
correct**, but it is a *different* effect from the wait-fold above and worth stating
cleanly: there, the real *work* (the 720ms upload) is emitted by OTel as a **child
`:uploading` span**, so OTel's `Host.directory.call_exec` self is ~0 and native's
`Host.directory` self is ~781ms — the upload is counted once in **both** sources (total
agrees), just bucketed into different classes. That is faithful at the total level and
is the inherent 2nd-source bucketing, exactly as claimed.

## jaccard pushback + the −2.4% framing

**jaccard ≥ 0.80 is the wrong gate for this workload — I agree with the direction**,
but the implementer's claim "(a) the replay change CANNOT move jaccard" is
**overstated**. The oracle ranks top-N by `SavedNS` from `RunWhatIfs` (the replay), so
the cycle fix *does* change `SavedNS` for the affected classes (the report even shows
`asModule` save going from over-credited 376ms to save==self). What is true is that
the **0.15** is *dominated* by un-matchable class *names* (OTel `:uploading`/`:stdout`
vs native `Host.directory`/`exec.processRun`), which the replay cannot change — so
jaccard is ≈ stable across the fix, but not provably invariant. The right resolution:
validate the cycle fix by the **structural invariants** (cycles=0,
SimStartConflicts=0, finish-preservation) and the **matched-class identity oracle**,
not by raw-class-name jaccard; and do report jaccard **pre/post-fix** as Erik asked —
I expect it ≈ identical (bucketing-dominated), which would settle it.

**The −2.4% OTel drift "correct compression" is plausible and directionally
supported** (the hardened model moves OTel makespan 6.09s→5.96s, *toward* native's
5.86s, by removing the spurious over-serialization that had inflated it). It is larger
than native's −0.1% because the OTel *source* has a different span structure
(`:uploading` children, §3.1 folds), not because of a replay defect. I cannot
independently confirm the magnitude without the trace; I'd ask the implementer to
confirm the −2.4% is *stable* and that OTel makespan stays closer to native than the
pre-fix value (it claims both).

## Code review of the patch (correctness)

No correctness bug found. Specifically vetted:
- **`advance` early-return anchors then stops** (lines 290-294): the prefix commits
  `target`'s start and returns *before* `par`'s post-spawn actions — so a post-spawn
  cross-reference can never be pulled into the prefix. Each `advance` has its own
  `pendCur`/`clock` locals, so the prefix walk and `par`'s later full finish are
  independent and re-anchor children to the *same* value (first-write-wins no-op).
- **Zero-duration child** (the comment at lines 206-211): `joinUpTo(target.StartNS)`
  fires before `actSpawn(target)` (rank: join action's at ≤ spawn at), finishing a
  zero-duration `target` via the memo, and `finish` re-checks `s.finished[i]` (line
  234) — correct, no double-replay.
- **`inFlight[par]` bracket** (lines 348-350): set/advance/reset with no early return
  between; re-entry hits the in-flight guard → `fallbackAnchor`. Genuine-cycle break
  (lines 239-245) still fires on a true `inFlight[i]` re-entry. Sound.
- **`spawnTo` recursion** anchors ancestors and counts every recorded-offset fallback
  (`FallbackAnchors`) — no silent approximation. `setStart` now counts disagreeing
  overwrites (`SimStartConflicts`) — the right invariant surfaced.
- The sort (not in the diff) is `(at, actionRank)` from the existing code; the new
  ranks/at-values compose with it correctly.

## Landable? Blockers?

**Landable — yes.** All three of my Round-3 blockers are resolved by the
reformulation, the load-bearing finish-invariance is proven (and I could not break
it), order-independence is by construction for the common path, the full native suite
passes untouched, and the patch is correct. The mechanism is cleaner than what the
council asked for.

**Non-blocking follow-ups (do before/with merge, none gates correctness on the
measured workloads):**
1. **Surface `SimStartConflicts` on the what-if runs**, not just the baseline
   (report.go/gate.go currently baseline-only), and make `TestCrossRootAnchor` assert
   0 under a factor — this is the only hole in the order-independence guarantee
   (fallback corners under scaling).
2. **Perf watch:** `spawnTo` re-walks a parent's prefix per out-of-order reference —
   O(refs²) in a parent's action count worst case (child finishes are memoized, so it
   is the clock re-accumulation, not the subtree). Measured fine (0.28s/86k, 64-wide
   fan-out); memoize the per-(parent, spawn) prefix clock if a multi-million-op trace
   ever stresses it.
3. **Design reconcile:** the "reuse native replay UNCHANGED" premise is now formally
   dead — record the shared-replay soundness fix (and that it fixes a *latent native*
   bug, not an OTel-only one) in the design.
4. Confirm **jaccard identical pre/post-fix** and that the **−2.4%** is stable (Erik's
   asks) — expected, but report them to close the loop.
5. `FallbackAnchors` §6.1 signal: the lead confirms `TestGateFallbackAnchorsReportOnly…`
   passes unmodified (it now counts the cross-root recorded-offset anchors), so the
   signal is repurposed, not lost — fine; just document the new meaning.

## Summary

- **Landable: yes**, with the five non-blocking follow-ups above (the only one I'd
  insist on before trusting what-ifs broadly is #1, surfacing `SimStartConflicts`
  across the counterfactual runs).
- **op#250 self-time verdict:** faithful within the suppressed joiner's small pre-wait
  work — OTel slightly *over*-credits op#250 (not under-credits), confirmed small by
  the measured 0.03 drift; the `asModule`/`Host.directory` divergences are
  attribution/bucketing (work in child `:uploading` spans, total agrees), not error —
  the implementer is right.
- **Order-independence: real** — by construction for the normal prefix-vs-full-finish
  path (provably identical clock up to the spawn, under any factor), corroborated by
  `SimStartConflicts=0`; the residual fallback corners (cross-root/in-flight) are not
  by-construction but are *counted* — close the gap by surfacing that counter on the
  what-if runs.
- **Finish-preservation: proven** (self never overlaps a wait + `max`-commutativity);
  I could not construct a finish-changing case, including the `waitEnd<targetEnd`/ε
  family — which the patch's own boundary test resolves via the wait's own end.
