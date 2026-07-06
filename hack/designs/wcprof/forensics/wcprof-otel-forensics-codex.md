# wcprof x OTel forensics: publishResult roots, local loss, Cloud subset

Status: independent fresh pass on 2026-06-26, repo HEAD `4585bf413d5ad09af918b39b0e2b62e95ad02006`.

Scope: investigation only. I made no source changes and no commits. I built a local CLI and created/used temporary artifacts under `/tmp`.

## Executive answer

Yes, data is missing from the local OTel span set. The right framing is not "publishResult emitted parentless." In both the old and fresh local captures, every deduped `dagql.publishResult` span has a non-empty `parentId`; the failure is that some named parent spans are absent from the span set.

For the fresh capture, that loss is already visible in the engine's per-client SQLite telemetry DB inside `dagger-engine.dev`, before the CLI-side OTLP exporter and before `hack/otlpdump`. That proves the local `otlpdump` HTTP receiver and the CLI external OTLP export are not the first place where these `publishResult` parents disappear.

The exact mechanism is still unproven. The remaining proven window is: after the engine creates the `call_exec` span in memory and before/in the engine telemetry DB export path (`LiveSpanProcessor`/SDK batch processor/client DB exporter). I did not find evidence that identifies which sub-hop drops it.

Cloud reconciliation: for the fresh run, Dagger Cloud read APIs I could reach returned 2,575 spans, a strict subset of the local capture's 11,452 deduped spans. Every Cloud span ID was present in local; 8,877 local span IDs were absent from Cloud. I did not find a full-fidelity Cloud read API. This does not prove whether Cloud storage ingested only the subset or whether the public GraphQL read surface is filtered/rolled up. It does prove the local capture is not the "wrong trace" and that `cloud.StreamSpans(root:true)`/`trace.spans` are not sufficient as the OTel source read path.

## Code contract checked

The design contract is zero-inference: the loader compiles spans into the same wcprof dump IR and uses causal parent = `wcprof.parent ?? parentId`; it never derives parents (`hack/designs/wcprof-otel-design.md:6-16`, `:68-83`). The call-exec wait target must be minted before the waiter-observable primitive is published (`hack/designs/wcprof-otel-design.md:361-389`), and `publishResult` is supposed to be a child of `call_exec` for native parity (`hack/designs/wcprof-otel-design.md:516-524`).

The current code matches that contract:

- `beginOTelCallExec` starts a passthrough span with `wcprof.op.kind=call_exec` and `dagger.io/dag.digest=callKey` (`dagql/otelprof_hooks.go:58-65`).
- `getOrInitCall` starts that span under `callsMu`, before publishing the `ongoingCall` (`dagql/cache.go:3689-3735`).
- `c.wait` emits wait links to `oc.execSpanCtx` after the waiter unblocks (`dagql/cache.go:3921-3958`).
- `publishResult` starts only when `oc.execSpanCtx.IsValid()` and uses `oc.sharedWorkCtx`, which carries the already-ended `call_exec` span (`dagql/cache.go:3999-4018`, `dagql/otelprof_hooks.go:68-83`).
- The loader's only parent rule is `wcprof.parent` override else `parentId`; it increments `OrphanedParents` when a non-empty parent span ID is absent (`engine/wcprof/wcotel/loader.go:299-307`, `:468-475`).
- The gate hard-fails unresolved wait targets and orphaned parents (`engine/wcprof/wcotel/gate.go:132-140`).

Therefore, a `dagql.publishResult` span with `parentId=<x>` but no span `<x>` in the input is incomplete data at the loader boundary. The code path also proves a valid `execSpanCtx` existed in memory for that `publishResult`; otherwise `beginOTelPublishResult` would not run.

## Telemetry pipeline checked

For engine spans, a per-client tracer provider writes live spans to the engine PubSub/client DB via `telemetry.NewLiveSpanProcessor(client.spanExporter)` (`engine/server/session.go:682-709`). Parent client DBs get additional live processors (`engine/server/session.go:730-747`). The PubSub trace subscription reads batches from the DB and emits OTLP JSON SSE (`engine/server/telemetry.go:197-233`). The CLI consumes `/v1/traces`, converts OTLP JSON back to spans, and forwards to `Params.EngineTrace.ExportSpans` (`engine/client/client.go:950-979`); CLI params wire that forwarder to process-wide `telemetry.SpanProcessors` (`internal/cmd/dagger/engine.go:167-169`).

The live processor sends a start snapshot by calling the underlying batch processor's `OnEnd` from `OnStart` (`github.com/dagger/otel-go@v1.43.1-0.20260515012101-af7cd0684887/live.go:15-30`). That explains live duplicate records: one start record and one end record per span, plus possible live updates.

Cloud export is configured separately as a live trace exporter: `engineTelemetryConfig` appends `ConfiguredCloudExporters` when shared exporters are enabled (`internal/cmd/dagger/engine.go:312-328`). The Cloud exporter is OTLP HTTP to `/v1/traces` and is wrapped in `SpanHeartbeater` (`engine/telemetry/cloud.go:79-92`, `:161-164`; `engine/telemetry/heartbeat.go:48-68`).

`hack/otlpdump` is a direct OTLP/protobuf HTTP receiver. Its trace handler decodes every received span and writes one JSON record with `traceId`, `spanId`, `parentId`, attrs, links, and dropped-link counts (`hack/otlpdump/main.go:96-158`). It does not roll up, sample, or dedupe.

Cloud read code used by the CLI trace path is `spansUpdated(org, traceId, root, before, after, listen)` with `root:true` and `SpanProps` (`internal/cloud/trace.go:26-65`, `:154-170`).

## Local capture measurements

I re-ran the analyzer and independent jq checks over the old shared capture and a fresh capture.

Old shared capture: `/tmp/otel-exec.jsonl`

