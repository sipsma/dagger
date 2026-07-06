# wcprof x OTel Chunk 4 cycle fix, round 3 review

Reviewer: Codex  
Scope: analysis only, no code changes. I read `hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md` first and verified the claims against the current `engine/wcprof/wcanalyze/replay.go`.

## Verdict

Prefix-to-spawn is still the correct direction. The implementer's empirical result is consistent with the source: the current anchor path calls `finish(parent)` just to obtain an unstarted child's start (`engine/wcprof/wcanalyze/replay.go:313-323`), and that can replay the parent's whole later timeline, including explicit wait joins (`engine/wcprof/wcanalyze/replay.go:379-381`) and implicit joins (`engine/wcprof/wcanalyze/replay.go:353-367`). That is enough to close a false cycle when the cross-tree singleflight waits are otherwise real.

I do **not** think the exact throwaway `spawnTo` in the writeup should land as-is in validated native replay. It fixes the measured trace, but it still has two correctness hazards:

1. the proposed skip predicate uses the wait target's recorded finish as a proxy for the wait interval end, even though the replay compiler currently discards the real wait end;
2. it creates a second replay pass whose "skip-for-spawn" behavior can deliberately disagree with full `finish(parent)`, with `setStart` silently masking only part of that disagreement.

The owner-set direction should be implemented as a shared `advanceTo`/prefix replay engine, not as an independent helper with slightly different semantics.

## REAL Issues

### HIGH: the skip predicate must use the actual wait end, not the target op end

The writeup's proposed residual fix is:

```go
case actWaitJoin:
    if s.p.endNS[a.ref] <= s.p.startNS[target] {
        if f := s.finish(a.ref); f > clock { clock = f }
    }
```

from `hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:212-217`. The doc calls this "exact-enough" because a join wait's end is approximately the target end (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:268-271`).

That is not exact enough for correctness. The graph keeps the wait interval explicitly: `WaitEdge.StartNS` and `WaitEdge.EndNS` are first-class fields (`engine/wcprof/wcanalyze/graph.go:48-56`) and native/OTel load preserves `EndNS` (`engine/wcprof/wcanalyze/graph.go:247-254`). But the replay action drops it: `action` has only `at`, `dur`, `ref`, and `kind` (`engine/wcprof/wcanalyze/replay.go:55-60`). During compile, a join wait uses `w.EndNS` only to classify the action, then discards it (`engine/wcprof/wcanalyze/replay.go:162-173`).

The correct partition is:

```text
wait.EndNS <= spawn.StartNS  => the wait was complete before the spawn; replay it for the spawn prefix
wait.EndNS >  spawn.StartNS  => the spawn occurred while the wait was still open; the wait did not gate that spawn
```

Using `target.EndNS` can misclassify. Concrete case:

```text
P waits on D: wait [50,110], D recorded [40,90]
P spawns T at 100
```

Recorded data proves T spawned while P's wait was still open, so that wait did not gate T. The proposed predicate sees `D.EndNS=90 <= T.StartNS=100` and serializes the wait into T's prefix anyway. Under a counterfactual that slows D, T is falsely delayed.

This is not hypothetical at the model level: the design already distinguishes wait intervals from target intervals, and wait end can lag target end due to wakeup/scheduler/export timing. The fix should thread the real `WaitEdge.EndNS` through the compiled action and use it for prefix gating.

### HIGH: fixed waits need the same cutoff rule

The proposed `spawnTo` still applies every fixed wait whose start precedes the spawn:

```go
case actWaitFixed:
    clock += a.dur
```

from `hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:218-219`.

That has the same bug class as the residual join wait. A fixed/resource wait can overlap a child spawn. If a fixed wait starts before the spawn but ends after it, the recorded data proves that wait did not gate the spawn. Since fixed waits already encode duration (`engine/wcprof/wcanalyze/replay.go:168-170`), the prefix replay can compute `waitEnd = a.at + a.dur` and skip it for a spawn cutoff when `waitEnd > spawnStart`.

Leaving fixed waits out of the cutoff rule keeps a real false-serialization path in the shared replay. It may not be the current module-loading cycle, but it is the same correctness bug.

