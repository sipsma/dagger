# wcprof OTel Teardown Final Count Review - Codex

Reviewed commit: `4074ad7867` on top of `501653ddcf` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`.

Verdict: SIGN OFF. No merge blockers found.

## Findings

### Low, non-blocking: loader still accepts non-carrier count markers

The current producer now emits the exact final declaration only on the teardown carrier, but the loader still treats any received `wcprof.session_span_count` attribute as a valid marker (`engine/wcprof/wcotel/loader.go:268-280`). A synthetic old-shape/root-marker trace still passes if `declared == received`.

I do not consider this a blocker because the production producer in this commit no longer emits per-query/root count markers, and the remaining root-marker use is test-fixture compatibility. If the desired contract is "only teardown carrier counts are valid," the follow-up is to make marker presence require `wcprof.session_complete=true` and update fixtures. Related small text cleanup: the absent-marker gate message still says the marker is "on the session root" (`engine/wcprof/wcotel/gate.go:164-165`), but the new production marker is on the carrier span.

## Confirmed

### My two residual leak paths are closed for the current producer

The old lower-bound path is gone. `serveQuery` now only records the trace/root IDs for the first outermost main-client query; it no longer stamps a running count at query return (`engine/server/session.go:1524-1536`). The exact count is declared once from teardown after service stop and DagQL drain: `removeDaggerSession` stops services, closes the resolver, waits for `dagqlInFlight == 0`, then calls `stampSessionComplete` and reaps the counter (`engine/server/session.go:439-471`).

`stampSessionComplete` reads `wcprofSpanCount.Final(traceID)`, creates a dedicated `wcprof.session_complete` carrier using the main client tracer provider, parents it to the recorded session-root span context, writes `wcprof.session_complete=true` plus the exact string count, and ends it before telemetry shutdown (`engine/server/session.go:548-589`). This makes the declaration independent of any query subtree, so dropping a whole trailing query no longer removes the max marker.

The padding path is also closed for the current producer. The declared value is read after query drain, not at a query boundary, and the carrier itself is excluded from both sides: `wcprofSpanCounter.OnStart` skips `wcprof.session_complete` by name before marking/counting (`engine/server/wcprofcount.go:64-78`), while the loader reads the count and then filters carrier spans out of the compiled op set (`engine/wcprof/wcotel/loader.go:268-294`). For current emitted traces, received marked engine spans should not exceed the final declared population; any dropped engine span makes `received < declared` and trips `MissingSpans`.

Manual checks matched the intended behavior:

```text
carrier survives, trailing query dropped: exit=1, missing-spans=2
carrier dropped: exit=1, marker=false fail-by-default
```

### Carrier handling keeps analysis unchanged

The carrier is a pure declaration messenger. It is not marked `wcprof.engine_span`, it is filtered before op-id assignment, and `TestCompletenessCarrierExactExcludedFromOps` asserts it does not leak into the graph (`engine/wcprof/wcotel/completeness_test.go:136-195`). Compile/replay behavior for real work is unchanged; the loader only adds provenance/gate data and removes the carrier from the op stream.

### Teardown ordering looks safe

Moving the DagQL drain ahead of telemetry shutdown is directionally safer for telemetry: no in-flight query can still be creating spans while its provider is shut down. The close path already stopped services and closed the resolver before the old later drain; this commit preserves that order and only moves the wait before release/telemetry shutdown (`engine/server/session.go:439-514`). New queries are rejected by `dagqlClosing`, and in-flight queries broadcast when drained (`engine/server/session.go:1458-1487`).

I did not find a current post-stamp cleanup path that creates marked per-client spans after the final declaration. The obvious container/buildkit release calls after the stamp are cleanup releases, not per-client tracer starts; the server package still compiles. The live validation in the commit message (`declared == received == 5312`) is the right empirical proof for this edge.

### Tests cover the closure

The new tests exercise the exact cases that mattered:

- Carrier exact count and carrier filtered from ops: `engine/wcprof/wcotel/completeness_test.go:136-195`.
- Whole trailing-query drop: `engine/wcprof/wcotel/completeness_test.go:197-234`.
- Carrier drop fail-by-default: `engine/wcprof/wcotel/completeness_test.go:236-257`.

## Tests Run

```text
go test ./engine/wcprof/wcotel ./engine/wcprof/wccloud ./cmd/wcprof-otel-analyze -count=1
go test ./engine/server -run TestNonExistent -count=1
go test ./engine/wcprof/wcotel -run 'TestCompleteness(CarrierExactExcludedFromOps|TrailingQueryDropCaught|CarrierDropFailsByDefault)$' -count=1 -v
```

All passed.
