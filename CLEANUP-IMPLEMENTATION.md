Focused cleanup implementation, 13 September 2026

Author: the separately staffed Codex cleanup implementer. This implements the
three-item A/B/C correction commission. The source candidate is
`193fb7c24a36e546bc87e72f9f838a9db4c0a75c`, tree
`3813552225a0d3c020c8d6bb3e327acab5a5531f`. It is signed off using the
repository-configured author identity. The separate evidence commit adds only
this report and `cleanup-evidence/` above that source candidate. The starting commit is
`1ba150a79b8a3f7568e27a22cee492f578ffa538`, tree
`d5a73331e65629681246351a864fd2ecda9b899e`; all six retained implementation
commits remain in its ancestry. Code was written fresh in this private worktree.

All passing final checks used exactly the source candidate's tree, including the final
fixtures. `git diff --cached --check` was clean before commit. The final baseline
control replaces the three named files with the exact `1ba150a79b` blobs and
intentionally fails; it does not test the correction.

| Check | Result | Command/source/result record | Log |
|---|---|---|---|
| Final fixtures over the base | Expected failures reproduce A/B | [baseline-regressions-v2.json](cleanup-evidence/baseline-regressions-v2.json) | [log](cleanup-evidence/baseline-regressions-v2.log) |
| Focused capture and retained persistence/restore checks | PASS, DagQL and core | [focused-v2.json](cleanup-evidence/focused-v2.json) | [log](cleanup-evidence/focused-v2.log) |
| Focused capture, producer and concurrent-group race checks | PASS, no race reports | [race.json](cleanup-evidence/race.json) | [log](cleanup-evidence/race.log) |
| Full affected packages | PASS, DagQL/core/core-schema | [packages.json](cleanup-evidence/packages.json) | [log](cleanup-evidence/packages.log) |
| Vet | PASS, no diagnostics | [vet.json](cleanup-evidence/vet.json) | [log](cleanup-evidence/vet.log) |
| Gofmt | Clean | [formatting.json](cleanup-evidence/formatting.json) | [log](cleanup-evidence/formatting.log) |

Exact commands for the final checks, run serially from this worktree:

```sh
go test -p 1 ./dagql ./core -run '^(TestCapturePersistedRecord|TestCapturePersistedContainer|TestContainerCompletedProducer|TestFilesystemCompletedProducerPersistence|TestContainerExecPersistsInputMetadata|TestContainerPersisted|TestContainerRestore|TestPersistedCoreLazyPayloadRelocation|TestContainerConcurrentGroupCompletionClearsLazyOnce|TestContainerExecEvaluatesParentMountsConcurrently|TestContainerGetVariantRefsEvaluatesImageParts)' -count=1 -timeout=90s -v
go test -race -p 1 ./dagql ./core -run '^(TestCapturePersistedRecord|TestCapturePersistedContainer|TestContainerCompletedProducer|TestFilesystemCompletedProducerPersistence|TestContainerRestoreConcurrentParts|TestContainerConcurrentGroupCompletionClearsLazyOnce|TestContainerExecEvaluatesParentMountsConcurrently)' -count=1 -timeout=120s -v
go test -p 1 ./dagql ./core ./core/schema -count=1 -timeout=120s
go vet -p 1 ./dagql ./core ./core/schema
gofmt -l dagql/cache_persistence_capture.go dagql/cache_persistence_capture_test.go core/container_persistence.go core/container_persistence_test.go core/container_capture_test.go
go test -p 1 -overlay cleanup-evidence/baseline-overlay.json ./dagql ./core -run '^(TestCapturePersistedRecordInitialAttachment|TestCapturePersistedContainer)' -count=1 -timeout=90s -v
```

The baseline overlay contains absolute paths from this worktree; reproducing it
in another checkout requires changing those path prefixes. [artifact-manifest.json](cleanup-evidence/artifact-manifest.json)
records full SHA-256 hashes for the report and every evidence file except the
manifest itself. [candidate.json](cleanup-evidence/candidate.json) records the source commit, tree, base and
changed-file blob/SHA-256 hashes. Disk was checked before compilation; the lowest
observed free space was about 6.1 GiB during race compilation, and about 6.7 GiB
remained after validation. No concurrent suites were launched.

`CapturePersistedRecord` now checks the selected row's initial dependency
attachment inside the registration critical section, before taking its temporary
hold, loading its payload, or entering the codec. Open attachment returns
`ErrPersistStateNotReady`; failed attachment returns an ordinary error. This
checks only the selected row. The counted cache operation still finishes on both
early returns. The regression exercises real publication with a parked attachment
hook, verifies no codec entry or ownership/operation drift, preserves a failed
row with an ordinary test hold, and checks successful, stable capture and final
collection after clean attachment.

