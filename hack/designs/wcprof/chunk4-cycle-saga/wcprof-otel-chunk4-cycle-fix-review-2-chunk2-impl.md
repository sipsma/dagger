# Chunk 4 cycle fix — Round-3 review (by the Chunk 2 implementer)

**Charge:** my Round-3 "prefix-spawn doesn't cycle" proof reasoned via the
implicit-JOIN cutoff. The implementer's residual lives in the pre-spawn explicit-
WAIT path, which my proof did not cover. Does my cutoff argument extend there, or is
the skip-predicate genuinely required? Plus the 5 shared questions. Verified against
my worktree's `replay.go` + the implementer's doc. Review only; no code, no commits.

## First: my Round-3 acyclicity proof was INCOMPLETE — I own it

My proof said "a prefix-replay of `A` to `T`'s spawn reaches only ops ending ≤
`t_spawn`, because `joinUpTo` only joins children ending ≤ the cutoff
(replay.go:356)." That covers the **implicit-join** path. It silently assumed the
parent's pre-spawn **explicit waits** (`actWaitJoin`, replay.go:379-382) are also
bounded by the cutoff. **They are not.** A parent's own pre-spawn wait has a
`ref`/target whose recorded end can be **after** `t_spawn`; processing it during the
prefix calls `finish(target)`, escaping the ≤ `t_spawn` reachable set. That is
exactly the residual: op#250's prefix (to op#251's spawn at 190 ms) processes
op#250's own wait on D (ends 205 ms) → `finish(D)` → D waits A (in-flight) → cycle.

So my cutoff argument **does not extend** to the pre-spawn explicit-wait path, and
**the skip-predicate is genuinely required** for prefix-spawn to stay acyclic under
OTel's attribution. My Round-3 proof held only for the native-shaped structure
(waits attributed to **children**, which the `joinUpTo` cutoff does exclude); it
failed for OTel's §3.1 structure (waits attributed to the **parent's own** span,
which `joinUpTo` never touches). I should have separated the two action paths.

## Charge 1 — EMIT vs REPLAY: legitimate general fix, §3.1 is faithful, NOT a seam

The residual surfaces because §3.1 lands a *suppressed concurrent* sub-caller's wait
on op#250's `call_exec` span; native records a separate `call` op (a child) and so
the wait sits on a child the cutoff already excludes. The question is whether the
skip-predicate fixes a general replay defect or papers over an OTel emit bug.

