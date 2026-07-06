# Round-1 review (producer-completion) — `814df0173c` — Chunk 3 implementer (lazy / wcprof.parent owner; service.start §3.4 carrier)

**Reviewed:** `git diff 4921d53662..814df0173c` + resulting files in the coder worktree.
Proportionate to a light producer-completion round, with rigor on §3.4 (my carried item).
Review only — did not modify the branch.

## SIGN-OFF — and §3.4 can be RETIRED from "carried"

No blocker. My long-carried `service.start` §3.4 item is genuinely closed to my standard:
the native/OTel wait asymmetry is fixed symmetrically in both `Get` and `startWithKey`, no
other service asymmetry remains, and the new test exercises both halves (slow start
headlines AND idle ranks 0). I am dropping §3.4 from my "carried" line.

### (a) service.start §3.4 — CLOSED

**The new wait edge mirrors native correctly.** `Get`'s `isStarting` branch now emits
`dagql.EmitOTelWait(ctx, starting.otelStartSpanCtx, WaitReasonService, …)` (core/services.go
:387 ctx-cancel, :391 done) alongside the native `wcprof.BeginWait(ctx, starting.profOpID,
WaitReasonService)` (:376) — both branches, matching native's `profWait.End()` in both. The
target `starting.otelStartSpanCtx` is the `service.start` span, stashed at :1053 alongside
`profOpID` (:1050) under `ss.l` and **before `starting` is published** — Invariant T holds, so
a `Get` caller that observes the in-flight start always has a valid target (and
`EmitOTelWait` emits a gate-observable targetless wait if the start ran untraced, never
dropping the edge). `"time"` is already imported (:10). It is a faithful mirror of
`startWithKey`'s `isStarting` branch (:1004/:1015/:1020).

**No other native/OTel service asymmetry.** I enumerated every `wcprof.` emit in services.go:
the `service.start` op (`BeginOp` :1032, OTel analog `beginOTelServiceStart` :1043, with
`endOTelServiceStart` mirroring `profOp.End` in all four end paths :1063/:1076/:1092/:1101)
and the two `isStarting` waits (:376 `Get`, :1004 `startWithKey`) — all now symmetric. There is
no separate native emit on the Stop path, the health-check (it is folded into the
`service.start` span's self-time, by design), or idle teardown, so there is nothing left to
mirror.

**The slow-start test genuinely exercises both halves** (`TestChunk4SlowServiceStartHeadlines`):
- HEADLINE: `service.start [8,60]` carries the start+health-check self-time — asserts
  `svcStart.SelfNS() >= 40ms` (NOT erased) and `TopBottlenecks` ranks `{service_start,
  service.start}` first.
- IDLE-RANKS-0 (the §3.4 self-erasure, validated even under a slow start): the daemon
  `exec.processRun` (substantial self-time but off-path, ends at 80 < consumer 84) → `savedFor
  == 0`; the long-lived availability span → `savedFor == 0`.
- Plus it pins the new wait: installer B's `service` wait resolves to `service.start` and the
  gate is clean (`unresolved=0 orphaned=0 unschedulable=0 interval>span=0`) — exercising the
  loaded analog of the EmitOTelWait added here.

This is the "slow start headlines" assertion §3.4 owed, and it also confirms the earlier
re-root (idle/availability contribute 0). Done to my standard. **Retire §3.4 from carried.**

### (b) Rename `FallbackAnchors` → `UnschedulableOps` — pure, complete, knob-removal safe

Behavior-identical identifier rename: the counter (`fallbackAnchor` increments
`UnschedulableOps`, replay.go:577) and the gate hard-fail (gate.go:142, `> 0`) are unchanged
in semantics — `UnschedulableOps` just names the post-item-3 meaning ("the recorded structure
can't schedule this" = unfaithful data) more honestly than "FallbackAnchors". Defined/used
consistently across replay.go (:311/:313), gate.go (:61/:105/:142/:170), report.go
(:163-176), and the CLI. **No stale refs:** the only remaining `MaxFallbackAnchors` mention is
an explanatory comment (gate.go:24) documenting the knob's removal; zero stale code/test refs.
**Knob removal safe:** `MaxFallbackAnchors` was the opt-out tolerance for unfaithful data —
dropping it makes the gate strictly stricter (always hard-fail on `> 0`), which is the
correct, principle-aligned end-state (no tolerance for unfaithful data; I flagged it as a
removal candidate in round 2).

### (c) Losslessness — measured, leave-out safe

Two complex post-skip captures (10456 / 10342 spans, nested SDK) pass the gate with 0
OrphanedParents — credible evidence the amplifier removal alone keeps realistic workloads
under the BSP queue. Leaving the backstop out is safe under our principle precisely because a
future overflow is **gate-detectable, not silent**: drops → dropped parents/targets → the
structural gate hard-fails (refuses) rather than emitting a wrong analysis. Measurement +
Erik's call; not my domain; lightly confirmed sound.

### (d) Moot items — moot for the gate (with one honest caveat on publishResult)

- **nested-client sub-session edge** (graph.go:270-294 already handles it): consistent with my
  round-2 read; moot. ✓
- **publishResult `wcprof.parent` — moot for merge, but empirically-moot, not structurally
  fixed.** I checked: the emit is **unchanged** (`beginOTelPublishResult(context.WithoutCancel
  (oc.sharedWorkCtx))`, cache.go:4081; otelprof_hooks.go:76 last touched in 4d6987fdc2), so it
  still parents through the already-ended `call_exec`. The "0 parentless post-skip" is an
  **empirical §9 measurement**, not a code guarantee. That is fine for merge for a stronger
  reason than the measurement: a parentless `publishResult` is `OpKindInternal` with an **empty
  `cpSpan`**, so it is a *root*, and the orphan check requires `cpSpan != "" && parentID == 0`
  (loader) — so it **cannot trip `OrphanedParents` whether the count is 0 or not**, and no
  internal-kind-root signal exists. So it is gate-invisible either way → genuinely moot for
  this merge. (Caveat for the record: if a future internal-kind-root faithfulness signal is
  ever added — my earlier proposal — the explicit-`execSpanCtx`-parenting fix would then be
  required. Out of scope now; correctly not done here.)

### (e) Anything missed

The remaining diff is rename propagation to the CLI (`cmd/wcprof-otel-analyze/main.go`),
`report.go`, and the replay/gate tests — all consistent with the rename, no behavior change.
Note this commit **intentionally** touches `wcanalyze`/`wcotel` (the rename + the §3.4 wait +
the chunk4 test); that does not violate the skip fix's "zero loader/replay change" property —
that property was scoped to the skip-fix commit, and this is a separate producer-completion
commit whose analysis-layer touches are a pure rename + an additive test. No blocker found.

## Converged — sign off. §3.4 retired from carried.

(Nothing further owed from me on this workstream.)
