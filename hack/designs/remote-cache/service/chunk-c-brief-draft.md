# Chunk C implementer brief — DRAFT (spawn when chunk B converges)

**Base**: fork from chunk B's converged worktree (svc-chunk-b-implementer-4c435784-a509122f,
tip = post-delta-review convergence) — carries the landed integration tip (df3e1d6b39 =
chunk A landed, schema 19) + chunk B's chains/walk-arm/vetting/contentless work.
Boilerplate (process rules, Step-0 gate + exact invocation, report format, pre-code surface
map, push-may-refuse, no PRs, stop-and-discuss) verbatim from prior briefs.

**Scope (service design §14 chunk C + §7 D1/D4, §8 boot flow, §11 engine counters, §12
rungs 1–2, §13 T-S2/T-S3/T-S5/T-S7/T-S9):**

1. **The engine cache-service client** (engine/cacheservice/client.go per §7.5): the six
   /v1 endpoints as implemented by chunk D (READ THE AS-BUILT PROTOCOL from the chunk D
   worktree: api/server/cacheservice/protocol.go + handlers — wire shapes are FROZEN as
   built, incl. multipart publish fields manifest/archive, stat digests-only, uploads →
   {url,method,uploadID}|alreadyExists, uploads/complete batch, 307 blob GET, bundle
   listing with downloadURL). Engine config: service URL, token (secret file/env), scope,
   budgets, exportOnShutdown, metadata-only mode. Auth header per chunk D's middleware.
2. **BlobSource implementation over the client** (chunk B's seam) for chain fetches at
   serving time; digest verification stays in MaterializeChain.
3. **Export pipeline wiring** (§7 D4): admin API POST /v1/cache/export on the engine's
   operator listener (productized, not testonly; coalescing; summary response), the
   exportOnShutdown hook in GracefulStop (after session drain + prune, budget-bounded),
   two-phase export through the client (publish metadata → stat → upload missing →
   complete). Replaces/subsumes chunk B's testonly transport for the export side (the
   testonly build-tag surface may remain for file-transport tests — do not delete chunk
   B's gates).
4. **Boot import wiring** (§8 D4): config-driven selection call → download K bundles →
   ImportBundle per bundle (oldest→newest), inside the boot window, importBudget-bounded,
   monotone degradation (fewer bundles → none → cold), typed counters; boot summary
   extension {bundles_offered/fetched/merged/skipped_by_reason/rows_imported/
   rows_deduped_by_origin/import_budget_exhausted} in stats file + debug snapshot (§11).
5. **The in-repo test service** (internal/testutil/cacheservice or similar): a small Go
   binary implementing the /v1 protocol over filesystem storage — the protocol's reference
   implementation. MUST pass the same conformance suite as the real service.
6. **The conformance suite** (T-S9): protocol-level tests parameterized over an endpoint
   URL; runs against the in-repo test service in dagger/dagger CI; exported so dagger.io
   can run it against real handlers (coordinate shape: a Go package with exported
   ConformanceTests(t, baseURL, token) or similar).
7. **The in-repo integration suite** (rung 2, extending the T-S4/T-S6 transport tests):
   two dev engines + the test service as Dagger services; T-S2 idempotence at integration
   level; T-S3 ≥3-cycle growth gate WITH a prune cycle (counts flat, evidence = stats
   files + export summaries per cycle); T-S5 metadata-only mode (lookups hit, zero chain
   fetches, lazy-form realizations > 0, run green); T-S7 salt partition (two engines
   different salts → typed ineligibility counter ≥1; pre-seeded shared salt
   (<rootDir>/secret-salt, 32 bytes) → hits) + candidate_ineligible_session_resources
   counter if not already emitted (§11 table).
8. **Engine counters completion** (§11.1): export summary counters, import/boot summary,
   candidate_ineligible_session_resources typed miss; everything consumed by a named test.

**Gate**: T-S2 (integration), T-S3, T-S5, T-S7, T-S9 (rung 2) + chunk A+B suites + Step-0.

**Known facts to carry**: engine-dev harness "N passed" counts leaf testctx cases;
containers are the only fragment-backed rows a warm store restores; the starved tie-break
(reset r24) affects selection when transport is dead — T-S3/T-S5 fixtures should expect
hit_restored semantics per §9's as-built counters; chunk D's service requires org tokens —
test service should accept a static token for the harness.
