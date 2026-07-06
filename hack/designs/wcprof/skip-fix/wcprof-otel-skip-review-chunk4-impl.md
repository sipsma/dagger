# wcprof × OTel — "skip the introspection subtree" fix plan: council review (chunk4 implementer)

Reviewer: chunk4 implementer. Verified against current branch code (HEAD on
`wcprof-otel-implementer-chunk4-7ad02bcf`). Grounded in file:line. I trusted
nothing in the plan text until I read the emit sites myself.

## TL;DR verdict

- **(a) Correctness — NOT a free win as written; one real hole + one unaddressed
  second site.** The cut is *sound in principle*, but the plan's framing ("skip
  calls `AroundFunc` would skip") is keyed on the **caller's** telemetry
  visibility, and that is the wrong key. The skip must be a function of the
  **work** (the call identity), decided once and read identically at every emit
  site. Done per-caller, it re-creates the exact unresolved-wait-target / orphan
  failures the gate now hard-fails on — on a *legitimate user trace*.
- **(b) Zero-inference — YES, preserved, if implemented as below.** Skipping is
  emitting *less*, not guessing. A dropped wait folds into the waiter's own
  self-time (coarse, but it is time the waiter genuinely spent blocked — not an
  inference). The one trap: do **not** "fix" the dangle by teaching
  `EmitOTelWait` to swallow invalid targets — that blinds the gate to genuine
  loss. Keep consistency *upstream* of the emit.
- **(c) Goal preserved — YES.** Introspection time folds into the nearest
  non-introspection ancestor's self-time (e.g. the module-load call), which stays
  profiled. "What was slow" still surfaces ("loading module X is slow") with
  user work first-class; you just can't drill *into* the schema walk — which Erik
  has said is fine, and is arguably the right granularity anyway.
- **(d) Simple or massive — MODERATE, localized.** No loader / replay / emit-
  *shape* change (this is strictly emitting a subset). But it is **not** a
  one-liner: the decision must be stored once and honored at **three site-pairs ×
  two sources × two paths** (singleflight + lazy), plus a small core→dagql plumbing
  for the deterministic variant. Contained to `dagql/cache.go` (+ one predicate
  hook). Call it a careful ~half-day change, not a patch.

**Bottom line: skip is the right direction and a clean win — but only if the skip
decision is attached to the WORK and applied symmetrically. The plan as literally
phrased (gate each emit on its caller's `IsSkipped`/`NoTelemetry`) is unsafe and
must not be implemented verbatim.**

---

## What I verified (so the council has equal footing)

**The amplifier is real and the mechanism is as described.** `OTelProfActive`
is `IsRecording()` only (`dagql/otelprof_hooks.go:41`), with no
`IsSkipped`/`NoTelemetry`/introspection check; `WithSkip` only sets a context
value and leaves the parent span recording (`dagql/internal.go:34`). So a
suppressed call whose parent is recording still trips `getOrInitCall` into
minting a `call_exec` (`dagql/cache.go:3732`). Confirmed.

**But "what `AroundFunc` skips" is five different things with three different
subtree semantics** (`core/telemetry.go:22-81`), and the plan conflates them:

| Skip path | Sets `WithSkip`? | Descendants skipped? | Safe to blind-skip its call_exec? |
|---|---|---|---|
| `introspection` (`introspectionInfo`, line 35-37) | **yes** | **yes (closed subtree)** | **yes** |
| `IsSkipped(ctx)` inherited (line 32-33) | — (inherited) | yes | yes (same subtree) |
| `isMeta` — `node`/`id`/`sync` (line 39-42, 456) | **no** | **no** | **NO — has live children** |
| seen-key dedup `!ShouldEmitTelemetry` (line 63) | **no** | **no** | **NO — has live children** |
| `field.Spec.NoTelemetry` (objects.go:655) | **no** (AroundFunc not even called) | **no** | **NO — has live children** |

The plan lists three criteria: "introspection-classified / `dagql.IsSkipped` /
`field.Spec.NoTelemetry`." The first two are the **closed** rows (safe). The
**third (`NoTelemetry`) is an open row**: a `NoTelemetry` field does not call
`AroundFunc` at all, so it sets no `WithSkip`, so its children are ordinary user
work that *do* get `call_exec`s nested under the `NoTelemetry` call's own
`call_exec`. Skip the parent's span and those children dangle (parent absent →
`OrphanedParents` → gate hard-fails). **Drop `NoTelemetry` from the skip
predicate** unless someone proves the three `NoTelemetry` sites
(`core/object.go:1168,1218,1264`) are leaves — and even then it buys ~nothing,
since the 33k volume is the *introspection* class, not `NoTelemetry`.

So: the safe skip key is **introspection-classified only**. Now the two real
risks.

---

