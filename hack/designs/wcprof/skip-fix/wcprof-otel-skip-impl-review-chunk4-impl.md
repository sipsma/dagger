# Review of `wcprof-otel-skip-impl-plan.md` — chunk4 implementer

Reviewer: chunk4 implementer. Verified every load-bearing claim against the
current branch (file:line are this worktree, identical emit @ `4585bf413d`). I
read the doc in full and engaged its pushback on my earlier review on the merits.

## Verdict

**Approve the core design; require one substantive change to the predicate.**
The static work-keyed cut, the target-flag wait gating, the re-homing argument,
and the distinct skip-flag are all **correct and verified** — this is a genuinely
careful doc and I found no flaw in the self-consistency proof. The one change I'd
require before implementing: **drop the "extend the debug-gated `introspectionInfo`
field-by-field" choice (§3.2a/§5.4/§10) in favor of a SEPARATE, debug-INDEPENDENT,
receiver-TYPE predicate.** That single change resolves the lead's points A, B, C
*and* D together, and its only stated justification — the §10 debug-orphan
argument — is **provably false against the loader code.**

## I concede my earlier (chunk4) point — the doc is right, verified

My prior review claimed `isMeta`/`NoTelemetry` are "open subtrees → orphan their
children." **That rationale is mechanically wrong, and the doc (§3.3) is correct.**
Skipping means *not minting and not reassigning `callCtx`* (gate at
`cache.go:3732`, the `callCtx, execSpan = …` assignment at `:3733` simply does not
run), so the resolver runs under the parent's still-current span and children
record the **parent's** span id — they re-home to a present ancestor, never to the
absent skipped span. I verified the loader makes this airtight: `opIDBySpan` is
built for **every** deduped span with **no kind filter** (`loader.go:253-256`),
and an orphan is counted **only** when the causal-parent span id resolves to `0` =
absent from the capture (`loader.go:299-305`). A re-homed child points at a present
span → no orphan. My *conclusion* (exclude `isMeta`/`NoTelemetry`) stands, but for
the doc's pragmatic reasons (no volume; `NoTelemetry` is `DoNotCache` and returns
at `cache.go:3601` before the emit — verified), not my orphan reason. Correction
accepted.

## Lead point A — CONFIRMED, and it's decisive (the debug-orphan argument is unfounded)

The doc's §10 keeps the predicate **debug-gated** on the rationale that a
debug-*independent* predicate "would skip the `call_exec` while the normal span
records, and a kept child would parent to a **non-op span** → `OrphanedParents`."

I verified this is false. There is no "non-op span": the loader assigns an opID to
**every** span regardless of `wcprof.op.kind` (`loader.go:253-256`). In the
debug scenario, the schema-builder's normal recording `dag.call` span is present
in the capture, so it **is** an op-parent; a child parenting to it resolves
(`parentID != 0`) → **no orphan** (`loader.go:299-305`). The justification
collapses.

With it gone, **debug-independent is strictly better**:
- **Deterministic** — removes the doc's own §10 "debug baggage is the one
  non-recipe input" caveat entirely. The predicate becomes a pure recipe function,
  which is exactly what §4.2 wants.
- **Volume-safe in every mode.** The doc frames "debug re-profiles introspection"
  as a feature (§5.4 pt 4) but does not flag the cost the lead names: in debug the
  ~33k schema-walk volume comes **back**, re-overflowing the 2048-slot BSP queue —
  so debug captures are lossy again. That makes the advertised benefit
  ("deep-dev introspection profiling recoverable via debug") **self-defeating**:
  you can re-enable the spans, but the capture that would profile them is the one
  that drops them. The "bonus" is illusory absent the separate BSP backstop.

The only thing truly lost by debug-independence is re-profiling introspection in
debug — which the doc itself calls a "bonus" and which is unusable for the reason
above. **Decouple the profiling-skip predicate from the debug gate.** The debug
gate exists for a *human inspecting the UI*; the profiling skip exists for the
*offline analyzer*. Different consumers — they should not share a gate.

