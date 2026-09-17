# Batch 7, author B: G5 result and the real-store conversion table

Author B, 17 September 2026. Base `c5b299142c`. Host: 16 CPUs, uid 1000, no sudo, no unshare, default parallelism. Nothing here is committed as test or production code yet; the experiment is [experiment.patch](experiment.patch) and applies to the base.

## Conclusion

1. **G5 as written fails.** Moving the read-only-mount probe out of `NewStore` lets 12 of 79 store tests pass. Its premise is wrong: export and `core/exec_error.go:174` are not the only mounting sites. Import mounts too, and so does every read through core.
2. **A small test-side change rescues almost all of them.** With a mount-free applier, differ and file reader in `engine/snapshots/testutil` (about 130 lines, test-only), plus reading bytes in place in test helpers and dropping one fixture's eager mount namespace, **70 of 79 pass unprivileged** on the real content store, native snapshotter, lease manager and metadata GC. No production code changes.
3. **9 tests in 6 files still need a real mount**, because the production code under test mounts read-only or enters a mount namespace. Each has a disposition below: 4 fold into author A's native cases, 3 convert, 2 split.
4. **Cost is small.** All six packages, whole, default parallelism: 18 s wall before, 41 s wall with the 58 newly running tests (includes a rebuild of the changed helper). Slowest package 13.8 s.

I recommend the council accept a fourth disposition the packet did not list: *keep on the real store, unprivileged*. It costs one helper file instead of rewriting about 60 dense tests onto a no-storage fake, and it keeps their byte, lease and GC assertions, which the no-storage fake (`dagql/cache_snapshot_sharing_test.go:23`, "a snapshot reference with no storage behind it") cannot carry.

## The count is 34 files and 79 tests, not 32

32 files import `engine/snapshots/testutil`. Two more reach `NewStore` through `executionFixture` without importing it: `core/builtin_lazy_test.go` and `core/lazy_completion_test.go`. I found them because run 1 logs a line at the probe, so the list is what executed, not a grep. 79 top-level tests touch the store. No other test in the six packages skips for privilege; the only other skips are `git not installed` (2) and one TODO in `dagql/cache_test.go:2289`.

## Why G5's premise is wrong: four mechanisms, measured

Run 1 replaces the probe's `t.Skipf` with a log line and changes nothing else. 67 of 79 fail. First-failure causes, by leaf test:

| Mechanism | Leaf failures | Site |
| --- | --- | --- |
| containerd temp mount | 73 | `apply.NewFileSystemApplier` and `walking.NewWalkingDiff` call `mount.WithTempMount`, which issues a mount system call even for a writable bind. Import (`engine/snapshots/pull.go:253`) hits it as well as export. |
| In-test `unshare(CLONE_NEWNS)` | 24 | `core/lazy_operation_execution_test.go:61`, `executionFixture`. Only `core/git_remote.go:278` ever reads `CleanMountNS()`. Every HTTP, mount and builtin test using the fixture paid for it. |
| Read-only bind through `LocalMounter` | 25 | `engine/snapshots/localmounter_linux.go:45`: a writable single bind returns its source path with no system call; a read-only one always mounts. `engine/snapshots/refs.go:643` adds `ro`, and the native snapshotter adds its own for a committed snapshot, so every read of an immutable ref mounts. Reached from `testutil.CheckFile`, test helpers using `core.MountRef`, and production (`File.Contents`, `Directory.Entries`, `Stat`, changeset paths, git checkout). |
| `setns` into the clean mount namespace | 1 | `core/git_remote.go:278` |

Pre-existing slop, named as the rule asks: the fixture's eager namespace (24 tests needing a privilege none of them used except git remote), and a test helper (`producedFileContents`) that reads a mode-0 file, which only root can do.

## The four runs

One `go test` invocation per package per run, all in one command so packages run concurrently; `-count=1 -json`; whole packages, so the union of selections. Per-test results: [logs/](logs/).

