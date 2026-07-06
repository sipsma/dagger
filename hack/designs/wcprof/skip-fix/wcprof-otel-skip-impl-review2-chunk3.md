# Round-2 review of `wcprof-otel-skip-impl-plan.md` (v2) — Chunk 3 implementer (lazy / wcprof.parent owner)

**Scope:** read v2 in full; re-verified the changed claims against the skip-implementer
worktree (HEAD `4585bf413d`). Two jobs: (1) did v2 correctly incorporate round 1, and
(2) a fresh holistic pass. Review only — no code.

## Verdict

**v2 correctly incorporated round 1, and the architecture is sound — ship it after §9, with
two refinements.** The predicate flip, N1, N3 (my lazy catch), N4, D, E, N6–N8 are all
correctly reflected, and I re-verified the load-bearing ones against code. The 3 pushbacks
are correct. Two things to fix before/while implementing:

- **(R1, from N2) Make `profSkip` a field on the `ResultCall` frame, not a separate
  `sharedResult` bool.** v2's N2 per-path rule is correct, but its *audit list is provably
  incomplete* (it never mentions `cache_egraph.go`, which has three `storeResultCall` sites).
  Auditing N construction sites is fragile; putting the flag on the frame makes the invariant
  *structural* (`clone`/`fork` are the only two copy paths) and also resolves the import edge
  and the set-before-read timing for free.
- **(R2, new holistic) The N1 outer-call gate is asymmetric with the OTel side** for
  directly-called plain accessors. Benign in practice (rare, ~0 self-time) but the §5.4
  "both sources drop the same class" claim is imprecise; pin it and add a §9 oracle case.

Everything else I checked holds.

---

## 1. Changes — did v2 incorporate round 1?

### N1 (outer native `OpKindCall`) — VERIFIED CORRECT
cache.go:3559-3565: the early return `if !wcprof.Enabled(ctx) || req==nil || req.ResultCall==nil
{ return getOrInitCallInner(..., nil) }` already passes a **nil** `profOp` to the inner, and
the inner is nil-safe. So adding `|| req.SkipProfile` cleanly suppresses the
`wcprof.BeginOp(OpKindCall)` at :3562 with no inner change. The doc's claim is exactly right.
(But see R2 — *whether* it should be gated by `profileSkip` is the subtlety.)

### N2 (lazy `profSkip` provenance) — PATHS CORRECT, AUDIT SCOPE INCOMPLETE
The three `initCompletedResult` paths are real and the per-path rule is right, verified:
- adopt canonical (cache.go:4132 `canonicalEquivalentSharedResultLocked`) → leave intact ✓
- copy existing frame (cache.go:4150-4151 `oc.res.storeResultCall(frame.clone())` from
  `oc.val.cacheSharedResult()`) → copy `shared.profSkip` ✓
- store request frame (cache.go:4159-4160 `storeResultCall(req.ResultCall.clone())`) → from
  `req.SkipProfile` ✓

And the invariant "`profSkip` travels with the stored `resultCall`" is the right one. **But
the doc's §5.1 enumerated audit list is incomplete.** Grepping every `storeResultCall(` /
`&sharedResult{` site, these set/copy a `resultCall` and are **not** in the doc's list:
- `cache_egraph.go:1064` (`shared.storeResultCall(frame)` in content-digest teaching),
  `cache_egraph.go:1485` (`res.storeResultCall(requestFrame.clone())`),
  `cache_egraph.go:1654` (`res.storeResultCall(nil)`) — **`cache_egraph.go` is absent from
  the doc entirely.**
- `cache.go:1975` (`storeResultCall(req.ResultCall)`), `:2356`
  (`storeResultCall(req.ResultCall.clone())`), `:2568` (`storeResultCall(frame.fork())`),
  `cache_persistence_worker.go:437` (`storeResultCall(snapshot.frame)`).

**Severity: volume-only, never a dangle** (a wrongly-*false* `profSkip` profiles introspection
lazy work → leak; the wrongly-*true* direction is over-cut, a *predicate* concern, not
provenance). So this won't re-trip the gate — but it can silently undercut the volume fix, and
"audit every site" is exactly the kind of maintenance burden that rots.

**R1 — the clean fix: put `profSkip` on the `ResultCall` frame.** `profileSkip` is a pure
function of (receiver type, field), which *are* frame identity, so the frame is its natural
home. `ResultCall` has exactly two copy chokepoints — `clone()` (result_call_frame.go:207) and
`fork()` (:238), both field-by-field — so the entire provenance audit collapses to "copy one
field in two functions." Then:
- `req.SkipProfile` becomes `req.ResultCall.profSkip` (CallRequest embeds `*ResultCall`), and
  `sharedResult.profSkip` becomes `shared.loadResultCall().profSkip` — both trivial reads, no
  separate fields to keep in sync, **no `cache_egraph.go`/etc. audit.**
- Set it once in `AroundFunc` (lock-safe, outside cache locks — the lock argument is
  unchanged); the read sites under `callsMu`/`lazyMu` just read the struct field (no
  `egraphMu`), preserving the §3.4 lock-safety basis.
