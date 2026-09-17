# Batch 6 implementation report: early snapshot sharing

Implementer, 17 September 2026. Commits 1 to 5 of the [continuation packet](../../implementation-continuation/b6-continuation/PACKET.md) section 4 are implemented on the batch 5 head, under the converged design at `6697a4b510` read through its vocabulary table, [decision 3](../../implementation-continuation/b6-continuation/DECISION-3-VERIFICATION-FORM.md), [decision 4](../../implementation-continuation/b6-continuation/DECISION-4-ENABLEMENT-AND-READINESS.md) and the designer's readiness note `26578890ce`.

| Identity | Commit |
| --- | --- |
| Branch | `sharing-implementer-implementation-7d93905c` |
| Base | `a26dc93750e42daf2de76678b0e54f51454cea33` |
| Implementation tip | `0a7c5c0c89127daef0240ce8394b0d11cf765c24` |
| Evidence tip | the commit that adds this report |
| Diff | `git diff a26dc93750 0a7c5c0c89 -- . ':!continuation-evidence'` |

## Commits

| Commit | Kind | What it does |
| --- | --- | --- |
| `a0ce9c123d` | evidence | Cherry-pick of the preserved readiness note `4b0e088d3c`, unchanged. |
| `3277121605` | evidence | Cherry-pick of the preserved rework plan `a25900bc04`, unchanged. |
| `bf0f5151cc` | production | Commit 1: receiver expiry at the sessionless constructor and at Commit; `partDonatedFacts` per-address donor validation replacing the whole-row capture and facts; the receiver's original structural admission recorded and rechecked. |
| `d342205f20` | production | Commit 2: `EnableSnapshotSharing` and the three-valued admission state; the per-E-interval notification collector and its union, membership, identity-teaching, live-import, publication, eager-completion and lazy-completion sites; the cohort queue, coalescing, the lazily created worker; the close step beside `closeRemoteCacheBridge`. |
| `d71c8ef9b1` | production | Commit 3: `probeAllParts` and unlocked selection; `Cache.SetPartPreparationContext`; `CoreSchemaBase.ForkForPersistedDecode`; `core.ContextWithPersistedDecodeDefaults` and `persistedDecodeDefaultDeps`; the `BackgroundDecode` audit flag and the decode preflight; `engine.WithSnapshotSharePreparation`, `CheckSnapshotSharePreparation`, `ErrSnapshotShareEvaluation`, the guards and the refusing provider; decision 4's engine-side enablement. |
| `14b1222e0d` | production | Commit 4: `prepareReadyPartFromBase` with `PrepareReadyPart` as its nil-base wrapper; `expectedRepresentation` and `expectedPredecessors`; Commit's predecessor-identity and expected-representation validation and overflow rejection; the pass's prepare, commit, member-release and Finish phases with their latches and receipt drain. |
| `0a7c5c0c89` | production + tests | Commit 5: the writers feeding the existing role, decode and checkpoint paths, the five production corrections the verification found, and the focused matrix in `dagql`, `core`, `engine/server` and one `core/integration` test. |
| `167630f0b7` | mixed | A first version of this report and its logs, which also swept in three later test changes: the nil-callback ineligibility case, the multi-part family's Service field and the core-base timing log. |
| `e509c41964` | production | Removes the report and logs again, leaving those three test changes standing alone, so the evidence tip below is droppable. Nothing was amended. |
| evidence tip | evidence | This report, the ledger and the logs. |

Every commit is signed off and carries no attribution trailer. No commit has been amended since the binding start; the two cherry-picked evidence commits predate it and are byte-identical to the originals. The evidence-only commits to drop before publication are `a0ce9c123d`, `3277121605` and the evidence tip; `167630f0b7` and `e509c41964` together contribute only the three test changes.

## Where the design was silent, and what I chose

