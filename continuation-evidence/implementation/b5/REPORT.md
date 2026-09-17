# Batch 5 implementation report: offers before execution and bounded renewal

Implementer, 17 September 2026. Steps 1 to 5 of the commission (`git show 64a0137a5f:continuation-evidence/implementation-commissions/BATCH5-OFFERS-IMPL.md`) are implemented on the reworked batch 4 head. The corrected verification set passed: the package, race and engine runs are below. It replaces the first set, which ran in a form the Human has since retired. The first set's results are kept, labeled, under `retired/`.

| Identity | Commit |
| --- | --- |
| Branch | `offers-implementer-implementation-fb886a62` |
| Base | `fd9cfd55a98b176bf82cf84ee1b4c3ae1257fde5` |
| Implementation tip | `e1adfa269457f8253aee6cb217e700876b8ef754` |
| Evidence tip | the commit that adds this report |
| Diff | `git diff fd9cfd55a9 e1adfa2694 -- . ':!continuation-evidence'` |
| Superseded evidence commit | `23b2787ac8` (the first report, committed before the correction arrived; this commit replaces its verification section) |

Governing revisions: the design at `49b268a354`, read through its vocabulary table; the packet at `2daf4b4d9e` and `64a0137a5f`; decision 1 as answered in the binding start; the designer's readiness points a to e (`62809f73bc`); the verification time rule, the test timeout rule and the time-and-slop rule (`b3d805de10`).

## Commits