```
raw span records:     22021
deduped span IDs:     11162
trace ID:             6227b3d37f72937685e4221997bbcc4d
publishResult spans:   4748
publish empty parent:     0
publish parent absent:  330
all orphaned parents:   330
call_exec spans:       4993
call_execs without publishResult child: 575
analyzer gate: FAIL, orphaned-parents=330, unresolved-targets=0
```

Fresh capture command shape:

```
go build -o ./bin/dagger ./cmd/dagger
go run ./hack/otlpdump -addr 127.0.0.1:43181 -out /tmp/codex-fresh-exec.jsonl
env OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:43181 \
    OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:43181/v1/logs \
    OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://127.0.0.1:43181/v1/metrics \
    OTEL_EXPORTER_OTLP_TRACES_LIVE=1 \
    ./hack/with-dev ./bin/dagger --progress=plain \
      -c 'container | from alpine | with-exec sleep 3 | stdout'
```

Fresh run trace: `766b9c56181bf287cf00febbeaaec0fe`.

Fresh local capture: `/tmp/codex-fresh-exec.jsonl`

```
raw span records:     22201
deduped span IDs:     11452
trace ID:             766b9c56181bf287cf00febbeaaec0fe
publishResult spans:   5048
publish empty parent:     0
publish parent absent:  537
all orphaned parents:   537
call_exec spans:       4915
call_execs without publishResult child: 404
analyzer gate: FAIL, orphaned-parents=537, unresolved-targets=1985
```

Fresh wait-link target loss:

```
wait edges:       7128
missing targets:  1985
missing by reason:
  call_exec:      1982
  singleflight:      3
missing waiter name:
  POST /query:    1985
```

So there are two observed incompleteness modes in the fresh local trace:

- `dagql.publishResult` spans whose parent ID names an absent span.
- Wait links, mostly `call_exec`, whose target span ID is absent.

The old `/tmp/otel-exec.jsonl` had the first mode but not the second, which explains why an unresolved-wait-target-only gate was insufficient. The current `OrphanedParents` gate catches the old case.

Concrete fresh orphan in local capture:

```
spanId:   0100d7cf573db8d0
name:     dagql.publishResult
parentId: 94b57ae35fb8aca0
startNs:  1782516841940413202
endNs:    1782516841959024673
attrs:    {"dagger.io/ui.passthrough":true,"wcprof.op.kind":"internal"}
```

In `/tmp/codex-fresh-exec.jsonl`, the child row exists and no row with `spanId=94b57ae35fb8aca0` exists.

## Engine DB boundary

I queried the dev engine container's per-client telemetry DB directly:

```
docker exec dagger-engine.dev sqlite3 \
  /var/lib/dagger/worker/clientdbs/yk1zgstbr53eqvnwye15fa0d7.db \
  "select count(*), count(distinct span_id)
     from spans
    where trace_id='766b9c56181bf287cf00febbeaaec0fe';"

22151|11427
```

That DB has slightly fewer spans than local `/tmp/codex-fresh-exec.jsonl` because local also includes CLI-side spans, but it is the pre-CLI-export persistent boundary for engine spans.

The same concrete orphan child is already in the engine DB:

```
0100d7cf573db8d0|94b57ae35fb8aca0|dagql.publishResult|1782516841940413202|1782516841959024673
```

Cross-checking every client DB in `dagger-engine.dev` for the parent ID found zero rows:

```
/var/lib/dagger/worker/clientdbs/ynxm37egyxm70sh9q8dit9jwp.db parent_span_rows=0
/var/lib/dagger/worker/clientdbs/hyiwfliccwteuaxe86nen25ha.db parent_span_rows=0
/var/lib/dagger/worker/clientdbs/vbkwdmc9l0hw8g3wsafiz9cta.db parent_span_rows=0
/var/lib/dagger/worker/clientdbs/ctnwhnklxj89h40goxuiu5dli.db parent_span_rows=0
/var/lib/dagger/worker/clientdbs/yk1zgstbr53eqvnwye15fa0d7.db parent_span_rows=0
/var/lib/dagger/worker/clientdbs/hh4p23kh61c8meafr14uhnq18.db parent_span_rows=0
/var/lib/dagger/worker/clientdbs/5z7ppv2q5n4g3zs3ik3mfiwf5.db parent_span_rows=0
```

I also exported the trace rows from that DB to `/tmp/codex-fresh-engine-db-spans.json` and deduped by `span_id`/max `end_time`:

```
engine DB raw records:        22151
engine DB deduped spans:      11427
publishResult spans:           5048
publish empty parent:             0
publish parent absent:          537
all orphaned parents:           549
  dagql.publishResult:          537
  POST /query:                   12
call_exec spans:               4915
```

The extra 12 `POST /query` DB orphans are consistent with engine DB lacking some CLI/root-side parent spans; they are not the publishResult issue. The important point is that all 537 fresh `publishResult` missing-parent cases are already present at the engine DB boundary.

This rules out these as the first loss point for the publishResult parent spans:

- CLI `/v1/traces` SSE consumption.
- CLI `Params.EngineTrace` forwarding to process-wide exporters.
- The OTLP HTTP transport from CLI to local `otlpdump`.
- `hack/otlpdump` decoding/writing.

It does not rule out loss inside the engine-side OTel SDK processor/exporter path before the DB, nor loss inside `clientSpans.ExportSpans` before insert. It also does not identify whether the parent span was dropped at start export, end export, both, or never reached the processor.

## Cloud measurements

I used the local Dagger credentials and org config but did not print the token.

For fresh trace `766b9c56181bf287cf00febbeaaec0fe`, I queried:

- `spansUpdated(root:true)`, matching `internal/cloud/trace.go`.
- `spansUpdated(root:false)`.
- `spansUpdated` with `listen` populated from the already returned IDs.
- `spansUpdated` across timestamp `before`/`after` windows.
- `trace(org,id){ spans { ...SpanProps... } }`.

All usable list paths converged on the same 2,575 span IDs. GraphQL schema introspection was disabled. I did not find a full-fidelity query returning the 11k span set.

