# Batch 6 implementation report: early snapshot sharing

Replacement implementer `cl-1bc62433d5e6050624a14d087cadcbdf`, 17 September 2026. This report replaces the previous implementer's report; its commits stay in history and are evidence only. Governing documents: the [continuation packet](../../implementation-continuation/b6-continuation/PACKET.md), [decision 3](../../implementation-continuation/b6-continuation/DECISION-3-VERIFICATION-FORM.md), [decision 4](../../implementation-continuation/b6-continuation/DECISION-4-ENABLEMENT-AND-READINESS.md), the design at `6697a4b510` and the designer's readiness note `26578890ce`.

| Identity | Commit |
| --- | --- |
| Branch | `sharing-implementer-fable-impl-07b90299` (fork of `sharing-implementer-implementation-7d93905c` at `aecadc5261`) |
| Base | `a26dc93750e42daf2de76678b0e54f51454cea33` |
| Implementation tip | `5ce25b685e` |
| Evidence tip | the commit that adds this report |
| Production diff | `git diff a26dc93750 5ce25b685e -- . ':!continuation-evidence'` |

## The result in short

The batch's five commits follow the design and decision 4. They carried one serious defect: **the sharing worker deadlocked the whole cache on any engine with sharing enabled**, which the previous report misread as slowness. It is fixed with a regression test. At the candidate tip the four packages pass unprivileged, the race subset passes, and the batch's engine test passes. **Gap:** the packet's own regression pair (`TestPartMixedExecOutputs`, `TestSchemaRecovery`) still has no passing run on batch 6 code. The deadlock wedged it in the previous implementer's run, and the one rerun the coordinator authorized after the fix was killed by a mis-sized outer bound before it could report anything (ledger row 5).

## The blocked read: cause, evidence, fix

**Cause.** Commit 5 (`0a7c5c0c89`) made `selectShareSlots` call `partLookupFor` inside its `egraphMu.Lock()` section. A lookup derives the frame's digests, and a frame that names an input by result ID and has derived nothing yet resolves that reference through `recipeDigestForCachedResult`, which takes `egraphMu.RLock()` (`dagql/result_call_frame.go:1103`). `sync.RWMutex` is not reentrant. The worker waited forever for a lock it held exclusively, and every later cache operation waited behind it. Batch 4's callers already prepare the lookup before E and say why (`newSessionlessPartSourceLease`: "Lookup preparation can resolve persisted numeric references, so it must precede the E section").

**Why it looked like a restart problem.** The digest is memoized per frame. Where the frame had already derived it, the pass got away with it; a restored frame has derived nothing, so the first pass after a restart always deadlocked. It was not limited to restarts or to the new test: in the previous implementer's engine run, `TestPartMixedExecOutputs` sat in one `Stdout` call for 9 minutes with no restart involved, and both `TestSchemaRecovery` orders sat in `Serve` for 7 minutes ([logs/previous-engine-run-excerpt.log](logs/previous-engine-run-excerpt.log), test-process goroutines). The previous report's "It is not a hang. Every selected test was progressing" was wrong.

**Evidence.**
1. In process: `TestSnapshotSharingSelectsRestoredFrame` on the unfixed code times out with the worker at `selectShareSlots -> partLookupFor -> recipeDigestForCachedResult -> RWMutex.RLock` ([logs/regression-unfixed.log](logs/regression-unfixed.log)); fixed, it passes in 0.1 s.
2. Real engine, unfixed code: a goroutine dump taken from nested engine B through its debug endpoint while the read was blocked shows the same stack, from `runSnapshotShareWorker` down to `result_call_frame.go:1103`. B's own never-shared capture blocked the same way, which is why the previous implementer's "control" read proved nothing ([logs/diagnostic-engine-dump.log](logs/diagnostic-engine-dump.log)).
3. Real engine, fixed: ledger row 4.

**Fix** (`e02185abcc`). Every imported member's lookup is prepared before the E section; eligibility and the structural admission stay under E; a lookup error is a traced skip of that receiver.

