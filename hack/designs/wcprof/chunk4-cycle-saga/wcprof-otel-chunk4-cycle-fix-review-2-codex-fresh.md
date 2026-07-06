# wcprof Chunk 4 cycle fix review 2 - Codex fresh

Scope: round-3 review of `hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md` and the proposed prefix-to-spawn replay fix. Erik's decision to adopt prefix-to-spawn in shared `wcanalyze` is treated as settled; this review stress-tests remaining correctness risks before changing validated native replay.

## Verdict

Prefix-to-spawn remains the right layer and direction. The implementer's exact cycle extraction supports the replay diagnosis: current `finish(i)` gets an unstarted child's start by doing full `finish(parent)` (`engine/wcprof/wcanalyze/replay.go:313-323`), which can pull in later waits/joins (`engine/wcprof/wcanalyze/replay.go:351-389`) and close an artificial cycle. The 9-op fixture and native-identical cycle evidence in the implementer writeup are the right regression artifacts to land (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:49-90`).

The residual OTel loop changes my prior taxonomy. I previously said a loop that survives prefix-spawn is a genuine impossible dependency. That is too broad. There is a third class: **a wait interval can start before a child spawn but outlast it, proving the wait was concurrent with that spawn and did not gate it** (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:127-147`). Serializing that wait during prefix replay is another false edge, not a real data cycle.

So: skipping non-gating prefix waits is principled. But the proposed snippet should not land exactly as written. The rule needs to use the actual recorded wait end, not `endNS[waitTarget]` as a proxy; it needs to cover fixed waits too; and the same spawn-gating semantics need to be applied to ordinary full parent replay, otherwise child starts become path-dependent on whether a cross-tree wait happened to demand the child out of order first.

## Findings

### High - Apply the spawn-gating rule consistently, not only in `spawnTo`

The proposed `spawnTo` skips an `actWaitJoin` when the wait target outlasted the target child spawn (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:203-220`). That fixes the out-of-order prefix path. But the normal `finish(parent)` path still processes wait joins at the wait start before later spawn actions (`engine/wcprof/wcanalyze/replay.go:371-381`).

That means the same recorded shape can produce different child starts depending on traversal order:

- If a cross-tree wait asks for the child first, `spawnTo(parent, child)` skips the overlapping wait and starts the child at the recorded/concurrent spawn point.
- If root-driven replay reaches `finish(parent)` first, the existing action loop processes the wait at its start, advances the parent clock, and then later `actSpawn` sets the child start too late.

This is path-dependent replay, and it is not acceptable for a shared analyzer. The residual proved a semantic point, not just a prefix-path patch point: **a wait gates a later action only if the wait completed before that action occurred in the recorded run**. The full replay's child-spawn scheduling needs that same rule.

Clean direction: make `advance(op, bound)` the single interpreter for both prefix-to-spawn and full finish. When advancing to a child spawn bound, only dependencies whose recorded completion is `<= bound` can gate the spawn. When advancing to op end, all waits that completed by op end can gate the final finish. This preserves the prefix fix and removes traversal-order dependence.

### High - `endNS[a.ref] <= startNS[target]` is an approximation; use real wait end

The proposed code uses `s.p.endNS[a.ref] <= s.p.startNS[target]` as the gating predicate (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:212-217`) because `action` does not carry wait end (`engine/wcprof/wcanalyze/replay.go:55-60`, `engine/wcprof/wcanalyze/replay.go:162-174`). That is not precise enough for validated replay.

The correct predicate is:

```text
recordedWaitEnd <= recordedSpawnStart
```

The wait interval is the thing that blocks the waiter. Target end is only a proxy for join waits, and the compiler intentionally allows a 1ms epsilon (`engine/wcprof/wcanalyze/replay.go:41`, `engine/wcprof/wcanalyze/replay.go:165-167`).

Concrete under-serialization case with the proxy:

- spawn target child starts at 100.0ms;
- wait interval ends at 100.0ms, so it can genuinely gate the spawn;
- waited-on target ends at 100.5ms, within `joinEpsilonNS`, so the wait still compiles as `actWaitJoin`;
- proxy check sees `targetEnd=100.5ms > spawnStart=100.0ms` and skips the wait.

That skips a real gating dependency. Exact `wait.EndNS` would process it.

