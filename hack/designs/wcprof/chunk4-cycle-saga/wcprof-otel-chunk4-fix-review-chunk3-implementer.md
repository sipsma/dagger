# wcprof × OTel — Chunk 4 cycle-fix review (by the Chunk 3 implementer)

**Reviewer context:** I implemented Chunk 3 (lazy / `wcprof.parent` stamping) and
flagged in Round 1 that the original "Chunk 2 only" attribution had not ruled out my
stamping, proposing the `wcprof.parent`-ignored loader recompile. That diagnostic was
run (cycles 5→8) and **vindicates the exoneration**. Round-2 scope: evaluate (Q1) the
exoneration + diagnostic validity, (Q2) the cycle diagnosis + the start-only-anchor
replay fix, (Q3) the lazy-exec composition test (`8c331d8272`) + the `service.start`
erasure finding. Reviewed `8c331d8272` (test) on `4d6987fdc2` (Chunk 4) against the
findings doc + `engine/wcprof/wcanalyze/replay.go`. **Review only — no code, no
commits.** Raw trace/native dump live in the implementer's container (not accessible);
I reason from the findings + the replay code and call out where a code-confirmation
suffices vs. where I'd want the extracted subgraph.

## Headline verdicts

1. **My Chunk 3 stamping is genuinely exonerated, and the diagnostic establishes it
   correctly** (Q1). `wcprof.parent`-ignored → cycles 5→8 means the cycle persists
   *and increases* without my re-homing — it is not the cause, and my stamping
   *reduces* cycles (3 of them). Combined with the Exp-2 topology (siblings, not
   nested), no cycle edge is a `wcprof.parent` edge.
2. **The cycle is correctly diagnosed** (Q2): a shared native+OTel **replay-anchor
   over-reach** in `replay.go:317-323` — `finish(child)` calls `finish(parent)` (the
   parent's *full* finish) to anchor an out-of-order-referenced child, and the
   parent's implicit join pulls in the concurrent cross-referencing sibling that waits
   back → the invented back-edge. Native cycling identically (Exp 3) confirms it is the
   shared replay, not OTel emit.
3. **The start-only `startOf` fix is correct, fundamental, and at the right level —
   with two real caveats to address before landing** (Q2): it zeroes the
   `FallbackAnchors` §6.1 signal (keep an out-of-order counter), and the prototype
   `startOf` lacks the cycle-guard the current `finish` has. The lead's specific worry
   — `parent-start + RECORDED offset` under a counterfactual that scales the parent's
   pre-spawn path — is **real but bounded**: it is an *accuracy* approximation (exact
   in baseline), **not** a soundness regression, and the −0.3% drift + passing oracle
   fixtures bound it. I did **not** find a better alternative; the emit re-root is
   correctly rejected.
4. **The lazy-exec composition test is sound** and closes my Round-1 LOW finding (Q3).
5. **The `service.start` erasure is real, and I should have caught it in Round 1**
   (Q3). The fix direction (re-root the availability span *out of* `service.start` in
   both sources) is correct; I flag the implementation details.

---

## Q1 — Is my stamping exonerated, and was the diagnostic run correctly?

**Yes on both, and the result is conclusive.**

- **The diagnostic is the right one and I trust the result.** `wcprof.parent`-ignored
  forces the loader's `causalParentSpanID` (`wcotel/loader.go`) to fall back to raw
  `parentId` for every op — exactly removing my Chunk 3 re-homing while leaving all
  other structure intact. Cycles 5→8 (and fallback anchors 18→21) means: (a) the cycle
  is **not created by** my stamping (it persists without it), and (b) my stamping
  **removes 3 cycles** — consistent with its §2.5 design intent (re-home lazy work off
  the already-ended producer onto the live consumer-side lazy op, which is what avoids
  the producer-ended cycle class). My Round-1 hypothesis holds.
- **Is my stamping merely a neutral *link* in any of the surviving 5?** No — and this
  is now settled two independent ways. First, the count *increasing* when stamping is
  removed rules out stamping as a positive contributor (a load-bearing link would make
  the count *drop* when removed). Second, the Exp-2 topology is decisive: op#54/op#94
  are concurrent **siblings** (under different `load module:` subtrees, common ancestor
  `POST /query`), and the cycle edges are (i) the **true singleflight wait** op#54→op#94
  (Chunk 2, not mine) and (ii) the **replay anchor** op#94→parent→…→op#54 (the
  `finish(parent)` over-reach, `replay.go:319`, traversing raw `parentId`/join — not a
  `wcprof.parent` edge). The "no-anchor SCC finds 0 cycles" result confirms the back-edge
  is purely the anchor, which operates on the parent tree regardless of parentage
  *source*. So no `wcprof.parent` edge is in the cycle.
