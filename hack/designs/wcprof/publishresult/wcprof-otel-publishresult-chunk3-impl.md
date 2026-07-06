# wcprof × OTel — the `publishResult` emit-gap: investigation, gate signal, fundamental fix

**Investigation + analysis only — no code, no commits.** I own the gate-signal /
faithfulness-counter framing, and I designed the `publishResult` span in Chunk 2, so I
verified this against the actual emit + loader + a real trace rather than taking the
read on faith.

## (a) Investigation — is the framing correct?

**Yes: it is an EMIT gap, not a loader issue. The `publishResult` spans are emitted
GENUINELY PARENTLESS.** I can prove the structural claim by deduction, and the finding's
own shape corroborates the precise mechanism.

### The loader is correct (ruled out first)
`causalParentSpanID(s)` returns `wcprof.parent ?? s.ParentID`; the loader resolves it via
`opIDBySpan[...]` (loader.go:284) and every deduped span gets an op id (loader.go:234–236,
no kind filtering). The Chunk 2 SDK test feeds `otSpan(idLazy, idExec, publishResult…)`
and asserts the loader nests it under call_exec — and it passes. So **given a recorded
parent edge, the loader parents `publishResult` correctly.** The bug is upstream of the
loader.

### The Chunk 2 SDK test cannot catch this (methodological gap)
That test **hardcodes** `parentId = idExec`. It validates the loader's *interpretation* of
a correct parent; it never exercises the runtime emit's *context propagation*. So it is
structurally incapable of catching a real emit-context gap. (Recommendation below: a
real-emit test through an in-memory tracer.)

### Deduction: the `publishResult` roots are genuinely parentless
1. The emit creates `publishResult` on `oc.sharedWorkCtx` (cache.go:4017), which carries
   the call_exec span — confirmed two independent ways: (i) the lease helpers
   (`withoutOperationLease`/`withOperationLease`, `snapshots.WithoutLazyLease`) only touch
   lease keys via `context.WithValue`, never the OTel span; (ii) **the resolver's own
   sub-call spans nest correctly under call_exec** — the trace has 331 roots, not the
   thousands we'd see if `sharedWorkCtx` had lost the span. So `sharedWorkCtx → call_exec`
   is real.
2. The **gate passes today**. Call_exec wait edges target the stashed `oc.execSpanCtx`
   *value*; an unresolved non-lock wait is `UnresolvedWaitTargets++` and a loud gate
   failure (loader.go:347–353, gate.go:118). Gate passes ⟹ those targets resolve ⟹
   **call_exec spans are present in the dump** (`opIDBySpan[call_exec] ≠ 0`).
3. If `publishResult.parentId` were the call_exec span, then (since call_exec is in the
   dump) `publishResult` would resolve to it and **not** be a root. But `publishResult`
   *are* roots. Therefore `publishResult.parentId` is **not** call_exec — and since the
   emit creates it on the call_exec context, the only remaining possibility is that the
   exported `parentId` is **empty**. The spans are emitted parentless. ∎

### The precise mechanism (corroborated by the finding's shape)
`Tracer(ctx)` = `trace.SpanFromContext(ctx).TracerProvider().Tracer(…)` (tracing.go:11–12),
and `Start` parents from the current span in ctx. The **only** difference between the
sub-calls (correctly nested) and `publishResult` (orphaned):
- Sub-calls run **during** `fn(sharedWorkCtx)` — call_exec is **live**.
- `publishResult` is created in the waiter path at cache.go:4017, **after** the resolver
  goroutine already ended call_exec at cache.go:3772 (`telemetry.EndWithCause(execSpan)`).

So `publishResult` is parented by **context-propagation through an already-ended span**,
and in this telemetry setup that yields a **root** (the live-span machinery —
`SpanHeartbeater.activeSpans`, heartbeat.go:61 deletes a span on completion — and the
custom span/provider plumbing do not serve an ended span as a context parent). The
**corroboration is the finding's own shape**: if `sharedWorkCtx` simply lacked call_exec,
the resolver's *sub-calls* would be orphaned too (hundreds more roots). Only
`publishResult` — the one span created *after* its parent ended — is orphaned. That is
exactly the ended-parent signature.

The two things that reference call_exec and **do** work both pass it **explicitly**, never
via context-propagation through the ended span:
- **native `pubOp`**: `wcprof.BeginOp(wcprof.ContextWithOpID(ctx, oc.profOpID), …)`
  (cache.go:4008) — explicit parent op id.
- **OTel wait edges**: `emitOTelWait(ctx, oc.execSpanCtx, …)` (cache.go:3958) — explicit
  stashed `SpanContext` value.

