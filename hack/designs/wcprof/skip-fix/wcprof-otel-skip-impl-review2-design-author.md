# Skip-fix implementation plan v2 — design-author round-2 review

Reviewed v2 in full against the implementer worktree (`wcprof-otel-skip-implementer-a7daa7c9-239fd90a`,
HEAD `4585bf413d`). Verified the changes at file:line and did a fresh holistic pass.

## Verdict

**v2 correctly incorporated round 1; approve the design with one perf/determinism
refinement (the cheap receiver-type seam) and the open items resolved below.** The
A/B/C convergence (separate, debug-independent, receiver-type predicate) is realized;
N1/N2/N3/N4 are real and correctly specified; the three pushbacks are correct (including
the one against my own round-1 suggestion). UI-isolation is correctly realized (pushback
3, confirmed) and there is no conflict with the canonical design or the no-inference
principle (zero loader/replay change re-verified). The remaining substance is one
holistic finding that makes the perf story *actually* work and removes two caveats.

## 1. Changes — verified

- **Predicate flip (A/B/C) — correctly realized.** Separate `profileSkip` in `core`,
  receiver-type + shared root set, `introspectionInfo`/normal telemetry untouched (§3.2).
  The retraction of the v1 debug-orphan rationale is correct and I re-confirmed it:
  `opIDBySpan` is built for every span with no kind filter (loader.go:251-254) and
  `OrphanedParents++` fires only on an *absent* parent span (`cpSpan != "" && parentID==0`,
  loader.go:299-310) — a normal `dag.call` span is a present op, so no orphan. Debug-
  independence is the right call and dissolves the v1 §10 caveat.
