# wcprof x OTel implementation roadmap review

Reviewed: `hack/designs/wcprof-otel-impl-plan.md` against the approved
`hack/designs/wcprof-otel-design.md` and current source.

Overall verdict: **sound enough to start from Chunk 1**, with two roadmap
adjustments I would make before treating the DoDs as complete. The dependency DAG
is basically right, including the non-obvious `exec.run` -> `call_exec` edge. The
five chunks are coherent and reviewable. The main misses are validation/plumbing
details, not design correctness.

## REAL issues

### 1. MEDIUM: the persisted-cache drift fixture is never scheduled

The plan correctly lists persisted-import decode wait edges as a post-v1 reserve
seam, gated on the §6.5 persisted-cache fixture
(`hack/designs/wcprof-otel-impl-plan.md:411-413`). But no chunk DoD actually runs
that fixture. Chunk 2 covers singleflight/cache-miss fan-in
(`hack/designs/wcprof-otel-impl-plan.md:209-220`), Chunk 3 covers lazy
(`hack/designs/wcprof-otel-impl-plan.md:253-266`), Chunk 4 covers exec/services
(`hack/designs/wcprof-otel-impl-plan.md:298-306`), and Chunk 5 covers Cloud
round-trip plus the standing complex gate
(`hack/designs/wcprof-otel-impl-plan.md:322-339`). None of those names the
persisted-cache import/decode fixture.

That fixture is part of the approved design, not optional polish:
`hack/designs/wcprof-otel-design.md:990-998` says to run after an engine restart
with imported cache, assert first-occurrence decode appears as call-span
self-time, and assert repeated/concurrent suppressed-decode workloads do not cause
native-vs-OTel top-N drift. The reason is also explicit: suppressed repeated hits
can block on `attachDepsWaitCh` / `persistDecodeWaitCh`
(`hack/designs/wcprof-otel-design.md:784-799`), and current source has those waits
with no `wcprof.BeginWait` in `ensurePersistedHitValueLoaded`
(`dagql/cache_persistence_import.go:563-620`). Repeated call spans are exactly the
ones OTel suppresses via `ShouldEmitTelemetry`
(`dagql/telemetry.go:48-64`, used by `core/telemetry.go:58-64`).

Recommendation: add this fixture to **Chunk 2** if the intent is "all cache-layer
known gaps quantified when the oracle comes online," or at latest **Chunk 5**
before v1 is declared complete. Chunk 2 is the cleaner fit because it already owns
the cache/singleflight oracle harness and touches `dagql/cache.go`.

### 2. MEDIUM: Chunk 1's dropped-link gate is not observable from current `hack/otlpdump` JSONL

Chunk 1 requires the §6.1 structural gate and says the otlpdump path asserts zero
dropped links (`hack/designs/wcprof-otel-impl-plan.md:160-174`). Chunk 2 then
relies on that for the suppressed-sibling cap-stress fixture
(`hack/designs/wcprof-otel-impl-plan.md:214-217`). The approved design also
expects the local path to read `DroppedLinksCount` / `DroppedAttributesCount`
(`hack/designs/wcprof-otel-design.md:902-917`).

Current `hack/otlpdump` does not emit those fields. Its span JSON includes
`traceId`, `spanId`, `parentId`, `name`, `startNs`, `endNs`, `attrs`, and `scope`
(`hack/otlpdump/main.go:109-119`), then serializes links as target ids plus attrs
only (`hack/otlpdump/main.go:124-134`). The OTLP proto does carry the data
(`go.opentelemetry.io/proto/otlp@v1.9.0/trace/v1/trace.pb.go:545-556` for span
dropped attr/event/link counts, and `:870-953` for per-link dropped attrs), and
the SDK source confirms `DroppedLinks()` exists on ended spans
(`go.opentelemetry.io/otel/sdk@v1.43.0/trace/snapshot.go:117-120`).

As written, a fresh implementer cannot satisfy "zero dropped links" from captured
JSONL without either changing `hack/otlpdump` or using a different raw-OTLP ingest
path. Recommendation: add `hack/otlpdump` output of span/link dropped counts to
Chunk 1's scope and touch-points, or explicitly say the Chunk 1 loader ingests raw
OTLP protobuf for this gate. The JSONL route is simpler and matches the plan.

### 3. LOW: clarify Chunk 1's fallback kind classification so `withExec` does not drift

This is not a blocker, but the wording is easy to implement inconsistently.
Chunk 1 says the loader fallback maps "un-augmented spans with `dag.digest` ->
`call`; `withExec` -> `exec`" (`hack/designs/wcprof-otel-impl-plan.md:145-149`).
The design's loader rule is more specific: use `wcprof.op.kind` when present;
otherwise structural classification includes "`dag.digest` and child `call_exec`
=> `call`; a `withExec` => `exec`"
(`hack/designs/wcprof-otel-design.md:841-844`).

Current source makes every emitted DagQL call span carry a digest and a
`Type.Field` name (`core/telemetry.go:45-51`, `core/telemetry.go:83-86`), while
native models `Container.withExec` as a `call` plus a `call_exec`
(`dagql/cache.go:3515`, `dagql/cache.go:3673`) and the actual executor work as
`exec.run` (`engine/engineutil/executor.go:122`). If a loader fallback treats the
visible `Container.withExec` call span itself as `exec` after Chunk 2/4, the class
table will drift from the native shape the plan is trying to converge to.

