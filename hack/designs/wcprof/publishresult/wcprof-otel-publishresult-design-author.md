# Chunk 4 — the `publishResult` root finding: investigation + fundamental fix (design author)

I led this from the emit side, tracing the full path on the implementer branch
`wcprof-otel-implementer-chunk4-7ad02bcf` (this design worktree is at base and has
no emit code): dagql emit → otel-go SDK/export → loader dedup/parent-resolution.
Investigation only.

## 1. Investigation — what the code actually does (don't take it on faith)

**The dagql EMIT provably parents `publishResult` to `call_exec`. It is created
ONLY as a child of an existing call_exec span — it cannot be emitted parentless.**
Evidence (branch line numbers):

- `cache.go:3732-3735`: the call_exec span is minted (`beginOTelCallExec`), then
  `sharedWorkCtx` is derived from the **same** `callCtx` that now carries it. So
  `sharedWorkCtx`'s ambient span is the call_exec span. (`withOperationLease`/
  `withoutOperationLease` only add/remove lease values via `context.WithValue` —
  they preserve the span; `operation_lease.go:26-48`.)
- `cache.go:4015-4018`: `publishResult` is emitted **only** `if oc.execSpanCtx.IsValid()`
  — i.e. only when call_exec exists — via `beginOTelPublishResult(context.WithoutCancel(oc.sharedWorkCtx))`.
  `otelprof_hooks.go:76` starts it as a child of the ambient span (= call_exec).
- otel-go export sets the child's `ParentSpanId` from the parent SpanContext
  (`transform.go:407-409`); `Passthrough()` is a pure UI attribute
  (`span.go:48`; consumed only in `dagql/dagui/*`), **not** a Cloud-export filter.
- Loader: dedup is by `SpanID` keeping max-end (`loader.go:213-216`) — it does **not**
  merge call_exec into the same-named caller span; **every** deduped span becomes an op
  (`loader.go:235-237, 280+`); a span is a root only when
  `opIDBySpan[causalParentSpanID(s)] == 0` (`loader.go:284-286`), and
  `causalParentSpanID` = `wcprof.parent` else `parentId` (`loader.go:445-449`).

So along the path I can read, `publishResult` ⟹ call_exec exists ∧
`publishResult.parent = call_exec` ∧ the export writes that parentId ∧ the loader
resolves it. **My code investigation does NOT reproduce a parentless publishResult.**

