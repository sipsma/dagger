# WHITEBOARD

## Agreement

## TODO
* Make sure that storing ID of objects on so many objects like Directory/File/Container doesn't actually result in O(n^2) space
   * Requires that pointers in call.ID are shared (or something fancier)
   * Probably just make sure shallow clone is used less, only when utterly needed for id op. make stuff more immutable
* Assess changeset merge decision to always use git path (removed `conflicts.IsEmpty()` no-git fast path), with specific focus on performance impact
   * Compare runtime/cost of old no-git path vs current always-git path in no-conflict workloads
   * Confirm whether correctness/cohesion benefits outweigh any measured regression and document outcome
* Remove internal `__immutableRef` schema API once and for all
   * Replace remaining stable-ID use cases with a cleaner non-internal API pattern in dagql/core
* Review the new HTTP implementation for clarity/cohesion
   * Current implementation is functional but confusing; do a low-priority cleanup pass
* Fix `query.__schemaJSONFile` implementation to avoid embedding megabytes of file contents in query args
   * Build/write via ref/snapshot path directly instead of passing huge inline string payloads through select args
* Clean up `cloneContainerForTerminal` usage
   * Find a cleaner container-child pattern for terminal/service callsites instead of special clone helper
* replacing CurrentOpOpts CauseCtx with trace.SpanContextFromContext seems sus, needs checking
* Reassess file mutator parent-passing + lazy-init shape (`WithName`/`WithTimestamps`/`Chown`/`WithReplaced`)
   * Current implementation passes parent object results through schema into core and appears correct in tests, but may not be the most cohesive long-term model.
   * Follow-up: revisit whether lazy-init/parent snapshot modeling can eliminate this explicit parent threading while preserving correctness for service-backed files.
* Assess whether we dropped any git lazyness (especially tree) and whether we should restore it
* Assess whether we really want persistent cache for every schema json file, that's probably a lot of files that are actually kinda sizable!
* Find a way to enable pruning of filesync mirror snapshots
   * Pretty sure filesync mirrors are currently not accounted for by dagql prune/usage accounting.
* !!! Check if we are losing a lot of parallelism in places, especially seems potentially apparent in the dockerBuild of e.g. TestLoadHostContainerd, which looks hella linear and maybe slower than it used to be
   * Probably time now or in near future to do eager parallel loading of IDs in DAG during ID load and such

## Notes
* Big downside to storing ID on Directory/File/Container and re-exec'ing to dagop-flavor is that IDs are really per-caller now, but results are shared. So if we store the ID, what which one is it? Too confusing to think through.
   * Eh, technically the e-graph equivalence should take care of this, might not matter
   * HOWEVER it would still mean that if you e.g. select a directory on a container, then the "presentation ID" to the caller should be something it knows. But I guess if the presentation ID is just "...Container.directory", then who cares?
   * Crucial part to is that Container has a cache dep on its referenced Directories, cannot prune directory unless Container is pruned
   * It will be a little weird that Container has a ObjectResult[Directory] and an associated ID that's not necessarily relevant in particular anymore (at least, recipe not necessarily relevant in its entirety, just the digests used for equivalence checking). But I'm okay with weird for now, not a big deal at all for this iteration.
   * So to summarize all of the above: Container storing ObjectResult for Directory (and similar patterns elsewhere) should be a-okay if our understanding is correct

* Approach to replace the convoluted "dag-op self reinvoke in different flavor" approach:
   * API fields can be marked as "persistable"
   * They are checked in memory same as any others
     * If we end up paging out to disk, we'd need an fallback check of on disk, which might be a lil tricky but feasible. 
       Not needed for now though
   * On completion, everything is stored in memory still, but we put it in a "persist queue"
     * In "page to disk" future, stays in-mem until persisted
   * Part of what is persisted, is the ID! As explicated in later point ("For persistence, ..." below)
   * On session close, does not leave memory
   * Then, in terms of lazy evaluation, there is no extra step that has to go through the cache. It's just a lazily evaluated/single-flighted callback. Emphasis on callback I guess, that needs to be set on the object as a field
   * On engine restart, we load the whole ID. 
      * Anything in the ID that relied on per-client state must be guaranteed cached too or else we are screwed of course, but that's fine and a given in reality.
     * This also relies on the fact that anything even slightly expensive stays in the persisted state so we don't recompute it when loading. We can just fly through the id loading and be done with it.

* For persistence, it's basically like an export. Don't try to store in-engine numeric ids or something, it's the whole DAG persisted in single-engine-agnostic manner. When loading from persisted disk, you are importing it (including e-graph union and stuff)
  * But also for now, let's be biased towards keeping everything in memory rather than trying to do fancy page out to disk

* Need to be really careful about .Clone() now
   * Should the result be cloned? I'm going with no because usually Clone is used to make a new thing
   * Honestly clone should be re-assessed fundamentally, but just be careful for now
   * I guess there are cases where perhaps you *DO* want to clone the result because it doesn't change, jsut metadata. I.e. selecting a subdir of a Directory, you want same result but new metadata field 

* A lot of eval'ing of lazy stuff is just triggered inline now; would be nice if dagql cache scheduler knew about these and could do that in parallel for ya
   * This is partially a pre-existing condition though, so not a big deal yet. But will probably make a great optimization in the near-ish future

* We should update the worker snapshot manager to be named that, not cache manager. 

* New rule of thumb for core: if it returns a LazyInitFunc, you should almost certainly not be calling it directly unless you are a dedicated core/schema api wrapping it. If you are not, you should be making a dagql.Select and/or id load to call this. That way, we keep all the ref+cache management in control of the dagql server!

* The current work in moving in the direction of not having the buildkit cache.Manager either (not to be confused with the solver.CacheManager...)
  * Idea in long run is to just store all the metadata in the objects themselves rather than storing the bkcache.ImmutableRef.
  * We are setting up bookkeeping and ref-counting (i.e. parent/child pointers) to aim for this future.


# Persistence Epic

## Persistence Notes

* Simulated persistence behavior can be misleading for fields whose IDs are still per-session/per-client scoped.
  * We should eventually avoid persisting results that can never be reused cross-session.
  * Not a blocker for this experiment.
* `container.from` needs follow-up on scope behavior.
  * End goal is that the digest-addressed invocation is persistable/reusable cross-session.
  * Tag-addressed flows can remain session-scoped where appropriate.
* Similar follow-up for `http` and any other per-client-scoped APIs that were marked persistable.
  * They may get pinned in-memory but provide little/no cross-session reuse until ID scoping is adjusted.
* TODO: function calls are now marked persistable unconditionally.
  * Optimization follow-up: skip persistable marking for per-session and never-cache function policies.
* There appears to be a race risk around ref-count reaching zero vs concurrent cache hits/acquires.
  * Need to verify exact behavior in current egraph lifecycle and ensure retain-on-zero does not make it worse.
* We need to integrate this with `safeToPersistCache`, but not yet.
  * Follow-up likely requires flipping default `safeToPersistCache` from false to true, then explicitly opting out where needed.
* Root cause discovered in `TestContainer/TestFileCaching/use_file_directly` during persistence experiment:
  * Retaining a persistable vertex (e.g. `Container.withExec`) without retaining its dependency chain allows e-graph equivalence evidence to be pruned.
  * In practice, dependent non-persistable terms (`Host.file`/`Directory.file`/`Container.withFile`) can be dropped on session close, so the retained term no longer has the bridging structure needed to match equivalent future inputs.
  * Short-term experiment strategy: retain full dependent results transitively whenever a result is retained/persisted.
  * Longer-term optimization option: retain only the necessary e-graph proof structure transitively (instead of full result payload retention), but that is explicitly deferred for now.

## Persistence Tasks

### Base implementation for memory-only first pass

* [x] Add a sticky retain flag on cached call results in dagql cache state (`sharedResult`) for simulation-only persistence.
* [x] When a persistable field call completes (initializer path), set the retain flag so zero-ref release does not evict it from egraph memory.
* [x] On cache hit from a persistable field, upgrade the hit result to retained even if original producer field was non-persistable.
  * Add explicit code comments explaining this ambiguous behavior choice so we revisit it intentionally later.
* [x] Update release lifecycle: when refcount reaches zero and retain flag is set, skip egraph removal and skip `onRelease`.
* [x] Add lightweight observability for the experiment (at least counters/logging for retained entries and session-close deltas).
* [x] Add focused tests for simulation semantics:
  * persistable result survives session close and is hit by a new session
  * non-persistable result still drops on zero ref
  * persistable-hit upgrade path retains entries created by non-persistable producers