- **Resolves the import edge (open item a) for free:** recompute `profSkip` from the frame's
  own recipe at frame construction/deserialization (it's deterministic from receiver type +
  field), so imported/persisted frames are correct, not default-false. (If recompute at import
  is lock-awkward, the volume-only default-false fallback still applies — but the frame home
  removes the *intra-process* gap, which is the larger one.)
- **Resolves set-before-read:** the frame carries `profSkip` from construction, so it is never
  read before it is set (the round-1 timing worry).

If the council prefers to keep the `sharedResult` bool, then N2 must audit **all**
`storeResultCall` sites above (not the `&sharedResult{` list), and `storeResultCall` itself
(cache.go:1547) is the natural single chokepoint to set it — but the frame is cleaner.

### N3 (lazy self-consistency) — MY CATCH, CORRECTLY INCORPORATED
§4.2b states it correctly: the §4.2 "waiter shares the recipe" proof does **not** cover lazy
(forcer ≠ producer `resultCall`, cache.go:2980 vs the joiner at :2964/:2973), and lazy is
closed by gating every lazy wait on the **target's stored flag** `shared.profSkip` — and it is
labeled **load-bearing, not "robustness."** §4.4 names the user-wait-loss (a non-introspection
forcer blocking on an introspection-produced pending value loses its wait edge) as a decision.
§4.3 (`wcprof.parent`) is correct — the override targets the lazy op (otelprof_lazy.go:97-100),
which is minted-or-skipped *together* with the gate (:3017), so it can never point into the
skipped set. The explicit "don't simplify the lazy gate to the waiter's own bit" warning is
there. **Satisfied — my lazy/`wcprof.parent` concerns are correctly handled.** (R1 changes
*where* `profSkip` lives, not the lazy gating logic, which stays gated on the target's flag.)

### N4 (stamp coverage) + predicate + over-cut
- **Receiver-type predicate (§3.2): sound.** Verified the 11 types are real schema reflection
  objects, and spot-checked their fields are metadata: `Function`/`FunctionArg` blocks
  (module.go:418-490) are all `with*` builders, `__*` internals, and `args`/`returnType`/
  `typeDef` accessors — resolvers `s.functionWith*`/`functionArgs`/`functionReturnType`/
  `functionArgTypeDef`, no container/exec/module-load. A repo-wide grep for
  `call|sync|evaluate|withExec|asModule|load|container` field names on these types found none.
  The loaders (`Query.moduleSource`, `ModuleSource.asModule`) are non-reflection receivers →
  stay profiled. The completeness argument (one type rule vs per-field chasing) is correct.
- **N4 stamp coverage:** relying on `srv.Around` (core/modtree.go:589, schema_build.go:107) +
  the §9.2 zero-residual assertion as backstop is reasonable. Keep §9.2 a hard gate.

---

## 2. The three pushbacks — all correct

1. **`!IsRecording` short-circuit useless in production — CORRECT.** Production records, so
   `!IsRecording` is always false there; the guard never fires (and `!IsRecording &&
   !wcprof.Enabled` likewise). My round-1 phrasing ("doesn't help the recording-skipped
   descendants") was the same point; the implementer's stronger "useless in production" is
   right. The real mitigation is the cheaper predicate + memoization — but see H3 below: the
   memo may not avoid the lock either.
2. **Doesn't fix parentless-`publishResult` here — AGREE, genuinely separable.** It concerns
   *kept survivors'* parentage (parented through the ended `call_exec`), which this cut never
   touches; the skip only changes their *count*. Folding it in would couple two independent
   changes. §5.6's cross-reference (don't declare "gate clean" under the internal-kind-root
   signal on the skip fix alone) is the correct boundary. Owned by me; separable. ✓
3. **profiler-skip ⊋ UI-suppress by design — AGREE, with a caveat (R2).** Keeping the
   predicate separate so a directly-called `TypeDef.asObject` still emits its `dag.call` UI
   span is the right call (no UI coupling, profiler self-consistency is independent of UI).
   The caveat: that very asymmetry interacts with N1 on the *native/OTel* axis — see R2.

---

## 3. Open items — resolved

- **(a) Recompute `profSkip` at import vs default-false:** it is a **volume-only** edge (never
  a dangle), so not a merge blocker on correctness. **Resolve via R1:** with `profSkip` on the
  frame, recompute it from the frame's recipe at construction/deserialization (deterministic),
  eliminating the gap; if import-time recompute is lock-awkward, the bounded default-false +
  §9.1/§9.6 measurement is an acceptable fallback. Don't leave it unresolved, but don't block
  on it.
- **(b) Over-cut schema audit:** sound in principle (spot-checked above). **Make the one-time
  `dagql.Fields[*core.<ReflectionType>]` audit + §9.4 capture check a hard merge gate**, and
  in it specifically verify *no reflection-type accessor lazily forces real work* (e.g., a
  metadata accessor that triggers a pending container/module-load on access) — that is the only
  way receiver-type over-cut could coarsen real work, and it is the precise thing to rule out.

---

## 4. Holistic — fresh catches now that the design is concrete

**H1 = R1 (frame-based `profSkip`).** Already stated; the single biggest simplification — it
turns the N2 audit + import edge + set-before-read into one structural fact.

**H2 = R2 (N1 ↔ OTel `dag.call` asymmetry — new).** N1 gates the *native* outer `OpKindCall`
by `profileSkip`. Its OTel analog is the **normal `dag.call` span** emitted by `AroundFunc`
(core/telemetry.go:136), which `profileSkip` deliberately leaves **un-gated** (pushback 3 —
`introspectionInfo` untouched). For the *directly-called plain accessors* (`Function.args`,
`TypeDef.as*`, `returnType`, `typeDef`, `functions` — `introspectionInfo`=false but
`profileSkip`=true, the exact 3.4k residual class), the two diverge: **OTel keeps a
"call"-kind op** (the loader makes the present `dag.call` span an op, classifyKind →
`"call"`), while **native drops `OpKindCall`** (N1). So §5.4's "both sources drop the same
class → oracle matches" is **imprecise** — it holds for the hideCtx case (both suppressed, via
`IsSkipped`) and the `introspectionInfo`-classified case (both suppressed), but **not** for a
directly-called plain accessor.
- **Severity: low/latent.** In practice the plain accessors are *always* called under the
  typedef-loading `hideCtx` (that is why they have no `dag.call` on `main`, §1.3), so the
  divergent case is rare; and the outer ops carry ~0 self-time, so a self-time oracle barely
  notices. But it is a real internal inconsistency in the doc's symmetry claim.
