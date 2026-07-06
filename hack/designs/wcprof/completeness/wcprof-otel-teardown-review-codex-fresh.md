# wcprof OTel Teardown Final Count Review - Codex Fresh

Verdict: **SIGN OFF**. The teardown carrier closes both false-pass paths I flagged in the previous checksum review. I did not find a remaining silent-incompleteness path in the current producer/loader shape.

## Findings

No merge-blocking findings.

### Non-blocking hardening: fail `received > declared` too

The exact teardown contract says `received <= declared` is impossible to violate on faithful data (`engine/server/wcprofcount.go:34-45`). The loader/gate still only fail `declared > received` (`engine/wcprof/wcotel/loader.go:268-280`, `engine/wcprof/wcotel/gate.go:164-168`). Under the reviewed producer I did not find a reachable counted post-`Final` span, so this is not a blocker, but `received > declared` should probably be a hard invariant because it would mean the declaration was not actually final.

### Non-blocking reliability: Cloud carrier delivery is safe-fail but not tightly synchronized

The carrier is created during remove cleanup (`engine/server/session.go:462-471`, `engine/server/session.go:559-588`), while `serveShutdown` closes `client.shutdownCh` before the cleanup path runs (`engine/server/session.go:1719-1728`). The SSE trace subscriber exits after `client.shutdownCh` and one empty fetch (`engine/server/telemetry.go:629-655`). If that stream exits before the carrier row is inserted, Cloud misses the carrier and the trace fails marker-absent, which is safe but can refuse an otherwise complete trace. The reported live Cloud validation shows it works on exercised workloads; this is a reliability follow-up, not a wrong-ranking path.

## Verification

### Prior path 1: dropped declared leaf masked by post-marker extra

Closed. The count is now read once at teardown via `Final` (`engine/server/wcprofcount.go:85-94`) after query drain and service stop (`engine/server/session.go:439-460`), then carried on a dedicated `wcprof.session_complete` span (`engine/server/session.go:581-588`). The carrier is skipped by the counter (`engine/server/wcprofcount.go:64-78`), so post-query spans that are part of the counted population are included in the declared total instead of acting as surplus.

The loader reads the declaration before filtering the carrier (`engine/wcprof/wcotel/loader.go:261-294`). The new carrier test proves leaf loss still fails with clean orphan/wait signals (`engine/wcprof/wcotel/completeness_test.go:141-195`). I also re-ran the previous synthetic masked-leaf shape in carrier form: it now reports `declared=6 received=5 missing=1` and gate-fails.

### Prior path 2: final marker plus trailing closed subtree dropped

Closed. Per-query running markers are gone; `serveQuery` now only captures the first outer query's trace/root span IDs (`engine/server/session.go:1527-1540`). The final count is declared on the teardown carrier, not on a query root. If a trailing query subtree drops while the carrier survives, `received < declared`; if the carrier itself drops, marker-absent fail-by-default fires.

The exact trailing-query regression test models the whole closed-subtree drop and now fails with `missing=2` (`engine/wcprof/wcotel/completeness_test.go:197-234`). Carrier loss is covered separately (`engine/wcprof/wcotel/completeness_test.go:236-257`). My synthetic version of the old multi-query false-pass now reports `declared=5 received=2 missing=3` and gate-fails.

### Last-ness and drain reorder

The query drain move is safe and necessary: `serveQuery` rejects new work once `dagqlClosing` is set and tracks in-flight queries with `dagqlInFlight`/`dagqlCond` (`engine/server/session.go:1473-1487`), and `removeDaggerSession` waits before stamping and before telemetry shutdown (`engine/server/session.go:451-460`). This also avoids late-query telemetry being lost by provider shutdown.

After the stamp, the remaining cleanup paths I inspected do not obviously create counted per-client spans. The carrier itself is created with the main client's tracer provider before `ShutdownTelemetry` (`engine/server/session.go:581-588`, `engine/server/session.go:496-502`), and the batch processor shutdown flushes it through the normal exporter.

### Loader/replay boundary

The compile/replay model remains untouched. The loader only reads provenance, counts received marked engine spans, reads the declared count, and filters the carrier before op construction (`engine/wcprof/wcotel/loader.go:268-300`). No `wcanalyze` behavior changed.

## Tests Run

```text
go test ./engine/wcprof/wcotel
go test ./engine/wcprof/wccloud
go test ./engine/server -run '^$'
go test ./cmd/wcprof-otel-analyze
go test ./engine/server
```

All passed.

## Final

The two tail-drop residuals are closed. Marker-absent remains fail-by-default, the carrier is excluded from the graph, and the replay stays a rational function of the emitted data. I would land this with the two hardening notes above tracked separately.
