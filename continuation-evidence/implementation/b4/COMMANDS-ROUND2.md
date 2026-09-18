# Council round 2 verification

Source: `f04a91577bd4ddc574f3bc527cdffe9fec2179a1`, above the unchanged integrated parent. All package selections ran sequentially with Go 1.26.6 on Linux/amd64. Privileged real-store selections used a private mount namespace. No broad recursive selection was run. The 23 selections below passed without skips.

## compile

```sh
go test ./core -run ^$ -count=1 -timeout=90s
```

PASS, 18.258 s including Go invocation/build overhead. [Output](logs/round2-compile.log).

## object

```sh
go test -race ./core -run '^Test(ModuleObject|PersistedModuleObjectPayloadRelocation|SavedPayloadRelocation)' -count=1 -timeout=90s
```

PASS, 5.846 s including Go invocation/build overhead. [Output](logs/round2-object.log).

## fixture

```sh
go test ./core/schema -run '^TestRemoteCacheFixture$' -count=1 -timeout=90s
```

PASS, 4.337 s including Go invocation/build overhead. [Output](logs/round2-fixture.log).

## dagql-boundaries

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^TestPart(Decision|Decode|ReadyPreparation)' -count=1 -v -timeout=90s
```

PASS, 40.073 s including Go invocation/build overhead. [Output](logs/round2-dagql-boundaries.log).

## core-boundaries

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(PartFilesystemPublicationRoles|PartAcquisitionRootRoutes|PartTypedPublicationRoles|CapturePersistedFilesystemDirectEvaluation|FilesystemPersistenceRetainsBodyLatch)$' -count=1 -v -timeout=90s
```

PASS, 8.910 s including Go invocation/build overhead. [Output](logs/round2-core-boundaries.log).

## whole-restart

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(WholeProducerMixedRestart|NativePendingContainerRequiresRecipe)$' -count=1 -v -timeout=90s
```

PASS, 6.664 s including Go invocation/build overhead. [Output](logs/round2-whole-restart.log).

## pending-metadata

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartPendingImageMetadataStaysSelective$' -count=1 -v -timeout=90s
```

PASS, 6.881 s including Go invocation/build overhead. [Output](logs/round2-pending-metadata.log).

## ready-backreference

```sh
go test -race ./dagql -run '^TestReadyPartDonorBackreferenceReleasedBeforeSync$' -count=1 -v -timeout=90s
```

PASS, 8.082 s including Go invocation/build overhead. [Output](logs/round2-ready-backreference.log).

## dagql-full

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^Test(Part|Scoped|ReadyPartReceipt|VisitEncodedReferences|ValueTransferPersistenceDecodePublication|CacheEvaluate(RetiresFinishedAttemptBeforeWaitersDrain|OwnCancellationOnlyCancelsOwnWait|SettlesBookkeepingBeforeReportingComplete|PendingBookkeepingSkipsUnclearedCallback)|EvaluateParts(SyncFailureRetriesOnlyBookkeepingPerGroup|SiblingGroupsRunConcurrently|OneCallRunsGroupsConcurrently))' -count=1 -v -timeout=90s
```

PASS, 14.817 s including Go invocation/build overhead. [Output](logs/round2-dagql-full.log).

## core-full

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(Part|ValueTransferCapture|ValueTransferList|Container(DirectEvaluateRunsRefinedGroups|ConcurrentGroupCompletionClearsLazyOnce|RoutingReadsRaceRefinedClear|RoutingReadsRaceUnrefinedClear))' -count=1 -v -timeout=90s
```

PASS, 27.073 s including Go invocation/build overhead. [Output](logs/round2-core-full.log).

## snapshots

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./engine/snapshots -run '^Test(PinSnapshotIndependentOwner|ChainContentClassification|ImportChain(FailurePrefix|ConcurrentPrefix|CanceledWaiter|LocalStores)|ImportImageSharesChainReuse)$' -count=1 -timeout=90s
```

PASS, 8.248 s including Go invocation/build overhead. [Output](logs/round2-snapshots.log).

## declared-handle

```sh
go test ./core -run '^TestModuleObjectAttachDependencyResultsRetainsSemanticInterfaceHandleField$' -count=1 -timeout=60s
```

PASS, 4.716 s including Go invocation/build overhead. [Output](logs/round2-declared-handle.log).

## collector-costs

```sh
go test ./dagql -run '^TestScopedCollectorConcurrentPublicationCost$' -bench '^(BenchmarkScopedSnapshotCollector|BenchmarkPartNativeHostEntry)$' -benchmem -benchtime=200ms -count=1 -v -timeout=90s
```

PASS, 35.924 s including Go invocation/build overhead. [Output](logs/round2-collector-costs.log).

## route-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(ScopeRootRekeyCost|AcquisitionRootRoutes)$' -count=1 -v -timeout=90s
```

