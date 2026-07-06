# wcprof x OTel Chunk 4 final replay hardening review

Reviewer: Codex  
Scope: final review of commit `e69d1f0049` via `hack/designs/wcprof-otel-chunk4-final-handoff.md` and `hack/designs/wcprof-otel-chunk4-hardening-e69d1f0049.patch`. No code changes, no commits.

## Verdict

Landable in shared/validated `wcanalyze`, with no code-level blocker found.

The end-ordered gating reformulation satisfies the round-3 correctness concerns better than the proposed skip predicate did:

- it uses the wait's own recorded `EndNS`, not `target.EndNS`, for gating wait sequencing (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:114-140`);
- it applies the same end-ordered treatment to fixed waits (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:128-133`);
- it has one shared `advance(op, stopAt)` interpreter for full finish and prefix anchoring (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:254-309`);
- it turns first-write-wins into a checked invariant via `SimStartConflicts` (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:181-195`).

I would still correct two claims around the patch before relying on them in review lore:

1. "End-ordering never changes a finish" is too strong. It can change a finish through a child spawned during a wait. That is not necessarily a bug; it is the intended removal of false serialization. But finish-invariance is not the proof.
2. The jaccard pushback is wrong for the actual `wcotel` oracle if it refers to `OracleComparison`: the oracle ranks by `RunWhatIfs` `SavedNS`, not by loader self-time.

Those are framing/validation issues, not blockers to the replay mechanism.

## REAL Issues / Adjustments

### MEDIUM: "finish-invariance" is false as stated

The handoff says parent finishes are unchanged because self segments never overlap wait intervals (`wcprof-otel-chunk4-final-handoff.md:40-44`, lead verification at `wcprof-otel-chunk4-final-handoff.md:86-90`). The self-segment premise is true: `SelfSegments` subtracts both child intervals and wait intervals (`engine/wcprof/wcanalyze/graph.go:379-397`). But it only proves that moving a gating wait from `StartNS` to `EndNS` will not move *self work* across the wait. It does not prove that finishes are unchanged.

Counterexample:

```text
P [0,200]
  wait W [0,100]
  child C [50,200], C duration 150
```

Old start-ordered replay gates C's spawn behind W, so C starts after the wait and can push P later. New end-ordered replay lets C spawn before W completes, then W gates P's finish. P's finish can change because C's simulated start changed and P implicitly joins C at the end. That is exactly the class the new model is meant to fix, and `TestConcurrentWaitOrderIndependent` intentionally exercises the dual semantics: a wait overlapping U's spawn does not gate U, but still gates P's finish (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:538-604`).

So the correct invariant is narrower:

```text
For any child spawn, the prefix path and full-finish path process the same action prefix up to that spawn.
A wait whose recorded end is after the spawn is not in that prefix on either path.
```

That gives order-independent starts. It does not imply old-vs-new finish invariance. I do not consider this a blocker because the old finish was the over-serialized one for spawns-during-waits; the handoff/commit message should not use finish-invariance as the safety proof.

### MEDIUM: the surviving recorded-offset fallback is counted, but not counterfactually immaterial

The only remaining `startOf`-style approximation I see is in `spawnTo` fallback paths:

- root referenced before its own root scheduling: `setStart(target, startNS[target])` plus `FallbackAnchors++` (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:311-328`);
- parent/ancestor in flight or missing the recorded child spawn: `fallbackAnchor` (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:330-373`).

That matches the handoff: the approximation is no longer the normal anchor path, and it is counted. But it is not inherently immaterial. In a shifted cross-root what-if, a root reached early by another root's wait can be anchored at recorded time, then later `Run` may try to schedule that root in a shifted frame (`engine/wcprof/wcanalyze/replay.go:312-345` in `e69d1f0049`). `SimStartConflicts` can surface the disagreement, but it does not repair the already-finished target.

This is acceptable if fallback anchors remain rare/reportable and zero on the validated traces. It should not be described as harmless. A nonzero `FallbackAnchors` remains a "the replay used an approximation" signal.

### MEDIUM: the jaccard-0.80 pushback is not correct for the actual oracle ranking

The handoff says the jaccard bar is wrong partly because "jaccard ranks top-N by self-time" and therefore the replay change cannot move it (`wcprof-otel-chunk4-final-handoff.md:52-58`). That is not true for the current `wcotel` oracle.

`TopBottlenecks` runs `wcanalyze.RunWhatIfs`, keeps positive savings, and sorts by `SavedNS` (`engine/wcprof/wcotel/oracle.go` in `e69d1f0049`: `TopBottlenecks`, lines 46-69). `CompareTopN` then computes jaccard on those `SavedNS`-ranked lists (`oracle.go`, lines 128-165). `RunWhatIfs` candidates are selected by total self-time, but the final top-N ranking is replay-dependent (`engine/wcprof/wcanalyze/replay.go` in `e69d1f0049`: lines 607-650).

So the clean test is not "jaccard must be identical pre/post" unless the measured jaccard is from a different self-time-only table. For the actual oracle, replay changes can move jaccard. The observed low jaccard may still be a real second-source bucketing mismatch (`:uploading`/`:stdout` vs semantic native classes), and the handoff's bucket explanation is plausible. But the self-time-invariant argument does not prove it.

