> Resolved by coordinator decision 1 and acquisition Addendum 1 at `04f6d695d4a944fd27e98eb006ed0cda761b3dca`. The scoped inline implementation and passing checks are recorded in [REPORT.md](REPORT.md). The original record below is retained as history.

# Blocker: storage-role ownership for inline part installation

The mandatory inline-output contract needs a persistence mechanism absent from the specified parent. This blocks the complete Prepare/Commit/Finish implementation in step 2. No replacement ownership convention has been implemented.

## Required behavior

The converged batch 4 design at `2ad7bff7a4f61e39361fd222230a21bf4ffb3569`, §3, requires an inline `items[i]` output to keep its enclosing owner row and full `PersistedPartAddress`. Two inline outputs may have the same PartKey. Section 8.1 also says:

> Inline contexts inherit origin and host attachment/full-address routing, not permission to read the root role map through item `SnapshotRoles`; keep existing item-role scoping.

Section 8.4 requires installed local descriptors and their complete desired role map to survive checkpoint and restart. The explanation blob `54dcdac7d7d46e18231d8e05f3834bc6bd5cfe52` §10.4 and §18 requires ownership before publication and the same persisted ownership after restart. It does not specify an inline storage-role representation.

## Parent evidence

All references below are to parent `77f6279559061fd1bb6b3b18e6b08582c7b013a3`.

- `dagql/cache_persistence_self.go:225-258`: list encoding keeps each `itemEncoding.Envelope` but discards `itemEncoding.SnapshotLinks`. The list encoding has no accumulated links.
- `dagql/cache_persistence_self.go:570-573`: `PersistedSnapshotRefLink` contains only `RefKey` and `Role`. Directory and File both use the literal role `snapshot`; no output path is carried here.
- `dagql/cache_persistence_codec.go:239-242`: `PersistDecodeContext.item` sets only server and item call. `ResultID()` is zero, and it receives no item role source.
- `dagql/cache_persistence_codec.go:210-222`: `SnapshotRoles` refuses zero result IDs. Giving an item the whole root map would additionally conflict with §8.1 and alias the two `snapshot` roles.
- `core/persisted_object.go:121-133`: the actual Directory/File snapshot-link loader rejects an inline decode's zero result ID before consulting `SnapshotRole`.
- `dagql/cache_persistence_codec.go:639-641,686`: reference walking also removes snapshot links from inline payloads. `dagql/cache_value_codec.go:114` passes nil links while mapping list children.
- `dagql/cache_persistence_self.go:581-596`: typed owner-link collection uses the outer value's link provider; it does not project links from inline list outputs.

The declared-path traversal correctly distinguishes inline envelopes from `result_ref` children. It does not provide the missing storage-role ownership or decode mapping.

## Reproduction

[Probe source](probes/inline_snapshot_roles_test.go.txt) and [output](logs/inline-parent-probe.log).

The probe ran in a separate detached worktree at the exact parent. It creates an **attached list row** containing two inline Directory values with completed stored snapshot descriptors. Both descriptors encode as snapshot form, both children have result ID zero, and the enclosing row has nonzero result ID 1.

Observed:

1. The standalone Directory control preserves `{Role: "snapshot", RefKey: "local-a"}`.
2. The list preserves **zero** snapshot links instead of two.
3. Decoding the list with its real owner ID fails with `decode list item 1: decode object_id envelope load: load persisted directory snapshot link: zero result ID`.

This is a representation-only probe. It uses existing cache/schema test setup and never opens snapshots or imports a chain; it makes no claim about real storage mechanics. The two failures are intended assertions of the missing contract, not claimed passing acceptance tests.

## Decision options

**A — preserve inline ownership; define scoped snapshot links (recommended).** Specify a durable mapping from `(owning row, OutputPath, codec role)` to owner links. This can be an explicit declared path on links, or a specified reversible role encoding. Preserve and visit those links through list encoding, persistence and capture; supply each inline decoder only its scoped map through a distinct owner/path carrier; project the same links for typed row-wide sync. Keep inline result identity zero and retain the enclosing row as owner. This preserves §3's ownership model, but requires specifying the durable representation and extending the stated item-role contract before implementing publication.

**B — make storage-bearing inline outputs separately owned rows.** Convert them to `result_ref` children before acquisition, so existing per-row `snapshot` roles and decoders apply. This needs an explicit revision to §3: it changes the owning row, declared-path resolution and the two-inline-output acceptance case. It is not an implementation of the present design.

Merely assigning the root row ID or concatenating a dotted role name does not resolve all of encoding, typed sync, capture, decode and restart. Restricting acquisition to root outputs would also leave the explicit inline requirement unmet. Neither shortcut was taken.

The commission says to implement the converged design as written and report a different mechanism with at least two options rather than improvise. This report requests that contract decision; no author or reviewer was contacted.
