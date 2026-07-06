# wcprof × OTel — Chunk 4 cycle fix review (by the design author)

**Scope:** evaluate the implementer's reframe + proposed fix
(`hack/designs/wcprof-otel-chunk4-cycle-findings.md`) — critically, not on trust —
plus the service.start erasure finding and the landed lazy-exec test. The owner's
standing principle governs: unsoundness is a showstopper, no seams, fix
fundamentally even if native shares it. I cannot see the 9.3 MB trace; I reasoned
from the findings + the shared replay code (`engine/wcprof/wcanalyze/replay.go`).

## Plain answers up front

1. **Is the cycle correctly diagnosed?** **Yes, with high confidence from the
   code.** It is a **shared native+OTel bug in the replay's *anchor* mechanism**,
   not an OTel-emit/false-nesting issue. The earlier council theory (op#54 nested
   under op#94; implicit join invents the back-edge) is **wrong**, and the reframe
   is the correct one. My own Chunk-4 diagnostic ("does native cycle?") came back
   **yes** — which is exactly the signature of a shared replay bug, not an emit gap.
2. **Is the start-only-anchor fix correct, fundamental, side-effect-free, and
   design-aligned?** **Correct and fundamental — and, crucially, it is what the
   replay's own documentation already says the anchor *should* do.** It is NOT a
   paper-over. One real side effect (it zeroes the `FallbackAnchors` signal) needs a
   small refinement, and one test must be updated. No better alternative exists
   among the options.
3. **Service.start erasure fix?** The erasure is **real and shared with native** (a
   genuine §3.4 gap); the re-root direction is **correct and must be applied to both
   sources**, but the concrete mechanism needs to be designed (it is non-trivial).
4. **Design-premise impact?** This fix changes the "reuse the validated native
   replay UNCHANGED" premise — **correctly.** The premise was a simplifying
   assumption that a genuine pre-existing replay bug invalidates; per the owner's
   "fix it fundamentally even if native shares it," fixing the shared replay is the
   right layer. It also **corrects** my own §1.1/§2.5/§6.3 framing ("cycle ⇒
   unfaithful emit"), which is incomplete.

---

## 1. The diagnosis — independently verified as code-plausible and correct

**The anchor over-reach is real (code-verified).** In `finish(i)` the anchor block
is `if par := s.p.parent[i]; par >= 0 && !s.inFlight[par] { s.finish(par); … }`
(`replay.go:318-319`). To obtain one out-of-order child's *start*, it replays the
parent's **entire `finish`** — including the parent's `joinUpTo` implicit join over
**all** the parent's children (`replay.go:353-369`, `:389`). The code comment even
admits "Replaying the parent may recursively replay op itself (via an implicit
join)" (`:314-315`). So anchoring a cross-subtree wait *target* drags in the
parent's whole subtree via the implicit join — which is how a back-edge to a
concurrent cross-referencer (that waits back) gets created. That is the cycle's
source, and it is structural in the shared replay.

**It is the decisive design-intent violation — not the data.** The replay's own
header states the intended behavior: *"ops reached through cross-tree wait targets
are anchored at original times when their own root has not been replayed yet …
mixing frames would corrupt the schedule"* (`replay.go:31-34`). The current
`finish(par)` anchor does the opposite — it replays the parent in the
**counterfactual** frame to anchor a cross-tree target, i.e. it **mixes frames**,
exactly what the comment warns "would corrupt the schedule." So the anchor is the
bug measured against the replay's *own* stated contract; the emitted data (faithful
in both sources) is not.

**The corrected topology is consistent with the code.** Exp 2 (op#54 and op#94 are
concurrent *siblings* cross-referencing via true singleflight waits, NOT nested;
"no-anchor SCC = 0 cycles") fits: the implicit join alone stays *within* a subtree,
so it cannot bridge L_go↔L_gosdk; only the anchor's `finish(par) → finish(root) →
root's implicit join over both module subtrees` bridges them and closes the loop
with the real `op#54 → op#94` wait. I could not run the SCC analysis, but the
mechanism is the only one in the code that produces a cross-subtree cycle from a
wait-DAG.

