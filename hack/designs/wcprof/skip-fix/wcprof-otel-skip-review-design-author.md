# Council review — skip the introspection subtree to fix the telemetry-volume regression

## Design author (emit domain) — verified against `wcprof-otel-implementer-chunk4-7ad02bcf`

I verified the whole chain in code rather than trusting the writeup. The proximate
cause is exactly as stated, and skipping is the right call — **but the proposed cut
(skip per-caller) has a real, code-grounded wait-dangle hole that the hard-failing
gate would turn into a refusal of good user traces. The fix is to key the skip on the
shared work (the `oc`), not the caller. With that one change it is a clean, simple,
zero-inference free win.**

### What I confirmed (file:line)

- **The cut signal exists and is deterministic.** `AroundFunc` (core/telemetry.go:22):
  `if dagql.IsSkipped(ctx) { NoopDone }`; `introspectionInfo` true → `WithSkip(ctx),
  NoopDone`. `introspectionInfo` (core/telemetry.go) classifies by **field + receiver
  type** (`__schema`, `function`, `sourceMap`, `Function.withArg/__*`, `TypeDef.with*/__*`,
  `FunctionArg/*TypeDef.__*`…) — a per-call-shape decision, not arbitrary context. So a
  given recipe is classified consistently.
- **`WithSkip` propagates** (dagql/internal.go:33-43: a context value), and a skipped
  descendant re-hits `if IsSkipped(ctx)` in AroundFunc → stays skipped. **So an
  introspection subtree is CLOSED: every descendant has `IsSkipped`=true.**
- **`isMeta` / seen-key dedup return `NoopDone` WITHOUT `WithSkip`** (core/telemetry.go) —
  so those skip only their own span; their children are NOT skipped. The cut must key on
  `IsSkipped` (introspection), which is the propagating one — meta/dedup are a different,
  non-propagating class and must NOT be folded into the cut (doing so would re-open the
  parent dangle).
- **wcprof emits regardless of any of this:** `getOrInitCall` gates `call_exec` only on
  `OTelProfActive(callCtx) = trace.SpanFromContext(callCtx).IsRecording()` (cache.go:3732;
  otelprof_hooks.go) — no `IsSkipped`/`NoTelemetry` check. Confirmed proximate cause.
- **The cache key does NOT separate skipped from non-skipped** — `callConcurrencyKeys{
  callKey: callDigest.String(), concurrencyKey}` (cache.go:~3677); `callDigest` is the pure
  recipe digest (receiver+field+args). **So a skipped caller and a non-skipped caller of
  the same recipe share the same `oc`.** This is the load-bearing fact for the wait risk.
- **The wait edge is emitted per-waiter, into the shared work:** `EmitOTelWait(ctx,
  oc.execSpanCtx, …)` (cache.go:3957), and its comment is explicit that it exists
  *because a suppressed caller never enters AroundFunc*. `EmitOTelWait` **deliberately
  emits a targetless wait when the target is invalid** so the gate fails loud on
  mixed/untraced-recording traces (otelprof_hooks.go).

### (Acid test) Is the skipped graph self-consistent? — Parent edges: YES. Wait edges: NOT under the proposed cut.

**Parent dangle — CLOSED, no hole.** A non-skipped op can only be a child of a skipped
op if introspection reached non-skipped work. But `WithSkip` propagates, so the entire
introspection subtree is `IsSkipped` (above). Cutting `call_exec` on `IsSkipped(ctx)`
therefore removes a *closed* set: every implicit (`parentId`) edge from a non-skipped
node points to a non-skipped node; edges *out of* the skipped set vanish with it. No
orphan. ✓ (One caveat to verify, below: explicit `wcprof.parent`/wait edges can bypass
this closure.)

**Wait dangle — REAL under the proposed per-caller cut.** The proposed rule skips the
`call_exec` when the *claimer* is skipped and skips the wait when the *waiter* is
skipped. But since the cache key is the bare recipe (no skip component), the **same
`oc` can be claimed by a skipped (introspection-subtree) caller and waited on by a
non-skipped (user) caller**:

1. introspection-subtree caller (IsSkipped, via propagation onto a cacheable sub-call)
   claims the `oc` first → its `call_exec` is skipped → `oc.execSpanCtx` invalid.
2. a non-skipped user caller of the same recipe joins → `EmitOTelWait(ctx,
   oc.execSpanCtx=invalid, …)` emits a **targetless wait** (cache.go:3957) →
3. loader counts an unresolved wait-target → **the hard-failing gate refuses an
   otherwise-good user trace** — i.e. it forces exactly the orphan/unresolved-target
   failure the no-inference invariant is built to reject.

**Does it actually happen?** It requires a cacheable recipe reachable from *both* an
introspection subtree and user work. In practice introspection resolvers are
TypeDef/Function builders + metadata reads (per `introspectionInfo`), which don't make
`Container`/`Directory`/user-resolver sub-calls, so a shared recipe is *unlikely*. **But
it is not provably closed** — the propagation skips *any* cacheable call under an
introspection ancestor, the key doesn't separate them, and the gate hard-fails on a
single occurrence. Per the governing principle (zero inference, gate is an invariant,
not a tolerance), "probably closed" is not good enough for a cut that, if wrong, refuses
a real user trace. Close it by construction.