* [x] Implement transitive retention of full dependent results for retained/persisted entries (simple/heavier experiment path).
  * [x] Rename `retainInMemory` to `depOfPersistedResult` to make intent explicit: this is about dependency liveness for persisted roots, not a generic retention feature.
  * [x] Add per-result dependency tracking on cached results (`sharedResult` -> dependent `sharedResult`s).
  * [x] Populate dependency edges during e-graph indexing by mapping each term input eq-class to a live producer result when available.
  * [x] Add transitive retention propagation helper (DFS/BFS under `egraphMu`) that marks all reachable dependencies as `depOfPersistedResult`.
  * [x] Apply transitive retention when:
    * a persistable field call completes
    * a persistable field cache-hit upgrades an existing non-persistable-produced result
  * [x] Keep retention sticky for now (no unretain/pruning pass in this experiment).
  * [x] Validate with:
    * `TestContainer/TestFileCaching/use_file_directly` (expected to stop re-executing on run #2)
    * focused dagql persistence tests
    * session-close retained-count logs still behaving as expected
* [ ] Deferred optimization follow-up: evaluate retaining only e-graph proof structure transitively (instead of full result payloads).

### TTL support

#### Plan

* Goal: re-add TTL enforcement in dagql cache/e-graph with no DB dependency.
  * TTL should gate cache-hit eligibility, not directly own object lifetime mechanics.
  * Keep this as an in-memory-only implementation for now.

* Core design: store TTL directly on `sharedResult`.
  * Current lookup paths:
    * term digest -> term set -> term results
    * output digest fallback -> result set -> associated term
  * Lookup already resolves to `sharedResult` objects; expiration checks should happen there.

* Proposed data model updates:
  * `ongoingCall` gets `ttlSeconds int64` copied from `CacheKey.TTL`.
  * Keep `egraphResultsByTermID` as:
    * `map[egraphTermID]map[sharedResultID]struct{}`
  * Add expiry on `sharedResult`:
    * `expiresAtUnix int64`
  * Expiry semantics:
    * `0` => never expires
    * `>0` => unix expiration timestamp

* Index/write behavior:
  * On call completion, set `sharedResult.expiresAtUnix` from `ongoingCall.ttlSeconds`:
    * `expiresAtUnix = now + ttlSeconds` when TTL is set
    * `expiresAtUnix = 0` when TTL is not set
  * If a result already exists and a new call with TTL aliases to that same result:
    * use conservative merge policy for now: `min(non-zero expiries)`; `0` only if all are `0`
    * add comments noting this is a policy choice and may need refinement.

* Lookup/read behavior:
  * In both canonical term lookup and output-digest fallback:
    * for each candidate `sharedResult`, check:
      * `expiresAtUnix == 0 || now < expiresAtUnix`
    * skip expired results and continue scanning for another candidate.
  * If all candidates are expired, treat as cache miss and run initializer.

* Session-scope behavior for unsafe TTL returns:
  * Previously this piggybacked on `persistToDB != nil`; now that DB path is removed, restore behavior explicitly with TTL.
  * In `wait` and `initCompletedResult`, use:
    * `if oc.ttlSeconds > 0 && !oc.res.safeToPersistCache { append sessionID implicit arg }`
  * This preserves per-session scoping for unsafe values when TTL is active.

* Expected properties:
  * TTL works in-memory without storage keys or DB.
  * Expired entries naturally stop hitting while leaving lifetime/refcount behavior unchanged.
  * Fits current e-graph/sharedResultID architecture cleanly.

* Tradeoffs / follow-ups:
  * No cross-process persistence yet (intentional for this step).
  * Expired results may linger in maps until normal cleanup or opportunistic prune.
  * One `sharedResult` can be associated with multiple terms/calls that had different TTL intent.
    * With `sharedResult.expiresAtUnix`, we lose per-association TTL precision.
    * Conservative `min` policy avoids over-retention but may expire reusable results earlier than ideal.
    * Future option: retain at least e-graph proof structure transitively while splitting TTL metadata by association if needed.
  * `GCLoop` is still DB-oriented and should be no-op-safe when `c.db == nil` (cleanup task).

#### Implementation

* [x] Plumb TTL into ongoing call state.
  * [x] Add `ttlSeconds` to `ongoingCall`.
  * [x] Set it from `CacheKey.TTL` in `GetOrInitCall`.
* [x] Store expiry on `sharedResult`.
  * [x] Add `expiresAtUnix int64` field.
  * [x] On completed call, compute candidate expiry from `ttlSeconds`.
  * [x] Merge expiry onto shared result with conservative policy:
    * [x] `0` only if all writers are `0`.
    * [x] otherwise earliest non-zero (`min`) wins.
  * [x] Add code comments explaining policy and why it is intentionally conservative.
* [x] Enforce TTL in lookup.
  * [x] Update deterministic result pickers to skip expired results.
  * [x] Apply this for both term-based lookup and output-digest fallback.
  * [x] If all candidates are expired, treat as cache miss.
* [x] Restore session scoping behavior for unsafe TTL values.
  * [x] Replace existing DB-gated checks with `ttlSeconds > 0 && !safeToPersistCache`.
  * [x] Apply in both return-ID shaping (`wait`) and index-ID shaping (`initCompletedResult`).
* [x] Keep e-graph term->result association maps as sets (no map value TTL).
* [ ] Validation.
  * [x] Run focused tests around persistable retention.
  * [x] Run focused tests around output-digest fallback.
  * [ ] Reconcile `TestCacheTTLNonPersistableEquivalentIDsCanCrossRecipeLookup` with new unsafe TTL session-scoping behavior.

### Pruning support

#### Plan

* Goal: implement cache pruning against dagql cache state (not buildkit), while preserving roughly the existing policy interface shape.
* Keep current top-level behavior shape:
  * automatic background gc entrypoint
  * explicit `engine.localCache.prune(...)` entrypoint
  * `useDefaultPolicy` + per-invocation space overrides
* Define dagql-native prune policy model equivalent to current worker policy fields:
  * `all`, `filters`, `keepDuration`, `reservedSpace`, `maxUsedSpace`, `minFreeSpace`, `targetSpace`
* Build dagql-native usage accounting needed for policy evaluation:
  * in-use vs releasable
  * size accounting
  * created/last-used timestamps
  * enough metadata for future filter support
* Implement policy application as ordered passes (like today’s list semantics), with deterministic candidate selection.
* Integrate prune execution under existing server-level serialization (`gcmu`) so automatic and manual prune do not race.
* Keep API return shape (`EngineCacheEntrySet`) but source entries from dagql cache state.
* Follow-up after first cut: remove or isolate remaining buildkit-coupled pruning hooks once dagql prune is authoritative.

#### NOTES

* Main prune-related entrypoints in current code:
  * `Server.gc()` in `engine/server/gc.go`
  * `Server.PruneEngineLocalCacheEntries(...)` in `engine/server/gc.go`
* Main callers:
  * `gc()` is scheduled through `srv.throttledGC`:
    * initialized in `engine/server/server.go` as `throttle.After(time.Minute, srv.gc)`
    * triggered once shortly after startup (`time.AfterFunc(time.Second, srv.throttledGC)`)
    * triggered after session removal in `removeDaggerSession` (`time.AfterFunc(time.Second, srv.throttledGC)`)
  * `PruneEngineLocalCacheEntries(...)` is called from GraphQL `engine.localCache.prune(...)` in `core/schema/engine.go`.
* Current implementation is buildkit-worker based end-to-end:
  * list entries: `srv.baseWorker.DiskUsage(...)`
  * prune: `srv.baseWorker.Prune(..., pruneOpts...)`
  * policy types: `bkclient.PruneInfo`
* Current `engine.localCache` API behavior:
  * `localCache` returns only one policy view (`EngineCache`) derived from `EngineLocalCachePolicy()` (the default/last policy).
  * It does not expose the full policy list, filters, or keepDuration.
  * `entrySet` has no filtering support yet.
  * `prune` returns `Void`, not the pruned set (even though server prune computes a set).
* `PruneEngineLocalCacheEntries` behavior details:
  * serialized by `srv.gcmu`.
  * when no active dagger sessions, calls `imageutil.CancelCacheLeases()`.
  * resolves prune options via `resolveEngineLocalCachePruneOptions(...)`.
  * executes worker prune and accumulates `UsageInfo` responses.
  * if anything pruned, attempts `SolverCache.ReleaseUnreferenced(...)` (buildkit solver metadata cleanup path).
  * returns `EngineCacheEntrySet` built from prune response items.
* `gc()` behavior details:
  * serialized by `srv.gcmu`.
  * runs worker prune with `srv.baseWorker.GCPolicy()` (full policy list).
  * sums pruned bytes and logs.
  * if anything pruned, schedules `srv.throttledReleaseUnreferenced` (5-minute throttle), which calls `srv.SolverCache.ReleaseUnreferenced(...)`.
* Policy resolution behavior today (`resolveEngineLocalCachePruneOptions`):
  * default when `UseDefaultPolicy=false`: single policy `{All: true}` (prune all releasable entries).
  * when `UseDefaultPolicy=true`: copy worker default policy list.
  * per-call overrides (`maxUsedSpace`, `reservedSpace`, `minFreeSpace`, `targetSpace`) are parsed from string disk-space syntax and applied to every selected policy.
  * tests in `engine/server/gc_test.go` verify:
    * override behavior for both default and non-default paths
    * no mutation of the default policy slice when overrides are applied
    * invalid disk-space strings produce argument-specific errors
* Default policy construction path:
  * worker created with `GCPolicy: getGCPolicy(...)`.
  * fallback order inside `getGCPolicy(...)`:
    * explicit engine config policies
    * converted buildkit config policies
    * generated defaults (`defaultGCPolicy(...)`)
  * `getDefaultGCPolicy(...)` currently means “last policy in list.”
  * conversion includes `All`, `Filter`, `KeepDuration`, and space fields.
  * when `SweepSize` is set, `TargetSpace` is derived from `MaxUsedSpace - SweepSize(...)` (clamped so it never uses 0, since 0 means “ignore”).
* Buildkit-specific coupling still embedded in prune surface:
  * `core.Query.EngineLocalCachePolicy()` returns `*bkclient.PruneInfo` (buildkit type leaks into core API interface).
  * `EngineLocalCacheEntries`/`PruneEngineLocalCacheEntries` operate only on worker/buildkit cache records.
  * comments note buildkit prune currently does not populate `RecordType` for pruned items.
* Interaction with current dagql cache:
  * dagql cache has its own `GCLoop`, but this is separate from engine prune APIs.
  * after recent changes, `GCLoop` is effectively no-op-safe when db is nil; it is not the policy-based prune mechanism we need.
* Key migration implication:
  * current public-ish behavior shape is “policy-driven prune with optional one-off space overrides,” but concrete enforcement is entirely buildkit-worker.
  * for hard cutover, dagql cache needs first-class policy evaluation + reclaim semantics; current interface can be preserved while swapping the backend implementation.

#### Implementation

##### Scope and constraints

* Hard cutover target: pruning decisions and deletions are driven by dagql cache state, not buildkit worker disk-usage/prune APIs.
* Keep external behavior shape roughly stable:
  * `engine.localCache.entrySet`
  * `engine.localCache.prune(useDefaultPolicy, maxUsedSpace, reservedSpace, minFreeSpace, targetSpace)`
  * automatic background `gc()` path
* Determinism requirement:
  * candidate ordering must be deterministic for reproducibility and easier debugging.
* Safety requirement:
  * do not prune in-use results (active refs / active evaluation dependencies).
  * prune execution must stay serialized with existing server gc lock (`gcmu`).
* Cohesion requirement:
  * one policy model used by both automatic gc and explicit prune API.

##### Phase 0: CacheVolume snapshot ownership cutover (prereq for pruning)

* [x] Move cache volume from metadata-only to snapshot-owning model in core.
  * [x] Extend `core.CacheVolume` with an owned snapshot ref (similar lifecycle to `Directory`/`File` snapshot ownership, but no lazy init).
  * [x] Add explicit snapshot access/update methods on `CacheVolume` (single place to enforce clone/release/refcount behavior).
  * [x] Keep cache volume identity keyed by namespaced cache key (`cache.Sum()`-derived identity), but ensure source-influenced variants are represented explicitly (see below).
* [ ] Preserve current source semantics while removing BuildKit cache-mount indirection.
  * [ ] Maintain behavior parity (cache mount behavior/result expectations), not strict parity of BuildKit identity internals.
  * [ ] Today, BuildKit cache mount identity is effectively `cacheID + optional baseRefID`; use this as reference behavior, but allow a different dagql-native identity rule if it is more cohesive and still preserves externally visible behavior.
  * [ ] Make cache identity derivation explicit in one place so future semantic changes (especially source/base handling) are easy and low-risk.
  * [ ] Keep owner/source preprocessing behavior coherent with current API expectations (`Owner` applies to source-initialized cache root/entries).
* [x] Replace `MountType_CACHE` runtime creation path with explicit snapshot mounts.
  * [x] In container mount-data prep, stop emitting BuildKit cache mounts for `CacheSource`; instead mount concrete snapshots (directory-style input mount behavior).
  * [x] In `withExec` output handling, capture writable cache mount outputs and write them back to the owning `CacheVolume` snapshot.
  * [x] Apply the same cutover for service container start path (services also call `PrepareMounts`; not just `withExec`).
* [ ] Re-home cache sharing-mode behavior away from BuildKit mount manager.
  * [ ] `SHARED`: default shared snapshot lineage for same cache identity.
  * [ ] `LOCKED`: serialize writes per cache identity in engine (explicit lock keyed by cache identity).
  * [ ] `PRIVATE`: follow current BuildKit-ish behavior for now, but isolate that policy decision behind an explicit seam so we can switch to stricter private semantics later without broad rewrites.
* [ ] Hook CacheVolume into dagql persistence/pruning accounting.
  * [ ] Ensure cache volume snapshot refs are visible to usage accounting (size + last-used/created metadata source).
  * [ ] Ensure pruner can reclaim cache-volume-backed snapshots safely when no active dependers.
* [ ] Validation for Phase 0 (before pruning Phase 1):
  * [x] `TestWithMountedCache`
  * [x] `TestWithMountedCacheFromDirectory`
  * [x] `TestWithMountedCacheOwner`
  * [ ] service path with mounted cache (`core/integration/services_test.go` cases)
  * [ ] platform and multi-session cache-mount reuse cases
  * NOTE: `TestServices/TestServiceTunnelStartsOnceForDifferentClients` currently fails (`expected 1 /cache/svc.txt`, `actual 0 /cache/svc.txt`). Current model writes service cache-mount outputs back on service cleanup; this does not yet provide live shared visibility while service is still running.
* [ ] REVIEW CHECKPOINT 0: confirm cache volume lifecycle semantics (identity, source/base behavior, sharing mode, write-back timing) before implementing prune metadata/policies.

##### Phase 1: Dagql prune metadata and usage accounting

* [x] Add/validate metadata on cache entries needed for pruning decisions:
  * [x] created timestamp
  * [x] last-used timestamp (updated on cache hit and post-initialization return)
  * [x] current in-use status derivable from cache state/refcount
  * [x] disk-space estimate/bytes tracked per entry (or explicit temporary approximation with TODO if exact accounting not ready)
  * [x] stable type/category string for debugging and future filters
* [x] Implement a locked snapshot method in dagql cache to enumerate usage entries with:
  * [x] id
  * [x] description/type
  * [x] size bytes
  * [x] created/lastUsed timestamps
  * [x] in-use bool
* [x] Ensure usage accounting includes retained persisted results (so they are visible candidates if policy says to reclaim).
* [x] Add focused unit tests in `dagql/cache_test.go` for usage snapshot correctness.
* [ ] REVIEW CHECKPOINT 1: stop and validate metadata/usage model before policy engine implementation.

##### Phase 2: Dagql-native prune policy model + option resolution

* [x] Introduce dagql-native prune policy struct (server-local or core-owned) equivalent to current fields:
  * [x] all
  * [x] filters
  * [x] keepDuration
  * [x] reservedSpace
  * [x] maxUsedSpace
  * [x] minFreeSpace
  * [x] targetSpace
* [x] Port/replace `resolveEngineLocalCachePruneOptions` to produce dagql-native policies (same override semantics).
* [x] Keep existing disk-space parsing behavior and error messages for overrides.
* [x] Preserve “default policy list copy + do-not-mutate originals” semantics.
* [x] Keep support for policy generation from engine config (`GC.Policies`, fallback defaults, sweep size behavior).
* [x] Add/port tests currently in `engine/server/gc_test.go` to validate identical override behavior.
* [ ] REVIEW CHECKPOINT 2: confirm policy compatibility and override parity before wiring prune execution.

##### Phase 3: Dagql prune execution engine

* [x] Add a dagql cache API to apply ordered prune policies and return pruned-entry report.
* [x] Implement policy pass execution:
  * [x] Evaluate each policy in order against current in-memory usage snapshot.
  * [x] Apply eligibility gates:
    * [x] not actively used
    * [x] keepDuration cutoff
    * [x] `all`/filters behavior (initially minimal but explicit)
  * [x] Compute reclaim target per policy using max/reserved/minFree/target semantics.
  * [x] Sort candidates deterministically (e.g. oldest last-used, then oldest created, then stable id tie-break).
  * [x] Prune until target satisfied or candidates exhausted.
* [x] Ensure pruning removes e-graph/result state coherently:
  * [x] remove indexes/associations
  * [x] release payload resources/onRelease hooks as needed
  * [x] maintain dependency and retained-state invariants
* [x] Add detailed debug logging hooks for prune decisions (candidate selected/skipped reason, reclaimed bytes).
* [x] Add focused unit tests:
  * [x] keepDuration behavior
  * [x] threshold behavior (`maxUsedSpace`/`targetSpace`)
  * [x] in-use entries never pruned
  * [x] deterministic selection order
* [x] Add explicit support for resolver-produced additional output results so detached results do not leak past resolver completion.
  * [x] Introduce a dagql interface for values that return additional resolver outputs that must be attached to cache once the main result is attached.
    * [x] Proposed shape: `AdditionalOutputResults() []AnyResult` (name can adjust, behavior should not).
    * [x] Additional outputs must be pure handles only; this method must never evaluate or materialize lazy values.
  * [x] Wire `GetOrInitCall` completion path to attach additional outputs immediately after main result indexing/attachment (same request lifecycle, not deferred/post-call).
    * [x] Do this after `initCompletedResult` has attached/indexed the parent result.
    * [x] For each additional output:
      * [x] Attach/reacquire via normal call-cache path using its existing ID (no synthetic IDs), so detached outputs become attached and cached outputs are ref-acquired consistently.
      * [x] Preserve laziness; attachment should not force output evaluation.
  * [x] Record explicit dependency edges from parent result -> attached additional outputs.
    * [x] This is required so persisted parent liveness retains output snapshots transitively.
    * [x] Do not rely only on term-input dependency indexing (that models child -> parent but not parent -> produced outputs).
  * [x] Ensure this flow works for `Container.withExec` outputs specifically:
    * [x] rootfs output directory result
    * [x] writable mount output directory/file results
    * [x] meta output directory result
  * [x] Keep hard-cutover behavior:
    * [x] no use of `PostCall` for this lifecycle step
    * [x] no fallback to container-intrinsic size accounting
    * [x] size accounting remains on snapshot-bearing types (Directory/File/CacheVolume)
  * [x] Add focused dagql tests for the new attachment semantics:
    * [x] detached additional outputs become attached after parent attach
    * [x] attached additional outputs remain lazy until evaluated
    * [x] parent result retains attached output deps in persisted-liveness traversal
    * [x] accounting/prune sees output-backed size through attached outputs (without container size hooks)
  * [x] Add integration assertions around `core/integration/localcache_test.go` scenarios that previously under-counted bytes.
* [x] REVIEW CHECKPOINT 3: review prune semantics and invariants before server API switch.

##### Phase 4: Server integration and entrypoint cutover

* [x] Switch `EngineLocalCacheEntries` from buildkit `DiskUsage` to dagql usage snapshot.
* [x] Switch `PruneEngineLocalCacheEntries` from `baseWorker.Prune` to dagql prune execution.
* [x] Keep existing `gcmu` serialization around explicit prune.
* [x] Switch `gc()` to run dagql prune using default policy list.
* [x] Remove buildkit-specific side effects from prune flow where no longer applicable:
  * [x] `imageutil.CancelCacheLeases()` path
  * [x] `SolverCache.ReleaseUnreferenced(...)` post-prune path
* [x] Keep return type `EngineCacheEntrySet` populated from dagql prune results.
* [x] REVIEW CHECKPOINT 4: verify server-level behavior (automatic gc + manual prune) before API cleanup.

##### Phase 5: API cleanup and compatibility polish

* [x] Remove buildkit type leakage from query interface where possible:
  * [x] replace `EngineLocalCachePolicy() *bkclient.PruneInfo` with dagql-native/core policy type
  * [x] update schema resolver mapping accordingly
* [x] Decide whether to expose full policy list in schema or keep current “default policy only” surface for now.
  * [x] keep current “default policy only” surface for now (full list exposure deferred)
* [x] Ensure docs/comments reflect dagql pruning, not buildkit pruning.
* [x] Update/fix `core/integration/localcache_test.go` to pass while preserving original behavior intent:
  * [x] do not weaken or “cheat” assertions
  * [x] keep testing the same underlying pruning/local-cache semantics the tests originally covered
  * [x] only adjust test mechanics where needed for dagql-prune cutover
  * [x] end state for this phase: `core/integration/localcache_test.go` passing
* [x] Add/adjust tests for `core/schema/engine.go` paths (`localCache`, `entrySet`, `prune`).
* [x] REVIEW CHECKPOINT 5: ensure interface coherence and no lingering buildkit-prune assumptions.

##### Phase 6: Follow-up tasks (non-blocking for first cut)

* [ ] Implement richer filter semantics (if needed) against dagql metadata categories.
* [ ] Improve size accounting precision if first-cut uses approximations.
* [ ] Add metrics for prune runs:
  * [ ] candidates
  * [ ] pruned count/bytes
  * [ ] skip reasons
* [ ] Prepare seam for future async on-disk persistence queue:
  * [ ] prune should operate on in-memory graph of truth
  * [ ] disk updates are best-effort reflection, never source of truth for prune eligibility

# Snapshot Manager Cleanup

## Execution status

* [x] Execute all phases in one continuous pass (0 -> 5), with incremental commits after each phase.
  * This is intentionally long-running so compactions don't reset scope mid-way.

## Goals

* Hard-cut the copied BuildKit cache-manager behavior out of `engine/snapshots`.
* Keep only minimal snapshot lifecycle functionality needed by current engine/core callsites.
* Move lifecycle/dependency/persistence/pruning responsibility out of snapshot manager and into dagql/core (where we already model object dependencies and retention).
* End state should look like a purpose-built Dagger snapshot primitive, not a partially-adapted BuildKit cache system.

## Design constraints (explicit)

* No persistence in snapshot manager:
  * no BoltDB metadata
  * no on-disk cache record/index bookkeeping
  * no migration paths for old metadata schema
* No pruning in snapshot manager:
  * no `DiskUsage` implementation
  * no `Prune` implementation
  * no external ref checker hooks
* No progress-controller plumbing:
  * remove `progress.Controller` from snapshot-manager API surface and internals
* No lazy blob/remote snapshot machinery:
  * remove `DescHandler`/`NeedsRemoteProviderError`-driven lazy/unlazy flow
  * remove stargz-specific behavior and remote snapshot label handling
* No internal parent/dependency retention graph in snapshot manager:
  * no parent ref graph ownership for lifecycle/refcount retention
  * no recursive release behavior based on internal ref DAG
* No `equalMutable` / `equalImmutable` dual representation:
  * mutable and immutable refs are distinct and permanent states
  * no metadata fields or conversion shortcuts based on “equal” sibling records

## Current package observations (from code pass)

* Persistence + metadata are deeply embedded today:
  * `engine/snapshots/metadata.go`
  * `engine/snapshots/metadata/metadata.go`
  * `engine/snapshots/migrate_v2.go`
  * manager init/get/search paths are metadata-index-driven (`chainid`, `blobchainid`, etc.)
* Prune and disk-usage are still implemented in `manager.go` (buildkit-shaped policy model).
* Lazy remote/blob stack is broad:
  * `blobs.go`, `remote.go`, `opts.go`, stargz branches in `refs.go`/`manager.go`
* Parent/lifecycle graph is currently encoded in snapshot refs:
  * `parentRefs` + `diffParents` + recursive parent release
  * metadata parent pointers
* Equal mutable/immutable compatibility paths still exist:
  * `equalMutable`, `equalImmutable`, special remove/release branches

## Plan

### Phase 0: Lock minimal required surface

* [x] Inventory current non-test callsites of `engine/snapshots` API and classify by operation:
  * [x] `Get`
    * Used by schema/directory loading by ID and generic ref handoff paths.
  * [x] `GetMutable`
    * Used by filesync and cache-volume/mount mutation paths.
  * [x] `New`
    * Used broadly for creating mutable snapshots in directory/file/container/service/http/git flows.
  * [x] `Commit`
    * Used broadly after writes/mutations (directory/file/filesync/service/etc).
  * [x] `Mount`
    * Used by filesync/contenthash/git/http/util execution helpers.
  * [x] `Merge`
    * Still used by directory/changeset flows.
  * [x] `Diff`
    * Still used by directory/changeset flows.
  * [x] `GetByBlob`
    * Used by image pull path in `core/containersource/pull.go`.
  * [x] `GetRemotes` / container image export needs
    * Used by exporter (`engine/buildkit/exporter/containerimage`, `engine/buildkit/exporter/oci`).
* [x] Freeze a minimal interface target (`SnapshotManager` + refs) based only on actual callsites.
  * Keep now: `New`, `Get`, `GetMutable`, `GetByBlob`, `Merge`, `Diff`, ref mount/commit/release/clone/finalize/extract, metadata read/write needed by `core/cacheref`, `engine/filesync`, `engine/contenthash`, and `GetRemotes`.
  * Drop now: persistence DB requirements, prune controller contract, progress-controller contract, lazy/stargz-specific public option types.
* [x] Add TODO comments at temporary compatibility seams that survive this phase.
  * Merge/Diff kept for now due active callsites; revisit once higher-level APIs absorb them.
  * GetRemotes kept temporarily in snapshots; bias remains to move export-specific remotes construction toward exporter package as cleanup progresses.
* [x] Checkpoint: confirm we are not preserving API methods solely for legacy internal behavior.
  * Any method kept in this pass has a current non-test caller in `core`, `engine/filesync`, `engine/contenthash`, or exporter code.

### Phase 1: Remove persistence and metadata DB completely

* [x] Delete metadata store dependency from `SnapshotManagerOpt` and manager state:
  * [x] remove `MetadataStore *metadata.Store`
  * [x] remove `root` metadata-db dependent logic (server no longer initializes/closes snapshots metadata DB)
* [x] Delete metadata persistence implementation files:
  * [ ] `engine/snapshots/metadata.go`
  * [x] `engine/snapshots/metadata/metadata.go`
  * [x] `engine/snapshots/migrate_v2.go`
  * Note: `engine/snapshots/metadata.go` is retained but rewritten as in-memory metadata/index logic (no BoltDB, no on-disk persistence).
* [x] Replace record metadata with in-memory fields directly on cache records/refs for values still needed at runtime (description, createdAt, recordType, etc., only if still used by remaining APIs).
* [x] Remove metadata index search paths (`chainIndex`, `blobchainIndex`) and any retrieval that depends on DB-backed search.
  * Index lookup is now entirely in-memory (`metadataStore.index`), including blob/chain and external cache-key indexes.
* [ ] Remove `RefMetadata` API methods that only existed for persisted metadata semantics.
  * Deferred: kept current methods for active core/filesync/contenthash callsites; trim after downstream callsite cleanup.
* [x] Checkpoint: snapshot manager can start, create refs, mount, and commit without any BoltDB or migration path.

### Phase 2: Remove pruning and progress plumbing

* [x] Delete prune/disk usage controller behavior from snapshot manager:
  * [x] remove `Controller` interface members from public `SnapshotManager` contract
  * [x] remove `DiskUsage` + `Prune` implementation and helpers from `manager.go`
  * [x] remove prune-related locks/fields (`muPrune`, `PruneRefChecker`, `GarbageCollect`) from manager state/opts
* [x] Remove progress controller usage from snapshot APIs:
  * [x] remove `pg progress.Controller` parameters from `Get`/`Merge`/`Diff` internals
  * [x] remove `progress` field from immutable refs
  * [x] remove progress-related wrappers in remote pull/unlazy flow
* [x] Update callsites to the simplified signatures.
* [x] Checkpoint: `engine/server/gc.go` and all prune-facing behavior are fully decoupled from snapshot manager.

### Phase 3: Remove lazy/stargz/remote-unlazy machinery

* [ ] Delete lazy descriptor handler model from public options:
  * [ ] remove/replace `DescHandler`, `DescHandlers`
  * [x] remove `NeedsRemoteProviderError`
  * [x] remove `Unlazy` marker option type
* [ ] Delete lazy blob/remote code paths:
  * [ ] `engine/snapshots/blobs.go`
  * [ ] `engine/snapshots/blobs_linux.go`
  * [ ] `engine/snapshots/blobs_nolinux.go`
  * [ ] `engine/snapshots/remote.go` (or replace with minimal non-lazy remote export utility if still required)
* [x] Remove stargz-specific branches in refs/manager code (`Snapshotter.Name() == "stargz"` branches and related remote-label prep).
  * `manager.New`, immutable mount/extract, and mutable mount no longer have stargz-special handling.
* [~] Simplify `GetRemotes` behavior to only operate on already-materialized snapshots/blobs (or move this responsibility upward if no longer needed).
  * In progress: lazy-provider error gates were removed and lazy detection now short-circuits false.
* [ ] Checkpoint: no `unlazy`, no lease-driven lazy pull-on-read, no stargz-specific logic remains.

### Phase 4: Remove internal parent-ref lifecycle graph and equal-ref compatibility

* [~] Delete parent graph ownership from refs:
  * [~] remove `parentRefs`, `diffParents`, recursive parent `release`/`clone`
    * Recursive parent release/clone behavior is removed; `parentRefs.release` is now a no-op and `clone` is shallow.
  * [ ] remove metadata parent encoding (`parent`, `mergeParents`, diff parent keys)
* [~] Delete dual-representation compatibility fields:
  * [ ] remove `equalMutable`, `equalImmutable`
  * [~] remove all paths that special-case them during remove/release/get
    * Materialization no longer creates mutable<->immutable sibling links.
    * manager get/getMutable and ref release paths no longer special-case equal mutable/immutable pairing.
* [x] Enforce simple lifecycle:
  * [x] mutable ref owns one snapshot id
  * [x] commit returns immutable ref for that committed snapshot
  * [x] no new mutable<->immutable “same data sibling” tracking in commit path
* [ ] Adjust `Merge`/`Diff` implementation strategy to avoid internal parent ownership assumptions:
  * [ ] either materialize explicit snapshots immediately
  * [ ] or push these operations upward if unnecessary in new model
* [~] Checkpoint: ref lifecycle is becoming linear/explicit; remaining equal-field struct cleanup + merge/diff parent metadata cleanup still pending.

### Phase 5: Final package trim and naming cleanup

* [~] Remove now-unused files and options after previous cutovers.
  * [x] removed snapshots `Root` option/field (leftover from prune path)
  * [x] removed unused `DescHandlerKey`
  * [x] removed `engine/snapshots/remotecache/*` package subtree
* [x] Remove any remaining imports of BuildKit cache-manager-era helpers that are no longer relevant.
  * direct import grep confirms no non-internal package imports `internal/buildkit/cache`.
* [~] Ensure package comments and type names describe snapshots only (not cache manager semantics).
  * in progress: major API surface now snapshot-focused, but some compatibility names/comments still remain (`cacheRecord`, etc.).
* [x] Re-run direct-import audit:
  * [x] verify no reintroduced dependency on `internal/buildkit/cache`
  * [x] verify only required `internal/buildkit/solver/*` subpackages remain (current external usage is in `pb`, `result`, and selected provenance/errdefs paths)
* [~] Checkpoint: `engine/snapshots` is significantly smaller and more cohesive; final naming/field cleanup remains.

## Validation plan

* [ ] Compile gates after each phase:
  * [ ] `go test ./engine/snapshots/... -run TestDoesNotExist -count=1`
  * [ ] `go test ./engine/buildkit/... -run TestDoesNotExist -count=1`
  * [ ] `go test ./core/... -run TestDoesNotExist -count=1`
  * [ ] `go test ./engine/server/... -run TestDoesNotExist -count=1`
* [ ] Integration smoke after major phases:
  * [ ] `dagger --progress=plain call engine-dev test --pkg ./core/integration --run='TestDirectory|TestContainer' --count=1`
* [ ] Explicitly watch for regressions in:
  * [ ] cache volume mount behavior through `withExec`
  * [ ] image export paths that still depend on snapshot remotes
  * [ ] service startup paths using snapshot refs

## Open questions to resolve during implementation

* [ ] Do we still need `Merge`/`Diff` in snapshot manager at all for current callsites, or can those be removed in favor of higher-level APIs?
* [ ] Which metadata fields must remain in-memory on refs for current UX/debug behavior (description/record type/etc.) versus can be deleted immediately?
* [ ] For image export, should remote-descriptor construction stay in `engine/snapshots` or move entirely to exporter-side code now that lazy handling is gone?

## Answered decisions

* `Merge` / `Diff`: keep them for now; still expected to be used.
* Metadata fields in memory:
  * Keep straightforward fields where easy.
  * If a field is difficult to preserve, verify whether it has active callsites; remove if unused.
  * If used but hard to preserve in the current pass, leave a TODO and keep moving.
* Image export remote descriptor construction:
  * Bias toward moving to exporter-side construction if practical during cleanup.
  * If unexpected complexity shows up, it is acceptable to leave it in `engine/snapshots` for now.

# Size Accounting

## Goals

* Replace placeholder `estimateSharedResultSizeBytes` with real snapshot-backed size accounting.
* Compute size only when it is useful for pruning decisions (avoid eager/always-on cost).
* Include cache-volume snapshots (mutable refs) in accounting.
* Prevent overestimation by deduplicating shared snapshots by snapshot record identity as part of first pass.
* Keep implementation cohesive with current dagql e-graph retention/liveness model.

## Design constraints

* No fallback to synthetic constant estimates once real sizing is available.
* Do not force expensive lazy evaluation purely for metrics.
  * If a result does not currently expose a concrete snapshot without additional compute, leave size unknown until it does.
* Dedupe by underlying snapshot record identity is mandatory in initial implementation.
  * This is required to avoid counting same bytes multiple times when multiple retained results reference the same snapshot.
* Mutable cache volume sizes must be represented and refreshed when they can change.
* First cut should prioritize correctness and determinism over micro-optimizations.

## Notes

* Current `dagql` usage entries are fed by `sharedResult.sizeEstimateBytes`, which is currently set from a placeholder.
* Real size implementation already exists in snapshot manager (`cacheRecord.size`) and persists in-memory metadata size.
* Persisted/retained graph behavior currently uses `depOfPersistedResult` + transitive `deps`.
* Prune-relevant candidates are retained results that are not actively used and not required by active retained roots.
* Cache-volume snapshots are created eagerly in `query.cacheVolume` and mounted into `withExec` as mutable refs; they must be included in size accounting.

## Plan

### Phase 0: Plumbing and interfaces

* [x] Add a small snapshot size access seam in `engine/snapshots` so callers can request real size from refs.
  * [x] Expose a method callable on immutable and mutable refs that returns the underlying `cacheRecord.size(ctx)`.
  * [x] Preserve existing internal metadata update behavior (`queueSize` / commit).
* [x] Add a dagql-side size provider hook for cached payloads.
  * [x] Introduce an internal interface implemented by cacheable core objects that can report snapshot-backed usage size.
  * [x] Implement it for `Directory`, `File`, and `CacheVolume`.
  * [x] `Container` remains aggregate-only (it should not pretend to own a single snapshot size).
* [x] Replace placeholder callsite in `dagql/cache.go` with provider-based sizing path.
  * [x] Keep unknown as explicit state until first real measurement.

### Phase 1: Prune-candidate gated measurement + mandatory dedupe

* [x] Define prune-relevant candidate set in dagql cache state.
  * [x] Candidate must be retained (`depOfPersistedResult` true).
  * [x] Candidate must not be actively used (`refCount == 0`).
  * [x] Candidate must not be transitively depended on by any actively used retained result.
* [x] Add transitive active-dependency closure helper under `egraphMu` to compute exclusion set.
* [x] Compute real size only for candidates with unknown/stale size.
* [x] Implement dedupe by snapshot record identity as part of first pass.
  * [x] For each usage-accounting pass, aggregate bytes by snapshot record ID instead of summing per-result blindly.
  * [x] If multiple results point at same snapshot record, count it once.
  * [x] Ensure deterministic tie-break/ownership when mapping deduped bytes back to entries.
* [x] Ensure `UsageEntries` and prune accounting use the deduped numbers.

### Phase 2: Mutable cache-volume correctness

* [ ] Ensure mutable cache-volume refs refresh/invalidate size when writes can change content.
  * [ ] Invalidate cached size on write paths where mutable content changes are committed/applied.
  * [ ] Recompute lazily on next prune-candidate measurement.
* [ ] Confirm cache-volume entries participate in dedupe with other snapshot-backed entries.
* [ ] Confirm repeated runs do not leak stale size metadata for frequently-mutated cache volumes.

### Phase 3: Integration and polish

* [x] Remove `estimateSharedResultSizeBytes` placeholder function and dead comments.
* [x] Add explicit comments near size-gating logic describing why we only size prune-relevant candidates.
* [x] Add explicit comments near dedupe logic documenting snapshot-record-level accounting choice.
* [x] Keep behavior deterministic across runs (entry ordering + dedupe ownership stable).

## Validation plan

* [ ] Unit tests in `dagql/cache_test.go`:
  * [ ] retained entry size is populated from real sizing path (not constant placeholder).
  * [ ] non-candidate retained entries do not trigger size calculation.
  * [ ] dedupe test: two retained results sharing one snapshot record report one logical byte budget in prune accounting.
  * [ ] mutable cache-volume mutation test: size refreshes after content change.
* [ ] Integration smoke:
  * [ ] `dagger --progress=plain call engine-dev test --pkg ./core/integration --run='TestEngine/TestLocalCache|TestContainer/TestWithMountedCache|TestContainer/TestLoadSaveNone' --count=1`
* [ ] Re-run key cache persistence tests to ensure no regressions from size-path changes.

## Open questions

* [ ] Best ownership model for mapping deduped snapshot bytes onto per-entry display fields when many entries share one snapshot.
  * Keep deterministic and simple in first pass; revisit UX refinements later.
* [ ] Whether to split usage view into:
  * [ ] per-entry logical view
  * [ ] global deduped physical view
  * (not required for first pass, but may help explain accounting to users)
* [ ] Consider measuring all snapshot sizes (including non-prunable) to support accurate engine total disk-usage visibility.
  * Verify first whether this is actually needed and desired as a product/system goal before implementing.

# Disk Persistence

## Design

### Core model

* In-memory dagql cache remains the single source of truth for all cache logic.
* Persistence is asynchronous and event-driven; cache mutations never synchronously write to disk.
* Persistence is represented as export/import of a portable DAG/e-graph shape (not engine-process-local numeric IDs).
* Persisted state is rebuilt into in-memory structures on engine startup before serving requests.

### Runtime architecture

* `PersistenceEmitter` (inside dagql cache mutation paths):
  * observes cache mutations that affect persistable state.
  * emits normalized persistence events after in-memory mutation is committed.
* `PersistenceQueue`:
  * unbounded queue of persistence events (first cut).
  * no drop policy in first cut.
  * feeds a separate persistence worker goroutine.
* `PersistenceWorker` (initially a goroutine, future remote service candidate):
  * drains queue.
  * first cut: no explicit coalescing/compaction optimization.
  * writes durable records to persistent storage.
* `PersistenceStore`:
  * direct insert/upsert/delete/read primitives for persisted cache records.
  * no append-only event log in first cut.
  * initial backend target: SQLite (WAL mode).
  * storage engine can change later without changing in-memory cache behavior.

### Decisions from review

* Graceful shutdown semantics are strict:
  * on SIGTERM/SIGINT (or any graceful shutdown), queue must be fully drained and worker writes fully synced before process exit.
* Ungraceful shutdown handling:
  * startup detects ungraceful previous shutdown.
  * if detected, persistence store is treated as untrusted and deleted/reset; rebuild from empty state.
* Startup import behavior:
  * only full rebuild is supported; no partial/degraded import mode.
* Tombstones:
  * prune/removal paths emit tombstones to persistence store.
* Last-used persistence:
  * do not persist last-used at all in first cut.
* Optional sequence number:
  * allowed if low overhead, not required for correctness in first cut.
* `safeToPersistCache` handling (first cut):
  * enqueue broadly from emitter.
  * apply filtering in worker before durable writes.
  * keep this boundary intentionally open; we may later persist some currently non-persisted categories for import/reconstruction reasons.
* Graceful shutdown wait policy:
  * wait indefinitely for queue drain + worker sync completion.
* Queue/write optimization:
  * skip coalescing/compaction optimizations for now; rely on direct SQLite upsert/delete behavior.
* Ungraceful-shutdown marker choice (tentative implementation direction):
  * store marker in SQLite metadata table (single source of truth, no extra sidecar file).
* Startup import robustness (tentative implementation direction):
  * load via one consistent SQLite read transaction/snapshot and rebuild in-memory from that.
* Persisted scope:
  * persist roots produced by persistable fields.
  * also persist all direct/transitive dependencies required to materialize those roots.
  * function-call result persistence must include anything referenced by the function-call result.
* `sharedResult` persistence must include `self` payload/state (not just graph metadata).
* Persistable type requirement:
  * each persistable type (directory/file/cachevolume/container/function-returnable objects, etc.) must implement serialize/deserialize support.
  * serialization must include enough snapshot metadata to restore mutable/immutable refs on demand (snapshotter/content-store/lease integration shape).
* E-graph rebuild strategy:
  * prefer replay-based rebuild from durable associations + union operations.
  * avoid depending on engine-local numeric IDs as durable keys.
* Tombstones:
  * emit tombstones only for keys known to have been previously persisted.
* Unsafe dependency policy:
  * if a persisted closure includes unsafe deps, persist anyway for now and emit warnings.
* Import failure policy:
  * if import is malformed/corrupt, wipe persistence store and continue startup from empty state.
* `self` persistence interface:
  * use one shared interface for all persistable `self` payloads.
* `self` payload storage format:
  * use opaque payload bytes + type discriminator in first cut.
* Worker write strategy:
  * allow batched writes with sane size/time defaults (tunable), while preserving graceful-shutdown full-drain guarantee.
* `unsafe` marker:
  * store explicit unsafe marker on durable rows (persist anyway + warn policy).
* Explicit terms:
  * persist first-class `terms` rows (do not derive-only).
* Ungraceful marker implementation:
  * strict/simple SQLite metadata row toggle.
* Schema direction:
  * use normalized schema (`results`, `terms`, `term_results`, `deps`, `eq_facts`, `snapshot_refs`, `meta`).
* `self` payload envelope:
  * use `{type, version, payload_bytes}` style envelope for opaque payload persistence.
* Persist reason:
  * tag persisted rows with reason (`root`, `dep_of_root`, `function_ref`) for debugging.
* Startup import ordering:
  * phase order locked as:
    1) `snapshot_refs`
    2) `results` (including `self` payload decode)
    3) replay `eq_facts`
    4) `terms`
    5) `term_results`
    6) `deps`