`publishResult` is the lone outlier that relies on context-propagation. **That reliance is
the gap.** (I could not single-step the SDK's ended-span parenting statically; the one
empirical confirmation is `grep` a fresh trace for a `dagql.publishResult` span's
`parentId` — but the deduction above is airtight regardless of the SDK's exact reason.)

## (b) The fundamental fix (model + data in harmony) — explicit parenting, NOT a `wcprof.parent` stamp

**Parent `publishResult` explicitly under the stashed `oc.execSpanCtx`**, the same valid
call_exec `SpanContext` already used as the wait target (Invariant T guarantees it valid):
start the span on `trace.ContextWithSpanContext(ctx, oc.execSpanCtx)` instead of relying on
the ended span sitting in `sharedWorkCtx`. This mirrors **both** native's explicit
`profOpID` parenting **and** the wait edges' explicit target — the two references that
already work — and is robust to whatever the SDK does with ended spans. The result: the
span's **real exported `parentId` = call_exec**. Structurally honest data; the loader and
replay are unchanged; no analysis compensation.

**This is causally correct, not just expedient:** publication (indexing / dependency
attachment) is work done on behalf of that execution; native parents `pubOp` under the
execution op, so cross-source parity *requires* call_exec as the parent.

### Why I reject the lead's `wcprof.parent`-stamp suggestion (a principled distinction)
`wcprof.parent` exists for genuine **causal re-pointing** — where a span's *structural*
parent (its OTel `parentId`) legitimately differs from its *causal* parent, as in Chunk 3
lazy re-homing (the resume span structurally sits elsewhere but is causally the producer's
child). For `publishResult` there is **no such divergence**: its structural parent
*should* be call_exec and its causal parent *is* call_exec. Stamping a `wcprof.parent`
override here would paper over a structurally-false span with a loader-side correction —
i.e. **compensating in the DATA/emit layer for a broken structural edge**, which is the
same anti-pattern the governing principle forbids, one level down. The honest fix makes
the span structurally true. Reserve `wcprof.parent` for true structural/causal divergence;
do not use it as a patch for a parent edge the emit simply failed to set.

### Close the methodological gap
Add a **real emit-path test**: drive `beginOTelPublishResult` through an in-memory SDK
tracer with an **already-ended** call_exec parent and assert the exported span's
`parentId == call_exec`. The current hardcoded-parent SDK test could never have caught
this; a real-emit test guards the fix and the whole class.

## (c) The gate signal — a 4th, STRUCTURAL faithfulness signal (does not fold into the three)

**Detector.** A root whose op-kind cannot be a top-level entry. The unambiguous,
currently-broken case is **`OpKindInternal` as a root**: an internal op is *by definition*
sub-work of a call — it can never be a session entry point, so an internal-kind root is a
structural contradiction. The principled general form: define the kinds that may
legitimately be roots (the session/top-level kind — the one real root of the 331) and flag
any root outside that set. `internal`, `call_exec`, and `leaf` are all sub-work kinds; a
faithful trace has none of them as a root. **Recommendation:** hard-fail on internal-kind
roots now (the proven bug, zero false-positive risk), and extend the same check to
`call_exec`/`leaf` roots after confirming the one legitimate top-level kind, so we don't
false-positive the real command root.

**Hard-fail? Yes** — and this is the principle, not a band-aid (unlike the
`FallbackAnchors` hard-fail I had to walk into via item 3). A sub-work-kind root is
structurally impossible in faithful data, so its presence *is* an unfaithful EMIT ⟹
hard-fail, fix the emit. 0 by construction for faithful data; non-zero ⟹ fix the emit.
**Sequencing caveat:** until the emit fix lands, every real trace has ~330 internal-kind
roots, so this signal hard-fails every real analysis. That is the *correct* posture
(refuse to ship multi-root what-ifs on unfaithful structure) and a forcing function — but
it means **the emit fix and the gate signal should land together** (emit fix first, or
same change), or the gate blocks all real runs.

**It does NOT fold into FallbackAnchors, and does NOT route through item-3's reclassified
corners.** This is the crux of why the current gate misses it. The three existing counters
(`CycleWarnings`, `FallbackAnchors`, `SimStartConflicts`) are **replay-time** signals —
they fire when the simulation hits a reference it cannot schedule. A parentless
`publishResult` root triggers **none** of them: `Run()` simply anchors it at its recorded
start (an "independent root") and replays it cleanly — no inverted reference, no orphan
spawn, no cycle. Item 3's reclassified `spawnTo`-in-flight / `joinUpTo`-orphan corners all
require a *parent/child or wait reference* to fail on; a clean parentless root has none.
So this needs its **own** signal, detected **structurally at gate/load time** (inspect the
roots' kinds — no replay needed), as a **4th** faithfulness signal alongside the three
replay-time ones.

The property generalizes cleanly:
> **faithful data ⟺ the gate's STRUCTURAL checks pass (root-kinds, dropped-links,
> unresolved-wait-targets) AND the REPLAY counters are 0
> (CycleWarnings + FallbackAnchors + SimStartConflicts).**

The structural checks catch unfaithful *shape* before replay; the counters catch unfaithful
*causality* during replay. Both, separately — the both-and principle, now spanning two
detection phases.

## (d) Finding #4 — baselines now EXACT; is it sound, and is baseline drift a new signal?

**Sound — and a genuine win.** Removing the chaining anchors each root at its **recorded**
start, so baseline makespan = `max(simulated root finish) − min(recorded start)`, which
equals the recorded makespan when the critical-path root's replay is faithful at factor 1.
The old `chainSimEnd` fed each root's *simulated* finish into the next root's start,
compounding per-root replay drift across all 331 roots (incl. the 330 fake ones); that
compounding **was** the −2.4%. It was a compensation artifact, and it is correctly gone
(+0.0%). I'll also retract my own earlier theory: in my items-1&2 review I speculated the
−2.4% might be concurrent-wait under-anchoring or unrecorded idle. It was neither — it was
the chaining. The chaining *masked* the structural problem at baseline (the makespan is set
by the one real command root, so 330 tiny late `publishResult` roots don't move it) while
the multi-root **what-ifs were always on bad data**.

**Critical nuance: baseline-exact is necessary but NOT sufficient for faithfulness.** The
`publishResult` bug is the proof — baseline is exact (6.11 = recorded) while the root
structure is badly unfaithful. So "baseline matches recorded" and "structure is faithful"
are **separate** checks (both-and again); do not let an exact baseline lull us into
trusting the structure.

**Should baseline-vs-recorded drift become a gate signal? Yes — as a SOUNDNESS PROBE, with
care.** At factor 1 a rational model on faithful data must reproduce the recorded makespan,
so a non-zero drift is a real signal. But — unlike the four faithfulness signals — baseline
drift **conflates** model-reconstruction choices (implicit join, the wait join/abandoned
ε, the fixed-wait max model — all rational *model* interpretations) and sub-ms rounding
with data faithfulness. So it is **not** a pure "fix the emit" signal; its failure mode is
the both-and investigation ("is it the model's replay or the data's structure?").
Recommendation: add a **baseline == recorded** gate assertion with a **tight ε**
(sub-percent), and on exceedance trigger the both-and investigation rather than auto-blaming
the emit. This is a valuable new invariant the chaining was hiding; the −2.4% was far past
any ε (a real compensation bug), now ~0.

## Bottom line

- **Framing correct?** Yes — verified, not on faith. The loader is correct; the Chunk 2
  SDK test hardcodes the parent and cannot catch this; by deduction (gate passes ⟹
  call_exec present ⟹ `publishResult` roots are genuinely parentless) it is an **emit
  gap**. Mechanism: `publishResult` is parented by context-propagation through an
  **already-ended** call_exec span (created in the waiter path after the resolver ended
  it), corroborated by the finding's shape (only `publishResult`, never the live-parent
  sub-calls, is orphaned).
- **Gate signal:** a **4th, structural** faithfulness signal — *an internal-kind (sub-work)
  op as a root* — detected at gate time, **hard-failing**, 0 by construction for faithful
  data. It does **not** fold into `FallbackAnchors` and does **not** route through item-3's
  replay-time corners (a clean parentless root trips none of them — which is exactly why
  the current gate misses it). Land it **with** the emit fix to avoid blocking all real
  runs.
- **Fundamental emit fix:** parent `publishResult` **explicitly** under the stashed
  `oc.execSpanCtx` (mirroring native's `profOpID` and the wait edges), so the real
  `parentId` = call_exec — structurally honest data, no loader override. **Reject** the
  `wcprof.parent` stamp: that mechanism is for genuine structural/causal divergence (lazy),
  and using it here would be a DATA-layer compensation for a broken structural edge — the
  anti-pattern one level down. Add a real emit-path test (in-memory tracer, ended parent).
- **Finding #4:** sound; the −2.4% was 100% chaining (I retract my earlier under-anchoring
  guess). Baseline-exact ≠ structurally-faithful (the `publishResult` bug proves it). Add a
  tight-ε baseline==recorded probe, classified as a both-and soundness signal, not a pure
  emit signal.
- **Alignment:** fully aligned, and this is the principle *working* — removing the chaining
  compensation surfaced the hidden data gap. **One sharpening of the harmony goal:** hold
  the DATA layer to the same no-compensation standard as the model. The honest fix is
  structural truth in the emit, not a `wcprof.parent` patch in the loader; an override is
  legitimate only where structure and causality genuinely diverge. And the hardcoded-parent
  SDK-test pattern is itself a faithfulness blind spot — test the emit's structure, not just
  the loader's interpretation of it.

**(Carried, separate):** the service.start §3.4 self-erasure re-root is still owed in both
sources.
