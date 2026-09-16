# Command ledger

The completed package commands and first engine runs were sequential under the original commission. After the verification direction changed, passing results were preserved: only the remaining engine selections were combined (initially with the subsequently retracted `--parallel=2` bound), while the opted-in diagnostic ran concurrently from an isolated checkout. Commands use the repository root except the named base and diagnostic worktrees. Privileged tests use a private mount namespace. The unfiltered package command names only core, schema and dagql and serializes them with `-p=1`. The final 28 package selections all pass; their manifest records exit status and wall time. The separate base layout probe also passes.

The preserved package set tested the production code after the HTTP cleanup correction. The later ownership guard fix and its new focused checks are recorded below. Subsequent edits clarify the output helper ownership comment and explicitly warm the scratch donor in warm-only integration setup; the completed package and cold selections do not execute that warm setup; [the exact editorial diff](probes/source-editorial.patch) records it. Test source tree: `2b75797f5acff6586673f0afd3da99f5161457f0`; source tree before the warm-only fixture edit: `08b6b934d65fe2306980af9a0aca7fc393759938`.

The [development log summary](DEVELOPMENT-CHECKS.md) records earlier iterations separately. Historical operation names in those logs are presented in current vocabulary; [the manifest](log-manifest.json) records each original byte count and SHA-256. Final selected package logs contain no skipped test or empty selection. The unfiltered command reports package completion rather than individual test names.

## package-full

```sh
env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core ./core/schema ./dagql -count=1 -timeout=10m
```

Exit 0; 148.134 s. [Output](logs/package-full.log).

## object

```sh
go test -race ./core -run '^Test(ModuleObject|PersistedModuleObjectPayloadRelocation|SavedPayloadRelocation)' -count=1 -timeout=90s
```

Exit 0; 53.249 s. [Output](logs/object.log).

## fixture

```sh
go test ./core/schema -run '^TestRemoteCacheFixture$' -count=1 -timeout=90s
```

Exit 0; 5.294 s. [Output](logs/fixture.log).

## dagql-boundaries

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^TestPart(Decision|Decode|ReadyPreparation)' -count=1 -v -timeout=90s
```

Exit 0; 40.043 s. [Output](logs/dagql-boundaries.log).

## core-boundaries

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(PartFilesystemPublicationRoles|PartAcquisitionRootRoutes|PartTypedPublicationRoles|CapturePersistedFilesystemDirectEvaluation|FilesystemPersistenceRetainsBodyLatch)$' -count=1 -v -timeout=90s
```

Exit 0; 9.384 s. [Output](logs/core-boundaries.log).

## whole-restart

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(WholeLazyOperationMixedRestart|NativePendingContainerRequiresRecipe)$' -count=1 -v -timeout=90s
```

Exit 0; 6.363 s. [Output](logs/whole-restart.log).

## pending-metadata

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartPendingImageMetadataStaysSelective$' -count=1 -v -timeout=90s
```

Exit 0; 5.839 s. [Output](logs/pending-metadata.log).

## ready-backreference

```sh
go test -race ./dagql -run '^TestReadyPartDonorBackreferenceReleasedBeforeSync$' -count=1 -v -timeout=90s
```

Exit 0; 5.773 s. [Output](logs/ready-backreference.log).

## dagql-full

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^Test(Part|Scoped|ReadyPartReceipt|VisitEncodedReferences|ValueTransferPersistenceDecodePublication|CacheEvaluate(RetiresFinishedAttemptBeforeWaitersDrain|OwnCancellationOnlyCancelsOwnWait|SettlesBookkeepingBeforeReportingComplete|PendingBookkeepingSkipsUnclearedCallback)|EvaluateParts(SyncFailureRetriesOnlyBookkeepingPerGroup|SiblingGroupsRunConcurrently|OneCallRunsGroupsConcurrently))' -count=1 -v -timeout=90s
```

Exit 0; 11.447 s. [Output](logs/dagql-full.log).

## core-full

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(Part|ValueTransferCapture|ValueTransferList|Container(DirectEvaluateRunsRefinedGroups|ConcurrentGroupCompletionRetainsLazy|RoutingReadsRaceRefinedCompletion|RoutingReadsRaceUnrefinedCompletion))' -count=1 -v -timeout=90s
```

Exit 0; 19.277 s. [Output](logs/core-full.log).

