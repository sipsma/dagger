# Batch 7, author B: every test invocation

This replaces the ledger tables in `REPORT-AUTHOR-B.md`, which abbreviated commands and gave ranges (slice 1 confirmation, S4). It is written from the session's own record of each command; nothing was rerun to make it. All invocations: uid 1000, no sudo, no mount namespace, default parallelism, no engine. "Test" is `-timeout`; "process" is the bound on the whole command, compile included. "Wall" is `time`'s real seconds where the command was timed, the test binary's own seconds (marked t) where only that was printed, and "unknown" otherwise. `SIX` is `./dagql ./core ./core/schema ./engine/engineutil ./engine/engineutil/imageexport ./engine/snapshots`; `SEVEN` adds `./engine/server`. Every `go test` carried `-count=1` unless a count is shown.

## G5 measurement (working tree: base plus the experiment patch as it grew)

| # | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- |
| G0 | `go test -run 'XXX_NONE' SIX -timeout 60s` (compile only) | 60 / 900 s | ok, no tests | 42.7 |
| G1 | `go test -json -timeout 240s SIX` | 240 / 330 s | 12 of 79 store tests pass | 17.9 |
| G2 | same | 240 / 330 s | 48 of 79 | 41.1 |
| G3 | `go test -json -timeout 120s ./core` | 120 / 200 s | core 22 of 38 | 31.8 |
| G4 | `go test -json -timeout 180s ./core ./core/schema` | 180 / 260 s | 69 of 79 | 28.3 |
| G4b | `go test -timeout 60s -run '^TestHTTPLazyOperationWriter$' ./core` | 60 / 200 s | ok | 1.1 t |

## Conversions

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 1 | `e4b65210ea` contents | `go test -json -timeout 60s ./dagql ./engine/snapshots ./engine/engineutil ./engine/engineutil/imageexport` | 60 / 240 s | 911 pass, 1 skip | 36.5 |
| 1a | working tree | `go test -timeout 60s -run '^TestLazyEvaluatedFilesystemClones$' ./core` | 60 / 300 s | ok | 0.2 t |
| 1b | working tree | `go test -timeout 60s -run '^TestBuiltinLazyOperationEvaluate$' ./core`, three times | 60 / 300 s each | build failure (my unused variable); FAIL, `AsPatch` mounts for scratch too; ok after the rewrite | 0.3 t |
| 1c | working tree | `go test -timeout 60s -run '^TestValueTransferPartsSelectedChain$' ./core`, twice | 60 / 300 s each | build failure; ok | 0.4 t |
| 1d | working tree | `go test -timeout 60s -run '^(TestLazyStoredResultsWithoutBacking\|TestBuiltinMetadataSelectors)$' ./core/schema` | 60 / 300 s | ok | 0.5 t |
| 2 | `9ea94cb1ef` contents | `go test -json -timeout 60s SIX` | 60 / 300 s | 2698 pass, 5 skips, 0 fail | 22.6 |

## Reselect rule and F3

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 2a | `e094252906` contents | `go test -timeout 60s ./dagql` | 60 / 300 s | ok | 45.6 (18.0 t) |
| 2b | `f9db98a420` contents | `go test -timeout 60s -run '^(TestPartProgressRule\|TestPartVersionRefusal\|TestCommitReadyPartChangedRefusals\|TestPublishEvaluatedPartsStopsWithoutProgress\|TestPublishEvaluatedPartsSurvivesContention\|TestDemandPartStopsWithoutProgress)$' ./dagql`, twice (second `-v`) | 60 / 300 s each | ok | 0.2 t |
| 2c | same, rule disabled by a temporary `return nil` | `go test -timeout 60s -run '^(TestPublishEvaluatedPartsStopsWithoutProgress\|TestDemandPartStopsWithoutProgress)$' ./dagql` | 60 / 300 s | FAIL as intended: both spin to their 5 s deadline; file restored | 36.0 (10.3 t) |
| 3 | `f9db98a420` contents | `go test -json -timeout 90s ./dagql ./core ./core/schema ./engine/server` | 90 / 400 s | 2898 pass, 5 skips, 0 fail | 44.0 |
| 3r | same | `go test -race -timeout 120s -run '^(TestPartProgressRule\|TestPartVersionRefusal\|TestCommitReadyPartChangedRefusals\|TestPublishEvaluatedParts\|TestDemandPartStopsWithoutProgress\|TestPartReselectWatch\|TestPartRefusalNamesItsSite)' ./dagql` | 120 / 600 s | ok | 65.6 (2.7 t) |
| 3a | `29b836c4e3` contents | `go test -timeout 60s -v -run '^TestHTTPLazyOperationWriter$' ./core` | 60 / 300 s | ok | 7.9 |
| 3b | same with the expected mode changed to 0700 | same command without `-v` | 60 / 300 s | FAIL as intended: the failing child fails the parent; file restored | unknown |

