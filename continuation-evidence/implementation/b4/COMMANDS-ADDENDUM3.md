# Addendum 3 verification

Binding scratch design: `435db0d9ab177aedb2f33f5efb4a93f8dc3bc72c`, blob `a07753c805cde2889e3e7aa04570ad5f20883f13`, sections A3.1–A3.5. Both council consolidations were read in full. The Addendum 2 final design references remain those in REPORT.md.

These package selections ran sequentially on Linux/amd64 with Go 1.26.6. All 23 passed, without skips. Real snapshot cases executed under the privileged private mount namespace. Durations include invocation/build overhead; individual Go test times are in the logs. [Machine-readable results](validation-addendum3.json).

## compile

```sh
go test ./core -run ^$ -count=1 -timeout=90s
```

PASS, exit 0, 18.085 s. [Output](logs/addendum3-compile.log).

## object

```sh
go test -race ./core -run '^Test(ModuleObject|PersistedModuleObjectPayloadRelocation|SavedPayloadRelocation)' -count=1 -timeout=90s
```

PASS, exit 0, 40.629 s. [Output](logs/addendum3-object.log).

## fixture

```sh
go test ./core/schema -run '^TestRemoteCacheFixture$' -count=1 -timeout=90s
```

PASS, exit 0, 15.246 s. [Output](logs/addendum3-fixture.log).

## dagql-boundaries

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^TestPart(Decision|Decode|ReadyPreparation)' -count=1 -v -timeout=90s
```

PASS, exit 0, 4.329 s. [Output](logs/addendum3-dagql-boundaries.log).

## core-boundaries

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(PartFilesystemPublicationRoles|PartAcquisitionRootRoutes|PartTypedPublicationRoles|CapturePersistedFilesystemDirectEvaluation|FilesystemPersistenceRetainsBodyLatch)$' -count=1 -v -timeout=90s
```

PASS, exit 0, 8.469 s. [Output](logs/addendum3-core-boundaries.log).

## whole-restart

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(WholeProducerMixedRestart|NativePendingContainerRequiresRecipe)$' -count=1 -v -timeout=90s
```

PASS, exit 0, 6.047 s. [Output](logs/addendum3-whole-restart.log).

## pending-metadata

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartPendingImageMetadataStaysSelective$' -count=1 -v -timeout=90s
```

PASS, exit 0, 5.771 s. [Output](logs/addendum3-pending-metadata.log).

## ready-backreference

```sh
go test -race ./dagql -run '^TestReadyPartDonorBackreferenceReleasedBeforeSync$' -count=1 -v -timeout=90s
```

PASS, exit 0, 3.241 s. [Output](logs/addendum3-ready-backreference.log).

## dagql-full

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^Test(Part|Scoped|ReadyPartReceipt|VisitEncodedReferences|ValueTransferPersistenceDecodePublication|CacheEvaluate(RetiresFinishedAttemptBeforeWaitersDrain|OwnCancellationOnlyCancelsOwnWait|SettlesBookkeepingBeforeReportingComplete|PendingBookkeepingSkipsUnclearedCallback)|EvaluateParts(SyncFailureRetriesOnlyBookkeepingPerGroup|SiblingGroupsRunConcurrently|OneCallRunsGroupsConcurrently))' -count=1 -v -timeout=90s
```

PASS, exit 0, 8.156 s. [Output](logs/addendum3-dagql-full.log).

## core-full

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(Part|ValueTransferCapture|ValueTransferList|Container(DirectEvaluateRunsRefinedGroups|ConcurrentGroupCompletionClearsLazyOnce|RoutingReadsRaceRefinedClear|RoutingReadsRaceUnrefinedClear))' -count=1 -v -timeout=90s
```

PASS, exit 0, 18.038 s. [Output](logs/addendum3-core-full.log).

## snapshots

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./engine/snapshots -run '^Test(PinSnapshotIndependentOwner|ChainContentClassification|ImportChain(FailurePrefix|ConcurrentPrefix|CanceledWaiter|LocalStores)|ImportImageSharesChainReuse)$' -count=1 -timeout=90s
```

PASS, exit 0, 5.422 s. [Output](logs/addendum3-snapshots.log).

## declared-handle

```sh
go test ./core -run '^TestModuleObjectAttachDependencyResultsRetainsSemanticInterfaceHandleField$' -count=1 -timeout=60s
```

PASS, exit 0, 4.061 s. [Output](logs/addendum3-declared-handle.log).

## collector-costs

```sh
go test ./dagql -run '^TestScopedCollectorConcurrentPublicationCost$' -bench '^(BenchmarkScopedSnapshotCollector|BenchmarkPartNativeHostEntry)$' -benchmem -benchtime=200ms -count=1 -v -timeout=90s
```

PASS, exit 0, 3.083 s. [Output](logs/addendum3-collector-costs.log).

## route-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(ScopeRootRekeyCost|AcquisitionRootRoutes)$' -count=1 -v -timeout=90s
```

