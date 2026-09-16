Batch 5 remains paused. This evidence-only plan maps completed commission step 1, commit `9a3cddf0ce6e9eb1723c78012b885f82b4bede92`, and the remaining steps onto the converged Lazy interfaces. No code, reset, build or test is part of this preparation. The two initial foundation commits, `3cc02ac970` and `782fc85d48`, do not establish the final interface; resume awaits the coordinator's named, reviewed head. Preserve existing commits and port their changes only when instructed.

Authority read: `git show 2cbca74cb354e8f70eeb265e16b4ab92c74e6c07:hack/designs/remote-cache/focused/08-lazy-values.md`, §§5, 6 and 6.1; `git show db36af0e45:hack/designs/remote-cache/proposed-design-explained.md`, §§12.1 and 18.1. Batch 5 design references below mean `git show 716a6311da:hack/designs/remote-cache/focused/05-offers-renewal.md`; commission order remains that at `865a8101e1`.

The existing offer implementation needs these coordinated substitutions, with the same admission decisions and ownership:

| Current use | Port on the reworked foundation |
| --- | --- |
| [Gate comparison](dagql/cache_offer.go#L74): `ProducerAddress`, `producerAddressKey`, `ProducerRunning`, `ProducerConsumed` | `LazyGroupAddress`, `lazyGroupAddressKey`, `LazyEvaluationRunning`, `LazyEvaluationEvaluated`. Preserve explicit overlap and the admission-side conservative whole-result overlap. |
| [Phase tests](dagql/cache_offer_test.go#L18): `ProducerPhase`, `partProducerState`, all four phases | `LazyEvaluationPhase`, `partLazyEvaluationState`, `LazyEvaluationOpen/Preparing/Running/Evaluated`; rename the `consumed` case to `evaluated`. Keep the same accept/refuse expectations. |
| [SourceCheck test](dagql/cache_offer_test.go#L55) and [native exclusion test](dagql/cache_offer_test.go#L211) | Replace group addresses and use the foundation's renamed private task-key convention, consistent with `lazyEvaluationTaskKey`. Keep `RunLazyTask`, `PrepareOriginal`, `BeginOriginal` and the rejection of a stale source check. |
| [Pure admission probes](dagql/cache_offer.go#L162) | Use renamed route/probe surfaces: `LazyOperationRoute`, `HasLazyOperation`. Keep complete address mapping without metadata evaluation; operation presence alone establishes neither output completeness nor donor unreadiness. |

`OfferParts`, `PartContentSource`, `TryAcquire`, `InstallReadyPart`, `PartSourceLease`, offer revisions and D2 settlement retain their contracts. Keep `PreparedReadyPart.original`, `OriginalPermit` and `beforeSyncCleanup`; the naming change does not remove private-run authorization or cleanup. Retest native admission against actual Lazy entry after the port.

Remaining commission steps, in order:

1. Step 2 adds the separate renewal-episode set beside batch 4's source/address/content/admitted-revision exhaustion set. Mailbox, epoch, FIFO capacity 64, one two-second deadline, cancellation and reply discard are unchanged; none depend on the old route name.
2. Step 3 reconciles the placeholder source to the design's concrete `PartContentSource`, accessor and `time.Time` argument, preserving fixed-address operation before attachment. Implement `Available`, `Provider`, both uses of `renewalRetryStatuses`, the 30-second byte-progress idle bound and `ChainContentError` classification as commissioned. Exhaustion falls through the existing dispatcher to the receiver's Lazy operation, prepared through `PersistedLazyOperationFactory.PrepareLazyOperation` and run through `LazyOperationInvocation.Run`.
3. Step 4 retains source construction, bridge attachment, server adapter and Stop. The separate shutdown commit still un-drops the pre-existing accumulator for every engine and says so in its message. Restart/onward tests use the foundation's `OperationState` / JSON `operationState` and format cut (schema 21, envelope 5, bundle 2 from the stated baseline, or the agreed successors). Batch 5 adds no further version change. Preserve installed ownership, pending siblings, retained operation bytes and direct inputs; offers remain separate owners.
4. Step 5 runs the commissioned focused matrix after resumption, with real snapshot peers and the prescribed mount environment. Update fixture observations to `lazy-enter`, `installed-lazy`, `lazy-ref-released`, `lazy-ref-release-error`, `TransferFixtureLazyReleaseObserver` and `partFixtureLazyContext`. Verify actual private/native Lazy entry, not synthetic installation bodies; keep the existing report groups. Preserve failures and command logs in the final separate evidence commit.

Read these batch 5 sentences under the new names:

| Design lines | Required reading |
| --- | --- |
| 15, 50, 55–58, 63 | The operation's Lazy evaluation gate; preparation means `PrepareLazyOperation`. Open/Preparing/Running/Consumed become Open/Preparing/Running/Evaluated with unchanged transitions. A missing required output after successful execution remains an invariant error; the new name grants no reopening. |
| 71, 210 | Direct dependencies belong to the retained Lazy operation. They remain distinct from offer-owner holds. Line 210's old version numbers are superseded by the foundation compatibility cut, while offer records remain unchanged. |
| 81, 230 | Installed-output bookkeeping never causes a second Lazy evaluation or another chain attempt. |
| 109, 123, 220, 226 | Exhaustion releases temporary ownership before the next route or Lazy evaluation. Preserve own completed output → eligible completed equivalent → usable chain → own Lazy operation; operation-less children retain exact mapped-parent delegation afterward. |
| 244, 256, 261, 267, 271 | Rename preparation, phases and fallback observations; preserve barriers, cancellation and sibling regressions. Forced content failure must reach the actual Lazy operation. |

Persisted `operationState: evaluated` describes saved operation success, not B's local output availability or fresh private latch. Conversely, a retained evaluated Lazy does not invalidate completed output. These distinctions govern admission, fallback and restart tests; no field-name inference or schema-resolver reconstruction is introduced.
