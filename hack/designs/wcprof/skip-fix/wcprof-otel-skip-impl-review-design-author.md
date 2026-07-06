# Skip-fix implementation plan — design-author review

Reviewed `wcprof-otel-skip-impl-plan.md` in full against the implementer worktree
(`wcprof-otel-skip-implementer-a7daa7c9-239fd90a`, HEAD `4585bf413d`) and the canonical
design + invariants. Every load-bearing claim re-verified at file:line.

## Verdict

**Approve the architecture; refine the predicate.** The static per-recipe cut, stamped
on `CallRequest.SkipProfile` in `AroundFunc`, gating call_exec+publishResult+every wait
across singleflight+lazy and both sources with **zero loader/replay change**, is correct,
self-consistent by construction, and actually removes the volume amplifier. It honors the
no-inference principle (verified: nothing in `engine/wcprof/wcanalyze` or `wcotel/loader`
changes). It is a genuine improvement over my own prior review's recommendation (below).

My one substantive change is the **predicate itself**: the lead's point A is correct and
verified, and it couples with the two calls that are mine (B, C) into a single cleaner
choice — **a separate, debug-independent, receiver-type profiling predicate**, rather than
extending the debug-gated `introspectionInfo`. That dominates the doc on all three axes
and dissolves the doc's own biggest self-flagged risk (§10 debug non-uniformity). Details
below.

## Canonical alignment + integrity

- **Canonical design doc NOT edited:** `hack/designs/wcprof-otel-design.md` is untracked
  on the implementer branch and in my worktree (verified `git ls-files` empty both
  places). The plan is a separate file. ✓
- **Zero loader/replay change — verified:** the plan touches `core/telemetry.go`,
  `dagql/cache.go`, `dagql/call_request.go` only; no `wcanalyze`/`wcotel/loader` edits.
  Aligns with the governing principle (the analysis is a rational function of faithful
  data; we change *what we emit*, never the analysis). ✓