PASS, exit 0, 5.296 s. [Output](logs/addendum3-route-costs.log).

## delegation-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartDelegationRealStore/native-restart$' -count=1 -v -timeout=90s
```

PASS, exit 0, 4.794 s. [Output](logs/addendum3-delegation-costs.log).

## mount-recorders

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestRecordCompletedContainerMountProducer$' -count=1 -v -timeout=90s
```

PASS, exit 0, 6.133 s. [Output](logs/addendum3-mount-recorders.log).

## mount-schema

```sh
go test ./core/schema -run '^(TestProducerResolver(Cleanup|OutputCleanup|Capture)|TestEagerContainerMountMetadataResolvers)$' -count=1 -v -timeout=180s
```

PASS, exit 0, 31.721 s. [Output](logs/addendum3-mount-schema.log).

## scratch-codecs

```sh
go test -race ./core -run '^(TestRecordCompletedProducer|TestEagerProducerCodecs|TestEagerProducerRelocation|TestEagerProducerSaveReopen)$' -count=1 -v -timeout=90s
```

PASS, exit 0, 5.913 s. [Output](logs/addendum3-scratch-codecs.log).

## scratch-real

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core/schema -run '^TestScratchDirectory(Producer|Acquisition|NativeReopen)$' -count=1 -v -timeout=90s
```

PASS, exit 0, 34.292 s. [Output](logs/addendum3-scratch-real.log).

## scratch-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core/schema -run '^TestScratchDirectory(Producer|Acquisition|NativeReopen)$' -count=1 -v -timeout=90s
```

PASS, exit 0, 5.517 s. [Output](logs/addendum3-scratch-costs.log).

## scratch-recording-costs

```sh
go test ./core -run '^$' -bench '^BenchmarkScratchCompletedRecording$' -benchmem -benchtime=300ms -count=1 -v -timeout=90s
```

PASS, exit 0, 4.932 s. [Output](logs/addendum3-scratch-recording-costs.log).

## delegation-focused

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartDelegation' -count=1 -v -timeout=90s
```

PASS, exit 0, 9.771 s. [Output](logs/addendum3-delegation-focused.log).

## delegation-path

```sh
go test -race ./dagql -run '^TestPartDelegation' -count=1 -v -timeout=90s
```

PASS, exit 0, 3.153 s. [Output](logs/addendum3-delegation-path.log).

## Fixture mapping follow-up

The first scratch engine counter assertion incorrectly treated the importer’s root-only result mapping as the full closure. The changed-argument report itself succeeded. The gated fixture now reports the current allocation for the complete closure, retaining roots first; production ImportValues semantics are unchanged. A real two-row fixture import regression verifies the dependency mapping against the imported recorded receiver.

```sh
go test ./core/schema -run '^TestRemoteCacheFixture$' -count=1 -v -timeout=90s
```

PASS. [Output](logs/addendum3-fixture-final.log).

## Test construction corrections

The first resolver cleanup run exposed a mock that injected release errors only under the recorder overlay; the mock now also injects the requested error on the wrapping-failure branch. The shared recorder/cleanup selection subsequently passes. [Initial mock failure](logs/addendum3-cleanup-mock-before.log). An initial real-store test compile used non-public/incorrect fixture members; the corrected test uses fixture rows for dependency IDs, imports a ValueBundle value, and reads accessors with Peek. No failed construction is counted as acceptance.

The historical scratch gap probe asserted the old cold failure. Its role is superseded by the positive scratch real-store, pending-restart and ownership checks; it is not copied onto the corrected source with obsolete expectations. Historical evidence remains unchanged.

## Engine verification and observations

The cold, warm and mixed selections ran sequentially against the final implementation source. Invocation times include compilation, engine startup and cleanup. Their selected Go tests passed without skips. The exact engine command manifest and default-policy disposition are recorded separately.

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecoveryCold$' --timeout=5m --test-verbose
```