## snapshots

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./engine/snapshots -run '^Test(PinSnapshotIndependentOwner|ChainContentClassification|ImportChain(FailurePrefix|ConcurrentPrefix|CanceledWaiter|LocalStores)|ImportImageSharesChainReuse)$' -count=1 -timeout=90s
```

Exit 0; 5.675 s. [Output](logs/snapshots.log).

## declared-handle

```sh
go test ./core -run '^TestModuleObjectAttachDependencyResultsRetainsSemanticInterfaceHandleField$' -count=1 -timeout=60s
```

Exit 0; 4.519 s. [Output](logs/declared-handle.log).

## collector-costs

```sh
go test ./dagql -run '^TestScopedCollectorConcurrentPublicationCost$' -bench '^(BenchmarkScopedSnapshotCollector|BenchmarkPartNativeHostEntry)$' -benchmem -benchtime=200ms -count=1 -v -timeout=90s
```

Exit 0; 5.909 s. [Output](logs/collector-costs.log).

## route-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(ScopeRootRekeyCost|AcquisitionRootRoutes)$' -count=1 -v -timeout=90s
```

Exit 0; 5.799 s. [Output](logs/route-costs.log).

## delegation-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartDelegationRealStore/native-restart$' -count=1 -v -timeout=90s
```

Exit 0; 4.639 s. [Output](logs/delegation-costs.log).

## mount-operations

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(MountedLazyRepresentations|MountLazyDetachedOwnership)$' -count=1 -v -timeout=90s
```

Exit 0; 6.291 s. [Output](logs/mount-operations.log).

## mount-schema

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core/schema -run '^(TestLazyOperationResolver(Cleanup|OutputCleanup|Capture)|TestEagerContainerMountMetadataResolvers|TestMountConstruct.*|TestBuiltin.*)$' -count=1 -v -timeout=180s
```

Exit 0; 73.849 s. [Output](logs/mount-schema.log).

## scratch-codecs

```sh
go test -race ./core -run '^(TestLazyInputAttachment|TestLazyOperationCodecs|TestLazyOperationRelocation|TestLazyOperationSaveReopen)$' -count=1 -v -timeout=90s
```

Exit 0; 6.667 s. [Output](logs/scratch-codecs.log).

## scratch-real

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core/schema -run '^TestScratchDirectory(LazyOperation|Acquisition|NativeReopen)$' -count=1 -v -timeout=90s
```

Exit 0; 7.95 s. [Output](logs/scratch-real.log).

## scratch-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core/schema -run '^TestScratchDirectory(LazyOperation|Acquisition|NativeReopen)$' -count=1 -v -timeout=90s
```

Exit 0; 5.331 s. [Output](logs/scratch-costs.log).

## storage-costs

```sh
go test ./core -run '^TestLazyStorageLayout$' -bench '^BenchmarkScratchLazyConstruction$' -benchmem -benchtime=300ms -count=1 -v -timeout=90s
```

Exit 0; 4.57 s. [Output](logs/storage-costs.log).

## delegation-focused

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartDelegation' -count=1 -v -timeout=90s
```

Exit 0; 10.008 s. [Output](logs/delegation-focused.log).

## delegation-path

```sh
go test -race ./dagql -run '^TestPartDelegation' -count=1 -v -timeout=90s
```

Exit 0; 5.816 s. [Output](logs/delegation-path.log).

## operation-core

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(Lazy|Filesystem|CapturePersisted|Container(FinalDelegation|Metadata|MountedSource|PathWriter|WithoutPath|GetVariant|Delegation|HasPending|DirectEvaluate|ConcurrentGroup|RoutingReads|Restore|Shutdown|Persistence|DirectorySelector|FileSelector|Exec|FromImage|EvaluatedLazyOperation)|DockerfileCompatMountSource|EvaluatedLazyOperation|RestoredSnapshot|SourceFilePaths|MoveProducedOutputs|BuiltinLazyOperation|GitLazyOperations|GitBundleLazyOperation)' -count=1 -v -timeout=180s
```

Exit 0; 14.662 s. [Output](logs/operation-core.log).

## http-core

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(HTTP|StatelessHTTP)' -count=1 -v -timeout=180s
```

Exit 0; 10.99 s. [Output](logs/http-core.log).

## http-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestHTTPPinLockCost$' -count=1 -v -timeout=90s
```

Exit 0; 4.436 s. [Output](logs/http-costs.log).

## operation-schema

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core/schema -run '^Test(HTTPResolvedCall|HTTPPendingInternalHits|GitResolvedFrames|GitFixedCommitAndLockFrames|SchemaFileLazy|LazyStoredResultsWithoutBacking|CloneContainer.*|WithImageConfig.*|ContainerTransferPendingChild)$' -count=1 -v -timeout=180s
```

