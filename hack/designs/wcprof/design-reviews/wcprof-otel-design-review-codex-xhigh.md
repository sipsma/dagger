# wcprof OTel Design Review

Reviewed against current `upstream/main` at `b442cd25331d50839748bbc72d222c4c2a56125a`.

## Overall verdict

The design is directionally right: the core idea of compiling OTel spans and explicit wait links into the existing wcprof IR is the right layer, and the replay should remain unchanged. The singleflight `call_exec` repair is especially well aligned with native wcprof.

However, I would not build this design as written. It has several real correctness gaps that can produce wrong counterfactual rankings on real traces, not just cosmetic drift:

1. lazy wait links cannot reliably target the proposed resume/lazy span with the current `evaluateOne` control flow;
2. the exec/user-work proposal loses the native profiler's engine-vs-user split and can mislabel engine overhead as user work;
3. the production Cloud ingest path cannot currently prove wait links were not dropped;
4. the categorical cache-hit omission is false for persisted/imported hits;
5. cache miss publication and normalization work is left outside the proposed `call_exec`/wait model.

These are fixable without changing the analyzer, but they need to be fixed in the design before implementation.

## REAL issues

### 1. HIGH: lazy wait links cannot target the resume span as specified

Verdict: REAL ISSUE.

The design says the existing resume span should become the lazy op, and joining consumers should emit `wait` links to that resume span. The causal model is right, but the implementation plan does not work with the current code ordering.

Native wcprof creates and publishes the lazy op ID while holding `shared.lazyMu`, before any waiter can observe `shared.lazyEvalWaitCh`:

- `dagql/cache.go:2935-2943` has joiners read `shared.lazyEvalWaitCh` and `shared.lazyEvalProfOpID`, unlock, then begin a lazy wait.
- `dagql/cache.go:2953-2961` creates the native lazy op and stores `shared.lazyEvalProfOpID`.
- `dagql/cache.go:2962-2966` publishes `shared.lazyEvalWaitCh` and unlocks.

That ordering is why native waiters can always point at the lazy op.

The proposed OTel target, the resume span, is not created until after that publish/unlock, inside the goroutine:

- `dagql/cache.go:2968` starts the goroutine.
- `dagql/cache.go:2984-2989` starts the resume span.
- `dagql/cache.go:2990-2994` wraps the callback context with `resumedCallbackSpan`.

So a concurrent joiner can hit `dagql/cache.go:2935-2943` before the resume span exists, and even the triggering consumer at `dagql/cache.go:3055-3057` has no synchronous access to the local `resumeSpan` created inside the goroutine. An OTel wait link needs a concrete target `SpanContext` when the waiter emits the link. Without a stored target span context, the implementation must either drop the wait edge or invent one later, both of which violate the brief's anti-inference and faithful-emit invariants.

Required design change: create the OTel lazy op span, or at least its target `SpanContext`, before `shared.lazyEvalWaitCh` becomes visible to waiters, and store it in the shared lazy state under the same lock. Then pass that span/context into the goroutine for callback execution and span end. This mirrors native's `shared.lazyEvalProfOpID` ordering.

The conceptual lazy fix is still correct: stop re-parenting callback children to the producer, keep the producer association as a non-wait UI link, and make consumer-triggered lazy work a synchronous child of the consumer. The current document just misses the target-publication requirement that makes wait links reliable.

### 2. HIGH: exec/user-work attribution is not faithful enough for the stated goal

Verdict: REAL ISSUE.

The brief's goal explicitly requires user work to be first-class: a slow `go build` in the user's code must be a valid headline answer. The design tries to satisfy that by adding `wcprof.work_type = "user"` to the `withExec`/exec span and not emitting executor phase spans.

That is not faithful to the current engine.

Native wcprof deliberately splits container execution into engine overhead and user process time:

- `engine/wcprof/README.md:36-39` says the current hooks record `exec` phases and split `exec.containerStart` from `exec.processRun` as user work.
- `engine/engineutil/executor.go:188-203` records each executor setup phase as `exec.<phase>`.
- `engine/engineutil/executor_spec.go:1398-1412` records `exec.containerStart` separately from `exec.processRun`, and only `exec.processRun` carries `WorkTypeUser`.
- `core/container_exec.go:1741-1742` records `withExec.prepareMounts`.
- `core/container_exec.go:2175-2182` records `withExec.applyOutputs`.

Current OTel does not emit corresponding executor phase spans. The executor OTel path is propagation and telemetry forwarding, not wall-clock phase instrumentation:

- `engine/engineutil/executor_spec.go:745-840` sets up OTel env/proxy/traceparent for the process.
- `engine/engineutil/executor_spec.go:835-837` propagates the current span context into the container.
- There are no `Tracer.Start` spans in the executor phase code matching native `exec.containerStart` or `exec.processRun`.

