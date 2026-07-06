# wcprof × OTel — Chunk 4 review + cycle investigation (by the Chunk 2 implementer)

**Reviewer context:** I built Chunk 2 (the `call_exec` singleflight emit,
`emitOTelWait`'s gate-observable-on-missing-target pattern, the suppressed-caller-
wait-lands-on-the-ancestor rule of §3.1). The cycle is in exactly that machinery,
so Part 2 is where I push hardest. Reviewed Chunk 4 `4d6987fdc2` (on Chunk 3
`107ebe5c0c`, base `b442cd2533`); isolation `git diff 107ebe5c0c..4d6987fdc2`.

**Verified by running** (throwaway worktree at `4d6987fdc2`, now removed): build of
`dagql/... engine/engineutil/... core/... engine/wcprof/...` **OK**; the Chunk 4
fixtures + emit-path + services tests **pass**; full `dagql` regression (the
exports rename) **passes**; `go vet` shows only **pre-existing** `WithTimeoutCause`
lostcancel warnings (confirmed identical at the Chunk 3 base `107ebe5c0c`).

═══════════════════════════════════════════════════════════════════════
## PART 1 — Chunk 4 normal review
═══════════════════════════════════════════════════════════════════════

### Verdict: sound to build Chunk 5 on; Chunks-1–4 trajectory sound.

### (A) In isolation — §3.3/§3.4 faithfulness + correctness

**Exec split (§3.3) — faithful, and it nests under MY `call_exec`.** This is the
hard cross-chunk dependency (`exec.run` is a child of `call_exec`), and I verified
it composes correctly:
- `beginOTelExecRun` (`engine/engineutil/otelprof.go:46-54`) starts `exec.run`
  (kind `exec`, passthrough, `dag.digest`=call digest/exec id) on the executor's
  `ctx` (`executor.go:140-142`). That `ctx` descends from the `withExec` resolver,
  which (per my Chunk 2 §3.1) runs under the `call_exec` span carried by
  `sharedWorkCtx` — so `exec.run` nests under `call_exec` as a genuine synchronous
  child (the caller is blocked through the run), and the implicit join — not an
  explicit wait — serializes it. ✔ Correct, and the right shape for the oracle.
- The `containerStart`(engine)/`processRun`(user) split (`otelprof.go:79-113`) is
  cut at the started-callback boundary (`executor_spec.go:1407-1426`), with a new
  `profStartedWall` atomic capturing the wall-clock boundary alongside native's
  `profStartedNS`; the spans are backdated with explicit `WithTimestamp` so they
  carry the true `[start,started]` / `[started,end]` intervals. `work_type=user`
  is on `processRun` **only** (`otelprof.go:117-121`) — exactly right; marking the
  whole run "user" would mislabel the non-sub-ms container-setup tax. The
  `started.IsZero()` setup-failure case correctly degrades to a single
  `containerStart` over the whole interval charged the error (`otelprof.go:95-101`),
  mirroring native. ✔
- "Lazy-triggered exec composes for free": agreed — a lazy-materialized `withExec`
  runs its executor under the lazy callback ctx (Chunk 3), whose `call_exec`
  descendant carries the span, so `exec.run` nests correctly with no extra work. ✔

**Services (§3.4) — faithful, Invariant T holds.** `beginOTelServiceStart`
(`services.go:206-221`) is minted under `ss.l` and stashed on
`startingService.otelStartSpanCtx` (`services.go:1027-1045`) **before** the
`ss.starting[key] = start` publish (`services.go:1046`) — the same ordering I used
for `call_exec`/`oc.execSpanCtx`. Installer wait edges (`services.go:996-1013`)
credit the blocked interval to `service.start` via my exported `EmitOTelWait` on
both the `ctx.Done()` and `<-starting.done` branches, and `endOTelServiceStart` is
called on every start-exit path (error/no-Wait/canceled/OK), nil-safe. The start
span scopes the start+health-check window, leaving the long-lived service span a
passthrough availability marker — so the idle daemon does not rank (§3.4). ✔ The
comment even carries the cross-session gate-observable note correctly (no
per-attempt reset needed: a fresh `startingService` per start). ✔

