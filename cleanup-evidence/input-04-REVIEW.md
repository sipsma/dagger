# Independent review of the remote-cache cleanup commits

Reviewer: Fable 5.1 (xhigh), fresh cleanup reviewer, 13 September 2026.
Worktree: `/home/exedev/.tailcall/worktrees/dagger-a7378ed8352b/remote-cache-cleanup-reviewer-d12f34a0-8f1a88a6`.
Candidate reviewed: commit `1ba150a79b8a3f7568e27a22cee492f578ffa538`, tree `d5a73331e65629681246351a864fd2ecda9b899e`. The worktree HEAD and tree matched these values throughout; the worktree stayed clean (no tracked changes, `dagger.lock` untouched).

## Verdict

**No blocking findings.** The six implementation commits and the documentation cleanup commit do what their messages say, contain no residue of the rejected equality, byte-observation, nested-forwarding, LLM or service-identity work, and introduce no regression I could find by reading the surrounding code or by running the unit-level checks below.

**Two should-fix findings** with bounded corrections (one small correctness gap in the new live-capture entry point, one stale internal-documentation statement), and **one validation limit** that must be recorded rather than fixed in code: the existing engine-level restart-persistence tests that exercise the changed Container/Directory/File encoders were never run on the final source, and my own attempt was interrupted.

Technical acceptance here is separate from permission to retain. All seven commits are authored and signed off as "Erik Sipsma" although the investigators established they were produced by the former coordinator agent; that attribution question is for the Human, not a technical defect, and I do not treat it as one.

## What I read and how I judged

Read in full before forming any view: the three investigator/handoff reports, the extracted direct Human instructions (all 38 messages), the preserved original guidance (`index.txt`, `engine-foundations.txt`, `source.txt`), the repository `CONTRIBUTING.md`, and the current `skills/engine-debugging/SKILL.md` in full. I kept Human instructions separate from reviewer suggestions and hypotheses; the retained designs and the earlier narrow approvals are evidence, not authority.

Then I read the complete diff of every commit in `a161ceb34c..1ba150a79b` and the surrounding source each commit depends on: changeset merge and schema wrappers, exec metadata preparation, container clone/consume/encode/decode paths, directory and file lazy encoders and visitors, egraph digest teaching, cache ownership, lazy-attempt locking, and the shutdown persister that the live capture mirrors. I also checked the independent auditor's removal map against the tree for residue.

Provenance checks: `a161ceb34c` tree is `0d72a60f37845ea1b2a7024b258b5be585ea4903`, identical to the independently reviewed author commit `3546c127c0`'s tree, as the takeover audit states. `17f7dd89f4` tree is `7b67a116a5f55ae948c5f34b36e56c3ac9499caf`.

## Residue and completeness of the cleanup

- **Code residue:** none. Grepping `core`, `dagql`, `engine` Go sources for the rejected vocabulary (observation, ByteRequirement, ValueEvidence, DigestEvidence, EstablishedContent, DeclaredIdentity, ImplementationEvidence, ConstructionBinding, ValueBindingProof, ScopedEvidence, MCPWorkspace, Service RuntimeID) finds only pre-existing unrelated identifiers (for example `runtimeID` for a module runtime reference in `core/module.go`, and a dagui span-test variable). Every file the removal map lists as existing only because of batches 3/4 is absent. The persistence schema version is `"19"` (`dagql/cache.go:150`), the batch-1 cut. `dagql/tla` holds 46 files with none of the batch-3 `match`/`alias` configurations, and its README has no batch-3 section.
- **Design-document residue:** none. The five retained documents under `hack/designs/remote-cache/` describe ordinary metadata transfer, retained producers, single-row capture, lazy acquisition and the unresolved lookup-preference choice. The rejected concepts appear only as explicit exclusions. Commit `8a8a4dfda2` rewrote `remote-cache-data-flows.md` (the seven-PR plan, value classes, matching associations, observations and the LLM/candidate PRs are gone) and `persisted-value-graphs.md` (the LLM conversation rows and "later conversation codec" paragraphs are gone), and added the three reduced documents that previously existed only under `/tmp`. Its three code hunks only reword comments that referred to a deferred LLM batch.
- **Nothing needed was lost.** The rewritten documents keep the direction the Human gave (original inputs for both completed and pending values, no recursive downloads, safe extra digests only, local failures unchanged, no service in scope) and record the D15 lookup-preference question as unresolved instead of deciding it.

