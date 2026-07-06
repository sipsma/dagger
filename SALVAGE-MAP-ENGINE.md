# SALVAGE-MAP-ENGINE — dependency split of the landed service chunks (A + B)

Input to the salvage assessment for Erik's merged-bundle ruling. Scope: the two chunks
**landed on the engine integration branch** — chunk A (bundles + origin identity, merge
`6146c7e31a`) and chunk B (chains + the content-chain source, merge `93135b3b0b`) — as
they exist at integration tip `95d0034f1b`. Factual dependency mapping only; no
architecture advocacy.

**The classification question, applied to every mechanism:** does its correctness or
semantics depend on bundles being *separate, exporter-composed units served verbatim*
(store-and-select), versus *one service-merged bundle*? Three answers appear:

- **A — Composition-independent:** correct and meaningful under either architecture,
  unchanged.
- **B — Agnostic code, store-and-select-calibrated semantics:** the code survives
  either way, but a semantic calibration (blast radius, naming, expectation) was chosen
  with separate bundles in mind and needs a deliberate re-decision under merged bundles
  — recalibration, not rewrite.
- **C — Store-and-select-specific:** encodes the separate-bundles contract itself.

---

## A — Composition-independent

| Mechanism | Where | Why independent |
|---|---|---|
| **Origin identity**: store UUID minted at fresh boot and at every wipe; `result_origins` rows (`UNIQUE(origin_store_uuid, origin_result_id)`); first-assignment-wins (`assignResultOriginLocked`); flush-time minting for local rows | `dagql/cache.go` (`loadStoreIdentity`), `persistdb/schema.sql`, `cache_persistence_worker.go` | Identifies each result by its minting store, which is what makes any cross-store arrival idempotent. A merged bundle must carry per-row origins too, or re-import double-counts. Wipe-remints-UUID (retiring stale origins) is store-lifecycle, not bundle-lifecycle. |
| **Allocator high-water mark** (`MetaKeyMaxResultID`, `noteAllocatedResultIDLocked`, survives in-memory drains) | `dagql/cache.go`, meta table | Guarantees a store never reuses a result ID, so origin pairs stay unique forever. Underpins origins regardless of composition. |
| **Portable ref tokens + payload walk/rewrite** (`$dagqlResultRef`, `$dagqlCallID`; rewrite into the local ID space at arrival) | `dagql/cache_persistence_refs.go`, `cache_bundle_import.go` | ID-space translation is required by any transfer between stores. Also used by *local* persistence (one encoding, R9) — load-bearing even with no service at all. |
| **Per-result transfer encoding** (same `PersistedResultEnvelope` as local persistence; engine-local rows stripped at export) | `cache_bundle_export.go` | R9's one-encoding. Any composition of any number of results reuses it per row. |
| **Arrival-time origin dedup** (`resultsByOrigin`; dedup gate skips `dropped` corpses so a re-supplied origin stages the healing row; identity-evidence union on dedup) | `cache_bundle_import.go` | The mechanism is idempotent multi-source arrival. It is *named* as the engine half of store-and-select ("engine-side merge"), but its correctness does not depend on separate bundles: even a single merged bundle arriving at a store with local overlap (or arriving twice) needs exactly this. Under merged bundles it processes fewer duplicates; nothing about it becomes wrong. |
| **Arrival atomicity**: all fallible work (parse, vetting, reference rewrite) before the first mutation; infallible commit under the coarse `egraphMu` hold | `cache_bundle_import.go` (`ImportBundle`) | Quiesce discipline for any arrival unit, whatever composed it. This is what satisfies reset R14's escape-hatch precondition (mechanism level; invocation policy is a separate, service-side decision). |
| **Vetting integration**: exactly-one-origin-per-row (missing ⇒ per-row `malformed` drop); shared keep/drop rules applied to bundle rows (missing-dep, cycles, cascades); rewrite-failure = missing-dep post-remap | `cache_persistence_vetting.go`, `cache_bundle_import.go` | Per-result damage philosophy (reset §8c) applied at a second arrival channel. Row-scoped, composition-blind. |
| **Bundle rows join the restored materialize protocol** (`restored: true`; restore-attached deferred-work failure = exhaustion; retirement mark correct-by-construction for linkless bundle rows) | `cache_bundle_import.go`, `cache_persistence_import.go` | Serving-side semantics of an imported row; independent of how the row traveled. |
| **Chain compute / materialize / blob plumbing**: `ChainForSnapshot` (zstd pinned+forced, hash-at-export only, memoized), `MaterializeChain` (fetch missing blobs → apply as snapshot layers → install a local-snapshot source), `ensureChainBlob`, ingest-abort on failed blob writes, the `BlobSource` seam (serve blobs by digest) | `engine/snapshots/chain.go`, `blobsource.go` | Pure content addressing. A CAS serves blobs by digest under any composition; "content arrives once, then serving is local" is composition-blind. |
| **`sourceContentChain` in the retained-source walk** (round-19 slot: local → chain → lazy; realization installs a local-snapshot source; round-24 starved tie-break + clear-on-delivery) | `dagql/cache.go`, `cache_egraph.go`, `cache_persistence_import.go` | Runtime serving semantics of a row that has a chain, however it got one. |
| **`result_content_chains` persistence + local chain-vetting fallback** (a row whose snapshots are gone survives local boot vetting on its chain source) | `persistdb/schema.sql`, `cache_persistence_vetting.go` | Local-store durability of chain sources; no service in the loop at all. |
| **Mutable-owner contentless decode** (identity crosses, content re-acquires) | `core/` | Type-level arrival semantics, composition-blind. |
| **Schema v19 as a unit** (result_origins, result_content_chains, ref-token payload encoding; wipe-on-upgrade) | `persistdb/schema.sql` | All three additions classified independent above. |
| **`ongoingCall.isPersistable` race fix** (atomic) | `dagql/cache.go` | Pre-existing engine bug fix; unrelated to the service entirely. |
| **Export mechanics**: closure walk over selected roots, engine-local row stripping, memoized chain compute, `BlobIndex` production | `cache_bundle_export.go` | The engine's job under either architecture is "serialize selected results portably." What to select is caller policy in both worlds. `BlobIndex` ("every blob digest the chains reference — what the caller offers the CAS") is upload-negotiation metadata for a CAS; it has **no import-side consumer** (verified) and is meaningful under any composition. |