### HIGH: a separate `spawnTo` pass plus first-write-wins can hide inconsistent starts

`setStart` is first-write-wins and silently ignores later attempts to set a different simulated start (`engine/wcprof/wcanalyze/replay.go:299-303`). The proposed `spawnTo` relies on that idempotence: it skips a concurrent wait for the child prefix, then later says `finish(parent)` will "recompute the same prefix clock" (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:168-173`).

With the proposed helper as written, full `finish(parent)` does **not** recompute the same prefix clock. Current full finish processes wait joins at the wait start (`engine/wcprof/wcanalyze/replay.go:371-381`) and fixed waits at the wait start (`engine/wcprof/wcanalyze/replay.go:383-384`). So the prefix path can skip a wait because it overlaps the spawn, while the full finish path later includes that same wait before the same spawn.

Example:

```text
P has wait W [50,205] on D
P spawns T at 190
P spawns U at 195
```

`spawnTo(P,T)` correctly skips W and starts T at the prefix clock. A later `finish(P)` in the current action loop joins W at 50, delays the clock, then reaches both spawn actions late. `setStart` ignores the late T start because T was already started, but U can still be started late, and P's finish can still be too late.

That is a correctness bug waiting for a slightly different graph. It also makes disagreement undetectable because `setStart` has no assertion/counter for "already started with a different value."

This strongly favors the unified engine: make `finish(i)` and prefix anchoring share the same `advanceTo(op, cutoff)` logic. For a spawn prefix, the cutoff is the child's recorded spawn. For full finish, the cutoff is the op's recorded end. Wait joins/fixed waits should be applied when their wait interval ended by the cutoff/action boundary, not merely because their start precedes it. Then a child start computed during prefix anchoring and during full finish is the same value by construction, or a disagreement is a bug worth surfacing.

### MEDIUM: the OTel attribution is not an emit bug, but §3.1's "ancestor actually blocked" wording is now too strong

The residual is surfaced by the OTel design choice to put a suppressed caller's wait link on the current ancestor span. That is exactly what the approved design specifies (`hack/designs/wcprof-otel-design.md:554-580`), and the design already acknowledges the concentrated fan-in from suppressed siblings (`hack/designs/wcprof-otel-design.md:582-587`).

I do not think this is papering over a bad OTel emit in replay. The wait interval is real, and a span can contain concurrent activities. The replay must not treat every wait that starts before every later child spawn as a spawn gate. That is a general replay-gating issue, and the native replay has the same structural vulnerability.

But the wording in §3.1 is no longer precise: "that ancestor is the op that actually blocked" (`hack/designs/wcprof-otel-design.md:558-561`) is only true at the scope/goroutine level. The ancestor span may still spawn or run other concurrent children while one suppressed caller is blocked. That distinction is exactly what the residual proved (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:133-141`).

Action: update the design text and validation framing. The emit is acceptable only if the shared replay is concurrency-aware for waits whose intervals overlap child spawns. Otherwise the attribution creates false serialization.

### MEDIUM: exact wait-end cutoff is sound for recorded overlaps, but it is not omniscient for all concurrency

For the case the implementer found, the partition is sound:

```text
wait.EndNS > spawn.StartNS
```

means the child spawned while the wait was open, so the wait cannot have gated that spawn. Skipping it for that prefix remains sound under counterfactual scaling because the recorded graph tells us the spawn was independent of that wait; changing the target class speed should not invent a causal dependency.

The converse is weaker:

```text
wait.EndNS <= spawn.StartNS
```

means the wait could have gated the spawn under the sequential op model. It does not prove causality in a span that aggregates concurrent goroutines. That limitation already exists in replay's chronological-action model: actions are sorted by recorded time (`engine/wcprof/wcanalyze/replay.go:176-185`), and implicit joins intentionally bake observed ordering in as constraints (`engine/wcprof/wcanalyze/replay.go:23-27`, implemented at `engine/wcprof/wcanalyze/replay.go:353-367`).

