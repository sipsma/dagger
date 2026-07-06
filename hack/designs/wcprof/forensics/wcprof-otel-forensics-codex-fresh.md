# wcprof OTel Forensics: publishResult / Missing Data

Fresh investigation from scratch, treating only the principle as settled: the analysis is a rational function of recorded data; it must not compensate for missing or misleading data.

## Verdict

The working theory mostly survives, but with a sharper hop:

- The 330/501 orphaned `dagql.publishResult` records are not loader artifacts, not parentless emit, not live-start duplicate confusion, not cross-trace parentage, and not span-ID normalization.
- The absent parent IDs are call_exec SpanContexts. In the fresh capture, sampled missing parent IDs are also used as `wcprof.wait.reason=call_exec` link targets by a waiter span, proving these IDs came from the `call_exec` span context published by the cache.
- The missing parent span records are already absent from the engine client telemetry SQLite DB while their `dagql.publishResult` children are present. That pins the loss before the final otlpdump receiver and before the local JSONL loader.
- The most plausible concrete mechanism is non-blocking `BatchSpanProcessor` queue loss in the live telemetry path, especially the engine per-client `LiveSpanProcessor` that writes to the client DB. This is strongly supported by code and symptoms, but I did not instrument the SDK drop counter, so I would word it as “pinned to engine telemetry storage/export; likely BSP queue drop,” not “proven HTTP choke” or “proven Cloud complete.”

The current `cloud.StreamSpans(root:true)` / `spansUpdated` API is not a full-fidelity trace source for this use. For the same trace it returns a strict subset of the local capture, and can omit parents whose spans are present locally. I did not find a full-fidelity Cloud fetch through the existing in-repo client path.

## Evidence

### Local captures

Existing artifact `/tmp/otel-exec.jsonl` after live de-dup:

- raw span records: `22021`
- deduped spans: `11162`
- trace IDs: `1`
- `dagql.publishResult`: `4748`
- publishResult with empty parent: `0`
- publishResult with parent ID absent from deduped span IDs: `330`
- present publishResult parents by kind: `4418` are `call_exec`

Fresh capture I ran with the Chunk 4/forensics CLI (`4585bf413d`) against `dagger-engine.dev`:

- command: `dagger --progress=plain -c 'container | from alpine | with-exec sleep 0 | stdout'`
- output: `/tmp/otel-fresh-codex.jsonl`
- raw span records: `21794`
- deduped spans: `11229`
- `dagql.publishResult`: `4952`
- publishResult with empty parent: `0`
- publishResult with parent ID absent: `501`
- all spans with absent parent: `501`, all of them `dagql.publishResult`
- present publishResult parents by kind: `4451` are `call_exec`

That reproduces the issue on a new capture. It is not stale state in `/tmp/otel-exec.jsonl`.

### Specific orphan chase

In the fresh capture:

- `dagql.publishResult` span `005b1b6211429b67` has parent `cc854d08a02a8b12`.
- No raw span record has `spanId=cc854d08a02a8b12`.
- The same missing ID is also a `wcprof.wait.reason=call_exec` link target from a `POST /query` span.

That makes the “bad publishResult-only parent” framing too narrow: the missing ID is the shared call_exec target handed to both child parentage and wait links.

The code path matches that:

- `beginOTelCallExec` starts the call_exec span and sets `wcprof.op.kind=call_exec` plus `dag.digest` (`dagql/otelprof_hooks.go:58`-`64`).
- The resulting `execSpan.SpanContext()` is stashed on `oc.execSpanCtx` under `callsMu` before publishing the ongoing call (`dagql/cache.go:3731`-`3758`).
- `dagql.publishResult` is emitted only when that SpanContext is valid and is intended to be a child of `oc.sharedWorkCtx`, i.e. the call_exec context (`dagql/cache.go:4013`-`4018`, `dagql/otelprof_hooks.go:76`-`82`).

So an ID that appears as a publishResult parent and call_exec wait target is not invented by the loader. It is a real emitted SpanContext whose span record did not survive.

