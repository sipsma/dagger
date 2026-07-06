# wcprof Chunk 4 cycle - fundamental replay analysis

Scope: re-opened review after Erik's ruling that recorded-offset `startOf` / Approach 1 is off the table if it produces a wrong counterfactual. This analysis re-reads the actual worktree `engine/wcprof/wcanalyze/replay.go` and reasons from the concrete `POST /query -> app/lib`, `app -> T=load foo`, `lib/W waits on T` example.

## Bottom Line

Prefix-spawn is the right fix direction. More precisely: an out-of-order request for a child op's finish must first compute the child start by replaying the authoritative parent only up to the child's spawn action under the current what-if factors, then stop. It must not replay the parent's full finish, and it must not pin the child to the original offset.

For the concrete `app`/`lib` example, prefix-spawn terminates and gives the right answer. The claim that it "still cycles if the parent joins a cross-referencer before the child's spawn" is not true for the normal singleflight topology where `W` waits on `T`: because `W` waits until `T` finishes, `W.EndNS >= T.EndNS > T.StartNS`, so an implicit join before `T`'s spawn cannot join `W`. A prefix replay of `app` to `T.StartNS` does not pull in `W`.

Prefix-spawn can still expose a real residual cycle, but only in a stricter topology: the parent must have a real dependency before the child's spawn, such as an explicit wait on `W` before `T.StartNS`, or an earlier child `U` that is implicitly joined before `T.StartNS`, and that dependency must itself require `T`. That graph says "parent must finish/wait on X before spawning T, while X waits on T." There is no causal schedule satisfying that; it is not the sibling singleflight cycle and should be handled by the shared recorded-back-edge/cycle policy as a genuine data cycle.

## Replay Facts From The Worktree

The replay model says children start at the parent's current simulated clock, not at recorded offsets. The top comment defines child spawns as "anchor the child's simulated start at the current clock" (`engine/wcprof/wcanalyze/replay.go:17-19`). The compiled program emits an `actSpawn` at each child's recorded `StartNS` (`engine/wcprof/wcanalyze/replay.go:159-161`), and the replay executes that spawn as `s.setStart(a.ref, clock)` (`engine/wcprof/wcanalyze/replay.go:377-379`). That `clock` already includes factor-scaled self time (`engine/wcprof/wcanalyze/replay.go:375-376`) and any earlier dependencies.

The bug is that an out-of-order finish request currently asks the wrong question. `finish(i)` needs `i`'s start. If `i` is not started, it calls `s.finish(parent)` (`engine/wcprof/wcanalyze/replay.go:313-323`). That computes the parent's full completion, including implicit joins of children sorted by end time (`engine/wcprof/wcanalyze/replay.go:351-368`, `engine/wcprof/wcanalyze/replay.go:389`) and explicit wait joins (`engine/wcprof/wcanalyze/replay.go:379-381`). Full parent completion is more than the child-start query requires.

The fallback then anchors in the parent's shifted original frame (`engine/wcprof/wcanalyze/replay.go:324-338`), and the file-level comment justifies original-frame anchoring for cross-tree wait targets whose roots have not been replayed (`engine/wcprof/wcanalyze/replay.go:29-34`). That rationale is valid for externally scheduled roots. It is not valid for a parented child whose spawn is causally produced by its parent's scaled prefix.

The current recursive engine has three ways to call `finish()`:

- anchor path: unstarted target asks for parent full finish (`engine/wcprof/wcanalyze/replay.go:313-323`);
- implicit join path: parent reaches original time and joins ended children (`engine/wcprof/wcanalyze/replay.go:351-368`);
- explicit wait path: a wait that lasted until target completion joins the target (`engine/wcprof/wcanalyze/replay.go:379-381`; classification at `engine/wcprof/wcanalyze/replay.go:162-173`).

The anchor path is the odd one out. It is not a real dependency on parent finish; it is a start-scheduling query. Treating it as a finish dependency both over-serializes and can introduce an artificial cycle.

## Concrete Example

Recorded:

- `POST /query` spawns siblings `app` and `lib`.
- `app` does 100ms of config parse, then spawns `T = load foo` at recorded 100ms.
- `T` runs 100ms to 300ms.
- `lib` has `W`, which dedups onto `T`, so `W` waits on `T` and unblocks at 300ms.

What-if: config parse goes to 0.

Correct result:

- `app` starts at 0.
- prefix replay of `app` to `T`'s spawn applies factor 0 to the 100ms parse.
- `T` starts at simulated 0, runs for 200ms, and finishes at 200ms.
- `W` waits on `T` and unblocks at 200ms.
- The profiler credits 100ms savings to config parse across the cross-tree wait.

Approach 1 result:

- `startOf(T)` sets `T = app.simStart + (T.origStart - app.origStart)`.
- `T` remains pinned at 100ms even though the parent prefix became 0.
- `W` still unblocks at 300ms.
- The savings is missed. This is a wrong counterfactual, not merely a diagnostic limitation.

Prefix-spawn result:

- `ensureSpawned(T)` asks `app` to replay only up to `T`'s spawn action.
- It executes the scaled self segment before the spawn, sets `T` at the current simulated clock, and stops.
- `finish(T)` can then compute `T`'s finish and unblock `W`.
- This is exactly the model already used by normal in-order replay; it just makes out-of-order targets use the same semantics.

## Does Prefix-Spawn Cycle?

For the sibling singleflight topology under discussion: no.

Reason: the only way prefix replay of `app` can join a cross-referencer before spawning `T` is if that cross-referencer is a child of `app` whose recorded end is `<= T.StartNS`, because implicit joins only join children whose `EndNS <= t` (`engine/wcprof/wcanalyze/replay.go:353-358`). But a real wait on `T`'s completion is compiled as `actWaitJoin` only when `wait.EndNS >= T.EndNS - epsilon` (`engine/wcprof/wcanalyze/replay.go:162-173`). Since `T.EndNS > T.StartNS`, a child that waits for `T` cannot have `EndNS <= T.StartNS`. It is therefore not joined by prefix-to-`T.StartNS`.

In the concrete `app`/`lib` example, `W` is not even a child of `app`; it is in the sibling `lib` subtree. Prefix replay of `app` has no path to join `W` unless `app` itself has a separate pre-spawn wait/dependency on `W`. If it does, that is not the reported sibling singleflight shape anymore.

Where prefix-spawn can cycle:

- parent `P` has an explicit wait before child `T`'s spawn, and that wait target requires `T`;
- parent `P` has an earlier child `U` ending before `T.StartNS`, so prefix replay must implicitly join `U`, and `U` requires `T`;
- the parent tree itself is cyclic/malformed.

Those are genuine causal cycles. The data says `P` cannot reach the spawn of `T` until `X` finishes, and `X` cannot finish until `T` exists/finishes. A correct prefix-spawn implementation should not hide that with recorded-offset anchoring. It should report/break it through the same principled cycle/back-edge mechanism used for real wait/join cycles.

So the implementer-reviewer statement needs qualification. "Prefix-spawn still cycles" is false for the normal cross-tree wait-on-target topology. It is true only if the parent's prefix contains a real dependency that reaches back to the target. That residual cycle is not an argument for Approach 1; it is a graph/data cycle that must be resolved explicitly.

## Fundamental Core

The deep bug is a conflation of two different questions:

1. "When does op `i` start under this counterfactual?"
2. "When does op `i` finish under this counterfactual?"

The current replay answers question 1 by calling question 2 on the parent (`engine/wcprof/wcanalyze/replay.go:313-323`). That is the overreach. It turns the parent-child spawn relation into a dependency on the parent's full finish. In a graph with true cross-tree waits, that invented dependency can complete a cycle that does not exist in the causal schedule.

Approach 1 fixes the cycle by answering question 1 from recorded offsets. That removes the overreach but violates the replay's own child-spawn semantics under counterfactual factors. It is not fundamental.

The clean fix is to make start computation first-class and authoritative:

- A root starts from root chaining/external schedule.
- A non-root starts when its parent reaches that child's spawn action in simulated time.
- A cross-tree wait may demand the target's finish out of order, but it must first invoke the target's authoritative start scheduler, not assign a start itself.
- If computing that start encounters a dependency cycle in the prefix, that is a real cycle and should go through the unified cycle/back-edge policy.

This can be implemented as `ensureSpawned(child)` / `advance(parent, untilSpawn(child))`, but it should share machinery with `finish()` rather than duplicate a second replay interpreter.

## Shape Of A Correct Implementation

The current `finish()` keeps `clock` and `pendCur` as local variables (`engine/wcprof/wcanalyze/replay.go:348-369`). That makes "replay parent to a prefix, stop, and later resume" awkward. A correct prefix-spawn patch should probably refactor the simulation into per-op replay state:

- state arrays for current action cursor, pending-child cursor, simulated clock, and phase (`unstarted`, `started`, `advancing`, `finished`);
- `advance(i, bound)` processes the same action stream up to either a spawn action/bound or the op end;
- `finish(i)` becomes `advance(i, end)`;
- `ensureSpawned(child)` becomes "ensure parent is started, then `advance(parent, childSpawnPoint)`, then require child started";
- `actSpawn` remains the only normal writer of a parented child's start;
- cycle detection applies to every recursive dependency edge encountered by `advance`, whether reached from an implicit join, an explicit wait join, or a prefix dependency.

