# Skip-fix implementation plan — review (Chunk 2 implementer)

Read the plan in full; re-verified every load-bearing claim against the skip-implementer worktree
(`wcprof-otel-skip-implementer-a7daa7c9`, HEAD `4585bf413d`). I built the call_exec/publishResult/wait
emit, so I weighted the gating sites + the loader. **File:line are that worktree.**

## Verdict

**The core is sound — implement it — but flip the single biggest design decision.** The static,
recipe-keyed cut is self-consistent by construction with zero loader/replay change (§4 holds, verified
below), the plumbing is sound and lock-safe, and the static cut is *genuinely better than the dynamic
`oc.profSkip = IsSkipped` cut I myself recommended in my earlier skip review* — I was wrong on that and
say so below. **But the debug-GATED predicate choice (§3.2 / §5.4 / §10) rests on a false premise** —
the "debug-orphan" argument — which I verified is mechanically wrong in the loader. Flip to a
**separate, debug-INDEPENDENT, receiver-type profiling predicate**: it is cleaner on every axis the
plan cares about (determinism, no UI coupling, completeness, debug volume-safety) and it *eliminates*
the plan's only admitted non-recipe input, making the cut a truly pure function of the recipe. The
lead's five pushes (A–E) are all correct; A is the linchpin and unlocks B/C/D.

## What I confirm is correct (the load-bearing core)

- **§4.2 static-cut self-consistency is airtight.** To join `ongoingCall`/lazy state for key `K` you
  must be making call `K` (`ongoingCalls` keyed `{callKey, concurrencyKey}`, `cache.go:3674-3677`;
  `callKey` = recipe digest, `:3669`). A predicate that is a pure function of the recipe ⇒ waiter's
  classification **equals** its target's ⇒ a non-skipped op can never hold a wait into the skipped set.
  Waits gate on the **target's stored flag** (`oc.profSkip`/`shared.profSkip`), covering executor
  self-wait and joiners (both route `c.wait`, `:3785`/`:3703`). Confirmed.
- **§4.1 parent re-homing is real and predicate-independent.** Skipping = not minting + not
  reassigning `callCtx` (`:3732-3733`), so sub-calls record the nearest **recording ancestor** as
  `parentId` — a present span. Verified against the loader: causal parent `wcprof.parent ?? parentId`
  (`loader.go:469-472` per the plan; orphan logic at `:299-310`).
- **Plumbing is sound and lock-safe.** Same `*CallRequest` pointer flows to `s.telemetry`/`AroundFunc`
  (`objects.go:656`) and `GetOrInitCall` (`:678`); `AroundFunc` runs **outside** cache locks. A
  cache-side predicate would nest `egraphMu.RLock` (`resultCallByResultID`,
  `result_call_frame.go:1381`, via `ReceiverCall` `:575`) under `callsMu`/`lazyMu` (`cache.go:706`/
  `:671`) — a real lock-order hazard. The lead's AGREE on this is correct; stamping on `CallRequest` is
  the right seam.
- **Invariant-T for the flag holds.** `oc.profSkip` can be set in the `ongoingCall` literal under
  `callsMu` (`:3744`) **before** the publish (`:3762`), exactly where I stash `execSpanCtx` (`:3758`).
  Every joiner sees it. Good.
- **"Follows for free" claims check out.** `publishResult` gates on `execSpanCtx.IsValid()` (`:4016`)
  — correct *because* publishResult is a child of call_exec, so no-call_exec ⇒ no-publishResult is
  always right (skip **and** mixed-recording both). Native `pubOp` gates on `oc.profOpID != 0`
  (`:4004`) and `(*Op)(nil).ID()==0` is guaranteed (`record.go:125-130`, explicit nil guard). No panic.
- **§5.5 distinct-from-invalid is correct and not contradictory.** The wait needs a *distinct*
  `profSkip` bool (so a genuine mixed/untraced target still emits a targetless wait → gate fails loud,
  `otelprof_hooks.go:111-135`); publishResult correctly keys on validity. These differ because the
  wait is a separate edge that must distinguish "deliberately skipped" from "lost," while publishResult
  is structurally absent whenever its parent is. Preserves the detector I built.
- **Honest correction to my own prior review:** I recommended deciding the skip as
  `IsSkipped(callCtx) || introspectionInfo || …` stored on `oc`. That is **worse** than this plan's
  recipe-only cut: a `clone`/dep-load under a `hideCtx` is `IsSkipped=true` but not introspection —
  my cut would have **deterministically dropped real shared work** (violating "user-work first-class"),
  whereas the recipe cut keeps it profiled (§4.2 / plan §10.2). The implementer is right; I withdraw
  the `IsSkipped`-based decision.

## The lead's five points — per-point, grounded

**A [BIGGEST] — AGREE, verified; the debug-orphan argument is mechanically wrong.**
The loader builds `opIDBySpan` for **every** deduped span with **no kind filter**
(`loader.go:251-254`: `for i, s := range deduped { opIDBySpan[s.SpanID] = i+1 }`), and counts
`OrphanedParents` **only** when the causal-parent *span is absent* (`parentID == 0`, `:303-310`). So a
child whose parent is a **present normal `dag.call` span is NOT an orphan** — that span is an op. The
plan's §10/§3.2/§5.4 claim that a debug-independent predicate makes "a kept child parent to a non-op
span → OrphanedParents" is false: there is no "non-op span"; the child re-homes to the present normal
span (which AroundFunc pushed onto `ctx` before the skipped call_exec site). Two consequences:
1. The justification for the **debug-gated** predicate collapses. A **debug-independent** predicate is
   preferable: fully deterministic, and it **eliminates the §10 "debug baggage is the one non-recipe
   input" caveat** — the cut becomes a *true* pure function of the recipe, so waiter==target by
   construction with no reliance on the target-flag fallback. That is a stronger zero-inference story.
