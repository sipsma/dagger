# wcprof × OTel — Chunk 4 cycle FIX review, Round 2 (by the Chunk 2 implementer)

**Reviewer context:** I built Chunk 2 (the `call_exec` singleflight emit, the
suppressed-caller-wait-on-ancestor rule). In Round 1 I proposed a *mechanism and a
fix direction that the implementer's empirical investigation has now contradicted.*
This review owns that correction, independently re-verifies the new diagnosis
against the replay code, and evaluates the proposed fix. Review only; no code, no
commits.

## TL;DR

- **My Round-1 theory was wrong, and I accept the correction.** I claimed op#54 was
  a *detached descendant* of op#94 and the cycle was a false implicit-join-over-
  nesting closed by my true wait — pointing at an **emit-side re-root**. The
  evidence refutes all of that: op#54/op#94 are concurrent **siblings** (Exp 2), the
  cycle is the replay's **anchor over-reach** (Exp 5), an emit re-root is empirically
  awful (Exp 4 → 3474 fallback anchors), and — decisively — **native cycles
  identically** (Exp 3), which an OTel-emit/false-nesting theory *cannot* explain.
- **The diagnosis is correct and I independently re-derived it from the code.** The
  one thing I had right survives and the fix preserves it: **the singleflight wait
  `op#54→op#94` is TRUE and load-bearing; the fix does not touch it.**
- **The start-only-anchor fix is correct, fundamental, and the right layer.** It is
  *not* a paper-over. One real caveat (the `FallbackAnchors` §6.1 signal) and one
  design reconcile (the "unchanged replay" premise) must be handled.
- **The `service.start` erasure is a real, separate shared bug; the both-sources
  re-root is the right fix.**

## 1. Re-examining my Round-1 theory against the correction

**Is the topology correction (siblings, not nested) right? Yes — and Exp 3 settles
it independently of the raw trace.** My Round-1 read hinged on op#54 being nested
under op#94 (op#94's resolver spawning op#54's concurrent load on op#94's context).
Two independent things refute it:

- **Exp 3 is the killer.** Native builds parentage from its **own wcprof context
  key**, not OTel traceparent (`engine/wcprof/record.go` `BeginOp` reads
  `CurrentOpID` from a wcprof key). If the cycle were an OTel-emit false-nesting
  artifact (my Round-1 theory), native — which never sees OTel's traceparent
  nesting — could not reproduce it. It reproduces **the identical 5 cycles**. So the
  cycle is in the *shared* model (detached `call_exec` + concurrent singleflight +
  the replay), **not** OTel emit. My "OTel false nesting" framing is dead.
- **The `actWaitJoin` timing wall I noted in Round 1 actually argued *against* my own
  theory.** I observed op#94 ends at 195 and op#54 at 205, so anything in op#94's
  subtree (ending ≤195) *cannot* `actWaitJoin` op#54 (`replay.go:165`,
  `w.EndNS ≥ w.Target.EndNS−ε`). I waved that away; I should have followed it. It
  means op#94's subtree does **not** depend on op#54 by a join — so the back-edge is
  not an implicit-join-over-nesting. It is the anchor (below).

**Does the anchor explanation supersede my nesting explanation, or are both in play?
It supersedes it — my nesting explanation was simply wrong** (siblings, and native-
reproducible). And note: because op#54/op#94 are in **different sibling subtrees**,
my §3.1 *"joiners in a different subtree"* assumption is **satisfied**, not violated.
My Round-1 "Chunk-2 §3.1 gap" framing is also withdrawn. This is not a singleflight-
emit gap at all.

**What I verified in the code (not just trusting the findings):**

- **The over-reach is real.** `finish(i)` anchors an unstarted op by calling
  `s.finish(par)` — the parent's **full** finish (`replay.go:318-319`), whose
  implicit join `joinUpTo` (`replay.go:353-369`) pulls in **all** the parent's
  children. So to get a child's *start*, the replay replays the parent *and every
  sibling it joins*. When the out-of-order child's sibling (op#94) is mid-replay
  (`inFlight`), the over-reach re-enters it → `inFlight` cycle-break
  (`replay.go:341-345`). The SCC therefore **requires** the child→parent anchor
  edge — exactly Exp 2's "no-anchor SCC = 0." Code-consistent.
