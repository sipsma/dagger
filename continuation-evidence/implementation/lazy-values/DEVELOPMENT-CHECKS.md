# Development checks

These logs preserve implementation-stage output. Historical names are translated to current vocabulary; the log manifest records the original byte count and SHA-256 before that vocabulary-only translation. They are separate from the final command ledger. Build-only checks and skipped real-store cases are not acceptance passes. The final privileged selections rerun the affected cases.

| Log | Final output marker | Failed tests reported in output |
|---|---|---|
| [development-base-existing-expectations.log](logs/development-base-existing-expectations.log) | Failure output retained | `TestContainerRestoreDirectMetadataSweep`, `TestRestoredSnapshotRoundTrip` |
| [development-base-snapshot-link-assertion.log](logs/development-base-snapshot-link-assertion.log) | Failure output retained | `TestCachePersistenceWorkerUsesEncodedSnapshotLinks` |
| [development-base-unsupported-target.log](logs/development-base-unsupported-target.log) | Failure output retained | `TestContainerPersistedUnsupportedTargetPreservesConsumedExecMeta` |
| [development-step1-compile.log](logs/development-step1-compile.log) | Passed command output; build only |  |
| [development-step1-final-race.log](logs/development-step1-final-race.log) | Passed command output |  |
| [development-step1-focused-rerun.log](logs/development-step1-focused-rerun.log) | Passed command output |  |
| [development-step1-focused.log](logs/development-step1-focused.log) | Failure output retained | `TestContainerRestoreIndependentParts`, `TestContainerRestoreDirectMetadataSweep`, `TestContainerRestoreConcurrentParts`, `TestContainerRestoreOpenFailureKeepsRecipeConsumed`, `TestContainerRestoreReportingAndBookkeepingRetry`, `TestRestoredSnapshotRoundTrip`, `TestContainerDirectorySelectorLeavesSiblingMountsPending` |
| [development-step1-parts.log](logs/development-step1-parts.log) | Failure output retained | `TestContainerMetadataOnlyMountMutationParts`, `TestContainerMountedSourceWriterParts`, `TestContainerMountedSourceWriterShadowsNestedMount`, `TestContainerWithoutPathFullEvaluationWithEmptyRootFS`, `TestContainerDelegationOverwritesStalePreCopiedAccessor` |
| [development-step1-real-rerun.log](logs/development-step1-real-rerun.log) | Failure output retained | `TestPartWholeoperationMixedRestart` |
| [development-step1-real.log](logs/development-step1-real.log) | Failure output retained | `TestLazyEvaluatedFilesystemClones` |
| [development-step1-schema-compile.log](logs/development-step1-schema-compile.log) | Passed command output; build only |  |
| [development-step1-whole-restart.log](logs/development-step1-whole-restart.log) | Passed command output |  |
| [development-step2-compile.log](logs/development-step2-compile.log) | Passed command output; build only |  |
| [development-step2-frame.log](logs/development-step2-frame.log) | Passed command output |  |
| [development-step2-http-rerun.log](logs/development-step2-http-rerun.log) | Passed command output |  |
| [development-step2-http.log](logs/development-step2-http.log) | Failure output retained | `TestHTTPLazyOperationCleanup` |
| [development-step2-schema-final.log](logs/development-step2-schema-final.log) | Passed command output |  |
| [development-step2-schema-rerun.log](logs/development-step2-schema-rerun.log) | Failure output retained | `TestHTTPResolvedCall` |
| [development-step2-schema.log](logs/development-step2-schema.log) | Failure output retained |  |
| [development-step3-schema.log](logs/development-step3-schema.log) | Passed command output |  |
| [development-step4-compile.log](logs/development-step4-compile.log) | Passed command output; build only |  |
| [development-step4-frame-final.log](logs/development-step4-frame-final.log) | Passed command output |  |
| [development-step4-git.log](logs/development-step4-git.log) | Passed command output |  |
| [development-step4-schema-rerun.log](logs/development-step4-schema-rerun.log) | Failure output retained | `TestGitResolvedFrames` |
| [development-step4-schema.log](logs/development-step4-schema.log) | Failure output retained | `TestGitResolvedFrames` |
| [development-step5-build.log](logs/development-step5-build.log) | Build or partial output; not acceptance |  |
| [development-step5-compile.log](logs/development-step5-compile.log) | Passed command output; build only |  |
| [development-step5-core-compile.log](logs/development-step5-core-compile.log) | Passed command output; build only |  |
| [development-step5-core-race.log](logs/development-step5-core-race.log) | Passed command output |  |
| [development-step5-core-rerun.log](logs/development-step5-core-rerun.log) | Passed command output |  |
| [development-step5-core.log](logs/development-step5-core.log) | Failure output retained | `TestLazyOperationPathCleanup` |
| [development-step5-metadata-extra.log](logs/development-step5-metadata-extra.log) | Passed command output |  |
| [development-step5-metadata.log](logs/development-step5-metadata.log) | Passed command output |  |
| [development-step5-schema-final.log](logs/development-step5-schema-final.log) | Passed command output |  |
| [development-step5-schema-new.log](logs/development-step5-schema-new.log) | Failure output retained | `TestBuiltinMetadataSelectors` |
| [development-step5-schema-race.log](logs/development-step5-schema-race.log) | Failure output retained | `TestMountConstructorsOwnNoRefs` |
| [development-step5-schema.log](logs/development-step5-schema.log) | Failure output retained | `TestEagerContainerMountMetadataResolvers`, `TestCloneContainerForSchemaChildDisablesFromContentDigest` |
| [development-step6-core-race.log](logs/development-step6-core-race.log) | Passed command output |  |
| [development-step6-dagql-final.log](logs/development-step6-dagql-final.log) | Passed command output |  |
| [development-step6-dagql-race.log](logs/development-step6-dagql-race.log) | Passed command output; skipped cases present |  |
| [development-step6-dagql.log](logs/development-step6-dagql.log) | Failure output retained; skipped cases present | `TestCachePersistenceWorkerUsesEncodedSnapshotLinks` |
| [development-step6-schema-race.log](logs/development-step6-schema-race.log) | Passed command output |  |
| [development-step7-git-frames.log](logs/development-step7-git-frames.log) | Passed command output |  |
| [development-step7-http-hits.log](logs/development-step7-http-hits.log) | Passed command output |  |
| [development-step7-persistence-expectations.log](logs/development-step7-persistence-expectations.log) | Failure output retained | `TestContainerPersistedUnsupportedTargetPreservesConsumedExecMeta` |
| [development-step7-stored-results.log](logs/development-step7-stored-results.log) | Passed command output |  |

Early failures include fixture construction/expectation corrections, the base expectation defects described in REPORT.md, and initial harness setup/compile errors. The complete output is retained rather than classifying those iterations as final passes. The final package manifest records actual exit statuses and invocation times.
