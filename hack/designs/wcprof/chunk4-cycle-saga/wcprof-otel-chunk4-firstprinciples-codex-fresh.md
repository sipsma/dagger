# wcprof OTel Chunk 4 First-Principles Review

Reviewer: Codex fresh pass

Scope: item 1 and item 2 review for `e8c0dfe498`, plus item 3 re-evaluation from the governing first-principles model.

## Alignment With The Principle

I agree with the governing principle, with one precision: the analysis should be a rational function of the recorded causal graph and the model's explicit assumptions. It should not infer missing causal edges, infer root sequencing from timestamps, or use recorded-offset approximations to keep a replay going.

If the model's answer is odd but follows from the graph, the next question is whether the graph is faithful. If the graph omitted a launch edge, wait edge, parent edge, or resource edge, the fix is emit-side. If the relevant cause is outside the instrumented Dagger trace, the honest answer is "out of scope for this data," not an analysis heuristic.

## Items 1 & 2

### Item 1: Zero-Duration Wait Target

Verdict: correct and properly scoped.

The bug I raised is fixed in the right place. `joinUpTo` now refuses to anchor an unstarted child when the child's recorded start equals the current action time (`engine/wcprof/wcanalyze/replay.go:462`-`478`). That is exactly the bad shape: a zero-duration child at the same timestamp as a max-gate could be implicitly joined at the pre-gate clock before its own spawn action ran. Deferring leaves `pendCur` in place, lets the max-gate run, and then the spawn action anchors the child at the gated clock.

The second half of the fix is also necessary: action rank now orders max-gates, then spawns, then self (`engine/wcprof/wcanalyze/replay.go:175`-`197`). Without spawn-before-self, a deferred zero-duration child at the start of a self segment would be anchored after the self segment, which is just the symmetric wrong answer. The scope argument is sound: a non-zero child starting at `t` subtracts `[t,end)` from the parent's self segments, so the same-timestamp spawn/self collision only exists for zero-duration children.

The tests cover both faces:

- `TestZeroDurWaitTargetPropagation` encodes the previous showstopper and now requires `Z=100ms`, `B=100ms`, and makespan `300ms`, with no conflicts (`engine/wcprof/wcanalyze/replay_cycle_test.go:329`-`368`).
- `TestZeroDurChildAtSelfStart` verifies the spawn-before-self rank by requiring the zero-duration child to anchor at `100ms`, not after the same-time self (`engine/wcprof/wcanalyze/replay_cycle_test.go:370`-`403`).
- The simpler boundary fixture now requires `Z` to start at the gated `100ms` and eliminates the prior "benign" conflict (`engine/wcprof/wcanalyze/replay_cycle_test.go:283`-`327`).

I do not see a new degeneracy. The defer path returns without advancing `pendCur`, but the zero-duration child's spawn action is at the same timestamp and is already in the action stream; after that spawn, the next `joinUpTo` consumes it. Multiple zero-duration children at the same timestamp repeat this constant local pattern and still advance through the pending list.

### Item 2: FallbackAnchors Hard-Fail

Verdict: correct as a fail-closed safety gate, but not a final first-principles solution.

The code now hard-fails by default whenever `FallbackAnchors > MaxFallbackAnchors` (`engine/wcprof/wcotel/gate.go:143`-`145`), and `MaxFallbackAnchors` defaulting to zero means any recorded-offset anchor fails (`engine/wcprof/wcotel/gate.go:21`-`33`, `engine/wcprof/wcotel/gate.go:68`-`77`). The test verifies both default failure and the explicit opt-out (`engine/wcprof/wcotel/gate_test.go:227`-`258`).

That is the right posture while the fallback exists: it rejects an approximation rather than silently trusting it. It survives the principle as a guardrail.

But the principle also says this is not the endpoint. The current replay still has the anti-pattern in code: `spawnTo` special-cases `par < 0` by setting the target to its recorded start and incrementing `FallbackAnchors` (`engine/wcprof/wcanalyze/replay.go:527`-`538`), and `fallbackAnchor` still records an offset in a shifted parent frame (`engine/wcprof/wcanalyze/replay.go:572`-`583`). The hard gate makes this safe by failing, but the rational model should remove the fallback paths:

- a root referenced out of order should anchor at its own recorded start as an exact root fact, not as a fallback;
- an in-flight ancestor or "parent prefix never reached target" condition should become a structural replay/data error unless the data records a causal edge that makes it schedulable.

`MaxFallbackAnchors` is acceptable only as an explicit best-effort/debug escape hatch. It should not be used in the Cloud/OTel confidence gate for owner-facing rankings.

## Item 3: Rational Root Model

The first-principles replay model should be:

- Each root has no incoming causal edge, so its simulated start is its recorded root start. Do not chain roots by observed order or idle gaps.
- Parent-child nesting is honored as recorded: a child spawn is reached by replaying the parent's prefix to that child's recorded spawn action; synchronous containment implies the parent joins children according to recorded child intervals.
- Explicit waits are honored exactly as recorded. A wait edge gates the waiter through the simulated finish of its target; if the edge is wrong or missing, that is a data problem.
- No recorded-offset fallback. If a non-root cannot be anchored through recorded parent/prefix structure, the replay should report an unschedulable/cyclic data shape rather than guess.

Concretely, this means `Run` should stop using `chainOrigEnd` / `chainSimEnd` to shift later roots (`engine/wcprof/wcanalyze/replay.go:358`-`391`). Roots should be initialized at `startNS[root]`. It also means `spawnTo(par < 0)` should be exact root scheduling, not a fallback counter (`engine/wcprof/wcanalyze/replay.go:527`-`538`).

### Test Case A: Concurrent Cross-Root Dedup

Data recorded:

- `R_A` root starts at `0`.
- `R_B` root starts at `0`.
- `R_B` does `100ms` setup, then spawns `T=load foo` at `100`; `T` runs `100->300`.
- `R_A` has `W` waiting on `T` and unblocks at `300`.
- There is no edge saying either root launches the other.

Logical answer from the data:

- Both roots stay anchored at recorded start `0`.
- Scaling `R_B` setup to zero moves `T`'s spawn from `100` to `0`.
- `T` still has `200ms` of self, so it finishes at `200`.
- `W` honors its wait edge and unblocks at `200`.
- Makespan is `200`, saving `100`.

The rational model gives this answer. Current chaining/fallback machinery may also get the makespan through first-write-wins in some orderings, but it reports fallback/conflict because two analysis heuristics are fighting. That diagnostic is self-inflicted by the non-rational root scheduling.

### Test Case B: Sequential CLI Roots

Data recorded:

- `R_A` root runs `0->100`.
- `R_B` root runs `150->250`.
- There is no recorded causal edge from `R_A` to `R_B`; only timestamps imply ordering.

Logical answer from the data:

- `R_B` starts at `150` no matter how `R_A` scales.
- Scaling `R_A` to zero does not pull `R_B` earlier.
- The run's span remains `0->250`; saving from `R_A` on overall makespan is zero, unless `R_A` was itself the last finisher.

That differs from the current `TestRootChaining`, which expects scaling the first root to pull the second root earlier while preserving a `50ms` idle gap (`engine/wcprof/wcanalyze/replay_test.go:191`-`225`). That test encodes a heuristic, not recorded causality.

If the desired physical question is "this shell script ran command B only after command A returned," the engine-only root data is insufficient. The emit fix is a recorded client/workflow edge: a parent span for the script/CLI sequence, or an explicit launch/dependency edge from command A completion plus think time to command B start. If that serialization lives outside Dagger instrumentation, it is out of scope for this trace. The analysis should not infer it from timestamps.

### Test Case C: Sub-Session Launched By Another Root

Data recorded, faithful case:

- `R_A` reaches a launch point for `R_B`.
- The trace records that launch causality, for example through a nested-client link or equivalent parent/launch edge.

Logical answer:

- `R_B` is not an independent pure root for replay purposes. It anchors through the recorded launch edge/prefix.
- Scaling pre-launch work in `R_A` shifts `R_B`'s start, and downstream waits across the session boundary follow normally.

The current graph builder already has one version of this data fix: nested-client links map a client ID to a hosting exec and reparent nested roots under that exec (`engine/wcprof/wcanalyze/graph.go:270`-`293`). That is aligned with the principle because the data supplies the causal edge.

Data recorded, unfaithful/missing-edge case:

- `R_B` appears as a root with no incoming launch/parent edge.
- There may still be a cross-root wait between `R_A` work and `R_B` work.

Logical answer from the data:

- `R_B` anchors independently at its recorded start.
- Any odd missed saving from pre-launch work in `R_A` is a data problem: the launch edge is missing.

Emit fix: record the sub-session launch as a causal edge, not a heuristic in replay. Depending on the actual source, that can be a nested-client link, a parent span/stamped parent, or a dedicated `session.start`/launch wait/link with the launch timestamp and target root identity.

## Strongest Counterexample Attempt

The strongest apparent challenge is sequential work outside the engine: a CI step or shell script starts Dagger command B only after Dagger command A exits. If scaling A to zero would really start B earlier, the rational engine-root model says no saving because it sees two roots with no edge.

That does not break the principle. It reduces to missing or out-of-scope data. If that sequence is part of the analyzed Cloud trace, emit a client/workflow parent or explicit dependency edge. If it is not instrumented, the engine trace cannot answer that wall-clock question without inference, and inference is exactly what the principle forbids.

I also do not accept the stronger wording that "a cross-root wait is always evidence of concurrency/independence." A cross-root wait is evidence of a dependency edge from the waiter to the target. It often implies overlapping concurrent roots when the wait actually blocks on in-flight work, but it does not prove there was no launch dependency between the roots. A sub-session can have both: a launch edge from `R_A` to `R_B`, and later a wait edge across their work. Root independence comes from absence of incoming causal edges, not from the mere presence of a cross-root wait.

## Code Direction

The rational model points to these analysis changes:

- Remove root chaining in `Run`: schedule every root at `startNS[root]`; makespan is max simulated root finish minus min root start.
- Treat `par < 0` in `spawnTo` as exact root anchoring, not fallback.
- Remove recorded-offset `fallbackAnchor` as an analysis result. If parent prefix cannot reach a supposed child, or an ancestor is in-flight in a way that prevents causal scheduling, report a structural data/replay error.
- Update or replace `TestRootChaining`; the current expected `150ms` scaled makespan is a heuristic expectation, not a first-principles one.

The corresponding emit/data requirements:

- Sequential CLI/workflow serialization must be recorded by a client/workflow parent or explicit dependency edge if it should affect the answer.
- Sub-session launch must be recorded as a causal launch/parent edge; nested-client reparenting is the right shape when applicable.
- A cross-root wait must remain a wait edge only when the waiter truly depends on the target result. If an emitted link is just a lookup/diagnostic relation to already completed work and should not gate counterfactuals, do not emit it as a wait.

## Final Summary

Items 1 & 2 verdict: item 1 is correct and fixes the zero-duration wait-target counterexample; item 2 is a valid fail-closed guard, but it is a temporary safety measure around an anti-pattern that the rational model should remove.

Strongest attempted counterexample: sequential external CLI/CI orchestration. It does not hold as an analysis counterexample; it reduces to missing/out-of-scope data. Emit a workflow/dependency edge if that sequencing should be analyzed.

Three test-case answers: concurrent cross-root dedup saves `100ms` and makespan becomes `200`; sequential CLI roots do not shift without a recorded edge; sub-session roots shift only if the launch edge is recorded, otherwise the data says independent and the emit must be fixed.

Principle alignment: aligned. The one pushback is wording: a cross-root wait is not always proof of root independence. The absence of an incoming causal edge is what makes a root independent; a cross-root wait is just an edge the model must honor.
