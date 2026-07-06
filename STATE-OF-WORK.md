# Chunk C — state of work at pause (2026-07-07)

Branch `svc-chunk-c-implementer-19c3c387`, 8 commits on chunk B's pre-landing tip
`55dab5073e` (which landed verbatim into the integration branch as merge `93135b3b0b`,
tip `07e050e233`). **Not rebased onto the landed tip and not landed anywhere**, per the
pause directive. The planned next step (see "next steps") was exactly that re-hop.

## Commits

| Commit | What |
|---|---|
| `5ee616114f` | engine/cacheservice client (as-built /v1 protocol) + BlobSource adapter + engine-config `cacheService` section + env overrides + in-repo test service (internal/testutil/cacheservice + cmd) + dagql plumbing (ReadCacheBundleManifest, PersistenceSchemaVersion, MetadataOnly export lever, CacheBundleBootSummary vehicle) |
| `97ee757387` | engine/server export pipeline (coalescing, encode→publish→stat→upload→complete, POST /v1/cache/export on the operator/debug listener, exportOnShutdown in GracefulStop) + boot bundle inflow (§8) + typed `candidate_ineligible_session_resources` at the lookup eligibility filter |
| `867738b328` | conformance suite (engine/cacheservice/conformance, RunConformance(t, baseURL, token)) — green vs the test service (T-S9 rung 1) |
| `6928779c68` | integration gate (core/integration/engine_cache_service_test.go, TestCacheService suite) + test-only file transport deleted under its replacement contract; test-service binary ships beside engine.tar via toolchains/engine-dev/test.go |
| (schema) | docs/static/reference/engine.schema.json regenerated for `cacheService` |
| `9d89d8a082` | fix: PutBlob leaves body ownership with the caller (http.Client closes ReadCloser bodies; the engine's content-store reader was double-closed → every blob upload "failed" → fully sparse CAS → lazy-form recompute cascade). Export summary gained `skip_reasons` (S8 diagnosability) |
| (T-S7 fix) | T-S7 tests switched to URI-form (`env://`) secrets — the salted handle surface |

## Gate record (all on this branch's tree, stable dagger CLI + engine-dev toolchain)

- Unit: full `go test ./dagql/ -race` ok; 32 bundle/chain leaf passes; engine/cacheservice
  (client + PutBlob-ownership regression) ok; conformance-vs-test-service ok; engine/snapshots ok.
- Integration `TestCacheService` (rung 2): **8 passed / 0 failed, exit 0**
  (TestCrossEngineWarmViaService, TestSparseBlobsFallThroughAndHeal,
  TestReimportAcrossBootsZeroGrowth, TestMultiCycleGrowthBound,
  TestMetadataOnlyExportServesViaLazyForms, TestSaltPartitionScopesReuse,
  TestSharedSaltCrossEngineHit + suite parent).
  T-S3 evidence: rows 4620 → 4620 (0 blob re-uploads, 5 already-present) → 4616 post-prune
  (chainless lazy export; prune propagates) → 4620 (pruned rows returned exactly once, 0
  re-uploads). T-S2 evidence: boot 2 rows_imported == 0, rows_deduped_by_origin == 4620 ==
  boot 1 rows_imported.
- Step-0, per-bucket (the canonical evidence format chunks A/B used): TestCachePersistence
  **25 passed**, /TestCrossSession **112 passed**, TestServices **80 passed** — all exit 0,
  zero failures. **Identical to chunk B's 25/112/80 baseline.** (A combined-regex run
  totaled 215; the −2 vs summing buckets is a counting artifact of the overlapping
  combined pattern with the suite-parent swap — TestCacheBundleTransport deleted,
  TestCacheService added — not a missing or failing test; the per-bucket runs are the
  proof.)

## Salvage dependency map

### Independent of store-and-select vs merged-bundle (reusable as-is)

- **The HTTP client mechanics** (engine/cacheservice/client.go): Bearer auth, multipart
  streaming publish, stat/upload/verify batching under protocol limits, 307-following blob
  GET, the no-token-on-presigned-URLs discipline, PutBlob body-ownership fix. All of this
  is CAS + artifact-store plumbing; a merged-bundle service still needs every piece.
- **The BlobSource adapter** (blobsource.go): 404→permanent-per-boot, everything-else
  transient. Pure §9.3 seam; independent of what metadata artifact produced the chain.
- **Blob CAS semantics end to end**: stat=verified-only, prepare/PUT/complete with
  completion authority, corrupt-bytes rejection, idempotent re-upload. Identical under any
  metadata model.
