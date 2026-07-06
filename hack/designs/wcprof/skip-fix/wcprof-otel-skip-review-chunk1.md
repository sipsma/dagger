# wcprof × OTel — "skip the introspection subtree" fix plan review (Chunk 1 / gate+loader owner)

**Reviewer:** Chunk 1 owner — I built the offline loader and the §6.1 structural
gate (the hard-fails on orphaned parents and unresolved wait targets that this fix
must not re-trip). Verified against the current branch HEAD `4585bf413d`. No code, no
commits.

## Verdict up front

- **(a) Correctness — NOT a free win as proposed; one real hole.** The *parent-edge*
  half of the acid test is **safe** (subtree-closure + natural re-homing — explained
  below). The *wait-edge* half is a **real hole**: skipping by the caller's
  telemetry-visibility breaks the uniform-recording invariant the emit explicitly
  relies on, and re-creates the unresolved-wait-target gate failure on an otherwise
  good user trace. The fix's own code (`EmitOTelWait`) documents this failure mode.
- **(b) Zero-inference — the *naive* skip violates it; a corrected skip preserves it.**
  As proposed it forces either a gate refusal of a good trace or (if someone "fixes"
  it by re-homing the dangling target) inference. With explicit boundary handling
  (Option E below) it stays zero-inference.
- **(c) Preserves the goal — yes, with the corrected skip.** User work stays
  first-class and its blocked time stays honest; the cost is that a *few*
  cross-boundary shared loads become coarse (fixed-delay), which Erik has accepted for
  this class.
- **(d) Simple or massive — the safe version is moderate, targeted, same emit shape.**
  Not a rewrite. A per-call gate + one flag on `ongoingCall` + one branch in the wait
  path + one vocab entry.

**"Skip by the caller's telemetry-visibility is the wrong cut" — confirmed.** It is
the right cut for *parents* and the wrong cut for *waits*. The clean fix keeps the
subtree-closure skip and adds explicit redirection for the edges that cross *into* the
skipped set.

## The mechanism — confirmed in code

`getOrInitCallInner` mints the call_exec gated **only** on `OTelProfActive` (cache.go:3732):

```go
var execSpan trace.Span
if OTelProfActive(callCtx) {                      // == trace.SpanFromContext(ctx).IsRecording()
    callCtx, execSpan = beginOTelCallExec(callCtx, ...)
}
...
if execSpan != nil { oc.execSpanCtx = execSpan.SpanContext() }   // cache.go:3757
```

`OTelProfActive` is true for a suppressed call **because the nearest non-suppressed
*ancestor* span is still recording** — `WithSkip`/`NoopDone` only set a context value,
they do not stop the ancestor recording. So the gate fires for exactly the calls
`AroundFunc` suppressed. Confirmed proximate cause.

I also confirmed the **volume class is mixed**, which forces the cut to honor
*propagating* `IsSkipped`: `introspectionInfo` classifies `__schema`,
`Function.withArg/__*`, `TypeDef.with*` per-digest (and *sets* `WithSkip`), but
`Function.args` / `Function.sourceModuleName` are **not** in its lists — they are
suppressed only via **inherited `IsSkipped`** from the three typedef-loading `hideCtx`
sites (`core/modfunc.go:867`, `core/sdk.go:253`, `core/sdk/module_typedefs.go:99`). So
a per-digest-classification-only skip would miss a chunk of the volume; catching all
of it **requires** honoring the propagating `IsSkipped` — i.e. subtree-closure.

## Parent edges — SAFE (the half of the acid test that holds)

The worry was "can a non-skipped (user-work) call be a child of a skipped
(introspection) call." With subtree-closure (skip on propagating `IsSkipped`) the
answer is **no, and even if it could, re-homing is automatic and inference-free**:

1. **The introspection subtree is closed.** Every node under a `hideCtx`/introspection
   node inherits `IsSkipped` (it propagates), so there is no non-suppressed node
   *inside* the skipped set. A non-suppressed node's parent therefore never points into
   the skipped set.
2. **A skipped call makes no span, so its children re-home naturally.** When we gate
   off `beginOTelCallExec`, `sharedWorkCtx` carries no call_exec span, so the
   resolver's sub-calls nest under the span that was already current — the nearest
   *recording* ancestor (the very span that made `OTelProfActive` true). The child's
   recorded `parentId` is a present span. No orphan, no inference. The loader reads it
   directly.

So the new `OrphanedParents` hard-fail (added at `4585bf413d`) will **not** trip from
the parent side — *provided publishResult is skipped together with its call_exec*
(skipping the call_exec but emitting its publishResult would orphan it). The plan does
keep them paired; keep it that way, and treat the `OrphanedParents` gate as the
backstop that proves the pairing held.