### The cleanest cut: key the decision on the SHARED WORK, not the caller

Decide profiling-emission **once, at `oc` creation, as a property of the `oc`**, and use
it for `call_exec`, `publishResult`, *and* the wait edge:

- At claim (cache.go:3732): `emit := OTelProfActive(callCtx) && !dagql.IsSkipped(callCtx)`;
  create `call_exec` iff `emit`; record `oc.profSkipped = OTelProfActive(callCtx) &&
  dagql.IsSkipped(callCtx)`.
- `publishResult` already follows for free — it is gated on `oc.execSpanCtx.IsValid()`
  (cache.go:4015), which is invalid when skipped.
- Wait edge (cache.go:3957): emit **iff `!oc.profSkipped`** (a property of the shared
  work), NOT iff the waiter is skipped.

Self-consistency by construction:
- `oc.profSkipped` → no `call_exec`, no `publishResult`, no waits → the work and every
  edge into it are absent together. No dangle, regardless of who waits. ✓
- `!oc.profSkipped`, `execSpanCtx` valid → `call_exec` + all waits resolve. ✓
- `!oc.profSkipped`, `execSpanCtx` invalid (untraced/mixed-recording claimer, NOT a skip)
  → targetless wait still emitted → gate fails loud. **The §3.1 mixed-recording detector
  is preserved** — this is why `oc.profSkipped` must be a distinct flag, not "execSpanCtx
  invalid."

The residual coarsening: a non-skipped user caller that *waits on* skipped introspection
work loses that wait edge, so the wait time folds into the waiter's own self. That is an
acceptable, honest coarsening (introspection time surfaces as the user caller's
self-time, never as a wrong number or a dangle) and forces **zero** inference. It is
strictly the behavior Erik accepts ("coarse/absent introspection is fine").

### One more thing to verify before landing (explicit edges bypass the subtree closure)

The parent-closure argument covers implicit `parentId` edges. **Explicit edges can still
cross into the skipped set:** (a) a `wcprof.parent` override (the §3.0.2 lazy re-pointing,
and the proposed publishResult parent stamp) that re-homes a non-skipped op onto a
producer that happens to be in the skipped set; (b) any wait whose *target* is skipped
(handled by the `oc.profSkipped` wait gate above). For (a): confirm lazy-eval producers
are never introspection-classified (they're deferred Container/Directory user work, so
they shouldn't be — but verify there is no `wcprof.parent`/wait target pointing from a
non-skipped op into the skipped set). The `oc`-keyed cut already closes the wait case;
(a) just needs a confirming check, not new machinery.

### Per-question verdict

- **(a) Correctness — free win or holes?** Free win for the goal, and the **parent dangle
  is closed** by `WithSkip` propagation. The **wait dangle is a real hole in the proposed
  per-caller cut** (cache.go:3957 + the recipe-only key at cache.go:~3677): a non-skipped
  waiter on a skipped-claimer `oc` emits a targetless wait → gate refusal. Closed by
  keying the cut on `oc.profSkipped`.
- **(b) Zero inference / no heuristics?** Yes, with the `oc`-keyed cut: it reads the same
  deterministic suppression decision normal telemetry already makes (`IsSkipped`), the
  loader/replay are untouched, and the graph stays self-consistent so nothing is ever
  inferred or gate-refused. The naive per-caller cut *risks* forcing a gate refusal /
  inference — a principle violation — so it is the wrong cut.
- **(c) Preserves the goal (slow-finding, user-work-first-class)?** Yes. Introspection is
  engine schema-building, not user work; its aggregate time folds up into the nearest
  non-skipped ancestor's self-time (module-load still shows as slow, just coarse), and no
  user work is lost or mis-attributed. User work stays first-class.
- **(d) Simple or massive?** Simple: one `oc.profSkipped` field set at claim, gate
  `call_exec` (cache.go:3732) and the wait (cache.go:3957) on it; `publishResult` follows
  for free. ~5 lines, no new emit shape, no loader/replay change.

### On the BSP overflow

The fix removes the **amplifier** (the ~33k introspection spans → volume back to
baseline → BSP queue no longer overflows → the drop-induced orphan/unresolved-target
loss disappears). It does **not** make the emit lossless for a legitimate user-work
burst — that backpressure/lossless work stays a separate correctness backstop. Don't
conflate the two; both are wanted, for different reasons.

### Verdict

**Skip the introspection subtree — yes, correct and a free win — but cut on the shared
work, not the caller.** Add `oc.profSkipped` (= `IsSkipped` at claim) and gate
`call_exec` + the wait edge on it (`publishResult` already follows). That keeps the graph
self-consistent by construction (zero inference, no gate refusal of good traces),
preserves the §3.1 mixed-recording detector, preserves the goal with honest coarsening,
and is ~5 lines. Before landing, also confirm no explicit `wcprof.parent`/wait edge
points from a non-skipped op into the skipped set.