## Commits

| Commit | Kind | What it does |
| --- | --- | --- |
| `a0ce9c123d`, `3277121605` | evidence | Cherry-picks of the preserved readiness note and rework plan, unchanged. |
| `bf0f5151cc` | production | Commit 1: receiver expiry at the sessionless constructor and at Commit; per-address donor validation (`partDonatedFacts`); the receiver's structural admission recorded and rechecked. |
| `d342205f20` | production | Commit 2: `EnableSnapshotSharing` and the off/on/closed admission state; the per-E-interval notification collector and its sites; the cohort queue, coalescing and worker; the close step after `closeRemoteCacheBridge`. |
| `d71c8ef9b1` | production | Commit 3: `probeAllParts` and unlocked selection; `SetPartPreparationContext`; `ForkForPersistedDecode`; `ContextWithPersistedDecodeDefaults`; the `BackgroundDecode` audit flag and preflight; the preparation marker, guards and refusing provider; decision 4's engine-side enablement. |
| `14b1222e0d` | production | Commit 4: `prepareReadyPartFromBase`; `expectedRepresentation` and `expectedPredecessors`; Commit's validation; the pass's prepare, commit, member-release and Finish phases. |
| `0a7c5c0c89` | production + tests | Commit 5: selection consults structural admission (this introduced the deadlock); a failed preparation no longer aborts its receiver's suffix; test hooks; the in-process matrix; the integration test. |
| `167630f0b7`, `e509c41964` | mixed | A report commit that swept in three test changes, then a commit removing the report again. Net: the nil-callback ineligibility case, the test family's Service field, the core-base timing log. |
| `37631180d3`, `490c386592` | evidence | The previous report and a correction to it. Superseded by this report. |
| `aecadc5261` | tests + evidence | Bounded the blocked read to 30 s and returned success if it blocked. Also added the typed test store and `TestSnapshotSharingTypedReceiverFillsOnePartPerPass` (kept) and edited the old report. Its integration-test change is undone by `e02185abcc`. |
| `e02185abcc` | production + tests | **Mine.** The deadlock fix and its regression test. It also restores the integration test's plain read: I restored that file with `git checkout <commit> -- <path>`, which staged it, and the commit swept it in. The message describes only the fix; `5ce25b685e`'s message records the restoration. The coordinator accepted the commit as is: no amend, no revert-and-reapply pair. |
| `ebef15dae1` | production | **Mine.** Removes the slot stop latch (`stopNow`, `stopOne`, `stopped`, two branches). Commit 5 stopped calling `abortShareSuffix` from the prepare phase, after which nothing could trip it. No behavior change. |
| `e81a971147` | tests | **Mine.** `NoJoinRefusal` and `DecodeWhileFinishPaused` released their barriers only on the success path, and one waited without a bound. Now deferred and bounded. |
| `5ce25b685e` | tests | **Mine.** The integration test's last assertion, never reached before, refused any fixture event for the row after the restart. See "Change to decision 4's wording" below. |

Every commit is signed off with no attribution trailer; nothing was amended. Evidence-only commits to drop before publication: `a0ce9c123d`, `3277121605`, `37631180d3`, `490c386592` and the evidence tip. `167630f0b7` + `e509c41964` net to test changes only. `aecadc5261` also touches the old `REPORT.md`; dropping the evidence directory removes that part.

## Review of commits 4 and 5

Read in full with the batch 4 code they call. Beyond the deadlock:

- **Pass protocol (commit 4).** No wait holds E, a gate or a payload lock. Every slot goroutine reports exactly one prepared outcome and one committed outcome even when `RunLazyTask` refuses admission (`NoJoin` busy) before the Body runs, so the pass cannot wait on a slot that never started. Members are released before any Finish and again by `defer`. A cancelled worker context stops issuing commits and disposes every held preparation. I found nothing to fix.
- **Commit validation.** A preparation with predecessors skips the original-observation stamp and is checked against the expected envelope pointer, payload revision, typed flag and each predecessor's task identity. `hasValue` implies a typed store and `!hasValue` implies an allocated envelope, so `row.persistedEnvelope = p.published` never stores nil. Sound.
- **Dead code** left by commit 5's own change: removed (`ebef15dae1`).
- **Left alone, named here.** After a failed preparation in the middle of a receiver's sequence, the next slot starts from the real record and is refused at Commit once the earlier slot publishes; harmless, and the installing task's completion queues a successor. `closeSnapshotSharing` returns an error that is always nil. `shareAdmissionState` reads the flag under `egraphMu.RLock()` rather than atomically (see costs).

