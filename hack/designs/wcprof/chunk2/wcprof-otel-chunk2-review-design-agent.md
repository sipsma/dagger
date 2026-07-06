# wcprof × OTel — Chunk 2 review (by the design author)

**Scope:** (A) Chunk 2 in isolation — the singleflight central fix (design §3.1),
and (B) the first **holistic** pass across Chunks 1+2. Reviewed commit
`f127e5662b` (on Chunk 1 `71b69f1f16`, base `b442cd2533`) against the contract of
record `hack/designs/wcprof-otel-design.md` (current, with the §6.1 update).

## Verdict

**Chunk 2 is sound to build Chunk 3 on, and the Chunks-1+2 trajectory is sound.**
The emit faithfully mirrors §3.1, Invariant T holds exactly, the oracle came online
as planned, and Chunk 1's loader + hardened gate needed **zero change** — the
cleanest possible evidence the chunks compose. I verified locally (temp worktree at
`f127e5662b`):

- `go build ./dagql/... ./cmd/wcprof-oracle/... ./cmd/wcprof-otel-analyze/... ./engine/wcprof/...`: **OK**.
- `go test ./engine/wcprof/wcotel/...`: **ok, 0.039s** — including the 5000-joiner
  cap-stress, so no quadratic blowup.
- **Invariant T**: confirmed by reading `cache.go:3688-3719` — `execSpan` is minted
  under `callsMu` (held since `:3648`) and `oc.execSpanCtx` stashed **before** the
  `c.ongoingCalls[callConcKeys] = oc` publish (`:3723`) and the unlock (`:3751`).
- **`lostcancel` vet warning is pre-existing**: identical warning at base
  `b442cd2533` (`cache.go:3668/3682`), shifted to `:3674/3701` at Chunk 2 — same
  `cancel` var from `context.WithCancelCause`, same lease-error return path. Chunk 2
  did not introduce or worsen it (it added `execSpan.End()` to that path, which is
  correct cleanup). The implementer's claim is accurate.

No correctness bug. Two divergences are both justified (one wants a one-line §3.1
doc reconcile; one wants explicit owner sign-off). Findings below.

---

## (A) Chunk 2 in isolation — §3.1 faithfulness + correctness

**Emit (`dagql/otelprof_hooks.go`, `dagql/cache.go`) — faithful to §3.1/§3.0/§3.0.1:**

