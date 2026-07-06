# wcprof × OTel — Chunk 4 review + independent cycle investigation (by the Chunk 1 implementer)

**Reviewer context:** I built Chunk 1 (loader, hardened §6.1 gate, vocabulary).
Reviewed `4d6987fdc2` against Chunk 3 `107ebe5c0c` (`git diff 107ebe5c0c..4d6987fdc2`)
and base `b442cd2533`. The cycle is squarely in my gate's territory, so Part 2 is
an independent investigation, not a re-read of the implementer's doc.

**Verified by running** (throwaway worktree at `4d6987fdc2`): `go test
./engine/wcprof/wcotel/` **green** (chunk2/3/4 + loader/gate/oracle); the
**emit-path** tests `./engine/engineutil/` and `./core/` **green**;
`TestChunk4ExecSplitFidelity` + `TestChunk4ServicesFidelity` **PASS**; `go build`
of `engineutil`/`core`/`dagql` **OK**.

## Plain verdict

1. **Chunk 4 is sound to build Chunk 5 on.** Faithful exec split + service start,
   Invariant T correct, `work_type=user` delivers the user-work-first-class
   headline, no degenerate perf.
2. **The Chunks-1–4 trajectory is sound *mechanically*** (loader unchanged a 4th
   time; gate green on module-free exec/service paths) — **but it now has one
   critical-path gap: the OTel source is not yet faithful on *module-loading*
   workloads**, which are the north star's primary case (CI runs load modules).
   The gate is correctly red there.
3. **Independent cycle verdict:** the gate is **right** — it is flagging a genuine
   over-serialization from an unfaithful (non-synchronous) nesting; the loader is
   **not** manufacturing it. It is **not Chunk 4** (high confidence). Disposition:
   a real **Chunk 2 × module-loading/nested-client** faithfulness gap that is
   **too central to defer as a clean §9 reserve seam** — it must be fixed, or its
   ranking impact measured and bounded with a principled gate accommodation,
   before v1 can claim "why was my CI run slow?" on real CI.

---

# PART 1 — Chunk 4 review

## (A) In isolation — faithful

**Exec split (§3.3) — correct.** `beginOTelExecRun` (`engineutil/otelprof.go:46`)
starts `exec.run` (`wcprof.op.kind=exec`, passthrough) on the executor ctx, which
descends from the withExec resolver's `call_exec` `sharedWorkCtx` (Chunk 2), so
`exec.run` nests under `call_exec` — I verified the wiring at
`executor.go:133`–`164` (the `ctx, execRunSpan = beginOTelExecRun(...)` ctx flows
into `c.run(...)`). The split is emitted from the **same** `startedCallback`
boundary native uses: a parallel `profStartedWall atomic.Int64`
(`executor_spec.go:1271`,`:1412`) captured beside native's `profStartedNS`, then
`emitOTelExecSplit` (`otelprof.go:96`) backdates `containerStart [start,started]`
(engine, no error) and `processRun [started,end]` (`work_type=user`, carries
runErr). **`work_type=user` is on `processRun` only** — the loader reads it and a
slow `go build` headlines as user work, not engine overhead. The **never-started**
case (`started.IsZero()`) correctly emits only `containerStart` over the whole
interval with the error (`otelprof.go:99`), mirroring native. Explicit
`WithTimestamp` carries the true retrospective intervals. ✔

**Services (§3.4) — Invariant T correct.** In `startWithKey` (`services.go:1024`–
`1043`): `beginOTelServiceStart` mints `service.start` **under `ss.l`** →
`start.otelStartSpanCtx = startSpan.SpanContext()` stashes → `ss.starting[key] =
start` publishes → `ss.l.Unlock()` after. The installer reads `starting` under
`ss.l` and emits its wait against `starting.otelStartSpanCtx`
(`services.go:996`,`:1010`) on **both** select branches. The `service.start` span
is ended on **every** exit path (start error, no-Wait, canceled, OK —
`:1054`,`:1067`,`:1083`,`:1092`). The long-lived availability span stays
passthrough; its idle daemon run is absorbed by `exec.run` and never *ranks*
(off the critical path — nothing waits for the daemon to finish). ✔

**Exported helpers — justified.** `otelProfActive`→`OTelProfActive`,
`emitOTelWait`→`EmitOTelWait` (`otelprof_hooks.go:34`,`:103`), so the executor
(`engineutil`) and service (`core`) emit sites share one gate + one wait-edge
wire format. Right call — it keeps the wire format byte-identical across all
sources and the loader/gate read them uniformly. The exec-run ident is the call
digest when known else exec id (`executor.go:116`–`118`), matching native so the
oracle matches per-exec. ✔

No degenerate perf: three passthrough spans per container run (already starting a
container) + one cheap unconditional `profStartedWall` atomic. Concurrency safe
(immutable `SpanContext`, write-before-publish, same pattern as Chunks 2/3).

**The validation holds (verified):** `TestChunk4ExecSplitFidelity` /
`ServicesFidelity` drive the real emit against an in-memory SDK, feed the exported
spans through my loader + gate, and converge with the native IR. Continuing the
Chunk-3 in-memory-emit-test practice (my Chunk 2 HOLISTIC-1). ✔

