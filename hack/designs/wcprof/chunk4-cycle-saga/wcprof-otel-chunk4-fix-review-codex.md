# wcprof x OTel Chunk 4 cycle fix review

Reviewed inputs:

- `hack/designs/wcprof-otel-chunk4-cycle-findings.md`
- shared replay source in `engine/wcprof/wcanalyze/replay.go`
- landed lazy-triggered-exec test commit `8c331d8272`
- Chunk 4 service/source code at `4d6987fdc2`

Raw trace/native dump artifacts were not available in this worktree, so I could not independently inspect the exact captured cycle subgraph. I can still verify the replay mechanism and evaluate whether the proposed fix is mechanically sound.

## Overall verdict

The revised diagnosis is credible and fits the replay code better than the earlier OTel-emit theory: the current replay can manufacture a cycle while trying to anchor an out-of-order referenced op's start, because `finish(child)` calls `finish(parent)` and therefore replays the parent to full completion, not just to the child's spawn point.

I do **not** think the proposed `startOf = parent-start + recorded offset` implementation is correct enough to land as the fundamental fix. It breaks this cycle by avoiding parent replay entirely, but it also skips the parent's scaled pre-spawn timeline in counterfactual runs. That trades one unsoundness for another: out-of-order wait targets can start too late/early under what-if scaling, and later accurate spawn actions cannot correct that because `setStart` is first-write-wins.

The fundamental fix should still be in the shared replay, not OTel emit, but it should be a **prefix/spawn anchor**: replay the parent only until the referenced child's spawn point, honoring scaled self-time and earlier joins/waits, then stop. Do not replay the parent's full finish.

## REAL issues

### HIGH: The proposed `startOf` snippet is not counterfactually sound as written

The findings correctly identify the current over-reach:

- A child/target not yet started enters `finish(i)` (`engine/wcprof/wcanalyze/replay.go:306-319`).
- If its parent exists and is not in flight, replay calls `s.finish(parent)` to establish the child's start (`replay.go:313-323`).
- `finish(parent)` is a full replay: it processes all actions (`replay.go:371-388`) and finally `joinUpTo(parent.end)` (`replay.go:389`).
- `joinUpTo` recursively finishes children whose recorded end is `<= t` (`replay.go:353-367`). So a full parent replay can pull in concurrent sibling work that was not needed to determine the target child's start.

That mechanism can close exactly the described loop: a true wait from one concurrent subtree to a target in another subtree asks for the target's finish; anchoring that target by fully replaying its parent/ancestor pulls in the waiter subtree; the waiter then waits back on the target. The cycle is in replay anchoring, not necessarily in graph parentage.

But the proposed fix:

```go
s.setStart(i, s.startOf(parent)+(recorded child-start - recorded parent-start))
```

does not compute the same thing as the normal replay spawn path. In the normal in-order path, a child's simulated start is the parent's **current clock at the spawn action** (`actSpawn` sets `s.setStart(child, clock)` at `replay.go:377-379`). That `clock` has already applied scaled self segments (`replay.go:375-376`) and any earlier joins/waits. `RunWhatIfs` explicitly runs simulations with per-class factors (`replay.go:481-489`, `replay.go:543-550`), so pre-spawn parent work must shift child starts under counterfactuals.

The `startOf` snippet instead preserves the recorded offset from parent start. That is exact only for baseline-ish cases where the parent's pre-spawn timeline is unchanged. Under a counterfactual that speeds/slows parent work before the child spawn, the recorded offset is wrong. Worse, once `startOf` calls `setStart`, the later accurate `actSpawn` cannot fix it because `setStart` is no-op after the first start (`replay.go:299-303`).

Concrete failure shape:

- Root/parent `P` has 100ms of class `A` work before spawning child `T`.
- A concurrent root/sibling `W` waits on `T`, and root ordering reaches `W` first, so `T` is an out-of-order wait target.
- If class `A` is scaled to 0, `T` should start 100ms earlier and `W` should finish earlier.
- The proposed `startOf` still anchors `T` at `P.start + recorded 100ms`, so the what-if misses that saving.

This is not a cosmetic timing drift. It can change bottleneck rankings for exactly the cross-root/cross-sibling waits this cycle exposed. The owner ruling says practical impact is irrelevant; mechanically, this is a new unsoundness.

Required fix direction:

- Keep the shared replay fix.
- Replace "full parent finish" with "advance/replay parent to this child's spawn point."
- That prefix replay must process the parent's actions only up to the child's spawn time, including scaled self-time and any implicit joins/waits that occur before that point.
- It must not process actions after the child's spawn or the final `joinUpTo(parent.end)`, which is the current over-reach.

### MEDIUM: Zeroing `FallbackAnchors` loses a useful signal unless replaced

The current `FallbackAnchors` counter increments when an op is anchored without the parent organically reaching its spawn (`replay.go:224-231`, `replay.go:324-337`). The findings note the proposed fix drives fallback anchors to zero and breaks `TestGateFallbackAnchorsReportOnlyAndThreshold` (`hack/designs/wcprof-otel-chunk4-cycle-findings.md:30`, `hack/designs/wcprof-otel-chunk4-cycle-findings.md:36-39`).

I agree the old counter includes artifacts from the bad full-finish anchor. But out-of-order anchoring is still diagnostically relevant: it tells us the replay had to answer a cross-tree/cross-sibling dependency before the ordinary root/parent traversal reached that op. Even if a prefix-spawn anchor makes this exact and non-fatal, losing all visibility makes future validation weaker.