## Per-commit correctness review

### `c49db1291f` Changeset merged directories get ordinary producers

The synthetic `changeset_merge_output` call (snapshot ID and path as its only inputs) is gone. Two internal, persistable fields `__mergeWithChangeset` and `__mergeWithChangesets` now produce the After directory synchronously through the unchanged merge bodies; the public wrappers select them and build the Changeset from `MergeBeforeDirectories` plus that After. This follows the existing `__withDirectoryDockerfileCompat` convention (`View(AllVersion)`, `IsPersistable()`, Go codegen skips `__` names at `cmd/codegen/generator/go/templates/introspect_emit.go:430`). Empty changesets are filtered before the internal call so the recorded `changes` list is the effective ordered input; one effective input is routed through the two-way field with the same conflict mapping the old code used; zero inputs still return the parent. `MergeWithChangesets` now errors below two inputs, which is unreachable through the wrappers. No other caller of the removed `WithChangeset`/`WithChangesets` core methods exists (only generated SDK clients in testdata reference the unchanged public API).

Minor, not a defect: each public call now runs `MergeBeforeDirectories` twice (once inside the internal field, once in the wrapper). Both go through cached `withDirectory`/`withoutDirectory` selections, so the result is the same and the extra cost is lookups, not copies.

### `371c77af48` Original exec metadata is what gets persisted

`execMeta` (`core/container_exec.go`) copies the input struct by value but appends into the `HostAliases` map in place; that is the only in-place map mutation on the input, and `copyExecInputMetadata` clones exactly that map (and its slices). Slices such as `ExtraSearchDomains` and `SecretEnvNames` are appended on the copy. `HostAliasFQDNs` is assigned a fresh map by service binding (`core/service.go:1663`) and is `json:"-"`, so it never reaches the payload. `originalExecMD` is set at both construction sites (`WithExec` and decode) and is never overwritten by the run-time assignment at `core/container_exec.go:1435`. Intended behavior change, correctly stated by the author: a pending exec persisted after a failed run now carries its input metadata instead of derived metadata.

### `8d144785d4` Completed containers keep their producer

`consumeLazyOp` (`core/container_parts.go:346`) is the only site that clears `Container.Lazy`; it now retains the op (unwrapping `ContainerRestoreLazy`) before clearing. Encoding writes the live op when present, otherwise the retained op, otherwise the raw bytes restored from disk. Complete rows decode without touching the producer's parents (`core/container.go:1545-1560`), which the unit test proves by inspecting the egraph snapshot after restart. Schema children are built field by field by `cloneContainerForSchemaChild` (`core/schema/container.go:3409`), which does not copy the new fields, so a child never inherits its parent's producer; `go vet` (copylocks) confirms no struct-copy of `Container` exists.

Two things worth knowing for the acquisition design, neither a defect of this commit: a metadata-only child of a fully evaluated parent is created without any lazy op (see the `parentPendingLazy` branch at `core/schema/container.go:200-230`), so it persists with no producer bytes; its recorded call frame's receiver identifies the parent, which is what a future fallback would have to route through. And the retained op keeps the parent `ObjectResult` reachable in memory for the container's lifetime, which the narrow review noted and which matches the cache's existing dependency retention.

`ContainerExecLazy.EncodePersisted` still refuses an exec that carries a `FunctionCall`. Only the public `withExec` resolver calls `Container.WithExec` in core, and it passes a nil function call (`core/schema/container.go:1641`), so no persisted row reaches the encoder with that state; I found no new encode failure for completed containers.

### `5c3fe15eb6` Completed directories and files keep their producer