## Change to decision 4's wording

Decision 4 asked that after the restart "a read through the saved handle adds no event for R". That cannot hold on a fixture-enabled engine: every lazy task's continuation records a row-level `owner-sync` event with an empty address before it synchronizes leases (`dagql/cache_part_task.go:76`), and a demand on an already complete part still runs its acquire task. Verified in process: a demand on a settled shared part records exactly one `owner-sync` and nothing else. The test now asserts what the observation means: after the restart no event names one of R's parts (no selection, install, download, evaluation or settle), and the only row-level event is `owner-sync`. Each event for R is logged; the passing engine run logged exactly two, both `owner-sync` with an empty address ([logs/engine-test.log](logs/engine-test.log)). The assertion still fails on any install, download or evaluation after the restart. The coordinator accepted the change; it goes to round 1 as a named change.

## Typed receivers: recommendation

Design §6 says a typed raw Container uses the same ordered preparation. The implementation gives a typed (already decoded) receiver one slot per pass; the installing task's completion queues the pass that fills the next part (`TestSnapshotSharingTypedReceiverFillsOnePartPerPass`).

**Recommendation: keep one slot per pass, and have the designer amend that §6 sentence.** Do not implement the ordered typed path in this batch. The coordinator takes this to round 1 as the author's position; option (a) is not implemented.

- The end state is identical: every part installed once, one pin and one accessor each. The intermediate state (one part present, another not) is already legal, because part finality is independent and the encoded sequence also publishes one Commit at a time.
- The ordered typed path needs the successor's store to expect a revision and, for Container, the exact view its predecessor *will* publish. `PartStorePreparer` and `PartBatchStorePreparer` cannot express that (`dagql/cache_part_store.go`). Adding it changes two batch 4 interfaces, three core stores (`core/part_store.go`), three call sites including the lazy path, and needs an opaque core-owned carrier passed back through DagQL. That is risk in converged code for a narrow case.
- The case is narrow: an imported row is encoded until something loads it, and whatever loads it normally demands its parts at once. A typed receiver with several parts still pending when a pass runs is a short window.
- The cost is bounded and cheap: one extra pass per extra part, each a probe of the cohort with no download and no evaluation.
- What would change my mind: batch 7's native tests showing multi-part typed receivers are common when passes run.

## Where the design was silent

Unchanged from the previous implementer's choices, which I read against the code and accept: per-address donor proof (`partDonatedFacts`: gate output state, offer owner, applied owner link by RefKey and output path, expiry); the receiver's Own-set proof is `requiredSessionResourcesGen`; a share target is an address whose descriptor carries a snapshot identity, so no core part name is consulted; the worker base is the cache lifetime plus the cache and the preparation marker; diagnostics are `slog` plus test hooks, and a sharing Commit emits the existing `installed-ready`; `ErrSnapshotShareIneligible` is a skip that changes no row state; the audit registry is `PersistedObjectFamily.BackgroundDecode`, false by default; the preflight follows declared child references only, holding each row while it reads it; registration and enablement sit immediately before `startRemoteCacheIntegration`; the schema-only fork's view is the engine's own base version; an ordered prefix's envelope pointer is allocated at preparation; a class union keeps both counted operations and the worker retires the surplus outside E; the class filter is "at least one imported member"; selection consults structural admission. Mine: a lookup that cannot be prepared skips that receiver for the pass.

## Deviations