**Native shares it (predicted, and consistent with the code).** Exp 3 ("native: 5
broken cycles") matches my Chunk-4 prediction. Native's `call_exec` (`execOp`) is
begun on the executor's detached `callCtx` and its resolver sub-calls nest via the
**wcprof context key**, not the live stack (`dagql/cache.go:3669-3717`) — the *same*
context-propagation nesting OTel uses, feeding the *same* replay anchor. So native
hits the identical anchor over-reach. This is the cleanest proof it is a replay bug,
not OTel emit: two independently-built graphs (different parentage mechanisms) cycle
identically because they share the replay.

**The earlier council theory is correctly refuted.** "op#54 is a detached
descendant of op#94, implicit join invents the back-edge" required nesting; Exp 2
shows siblings. I had hedged this in my Chunk-4 review (I flagged the exact-mechanism
as unconfirmed and made the native-comparison the decisive diagnostic); the reframe
resolves it in the direction my diagnostic pointed. Good empirical correction.

**Alternatives ruled out (independently):** a *missed* dependency would
under-serialize (a missing edge), never cycle; the closing `actWaitJoin`
(`waitEnd=195=targetEnd`, `replay.go:165`) is a faithful "waited to completion"
join, not a mis-classification; and a loader/emit bug is excluded by native cycling
on its own faithful data. The only remaining cause is the replay anchor — which the
code confirms.

**Evidence I'd still want (cheap, for the record, given the stakes):** the
`no-anchor SCC = 0` output (confirms the waits are a DAG ⇒ the cycle is purely
anchor-induced) and the native dump's "5 cycles" line (confirms Exp 3). Both are a
few lines to extract; I'm confident enough from the code to endorse the direction
without the 9.3 MB trace, but these two close the loop empirically.

## 2. The fix — correct, fundamental, design-aligned (one caveat)

`startOf(i)` (findings §Exp 5) computes a start by walking **up the parent chain**
adding *recorded* offsets to the root's start, with **no `finish` call** — so it
performs no implicit join, touches no waits, and creates no cross-tree edges. The
anchor block becomes `if !s.started[i] { s.startOf(i) }`. Assessment:

- **It removes the over-reach at the root.** The cycle existed only because
  anchoring called `finish(par)`. `startOf` gets the start *directly*; the implicit
  join is never invoked for anchoring, so the false cross-tree back-edge cannot
  form. `cycles → 0` for both sources follows necessarily.
- **It is the documented intent, not a paper-over.** `startOf` anchors cross-tree
  targets at **original (recorded) frame times** — precisely `replay.go:31-34`. The
  current code's counterfactual anchor for those targets was the *deviation*. So
  this is the replay finally doing what it says.
- **The reviewer's "wrong counterfactual start" worry is resolved by that
  alignment.** Yes, `startOf` uses the *recorded* parent→child offset, so an
  out-of-order target's start does **not** reflect a counterfactual that scales the
  parent's pre-spawn path. But that is *intended* — cross-tree targets are supposed
  to be original-frame-anchored (no frame mixing). And it does **not** break the
  counterfactual that matters: scaling the target's **own** class still propagates,
  because `finish(target) = startOf-start + scaled-self`, and the cross-tree waiter
  takes `max(clock, finish(target))`. So bottleneck attribution on the target's own
  class is preserved; only the (intentionally-excluded) cross-frame parent-pre-spawn
  coupling is dropped. The −0.3% makespan drift + passing counterfactual/oracle
  fixtures corroborate no normal-case regression.
- **The cycle-break is preserved for *genuine* cycles.** `startOf` only recurses up
  the (acyclic) parent tree, so it cannot loop; the `inFlight` cycle-break
  (`replay.go:341-345`) remains for true wait-DAG cycles (a real unfaithful-emit
  bug). So after the fix `CycleWarnings` becomes a **clean** signal — it fires only
  on genuine wait cycles, never on the anchor artifact. **This is the right way to
  satisfy the owner's "no seams": it makes the §6.1 `CycleWarnings == 0` invariant
  *correct* rather than relaxing it.** (It supersedes my Chunk-4 suggestion to relax
  the gate — better.)
- **Caveat 1 — the `FallbackAnchors` signal is zeroed.** `startOf` always succeeds,
  so the fallback path (and its `FallbackAnchors` counter) never runs. That counter
  was a §6.1 soft "detached/re-pointed work" diagnostic. Its *failure* meaning is
  obsolete (out-of-order anchoring is now handled correctly, not a "slip"), but the
  *count* is still a useful "this trace has many cross-tree references" diagnostic.
  **Recommend keeping a renamed counter** (e.g. `OutOfOrderAnchors`) incremented
  when `startOf` anchors an op the parent hadn't yet spawned, so no diagnostic is
  lost. Update §6.1 accordingly.
- **Caveat 2 — `TestGateFallbackAnchorsReportOnlyAndThreshold` must be updated**, as
  flagged — it asserts the old cross-root-fallback behavior the fix removes. That is
  a test codifying obsolete behavior, not a regression.

**Better fundamental fix? No, among the real options.** (a) Emit re-root (Exp 4):
0 cycles but 18→3474 fallback anchors *and* it changes only OTel emit → diverges
from native → breaks the oracle. Worse on every axis. (b) Loader suppress/reclassify
the closing wait: design-forbidden (§6.3, no replay machinery to paper over data)
and wrong (the wait is a real join). (c) A full topological-order replay rewrite:
far larger blast radius than the surgical `startOf`. The start-only anchor is the
minimal change that fixes the root cause in the shared layer — I did not find a
cleaner one.

**One correctness check to add when landing:** a **synthetic regression test** that
reproduces the cycle *structure* (two concurrent cross-subtree singleflight peers
under sibling parents, one waiting on the other) and asserts `CycleWarnings == 0`
after the fix and `> 0` before. This is buildable without the 9.3 MB trace (the
chunk2/3 tests already construct synthetic graphs), and it codifies the fix so the
anchor can never regress. Essential before landing.

## 3. Design-premise reframe — endorse, and correct the cycle taxonomy

The design rests on "reuse the **validated** native replay **unchanged**; fix
faithfulness at emit." This fix changes `wcanalyze/replay.go`. I endorse it as the
correct layer, and I'd fold two corrections back into the design (I am the author):

- **The premise is amended, not broken.** "Validated" meant "trusted for the cases
  v1 exercised." Module-loading concurrency (concurrent cross-referenced
  `call_exec`s) was outside that envelope, and it surfaced a *genuine pre-existing*
  replay bug (the anchor frame-mixing, contradicting the replay's own §31-34
  intent). Per the owner's ruling, fixing it in the shared replay is correct — it
  improves the analyzer for **both** sources. Update the design to: "reuse the
  native replay, fixing genuine shared bugs it surfaces (Chunk 4: the anchor
  over-reach)."
