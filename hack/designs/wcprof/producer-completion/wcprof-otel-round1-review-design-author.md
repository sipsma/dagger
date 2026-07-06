# Round-1 producer-completion review — design author, commit `814df0173c`

Light completion round (service.start §3.4 asymmetry + losslessness measurement +
renames). Reviewed `git diff 4921d53662..814df0173c` against canonical §3.4/§3.0.1 at
file:line; pass kept proportionate.

## Verdict: SIGN OFF.

The §3.4 Get-side OTel wait correctly closes the native/OTel asymmetry under Invariant T,
the rename is pure and complete, the knob removal and losslessness leave-out are both
consistent with the principle, and the moot items are genuinely moot. No blocker.

## (a) service.start §3.4 — faithful

The only behavioral add is the OTel wait in `Services.Get`'s `isStarting` branch
(core/services.go:387/391). Verified:
- **Mirrors native and `startWithKey`.** Get already emitted native
  `wcprof.BeginWait(starting.profOpID, WaitReasonService)` (:376) with no OTel analog;
  the diff adds `dagql.EmitOTelWait(ctx, starting.otelStartSpanCtx, WaitReasonService,…)`
  in **both** unblock branches (`ctx.Done()` :387 and `starting.done` :391) — byte-for-byte
  the pattern `startWithKey` already uses (:1015/:1020). The asymmetry is closed.
- **Invariant T (§3.0.1) holds.** The target `starting.otelStartSpanCtx` is the
  service.start span context, stashed at `start.otelStartSpanCtx = startSpan.SpanContext()`
  (:1053) **before** the waiter-observable publish `ss.starting[key] = start` (:1055), under
  the services lock that `Get` also reads under (:368). So any `Get` caller that sees the
  `starting` entry has a valid target — no stash-after-publish window.
- **Gate-observable if untraced (distinct-from-invalid).** `EmitOTelWait` self-gates on a
  recording waiter and emits a targetless (gate-observable) wait if the start ran untraced,
  rather than dropping the edge — identical to the singleflight/lazy waits and to native's
  unresolved `BeginWait`. The blocked interval `[otelWaitStartNS, now]` is captured before
  the select and closed on unblock in both branches. Correct.
- **The slow-start test validates the analysis-side §3.4 faithfulness** and is the proper
  inverse of `TestChunk4ServicesFidelity`: a trace where the daemon/availability span comes
  up late (`[55,82]`) so service.start `[8,60]`'s start+health-check self is *not* erased.
  It asserts (1) gate clean — `UnresolvedWaitTargets/OrphanedParents/UnschedulableOps == 0`,
  the installer wait resolving to service.start (Invariant T); (2) `svcStart.SelfNS() ≥ 40ms`
  — the self is carried, not absorbed by the long-lived child (which overlaps only the tail);
  (3) `TopBottlenecks(...)[0]` is `{service_start, service.start}` — the slow start HEADLINES
  while the off-path idle daemon does not rank. Together with the fidelity test (daemon spans
  the window → self ~0 → doesn't headline), both self-erasure directions are covered. The
  self-erasure model is exactly canonical §3.4: service.start self = the window not covered by
  the availability child = the real start work.

## (b) Rename + knob removal — pure, complete, principle-consistent

- `FallbackAnchors → UnschedulableOps`, `FallbackAnchorOps → UnschedulableOpsSample`,
  `fallbackAnchor → anchorUnschedulable` across replay.go/report.go/gate.go (+ cmd + tests).
  Function bodies are **identical** (anchor at recorded offset, count++, sample) — a pure
  identifier rename; the faithfulness signal's semantics are unchanged. Completeness
  verified: the only remaining `FallbackAnchors`/`MaxFallbackAnchors` token anywhere is a
  historical comment (gate.go:24) — no code reference left, so no build break.
- **Knob removal is behavior-preserving for the default and tightens correctly.**
  `GateOptions{MaxFallbackAnchors int}` → `struct{}`; the gate goes from
  `r.FallbackAnchors > opts.MaxFallbackAnchors` to `r.UnschedulableOps > 0`. Since the knob
  defaulted to 0 (hard-fail on any), the default path is unchanged; only the debug escape
  hatch (tolerate N) is gone — which is exactly right under the principle (an unschedulable
  op is unfaithful EMIT, never a quantity to tolerate). The cmd `-max-fallback-anchors` flag
  removal matches.
- *Note (not a blocker):* this is a cosmetic + small public-API change to the **shared**
  `wcanalyze`/`wcotel` (`Simulation` fields, `GateOptions`) that native/PR #13393 also use.
  All in-repo consumers are updated; external consumers don't exist. Flag it for the same
  "shared analyzer" awareness the chunk4 replay changes got — no action needed.

## (c) Losslessness measured, left out — sound

Consistent with the principle: the structural gate **refuses** dropped/incomplete data
(OrphanedParents/UnresolvedWaitTargets), so a BSP overflow can never be silently *ranked* —
only refused. A losslessness backstop would improve *coverage* (fewer refused captures), not
*correctness*. Post-skip the amplifier is gone, and two complex captures (10456/10342 spans)
pass the gate with 0 OrphanedParents and no dropped batches — empirically no overflow. If a
future heavy user-work burst overflows, the gate catches it. Erik's leave-it-out is correct;
it remains a separate, optional optimization (not a faithfulness gap).

## (d) Moot items + alignment

- **publishResult `wcprof.parent` moot** — confirmed: a parentless `publishResult` has an
  empty causal-parent span, so it does **not** trip `OrphanedParents` (the gate requires
  `cpSpan != ""`); the skip fix only reduces the count. It's a (false) root, not an orphan,
  so it doesn't block this gate — exactly the chunk3-owned boundary already documented. Moot
  for this work.
- **nested-client edge handled** — the sub-session launch is the recorded reparenting edge
  (graph.go), so those roots aren't pure roots; no conflict.
- **No canonical conflict; zero unintended loader/replay behavior change.** The wcanalyze
  diff is the rename (cosmetic) plus the knob removal (= the prior default). The replay's
  anchoring/gating logic is untouched. Aligns with the rational-function principle.

## (e) Anything missed — one non-blocking note

The new §3.4 Get wait inherits the **cross-session** targetless-wait behavior: a traced
`Get` caller blocking on a service started by another (untraced-in-this-trace) session emits
a gate-observable unresolved wait → that single-trace capture gate-fails. This is
*correct-by-design* under the settled one-trace / cross-session-out-of-scope stance (refuse
incomplete data, never mis-rank it) and matches native + the singleflight/lazy cross-session
handling — not a regression, just worth noting that service-heavy cross-session captures are
refused rather than analyzed.

**Signed off.** §3.4 asymmetry closed faithfully (Invariant T, mirrors startWithKey, slow-
start test validates headline + idle-zero + gate clean); rename pure and complete; knob
removal and losslessness leave-out principle-consistent; moot items moot; no canonical
conflict; loader/replay behavior unchanged save the cosmetic rename.
