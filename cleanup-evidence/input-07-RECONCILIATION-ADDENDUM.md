# Reconciliation addendum: the coordinator's two capture findings

Scope auditor fork, 13 September 2026. Candidate unchanged: `1ba150a79b8a3f7568e27a22cee492f578ffa538`,
tree `d5a73331e65629681246351a864fd2ecda9b899e`. This addendum supplements `REVIEW.md` (kept as written);
it does not reopen the residue conclusions there. Inputs: the coordinator's `COORDINATOR-CLEANUP-REVIEW.md`,
its two probe sources (`attachment_probe_test.go.txt` sha256 bee03c37…, `direct_capture_probe_test.go.txt`
sha256 9bbcaacb…), `probes-v2.log`, and the candidate's source. No native, model or build-heavy checks were
run for this addendum; the one probe run below is a focused unit-level test (evidence/probes-repro.log).

## Verdict

Both findings are genuine correctness defects of the retained one-row capture primitive
(`dagql/cache_persistence_capture.go`, commit `1ba150a79b`), not residue of removed work and not the missing
graph or acquisition features. They violate the primitive's own documented contract ("a row being evaluated
reports ErrPersistStateNotReady"; "copies one registered row without evaluating it") on existing production
paths. I concur with the should-fix classification. Bounded fixes belong inside the capture primitive and,
for the second, the Container codec or its existing evaluation latches; they are within the Human's
"further cleanup" process (fresh implementer, same council), not a scope expansion. Finding 2 also
corrects a premise in my own review 14, stated below.

## Independent reproduction

I ran the coordinator's probe sources against my clean checkout of the candidate through a Go overlay
pointing at my worktree paths (evidence/probe-overlay.json), production blobs untouched:

    go test -p 1 -overlay <overlay> ./dagql ./core -run '^TestReviewCaptureDuring' -count=1 -timeout=120s -v

Both probes fail exactly as in the coordinator's `probes-v2.log`: capture returns a record with
`attachment=1` (open) in the first, and a record with `WorkingDir=/half-written` and `consumed=false` in
the second, where the probes expect ErrPersistStateNotReady (evidence/probes-repro.log, EXIT=1 by design).

## Finding 1: capture during an open initial attachment (concur)

- Publication registers the row and takes the handoff hold under egraphMu, then creates the open
  dependency barrier (`attachDepsWaitCh`, cache.go:5750-5754), releases egraphMu, and runs
  `attachDependencyResults` outside the lock (5758). That attachment rewrites the object's declared
  references in place (for example `mnt.CacheSource.Volume = typed`, container.go:1140-1190 region), so an
  encode during the window reads references mid-rewrite or hits a not-yet-attached one.
- The rest of the cache treats such rows as not servable: the persist worker marks any non-clean row
  invalid (cache_persistence_worker.go:291), digest lookups skip failed rows (cache_egraph.go:611, 635),
  and publication adoption requires clean attachment (cache.go:2584).
- Capture checks registration only (capture.go:29-33). The probe uses the existing `onAttach` test hook
  to park attachment and takes the registered row from `resultsByID`; that is a legitimate stand-in for a
  handle obtained through a lookup or by-ID load while attachment is open.
- Bounded fix: after the registration check and hold, consult `shared.attachmentState()` (an existing
  accessor, already nested under egraphMu elsewhere): open → ErrPersistStateNotReady; failed → error. No
  new lock, state or interface. A regression test can be adapted from the probe using the existing hook.
- Scope caution: check the captured row's own state only. Do not require clean attachment of its
  dependencies recursively here; the later graph capture holds and checks each row it selects.

## Finding 2: capture's lazyMu does not exclude direct object-side evaluation (concur, with my correction)

- Review 14 asserted that holding a row's dagql `lazyMu` blocks publication of a new attempt, "which is
  the only way object-side state changes for a row without an attempt." That is wrong for Containers.
  `evaluatePartsDirect` (container_parts.go:537-545) and `Container.Evaluate` (container.go:1104-1111) run
  the same group bodies through core's `LazyState` latches with no dagql attempt. Production callers on
  published rows: `stdoutLegacy` → `parent.Self().Stdout` → `metaFileContents` →
  `evaluatePartsDirect(ContainerPartExecMeta)` (schema/container.go:1666, container_exec.go:2461-2462), and
  `getVariantRefs` → `variant.evaluatePartsDirect(Metadata)` then `(Metadata, FS)` (container.go:6877, 6888).
  The non-legacy `stdout` resolver evaluates through dagql first (1653), so the direct call there is a
  no-op; the legacy view and the export path are not covered.
