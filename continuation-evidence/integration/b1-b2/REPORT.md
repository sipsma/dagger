# Batch 1 + batch 2 integration

Integration replay is complete; verification is **not green**. All required commands ran. Build, both batches' race selections, all four dev-engine selections, selected-chain peers and the real-store restart peer passed. Vet and the unfiltered core package failed; details and unchanged logs follow. The additional whitespace check flags inherited batch 2 evidence logs.

Branch: `remote-cache-integration-b1-b2-36d54e12`.
Reviewed foundation: `1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f`.
Batch 1 head, preserved as an exact ancestor: `a88e0e0cdde5383d085347c55288ff84651239e2`.
Original batch 2 head: `aa7f3fa330faf7ac95217665a61edcb95b633768`.
Integrated implementation head tested below: `5483cfc495f93d4777104bda7edede27e33607c9`.
This report and its supporting evidence are added by one final separate signed-off commit above that head.

Commission: `0cc32bd040cd541eba40a4cb8aff268dd5493262:continuation-evidence/implementation-commissions/INTEGRATION-B1-B2.md`.
The commission, coordinator stack manifest, and full engine-debugging skill were read before verification.
The requested initial hard reset to the reviewed batch 1 tip completed successfully.
Batch 2 was then replayed on the managed branch with:

```sh
git reset --hard aa7f3fa330faf7ac95217665a61edcb95b633768
git -c rebase.updateRefs=false rebase --reapply-cherry-picks --empty=keep --onto a88e0e0cdde5383d085347c55288ff84651239e2 1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f
```

The rebase paused twice for the four file conflicts below; both continuation commands used `GIT_EDITOR=true git -c rebase.updateRefs=false rebase --continue`. The initial rebase and first continuation exited 1 at conflicts; the final continuation exited 0. No commits were skipped or squashed.

## Conflicts and resolutions

| Original batch 2 commit | File / location | Resolution |
| --- | --- | --- |
| `85de1ed96d12d1b91589dfdba4368b7cbc512557` | `core/http.go`, `HTTPState.EncodePersistedObject` | Kept batch 1's payload capture under `state.mu` and marshaling of that saved payload after unlocking. Added batch 2's `Form` to the saved payload while locked, deriving snapshot presence from the captured `snapshotID` (the same condition that builds the snapshot links). This preserves consistent HTTP metadata and the foreign backing form. |
| `f72572cb9a264a23d56791185a60765213412d33` | `core/file.go`, `FileBlobLazy.Evaluate` | Retained batch 1's `newRef = nil` ownership transfer after commit; replaced the two accessor writes with batch 2's `SetPath` and `SetSnapshot`. |
| `f72572cb9a264a23d56791185a60765213412d33` | `core/git_bundle.go`, `importGitBundleInto` | Kept batch 1's extracted destination-based body, cleared services and error-only return. Applied batch 2's versioned setters to `dst` instead of reconstructing the old return-value Directory. |
| `f72572cb9a264a23d56791185a60765213412d33` | `core/git_local.go`, `LocalGitRepository.cleanedInto` | Kept batch 1's extracted destination-based body, cloned services, ownership handoff, `(false, nil)` return and following index-cleanup helper. Applied batch 2's versioned setters to `dst`. |

Original conflict hunks: [HTTP](conflicts/85de1ed96d-http.diff), [filesystem writers](conflicts/f72572cb9a.diff).
No other manual code edits were made. Directory/File switches and codec/visitor registrations merged automatically; the combined label audit found no cases missing from either input (48 Directory case labels, 19 File case labels, 78 visitor keys and 53 family names). See [registry audit](registry-audit.txt).
`core/container_persistence.go`, including the batch 2 Container encoder guard, is byte-identical to the original batch 2 head.

## History and range-diff

```sh
git range-diff 1ca9f28a..aa7f3fa330 a88e0e0cdd..5483cfc495f93d4777104bda7edede27e33607c9
```

Exit status: 0. [Complete range-diff](range-diff.txt).
All 21 batch 2 commits map one-to-one in order: 19 `=` entries and two `!` entries, solely the conflict-bearing commits `85de1ed96d` and `f72572cb9a`. No added or dropped entries.
Every commit message, sign-off, author identity and author date matches its original exactly. Batch 1 is the unchanged ancestor. [Commit mapping](commits.json), [history audit](history-audit.txt).
All five named batch 2 evidence-only commits remain in their original positions, and both batch 1 evidence commits remain in ancestry. The reviewed design-copy deletion `09cc690a42` is retained as `56e40116ee`; it was not classified by its message prefix.

