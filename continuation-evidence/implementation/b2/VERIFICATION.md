# Verification ledger

The engine-debugging skill was read in full before tests. Package selections
were narrow and sequential; full working logs are under `/tmp`. Native runs used
the documented engine-dev route and independent nested dev-engine state/clients.
This ledger describes a blocked checkpoint, not full acceptance.

| Log | Command / result | Meaning |
| --- | --- | --- |
| `addendum-guards.log` | Focused core filesystem publication/body-latch controls — pass | The preserved negative probe is permanent and passes; Container control retained. Final verbose run below repeats these controls. |
| `addendum-build.log` | `go build ./core/schema` — exit 0 | Addendum commit builds independently. |
| `addendum-dagql.log` | Capture regression selection — pass | Row guard scope retains ownership/cancellation behavior. |
| `step3-transfer.log` | `go test ./dagql -run '^(TestValueTransfer|TestVisitPersistedCallID|TestCapturePersistedRecord)' -count=1 -timeout=90s` — pass | Held capture, exact references, marker filtering, preparation/publication boundaries. |
| `step3-decode.log` | `go test ./dagql -run '^(TestValueTransfer|TestCachePersistenceImported.*Decode.*|TestCachePersistenceDecodeInstallPreservesRequiredSessionResources|TestPersistDecodeChildLoadBorrowsAdmittedOperation|TestCacheMakeResultUnpruneableClearsPersistedExpiry)$' -count=1 -timeout=90s` — pass | Stale decode disposal, desired-role copies, preserved cleanup/session ownership. |
| `step3-native-codecs.log` | Focused core transfer and native producer/storage persistence regressions — pass with store privilege skip | Metadata controls; not real chain-byte evidence. |
| `step4-core.log` | `go test ./core -run '^(TestModDepsForCallInstalledPreference|TestForeignModuleContextReaders)$' -count=1 -timeout=90s` — pass | Both import orders, Module-reference positions, host-reader sentinel. |
| `step5-dagql.log` | `go test ./dagql -run '^(TestValueTransfer|TestSchemaModuleSelectionFallback|TestCachePersistenceSchemaMismatchWipesStore)' -count=1 -timeout=120s` — pass | Includes pending ownership through local restart and onward transfer, exact raw fixture holds and schema fallback. |
| `step5-race.log` | `go test -race ./dagql -run '^(TestValueTransferCapture|TestValueTransferImportPublication|TestValueTransferOfferOwners)$' -count=1 -timeout=180s` — pass | Includes actual concurrent replacement/capture/collection, concurrent lookup at commit and Close during admitted import. |
| `step5-core.log` | `go test ./core -run '^(TestValueTransfer|TestCapturePersistedFilesystemDirectEvaluation|TestCapturePersistedContainerDirectEvaluation|TestFilesystemOutputRevision|TestFilesystemPersistenceRetainsBodyLatch|TestModDepsForCallInstalledPreference|TestForeignModuleContextReaders|TestBoundToolsUseTheirDefiningSchemaAuthoritatively)' -count=1 -timeout=120s -v` — pass, two explicit store skips | Foreign forms as roots and inline items, candidate BFS, permanent latch probe, existing defining-schema lazy-load/rebinding regression. The real view and mount chain tests skip for missing bind-mount privilege. |
| `step5-schema.log` | `go test ./core/schema -run '^(TestRemoteCacheFixture|TestForeignModuleContextReaders|TestForeignLocalItemRemoval|TestForeignWithSourceSubpathWorkspace)$' -count=1 -timeout=90s` — pass | Gate/root/path validation, repeated GraphQL imports, exact numeric handles beyond float precision, forked handlers, counters, cancellation, source Workspace/removal controls. |
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
