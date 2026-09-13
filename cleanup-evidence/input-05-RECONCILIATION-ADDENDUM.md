# Reconciliation addendum to REVIEW.md

Fresh cleanup reviewer (Fable 5.1, xhigh), 13 September 2026. `REVIEW.md` is preserved unchanged; this addendum records the council reconciliation. Candidate unchanged: `1ba150a79b8a3f7568e27a22cee492f578ffa538`, tree `d5a73331e65629681246351a864fd2ecda9b899e`. No tests were rerun for this addendum; no native or model runs; no helper staffing.

## Confirmation of my completed result

My independent review stands as written: no blocking findings; two should-fix items (capture ignores attachment state; two stale internal-doc passages); no residue of removed work; unit, race and vet checks passing at the candidate; native coverage an explicit, unclosed limit after the interrupted run. The coordinator has accepted the internal-doc item as a bounded should-fix after checking the passages.

Inputs read for this addendum: the coordinator's `COORDINATOR-CLEANUP-REVIEW.md`, both probe sources, `probes-v2.log`, `validation-capacity-note.md`, and the scope auditor's `RECONCILIATION-ADDENDUM.md`. I then re-read the candidate source for the direct evaluation path rather than rerunning the probes, which two council members had already run independently with matching output.

## Finding 1, attachment state: concur

Identical to my should-fix 1. I adopt the auditor's scope caution: check the captured row's own attachment state only; do not recursively require clean attachment of dependencies in this fix. Bounded correction as already described in `REVIEW.md`.

## Finding 2, direct object-side evaluation: concur, and a correction to my own review

My review verified that the capture's `lazyMu` hold excludes cache-side attempts and that lock order is safe. It did not consider the object-side entry points that run the same group bodies without a cache attempt. That was an omission on my part; the coordinator's probe and the auditor's trace are right.

What I verified from source:

- `Container.evaluatePartsDirect` (`core/container_parts.go:537`) and `Container.Evaluate` (`core/container.go:1100`) run group bodies through `LazyState.EvaluateGroup`, which holds only the per-group mutex across the body and publishes no cache attempt. Capture's readiness checks look only at cache-side attempts and bookkeeping, and `lazyMu` does not order these runners.
- The bodies write shared fields in place before completion: template-A metadata groups first re-materialize metadata from the parent and then apply the op (`core/container_parts.go:977`), and the exec, image and restore bodies set accessors and mount lists. The encoder reads `Config`, mounts and part descriptors with no lock (`encodeContainerMetadata` at `core/container.go:1365`, `encodeContainerParts` at `core/container_persistence.go:312`). A concurrent capture is therefore a data race and can serialize a torn payload; the probe's `WorkingDir=/half-written` with `consumed=false` is exactly that.
- Production reachability, confirmed: the direct exec-metadata reader is behind `Stdout`, `Stderr`, `CombinedOutput` and `ExitCode` (`core/container_exec.go:2435-2447`); the non-legacy resolvers evaluate through the cache first, but the legacy views call the value directly (`core/schema/container.go:1666` and the legacy stderr sibling). The export, publish and tarball paths call `getVariantRefs` (`core/container.go:6688`, `6745`, `6964`, `core/container_image.go:373`), which forces metadata and rootfs directly on each variant. All of these run inside session operations, so the quiescent shutdown persister is unaffected; only live capture is exposed.
- Every production container op except one is refined (implements `EvaluateContainerGroup`); the exception is `ContainerImportLazy`, whose whole-op body runs under `LazyState.Evaluate`, which holds the op's `LazyMu` across the body.
- Directory and File have no direct evaluation entry points: their bodies run only through `LazyEvalFunc`, which the cache invokes, and I found no core caller of a lazy body outside the cache for those types. The gap is specific to Container.

Classification: a correctness defect in the retained one-row capture primitive's own contract ("no evaluation, ready state only"), on existing production paths. Should-fix, same standing as finding 1. Not residue of removed work and not the missing exporter or acquisition feature.

## Bounded correction scope for finding 2

I agree with the auditor's constraints and add these specifics for the implementer:

- Put the exclusion on the value side, in the Container codec or a small helper it calls, using the latches that already exist: the per-group mutex that `EvaluateGroup` holds across a body, and the op's `LazyMu` for the one unrefined op. Use try-lock only, so capture never blocks behind a running body; a busy latch means `ErrPersistStateNotReady`. Hold acquired latches across the encode and release them afterwards, so no direct body can start writing mid-encode. Ensure the metadata group's latch entry exists before trying it, the same way `EvaluateGroup` creates it, so a first-time runner cannot slip past. Because runners hold at most one group latch at a time and never wait on the capture, try-locking several latches from the capture side cannot deadlock.
- Keep the shutdown persister's behavior unchanged; at quiescence every try-lock succeeds, so its output is byte-identical.
- Do not route direct evaluation through the cache. Those callers hold the container value without its result handle; changing that is a lazy-kernel regime change, well beyond cleanup, and would touch modeled behavior.
- Directory and File need a verification statement in the change description, not code.
- Proportional regression checks: a unit test adapted from the coordinator's probe expecting `ErrPersistStateNotReady` during a parked direct metadata body, a second case showing capture succeeds with the final value once the body completes, and a `-race` run of the capture and producer tests. No native or model run is required for this correction.

Expected size: on the order of forty to sixty production lines plus tests, in `core/container_persistence.go` and possibly `core/lazy_state.go` for a try-lock accessor. Within the Human's "further cleanup" process for a fresh implementer and the same council.

## Disagreements

None material. Two refinements to the coordinator's wording: the Directory/File check is verify-only, and routing direct evaluation through the cache should be excluded from the implementer's options, not merely disfavored.

## Unchanged limits

The interrupted native run remains invalid coverage and is not to be repeated during cleanup. The end-to-end restart-persistence subtests for the changed encoders remain unexercised on this source; that limit is recorded, not a code change.
