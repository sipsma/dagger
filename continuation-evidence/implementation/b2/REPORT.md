# Batch 2 implementation: blocked at held capture

Base: `1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f`. The first command reset the managed branch to that commit; `git log -1` and `git status` confirmed the requested base and a clean tree. Branch: `remote-cache-b2-transfer-implementer-26387947`.

This is a partial implementation, **not a completed batch-2 candidate**. Work stopped at commission step 3 because the capture design assumes a synchronization foundation that File and Directory do not provide. Steps 3–5 remain outstanding, including export/import, decode publication, schema selection, operational foreign-path guards, the fixture field and native verification. Uncommitted step-3 exploration was removed from the branch before this evidence commit.

## Coordinator decision required

Focused design `7a998854cc3b2efc7f221ffb9ec6debf5ab3d44e`, §5.1, requires live capture to refuse active bodies, use the existing nonblocking core persistence guards, and compare typed output revisions after copying. Section 4.2 assigns the typed output and payload revision writers to batch 4, with batch 2 establishing comparison hooks.

The existing Container implementation has a nonblocking persistence guard. File and Directory do not: their encoders read path/snapshot/producer fields without acquiring the producer's `LazyState.LazyMu`. The cache row's `lazyMu` does not exclude direct object-side evaluation. The common `LazyState.Evaluate` holds its own mutex across a body, independently of the cache row's attempt bookkeeping.

The focused probe pauses a body through the actual native FileSubfileLazy / DirectorySubdirectoryLazy state latch, then calls the existing public `CapturePersistedRecord` on its attached result. **Both captures succeed while that latch is held**, instead of returning `ErrPersistStateNotReady`. The existing Container direct-evaluation control passes. This proves the missing busy-body refusal; it does not claim to reproduce a torn payload or a data race.

Sources at implementation commit `85de1ed96d12d1b91589dfdba4368b7cbc512557`:

- `dagql/cache_persistence_capture.go:54`: cache row mutex and cache attempt checks.
- `core/lazy_state.go:57`: the independent body latch.
- `core/file.go:227`, `core/directory.go:242`: encoders without that guard.
- `core/container_persistence.go:100`: the existing nonblocking guard and its lock-order rationale.

Two options for the coordinator:

1. **Move the required capture synchronization into batch 2.** Add the File/Directory object guards and a typed output revision read/publication contract before held export, instrument the existing writers, and have batch 4 consume that foundation. This keeps batch 2 independently verifiable, but moves write-side synchronization scope forward from batch 4.
2. **Land that foundation as a prerequisite before resuming step 3.** Split the guards and native output revision writers from batch 4 into a preceding foundation commit, then resume this branch on it. This preserves a single owner for the synchronization work but changes the implementation dependency order.

I have not substituted repeated whole-record encoding for the specified revision contract, weakened the active-body check, or excluded File/Directory from transfer. Please resolve the scope/order through the coordinator before work resumes.

## Commits

- `de126ebe775d078a4c6e240d4d697d3bfdc6834c` — common transfer records; independent offer owners and slot primitives; collection, retention closure, prune simulation, metadata accounting and debug records; declared offer references and owner reconstruction; schema 20 / envelope 4 cut.
- `85de1ed96d12d1b91589dfdba4368b7cbc512557` — foreign codec normalization/validation and descriptor mapping; raw pending filesystem shells with the unavailable-part error; foreign mutable-state discriminators and local-source marker; variadic content-digest labels and the nine attachment sites.
- The final separate evidence commit contains this report, successful check logs, and the isolated failing probe. Both implementation commits are signed off.

Tests were added beside the first two implementation steps to check their invariants before proceeding. They are preliminary coverage, not a substitute for commission step 5.

## Verification

All tests followed the fully read engine-debugging skill, with narrow sequential package selections and logs in `/tmp`.

| Evidence | Command / result | What it establishes |
| --- | --- | --- |
| `step1-owners.log` | `go test ./dagql -run '^(TestValueTransferOfferOwners|TestValueTransferOwnerPersistence|TestCapturePersistedRecordValuesAndColdCopies|TestCacheMetadataEstimateFormula|TestCachePruneMetadataEstimateCreditsCollectedDependencyClosure)$' -count=1` — pass | Independent slot/active owner holds, cycle refusal, direct-only resource propagation, owner persistence/collection, active-owner prune protection, and selected existing capture/accounting controls. |
| `step2-foreign.log` | `go test ./core -run '^TestValueTransfer' -count=1` — pass | Foreign-form refusals and normalization; File descriptor mapping; pending shell metadata and explicit demand error. The part test checks descriptors, **not real chain bytes**. |
| `step2-native.log` | `go test ./core -run '^(TestFilesystemCompletedProducerPersistence|TestPersistedRelocatedRowsSurviveWorkerSaveAndReopen|TestContainerPersistedPartsIgnorePendingAccessorSeeds|TestPersistedCoreStorageRolesAreClassified)$' -count=1` — pass | Selected existing producer, storage-role, relocation and local persistence controls. |
| `step2-schema-build.log` | `go build ./core/schema` — exit 0 | Build compatibility of the widened interface and schema attachment sites. |
| `capture-guard-gap.log` | `go test ./core -run '^(TestB2CaptureGuardGap|TestCapturePersistedContainerDirectEvaluation)$' -count=1 -v` — expected probe failure | File and Directory accept capture while their native body latch is held; Container's existing refusal control passes. |

`reproduce-capture-guard-gap.sh` installs the preserved probe temporarily and removes it on exit. Its source is `capture-guard-probe.go.txt`, so the deliberately failing probe is not part of ordinary package tests. The script is expected to exit nonzero on this tip.

The full requested race selection, schema/server reader cases, real selected-chain storage tests and `RemoteCacheTransferSuite/TestSchemaRecovery` have **not** been completed or claimed. No engines were started for native cross-engine verification.

## Persistence consequence and review boundary

Schema 20 and envelope 4 deliberately do not migrate schema-19/envelope-3 checkpoints. Starting an engine with these changes against the previous format cold-starts the cache under its existing reset policy. Bundle version 1 records are defined; the live bundle APIs are not committed yet.

Reviewers should inspect offer ownership versus direct dependency requirements, collection/prune accounting, restored offer records, strict foreign-form validation, and the identity attachment expressions. Boot settlement of a completed output beside a redundant offer, complete desired-role/decode publication, full capture comparison hooks, and the rest of the commission still require implementation and verification. The local-source marker alone is not the operational foreign-path boundary; that belongs to the outstanding step 4.

No acquisition, offer scheduling, sharing, pushes, pull requests, tags, infrastructure changes, agent transcript reads or author/reviewer contact were performed.
