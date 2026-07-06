# Skip-introspection fix — council review (Chunk 2 implementer)

Verified against the **Chunk 4 worktree** (`wcprof-otel-implementer-chunk4-7ad02bcf-e44a5fe4`,
HEAD `4585bf413d`) — that is the branch carrying the full emit + the regression; my own
`/tmp/pubres-clean.jsonl` capture is used to ground the volume numbers. File:line below are that
worktree unless noted.

## Verdict (TL;DR)

- **(a) Correctness — NOT a free win as written.** The *idea* (skip the introspection subtree) is
  correct and the volume win is real. But the naive implementation the plan implies — *"skip by the
  call's telemetry-visibility, evaluated from the local `ctx` at each emit site"* — **re-creates the
  exact orphan / unresolved-wait-target gate failure on legitimate user traces.** The bulk of the
  suppressed volume is the **ctx-propagated `IsSkipped` descendant class**, and singleflight
  (`ConcurrencyKey == SessionID`, always set) lets a **non-skipped joiner dangle on a skipped
  executor's `call_exec`**. It is a free win **only** if the skip is decided **once on the shared
  work** and every dependent edge (waits, `publishResult`) is **slaved to that one decision**.
- **(b) Zero-inference — the SAFE cut yes; the NAIVE cut no.** The safe cut leaves loader/replay
  untouched and produces a self-consistent graph (no heuristic anywhere). The naive cut forces the
  forbidden choice: either the gate **refuses an otherwise-good user trace** (a dangling wait), or
  someone "repairs" the dangle in the loader (inference). So the naive cut **violates the principle**;
  the safe cut honors it.
- **(c) Goal — preserved.** User-work `dag.call` + `call_exec` + user↔user waits are untouched; only
  introspection goes absent (Erik is fine with that). **One accepted coarsening to call out
  explicitly:** user-work time spent *blocked on shared introspection work* loses its wait edge — not
  a wrong answer, just uncharacterized blocked time. Worth Erik's explicit OK.
- **(d) Simplicity — conceptually simple, mechanically MODERATE (not massive; no emit-shape/loader
  change).** A single shared-work skip flag plumbed through `ongoingCall` (and the lazy shared state),
  applied at **all** `OTelProfActive` emit sites, with waits slaved to the *target's* flag. The
  one-line "`OTelProfActive(ctx) && !IsSkipped(ctx)`" version is simple **and wrong**.

## Mechanism — verified, the diagnosis holds

- The emit gate is `OTelProfActive(ctx) = trace.SpanFromContext(ctx).IsRecording()` only
  (`dagql/otelprof_hooks.go:41-43`) — **no** `IsSkipped`/`NoTelemetry`/introspection check. The
  call_exec emit at `cache.go:3732-3733`, `publishResult` at `4017`, waits at `3958`, lazy op at
  `3017`, lazy waits at `2973/3103` all fire whenever a recording span is present.