* `eq_facts` ownership model:
  * each `eq_facts` row is "owned" by one persisted result (`owner_result_key`), meaning that result asserted/required this union fact.
  * the same canonical digest pair may appear under multiple owners; this is intentional for lifecycle isolation.
  * when pruning/deleting a persisted result, tombstone all `eq_facts` rows owned by that result.
  * during import, union from all live `eq_facts`; if one owner is deleted but another still owns the same pair, equivalence remains.
  * this prevents leaks (facts with no surviving owner) while preserving shared equivalence facts still referenced by other persisted roots.

### Proposed durable snapshot/ref metadata (v1, for review)

* Goal: minimal durable fields to restore object `self` payloads that reference mutable/immutable snapshots on demand.
* Proposed durable fields per snapshot-ref record:
  * `ref_key` (stable durable key, not process-local pointer/id)
  * `snapshot_id` (snapshotter key/id used to mount or lookup snapshot)
  * `kind` (`immutable` or `mutable`)
  * `snapshotter_name` (for mount/lookup path correctness)
  * `lease_id` (primary lease tracking key for ref lifecycle restore)
  * `content_blob_digest` (if blob-backed/addressable)
  * `chain_id` (if available; for blob/addressable and import/linking paths)
  * `blob_chain_id` (if available; for addressable chain lookup)
  * `diff_id` (if available)
  * `media_type` (if blob-backed and needed by export/import flows)
  * `blob_size` (optional but useful for export/validation)
  * `urls_json` (optional source URLs for blob-backed refs)
  * `record_type` (for usage/debug parity)
  * `description`
  * `created_at_unix_nano`
  * `size_bytes` (cached size hint; can be recomputed)
  * `deleted` (bool/tombstone state as needed)
