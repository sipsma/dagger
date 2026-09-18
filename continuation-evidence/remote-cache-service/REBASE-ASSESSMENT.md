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

## Re-rebase onto the integrated tip

PENDING: the successor workstream is fixing the two inherited tests and
the fixture-report limitation on its own tip and will name an integrated
tip; our commits are then cherry-picked onto it again and the unit
packages and one full dagql run repeated.

## Recommendation (provisional, before the re-rebase)

Switch base. Their delta and ours do not compete: the only file overlap
is three dagql files, the one conflict is an import line, and their
production changes fix acquisition and backing-snapshot defects that our
service runs would otherwise hit (a not-ready part-source candidate, a
key-only renewal panic, cloning a part-acquired File into a Container, the
imported cache volume's snapshot lifetime). Nothing of theirs supersedes
E1 through E10, the cached module definition, E9's null-row dependencies
or the pending E11 and E12. On `b654716d10` our five other unit packages,
both engine suites, the default loop set and the demo pass; the dagql
package's two inherited test defects are open until the re-rebase onto
the tip that carries their deterministic fixes passes a full run.

Differences to resolve with them, none blocking the switch:

1. The two inherited dagql tests are schedule-dependent (fixture test) and
   read a count before a detached release (boundary test); their fixes
   must not poll the clock, which their first boundary fix did.
2. The fixture report requires a controller that a real integration
   never creates; deferred on our side, fixed on theirs.
3. `ErrPartNoProgress` is a new terminal outcome of a demand; our client
   reports it as a failed export, which is correct, but the service side
   should expect that outcome where it previously saw a retry.
4. The boot-time whole-cache wipe on a dangling persisted snapshot link
   stays their open policy item; E12's builtin-layer blob lifetime stays
   ours; neither side's fix covers the other's case.
5. Their `testutil.NewStore` now runs unprivileged with in-place
   substitutions for mount paths; tests that relied on the skip now run
   and take the core package from 3 s to 34 s at the 60 s bound.