- **`call_exec` minted under `callsMu`, threaded into `sharedWorkCtx`** — verified.
  `beginOTelCallExec(callCtx, …)` (`otelprof_hooks.go:54-62`) starts the span on
  `callCtx` (which carries the executor caller's span via `context.WithoutCancel`,
  `cache.go:3672`), reassigns `callCtx`, and `sharedWorkCtx` derives from it
  (`cache.go:3694`) so `fn`'s sub-call spans nest under `call_exec` **regardless of
  AroundFunc suppression** — the structural Break #3 fix. `TestChunk2EmitterNotExecutor`
  proves it: with a suppressed executor, the resolver sub-call's `Parent.ID ==
  call_exec` (`chunk2_test.go:283-289`).
- **Invariant T target publication** — verified (above). The `SpanContext` is
  stashed on `ongoingCall.execSpanCtx` (`cache.go:1772-1778`, `:3716-3719`) before
  publish, so every joiner reading `oc` has a valid wait target. Matches native's
  `oc.profOpID` ordering exactly.
- **Per-caller wait links from `c.wait`** (`cache.go:3899-3917`, `emitOTelCallWait`
  `otelprof_hooks.go:96-118`) — on the waiter's current span via `AddLink`, reason
  `singleflight` (joiner) / `call_exec` (executor), interval as **absolute-unix-ns
  decimal strings** (`strconv.FormatInt`, `:111-115`). Emitted from the cache layer
  (not AroundFunc) so a **suppressed** caller's wait still lands on its ancestor —
  the design's load-bearing fix for Breaks #1–#2. `TestChunk2SingleflightOracle`
  proves the payoff: every joiner's self-time is now ~0 (`chunk2_test.go:213-219`),
  i.e. the joiner's interval is correctly a wait, not fake self-time.
- **`dagql.publishResult`** child of `call_exec` (`cache.go:3972-3976`,
  `beginOTelPublishResult`) — parented under the already-ended `call_exec`
  `SpanContext` carried by `sharedWorkCtx`, created once inside
  `initCompletedResultOnce.Do`, `kind=internal`. Native-parity diagnostic, exactly
  as §3.1 specifies (and the late-child shape correctly contributes no join, per
  the §3.1 reframing).
- **Self-time is not double-subtracted** by the redundant executor edge: the
  executor's `call_exec` child interval and its wait interval overlap, and
  `SelfSegments` subtracts their *union* (`graph.go:379-398`), so the executor edge
  is genuinely "redundant-but-harmless" (self-time identical with or without it) —
  the §3.1 claim is literally true. Both native and OTel emit it, so the oracle
  matches.

**Robustness:** `emitOTelCallWait` guards `!target.IsValid()` and
`!span.IsRecording()` (`otelprof_hooks.go:103-108`), so telemetry-off and
missing-target paths are no-ops; `otelProfActive` (`:36-38`) keeps the
telemetry-off path allocation-free, mirroring `core.AroundFunc`. The waiter span is
still recording when the link is added (the caller's AroundFunc span outlives
`GetOrInitCall`). Error paths end `execSpan`/`pubSpan` (`cache.go:3702-3704`,
`:3730`, `:3985`).

**Performance:** O(1) per cache miss (one `Start`) + O(1) per blocked caller (one
`AddLink`) + one `publishResult` per execution. No superlinear cost; cap-stress at
5000 waits runs in tens of ms. A pathological fan-in span holds up to 16384 links
(~MB), which §3.0 already accepted. One extra `time.Now()` per `c.wait` even when
telemetry is off (`cache.go:3909`) — negligible.

**Simplicity:** the emit is three small functions; the cache integration is a
handful of guarded lines slotted beside the existing native hooks. Not
over-abstracted.

**Validation:** the deterministic oracle (`TestChunk2SingleflightOracle`) is the
real proof — it builds the *exact* emit shape and an equivalent native IR, compiles
both through the loader, and asserts `Oracle(...).Agrees(0.99, 0.02)` with
`call_exec` ranked #1 in both (`chunk2_test.go:237-252`). The known-answer test
(`:355-426`) proves the counterfactual still distinguishes on-path from parallel
work *through the OTel wait edges* (on-path saves ≥25ms, off-path ≤2ms). The
cap-stress test asserts 0 drops at 16384, fan-in modeled as max-not-sum, and that a
dropped link **fails** the gate (`:303-352`). This is a genuinely strong suite.

---

## (B) Holistic — Chunks 1+2 composition + north star

**The chunks compose cleanly — and that's the headline.** Chunk 2 touched only the
emit (`dagql/*`) and added the oracle harness; it changed **neither**
`wcotel/loader.go` nor `wcotel/gate.go`. The Chunk 1 loader already classified
`call_exec` by `wcprof.op.kind`, suppressed the `withExec⇒exec` fallback on a
`call_exec` child, mapped wait links, and routed `publishResult` — so Chunk 2's
real emit dropped straight in. This retroactively validates the Chunk 1 decision to
build the loader against the *full* future emit. The hardened §6.1 gate (Chunk 1
follow-up) now does **real** work on augmented traces: the singleflight oracle test
asserts `UnresolvedWaitTargets==0 && MalformedWaitTimings==0`
(`chunk2_test.go:206-208`) and the cap-stress test exercises the dropped-link
invariant — exactly the under-serialization checks the §6.1 reconciliation added.

**North star intact.** The deterministic singleflight oracle at jaccard=1.00 /
drift=0.00 proves the central fix reconstructs native's bottleneck ranking from the
OTel emit alone — the core thesis ("compile a Cloud trace into the same IR, rank
via the unchanged replay") now has running evidence. User-work-first-class remains
on track for Chunk 4 (the exec split), and nothing here forecloses it.

**The mixed-workload `jaccard=0` is sound, not a regression — with one caveat to
keep asserting.** I agree with the implementer: this is the impl-plan §4-#2 effect.
On a mixed container workload the *bottlenecks* are exec process time, lazy
materialization, and leaf I/O — all of which live in Chunks 3/4 and the §3.5 seam,
so native ranks classes (`exec.processRun`, `lazy`, `withExec.prepareMounts`, …)
the OTel source does not yet emit; disjoint top-N ⇒ jaccard=0. The decisive
evidence it is *not* a Chunk-2 emit bug: the **singleflight-isolating** oracle is
clean (jaccard=1.00), so the emit is faithful for what it covers. The mixed result
is the oracle *working* — drift-localization fingering the next chunks.
**Caveat / what to keep asserting** as Chunks 3/4 land: the divergence must stay
*native-only* (missing classes), with **no OTel-only invented classes** and **no
drift on the shared singleflight classes**. The empirical run claims exactly that
("fingered them precisely"); I can't independently re-run the empirical oracle (it
needs a built augmented engine-dev + a same-run native dump), but the structural
argument + the clean deterministic oracle make it sound. Recommend the empirical
oracle harness print and check `NativeOnly`/`OTelOnly` explicitly each chunk (the
harness already exposes them, `oracle.go:108-130`), so "expected missing classes"
is asserted, not assumed.

No cross-cutting robustness or scaling problem is emerging beyond the always-on
volume posture (divergence #2, below), which is named and bounded.

---

## Divergences (both justified)

### 1. Omitted `dag.call` on `call_exec`/`publishResult` — AGREE; reconcile §3.1

§3.1 lists "`dag.digest` + `dag.call`" on the `call_exec` span; the implementer
emits only `dag.digest` (`otelprof_hooks.go:57-60`). This is correct and I'll
reconcile the doc:
- The loader reads `dag.digest` (ident), never `dag.call` (verified in
  `loader.go` — no `DagCallAttr` consumer).
- The `call_exec` span is `ui.passthrough` (no UI consumer for its `dag.call`
  either; the visible *caller* span already carries `dag.call` from
  `core.AroundFunc:73-86`).
- Re-deriving it means a second `req.ResultCall.CallPB(ctx)` + `Encode()` **per
  cache miss** on the hot path — the expensive serialization, duplicated for no
  consumer.

So `dag.digest` alone is the right `call_exec` payload. **Severity: noise** (correct
as built). **Action (design owner): update §3.1** to specify `dag.digest` only on
`call_exec`/`publishResult`, with the "passthrough, no consumer, avoid double
`CallPB().Encode()`" rationale — a justified-discovery doc reconcile.

### 2. Always-on production telemetry posture — AGREE it follows from the goal; wants sign-off + an escape hatch

Emission is gated on `trace.SpanFromContext(ctx).IsRecording()`
(`otelprof_hooks.go:36-38`), i.e. *whenever telemetry is already on*, **not** behind
a profiling flag. This is **correct for the north star**: "why was my CI run slow
on *any* Cloud trace" requires the data to be present without opt-in, and the design
(§3.1, §4.1) explicitly scopes the cost to cache *misses* (two passthrough spans +
one tiny link per blocked caller; **hits emit nothing**). So the volume is bounded
to executed calls + concurrent joiners, on runs that already pay for telemetry.

That said, this is a genuine production-volume/ops decision and the design proposed
no gate, so the implementer is right to flag it for **explicit owner sign-off**.
- **Volume assessment:** acceptable and bounded — it is an *increment* on already-
  traced runs (roughly +2 passthrough spans per existing executed-call span, plus
  wait links), zero on hits, zero on untraced runs. The real exposure is a cold,
  miss-heavy CI run shipping more passthrough spans to Cloud.
- **Recommendation (MEDIUM, ops-safety):** keep always-on as the default, but add a
  cheap **engine-wide kill switch** (one env var, e.g. `_DAGGER_WCPROF_OTEL=0`)
  gating `otelProfActive`. This is insurance, **not** a per-session opt-in (which
  would defeat the goal): if Cloud volume/cost ever bites, it can be turned off
  without a revert. Worth a one-line note in §3.1/§4 on the posture + the switch.

---

## Issues summary (severity)

- **REAL / MEDIUM (ops):** always-on volume posture needs owner sign-off + an
  engine-wide kill switch as insurance (divergence #2). Not a Chunk-2 defect — a
  product/ops decision the emit forces into the open.
- **REAL / LOW (doc):** §3.1 over-specifies `dag.call` on `call_exec`; reconcile to
  `dag.digest`-only (divergence #1).
- **NOISE / verified-fine:** `lostcancel` (pre-existing, not Chunk 2); the executor
  wait edge is pure redundancy for self-time (intentional, for native parity,
  ~1 link/miss); two same-named spans per executed call (caller + passthrough
  `call_exec`) mirror native and are disambiguated by `wcprof.op.kind`.
- **Forward (carry into later chunks):** keep the empirical oracle asserting
  `NativeOnly == {expected not-yet-built classes}` and `OTelOnly == {}`, so the
  mixed-workload divergence stays *explained* rather than assumed.

## Bottom line

Chunk 2 is faithful to §3.1, correct, robust, performant, and simple; Invariant T
holds; the oracle is online and the deterministic singleflight check converges
exactly. Holistically, Chunks 1+2 compose with zero loader/gate churn and the
trajectory toward the north star is healthy — the only "failure" (mixed-workload
jaccard=0) is the oracle correctly localizing the not-yet-built chunks. **Proceed
to Chunk 3.** Fold the two design reconciles (drop `dag.call` from the §3.1
`call_exec` spec; document the always-on posture + a kill-switch) back into
`wcprof-otel-design.md`, and get Erik's explicit sign-off on always-on telemetry.
```