**Therefore the framing "the EMIT is missing the parent edge" is directionally right
(there IS a parent-edge gap in the captured graph — the 330 roots are real) but
mechanistically imprecise: the emit does set a parent — via the *ambient* OTel
context — and that ambient edge is being lost between emit and the loaded graph.**
The exact locus needs the real trace (which I don't have); it is one of:

- **H2 (most likely): the ambient parent is an ALREADY-ENDED span.** call_exec is
  ended early, inside the shared-work goroutine (`cache.go:3771`,
  `telemetry.EndWithCause(execSpan)`), and `publishResult` is created later, in
  `initCompletedResultOnce.Do`, from that **ended** span's context on a **detached**
  (`WithoutCancel`/`WithCancelCause`) cross-goroutine context. If Dagger's live SDK
  path doesn't carry an ended/detached span as a propagated parent, the child starts
  parentless. This is squarely an emit-shape fragility.
- **H1: call_exec is absent from the captured trace** (less likely — the
  `LiveSpanProcessor` snapshots at OnStart, `live.go:25-31`, so call_exec is exported
  when it starts — but worth ruling out for the detached/late path).
- **H3: loader bug** — not reproduced in my reading; lowest priority.

**Decisive diagnostic (for whoever has the trace):** inspect the `parentId` of the
root `publishResult` spans. *empty* ⟹ H2 (ambient/ended-span parent never recorded);
*a spanId not present in the trace* ⟹ H1 (call_exec dropped); *call_exec's spanId,
and call_exec IS present* ⟹ H3. The fix below is correct for H2/H1; only the "does it
fully resolve" answer differs (see §2).

## 2. The fundamental fix — make the causal parent EXPLICIT (model + data in harmony)

**Yes, this is the SAME class as the §3.1 singleflight / §3.2 lazy / §3.0.2
`wcprof.parent` choke points — and it has the same fix.** Every one of those was a
place where the *ambient* OTel parent was wrong, suppressed, or missing, and we
stopped relying on it and published an **explicit** causal edge (mint call_exec under
Invariant T; stamp `wcprof.parent` for re-homed lazy ops). `publishResult` is the
same shape: it leans on the ambient ended/detached span to carry its parent, and that
is fragile.

**The decisive confirmation that explicit-is-right: NATIVE already does exactly
this.** The native `publishResult` op is parented **explicitly**:
`wcprof.BeginOp(wcprof.ContextWithOpID(context.Background(), oc.profOpID), …)`
(`cache.go:4008-4011`) — it deliberately starts from `context.Background()` and
injects the parent op id (`profOpID`), precisely *because* the ambient context's
parent is not to be trusted for this deferred publication. The OTel side relying on
the ambient ended span is the lone inconsistency. Bringing them into **harmony** = make
the OTel parent explicit too.

**Concrete fix (adopt the lead's suggestion, with one caveat): stamp `wcprof.parent`
on the publishResult span with the call_exec span id we already hold.** We stash
`oc.execSpanCtx` under Invariant T (`cache.go:3755-3758`) and already gate on
`oc.execSpanCtx.IsValid()` — so `beginOTelPublishResult` can set
`attribute.String(telemetryattrs.WcprofParentAttr, oc.execSpanCtx.SpanID().String())`.
The loader's `causalParentSpanID` reads `wcprof.parent` first, so the edge is now an
**explicit, durable attribute** on the publishResult span — independent of whether the
ambient ended/detached span propagated. This is the minimal change and it reuses the
exact §3.0.2 mechanism.

**The caveat that the diagnostic decides:** `wcprof.parent` makes the loader *look up*
call_exec by span id — it only resolves if call_exec is **in the trace**. So:
- **H2 (parentId empty) → the explicit stamp FULLY FIXES it** (call_exec is present;
  we just weren't linking to it). This is the clean, complete fix and my lead
  hypothesis.
- **H1 (call_exec absent) → the stamp alone does NOT resolve** (it would point at a
  missing span → still an unresolved parent → still a root, now loudly via the gate in
  §3). Then the emit must *also* ensure call_exec survives capture (fix the
  detached/late export), or re-home publishResult onto a parent that is durably present
  (its nearest exported ancestor). Don't stamp blindly and declare victory — confirm
  H2 first.

**Interaction with §3.1 suppressed-caller folding: none, and it harmonizes.** The
suppressed-caller rule is about *wait* attribution (where a blocked caller's wait edge
lands). `publishResult` is a *child op*, not a wait; stamping its parent = call_exec
puts it under the shared execution exactly as native puts `pubOp` under `profOpID`. The
existing design note (it's a "native-parity diagnostic" charged to the caller class
because publication runs after call_exec and the caller wait close,
`otelprof_hooks.go:68-75`) is unchanged — that's a *self-time accounting* statement;
this fix is a *parent-edge* statement. They're orthogonal and now consistent with
native on both axes.

## 3. The gate signal — an "unfaithful root" faithfulness counter (hard-fail, 0 by construction)

Yes. The right signal is **broader than "internal-kind root"**: it is **any root that
is not a legitimate session/command root.** A faithful trace roots *only* the genuine
trace root(s) — the `session_phase` query/command spans (multiple are fine: concurrent
sessions, case a). Any other op surfacing as a root — `internal` (publishResult),
`call_exec`, `lazy`, `exec`, `call` — is an op that *by design has a causal parent*, so
its appearance as a root is a missing/broken parent edge = unfaithful emit.

- **Definition:** `UnfaithfulRoots = count(roots where kind ∉ {session_phase})` (the
  session/command allowlist; refine the exact kind set to whatever the genuine trace
  root carries). Hard-fail when `> 0`.
- **Fits "= 0 by construction for faithful data":** it joins `CycleWarnings`,
  `FallbackAnchors`, `SimStartConflicts` (item-3 made those data-faithfulness signals)
  as the **emit-faithfulness** member of the same family — those three catch unfaithful
  *replay-reachability*; this catches an unfaithful *root forest*. A faithful emit
  produces only session roots, so it is 0 by construction; non-zero ⟹ fix the emit.
- **It is general and future-proof:** it would have caught publishResult *before* the
  chaining masked it, and it catches the next choke point that loses its parent — which
  is exactly why this signal, not a publishResult-specific check, is the right design.
- **It is the structural complement to item 3's makespan finding:** item 3 noted the
  baseline is faithful (everything sits at recorded time regardless of root structure)
  but multi-root *what-ifs* sit on bad root structure. This counter is what makes that
  unfaithful structure *fail loudly* instead of passing silently.

## 4. Design reconcile (specify; do not edit)

- **New EMIT INVARIANT (generalize §3.0.2):** an op's causal parent must be published
  as an **explicit, durable edge**, never inferred from an ambient OTel span that may
  be ended, detached, suppressed, or cross-goroutine. For shared/deferred work
  (singleflight publication, lazy re-pointing, and any future choke point that runs
  outside its causal parent's live span), stamp `wcprof.parent` with the
  Invariant-T-stashed parent SpanContext. State the rule as: *if the work does not run
  synchronously inside its causal parent's live span, do not rely on the ambient
  parent — stamp it.* publishResult is the new concrete instance; native's
  `ContextWithOpID(profOpID)` is the cross-source precedent.
- **§6.1 gate:** add `UnfaithfulRoots` (non-session-root count) as a hard-fail
  data-faithfulness invariant, documented alongside Cycle/Fallback/SimStartConflict as
  "= 0 by construction for faithful data; non-zero ⟹ fix the emit choke point."
- **Cross-source note:** record that native parents publication explicitly via the op
  id and OTel must parent it explicitly via `wcprof.parent` — one sentence so a future
  reader sees the two sources are deliberately parented the same way, not by accident.
- **Scope:** this is the **intra-trace** parent edge and is fixable now; it is distinct
  from the deferred **cross-session** orchestrator link (test-framework semaphores),
  which the engine genuinely cannot observe and stays out of scope.

## Summary

- **Is the framing correct?** Directionally yes (there is a real captured parent-edge
  gap — the 330 roots are real and it's the principle working: removing the chaining
  unmasked it), but **mechanistically imprecise**: the dagql emit *does* set
  publishResult's parent to call_exec (it's created only as call_exec's child —
  `cache.go:3732-3735, 4015-4018`; `transform.go:407`; loader `284-286, 445-449`), so
  "the emit omits the edge" isn't what the code does. The edge is set via the **ambient
  ended/detached span** and lost in capture. Decisive check: the `parentId` of the root
  publishResult spans (empty ⟹ H2, the ambient/ended-span parent; a missing spanId ⟹
  H1, call_exec dropped).
- **Fundamental fix:** make the parent **explicit** — stamp `wcprof.parent =
  oc.execSpanCtx.SpanID()` on the publishResult span (we already hold it under
  Invariant T). Same class and same mechanism as the §3.1/§3.2/§3.0.2 choke points, and
  it brings OTel into harmony with native, which *already* parents publication
  explicitly (`ContextWithOpID(profOpID)`, `cache.go:4008-4011`). The lead's suggestion
  is right; the one caveat is it fully resolves under H2 (call_exec present) but under
  H1 (call_exec dropped) the emit must also keep call_exec in the capture — so confirm
  H2 with the diagnostic before declaring done. No conflict with suppressed-caller
  folding (that's wait attribution; this is the child-op parent edge).
- **Gate signal:** add `UnfaithfulRoots` = roots whose kind ∉ {session_phase}, hard-fail
  > 0 — the emit-faithfulness member of the "= 0 by construction" family, general enough
  to catch the next lost-parent choke point, not just publishResult.
- **Alignment:** fully aligned with the principle and the harmony goal — the analysis
  reads an explicit recorded edge and infers nothing; the data publishes the true
  parent. My only pushback is precision: "missing edge" → "fragile *ambient* edge,
  lost in capture; replace with an explicit durable one," and **run the parentId
  diagnostic before stamping** so we fix the actual locus (H2 vs also-H1), not a
  presumed one.
```