- **§1.1/§2.5/§6.3 "cycle ⇒ unfaithful emit" is incomplete and should be
  corrected.** A cycle can come from **either** unfaithful emit **or** a replay
  anchor bug on faithful data. The discriminator is exactly the Chunk-4 diagnostic:
  **does native cycle on the same faithful data?** Native cycles ⇒ shared replay
  bug (fix the replay); native clean ⇒ OTel emit bug (fix the emit). Bake this
  two-way taxonomy + the diagnostic into §6.1/§6.3 so the next cycle is triaged
  correctly instead of being assumed an emit fault.

This is a real improvement to the design's mental model, not a retreat.

## 4. service.start self-time erasure — real, shared, §3.4 gap; re-root direction correct

Verified by reasoning + code: `service.start` brackets `svc.Start` `[10,60]`, and the
daemon's `exec.run` is begun under `svcCtx` (which carries the `service.start`
op/span), so the daemon `exec.run` is a **child** of `service.start` and covers
`[~10, teardown] ⊇ [10,60]`. `SelfSegments` subtracts that child, so
`service.start` self-time collapses to ~the 2 ms gap before the daemon starts — a
slow start does **not** headline. This is exactly the §3.4 trap, *inverted*: the
design said "leave the availability span non-self-time-bearing," but didn't prevent
the availability `exec.run` from being `service.start`'s child and **absorbing**
`service.start`'s own self. It is **shared with native** (native's `service.start`
op has the same daemon `exec.run` child under `svcCtx`), so it satisfies the owner's
"fix it in the whole system" — and an OTel-only fix would diverge from native and
break the oracle (the finding is right to require both).