Cloud span-set summary from `/tmp/codex-cloud-trace-spans.spans.jsonl`:

```
Cloud spans returned:      2575
trace ID:                 766b9c56181bf287cf00febbeaaec0fe
publishResult spans:       901
publish empty parent:        0
publish parent absent:      10
all orphaned parents:      163
call_exec spans:           905
call_execs without publishResult child: 14
partial=true spans:          0
true roots returned:         1
sum(childCount):          2411
```

`sum(childCount)=2411`, and `2575 - 1 true root - 163 orphaned-parent spans = 2411`, so the returned view's child counts are internally consistent with the returned parent-child edges. That does not prove Cloud storage is complete; it only proves the returned subset is self-consistent for the edges it returns.

Local-vs-Cloud ID set for the same fresh trace:

```
local deduped span IDs: 11452
cloud span IDs:         2575
intersection:           2575
cloud_not_local:           0
local_not_cloud:        8877
```

Representative deduped local vs Cloud counts by span name:

```
dagql.publishResult          local=5048  cloud=901  ratio=0.178
Function.__withArg           local=366   cloud=61   ratio=0.167
Function.args                local=325   cloud=7    ratio=0.022
Function.sourceModuleName    local=319   cloud=2    ratio=0.006
Host.directory               local=124   cloud=74   ratio=0.597
ModuleSource.asModule        local=102   cloud=61   ratio=0.598
TypeDef.asEnum               local=95    cloud=0
TypeDef.asInput              local=99    cloud=0
TypeDef.asInterface          local=95    cloud=0
TypeDef.asList               local=101   cloud=0
TypeDef.asObject             local=44    cloud=0
TypeDef.asScalar             local=108   cloud=0
```

This is not the exact prior reported number (2,784 vs 11,033); it is the same qualitative finding on a fresh trace: Cloud read APIs return a strict subset. I did not reproduce the prior "Cloud subset has 0 orphans" claim. In this fresh trace, the returned Cloud subset still has 163 spans whose non-empty `parentId` is absent from the returned set, including 10 `dagql.publishResult` spans.

The Cloud result therefore cannot be treated as a full-fidelity source for wcprof OTel analysis. It may be:

- a filtered/rolled-up GraphQL read view over a fuller store, or
- actual upstream ingest/storage loss, or
- both.

I do not have enough evidence to distinguish those without Cloud server-side raw store access or an explicitly documented full-fidelity trace read API.

## Prior claims verified or rejected

- "publishResult emitted parentless": rejected for the checked captures. Old and fresh local captures have `publish_empty_parent=0`; Cloud returned subset also has `publish_empty_parent=0`.
- "parentId set but parent span absent": verified. Old local: 330; fresh local: 537; fresh engine DB: 537 `dagql.publishResult` cases.
- "loss is engine to otlpdump HTTP export choking on the burst": not supported, and for the concrete fresh publishResult parent loss it is too late in the pipeline. The engine DB already lacks the parent span.
- "unresolved wait target gate alone catches this": rejected. Old `/tmp/otel-exec.jsonl` had 330 orphaned parents and 0 unresolved wait targets.
- "Cloud read path is complete and local is the lossy one": not verified. For the fresh trace, Cloud read returned a strict subset of local and all Cloud IDs were present locally.
- "Cloud subset is an internally complete TUI rollup": not reproduced. The fresh Cloud subset has orphaned parent IDs against the returned set.

## Impact

The OTel source is not faithful for these captures.

Local captures fail hard structural validation for the right reason. A rational analyzer should not compensate for these missing parent/target spans. The visible symptoms are not merely cosmetic:

- Orphaned `publishResult` spans become false roots, so counterfactual savings that should cross their parent edge cannot propagate.
- Missing wait-link targets turn joins into fixed delays, losing counterfactual propagation to the target class.
- `baseline == recorded makespan` is not sufficient proof of faithfulness; false leaf roots can replay to the same end time while still producing wrong multi-root what-if rankings.

For Cloud, the production OTel-source goal is blocked until a full-fidelity Cloud trace read path is proven or built. The currently reachable GraphQL surfaces returned only 2,575 of 11,452 local deduped span IDs for the fresh run.

Native wcprof itself is not implicated by this evidence; the issue is in the OTel telemetry source/path.

## Remaining uncertainty

The exact local mechanism is unproven. The evidence narrows it to this window:

```
beginOTelCallExec creates call_exec span in memory
  -> OTel SDK span processor/export path for the engine tracer provider
  -> clientSpans.ExportSpans serialization/insertion
  -> engine client DB
```

The fresh DB evidence proves the parent is absent by the engine DB boundary. It does not prove whether:

- the span never reached `LiveSpanProcessor.OnStart`;
- the underlying SDK batch processor dropped its start/end export;
- `clientSpans.ExportSpans` skipped it due serialization/export error;
- DB insertion lost it;
- a malformed/bad span ID was stored somewhere else; or
- another processor interaction is involved.

I saw no evidence that `dagger.io/ui.passthrough` is a Cloud/local export filter. It is just an attribute in all checked raw paths.

Cloud storage vs Cloud read-view remains unproven. Public/client-side probing found only subset-returning list APIs. Server-side Cloud raw ingest/store inspection is required to decide whether Cloud stored all 11k spans and the GraphQL API filters them, or Cloud ingested/stored only the 2.5k subset.

## What is needed to prove the exact mechanism

Do a temporary dev-only span-ID accounting pass for one trace. Do not rely on logs alone.

Minimum checkpoints:

1. Immediately after `beginOTelCallExec`: record `traceID`, `spanID`, call key/class.
2. In a wrapper around the engine tracer provider's span processors: record `OnStart` and `OnEnd` span IDs before the SDK batch processor.
3. At `clientSpans.ExportSpans`: record every span ID received, plus errors from attribute/event/link/scope/resource serialization.
4. Immediately after DB insert: record inserted span IDs/counts.
5. On CLI `/v1/traces` consumption and external OTLP export: record forwarded span IDs only to confirm the already-proven downstream path.

