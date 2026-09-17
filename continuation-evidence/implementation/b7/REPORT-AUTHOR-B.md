# Batch 7, author B: slice 1 report

Author B, 17 September 2026. Branch `verification-implementer-implementation-128fdcd9`, base `c5b299142c`.

| Identity | Commit |
| --- | --- |
| Implementation tip | `959054a2e0` |
| Evidence tip | the commit that adds this report |
| Production diff | `git diff c5b299142c 959054a2e0 -- dagql ':!dagql/*_test.go'` |

## The result in short

1. **The real-store tests run unprivileged.** 75 of the 79 tests that build the real snapshot store now run and pass with no root, sudo, unshare, mount namespace or privilege skip. The other 4 are the git tests folded into author A's native `TestGitTrees`; they sit behind an unconditional skip that names it and are deleted when that case lands. Seven packages, whole, default parallelism: 3003 pass, 0 fail, about 27 s wall when the build is warm.
2. **Running them found a production defect on the first day.** `core`'s `TestPartInlineAddress/concurrent` fails about one run in twenty, on the unchanged batch 6 tree as well. It is a race in batch 4's source scan that fails a demand with a wrong hard error when two parts of one imported row are demanded concurrently, which is the default. Fixed in `959054a2e0` with two deterministic tests that fail before it. **This needs the council's eye**: I chose between options; see below.
3. **The reselect progress rule is in**, as the audit and the designer's B6 describe, in two commits: names and warnings with no behavior change, then the rule. Six sites record, not the eight my audit listed, and a version check records only on the payload revision, because `OutputRevision` is not monotonic for a Container.
4. **F3, the umask control, fits in process** and runs in a child process, so nothing mutates the shared umask.
5. **Not started:** item 3, the `Triggers` fault-injection observations and F2's Service and Module decoder closure with both leader orders.

## The production defect the conversion exposed

**Failed assertion.** `core/part_inline_test.go:116`, `require.NoError` on the second of two concurrent `LazyEvalFunc` calls for items 0 and 1 of one imported list row: `snapshot path does not name a declared inline envelope`.

**Call sequence**, from a stack captured at the error (temporary instrumentation, not committed): `demandPart` acquire Body, obtain Body, `installChainPart`, `PrepareReadyPart`, `prepareReadyPartFromBase`, `prepareScopedPartRecord(record, source, ...)`, `partRecordAt(source, items[i])`, `persistedEnvelopeAt`. The `source` record was entirely zero (`ResultID` 0, envelope kind empty) while `record`, the receiver's, was row 2.

**Cause.** `scanPartSources` (`dagql/cache_part_source.go:507`) probes every candidate and, when a probe answers `ErrPersistStateNotReady`, does `continue`. That passes over the probe, not the candidate. The candidate stays in the ranking with an empty record (capture failed) or an unvalidated one (capture succeeded, the version check failed), and its offer can still win as `PartDownloadable`, because for the receiver's own offer the ranking does not even consult availability. The receiver is a candidate for its own offers, and a sibling part's publication is exactly what makes the receiver's probe not ready. So the trigger is nothing more than two parts of one imported row demanded at once, which `evaluateAcquiredParts` and `evaluateAcquiredScope` do through an errgroup. For a root address the same path would fail with `part scope is not a codec envelope` (inferred from `partRecordAt`; I reproduced only the inline form).

**Attribution, with evidence.** 2 failures in 40 runs at my tip; 3 in 60 with `dagql` checked out at `9ea94cb1ef`, which is byte-identical to the batch 6 head for that directory. So it is not the rule, the site names or the test-store stand-ins. It was invisible because this test never ran unprivileged, and the privileged runner used `-p=1`.

**Options.**
- **A (implemented).** An unready candidate is not selectable in that scan. If it is the receiver, the scan answers a reselect (`scan: receiver not ready`), because the holder is another capture or a sibling's publication and ends soon. If it is another row it is passed over, because that row can stay unready for as long as its own evaluation runs, and the demand takes its other routes; this is what already happens to a busy Ready donor.
- **B.** Keep the selection and re-probe the source row in `installChainPart` when its record is empty. Leaves the unvalidated-record shape in place and spreads the repair over two functions.
- **C.** Make `prepareScopedPartRecord` skip the donor record for a downloadable source whose part is not Container metadata, the only installer that reads it (`core/part_store.go:68`). Smallest, but it lets a zero record keep flowing through Prepare and Commit.

I chose A because it restores the invariant "a selected source carries its row's validated record" at the one place that can break it. Its behavior change: under that contention a demand now rescans, or passes over a busy row's offer and may run its Lazy operation where a download was possible. Before, it failed. After the fix the concurrent test passed 80 of 80.

Tests: `TestPartSourceScanSkipsUnreadyCandidate` (both shapes, then recovery) and `TestPartSourceScanSkipsUnreadyDonor`, both failing before the fix.

## Commits