- **Residual route?** The only theoretical residue is that my stamping *changes which
  ops are siblings/descendants* (re-homing relocates a subtree), which could in
  principle move some op into/out of a cycle. The 5→8 result captures exactly this net
  effect and shows it is **beneficial** (fewer cycles). And the fix is upstream of all
  parentage anyway: `startOf` repairs the anchor for **every** op regardless of whether
  its parent came from `wcprof.parent` or raw `parentId`, so even a hypothetical
  stamping-relocated op is covered. **Exoneration is complete.**

(One precision I'd note for the record: the findings report only the *counts* 5→8, not
that the 5 are a strict subset of the 8. The topology + the count-direction make the
conclusion robust without that, but if the owner wants zero doubt, the cheap extra is
to dump the 5 cycle SCCs and confirm none contains a `wcprof.parent`-sourced edge — I
do not consider it necessary.)

---

## Q2 — The cycle diagnosis + the `startOf` fix

### The diagnosis is correct — confirmed in the code

`replay.go:308-345` `finish(i)`: when an op is referenced before it has been started
(the out-of-order case for cross-referenced concurrent work), the anchor block does:

```
317  if !s.started[i] {
318      if par := s.p.parent[i]; par >= 0 && !s.inFlight[par] {
319          s.finish(par)                 // ← THE OVER-REACH: parent's FULL finish
320          if s.finished[i] { return s.simFinish[i] }
321      }
322      ... fallback (simStart[par] + recorded offset) ...
```

`finish(par)` replays the parent's **entire** action sequence — `actSelf`, `actSpawn`,
**and `joinUpTo` over all the parent's children** (`replay.go:353-369`). For the root
`POST /query`, that join pulls in op#54 (a sibling of op#94), whose true singleflight
wait recurses back into op#94 — which is `inFlight` (we entered this from
`finish(op#94)`), so `s.inFlight[i]` trips the cycle-break (`replay.go:341-345`). The
diagnosis "the anchor only needs the parent's progress *to the spawn*, but replays the
whole finish" is **exactly right**: anchoring a child's *start* does not require
computing the parent's *joins*, yet the current code does, and the joins are what close
the loop. ✔

