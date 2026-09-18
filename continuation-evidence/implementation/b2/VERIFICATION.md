# Verification ledger

The engine-debugging skill was read in full before tests. Package selections
were narrow and sequential; full working logs are under `/tmp`. Native runs used
the documented engine-dev route and independent nested dev-engine state/clients.
The current standalone acceptance follows addendum 2; the earlier checkpoint
commands below remain historical evidence.

## Round 3 verification

R1 is implemented at `f985ac7887892f94d868aa8e8995a0e251dbf54c`, above the
reviewed `5e1007a749887cb0ef975331739fed58413f251a`. The only code change is the
explicit environment opt-in at the beginning of the diagnostic test. R2's
journal notes are in [REPORT.md](REPORT.md). No production code changed.

The following commands ran sequentially at that implementation tip and exited
0; [round3-commands.txt](logs/round3-commands.txt) records their exact invocations.

```sh
go test -p=1 -c -o /tmp/b2-r3-integration.test ./core/integration
env -u _DAGGER_TEST_REMOTE_CACHE_PRUNE_DIAGNOSTIC dagger api call engine-dev test --pkg ./core/integration --run='RemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic' --timeout=5m --test-verbose
```

The compile-only check produced the integration test binary with no compiler
output. The default suite selection completed successfully and skipped the
diagnostic in 0.0s, before `runTransferSchemaRecovery` or any scenario engines.
The skip explains the opt-in and the allocation cap:

> set _DAGGER_TEST_REMOTE_CACHE_PRUNE_DIAGNOSTIC=1 to opt in to the disk-pressure diagnostic (up to 8 GiB temporary allocation)

[Native run](logs/round3-default-skip.log),
[scoped trace](logs/round3-default-skip-trace.log),
[test output](logs/round3-default-skip-test.log).
Trace: <https://dagger.cloud/dagger/traces/96e9ad15e8f5a157197f66894ae20475>.
The engine-dev harness starts its usual test infrastructure; the skipped test
does not start its recovery scenario or allocate the pressure file.
Pinned-bounds acceptance cases remain enabled by default and unchanged.

For a future explicitly opted-in run, after preparing the persistent dev engine
and local binaries with `./hack/dev`, the invocation is:

```sh
env _DAGGER_TEST_REMOTE_CACHE_PRUNE_DIAGNOSTIC=1 ./hack/with-dev go test -v -count=1 -run='RemoteCacheTransferSuite/TestDefaultGCPruneDiagnostic' -timeout=15m ./core/integration
```

That command is documented only; the pressure diagnostic was not run in round 3.
The separate fixture-root variable does not opt into the diagnostic. Existing
measured-pressure assertions and cleanup remain unchanged. Round 2's measured
run below remains the P2 evidence; neither it nor the pinned acceptance suite
was repeated for this test-only guard.

## Round 2 verification

Council decisions P1–P5 are implemented at `b76c6ccbff2f68f2ca6f52b5b89ef7ec6c6d2dbf`.
All commands and selections are recorded exactly in
[round2-commands.txt](logs/round2-commands.txt). Every acceptance command exited 0.

| Evidence | Result |
| --- | --- |
| [DagQL](logs/round2-dagql.log), [race selection](logs/round2-race.log) | Capture, import publication, offer ownership and persistence pass. Invalid offer-copy regression returns not-ready without leaking a hold or lock. Both inaccessible installed-schema candidate cases fall through, including expiry fallback. Required concurrent capture/publication/owners controls pass under the race detector. |
| [Schema](logs/round2-schema.log), [core](logs/round2-core.log) | Imported pending Container child returns unavailable for derived rootfs entries; direct evaluation refuses pending fs and metadata sweep succeeds. Existing metadata-chain and concurrent pending-query controls pass. Fixture exposes reset reason and prune count without a saved handle. |
| [Server](logs/round2-server.log) | Real automatic disk and metadata prune passes update the gated fixture's count, which another server instance reads independently of cache state. Reset-reason fields survive that diagnostic update. |
| [Privileged peers](logs/round2-privileged.log) | Both Git backends, nested Directory/File chain, Container mount, unopened sibling/broken-open controls, completed-offer restart, File/Directory body-latch and revision controls, and Container control execute and pass. No skips. Uses the authorized sudo/unshare mount namespace launcher. |
| [Native acceptance](logs/round2-native.log), [scoped trace](logs/round2-native-acceptance-summary.log) | Both warmed-runtime import orders and all existing report/node/context/restart/interface/scalar/enum/tool/native/foreign-context assertions pass. Each pinned restart asserts no reset, zero removals and a retained persisted edge. |
| [Default-policy diagnostic](logs/round2-default-prune.log), [row-specific evidence](logs/round2-restart-diagnostics.log), [trace](logs/round2-default-prune-summary.log) | Exact saved-report row 4681 is present and persisted before shutdown, then pruned under default policy and absent after restart. Removed roots: 0 → 16; no reset. The engine logs `CacheProbe.report` with `seed="same"` for the exact removed ID. The assertion ran; no diagnostic skip in this run. |

