# Batch 5 implementation report: offers before execution and bounded renewal

Implementer, 17 September 2026. Steps 1 to 5 of the commission (`git show 64a0137a5f:continuation-evidence/implementation-commissions/BATCH5-OFFERS-IMPL.md`) are implemented on the reworked batch 4 head. Every package invocation in the verification set passed, and so did the engine invocation (results below).

| Identity | Commit |
| --- | --- |
| Branch | `offers-implementer-implementation-fb886a62` |
| Base | `fd9cfd55a98b176bf82cf84ee1b4c3ae1257fde5` |
| Implementation tip | `2b564d9c9c0e66a09dd041a4b3c3bcb7859593e4` |
| Evidence tip | the commit that adds this report |
| Diff | `git diff fd9cfd55a9 2b564d9c9c -- . ':!continuation-evidence'` (24 files, +4184/−155) |

Governing revisions: the design at `49b268a354`, read through its vocabulary table; the packet at `2daf4b4d9e` and `64a0137a5f`; decision 1 as answered in the binding start; the designer's readiness points a to e (`62809f73bc`); the verification time rule and the test timeout rule.

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
| evidence tip | evidence | This report, the ledger data and the logs. |

The evidence commits `d49e309bcd`, `f7af21e6f1` and the evidence tip are to be dropped before publication. Every commit is signed off and has no attribution trailer. Before any check or report, the port commit was amended once: `fbf61b8d78` had captured the pre-rename index. The coordinator confirmed that amend. `1c21c17463` and every later commit are unamended.

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

## Verification ledger

All commands ran from the worktree root at `2b564d9c9c`. The privileged runner is `-exec 'sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private'`, written `$PRIV` below. `sudo -n true` exited 0 in the same set. The seven package invocations ran concurrently inside one shell bounded by `timeout 420s`, starting 02:34 UTC. The set took **117.2 s** of wall time (budget: 15 minutes). Per-test results and durations for every invocation are in [package-results.json](package-results.json); full verbose output is in [logs](logs/).

| Invocation | Command | Timeout | Exit | Wall | Go time | Results |
| --- | --- | --- | --- | --- | --- | --- |
| dagql-priv | `go test -exec "$PRIV" ./dagql -count=1 -v -timeout=300s` (whole package) | 300 s | 0 | 25.6 s | 17.895 s | 743 pass, 0 fail, 1 skip (see below), 0 races |
| dagql-priv-race | `go test -race -exec "$PRIV" ./dagql '-run=^Test(Offer\|Renewal\|RemoteCache\|Part\|CacheCloseWithShutdownError\|ValueTransfer)' -count=1 -v -timeout=300s` | 300 s | 0 | 109.5 s | 42.849 s | 181 pass, 0 fail, 0 skip, 0 races |
| core-priv | `go test -exec "$PRIV" ./core -count=1 -v -timeout=300s` (whole package) | 300 s | 0 | 35.1 s | 26.888 s | 1285 pass, 0 fail, 0 skip |
| core-priv-race | `go test -race -exec "$PRIV" ./core '-run=^Test(OfferParts\|Part\|HTTP\|StatelessHTTP\|ValueTransfer\|SnapshotTransfer\|LazyOperation\|EvaluatedLazyOperation)' -count=1 -v -timeout=300s` | 300 s | 0 | 116.8 s | 55.754 s | 199 pass, 0 fail, 0 skip, 0 races |
| schema-priv | `go test -exec "$PRIV" ./core/schema -count=1 -v -timeout=300s` (whole package) | 300 s | 0 | 38.2 s | 13.532 s | 516 pass, 0 fail, 0 skip |
| server | `go test ./engine/server -count=1 -v -timeout=120s` (whole package) | 120 s | 0 | 12.8 s | 4.333 s | 281 pass, 0 fail, 0 skip |
| server-race | `go test -race ./engine/server -count=1 -v -timeout=240s` (whole package) | 240 s | 0 | 117.2 s | 5.795 s | 281 pass, 0 fail, 0 skip, 0 races |

In the table, `\|` stands for `|` inside the regular expression. Wall time includes compilation.

The one skip is `TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases`. It skips unconditionally ("TODO: re-enable after last-waiter canceled cleanup semantics are decided") and does so at the base as well. It is not counted as a pass.

Batch 5 test durations (plain / race), in seconds:

| Test | Plain | Race |
| --- | ---: | ---: |
| `dagql/TestOfferPartsBeforeStart` | 0.08 | 0.23 |
| `dagql/TestOfferPartsInvalidatesSourceCheck` | 0.03 | 0.04 |
| `dagql/TestOfferPartsOwnership` | 0.02 | 0.09 |
| `dagql/TestOfferPartsResourcesAndSettlement` | 0.01 | 0.08 |
| `dagql/TestOfferPartsInlineAndReferencedRows` | 0.02 | 0.15 |
| `dagql/TestOfferPartsClosesNativeAdmission` | 0.01 | 0.11 |
| `dagql/TestOfferPartsAcceptedCleanupFailure` | 0.04 | 0.18 |
| `dagql/TestOfferPartsPreparationWindow` | 1.60 | 0.18 |
| `dagql/TestOfferSettlementReplacement` | 1.47 | 3.94 |
| `dagql/TestOfferResourcesDoNotGateLookup` | 0.69 | 0.47 |
| `dagql/TestOfferPendingRestartAndForward` | 0.53 | 0.94 |
| `dagql/TestCacheCloseWithShutdownError` | 0.11 | 0.18 |
| `dagql/TestRenewalMailbox` | 0.00 | 0.01 |
| `dagql/TestRenewalEpisodeSet` | 0.00 | 0.00 |
| `dagql/TestRenewalClaimKeepsSourceCheck` | 0.01 | 0.08 |
| `dagql/TestRenewalChainControls` | 3.60 | 4.99 |
| `dagql/TestRenewalExhaustion` | 1.03 | 4.50 |
| `dagql/TestRenewalShutdownDrainsOwnership` | 0.31 | 0.58 |
| `dagql/TestRemoteCacheBridgeAttachment` | 0.04 | 0.17 |
| `dagql/TestRemoteCacheUnusedCost` | 0.41 | 1.00 |
| `dagql/TestPartContentSourceAvailability` | 0.01 | 0.04 |
| `dagql/TestPartContentIdleReader` | 0.00 | 0.00 |
| `dagql/TestPartFixedProvider*` (3 tests) | ≤0.01 | ≤0.04 |
| `dagql/TestPartUnusedHostAllocatesNoGate` | 0.00 | 0.00 |
| `core/TestOfferPartsNativeAdmission` | 0.29 | 0.97 |
| `engine/server/TestRemoteCacheIntegrationConfig` | 0.00 | 0.00 |
| `engine/server/TestRemoteCacheAdapterLifetime` | 0.01 | 0.00 |
| `engine/server/TestRemoteCacheGracefulStop` | 1.74 | 1.63 |

### Engine invocation

One `dagger api call engine-dev test` invocation ran the whole gated transfer suite (all four methods, including the mixed-exec test and the opted-in default-policy diagnostic) at `2b564d9c9c`. It ran concurrently with the package set, using the rework's diagnostic runner overlay ([patch](logs/diagnostic-runner-overlay.patch)) and an env file containing `_DAGGER_TEST_REMOTE_CACHE_PRUNE_DIAGNOSTIC=1`. The overlay was applied to the working tree for this invocation only and restored afterwards; `git diff HEAD -- .dagger` is empty.

```sh
timeout 1020s dagger -vv api call engine-dev test --pkg ./core/integration '--run=^TestRemoteCacheTransferSuite$/^(TestSchemaRecovery|TestSchemaRecoveryCold|TestPartMixedExecOutputs|TestDefaultGCPruneDiagnostic)$' --timeout=10m --test-verbose --env-file=file:/tmp/b5-verify/diagnostic.env
```

Test timeout 10 minutes; outer bound 1020 s. **Exit 0; 818.4 s wall** (02:33:55 to 02:47:34 UTC; budget 15 minutes), including 37.8 s workspace load and the dev engine build. Trace `e2b788da678c718c1e0f987488bc99c7`. The results below come from `dagger trace` on that trace ([root](logs/engine-trace-root.log), [suite](logs/engine-trace-suite.log), one log per method in [logs](logs/)); the CLI output is [engine-cli.log](logs/engine-cli.log). No selected test was skipped: the diagnostic ran its `after` phase.

| Test | Result | Duration |
| --- | --- | ---: |
| `TestRemoteCacheTransferSuite` | PASS | 9m28s |
| `TestRemoteCacheTransferSuite/TestSchemaRecovery` (both warm orders) | PASS | 3m46s |
| `TestRemoteCacheTransferSuite/TestSchemaRecoveryCold` | PASS | 2m37s |
| `TestRemoteCacheTransferSuite/TestPartMixedExecOutputs` | PASS | 1m0s |
| `TestRemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic` | PASS | 2m5s |
| `TestRemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic/after` | PASS | 1m8s |