### Loader ruled out

The loader preserves the raw `parentId`, de-dups by `spanId` keeping max `endNs`, and maps parent IDs mechanically:

- parse preserves JSONL `parentId` (`engine/wcprof/wcotel/loader.go:190`-`198`)
- live de-dupe is by `spanId` with max end time (`engine/wcprof/wcotel/loader.go:212`-`217`)
- non-lock wait links increment `UnresolvedWaitTargets` when the target span ID is absent (`engine/wcprof/wcotel/loader.go:370`-`379`)
- the structural gate now hard-reports orphaned parents (`engine/wcprof/wcotel/gate.go:93`-`95`, `engine/wcprof/wcotel/gate.go:138`-`139`)

Running the current analyzer on `/tmp/otel-exec.jsonl` fails the gate with `orphaned-parents=330`. Running it on my fresh capture fails with `orphaned-parents=501` and `unresolved-targets=2046`.

### Engine DB pins the hop

For a second fresh capture with a larger CLI-side BSP queue (`/tmp/otel-fresh-bigqueue-codex.jsonl`, trace `a70ac025d4dfc85b78ece8a20a1af9f4`), I queried the engine client DB:

- DB: `/var/lib/dagger/worker/clientdbs/z7pfx4zrz2yupyeszpwj1kx2c.db`
- parent `0c352bf3d4a676ef`: `0` rows
- child `00a45b9e3104fd20`: `1` row, `name=dagql.publishResult`, `parent_span_id=0c352bf3d4a676ef`
- parent `c34e17d661487183`: `0` rows
- child `01a4c67dc7174523`: `1` row, `name=dagql.publishResult`, `parent_span_id=c34e17d661487183`

This proves the final otlpdump receiver and JSONL parser are not the first loss point. The engine telemetry DB already has child rows whose parent rows are missing.

The likely mechanism is visible in the telemetry stack:

- engine per-client spans are saved via `telemetry.NewLiveSpanProcessor(client.spanExporter)` (`engine/server/session.go:705`-`709`) and also to parent client DBs the same way (`engine/server/session.go:730`-`735`)
- `NewLiveSpanProcessor` wraps `sdktrace.NewBatchSpanProcessor` with only a near-immediate timeout (`/home/sipsma/go/pkg/mod/github.com/dagger/otel-go@v1.43.1-0.20260515012101-af7cd0684887/live.go:15`-`21`)
- the OTel SDK default queue is `2048` and “if the queue gets full it drops the spans” (`/home/sipsma/go/pkg/mod/go.opentelemetry.io/otel/sdk@v1.43.0/trace/batch_span_processor.go:35`-`39`)
- without `WithBlocking`, enqueue uses `enqueueDrop`; on a full queue it increments a dropped counter and discards the span (`/home/sipsma/go/pkg/mod/go.opentelemetry.io/otel/sdk@v1.43.0/trace/batch_span_processor.go:390`-`426`)

That failure mode exactly matches the observed data: the SpanContext survives in memory and is used by children/waits, while the span record itself is silently lost before the DB/stream.

I also tried increasing the CLI-side BSP queue (`OTEL_BSP_MAX_QUEUE_SIZE=100000`, `OTEL_BSP_MAX_EXPORT_BATCH_SIZE=4096`). The comparable run still had `392` publishResult parents absent. The workload/cache state differed, so I would not use the count as a strict A/B, but it does falsify “only final otlpdump HTTP/export queue was too small.” The engine DB query is the stronger hop proof.

## Cloud Reconciliation

For `/tmp/otel-exec.jsonl`, querying Cloud with the same `spansUpdated` subscription shape used by `cloud.StreamSpans` returned:

- Cloud spans: `2369`
- local dedup spans: `11162`
- Cloud IDs not in local: `0`
- local IDs not in Cloud: `8793`
- Cloud publishResult: `853`
- Cloud call_exec: `869`
- Cloud spans with absent parent in returned set: `85`
- all `85` of those parents are present in local