**Robustness/perf/simplicity:** three passthrough spans per container run, one per
service start, all on paths already starting a container/service — bounded.
`profStartedWall` is one unconditional atomic per run (cheap). `endWall` captured
once. No degenerate shape. Error paths end every span. The `lostcancel` warnings
are pre-existing (`WithTimeoutCause` at `executor.go:602/669`, `services.go:918` —
far from the Chunk 4 hunks, identical at the Chunk 3 base).

### (B) Holistic — Chunk 4 reuses my Chunk 2 emit faithfully

- **`OTelProfActive` / `EmitOTelWait` are exported byte-identically** (`otelprof_hooks.go:34-44,103-126`):
  the rename is a pure export (the bodies are unchanged), so the executor exec
  emit and the services installer waits gate on the *same* "is OTel recording
  here?" predicate and emit the *same* wire format I defined — including the
  gate-observable-on-missing-target behavior I added in the Chunk 2 convergence
  pass. The comment even documents the cross-session targetless case for services.
  One implementation, every source identical on the wire. ✔
- `exec.run` under `call_exec`, installer waits to `service.start`, lazy-triggered
  exec composing — the cumulative graph is coherent, and the chunks still touch
  **no** replay/loader/gate (`git diff --name-only` confirms additive-only). North
  star: **user-work is now first-class** (a slow `go build` headlines as
  `processRun`/`work_type=user`), which was the Chunk 4 milestone. On track.

**Part 1 issues:** none REAL beyond the cycle (Part 2). NOISE: pre-existing
`lostcancel`. Severity: clean.

═══════════════════════════════════════════════════════════════════════
## PART 2 — The cycle (independent verdict; this is my area)
═══════════════════════════════════════════════════════════════════════

I did **not** take the implementer's analysis on faith. I re-derived the mechanism
from the replay/graph code, my own `call_exec`/suppressed-wait emit, and the
timestamps in the report. My conclusion: **the implementer's core theory is
correct — a real graph cycle that is an over-serialization artifact, not a replay
bug and not Chunk 4 — and I can sharpen both the mechanism and the disposition in a
way that changes what a fix should (and must not) do.**

### 1. Cause — confirmed, and sharpened: the WAIT is true, the NESTING is the lie

The cycle is `op#54 --wait:singleflight--> op#94` (closing edge) plus an implicit-
join chain `op#94 ⇒ … ⇒ op#54`. Two facts from the report's own timestamps settle
which edge is false **without needing the raw trace**:

- **op#94 `[160..195]`, op#54 `[160..205]` — op#54 outlives op#94 by 10ms.** A
  *synchronous* parent cannot end before a child it is blocked inside. So op#54 is
  **not** synchronously nested under op#94; it is **detached/concurrent** work whose
  OTel subtree merely *descends from* op#94 via context propagation. That is exactly
  the design §1.1/§2.2 hazard ("OTel parentage is context propagation, not the live
  call stack; detached/concurrent work nests anyway and the implicit join invents a
  dependency"). **The implicit-join edge `op#94 ⇒ … ⇒ op#54` is the lie.**
- **The wait ends at 195 = op#94's end** → `actWaitJoin` (`replay.go:162-167`:
  `w.EndNS >= w.Target.EndNS - ε`, here exact). This is a **genuine** join: op#54
  (an execution of module-source *A*) really did block until op#94 (execution of
  module-source *X*) finished, because A's resolver needed X and singleflight-joined
  op#94's in-flight execution. **The wait edge is TRUE.** (The ε-boundary the lead
  asked about is a red herring — the equality is a real join, not a borderline
  misclassification; reclassifying it `actWaitNoop` would *hide* a real dependency.)