This preserves the on-demand recursive replay design but removes the bad query conflation. The recursion itself is not the core bug. `first-write-wins` `setStart` (`engine/wcprof/wcanalyze/replay.go:299-304`) is acceptable only if the first writer is authoritative. It becomes unsafe when fallback/original-frame anchoring writes a parented child's start before the parent reaches the spawn. A correct state-machine version should make non-root starts single-authority: parent spawn or a verified equivalent prefix-to-spawn, not wait-target demand.

Performance should stay linear per simulation if each op's action cursor only advances forward. A naive helper that rescans the parent's prefix for every out-of-order reference could become superlinear on large traces; the stateful `advance` shape avoids that.

## Unified Cycle Handling

The seed hypothesis is close: the anchor path both overreaches and has different cycle semantics. But I would phrase the fundamental fix as "separate start scheduling from finish computation, then run all real dependency edges through one cycle policy."

Not every path to `finish()` is equivalent:

- explicit wait join is a real causal dependency;
- implicit join is the replay's synchronous-parent assumption;
- prefix-spawn is not a dependency on the child being finished, but prefix replay can encounter real dependencies before the spawn.

The common requirement is that when any real dependency edge reaches an in-flight op, the replay must handle it consistently. The current in-flight guard (`engine/wcprof/wcanalyze/replay.go:341-345`) is too blunt as the whole story: it returns original duration from the anchored start. That may be acceptable as the existing "recorded back-edge" breaker, but the policy should be made explicit and used for residual cycles after the anchor overreach is removed. If correct prefix-spawn still loops, the loop is no longer an artificial anchor cycle; it is a real impossible dependency cycle in the IR and should fail/report or be broken by the principled recorded-order rule, not hidden by original-frame starts.

## Is Original-Frame Anchoring Ever Correct?

Strongest case for Approach 1:

- The replay comment says the simulation runs in the original trace time frame because cross-tree wait targets may be reached before their own roots replay (`engine/wcprof/wcanalyze/replay.go:29-34`).
- If the target is a true independent root, or an externally scheduled async op whose start is not caused by a scaled parent prefix, preserving its original start/displacement is correct. Scaling work in an unrelated caller should not retroactively move an independent root earlier.
- If no factor affects any ancestor prefix before the child spawn, recorded offset and prefix-spawn happen to agree.

That case does not save Approach 1 for parented ops. The compiled replay already encodes parent-child starts as simulated spawn actions (`engine/wcprof/wcanalyze/replay.go:159-161`, `engine/wcprof/wcanalyze/replay.go:377-379`). A parented child is not externally scheduled in this model; its start is caused by its parent reaching the spawn point. The concrete config-parse example shows Approach 1 missing real cross-tree savings. Therefore original-frame anchoring is rigorously correct only for true roots/external starts, or as an accidental equality case. It is not correct as a general fallback for parented out-of-order targets.

The old comment should be narrowed. "Original frame" is a root/external scheduling rule, not a license to pin child starts when their parents' simulated prefixes changed.

## Tests Required

Before accepting the fix, I would require focused tests for:

- the exact `app`/`lib` cross-tree wait example: scaling `app`'s pre-spawn config parse moves `T` earlier and unblocks `W` earlier;
- the reported sibling singleflight cycle: old replay cycles, prefix-spawn replay does not, and the true wait edge remains present;
- a residual real prefix cycle: parent has a pre-spawn dependency that waits back on the child, and the unified cycle/back-edge mechanism reports/breaks it predictably;
- an independent-root cross-tree wait: original-frame/root scheduling is preserved where it is actually correct;
- performance shape: many out-of-order waits to children of the same parent do not repeatedly rescan the same prefix.

## Owner Decision

The owner should require the prefix-spawn/stateful-advance fix, not Approach 1. The implementation bar is:

- no recorded-offset fallback for parented child starts;
- start times computed by the same scaled prefix semantics as normal in-order replay;
- residual cycles handled as real data/replay cycles by one policy;
- root/external original-frame anchoring preserved only for true roots or explicitly detached starts;
- tests covering the concrete counterexample and the residual-cycle topology.

## Short Summary

Prefix-spawn terminate-or-not: it terminates for the normal sibling cross-tree singleflight case. It only cycles if the parent's prefix genuinely depends on something that depends back on the child; that is a real IR cycle, not an anchor artifact.

Fundamental core and cleanest fix: the bug is using `finish(parent)` to answer "when does child start?" The clean fix is a first-class start scheduler / stateful `advance` that replays the parent only to the child spawn under current factors, then uses one cycle policy for any real dependency encountered.

Original-frame anchoring: correct for true roots or externally scheduled starts, and coincidentally correct when the ancestor prefix is unchanged. It is not correct for parented child ops under scaled pre-spawn work.

Owner must decide: require the correct prefix-spawn/stateful replay refactor now, with no approximation path, and define the residual-cycle policy explicitly rather than hiding it behind recorded offsets.
