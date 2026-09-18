# Verification commands

## Round 2

The tested implementation tip is `688bef3408`; the new commits are recorded in `ROUND2-COMMITS.txt`. The engine-debugging skill was read again before running tests. All commands ran sequentially, with no edits to Go files during the unfiltered run. `GOFLAGS` was empty.

```sh
go test -c ./core -o /tmp/b1-r2-core.test
sudo -n unshare --mount --propagation private /tmp/b1-r2-core.test -test.run '^TestGitBundleCompletedProducerEvaluate$' -test.count=1 -test.v

go test -o /tmp/b1-r2-core.test ./core -run '^TestRecordCompletedProducer$' -count=1 -v
go test ./core/schema -run '^TestProducerResolverCleanup$' -count=1 -v

env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core ./core/schema ./dagql -count=1
```

The final command is I3's unfiltered package run. `-p=1` serializes packages; `-exec` gives each test binary mount privileges inside a private mount namespace. Neither flag filters tests. The preserved Go paths let the nested test-only build-overlay fixture use the existing toolchain and dependency cache. The fixture builds only its five injection cases in the child process, while the outer package run remains unfiltered.

The first I1 fixture run failed because its RemoteGitRepository omitted a platform; adding the ordinary Linux/amd64 platform made that saved row valid. The final I1 log records the complete bundle test, including the added moving-hint case.

## Round 1

Run from the repository root. All selections below completed successfully and ran sequentially. The final logs are alongside this file. Compilation and successful earlier commit checks are recorded in `logs/builds.txt`; initial cleanup and recording logs are retained separately.

```sh
go test -o /tmp/b1-core.test ./core -run '^(TestRecordCompletedProducer|TestEagerProducerCodecs|TestEagerProducerRelocation|TestEagerProducerSaveReopen|TestCompletedProducerAttachmentBeforePublication|TestMoveProducedOutputs|TestProducerPathCleanup|TestProducerTemporaryIndexCleanup)$' -count=1 -v

go test ./core/schema -run '^(TestProducerResolverCleanup|TestProducerResolverOutputCleanup|TestProducerResolverCapture)$' -count=1 -v

go test -c ./core -o /tmp/b1-core.test
sudo -n unshare --mount --propagation private /tmp/b1-core.test -test.run '^TestGitCompletedProducersEvaluate$' -test.count=1 -test.v
sudo -n unshare --mount --propagation private /tmp/b1-core.test -test.run '^TestGitCompletedProducersRemoteEvaluate$' -test.count=1 -test.v
sudo -n unshare --mount --propagation private /tmp/b1-core.test -test.run '^TestGitBundleCompletedProducerEvaluate$' -test.count=1 -test.v
sudo -n unshare --mount --propagation private /tmp/b1-core.test -test.run '^TestHTTPCompletedProducerEvaluate$' -test.count=1 -test.v
sudo -n unshare --mount --propagation private /tmp/b1-core.test -test.run '^TestHTTPProducerCleanup$' -test.count=1 -test.v
sudo -n unshare --mount --propagation private /tmp/b1-core.test -test.run '^TestAuditedEagerProducersEvaluate$' -test.count=1 -test.v

go test -race -c ./core -o /tmp/b1-core-race.test
sudo -n unshare --mount --propagation private /tmp/b1-core-race.test -test.run '^(TestHTTPStateConcurrentResolveCapture|TestStatelessHTTPProducerIsolation|TestCompletedProducerConcurrentDemand)$' -test.count=1 -test.v
sudo -n unshare --mount --propagation private /tmp/b1-core-race.test -test.run '^TestHTTPProducerWriter$' -test.count=1 -test.v

dagger api call engine-dev test --pkg ./core/integration --run='TestGit/(TestGitUncommittedLocal|TestGitUncommittedRemote|TestGitBundleRoundTripAndStockInterop|TestGitBundleImportAfterPrerequisiteRefAdvances|TestGitCommit|TestDiscardGitDir|TestGitDepth|TestGitTags)$'
dagger api call engine-dev test --pkg ./core/integration --run='TestHTTP/(TestHTTPName|TestHTTPPermissions|TestHTTPChecksum|TestHTTPChecksumMismatch|TestHTTPTimestamp|TestHTTPETag|TestHTTPCachePerSessions|TestHTTPAuth|TestHTTPService)$'
dagger api call engine-dev test --pkg ./core/integration --run='TestCachePersistence/TestDiskPersistenceAcrossRestart/eager_producers_survive_restart$'

git diff --check
```

The installed CLI was `v1.0.0-beta.13`. Dev-engine output used the default UI and was redirected to bounded `/tmp/b1-*.log` files. The UI summaries show top-level test counts; their exact method/subtest filters are included above. Service teardown spans can appear as errors while the command's test result remains `PASSED` with exit status zero.

The cleanup test locates `strace` and launches its own timestamp child with `-f -qq -e trace=utimensat -e inject=utimensat:error=EIO:when=1`. Following Go's threads is required for this fault. The real filesystem fixture additionally creates an isolated mount namespace for Git's execution context, preventing temporary resolver mounts from propagating to the host.
