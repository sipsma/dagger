# wcprof OTel Design Review, Pass 2

Reviewed updated local design at `hack/designs/wcprof-otel-design.md` against current tree `b442cd25331d50839748bbc72d222c4c2a56125a`.

## Overall verdict

The updated design fixed several of the previous issues in the right direction. In particular, the new target-before-primitive invariant is the right rule, the exec `containerStart`/`processRun` split now matches native, and the `wcprof.parent` idea is a reasonable way to keep lazy UI parentage while giving the analyzer an explicit causal parent.

I still would not build it as written. The new draft has three material correctness problems:

1. the wait timestamp format has no emitter-known epoch;
2. the link-cap argument is still unsafe because suppressed callers can concentrate many wait links on one ancestor span;
3. the proposed `publishResult` child under an already-ended `call_exec` does not actually charge publication to the `publishResult` class in replay.

There are also two narrower issues: the lazy "UI byte-for-byte unchanged" claim is stronger than the design can guarantee, and the persisted-hit residual gap is real source-wise but is dismissed too casually.

## REAL issues

### 1. HIGH: `wcprof.wait.*_rel_ns` has no defined epoch the emitter can know

Verdict: REAL ISSUE.

The updated doc avoids Cloud JSON float64 precision loss by proposing wait start/end attributes as decimal strings of "trace-relative nanoseconds" (`hack/designs/wcprof-otel-design.md:302-315`) and says the loader rebases them to the same trace epoch it uses for op intervals (`:313-315`, `:761-767`).

The string part is good. The "trace-relative" part is underspecified and, as written, not implementable.

The loader's op epoch is "trace min start" (`hack/designs/wcprof-otel-design.md:761-762`). That value is only known after the loader has all spans. The engine emits the wait link at runtime from `c.wait`, lazy evaluation, or service wait code, before the future trace minimum is knowable. The source gives those emit points local wall-clock timing only:

- cache waits are recorded around the `select` in `dagql/cache.go:3867-3881`;
- lazy waits are recorded at `dagql/cache.go:2935-2943` and `dagql/cache.go:3055-3057`;
- service waits are recorded around `starting.done` in `core/services.go:335-343` and `core/services.go:951-963`.

There is no trace-min-start value in those contexts. OTel span start times themselves are assigned by the SDK when `Start` runs (`go.opentelemetry.io/otel/sdk/trace/tracer.go:144-147`), and Cloud returns span start/end as typed `time.Time` (`internal/cloud/trace.go:88-89`, converted at `internal/cloud/trace.go:312-323`). That does not give the engine a shared trace epoch for link attributes.

If the engine guesses a different epoch than the loader, wait intervals attach at the wrong time. That can change self-time cuts, action ordering, `actWaitJoin` classification, and ultimately counterfactual rankings.

Required design change: define an emitter-known epoch. The simplest fix is to encode absolute Unix nanoseconds as decimal strings; strings already avoid the float64 problem. Other valid options are waiter-span-relative offsets plus the loader adds the waiter's span start, or emitting a root/session epoch attribute that every wait offset references. As written, "trace-relative to loader min start" is not a wire format.

### 2. HIGH: per-waiter links can still exceed caps when suppressed callers collapse onto one parent

Verdict: REAL ISSUE.

The design argues wait links should live on the waiter because "each waiter blocks on only a handful of things" and therefore avoids the default 128 link cap (`hack/designs/wcprof-otel-design.md:283-325`). That is true when every logical caller has its own span. It is false in the exact suppression case the design is fixing.

Repeated cacheable calls are suppressed by `ShouldEmitTelemetry`:

- `dagql/telemetry.go:48-64` returns false for seen cacheable call digests;
- `core/telemetry.go:53-64` then returns the original context unchanged.

The design correctly emits wait links from the cache layer because suppressed callers never enter `AroundFunc` (`hack/designs/wcprof-otel-design.md:485-490`). But if the caller span is suppressed, the link is added to the current ancestor span, not to a per-caller span (`hack/designs/wcprof-otel-design.md:464-471`).