`root:true` and `root:false` returned the same count. Supplying `listen` with specific span IDs returned the same historical view. Supplying non-null `before`/`after` returned zero spans in my probes. GraphQL introspection is disabled.

For my fresh trace `4eebe9fefaf7fb242a97b33286bd793f`, Cloud returned:

- Cloud spans: `3095`
- local dedup spans: `11229`
- Cloud is again a strict subset of local IDs
- Cloud spans with absent parent in returned set: `411`
- `282` of those parents are present locally
- `129` are absent locally too

Conclusion: the current Cloud read path is a smaller subscription/UI view, or at least behaves like one. It is not a full-fidelity trace fetch suitable for wcprof forensics. It cannot validate completeness, and “Cloud is likely complete” is not supported.

## Strongest Refutations Tried

- Loader bug: refuted. Raw JSONL and engine DB contain children with parent IDs whose parent rows are absent. The loader is only surfacing that.
- Parentless publishResult emit: refuted. Empty publishResult parent count is zero. Present publishResult parents are all call_exec. Missing parent IDs are also call_exec wait targets in the fresh capture.
- Cross-trace or cross-session parent: refuted for local captures. Each local capture had exactly one trace ID.
- Span ID normalization: refuted. IDs are 16-char lowercase hex from `hex.EncodeToString` in otlpdump; missing IDs are absent as raw `spanId` and as DB `span_id`.
- Live heartbeat/start-end semantics: refuted as primary explanation. De-duping live start/end records still leaves absent parent IDs, and the parent has zero raw records / zero DB rows.
- Cloud as complete source: refuted for the current API. The Cloud response is a strict subset of local and can omit parents that local has.
- Emit wrote a parent ID for a span that never existed: possible only in the trivial sense that the SDK gave us a valid SpanContext and the span record was then dropped. The call_exec span is started in code; the same ID is reused by wait links. This points to telemetry export/storage loss, not a random ID bug in publishResult.

## Impact

The ranking is not trustworthy on affected traces. Missing call_exec parents turn publishResult into false roots and can degrade wait edges into fixed delays when the wait target is absent. The current gate correctly fails on current captures, which is the right posture.

The Cloud north-star path is also not ready if it depends on `cloud.StreamSpans(root:true)`: that API does not return the complete trace graph.

## Principled Fix

Do not add loader inference or re-parent publishResult away from call_exec. The data says these spans had call_exec parents; the missing data is the call_exec span records.

Fix the data path:

1. Make the engine per-client telemetry DB path lossless for causal profiling spans: use a blocking or synchronous live span processor, or otherwise guarantee no `BatchSpanProcessor` queue drops before `clientSpans.ExportSpans`.
2. Expose and gate on any SDK/exporter dropped-span counter. A trace with dropped spans is incomplete data and must fail before ranking.
3. Provide a full-fidelity Cloud trace fetch for wcprof, separate from the current `spansUpdated(root:true)` view if necessary. Validate it against a lossless local capture by exact span ID counts and zero orphaned parents.
4. Re-run the same module-load workload after fixing transport. If orphaned parents remain with a lossless path, then investigate emit context propagation; current evidence points to transport/storage loss first.

End summary:

- Strongest attempt to break the drop theory: querying raw JSONL, Cloud, and the engine client DB for specific orphan parent IDs. It survived for local data and became stronger: the engine DB itself is missing the parent rows.
- Is data missing, and where: yes. Missing at or before engine client telemetry DB insertion/export; not first lost by otlpdump or the loader.
- Cloud reconciliation: current `spansUpdated`/`StreamSpans` returns a strict subset, not full fidelity; no complete Cloud fetch found.
- Principled fix: make telemetry storage/export lossless and make Cloud expose a full-fidelity trace source; keep the analysis zero-inference.

## Addendum: Local Drop Site + Cloud Backend Cross-Check

This pass used the proprietary backend checkout at
`/home/sipsma/.tailcall/worktrees/sipsma-dagger.io-1311b0c59e24/dagger-io-backend-checkout-b5a08256-1d28c96b`
(`d54eee1a2`). I could read it.