Exit 0; 10.565 s. [Output](logs/operation-schema.log).

## format-cut

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^Test(LazyOperationCompatibilityCut|CachePersistenceSchemaMismatchWipesStore|CachePersistenceWorkerUsesEncodedSnapshotLinks)$' -count=1 -v -timeout=90s
```

Exit 0; 6.027 s. [Output](logs/format-cut.log).

## base-storage-costs

```sh
go test ./core -run '^TestLazyStorageLayout$' -count=1 -v -timeout=90s
```

Exit 0. [Output](logs/base-storage-costs.log).

Working directory: `/tmp/dagger-lazy-values-base-018a0e69`. Identical temporary layout probe at base; removed after the run.

## HTTP cleanup regression

Before correction (expected failure, exit 1):

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestHTTPLocalBodyFailures/successful_derivation_cleanup$' -count=1 -v -timeout=90s
```

[Before output](logs/http-cleanup-before.log). The initial corrected case passed with `-race`; final `http-core` includes the added derived-ref release-count assertions and retry on the same receiver.

## Engine verification

Passing results were preserved when the verification form changed. The final package set and cold proof were not restarted. Only the failed warm selection and as-yet-unrun engine cases were combined. The diagnostic requires a different runner environment, so it ran concurrently from an isolated checkout. The first pair was launched with the then-required `--parallel=2`; after the bound was retracted and that pair failed in engine construction, the retry used the harness default. Unrequested persistence siblings were excluded using `--skip`; this is selection filtering, not an acceptance skip.

