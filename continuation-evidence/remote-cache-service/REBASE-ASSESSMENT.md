# Rebase assessment: our engine work onto the Successor workstream's tip

Rebase engine (a fork of the Engine seat), 18 September 2026.

## Inputs

- Our input: branch `engine-seat-0bf270c0` at `eb71d1b54c`, 27 commits on
  base `c5b299142c` (E3 through E10, the cached module definition, and five
  evidence commits; E11 and E12 were not yet committed when this track
  forked and are not part of this rebase).
- Their tip: `92b8057912` on `b7-integration-author-b` (production tree
  identical to `90e34e09a3`), base `c5b299142c`, 130 commits, 193 files.
  Reports: `continuation-evidence/implementation-continuation/b6-continuation/CLOSING.md`
  at `bb799b6dc1` and `.../b7-continuation/CLOSING.md` at `e5c5331500`
  (branch `coordinator-coordination-13180ce3`);
  `continuation-evidence/implementation/b7/REPORT.md` at `10d3288e32`.
- Result: branch `rebase-engine-on-92b8057912`, every commit cherry-picked
  with `-x` in order.

## What they changed in production code

Forty non-test Go files. By their coordinator's list, checked against the
diffs:

- `959054a2e0` dagql/cache_part_source.go: `scanPartSources` no longer
  keeps a candidate whose probe is not ready.
- `e094252906`, `f9db98a420`, `af2ddb4e36` dagql/cache_part_refusal.go,
  cache_part_demand.go, cache_part_reselect_watch.go and the seven retry
  loops: every reselect refusal names its site (`partRefused(...)`
  replaces bare `ErrPartReselect`), a demand refused twice at the same
  counters fails with `ErrPartNoProgress`.
- `e844245c8b` dagql/cache_part_content.go `Provider`: the address map is
  always allocated, so a key-only offer's first renewal does not panic.
- `1bfece3b77` core/container.go, core/directory.go: cloning a
  part-acquired File or Directory into a Container accepts a value whose
  restore operation never ran.
- `16786b5fe3`, `5d3ee071c7`, `cfa148371c` core/backing_snapshot.go and
  its five callers (Container.WithMountedCache, both exec mount paths,
  git_remote.go, schema/host.go): an imported CacheVolume, RemoteGitMirror
  or ClientFilesyncMirror row that creates its snapshot at first use
  attaches the row's owner lease (`EnsureBackingSnapshot`); a failed attach
  fails the call and drops the snapshot; creation, sync and discard are
  serialized per value. This removes one trigger of the boot-time
  whole-cache wipe; the policy that a dangling persisted snapshot link
  wipes the cache at boot is unchanged (dagql/cache_persistence_import.go,
  their `boot-wipe/FINDING.md`).
- `87c099a615` dagql/cache_value_capture.go: a comment on
  `WithExportedValues` (a busy row is a transient `ErrPersistStateNotReady`).
- Gated test fixture (`7dcb4e9f9c`, `368aa733f1`, `42921315ce`,
  `d05749ae19`, `7133aa547e`, `637b4b4d56`, `fbf62dd48c`, `96610fe471` and
  later): dagql/cache_fixture_barrier.go, cache_fixture_control.go,
  engine/fixturetransport/, engine/server/remote_cache_fixture_controller.go,
  core/schema/remote_cache_fixture_control.go, reach points in
  dagql/cache.go (`syncResultSnapshotLeases`, `Close`) and
  dagql/cache_persistence_import.go (`ensurePersistedHitValueLoaded`).
  Inert unless `_DAGGER_TEST_REMOTE_CACHE_FIXTURE_ROOT` is set; the same
  gate our transfer fixture uses.
- engine/server/server.go: the fixture's transport enable and its
  integration selection before `startRemoteCacheIntegration`; a real
  `RemoteCacheIntegrationConfig` takes precedence.
- engine/snapshots/lease.go: `IsTransferLease` helper only.
- engine/snapshots/testutil: `NewStore` runs without privileges
  (`e4b65210ea`, `397168d119`): real content store, metadata, native
  snapshots, leases, garbage collection and tar handling, with an in-place
  applier and differ and direct directory reads in place of the mount
  paths (engine/snapshots/testutil/inplace.go); it no longer skips on this
  host.
- dagql/tla, .dagger/modules/tla-check: models only.

## What they did not change (verified against the diffs)