### Local drop-site discriminator

The strongest experiment to break "it is the SDK BSP queue" is set accounting at
four boundaries for only causal profiling spans:

- `A`: `beginOTelCallExec` minted IDs plus the IDs published as `publishResult.parentId`
  and `wcprof.wait.reason=call_exec` targets.
- `B`: a temporary tracer-provider audit processor, registered immediately before
  `telemetry.NewLiveSpanProcessor`, logging `OnStart` and `OnEnd` span IDs.
- `C`: `clientSpans.ExportSpans` input IDs before marshal/insert.
- `D`: per-row DB insert success/failure IDs, plus final DB contents.

The outcome matrix is decisive:

- `A` missing from `B`: emit/sampling/provider bug; the call_exec span never reached
  processors.
- `B` present but `C` missing: SDK `BatchSpanProcessor` enqueue/export loss.
- `C` present but `D` missing: marshal/SQLite insert bug. This must include counters
  for each `continue` in `clientSpans.ExportSpans`, because marshal failures currently
  skip individual spans after logging a warning.
- `D` present but otlpdump missing: final DB stream / CLI / otlpdump loss.

I did not land or run that temporary instrumentation. I did re-check the code paths:

- The OTel SDK calls every span processor's `OnStart` synchronously before `Tracer.Start`
  returns the span context (`go.opentelemetry.io/otel/sdk@v1.43.0/trace/tracer.go:66`-`71`).
  That makes "bad ID minted after no OnStart" possible only if the span was not
  recording/sampled or the provider was not the expected SDK provider.
- The engine client DB path uses `telemetry.NewLiveSpanProcessor(client.spanExporter)`
  (`engine/server/session.go:695`-`699`) and again for parent DBs
  (`engine/server/session.go:720`-`726`).
- `NewLiveSpanProcessor` wraps a normal SDK `BatchSpanProcessor` with only a near-immediate
  timeout (`github.com/dagger/otel-go@.../live.go:15`-`21`) and converts `OnStart`
  into an `OnEnd` snapshot (`live.go:25`-`30`).
- The SDK BSP defaults to a 2048 queue and drops when full unless blocking is enabled
  (`go.opentelemetry.io/otel/sdk@v1.43.0/trace/batch_span_processor.go:35`-`39`,
  `:390`-`:426`).
- `clientSpans.ExportSpans` can also skip spans on marshal errors before insertion
  (`engine/server/telemetry.go:342`-`371`) and inserts rows one at a time
  (`engine/server/telemetry.go:405`-`409`).

So queue-drop remains the leading local hypothesis, but not yet the proven hop. The
proof should be `B present / C absent`, and the no-drop run should change the engine
process's live processor itself: `WithBlocking` or a synchronous/simple processor for
`client.spanExporter`. The earlier CLI-side `OTEL_BSP_MAX_QUEUE_SIZE=100000` run did
not prove or disprove the engine client DB BSP path.

### Cloud backend: read view vs stored subset

The backend source and a fresh query with the saved org ID overturn the "just a rolled-up
read view" explanation for this particular trace.

Backend code:

- `/v1/traces` ingest unmarshals every OTLP span and appends every one to the main
  `batch`; it does not filter `call_exec`, `publishResult`, or wcprof spans
  (`api/otlp/traces.go:61`-`117`).
- The handler calls `InsertSpansAsync` for that whole batch and returns 500 on error
  (`api/otlp/traces.go:145`-`154`).
- `InsertSpansAsync` writes to ClickHouse with `async_insert=1` and
  `wait_for_async_insert=0` (`api/db/traces.go:2168`-`2172`), then appends every span
  and sends the batch (`api/db/traces.go:2237`-`2278`). That leaves a possible
  asynchronous ClickHouse loss/rejection point after the HTTP handler has accepted the
  request.
- `trace(id).spans` calls `TraceQueries.Spans`, which selects from the base
  `otel_traces` table ordered by timestamp (`api/graph/trace.resolvers.go:385`-`388`,
  `api/db/traces.go:563`-`570`).
