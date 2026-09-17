# Batch 6 implementation report: early snapshot sharing

Replacement implementer `cl-1bc62433d5e6050624a14d087cadcbdf`, 17 September 2026. This report replaces the previous implementer's report; its commits stay in history and are evidence only. Governing documents: the [continuation packet](../../implementation-continuation/b6-continuation/PACKET.md), [decision 3](../../implementation-continuation/b6-continuation/DECISION-3-VERIFICATION-FORM.md), [decision 4](../../implementation-continuation/b6-continuation/DECISION-4-ENABLEMENT-AND-READINESS.md), the design at `6697a4b510` and the designer's readiness note `26578890ce`.

| Identity | Commit |
| --- | --- |
| Branch | `sharing-implementer-fable-impl-07b90299` (fork of `sharing-implementer-implementation-7d93905c` at `aecadc5261`) |
| Base | `a26dc93750e42daf2de76678b0e54f51454cea33` |
| Implementation tip | `98f954c91e` (round 1 candidate was `5ce25b685e`) |
| Evidence tip | the commit that adds this report |
| Production diff | `git diff a26dc93750 98f954c91e -- . ':!continuation-evidence'` |

## The result in short

The batch's five commits follow the design and decision 4, and they carried **four serious defects**: the deadlock I fixed before round 1, and round 1's three blocking findings.

1. The sharing worker deadlocked the whole cache on any engine with sharing enabled (fixed in `e02185abcc`; round 1 candidate).
2. Commit 4 broke the publication of every evaluated Lazy operation, with sharing on or off: a silent spin, and behind it a nil envelope (R1: `da4e91cf03`, `2831250c3b`).
3. A refused sharing slot could fail a user's demand that had joined its task (R2: `2be7281914`).
4. Under cancellation a slot's launching call could report a refusal while its Body still ran, losing an installed receipt, a pin and a receiver hold (R3: `0a08aa1cc9`).

Round 1 found 2 (the generic reviewer by reading, I from an engine dump), 3 and 4. All are fixed with tests that fail on the previous code. At the final tip the four packages pass unprivileged, the race selection passes, and one engine invocation of the batch's integration test together with the packet's regression pair passes (ledger). The previous implementer's report had called the first of these "not a hang".

## The blocked read: cause, evidence, fix

**Cause.** Commit 5 (`0a7c5c0c89`) made `selectShareSlots` call `partLookupFor` inside its `egraphMu.Lock()` section. A lookup derives the frame's digests, and a frame that names an input by result ID and has derived nothing yet resolves that reference through `recipeDigestForCachedResult`, which takes `egraphMu.RLock()` (`dagql/result_call_frame.go:1103`). `sync.RWMutex` is not reentrant. The worker waited forever for a lock it held exclusively, and every later cache operation waited behind it. Batch 4's callers already prepare the lookup before E and say why (`newSessionlessPartSourceLease`: "Lookup preparation can resolve persisted numeric references, so it must precede the E section").

**Why it looked like a restart problem.** The digest is memoized per frame. Where the frame had already derived it, the pass got away with it; a restored frame has derived nothing, so the first pass after a restart always deadlocked. It was not limited to the new test: in the previous implementer's engine run both `TestSchemaRecovery` orders sat in `Serve` after their restart for 7 minutes, and `TestPartMixedExecOutputs` sat in one `Stdout` call for 9 minutes ([logs/previous-engine-run-excerpt.log](logs/previous-engine-run-excerpt.log), test-process goroutines). The previous report's "It is not a hang. Every selected test was progressing" was wrong. **Correction to my own first reading:** I attributed all four parked tests to this deadlock. For mixed-exec that was probably wrong: it has no restart and was parked in exactly the call the second defect below spins in. A test-process dump cannot tell the two apart.

**Evidence.**
1. In process: `TestSnapshotSharingSelectsRestoredFrame` on the unfixed code times out with the worker at `selectShareSlots -> partLookupFor -> recipeDigestForCachedResult -> RWMutex.RLock` ([logs/regression-unfixed.log](logs/regression-unfixed.log)); fixed, it passes in 0.1 s.
2. Real engine, unfixed code: a goroutine dump taken from nested engine B through its debug endpoint while the read was blocked shows the same stack, from `runSnapshotShareWorker` down to `result_call_frame.go:1103`. B's own never-shared capture blocked the same way, which is why the previous implementer's "control" read proved nothing ([logs/diagnostic-engine-dump.log](logs/diagnostic-engine-dump.log)).
3. Real engine, fixed: ledger row 4.

**Fix** (`e02185abcc`). Every imported member's lookup is prepared before the E section; eligibility and the structural admission stay under E; a lookup error is a traced skip of that receiver.

## The second defect: publishing an evaluated Lazy operation spun forever

