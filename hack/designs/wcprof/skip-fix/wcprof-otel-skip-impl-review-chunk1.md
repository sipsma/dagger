# wcprof × OTel — skip-fix impl-plan review (Chunk 1 / loader+gate owner)

**Reviewer:** Chunk 1 owner — I wrote the offline loader and the §6.1 gate
(`OrphanedParents`/`UnresolvedWaitTargets`), so **point A is mine to settle**. Read the
plan in full and re-verified every load-bearing claim against the branch at HEAD
`4585bf413d` (the implementer's worktree). No code, no commits.

## Verdict

**Strong plan; the static work-keyed cut is the right architecture and I confirm it is
self-consistent with zero loader/replay change.** It also supersedes the Option-E
"redirected fixed-delay wait" I floated last round — under a *static* cut the
cross-boundary wait is impossible, so there is nothing to redirect, and I withdraw
Option E (it solved a *dynamic*-cut problem). **Landable, with four changes**, one of
which is decisive and mine to call:

1. **[A — decisive, settled below] Drop the debug gate: use a debug-INDEPENDENT
   predicate.** The doc's debug-gating rests on a debug-orphan argument that is
   **mechanically false against my loader**. With the justification gone, debug-gating
   is strictly worse — it re-introduces the BSP amplifier in debug mode, the one mode
   you most need clean telemetry.
2. **[B+C — converges with A] Make it a SEPARATE predicate, classified by receiver
   TYPE**, leaving `introspectionInfo`/normal telemetry untouched. A+B+C all point at
   the same artifact.
3. **[D] Measure the classification cost in v1**; the proposed `!IsRecording`
   short-circuit is weaker than it looks (OTel records in production).
4. **[E] Make "exec-split/service-start emit no skipped-class span" a HARD §9 check**,
   not a stated assumption.

The core mechanics — static recipe cut (§4), `CallRequest.SkipProfile` stamped in
`AroundFunc` (§3.4), flag kept distinct from target validity (§5.5), native+OTel
symmetry (§5.4), publishResult/pubOp following for free (§5.2) — are **sound and I
confirm them** (details under "Confirmations").

---

## Point A — SETTLED: the debug-orphan argument is false; use a debug-independent predicate

**The doc (§3.2/§5.4/§10) chooses the *debug-gated* `introspectionInfo` primarily
because a debug-independent predicate would, it claims, "skip the `call_exec` while the
normal span records, so a kept child parents to a non-op span → `OrphanedParents`."
That is wrong. There is no such thing as a "non-op span" in my loader.** Verified:

1. **Every deduped span becomes an op — no kind filter** (`loader.go:251-254`):
   `opIDBySpan[s.SpanID] = i+1` for *every* span; each then becomes an op event
   (`:341-353`) or an open-op (`:329-337`). Nothing is excluded.
2. **A normal `dag.call` span is a present `"call"` op** (`classifyKind`,
   `loader.go:434-447`): a span with `DagDigestAttr` and no `wcprof.op.kind` returns
   `"call"`; even with *no* attrs it is still a present op (kind `""`). Either way it
   is in `opIDBySpan`.
3. **`OrphanedParents` fires ONLY when the parent SPAN is absent** (`loader.go:303-306`):
   `parentID := opIDBySpan[cpSpan]; if cpSpan != "" && parentID == 0 { OrphanedParents++ }`.
   A *present* normal span ⇒ `parentID != 0` ⇒ **no orphan**. The check is a direct
   map lookup (`causalParentSpanID`, `:471-475`), not a chain walk.
4. **The normal span is genuinely the recorded parent in that scenario**
   (`objects.go:655-665`): `AroundFunc` returns `telemetryCtx` carrying its normal
   span and the call sets `ctx = telemetryCtx` *before* `cache.GetOrInitCall(ctx, …)`
   (`:678`). So when profiling is skipped (`call_exec` gated off, `callCtx`
   unreassigned at `cache.go:3733`) the resolver's sub-calls nest under the **present
   normal span**; the child records *that* span id as `parentId`. Present ⇒ resolves ⇒
   no orphan.

**So a debug-independent predicate does NOT orphan anything.** In *non-debug* the
skipped builder makes no span and children re-home to the recording ancestor (the
doc's own §4.1, correct). In *debug* with a debug-independent predicate the skipped
builder's normal span is present and children parent to it. **Both parent to a present
node; `OrphanedParents == 0` either way.** The doc's premise — that the loader only
mints ops for wcprof-marked spans — is the error; it mints one for *every* span.

### Therefore: debug-independent is preferable, and debug-gating is actively harmful

With the orphan justification gone, the remaining reasons for the debug-gated choice do
not survive, and one is a real defect the doc does not flag:

- **Debug-gating brings the regression back in debug** (the doc frames this as a
  "debug bonus," §5.4.4/§10, without flagging the cost). In debug the predicate is
  *false* for the schema-builders ⇒ `call_exec` + `publishResult` are minted again ⇒
  the ~33k-span amplifier returns ⇒ the BSP queue overflows ⇒ the orphan/unresolved
  losses this fix exists to kill come back — **in the exact mode you capture traces to
  debug a problem.** That is not a bonus; it is the regression behind a flag.
- **Debug-independent is a *pure* function of the recipe** — it removes the doc's own
  §10 "debug baggage is the one non-recipe input" caveat, the single thing that keeps
  the cut from being fully deterministic and forces the "gate on the target's stored
  flag for robustness" hedge (§4.2). Pure recipe ⇒ waiter and target *always* agree ⇒
  the static cut's guarantee is unconditional, not "self-consistent either way."
- **Oracle comparability is unaffected:** native and OTel both consume the same
  predicate, so both drop the introspection class in every mode — the oracle stays
  comparable (it simply never compares introspection, which is the premise: it's
  uninteresting). The "debug-consistent oracle" argument is satisfied without
  re-profiling.
- **The only thing lost is re-profiling introspection in debug — which the doc itself
  calls a "bonus."** Not worth re-importing the overflow.

**Settlement: use a debug-independent predicate.** This is the loader/gate owner's
call and it is unambiguous from the code.

---

## A+B+C converge: one SEPARATE, debug-independent, receiver-TYPE predicate

Point A wants debug-independent; **point B** (keep `introspectionInfo`/UI untouched)
wants a separate predicate; **point C** (robust/complete) wants receiver-type
classification. These are the same artifact:

```
profSkip(call) := receiver-type ∈ {Function, TypeDef, FunctionArg, ObjectTypeDef,
                  InterfaceTypeDef, InputTypeDef, FieldTypeDef, ListTypeDef,
                  EnumTypeDef, EnumMemberTypeDef, ScalarTypeDef}   // any field on a reflection type
                  || introspection-root(call)                      // __schema, currentTypeDefs, sourceMap, …
```

separate from `introspectionInfo`, **not** debug-gated. Why this dominates the doc's
"extend `introspectionInfo`, debug-gated, §3.2a named accessors":

- **It is still a pure function of the recipe**, so the §4 acid test holds unchanged
  (waiter and target of a shared key share the recipe ⇒ same `profSkip` ⇒ no
  cross-boundary wait). I confirm the acid test is sound (see Confirmations).
- **Complete, not workload-specific** (point C): it catches *every* schema-walk
  accessor — `Function.args`/`sourceModuleName` (the ~3k inherited-skip class the doc
  must hand-name in §3.2a) and any future one — without the §9.1 "refine the field
  list" loop. The doc's 3.2a is the under-catch risk it acknowledges.
- **No UI change** (point B): `introspectionInfo`/normal telemetry untouched, so a
  directly-called `TypeDef.asObject` still emits its `dag.call` span. The doc's
  extend-the-classifier path couples a profiler-volume fix to a user-facing telemetry
  change — avoid it unless the author specifically *wants* that UI suppression (if so,
  the additions must still be debug-independent to satisfy A, which then sits
  awkwardly beside the existing debug-gated cases — another reason to keep it
  separate).
- **Cheaper per call** (helps D): a receiver-*type* check needs only the *immediate*
  receiver's type — **one `ReceiverCall` hop** — versus `introspectionInfo`'s full
  receiver-chain walk to find the `__schema` root. So it both broadens the cut and
  shrinks the per-call cost.

**One caveat to discharge (over-cut):** the receiver-type cut assumes *no field on
those reflection types does real user work* (the doc asserts this in §3.2 but only
audits a named subset). Before shipping 3.2b-style, do a one-time schema audit that no
reflection-type field triggers container/exec/module-load work; exclude any that does
(fall back to a name list for it). An over-cut here is **not** a dangle (still
self-consistent — point A holds), but it silently coarsens *real* work, violating
"user-work first-class," so it must be checked, not assumed. The §9.1 residual
measurement is the backstop, but the audit is cheap and should be done up front.

---

## Confirmations (the lead's AGREE-WITH list, re-verified against my code)

- **Static cut + §4 acid test — SOUND.** "To wait on cache key `K` you must call `K`,
  so a waiter's `profSkip` equals its target's" (§4.2) is correct: `ongoingCalls` is
  keyed on the recipe digest (`callKey`, `cache.go:3669-3677`) and `profSkip` is a
  function of that recipe, so a non-skipped op cannot hold a wait into the skipped set.
  Combined with re-homing (§4.1, confirmed via the loader + `objects.go` threading
  above), every kept edge points at a kept node. **This is the elegant core, and it is
  why my Option E is unnecessary** — I withdraw it. (My prior hole was real *for the
  dynamic per-caller cut*; the static cut eliminates the race the doc names in §4.2,
  which is exactly the failure I described.)
- **`CallRequest.SkipProfile` stamp over a cache-side predicate — agree, the
  lock-safety basis is real.** `introspectionInfo → ReceiverCall →
  resultCallByResultID` takes `egraphMu.RLock` (`result_call_frame.go`); calling it
  under `callsMu`/`lazyMu` (`cache.go:3732`/`:3017`) nests cache locks. Stamping in
  `AroundFunc` (outside any cache lock, `objects.go:656`) reduces the under-lock cost
  to a `bool` read. Note this lock argument **survives the switch to a receiver-type
  predicate** (it still resolves the receiver) — so the `CallRequest` plumbing is the
  right seam regardless of A/B/C.
- **Distinct `profSkip` vs `execSpanCtx.IsValid()` (§5.5) — agree, and it protects MY
  gate.** Keeping the wait gated on the bool, not on target validity, means a
  *non*-skipped target with an invalid span still emits the targetless wait →
  `UnresolvedWaitTargets > 0` → hard-fail (`gate.go:132-133`). The mixed/untraced-
  recording detector stays intact. Do not overload validity as "skipped" — confirmed.
- **The chunk4 "open-subtree orphans children" rationale is mechanically wrong (§3.3) —
  agree.** Re-homing (every span an op + parent resolved by direct lookup) means
  `isMeta`/`NoTelemetry` would *not* orphan children even if included; the
  exclude-conclusion stands on the *pragmatic* grounds (dedup is the only
  correctness-grounded exclusion). My point-A settlement rests on the same loader fact,
  so this is internally consistent.
- **publishResult/pubOp follow for free, and that AUTO-ENFORCES the pairing I demanded
  last round.** `publishResult` is gated on `oc.execSpanCtx.IsValid()` (`cache.go:4016`);
  skip the `call_exec` ⇒ invalid ⇒ no publishResult. So "skip call_exec and
  publishResult *together*" (my prior requirement to avoid orphaning publishResult) is
  structural, not a separate gate — good. (Verify-item the doc itself flags:
  `(*wcprof.Op)(nil).ID() == 0` for native pubOp, `cache.go:4004`.)

---

## D — performance: measure in v1; the short-circuit is weaker than it looks

Moving `introspectionInfo` ahead of the `IsSkipped` early-return (§3.4) makes it run
for the ~33k inherited-skip descendants that currently return early — and this is the
**always-on production OTel path** (`OTelProfActive == IsRecording`, and production
records). So the proposed `!IsRecording` short-circuit (§10) **does not fire in
production** — it only helps when neither profiler is active, which production isn't.

So: **agree with the lead — put a short-circuit in v1 and MEASURE, do not defer.** Two
real mitigations beyond the (weak) recording short-circuit:
- The **receiver-type predicate** (point C) is one `ReceiverCall` hop, not a full
  chain walk — the cheapest correctness-preserving reduction, and it's already
  motivated by A/B/C.
- **Cache the classification** on the receiver/result if the §9 profile shows the
  `egraphMu.RLock` per descendant is hot under contention.
The decision matters because this fix exists to *fix* a production cost; it must not
trade a span-volume regression for a lock-traffic one. The §9 plan should add a
classification-cost measurement, not just a volume measurement.

## E — exec-split / service-start: make it a hard check

§6's "the executor exec-split and service-start never emit a skipped-class span, so no
gate needed" is an **assumption** (introspection never enters container-exec/service
resolvers). Plausible, but it's exactly the kind of thing that's true until a future
refactor routes a typedef-load through a service. **Make §9 grep the post-skip capture
for any skipped-class `call_exec` name carrying an exec/service work-type** and assert
zero — a hard check, cheap, and the doc already says "if one ever could, the same
work-flag gate applies," so wire that gate's *absence of need* to a test rather than a
sentence.

---

## Other notes

- **Gate is hard-fail on both signals** (`gate.go:132-133` UnresolvedWaitTargets,
  `:138-140` OrphanedParents) — so §9.2's "`0`/`0` on a fresh capture" is the correct,
  load-bearing acceptance bar. Keep it as the merge gate; it is the thing that proves
  the boundary is self-consistent on real data, not just on paper.
- **§9.6 determinism check is well-aimed** (intermittent `UnresolvedWaitTargets > 0`
  would be the dynamic-cut race signature). With the debug-INDEPENDENT predicate it
  should be deterministically `0` with no debug-uniformity asterisk — another reason to
  prefer A.
- **Tangent, not this fix:** the whole `call_exec`+`publishResult`-per-cache-miss emit
  is always-on in production (gated only on `IsRecording`). This fix removes the
  *introspection* multiplier, but the per-miss span pair on real work remains
  always-on overhead — a scaling question owned by the canonical "always-on 2nd source"
  decision, not here. Worth a sentence in the doc so it isn't mistaken for solved.

## Answers to the council questions, from the loader/gate seat

- **Adds analysis-side complexity / inference?** **No.** Zero loader/replay change
  verified — the engine emits a smaller, self-consistent graph; the loader resolves
  every kept edge by the same direct lookup it already does. No orphan to re-home in
  the loader, no target to invent, no new vocab. The principle is honored.
- **Solves the volume regression?** **Yes**, *if* the cut catches the full class — the
  receiver-type predicate (C) closes the doc's §3.2a under-catch risk; §9.1 validates.
- **Correct / free for the goal?** **Yes** with the static cut — confirmed; my prior
  wait-edge hole is closed by construction, not papered over.
- **Simple or massive?** Moderate and localized, as the doc says — and *simpler* with
  the separate receiver-type predicate (no `introspectionInfo` surgery, no field-list
  chasing, no debug-uniformity caveat).

**Bottom line:** approve the architecture; require (A) debug-independent predicate
[settled — the orphan justification is false against `loader.go:251-254`/`:303-306`],
adopt (B+C) a separate receiver-type predicate with a one-time over-cut audit, (D)
measure the classification cost in v1, (E) make the exec/service no-skip a hard §9
check. Land only after §9's `0`/`0` gate and the volume + classification-cost numbers
pass.