Therefore a leaf `Container.withExec`/`call_exec` span self-time is not "approximately process time" in the sense the design needs. It also contains container start, setup, mount preparation, service-monitor interactions, output application, and other engine work unless those happen to be covered by child spans. Marking that whole span `user` can produce a wrong headline: engine overhead reported as the user's slow command. Leaving it unmarked fails the "user work first-class" goal.

Required design change: add an honest OTel representation of at least the native `exec.containerStart` vs `exec.processRun` split, with `wcprof.work_type=user` only on the process-run interval. It does not have to expose every setup phase in the UI, but the IR needs the split if the report is going to distinguish "your build is slow" from "Dagger/container setup is slow." The design's "do not add executor phases; sub-ms" claim is not supported by the source, and the native profiler exists precisely because these wall-clock phases can matter.

### 3. HIGH: Cloud ingest cannot currently assert that wait links were not dropped

Verdict: REAL ISSUE.

The design depends on span links as causal wait edges. If a wait link drops, the counterfactual graph is wrong. The document recognizes this and proposes a structural gate: assert the link cap was not hit. That gate is not implementable through the current production ingest path.

The OTel SDK default link cap is 128:

- `go.opentelemetry.io/otel/sdk/trace/span_limits.go:21-23` sets `DefaultLinkCountLimit = 128`.
- `go.opentelemetry.io/otel/sdk/trace/span_limits.go:61-68` says new links past the cap evict the oldest links.
- `go.opentelemetry.io/otel/sdk/trace/span.go:764-793` applies link and per-link-attribute limits when `AddLink` is called.

Local OTLP preserves dropped-link counts:

- `github.com/dagger/otel-go/transform.go:397-405` writes `DroppedLinksCount`.
- `github.com/dagger/otel-go/transform.go:445-467` preserves link attributes and dropped link-attribute counts.
- `github.com/dagger/otel-go/transform.go:614-617` can read `DroppedLinksCount` back from OTLP.

But the Dagger Cloud GraphQL trace API used by the current repo does not expose those counts:

- `internal/cloud/trace.go:32-65` requests `links { traceId spanId traceState attributes }`, but not dropped-link or dropped-link-attribute counts.
- `internal/cloud/trace.go:81-99` has no dropped count fields on `SpanData`.
- `internal/cloud/trace.go:112-117` has no dropped count fields on `SpanLink`.
- `internal/cloud/trace.go:305-365` converts Cloud span data back to OTLP without setting `DroppedLinksCount`.

Counting `len(span.Links) == configuredLimit` is not sufficient. It misses backend-side truncation, per-link-attribute truncation, and any case where the configured SDK limit and the Cloud-returned shape diverge. A span with 127 returned links can still have lost required wait edges if Cloud applied a different cap, and the loader would have no signal.

There is also a smaller encoding risk: Cloud attributes are decoded into `map[string]any` (`internal/cloud/trace.go:91`, `:116`), JSON numbers arrive as `float64` (`internal/cloud/trace.go:374-379`), and the design proposes nanosecond wait start/end attributes. Absolute Unix nanoseconds are too large to be represented exactly as JSON floats. The rounding is probably not material for millisecond-scale bottlenecks, but it contradicts the "exact blocked intervals" contract and is easy to avoid.

Required design change: make wait-edge durability observable on the Cloud path before relying on it. Options include exposing dropped-link and dropped-link-attribute counts in Cloud's trace API, using an unlimited or sufficiently high engine span limit plus an exported configured-limit attribute, and encoding wait timestamps as strings or relative nanoseconds with an explicit schema. Then the loader can fail loudly instead of silently accepting a causally incomplete graph.

### 4. MEDIUM: "cache hits are negligible" is false for persisted/imported hits

Verdict: REAL ISSUE.

The design says cache hits can be omitted because they have negligible self-time and no wait edge. That is true for an already materialized in-memory hit. It is not true for persisted/imported hits.

The cache-hit path can block and do real work before returning:

- `dagql/cache.go:3632-3639` returns a hit only after `lookupCacheForRequest`.
- `dagql/cache_egraph.go:883-915` handles a hit and then calls `ensurePersistedHitValueLoaded`.
- `dagql/cache_persistence_import.go:563-578` can block waiting for dependency attachment.
- `dagql/cache_persistence_import.go:598-615` can block behind another persisted-payload decode.
- `dagql/cache_persistence_import.go:658-700` decodes a persisted payload and syncs snapshot leases.

That work can be on the critical path of a CI run, especially after engine restart or when using imported/persisted cache state. If the loader drops cached spans categorically, this time either disappears or is charged to an ancestor's opaque self-time. If the span is suppressed because the digest was already seen, the design emits nothing to recover it.