### cold-engine

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecoveryCold$' --timeout=5m --test-verbose
```

Exit 0; invocation 484.565 s; trace `2b27f7c4d377ce0352658e5f370d6f32`; passed, no skips. [Selected output](logs/cold-engine-counters.log).

Selected test: 199.09 s.

### remaining-engine

```sh
dagger -vv api call engine-dev test --pkg ./core/integration '--run=^(TestRemoteCacheTransferSuite|TestGit|TestHTTP|TestCachePersistence)$/^(TestSchemaRecovery|TestPartMixedExecOutputs|TestGitUncommittedLocal|TestGitUncommittedRemote|TestGitBundleRoundTripAndStockInterop|TestGitBundleImportAfterPrerequisiteRefAdvances|TestGitCommit|TestDiscardGitDir|TestGitDepth|TestGitTags|TestGitLatest|TestGitLatestFallsBackToHead|TestGitCommitReleaseTags|TestGitCommitReleaseTagFreshness|TestHTTPName|TestHTTPPermissions|TestHTTPChecksum|TestHTTPChecksumMismatch|TestHTTPTimestamp|TestHTTPETag|TestHTTPCachePerSessions|TestHTTPAuth|TestHTTPService|TestDiskPersistenceAcrossRestart)$' '--skip=^TestCachePersistence$/^TestDiskPersistenceAcrossRestart$/^(changeset_merge_operation_survives_restart|directory_and_file_restore_without_opening|module_function_directory_list_survives_repeated_restarts|module_core_metadata_returns_survive_restart|container_parts_preserve_mutations_and_unopened_snapshots|local_cache_survives_restart|lazy_imported_snapshot_links_count_toward_local_cache_usage_and_max-used_prune|unclean_shutdown_discards_local_cache_state_and_recovers|container_withNewFile_hit_survives_restart|container_selector_lazy_dependencies_survive_restart|directory_search_result_list_survives_restart|changeset_diff_stat_list_survives_restart|service-bound_graph_does_not_break_disk_persistence|generator_group_graph_does_not_break_disk_persistence|private_field_handle_survives_restart|function_cache_control_survives_restart|typescript_function_cache_control_survives_restart|contextual_function_cache_survives_restart|container_withExec_output_on_host_mount_survives_restart|container_withExec_output_on_host_mounted_file_survives_restart|container_child_exec_during_concurrent_mounted_directory_parent_eval|git_repository_and_ref_survive_restart|engine-dev_container_build_survives_restart|cache_volume_survives_restart|source-backed_cache_volume_supports_concurrent_mounts_after_restart)$' --timeout=15m --test-verbose
```

Exit 1; invocation 1168.698 s; trace `124abeda8af965c452cf6c6b6094b9b5`; see per-test results. [Selected output](logs/remaining-engine-selected.log).

| Test | Result | Seconds |
|---|---|---:|
| `TestRemoteCacheTransferSuite` | PASS | 406.04 |
| `TestRemoteCacheTransferSuite/TestPartMixedExecOutputs` | PASS | 77.78 |
| `TestRemoteCacheTransferSuite/TestSchemaRecovery` | PASS | 328.26 |
| `TestRemoteCacheTransferSuite/TestSchemaRecovery/before` | PASS | 158.32 |
| `TestRemoteCacheTransferSuite/TestSchemaRecovery/after` | PASS | 74.32 |
| `TestRemoteCacheTransferSuite/TestSchemaRecovery/foreign_context` | PASS | 50.13 |
| `TestHTTP` | FAIL | 0.03 |
| `TestHTTP/TestHTTPName` | PASS | 0.76 |
| `TestHTTP/TestHTTPPermissions` | FAIL | 1.55 |
| `TestHTTP/TestHTTPCachePerSessions` | FAIL | 1.7 |
| `TestHTTP/TestHTTPTimestamp` | FAIL | 3.2 |
| `TestHTTP/TestHTTPChecksum` | FAIL | 3.37 |
| `TestHTTP/TestHTTPETag` | PASS | 3.42 |
| `TestHTTP/TestHTTPService` | FAIL | 3.46 |
| `TestHTTP/TestHTTPChecksumMismatch` | FAIL | 4.05 |
| `TestHTTP/TestHTTPAuth` | PASS | 11.69 |
| `TestCachePersistence` | PASS | 0.0 |
| `TestCachePersistence/TestDiskPersistenceAcrossRestart` | PASS | 0.03 |
| `TestCachePersistence/TestDiskPersistenceAcrossRestart/lazy_values_survive_restart` | PASS | 60.75 |

### default-policy-opted-in

```sh
dagger -vv api call engine-dev test --pkg ./core/integration '--run=TestRemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic$' --timeout=10m --test-verbose --env-file=file:/tmp/lazy-values-default.env
```

Exit 0; invocation 429.524 s; trace `dc96140b4cfc0fb165575e120014be50`; passed, no selected skips. [Selected output](logs/default-policy-opted-in-selected.log).

| Test | Result | Seconds |
|---|---|---:|
| `TestRemoteCacheTransferSuite` | PASS | 161.87 |
| `TestRemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic` | PASS | 161.87 |
| `TestRemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic/after` | PASS | 77.2 |

The combined invocation covers both warm orders, mixed exec, twelve Git methods, nine HTTP methods and the lazy-value restart subtest. [Machine-readable selection list](remaining-selections.json). The opted-in diagnostic has its own result and counters; no omitted, skipped or unreached control is counted as a pass.

The diagnostic uses an isolated checkout of the same implementation plus the recorded [runner overlay](probes/default-policy-runner.patch). Env-file values are passed to child sessions; the overlay also enables the gate in the test process. The runner file was restored byte-for-byte afterward. The tree used by that combined warm/mixed/restart invocation is `a9861d95c7fa36a23c8f9a9bd5c6930e8dc62325`. The [integration-only fixture changes](probes/integration-fixture.patch) postdate the preserved package and cold selections; the latter execute none of the changed warm setup, and the restart expectation edits affect only the selected restart fixture.

### Earlier final warm failure

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecovery$' --timeout=5m --test-verbose
```

Exit 1; invocation 526.883 s; selected test 292.49 s; trace `4e0c4d61ac58bf02fa76d726c8fd0c8f`. Both orders failed with one scratch Lazy entry instead of zero. Their later controls were not reached. Foreign context passed. [Assertion excerpt](logs/warm-before-correction.log). The correction explicitly warms the native scratch donor now that schema generation no longer does that incidentally.

### Interrupted engine construction