For the concrete fresh case, the accounting should answer where `94b57ae35fb8aca0` disappears.

For Cloud, the necessary proof is either:

- a Cloud-side raw-store query for trace `766b9c56181bf287cf00febbeaaec0fe`, compared by span ID to local `/tmp/codex-fresh-exec.jsonl`; or
- a documented full-fidelity API endpoint/query that returns all stored spans with attrs, links, parent IDs, and dropped counts.

## Principled fix

Do not infer missing parents or synthesize waits in the loader/replay.

If the local mechanism is SDK/batch/drop behavior before the engine DB, fix the engine telemetry export path so the per-client DB and Cloud exporter use a lossless path for trace spans needed by the OTel wcprof source. That likely means replacing the lossy live/batch hop for these internal sinks with a processor/exporter that backpressures or reports hard errors instead of silently losing span IDs, and adding counters/tests that fail on missing exported IDs. The exact patch should wait for the checkpoint accounting above.

If `clientSpans.ExportSpans` serialization or DB insert is the drop site, fix that path and make skipped spans observable as hard errors for trace capture.

If Cloud storage is complete but GraphQL is a TUI/subset view, the OTel source must use a full-fidelity Cloud API, not `StreamSpans(root:true)`. If Cloud ingest/storage is lossy, fix Cloud ingest/storage before using Cloud as the OTel source.

## Required closing answers

Is data missing?

Yes. Proven for local captures and for the engine DB boundary. `dagql.publishResult` spans have non-empty `parentId` values that name absent spans. Fresh local: 537 such `publishResult` spans. Fresh engine DB: the same 537.

Where is it missing?

The publishResult parent span is absent by the engine per-client SQLite DB boundary inside `dagger-engine.dev`. Therefore the first loss is upstream of CLI OTLP export and upstream of `hack/otlpdump`. The remaining unproven window is engine in-memory span creation to SDK/live processor/client DB export/insert.

Exact mechanism?

Unproven. The next proof requires span-ID accounting at `beginOTelCallExec`, processor `OnStart`/`OnEnd`, `clientSpans.ExportSpans`, and DB insert. For the concrete fresh orphan, trace where parent span ID `94b57ae35fb8aca0` disappears.

Cloud-vs-local reconciliation?

For the fresh run, Cloud read APIs returned a strict subset of local: 2,575 Cloud IDs, all present in local, versus 11,452 local deduped IDs. I found no full-fidelity Cloud read API. This may be a rolled-up/filtered read view or genuine Cloud ingest/storage loss; client-side evidence cannot distinguish. It is not evidence that the local capture is the wrong trace.

True impact on the OTel source?

The OTel source is currently unfaithful for these traces. The loader/gate is correct to fail. Counterfactual rankings from these incomplete span sets should not be trusted.

Principled fix?

Fix the data path, not the model. Prove and repair the local drop site. For Cloud, provide/prove a full-fidelity read path or fix Cloud ingest/storage. Keep the loader mechanical and keep hard structural gates for orphaned parents, unresolved waits, and dropped links.

## 2026-06-26 follow-up: exact drop site and Cloud reconciliation

This pass added temporary dev-only span-ID accounting and then reverted it. The diagnostic was gated by `DAGGER_WCPROF_OTEL_DIAG` and wrote JSONL from four hops:

- `beginOTelCallExec` and `beginOTelPublishResult` at the emission points (`dagql/otelprof_hooks.go:58-65`, `dagql/otelprof_hooks.go:76-83`).
- A temporary engine `sdktrace.SpanProcessor` registered after `dagql.NewWcprofLazyParentProcessor()` and before the per-client `telemetry.NewLiveSpanProcessor` (`engine/server/session.go:694-709`).
- Entry to `clientSpans.ExportSpans`, serialization skip/error points, and post-`db.InsertSpan` success/error (`engine/server/telemetry.go:321-365`, `engine/server/telemetry.go:399-410`).
- The actual engine client SQLite DB.

The temporary code is not left in the tree. Build verification after reverting: `go build ./dagql ./engine/server ./engine/telemetry`.

### Local default-queue run

Command shape:

```
docker run ... -e DAGGER_WCPROF_OTEL_DIAG=/var/lib/dagger/wcprof-diag-default.jsonl localhost/dagger-engine.dev --extra-debug --debugaddr=0.0.0.0:6060
env OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:43183 ... ./hack/with-dev ./bin/dagger --progress=plain -c 'container | from alpine | with-exec sleep 3 | stdout'
```

Trace: `fc3f33167e9018555a11589b45e9014b`.

Engine DB boundary: main client DB `jm5bj315wicwge9ysdl1x2g5w.db` had 56,261 span rows and 28,618 distinct span IDs for the trace. Deduped DB had 12,184 `dagql.publishResult` spans, 599 `dagql.publishResult` spans whose non-empty parent ID was absent, and 825 total absent-parent spans.

Diagnostic unique ID counts for the same trace:

```
begin_call_exec                16592
begin_publish_result           16592
processor_on_start             36796
processor_on_end               36796
export_span_received           28618
db_insert_ok                   28618
begin_call_exec missing start      0
begin_call_exec missing end        0
begin_call_exec missing export  3610
begin_call_exec missing insert  3610
begin_publish missing export    4408
begin_publish missing insert    4408
```

Concrete orphan: `dagql.publishResult` child `65ba7de3cb0e041f` named parent `d1551deea8b8f122`. The parent had `begin_call_exec`, `processor_on_start`, and `processor_on_end`; it had no `export_span_received` and no `db_insert_ok`. The child had `begin_publish_result`, `processor_on_start`, `processor_on_end`, `export_span_received`, and `db_insert_ok`.