PASS, exit 0; 316.715 s invocation, 212.04 s selected test; trace `3df9e6ae55a1d589249c550bb46a3bb4`. [CLI output](logs/addendum3-cold-engine.log).

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecovery$' --timeout=5m --test-verbose
```

PASS, exit 0; 524.731 s invocation, 270.93 s selected test; trace `47888a25c79c32647da1c39d700064ba`. [CLI output](logs/addendum3-warm-engine.log).

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestPartMixedExecOutputs$' --timeout=5m --test-verbose
```

PASS, exit 0; 146.698 s invocation, 70.81 s selected test; trace `d5215ba69b99ccefb25e1cf482b1356a`. [CLI output](logs/addendum3-mixed-engine.log).

The cold scratch counter is read after the changed-argument call and after repeated demand. It records one entry for the current imported no-receiver `directory` row; warm records zero in both import orders. The full 226-row metadata closure and exact counters are [cold closure](probes/cold-addendum3-closure.json), [cold counters](probes/cold-addendum3-counters.json), [warm counters](probes/warm-addendum3-counters.json). Every row is included; large recipe bytes are summarized by length/hash. The closure comes from the unmodified export copied into the cold test's explicit foreign-context control, before its expired-Module variant. Raw schema/blob dumps stay outside committed evidence.

Passing test observations were fetched read-only with `dagger cloud logs <trace-id> -o /tmp/<run>-trace.log`. Full-trace logs work without the older failing `--test` filter. Only focused counters and control summaries are retained: [cold](logs/addendum3-cold-counters.log), [warm](logs/addendum3-warm-counters.log), [mixed](logs/addendum3-mixed-counters.log).

## Default-policy diagnostic

The existing variant was selected with the same opt-in file containing only `_DAGGER_TEST_REMOTE_CACHE_PRUNE_DIAGNOSTIC=1`:

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic$' --timeout=5m --test-verbose --env-file=file:/tmp/b4-a3-default.env
```

The initial invocation exited 0 but **skipped at the opt-in gate** (41.479 s invocation, 0.00 s test). `engine-dev test` mounts that secret file without exporting it into the runner's environment. This is not counted as a pass. [Skip output](logs/addendum3-default-unset-counters.log).

The same selection then ran with a temporary [one-line runner-only environment setting](probes/default-policy-runner.patch), scoped to the env-file branch. Neither production engine code nor the integration test, its disk policy, allocation cap, import order or selection changed. The retained patch uses zero context (`git apply --unidiff-zero`); its applicability was checked without applying it. The runner file was restored byte-for-byte in a finally block; the final implementation contains no runner modification.

**PASS without skips**, exit 0; 202.641 s invocation, 128.40 s selected test; trace `51c009ebcf25f7674d15a3e2a4e0d025`. It completed the matching/changed report, warm scratch, note File and bound-tool controls, then requested 3,591,062,016 bytes of pressure under its 8 GiB cap. The default policy removed 17 persisted roots, explicitly including saved report row 4697. Reopen had no persistence/local-cache reset, and that root was absent. [CLI output](logs/addendum3-default-policy-opted-in.log), [observations](logs/addendum3-default-policy-counters.log), [counter data](probes/default-policy-addendum3-counters.json). This is a reached pruning control; the earlier unconfigured invocation is kept separately as a skip.

[All engine commands and dispositions](validation-addendum3-engines.json).

## Historical parent probe

After the engine selections, this exact previously passing gap probe ran again in `/tmp/dagger-b4-inline-probe-77f62795`, verified at unchanged HEAD `77f6279559061fd1bb6b3b18e6b08582c7b013a3`:

```sh
go test ./core/schema -run '^TestB4EagerContainerProducerProbe$' -count=1 -v -timeout=60s
```

PASS as a historical gap probe, 0.536 s package runtime. [Output](logs/addendum3-parent-eager-probe.log). Its old missing-recipe observation remains consistent with the implemented parent-delegation extension; it is not a missing-part recovery acceptance result.

## Repository and evidence checks

Code tip is `78eab473803a5f5405bf0d315359bed4b0aa1270`, the council's round-1 pin. The final separate evidence commit changes no code. All 25 commits above the integrated parent have sign-offs, and the parent remains an ancestor. All 12 Go files changed in this dispatch are formatted. `git diff --check` passes; all new JSON artifacts parse and local Markdown links resolve. The full evidence directory was scanned case-insensitively for the prohibited wording and all three findings were reworded in this evidence commit. No history was rewritten. The original five implementation commits and all subsequent commits are listed in [COMMITS.md](COMMITS.md).

Committed log excerpts normalize whitespace only; recorded values and outcomes are unchanged.