* Notes:
  * parent/merge/diff parent graphs are not a durable requirement for first cut if object-level persisted deps already reconstruct materialization closure.
  * equal-mutable/equal-immutable compatibility metadata should not be persisted.

### Event and record shape (first sketch)

* Persistence events should be idempotent and replay-safe.
* Suggested logical event categories:
  * upsert result record (identity + metadata + payload linkage)
  * upsert term association (term <-> result links)
  * upsert dependency edge (result -> dependency result)
  * union/merge equivalence classes
  * touch/update usage metadata (created/expires/safe-to-persist/unsafe marker)
  * delete/tombstone when prune removes persisted state
* Records should use stable recipe-based identities:
  * call IDs/digests
  * term digests and input eq class references
  * output digest/extra digest mappings
  * dependency edges for transitive retention reconstruction
* For first cut, this event model is transport-only between cache and worker.
  * durable representation in SQLite is direct upsert/delete tables, not event-log replay files.

### Startup import flow

* On engine startup:
  * read persisted records from `PersistenceStore`.
  * replay/import records to rebuild in-memory dagql cache/e-graph state.
  * restore persisted result flags and dependency graph needed for prune/retention behavior.
* Import should be deterministic and idempotent:
  * replaying the same records multiple times must produce equivalent in-memory state.