DagQL can resolve many sibling selections concurrently under the same parent context:

- `dagql/server.go:1120-1124` documents parallel selection resolution;
- `dagql/server.go:1144-1163` starts one goroutine per selection and waits for all.

So a single visible parent span can receive one wait link for every suppressed repeated sibling. That can easily be "hundreds of joiners" concentrated on one parent, which is the same shape the design was trying to avoid on the target span.

This matters because the SDK link cap evicts oldest links on overflow:

- default link cap is 128 (`go.opentelemetry.io/otel/sdk/trace/span_limits.go:21-23`);
- overflow drops the oldest links (`go.opentelemetry.io/otel/sdk/trace/span_limits.go:61-68`);
- `AddLink` applies limits while the span records (`go.opentelemetry.io/otel/sdk/trace/span.go:764-793`).

The design's Cloud round-trip test is necessary but not sufficient unless it includes this suppressed-parent fan-in at cap-stress sizes. A small "known augmented trace" can pass while production traces with large parallel repeated selections silently lose wait links.

Required design change: explicitly bound or eliminate this concentration. Options include an unlimited/high enough engine `LinkCountLimit` plus a Cloud stress round-trip that exceeds realistic maximum parent fan-in, forcing lightweight suppressed-caller waiter spans above a threshold, or aggregating repeated waits only if the replay semantics remain exact. The current "per-waiter links are tiny" premise is false for suppressed callers.

### 3. MEDIUM: `publishResult` as a late child does not fix counterfactual attribution

Verdict: REAL ISSUE in the design claim, even though the source shape matches native.

The updated doc says to emit `dagql.publishResult` as a child of `call_exec`, with parent set to the already-ended `call_exec` span context (`hack/designs/wcprof-otel-design.md:447-462`). It also claims the result is `call_exec` children including `dagql.publishResult`, and that this fixes publication-heavy oracle drift (`hack/designs/wcprof-otel-design.md:492-498`).

Native does record `dagql.publishResult` this way:

- `call_exec` ends in the goroutine immediately after `fn` returns (`dagql/cache.go:3700-3706`);
- the caller wait ends when `oc.waitCh` closes (`dagql/cache.go:3875-3881`);
- publication then runs in `oc.initCompletedResultOnce.Do` (`dagql/cache.go:3922-3947`);
- native parents `dagql.publishResult` under `oc.profOpID` using `wcprof.ContextWithOpID(context.Background(), oc.profOpID)` (`dagql/cache.go:3926-3934`).

But replay does not treat a child that ends after its parent as work the parent waits for. Child spawn actions are compiled for every child (`engine/wcprof/wcanalyze/replay.go:159-160`), but the implicit join only joins children whose recorded end is `<= t` (`engine/wcprof/wcanalyze/replay.go:353-369`), and the final join uses the parent's own `EndNS` (`engine/wcprof/wcanalyze/replay.go:389`). For a publication op that ends after `call_exec.EndNS`, `call_exec` will not join it.

Self-time accounting also does not subtract that late child from the caller, because the child is under `call_exec`, not under the caller that is still executing publication. `SelfSegments` subtracts only a span's own children and waits (`engine/wcprof/wcanalyze/graph.go:379-398`), and the caller's explicit wait ended before publication (`dagql/cache.go:3875-3881`).

So the publication interval is not actually charged as a counterfactual dependency of `dagql.publishResult`; it remains effectively caller or ancestor self-time for replay purposes, while also existing as a late child diagnostic op. That may match native's current imperfect shape, but it does not satisfy the doc's claim that the serial miss-path work is charged to the right class.

Required design change: be explicit. Either state that `dagql.publishResult` is emitted only for native parity/diagnostics and not expected to fix counterfactual attribution, or change the model so publication is a synchronous child/wait target of the actual caller/ancestor still blocked during publication. If the latter is the desired behavior, native should be fixed too so the oracle remains meaningful.