1. **Per-address donor proof.** The design requires the donated address's facts rather than the donor's whole payload, without naming them. `partDonatedFacts` is the gate's output state for that full address (phase, installation and owning task), the offer owner attached there, whether an applied snapshot-owner link at that output path names the copied SnapshotID, and the donor's expiry. A sibling publication, an offer attached elsewhere and a payload-revision bump all leave every field unchanged. The applied-link requirement is the design's "applied role naming the same B-local SnapshotID": the code matches by RefKey and canonical output path rather than by role name, because `PartDescriptor` carries no role and the design says to use batch 4's shapes unchanged.
2. **The original Own-set proof** is the receiver's `requiredSessionResourcesGen`. That counter moves only when the stored set actually changes (`recomputeRequiredSessionResourcesLocked` compares first), so an earlier same-pass install that adds edges already inside Own(R) does not invalidate a later slot, while a real change does.
3. **Snapshot-sharing targets.** A donated address qualifies when its descriptor carries a snapshot identity. A metadata part never has one, so no core part name is consulted from DagQL, and legal absence and completed parts are excluded by the same rule.
4. **Worker context.** The base is the engine/cache lifetime plus the cache and the preparation marker. It carries no operation-lease provider: there is no cache-level source for one at this base, `withOperationLease` is a no-op without a provider, and `PinSnapshot` builds its own resource-pin context independently of any ambient lease. The engine's registered preparation callback is free to add one.
5. **Diagnostics.** A pass or slot outcome is a `slog` record plus a test hook. Sharing adds no fixture event kind: a sharing Commit emits the existing `installed-ready`, and until batch 7's `sharing` report group exists the absence of `selected-ready` is the distinguishing mark, as decision 4 adopted.
6. **`ShareIneligible`** is the sentinel `ErrSnapshotShareIneligible`, a skip that changes no row state and no lookup eligibility.
7. **The audited registry** is a `BackgroundDecode` field on `PersistedObjectFamily`, false by default, set in `core/persisted_families.go` for the families in the design's admitted-decoder table. Keeping the flag next to each registration is what makes a newly registered or unaudited decoder ineligible by default.
8. **The preflight** follows declared child references only. A recorded call's references and storage roles are descriptive, not decode requests. It checks the family of every object payload in each record, including inline payloads inside a list, holds each row while it reads it, and treats a reference back into the walk as already checked.
9. **Registration and enablement placement.** Decision 4 names the range "after `initLocalCacheState` returns and before `startRemoteCacheIntegration`". The call sits at the end of that range, immediately before the integration starts, where every other initialization that can fail has completed and nothing can dispatch a request yet.
10. **The core view** of the schema-only fork is the engine's own base version, `engine.BaseVersion(engine.NormalizeVersion(engine.Version))`. There is no client to take a view from, and the defaults factory uses the decoding server's view, so both agree by construction.
11. **The ordered prefix's expected envelope** is allocated during preparation and published unchanged by that slot's Commit, so a successor can expect an exact pointer rather than a revision number, which the design requires ("matching revision numbers alone never authorize a different installation").
12. **Coalesced counted operations.** A class union that combines two pending items keeps both counted operations on the surviving item; the worker retires the surplus outside E when it takes the item. Duplicate member holds dropped by that union are released through the ordinary unlocked path by the next queue operation.
13. **The class filter** is "at least one registered imported member", a graph-visible origin fact. Pending-part determination stays in the unlocked probe, so a conservatively queued class can yield an empty pass.
14. **Selection consults structural admission.** A donor batch 4's sessionless constructor would refuse is not selected, so an address does not spend its one attempt per pass on a slot that cannot be admitted.

## Deviations from the commission and the design

1. **Donor part-revision validation landed in commit 1, not commit 4.** It is the same work the packet lists under commit 4, but it belongs with the rest of the per-address donor proof the sessionless constructor records, and separating the record from its check would have left commit 1 storing facts nothing read.
2. **`PartProbe.OfferRev` is not populated, and `OutputRev` is populated only by the multi-address probe.** The packet lists "`OutputRev`/`OfferRev` population on the probe" as batch 6's work. `OutputRev` now carries the gate's per-address installation identity in `probeAllParts`. `OfferRev` cannot be filled by a probe at all: offer slots are guarded by the graph lock and the design requires probes to run outside it. The per-address offer identity is therefore recorded on the lease inside the constructor's E section, where it is validated again at Commit. `probePart`, the demand-side single-address probe, is unchanged.
3. **A typed receiver takes at most one slot per pass.** The typed prepared store computes the output revision it expects from the live value at preparation (`core/part_store.go`, `filePartStore.expected` and the Container equivalent), so a second ordered typed slot cannot name the revision its prefix will publish. Making it possible means changing batch 4's `PartStorePreparer` signature to accept an expected base, which the design says keeps its arity. The design's own §6 text says a typed raw Container should use the same ordered preparation, so this is a real departure: an imported Container that has already been decoded and is missing two parts fills one part per trigger instead of both in one pass. Encoded receivers, which is what an imported row is before any demand decodes it, take the full ordered sequence, and every §9.1 and §9.2 multi-part case is encoded. The options are (a) extend `PartStorePreparer` with an expected-base argument and update its implementers, or (b) this behavior. I chose (b) as the smaller change and am reporting it rather than deciding it permanently.
4. **`TestPartDecodePublication` does not exist** (design §9.1 line 346). The real decode-publication tests are `dagql/cache_part_decode_test.go`'s `TestPartDecodeBeforeExternalFinish` and `TestPartDecodeLosesToInstalledRevision`, plus batch 2's `TestValueTransferPersistenceDecodePublication`. Those three require a real snapshot store and skip unprivileged (see the slop section), so the shared decode contracts are covered by new in-process cases instead: `TestSnapshotSharingDecodeWhileFinishPaused` and `TestSnapshotSharingFailedFinishAndRetry`.
5. **`ServicesAndDecode` is delivered in part.** See the §9.2 mapping below.