| Run | Change | Command bound | Wall | Store tests passing |
| --- | --- | --- | --- | --- |
| 1 | probe logs instead of skipping | `-timeout 240s`, six packages | 17.9 s | 12 of 79 |
| 2 | + mount-free applier, differ, `CheckFile` in `testutil` | `-timeout 240s`, six packages | 41.1 s (rebuild included) | 48 of 79 |
| 3 | + `executionFixture` no longer requires the namespace | `-timeout 120s`, `./core` | 31.8 s | core 22 of 38 |
| 4 | + test-side byte reads in place (`testutil.Root`), two helpers that demand as `File.Contents` does and then read in place | `-timeout 180s`, `./core ./core/schema` | 28.3 s | 69 of 79 |
| 4b | + mode-0 and `public eager layout` reads in the writer test | `-timeout 60s`, `-run '^TestHTTPLazyOperationWriter$' ./core` | 1.1 s test time | 70 of 79 |

Packages `dagql`, `engine/snapshots`, `engine/engineutil`, `engine/engineutil/imageexport` pass whole from run 2 and were not rerun. Package times with everything applied: `core` 13.8 s, `dagql` 13.3 s, `core/schema` 12.1 s, `engine/snapshots` 7.5 s, `engine/engineutil` 0.5 s, `imageexport` 0.4 s. No run hung; no timeout fired.

## What the mount-free test store is

`testutil.Store` already wraps the applier and differ to count and inject faults (`observedApplier`, `observedDiffer`). The change swaps what they wrap:

- **Applier**: same processor chain and digest as containerd's, then `archive.Apply` straight into the bind mount's `Source` directory instead of into a temp mount of it.
- **Differ**: same writer, compressor, uncompressed-digest label and commit as containerd's walking differ, with `archive.WriteDiff` over the two `Source` directories.
- **`Root(t, ref)`**: the directory behind a committed snapshot; `CheckFile` uses it.
- The probe and its skip are deleted.

Kept real: containerd's local content store, native snapshotter, metadata DB, lease manager, `GarbageCollect`, the engine's `SnapshotManager`, tar bytes and layer digests. Not exercised any more by these tests: the two `WithTempMount` calls, `LocalMounter`'s read-only mount, and `File.Contents`/`Directory.Entries` as the byte reader. All of those run in every native case.

Risks I see. The stand-ins repeat about 100 lines of containerd logic, so a containerd behavior change would not show here; native cases use the real ones. `blobs.go:157` hard-codes `walking.NewWalkingDiff` when `NeedsComputeDiffBySelf` is true; unprivileged it fails, logs a warning and falls back to the store's differ, so such a test passes through the fallback rather than the first path. Replacing `File.Contents` with "demand, then read in place" keeps the demand (`Snapshot.GetOrEval`, `File.GetOrEval`, the same two calls `core/file.go:914,921` make) and drops only the mount.

## Conversion table

Dispositions: **keep** = stays on the real store, unprivileged, by the mount-free test store (measured passing in runs 2 to 4b); **convert** = rewrite the mounting step onto something that does not mount; **fold** = author A's native case carries it and the unit test is deleted; **split** = protocol half stays, byte half folds. "Tests" counts top-level store tests. Times are summed test times from the passing run.

### Keep: 28 files, 69 tests, no further work beyond the helper change and the listed test-side edits