## The scan defect

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 4 | `6b7c8fae91` | `go test -json -timeout 60s SEVEN` | 60 / 400 s | FAILED: `core` `TestPartInlineAddress/concurrent`; 2999 pass | 26.8 |
| 4a | `6b7c8fae91` | `go test -count=40 -timeout 170s -run '^TestPartInlineAddress$' ./core` | 170 / 400 s | 2 of 40 fail | 67.8 |
| 4b | `6b7c8fae91` with `dagql` checked out at `9ea94cb1ef` | same with `-count=60` | 170 / 500 s | 3 of 60 fail; tree restored | 135.6 |
| 4c | `6b7c8fae91` plus a temporary stack in the error | `go test -count=60 -timeout 170s -run '^TestPartInlineAddress$/^concurrent$' ./core` | 170 / 500 s | 2 failures with stacks; file restored | unknown |
| 4d | working tree | `go test -timeout 60s -run '^TestPartSourceScanSkipsUnreadyCandidate$' ./dagql` | 60 / 300 s | FAIL as intended, before the fix | 0.4 t |
| 4e | working tree | `go test -timeout 60s -run '^(TestPartSourceScanSkipsUnreadyCandidate\|TestPartSourceScanSkipsUnreadyDonor)$' ./dagql`, three times | 60 / 300 s each | build failure (missing import); both FAIL before the fix; ok after it | 0.1 t |
| 4f | `959054a2e0` contents | `go test -count=80 -timeout 170s -run '^TestPartInlineAddress$/^concurrent$' ./core` | 170 / 500 s | 80 of 80 pass | 55.1 |
| 5 | `959054a2e0` | `go test -json -timeout 60s SEVEN` | 60 / 400 s | 3003 pass, 5 skips, 0 fail | 61.6 |

## Item 3, F2 and the slice 1 corrections

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 5a | working tree | `go test -timeout 60s -run '^TestSnapshotSharingPublicationTriggerFaults$' ./dagql`, four times | 60 / 300 s each | three FAILs of the test as I was writing it (members read after release; a request frame that hit the pair; undrained pass-start reports), then ok | 0.1 t |
| 5b | working tree | same with `-count=30 -timeout 120s` | 120 / 300 s | ok | 1.3 t |
| 5c | working tree | `go test -timeout 60s -run '^(TestSnapshotSharingCongruenceRepairTrigger\|TestSnapshotSharingEnqueueDoesNotWaitForTheWorker)$' ./dagql` | 60 / 300 s | ok | 0.2 t |
| 5d | `6333323d56` contents | `go test -count=20 -timeout 120s -run '^(TestSnapshotSharingCongruenceRepairTrigger\|TestSnapshotSharingEnqueueDoesNotWaitForTheWorker\|TestSnapshotSharingPublicationTriggerFaults)$' ./dagql` | 120 / 300 s | ok | 1.2 t |
| 5e | working tree | `go test -timeout 90s -run '^TestSnapshotSharingMarkedDecodeOfModuleAncestry$' ./engine/server` | 90 / 400 s | ok | 0.2 t |
| 5f | `13cb401c51` contents | `go test -timeout 90s -run '^(TestSnapshotSharingMarkedDecodeOfModuleAncestry\|TestSnapshotSharingMarkedDecodeOfEncodedService)$' ./engine/server` | 90 / 400 s | ok | 0.2 t |
| 5g | working tree | `go test -timeout 60s -run '^(TestInstallChainPartRecordsOnce\|TestInstallChainPartStopsWithoutProgress)$' ./dagql` | 60 / 300 s | ok | 0.8 t |
| 5h | `6e2d9a780c` contents | `go test -timeout 60s -run '^TestHTTPLazyOperationWriter$' ./core` | 60 / 300 s | ok | 1.5 t |
| 6 | `af2ddb4e36` | `go test -json -timeout 60s ./dagql ./core ./engine/server` | 60 / 180 s | 2392 pass, 5 skips, 0 fail | 59.5 |

