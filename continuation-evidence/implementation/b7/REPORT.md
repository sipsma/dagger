# Batch 7: integrated verification and stack. Index

17 September 2026. Two authors: A `cl-1bc62433d5e6050624a14d087cadcbdf` (gated fixture, native cases, engine runs) and B `cl-f38596e282519dbbb6ee5ec557e3aa32` (real-store test conversion, production corrections, bounded models, packaging, manifest). Everything below is on local branch `b7-integration-author-b`; nothing is pushed, no pull request, no tag.

| Identity | Commit |
| --- | --- |
| Batch 6 head (base) | `c5b299142ca672cbd2ef0a389492f11de85ff08b` |
| Integrated implementation tip | `0fca47daa774e64f05ca9aace55378ded38ea103` (merge of A's `9c376138c1` / `3af661532a` into B's line) |
| Integrated evidence tip | the commit that adds this index |
| Packaged branch | `b7-packaging/remote-cache/b7-verification` at `cd3b04e40192251d5f3b46ccaacb817487394c1d`, on `b7-packaging/remote-cache/b6-sharing` |

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

## Test conversion

[g5/G5-AND-CONVERSION-TABLE.md](g5/G5-AND-CONVERSION-TABLE.md): decision 4's G5 was wrong (import also mounts), the fourth disposition (a mount-free real store, `engine/snapshots/testutil/inplace.go`) was accepted, and the table gives every privileged test its disposition. No privilege skip remains; the seven packages have one skip, the base's TODO.

## Models

`dagql/tla/RemoteParts.tla`, `RemoteOwners.tla`, `RemoteSharing.tla`, `RemoteCheckpoint.tla` with 36 configurations, registered in `.dagger/modules/tla-check` and described in `dagql/tla/README.md`. Quick bounded runs only (ledger M1 to M17); the runs at the design's ceilings are not done.

## Stack manifest and packaging

- [manifest/BATCHES-1-6.md](manifest/BATCHES-1-6.md) and [manifest/PACKAGED-1-6.md](manifest/PACKAGED-1-6.md): completion records and the packaged local branches for the foundations and batches 1 to 6, SHA map in `packaged-1-6.json`.
- [manifest/BATCH-7.md](manifest/BATCH-7.md): batch 7's record, its packaged branch, the corrections to earlier batches listed for the Human's fold-back decision, SHA map in `batch-7.json`.

## Slice 3 verification at the integrated tip (author B, ledger rows 26 to 28)

- Seven packages, `-timeout 90s`, unprivileged, default parallelism: 3057 pass, 1 base skip, 0 fail.
- `-race` selection of the in-process concurrency tests in `dagql`: ok.
- Native run of the sixteen tests on `remote-cache-b7-engine`, `--timeout=15m`, process bound 1260 s: **pass**, exit 0, `✔ PASSED`, 822 s wall (log `logs-author-b/slice3/native-sixteen-0fca47daa7.log`); host load average 10.4, 24.4, 37.3 at the start and 36.6, 53.8, 47.8 at the end, two other workstreams' engines running. The five `✘` steps in the CLI summary are the cases' own injected faults (fetches from `origin.remote-cache.invalid`, the negative `Host.directory` cases) and the nested engine service's span ending in ERROR when the run tears it down; the suite result is the pass. Per-test durations are not in the CLI output; the trace is `512d6ab9a6d99407a7fcfba486ff024f`.

## Open items for the Human

Listed at the end of [manifest/BATCH-7.md](manifest/BATCH-7.md): boot's wipe-on-one-bad-row policy (option (d)); `core/file.go:316` / `core/directory.go:338`; the `core/object.go` split; the long model runs.