- Real bodies write shared fields before completion: template A first re-materializes metadata from the
  parent and then applies the op (container_parts.go, evaluateTemplateAContainerGroup), and
  `UpdateImageConfig` assigns `container.Config` in place (container.go). The codec reads `Config`,
  mounts and accessors without a lock (encodeContainerMetadata, encodeContainerParts). A capture
  concurrent with a direct body is therefore a data race and can produce a torn record, which is what the
  probe shows with a controlled body in the real `LazyState.EvaluateGroup` path.
- Semantic consequence on a receiving engine: for template-A metadata ops the recipe re-materializes
  metadata from the parent before applying, so a torn `consumed=false` metadata value would be overwritten
  when the recipe runs; the durable harm is the race itself and records whose metadata does not match any
  state, plus torn part or accessor reads for snapshot groups. That is enough to require a fix in a
  primitive whose contract is "no evaluation, ready state only."
- Other capturable types: Directory and File bodies run only through dagql's `LazyEvalFunc` (no direct
  Evaluate or Sync on those types; verified in reviews 09 and 10), so `lazyMu` covers them. `Changeset.Evaluate`
  calls `cache.Evaluate` (dagql). `Module`, `ModuleSource`, `Service`, `EngineCacheEntrySet` and
  `TerminalLegacy` `Evaluate` wrappers run no lazy body. The gap is specific to Container's two direct
  entry points.
- Bounded fix shape (the implementer chooses; constraints only): either make attached containers' direct
  evaluation go through the dagql attempt so `lazyMu` excludes it, or let the Container codec, or a
  minimal value-side readiness check, use the existing per-group latch mutex that `LazyState.EvaluateGroup`
  already holds across a body (try-lock busy → ErrPersistStateNotReady, otherwise hold across the encode)
  and the whole-op `LazyMu` that `LazyState.Evaluate` holds for unrefined ops. Keep per-group parallelism,
  no evaluation on capture, no new locking regime or state, no change to the quiescent persister's
  behavior, no graph or acquisition machinery.

## What this does not change

The residue and regression conclusions of `REVIEW.md` stand: no removed-work residue, model and schema
untouched, unit tests and vet passing at the candidate (evidence/unit.log, evidence/vet.log). The two
findings are defects in new retained code, to be fixed by the separately staffed implementer and returned
to the same council. The coordinator's view that my stale-baseline-page observation is non-blocking is
correct; it revives no obligation.

## Council scope assessment (after the fresh review)

Input: the fresh reviewer's `reviews/remote-cache-cleanup/REVIEW.md` (read in full). Its documentation
finding is verified at the candidate: `internal-docs/cache_persistence.md:413-414` and
`internal-docs/lazy_evaluation.md:476-477` still say a Container keeps "its original recipe only while
computation remains", and the Directory/File snapshot-form description omits the producer kind and bytes
added by `5c3fe15eb6`. No file under `internal-docs/` changed between `17f7dd89f4` and the candidate, so
the retained commits changed the persisted representation without updating the documents the
engine-debugging skill names as the current mental model. A few-sentence correction is cleanup
completeness, not new scope.

Proposed bounded set for a separate author: (1) attachment-state readiness check in
`CapturePersistedRecord`; (2) exclusion of Container's direct object-side evaluation from a live capture;
(3) the two internal-doc passages plus the Directory/File snapshot-form sentences; (4) the baseline
historical HTML row stays non-blocking; (5) no graph or acquisition work.

Verdict: no material scope concern. All three items are reproduced or verified defects in retained code
or stale statements about it, each has a bounded correction, and none revives removed work or reaches
into the excluded draft (the attachment check must be written fresh, as both reviews say). Two
constraints the council should give the author so the set stays bounded:

- For item 2, the fix must be capture-side readiness using the existing core latches (the per-group
  mutex `LazyState.EvaluateGroup` holds across a body, and the whole-op `LazyMu` for unrefined ops), not a
  rerouting of `evaluatePartsDirect` or `Container.Evaluate` through dagql attempts. Rerouting would change
  how local evaluation runs for attached containers, which is a behavior change outside remote caching
  and exactly what the Human forbade. The quiescent persister's behavior must not change.
- For item 3, correct only the affected passages; do not describe graph transfer or acquisition as
  existing behavior, and do not broaden into a rewrite of the internal documents.

The fresh reviewer's optional nit (refuse a frame-less non-Query row in capture as the persister does) may
ride in the same change; it adds no scope. Its recorded validation limit, the engine-level restart
subset never run on final `8d144785d4`/`5c3fe15eb6` source and interrupted by the disk incident, is a
completion condition, not code: one bounded run of that subset on the fixed candidate, when capacity
exists, closes it for both the retained commits and the fix commits at once.

## Validation severity: is a native restart-subset pass required to close this cleanup review?

Answer: no. It is neither blocking nor should-fix for closing the cleanup review. It is a validation
limit to record now and to close with one bounded run before publication or Human review of the feature,
when host capacity exists. Reasoning, from source and existing evidence at `1ba150a79b`:

What the changed encoders do to persisted data: completed Container, Directory and File rows now carry
producer bytes (`lazyJSON`, plus `lazyKind` for Directory/File) that were previously absent; pending rows
are unchanged; snapshot links, roles, paths and the `SnapshotLinks` returned to the persister are
unchanged. `371c77af48` changes only the exec metadata inside pending exec bytes. `c7e20ab5d4` adds one
labelled extra on the pinned `Container.from` identity. The commissioned fixes change capture readiness and
documentation, not payload fields.

Why existing evidence bounds the risk:
- The new bytes are inert on the local paths. No production code calls `VisitEncodedReferences` (no
  boot-time walk of `lazyJSON`); boot import decodes payloads on hit and eagerly only for absent values;
  complete rows keep the bytes without decoding them (`container.go:1545-1552`, `directory.go:334`,
  `file.go:313`), so no ancestor is loaded and no reference in those bytes is resolved locally. The
  `persistedFamiliesTestEnv.restart` fixture runs the real `Cache.Close` (real `persistCurrentState`) and
  the real `NewCache` reopen (real `importPersistedState`) over a SQLite file with every core class
  installed on a real dagql server; only the snapshot manager is a fake, and the manager interface calls
  are unchanged by these commits. `TestContainerCompletedProducerPersistsWithoutLoadingParents`,
  `TestFilesystemCompletedProducerPersistence` (Directory, File and container-rooted producers),
  `TestContainerExecPersistsInputMetadata` and `TestRemoteCacheExtraDigestMetadata` exercise exactly the
  changed encode, flush, import, decode-on-hit, open-failure, retry, re-encode and relocation paths with
  real close/reopen; the four-package suites and the capture race run pass at the candidate.
- The only new failure class is a retained live op that fails to encode on a complete row, which would
  fail the flush at close (`failed to persist dagql cache during close`, persistence left dirty).
  Source bounds it: the only encode errors are nil or unattached references (`encodePersistedObjectRef`),
  impossible for published rows whose parents, module contexts, secrets and services are attached; the
  Directory and File typed encoders cover every production lazy type except the restore lazies, which are
  excluded from capture; `ContainerExecLazy` with a FunctionCall is never a cache result (reviews 07, 08,
  10 and the takeover audit agree).

The concrete residual risk, stated plainly: a real engine produces row combinations the unit fixtures do
not (containers referencing services, secrets, sockets, module contexts and volatile exec cache hits) and
uses the real snapshot manager and lease sync. Nothing in the seven commits touches snapshot links, lease
sync or the opening code, so the real-manager surface is the unchanged baseline's; the retained-op encode
over real object graphs is the one surface the unit fixtures approximate rather than reproduce. That is a
narrow, source-bounded gap, appropriate to close with one bounded native run of the restart subset on the
fixed candidate (it will cover the retained commits and the fixes together), not a reason to hold the
cleanup review open or to run a build-heavy suite on a host with 9.6G free.

This matches the Human's proportionality direction (9266: judgment and efficiency, no dropping of
validation or quality) and the original guidance G28 (evidence is slice-specific). It is my recommendation
on severity; the Human decides what gates publication.