1. Donor part-revision validation landed in commit 1, not commit 4, beside the facts it checks.
2. `PartProbe.OfferRev` is not populated: offer slots are guarded by E and probes run outside it. The per-address offer identity is recorded on the lease inside the constructor's E section and rechecked at Commit. `OutputRev` is populated by `probeAllParts` only.
3. One typed slot per pass (above).
4. The design's `TestPartDecodePublication` does not exist. The real decode tests need a real store and skip unprivileged, so the shared-decode contracts are covered in process by `DecodeWhileFinishPaused` and `FailedFinishAndRetry`.
5. `ServicesAndDecode` is delivered in part (mapping below).

## Ordinary behavior changes

1. An engine that can receive imports (integration configured, or the fixture variable set) builds its core schema base at startup instead of on its first client, and a construction failure fails `NewServer`. Every other engine builds, registers and enables nothing.
2. An engine that imported earlier and restarts with neither has restored Imported rows with sharing off. They lose only early sharing.
3. Close and discard close sharing admission and cancel the worker right after the bridge detach, before the quiescence wait.
4. `CommitReadyPart` validates the expected representation, predecessor identities and revision overflow. For a public single-demand preparation the expectation is the observed representation and the list is empty: unchanged.
5. An encoded Commit publishes the envelope its preparation allocated rather than a copy made at commit time; same content.
6. A sessionless share requires an applied owner link for the donated snapshot, rechecks per-address donor facts and refuses an expired receiver. No ordinary demand is sessionless.
7. The persisted Module decoder's default dependencies go through `persistedDecodeDefaultDeps`; unmarked, that is `query.DefaultDeps(ctx)` verbatim.
8. Guarded boundaries return `ErrSnapshotShareEvaluation` under the marker only.

## Decision 4 costs

1. **Preflight traversal.** Paid only by a typed preparation whose donated descriptor carries Service references: one capture and one reference visit per row in the declared-child closure. Encoded receivers and service-free slots pay nothing.
2. **Guard checks on unmarked paths.** One context-value lookup at each guarded boundary. Not separable from noise in the package timings.
3. **Notification hooks on an engine with sharing off.** Decision 4 says "one flag check". It is a flag check under `egraphMu.RLock()` at each eager publication and each successful lazy completion (`shareAdmissionState`), and a plain field read at sites that already hold E. The read lock queues behind a pending writer. Small beside the exclusive E sections every publication already takes, but it is a lock acquisition, not a free read. An atomic mirror would remove it at the price of a second copy of the state; I did not change it.
4. **Static core base ahead of admission.** Measured 6.7 ms in this run (`TestSnapshotSharingPreparationContext`); the previous implementer measured 11 to 16 ms over five runs. Once, at startup, on enabled engines only.
5. **Audit maintenance.** A new persisted family, or a decoder that starts loading a new reference, makes receivers that need it ineligible until it is audited and marked. 33 families are marked in `core/persisted_families.go` (recounted).

## Decision 4's question: can the worker wait behind anything `GracefulStop` holds?

No. `gcmu` is taken in four places, all in `engine/server` (`gc.go:87`, `:307`, `:323`, `server.go:875`). The worker runs `dagql`, `core` and `engine/snapshots` code, and `go list -deps` of those three contains no `engine/server` (rechecked). The one `engine/server` closure it can run forks the already-built base and binds two context values; it takes no `gcmu` and no session lock. `TestSnapshotSharingShutdown` covers the in-process half: Close waits for an active pass, then returns with an empty queue and zero counted operations.

## §9.2: what has evidence, what is batch 7's