## Integration branch `b7-integration-author-b`

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 7 | `5f7537bfce` contents | `go test -json -timeout 90s SEVEN` | 90 / 210 s | FAILED: `dagql` `TestSnapshotSharingCancelAfterPublicationDeliversReceipt`; 3031 pass | 89.1 |
| 7a | author A's `7356f6d6bc`, detached | `go test -count=5 -timeout 120s -run '^TestSnapshotSharingCancelAfterPublicationDeliversReceipt$' ./dagql` | 120 / 300 s | 5 of 5 fail | 52.1 t |
| 7b | working tree | same with `-count=10` | 120 / 300 s | ok | 2.3 t |
| 7c | `1e20b211c6` | `go test -timeout 90s ./dagql` | 90 / 210 s | ok | 18.3 |
| 7d | working tree | `go test -timeout 90s -run '^TestSnapshotSharingDecodeLeaderOrders$' ./engine/server`, twice | 90 / 400 s each | build failure (embedded field named like a method); ok | 0.2 t |
| 7e | working tree | same with `-count=20 -timeout 120s` | 120 / 300 s | ok | 2.5 t |
| 7f | scratch test, never committed | `go test -timeout 60s -run '^TestB7Repro' -v ./core`, six times, two of them with temporary prints in `dagql` that were restored | 60 / 300 s each | one FAIL (no pass ran: the rows were not yet equivalent), one FAIL from my scratch nil call, the rest ok with the logs quoted in the report | 0.2 to 10.4 t |
| 7g | working tree | `go test -count=10 -timeout 60s -run '^TestSharingOwedBookkeepingIsPaidByAnExactDemand$' ./core` | 60 / 300 s | ok | 1.3 t |
| 8 | `37518e0769` | `go test -json -timeout 90s SEVEN` | 90 / 210 s | 3041 pass, 5 skips, 0 fail | 90.5 |
| 8a | working tree | `go test -timeout 60s -run '^TestRenewalChainControls$/^a_key-only' ./dagql` | 60 / 300 s | PANIC as intended, before the fix: `assignment to entry in nil map`, `cache_part_content.go:330` | unknown |
| 8b | `e844245c8b` contents | `go test -timeout 90s -run '^(TestRenewal\|TestPartContent)' ./dagql` | 90 / 300 s | ok | 3.3 t |
| 9 | `e844245c8b` | `go test -timeout 90s ./dagql` | 90 / 210 s | ok | 16.0 (12.1 t) |

The five skips in every whole-package run: `core` `TestGitLazyOperationsEvaluate`, `TestGitLazyOperationsRemoteEvaluate`, `TestGitBundleLazyOperationEvaluate`, `TestValueTransferPartsGitTrees` (folded into `TestGitTrees`, interim); `dagql` `TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases` (a TODO at the base).

Process bounds before run 6 were looser than the rule asks; from run 6 on they are the test timeout plus at most 120 s for whole-package runs. Narrow selections kept a 300 s process bound because a cold `core` or `engine/server` test build alone can take over a minute; I should have tightened those to the same form and will.