| Commit | Kind | What it does |
| --- | --- | --- |
| `12fac103f4`, `d92e8f1c87`, `6b7c8fae91`, this one | evidence | G5 and the conversion table; the reselect audit; its addendum; this report and the ledger's logs. Dropped before publication. |
| `e4b65210ea` | test helper | `engine/snapshots/testutil`: in-place applier, differ and `Root`; the probe and its skip deleted. `inplace.go`'s comment states what stays real, what these tests no longer exercise, and the `blobs.go` fallback. |
| `397168d119` | tests | `core`: `executionFixture` without its mount namespace; helpers and byte checks read in place; mode-0 files checked by mode and time. |
| `b073363f07` | tests | `core/schema`: the same, with `demanded_read_test.go`. |
| `186de94c3b` | tests, convert | `TestLazyEvaluatedFilesystemClones` on a rootfs view and a blob. |
| `a25e63fe31` | tests, convert | The empty-patch subtest. **Outcome differs from the table's first choice**: `AsPatch` mounts even for scratch inputs, so the subtest now asserts the operation-free File encoding on a File built directly. It no longer shows that `AsPatch` returns such a File. A coverage reduction for the coordinator to accept or refuse; the alternative is a row in a native Changeset test, which cannot see `Lazy == nil`. |
| `9d9fb9d6fb` | tests, split | `TestValueTransferPartsSelectedChain` on a view built already evaluated; the real view is `TestHostInputs`'. |
| `50a5d00814` | tests, split | `TestBuiltinMetadataSelectors` keeps its metadata; the bytes are `TestPipeline/Cold`'s. |
| `43986a201d` | tests, convert | `TestLazyStoredResultsWithoutBacking` with stub producer resolvers. The stubs were not ugly; no fold. |
| `9ea94cb1ef` | tests | The four folded git tests behind `foldedIntoNative`, an unconditional skip naming `TestGitTrees`. |
| `e094252906` | production, no behavior change | Every one of the 56 reselect returns names its site through `partRefusal`; the four loops that lacked batch 6's warning carry it. |
| `f9db98a420` | production | The progress rule: `partChanged`, the demand's record, `PartNoProgressError`. |
| `29b836c4e3` | tests | F3: the two umask controls run in a child process with a 30 s deadline; the parent requires the child to pass and to have run the body. I checked that a failing child fails the parent. |
| `959054a2e0` | production correction | The scan defect above. |

Between `e4b65210ea` and `9ea94cb1ef` the `core` package is red unprivileged by construction: tests that used to skip now run, and they need their package's edits.

## Deviations, each for a ruling or a note

1. **The interim skip.** "Delete no folded test until A's case exists" and "the six packages pass" cannot both hold unless the four git tests skip meanwhile. The skip is unconditional, so it is not a privilege skip, and the ledger lists it by name. They never ran unprivileged before either.
2. **Six recording sites, not eight**, and **no site records on `OutputRevision`**. Both are in the audit's addendum with the reasons. Both cost coverage only.
3. **A fourth loop applies the rule**: the Lazy decision's two scans, because after the second it returns its own refusal, which names no counters.
4. **One nil-off test hook added**, `testBeforePartCommit`, at the top of `CommitReadyPart`. Author A's `FixtureBeforeCommit` barrier sits a few lines below it and gives no access to the preparation. At integration one can wrap the other.
5. **The empty-patch subtest**, above.

## Ordinary behavior changes

- `959054a2e0`: selection under contention, as described. Nothing else changes when no row is unready.
- `f9db98a420`: a demand that is refused twice at one counted site with the counters unmoved fails with `PartNoProgressError` instead of retrying. By the argument in the audit this cannot happen unless a site's expectation is wrong.
- `e094252906`: error text of a reselect refusal now includes its site. No caller compares the text.

## Limits to carry into the batch report

- The real-store tests prove no byte obligation that passes through the stand-in applier, differ or reader. Export bytes stay with `TestHostInputs`, applied-layer bytes with `TestPipeline`.
- `engine/snapshots/import_test.go` shows `ImportChain` staging a failing apply or write (F1, in process). The production applier's own failures are `TestPipeline/FailedChain`'s malformed-archive subtest's.
- A mode-0 file's bytes are not read; its mode and time are.
- `core/file.go:316` and `core/directory.go:338`: the named open item in the audit, with the designer's two constraints.
- The rule does not see a deterministic refusal at a busy or ended site; the warning does, and now says where.

## Pre-existing slop met

- `core/lazy_operation_execution_test.go`: the eager mount namespace for 24 tests, one of which needed it. Removed.
- The same file's two subtests that changed the process umask. Moved to a child process.
- `producedFileContents` read a mode-0 file, which only root can. Handled.
- `core/schema/foreign_module_context_test.go` is not gofmt-clean at the base. Not mine, not touched.

## Ledger

All unprivileged, uid 1000, default parallelism, `-count=1`, one invocation per package per variant, packages of a variant in one command. Wall times include compilation; the `-timeout` bounds only each test binary. Per-test results: `logs-author-b/`. The four G5 measurement runs are in `g5/`.