- `spansUpdated` calls `BatchSpans` (`api/graph/trace.resolvers.go:316`-`320`).
  `BatchSpans` first counts rows in the base table and, when `spanCount < 100000`,
  returns a plain full-table `selectSpans` result, not the partial incremental view
  (`api/db/traces.go:605`-`622`). The `partial=true` marker is only set inside the
  large-trace incremental branch (`api/db/traces.go:738`-`746`).

Measurement for local trace `6227b3d37f72937685e4221997bbcc4d`:

- Using the saved org ID from the earlier successful request, `spansUpdated(root:true)`
  returns `2369` unique spans, all `partial=false`: `853` `dagql.publishResult`,
  `869` `call_exec`, `85` absent parents in the returned set.
- `trace(id){ spans { ... } }` returns exactly the same `2369` spans, all `partial=false`.
- The full resolver set equals the subscription set exactly:
  `sub_minus_full=0`, `full_minus_sub=0`.
- Local `/tmp/otel-exec.jsonl` has `11162` de-duped spans. Cloud full-store-visible
  spans are a strict subset of local: `full_minus_local=0`, `local_minus_full=8793`.
- Sample Cloud-store orphan: span `fce6dd3158d05a85` (`Host._sshAuthSocket`) has parent
  `991e5f5900274303`, and that parent is present in the local capture as
  `Host._sshAuthSocket` but absent from Cloud's full `trace.spans` result.

I initially got `spansUpdated:null` and `trace: no rows` when using the current
`~/.config/dagger/org` value; that value is not the same as the saved org ID used for
the trace. I did not print either value. Reusing the saved org ID reproduced the earlier
Cloud result and enabled the full `trace.spans` query.

Conclusion for Cloud: for this trace, the 1/5 result is not merely a `spansUpdated`
read-view rollup. The base Cloud store as exposed by `trace.spans` contains only the
same 2369-span subset. The backend ingest code does not contain a semantic filter that
would explain selectively omitting the other 8793 rows. The remaining plausible Cloud
loss points are upstream of the Cloud handler (engine/CLI exporter path, likely another
BSP/transport queue) or inside/asynchronously after ClickHouse `async_insert` acceptance.
The code alone does not distinguish those two.

### What would prove the Cloud hop

Add the same span-ID accounting at Cloud ingress:

- Count and sample span IDs immediately after OTLP unmarshal and before `batch = append`.
- Count batch IDs immediately before `InsertSpansAsync`.
- Count append/send errors and, if possible, enable `wait_for_async_insert=1` or query
  ClickHouse async-insert failure diagnostics for the test.
- Compare those sets against the local otlpdump set and against `trace.spans`.

If Cloud ingress sees all 11162 local IDs but `trace.spans` has 2369, the loss is
ClickHouse async insert/storage. If Cloud ingress only sees 2369, the loss is upstream
of Cloud, not in backend storage or the read resolver.

Addendum summary:

- Strongest local discriminator: `A/B/C/D` span-ID set accounting plus an engine-side
  blocking/simple live processor run. Queue-drop survives as leading hypothesis, but
  the exact local hop is not proven until `B present / C absent` is measured.
- Cloud read-view verdict: not a read-view explanation for this trace. `trace.spans`
  and `spansUpdated` both return the same 2369 all-`partial=false` rows.
- Cloud loss location: proven missing from the Cloud base store as exposed by GraphQL;
  not yet proven whether missing before `/v1/traces` receives it or after ClickHouse
  async insert accepts it.
- Principled fix direction: do not compensate in analysis. Make the engine/local
  telemetry path lossless for causal spans, make Cloud ingest/storage loss observable
  and lossless for these spans, and gate ranking on zero dropped/missing causal data.

## Addendum: why BSP pressure appears now - volume and telemetry skip

