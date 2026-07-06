# Chunk B implementer brief — DRAFT (held until gates clear)

**Gates before spawning:** (1) ~~reset §9 landed~~ CLEARED (integration tip 93cc7419a4); (2) ~~§9.1 ratified~~ CLEARED (reset round 19, v24 — order: local → content-chain → lazy); (3) chunk A stable enough to base on — STILL OPEN. Original gate text follows: (1) reset §9 warm serving landed on the take-3 integration
branch; (2) the §9.1 source-order amendment (local → content-chain → lazy) RATIFIED in the
reset design; (3) chunk A landed or stable enough to base on. Spawn a fresh Fable xhigh
implementer; adapt the boilerplate (process rules, Step-0 gate, report format, pre-code
surface map) from the chunk A brief verbatim.

---

You are the implementer for CHUNK B of Dagger's remote-cache SERVICE PHASE: content chains
and the content-chain materialization source. Fresh Fable xhigh; spawned by the
service-phase manager. Required reading identical to chunk A (service design v4 IN FULL —
especially §6.4, §6.5, §7 D2, §8.1 step 6, §8 D4, §9 in full, §13, §14 chunk B; reset
design §7/§9 + lessons; internal-docs cache_persistence.md, cachebasics.md; PLUS
engine/snapshots: remote.go (ensureExportBlob), pull.go, persistent_metadata.go,
diffapply).

Scope, per §14 chunk B:
1. **Chain computation at export** (§7 D2): ChainForSnapshot over the existing
   ensureExportBlob/compression machinery; zstd pinned; per-snapshot chain memoization in
   snapshot-manager metadata; hash-at-export ONLY (R4 — nothing during normal builds);
   chains + blob index into the bundle manifest (the fields chunk A left empty);
   per-result chain rows persisted locally in result_content_chains (table exists from
   chunk A's schema bump); mutable-owner snapshots produce no chain (§6.4).
2. **Chain sources at import** (§8.1 step 6): manifest chain entries become
   sourceContentChain entries on materializationState, ordered per the RATIFIED amendment:
   local snapshot → content-chain → lazy form. Same-origin source union (§8.1 step 3).
3. **The content-chain source realization** (§9): un-reserve sourceContentChain; the walk
   arm = prefix check via imported_layer_{blob,diff}_index → fetch missing blobs into the
   content store (file/dir CAS interface this chunk; the HTTP client is chunk C — build
   against a small BlobSource interface) → apply layers via the existing unpack/apply
   machinery → register refKeys + owner lease + snapshot_content_links → install
   local-snapshot source on the home → continue walk. Failure typing per §9.3: 404/absent
   = permanent (mark non-viable this boot), transport = transient (no marking), digest
   mismatch = permanent + loud + discard (L8), apply failure = transient. Counters:
   chain_fetch_{ok,missing,error,corrupt}, served_from{content_chain} joining the reset
   vocabulary.
4. **N1 — local-restore vetting learns the chain fallback** (§8.4): a locally-restored row
   whose refKeys are gone survives vetting if it has a persisted chain OR a lazy fragment
   (vetting.go:149–161 gains the chain as second fallback). Red test T-S13.
5. **Mutable-owner contentless decode** (§6.4): git mirror + filesync mirror decoders gain
   the contentless-identity path (CacheVolume already tolerant — verify, don't churn);
   until a type's decode lands its rows are non-portable (excluded at export with typed
   counter). Red tests per type; T-S12.

Gate: T-S4 (cross-store warm proof via file bundles + dir CAS: outputs equal,
hit_restored ≥ K per field, served_from{content_chain} ≥ 1, demoted_to_miss == 0), T-S6
(sparse blobs: delete one blob → green run, chain_fetch_missing ≥ 1, lazy-or-demote covers,
second run hits), T-S8 (N sessions force one imported result → one chain materialization,
N−1 waiters, -race), T-S12, T-S13, + Step-0 (reset suites, engine-dev-test workflow, NEVER
./bin/dagger).

Constraints: do NOT redesign the source walk — the chain arm plugs into reset §9's
realize() as built. R12 demand-boundedness is structural: never materialize a dependency
DAG from the chain arm. No new cross-cutting concurrency (R15): concurrent same-blob
fetches dedup on the content store's ingestion locks, nothing new.