Found by the pair rerun the coordinator ordered (ledger row 6), which is why that run was worth its cost.

**Cause.** Commit 4 (`14b1222e0d`) changed `CommitReadyPart`'s payload check from the preparation's own observation (`p.version.payload`) to the new `p.expectedRepresentation`. `PreparedReadyPart` has two constructors. `prepareReadyPartFromBase` sets the field. `prepareEvaluatedParts` (`dagql/cache_part_lazy.go:62`), which prepares the publication of an evaluated Lazy operation, was not touched and left it zero: revision 0, nil envelope, no typed value. No real row matches that, so Commit returned `ErrPartReselect` every time, and `publishEvaluatedParts` retries on reselect with no bound but context cancellation. On batch 6 code every publication of an evaluated Lazy operation of a part-acquisition row spun a core forever and its demand never returned. That is mixed-exec's `execMeta` step.

**Why the packages did not catch it.** The batch 4 tests that publish a lazy operation over a real row need a real snapshot store and skip unprivileged; `TestPartLazyOperationMissingOutputStopsOnce` stops before Commit.

**Evidence.** At 5 m 30 s of test time the test process was parked in `loaded.Stdout` (`remote_cache_mixed_exec_test.go:106`), and nested engine B's dump shows the Lazy operation's Body goroutine *runnable* inside `runLazyOperationDecision -> publishEvaluatedParts -> prepareEvaluatedParts`, its waiters five minutes in `waitForLazyEvaluation`, and the sharing worker idle at its wake select ([logs/pair-rerun-spin.log](logs/pair-rerun-spin.log)). In process, `TestPartLazyOperationPublishesOverObservedRepresentation` spins for its whole 10 s bound on the previous code ([logs/lazy-publish-unfixed.log](logs/lazy-publish-unfixed.log)) and passes at once with the fix.

**Fix, in two commits.** `da4e91cf03` taught Commit a rule for the stamp alone. The generic reviewer found the other half (R1): commit 4 had also made an encoded Commit install `p.published`, which only `prepareReadyPartFromBase` allocated, so with the stamp fixed the same publication succeeded and installed a **nil envelope**, and my test, which looked only at the snapshot link, did not notice. `2831250c3b` replaces the per-field rule: both constructors end with one shared step, `seal`, which records the expected representation, the predecessors and, for an encoded receiver, the envelope to install, and requires a typed receiver to carry its store. Commit checks those invariants before any lock and answers a violation with an error that names it, never with a reselect. The tests publish over an encoded and a typed receiver and assert the published representation; they fail on both earlier states ([logs/lazy-publish-unfixed.log](logs/lazy-publish-unfixed.log) for the first).

## Commits