This is genuinely a **shared replay bug** (not OTel emit): the recorded graph is
faithful (op#54/op#94 are true siblings, the cross-wait is a true singleflight join);
the replay *invents* the back-edge while anchoring. Native cycling identically (Exp 3 —
native builds parentage from its own wcprof context key, not OTel traceparent, yet
cycles the same 5) corroborates that the bug is downstream of parentage, in the anchor.
I cannot re-run the native dump, but the mechanism is fully code-explained by
`replay.go:319`, so I do not need it to accept Exp 3; if the owner wants the empirical
confirmation on the record, the native "5 broken cycles" line from `/tmp/native-exec.dump`
would close it.

### The `startOf` fix — correct, fundamental, right level

The fix replaces the anchor block with `if !s.started[i] { s.startOf(i) }`, where
`startOf` anchors via `startOf(parent) + recorded-offset` **without** replaying the
parent's joins. This is the **fundamental** fix, not a paper-over, because:

- **It removes the invented edge at its source.** The replay was fabricating a
  dependency (parent's join → sibling → back-wait) that is *not in the recorded graph*,
  purely as a side effect of using `finish(parent)` to get a *start*. `startOf` computes
  the start directly. The graph stays faithful; the replay stops inventing.
- **It is at the only correct level.** Native cycles identically ⇒ the bug is in the
  shared `wcanalyze` replay (PR #13393). Fixing the shared replay fixes both sources in
  one place. The design's "reuse native UNCHANGED" premise is *superseded by the
  discovery that native has the same bug* — "unchanged" would mean shipping a known
  unsoundness. Per the owner's "fix the whole system" ruling this is sanctioned, and it
  is a strict improvement to native's analyzer. (Design-doc reconcile: §1/§1.1's "reuse
  the validated replay unchanged" → "we found + fixed a latent replay-anchor unsoundness
  shared with native.")
- **In-order timing is untouched** — verified structurally: an in-order child is
  `setStart` by the parent's `actSpawn` (`replay.go:378`) *before* `finish(child)` is
  reached, so `started[child]` is already true and the anchor block (hence `startOf`)
  never runs for it. `startOf` fires **only** for out-of-order-referenced ops. This is
  why the findings' −0.3% baseline drift + "all counterfactual/oracle/ranking fixtures
  pass" is plausible, and I credit it (I cannot re-run the live workload, but the code
  structure supports the no-normal-case-regression claim).

### The lead's concern — `parent-start + RECORDED offset` under scaling — REAL but bounded (accuracy, not soundness)

This is the right thing to probe, and the answer is nuanced:

- The **counterfactually-correct** start of a child is `parent.simStart +` the
  *scaled* duration of the parent's pre-spawn path (what the current code's `actSpawn`
  computes when it reaches the spawn). `startOf` uses the **recorded** offset
  (`startNS[i] − startNS[par]`). So under a counterfactual that scales the parent's
  *pre-spawn* path, `startOf` gives a start that is off by `(scaled − recorded)` of that
  pre-spawn path — **only for out-of-order-anchored ops**.
- **Exact in baseline** (factors=1: recorded == replayed), so `RunWhatIfs`'s baseline
  makespan and all baseline-derived numbers are unaffected. The error is confined to
  *scaled* runs *and* to *out-of-order* ops (cross-referenced concurrent work) *and* is
  bounded by the parent's *pre-spawn* path scaling. For these ops the parent is
  typically the session root / a `load module:` node whose own pre-spawn self-time is
  small, so the error is small — consistent with the −0.3% drift and the passing
  oracle/ranking fixtures.
- **It cannot produce an unsound result:** `startNS[child] ≥ startNS[parent]` ⇒ offset
  ≥ 0 ⇒ `child.simStart ≥ parent.simStart` (no child-before-parent, no negative
  duration). So the trade is purely *accuracy*, never *soundness* — and the owner's
  showstopper is the *unsoundness* (the cycle), which `startOf` removes cleanly.
- **A counterfactually-exact alternative exists but is worse:** replay the parent's
  *self-time actions up to the spawn* (scaled) while skipping the joins. That keeps the
  scaled offset but is more complex *and* can still hit the cycle if the parent joins a
  cross-referencer *before* the spawn point. `startOf` (skip all parent joins) is the
  clean cut. **My recommendation:** accept `startOf`, and document the out-of-order
  start as a recorded-offset approximation that is exact in baseline and bounded under
  scaling. If a future workload shows a material ranking shift traceable to it, the
  scaled-up-to-spawn refinement is the escalation — but the evidence says it is not
  needed for v1.

### Two caveats to fix before landing

1. **Don't just zero `FallbackAnchors` — preserve the diagnostic.** The §6.1 gate's
   `FallbackAnchors` is a soft "detached/re-pointed work slipped" signal (it is what
   surfaced the 18 anomalies here, and it is the metric my Chunk 3 fix was measured by).
   `startOf` makes out-of-order anchoring the normal path, so the counter drops to 0 and
   the signal is lost. **Adopt the findings' refinement: keep an `OutOfOrderAnchors`
   (or keep the `FallbackAnchors` name) counter incremented when `startOf` anchors an op
   whose parent is `inFlight`/not-yet-spawned** — it still flags "this trace has
   concurrent cross-referencing," now as a benign structural signal rather than a
   pre-cycle alarm. Update `TestGateFallbackAnchorsReportOnlyAndThreshold` to the new
   semantics (it asserts the old behavior — a legitimate test change, not a regression
   to hide).
2. **`startOf` must be cycle-guarded.** The prototype recurses `startOf(i) →
   startOf(parent[i])` up the parent chain with **no `inFlight` guard** — the current
   `finish` has one (`replay.go:341-345`). The `parentId` tree is acyclic *by
   construction* (spans form a tree; my `wcprof.parent` points at the lazy op, an
   ancestor-side node, never a descendant — verified for Chunk 3), so in practice this
   won't recurse forever. But a future emit bug that produced a `parentId` 2-cycle would
   turn today's graceful cycle-break into a **stack overflow** — strictly worse. The
   landed `startOf` should carry an `inFlight`-style guard (or assert parent-chain
   acyclicity) so it degrades as gracefully as `finish` does. LOW, but it is a real
   robustness gap in the proposed code.

### Does my lazy-re-point experience suggest a better emit-side fix? No — and it confirms the framing

The implementer's framing ("emit re-root is the worse alternative; the replay anchor is
the real fix") is **sound**, and my Chunk 3 experience sharpens *why*:

- My Chunk 3 re-root worked **because the emit was genuinely unfaithful** there — the
  producer `parentId` was a *UI lie* (the producer had ended), so re-homing to the live
  consumer-side lazy op both fixed faithfulness **and** reduced fallback anchors (the
  re-home target is on the live replay path, so the op anchors normally).
- The Exp-4 wholesale `call_exec` re-root exploded fallback anchors to 3474 for the
  opposite reason: it detached call_execs from their **live** callers, so their parents
  were no longer on the active path → mass out-of-order anchoring. My re-root was
  *surgical to a live anchor*; the wholesale re-root *removed* the live anchor.
- Crucially, **the module cycle is not an emit-faithfulness problem at all** (native
  cycles identically). The structure is faithful: true siblings, a true singleflight
  wait. There is **no** faithful emit change that removes it — and suppressing/reclassifying
  the true wait is exactly the seam the owner forbids. So unlike lazy (an emit bug fixed
  at emit), this is a replay bug that must be fixed in the replay. The two cases have
  *different root causes and therefore different correct fix locations* — and the
  implementer identified that correctly via Exp 3. My experience does **not** suggest a
  surgical emit fix here; it confirms there isn't one.

**No better alternative found.** Ranked: `startOf` (sound, simple, bounded
approximation) ≫ scaled-up-to-spawn (exact but complex, may not fully break the cycle)
≫ emit re-root (Exp 4: 3474 fallback anchors, mutates faithful structure) ≫
suppress/reclassify the wait (forbidden seam, and the wait is true).

---

## Q3a — The lazy-exec composition test (`8c331d8272`) — sound

It directly closes my Round-1 LOW finding and locks the composition down correctly:

- Drives the **real** `beginOTelLazyOp` (producer case) + the **real** stamping
  processor (`newLazyRecordingRoot`), with `exec.run` created as a **direct** child of
  the re-pointed callback ctx (parent = producer via `resumedCallbackSpan`) and
  `containerStart`/`processRun` as `exec.run`'s children — the exact live shape.
- Asserts all the right things (`otelprof_lazy_exec_test.go`): (1) `exec.run` UI parent
  stays the producer; (2) `exec.run` (direct child) is stamped `wcprof.parent=lazy op`
  while both phases (descendants) are **un**stamped — the kind-agnostic discriminator
  proven on the real processor; (3) the loader re-homes the whole exec subtree under the
  lazy op, phases stay under `exec.run`, **`work_type=user` survives the re-home**, and
  the §6.1 gate is green. ✔
- **Reasonable cross-package boundary:** the exec spans are hand-built to mirror
  `engineutil`'s `beginOTelExecRun`/`emitOTelExecPhase` wire shape (dagql can't import
  engineutil), and the comment says so; the real engineutil emit is covered by
  `TestEmitExecSplitProducesLoaderShape`. So the *shape* is verified in engineutil and
  the *composition with stamping* here — the right split. No integration test drives
  both real emits together, but that needs the full engine; this emit-path coverage is
  the correct unit-level closure of my finding. Sound.

