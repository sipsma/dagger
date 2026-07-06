# wcprof Chunk 4 final replay review - Codex fresh

Scope: final review of commit `e69d1f0049` via `hack/designs/wcprof-otel-chunk4-final-handoff.md` and `hack/designs/wcprof-otel-chunk4-hardening-e69d1f0049.patch`. I treated prefix-to-spawn as the owner-set direction and reviewed the new mechanism: gating waits sequenced at recorded `EndNS`, plus shared `advance(op, stopAt)`.

## Verdict

Not landable as-is. The end-ordered gating reformulation is the right shape for `actWaitJoin`, and it fixes the earlier path-dependence class more cleanly than a separate skip predicate. But the same mechanism is wrong for fixed waits: the patch sequences fixed waits at `EndNS` and still applies them as `clock += duration` (`engine/wcprof/wcanalyze/replay.go` in `e69d1f0049`: `compileProgram` lines 191-196, `advance` lines 449-450). If any child spawned during that fixed wait has already advanced `clock`, adding the full duration at the end double-counts.

There is also an equal-time zero-duration child issue: `joinUpTo(a.at)` runs before action tie-breaks (`replay.go:416-436`), so a zero-duration child with `StartNS == EndNS == waitEnd` can be implicitly joined before the gating wait that was supposed to gate same-time spawns. That defeats the documented `gating-wait < spawn` rank at that boundary and only surfaces as `SimStartConflicts`, which the gate reports but does not fail (`engine/wcprof/wcotel/gate.go:103-140`).

## Blocker - Fixed waits double-count after concurrent child work

Counterexample:

- Parent `P` interval `[0,300]`.
- `P` has self `[0,50]` and `[200,300]`.
- `P` has a fixed/targetless wait `[50,200]`.
- `P` spawns child `U` at recorded `[100,200]`, during the fixed wait.
- `U` has 100ms self.

Correct replay under the new concurrency rule:

- `P` does 50ms self: `clock=50`.
- `U` spawned during the fixed wait, so it starts at `50`.
- `U` finishes at `150`.
- The fixed wait completes at `200`.
- `P` then runs post-wait self `[200,300]` for 100ms.
- `P` finishes at `300`.

What the patch does:

- The child spawn at 100 sets `U` start to `50`.
- At the fixed wait action at 200, `joinUpTo(200)` first joins `U` and advances `clock` to `150` (`replay.go:416-429`).
- Then `actWaitFixed` adds the full 150ms duration (`replay.go:449-450`), taking `clock` to `300`.
- Then post-wait self adds 100ms, so `P` finishes at `400`.

That is a real finish error, not just a spawn difference. It directly attacks the invariance argument: self does not overlap waits, but children can. The new test `TestFixedWaitOverlapsSpawn` only checks `U`'s start (`replay_cycle_test.go:290-312`); it misses `P`'s finish.

The fix is not to abandon end-ordering. It is to treat fixed waits like "complete by this recorded end", not "add duration at this recorded end." That requires preserving the simulated clock at the wait start, or representing fixed waits with enough state to do:

```text
clock = max(clock, simulatedWaitStartClock + recordedDuration)
```

For join waits, `max(clock, finish(target))` already has this shape. For fixed waits, `clock += duration` at `EndNS` is wrong once other concurrent work can advance `clock` during the interval.

## Blocker - Equal-time zero-duration child bypasses the action rank

The patch relies on `actionRank`: gating wait at a timestamp sorts before self, then spawn, then noop (`replay.go:153-168`). That correctly says `waitEnd == spawn` should gate the spawn.

But `advance` calls `joinUpTo(a.at)` before every action (`replay.go:434-436`), and `joinUpTo` joins every child with `EndNS <= t` (`replay.go:416-429`). So for a zero-duration child `U [100,100]` and a gating wait ending at `100`, the sequence is:

- at the wait action, before the wait itself runs, `joinUpTo(100)` sees `U.EndNS == 100`;
- `U` is not started, so it is anchored at the pre-wait clock and may be finished;
- only then does the gating wait run;
- the later `actSpawn` for `U` hits `setStart` with a different value, increments `SimStartConflicts`, but first-write-wins preserves the wrong ungated start (`replay.go:348-362`).

This is exactly the degenerate equality class the final review asked us to hammer. The existing boundary test uses a non-zero child `[100,150]` (`replay_cycle_test.go:235-281`), so it does not cover the failure. The handoff says `SimStartConflicts=0` on the measured traces, which is good empirical evidence for those traces, but the mechanism itself is not correct at the boundary.

