# Addendum 2 verification

Binding design text: producer `d674bea3d6f270c8fe3b2118b87bda754e26a89e` (blob `3e254e4d36a9cd80848146b223067f84d2c98ad3`), acquisition `7af104b49aad9dd4c9a02b42b9d2152e59a9c8fe` (blob `d63729442180a2d7faf872b687bf8d64cf19083b`); consolidation `a8c33ab8e73ae64a37a07042a6e1a0adf0a4211d` read in full.

All commands below ran sequentially on the Addendum 2 implementation, using Go 1.26.6 on Linux/amd64. Real-store tests used the privileged private mount namespace. The full core/DagQL/snapshot selections executed without skips. The exact command/result manifest is [validation-addendum2.json](validation-addendum2.json).

## compile

```sh
go test ./core -run ^$ -count=1 -timeout=90s
```

PASS, 21.821 s including Go invocation/build overhead. [Output](logs/addendum2-compile.log).

## object

```sh
go test -race ./core -run '^Test(ModuleObject|PersistedModuleObjectPayloadRelocation|SavedPayloadRelocation)' -count=1 -timeout=90s
```

PASS, 5.899 s including Go invocation/build overhead. [Output](logs/addendum2-object.log).

## fixture

```sh
go test ./core/schema -run '^TestRemoteCacheFixture$' -count=1 -timeout=90s
```

PASS, 31.769 s including Go invocation/build overhead. [Output](logs/addendum2-fixture.log).

## dagql-boundaries

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^TestPart(Decision|Decode|ReadyPreparation)' -count=1 -v -timeout=90s
```

PASS, 25.866 s including Go invocation/build overhead. [Output](logs/addendum2-dagql-boundaries.log).

## core-boundaries

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(PartFilesystemPublicationRoles|PartAcquisitionRootRoutes|PartTypedPublicationRoles|CapturePersistedFilesystemDirectEvaluation|FilesystemPersistenceRetainsBodyLatch)$' -count=1 -v -timeout=90s
```

PASS, 8.494 s including Go invocation/build overhead. [Output](logs/addendum2-core-boundaries.log).

## whole-restart

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(WholeProducerMixedRestart|NativePendingContainerRequiresRecipe)$' -count=1 -v -timeout=90s
```

PASS, 6.172 s including Go invocation/build overhead. [Output](logs/addendum2-whole-restart.log).

## pending-metadata

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartPendingImageMetadataStaysSelective$' -count=1 -v -timeout=90s
```

PASS, 5.886 s including Go invocation/build overhead. [Output](logs/addendum2-pending-metadata.log).

## ready-backreference

```sh
go test -race ./dagql -run '^TestReadyPartDonorBackreferenceReleasedBeforeSync$' -count=1 -v -timeout=90s
```

PASS, 3.166 s including Go invocation/build overhead. [Output](logs/addendum2-ready-backreference.log).

## dagql-full

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^Test(Part|Scoped|ReadyPartReceipt|VisitEncodedReferences|ValueTransferPersistenceDecodePublication|CacheEvaluate(RetiresFinishedAttemptBeforeWaitersDrain|OwnCancellationOnlyCancelsOwnWait|SettlesBookkeepingBeforeReportingComplete|PendingBookkeepingSkipsUnclearedCallback)|EvaluateParts(SyncFailureRetriesOnlyBookkeepingPerGroup|SiblingGroupsRunConcurrently|OneCallRunsGroupsConcurrently))' -count=1 -v -timeout=90s
```

PASS, 8.544 s including Go invocation/build overhead. [Output](logs/addendum2-dagql-full.log).

## core-full

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(Part|ValueTransferCapture|ValueTransferList|Container(DirectEvaluateRunsRefinedGroups|ConcurrentGroupCompletionClearsLazyOnce|RoutingReadsRaceRefinedClear|RoutingReadsRaceUnrefinedClear))' -count=1 -v -timeout=90s
```

PASS, 17.960 s including Go invocation/build overhead. [Output](logs/addendum2-core-full.log).

## snapshots

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./engine/snapshots -run '^Test(PinSnapshotIndependentOwner|ChainContentClassification|ImportChain(FailurePrefix|ConcurrentPrefix|CanceledWaiter|LocalStores)|ImportImageSharesChainReuse)$' -count=1 -timeout=90s
```

PASS, 5.507 s including Go invocation/build overhead. [Output](logs/addendum2-snapshots.log).

## declared-handle

```sh
go test ./core -run '^TestModuleObjectAttachDependencyResultsRetainsSemanticInterfaceHandleField$' -count=1 -timeout=60s
```

PASS, 4.162 s including Go invocation/build overhead. [Output](logs/addendum2-declared-handle.log).

## collector-costs

```sh
go test ./dagql -run '^TestScopedCollectorConcurrentPublicationCost$' -bench '^(BenchmarkScopedSnapshotCollector|BenchmarkPartNativeHostEntry)$' -benchmem -benchtime=200ms -count=1 -v -timeout=90s
```

PASS, 11.721 s including Go invocation/build overhead. [Output](logs/addendum2-collector-costs.log).

## route-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(ScopeRootRekeyCost|AcquisitionRootRoutes)$' -count=1 -v -timeout=90s
```

PASS, 6.001 s including Go invocation/build overhead. [Output](logs/addendum2-route-costs.log).

