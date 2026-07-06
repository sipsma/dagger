# wcprof × OTel — rewrite plan (in-place hard cut)

Status: **implementation plan, pre-implementation.** Review artifact. No code written yet.
Companion to `hack/designs/wcprof-otel-findings.md` (the diagnosis this plan fixes).

Mandate (Erik, via CoS): rewrite **in place** as a **hard cut**. Keep the trustworthy
foundation (native wcprof model + counterfactual replay, op-set generalization, ingest
plumbing, the correctly-windowed emit attrs/wait edges). Surgically and fundamentally fix
the design flaw + bugs. **Leave no crud** — the end-state must read as if designed correctly
from the start. Validation is first-class.

> **This revision reworks the design after the chunk-1 cycle finding** (see
> `wcprof-otel-findings.md` §RC-cycle). The earlier draft tried to keep a loader-side
> *synthesize-and-reparent* transform (`call_exec`, exec-phase) and "break" residual cycles.
> That was wrong on two counts Erik ruled disqualifying: (1) the synthesis **is loader-side
> causal inference** — exactly what the anti-inference invariant forbids; (2) "breaking and
> counting" cycles is **masking** a known falsehood. This rework removes both. The design
> below does **no loader synthesis and no cycle-masking**; cycles are impossible **by
> construction** (faithful emit + a timing-honest replay). It reads as the correct design,
> not a patch over the broken one.

Confidence tags: **[confirmed-code]**, **[confirmed-empirical]**, **[strong-hypothesis]**,
**[design]** (new assertion + rationale), **[verify]** (implementer must confirm).

---

## 0. What we keep, change, delete

**Keep (trustworthy):**
- `engine/wcprof/**` native recorder/model; `wcanalyze/graph.go` self-time accounting;
  `wcanalyze/replay.go` counterfactual **as the DAG engine** (modulo the §1.5 join-semantics
  fix); `wcanalyze/opset.go` op-set generalization.
- `wcotel` ingest (`ProfSpan`, `Dedup`, otlpdump + Cloud readers) and the `cmd/wcprof-analyze`
  Cloud path.
- Emit-side **attrs** (`kind/work/owner`, exec argv/exit, lock keys) and **wait-edge windows**
  (`wcprof.wait.*` carrying `[waitStart, waitEnd]` and, for singleflight, `[execStart, execEnd]`;
  lock events). These are correct and become *more* load-bearing (the replay now relies on the
  windows, not on span end-times).

**Change (the substance):**
- **Emit:** make span nesting *faithful* at the one place it isn't — lazy eval (§1.2). Ensure a
  singleflight wait truthfully identifies its joined execution (§1.6).
- **Replay (`replay.go`):** fix the join semantics so concurrent/mutual joins that reality
  resolved by timing stay acyclic — **honor the recorded wait window / recorded order; never
  recurse into a shared execution's full finish in a way that can cycle** (§1.5). This is the
  linchpin and is **shared with native**.
- **Report/validation:** assert acyclicity as a correctness signal; the cross-source oracle now
  also answers "does native cycle too?" (§4).

**Delete (hard cut — no vestigial code):**
- `resumedCallbackSpan` (`dagql/cache.go:2814-2826`) — the wrapper that redirects lazy eval
  nesting to the install span; once the resume span is the propagation parent it has no reason
  to exist (§1.2).
- **All loader synthesize-and-reparent inference:** `synthesizeCallExec` and its
  interval-containment child reparenting; the `synthesizeExecPhases` child-op synthesis +
  reparenting; `breakWaitCycles` and the `DroppedCycleWaits` masking machinery
  (`loadotel_synth.go`, the chunk-1 additions to `graph.go`/`loadotel.go`). These invent nodes
  and reparent real spans by guessing — the anti-inference breach. See §5 for the chunk-1 fate.

---

## 1. The core design

### 1.1 The principle (one rule, no inference, timing-honest)

The replay treats a `parentId` nesting edge as a **synchronous join** (the parent was blocked
inside the child). That is valid for native (built by inline `BeginOp` on the live call stack)
and the goal is to make it valid for OTel too — **by ensuring the emitted nesting is faithful,
not by reconstructing structure on the analysis side.**

> **Runtime IR = the trace's EXPLICIT structure only.** Ops come from emitted spans; the
> structural parent of a work span is the op that *synchronously produced it* (the engine must
> emit it that way); cross-consumer dependencies are the emitted `wcprof.wait.*` windowed edges.
> The loader does **no node synthesis and no reparenting-by-inference.** Where the engine's
> nesting is *unfaithful*, fix it at **emit** (the representing span becomes the propagation
> parent of its work) — never guess it back at load. The replay computes makespan from this
> explicit structure, **honoring the recorded wait windows and recorded event order**, so that
> joins reality resolved by timing cannot become impossible cycles in the model.

Two corollaries that drive the rest:

- **No masking.** A wait back-edge (the rare mutual near-instant join) is **resolved truthfully by
  its recorded wait window** — the replay always terminates with each back-edge contributing the
  ~0 it actually blocked — **never** by dropping edges or assuming arbitrary durations. Validation
  **asserts** that short-circuited back-edges are **degenerate (near-zero)** and **fails loudly** on
  any non-degenerate one (a large back-edge window would signal real under-coupling to investigate).
  The diagnostic reports what was short-circuited; it never hides it.
- **Correctness is a property of self-time + recorded timing, not raw interval containment.** A
  detached op (a lazy resume) may outlive its structural parent; that is harmless **iff** its
  self-time is glue, its children carry the real work, and consumers reach it via windowed wait
  edges — exactly as native tolerates. Parent-interval-violation is a *diagnostic that must
  collapse for synchronous children*, not a blanket law.

