# Boot wipe: a late-created backing snapshot never gets its owner lease

Author B, 17 September 2026. Dispatch `bfb1abc8fe`, diagnostic `149be4bf59`. No production code changed yet.

## Cause

Four backing types are exported as `foreign_uninitialized` and arrive on the receiver with no snapshot: `CacheVolume`, `RemoteGitMirror`, `ClientFilesyncMirror`, `HTTPState`. On first use each one creates a fresh local snapshot. Only `HTTPState` then attaches the owner lease (`core/schema/http.go:173`, `SyncResultSnapshotOwnerLeases`). The other three create the snapshot and tell nobody.

For a locally created row this never shows, because the creating resolver makes the snapshot before it returns (`core/schema/cache.go:93`, `core/schema/query.go:132,146`) and the lease sync at publication sees the link. An imported row was published, and synced, with no link; the snapshot comes later.

## Call sequence (cache volume, as in A's run)

1. B imports the SDK runtime's Container ancestry. The `CacheVolume` row decodes with `foreignUninitialized` and an empty `snapshotID`. Import's lease sync sees no links. Correct so far.
2. B serves the module. `Container.WithMountedCache` (`core/container.go:6219`) or the exec mount path (`core/container_exec.go:978`, `:1819`) finds `getSnapshot() == nil` and calls `InitializeSnapshot`. `SnapshotManager().New` prepares the snapshot under the lease in the calling context (`EnsureLease`), which is the session's. No owner lease `result/<id>/snapshot` is created, and `snapshotOwnerLinks` on the row stays empty. This is why A's in-memory report shows no link for the row.
3. The session ends. The context lease goes. The value still holds the `MutableRef`, but a held ref is not a lease.
4. Collection removes the snapshot. (In my reproduction the collection before `Close` is enough.) A running engine is already wrong at this point: the row's value points at a removed snapshot, so the next exec that mounts this cache volume would fail, restart or no restart.
5. Clean shutdown. The checkpoint derives a typed row's links from the value (`desiredSnapshotLinksForResult` to `collectSnapshotOwnerLinks` to `CacheVolume.PersistedSnapshotRefLinks`), which now reports the snapshot. A `result_snapshot_links` row with role `snapshot` is saved. This is why A sees the link in the mirror and not in memory.
6. Boot: `AttachLease(result/<id>/snapshot, <snapshot>)` returns not found, `cache_persistence_import.go:567-590` treats it as damage and wipes everything.

`TestPipeline/DonorReleased/AfterRestart` passes because there B loads the module before it imports, so the runtime's cache volume row is B's own.

## Reproduction

`boot-wipe/repro_test.go.txt` (scratch, uncommitted, in `core`). Real stores, real collection, no engine. A exports the row; B imports, loads the row, does what the production site does (`InitializeSnapshot`, `RemoteGitMirror.acquire`, `ClientFilesyncMirror.EnsureCreated`), releases the session, collects, closes, reloads, reopens.

Failed assertion, all three kinds: `PersistenceResetReason()` is `import_failure`, expected none. Boot log, same text as A's:

- `attach imported result 1 owner lease "snapshot": …: not found` (cache volume)
- `attach imported result 1 owner lease "bare_repo": …: not found` (git mirror)
- `attach imported result 1 owner lease "snapshot": …: not found` (filesync mirror)

Log: `boot-wipe/repro-three-kinds.log`. A's extra six-minute run is not needed.

## Options

**(a) Sync the row's owner leases at each late-creation site, as `HTTPState` already does.** One helper in `core` that takes the row, creates the snapshot if missing and calls `SyncResultSnapshotOwnerLeases`. Five call sites, and every one already holds the row as an `ObjectResult`: `container.go:6219`, `container_exec.go:978` and `:1819` (`cacheSrc.Volume`), `git_remote.go:527` (`repo.Mirror`), `schema/host.go:331` (`persistedMirror`). Between `New` and the sync the snapshot is covered by the context lease, the same window `HTTPState` has today. Cost: a future sixth site can forget; the helper and a comment on the three `foreignUninitialized` fields are the guard. Small. The reproduction becomes the failing-before test, driven through the production entry points where they need no mount.

**(b) Make it generic in dagql.** When a call completes, re-sync the leases of its dependency rows whose value declares that its links can appear late (a marker interface, or reuse `CacheUsageMayChange`). Nothing to remember per site. But the dependency is not always direct (a git tree call reaches the mirror through the repository), it puts a value lock and a link comparison on the completion path of every `withExec`, and it is new mechanism in batch 4 code for three call sites. I do not recommend it.