| Commit | Kind | What it does |
| --- | --- | --- |
| `d49e309bcd` | evidence | Cherry-pick of the readiness note `22d9c17296`, unchanged. |
| `1c21c17463` | production | Port of `9a3cddf0ce` (step 1, `OfferParts`) with the proposal's §6 names. The message states the provenance. |
| `f7af21e6f1` | evidence | Cherry-pick of the rework plan `3b0acaee57`, unchanged. |
| `136db370b8` | production | Step 2: renewal episodes in `PartDemandState`; the bounded `RemoteCacheBridge` mailbox with Take, Reply, cancellation and detach. |
| `d5fef064a3` | production | Step 3: the concrete `PartContentSource`, its constructor and accessor, `Available(offer, time.Time)`, the provider with renewal, `renewalRetryStatuses`, the idle bound and `ChainContentError` stages. Also the decision 1 override and the `Available` signature change at existing implementers. |
| `e24aa3d0b9` | production | Step 4, part 1: `AttachRemoteCacheBridge`/`DetachRemoteCacheBridge`; cache close detaches before drain; `NewServerOpts.RemoteCacheIntegration`, the adapter, Run and Stop. |
| `f3934542da` | production | Step 4, part 2, in its own commit (the Human's ruling): `Cache.CloseWithShutdownError` and the `GracefulStop` returns. The message states that this un-drops the pre-existing error accumulator for every engine. |
| `14c922ae49` | tests | Step 4, part 3: pending-offer restart and A→B→C forwarding; nil-config cost. No production code. |
| `b723b1e105` | tests | Step 5: the remaining focused matrix. |
| `2b564d9c9c` | docs | `internal-docs/cache_persistence.md`: the integration stop in the graceful-shutdown sequence. |
| `23b2787ac8` | evidence | First report (superseded). |
| `e1adfa2694` | test fix | `TestRemoteCacheTransferSuite` runs its tests in parallel again (coordinator item A). `Middleware()[1:]` dropped `testctx.WithParallel()`; commit `12e20a25d0` added it on 16 September with no recorded reason. Each test already uses its own volumes, state keys and temp dirs. |
| evidence tip | evidence | This corrected report, the ledger data and the logs. |

The evidence commits `d49e309bcd`, `f7af21e6f1`, `23b2787ac8` and the evidence tip are to be dropped before publication. Every commit is signed off and has no attribution trailer. Before any check or report, the port commit was amended once: `fbf61b8d78` had captured the pre-rename index. The coordinator confirmed that amend. `1c21c17463` and every later commit are unamended.

## Choices where the design is silent

1. **Decision 1 form (bound by designer point a).** The override interface is named `PartContentOverride`. It carries both `Available(PersistedPartOffer, time.Time)` and `Provider`. It is a nil-by-default atomic field of the `PartContentSource` struct, consulted first inside the struct's own `Available` and `Provider`. Ranking (`scanPartSources`) and `installChainPart` each keep one call through the struct. `Cache.SetPartContentSource` keeps its name and installs or clears the override. Per the coordinator's answer 1, the override is **not** gated on `EnableTransferFixtureParts`. The doc comment says only tests and the environment-gated fixture set it. `core/schema/remote_cache_fixture*.go` changed only for the signature.
2. **Nil-safe source (designer point c).** `(*PartContentSource)(nil)` behaves like a source built with a nil transport: default transport, no bridge, no override. A zero-value cache needs no source object. `SetPartContentSource` requires a cache from `NewCache`; no zero-value cache calls it.
3. **Construction commit.** The step 3 commit already constructs the source in `NewCache`, rather than step 4 as the commission lists. The override now lives on the struct, so without construction the existing override tests could not run at that commit. The initial cache literal covers the empty-dbPath return. The literal rebuilt after a failed import reuses the same pointer.
4. **Default transport.** A package-level `http.DefaultTransport` clone with `DisableCompression`, created on first content access and shared by caches whose transport is nil. In-package tests inject their `RoundTripper` by setting the field right after `NewCache`, before first use; that is the construction point.
5. **Mutex M before the struct existed.** The step 2 mailbox holds a pointer to its owner's mutex. From step 3 on, that is the source's `mu`.
6. **Controlled clocks.** Production code uses `time`; tests use `testing/synctest`, as `dagql/cache_lazy_retry_test.go` already does. No clock field was added.
7. **Episode key and deadline.** The key is the JSON of the demand's target address, recorded in `PartDemandState` by `demandPart`, plus a domain-separated digest of the ordered blob digests. The reply-correlation fingerprint is a domain-separated digest of the JSON-encoded ordered `ExportLayer` records. The deadline is set at claim time, immediately before the first enqueue attempt: `min(t0+2s, ctx deadline)`. A waiter on an existing episode waits at most until that deadline. Claim, supply and unavailable never change `PartDemandState.revision` (designer point d). `exhaust` marks a supplied episode `exhausted` and bumps the revision once, as before.
8. **Renewal triggers and bound.** An absent URL, an empty URL or an expired address triggers renewal in `ReaderAt`. A retry status triggers it when the response is opened. A blob whose current address came from the episode never renews again. A reply that repeats the address that just failed, or has no usable address for the blob, is unavailable. A non-HTTP(S) or relative URL is unavailable and is never opened or renewed.
9. **Reader.** The first `ReadAt` opens the response, so `ReaderAt` makes no request. Every open sends `Range: bytes=<off>-<size-1>`. A 206 must carry exactly that `Content-Range`. A 200 is the whole object: `Content-Length`, when present, must equal the size, and the prefix is read into `io.Discard` without buffering the object. Contiguous reads reuse the response. `Close` cancels the reader context and closes the current body without taking the cursor mutex.
10. **Idle accounting.** The timer is armed only around a blocked network operation: the request, including redirects and headers, or a body read. It fires after 30 seconds minus the idle time already spent on that response. Positive bytes reset the spent time. Expiry cancels that request with a typed idle cause.
11. **Mailbox details.** Sequences start at 1 per attachment. The requester owns the single deadline timer and completes an expired or canceled exchange, compacting the FIFO under M. Take skips entries already past their deadline and returns a copied layer slice. A valid reply has only known digests; a negative reply has none. A positive reply may omit the needed blob, in which case the provider reports unavailable.
12. **Server placement.** The integration starts at the end of `NewServer`: after local cache initialization, before the server serves anything, and after every other initialization step that can fail, so a failed `NewServer` never leaves a running callback. `GracefulStop` stops it before session teardown and outside `gcmu` (designer point e). The adapter wraps the bridge's closed error as `ErrRemoteCacheAdapterClosed`. `Stop` reports only a failed join. A Run error not caused by cancellation is logged.
13. **Duplicate cause text** (designer point e). `adapterStopErr` appears both inside the cache-close error and in the explicit join. This is deliberate.
14. **Test placement.** The design row "engine/snapshots real chain controls" is `dagql/TestRenewalChainControls`. It uses real `engine/snapshots` stores and `ImportChain`, but the provider lives in dagql. The `engine/snapshots` package is unchanged.
15. **Batch 4 fixed-provider tests.** Three tests changed where the design changed behavior: contiguous reads now share one response, and a provider serves only blobs of its offered chain (the cancellation test gained its layer).

## Ordinary behavior changes

- **`GracefulStop` returns its collected errors for every engine** (`f3934542da`). Errors from session teardown, client DB close, shutdown prune, cache close and executor option close were previously dropped. `cmd/engine` only logs the returned error.
- **Fixed-address downloads without an integration.** The provider now uses the default transport clone with transparent decompression disabled, instead of `http.DefaultClient`. It keeps one response for contiguous reads instead of one bounded request per read. It accepts only HTTP(S) addresses, checks descriptors against the chain and applies the 30-second idle bound. Ranking treats non-HTTP or relative URLs as unavailable; previously any non-empty unexpired URL counted.
- **Cache close** detaches an attached bridge under M before draining. With no integration, nothing is attached and nothing changes.
- **A failed integration stop** leaves the checkpoint dirty, so the next start wipes DagQL persistence. The commission names this as an accepted cost.
- **`NewServer`** rejects a non-nil `RemoteCacheIntegration` without `Run`. The option is new and nil by default.
- **No format version change.** The base's cut stands (schema 21, envelope 5, bundle 2). Offers remain separate owners. Installed-output and Lazy-operation input dependencies stay direct edges.

## Design §7 rows and their tests

| Design row | Tests |
| --- | --- |
| `dagql/TestOfferPartsBeforeStart` | `dagql`: `TestOfferPartsBeforeStart`, `TestOfferPartsInvalidatesSourceCheck`, `TestOfferPartsInlineAndReferencedRows`, `TestOfferPartsClosesNativeAdmission`; `core`: `TestOfferPartsNativeAdmission` (real pending native File: accepted before start installs with zero body entries and no read at acceptance; ExecutionStarted while the body runs, and the body finishes). Imported pending receivers: `TestOfferPendingRestartAndForward`. |
| `dagql/TestOfferPartsOwnership` | `TestOfferPartsOwnership` (dedup, same-owner refresh, cycle, full-length dispositions), `TestOfferPartsPreparationWindow` (split-E protection; refused new offer, same-owner refresh and replacement each release once), `TestOfferPartsAcceptedCleanupFailure`, `TestOfferPartsResourcesAndSettlement` (old acquisition survives replacement); batch 4's `TestPartAdmittedChainLifetime` (pruning). |
| `dagql/TestOfferSettlementReplacement` | `TestOfferSettlementReplacement` (replacement during the winning acquisition retired; AlreadyComplete after Commit; failed sync keeps output and drops the O1 hold, with and without the back-reference; bookkeeping-only retry settles once); `TestOfferPartsResourcesAndSettlement`; batch 4's `TestPartSettlementRetiresReplacement`. |
| `dagql/TestOfferResourcesDoNotGateLookup` | `TestOfferResourcesDoNotGateLookup` (lookup and requirement generation unchanged; unauthorized demand runs the next route; authorized demand installs direct references and requirements propagate); batch 4's `TestPartSessionlessOwnSubset/offer-only` (sharing subset cannot use offer-only permission). |
| `dagql/TestRenewalMailbox` | `TestRenewalMailbox`, `TestRemoteCacheBridgeAttachment`. |
| `dagql/TestRenewalExhaustion` | `TestRenewalExhaustion`, `TestRenewalEpisodeSet`, `TestRenewalClaimKeepsSourceCheck`; multi-layer cases in `TestRenewalChainControls`. |
| `engine/snapshots` real chain controls | `dagql/TestRenewalChainControls` (real stores and `ImportChain`: local prefix, renewal, all-local, Info, each retry status on both attempts, 500, transport error, non-HTTP, truncation, wrong digest, stalled stream, writer and lease faults, no bridge, no key, negative, expired and partial replies; every response closed). Classification contracts: the unchanged `engine/snapshots` tests. |
| Idle-reader checks | `TestPartContentIdleReader` (header stall, blocked read, progress reset, time outside reads not counted, Close without the cursor mutex, cancellation, Range behavior and a bad 206); `TestPartFixedProvider*`. |
| Pending-offer restart/forward | `TestOfferPendingRestartAndForward`; batch 2/4's `core/TestValueTransferPersistenceFinalOfferRestart` (redundant slot retired after lease restoration). |
| Server lifetime | `TestRemoteCacheUnusedCost`, `TestPartUnusedHostAllocatesNoGate`, `TestRemoteCacheIntegrationConfig`, `TestRemoteCacheBridgeAttachment`, `TestRemoteCacheAdapterLifetime`, `TestRenewalShutdownDrainsOwnership` (delivered renewal and active reader), `TestRemoteCacheGracefulStop` (noncooperative Run: stop error returned, a later Close cannot mark clean, restart is unclean; cooperative control closes clean; the accumulator is returned), `TestCacheCloseWithShutdownError` (seeded cause with a live context, no clean marker). Old replies: `TestOfferPendingRestartAndForward`, `TestRemoteCacheBridgeAttachment`. |
| Native pipeline | Batch 7. The existing gated suite ran as a regression (below). |

## Verification ledger (corrected form)

This follows the time-and-slop rule (`git show b3d805de10:continuation-evidence/direction/TIME-AND-SLOP-RULE.md`), the test timeout rule and the verification time rule. Nothing ran as root, under `sudo`, under `unshare` or in a mount namespace. `-race` covered only the batch's in-process concurrency tests. All commands ran from the worktree root at `e1adfa2694`. The three Go invocations ran concurrently from 03:01:02 UTC, inside a shell bounded by `timeout 300s`, and the set took **21.4 s** of wall time. Per-test results and durations are in [package-results.json](package-results.json), every skip with its reason line is in [skipped.json](skipped.json), and the full output is in [logs](logs/).

| Invocation | Command | Timeout | Exit | Wall | Go time | Results |
| --- | --- | --- | --- | --- | --- | --- |
| packages | `go test ./dagql ./core ./core/schema ./engine/server -count=1 -v -timeout=120s` | 120 s per package binary | 0 (four `ok` lines, no `FAIL`) | ≤21.4 s (see note) | dagql 8.613 s; core 7.037 s; core/schema 15.068 s; engine/server 6.287 s | 2593 pass, 0 fail, 112 skip (40 top-level tests skipped entirely or in part) |
| dagql-race | `go test -race ./dagql '-run=^Test(Offer\|Renewal\|RemoteCache\|CacheCloseWithShutdownError)' -count=1 -v -timeout=120s` | 120 s | 0 | 13.7 s | 7.725 s | 33 pass, 0 fail, 10 skip, 0 races |
| server-race | `go test -race ./engine/server -count=1 -v -timeout=60s` | 60 s | 0 | 14.0 s | 7.230 s | 281 pass, 0 fail, 0 skip, 0 races |

In the table, `\|` stands for `|` inside the regular expression. Note on the packages row: my wrapper script named that invocation `packages`, so the set's own summary overwrote its status file. Its exit status is therefore taken from the log (four `ok` lines and no `FAIL`), and its wall time is bounded by the set's 21.4 s; the other two invocations finished at 13.7 s and 14.0 s.

**Skips.** All 121 mount skips have the same reason: `real native snapshot transfer requires read-only bind mount privileges: … operation not permitted`, from `engine/snapshots/testutil.NewStore`. By package, the plain run skipped 65 in core, 25 in dagql and 22 in core/schema. One further skip is `dagql/TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases` ("TODO: re-enable after last-waiter canceled cleanup semantics are decided"), which skips unconditionally at the base too. The race run's 10 skips are all mount skips in this batch's tests. None is counted as a pass.

**This batch's tests that skip unprivileged** (a finding against my own work under rule 6):

| Test | Skipped subtests | Design row it carries |
| --- | ---: | --- |
| `dagql/TestRenewalChainControls` | whole test | real chain controls |
| `dagql/TestRenewalExhaustion` | whole test | renewal exhaustion |
| `dagql/TestOfferSettlementReplacement` | 4 of 4 | settlement and replacement |
| `dagql/TestOfferResourcesDoNotGateLookup` | whole test | offer-only resources |
| `dagql/TestRenewalShutdownDrainsOwnership` | whole test | server lifetime, drain |
| `dagql/TestOfferPendingRestartAndForward` | whole test | restart and forward |
| `dagql/TestRemoteCacheUnusedCost` | whole test | nil-config cost |
| `core/TestOfferPartsNativeAdmission` | 2 of 2 | before-start admission on a real value |

These tests are unit tests that need real mounts only because they call `ImportChain` through `testutil.NewStore`. The rows they carry now have no passing evidence in the permitted form. What they last showed is in the retired privileged run below. The tests that do run unprivileged are:
- the ported `TestOfferParts*` tests, `TestOfferPartsPreparationWindow`, `TestCacheCloseWithShutdownError`, `TestRenewalMailbox`, `TestRenewalEpisodeSet`, `TestRenewalClaimKeepsSourceCheck`, `TestRemoteCacheBridgeAttachment`, `TestPartContentSourceAvailability`, `TestPartContentIdleReader`, `TestPartFixedProvider*` and `TestPartUnusedHostAllocatesNoGate`;
- in engine/server, `TestRemoteCacheIntegrationConfig`, `TestRemoteCacheAdapterLifetime` and `TestRemoteCacheGracefulStop`.

### Engine regression

```sh
timeout 540s dagger api call engine-dev test --pkg ./core/integration '--run=^TestRemoteCacheTransferSuite$/^TestPartMixedExecOutputs$' --timeout=5m --test-verbose
```

Test timeout 5 minutes. The 540 s outer bound covers the measured build and setup (about 4 minutes in the first run) plus the test timeout. There was no diagnostic, env file or runner overlay; the working tree was clean at `e1adfa2694`. The run started at 03:00:57 UTC, concurrently with the Go set, and gave **exit 0 in 300.7 s wall** (budget about 5 minutes including the build). Of that, the workspace load took 32.7 s, the dev engine module 26.2 s and the `.test` step 4m1s, most of it the engine build and start. Trace `70d079daa2e707780e3fd22b64efe282`. Results come from `dagger trace --test` ([suite](logs/engine-trace-suite.log), [test](logs/engine-trace-TestPartMixedExecOutputs.log)); the CLI output is [engine-cli.log](logs/engine-cli.log); machine-readable form in [engine-results.json](engine-results.json).

| Test | Result | Duration |
| --- | --- | ---: |
| `TestRemoteCacheTransferSuite` | PASS | 1m10s |
| `TestRemoteCacheTransferSuite/TestPartMixedExecOutputs` | PASS | 1m10s |

This test covers the batch's two engine-visible changes. The gated fixture's content source now goes through `PartContentOverride` with the `time.Time` signature, and the engines' shutdown path now goes through the integration stop (nil here) and `CloseWithShutdownError`. The dev engine service exited normally in this run.

### Earlier results under a retired practice

On 17 September before the correction, at `2b564d9c9c`, the first set ran whole packages as root under `sudo -n … unshare --mount --propagation private`, and `-race` over broad selections. All seven invocations exited 0 in 117.2 s of wall time, and every real-store test above passed. The single engine invocation ran the whole transfer suite plus the opt-in diagnostic with its runner overlay: exit 0 in 818.4 s, trace `e2b788da678c718c1e0f987488bc99c7`, with the suite's four tests running serially because of the line fixed in `e1adfa2694`. The Human has retired both forms. The results are kept only as evidence of what those tests showed, in [retired/privileged-packages](retired/privileged-packages/) and [retired/full-suite-engine](retired/full-suite-engine/). Per-test durations for this batch's real-store tests in that run, plain / race:

| Test | Plain | Race |
| --- | ---: | ---: |
| `dagql/TestRenewalChainControls` | 3.60 s | 4.99 s |
| `dagql/TestRenewalExhaustion` | 1.03 s | 4.50 s |
| `dagql/TestOfferSettlementReplacement` | 1.47 s | 3.94 s |
| `dagql/TestOfferResourcesDoNotGateLookup` | 0.69 s | 0.47 s |
| `dagql/TestRenewalShutdownDrainsOwnership` | 0.31 s | 0.58 s |
| `dagql/TestOfferPendingRestartAndForward` | 0.53 s | 0.94 s |
| `dagql/TestRemoteCacheUnusedCost` | 0.41 s | 1.00 s |
| `core/TestOfferPartsNativeAdmission` | 0.29 s | 0.97 s |

That run's observations still stand as observations: the cold test's `CacheProbe.report(seed: "different")` span recorded "6x part sources changed; reselect" on a call that succeeded, and the dev engine service showed ERROR at teardown.

## Real-store test files (coordinator item C)

These are the files whose tests call `engine/snapshots/testutil.NewStore` (directly or through a fixture), which skips without read-only bind-mount privileges. For the files in the packages run, the counts are the skipped top-level tests, with skipped subtests in parentheses, taken from [skipped.json](skipped.json). Columns 4 to 6 come from reading the test source and the existing integration tests; I have not run a comparison. "Batch 7 N" means the native gated-fixture tests designed in `12e776930d:hack/designs/remote-cache/focused/07-integrated-verification.md` §5. That design also has a "U" tier of these same in-process real-store peers, run with the privileged runner (its §6), and the new rule forbids that runner.

| File | Origin | Tests (subtests) | What it proves that nothing else does | Existing core/integration coverage | Batch 7 N coverage |
| --- | --- | --- | --- | --- | --- |
| `core/builtin_lazy_test.go` | base | 1 | Builtin Container Lazy evaluation installs a real snapshot in process | Partial: `TestDiskPersistenceAcrossRestart/lazy_values_survive_restart` (pending builtin evaluates after restart) | Cold builtin route in `TestPipeline/Cold` |
| `core/container_mount_lazy_test.go` | base | 1 (2) | Directory and File mount Lazy representations over real snapshots | Partial: `…/container_parts_preserve_mutations_and_unopened_snapshots` | `TestHostInputs` (mount aliases) |
| `core/git_lazy_test.go` | base | 3 | Git bundle, local and remote Lazy operations evaluate into real snapshots | GitSuite (`TestGitBundle*`, `TestGit*`) and `…/git_repository_and_ref_survive_restart` exercise the public paths, not the saved operation directly | `TestGitTrees` |
| `core/http_lazy_test.go` | base | 4 | HTTP chain avoids the operation; local-body failures, ownership and pin/lock cost | HTTPSuite for public behavior; `…/lazy_values_survive_restart` for restart | `TestHTTPRestore` |
| `core/lazy_completion_test.go` | base | 1 | Evaluated filesystem clones keep correct snapshot ownership | None found | None named |
| `core/lazy_operation_execution_test.go` | base | 6 | HTTP Lazy evaluate, writer modes, cleanup exits, concurrent demand, state/capture race and stateless isolation, with real stores | HTTPSuite (public paths only) | `TestHTTPRestore` (outcomes, writer layouts) |
| `core/mount_lazy_ownership_test.go` | base | 1 (4) | Detached mount Lazy ownership with injected failures | None found | None named |
| `core/part_acquisition_test.go` | base | 4 (11) | Root acquisition routes with fault injection (sync retry, pin-release retry, fallback), mixed Container restart, mount receiver roles, private whole builtin | `RemoteCacheTransferSuite/TestPartMixedExecOutputs` (mixed exec); schema recovery (chain route) | `TestPipeline/FailedChain`, output/task completion row |
| `core/part_delegation_mount_test.go` | base | 1 (2) | Delegated mount role mismatches are refused | None found | Host/mounts row (partial) |
| `core/part_delegation_test.go` | base | 1 (9) | Parent-part delegation modes, including sync retry and native restart at several depths | Cold schema recovery asserts delegation (`assertColdPartDelegation`) | `TestSchemaRecoveryCold` (partial) |
| `core/part_filesystem_race_test.go` | base | 1 (2) | Filesystem publication role races | None found | None named |
| `core/part_inline_test.go` | base | 1 (3) | Inline-address acquisition, sequential, concurrent and shared-snapshot sync retry | None found | Host/mounts row (full addresses; partial) |
| `core/part_offer_admission_test.go` | **batch 5** | 1 (2) | Offer accepted before a real body starts installs with zero body entries; offer refused while the body runs | None | `TestOffers/BeforeStart`, `/Running` |
| `core/part_publication_race_test.go` | base | 1 | Typed publication role race | None found | None named |
| `core/part_scope_boot_test.go` | base | 3 (5) | Boot scan failure, empty boot and root rekey cost with scoped links | `…/unclean_shutdown_discards_local_cache_state_and_recovers` (reset path only) | Encoded restart row (partial) |
| `core/part_whole_restart_test.go` | base | 3 | Mixed whole-Lazy restart, native pending Container recipe requirement, selective pending image metadata | `…/container_parts_preserve_mutations_and_unopened_snapshots`, `…/lazy_values_survive_restart` (partial) | `TestEncodedRestart` (partial) |
| `core/schema/container_lazy_test.go` | base | 1 (5) | Builtin metadata selectors against real snapshots | None found | None named |
| `core/schema/directory_scratch_test.go` | base | 3 (11) | Scratch Directory acquisition modes (cold, warm, restart, manager failure and cancel, prepare failure, sync retry), Lazy operation and native reopen | Cold schema recovery asserts scratch acquisition (`assertScratchAcquisition`) | `TestSchemaRecoveryCold` (partial) |
| `core/schema/http_lazy_test.go` | base | 2 (4) | Pending internal HTTP hits (absent, advanced, changed body) and the resolved call | HTTPSuite (`TestHTTPETag`, `TestHTTPUsedInCache`; public paths) | `TestHTTPRestore` |
| `core/schema/lazy_stored_results_test.go` | base | 1 | Stored results without backing bytes | Warm schema recovery (stored-result acceptance, per the rework report) | `TestSchemaRecovery` |
| `core/schema/query_lazy_test.go` | base | 1 | Schema File Lazy evaluation | `…/lazy_values_survive_restart` (`__schemaJSONFile`) | None named |
| `core/snapshot_transfer_test.go` | base | 1 | Typed adoption of an imported snapshot and restart | Schema recovery (transfer and restart) | `TestPendingOffersRestart`, encoded restart row |
| `core/value_transfer_chain_test.go` | base | 3 (4) | Selected chain export and import, including the Container mount opening only the selected snapshot, and Git trees on both backends | Schema recovery (one selected chain) | `TestGitTrees`, `TestHostInputs` |
| `core/value_transfer_restart_test.go` | base | 1 | Redundant offer retired only after lease restoration at restart | None found | `TestPendingOffersRestart` |
| `dagql/cache_offer_matrix_test.go` | **batch 5** | 3 (6) | Settlement against replacement, commit and failed sync; offer-only resources through an actual install; drain with a delivered renewal and an active reader | None | `TestOffers/Replacement`, `/Resources`; restart/shutdown row |
| `dagql/cache_offer_restart_test.go` | **batch 5** | 2 | Live offer through A→B→restart→C with renewal on demand only; nil-config fixed-address install and zero-allocation ranking | None | `TestPendingOffersRestart`, `TestRenewal` |
| `dagql/cache_part_admission_external_test.go` | base | 1 | Sessionless share of a restored Directory | None found | Sharing rows (batch 6/7) |
| `dagql/cache_part_boundary_test.go` | base | 2 (5) | Arrival during Lazy preparation (ready, chain, second refusal); pin-cancel and missing-descriptor boundaries | None found | Output/task completion row (partial) |
| `dagql/cache_part_chain_lifetime_test.go` | base | 2 (6) | Admitted chain survives donor collection and slot replacement; back-reference sync failure; ImportChain ref cleanup handoff | None found | Two-offer-owner row, donor lifetime row |
| `dagql/cache_part_content_test.go` | **batch 5** | 2 | Provider over real `ImportChain`: statuses on both attempts, truncation, digest, stall, local faults, one episode per chain; exhaustion across equivalent sources to the Lazy fallback | None | `TestRemoteCacheIntegratedContent` (U tier), `TestRenewal` (native success and timeout only) |
| `dagql/cache_part_decode_test.go` | base | 2 | Typed decode before external Finish; decode loses to an installed revision | None found | Encoded decode row |
| `engine/snapshots/import_test.go` | base | 16 (not run in this set) | ImportChain and ExportChain mechanics on real stores (prefix reuse, pins, cancellation, classification) | None; the engine uses these paths indirectly | `TestRemoteCacheIntegratedContent` (U tier) |
| `engine/engineutil/containerimage_lifetime_test.go`, `engine/engineutil/imageexport/lifetime_test.go` | base | 2 + 1 (not run in this set) | Image export and container image lifetimes on real stores | Image export integration tests indirectly | None named |

Summary facts for the Human's decision:
- 31 of these files are in the four packages; 40 top-level tests skipped in this run.
- Four files (8 tests) are batch 5's own.
- Batch 7's native design names a counterpart for most offer and renewal rows. For the fault-injection, barrier and cost cases (sync retry, pin release, preparation and publication races, boot scans, rekey cost, local writer and lease faults), nothing native is named that would carry the same observation.

## Development checks (not verification)

These are the builds and runs made while writing the code; results come from the verification set above. Logs are in [logs/dev](logs/dev/).

- Every commit was compiled with `go build`/`go vet` for the packages it touched before it was committed.
- The port check (`go test ./dagql -run '^TestOfferParts' -count=1 -v`, plain and `-race`, both exit 0, 28.1 s and 60.3 s) ran before the test timeout rule and carried no `-timeout`. See [logs/port](logs/port/).
- New tests ran as they were written, with `-timeout=60s` (in-process) or `-timeout=120s` (privileged stores). Before the rule, the step 2 run and the first step 3 run carried no timeout, and the later step 3 runs carried `-timeout=300s`.
- **Incident.** The first run of `TestRemoteCacheBridgeAttachment` carried no `-timeout` and hung for about six minutes until the Human interrupted it. Its partial log was overwritten by the rerun; [step4a-dagql-hung.log](logs/dev/step4a-dagql-hung.log) is a transcription of what that log contained when it was read after the interruption. A goroutine started before the request was queued consumed that request, so a check failed. The failure aborted the test before it ended an open cache operation, while a concurrent `Close(context.Background())` waited for that operation. Cleanup then waited on `Close` through the shared run-once guard. The test now takes the request first, ends the operation in `defer` and closes with a deadline. Every new test's waits outside `synctest` are bounded, and every run since carries a timeout. This led to the Human's test timeout rule.
- Development failures fixed in test code only: a layer comparison across time locations and a fixture that left the lower address usable (step 3); the close-cause test's shared cleanup asserting nil (step 5).

## Measured cost against the budget (corrected set)

| Set | Budget | Measured |
| --- | --- | --- |
| Package and race invocations (3, concurrent) | a few minutes | 21.4 s wall |
| Engine regression (1) | about 5 min including build | 300.7 s wall (test 1m10s) |
| Model runs | 0 | 0 |

## Not run, and why

- **`engine/snapshots` and `engine/engineutil`**: unchanged by this batch and not in the coordinator's package list. Their real-store tests would skip unprivileged.
- **Real-store tests**: skipped unprivileged, as listed; no privileged rerun (rule 2).
- **The rest of the transfer suite and the opt-in diagnostic**: not needed for this batch's regression (rule 4). The mixed-exec test is the one that exercises the fixture seam and the shutdown change.
- **Model runs, `./...`, the native pipeline proof (batch 7)**: not run.

## Pre-existing slop that slows the work (rule 5)

- **Serial suite.** `core/integration/remote_cache_transfer_test.go:28` dropped `testctx.WithParallel()`, which cost about 9.5 minutes of serial engine time in the first run. Fixed in `e1adfa2694`.
- **Tests that skip without mount privileges.** 40 top-level unit tests in dagql, core and core/schema (table above), plus 19 in `engine/snapshots` and `engine/engineutil`, silently skip without mount privileges. Running them needs root and a mount namespace, and batch 7's design §6 prescribes exactly that runner. Batch 5 added 8 more tests of this kind; that is my own new slop, reported rather than hidden.
- **Unconditional skip.** `dagql/cache_test.go:2289` skips unconditionally with a TODO.
- **gofmt and vet at the base.** `core/schema/foreign_module_context_test.go` is not gofmt-clean, and `go vet ./engine/server` fails at the base on `engine/server/session_attachables.go:211` (a discarded cancel). Every vet run of that package reports it, so a clean vet cannot serve as a gate there.
- **Build cost of an engine test.** A 70-second engine test costs about 5 minutes wall, because the `engine-dev test` step builds and starts a dev engine each time (4m1s of the 300.7 s run).
- **Engine run output.** The dev-engine CLI output of a passing `--test-verbose` run contains only the top-level test count. Per-test results need a separate `dagger trace --test` query per test.

## Limits

- Latency and memory are not measured, except the zero-allocation check for ranking a renewal-only offer without a bridge. The default transport's disabled decompression is set by construction and not exercised against a compressing server.
- Engines do not yet supply `RemoteCacheIntegration`. The native gated fixture gains it in batch 7, as the design says.
- `core/TestOfferPartsNativeAdmission` supplies bytes through the test override (decision 1); byte transfer through the transport is covered in dagql.
- The server-lifetime `GracefulStop` test uses a partly built `Server` (the fields `GracefulStop` touches), not a full `NewServer`.
- Source reading establishes the lock order (M never nested with E, G, D, P or the demand mutex; claim under the demand mutex, release, then M; results published under M before demand state). The race runs support it; no separate lock-order tool was run.
