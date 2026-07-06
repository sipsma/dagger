# wcprof OTel Teardown-Final-Count Review

Reviewed commit `4074ad7867` against `501653ddcf` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`.

Verdict: **SIGN OFF.** I found no merge-blocking issues. The one exact teardown declaration closes both prior tail-drop blockers: a dropped counted leaf can no longer be offset by later spans, and losing a trailing query no longer downgrades the declaration to an earlier per-query marker. The graph/replay remain untouched; the carrier is read only for the completeness checksum and then removed before op compilation.

## Real Issues

None.

## Verification

### Blocker 1: offsetting a dropped counted leaf

**Resolved.** The previous `received >= declared` hole depended on the declared value being a running floor. This patch declares once after the session has quiesced:

- `removeDaggerSession` stops session services before the declaration (`engine/server/session.go:439-442`), then closes/drains dagql by setting `dagqlClosing` and waiting for `dagqlInFlight == 0` (`engine/server/session.go:451-460`).
- The exact count is read and stamped after that drain (`engine/server/session.go:462-471`), before per-client telemetry shutdown (`engine/server/session.go:496-501`).
- The counter's `Final` is a non-mutating read of the per-trace total (`engine/server/wcprofcount.go:85-94`), and `Reap` happens only after the carrier span is created (`engine/server/session.go:470-471`).
- The carrier is excluded from its own total by name in `OnStart`, before marking/counting (`engine/server/wcprofcount.go:64-78`).
- The loader counts distinct received `wcprof.engine_span` spans, reads the declared total, and records `MissingSpans` when declared exceeds received (`engine/wcprof/wcotel/loader.go:261-280`).

This makes the live producer's invariant `received <= declared` credible. I specifically checked the post-stamp release path: container/directory/file release paths release snapshot refs rather than starting dagql/per-client spans (`core/container.go:1729-1758`, `core/directory.go:69-78`, `core/file.go:62-70`; snapshot releases are ref cleanup at `engine/snapshots/refs.go:651-659` and `engine/snapshots/refs.go:822-830`). The complete-capture validation reporting exact reconciliation is consistent with the source shape.

The new carrier-form test directly covers the originally silent leaf case: the complete trace reconciles `declared=received=5`, the carrier is excluded from compiled ops, and dropping `exec.processRun` produces `MissingSpans=1` while orphan/wait-target signals stay clean (`engine/wcprof/wcotel/completeness_test.go:136-195`).

### Blocker 2: losing the final declaration

**Resolved.** The declared count no longer rides on a query root. `serveQuery` now only records the first outermost query's trace/root IDs (`engine/server/session.go:1527-1540`); the count itself is stamped once on a dedicated teardown carrier (`engine/server/session.go:549-588`).

That carrier is parented into the trace using the captured trace ID/root span ID (`engine/server/session.go:573-580`), ended immediately (`engine/server/session.go:581-588`), and created before provider shutdown (`engine/server/session.go:496-501`). The loader reads its count before filtering it out of the graph (`engine/wcprof/wcotel/loader.go:268-294`). If the carrier itself is dropped, `SessionMarkerPresent` remains false and the gate fails by default (`engine/wcprof/wcotel/gate.go:160-168`), covered by `TestCompletenessCarrierDropFailsByDefault` (`engine/wcprof/wcotel/completeness_test.go:236-257`).

The trailing-query regression is also covered directly: dropping an entire trailing query subtree leaves reference signals clean but now fails with `MissingSpans=2` because the surviving carrier declares the exact final total (`engine/wcprof/wcotel/completeness_test.go:197-234`).

### Loader / Replay Scope

The carrier is a loader-side completeness input only. It is counted before graph construction and then filtered from `deduped` before deterministic op assignment (`engine/wcprof/wcotel/loader.go:283-300`). There is no `wcanalyze`/replay change in this diff, and no new causal inference path.

### Edge Cases

- **Root marker / carrier drop:** covered by absent-marker fail-by-default (`engine/wcprof/wcotel/completeness_test.go:236-257`).
- **Multiple queries:** covered by the trailing-query fixture (`engine/wcprof/wcotel/completeness_test.go:197-234`), and source-side query admission/drain is guarded by `dagqlClosing`/`dagqlInFlight` (`engine/server/session.go:1473-1487`, `engine/server/session.go:451-460`).
- **Carrier self-count:** prevented by the `wcprof.session_complete` span-name skip in `OnStart` (`engine/server/wcprofcount.go:69-73`) and by the loader filter (`engine/wcprof/wcotel/loader.go:287-294`).
- **Post-teardown residual:** I did not find a counted per-client span path after `stampSessionComplete`; telemetry shutdown happens after the carrier, and later cache release is after telemetry shutdown (`engine/server/session.go:496-521`).

## Noise / Cleanup

Low-severity wording drift only: some loader/gate/provider comments still say the count is on the "session-root span" or refer to the old running-total/max scheme (`engine/wcprof/wcotel/loader.go:120-125`, `engine/wcprof/wcotel/gate.go:164-165`, `engine/server/session.go:778-784`). The behavior is correct, but those messages should eventually say "teardown carrier" to avoid misleading future debugging.

## Tests Run

```text
go test ./engine/wcprof/wcotel ./engine/server -run 'TestCompleteness|Test.*Session|Test.*Wcprof|Test.*Telemetry' -count=1
go test ./engine/wcprof/wcotel ./engine/wcprof/wccloud ./cmd/wcprof-otel-analyze ./engine/server -count=1
```

Both passed.