### 4. MEDIUM: lazy "UI byte-for-byte unchanged" is stronger than the mechanism supports

Verdict: REAL ISSUE in the stated property; the causal idea is still sound.

The updated lazy design says it keeps the OTel span tree and UI "byte-for-byte unchanged" while minting the `lazy` op under `lazyMu` before `lazyEvalWaitCh` is published (`hack/designs/wcprof-otel-design.md:519-539`, `:576-579`).

That cannot be literally true in all cases.

Today the resume span is started inside the eval goroutine, after `lazyEvalWaitCh` has been published:

- `lazyEvalWaitCh` is set and `lazyMu` is unlocked at `dagql/cache.go:2962-2966`;
- the goroutine starts at `dagql/cache.go:2968`;
- the resume span starts at `dagql/cache.go:2984-2989`;
- its callback wrapper is installed at `dagql/cache.go:2990-2994`.

The SDK records the span start time at `Start` unless a timestamp option is supplied (`go.opentelemetry.io/otel/sdk/trace/tracer.go:144-147`). Moving the same resume/lazy span creation under `lazyMu` changes its start timestamp and live-start export ordering. That may be acceptable for wcprof, and it mirrors native's earlier lazy op creation (`dagql/cache.go:2953-2961`), but it is not byte-for-byte unchanged.

There is also an existence difference. Current code creates no resume span if no original producer span context is captured (`dagql/cache.go:2971-2995`). The updated design says to mint the lazy op unconditionally when telemetry is on, decoupled from whether a producer span context was captured (`hack/designs/wcprof-otel-design.md:531-534`). That adds a span in cases where the current UI trace would have none, unless the design introduces a separate hidden wcprof-only span and leaves the existing resume span alone.

Required design change: specify which span exists. If the existing resume span is reused as the `lazy` op, the design must acknowledge its start time changes and validate the UI impact. If a separate hidden `wcprof.op.kind=lazy` span is added, the design must stop claiming the trace is byte-for-byte unchanged and must specify how it is hidden from dagui. The `wcprof.parent` override itself is fine; the problem is the stronger UI/timing claim.

### 5. LOW: persisted-hit residual gap is source-accurate but not harmless

Verdict: REAL ISSUE in the reasoning, not necessarily a v1 blocker.

The updated doc is correct on two source facts:

- first occurrence persisted/imported hits are not suppressed, because `ShouldEmitTelemetry` only suppresses seen cacheable digests (`dagql/telemetry.go:48-64`);
- the call span wraps the full `GetOrInitCall` (`dagql/objects.go:655-678`), and `GetOrInitCall` reaches persisted hit loading through `lookupCacheForRequest` and `ensurePersistedHitValueLoaded` (`dagql/cache.go:3632-3639`, `dagql/cache_egraph.go:883-915`, `dagql/cache_persistence_import.go:552-703`);
- native does not record a wait inside `ensurePersistedHitValueLoaded`; there is no `wcprof.BeginWait` in that file, while the blocking points are real (`dagql/cache_persistence_import.go:563-578`, `:598-615`, `:658-700`).

The questionable part is the claim that the remaining suppressed-repeat case is "oracle drift, not a wrong counterfactual" (`hack/designs/wcprof-otel-design.md:696-711`).

If a repeated suppressed caller blocks on an in-flight persisted decode or dependency attachment, OTel charges that time to the visible ancestor because there is no caller span. Native charges it to the per-caller call op's self-time because native records call ops for hits. Neither has the ideal decode wait edge, but they can produce different class rankings. Since the design's own validation uses native top-N `RunWhatIfs` as the strongest oracle (`hack/designs/wcprof-otel-design.md:822-839`), this is not merely cosmetic drift if the path is hot.