Recommendation: spell out the intended precedence in Chunk 1. For example:
`wcprof.op.kind` wins; a DagQL call span with `dag.digest` remains `call`; the
new Chunk 4 `exec.run` span carries `wcprof.op.kind=exec`; any legacy/unaugmented
`withExec` fallback is only for the intentionally-wrong baseline and must not
override the corrected shape once `call_exec` / `exec.run` exist.

## Noise / verified

### Dependency DAG

Verdict: **NOISE; correct.**

The `exec.run` dependency on Chunk 2 is real. The design parents `exec.run` under
`call_exec` (`hack/designs/wcprof-otel-design.md:691-700`), and current source
shows why: `withExec` captures `causeCtx` before executor instrumentation
(`core/container_exec.go:1304`), then calls `engineClient.Run` with the current
execution context (`core/container_exec.go:2104-2118`). Native creates
`exec.run` inside the executor (`engine/engineutil/executor.go:116-130`). The
OTel version needs the resolver to already be running under Chunk 2's `call_exec`
span for this parentage to be natural.

Lazy and exec are correctly modeled as siblings after Chunk 2. Lazy needs
`call_exec` for sub-call fidelity (`hack/designs/wcprof-otel-impl-plan.md:267-269`)
but not the exec split. Services only need the wait vocabulary and target-before-
primitive pattern, and the plan acknowledges that it is bundled into Chunk 4 by
review convenience rather than a hard dependency
(`hack/designs/wcprof-otel-impl-plan.md:383-386`).

### Chunk sizing

Verdict: **NOISE; reasonable.**

Chunk 1 is a real foundation: loader, shared vocabulary, provider link cap, and
structural gate. Chunk 2 is the right first faithfulness chunk because the design
calls cache singleflight the central fix and the place the oracle should come
online (`hack/designs/wcprof-otel-design.md:445-570`,
`hack/designs/wcprof-otel-design.md:1052-1056`). Chunk 3 keeps the lazy emit and
stamping processor together, which is the right review unit because the processor
has no standalone behavioral value. Chunk 5 is appropriately productionization.

Chunk 4 is the only large one. Bundling exec + services is defensible as "finish
the remaining choke points," and the plan already says to split into 4a/4b if it
reviews heavy (`hack/designs/wcprof-otel-impl-plan.md:275-280`). I would keep that
as a planned escape hatch, not a blocker.

### Buildable/testable per chunk

Verdict: **mostly yes, with the two real fixes above.**

The oracle can genuinely come online at Chunk 2 for choke-point-isolating
singleflight workloads. Native wcprof records call and shared execution ops
(`dagql/cache.go:3505-3518`, `dagql/cache.go:3669-3717`), waits in `c.wait`
(`dagql/cache.go:3853-3881`), and `publishResult`
(`dagql/cache.go:3922-3947`). Chunk 2 is exactly where OTel emits corresponding
`call_exec`, wait links, and `publishResult`; no lazy/exec/service mechanisms are
needed for singleflight-heavy workloads.

The plan's "complex workload only converges at Chunk 5" warning is sound.
Design §6.4 explicitly calls for a representative complex standing drift gate
(`hack/designs/wcprof-otel-design.md:948-955`), and the roadmap correctly defers
that until all choke points are implemented (`hack/designs/wcprof-otel-impl-plan.md:357-366`,
`hack/designs/wcprof-otel-impl-plan.md:329-339`). Earlier chunks should use
workloads that isolate the newly corrected choke point.

### Fidelity to reserve seams

Verdict: **NOISE; good.**

The plan keeps the qualified fan-in merge out of v1
(`hack/designs/wcprof-otel-impl-plan.md:406-408`) and matches the corrected design
seam (`hack/designs/wcprof-otel-design.md:1154-1174`). It also keeps
`publishResult`-as-wait-target, persisted decode wait targets, finer exec phases,
leaf I/O, and multi-trace aggregation out of v1
(`hack/designs/wcprof-otel-impl-plan.md:401-420`). That matches the design's
scope decisions, including one trace only
(`hack/designs/wcprof-otel-design.md:1203-1224`).

## Suggestions

- In Chunk 1, specify the exact `wcprof.parent` encoding, e.g. lower-hex OTel
  span id string, so the stamping processor and loader cannot choose different
  encodings. The design says "span id" but not the concrete wire representation
  (`hack/designs/wcprof-otel-design.md:399-404`).
- In Chunk 1, tell the implementer to build span limits from
  `sdktrace.NewSpanLimits()` and then set `LinkCountLimit = 16384`. The SDK
  supports `WithSpanLimits` (`go.opentelemetry.io/otel/sdk@v1.43.0/trace/provider.go:421-443`),
  but `WithRawSpanLimits` uses zeros as real zero limits unless populated from
  `NewSpanLimits` first (`provider.go:446-465`).
- For Chunk 1's un-augmented baseline, say "simple no-service workload" or
  equivalent. The structural gate is useful before faithfulness, but a baseline
  trace that includes long-lived service availability spans may fail for reasons
  the design intentionally fixes later in §3.4.

## Start recommendation

Start Chunk 1, but fold in the `hack/otlpdump` dropped-count output as part of
that chunk, and schedule the persisted-cache drift fixture before v1 completion.
With those adjustments, the roadmap is a practical execution plan for the
approved design.
