# Completed Container publication fix

Verification is **green under the commissioned criteria**: every required build and test command passed. Vet exited 1 with exactly the four permitted pre-existing `lostcancel` diagnostics. Existing skips and scope limits are recorded below.

Implementation head: `17384b793fe715006ffdbba4b1075354cd7733d8`.
Parent: `f035d2c2a307cdf0ab66025aef147016a02d2e8a`.
Branch: `remote-cache-integration-b1-b2-36d54e12`.

## Change

The Container dependency hook snapshots the operational `Lazy`, falling back to the live `completedRecipe`, under `lazyOpMu`. It unlocks before any attachment callback and invokes exactly one recipe's existing `AttachDependencies`. The resulting producer dependencies retain `Owned: false`. No ancestor decoding, producer execution, or completed JSON handling was added. The pending-Container encoder guard is unchanged.

This implements option A from the accepted [diagnosis](../FAILURE-DIAGNOSIS.md), as required by batch 1 section 10 and batch 2 section 5.1. The omission dates to foundation commit `c3491f00ad`, before both batches, and was exposed by batch 2's boot validator. The signed-off implementation commit records that attribution.

The new `TestContainerCompletedProducerAttachesParentAtPublication` completes a real `ContainerWithLabelLazy` before publication, verifies the operational recipe was consumed and the live recipe retained, then publishes through a synthetic call without a receiver or arguments. It asserts that the child's explicit direct dependency set contains exactly the parent. Consequently, call-graph edges cannot mask the missing attachment that caused the original failure.

Production changes are limited to `core/container.go`; the one new test is in `core/container_producer_persistence_test.go`. See the [implementation diff](implementation.diff). The encoder and existing shutdown test are byte-identical to the accepted parent; the two engine files with baseline vet diagnostics are byte-identical to upstream main `dfe204d216`. [Blob and provenance checks](unchanged-files.txt).

## Verification method

The full commission list ran sequentially on the implementation head before adding evidence. Every result records that same head, command, start and finish timestamps, elapsed time, exit status and complete combined output. No failed command was replaced with a narrower selection.

The unfiltered run covers `./core ./core/schema ./dagql -count=1` through the privileged private-mount-namespace runner, with `-p=1` to serialize packages and `-v` to expose individual test verdicts. Both batches' named race selections, the separate batch 1 writer selection, selected-chain peers, the real-store restart peer, and all four dev-engine selections are unchanged. The dev-engine commands use the installed CLI's default UI. The additional whitespace check covers only the new implementation commit; earlier inherited evidence whitespace remains documented in the original report.

[Environment](environment.txt), [sequential runner](run-verification.py), [runner changes from the original commission run](runner-changes.diff), [machine-readable results](results.json).

## Results

| Check | Exit status | Seconds | Evidence |
| --- | ---: | ---: | --- |
| `build` | 0 | 42.213 | [command](logs/build-command.txt), [complete log](logs/build.log) |
| `vet` | 1 | 15.206 | [command](logs/vet-command.txt), [complete log](logs/vet.log) |
| `unfiltered` | 0 | 83.602 | [command](logs/unfiltered-command.txt), [complete log](logs/unfiltered.log) |
| `b1-race-compile` | 0 | 41.635 | [command](logs/b1-race-compile-command.txt), [complete log](logs/b1-race-compile.log) |
| `b1-race` | 0 | 1.688 | [command](logs/b1-race-command.txt), [complete log](logs/b1-race.log) |
| `b1-race-writer` | 0 | 3.195 | [command](logs/b1-race-writer-command.txt), [complete log](logs/b1-race-writer.log) |
| `b2-race` | 0 | 4.441 | [command](logs/b2-race-command.txt), [complete log](logs/b2-race.log) |
| `b2-selected-chains` | 0 | 5.417 | [command](logs/b2-selected-chains-command.txt), [complete log](logs/b2-selected-chains.log) |
| `b2-offer-restart` | 0 | 4.854 | [command](logs/b2-offer-restart-command.txt), [complete log](logs/b2-offer-restart.log) |
| `b1-native-git` | 0 | 153.192 | [command](logs/b1-native-git-command.txt), [complete log](logs/b1-native-git.log) |
| `b1-native-http` | 0 | 59.781 | [command](logs/b1-native-http-command.txt), [complete log](logs/b1-native-http.log) |
| `b1-native-restart` | 0 | 96.907 | [command](logs/b1-native-restart-command.txt), [complete log](logs/b1-native-restart.log) |
| `b2-native-schema` | 0 | 282.331 | [command](logs/b2-native-schema-command.txt), [complete log](logs/b2-native-schema.log) |
| `diff-check` | 0 | 0.018 | [command](logs/diff-check-command.txt), [complete log](logs/diff-check.log) |