## After the renewal fix: integration again, the clone defect, the models

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 10a | merge of A's `5080ec1468` with my test repair reverted (`0b934532bc`) | `go test -count=10 -timeout 120s -run '^TestSnapshotSharingCancelAfterPublicationDeliversReceipt$' ./dagql`, twice: the first time my revert had not applied, the second it had | 120 / 300 s each | ok both; the second is the one that counts: batch 6's test unchanged, on A's fix `96610fe471` alone | 2.2 t |
| 10b | scratch test, never committed | `go test -timeout 60s -run '^TestB7CloneRepro$' -v ./core` | 60 / 200 s | reproduced: `file must be materialized, got lazy *core.FileRestoreLazy` | 0.2 t |
| 10c | working tree, before the fix | `go test -timeout 60s -run '^TestPartAcquiredValuesCloneForContainers$' ./core` | 60 / 200 s | FAIL as intended, File and Directory | 0.3 t |
| 10d | working tree, after the fix | same | 60 / 300 s | ok | 0.3 t |
| 10 | working tree with the fix | `go test -json -timeout 90s ./dagql ./core ./core/schema ./engine/server` | 90 / 210 s | FAILED: my own `TestSharingOwedBookkeepingIsPaidByAnExactDemand`, on A's new `share-skipped` event; 2943 pass | 40.3 |
| 10e | working tree | `go test -count=20 -timeout 120s -run '^(TestSharingOwedBookkeepingIsPaidByAnExactDemand\|TestPartAcquiredValuesCloneForContainers)$' ./core` | 120 / 300 s | ok | 5.5 t |
| 11 | `a4fb30b4dd` | `go test -timeout 90s ./core` | 90 / 210 s | ok | 14.5 t |

Model runs. All local: `timeout <T>s java -Xmx8g -XX:+UseParallelGC -cp ~/tla/tools/tla2tools.jar tlc2.TLC -workers auto -deadlock -metadir <scratch> -config <cfg> <module>.tla`, the jar byte-identical to the runner's pinned TLC 1.7.4. The process bound of each was the `timeout` plus 20 to 40 s. No engine.

| # | What | Timeout | Result | Wall |
| --- | --- | --- | --- | --- |
| M1 | `remote_parts`, first run | 180 s | pass, 24,742 states | 2.3 s |
| M2 | its two faults and three probes | 60 s each | four as named; **`fault_certify_sibling` passed, which proves nothing**: the fault could only fire on a finished operation | 1 to 2.4 s each |
| M3 | `remote_parts` and `fault_certify_sibling` after the fault was made to fire on any successful group ending | 120 s each | pass, 26,270 states; `ServedOutputIsComplete` violated | 2.2 s, about 2 s |
| M4 | `remote_owners`, first run | 180 s | **`OwnRequirementIsDirectClosure` violated**: the model did not cascade requirement growth to dependants, the code does | under 2 s |
| M5 | `remote_owners` after the cascade | 180 s | pass, 3,092 states | 1.7 s |
| M6 | its three faults and three probes | 60 s each | all as named | about 1 s each |
| M7 | the two `remote_parts` faults again after the last edit | 60 s each | as named | about 2 s each |
| M8 | sizing probe, not kept: `remote_parts` with three tasks and two cancellations | 180 s | pass, 855,547 states | 6.6 s |
| M9 | `remote_sharing`, `remote_sharing_decoded` | 180 s each | pass, 1,293 and 672 states | 1.1 s, 1.2 s |
| M10 | sharing's five faults and five probes | 60 s each | all as named | about 1 s each |
| M11 | `remote_checkpoint`, first run | 180 s | **`DesiredRolesStayProtected` violated**: a modeling error, the restart restored a checkpoint older than the receiver's collection; the checkpoint is now the clean shutdown's | under 2 s |
| M12 | `remote_checkpoint` after that | 180 s | pass, 2,134 states | 1.5 s |
| M13 | its three faults and four probes | 60 s each | all as named | about 1 s each |
| M14 | all 33 registered configurations against `expectedOutcome` | 60 s each, 600 s process | 0 mismatches | 49 s total |
| M15 | `remote_parts` and the two new progress-rule configurations | 120 s each | pass 26,270; `NoProgressIsUnreachable` violated; pass 47,466 | about 2 to 3 s each |
| M16 | all 35 configurations against their expectations, after the B7 edits | 60 s each, 600 s process | 0 mismatches | 40 s total |