| Commit | Kind | What it does |
| --- | --- | --- |
| `a0ce9c123d`, `3277121605` | evidence | Cherry-picks of the preserved readiness note and rework plan, unchanged. |
| `bf0f5151cc` | production | Commit 1: receiver expiry at the sessionless constructor and at Commit; per-address donor validation (`partDonatedFacts`); the receiver's structural admission recorded and rechecked. |
| `d342205f20` | production | Commit 2: `EnableSnapshotSharing` and the off/on/closed admission state; the per-E-interval notification collector and its sites; the cohort queue, coalescing and worker; the close step after `closeRemoteCacheBridge`. |
| `d71c8ef9b1` | production | Commit 3: `probeAllParts` and unlocked selection; `SetPartPreparationContext`; `ForkForPersistedDecode`; `ContextWithPersistedDecodeDefaults`; the `BackgroundDecode` audit flag and preflight; the preparation marker, guards and refusing provider; decision 4's engine-side enablement. |
| `14b1222e0d` | production | Commit 4: `prepareReadyPartFromBase`; `expectedRepresentation` and `expectedPredecessors`; Commit's validation; the pass's prepare, commit, member-release and Finish phases. |
| `0a7c5c0c89` | production + tests | Commit 5: selection consults structural admission (this introduced the deadlock); a failed preparation no longer aborts its receiver's suffix; test hooks; the in-process matrix; the integration test. |
| `167630f0b7`, `e509c41964` | mixed | `167630f0b7` carries three test files (`dagql/cache_snapshot_sharing_family_test.go`, `dagql/cache_snapshot_sharing_test.go`, `engine/server/snapshot_sharing_test.go`) together with the first report and its logs; `e509c41964` takes the report and logs back out and touches nothing else. **Neither is purely evidence: publication packaging must keep the test content of `167630f0b7`** (the nil-callback ineligibility case, the test family's Service field, the core-base timing log) and may drop only its `continuation-evidence/` part, which is exactly what `e509c41964` removes. |
| `37631180d3`, `490c386592` | evidence | The previous report and a correction to it. Superseded by this report. |
| `aecadc5261` | tests + evidence | Bounded the blocked read to 30 s and returned success if it blocked. Also added the typed test store and `TestSnapshotSharingTypedReceiverFillsOnePartPerPass` (kept) and edited the old report. Its integration-test change is undone by `e02185abcc`. |
| `e02185abcc` | production + tests | **Mine.** The deadlock fix and its regression test. It also restores the integration test's plain read: I restored that file with `git checkout <commit> -- <path>`, which staged it, and the commit swept it in. The message describes only the fix; `5ce25b685e`'s message records the restoration. The coordinator accepted the commit as is: no amend, no revert-and-reapply pair. |
| `ebef15dae1` | production | **Mine.** Removes the slot stop latch (`stopNow`, `stopOne`, `stopped`, two branches). Commit 5 stopped calling `abortShareSuffix` from the prepare phase, after which nothing could trip it. No behavior change. |
| `e81a971147` | tests | **Mine.** `NoJoinRefusal` and `DecodeWhileFinishPaused` released their barriers only on the success path, and one waited without a bound. Now deferred and bounded. |
| `5ce25b685e` | tests | **Mine.** The integration test's last assertion, never reached before, refused any fixture event for the row after the restart. See "Change to decision 4's wording" below. |

Round 1 corrections, all mine, in order:

| Commit | Decision | What it does |
| --- | --- | --- |
| `da4e91cf03` | R1, first half | `CommitReadyPart` holds a preparation with no predecessors to its own observation. Landed before the coordinator's message about the second half reached me; incomplete on its own: Commit then succeeded and installed a nil envelope on an encoded row. |
| `2831250c3b` | R1 | Both constructors of `PreparedReadyPart` end with one shared step, `seal`; Commit reads what it recorded and answers a violated construction invariant with a named error outside the reselect class. `da4e91cf03`'s per-field rule is removed again. Tests publish an evaluated Lazy operation over an encoded and a typed receiver and assert the published representation; a second test breaks a real preparation each way. |
| `2be7281914` | R2 (A1) | A slot that ends without installing returns `ErrPartReselect`; a real local pin, open or decode error keeps its cause; the private sentinel is gone. Real-form regression: a `demandPart` joins a slot that is then refused and completes by its ordinary route. Adds one kernel test hook at the join point and a part router for the in-process test family. |
| `0a08aa1cc9` | R3 (A2) | One reporter per slot outcome. The launcher waits for the kernel's attempt without a cancelable context and reports only when no Body was admitted; an admitted Body reports after its own cleanup, delivers an Installed receipt on every path and returns after member release; the pass clears donor and prefix references before the first Finish. Three barrier tests. |
| `6d5e2951be` | R9 | `FailedFinishLastOwner`, `EncodedInstallRetryAfterDecode` (failed and partial attachment), and the import triggers (a live import queues; a failed identity plan or root validation queues nothing). |
| `5de3fef067` | R4 | The last successful prefix per receiver is carried over a refused address. |
| `3a5b5fcd80` | R5 (A3) | The typed allowance is spent only by a slot that passed the slot-context checks; a decoded receiver's successor cohort is queued in the E section that releases the active cohort, before the first decrement. |
| `e5369037ef` | R6 | A receiver's admitted equivalents are collected once per selection and intersected with the cohort. |
| `dd1efd8937` | R7 | An executed marked decode of an encoded Service whose module context is an encoded Module; the preflight skips typed rows and requires the decoding server's native class; root and factory agreement is checked by the engine callback before any shared attempt. |
| `b194ad3204` | R8 | The multi-address probe treats a row whose lease guard is held as busy, without waiting. |
| `06abb874e3` | R10 | The small cuts: `closeSnapshotSharing` without context or error, no duplicated release, dead fields removed, set-once registration, the fixture's non-empty test, the Module decoder's query error wrapper restored. |
| `98f954c91e` | R12 | A behavior-neutral warning when a reselect loop passes 16384 iterations, repeating at doublings. |

Every commit is signed off with no attribution trailer; nothing was amended. Evidence-only commits to drop before publication: `a0ce9c123d`, `3277121605`, `37631180d3`, `490c386592` and the evidence tip. `167630f0b7` + `e509c41964` net to test changes only and are not droppable as a pair. `aecadc5261` likewise carries test content (the typed test store and one test) beside an edit to the old `REPORT.md`; keep the tests, drop the evidence directory part.

## Review of commits 4 and 5, and what it missed

I read both commits in full before round 1 and reported one defect (the deadlock) and one piece of dead code. **That review missed three blocking defects**, which round 1 and an engine run found:

- I checked that every slot reports one prepared and one committed outcome, and did not ask who owns an outcome once a Body is running under cancellation (R3).
- I checked Commit's new validation against the constructor beside it, and not against `PreparedReadyPart`'s other constructor (R1).
- I did not follow what an ordinary demand sees when it joins a slot's task (R2).

What the review did establish and still holds: no wait in the pass holds E, a gate or a payload lock; members are released before any Finish and again by `defer`; the stop latch commit 5 left dead is removed (`ebef15dae1`). The three items it named and left alone are now settled: the refused middle address (R4), `closeSnapshotSharing`'s always-nil error (R10), and the admission flag read under `egraphMu.RLock()`, which stays and is a named cost.

## Change to decision 4's wording

Decision 4 asked that after the restart "a read through the saved handle adds no event for R". That cannot hold on a fixture-enabled engine: every lazy task's continuation records a row-level `owner-sync` event with an empty address before it synchronizes leases (`dagql/cache_part_task.go:76`), and a demand on an already complete part still runs its acquire task. Verified in process: a demand on a settled shared part records exactly one `owner-sync` and nothing else. The test now asserts what the observation means: after the restart no event names one of R's parts (no selection, install, download, evaluation or settle), and the only row-level event is `owner-sync`. Each event for R is logged; the passing engine run logged exactly two, both `owner-sync` with an empty address ([logs/engine-test.log](logs/engine-test.log)). The assertion still fails on any install, download or evaluation after the restart. The coordinator accepted the change; it goes to round 1 as a named change.

## Typed receivers (R5, amendment A3)

A receiver that is already decoded takes one slot per pass; the design's §6 sentence is superseded by A3. My round 1 recommendation argued that the end state was identical to ordered preparation. **That premise was wrong**, as the council showed: the pass releases its members before the first part's Finish, so when the cohort hold was the donor's last owner the donor was collected before the completion trigger could queue a successor, and every later part was lost to sharing. There was also a plain bug: the allowance was spent before the slot-context checks, so an ineligible first address suppressed an eligible sibling.

Both are fixed in `3a5b5fcd80`: the allowance is spent only by a slot that passed `shareSlotContext`, and when a decoded receiver's slot Installed and selection had left it a further address, the successor cohort is queued in the E section that releases the active cohort, before the first decrement, so the donor is held continuously. `TestSnapshotSharingTypedSuccessorHoldsTheDonor` removes the donor's session and saved edge while the first pass is parked and still ends with both parts installed.

Not done, and the coordinator does not require it: spending the allowance on the first successful *preparation* rather than the first slot that passed the context checks. I see no cheap clean way. Every further typed slot would have to be launched and then stopped, which is the stop latch this branch removed; a refused typed preparation therefore leaves that receiver to a later trigger or an ordinary demand.

## Where the design was silent

Unchanged from the previous implementer's choices, which I read against the code and accept: per-address donor proof (`partDonatedFacts`: gate output state, offer owner, applied owner link by RefKey and output path, expiry); the receiver's Own-set proof is `requiredSessionResourcesGen`; a share target is an address whose descriptor carries a snapshot identity, so no core part name is consulted; the worker base is the cache lifetime plus the cache and the preparation marker; diagnostics are `slog` plus test hooks, and a sharing Commit emits the existing `installed-ready`; `ErrSnapshotShareIneligible` is a skip that changes no row state; the audit registry is `PersistedObjectFamily.BackgroundDecode`, false by default; the preflight follows declared child references only, holding each row while it reads it; registration and enablement sit immediately before `startRemoteCacheIntegration`; the schema-only fork's view is the engine's own base version; an ordered prefix's envelope pointer is allocated at preparation; a class union keeps both counted operations and the worker retires the surplus outside E; the class filter is "at least one imported member"; selection consults structural admission. Mine: a lookup that cannot be prepared skips that receiver for the pass.

## Deviations

1. Donor part-revision validation landed in commit 1, not commit 4, beside the facts it checks.
2. `PartProbe.OfferRev` is not populated: offer slots are guarded by E and probes run outside it. The per-address offer identity is recorded on the lease inside the constructor's E section and rechecked at Commit. `OutputRev` is populated by `probeAllParts` only.
3. One decoded receiver slot per pass. Accepted by amendment A3 on two conditions, both implemented (above).
4. The design's `TestPartDecodePublication` does not exist. The real decode tests need a real store and skip unprivileged, so the shared-decode contracts are covered in process by `DecodeWhileFinishPaused` and `FailedFinishAndRetry`.
5. `ServicesAndDecode` is delivered in part (mapping below).
6. **The worker base carries no operation-lease provider**, which design §4.3 listed. Accepted by amendment A4 with this reasoning: the kernel's `withOperationLease` proceeds without a provider; a pass creates no storage resource outside `SnapshotManager.PinSnapshot`, which makes and owns its own pin lease and attaches the snapshot and its ancestry before opening; the receiver's later owner lease comes from ordinary row lease synchronization. The generic reviewer agrees nothing a pass touches is unprotected. A decoded receiver's second accessor on a real snapshot manager was not traced and is on the batch 7 list.
7. A canceled Finish has no sharing variant in `EncodedInstallRetryAfterDecode`: the pass finishes on an uncancelable context. Batch 4's `TestReadyPartReceipt` covers a canceled external Finish.

## Ordinary behavior changes

1. An engine that can receive imports (integration configured, or the fixture variable set) builds its core schema base at startup instead of on its first client, and a construction failure fails `NewServer`. Every other engine builds, registers and enables nothing.
2. An engine that imported earlier and restarts with neither has restored Imported rows with sharing off. They lose only early sharing.
3. Close and discard close sharing admission and cancel the worker right after the bridge detach, before the quiescence wait.
4. `CommitReadyPart` validates the expected representation, predecessor identities and revision overflow. For a public single-demand preparation the expectation is the observed representation and the list is empty: unchanged.
5. An encoded Commit publishes the envelope its preparation allocated rather than a copy made at commit time; same content.
6. A sessionless share requires an applied owner link for the donated snapshot, rechecks per-address donor facts and refuses an expired receiver. No ordinary demand is sessionless.
7. The persisted Module decoder's default dependencies go through `persistedDecodeDefaultDeps`; unmarked, that is `query.DefaultDeps(ctx)` verbatim.
8. Guarded boundaries return `ErrSnapshotShareEvaluation` under the marker only.
9. **The notification hooks run on every engine.** Each eager publication and each successful lazy completion reads the admission flag under `egraphMu.RLock()`, and the lookup, identity-teaching, import and publication intervals that already hold E read it as a plain field. With admission off that is all they do.
10. **Every part preparation is sealed and Commit refuses a broken one with an error** (`2831250c3b`). For the two existing constructors nothing changes; a preparation that violates a construction invariant used to be answered with a reselect.
11. **A row whose lease guard is held is busy for a sharing pass** (`b194ad3204`). Only the multi-address sharing probe looks; the demand-side probe is unchanged.
12. **The two unbounded reselect loops log a warning** after 16384 iterations and at each doubling (`98f954c91e`). Nothing else about them changes.
13. `SetPartPreparationContext` refuses a nil callback, a replacement, a first registration after admission and any registration after close. The engine registers once before enabling, so it is unaffected.

## Decision 4 costs

1. **Preflight traversal.** Paid only by a typed preparation whose donated descriptor carries Service references: one capture and one reference visit per row in the declared-child closure. Encoded receivers and service-free slots pay nothing.
2. **Guard checks on unmarked paths.** One context-value lookup at each guarded boundary. Not separable from noise in the package timings.
3. **Notification hooks on an engine with sharing off.** Decision 4 says "one flag check". It is a flag check under `egraphMu.RLock()` at each eager publication and each successful lazy completion (`shareAdmissionState`), and a plain field read at sites that already hold E. The read lock queues behind a pending writer. Small beside the exclusive E sections every publication already takes, but it is a lock acquisition, not a free read. An atomic mirror would remove it at the price of a second copy of the state; I did not change it.
4. **Joined-demand latency.** A demand for an address that a slot is working on joins the slot's task and waits for what the slot waits for: the pass's remaining preparations and commits, the member release, and the Finishes ahead of its own. A pass downloads and evaluates nothing, so the wait is bounded by local work, but it is a wait an ordinary demand did not have before (amendment A1).
5. **Static core base ahead of admission.** Measured 4.7 ms in the final run and 6.7 ms in the round 1 run (`TestSnapshotSharingPreparationContext`); the previous implementer measured 11 to 16 ms over five runs. Once, at startup, on enabled engines only.
6. **Audit maintenance.** A new persisted family, or a decoder that starts loading a new reference, makes receivers that need it ineligible until it is audited and marked. 33 families are marked in `core/persisted_families.go` (recounted).

## Decision 4's question: can the worker wait behind anything `GracefulStop` holds?

No. `gcmu` is taken in four places, all in `engine/server` (`gc.go:87`, `:307`, `:323`, `server.go:875`). The worker runs `dagql`, `core` and `engine/snapshots` code, and `go list -deps` of those three contains no `engine/server` (rechecked). The one `engine/server` closure it can run forks the already-built base and binds two context values; it takes no `gcmu` and no session lock. `TestSnapshotSharingShutdown` covers the in-process half: Close waits for an active pass, then returns with an empty queue and zero counted operations.

## §9.2: what has evidence, what is batch 7's

| §9.2 bullet | Evidence here | Batch 7 |
| --- | --- | --- |
| Donor lifetime | `TestSharedHostDirectoryLifetime` on a real engine (ledger row 4). In process: `ReleaseThenExternalFinish`, `ReadinessAndExpiry`. | `TestSharingDonorRestart`: session end, dropped edge, real GC, a restored-but-unopened donor. |
| Encoded multi-part pass | `PreparedSequence` (two and three parts), `IndependentParts`. | `TestSharingFinish`; both parts read after reopen in `TestEncodedRestart`. |
| Donor sibling and failure | `DonorReceivesSibling`. | G2: failing prefix over real storage, zero transient pins. |
| Installed checkpoint | `FailedFinishAndRetry`, `FailedFinishLastOwner`, `EncodedInstallRetryAfterDecode` (failed and partial attachment), `DecodeWhileFinishPaused`, `Shutdown`; across a real restart, the integration test's restored row and link. | `TestEncodedRestart`, `TestPendingOffersRestart`, G3. |
| Decoded service view | `engine/server` `TestSnapshotSharingMarkedDecodeOfEncodedService`: a saved Service whose module context is a saved minimal Module, both genuinely encoded after a reopen, decoded under the marker through the registered callback; native values, the exact Module row, the pure factory asked once. Plus `TestSnapshotSharingPreparationContext`, `TestPersistedDecodeDefaultDeps`, `TestCheckPersistedDecodeDefaults`, `TestSnapshotSharingDecodePreflight`, `TestSnapshotSharingTypedServiceReceiverNeedsRegistration`. **Before `dd1efd8937` no marked decode was executed at all**; the previous wording ("the full schedule is missing") understated that. | The two leader orders (sharing-first and foreground-first joining one shared decode) and the SDK-built ancestry: ModuleSource, runtime Container, type definitions. With G4. |

## Verification ledger

All unprivileged, no root, default parallelism; every engine invocation ran alone. Rows 1 to 5 are the round 1 candidate's (`5ce25b685e`; row 3 on the unfixed code); rows 6 to 8 are the pair reruns that found and then cleared the publication spin; **rows 9 to 11 are the final tip `98f954c91e`**. Rows 1 and 2 ran concurrently with each other, and so did rows 9 and 10.

**Engine invocations are an exception, not the normal form.** The time and slop rule allows one cheap engine test per batch. This batch's replacement ran seven: the diagnostic (3), the round 1 single test (4), and five the coordinator authorized one by one because the packet's own regression had only ever deadlocked on batch 6 code (5 to 8 and 11; 7 was stopped seconds after launch). Each is recorded for what it was.

| # | Command | Timeout | Result | Duration |
| --- | --- | --- | --- | --- |
| 1 | `go test ./dagql ./core ./core/schema ./engine/server -timeout 120s -count=1 -v` | `-timeout 120s`, harness 420 s | pass | dagql 5.5 s, core 5.5 s, core/schema 13.8 s, engine/server 4.3 s; 46 s wall ([logs/packages.log](logs/packages.log)) |
| 2 | `go test -race ./dagql ./core ./engine/server -run 'TestSnapshotSharing\|TestPartSessionless\|TestReadyPartReceipt\|TestPartReadyRevalidation\|TestPersistedDecodeDefaultDeps\|TestSnapshotSharePreparationCoreGuards' -timeout 180s -count=1 -v` | `-timeout 180s`, harness 420 s | pass, no race reported | dagql 3.5 s, core 1.5 s, engine/server 1.5 s; 104 s wall with the race build ([logs/race.log](logs/race.log)) |
| 3 | Diagnostic, unfixed code (`aecadc5261` plus uncommitted dump code): `dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSharedHostDirectoryLifetime$' --test-verbose --timeout=5m` | `--timeout=5m`, outer 600 s | **failed as intended**: both reads blocked, dump captured | test 201.5 s; 7 m 49 s wall ([logs/diagnostic-engine-dump.log](logs/diagnostic-engine-dump.log)) |
| 4 | Round 1: same selection at `5ce25b685e`, `--timeout=4m` | `--timeout=4m`, outer 600 s | **pass** (1 passed) | test 1 m 27 s; 5 m 16 s wall ([logs/engine.log](logs/engine.log); the test's own output, fetched from the recorded trace with `dagger cloud logs`, is [logs/engine-test.log](logs/engine-test.log)) |
| 5 | The packet's regression pair, authorized by the coordinator: `dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/(TestPartMixedExecOutputs\|TestSchemaRecovery)$' --test-verbose --timeout=8m`, run alone | `--timeout=8m`, outer 720 s | **no result: killed by the outer bound** (exit 124) before the test timeout could fire | 720 s wall ([logs/pair-run-killed.log](logs/pair-run-killed.log)) |
| 6 | Pair rerun at `5ce25b685e` with the uncommitted watchdog (goroutine dumps of the test process and every nested engine at 5 m 30 s of test time) | `--timeout=7m`, outer 810 s | **failed, and found the publication spin**: `TestPartMixedExecOutputs` hit the 7 m timeout parked in `Stdout` with engine B's Lazy operation Body runnable inside `publishEvaluatedParts`; `TestSchemaRecovery`'s three subtests returned without a failure but got **no recorded PASS**, because the package died on its sibling's timeout | 656 s wall ([logs/pair-rerun-spin.log](logs/pair-rerun-spin.log)) |
| 7 | Pair at `da4e91cf03` | `--timeout=7m`, outer 780 s | **stopped by me a few seconds after launch**, before it printed a line: the coordinator's message that `da4e91cf03` was incomplete arrived just after I launched it. No result. | seconds |
| 8 | Pair at `2831250c3b` with the uncommitted watchdog | `--timeout=7m`, outer 780 s | **pass**: `TestPartMixedExecOutputs` 1 m 20 s; `TestSchemaRecovery` 2 m 15 s (before 1 m 16 s, after 1 m 16 s, foreign_context 52.5 s); the watchdog never fired | 365 s wall ([logs/pair-pass.log](logs/pair-pass.log), verdicts from trace `0055bf644b10f36a9ff9eef16dcf7f44`) |
| 9 | Final: `go test ./dagql ./core ./core/schema ./engine/server -timeout 120s -count=1 -v` at `98f954c91e` | `-timeout 120s`, harness 500 s | pass | dagql 5.0 s, core 2.4 s, core/schema 13.8 s, engine/server 5.1 s; 61 s wall ([logs/final-packages.log](logs/final-packages.log)) |
| 10 | Final: `go test -race ./dagql ./core ./engine/server -run 'TestSnapshotSharing\|TestPartSessionless\|TestReadyPartReceipt\|TestPartReadyRevalidation\|TestPartLazyOperation\|TestCommitReadyPart\|TestPartReselectWatch\|TestPersistedDecodeDefaultDeps\|TestCheckPersistedDecodeDefaults\|TestSnapshotSharePreparationCoreGuards' -timeout 180s -count=1 -v` | `-timeout 180s`, harness 500 s | pass, no race reported | dagql 5.0 s, core 1.6 s, engine/server 1.6 s; 91 s wall with the race build ([logs/final-race.log](logs/final-race.log)) |
| 11 | Final, one invocation and one build at `98f954c91e` with the uncommitted watchdog: `dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/(TestSharedHostDirectoryLifetime\|TestPartMixedExecOutputs\|TestSchemaRecovery)$' --test-verbose --timeout=7m` | `--timeout=7m`, outer 780 s | **pass**, every test with a recorded verdict: `TestSharedHostDirectoryLifetime` 1 m 24 s, `TestPartMixedExecOutputs` 1 m 16 s, `TestSchemaRecovery` 2 m 16 s (before 1 m 14 s, after 1 m 15 s, foreign_context 50.6 s); the watchdog never fired | 370 s wall ([logs/final-engine.log](logs/final-engine.log); the sharing test's own output is [logs/final-engine-sharing-test.log](logs/final-engine-sharing-test.log); verdicts from trace `d0dcde49df2870e51f08f020b72e93d5`) |

Rows 4 and 11 logged the same observations (log lines by intent, not assertions: an ordinary demand may legitimately win the install, so the test requires only what holds on both routes): `shared host directory row=4231 selected-ready-before-install=false counts=map[installed-ready:1 settled:1]`, so on a real engine the early sharing pass installed the part and no demand selected a source; and after the restart exactly two events for the row, both `owner-sync` with an empty address.

**Row 5 proves nothing either way.** The bounds were mis-sized: an invocation spends about four and a half minutes loading and building before the first test line, so an 8-minute test timeout needs an outer bound near 14 minutes, and at 12 the outer bound fired first. The test timeout is what prints every test's stack; the kill printed nothing, tore down the nested engines, and left no trace ID, so there was nothing to dump and no way to say where each test was. All that is recorded is the running-step count, flat at 17 from 7 m 00 s to 11 m 30 s. The tests had about seven and a half minutes of test time and had not finished. The numbers were the coordinator's and I did not check their sum before launching; both of us should have. As instructed I did not raise the bound or rerun.

Earlier engine runs by the previous implementer, all on unfixed code. None is evidence of anything but the two hangs:

| Run | Selection and commit | Timeout | What it was |
| --- | --- | --- | --- |
| A | mixed-exec, schema-recovery and the sharing test, at `0a7c5c0c89` | `--timeout=10m` | **Hung.** Package timeout after 10 m 26 s of test time, 13 m 55 s wall. All four tests parked in one call each for 7 to 9 minutes. Three were the deadlock; mixed-exec, which has no restart and sat in the `Stdout` call row 6 later showed spinning, was most probably the publication spin. A test-process dump cannot tell them apart ([logs/previous-engine-run-excerpt.log](logs/previous-engine-run-excerpt.log)). Reported at the time as "not a hang". |
| B | the sharing test alone, at `490c386592` | `--timeout=10m` | **Deadlocked.** Package timeout at 600 s, 13 m 12 s wall; the test parked in `Entries` after the restart for 9 minutes (`/tmp/b6/engine-single.log`, not committed). |
| C | the sharing test alone with the 30 s bounded read (`aecadc5261`) | not recorded | **Terminated by the coordinator** after 11 minutes with no result (`/tmp/b6/engine-single2.log`, not committed). |

Row 9 covers every case of the batch: 34 `TestSnapshotSharing*` tests and the three part tests (`TestPartLazyOperationPublishesOverObservedRepresentation`, `TestCommitReadyPartRejectsBrokenPreparation`, `TestPartReselectWatch`) in `dagql`, 3 in `core`, 4 in `engine/server`; none skips. 112 subtests skip in those packages, 111 with "operation not permitted": the pre-existing real-store tests (decision 2), **which is why no package run could see the publication spin**. Development-loop runs of single tests (each with `-timeout` 30 to 180 s) are not listed; rows 9 and 10 supersede them. Every correction's test was also run against the code before its fix and failed there; the commit messages say how.

## Pre-existing slop

1. **Every real-snapshot-store test skips unprivileged** (`engine/snapshots/testutil/store.go:74-92`). 111 subtests in these four packages produce no evidence in any permitted run, including the two decode tests the design wanted extended. Decision 4's G5 carries the fix to batch 7.
2. **The nested engines' logs never reach the test output.** A hang inside a nested dev engine is invisible from `--test-verbose`; the only way to see it was to expose the debug endpoint and fetch a dump from inside the test. `core/integration/engine_test.go:865` already does this for one test. A shared helper that dumps a nested engine's goroutines when a bounded call blocks would have saved two ten-minute runs here.
3. **Each `engine-dev test` invocation rebuilds the engine before the first test line** (the single test ran 1 m 27 s inside a 5 m 16 s invocation). Known. It is also what mis-sized ledger row 5's outer bound.
4. **A passing `engine-dev test --test-verbose` run prints no test output when its output is captured to a file**: only the summary and a trace URL. The verification time rule takes per-test durations and logged observations "from the verbose output", which on a pass exists only in the cloud trace (`dagger cloud logs <trace> --test <name>`). Whoever needs a passing run's `t.Logf` lines has to know that.
5. **`publishEvaluatedParts` and `demandPart` retry a reselect with no bound, no backoff and no progress condition** (batch 4). Any deterministic refusal answered with a reselect becomes a silent CPU spin and a demand that never returns; in this batch that happened once on an engine for seven minutes. A sound progress rule needs an audit of every reselect site and is a required item for the batch 7 commission (R12); this batch adds only the hard error for a broken preparation and the warning diagnostic.
6. **The privilege skips hid a defect that affects every engine.** The publication spin was reachable with sharing off, and the batch 4 tests that would have caught it skip without mount privileges. Decision 2's conversion is owed for this reason too.
7. `core/schema/foreign_module_context_test.go` is not gofmt-clean at the base, and `go vet ./engine/server` reports `session_attachables.go:211` (previous implementer's observation; untouched).

## Limits and open items

- The in-process cases use a fake snapshot manager: they prove ownership, ordering, holds and lock discipline, not bytes.
- **The preflight is still conservative for an encoded row in a completed form** (R7): batch 2's visitors are not form-aware, so such a row also reports the references of the operation it retains raw, and the walk follows them. That can only make a slot ineligible and enlarge the walk; it cannot admit an unsafe decode. Making the visitors form-aware changes every core family's visitor. I did not attempt it and say so rather than claim R7's sentence in full.
- The marked-decode test does not reach the two leader orders or the SDK-built ancestry (batch 7, with G4).
- A refused typed preparation leaves its receiver to a later trigger or an ordinary demand (R5, above).
- The deadlock's regression test wedges the graph lock when it fails, so on a regression it ends at the package `-timeout` with every stack printed rather than at its own bounded wait.
- `Triggers` and its companions now cover a union, a membership insertion with no union, a live import, a failed identity plan, a failed root validation, eager and lazy completion, a collected row and admission off. Still not forced: a publication rollback inside the indexing interval, and an attachment failure after the early publication flush. Both need fault injection that does not exist at this base, and both are one-pass retention behaviors, not ownership invariants.
- The engine runs used uncommitted instrumentation in `core/integration` (a watchdog that dumps the test process and every nested engine if the package is still running at 5 m 30 s, and the nested engines' debug endpoint). It is not part of the candidate.