## My central recommendation: a separate, debug-independent, receiver-TYPE predicate (resolves A+B+C+D)

The doc lists this as alternative §3.2b + "separate predicate," then recommends
*against* it. I'd recommend *for* it. It dominates §3.2a on all four of the lead's
axes simultaneously:

- **(A) determinism / volume-safety** — debug-independent, per above.
- **(B) no UI change** — a separate `profSkip(ctx, call)` leaves
  `introspectionInfo` and normal telemetry untouched, so the doc's §3.2 "directly
  -called `TypeDef.asObject` stops emitting `dag.call`" UI regression evaporates.
  The lead's lean to keep it isolated is correct, and a separate predicate makes
  isolation free.
- **(C) completeness / robustness** — classify by **receiver type** (`Function`,
  `TypeDef`, `FunctionArg`, `ObjectTypeDef`, `InterfaceTypeDef`, `InputTypeDef`,
  `FieldTypeDef`, `ListTypeDef`, `EnumTypeDef`, `EnumMemberTypeDef`, `ScalarTypeDef`)
  + the existing root-field list. Every accessor on those reflection types is
  metadata, so this catches the whole class with **no per-workload field-chasing**
  (the §3.2a "iterate the field lists from the forensics set" is workload-specific
  and will under-catch — the lead's point C). I checked the forensics names:
  `Query.sourceMap` is a root field; `ObjectTypeDef.__withFunction`,
  `Function.__withArg/.args/.withArg/.sourceModuleName`, `TypeDef.as*` are **all**
  reflection-type accessors. Root-list ∪ receiver-type covers 100% of the observed
  class **and** generalizes.
- **(D) performance** — this is the part neither doc nor lead noted: a
  receiver-type predicate needs only the **immediate** receiver's type, i.e. at
  most **one** `ReceiverCall` (`result_call_frame.go:575` →
  `resultCallByResultID` → `egraphMu.RLock`, `:1378`), versus `introspectionInfo`'s
  **full upward chain walk** (a loop of `ReceiverCall`s). So it is *no more*
  expensive than the doc's predicate and likely cheaper — partially mitigating D
  on its own.

**Correctness is preserved:** the immediate receiver type is a function of the
recipe (the receiver chain is part of the digest at `cache.go:3669`), so §4.2's
"waiter shares target's recipe ⟹ same classification" still holds, and being
debug-independent it removes the §10 cross-session/debug-mismatch residual
(the §4.2 target-flag gating still backstops it regardless). Boundary: verify no
reflection-type field triggers real work (the doc asserts none do; reasonable —
these are pure metadata reads — but make it a §9 spot check, since over-skipping a
field that lazily loads a module would be a goal violation, not just granularity).

**Keep everything else about the plumbing** — stamp the result on
`CallRequest.SkipProfile` in `AroundFunc`. That choice is orthogonal to the
predicate and is well-justified (see below); just compute a receiver-type
`profSkip` there instead of mutating `introspectionInfo`.

## The rest of the doc — verified solid (do not relitigate)

- **§4.2 wait-edge self-consistency — airtight, verified.** `ongoingCalls` is keyed
  `{callKey, concurrencyKey}` with `concurrencyKey = SessionID`
  (`cache.go:1347-1350,3674-3677`; `objects.go:603`), so singleflight sharing is
  **within-session** — no cross-session debug split there. Cross-session sharing
  only occurs on the **lazy** `sharedResult` state, and the doc correctly gates the
  lazy wait on `shared.profSkip` (the leader's stored decision), so a joiner never
  recomputes a divergent bit. Gating on the **target's stored flag** (not the
  waiter's) is the right call and makes it robust even under the debug residual the
  separate predicate already removes. No non-skipped waiter can hold an edge into
  the skipped set. Confirmed.
- **§4.1 re-homing — correct** (conceded above; verified at loader).
- **§5.5 distinct `profSkip` bool vs `execSpanCtx.IsValid()` — correct and
  important.** Overloading validity would blind the genuine-loss detector
  (`otelprof_hooks.go:111-135`). Keep them distinct. Agree fully.
- **Plumbing — verified sound.** Same `req` pointer flows to `s.telemetry` (=
  `AroundFunc`, can mutate) and `cache.GetOrInitCall` (`objects.go:583,656,678`).
  `initCompletedResult` receives `req` (`cache.go:4103`), so the lazy
  `sharedResult.profSkip` plumbing is feasible. `SkipProfile` must be added to
  `Clone()` and **must not** enter `callKey`/`callDigest`/`concurrencyKey` (the doc
  says so — enforce it; it's request policy like `DoNotCache`).
- **§5.4 skip native + OTel from one decision — agree** (oracle stays comparable).
  Note the *reason* "debug bonus" (pt 4) collapses with a debug-independent
  predicate, but the conclusion is unchanged and the other reasons hold.
- **Lock-safety basis (CallRequest over a cache-side predicate) — confirmed.** A
  predicate called at `cache.go:3732`/`:3017` runs under `callsMu`/`lazyMu`; the
  receiver walk takes `egraphMu.RLock` — that nests `egraphMu` under
  `callsMu`/`lazyMu`, a real lock-ordering hazard. `AroundFunc` runs outside cache
  locks (`objects.go:656`), so the stamp avoids it. Good call by the doc; the lead's
  independent verification matches mine.

## Per the lead's other pushes

- **B (UI coupling) — agree, and the separate predicate above resolves it.** Do not
  couple a profiler-volume fix to a user-facing telemetry change.
- **C (field completeness) — agree; receiver-type (above) is the complete cut.**
- **D (perf) — agree, measure in v1; but the lead's proposed mitigation is
  insufficient.** The "!IsRecording short-circuit" only helps the telemetry-**off**
  path; the 33k inherited-skip descendants are **recording** (that's the bug), so
  the short-circuit does not touch the hot path. The real mitigations are the
  receiver-type predicate (cheaper walk, above) and/or memoizing the classification
  on the `ResultCall`. Measure the `egraphMu.RLock` acquisition cost/contention
  under the module-load workload (it's a read lock, so it contends only with
  indexing writers — but module load has many). Put the number in §9.
- **E (exec-split/service-start assumption) — agree; make §6 a hard §9 check**, not
  a narrative assumption: assert zero skipped-class spans originate from
  `engine/engineutil` exec-split or `core` service-start on a real capture.

## Must-verify nits before landing

1. **`(*wcprof.Op)(nil).ID() == 0`** — the "native `pubOp` follows for free" claim
   (§5.2) depends on it; if `ID()` deref-panics on nil, that row breaks. The doc
   flags it; confirm with a test.
2. **No reflection-type field does real work** (the receiver-type predicate's one
   correctness assumption) — §9 spot check.
3. **§8 test #4 and #5 are the load-bearing ones** — keep them: the
   skipped-claimer/non-skipped-joiner collapse (proves the static cut) and the
   non-skipped-target-with-invalid-span still-fails-loud (proves the flag didn't
   blind the detector). Add one: a debug-on capture stays at baseline volume
   (proves debug-independence closed the §10 hole).

## Bottom line

Feasible, correct, and close to landable. The static work-keyed cut + target-flag
gating + re-homing are verified self-consistent with zero loader/replay change, so
the no-inference principle holds and the goal (user-work first-class; introspection
folds into the nearest kept ancestor's honest self-time) is preserved. **The one
change I'd insist on: a separate, debug-independent, receiver-type profiling
predicate** — it is more complete (C), touches no UI (B), is deterministic and
volume-safe in every mode (A), is no costlier and likely cheaper (D), and removes
the doc's only false premise. I'd take the implementation on those terms.
