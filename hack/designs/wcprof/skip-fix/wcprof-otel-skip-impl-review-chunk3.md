# Review of `wcprof-otel-skip-impl-plan.md` — by the Chunk 3 implementer (lazy / wcprof.parent owner)

**Scope:** read the doc in full; re-verified every load-bearing claim against the
skip-implementer worktree (`…/wcprof-otel-skip-implementer-a7daa7c9-239fd90a`, HEAD
`4585bf413d`). Special scrutiny of §4.3/§5.3 (lazy / `wcprof.parent`), per my ownership.
Review only — no code.

## Verdict

**Endorse the core design — with one substantive change and a few hardenings.** The
static, work-keyed cut + CallRequest-stamp + gate-on-the-target's-stored-flag is sound:
zero loader/replay change (verified), no inference, self-consistent by construction, and
the CallRequest plumbing has a real lock-safety basis I confirmed. **The one substantive
change:** drop the debug-gated / extend-`introspectionInfo` predicate (§3.2a/§5.4/§10) for
a **separate, debug-independent, receiver-type predicate** — this resolves the lead's A, B,
C, and D at once, and the doc's stated justification for debug-gating rests on an orphan
argument that is mechanically false against the loader. My domain (§4.3/§5.3) is correct in
implementation but its *rationale* in §4.2 does not actually cover the lazy path — a gap to
close in the doc even though the gating is right.

---

## 1. The acid test — verified, including the lazy path

**§4.1 parent edges — SAFE, confirmed.** A skipped call mints no span, `callCtx` is not
reassigned (cache.go:3732-3735), the resolver runs under the nearest recording ancestor, so
a kept child records a **present** parent span. And the loader makes *every* deduped span an
op-parent (loader.go opIDBySpan, no kind filter) and counts `OrphanedParents` **only** when
the recorded parent span is *absent* (`cpSpan != "" && parentID == 0`, loader.go:303-310).
So re-homing onto any present ancestor never orphans. Holds for any predicate. ✓

**§4.2 wait edges (singleflight) — SAFE, confirmed.** Joiner and executor both target
`oc.execSpanCtx` and both flow through `c.wait` (cache.go:3785/3703). The waiter shares the
target's cache key ⟹ same recipe ⟹ same static `SkipProfile`; gating on the target's stored
`oc.profSkip` closes it. ✓ for the singleflight path.

**§4.3 / §5.3 lazy path — implementation CORRECT, but §4.2's rationale does NOT cover it
(my domain; flag this).** In the lazy path the **waiter is not a caller of the target's
recipe.** `resultCall := shared.loadResultCall()` (cache.go:2980) is the *producer*; the
joiner waiting at cache.go:2964/2973 is a *forcer/consumer* of the pending value. Different
recipes. So the §4.2 "waiter shares the target's recipe" argument — the doc's headline
self-consistency proof — **silently does not apply to lazy.** What actually keeps the lazy
path consistent is gating the joiner/leader wait on the **target's** stored flag
`shared.profSkip` (keyed on `resultCall`), which §5.3 does:

- `shared.profSkip` (producer is introspection) ⇒ `beginOTelLazyOp` gated off (cache.go:3017)
  ⇒ `lazyEvalSpanCtx` stays the reset-invalid zero (cache.go:2996) ⇒ joiner wait gated off
  ⇒ no op, no edge. A *non-introspection forcer* of this value has its wait gated off by the
  **target's** flag — no dangle, even though the forcer would be "kept." ✓
