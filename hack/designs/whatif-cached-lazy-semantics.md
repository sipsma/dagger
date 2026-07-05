# what-if-cached × lazy evaluation — what "cached" means, made concrete

Deep analysis requested by Erik (2026-07-05) after his A1 review: withExec is
not special — a large fraction of dagql operations split into cheap
synchronous plan-building plus deferred lazy production — so a rule keyed to
exec-kind ops is suspicious, and the deeper question is what "X was cached"
even MEANS across that split. This document answers both from the engine and
cache code as it exists at this branch. Delivered as analysis only (no code
changed with it); the emits it proposed have since landed under Erik's
2026-07-05 ruling — the landed-resolution/STATUS callouts below mark what
shipped. Doctrine applies: every gap identified here resolves to an EMIT
proposal, never analyzer inference.

**TL;DR.**
(1) Erik is right that withExec is not special: ~68 concrete lazy types span
Container/Directory/File, and `Container.from` is the both-halves-expensive
case (registry config fetch sync, layer pull deferred). But laziness is not
universal either: git/http/host/module-load/service production is
resolver-synchronous — the existing call-subtree elision is already correct
for those.
(2) "Cached" decomposes cleanly: the dagql cache caches the CALL (the outer
recipe); production completeness is a separate, orthogonal axis
(`lazyEvalComplete`) on the same shared result. Four of the six combinations
in the cartesian product are real engine states with named code paths; "inner
cached, outer not" is not a dagql state at all (it lives below dagql, in
buildkit content stores and the volatile-exec substitution).
(3) The general rule is **deferred-production attribution**: any op whose
emitted attribution says "I am (part of) the deferred production of recipe
digest D" joins D's elision regions, regardless of kind. The lazy op at
`dagql/cache.go:3045` is the single choke point for ALL ~68 lazy types, it
has the producer's authoritative `ResultCall` in hand at that exact line, and
the recipe digest is memoized on the frame — the emit is one line per source
and near-free.
(4) A1's machinery (root-inclusive regions, ancestor-waiver, keep fixpoint)
IS the general rule's machinery; only its region SOURCING (exec-kind idents)
is the special case, an artifact of exec.run being today's only
deferred-production op that carries a producer digest. Verdict: **subsume** —
keep the machinery and the exec sourcing as a fallback for existing traces,
add the lazy-op emit, source regions kind-agnostically from it.
(5) The analysis also surfaces one genuinely new model hole neither v1 nor A1
covers — post-completion forcers of a lazy result leave NO recorded edge, so
eliding a region can silently take NESTED foreign-digest production a later
consumer genuinely needed (savings overstated). The missing fact is an
evaluation-time event, so the remedy is a small "forced (already complete)"
emit at the Evaluate fast path; structural cache-input edges CANNOT stand in
for it (review-verified: every ordinary consumer references the digest
structurally, so input-edge demand would destroy real savings). The emit and
its consumption landed with Erik's 2026-07-05 ruling (row V31).

---

## 1. The empirical landscape of deferred production

### 1.1 Where laziness lives

The dagql contract is generic (`HasLazyEvaluation` / `LazyEvalFunc` /
`Cache.Evaluate`, `internal-docs/lazy_evaluation.md`), but its users are
enumerable. Every concrete `core.Lazy` implementation in the tree (grep
`^type [A-Z]\w*Lazy struct` in `core/*.go` — the exported runtime types;
the lowercase `persisted*Lazy` twins are counted separately):

- **Container**: ~49 types — `ContainerExecLazy` (`core/container_exec.go:100`),
  `ContainerFromImageRefLazy` (`core/container_image.go:33`),
  `ContainerVolatileExecCacheHitLazy` (`core/container_exec.go:116`),
  `ContainerImportLazy`, `ContainerRootFSLazy`/`Directory`/`File` selector
  views, and the long tail of `ContainerWith*/Without*` mutation lazies.
- **Directory**: 13 types (`DirectoryWithDirectoryLazy`, `DirectoryDiffLazy`,
  `DirectorySubdirectoryLazy`, …).
- **File**: 5 types (`FileSubfileLazy`, `FileWithReplacedLazy`, …).
- Each has a `persisted*Lazy` twin — the lazy RECIPE is a first-class
  persisted form (see §2.3).

