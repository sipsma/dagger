# wcprof x OTel publishResult forensics - Codex scratch pass

Source references below are against implementation commit `4585bf413d` unless
otherwise noted. I treated the governing principle from the brief as fixed:
analysis is a rational function of faithful data; it must not infer or
compensate for missing causality.

## Verdict

**Data is genuinely missing from the local trace material.** This is not a
loader dedupe bug, not a cross-trace parent resolution bug, and not an
emit-parentless bug for `dagql.publishResult`.

For `/tmp/otel-exec.jsonl`, after live-start/live-end dedupe exactly as the
loader does, there are 330 `dagql.publishResult` spans with non-empty
`parentId` whose parent span id never appears anywhere in the raw JSONL. Example
raw facts:

| publishResult span | recorded parentId | raw publishResult rows | raw parent rows |
| --- | --- | ---: | ---: |
| `f7cb8f7c790e54de` | `f5dd244d963a9c0b` | 1 | 0 |
| `ccf600a825b232d5` | `1f36b801219a87cd` | 1 | 0 |
| `a031e1c34683be68` | `f1354d9284e5e3e3` | 2 | 0 |
| `7a3622831cbdbc4e` | `ff88b667d806fc87` | 2 | 0 |

The exact first loss point is **not fully proven**. The strongest current
evidence pins the loss upstream of the CLI's local/Cloud exporters, at or before
the engine client telemetry DB/SSE stream. The likely culprit is the engine
per-client `LiveSpanProcessor` using the OpenTelemetry SDK's default dropping
`BatchSpanProcessor` queue, but that remains a hypothesis until that hop is
instrumented or the engine is run with a no-drop/large-queue provider.

## What Is Proven

**REAL issue - local captures contain orphaned causal parents.**

Measured captures:

| capture | raw span rows | unique spans | traces | roots | publishResult total | publishResult empty parent | publishResult orphan parent | call_exec total | call_exec with no publish child |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `/tmp/otel-exec.jsonl` | 22021 | 11162 | 1 | 1 | 4748 | 0 | 330 | 4993 | 575 |
| `/tmp/pubres-mod.jsonl` | 21297 | 10885 | 1 | 1 | 4783 | 0 | 370 | 4666 | 253 |
| `/tmp/pubres-clean.jsonl` | 21170 | 10801 | 1 | 1 | 4659 | 0 | 224 | 4779 | 344 |
| `/tmp/codex-default.jsonl` | 21014 | 10750 | 1 | 1 | 4647 | 0 | 400 | 4737 | 490 |
| `/tmp/codex-bigq.jsonl` | 20971 | 10729 | 1 | 1 | 4662 | 0 | 324 | 4703 | 365 |

This is the right framing: `parentId` is present, and the referenced span id is
absent. `hack/otlpdump/main.go:109-117` writes OTLP `span.ParentSpanId`
directly as JSON `parentId`; it does not synthesize or drop parent IDs.

The loader then does the expected mechanical thing: it resolves
`wcprof.parent ?? parentId`, looks up the referenced span in `opIDBySpan`, and
now counts an orphan if that id is non-empty but absent
(`engine/wcprof/wcotel/loader.go:299-310`). That is not inference; it is a
faithfulness check. The gate hard-fails the condition
(`engine/wcprof/wcotel/gate.go:132-140`).

Running the current analyzer on `/tmp/codex-bigq.jsonl` exits non-zero and
reports:

- `capture-loss: orphaned-parents=324`
- `wait-loss: unresolved-targets=2113`

So the current gate now catches this family. The unresolved wait targets show
this is not just a cosmetic `publishResult` root problem; missing `call_exec`
targets also break wait propagation.

**NOISE - not an emit-parentless publishResult bug.**

The source says `dagql.publishResult` is intentionally a child of `call_exec`
for native parity (`hack/designs/wcprof-otel-design.md:516-524`). The current
emit matches that shape:

- `call_exec` is started on `callCtx` at `dagql/cache.go:3731-3734`.
- Its `SpanContext` is stashed on `oc` before publication at
  `dagql/cache.go:3755-3758`.