| # | Tree | Command | Bound | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 1 | `e4b65210ea` contents, before commit | `go test -json ./dagql ./engine/snapshots ./engine/engineutil ./engine/engineutil/imageexport` | `-timeout 60s` | 911 pass, 1 skip (pre-existing TODO) | 36.5 s |
| 2 | `9ea94cb1ef` contents | `go test -json ./dagql ./core ./core/schema ./engine/engineutil ./engine/engineutil/imageexport ./engine/snapshots` | `-timeout 60s` | 2698 pass, 5 skips (4 folded, 1 TODO), 0 fail | 22.6 s |
| 3 | `f9db98a420` contents | `go test -json ./dagql ./core ./core/schema ./engine/server` | `-timeout 90s` | 2898 pass, 5 skips, 0 fail | 44.0 s |
| 3r | same | `go test -race -run '^(TestPartProgressRule\|TestPartVersionRefusal\|TestCommitReadyPartChangedRefusals\|TestPublishEvaluatedParts\|TestDemandPartStopsWithoutProgress\|TestPartReselectWatch\|TestPartRefusalNamesItsSite)' ./dagql` | `-timeout 120s` | ok, 2.7 s of tests | 65.6 s |
| 4 | `6b7c8fae91` | the seven packages | `-timeout 60s` | **FAILED**: `core` `TestPartInlineAddress/concurrent`; 2999 pass | 26.8 s |
| 4a | `6b7c8fae91` | `go test -count=40 -run '^TestPartInlineAddress$' ./core` | `-timeout 170s` | diagnostic: 2 of 40 fail | 67.8 s |
| 4b | `6b7c8fae91` with `dagql` at `9ea94cb1ef` | same, `-count=60` | `-timeout 170s` | diagnostic: 3 of 60 fail, so the failure predates my `dagql` changes | 135.6 s |
| 4c | `6b7c8fae91` plus a temporary stack in the error | `-count=60 -run '^TestPartInlineAddress$/^concurrent$'` | `-timeout 170s` | diagnostic: the call sequence above; file restored | about 90 s |
| 4d | `959054a2e0` contents | `-count=80 -run '^TestPartInlineAddress$/^concurrent$' ./core` | `-timeout 170s` | 80 of 80 pass | 55.1 s |
| 5 | `959054a2e0` | `go test -json ./dagql ./core ./core/schema ./engine/server ./engine/snapshots ./engine/engineutil ./engine/engineutil/imageexport` | `-timeout 60s` | **3003 pass, 5 skips (4 folded, 1 TODO), 0 fail**; slowest package `core/schema` 17.4 s | 61.6 s (full rebuild) |

Narrow development selections, each `-timeout 60s` and under 10 s of test time, are not listed one by one: each converted test once after its edit, the two loop tests once with the rule disabled to see them spin to their 5 s deadline and fail, and the writer test once with a wrong expectation to see a failing child fail the parent. No engine run was made or needed.

Skips in run 5, by name: `core` `TestGitLazyOperationsEvaluate`, `TestGitLazyOperationsRemoteEvaluate`, `TestGitBundleLazyOperationEvaluate`, `TestValueTransferPartsGitTrees` (folded into `TestRemoteCacheTransferSuite/TestGitTrees`); `dagql` `TestCacheContextCancel/last_waiter_canceled_fn_returns_value_still_releases` (a TODO at the base, `dagql/cache_test.go:2289`). No privilege skip remains.

## Open questions

1. Option A for the scan defect, or B or C?
2. The empty-patch subtest's reduced claim: accept, or fold a row somewhere?
3. The interim unconditional skip: accept until `TestGitTrees` lands?
4. Should the hook list author A sends include `testBeforePartCommit`, so that one wraps the other at integration?

# Slice 1 corrections and item 3, 17 September, later

Implementation tip `af2ddb4e36`. Authority: the slice 1 consolidation at `c8ece27c7e`, the coordinator's answers to this report's four questions, and design section B6.

## Rulings received

Scan defect: option A stands, accepted by every seat. The empty-patch subtest's reduced claim is accepted permanently: that `Changeset.AsPatch` returns a File with no operation is not covered by any test, and `ChangesetSuite/TestChangesAsPatch` carries the patch bytes. The interim skip on the four folded git tests is accepted until slice 2 only; none may remain at slice 3. `testBeforePartCommit` stays because its tests need the preparation itself, and at integration author A's `beforeCommit` reach wraps it at one call site.

## Corrections

| Item | Commit | What changed |
| --- | --- | --- |
| C6 | `6e2d9a780c` | The umask child's `-test.timeout` is 20 s under the parent's 30 s deadline, so a hung child prints its stacks before it is killed. |
| C7, the invariant | `af2ddb4e36` | The comment beside `PartDemandState.refused` states that a loop which records a refusal never returns it onward into another loop recording into the same demand state, and says which loop records what. I checked each: `publishEvaluatedParts` never returns the class; `installChainPart`'s loop returns only `chain: offer owner not allowed` or `chain: reacquire not granted`, both uncounted; the decision's two scans record what `InstallReadyPart` or `installChainPart` return and then return `decision: both scans refused`; a `CheckPartSources` refusal leaves the decision before the scans record anything and is recorded once by the acquire loop. |
| C7, the test | `af2ddb4e36` | `TestInstallChainPartRecordsOnce`: a counted Commit refusal inside `installChainPart` inside an obtain Body over a real store leaves exactly one record, and the install then succeeds. `TestInstallChainPartStopsWithoutProgress`: the wrong-expectation shape through the same loop returns the hard error out of the Body with `Loop: installChainPart`. |
| C8 | below | Every invocation now lists its process bound beside its test timeout. |

**C7, the coordinator's question.** No: no recording site reachable from `installChainPart` compares a source-row counter, so a donor counter that moved once cannot produce `ErrPartNoProgress` there. The loop's rebuilt lease (`cache_part_content.go:628`) is downloadable and carries no source row, no `version` and no `facts`. Of the six recording sites, `commit: donor version` and `commit: donor facts changed` are both inside `source.readiness == PartReady`, `commit: delegation child version` needs a delegation, which the rebuilt lease does not have, and `scan: candidate version` is not in this loop. What remains is `commit: receiver version` and `commit: receiver representation`, which compare the receiver's payload revision captured afresh by each round's `PrepareReadyPart`. The reused `offerRev` is read only by the exhaustion key (`cache_part_content.go:558`), never by a recording site. `TestInstallChainPartRecordsOnce` moves the donor's payload, offer and ownership counters in the same step as the receiver's and asserts one record, keyed by the receiver, and a successful install.