**The unifying form:** every execution / deferred-work node is a **real, emitted, bounded span
that is the propagation parent of its work** — exactly as native records `OpKindLazy` /
`OpKindCallExec` / exec phases inline. The loader does **zero *inference*** — no node synthesis,
and no reparenting that *guesses* a causal relationship (the deleted `synthesizeCallExec` was both:
it invented `call_exec` nodes and reparented real spans by interval-containment guessing). The
loader is otherwise a plain spans→ops→waits pass, with **one** permitted non-trivial step:
**availability elision** (§1.4a) — removing ops the engine *explicitly classified* `work=availability`
/ `service_lifetime` and reconnecting their real children to the surviving tree. That is **not
inference** (it's tag-driven, removes-and-heals deterministically, only ever moves a child *up* its
own ancestor chain, and restores native's IR — native has no daemon work-op); it provably cannot
manufacture the cross-instance cycle that interval-containment synthesis did. Per choke point:

| Choke point | Execution node (a real emitted span; work nests under it) | What we do |
|---|---|---|
| **Lazy eval** | the `resume <field>` span | **emit fix** (§1.2): make the resume span the propagation parent of its work (today work mis-nests under the install span) |
| **Singleflight call** | a bounded, hidden **`call_exec` span** | **emit it** (§1.3): native-faithful twin of `OpKindCallExec`; the resolver `fn` runs under it so its work nests there; bounded to `[execStart,execEnd]` and created on the execution's `WithoutCancel` context → it survives first-caller cancellation. **Delete all loader synthesis.** |
| **Container exec** | the **exec span** (R-C propagation parent; in-container work already nests there) | **drop the synthesized phase ops** (§1.4): no reparenting, no double-count; attribute self-time to work-types by the explicit `Container started/exited` events; phase-level what-if deferred |
| **Service start / lifetime** | the bounded **`service_start` span** (real emitted, twin of `OpKindServiceStart`) is the work node; the long-lived **`service_lifetime`** span is *availability*, not work | **keep `service_start` as-is** (already an emitted bounded propagation-parent span — consistent with the principle, no change); **elide `service_lifetime` + `work=availability` ops** by the tag-driven pass (§1.4a) |

### 1.2 Lazy resume span — faithful emit (make it the propagation parent of its work) · [confirmed-code] · EMIT

**Root:** during lazy eval the callback runs under `callbackCtx`, whose active span is
`resumedCallbackSpan{sc: originalSpanCtx}` returning the **install span** (`dagql/cache.go:2814,2820`).
So eval work (execs, sub-calls) nests under the install span, not the `resume <field>` span
(`:3008`). The resume span is near-childless → self-time = full duration (the 62.81s phantom),
and the same wall-clock is double-counted (once as the resume span's "engine self-time", once as
the real subtree under the install span). This is the prime driver of the +65%.

**Fix:** create the resume span **as a child of the install span** and make it the
**propagation parent** of the eval work. It's already `telemetry.Passthrough()` (the UI skips it
and renders children at its parent's level — `otel-go/span.go:46`), so parenting it under the
install span keeps the **UI byte-identical** while the analyzer gets a correct bounded lazy op
(self = glue, children = the real work) — the OTel twin of native `OpKindLazy`.

```go
// Parent the resume span under the install span; with Passthrough the UI still renders
// eval work under the installing call, while the analyzer sees the resume span as the
// bounded structural parent of the real work (self = deferred-eval glue, children = work).
installCtx := trace.ContextWithSpanContext(evalCtx, originalSpanCtx)
resumeCtx, resumeSpan = Tracer(installCtx).Start(installCtx, spanName,
    trace.WithLinks(links...),                 // cause links → install/owner spans (failure attribution, unchanged)
    trace.WithAttributes( /* kind=lazy, work=engine, owner=engine */ ),
    telemetry.Passthrough())
// Resume span is the propagation parent of eval work. The install span is preserved as the
// explicit log/error target so a lazy-triggered exec's stdout/stderr (SpanStdio) and
// error-origin still attribute to the installing call.
callbackCtx = telemetryattrs.ContextWithLogTarget(resumeCtx, originalSpanCtx)
```
Then **delete** `resumedCallbackSpan`.

**MANDATORY — preserve the install span as log/error target.** [confirmed-code] Deleting the
wrapper changes the *active span* during eval from install → resume, and product-visible paths
derive their span from the ambient active span: a lazy-triggered exec captures
`causeCtx := trace.SpanContextFromContext(ctx)` (`core/container_exec.go:1311`), used by the
executor as `logTarget` for `SpanStdio` (`executor_spec.go:759`); exec error-origin tracks the
same (`core/exec_error.go:66-68`). Lazy→exec is the *hot* path. So the install span is preserved
explicitly via a context value:
- New helper `telemetryattrs.ContextWithLogTarget(ctx, sc)` / `LogTargetFromContext(ctx)` (falls
  back to `trace.SpanContextFromContext` when unset → non-lazy callers unchanged); read at
  `container_exec.go:1311` and the `exec_error.go:68` fallback. This is the executor's existing
  `logTarget`/`propagationParent` split (`executor_spec.go:116-122`) generalized to the context
  level: nesting → resume, log/error → install.
- Validation adds a before/after capture asserting lazy-triggered exec stdout/stderr + error
  still land on the installing call.

Failure attribution is unaffected (rides the resume span's cause links `:3006` + `DagBlockedAttr`
`:3047`, independent of the active span). The lazy wait edges (`emitLazyWaitEdge` `:3107`) are
correct and unchanged.

### 1.3 Singleflight — emit a bounded hidden `call_exec` span; delete all loader synthesis · [confirmed-code] · EMIT (+ LOADER deletion)

**The shape today.** The shared resolver runs as `fn(oc.sharedWorkCtx)` (`cache.go:3757`); its
active OTel span is `oc.execSpanCtx = trace.SpanContextFromContext(oc.sharedWorkCtx)` = the first
caller's **call span** (`:3749`). So the resolver work nests under the *first caller's call span*,
and joiners emit a singleflight wait edge → that span (carrying `[waitStart,waitEnd]` + the shared
`[execStart,execEnd]`). The earlier draft therefore *synthesized* a bounded `call_exec` node and
*reparented* the call span's in-window children into it — because the call span does **not** bound
the execution (caller cleanup makes it **overhang**; under `context.WithoutCancel` (`:3717`) the
first caller can stop waiting (`:3958`) and **end its span before `execEnd`** while the execution
continues for joiners). That synthesis is the **anti-inference breach** (invents a node, guesses
children by interval containment) and manufactured the cycle (§RC-cycle).

**Why "just use the call span as the execution node" is also unsound.** [review — Codex] Tempting
to keep the call span as the execution node (work already nests there) and skip synthesis. But
under early cancellation the call span ends before `execEnd`, so the post-cancel resolver work is
**orphaned**: it nests under an already-ended span, falls *outside* that span's interval, and the
replay never folds it into the span's finish (a parent only joins children that ended by its own
end). The execution node would then knowingly fail to bound its own execution — a falsehood, not
just an imprecision. Using the emitted `[execStart,execEnd]` for the *join gate* releases the
joiner at the right time but does **not** make that orphaned work visible/attributed. So the
call span cannot faithfully be the execution node.