### Noise (LOW)
- A long-lived service daemon's `processRun` carries `work_type=user` with the
  full idle duration as self-time. It correctly **doesn't rank** (off critical
  path) and is **oracle-consistent** (native records the identical split), but it
  inflates the raw user-work self-time *table* (not the what-if headline). Shared
  native/OTel characteristic, not a Chunk 4 defect — worth a one-line note in the
  report so readers don't misread the table.
- Pre-existing `lostcancel`-family vet warnings at `executor.go:518/602/669` and
  `services.go:918` — I verified the `WithTimeoutCause` calls exist at base
  `b442cd2533` and Chunk 4 added none (line-shifted only). Not Chunk 4.

## (B) Holistic — composition holds, with the module-loading caveat

**The loader needed no change a 4th time** (verified empty diff over
`wcprof/wcotel/**`, `telemetryattrs`, **and `wcanalyze/`** — the replay is
untouched too). The loader already classifies `exec`/`exec_phase` by
`wcprof.op.kind` and reads `work_type` (Chunk 1), so Chunk 4's spans compile with
zero loader awareness — the IR contract has now held four times. On the
**module-free** exec/service paths the gate stays green (the committed fidelity
fixtures + the implementer's module-free live runs). That part composes cleanly.

The caveat is Part 2: on **module-loading** workloads the gate is red. That is the
gate doing its job — and it means the *holistic* picture is "mechanically
composing, but not yet faithful on the primary real workload."

---

# PART 2 — Independent cycle investigation

I reasoned from the replay/loader/gate code (which I own) + the described cycle
structure + the design. I did **not** have the 9.3 MB raw trace; where that
limits me I say so.

## Q1 — Is the gate right, or is the loader/gate manufacturing the cycle?

**The gate is right; the loader is not manufacturing it.** I traced what the
loader actually does with the two edges of the loop:

- **The closing edge `op#54 --singleflight--> op#94`** comes from a `purpose=wait`
  link the engine emitted (Chunk 2 `EmitOTelWait`), carrying op#94's `call_exec`
  span id as target. The loader resolves the link target to op#94 and attributes
  the wait to the span carrying it (op#54's subtree). This is a **faithful**
  compile of an emitted link — the loader invents nothing. The ε-boundary
  classification is **correct**: `waitEnd=195 ≥ targetEnd=195 − ε ⇒ actWaitJoin`
  (`replay.go:165`) is the *genuine* singleflight-join (a joiner blocks exactly
  until the execution ends); it is not a boundary artifact.
- **The reverse path `op#94 ⇒ … ⇒ op#54`** is ordinary `parentId` nesting. These
  are `Query.moduleSource` `call_exec` ops — **not** lazy work — so there is **no
  `wcprof.parent`** override; the loader uses `parentId` directly
  (`wcprof.parent ?? parentId`). So the nesting it compiles is exactly the OTel
  span tree. No mis-resolution is possible here — the loader is reading the
  emitted parent edge verbatim.

So both edges are faithful compiles of the **emitted** trace. The cycle is then
manufactured by the **implicit join** (`replay.go:353`–`369`, the §1.1
assumption), which reads op#94⊃op#54 nesting as "op#94 synchronously waited for
op#54." **The decisive evidence that this inferred edge is false: the workload
completed `rc=0` with no deadlock.** If op#94's execution had *truly*
synchronously contained op#54 *and* op#54 truly waited for op#94, that is a
literal deadlock — which did not happen. Therefore the nesting is **not**
synchronous containment, the implicit-join edge is invented, and the loop is
over-serialization. This is exactly the §1.1/§2.2 hazard the gate exists to catch
— **the gate firing is the correct outcome, not a false positive.** (I corroborate
the implementer's theory here independently; I did not need to trust it.)

One thing I checked specifically because it would have been a loader bug: two
distinct `call_exec`s of `Query.moduleSource` cannot share one `ongoingCall` (one
per concurrency key), so op#54 does not *directly* join op#94 — the wait is from a
**sub-call inside op#54's execution** that singleflight-joined op#94's in-flight
work, attributed (correctly) to op#54's subtree via the suppressed-caller→ancestor
rule (§3.1). That is faithful, not a mis-attribution. ✔

## Q2 — Truly not Chunk 4?

**Not Chunk 4 — high confidence.** Three independent legs:
1. **Strip test** (remove every Chunk 4 op → *identical* 5 cycles + 18 fallback
   anchors; every cycle op is `call`/`call_exec Query.moduleSource` or `load
   module:` — zero Chunk 4 kinds). This is strong: the cycle's edges do not
   involve any Chunk 4 op.
2. **Chunk 4 is purely additive emit** — I verified its diff touches no
   `replay.go`, no `loader.go`/`gate.go`, no `wcanalyze/`. Its `exec.run`/phase
   spans are **leaf children** of the module-runtime `call_exec`s; they add no
   back-edges and don't restructure the moduleSource subtree.
3. **Module-free** exec/service workloads (which *do* emit Chunk 4 spans) are
   cycle-free.

The one residual the strip test cannot fully exclude is an **observer effect** —
Chunk 4's span minting being present *during capture* perturbing the moduleSource
timing/nesting (the strip removes the ops, not their runtime influence). I judge
that negligible (additive passthrough spans on an already-container-starting path
don't change module-loading structure), but the **clean closure is the from-source
rebuild at Chunk 3 HEAD** the implementer offers. I'd recommend running it — it's
cheap and makes "not Chunk 4" airtight — but I do **not** consider it required to
reach the conclusion; the strip test + additive-emit proof already carry it.

## Q3 — In scope, or a seam?

A **known hazard class, an unhandled specific case.** §1.1/§2.2 anticipate that
OTel parentage (context propagation, not the call stack) can let the implicit join
invent dependencies/cycles for detached/concurrent work. But §3.1's singleflight
fix rests on the assumption *"the joiners are in a different subtree (the execution
is not nested under them)."* Concurrent `moduleSource` loads that **cross-join
through nesting** — one module's load triggering another's lookup so their
nested-client subtrees interleave — **violate that assumption**: the joiner is in a
*different* subtree for the wait edge **and** an *ancestor/peer* via nesting, so the
two edges close a loop. This is a **Chunk 2 (singleflight) × module-loading /
nested-client** interaction, surfacing now because module-loading is the **first**
workload exercised live (Chunks 2/3 used core-API and lazy-Directory). It is a
real emit-faithfulness gap, not a loader/gate/replay bug.

## Q4 — What to do? (where I differ from the implementer's lean)

The implementer offers "accept as a §9 reserve seam" as an option. **I push back on
that being clean**, for one reason: **the cycle is in the north-star critical
path.** Leaf-I/O (a genuine §9 seam) is peripheral; *module loading is the common
case* — `dagger call` against a module is what a real CI run is. A gate that goes
**red on module-loading** means the OTel source's own trust signal is red on the
primary use case. The cycle-break (`replay.go:341`–`345`, assume recorded
duration) keeps the result *approximate* and the gate keeps it *loud* — so this is
**not a Chunk 5 mechanics blocker** — but "approximate + loud" is not "faithful,"
and v1's promise is faithfulness on CI traces.

So my disposition:
- **Before v1**, this needs **either** (a) an emit fix so concurrent module-source
  executions don't spuriously nest (the design's own principle: "make every
  nesting the analyzer reads as synchronous truly synchronous"), **or** (b) a
  **measured** bound on the over-serialization's *ranking* impact plus a principled
  gate accommodation for this specific known pattern — so the gate stays meaningful
  (green when the bottleneck answer is trustworthy, red only when genuinely
  degraded past the bound), rather than red on every CI trace.
- **The immediate next step is measurement, not a fix decision:** does the
  cycle-break's approximation actually move the top-N `RunWhatIfs` ranking on the
  module-loading trace, or is the bottleneck answer still right despite the 5
  cycles? That single number decides (a) vs (b). It needs the raw trace's what-if
  output (and the baseline-drift % the report already computes) — **which I could
  not access**, so I flag it as the key artifact to extract next.
- **Generality caveat:** I would *not* patch `moduleSource` narrowly. The "different
  subtree" assumption is fragile under *any* concurrent-shared-work-through-nesting,
  so the fix (or the bound) should be principled enough to cover the class, and the
  Chunk-3 rebuild + the extracted cycle subgraph should pinpoint *why* the two
  concurrent executions nest (a module-loading context-propagation question) before
  choosing the mechanism.

**What I'd need to go further:** the extracted cycle subgraph (the ~10 ops + their
parentId/wait links) and the module-loading trace's `RunWhatIfs` top-N + baseline
drift. With those I could say whether (a) or (b) is right and how invasive (a) is.
From code + structure alone I'm confident on **cause**, **not-Chunk-4**, and
**gate-is-correct**; the **fix-vs-bound** call needs the ranking-impact number.

---

## Bottom line

Chunk 4 faithfully closes the last two choke points: the exec engine/user split
makes user work first-class (`work_type=user` on `processRun`, the north-star
headline), services are Invariant-T-correct, and it composes — loader unchanged a
4th time, gate green on module-free paths. **Proceed to Chunk 5.** The cycle is
**the gate working exactly as designed** — loudly flagging a genuine
over-serialization from an unfaithful nesting — and is **not Chunk 4** but a
**Chunk 2 × module-loading/nested-client** faithfulness gap. Because module
loading is the north star's primary workload, I would **not** file it as a quiet
§9 seam: it is a v1-gating item to **measure (ranking impact) then fix-or-bound**,
with a Chunk-3 from-source rebuild + the extracted cycle subgraph as the cheap
artifacts that turn "high-confidence" into "airtight" and pinpoint the mechanism.
Suggested design touch-up for the lead: record this as an explicit open faithfulness
gap against §3.1's "different subtree" assumption (not in §9's peripheral-seam
list), scoped to concurrent shared-work-through-nesting.