## B — Agnostic code, store-and-select-calibrated semantics

| Mechanism | Where | What survives / what needs a re-decision |
|---|---|---|
| **Bundle-as-validation-unit semantics**: section garbage ⇒ whole-unit skip (`CacheBundleSkipError` classes, e.g. `malformed_chains`); all-or-nothing per-unit commit; per-row damage inside a valid unit ⇒ row drops with in-bundle dependents (`chain_damaged`) | `cache_bundle_import.go` | The code is unit-relative and composition-blind: whatever arrives as "one bundle" gets unit-integrity gating with per-row blast radius inside it. The store-and-select **calibration** is that a skipped unit = one exporter's set. Under one merged bundle, the identical rule means one section-garbage byte skips the *entire* pull. Survives with a deliberate blast-radius re-decision (unit size / sharding / section-scoped skip), not a rewrite. |
| **Manifest shape: singular exporter `StoreUUID`** | `cache_bundle.go:42`, import consumers at `cache_bundle_import.go:76` (summary copy) and `:643` (log line) | **Informational only — nothing is keyed on it** (dedup keys on per-row origins). A service-merged bundle passes through this code unchanged with a composer UUID or none. The store-and-select residue is the field's *connotation* ("a bundle is one store's export"), not a mechanism. |
| **Test-only transport model** (fetch-whole-bundle-by-key file transport; the T-S4/T-S6 cross-engine proofs ride it) | `engine/server/cachetransport_testonly.go`, `cmd/engine/debug_cachetransport_testonly.go` | Two layers with different fates: the *bundle-fetch-by-key* model mirrors store-and-select's serving contract; the *`BlobSource`* layer underneath (blobs by digest) is CAS-shaped and independent. Compiled out of non-test builds either way; the proofs re-target whatever transport chunk-C-or-successor defines. |
| **`RowsDedupedByOrigin` reporting frame** (summary counters assuming cross-bundle duplication is routine) | `cache_bundle_import.go` (summary struct) | Inert metrics; under merged bundles the counter trends to ~0 but reports honestly. Only the *expectation* it encodes is store-and-select-shaped. |

## C — Store-and-select-specific (in the landed chunks)

**Very little landed engine-side is load-bearing store-and-select.** The specific
contract — a service that stores exporter bundles verbatim and *selects* which to serve
— lives in the unlanded chunk C (service client, selection protocol) and in the service
repo's design, not in these two chunks. What remains engine-side:

1. **Naming, comments, and doc framing** that say "bundle = one exporter's coherent
   export" (including the manifest connotation above). Cosmetic under a re-ruling.
2. **Nothing else identified.** Every mechanism inspected either classifies as A or
   carries only the calibrations listed in B.

## Corrections to the routing priors (the point of the exercise)

1. **"Bundle-as-unit manifest shape → store-and-select-specific": PARTIAL, mostly
   wrong.** The per-result encoding and per-row origins are independent (A); the
   manifest's exporter `StoreUUID` is informational with zero keyed consumers
   (verified); the unit-validation code is composition-blind. Only the blast-radius
   calibration and the field's connotation are store-and-select-shaped (B).
2. **"Whole-bundle skip semantics → store-and-select-specific": REFINED.** The code
   survives as unit-relative integrity gating; what needs re-deciding under merged
   bundles is the *size of the unit the rule applies to* (B), because the identical
   rule at merged-bundle scale means all-or-nothing for an entire pull.
3. **"Selection-facing metadata → store-and-select-specific": CORRECTED — essentially
   none landed engine-side.** `BlobIndex` is CAS upload negotiation (independent, no
   import consumer); selection metadata proper never reached the engine (it is chunk-C
   / service-side territory).
4. **"Anything assuming the service returns exporter bundles verbatim": CONFIRMED but
   nearly empty engine-side** — the assumption lives in the test transport's
   fetch-by-key model and in naming (B/C above), not in import mechanics, which accept
   any well-formed unit.
5. **Priors confirmed as stated:** origin identity + high-water mark, portable-ref
   token contract, chain computation/materialization, per-result vetting mechanics,
   §9 source-walk integration, two-phase export mechanics — all A, with the one nuance
   that *arrival-time origin dedup* (the "engine-side merge") is also A: it is named by
   the store-and-select architecture but required for idempotent arrival under any
   architecture (local overlap, re-import), and merely does less work under merged
   bundles.

---
*Method note: classifications verified against the tree at `95d0034f1b` (grep-verified
consumer sets for the two decisive cases: manifest `StoreUUID`, `BlobIndex`). Landing
reviews for both chunks (reset design rounds 23 and 25) are the provenance for the
mechanism inventory.*