- **The true wait is untouched.** The fix changes only the anchor block; `c.wait`'s
  `EmitOTelWait(op#54→op#94)` and its `actWaitJoin` classification are unchanged.
  Confirmed: the load-bearing edge (the Break #1 fix) survives. ✔

I'd **welcome** the extracted cycle subgraph (the raw `parentId` chains + the exact
op#94-side edge that first references op#54) and the native cycle output to make the
sibling topology 100% airtight — but they would *confirm*, not change, the verdict:
Exp 3 + the over-reach code already refute my Round-1 and establish the shared-replay
cause.

## 2. The start-only-anchor fix — correct, fundamental, right layer

**Correctness — and why the "recorded-offset" start is not a new approximation.**
The fix replaces the over-reaching `finish(par)` with `startOf`, which computes
`startOf(parent) + (startNS[i] − startNS[parent])`. That formula is **byte-identical
to the fallback the code already uses** at `replay.go:331`. So the fix does not
*introduce* the recorded-offset approximation — it **generalizes the existing
fallback** and deletes the `finish(par)` over-reach. `startOf` only ever climbs the
**parent tree** (acyclic by construction); it never traverses wait/join edges, so it
**cannot** cycle. That is precisely why it works: it decouples *start-anchoring* from
*finish-recursion*, and finish-recursion (over join/wait edges) is the only thing
that can cycle.

**The scaled-start question the lead raised — and why the approximation is
*necessary*, not a weakness.** Yes: an out-of-order op's start now uses the recorded
offset, so a what-if that scales the parent's *pre-spawn* path would mis-anchor it
(the in-order case is untouched — `actSpawn` still sets the scaled clock,
`replay.go:377-378`, and `startOf` runs only when `!started[i]`). But this is
unavoidable for a cycle-free anchor:

- Getting the *exact scaled* start of a concurrent op requires replaying its parent's
  pre-spawn work — i.e. join/finish recursion — which is exactly what re-introduces
  the cycle. A "partial replay up to the spawn point" alternative does **not** escape
  it: if the cross-referencing sibling spawned *before* the out-of-order op, the
  partial replay still joins it and still re-enters an `inFlight` op. So **any**
  attempt to be more-exact than the recorded offset reopens the cycle.
- The old code already accepted this exact approximation in its fallback
  (`:331`); the fix just stops *sometimes* doing the cyclic `finish(par)` first. So
  the soundness trade is "exact-but-cyclic for some out-of-order ops" →
  "approximate-but-acyclic for all." For the owner's soundness bar, acyclic strictly
  wins, and the residual is bounded (empirically −0.3% makespan, all oracle/ranking
  fixtures pass). **This is the fundamental fix, not a paper-over** (it is not a
  cycle-break, not edge suppression, not data massaging — it removes the over-reach
  at the root).

**Right layer? Yes, per the owner's ruling — and the emit alternative is refuted.**
The owner's standing principle ("a bug is a bug even if native shares it; fix the
whole system") makes the **shared replay** the correct site: native has the identical
latent bug (Exp 3), and only a shared-replay fix repairs both sources at once. My
Round-1 emit re-root is doubly wrong: Exp 4 shows it explodes fallback anchors
18→3474 (re-rooting orphans every `call_exec`, destroying the very start-anchoring
the replay needs), **and** it is OTel-only — native would still cycle. So the
implementer's rejection of the emit re-root is sound, and the start-only anchor is
the better fix on every axis.

**Side effects — one real, must be handled:**

- **It zeroes the `FallbackAnchors` §6.1 signal.** That signal flags detached/
  re-pointed work and is a genuine regression detector for *future* emit chunks; if
  the fix makes it structurally always-0, we lose a diagnostic the council has leaned
  on. **Recommend (not optional): implement the implementer's refinement** — have
  `startOf` increment a counter when it anchors an *out-of-order reference* — so the
  diagnostic survives as "out-of-order anchors," and the gate keeps a meaningful
  signal. Without it, the §6.1 bounded-fallback-anchor gate becomes vestigial.
- **`TestGateFallbackAnchorsReportOnlyAndThreshold` must be updated.** I read it
  (`gate_test.go:227-252`): it deliberately builds a root that wait-joins two later-
  starting roots to *generate* fallback anchors, then asserts the report-only +
  threshold behavior. The fix removes that fallback-anchor source, so the test fails
  on `FallbackAnchors == 0`. Updating it is **correct** (it asserts the old buggy
  behavior) — but it should be re-pointed at the refinement counter above, so the
  threshold mechanism stays under test rather than being deleted.

**Design reconcile to flag:** the design's premise was *"reuse the validated native
replay UNCHANGED"*. This fix changes the shared replay (and thus native). That is
*aligned with the owner's ruling*, but the premise is now false and the design doc
must record: the replay carries a soundness fix (anchor over-reach → start-only),
fixing a latent native bug PR #13393's tests didn't cover (concurrent cross-
referenced shared-work).