Required design change: keep it as a seam if desired, but phrase it as a known native/OTel attribution gap that must be bounded by validation. If the persisted-cache fixture shows non-trivial ranking drift, the fix needs to add a real decode target + wait edges to native and OTel, or selectively retain caller spans while a persisted decode/dep attach is in flight.

## NOISE / verified sound decisions

### Target-before-primitive ordering

Verdict: NOISE. The new invariant is correct and grounded in current source.

Native publishes target IDs before waiters can observe the synchronization primitive:

- `call_exec`: native creates `execOp` at `dagql/cache.go:3669-3677`, stores `oc.profOpID` at `dagql/cache.go:3693`, publishes `ongoingCalls` at `dagql/cache.go:3696-3697`, and only unlocks at `dagql/cache.go:3715`;
- lazy: native stores `shared.lazyEvalProfOpID` at `dagql/cache.go:2953-2961`, then publishes `shared.lazyEvalWaitCh` at `dagql/cache.go:2962`, and unlocks at `dagql/cache.go:2966`;
- services: native stores `start.profOpID` at `core/services.go:978-985`, then publishes `ss.starting[key]` at `core/services.go:985`, before unlocking at `core/services.go:986`.

The OTel design should mirror this by stashing target span contexts before publishing `oc`, `lazyEvalWaitCh`, or `starting`. That resolves the lazy wait-target race from the first review.

### `wcprof.parent` as an explicit causal override

Verdict: NOISE. The mechanism is conceptually sound, with the implementation caveats already noted above.

The current lazy code really does re-point callback child spans to the producer:

- `resumedCallbackSpan.SpanContext()` returns the original producer context (`dagql/cache.go:2797-2805`);
- the callback context installs that wrapper over the resume span (`dagql/cache.go:2990-2994`);
- existing tests assert a lazy child span has the original producer as parent, while the resume span is under the trigger (`dagql/cache_test.go:540-552`).

The proposed span-processor discriminator is also technically sound for direct children. The SDK computes the recording span parent from `trace.SpanContextFromContext(ctx)` before calling processors (`go.opentelemetry.io/otel/sdk/trace/tracer.go:87-95`, `:149-160`), and calls `OnStart` synchronously with the original parent context and `ReadWriteSpan` (`go.opentelemetry.io/otel/sdk/trace/tracer.go:66-71`; `go.opentelemetry.io/otel/sdk/trace/span_processor.go:19-21`). `ReadWriteSpan.Parent()` exposes that stored parent (`go.opentelemetry.io/otel/sdk/trace/span.go:35-41`, `:645-649`), and `ReadWriteSpan` includes `trace.Span`, so `SetAttributes` is available (`go.opentelemetry.io/otel/sdk/trace/span.go:88-98`).

Therefore, if the context carries `{producerSpanID, lazyOpSpanID}`, stamping only spans whose stored parent is `producerSpanID` should catch direct re-pointed children and not descendants. Descendants inherit the override value in context, but their stored parent is their actual immediate parent, not the producer.

### "resumedCallbackSpan is the sole parent-diverging wrapper"

Verdict: NOISE, based on current source.

The only production `SpanContext() trace.SpanContext` override on a span wrapper is `resumedCallbackSpan` (`dagql/cache.go:2797-2808`). The other production `ContextWithSpanContext` calls are different shapes:

- executor OTel propagation installs the legitimate `state.causeCtx` for process telemetry (`engine/engineutil/executor_spec.go:751-753`, `:1013-1015`);
- file/directory/changeset code re-anchors to the current span context itself (`core/file.go:828`, `core/file.go:851`, `core/directory.go:1624`, `core/directory.go:1801`, `core/changeset.go:585`);
- error propagation injects origin context into error metadata, not span parentage (`core/exec_error.go:117-132`);
- session attachables intentionally clear telemetry from that registration context (`engine/server/session.go:1349-1353`).

So lazy-only `wcprof.parent` is a reasonable bounded convention.

### Stamping processor registration

