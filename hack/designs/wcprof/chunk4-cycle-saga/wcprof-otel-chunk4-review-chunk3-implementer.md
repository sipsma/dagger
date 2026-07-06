# wcprof × OTel — Chunk 4 review (by the Chunk 3 implementer)

**Reviewer context:** I implemented Chunk 3 (lazy / deferred eval + the
`wcprof.parent` stamping processor). Two-part review of Chunk 4 `4d6987fdc2`
(exec engine/user split §3.3 + services §3.4) against Chunk 3 `107ebe5c0c` and
base `b442cd2533`, **plus** an independent investigation of the module-loading
cycle (Part 2) — which lands partly in my lazy/nested-client territory.

**Verified by reading the diff + the analysis path; I did not run the live
module workload (no access to the 9.3 MB trace / the implementer's container).**
Where a conclusion needs the raw trace or a from-source rebuild, I say so.

## Headline verdicts

1. **Chunk 4 is sound to build Chunk 5 on.** The exec split and service-start emit
   are faithful to §3.3/§3.4, Invariant T holds for `service.start`, the emit is
   purely additive (no analysis-path change), and the deterministic fixtures are
   strong. One **LOW gap**: no committed *lazy-triggered exec* composition test
   (Part 1B) — the very composition the module workload exercises. Recommend adding;
   not a blocker.
2. **Chunks-1–4 trajectory is sound.** The loader/gate are unchanged a 4th time;
   `wcprof.op.kind=exec`/`work_type` were defined inert in Chunk 1 and are now
   consumed; user-work-first-class (§3.3 `processRun`, `work_type=user`) is achieved.
3. **The cycle is a REAL over-serialization artifact (not a deadlock, not a replay
   bug) — and I AGREE it is not Chunk 4.** But the implementer's attribution
   ("Chunk 2 / module-loading / nested-client") is **incomplete: it did not rule out
   Chunk 3.** Module loading uses lazy eval, the OTel causal tree is purely
   `wcprof.parent ?? parentId`, and my stamping *could* be the attachment mechanism
   that places one peer in the other's subtree. My stamping is **not the root cause**
   (it emits no singleflight wait, and was designed to *reduce* the producer-based
   cycle class), but whether it is *in the chain* is **undetermined** and should be
   settled by one cheap loader-only diagnostic before "Chunk 2 only" is finalized.
   Disposition: a **§9 reserve seam for v1** (gate-flagged + replay-broken loudly,
   bounded), not a Chunk 4 blocker — but it **will** bite Chunk 5's §6.4 standing
   gate on any module-loading workload, so Chunk 5 must account for it.

---

## PART 1A — Chunk 4 in isolation (§3.3/§3.4)

### Exec split (§3.3) — faithful, verified

`engine/engineutil/otelprof.go` + `executor.go:113-162` + `executor_spec.go:1268-1432`:

- **`exec.run` nests under `call_exec`.** `beginOTelExecRun` (`otelprof.go:42-50`)
  starts on the executor `ctx`, which descends from the withExec resolver's
  `call_exec` `sharedWorkCtx` (the resolver runs the executor synchronously, §3.1),
  so the OTel parent of `exec.run` is the `call_exec` span. ✔ Matches §3.3 / native
  `OpKindExec`. It is a genuine synchronous nesting (the caller is blocked through
  the run), so the implicit join — not a wait edge — serializes it (Invariant E).
- **The `containerStart`/`processRun` split at the started-callback** is correct
  (`otelprof.go:80-117`, driven from `executor_spec.go:1403-1432`):
  `[start, started]` engine (no error), `[started, end]` user (`work_type=user`,
  carries the run error). **Never-started case** (`started.IsZero()`): only
  `containerStart` over `[start, end]` charged the error — mirrors native exactly
  (`executor_spec.go:1411-1419`). ✔ `work_type=user` is on `processRun` ONLY
  (`otelprof.go:108-110`), so engine setup is not mislabeled user work. ✔
- **Timestamps** are backdated via `trace.WithTimestamp` (`otelprof.go:111-116`) —
  correct, the boundary is known only in retrospect, exactly as native records from
  stored nanos. The new `profStartedWall` atomic (`executor_spec.go:1271-1281`)
  captures the started-callback wall-clock at the same boundary as native's
  `profStartedNS`. ✔
- **`execIdent` hoist** (`executor.go:116-119`) so both native + OTel share the
  same `CallDigest||state.id` ident — clean, and `exec.run` carries it for oracle
  matching while the phase spans use `state.id` (matching native's per-phase
  ident). ✔

**§3.3 verdict: faithful.** The `TestChunk4ExecSplitFidelity` fixture
(`chunk4_test.go:87-205`) is strong: both phases, work_type, headline-fingers-the-
slow-phase, §6.2 oracle vs native (jaccard=1.0/drift=0.01), identity-level self-time
match.

### Services (§3.4) — faithful, Invariant T verified

`core/services.go`:

- **Invariant T for `service.start`.** `beginOTelServiceStart` (`services.go:206-222`)
  is minted under `ss.l` and the resulting `start.otelStartSpanCtx` is stashed
  **before** `ss.starting[key] = start` is published (`services.go:1031-1046`), so an
  installer that joins always has a valid target. ✔ Mirrors the native `profOpID`
  ordering and my Chunk 2/3 Invariant-T pattern.
- **Installer wait links** (`services.go:996-1011`) emit `reason=service` to
  `starting.otelStartSpanCtx` via the shared `dagql.EmitOTelWait`, on both the
  `done` and `ctx.Done` paths — the analog of native's `BeginWait`. ✔
- **Idle availability not mistaken for work.** The long-lived `exec <args>` span
  stays passthrough; its child `exec.run` absorbs the daemon's idle run, so the
  service span's own self-time is the modest setup gap. The
  `TestChunk4ServicesFidelity` fixture (`chunk4_test.go:209-326`) proves the idle
  daemon has the **most self-time** (105 ms) yet does **not rank** (off the critical
  path → 0 saved), while the on-path consumer does — the "total time ≠ bottleneck"
  property. ✔ It also confirms the §6.1 gate stays green with the long-lived daemon
  (daemon self 105 ms ≤ makespan 130 ms — see the §3.4 robustness note below).

- **Excellent holistic catch by the implementer:** the `otelStartSpanCtx` doc
  (`services.go:60-72`) explicitly reasons that the stale-target bug I fixed in
  Chunk 3 (`lazyEvalSpanCtx`) does **not** apply here, "because each start gets a
  fresh `startingService`, deleted from `ss.starting` when it completes." I verified
  this: `delete(ss.starting, key)` on every completion path (`services.go:1058,
  1071, 1078`), so a joiner reading `starting.otelStartSpanCtx` (under `ss.l`, while
  `isStarting`, `services.go:970`) always reads the *current* start — never a stale
  one. The contrast with lazy (where the field is reused on the same `sharedResult`
  across retries) is exactly right. This is careful cross-chunk reasoning.

### Exported helpers — clean

`otelProfActive → dagql.OTelProfActive`, `emitOTelWait → dagql.EmitOTelWait`
(`dagql/otelprof_hooks.go:29-44, 86-118`): byte-identical bodies, exported so
`engineutil` (§3.3) and `core` (§3.4) gate on the *same* condition and emit the
*same* wire format. One canonical gate + wait-edge format across all four sources —
the right call. The loader needs no change (it already classifies `exec` by
`wcprof.op.kind` and reads `work_type`). ✔

### Robustness / perf / simplicity — NOISE-level notes

- **NOISE — unconditional `time.Now()` + atomic store in `startedCallback`**
  (`executor_spec.go:1278-1281`): runs even when telemetry is off (native's is gated
  on `wcprof.Enabled`). Negligible — one `time.Now()` + atomic per *container start*,
  a path already doing far more expensive work. The implementer flagged it.
- **LOW — long-lived-daemon gate headroom.** The services fixture's daemon self
  (105 ms) is < makespan (130 ms), so the "no op self-time > makespan" invariant
  (`gate.go`) holds. But a daemon that runs for *nearly the whole trace* would have
  `processRun` self approaching makespan; it stays ≤ makespan (so no gate fail), but
  it's the closest any op gets to that bound. Worth a sentence in §3.4 that a
  service daemon's `processRun` is the op most likely to approach the self≤makespan
  bound, and the §6.4 standing-gate workload should include a real long-lived
  service to confirm it never crosses. Not a blocker.
- **Simplicity:** appropriate — one exec-emit file, three small service hooks, the
  helper exports. Not over-abstracted.

**Part 1A bottom line: no blocking issues; §3.3/§3.4 faithfully implemented and
well-tested deterministically.**

---

## PART 1B — Holistic: the "lazy-triggered exec composes for free" claim

This is my sharpest angle (the discriminator is mine). The claim: a `withExec`
returns a *pending* Container, the exec runs during lazy eval, so the exec subtree's
`parentId` chain roots at the producer, and my Chunk 3 stamping re-homes it to the
lazy op — with no change to my processor.

**It is faithful by construction — verified by reasoning through the discriminator,
but NOT covered by a committed test (the gap).**

The discriminator (`dagql/otelprof_lazy.go` `OnStart`) stamps `wcprof.parent` on a
span iff `s.Parent().SpanID() == producerSpanID` — the *direct* re-pointed children
only. In the lazy-triggered exec, the deferred work runs under the callback ctx
(current span = `resumedCallbackSpan`, whose `SpanContext()` is the producer). So:

- The **direct** re-pointed child (whatever its kind) is stamped → re-homed to the
  lazy op. The discriminator is kind-agnostic, so a `call_exec` or `exec.run` that is
  itself the direct child is stamped correctly (the lead's specific worry).
- Its **descendants** — whether `caller → call_exec → exec.run → processRun`
  (if the lazy callback re-resolves the withExec call) or `exec.run → processRun`
  (if exec.run is the direct child) — keep their real `parentId` (≠ producer) and
  fall through unstamped, following the stamped ancestor. So the **whole** exec
  subtree re-homes under the lazy op via the single stamp + `parentId` descent.

Chunk 4's exec spans are leaves: `exec.run` under `call_exec`, phases under
`exec.run`, all by `parentId`, none with their own `wcprof.parent` — so they
compose with my discriminator without any change to it, exactly as the lead and the
Chunk 1 implementer's forward note (Chunk 3 review) predicted. The Chunk 4 phase
spans' `trace.WithTimestamp` does not affect `OnStart` parent-ctx propagation (the
SDK still passes the start ctx; the parent is still the recorded parent).

**LOW gap (REAL):** there is **no committed unit test** for this composition — the
Chunk 4 tests contain zero `lazy`/`wcprof.parent`/`resume` references, and my Chunk
3 tests predate `exec.run`. So "composes for free" rests on reasoning + the empirical
module run — and the empirical run is precisely where the cycle appeared. Recommend
a committed emit-path test (the analog of my `TestLazyEmitNestedOverrideNoCrossStamping`
but with a lazy-triggered `exec.run`/`processRun`): assert the exec subtree re-homes
under the lazy op (direct child stamped, `exec.run`/phases unstamped descendants),
loads cleanly, gate green. This is cheap and would lock down the composition that the
module workload stresses.

---

## PART 2 — The cycle (independent investigation; NOT trusting the analysis doc)

I re-derived the cycle from the described structure + the code/design, rather than
accepting the implementer's theory.

### 2.1 The cycle is a real over-serialization artifact — AGREE

The replay's `finish(op)` recursion (`replay.go`) joins an op's causal children
(`joinUpTo`, `:353-369`) and follows wait edges. The two ingredients:

- **Real edge `op#54 → op#94` (Chunk 2 singleflight):** op#54's resolver sub-calls
  shared work that is in-flight as op#94; it joins via `c.wait` → an
  `emitOTelWait(reason=singleflight)` to `op#94`. The closing edge
  (waiter `[160,205]`, target `[160,195]`, `waitEnd=195 ≥ targetEnd=195`) classifies
  `actWaitJoin` (`replay.go:165-167`). This is a genuine dependency. ✔
- **Reverse edge `op#94 ⇒ … ⇒ op#54` (implicit join over the causal subtree):** for
  `finish(op#94)` to recurse into `op#54`, op#54 must be a **causal descendant** of
  op#94 (its `ParentID` chain reaches op#94). The recursion re-enters op#94 via
  op#54's singleflight wait → `inFlight[op#94]` true → `CycleWarnings++` and the
  cycle-break assumes recorded duration (`replay.go:341-345`).

The workload completed (rc=0) ⇒ no real mutual dependency ⇒ one edge is an artifact.
The artifact is the implicit-join edge: it infers op#94 *synchronously waited for*
op#54 from the fact that op#54's span *nests in* op#94's subtree — but OTel parentage
is built from **context propagation, not the live call stack** (§1.1/§2.1), so for
concurrent/detached work the nesting does not imply synchronous waiting. This is the
**§1.1/§2.2-anticipated hazard**, and the replay's cycle-break + the §6.1 gate are the
designed safety valve/alarm. **Not a deadlock, not a replay bug — agreed.**

### 2.2 Is it Chunk 4? — NO, agreed (and code-confirmed)

- **Strip test** (Chunk 4 ops removed → identical 5 cycles + 18 fallback anchors):
  data-level evidence the cycle is independent of Chunk 4 emit.
- **Code confirms it:** Chunk 4's `exec.run`/`containerStart`/`processRun` are
  **leaf children** of `call_exec` (`otelprof.go`, `executor.go:140-162`) and emit
  **no wait edges** — they cannot add the back-edge that closes a loop. `service.start`
  *does* emit waits, but the cycle is `Query.moduleSource` `call_exec`s (Chunk 2),
  not services.
- **No analysis-path change:** `git diff --name-only 107ebe5c0c..4d6987fdc2` touches
  no `replay.go` / `wcotel/loader.go` / `wcotel/gate.go` / `graph.go` — purely
  additive emit + the helper rename. Confirmed.

The strip test proves it from the data; a from-source rebuild at Chunk 3 HEAD (no
Chunk 4 binary at all) would be belt-and-suspenders. Given the leaf-children code, I
do **not** consider the rebuild strictly necessary to exonerate Chunk 4 — but it is
the clean way to close the "is it Chunk 4" loop if the owner wants zero doubt.

### 2.3 Is Chunk 3 (my stamping) involved? — UNDETERMINED; the implementer did NOT rule it out

This is my key independent finding. The implementer attributes the cycle to "Chunk 2
/ module-loading / nested-client" but **only ruled out Chunk 4** (the strip test
removed Chunk 4 ops, not Chunk 3 stamps). Two facts make Chunk 3 a live possibility:

1. **The OTel causal tree is purely `wcprof.parent ?? parentId`.** For OTel there are
   **no `nested_client` link events**, so the loader's nested-client reparent
   (`graph.go:270-294`) never fires (design §5 — OTel nests nested clients via
   traceparent, the `nested_client`/`result` link handling "goes unused"). I confirmed
   the wiring: `op.Parent` is set from `op.ParentID` (= `wcprof.parent ?? parentId`)
   and the `nestedClientExec` fallback only triggers when `ParentID` doesn't resolve.
   **So every edge in the cycle's nesting chain is either raw `parentId` (Chunk 2
   call_exec-under-caller, or nested-client traceparent) OR a `wcprof.parent` stamp
   (mine).**
2. **Module loading uses lazy evaluation.** If `Query.moduleSource` (or a step in the
   "load module:" chain) returns a pending result forced during loading, the lazy
   callback's deferred work is re-pointed under the producer and my processor stamps
   the *direct* child with `wcprof.parent → the lazy op`. The moduleSource `call_exec`
   would then be a descendant of that stamped caller — i.e. **a `wcprof.parent` edge
   sits in op#54's ancestor chain**, and that edge is what attaches op#54 (via the
   lazy op, under the *consumer*) into op#94's subtree.

The decisive subtlety: my stamping **relocates** the attachment point of lazy work
from the *producer* (raw `parentId`) to the *consumer-side lazy op* (`wcprof.parent`).
That relocation can move op#54 **into or out of** op#94's causal subtree depending on
whether the producer vs. the consumer sits under op#94. So my stamping could, in
principle, be **creating** this specific cycle (by re-homing op#54 under a consumer
that is in op#94's subtree) **or hiding** a different one (the producer-already-ended
cycle of §2.5, which my stamping was *designed* to prevent) — I cannot tell which
from the described structure alone.