```sh
dagger -vv api call engine-dev test --pkg ./core/integration '--run=^(TestRemoteCacheTransferSuite|TestGit|TestHTTP|TestCachePersistence)$/^(TestSchemaRecovery|TestPartMixedExecOutputs|TestGitUncommittedLocal|TestGitUncommittedRemote|TestGitBundleRoundTripAndStockInterop|TestGitBundleImportAfterPrerequisiteRefAdvances|TestGitCommit|TestDiscardGitDir|TestGitDepth|TestGitTags|TestGitLatest|TestGitLatestFallsBackToHead|TestGitCommitReleaseTags|TestGitCommitReleaseTagFreshness|TestHTTPName|TestHTTPPermissions|TestHTTPChecksum|TestHTTPChecksumMismatch|TestHTTPTimestamp|TestHTTPETag|TestHTTPCachePerSessions|TestHTTPAuth|TestHTTPService|TestDiskPersistenceAcrossRestart)$' '--skip=^TestCachePersistence$/^TestDiskPersistenceAcrossRestart$/^(changeset_merge_operation_survives_restart|directory_and_file_restore_without_opening|module_function_directory_list_survives_repeated_restarts|module_core_metadata_returns_survive_restart|container_parts_preserve_mutations_and_unopened_snapshots|local_cache_survives_restart|lazy_imported_snapshot_links_count_toward_local_cache_usage_and_max-used_prune|unclean_shutdown_discards_local_cache_state_and_recovers|container_withNewFile_hit_survives_restart|container_selector_lazy_dependencies_survive_restart|directory_search_result_list_survives_restart|changeset_diff_stat_list_survives_restart|service-bound_graph_does_not_break_disk_persistence|generator_group_graph_does_not_break_disk_persistence|private_field_handle_survives_restart|function_cache_control_survives_restart|typescript_function_cache_control_survives_restart|contextual_function_cache_survives_restart|container_withExec_output_on_host_mount_survives_restart|container_withExec_output_on_host_mounted_file_survives_restart|container_child_exec_during_concurrent_mounted_directory_parent_eval|git_repository_and_ref_survive_restart|engine-dev_container_build_survives_restart|cache_volume_survives_restart|source-backed_cache_volume_supports_concurrent_mounts_after_restart)$' --timeout=15m --parallel=2 --test-verbose
```

Exit -15 (local SIGTERM after server session removal); 768.655 s. [Original CLI output](logs/build-interruption-remaining-engine.log).

```sh
dagger -vv api call engine-dev test --pkg ./core/integration '--run=TestRemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic$' --timeout=10m --parallel=2 --test-verbose --env-file=file:/tmp/lazy-values-default.env
```

Exit -15 (local SIGTERM after server session removal); 768.598 s. [Original CLI output](logs/build-interruption-default-policy-opted-in.log).

Both server builds failed with client-attachment timeouts and were canceled before selected tests began. The local clients waited after both sessions ended; they were terminated rather than counted as tests. [Runner error excerpt](logs/build-interruption-runner.log) and [invocation records](build-interruption-results.json) preserve the trace IDs, raw hashes and timings. Fetching those traces also timed out. Only the still-unrun selections were retried.

A preliminary cold invocation before the HTTP cleanup correction passed in 420.49 s (selected test 181.40 s); its following warm invocation was canceled. [Cold output](logs/preliminary-cold-engine-counters.log), [canceled warm output](logs/preliminary-warm-engine.log). These are development evidence, not replacements for the final acceptance results.

Static checks: `git diff --check`, source/evidence vocabulary audit, new commit signoffs, parent ancestry and clean worktree after the separate evidence commit. These checks do not rerun passing selections.

## Ownership contention correction

The combined warm/mixed/restart invocation above passed those methods and three HTTP methods, but six HTTP methods returned a transient ownership guard error and Git did not complete before the package timeout. Only the six failed HTTP methods and twelve commissioned Git methods were combined for the final invocation below. The separately requested single-method isolation passed before the fix; its logs remain separate. No earlier cold, warm, mixed, restart, default-policy or package selection was repeated.

### http-isolation

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='^TestHTTP$/^TestHTTPPermissions$' --timeout=3m --test-verbose
```

Exit 0; invocation 230.097 s; trace `a3d813be26eb72979013bfb1ffe2f8b5`. [Verbose selected output](logs/http-isolation-selected.log).

| Test | Result | Seconds |
|---|---|---:|
| `TestHTTP` | PASS | 0.0 |
| `TestHTTP/TestHTTPPermissions` | PASS | 4.31 |

### http-git-final

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='^(TestHTTP|TestGit)$/^(TestHTTPPermissions|TestHTTPCachePerSessions|TestHTTPTimestamp|TestHTTPChecksum|TestHTTPService|TestHTTPChecksumMismatch|TestGitUncommittedLocal|TestGitUncommittedRemote|TestGitBundleRoundTripAndStockInterop|TestGitBundleImportAfterPrerequisiteRefAdvances|TestGitCommit|TestDiscardGitDir|TestGitDepth|TestGitTags|TestGitLatest|TestGitLatestFallsBackToHead|TestGitCommitReleaseTags|TestGitCommitReleaseTagFreshness)$' --timeout=15m --test-verbose
```

