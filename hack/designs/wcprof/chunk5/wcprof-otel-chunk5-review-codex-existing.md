# wcprof OTel Chunk 5 Review

Reviewed commit `9555281f27` against `814df0173c` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`.

## Verdict

Not a clean v1 capstone sign-off as written. The Cloud front-end implementation is a clean zero-inference input swap, and the local/unit validation is mostly sound. The blocker is the characterization of the remaining CLI→Cloud exporter loss as "gate-safe": the current gate cannot prove completeness when the missing spans are leaves or otherwise closed subtrees. That can silently erase user-work-first-class attribution, especially `exec.processRun`, without producing an orphaned parent or unresolved wait target.

If the owner explicitly narrows Chunk 5 to "Cloud loader is correct for complete Cloud traces; exporter losslessness remains required for production trust," the code itself is buildable. But the current productionization claim that incomplete Cloud traces are made safe by the gate is too strong.

## REAL Issues

### HIGH: Export-side span loss is not generally gate-safe

The named gap is correctly located outside the Cloud front-end, but it is not safe to rely on the structural gate to catch every incomplete Cloud trace.

Evidence:

- The Cloud converter has no source-loss provenance to carry into the gate. `SpanFromCloud` maps IDs, times, attrs, status, and links, and leaves dropped link/attr counts unavailable by construction (`engine/wcprof/wccloud/cloud.go:33-35`, `:36-62`). There is also no dropped-span or expected-span-count signal in `wcotel.Compiled`.
- The gate hard-fails on structural symptoms: unresolved wait targets, malformed wait timings, orphaned parents, dropped links on wait traces, cycles, self/interval impossibilities, and unschedulable ops (`engine/wcprof/wcotel/gate.go:117-143`). It does not know that a parent should have had a child, or that the source exporter dropped a leaf span.
- The live round-trip test does not enforce the claim in the incomplete branch. When `complete == false`, it only logs unresolved/orphan counts unless `WCPROF_REQUIRE_COMPLETE` is set (`engine/wcprof/wccloud/roundtrip_cloud_test.go:154-160`).

Concrete counterexample: if the CLI→Cloud BSP drops an `exec.processRun` leaf but keeps its `exec.run` / `call_exec` ancestors, the Cloud graph has no orphaned parent, no unresolved wait target, and no dropped-link metadata. The gate can pass while the user-work span is gone and the time is attributed to an ancestor's self-time/class instead. That violates the "user work first-class" goal even though the graph is structurally schedulable.

The principled fix is still emit/export-side, not loader inference: make the Cloud export path lossless/backpressured, or emit/store a dropped-span/completeness signal that the gate hard-fails on. Until then, a Cloud trace is trustworthy only when independently known complete, not merely because the structural gate passes.

### MEDIUM: The live Cloud round-trip does not actually assert graph equality

`TestCloudRoundTrip` claims that a complete Cloud trace compiles to the same graph as local, and logs `cloud graph == local`, but the assertion only compares `SpanCount` and wait-edge count (`engine/wcprof/wccloud/roundtrip_cloud_test.go:146-153`). It does not compare compiled events, parent IDs, op kind/class/work type, outcomes, result IDs, timestamps, or wait targets.

That leaves real Cloud attr transformations under-tested. For example, losing `wcprof.work_type=user`, `wcprof.parent`, or `dag.digest` could keep the same span and wait counts while changing the analysis. The unit field-map test covers the converter shape (`engine/wcprof/wccloud/cloud_test.go:77-115`) and the wait-ns tests cover decimal-string preservation (`engine/wcprof/wccloud/cloud_test.go:197-229`, `roundtrip_cloud_test.go:92-126`), but the live §6.6 graph-equality claim needs a canonical compiled-event/graph comparison if it is going to be relied on as the production ingest proof.

Related small gap: live wait timing comparison keys local waits only by target span ID (`engine/wcprof/wccloud/roundtrip_cloud_test.go:166-174`). If one waiter has multiple wait links to the same target, later entries overwrite earlier ones. That is probably not hit by the reported 753-link trace, but it is not a fully general bit-exact wait-link comparison.

### LOW: CLI report-write errors are now reported as gate failures

`analyze` returns `false` for both structural gate failures and `wcanalyze.WriteReport` errors (`cmd/wcprof-otel-analyze/main.go:120-133`), and both `runFiles` and `runCloud` turn that into `structural gate failed (see above)` (`cmd/wcprof-otel-analyze/main.go:101-103`, `:114-115`). This is not on the profiler model path, but it is a regression from returning the report error directly.

## Confirmed / Noise

- The Cloud front-end itself is zero-inference. `SpanFromCloud` is a mechanical field map (`engine/wcprof/wccloud/cloud.go:36-62`), and `Load` immediately feeds those spans to the unchanged `wcotel.Compile` and `wcanalyze.Build` (`engine/wcprof/wccloud/cloud.go:104-117`).
- The OSS Cloud client is using the intended read surface: `StreamSpans` sends `root:true`, `listen:nil` (`internal/cloud/trace.go:160-170`). Backend code confirms that with `listen == nil`, `BatchSpans` takes the full `selectSpans` branch rather than the incremental subset branch (`api/db/traces.go:605-621` in the backend checkout). The resolver batches results but does not intentionally roll them up (`api/graph/vars.go:33-66`).
- Cloud preserves the needed span fields through the read path: the backend selects link IDs and attributes (`api/db/traces.go:1095-1098`) and reconstructs span attributes/links in the GraphQL model (`api/db/traces.go:1532`, `:1550-1556`). Numeric-looking wait timestamps emitted as strings remain strings because stored string attrs are JSON-quoted and `unmarshalAttributes` strips quotes before exposing them (`api/db/traces.go:2323-2327`, `:2341-2349`).
- The converter cap-stress is meaningful for the wccloud→wcotel path: 3000 wait links through the fake Cloud stream compile and gate clean (`engine/wcprof/wccloud/cloud_test.go:154-190`). It is not a live Cloud-storage cap test, and the test comment is honest about that (`:158-160`).
- The §6.4 drift gate is a useful standing regression check: it runs §6.1 first, computes replay baseline versus actual, and enforces ±2% (`engine/wcprof/wcotel/drift_gate_test.go:42-70`). My run produced `drift=-0.012%` with `start-conflicts=0`.
- The persisted/imported fixture is faithful for the modeled shape: the consumer wait targets the lazy decode op, and the decode work remains nested under that lazy op (`engine/wcprof/wcotel/chunk5_persisted_test.go:41-47`, `:67-77`). No special loader logic was added.

## Tests Run

```text
go test ./cmd/wcprof-otel-analyze ./engine/wcprof/wccloud ./engine/wcprof/wcotel -run 'Test(SpanFromCloud|CloudFetch|CloudCap|CloudWait|StandingDriftGate|Chunk5Persisted)' -count=1 -v
go test ./cmd/wcprof-otel-analyze ./engine/wcprof/wccloud ./engine/wcprof/wcotel -count=1
```

Both passed. I did not run the env-gated live Cloud round-trip locally.