### Exact invocations

```sh
# build: exit 0
go build ./...

# vet: exit 1
go vet ./core/... ./dagql/... ./engine/...

# unfiltered: exit 0
env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core ./core/schema ./dagql -count=1 -v

# b1-race-compile: exit 0
go test -race -c ./core -o /tmp/b1-b2-fix/core-race.test

# b1-race: exit 0
sudo -n unshare --mount --propagation private /tmp/b1-b2-fix/core-race.test -test.run '^(TestHTTPStateConcurrentResolveCapture|TestStatelessHTTPProducerIsolation|TestCompletedProducerConcurrentDemand)$' -test.count=1 -test.v

# b1-race-writer: exit 0
sudo -n unshare --mount --propagation private /tmp/b1-b2-fix/core-race.test -test.run '^TestHTTPProducerWriter$' -test.count=1 -test.v

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

# diff-check: exit 0
git diff --check f035d2c2a307cdf0ab66025aef147016a02d2e8a..17384b793fe715006ffdbba4b1075354cd7733d8

```

### Publication and shutdown regression

The full unfiltered run passed `core` (10.362s), `core/schema` (43.365s), and `dagql` (2.981s). Its [complete log](logs/unfiltered.log) includes:

```text
--- PASS: TestContainerCompletedProducerAttachesParentAtPublication (0.02s)
--- PASS: TestContainerShutdownPersistenceWaitsForReader (0.06s)
    --- PASS: TestContainerShutdownPersistenceWaitsForReader/pending (0.04s)
    --- PASS: TestContainerShutdownPersistenceWaitsForReader/completed (0.02s)
```

The original shutdown test was neither edited nor narrowed. The former completed-branch import failure is resolved.

### Vet baseline

`go vet ./core/... ./dagql/... ./engine/...` exited 1. Its complete output is exactly:

```text
engine/engineutil/executor.go:565:7: the cancel function returned by context.WithTimeoutCause should be called, not discarded, to avoid a context leak
engine/engineutil/executor.go:649:14: the cancel function returned by context.WithTimeoutCause should be called, not discarded, to avoid a context leak
engine/engineutil/executor.go:716:7: the cancel function returned by context.WithTimeoutCause should be called, not discarded, to avoid a context leak
engine/server/session_attachables.go:211:14: the cancel function returned by context.WithTimeoutCause should be called, not discarded, to avoid a context leak
```

There are no copylocks diagnostics or other new vet findings. Both affected engine files match upstream main `dfe204d216`; see [provenance](unchanged-files.txt). No diagnostic was suppressed.

### Existing skips and scope limits

- The unfiltered `dagql` run retains `TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases`, skipped at `dagql/cache_test.go:2289` pending the existing last-waiter cleanup semantics decision.
- The native schema selection retains the existing `TestSchemaRecoveryCold` skip at `core/integration/remote_cache_transfer_test.go:105`. It does not prove fully cold acquisition, which remains outside this reviewed acceptance boundary. No default-GC diagnostic was added to the selection.

Both skip sites are unchanged from the accepted parent; see [source and unchanged-file checks](existing-skip-source.txt). No skip was added and no prescribed test selection was narrowed. The named race selections and both privileged peer selections passed without skips or race reports.

All four dev-engine selections passed. Service teardown spans and expected negative HTTP operations may be marked `ERROR` in successful logs; each final test verdict and command status is recorded above. Read-only views of the successful schema trace `acdf3cd08ed0e4715b5c31582fdc65e8` confirm the recovery case and existing cold-case boundary; no tests were rerun to collect them.

| Trace view | Exit status | Evidence |
| --- | ---: | --- |
| `b2-native-schema-trace` | 0 | [command](logs/b2-native-schema-trace-command.txt), [log](logs/b2-native-schema-trace.log) |
| `b2-native-cold-boundary` | 0 | [command](logs/b2-native-cold-boundary-command.txt), [log](logs/b2-native-cold-boundary.log) |

[Trace collection script](collect-schema-traces.py), [trace command metadata](trace-results.json).

## Evidence integrity

All verification preceded this separate evidence commit, with a clean worktree at the tested implementation head. Full output was copied without filtering. [SHA256SUMS](SHA256SUMS) covers this fix directory except the checksum file itself. The parent report received a Fix section and its entry in the parent checksum manifest was refreshed; earlier run logs, the accepted diagnosis and follow-up evidence remain unchanged. No reviewed commit was amended.
