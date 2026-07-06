# wcprof OTel Round 1 Review - Codex Fresh

Reviewed commit: `814df0173c` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`,
diffed against `4921d53662`.

Line numbers below refer to the checked-out `814df0173c` tree.

## Verdict

SIGN OFF. I found no merge-blocking issue in this light producer-completion round.

The `Services.Get` OTel wait closes the service-start native/OTel asymmetry, the slow-start fixture asserts the right headline behavior, and the `FallbackAnchors` -> `UnschedulableOps` change is behaviorally a rename plus the intended hard-fail posture. I found one stale comment in `report.go`; it is documentation noise, not a correctness blocker.

## Checks

### service.start symmetry

The new `Services.Get` branch mirrors the existing native wait:

- Native still starts a service wait with `wcprof.BeginWait(ctx, starting.profOpID, wcprof.WaitReasonService)` (`core/services.go:376`).
- The new OTel wait captures the same blocked interval and uses `starting.otelStartSpanCtx` with `WaitReasonService` on both cancel and normal completion paths (`core/services.go:383`, `core/services.go:387`, `core/services.go:391`).
- `EmitOTelWait` self-gates on the waiter span and intentionally emits an attributed targetless link when the target span context is invalid, so mixed/untraced starts remain gate-observable instead of silently dropped (`dagql/otelprof_hooks.go:103`).
- Invariant T holds: the `service.start` span is minted under `ss.l`, its span context is copied onto `startingService`, and only then is `ss.starting[key]` published (`core/services.go:1036`, `core/services.go:1042`, `core/services.go:1052`, `core/services.go:1055`). The stored field comment describes the same invariant (`core/services.go:61`).
- The prior `startWithKey` in-flight branch already emits the same OTel service wait shape (`core/services.go:1004`, `core/services.go:1011`, `core/services.go:1015`, `core/services.go:1020`). `rg WaitReasonService` found no other native service wait without an OTel analog; `Stop` only waits on `starting.done` without native wcprof wait, so it is not an asymmetry.

The new test covers the requested inverse case. `TestChunk4SlowServiceStartHeadlines` builds a slow `service.start`, checks the structural gate is clean (`engine/wcprof/wcotel/chunk4_test.go:370`), asserts `service.start` retains meaningful self-time (`engine/wcprof/wcotel/chunk4_test.go:382`), asserts it is the top bottleneck (`engine/wcprof/wcotel/chunk4_test.go:396`), and asserts both the idle daemon process and long-lived availability span save zero (`engine/wcprof/wcotel/chunk4_test.go:405`, `engine/wcprof/wcotel/chunk4_test.go:409`).

### Rename / gate cleanup

This is behavior-preserving where it touches the replay:

- `Simulation.FallbackAnchors` became `Simulation.UnschedulableOps`, and `FallbackAnchorOps` became `UnschedulableOpsSample` with the same meanings (`engine/wcprof/wcanalyze/replay.go:302`).
- The old `fallbackAnchor` helper is now `anchorUnschedulable`; the body still anchors at the recorded/parent-relative offset only to bound damage, increments the counter, and samples up to 10 ops (`engine/wcprof/wcanalyze/replay.go:571`).
- All prior call sites now call `anchorUnschedulable` in the same places: malformed child join, unschedulable parent, in-flight parent inversion, and parent prefix miss (`engine/wcprof/wcanalyze/replay.go:475`, `engine/wcprof/wcanalyze/replay.go:539`, `engine/wcprof/wcanalyze/replay.go:548`, `engine/wcprof/wcanalyze/replay.go:562`).
- The gate now hard-fails on any `UnschedulableOps > 0`, and `GateOptions` is intentionally empty (`engine/wcprof/wcotel/gate.go:21`, `engine/wcprof/wcotel/gate.go:80`, `engine/wcprof/wcotel/gate.go:142`).
- The CLI flag `-max-fallback-anchors` is removed and the analyzer passes `wcotel.GateOptions{}` (`cmd/wcprof-otel-analyze/main.go:31`, `cmd/wcprof-otel-analyze/main.go:64`).
- The cross-root gate test now asserts a faithful cross-root shape produces `UnschedulableOps == 0` and still passes (`engine/wcprof/wcotel/gate_test.go:262`, `engine/wcprof/wcotel/gate_test.go:287`).

I searched for stale exported symbols / flags. No `FallbackAnchors`, `FallbackBound`, `MaxFallbackAnchors`, `fallbackAnchor`, or `-max-fallback-anchors` references remain, except the historical mention in the `GateOptions` comment explaining the removed knob (`engine/wcprof/wcotel/gate.go:24`).

Minor non-blocker: `engine/wcprof/wcanalyze/report.go:172` still has lowercase stale wording: "fallback-anchored" and "fallback-anchors count" in a comment. The rendered output uses the new wording; this does not affect behavior.

### Losslessness / backstop

I did not independently rerun the two live captures. The claimed evidence is the right kind for this stage: post-skip complex traces with a clean structural gate, `OrphanedParents=0`, and no exporter/otlpdump drop signal. The gate is the safety boundary for incomplete data: orphaned parents, unresolved wait targets, dropped wait links, cycles, and unschedulable ops all fail loudly in `CheckStructural` (`engine/wcprof/wcotel/gate.go:126`, `engine/wcprof/wcotel/gate.go:132`, `engine/wcprof/wcotel/gate.go:139`, `engine/wcprof/wcotel/gate.go:142`).

There is one general limitation worth keeping in mind: a gate pass alone cannot prove that an arbitrary independent root span was not dropped with no children/waits left behind. For the measured producer-completion decision, the clean otlpdump/exporter measurement closes that practical gap. Given Erik's explicit leave-it-out decision, not adding a BSP/backpressure backstop here is safe for merge: future structural loss refuses to rank, and future non-structural exporter loss should be addressed with the already-noted bounded queue + dropped-span counter.

### Moot items

The publishResult parent stamping item is plausibly moot for this round: the skip fix removed the volume amplifier that dropped `call_exec` parents, and the reported post-skip captures have 0 parentless `publishResult` and 0 orphaned parents. No loader/replay accommodation is being added.

The nested-client sub-session edge is already represented in the graph builder: `nested_client` links map client IDs to their hosting exec op, and roots with that client ID are re-parented under the host exec (`engine/wcprof/wcanalyze/graph.go:270`, `engine/wcprof/wcanalyze/graph.go:281`, `engine/wcprof/wcanalyze/graph.go:290`). Nothing in this diff weakens that path.

## Tests Run

```sh
go test ./engine/wcprof/wcotel -run 'TestChunk4(SlowServiceStartHeadlines|ServicesFidelity)|TestStructuralGate|TestGate'
go test ./engine/wcprof/wcanalyze ./cmd/wcprof-otel-analyze
go test -count=1 ./core -run 'TestEmitServiceStartProducesLoaderShape'
```

All passed.

## Conclusion

No blocker remains. I would clean the stale `report.go` comment opportunistically, but I would not hold the merge for it.
