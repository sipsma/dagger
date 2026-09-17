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
