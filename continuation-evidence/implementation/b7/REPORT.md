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

All unprivileged, uid 1000, default parallelism, `-count=1`, one invocation per package per variant, packages of a variant in one command. Wall times include compilation; the `-timeout` bounds only each test binary. Per-test results: `logs/`. The four G5 measurement runs are in `g5/`.

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
