# wcprof x OTel publishResult emit-gap review

## Verdict

The framing is directionally correct: `dagql.publishResult` roots are a data/emit faithfulness gap, not something the replay should compensate for. I cannot independently recompute the empirical `330/331` count because the raw trace is not in this worktree; the trace evidence available to me is the implementer/lead report in `hack/designs/wcprof-otel-publishresult-handoff.md:39-49`. But the source strongly supports the diagnosis: if a `publishResult` span had a resolvable parent edge in the loader input, the loader would preserve it mechanically.

The fundamental fix should be an emit/front-end data fix: make each `dagql.publishResult` span carry the real `call_exec` parent edge. Prefer a normal OTel parentId over `wcprof.parent`; `wcprof.parent` is the lazy UI-divergence escape hatch, and using it here would broaden that mechanism without a UI reason.

## REAL Issues

### HIGH: `dagql.publishResult` roots are an unfaithful-data gap the current gate misses

The design contract says `dagql.publishResult` is not a root. It is a late native-parity diagnostic child of `call_exec`: design `hack/designs/wcprof-otel-design.md:516-524` says to bracket `initCompletedResult` with an OTel `dagql.publishResult` whose parent is the already-ended `call_exec` `SpanContext`. The design also states the loader is mechanical and uses only `wcprof.parent ?? parentId` for causal parentage (`hack/designs/wcprof-otel-design.md:899-907`).

Native does record the same parent relation. In the implementation tree at `98ee73047c`, `dagql/cache.go:4003-4011` starts the native `dagql.publishResult` op with `wcprof.ContextWithOpID(context.Background(), oc.profOpID)`, i.e. parented under the shared execution op.

The OTel emit path creates and stashes the intended `call_exec` parent target:

- `98ee73047c:dagql/cache.go:3731-3734` starts `call_exec`;
- `98ee73047c:dagql/cache.go:3755-3758` stores `oc.execSpanCtx = execSpan.SpanContext()`;
- `98ee73047c:dagql/cache.go:4013-4018` checks that `oc.execSpanCtx` is valid before emitting `publishResult`.

But the actual `publishResult` start does not use the stashed parent. It calls `beginOTelPublishResult(context.WithoutCancel(oc.sharedWorkCtx))` at `98ee73047c:dagql/cache.go:4016-4018`, and the helper starts a child of whatever span is ambient in that context (`98ee73047c:dagql/otelprof_hooks.go:68-83`). That is not fail-closed against exactly this bug: the code has the intended parent `SpanContext`, proves it is valid, and then relies on ambient context state instead of forcing that context as the parent.

The loader is not reclassifying a valid parented span as a root. It maps a span to an op parent by direct lookup of the emitted causal parent span id:

- `98ee73047c:engine/wcprof/wcotel/loader.go:282-287` computes `parentID := opIDBySpan[causalParentSpanID(s)]`;
- `98ee73047c:engine/wcprof/wcotel/loader.go:442-450` defines `causalParentSpanID` as `wcprof.parent` if present, else `s.ParentID`;
- `98ee73047c:engine/wcprof/wcanalyze/graph.go:283-300` makes an op a root only when it has no resolvable parent after that mechanical wiring.

So a `publishResult` root means the parent data is absent or unresolved in the span set. If the input was local `otlpdump`, the dumper itself preserves the OTLP `ParentSpanId` as `parentId` (`98ee73047c:hack/otlpdump/main.go:109-117`), so a local fake root would point back to engine/SDK emit data rather than the dumper. If the count came from the Cloud trace API path, the same conclusion is still data-side, but the immediate fix site could be Cloud/API conversion if raw OTLP had the parent and the fetched trace lost it. Either way, the loader should not infer the edge.

The current structural gate has no check for this. It reports `RootCount` (`98ee73047c:engine/wcprof/wcotel/gate.go:37-43`) but only fails on cycles, self/interval bounds, wait-loss, dropped links, and fallback anchors (`98ee73047c:engine/wcprof/wcotel/gate.go:121-145`). A graph with hundreds of explicit internal passthrough roots can therefore pass.

Impact: baseline makespan can still match recorded time because independent roots are now anchored at their own recorded starts, but multi-root what-ifs operate on an unfaithful causal structure. That is exactly the model/data disharmony Erik is trying to eliminate.

### MEDIUM: Existing tests do not assert the parent edge that matters

The emit-path test exercises `beginOTelPublishResult(execCtx)` directly (`98ee73047c:dagql/otelprof_hooks_test.go:131-138`) and then asserts only the `wcprof.op.kind` and passthrough attributes for `publishResult` (`98ee73047c:dagql/otelprof_hooks_test.go:162-168`). I found no parent assertion for `dagql.publishResult` in `dagql` or `engine/wcprof/wcotel`.

The fixture oracle assumes the right shape by hand-building `publishResult` as a child of `call_exec` (`98ee73047c:engine/wcprof/wcotel/chunk2_test.go:170-184`, `:268-273`), but that does not prove the real cache path emits that shape after `call_exec` has ended. The missing assertion explains how this gap escaped.

## Noise / Non-Issues

### Not a loader-root-detection bug

The loader has no heuristic root detection here. It uses emitted `wcprof.parent` or OTel `parentId`; if either resolves to an op, `wcanalyze.Build` wires the parent. A fake root is therefore not a loader choice unless the parent span id is absent or unresolved in the input data.