- The static-cut self-consistency (§4) is exactly the Invariant-E (parent) / Invariant-T
  (wait-target) closure the canonical gate enforces; the §6.1 faithfulness counters stay
  0 by construction. ✓ One canonical reconcile this *implies* (for later, not now): record
  the profiler-skip predicate as an emit invariant ("the introspection/schema-walk class
  is not profiled; faithfulness counters remain 0").

## What I verified and agree with (the lead's AGREE set)

- **Static per-recipe cut + §4 acid test — sound, and better than my prior recommendation.**
  The key insight (to wait on cache key `K` you must call `K` → a waiter's classification
  equals its target's, so a non-skipped op can't hold a wait into the skipped set) holds:
  `ongoingCalls` is keyed `{callKey, concurrencyKey}` with `callKey` the recipe digest
  (cache.go:3669,3674-3677), and waits target the shared work (cache.go:3958). **Credit
  where due:** the doc correctly rejects my earlier "key on `oc.profSkip = IsSkipped` at
  claim" (dynamic) recommendation — §4.2 shows that cut would *race-skip real shared work*
  (a `clone`/dep-load claimed under a `hideCtx` is `IsSkipped` but is genuine work), making
  its profiled-ness nondeterministic. The static recipe cut profiles that work
  deterministically (its recipe isn't introspection) and still can't dangle. This is the
  right call and a real improvement.
- **Lock-safety basis — verified.** `beginOTelCallExec` mints under `callsMu` (cache.go:3733,
  Invariant T), and the predicate's `introspectionInfo`→`ReceiverCall`→`resultCallByResultID`
  takes `egraphMu.RLock` (result_call_frame.go:1381). A cache-side predicate would nest
  `egraphMu` under `callsMu`/`lazyMu` — a real ordering/contention hazard. Stamping in
  `AroundFunc` (outside cache locks, cache.go:656/678 lineage) is the correct seam. ✓
- **Distinct skip-flag vs `execSpanCtx.IsValid()` (§5.5) — correct.** Keeping the flag
  separate preserves the targetless-wait mixed-recording detector. ✓ (I made this point
  last round; the doc implements it faithfully.)
- **chunk4 "open-subtree orphans children" was mechanically wrong — verified.** Skipping
  mints no span and does not reassign `callCtx` (cache.go:3732-3733), so the resolver runs
  under the parent's still-current span and children record a **present** parent; the loader
  makes an op from every span and only orphans on an *absent* parent span (loader.go:251,
  302-305). Re-homing handles "open subtrees." The exclude-conclusion stands (for the
  pragmatic reasons in §3.3), but the rationale is corrected. ✓

## Point A (debug-gating) — STRONG AGREE, verified; and it forces B

**The doc's debug-orphan justification is unfounded — confirmed at file:line.** §10 claims
a debug-independent predicate would skip `call_exec` while the normal span records, so a
kept child "parents to a non-op span → OrphanedParents." But the loader builds `opIDBySpan`
for **every** deduped span with no kind filter (loader.go:251-254), and `OrphanedParents++`
fires only when the recorded parent **span is absent from the capture** (loader.go:302-305).
A normally-recorded `dag.call` span is a present op, so a child of it never orphans. The
only justification offered for debug-gating therefore collapses.

Consequences (I agree with the lead and add force):
- **Debug-gating's unflagged cost:** the doc frames "debug re-profiles introspection" as a
  bonus, but it re-introduces the ~33k-span amplifier in debug → BSP overflow → the exact
  orphan/unresolved-target capture loss this fix cures → **debug captures gate-FAIL.** The
  "bonus" produces unusable data; it's a footgun, not a feature.
- **Debug-independent dissolves the doc's own biggest risk (§10).** A debug-independent
  predicate is a *pure* function of the recipe, removing "debug baggage is the one
  non-recipe input" entirely — the static-cut determinism becomes unconditional.
- **A forces B:** `introspectionInfo`'s receiver-type switch is debug-gated (telemetry.go:402)
  for the UI; you cannot both reuse it and be debug-independent. So debug-independence
  requires a predicate that is *not* the debug-gated `introspectionInfo` — i.e. a separate
  (or debug-independent) classifier. This is the bridge to point B.

## Point B (UI-coupling) — MY CALL: ISOLATE (separate profiler predicate)

Extending `introspectionInfo` (§3.2a) also suppresses directly-called `TypeDef.asObject`/
`Function.args` from **normal/UI** telemetry — coupling a profiler-volume fix to a
user-facing telemetry change. **As design owner: keep it isolated.** Use a separate
`profSkip(call)` predicate; leave `introspectionInfo` and normal telemetry untouched.

Reasons:
1. **Separation of concerns / the core invariant.** The OTel profiler is an *optional second
   consumer* that READS the engine's telemetry and ADDS profiler spans. It must not
   SUBTRACT normal/UI spans. Changing what the UI shows is out of the profiler's lane.
2. **Blast radius.** `introspectionInfo` also feeds the UI and (adjacent) the dedup path;
   editing it for a profiler reason risks unrelated behavior. A separate predicate is
   contained to the emit gate.
3. **The doc's "single source of truth" argument actually cuts the other way.** UI
   suppression *wants* debug-gating (show introspection in debug); the profiler *wants*
   debug-independence (stay volume-safe). Those are two different truths — one classifier
   can't serve both without the profiler inheriting the UI's debug-gating, which is exactly
   the point-A problem. Separate responsibilities → separate predicates. They can share the
   type/field constants (DRY) without being the same function.
4. The doc calls the UI change "benign noise reduction," but that is a product/UI judgment
   to make on its own merits, not a side effect of a profiler fix.

## Point C (field-set scope) — MY CALL: receiver-TYPE (3.2b), enabled by isolation

3.2a (named accessors) is workload-specific and needs an ongoing "refine driven by §9
re-measure" loop — a maintenance burden **and** a silent-regression risk (a new workload
surfaces uncaught schema-walk accessors → volume creeps back, only caught if someone
re-measures). 3.2b (classify by receiver TYPE — any field on `Function`/`TypeDef`/
`FunctionArg`/`*TypeDef`) is complete and robust. The doc prefers 3.2a *only* because 3.2b
"broadens normal-telemetry suppression more" — **but once the predicate is isolated (B),
that objection evaporates entirely** (a separate profiler predicate touches no normal
telemetry). So with isolation, 3.2b is strictly better: complete, no field-chasing, no UI
impact.

**A + B + C converge on one design:** a **separate, debug-independent, receiver-type**
profiler predicate — `profSkip(call) = (receiver == nil && field ∈ introspectionRootSet)
|| (immediate receiver type ∈ reflectionTypeSet)`. It is still a pure function of the
recipe, so the §4 acid test holds unchanged (waiter shares recipe → same classification).
Bonus: it needs only the *immediate* receiver type (one `ReceiverCall`), not
`introspectionInfo`'s full chain walk — cheaper, which also helps point D. This dominates
both 3.2a and 3.2b, keeps the UI untouched, and is fully deterministic.

## Point D (perf) — AGREE

Stamping moves the receiver walk (an `egraphMu` lookup) ahead of the `IsSkipped` early
return (the stamp must precede it, §3.4 — correct), so it now runs for the ~33k
inherited-skip descendants that currently early-return. The doc defers measurement; I
agree with the lead: **add the `!IsRecording` short-circuit in v1 and measure.** If no
recording span is present, no profiler span will emit regardless, so the classification is
wasted — and `IsRecording` is stable across the `AroundFunc`→`getOrInitCall` lineage. The
receiver-type predicate (immediate receiver only) is cheaper than the full-chain
`introspectionInfo`, which mitigates but does not remove the concern; short-circuit + a
real before/after profile on the module-load workload, in v1.

## Point E (exec-split / service-start) — AGREE

§6's "they never emit a skipped-class span → no gate needed" is a plausible assumption
(introspection doesn't trigger container-exec/service resolvers) but is currently just
asserted. Make it an **explicit hard §9 check**: confirm on the exec-heavy and
service/lazy captures that zero skipped-class spans originate from `engine/engineutil`
exec-split or `core` service-start. If one ever can, the same work-flag gate applies there.

## Additional issues I raise (beyond A–E)

1. **AroundFunc registration completeness — a real dependency to verify (the flip side of
   the lock-safety win).** The `CallRequest.SkipProfile` seam *relies* on `AroundFunc`
   stamping the bit. Any **recording** path that reaches `getOrInitCall` *without* going
   through `AroundFunc` leaves `SkipProfile=false` → introspection profiled → the volume
   regression (and BSP overflow) persists on that path. The doc notes `cmd/introspect`
   lacks `AroundFunc` (fine — non-recording), but it should **affirmatively confirm**
   `AroundFunc` (via `srv.Around`) covers every recording-engine path into `getOrInitCall`,
   not just the two cited registration sites. The rejected cache-predicate alternative
   wouldn't have this gap (computed at the cache site); the chosen approach trades that for
   lock-safety. Acceptable — but verify it, and add a §9 assertion that the post-skip
   capture has **zero** `dag.call=0` introspection `call_exec` names remaining (which would
   also catch an unstamped path).
2. **Lazy reach identity — verify.** §3.4 sets `sharedResult.profSkip = req.SkipProfile`,
   relying on `req.ResultCall` being the same call as the `resultCall` used for
   `profCallClass`. If they can differ, the lazy skip could mismatch the recipe → a dangle.
   The doc asserts identity; confirm it on the §9.3 lazy capture.
3. **This touches the SHARED native recorder.** Gating native (`wcprof.BeginOp`/`pubOp`/
   `BeginWait`) on the same flag (cache.go:3717, 3941, 2998, 3094) changes native dumps
   (introspection ops vanish there too). That's correct for oracle comparability and native
   is dev-only — but it modifies validated PR #13393 behavior, so it deserves the same
   "shared native code" scrutiny the chunk4 replay changes got. Flag, not block.

## Bottom line

Architecture: **approve** — static per-recipe cut, `CallRequest.SkipProfile` stamp,
gate-everything across both sources, zero analysis-side change; it solves the regression,
stays zero-inference, preserves user-work-first-class (introspection time folds honestly
into the kept ancestor's self), and is moderate/localized. **Refine the predicate** to a
separate, debug-independent, receiver-type classifier (A+B+C converge): orphan-safe
(verified), deterministic (dissolves §10), UI-untouched (B), complete/no-field-chasing (C),
and cheaper (D). Add the `!IsRecording` short-circuit + measure (D), make exec/service a
hard §9 check (E), and verify `AroundFunc` registration completeness + the lazy-call
identity before landing. No conflict with the canonical design or the no-inference
principle.
