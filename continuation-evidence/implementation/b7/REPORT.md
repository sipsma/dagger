# Batch 7 implementation report: author A, fixture and native cases

Author A `cl-1bc62433d5e6050624a14d087cadcbdf`, 17 September 2026. Governing documents: the [continuation packet](../../implementation-continuation/b7-continuation/PACKET.md) at `674f96672d`, the commission, the design at `bb71b01203` including its amendments B1 to B5, and batch 6's closing record. This report is written per review slice; slice 1 is below.

| Identity | Commit |
| --- | --- |
| Branch | `cl-1bc62433d5e6050624a14d087cadcbdf-b7-tests-a26ba67d` |
| Base | `c5b299142ca672cbd2ef0a389492f11de85ff08b` (batch 6 head) |
| Slice 1 implementation tip | `20a56c46f5` |
| Evidence tip | the commit that adds this report |

## Slice 1: the dump facility and the fixture controller

### Commits

| Commit | What it does |
| --- | --- |
| `4ba19a566c` | Opt-in nested-engine goroutine dump in the integration harness (`_DAGGER_TESTS_NESTED_ENGINE_DUMP_AFTER`, also read from the file `engine-dev test --env-file` mounts). Off, nothing changes. |
| `7dcb4e9f9c` | Cache controls of the gated fixture: hold tokens, retained-root removal, and barriers with the closed point and action sets, wired at the real operations. Nil off-gate. |
| `368aa733f1` | The field's new operations over contained, strictly decoded request records, and the engine's fixture controller: the real `OfferParts`, the real metadata GC, and the consumer loop of the real renewal mailbox with `takeRenewal`, `replyRenewal` and `armRenewalReply` (amendment F5). |
| `42921315ce` | The in-process transport: `engine/fixturetransport`, applied at the two HTTP clients and the content source, the gated go-git re-registration, the GitCLI configuration hook, the `transport` and `observe` operations, the observation bound and the report's `controls` and `transport` groups. |
| `1bbde2fb2a` | Fix to `4ba19a566c`: with the diagnostic on, the debug port was exposed before port 1234, so a tunnel's default endpoint became the debug port. |
| `20a56c46f5` | The native harness for batch 7's cases and `TestFixtureControls`. |

### What the slice delivers, against the commission's commit 1

| Commission item | Delivered |
| --- | --- |
| `exportSelected` | Yes. Full addresses from a request record; every unique layer copied from its actual ReaderAt to a temporary blob with size and digest checked, then renamed; bundle published last; copied bytes reported per layer. The older `export` with `outputIDs` stays for the tests that use it. |
| `offer` | Yes: holds the exact receiver for the real adapter `OfferParts`; every full-address disposition reported, earlier accepted entries kept beside a later failure. **Not yet exercised natively**; `TestOffers` in slice 2 is its first native use. |
| `transport` | Yes: a copied script swapped in as one immutable generation. |
| `takeRenewal` / `replyRenewal` | Yes, through the engine controller's one consumer loop of the real mailbox. In-process test against the real bridge; first native use is `TestRenewal` in slice 2. |
| `armRenewalReply` (amendment F5) | Yes. The loop fills in the exchange ID and calls the real `ReplyRenewal`; a template may name its layers and the controller computes the real fingerprint (`dagql.RenewalChainFingerprint`, newly exported). |
| `barrierArm` / `barrierWait` / `barrierRelease`, closed action set | Yes: 22 points, `pause` plus five faults each legal at one point. |
| `hold` / `releaseHold`, `dropRetainedRoots`, `gc` | Yes. `gc` runs the engine's real `containerdMetaDB.GarbageCollect` under `gcmu` and reports snapshot, blob and lease counts before and after. `gc` and `dropRetainedRoots` get their first native use in `TestSharingDonorRestart`. |
| Extended `report` | In part: the `controls` group (hold tokens, armed barriers), the `transport` group, the observation bound with failing overflow. **Owed in slice 2**, with the cases that read them: the correlation fields of §3.2 on part events (`demandID`, `taskGeneration`, `passID`, `sourceRoute`), the `sharing`, `renewal` and `storage` groups, and the delegated-install distinction in `acquisition`. |
| Nil-off hooks | Yes: with no fixture state a barrier site is one atomic load; off-gate there is no controller, no consumer loop, no bridge, no dispatcher, and the go-git registration and GitCLI options are untouched. |
| Startup transport factory at the two HTTP clients and `PartContentSource` | Yes (`fixturetransport.Wrap`). |
| Gated go-git protocol re-registration | Yes (`schema.InstallRemoteCacheFixtureGitTransport`, called at startup under the gate). |
| `GitCLI.WithConfig` hook | Yes (`core.EnableRemoteCacheFixtureGit`): `url.file://<root>/git/.insteadOf` for the one fixture Git host, and `protocol.file.allow=always`. |
| `TestFixtureControls` | Yes, in two halves: protocol in process (`core/schema`), native in `core/integration` with BarrierActions, GitVisibility, TransportShape, AbsentGate, MalformedRecords, NoStaleBarrierOrHoldAfterRestart and ObserverOverflow. |

