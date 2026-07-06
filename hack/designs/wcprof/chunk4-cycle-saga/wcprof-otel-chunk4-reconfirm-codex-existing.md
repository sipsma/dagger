# wcprof x OTel Chunk 4 replay reconfirm review

Reviewer: Codex  
Scope: review only. I read `wcprof-otel-chunk4-reconfirm-handoff.md` and `wcprof-otel-chunk4-postreview.patch` first, then checked the final source at `692aaabd3f`.

## Verdict

Landable now, with one policy change I recommend before treating the OTel structural gate as a soundness gate: make `FallbackAnchors > 0` a hard failure for corrected-engine traces.

The new max-based fixed-wait model is correct. It fixes the hole I missed in the previous review: fixed waits cannot be modeled as an additive `clock += dur` at wait end, because that serializes children that overlapped the wait. The new two-marker model records the simulated clock at fixed-wait start and applies `clock = max(clock, startClock + dur)` at fixed-wait end. That is the right shape.

I agree with keeping raw `SimStartConflicts` report-only for now. It is not a clean invariant because the zero-duration child-at-join-end case is a real benign conflict in the current replay ordering. The invariant with teeth should be `FallbackAnchors == 0`; harmful start conflicts require the recorded-offset fallback path, while the benign conflict does not.

## REAL Issues / Decisions

### MEDIUM: `FallbackAnchors > 0` should hard-fail the structural gate

`FallbackAnchors` is now exactly the remaining recorded-offset approximation. The normal path anchors out-of-order children by replaying the producer prefix (`finish` calls `spawnTo`, and `spawnTo` calls shared `advance`) (`engine/wcprof/wcanalyze/replay.go:408-555` at `692aaabd3f`). The fallback path only fires for parentless/cross-root forward references, in-flight ancestors, or inconsistent parent/child structure (`engine/wcprof/wcanalyze/replay.go:512-555`), and `fallbackAnchor` preserves recorded offset (`engine/wcprof/wcanalyze/replay.go:557-568`).

That is the rejected `startOf` approximation in a narrower form. The new cross-root test demonstrates why it is not harmless: baseline can have `FallbackAnchors > 0` with no conflict, but a what-if factor can then produce `SimStartConflicts` when the shifted schedule disagrees with the recorded-offset anchor (`engine/wcprof/wcanalyze/replay_cycle_test.go:393-440`; handoff lines `61-68`).

There can be faithful trace data with `FallbackAnchors > 0`: a real cross-root wait is not necessarily malformed. But it is not faithfully *replayable under counterfactuals* by this model without approximation. For a correctness gate, rejecting it is not a false positive; it is an unsupported-shape failure. The error text should say that, rather than blaming OTel emit.

Recommendation: make nonzero fallback anchors a hard structural-gate violation on corrected-engine traces. Keep the CLI/report diagnostic too.

### LOW: `SimStartConflicts` is correctly report-only unless it is split into clean subcounters

The implementer is right that raw `SimStartConflicts` now conflates harmful and benign cases. `setStart` counts every attempted different start (`engine/wcprof/wcanalyze/replay.go:389-403`), and `TestZeroDurChildAtJoinWaitEnd` intentionally produces one conflict that does not corrupt finish (`engine/wcprof/wcanalyze/replay_cycle_test.go:283-324`).

The benign case is real:

- `joinUpTo` runs before the wait action at the same timestamp (`engine/wcprof/wcanalyze/replay.go:475-478`);
- a zero-duration child with `EndNS == wait.EndNS` can be implicitly joined and anchored before the wait action;
- its later spawn action tries to set a different start;
- the child has zero duration and is absorbed by the same wait boundary, so the parent finish remains correct.

Because of that, hard-failing raw `SimStartConflicts` would false-positive. I would not spend landing risk on "cleaning" this counter now. If desired later, split it into:

- benign zero-duration boundary conflicts;
- fallback-associated conflicts;
- unexpected conflicts.

