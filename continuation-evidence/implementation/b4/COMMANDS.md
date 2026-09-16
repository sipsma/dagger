# Final focused commands

Implementation tree: `521b90d51d` (tests ran against the same source before committing). Parent: `77f6279559061fd1bb6b3b18e6b08582c7b013a3`. Go 1.26.6, Linux/amd64. Final selections below ran sequentially, with no recursive package selection. Real snapshot tests used the private privileged mount namespace and did not skip.

## DagQL, core, snapshots and fixture

```sh
go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./dagql -run '^Test(Part|Scoped|ReadyPartReceipt|VisitEncodedReferences|ValueTransferPersistenceDecodePublication|CacheEvaluate(RetiresFinishedAttemptBeforeWaitersDrain|OwnCancellationOnlyCancelsOwnWait|SettlesBookkeepingBeforeReportingComplete|PendingBookkeepingSkipsUnclearedCallback)|EvaluateParts(SyncFailureRetriesOnlyBookkeepingPerGroup|SiblingGroupsRunConcurrently|OneCallRunsGroupsConcurrently))' -count=1 -v -timeout=90s

go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^Test(Part|ValueTransferCapture|ValueTransferList|Container(DirectEvaluateRunsRefinedGroups|ConcurrentGroupCompletionClearsLazyOnce|RoutingReadsRaceRefinedClear|RoutingReadsRaceUnrefinedClear))' -count=1 -v -timeout=90s

go test -race -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./engine/snapshots -run '^Test(PinSnapshotIndependentOwner|ChainContentClassification|ImportChain(FailurePrefix|ConcurrentPrefix|CanceledWaiter|LocalStores)|ImportImageSharesChainReuse)$' -count=1 -timeout=90s

go test ./core/schema -run '^TestRemoteCacheFixture$' -count=1 -timeout=90s
```

PASS: [DagQL](logs/dagql-race.log) 4.663 s; [core](logs/core-race.log) 8.254 s; [snapshots](logs/snapshot-faults-race.log) 4.234 s; [fixture schema](logs/fixture-schema.log) 0.285 s. Snapshot/fixture selections ran before the final core-only typed-field fix; their source did not subsequently change. Earlier passing decode/lifetime/native and inline selections are retained as supplemental evidence.

## Declared module field regression

The added persisted-kind assertion first failed against the existing attachment behavior:

```sh
go test ./core -run '^TestModuleObjectAttachDependencyResultsRetainsSemanticInterfaceHandleField$' -count=1 -timeout=60s
```

[Expected pre-fix failure](logs/declared-handle-before.log): `scalar_json` instead of a relocatable `result_id`. After keeping the already attached typed field:

```sh
go test -race ./core -run '^Test(ModuleObject|PersistedModuleObjectPayloadRelocation|SavedPayloadRelocation)' -count=1 -timeout=90s
```

[PASS](logs/declared-handle-after.log), 1.655 s. The regression proves the declared handle reaches the existing visitor and changes ID; existing SDK conversion/lifetime tests also pass.

## Native module proof

The default Dagger progress UI was used. Each run finished before starting the next suite.

```sh
dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecovery$' --timeout=5m

dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecoveryCold$' --timeout=5m
```

- Warm: [PASS](logs/warm-module.log), both import orders and subsequent restart/default assertions; [trace](https://dagger.cloud/dagger/traces/a2488cb947218342163d9a489b0ba81d).
- Cold: [FAIL](logs/cold-module-excerpt.log), unavailable `mount:/schema.json` during ordinary `AsModule().Serve` after import; [trace](https://dagger.cloud/dagger/traces/3d2311232b262d9242880d88c6437624). This is the final run after the typed-field correction.
- The earlier warm failure is retained in [the diagnostic excerpt](logs/warm-before-fix-excerpt.log). It stopped at the artifact file because the raw A handle escaped relocation. It is superseded by the passing warm run.
- The first cold attempt exposed a fixture-only missing compression configuration; it was fixed to use explicit uncompressed export. Its panic left peer shutdown stuck; the verified inner engine received SIGQUIT for diagnostics. Later cold runs finish normally and fail at the recorded source-availability boundary. No passing cold result is claimed.

The complete warm trace was also read with `dagger trace a2488cb947218342163d9a489b0ba81d`; the scoped test views were inspected. Only focused test evidence is committed, not full schema/bundle dumps.

## Runtime costs

These standalone final samples supersede preliminary samples:

```sh
go test ./dagql -run '^TestScopedCollectorConcurrentPublicationCost$' -bench '^(BenchmarkScopedSnapshotCollector|BenchmarkPartNativeHostEntry)$' -benchmem -benchtime=200ms -count=1 -v -timeout=90s

go test -exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestPart(ScopeRootRekeyCost|AcquisitionRootRoutes)$' -count=1 -v -timeout=90s
```

Both PASS. [Collector/host output](logs/collector-host-costs.log); [real route and boot re-key output](logs/route-rekey-costs.log). REPORT.md gives values and limitations.

## Parent probes

The existing detached worktree is at the exact parent. For the eager Container probe, copy [the source artifact](probes/eager_container_producer_test.go.txt) to `core/schema/b4_eager_container_probe_test.go` there, then run:

```sh
go test ./core/schema -run '^TestB4EagerContainerProducerProbe$' -count=1 -v -timeout=60s
```

[PASS as a gap probe](logs/parent-eager-producer.log): the actual schema eager branch captures no saved recipe and exports producer state `none`. It is not an acceptance pass for missing-part recovery. An initial fixture invocation without an explicit platform failed with `unknown`; the recorded probe specifies `linux/amd64`.

The earlier inline-role probe and step-1 logs remain historical artifacts from `21234ff019`. That probe failed as expected on the parent; Addendum 1 and the new passing scoped checks resolve it. No parent commit was changed.

## Repository checks

`gofmt -l` on changed/new Go sources produced no paths. `git diff --check` and `git diff --cached --check` passed before the fifth implementation commit. Every implementation commit carries its sign-off. The final evidence commit is separate.
