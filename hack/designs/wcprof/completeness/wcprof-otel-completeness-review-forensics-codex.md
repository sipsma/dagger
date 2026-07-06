# wcprof OTel Completeness Checksum Review - Codex

Reviewed commit: `501653ddcf` on top of `9555281f27` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`.

Verdict: NOT SIGNED OFF. One blocker remains.

## Findings

### Blocker: the checksum is a max of surviving running markers, not a final trace checksum

The patch closes the simple case where the trace has one surviving session-count marker and one later leaf span is dropped: the loader sees `declared > received` and hard-fails. It does not close the general silent-incompleteness hole for the real shape the commit explicitly calls out: one trace with many main-client queries.

Producer behavior:

- `serveQuery` stamps each outermost main-client `POST /query` span at query return, not at session/trace completion (`engine/server/session.go:1466-1482`).
- `wcprofSpanCounter.Stamp` writes the current running total from the shared per-trace map (`engine/server/wcprofcount.go:78-94`).
- The implementation documents that a command issues many main-client queries under one trace and that the loader should keep the max such marker (`engine/server/wcprofcount.go:30-36`).

Loader behavior:

- The loader counts received marked engine spans and takes the maximum `wcprof.session_span_count` value among the spans it received (`engine/wcprof/wcotel/loader.go:269-280`).
- The gate hard-fails only when no marker is present or when `declared > received` (`engine/wcprof/wcotel/gate.go:164-167`).

That means a loss that drops the final query subtree together with the final/largest marker can still pass. The loader will see an earlier lower marker, count only the earlier surviving spans, and reconcile cleanly. This is not hypothetical in the model: non-blocking exporter loss can drop arbitrary spans, and a whole late query subtree breaks no parent/wait edge into the earlier graph.

Direct no-edit synthetic check against this commit:

```text
# This is exactly what the loader would see if a four-engine-span trace had two
# query roots, markers 2 and 4, and the second root+leaf carrying marker 4 dropped.
span 1111111111111111: wcprof.engine_span=true, wcprof.session_span_count=2
span 2222222222222222: wcprof.engine_span=true

go run ./cmd/wcprof-otel-analyze <that two-span capture>
exit=0
structural gate: PASS
completeness: missing-spans=0 (declared=2 received=2 marker=true)
```

The trace is indistinguishable from a complete two-span trace because the design has no final-marker signal. The "root-marker itself dropping -> absent -> refuse" argument only holds for a single marker or when all markers drop. It does not hold once earlier per-query markers survive.

Clean fix: distinguish a final trace/session checksum from running per-query progress. The loader should require that final marker specifically, not any marker. If that final marker is absent, fail. If it is present, reconcile the exact final declared population. A dedicated `wcprof.session_complete`/final-count span at teardown, or a final marker with a separate boolean attribute, would make "final marker dropped" observable instead of silently falling back to an earlier running count.

### Blocker, same mechanism: `received >= declared` can mask loss

The code intentionally tolerates `received > declared`; `MissingSpans` is only set when `DeclaredEngineSpans > ReceivedEngineSpans` (`engine/wcprof/wcotel/loader.go:279-280`), and the gate does not reject `received > declared` (`engine/wcprof/wcotel/gate.go:164-167`).

That interacts badly with the producer's documented residual: spans created after the last query's `Stamp` are marked engine spans but excluded from the declared total (`engine/server/wcprofcount.go:41-44`). Because the loader still counts those marked spans as received, they can numerically hide a dropped declared span.

Direct no-edit synthetic check:

```text
# Declared population was 2; one declared leaf is missing; one post-stamp
# uncounted engine span arrived. The loader sees declared=2 received=2.
go run ./cmd/wcprof-otel-analyze <root-marker + post-stamp-extra>
exit=0
structural gate: PASS
completeness: missing-spans=0 (declared=2 received=2 marker=true)
```

This is the same root issue: the declared population is not a final exact population. A final marker after all counted spans are known, plus an exact `received == declared` requirement for that final population, would remove this masking path.

## Confirmed

### The simple leaf-drop hole is closed

For a trace where the declaration survives and names the final population, the loader/gate behavior is correct. `Compile` dedups first (`engine/wcprof/wcotel/loader.go:229-256`), counts distinct marked engine spans, keeps the max declared count, and computes `MissingSpans` when declared exceeds received (`loader.go:269-280`). The gate reports and hard-fails `MissingSpans > 0` (`engine/wcprof/wcotel/gate.go:164-167`, `gate.go:191-196`).

`TestCompletenessGateCatchesDroppedLeaf` exercises the exact prior hole: the dropped leaf leaves `OrphanedParents` and `UnresolvedWaitTargets` clean, but `MissingSpans=1` and `gate.Err() != nil` (`engine/wcprof/wcotel/completeness_test.go:17-64`). I ran it directly and it passed.

`TestCompletenessGateFailsByDefaultWithoutMarker` verifies old/unstamped captures are refused (`engine/wcprof/wcotel/completeness_test.go:67-87`).

### Counting is distinct-span and front-end shared

The engine marks spans at `OnStart` using `wcprof.engine_span` before the live exporters (`engine/server/session.go:717-724`, `engine/server/wcprofcount.go:54-64`). The loader counts after span-ID dedup, so start/end duplicate exports do not double-count (`engine/wcprof/wcotel/loader.go:229-256`, `loader.go:269-280`). Both otlpdump and Cloud feed the same `wcotel.Compile`, so the check applies to both front-ends.

The Cloud unit tests stamp synthetic complete traces with distinct counts and still exercise dedup and high fan-in (`engine/wcprof/wccloud/cloud_test.go:151-178`, `cloud_test.go:202-214`).

### The two tightenings are good

The CLI now separates report-write I/O failure from structural gate failure (`cmd/wcprof-otel-analyze/main.go:94-104`, `main.go:116-138`).

The live Cloud round-trip complete branch now compares a structural graph fingerprint rather than only span/wait counts (`engine/wcprof/wccloud/roundtrip_cloud_test.go:145-162`, `roundtrip_cloud_test.go:214-257`). That fixes the weakness from the prior review. It is a test-only change; compile/replay remain unchanged.

## Tests Run

```text
go test ./engine/wcprof/wcotel ./engine/wcprof/wccloud ./cmd/wcprof-otel-analyze -count=1
go test ./engine/server -run TestNonExistent -count=1
go test ./engine/wcprof/wcotel -run TestCompletenessGateCatchesDroppedLeaf -count=1 -v
```

All passed. The blocker is not a unit-test failure; it is that the safety mechanism still lacks a final, loss-detectable declaration for multi-query traces.