- `!shared.profSkip` (producer is real work) ⇒ lazy op minted/present ⇒ joiner wait resolves;
  an *introspection forcer* still emits its wait, which re-homes onto the forcer's nearest
  recording ancestor (honest: that ancestor's subtree did synchronously wait). ✓

This is fine, but the doc frames target-flag gating as mere "robustness against the one
non-recipe input (debug baggage)" (§4.2 last paragraph). **For the lazy path it is
load-bearing, not robustness.** Please state explicitly that lazy consistency rests on
target-flag gating because forcer ≠ producer, so a future reader doesn't "simplify" the lazy
gate to the waiter's own bit and reopen a cross-recipe dangle.

**`wcprof.parent` override never crosses the boundary — confirmed (my Chunk 3 code,
unchanged).** The processor stamps `wcprof.parent =` the lazy op span only on the producer's
*direct* re-pointed children (`s.Parent().SpanID() == producerSpanID`, otelprof_lazy.go:97-100),
i.e. it points at the lazy op, never the forcer. When the producer is skipped, no lazy op is
minted ⇒ `withLazyParentOverride` is never called (otelprof_lazy.go:166 is inside
`beginOTelLazyOp`) ⇒ **no override exists to point anywhere.** When kept, it points at the
present lazy op, whose own `parentId` is minted under `evalCtx` (otelprof_lazy.go:154/170) and
re-homes over an introspection forcer to a present ancestor. So the prompt's point-8 worry
("introspection forces a non-suppressed lazy op → `wcprof.parent` into the skipped set")
genuinely cannot arise. ✓

**Two minor lazy items to nail in the doc:**
- *User-wait loss (acknowledge it):* a non-introspection forcer that blocks on an
  introspection-produced pending value has its wait dropped (gated by the target's flag).
  Rare (introspection metadata is computed eagerly and seldom pending) and cheap, and it is
  self-consistent — but it is a real, if tiny, loss of a *user* wait. Name it so it's a
  decision, not an accident.
- *Set-before-read ordering:* §5.3 sets `sharedResult.profSkip` in `initCompletedResult`
  (cache.go:4019). Confirm it is set before any lazy path can read it (publication precedes
  forceability, so it should be) — and note that the default-false is *safe-but-leaky* if
  ever missed (it would profile introspection lazy work, never dangle), so this is a volume
  edge, not a correctness one.

---

## 2. Zero loader/replay change, no-inference, lock-safety — all confirmed

- **Zero analysis-side change / no inference: TRUE.** The fix is emit-only; the loader and
  replay are untouched and the emitted graph stays self-consistent (every kept edge points
  at a kept, present node). No orphan to re-home, no target to invent, no new vocab. ✓
- **Lock-safety basis for the CallRequest-stamp: TRUE (verified).** `introspectionInfo` walks
  `ReceiverCall` (result_call_frame.go:575→:603) → `resultCallByResultID`, which takes
  `c.egraphMu.RLock()` (result_call_frame.go:1381). `getOrInitCall` holds `callsMu` across
  :3689–:3783 (so :3732 is under it) and the lazy path holds `lazyMu` across :2934–:3025 (so
  :3017 is under it). A cache-side predicate would therefore acquire `egraphMu` while holding
  `callsMu`/`lazyMu` — new lock-order nesting + contention on the hot path. Stamping in
  `AroundFunc` (objects.go:656, outside cache locks) reduces the under-lock cost to one bool
  read. The doc's CallRequest choice (§3.4) is the right one for this reason; agree.
- **§5.5 distinct flag vs `execSpanCtx.IsValid()`: agree, and important.** Keeping `profSkip`
  a distinct bool preserves the targetless-wait detector for genuine mixed-recording loss.
  Verified the detector exists (EmitOTelWait emits targetless on invalid target;
  `UnresolvedWaitTargets` hard-fails at gate.go). Do not overload validity as "skipped." ✓

---

## 3. Point A (the lead's biggest) — CONFIRMED: the debug-orphan justification is false; go debug-independent

The doc justifies reusing the **debug-gated** `introspectionInfo` (§10) by claiming a
debug-independent predicate would, in debug mode, "skip the call_exec while the normal span
records, and a kept child would parent to a **non-op span** → `OrphanedParents`."

**That is mechanically wrong, verified at loader.go:299-313.** The loader assigns an op id to
*every* deduped span with no kind filter, and `OrphanedParents++` fires *only* when the
recorded parent **span** is absent (`cpSpan != "" && parentID == 0`). A schema-builder's
normal `dag.call` span is a *present* span ⟹ it is an op-parent ⟹ a kept child of it resolves
(`parentID != 0`) ⟹ **no orphan.** The premise "non-op span" does not exist in this loader.
The debug-orphan defense collapses.

With it gone, **debug-independent is strictly better**, and the doc's "debug bonus" framing is
actively harmful:
- The debug-gated choice **re-introduces the entire volume regression in debug mode** — the
  receiver-type switches are gated off under `slog.IsDebug` (telemetry.go:402), so in debug
  the schema-builders profile again → the ~33k-span amplifier returns → the BSP overflow and
  the orphan/unresolved-target capture loss return. Debug traces are exactly when you most
  need clean capture; the doc sells this as a feature without flagging the cost.
- It also leaves the §10 determinism caveat (debug baggage is the one non-recipe input). A
  debug-independent predicate is a *total, deterministic* function of the recipe and removes
  the caveat outright.
- Oracle comparability does **not** require debug-gating: a debug-independent predicate has
  both sources skip the same class in *every* mode (the doc gates native on the same flag,
  §5.4) — comparable without the volume relapse.

If deep-dev introspection profiling is ever wanted, gate *that* behind an explicit dedicated
flag, never the general debug baggage. **Recommend: debug-independent predicate.**

---

## 4. Points B / C / D / E

**B (UI coupling) — CONFIRMED, agree with isolation.** `AroundFunc` uses `introspectionInfo`
for the *normal-span* skip (telemetry.go:35-37), so §3.2a extending it makes directly-called
`TypeDef.asObject`/`Function.args` stop emitting `dag.call` spans — a UI/normal-telemetry
change bundled into a profiler-volume fix. Keep them decoupled: a **separate** profiling
predicate leaves `introspectionInfo`/UI untouched.

**C (completeness) — agree, receiver-type dominates.** §3.2a's named-accessor list is the
module-load forensics set and will under-catch other workloads (e.g. `ObjectTypeDef.functions`,
`Function.returnType`, `TypeDef.kind`, `FunctionArg.*`). Classifying by **receiver type** —
any field whose immediate receiver is a reflection type (`Function`, `TypeDef`, `FunctionArg`,
`*TypeDef`, …) — is complete and safe: every field on those types is type-system metadata,
never slow user work, and module *loading* (the slow part) is `Query.moduleSource` /
`asModule`, whose receiver is **not** a reflection type, so it stays profiled. (Keep the
root-entry list — `__schema`/`function`/`typeDef`/`sourceMap`/`currentTypeDefs`/`__*TypeDef`
— since those are Query-level, receiver ≠ reflection type.)

**B + C + D converge on one predicate.** A **separate, debug-independent predicate** =
`rootIntrospectionField(call) || receiverIsReflectionType(call)` resolves all three: no UI
change (B), complete/robust (C), and **cheaper (D)** — it needs only the *immediate* receiver
(one resolution), versus `introspectionInfo`'s walk *up the whole chain* to return false for
non-introspection calls. Recommend this over extending `introspectionInfo`. (The doc's
"single source of truth / oracle" argument for one classifier is real but weaker than the
combined A+B+C+D case; the separate predicate keeps the oracle comparable too.)

**D (performance) — agree to measure in v1; the lead's specific mitigation is misdiagnosed.**
The doc moves `introspectionInfo` ahead of the `IsSkipped` early return (necessary — §3.4 —
to classify inherited-skip descendants by their own recipe), so it now runs for the ~33k
recording-but-skipped descendants. The lead's "`!IsRecording` short-circuit" does **not**
help: those descendants *are* recording (WithSkip never strips the span — that is the bug).
The real cost is `introspectionInfo`'s chain-walk (each level a `ReceiverCall` →
`egraphMu.RLock`), worst-case for the *non-introspection-under-hideCtx* calls (clone/dep-load)
that walk to the root before returning false. The **receiver-type predicate (C) is the
mitigation** — O(1) immediate-receiver, no chain walk. So: ship the cheaper predicate and
**measure** (don't defer).

**E (exec-split / service-start assumption) — agree, make it a hard check.** §6 asserts those
sites never emit a skipped-class span because introspection never triggers container-exec /
service resolvers. Plausible (introspection is metadata), but it is an assumption; the §9
capture must assert *zero* skipped-class spans originate there, and the doc already says "if
one ever could, the same work-flag gate applies" — make that a gating check, not a footnote.

---

## 5. Does it ACTUALLY solve the volume regression? + one cross-cutting interaction

**Conditional yes.** It removes the introspection-class `call_exec`/`publishResult` (the
stated 100% of the increase), so volume returns to ~baseline **iff the predicate catches the
full class** (→ receiver-type, point C) and §9 confirms. Note the static cut *also* profiles
the real shared work under `hideCtx` (clone/dep-load) that `main` suppressed by inheritance
(§11c) — correct to keep, but it means the volume win rests on introspection being the
dominant *miss* class; §9.1's residual-name inspection is load-bearing, not optional.

**Cross-cutting (flag for the council): `publishResult` parentage is orthogonal but needed
for a clean gate.** The kept (non-skipped) `publishResult` spans are still emitted
**parentless** (my separate finding, `wcprof-otel-publishresult-chunk3-impl.md`: they are
parented by context-propagation through the already-ended `call_exec`). The skip fix reduces
their *count* but does not fix the *parent* of the survivors. Those parentless internal-kind
roots do **not** trip `OrphanedParents` (empty `cpSpan`) — so §9.2's `OrphanedParents == 0`
can pass — but they *would* trip the internal-kind-root faithfulness signal I proposed. So:
the skip fix and the explicit-`execSpanCtx` parenting fix are independent and **both** needed
before the gate is fully clean under that signal. The doc should cross-reference this so "gate
clean" isn't declared on the skip fix alone.

---

## 6. Council questions

- **(a) Correct / free win?** Correct, and ~free for the goal *with the static work-keyed
  cut and target-flag gating* — including the lazy path, where target-flag gating (not the
  shared-recipe property) is what closes it. The dynamic per-caller cut is correctly rejected
  (race-dangle).
- **(b) Zero inference / no analysis complexity?** Confirmed — loader/replay untouched, graph
  self-consistent by construction.
- **(c) Goal preserved, user work first-class?** Yes; only the introspection/metadata class
  goes absent (its time folds honestly into the kept ancestor that synchronously spent it).
  One tiny exception to acknowledge: a user forcer's wait on an introspection-produced lazy
  value is dropped.
- **(d) Simple or massive?** Moderate/localized, and the **separate debug-independent
  receiver-type predicate is simpler** than the debug-gated `introspectionInfo` extension (no
  UI coupling, no determinism caveat, cheaper). The CallRequest-stamp is justified by lock
  safety.

**Net:** implement as specified, but (1) use a separate, debug-independent, receiver-type
predicate (resolves A/B/C/D); (2) state in §4 that lazy consistency rests on target-flag
gating, not shared-recipe; (3) make E a hard §9 check; (4) cross-reference the orthogonal
`publishResult`-parent fix as also required for a clean gate; (5) acknowledge the
user-forcer-on-skipped-lazy wait loss. Land only after the §9 measurements.

**(Carried, separate):** service.start §3.4 self-erasure re-root still owed in both sources.