## Hole #1 — the singleflight wait edge (the plan half-saw this; it is worse than stated)

`getOrInitCall` mints `execSpan` when a caller **claims** a singleflight and
stashes `oc.execSpanCtx` as the **joiner wait target** (Invariant T,
`cache.go:3732-3760`). Every caller — joiner *and* executor — then emits
`EmitOTelWait(ctx, oc.execSpanCtx, …)` (`cache.go:3958`).

The classification is keyed on the **call's receiver chain**
(`introspectionInfo(ctx, req.ResultCall)`, `core/telemetry.go:354`) — a *static
property of the call C* — **plus** the *dynamic* inherited `IsSkipped(ctx)`.
These two diverge exactly on a **non-introspection call reached from inside an
introspection subtree** (e.g. schema-building triggers a fresh module/dep load).
That call is `IsSkipped(ctx)==true` (inherited) but `introspectionInfo(C)==false`.

Failure sequence, all on a real user trace:
1. an introspection-context caller **claims** that shared non-introspection call
   C first (it is in-flight);
2. **user work concurrently joins** C;
3. plan gates the *claim's* `call_exec` on the claimer's `IsSkipped` → **no
   `execSpan`** → `oc.execSpanCtx` stays the zero value;
4. the user joiner is **not** skipped, so a per-caller gate does **not** suppress
   its wait → `EmitOTelWait(ctx, <invalid>, …)` fires.

And `EmitOTelWait` **deliberately emits the link even when the target is
invalid** (`dagql/otelprof_hooks.go` ~115-130): the design *wants* the gate to
fail loud on "a recording waiter joining shared work started by an untraced
session … cannot be faithfully analyzed." The proposed skip manufactures
*precisely that shape on purpose* → unresolved-target → **`gate.go:132` hard-fail
on an otherwise-good user trace.** This is not hypothetical; it is the explicit
contract of the wait emitter.

**The plan says to skip "the associated wait edge at cache.go:3943-3949" — but
does not say *on what condition*.** The only correct condition is the **work's**
decision, not the joiner's. See the fix shape below.

## Hole #2 — the lazy-eval path is a second, unaddressed instance

The plan never mentions it, but `cache.go` has the identical pattern for lazy
evaluation: `lazySpan` minted under `OTelProfActive(evalCtx)` (`cache.go:3017`),
`shared.lazyEvalSpanCtx` stashed, and joiner wait edges at `cache.go:2973` and
`:3103`. The same divergence (introspection-context leader, user joiner) dangles
the lazy wait identically. **Any fix that touches only the singleflight path is
incomplete and the gate will catch it on the next lazy-heavy capture.**

## Non-hole — publishResult already follows call_exec for free

`beginOTelPublishResult` is already guarded by `if oc.execSpanCtx.IsValid()`
(`cache.go:4017`). So if "skip" means "do not mint `execSpan`" (leaving
`execSpanCtx` zero), publishResult auto-skips. **Good** — but note this means the
*validity of `execSpanCtx` is already overloaded* as "was this profiled," which
is the trap in the next section.

## Hole #3 — cross-source oracle symmetry (native must skip too)

Native `execOp` is gated only on `wcprof.Enabled` (`cache.go:3717-3723`), with no
introspection check — so native emits the same 33k introspection ops. The native
`pubOp` (`cache.go:4010`) and `wcprof.BeginWait` mirror it. If we skip on the OTel
path only, the **cross-source oracle** (native vs OTel per-class self-time — the
load-bearing validation of this whole effort) diverges on the introspection
class. **The skip must gate native and OTel from one shared decision**, at all
three site-pairs.

---

## The cleanest shape (recommendation)

**Invariant to hold:** *a node's profiled-ness is a pure function of the call
identity C, decided once at claim/mint, stored on the shared work record
(`ongoingCall` / lazy `shared`), and read identically by (1) the `call_exec`
mint, (2) publishResult, and (3) every inbound wait edge — for both native and
OTel.* Then a call is **either fully profiled (span + all inbound waits) or fully
absent (no span, no inbound waits)** — self-consistent by construction, no kept
edge ever points at an absent node, zero loader/replay inference.

Concretely:
1. Add an explicit `oc.otelSkipped` / `shared.otelSkipped` bool (do **not**
   overload `execSpanCtx.IsValid()` — that signal already means "genuine
   cross-session loss → fail loud" at `otelprof_hooks.go` ~115 and
   `cache.go:2950`; conflating "deliberately skipped" with "lost" **blinds the
   gate**, which violates (b)).
2. Set it once, at claim/mint, from a **per-call predicate** (next section).
3. Gate on it: skip `call_exec`/`execOp` mint, skip `pubSpan`/`pubOp`, and **skip
   the `EmitOTelWait`/`BeginWait` call** (not inside the emitter — at the call
   site) whenever the *target work* is `otelSkipped`. Same for the lazy trio.