- **The precise invariant:** the *outer* call op should be gated by the same decision in both
  sources — i.e., "did `AroundFunc` emit the normal span" (`!(IsSkipped || introspectionInfo
  || isMeta || !ShouldEmitTelemetry)`), which is what governs the OTel `dag.call` — **not**
  `profileSkip`. `profileSkip` is a *proxy* that matches except for directly-called plain
  accessors. **Recommendation:** either (i) accept the proxy and **pin §5.4** ("native
  `OpKindCall` is gated by `profileSkip`, which over-suppresses vs OTel's `dag.call` only for
  directly-called reflection accessors; impact is ~0-self-time and they are effectively always
  under `hideCtx`") and **add a §9 oracle case** that directly calls a reflection accessor and
  asserts the per-class self-time still matches within tolerance; or (ii) if the oracle proves
  sensitive, gate the outer op on the AroundFunc-suppression decision instead. Don't leave
  §5.4 claiming exact symmetry.

**H3 (memoization may not avoid the lock — refines D).** §5.7 proposes memoizing `profileSkip`
by `(receiverTypeName, field)`. But obtaining `receiverTypeName` requires `ReceiverCall →
resultCallByResultID → egraphMu.RLock` (result_call_frame.go:603/1381) — i.e., **you need the
lock to compute the memo key.** So the memo saves the set-membership test, not the
`egraphMu` acquisition, which is the cost D is about. Confirm whether the immediate receiver's
type name is available without resolving the receiver call (e.g., cached on the
`ResultCallRef`); if not, the memo does **not** mitigate the lock cost and the §9.9
measurement is the real arbiter (and R1's frame-stored bool, computed once in `AroundFunc`,
sidesteps the per-read cost entirely on the lazy path).

**H4 (minor, already acknowledged).** The static cut profiles real shared work under `hideCtx`
(clone/dep-load) that `main` suppressed by inheritance, so volume will **not** return to
`main` (§9.6 says so honestly). Just don't let a reviewer expect "≈ main" — the bar is "no
schema amplifier; residual is real per-miss work."

**No new correctness/dangle hole.** I re-stress-tested parent re-homing (loader.go:299-313:
every span an op; orphan only on absent parent span), singleflight + lazy waits (target-flag
gating), the `wcprof.parent` override, native symmetry, and the result-adoption provenance.
The only correctness-adjacent gap is N2 audit completeness, addressed structurally by R1; the
rest are volume/oracle precision, not dangles.

---

## 5. Council questions

- **(a) Correct / free win?** Yes for the goal, with the static cut — and now I'd add: make it
  *robustly* correct by homing `profSkip` on the frame (R1), so the by-construction
  self-consistency doesn't rest on a hand-maintained site list.
- **(b) Zero inference?** Yes — loader/replay untouched; smaller self-consistent graph; the
  §9 assertions (rightly elevated) are the proof.
- **(c) Goal preserved?** Yes, modulo the named §4.4 user-wait loss and the N6/§9.4 over-cut
  gate (make it hard).
- **(d) Simple or massive?** Moderate — and R1 makes it *simpler* (one frame field + two copy
  sites replaces the CallRequest field + sharedResult field + multi-site provenance audit).

**Bottom line: approve v2's direction; implement with `profSkip` homed on the `ResultCall`
frame (R1, collapses the incomplete N2 audit + open-item-a + set-before-read), pin the §5.4
oracle claim and add the directly-called-accessor case (R2/H2), verify the memo key is
lock-free or rely on §9.9 (H3), and make the over-cut schema audit a hard gate (open-item-b).
Land only after the §9 completeness + oracle measurements pass.**

**(Carried, separate):** service.start §3.4 self-erasure re-root still owed in both sources.