The Container encoder now excludes direct object evaluation using the existing
`LazyState` latches. It reads the active producer under `lazyOpMu`, or the retained
producer if the active pointer was cleared. It try-locks that producer's `LazyMu`
and each already-registered group's body mutex, and holds them through metadata,
part, recipe, and final JSON encoding. Busy latches return
`ErrPersistStateNotReady`. Holding `LazyMu` prevents new groups from being
registered or started, so capture needs no new latch entries, state, or routing.
Existing group mutexes cover runners that already passed `LazyMu`. Sibling
evaluation remains parallel outside capture.

Lock ordering was checked against `Evaluate`, `EvaluateGroup`,
`consumeFinalParentDelegations`, and `clearLazyWhenConsumed`. Consumption takes
`LazyMu` before `lazyOpMu`; capture only *tries* `LazyMu` while holding
`lazyOpMu`, so that inverse acquisition cannot wait. A group body can consult
`LazyMu`, so capture likewise only tries its body mutex and releases every
acquired lock on refusal. The codec uses locked completion accessors to avoid
re-entering `LazyMu`. Current group mappings read settled local metadata and do
not acquire the latch again. Encoding declared references does not load or
evaluate their ancestors. The selected cache row's existing `shared.lazyMu`
guard and ownership hold remain in place.

The retained producer matters even after `Container.Lazy` becomes nil:
`ContainerImportLazy` consumes that pointer while still inside its whole-op
`LazyState.Evaluate` body. Capture locks the retained producer's same latch,
including that last part of the body. Refined restoration shares its recipe's
state; a fully restored producer retained only as bytes needs no decode. At
quiescence the existing latches are available and encoding preserves the same
payload and snapshot links.

Production paths checked: legacy stdout/stderr use `metaFileContents` and
`evaluatePartsDirect`; export/publish/tarball use `getVariantRefs`; whole-object
`Container.Evaluate` routes refined operations through the same group latches and
the unrefined import through its whole-op latch. None of those evaluation paths
was rerouted. Directory and File have no analogous direct body entry point:
their bodies and completed-producer updates run inside the cache's
`LazyEvalFunc` callback. Their existing cache-side capture exclusion is retained.

The two current internal-document passages now describe retained original
producer inputs, Directory/File snapshot `lazyKind`/`lazyJSON`, and completed-row
decode retaining raw bytes without loading producer ancestors. Historical HTML,
the optional frame-less-row suggestion, graph/acquisition/equality machinery,
models, persistence schema, and the rejected four-file draft were not changed.

Validation evidence is in [cleanup-evidence](cleanup-evidence/). Each named run
has a `.log`, `.json` with the exact command, timestamps, exit status, source
tree, SHA-256 hashes and free disk before/after, and a `.patch` recording the
staged source delta from the base. The baseline overlay substitutes only the
three archived base files named in `baseline-overlay.json`; new regression
sources come from the corresponding patch. Input report/instruction paths and
SHA-256 hashes and archived copies are in `input-manifest.json`.

The initial baseline run reproduced both findings: open attachment was encoded,
direct metadata/snapshot/whole bodies were encoded while parked, and the codec
did not exclude a new direct group. The first corrected-source focused run
passed DagQL but exposed test-fixture mistakes: an empty platform rejected by
JSON decode and a completed parent whose normal final-delegation sweep entered
the deliberately parked sibling. The verified child process was sent SIGQUIT
(PID 427373, parent `go test` PID 426644); its stack trace is preserved in
`focused.log`. This is failed development evidence, not passing coverage. The
fixture was corrected to use a valid platform and an unevaluated parent.
Production code did not change between that run and the successful second run.

The new Container tests use controlled bodies inside real
`LazyState.EvaluateGroup`, `evaluatePartsDirect`, `Container.Evaluate`, and
whole-op `LazyState.Evaluate`. They cover busy metadata and snapshot groups,
first-time group exclusion while encoding, sibling progress after refusal,
whole-op bodies before and after clearing `Lazy`, final stable payloads, and
unlock on codec failure. They do not run a real process or remote transfer.
Existing completed-producer/restart tests cover cold ancestors staying undecoded,
saved outputs staying unopened on metadata access, local open failure, retry,
relocation, and re-encoding.

Native validation remains an explicit limit: the reviewers' interrupted native
restart subset reached no test results and supplies no coverage. No native engine
build, model run, broad `./...` suite, public action, helper coordination, server
change, or manual artifact/cache deletion was performed here. Serial focused
unit/race and affected-package checks were selected after reading the current
engine-debugging skill in full. These checks do not certify real-engine snapshot
manager/lease combinations or a connected cross-engine remote-cache feature.
Independent council review of the exact source candidate remains the next step.