## Packaging and the export check

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| P1 | each of the six `b7-packaging/remote-cache/*` heads, detached | `go build ./...` then `go vet ./dagql ./core ./core/schema ./engine/snapshots` | no tests / 540 s and 300 s | all six ok | 54 to 101 s each |
| 12 | working tree on `ad32ac7066` | `go test -count=10 -timeout 120s -run '^TestExportDuringAnActiveTaskIsNotReady$' ./dagql` | 120 / 300 s | ok | 0.3 t |

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 13 | `ee7d756b85` plus the deletion of the folded git tests | `go test -json -timeout 90s ./dagql ./core ./core/schema ./engine/server ./engine/snapshots ./engine/engineutil ./engine/engineutil/imageexport` | 90 / 210 s | 3050 pass, 1 skip (the base's TODO), 0 fail | 85.0 |
| 14 | working tree | `go test -timeout 60s -run '^TestTransferConstructorContentUnitesDownstreamCall$' ./dagql` | 60 / 300 s | ok | 0.3 t |
| 15 | working tree on `f6d17a03d5` | `go test -count=20 -timeout 120s -run '^TestSnapshotSharingPrefixCarriedOverRefusedAddress$' ./dagql` | 120 / 300 s | ok | 0.5 t |
| M17 | the 21 `remote_sharing*` and `remote_checkpoint*` configurations after the D11 edits | `timeout 60s` each, 600 s process | 0 mismatches; `remote_checkpoint` 2,134 states, the new `fault_drop_operation` violates `OperationRetained` | 34 s total |

Boot wipe investigation. All on the working tree of `770e9de2b5` plus the uncommitted scratch file `core/zz_b7_wipe_repro_test.go`.

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 16 | scratch, shared imported Directory, four variants (encoded, evaluated, owner attach fails, the same with collection after close) | `go test ./core -run 'TestB7WipeRepro$' -timeout 90s -count=1`, several invocations while the variants were written | 90 / 300 s | ok every time: no reset, so the sharing and owed paths do not cause the wipe | unknown |
| 17 | scratch rewritten: imported `CacheVolume`, snapshot initialised on B | same command | 90 / 300 s | FAIL as intended: `import_failure`, `attach imported result 1 owner lease "snapshot": … not found` | 27 s with build, test 0.08 s |
| 18 | scratch, three kinds (cache volume, git mirror, filesync mirror) | same command | 90 / 300 s | FAIL as intended, all three; log `boot-wipe/repro-three-kinds.log` | 20 s, tests 0.27 s |
| 19 | scratch, only the collection before `Close` | same command | 90 / 300 s | FAIL as intended, all three: the running engine's collection takes the snapshot | unknown, tests 0.59 s |
| 20 | scratch, cache volume case driven through the production `Container.WithMountedCache` | `go test ./core -run 'TestB7WipeRepro$/cache-volume' -timeout 90s -count=1` | 90 / 210 s | FAIL as intended, same boot error | 29 s with build, test 0.46 s |
| 21 | working tree with the fix, first version of the real test | `go test ./core -run '^TestImportedBackingSnapshotIsOwnedByItsRow$' -timeout 90s -count=1` | 90 / 210 s | FAIL, my test's fault: it used a released session's context and the new sync refused it | 27 s with build |
| 22 | the same with a context per session | same command | 90 / 210 s | ok | 22 s with build, test 0.6 s |
| 23 | the same with the lease sync disabled by a temporary edit, restored and diffed afterwards | same command | 90 / 210 s | FAIL as intended, all three kinds, at the first mount after the collection; `boot-wipe/test-without-sync.log` | 22 s with build |
| 24 | working tree with the fix | `go test -timeout 90s -count=1 ./core ./core/schema` | 90 / 210 s | `core/schema` ok; `core` FAIL: `TestContainerMetadataOnlyMountMutationParts` mounts an initialised volume with no Query in the context and the helper looked the Query up first | 30 s |
| 25 | the Query lookup made lazy; this is the tree committed as `16786b5fe3` | `go test -timeout 90s -count=1 ./core` | 90 / 210 s | ok. `core/schema` not rerun: its only change is the one call in `host.go`, vetted | 33 s |

Slice 3, on the integrated tip `0fca47daa7` (merge of A's `3af661532a`), clean tree.

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 26 | `0fca47daa7` | `go test -json -timeout 90s ./dagql ./core ./core/schema ./engine/server ./engine/snapshots ./engine/engineutil ./engine/engineutil/imageexport` | 90 / 210 s | 3057 pass, 1 skip (the base's TODO), 0 fail; `logs-author-b/slice3/seven-packages.txt` | 47.2 |
| 27 | `0fca47daa7` | `go test -race -timeout 180s -run '^(TestPartProgressRule\|TestPartVersionRefusal\|TestCommitReadyPartChangedRefusals\|TestPublishEvaluatedParts\|TestDemandPartStopsWithoutProgress\|TestInstallChainPart\|TestPartReselectWatch\|TestPartRefusalNamesItsSite\|TestPartSourceScan\|TestPartInlineAddress\|TestSnapshotSharing\|TestExportDuringAnActiveTask\|TestTransferFixture)' ./dagql` | 180 / 300 s | ok | 68.4 (6.0 t) |
| 28 | `0fca47daa7` | `_EXPERIMENTAL_DAGGER_RUNNER_HOST=container://remote-cache-b7-engine timeout 1260 dagger api call engine-dev test --pkg ./core/integration --run='^TestRemoteCacheTransferSuite/(…sixteen names…)$' --test-verbose --timeout=15m --env-file=file:/tmp/b7/dump-5m.env` (full selection in the log) | 15 m / 1260 s | pass, exit 0; load 10.4, 24.4, 37.3 at start, 36.6, 53.8, 47.8 at end | 822 |
| P7 | `b7-packaging/remote-cache/b7-verification` | none: not checked out; its tree is the tree of rows 26 to 28 without evidence, verified by `git diff` | – | tree equal | – |

Slice 3 generic review G1. Working tree on `001cfd02da` plus the fix.

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 29 | fix and new test | `go test ./core -run '^TestImportedBackingSnapshot' -timeout 60s -count=1` | 60 / 210 s | ok | 22.5 (0.65 t) |
| 30 | the discard disabled by a temporary edit, restored and diffed afterwards | `go test ./core -run '^TestImportedBackingSnapshotIsDroppedWhenItsOwnerAttachFails$' -timeout 60s -count=1` | 60 / 210 s | FAIL as intended, all six schedules: the value still reports the link; `boot-wipe/test-without-discard.log` | 20.7 |
| 31 | fix restored, assertion tightened to the fixture's fault text; this is `5d3ee071c7` | `go test ./core -run '^TestImportedBackingSnapshot' -timeout 60s -count=1` | 60 / 210 s | ok | 22.6 (1.4 t) |
| 32 | `5d3ee071c7` | `go test -timeout 90s -count=1 ./core ./core/schema` | 90 / 210 s | ok, ok | 36.2 |
| P7b | `b7-packaging/remote-cache/b7-verification` rebuilt at `5d3ee071c7` | `package_b7.py` | – | 88 kept, 27 omitted, head `2b34e9e3e7`, tree equal | – |

Slice 3, one step per value, and the final integration.

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 33 | lock and new test, first version | `go test -race ./core -run '^TestImportedBackingSnapshotConcurrentFirstUses$' -timeout 120s -count=5` | 120 / 300 s | FAIL: a data race in my test helper, which read a mirror's snapshot without its lock; not production | 53 |
| 34 | test helper reads under the value's lock | same | 120 / 300 s | ok | 44.6 (3.6 t) |
| 35 | the helper's lock disabled by a temporary edit, restored and diffed afterwards | same | 120 / 300 s | FAIL as intended: a successful caller has no snapshot (first run of this: a nil dereference in the test at the same point; the test now reports it as an error); `boot-wipe/test-without-lock.log` | 42.7 |
| 36 | lock restored; this is `cfa148371c` | `go test -race ./core -run '^TestImportedBackingSnapshot' -timeout 120s -count=1` then `go test -timeout 90s -count=1 ./core ./core/schema` | 120 / 300 s; 90 / 210 s | ok; ok, ok | 44.0; 35.2 |
| 37 | `90e34e09a3` (A's `9abe5072d5` merged) | `go test -json -timeout 90s ./dagql ./core ./core/schema ./engine/server ./engine/snapshots ./engine/engineutil ./engine/engineutil/imageexport` | 90 / 210 s | 3068 pass, 1 skip (the base's TODO), 0 fail; `engine/snapshots`, `engine/engineutil` and `imageexport` were go's cached results, unchanged since row 26; `logs-author-b/slice3-final/seven-packages.txt` | 41.7 |
| 38 | `90e34e09a3`, clean tree | the row 28 command, `_EXPERIMENTAL_DAGGER_RUNNER_HOST=container://remote-cache-b7-engine timeout 1260 dagger api call engine-dev test … --timeout=15m` | 15 m / 1260 s | **FAIL, two bodies**: `TestSharingDonorRestart/DonorAfterImport` (two-minute receipt wait expired) and `TestWorkspaceCapture` (nested engine restart exited 1 at start); 82 of 84 leaves pass; load 11.4, 12.4, 20.4 at start, 31.4, 63.5, 50.9 at end; `logs-author-b/slice3-final/` | 885 |
| P7c | `b7-packaging/remote-cache/b7-verification` rebuilt at `90e34e09a3` | `package_b7.py` | – | 90 kept, 28 omitted, head `bc905aed16`, tree equal; earlier packaged SHAs unchanged | – |

Final rerun, ordered by the Coordinator.

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 39 | `92b8057912` (`90e34e09a3` plus the test-only start probe `92b8057912`), clean tree | the row 28 command | 15 m / 1260 s | **pass**, exit 0, all sixteen; load 13.7, 11.5, 26.1 at start, 29.1, 69.8, 58.6 at end; the probe was not reached; `logs-author-b/slice3-final/native-sixteen-92b8057912.log` | 943 |
| P7d | `b7-packaging/remote-cache/b7-verification` rebuilt at `92b8057912` | `package_b7.py` | – | 91 kept, 28 omitted, head `110ec17723`, tree equal; earlier packaged SHAs unchanged | – |

After the rebase review.

| # | Tree | Command | Test / process | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 39a | working tree with the ownership-count fix (`fe963009bf`) | `go test ./dagql -run '^(TestPartSourceSelectionLaterRoute\|TestPartSessionlessOwnSubset\|TestPartReadyRevalidationAndCanceledFinish\|TestPartReadyPreparationBoundaries\|TestPartDecisionPreparationArrival\|TestPartDecisionInlineAdmissionAndPendingSync\|TestPartDecisionOfferAfterRunning)$' -timeout 60s -count=5 -v` | 60 / 210 s | ok, 95 passes | 10.2 (5.7 t) |
| 40 | `aaef58c1e4` (A's `d168c733ac`, `5272f3d454`, `e77b89c743` merged) | `go test -json -timeout 90s -count=1 ./dagql ./core ./core/schema ./engine/server ./engine/snapshots ./engine/engineutil ./engine/engineutil/imageexport` | 90 / 210 s | 3072 pass, 1 skip (the base's TODO), 0 fail; `logs-author-b/slice3-final/seven-packages-aaef58c1e4.txt` | 43.6 |
| P7e | `b7-packaging/remote-cache/b7-verification` rebuilt at `aaef58c1e4` | `package_b7.py` | – | 95 kept, head `71188af8c6`, tree equal; earlier packaged SHAs unchanged | – |