There are **no** lazy types for git, http, host, module loading, secrets, or
services. Those families produce synchronously inside their resolvers:
`Host.directory` runs the filesync snapshot inline and pre-seeds the
accessors (`core/schema/host.go:313-326`); module loading's dominant cost
(the 20.37s `codegen generate-typedefs` exec in the V23 calibration) sits
INSIDE `ModuleSource.asModule`'s call subtree — proven empirically by the
ranking sweep (`whatif-cached-calibration.md`), which saves 22.17s/22.55s by
eliding that one call region. For these families the v1 call-subtree
semantics is already the right answer.

### 1.2 The recorded shapes, per family

wcprof op emit sites are a closed set (grep `wcprof.BeginOp|wcprof.RecordOp`
outside `engine/wcprof`): call / call_exec / publishResult
(`dagql/cache.go:3614, :3791, :4094`), lazy (`dagql/cache.go:3045`), exec.run
+ phases (`engine/engineutil/executor.go:137, :221`,
`engine/engineutil/executor_spec.go:1430-1433`), withExec wrapper phases
(`core/container_exec.go:1742, :2195`), service.start
(`core/services.go:1032`), session phases (`engine/server/session.go`).
`OpKindIO` is defined but has zero emit sites — external I/O time lands in
the enclosing op's self-time everywhere.

| family (examples) | sync half (inside `call`→`call_exec` subtree) | deferred half (under a LAZY op parented to the first FORCER) | where the wall-clock lives | attribution recorded today |
|---|---|---|---|---|
| `Container.withExec` | build `ContainerExecState` (ExecMD, opts) — cheap | `ContainerExecLazy.Evaluate` → `withExec.prepareMounts` (`container_exec.go:1742`) → `exec.run` + phases → `withExec.applyOutputs` (`:2195`) | deferred (the container run) | call/call_exec: recipe digest ✓. Lazy op: CLASS only (`profCallClass`), no ident; ResultID=sharedResult id at end (`cache.go:3116`). `exec.run`: producer CallDigest (`executor.go:130-138`) ✓. Wrapper phases: nothing |
| `Container.from` | registry `ResolveImageConfig` — manifest+config over the network (`core/schema/container.go:1122-1130`) — EXPENSIVE | `ContainerFromImageRefLazy.Evaluate` (`container_image.go:75`) — the layer pull | BOTH halves | same as above minus exec.run: the pull records only under the un-attributed lazy op |
| `Directory.*` / `File.*` mutation & view lazies | arg normalization, shell construction — cheap | snapshot reopening, content ops; frequently `cache.Evaluate(parent)` → NESTED lazy ops chain under the same forcer | deferred | lazy op class only; nothing else |
| `Container` config mutations (`withEnvVariable`, …) | shell copy | `materializeContainerStateFromParent` + the eager helper | deferred but usually small; the parent-evaluation it forces can be huge | lazy op class only |
| git / http / host / module load | EVERYTHING (fetch, snapshot, codegen) | — none | sync | call/call_exec digest ✓ (v1 semantics already correct) |
| services | `Service` object creation cheap | not dagql-lazy: `Services.startWithOpts` singleflight keyed by a service digest (`core/services.go:473-477`) | start+healthcheck under `service.start` | `service.start` Ident = the service's CONTENT-PREFERRED digest (`services.go:524, :1032-1034`) — but the ident attributes per-session runtime READINESS, not result production: warm runs re-start services, so it never roots elision regions in either digest direction (the §4.1 landed resolution, V33) |

Two structural facts about the lazy shape, both load-bearing:

- The lazy op is parented under the **forcer**, not the producer
  (`cache.go:3045` begins it on `evalCtx`, derived from the forcing caller's
  ctx), and joiners of an in-flight evaluation record `WaitReasonLazy` waits
  targeting it (`cache.go:2988`). The producer call's subtree contains none
  of this.
- The **wrapper phases are inside the lazy op's subtree**. For withExec,
  `prepareMounts`/`applyOutputs` record under the evaluation context
  (`container_exec.go:1742, :2195`) — i.e. the exact 1.31s "wrapper
  remainder" the V26 calibration measured is attributable the moment the
  lazy op itself is.

### 1.3 What the OTel source records