## 3. The `service.start` erasure + re-root fix — real, separate, fix is right

**Verified in code, and it is a genuine §3.4 gap.** `beginOTelServiceStart` starts
`service.start` on `svcCtx` and returns the updated `svcCtx`
(`core/services.go:215-222`); `svc.Start(svcCtx, …)` runs the daemon under it, so the
long-lived daemon `exec.run` (Chunk 4) becomes a **child** of `service.start` and its
interval (the whole start+health window, and beyond) is subtracted from
`service.start`'s self-time (`graph.go` `SelfSegments` subtracts children). Hence the
empirical 50ms window → 2ms self / 1ms SavedNS: a slow service start does **not**
headline. The design said "leave the availability span non-self-time-bearing" but
never prevented the daemon exec from **absorbing** `service.start`'s own self. Shared
with native (native's `service.start` op likewise parents the daemon exec under
`svcCtx`). **The both-sources re-root is the right fix** — an OTel-only re-root would
desync from native and break the oracle, and the owner's ruling requires fixing both.
This is a *distinct* shared-model bug from the cycle (both surfaced by the first
service/module workloads), and both deserve the same "fix fundamentally, both
sources" treatment.

## 4. The landed lazy-exec composition test (`8c331d8272`) — good, low-risk

Verified: purely additive (`dagql/otelprof_lazy_exec_test.go`, +140, on top of Chunk
4, touches nothing else). It closes the Chunk 3 reviewer's flagged gap — asserting a
lazy-triggered `exec.run` (a *direct* re-pointed child) is stamped `wcprof.parent`=
lazy op while its phases (descendants) stay unstamped, the loader re-homes the exec
subtree under the lazy op with `work_type=user` surviving, gate green. Sound to land;
no concerns.

## Verdicts

1. **Cycle correctly diagnosed, including the topology correction? YES.** Concurrent
   **sibling** module-source executions (not nested), cross-referencing via my TRUE
   singleflight waits, with the cycle closed by the **replay anchor's over-reach**
   (`finish(par)` full finish to obtain a child's start). **Native reproduces it
   identically (Exp 3)** → a shared `wcanalyze`-replay bug, not OTel emit. My Round-1
   nested/emit theory is withdrawn; I independently re-derived the anchor mechanism
   from `replay.go:318-319,353-369,341-345`.
2. **Fix correct, fundamental, side-effect-managed, aligned? YES, with two musts.**
   The start-only anchor (`startOf`) removes the over-reach at the root, is acyclic by
   construction (climbs only the parent tree), preserves the true singleflight waits,
   and the recorded-offset start is the *necessary* (and pre-existing, `:331`) price
   of a cycle-free anchor — not a paper-over. **Must:** (a) add the out-of-order
   counter to preserve the `FallbackAnchors` §6.1 diagnostic; (b) update
   `TestGateFallbackAnchorsReportOnlyAndThreshold` onto that counter. **Flag:** the
   design's "unchanged replay" premise is now false — reconcile it.
3. **Service fix? YES** — real erasure, shared with native, both-sources re-root is
   correct and oracle-preserving.
4. **Better alternative? No.** Emit re-root is refuted (Exp 4 + OTel-only); partial-
   replay-to-spawn still cycles; cycle-break/edge-suppression are paper-overs the
   owner rightly forbids. `startOf` is the right fix.

**Net:** the implementer went where the evidence pointed and corrected the council
(including me). The fix is sound and fundamental. Land it **with** the fallback-anchor
counter refinement + the test update + the design reconcile, and land the
`service.start` both-sources re-root alongside. I'd welcome the cycle subgraph +
native cycle dump to make the write-up airtight, but they don't change this verdict.