Required design change: distinguish cheap in-memory hits from hits that wait/decode/import materialized state. The minimal faithful approach is to keep cached call spans that have non-trivial duration or add explicit spans/waits for persisted hit decode and dependency attachment. The important point is that "hit" is not equivalent to "zero runtime work" in current code.

### 5. MEDIUM: result publication and returned-result normalization are not modeled

Verdict: REAL ISSUE.

The proposed cache fix models the resolver `fn` as `call_exec` and the blocked interval in `c.wait` as a wait edge. That covers the central singleflight execution, but it does not cover all serial work on the cache miss path.

Native records result publication explicitly:

- `dagql/cache.go:3700-3706` runs `fn` in the shared goroutine and ends the native `call_exec` op.
- `dagql/cache.go:3853-3881` records the caller wait only around the `oc.waitCh` select.
- `dagql/cache.go:3922-3947` records `dagql.publishResult` for result publication, indexing, and dependency attachment.
- `dagql/cache.go:4009-4012` then normalizes the returned result via `ensurePersistedHitValueLoaded`.

The design's `call_exec` span, if it only brackets `fn`, will not contain publication. The caller wait edge also ends before publication. Without a publication/normalization span or wait model, that serial work is attributed as caller or parent self-time instead of internal cache work, and the OTel vs native oracle will drift on workloads where publication/import is non-trivial.

Required design change: either carry over native's `dagql.publishResult` as an OTel op, or explicitly justify that publication and normalization are intentionally charged to the caller class. The latter would be a behavioral difference from native and should not be the default.

## NOISE / verified sound decisions

### Reusing the existing IR and replay unchanged

Verdict: NOISE. This part is correct.

The analyzer's contract is exactly the one the design targets:

- Replay treats self segments, child spawns, waits, and implicit joins as its action model (`engine/wcprof/wcanalyze/replay.go:11-27`).
- A wait becomes a real join only when it targets another op and ended at or after the target's end (`engine/wcprof/wcanalyze/replay.go:162-174`).
- Explicit wait joins are `clock = max(clock, finish(target))`, not additive (`engine/wcprof/wcanalyze/replay.go:379-381`).
- Self-time is interval minus child intervals and own wait intervals (`engine/wcprof/wcanalyze/graph.go:379-398`).

That means the design must feed honest parentage and honest wait edges. It does not need, and should not add, replay-side surgery.

### The singleflight diagnosis and `call_exec` repair

Verdict: NOISE. The diagnosis is real, and the proposed repair is sound in shape.

Current telemetry suppression is digest-based and suppresses repeated cacheable calls:

- `dagql/telemetry.go:48-64` stores seen call keys and returns false for repeated cacheable calls.
- `core/telemetry.go:53-64` returns the unchanged context when suppressed.

Native cache execution has the exact op/wait shape the design wants to mirror:

- `dagql/cache.go:3666-3677` creates the shared `call_exec` op on the detached call context.
- `dagql/cache.go:3700-3713` runs the resolver under that shared context.
- `dagql/cache.go:3867-3874` records every caller wait to the shared execution, with `singleflight` for joiners.

Adding an OTel `call_exec` span in the cache layer, independent of `AroundFunc`, is the right fix for emitter-vs-executor races and suppressed joiners. The needed correction is to include the publication/normalization issue above, not to abandon the shape.

### Wait links on the waiter span, including suppressed callers' parent span

Verdict: NOISE, with one validation requirement.

Putting the wait edge on the span that is current in `c.wait` is the right OTel analog of native `wcprof.BeginWait`, which derives the waiter from context. If the caller span was suppressed, the current span is its parent or ancestor. That ancestor is the only visible op whose wall-clock interval includes the blocked work, so attaching the wait there is preferable to inventing a synthetic suppressed call span in the loader.

The concurrent-parent concern is not a fundamental replay problem. DagQL resolves sibling selections in parallel (`dagql/server.go:1120-1163`), so multiple suppressed child waits can attach to the same parent span. Replay does not add those waits together; each explicit join is a max against the target finish (`replay.go:379-381`), and self-time subtracts the union of wait intervals (`graph.go:379-398`). Overlapping waits therefore model fan-out/fan-in rather than serializing all children.

The validation suite should still include this shape: one parent resolving many repeated/suppressed selections concurrently, with staggered target finishes. That catches bad timestamp placement or accidental fixed-delay classification.

### Loader classification is not causal inference

Verdict: NOISE.

The loader steps in the design are mechanical as long as they stay within the stated boundaries:

- span parent ID to op parent;
- `link.purpose="wait"` to wait event;
- attributes and names to class/kind/work type/outcome;
- live-span dedup by span ID, keeping the ended copy.

