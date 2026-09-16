# Batch 2 standalone implementation

Branch: `remote-cache-b2-transfer-implementer-26387947`.
Base: `1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f`.
Implementation tip: `5316e2246885fc5e343c0a91df10fb9a2b712e80`.

Steps 1–5 are implemented under coordinator addenda 1 and 2. The full native
standalone acceptance suite passes with B's runtime prepared through normal
`AsModule` before import, without serving its schema or entering `report`.
The isolated confirmation of the previously failing restart case also passes.
The fully cold order remains an explicit skipped test naming addendum 2 and
belongs to batch 4/7 acceptance. Batch 1 has not been merged into this branch.

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
transport. A separate real-store restart peer verifies that completed local
snapshot ownership survives restart, redundant offer retirement, transfer-pin
removal and GC. Exact commands and outcomes are in [VERIFICATION.md](VERIFICATION.md).

The native fixture now uses the same GC bounds as the existing persistence suite.
An earlier run using default limits lost a saved row at restart; it is retained
in the verification history, not silently treated as a pass. Its precise cause
was not established; both runs with the final fixture bounds pass. No production
lookup, acquisition or persistence behavior was changed during addendum 2.

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
12. This separate final evidence commit — report, verification, logs and stack manifest.

All reviewed commits were preserved. The initial requested hard reset and clean
base confirmation were performed in the original turn; resumptions retained the
implementation history. No acquisition, offer scheduling, sharing, pushes, PRs,
tags, infrastructure changes, agent transcript reads or author/reviewer contact
were performed. Registry and visitor edits remain additive for integration.