* Engine should not start serving until import has completed.
* If ungraceful-shutdown marker is detected, clear persistence store first, then boot with empty cache.

### Consistency and failure semantics

* Cache mutation happens first; persistence is eventual.
* Crash window exists between in-memory mutation and durable write unless queue is drained.
* First-cut accepted behavior:
  * persisted state can lag in-memory state during runtime.
  * ungraceful crash invalidates trust in persisted cache; startup wipes and restarts persistence from empty.
* Worker writes must be retryable; transient store failures should not corrupt in-memory cache.

### Integration boundaries

* Persistability gates:
  * only persist results/edges allowed by persistable field policy.
  * integrate with `safeToPersistCache` before full rollout.
* Prune interaction:
  * prune decisions still run on in-memory state.
  * prune must emit delete/tombstone persistence updates for persisted records it removes.
* Additional output results:
  * attachment/dependency edges must be persisted so startup import rebuilds identical dependency closure.

### Non-goals for first implementation

* No synchronous write-through in resolver hot paths.
* No remote persistence service yet (goroutine-only worker is enough initially).
* No read-through fallback from store during request execution; startup import is the restore mechanism.
* No page-out/paging policy work in this phase; focus is durable reconstruction of persisted cache graph.

### Open questions (next decisions)

* Optional sequence number:
  * include global monotonic `seq` column on persistence writes now, or defer entirely?
