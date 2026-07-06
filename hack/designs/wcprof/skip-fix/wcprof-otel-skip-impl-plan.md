# wcprof × OTel — implementation plan: skip the introspection class to fix the telemetry-volume regression

**Author:** skip-fix implementer
**Status:** design for council re-review (v2) — NO product code written this turn.
**Branch reviewed:** `wcprof-otel-skip-implementer-a7daa7c9` @ HEAD `4585bf413d` (identical emit to the chunk4 worktree).
**Inputs to v2:** the eight council reviews of v1 (`hack/designs/wcprof-otel-skip-impl-review-*.md`). Every claim below — mine and the council's — was re-verified against the current branch; file:line are this worktree.

---

## Revision note — what changed from v1 (and why)

The council **unanimously approved the architecture** (static, work-keyed cut; `CallRequest.SkipProfile` stamped in `AroundFunc`; gate-on-the-target's-stored-flag; distinct-from-invalid; re-homing; native+OTel symmetry; zero loader/replay change). v2 makes the **one agreed predicate change** and closes the **gaps v1 missed**:

1. **PREDICATE FLIP (unanimous).** Replace the v1 "extend the debug-gated `introspectionInfo`, named accessors" with a **separate, debug-INDEPENDENT, receiver-TYPE** profiling predicate (§3). My v1 debug-orphan justification (old §10) was **mechanically false** — verified against the loader — and is retracted (§3.1).
2. **N1 — outer native `OpKindCall` (HIGH).** v1's native-gate list missed the outer `wcprof.BeginOp(OpKindCall)` at `cache.go:3562`; gate it too or the oracle breaks (§5.2).
3. **N2 — lazy `profSkip` provenance (HIGH).** `sharedResult.profSkip = req.SkipProfile` is wrong on result *adoption/copy* paths; derive it from the **stored producer `resultCall`** (§5.1).
4. **N3 — lazy self-consistency (MED).** The §4.2 "waiter shares the recipe" proof does **not** cover lazy (forcer ≠ producer); lazy is closed by **target-flag gating**, which is load-bearing, not "robustness" (§4.2b). Plus a named user-wait-loss decision (§4.4).
5. **N4 — stamp coverage (MED).** Affirmatively verify `AroundFunc` covers every recording path; add a zero-residual-introspection assertion (§9).
6. **N6/N7/N8/D/E** — over-cut audit, parentless-`publishResult` cross-reference, success metric, perf measurement, exec/service hard check (§3.3, §5.6, §9, §10).
7. **§9 elevated to the correctness centerpiece** — the "self-consistent by construction" elegance now rests on plumbing *completeness* (N1/N2/N4 + lazy target-flag), so the empirical gate/zero-residual assertions are the real proof, not an afterthought (§9).

---

## 0. TL;DR

- **Feasible and clean.** Skip the introspection / schema-building class at the wcprof OTel (and native) emit via a **static, work-keyed** cut: zero loader/replay change, no new emit shape, no new wait vocab, no `ongoingCalls` key split.
- **The load-bearing idea (singleflight):** to wait on cache key `K` you must call `K`'s field, so a waiter's skip-class equals its target's — a non-skipped op can never hold a singleflight wait into the skipped set (§4.2). For **lazy**, the forcer is a *different* recipe than the producer, so consistency rests instead on **gating every lazy wait on the target's stored flag** (§4.2b). Parent edges are safe by natural re-homing in both (§4.1).
- **The predicate is a SEPARATE, debug-independent, receiver-TYPE classifier** in `core`: skip any call whose **immediate receiver type** is a reflection type {`Function`, `FunctionArg`, `TypeDef`, `ObjectTypeDef`, `InterfaceTypeDef`, `InputTypeDef`, `FieldTypeDef`, `ListTypeDef`, `ScalarTypeDef`, `EnumTypeDef`, `EnumMemberTypeDef`} **or** a Query-level introspection root field (`__schema`, `currentTypeDefs`, `function`, `typeDef`, `sourceMap`, `__*TypeDef`, …). It is a pure function of the recipe (receiver type + field, both in the digest), so the §4 acid test holds; it leaves `introspectionInfo`/normal-telemetry **untouched** (§3).
- **Plumbing unchanged from v1:** stamp `CallRequest.SkipProfile` in `AroundFunc` (lock-safety basis confirmed real), store per-shared-work flags, gate the target's flag, keep the flag distinct from `execSpanCtx.IsValid()`, skip native + OTel from one decision.
- **Excluded:** `ShouldEmitTelemetry` dedup (correctness — caller-state-dependent), `isMeta` and `NoTelemetry` (pragmatic). The chunk4 "they orphan their children" rationale is wrong (re-homing); the conclusion stands (§3.3).
- **This is not the BSP fix.** It removes the ~33k-span amplifier; a legitimate heavy user-work burst can still overflow the 2048-slot `BatchSpanProcessor`. Backpressure stays a separate backstop (§10).
- **Correctness proof = §9 empirical assertions.** By-construction self-consistency depends on plumbing completeness (N1/N2/N4 + lazy target-flag), so the gate `0/0` + zero-residual-introspection + exec/service checks across singleflight/lazy/adopted/imported captures are the real merge bar.

---

## 1. Problem statement and verified mechanism

### 1.1 The regression

On a module-load workload the branch emits **~8.5–11×** `main`'s engine telemetry. Three forensic captures agree: ordinary `dag.call` telemetry is unchanged (~820–853 spans), and 100% of the increase is the wcprof OTel emit — one `call_exec` + one `dagql.publishResult` per cache **miss** — for the introspection / schema-building class, which appears as `wcprof.op.kind=call_exec` with **no** matching `dag.call` span:

```
Query.sourceMap, ObjectTypeDef.__withFunction/.functions/.constructor/.fields,
Function.__withArg/.args/.withArg/.sourceModuleName/.returnType,
FunctionArg.typeDef, TypeDef.asScalar/asObject/asList/asInterface/asInput/asEnum,
FieldTypeDef.typeDef, ListTypeDef.elementTypeDef, EnumTypeDef.members, ...
```

That doubled ~33k-span stream overflows the OTel SDK's default **2048-slot non-blocking `BatchSpanProcessor`** queue → silent drops → dropped parent/target spans → the `OrphanedParents`/`UnresolvedWaitTargets` losses the §6.1 gate hard-fails on. The volume regression is the proximate cause of the capture loss.

### 1.2 Why it happens (re-derived)

- Normal telemetry deliberately suppresses this class in `core.AroundFunc` (`core/telemetry.go`, registered via `srv.Around` at `core/modtree.go:589`, `core/schema_build.go:107`): `IsSkipped(ctx)` → `NoopDone` (`:32-34`); `introspectionInfo` true → `WithSkip(ctx), NoopDone` (`:35-38`); `isMeta`/dedup → `NoopDone` (`:39-43`, `:60-64`).
- `WithSkip` is a context value only (`dagql/internal.go:34-43`); it does not strip the recording span.
- The wcprof OTel gate is recording-only: `OTelProfActive = trace.SpanFromContext(ctx).IsRecording()` (`dagql/otelprof_hooks.go:41-43`), no skip check. So `getOrInitCallInner` mints `call_exec` for every recording cache miss (`cache.go:3731-3734`); `publishResult` follows (`:4016-4018`); a wait is emitted per caller (`:3958`). The native recorder additionally mints an outer `OpKindCall` (`cache.go:3562`) and inner `execOp`/`pubOp`/waits, gated only on `wcprof.Enabled`.

### 1.3 The class is mixed; a pure-recipe predicate must be complete

`introspectionInfo` (`core/telemetry.go:354-452`) classifies the root list (`:363-387`) and the receiver-type builders/`__*` on `Function`/`TypeDef`/… (`:401-446`, **debug-gated** at `:402`). It does **not** classify the plain accessors (`Function.args/returnType/sourceModuleName`, `TypeDef.as*`, `FunctionArg.typeDef`, `ObjectTypeDef.functions/constructor/fields`, …) — those are suppressed on `main` only by *inherited* `IsSkipped` under the typedef-loading `hideCtx` (`core/modfunc.go:866-875`, `core/sdk.go:253-305`, `core/sdk/module_typedefs.go:99-120`). codex-existing re-counted the capture: v1's named-accessor list (`Function.args/sourceModuleName`, `TypeDef.as*`) leaves **3,394** `call_exec` spans (`Function.returnType` 952, `FunctionArg.typeDef` 845, `ObjectTypeDef.functions/constructor/fields` 114 each, …) — all reflection metadata. A complete cut must catch the whole reflection-accessor class, which a **receiver-TYPE** predicate does in one rule (§3.2).

---

## 2. Constraints

1. **Governing principle (settled):** the analysis is a rational function of *faithful* data — **zero** loader/replay change; no inference/fallback/heuristic. Fix the emit, never the model.
2. **Do not re-trip the gate on a good trace.** `OrphanedParents` (`loader.go:299-310`) and `UnresolvedWaitTargets` (`:356-405`, non-lock only at `:371`) hard-fail at `gate.go:132-140`. Post-skip → `0/0` across singleflight + lazy + adopted + imported captures.
3. **Keep "intentionally skipped" separable from "accidentally invalid."** `EmitOTelWait` emits a *targetless* wait on an invalid target so the gate fails loud on genuine loss (`otelprof_hooks.go:111-135`). Don't teach it to swallow invalid targets; don't overload `execSpanCtx.IsValid()` as "skipped."
4. **User work first-class.** Losing introspection granularity is accepted; coarsening/misattributing real work is not — hence the over-cut audit (N6).

---

## 3. The predicate (REVISED — separate, debug-independent, receiver-type)

### 3.1 Retraction of the v1 debug-gating rationale

v1 reused the **debug-gated** `introspectionInfo` and justified it (old §10) by claiming a debug-independent predicate would, in debug mode, "skip the `call_exec` while the normal span records, so a kept child parents to a non-op span → `OrphanedParents`." **That is mechanically false — verified, and settled by the loader owner (chunk1):**

- The loader builds `opIDBySpan` for **every** deduped span, **no kind filter**: `for i, s := range deduped { opIDBySpan[s.SpanID] = uint64(i+1) }` (`loader.go:251-254`).
- A normal `dag.call` span is a **present** op (`classifyKind`: `DagDigestAttr` present, no `wcprof.op.kind` → `"call"`, `loader.go:444-445`).
- `OrphanedParents++` fires **only** when the causal-parent *span is absent* (`cpSpan != "" && parentID == 0`, `loader.go:299-310`; `causalParentSpanID = wcprof.parent ?? parentId`, `:468-475`).

So there is no "non-op span." In debug, a skipped builder's normal recording `dag.call` span is present → a kept child parents to a present op → **no orphan**. The justification collapses. Worse, debug-gating is *actively harmful*: in debug the receiver-type cases are off (`telemetry.go:402`) → the ~33k amplifier returns → BSP overflow → the orphan/unresolved loss returns **in the exact mode you capture to debug a problem**. The v1 "debug bonus" is illusory (you can re-enable the spans, but the capture that would profile them is the one that drops them). **Decision: the profiling predicate is debug-INDEPENDENT.**

### 3.2 The predicate

A **new** classifier in `core`, separate from `introspectionInfo` (which is the UI/normal-telemetry decision and stays untouched):

```
profileSkip(ctx, call) :=
    call.Receiver == nil        ? call.Field ∈ introspectionRootSet
  : immediateReceiverTypeName(call) ∈ reflectionTypeSet
      || ( immediateReceiverTypeName(call) == "Query" && call.Field ∈ introspectionRootSet )
```

- `reflectionTypeSet = { Function, FunctionArg, TypeDef, ObjectTypeDef, InterfaceTypeDef, InputTypeDef, FieldTypeDef, ListTypeDef, ScalarTypeDef, EnumTypeDef, EnumMemberTypeDef }` — the GraphQL type-system reflection objects. Verified these are real schema object types in `core/schema/module.go` (`*core.Function` ~471, `*core.FunctionArg` 478/487, `*core.TypeDef` 581, `*core.ObjectTypeDef` 596, `*core.InterfaceTypeDef` 610, `*core.InputTypeDef` 618, `*core.FieldTypeDef` 623, `*core.ListTypeDef` 629, `*core.ScalarTypeDef` 634, `*core.EnumTypeDef` 637, `*core.EnumMemberTypeDef` 648). **Every field on these types is type-system metadata** (accessors `as*`/`args`/`typeDef`/`functions`/`fields`/`returnType`/…, builders `with*`, internal `__*`) — none does container/exec/module-load work (audited; N6 keeps it a §9 check).
- `introspectionRootSet` = the existing root-field name set from `introspectionInfo` (`core/telemetry.go:363-387`: `__schema`, `__schemaJSONFile`, `__schemaVersion`, `currentTypeDefs`, `currentModule`, `currentFunctionCall`, `function`, `typeDef`, `sourceMap`, `__loadInputTypeDef`, `__function`, `__functionArg`, `__functionArgExact`, `__fieldTypeDef`, `__fieldTypeDefExact`, `__enumMemberTypeDef`, `__enumValueTypeDef`, `__listTypeDef`, `__objectTypeDef`, `__interfaceTypeDef`, `__inputTypeDef`, `__scalarTypeDef`, `__enumTypeDef`). **Extract this set to a shared constant** consumed by both `introspectionInfo` (behavior unchanged) and `profileSkip` (DRY, no divergence on the roots).

**Why it dominates v1's extend-`introspectionInfo`-3.2a:**
- **Complete (point C):** the receiver-type rule catches the full reflection-accessor class — including the 3,394 residual codex-existing measured — with **no per-workload field-chasing**. Module *loading* (the slow part) is `Query.moduleSource`/`ModuleSource.asModule`, whose receivers are **not** reflection types, so it stays profiled (kept). ✓
- **No UI coupling (point B):** `introspectionInfo`/normal telemetry untouched, so a directly-called `TypeDef.asObject` still emits its `dag.call` span. The profiler ADDS spans; it must not SUBTRACT UI spans (design-author's call). The "single source of truth / oracle" argument for one classifier cuts the other way — UI *wants* debug-gating (show introspection in debug), the profiler *wants* debug-independence; two truths → two predicates (share the root constant for DRY).
- **Pure recipe function (point A):** receiver type and field are both in the call digest (`callKey`, `cache.go:3669`), and the predicate is debug-independent, so the §4 acid test holds with **no** non-recipe input — removing the v1 §10 debug-baggage caveat entirely. Determinism is unconditional, not "self-consistent either way."
- **Cheaper (point D):** one **immediate** `ReceiverCall` hop (to read the receiver's type name), not `introspectionInfo`'s walk up the whole chain to find a root. So it broadens the cut *and* shrinks per-call cost (§5.7).

### 3.3 Exclusions (unchanged conclusion; correct reasons)

- **`ShouldEmitTelemetry` dedup — EXCLUDE (correctness).** Not a recipe function: it depends on per-session seen-state mutated per call (`dagql/telemetry.go:48-64`, used at `core/telemetry.go:60-64`). Folding it in would break waiter↔target agreement and drop *real repeated cache-miss executions*. (All 8 reviews agree.)
- **`field.Spec.NoTelemetry` — EXCLUDE (pragmatic).** Not the volume class; all current `NoTelemetry` fields are `DoNotCache` proxies (`core/object.go:1161-1169`, `:1214-1219`, `:1259-1265`) that return before the emit site (`cache.go:3601-3650`), so including it is a no-op.
- **`isMeta` (`node`/`id`/`sync`) — EXCLUDE (pragmatic + goal).** Not in the volume list; `sync` forces evaluation (user-work-adjacent) and should stay profiled.
- **Note (retained from v1):** the chunk4 "`isMeta`/`NoTelemetry` orphan their children" rationale is mechanically wrong — re-homing (no span pushed → children record the parent's present span; §4.1) prevents it. The exclude-conclusion stands on the pragmatic grounds above; chunk4 conceded this in its v2 review.

### 3.4 Where computed and plumbed (unchanged from v1 — confirmed)

`profileSkip` lives in `core` (Dagger type names). The cache is a lower layer, so stamp the decision on `dagql.CallRequest.SkipProfile` (add field + `Clone()` copy at `dagql/call_request.go:8-19,29-36`). The same `req` pointer flows from `ObjectResult.preselect` (`objects.go:583-604`) to both `s.telemetry(ctx, req)` (= `AroundFunc`, `:656`) and `cache.GetOrInitCall(…, req, …)` (`:678`), so `AroundFunc` mutates it:

```go
// core/telemetry.go AroundFunc — stamp the static, debug-independent skip BEFORE the
// IsSkipped early return, so inherited-skip descendants (under a hideCtx) are classified
// by their OWN recipe: reflection-receiver work is skipped; real shared work (clone /
// dep-load, whose receiver is NOT a reflection type) stays profiled.
if req == nil || req.ResultCall == nil { return ctx, dagql.NoopDone }
req.SkipProfile = profileSkip(ctx, req.ResultCall)   // NEW predicate; introspectionInfo untouched
if dagql.IsSkipped(ctx) { return ctx, dagql.NoopDone }
... // introspectionInfo / isMeta / dedup / normal span — all unchanged
```

**Lock-safety (confirmed by all reviewers):** `profileSkip`'s `ReceiverCall` → `resultCallByResultID` takes `egraphMu.RLock` (`result_call_frame.go:575-603`, `:1377-1387`). `AroundFunc` runs **outside** any cache lock (`objects.go:656`); a cache-side predicate at `cache.go:3732`/`:3017` would nest `egraphMu` under `callsMu`/`lazyMu` — a real lock-order hazard. So `CallRequest.SkipProfile` is the right seam (the lock argument survives the switch to a receiver-type predicate). `SkipProfile` is request-only policy (like `DoNotCache`): copy it in `Clone()`, and it must **not** enter `callKey`/`callDigest`/`concurrencyKey`.

(Alternative — a registered cache predicate — is rejected for the lock hazard and scattered registration; noted for completeness.)

---

## 4. The acid test — self-consistent with ZERO loader/replay change

### 4.1 Parent edges — SAFE by re-homing (predicate-independent)

A skipped call mints no span (`beginOTelCallExec` gated at `cache.go:3732`; `callCtx` not reassigned at `:3733`), so its resolver runs under the nearest **recording ancestor** (`oc.sharedWorkCtx` derives from the unreassigned `callCtx`, `:3735`,`:3765`); every sub-call records a **present** `parentId`. A chain of skipped builders pushes nothing → kept descendants re-home to the nearest kept ancestor; the trace root is always recording. **No `OrphanedParents`** — given `call_exec` and `publishResult` are skipped together (they are; §5.2). Verified against the loader (every span an op; orphan only on an *absent* parent span). Holds for any predicate.

### 4.2 Singleflight wait edges — IMPOSSIBLE to cross the boundary

Singleflight waits target `oc.execSpanCtx` (`cache.go:3958`); both executor (`joined=false`) and joiner (`joined=true`) route `c.wait` (`:3785`,`:3703`). The waiter and target share the cache key (`ongoingCalls` keyed `{callKey, concurrencyKey}`, `:3674-3677`; `callKey` = recipe digest, `:3669`) — to join you must call `K`. Since `profileSkip` is a pure recipe function, the waiter's `SkipProfile` **equals** the target's. Gating waits on the **target's stored flag** (`!oc.profSkip`) then means: target skipped ⇒ waiter skipped, no span and no wait (consistent); target kept ⇒ span valid, wait resolves. A non-skipped waiter on a skipped target **cannot occur**. This is exactly why the cut must be **static (per recipe)**, not dynamic (per-caller `IsSkipped`): a dynamic cut races on who claims a shared key (`clone`/dep-load under `hideCtx` is `IsSkipped` but real work) → dangling target → gate refusal of a good trace. (`concurrencyKey = SessionID`, `objects.go:603`, so singleflight sharing is within-session — no cross-session split here.)

### 4.2b Lazy wait edges — closed by TARGET-FLAG gating (load-bearing, NOT "robustness") [N3]

**The §4.2 "waiter shares the recipe" argument does NOT cover the lazy path** (chunk3, lazy owner — correct, verified). The lazy op represents the *producer* `resultCall = shared.loadResultCall()` (`cache.go:2980`); the joiner waiting at `cache.go:2964`/`:2973` is a **forcer/consumer** of the pending value — a *different* recipe. So waiter≠target recipe, and shared-recipe agreement does not apply.

What closes the lazy path is gating every lazy wait on the **target's stored flag** `shared.profSkip` (keyed on the producer `resultCall`, §5.1) — this is **load-bearing**, not the "robustness against debug" framing v1 used:
- `shared.profSkip` (producer is reflection/introspection) ⇒ `beginOTelLazyOp` gated off (`:3017`) ⇒ `lazyEvalSpanCtx` stays the reset-invalid zero (`:2996`) ⇒ joiner/leader waits gated off ⇒ no op, no edge. A *non-skipped forcer* of this value has its wait gated off by the **target's** flag — no dangle. ✓
- `!shared.profSkip` (producer is real work) ⇒ lazy op present ⇒ waits resolve; an *introspection forcer* still emits its wait, which re-homes onto the forcer's nearest recording ancestor (honest). ✓

**State this explicitly in the code/comments so no future reader "simplifies" the lazy gate to the waiter's own bit and reopens a cross-recipe dangle.**

### 4.3 `wcprof.parent` lazy override — does not cross the boundary (confirmed)

The processor stamps `wcprof.parent = lazyOpSpanID` only on the producer's **direct** re-pointed children (`s.Parent().SpanID() == producerSpanID`, `otelprof_lazy.go:88-101`) — it points at the lazy op, never the forcer. Key the lazy skip on `resultCall` (`shared.profSkip`): producer skipped ⇒ `beginOTelLazyOp` not called ⇒ `withLazyParentOverride` never set (`otelprof_lazy.go:166`) ⇒ no override exists; producer kept ⇒ override targets the present lazy op, whose own `parentId` re-homes over any introspection forcer. So the point-8 worry ("introspection forces a non-suppressed lazy op → `wcprof.parent` into the skipped set") cannot arise. No `wcprof.parent`→`parentId` fallback needed (confirm on the §9 lazy capture).

### 4.4 Accepted coarsenings (named decisions)

- Introspection self-time folds into the nearest kept ancestor's self-time — honest (the ancestor synchronously spent it).
- **[N3] User-wait loss:** a *non-introspection* forcer that blocks on an *introspection-produced* pending value loses that wait edge (gated by the target's flag), folding the blocked time into the forcer's self-time. Rare (introspection metadata is usually computed eagerly, seldom pending) and cheap; self-consistent. Named here as a decision, not an accident.

---

## 5. Storage and gating

### 5.1 Flags + N2 provenance rule

- `ongoingCall.profSkip bool` (next to `execSpanCtx`, `cache.go:1777-1783`), set at claim from `req.SkipProfile`.
- `sharedResult.profSkip bool` (next to `resultCall`, `cache.go:1452`). **[N2 — must follow the STORED producer `resultCall`, not blindly `req`.]** `initCompletedResult` (`cache.go:4103`) establishes `oc.res` three ways:
  - **adopt canonical** (`:4132`, `oc.res = canonicalEquivalentSharedResultLocked(...)`): `oc.res` *is* an existing shared result — **leave its `profSkip` intact** (set when that result was created).
  - **copy existing frame** (`:4149-4153`, `oc.res.storeResultCall(frame.clone())` from `oc.val.cacheSharedResult()`): set `oc.res.profSkip = shared.profSkip` (copy from the source shared result).
  - **store request frame** (`:4159-4162`, `oc.res.storeResultCall(req.ResultCall.clone())`): set `oc.res.profSkip = req.SkipProfile` — the **only** correct use of `req`.
  - **Invariant:** *`profSkip` travels with `resultCall`* — wherever a `sharedResult`'s `resultCall` is set or `clone()`d, set/copy `profSkip` alongside it. Audit the other `sharedResult` construction/copy sites (`cache.go:1799`, `:2431`, `:2531`; DoNotCache `:3625` cannot be lazy, `:3621`) and copy from the source when copying a frame.
  - **Imported/persisted results** (`cache_persistence_import.go:164`, `:587-594`; `cache_persistence_worker.go:421`; lazy registration `cache.go:2779-2792`,`:2000`,`:4449`): `profSkip` is not persisted, so on import it defaults **false** → an imported *introspection lazy* result would be profiled (volume), **never dangle** (self-consistent — the op exists, waits resolve). This is a bounded **volume edge, not a correctness edge**. Resolve by recomputing `profSkip` from the stored `resultCall` at import/registration *in a lock-safe place off the hot path*; if that proves awkward, accept default-false and let §9 confirm imported-lazy-introspection volume is negligible (introspection metadata is eagerly computed, rarely lazy — but prove it, don't assume).

Both flags are **distinct** from `execSpanCtx`/`lazyEvalSpanCtx` validity (§5.5).

### 5.2 Singleflight gating

| site | now | change |
|---|---|---|
| **[N1]** outer native `OpKindCall` `cache.go:3559` | `if !wcprof.Enabled(ctx) \|\| req==nil \|\| req.ResultCall==nil` → inner(nil) | add `\|\| req.SkipProfile` (skip the outer native call op too — nil-op methods are safe, `record.go:124-130`) |
| native `execOp` `cache.go:3717` | `if wcprof.Enabled(ctx)` | `&& !req.SkipProfile` |
| OTel `call_exec` `cache.go:3732` | `if OTelProfActive(callCtx)` | `&& !req.SkipProfile` |
| store flag `cache.go:3744-3759` | — | `oc.profSkip = req.SkipProfile` (under `callsMu`, before publish at `:3762` — Invariant T) |
| native `pubOp` `cache.go:4004` | `if oc.profOpID != 0` | follows for free (`(*wcprof.Op)(nil).ID()==0`, `record.go:124-130`) |
| OTel `publishResult` `cache.go:4016` | `if oc.execSpanCtx.IsValid()` | follows for free (skipped ⇒ never minted ⇒ invalid) |
| native `BeginWait` `cache.go:3941` | `if wcprof.Enabled(ctx)` | `&& !oc.profSkip` |
| OTel `EmitOTelWait` `cache.go:3958` | unconditional | `if !oc.profSkip { … }` |

**N1 is the oracle-symmetry fix:** without gating the outer `OpKindCall` (`cache.go:3562`), a skipped introspection miss still gets a native call op while OTel has none → "native and OTel drop the same class" is false. Verified: `getOrInitCall` (`:3552-3579`) wraps the inner call with `wcprof.BeginOp(OpKindCall)` gated at `:3559`; the early-return already passes a nil `profOp` to the inner, and the inner is nil-safe, so adding `|| req.SkipProfile` is clean.

### 5.3 Lazy gating

| site | now | change |
|---|---|---|
| native `lazyOp` `cache.go:2998` | `if wcprof.Enabled(evalCtx)` | `&& !shared.profSkip` |
| OTel `lazy` span `cache.go:3017` | `if OTelProfActive(evalCtx)` | `&& !shared.profSkip` |
| native leader `BeginWait` `cache.go:3094` | `if wcprof.Enabled(...)` | `&& !shared.profSkip` |
| OTel leader wait `cache.go:3098-3103` | `if lazySpan != nil` | follows for free |
| native joiner `BeginWait` `cache.go:2964` | unconditional | `if !shared.profSkip { … }` |
| OTel joiner wait `cache.go:2973` | unconditional | `if !shared.profSkip { … }` |

All lazy waits gate on the **target's** `shared.profSkip` (load-bearing, §4.2b). The joiner still passes the (possibly invalid) `shared.lazyEvalSpanCtx` so the mixed-recording detector survives for *non-skipped* lazy work (§5.5).

### 5.4 Native ↔ OTel symmetry (one decision; "debug bonus" reason dropped)

Gate native on the same flag (incl. the N1 outer call) so the cross-source oracle stays comparable in every mode. The v1 "debug bonus" reason for symmetry collapses with a debug-independent predicate (both sources simply skip the class in all modes — comparable without the volume relapse); the conclusion and the other reasons hold. This modifies validated PR #13393 native behavior (introspection ops vanish from native dumps too) — correct for the oracle and native is dev-only, but flag it for the same "shared native code" scrutiny the chunk4 replay changes got.

### 5.5 "Skipped" distinct from "invalid target" (unchanged — confirmed)

Gate waits on the `profSkip` bool, never on target validity, so a *non-skipped* target with an invalid span (genuine mixed/untraced recording) still emits a targetless wait → `UnresolvedWaitTargets` → gate fails loud (`otelprof_hooks.go:111-135`). Do not change `EmitOTelWait`.

### 5.6 [N7] Surviving `publishResult` parentage — orthogonal, but needed for a fully-clean gate

chunk3 reports the kept (non-skipped) `publishResult` spans are emitted **parentless** (their separate `wcprof-otel-publishresult-chunk3-impl.md` finding: parented by context-propagation through the already-ended `call_exec`, `otelprof_hooks.go:76-84` / `cache.go:4017`). The skip fix reduces their **count** but does not change the survivors' parentage. Parentless internal-kind roots have an *empty* `cpSpan`, so they do **not** trip `OrphanedParents` (§9.2 can pass) — but they *would* trip chunk3's proposed internal-kind-root faithfulness signal. **Do not declare "gate clean" on the skip fix alone:** a fully-clean gate under that signal also needs chunk3's separate explicit-`execSpanCtx`-parenting fix. Independent fixes; cross-referenced here so the boundary is explicit.

### 5.7 [D] Classification cost — measure in v1

Stamping moves `profileSkip` ahead of the `IsSkipped` early return (necessary — inherited-skip descendants must be classified by their own recipe), so it runs for the ~33k recording-but-skipped descendants on the hot module-load path. The receiver-type predicate is the real mitigation (one immediate `ReceiverCall`/`egraphMu.RLock`, not `introspectionInfo`'s chain walk). A guard to skip the stamp when no profiler will consume the bit must cover **both** sinks — `trace.SpanFromContext(ctx).IsRecording() || wcprof.Enabled(ctx)` — but it does **not** help production (which records), so it is not the mitigation. **Measure the `egraphMu.RLock` cost/contention on the ~33k path in v1** (read lock, contends with indexing writers, of which module load has many). If hot, **memoize by `(receiverTypeName, field)`** — the predicate's only inputs — in a small concurrent map. Put the number in §9, do not defer.

---

## 6. Every emit/plumbing site touched (file:line)

Predicate / plumbing:
- `core/telemetry.go` — new `profileSkip(ctx, *ResultCall) bool` (receiver-type + shared root set); extract `introspectionRootSet` constant from `:363-387`; stamp `req.SkipProfile = profileSkip(...)` in `AroundFunc` before the `IsSkipped` return (`:29-43`). **`introspectionInfo` itself unchanged.**
- `dagql/call_request.go:8-19,29-36` — add `SkipProfile bool` + copy in `Clone()`; never in `callKey`/digest/`concurrencyKey`.

Storage:
- `dagql/cache.go:1758-1786` — `ongoingCall.profSkip`.
- `dagql/cache.go:1429-1452` — `sharedResult.profSkip`; set per the N2 provenance rule in `initCompletedResult` (`:4132`/`:4151`/`:4160`) + audited construction/copy sites + import/registration.

Gating: `cache.go:3559` (**N1 outer native call**), `:3717` (native execOp), `:3732` (OTel call_exec), `:3744-3759` (store `oc.profSkip`), `:3941` (native wait), `:3958` (OTel wait); lazy `:2964`,`:2973`,`:2998`,`:3017`,`:3094`. `:4004`/`:4016`/`:3098-3103` follow for free.

Not touched (verify, do not gate — §9 hard check, E): exec-split (`engine/engineutil/otelprof.go:46-117`, `executor.go:121-142`, `executor_spec.go:1405-1430`) and service start (`core/services.go:995-1012`,`:1021-1035`) — they fire inside container-exec/service resolvers introspection never reaches.

---

## 7. Implementation sequencing

1. **Predicate + plumbing, no behavior change.** New `profileSkip` + `introspectionRootSet` extraction (leave `introspectionInfo` alone); `CallRequest.SkipProfile` (+`Clone`); stamp in `AroundFunc`. Unit-test the classifier (§8). Nothing reads the bit yet.
2. **Singleflight gating + storage**, incl. **N1 outer call**. Emit-path SDK tests.
3. **Lazy gating + storage**, incl. **N2 provenance** (adopt/copy/req + import). Lazy emit-path tests.
4. **Native symmetry** sweep (all native gates incl. N1). Oracle test.
5. **Validation (§9)** — measure before declaring done.

Each step compiles and is independently testable.

## 8. Tests (unit / emit-path; `tracetest.SpanRecorder` harness)

1. **Classifier:** `profileSkip` true for the reflection-receiver accessors **and** the root set — explicitly incl. the residual class (`Function.returnType`, `FunctionArg.typeDef`, `ObjectTypeDef.fields/functions/constructor`, `FieldTypeDef.typeDef`, `ListTypeDef.elementTypeDef`, `EnumTypeDef.members`); false for user work (`Container.withExec`, `Query.moduleSource`, `ModuleSource.asModule`). **Debug-independent:** identical result with debug baggage set. `introspectionInfo`/normal `dag.call` emission **unchanged** for a directly-called `TypeDef.asObject` (no UI regression).
2. **Literal skip:** a reflection-receiver/`__schema` call emits no `call_exec`/`publishResult`.
3. **Re-homing:** a skipped call whose resolver makes a kept sub-call → sub-call `parentId` is a present span; gate `0` `OrphanedParents`.
4. **[load-bearing] Skipped claimer + non-skipped joiner on one `ongoingCall`:** with the static cut the joiner of a reflection key is itself skipped ⇒ no wait, `0` `UnresolvedWaitTargets`. Keep the guard asserting the *dynamic* `IsSkipped` cut would dangle here (documents why static is required).
5. **[load-bearing] Distinct-from-invalid:** a non-skipped target with an artificially invalid span still emits a gate-observable targetless wait (`UnresolvedWaitTargets > 0`).
6. **Kept singleflight unchanged:** a non-reflection shared call still emits `call_exec`+`publishResult`+resolved waits (executor + joiner), and the outer native `OpKindCall` still appears.
7. **[N1] Outer native call skipped:** a skipped miss emits no native `OpKindCall` (oracle symmetry).
8. **[N2] `sharedResult.profSkip` provenance:** (a) adopted canonical result keeps its own flag; (b) copied-frame result copies the source flag; (c) request-frame result uses `req.SkipProfile`; (d) imported lazy result default-false profiles (volume) but never dangles.
9. **Lazy variants:** skipped producer → no lazy op/waits, `0/0`; kept producer → lazy op + waits resolve, re-pointed work carries `wcprof.parent =` present lazy op; kept producer forced by a skipped introspection forcer → re-home to present ancestor, `0` orphans; **non-skipped forcer on a skipped-producer lazy value → wait dropped (the §4.4 named loss), no dangle.**
10. **Native symmetry:** introspection class absent from native `OpKindCall`/`execOp`/`lazyOp`/waits; oracle per-class self-time aligned.

## 9. Validation / measurement — the correctness centerpiece (MUST pass before landing)

The by-construction self-consistency now rests on **plumbing completeness** (N1 outer call, N2 provenance, N4 stamp coverage, lazy target-flag gating). So these empirical checks are the real proof, not an afterthought. Capture via `telemetry-capture` (`hack/otlpdump`); dev engine per `engine-debugging`. Workloads: module-load (the regression), an exec-heavy one, and a lazy/service one.

**Completeness assertions (hard merge gate):**
1. **Gate `0/0`** (`OrphanedParents`, `UnresolvedWaitTargets`; also `Cycles==0`) on a fresh capture **across all paths**: singleflight, lazy, **adopted/canonical** results, and **imported/persisted** results (`gate.go:132-140`).
2. **[N4] Zero residual introspection:** no `wcprof.op.kind=call_exec` span carries a reflection-receiver/root name with `dag.call=0` — catches both an incomplete predicate *and* any recording path that bypassed `AroundFunc` (affirmatively verify `srv.Around` coverage of every recording engine path into `getOrInitCall`; `cmd/introspect` is non-recording, fine).
3. **[E] Exec/service hard check:** zero `wcprof.op.kind ∈ {exec, exec_phase, service_start}` originates from the skipped class, and user `processRun` self-time is unchanged vs pre-fix.
4. **[N6] Over-cut spot check:** confirm no reflection-type field coarsened *real* work — user `processRun`/exec self-time and the kept residual `call_exec` names are all genuine work (one-time schema audit of the `dagql.Fields[*core.<ReflectionType>]` blocks in `core/schema/module.go` that none triggers container/exec/module-load, plus this capture check).
5. **Lazy `wcprof.parent` clean:** the lazy-heavy capture shows `0` orphans and no `wcprof.parent` into a skipped node (§4.3).

**Regression / oracle measurements:**
6. **[N8] Volume:** the schema amplifier is gone; residual `call_exec` names are real kept work. Target is **not** "equal to main." forensics-codex estimates ~5.3k post-fix (3.3k ordinary + ~849 visible `call_exec` + ~849 `publishResult` + ~336 lazy/exec) vs main's ~3.4k; the ~1.9k gap is the legitimate per-real-miss `call_exec`+`publishResult` pair — the always-on second-source cost, a separate scaling question (out of scope here, but note it).
7. **BSP `DroppedSpans` → 0:** instrument the `BatchSpanProcessor` (`engine/server/session.go`) drop counter; expect `>0` pre-fix and `0`/near-zero post-fix on module-load — the decisive proof that removing the amplifier closes the orphan loss. Residual drops at baseline volume = the separate BSP-backpressure issue.
8. **Oracle aligned:** dev engine with both sources → per-class self-time tables match (both drop the same class); spot-check kept user-work self-time vs pre-fix native ground truth.
9. **[D] Classification cost:** before/after profile of `AroundFunc`/`profileSkip` (the `egraphMu.RLock` per inherited-skip descendant) on module-load; if hot, land the `(receiverTypeName, field)` memoization.
10. **Determinism:** several module-load runs → `UnresolvedWaitTargets == 0` every run (with the debug-independent predicate there is no debug-uniformity asterisk).

---

## 10. Risks, open questions, disagreements

- **[N6] Over-cut (receiver-type's one assumption):** that no reflection-type field does real container/exec/module work. Verified by sampling (`as*`/`functions`/`fields`/`typeDef`/`with*` are metadata) and the loaders live on non-reflection receivers (`ModuleSource.asModule`, `Query.moduleSource`) → stay profiled. An over-cut is a *goal violation* (silent coarsening of real work), not a dangle, so it is a §9.4 hard check + a one-time schema audit. Keep the predicate profiler-only so a reflection-type *name collision* (a user object literally named `Function`/`TypeDef`) loses profiling granularity, not UI telemetry — pre-existing risk in `introspectionInfo`, contained here.
- **[N2] Import gap:** imported persisted lazy results default `profSkip=false` → introspection lazy work (if any) is profiled (bounded volume edge), never dangles. Recompute from the stored `resultCall` at import if lock-safe; else accept + measure (§9.1/§9.6).
- **[N4] Stamp coverage:** the `CallRequest` seam relies on `AroundFunc` running on every recording path; verified registration at `core/modtree.go:589`/`core/schema_build.go:107`, but affirmatively confirm no other recording-engine path reaches `getOrInitCall` un-stamped (§9.2 is the backstop). The rejected cache-predicate wouldn't have this gap, but it has the lock hazard — accepted trade.
- **[N7] `publishResult` survivors are parentless** — orthogonal; a fully-clean gate under the internal-kind-root signal needs chunk3's separate fix (§5.6).
- **Telemetry-off + native-on corner (chunk2):** if `s.telemetry==nil` but `wcprof.Enabled` (a bare native-only path), `SkipProfile` stays false → native re-profiles introspection. Dev-only, self-consistent; flag, don't block.
- **Debug-baggage caveat — REMOVED.** The debug-independent predicate is a pure recipe function; the v1 §10 "one non-recipe input" caveat no longer applies, and the static cut's determinism is unconditional.
- **Not the BSP fix (boundary):** removes the amplifier; a legitimate heavy user-work burst can still overflow the 2048-slot queue. Backpressure stays a separate backstop (§9.7).
- **No flaw found in the static cut after re-stress-testing** parent edges, singleflight + lazy waits, the `wcprof.parent` override, native symmetry (incl. the outer call), and the result-adoption provenance. My v1 errors (the debug-orphan rationale; "stamp from `req` in `initCompletedResult`"; the named-accessor list; the singleflight-only acid test) are corrected above.

### Where I (still) push back / nuance, with evidence

- **None of the council's must-fixes are wrong** — I verified N1 (`cache.go:3559-3565`), N2 (`cache.go:4132/4151/4160`), the loader no-kind-filter (`loader.go:251-254`,`:299-310`), and the reflection types (`core/schema/module.go`), and accept all. My v1 debug-gating was a genuine error.
- **One refinement to the lead's framing (D):** the proposed `!IsRecording` short-circuit is not just "weak" — it is **useless in production** (production records), as is `!IsRecording && !wcprof.Enabled`. The real mitigation is the cheaper receiver-type predicate + `(receiverTypeName, field)` memoization; I've made that the plan (§5.7) rather than carrying a guard that implies a saving it can't deliver.
- **One nuance on N7:** I cross-reference chunk3's parentless-`publishResult` finding but do **not** adopt a fix for it here — it is genuinely orthogonal (it concerns *kept* survivors' parentage, which this fix never touches), and folding it in would couple two independent changes. Flagged so "gate clean" isn't over-claimed; owned by chunk3.
- **One scope assertion:** I keep the predicate a **separate** `core` function rather than extending `introspectionInfo`, per B — but I want it on record that this *intentionally* lets a directly-called reflection accessor keep emitting its normal `dag.call` UI span while the profiler skips it. That divergence (profiler skips more than the UI suppresses) is correct and desired; if a future reader expects profiler-skip ≡ UI-suppress, that expectation is wrong by design.

## 11. Council questions

- **(a) Correct / free win?** Correct, free for the goal, with the static cut — singleflight closed by shared-recipe + target-flag, **lazy closed by target-flag (load-bearing)**, parents by re-homing, native by the same flag incl. the outer call (N1).
- **(b) Zero inference?** Yes — loader/replay untouched; the engine emits a smaller, self-consistent graph proven by §9, not just by construction.
- **(c) Goal preserved, user-work first-class?** Yes; only the reflection/introspection class goes absent (folds honestly into the kept ancestor), modulo the named §4.4 user-wait loss and the N6 over-cut audit.
- **(d) Simple or massive?** Moderate/localized — and the separate receiver-type predicate is *simpler* than extending `introspectionInfo` (no UI coupling, no debug caveat, no field-chasing, cheaper). The N2 provenance and N1 outer-call are small, contained additions.

**Verdict: implement as specified — separate, debug-independent, receiver-type predicate; `CallRequest.SkipProfile` stamp; gate call_exec + publishResult + every wait + the outer native call across singleflight + lazy + both sources; `profSkip` distinct from target validity; `sharedResult.profSkip` follows the stored producer recipe. Land only after the §9 completeness assertions and measurements pass.**
