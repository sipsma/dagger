# Verification ledger

The engine-debugging skill was read in full before tests. Package selections
were narrow and sequential; full working logs are under `/tmp`. Native runs used
the documented engine-dev route and independent nested dev-engine state/clients.
The current standalone acceptance follows addendum 2; the earlier checkpoint
commands below remain historical evidence.

## Addendum 2 verification

Production code is unchanged from `4041385fc3`; the new commits add real-store
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
also passes. The precise cause of the earlier default-GC run's row loss was not
established; no claim that this earlier attempt passed is made.
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