- **The export pipeline shape** (engine/server/cacheservice.go): coalescing single-flight,
  two-phase publish-then-blob-delta, retry-then-skip sparseness, budget bounding, the
  operator endpoint + GracefulStop hook, the §11 export summary + skip_reasons. What it
  publishes would change (one merged artifact vs a bundle), not how.
- **Boot import wiring shape**: config resolution (engine.json + env-only provisioning),
  the pre-serving boot window placement, budget-bounded monotone degradation, the boot
  summary vehicle (stats file + debug snapshots). The *loop body* changes (see below); the
  frame does not.
- **Engine config surface** (`cacheService` section + DAGGER_CACHE_SERVICE_* env) and the
  regenerated schema.
- **The test-service + conformance PATTERN**: a filesystem reference implementation with
  signed data URLs + an exported RunConformance(t, baseURL, token) suite that pins the wire
  contract implementation-independently. The pattern (and most cases: auth matrix, publish
  validation, blob lifecycle, limits) survives any protocol revision; specific endpoint
  cases would be re-derived from the new protocol.
- **Counters**: chain-fetch vocabulary consumption, the boot/export summaries as vehicles,
  `candidate_ineligible_session_resources` (a lookup-eligibility fact, orthogonal to
  transport), `MetadataOnly` as a recompute-forcing lever.
- **The integration-harness machinery**: dev engines + service as Dagger services,
  test-service binary shipped beside engine.tar, salt pre-seeding helper, stats-file
  readers, admin-export driver. Workload/proof shapes (random-marker equality + counters)
  are architecture-independent.
- **dagql additions**: ReadCacheBundleManifest (byte-identical manifest extraction),
  PersistenceSchemaVersion accessor, CacheBundleBootSummary plumbing.

### Assumes separate-bundles (store-and-select) semantics

- **The selection call** (`SelectBundles` + `GET /v1/scopes/{scope}/bundles` with
  schemaVersion/bundleFormat/limit): the "which bundles for this engine" question is the
  store-and-select brain. Gone or reshaped under a merged-bundle service.
- **The multi-bundle import loop** (engine/server cacheServiceBootImport): oldest→newest
  ordering, per-bundle skip accounting, `bundles_offered/fetched/merged/
  skipped_by_reason` counter shapes, K/importLimit. A merged model imports one artifact.
- **Test-service selection semantics** (newest-complete-per-store, complete-beats-newer-
  pending, per-store grouping) and the conformance cases pinning them
  (SelectionSemantics, parts of BundleLifecycle/SelectionValidation).
- **Integration assertions keyed to bundle multiplicity**: T-S3's `bundles_merged == 2/3`
  per cycle and cross-bundle origin-dedup assertions; T-S2's re-import-same-bundle shape
  (the zero-growth *property* survives; the mechanism asserted is per-bundle dedup).
- **Publish/complete lifecycle as bundle-per-export** (pending→complete status, per-bundle
  downloadURL). A merged service likely keeps *an* artifact lifecycle, but the
  one-export-one-bundle identity is store-and-select's.
- Note: per-row origin identity, ref tokens, chain computation/materialization, and
  per-result vetting are chunks A/B's surface, not built here — but chunk C consumes them
  only through ImportBundle/ExportBundle, so chunk C carries no additional coupling to
  them beyond those two entry points.

## As-built findings recorded during the gate (worth keeping regardless of architecture)

1. **T-S7 divergence**: the salt partition surfaces at *identity*, not at the eligibility
   filter — salted handles are baked into downstream recipe digests via content-digest
   scoping, so a different-salt engine finds zero candidates and
   `candidate_ineligible_session_resources` can never fire cross-engine. The counter is
   emitted at the filter and unit-pinned (TestSessionResourceIneligibilityIsTyped); the
   integration proof asserts the partition via scoped recompute + shared-salt verbatim
   transfer.
2. **Only URI-form secrets are salted** (`SecretHandleFromPlaintext`); `setSecret` handles
   are `hash(name, accessor)` — salt-independent and therefore *portable across engines*
   (observed directly: a setSecret-dependent exec transferred across differing salts).
3. **http.Client closes ReadCloser request bodies** — the double-close bug above; any
   future upload path streaming from the content store must keep body ownership explicit.

## Mid-flight at pause

- Nothing. The per-bucket Step-0 re-tally completed before the pause took effect (numbers
  above); every gate this chunk owns is green on this branch.

## Next steps (as they would have been)

1. Rebase the 8 commits onto the landed integration tip `07e050e233` (expected
   zero-conflict; base merged verbatim) and re-run unit + TestCacheService as insurance.
2. Adversarial code review round (fresh Codex), then landing package to the take-3 manager.
3. Hand the conformance suite to chunk D for rung-3 wiring (RunConformance against the
   real handlers).