- Export/import: `dagql/cache_transfer_types.go` (bundle format, version),
  `Cache.WithExportedValues`, `ImportValues`, `ValueSelection`,
  `ValueBundle`: no code change. Our `OutputsOf` selection and
  `WithResultsByNumber` are ours alone.
- Integration adapter: engine/server/remote_cache.go, session.go,
  snapshot_sharing.go: no diff. Our adapter, session reports and startup
  gate apply cleanly.
- Pruning and GC: dagql/cache_prune.go, engine/server/gc.go: no diff.
  Their native fixture pins the same never-reclaim bounds our harness does
  (core/integration/remote_cache_harness_test.go), so the H3 finding stands.
- Module loading: core/schema/modulesource.go, core/module.go,
  core/persisted_visitors.go: no diff.
- Snapshot export: engine/snapshots/remote.go, blobs.go: no diff. The
  builtin-layer blob instability (E12's subject) is not addressed by them;
  `EnsureBackingSnapshot` is a different lifetime defect (receiver-created
  backing snapshots of imported rows), not the builtin image's layer blobs.

## Conflicts and resolutions

One textual conflict in 27 cherry-picks.

- `da96ca86e9` (E6, "log each part installed from a remote cache
  download") against their `42921315ce` in dagql/cache_part_content.go:
  both add an import at the same line (`engine/slog` ours,
  `engine/fixturetransport` theirs). Resolved by keeping both imports;
  no other change. The log line's condition is unchanged: it is emitted
  after `PartInstalled` when `finishReadyPartInline` returned nil, and
  their `partRefused` sites around it do not alter that.

No semantic conflict. Our three overlapping files:

- dagql/cache.go: their hunks are in `syncResultSnapshotLeases` (fixture
  reach points), the `Cache` struct (fixture fields, `sharePassSeq`,
  `testBeforePartCommit`), `evaluateOne` and `EvaluateParts` (reselect
  watches) and `CloseWithShutdownError`. Our E9 change in
  `initCompletedResult` (walk the request frame when the row has none) is
  untouched and still needed: nothing of theirs records dependencies for
  null rows.
- dagql/cache_part_content.go: above.
- dagql/cache_value_capture.go: their change is the comment; our
  `OutputsOf` and `selectedOutputs` apply as before.

## Behavior differences accommodated

None required a code change on our side. Differences to be aware of:

- `ErrPartNoProgress`: within one demand, a counter-changing refusal that
  repeats at the same site, row and current counters ends the demand with
  this error instead of retrying forever; ordinary busy or uncounted
  refusals still retry, and the ready, downloadable, lazy ranking is
  unchanged. Our client maps export failures by
  `ErrRemoteCacheResultNotFound` and `ErrPersistStateNotReady`; a
  no-progress error surfaces as a `failed` export or an errored demand,
  which is the right answer.
- `EnsureBackingSnapshot`: imported CacheVolume and mirror rows now own
  their receiver-created snapshot. This helps the imported SDK runtime's
  cache volumes (the Go build cache mounts) survive the creating session.
- `testutil.NewStore` no longer skips: our core package's snapshot
  transfer tests now run on this host against the real stores, with the
  in-place substitutions above for the mount paths (they passed).

## Verification on the first rebased tree, `b654716d10`

Everything in this section was run on `b654716d10`, the cherry-pick onto
`92b8057912`; the re-rebase onto the integrated tip has its own section.

Unit packages, one run each at `-timeout 60s`, on a loaded host:

| package | result |
|---|---|
| engine/remotecache | ok 0.14 s |
| engine/remotecache/protocol | ok 0.02 s |
| engine/server | ok 4.8 s |
| core | ok 34.4 s (the store-backed tests ran instead of skipping) |
| core/schema | ok 12.5 s |
| dagql | see below |

dagql, full package at `-timeout 60s`, four runs in total, as they happened:

| tree | run | result |
|---|---|---|
| rebased `b654716d10` | 1, concurrent with the core and core/schema runs | FAIL: `TestFixtureBarrierOwnerAttachFaults/failOwnerAttachBefore` expected 0 attaches, got 1 (15.1 s) |
| pristine `92b8057912` (detached worktree) | 1 | ok 13.2 s |
| rebased `b654716d10` | 2, no concurrent package runs, load average 16 | FAIL: `TestPartReadyPreparationBoundaries/missing-local-descriptor` failed an assertion at cache_part_boundary_test.go:134 and the subtest hung to the 60 s bound |
| pristine `92b8057912` | 2 | ok 14.8 s |

Focused runs of the two tests, one invocation per tree with `-count=5`:
ok on both trees (1.3 s each). Each test alone also passed on both trees.

Facts about the two tests: `TestFixtureBarrierOwnerAttachFaults` is
theirs (`7dcb4e9f9c`); `TestPartReadyPreparationBoundaries` is in the
common base (`2777bb534d`, 16 September) and neither side changed it.
Neither test is in a file our commits touch. The code they exercise
(`syncResultSnapshotLeases`, `RunLazyTask`, `FinishReadyPart`, snapshot
sharing) is byte-identical between the two trees; our dagql/cache.go
changes are a test hook for `SessionResults` and E9's fallback for
frameless rows, which these tests never reach. The Rebase reviewer's
reading of the fixture test: the assertion runs after the sharing pass,
and `FinishReadyPart` can lead a fresh bookkeeping attempt after the
one-shot fault retires, giving the extra attach without an external
demand; their author B is checking that schedule. Causality is open: the
asymmetry (two failures on the rebased tree, none on the pristine one,
across four runs under differing load) is not explained by anything in
our commits.

Both failures were then confirmed as inherited test defects, not rebase
regressions. Their author B, on the fixture test: `FinishReadyPart` opens
the token's owner sync before it joins the lazy attempt, so a fast failing
original attempt can retire first and Finish then legitimately leads the
retained bookkeeping continuation itself; the test assumed only the joined
schedule. The Rebase reviewer, on the boundary test: it reads
`incomingOwnershipCount` right after `RunLazyTask` returns while the
detached attempt's deferred `releasePartRow` may not have run, and it holds
`egraphMu.RLock` across `require`, so a failed assertion leaves the lock
held and cleanup hangs to the bound. The successor workstream took
ownership of both fixes on its own tip (their joined-schedule fix is
`d168c733ac`; a deterministic retried-schedule case and the boundary test
follow); my analysis went to their coordinator as input. The re-rebase
onto their integrated tip is recorded below.

## Known test-instrument limitation, deferred

core/schema/remote_cache_fixture.go assigns the storage group's error to
the whole fixture report, and `Server.RemoteCacheFixtureStorage`
(engine/server/remote_cache_fixture_controller.go) requires the fixture
controller, which `remoteCacheFixtureIntegration` never creates when a
real `RemoteCacheIntegrationConfig` is supplied. So with the fixture root
set beside a real service, the fixture report operation fails, although
the storage counts are server facts that need no controller. It does not
affect URL-only service runs or our fixture-only integration tests. The
successor workstream is fixing it on its side; on ours it is recorded, not
fixed.

Engine suites through `engine-dev test` on `remote-cache-engine`, once each:

| suite | result |
|---|---|
| TestModuleDefinitionSuite, `--timeout=6m` | 2 passed, 01:06:24 to 01:10:28 |
| TestRemoteCacheStartupSuite, `--timeout=3m` | 2 passed, 01:11:33 to 01:16:41 |

Loop and demo from the dagger.io worktree (`rebase-engine-daggerio-6dd07164`
at the service tip `ee60e43df`, the rebased checkout bind-mounted at its
`dagger-src/`, engine `remote-cache-engine`, Service seat idle by
agreement):

| run | result |
|---|---|
| loop, full build, default set, `--timeout=2m` | engine tarball 200 s, CLI 75 s; the test step was killed by the shell bound (2 m plus 60 s) with the runner still at 30 steps running and no test output flushed (log /tmp/remote-cache-e2e-20260918-012200.log) |
| demo, `--skip-build` | passed: B hit the function call, no body run on B, same binary; A 32.291 s (module load 19.925 s, build 12.366 s), B 458 ms (module load 283 ms, read 175 ms); A uploaded 385,822,791 bytes in 18 blobs, B downloaded 6,340,096 bytes in 1 part (log /tmp/remote-cache-e2e-20260918-012500.log) |
| loop, `--skip-build --timeout=6m`, default set | passed, exit 0, go test 79.527s: TestBlobStore 9.15s, TestCacheServiceStarts 10.91s, TestColdEngineReusesResult 76.04s, TestColdEngineStartsAfterExport 76.04s, TestColdEngineDirectoryFunction 79.51s (log /tmp/remote-cache-e2e-20260918-012720.log). The Service seat measured the default set at 74 to 172 s across fifteen runs and has committed 5 m as the loop default; 2 m was stale. |

## Re-rebase onto the integrated tip `bf509625e7`

Their integrated tip is `bf509625e7` on `b7-integration-author-b`, on top
of `92b8057912`: `fe963009bf` and `1856d1ae92` (the boundary test counts
ownership after the attempt's release, through a nil-checked hook, and
returns assertion failures from the worker), `d168c733ac`, `e77b89c743`
and `1b45911933` (the fixture test pinned to both schedules, bounded and
parallel), `5272f3d454` (the fixture report without the storage group on
an engine with no controller), plus evidence. The Rebase reviewer cleared
each by source review. Their packaged branch is
`b7-packaging/remote-cache/b7-verification` at `6b35df2863`.

Our input widened to the engine seat's tip `0c334e5f06`: `eb71d1b54c`
plus E12 `75b863fee9` (a layer's blob bound to its snapshot by a
garbage-collection label), the E12 follow-up `849465c32d` (blobs held
through leases, builtin image blobs for the engine's lifetime), E11
`e6ce803102` (export compression, zstd or uncompressed) and `0c334e5f06`
(a gofmt reorder). Their later E12 round 2, E11 test fix, E13 and the
zstd default are not in this rebase and follow as cherry-picks.

Result: branch `rebase-engine-on-bf509625e7`, 31 commits cherry-picked
with `-x`, tip `1e417aef41` before this note. The same single textual
conflict (the E6 import line), resolved the same way. The file overlap
with their delta grew to five: dagql/cache.go, dagql/cache_part_content.go,
dagql/cache_value_capture.go, engine/snapshots/lease.go (their
`IsTransferLease` beside our `PinContent`) and
engine/snapshots/testutil/store.go (their in-place applier and differ
beside our `observedSnapshotter` wrapper); the last two merged without a
textual conflict and both sides' code is preserved.

### Verification on `1e417aef41`

`go build ./...` and vet clean. Unit packages, one run each at
`-timeout 60s`:

| package | result |
|---|---|
| engine/remotecache | ok 0.13 s |
| engine/remotecache/protocol | ok 0.03 s |
| engine/server | ok 3.6 s |
| dagql (full package) | ok 11.4 s; both inherited tests now pass |
| core | ok 21.3 s |
| core/schema | ok 13.3 s |
| engine/snapshots | FAIL: E12's `TestImportedLayerBlobIsBoundToItsSnapshot`, "without diffing the snapshot", expected 0 diffs, got 1 |

The engine/snapshots failure is E12's, not the rebase's. On our base the
store tests skip on this host; E12's test was reported as passed in the
engine-dev container, which was later shown to have been a skip (see the
follow-up section below). On their tip `testutil.NewStore` runs
unprivileged; the walking differ fails to mount and export falls back to
the store's in-place differ, whose `Diffs` counter the test reads. The
export after the reload produced the same digest as the imported blob
but by diffing: the reopened ref carried no recorded blob although the
blob was present and leased. `GetBySnapshotID` rehydrates a reopened
ref's metadata with the snapshot ID, committed flag and a description
only (engine/snapshots/manager.go `rehydrateSnapshotMetadataLocked`), and
the manager's metadata store is in memory and recreated on reload. The
engine seat owns this and is changing the import path and the reuse
assertion in an E12 round 3 on top of `95099e8a52`. Two E11/E12 review
findings were open at this point (the forced-variant reuse path labeling
the recorded blob digest; the reuse test counting actual writes rather
than differ calls); their fixes are among the follow-up picks below.

Engine suites on `1e417aef41`, through `engine-dev test` on
`remote-cache-engine`, once each: TestModuleDefinitionSuite 2 passed
(01:43:17 to 01:49:22), TestRemoteCacheStartupSuite 2 passed (01:49:22 to
01:54:26).

A loop and a demo were chained after the suites on `1e417aef41` and are
not evidence: while the loop's CLI build was syncing the bind-mounted
checkout I cherry-picked this note's commits into that checkout, the
sync failed ("failed to select host directory ... not found", loop exit
1), and the demo that followed with `--skip-build` ran the new engine
tarball with the previous run's CLI binary. Recorded here because they
happened; the loop and demo that count are on the final tip below.

## Follow-up: the engine seat's later commits and the final tip

The engine seat's tip moved on while this rebase was verified. On top of
`0c334e5f06`, cherry-picked with `-x` in order: `0c8ed12811` (E12 round
2: the reuse path labels the recorded blob, not a forced variant),
`9dfa451ecb` (E11 test fix: zstd reuse proven by content writes),
`95099e8a52` (E13: a builtin image's layers taken from the builtin store
on chain import), `108ab539fe` (E13 round 1: builtin store opened before
the snapshot manager, unconditional size check), `b88096f0b8` (E12 round
3: an export reuses a reopened snapshot's blob from its label, restoring
the ref's blob metadata from the content store; the finding above),
`c7cd722841` (E11 round 2: zstd is the export default), `4c85e161f4` and
`0f05361f1c` (definition lookup log lines), `64eae4e230` (the size test's
larger case expects the provider's short read). One textual conflict:
E13's change to engine/snapshots/testutil/store.go against the
successor's unprivileged store; resolved by keeping their in-place
applier and differ and adding E13's `BuiltinContent: s.Builtin`, so the
overlap with their delta is now six files (engine/server/server.go
joined it, without conflict).

One adaptation of ours, `c96012aad7`, test helper only: the successor's
store handed the raw content store to its in-place applier and differ,
so a fallback diff's blob writes never reached `BeforeWrite`, and the
three E11/E12 assertions that count content writes saw zero
(TestLabelUpdateFailureIsRepairedByTheRetry,
TestFlatLeaseNamingOnlyTheSnapshotLosesTheBlob,
TestZstdExportChainImports, verbose log /tmp/rebase-engine-snapshots-v1.log
on `b347418163`). Both now use the same observed wrapper the manager
uses.

A fact that surfaced here: the E11, E12 and E13 store tests never ran on
our base. `testutil.NewStore` skipped without bind-mount privileges both
on this host and in the engine-dev container, and the "N passed"
summaries counted skips. The successor's unprivileged store is where they
run for the first time, on this branch.

### Verification on the final tip `c96012aad7`

`go test -v -count=1 -timeout 60s ./engine/snapshots/ ./engine/server/
./engine/remotecache/`, clean tree, unfiltered log
/tmp/rebase-engine-snapshots-v2.log, exit 0. The thirteen store tests
all `--- PASS`, none skipped: TestImportedLayerBlobIsBoundToItsSnapshot
0.41 s, TestFlatLeaseNamingOnlyTheSnapshotLosesTheBlob 0.44 s,
TestDiffedBlobIsBoundToItsSnapshot 0.27 s, TestBuiltinImageBlobsArePinned
0.17 s, TestLabelUpdateFailureIsRepairedByTheRetry 0.32 s,
TestForcedVariantKeepsTheRecordedBlob 0.49 s, TestZstdExportChainImports
0.71 s, TestChainImportTakesBuiltinLayersFromTheBuiltinStore 0.64 s,
TestChainImportReadsOnlyNonBuiltinLayersFromTheProvider 0.59 s,
TestChainImportFallsThroughWhenTheBuiltinStoreFails 0.63 s,
TestChainImportRefusesBuiltinLayersOfAnotherSize/zero 0.63 s and /larger
0.47 s; TestLocalCacheStateTakesTheBuiltinStore/refused_without_the_store
and /wired PASS. Packages: engine/snapshots ok 6.28 s, engine/server ok
2.62 s, engine/remotecache ok 0.12 s.

Unit packages at `-timeout 60s` on `3d7a492ae2`: dagql ok 20.7 s, core
ok 16.2 s. The production files of both packages are unchanged between
`3d7a492ae2` and `c96012aad7`; the later commits touch engine/snapshots,
engine/server, engine/remotecache, one log line in
core/schema/modulesource.go and the shared test helper
engine/snapshots/testutil/store.go, which core's store-backed tests also
use, so those two results are runs on `3d7a492ae2`, not on the final
tip.
core/schema rerun on `c96012aad7`: ok 12.2 s.

The engine suites were not rerun on the final tip: the coordinator
scoped them out because the later commits touch neither path and both
passed on `1e417aef41`.

Shared loop and demo, from the dagger.io worktree at the service seat's
`2880b4268` (its import materialization under review), engine
`c96012aad7` bind-mounted and built in full, host otherwise idle; the
Service seat and the Service reviewer report from the same logs:

| run | result |
|---|---|
| loop, full build, default set, 5 m bound | passed, exit 0, go test 79.1 s: the five tests that ran all PASS, TestBlobStore 10.58 s, TestCacheServiceStarts 11.81 s, TestColdEngineReusesResult 75.35 s, TestColdEngineStartsAfterExport 75.45 s, TestColdEngineDirectoryFunction 79.12 s; TestBaseline is not in this log (log /tmp/remote-cache-e2e-20260918-020854.log). The Rebase reviewer read the three B engines' own logs: `_moduleDefinition` cached=true on all three with zero "module definition computed" lines; reuse B downloaded 129 bytes, late B 128 bytes and released its startup after one answered import in 287 ms, directory B downloaded 6,258,815 bytes across four parts. |
| demo, `--skip-build` | passed, exit 0: B hit the function call, no body run on B, same binary; A 30.709 s (module load 17.064 s, build 13.645 s), B 407 ms (module load 283 ms, read 125 ms); both engines report exportCompression=zstd (E11's default); A uploaded 155,058,581 bytes in 17 blobs, upload 0.998 s, against 385,822,791 bytes uncompressed in the earlier demo on `b654716d10`; B downloaded 3,499,087 bytes in 1 part (log /tmp/remote-cache-e2e-20260918-021135.log) |

Host handed back to the Service seat after the demo.

## Recommendation

Switch base to the successor's integrated tip `bf509625e7`; the branch
to take is `rebase-engine-on-bf509625e7` at the tip named in the final
report, which is the engine seat's `0f05361f1c` on top of it plus the one
test-helper commit and this note.

Why. Their delta and ours do not compete: the file overlap is six files,
the two textual conflicts are an import line and a test-store option
block, and their production changes fix acquisition and backing-snapshot
defects that our service runs would otherwise hit: a not-ready
part-source candidate, a key-only renewal panic, cloning a part-acquired
File into a Container (`1bfece3b77`'s `HasPendingLazyComputation` change
in core/container.go, which is the run 3b "still lazy" guard our
original branch lacks), and the imported cache volume's snapshot
lifetime.
Nothing of theirs supersedes E1 through E13, the cached module
definition, or E9's null-row dependencies. On the rebased tree our unit
packages, both engine suites and the demo pass, and their unprivileged
test store runs our E11, E12 and E13 store tests for the first time
anywhere, all passing.

Differences resolved during this rebase:

1. Their two inherited dagql test defects (schedule-dependent fixture
   test, ownership count read before a detached release) were fixed on
   their tip after my analysis; the fixes are in `bf509625e7`.
2. Their fixture report required a controller that a real integration
   never creates; fixed on their tip (`5272f3d454`).
3. Their test store handed its raw content store to the in-place differ;
   adapted on our branch (`c96012aad7`). The successor adopted the same
   helper fix on its tip as `ed7a4a47f9` (test helper only); at the next
   rebase `ed7a4a47f9` replaces `c96012aad7`. No re-rebase now: run 4
   and the demo stay on `c96012aad7` over `bf509625e7`.

Differences still to resolve with them, none blocking the switch:

1. `ErrPartNoProgress` is a new terminal outcome of a demand; our client
   reports it as a failed export, which is correct, and the service side
   should expect that outcome where it previously saw a retry.
2. The boot-time whole-cache wipe on a dangling persisted snapshot link
   stays their open policy item; E12's builtin-layer blob lifetime and
   E13's builtin-store import are ours; neither side's fix covers the
   other's case.
3. Their store substitutes in-place applier and differ and direct
   directory reads for the mount paths, so a test passing there proves
   the store behavior under that backend, not a mounted engine; the
   native cases in core/integration and the loop and demo cover the
   mounted paths.
The two E11/E12 review findings that were open during this rebase (the
forced-variant reuse path labeling the recorded blob digest; the reuse
test counting actual writes rather than differ calls) are closed: the
fixes are `5193483904` and `c3139228cf` on this branch (picks of
`0c8ed12811` and `9dfa451ecb`), the write instrumentation works after
`c96012aad7`, and the Engine reviewer accepted the E11 store
verification and closed E12 round 3 and E13 on the `c96012aad7` run.
