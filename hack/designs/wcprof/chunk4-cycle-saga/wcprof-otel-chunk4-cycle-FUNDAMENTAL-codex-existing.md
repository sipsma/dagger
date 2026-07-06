# Chunk 4 cycle: prefix-spawn stress review

Scope: stress-test Erik's chosen direction, not defend it. I verified against the current worktree's `engine/wcprof/wcanalyze/replay.go`.

## Bottom line

Prefix-spawn is the right semantic direction: to start an out-of-order target child, replay the parent only to that child's spawn point, with scaled self-time and earlier dependencies applied. It fixes the specific unsoundness in the current anchor path, which calls `finish(parent)` and therefore replays the parent to full completion just to get a child start (`engine/wcprof/wcanalyze/replay.go:313-323`, `:371-389`).

The implementer-reviewer rebuttal, "it still cycles if the parent joins a cross-referencer before the child's spawn," is not valid for the reported app/lib shape. If `W` truly waits on `T`, then replay classifies that as `actWaitJoin` only when `W.waitEnd >= T.end - epsilon` (`replay.go:162-173`). For a normal positive-duration `T`, `W` ends after `T` starts. A prefix replay that stops at `T.start` will not join `W` via `joinUpTo`, because `joinUpTo(cutoff)` only joins children whose recorded end is `<= cutoff` (`replay.go:353-367`).

But prefix-spawn must be implemented as a real replay prefix, not a helper that computes offsets. The safe implementation is a unified replay state machine: `advanceTo(op, cutoff)` for parent prefixes and `advanceTo(op, end)` for finishes, with one cycle/back-edge mechanism for anchor, implicit-join, and explicit-wait paths.

## Replay facts

The current replay has three dependency entry paths:

- Anchor/start path: if `finish(i)` is asked to finish an unstarted op, it calls `finish(parent)` when the parent exists and is not in flight (`replay.go:313-323`).
- Implicit join path: parent progress calls `joinUpTo(t)`, which calls `finish(child)` for children with `child.end <= t` (`replay.go:353-367`).
- Explicit wait-join path: an `actWaitJoin` calls `finish(waitTarget)` (`replay.go:379-381`).

The current anchor path is the bug. It needs the parent's clock at one spawn point, but it obtains that by finishing the parent completely, including all later actions and the final `joinUpTo(parent.end)` (`replay.go:371-389`). That can pull in a concurrent cross-referencer that waits back on the target.

The current cycle guard is global but late: it detects `finish(i)` while `i` is already `inFlight` (`replay.go:341-345`). The anchor call to `finish(parent)` happens before the target op is marked `inFlight`, so anchor-start recursion is not handled as cleanly as ordinary wait/implicit-join recursion.

## Does prefix-spawn terminate?

### For the app/lib topology: yes

Concrete shape:

- `POST /query` starts siblings `app` and `lib`.
- `app` does config parse, then spawns `T = load foo` at recorded t=100.
- `lib` has `W`, and `W` waits on `T`.

To answer `finish(T)` out of order, prefix-spawn does:

1. Ensure `app` has a simulated start.
2. Advance `app` only until the `actSpawn(T)` point.
3. Set `T`'s simulated start from the `app` clock at that spawn.
4. Stop. Do not process `app` actions after `T`'s spawn. Do not run `joinUpTo(app.end)`.

The cross-referencer `W` cannot be joined by this prefix through normal implicit joins. Since `W` waits for `T`, `W`'s wait-join requires `W.end >= T.end - 1ms` (`replay.go:162-173`). For the concrete example `T=[100,300]`, `W.end` is near 300, not `<= T.start=100`. `joinUpTo(100)` therefore will not include `W` (`replay.go:353-367`). The rebuttal confuses "the full parent eventually joins W" with "the prefix before T's spawn joins W." The former is current over-reach; the latter is false for this topology.

Under the what-if config-parse -> 0, prefix-spawn also gives the right answer: the parent clock at `actSpawn(T)` shifts from 100 to 0 because scaled self-time is applied before the spawn (`replay.go:375-379`). Approach 1 missed that by pinning the recorded offset.

### Can prefix-spawn still re-enter an in-flight op?

Yes, but only under stronger topologies than the rebuttal stated. Those are real cycles or boundary degeneracies, not the reported module-source shape.

Examples:

- Parent-prefix explicit wait: before spawning `T`, parent `P` has an explicit wait-join on `W`; `W` waits on `T`. To reach `T`'s spawn, replay must finish `W`; to finish `W`, replay must finish `T`; to start `T`, replay must advance `P` past the wait on `W`. That is a genuine circular dependency in the recorded constraints.
- Earlier-child implicit join: before spawning `T`, `P` joins a child `A` whose own finish waits on `T`. For a normal `T` ending after spawn, `A.end <= T.start` and `A waits on T` are incompatible. But zero-duration or sub-epsilon targets can make the current `joinEpsilonNS = 1ms` classify a near-boundary wait as a join (`replay.go:41`, `:162-173`). That should be treated as a real residual cycle or a classification-boundary case to test explicitly.
- Ancestor chain cycle: starting `T` requires advancing ancestor `P`; prefix dependencies of `P` wait back on `T` or a descendant of `T`.

So the correct conclusion is: prefix-spawn terminates for faithful acyclic data like the app/lib case, and if a correct prefix still loops, Erik's ruling is right - that loop is a real recorded constraint cycle to detect and resolve, not a reason to return to recorded-offset anchoring.

## Residual hazards

### 1. Prefix must replay all pre-spawn constraints

A correct prefix cannot just scale self segments. It must process the same ordering rules as normal replay up to the target spawn:

- scaled self segments (`replay.go:375-376`)
- earlier child spawn actions (`replay.go:377-379`)
- implicit joins due before the cutoff (`replay.go:353-367`)
- explicit waits before the cutoff (`replay.go:379-386`)

If a parent genuinely waits before spawning `T`, then `T`'s simulated start depends on that wait. Skipping those dependencies would be another wrong counterfactual.

### 2. Same-time semantics need to match the compiled action order

Actions are sorted by original time, and at the same time `self < spawn < wait` (`replay.go:144-185`). A prefix to `T`'s spawn must match that order: run `joinUpTo(T.start)`, process any same-time actions that precede `T`'s specific spawn if they can affect clock, then set `T`'s start exactly as normal `actSpawn` would. In practice same-time spawn actions do not advance the clock, but relying on that informally is unnecessary risk.

### 3. `setStart` first-write-wins becomes sharper

`setStart` ignores later writes once an op is started (`replay.go:299-303`). That is fine only if prefix anchoring and later ordinary spawn anchoring compute the same value.

This argues for one of two implementation disciplines:

- The prefix path uses the exact same `advanceTo` state as full replay, so ordinary replay later continues from that state rather than recomputing.
- Or, if recomputation is used, `setStart` should assert/log when a second computed start differs from the first. Silent disagreement would be a correctness bug.

Approach 1 failed exactly here: it wrote an approximate start early, and the later accurate spawn could not correct it.

### 4. Naive prefix replay can go super-linear

If every out-of-order wait target under a large parent replays the parent from its start to that target's spawn, worst-case cost becomes roughly O(number of targets x parent timeline). A wide parent with many cross-referenced children could regress badly.

The implementation should memoize per simulation, not globally, because factors differ per simulation (`RunWhatIfs` creates one `Simulation` per factor/class at `replay.go:481-489`, `:543-550`). A robust shape is:

- per-op replay cursor: current action index, pending-child cursor, current simulated clock
- `advanceTo(op, cutoffOriginalTime)` is monotonic
- `finish(op)` is `advanceTo(op, op.end)` plus finalization
- if a later call asks for an earlier cutoff, it returns the already-known clock/start state

That is more invasive than a small `startOf` helper, but it is what makes prefix-spawn both accurate and scalable.

### 5. Root scheduling must be explicit

The replay currently starts roots in `Run`, preserving idle gaps for strictly sequential roots and displacement for overlapping roots (`replay.go:275-295`). The file comment says cross-tree targets are anchored in the original trace frame when their own root has not replayed yet (`replay.go:29-34`).

If "original-frame anchoring" can produce knowingly wrong counterfactuals, that rationale has to be revised. A prefix-spawn implementation must define how an out-of-order target whose ancestor is an unstarted root gets that root's simulated start:

- If roots are independent concurrently submitted queries, recorded displacement may be right.
- If roots are sequential CLI phases whose later start should inherit earlier-root savings, the root start must be computed through the same root-chaining logic as `Run`.

This may not affect the reported single `POST /query` app/lib cycle, but it is part of the same anchor correctness surface.

### 6. Prefix needs its own in-progress state