This is not a replay-landing blocker. It is an oracle interpretation issue: use pre/post same-source comparisons and bucket-localized evidence, not the claim that jaccard cannot move.

### LOW: `SimStartConflicts` is report-only in the OTel gate

The patch surfaces `SimStartConflicts` in the gate report (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:795-823`), but `CheckStructural` does not make nonzero conflicts a violation (`engine/wcprof/wcotel/gate.go` in `e69d1f0049`: lines 97-140).

If the invariant is "must stay 0 on a faithful baseline" (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:169-177`), then a nonzero count should probably fail the structural gate, or at least be an explicit bounded option like fallback anchors. This is not a blocker for the reported traces because the measured value is 0, but it is worth tightening.

### LOW: one stale replay comment remains

`replayProgram.actions` still says actions are sorted by `(at, self<spawn<wait)` (`engine/wcprof/wcanalyze/replay.go` in `e69d1f0049`: lines 83-85), but the new rank is `wait/fixed < self < spawn < noop` at equal timestamps (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:90-108`). The nearby `actionRank` comment is correct, so this is documentation drift only.

## NOISE / Non-Issues

### Round-3 concern: own wait end vs target-end proxy

Resolved. The compiled action for a join wait is sequenced at `w.EndNS` (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:114-127`). The boundary test explicitly covers the case I was worried about: target end equals the spawn but the wait's own end is later, and the spawn is not gated (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:606-664`).

### Round-3 concern: fixed waits

Resolved. Fixed waits are also sequenced at `w.EndNS`, with `dur = w.Duration()` (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:128-133`), and the test covers a fixed wait overlapping a child spawn (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:666-696`).

### Round-3 concern: first-write-wins path dependence

Resolved for the normal prefix/full-finish path. `finish` now calls `spawnTo`, and both prefix anchoring and full finish run through `advance` (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:198-309`). Because wait actions are end-ordered, a wait open at a spawn is after that spawn in both paths. `setStart` still keeps first-write-wins, but a different later start increments `SimStartConflicts` (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:181-195`).

`SimStartConflicts=0` is a sound regression signal for "both paths tried to start the same op differently." It is not a complete proof of every replay property: it will not detect finish changes, and fallback-anchor cases can still be approximate. But it covers the specific first-write-wins hazard I raised.

### Noop / cancellation waits

No new issue found. A wait that ends before its target's recorded end by more than `joinEpsilonNS` is still `actWaitNoop`, contributes no time, and stays at the wait start (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:134-139`). That preserves the old abandoned-wait semantics. A noop can still serve as an action point for implicit joins, as before.

The `joinEpsilonNS` boundary remains the existing classification compromise: waits within 1ms of target end classify as joins (`engine/wcprof/wcanalyze/replay.go` in `e69d1f0049`: lines 46 and 180-181). End-ordering does not reuse epsilon for spawn gating; equal-time gating is handled by action rank, where wait/fixed actions sort before spawns at the same timestamp (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:90-108`). That addresses the specific epsilon leak I was worried about.

### OTel -2.4% baseline compression

I cannot independently verify the numeric `-2.4%` without the raw trace, but the direction is consistent with the new model. End-ordering can compress a trace when the old replay falsely serialized child spawns behind waits that were still open. That is the intended correction, not evidence of a new replay bug. The stronger support is the reported zero cycles, zero fallback anchors, zero start conflicts, and the config-parse 100ms fixture preserving the counterfactual effect (`wcprof-otel-chunk4-final-handoff.md:67-82`).

### Performance

No blocker found. The patch keeps the compiled flat action program, and `advance` still scans one op action slice at a time (`wcprof-otel-chunk4-hardening-e69d1f0049.patch:270-309`). Prefix anchoring can still re-walk prefixes, but the handoff reports 0.28s on 86k native ops and includes a 64-way fan-out test (`wcprof-otel-chunk4-final-handoff.md:67-73`, `wcprof-otel-chunk4-hardening-e69d1f0049.patch:724-763`). That is enough for landing; larger stress can remain a follow-up if traces grow.

## Final Answer To The Charges

1. **Does end-ordering satisfy my round-3 concerns?** Yes for the three core concerns: own wait end, fixed waits, and shared interpreter/path-independence. `SimStartConflicts=0` is good evidence for start order-independence on the exercised traces, but not a proof of finish-invariance or fallback correctness.
2. **Is finish-invariance airtight?** No. Full finishes can change through children spawned during waits. That is not a blocker; it is the desired correction when the old model was false-serializing. The safety proof should be order-independent starts plus recorded wait-interval semantics, not "finishes never change."
3. **Is recorded-offset fallback only/countable/immaterial?** It is now only in fallback paths and counted. It is not immaterial in principle; nonzero fallback anchors still mean approximation.
4. **Is the jaccard argument sound?** Not for the actual `wcotel` oracle, which ranks by `SavedNS`, not self-time. Low cross-source jaccard may still be bucket drift, but that needs bucket-localized evidence, not replay-invariance.
5. **Landable in validated native?** Yes. I found no blocker in the hardening patch. I would adjust the handoff/commit-message framing and consider making `SimStartConflicts > 0` a hard or bounded structural-gate violation.
