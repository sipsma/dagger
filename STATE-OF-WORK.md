# STATE OF WORK — service phase (manager/designer seat), at the pause

Written 2026-07-07 by service-phase-designer-84d84a98 on receipt of the pause directive.
This is the phase-level record; each implementer worktree carries its own STATE-OF-WORK.md.

## What is COMPLETE

- **The design**: `hack/designs/remote-cache/service/design.html` v4+, approved (3 Codex
  rounds + cache-chief), review log through round 16 records every as-built adjudication.
- **Chunk A (bundle & origins) — LANDED** on the take-3 integration branch: tip
  `df3e1d6b39` (merge `6146c7e31a`), schema 18→19. Origin identity + allocator high-water,
  portable-ref contract (4 locations, `$dagqlResultRef`/`$dagqlCallID` tokens), bundle
  writer/reader over streams, forward-closure export filter, bundle-flavored vetting +
  origin dedup with identity-evidence union, wipe-isolation. 3 review rounds → READY;
  post-merge insurance green.
- **Chunk B (chains + content-chain source) — LANDED**: integration tip `07e050e233`
  (merge `93135b3b0b`). Chain export (zstd pinned, hash-at-export, memoized), the
  content-chain walk arm (§9.3 failure typing, counters, ingest-abort),
  chains-validate-before-vetting (blast-radius split), N1 local-vetting chain fallback,
  mutable-owner contentless decoders, the reset-r24 starved tie-break (ratified), a
  pre-existing isPersistable race fix. **First cross-engine warm proof achieved** (T-S4
  via file transport: fresh engine reproduced another engine's random-marker output;
  `served_from_content_chain ≥ 1`, zero demotes; T-S6 sparse-heal; T-S8 singleflight).
  3 review rounds → READY; landing insurance green.
- **Chunk D (service protocol/storage + production mounting) — review-converged, NOT
  merged** (dagger.io branch `svc-chunk-d-implementer-207ba9ac` @ `bc5ec0cad`, 12
  commits): /v1 endpoints, blobstore backends (S3/Namespace/filesystem), signed upload
  tokens, staging+promote with advisory-lock completion, verified-blob inventory,
  sanitized typed event log, production mounting behind `enable_cache_service`.
  5 review rounds → READY both slices.
- **Chunk F (insight) — complete on its branch** (`svc-chunk-f-implementer-8ff64dab`,
  pushed): inventory + event APIs, version-degrading read-only indexer, HTML dashboard.
  Completed its own pause protocol; not reviewed (pause landed before its review round).

## What was MID-FLIGHT at the pause

- **Chunk C (engine transport + test service + conformance)**: ~6 commits on its branch
  (`svc-chunk-c-implementer` worktree), all unit gates green, integration suite green on
  final background run; was about to re-hop onto `07e050e233` and report. Pause relayed;
  it is writing its own state note. NOT reviewed, NOT landed. One design-relevant finding
  it surfaced: T-S7's cross-engine salt-partition counter is unimplementable as written —
  salted handles are baked into recipe identity (content-digest scoping), so a
  different-salt engine misses at IDENTITY level (zero candidates) and the eligibility
  gate never fires; the partition is provable by consequence instead. (Design §11/T-S7
  needs this as-built correction whenever work resumes.)
- **Chunk B landing follow-ups**: none — fully closed.
- **Next steps that would have followed**: chunk C review + landing; chunk E (keeper
  lane, checks enqueue, e2e full-stack — brief drafted at
  `hack/designs/remote-cache/service/chunk-e-brief-draft.md`); chunk F review; chunk G
  (live deploy + cloud warm proof).

## Salvage dependency map (factual; the ruling is store-and-select → merged-bundle)

### Independent of store-and-select vs merged-bundle (salvageable as-is)

**Landed engine work (chunks A+B — already integration reality, and architecture-neutral):**
- Origin identity (store UUID + result ID + persisted allocator high-water): the mechanism
  that makes ANY import idempotent — including a server-side merge. This is the piece that
  structurally kills the v2 result-row blowup regardless of WHERE bundles are composed.
- The portable-ref contract (all four ref locations, token encodings, structured rewrites).
- Bundle encode/decode machinery itself (one-encoding writer/reader over streams,
  forward-closure filter, identity validation, per-result vetting reuse, wipe-isolation):
  ingesting one merged bundle is the trivial case of the same code.
- Content chains + the content-chain source (compute-at-export, walk arm, failure typing,
  counters, prefix reuse, ingest hygiene) — content transport is orthogonal to metadata
  composition.
- N1 vetting chain fallback; contentless mutable-owner decode; the reset-r24 tie-break;
  the eager-decode lease rule; schema 19.

**Service work (dagger.io, unmerged branches):**
- Blob CAS: backends, org-prefixed keys, signed URLs, staging+promote, advisory-lock
  completion, `cache_blobs` presence-means-verified inventory, upload-token authority.
- Event log with typed/sanitized payloads; org tenancy + auth composition; production
  mounting pattern + startup gates; dev-server harness; limits/DoS hardening.
- Chunk F's machinery: event/CAS surfaces, the version-aware degrading indexer pattern,
  dashboard rendering (its own state note quantifies ~70% neutral).
- Chunk C's client mechanics, BlobSource implementation, export-pipeline and boot-import
  wiring shapes, test-service + conformance PATTERN.

### Assumes separate-bundles semantics (the store-and-select-specific ~minority)

- Service: §10.3 selection (newest-per-exporter-store, K bundles), `cache_bundles` as a
  per-bundle serving inventory, manifest-as-selection-input, per-bundle blob-coverage /
  bundle-timeline insight semantics.
- Engine: the multi-bundle boot-import loop (selection call → K downloads → per-bundle
  merge) — the per-bundle ingest code survives; the multi-bundle orchestration and its
  budget/degradation counters are specific.
- Design doc: §2 (the load-bearing decision), §10 D2/D3, parts of §6.6 (the snowball /
  prune-propagation growth reasoning), §12/§13 test shapes that assume K-bundle selection
  (T-S3's multi-bundle cycles as written).

### Load-bearing inputs for whatever comes next (factual, not advocacy)

- §2's rejected-alternative analysis enumerates the three costs any merged-bundle design
  must answer (server-side identity model, engine-schema coupling, durable merged state) —
  and origin-keyed idempotent import (landed) changes that calculus: server-side merging
  is no longer structurally exposed to the v2 blowup, because row identity is now durable
  and origin-deduplicable wherever the merge runs.
- The engine's ingest path takes N bundles today; a merged-bundle service reduces this to
  N=1 with zero engine changes. The delta is entirely service-side composition (plus
  whatever tooling composes bundles — candidates include running the engine's own
  bundle code offline, which the one-encoding rule makes possible).
- Wire-protocol surfaces most likely to change: selection/listing; publish/stat/upload
  and blob GET are composition-agnostic.

## Branch/worktree inventory at pause

| what | where | state |
|---|---|---|
| design + review log | this worktree, branch `service-phase-designer-84d84a98` | committed; push attempted |
| integration branch (A+B landed) | take-3 worktree family, tip `07e050e233` | landed; take-3 manager runs push retries |
| chunk C | `svc-chunk-c-implementer-19c3c387-43415127` | pausing per directive |
| chunk D | dagger.io `svc-chunk-d-implementer-207ba9ac-bbf98c05` @ bc5ec0cad | pausing per directive |
| chunk F | dagger.io `svc-chunk-f-implementer-8ff64dab` | paused, pushed |
| chunk A/B seats | worktrees retained | closed (landed); warm reference |