## C9: statements for the record

- **Decision 6's two limits.** A real-store test proves no byte obligation that passes through the stand-in applier, differ or reader: export bytes stay with `TestHostInputs`, applied-layer bytes with `TestPipeline`. And F1 in `engine/snapshots/import_test.go` proves `ImportChain`'s staging of a failing apply or write, not the production applier's own failures, which `TestPipeline/FailedChain`'s malformed-archive subtest carries.
- **The scan fix's cost.** Under contention on a row, a demand now rescans when the receiver itself could not be captured, and passes over another row that could not, which can run a Lazy operation where a download was possible a moment later. Before, the same contention failed the demand.
- **The stand-in's size.** `engine/snapshots/testutil/inplace.go` is 253 lines, 40 of them the comment that states its limits. My table's "about 130" was the experiment before it followed containerd's applier and differ step for step.
- **`scan: receiver not ready`** is an ordinary refusal: it names no counters, so the rule never records it.

## Item 3

| Commit | What it delivers |
| --- | --- |
| `6333323d56` | The four `Triggers` observations the designer listed as owed, in process on the batch 6 test family. Rollback inside the indexing interval, by a structural ref collected through the existing `testBeforePublicationIndex` point: the flush still queues the class, holding the survivors and never the removed row. Attachment failure after the early flush, by a value whose attachment hook fails: the cohort's hold is the row's only owner, the pass plans nothing, and releasing the cohort collects the row. Congruence repair: uniting two parents fills an imported child from its sibling. An import and a lookup each queue a class and return while the worker is parked in a pass; exactly two items wait behind it. No new hook: the fault points are an existing field and the test value's own method, and the pass barrier is batch 6's helper over `testBeforeSharePass`, which author A's `reachSharePassTaken` wraps. 20 of 20 repeated runs passed. |
| `13cb401c51` | F2, the decoder closure: a Service whose Module has a source, a context source, a runtime Container, a dependency Module, and object, interface and enum type definitions, each a hand-attached row, restored encoded and decoded under the marker. No guard trips, the pure factory is asked once per Module (twice), every reference resolves to its exact persisted row. Batch 6's minimal test now shares the two-lives helper. Not covered, as decision 5 says: what only a real SDK load puts in those rows. |

**F2's two leader orders are not delivered yet, and I need a decision.** The orders need a real signal that the second party has *joined* the first's shared decode attempt before the first is released. Without it a test passes vacuously: a joiner that arrives after the attempt finished takes the fast path and everything it can observe is identical. The kernel's join point is `testPersistDecodeJoined` (`dagql/cache_persistence_import.go:707`), an unexported field, and the test must live in `engine/server`, because `dagql` cannot import the Service and Module decoders. Author A's exported barrier mechanism is reachable from `engine/server` and already has `decodeCopied` and `decodeBeforePublish` on the leader's side, but no point on the joiner's. Options:
1. **Recommended.** Add one point, `decodeJoined`, to A's mechanism at that line, wrapping the existing field as A's other points do. The test then arms `decodeCopied` on the leader and `decodeJoined` on the joiner by row, waits for each to be reached, and releases joiner then leader. I write the test on the integrated branch at slice 2. Either A adds the point or I do at integration; it is five lines in A's pattern.
2. An exported setter for the join hook on my branch now. It would be a second mechanism beside A's and would be removed at integration. I advise against it.
3. Deliver the orders only in `dagql`, with a test family standing in for the decoders. `dagql` already tests the shared attempt's join mechanics that way, so this adds nothing about the real decoders, which is the point of F2.

## Ledger, continued

Bounds: "test" is `-timeout`, "process" is the bound on the whole command, compile included. Runs 1 to 5 above were made with process bounds of 240, 300, 400, 400 and 400 s (3r: 600 s; 4a to 4d: 400 to 500 s), which is looser than the rule asks; from here the process bound is the test timeout plus at most 120 s for compilation.

| # | Tree | Command | Test / process bound | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 6 | `af2ddb4e36` | `go test -json ./dagql ./core ./engine/server` | 60 s / 180 s | 2392 pass, 5 skips (4 folded, 1 TODO), 0 fail; slowest `core` 18.1 s | 59.5 s (rebuild) |

Narrow development selections since run 5, each 60 s / 300 s, under 2 s of tests: the four trigger tests, once each while being written and once as `-count=20` (120 s / 300 s, 1.2 s); the two marked decode tests (90 s / 400 s); the two `installChainPart` tests; the writer test after C6. `engine/snapshots`, `engineutil`, `imageexport` and `core/schema` are untouched since run 5 and were not rerun.

# Integration, the leader orders and the owed-bookkeeping finding, 17 September, later still

Branch `b7-integration-author-b`, author B's branch merged onto author A's `2e452a72de`. Commits: `9c21c9798b` and a second merge (no textual conflicts either time); `5f7537bfce` `reachBeforeCommit`, one site for `testBeforePartCommit` and A's `beforeCommit` barrier; `1e20b211c6` repair of batch 6's `TestSnapshotSharingCancelAfterPublicationDeliversReceipt`, which failed 5 of 5 on A's tip alone because A's `prepareDone` and `beforeCommit` points now meet the fixture mutex that test holds; `5a7b7d1cce` F2's two leader orders in `engine/server` on A's `decodeCopied` and `decodeJoined` points, 20 of 20; `37518e0769` the test below.

## A's finding: a read after a failed sharing sync retries no bookkeeping