## delegation-costs

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartDelegationRealStore/native-restart$' -count=1 -v -timeout=90s
```

PASS, 4.694 s including Go invocation/build overhead. [Output](logs/addendum2-delegation-costs.log).

## mount-recorders

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestRecordCompletedContainerMountProducer$' -count=1 -v -timeout=90s
```

PASS, 5.989 s including Go invocation/build overhead. [Output](logs/addendum2-mount-recorders.log).

## mount-schema

```sh
go test ./core/schema -run '^(TestProducerResolver(Cleanup|OutputCleanup|Capture)|TestEagerContainerMountMetadataResolvers)$' -count=1 -v -timeout=180s
```

PASS, 35.368 s including Go invocation/build overhead. [Output](logs/addendum2-mount-schema.log).

## Additional focused checks

`go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPartDelegation' -count=1 -v -timeout=90s` — PASS. [Output](logs/addendum2-delegation-focused.log).

`go test -race ./dagql -run '^TestPartDelegation' -count=1 -v -timeout=90s` — PASS; also covered again by the full DagQL selection. [Output](logs/addendum2-delegation-path.log).

The real resolver recording-rejection overlay additionally covers overwritten mount clones; [output](logs/addendum2-shadow-cleanup.log). Its fixture implements empty-store stale-lease reconciliation, which the earlier mock did not provide after Addendum 1. This is test infrastructure only.

Initial test construction corrections: a private mount invocation used the wrong method name before compilation, a pure route fixture omitted its platform, an overlay local variable collided with the real guard, and the negative mount-kind test initially edited the parent ordinal instead of the child. The parent-kind check initially compared the codec against `Container`; the actual registered name is `core.Container`, now checked correctly. No failing construction is counted as a passing check.

## Scratch gap probe

The probe source is preserved as [scratch_acquisition_gap_test.go.txt](probes/scratch_acquisition_gap_test.go.txt). Copy it temporarily to `core/schema/b4_scratch_gap_probe_test.go` and run:

```sh
go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core/schema -run '^TestB4ScratchAcquisitionGapProbe$' -count=1 -v -timeout=90s
```

PASS as a gap probe: cold exact demand fails despite local canonical scratch storage; the warm ordinary Directory row succeeds. [Output](logs/scratch-gap-probe.log). The temporary file was removed after the run. This does not change or repair the cold engine test.

## Final engine selections

These ran sequentially after all 17 implementation package commands. They used the default progress UI and the same implementation source, including the final route logging committed as `4ccf106dd4`. Durations below include build/startup/cleanup, not just the selected Go test. [Exact result manifest](validation-addendum2-engines.json).

```sh
dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecovery$' --timeout=5m --test-verbose

dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestPartMixedExecOutputs$' --timeout=5m --test-verbose

dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecoveryCold$' --timeout=5m --test-verbose
```

- Warm: PASS, exit 0, 377.405 s; trace ID `8cddde1419dc723e9cddead086ae3462`. [Output](logs/addendum2-warm-engine.log).
- Mixed actual exec: PASS, exit 0, 111.400 s; trace ID `0967fe7633b146e9202c701e625d3e1d`. [Output](logs/addendum2-mixed-engine.log).
- Cold: FAIL, exit 1, 195.036 s; selected test 153.45 s; trace ID `075421afee8f5ae279ddfbd314ce854c`. [Focused output](logs/cold-addendum2-final-excerpt.log). Matching report, selected artifact, SDK/Host route counters and echo control pass before the unchanged changed-argument control reaches [blocker 3](BLOCKER-3.md). The independent foreign-context subtest passes. Later cold restart/default controls are not reached.

The warm/mixed service-stop `ERROR` lines are cleanup observations; their selected test summaries and CLI exit codes are passing. Read-only `dagger cloud logs <trace-id> --test <selected-test-name>` attempts returned `no test named` for these two traces, so no new mixed-demand timing is claimed from them. The committed CLI outputs and exit manifest establish their results. Cold failure output directly includes the exact route counters and assertion stack; full schema/bundle dumps are omitted from evidence.

The first post-Addendum cold run used the same command and failed at the same scratch boundary (154.12 s selected test, trace ID `153bdebcb2fa21f394410d472b883849`). Its [excerpt](logs/cold-addendum2-excerpt.log) and [closure projection](probes/cold-addendum2-closure-summary.json) remain separately labelled; they are not substituted for the final rerun.

## Historical parent selection rerun

After the engine selections, the previously passing parent gap probe was rerun in `/tmp/dagger-b4-inline-probe-77f62795`, whose HEAD was verified as `77f6279559061fd1bb6b3b18e6b08582c7b013a3`. Its existing probe file was used; no parent files or commits were changed.

```sh
go test ./core/schema -run '^TestB4EagerContainerProducerProbe$' -count=1 -v -timeout=60s
```

PASS as the historical gap probe (0.489 s package runtime). [Output](logs/addendum2-parent-eager-probe.log). The actual eager `withWorkdir` result has consumed metadata and no saved recipe, matching the blocker-2 finding addressed by the binding delegation design.

## Repository checks

Implementation tip before the separate evidence commit: `4ccf106dd4`. All seven new implementation commits above `fbd4013309` carry sign-offs. The specified parent remains an ancestor. `gofmt -l` reports no changes for all 20 Go files changed in this dispatch; `git diff --check` passes. All local links in the report, blocker and command ledgers resolve, and both result manifests and the closure projection parse as JSON. The scratch probe source is retained only as an evidence artifact, with no temporary test left in the implementation tree.