The producer is captured in `LazyEvalFunc` after `Evaluate` returns and independently of the pointer clear (`core/directory.go:145-152`, `core/file.go:134-141`), which correctly covers the container-rooted bodies that clear `dir.Lazy` themselves (`core/container.go:3040`, `3051`, `3246`, `3286`, `3314`, and the file equivalents). The typed encoders cover every production `Lazy[*Directory]` (fifteen directory kinds plus the two container-rooted kinds) and every production `Lazy[*File]` (seven kinds); the restore lazies are excluded from capture by design. So no completed production value can newly fail to encode. `visitPersistedLazyKind` accepts an empty kind with empty bytes, so old snapshot-form rows without producer fields still pass the visitor. New children (`Subdirectory`, `Subfile`) are constructed fresh and inherit nothing. Producer inputs are attached results because the lazies declare them through `AttachDependencies` before publication, the same contract the pending form already relied on.

### `c7e20ab5d4` The `remote-cache` extra-digest label

A one-constant policy marker using the existing labelled extra-digest representation. The label is attached alongside the unchanged `content` entry, deduplicated, and preserved when the content digest is later replaced (`TeachContentDigest`, `dagql/cache_egraph.go`). Because labels are isolation keys in the egraph, a `remote-cache` entry joins only with other `remote-cache` entries carrying the same digest, and the same digest is always present under `content` too, so no new equivalence is created. Only the pinned `Container.from` identity is marked (`core/schema/container.go:1247`). No storage-format change. `TestExtraDigestLabelIsolation` simply uses the constant as one of its two differing labels; its assertions are unchanged.

### `1ba150a79b` Capture one live persisted record

The entry point mirrors the shutdown persister's stub construction (`persistResultEnvelope`) and codec call, takes an ordinary cache operation so `Close` waits for it, holds the row through `incrementIncomingOwnershipLocked` and releases it with collection and `OnRelease` under a non-cancelled context, refuses rows with a published attempt or pending bookkeeping on the whole group or any part group, and holds `lazyMu` across the encode only when the row is armed but unstarted so that no evaluation can start under it. Lock order is safe: a scan of `dagql/cache.go` shows every `lazyMu` acquisition lives in functions that do not hold `egraphMu` (`registerLazyEvaluation`, `waitForLazyEvaluation`, `evaluateResolved`, `pendingLazyGroups`, `evaluateGroup`, the two `HasPending*` helpers), and the capture releases `egraphMu` before taking `lazyMu`. The race run of the capture tests passes. Cold rows are copied from their stored envelope without decoding. This is one-row capture with local IDs; it is not closure export, relocation, import or acquisition, and the commit and its document say so.

## Findings

### Should-fix 1: capture accepts rows whose dependency attachment is open or failed

Evidence. `dagql/cache_persistence_capture.go:28-33` verifies only registration (`resultsByID[shared.id] == shared`). The shutdown persister, which this API is documented to mirror, selects rows through `snapshotPersistedRootClosureLocked` (`dagql/cache_persistence_worker.go`), which marks every row with `attachmentState() != resultAttachmentClean` invalid, and `attachmentState()` (`dagql/cache.go:2205`) reports open while `AttachDependencies` is still running and failed when it errored. A capture during that window encodes a row whose dependency edges are not yet, or never will be, registered.

Why it matters now. The commit's own contract is "not ready" for state the persister would not save; attachment is part of that state. The excluded four-file draft's tracked change to this file is described in the takeover audit as adding "an attachment check", which shows the author recognized the gap after committing. That draft stays excluded; the correction must be written fresh.

