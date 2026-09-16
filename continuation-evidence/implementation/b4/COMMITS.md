# Commits above the integrated parent

Parent `77f6279559061fd1bb6b3b18e6b08582c7b013a3` is unchanged and remains an ancestor. Every commit below has a Signed-off-by trailer. The final separate evidence commit is named in the delivery reply.

- `e307838a698b7845e0bf743225281d90b01b01e1` — dagql: add per-part writer gates and supplied lazy tasks
- `21234ff01904820a1c3669e0a8f4b273062e61f9` — docs: record batch 4 progress and inline snapshot ownership blocker
- `1ccf619f43d0710b1eefe92fec238ade8397eaaf` — dagql: prepare and publish acquired root outputs with owned receipts
- `f19717d0a067d8f33c18fc94695dabc2bceb76ff` — cache: import selected chains and settle acquired output ownership
- `df39d040c56f31cce8edf47855d205bb9dcfe909` — cache: acquire root outputs with fresh private producers and raw restore views
- `521b90d51d0ac66771aea0da460549e6b0592e63` — cache: preserve scoped inline acquisition and exercise native transfer paths
- `b4b84f5c60ea9b814b6b404e108cfb38bedb80d1` — docs: record batch 4 verification costs and cold module blocker
- `8e0e2198d8900f105e6b37e8378690f173d782e0` — core: extract declared object retention for separate review
- `5b73480d43aaf86aeacf591888f1a7c27d97c611` — core: retain attached declared SDK object fields for relocation
- `506649d04a36c41d0b9a676645efdc524eca0beb` — cache: prove downloaded filesystem and private exec metadata coexist
- `2777bb534d8bcd37d5b40e6dab48186b7f43cc36` — cache: cover acquisition decision and publication boundaries
- `c4d58da56bd9d676a80f8ebf66d176389518172c` — cache: verify whole-producer restart and pre-sync donor release
- `fbd4013309a71247a1f68f78dc46e9afa6ee5e7c` — docs: record mixed exec proof and blocker-two follow-up evidence
- `f134133fc807218ed8007b2d70f505b9ab2a2598` — core: record completed eager Container mount producers
- `c02411efad0b5e3f1bbb6c23d1cc3152fc2af1b2` — cache: acquire unchanged Container parts from their exact recorded parent
- `21d0da8c86db12af34d0911a2119297be9bdd0c8` — core: verify eager mount recording rejection releases owned clones
- `3d90f1f724fec32ab27dd91a2d047c1def8e8768` — core: release shadowed mount clones when eager recording fails
- `ff0879dfcbec2309847ffb04e1615e831454bb3e` — cache: verify delegation ordering, waiter lifetime, forwarding and restart
- `3337963558b7178156d228e6b3e38136beb919e0` — test: count exact acquisition hops through the cold SDK chain
- `4ccf106dd4a2b6a9d7cfcd5a2d03bb2cb9fdb996` — test: report inherited mount producer routes by exact source row
- `6d5cbe20a42c3c82a74209eeed5901cd149dfd52` — docs: record binding addendum implementation and scratch acquisition blocker
- `8d4b99c0116bae44439055fe73d325bf882b6691` — docs: inventory producer-less pending values in the full cold closure
- `f9158c2ba2735be974770246a2449daee0b106c2` — core: record and restore the eager scratch Directory with an empty recipe
- `b404de7252b0b023692ae11ac62a0991925de3b6` — core: verify scratch producer acquisition, restart and independent ownership
- `78eab473803a5f5405bf0d315359bed4b0aa1270` — test: observe exact scratch rows through cold and warm recovery controls
- `e1443fff63896b9f274e75874a377e1df4799862` — docs: complete batch 4 acquisition evidence and cold controls
- `733cec2bb0afd2e2455858890a5ce457a0837f96` — fix(core): retain only ID-shaped module object fields (B4-D1)
- `c62429de3f615aef81f35c31244d8b31a5a9d1cd` — fix(dagql): prepare sessionless lookups outside graph lock (B4-D10)
- `855004e49fb7845495881ab261738c3a7bb7b57c` — fix(dagql): release selected sources on scan failure (B4-D7)
- `3b3a076e1fbeb07229a8deae39ca996c190ed1a1` — fix(dagql): retain imported ref cleanup through publication (B4-D5)
- `ded91500c9ad0ce45c7842eefd840078ead6c56a` — fix(dagql): preserve acquisition causes at final fallback (B4-D6)
- `66a1822fa6be3c79a9815f1b19ae56b9b4381494` — fix(dagql): key exhausted content by offer revision (B4-D2)
- `68535753642d916582c3775147ff4911f7357d64` — fix(dagql): settle native parts through offer retirement (B4-D3)
- `0829066c7e8223f4662e48a46010ec3c05faa920` — fix(dagql): stream blobs when endpoints ignore Range (B4-D8)
- `9f71286d8c8f78d59094fe0811494851054f203a` — refactor(core): remove dead Container pending carrier (B4-D4)
- `edd08565fc8069c9a7471a17fa5e27613295cba0` — refactor(dagql): simplify part lookup and address checks (B4-D9)
- `f04a91577bd4ddc574f3bc527cdffe9fec2179a1` — test(cache): validate import mappings and private FS observation

