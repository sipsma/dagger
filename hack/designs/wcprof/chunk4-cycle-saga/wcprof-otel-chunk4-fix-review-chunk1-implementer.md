# wcprof × OTel — Chunk 4 cycle fix review (by the Chunk 1 implementer)

**Reviewer context:** I built the §6.1 gate and I know the `wcanalyze`
replay/loader best — and the proposed fix is now *in the shared replay*
(`engine/wcprof/wcanalyze/replay.go`), so this is squarely my territory. I
verified the mechanism from the replay code at my HEAD (`71b69f1f16`, which
carries the unchanged shared replay) rather than trusting the findings doc. I
could not access the 9.3 MB raw trace / native dump; where that limits the
*empirical* claims I say so.

## Plain verdict

- **Cycle correctly diagnosed: YES.** The mechanism — the anchor obtains an
  out-of-order op's *start* by replaying the parent's *full finish*, whose
  implicit join pulls in a concurrent cross-referencing **sibling** that waits
  back — is a **faithful reading of replay.go:317-321 + 351-369**. I confirmed it
  line-by-line.
- **`startOf` fix: correct, fundamental, and effectively side-effect-free** (with
  one refinement). It is the genuine fix to a real *replay* bug, not a paper-over,
  and decouples start-anchoring from the parent's finish — the right thing.