| File | Tests | Passing with G5 alone | Passing with proposal | Seconds | Extra test-side edit needed |
| --- | --- | --- | --- | --- | --- |
| `dagql/cache_offer_matrix_test.go` | 3 | 0 | 3 | 1.6 | none |
| `dagql/cache_offer_restart_test.go` | 2 | 0 | 2 | 0.7 | none |
| `dagql/cache_part_admission_external_test.go` | 1 | 1 | 1 | 0.1 | none |
| `dagql/cache_part_boundary_test.go` | 2 | 1 | 2 | 0.7 | none |
| `dagql/cache_part_chain_lifetime_test.go` | 2 | 0 | 2 | 1.2 | none |
| `dagql/cache_part_content_test.go` | 2 | 0 | 2 | 4.1 | none |
| `dagql/cache_part_decode_test.go` | 2 | 2 | 2 | 0.1 | none |
| `engine/snapshots/import_test.go` | 16 | 0 | 16 | 6.5 | none |
| `engine/engineutil/containerimage_lifetime_test.go` | 2 | 0 | 2 | 0.4 | none |
| `engine/engineutil/imageexport/lifetime_test.go` | 1 | 0 | 1 | 0.4 | none |
| `core/part_delegation_mount_test.go` | 1 | 0 | 1 | 0.3 | none |
| `core/part_delegation_test.go` | 1 | 0 | 1 | 1.8 | none |
| `core/part_filesystem_race_test.go` | 1 | 1 | 1 | 0.2 | none |
| `core/part_inline_test.go` | 1 | 0 | 1 | 1.5 | none |
| `core/part_publication_race_test.go` | 1 | 0 | 1 | 0.3 | none |
| `core/part_scope_boot_test.go` | 3 | 3 | 3 | 0.7 | none |
| `core/container_mount_lazy_test.go` | 1 | 0 | 1 | 0.3 | fixture namespace |
| `core/mount_lazy_ownership_test.go` | 1 | 0 | 1 | 0.2 | fixture namespace |
| `core/part_whole_restart_test.go` | 3 | 1 | 3 | 0.3 | fixture namespace |
| `core/http_lazy_test.go` | 4 | 0 | 4 | 0.6 | fixture namespace; 1 byte read in place |
| `core/lazy_operation_execution_test.go` | 6 | 0 | 6 | 1.5 | owns the fixture and `producedFileContents`; mode-0 files assert mode and time, not bytes (the only coverage given up: bytes of a file nobody but root can read; the other three modes read them) |
| `core/part_acquisition_test.go` | 4 | 0 | 4 | 1.6 | 1 byte read in place |
| `core/part_offer_admission_test.go` | 1 | 0 | 1 | 0.2 | 2 byte reads in place |
| `core/snapshot_transfer_test.go` | 1 | 0 | 1 | 0.3 | 4 reads in place |
| `core/value_transfer_restart_test.go` | 1 | 0 | 1 | 0.1 | 1 byte read in place |
| `core/schema/directory_scratch_test.go` | 3 | 2 | 3 | 0.8 | 2 entry reads in place |
| `core/schema/http_lazy_test.go` | 2 | 1 | 2 | 0.3 | 1 byte read in place |
| `core/schema/query_lazy_test.go` | 1 | 0 | 1 | 0.1 | 1 byte read in place |

### The six files that still need a decision: 10 store tests, 1 already passes, 9 do not