---

## Q3b — The `service.start` erasure — real; I missed it in Round 1

**Confirmed real, and I under-reviewed this in Round 1.** In Round 1 I checked that the
idle daemon doesn't rank (true) and the gate's self≤makespan headroom, but I did **not**
check that `service.start`'s *own* self-time captures the start window — and it does
not. In the Chunk 4 fixture the `service.start` op is `[8,60]` (52 ms) but its self is
~2 ms, because the availability span (its child, `[10,122]`) absorbs `[10,60]` and the
daemon `exec.run` under it absorbs the rest. So a genuinely **slow service start** is
attributed to the idle daemon's `exec.run`/`processRun` (off critical path, never ranks)
rather than to `service.start` — it **cannot headline**. The fresh-Codex minority was
right; I credit it.

- **It is a §3.4 design gap, shared with native** (the findings' code-confirmation that
  native's `service.start` also has the daemon `exec.run` as a child under `svcCtx` is
  consistent with `core/services.go` — the long-lived run nests under the start window).
  Under the owner's "fix the whole system" ruling, both sources must be fixed.
- **The fix direction is correct:** re-root the long-lived availability span (and its
  daemon `exec.run`) **out of** `service.start`, so `service.start`'s self = the real
  start+health-check window and a slow start headlines, while the re-rooted availability
  stays off the critical path (its daemon `exec.run` absorbs the idle, doesn't rank).
  **Must be done in both native + OTel** (an OTel-only re-root would diverge from native
  and break the §6.2 oracle) — the findings note this. ✔
- **Implementation flags (for the implementer, not me to fix):** (1) the re-rooted
  availability span needs a parent that is *not* `service.start` and itself does not
  rank — most naturally the installer/`asService` call span or a detached context
  captured *before* `service.start` is started; (2) this changes the service span tree,
  so the Chunk 4 services fixture + oracle must be updated to the new shape (and should
  add an assertion that a *slow* `service.start` now headlines — the property the
  current fixture omits); (3) it does **not** interact with my Chunk 3 stamping (it is a
  different re-root, not a `wcprof.parent` lazy re-point), and the `startOf` replay fix
  anchors the re-rooted span universally, so the two fixes compose.

---

## Severity summary

- **REAL / SHOWSTOPPER (correctly diagnosed, fix sound)** — the replay-anchor cycle
  (`replay.go:319` over-reach). `startOf` is the right fundamental fix at the right
  (shared-replay) level. **Land it, with: the `OutOfOrderAnchors` counter (don't lose
  the signal), a cycle-guard on `startOf`, and a documented out-of-order-start
  recorded-offset approximation (exact baseline / bounded under scaling).**
- **REAL / MEDIUM** — `service.start` self-erasure (§3.4 design gap, shared). Fix
  direction right (re-root availability out, both sources); add a "slow start
  headlines" assertion. I missed this in Round 1.
- **REAL / LOW (now closed)** — lazy-exec composition was untested; `8c331d8272` closes
  it soundly.
- **DESIGN RECONCILE** — the "reuse native replay UNCHANGED" premise (§1/§1.1) is
  superseded: a latent replay-anchor unsoundness shared with native is being fixed in
  the shared analyzer (a strict improvement to PR #13393).
- **NOISE / verified-fine** — my stamping (exonerated, reduces cycles); the diagnostic
  (correct); in-order replay timing (untouched by `startOf`).

## Bottom line

The reframe is right and my Round-1 catch is vindicated: the cycle is a **shared
native+OTel replay-anchor over-reach**, not an OTel-emit/nesting/stamping issue — my
Chunk 3 stamping is fully exonerated (it *reduces* cycles), established conclusively by
the 5→8 diagnostic + the sibling topology. The `startOf` start-only anchor is the
**correct, fundamental fix at the only correct level** (the shared replay, because
native cycles identically), with in-order timing untouched. **Land it** with three
refinements: preserve the out-of-order signal (don't silently zero `FallbackAnchors`),
cycle-guard `startOf`, and document the recorded-offset out-of-order-start approximation
(sound always; accuracy-bounded under scaling — not a paper-over). The emit re-root is
correctly rejected (my own lazy re-root experience confirms why a surgical-to-a-live-op
re-root works while a wholesale one explodes anchors, and why this faithful-structure
cycle has no emit fix). The lazy-exec test is sound and closes my finding. The
`service.start` erasure is a real §3.4 gap I should have caught in Round 1 — its
both-sources re-root fix is the right direction and needs a "slow start headlines"
assertion.