**Decision — emit a bounded, hidden `call_exec` span (native-faithful), delete all loader
synthesis.** [design] At the execution site (the goroutine that runs `fn`, on the `WithoutCancel`
context — `dagql/cache.go` ~3750), open a span `call_exec` (`telemetry.Internal()` → hidden in
UI/Cloud, still exported), `kind=call_exec`, ended when `fn` returns (`= execEnd`); run `fn` under
it so the resolver work nests under it. Per-caller `call` spans stay as the caller frame
(self = glue); first caller + joiners wait on the `call_exec` span (native: `WaitReasonCallExec` /
`WaitReasonSingleflight`). This is the exact twin of native's `OpKindCallExec` (`cache.go:3722`,
which already creates the native op at this site).
- **Bounded + cancellation-correct:** the span lives `[execStart,execEnd]` on the `WithoutCancel`
  context, so it survives first-caller cancellation and faithfully bounds the full execution; no
  orphaned work, no overhang. The join gate (`replay.go:152`) now sees a target whose `End` *is*
  `execEnd`.
- **Log/error target preserved (same as lazy — [review — Codex]):** making `call_exec` the
  propagation parent changes the *ambient* span seen by resolver work, so a singleflight-executed
  resolver that triggers an exec would route its stdout/stderr (`SpanStdio`) + error-origin to the
  hidden `call_exec` (`telemetry.Internal()` is only a render-hint attr, not a log-target). Fix: when
  opening `call_exec`, **capture the prior `LogTargetFromContext(sharedWorkCtx)` (= the call span)
  first**, then run `fn` under `ContextWithLogTarget(callExecCtx, priorLogTarget)` — propagation
  parent = `call_exec`, log/error target = the call span (unchanged behavior). This reuses the §1.2
  helper; it preserves any already-set lazy install-span target too.
- **Loader deletes ALL synthesis:** no `synthesizeCallExec`, no `ceKey`, no reparenting, no
  retargeting. The joiner wait edge targets the real `call_exec` span directly. The redundant
  `[execStart,execEnd]` link attrs (the span's own bounds now carry that) can be dropped from the
  emit. **Net loader change: a large deletion.**
- **Oracle parity:** native and OTel now have the *same* `call`+`call_exec` structure, so the
  cross-source oracle (§4.3) compares like-for-like.

**Volume — stated honestly, flagged for Erik.** This adds **+1 hidden span per cache-miss
execution**. On `defb` (raw ≈ 8378 spans; **2408** `call` spans, of which the executed/miss
fraction is large in a cold-ish build) that is roughly **+1.5–2.4k spans (~+18–29%)** on *every*
trace, including user-facing Cloud traces — but it *also deletes* the ~112 synthesized `call_exec`
+ 450 synthesized phase ops. This is the price of a sound, native-faithful, inference-free
foundation; it is exactly the soundness-over-volume trade Erik's ruling favors (the original sin
was using volume to justify *inference*). Flagged in §6 as a product/volume decision. (A
lower-volume variant — emit `call_exec` only when telemetry is on AND the call actually executes,
which is already the only site reached — is the default; we do **not** emit it for cache hits.)

### 1.4 Container exec — exec op + event-segment work-type; no phase synthesis · [design] · LOADER (deletion)

**Why no synthesis.** The exec span is the R-C propagation parent; in-container nested-client work
(module runtimes) already nests under it faithfully. The earlier draft synthesized
`containerStart`/`processRun` child ops from the `Container started/exited` events **and reparented
in-window children** under `processRun` — the same interval-containment inference, and the source
of the processRun double-count (a childless `processRun` leaf duplicating the nested subtree).

**Decision — drop the synthesized phase ops and the reparenting.** The exec op itself is the
execution node (work nested for module execs → self = glue; no children for a plain `go build` →
self = the process time, exactly the user-facing answer). This removes the double-count and the
inference. **Scope of what replaces the phase split** [review — Codex; be explicit]:
- **Attribution (in scope):** attribute the exec op's self-*segments* to work-types using the
  explicit `Container started`/`Container exited` timestamps as boundaries (`[exec.start,started]`
  = engine setup, `[started,exited]`−children = user_process, `[exited,exec.end]` = engine
  cleanup). This is a **report/breakdown** change only: `SelfSegments` carry a per-segment
  work-type, and `writeWorkBreakdown` sums by segment work-type. From explicit events, not
  inference. Fixes the "containerStart counted as user_process" mislabel in the breakdown.
