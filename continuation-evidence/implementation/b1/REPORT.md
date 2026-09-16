# Batch 1 implementation report

Implemented the commission from `fd833e35089ec8ba6fe588fe99fa6a79015a4077` and focused design from `860cc5c8b6bb3ad4a59d690573c5f2fcf0377f38`, with the specified explanation and shared interfaces. The initial authorized reset to `1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f` was verified with `git log -1` and a clean `git status`.

Implementation commits, in commission order (full hashes in [COMMITS.txt](COMMITS.txt)):

1. `c9f908e771` — supporting eager construction cleanup and fault cases.
2. `aa1e1db818` — completed-producer recording, moves and dependency attachment.
3. `760062120e` — cleaned and imported Git Directory producers.
4. `0b6b47e33c` — exact Git Ref and Commit tree producers.
5. `52d4131a79` — stateless HTTP File producer, compatible writer selection and locked state capture.
6. `06724072c7` — builtin whole-Container and saved schema-byte producers.
7. `c35802efe6` — persistence, execution, ownership, failure, concurrency and resolver verification.

Every implementation commit is signed off and was buildable before proceeding. This directory is the final, separate evidence commit. Nothing was pushed, published or tagged.

## Verification

The engine-debugging skill was read before tests. Selections ran sequentially; all final commands exited successfully. Mount-dependent tests ran against real snapshot stores as root in private mount namespaces, without environmental skips. The timestamp fault case uses `strace` to inject one `utimensat` failure. [COMMANDS.md](COMMANDS.md) records the commands; [builds.txt](logs/builds.txt) records successful builds.

| Evidence | What it establishes |
| --- | --- |
| [Unit tests](logs/unit.log) | Recording guards and nonmutation; move ownership; existing selector attachment and unchanged bare alias ownership; pending/completed codecs, exact input decoding, relocation, two save/reopen cycles without snapshot opens or ancestor decoding, retention across session release and final pruning. |
| [Resolver tests](logs/resolvers.log) | Cleanup on eager wrapper, digest, owner-sync and recording failures; captured producer kinds at all four Git sites, ordinary HTTP, builtin Container and schema File. |
| [Local Git](logs/git-local.log) | Cleaning selected dirty/staged/deleted/untracked/ignored inputs without changing the original index; Ref/Commit trees across all repository/argument discard combinations; invalid saved bare-cleaning input. |
| [Remote Git](logs/git-remote.log) | Saved Ref/Commit SHAs remain selected after HEAD advances, including a real submodule, depth and tag options. |
| [Git bundles](logs/git-bundle.log) | Real full/incremental import, exact imported refs, missing prerequisites, changed header/ref order, incompatible object format, malformed/oversized files; matching eager/private errors. The bounded integration selection also exercises an advanced prerequisite hint. |
| [HTTP execution](logs/http-evaluate.log), [writer](logs/http-writer.log) | Equal/changed bodies, checksums, response status/timestamps, transport/cancellation errors; root-relative names, exact modes and restrictive umask; unchanged public writer layout and absence of a saved producer on that path. |
| [HTTP cleanup](logs/http-cleanup.log), [timestamp injection](logs/timestamp-fault.log) | Response closure and separate mutable/immutable release counts for copy, close, chmod, timestamp, checksum, mount, commit, digest refusal and failed move. |
| [Audited outputs](logs/audited-outputs.log) | Packaged builtin manifest/platform/config, ordinary missing-content error, schema bytes with schema generation unavailable, and a snapshot-only empty patch negative control. |
| [Race selection](logs/race.log) | Consistent HTTPState tuple capture, private HTTP isolation including guarded unavailable state, and one execution per pending instance with independent decoded latches. |
| [Git integration](logs/integration-git.log), [HTTP integration](logs/integration-http.log) | The design's exact bounded schema selections, including existing auth/service behavior. |
| [Native restart](logs/integration-restart.log) | Ready HTTP/schema snapshots and builtin rootfs reopen after a clean engine restart; each stored-open counter advances once and the HTTP origin receives no additional request. |

## Deviations and review notes

No design mechanism or producer scope was changed. Verification corrected the temporary-index cleanup helper to preserve the original copy/close error text, and moved the existing bundle complexity annotation onto the extracted body. New producer operations do not alter checkout bodies, eager identity teaching or public auth/service HTTP layout. No Changeset producers were added.

The patch negative control uses an unchanged Changeset. The host's Git 2.43 cannot execute the existing nonempty patch body's newer `diff --no-index` pathspec syntax; that initial fixture attempt failed with exit 129. The final control verifies the saved representation of a real empty patch and does not claim nonempty patch execution coverage. Test-fixture setup issues (SDK references, server context, codec registration and syscall tracing of Go threads) were corrected before the final runs.

Reviewers should inspect borrowed versus owned refs, cleanup after producer latches, exact saved Git inputs, HTTP's returned-body digest comparison, and the builtin `_builtinContainer` encode/decode/visit pairing. The invoker, transfer identity changes, unavailable remote bytes, donor-release proof and two-engine hit proof remain later batches' work. Performance and schema-byte retention costs were not measured.