- `publishResult` is started from `context.WithoutCancel(oc.sharedWorkCtx)` at
  `dagql/cache.go:4013-4018`.
- `beginOTelPublishResult` starts an ordinary child span from the context at
  `dagql/otelprof_hooks.go:68-83`.

All five measured captures had `publishResult empty parent = 0`. The missing
thing is the parent span record, not the parent id.

**NOISE - not loader mis-dedup or cross-trace confusion.**

The local captures above each contain one trace id. Live export double-emits most
spans, as the design expects (`hack/designs/wcprof-otel-design.md:896-898`):
for `/tmp/otel-exec.jsonl`, 10859 span IDs had two rows and 303 had one. The
missing parent ids have zero raw rows, so no dedupe policy can recover them.

## Pipeline Findings

The relevant pipeline has two distinct loss surfaces.

1. Engine session tracer provider -> per-client live processor -> client DB.
2. Client subscribes to `/v1/traces` from that DB -> CLI `EngineTrace`
   exporter -> CLI global processors -> local OTLP exporter and Cloud exporter.

The source supports that split:

- Engine session providers export to client DBs through
  `telemetry.NewLiveSpanProcessor(client.spanExporter)` at
  `engine/server/session.go:682-709`, and additionally to parent client DBs at
  `engine/server/session.go:730-736`.
- The client DB is append-only (`engine/clientdb/schema.sql:5-30`), and
  `InsertSpan` has no conflict/update path (`engine/clientdb/queries.sql.go:76-123`).
- The engine SSE endpoint pages rows by increasing DB id with limit 1000
  (`engine/server/telemetry.go:195-224`) and the loop continues until shutdown
  drains with no data (`engine/server/telemetry.go:629-667`).
- The CLI consumes `/v1/traces` and forwards the decoded spans to
  `c.Params.EngineTrace.ExportSpans` (`engine/client/client.go:950-982`).
- The CLI telemetry proxy forwards OTLP child-process spans into the same global
  span processors (`internal/cmd/dagger/run.go:229-248`).

The OpenTelemetry SDK processor in use is lossy by default:

- `github.com/dagger/otel-go` `NewLiveSpanProcessor` wraps the exporter in
  `sdktrace.NewBatchSpanProcessor` with only a shorter timeout
  (`otel-go/live.go:15-21`).
- The SDK default queue is 2048 and "drops the spans" when full unless
  `BlockOnQueueFull` is set (`otel/sdk/trace/batch_span_processor.go:20-61`).
- The non-blocking enqueue path increments an internal dropped counter and
  otherwise discards (`otel/sdk/trace/batch_span_processor.go:415-430`).

The running dev engine did not have any `OTEL_BSP_*` queue env configured
(`docker inspect dagger-engine.dev`; container has only Dagger manifest/env
entries and has been up for 21h). My high-queue experiment changed the CLI-side
environment, not that already-running engine provider. That is why it is strong
but not complete evidence for engine-side queue loss.

## Cloud Reconciliation

The prior "Cloud is complete" and "Cloud is just a rolled-up view" assumptions
are both too strong.

I queried the same GraphQL subscription used by `dagger trace`
(`internal/cloud/trace.go:26-65`, `internal/cloud/trace.go:152-183`) with
Erik's credentials, without printing credentials. I also queried both
`root:true` and `root:false`; this client hardcodes `root:true`, but the
operation accepts the variable.

Results:

| trace | local unique | Cloud `root:true` unique | Cloud `root:false` unique | Cloud IDs not in local | Cloud orphan parent ids |
| --- | ---: | ---: | ---: | ---: | ---: |
| `/tmp/otel-exec` trace `6227...` | 11162 | 2369 | 2369 | 0 | 85 |
| fresh default trace `c972...` | 10750 | 2833 | 2833 | 0 | 132 |
| fresh high-queue trace `46e...` | 10729 | 10729 | 10729 | 0 | 324 |

Interpretation:

- `StreamSpans` is not a reliable proof of full Cloud trace fidelity. For two
  traces it returned a strict subset of the local IDs.
- The subset is not explained by the `root` flag; `root:true` and `root:false`
  returned identical counts in these queries.
- The subset is not parent-closed in my measurements; e.g. the `6227...` Cloud
  response had 85 parent ids absent from the returned set.
- The high-queue run is important: the same API returned exactly the local ID
  set. That argues against a fixed "rolled-up view" explanation and suggests a
  downstream export/ingest loss mode on the default runs.

Most likely there are two phenomena:

1. **Upstream engine/client-stream loss:** local and Cloud both miss the same
   `publishResult` parents on the high-queue run. This is at or before the spans
   enter the CLI global processors.
2. **Downstream Cloud export/ingest loss:** on default runs, Cloud has a strict
   subset of local. Raising the CLI-side BSP queue made Cloud match local for the
   fresh run, so the CLI Cloud exporter queue is a plausible mechanism. I did
   not prove whether the exact downstream loss occurs in that queue, HTTP export,
   or Cloud ingest.

I found no full-fidelity alternate Cloud read path in the checked-in client. The
`dagger trace` command uses `Client.StreamSpans` and converts those records back
to OTLP (`internal/cmd/dagger/trace.go:61-87`). GraphQL introspection is disabled
on the endpoint, so I could not discover a richer schema from this pass.

## Impact

This is a correctness blocker for trusting rankings from these captures.

If a parent span is missing, the loader would otherwise surface the child as a
false root. That breaks the rational model: savings that should propagate
through the parent edge are lost. Missing `call_exec` wait targets are even more
directly harmful: the loader cannot compile the recorded wait as a join, so it
degrades to fixed delay. The new `OrphanedParents` and unresolved-target gates
are therefore the correct fail-closed posture.

This also means the design's Cloud round-trip requirement is still necessary and
not yet satisfied. The design explicitly requires fetching a Cloud trace and
proving links and compiled graph match a local dump
(`hack/designs/wcprof-otel-design.md:1132-1151`). The current `StreamSpans`
subscription is not proven suitable for that test.

## Principled Fix

Do not add loader inference, reparenting, chaining, or special handling for
`dagql.publishResult`. The data says a parent existed; the parent record is
absent. The rational response is to fail and fix the data path.

Concrete next steps:

1. Instrument the engine-side per-client span path at the hop boundary:
   count/export span IDs before `LiveSpanProcessor`, after its batch export into
   `clientSpans.ExportSpans`, and after `InsertSpan`. For a small diagnostic
   build, logging just span id/name/parent id for `call_exec` and
   `dagql.publishResult` is enough.
2. Run the same workload with the engine provider configured no-drop
   (`BlockOnQueueFull`) or with a large queue in the engine process itself. The
   current high-queue experiment only covered the CLI-side exporters.
3. Apply the same no-drop/large-queue posture to the CLI Cloud/local export leg
   or bypass the generic live BSP for profiling-critical trace export. The
   default Cloud subset behavior is unacceptable for the wcprof source.
4. Keep `OrphanedParents > 0`, unresolved wait targets, dropped wait links, and
   malformed wait timings as hard gate failures. These are faithfulness
   preconditions, not quality metrics.
5. For production, provide or identify a full-fidelity Cloud trace fetch API.
   `StreamSpans` as used by `dagger trace` is not sufficient until it can be
   shown to return the complete span/link set for large augmented traces.

## Remaining Uncertainty

I did not directly inspect the engine client DB for the fresh traces; the DB
location was not obvious in the running container. Therefore the exact first
loss point is still "at or before the engine client DB/SSE stream", not pinned to
one line of code.

The engine-side default `LiveSpanProcessor` queue is the leading suspect because
it is the only source-level lossy buffer on the shared upstream path that matches
the burst size and the observed symptoms. But it should be verified with the
instrumentation above before anyone states it as fact.

## Addendum: definitive local and Cloud experiments