Machine-readable form: [engine-results.json](engine-results.json). Observations, neither of which failed the run:

- **Reselect errors on a passing call.** In `TestSchemaRecoveryCold`, the span `CacheProbe.report(seed: "different")` is marked ERROR with "6x part sources changed; reselect". The test's `require.NoError` on that call and its later assertions passed, so these are internal dispatcher reselects recorded on the span. The suite never calls `OfferParts`, and its fixture uses the unchanged override path. The saved rework logs contain only selected output, so they neither show nor rule out the same span at the base. I did not rerun the base to compare.
- **Dev engine service ERROR at teardown.** The trace's services list shows the dev engine service ✘ ERROR after 10m21s, while every test passed. It is most likely the stop at the end of the run; the trace did not show its cause.
- The trace view keeps span trees and log tails but not the tests' `t.Logf` counter lines, so no counters are quoted here.


## Development checks (not verification)

These are the builds and runs made while writing the code; results come from the verification set above. Logs are in [logs/dev](logs/dev/).

- Every commit was compiled with `go build`/`go vet` for the packages it touched before it was committed.
- The port check (`go test ./dagql -run '^TestOfferParts' -count=1 -v`, plain and `-race`, both exit 0, 28.1 s and 60.3 s) ran before the test timeout rule and carried no `-timeout`. See [logs/port](logs/port/).
- New tests ran as they were written, with `-timeout=60s` (in-process) or `-timeout=120s` (privileged stores). Before the rule, the step 2 run and the first step 3 run carried no timeout, and the later step 3 runs carried `-timeout=300s`.
- **Incident.** The first run of `TestRemoteCacheBridgeAttachment` carried no `-timeout` and hung for about six minutes until the Human interrupted it. Its partial log was overwritten by the rerun; [step4a-dagql-hung.log](logs/dev/step4a-dagql-hung.log) is a transcription of what that log contained when it was read after the interruption. A goroutine started before the request was queued consumed that request, so a check failed. The failure aborted the test before it ended an open cache operation, while a concurrent `Close(context.Background())` waited for that operation. Cleanup then waited on `Close` through the shared run-once guard. The test now takes the request first, ends the operation in `defer` and closes with a deadline. Every new test's waits outside `synctest` are bounded, and every run since carries a timeout. This led to the Human's test timeout rule.
- Development failures fixed in test code only: a layer comparison across time locations and a fixture that left the lower address usable (step 3); the close-cause test's shared cleanup asserting nil (step 5).

## Measured cost against the budget

| Set | Budget | Measured |
| --- | --- | --- |
| Package invocations (7, concurrent) | 15 min wall | 117.2 s wall |
| Engine invocation (1) | 15 min wall | 818.4 s wall |
| Model runs | 0 | 0 |

The two sets overlapped from 02:34:02 to 02:36:00 UTC; the whole verification took 13 min 39 s of wall time.

## Not run, and why

- **`engine/snapshots`**: unchanged since the base, where it passed in the rework ledger. Its classification is exercised through `TestRenewalChainControls`.
- **Whole-package `-race` for dagql and core**: the race variants cover the offer, renewal, part, transfer, HTTP and Lazy selections listed above instead.
- **Model runs**: none authorized. Batch 7 owns the model extension.
- **Native pipeline proof** (renewal, skipped report body, forced failure with Lazy evaluation in real engines): batch 7.
- **`./...`**: not run.

## Unrelated problems found (not changed)

- `core/schema/foreign_module_context_test.go` is not gofmt-clean at the base.
- `go vet ./engine/server` reports `session_attachables.go:211` (a discarded `context.WithTimeoutCause` cancel) at the base.
- The unconditional skip in `dagql/TestCacheContextCancel` noted above.

## Limits

- Latency and memory are not measured, except the zero-allocation check for ranking a renewal-only offer without a bridge. The default transport's disabled decompression is set by construction and not exercised against a compressing server.
- Engines do not yet supply `RemoteCacheIntegration`. The native gated fixture gains it in batch 7, as the design says.
- `core/TestOfferPartsNativeAdmission` supplies bytes through the test override (decision 1); byte transfer through the transport is covered in dagql.
- The server-lifetime `GracefulStop` test uses a partly built `Server` (the fields `GracefulStop` touches), not a full `NewServer`.
- Source reading establishes the lock order (M never nested with E, G, D, P or the demand mutex; claim under the demand mutex, release, then M; results published under M before demand state). The race runs support it; no separate lock-order tool was run.