- **N1 (outer native `OpKindCall`) — verified.** `getOrInitCall` mints
  `wcprof.BeginOp(OpKindCall)` gated at cache.go:3559 and passes `profOp` to the inner;
  the early-return path passes `nil` and the inner is nil-safe. Adding `|| req.SkipProfile`
  to the early return correctly skips the outer call; children re-home via `ContextWithOpID`
  (the skipped call pushes no op id, so sub-calls see the parent's). Oracle-symmetry fix is
  real and correct.
- **N2 (provenance) — verified, and dangle-safe by construction.** The three result-
  establishing paths are exactly as described: adopt-canonical (cache.go:4131 — leave the
  adopted result's flag intact; it's the canonical-equivalent recipe, so it matches),
  copy-frame (cache.go:4148-4153 — copy `profSkip` from the source `shared`), req-frame
  (cache.go:4159-4161 — `req.SkipProfile`). **Key point the doc gets right:** a wrong
  stored `profSkip` (e.g., import default-false) can never *dangle*, because the lazy span
  *and* every lazy wait gate on the **same** stored flag — so they are consistently
  present-or-absent regardless of whether the flag matches the recipe. Provenance is a
  *volume/classification* concern, not a safety one. The "profSkip travels with
  `resultCall`" invariant + the audit of the other construction/copy sites is the right
  framing.
- **N3 (lazy target-flag gating, load-bearing) — correct.** The §4.2 shared-recipe proof
  genuinely does not cover lazy (forcer ≠ producer recipe); closing it via the target's
  stored `shared.profSkip` is load-bearing, not "robustness." The instruction to comment it
  so no one "simplifies" the lazy gate to the waiter's own bit is exactly right. Timing is
  safe: `shared.profSkip` is set at result creation (`initCompletedResult`), and lazy
  forcing (the reads at cache.go:2964/2973/3017/3094) happens later, so the set precedes
  all reads — no concurrent-set race.
- **N4 (stamp coverage + zero-residual §9 check) — incorporated** as a hard merge gate
  (§9.2). Good — this is the backstop for the registration dependency I flagged in round 1.
- **`concurrencyKey = SessionID` (objects.go:603) — verified.** Singleflight sharing is
  within-session, so the §4.2 acid test holds with no cross-session split; cross-session
  concerns are confined to lazy/import, which N3 + the import edge handle.

## 2. The three pushbacks — all correct

1. **`!IsRecording` short-circuit is useless in production — correct, and I concede it.**
   Production always records, so the guard never fires; `!IsRecording && !wcprof.Enabled`
   likewise. My round-1 suggestion was wrong for the production cost. **However, the
   replacement (memoize by `(receiverTypeName, field)`) is *also* ineffective** — see the
   holistic finding §4.1: you must do the `ReceiverCall`/`egraphMu` lookup to obtain
   `receiverTypeName` (the memo key), and that lookup *is* the cost; the memo only caches
   the cheap set-membership. So neither my guard nor the doc's memo addresses D. The real
   fix is the cheap seam below.
2. **Don't fix chunk3's parentless-`publishResult` here — correct.** It concerns *kept*
   survivors' parentage, which this fix never touches; folding it in would couple two
   independent changes. And the doc's §5.6 reasoning is verified: a parentless
   `publishResult` has an **empty** `cpSpan`, so it does **not** trip `OrphanedParents`
   (loader.go:302 requires `cpSpan != ""`) — so the skip-fix's §9.1 gate-`0/0` is
   achievable *on its own* (the pre-fix orphans came from BSP-dropped parent spans, which
   the volume fix removes). A *fully* clean gate under chunk3's proposed internal-kind-root
   signal needs chunk3's fix; the boundary is correctly drawn.
3. **profiler-skip ⊋ UI-suppress — yes, this is exactly the isolation I asked for.**
   A directly-called `TypeDef.asObject` keeps its `dag.call` UI span while the profiler
   skips its `call_exec`. Confirmed orphan-safe: the kept child parents to the present
   normal span (loader makes an op from it). This is the correct realization of point B —
   the profiler ADDS spans and never SUBTRACTS UI spans. Recorded as intended.

## 3. Open items — resolved

- **(a) Recompute `profSkip` at import vs accept the default-false edge → RECOMPUTE if
  lock-safe; it's cheap and removes a warm-run volume regression.** With the cheap seam
  (§4.1) the predicate needs only `(receiverTypeName, field)`, both present on the imported
  `resultCall` frame, so recomputing at import/registration is a pure-function call with no
  `egraphMu` dependency on the hot path — do it. The default-false fallback is acceptable
  only as a backstop. Note: this is volume-only (never a dangle, per N2), so it is not a
  merge blocker, but warm/cache-heavy runs (lots of imported results) are exactly where the
  edge could matter, so recompute rather than "measure and hope."
- **(b) Over-cut audit → supported by my spot-check; keep it a §9 hard gate.** The
  reflection-type fields in core/schema/module.go (`*core.Function` :418-476, `*core.FunctionArg`
  :478+, `*core.TypeDef`/`*ObjectTypeDef`/… ) are all metadata accessors/builders/`__internal`;
  a grep for `Container`/`Directory`/`exec`/`asModule`/`Evaluate` on those receivers found
  nothing, and `FunctionCall` (returnValue/returnError — real-work-adjacent) is correctly
  *excluded* from the set. The goal is preserved: module-load slowness lives on
  `Query.moduleSource`/`ModuleSource.asModule` and the SDK `Container.withExec` — none are
  reflection receivers, so they stay profiled. Keep §9.4 as the empirical confirmation
  (watch `Function.withGenerator`/`withUp`/`withCheck` — builders, should be metadata, but
  confirm they don't eagerly evaluate).

## 4. Holistic pass — fresh findings

### 4.1 [main finding, D] The receiver type is FREE at the `objects.go` call site — use it; the memoization is unnecessary

The doc computes `profileSkip` in `AroundFunc` via `req.ResultCall.ReceiverCall(ctx)` →
`receiverCall` → `refCall` (cache.go result_call_frame.go) — a potential `egraphMu` lookup,
paid for every one of the ~33k recording-but-skipped descendants (the stamp must precede
the `IsSkipped` early return). The memo by `(receiverTypeName, field)` can't avoid it: you
need the lookup to get `receiverTypeName`.

But the immediate receiver's type name is **already in hand, lookup-free**, at the
`objects.go` call site: `r.class.inner.Type().Name()` (used for error messages at
objects.go:668/671/676). `r` *is* the immediate receiver; `r.class` is the type the field
is called on — exactly what the predicate needs, alongside `req.Field`. So:

- **Stamp `req.ReceiverTypeName = r.class.inner.Type().Name()` in `objects.go` (cheap
  string), and have `core.AroundFunc`'s `profileSkip` read that field** instead of walking
  `ReceiverCall`. The reflection-type-set membership stays in `core` (layering preserved);
  only a string crosses the boundary.

This single change:
1. **Resolves D** — no `egraphMu` lookup per call; the 33k-descendant cost becomes 33k
   cheap string reads. No memoization needed (drop §5.7's memo; it wouldn't have helped).
2. **Removes the §10 eviction/resolvability caveat** — `r.class` is always in hand, so the
   predicate is *fully* deterministic; there is no "`ReceiverCall` unresolvable → default
   false → profiled" edge and no claimer/joiner divergence under eviction timing.
3. **Is arguably more correct** — `r.class` is the authoritative receiver type, not a
   reconstructed `ResultCall.Type.NamedType`.

Net: simpler (no memo, no caveat), faster, and more deterministic. I'd make this the v1
implementation, not a "measure then maybe memoize." (If a string field on `CallRequest` is
unwanted, pass it through the existing `req` build in `preselect` where `r` is in scope.)

### 4.2 Native re-homing for the skipped outer call — consistent

When the outer `OpKindCall` and inner `execOp` are both skipped, the native recorder pushes
no op id, so sub-calls inherit the parent's op id via `ContextWithOpID` and re-home — the
native loader (`wcanalyze`) sees a present parent, no orphan. Symmetric with the OTel
re-homing. ✓ No native dangle from N1.

### 4.3 Goal + zero-inference — preserved

Loader/replay untouched (verified). The kept set (module load, SDK exec, user work) is
profiled; only the cheap reflection/schema-walk class folds into its kept ancestor's
self-time. The §4.4 user-wait loss (a non-introspection forcer blocking on an
introspection-produced *pending* value) is correctly named as a decision — plausibly rare
(metadata is computed eagerly), and §9.9 validates it. Self-consistent, no inference.

### 4.4 Nothing else smells wrong

I re-stress-tested: singleflight (within-session, shared-recipe + target-flag), lazy
(target-flag, set-before-read timing), the `wcprof.parent` override (§4.3, targets the
present lazy op), N2 provenance (same-flag gating ⇒ dangle-safe), N1 (re-home), the
over-cut set, and the publishResult boundary. No new hole. The §9 elevation to the
correctness centerpiece is the right posture given the by-construction proof now rests on
plumbing completeness (N1/N2/N4 + lazy target-flag).

## 5. Canonical alignment

- **Zero loader/replay change — re-verified.** Touches `core/telemetry.go`,
  `dagql/cache.go`, `dagql/call_request.go` (+ the cheap `objects.go` stamp I recommend).
  No `wcanalyze`/`wcotel` edits. Honors the rational-function principle. ✓
- **UI-isolation (my round-1 call) — correctly realized** (separate predicate;
  `introspectionInfo` untouched; pushback 3 confirms the intended profiler ⊋ UI divergence). ✓
- **Reconcile still implied (for later, not now):** record in the canonical design the
  profiler-skip emit invariant ("the reflection/introspection class is not profiled;
  faithfulness counters stay 0 by construction") and the native-symmetry note (this gates
  shared PR #13393 native ops — same scrutiny as the chunk4 replay changes, §5.4).

## Bottom line

v2 is a strong revision that correctly absorbed round 1 — the predicate flip, N1, N2, N3,
N4, the corrected exclusions, and the honest §9-as-centerpiece framing are all sound and
verified. The three pushbacks are correct (including against my own `!IsRecording`
suggestion). **One refinement carries real weight:** compute the predicate from the
lookup-free `r.class` receiver type at the `objects.go` call site rather than via
`ReceiverCall` in `AroundFunc` — it resolves the perf concern (D) outright, makes the memo
unnecessary, and removes the eviction-determinism caveat. Resolve open-item (a) by
recomputing `profSkip` at import (cheap with that seam), keep open-item (b) as the §9.4
hard gate. With the cheap seam folded in, implement as specified; land only after the §9
completeness assertions pass.