**One parent-side caveat to verify, not hand-wave:** the Chunk 3 `wcprof.parent`
override. A lazy op stamps `wcprof.parent` = the call_exec that *forced* its
evaluation. If a **suppressed** introspection call forces a **non-suppressed** lazy op,
that op's `wcprof.parent` points into the skipped set → `OrphanedParents` hard-fail.
This is the same cross-boundary shape as the wait hole, on the causal-parent edge.
Verify whether introspection ever forces a non-suppressed deferred op (it may not —
typedef-forced lazies are themselves suppressed); if it can, the fix is the boundary
rule below (fall back to the natural `parentId` when the stamped trigger is
suppressed), not a dangling override.

## Wait edges — THE HOLE (the fix's own code documents it)

The wait is emitted from the cache layer **specifically because** a suppressed caller
never enters `AroundFunc` (cache.go:3943-3949 comment), targeting `oc.execSpanCtx`,
and "for a joiner this is the only edge connecting it to the execution." `EmitOTelWait`
(otelprof_hooks.go) then states the load-bearing assumption verbatim:

> "In the always-on model the work owner and every waiter record **uniformly**, so a
> recording waiter's target (`oc.execSpanCtx` for call_exec...) is **always valid
> (Invariant T)**. The only way it is invalid here is a **non-uniform /
> mixed-recording trace**... We must not drop the edge silently... the loader resolves
> no target and counts an unresolved wait, and the **structural gate fails loud**...
> Such a trace... cannot be faithfully analyzed anyway, so failing loud is the correct
> outcome."

**The proposed skip deliberately manufactures that non-uniform trace.** Concretely:

- A shared singleflight digest `D` (the obvious candidate is `Query.moduleSource` —
  the cycle investigation already showed concurrent `Query.moduleSource` call_execs
  cross-referencing) is reachable both from inside a `hideCtx` (typedef loading /
  schema build, suppressed) and from non-suppressed user work.
- If the **suppressed** caller wins the singleflight and becomes the executor, the fix
  gates off its call_exec → `execSpan == nil` → `oc.execSpanCtx` is the zero value
  (invalid) at cache.go:3757.
- A **non-suppressed** user caller then joins `D` and waits. It is *not* suppressed, so
  its wait is *not* skipped; it calls `EmitOTelWait(oc.execSpanCtx=invalid, ...)` and
  takes the documented invalid-target path → **targetless wait → loader counts an
  unresolved wait → my §6.1 gate hard-fails** on a trace whose user-work portion is
  perfectly good.

This is exactly the acid-test failure: skipping the introspection subtree leaves a
**non-skipped node with a wait-target pointing into the skipped set**, and the gate
(correctly, given the data is now non-uniform) refuses it. The skip converts the
"shouldn't-happen, cross-session" non-uniformity the comment anticipates into a
**systematic, intra-session** non-uniformity on the *exact workload this fix targets*
(module loading). I cannot 100%-confirm the dangling fires without the trace, but the
structural preconditions are all present and `Query.moduleSource` is a known
cross-concurrent singleflight — this must be assumed live, not hoped absent.

## The unifying principle for the fix

Skipping a subtree is **introducing a faithfulness boundary**. The graph stays
self-consistent iff **every edge that crosses *into* the skipped set from a
non-suppressed node is explicitly redirected at the boundary** — never left dangling
(a dangling edge *is* the non-uniformity the gate exists to fail on). Three edge kinds
cross in:

- **Sub-call parent edges** → handled *automatically* by natural re-homing (skipped
  call makes no span). ✔ Nothing to do.