I would not block prefix-spawn on solving unknowable concurrency with no signal, but the new code should be explicit: it is removing **proven non-gating** waits, not proving every ended-before wait is causal. If future traces show false serialization from ended-before-but-independent waits, the fix is more granular emit/op structure or explicit concurrency metadata, not inference in the loader.

### MEDIUM: root and in-flight fallback paths still preserve recorded offsets

The proposed helper still has recorded-offset fallback paths:

- root/parentless target: `setStart(target, startNS[target])` (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:175`);
- parent in-flight/self-reference: `simStart[par] + recorded offset` (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:186-188`);
- parent parent in-flight: recorded-offset fallback for `par` (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:176-184`).

Those may be rare, but they are the same family of approximation that made `startOf` unacceptable when it affected real cross-tree starts. The current replay comment explicitly says roots stay in the original trace frame because cross-tree targets may be anchored before their roots replay (`engine/wcprof/wcanalyze/replay.go:29-34`), and `Run` still schedules roots later (`engine/wcprof/wcanalyze/replay.go:275-295`).

If the owner wants "no wrong counterfactuals" as the standard, these paths need tests and diagnostics:

- cross-root wait target reached before its root is replayed;
- in-flight parent prefix recursion;
- a real residual cycle where a pre-spawn gating dependency waits back on the target.

For the last case, do not silently use recorded offset. It should go through the same cycle/back-edge reporting policy.

### MEDIUM: performance is measured on 11k ops, but the algorithmic bound still needs protection

The writeup reports no performance regression on the 11,162-op real trace (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:235-247`) and notes that `spawnTo` re-walks parent prefixes per out-of-order reference (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:272-274`). That is enough to unblock experimentation, not enough for validated shared replay.

Current replay is intentionally a cheap array-based DP (`engine/wcprof/wcanalyze/replay.go:36-39`). A naive prefix walk can become super-linear on a parent with many out-of-order referenced children. The landing version should either:

- memoize/cursor prefix advancement per `(simulation, op, cutoff/action-index)`; or
- include a stress test that demonstrates acceptable behavior on a large fan-out/op-prefix workload.

This is not a reason to reject prefix-spawn. It is a reason not to land the throwaway implementation unchanged.

### LOW: diagnostics need to change, not disappear

The writeup says `FallbackAnchors` goes to 0 under prefix-spawn (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:40-45`, `hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:263-267`). That is fine if the old signal was mostly reporting anchor artifacts.

But the replacement should not be "no signal." The gate should expose at least:

- prefix-anchor count;
- concurrent waits skipped for spawn prefixes;
- exact in-flight/recorded-offset fallbacks, if any remain;
- start-disagreement attempts in `setStart`;
- real cycle/back-edge count.

Those are the counters that will tell us whether the new replay is doing ordinary prefix anchoring or silently leaning on approximations.

## NOISE / Non-Issues

### Prefix-to-spawn itself is the right fix direction

The source matches the implementer's diagnosis of anchor overreach. Current `finish(i)` starts an unstarted op by fully finishing its parent (`engine/wcprof/wcanalyze/replay.go:313-323`). Full parent replay can pull in actions after the child's spawn, including wait joins (`engine/wcprof/wcanalyze/replay.go:379-381`). That is too much work for a start anchor.

Replaying only the scaled parent prefix to the child's spawn is the correct semantic replacement. It preserves the config-parse behavior that `startOf` broke: parent pre-spawn self-time remains scaled before the child start.

### Skipping waits that overlap the spawn is not a heuristic

With the corrected exact predicate (`wait.EndNS > spawn.StartNS`), skipping is not causal guessing. It is a direct use of recorded temporal facts: the child was spawned while the wait was still open, so that wait did not gate that spawn. This is the same kind of recorded-interval reasoning already used to classify abandoned waits (`engine/wcprof/wcanalyze/replay.go:162-173`).

### Fixing replay is legitimate even though OTel surfaced the residual

