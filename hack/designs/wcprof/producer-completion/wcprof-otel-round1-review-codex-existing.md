# wcprof OTel round-1 producer-completion review - Codex

Reviewed commit `814df0173c` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`
against `4921d53662`.

Targeted tests run:

```sh
go test ./engine/wcprof/wcanalyze ./engine/wcprof/wcotel -run 'TestChunk4SlowServiceStartHeadlines|TestChunk4ServicesFidelity|TestGate|Test.*Replay|Test.*Config|Test.*Cycle|TestRootsIndependent'
go test ./core -run 'TestEmitServiceStartProducesLoaderShape'
```

Result: pass.

## Verdict

Sign off. I found no merge-blocking issue.

The `service.start` symmetry fix is correct, the replay/gate rename is code/API
complete and behavior-preserving, and the losslessness posture remains consistent
with the principle: no analysis compensation, gate refuses incomplete/unfaithful
data.

## Verification

### service.start wait symmetry

The new `Services.Get` `isStarting` branch now mirrors the existing
`startWithKey` `isStarting` branch:

- Native wait was already present:
  [core/services.go:376](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:376).
- OTel wait now records the same interval with `WaitReasonService`, on both
  cancellation and completion:
  [core/services.go:383](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:383),
  [core/services.go:387](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:387), and
  [core/services.go:391](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:391).
- `startWithKey` has the same native + OTel wait shape:
  [core/services.go:1004](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:1004),
  [core/services.go:1011](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:1011),
  [core/services.go:1015](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:1015), and
  [core/services.go:1020](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:1020).
- The wait target is Invariant-T safe: the service-start span context is minted
  under `ss.l` and stored before `ss.starting[key]` is published:
  [core/services.go:1036](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:1036),
  [core/services.go:1042](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:1042),
  [core/services.go:1052](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:1052), and
  [core/services.go:1055](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/services.go:1055).
- If the start ran untraced, `starting.otelStartSpanCtx` remains invalid and
  `dagql.EmitOTelWait` intentionally emits a targetless wait on a recording
  waiter so the structural gate fails loud:
  [dagql/otelprof_hooks.go:103](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/otelprof_hooks.go:103).

I do not see another `WaitReasonService` native/OTel asymmetry: the two service
wait branches are `Services.Get` and `startWithKey`, and both now emit OTel waits.

The new slow-start fixture checks the intended product behavior:

- service wait resolves and the gate is clean:
  [engine/wcprof/wcotel/chunk4_test.go:370](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/chunk4_test.go:370).
- `service.start` retains self-time instead of being erased by the long-lived
  availability span:
  [engine/wcprof/wcotel/chunk4_test.go:382](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/chunk4_test.go:382).
- the top bottleneck is `{service_start,service.start}`:
  [engine/wcprof/wcotel/chunk4_test.go:388](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/chunk4_test.go:388).
- the idle daemon and long-lived availability span do not rank:
  [engine/wcprof/wcotel/chunk4_test.go:402](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/chunk4_test.go:402).

### Rename / gate cleanup

The replay behavior is unchanged. The diff renames `FallbackAnchors` to
`UnschedulableOps`, `FallbackAnchorOps` to `UnschedulableOpsSample`, and
`fallbackAnchor` to `anchorUnschedulable`; the anchor calculation and counter
behavior are otherwise identical:

- [engine/wcprof/wcanalyze/replay.go:302](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcanalyze/replay.go:302)
- [engine/wcprof/wcanalyze/replay.go:571](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcanalyze/replay.go:571)
- [engine/wcprof/wcanalyze/replay.go:576](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcanalyze/replay.go:576)

The gate now has no tolerance knob and hard-fails on any unschedulable op:

- `GateOptions` is empty:
  [engine/wcprof/wcotel/gate.go:21](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/gate.go:21).
- `CheckStructural` copies `sim.UnschedulableOps` and fails when it is non-zero:
  [engine/wcprof/wcotel/gate.go:104](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/gate.go:104) and
  [engine/wcprof/wcotel/gate.go:142](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/gate.go:142).
- the `-max-fallback-anchors` CLI flag is removed and callers use
  `wcotel.GateOptions{}`:
  [cmd/wcprof-otel-analyze/main.go:31](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/cmd/wcprof-otel-analyze/main.go:31) and
  [cmd/wcprof-otel-analyze/main.go:64](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/cmd/wcprof-otel-analyze/main.go:64).

I found no stale code references to `FallbackAnchors`, `FallbackAnchorOps`,
`FallbackBound`, or `MaxFallbackAnchors`. There are two stale prose comments:

- [engine/wcprof/wcanalyze/report.go:174](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcanalyze/report.go:174) still says `fallback-anchored` / `fallback-anchors`.
- [dagql/otelprof_lazy.go:23](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/otelprof_lazy.go:23) still says `fallback-anchor`.

These are wording-only and not blockers, but the rename is not literally complete
until those comments are updated.

### Losslessness / deferred backstop

I did not independently rerun the two complex live captures, but the stated
evidence is the right merge-gate signal for this design: post-skip complete
captures with structural gate PASS, zero orphaned parents, and no dropped batches.
The gate now fails hard on orphaned parents, unresolved targets, dropped wait links,
cycles, and unschedulable ops, so leaving the BSP backstop out is safe in the
principled sense: incomplete data is refused rather than compensated for.

### Moot items

The publishResult parentlessness item is moot for the current data if the
post-skip structural gate reports zero orphaned parents. The gate still catches a
regression if parent spans disappear again:
[engine/wcprof/wcotel/gate.go:132](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/gate.go:132).

The nested-client sub-session edge is already represented in the shared native
graph builder: nested-client links reparent roots under the hosting exec op at
[engine/wcprof/wcanalyze/graph.go:270](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcanalyze/graph.go:270).

## Non-blocking notes

- There is no direct unit test that drives the new `Services.Get` `isStarting`
  branch itself. The code is simple and mirrors `startWithKey`, and the emitted
  shape is covered by the chunk4 loader fixture plus the core emit helper test, so
  I do not consider this a blocker.
- Clean up the stale `fallback-anchor` prose comments when convenient.

## Remaining blockers

None.