## Verification

All commands ran sequentially at the integrated implementation head, with no source edits during testing. `GOFLAGS` was empty. The privileged runner preserves the existing Go toolchain/dependency cache and gives each test binary a private mount namespace. The three-package run is unfiltered; `-p=1` only serializes packages. No `go test ./...` was run. Dev-engine commands use the installed v1 CLI and its default progress UI.
The batch 1 race selection includes the separate writer selection from its ledger. The batch 2 race selection is the expanded round 2 selection. Selected-chain and real-store restart peers are separate privileged runs. No failed check was omitted or replaced with a narrower passing selection.

[Environment](environment.txt), [exact sequential runner](run-verification.py), [machine-readable results](results.json).

Expected combined behavior: eager outputs retain batch 1's saved producers and dependency ownership; batch 2 transfers validated metadata and selected snapshot chains, preserves independent offer ownership across restart, and recovers schemas with the existing prepared-runtime acceptance boundary. This integration leaves acquisition and the Container encoder-guard change to the later batch.

| Check | Exit status | Seconds | Output and invocation |
| --- | ---: | ---: | --- |
| `build` | 0 | 49.066 | [log](logs/build.log), [command](logs/build-command.txt) |
| `vet` | 1 | 16.819 | [log](logs/vet.log), [command](logs/vet-command.txt) |
| `unfiltered` | 1 | 93.712 | [log](logs/unfiltered.log), [command](logs/unfiltered-command.txt) |
| `b1-race-compile` | 0 | 54.297 | [log](logs/b1-race-compile.log), [command](logs/b1-race-compile-command.txt) |
| `b1-race` | 0 | 1.662 | [log](logs/b1-race.log), [command](logs/b1-race-command.txt) |
| `b1-race-writer` | 0 | 3.229 | [log](logs/b1-race-writer.log), [command](logs/b1-race-writer-command.txt) |
| `b2-race` | 0 | 26.846 | [log](logs/b2-race.log), [command](logs/b2-race-command.txt) |
| `b2-selected-chains` | 0 | 5.19 | [log](logs/b2-selected-chains.log), [command](logs/b2-selected-chains-command.txt) |
| `b2-offer-restart` | 0 | 4.617 | [log](logs/b2-offer-restart.log), [command](logs/b2-offer-restart-command.txt) |
| `b1-native-git` | 0 | 154.601 | [log](logs/b1-native-git.log), [command](logs/b1-native-git-command.txt) |
| `b1-native-http` | 0 | 55.083 | [log](logs/b1-native-http.log), [command](logs/b1-native-http-command.txt) |
| `b1-native-restart` | 0 | 97.844 | [log](logs/b1-native-restart.log), [command](logs/b1-native-restart-command.txt) |
| `b2-native-schema` | 0 | 287.664 | [log](logs/b2-native-schema.log), [command](logs/b2-native-schema-command.txt) |
| `diff-check` | 2 | 0.036 | [log](logs/diff-check.log), [command](logs/diff-check-command.txt) |

### Exact commands

All commands below ran from the repository root. Per-command start/finish times are in the invocation records and `results.json`.

```sh
# build: exit 0
go build ./...

# vet: exit 1
go vet ./core/... ./dagql/... ./engine/...

# unfiltered: exit 1
env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core ./core/schema ./dagql -count=1

# b1-race-compile: exit 0
go test -race -c ./core -o /tmp/b1-b2-integration/core-race.test

# b1-race: exit 0
sudo -n unshare --mount --propagation private /tmp/b1-b2-integration/core-race.test -test.run '^(TestHTTPStateConcurrentResolveCapture|TestStatelessHTTPProducerIsolation|TestCompletedProducerConcurrentDemand)$' -test.count=1 -test.v

# b1-race-writer: exit 0
sudo -n unshare --mount --propagation private /tmp/b1-b2-integration/core-race.test -test.run '^TestHTTPProducerWriter$' -test.count=1 -test.v

# b2-race: exit 0
go test -p=1 -race ./dagql -run '^(TestValueTransferCapture|TestValueTransferImportPublication|TestValueTransferOfferOwners|TestValueTransferOfferCopyFailure|TestSchemaModuleSelection.*)$' -count=1 -timeout=180s -v

# b2-selected-chains: exit 0
env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestValueTransferParts' -count=1 -timeout=180s -v

# b2-offer-restart: exit 0
env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestValueTransferPersistenceFinalOfferRestart$' -count=1 -timeout=120s -v

# b1-native-git: exit 0
dagger api call engine-dev test --pkg ./core/integration --run='TestGit/(TestGitUncommittedLocal|TestGitUncommittedRemote|TestGitBundleRoundTripAndStockInterop|TestGitBundleImportAfterPrerequisiteRefAdvances|TestGitCommit|TestDiscardGitDir|TestGitDepth|TestGitTags)$'

# b1-native-http: exit 0
dagger api call engine-dev test --pkg ./core/integration --run='TestHTTP/(TestHTTPName|TestHTTPPermissions|TestHTTPChecksum|TestHTTPChecksumMismatch|TestHTTPTimestamp|TestHTTPETag|TestHTTPCachePerSessions|TestHTTPAuth|TestHTTPService)$'

# b1-native-restart: exit 0
dagger api call engine-dev test --pkg ./core/integration --run='TestCachePersistence/TestDiskPersistenceAcrossRestart/eager_producers_survive_restart$'

# b2-native-schema: exit 0
dagger api call engine-dev test --pkg ./core/integration --run='RemoteCacheTransferSuite/TestSchemaRecovery' --timeout=20m --test-verbose

# diff-check: exit 2
git diff --check a88e0e0cdd..5483cfc495f93d4777104bda7edede27e33607c9

```