The residual appears because OTel attributes suppressed-caller waits to an ancestor span, but the replay bug is broader: the replay currently treats wait start as a serialization point for later actions. The native path can hit the same anchor-overreach class, and the implementer reports native cycles identically (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:16-24`, `hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:235-244`). I cannot independently inspect the raw trace here, but the code-level mechanism is real.

## Shared Questions

### 1. Emit vs replay

Verdict: fix replay, and update the design wording. Do not solve this by removing or suppressing the OTel wait.

The wait edge is real and load-bearing. Dropping it would regress the singleflight fix. Emitting a per-joiner hidden span would avoid this specific ancestor-overlap shape but reopens the volume/visibility tradeoff the design deliberately rejected. The correct general rule is: a wait constrains a child spawn only if the wait interval completed before that spawn; an overlapping wait is concurrent with that spawn.

That said, §3.1 should stop claiming the ancestor span "actually blocked" as a whole. It contains the blocked activity; it may also contain independent concurrent child spawns.

### 2. Skip-predicate soundness

The semantic predicate is sound:

```text
include wait in prefix iff wait.EndNS <= cutoff.StartNS
```

The proposed implementation predicate is not sound as written:

```text
target.EndNS <= cutoff.StartNS
```

It can include a wait that outlived the spawn but whose target finished before the spawn. That is a false gating edge. Thread the real wait end through `action`.

Apply the same rule to fixed waits using `a.at + a.dur`.

Boundary: `wait.EndNS == spawn.StartNS` should count as complete-by-spawn, so `<=` is the right comparison if timestamps are treated as half-open intervals. Do **not** reuse `joinEpsilonNS` (`engine/wcprof/wcanalyze/replay.go:41`) for this cutoff unless there is a separate clock/export tolerance argument; the existing epsilon is for classifying whether a wait reached the target's end (`engine/wcprof/wcanalyze/replay.go:162-173`), not for proving that a wait did or did not overlap a child spawn.

### 3. Residual taxonomy

The two-tier policy is right:

- **Concurrent non-gating edge:** wait interval overlaps the spawn cutoff. Skip it for that prefix. This should not increment cycle warnings.
- **Genuine residual cycle:** a pre-spawn gating dependency, implicit join, or explicit wait that actually completed before the spawn still leads back to the target/in-flight op. That is a real cycle under the replay model and should be broken/reported by the shared back-edge mechanism.

The landing code should make those cases mechanically distinct. Do not let recorded-offset fallback absorb the second case silently.

### 4. First-write-wins / engine shape

The current `setStart` behavior (`engine/wcprof/wcanalyze/replay.go:299-303`) is acceptable only if all paths that can start an op are guaranteed to compute the same value. The proposed separate `spawnTo` does not provide that guarantee because full `finish` still has different wait semantics.

Recommendation: implement a unified `advanceTo` engine:

- one action loop shared by prefix anchoring and full finish;
- action cutoff parameter;
- exact wait-end gating;
- fixed waits handled by interval end;
- start writes either match or increment a diagnostic/fail test.

This is meaningfully better than an independent `spawnTo`; it is the fundamental version of prefix-spawn.

## Remaining Blockers Before Landing

1. Add exact wait-end data to compiled actions and use it for prefix gating.
2. Apply the cutoff rule to fixed waits as well as join waits.
3. Avoid divergent `spawnTo` vs `finish` semantics; land a shared `advanceTo`/prefix engine or prove identical start calculations with diagnostics.
4. Add tests for:
   - minimal module-source cycle;
   - config-parse correctness;
   - wait target finishes before spawn but wait ends after spawn;
   - fixed wait overlaps spawn;
   - first-write disagreement attempt;
   - zero-duration/equal-boundary spawn (`wait.EndNS == spawn.StartNS`);
   - cross-root or in-flight-parent anchoring.
5. Replace `FallbackAnchors` with refined counters rather than removing visibility.
6. Add a fan-out/prefix performance stress test or memoization.

## Bottom Line

Adopt prefix-to-spawn in shared `wcanalyze`, but do not land the writeup's exact `spawnTo` helper as the final native replay fix. The correct landing shape is a unified prefix/full replay engine using exact wait intervals. With that change, the cycle diagnosis and fix direction are sound; without it, the patch risks trading the current anchor overreach for new false serialization that the first-write-wins start cache can hide.