Exit 0; invocation 158.117 s; trace `fd96be142f4e43783f9ace5ea4d087c5`. [Verbose selected output](logs/http-git-final-selected.log).

| Test | Result | Seconds |
|---|---|---:|
| `TestHTTP` | PASS | 0.01 |
| `TestHTTP/TestHTTPPermissions` | PASS | 13.02 |
| `TestHTTP/TestHTTPChecksumMismatch` | PASS | 13.31 |
| `TestHTTP/TestHTTPService` | PASS | 13.4 |
| `TestHTTP/TestHTTPChecksum` | PASS | 13.41 |
| `TestHTTP/TestHTTPTimestamp` | PASS | 13.95 |
| `TestHTTP/TestHTTPCachePerSessions` | PASS | 19.31 |
| `TestGit` | PASS | 0.01 |
| `TestGit/TestGitUncommittedRemote` | PASS | 1.59 |
| `TestGit/TestDiscardGitDir` | PASS | 0.28 |
| `TestGit/TestDiscardGitDir/git_dir_is_present` | PASS | 6.72 |
| `TestGit/TestDiscardGitDir/git_dir_is_not_present` | PASS | 3.79 |
| `TestGit/TestGitCommit` | PASS | 13.95 |
| `TestGit/TestGitLatest` | PASS | 14.57 |
| `TestGit/TestGitLatestFallsBackToHead` | PASS | 14.76 |
| `TestGit/TestGitCommitReleaseTags` | PASS | 15.26 |
| `TestGit/TestGitUncommittedLocal` | PASS | 15.27 |
| `TestGit/TestGitBundleImportAfterPrerequisiteRefAdvances` | PASS | 15.61 |
| `TestGit/TestGitBundleRoundTripAndStockInterop` | PASS | 16.17 |
| `TestGit/TestGitCommitReleaseTagFreshness` | PASS | 16.59 |
| `TestGit/TestGitDepth` | PASS | 16.96 |
| `TestGit/TestGitTags` | PASS | 1.69 |
| `TestGit/TestGitTags/remote` | PASS | 0.01 |
| `TestGit/TestGitTags/remote/prefix-qualified_tag_pattern` | PASS | 0.03 |
| `TestGit/TestGitTags/remote/branches_pattern` | PASS | 0.01 |
| `TestGit/TestGitTags/remote/all_branches` | PASS | 0.02 |
| `TestGit/TestGitTags/remote/all_tags` | PASS | 1.01 |
| `TestGit/TestGitTags/remote/ref-qualified_tag_pattern` | PASS | 0.04 |
| `TestGit/TestGitTags/remote/tag_pattern` | PASS | 0.08 |
| `TestGit/TestGitTags/remote_(short)` | PASS | 0.0 |
| `TestGit/TestGitTags/remote_(short)/all_tags` | PASS | 0.61 |
| `TestGit/TestGitTags/remote_(short)/prefix-qualified_tag_pattern` | PASS | 0.02 |
| `TestGit/TestGitTags/remote_(short)/branches_pattern` | PASS | 0.01 |
| `TestGit/TestGitTags/remote_(short)/all_branches` | PASS | 0.01 |
| `TestGit/TestGitTags/remote_(short)/ref-qualified_tag_pattern` | PASS | 0.04 |
| `TestGit/TestGitTags/remote_(short)/tag_pattern` | PASS | 0.13 |
| `TestGit/TestGitTags/local_worktree` | PASS | 0.0 |
| `TestGit/TestGitTags/local_worktree/branches_pattern` | PASS | 14.7 |
| `TestGit/TestGitTags/local_worktree/all_branches` | PASS | 14.61 |
| `TestGit/TestGitTags/local_worktree/prefix-qualified_tag_pattern` | PASS | 14.86 |
| `TestGit/TestGitTags/local_worktree/ref-qualified_tag_pattern` | PASS | 14.7 |
| `TestGit/TestGitTags/local_worktree/all_tags` | PASS | 15.3 |
| `TestGit/TestGitTags/local_worktree/tag_pattern` | PASS | 14.5 |
| `TestGit/TestGitTags/local_git` | PASS | 0.0 |
| `TestGit/TestGitTags/local_git/all_branches` | PASS | 13.06 |
| `TestGit/TestGitTags/local_git/branches_pattern` | PASS | 12.76 |
| `TestGit/TestGitTags/local_git/prefix-qualified_tag_pattern` | PASS | 13.28 |
| `TestGit/TestGitTags/local_git/ref-qualified_tag_pattern` | PASS | 12.86 |
| `TestGit/TestGitTags/local_git/tag_pattern` | PASS | 12.83 |
| `TestGit/TestGitTags/local_git/all_tags` | PASS | 13.71 |