**Not a production gap in the read path. The read is served by another row.** Reproduced in process (`core`, real store, A's `failOwnerAttachAfter` fault): after the failed sync R's events are `[installed-ready owner-sync]`. Then:
- `LoadResultByResultID(ctx, "b", srv, R)`, which is what a client's `Ref[Directory](R)` does, returns **row 1, the local donor**, not R. A session load of a handle is an ordinary lookup, the donor is a complete equivalent, and equivalent results are interchangeable (G8). `Evaluate`, `file("notes.txt")` and the bytes all come from the donor. `evaluateResolved` is never called for R; R's events do not change.
- `LoadResultByResultID(ctx, "", srv, R)`, the exact load, returns R. `Evaluate(R)` goes `evaluateOne`, `usesPartAcquisition` true, `evaluateAcquiredScope`, `demandPart(R, snapshot)`, sees `PartOutputInstalled`, `joinPartInstallation`, and the owning continuation retries: events become `[installed-ready owner-sync owner-sync settled owner-sync]`, with no selection, read, evaluation or second install.

That also explains the contrast A saw: a demand-owned chain install has no local equivalent, so the next read is served by R itself and pays the debt.

**What stays held meanwhile.** The installed part's protection (one transient snapshot pin) and the task's continuation are retained until the bookkeeping succeeds (`cache_part_task.go:76..90`: protections are released only after `synced`). Batch 6's `TestSnapshotSharingFailedFinishAndRetry` asserts exactly that retention, and `FailedFinishLastOwner` that collection of R releases it once. So while a complete local equivalent serves every session read, nothing retries R's bookkeeping, and one pin per failed slot stays until R is demanded exactly, R is collected, the cache closes, or the engine restarts (A observed the restart recovering it). A later sharing pass does not retry it either: it sees the part Busy and skips it.

**Options.**
1. **Recommended: no production change; reshape the native subtest.** Release the donor's owners before the read (session end and edge drop, as `TestSharingDonorRestart` already does), so R serves the read, and assert the bookkeeping-only retry then. Name the retention as a limit: bounded to one pin and one continuation per slot whose sync failed, on a storage-failure path, ended by exact demand, collection, close or restart.
2. Let the sharing worker retry owed bookkeeping when a later pass meets a part that is Installed and not settled, by joining its installation as a demand does. It would end the retention without a demand, but it gives sharing a retry of its own, which the design's section 4 refuses ("no continuation state"); a design decision, not a correction.
3. Make a session load that resolves a handle to an equivalent also wake the exact row's owed bookkeeping. I advise against: it makes an ordinary lookup do work for a row it did not select, against G8 and "ordinary engine behavior unchanged".

## Ledger, continued

| # | Tree | Command | Test / process bound | Result | Wall |
| --- | --- | --- | --- | --- | --- |
| 7 | `5f7537bfce` contents | the seven packages | 90 s / 210 s | **FAILED**: `dagql` `TestSnapshotSharingCancelAfterPublicationDeliversReceipt`; 3031 pass | 89.1 s |
| 7a | A's tip `7356f6d6bc`, detached | `-count=5 -run` that test, `./dagql` | 120 s / 300 s | diagnostic: 5 of 5 fail, so it is A's | 52 s |
| 7b | `1e20b211c6` | `./dagql` | 90 s / 210 s | ok | 18.3 s |
| 8 | `37518e0769` | the seven packages | 90 s / 210 s | **3041 pass, 5 skips (4 folded, 1 TODO), 0 fail**; slowest `core` 40.8 s, about twice its usual time, the host being busy | 90.5 s |

Narrow selections, each 60 to 120 s / 300 to 400 s: the leader-order test once and `-count=20`; the owed-bookkeeping reproduction several times while tracing, twice with temporary prints in `dagql` that were restored, and `-count=10` in its final form.

# Later again: the renewal panic, and the two confirmation findings

- **Renewal panic, batch 5, fixed in `e844245c8b`** on the integration branch. `Provider` cloned `offer.Chain.Addresses`; a clone of nil is nil, and the first successful renewal of a key-only offer assigned into it (`cache_part_content.go:330`). `Provider` now always makes the map. `TestRenewalChainControls/a key-only offer renews into its first addresses` panics at that line without the fix. I looked for the pattern elsewhere: it is the only `maps.Clone` in `cache_part_content.go` and `cache_part_renewal.go`; every other map those files assign into is made at construction or guarded (`renewed`, `exhaustedContent`, `renewals`, `exchanges`). The four other `maps.Clone` calls in `dagql` are read-only copies or clones of maps that `NewClass` makes non-nil.
- **Generic F2, "the decode-join test hangs in cleanup after an assertion fails", is author A's test** (`dagql/cache_fixture_control_test.go`). My leader-order test does not have the shape: both releases are idempotent and deferred before either load starts, both barriers are armed before the first load, test defers run before the `t.Cleanup` that closes the cache, and that Close has a fresh 10 s context.
- **Simplification S4: fixed.** `LEDGER-AUTHOR-B.md` lists every invocation of this batch with its exact command, both bounds, result and wall time, from the session record, with `unknown` where a time was not captured. Nothing was rerun for it.

# The models, the clone defect, and the test that was green for me

Integration branch tip `7fff2db12f` plus this evidence commit.

## `TestSnapshotSharingCancelAfterPublicationDeliversReceipt`: it ran, and it was green because I had repaired it

It is neither flaky nor skipped. It **failed** in my run 7 (`5f7537bfce`, the first merge of A's `7356f6d6bc`, which already contains `87d62517c2`), and 5 of 5 on A's tip alone; I reported that then. I repaired the *test* in `1e20b211c6` (install its fixture after publication), and run 8, the 3041-pass run, includes that repair: the ledger's per-test record shows it passing in 0.7 s. A later fixed the *cause* in `96610fe471`. With A's tip `5080ec1468` merged I reverted my repair (`0b934532bc`) and batch 6's test passes unchanged, 10 of 10. One failure, two fixes, A's is the right one and is the only one left.

## `file must be materialized, got lazy *core.FileRestoreLazy`: reproduced, clearly (a), fixed in `1bfece3b77`

**Reproduction, in process.** An imported File row on B with a complete local equivalent; a sharing pass installs its snapshot while it is encoded; the exact row is loaded and `Evaluate` succeeds. The value then has its snapshot set, `Lazy = *FileRestoreLazy`, and `IsEvaluated() == false`, and `cloneDetachedFileForContainerResult` refuses it with A's exact message. The Directory clone refuses a `*DirectoryRestoreLazy` the same way.

**Cause.** A is right about the guard and the mechanism is wider than the fallback. A File or Directory decoded from a record in snapshot form carries its `stored` descriptor and a restore operation. On a row that uses part acquisition, `Evaluate` goes to `demandPart`, finds the part complete and calls `OpenPart` (`core/part_open.go:12`), which sets the snapshot accessor directly. The restore operation never runs, so it is never marked evaluated. Any Container operation that clones such a value fails, chain failure or not; the retained-exec fallback is simply the first thing that mounts an imported File into a Container on B.

**Audit of every `IsEvaluated` reader in `core`** (none in `core/schema`, `dagql`, `engine`):

| Site | What it decides | Affected |
| --- | --- | --- |
| `container.go:878`, `:920` the two clones | hard error "must be materialized" | **yes**, fixed |
| `directory.go:2424` `materializedDirectorySnapshotAndPath` | hard error "still lazy" | **yes**, same shape, fixed |
| `stored_snapshot.go:78,94` `HasPendingLazyComputation` | `stored == nil && Lazy != nil && !IsEvaluated()` | no: it already exempts a value with a stored descriptor. This is the definition the three sites above should have used |
| `file.go:147`, `directory.go:158`, `container.go:1118`, `container_parts.go:490`, `container_persistence.go:221` `LazyEvalFunc` and routing | whether to return an evaluation function | no: they route to the part host, which opens the part; never an error |
| `filesystem_output.go:39,66`, `part_store.go:374,665` | whether the persistence guard must try-lock the body latch | no: a try-lock more, never an error |
| `container_persistence_debug.go:82`, `container_exec.go:134` | a debug field; a delegating accessor | no |

**Why (a) and not (b).** The three sites ask "has the operation run" when they mean "is computation still pending", and `core` already defines the second: `HasPendingLazyComputation`. The fix is that one call at three sites; each still requires the path and snapshot accessors to be set, so an unmaterialized value is refused as before. (b), marking the restore operation evaluated when a part is opened, would write `LazyState` from `OpenPart` without the body latch and change what the persistence guards lock; more reach for the same result.

Test: `TestPartAcquiredValuesCloneForContainers`, File and Directory, fails before with A's message. A's native `RetainedExec` should now pass unchanged; I have not run it.

## Models against the designer's B7

- Each header now lists its assumptions beside the `CacheLifecycle` invariant that discharges each (`OwnershipExact`, `NoUnderflow`, `NoResurrection`, `LazyMutualExclusion`, `LazySuccessPermanent`, `RequiredExact`, `FlushCleanCapture`, `FlushReferentialIntegrity`; I checked each name exists). Two named gaps with no such invariant: batch 6's A2 (one reporter per slot, a pass does not end while a Body owns a preparation), carried by batch 6's three cancellation tests; and "a process that dies without its checkpoint boots into a reset", carried by Go's reset tests and native `TestEncodedRestart/LocalRestoreReset`.
- B7's new fault, release members before queueing the successor: already `remote_sharing_fault_decrement_before_successor`. B7's new probe, a decoded receiver filled over two passes with no ordinary donor owner: my probe did not require the donor to be unowned; it now requires the owner to have gone during the first pass, and it is reachable.
- B7 asks for the progress rule in `remote_parts`, which I had left out. It is in: the invariant `NoProgressIsUnreachable` on the code as it is, the fault `WrongExpectation` that must violate it, and a second configuration showing that under the fault the hard error comes only on the second refusal. **One limit, stated in the header:** in these bounds no other actor can move the receiver's revision between a preparation and its Commit, because one operation publishes every pending part at once, so a legitimate counted refusal is unreachable and the invariant is not tested by real contention here. Go's `TestPublishEvaluatedPartsSurvivesContention` is what shows that.
- B7's ownership core lists a task generation, joiners, `NoJoin` and a Body outliving a cancelled wait. The modules model joiners and cancellation at waits; they do not model generations, `NoJoin` or an abandoned Body that keeps running. Those stay with batch 4's task tests.
- 35 configurations now; all match `expectedOutcome` (run M16).

# Export during a held sharing pass (dispatch `d504219373`)

**Not a defect, and no non-fixture caller can see it today. No production change.**

**Call sequence.** `WithExportedValues` (`dagql/cache_value_capture.go:339`) takes holds on the selected closure (`holdTransferClosure`), then `capture.copy`, which calls `captureHeldPersistedRecord` for each row (`dagql/cache_persistence_capture.go:72`). That function refuses a row with any attempt in flight: `shared.lazyPartGroups[key].attempt != nil` gives `persist state not ready: result N group "<key>"`. A sharing slot runs as the task `obtain:<part>`, so while a pass holds a prepared slot the row has such an attempt, and the message A saw, `group "obtain:mount:/work"`, is that line. The export is a capture: it demands nothing, publishes nothing to R (the withheld subtest's comment says it "publishes to R"; it does not), and on refusal releases its holds and returns.

**It is batch 2's contract, not sharing's.** `CapturePersistedRecord`'s comment states it: "A row being evaluated reports ErrPersistStateNotReady". The checkpoint worker treats it as skip-and-retry-later (`cache_persistence_worker.go:260`), `OfferParts` returns it as the `OfferUnavailable` disposition (`cache_offer.go:243..259`), and batch 5's `cache_offer_matrix_test.go:109` asserts exactly that. An ordinary demand's `obtain` or `lazy` task on the row gives the same answer for as long as it runs, which for an exec is minutes, far longer than a pass's window. `TestExportDuringAnActiveTaskIsNotReady` (committed with this note) shows both: a pass paused at `beforeCommit` and an ordinary task refuse the export identically, the refused export keeps no hold, the pass is undisturbed, and the export succeeds once the task has ended.

**Who can call it.** In this repository `WithExportedValues` has two callers, both the gated fixture (`core/schema/remote_cache_fixture.go:327`, `remote_cache_fixture_control.go:332`). The real integration's surface, `RemoteCacheAdapter` (`engine/server/remote_cache.go`), has `OfferParts`, `TakeRenewalRequest`, `ReplyRenewal` and `Stop`: no export. So nothing but the fixture can meet this today. When a real exporter is added it will meet it for every row that has work in flight, with or without sharing.

**Options.**
1. **Recommended: a documented transient that the caller retries**, which is what the two existing consumers of this sentinel already do. The test's exact operation should retry on `ErrPersistStateNotReady` until the pass is released, or take its export before the hold. I would also state the contract on `WithExportedValues`' own comment, which today says nothing about it; that is a one-line documentation change I have not made without your word.
2. Make the export wait for the row's tasks as a demand does. I advise against it: the capture is deliberately non-blocking (`captureHeldPersistedRecord` holds `lazyMu`; `core/container_persistence.go:120` says why a live capture must not wait for a body that may need the cache), it would make an export block for the length of any exec on any row of its closure, and it is the kind of new wait on converged code that B4 ruled out.
3. Have the fixture's `exportSelected` retry internally. It hides the contract from the test that should know it.

# Folded git tests deleted; the Workspace constructor question (dispatch `162d4fe1dc`)

**Folded tests.** A's `d70534a46a` merged (`ee7d756b85`). `TestGitTrees` names `Local`, `LocalCleaned`, `LocalBundle`, `RemoteDownload` and `RemoteFallback`, so `core/git_lazy_test.go`, `TestValueTransferPartsGitTrees`, the git server stub and `foldedIntoNative` are deleted. Seven packages: 3050 pass, 0 fail, one skip, the base's TODO. No test skips for a folded row or for privilege.

**Workspace: no production finding. A's test exports the wrong root, and its expectation for the constructor should be "runs".**

What a Workspace contributes to identity, from the code:
- `currentWorkspace` is registered `WithInput(dagql.PerCallInput)` (`core/schema/workspace.go:32`), and `PerCallInput` mixes `identity.NewID()` into the call (`dagql/cache_inputs.go:72`). A Workspace result's identity is unique per invocation: not per client, not per host path, not content. Any call that takes it as an argument has a recipe that never matches another call's, on one engine or two.
- `Workspace.directory` is `WithInput(dagql.PerClientInput)` on that per-call receiver (`workspace.go:89`), so its recipe is per call as well. The Directory it returns is a host load and carries a content digest.
- The design's mechanism for exactly this is in `core/modfunc.go:947`: a module function that has Workspace arguments and returns a module object gets, on its result, a content digest over everything the object holds (`CollectContent`, each field by its `ContentPreferredDigest`), labelled `ExtraDigestLabelRemoteCache`, so it survives export (`transferExtras` keeps digests marked with that label).

So the constructor always re-runs on B. Its result then unites with A's imported Project by that content digest, if the two are content-identical, and a downstream call on B's Project has the same recipe as A's, because a receiver contributes its content-preferred digest.

Why A's case gets no hit regardless: it exports `made.ID`, the **Project** (`remote_cache_workspace_test.go.withheld:96`). An export's closure follows dependencies (`holdTransferClosure` walks `res.deps`). `describe`'s result depends on the Project, not the reverse, so it is not in the bundle, and B has nothing of `describe` to hit. `TestPipeline` hits because it exports `built.ID`, the result of the method whose body it expects skipped.

In process, `TestTransferConstructorContentUnitesDownstreamCall` (`dagql`, committed) shows both shapes with two constructors whose recipes never match and whose results share a remote-cache content digest: exporting the method's result, the method's body runs 0 times on B; exporting only the constructor's result, it runs once.

**The true statement for §5's row:** the constructor re-runs on B (its body entry on B is 1, not 0); its result unites with the imported aggregate by the remote-cache content digest of what it holds; a downstream method **whose result was exported** is skipped; the fields' owner links are the receiver's own after install. "Ordinary B constructor aggregate matches" holds in that sense and only if the aggregate's content is identical.

**What A should change.** Export the downstream method's result. `describe` returns a String, which has no ID to export by handle, so make the skipped method return an object (a File with the description, or a small module object) and export that; keep a changed-argument control. Expect `New` to run on B.

**What I could not check without the engine, and A should log:** whether `Project`'s content digest is really equal on A and B. `Source` is a host directory and contributes its content digest; `Built` is an exec result with no content digest, so it contributes its recipe digest, which is content-based only if every input on its chain is (the mounted `Source` is; a base image by digest is). If the two checkouts differ in file modes, or the module's exec chain takes anything per-client, the digests differ and nothing downstream can hit. A can read the `remote-cache` extra digest of `project`'s ID on both engines before asserting anything else. If they differ with identical bytes and modes, that would be a production finding and I would want the two IDs.

# Slice 2 corrections (consolidation `d1e12d9314`)

| Item | Commit | What changed |
| --- | --- | --- |
| D10 | `dagql: assert zero transient pins…` | `TestSnapshotSharingPrefixCarriedOverRefusedAddress` reads the manager's released count at the end of the first pass: two pins taken for the two installed addresses, two given back, none for the refused address. 20 of 20. |
| D11 | `dagql/tla: model the retained operation…` | The seat was right that the probe showed an operation assumed available. The retained operation is now a bit the checkpoint saves, the restart restores and `RunProducer` requires, with the invariant `OperationRetained` and a fault, `DropOperationAtCheckpoint`, that violates it. The header says the bit is all that is modeled; the operation's bytes and that they decode are Go's. The unread `saved.complete` is gone. I also took the seat's smaller stale-role fault: the revision bypass is removed and `DesiredCoversInstalled` is still violated, by two valid commits. 36 configurations; the 21 sharing and checkpoint ones rerun, 0 mismatches, 34 s. |

**D12, for the record.** While a part's bookkeeping is owed (its owner synchronization failed), the receiver keeps that part's transient pin, keeps its redundant offer unretired, since an offer retires at settlement, and answers an export with `ErrPersistStateNotReady`, because `captureHeldPersistedRecord` refuses a group whose `syncPending` is set. An exact demand of the row, the row's collection, a cache close or a restart ends that state; a session read served by a complete local equivalent does not. Bounded to one pin and one continuation per slot whose synchronization failed. The conversion table's `AsPatch` row now records its outcome.

**D13.** `LEDGER-AUTHOR-B.md` gives, for every invocation of this batch, the tree, the exact command, the real `-timeout` and the process bound beside the measured time, with `unknown` where a time was not captured; nothing was rerun to write it. Where my process bounds were looser than the rule (runs 1 to 5, and the 300 s I kept on narrow selections) the ledger says so.

## Boot wipe: cause found and reproduced in process, no production change yet

Full text in `boot-wipe/FINDING.md`. An imported `CacheVolume`, `RemoteGitMirror` or `ClientFilesyncMirror` row arrives with no snapshot and creates one at first use. Nothing attaches the row's owner lease then; only `HTTPState` does (`core/schema/http.go:173`). The session's context lease is the snapshot's only protection, so after the session ends collection removes it, the checkpoint still saves the link because it reads links from the value, and boot wipes the cache. The running engine is already wrong after the collection: the next mount of that cache volume would fail.

Reproduced for all three kinds with real stores and real collection, same boot error text as A's run. Recommendation: option (a), sync the row's owner leases at the five late-creation sites through one helper, the pattern `HTTPState` already uses. Ledger rows 16 to 19.

## Boot wipe: fixed in `16786b5fe3`, option (a)

`core.EnsureBackingSnapshot` at all five sites; a failed sync fails the call as `HTTPState`'s does and is retried at the next use. `TestImportedBackingSnapshotIsOwnedByItsRow` covers the three kinds, asserts the live fault is gone without a restart and that the restart keeps the cache; it fails for all three with the sync removed. The scratch file is deleted. The commit applies cleanly onto A's `2036dcac4d` (`git merge-tree`). Option (d) is recorded in `boot-wipe/FINDING.md` as a named item for the Human, not done. Ledger rows 21 to 25.

## Slice 3: integration, reruns, the native run, packaging and the manifest

A's final tip `3af661532a` (implementation `9c376138c1`) merged as `0fca47daa7`, no conflicts; each of my three fixes is in the tree once (A's `-x` copies were identical); Commit's `beforeCommit` reach already wraps `testBeforePartCommit` at the one site `reachBeforeCommit`. Seven packages, the `-race` selection and the sixteen-test native run all pass at that tip (ledger 26 to 28; the native run at 822 s on a host at load 37 to 54 with two other engines working). Batch 7 is packaged as `b7-packaging/remote-cache/b7-verification` (`cd3b04e401`, 87 commits) by `manifest/package_b7.py`, a different method from batches 1 to 6 because of the five merges; `manifest/BATCH-7.md` is the record and lists the eight corrections to earlier batches for the Human's fold-back decision. A's report is now `REPORT-AUTHOR-A.md`; `REPORT.md` is the batch index.

## Slice 3 generic review G1: fixed in `5d3ee071c7`

The reviewer was right: a failed owner attach left the created snapshot in the value, unowned. Now the failed call drops it and the value is uninitialised again; six schedules in `TestImportedBackingSnapshotIsDroppedWhenItsOwnerAttachFails` fail without the drop. `core` and `core/schema` pass; the other five packages do not contain the change. Packaged branch rebuilt: `2b34e9e3e7`. Ledger 29 to 32.
