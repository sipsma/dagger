# Completed Container dependency failure and copylocks follow-up

The copylocks issue is fixed in test-only commit `8d99a010afc875466c999d44c174a5a0c95f3504`, above accepted integration head `97ee3385fdfdb502638922d329e9775e0d040e67`. Vet now exits 1 with **only the four pre-existing lostcancel diagnostics**. No production code changed.

The dependency failure is **not integration-only**: the unchanged batch 2 standalone head `aa7f3fa330faf7ac95217665a61edcb95b633768` reproduces the same failure. Batch 1 standalone passes. There is no batch 1 Container ownership change to blame for this scenario. A completed-before-publication Container retains its recipe but its attachment hook visits only the operational `Lazy`; once that pointer is cleared, the recipe's parent is never added to the result's direct dependencies. This omission predates both batches. Batch 2 adds the boot-time check that detects it.

No fix for the dependency failure has been implemented, including in the diagnostic overlay. The original test, its pending/completed branches, assertions and selection remain intact. The coordinator's decision is pending.

## Authority and reproduction

Both designs and the full engine-debugging skill were read before edits:

```sh
git show 860cc5c8b6bb3ad4a59d690573c5f2fcf0377f38:hack/designs/remote-cache/focused/01-producers.md
git show 2e75801259faf20df2c20a44076c19b027a541b1:hack/designs/remote-cache/focused/02-value-transfer.md
```

Batch 1 §3.2 requires attachment of live completed Directory/File recipes, §9 requires saved producers to survive reopen without evaluating inputs, and §10 explicitly requires producers to preserve direct input dependencies. Batch 2 §5.1 requires ordinary payload/call references to be exact direct dependencies; §§6.2 and 7 preserve and validate those edges during transfer and boot.

The first three runs below occurred before any source edit. Standalone checks used fresh detached worktrees at the exact supplied commits. Every test invocation ran the entire `./core` package, with `-count=1`, no `-run`, no `-skip`, and no test filtering. Packages and commands ran sequentially. `GOFLAGS` was empty; the privileged private-mount-namespace runner used the same Go paths as the accepted integration report.

| Source | Core exit | Shutdown reader pending / completed | Evidence |
| --- | ---: | --- | --- |
| Accepted integration `97ee3385fd` | 1 | PASS / FAIL: missing direct parent dependency | [complete log](followup/integrated-original.log), [command](followup/integrated-original-command.txt) |
| Batch 2 standalone `aa7f3fa330` | 1 | PASS / FAIL: identical missing direct parent dependency | [complete log](followup/b2-standalone.log), [command](followup/b2-standalone-command.txt) |
| Batch 1 standalone `a88e0e0cdd` | 0 | PASS / PASS | [complete log](followup/b1-standalone.log), [command](followup/b1-standalone-command.txt) |
| Test-only fix `8d99a010af` | 1 | PASS / FAIL: unchanged dependency failure; `TestRecordCompletedProducer` passes | [complete log](followup/followup-unfiltered.log), [command](followup/followup-unfiltered-command.txt) |
| Same fix, logging-only overlay | 1 | PASS / FAIL: unchanged dependency failure | [complete log](followup/diagnostic-unfiltered.log), [command](followup/diagnostic-unfiltered-command.txt) |

The common unmodified-test invocation was:

```sh
env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -count=1 -v
```

[Baseline results](followup/baseline-results.json) and [follow-up results](followup/followup-results.json) include full hashes, working directories, timestamps, elapsed times and statuses. The dependency failure was the only failed test in each failing core run. No failure was filtered around or relabeled as a pass.

## Exact mechanism

All source line references below are at `8d99a010af`; the referenced production code and original shutdown test are unchanged from `97ee3385fd`.