Therefore the local loss is not in `beginOTelCallExec`, not in span processor `OnStart`/`OnEnd` before the exporter, not in `clientSpans.ExportSpans` serialization, not in SQLite insert, not in CLI OTLP export, and not in `hack/otlpdump`. The first lost hop is between the pre-BSP processor and `clientSpans.ExportSpans`.

That window is the SDK `BatchSpanProcessor` inside Dagger's live span processor. Dagger's `telemetry.NewLiveSpanProcessor` wraps the exporter in `sdktrace.NewBatchSpanProcessor` (`github.com/dagger/otel-go .../live.go:15-21`) and sends a live start snapshot through the underlying BSP from `OnStart` (`.../live.go:25-30`). The OTel SDK default queue is 2,048 spans (`go.opentelemetry.io/otel/sdk@v1.44.0/trace/batch_span_processor.go:20-28`), documents that a full queue drops spans unless `BlockOnQueueFull` is set (`.../batch_span_processor.go:35-39`), sizes the queue from env (`.../batch_span_processor.go:90-113`), and uses `enqueueDrop` when not blocking (`.../batch_span_processor.go:392-398`). The engine per-client SQLite exporter is installed under that live processor at `engine/server/session.go:682-709`; `clientSpans.ExportSpans` is the DB exporter at `engine/server/telemetry.go:314-365`.

### Local no-drop confirmation

Engine-only large queue:

```
docker run ... \
  -e DAGGER_WCPROF_OTEL_DIAG=/var/lib/dagger/wcprof-diag-largeq.jsonl \
  -e OTEL_BSP_MAX_QUEUE_SIZE=200000 \
  -e OTEL_BSP_MAX_EXPORT_BATCH_SIZE=20000 \
  localhost/dagger-engine.dev --extra-debug --debugaddr=0.0.0.0:6060
./hack/with-dev ./bin/dagger --progress=plain -c 'container | from alpine | with-exec sleep 3 | stdout'
```

Trace: `087fcefc0c6ae1edd2e7089b25fb6ed1`.

Diagnostic unique ID counts:

```
begin_call_exec                16589
begin_publish_result           16589
processor_on_start             36790
processor_on_end               36790
export_span_received           36809
db_insert_ok                   36809
begin_call_exec missing export     0
begin_call_exec missing insert     0
begin_publish missing export       0
begin_publish missing insert       0
```

Main engine DB `ubw8uskx4jrfs9orc2699i65i.db` had 73,618 rows and 36,809 distinct span IDs. Deduped DB had 16,589 `dagql.publishResult` spans and 0 `dagql.publishResult` absent-parent cases. There were still 12 total absent-parent spans, all outside the `dagql.publishResult` issue.

This proves the exact local mechanism: engine-side SDK BSP queue drop in the per-client `telemetry.NewLiveSpanProcessor` before the SQLite exporter.

### Cloud code path

The Cloud path for a shared dev engine is downstream of the engine DB, not an independent direct engine exporter for engine spans:

- The CLI sends `Params.EngineTrace = telemetry.SpanForwarder{Processors: telemetry.SpanProcessors}` to the engine client params (`internal/cmd/dagger/engine.go:165-169`).
- The CLI subscribes to engine telemetry when `EngineTrace` is set (`engine/client/client.go:536-543`).
- The engine `GET /v1/traces` subscription reads rows from the client SQLite DB with `db.SelectSpansSince` and marshals them back as OTLP JSON (`engine/server/telemetry.go:197-230`, `engine/server/telemetry.go:666-725`).
- The CLI consumes those SSE batches, unmarshals to spans, and calls `c.Params.EngineTrace.ExportSpans` (`engine/client/client.go:956-980`).
- `SpanForwarder.ExportSpans` calls every CLI span processor's `OnStart` or `OnEnd` (`github.com/dagger/otel-go .../exporters.go:28-39`).
- The Cloud exporter is an OTLP HTTP exporter to `/v1/traces` (`engine/telemetry/cloud.go:68-92`), wrapped as a live trace exporter by `telemetry.Init` (`github.com/dagger/otel-go .../init.go:425-429`) and therefore also behind an SDK BSP queue.

Implication: spans dropped before the engine DB cannot be present in Cloud for this mode. I checked the default trace: all 3,610 `begin_call_exec` IDs that reached pre-BSP `processor_on_end` but missed engine DB insert were absent from Cloud `trace.spans`.

### Cloud backend read/store code

Backend repo was readable at `/home/sipsma/.tailcall/worktrees/sipsma-dagger.io-1311b0c59e24/dagger-io-backend-checkout-b5a08256-1d28c96b`.

Cloud OTLP ingest code does not show a sampler/filter in the trace handler. `api/otlp/traces.go:59-117` iterates every received OTLP span into the `batch`; `api/otlp/traces.go:145-154` calls `InsertSpansAsync` and returns HTTP 500 on error.

Storage is the `telemetry_2024_02_28.otel_traces` table. It stores rows keyed by `(OrgId, TraceId, RootSpan, SpanId)` in a `ReplacingMergeTree(UpdateTime)` (`api/schema/telemetry/00001_create_traces_table.sql:4-60`). `InsertSpansAsync` appends every span to a ClickHouse batch and sends it (`api/db/traces.go:2045-2061`, `api/db/traces.go:2168-2278`). Caveat: inserts use `async_insert=1` and `wait_for_async_insert=0` (`api/db/traces.go:2168-2172`), so direct ClickHouse querying would be the strongest durability proof. I could not do that from this shell: there is no ClickHouse client, repo `.env` files did not contain ClickHouse connection variables, and the available AWS SSO profile was expired.

The read path is not a root-rollup for these traces:

- `trace.spans` calls `r.db.Trace.Spans` (`api/graph/trace.resolvers.go:385-388`), which calls `selectSpans(...).OrderBy("Timestamp ASC")` (`api/db/traces.go:563-570`).
- `selectSpans` reads from `TelemetryTracesTable + " traces FINAL"` with only org/trace filters unless the caller requested extra joins (`api/db/traces.go:1045-1147`).
- `spansUpdated(root:true)` calls `BatchSpans` (`api/graph/trace.resolvers.go:316-320`). `BatchSpans` counts stored rows, and if `listen == nil` or `spanCount < 100_000`, it also uses full `selectSpans(...).OrderBy("Timestamp ASC")` (`api/db/traces.go:605-621`). The root/listen incremental partial query is the `else` branch for >=100k listened traces (`api/db/traces.go:622-747`).

Therefore the ~1/5 result is not explained by `root:true` read-view rollup for these traces. `trace.spans` is the full-store read path, and `spansUpdated(root:true, listen:nil)` should also full-read below 100k rows.

### Cloud measurements

Engine large queue only, default CLI queue, trace `087fcefc0c6ae1edd2e7089b25fb6ed1`:

```
engine DB distinct spans       36809
Cloud trace.spans              17872
Cloud IDs not in engine DB        25  (CLI/root-side spans)
engine DB IDs not in Cloud     18962
engine DB publishResult        16589
Cloud publishResult             7492
engine DB publishResult orphans    0
Cloud publishResult orphans      487
```

This looked like Cloud storage/read loss, but the producer path still had the CLI process default BSP queue.

Engine large queue plus CLI large queue, trace `e575065ffb1453abfdc92a5a5fd8ebcc`:

```
OTEL_BSP_MAX_QUEUE_SIZE=200000 OTEL_BSP_MAX_EXPORT_BATCH_SIZE=20000 ./hack/with-dev ./bin/dagger ...

engine DB rows                 30376
engine DB distinct spans       15188
Cloud trace.spans              15213
Cloud IDs not in engine DB        25  (CLI/root-side spans)
engine DB IDs not in Cloud         0
engine DB publishResult         6898
Cloud publishResult             6898
engine DB publishResult orphans    0
Cloud publishResult orphans        0
```

The lower count versus the previous trace is from a warmer workload/cache state, not from loss: the comparison is by span ID for the same trace, and `local_not_cloud=0`.

This proves Cloud's subset was not a rolled-up TUI/read view and not a wrong local capture. For the measured workload, Cloud ingest/store/read can retain the complete engine DB span set. The missing Cloud rows in the engine-large-only run were lost before Cloud ingest, in the CLI-side SDK BSP queue that re-exports engine DB SSE batches to the Cloud OTLP exporter.

## Updated closing answers

Is data missing?

Yes. With default queues, real `call_exec` and `dagql.publishResult` span IDs are emitted and reach a pre-BSP span processor, but many never reach the engine client SQLite DB or Cloud. The `dagql.publishResult` orphan roots are a faithful symptom of span data loss, not a loader/model artifact.

Where exactly is it lost locally?

The exact local drop site is the engine process SDK `BatchSpanProcessor` queue inside the per-client `telemetry.NewLiveSpanProcessor` registered at `engine/server/session.go:682-709`. It drops between pre-BSP `processor_on_end` and `clientSpans.ExportSpans`. It is not `beginOTelCallExec`, not engine emit in memory, not `clientSpans.ExportSpans` serialization, not DB insert, not CLI OTLP export, not OTLP transport, and not `hack/otlpdump`.

Exact mechanism?

SDK BSP queue overflow/drop. Default queue is 2,048; the module-load burst creates far more live start/end snapshots than that. Raising the engine BSP queue to 200,000 made `begin_call_exec_missing_export`, `begin_call_exec_missing_insert`, `begin_publish_missing_export`, and `begin_publish_missing_insert` all go to 0, and eliminated all `dagql.publishResult` absent-parent cases in the engine DB.

Cloud-vs-local reconciliation?

The Cloud ~1/5/subset finding is not a `root:true` rolled-up read view and not a wrong local capture. For traces below 100k stored rows, backend `trace.spans` and `spansUpdated(root:true, listen:nil)` use the full `otel_traces FINAL` read. The Cloud store/read returned only a subset when the CLI process used the default BSP queue while re-exporting engine DB spans to Cloud. When both the engine queue and the CLI queue were raised, Cloud `trace.spans` contained every engine DB span ID for the trace, plus 25 expected CLI/root-side spans.

Does Cloud contain call_execs dropped on the engine->client-DB path?

No for the checked default run. All 3,610 `begin_call_exec` IDs missing from engine DB were also absent from Cloud `trace.spans`. This follows the code path too: in shared dev-engine mode Cloud upload consumes engine DB SSE batches; it cannot recover spans already dropped before the DB.

Cloud ingest/storage loss?

Not proven as the cause; for this workload it is disproven as the necessary explanation. Backend code shows no ingest sampler/filter, and the both-large-queue run proves Cloud ingest/store/read can carry the full engine DB set. Remaining caveat: I did not directly query ClickHouse, so I cannot make a universal durability claim about async ClickHouse failure modes. The measured missing rows are upstream of Cloud ingest.

True impact on the OTel source?

Default local captures and default Cloud traces from this burst are unfaithful; the loader/gate is correct to reject them. A Cloud trace can be faithful for this workload when the upstream engine and CLI live span queues do not drop, but the current default pipeline is lossy under burst and therefore not safe as the wcprof OTel source.

Principled fix?

Make the internal telemetry paths that feed the engine client DB and Cloud OTel source lossless or explicitly failing. The practical fix is to stop using a drop-on-full SDK BSP for these required span paths, or configure/wrap it with backpressure (`BlockOnQueueFull`) or a sufficiently bounded lossless queue plus hard counters/errors. This must be applied to both:

- the engine per-client SQLite live span processor; and
- the CLI process Cloud re-export live span processor that consumes engine DB SSE.

Do not compensate in the loader/replay. Keep the structural gates; fix emit/export so the data is faithful.

## Follow-up: why now? telemetry volume and introspection skip