Erik's hypothesis is correct: the branch emits far more telemetry than base/pre-emit,
and the increase is primarily the wcprof OTel cache-path spans. Those spans currently
ignore the same telemetry skip that normally suppresses high-volume introspection and
introspection-style calls.

### Measurement

Same warmed workload:

`dagger --progress=plain -c 'container | from alpine | with-exec sleep 0 | stdout'`

Captured with local `hack/otlpdump`, `OTEL_EXPORTER_OTLP_TRACES_LIVE=1`, and de-duped
by `(traceId, spanId)` because live export writes start and end records.

| ref | capture | raw span records | unique spans | logs | metrics |
| --- | --- | ---: | ---: | ---: | ---: |
| base `b442cd2533` | `/tmp/otel-volume-base-b442-warm.jsonl` | 2834 | 1417 | 472 | 0 |
| pre-wcprof-emit `71b69f1f16` | `/tmp/otel-volume-preemit-71b-warm.jsonl` | 2834 | 1417 | 471 | 0 |
| branch `4585bf413d` | `/tmp/otel-volume-branch-dev-4585bf-warm.jsonl` | 23318 | 12018 | 600 | 113 |
| release `v0.21.7` | `/tmp/otel-volume-release-v0217-implcwd.jsonl` | 3320 | 1660 | 727 | 0 |

Branch vs base:

- Unique spans: `12018 / 1417 = 8.48x`.
- Raw span records: `23318 / 2834 = 8.23x`.
- Unique-span delta: `10601`.
- Wcprof-kind spans on branch: `9857`, or `92.98%` of the unique-span delta.
- The two cache-path additions alone are `4844 call_exec + 4919 dagql.publishResult =
  9763`, or `92.10%` of the unique-span delta and `81.24%` of all branch unique spans.

Branch unique span breakdown by `wcprof.op.kind`:

- `internal`: `4919` (`dagql.publishResult`)
- `call_exec`: `4844`
- `<none>`: `2161`
- `lazy`: `73`
- `exec_phase`: `14`
- `exec`: `7`

The high-volume introspection-style names Erik called out are absent on base/pre-emit
and present on the branch as `call_exec` spans:

- `Function.__withArg`: base `0`, pre-emit `0`, branch `366`
- `Function.sourceModuleName`: base `0`, pre-emit `0`, branch `294`
- `Function.args`: base `0`, pre-emit `0`, branch `291`
- `TypeDef.asScalar`: base `0`, pre-emit `0`, branch `98`
- `TypeDef.asInterface`: base `0`, pre-emit `0`, branch `98`
- `TypeDef.asInput`: base `0`, pre-emit `0`, branch `95`
- `TypeDef.asEnum`: base `0`, pre-emit `0`, branch `94`
- `TypeDef.asList`: base `0`, pre-emit `0`, branch `88`
- `TypeDef.asObject`: base `0`, pre-emit `0`, branch `34`

Other branch-only top `call_exec` names in the same family:

- `ObjectTypeDef.__withFunction`: `297`
- `Query.sourceMap`: `294`
- `FunctionArg.__withSourceMap`: `217`
- `Function.__withSourceMap`: `196`
- `Function.withArg`: `131`
- `Function.withDescription`: `112`
- `Query.__functionArgExact`: `103`
- `Query.__functionArg`: `94`
- `TypeDef.withOptional`: `90`
- `ObjectTypeDef.__withField`: `88`

There is also a branch increase in ordinary-looking `POST /query` spans (`12` on
base/pre-emit, `796` on branch, no `wcprof.op.kind`). I did not fully root-cause that
secondary increase in this pass. It is not needed to explain the BSP pressure: the
explicit wcprof spans alone account for almost the whole delta.

### Normal skip mechanism

Normal DAGQL telemetry is deliberately filtered before span creation:

- `dagql/objects.go:655`-`665` only calls `s.telemetry(ctx, req)` when telemetry exists
  and `!field.Spec.NoTelemetry`, then replaces `ctx` with the returned telemetry
  context.
