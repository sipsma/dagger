# wcprof OTel skip implementation plan v2 review - Codex fresh

Reviewed plan:
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-implementer-a7daa7c9-239fd90a/hack/designs/wcprof-otel-skip-impl-plan.md`

Code checked in that worktree at `4585bf413d5ad09af918b39b0e2b62e95ad02006`.

## Verdict

v2 fixes the main round-1 design problems. The separate, debug-independent, static profiling predicate is the right direction; gating native and OTel from the same `SkipProfile` bit keeps the analysis side zero-inference; and the target-flag rule for lazy is the correct way to avoid cross-recipe dangling waits.

I would still not call the plan complete as written. I found one concrete coverage hole: `dagql/cache.go:2349-2369` constructs a `CallRequest` and calls `GetOrInitCall` directly, bypassing `AroundFunc`, so N4 is not resolved until that synthetic/Nth path gets an explicit skip provenance rule. I also want the receiver-type predicate qualified or tested against schema-name collisions before landing.

## Findings

### High: N4 stamp coverage still misses `Result.LoadNthValue`

The plan relies on `AroundFunc` stamping `req.SkipProfile` before `GetOrInitCall` (`wcprof-otel-skip-impl-plan.md:111-126`) and makes zero residual introspection a hard validation check (`:266-269`). That covers the normal resolver path: `ObjectResult.preselect` builds the request, calls telemetry/AroundFunc at `dagql/objects.go:655-665`, then calls `GetOrInitCall` at `dagql/objects.go:678`.

There is another non-test recording path into `GetOrInitCall`: `Result.LoadNthValue` builds a new request directly at `dagql/cache.go:2349-2355` and calls `cache.GetOrInitCall` at `dagql/cache.go:2369`. That request never goes through `AroundFunc`, so `SkipProfile` would default false. This is especially relevant to the reflection class because many skipped schema calls return lists (`Function.args`, `ObjectTypeDef.functions`, `ObjectTypeDef.fields`, `EnumTypeDef.members` in `core/schema/module.go:471-648`), and loading an Nth element is part of walking those lists.

This does not require a loader/replay change. The clean emit-side rule is to propagate the producer cut into this synthetic request: when deriving the Nth request from `r.shared`, set `req.SkipProfile` from the parent/result provenance, likely `r.shared.profSkip` once that field exists. That preserves subtree closure without evaluating the core predicate under cache locks. Add an explicit test for a skipped reflection list whose element is loaded via `LoadNthValue`: no `call_exec`/`publishResult`, no waits, and zero residual introspection.

Without this, the design can still be self-consistent enough to pass orphan/unresolved gates, but it may fail the actual volume objective and the §9 zero-residual-introspection assertion.

### Medium: raw receiver `NamedType` needs a collision proof or qualifier

The v2 predicate skips any call whose immediate receiver type name is in the reflection set (`wcprof-otel-skip-impl-plan.md:88-96`). The over-cut risk section acknowledges name collisions (`:282-285`), but treats them as contained because only profiler granularity is lost.

That is too casual for the goal. `ResultCallType` stores only `NamedType` (`dagql/result_call_frame.go:24-28`), and module objects report their schema name directly (`core/object.go:951-955`). If a real module object can be named `Function`, `TypeDef`, `ObjectTypeDef`, etc., a receiver-name-only predicate would skip all profiler detail for its real methods, including user work. Normal UI telemetry would remain, but wcprof would violate "user work first-class."

This may be impossible because schema object names share one namespace and `Server.InstallObject` returns an existing type for duplicate names (`dagql/server.go:540-550`), but the plan should prove that rather than relying on a note. I would add either:

- a classifier qualifier proving the receiver is the core reflection type, not merely a schema type with the same name; or
- a synthetic module-name-collision test showing such objects cannot be installed/called, plus a comment making that invariant explicit.

The one-time audit that every core reflection field is metadata (`wcprof-otel-skip-impl-plan.md:95`, `:270`, `:284`) is otherwise sound: the listed `dagql.Fields[*core.<reflection type>]` blocks in `core/schema/module.go:471-648` are type-system accessors/builders, while module loading stays on `Query.moduleSource` / `ModuleSource.asModule`, outside the reflection receiver set.

### Medium: the memoization plan does not remove the expensive part unless the key is available pre-resolution

I agree with v2 that a `!IsRecording` short-circuit is not a production mitigation: production telemetry records, and native must also be considered (`wcprof-otel-skip-impl-plan.md:217-219`, `:296`). The receiver-type predicate is much cheaper than the old chain walk.

But the suggested `(receiverTypeName, field)` memoization only helps after computing `receiverTypeName`. Today that computation is the part that can hit cache/egraph state: `ResultCall.ReceiverCall` resolves refs through `resultCallByResultID` (`dagql/result_call_frame.go:575-607`), which takes `egraphMu.RLock` (`dagql/cache.go:1377-1387`). If the implementation has to take that read lock before it can build the memo key, the memo saves set membership/string checks but not the lock cost.

This is not a design blocker because §9 already requires measuring classification cost (`wcprof-otel-skip-impl-plan.md:277`). If it is hot, the optimization likely needs a key available before receiver resolution, such as receiver result ID/frame identity to cached type name, or another safe way to carry receiver type in the request.

### Low: imported/persisted default-false is acceptable only as a measured volume edge

The N2 provenance rule is otherwise corrected: adoption leaves the existing `sharedResult` flag intact, existing-frame copies copy the source flag, and request-frame creation uses `req.SkipProfile` (`wcprof-otel-skip-impl-plan.md:167-173`). That matches the code paths in `initCompletedResult`: canonical adoption at `dagql/cache.go:4130-4146`, existing-frame copy at `dagql/cache.go:4149-4153`, and request-frame storage at `dagql/cache.go:4159-4162`.

For imported/persisted results, v2 explicitly allows `profSkip=false` by default and calls it a volume edge, not a correctness edge (`wcprof-otel-skip-impl-plan.md:173`, `:285`). I agree with that classification: default false means the op exists and waits resolve, so there is no no-inference violation. It can reintroduce some introspection lazy volume, so the warm/imported capture in §9.1/§9.6 must actually exercise persisted lazy/reflection data before this is considered resolved.

If recompute is chosen instead, the plan needs a package-boundary story, not just a lock-safety story: the predicate lives in `core`, while import and lazy registration are in `dagql` (`dagql/cache_persistence_import.go:164-175`, `dagql/cache.go:2779-2792`). Persisting the flag with the result frame or explicitly injecting a classifier would be cleaner than calling back upward from `dagql`.

## Change Review

N1 is resolved in the plan. Gating the outer native `OpKindCall` at `dagql/cache.go:3559-3565` is necessary for native/OTel symmetry, and the proposed nil-op path is safe because `wcprof.Op` methods are nil-safe (`engine/wcprof/record.go:124-139`, `:175-178`).

N2 is resolved conceptually for the main live paths. The invariant "profSkip travels with resultCall" is the right invariant. The implementation still has to copy the flag anywhere a `sharedResult` is cloned or rebuilt, including `newDetachedResult` (`dagql/cache.go:1799-1811`), content-digest wrapping (`dagql/cache.go:2431-2448`), and session-resource wrapping (`dagql/cache.go:2531-2569`). The plan names these sites, which is enough for design review.

N3 is resolved. The plan correctly stops using the singleflight same-recipe proof for lazy and instead gates lazy ops and waits on the target shared result's stored flag (`wcprof-otel-skip-impl-plan.md:142-150`, `:192-203`). The named "user-wait loss" in §4.4 is a real coarsening, but it is an emit choice, not analysis inference.

N4 is not fully resolved because of `LoadNthValue` above. The plan's §9 zero-residual-introspection check is the right backstop, but the design should name this direct path and how its `SkipProfile` is derived.

The three implementer pushbacks are mostly correct:

- `!IsRecording` is not a production mitigation; measure the receiver-resolution cost instead. Caveat: `(receiverTypeName, field)` memoization may not avoid the egraph read lock.
- Leaving the chunk3 parentless-`publishResult` fix out of this change is correct. The skip fix can reduce count, but kept survivor parentage is a separate emit issue (`wcprof-otel-skip-impl-plan.md:213-215`).
- Profiler-skip being broader than UI suppression is correct. A directly called reflection accessor may keep a normal `dag.call` while wcprof skips `call_exec`/`publishResult`; the loader handles present normal spans as ops (`engine/wcprof/wcotel/loader.go:217-254`, `:299-310`), so this does not create orphans.

## Holistic Pass

The core design still aligns with the governing principle. Loader and replay stay unchanged; the engine chooses to emit a smaller graph that is internally self-consistent. Parent re-homing is a property of context propagation, not a loader heuristic. Singleflight is closed by a pure recipe cut. Lazy is closed by target-flag gating. Invalid-target detection remains loud because skipped is represented by an explicit bool, not by treating an invalid `SpanContext` as harmless.

The volume-regression fix should work if the coverage hole is closed. The root cause was wcprof adding `call_exec` + `publishResult` for cache misses that normal telemetry deliberately suppresses. Gating both spans and all waits on the same static flag removes that amplifier while retaining the legitimate always-on second-source cost for real cache misses. The plan is right not to chase "equal to main" as the success metric.

This is moderate, localized complexity. The only analysis-side complexity is zero; all complexity is in emit plumbing and validation. That is the right trade for this project. The merge bar should be the §9 checks plus the extra `LoadNthValue` residual test: gate `0/0`, zero residual reflection/root `call_exec`, no exec/service over-cut, user `processRun` self-time unchanged, and no BSP drops on the module-load workload.

## Final Answer

Implement v2's architecture, but do not land it until `Result.LoadNthValue`/direct `GetOrInitCall` stamping is addressed and the receiver-name collision risk is proved or guarded. With those handled, the plan remains simple enough, preserves user work, and keeps the analysis rational with zero loader/replay inference.