**The precise emit mechanism — and it runs through my Chunk 2 code.** Why is the
wait attributed to op#54's `call_exec` (making a `call_exec` op the *waiter*)?
Because op#54's resolver makes a **suppressed concurrent-duplicate** sub-call to
module-source X: X is being executed by op#94, so X's digest is already "seen" in
the session → `ShouldEmitTelemetry` suppresses that sub-call's caller span → its
singleflight wait lands on the **nearest recording ancestor = op#54's `call_exec`
span**. That is precisely my §3.1 rule ("if the caller was telemetry-suppressed,
the waiter is the ancestor that actually blocked", `dagql/cache.go` `c.wait` →
`EmitOTelWait(ctx, oc.execSpanCtx, …)`). Combined with op#54 being a (detached)
descendant of op#94, the wait is a back-edge into op#94's own subtree → cycle.

So the loop is: my **true** suppressed-joiner wait edge (load-bearing — it is the
fix for Break #1) closing over a **false** detached-concurrent nesting edge. The
replay's `inFlight` cycle-break (`replay.go:341-345`) is the safety valve; the §6.1
gate flags it.

### 2. Truly not Chunk 4 — yes, and I'd still close the loop with a rebuild

Conclusive enough to rule Chunk 4 out: (a) the strip test (remove all Chunk 4 ops →
identical 5 cycles/18 anchors); (b) the Chunk 4 diff touches no `replay.go`/loader/
gate (I verified — additive emit only); (c) every cycle node is `call_exec`/`call
Query.moduleSource`, with `exec.run`/`processRun` as **leaf** children that add no
back-edges; (d) module-free exec/service traces (which *do* emit Chunk 4 ops) are
cycle-free. **It is a Chunk 2 (singleflight) × module-loading interaction, not Chunk
4.** I'd still endorse the implementer's option-4 (rebuild at Chunk 3 HEAD and
re-capture) as the *definitive* close — the strip test proves it from the data, the
rebuild proves it from source — **and** I'd ask for the raw parentId chain
`op#94→…→op#54` to confirm op#54 descends from op#94 (the timestamps already imply
the detached-concurrent signature, but the chain makes "nested" explicit).

### 3. It is a Chunk 2 gap — a real one, and bounded in scope

