# Stack manifest completion record, batch 7: integrated verification

Author B, 17 September 2026. Local refs only; **nothing pushed, no pull request, no tag**. `batch-7.json` beside this file has every original commit with its class and its packaged SHA (`null` when omitted). `package_b7.py` builds the branch.

Integrated parent `c5b299142ca672cbd2ef0a389492f11de85ff08b` (batch 6 head). Integrated implementation tip `90e34e09a3f2bf8a22a00c73b5223fe4b3fb795f`, tree `85300db229b1bbd4c18574c2c2608c7fa975dd5b`. 124 commits: 118 non-merge and 6 merges of author A's branch into `b7-integration-author-b`. Of the 118: **90 kept, 25 evidence-only, 3 duplicates**, none mixed. Packaged production and test diff: `git diff c5b299142c 90e34e09a3 -- . ':!continuation-evidence' ':!cleanup-evidence' ':!cleanup-review-evidence' ':!CLEANUP-IMPLEMENTATION.md'`.

## Packaged branch

| Branch | On | Kept | Omitted | Packaged head | Packaged tree |
| --- | --- | ---: | ---: | --- | --- |
| `b7-packaging/remote-cache/b7-verification` | `b7-packaging/remote-cache/b6-sharing` `38583498cf` | 90 | 28 | `bc905aed166e28d13fc0b3f2e380cb74bb3c0b96` | `6402661c41c90e362d35dbbbdde86342b23e6ebb` |

**How it is built, and why not as batches 1 to 6 were.** Those segments are linear, so each packaged commit is its original's tree without evidence. Batch 7 has two authors and six merges; a commit on one author's line does not contain the other's work, so its tree cannot be copied. Each non-merge commit is instead applied as its own change: a three-way merge of its evidence-stripped tree against its parent's, onto the packaged line, in `git rev-list --reverse --topo-order --no-merges` order, on git objects only (`git merge-tree --write-tree`; no checkout, no working tree). Author, author date, message and trailer normalisation are as in `package.py`, so a rebuild gives the same SHAs. All 118 applied with no conflict. The merge commits carry no change of their own, which the final tree comparison proves rather than assumes.

**Omitted.** 25 evidence-only commits (listed in `batch-7.json`), and three duplicates: author A's `-x` copies of my fixes, `397221fe5c` (of `e844245c8b`), `624b48d8dc` (of `1bfece3b77`) and `f51ac7f796` (of `16786b5fe3`). Topological order reaches my original first, so A's copy applies as no change and is dropped; each fix is in the branch once, without a `cherry picked from` line. The repair `1e20b211c6` and its revert `0b934532bc` are both kept: they are real history on the integrated line and cancel exactly.

## Verification

- **Tree comparison.** The packaged head's tree equals the integrated tip's tree without the four evidence entries (the script refuses to write the ref otherwise). `git diff <packaged head> <integrated tip>` names only `continuation-evidence/`, `cleanup-evidence/` and `CLEANUP-IMPLEMENTATION.md`.
- **Trailers.** All 90 messages end with `Signed-off-by`; none has `Co-Authored-By`, a generated-with line or a `cherry picked from` line. No merge commits in the branch.
- **Buildable and tested, by tree identity.** The packaged tree is the tree on which `go build ./...`, the seven packages, the `-race` selection and the integrated native run of slice 3 ran (ledger rows 26 to 28 at `0fca47daa7`, 29 to 36 since; the seven packages pass at this tip, row 37; the native run at this tip, row 38, failed two bodies under host load and is reported, not rerun). I did not check the head out separately: it would be the same files, and the native run was reading the working tree at the time.
- **Not verified:** intermediate packaged commits were not built. Because the order is a linearisation of two interleaved lines, an intermediate commit of one author can precede the other author's commit it was originally merged with; every commit applied cleanly, but I have not shown that each intermediate tree compiles.

## Corrections to earlier batches' code, kept as their own commits in batch 7

The Human decides before publication whether any of these is folded back into the batch it corrects. Each has a test that fails without it.

| Packaged | Original | Corrects | What |
| --- | --- | --- | --- |
| `51d24c1f09` | `e094252906` | batch 4 (acquisition) | every reselect refusal is named by site; all seven retry loops carry a watch and the warning |
| `777e484881` | `f9db98a420` | batch 4 | the progress rule: a loop refused twice at the same site with no counter moved fails with `PartNoProgressError` instead of spinning |
| `af0a3b200f` | `af2ddb4e36` | batch 4 | states and tests that one refusal is one progress record |
| `dcd36f6c96` | `959054a2e0` | batch 4 | `scanPartSources` no longer selects a source from a row whose capture returned `ErrPersistStateNotReady` (the pre-existing `TestPartInlineAddress/concurrent` flake, about 5 %) |
| `3e0a06bca5` | `e844245c8b` | batch 5 (offers) | a key-only offer's first renewal wrote into a nil address map and panicked |
| `6c6cdfd8ac` | `1bfece3b77` | batch 4 and the lazy-values rework | the three clone guards in `core/container.go` and `core/directory.go` rejected a part-acquired File or Directory (`file must be materialized, got lazy *core.FileRestoreLazy`) |
| `f2c36a337f` | `87c099a615` | batch 2 (transfer) | documentation only: `WithExportedValues` never waits, a busy row is a transient refusal |
| `fbd8ca8f17` | `16786b5fe3` | batch 2 (the foreign-uninitialized backing forms) | an imported `CacheVolume`, `RemoteGitMirror` or `ClientFilesyncMirror` created its snapshot at first use with no owner lease; a collection then removed it and the next clean boot wiped the whole cache |
| `2b34e9e3e7` | `5d3ee071c7` | the same | slice 3 generic review G1: when the owner attach fails, the created snapshot is dropped with the failed call instead of staying in the value unowned |
| `3238e5c94c` | `cfa148371c` | the same | creation, sync and discard are one step per value under the helper's own lock, so concurrent first uses cannot drop a snapshot under each other |

Not corrections, but production code in batch 7 all the same: the gated fixture (author A). It is compiled into the engine and inert unless enabled: `dagql/cache_fixture_*.go`, `dagql/cache_part_fixture.go`, `dagql/cache_transfer_fixture.go`, `core/remote_cache_fixture.go`, `core/schema/remote_cache_fixture*.go`, `engine/server/remote_cache_fixture_controller.go`, `engine/fixturetransport/`, and its reach points inside batch 4 to 6 files. `710609554d` makes Commit's in-package test field and the fixture's `beforeCommit` barrier one call site. Author A's report describes the gate.

## Named open items that travel with the stack

- Boot treats one row with a dangling owner link as damage and wipes the whole store; option (d) in `../boot-wipe/FINDING.md`. For the Human. Not done.
- `core/file.go:316` and `core/directory.go:338` map a busy snapshot writer to `ErrPersistStateNotReady`; any change belongs in `demandPart`'s mapping behind a marker, after a full snapshot-writer audit. Not done.
- Splitting batch 4's `core/object.go` commits into their own PR is still left to the Human's pre-publication view, as in `PACKAGED-1-6.md`.
- The long model runs at the design's ceilings have not been run; only the quick bounded configurations have (ledger M1 to M17).
