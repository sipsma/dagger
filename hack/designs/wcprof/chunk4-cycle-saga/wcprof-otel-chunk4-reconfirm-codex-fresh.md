# wcprof OTel Chunk 4 Cycle Fix Reconfirm Review

Reviewer: Codex fresh pass

Scope: reconfirm `692aaabd3f` / `wcprof-otel-chunk4-postreview.patch`, especially the max-based fixed-wait model and the two remaining gate posture questions.

## Verdict

The max-based fixed-wait model is the right model and is implemented faithfully: fixed waits now behave like concurrent non-scalable segments, not additive serial jumps. It fixes both counterexamples I raised previously.

I am not comfortable with the current `SimStartConflicts` posture. The documented zero-duration conflict is locally benign for the parent finish in the added fixture, but it is not generally benign once that zero-duration child, or an op that waited on it, participates in another wait chain. `FallbackAnchors` is not a sufficient harm precondition for that class. This should be made clean and enforced, or the zero-duration same-timestamp ordering should be fixed so the conflict does not occur.

I agree with Erik's lean that `FallbackAnchors > 0` should hard-fail the OTel structural gate. It can arise from a faithful trace shape, especially cross-root references, but it means this replay used the surviving recorded-offset approximation. That is an analyzer confidence failure even when the emit is faithful.

## Max-Based Fixed Waits

No issue.

The code matches the proposed model:

- `compileProgram` gives every fixed wait a private slot and emits `actWaitFixedStart` at `StartNS` plus `actWaitFixedEnd` at `EndNS` with the original duration in `dur` (`engine/wcprof/wcanalyze/replay.go:212`-`224`).
- `actWaitFixedStart` records the current simulated clock in that slot (`engine/wcprof/wcanalyze/replay.go:490`-`492`).
- `actWaitFixedEnd` computes `fixedWaitClock[slot] + dur` and raises `clock` with a `max`, not `+=` (`engine/wcprof/wcanalyze/replay.go:493`-`498`).

That is backward-compatible with the old additive behavior when no concurrent work advances the clock between the wait start and wait end: the end action sees `clock == waitStartClock`, so `max(clock, waitStartClock+dur)` is exactly `clock += dur`.

It also handles overlapping or nested fixed waits in the same op more naturally than a single scratch value would: `numFixedWaits` assigns a distinct slot per fixed wait (`engine/wcprof/wcanalyze/replay.go:106`-`108`, `engine/wcprof/wcanalyze/replay.go:220`-`224`), so each end marker gates against its own recorded start clock.

The equal-timestamp rank is also consistent with the model. Fixed wait ends rank with max-gates before self/spawn at the same timestamp; fixed wait starts rank after self/spawn because they only record a clock and must not gate same-instant child fan-out (`engine/wcprof/wcanalyze/replay.go:175`-`192`). That preserves the inclusive "wait ended at this instant gates later work" rule without serializing a spawn that merely coincides with the wait start.

The added regression test covers the two important cases:

- child finishes inside lock: expected finish is `95ms`, not additive `125ms` (`engine/wcprof/wcanalyze/replay_cycle_test.go:331`-`356`);
- child spawns during lock: child anchors at `50ms` and parent finishes at `300ms`, not `400ms` (`engine/wcprof/wcanalyze/replay_cycle_test.go:359`-`389`).

## FallbackAnchors

Recommendation: make `FallbackAnchors > 0` a hard gate failure for the OTel structural gate.

The current gate only enforces fallback anchors when `MaxFallbackAnchors > 0`; `0` means report-only (`engine/wcprof/wcotel/gate.go:63`-`72`, `engine/wcprof/wcotel/gate.go:138`-`140`, `engine/wcprof/wcotel/gate.go:165`-`169`). That leaves the surviving `startOf`-style approximation as an observable metric rather than a correctness gate.

There can be faithful traces with `FallbackAnchors > 0`. The cross-root fixture is exactly that kind of shape: one root waits on another overlapping root, and the replay must recorded-offset-anchor the out-of-order root (`engine/wcprof/wcanalyze/replay_cycle_test.go:393`-`420`). So hard-failing is not necessarily saying "the trace is unfaithful." It is saying "this analyzer did not compute the counterfactual from causal prefixes and cannot guarantee the ranking."