The current `inFlight` is for `finish`, not for "I am advancing this parent prefix to spawn child T." A unified implementation needs states that distinguish:

- not started
- start/prefix advancing
- started but not finished
- finishing
- finished

Without that, a residual real cycle in a prefix can either recurse forever or fall into a fallback path that hides the true cycle. The owner explicitly ruled that a correct prefix loop is a real data cycle; the code should make that visible.

## Is there a more fundamental fix?

Yes: prefix-spawn is the correct anchor semantics, but the more fundamental implementation is a unified advance engine.

Current `finish()` mixes two separate jobs:

- establish an op's start by asking the parent to reach the spawn
- finish the op

Because the only primitive is `finish(parent)`, the start job over-reaches. The fundamental split should be:

- `ensureStarted(i)`: if parented, `advanceTo(parent, startNS[i], stopAtSpawn=i)`; if root, compute root start through the root schedule.
- `advanceTo(i, cutoff)`: progress op `i` through actions and due implicit joins up to cutoff, with scaled self-time.
- `finish(i)`: `ensureStarted(i)` then `advanceTo(i, endNS[i])`, then mark finished.

Then all three dependency types use the same machinery:

- anchor dependency: `ensureStarted(child)` uses `advanceTo(parent, child.start)`
- implicit join dependency: `advanceTo(parent, t)` calls `finish(child)` for due children
- explicit wait dependency: wait action calls `finish(target)`

Cycle/back-edge detection should sit underneath all of those calls, not just happen after the current anchor block. In current local code, the `inFlight` guard is line `341`, after the parent-anchor block (`replay.go:313-339`). That ordering is the smell.

This unified version is meaningfully better than a minimal prefix patch because it also addresses performance, first-write consistency, root-start semantics, and residual-cycle reporting. Prefix-spawn is not a mere special case; it is the required semantics of the anchor edge inside the unified engine.

## Tests I would require

1. App/lib counterfactual test:
   - `app` has 100ms pre-spawn self then spawns `T`.
   - `lib/W` waits on `T`.
   - Scaling app pre-spawn class to 0 starts `T` at 0 and unblocks `W` 100ms earlier.

2. Current-cycle regression:
   - Minimal sibling/cross-reference shape that cycles under full-parent anchor.
   - Prefix-spawn removes the cycle without changing true wait propagation.

3. Rebuttal topology test:
   - Parent has a post-spawn cross-referencer that waits on `T`.
   - Prefix to `T.start` must not join it.

4. Real residual cycle test:
   - Parent has a pre-spawn wait on `W`; `W` waits on `T`.
   - Prefix detects/reports a real cycle instead of falling back to recorded offsets.

5. Same-time/epsilon boundary test:
   - Sub-millisecond/zero-duration `T` where join epsilon could classify an end-at-cutoff wait as a join.
   - Decide and lock down the intended behavior.

6. Root out-of-order target test:
   - Later root is targeted before `Run` reaches it.
   - Verify the root start follows the chosen root scheduling semantics.

7. Performance guard:
   - Many out-of-order waits to children under one large parent.
   - Assert no repeated full prefix scans blow up.

## Owner decisions

1. Approve a shared replay refactor, not a local OTel emit change and not recorded-offset anchoring.
2. Decide root scheduling semantics for out-of-order cross-root targets; update the `replay.go:29-34` rationale accordingly.
3. Keep a diagnostic for out-of-order prefix anchors. The old `FallbackAnchors` signal is contaminated by the bad fallback behavior (`replay.go:224-231`, `:324-337`), but a replacement like `OutOfOrderStartAnchors` is still useful.
4. Decide how residual real cycles are surfaced: hard gate/report with extracted cycle chain, not silent "original duration" unless that is explicitly the existing back-edge policy for known impossible recorded constraints.

## Plain answer

Prefix-spawn terminates for the reported app/lib shape. The rebuttal fails because a true `W waits on T` edge means `W` ends at/near `T.end`, and a prefix replay stopping at `T.start` will not join `W` via `joinUpTo`.

Prefix-spawn can still encounter real residual cycles if the parent's pre-spawn prefix genuinely depends on work that waits back on the not-yet-spawned child, or in zero/sub-epsilon boundary cases. Those should be diagnosed as real cycles.

The more fundamental fix is warranted: implement prefix-spawn as part of a unified `advanceTo` replay engine with one cycle mechanism for anchor, implicit join, and explicit wait dependencies.