The two new focused package selections ran concurrently with the final engine invocation. They exercise completed body/group guards, preservation of pending-body exclusion, ownership retry outside the lease lock, and cancellation without ownership mutation. A final source review retained Container's state latch even for whole-operation completion, because it also excludes acquisition publication; only consumed group-body locks are bypassed. That conservative adjustment restores the original state-latch exclusion and has its own new race regression below. It postdates the engine build; the engine suite was not repeated. [Exact production adjustment](probes/final-guard-adjustment.patch). The earlier completed-body log includes an intermediate Container case that was removed when preserving that exclusion; File/Directory cases and group cases remain unchanged.

### completion-core

```sh
go test -race ./core -run '^TestLazyPersistenceCompleted(Body|Group)Guards$' -count=1 -v -timeout=90s
```

Exit 0; invocation 52.702 s. [Output](logs/completion-core.log).

### ownership-dagql

```sh
go test -race ./dagql -run '^TestSnapshotOwnershipRetriesBusyOperation$' -count=1 -v -timeout=90s
```

Exit 0; invocation 57.634 s. [Output](logs/ownership-dagql.log).

### completed-container-exclusion

```sh
go test -race ./core -run '^TestLazyCompletedContainerPublicationExclusion$' -count=1 -v -timeout=90s
```

Exit 0; invocation 40.786 s. [Output](logs/completed-container-exclusion.log).


## Binding correction after the intermediate run

The Human rejected the timer-based ownership retry. It and its regression have been removed; the last combined HTTP/Git pass and ownership retry test are historical intermediate evidence, not acceptance of the current implementation. Previously accepted package/cold/warm/mixed/restart/default-policy results remain intact. The completed-Container publication-exclusion regression remains valid. STATUS.md records the exact reader-only contention and the two contract options; no new test was launched after the correction.

## Decision 1 final verification

Coordinator Decision 1 is at `bdfc380e66a0194ed26e052508802371379a6efe`, record blob `21683b4c447401c0d4d33057879459a00888f3dd`. The final implementation uses real latch waits outside graph locks. It does not contain the rejected timer retry. The blocking read is selected only by ownership synchronization; boot/import and capture/Commit retain the nonblocking path. The encoder obtains the operation pointer before its state latch to preserve the pointer-then-state lock order.

The following two focused race invocations ran concurrently with one combined engine build. The shutdown test was rerun because its encoder helper and stack assertion changed; no other previously accepted selection was repeated. The final engine invocation covers only the same six failed HTTP methods and twelve Git methods, as explicitly directed.

### owner-read-core

```sh
env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^(TestSnapshotOwnerCompletedReaders|TestSnapshotOwnerWaitsForBody|TestSnapshotOwnerWaitsForEncoder|TestContainerShutdownPersistenceWaitsForReader)$' -count=1 -v -timeout=90s
```

Exit 0; invocation 52.205 s. [Output](logs/owner-read-core.log).

### owner-read-dagql

```sh
go test -race ./dagql -run '^TestSnapshotOwnerSync(ReadSelection|InlineRead)$' -count=1 -v -timeout=90s
```

Exit 0; invocation 57.1 s. [Output](logs/owner-read-dagql.log).