Recommendation: replace or rename the signal, e.g. `OutOfOrderStartAnchors` / `PrefixAnchors`, and sample those ops the way `FallbackAnchorOps` does today. The gate should probably stop treating this as a structural-failure candidate, but the report should keep the diagnostic.

### HIGH: `service.start` self-time erasure is real and needs a shared native+OTel fix

The new service finding is correct. Both sources currently place the long-lived daemon/availability work under `service.start`, and `SelfSegments` subtracts child intervals from parent self-time (`engine/wcprof/wcanalyze/graph.go:379-397`). That means a service start window can be erased by a daemon child that overlaps it.

Source evidence:

- `service.start` is begun on `svcCtx` before `svc.Start` (`core/services.go` at `4d6987fdc2:1020-1035`) and ended after `svc.Start` returns (`4d6987fdc2:1051-1092`).
- `svc.Start` starts the service exec span with that same context (`core/service.go` at `4d6987fdc2:748-755`).
- The daemon run uses that same context in `bk.Run` (`core/service.go` at `4d6987fdc2:833-856`).
- Native executor ops also follow the active context, so the native daemon `exec.run` is under `service.start` just like OTel.
- The Chunk 4 service fixture explicitly modeled an availability span child under `service.start` and a daemon below it (`engine/wcprof/wcotel/chunk4_test.go` at `4d6987fdc2:219-229`), then asserted only that the idle daemon does not rank (`4d6987fdc2:271-289`). It did not assert that a slow service start ranks as `service.start`.

The proposed direction, re-rooting the long-lived availability/daemon out of `service.start` in **both** native and OTel, is the right fundamental fix. `service.start` should represent the bounded start/health-check wait window; the daemon process can outlive that window and must not subtract the start window's self-time.

Implementation constraints for the eventual fix:

- Keep installer wait links targeting `service.start`.
- Preserve UI/error-origin cause links for the visible service span.
- Fix native and OTel together, or the oracle stops being meaningful.
- Add a fixture where slow service readiness, with an overlapping daemon process, headlines as `service.start`.

## NOISE / verified

### Cycle diagnosis: credible

The topology correction, "siblings/cross-referenced concurrent subtrees, not descendant false nesting," is consistent with the replay code. A sibling topology has no graph cycle by itself; the cycle appears when an out-of-order wait target forces replay to anchor by finishing a parent/ancestor, and that full finish recursively joins a concurrent subtree. The findings' "no-anchor SCC = 0" claim is exactly what I would expect if the graph edges are acyclic and the cycle is introduced by replay's start-ordering mechanism (`hack/designs/wcprof-otel-chunk4-cycle-findings.md:11-16`).

"Native cycles identically" is plausible and load-bearing. Native and OTel both feed the same replay; native call executions are also detached `call_exec` children with explicit waits, and the current replay's anchor code is source-agnostic. I would still ask the implementer to attach the extracted native cycle subgraph or the native `sim diagnostics` output to the PR, because the raw artifacts are not available here.

### Emit re-root rejection: sound

The findings say re-rooting every `call_exec` off the caller tree removes cycles but explodes fallback anchors 18 -> 3474 (`hack/designs/wcprof-otel-chunk4-cycle-findings.md:14`). That matches the model: removing parentage deprives replay of ordinary spawn anchoring, so many starts become out-of-order/fallback. It also would diverge from native unless applied system-wide, and it throws away useful synchronous parent structure.

I would not pursue OTel-only emit re-rooting. The cleaner fix is in shared replay, but not the exact `recorded-offset startOf` snippet.

### Design premise change: acceptable, but document it

The approved design's premise was "reuse replay unchanged." The new evidence, if confirmed by native output, invalidates that premise. Changing the shared native replay is aligned with the owner's ruling because this is not an OTel accommodation; it is a shared analyzer correctness fix. The design/plan should be updated to say "reuse the corrected shared replay" and should add replay-level tests for the concurrent cross-reference cycle.

### Landed lazy-triggered-exec test: good coverage

The `8c331d8272` test is well targeted. It verifies that an exec started during lazy evaluation is a direct re-pointed child stamped with `wcprof.parent`, while its phase descendants are not over-stamped, and the loader re-homes the exec subtree under the lazy op with `work_type=user` surviving (`dagql/otelprof_lazy_exec_test.go` at `8c331d8272:63-139`). This does not address the replay anchor cycle, but it closes a real composition risk between Chunks 3 and 4.

## Required validation before landing a replay fix

I would require:

- A minimal replay unit test that reproduces the sibling/cross-reference cycle under the current full-parent anchor and passes under the prefix-spawn anchor.
- A counterfactual unit test for the pre-spawn scaling case above, proving the fix does not use recorded offsets when parent pre-spawn work is scaled.
- The native captured output or extracted subgraph demonstrating the identical native cycle.
- Existing `wcanalyze` tests, Chunk 2/3/4 oracle fixtures, and the lazy-triggered-exec composition test.
- A service-start fixture proving slow readiness remains `service.start` self-time even with an overlapping long-lived daemon.

## Plain answers

- Is the cycle correctly diagnosed? Mostly yes. The replay anchor mechanism is a real, code-confirmed over-reach, and the sibling/native findings are plausible. I want the extracted native/cycle subgraph attached for final proof.
- Is the proposed fix correct/fundamental/side-effect-free? No, not as written. Shared replay is the right layer, but `parent-start + recorded offset` is not counterfactually sound.
- Better alternative? Yes: replay/advance the parent only to the child's spawn point, preserving scaled pre-spawn work and earlier dependencies, then stop before full parent completion.
- Service.start erasure? Real. Re-root the long-lived daemon/availability out of `service.start` in both native and OTel and add a slow-start fixture.