I ran the requested minimal instrumentation in an isolated dev engine, not the
shared `dagger-engine.dev`.

Custom engine setup:

- Temporary engine source patch:
  - Registered a diagnostic span processor before the per-client live processor.
  - Logged only `call_exec` and `dagql.publishResult` span ids at
    `processor-start`, `processor-end`, and `clientSpans.ExportSpans`
    (`db-export`).
  - Added an env-gated `WCPROF_OTEL_NODROP=1` mode that replaces the engine
    per-client live processor's dropping SDK batch queue with
    `WithMaxQueueSize(65536)` + `WithBlocking()`.
  - Temporarily patched `toolchains/engine-dev/docker.go` only so custom engine
    names containing `wcprof` got diagnostics, names containing `nodrop` also
    got the no-drop queue, and custom names did not bind host port `6060`.
- Custom containers/images only:
  - `dagger-engine.codex-wcprof-default`
  - `dagger-engine.codex-wcprof-nodrop`
  - `localhost/dagger-engine.codex-wcprof:latest`
- Cleanup: both custom containers, their volumes, the custom image, and all
  temporary source edits were removed. The shared `dagger-engine.dev` was not
  restarted or removed.

### Q1: exact local loss site

**Proven: the local loss is inside the engine per-client live span processor,
before `clientSpans.ExportSpans`, before the client SQLite DB, before the CLI,
and before `otlpdump`.**

Relevant source path:

- The engine session installs `telemetry.NewLiveSpanProcessor(client.spanExporter)`
  for the client DB and parent DBs (`engine/server/session.go:682-709`,
  `engine/server/session.go:730-736`).
- `telemetry.NewLiveSpanProcessor` wraps its exporter in
  `sdktrace.NewBatchSpanProcessor` (`github.com/dagger/otel-go/live.go:15-21`).
- The SDK batch processor defaults to a 2048 queue and drops when full unless
  blocking is enabled (`go.opentelemetry.io/otel/sdk/trace/batch_span_processor.go:20-61`,
  `:415-430`).
- `clientSpans.ExportSpans` is the client DB insert boundary
  (`engine/server/telemetry.go:321-410`); the table is append-only
  (`engine/clientdb/schema.sql:5-30`) and is streamed by increasing row id
  (`engine/server/telemetry.go:195-224`, `:629-667`).

Default engine live processor, same burst workload:

| boundary / capture | unique `call_exec` | unique `dagql.publishResult` |
| --- | ---: | ---: |
| diagnostic `processor-start` | 16373 | 16373 |
| diagnostic `processor-end` | 16373 | 16373 |
| diagnostic `db-export` | 13270 | 13990 |
| local `otlpdump` deduped | 13270 | 13990 |

Local capture `/tmp/wcprof-default.jsonl`:

- raw span rows: 59885
- unique spans: 30844
- roots: 1
- orphaned parents: 1331
- `dagql.publishResult` total: 13990
- `dagql.publishResult` orphaned parents: 1331
- unresolved wait links: 3171
- structural gate: FAIL

This is the first divergent hop. The diagnostic processor observed all spans,
but the engine DB exporter did not. The `otlpdump` counts match `db-export`,
which means the loss is already present before the DB/SSE/CLI/local receiver
path.

No-drop engine live processor, same burst workload:

| boundary / capture | unique `call_exec` | unique `dagql.publishResult` |
| --- | ---: | ---: |
| diagnostic `processor-start` | 16367 | 16367 |
| diagnostic `processor-end` | 16367 | 16367 |
| diagnostic `db-export` | 16367 | 16367 |
| local `otlpdump` deduped | 16367 | 16367 |

Local capture `/tmp/wcprof-nodrop.jsonl`:

- raw span rows: 72634
- unique spans: 36317
- every span had exactly two live rows
- roots: 1
- orphaned parents: 0
- `dagql.publishResult` total: 16367
- `dagql.publishResult` orphaned parents: 0
- unresolved wait links: 0
- structural gate: PASS

