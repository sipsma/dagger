# wcprof OTel Completeness-Checksum Review

Reviewed commit `501653ddcf` against `9555281f27` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`.

## Verdict

Not landable yet. The change correctly catches the simple dropped-leaf case it tests, and the producer/loader shape is in the right direction, but the checksum is still not a complete population invariant. It only hard-fails `declared > received`; it explicitly allows `received >= declared`. Because the producer also explicitly permits marked engine spans that are not included in the last declaration, those extra received spans can offset a dropped counted leaf such as `exec.processRun` and the gate can pass silently.

This is the exact class of gap the checksum was meant to close. It needs an exact counted population, or an exact per-span/sequence completeness proof, not a lower-bound comparison.

## REAL Issues

### HIGH: `received >= declared` can still pass with a dropped counted leaf

The loader counts every distinct received span marked `wcprof.engine_span` (`engine/wcprof/wcotel/loader.go:268-270`) and takes the maximum valid `wcprof.session_span_count` marker as the declaration (`engine/wcprof/wcotel/loader.go:272-276`). It only computes `MissingSpans` when `DeclaredEngineSpans > ReceivedEngineSpans` (`engine/wcprof/wcotel/loader.go:279-280`), and the gate only fails on marker absence or `MissingSpans > 0` (`engine/wcprof/wcotel/gate.go:160-168`).

That is not sufficient because the producer's own contract allows over-receipt. The counter stamps a running total at each main-client query return (`engine/server/session.go:1478-1482`), while the implementation comments explicitly acknowledge that spans created after the last stamp are excluded from `N` but can still be received (`engine/server/wcprofcount.go:41-44`). Those spans are still marked at span start by the shared processor (`engine/server/wcprofcount.go:54-63`) and therefore increase `ReceivedEngineSpans`.

Concrete masking case:

1. A main query stamps `wcprof.session_span_count=100`.
2. One counted leaf from that query, e.g. `exec.processRun`, is dropped in export.
3. One later post-stamp engine span is created and exported. It is marked `wcprof.engine_span=true`, but it was not included in the stamped count.
4. Loader sees `declared=100`, `received=100`, `missing=0`; no parent/wait edge need be broken, so the gate passes while the user-work leaf is gone.

This is not hypothetical under the current lifecycle. `removeDaggerSession` reaps the counter before stopping services and shutting down telemetry (`engine/server/session.go:425-427`, `:438-479`), so any teardown/service spans after the last query stamp are necessarily outside the declared total while still being marked if they start through a per-client provider.

The fix needs to make the reconciled population exact. Options include: final authoritative declaration after all marked engine spans are known and before any further marked span can start; marking only spans that are in the declared population; adding a monotonic per-span sequence/checksum and failing on gaps/over-receipt; or hard-failing `received != declared` after eliminating post-stamp marked spans. As written, the checksum is a lower-bound check, not a completeness checksum.

### HIGH: Losing the highest declaration can silently downgrade to an earlier marker

The multi-query fix relies on a running total per main query and loader-side `MAX` (`engine/server/wcprofcount.go:30-35`, `engine/wcprof/wcotel/loader.go:272-276`). That handles fragmentation only if the highest marker is present. If the final `POST /query` end export is lost but its start snapshot survived, the parent span is still present, so `OrphanedParents` does not fire; the marker attribute is missing because it is set only at query return on the still-open span (`engine/server/session.go:1464-1482`, `engine/server/wcprofcount.go:91-93`). An earlier lower marker can remain present, `SessionMarkerPresent=true`, and later received spans can make `received >= declared`.

This creates another silent path for exactly the same leaf-drop problem: the declaration can undercount the trace, and the gate does not treat under-declaration or over-receipt as a violation.

The current tests do not cover this. `TestCompletenessGateCatchesDroppedLeaf` proves `declared=5, received=4` fails (`engine/wcprof/wcotel/completeness_test.go:42-64`), and `TestCompletenessGateFailsByDefaultWithoutMarker` proves no marker fails (`engine/wcprof/wcotel/completeness_test.go:67-87`). There is no test for `declared < received` or for a missing final/max marker with an earlier marker still present.

## Other Notes

- The basic producer plumbing is otherwise sound: the shared span processor is registered before the live exporters, so the `wcprof.engine_span` mark exists before live-start snapshots (`engine/server/session.go:716-728`), and it is shared across per-client providers for nested clients (`engine/server/wcprofcount.go:23-28`).
- The loader-side distinct-span accounting is dedup-safe for normal live start/end duplicates because it runs after `Compile` dedups by span ID and keeps the max-ended copy (`engine/wcprof/wcotel/loader.go:229-256`).
- Fail-by-default for an entirely unstamped trace is correct and aligns with the no-inference principle (`engine/wcprof/wcotel/gate.go:164-165`).
- The CLI report-write fix from the previous review is correct: `analyze` now returns gate status separately from report I/O errors (`cmd/wcprof-otel-analyze/main.go:97-107`, `:119-145`).
- The Cloud round-trip tightening is materially better: it now compares a structural graph fingerprint instead of only op/wait counts (`engine/wcprof/wccloud/roundtrip_cloud_test.go:153-158`, `:214-257`). It is still structural rather than full semantic equality; it omits fields such as `WorkType`, `Outcome`, `ResultID`, and `Open` from the fingerprint even though those affect attribution/reporting (`engine/wcprof/wcanalyze/graph.go:17-30`). This is secondary to the checksum blocker.

## Tests Run

```text
go test ./cmd/wcprof-otel-analyze ./engine/wcprof/wcotel ./engine/wcprof/wccloud ./engine/server -run 'Test(Completeness|Cloud|SpanFromCloud|StandingDriftGate|ParseDedup|TimestampExactness|CausalParent|OpKind|RemoteWorkspace|Session)' -count=1
go test ./cmd/wcprof-otel-analyze ./engine/wcprof/wcotel ./engine/wcprof/wccloud ./engine/server -count=1
```

Both passed.