What I *can* state firmly:

- **My stamping is not the root cause.** The cycle's necessary first ingredient is
  the **Chunk 2 singleflight wait** (`op#54 → op#94`); Chunk 3 emits no singleflight
  waits. Chunk 3 alone cannot form this loop.
- **My stamping was designed to *reduce* the cycle class, not add to it.** §2.5/§3.2:
  re-homing lazy work to the consumer-side lazy op is precisely what avoids the
  "work parented to an already-ended producer" cycle. Without it, raw `parentId`
  would dangle moduleSource execs under ended producers (more fallback anchors /
  different cycles).
- **But "Chunk 2 only" is not yet established.** The honest status is: root cause =
  Chunk 2 singleflight + call_exec-under-caller nesting (the §3.1 "joiners in a
  different subtree" assumption violated by concurrent module-source cross-joins);
  *attachment mechanism* = either raw `parentId` or my `wcprof.parent` — **unverified.**

**The cheap diagnostic that settles it (loader-only, no engine rebuild):** recompile
the captured cycle trace with `wcprof.parent` **ignored** (force the loader to use raw
`parentId`). If the 5 cycles persist identically → my stamping is not load-bearing
for the cycle (pure Chunk 2 / nested-client nesting), and "Chunk 2 only" is confirmed.
If they change/disappear → my `wcprof.parent` re-homing is the attachment mechanism,
and the fix space includes Chunk 3. Even simpler if the trace is re-runnable: have the
loader/gate report, per cycle op, whether its `ParentID` came from `wcprof.parent` or
raw `parentId`. I recommend running one of these before finalizing the attribution —
it is the difference between "a Chunk 2 nesting seam" and "a Chunk 2+3 interaction."

(The 18 **fallback anchors** are most likely the same module-loading structure;
note my stamping should, if anything, *reduce* them vs. producer-already-ended raw
`parentId`. They are a report-only regression metric, not the gate failure.)

### 2.4 In scope or a seam? Disposition

- **Anticipated class, specific case unhandled.** §1.1/§2.2 name the hazard; §3.1's
  singleflight model explicitly *assumes* "the joiners are in a different subtree."
  Concurrent module-source loads that cross-join **through nesting** violate that
  assumption — a Chunk 2 / module-loading / nested-client gap (with the open Chunk 3
  attachment question above).
- **It surfaces loudly, not silently.** The §6.1 gate **fails** on it (5 cycles) and
  the replay cycle-break yields an approximately-correct (recorded-duration) result.
  This is exactly the designed behavior for an unfaithful nesting — not a silent
  mis-attribution. That materially lowers the severity.
- **Recommended disposition: a documented §9 reserve seam for v1, NOT a Chunk 4/5
  emit blocker** — *conditioned on* the §2.3 diagnostic first confirming the
  attachment mechanism (so the seam is described accurately as Chunk 2-nesting vs.
  Chunk 2+3). Rationale: the root cause is Chunk 2's singleflight nesting model, not
  Chunk 4; the gate+cycle-break handle it; and the faithful fix is genuinely hard
  (you must either avoid call_exec-under-caller nesting when the execution is
  detached/shared — but that nesting is load-bearing for the executor-caller's real
  synchronous case, §3.1 — or detect, at wait-emit time, that a singleflight target
  is also an implicit-join ancestor and suppress/reclassify the wait, which needs
  nesting knowledge the emit site doesn't have). That is its own investigation, not a
  one-line fix.
- **Chunk 5 caveat (important):** this is **not** free of Chunk 5 consequences. The
  §6.4 standing drift gate is meant to run on a *representative complex* workload
  (the design explicitly names "a module pipeline with services + lazy dirs + nested
  clients"). On any module-loading workload the gate will **fail on these cycles**.
  So Chunk 5 must either (a) land the emit fix, (b) use a cycle-free representative
  workload for the standing gate and document the module-loading cycle as a known
  excepted hazard, or (c) teach the gate to except this specific class. That is a
  Chunk 5 design decision the owner should make consciously — flagging it now.

---

## Severity summary

- **REAL / MEDIUM — module-loading cycle.** Over-serialization artifact (gate-flagged,
  replay-broken, bounded). Not Chunk 4. Root cause Chunk 2 singleflight + nesting;
  **Chunk 3 attachment involvement undetermined — run the `wcprof.parent`-ignored
  loader diagnostic before concluding "Chunk 2 only."** Disposition: §9 reserve seam
  for v1, with a Chunk 5 §6.4 caveat. Not a Chunk 4 blocker.
- **REAL / LOW — no committed lazy-triggered-exec composition test** (Part 1B): the
  "composes for free" claim is reasoned + empirical only, and the empirical path is
  where the cycle lives. Recommend adding a committed emit-path test.
- **LOW — long-lived-daemon gate headroom** (§3.4): `processRun` is the op closest to
  the self≤makespan bound; §6.4 workload should include a real long-lived service.
- **NOISE / verified-fine:** unconditional `time.Now()`+atomic per container start;
  exec spans are leaves (no back-edges); `service.start` Invariant T; services'
  fresh-`startingService`-no-stale-target reasoning (correct, and a good catch of my
  Chunk 3 bug's non-applicability); helper exports byte-identical; `nested_client`
  reparent unused by OTel.

## Bottom line

Chunk 4 faithfully lands the last two choke points: the exec engine/user split makes
user work first-class (`processRun`/`work_type=user`), and `service.start` is
Invariant-T-correct with the idle daemon proven not to rank. It composes with my
Chunk 3 stamping (the exec subtree re-homes under the lazy op for free) — though that
composition wants a committed test. **Proceed to Chunk 5.** The cycle is a real,
bounded, loudly-flagged over-serialization artifact that is **not** Chunk 4's doing;
the implementer correctly exonerated Chunk 4 but stopped short of ruling out my Chunk
3 stamping as the *attachment mechanism* — settle that with the cheap
`wcprof.parent`-ignored loader recompile, then record it as a §9 reserve seam with an
explicit Chunk 5 §6.4 standing-gate caveat.