The no-drop run closes the loop: the same instrumented engine code path, with
the live processor queue made blocking/large, preserves all `call_exec` and
`publishResult` spans through `clientSpans.ExportSpans` and the local capture.
The default run loses them between the diagnostic processor and `db-export`.
That is exactly the SDK batch queue inside the engine per-client
`LiveSpanProcessor`; not the loader, not SQLite, not the SSE stream, not the CLI,
and not `otlpdump`.

### Q2: Cloud read-view vs ingest/storage loss

**Proven: the Cloud `~1/5` result is not a `spansUpdated(root:true)` view filter.
For these traces it reflects spans missing from Cloud's stored/readable trace
rows. The backend read path is full-fidelity below 100k spans; the remaining
loss is on the client-to-Cloud export leg before Cloud storage.**

Backend code evidence:

- Cloud OTLP ingest converts every OTLP span in the request into a `db.Span` and
  appends it to the insert batch; there is no ingest sampling/filtering in this
  path (`api/otlp/traces.go:59-117`).
- The batch is inserted with `InsertSpansAsync` (`api/otlp/traces.go:145-154`),
  which prepares an insert into `telemetry_2024_02_28.otel_traces` and appends
  every span in the batch (`api/db/traces.go:2168-2278`).
- The trace table stores `SpanId`, `ParentSpanId`, attributes, links, and
  `RootSpan` as a materialized `ParentSpanId = ''` column
  (`api/schema/telemetry/00001_create_traces_table.sql:4-60`).
- `spansUpdated` calls `TraceQueries.BatchSpans` (`api/graph/trace.resolvers.go:316-320`).
- `BatchSpans` first counts stored rows in `otel_traces` and, when
  `spanCount < incrementalThreshold` (`100000`), selects all spans from
  `otel_traces` ordered by timestamp (`api/db/traces.go:605-621`). The partial
  priority/listen CTE is only used for traces at or above that threshold
  (`api/db/traces.go:618-747`).
- The non-subscription `trace { spans }` resolver also calls `TraceQueries.Spans`
  (`api/graph/trace.resolvers.go:385-388`), which selects all stored spans from
  `otel_traces` (`api/db/traces.go:563-570`).

Measured Cloud reads used `trace { spans { ... } }`, not only
`spansUpdated(root:true)`, so this tested the full backend read path available
through GraphQL.

Cloud vs local:

| trace | local deduped spans | Cloud `trace { spans }` | Cloud IDs not in local | local IDs missing from Cloud | Cloud orphan parent ids |
| --- | ---: | ---: | ---: | ---: | ---: |
| old `/tmp/otel-exec` `6227...` | 11162 | 2369 | 0 | 8793 | 65 |
| fresh default engine `7799...` | 30844 | 15654 | 0 | 15190 | 218 |
| fresh no-drop engine `2202...` | 36317 | 16678 | 0 | 19639 | 382 |
| no-drop engine + large CLI BSP queue `ccd7...` | 14998 | 14998 | 0 | 0 | 0 |

Interpretation:

- The Cloud result is a strict subset of local when the CLI Cloud exporter uses
  its normal live batch queue.
- `trace { spans }` returns the same kind of subset as `spansUpdated`, so this is
  not a `root:true` subscription view issue.
- The backend code says traces this size should read all rows stored in
  `otel_traces`; therefore the missing spans are not present in Cloud's stored
  readable trace rows.
- The same no-drop engine with `OTEL_BSP_MAX_QUEUE_SIZE=131072` and
  `OTEL_BSP_MAX_EXPORT_BATCH_SIZE=8192` in the CLI process produced exact
  Cloud/local parity: 14998/14998 spans, zero orphan parent ids.

That last run isolates the second loss to the CLI-to-Cloud exporter leg, before
Cloud storage. It is not backend ingest/read filtering. The most likely concrete
mechanism is the CLI process's Cloud live trace exporter using the same dropping
SDK batch queue class. Source path:

- CLI telemetry config appends Cloud span exporter to `LiveTraceExporters`
  (`internal/cmd/dagger/engine.go:312-328`).