- **Phase-level what-if (DEFERRED, explicitly):** the counterfactual still scales whole **ops**,
  so there is **no** separate "scale containerStart vs processRun" what-if (the old synthesized
  phase ops gave that). For v1 this is an accepted limitation: the user-facing answer ("the exec
  took N s") and the work-type breakdown are correct; phase-granular what-if is not offered. If it
  proves needed, the native-faithful path is to **emit** real phase spans (containerStart /
  processRun as propagation parents, like §1.3's `call_exec`) — never to re-synthesize/reparent.
- `[verify]` started/exited present + ordered; absent ⇒ classify the whole exec by its op-level
  work-type (no segmentation), no crash.

### 1.4a Service: bounded `service_start` emit (consistent) + tag-driven availability elision (the one permitted loader pass) · [confirmed-code] · EMIT (no change) + LOADER (justified)

A service has two emitted spans (`core/services.go`, `core/service.go`): a **bounded
`service_start`** span (`kind=service_start`, `telemetry.Internal()`, ends at readiness — waiters
target it because it is bounded) and the long-lived service exec span tagged
**`kind=service_lifetime`/`work=availability`** (the daemon, ending at teardown). Today the lifetime
exec nests *under* `service_start`, and readiness work (e.g. the healthcheck) nests under the
lifetime exec; `elideAvailability` (`loadotel.go:169`) removes the availability ops and reparents
their real children up to the nearest non-availability ancestor (= `service_start`).

**`service_start` emit is already consistent with the principle** — it is a real emitted bounded
propagation-parent span, the OTel twin of native `OpKindServiceStart`. No change needed; it is the
service's work node, and waiters' wait edges target it (bounded → a valid join target).

**Why `service_lifetime` must be elided (not imported as work):** a daemon's lifetime is
*availability, not work* — its wall-time is mostly idle-waiting-to-be-stopped and its end is gated
by teardown (everything finishing), not its own CPU. Importing it as a scheduled op would corrupt
self-time and **fool the counterfactual** (the daemon is often the last span to end → falsely looks
makespan-determining). Native has **no daemon work-op** (only the bounded `service_start`); eliding
it **restores native's IR**.

**Why this elision is NOT the deleted inference, and IS permitted** [resolves the lead's gap]:
- **Tag-driven, not guessed:** it keys off the engine's *explicit* `work=availability` /
  `service_lifetime` classification — never timestamps/interval containment. It does not *choose*
  which children belong where (the `synthesizeCallExec` sin); it removes explicitly-classified
  non-work nodes and reconnects their children to the surviving ancestor.
- **Up-tree only, provably acyclic:** a child is reparented only to one of its own ancestors, so no
  cross-tree edge is created → it cannot manufacture the cross-instance cycle synthesis did.
- **Semantically sound for the readiness case (the common one):** the trapped real child is
  readiness work (healthcheck/container-setup) whose true work-home *is* `service_start` (its
  `[start, readiness]` interval contains that work), so reparenting it onto `service_start` is the
  correct home, not a false join.
- **Post-readiness / nested non-availability descendants (rare):** reparented up they land as a
  *detached* child of `service_start` (they end after it → not implicit-joined; the §1.0 corollary
  makes detached children harmless, not ranked — correct for daemon-runtime work), and the
  `ParentIntervalViolations` diagnostic (§3.5) surfaces any non-degenerate case rather than hiding
  it. We never silently fabricate a join.

So availability elision stays, **explicitly carved out** as the one permitted deterministic
tag-driven loader pass (§1.1) — distinct in kind from the deleted interval-inference synthesis.
(A *fully* emit-faithful variant with **no** loader reparent is possible later — emit the readiness
work directly under `service_start` and the daemon as a self-contained availability subtree — but
it depends on the **deferred** exec-phase split (§1.4), since container-setup is currently part of
the one service exec span. Deferred consistently with §1.4; not needed for v1.)

### 1.5 The replay join semantics — honor recorded timing; back-edges resolved truthfully (never masked) · [design] · THE LINCHPIN (shared with native)

This is the deepest change and the one that actually removes the cycle. **It is a replay-model
fix shared with native** (native has the same op/wait structure and the same recursive-finish
replay).

**The cycle, precisely** (`wcprof-otel-findings.md` §RC-cycle): two `Query.moduleSource`
evaluations run concurrently; each, mid-resolution, makes a re-entrant `moduleSource`-keyed call
that **singleflight-joins the other in-flight execution**; both joins are **near-instant** (~0
duration, same instant ~24.74s — the joiner caught the other execution right as it committed).
Reality is acyclic (it finished). But the replay combines (a) the **implicit join** (a parent is
modeled as synchronously waiting for everything nested under it) with (b) **`actWaitJoin` pinning
the joiner to the target's full recursive simulated finish** (`replay.go:~406`,
`clock = max(clock, finish(target))`), **discarding the recorded wait window**. Two mutual
near-instant joins then assert `A ≥ finish(C) ∧ C ≥ finish(A)` → the recursive `finish()` re-enters
an in-flight op → "cycle." This is the **same disease as the lazy bug**: temporal adjacency treated
as hard synchronous causality, with the timing that made reality acyclic thrown away. The
synthesis (§1.3) merely manufactured an equivalent cycle in OTel; **deleting it does not remove the
cycle** (the mutual structure persists with real nodes too — and native would exhibit it), so the
fix must be here.

**Decision (principle, firmly): keep the recursive full-finish join for the acyclic majority
(exact coupling); resolve only the rare re-entrant/mutual join — the cycle back-edge — using the
recorded wait window (its truthful, ~0 contribution). No edge-dropping, no arbitrary-duration
masking.**

- With bounded execution nodes (§1.3 `call_exec`, §1.2 resume span, the exec span), a join's
  target is bounded to its execution, so for the **DAG majority** the join releases the waiter at
  the target's **simulated finish** — the existing `actWaitJoin` recursion, which gives exact
  multi-hop counterfactual coupling (scale a deep op → its execution's finish moves → its waiters
  release earlier). This is *kept*. The bounded target's `End` is the release point, so **no new
  IR field is needed** (this is why §1.3's bounded emit matters — it removes the unbounded-target
  problem that would otherwise force a separate release-window field).
- The **only** change is the cycle case: when the recursive `finish()` re-enters an op that is
  already in-flight (a mutual/back-edge — the two concurrent near-instant singleflight joins), do
  **not** recurse (that's the cycle) and do **not** "assume original duration" (the current
  `replay.go:367-371` masking, which ignores the counterfactual). Instead, the back-edge
  contributes the **joiner's recorded wait window** `[waitStart, waitEnd]` — the ground truth of
  how long it actually blocked (~0 for the near-instant mutual joins). This is acyclic by
  construction (the back-edge never recurses) and truthful (uses the recorded reality that made
  the schedule acyclic), not masking. (Implementation note: the compiled join action must now
  **carry the wait window** — `replay.go:149/405` currently keep only the target ref — so the
  short-circuit can return `waitEnd − waitStart`.)
- **Why this is acyclic + coupling-preserving:** forward (non-cyclic) joins keep full-finish
  recursion → exact coupling on the 99.9% DAG part. Only the back-edge of a cycle is short-
  circuited via its recorded window. On `defb` the cycles are 8/8940, all ~0 duration, so the
  short-circuit changes ~nothing quantitatively while making the graph provably acyclic.

**Tradeoff + the back-edge diagnostic (`cycles==0` is not enough — [review — Codex]).** The
back-edge's contribution is its *recorded* window, so scaling the back-edge's target does not
propagate through that one edge (the forward direction still does). For the near-instant mutual
joins this is exactly right (~0 either way). If a *non-degenerate* cycle ever appears (a back-edge
with a large recorded window), the short-circuit would under-couple it — so the replay must surface
a **`BackedgeShortCircuits` diagnostic** (count + max + total recorded wait duration). The invariant
(§4) asserts back-edges are **near-zero-duration** for the known mutual-join case and **fails loudly
on any non-degenerate back-edge** — this is the truthful replacement for the deleted
`DroppedCycleWaits` masking-count (it reports what was short-circuited and how big, never hides it).
**The exact realization** (cycle-aware `finish()` short-circuit vs a time-ordered DES) is settled in
the implementation chunk, gated by the validation below; it must not regress DAG-case rankings.

**Native parity — must be answered, not assumed.** [strong-hypothesis] Native shares the replay
and has the same `OpKindCallExec` + `WaitReasonSingleflight` + implicit-join structure, so a
re-entrant-concurrent-module workload should produce the **identical** cycle in native. The
cross-source oracle (§4.3) on such a workload is the **arbiter**: it confirms native cycles too
(→ this is a shared-model fix, correct to make for both) and that the §1.5 fix yields acyclic,
coupling-preserving, *matching* rankings on both sources.

### 1.6 Suppression — a singleflight wait must truthfully identify its joiner · [confirmed-code] · EMIT

`ShouldEmitTelemetry` suppresses a repeated call: `AroundFunc` returns `NoopDone` with **no span**
(`core/telemetry.go:63`), so during that call's resolution the ambient active span stays the
*parent*. A suppressed re-entrant `moduleSource` call that joins another execution therefore emits
its singleflight wait edge (`AddSingleflightWaitEdge(ctx, oc.execSpanCtx, …)`, `cache.go:3950`)
attached to its **parent** (e.g. a `withName` span) — the semantically impossible "withName joins
moduleSource" edge that helped tangle the cycle. This is the RC5 "suppression hole" earlier filed
as *minor*; it is **load-bearing** (it mis-attributes causal edges).

**Mechanism — must be implementable; "don't suppress at `AroundFunc`" is not.** [review — Codex]
`AroundFunc` decides suppression *before* the cache knows whether this call will become a singleflight
joiner (join-ness is discovered later, in `wait(... joined)`), so we cannot selectively un-suppress
joiners there. Two implementable options; the plan picks the first:
- **(chosen) Wrap the join in a minimal join-identity span that COVERS the wait window.** [review —
  Codex] The span must span `[joinStart, unblock]` to be a structurally-correct waiter (a waiter op
  whose span starts *after* `waitStart` is wrong for self-time/replay). So open it **before** the
  blocking `select` (`cache.go:~3931/3935`, where `joinStartUnixNano` is captured), end it after the
  wait completes, and attach the singleflight wait edge to it — only when the joiner's own call span
  was **suppressed**. To know that reliably, `AroundFunc` sets a **per-call "has-own-span" marker on
  BOTH paths** — emitted ⇒ `true`, suppressed ⇒ explicitly `false` — in the context it returns for
  that call. **This is mandatory** [review — Codex]: the suppressed path currently returns the input
  context *unchanged* (`core/telemetry.go:63`), so a suppressed re-entrant call would otherwise
  *inherit* its parent's `true` marker (Go context inheritance) and `wait()` would wrongly think the
  joiner has its own span — recreating the mis-attribution. Each `AroundFunc` overrides the marker for
  its own call, so inherited parent state never counts. (Equivalent: store a unique per-call token and
  compare identity; the boolean-both-paths form is simpler.) The span carries the joiner's recipe
  digest → the wait truthfully reads "this `moduleSource` joiner waited on that `moduleSource`
  execution." Volume is bounded — only **suppressed-call collisions**, a small fraction of repeats.
- (alternative) Carry the joiner's identity on the wait-edge attrs and have the loader attribute the
  wait to an identity-labeled node; rejected as more loader logic for the same effect.

With §1.5 the *cycle* harm is already neutralized (near-instant joins contribute ~0); this fix
restores **attribution accuracy** (the join is credited to the real joiner — correct
work-type/identity breakdown) and removes the semantically impossible edge from the graph.
`[verify]` the cleanest join-site span point in `cache.go wait()`.

### 1.7 Why this is correct (and matches native), and why it's simpler

After §1.2–1.6 the IR is built from explicit structure only, and every choke point is a real
emitted bounded propagation-parent span matching native:
- **lazy:** bounded `resume` span (emit-faithful parent of its work) — twin of `OpKindLazy`.
- **singleflight:** bounded hidden `call_exec` span (resolver work nested) + per-caller `call`
  frames waiting on it — twin of `OpKindCallExec` + waits.
- **exec:** the exec op (work nested), work-type-segmented breakdown from explicit events — twin of
  exec + phases (phase-granular what-if deferred).
- **service:** bounded `service_start` (emitted, work node) + `service_lifetime`/availability elided
  by the one tag-driven loader pass (§1.4a) — twin of native `OpKindServiceStart` + no daemon work-op.
- Cross-consumer deps are explicit windowed `wcprof.wait.*` edges; the replay releases at the
  bounded target's finish and short-circuits only the rare cycle back-edge via its recorded window.
  **No loader *inference* (no node synthesis, no guessed reparenting), no masking** — the sole loader
  reparent is the tag-driven availability elision (§1.4a), explicitly carved out.

It is **simpler** than the rejected draft: the **loader becomes a spans→ops→waits pass plus one
deterministic tag-driven step — availability elision (§1.4a)** (deletes `synthesizeCallExec`, the
reparenting, `breakWaitCycles`, ~562 synthesized ops on `defb`); the only additions are two small
*emit* spans (the `call_exec` execution node and the join-identity span) that make nesting faithful
at the source. One rule — every execution node is a real emitted bounded span; the loader does no
inference (the availability elision is explicit-tag-driven, not guessed) — covers all four choke
points. The +65% collapses (lazy double-count
gone via §1.2; the cycle gone via §1.5), and the "huge ops save 0ns" inversion resolves (real work
is back on the counterfactual's critical path). Verified by §4, not asserted. **Cost:** the
`call_exec` emit adds ~+18–29% spans (§1.3) — the one real tradeoff, flagged for Erik (§6).

---

## 2. The coverage gap — honest confrontation (NOT a blocker) · [confirmed-empirical]

~46% of `defb` ops are un-augmented buildkit/IO (`HTTP GET`, `fetching`, `copy`, `pulling`,
`resolving`, `git`, plus 2924 `POST /query`): no `kind`, no work-type, no wait edges. **Does this
undermine "why was my CI run slow?" Conclusion: no.**

1. **The un-augmented spans are overwhelmingly synchronous leaf I/O or synchronous containers**,
   for which nesting + implicit-join is *already correct*: a buildkit solve/pull/fetch is leaf work
   the engine blocks on (self = its duration; the parent correctly joins it); a `POST /query` is a
   nested client synchronously blocked on the engine. Leaf work needs no outgoing wait edge.
2. **The empirics rule it out as the driver.** `303c` is **66%** un-augmented yet drifts only
   **+5.9%** with **0 cycles**; `defb`'s +65% correlates with **lazy/singleflight depth**, not the
   un-augmented fraction (which is *lower* proportionally than 303c).
3. **Residual risks are second-order and bounded:** (a) buildkit-internal dedup is invisible →
   *under*-count of a shared vertex, harmless to "what dominated"; (b) `kind=unknown` ops are
   missing only from the *work-type breakdown*, not from makespan/counterfactual ranking.

**What the plan does (proportionate):**
- **Does not gate on it / does not escalate.** A dominant `go build` (exec self-time) or image pull
  (IO leaf) both rank by self-time with correct join behavior.
- **Optional `--classify-unknown`** name table (`HTTP*`/`fetching*`/`pulling*` → `external`) for the
  *breakdown only* — labeling, never causal inference; `kind` stays `unknown`. Cosmetic.
- **Validation polices it:** the cross-source oracle (§4.3) surfaces any real drift contributed by
  un-augmented nesting; *that* would be the trigger to augment a specific buildkit choke point.
- **Cloud fidelity blind spot, honest:** `cloudSpansToProfSpans` (`cloud.go:89`) leaves dropped
  attr/link/event counts zero (the Cloud GraphQL fragment doesn't expose them), so that diagnostic
  is otlpdump-only; Cloud's safety net is the raised 8192 caps + the structural/drift gates.
  Exposing dropped counts in the Cloud API is a worthwhile out-of-scope follow-up.

If review disagrees the gap is non-fatal, this is the one place to escalate to Erik. My assessment,
with the 303c evidence, is that it is not.

---

## 3. File-by-file

### 3.1 `dagql/cache.go` — EMIT (call_exec span + lazy + suppression)
- **§1.3 `call_exec` span:** at the execution goroutine (~`:3750`, on the `WithoutCancel` ctx,
  alongside the existing native `wcprof.BeginOp(OpKindCallExec)` at `:3722`), open a hidden
  (`telemetry.Internal()`) `kind=call_exec` span; **capture `LogTargetFromContext(sharedWorkCtx)`
  first**, then run `fn` under `ContextWithLogTarget(callExecCtx, priorLogTarget)` so resolver work
  nests under `call_exec` while exec stdout/stderr + error-origin still target the call span (the
  same split as lazy); end the span when `fn` returns; set `oc.execSpanCtx` to it. First-caller +
  joiner wait edges target this span. Drop the now-redundant `[execStart,execEnd]` attrs from
  `AddSingleflightWaitEdge` (the span's own bounds carry them); keep `[waitStart,waitEnd]`.
- **§1.2 lazy:** rewrite the resume-span creation — parent under `originalSpanCtx`, make the resume
  span the propagation parent, set the log target
  (`callbackCtx = telemetryattrs.ContextWithLogTarget(resumeCtx, originalSpanCtx)`). **Delete**
  `resumedCallbackSpan` (`:2814-2826`).
- **§1.6 suppression:** `AroundFunc` sets a per-call "has-own-span" marker on **both** paths
  (emitted ⇒ `true`; suppressed at `:63` ⇒ explicitly `false`, overriding the inherited parent
  marker — see §1.6). In `wait()`, if the marker is `false`, open a minimal `kind=call` join-identity
  span **before the blocking `select` (`:~3931`)** spanning `[joinStart, unblock]`, carrying the
  joiner's digest, end it after the wait completes, and attach the singleflight wait edge to it.

### 3.1a `engine/telemetryattrs/wcprof_emit.go` — log-target context value
- Add `ContextWithLogTarget(ctx, trace.SpanContext) context.Context` / `LogTargetFromContext(ctx) trace.SpanContext`
  (falls back to `trace.SpanContextFromContext` when unset). Low-level pkg already imported by
  `core` and `dagql`; context-level generalization of the executor's logTarget/propagationParent
  split.

### 3.1b `core/container_exec.go` + `core/exec_error.go` — read the log target
- `container_exec.go:1311`: `causeCtx := telemetryattrs.LogTargetFromContext(ctx)`.
- `exec_error.go:68`: replace only the *fallback* (`spanCtx = trace.SpanContextFromContext(ctx)` inside
  `if !spanCtx.IsValid()`) with `LogTargetFromContext(ctx)`, preserving the `origin`-first precedence.
- Non-lazy callers unchanged (helper falls back to the ambient span).

### 3.2 `engine/wcprof/wcanalyze/replay.go` — the join-semantics fix (§1.5) · the core change
- **Keep** `actWaitJoin`'s recursive full-finish for the acyclic majority (exact coupling against
  the now-**bounded** target — §1.3/§1.2/§1.4 make all execution nodes bounded, so the target's
  `End` is the release point; no new IR field).
- **Change only the cycle case:** replace the `inFlight` "assume original duration" break
  (`:367-371`, masking) with a truthful short-circuit — when `finish()` re-enters an in-flight op
  via a wait back-edge, that edge contributes the **joiner's recorded wait window**
  `[waitStart,waitEnd]` (~0 for the near-instant mutual joins) instead of recursing. Acyclic by
  construction, no masking. The compiled join action must **carry the wait window** (today it keeps
  only the target ref — `:149/:405`) so the short-circuit can return `waitEnd − waitStart`. The
  short-circuit applies **only on the wait-join re-entry path**, never to implicit child joins (the
  nesting tree is a DAG — only wait edges can form a re-entry).
- **Surface a `BackedgeShortCircuits` diagnostic** (count + max + total recorded wait duration) — the
  truthful replacement for the deleted `DroppedCycleWaits`; it reports what was short-circuited, not
  hides it.
- Exact realization (cycle-aware `finish()` short-circuit vs a time-ordered DES) decided here under
  the §4 gates; must preserve DAG-case bottleneck rankings. This is the implementation chunk's
  primary risk surface — budget for iteration + the oracle.

### 3.3 `engine/wcprof/wcanalyze/loadotel_synth.go` — DELETE the inference
- **Delete** `synthesizeCallExec` (+ `ceKey`, the joiner guards, `reparentContained`) and the
  child-reparenting in `synthesizeExecPhases`. Replace `synthesizeExecPhases` with event-boundary
  **work-type segmentation** of the exec op's self-time (§1.4) — no child ops, no reparenting. If
  this leaves the file with only the segmentation helper, fold it into `graph.go`/`loadotel.go` and
  delete the file. No vestige of the synthesis approach remains.

### 3.4 `engine/wcprof/wcanalyze/loadotel.go` — no synthesis, no cycle-breaking; keep availability elision
- Remove the `synthesizeCallExec`/`synthesizeExecPhases`-reparent calls and the `breakWaitCycles`
  call. The loader becomes: spans → ops (parentId nesting) → **`elideAvailability()`** → wait edges
  (windowed) → `finalize()`.
- **Ordering [review — Codex]: `elideAvailability()` runs BEFORE attaching wait edges** (matching the
  current safe order — `loadotel.go:83` precedes the wait pass at `:91`). If it ran *after*, a
  surviving wait could hold a pointer to a now-deleted availability op, and `compileProgram`'s
  `idxByID[target.ID]` lookup (no `ok` check, `replay.go:154`) would silently map the dangling target
  to op index 0. Eliding first means a wait to an elided op resolves to `Target=nil` (handled as a
  fixed/orphan wait), never a dangling index-0.
- **Keep `elideAvailability` (§1.4a)** — the one permitted deterministic tag-driven pass (it is
  *not* the deleted inference; see §1.1). Its doc comment must be updated to drop the stale
  "chunk-5 emission invariant" reference and state the §1.4a justification (tag-driven, up-only,
  restores native IR). It is **not** in the §3.9 deletions.
- The `call_exec` is now a **real emitted span** (§1.3) → it loads as an ordinary op; the joiner
  wait edge targets it directly (no retarget). No `[execStart,execEnd]` synthesis input needed.
- Optional `--classify-unknown` labeling pass (§2), gated, off by default.

### 3.5 `engine/wcprof/wcanalyze/graph.go` — IR
- **Remove** `breakWaitCycles` and `DroppedCycleWaits` (masking — deleted). Keep
  `ParentIntervalViolations` as a *diagnostic* (synchronous-children only).
- Add the exec self-time work-type segmentation (§1.4) to self-time accounting / the breakdown.

### 3.6 `engine/wcprof/wcanalyze/invariants.go` — NEW (validation spine; the salvaged chunk-1 concept)
- `type Invariant struct { Name string; Violations int; Worst string }`;
  `func (g *Graph) CheckInvariants(actualMakespanNS int64) []Invariant`:
  (1) **back-edges degenerate** — `BackedgeShortCircuits` are all **near-zero recorded duration**
  (the known mutual-join case); **fail loudly on any non-degenerate back-edge** (a large recorded
  wait window short-circuited → potential under-coupling, investigate); (2) **no op self >
  makespan**; (3) **bounded fallback** (≈0); (4) **sync parent-interval containment**
  (non-wait-targeted children only → `ParentIntervalViolations`); (5) **drift** vs actual when
  supplied.
- Reusable spine for the report (§3.8), regression/golden tests, and the oracle.

### 3.7 `cmd/wcprof-analyze` — tooling
- `-dump-spans=<path>`: serialize loaded `[]ProfSpan` (cloud/otel) → otlpdump JSONL, so a Cloud
  trace becomes a reproducible offline fixture (checked-in tests without a token; offline dev loop).
- `-diagnose`: print `CheckInvariants` + the existing `DriftOrigins`/`BaselineDrift`/`ExplainFinish`.
- `--classify-unknown` flag (§3.4).

### 3.8 `engine/wcprof/wcanalyze/report.go` — surface invariants loudly
- Replace the quiet `sim diagnostics:` line with a `CheckInvariants` block; any violation prints
  `WARNING:` with the worst offender. Drift line stays, now backed by the gate.

### 3.9 Deletions (hard cut)
- `resumedCallbackSpan`; `synthesizeCallExec` + `ceKey` + reparent helpers; `synthesizeExecPhases`
  child synthesis/reparent; `breakWaitCycles` + `DroppedCycleWaits`. The `replay.go` `inFlight`
  duration-masking break is **replaced** (not just removed) by the recorded-window short-circuit
  (§3.2). Grep-sweep for anything left dead. The end-state reads as: faithful emit (incl. the
  `call_exec` span) + windowed waits + a timing-honest replay — no loader synthesis ever existed.

---

## 4. Validation (first-class)

Weak validation on toy traces is how the +65% *and* the cycle shipped undetected. Layers, each of
which would have caught a real defect; the acyclicity assertion now backs a by-construction
guarantee (not a masked count).

### 4.1 Hard structural invariants (unit) — `invariants_test.go` (NEW)
`CheckInvariants` asserts on every fixture: **all `BackedgeShortCircuits` near-zero recorded
duration** (and **fail** on any non-degenerate back-edge), `no op self > makespan`, `fallback==0`
(well-formed), `ParentIntervalViolations==0` for synchronous children. Hand-crafted `ProfSpan`
fixtures: lazy resume with nested work (self=glue, children present); a **re-entrant concurrent
singleflight** pair with emitted bounded `call_exec` spans (the cycle shape) → assert the replay
schedules it **acyclically** with each near-instant back-edge contributing ~0 (this is the §1.5
regression test, replacing the deleted synthesis tests); a module exec (work-type segments correct,
no double-count); a plain exec (self = process time); a **service** fixture (`service_start` +
`service_lifetime`/availability daemon + a readiness/healthcheck child + a late teardown) → assert
(§1.4a) the lifetime+availability ops are elided (not ranked), the readiness child reparents to
`service_start`, the daemon is not a phantom bottleneck, and teardown timing does not move makespan.

### 4.2 Standing drift gate — two fixtures — `regression_test.go` (NEW)
- **`testdata/defb713e.otlp.jsonl` — replay + structural regression (NOT the full drift gate, NOT
  the bounded-call_exec proof).** Snapshot the immutable trace (§3.7). Assert the §1.5 replay fix +
  the loader deletion hold on a real trace: back-edges all near-zero (acyclic, no masking), no
  processRun double-count, replay doesn't explode. **Caveat [review — Codex]:** this *old* recording
  has no emitted `call_exec` spans and mis-nests lazy work, so it **cannot** prove the §1.3/§1.2
  emit semantics — only that the new replay + loader handle a pre-fix trace correctly. Drift improves
  but won't hit the gate (fixing the old recording would require the inference we deleted), so assert
  "improved vs +65%", not an absolute threshold. The real §1.3/§1.2/§1.5 proof is the fresh fixture +
  oracle below.
- **`testdata/<fresh>.otlp.jsonl` — the real drift gate.** Fresh `engine-dev container sync` on an
  engine built **with** the §1.2 + §1.3 + §1.6 emit fixes; assert `drift ≤ ~10–15%`, back-edges
  degenerate, `fallback==0`, no op self > makespan, top bottleneck is a *real* work op (not `lazy:resume*`).

### 4.3 Cross-source native↔OTel oracle — `oracle_test.go` (NEW) · also answers the native-cycle question
Check in a matched pair from one real run (native `/debug/wcprof/dump` + `otlpdump` JSONL),
generated via the documented loop (`hack/dev` + `--profile` + otlpdump + curl dump). The test
builds both graphs and compares **per-class self-time** under a checked-in native↔OTel mapping +
exclusion table (now near-1:1 since OTel emits `call_exec` too — native `call`↔OTel `call`, native
`call_exec`↔OTel `call_exec`; excludes OTel-suppressed hits, native-finest exec phases (OTel defers
them, §1.4), OTel-only leaf-IO). Assert mapped classes agree within tolerance — esp.
`lazy:resume*` (~glue both sides post-§1.2). **Crucially**, run on a **re-entrant-concurrent-module
workload** and assert: native and OTel **both acyclic** post-§1.5, with matching bottleneck
rankings — this is the arbiter for the §1.5 native-parity hypothesis and the no-coupling-regression
requirement.

### 4.4 Known-answer injection (end-to-end) — `core/integration` harness (NEW)
File-gated artificial delays on the OTel path, asserting rankings: **serial** on the critical path
→ ranked #1; **parallel/off-path** → not ranked; **lazy** delay → ranked via `lazy:resume*`;
**singleflight shared-exec** delay → joiners show the dependency, ranked once (not N×) **and the
coupling is preserved** (scaling the shared exec shows savings in joiners — the §1.5 coupling
guard); **service-start readiness** → ranked; **daemon availability** → not ranked. Disarming each
removes it (no false positive).

### 4.5 Golden + robustness — extend `loadotel_test.go`
Small fixed traces → checked-in expected ops/edges/self-times; robustness: fragmented/multi-root,
`Open` spans, dropped counts >0 (diagnostics fire), fully un-augmented trace (low-fidelity handling,
no crash, report flags low coverage rather than confident rankings).

### 4.6 What "good" looks like (gates)
Fresh-capture drift ≤ ~10–15% with back-edges degenerate (near-zero) / fallback==0 / no op self >
makespan; old `defb` back-edges degenerate + no double-count + improved drift; oracle per-class
agreement (esp. `lazy:resume*`) **and** native+OTel both with degenerate back-edges + matching
rankings on the re-entrant workload; injection serial ranked / parallel not / singleflight coupling
preserved, no false positives; lazy + call_exec exec logs/errors still on the install/call span; top
answer a real work op. Checked-in assertions — a miss is a build failure.

---

## 5. Sequencing + chunk roadmap (reworked)

1. **Replay join-semantics fix + invariant spine (§3.2, §3.5–3.6, §4.1).** The foundational chunk:
   makes the replay terminate with back-edges resolved truthfully (degenerate), and asserts it.
   Testable on hand-crafted fixtures + the
   `defb` snapshot **without** an engine rebuild. This must land first — everything downstream
   assumes an acyclic, timing-honest replay.
2. **Delete the loader synthesis (§3.3–3.4)** + the `-dump-spans` tooling and old-`defb` regression
   fixture (§3.7, §4.2 first bullet). Proves the deletion + §1.5 yield degenerate back-edges (no
   masking, replay terminates) on the real trace.
3. **Emit fixes (§3.1, §3.1a–b): `call_exec` span (§1.3) + lazy resume-parent/log-target split
   (§1.2) + join-identity suppression fix (§1.6).** The +65% prime driver + cancellation-correct
   bounded execution nodes + attribution. Validated by a FRESH post-fix capture drift gate (§4.2
   second bullet), the oracle (§4.3), and the lazy log/error capture. Requires an engine rebuild
   (`hack/dev`).
4. **Cross-source oracle + injection (§4.3–4.4)** — incl. the native-parity / re-entrant-workload
   arbiter for §1.5.
5. **Report/diagnose surfacing (§3.8) + exec work-type segmentation (§1.4) + coverage labeling +
   robustness (§2, §4.5).**

Each step lands with its tests.

### Fate of the in-progress chunk-1 changes — **SCRAP the inference; salvage the spine concept**
The uncommitted chunk-1 changes (`graph.go`, `loadotel.go`, `loadotel_synth.go`,
`loadotel_synth_test.go` modified; `invariants.go`, `invariants_test.go` new) were built on the
loader-synthesis approach this rework deletes:
- **Scrap:** the `synthesizeCallExec` transitive joiner-guard, the `processRun` reparenting, and
  `breakWaitCycles`/`DroppedCycleWaits` — all invalidated (they polish/guard an inference transform
  we're removing, and the cycle-breaker is masking). The `loadotel_synth_test.go` cases that assert
  synthesis/guard behavior go with them.
- **Salvage (concept, not necessarily code):** `invariants.go` / `CheckInvariants` — the invariant
  spine is exactly right and survives, **with one semantic change**: drop the `DroppedCycleWaits`
  masking-count invariant; replace it with the **`BackedgeShortCircuits` degeneracy** check (§3.6
  inv. 1) backed by the §1.5 by-construction resolution — it asserts the short-circuited back-edges
  are near-zero and fails on non-degenerate ones, reporting (not hiding) what was resolved.
- **Mechanics:** do **not** revert/delete the code this round (design only). The implementation
  round starts from this fate decision — likely `git checkout` the three modified loader files back
  to `HEAD` and re-derive `invariants.go` against the §1.5 replay. Net: most of chunk-1's *code* is
  discarded; its *validation-spine idea* carries forward.

## 6. Product decisions (resolved by Erik) + open flags

**Resolved by Erik (recorded):**
- **`call_exec` emit volume (§1.3) — ACCEPTED for v1.** Erik signed off on the bounded hidden
  `call_exec` span per cache-miss execution (~**+18–29% spans**, partly offset by deleting ~562
  synthesized ops) — "fine for now, we'll see how it goes." Keep the native-faithful per-execution
  emit; revisit volume later if it proves an issue (but the no-emit alternative stays rejected — it's
  *unsound* under early cancellation: orphaned post-cancel work).
- **Phase-granular what-if (§1.4) — DEFERRED for v1, ACCEPTED.** Erik signed off: drop the
  synthesized exec phase ops; keep work-type *attribution* via event-boundary segmentation (report
  only); no separate containerStart-vs-processRun what-if for v1. If needed later, emit real phase
  spans (never re-synthesize).

**Open flags (for review / Erik awareness):**
- **§1.5 is a shared-replay change and the deepest item.** It changes the counterfactual's join
  semantics for *both* OTel and native. The native-parity hypothesis (native exhibits the same
  cycle) is **[strong-hypothesis]** pending the §4.3 oracle. If the oracle shows native does *not*
  cycle (something OTel-specific still manufactures it), that's a stop-and-look.
- **Availability elision carve-out (§1.4a):** the one loader pass that reparents — kept, explicitly
  justified as tag-driven (not the deleted inference), up-only/acyclic, native-IR-restoring. A
  fully emit-faithful no-reparent variant is possible later, tied to the deferred exec-phase split.
- **Lazy log/error target (§1.2) + call_exec log/error target (§1.3):** the install/call-span
  logTarget splits are mandatory; no intended product-surface change; gated by the before/after
  capture.
- **Coverage gap (§2):** treated as non-fatal (303c evidence); the one escalation candidate if
  review disagrees.

## 7. Risks
- **R1 — §1.5 replay fix regresses DAG-case rankings or coupling.** The biggest risk. Mitigation:
  the injection tests (§4.4) assert coupling is preserved; the oracle (§4.3) asserts native+OTel
  rankings match; the algorithm keeps full-finish coupling on the acyclic majority and only uses
  recorded order for the rare back-edge. Budget iteration here.
- **R2 — native parity false.** Mitigation: the oracle is the arbiter; if false, escalate (the cycle
  would be OTel-specific after all, redirecting the fix).
- **R3 — lazy UI/log/error regression.** Mitigation: Passthrough + parent=install keeps the tree;
  the logTarget split keeps logs/errors on the install span; capture gate.
- **R4 — exec event-segmentation edge cases** (missing/disordered started/exited). Mitigation:
  fall back to op-level work-type, no crash; `[verify]`.
- **R5 — `-dump-spans` fidelity.** Mitigation: parity assert (cloud-loaded == dumped-JSONL-loaded).