| §9.2 bullet | Evidence here | Batch 7 |
| --- | --- | --- |
| Donor lifetime | `TestSharedHostDirectoryLifetime` on a real engine (ledger row 4). In process: `ReleaseThenExternalFinish`, `ReadinessAndExpiry`. | `TestSharingDonorRestart`: session end, dropped edge, real GC, a restored-but-unopened donor. |
| Encoded multi-part pass | `PreparedSequence` (two and three parts), `IndependentParts`. | `TestSharingFinish`; both parts read after reopen in `TestEncodedRestart`. |
| Donor sibling and failure | `DonorReceivesSibling`. | G2: failing prefix over real storage, zero transient pins. |
| Installed checkpoint | `FailedFinishAndRetry`, `DecodeWhileFinishPaused`, `Shutdown`; across a real restart, the integration test's restored row and link. | `TestEncodedRestart`, `TestPendingOffersRestart`, G3. |
| Decoded service view | Partly: `TestSnapshotSharingPreparationContext`, `TestPersistedDecodeDefaultDeps`, `TestSnapshotSharingTypedServiceReceiverNeedsRegistration`. | **Named gap:** the full `ServicesAndDecode` schedule (encoded Service and Module ancestry, both leader orders, the zero-client and zero-start counters) needs a real module fixture. Belongs with G4. |

## Verification ledger

Rows 1, 2, 4 and 5 at `5ce25b685e`; row 3 on the unfixed code. All unprivileged, no root, default parallelism. Rows 1 and 2 ran concurrently with each other; every engine invocation ran alone.