Verdict: NOISE as a design seam, not a new issue.

The doc correctly points at the per-client tracer provider setup:

- the provider options are built at `engine/server/session.go:682-689`;
- parent-client exports are additional span processors on the same provider, appended at `engine/server/session.go:709-715`;
- the provider is constructed at `engine/server/session.go:726`;
- live export snapshots happen in `LiveSpanProcessor.OnStart` (`github.com/dagger/otel-go/live.go:25-30`).

Registering the stamping processor before the live processors is sufficient for live snapshots; registering it after them is still sufficient for the ended copy because `OnStart` is synchronous before the span can end. The doc's fixture should catch missing registration.

### Exec engine/user split

Verdict: NOISE. The revised split is correct.

The current native split point is the started callback:

- `profStartNS` is captured before `callWithIO` (`engine/engineutil/executor_spec.go:1270-1277`);
- after `callWithIO` returns, native records `exec.containerStart` as `[profStartNS, startedNS]` and `exec.processRun` as `[startedNS, endNS]`, with `WorkTypeUser` only on process runtime (`engine/engineutil/executor_spec.go:1398-1412`);
- per-phase native executor ops exist as a finer follow-up (`engine/engineutil/executor.go:188-203`);
- `withExec.prepareMounts` and `withExec.applyOutputs` are also native follow-up candidates (`core/container_exec.go:1741-1742`, `core/container_exec.go:2175-2182`).

The design should clarify whether these OTel phase spans are children of an added `exec.run` span or direct children of the current `withExec` span, because current OTel trace propagation uses the `withExec` span context (`core/container_exec.go:1304`; `engine/engineutil/executor_spec.go:751-753`, `:835-837`). That clarification does not undermine the split itself. `work_type=user` belongs only on `exec.processRun`.

### Cloud string attributes and span start/end precision

Verdict: PARTIAL NOISE.

Encoding wait attributes as strings avoids the current Cloud JSON float64 path:

- Cloud link attributes are `map[string]any` (`internal/cloud/trace.go:112-117`);
- JSON numbers are decoded as `float64` and then converted back to OTLP (`internal/cloud/trace.go:368-379`);
- strings remain strings (`internal/cloud/trace.go:370-371`).

Span start/end are not subject to that attribute float64 path in the current client shape; they are typed `time.Time` (`internal/cloud/trace.go:88-89`) and converted with `UnixNano()` (`internal/cloud/trace.go:312-323`).

The real issue is the missing epoch for `*_rel_ns`, covered above.

### First-occurrence persisted hits

Verdict: NOISE for the source claim.

The doc is right that first-occurrence persisted/imported hits are captured by the existing OTel call span. `AroundFunc` only suppresses already-seen cacheable digests (`core/telemetry.go:53-64`, `dagql/telemetry.go:48-64`), and the call span wraps `cache.GetOrInitCall` (`dagql/objects.go:655-678`), including persisted hit loading (`dagql/cache_egraph.go:883-915`, `dagql/cache_persistence_import.go:552-703`). The remaining concern is the repeated/concurrent residual described in the REAL issue above.

### One-trace scope

Verdict: NOISE.

I did not find the updated loader design depending on multi-trace aggregation. It consistently scopes input to one Cloud trace (`hack/designs/wcprof-otel-design.md:731-733`) and explicitly keeps whole-CI-job aggregation out of the loader (`hack/designs/wcprof-otel-design.md:1058-1065`). Scale-out and clock skew remain seams (`hack/designs/wcprof-otel-design.md:1023-1031`), not hidden loader assumptions.

## Bottom line

The revised design is closer, but the new material needs another edit. Fix the wait timestamp epoch, harden or redesign wait-link cap safety for suppressed-parent fan-in, and stop claiming late-child `publishResult` fixes counterfactual attribution. The `wcprof.parent` override is not the main problem; with the direct-parent discriminator and targeted fixtures, it is a defensible explicit emit mechanism.