The OTel lazy span carries only `wcprof.op.kind=lazy`
(`dagql/otelprof_lazy.go:159, :174`) — no `dag.digest`. Its NAME is either
the producing class or `"resume <field>"` (the UI-load-bearing resume form,
`otelprof_lazy.go:142-160`). So the OTel source has strictly less lazy
attribution than native (which at least has ResultID engine-locally).

---

## 2. Cache-semantics ground truth

### 2.1 What the cache caches, exactly

The dagql cache caches **calls**: recipe digest → `sharedResult`
(`internal-docs/cachebasics.md`, `egraph.md`). Production completeness is a
separate axis stored on the SAME `sharedResult`: `lazyEval` /
`lazyEvalComplete` (`cache.go` lazy state; `lazy_evaluation.md`). There is no
cache entry for "the production" distinct from the call. Consequently:

- **A hit returns the attached result in whatever production state it is
  in.** Every call hit passes through `ensurePersistedHitValueLoaded`, which
  re-runs `registerLazyEvaluation` before the hit escapes
  (`cache_persistence_import.go:587, :594`) — persisted or not — so the hit
  carries the pending callback if production hasn't happened; the
  `AttachResult` hit-rewrap path does the same (`cache.go:2021`).
- **Within engine lifetime, production runs at most once per shared
  result**: the first `Evaluate` leads (`cache.go:3014+`), concurrent forcers
  join via `lazyEvalWaitCh` with recorded lazy waits (`cache.go:2972-3011`),
  and once `lazyEvalComplete` every later `Evaluate` returns on the fast path
  (`cache.go:2949`) — recording NOTHING (see §4.4).
- Failure leaves production pending and retryable (`lazy_evaluation.md`).

### 2.2 What the recorded "hit" outcome means (an asymmetry)

- **Native**: `OutcomeHit` is stamped whenever `res.HitCache()`
  (`cache.go:3624`) — REGARDLESS of pending production. A native "hit" means
  "the outer call was satisfied from cache", nothing about production.
- **OTel**: `CachedAttr` is stamped only when
  `cached && !dagql.HasPendingLazyEvaluation(res)` (`core/telemetry.go:270`);
  a pending-lazy hit instead gets `PendingAttr` (`core/telemetry.go:303-307`),
  which the loader currently ignores. So an OTel "hit" means "outer hit AND
  production already complete"; a pending-lazy hit loads as generic "ok".

This asymmetry is invisible today only because the calibration extracts hit
sets from native dumps. It becomes load-bearing the moment hit-set extraction
or eligibility runs on OTel captures of lazy-heavy warm runs — and it is an
emit-level fact (`PendingAttr` exists; the loader could consume it), never an
analyzer guess.

### 2.3 Persistence: the lazy form is first-class

`EncodePersisted` on Directory/File/Container chooses between a **snapshot
form** (concrete snapshot available) and a **lazy form** (`LazyKind` +
`LazyJSON`, the typed recipe + attached dependency refs) —
`core/directory.go:260-273`; a result with NEITHER is transiently
unpersistable (`ErrPersistStateNotReady`, `dagql/cache.go:94`,
`core/directory.go:280`, handled by the worker at
`cache_persistence_worker.go:243`). Every lazy type has a `persisted*Lazy`
twin including `persistedContainerExecLazy` (`core/container_exec.go:104`)
and `persistedContainerFromLazy`. On import, a lazy-form hit is decoded and
returned with production still pending (`cache_persistence_import.go:587,
:594`): **"outer cached, inner not-yet-produced" is a real, engineered,
cross-restart state — not a hypothetical.**

### 2.4 Additional states the code shows

- **Substituted production**: `ContainerVolatileExecCacheHitLazy`
  (`core/container_exec.go:116`) materializes from a broad volatile-exec
  cache hit and restores request-local env — production replaced by a
  cheaper materialization from a different cached result. (Per review: today
  this shape is decoder-supported — reconstructed from persisted JSON
  carrying `VolatileCacheHitParentResultID`, `container_exec.go:2218` — with
  no live runtime construction site found; treat it as an import/legacy
  form, cited here for the STATE it demonstrates rather than its current
  frequency.) Below-dagql content reuse (buildkit snapshot/content stores)
  has the same shape live and constantly: the dagql call misses, the
  production runs, but its internals are cheap because layers exist. This is
  where "inner cached, outer not" actually lives.