| # | Command | Timeout | Result | Duration |
| --- | --- | --- | --- | --- |
| 1 | `go test ./dagql ./core ./core/schema ./engine/server -timeout 120s -count=1 -v` | `-timeout 120s`, harness 420 s | pass | dagql 5.5 s, core 5.5 s, core/schema 13.8 s, engine/server 4.3 s; 46 s wall ([logs/packages.log](logs/packages.log)) |
| 2 | `go test -race ./dagql ./core ./engine/server -run 'TestSnapshotSharing\|TestPartSessionless\|TestReadyPartReceipt\|TestPartReadyRevalidation\|TestPersistedDecodeDefaultDeps\|TestSnapshotSharePreparationCoreGuards' -timeout 180s -count=1 -v` | `-timeout 180s`, harness 420 s | pass, no race reported | dagql 3.5 s, core 1.5 s, engine/server 1.5 s; 104 s wall with the race build ([logs/race.log](logs/race.log)) |
| 3 | Diagnostic, unfixed code (`aecadc5261` plus uncommitted dump code): `dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSharedHostDirectoryLifetime$' --test-verbose --timeout=5m` | `--timeout=5m`, outer 600 s | **failed as intended**: both reads blocked, dump captured | test 201.5 s; 7 m 49 s wall ([logs/diagnostic-engine-dump.log](logs/diagnostic-engine-dump.log)) |
| 4 | Final: same selection, `--timeout=4m` | `--timeout=4m`, outer 600 s | **pass** (1 passed) | test 1 m 27 s; 5 m 16 s wall ([logs/engine.log](logs/engine.log); the test's own output, fetched from the recorded trace with `dagger cloud logs`, is [logs/engine-test.log](logs/engine-test.log)) |
| 5 | The packet's regression pair, authorized by the coordinator: `dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/(TestPartMixedExecOutputs\|TestSchemaRecovery)$' --test-verbose --timeout=8m`, run alone | `--timeout=8m`, outer 720 s | **no result: killed by the outer bound** (exit 124) before the test timeout could fire | 720 s wall ([logs/pair-run-killed.log](logs/pair-run-killed.log)) |

Row 4's logged observations: `shared host directory row=4231 selected-ready-before-install=false counts=map[installed-ready:1 settled:1]`, so on a real engine the early sharing pass installed the part and no demand selected a source; and after the restart exactly two events for the row, both `owner-sync` with an empty address.

**Row 5 proves nothing either way.** The bounds were mis-sized: an invocation spends about four and a half minutes loading and building before the first test line, so an 8-minute test timeout needs an outer bound near 14 minutes, and at 12 the outer bound fired first. The test timeout is what prints every test's stack; the kill printed nothing, tore down the nested engines, and left no trace ID, so there was nothing to dump and no way to say where each test was. All that is recorded is the running-step count, flat at 17 from 7 m 00 s to 11 m 30 s. The tests had about seven and a half minutes of test time and had not finished. The numbers were the coordinator's and I did not check their sum before launching; both of us should have. As instructed I did not raise the bound or rerun.

Earlier engine runs by the previous implementer, all on unfixed code. None is evidence of anything but the deadlock:

| Run | Selection and commit | Timeout | What it was |
| --- | --- | --- | --- |
| A | mixed-exec, schema-recovery and the sharing test, at `0a7c5c0c89` | `--timeout=10m` | **Deadlocked.** Package timeout after 10 m 26 s of test time, 13 m 55 s wall. All four tests parked in one call each for 7 to 9 minutes ([logs/previous-engine-run-excerpt.log](logs/previous-engine-run-excerpt.log)). Reported at the time as "not a hang". |
| B | the sharing test alone, at `490c386592` | `--timeout=10m` | **Deadlocked.** Package timeout at 600 s, 13 m 12 s wall; the test parked in `Entries` after the restart for 9 minutes (`/tmp/b6/engine-single.log`, not committed). |
| C | the sharing test alone with the 30 s bounded read (`aecadc5261`) | not recorded | **Terminated by the coordinator** after 11 minutes with no result (`/tmp/b6/engine-single2.log`, not committed). |

Row 1 covers every new case: 21 `TestSnapshotSharing*` tests in `dagql`, 2 in `core`, 3 in `engine/server`; none skips. 112 subtests skip in those packages, 111 with "operation not permitted": the pre-existing real-store tests (decision 2). Development-loop runs of single tests (each with `-timeout` 30 to 90 s) are not listed; row 1 supersedes them.

## Pre-existing slop

1. **Every real-snapshot-store test skips unprivileged** (`engine/snapshots/testutil/store.go:74-92`). 111 subtests in these four packages produce no evidence in any permitted run, including the two decode tests the design wanted extended. Decision 4's G5 carries the fix to batch 7.
2. **The nested engines' logs never reach the test output.** A hang inside a nested dev engine is invisible from `--test-verbose`; the only way to see it was to expose the debug endpoint and fetch a dump from inside the test. `core/integration/engine_test.go:865` already does this for one test. A shared helper that dumps a nested engine's goroutines when a bounded call blocks would have saved two ten-minute runs here.
3. **Each `engine-dev test` invocation rebuilds the engine before the first test line** (the single test ran 1 m 27 s inside a 5 m 16 s invocation). Known; this batch's replacement ran three invocations: one diagnostic, the final single test, and the pair the coordinator authorized.
4. **A passing `engine-dev test --test-verbose` run prints no test output when its output is captured to a file**: only the summary and a trace URL. The verification time rule takes per-test durations and logged observations "from the verbose output", which on a pass exists only in the cloud trace (`dagger cloud logs <trace> --test <name>`). Whoever needs a passing run's `t.Logf` lines has to know that.
5. `core/schema/foreign_module_context_test.go` is not gofmt-clean at the base, and `go vet ./engine/server` reports `session_attachables.go:211` (previous implementer's observation; untouched).

## Limits and open items

- The in-process cases use a fake snapshot manager: they prove ownership, ordering, holds and lock discipline, not bytes.
- **`TestPartMixedExecOutputs` and `TestSchemaRecovery` have no passing run on this batch** (ledger row 5 and the paragraph under it). Whether they are slow on this host or parked is unknown. Recommended, for the coordinator to decide: one rerun with the same `--timeout=8m` and an outer bound of 14 minutes, so that a test that does not finish fails by its own timeout and prints every test goroutine, and with the nested engines' debug endpoint exposed the way the diagnostic run did if a dump is wanted. I reread the batch's remaining graph-lock sections (`partDonatedFactsLocked`, `CommitReadyPart`'s donor check, the queue and hook sites) for a second bug of the deadlock's class and found none; that is reading, not evidence.
- The regression test for the deadlock wedges the graph lock when it fails, so on a regression it ends at the package `-timeout` with every stack printed rather than at its own bounded wait.
- `Triggers` does not force a publication rollback inside the indexing interval or an attachment failure after the early flush; both need fault injection that does not exist at this base.