2. The plan frames "debug re-profiles introspection" as a *benefit* without its cost. It **inverts the
   safety claim**: debug-gating re-emits the ~33k introspection call_exec **in debug**, re-arming the
   BSP overflow → debug captures get *more* drop/orphan loss, not less. The debug-independent predicate
   is what actually keeps debug traces volume-safe. The only thing lost is a niche "re-profile
   introspection in debug" affordance the plan itself calls a "bonus." Oracle comparability is **not** a
   reason to debug-gate: a debug-independent predicate skips uniformly in both sources and all modes, so
   the oracle stays comparable.

**B — AGREE: don't couple the profiler to the UI.** `introspectionInfo` *is* the
normal-telemetry/UI decision (`telemetry.go:35-37`), so §3.2a's extension changes user-facing
telemetry: a directly-called `TypeDef.asObject`/`Function.args` stops emitting `dag.call` **and** (since
the introspection branch returns `WithSkip`, `:37`) now propagates suppression to its whole subtree —
a broader UI change than the plan frames. Keep `introspectionInfo` untouched; use a **separate**
`profSkip(call)`.

**C — AGREE, and it merges with B.** §3.2a's named-field list is workload-specific (module-load
forensics) and will under-cut other workloads; §3.2b (classify by **receiver type** — any field on a
reflection type is metadata) is complete and future-proof. A *separate* receiver-type `profSkip`
predicate gives B (no UI change) **and** C (complete) at once, and stays recipe-pure (receiver type is
part of the recipe) so §4 still holds.

**D — AGREE it's real; the proposed mitigation does not help production.** Moving `introspectionInfo`
ahead of the `IsSkipped` early-return (`telemetry.go:32` → before `:35`) runs the receiver-chain walk
(`ReceiverCall` → `egraphMu.RLock` cache lookups) for **all ~33k inherited-skip descendants** that
today early-return uncomputed. In production OTel is **always recording**, so the suggested
`!IsRecording` short-circuit is *false on the hot path* and saves nothing (the correct guard is
`!IsRecording && !wcprof.Enabled` to preserve native — but that too is false in prod). Real options:
**memoize the decision by recipe digest** (it is a pure function of the recipe — the natural fix) or
accept it — but **measure in v1**, do not defer. (Note: a receiver-*type* predicate often resolves at
the immediate receiver, depth-1, vs `introspectionInfo`'s walk-to-root — a secondary reason to prefer
C.)

**E — AGREE: make it a hard check, not an assumption.** §6's "exec-split/service-start never emit a
skipped-class span, so no gate needed" is plausible (those fire inside container-exec/service
resolvers introspection never reaches) but unproven. The §9 capture must **assert zero skipped-class
spans originate there** (and if one ever could, the same work-flag gate applies). The plan says
"confirm"; elevate it to a gating acceptance check.

## Unified recommendation

**One change to the plan: replace the debug-gated, `introspectionInfo`-extending predicate with a
separate, debug-independent, receiver-type `profSkip(call)`** (leave `introspectionInfo`/UI untouched;
keep everything else — `CallRequest.SkipProfile` stamp in `AroundFunc`, per-shared-work `profSkip`
flags, gate-on-target's-flag, native+OTel symmetry, distinct-from-invalid). This single move resolves
A (determinism + debug volume-safety), B (no UI coupling), C (completeness), and D's determinism leg,
and makes the cut a true pure function of the recipe — eliminating the §10 caveat and *strengthening*
the self-consistency rather than relying on the target-flag fallback. Native+OTel still skip from the
one decision; the oracle stays comparable in every mode.

## Additional points (my emit-builder angle)

- **Telemetry-off + wcprof-on corner:** the stamp only runs when `s.telemetry != nil &&
  !field.Spec.NoTelemetry` (`objects.go:655`). If telemetry is unregistered but `wcprof.Enabled`
  (native-only, e.g. a bare `--profile` path), `SkipProfile` stays false → native re-profiles
  introspection. Dev-only and self-consistent (§10 notes the `cmd/introspect` variant), but flag that
  native volume is unbounded on that path; not a blocker.
- **Lazy flag happens-before — verify, don't assume.** `sharedResult.profSkip` is set in
  `initCompletedResult` (`:4019`) and read in `evaluateOne` under `lazyMu` (§5.3). Forced eval happens
  after the result is returned, so it *should* be safe, but confirm the set publishes before any
  concurrent lazy reader (a `lazyMu`-ordered set, or rely on the result-publication barrier) — call it
  out in the §8 lazy tests rather than assuming.
- **Confirmed: zero analysis-side complexity.** No loader/replay/vocab/emit-shape change; the static
  cut makes a cross-boundary edge *impossible*, so (unlike the gate-owner's Option-E redirected wait)
  there is nothing for the analysis to resolve. Honors the no-inference principle.
- **Test #4 / #5 (§8) are the right guards** — keep the assertion that the *dynamic* cut would dangle
  (documents why static is required) and that an artificially-invalid non-skipped target still trips
  `UnresolvedWaitTargets` (proves `profSkip` didn't blind the detector).

## Bottom line

Feasible, sound, and aligned with the principle + goal. **Implement — with the debug-independent
separate receiver-type predicate (flip §3.2/§5.4/§10), measure D before deferring, and harden E into a
§9 acceptance check.** Do not land before the §9 measurements (volume ≈ baseline; gate `0/0`; BSP
`DroppedSpans` →0; oracle aligned) pass.