## Ordinary behavior changes

1. **An engine that can receive imports builds its core schema base at startup** rather than on its first client, and a base-construction failure now fails `NewServer` instead of that first request. This is decision 4's accepted cost, and it applies only to an engine with `RemoteCacheIntegration` configured or the gated fixture's environment variable set. Every other engine registers nothing, builds nothing early and starts exactly as before; its notification hooks cost one flag check.
2. **An engine that imported earlier and restarts with no integration and no fixture variable** has its restored Imported rows but sharing stays off for them. They lose only early sharing: ordinary demand still follows local, offered chain, then the row's Lazy operation.
3. **Close and discard now close sharing admission and cancel the worker**, immediately after the remote-cache bridge detach and before the quiescence wait. On a cache that never enabled sharing this is a flag write and a return.
4. **`CommitReadyPart` gained validation**: the expected representation, each predecessor's installation identity, and payload and installation revision overflow. For a public single-demand preparation the expected representation is the observed one and the predecessor list is empty, so its behavior is unchanged.
5. **An encoded Commit publishes the envelope pointer its preparation allocated** instead of a copy made at commit time. The content is identical.
6. **A sessionless share's admission changed**: it now requires an applied owner link for the donated snapshot, rechecks per-address donor facts instead of whole-row facts, and refuses an expired receiver. No ordinary demand uses `sessionlessShare`, so foreground lookup, ranking and acquisition are untouched.
7. **The persisted Module decoder's default dependencies go through `persistedDecodeDefaultDeps`.** Outside a sharing preparation marker it is `query.DefaultDeps(ctx)` verbatim.
8. **Guarded boundaries return `ErrSnapshotShareEvaluation` under the marker.** An unmarked context reaches them unchanged; `PartContentSource.Available` stays unguarded because it requests nothing.

## The question decision 4 asked: can the worker wait behind anything GracefulStop holds?

**No.** `GracefulStop` takes `srv.gcmu` at `engine/server/server.go:875` and holds it, through session removal and the shutdown prune, across `srv.engineCache.CloseWithShutdownError` at `:908`. If the sharing worker could block on `gcmu`, Close would wait for a worker waiting on Close's caller. It cannot:

- `gcmu` is taken in exactly four places, all in `engine/server`: `PruneEngineLocalCacheEntries` (`gc.go:87`), `gc` (`gc.go:307`), `gcIfLocalCachePressure` (`gc.go:323`) and `GracefulStop`. The first is a client API selection, the second and third are the scheduler and the pressure monitor.
- The worker lives in `dagql` and calls `dagql`, `core` and `engine/snapshots`. None of those three packages depends on `engine/server`: `go list -deps ./dagql`, `./core` and `./engine/snapshots` do not contain `github.com/dagger/dagger/engine/server`. So no call the worker makes can reach a `gcmu` acquisition by import alone.
- The one piece of `engine/server` code the worker can run is the closure `initSnapshotSharing` registers (`engine/server/snapshot_sharing.go`). Its body takes `srv.coreSchemaBaseMu` through `getCoreSchemaBase`, forks the installed base under DagQL's install lock, and binds two context values. It takes no `gcmu` and no session lock.
- The worker's storage calls - `PinSnapshot`, `GetBySnapshotID`, `AttachLease`, `RemoveLease` and ref release - are `engine/snapshots` methods guarded by that package's own mutex and the lease manager. The shutdown prune uses the same manager but never waits on a cache operation, so an overlap is a bounded wait in one direction, not a cycle.

