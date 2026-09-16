# Batch 2 standalone implementation

Branch: `remote-cache-b2-transfer-implementer-26387947`.
Base: `1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f`.
Implementation tip: `b76c6ccbff2f68f2ca6f52b5b89ef7ec6c6d2dbf`.

Steps 1–5 are implemented under coordinator addenda 1 and 2. The full native
standalone acceptance suite passes with B's runtime prepared through normal
`AsModule` before import, without serving its schema or entering `report`.
The isolated confirmation of the previously failing restart case also passes.
The fully cold order remains an explicit skipped test naming addendum 2 and
belongs to batch 4/7 acceptance. Batch 1 has not been merged; integration onto its head is pending.

## Round 2 council decisions

P1–P5 are addressed in new signed commits. P1 makes foreign pending Containers
report pending evaluation, refuses direct filesystem evaluation, and treats only
captured metadata / absent parts as final during delegation. Ordinary child
producers now retain their pending parent dependency. The imported-Container
schema regression verifies `withEnvVariable(...).rootfs().entries()` returns the
unavailable-part error and that the metadata delegation sweep remains usable.

P3's operational and scoped candidate accessibility checks were already present
at the reviewed tip. The candidate filter is now explicit and permanent tests
verify both inaccessible-candidate cases continue to the eligible recorded row,
then to canonical selection when that row expires. The final check remains.
P5 replaces offer-copy panics with not-ready errors throughout capture,
persistence and reference visitors. An invalid layer timestamp exercises both
capture APIs and verifies lock release and unchanged ownership counts.

P4 removes the implementation branch's design copy. The authority is the
designer's `2e75801259faf20df2c20a44076c19b027a541b1` commit, blob
`ceb089285bd7d1f9938171c42c6d09d47310a141`, including the addendum 2 paragraph.
P2's mechanism and measured evidence are recorded below.

## Result and verification

Held export copies exact value closures, separates offer-owner retention from
lookup requirements, exports only selected immutable chains, and imports fresh
rows through one atomic publication. Foreign filesystem values retain pending
metadata and saved producers; demand still returns the unavailable-part error.
Schema recovery prefers installed operational Modules, then eligible exact
recorded Modules, then the existing session-compatible canonical fallback.
Operational readers of foreign local module paths return the explicit sentinel.
Schema 20 / envelope 4 is a hard cut, with bundle version 1 and no migration.

The native suite uses separate engine state, clients and fixture volumes. Only
committed metadata bundles cross engines. It verifies:

- Import before schema installation and after normal schema loading; ordinary
  `report` hits with zero B function entries, and a changed argument enters once.
- Saved handles through `node(id:)`, B's current File default context, then a
  clean restart and B's edited Directory default context. A raw fixture check
  verifies the saved row exists immediately after restart.
- Interface argument/return conversion, the non-builtin `Platform` scalar and
  module-defined `Status` enum, including saved-handle recovery after restart.
- Portable lazy bound tools, invocation through their defining schema, and
  same-type return rebinding (`withSeed` followed by `label`).
- A higher eligible native recorded Module in a bare client, with an assertion
  that its imported equivalent has a lower result ID. Its contextual tool reads
  B's notes. The existing conflicting-schema unit control also remains passing.
- Both residual foreign-context cases in a client with prepared runtime but no
  served Module: an explicitly imported recorded Module, and an expired recorded
  Module with an eligible imported equivalent. Bare `node` loads recover the
  schema; bound tools invoke the contextual method without requiring a GraphQL
  fragment on an uninstalled type. Both return the foreign-context sentinel
  before entering that method. A native no-import contextual control succeeds.

The privileged selected-chain peers all run and pass, with no skips. They cover
real local and remote Git checkout backends, a nested Directory/File view over a
real snapshot, whole-parent bytes, a selected Container mount, unopened sibling
parts, and a broken completed local open. The remote Git peer uses a local file
transport. A separate real-store restart peer installs the completed-output state directly
(the batch has no acquisition), then verifies real snapshot ownership through
restart, redundant offer retirement, transfer-pin removal and GC. Exact commands and outcomes are in [VERIFICATION.md](VERIFICATION.md).

The engine prunes DagQL persisted roots under its default disk-derived GC
policy: `gcLocked` calls `Cache.Prune` (`engine/server/gc.go`, formerly line 354),
and `getDagqlGCPolicy` / `defaultGCPolicy` supply disk-derived space bounds and
an `All: true` final policy (formerly lines 453–541). The existing persistence
suite already pins the same high bounds for restart measurement. Batch 2's
offer-owner traversal only adds retention; no prune eligibility or policy was
relaxed, and imported roots retain ordinary pruneable persisted edges.