- **Changing the shared replay is justified here.** The design premise ("reuse
  the validated native replay unchanged, fix faithfulness at emit") rests on the
  replay being correct. This investigation **falsifies that for this case**:
  native cycles identically, so it is a genuine latent replay bug, and the emit
  is *not* the faulty layer (the structure is faithful). The owner's ruling holds.
- **My `FallbackAnchors` gate signal:** losing it entirely is a minor diagnostic
  regression; **keep the counter in `startOf`** (the implementer's refinement is
  the right call). My `TestGateFallbackAnchorsReportOnlyAndThreshold` failing means
  it **asserted the old mechanism's semantics**, *not* that the fix is wrong.
- **service.start erasure: a real, separate §3.4 faithfulness gap**, shared with
  native; the re-root direction is sound, with one caveat below.
- **Better alternative: none.** `startOf` is the cleanest fundamental fix; the
  emit re-root is correctly rejected.

## First — a correction to my own Chunk 4 review

In my Chunk 4 review I corroborated the council's topology: "op#94's subtree
**nests** op#54 … the implicit join turns the nesting into a causal dependency."
**That mechanism was wrong**, and I can now see why *from the code*, not just from
the implementer's word. Exp 2's discriminating evidence — **"no-anchor SCC ⇒ 0
cycles"** — rules out the nested-descendant theory: if op#54 were a *descendant*
of op#94, op#94's **own** `joinUpTo` (line 365) would reach op#54 with no anchor
needed, so removing the anchor would *not* remove the cycle. Because removing the
anchor *does* remove it, the back-edge **must** come from the anchor's
`finish(par)` (line 318) reaching a *sibling* via the **shared parent's** join —
i.e. op#54 and op#94 are siblings under `POST /query`, exactly as Exp 2 reports.
My higher-level conclusions survive (the cycle is false / acyclic real graph, the
gate is right to flag, the loader manufactures nothing), but my *mechanism* and my
*disposition* (I called it an emit gap to fix at Chunk 2) were wrong: it is a
**replay** bug. I own that; the empirical reframe corrected it and the code
confirms the reframe.

## Q1 — Verify the anchor over-reach from the code (CONFIRMED)

`finish(i)` (replay.go:308) for an unstarted op:

```
317  if !s.started[i] {
318      if par := s.p.parent[i]; par >= 0 && !s.inFlight[par] {
319          s.finish(par)                      // ← FULL parent finish, not "to the spawn"
320          if s.finished[i] { return s.simFinish[i] }
321      }
```

`s.finish(par)` runs the parent's entire body, including its implicit join
`joinUpTo` (line 353), which does `s.finish(c)` for **every** pending child `c`
ending by the action time (line 365). So to get op#94's start, the replay finishes
P, and P's join calls `finish(op#54)` — and op#54 is `inFlight` (we are inside its
replay, which hit `actWaitJoin(op#94)` at line 379 → `finish(op#94)`). That trips
the cycle break at line 341-344. **The closing edge is the anchor's `finish(par)`
over-reach, not the wait and not a nesting.** The implementer's description is
accurate to the code. The `actWaitJoin` classification of the *real* edge
(op#54→op#94, `waitEnd=195 ≥ targetEnd=195−ε`, replay.go:165) is also correct —
that edge is genuine; only the back-edge is invented. ✔

*Empirical residual:* that this specific cycle is the sibling-under-P shape (vs
some other anchor-dependent shape) rests on Exp 2/Exp 3, which I can't run. But the
shape is **forced** by the code (only `finish(par)` reaches a non-descendant), so
I'm confident. The extracted ~10-op cycle subgraph + the native "5 cycles" output
would make it airtight; I'd take them if the lead wants belt-and-suspenders, but I
don't need them to confirm the *mechanism*.

## Q2 — Is `startOf` the right start, and is the counterfactual staleness new unsoundness?

**It is a bounded, baseline-exact approximation — not new unsoundness — and it
changes only the out-of-order case, as claimed.** Four grounded reasons:

1. **In-order results are byte-identical.** An in-order op is started by its
   parent's `actSpawn` (line 378, `setStart(a.ref, clock)`) *before* `finish` is
   ever called on it, so `if !s.started[i]` (line 316) is false and `startOf` is
   never reached. The change is confined to ops reached out-of-order via a
   cross-tree reference. ✔ (matches the implementer's claim)
2. **At baseline (factor 1) it is exact.** The counterfactually-correct spawn time
   is `parent-start + parent's pre-spawn clock`, which at factor 1 equals
   `parent-start + (recorded child-start − recorded parent-start)` — precisely the
   `startOf` offset. So baseline makespan is unchanged (the reported −0.3% is vs
   *actual*, i.e. noise-level; vs the old *baseline* it should be ~0).
3. **It is not a new computation — it generalizes the existing fallback.** The old
   code's fallback anchor already used this exact formula (replay.go:329-330:
   `anchor = s.simStart[par] + (startNS[i] − startNS[par])`). `startOf` just
   applies it unconditionally for out-of-order ops and **drops the cycle-prone
   `finish(par)` attempt** — and improves on the old fallback by *recursing* to
   anchor relative to the nearest *started* ancestor (so the subtree shifts with
   its anchor under a counterfactual, rather than snapping to a raw recorded start
   as line 327's `anchor := s.p.startNS[i]` did when the parent was unstarted).
4. **Versus the case it actually replaces, it is *more* accurate.** The
   out-of-order op's old path was either `finish(par)` → **cycle** → break at
   recorded *duration* (line 344, ignoring scaling of the op's own work entirely),
   or the fallback offset. `startOf` gives the offset start and then **replays the
   op's own actions with scaling** — strictly better than assuming recorded
   duration. The residual staleness (the recorded *offset* not reflecting scaling
   of the parent's pre-spawn path) is **second-order** (it perturbs only the
   out-of-order op's *start*, by the amount that one parent-prefix was scaled) and
   the implementer's fixtures — `wcanalyze`'s own counterfactual tests + all
   chunk2/3/4 oracle/ranking fixtures — pass. So it does not move rankings.

Net: `startOf` is the correct start for an out-of-order op under the model. It
trades a sliver of counterfactual precision (already absent in the old fallback /
cycle-break) for the guarantee of no false cycle — which, per the owner's
unsoundness ruling, is the right trade.

## Q3 — My `FallbackAnchors` §6.1 signal

**Keep a counter in `startOf` (refinement = correct); don't drop the signal
silently.** My read:

- The signal was a "detached/re-pointed work" diagnostic (replay.go:226-231,
  report-only in gate.go:131). Most of what it counted *was* the same out-of-order
  anchoring `startOf` now handles cleanly — so much of it was "symptom of the
  over-reach." But not all: a genuinely-detached op (lazy re-point reached
  out-of-order) is real cross-tree structure worth surfacing. **Zeroing it loses
  that.** So keep a counter when `startOf` anchors an out-of-order reference, and
  **reframe its meaning** from "fallback (something went wrong)" to "out-of-order
  anchor (informational)." It stays report-only (gate.go:131 already gates on
  `MaxFallbackAnchors > 0`), so the gate's PASS/FAIL is unaffected either way.
- **My `TestGateFallbackAnchorsReportOnlyAndThreshold` failing is *not* a
  fix-is-wrong signal.** I wrote it (gate_test.go) to build cross-root wait-joins
  (roots B,C reached out-of-order by root A's waits) and assert `FallbackAnchors >
  0`. Those roots have `par = -1`, so they never hit the buggy `finish(par)`
  over-reach — they go straight to the recorded-start anchor (line 326-327). That
  was *correct* handling, and `startOf` handles it identically (recorded start),
  just without bumping the counter if the counter is removed. So the test asserted
  the **old counter semantics**, not buggy makespan behavior. With the counter
  refinement it is salvageable (the same construction still produces out-of-order
  anchors → count > 0); its purpose (exercise the gate's report-only threshold)
  remains valid. Update it to the new counter name/semantics — don't read its
  failure as the fix breaking something.

## Q4 — Fundamental fix vs paper-over, and the right layer

**Fundamental, and correctly in the replay.** The real dependency graph
(op#54→op#94, both children of P) is **acyclic**; the cycle was *manufactured* by
the anchor coupling an op's *start* to its parent's *full finish* (which drags in
unrelated sibling joins). `startOf` **decouples start from finish** — a start
should depend only on *when the parent spawns this op*, never on the parent
joining its *other* children. That is the principled correction of the model, not
a tolerance of bad structure (the structure is fine; the replay was over-reaching).

On the layer: the design's "replay unchanged, fix at emit" premise is **falsified
for this case** — native builds parentage from its own context key and **cycles
identically**, so this is a latent bug in the shared analyzer (PR #13393), not an
OTel-emit faithfulness issue. Fixing it in the shared replay fixes both sources in
one place and keeps the oracle meaningful (an OTel-only replay change would diverge
from native). The **emit re-root alternative is correctly rejected**: re-rooting
`call_exec` off the caller tree turns every execution into an anchorless root, so
*everything* becomes out-of-order — hence its fallback-anchor explosion 18→3474
(Exp 4). That is strictly worse and treats a symptom at the wrong layer. I looked
for a smarter middle path (replay the parent only *up to* the child's spawn) and it
doesn't help — if P joins op#54 *before* op#94's spawn, partial replay still
cycles; pure offset (`startOf`) is the clean avoidance. **No better alternative.**

One guardrail I'd require before landing, since it touches native's validated
analyzer: confirm `engine/wcprof/wcanalyze/replay_test.go` (PR #13393's own
counterfactual tests) pass unchanged — the implementer says "wcanalyze's own
counterfactual tests pass," and that is the load-bearing regression check for not
breaking native. I'd want that stated explicitly per-test, not just in aggregate.

## service.start erasure (separate finding) — real, fix direction sound

**Verified real from the code**, and it is *worse* than the "idle daemon doesn't
rank" point I noted in my Chunk 4 review (I undercalled it — the fresh-Codex
minority was right). `service.start` brackets `svc.Start` `[10,60]`, and the daemon
`exec.run` is begun under `svcCtx` (which descends from `service.start`), so
`exec.run` `[~12, session-end]` is a **child** of `service.start`. Self-time =
interval − children (graph.go SelfSegments), so the health-check window `[12,60]`
is subtracted as child time and `service.start` self collapses to `~[10,12]` = the
reported 2 ms. **A genuinely slow start does not headline** — and it's **on the
critical path** (installers wait for it), unlike the idle daemon. Shared with
native (same parentage). The re-root direction (begin the daemon under a context
*not* descending from `service.start`, in **both** sources) is correct and is the
honest fix. **Caveat to flag:** re-rooting the daemon to a standalone root makes it
a root in `ActualMakespanNS` / the replay's root chain (replay.go:265-297); its
`[~12, session-end]` interval should not extend makespan beyond the session root,
but verify it doesn't perturb the root set / drift on a real service trace before
landing. This is a distinct fix from `startOf`; both are needed for v1, both belong
in both sources.

## Landed lazy-exec test (`8c331d8272`) — clean, and it validates my Chunk 3 call

Test-only (one file, `dagql/otelprof_lazy_exec_test.go`, +140; touches no
replay/loader/gate/emit). It asserts exactly the composition I *predicted* in my
Chunk 3 review: a lazy-triggered `exec.run` is the direct re-pointed child → gets
`wcprof.parent` stamped, its `containerStart`/`processRun` phases are descendants →
stay unstamped and follow their ancestor, the loader re-homes the exec subtree
under the lazy op with `work_type=user` surviving, gate green — "no change to the
processor (kind-agnostic discriminator)." Good closure on the Chunk 3 → Chunk 4
seam. ✔

## What I'd need to go further

I confirmed the **mechanism** and the **fix's correctness** from the replay code
alone. The **empirical** claims I relied on the implementer for — native cycles
identically (Exp 3), siblings-not-nested (Exp 2), drift −0.3% — are *consistent
with* and *forced by* the code, but to make them airtight the cheap artifacts are:
(1) the extracted ~10-op cycle subgraph (parent + wait edges) to see the sibling
topology directly; (2) the native dump's cycle count on the same run; (3) the
per-test `replay_test.go` pass list under the prototype. I did **not** rerun the
prototype (it isn't landed). If the lead wants me to verify the empirics rather
than the mechanism, those three are what I'd ask the implementer to extract.

## Bottom line

The cycle is **correctly diagnosed** as a shared-replay anchor over-reach (I
confirmed the mechanism at replay.go:317-321/351-369 and corrected my own earlier
wrong topology). **`startOf` is the correct, fundamental, side-effect-bounded fix**
— baseline-exact, in-order-identical, strictly better than the cycle-break it
replaces, and correctly placed in the shared replay because native shares the bug
and the emit is faithful. **Land it, with the counter refinement** (preserve the
out-of-order-anchor diagnostic, reframed; update my `TestGateFallbackAnchors…`
to the new semantics — its failure is expected, not a defect), and **confirm
`replay_test.go` passes per-test** as the native-regression guard. The
**service.start erasure** is a separate, real §3.4 faithfulness gap to fix in both
sources via the daemon re-root (verify the root-set/makespan effect). Both fixes
honor the owner's principle — fundamental, whole-system, no seams. No better
alternative than `startOf`; the emit re-root is rightly rejected.