No test is therefore required by decision 4's "with a test if the answer is yes". The in-process half of the ordering is covered anyway: `TestSnapshotSharingShutdown` holds a pass, shows `Cache.Close` does not return while it is active, and shows that after the release Close returns, the queue is empty, every counted operation ended and admission cannot reopen.

## S3(a) costs

The design asked for four costs to be named.

1. **Preflight traversal before ordinary decode.** Only a typed preparation whose donated descriptor carries Service references pays it. It walks the exact rows that decode will load: one capture and one reference visit per row, no storage and no typed construction. Its size is the closure of declared child references under the donated services, and each row is visited once. A slot with no services, and every encoded receiver, does no traversal at all.
2. **Guard checks on unmarked paths.** One context-value lookup at each guarded boundary: `Cache.Evaluate`, `EvaluateParts`, `PartHost.Evaluate`, `RunNative`, `demandPart`, lazy operation preparation and run, `LazyState.Evaluate` and `EvaluateGroup`, `Service.Start`, `Services.Start`, `StartBindings`, `SchemaBuilder.Schema` and `TypeDefs`, `CoreSchemaBase.viewState`, `executableClientFromContext`, `PartContentSource.Provider` and `installChainPart`. Measured against the base on the same host, the four-package regression moved from 21 s (batch 5's measurement) to the durations in the ledger below, which are dominated by `core/schema`; no per-boundary cost is separable at that resolution.
3. **Static core-base construction ahead of admission.** Measured below.
4. **Audit maintenance.** `BackgroundDecode` is false by default, so a new persisted family, or a decoder that starts loading a reference it did not load before, makes a receiver that needs it ineligible until someone audits and marks it. That is the intended failure direction, and it is the standing obligation this batch adds: 33 families are marked today.

## Decision 3's §9.2 mapping: what has evidence, what is batch 7's

| §9.2 bullet | Evidence in batch 6 | Left to batch 7 |
| --- | --- | --- |
| Donor lifetime | `core/integration` `TestSharedHostDirectoryLifetime`: for the imported Host `directory` row, exactly one `installed-ready` and one `settled`, no `provider-read`, `lazy-enter` or `installed-chain`, its own SnapshotLinks entry naming the donor's exact snapshot, and after the existing restart no reset, still Imported with that link, and a read through the saved handle adds no part event. In process: `ReleaseThenExternalFinish` (every donor and member hold ended before Finish, balance restored) and `ReadinessAndExpiry` (eager, desired-only, expired and native controls). | `TestSharingDonorRestart`: ending L's session, dropping its persisted edge, real snapshot GC, L actually collected, and a restored-but-unopened donor over real bytes. |
| EncodedMultiPartPass | `PreparedSequence` (two parts) and its three-part control: all prepared before any commit, each publishing once with every earlier role present, no same-pass stale-copy refusal, one pin and one protection release per installed part. `IndependentParts`: a completed sibling is untouched and an absent part is never overwritten. | `TestSharingFinish` and the save-and-reopen read of both parts in `TestEncodedRestart`. Decision 4 confirmed the three-part control and the exact-Service-reference variant stay in process only. |
| Donor sibling and failure | `DonorReceivesSibling`: a row receives one part earlier in commit order and still donates its own unchanged part in the same pass, despite its whole payload revision moving. | G2: a failing-prefix schedule asserting zero transient pins after the pass, over real storage. |
| Installed checkpoint | In process: a failed owner synchronization keeps the installed output and its protection, and a later demand retries only the bookkeeping with no second Prepare or pin; `DecodeWhileFinishPaused`; `Shutdown`. Across a real restart: the donor-lifetime test's restored row, link and reset-free boot. | `TestEncodedRestart` (a real partial `AttachLease` fault and a pin across process exit) and `TestPendingOffersRestart`; G3. |
| Decoded service view | Partly. `engine/server` `TestSnapshotSharingPreparationContext`: the static base is built before admission, the registered callback yields a schema-only server carrying the engine root whose native Container, Service, Module, Directory and File classes resolve, the marker survives into it, and `Server.DefaultDeps`, `SchemaBuilder.Schema` and `TypeDefs` all refuse under it. `core` `TestPersistedDecodeDefaultDeps`: the pure factory returns a fresh builder for the decoding server's own view, refuses a server with another root, refuses a marked decode with no registered factory, and leaves the unmarked path on `Query.DefaultDeps`. `dagql` `TestSnapshotSharingTypedServiceReceiverNeedsRegistration`: with no registered callback, a typed receiver whose donated descriptor carries Service references is `ShareIneligible` before any shared decode attempt and takes no pin. | **Named gap.** The full `ServicesAndDecode` schedule is not delivered: a genuinely encoded Service plus encoded ModuleContext, ModuleSource, runtime, type and dependency ancestry, with the marked worker's Prepare leading the shared decode, paused, and an ordinary foreground joiner with a held ClientScope and the same core view resuming in both leader orders, and the counters for zero client creation, zero `buildTypeDefs`, zero provider delegate reads and zero service starts. Building that ancestry needs a real module fixture rather than a fake snapshot manager, which is what made it too large for this batch; the mechanism it would exercise is covered piecewise above. This belongs with batch 7's G4 row. |

Decision 3 said some §9.2 rows would have no passing evidence at convergence. The rows above name exactly which.

## Pre-existing slop I met

1. **Every real-snapshot-store test skips unprivileged, and that includes the two decode tests I was told to extend.** `engine/snapshots/testutil.NewStore` calls `requireNativeMount` (`engine/snapshots/testutil/store.go:74-92`), which probes a read-only bind mount and skips the whole store when it is not permitted (`:89`). In this environment every test built on it skips: `TestPartDecodeBeforeExternalFinish`, `TestPartDecodeLosesToInstalledRevision`, `TestPartReadyPreparationBoundaries` and the rest of the 31 files that use it. What it costs: the decode-publication tests the coordinator asked me to extend would have produced no evidence in any run I am permitted to make, so the shared decode contracts are covered by new in-process cases instead, and `boundaryPinManager`, which decision 3 named as an existing fake, is a wrapper around a real manager and is unusable unprivileged. I added a mount-free fake in the `dagql` test package rather than touching the shared helper. Decision 4's G5 already carries the fix to batch 7; the probe is for the export path, and sharing uses only pin, open, `AttachLease` and release.
2. **Two files fail `gofmt`/`go vet` at the base, untouched by this batch:** `core/schema/foreign_module_context_test.go` is not gofmt-clean, and `go vet ./engine/server` reports `session_attachables.go:211` discarding the cancel function from `context.WithTimeoutCause`. Neither is mine; both cost a moment of noise on every formatting and vet check.

## Limits

- The in-process cases use a fake snapshot manager: they prove ownership, ordering, holds and lock discipline, not bytes. No test in this batch reads or writes a real snapshot except through the one `core/integration` test and the two engine tests that were already there.
- `Triggers` covers the union, a membership insertion with no union, live import through the publication flush, eager completion, lazy completion and the admission-off case. It does not force a publication rollback inside the initial indexing interval, nor an unlocked attachment failure after the early flush; both need a fault injection that does not exist at this base, and both are one-pass retention behaviours rather than ownership invariants.
- `CompletionRegistrationRace` shows the waiter wake happening while the notification is blocked on the graph lock, and that a collected row is never resurrected. It does not exercise a real concurrent collection between the wake and the hook.
- Scan latency, prepared-view memory and the added receiver retention remain unmeasured, as the design says.

### Measured cost of the static core base

`TestSnapshotSharingPreparationContext` logs it: five runs gave 15.96 ms, 12.48 ms, 11.01 ms, 13.73 ms and 13.07 ms on this host, so about 13 ms of engine startup, once, on an engine that can receive imports. That is the whole of decision 4's first behavior change in wall time; the second is that the same 13 ms of work now fails `NewServer` rather than a first request if it fails at all.

## Verification ledger

Every invocation carries an explicit timeout. Selections are grouped one invocation per package per runner variant, and the real-engine tests are one invocation.

| # | Command | Timeout | Result | Duration |
| --- | --- | --- | --- | --- |
| 1 | `go build ./dagql ./core ./core/schema ./engine/server` at `3277121605` | harness 600 s | pass, exit 0 | 38.2 s (cold) |
| 2 | `go test ./dagql ./core ./core/schema ./engine/server -timeout 300s -count=1 -v` at `0a7c5c0c89` plus this report's test additions | `-timeout 300s`, harness 420 s | pass | dagql 6.09 s, core 5.51 s, core/schema 11.33 s, engine/server 2.84 s; 17 s wall warm ([logs/packages.log](logs/packages.log)) |
| 3 | `go test -race ./dagql ./core ./engine/server -run 'TestSnapshotSharing\|TestPartSessionless\|TestReadyPartReceipt\|TestPartReadyRevalidation\|TestPersistedDecodeDefaultDeps\|TestSnapshotSharePreparationCoreGuards' -timeout 180s -count=1 -v` | `-timeout 180s`, harness 420 s | pass | dagql 3.18 s, core 1.61 s, engine/server 1.49 s; 62 s wall including the race build ([logs/race.log](logs/race.log)) |
| 4 | `dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/(TestPartMixedExecOutputs\|TestSchemaRecovery\|TestSharedHostDirectoryLifetime)$' --test-verbose --timeout=10m` at `0a7c5c0c89` | `--timeout=10m`, harness 900 s | **failed: the package hit its 10-minute test timeout with all four selected subtests still running** | 13 m 55 s wall ([logs/engine-run.log](logs/engine-run.log)) |

Earlier package and race runs at commits 1 to 4 also passed; they are superseded by runs 2 and 3 at the final candidate and are not listed separately.

Run 2 covers the whole of `dagql`, `core`, `core/schema` and `engine/server`, which includes every new case: 22 `TestSnapshotSharing*` tests in `dagql`, `TestPersistedDecodeDefaultDeps` and `TestSnapshotSharePreparationCoreGuards` in `core`, and three in `engine/server`. 72 subtests skip in those four packages, all of them pre-existing real-snapshot-store tests (see the slop section); none of them is new.

### Run 4: what it showed and what it did not

It is not a hang. Every selected test was progressing when the package timeout fired:

- `TestSharedHostDirectoryLifetime` reached `remote_cache_sharing_test.go:163`, its last step, having already passed every donor-lifetime assertion **and** the restart ones. Its recorded observation is the load-bearing one:
  `shared host directory row=4231 selected-ready-before-install=false counts=map[installed-ready:1 settled:1]`
  For the imported Host `directory` row: exactly one `installed-ready`, exactly one `settled`, and no `provider-read`, `lazy-enter`, `installed-chain` or `selected-ready` at all. The absence of `selected-ready` is decision 4's distinguishing mark, so on a real engine with real bytes **the early sharing pass installed that part, not a demand**. It then passed the restart assertions: no persistence reset, the row still Imported, and its own owner link naming the donor's exact snapshot after reopening. What it did not reach: the final read through the saved handle and the assertion that the read adds no part event.
- `TestSchemaRecovery/before` and `/after` were at 8 m 58 s, past their restarts at `remote_cache_transfer_test.go:398`, with their route counters already logged (`installed-chain:1 installed-ready:49 owner-sync:55 provider-read:1 settled:50` for both orders) and reset-free restarts (`RemovedPersistedRootCount:0`). `TestSchemaRecovery/foreign_context`, a third subtest the packet's estimate did not account for, also ran.
- `TestPartMixedExecOutputs` was inside an ordinary `Stdout` call at `remote_cache_mixed_exec_test.go:105`.

The cause is cost, not correctness: three heavyweight tests running in parallel, each driving one or two nested dev engines and at least one restart, need more than ten minutes of test time on this host. The packet's estimate of about four minutes of test time came from a two-test selection.

**This is the budget report the brief asks for, and the one open item.** The engine regression is therefore not green, and I have not re-run it: a re-run needs a timeout above the 10-minute budget, which is exactly the case I must report before running.

- **Proposed:** re-run the same invocation unchanged except `--timeout=20m`, with an outer bound of 25 minutes. Expected cost about 20 to 22 minutes wall, one engine build. The evidence says this passes: every test was making progress and the new one was one call from the end.
- **Alternative:** keep `--timeout=10m` and drop `TestSharedHostDirectoryLifetime` from the selection. That stays inside the budget and probably lets the two original tests finish, but it leaves decision 4's required integration test with no green result, keeping only the log evidence above.

I recommend the first: the observation this batch exists to produce is already in the log, and one longer run turns it into a recorded pass.

## Open questions

1. **The engine regression's timeout**, above. Nothing else in the batch depends on the answer.
2. **Ordered preparation for a typed receiver** (deviation 3). If the council wants the design's §6 sentence honored literally, `PartStorePreparer` gains an expected-base argument and its implementers change; I did not do that on my own because the design also says batch 4's public arities stay as they are.
3. **`PartProbe.OfferRev`** stays unpopulated (deviation 2). If a reviewer wants the field filled, the only correct filler is an E-holding caller, which is where the equivalent fact already lives.
