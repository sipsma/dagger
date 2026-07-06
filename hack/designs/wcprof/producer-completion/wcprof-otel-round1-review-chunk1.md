# wcprof × OTel — Round-1 producer-completion review (Chunk 1 / loader+gate owner)

**Reviewer:** Chunk 1 owner (loader + §6.1 gate). Reviewed `814df0173c` atop
`4921d53662` (unpushed). Light round — proportionate pass, focused on the rename
(my gate signal) and the complex-capture gate-clean claim. No code modified.

## SIGN-OFF ✅

Clean. The rename is pure and complete, the gate still hard-fails correctly, the
service.start wait mirrors native faithfully, and the losslessness leave-out is safe
because my gate is the backstop. No blocker.

## (a) The `FallbackAnchors → UnschedulableOps` rename — pure, complete, behavior-identical

- **Counting semantics unchanged.** `git diff` on `wcanalyze/replay.go` is a **pure
  rename** — every changed line is just the identifier swap (`fallbackAnchor →
  anchorUnschedulable`, `FallbackAnchors → UnschedulableOps`); filtering those out
  leaves zero diff lines. So what counts as unschedulable is byte-for-byte the old
  signal. `report.go`/`gate.go` likewise rename-only.
- **Still hard-fails on `>0`.** `gate.go:142` `if r.UnschedulableOps > 0` appends the
  same violation message as the old `FallbackAnchors` ("inverted reference / malformed
  nesting … impossible in a faithful synchronous nesting … unfaithful EMIT … never
  papered over"). `r.UnschedulableOps = sim.UnschedulableOps` (:105). ✓
- **Knob removal is behavior-identical at the default.** The old `FallbackBound`
  field's own comment was `// MaxFallbackAnchors, echoed; 0 = hard-fail on any` — i.e.
  the default (`MaxFallbackAnchors=0`) **already** hard-failed on `>0`. The new
  unconditional `>0` therefore matches the default exactly; only the tolerance escape
  hatch is removed — which is precisely the unconditional hard-fail my chunk4 round-2
  reconfirm review endorsed (and Erik's lean). `GateOptions` is now `struct{}` and the
  sole caller (`cmd/wcprof-otel-analyze/main.go`) is updated to `GateOptions{}`, with
  the `-max-fallback-anchors` flag dropped — *including* its stale, contradictory help
  text (`"0 = report-only"`, which never matched the actual `0 = hard-fail` semantics).
  No caller broken.
- **No stale refs.** The only `MaxFallbackAnchors` token left anywhere in code is the
  explanatory comment at `gate.go:24` documenting the removal — no live usage of
  `FallbackAnchors`/`MaxFallbackAnchors`/`FallbackBound`/`-max-fallback-anchors`
  remains.

Net: a faithful rename + the agreed unconditional-hard-fail, no smuggled behavior
change to the signal itself.

## (b) service.start §3.4 — the new `EmitOTelWait` mirrors native; test is real

- **Faithful mirror, not a double-emit.** `core/services.go` `Services.Get` `isStarting`
  branch (~:375) now emits, in **both** arms of the wait `select`
  (`case <-ctx.Done()` :387, `case <-starting.done()` :391), the pair `profWait.End()`
  (native) + `dagql.EmitOTelWait(ctx, starting.otelStartSpanCtx, WaitReasonService, …)`
  (OTel). Only one arm fires per call, so it's exactly one OTel wait per blocked Get —
  the analog of native's `BeginWait(WaitReasonService)`, matching the pre-existing
  `startWithKey` pattern (:1015/:1020). It targets the `service.start` span and, per
  Invariant T, `EmitOTelWait` emits a gate-observable **targetless** wait if the start
  ran untraced (cross-session) rather than dropping the edge — preserving the
  distinct-from-invalid property I rely on. ✓
- **`TestChunk4SlowServiceStartHeadlines` is substantive.** It builds the slow-start
  inverse (service.start [8,60]=47ms self, idling to 80; daemon spins up at the tail)
  and asserts all of: gate clean (the installer's service wait resolves to
  service.start), service.start **carries** the start+health-check self-time (not
  erased to ~0), the slow service.start **headlines** (`TopBottlenecks` `ranked[0] ==
  {service_start, service.start}`), AND idle-doesn't-rank (idle daemon `exec.processRun`
  saved=0, long-lived availability span saved=0). That is exactly the "slow start
  headlines + self-erasure still holds" assertion §3.4 owed. ✓

## (c) Losslessness — measurement sound, leave-out safe

Two complex post-skip captures (10456 / 10342 spans, incl. nested SDK) gate clean with
0 `OrphanedParents`. **`0/0` is the correct merge proxy** from my seat: my gate is the
exact mechanism that catches drop-induced loss (a dropped parent/target surfaces as
`OrphanedParents`/`UnresolvedWaitTargets`), so a clean gate at this volume *is* the
losslessness evidence. Leaving out a BSP-backpressure backstop is safe because the
failure mode is **loud, not silent**: if a future heavier workload overflows the
BatchSpanProcessor, the gate refuses the incomplete capture rather than producing a
plausible-but-wrong analysis — which is the rational-function-of-faithful-data
discipline, not a regression. Endorse the leave-out.

## (d) Moot items — genuinely moot

- **publishResult `wcprof.parent`: 0 parentless** on the captures — so there are no
  internal-kind parentless roots to re-home, and (as I noted in the publishResult
  review) such survivors carry an *empty* `cpSpan` and don't trip `OrphanedParents`
  anyway. Nothing owed. ✓
- **Nested-client edge handled (`graph.go:270-294`)** and empirically confirmed by the
  nested-SDK captures gating clean (0 orphans) — this also closes the case-2
  ("call_exec in a sibling sub-trace") branch I flagged in the publishResult review. ✓

## (e) Anything missed

Nothing. The rename touches my files but is pure (verified line-by-line), the
unconditional hard-fail is the agreed end-state, the service wait is a correct mirror,
and the complex captures validate the gate empirically. **Converged — sign off.**