**The re-root direction is correct** (the daemon `exec.run` belongs under the
long-lived availability span, not under the bounded `service.start` window). But the
concrete mechanism is non-trivial and unspecified: the daemon executor's ctx
currently descends from `svcCtx`, so re-rooting means launching the daemon under the
availability span's ctx instead of `service.start`'s, in **both** native (`svcCtx`
op nesting) and OTel — without disturbing the existing service-span semantics or the
installer waits. I endorse fixing it (it is a genuine faithfulness bug by the
owner's bar) but recommend it be **designed + reviewed as its own change**, not
bundled into the cycle fix — it touches a different choke point and both sources'
emit. Fold the §3.4 gap (availability must not absorb `service.start` self) back
into the design now.

## 5. Landed lazy-exec composition test (`8c331d8272`) — substantive and correct

Test-only (140 lines). It drives the **real** `beginOTelLazyOp` + stamping
processor with an `exec.run` (direct re-pointed child) + `containerStart`/`processRun`
phases (descendants), against the in-memory SDK, then through the loader + gate. It
asserts the right five things: exec.run UI parent stays the producer; exec.run is
stamped `wcprof.parent`=lazy op; **phases are NOT stamped** (the §3.0.2
discriminator — descendants follow via parentId); the loader re-homes the whole
exec subtree under the lazy op; `work_type=user` survives; gate green. This properly
codifies the "lazy-triggered exec composes for free" claim on real exported spans.
Good.

## Issues summary

- **REAL / SHOWSTOPPER (resolved by this fix) — the replay anchor over-reach.**
  Diagnosis correct (shared replay bug, native cycles identically). `startOf` fix is
  correct, fundamental, and aligned with the replay's documented intent; makes the
  §6.1 cycle invariant *correct* (no seam). Land it with: a synthetic cycle
  regression test; a renamed out-of-order-anchor counter to preserve the diagnostic;
  the `TestGateFallbackAnchorsReportOnlyAndThreshold` update.
- **REAL / SHOWSTOPPER — service.start self-time erasure** (§3.4 gap, shared with
  native). Re-root direction correct; design the concrete both-sources re-root as
  its own change.
- **DESIGN reconciles** — amend the "replay unchanged" premise; correct the
  §1.1/§2.5/§6.3 "cycle ⇒ unfaithful emit" to the two-way taxonomy + native-cycle
  diagnostic; record the §3.4 availability-absorption gap.
- **NOISE / good** — the landed lazy-exec test is substantive and correct.

## Bottom line

The reframe is right and the diagnosis is sound: this is a **shared replay anchor
bug** (the over-reaching `finish(par)` anchor that mixes frames, contradicting the
replay's own `:31-34` contract), surfaced — not caused — by the OTel work, and
native cycles identically. The **start-only anchor is the correct, fundamental,
minimal fix**: it implements the documented original-frame anchoring, eliminates the
false cycles in *both* sources, preserves makespan and the counterfactual on each
op's own class, and turns `CycleWarnings` into a clean signal (satisfying "no seams"
by making the gate correct, not lenient). Land it with the synthetic regression
test, the renamed anchor counter, and the test update; fix the (separate, also-real,
also-shared) service.start erasure as its own both-sources re-root; and fold the
premise amendment + cycle-taxonomy correction + §3.4 gap back into the design. I do
not need the raw trace to endorse this, but recommend attaching the
no-anchor-SCC-0 and native-5-cycles outputs as the load-bearing empirical record.
```