- **Content-digest rebinding**: `Container.from` detaches identity from the
  tag to the content digest after resolving (`core/schema/container.go:1158+`
  comment), and equivalence teaching generally means a hit can arrive through
  the e-graph rather than exact recipe identity — stated simplification #1's
  domain, quantified by the module-build calibration.
- **Remote cache (directional)**: nothing in this repo implements it, but the
  persisted lazy/snapshot split IS its structural template: a remote hit that
  must transfer content before the payload is usable is "outer hit +
  production-not-materialized" where the first materialization costs a PULL
  instead of a re-run. That is the concrete meaning of the design's pullCost
  seam (see §3, state B2).

---

## 3. The meaning-space: outer call state × production state

"Outer" = the dagql call (recipe digest D). "Inner" = D's deferred
production. Combinations, verdicts, and the counterfactual each implies:

| # | outer × inner | real? | code path | counterfactual semantics for "D ∈ CachedSet" |
|---|---|---|---|---|
| A | miss × (runs, sync or deferred) | REAL — the cold case | resolver runs (`getOrInitCallInner`); production sync in-resolver or later under the first forcer's lazy op | This is what elision REMOVES. For sync families: the call subtree (v1). For lazy families: call subtree + the deferred-production ops attributed to D, wherever they were recorded (the general rule) |
| B1 | hit × production complete (materialized in memory) | REAL — the common warm case | `lookupCacheForRequest` → materialized payload; OTel stamps CachedAttr (`telemetry.go:270`) | The **local warm hit**: `finish = start + 0`; nothing of D runs. v1+A1's target semantics — correct as approved |
| B2 | hit × production pending | REAL, two variants: (i) in-memory hit on a not-yet-forced lazy result — every hit passes `ensurePersistedHitValueLoaded`, which re-registers the pending callback (`cache_persistence_import.go:587, :594`); the `AttachResult` hit-rewrap path does the same (`cache.go:2021`); (ii) persisted lazy-form import — same path, envelope decoded to a lazy recipe | hit returns pending result; FIRST Evaluate runs the production under the forcer | The hit costs `start + 0` for the CALL, but production still happens once, at first demand. **This is structurally the remote-pull scenario**: "cached" delivered the recipe/identity, and first materialization costs X — X = a production re-run (persisted lazy form) or a pull (remote cache). The pullCost seam's REAL meaning: not a constant on the hit, but the cost of first materialization, charged at the first forcer. v1 charging pullCost at the CALL is a simplification of this (adequate while pullCost=0) |
| B3 | hit × production complete via persisted SNAPSHOT envelope | REAL | envelope decoded at HIT time, inside `ensurePersistedHitValueLoaded`, before the hit escapes (`cache_persistence_import.go:552-631`, `core/directory.go:303`) | warm hit plus a bounded decode cost charged AT THE CALL (not at a forcer) — distinct from B2's first-forcer cost |
| C | "inner cached, outer not" (production skipped under an outer miss) | NOT a dagql state | no cache entry exists for production separate from the call | Exists only BELOW dagql: buildkit content reuse and the volatile-exec substitution (`ContainerVolatileExecCacheHitLazy`) make production cheap/substituted while the outer call misses. Not addressable by a recipe-digest CachedSet, and therefore correctly OUT of scope for this simulator; the honest treatment of a recorded run that benefited from it is: the production ops are in the data with their (small) real durations, and elision/keep applies to them as recorded |
| D | hit through EQUIVALENCE (different recipe, same eq-class) | REAL | e-graph structural/term lookup, content-digest teaching (`egraph.md`) | stated simplification #1: not modeled; understates savings; remedy = equivalence-fact emit |