- `core.AroundFunc` suppresses three different ways, with **three different keyings**
  (`core/telemetry.go`):
  - **introspection root** — `introspectionInfo(ctx, req.ResultCall)` → `WithSkip(ctx)` + `NoopDone`
    (`:35-37`). **Structural** (walks the call's receiver chain, `:354-452`) → uniform per callKey.
  - **introspection descendants** — `if dagql.IsSkipped(ctx) { return ctx, NoopDone }` (`:32-34`).
    **Context-propagated** (the root's `WithSkip` flows down `ctx`) → **NOT uniform per callKey**.
  - **meta / seen-key dedup** — `isMeta` (`:39`), `ShouldEmitTelemetry` (`:62`).
- The normal telemetry layer above it keys on `field.Spec.NoTelemetry` (`dagql/objects.go:655`) —
  structural/uniform, but it covers only fields *flagged* `NoTelemetry`, **not** the high-volume
  class.
- **The high-volume class is the ctx-propagated descendant class.** `Function.args`,
  `TypeDef.as*`, `Function.__withArg`, `ObjectTypeDef.__withFunction` are **not** structurally
  introspection (trace `introspectionInfo`: `args`/`as*` aren't in the `Function`/`TypeDef` lists at
  `:404-445`, no `__` prefix) — they are suppressed on main **only** because they are reached under an
  introspection root → `IsSkipped(ctx)`.
- **Grounding in my own capture** (`/tmp/pubres-clean.jsonl`, the same module-load workload):
  wcprof emit = **9438 of 10801 distinct spans (87%)** — 4779 `call_exec` + 4659 `publishResult` vs
  1306 normal (`NONE`-kind, carrying `dagger.io/dag.call`) + 57 lazy. The `call_exec` names are
  *exactly* the council's list (`Function.__withArg` 366, `ObjectTypeDef.__withFunction` 319,
  `Function.sourceModuleName` 296, `Query.sourceMap` 293, `Function.args` 287, `TypeDef.as*` …), and
  the normal user-work spans (`Query.moduleSource`, `ModuleSource.asModule`, `Container.withEnvVariable`,
  `Host.directory`, `Module.withObject`) are the untouched `dag.call` set. The diagnosis is correct.

## The acid test — self-consistency (THE hole), with file:line

**Parent edges are safe.** The introspection subtree is *closed*: `IsSkipped` short-circuits
**every** descendant (`telemetry.go:32`), so on main the whole subtree emits zero spans — there is no
non-skipped child of a skipped parent. If the wcprof emit skips by *not* calling `beginOTelCallExec`,
`callCtx` is simply left unreassigned (`cache.go:3733`) and any genuinely-non-skipped work re-homes up
to the nearest still-recording ancestor (a real emitted node). No orphaned parent. ✓

**Wait edges are the hole.** `c.wait` runs for **both** the executor (`cache.go:3785`, `joined=false`,
reason `call_exec`) and joiners (`:3703`, `joined=true`, reason `singleflight`), and both emit
`EmitOTelWait(ctx, oc.execSpanCtx, …)` at **`cache.go:3958`** — attached to the **waiter's** `ctx`,
targeting the **shared work's** `oc.execSpanCtx` (stashed by the executor at `:3758`). Now:

1. `ConcurrencyKey = clientMD.SessionID` is set for **every** call (`objects.go:600-604`), so within a
   session **any** callKey is singleflight-joinable (`:3694`).
2. Suppose introspection-context caller C1 claims X (a descendant call): `IsSkipped(C1.ctx)=true`. The
   naive cut skips X's `call_exec` at `:3732` → `oc.execSpanCtx` is the zero value (invalid).
3. Concurrently, **non-introspection** caller C2 requests the **same** callKey X →
   `IsSkipped(C2.ctx)=false` → joins (`:3703`) → `c.wait` → naive cut evaluates `IsSkipped(C2.ctx)=false`
   → **emits** the wait at `:3958` → target `oc.execSpanCtx` = **invalid** → unresolved-wait-target →
   **gate HARD-FAILS** (`engine/wcprof/wcotel/gate.go:118-120`) on an otherwise-good user trace.

That is precisely "skip by the caller's telemetry-visibility is the wrong cut." The executor and the
joiner evaluate `IsSkipped` on **different** ctxs and disagree, because `IsSkipped` is ctx-propagated,
not a property of the shared work. (The executor's *own* self-wait is fine — same ctx as its
call_exec decision — so the hole is specifically the cross-caller join.) The same hazard exists on the
lazy path (`:3017` producer vs `:3103` waiter).

## The cleanest emit-side option (recommended)

**Decide once on the shared work; slave every dependent edge to it. Zero new heuristics.**

- At the executor (`cache.go:3732`), compute the skip **once** from the executor's ctx +
  the call's structure: `skip := IsSkipped(callCtx) || introspectionInfo(req.ResultCall) ||
  isMeta(req.ResultCall)` (the descendant flag is already on `callCtx` because AroundFunc set
  `WithSkip` upstream — `telemetry.go:37` → flows to `:3732`). Store it on the `ongoingCall`
  (`oc.profSkip`), set under `callsMu` next to `oc.execSpanCtx` (`:3755-3759`).
- Gate `call_exec` (`:3733`) and `publishResult` (`:4017`) on `!skip`.
- Gate **every** wait (`:3958`, executor and joiner alike) on **`!oc.profSkip`** — i.e. on whether the
  **target** shared work was emitted, **never** on the waiter's own `IsSkipped(ctx)`. This is the
  invariant that makes it sound: *emit the wait iff the node it points at was emitted.* The skipped
  set then has **no incoming edge** by construction → self-consistent → gate passes → loader/replay
  see a clean graph and infer nothing.
- Mirror the same flag on the lazy shared state for `:3017/2973/3103`, and apply `!skip` at the other
  `OTelProfActive` choke points the comment lists (exec-split in `engine/engineutil`, service start in
  `core`). **Completeness matters: one missed site = one dangling edge = one gate refusal.**

Why not "emit iff a non-suppressed consumer observes the work"? That polarity needs to know future
joiners at mint time, which **conflicts with Invariant T** (the call_exec must be minted *before* the
`ongoingCalls` publish so joiners have a target — `:3727-3733/3755-3758`). Decide-at-mint +
slave-dependents is the clean cut that preserves Invariant T. Its only cost: the decision is
first-caller-dependent (a callKey reached by *both* introspection and user-work is profiled-or-not by
race) and a user-work joiner that inherits a "skip" loses its wait onto a now-absent node. Both are
**self-consistent and inference-free**; the lost wait is exactly the accepted coarsening in (c).

`oc.profSkip` must be **distinct** from "`execSpanCtx` invalid": the existing code *intentionally*
emits a dangling wait when the executor was *untraced* (mixed-recording) to surface it
(`otelprof_hooks.go:111-125`). Keep that — `profSkip` suppresses the wait for *deliberately* skipped
work; an invalid target that is *not* `profSkip` still surfaces. Clean separation.

## Reconciliation with the orphan saga — my position update (honest)

This diagnosis is **more fundamental than my earlier "capture-instrument tail-drain" framing**, and I
think it is the actual root cause I failed to pin. A `BatchSpanProcessor` is in use (engine
`session.go:2321`) with the SDK default bounded queue; the branch emits ~9438 wcprof spans (87% of the
stream) on top of normal telemetry. A burst past the queue → **silent, effectively-random enqueue
drops** — which reproduces every property I measured (bidirectional loss, batch-concentrated,
position varies by capture) **better** than "drain on CLI exit." So the orphans are very plausibly
**real, self-inflicted BSP drops from this over-emission**, not a capture artifact and not an emit
gap. Per the standing rule this is **not settled** — the decisive checks: (1) instrument BSP
`DroppedSpans` and show it's >0 pre-fix; (2) after the skip fix, recount orphans → expect **0** on
this workload. If both hold, the volume regression *is* the orphan cause and this fix closes it.

## Residual / caveats (none block the approach)

1. **Skip fixes the volume contract; it does NOT make the pipeline lossless.** A legitimate heavy
   *user-work* burst can still overflow the BSP queue → drop → orphans. The BSP-lossless/backpressure
   work remains the real correctness backstop, as the plan says. Skip removes the *dominant amplifier*
   and the volume-contract breach; it is necessary but not sufficient for drop-freedom.
2. **Normal cache-misses still carry `call_exec`+`publishResult` (≈3 spans where main emitted 1).**
   After skipping introspection, confirm the residual normal-miss doubling is within the volume
   contract (cache *hits* emit nothing new, so it may be small — but verify, don't assume "~baseline").
3. **Native wcprof (`wcprof.BeginOp`, gated on `wcprof.Enabled`, `:3720`) is opt-in and not part of
   the telemetry volume contract — leave it as-is.** Consequence: the cross-source oracle must compare
   on non-skipped classes only (native still profiles introspection; OTel won't). Accepted: OTel was
   never required to be native-parity.
4. **Goal coarsening (c) needs Erik's explicit nod:** dropping user-work waits *onto* skipped
   introspection loses attribution of user time blocked on schema/module-build. Inference-free and
   self-consistent, but it is a real (small) loss of "what was slow" for that specific blocked time.

**Bottom line:** approve the *direction*; reject the *naive per-ctx cut* (it re-creates the gate
failure on good traces — `cache.go:3732` vs `:3958` disagreement under `:3703` singleflight). Land it
only as **decide-once-on-shared-work + slave-all-dependent-edges (`oc.profSkip`)**, applied at every
`OTelProfActive` site. That is simple-enough, zero-inference, goal-preserving, and provably
self-consistent.
