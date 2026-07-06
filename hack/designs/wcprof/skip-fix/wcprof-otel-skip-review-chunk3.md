# wcprof × OTel — skip-the-introspection-subtree fix plan: council review

---

## Review by the Chunk 3 implementer (owner of the gate-signal / faithfulness framing)

**Verdict up front:** skipping the introspection class is the right call AND the goal
survives it — but **the proposed cut is described with a fatal criterion** (`dagql.IsSkipped`).
Skipping by the **caller's** telemetry-visibility is race-dependent and *re-creates the exact
dangling wait-target the gate now hard-fails on*. The fix is sound if and only if the cut is a
**function of the shared work itself** (the call's introspection/`NoTelemetry` classification),
applied uniformly to `call_exec` + `publishResult` + the wait edge. Done that way it is
self-consistent **by construction**, zero-inference, and keeps user work first-class. I verified
all of this against branch code.

### (a) Correctness — free win, or does something go off?

**Skipping the introspection class is correct and ~free for the goal — IF cut correctly.**
The decisive fact: **user work never singleflight-waits on an introspection key.** To wait on a
cache key K you must *call* K's field (the concurrency key is the recipe digest, which includes
the field — `AroundFunc`'s `callDigest`/`req.ConcurrencyKey`). User pipelines do not call
`Function.args` / `TypeDef.as*` / `__schema` etc. So dropping those keys hides **no** user wait
and distorts **no** user self-time. The cross-boundary plumbing keys that user work *might* wait
on (the clone / runtime-load below) are **non-introspection** and stay profiled. So the analysis
does not "go off" — *provided the cut is by the call, not the caller.*

It is **NOT** free if cut by the caller. See (the acid test).

### THE ACID TEST — is the post-skip graph self-consistent? (the part I was asked not to rubber-stamp)

There are exactly two edge classes that can point into the skipped set. I worked both against
code:

**Parent edges — SAFE, by the existing context fall-through.** A skipped call emits *no* span:
`AroundFunc` returns `WithSkip(ctx), NoopDone` (core/telemetry.go:37) so no normal span, and
under the fix `getOrInitCall` skips `beginOTelCallExec` so `execSpan == nil`, `callCtx` is left
unchanged (dagql/cache.go:3731-3734), and the resolver runs under the **parent's** current span.
So a non-skipped child Y of a skipped call X is created under X's parent's span → Y reparents to
the nearest *emitted* ancestor. No orphan, no inference. This is the **same** mechanism already
validated by the §6.5 "emitter ≠ executor" test (`chunk2_test.go:265-287`, a suppressed caller's
sub-call nests under `call_exec`). ✔ Holds for any function-of-the-call cut.

**Wait edges — the real hole, and it is caller-dependence specifically.** The wait edge
(dagql/cache.go:3958) targets `oc.execSpanCtx` (the shared `call_exec`), and **both** the joiner
*and the executor itself* emit one (the executor falls through to `c.wait(..., joined=false)` at
cache.go:3785). If the work's `call_exec` was skipped, `oc.execSpanCtx` is invalid → the waiter
emits a target-less wait → `UnresolvedWaitTargets++` → **gate hard-fail** (loader.go:347-353,
gate.go:118). So the question is precisely: *can a waiter that is NOT skipped reference work whose
`call_exec` WAS skipped?*

- **Under the proposed `IsSkipped` (caller) cut: YES — and non-deterministically.** `IsSkipped`
  is a context value (dagql/internal.go:34-43) that **inherits** down a subtree and is set
  explicitly around user-facing plumbing: `core/sdk.go:253` (`hideCtx` over
  `CloneContainerDirectoryAccessor`/`Mounts`/`Meta` of `ctr.Self().FS`/`.Mounts`) and
  `core/modfunc.go:867` (`hideCtx` over `loadFunctionRuntime` → `runtimeImpl.Runtime(ctx, …)`,
  modfunc.go:780-799). These do **content-addressed** work on the *same* objects user work
  touches. So the same field F / key K is reachable from a `hideCtx` path (`IsSkipped=true`) and a
  user path (`IsSkipped=false`). Whoever wins the singleflight race claims K: if the skipped
  clone/runtime-load wins, `call_exec(K)` is skipped, and the non-skipped user joiner's
  wait-target dangles → the gate refuses an otherwise-good user trace, **flakily** (race-decided).
  This is *exactly* the scenario the brief flagged, and it is real, not hypothetical — it is the
  direct consequence of `WithSkip` wrapping cache-reaching plumbing over shared resources.

- **Why this is disqualifying even without a reproducer:** `call_exec` is **shared work**. Making
  its emit depend on *which caller won a race* means the same trace can compile to a different
  graph run-to-run. That is unfaithful data by definition (the principle), independent of whether a
  specific dangle manifests.

- **Under a function-of-the-call cut: the hole CANNOT open — by construction.** If the skip
  predicate is a function of the call (its field/receiver classification), then to *wait* on K you
  *call* K's field, so the waiter's classification *equals* K's classification. A non-skipped
  waiter on a skipped K is therefore impossible: same key ⟹ same field ⟹ same decision. The
  target is emitted **iff** its waiters reference it. Zero dangling, zero inference. The dangerous
  "F is skipped for caller A but not caller B" collapses to "F is skipped iff F's classification
  says so" — uniform across all callers.

**Conclusion of the acid test:** the danger is *entirely* in the caller-dependence of the
predicate, not in skipping per se. Fix the predicate and both edge classes are self-consistent.

### (b) Keeps the analysis zero-inference / no-heuristics?

**Yes — with the function-of-the-call cut, the loader and replay need ZERO changes.** No orphan to
re-home, no unresolved target to invent, no chaining. Self-consistency is structural: wait edges
never cross the skip boundary (waiter shares the field with its target), parent edges cross it but
reparent via context fall-through (an emit-time fact, not an inference). The `IsSkipped` cut, by
contrast, would force one of the two things the principle forbids: either the loader *infers* a
parent/target for the dangling edge, or the gate *refuses* a good trace. Reject it on that basis
alone.

### (c) Preserves the goal (find what's slow, user work first-class)?

**Yes.** User work and everything user work cache-waits on (non-introspection keys, **including**
the cross-boundary clone/runtime plumbing, which is non-introspection and stays profiled) remain
first-class. Only the introspection class — which user work does not depend on through the cache —
is dropped, which Erik has accepted. No user wait or self-time is silently absorbed. (One honest
note: if a *user module* dynamically builds typedefs via `Function.withArg`/`TypeDef.*`, those are
introspection-classified and would go unprofiled — but that is cheap object-building, not the
slow work the goal targets, and it stays self-consistent.)

### (d) Simple, or massive / different emit shape?

**Moderate and localized — but NOT the one-liner the `IsSkipped` framing implies, because of a
layering constraint I verified.** The high-volume class is classified by `introspectionInfo`,
which lives in **core** (core/telemetry.go:354-456) and keys on field names
(`__schema`/`function`/`typeDef`/`sourceMap`/`Function.withArg`/…). It is **not** reachable from
`dagql/cache.go` (core depends on dagql, not the reverse). And `field.Spec.NoTelemetry` —
the only classifier already in dagql (objects.go:919) — covers a **different** set: module
entrypoint/proxy *routing* fields (object.go:1168/1218/1264 "pure routing"), **not** the
introspection class. So a dagql-only `NoTelemetry` gate would **not** cut the volume.

Therefore the clean cut needs core → dagql communication: stamp the per-call classification onto
`dagql.CallRequest` (which today has no such field; add one + update `Clone()` at
call_request.go:21-37), set it in `core` where `introspectionInfo`/`isMeta` already run, and read
it at the two emit sites (`beginOTelCallExec`/`publishResult` at cache.go:3732/4016, and the wait
edge at cache.go:3958). **Not a new emit shape, not massive** — a small struct field + one stamp +
two guards.

### The recommended cut, precisely (and what to EXCLUDE)

**Skip predicate `P(call)` — a pure function of the call, stamped on `CallRequest` by core:**
`P = introspectionInfo(call) || isMeta(call) || call.Field.Spec.NoTelemetry`. Gate **all three**
emissions (`call_exec`, `publishResult`, and the wait edge — executor's *and* joiners') on `P`.

**Explicitly EXCLUDE from the predicate:**
- `dagql.IsSkipped(ctx)` — inherited / caller-dependent ⟹ the dangling hole above.
- the `ShouldEmitTelemetry` seen-key **dedup** (dagql/telemetry.go:48, used at telemetry.go:62) —
  it is *caller-set* dependent: within a single `ongoingCall` the executor sees first-time=true
  and joiners see seen=false (AroundFunc runs per-caller). Gating the **wait edge** on it would
  drop *joiners'* singleflight blocking while keeping the target — silently erasing real waits the
  analysis needs. The dedup is a normal-span concern; keep it out of the profiling cut.

This is also **cleaner than the brief's "emit iff a non-suppressed consumer observes the work"
candidate.** That consumer rule is correct in spirit but needs *deferred/lazy* `call_exec`
creation (you don't know the consumer set when the executor mints the span) which collides with
**Invariant T** (the target must exist, under lock, before any waiter observes the
ongoingCall — cache.go:3755-3763), and it makes the same field profile-or-not depending on who
happened to call it this run (more data-dependence). The function-of-the-call cut gets the same
self-consistency with **no deferral, no Invariant-T interaction, and no run-to-run variance.**

### Orthogonal, do not conflate

This volume fix is **independent** of the `publishResult` parentless-root finding (my prior review,
`wcprof-otel-publishresult-chunk3-impl.md`): for the *non-skipped* `publishResult` spans that
remain, parent them explicitly under the stashed `oc.execSpanCtx`. And the BSP-losslessness work is
the right *separate* backstop for legitimate user-work bursts — the skip removes the amplifier, but
a real heavy run can still exceed 2048; don't let the skip be treated as the BSP fix.

### One empirical check to close it

After implementing the **function-of-the-call** cut, run the module-load workload and assert the
gate sees `UnresolvedWaitTargets == 0`, `0` internal-kind/orphan roots beyond the one real root,
and span volume ≈ main. If anyone instead prototypes the `IsSkipped` cut, the falsifying check is:
run the same workload a few times and watch for *intermittent* `UnresolvedWaitTargets > 0` — the
race signature.

**Bottom line:** (a) correct + ~free for the goal, but only with the right cut; (b) zero-inference
preserved by construction (caller-dependent cut would break it); (c) goal preserved, user work and
its waits first-class; (d) moderate/localized (a `CallRequest` field + core stamp + two guards),
forced off the one-liner by the core-only location of `introspectionInfo`. **Adopt the
function-of-the-call predicate; reject `IsSkipped` and the dedup from the cut.**

**(Carried, separate):** service.start §3.4 self-erasure re-root still owed in both sources.