`SimStartConflicts` also is not a hard gate violation today. `CheckStructural` records it (`gate.go:103-105`) and prints it (`gate.go:169`), but does not add a violation (`gate.go:116-140`). If this counter is the invariant protecting first-write-wins, nonzero should fail OTel structural validation, not merely appear in output.

## Other Edge Cases

`actWaitNoop` / cancellation: leaving abandoned waits at `StartNS` is consistent with the old model. I did not find a new cancellation-specific break from end-ordering because non-gating waits still do not advance time. The existing caveat remains: self-time subtracts the wait interval, so abandoned waits compress the op's replayed self. That is pre-existing.

`joinEpsilonNS`: the patch still classifies a wait as a join when `waitEnd >= targetEnd - 1ms` (`replay.go:181`). Sequencing the action at the wait's own end is the right improvement, but equality cases within epsilon can still gate a spawn even if target end is slightly after the spawn. That is probably acceptable as the existing timestamp-fuzz policy, but it needs an explicit boundary test with `waitEnd == spawn < targetEnd` inside epsilon.

## Jaccard 0.80

The pushback is partly right and partly overstated.

It is sound to say a buildkit-heavy native-vs-OTel oracle can have low Jaccard because the two sources bucket the same work differently. The handoff's `Host.directory` vs `:uploading` example is plausible source bucketing, not necessarily replay regression.

But the stated reason "Jaccard ranks by self-time, so replay cannot move it" is not correct. `TopBottlenecks` runs `RunWhatIfs` and sorts positive classes by `SavedNS`, not by self-time (`engine/wcprof/wcotel/oracle.go:50-68`). `RunWhatIfs` uses self-time to choose candidates and thresholds, but the top-N ordering is replay output. A replay change can move Jaccard by changing savings.

The decisive check is exactly the one in the prompt: compare pre- and post-fix top-N keys/savings. If Jaccard is identical pre/post, the low absolute number is not a cycle-fix regression. If only the absolute post-fix 0.15 is reported, the "cannot move it" argument is not sufficient.

## Recorded-Offset Fallback

The surviving fallback is still an approximation. For `par < 0`, `spawnTo` anchors a root at recorded start (`replay.go:465-475`). For `par` in flight, it falls back to recorded offset (`replay.go:488-493`, `replay.go:511-520`). Those are counted, which is a necessary improvement, but the answer can still be wrong under a what-if that shifts the relevant root/ancestor.

I buy that these are last-resort cases rather than the normal path. A parent in flight means the target is being demanded from within the same prefix before the parent reaches the spawn: there may not be a well-defined acyclic scaled start to compute. A cross-root forward reference can also be inherently cyclic with root chaining. But if a fallback can produce a known approximate answer, it should be treated as a degraded-confidence result. At minimum, nonzero fallback anchors and nonzero start conflicts need clear report semantics; for the OTel gate, I would strongly consider making `SimStartConflicts > 0` hard-fail.

## Landability

Do not land `e69d1f0049` as-is into validated native replay.

Required before landing:

- Fix `actWaitFixed` end-ordering so it gates by fixed wait completion time without adding the full duration after concurrent child joins.
- Add an assertion for parent finish to `TestFixedWaitOverlapsSpawn`; the current test only checks child start.
- Fix or explicitly handle the zero-duration `waitEnd == spawn == childEnd` ordering bug where `joinUpTo` runs before the gating wait.
- Add tests for zero-duration child at wait-end, `waitEnd == spawn < targetEnd` within `joinEpsilonNS`, and fixed waits with a child ending before the wait end.
- Make `SimStartConflicts` a hard invariant for OTel gate, or document why a nonzero value is allowed to pass despite indicating residual order dependence.
- Verify pre/post oracle top-N equality if using low Jaccard as a non-regression argument.

## Short Summary

Landable in validated native: no, not as-is.

Blockers: fixed waits at `EndNS` double-count after concurrent child work; zero-duration child at the same timestamp as a gating wait can be joined before the wait despite the action rank; `SimStartConflicts` is only report-only.

Strongest attempted counterexample: fixed wait `[50,200]`, child `[100,200]`, post-wait self `[200,300]`. It holds. The patched replay starts the child correctly but then joins it before adding the fixed wait duration at 200, causing parent finish `400` instead of the correct `300`.
