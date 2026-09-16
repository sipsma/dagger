# Ownership guard decision required

Recorded at 2026-09-16T23:17:31.068315+00:00. No test is running. The Human's binding correction rejects the one-millisecond ownership-guard timer retry; the added ownership retry and its test have been removed. `dagql/cache.go` is back to its committed implementation. No replacement synchronization mechanism has been added. The six implementation commits and status checkpoint remain unchanged; step 7 and the final evidence commit are not complete.

## Exact remaining contention

The nonblocking guard is shared by ownership readers and part publication, so a busy guard does **not** necessarily mean an operation body is active:

1. A Container has a retained completed whole operation, or completed refined groups. A read of `PersistedOutputRevision` takes `tryPartPublicationGuard`: first `lazyOpMu`, then `LazyMu`, then any unconsumed group latches. Reading checked snapshot links uses the same guard.
2. While that read holds `lazyOpMu`, a second ownership read calls the same guard. Its first `TryLock` fails and returns `ErrPersistStateNotReady`. No operation body is running. This first branch precedes even the `Lazy == nil` check, so the same sequence is possible without an operation pointer.
3. An encoder holding `LazyMu` produces another reader-only busy case. The completed-Container exclusion regression completed the whole operation, acquired its persistence guard, and observed not-ready from the publication guard; it then verified the publication guard still holds the state latch. The regression passed with `-race`: 40.786-second invocation, 1.439-second package execution.
4. Simply skipping `LazyMu` for evaluated Containers removes exclusion between the encoder and acquisition publication: the encoder does not retain `lazyOpMu`. The final working change therefore preserves that shared state latch while skipping consumed group-body latches. File/Directory can skip a completed body latch because their separate output mutex still protects publication.
5. `syncResultSnapshotLeases` returns the not-ready error. A native task sets `syncPending` after its successful body, but the current waiter still receives the error; only a subsequent demand retries the bookkeeping. Fresh result publication also calls the synchronizer and has no completed-task continuation to absorb that error. These call sites are outside the graph lock, but share a reader whose other callers must remain nonblocking.

Code locations in the working tree: `core/part_store.go` (`PersistedOutputRevision`, `PersistedSnapshotRefLinksChecked`, `tryPartPublicationGuard`), `core/container_persistence.go` (`lockForPersistence`), `dagql/cache.go` (`syncResultSnapshotLeases`, native task completion and fresh publication), and `core/lazy_persistence_completion_test.go` (`TestLazyCompletedContainerPublicationExclusion`). The snapshot collector also serves boot/import paths, so changing it globally to block is not an established safe substitution.

This is a concrete lock sequence supported by source and the completed-state exclusion regression. It is not a claim that the original engine log identified which of the three bare guard branches failed. The requested single-method isolation passed, so the original failure was not reproduced as an unconditional completed-operation rejection.

## Two bounded options

1. **Separate the outside-graph-lock ownership read.** Let this caller wait on the existing reader/publication and body latches in their established order; keep capture and Commit nonblocking. The contract must cover contention from another reader as well as a body, and must not hold the pointer/state latch while waiting on a group body that can consult it. This requires a distinct ownership-read contract, beyond merely checking body completion.
2. **Keep the read nonblocking and resume bookkeeping on guard release.** Extend the existing continuation to await an explicit guard-release notification, including reader/publication holders, and cover the initial-publication path. A body's completion signal alone cannot wake the reader-only case above. This is also an explicit synchronization-contract change; no timer or polling would be involved.

I have stopped before implementing either additional contract, as directed when the two body-only choices do not cover the observed contention. Neither option changes the retained-operation representation or removes publication exclusion.

## Verification retained

All previously accepted package, cold, warm, mixed, restart and opted-in default-policy results remain intact. No passing selection was restarted for evidence tidiness.

- Requested isolation: `TestHTTP/TestHTTPPermissions`, passed, 4.31 seconds selected and 230.097 seconds invocation; trace `a3d813be26eb72979013bfb1ffe2f8b5`.
- Intermediate combined run: the six failed HTTP methods and twelve commissioned Git methods all passed without skips, 158.117 seconds invocation; trace `fd96be142f4e43783f9ace5ea4d087c5`. This build included the now-rejected ownership retry. Its output remains evidence of that intermediate implementation and is **not** acceptance of the current tree after removal.
- Completed File/Directory body guards and refined-group guards passed with `-race`; the original intermediate log also contains a Container fast-path case that was removed to preserve publication exclusion.
- The ownership polling regression was removed with the rejected mechanism; its passing log is historical only.
- The final completed-Container publication-exclusion regression passed with `-race` and remains in the working tree.

Logs and exact commands are retained in `/tmp/lazy-values-validation/`: `http-isolation-result.json`, `http-isolation-verbose.log`, `http-git-final-result.json`, `http-git-final-trace.log`, `completion-core.log`, and `completed-container-exclusion.log`. The draft command ledger and manifests also preserve them. The draft report explicitly marks the rejected run as intermediate. No new engine run was started after the binding correction.

The draft evidence and staged step-7 code remain available for review; this commit changes only STATUS.md. The next implementation step requires the ownership-read/continuation contract decision above, then verification of the affected failed or unreached cases and the separate final evidence commit.