Erik's question: are we hitting BSP overflow now because this branch emits much
more telemetry than main, specifically by bypassing the normal telemetry skip for
literal/introspection-style schema calls?

### Measurement setup

I measured engine client SQLite DB spans, not otlpdump or Cloud, to avoid the
already-proven downstream BSP loss. Each measurement used a separate Docker
engine container and volume, with the engine process started with:

```console
OTEL_BSP_MAX_QUEUE_SIZE=200000
OTEL_BSP_MAX_EXPORT_BATCH_SIZE=20000
OTEL_BSP_SCHEDULE_DELAY=100
```

Workload for all three refs was the same current checkout as the loaded module:

```console
dagger --progress=plain -c 'container | from alpine | with-exec sleep 3 | stdout'
```

Refs/images:

- current branch HEAD `4585bf413d5ad09af918b39b0e2b62e95ad02006`, image `localhost/dagger-engine.dev`, trace `819a3d8466b6aa9c389d8d92ca54eead`, top-level DB `qz9egyf79tktoc1oii1yslrbu.db`
- main `672ce93530da9d71ac2714562eabdb7924c291c5`, built in `/tmp/dagger-main-telemetry-672ce`, image `localhost/dagger-engine.telemetry-main`, trace `69e115a2a65aaf8a5177ceaeda1a4c0b`, top-level DB `gl0yrjzji8tb09e5fbpm3lx57.db`
- pre-wcprof-OTel-emit parent `71b69f1f16d7c11dba6becc779996eb1f1875d5e`, built in `/tmp/dagger-preemit-telemetry-71b69`, image `localhost/dagger-engine.telemetry-preemit`, trace `b3b0b42799a2f705dfaec894fed97f4a`, top-level DB `psvm0m0ih59ngcifneb2obaq0.db`

The main/preemit `hack/build` auto-start step failed at the end because port
6060 was already bound, but both builds had already exported `bin/dagger`,
`bin/engine.tar`, and loaded the Docker image. I started my own no-port-bind
measurement containers from those images.

SQL method: for each trace I counted distinct `span_id` from the top-level client
DB, using the latest row per span (`row_number() over (partition by span_id order
by id desc) = 1`) for name/attribute analysis. Attribute classification used
`cast(attributes as text) like '%wcprof.op.kind%'`.

### Volume result

```text
ref        distinct spans  wcprof spans  ordinary spans
current            36807         33514            3293
main                3362             0            3362
preemit             3571             0            3571
```

So yes, total engine telemetry volume is really up: current is 10.95x main and
10.31x the pre-emit parent for this workload. But ordinary telemetry is not up;
current ordinary spans (`3293`) are baseline-sized versus main (`3362`) and
preemit (`3571`). The increase is the new wcprof OTel source spans.

Current wcprof span kinds:

```text
wcprof.op.kind  count
internal        16589  # dagql.publishResult
call_exec       16589
lazy              276
exec_phase         40
exec               20
```

The exact current volume driver is the paired cache-miss emission of
`call_exec` and `dagql.publishResult`, with smaller additions from later lazy and
exec-split chunks.

### Introspection-style names

Main/preemit emitted none of the high-volume schema-building names in the
top-level trace DB. Current emitted them as `wcprof.op.kind=call_exec` spans:

```text
name                         wcprof.op.kind  current count
Function.__withArg           call_exec       1011
Function.args                call_exec        985
Function.sourceModuleName    call_exec        985
Function.withArg             call_exec        566
ObjectTypeDef.__withFunction call_exec       1431
Query.__function             call_exec        124
Query.__functionArg          call_exec        518
Query.__functionArgExact     call_exec        169
TypeDef.asEnum               call_exec        190
TypeDef.asInput              call_exec        190
TypeDef.asInterface          call_exec        190
TypeDef.asList               call_exec        190
TypeDef.asObject             call_exec        190
TypeDef.asScalar             call_exec        190
TypeDef.withFunction         call_exec        289
dagql.publishResult          internal       16589
```

Sample current `Function.args` attributes:

```json
[{"key":"dagger.io/ui.passthrough","value":{"boolValue":true}},{"key":"wcprof.op.kind","value":{"stringValue":"call_exec"}},{"key":"dagger.io/dag.digest","value":{"stringValue":"xxh3:75ba6ed2f32b2c88"}}]
```

That directly reconciles the earlier local capture counts (`Function.args=325`,
`Function.__withArg=366`, etc.): those were not ordinary call telemetry. They
were wcprof `call_exec` spans, and the earlier local capture was lossy. With the
engine queue raised, the same category is much larger (`Function.args=985`,
`Function.__withArg=1011`, `TypeDef.as*=190 each`).

### Skip mechanism in code

Normal DagQL call telemetry is emitted by `core.AroundFunc`, installed on core
servers at `core/schema/coremod.go:47`, `core/schema_build.go:107`,
`core/modtree.go:589`, and `core/sdk/module.go:77`.

The normal skip path is:

- `core/telemetry.go:32-38`: if the context is already skipped, no-op; if
  `introspectionInfo` classifies the current call as introspection, return
  `dagql.WithSkip(ctx)` and no span.
- `core/telemetry.go:39-43`: meta calls are no-op.
- `core/telemetry.go:60-64`: `dagql.ShouldEmitTelemetry` dedupes repeated cache
  keys unless repeated telemetry or do-not-cache is in force.
- `dagql/objects.go:655-665`: `AroundFunc` is only invoked when
  `!field.Spec.NoTelemetry`.
- `dagql/telemetry.go:48-64`: the repeated-key dedupe predicate itself.

The introspection classifier covers the schema-building roots and receiver-chain
mutators:

- `core/telemetry.go:363-387`: roots including `__schema`, `currentTypeDefs`,
  `function`, `typeDef`, `__function`, `__functionArg`,
  `__functionArgExact`, `__objectTypeDef`, `__scalarTypeDef`, etc.