Erik's three anchors verified: (a) = B1 ✓; (b) = B2 ✓ — with the refinement
that B2's "pull" is one instance of a general "first-materialization cost"
that also covers the persisted-lazy re-run (snapshot-envelope decode is NOT
in this family — it is B3's at-hit cost); (c) = A ✓.

Consequence for the simulator's hypothesis semantics: **"D ∈ CachedSet"
under the local-warm-hit model (v1) means asserting state B1** for every
caller of D — outer skipped AND production gone. That is only coherent if
the elision removes D's production wherever it was recorded — which is
exactly why the call-subtree-only rule failed on lazy families, and why the
fix must be attribution-driven, not kind-driven. A future
`pullCost > 0` mode is asserting B2, and its faithful shape is "production
replaced by a cost at the first forcer" — the current
charge-at-the-call plumbing is an approximation to revisit when pullCost
becomes real (the calibration harness is already the test bench for it).

---

## 4. The general rule: deferred-production attribution

### 4.1 Statement

> An op is part of recipe digest D's production iff its emitted attribution
> says so. Elision regions for D ∈ CachedSet are: (1) the nesting subtrees of
> D's non-hit call ops (the sync half), plus (2) the subtrees (root
> inclusive) of every op carrying a deferred-production attribution to D —
> regardless of op kind (landed narrowing: except `service_start`, whose
> ident is readiness, not production — the V33 resolution below). Demand,
> keep-fixpoint, ancestor-waiver, and whole-region elide-or-keep apply to
> (2) exactly as A1 defined them.