* Tombstone row keys:
  * finalize exact row-key strategy once table schema is defined (same durable identity basis used by upsert/delete).
* Durability sync point:
  * on graceful shutdown, do we require explicit WAL checkpoint + sync after drain, or rely on committed transactions only?
  * current direction: rely on committed transactions only (no forced checkpoint requirement).
* Sequence number usage:
  * if we add `seq`, is it debug-only ordering metadata or does import/replay logic depend on it?

### Concrete SQLite schema proposal (v1 draft for review)

* `meta`:
  * `key TEXT PRIMARY KEY`
  * `value TEXT NOT NULL`
  * required keys:
    * `schema_version`
    * `clean_shutdown` (`"0"`/`"1"`)
* `results`:
  * `result_key TEXT PRIMARY KEY` (stable durable key for shared result)
  * `id_digest TEXT NOT NULL`
  * `output_digest TEXT`
  * `output_extra_digests_json TEXT NOT NULL DEFAULT '[]'`
  * `output_effect_ids_json TEXT NOT NULL DEFAULT '[]'`
  * `self_type TEXT NOT NULL`
  * `self_version INTEGER NOT NULL`
  * `self_payload BLOB NOT NULL`
  * `dep_of_persisted_result INTEGER NOT NULL DEFAULT 0`
  * `safe_to_persist_cache INTEGER NOT NULL DEFAULT 1`
  * `unsafe_marker INTEGER NOT NULL DEFAULT 0`
  * `persist_reason TEXT NOT NULL` (`root`/`dep_of_root`/`function_ref`)
  * `created_at_unix_nano INTEGER NOT NULL`
  * `expires_at_unix INTEGER NOT NULL DEFAULT 0`
  * `record_type TEXT NOT NULL DEFAULT ''`
  * `description TEXT NOT NULL DEFAULT ''`
  * `deleted INTEGER NOT NULL DEFAULT 0`
  * `deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0`
  * indexes:
    * `idx_results_output_digest(output_digest)`
    * `idx_results_deleted(deleted)`
* `terms`:
  * `term_digest TEXT PRIMARY KEY`
  * `self_digest TEXT NOT NULL`
  * `input_digests_json TEXT NOT NULL` (stable digest list, no engine-local IDs)
  * `created_at_unix_nano INTEGER NOT NULL`
  * `deleted INTEGER NOT NULL DEFAULT 0`
  * `deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0`
* `term_results`:
  * `term_digest TEXT NOT NULL`
  * `result_key TEXT NOT NULL`
  * `deleted INTEGER NOT NULL DEFAULT 0`
  * `deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0`
  * `PRIMARY KEY(term_digest, result_key)`
  * foreign keys:
    * `term_digest -> terms.term_digest`
    * `result_key -> results.result_key`
* `deps`:
  * `parent_result_key TEXT NOT NULL`
  * `dep_result_key TEXT NOT NULL`
  * `deleted INTEGER NOT NULL DEFAULT 0`
  * `deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0`
  * `PRIMARY KEY(parent_result_key, dep_result_key)`
  * foreign keys:
    * `parent_result_key -> results.result_key`
    * `dep_result_key -> results.result_key`
* `eq_facts`:
  * `owner_result_key TEXT NOT NULL` (result that "owns" this equivalence fact for tombstone cleanup)
  * `lhs_digest TEXT NOT NULL`
  * `rhs_digest TEXT NOT NULL`
  * `created_at_unix_nano INTEGER NOT NULL`
  * `deleted INTEGER NOT NULL DEFAULT 0`
  * `deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0`
  * `PRIMARY KEY(owner_result_key, lhs_digest, rhs_digest)`
  * foreign keys:
    * `owner_result_key -> results.result_key`
  * invariant:
    * store canonical digest pair ordering (`lhs_digest <= rhs_digest`) for deterministic dedupe.
* `snapshot_refs`:
  * `ref_key TEXT PRIMARY KEY` (global unique)
  * `kind TEXT NOT NULL` (`immutable`/`mutable`)
  * `snapshot_id TEXT NOT NULL`
  * `snapshotter_name TEXT NOT NULL`
  * `lease_id TEXT NOT NULL`
  * `content_blob_digest TEXT NOT NULL DEFAULT ''`
  * `chain_id TEXT NOT NULL DEFAULT ''`
  * `blob_chain_id TEXT NOT NULL DEFAULT ''`
  * `diff_id TEXT NOT NULL DEFAULT ''`
  * `media_type TEXT NOT NULL DEFAULT ''`
  * `blob_size INTEGER NOT NULL DEFAULT 0`
  * `urls_json TEXT NOT NULL DEFAULT '[]'`
  * `record_type TEXT NOT NULL DEFAULT ''`
  * `description TEXT NOT NULL DEFAULT ''`
  * `created_at_unix_nano INTEGER NOT NULL`
  * `size_bytes INTEGER NOT NULL DEFAULT 0`
  * `deleted INTEGER NOT NULL DEFAULT 0`
  * `deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0`
* `result_snapshot_refs` (generic non-opaque linkage for shared result -> snapshot refs):
  * `result_key TEXT NOT NULL`
  * `ref_key TEXT NOT NULL`
  * `role TEXT NOT NULL` (type-agnostic linkage role name)
  * `slot TEXT NOT NULL DEFAULT ''` (path/name/index as needed)
  * `deleted INTEGER NOT NULL DEFAULT 0`
  * `deleted_at_unix_nano INTEGER NOT NULL DEFAULT 0`
  * `PRIMARY KEY(result_key, ref_key, role, slot)`
  * foreign keys:
    * `result_key -> results.result_key`
    * `ref_key -> snapshot_refs.ref_key`

### Concrete import mapping (v1)

* Import transaction reads from SQLite snapshot.
* Rebuild order:
  * load `snapshot_refs` into ref-lookup map.
  * load `results`, decode `self_payload`, attach `result_snapshot_refs` links.
  * replay all live `eq_facts` rows (`union(lhs_digest, rhs_digest)`).
  * load `terms`.
  * load `term_results`.
  * load `deps`.
* Rows with `deleted=1` are skipped as live state and only used for idempotent replay semantics.

### Detailed behavior specs (locked for implementation)

#### 1) `eq_facts` extraction and durability rules

* Purpose:
  * `eq_facts` is the durable summary of output-equivalence merges needed to reconstruct e-graph union state on startup.
* Source of truth during runtime:
  * runtime e-graph (`egraphDigestToClass` + union-find parents/ranks) remains authoritative while engine is live.
  * `eq_facts` is export state derived from runtime cache state, not a parallel runtime authority.
* Fact shape:
  * each row is `(owner_result_key, lhs_digest, rhs_digest)` with canonical digest ordering (`lhs <= rhs`).
  * a row means: "for this owner result closure, these two digests must be unioned at import."
* What must be included when exporting a persisted root closure:
  * digest equalities induced by request identity and output identity for closure results:
    * request recipe digest
    * request extra digests
    * output digest
    * output extra digests
  * this mirrors the runtime merge set currently formed by `indexWaitResultInEgraphLocked`.
* Owner semantics:
  * owner is lifecycle ownership only (not canonicality).
  * same `(lhs,rhs)` may appear for multiple owners; this is expected.
  * pruning/deleting one owner tombstones only that owner's rows.
  * import unions from all live rows; equivalence survives as long as at least one owner still asserts it.
* Determinism:
  * extraction sorts digest pairs and emits stable row ordering before enqueueing.

#### 2) Persisted root + transitive closure rules

* Persisted root trigger:
  * a completed call whose field is persistable (or a persistable lookup hit that upgrades an existing shared result) marks its shared result as persisted-root-relevant.
* Closure rule (required):
  * durable export for each persisted root must include all direct/transitive dependencies required to materialize that root.
  * this includes dependencies attached via:
    * term input dependency linkage (`indexResultDependenciesLocked` behavior)
    * explicit additional output attachment edges (`HasAdditionalOutputResults`).
* Function-result closure rule (required):
  * if function result references objects/results, those referenced results must be included in closure export.
* Unsafe policy:
  * unsafe rows are still exported in first cut (with marker + warning), not dropped.
* Tombstone scope:
  * emit tombstones only for rows that previously existed durably for that owner/result.
* Practical consequence for host-directory scenario:
  * client-specific host load call itself is expected miss on new client.
  * after host load executes and attaches output digest facts + unions, downstream container ops can hit via reconstructed/persisted equivalence state.

#### 3) Startup reconstruction obligations (what must be rebuilt exactly)

* Import must rebuild not just object payloads, but all lookup/index structures needed for cache hits.
* Required in-memory reconstruction outputs:
  * `resultsByID`
  * `egraphTerms` and `egraphTermsByDigest`
  * `egraphResultsByTermID` and `egraphTermIDsByResult`
  * `egraphResultsByOutputDigest`
  * union-find equivalence state (`egraphDigestToClass`, `egraphParents`, `egraphRanks`) from `eq_facts`
  * dependency graph (`sharedResult.deps`) for persisted-retention and prune-liveness semantics
* Strict consistency checks at import:
  * every `term_results` row references existing live term + result
  * every `deps` row references existing live result endpoints
  * every `result_snapshot_refs` row references existing live result + snapshot ref
  * malformed `self_payload` decode is import-fatal
* Failure policy:
  * any structural inconsistency or decode failure => wipe persistence store and continue cold-start.
* Why this matters:
  * if any of the above indexes are not rebuilt, runtime may boot with payloads present but miss cache lookups due to missing e-graph/search structures.

## Implementation

### Phase 0: Lock contracts, keys, and ownership semantics

* [x] Finalize persistence key derivation and invariants.
  * [x] `result_key`: stable digest derived from `sharedResult.originalRequestID`.
  * [x] `term_digest`: existing canonical term digest (`selfDigest + canonicalized input eq IDs`).
  * [x] `eq_facts`: canonical pair ordering (`lhs <= rhs`) and owner-scoped lifecycle via `owner_result_key`.
  * [x] `ref_key`: global unique durable snapshot-ref key.
* [x] Finalize hard invariants to enforce in code.
  * [x] Persisted graph closure rule: persisted root implies persisted transitive deps.
  * [x] Function-result closure rule: any result referenced by function outputs must also be persisted.
  * [x] Tombstone rule: only rows previously persisted can be tombstoned.
  * [x] Import-failure rule: malformed/inconsistent import state triggers wipe-and-continue.
* [x] Add implementation notes in code comments near the emitter/worker boundary.
  * [x] Worker applies summarized state updates (upsert/tombstone), not event-log replay semantics.
  * [x] `eq_facts` ownership semantics and duplicate pair across owners are intentional.

### Phase 1: SQLite store bootstrap and lifecycle metadata

* [x] Add persistence store package and schema migration wiring (SQLite WAL).
  * [x] Follow existing `dagql/db` pattern for schema + prepared query interface shape (sqlc-style generated query wrapper usage pattern).
  * [x] Mirror cache bootstrap pattern already used in `dagql.NewCache`:
    * [x] open DB with `modernc.org/sqlite`
    * [x] apply schema
    * [x] prepare query interface
    * [x] wire failure handling/close paths consistently.
