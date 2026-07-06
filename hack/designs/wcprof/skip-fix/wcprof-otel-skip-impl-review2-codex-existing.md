# wcprof OTel skip implementation plan v2 review - Codex existing

Reviewed plan: `/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-implementer-a7daa7c9-239fd90a/hack/designs/wcprof-otel-skip-impl-plan.md` at `4585bf413d`.

## Verdict

v2 fixes the main round-1 problems: the predicate is now complete enough for the measured schema-walk volume, debug-independent, and separated from normal telemetry; N1 and the lazy target-flag correction are real; the validation plan is much stronger.

I would still not implement it exactly as written. There is one new high-severity semantic hole: with zero loader changes, a direct reflection accessor that still emits a normal `dag.call` span is not actually skipped by the OTel profiler. The loader includes ordinary `dag.digest` spans as `OpKindCall`, so native and OTel can diverge precisely where v2 says "UI span kept, profiler skipped." There is also a medium N2/provenance completeness issue: the plan's invariant is right, but the concrete `sharedResult` copy/import story is still under-specified across all `storeResultCall` paths and has a layering problem for import recomputation.

## REAL ISSUE 1 - HIGH - "UI span kept, profiler skipped" is false with the current loader

v2 deliberately leaves `introspectionInfo`/normal telemetry untouched and says a directly-called reflection accessor can keep its normal `dag.call` UI span while the profiler skips it (`wcprof-otel-skip-impl-plan.md:98-101`, `:293-298`). That does not hold with "zero loader/replay change."

`core.AroundFunc` normal spans carry both `dagger.io/dag.digest` and `dagger.io/dag.call` (`core/telemetry.go:83-86`) and are started as normal OTel spans (`core/telemetry.go:136`). The wcotel loader assigns op IDs to every deduped span, with no kind filter (`engine/wcprof/wcotel/loader.go:251-254`), and classifies any span with `DagDigestAttr` as `OpKindCall` (`engine/wcprof/wcotel/loader.go:444-446`).

So if `profileSkip(TypeDef.asObject) == true` but normal telemetry emits `TypeDef.asObject`, OTel analysis still contains a `call` op for that accessor. Meanwhile v2 gates native's outer `OpKindCall`, inner `call_exec`, `publishResult`, and waits on `SkipProfile` (`wcprof-otel-skip-impl-plan.md:177-190`). That means:

- OTel has a normal `call` op.
- native has no corresponding op.
- the cross-source oracle diverges on direct visible reflection calls.
- the claim "profiler-skip superset UI-suppress" is not implementable without either suppressing the normal span too or teaching the loader to ignore an emitted skip marker.

This does not block the immediate module-load volume fix, because the forensics class appeared with `dag.call=0`. But it invalidates v2's broader scope assertion and its test expectation that direct `TypeDef.asObject` keeps normal `dag.call` while profiler-skipped (`wcprof-otel-skip-impl-plan.md:251`).

Clean options:

1. Suppress normal telemetry for the profiler-skipped reflection class too, by extending `introspectionInfo` or an equivalent normal-telemetry gate. This preserves zero loader change and makes native/OTel agree, but it is a UI behavior change.
2. Keep normal UI spans and add an explicit emitted skip marker that the loader honors. That is still emit-side data, not inference, but it is a loader change and must be admitted as such.
3. Narrow the profiler skip to calls that normal telemetry already suppresses. That avoids UI divergence but gives up v2's direct-reflection skip claim.

As written, the plan tries to have both "normal span kept" and "profiler absent" with no loader change; current source cannot do that.

## REAL ISSUE 2 - MEDIUM - N2 invariant is right, but the concrete provenance audit is incomplete

The new invariant is correct: `sharedResult.profSkip` must travel with the stored producer `resultCall`, not blindly with the current request (`wcprof-otel-skip-impl-plan.md:165-173`). The three `initCompletedResult` branches are real: adopt canonical (`dagql/cache.go:4130-4141`), copy an existing frame (`dagql/cache.go:4149-4153`), or store the request frame (`dagql/cache.go:4159-4161`).

