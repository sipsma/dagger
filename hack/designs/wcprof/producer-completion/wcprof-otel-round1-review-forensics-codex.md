# wcprof OTel Round 1 Review - Codex

Reviewed commit: `814df0173c` on top of `4921d53662` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`.

Verdict: SIGN OFF. I found no merge blocker.

## Findings

### Low, non-blocking: a couple of comments still use old fallback wording

The rename is complete at the API/behavior level, but not literally complete in prose comments:

- `engine/wcprof/wcanalyze/report.go:172-175` now says "unschedulable-op anchor" on line 172, but still describes "fallback-anchored" classes and a "fallback-anchors count" on lines 174-175.
- `dagql/otelprof_lazy.go:16-23` still says "cycle / fallback-anchor" in a design comment.

This is not a correctness issue. The removed symbols/flags are gone, the gate/report behavior uses `UnschedulableOps`, and the only other `MaxFallbackAnchors` mention I found is the intentional historical comment in `engine/wcprof/wcotel/gate.go:24`.

## Service.start symmetry

The new `Services.Get` wait emission correctly mirrors the existing `startWithKey` installer wait. The native wait still starts at `core/services.go:376`; the new OTel wait timestamps start at `core/services.go:383` and emit on both cancellation and completion at `core/services.go:387` and `core/services.go:391`, targeting `starting.otelStartSpanCtx` with `WaitReasonService`.

The existing `startWithKey` path already has the same native plus OTel shape at `core/services.go:1004-1020`. The service-start span context is stashed before publishing `ss.starting[key]`: `beginOTelServiceStart` is called while holding `ss.l` at `core/services.go:1036-1044`, the span context is copied into `startingService` at `core/services.go:1052-1054`, and the starting record is published at `core/services.go:1055`.

The targetless/mixed-recording behavior is preserved by `dagql.EmitOTelWait`: it self-gates on the waiter span at `dagql/otelprof_hooks.go:103-110`, but intentionally still emits attributed wait links with an invalid target at `dagql/otelprof_hooks.go:111-134`, making the gate fail loud rather than silently losing a wait.

I found no remaining native-only service wait path. The only `WaitReasonService` waits are the two expected service-start join paths.

The new test covers the requested outcome. `TestChunk4SlowServiceStartHeadlines` builds the slow-start scenario at `engine/wcprof/wcotel/chunk4_test.go:336-363`, requires a clean structural gate at `chunk4_test.go:370-375`, asserts `service.start` retained substantial self-time at `chunk4_test.go:377-385`, asserts it ranks first at `chunk4_test.go:388-400`, and asserts the idle daemon plus long-lived availability span do not rank at `chunk4_test.go:402-412`.

## UnschedulableOps rename

This is behavior-identical to the prior `FallbackAnchors` gate, apart from removing the tolerance knob and hard-failing any unschedulable op.

The simulation fields/methods are renamed at `engine/wcprof/wcanalyze/replay.go:302-313`, and the anchoring behavior remains the same in `anchorUnschedulable` at `engine/wcprof/wcanalyze/replay.go:565-579`: increment the count and record a bounded sample.

The structural gate now has an empty `GateOptions` at `engine/wcprof/wcotel/gate.go:21-26`, records `UnschedulableOps` at `gate.go:61-67` and `gate.go:98-106`, and hard-fails `UnschedulableOps > 0` at `gate.go:142-144`. CLI wiring dropped `-max-fallback-anchors` and passes `wcotel.GateOptions{}` at `cmd/wcprof-otel-analyze/main.go:29-64`.

I found no stale code references to the old names and no remaining callers depending on the removed knob. Tests passed:

```text
go test ./core ./engine/wcprof/wcotel ./engine/wcprof/wcanalyze ./cmd/wcprof-otel-analyze -count=1
```

## Losslessness

I did not rerun the two live capture workloads in this light review. The stated measurement is the right evidence shape for the decision: complete post-skip captures that pass the structural gate, with `OrphanedParents=0`, no unresolved waits, and no dropped OTLP batches. Since the gate now hard-fails any missing parent, unresolved wait target, cycle, interval/span impossibility, or unschedulable op, leaving out the BSP backstop is acceptable for this merge: a future incomplete trace is refused, not analyzed with inference.

## Moot Items

The parentless `publishResult` item is moot for the producer-completion scope if the post-skip captures truly have zero parentless `publishResult` spans and the structural gate passes with `OrphanedParents=0`. There is no analysis-side heuristic added.

The nested-client sub-session edge is already handled in the analyzer graph construction: nested-client links are collected and root ops in the linked client are reparented under the host exec at `engine/wcprof/wcanalyze/graph.go:270-294`. I do not see a Round 1 gap there.