**(c) Create the snapshot at import.** Import would initialise every foreign-uninitialized backing row and its own lease sync would cover it. It makes snapshots for volumes nobody mounts, and a cache volume with a source and an owner needs a server `Select` for the chown, which import does not have. No.

**(d) Boot drops the one row instead of wiping.** Separate question. Treating a dangling owner link as damage is the base's deliberate policy, and (d) alone leaves step 4's live fault. Named only; not for this batch unless the Human wants it.

Recommendation: **(a)**. It is the existing pattern, it fixes the live fault as well as the wipe, and it is five lines at five sites plus the helper.

## Separate observation, no action proposed

The checkpoint saves a typed row's links from the value's current state, not from the links whose leases were attached. That is what turned a missing lease into a whole-cache wipe rather than a row that persists as uninitialised. Saving only leased links is not sound on its own, because the encoder writes the payload's `Form` from the value too, so form and links would disagree. I mention it because any future late-created snapshot fails the same loud way.

## Outcome (17 September 2026)

The Coordinator chose option (a). Fixed in `16786b5fe3`: `core.EnsureBackingSnapshot` creates the snapshot and syncs the row's owner leases, used at all five sites. Policy on a failed sync: the call fails, as `HTTPState`'s does; a foreign row syncs again at its next use, so the failure is retried rather than left as a snapshot with no owner. The scratch reproduction is deleted; `core/backing_snapshot_test.go` (`TestImportedBackingSnapshotIsOwnedByItsRow`) replaces it for all three kinds, the cache volume through the production `WithMountedCache`. It asserts the live fault is gone (after the session ends and a collection runs, the row mounts the same snapshot and a write through it succeeds, no restart), then that a clean restart keeps the cache and reopens the same snapshot. With the sync removed all three kinds fail at the first mount after the collection: `boot-wipe/test-without-sync.log`. The wipe itself without the fix is in `boot-wipe/repro-three-kinds.log`.

The two mirror kinds are driven through the helper, not through `initRemote` and `host.directory`, because those need git over the network and a client filesync connection. Their call sites are one line each.

## Named item for the Human: option (d), not done here

Boot treats one row whose saved owner link points at a missing snapshot as damage to the whole store and wipes everything. This fix removes the one known way to reach that by ordinary operations; it does not change the policy. Whether boot should instead drop that row and its dependants and keep the rest is a decision about the base's persistence contract, with its own risks (a dropped row that other saved rows reference, and hiding real corruption). It is recorded here as an open item for the Human and nothing in batch 7 implements it. The related observation above stands with it: the checkpoint saves a typed row's links from the value, not from the leases actually attached, which is why any future late-created snapshot that misses `EnsureBackingSnapshot` would fail in the same loud way.

## Slice 3 generic review, G1: a failed attach left the snapshot unowned (fixed in `5d3ee071c7`)

The reviewer (`d52af29d81`) showed that with `16786b5fe3` a sync that fails before the attach leaves the created snapshot in the value, protected by the session alone, while the value already reports its link; after session end and collection the retry fails with `not found`, and a restart without a retry wipes the cache. Reproduced for all three kinds. The chosen form is "fail the call and drop the snapshot": the value returns to exactly its uninitialised state, so the next use creates and syncs again and a checkpoint saves the foreign-uninitialised form. A reopened cache volume snapshot is not dropped, since its row already owns it. If the attach itself succeeded and a later step failed, the owner lease keeps the dropped key as a resource until the row's lease cleanup, and the next attach adds the new key to the same lease. The filesync mirror refuses to drop a snapshot another caller has mounted meanwhile and reports that with the error; that caller's own sync owns it. Regression: `TestImportedBackingSnapshotIsDroppedWhenItsOwnerAttachFails`, six schedules (three kinds, retry after the collection and restart without a retry), all failing without the drop (`test-without-discard.log`).

## One step per value (`cfa148371c`)

The Coordinator's check after G1: creation, sync and discard were not serialised per row. Two concurrent first uses could interleave so that the second took the first's snapshot as its own, and the first's failed sync dropped it under the second. `EnsureBackingSnapshot` now holds a lock of its own per value (`backingMu` on each type), not the value's state lock: the lease sync reads the value's links through the state lock and the encoder takes the state lock too, so neither waits behind a lease-manager call. `TestImportedBackingSnapshotConcurrentFirstUses` runs eight first uses per kind with the first attach faulted; without the lock, under the race detector, a successful caller comes back with no snapshot (`test-without-lock.log`).

The cold-order fact used above is the one A's report names as `TestSchemaRecoveryCold`'s evidence, and `TestPipeline/DonorReleased/AfterRestart`'s comment states that B serves its module before importing; both agree with the sequence here.