§3.1 states the assumption verbatim: *"the joiners are in a different subtree (the
execution is not nested under them)."* Concurrent module-source loads **violate
it**: op#94's resolver spawns op#54's load (concurrently, detached) on op#94's
context, so the *joiner* (op#54) is nested **under** the execution (op#94), and my
suppressed-wait-on-ancestor rule then lands the wait on a span that sits *inside the
target's subtree*. The design anticipated the hazard **class** (§2.2) but not this
**specific** shape (a suppressed joiner whose ancestor is the target's descendant).

**Scope:** it needs a resolver to spawn **detached concurrent** sub-work that
**singleflight-joins back** — characteristic of module-loading / nested-client /
parallel-shared-work, where loads run in parallel under a shared context. Ordinary
synchronous core-API singleflight (sub-calls the resolver blocks on) does **not**
hit it: there the nesting is genuine (parent outlives child) and the implicit join
is correct. So it is unlikely to "bite any sufficiently-concurrent singleflight" —
it bites *concurrent shared-work loading*, which module/nested-client loads are the
archetype of. But it is **common** (any `dagger call` on a module loads modules), so
it is not an exotic edge.

### 4. Disposition — §9 seam for v1 is defensible, with three load-bearing caveats

**A. Do NOT "drop/suppress the singleflight wait."** That wait is TRUE and
load-bearing — dropping it re-introduces Break #1 (op#54's join-time becomes fake
self-time; X's cost stops propagating to A's critical path). The lie is the
**nesting**, not the wait. Any fix must break the false *nesting*, never the true
*edge*. (This is the single most important thing for the next implementer to get
right; the naive "reclassify/suppress the back-edge wait" is wrong.)

**B. The real fix lives at the module-loading / concurrent-spawn emit, not in my
`call_exec`.** My `call_exec`→`sharedWorkCtx` threading is the Break #3 fix and
*must* keep nesting synchronous resolver children; it cannot distinguish
synchronous from detached sub-work. The faithful fix is for the code that spawns
**concurrent** module loads to detach their OTel causal parent — exactly analogous
to the §3.2 lazy re-point: keep the UI nesting if desired, but emit a `wcprof.parent`
override (or re-root) so the loader does **not** treat the detached concurrent load
as a synchronous descendant. That removes the false implicit-join edge at the
source and the cycle never forms. **This is a new design item (a §3.x "detached
concurrent shared-work" rule), squarely owned by module-loading/nested-client — not
fixable inside Chunk 2's `call_exec` and not in Chunk 4.** *(A cheaper emit-side
detector — track in-flight `call_exec` span ids in a context set and, in `c.wait`,
notice the target is an ancestor of the waiter — can identify the case, but the
right response is still to re-root the nesting, not drop the wait, so it does not
avoid the module-loading change.)*

**C. The §6.1 `CycleWarnings == 0` HARD invariant cannot stand for module-loading
as-is.** The gate hard-fails on any cycle ("a cycle ⇒ unfaithful emit"). If common
module workloads always cycle, then every such trace gate-fails and the §6.4
standing CI gate rejects them. The cycle-break does yield a *bounded* result, but
note it **specifically over-credits the `moduleSource` `call_exec` class** (op#94's
simulated finish absorbs op#54's broken finish), so the bottleneck ranking is
distorted *upward* for that class — "approximately correct" with a known directional
bias, not neutral. So accepting the seam **requires** contextualizing the gate
(e.g. cycle invariant relaxed/annotated for traces with detached-concurrent
shared-work, until the §3.x fix lands) — which is a real weakening to flag to the
owner, the cost of deferring rather than fixing.

**My recommendation:** accept as a documented §9 reserve seam for **v1** (the
cycle-break + a *contextualized* gate make it loud and bounded, not silently wrong),
**and** open the §3.x "detached concurrent shared-work re-root" design item as the
real fix (target it before the §6.4 standing gate is trusted on module workloads,
i.e. with/around Chunk 5). Reason from the strip test + timestamps is enough to be
confident in the mechanism; I'd want the rebuild + raw cycle subgraph only to make
the write-up airtight, not to change the verdict.

═══════════════════════════════════════════════════════════════════════
## Bottom line
═══════════════════════════════════════════════════════════════════════

1. **Chunk 4 is sound to build Chunk 5 on.** The exec split and services are
   faithful to §3.3/§3.4, Invariant T holds for `service.start`, `exec.run` nests
   correctly under my `call_exec`, and the exported `OTelProfActive`/`EmitOTelWait`
   reuse my Chunk 2 wire format byte-identically — user-work is now first-class.
2. **The Chunks-1–4 trajectory is sound**, with the module-loading cycle as a
   known, flagged limitation (not a regression, not Chunk 4).
3. **My independent cycle verdict:** a **real graph cycle** = a TRUE suppressed-
   joiner singleflight wait (mine, load-bearing) closing over a FALSE detached-
   concurrent nesting edge (the §2.2 lie, proven by op#54 outliving op#94). **Not a
   replay bug, not Chunk 4** — a Chunk-2 §3.1 "different subtree" gap exposed by
   concurrent module/nested-client loading. **Disposition:** §9 reserve seam for v1
   **iff** the §6.1 cycle invariant is contextualized for this case; the real fix is
   a new §3.x "re-root detached concurrent shared-work" emit rule at the module-
   loading side (NOT dropping the true wait, NOT changing `call_exec`). Confirm with
   a Chunk-3-HEAD rebuild + the raw cycle subgraph.