Round 2 adds fixture-gated diagnostics outside the cache database. They preserve
the current boot's `PersistenceResetReason` even when the engine replaces the
cache instance after a reset, the engine-level reset reason, the cumulative
count of persisted roots removed by automatic disk/metadata GC, and disk-pruned
result IDs. The native suite reads them before loading any saved handle after
restart, asserts no reset and zero removals with pinned bounds, then asserts the
saved row still has its persisted edge. There are no fixture file operations
when the gate is absent. The exported fixture APIs remain gated by convention.

The targeted default-policy reproduction resolved S1 as ordinary pruning of a
pruneable imported root. In trace `a1363afb7a7c79639b0741cf7fbc4c05`, saved
`CacheProbe.report(seed: "same")` row **4681** had a persisted edge before shutdown.
After clean restart, both reset reasons were empty and the removed-root count
was **16**, with `dagql.result.4681` among the decisions. The engine logged the
exact row's removal; the test asserted the row was absent. Pinned before/after
acceptance cases both reported zero removed roots and no reset.

The diagnostic keeps the default limits unchanged. It measured a 99,000,000,000
byte minimum-free target and 103,559,827,456 available bytes, then created a
6,397 MiB temporary file in the fixture volume (below its 8 GiB cap) to cross
that target with room for the roughly 627 MB cache. Cleanup removed the file.
Earlier attempts had no reclaim target and explicitly skipped the pruning
assertion; they are not pruning evidence. The historical failure lacked these
counters; this reproduction establishes the same saved-report removal mechanism,
not a retroactive measurement of that earlier process. No reset or ownership
failure was observed. See [restart diagnostics](logs/round2-restart-diagnostics.log)
and [VERIFICATION.md](VERIFICATION.md).


## Writer and reader inventories

Addendum 1's nonblocking File/Directory capture guards and typed `OutputRev`
remain unchanged. The permanent latch-held negative tests and Container control
pass. [OUTPUT-WRITERS.md](OUTPUT-WRITERS.md) is the complete report appendix of
production publication and initializer sites from the included AST audit,
including completion wrappers and stored-snapshot callbacks. The four accessor
writes are centralized in guarded setters; completion retains the body's latch.
Capture samples and rechecks typed output and encoded representation revisions.
[FOREIGN-PATH-READERS.md](FOREIGN-PATH-READERS.md) classifies every audited path
reader and conversion, including the data-only exceptions.

## Signed commit order

1. `de126ebe775d078a4c6e240d4d697d3bfdc6834c` — common records, owners, visitors and format cut.
2. `85de1ed96d12d1b91589dfdba4368b7cbc512557` — foreign forms and identity labels.
3. `a9112444f626464590af400205bd39baf2cc00a6` — historical capture-blocker evidence.
4. `f72572cb9a264a23d56791185a60765213412d33` — addendum 1 guards and output revisions.
5. `336de2cdb379323827f1be90ec32c9f548447bd4` — held export and atomic import.
6. `74b24a43784c4e6e52ec5cbe599c3fc78ac36695` — schema recovery and foreign-path guards.
7. `ee7ccb765a3fb9b4d45f8f2efe5ba31952116ac7` — gated fixture and native checkpoint.
8. `4041385fc3eff39c22e2617c96f8bcb30fe036ba` — historical cold-runtime blocker evidence.
9. `d6581335a9cd2ed6382b57a49e4a330fbf013bc6` — both Git selected-chain peers.
10. `66e115a4a09d14fd045f394c9e45a1e93328f5d1` — real completed-offer restart peer.
11. `5316e2246885fc5e343c0a91df10fb9a2b712e80` — full standalone native acceptance and design §10 addendum.
12. `d0dfcd04c87be3c39c579b7aae70e0c381b868f7` — round 1 report, verification, logs and stack manifest.
13. `1cdd67af3c8daccf520a85aff2584072533456a6` — P5, fallible offer copies.
14. `090193930c3c7f14f405df5cd0a2af0700832d08` — P3, explicit accessibility filter and fallback regressions.
15. `5500d91796402b51560a0a359cb73f2032ba1f82` — P1, pending Container evaluation and children.
16. `09cc690a42bc5cd79a22b0e3acee891a929b493d` — P4, remove implementation-owned design copy.
17. `8222830b0dd4795005d5cd4674ed7117436c5c23` — P2, restart diagnostics and measured default-policy reproduction.
18. `b76c6ccbff2f68f2ca6f52b5b89ef7ec6c6d2dbf` — skip the environmental diagnostic when crossing the default target would exceed its allocation cap.
19. This final separate round 2 evidence commit — report, ledger, logs and batch 2 manifest update.

The earlier three evidence-only commits remain intact, including the two
interleaved blocker checkpoints. This round adds a fourth evidence-only commit;
integration must preserve implementation order when dropping evidence for publication.

All reviewed commits were preserved. The initial requested hard reset and clean
base confirmation were performed in the original turn; resumptions retained the
implementation history. No acquisition, offer scheduling, sharing, pushes, PRs,
tags, infrastructure changes, agent transcript reads or author/reviewer contact
were performed. Registry and visitor edits remain additive for integration.