PASS, 7.989 s including Go invocation/build overhead. [Output](logs/round2-route-costs.log).

## delegation-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartDelegationRealStore/native-restart$' -count=1 -v -timeout=90s
```

PASS, 5.878 s including Go invocation/build overhead. [Output](logs/round2-delegation-costs.log).

## mount-recorders

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestRecordCompletedContainerMountProducer$' -count=1 -v -timeout=90s
```

PASS, 6.834 s including Go invocation/build overhead. [Output](logs/round2-mount-recorders.log).

## mount-schema

```sh
go test ./core/schema -run '^(TestProducerResolver(Cleanup|OutputCleanup|Capture)|TestEagerContainerMountMetadataResolvers)$' -count=1 -v -timeout=180s
```

PASS, 47.503 s including Go invocation/build overhead. [Output](logs/round2-mount-schema.log).

## scratch-codecs

```sh
go test -race ./core -run '^(TestRecordCompletedProducer|TestEagerProducerCodecs|TestEagerProducerRelocation|TestEagerProducerSaveReopen)$' -count=1 -v -timeout=90s
```

PASS, 7.844 s including Go invocation/build overhead. [Output](logs/round2-scratch-codecs.log).

## scratch-real

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core/schema -run '^TestScratchDirectory(Producer|Acquisition|NativeReopen)$' -count=1 -v -timeout=90s
```

PASS, 88.900 s including Go invocation/build overhead. [Output](logs/round2-scratch-real.log).

## scratch-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core/schema -run '^TestScratchDirectory(Producer|Acquisition|NativeReopen)$' -count=1 -v -timeout=90s
```

PASS, 7.820 s including Go invocation/build overhead. [Output](logs/round2-scratch-costs.log).

## scratch-recording-costs

```sh
go test ./core -run '^$' -bench '^BenchmarkScratchCompletedRecording$' -benchmem -benchtime=300ms -count=1 -v -timeout=90s
```

PASS, 5.749 s including Go invocation/build overhead. [Output](logs/round2-scratch-recording-costs.log).

## delegation-focused

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartDelegation' -count=1 -v -timeout=90s
```

PASS, 11.132 s including Go invocation/build overhead. [Output](logs/round2-delegation-focused.log).

## delegation-path

```sh
go test -race ./dagql -run '^TestPartDelegation' -count=1 -v -timeout=90s
```

PASS, 6.478 s including Go invocation/build overhead. [Output](logs/round2-delegation-path.log).

## Decision-focused checks

These preceded the complete final rerun. Every decision commit was built and checked before the next commit. Their cases are also included in the final selections above.

```sh
go test -race ./core -run '^Test(ModuleObject|PersistedModuleObjectPayloadRelocation|SavedPayloadRelocation)' -count=1 -timeout=90s
```

PASS. [Output](logs/round2-d1.log).

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^TestPartSessionless' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-d10.log).

```sh
go test -race ./dagql -run '^TestPart(Source|DecisionFinalSource)' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-d7.log).

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^TestPart(ImportChainRefCleanupHandoff|AdmittedChainLifetime)$' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-d5.log).

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(AcquisitionRootRoutes|DelegationRealStore)$' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-d6.log).

```sh
go test -race ./dagql -run '^TestPart(FixedProviderAndContentExhaustion|OfferReplacementNotExhausted)$' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-d2.log).

```sh
go test -race ./dagql -run '^Test(PartNativeCompletionRetiresOffer|PartSettlementRetiresReplacement|CacheEvaluate(Sync|PendingBookkeeping|SettlesBookkeeping)|EvaluateParts(SyncFailureRetriesOnlyBookkeepingPerGroup|SiblingGroupsRunConcurrently|OneCallRunsGroupsConcurrently))' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-d3.log).

```sh
go test -race ./dagql -run '^TestPartFixedProvider' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-d8.log).

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(Part(ContainerMixedRestart|WholeProducerMixedRestart|NativePendingContainerRequiresRecipe)|Container(DirectEvaluateRunsRefinedGroups|ConcurrentGroupCompletionClearsLazyOnce|RoutingReadsRaceRefinedClear|RoutingReadsRaceUnrefinedClear)|RecordCompletedContainerMountProducer)$' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-d4.log).

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^TestPart(Gate|Source|Sessionless|Decision)' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-d9.log).

```sh
go test -race ./core -run '^TestPartFixtureReleaseObserverPrivateFS$' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-notes-private-ref.log).

```sh
go test ./core/schema -run '^TestRemoteCacheFixture$' -count=1 -v -timeout=90s
```

PASS. [Output](logs/round2-notes-fixture.log).

## Inline-map before/after regression

