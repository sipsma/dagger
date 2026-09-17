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