This is exactly the task's hinted rule — "emit the singleflighted node iff a
non-suppressed consumer observes the work, regardless of which caller initiated
it" — realized as "decide per work, store on the work record, read everywhere."

## Which predicate — and the determinism argument (Cut 2 over Cut 1)

- **Cut 1 — gate on `IsSkipped(ctx)` (dynamic, free in dagql).** Self-consistent
  *if* waits read the stored decision. **But** the profiled-ness of a *shared
  non-introspection* call then depends on **who won the claim race** — introspection
  vs user. The same workload can attribute the same user wall-time to a profiled
  child on one run and to absorbed self-time on the next. For a "what was slow"
  profiler that is a real, if bounded, defect (non-deterministic ranking).
- **Cut 2 — gate on `introspectionInfo(C)` (static, per-call).** Deterministic.
  A shared non-introspection call is **always** profiled (whoever claims), so user
  joiners always resolve; only the static-introspection class is dropped — which
  is the 33k. Boundary/shared calls stay profiled. **Recommended.**

**Cost of Cut 2 = one plumbing seam.** `introspectionInfo` lives in `core`
(it hard-codes Dagger schema field names); the emit lives in `dagql` (a lower
layer). So `dagql` cannot call it. Resolve by having `core` hand `dagql` the
per-call decision — mirror the existing `Server.Around(AroundFunc)` registration
with a registered skip predicate the cache calls with `req`, **or** stamp the bit
on the `CallRequest`. Either is small and keeps the Dagger-specific
classification in `core`. Do **not** reuse the propagating `WithSkip` value for
this — it cannot distinguish "C is introspection" from "C is under introspection,"
which is the very distinction Cut 2 needs.

**Parent-edge closure is already safe for Cut 2** (verified): `WithSkip`
propagates, so an introspection subtree is uniformly skipped; a kept descendant
(a fresh non-introspection root spawned from within it) re-homes to the nearest
recording ancestor *above* the boundary, because a skipped call mints no span and
therefore never replaces the active span in `Tracer(callCtx).Start(callCtx,…)`
(`otelprof_hooks.go:58`, `tracing.go:11`). That ancestor is a present user
`call_exec`. No parent dangle.

---

## Must-measure before trusting it (do not assume)

1. **Re-run the module-load workload and *count* post-skip volume.** Cut 2 keeps
   non-introspection calls that merely *sit inside* introspection subtrees; the
   residual is an empirical question. If volume is still high, inspect *which*
   calls remain — if they're boundary-shared, keeping them is correct; if they're
   pure schema-walk that simply didn't match `introspectionInfo`, refine the
   predicate (don't widen to `IsSkipped`).
2. **Re-run the structural gate on a fresh capture** — expect 0 unresolved-targets
   *and* 0 orphaned-parents from this change.
3. **Confirm native↔OTel oracle still aligns** on the post-skip capture (both
   sources dropped the same class).
4. Confirm a **lazy-heavy** capture (not just singleflight) stays clean.

This last point ties back to the open capture-loss thread: the 33k amplifier is
the most likely driver of the BSP-queue overflow behind the orphaned-parent loss,
so removing it should *also* shrink that loss — but that is a prediction to
verify on a fresh capture, **not** a settled claim, and it does not retire the
separate BSP-backpressure backstop for legitimate user-work bursts.

## Direct answers to the four questions

1. **Free win?** Directionally yes; literally no. Naive per-caller skip
   re-creates the orphan/unresolved-target hard-fails (Holes #1, #2) and breaks
   the oracle (Hole #3). Clean win only as the work-level stored-decision shape.
2. **Stays zero-inference?** Yes — provided you keep consistency upstream of
   `EmitOTelWait` and never teach the emitter to swallow invalid targets. Dropped
   waits fold into the waiter's real self-time (coarse, not inferred).
3. **Simple?** Moderate and localized to `dagql/cache.go` + one predicate hook;
   no emit-shape/loader/replay change.
4. **If clean skipping needed compromise — options?** It does not, *if* you take
   Cut 2 (static per-call predicate, stored on the work record, honored at all
   site-pairs and both sources). I'd reject Cut 1 (race-nondeterministic) and
   reject the `NoTelemetry`/`isMeta`/dedup rows (open subtrees → parent dangle).
   A heavier alternative — *collapse* each introspection subtree into one
   aggregate span instead of dropping it (keeps a wait target, still cuts volume)
   — is viable but a different, more complex emit shape; not worth it given Erik
   accepts losing the granularity.

**Recommendation: approve the direction; require the work-level stored-decision
implementation (Cut 2) covering singleflight + lazy + publishResult across native
and OTel; reject the verbatim per-caller phrasing. I'd take the implementation
task on these terms.**