Native acceptance trace: <https://dagger.cloud/dagger/traces/93fe691e87443f2482042f0f6961baf2>.
Only the cold acceptance case is intentionally skipped under addendum 2:
[cold boundary](logs/round2-native-cold.log). The default-policy diagnostic in
that earlier combined run also skipped because there was no reclaim target;
its later measured-pressure run is the evidence that closes P2.

Measured-pressure trace: <https://dagger.cloud/dagger/traces/a1363afb7a7c79639b0741cf7fbc4c05>.
The default minimum-free target was 99,000,000,000 bytes. The diagnostic measured
103,559,827,456 available bytes, requested 6,707,311,104 temporary bytes and wrote
6,397 MiB after rounding to its block size, below the 8 GiB cap. It removed the
file in cleanup. The bound itself was not changed; imported roots remain
pruneable. The fixture's journal records cumulative automatic disk/metadata GC
removals, including the previous engine process, and the current boot's reset
reason before any cache replacement can hide it.

Development attempts: a small Container-only probe (trace
`b1e3bfff46bc13b5cdab212df2469c3d`), the combined native run above, and a fixed
2 GiB pressure attempt (`4766a5d3014096ebb8456b2dd6cb6167`) all completed with
explicit no-pressure skips. They establish no row-removal claim. Measuring the
actual target and available space made the final pressure test bounded and
conclusive. Initial schema regression construction was corrected to use the
registered codec family and the legacy ID view required by the exact council
query. These setup attempts are superseded by the passing permanent regression.

The final native diagnostic rebuilt the engine and integration tests. A separate
compile-only integration check also completed successfully before that run.
The default-pressure branch was the only behavioral test change after the full
pinned acceptance run; production code and pinned acceptance behavior were
unchanged. The final test-only change skips the diagnostic when the required
allocation exceeds its 8 GiB cap; its measured passing branch is unchanged. A
final integration compile-only check passes at the implementation tip. No broader
suites were run.

## Addendum 2 verification (historical)

Before round 2, production code was unchanged from `4041385fc3`; those commits add real-store
peers, complete the native fixture, and record the coordinator's acceptance
boundary. The existing focused DagQL/core/schema/server checks and targeted race
selection below therefore remain applicable.

| Evidence | Command / result | Meaning |
| --- | --- | --- |
| [Selected chains](logs/addendum2-selected-chains.log), [exact command](logs/addendum2-selected-chains-command.txt) | Privileged `go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -run '^TestValueTransferParts' -count=1 -timeout=180s -v` with GOPATH/GOCACHE preserved — exit 0 | Both Git backends, selected nested view, Container mount and broken-open control execute with real stores; no skips. |
| [Final-offer restart](logs/addendum2-final-offer-restart.log), [exact command](logs/addendum2-final-offer-restart-command.txt) | Same privileged launcher, `./core -run '^TestValueTransferPersistenceFinalOfferRestart$' -count=1 -timeout=120s -v` — exit 0 | Local completed output plus redundant offer is restored, owner retained, offer retired, transfer pins removed, GC run and bytes read. |
| [Native full suite](logs/addendum2-native.log), [exact command](logs/addendum2-native-command.txt) | `dagger api call engine-dev test --pkg ./core/integration --run='RemoteCacheTransferSuite/TestSchemaRecovery' --timeout=20m --test-verbose` — exit 0 | Both import orders, zero-entry hit and negative control, node/context/restart, interface/scalar/enum, lazy tools/rebinding, exact native Module over lower imported equivalent, both foreign-context failures. Fully cold order is explicitly skipped by addendum 2. |
| [Isolated restart confirmation](logs/addendum2-native-after-confirm.log), [exact command](logs/addendum2-native-after-confirm-command.txt) | `dagger api call engine-dev test --pkg ./core/integration --run='RemoteCacheTransferSuite/TestSchemaRecovery$/after$' --timeout=20m --test-verbose` — exit 0 | Independent repeat of the previously failing order under the final fixture bounds, including clean restart and exact native Module control. |

[Scoped native trace summary](logs/addendum2-native-trace-summary.log) and
[explicit cold-order boundary](logs/addendum2-cold-boundary.log) were fetched from
the successful full trace without rerunning tests.

Full native trace: <https://dagger.cloud/dagger/traces/99a85ab411411db41ec44b04eef30c16>.
Only the cold-order case is intentionally skipped. Its source contains the full
scenario behind the skip, with runtime preparation disabled for that order.
Design §10 and the stack manifest record that batch 4/7 owns its acceptance.

Native development history: the first prepared-runtime run passed the original
hit/context/restart assertions. The expanded fixture initially used a nonexistent
module-object `portableID` field and an inline GraphQL fragment on an uninstalled
type. Those were corrected using the existing portable LLM binding and recovered
schema tool dispatch. The bare residual client was then prepared without serving,
as required by addendum 2. A subsequent full run passed the first import order and
both foreign cases but lost the second order's saved row across restart under the
fixture's default GC limits. The final fixture uses the existing persistence
suite's GC bounds and asserts raw saved-row presence immediately after restart.
The full suite passes. An independent isolated confirmation of the second order
also passes. Those historical logs omitted reset and prune counters; round 2 above provides
a measured reproduction of ordinary removal in the same saved-report scenario. The
earlier failed attempt is not counted as a pass.
[Earlier restart failure excerpt](logs/addendum2-native-default-gc-failure.log).
The earlier full logs remain under `/tmp/b2-addendum2-native-{1,2,3,4}.log`; these are
superseded attempts, not acceptance evidence.