- `core/telemetry.go:32`-`38` returns `NoopDone` when `dagql.IsSkipped(ctx)` is already
  set, and marks introspection calls with `dagql.WithSkip(ctx)` plus `NoopDone`.
- `core/telemetry.go:53`-`65` also applies the ordinary per-query
  `dagql.ShouldEmitTelemetry` de-dupe.
- `dagql/internal.go:32`-`42` defines the skip context bit.
- `core/telemetry.go:354`-`452` is the introspection/introspection-style predicate.
  It directly covers literal roots such as `__schema`, `currentTypeDefs`,
  `currentModule`, `currentFunctionCall`, `function`, `typeDef`, `sourceMap`,
  `__function`, `__functionArg`, and related private roots
  (`core/telemetry.go:362`-`387`). It also suppresses high-volume receiver families:
  `Function.withArg/withSourceMap/withDescription`, `Function.__*`,
  `TypeDef.with*`, `TypeDef.__*`, and `__*` fields on `FunctionArg`,
  `ObjectTypeDef`, `InterfaceTypeDef`, `InputTypeDef`, `FieldTypeDef`, `ListTypeDef`,
  `EnumTypeDef`, and `EnumMemberTypeDef`
  (`core/telemetry.go:401`-`445`).

Some measured names, such as `TypeDef.as*` and `Function.args`, are not directly listed
in the receiver-specific switch. They are still suppressed in the normal path because
they run beneath an introspection root where `AroundFunc` has already set
`dagql.WithSkip(ctx)`. The base and pre-emit captures confirm this empirically: those
names emitted zero spans on the same workload.

### Wcprof emit bypasses the skip

The branch's wcprof OTel emission is in the cache path and is gated only by whether the
current context has a recording span:

- `dagql/cache.go:3713`-`3734` creates the detached call context and starts
  `beginOTelCallExec(...)` when `OTelProfActive(callCtx)` is true.
- `dagql/otelprof_hooks.go:41`-`43` defines `OTelProfActive` as only
  `trace.SpanFromContext(ctx).IsRecording()`.
- `dagql/otelprof_hooks.go:58`-`65` starts the `call_exec` span with
  `telemetry.Passthrough()`, `wcprof.op.kind=call_exec`, and the digest. It does not
  consult `dagql.IsSkipped`, `FieldSpec.NoTelemetry`, `introspectionInfo`, or
  `ShouldEmitTelemetry`.
- `dagql/cache.go:3999`-`4029` starts `dagql.publishResult` whenever the `call_exec`
  span context is valid. That path also has no skip/de-dupe predicate.
- `dagql/cache.go:3943`-`3949` is explicit about the intent to emit wait edges from the
  cache layer because a telemetry-suppressed caller never enters `AroundFunc`; the wait
  emission itself is not the main span-count driver, but it shows this path is
  intentionally outside the normal telemetry suppression point.

This means the branch emits wcprof spans for every executed cache miss under a recording
ancestor, including calls for which normal telemetry deliberately returns `NoopDone`.
The measured local trace is exactly that shape: base/pre-emit emit no spans for the
introspection-style calls, while the branch emits hundreds of `call_exec` spans for
them plus one `dagql.publishResult` for nearly every `call_exec`.

### Verdict

There is a real telemetry-volume increase versus both base `b442cd2533` and the
pre-wcprof-emit commit `71b69f1f16`: about `8.5x` unique spans on this warmed workload.
The increase is not inherent to the workload and not explained by live double-emission.
It is overwhelmingly driven by the new wcprof OTel emit spans.

The introspection/introspection-style telemetry skip is not honored by the wcprof
`call_exec` / `publishResult` emission path. That is a showstopper under Erik's stated
criterion: the branch is publishing spans for high-volume calls the engine's ordinary
telemetry intentionally suppresses. The immediate principled fix is in emission, not in
analysis or BSP sizing: make the wcprof OTel cache-path emission share the same
"should this call produce telemetry?" decision as normal DAGQL telemetry, or an exactly
equivalent predicate, before starting `call_exec`, `publishResult`, and any associated
wait edge for a suppressed caller.
