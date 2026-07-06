# wcprof OTel Chunk 5 Review - Codex

Reviewed commit: `9555281f27` on top of `814df0173c` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`.

Verdict: NOT SIGNED OFF. One blocker.

## Findings

### Blocker: the known Cloud export loss is not gate-safe in general

The commit correctly names the residual Cloud loss as CLI to Cloud exporter/BSP loss, but it then treats that gap as safe because the structural gate refuses incomplete Cloud traces. That is not an invariant the code can uphold.

The production Cloud path has no independent completeness signal. `wccloud.Fetch` streams whatever `StreamSpans` returns and maps it (`engine/wcprof/wccloud/cloud.go:88-99`); `wccloud.Load` compiles/builds that set unchanged (`cloud.go:104-117`); `cmd/wcprof-otel-analyze` then accepts the trace if `CheckStructural` passes (`cmd/wcprof-otel-analyze/main.go:109-117`, `main.go:120-133`). The structural gate checks impossible/inconsistent structure: unresolved waits, malformed waits, orphaned parents, cycles, impossible intervals, and unschedulable ops (`engine/wcprof/wcotel/gate.go:117-144`). It does not and cannot prove that every emitted span arrived.

Direct no-edit experiment against this commit shows the hole. I removed both start/end records for one leaf `copy` span from the committed drift fixture (`engine/wcprof/wcotel/testdata/drift-module-functions.otlpdump.jsonl:117-118`, span `013d2e21d627c959`) and ran the analyzer:

```text
go run ./cmd/wcprof-otel-analyze <(awk '!/"spanId":"013d2e21d627c959"/' engine/wcprof/wcotel/testdata/drift-module-functions.otlpdump.jsonl)
exit=0
structural gate: PASS
ops=60 roots=1 open=0 wait-edges=11
orphaned-parents=0 unresolved-targets=0 unschedulable-ops=0
```

That is an incomplete trace which the gate accepts. This is expected mechanically: dropping an isolated leaf, or a whole self-contained subtree, leaves no dangling parent/wait edge for the structural gate to observe. A non-blocking exporter queue can drop arbitrary spans, so the known residual Cloud exporter loss cannot be considered "never wrong-ranked" without a dropped-span/completeness signal or lossless/backpressured export path.

The live round-trip test also does not enforce the safety claim. In the incomplete branch, it logs "the structural gate correctly refuses it" but never asserts `gate.Err() != nil` unless `WCPROF_REQUIRE_COMPLETE` is set (`engine/wcprof/wccloud/roundtrip_cloud_test.go:154-160`). A structurally clean but incomplete trace would pass the test and be reported as safe.

This does not invalidate the Cloud field mapper. It invalidates the productionization claim that the named export-side gap is safe to defer while enabling `-trace` analysis. Clean options:

- Make the Cloud export path lossless for this source, or surface a dropped-span counter/completeness marker into the trace and hard-fail it before ranking.
- Keep the Cloud front-end behind a "complete only" gate whose completeness is externally proven, not inferred from structural consistency.
- At minimum, change the round-trip test to fail incomplete traces whose structural gate passes, because that is exactly the unsafe case.

### Medium: the complete-roundtrip assertion is weaker than the text says

The complete branch of `TestCloudRoundTrip` says the Cloud graph must match local, but it only compares span count and wait-edge count (`engine/wcprof/wccloud/roundtrip_cloud_test.go:145-153`). Because the subset check plus equal count proves the same span IDs are present (`roundtrip_cloud_test.go:81-90`, `roundtrip_cloud_test.go:128-130`), this is useful, but it does not prove graph equality or byte-identical compiled events. Parent IDs, names, timestamps, non-wait attributes, and outcomes are covered by representative unit tests, not by the live round-trip.

This is not the main blocker, but if the round-trip is meant to be the production proof, compare the compiled IR or graph shape directly in the complete branch.

## Confirmed

### Cloud front-end is zero-inference

`SpanFromCloud` is a mechanical field map: trace/span/parent IDs, name, start/end timestamps, attributes, status, and links are copied from `cloud.SpanData` into `wcotel.Span` (`engine/wcprof/wccloud/cloud.go:36-62`). The only normalization is lower-casing IDs so references match consistently (`cloud.go:37-55`). It does not synthesize parents, wait edges, timestamps, or attributes.

The Cloud API client uses `spansUpdated(root: true, listen: nil)` (`internal/cloud/trace.go:154-170`). The new front-end then calls the unchanged `wcotel.Compile` and `wcanalyze.Build` (`engine/wcprof/wccloud/cloud.go:104-117`). This chunk does not modify `wcotel` loader/gate/replay or `wcanalyze` production code; the only `wcotel` additions are tests and fixture data.

One inherited caveat: `internal/cloud.streamGraphQL` logs callback errors instead of returning them (`internal/cloud/trace.go:272-274`). Normal schema decode is covered by tests, but if a Cloud event decode ever fails, this is another partial-data path. It reinforces the need for an explicit completeness signal rather than relying on the structural gate.

### Wait-ns bit exactness is meaningfully tested

The unit converter test uses 19-digit values above 2^53 and checks the compiled wait interval matches exact integer arithmetic (`engine/wcprof/wccloud/cloud_test.go:193-229`). The live env-gated test compares every wait link present in both local and Cloud by string value and fails if no wait links are checked (`engine/wcprof/wccloud/roundtrip_cloud_test.go:92-126`). That is the right proof for the string-ns JSON float64 hazard.

The 3000-way fan-in cap-stress is meaningful for the converter/compiler side: it creates 3000 linked waits and verifies all survive as resolved waits (`engine/wcprof/wccloud/cloud_test.go:154-190`). It does not prove the live Cloud 16384 cap by itself; the code is honest about that being the local half.

### Drift gate is sound

`TestStandingDriftGate` first requires the section 6.1 structural gate to pass, then compares simulated baseline makespan to actual makespan within a +/-2% band (`engine/wcprof/wcotel/drift_gate_test.go:42-70`). I ran it directly:

```text
go test ./engine/wcprof/wcotel -run TestStandingDriftGate -count=1 -v
drift-module-functions.otlpdump.jsonl: ops=61 wait-edges=11 actual=295044561ns baseline=295010226ns drift=-0.012% (start-conflicts=0)
```

That is an appropriate standing regression check for faithful complete captures.

### Persisted fixture is faithful at the loader boundary

`TestChunk5PersistedResultDecodeFaithful` constructs the persisted lazy-decode shape, verifies the consumer's lazy wait resolves to the lazy decode op, and verifies the decode work nests under that op (`engine/wcprof/wcotel/chunk5_persisted_test.go:55-77`). It adds no loader/replay inference. It is a hand-built loader fixture, not a live persisted-cache integration test; `ProfileSkip` JSON import survival remains covered by the prior skip-fix tests, not this chunk.

## Tests Run

```text
go test ./engine/wcprof/wccloud ./engine/wcprof/wcotel ./cmd/wcprof-otel-analyze -count=1
go test ./engine/wcprof/wcotel -run TestStandingDriftGate -count=1 -v
go test ./engine/wcprof/wccloud -run 'Test(SpanFromCloudFieldMap|CloudFetchDedupGateClean|CloudCapStressThousandsOfWaitLinks|CloudWaitNSBitExactThroughConverter)$' -count=1 -v
go test ./engine/wcprof/wcotel -run TestChunk5PersistedResultDecodeFaithful -count=1 -v
```

All passed. The blocker is not a failing unit test; it is the missing completeness invariant for a known lossy Cloud export path.
