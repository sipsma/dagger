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

Every implementation commit is signed off and was buildable before proceeding. Round 1 evidence was committed separately as `7eb998467e`; round 2 preserves those reviewed commits and adds another final evidence commit. Nothing was pushed, published or tagged.

## Round 2: I1–I4

Applied the coordinator's consolidation at `610c9e5f4c733198f41e306abcea609604feced3` on the reviewed tip `7eb998467e3649da4a9928e436d67988b88863d0`, without amending it. New implementation commits (full hashes in [ROUND2-COMMITS.txt](ROUND2-COMMITS.txt)):

1. `1b366cf841` — I1: execute a saved bundle producer with an advanced prerequisite hint and a decoded repository.
2. `a144bd1718` — I2: recording rejection at all eager sites and the File negative matrix.
3. `688bef3408` — I4: document the live-recipe publication ownership contract at both attachment fallbacks.

| Decision | New evidence |
| --- | --- |
| I1 | [Bundle execution](logs/round2-bundle.log): capture the repository and producer before advancing the remote tip, decode the producer against a decoded Repo row, then execute with saved `PrerequisiteRef="main"`. A forwarding backend observer verifies that `Get` receives `refs/heads/main` with the original prerequisite SHA, while remote metadata reports the advanced tip. Imported refs match the saved bundle refs and their parents match the recorded prerequisite. All Git work uses the existing backend. |
| I2 | [Recording matrix](logs/round2-record.log) and [resolver rejection](logs/round2-resolver.log): File now mirrors Directory's nil, typed-nil, existing operation/recipe, accessors, path/snapshot and restore-only rejection checks, including nonmutation. The five additional real resolver calls reject a non-nil operational Lazy, return no output, publish no new value and release exactly one fresh ref. Injected release errors remain discoverable alongside the invariant error; borrowed inputs and retained HTTP state are not released. |
| I3 | [Unfiltered package log](logs/round2-unfiltered.log) and [invocation record](logs/round2-unfiltered-command.txt): `go test ./core ./core/schema ./dagql -count=1`, with only `-p=1` and a mount-isolating `-exec` wrapper added. Exit status 0; core 9.460s, core/schema 43.493s, dagql 2.380s. No package or test filter was used, and no failing package was omitted or retried selectively. |
| I4 | The qualifications and helper-to-design mapping below, plus the two code comments in `688bef3408`. |

The five I2 outputs are constructed inside their resolvers, so their non-nil-Lazy faults use a test-only Go build overlay immediately before the unchanged recording guards. It runs a child test process, changes no worktree source, and adds no production hook. Snapshot test doubles observe the actual output and its release. The full package run also executes this fixture. The focused I1 fixture initially lacked a valid backend platform; that fixture error was corrected before the final tests. All final round 2 commands passed; the unfiltered log retains Go's default package-level verbosity.

## Round 1 verification

The engine-debugging skill was read before tests. Round 1 selections ran sequentially; all final round 1 commands exited successfully. Mount-dependent tests ran against real snapshot stores as root in private mount namespaces, without environmental skips. The timestamp fault case uses `strace` to inject one `utimensat` failure. [COMMANDS.md](COMMANDS.md) records the commands; [builds.txt](logs/builds.txt) records successful builds.

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

No producer scope was changed. Verification corrected the temporary-index cleanup helper to preserve the original copy/close error text, and moved the existing bundle complexity annotation onto the extracted body. That verification commit also contains import and blank-line formatting. The prescribed commit order describes the mechanisms; those small corrections landed during verification. New producer operations do not alter checkout bodies, eager identity teaching or public auth/service HTTP layout. No Changeset producers were added.

The eager error text remains unchanged when cleanup succeeds. Joining a failing release can extend the primary error text; callers can still inspect both causes. In particular, `cleanedInto` now reports an uncommitted mutable ref's release error. On the no-worktree path, such a failure turns an otherwise successful alias return into an error. This follows §12's requirement to release on every uncommitted exit and join cleanup errors.

The HTTP restoration writer sends every write-callback error through mount-root trimming, including open, copy, close and timestamp failures. This is a deliberate superset of eager `fileResult`, which trims rename and chmod errors. It applies §6.2's filesystem-error normalization consistently to the private writer; public `FetchHTTPFile` still selects the unchanged branch.

The §13.4 producer-capture assertions landed at unit level in `TestProducerResolverCapture`: the real cleaned, bundle, Ref-tree, Commit-tree, ordinary HTTP, builtin Container and schema File resolvers are called and their encoded producer kind/JSON inspected. The core execution tests separately compare eager and private outputs. This permits direct inspection of the internal representation without adding a public field or debug hook. The unchanged Git/HTTP integration selections are regression runs; their fixtures do not inspect saved producer payloads. The native restart subtest checks call fields, row identities, stored-open counters and origin request count, not saved recipe bytes. Public auth HTTP's lack of a producer is checked in `TestHTTPProducerWriter`; the integration auth/service cases verify existing behavior, not the saved representation.

The alias contract is proven one level below `gitSchema.cleaned`, in `TestProducerPathCleanup/cleaned_bare_alias`: the same Directory returns, the temporary mutable ref is released once, the borrowed ref is untouched, the existing recipe stays intact and alias attachment adds no second ancestor owner. No test drives the schema's bare-repository pointer-comparison branch directly. The new round 2 cleaned rejection case exercises its fresh-output branch.

The implementation uses these small helpers and guards to express design requirements; the names are not all prescribed by the design:

| Helper or guard | Design basis |
| --- | --- |
| `nilProducerValue` | §3.1's nil/typed-nil rejection; reflection only checks nil, never serializes a recipe. |
| `producedDirectoryOutput`, `producedFileOutput` | §§3.1 and 3.3's ready path/accessor/non-nil snapshot checks, using `Peek` without evaluation. |
| `validateProducedDirectoryReceiver`, `validateProducedFileReceiver` | §3.3's fresh receiver requirement; reject missing accessors, an installed snapshot or stored descriptor before work. |
| `attachCompletedProducerInput` | §3.2's attach-and-type-check step for each exact input wrapper. |
| `withTemporaryGitIndex` | §§5.1 and 12's early removal registration and original copy/close error handling. |
| `recordCompletedBuiltinProducer` | §8.1's internal no-replacement store with nil-Lazy, absent-live-recipe and empty-JSON guards. |
| Builtin installed-snapshot guard | §§3.3 and 8.1's fresh private whole-Container receiver requirement. |
| `BuiltInContainer` releases its output when its body fails | §§3.3, 7.1 and 8.1's construction ownership and failure cleanup, additional to §12's schema-level wrapping guard. |

The attachment fallbacks now document that a live recipe belongs to exactly one value. Concurrent publication of one shared value remains outside this contract; the existing concurrent-demand test does not prove concurrent attachment safety.

The patch negative control uses an unchanged Changeset. The host's Git 2.43 cannot execute the existing nonempty patch body's newer `diff --no-index` pathspec syntax; that initial fixture attempt failed with exit 129. The final control verifies the saved representation of a real empty patch only. It does not dynamically establish that a nonempty `asPatch` File has no producer. Test-fixture setup issues (SDK references, server context, codec registration and syscall tracing of Go threads) were corrected before the final round 1 runs.

Reviewers should inspect borrowed versus owned refs, cleanup after producer latches, exact saved Git inputs, HTTP's returned-body digest comparison, and the builtin `_builtinContainer` encode/decode/visit pairing. The invoker, transfer identity changes, unavailable remote bytes, donor-release proof and two-engine hit proof remain later batches' work. Performance and schema-byte retention costs were not measured.
