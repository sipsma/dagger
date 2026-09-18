# Batch 7: integrated verification and stack. Index

17 September 2026. Two authors: A `cl-1bc62433d5e6050624a14d087cadcbdf` (gated fixture, native cases, engine runs) and B `cl-f38596e282519dbbb6ee5ec557e3aa32` (real-store test conversion, production corrections, bounded models, packaging, manifest). Everything below is on local branch `b7-integration-author-b`; nothing is pushed, no pull request, no tag.

| Identity | Commit |
| --- | --- |
| Batch 6 head (base) | `c5b299142ca672cbd2ef0a389492f11de85ff08b` |
| Integrated implementation tip | `90e34e09a3f2bf8a22a00c73b5223fe4b3fb795f` (A's `3af661532a` merged as `0fca47daa7`; the slice 3 review fixes `5d3ee071c7` and `cfa148371c`; A's `9abe5072d5` merged as `90e34e09a3`) |
| Integrated evidence tip | the commit that adds this index |
| Packaged branch | `b7-packaging/remote-cache/b7-verification` at `bc905aed166e28d13fc0b3f2e380cb74bb3c0b96`, on `b7-packaging/remote-cache/b6-sharing` |

## Reports

- [REPORT-AUTHOR-A.md](REPORT-AUTHOR-A.md): the dump facility, the fixture controller and gate, the native cases (sixteen tests in `TestRemoteCacheTransferSuite`), the full-set measurements on a quiet and on a loaded host, the engine container, A's ledger.
- [REPORT-AUTHOR-B.md](REPORT-AUTHOR-B.md): G5 and the mount-free real-store conversion, the reselect audit and progress rule, the inherited items (`Triggers`, decode leader orders, F2), the corrections, the models, packaging, and each finding as it arrived. [LEDGER-AUTHOR-B.md](LEDGER-AUTHOR-B.md) has every test and model invocation with both bounds.

## Findings and corrections (all fixed, each with a test that fails without it)

| Finding | Where | Fix |
| --- | --- | --- |
| Reselect loops could spin without progress | [reselect/AUDIT.md](reselect/AUDIT.md) | progress rule, `f9db98a420`, `af2ddb4e36` |
| `scanPartSources` selected a row whose capture was not ready (pre-existing 5 % flake) | REPORT-AUTHOR-B, slice 1 | `959054a2e0` |
| Key-only offer's first renewal panicked on a nil map | REPORT-AUTHOR-B | `e844245c8b` |
| Clone guards rejected a part-acquired File or Directory | REPORT-AUTHOR-B | `1bfece3b77` |
| Owed bookkeeping after a failed sharing sync | REPORT-AUTHOR-B | not a defect; explained |
| Export during an active pass | REPORT-AUTHOR-B | documented transient, `87c099a615` |
| Workspace constructor identity | REPORT-AUTHOR-B, A's `TestWorkspaceCapture` | not a defect; the export choice was wrong |
| A clean restart wiped the cache after ordinary operations | [boot-wipe/FINDING.md](boot-wipe/FINDING.md) | `16786b5fe3`; option (d) named for the Human |
| Slice 3 generic review G1: a failed owner attach still left the snapshot unowned | [boot-wipe/FINDING.md](boot-wipe/FINDING.md), reviewer `d52af29d81` | `5d3ee071c7`; one step per value, `cfa148371c` |

## Test conversion

[g5/G5-AND-CONVERSION-TABLE.md](g5/G5-AND-CONVERSION-TABLE.md): decision 4's G5 was wrong (import also mounts), the fourth disposition (a mount-free real store, `engine/snapshots/testutil/inplace.go`) was accepted, and the table gives every privileged test its disposition. No privilege skip remains; the seven packages have one skip, the base's TODO.

## Models

`dagql/tla/RemoteParts.tla`, `RemoteOwners.tla`, `RemoteSharing.tla`, `RemoteCheckpoint.tla` with 36 configurations, registered in `.dagger/modules/tla-check` and described in `dagql/tla/README.md`. Quick bounded runs only (ledger M1 to M17); the runs at the design's ceilings are not done.

## Stack manifest and packaging

- [manifest/BATCHES-1-6.md](manifest/BATCHES-1-6.md) and [manifest/PACKAGED-1-6.md](manifest/PACKAGED-1-6.md): completion records and the packaged local branches for the foundations and batches 1 to 6, SHA map in `packaged-1-6.json`.
- [manifest/BATCH-7.md](manifest/BATCH-7.md): batch 7's record, its packaged branch, the corrections to earlier batches listed for the Human's fold-back decision, SHA map in `batch-7.json`.

## Slice 3 verification (author B, ledger rows 26 to 38)

- At `0fca47daa7`: seven packages, `-timeout 90s`, unprivileged, default parallelism: 3057 pass, 1 base skip, 0 fail.
- At `5d3ee071c7` and `cfa148371c` (only `core` changed): `core` and `core/schema` pass; the six failed-attach schedules fail without the discard, the concurrent first uses fail without the lock.
- At the final tip `90e34e09a3`: seven packages, 3068 pass, 1 base skip, 0 fail.
- `-race` selection of the in-process concurrency tests in `dagql`: ok.
- Native run of the sixteen tests on `remote-cache-b7-engine`, `--timeout=15m`, process bound 1260 s, at `0fca47daa7`: **pass**, exit 0, `✔ PASSED`, 822 s wall (log `logs-author-b/slice3/native-sixteen-0fca47daa7.log`); host load average 10.4, 24.4, 37.3 at the start and 36.6, 53.8, 47.8 at the end, two other workstreams' engines running. The five `✘` steps in the CLI summary are the cases' own injected faults (fetches from `origin.remote-cache.invalid`, the negative `Host.directory` cases) and the nested engine service's span ending in ERROR when the run tears it down; the suite result is the pass. Per-test durations are not in the CLI output; the trace is `512d6ab9a6d99407a7fcfba486ff024f`.
- The same run at the final tip `90e34e09a3`: **failed two bodies**, 885 s wall, load 11.4, 12.4, 20.4 at the start and 31.4, 63.5, 50.9 at the end: `TestSharingDonorRestart/DonorAfterImport` (its two-minute wait for the sharing pass's receipt expired; the pass reached `beforeCommit` after the wait) and `TestWorkspaceCapture` (the nested engine's restart exited with code 1 within 1.3 s of starting; its stderr is not in the log). 82 of 84 leaves pass, including every other restart. Reported, not rerun; see REPORT-AUTHOR-B.

## Open items for the Human

Listed at the end of [manifest/BATCH-7.md](manifest/BATCH-7.md): boot's wipe-on-one-bad-row policy (option (d)); `core/file.go:316` / `core/directory.go:338`; the `core/object.go` split; the long model runs.