| File | Test | Why it still mounts | Disposition |
| --- | --- | --- | --- |
| `core/git_lazy_test.go` | `TestGitLazyOperationsEvaluate`, `TestGitBundleLazyOperationEvaluate` | production local-git checkout and bundle read mount the source read-only | **fold** into native `TestGitTrees`, rows local × Ref/Commit × fallback with saved `DiscardGitDir`, depth and tags; bundle as one more fallback row |
| `core/git_lazy_test.go` | `TestGitLazyOperationsRemoteEvaluate` | `setns` into the clean mount namespace | **fold** into `TestGitTrees`, remote × fallback. The file is then deleted; its helpers move to whichever kept test still uses them (`operationDirectoryResult`, `operationGitSnapshot` have other callers). |
| `core/value_transfer_chain_test.go` | `TestValueTransferPartsGitTrees` (local, remote) | same two causes | **fold** into `TestGitTrees`, local and remote × download |
| `core/value_transfer_chain_test.go` | `TestValueTransferPartsSelectedChain` | `Directory.Subdirectory` and `Subfile` evaluate by a read-only `Stat` | **split**. Unit keeps the export observations (whole parent chain exported, parent snapshot opened once, idempotent release, no forward chain) on a view built already evaluated. The real `Subdirectory` view folds into native `TestHostInputs`, which the design's §5 row already names for "nested view exports whole chain". |
| `core/value_transfer_chain_test.go` | `TestValueTransferPartsContainerMount` | passes | **keep** |
| `core/lazy_completion_test.go` | `TestLazyEvaluatedFilesystemClones` | uses `Subdirectory`/`Subfile` only to obtain an evaluated lazy value | **convert**: take the evaluated Directory from `ContainerRootFSLazy` and the File from `FileBlobLazy`, neither of which mounts read-only (both already pass in kept tests). The subject, clone keeps the snapshot and drops the operation, is unchanged. |
| `core/builtin_lazy_test.go` | `TestBuiltinLazyOperationEvaluate`, subtest `empty patch without operation` only; the body and the other subtest pass | `Changeset` path computation mounts both sides read-only | **convert**: build the empty changeset over scratch directories, which `MountRef` serves from a temp dir with no mount (`core/util.go:237`). If `AsPatch` still mounts, delete the subtest: `ChangesetSuite/TestChangesAsPatch` covers patch bytes natively, and the no-operation encoding is three lines I would move to a pure encode test. I will report which. **Outcome (`a25e63fe31`): `AsPatch` mounts even for scratch inputs, so the subtest now asserts the operation-free File encoding on a File built directly. That `AsPatch` returns a File with no operation is covered by no test; accepted permanently by the coordinator and the scope seat, with `ChangesetSuite/TestChangesAsPatch` carrying the patch bytes.** |
| `core/schema/container_lazy_test.go` | `TestBuiltinMetadataSelectors` (5 subtests); the file's other 5 tests never touch the store | the final demand evaluates `ContainerFileLazy`/`ContainerDirectoryLazy`, which `Stat` through a mount | **split**. Unit keeps every metadata assertion (operation path, platform, fallback, persisted implicit input) and evaluates the parent directly. The byte demand through a builtin container folds into native `TestPipeline/Cold`, which the design already charges with "cold builtin acquisition route observed". |
| `core/schema/lazy_stored_results_test.go` | `TestLazyStoredResultsWithoutBacking` | producer side computes `contents`, `search`, `stdout` with the real resolvers, which mount | **convert**: producer side gets three stub resolvers returning the saved values; consumer side keeps the real schema, so both halves of the claim stay (stored hits need no backing; absent calls demand it and fail with `ErrUnavailablePart`). Fallback if the stub for `search` turns ugly: fold into `TestPipeline/Warm`, "metadata-only zero reads". |

### What I need from author A, through the coordinator

| Native test (A's) | Rows folded into it from unit tests |
| --- | --- |
| `TestGitTrees` | local × Ref and Commit × fallback, asserting the re-evaluated tree equals the eager tree for `DiscardGitDir` on the repository and on the call, depth 1, tags included (from `TestGitLazyOperationsEvaluate`, 8 combinations; A may sample); `cleaned` tree restores tracked and deleted files and leaves the source's index and dirty file untouched; bundle, full and incremental (`TestGitBundleLazyOperationEvaluate`); remote over HTTP × fallback (`TestGitLazyOperationsRemoteEvaluate`); local and remote × download (`TestValueTransferPartsGitTrees`) |
| `TestHostInputs` | a nested view made by real `directory("visible").file("value.txt")` exports its whole parent chain and B reads `selected bytes` (from `TestValueTransferPartsSelectedChain`) |
| `TestPipeline/Cold` | a file and a directory selected from a builtin container by absolute and relative path read `saved` and `["data"]` on B (from `TestBuiltinMetadataSelectors`) |

If A's fixture cannot reach one of these rows cheaply, I would rather hear it now than delete the unit test first. I will not delete any folded test until A's case exists and names the rows.

## End state

`requireNativeMount` and its `t.Skipf` are deleted. `executionFixture` creates no namespace. No file in the six packages skips for privilege, and none needs root, sudo, unshare or a mount namespace. Every kept test runs in the ordinary unprivileged package run that batch 6 already used, so a defect like batch 6's second one (every Lazy publication failing) would have failed `core` and `dagql` in seconds.

## Proposed commits, after the council rules on the table

1. `engine/snapshots/testutil`: mount-free applier, differ, `Root`; delete the probe. Tests: the 16 import/export tests are its proof.
2. `core`: `executionFixture` without the namespace; helpers read in place; the byte-read edits. One commit per package (`core`, `core/schema`).
3. The three converts and two splits, one commit each.
4. Deletions of folded tests, only after A's cases land, at slice 2 integration.