## Packaging and shared-path notes

The object-field correction remains authorized in this batch by B4-D1 and the numbered DECISION-1 at coordinator `418321a5e1`. At packaging, `8e0e2198d8` (extraction inverse), `5b73480d43` (isolated retention) and `733cec2bb0` (ID-only narrowing) are candidates for a separate PR ahead of acquisition. The focused review matrix is declared handle strings, existing result/IDable forms, inline object maps and raw ParentFields, nullable values, object lists, and declared interfaces. The existing handle/relocation checks and new inline-map regression pass; the new regression fails against the preceding broad retention implementation.

`restoredDelegation` marks only a validated pure JSON mapping while publishing each decoded row under P. It is not a global boot scan, does not load the parent, and performs no body or provider work. The admitted flag enables only the closed ten-field mapping after local restore.

The fixture additions beyond delegation's `Source` are `SnapshotID`, the `producer-ref-released` and `producer-ref-release-error` kinds, `TransferFixtureProducerReleaseObserver`, and dependency mappings from `fixtureImportedMappings`. Batch 7 must account for these when closing its report vocabulary. Dependency mappings now validate the computed allocation against an independently reported non-root row (including call type/field and numeric receiver where present), and refuse to report an unvalidated non-root closure. The gated release observer substitutes the concrete ref type only on the private Container producer's FS handle after successful execution. The receiver's accessor, ordinary FS, private execMeta and mounts keep their own ref types. The focused assertion checks that scope and observes the underlying release before reporting it; the mixed engine proof separately checks actual redundant-FS release and ownership order.

Two production exported surfaces beyond the named addenda remain: `PersistDecodeContext.WithSnapshotRoles` and `ClonePersistedSnapshotLinks`. The latter is a facade over the single internal snapshot-link copy helper; it adds no second slice-copy site. The sessionless constructor's new outside-E wrapper and prepared-lookup argument to its locked helper are private; Prepare/Commit/Finish public arity is unchanged. Its real Directory checkpoint/reopen test uses an exported bridge only in a `_test.go` file, not a production API.

The shared `resourcePin` release contract retries failed removal using its mutex/released flag instead of permanently memoizing failure. This also affects ordinary imports and is within the independent-owner contract. Shared pull paths carry `chainMode`; ordinary image imports pass false and retain their existing error classification and chain reuse. Native completion now calls D2 after owner sync and operation cleanup, propagating settlement errors through retained native bookkeeping. None of these changes adds renewal workers, batch 5 episode state or batch 6 ordered preparation.