Then hard-fail the latter two. For this landing, hard-failing `FallbackAnchors` gives the invariant teeth without rejecting the known benign zero-duration case.

### LOW: the fixed-wait start/end marker ordering is a policy choice, but a defensible one

At equal timestamps, `actWaitFixedEnd` ranks with max gates before self/spawn, while `actWaitFixedStart` ranks after self/spawn (`engine/wcprof/wcanalyze/replay.go:175-192`). That means:

- a spawn exactly at fixed-wait end is gated by the completed fixed wait;
- a spawn exactly at fixed-wait start remains concurrent with the fixed wait;
- a zero-duration fixed wait has its end marker before its start marker, but `dur == 0`, so it cannot advance the clock and has no effect.

This is consistent with half-open interval semantics and with the replay's conservative choice to preserve concurrency when the recording does not prove a gate. No blocker.

## NOISE / Non-Issues

### Max-based fixed waits are correct

Compile now emits two actions for a fixed wait: `actWaitFixedStart` at `w.StartNS`, and `actWaitFixedEnd` at `w.EndNS` with `dur = w.Duration()` (`engine/wcprof/wcanalyze/replay.go:202-229`). The simulation allocates per-simulation fixed-wait clock slots (`engine/wcprof/wcanalyze/replay.go:279-281`, `engine/wcprof/wcanalyze/replay.go:325-339`) and interprets the two markers as:

```go
start: fixedWaitClock[slot] = clock
end:   clock = max(clock, fixedWaitClock[slot] + dur)
```

(`engine/wcprof/wcanalyze/replay.go:490-498`).

That gives the right semantics:

- if no child/work overlaps the fixed wait, `clock` is still the start clock at the end, so `max(clock, start+dur)` is equivalent to the old `+= dur`;
- if a child finishes inside the wait, the child join and the fixed wait combine by max, not by sum;
- if a child spawns during the wait, the wait start does not serialize that spawn, and the wait end still gates the parent finish.

The tests cover both fixed-wait failures directly: child finishes inside lock = `95ms`, and child spawned during lock = `300ms` (`engine/wcprof/wcanalyze/replay_cycle_test.go:326-391`). The handoff's report that the 86k native trace has 12 real fixed waits and bit-unchanged makespan is consistent with the code: where no child overlaps the delay, the max model collapses to the old additive result.

### The fixed-wait action count preallocation is not a correctness issue

`totalActions` still counts one per wait before fixed waits are expanded to two actions (`engine/wcprof/wcanalyze/replay.go:149-173`, `engine/wcprof/wcanalyze/replay.go:220-224`). That only underestimates slice capacity; `append` grows the slice correctly. No correctness concern.

### RunWhatIfs conflict reporting is a useful improvement

`RunWhatIfs` now returns the maximum `SimStartConflicts` seen across what-if simulations (`engine/wcprof/wcanalyze/replay.go:655-747`), and the report prints it (`engine/wcprof/wcanalyze/report.go:161-182`). This matters because baseline factor 1 can hide recorded-offset disagreements that only appear after a factor shifts the schedule. Good change.

## Answers To Open Questions

1. **Max-based fixed-wait model correct?** Yes. It is the right concurrent non-scalable segment model. It is backward-compatible when no child overlaps the fixed wait, and it fixes both the `95ms` and `300ms` cases.
2. **`SimStartConflicts`: clean-and-enforce or report-only?** Keep raw `SimStartConflicts` report-only for this landing. The benign zero-duration boundary conflict is legitimate. If the project wants a hard invariant later, split the counter into benign/fallback/unexpected categories and enforce the non-benign categories.
3. **`FallbackAnchors` hard-fail?** Yes. This is the actual approximation and the harmful conflict precondition. A faithful trace can produce fallback anchors, especially cross-root waits, but that means the replay used an unsupported recorded-offset approximation. A hard gate is appropriate for corrected-engine traces that are supposed to support exact counterfactuals.
4. **Landable now?** Yes, assuming the gate policy is updated or explicitly accepted as a follow-up. No replay-model blocker remains in the patch I reviewed.