## Failures and limits

- **Vet, exit 1:** six `copylocks` diagnostics in `core/completed_producer_test.go` at lines 50, 56 (two), 107 and 113 (two). Batch 1's nonmutation assertions copy `Directory` / `File` values; batch 2 embeds `filesystemOutput`, which contains `sync.Mutex`. Four additional `lostcancel` diagnostics occur in `engine/engineutil/executor.go:565,649,716` and `engine/server/session_attachables.go:211`. Those two engine files have no changes from the common foundation to the integrated head; see [provenance check](vet-context-warning-provenance.txt). The exact diagnostics are preserved in [vet.log](logs/vet.log). No warning was suppressed.
- **Unfiltered packages, exit 1:** `TestContainerShutdownPersistenceWaitsForReader/completed` failed in `core`. Reopening the store logged `result 2: visit persisted core.Container payload at "": reference objectJSON.lazyJSON.parentResultID to 1 is not a direct dependency`, wiped the store, and returned persistence reset reason `import_failure` where the test expected an empty reason. This is a completed-producer dependency validation failure observed during reopen, not a hang. The test's logged goroutine stack demonstrates its intentional wait on the read-only latch. `core` finished in 9.478s; `core/schema` passed in 42.086s and `dagql` passed in 2.719s. The complete package output is [unfiltered.log](logs/unfiltered.log). No package or subtest was filtered out or selectively rerun.
- **Diff whitespace check, exit 2:** the retained batch 2 evidence logs contain trailing whitespace and space-before-tab diagnostics. The original batch 2 range produces the same evidence-file diagnostics; see [original range check](inherited-evidence-whitespace.txt). Reviewed evidence was preserved unchanged.

The required named race selections, selected-chain peers and real-store restart peer ran without skips or race reports. The privileged peers cover both real Git checkout backends, the selected Directory/File chain, Container mount selection, and final redundant-offer retirement across restart.

Service teardown spans may be marked `ERROR` in successful dev-engine logs; command status and the final test verdict determine the recorded result. The earlier vet and unfiltered failures remain failures regardless of later selections passing.

All four bounded dev-engine commands passed. Their displayed test counts summarize suite/package spans, rather than the number of individual assertions. The batch 2 native run completed in 287.664 seconds. [Scoped schema-recovery trace](logs/b2-native-schema-trace.log) and [cold-order boundary](logs/b2-native-cold-boundary.log) were fetched read-only from the same successful trace; no tests were rerun. The cold case explicitly skipped in 0.0s with the existing addendum-2 boundary. This is not evidence of fully cold acquisition, and the default GC pressure diagnostic was not selected by this commission.

Trace-fetch commands and exit statuses:

```sh
# b2-native-schema-trace: exit 0
dagger trace 0af83972d2b99132c009d701bd2292c2 --test TestRemoteCacheTransferSuite/TestSchemaRecovery
# b2-native-cold-boundary: exit 0
dagger trace 0af83972d2b99132c009d701bd2292c2 --test TestRemoteCacheTransferSuite/TestSchemaRecoveryCold
```

No implementation or test change was made after the conflict resolutions. The final evidence commit contains only this integration directory; it does not update the inherited standalone manifests or reports. No pushes, pull requests, tags, or author/reviewer contacts were made.