Bounded correction. Inside the existing `egraphMu` critical section that checks registration, return `ErrPersistStateNotReady` for `resultAttachmentOpen` and a plain error for `resultAttachmentFailed` (mirroring the persister's exclusion), plus one unit test that blocks attachment with an existing fixture pattern and asserts the not-ready error and no leaked hold or operation. About ten lines of production code. Optional nit in the same change: the persister refuses a frame-less non-Query row, while capture would encode it with a nil frame; refusing it the same way keeps the two paths identical.

Classification: cleanup defect in the committed slice's readiness contract, not future feature work. Not blocking, because no production caller exists yet.

### Should-fix 2: internal documentation now misdescribes the persisted forms

Evidence. `internal-docs/cache_persistence.md:413-414` and `internal-docs/lazy_evaluation.md:476-477` still say a Container keeps "its original recipe only while computation remains", and the surrounding text describes Directory/File snapshot forms without the producer kind and bytes that `5c3fe15eb6` added. The engine-debugging skill directs agents to these documents as the current mental model.

Bounded correction. Update the two passages to say that completed Container rows retain the producer's encoded inputs, that Directory/File snapshot forms carry `lazyKind`/`lazyJSON` for the completed producer, and that decode of a completed row keeps those bytes without loading ancestors. A few sentences; no code.

Classification: completeness of the retained commits (they changed the persisted representation without updating the documents that explain it). Low severity.

### Validation limit (record, do not fix in code)

The historical record shows unit and package tests for every slice and one native subtest (`changeset merge producer survives restart`) on the final source, plus five public merge cases on pre-nit source. The other subtests of `TestCachePersistence/TestDiskPersistenceAcrossRestart` that push completed Containers, Directories and Files through a real engine's save, restart and reopen ("directory and file restore without opening", "container parts preserve mutations and unopened snapshots", "container withNewFile hit survives restart", "container selector lazy dependencies survive restart", "container withExec output on host mount survives restart", "module core metadata returns survive restart", "service-bound graph", "generator group graph", "git repository and ref survive restart") were not run on any source at or after `8d144785d4`/`5c3fe15eb6`. Those commits change the payload of every completed filesystem-bearing row, so this is the most relevant regression surface that remains unexercised end to end.

I launched exactly that subset once (`evidence/cleanup-review-native.sh`). The host disk reached 100% during the engine build phase; the coordinator sent SIGINT to that CLI only, and the log (`evidence/native-restart-subset-INTERRUPTED.log`, 53 lines, no test results) is preserved as interrupted and invalid coverage. Per the coordinator's operational instruction I did not rerun it. This limit should be closed with one bounded run of that subset before any Human review of the candidate, once capacity exists; it is not a reason to change code now.

## Checks I ran on the candidate (all at HEAD `1ba150a79b`, tree `d5a73331e6`)

Log: `evidence/unit-focused-race-vet.log`; script: `evidence/cleanup-review-unit.sh`.

| Check | Result |
|---|---|
| `gofmt -l` over the twenty changed Go files | clean |
| `go test ./dagql/call -run TestRemoteCacheExtraDigest` | ok |
| `go test ./dagql -run 'TestCapturePersistedRecord\|TestRemoteCacheExtraDigestMetadata\|TestExtraDigestLabelIsolation\|TestEquivalencySetCacheHits\|TestCacheTeachContentDigest'` | ok |
| `go test ./core -run 'TestContainerCompletedProducerPersistsWithoutLoadingParents\|TestFilesystemCompletedProducerPersistence\|TestContainerExecPersistsInputMetadata\|TestContainerExecSuccessConsumesFinalReadOnlyMount'` | ok |
| `go test ./core/schema -run TestCoreSchemaObjectsHavePersistedFamilies` | ok |
| `go test -race ./dagql -run '^TestCapturePersistedRecord'` | ok, no race reports |
| `go vet ./core ./core/schema ./dagql ./dagql/call` | clean |

Not run by me: full package suites (historically passing per the frozen records, which I read but did not re-execute), any model checks, any other integration tests. Historical validation is attributed to its source-at-run in the takeover audit and I have not relabelled any of it.

## What is future work, not a cleanup defect

The candidate does not export a dependency closure, translate IDs, import into a second cache, use a supplied snapshot chain, or fall back to the original producer on a failed download. The five documents say so and the commits do not claim otherwise. The lookup-preference policy (D15), offer timing for unstarted work, and address refresh remain explicitly open. The metadata-only-child observation under `8d144785d4` above is an input to the acquisition design, not a defect.

## Coverage limits of this review

I reviewed the a161..1ba range and the code it touches, plus tree-wide residue greps and the removal map's file list. I did not re-review the pre-codec foundations between `17f7dd89f4` and `a161ceb34c`, whose acceptance the record already covers. I did not inspect the excluded dirty stack's four-file delta beyond the audit's description, and I did not disturb it. I did not fetch or verify any GitHub state. No production fixes were authored here.