Concrete over-serialization case with the proxy:

- waited-on target ends at 90ms;
- the waiter remains blocked/bookkeeping until 110ms;
- child spawns at 100ms.

`targetEnd <= spawnStart` would process the wait as a spawn gate, but the recorded wait interval outlasted the spawn, so the spawn was concurrent with the wait. Exact `wait.EndNS` would skip it.

Recommendation: add a recorded wait-end field to `action` for wait actions rather than reusing target op end. Keep the target end for join classification; use wait end for spawn-gating.

### Medium - The rule should cover fixed waits as well as wait joins

The snippet only applies the recorded-back-edge predicate to `actWaitJoin`; `actWaitFixed` always advances the prefix clock (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:212-220`). But a targetless/fixed wait can also overlap a child spawn. If a fixed wait starts before a child spawn and ends after it, the recorded child spawned during that wait, so the wait did not gate that spawn.

For prefix-to-spawn and ordinary spawn scheduling, fixed waits should use the same completion-before-spawn test:

```text
waitStart + waitDuration <= spawnStart
```

If true, it gates the spawn as fixed delay. If false, it is concurrent with the spawn and should not delay that child's start. It can still contribute to the parent's eventual finish.

### Medium - The in-flight recorded-offset fallback needs an explicit diagnostic/cycle policy

The proposed `spawnTo` still has recorded-offset fallbacks when the parent or parent's parent is in flight (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:176-188`). I understand why the prototype needs a termination escape, but this is the same family of approximation Erik rejected if it can affect a parented child under scaled prefix work.

If this path remains, it should not be silent:

- increment a dedicated `InFlightSpawnFallbacks`/`PrefixCycleBreaks` counter;
- sample the ops like `FallbackAnchorOps` currently does (`engine/wcprof/wcanalyze/replay.go:224-231`);
- add a regression fixture for the case;
- decide whether it is a hard gate failure or a recorded-back-edge cycle break.

The measured traces have fallback anchors at zero after the fix (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:233-247`), which is good. It does not prove the fallback is harmless when hit.

### Medium - Perf is probably fine for 11k ops, but the algorithmic shape still needs a guard

The implementer measured `RunWhatIfs` at ~0.18s on the 11k-op trace with no observed blowup (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:233-247`). That is encouraging, but the snippet can re-walk a parent's prefix per out-of-order reference (`hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md:272-274`).

Before landing into a tool expected to run many counterfactual simulations over large traces, add either:

- a memo/stateful cursor design that advances each op monotonically per simulation, or
- a targeted stress test with many out-of-order waits to late children under one large parent.

This is not a reason to reject prefix-to-spawn. It is a reason to avoid accidental `O(wait_edges * parent_prefix)` behavior.

## Emit vs Replay

This should be fixed in replay, not by undoing §3.1 emit.

The OTel emit behavior is doing what the design says: `EmitOTelWait` attaches the wait link to the current recording span, and for a telemetry-suppressed caller that can be an ancestor span (`git show 4d6987fdc2:dagql/otelprof_hooks.go:86-134`). The cache wait path records the real blocked interval around `c.wait` (`git show 4d6987fdc2:dagql/cache.go:3943-3958`). The design explicitly calls out suppressed callers landing waits on an ancestor as the intended way to avoid forcing a caller span for every suppressed call (`hack/designs/wcprof-otel-design.md:554-603`).

Native dodges this particular residual because it has a separate native call op per caller. That does not make the OTel edge unfaithful. The OTel span model is coarser here: the ancestor span contains multiple concurrent suppressed sibling activities. A wait on that ancestor's interval is a real blocked sub-interval, but it is not necessarily a gate for every later child span under the same ancestor.

Forcing OTel to emit synthetic caller spans for all suppressed callers would solve this one shape by increasing IR granularity, but it would violate the volume posture and avoid a general replay issue: the replay currently treats any wait action as if it orders all later actions in that op. The residual proves that assumption is too strong for spans/ops that aggregate concurrent work. The replay must learn the recorded gating rule.

So the fix is replay. Emit should remain as-is unless a separate product decision is made to pay for unsuppressed caller spans.

## Skip Predicate Soundness

The principle is sound:

```text
A recorded wait gates a recorded spawn only if the wait completed no later than the spawn.
```