* [x] Implement metadata lifecycle.
  * [x] On process startup-before-serving: set `clean_shutdown=0`.
  * [x] On graceful shutdown completion (after full queue drain + worker sync): set `clean_shutdown=1` (current implementation marks this on dagql cache loop shutdown).
  * [x] On startup, if `clean_shutdown != 1` (or unreadable/corrupt): wipe DB and reinitialize schema.
* [x] Implement direct upsert/tombstone primitives for all normalized tables.
  * [x] `results`, `terms`, `term_results`, `deps`, `eq_facts`, `snapshot_refs`, `result_snapshot_refs`, `meta`.
* [x] Keep write path dumb and idempotent.
  * [x] no coalescing optimization in first pass.
  * [x] rely on batched upserts/deletes with ON CONFLICT update semantics.
* [x] Hard cutover cleanup: removed legacy TTL-only `dagql/db` persistence package and all cache usage of it.

### Phase 2: Persistable self serialization + snapshot-ref linkage

* [x] Introduce one shared persistence interface for persistable `self` payloads.
  * [x] Interface emits opaque payload envelope fields:
    * [x] `self_type`
    * [x] `self_version`
    * [x] `self_payload`
  * [x] Interface also emits generic snapshot-ref links:
    * [x] one or more `result_snapshot_refs` rows with `(ref_key, role, slot)`.
* [ ] Implement serializers/deserializers for currently persistable core object types.
  * [x] Directory (object-ID envelope + snapshot-link provider hook)
  * [x] File (object-ID envelope + snapshot-link provider hook)
  * [ ] Container (pending explicit snapshot-link provider decision; likely no direct links, deps-only)
  * [x] CacheVolume (object-ID envelope + snapshot-link provider hook)
* [x] Expand persistence surface to full SDK-return model (not only a small set of core object types).
  * [x] Audit `ConvertFromSDKResult` pathways and lock supported durable shapes:
    * [x] scalars
    * [x] core object IDs
    * [x] user module object IDs
    * [x] lists
    * [x] nested combinations of the above
  * [x] Define how non-object/scalar SDK values map into persisted result payload rows so function result persistence is complete.
  * [x] Add explicit validation/failure behavior for unsupported SDK result shapes (fail clearly; no silent partial persistence).
* [ ] Snapshot-ref metadata integration:
  * [ ] emit `snapshot_refs` rows for all linked refs referenced by persisted results.
  * [ ] include required mutable/immutable metadata fields from current ref model.
  * [x] do not persist internal parent/equal-ref compatibility graphs.

### Phase 3: Emitter + queue + worker plumbing

* [x] Add `PersistenceEmitter` in dagql cache mutation paths.
  * [x] Trigger on persisted-root creation/update and persisted-root deletion/prune.
  * [x] Trigger when result closure changes (deps/term associations/eq facts/snapshot links).
* [x] Build closure export for a persisted root:
  * [x] collect root result + all transitive dependency results needed for materialization.
  * [x] collect terms associated with collected results.
  * [x] collect `term_results` links for those terms/results.
  * [x] collect dependency edges (`deps`) among collected results.
  * [x] collect owner-scoped `eq_facts` rows required to rebuild equivalence for collected closure.
  * [x] collect `snapshot_refs` + `result_snapshot_refs` for all collected results.
  * [ ] follow-up: replace placeholder snapshot-ref metadata rows with full ref metadata extraction.
* [x] Add unbounded in-process queue + worker goroutine.
  * [x] queue item shape carries normalized table rows (state-upsert payload), not raw low-level mutation internals.
  * [x] worker-side filtering for `safeToPersistCache` policy in first cut.
* [x] Add worker batching:
  * [x] size-based batch threshold.
  * [x] time-based flush threshold.
  * [x] explicit synchronous flush API for shutdown.

### Phase 4: Worker apply logic (upsert/tombstone state writes)

* [x] Implement transactional apply per batch:
  * [x] write `snapshot_refs` first.
  * [x] write `results`.
  * [x] write `result_snapshot_refs`.
  * [x] write `eq_facts`.
  * [x] write `terms`.
  * [x] write `term_results`.
  * [x] write `deps`.
* [x] Implement tombstone propagation for prune/delete:
  * [x] result tombstones.
  * [x] owner-scoped `eq_facts` tombstones.
  * [x] relation-row tombstones (`deps`, `term_results`, `result_snapshot_refs`) where appropriate.
  * [ ] follow-up: consider snapshot_ref tombstone coverage once cross-result ref sharing policy is finalized.
* [x] Keep store semantics idempotent:
  * [x] repeated upsert/tombstone events must converge to same durable state.
  * [x] no dependency on event arrival uniqueness.

### Phase 5: Startup import and in-memory reconstruction

* [x] Add startup import gate before server starts serving requests.
* [x] Implement import order (locked):
  * [x] load `snapshot_refs` map.
  * [x] load + decode `results` and rehydrate sharedResult records (eager when decodable, lazy envelope fallback otherwise).
  * [x] attach `result_snapshot_refs` links.
  * [x] replay live `eq_facts` unions.
  * [x] load `terms`.
  * [x] load `term_results`.
  * [x] load `deps`.
* [x] Rebuild all required in-memory indexes:
  * [x] term digest indexes.
  * [x] result<->term association maps.
  * [x] output-digest reverse index.
  * [x] dependency graph used for dep-of-persisted retention and prune liveness.
* [x] Validate imported state:
  * [x] missing FK targets / malformed payload / digest inconsistencies => fail import path and wipe store.
  * [ ] follow-up: promote currently debug-log-only import stats into metrics.

### Phase 6: Graceful shutdown and failure handling

* [x] Integrate shutdown flow:
  * [x] stop new queue ingestion.
  * [x] drain queue fully.
  * [x] force worker flush and wait indefinitely for completion.
  * [x] set `clean_shutdown=1` only after successful flush+commit.
* [x] Crash semantics:
  * [x] if process exits ungracefully, next startup sees `clean_shutdown!=1`, wipes persistence DB, cold starts.
* [x] Observability:
  * [x] queue depth (debug logs).
  * [x] worker batch size / apply latency (debug logs).
  * [x] import duration and row counts per table (info log on import completion).
  * [ ] wipe-on-unclean-start counter metric (warning logs exist; metric follow-up).

### Phase 7: Validation matrix and rollout sequence

* [x] Unit tests for store + worker behavior:
  * [x] idempotent upsert/tombstone.
  * [x] owner-scoped `eq_facts` tombstone semantics.
  * [x] clean-shutdown toggle behavior.
* [x] Dagql cache persistence tests:
  * [x] persisted root survives session close and process restart.
  * [x] persisted function result restores all referenced outputs/deps.
  * [x] prune emits tombstones and import no longer restores pruned entries.
* [x] Startup robustness tests:
  * [x] unclean shutdown marker causes wipe.
  * [x] malformed/corrupt durable rows cause wipe-and-continue.
* [x] Integration tests (targeted, primary focus in this phase):
  * [x] Add major new integration coverage under `TestEngine` for disk persistence behavior.
  * [x] Add a restart harness for engine-as-a-service lifecycle tests:
    * [x] start engine service container
    * [x] run phase-A workload
    * [x] stop service container
    * [x] restart service container (same persistence state)
    * [x] run phase-B workload and assert cross-restart cache reuse behavior
  * [x] Validate local cache behavior across restart.
  * [x] Validate function cache control behavior across restart.
  * [x] Validate cache volume + container/file/directory closure restoration across restart.
  * [x] Include at least one complex scenario matching the container+host-mount+withExec chain discussed in design review.
  * [x] Guard imported unresolved object/scalar envelopes from recursive decode in no-server contexts by treating those hits as misses.

### Phase 8: Manual workflow debugging for engine-dev / module-load cache behavior

#### Problem statement

The remaining priority is the manual `engine-dev container with-exec --args true stdout` workflow, not just the focused restart tests we added.

Manual symptoms reported for this workflow:
* Fresh dev engine in Docker with empty cache:
  * `docker rm -fv dagger-engine.dev; docker volume rm dagger-engine.dev; ./hack/dev`
* First build through the dev engine:
  * `./hack/with-dev dagger --progress=plain call engine-dev container with-exec --args true stdout`
  * expected cold build
* Immediate reruns are inconsistent:
  * sometimes the `load module` / SDK calls are cached, sometimes not
  * sometimes the engine build calls are cached, sometimes not
  * more reruns often improve cache-hit behavior, but not deterministically
* After graceful-ish restart (`docker stop --signal SIGTERM dagger-engine.dev`, then `docker start dagger-engine.dev`), the workflow can lose all cache on boot with import failure of the form:
  * `level=WARN msg="dagql persistence import failed; wiping and cold-starting" err="import term_result: missing result \"xxh3:...\""`
* Once that happens, there are obviously no cache hits on the next run.
* Modified-source rebuild variant:
  * same workflow as above, but after warm cache is established, make a no-op source edit (for example a comment change in `./core/schema/git.go`) and rebuild
  * one observed failure mode was a late export/tarball error:
    * `Container.asTarball(forcedCompression: Zstd): File! ERROR`
    * `container image to tarball file conversion failed: failed to export: lease "sha256:001e3e5fa34066ae93b36cb6486cf9c25d67699c6ab2bc24528feb0e09ce7303": not found`
  * the most recent retry did not reproduce that exact export error; instead it hung later in module/toolchain execution after successful `Container.asTarball` calls

Current priority order:
1. Cache must survive engine restart for the `engine-dev container with-exec --args true stdout` workflow.
2. After restart, the engine-build path itself should be as warm as expected.
3. Warm rebuilds after a no-op source edit must not fail late in `asTarball` / export with missing leases.

Important debugging constraint:
* Progress output is only a heuristic.
* For now, the engine's own cache logic and its logs are the source of truth.
* `docker logs dagger-engine.dev` should be treated as the primary evidence stream for this phase.

#### Plan / checklist

* [x] Reproduce the manual `./hack/dev` + `./hack/with-dev dagger ... engine-dev ...` workflow exactly on current HEAD and capture:
  * [x] first run
  * [x] second immediate rerun
  * [x] third rerun for convergence behavior
  * [x] post-SIGTERM restart run (captured far enough to confirm import wipe + colder behavior)
* [x] Capture and summarize engine logs from `docker logs dagger-engine.dev` around each run boundary.
* [~] Diagnose any remaining import failure of the form `import term_result: missing result "xxh3:..."`.
  * [x] Reproduced on current HEAD in manual restart workflow.
  * [x] Observed the same digest in the pre-restart persistence worker tombstone batch and the post-restart import failure.
  * [x] Determined that background dagql prune is selecting persisted closure members and emitting tombstones for them.
  * [x] Confirmed the prune shield fixes the manual restart wipe in the `engine-dev container with-exec --args true stdout` workflow.
* [~] Diagnose inconsistent rerun cache behavior for:
  * [x] module-load / SDK calls: in the latest clean manual run, run #2 dropped from `load module: . DONE [2m16s]` to `load module: . DONE [3.9s]`.
  * [x] engine build calls: in the latest clean manual run, run #2 had `EngineDev.container CACHED [0.0s]`.
  * [x] The latest clean manual run did not show the earlier “needs a third run to converge” behavior.
* [ ] If progress output is too ambiguous in future manual regressions, add targeted engine log augmentation focused on:
  * [ ] cache hit/miss decisions for the relevant call IDs
  * [ ] persistence import of terms/results/term_results for the affected digests
  * [ ] module-load caching path and engine-dev build call identities
  * [ ] `Container.asTarball` / exporter lease usage for warm rebuilds after source edits
  * [ ] module/toolchain exec lifecycle (`ModuleFunction.Call` / `Container.WithExec` / `runContainer`) for the current changelog hang