That does not violate the anti-inference invariant. The forbidden operations would be timestamp reparenting, treating `dag.inputs` as runtime waits, following non-wait links as causal edges, or synthesizing missing nodes. The design explicitly rejects those.

### Live export dedup

Verdict: NOISE. The design correctly accounts for live OTel snapshots.

`LiveSpanProcessor.OnStart` exports a snapshot immediately (`github.com/dagger/otel-go/live.go:25-30`), before later links and end-time attributes exist. The ended span is exported later. A Cloud/OTLP loader must key by span ID and keep the ended copy. This is mechanical deduplication, not inference.

### Link attributes are preserved locally, but still need end-to-end Cloud validation

Verdict: PARTIAL NOISE, PARTIAL REAL ISSUE.

Local engine/client OTLP conversion preserves links and link attributes:

- `github.com/dagger/otel-go/transform.go:397-405` writes span links and dropped counts.
- `github.com/dagger/otel-go/transform.go:445-467` writes link attributes.
- `internal/cloud/trace.go:52-57` requests link attributes from Cloud.
- `internal/cloud/trace.go:349-363` converts returned Cloud link attributes back to OTLP.

So the basic "do link attributes survive the Go client shape?" claim is fine. The real issue is the missing dropped-count visibility on the production Cloud API, covered above. The design should require an actual Cloud round-trip test with wait-link attributes, not assume backend retention from the local transform alone.

### Services

Verdict: NOISE, assuming the implementation stores the target span context before publishing `starting`.

The native service model is a good template:

- Waiters block on `starting.done` and record a service wait to `starting.profOpID` (`core/services.go:335-343`, `core/services.go:951-963`).
- The start op is created before `ss.starting[key]` is published (`core/services.go:971-985`).
- `svc.Start` is the bounded start/health-check window (`core/services.go:990`), and the daemon availability lifetime continues after that (`core/services.go:1028-1033`).

A dedicated OTel `service.start` span plus waiter links is sound. The long-lived availability span must remain non-self-time-bearing for wcprof, as the design says.

### Nested clients under exec

Verdict: NOISE for parentage.

OTel has a real advantage over native here. Native needs `LinkKindNestedClient` stitching (`engine/wcprof/wcanalyze/graph.go:270-294`) because module-runtime API calls are a different client. OTel propagates the exec's trace context into the container:

- `core/container_exec.go:1304` captures the current span context as `causeCtx`.
- `engine/engineutil/executor_spec.go:751-753` reinstalls that context for OTel setup.
- `engine/engineutil/executor_spec.go:835-837` injects propagation env into the process.

So nested-client spans should naturally sit under the exec/withExec trace context. No loader-side nested-client causal inference is needed.

### `call_exec` executor explicit wait is redundant but harmless

Verdict: NOISE.

For the caller that actually executes a cache miss, the shared `call_exec` span is structurally nested under that caller or its visible ancestor. The replay's implicit child join will wait for the child. Emitting the same wait edge uniformly from `c.wait` is still harmless because explicit wait joins are idempotent max operations, not additive (`replay.go:379-381`). Uniform emission is simpler and makes joiner behavior reliable.

### Validation plan

Verdict: mostly NOISE, but incomplete until the REAL issues above are added.

The cross-source oracle is the right strongest check: run native wcprof and OTel on the same corrected engine trace, compile both to the same IR, and compare top-N `RunWhatIfs` rankings (`engine/wcprof/wcanalyze/replay.go:485-510`). The known-answer tests and standing drift gate are also appropriate.

The plan needs additional fixtures:

- lazy fan-in where a joiner arrives before the evaluator goroutine creates the resume span;
- many suppressed sibling selections under one parent, to prove parent-level wait links do not over-serialize;
- persisted-cache hit/import/decode cases;
- a `withExec` workload where container setup is intentionally delayed separately from process runtime;
- a Cloud round-trip test that proves wait links, wait-link attributes, and dropped-count signals survive production ingest.

## Brief critique and human decisions

The brief's invariants are mostly correct and useful. I would not loosen anti-inference or reuse of the existing replay.

Two decisions need human agreement:

1. UI behavior for lazy evaluation. Faithful parentage means deferred work renders under the consumer that forced it, with a non-causal link back to the producer. That is a real UI behavior change, not just telemetry plumbing.
2. Product scope for "CI run." The design is per Cloud trace. If a human expects "why was my CI run slow?" to cover multiple Dagger sessions or multiple traces in one CI job, aggregation is a separate product/design problem. It should not be smuggled into this loader.

## Bottom line

Build on the design's central structure, but do not treat it as complete. Fix lazy target publication, exec user/engine attribution, Cloud dropped-link observability, and cache lifecycle modeling first. After those changes, the proposed trivial loader plus unchanged replay should be a sound path.