If the spawn happened while the wait was still open, the recorded run proves they were concurrent. Serializing the wait before that spawn invents an edge.

This is not merely a heuristic to zero the trace. It is the same recorded-temporal-order principle already used by the replay in other places: child implicit joins are only pulled when child end is `<= t` (`engine/wcprof/wcanalyze/replay.go:351-358`), and waits that did not last until target completion become `actWaitNoop` rather than a join (`engine/wcprof/wcanalyze/replay.go:162-173`).

The implementation form should change:

- use exact `wait.EndNS <= spawn.StartNS`;
- define the equality/epsilon boundary explicitly;
- apply it to `actWaitJoin` and `actWaitFixed`;
- add fixtures for exact equality, just-before, just-after, and epsilon cases.

Under counterfactuals, using recorded gating classification is the right model. A wait that did not gate a spawn in the recorded run should not become a prerequisite merely because a target is hypothetically faster. A wait that did gate the spawn should propagate speedups through the prefix.

## Residual Taxonomy

I would split the cases this way:

1. **Anchor overreach:** asking for child start calls full parent finish. Fix with prefix-to-spawn.
2. **Concurrent non-gating wait:** wait starts before a spawn but ends after it. Skip for that spawn; still let it affect the parent finish where appropriate.
3. **Genuine cycle:** a dependency completed before the spawn and is therefore a real prefix gate, but that dependency reaches back to the child/target. This is an impossible causal loop and belongs in the unified cycle/back-edge policy.

The classes can masquerade as each other if the implementation uses target end instead of wait end, or if boundary timestamps are fuzzy. That is why exact wait-end storage and boundary tests matter.

## Skip vs Back-Edge

Skip is more principled than reactive back-edge for concurrent non-gating waits. A back-edge mechanism only fires after the replay has already inserted a false edge. It would still over-serialize non-cyclic cases and can still distort rankings even when no cycle appears.

Reactive cycle breaking is appropriate only after the replay has applied the recorded gating predicate and a real dependency loop remains. At that point the loop is not "a wait overlapped a spawn"; it is "the parent could not reach the spawn without dependency X, and X depends back on the child." That should be counted/reported/broken by one explicit policy.

## Remaining Blockers Before Landing

- Store exact wait end in compiled actions and use `waitEnd <= spawnStart`, not `targetEnd <= spawnStart`.
- Apply the recorded gating rule to ordinary child spawn scheduling in full `finish(parent)`, not only to out-of-order `spawnTo`.
- Apply the same rule to fixed waits.
- Replace or refine `FallbackAnchors`: track `PrefixSpawnAnchors`, `ConcurrentWaitSkips`, and any in-flight recorded-offset fallback separately.
- Make the in-flight recorded-offset fallback explicit and tested; do not silently reintroduce Approach 1.
- Add root/out-of-order cross-root tests. `par < 0 { setStart(recorded) }` is right for truly external roots, but a later sequential root may need root-chain displacement if it is reached out of order.
- Add zero-duration/equality/epsilon tests. The residual includes a zero-duration join at `[190..190]`; boundaries are not theoretical.
- Add one large fan-in/prefix perf test or memoization.
- Run native `wcanalyze` regression tests plus the chunk2/3/4 oracle fixtures with the final implementation, not only the prototype.

## Short Summary

Emit-vs-replay verdict: replay fix. §3.1's attribution is faithful to the available OTel span granularity; the residual exposes a general replay ordering bug for waits concurrent with later child spawns, not an emit seam to hide in OTel.

Skip predicate: principled, but the snippet's `targetEnd <= spawnStart` proxy is not precise enough. Use exact `waitEnd <= spawnStart`. Misclassification: with join epsilon, `waitEnd == spawnStart` but `targetEnd > spawnStart` would skip a genuine gate; with target finishing before spawn but wait ending after spawn, the proxy would serialize a non-gating wait.

Skip vs back-edge: skip concurrent non-gating waits preemptively; use the unified back-edge/cycle mechanism only for residual real cycles after recorded gating is applied.

Remaining blockers: exact wait-end in actions, consistent full-replay spawn semantics, fixed-wait handling, explicit diagnostics for in-flight fallbacks/skips, boundary tests, cross-root scheduling tests, and a prefix fan-in perf guard.