**It is a general replay-gating fix.** The wait is *faithfully recorded*: a
concurrent descendant of op#250 genuinely blocked on D over `[start,205]`. The
interval is true; the only thing the replay must decide is whether that wait *gated*
op#251's spawn — and the recorded ordering answers it (op#251 spawned at 190, the
wait ended at 205, so the wait was **concurrent with** the spawn and did not gate
it). The skip-predicate reads that recorded ground truth — the **same principle**
the join path already uses via the `joinUpTo` cutoff, and the same family as
`actWaitNoop` (waitEnd < targetEnd ⇒ didn't gate). Making the parent-attributed
wait obey the same gating-cutoff the child-attributed wait already obeys is a
*consistency* fix, not a paper-over, and it is forward-general: any source that ever
attributes a wait to a parent op (a future native change, a synchronous resolver
with an internal concurrent wait) benefits identically.

**Is §3.1's attribution faithful? Yes — including self-time.** I checked the worry
that putting the concurrent wait on op#250 under-credits op#250's self-time vs
native. It does not, to first order: native subtracts the concurrent sub-call's
**child interval** `[start_CC,205]` from op#250's self (`SelfSegments`); OTel
subtracts the **wait interval** `[start_wait,205]`. Since the suppressed caller
starts then immediately waits, `start_CC ≈ start_wait`, so both sources subtract ≈
the same blocked interval — op#250's self-time matches within the small `start_CC −
start_wait` gap. So §3.1 is faithful on both the interval *and* the self-time; the
*only* divergence native dodges by accident (child vs own-span) is the gating
interpretation, which the skip-predicate repairs generally. **Fix belongs in the
replay; no emit change is warranted, and §3.1 stays.** *(Worth a one-line oracle
spot-check that op#250's `call_exec` class self-time matches native on the real
trace, to confirm the `start_CC ≈ start_wait` assumption holds in practice.)*

## Charge 2 — skip-predicate soundness: correct, with a bounded ε misclassification

`endNS[target] ≤ startNS[spawn]` is the right *shape* of test. A **genuinely
gating** wait means the parent blocked on it and only then spawned the child, so its
end precedes the spawn ⇒ `≤` is true ⇒ processed (not skipped). A **concurrent**
wait means the parent spawned the child while still waiting ⇒ its end follows the
spawn ⇒ skipped. So it does **not** under-serialize genuine gating in the exact case.

**But it uses `endNS[a.ref]` (the target's recorded end) as a proxy for the wait's
end**, because the compiled `action` (replay.go:55-60) stores only the wait's
*start* (`at`), not its end. For a JOIN, `waitEnd ≈ targetEnd` only within
`joinEpsilonNS` (1 ms; the classifier is `waitEnd ≥ targetEnd − ε`, replay.go:165).
**Misclassification case:** a parent synchronously waits on Z, unblocks at `t`
(`waitEnd = t`), and spawns the child at `t`; Z's *target* ends at `t + δ` with `0 <
δ ≤ ε`. The wait genuinely gated the spawn, but the predicate sees `endNS[Z] = t+δ >
t = startNS[child]` → **skips it → under-serializes** (and, in a what-if that scales
the gating work, over-credits the speedup). The error is bounded by ε (1 ms), and
the cycle-closing waits are far outside it (D ends 205, spawn 190 — a 15 ms gap), so
the cycle fix is robust; but for *exactness* the fix should thread the wait's actual
recorded end through the compiled action and test `waitEnd ≤ startNS[spawn]`. The
implementer flags this as optional — given Erik's accuracy ruling, I'd make it
**required** (it is the difference between "gating-correct" and "gating-correct ±1
ms," and it removes the only soundness asterisk).

## Charge 3 — residual taxonomy: two distinct classes, two-tier policy correct + complete

- **Spurious over-serialization** (NOT a real cycle): the anchor over-reach *and* a
  concurrent non-gating pre-spawn wait. Both are artifacts of the replay's gating
  logic serializing things the recording shows were concurrent. **Eliminate** them
  (prefix-to-spawn + skip-predicate). No counterfactual is lost — they were never
  real dependencies.
- **Genuine recorded cycle**: a true circular dependency in the data (mutual
  `actWaitJoin` that both genuinely gated). No well-defined counterfactual ⇒
  **break + report** (recorded duration, the §1.5 mechanism, replay.go:341-345).

The two-tier policy — eliminate spurious, break+report genuine — is **correct and
complete for the action types**: spawns/self carry no cross-reference; child joins
are gated by the `joinUpTo` cutoff; explicit waits are gated by the skip-predicate.
The unifying principle is precise: **a wait gates an op's own FINISH (always
processed, normal loop) but gates a CHILD's SPAWN only if it ended by the spawn
(skip-predicate).** After both, any remaining cycle is genuine and the break is the
right answer. One caveat (Charge 4): the policy is currently only enforced in the
*prefix*, not the normal finish — so "complete" holds for cycle-elimination but not
yet uniformly for the spawn-anchor value.

## Charge 4 — first-write-wins: works here, but order-dependent and fragile

`spawnTo` sets op#251's start *ungated* (correct, 190); op#250's later **full**
finish processes the same pre-spawn D-wait through the *normal* loop (replay.go:379-
382 — **no skip-predicate there**), so when it reaches `actSpawn(op#251)` its clock
is already ≥ `finish(D)` ≈ 205, and it would set op#251 = 205 (over-serialized).
`setStart` is first-write-wins (replay.go:300), so whichever path runs first sticks.

In the residual ring the prefix runs first (A references op#251 out-of-order before
op#250 is finished) ⇒ 190 wins ⇒ correct. **But correctness is replay-order-
dependent:** if op#250's full finish runs *before* op#251 is referenced out-of-order
(a different root/join order, or a what-if that reorders), op#251 = 205 wins ⇒ the
concurrent wait wrongly gates the spawn. This is not a cycle blocker (the cycle is
gone), and it is no *worse* than today's code (the current full-finish anchor already
over-serializes here) — but the fix is **incomplete on accuracy**: the
concurrent-wait-doesn't-gate-spawn principle is applied in `spawnTo` only, not in the
normal loop's `actSpawn`. For an order-independent, robust fix, the skip-predicate
(or an equivalent "anchor children at the pre-wait clock") should govern the spawn
anchor in **both** paths, so op#251 = 190 regardless of which runs first. As written,
it relies on the prefix-first ordering that the measurements happen to exercise.
**Flag this as the top robustness gap to close before landing in native.**

## Charge 5 — remaining blockers

- **Native per-test regression (highest priority).** The fix is in the *shared*
  replay → it changes native + PR #13393. The doc reports the wcotel/oracle fixtures
  pass and the native *real trace* improves, but does **not** confirm native's own
  `wcanalyze/replay_test.go` suite passes (beyond the one fallback-anchor test known
  to need updating). Erik's "stress-test before it lands in validated native code"
  demands running the full native replay/counterfactual unit suite and reconciling
  every delta — required before landing.
- **Root scheduling / cross-root anchoring (untested).** The original-frame rationale
  (replay.go:29-34) exists *specifically* because cross-tree/cross-root waits anchor
  ops at original times before their root is replayed. The fix rewrites that anchor,
  but both fixtures are single-root. `spawnTo` climbs `parent` and falls back to a
  recorded offset when the parent's parent is in-flight (proposed lines 178-184); its
  behavior when the out-of-order target's parent lives under a *different, not-yet-
  scheduled root* must be tested — that is the exact case the rationale warns about.
- **ε / wait-end proxy** (Charge 2): make the wait-end exact, don't ship the ±ε
  proxy.
- **`FallbackAnchors` §6.1 diagnostic → 0.** Keep a refined counter (increment in
  `spawnTo`'s in-flight/self-ref fallback, lines 186-188) so the detached-work signal
  survives; update `TestGateFallbackAnchorsReportOnlyAndThreshold` onto it (same as
  my Round-2 note).
- **Perf:** `spawnTo` re-walks a parent prefix per out-of-order reference; measured
  ~0.18 s/11k ops (fine). Memoize the per-(parent, spawn-point) prefix clock if
  multi-million-op traces ever stress it (the doc flags this).
- **Design reconcile:** the "reuse native replay UNCHANGED" premise is now dead —
  record the shared-replay soundness fix in the design (carries from Round 2).

## Summary

- **Does my acyclicity proof survive the pre-spawn-wait path? No.** It covered only
  the implicit-join cutoff; the parent's pre-spawn explicit `actWaitJoin` (target
  ending after the spawn) escapes it. **The skip-predicate is genuinely required** —
  I own the gap.
- **Emit-vs-replay:** **fix in the replay; §3.1 is faithful** (it records the true
  blocked interval and subtracts ≈ the same self-time native does — only the gating
  attribution differs, which the skip-predicate repairs generally). **Not a forbidden
  seam** — it generalizes the existing join-cutoff gating principle to the wait path.
- **Skip-predicate soundness:** correct in the exact case (no under-serialization of
  genuine gating); **one bounded ε misclassification** from using `targetEnd` as the
  wait-end proxy (a gating wait whose target ends ≤ ε after the spawn is wrongly
  skipped) — make the wait-end exact to remove it.
- **Remaining blockers:** (1) run the **native replay unit suite** and reconcile —
  it touches validated native code; (2) **first-write-wins is order-dependent** —
  apply the no-gate-on-concurrent-wait rule to the normal `actSpawn` too, not just
  `spawnTo`, for order-independent accuracy; (3) **test cross-root anchoring** (the
  case the original-frame rationale exists for); (4) exact wait-end; (5) keep a
  refined fallback-anchor counter; (6) perf memo if needed. **Verdict: prefix-to-
  spawn + the recorded-back-edge skip-predicate is the right, fundamental direction
  and Erik's adoption is sound — but it is not yet landable: close the order-
  dependence and the native-suite/cross-root validation first.**