* [ ] Add integration coverage once the manual failure signatures are understood well enough to encode deterministically.

#### Progress log

* [x] Added targeted restart coverage under `TestEngine/TestDiskPersistenceAcrossRestart` for:
  * [x] local cache survives restart
  * [x] function cache control survives restart
  * [x] container withExec output on host mount survives restart
  * [x] cache volume survives restart
* [x] Diagnosed and fixed persisted object import recursion by hard-cutting object persistence over from `object_id -> dagql Load` to real object self payloads.
* [x] Diagnosed and fixed restart failures caused by persisting snapshot-manager metadata IDs instead of stable snapshotter IDs.
* [x] Diagnosed and fixed restart failures caused by persisting lazy directory/file refs before their snapshots were materialized.
* [x] Diagnosed and fixed restart failures caused by persisted container payloads referencing mounted source objects that were not included in the persisted closure.
* [x] Verified current focused coverage passes:
  * [x] `go test ./dagql -run 'TestPersistedSelfCodec|TestCachePersistenceImport|TestCachePersistenceWorker|TestCachePersistenceCleanShutdownToggleOnClose|TestCachePersistenceWorkerEqFactTombstoneScopedByOwner|TestCachePersistenceWorkerIdempotentUpsertAndTombstone' -count=1`
  * [x] `dagger --progress=plain call engine-dev test --pkg ./core/integration --run='TestEngine/TestDiskPersistenceAcrossRestart'`
  * [x] `dagger --progress=plain call engine-dev test --pkg ./core/integration --run='TestModule/TestFunctionCacheControl'`
* [x] Re-ran the manual workflow after the persistence fix:
  * [x] Cold run: `EngineDev.container DONE [2m56s]`; target tail `Container.withExec DONE [0.7s]`.
  * [x] Second run: `load module: . DONE [37.1s]`; `EngineDev.container DONE [21.0s]`; target tail `Container.withExec DONE [0.5s]`.
  * [x] Third run: `load module: . DONE [13.9s]`; `EngineDev.container CACHED [0.0s]`; target tail `Container.withExec CACHED [0.0s]`.
  * [x] Post-SIGTERM restart: engine logs show `dagql persistence import failed; wiping and cold-starting` with `import term_result: missing result "xxh3:f4ccac0d8d902d05"`.
  * [x] Same digest also appears immediately before restart in a persistence worker tombstone batch:
    * `rootResultKey=xxh3:f4ccac0d8d902d05 resultTombstones=1 termResultTombstones=1 depTombstones=2`
  * [x] Same window also shows background `dagql prune selected candidate` activity immediately before those tombstone batches.
  * [x] Current diagnosis for Problem 1:
    * automatic dagql prune is selecting `depOfPersistedResult` entries and tombstoning them individually
    * restart import can then see surviving durable relations (`term_results`) that still point at a result digest that was tombstoned by prune
  * [x] Added a first shield in code: automatic dagql prune now skips `depOfPersistedResult` entries instead of pruning and tombstoning them.
  * [x] Post-restart run therefore regressed to a much colder path:
    * `load module: . DONE [1m24s]`
  * [x] Current high-confidence read:
    * same-process reruns do converge, but not immediately
    * restart still wipes/cold-starts for this manual workflow because import sees a `term_result` row referencing a missing/tombstoned result digest
* [x] Re-ran the full manual workflow on the current post-fix state:
  * [x] Clean run #1 succeeded:
    * `load module: . DONE [2m16s]`
    * `EngineDev.container DONE [2m53s]`
    * target tail `Container.withExec DONE [0.7s]`
  * [x] Clean run #2 was strongly warm:
    * `load module: . DONE [3.9s]`
    * `EngineDev.container CACHED [0.0s]`
    * target tail `Container.withExec CACHED [0.0s]`
  * [x] The old restart wipe signature is no longer present:
    * did not observe `dagql persistence import failed; wiping and cold-starting`
    * did not observe `import term_result: missing result`
  * [x] Follow-up regressions after the prune shield were identified and fixed:
    * `FunctionCallArgValue` object-wrap failures from eager `cache.wait` reconstruction of typed object results
    * mounted `ModuleSource.contextDirectory` child object decode falling over on `missing persisted result key`
    * eager mutable `CacheVolume` snapshot reopen hitting `... is locked: locked`
  * [x] Current exact continuation workflow from the same running `dagger-engine.dev` succeeds:
    * run #2:
      * `daggerDev CACHED [0.0s]`
      * `DaggerDev.engineDev CACHED [0.0s]`
      * `EngineDev.container CACHED [0.0s]`
      * `Container.withExec CACHED [0.0s]`
    * post-restart run:
      * `load module: . DONE [11.1s]`
      * `DaggerDev.engineDev DONE [5.1s]`
      * `EngineDev.container CACHED [0.0s]`
      * `Container.withExec CACHED [0.0s]`
  * [x] In the latest post-restart logs, none of the old blocker signatures appear:
    * no `dagql persistence import failed; wiping and cold-starting`
    * no `import term_result: missing result`
    * no `missing persisted result key`
    * no mutable-snapshot `locked` errors
  * [ ] Follow-up to revisit later:
    * post-restart `load module` and `DaggerDev.engineDev` still do real work instead of showing as cached, even though the command now succeeds end-to-end
* [x] New manual regression identified in the modified-source rebuild workflow:
  * [x] After a no-op source edit and warm rebuild, the workflow can fail late in `Container.asTarball(forcedCompression: Zstd)` with:
    * `failed to export: lease "sha256:001e3e5fa34066ae93b36cb6486cf9c25d67699c6ab2bc24528feb0e09ce7303": not found`
  * [x] Initial code read points at exporter/lease handling rather than the earlier dagql import/tombstone issues:
    * `core/container.go` `AsTarball`
    * `engine/buildkit/exporter/oci/export.go`
    * snapshot/content lease interactions around the stable base snapshot/content chain
  * [x] Retried the modified-source rebuild flow from a clean state and did **not** reproduce the exact `lease ... not found` exporter failure.
  * [x] On that retry, `Container.asTarball` completed successfully for the relevant digests, and the run later hung instead.
  * [x] `SIGQUIT` dump from that retry shows the strongest blocking chain is:
    * `moduleSourceSchema.loadDependencyModules`
    * `dagql.cache.wait`
    * `ModuleFunction.Call`
    * `Container.WithExec`
    * `buildkit.Worker.runContainer`
    * `go-runc monitor Wait`
  * [x] Current high-confidence read for the modified-source workflow:
    * the earlier exporter lease-not-found issue is at least intermittent and did not reproduce on the latest retry
    * the current concrete blocker is a later hang in the `changelog` toolchain exec path
    * the wait chain is process/runtime lifecycle (`runContainer` / `Runc.Run` / monitor `Wait`), not an e-graph or dagql mutex deadlock
  * [x] Subsequent exact repro of the original modified-source workflow now succeeds on the current tree:
    * run #1 succeeded
    * run #2 was strongly warm (`daggerDev`, `DaggerDev.engineDev`, `EngineDev.container`, and `Container.withExec` all cached)
    * post-restart run succeeded (still not fully warm in `load module` / `DaggerDev.engineDev`)
    * modified-source run after the temporary `core/schema/git.go` comment change also succeeded
    * did **not** reproduce:
      * `failed to export: lease "sha256:001e3e5fa34066ae93b36cb6486cf9c25d67699c6ab2bc24528feb0e09ce7303": not found`
      * `import term_result: missing result`
      * `missing persisted result key`
      * mutable-snapshot `locked` errors
  * [x] Current best read:
    * the original modified-source `asTarball` lease failure is fixed on the current tree
    * the remaining cache-quality gap is still that post-restart `load module` / `DaggerDev.engineDev` do work instead of being fully warm
* [x] Restart cache-quality follow-up on the remaining `load module` / `DaggerDev.engineDev` gap:
  * [x] Added targeted `phase8-cache` tracing in `dagql/cache.go` for the relevant fields.
  * [x] Exact cache-trace repro confirms the post-restart misses are not lookup failures on previously seen digests.
  * [x] Both `moduleRuntime` and `engineDev` are requested under brand-new digests after restart:
    * `moduleRuntime`: 16 unique post-restart miss digests, none seen earlier in the same log
    * `engineDev`: 1 post-restart miss digest, also not seen earlier
  * [x] Current high-confidence read:
    * this remaining gap is an identity/input-drift problem across reboot, not a persisted-hit decode/import miss on a stable request digest
    * next empirical step is to log the derived self/input digests plus receiver/module/arg ID digests for those calls and identify which input changes first across restart
* [x] Follow-up trace with receiver/module/arg digest breakdown isolates two distinct remaining causes:
  * [x] `moduleRuntime` is still a real post-restart cache-key miss:
    * all post-restart `moduleRuntime` lines are `lookup-miss`
    * `selfDigest` and `moduleContentDigest` stay stable
    * the moving inputs are the `receiverDigest` (`Query.dangSdk`) and `modSource` arg recipe digest
    * `modSource` currently shows no direct `content:` digest in the traced arg payload
  * [x] `DaggerDev.engineDev` is **not** a key miss anymore:
    * post-restart it reaches `lookup-hit`
    * then degrades to `hit-fallback-miss` with `persisted hit payload not decodable in current context`
    * this isolates `engineDev` as an imported object decode problem, separate from the `moduleRuntime` cache-key drift
  * [x] `Query.dangSdk` receiver digests correspond to persisted module-object constructor calls, which strongly suggests dynamic module objects still need proper content addressing / persistence decode support rather than more build-level debugging
* [x] User manual validation on the latest tree supersedes the older automated traces for restart behavior:
  * [x] `load module` is now looking cached as expected after engine restart in the user's latest manual run
  * [x] The remaining practical gap is narrower: the engine build itself is still not quite as cached as expected after restart
  * [ ] Next debugging focus should therefore shift from broad module-load restart misses to the specific post-restart engine-build path that is still colder than expected
* [x] Follow-up engine-build-focused traces after the user report:
  * [x] `daggerDev` and `DaggerDev.engineDev` are now hot in the automated restart repros
  * [x] `EngineDev.container` is still not fully warm after restart
  * [x] Hardening steps already in place that should be kept:
    * dynamic `ModuleObject` results now carry content digests and decode from persistence
    * SDK-scoped `ModuleSource` results now carry stable content digests
    * contextual directory/file args now get explicit content digests
    * container-derived directory/file outputs now get explicit content digests
  * [x] Latest automated read:
    * the remaining post-restart cold path is no longer just `load module`
    * `EngineDev.container` still does real work even when upstream module/constructor steps are cached
    * some final assembly `withDirectory` sources are now properly content-addressed, but the remaining misses are more mixed than before and now include upstream object/file/module edges again

### Execution order (first implementation pass)

* [ ] Execute in this strict order:
  * [ ] Phase 0
  * [ ] Phase 1
  * [ ] Phase 2
  * [ ] Phase 3
  * [ ] Phase 4
  * [ ] Phase 5
  * [ ] Phase 6
  * [x] Phase 7
* [ ] Commit at the end of each phase with detailed message including:
  * [ ] problem addressed
  * [ ] chosen design and tradeoffs
  * [ ] implementation details and constraints