- `core/telemetry.go:401-444`: when not in debug mode, receiver-chain calls on
  `Function`, `TypeDef`, `FunctionArg`, `ObjectTypeDef`, etc. are classified
  introspection for `Function.__*`, `Function.withArg`,
  `TypeDef.with*`, and other `__*` builder fields.
- `core/telemetry_test.go:233-258` and `core/telemetry_test.go:273-291` test
  root skip, descendant skip, and function-builder classification.

The high-volume fields themselves are installed in `core/schema/module.go`:
`Function.__withArg` at `core/schema/module.go:467`, `Function.args` and
`Function.returnType` at `core/schema/module.go:471-476`, and `TypeDef.as*` at
`core/schema/module.go:581-594`. The `TypeDef.as*` names are not listed
directly in `introspectionInfo`; in the module-load workload they are suppressed
by being descendants of introspection/schema-building roots.

### Where wcprof emit bypasses the skip

The wcprof OTel emit path in the cache layer does not use the normal telemetry
predicate.

Evidence:

- `dagql/objects.go:583-599` builds `CallRequest` with
  `DoNotCache`, `IsPersistable`, and `PassthroughTelemetry`, but not
  `NoTelemetry` or "telemetry skipped" state.
- `dagql/call_request.go:8-19` confirms `CallRequest` has no `NoTelemetry` or
  `ShouldEmitTelemetry` result bit.
- `dagql/cache.go:3601-3650` returns early for `req.DoNotCache`, so wcprof
  `call_exec` is not emitted for do-not-cache calls.
- `dagql/cache.go:3679-3687` returns early on cache hit, so wcprof `call_exec`
  is not emitted for cache hits.
- `dagql/cache.go:3731-3734` starts `beginOTelCallExec` on a cache miss when
  `OTelProfActive(callCtx)` is true.
- `dagql/otelprof_hooks.go:41-43` defines `OTelProfActive` as only
  `trace.SpanFromContext(ctx).IsRecording()`.
- `dagql/otelprof_hooks.go:22-28` explicitly says these spans are gated only on
  telemetry being active, not on `wcprof.Enabled`, and are part of normal
  telemetry whenever a recording span is present.
- `dagql/otelprof_hooks.go:45-65` starts the `call_exec` span and marks it
  passthrough with `wcprof.op.kind=call_exec`.
- `dagql/cache.go:4013-4018` starts `dagql.publishResult` whenever the
  `call_exec` span context is valid.
- `dagql/otelprof_hooks.go:68-83` marks `dagql.publishResult` passthrough with
  `wcprof.op.kind=internal`.
- `dagql/cache.go:3943-3949` says the wait edge is emitted from the cache layer
  specifically because a telemetry-suppressed caller never enters `AroundFunc`.

Conclusion: normal call telemetry still honors the introspection/schema skip;
the wcprof OTel cache-miss emit path does not. It is not literally
"unconditional for every cached call" because do-not-cache calls and cache hits
return before `beginOTelCallExec`; it is unconditional for every cache miss under
a recording ancestor span, including calls normal telemetry deliberately
suppresses.

This is why the high-volume names reappear as OTel spans without corresponding
normal spans on current. The path has no access to `field.Spec.NoTelemetry`,
`dagql.IsSkipped`, `introspectionInfo`, `isMeta`, or the `ShouldEmitTelemetry`
decision.

### Literal `__schema` / `__type` probes

I also ran focused no-module-load probes:

```console
dagger api query --no-load-module   # current
dagger api query --no-mod           # main
```

Queries:

```graphql
{ __schema { queryType { name } } }
{ __type(name:"Query") { name fields { name } } }
```

Results:

- current `__schema`, trace `48644d547bd0f32f460602ed4d195c6e`: `POST /query`
  normal span, plus wcprof `Query.__schema` and `dagql.publishResult`.
- main `__schema`, trace `1f56ee12bbad4589192dbe3dcf23b24d`: only
  `POST /query`. This proves wcprof broke the literal `__schema` skip.
- current `__type`, trace `51d40509c82b4e1bd943bf8a83553be7`: normal
  `Query.__type`, normal introspection field spans, plus wcprof `Query.__type`
  and `dagql.publishResult`.
- main `__type`, trace `37b8544d0bc4e510b0aaca4e75c8807c`: normal
  `Query.__type` and normal introspection field spans, no wcprof spans.

Important nuance: Erik's assumption that literal `__type` is normally skipped is
not true for the measured main/current code path. `dagql/introspection/types.go`
installs `__type` at `dagql/introspection/types.go:34-46`, but `__type` is not
in the `core/telemetry.go:363-387` introspection root list. That is preexisting
ordinary telemetry behavior. The branch still adds extra wcprof spans for
`__type`; it just did not create the normal `Query.__type` span.

### Answer

Telemetry volume is up, but not because ordinary telemetry itself got noisier.
The measured increase is almost entirely the wcprof OTel source: `call_exec`
plus `dagql.publishResult` on cache misses. The normal high-volume
introspection/schema-building skip still suppresses ordinary spans for the
module-load workload, but the wcprof cache-layer emit path bypasses that skip and
therefore emits the suppressed calls as `wcprof.op.kind=call_exec`.

This is the proximate "why now" for the BSP overflow: the branch adds roughly
33k extra spans to a workload that previously emitted roughly 3.3k-3.6k engine
spans. About 16.6k are `call_exec` and 16.6k are `dagql.publishResult`; many of
the `call_exec` spans are exactly the high-volume schema/introspection-style
calls Erik called out.

Principled fix for this showstopper: the wcprof OTel emit gate must incorporate
the same telemetry suppression decision as ordinary call telemetry before
emitting cache-layer `call_exec`/`publishResult` spans, rather than merely
checking for a recording ancestor span. The data source should not compensate in
the loader; it should stop emitting analysis-source spans for calls the telemetry
model intentionally suppresses, unless the design explicitly changes that
policy and the queue/backpressure story is made lossless for the resulting
volume.