- `telemetry.Init` wraps live exporters with `NewLiveSpanProcessor`
  (`github.com/dagger/otel-go/init.go:398-412`, `:427-436`).
- Cloud exporter itself just sends to `/v1/traces`
  (`engine/telemetry/cloud.go:79-92`, `:161`) and the `SpanHeartbeater`
  forwards received spans (`engine/telemetry/heartbeat.go:48-67`).

I could not run raw ClickHouse queries directly: there was no ClickHouse DSN in
the environment, no ClickHouse client installed, and AWS SSM access was blocked
by missing credentials. However, the backend GraphQL `trace { spans }` path is a
direct full read from `otel_traces FINAL` for these trace sizes, so it is enough
to distinguish "read-view filter" from "not stored/readable by Cloud".

### Updated root cause

There are two independent dropping queues:

1. **Engine per-client live processor** drops before the engine client DB.
   This creates local `otlpdump` orphan parents and unresolved wait targets.
   No-drop engine mode eliminates the local data loss and makes the structural
   gate pass.
2. **CLI Cloud live exporter processor** drops before Cloud ingest/storage.
   This makes Cloud store/read only a subset even when local `otlpdump` is
   complete. Raising the CLI BSP queue makes Cloud exactly match local.

### Principled fix after proof

For wcprof OTel data, both queues must be non-dropping or explicitly
backpressured:

- Engine per-client trace export to client DBs must not use a dropping live BSP
  for spans/links required by wcprof analysis.
- CLI Cloud trace export must not use a dropping live BSP for augmented traces,
  or the Cloud source will remain untrustworthy even when the local engine DB is
  complete.
- The loader/gate posture remains correct: `OrphanedParents > 0`,
  unresolved wait targets, malformed wait timings, and dropped wait links are
  hard faithfulness failures. No loader compensation should be added.

## Addendum: why the BSP drop appears now

Erik's volume hypothesis is correct. The current wcprof branch emits roughly
10x as many spans as base main / the pre-wcprof-emit commit on the module-load
workload that exposed the loss, and the increase is almost entirely the new
wcprof OTel spans in `dagql/cache.go`.

### Measurement setup

I compared three refs with the same workload:

```text
./bin/dagger --progress=plain call container from --address alpine with-exec --args 'sleep 1' stdout
```

The command intentionally follows the same CLI parse/schema-load path used by
the prior forensics runs. It fails after loading type definitions with
`unknown command "container"`, which is fine for this measurement because the
question is the high-volume schema/introspection burst before command parsing.

To avoid measuring the SDK queue's preservation bias, I used a temporary local
diagnostic engine build on each ref where the engine's per-client
`LiveSpanProcessor` used a large blocking queue. The CLI process also used:

```text
OTEL_BSP_MAX_QUEUE_SIZE=131072
OTEL_BSP_MAX_EXPORT_BATCH_SIZE=8192
OTEL_EXPORTER_OTLP_TRACES_LIVE=1
```

Custom engines were named `dagger-engine.wcprofvol-*`; `dagger-engine.dev` was
not restarted or deleted. The temporary diagnostic source edits were reverted
after measurement, and the custom containers/images/volumes were removed.

Measured captures:

| ref | capture | raw JSONL rows | deduped spans | `dag.call` spans | wcprof spans | wait links |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| base main `b442cd2533` | `/tmp/wcprofvol-main.jsonl` | 8605 | 3526 | 853 | 0 | 0 |
| pre-emit Chunk 1 `71b69f1f16` | `/tmp/wcprofvol-preemit.jsonl` | 8522 | 3526 | 853 | 0 | 0 |
| current branch `4585bf413d` | `/tmp/wcprofvol-branch.jsonl` | 74113 | 36318 | 853 | 33065 | 17019 |

Delta:

- Current branch vs main/pre-emit: `36318 - 3526 = 32792` additional deduped
  spans, a `10.30x` span-count increase.
- Raw live rows increase from `8605` to `74113`, an `8.61x` row-count increase.
  Live export double-emits span start/end rows, so every added span is roughly
  two queue entries.