That distinction is fine. The owner has set the bar at right answers, not best-effort approximations. If a faithful cross-root trace should pass, the fix is a real root-scheduling/prefix mechanism for that topology, not silently accepting the recorded-offset fallback. Until then, `FallbackAnchors > 0` should fail the OTel gate.

## SimStartConflicts

Issue: report-only `SimStartConflicts` is not justified by the current "benign zero-duration child" argument.

Severity: High for replay correctness if non-zero conflicts are allowed through without either a proof of non-observability or a hard gate.

The new comment says one source of `SimStartConflicts` is benign: a zero-duration child whose end equals a gating wait's end may be implicitly joined at the pre-wait clock before its spawn re-anchors it, but its zero-duration finish is absorbed and the parent finish is unaffected (`engine/wcprof/wcanalyze/replay.go:302`-`318`). The added test proves only that local parent finish case (`engine/wcprof/wcanalyze/replay_cycle_test.go:283`-`323`).

That does not prove the conflict is globally harmless. A zero-duration child's simulated finish is still a dependency value other ops can read through explicit waits. A concrete topology:

- `P` waits on `T` from `50ms` to `100ms` and has zero-duration child `Z` at `100ms`.
- `B` also waits on `Z` until `100ms`.
- A later critical op `A` waits on `B` until `100ms`, then has `200ms` of self.
- Root implicit order reaches `B` before `P`.

When replaying `B`, `finish(Z)` calls `spawnTo(P, Z)`. Inside `advance(P, Z)`, `joinUpTo(100ms)` runs before the wait/spawn tie-break and sees `Z.EndNS == 100ms`; because `Z` is not started, it anchors and finishes `Z` at `P`'s pre-wait clock (`50ms`) via `joinUpTo` (`engine/wcprof/wcanalyze/replay.go:457`-`470`). The later spawn at `100ms` only increments `SimStartConflicts`; first-write-wins keeps `Z` finished at `50ms` (`engine/wcprof/wcanalyze/replay.go:389`-`404`, `engine/wcprof/wcanalyze/replay.go:481`-`484`). `B` can then finish at `50ms`, and `A` can unblock `50ms` too early. There is no fallback anchor in that path, so a `FallbackAnchors` hard gate does not catch it.

This is not an argument against counting the local fixture's conflict as benign for `P`'s finish. It is an argument that `SimStartConflicts` cannot remain report-only on the assumption that the zero-duration variant is universally harmless. The signal should be made clean and enforced, or the replay should avoid producing the same-time zero-duration join-before-spawn conflict in the first place. A narrower compromise would be to enforce conflicts for any op that is an explicit wait target or whose wrong finish can propagate, but the simpler correctness posture is: if `setStart` tries to move an op, the replay is order-dependent and should not silently rank.

`RunWhatIfs` now surfaces conflicts from scaled runs (`engine/wcprof/wcanalyze/replay.go:659`-`746`) and `report.go` prints them (`engine/wcprof/wcanalyze/report.go:167`-`177`), which is useful, but printing is not enough for the structural gate if rankings can be wrong without fallback anchors.

## Final Summary

Max-based model correct? Yes. `actWaitFixedEnd` does `clock = max(clock, recorded_start_clock + dur)`, the two-marker bookkeeping is per-wait-slot, and both fixed-wait counterexamples are fixed (`95ms` and `300ms`). It is backward-compatible when there is no concurrent child work.

FallbackAnchors hard-fail verdict: yes for the OTel gate. A faithful trace can legitimately produce one, but the current replay then used a recorded-offset approximation, so the analysis is not at the required confidence bar.

SimStartConflicts posture: report-only is not sufficient. The zero-duration case is only locally benign; it can become a wrong transitive wait result. Make the signal clean and enforce it, or fix the ordering so this conflict disappears.

Landable now? The fixed-wait correction is landable. I would not land the shared validated replay with `SimStartConflicts` intentionally report-only unless the zero-duration transitive-wait counterexample is disproved by an added fixture or the conflict is made a hard gate / eliminated.