But the plan still lists only some construction/copy sites (`wcprof-otel-skip-impl-plan.md:172`) and should require a full audit of every `storeResultCall` / frame-copy path. Current source has additional relevant sites:

- `attachResult` normalizes and stores a frame back onto the same shared result (`dagql/cache.go:1965-1976`).
- `NthValue` stores a forked child frame on a detached child shared result (`dagql/cache.go:2349-2357`).
- `WithContentDigest`/copy-style flows create a new `sharedResult` and store/fork the frame (`dagql/cache.go:2431-2436`, `:2531-2536`, `:2567-2569`).
- imports create a `sharedResult` and store the persisted frame (`dagql/cache_persistence_import.go:164-176`).
- persistence encoding creates a temporary shared result and stores a snapshot frame (`dagql/cache_persistence_worker.go:430-439`).

Some of these may be behaviorally irrelevant to lazy profiling, but the invariant says every frame copy must carry the bit. The implementation should make this hard to miss, not rely on comments. Since `dagql` cannot call a `core` predicate directly, "recompute at import" (`wcprof-otel-skip-impl-plan.md:173`, `:285`) is also not a cleanly specified option. It needs either a callback seam, a persisted/stored bit, or an explicit decision to accept default-false import behavior.

My recommendation for the open import item: accept default-false for imported/persisted results in v1 unless §9 shows real volume. Recomputing in `dagql` violates the layering that motivated `CallRequest.SkipProfile`; a callback or persistence schema change is more complexity than this bounded volume edge deserves. But the plan should state that as the decision, not leave "recompute if lock-safe" as an underspecified path.

## REAL ISSUE 3 - MEDIUM - The receiver-type classifier over-cuts more than "plain accessors"; the audit should include builder methods that load IDs

The receiver-type predicate now skips every field on the 11 reflection types (`wcprof-otel-skip-impl-plan.md:88-96`). That correctly catches the measured misses, and the plain accessor implementations are trivial metadata returns (`core/schema/module_typedef_canonical.go:10-151`). However, the full receiver-type rule also skips builder methods such as `Function.withArg`, `Function.__withReturnType`, `ObjectTypeDef.__withFunction`, `InterfaceTypeDef.__withFunction`, and `FieldTypeDef.__withTypeDef` (`core/schema/module.go:418-650`).

Those methods still look like metadata, not user container work, but several load IDs or do schema lookups: e.g. `functionWithArg` loads a `TypeDefID` and source map (`core/schema/module.go:1557-1654`), `functionWithReturnType` loads a return `TypeDefID` (`core/schema/module.go:1671-1682`), and `interfaceTypeDefWithFunction` loads a `FunctionID` (`core/schema/module.go:1874-1885`). The plan's §9.4 over-cut audit is therefore not optional. It needs to explicitly cover both accessors and builders, not just the residual field list.

I do not see evidence these methods run container/exec/module-load work. The real module-loading work remains on non-reflection receivers like `Query.moduleSource` and `ModuleSource.asModule`, as v2 says. This is a validation requirement, not a blocker.

## REAL ISSUE 4 - LOW/MEDIUM - The `!IsRecording` pushback is overstated

The implementer pushback says the `!IsRecording` short-circuit is "useless in production" (`wcprof-otel-skip-impl-plan.md:217-219`, `:295-296`). For the production/Cloud path that caused the regression, yes: production is recording, so the guard will not mitigate the 33k-call path. Receiver-type classification and optional `(receiverTypeName, field)` memoization are the real mitigations there.

But the guard is not universally useless. A correct guard of `trace.SpanFromContext(ctx).IsRecording() || wcprof.Enabled(ctx)` would avoid the receiver lookup when neither OTel nor native profiling can consume the bit. That helps telemetry-off tests and non-recording local paths. I would keep it if cheap and clearly label it as a non-production fast path; I agree it should not be sold as the main mitigation.

## Changes Verified / Noise