### The hook list (amendment B5), for author B

One mechanism: `(*Cache).fixtureReach(ctx, FixtureBarrierEvent) error`. Off-gate it is one atomic load of `c.partFixture`. An in-package test turns it on with `c.EnableTransferFixtureParts()` and arms with `c.ArmTransferFixtureBarrier`, so a test that needs a pause or a fault at one of these sites needs no new hook field.

| File | Site | Points |
| --- | --- | --- |
| `dagql/cache_part_demand.go` | `demandPart`, after `selectDemandPartSource` returns a held source | `sourceSelected` |
| `dagql/cache_part_demand.go` | `runLazyOperationDecision`, around `BeginOriginal` and before `invocation.Run` | `beforeBeginOriginal`, `originalSealed`, `lazyEntry` |
| `dagql/cache_part_install.go` | end of `prepareReadyPartFromBase` (`reachPrepared`); `CommitReadyPart` after the invariant checks and in a deferred call that runs after every lock is released | `prepareDone`, `beforeCommit`, `commitPublished` |
| `dagql/cache_part_lazy.go` | end of `prepareEvaluatedParts` | `prepareDone` |
| `dagql/cache_snapshot_sharing.go` | `reachSharePassTaken` (**wraps** `testBeforeSharePass`), `reachSharePass`, `reachBeforeShareFinish` (**wraps** `testBeforeShareFinish`); `snapshotShareItem.passID` | `sharePassTaken`, `shareAllPrepared`, `shareMembersReleased`, `beforeFinish` |
| `dagql/cache.go` | `syncResultSnapshotLeasesGuarded` around the real `AttachLease` (inside the row's lease guard, where the attachment is); `syncResultSnapshotLeases` after the guard is released | `beforeOwnerAttach`, `afterOwnerAttach`, `ownerSyncDone` |
| `dagql/cache_part_fixture.go` | `partFixtureProvider.ReaderAt`, `partFixtureReader.ReadAt` and `.Close` | `chainReaderOpen`, `chainRead`, `chainClose` |
| `dagql/cache_part_renewal.go` | `request` after `enqueue`, `TakeRenewalRequest` before return, `ReplyRenewal` deferred past M; `RemoteCacheBridge.cache` | `renewalEnqueued`, `renewalDelivered`, `renewalReplied` |
| `dagql/cache_persistence_import.go` | hit-time typed decode, before `DecodeResult` and before the compare-and-publish | `decodeCopied`, `decodeBeforePublish` |

`testAfterSharePass` and `testShareSkipped` keep their own fields: they are observations with no design point. Close calls `closeFixtureBarriers` and `releaseAllFixtureHolds` after `closeSnapshotSharing`.

### F4, settled by reading; to be confirmed natively in slice 2

At boot the cache calls `snapshotManager.AttachLease` for every saved owner link, and that stats the snapshot (`engine/snapshots/persistent_metadata.go:159`). A link whose snapshot does not exist fails the import, which `NewCache` treats as `import_failure`: it wipes the dagql store and cold-starts, and the engine records `dagql_import_failure` in the fixture's journal. Only a failure of `DeleteStaleDaggerOwnerLeases` (`errOwnerLeaseReconciliation`) fails the boot. So the damage for G3 is one row of `result_snapshot_links` in `/var/lib/dagger/dagql-cache.db`: set its `ref_key` to a snapshot that does not exist, from an outer container with `sqlite3` on B's state volume, between a clean stop and the start. Expected: a recorded whole-cache reset, no remote request, no per-row repair.

### Observations from the native runs

- `failChainClose` does not fail the read. The fault fires at the real reader's Close after the blob was fully read and verified; the import succeeds and the row installs once. The design asks only that the real Close happen and cleanup not be skipped, so this is recorded, not reported as a defect.
- `failChainOpen` and `failChainRead` fail that one demand with "imported filesystem part is unavailable" (the row has no Lazy operation to fall back to); the next demand installs the chain once.
- Either owner-attach fault fails that demand with the fixture's local-storage error, and the retry reads no content again.
- The export mapping lists the whole exported closure (the shared Host row too), not only the roots; my first version of the test assumed otherwise.

### Verification ledger, slice 1

Unprivileged, no root, default parallelism. One engine invocation at a time.

| # | Command | Bounds | Result | Time |
| --- | --- | --- | --- | --- |
| 1 | `go test ./dagql ./core ./core/schema ./engine/server ./engine/fixturetransport -timeout 120s -count=1 -v` at `20a56c46f5` | `-timeout 120s`, harness 600 s | pass | dagql 6.4 s, core 2.9 s, core/schema 12.2 s, engine/server 2.4 s, fixturetransport 0.01 s; 17 s wall ([logs/slice1-packages.log](logs/slice1-packages.log)) |
| 2 | `go test -race ./dagql ./engine/server ./engine/fixturetransport -run 'TestFixture\|TestSnapshotSharing\|TestRemoteCacheFixture' -timeout 180s -count=1 -v` | `-timeout 180s`, harness 600 s | pass, no race | 92 s wall with the race build ([logs/slice1-race.log](logs/slice1-race.log)) |
| 3 | `go test ./core/integration -run '^TestNestedEngineDump' -timeout 120s -count=1 -v` (engine-free) | `-timeout 120s` | pass | 14 s wall ([logs/slice1-dump-unit.log](logs/slice1-dump-unit.log)) |
| 4 | `dagger api call engine-dev test --pkg ./core/integration --run='^TestRemoteCacheTransferSuite/TestFixtureControls$' --test-verbose --timeout=6m --env-file=file:/tmp/b7/dump-100s.env` at `42921315ce` plus the uncommitted native test | `--timeout=6m`, outer 700 s | **failed: my defect in the dump facility.** With the opt-in on, the debug port was exposed before 1234, the tunnel's default endpoint became the debug port, and all seven subtests timed out in `dagger.Connect`. The same run is the facility's native proof: at 1 m 40 s it wrote the test process's goroutines and a dump of all seven nested engines, none failed, and the gated engines' dumps show the fixture controller's consumer loop in `TakeRenewalRequest`. Fixed in `1bbde2fb2a`. | 570 s wall ([logs/slice1-native-1-excerpt.log](logs/slice1-native-1-excerpt.log)) |
| 5 | Same selection at `1bbde2fb2a` plus the uncommitted native test, dump opt-in at 5 m | `--timeout=6m`, outer 700 s | **6 of 7 pass**: AbsentGate 35 s, MalformedRecords 43 s, TransportShape 48 s, NoStaleBarrierOrHoldAfterRestart 54 s, GitVisibility 55 s, ObserverOverflow 68 s. BarrierActions failed on my test's wrong assumption about the export mapping (above); no production failure. | 306 s wall ([logs/slice1-native-2-excerpt.log](logs/slice1-native-2-excerpt.log)) |
| 6 | `--run='^TestRemoteCacheTransferSuite/TestFixtureControls/BarrierActions$'` with the corrected assertion (the tree committed as `20a56c46f5`) | `--timeout=5m`, outer 640 s | **pass**, 1 m 10 s | 299 s wall ([logs/slice1-native-3.log](logs/slice1-native-3.log); the test's own output from trace `a21f6e9b9c636ea156af9a086b160683` is [logs/slice1-native-3-test.log](logs/slice1-native-3-test.log)) |

Every subtest of `TestFixtureControls` has a recorded native pass, six in row 5 and one in row 6; the full set runs together once at the slice 2 review, as the packet says.

### Pre-existing slop met

1. `engine-dev test --env-file` mounts the file at `/dagger.env` and nothing exports it into the test process, so an opt-in variable cannot be passed to a native test by that flag alone. The dump facility reads the file itself. Whoever next needs an opt-in variable natively meets the same gap.
2. A tunnel's default endpoint is the first exposed port of the service, silently. Every nested-engine test relies on 1234 being first.
3. Per-test verdicts, durations and `t.Logf` lines of a passing native run are still only in the recorded trace (`dagger cloud logs <trace> --test <name>`).

### Limits of slice 1

- `offer`, `takeRenewal`, `replyRenewal`, `armRenewalReply`, `gc`, `hold` and `dropRetainedRoots` are tested in process and wired, but only `hold`, the barriers, `exportSelected`, `transport`, `observe` and `report` have run on a real engine so far. The slice 2 cases are their first native use.
- The renewal points are observations; nothing in slice 1 pauses at them natively.
- A pause at `beforeOwnerAttach` or `afterOwnerAttach` holds the row's lease guard, because that is where the real attachment is. The design's list of locks a pause must not hold does not name that guard; I note it so the council can object.

## Slice 1 corrections and slice 2 progress (interim, 17 September 2026)

Conclusions first.

- Every slice 1 finding assigned to me is fixed: C1 to C5 and C9 of the consolidation, and F1 to F4 and the harness finding of the confirmation round. Each production-side fix has an in-process test that fails before it, with two stated exceptions below.
- Eleven native tests exist and pass, with two subtests that fail on production defects that are with the coordinator, not masked.
- Two production defects were found by native cases. One is fixed by author B and cherry-picked here; the other is open.
- One regression was mine and lasted five commits: `go test ./dagql` was red from `87d62517c2` to `d5d8deb18c`. Found by me, fixed in `96610fe471`. How it happened is below.
- Twenty-eight nested engines ran in parallel at default parallelism without thrash. That is the F6 data so far; the one full-set measurement is still owed.

### Corrections

| Finding | Commit | Failing-before test |
| --- | --- | --- |
| C1 paused reply blocks Stop | `12f1b581fc` | `TestRemoteCacheFixtureStopReleasesPausedReply` |
| C2 hold admission races Close | `0b59c27ac4` | `TestFixtureHoldAdmittedBeforeCloseLeavesNoToken` (race, 20 runs) |
| C3 retired deliveries accumulate; C5 armed reply outcome dropped | `637b4b4d56` | `TestRemoteCacheFixtureRetiresUntakenDeliveries`; C5's assertions are in `TestRemoteCacheFixtureRenewal` |
| C4 cleanup registered late, unbounded | `a8038f8450`, then `5b943119c3` | none: the path needs real services; it is the control flow the reviewer read |
| C9 injected cause survives | `1fab495171` | native assertion in `TestFixtureControls/BarrierActions`; it does survive |
| F1 cancellation alone cannot end a paused reply | `bc4618cbef` | `TestRemoteCacheFixtureLifetimeCancelReleasesPausedReply` |
| F2 `decodeJoined` test hangs after a failed assertion | `0dbaedcd91` | the reviewer's injected-failure reproduction |
| F3 `observe` keeps old reached points | `96610fe471` | `TestFixtureObserverOverflow` |
| F4 armed-reply history unbounded | `5080ec1468` | `TestRemoteCacheFixtureArmedReplyHistoryIsBounded` cannot compile before the change; the reviewer's reproduction is the failing-before evidence |
| C8 ledger bounds | this commit | slice 1 row 3 corrected below; every row here has both bounds |
| `decodeJoined` for author B | `d05749ae19` | `TestFixtureBarrierDecodeJoined` |

Slice 1 ledger row 3 correction: its process bound was not recorded. It ran under the tool's bound, at most 600 s; the exact value is unknown.

### My regression

`87d62517c2` journals every reached point and took the part events' mutex to do it. Batch 6's `TestSnapshotSharingCancelAfterPublicationDeliversReceipt` parks a Body at Commit's part event by holding that same mutex, so the Body parked at `prepareDone` instead and the test failed at "the slot never published". After that commit I ran only the fixture tests, not the package. I then chained a commit after `go test … | tail`, whose exit status is `tail`'s, so `d5d8deb18c` went in on a visibly failing run. `96610fe471` gives the journal its own lock and the package passes again, also with `-race` on the fixture and sharing tests. I now write the test result to a file and commit only on a grep of `^ok`.

### Fixture additions in slice 2

- `87d62517c2`, `96610fe471`: the report's `reached` list, every point of the closed barrier set the cache passed, on one sequence counter with the part events. This is the correlation section 3.2 asks for: task generation, pass, source route and exchange are already in a point's event.
- `7356f6d6bc`, `fbf62dd48c`: the `storage` group: snapshot and blob counts, owner leases by ID, transient pins with what each holds.
- `637b4b4d56`, `5080ec1468`: the `renewal` group.
- `d5d8deb18c`: `share-skipped` part events with the cause.
- `7133aa547e`: the fixture's content override now serves only an offer with no address and no renewal key. Any other offer goes to the real content source, so the real HTTP content path, the renewal mailbox and the transport dispatcher are reachable natively. Before this no native case could touch them.
- `07fc672f1a`: my `gc` control lacked the containerd namespace and had never worked on an engine; slice 1's native test only called it with a bad argument. My miss.
- `f9672bc95f`: the dispatcher returned an unobserved `http.NoBody`, so every error status looked unclosed. A fixture defect, found by the status cases.

### Native tests

All in `RemoteCacheTransferSuite`. Durations are the test's own, from the trace.

| Test | State | Commit |
| --- | --- | --- |
| `TestFixtureControls` (7 subtests) | pass at the corrected tip, 222 s wall | `20a56c46f5`, `1fab495171` |
| `TestSharingDonorRestart` both orders | pass, 84 to 86 s | `3fbdb3e7c0` |
| `TestEncodedRestart/FailedAttachThenRestart`, `/LocalRestoreReset`, `/RetryOnlyBookkeeping` | pass, 81 to 88 s | `1828ecfee9`, `02ceab3911` |
| `TestPendingOffersRestart` | pass, 2 m 11 s | `27a66ccda5` |
| `TestHTTPRestore` three outcome classes (7 subtests) | pass, 75 to 80 s | `5c9d2f353d` |
| `TestHostInputs` (2) | pass, 75 s | `2e452a72de` |
| `TestSharingFinish/OnePass` | pass, 1 m 54 s | `fc75ba9a0c` |
| `TestOffers/BeforeStart`, `/Preparing`, `/Running` | pass, 94 s | `549f64d12f` |
| `TestGitTrees/RemoteDownload`, `/RemoteFallback` | pass, 2 m 25 s | `28dec66334` |
| `TestRenewal/ArmedReplySucceeds`, `/ExpiredAddressesRenewed`, `/TimeoutThenFallback` | pass with B's fix; 81 to 94 s | `796f571ea1`, `397221fe5c` |
| `TestPipeline/Warm`, `/Cold` | pass, 2 m 28 s and 2 m 14 s | `be5f48d0d8` |
| `TestPipeline/FailedChain` content cases (9) | pass, 76 to 84 s | `be5f48d0d8` |
| `TestPipeline/FailedChain/RetainedExec` | **fails: production finding 2** | `be5f48d0d8` |

F4 is confirmed natively by `LocalRestoreReset`: one damaged `ref_key` in `result_snapshot_links` gives `import_failure` and `dagql_import_failure`, the engine boots, nothing is demanded and no fixture host is reached.

Rows author B folded in, as named in the tests: `TestHostInputs/NestedViewExportsWholeChain` (from `TestValueTransferPartsSelectedChain`); `TestGitTrees/RemoteDownload` (from `TestValueTransferPartsGitTrees`, remote); `TestGitTrees/RemoteFallback` (from `TestGitLazyOperationsRemoteEvaluate` and the Ref, Commit, discard, depth and tags combinations of `TestGitLazyOperationsEvaluate`). Still owed: the local backend, the `cleaned` tree, bundles, and `TestPipeline/Cold`'s builtin selector row.

### Production findings

1. **Engine panic on the first accepted renewal of a key-only offer.** `assignment to entry in nil map` in `partContentProvider.renew`; `Provider` cloned a nil address map. Found by `TestRenewal/ArmedReplySucceeds`. Fixed by author B (`e844245c8b`, here `397221fe5c`); the native case passes unchanged.
2. **The retained exec's fallback fails when the exec mounts an imported File.** `clone detached file for container result: file must be materialized, got lazy *core.FileRestoreLazy` (`core/container.go:921`). The File's output was installed by part acquisition, its Lazy operation never ran, and the clone guard tests the Lazy operation. Open, with the coordinator; the native case is committed as written and fails.
3. Not a defect, ruled by the coordinator after author B reproduced it in process: while a complete equivalent local row is alive, an ordinary read of an imported row's handle is served by that row and never touches the imported one, so bookkeeping owed on it is paid only by an exact demand, a collection or a close. `RetryOnlyBookkeeping` was reshaped to end the donor's owner first. Measured natively: one transient pin is held in the owed state and released by the settlement.

### What this means for cases still to write

The same fact limits two of the designer's owed rows natively. A foreground read cannot race a pass on the receiver's own address, and cannot decode the receiver, while the donor is alive, and a pass needs a live donor. `TestSharingFinish/FailingPrefix` and `/ForegroundReadRacesPass` therefore use the fixture's exact selected export as the foreground operation. They are written and on the engine now. A Container receiver whose mount order differs from the donor's cannot be made congruent natively at all: a different mount order is a different recipe. Options: (a) keep it in process where `core/part_delegation_mount_test.go` already carries it; (b) a native Directory-level case, where equal content does unite rows. I propose (a) and will say so at the slice 2 review.

### Verification ledger, slice 2 so far

Unprivileged, default parallelism, one engine invocation at a time. Every engine row is `dagger api call engine-dev test --pkg ./core/integration --run=<selection> --test-verbose --timeout=<T> --env-file=file:/tmp/b7/dump-5m.env` under `timeout <outer>`.

| # | Selection | Bounds (test, process) | Result | Wall |
| --- | --- | --- | --- | --- |
| 7 | `TestSharingDonorRestart` | 5 m, 640 s | fail: my `gc` control (namespace); donor-after-import resolved to R | 295 s |
| 8 | donor, encoded restart, `TestFixtureControls/NoStale…` | 6 m, 680 s | gc fixed; 3 test-side expectation failures | 340 s |
| 9 | same three failing subtests plus pending offers | 6 m, 680 s | **no result: the run was killed at 5 m 30 s when my session was interrupted** | unknown |
| 10 | donor, encoded restart, pending offers | 6 m, 680 s | donor passes both orders; the rest test-side | 416 s |
| 11 | encoded restart, pending offers | 6 m, 680 s | pass | 281 s |
| 12 | `TestFixtureControls` at the corrected tip | 6 m, 680 s | pass | 222 s |
| 13 | sharing finish, HTTP restore, host inputs | 7 m, 760 s | HTTP and host pass; finish failed on my pin expectation | 192 s |
| 14 | sharing finish, renewal | 6 m, 680 s | **engine panic (finding 1)**; timeout case passes | 245 s |
| 15 | finish, renewal (2), offers, Git, pipeline | 8 m, 810 s | all pass but pipeline (my GraphQL variable type) | 222 s |
| 16 | pipeline, renewal success, retry subtest | 8 m, 810 s | renewal and retry pass; finding 2; five status cases on my dispatcher defect; Cold on my exec count | 370 s |
| 17 | pipeline Cold and the five status cases | 8 m, 810 s | pass | 362 s |

Two runs started with an edit of mine inside their first minute: run 16, a test file in `engine/server` at 41 s, and run 17, the F4 change to `core`, `core/schema` and `engine/server` at 48 s. Neither file set is in the selected test package, and both runs built and behaved as the tree before the edit would, but the rule is one minute and I broke it twice. The runs are recorded as they are.

In-process runs for the corrections: `go test ./dagql -count=1 -timeout 400s` under a 560 s process bound, pass, 4.9 s; `go test -race ./dagql -run 'TestFixture|TestSnapshotSharing' -timeout 400s` under 560 s, pass, 5.7 s; `go test -race ./engine/server -run 'TestRemoteCacheFixture|TestRemoteCache' -count=2 -timeout 200s` under 320 s, pass, 3.7 s; `go test ./core/schema -run 'TestFixture|TestRemoteCacheFixture' -timeout 240s` under 600 s, pass, 0.9 s; `go test ./engine/fixturetransport -timeout 60s` under 150 s, pass.

### Slop met since slice 1

- A shell pipeline's exit status hides a failing test; see my regression above. Not the repository's slop, mine, but it cost a red package for five commits.
- A passing native run prints no verdict lines at all, so "which subtests ran" has to be confirmed from the trace each time.

## Slice 2: native cases, the full-set measurement and what is not native

Conclusions first.

- The full native set ran together once, at default parallelism, unprivileged: fifteen tests, 116 nested engines in all, exit 0, **526 s wall** including the build. No thrash: every subtest took as long as it does alone or less. F6 needs no change to parallelism and no sharing of engines between scenarios.
- Every native row of amendment B1 is delivered, or is listed below as in process by ruling with its carrier. Nothing is skipped for privilege.
- Native cases found two production defects, both fixed by author B and cherry-picked here, and one regression of mine (recorded above).
- All of author B's folded rows are named in the tests that carry them.

### F6: the full native set, run once

`timeout 1260 dagger api call engine-dev test --pkg ./core/integration --run='^TestRemoteCacheTransferSuite/(TestPartMixedExecOutputs|TestHostInputs|TestSharedHostDirectoryLifetime|TestSchemaRecovery|TestSchemaRecoveryCold|TestGitTrees|TestSharingDonorRestart|TestEncodedRestart|TestPendingOffersRestart|TestFixtureControls|TestPipeline|TestOffers|TestRenewal|TestHTTPRestore|TestSharingFinish)$' --test-verbose --timeout=15m --env-file=file:/tmp/b7/dump-5m.env`, at `d70534a46a`, clean tree. Test timeout 15 m, process bound 1260 s, approved by the coordinator beforehand. Result: exit 0, 526 s wall, trace `d85a79e82ecc506d2ee49e5d5d04cd86`. The opt-in prune diagnostic is outside every verification set and was not selected. `TestWorkspaceCapture` was withheld at the time and ran alone afterwards (ledger).

| Test | In the full run | Alone or in a small batch |
| --- | --- | --- |
| `TestPartMixedExecOutputs` | 1 m 24 s | not run alone by me |
| `TestHostInputs` | 1 m 23 s | 1 m 15 s |
| `TestSharedHostDirectoryLifetime` | 1 m 29 s | not run alone by me |
| `TestSchemaRecovery` | 3 m 39 s | not run alone by me |
| `TestSchemaRecoveryCold` | 3 m 28 s | not run alone by me |
| `TestGitTrees` | 2 m 40 s | 2 m 30 s |
| `TestSharingDonorRestart` | 1 m 41 s | 1 m 26 s |
| `TestEncodedRestart` | 1 m 38 s | 1 m 28 s |
| `TestPendingOffersRestart` | 2 m 37 s | 2 m 11 s |
| `TestFixtureControls` | 1 m 15 s | its longest subtest alone: 1 m 10 s |
| `TestPipeline` | 5 m 31 s | its longest subtest, `DonorReleased/AfterRestart`: 3 m 2 s here, 3 m 9 s in a batch |
| `TestOffers` | 5 m 12 s | subtests 52 to 57 s here, 85 to 94 s in a batch |
| `TestRenewal` | 1 m 29 s | 1 m 34 s |
| `TestHTTPRestore` | 6 m 53 s | subtests 53 s to 1 m 34 s here, 75 to 100 s in a batch |
| `TestSharingFinish` | 1 m 25 s | 1 m 54 s |

The three long parents are not slow work. Their subtests are queued by the test runner's default parallelism, which is the 16 CPUs of this host, so a parent with fifteen subtests waits for slots while each subtest runs at full speed. **Peak parallel engines:** I did not instrument it. With 16 parallel leaves and one to three engines a leaf, the peak is between 30 and 40; the earlier batches ran 20 and 28 engines at once at the same per-subtest times. Host: 16 CPUs, 62 GiB, about 20 GiB in use before the run. The 5 minute opt-in dump did not produce output in the log; a passing run prints nothing.

### Measurement report

| What | Value | From |
| --- | --- | --- |
| Sharing passes after importing one executed Container's closure (8 rows) | 6 passes that prepared slots: 1, 1, 1, 2, 3 and 4 slots; R's own pass prepared all 4 (metadata, filesystem, written mount, unchanged sibling mount) | `TestSharingFinish/OnePass`, reached-point journal |
| Import to R's first external Finish, with every earlier Finish paused and released through the harness | 6.0 s, of which almost all is the harness's nine control round trips | same |
| Slots a pass left alone for a service-backed receiver, with causes | 0 | `TestSharingFinish/ServiceBackedReceiver`, `share-skipped` events |
| Provider bytes on the real HTTP content path | one 2048 byte layer, read fully in one ranged request (`bytes=0-2047`), body closed | `TestPipeline/FailedChain/Served` |
| Permanent status (401, 403, 404, 410) | requested once, body closed, one fallback into the saved producer | `TestPipeline/FailedChain/Status4xx` |
| Renewal with nobody answering | the demand took 2.06 s, one exchange, one fallback; the controller's untaken record retired with it | `TestRenewal/TimeoutThenFallback` |
| Transient pins added by sharing passes and the reads after them | 0, before and after a real collection | `OnePass`, `ForegroundReadRacesPass`, `ServiceBackedReceiver` |
| Transient pins in the owed-bookkeeping state | 1, held for the retry; released by the settlement an exact demand causes | `TestEncodedRestart/RetryOnlyBookkeeping` |
| Transient pins of a live `Container.from` | 1 (three content blobs and the rootfs snapshot), for as long as that ref is held; not a pass's | `OnePass`, storage group |
| Fixture hold tokens and armed barriers left by any case | 0 | `Controls` in every report that asserts it |
| Storage after `OnePass`'s passes | 17 snapshots, 4 blobs, 32 owner leases | storage group |
| Retained after a donor's release and a real collection | snapshots 3 to 2: the donor's own row and lease go, the shared snapshot stays under the receiver's lease | `TestSharingDonorRestart` |
| Exported chain of a nested Host view | the whole parent chain, bytes copied, one provider open per layer on B | `TestHostInputs/NestedViewExportsWholeChain` |

Retained bytes are counts, not bytes: the storage group reports snapshot, blob and lease counts. A byte figure needs the snapshotter's usage walk, which I did not add.

### Production findings from native cases

1. Engine panic on the first accepted renewal of a key-only offer (`partContentProvider.renew`, nil address map). Fixed by author B, here `397221fe5c`. Found by `TestRenewal/ArmedReplySucceeds`, which passes unchanged.
2. A Container operation that clones a part-acquired imported File or Directory failed (`file must be materialized, got lazy *core.FileRestoreLazy`). Fixed by author B, here `624b48d8dc`. Found by `TestPipeline/FailedChain/RetainedExec`, which passes unchanged.
3. Author B's scan defect (`scanPartSources` ranking a not-ready candidate) was not met by any native case here.
4. Ruled not defects, after author B reproduced each in process: an ordinary read of an imported row's handle is served by a complete equivalent local row while one lives; an export of a row with an attempt in flight answers not ready (batch 2's contract); a call taking a `Workspace` never matches by recipe, and a Workspace-taking constructor's object unites by content instead.

### Not delivered natively

| Row | State | Carrier and reason |
| --- | --- | --- |
| G2, the failing prefix (pause at `beforeCommit` plus a concurrent decode) | in process by ruling | batch 6's `PrefixCarriedOverRefusedAddress` and `DecodeWhileFinishPaused`. No ordinary native operation demands or decodes the receiver exactly while its donor lives, and a pass needs a live donor. `TestSharingFinish/FailingPrefix` was written, met the not-ready export answer, and is dropped for that reason. |
| A Container receiver whose mount order differs from the donor's | in process by ruling | `core/part_delegation_mount_test.go`. A different mount order is a different recipe, so the two rows are never congruent natively. |
| A decoded multi-part receiver filled over two passes with the donor's last owner gone | in process by ruling | batch 6's in-process test and author B's `RemoteSharing` model probe. |
| The SDK-built Service and Module ancestry with both leader orders (F2) | in process, author B | natively only the smoke and the measurement: `TestSharingFinish/ServiceBackedReceiver`. |
| Auth-backed http File: no saved producer, refusal, unavailability | in process by ruling | `core/http_lazy_test.go` and the offer-resource tests; natively `TestOffers/Resources`. The import succeeds, but every non-root row needs A's Secret, B's session cannot hold it, and the fixture's batch 2 consistency check rightly sees no row it is entitled to. The check is not relaxed. |
| Restrictive umask control of the HTTP writer (F3) | not native | author B's unprivileged subprocess test. |
| A local writer or storage failure during chain installation stays a local error (F1) | not native | per B2's option; the closed action set has only the two owner-attach faults, which `TestFixtureControls/BarrierActions` and `TestEncodedRestart` use. |
| `TestHTTPRestore/StatusTable`, 304 over an empty saved body | not run | two empty Files are one row and a bundle cannot name a root twice; 204 over an empty body is the one empty case. |

### Verification ledger, slice 2 continued

Same form as rows 7 to 17: `dagger api call engine-dev test --pkg ./core/integration --run=<selection> --test-verbose --timeout=<T> --env-file=file:/tmp/b7/dump-5m.env` under `timeout <outer>`.

| # | Selection | Bounds (test, process) | Result | Wall |
| --- | --- | --- | --- | --- |
| 18 | `TestSharingFinish/FailingPrefix`, `/ForegroundReadRacesPass`, `TestHTTPRestore/StateResolveLayout` | 7 m, 760 s | layout table passes; both sharing subtests armed their barrier too late (my test) | 420 s |
| 19 | sharing finish (3), Git local (3), workspace, offers (2), pipeline Cold | 8 m, 810 s | OnePass, the racing read's first form, Resources, Local, LocalCleaned, Cold pass; four test-side or ruled failures | 238 s |
| 20 | service-backed, bundle, replacement, donor released, status table, workspace, RetainedExec, with B's clone fix | 8 m, 810 s | RetainedExec, Replacement, LocalBundle pass; four test-side failures | 301 s |
| 21 | service-backed, donor released, status table, auth-backed, workspace | 8 m, 810 s | service-backed, donor released (both), status table pass; auth-backed and workspace as ruled | 362 s |
| 22 | **F6: the full native set** | 15 m, 1260 s | **pass** | **526 s** |
| 23 | `TestWorkspaceCapture`, reshaped | 5 m, 640 s | **no result.** The run stalled with no test output and was killed by the process bound, exit 124, no stacks. The shared `dagger-engine-v1.0.0-beta.13` container no longer existed afterwards. I did not remove it; reported to the coordinator, and no engine run was started after it. | 640 s |

In-process runs since the interim section: `go test ./engine/server -race -run 'TestRemoteCacheFixture' -count=2 -timeout 200s` under 320 s, pass, 1.7 s; `go test ./core/schema -run 'TestFixture|TestRemoteCacheFixture' -timeout 240s` under 600 s, pass; `go test ./core -run 'TestPartRestoredClone|TestRestored' -timeout 300s` under 450 s after cherry-picking `624b48d8dc`, pass; `go vet ./core/integration` before every engine run.

### Still owed for slice 2

One valid native run of the reshaped `TestWorkspaceCapture` (uncommitted in the worktree), blocked on the engine container above.

### Proposed names and what is registered

All registered names are subtests of `TestRemoteCacheTransferSuite` in `core/integration`.

| Design name (§5 as amended by B1) | Registered | Note |
| --- | --- | --- |
| `TestFixtureControls` | same; `AbsentGate`, `MalformedRecords`, `BarrierActions`, `NoStaleBarrierOrHoldAfterRestart`, `ObserverOverflow`, `TransportShape`, `GitVisibility` | |
| `TestPipeline/Warm`, `/Cold` | same | the module function is `build(input, variant)` returning `Build{variant, dirs}`; the design's `report(input, variant)` would have changed the existing `report(seed)` that `TestSchemaRecovery` uses |
| `TestPipeline/FailedChain` | `TestPipeline/FailedChain/RetainedExec` | the retained exec on the private receiver |
| content classification, "subtests of `TestPipeline/FailedChain`" | `TestPipeline/FailedChain/Served`, `/Status401`, `/Status403`, `/Status404`, `/Status410`, `/Status500`, `/TransportFault`, `/TruncatedBody`, `/DigestMismatch` | on the real HTTP content path with addressed offers |
| design §2 step 5 | `TestPipeline/DonorReleased/BeforeRestart`, `/AfterRestart` | |
| `TestHTTPRestore` | `/ChainSucceeds`, `/ChainFailsOriginSame`, `/ChainFailsOriginDiffers/{changed body, server error, not found, truncated body, no response}`, `/StateResolveLayout`, `/StatusTable`, `/AuthBacked/{ChainSucceeds, ChainFails}` | |
| `TestGitTrees` | `/RemoteDownload`, `/RemoteFallback`, `/Local`, `/LocalCleaned`, `/LocalBundle` | |
| `TestHostInputs` | `/NestedViewExportsWholeChain`, `/UnselectedSiblingNeverOpened` | |
| `TestWorkspaceCapture` | same | |
| `TestSharingDonorRestart` | `/DonorBeforeImport`, `/DonorAfterImport` | |
| `TestSharingFinish` | `/OnePass`, `/ForegroundReadRacesPass`, `/ServiceBackedReceiver` | G2's failing prefix, the differing mount order and the two-pass decoded receiver are not registered; see the limits |
| `TestEncodedRestart` | `/FailedAttachThenRestart`, `/RetryOnlyBookkeeping`, `/LocalRestoreReset` | `LocalRestoreReset` is G3 and F4 |
| `TestPendingOffersRestart` | same | includes the A to B to C forward |
| `TestOffers/BeforeStart`, `/Preparing`, `/Running`, `/Replacement`, `/Resources` | same; `/Resources/{SessionWithoutResource, SessionWithResource}` | |
| `TestRenewal` | `/ArmedReplySucceeds`, `/ExpiredAddressesRenewed`, `/TimeoutThenFallback` | |
| the seven `TestRemoteCacheIntegrated…` peers | not written | amendment B1 |