- Ordinary call telemetry did not increase: `dag.call` stayed exactly `853` on
  all three refs. That is important: the normal `AroundFunc` telemetry skip is
  still working for ordinary call spans.

Current-branch wcprof span breakdown:

| `wcprof.op.kind` | spans |
| --- | ---: |
| `call_exec` | 16367 |
| `internal` (`dagql.publishResult`) | 16367 |
| `lazy` | 274 |
| `exec` | 19 |
| `exec_phase` | 38 |

So the volume increase is not primarily wait links or phase spans. It is the
two passthrough spans per executed cache miss added by Chunk 2:
`call_exec` plus `dagql.publishResult`.

### Introspection skip status

The normal telemetry skip mechanism is:

- `dagql.ObjectResult.call` invokes `s.telemetry` only when
  `!field.Spec.NoTelemetry`, then calls `cache.GetOrInitCall` regardless
  (`dagql/objects.go:655-678`).
- `FieldSpec.NoTelemetry` is explicitly an `AroundFunc` suppression flag
  (`dagql/objects.go:916-919`).
- `core.AroundFunc` immediately no-ops if `dagql.IsSkipped(ctx)` is already set
  (`core/telemetry.go:32-34`).
- If the current call is classified as introspection, `core.AroundFunc` returns
  `dagql.WithSkip(ctx)` and `NoopDone` (`core/telemetry.go:35-38`).
- `dagql.WithSkip` only sets a context value; it does not remove the current
  recording span from the context (`dagql/internal.go:32-42`).
- `introspectionInfo` classifies literal GraphQL/schema roots such as
  `__schema`, `function`, `typeDef`, `sourceMap`, and the `__*TypeDef` roots
  as introspection (`core/telemetry.go:354-387`).
- The same classifier suppresses high-volume `Function` / `TypeDef` builder and
  `__*` descendants when debug baggage is not set (`core/telemetry.go:401-444`).
- Repeated non-introspection call spans are also suppressed by
  `dagql.ShouldEmitTelemetry` (`dagql/telemetry.go:48-63`), called from
  `core.AroundFunc` (`core/telemetry.go:58-65`).

The wcprof OTel emit path bypasses those skips:

- `OTelProfActive` is only `trace.SpanFromContext(ctx).IsRecording()`
  (`dagql/otelprof_hooks.go:41-43`).
- `getOrInitCallInner` starts a `call_exec` span whenever `OTelProfActive` is
  true (`dagql/cache.go:3725-3734`). There is no check for
  `dagql.IsSkipped(ctx)`, `FieldSpec.NoTelemetry`, or
  `dagql.ShouldEmitTelemetry`.
- `beginOTelCallExec` marks the span as `ui.passthrough`, gives it
  `wcprof.op.kind=call_exec`, and stores only `dag.digest`
  (`dagql/otelprof_hooks.go:45-65`).
- `wait` then emits `dagql.publishResult` for every valid `execSpanCtx`
  (`dagql/cache.go:4013-4018`), and `beginOTelPublishResult` marks it
  `ui.passthrough` / `wcprof.op.kind=internal`
  (`dagql/otelprof_hooks.go:68-83`).
- The file comment says this is intentional: the profiler spans are "gated only
  on telemetry being active" and cost "two extra passthrough spans" per cache
  miss (`dagql/otelprof_hooks.go:22-28`). That is the exact decision now
  causing the volume increase.

Measured introspection-style counts:

| span name | main | pre-emit | current branch total | current branch wcprof | current branch `dag.call` |
| --- | ---: | ---: | ---: | ---: | ---: |
| `Query.sourceMap` | 0 | 0 | 1672 | 1672 | 0 |
| `ObjectTypeDef.__withFunction` | 0 | 0 | 1398 | 1398 | 0 |
| `Function.__withArg` | 0 | 0 | 1011 | 1011 | 0 |
| `Function.args` | 0 | 0 | 952 | 952 | 0 |
| `Function.sourceModuleName` | 0 | 0 | 952 | 952 | 0 |
| `Function.returnType` | 0 | 0 | 952 | 952 | 0 |
| `FunctionArg.typeDef` | 0 | 0 | 845 | 845 | 0 |
| `FunctionArg.__withSourceMap` | 0 | 0 | 774 | 774 | 0 |
| `TypeDef.asScalar` | 0 | 0 | 189 | 189 | 0 |
| `TypeDef.asList` | 0 | 0 | 189 | 189 | 0 |
| `TypeDef.asInput` | 0 | 0 | 189 | 189 | 0 |
| `TypeDef.asInterface` | 0 | 0 | 189 | 189 | 0 |
| `TypeDef.asEnum` | 0 | 0 | 189 | 189 | 0 |
| `TypeDef.asObject` | 0 | 0 | 189 | 189 | 0 |

The `dag.call=0` column is the key signal: these are not ordinary user-visible
call spans leaking through `AroundFunc`; they are the new wcprof `call_exec`
spans. A sampled branch `Function.__withArg` span had:

```text
name=Function.__withArg
attrs={
  dagger.io/dag.digest=xxh3:05565a22314b1b26,
  dagger.io/ui.passthrough=true,
  wcprof.op.kind=call_exec
}
```

and no `dagger.io/dag.call` attribute.

### Literal GraphQL introspection

I also ran a direct raw query:

```graphql
{ __schema { types { name } } }
```

with `dagger query -M` against the same custom engines.

| ref | raw rows | deduped spans | `dag.call` spans | wcprof spans | relevant spans |
| --- | ---: | ---: | ---: | ---: | --- |
| main | 53 | 15 | 0 | 0 | none |
| current branch | 43 | 17 | 0 | 2 | `Query.__schema`, `dagql.publishResult` |

The branch `Query.__schema` span was also a wcprof-only passthrough span:

```text
name=Query.__schema
attrs={
  dagger.io/dag.digest=xxh3:28d55c4028c9d8c0,
  dagger.io/ui.passthrough=true,
  wcprof.op.kind=call_exec
}
```

and its child `dagql.publishResult` had `wcprof.op.kind=internal`. Base main
correctly emitted no call span for the literal introspection query.

### Verdict

**Yes, the branch has a real telemetry-volume regression.** On the measured
schema-load workload, current branch emits `36318` spans where main/pre-emit
emit `3526`, a `10.30x` increase. The regression is self-inflicted by the
wcprof OTel cache-level emit path.

**The normal telemetry skip is still honored by normal call telemetry.** The
ordinary `dag.call` span count is unchanged at `853`, and skipped
introspection-style names have `dag.call=0`.

**The wcprof OTel emit path breaks the skip.** It starts `call_exec` and
`publishResult` from `dagql/cache.go` for every executed cache miss whenever a
recording span is present, even when `core.AroundFunc` has marked the subtree
skipped or when a field's normal telemetry is suppressed by `NoTelemetry`.

**Why the BSP drop appears now:** the branch adds `33065` wcprof spans and
`17019` wait links on this workload. With live export, the span increase alone
adds about `66130` extra start/end queue entries. The SDK live processor's
default queue is `2048` and drops when full unless blocking is enabled
(`go.opentelemetry.io/otel/sdk@v1.41.0/trace/batch_span_processor.go:20-61`,
`:417-431`), while `github.com/dagger/otel-go` `NewLiveSpanProcessor` uses that
dropping BSP with only a shorter timeout
(`github.com/dagger/otel-go@v1.43.1-0.20260515012101-af7cd0684887/live.go:15-21`).

### Disposition

This is a showstopper for the current emit posture. The data path still needs
non-dropping/backpressured export for faithful wcprof traces, but fixing queues
alone would preserve an avoidable telemetry explosion. The wcprof OTel emit must
honor the same suppression boundary as ordinary call telemetry for
introspection/introspection-style/no-telemetry calls, or otherwise provide a
separate, explicitly bounded profiling data channel that does not violate the
normal telemetry volume contract.