- **Predicate flip:** good. The receiver-type rule covers the known misses from round 1, including `Function.returnType`, `FunctionArg.typeDef`, `ObjectTypeDef.fields/functions/constructor`, `FieldTypeDef.typeDef`, `ListTypeDef.elementTypeDef`, and `EnumTypeDef.members` (`core/schema/module.go:471-650`; implementations at `core/schema/module_typedef_canonical.go:10-151`). It also removes the debug-baggage non-recipe input. This correctly resolves the round-1 named-list issue.
- **Debug-orphan retraction:** correct. The loader maps all spans to op IDs (`engine/wcprof/wcotel/loader.go:251-254`) and increments `OrphanedParents` only when the parent span is absent (`engine/wcprof/wcotel/loader.go:299-310`). The v1 orphan rationale was false.
- **N1 outer native `OpKindCall`:** correct. The outer native op is started in `getOrInitCall` before the inner call (`dagql/cache.go:3559-3565`). If it is not gated, native keeps introspection call rows after OTel drops `call_exec`. Nil op methods are safe, including `ID()==0` (`engine/wcprof/record.go:63-65`, `:124-130`).
- **Singleflight target-side gating:** correct. Singleflight sharing is keyed by `callKey` plus session `ConcurrencyKey` (`dagql/cache.go:3674-3677`; session id set at `dagql/objects.go:600-604`), waits route through `c.wait` and currently emit both native and OTel waits (`dagql/cache.go:3694-3703`, `:3935-3958`). A stored `oc.profSkip` read by waits is the right closure mechanism.
- **N3 lazy correction:** correct and important. Lazy forcer and producer are different recipes; the shared-recipe proof does not apply. The producer `resultCall` is loaded from the shared result (`dagql/cache.go:2980`), lazy joiner waits currently use shared lazy state (`dagql/cache.go:2951-2973`), and the lazy op is minted before `lazyEvalWaitCh` publication (`dagql/cache.go:2997-3022`). Gating every lazy wait on `shared.profSkip` is load-bearing.
- **`wcprof.parent` boundary:** correct. The stamping processor only stamps direct re-pointed children whose parent span is the producer span (`dagql/otelprof_lazy.go:88-101`), so if no lazy op is minted there is no `wcprof.parent` into a skipped lazy op.
- **N4 stamp coverage:** mostly acceptable with §9 as a hard backstop. Engine/core servers register `AroundFunc` in the expected places (`core/modtree.go:585-589`, `core/schema_build.go:102-107`, `core/schema/coremod.go:43-47`, `core/sdk/module.go:72-78`). `cmd/introspect` creates a cache/server without that hook but is not the recording engine path (`cmd/introspect/introspect.go:40-45`). The zero-residual assertion is still necessary.
- **N7 parentless `publishResult`:** I agree it is orthogonal to the skip volume fix. But once an internal-kind-root signal lands, the skip fix cannot claim "fully clean" until the separate survivor parentage fix also lands.
- **Exec/service hard check:** right disposition. Exec and service emit outside `dagql` and gate only on recording (`engine/engineutil/otelprof.go:77-80`, `engine/engineutil/executor.go:133-142`, `core/services.go:995-1011`, `:1027-1035`); validation must prove residual exec/service spans are real kept work.

## Open Items

- **Import recomputation:** do not recompute in `dagql` unless a clean callback/persisted-bit seam is designed. For v1, default-false imported results are acceptable as a bounded volume edge, provided §9 includes an imported/persisted workload and proves no meaningful schema-lazy volume remains.
- **Over-cut schema audit:** I confirm the broad direction: current reflection-type fields are metadata/schema operations, not container process work. The audit must include builder methods that load other reflection IDs, not only trivial accessors.

## Bottom Line

The v2 architecture is still aligned with the governing principle: emit a smaller faithful graph; do not ask the loader or replay to infer. But the separate-predicate/no-loader-change combination has to be corrected for direct normal `dag.call` spans. Decide whether profiler-skip is allowed to suppress normal telemetry too, or add explicit emitted skip data that the loader honors. After that, tighten N2 with a full `storeResultCall` audit and make import default-false an explicit v1 decision.