### Not parentless by design

Both the design and code comments say `publishResult` should be a child of `call_exec` (`hack/designs/wcprof-otel-design.md:516-524`, `98ee73047c:dagql/otelprof_hooks.go:68-75`, `98ee73047c:dagql/cache.go:4013-4014`). Treating it as parentless would contradict the approved design and native parity.

### Do not reintroduce chaining or a replay fallback

The item-3 root model is aligned with the governing principle. `Run` now anchors every root at its recorded start before finishing any root (`98ee73047c:engine/wcprof/wcanalyze/replay.go:367-383`), and the replay comment explicitly rejects root chaining/inferred temporal dependence (`98ee73047c:engine/wcprof/wcanalyze/replay.go:38-52`). The `publishResult` symptom is not a reason to compensate in analysis.

## Fundamental Fix

Fix the emitted data so `publishResult` has the normal OTel parentId of its `call_exec` span.

Concrete shape:

1. At publication, use the already-stashed `oc.execSpanCtx` as the parent for `dagql.publishResult`.
2. Preserve the tracer provider from the real recording context. Because `dagql.Tracer(ctx)` derives the provider from the current span (`98ee73047c:dagql/tracing.go:11-12`), do not replace the current span with a bare `trace.ContextWithSpanContext` before obtaining the tracer. Capture the tracer from `oc.sharedWorkCtx`, then start with a context whose current span context is `oc.execSpanCtx`, or store the actual `trace.Span` if that is cleaner.
3. Add an emit-path test on the real cache publication path, or at minimum extend the helper test to assert `publishResult.Parent().SpanID() == callExec.SpanContext().SpanID()` after the parent span has already ended.
4. Add a Cloud/local round-trip check for this specific edge: every emitted `dagql.publishResult` must either have `parentId == call_exec.spanId` or a deliberately documented equivalent causal parent. If local OTLP has it and Cloud fetch loses it, the fix belongs in the Cloud trace front-end rather than the engine emitter.

I would not use `wcprof.parent` as the primary fix. That attribute is documented as a lazy-only override for spans whose UI `parentId` is intentionally non-causal (`hack/designs/wcprof-otel-design.md:426-473`, `98ee73047c:engine/telemetryattrs/attrs.go:60-68`). `publishResult` has no UI-visible reason to keep a false parentId or no parentId; the honest data is the ordinary OTel parent edge. Using `wcprof.parent` would make the analyzer correct while still leaving the trace tree structurally dishonest to every other consumer, and it would weaken the lazy-only guardrail.

`wcprof.parent` is acceptable only as a fallback if there is a proven exporter/UI constraint that makes normal OTel parentId impossible. If that happens, the design must explicitly broaden §3.0.2 beyond lazy and add tests proving descendants are not flattened or over-reparented.

## Gate Posture

Add a hard-failing faithfulness signal for impossible roots. The narrow immediate check is:

- root with `wcprof.op.kind == "internal"` and `class == "dagql.publishResult"` ⇒ fail.

The better general check is an allowlist of root-capable op shapes. Design §3.5 says the natural OTel root is the per-query `POST /query` span (`hack/designs/wcprof-otel-design.md:803-808`); internal passthrough work such as `call_exec`, `dagql.publishResult`, exec phases, lazy work, and service starts should not be independent roots in a faithful single trace. The gate should count and sample `UnexpectedRoots` / `InternalRoots` and fail before ranking.

Also add an unresolved-parent provenance counter in the loader: when a span carries a non-empty `parentId` or `wcprof.parent` but that id is absent from the span set, count it. That catches front-end truncation separately from truly parentless internal roots. It would not replace the internal-root check, because this case may be an empty parentId rather than an unresolved one.

This is consistent with the prior "enforce on the precondition" posture. A finite replay sweep cannot prove causal soundness; structural faithfulness counters should be zero by construction for faithful data, and non-zero should stop trusted ranking until emit/front-end data is fixed.

## Baseline / `-2.4%` Assessment

The implementer's baseline correction is sound. The old chaining model propagated simulated drift from one root into later root starts; removing that inference and anchoring each root independently at recorded start (`98ee73047c:engine/wcprof/wcanalyze/replay.go:367-383`) makes the baseline move toward the data rather than away from it. The reported OTel change from `-2.4%` drift to exact recorded makespan is therefore plausible and consistent with the code change.

Baseline-vs-recorded drift should become a faithfulness check, but not the only one. For complete, closed, single-trace augmented captures, factor-1 replay should match the recorded makespan within a tight tolerance; non-zero drift is a strong signal of missing edges, malformed structure, or a model bug. However, this `publishResult` case shows exact baseline is not sufficient: a fake-root structure can still reproduce the recorded makespan while producing wrong multi-root counterfactuals. Keep baseline drift as a hard validation gate for corrected fixtures/round-trips, and keep structural root/parent/wait counters as independent hard gates.

## Required Human / Implementer Decisions

1. Confirm whether the `330/331` count came from local `otlpdump` or Cloud trace API. If Cloud-only, compare the same run against raw OTLP to locate whether the edge is lost in engine emit or Cloud conversion.
2. Prefer normal OTel parentId for `publishResult`. Use `wcprof.parent` only if normal parentage is proven impossible, and then update the design guardrails accordingly.
3. Add a hard gate for impossible internal roots before treating any OTel ranking as trusted.