## Earlier focused commands

| Log | Command / result | Meaning |
| --- | --- | --- |
| `addendum-guards.log` | Focused core filesystem publication/body-latch controls — pass | The preserved negative probe is permanent and passes; Container control retained. Final verbose run below repeats these controls. |
| `addendum-build.log` | `go build ./core/schema` — exit 0 | Addendum commit builds independently. |
| `addendum-dagql.log` | Capture regression selection — pass | Row guard scope retains ownership/cancellation behavior. |
| `step3-transfer.log` | `go test ./dagql -run '^(TestValueTransfer\|TestVisitPersistedCallID\|TestCapturePersistedRecord)' -count=1 -timeout=90s` — pass | Held capture, exact references, marker filtering, preparation/publication boundaries. |
| `step3-decode.log` | `go test ./dagql -run '^(TestValueTransfer\|TestCachePersistenceImported.*Decode.*\|TestCachePersistenceDecodeInstallPreservesRequiredSessionResources\|TestPersistDecodeChildLoadBorrowsAdmittedOperation\|TestCacheMakeResultUnpruneableClearsPersistedExpiry)$' -count=1 -timeout=90s` — pass | Stale decode disposal, desired-role copies, preserved cleanup/session ownership. |
| `step3-native-codecs.log` | Focused core transfer and native producer/storage persistence regressions — pass with store privilege skip | Metadata controls; not real chain-byte evidence. |
| `step4-core.log` | `go test ./core -run '^(TestModDepsForCallInstalledPreference\|TestForeignModuleContextReaders)$' -count=1 -timeout=90s` — pass | Both import orders, Module-reference positions, host-reader sentinel. |
| `step5-dagql.log` | `go test ./dagql -run '^(TestValueTransfer\|TestSchemaModuleSelectionFallback\|TestCachePersistenceSchemaMismatchWipesStore)' -count=1 -timeout=120s` — pass | Includes pending ownership through local restart and onward transfer, exact raw fixture holds and schema fallback. |
| `step5-race.log` | `go test -race ./dagql -run '^(TestValueTransferCapture\|TestValueTransferImportPublication\|TestValueTransferOfferOwners)$' -count=1 -timeout=180s` — pass | Includes actual concurrent replacement/capture/collection, concurrent lookup at commit and Close during admitted import. |
| `step5-core.log` | `go test ./core -run '^(TestValueTransfer\|TestCapturePersistedFilesystemDirectEvaluation\|TestCapturePersistedContainerDirectEvaluation\|TestFilesystemOutputRevision\|TestFilesystemPersistenceRetainsBodyLatch\|TestModDepsForCallInstalledPreference\|TestForeignModuleContextReaders\|TestBoundToolsUseTheirDefiningSchemaAuthoritatively)' -count=1 -timeout=120s -v` — pass, two explicit store skips | Foreign forms as roots and inline items, candidate BFS, permanent latch probe, existing defining-schema lazy-load/rebinding regression. The real view and mount chain tests skip for missing bind-mount privilege. |
| `step5-schema.log` | `go test ./core/schema -run '^(TestRemoteCacheFixture\|TestForeignModuleContextReaders\|TestForeignLocalItemRemoval\|TestForeignWithSourceSubpathWorkspace)$' -count=1 -timeout=90s` — pass | Gate/root/path validation, repeated GraphQL imports, exact numeric handles beyond float precision, forked handlers, counters, cancellation, source Workspace/removal controls. |
| `step5-server.log` | `go test ./engine/server -run '^TestForeignModuleContextReaders$' -count=1 -timeout=90s` — pass | Related-source/default-context reference conversion refuses foreign local paths; native control passes. |
| `final-build.log` | `go build ./core/schema ./engine/server` — exit 0 | Current production build. |
| `native-cold-runtime-blocker.log` | `dagger api call engine-dev test --pkg ./core/integration --run='RemoteCacheTransferSuite/TestSchemaRecovery' --timeout=20m` — exit 1 | A executes and exports; B imports without entering report; cold B module construction demands pending Container.fs. No ordinary B hit or later assertion is claimed. |

The native fixture was corrected twice before this failure: explicit normal
Module.serve established the requested schema, and counter matching accounted for
the original function-name casing returned by CurrentFunctionCall. Those fixture
issues are not the reported design blocker. The failure excerpt is from the
third run; the complete log remains `/tmp/b2-step5-native-3.log` with its trace
link in the excerpt. Subsequent checks compiled the preserved checkpoint and
validated its unit peers; no alternate runtime-selection policy was introduced.

Earlier step-1/step-2 logs and the old capture-gap probe remain historical
artifacts from the first report. The old gap is now resolved by the addendum.
