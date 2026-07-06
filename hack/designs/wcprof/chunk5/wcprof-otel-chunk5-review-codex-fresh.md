# wcprof OTel Chunk 5 Review - Codex Fresh

Reviewed commit: `9555281f27` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`,
diffed against `814df0173c`.

Line numbers below refer to the checked-out `9555281f27` tree.

## Verdict

SIGN OFF for v1 capstone. I found no merge-blocking issue.

The Cloud front-end is an input swap, not a second analysis path: `SpanFromCloud` mechanically maps `cloud.SpanData` into the neutral `wcotel.Span`, and `Load` immediately feeds the unchanged `wcotel.Compile` / `wcanalyze.Build` stage. The bit-exact wait-timestamp tests are meaningful, the drift gate is wired to a complete capture plus §6.1, and the persisted/imported lazy fixture exercises the intended no-special-loader path.

One precision caveat: I agree the observed large-trace residual is out-of-scope for this loader-side chunk and gate-refused in the measured case, but "gate-safe" should not be read as a proof that every possible Cloud/export span drop is structurally detectable. A dropped leaf or otherwise unreferenced span can be invisible to a structural gate without a dropped-span counter. That is not a blocker for this commit, but it is the reason the named follow-up needs to remain a real production backstop, not optional polish.

## Checks

### Cloud Front-End

`SpanFromCloud` is a pure field map:

- Span IDs, trace IDs, parent IDs, and link target IDs are lower-cased only for normalization (`engine/wcprof/wccloud/cloud.go:36`, `engine/wcprof/wccloud/cloud.go:47`, `engine/wcprof/wccloud/cloud.go:52`).
- Start and end times are copied from Cloud timestamps; nil `EndTime` maps to `EndUnixNS=0` for the loader's in-flight/open-op convention (`engine/wcprof/wccloud/cloud.go:41`, `engine/wcprof/wccloud/cloud.go:57`).
- Attributes and link attributes are carried verbatim as `map[string]any`; no parents, wait edges, timestamps, or outcomes are synthesized (`engine/wcprof/wccloud/cloud.go:49`, `engine/wcprof/wccloud/cloud.go:59`).
- Status error is a direct enum-name comparison to the same status form the otlpdump loader recognizes (`engine/wcprof/wccloud/cloud.go:20`, `engine/wcprof/wccloud/cloud.go:60`).
- `Load` calls `Fetch`, then `wcotel.Compile`, then `wcanalyze.Build`; there is no Cloud-specific compile/replay branch (`engine/wcprof/wccloud/cloud.go:104`, `engine/wcprof/wccloud/cloud.go:109`, `engine/wcprof/wccloud/cloud.go:113`).

The CLI preserves the same gate+report flow for either input source. `-trace` only changes `loadCloud`, which delegates to `wccloud.Load` (`cmd/wcprof-otel-analyze/main.go:64`, `cmd/wcprof-otel-analyze/main.go:104`, `cmd/wcprof-otel-analyze/main.go:153`).

### §6.6 Bit Exactness

The local converter tests cover the important failure modes:

- `TestSpanFromCloudFieldMap` pins lower-casing, parent dereference, nil-end handling, status error mapping, and link carry-through (`engine/wcprof/wccloud/cloud_test.go:80`).
- `TestCloudFetchDedupGateClean` drives the full fake Cloud stream through `Load`, including a live start/end duplicate, and asserts dedup + structural gate success (`engine/wcprof/wccloud/cloud_test.go:121`).
- `TestCloudWaitNSBitExactThroughConverter` uses 19-digit Unix-ns values above `2^53`, carried as strings, and checks the compiled wait event has exact epoch-rebased integers (`engine/wcprof/wccloud/cloud_test.go:193`, `engine/wcprof/wccloud/cloud_test.go:213`).
- `TestCloudCapStressThousandsOfWaitLinks` builds a 3000-way wait-link fan-in on one span and confirms every wait survives conversion/compile and resolves (`engine/wcprof/wccloud/cloud_test.go:161`, `engine/wcprof/wccloud/cloud_test.go:181`).

The live Cloud round-trip is env-gated, so I did not run the claimed 753-link real-trace assertion locally. The test body would check exactly the right thing when run: it fetches Cloud spans, parses the same local otlpdump capture, asserts Cloud span IDs are a subset of local, and compares `wcprof.wait.*_unix_ns` string values for wait links present in both (`engine/wcprof/wccloud/roundtrip_cloud_test.go:62`, `engine/wcprof/wccloud/roundtrip_cloud_test.go:76`, `engine/wcprof/wccloud/roundtrip_cloud_test.go:92`). It also requires non-vacuous long timestamp strings (`engine/wcprof/wccloud/roundtrip_cloud_test.go:115`).

Non-blocking test-strength note: the "complete cloud graph == local" branch only compares span count and wait-edge count before asserting Cloud gate success (`engine/wcprof/wccloud/roundtrip_cloud_test.go:130`, `engine/wcprof/wccloud/roundtrip_cloud_test.go:146`). Because `complete` plus subset equality implies the same span IDs, and the unit field-map test pins the converter, I am not treating this as a blocker. A stronger future assertion would compare compiled events or selected graph structure directly.

### §6.4 Drift Gate

The drift gate is sound for a standing regression test:

- It loads either `WCPROF_DRIFT_CAPTURE` or the committed fixture (`engine/wcprof/wcotel/drift_gate_test.go:26`).
- It runs §6.1 first and fails immediately on incomplete/impossible data (`engine/wcprof/wcotel/drift_gate_test.go:42`).
- It compares simulated baseline from `RunWhatIfs` to `ActualMakespanNS` with a ±2% bound (`engine/wcprof/wcotel/drift_gate_test.go:49`, `engine/wcprof/wcotel/drift_gate_test.go:66`).

I ran it verbosely; the committed fixture reported:

```text
ops=61 wait-edges=11 actual=295044561ns baseline=295010226ns drift=-0.012% (start-conflicts=0)
```

That matches the claimed local number. I did not run the external `WCPROF_DRIFT_CAPTURE` CI workload.

### Persisted / Imported Fixture

`TestChunk5PersistedResultDecodeFaithful` is faithful to the intended shape:

- Consumer `Directory.export` waits on the imported result's lazy decode (`engine/wcprof/wcotel/chunk5_persisted_test.go:37`, `engine/wcprof/wcotel/chunk5_persisted_test.go:41`).
- The lazy persisted-decode op is explicit, and the decode work nests under it (`engine/wcprof/wcotel/chunk5_persisted_test.go:45`, `engine/wcprof/wcotel/chunk5_persisted_test.go:47`).
- The gate is clean and the wait resolves to the lazy op (`engine/wcprof/wcotel/chunk5_persisted_test.go:57`, `engine/wcprof/wcotel/chunk5_persisted_test.go:67`).
- There is no special loader logic; this is the normal parentId + wait-link path.

Minor doc nit: the test comment still says it asserts simulated baseline drift, but the implementation intentionally leaves drift to `TestStandingDriftGate` (`engine/wcprof/wcotel/chunk5_persisted_test.go:16`, `engine/wcprof/wcotel/chunk5_persisted_test.go:78`). Not a blocker.

### Export-Side Gap

The named residual Cloud loss is correctly outside the Cloud front-end and should be fixed in export infrastructure: the front-end has no read-side knob or inference that can reconstruct missing spans. The product path is still principled: it emits what it has, compiles without guessing, and runs the structural gate before reporting (`cmd/wcprof-otel-analyze/main.go:90`, `cmd/wcprof-otel-analyze/main.go:113`).

The important qualification is that structural gate refusal is guaranteed only for losses that leave structural evidence: orphaned parents, unresolved wait targets, dropped wait links where reported, cycles, impossible intervals, or unschedulable ops. Cloud does not self-report dropped spans or dropped links (`engine/wcprof/wccloud/cloud.go:33`), and the incomplete branch of the live test logs the known gap without asserting `gate.Err() != nil` unless `WCPROF_REQUIRE_COMPLETE` is set (`engine/wcprof/wccloud/roundtrip_cloud_test.go:154`). That matches the current out-of-scope decision, but the follow-up dropped-span counter remains the clean way to make all export loss gate-visible.

## Tests Run

```sh
go test ./engine/wcprof/wccloud
go test ./engine/wcprof/wcotel -run 'TestStandingDriftGate|TestChunk5PersistedResultDecodeFaithful'
go test ./cmd/wcprof-otel-analyze
go test -v ./engine/wcprof/wcotel -run 'TestStandingDriftGate|TestChunk5PersistedResultDecodeFaithful'
go test -v ./engine/wcprof/wccloud
```

All non-env-gated tests passed. `TestCloudRoundTrip` skipped locally because `WCPROF_CLOUD_TRACE_ID` and `WCPROF_LOCAL_CAPTURE` were not set.

## Conclusion

No merge blocker. This is sound to land as the v1 capstone, with the export-loss caveat kept explicit: the Cloud front-end must stay zero-inference, and the dropped-span/backpressure follow-up is the right place to make large-trace loss universally gate-visible.