### http-git-decision1

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='^(TestHTTP|TestGit)$/^(TestHTTPPermissions|TestHTTPCachePerSessions|TestHTTPTimestamp|TestHTTPChecksum|TestHTTPService|TestHTTPChecksumMismatch|TestGitUncommittedLocal|TestGitUncommittedRemote|TestGitBundleRoundTripAndStockInterop|TestGitBundleImportAfterPrerequisiteRefAdvances|TestGitCommit|TestDiscardGitDir|TestGitDepth|TestGitTags|TestGitLatest|TestGitLatestFallsBackToHead|TestGitCommitReleaseTags|TestGitCommitReleaseTagFreshness)$' --timeout=15m --test-verbose
```

Exit 0; invocation 165.832 s. Trace `6f03c497f3c291aad6eefd9e8134b7e1`. [Verbose selected output](logs/http-git-decision1-selected.log).

| Test | Result | Seconds |
|---|---|---:|
| `TestHTTP` | PASS | 0.01 |
| `TestHTTP/TestHTTPPermissions` | PASS | 13.4 |
| `TestHTTP/TestHTTPChecksum` | PASS | 13.72 |
| `TestHTTP/TestHTTPService` | PASS | 13.72 |
| `TestHTTP/TestHTTPChecksumMismatch` | PASS | 13.8 |
| `TestHTTP/TestHTTPTimestamp` | PASS | 14.16 |
| `TestHTTP/TestHTTPCachePerSessions` | PASS | 19.57 |
| `TestGit` | PASS | 0.02 |
| `TestGit/TestGitUncommittedRemote` | PASS | 1.36 |
| `TestGit/TestDiscardGitDir` | PASS | 0.24 |
| `TestGit/TestDiscardGitDir/git_dir_is_present` | PASS | 8.07 |
| `TestGit/TestDiscardGitDir/git_dir_is_not_present` | PASS | 3.6 |
| `TestGit/TestGitCommit` | PASS | 14.39 |
| `TestGit/TestGitLatest` | PASS | 15.15 |
| `TestGit/TestGitLatestFallsBackToHead` | PASS | 15.36 |
| `TestGit/TestGitUncommittedLocal` | PASS | 15.68 |
| `TestGit/TestGitCommitReleaseTags` | PASS | 15.83 |
| `TestGit/TestGitBundleImportAfterPrerequisiteRefAdvances` | PASS | 15.77 |
| `TestGit/TestGitBundleRoundTripAndStockInterop` | PASS | 16.67 |
| `TestGit/TestGitDepth` | PASS | 17.23 |
| `TestGit/TestGitCommitReleaseTagFreshness` | PASS | 17.33 |
| `TestGit/TestGitTags` | PASS | 1.5 |
| `TestGit/TestGitTags/remote` | PASS | 0.0 |
| `TestGit/TestGitTags/remote/branches_pattern` | PASS | 0.03 |
| `TestGit/TestGitTags/remote/all_branches` | PASS | 0.04 |
| `TestGit/TestGitTags/remote/prefix-qualified_tag_pattern` | PASS | 0.39 |
| `TestGit/TestGitTags/remote/ref-qualified_tag_pattern` | PASS | 0.09 |
| `TestGit/TestGitTags/remote/all_tags` | PASS | 0.83 |
| `TestGit/TestGitTags/remote/tag_pattern` | PASS | 0.13 |
| `TestGit/TestGitTags/remote_(short)` | PASS | 0.0 |
| `TestGit/TestGitTags/remote_(short)/branches_pattern` | PASS | 0.15 |
| `TestGit/TestGitTags/remote_(short)/prefix-qualified_tag_pattern` | PASS | 0.28 |
| `TestGit/TestGitTags/remote_(short)/ref-qualified_tag_pattern` | PASS | 0.07 |
| `TestGit/TestGitTags/remote_(short)/all_branches` | PASS | 0.03 |
| `TestGit/TestGitTags/remote_(short)/all_tags` | PASS | 0.83 |
| `TestGit/TestGitTags/remote_(short)/tag_pattern` | PASS | 0.14 |
| `TestGit/TestGitTags/local_git` | PASS | 0.0 |
| `TestGit/TestGitTags/local_git/all_branches` | PASS | 13.06 |
| `TestGit/TestGitTags/local_git/branches_pattern` | PASS | 13.38 |
| `TestGit/TestGitTags/local_git/prefix-qualified_tag_pattern` | PASS | 13.68 |
| `TestGit/TestGitTags/local_git/ref-qualified_tag_pattern` | PASS | 13.02 |
| `TestGit/TestGitTags/local_git/tag_pattern` | PASS | 13.09 |
| `TestGit/TestGitTags/local_git/all_tags` | PASS | 14.65 |
| `TestGit/TestGitTags/local_worktree` | PASS | 0.0 |
| `TestGit/TestGitTags/local_worktree/all_branches` | PASS | 14.86 |
| `TestGit/TestGitTags/local_worktree/branches_pattern` | PASS | 14.91 |
| `TestGit/TestGitTags/local_worktree/prefix-qualified_tag_pattern` | PASS | 15.03 |
| `TestGit/TestGitTags/local_worktree/ref-qualified_tag_pattern` | PASS | 15.0 |
| `TestGit/TestGitTags/local_worktree/all_tags` | PASS | 15.27 |
| `TestGit/TestGitTags/local_worktree/tag_pattern` | PASS | 14.9 |

Tested source/index tree: `f932ec69a60d9d74124fb7b4f48db691414cd9db`. Earlier successful cold/warm/mixed/restart/default-policy/package evidence stands under the no-repeat direction. The intermediate run with the rejected retry is retained above as history only.