1. **The test completes a detached Container before publication.** [core/container_shutdown_persistence_test.go:69](../../../core/container_shutdown_persistence_test.go#L69) publishes `parentRes` as result 1. Lines 73–84 put that exact result in `shutdownContainerReadOp.ContainerWithLabelLazy.Parent`, execute the metadata body, and, only for `completed`, call `ctr.Evaluate`. The assertions establish `Lazy == nil` and `completedRecipe == op` before result 2 is attached at line 86. This is the ordinary `withLabel` producer, not `_builtinContainer`.

2. **Completion preserves the producer, but does not establish a graph edge.** [core/container_parts.go:346](../../../core/container_parts.go#L346) moves the original operation into `completedRecipe` and clears `Lazy`. This behavior came from foundation commit `c3491f00ad2187dbbffd1fc058e9d41835d4e9c0` (`core: retain completed container producer inputs`), already an ancestor of the common foundation `1ca9f28a`. It is not a batch 1 change.

3. **The first divergence is attachment.** [core/container.go:1150](../../../core/container.go#L1150) selects only `lazyOpForRouting()`, which returns `container.Lazy` ([core/container_parts.go:335](../../../core/container_parts.go#L335)). The call to `lazy.AttachDependencies` at [core/container.go:1213](../../../core/container.go#L1213) is therefore skipped for the completed case. No fallback visits `completedRecipe`. There are no mount, secret, socket or service references in this fixture. In the pending case, the same hook calls [ContainerWithLabelLazy.AttachDependencies at core/container.go:2466](../../../core/container.go#L2466), returning the exact parent, and result 2 gets dependency 1.

4. **The fixture's call frame cannot supply the omitted edge.** [core/persisted_families_test.go:126](../../../core/persisted_families_test.go#L126) constructs a synthetic field frame containing only kind, field and type: no receiver, arguments or other reference to result 1. Thus the separate call-reference retention loop at [dagql/cache.go:5869](../../../dagql/cache.go#L5869) has no parent reference to add. A real receiver-bearing call can mask this attachment omission by independently naming the parent. The fixture makes the completed-recipe attachment gap observable.

5. **`Owned: false` is not the cause.** The pending recipe returns that classification at [core/container.go:1223](../../../core/container.go#L1223), but [dagql/cache.go:6032](../../../dagql/cache.go#L6032) calls `addExplicitDependency` for every returned dependency regardless of `Owned`. That flag controls install-span attribution at lines 6035–6040; it does not eliminate physical ownership. Changing it to true would not repair the completed case, because the hook returns no producer dependency at all.

6. **Encoding records the reference without checking this owner's dependency set.** [core/container_persistence.go:72](../../../core/container_persistence.go#L72) selects `completedRecipe` when the parts are final and `Lazy` is nil. The producer encoder at [core/container.go:2475](../../../core/container.go#L2475) emits `parentResultID: 1`. [dagql/cache_persistence_codec.go:78](../../../dagql/cache_persistence_codec.go#L78) resolves the already attached row ID; it does not verify that result 2 owns result 1. The checkpoint copies actual `res.deps` at [dagql/cache_persistence_worker.go:55](../../../dagql/cache_persistence_worker.go#L55), so it persists an empty dependency list beside that nonempty recipe reference. Result 1 survives separately because it has its own persisted root edge, not because result 2 retains it.

7. **Batch 2 detects the mismatch on boot.** After restoring the saved dependency rows at [dagql/cache_persistence_import.go:329](../../../dagql/cache_persistence_import.go#L329), line 365 calls `validateStoredOwnershipLocked`, introduced by batch 2 commit `de126ebe775d078a4c6e240d4d697d3bfdc6834c` (integrated counterpart `afe2e0a2b2`). Its ordinary-row visitor requires `PersistedRefChild` and `PersistedRefCall` IDs to belong to that row's `res.deps` ([dagql/cache_offer_owner.go:391](../../../dagql/cache_offer_owner.go#L391)). `core.Container` is registered at [core/persisted_families.go:14](../../../core/persisted_families.go#L14); its visitor walks retained `lazyJSON` at [core/persisted_visitors.go:414](../../../core/persisted_visitors.go#L414), dispatches `withLabel` through `parentOnly` at line 297, and reports `objectJSON.lazyJSON.parentResultID` as a child reference (lines 170–174). Result 2's empty set fails the membership test at `dagql/cache_offer_owner.go:397` even though result 1 exists in the store. No offer-owner exception applies.

8. **The error triggers the ordinary whole-store reset.** [dagql/cache.go:496](../../../dagql/cache.go#L496) sets `CachePersistenceResetImportFailure`, logs the visitor error and wipes/recreates the store. Reopen then violates [core/persisted_families_test.go:69](../../../core/persisted_families_test.go#L69). The reader-latch wait and clean shutdown themselves succeeded. This is not a deadlock, failed snapshot open, foreign acquisition issue, or the pending-Container encoder guard reserved for a later batch.

### Observed graph state

A Go build overlay added only logging before attachment, after capture and after session release. It changed no assertions, branches, producer code or dependency ownership. The worktree's shutdown test was never edited. [Overlay diff](followup/diagnostic-overlay.diff), [overlay source](followup/container_shutdown_diagnosis_test.go.txt), [mapping](followup/diagnostic-overlay.json), [result](followup/diagnostic-results.json).

| Case | Operational `Lazy` before attach | Retained recipe is original op | Result 2 direct dependencies after attach / after session release | Encoded producer parent | Result 1 incoming ownership after release |
| --- | --- | --- | --- | --- | --- |
| pending | non-nil | false | `[1]` / `[1]` | 1 | 2: its persisted root and result 2's edge |
| completed | nil | true | `[]` / `[]` | 1 | 1: only its own persisted root |

Both rows have persisted root edges in both cases. The completed payload has `fs` and `execMeta` authoritatively absent, while retaining `lazyJSON` with parent 1. The extra read-only observations preserve the original failure. The exact invocation is in [diagnostic-unfiltered-command.txt](followup/diagnostic-unfiltered-command.txt); it runs all core tests with `-overlay=/tmp/b1-b2-followup/diagnostic-overlay.json` and no selection filter.

### Why the batch 1 attribution is incorrect

The [source-provenance comparison](followup/source-provenance.json) checks exact function/file content across the common foundation, both standalone heads and the accepted integration head. `Container.AttachDependencyResultsKinds`, `consumeLazyOp`, `ContainerWithLabelLazy.EncodePersisted`, the entire shutdown test and its environment fixture are identical across all four. [Git history and provenance commands](followup/provenance.log) show:

- Batch 1 makes no changes to DagQL ownership, Container completion, Container persistence, or either fixture file. Its only `core/container.go` change adds the `_builtinContainer` decoder case.
- Batch 1 adds completed-recipe fallback specifically for Directory and File ([core/directory.go:120](../../../core/directory.go#L120), [core/file.go:111](../../../core/file.go#L111)); Container keeps the pre-existing omission. Its new builtin producer has no child references ([core/builtincontainer.go:97](../../../core/builtincontainer.go#L97)) and is not used by this test.
- Batch 1's standalone success does not prove a result-2-to-result-1 edge: it lacks batch 2's new boot ownership validation, and the fixture's final parent-presence assertion is satisfied by result 1's independent persisted root. Batch 2 standalone already contains the validator and fails without any batch 1 code.

The accurate attribution is therefore **a foundation completed-producer attachment omission exposed by batch 2's direct-reference validator**, present in standalone batch 2 as well as the integration. The four rebase conflict resolutions do not touch this path.

## Fix options for coordinator decision — none implemented

| Option | Mechanism and trade-offs | Design impact |
| --- | --- | --- |
| **A. Attach the live completed Container recipe's inputs at publication** | In the Container dependency hook, choose operational `Lazy` when present, otherwise the live `completedRecipe`, and invoke exactly one recipe's existing `AttachDependencies`. Snapshot the choice under the Container operation mutex, release it before graph callbacks, and preserve `Owned: false`. Existing exact-reference attachment then creates/deduplicates the physical edge. Keep encoded completed JSON encoded; do not decode ancestors or execute producers. This retains inputs for the receiver's lifetime and therefore carries the intended retention cost. A commissioned implementation must check attachment ownership/concurrency and encoded-value paths, and rerun the unchanged pending/completed test. | **Required by the two designs as written.** Implements batch 1's §10 direct-input retention and §9 persistence contract with batch 2 §§5.1/7 unchanged. Batch 1 §3.2 explicitly names Directory/File, so its implementation guidance should be clarified to mention this Container gap; the behavioral contract needs no change. It does not relax the pending-Container encoder guard. |
| **B. Permit completed recipe references outside the row's direct dependencies** | Change boot validation to accept a referenced row merely because it exists, or is retained through another path. This could avoid this fixture's reset because parent 1 is an independent root, but does not establish result 2's ownership. Pruning that independent root can lose an exact producer input; export's dependency closure can omit it. Making this alternative sound would require an additional ownership-reconstruction policy, with explicit timing, resource propagation, validation and corruption-handling rules. It is not equivalent to fixing the attachment hook. | Changes batch 2 §5.1's exact-direct-dependency rule and §7's boot validation/reset contract; consistency would also require reconsidering §6.2 import validation. Without ownership reconstruction it violates batch 1 §§9–10's retained-input guarantee. It cannot be adopted as an implementation-only relaxation of the accepted designs. |

Adding a receiver to this one test's synthetic call frame would conceal the missing recipe-attachment edge through the independent call-reference path. That is not a substitute for option A, and the test was not changed to do it. No dependency fix, validator relaxation, test skip, or assertion removal is included in this follow-up.

## Copylocks fix and vet result

The test-only change reconstructs each original Directory/File snapshot field by field as a separately allocated pointer, including `OutputRev`, `persistenceBody`, foreign state, platform, services, stored state/diagnostics, operational and completed recipes, encoded recipe fields, path accessor and snapshot accessor. `require.Equal` compares the full values through those pointers. The fresh, exclusively owned fixtures have zero-valued output mutexes; the snapshot leaves its own mutex zero-valued and the full comparison still checks that state. No mutex is copied. The original shallow pointer/slice field semantics, invalid-input matrix, success assertions and accessor-identity assertions are retained. The fixture constructors are [core/container_persistence_test.go:103](../../../core/container_persistence_test.go#L103) and [core/eager_producer_execution_test.go:86](../../../core/eager_producer_execution_test.go#L86).

```sh
go vet ./core/... ./dagql/... ./engine/...
```

Exit status **1**, with exactly these four remaining lostcancel locations and no copylocks diagnostics:

- `engine/engineutil/executor.go:565:7`
- `engine/engineutil/executor.go:649:14`
- `engine/engineutil/executor.go:716:7`
- `engine/server/session_attachables.go:211:14`

[Complete vet log](followup/followup-vet.log), [invocation and status](followup/followup-vet-command.txt). `git diff --exit-code dfe204d216..HEAD -- engine/engineutil/executor.go engine/server/session_attachables.go` exited 0 with no output; both files are unchanged from upstream main `dfe204d216a579657e00604494ef3268f1062113`. The unfiltered post-fix core run passed `TestRecordCompletedProducer` and still failed the unchanged Container completed case, as expected for this diagnosis-only scope.

The accepted integration report and its logs remain unchanged. This evidence is a separate new signed-off commit above the test-only fix. No production code, reviewed commit, or failing shutdown test was modified; no pushes, pull requests, tags or author/reviewer contacts were made.