- **Wait edges** (non-suppressed joiner → suppressed executor's call_exec) → must be
  redirected explicitly. **Do not silently drop it** — a dropped wait inflates the
  joiner's self-time (the under-serialization the gate catches) and is *unfaithful*.
- **`wcprof.parent` overrides** (non-suppressed lazy → suppressed trigger) → fall back
  to the natural `parentId` (verify it can occur first).

Redirecting these is **restoring uniformity, not inference** — each is an explicit emit
decision recording a true fact, honored by the loader's existing mechanisms.

## Options (and the recommendation)

**Option B — the plan as written (skip call_exec/publishResult/wait by
`IsSkipped|introspection|NoTelemetry`).** Fixes volume, parent-safe — but has the wait
hole. **Not landable alone.**

**Option E — subtree-closure skip + explicit boundary handling (RECOMMENDED).**
1. Gate call_exec + publishResult on `!suppressed` (`suppressed = IsSkipped(ctx) ||
   introspection || field.Spec.NoTelemetry`) at cache.go:3732 / :4017. [volume ✓,
   parent-safe ✓]
2. Record `oc.execSuppressed = true` when the executor gates its call_exec off
   (cache.go:3755).
3. In the wait path (cache.go:3940): *waiter suppressed* → skip the wait (consistent);
   *non-suppressed + valid target* → normal targeted wait; *non-suppressed + invalid
   target + `oc.execSuppressed`* → emit a **fixed-delay / resource-class wait**
   (`reason = suppressed`); *non-suppressed + invalid + not suppressed* → leave it
   targetless (real loss → gate fails, correct).
4. Vocab: one `WaitReason` ("suppressed") in the lock/fixed-delay family, so the
   gate's `UnresolvedWaitTargets` excludes it (same as `lock`) and it compiles to the
   existing **finish-invariant** max-based fixed-wait segment (the model I
   re-confirmed last round). No new loader logic.
5. `wcprof.parent` fallback for the lazy cross-boundary case (if it occurs).

Net: the suppressed shared work becomes a **non-scalable fixed delay** for the user
caller — the blocked *time* is preserved (user self-time stays honest, makespan stays
honest), zero inference, gate green. The only loss: you cannot *what-if* that shared
load (it's modeled as fixed). That is the accepted "coarse/absent for this class."
**Moderate, targeted, same emit shape.**

**Option C — mint-always + drop-unless-observed (the literal "emit iff a non-suppressed
consumer observes it"; UPGRADE).** Always mint the call_exec (so it is the correct
parent *and* a valid wait target), tag it when a non-suppressed caller waits, and have
a span processor **drop on end** any suppressed-subtree call_exec with no
non-suppressed-observer tag (do not live-export it). This *preserves what-if-ability*
on cross-boundary shared loads — strictly more faithful than E. Cost: a custom
processor + observer tagging + ensuring those call_execs aren't live-exported (else the
BSP amplifier isn't actually removed). **Recommend only if profiling *why module
loading is slow* requires what-if-ing those shared loads** — plausible for this
workload, but start with E and reach for C if the coarse loads turn out to matter.

Note a lazy-mint shortcut does **not** buy fine profiling: a call_exec minted by the
joiner *after* the resolver ran is a childless sibling of the re-homed sub-calls, so it
either double-counts (full interval as self-time) or collapses to a zero-dur marker —
which is just Option E by another name. Fine profiling requires minting up front
(Option C); coarse requires only Option E.

## Answers to the four questions

1. **Is "skip this data" correct — free win?** The *volume* reduction is a free win
   (Erik accepts losing introspection profiling). The *naive skip* is **not** free — it
   trips the wait-target gate on a good user trace. It's a free win **once the
   boundary edges are redirected** (Option E).
2. **Can we skip with zero new inference?** Yes — Option E. The fixed-delay wait and
   the `wcprof.parent` fallback are explicit emit facts honored by existing
   loader/gate mechanisms; no heuristic enters the analysis. (The naive skip would
   *force* inference or a refusal — that's the thing to avoid.)
3. **Simple or massive?** Option E is moderate and same-shape. Option C is the
   bigger change (a processor) and only if fine-profiling shared loads is required.
4. **If clean skipping needs care:** it does, and the cleanest is **Option E** —
   subtree-closure skip (the right *parent* cut) **plus** explicit redirection of the
   wait edges crossing the boundary (the part "skip by caller visibility" gets wrong).

## Per-reviewer bottom line

- **Hole:** cache.go:3732 (gating off the executor's call_exec) → cache.go:3757
  (`oc.execSpanCtx` invalid) → cache.go:3946 (`EmitOTelWait` invalid-target path) →
  §6.1 `UnresolvedWaitTargets` hard-fail, on a non-suppressed joiner of a
  suppressed-executor shared digest (`Query.moduleSource` under `hideCtx`). The emit's
  own Invariant-T comment confirms this is the designed-for failure on a non-uniform
  trace; the skip manufactures the non-uniformity.
- **Secondary hole to verify:** `wcprof.parent` (Chunk 3) stamped at a suppressed
  trigger for a non-suppressed lazy op → `OrphanedParents` hard-fail.
- **Cleanest emit-side option:** Option E (subtree-closure skip + `oc.execSuppressed`
  + non-suppressed-joiner-on-suppressed-target → fixed-delay/resource wait + a
  "suppressed" wait reason in the lock family + `wcprof.parent` fallback). Option C if
  the cross-boundary shared loads must stay what-if-able.
- **Keep paired:** call_exec and publishResult must be skipped together (else
  `OrphanedParents`). Let the two new gate signals (orphaned-parents, unresolved-wait)
  stand as the merge gate proving the boundary is self-consistent.