Only `core/object.go` was temporarily replaced by its preceding `e1443fff63` content; the new regression and all other source remained current. The file was restored byte-for-byte in a finally block before the final verification. No commit was amended.

```sh
go test -race ./core -run '^TestModuleObjectAttachDependencyResultsPreservesInlineMap$' -count=1 -v -timeout=90s
```

Expected failure: the broad retention returned an attached Child handle instead of the original inline map. [Before output](logs/round2-d1-inline-before.log), [overlay record](probes/round2-inline-map-before.json). The narrowed implementation passes in the final object selection above. The original declared-handle relocation regression is retained and passes as well.

During authoring, the direct attachment fixture initially used an unattached synthetic parent, then an SDK conversion assertion requiring an attached parent; the regression was corrected to check raw ParentFields and retain a real dependency via an independent child call. Edit/type typos caused compile failures before correction, and an early HTTP check overlapped an unfinished local Container cleanup edit; that edit was set aside and the HTTP selection was rerun before committing. These construction checks are not acceptance passes.

## Engine proofs

These ran sequentially after all 23 package selections, with the default progress UI. Each selected test passed without skips. Service-stop ERROR lines are cleanup observations; test summaries and CLI exit codes establish the result.

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecoveryCold$' --timeout=5m --test-verbose
```

PASS; 179.13 s selected test, 451.179 s invocation, trace `d0b36d6dddc1270841e7847f6230c58f`. [CLI](logs/round2-cold-engine.log), [observations](logs/round2-cold-engine-counters.log), [counters](probes/round2-cold-engine-counters.json).

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecovery$' --timeout=5m --test-verbose
```

PASS; 249.14 s selected test, 294.029 s invocation, trace `0a22a548c1ad35d8f605611e47fde4da`. [CLI](logs/round2-warm-engine.log), [observations](logs/round2-warm-engine-counters.log), [counters](probes/round2-warm-engine-counters.json).

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestPartMixedExecOutputs$' --timeout=5m --test-verbose
```

PASS; 71.14 s selected test, 136.196 s invocation, trace `937a5d528c9b7d49fa4e6fb4919b5477`. [CLI](logs/round2-mixed-engine.log), [observations](logs/round2-mixed-engine-counters.log), [counters](probes/round2-mixed-engine-counters.json).

```sh
dagger -vv api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic$' --timeout=5m --test-verbose --env-file=file:/tmp/b4-r2-default.env
```

PASS; 132.22 s selected test, 212.751 s invocation, trace `45ff862b676437da203f629e84dd71f0`. [CLI](logs/round2-default-policy-opted-in.log), [observations](logs/round2-default-policy-opted-in-counters.log), [counters](probes/round2-default-policy-opted-in-counters.json).

The default-policy diagnostic used the already documented one-line [runner overlay](probes/default-policy-runner.patch) to export `_DAGGER_TEST_REMOTE_CACHE_PRUNE_DIAGNOSTIC=1` when the flag-only env file is supplied. The runner file was restored byte-for-byte in a finally block. No unconfigured or skipped invocation is counted as a round 2 pass. Its pruning outcome is separate from the cold proof's non-pruning restart.

The complete cold metadata closure, rather than a root-only projection, is retained in [the 226-value inventory](probes/round2-cold-closure.json). Full schema dumps and recipe bodies are omitted; metadata, exact ordinals and imported IDs, empty scratch recipe, and recipe byte counts/hashes remain.

Read-only log retrieval used the full trace because the default CLI report hides passing test logs:

```sh
dagger cloud logs d0b36d6dddc1270841e7847f6230c58f -o /tmp/b4-r2-validation/cold-engine-trace.log
dagger cloud logs 0a22a548c1ad35d8f605611e47fde4da -o /tmp/b4-r2-validation/warm-engine-trace.log
dagger cloud logs 937a5d528c9b7d49fa4e6fb4919b5477 -o /tmp/b4-r2-validation/mixed-engine-trace.log
dagger cloud logs 45ff862b676437da203f629e84dd71f0 -o /tmp/b4-r2-validation/default-policy-opted-in-trace.log
```

## Historical parent gap probe

The existing detached worktree remained at exact parent `77f6279559061fd1bb6b3b18e6b08582c7b013a3`. Its existing probe was rerun without changing parent files or commits.

```sh
go test ./core/schema -run '^TestB4EagerContainerProducerProbe$' -count=1 -v -timeout=60s
```

PASS as a historical gap probe, not a missing-part recovery acceptance pass. [Output](logs/round2-parent-eager-probe.log).

## Repository checks

All 94 changed Go sources are formatted. The specified parent remains an ancestor, every commit above it has a sign-off, and no reviewed commit was amended. Whitespace, local artifact links, JSON artifacts, and the case-insensitive prohibited-word scan were checked before the separate evidence commit. The final worktree is clean.