Today, attribution-to-D exists on: `exec.run` (Ident = CallDigest,
`executor.go:130-138` — A1's source), and — nominally — `service.start`
(Ident = the service's content-preferred digest, `services.go:524,
:1032-1034`; this analysis originally treated it as usable where that
digest coincides with the recipe digest a CachedSet names, but the landed
resolution below excludes it from sourcing ENTIRELY — the ident is real
attribution of readiness, not of production). It is MISSING on the one op
that would make the rule complete and kind-agnostic: the **lazy op
itself**.

> **Landed resolution (V33):** implementation surfaced the deeper fact —
> service startup is per-session runtime READINESS, not result production
> (`ServiceKey` is session-scoped, `core/services.go:473-477`; a real warm
> run re-starts every service with all results cached). So `service.start`
> is EXCLUDED from region sourcing entirely, in both digest directions: the
> coincidence caveat above is moot, and eliding a coincident start would
> remove work warm reality re-pays. This is a reasoned narrowing of the
> "regardless of op kind" rule, pinned from both directions by V33.

### 4.2 The missing emit, precisely

At `dagql/cache.go:3045` the lazy op is begun with class
`profCallClass(resultCall)` and NO ident — while `resultCall` (the
producer's authoritative `ResultCall`, loaded two paragraphs up at
`cache.go:3017`) is in scope. The recipe digest is one call away:
`resultCall.deriveRecipeDigest(c)` (`dagql/result_call_frame.go:289`),
memoized on the frame (`recipeDigestOnce`, `result_call_frame.go:633+`), so
the marginal cost is at most one digest derivation per lazy evaluation (not
per hit), usually zero (already derived at publication). The emit:

- **native**: `Ident: <recipe digest>` in the `BeginOp` opts at
  `cache.go:3045`.
- **OTel**: `attribute.String(telemetry.DagDigestAttr, <digest>)` on both
  span-mint branches in `beginOTelLazyOp` (`otelprof_lazy.go:159, :174` —
  the complete set of lazy span mints). Symmetric with how `exec.run`'s
  OTel span carries it (`engineutil/otelprof.go`).

Two disciplines the emit must state up front (review-flagged, both
resolvable at the seam):
- **Lock order**: the leader block at `cache.go:3045` runs while
  `shared.lazyMu` is held (`:2955` → unlock `:3069`), and
  `deriveRecipeDigest` can recurse through result refs into
  `egraphMu.RLock` (`result_call_frame.go:1035` region). No current reverse
  edge exists, but the emit should not mint a new `lazyMu → egraphMu`
  ordering constraint: derive the digest BEFORE taking `lazyMu` (the frame
  memo makes the early derivation free on the common path), or read a
  digest pre-derived at publication time.
- **Error path**: `deriveRecipeDigest` returns an error; the emit degrades
  by OMITTING the ident (the op stays class-only, exactly today's shape) —
  it must never alter lazy-evaluation behavior. An ident-less lazy op on a
  hypothesized digest is then simply not in any region, the same
  under-elision honesty the exec fallback (`state.id`) already has.

Because this is the single choke point for ALL lazy evaluation, one emit
covers all ~68 lazy types at once — Container, Directory, File, from's pull,
the volatile substitution, everything — with zero per-type work and zero
inference. Loader-side: nothing to change; the existing ident plumbing
carries it (`wcotel/loader.go` already maps `dag.digest` to Ident for every
span). Analyzer-side: region sourcing adds `lazy`-kind idents to the existing
exec-kind index (NOT `service_start` — per-session readiness, never
production; the §4.1 landed resolution) — the fixpoint,
waivers, and replay are UNCHANGED (they are already kind-agnostic over
"root-inclusive attributed region").

Optional companions in the same emit review (each independent):
1. `PendingAttr` consumption in the loader (closes the §2.2 hit-meaning
   asymmetry for OTel warm captures).
2. A hit-outcome refinement recording production state at hit time
   (native could distinguish B1/B2 hits via `HasPendingLazyEvaluation` at
   `cache.go:3624`'s outcome switch) — this is what makes a future
   pullCost-at-first-forcer model calibratable.

### 4.3 Is A1 a strict special case? Where the answers differ

A1-as-shipped = the general rule restricted to exec-kind attribution. On
workloads, the general rule differs wherever production cost is recorded
OUTSIDE `exec.run` subtrees:

- **The wrapper phases** — `withExec.prepareMounts` / `withExec.applyOutputs`
  and the lazy op's own self — sit inside the lazy op's subtree but outside
  `exec.run`'s. The V26 calibration measured this exact residual: **1.31s of
  a 3.86s counterfactual (~25% of baseline)** on the withExec pipeline. The
  general rule elides it; A1 cannot. (The lead's condition-3 "no class-based
  wrapper attribution" is thereby honored: the wrapper joins via the lazy
  op's OWN emitted digest, not via class names.)
- **Non-exec lazy production**: `Container.from`'s layer pull, all
  Directory/File lazy work — invisible to A1, covered by the general rule.
- **Nested lazy chains**: a lazy callback forcing its parent records a
  NESTED lazy op under the same forcer (evaluation context nesting,
  `cache.go:3015-3045`). Under the general rule each carries its OWN
  producer digest: caching only the outer digest elides the whole chain by
  nesting (parents were only demanded to produce it — correct, and any
  recorded external demand still keeps via the fixpoint); caching an inner
  digest elides exactly that inner region. A1 sees none of this.
- On pure exec shapes with negligible wrappers, the two agree; A1's
  ancestor-waiver semantics, third-party keeps, and V24–V26 expectations all
  carry over verbatim (the waiver's "ancestor of the region root" predicate
  is unchanged — the lazy op's region root is the lazy op, and its forcer
  chain is its ancestry).

So: strict subset in mechanism, strictly dominated in coverage, no case
where A1 gives a better answer — only cases where it gives less.

### 4.4 A genuinely new finding: the silent post-completion forcer

Once `lazyEvalComplete`, later `Evaluate` calls return on the fast path
(`cache.go:2949`) recording NO op and NO wait edge — a forcer that arrived
after completion leaves no recorded trace of its dependency on the
production.

Scoped precisely, per review: for the cached digest D ITSELF this is NOT a
hole — under B1 semantics ("D's payload is materialized in the warm cache"),
a post-completion forcer of D would counterfactually hit the materialized
payload too, so eliding D's production and leaving that forcer untouched is
the correct answer. The real hole is one level down: **production of a
NON-hypothesized digest that ran nested inside an elided region** (a parent
evaluated within D's lazy chain, §4.3's nesting case). If a later consumer
outside the region forced that nested result after completion, the data has
no edge; elision removes the nested production with D's region, the
surviving consumer's recorded timeline shows the free fast path, and the
counterfactual silently omits the production cost that consumer would now
bear. Error direction: savings OVERSTATED — the failure mode the
elide-or-keep doctrine exists to refuse — but bounded to nested
foreign-digest production inside elided regions.

The remedy must carry the missing FACT: "op X forced materialization of
result R" is an evaluation-time event, and no structural data can substitute
for it. Its analyzer consumption rule must encode the B1 scoping above: a
forced-fact whose digest is ITSELF in CachedSet demands nothing (the forcer
would hit the materialized payload counterfactually); only a forced-fact
naming a NON-hypothesized digest whose production sits inside an elided
region rescues that nested production (keeps it, or the enclosing region,
per the usual whole-region rule).

**STATUS: landed (Erik's 2026-07-05 ruling; catalog row V31).** The fast
path at `cache.go:2949` now emits the fact (a `LinkKindForced` event / a
forced-purpose span link, deduped per forcer, targeting the completing lazy
op retained for this purpose), and the keep test consumes it with exactly
the rule above (a live op's fact into the region keeps it; an
eligible-hypothesized-digest fact is free under B1). There is deliberately
NO ancestor waiver for facts, unlike A1's waits: a fact is emitted only on
the Evaluate fast path — post-completion consumption — while the launch
join takes the slow path and never emits one, so an ancestor's fact is a
survivor's real demand like any other (pinned by V31's ancestor variant).
The interim stated-simplification treatment is therefore withdrawn. In particular the Chunk-4 **cache-DAG input edges are NOT usable as
keep-demand here** (an earlier draft of this analysis proposed that; review
refuted it): `CacheInputs` are structural recipe references, and every
ordinary consumer of D carries D there — under B1 those consumers use the
materialized hit and demand nothing, so input-edge demand would
systematically under-elide and destroy real savings. The honest fix is a
small emit at the Evaluate boundary: a zero-duration "forced (already
complete)" event — waiter op → producer digest — at the `cache.go:2949` fast
path (the leader/joiner paths already leave ops and waits). Volume is
bounded by Evaluate calls on completed results; if measurement shows it
matters, it can be sampled DOWN only by dropping duplicates per (waiter op,
digest), never by inference.

### 4.5 Calibration implications, revisited

- The **1.31s wrapper remainder** (V26): fully explained; the general rule
  recovers it (§4.3). Expected post-emit drift on the withExec pipeline:
  the counterfactual chain loses its wrapper segment, leaving the ~365ms
  session phase + the untransferred `Container.from` digest — i.e. drift
  should fall from +362% toward the digest-instability floor.
- The **module-build gap** (+17069%): re-examined against §1.1 — module
  loading is resolver-synchronous (no lazy types; codegen inside the
  asModule region, proven by the ranking). NO part of that gap is
  lazy-attribution; it is entirely simplification #1 (run-specific recipe
  digests / equivalence hits), as originally accounted.
- The **untransferred `Container.from` digest** (2.19s in the pipeline
  calibration) is digest instability, not attribution — but note from is
  ALSO the both-halves-expensive class, so once digests transfer (equivalence
  emit), its pull elision will need the general rule too.

---

## 5. A1's standing — honest assessment

**Verdict: subsume.** Concretely:

- The MACHINERY A1 introduced — root-inclusive attributed regions, the
  ancestor-of-root production-waiver (counted, printed), third-party keeps,
  the `coveredByExcl` structural propagation, rows V24–V25's semantics — is
  the general rule's machinery and survives unchanged. Nothing about it is
  exec-specific; review already hardened it.
- The region SOURCING (exec-kind idents) is the special case Erik smelled.
  It is not wrong — it consumes real emitted attribution and improved the
  calibration — but it is a data-availability accident: exec.run happens to
  be the only deferred-production op carrying a producer digest today.
- Path: land the lazy-op ident emit (§4.2; one choke point, both sources,
  near-free), generalize region sourcing to "any op kind whose ident is a
  cached digest and which is not itself a call/call_exec" — as landed:
  every non-call ident-carrying kind EXCEPT `service_start` (per-session
  readiness, never production; §4.1 landed resolution, V33) — and keep
  exec-ident sourcing operative regardless, since it is what pre-emit traces
  (including every existing Cloud trace) carry. A1's V24–V26 rows stay
  valid as the exec-shaped instances of the general rule; new rows cover
  lazy-shaped and from-shaped fixtures, plus the §4.4
  forced-evaluation-fact emit (landed).
- Not proposed: reverting A1 (loses real coverage on all existing traces for
  no gain), or keeping it as-is (leaves the majority of lazy production —
  and the measured 25% wrapper residual — unattributed).

**RULING (Erik, 2026-07-05): all of the above approved and LANDED as one
change set** — (1) the general rule + lazy-op ident emit (with the two §4.2
disciplines binding), (2) the hit-production-state emit (`hit_pending`,
OTel-additive), (3) the forced-evaluation-fact emit + its consumption rule,
and (4) the loader's PendingAttr-aware outcome precedence (with the
bare-PendingAttr ambiguity resolved to "never guessed" — see the design md
§6.5 note 13). Catalog rows V27–V34 pin the set; A1's sourcing became the
automatic pre-emit-trace fallback of the general rule, as §5 proposed.
