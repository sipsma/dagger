# wcprof × OTel — Chunk 4 review + cycle investigation (by the design author)

**Scope:** Part 1 — normal Chunk 4 review (exec engine/user split §3.3 + service
start §3.4), isolation + holistic across Chunks 1–4. Part 2 — independent
investigation of the module-loading **cycle**. Reviewed commit `4d6987fdc2` (on
Chunk 3 `107ebe5c0c`, base `b442cd2533`) against `hack/designs/wcprof-otel-design.md`.

## Plain answers up front

1. **Is Chunk 4 sound to build Chunk 5 on?** **Yes.** The exec split and service
   start are faithful to §3.3/§3.4, Invariant T holds for `service.start`, the
   loader/gate/replay are untouched a 4th time, build + all emit-path tests pass.
2. **Is the Chunks-1–4 trajectory sound?** **Yes** — and this chunk lands the
   **user-work-first-class** milestone (a slow `go build` headlines as
   `processRun work_type=user`, not engine overhead).
3. **The cycle (independent verdict):** I **confirm** it is a real graph cycle =
   over-serialization artifact (a false implicit-join edge from OTel
   context-propagation nesting of concurrent/cross-joining `moduleSource`
   executions), **not** a replay bug and **not** a deadlock — reached by a cleaner
   argument than the implementer's. It is **almost certainly not Chunk 4** (strong
   evidence; one cheap confirmation worth doing). It is a **Chunk 2 (singleflight)
   × module-loading** design gap in §3.1's "joiners in a different subtree"
   assumption. **Before choosing fix-vs-seam, one decisive diagnostic is missing:
   does *native* wcprof also cycle on this workload?** My strong hypothesis is yes
   — which would make it a shared native+OTel seam (the validated cycle-break
   already handles it), not an OTel faithfulness bug. Details in Part 2.

Verified locally (temp worktree at `4d6987fdc2`): `go build ./dagql/... ./engine/...
./core/...` **OK**; `go test ./engine/wcprof/wcotel/...` **ok 0.046s**;
`./engine/engineutil` **ok**, `./core` **ok**; loader/gate/`wcanalyze` untouched by
the Chunk 4 diff. `go vet` shows only pre-existing `WithTimeoutCause`/`lostcancel`
warnings (not in Chunk 4's hunks).

---

## Part 1A — Chunk 4 in isolation (§3.3/§3.4)

**Exec engine/user split (§3.3) — faithful.** `beginOTelExecRun`
(`engine/engineutil/otelprof.go:44-52`) starts `exec.run` (`wcprof.op.kind=exec`,
passthrough) on the executor ctx, which descends from the `withExec` resolver's
`call_exec` `sharedWorkCtx` (Chunk 2) — so `exec.run` nests under `call_exec`, a
genuine synchronous nesting (`executor.go:133-141`). `emitOTelExecSplit`
(`otelprof.go:78-100`) emits `containerStart` (engine, `[start,started]`) and
`processRun` (user, `[started,end]`, `work_type=user` **only**) backdated at the
started-callback boundary, mirroring native's two `RecordOp`s
(`executor_spec.go:1403-1429`); the **never-started** case correctly emits only
`containerStart` over `[start,end]` carrying the error (`otelprof.go:86-91`). The
wall-clock boundary is captured at the same callback as native
(`profStartedWall`, `executor_spec.go:1276-1289`) with one cheap unconditional
atomic per run — negligible.

**Services (§3.4) — faithful + Invariant T.** `beginOTelServiceStart`
(`core/services.go:206-221`) brackets the start window; **minted under `ss.l` and
stashed on `start.otelStartSpanCtx` before `ss.starting[key] = start`**
(`services.go:1027-1045`) — Invariant T holds, every installer joining the start has
a valid target. Installers emit a `service` wait on both the `ctx.Done()` and
`starting.done` branches (`services.go:996-1011`); `endOTelServiceStart` covers all
start exits, nil-safe. The long-lived availability span stays passthrough; the
daemon's `exec.run`/`processRun` runs in the background with **no waiter**, so it is
not credited makespan and **does not rank** (verified by the services fixture). Per
the comment, no per-attempt reset is needed (unlike lazy): each start gets a fresh
`startingService` deleted on completion.

**Exported helpers — clean.** `dagql.OTelProfActive` / `dagql.EmitOTelWait`
(`otelprof_hooks.go:32-118`) are the one canonical gate + wire format, now reused by
the executor and `core/services` emit sites that live outside `dagql`. One
definition keeps "is the OTel source recording here?" and the wait-edge bytes
identical everywhere.

**Two notes (both expected seams, not defects):**
- *Finer-phase folding (§3.3 seam).* `containerStart` covers only the
  `runContainer` window; the earlier setup funcs (`setupNetwork`, `setupRootfs`, …
  — native's `exec.<phase>` ops) fall into `exec.run` **self-time**, classed
  `exec:exec.run`. So a slow `setupNetwork` (the original wcprof "300ms serial tax"
  headline) is **captured and correctly engine-classed** (not mislabeled user), but
  at coarser granularity than native — so the oracle will drift on
  `exec.run`-vs-`exec.<phase>` *labels* on setup-heavy workloads. This is the
  designed §3.3 finer-phase seam (deferred), and it does **not** weaken the
  user-work headline (`processRun` is split out cleanly). Worth wiring into the
  Chunk-5 oracle scope/class accounting alongside the Chunk-3 scope-matching.
- *Idle-daemon self-time.* A daemon's `processRun` carries large `work_type=user`
  self-time (whole-session lifetime) but never ranks (no waiter). Benign for the
  ranking; a future "idle availability vs active user work" distinction is a seam.

**Perf / simplicity:** three passthrough spans per container run + one per service
start + tiny per-installer links; all on paths already starting containers. No
quadratic. The new `engine/engineutil/otelprof.go` is small and self-contained.

## Part 1B — Holistic across Chunks 1–4

**Composition holds a 4th time.** The Chunk 4 diff adds emit in the **executor**
and **core/services** (beyond `dagql/`) plus tests, and touches **no**
`loader.go`/`gate.go`/`wcanalyze` (confirmed). The new out-of-`dagql` emit sites
nest correctly via the same context propagation (`exec.run` under `call_exec`;
`service.start` under the service span). The **"lazy-triggered exec composes for
free"** discovery is real and a nice validation of Chunk 3: a lazy materialization's
`withExec` `call_exec` is a *direct* re-pointed child (stamped `wcprof.parent` = the
lazy op), and `exec.run` nests under *that* `call_exec` (parentId), so it re-homes
to the lazy op **transitively** via its stamped ancestor — exactly the §3.0.2
"descendants follow their stamped ancestor" design, loader unchanged. (Minor: the
exec.run's own `parentId` is the `call_exec`, not the producer directly — the
implementer's shorthand — but the transitive re-home is correct.)

**North star: user work is now first-class.** With the engine/user split, the OTel
source can answer "your `go build` is the bottleneck" (processRun) distinctly from
"container setup is slow" (containerStart/exec.run). That was the milestone; it is
met. The remaining gaps to v1 are productionization (Chunk 5) and the cycle
disposition (Part 2).

---

## Part 2 — The cycle: independent investigation

I read the implementer's analysis and did **not** assume it. I cannot see the
9.3 MB trace, so I reasoned from the described cycle structure + the replay/loader
code + the design. My conclusions agree with the implementer's headline but I reach
them differently, find the evidence **stronger** on the cause and **weaker (with a
clear gap)** on disposition, and identify a decisive missing diagnostic.

### (1) Cause — confirmed: false implicit-join edge, not a replay bug, not a deadlock

The cleanest argument needs only three facts and the replay code:

1. **The closing singleflight edge is correctly classified `actWaitJoin`.** With
   `waiter[160..205] target[160..195]`, `waitEnd=195 = targetEnd=195`, the replay
   classifies it `actWaitJoin` because `w.EndNS >= w.Target.EndNS - joinEpsilonNS`
   (`replay.go:165`, ε=1 ms). That is the *faithful* reading: a singleflight joiner
   blocks on `oc.waitCh` until the execution's `fn` completes, so it genuinely
   waited to the target's end. So this edge is a **real** dependency, **not** a
   mis-classification, and **not** an ε-boundary artifact (it is exact, not
   borderline). The reviewer's hypothesis "maybe the closing join is mis-classified"
   is ruled out.
2. **The workload completed (rc=0) — no runtime deadlock.** Therefore a graph cycle
   of *all-real synchronous* edges is impossible (it would have deadlocked).
3. **(1)+(2) ⟹ at least one OTHER edge in the cycle is false.** Since the closing
   `actWaitJoin` is real, the false edge is in the `op#94 ⇒ … ⇒ op#54`
   **implicit-join** chain. The implicit join (`replay.go:353-369`) infers a parent
   synchronously waited for everything nested in its subtree — but OTel parentage is
   built from **context propagation, not the live call stack** (§1.1), and the
   `call_exec` resolver runs in a **detached goroutine** (`cache.go:3700`), so a
   concurrent/work-sharing `moduleSource` execution can become *nested* under
   `op#94`'s subtree without `op#94` having synchronously blocked on it. That is the
   false edge.

So: a **real graph cycle that is an over-serialization artifact** — the §1.1 hazard
made concrete. Not a replay flaw (the cycle-break + the gate are working as
designed), not a deadlock. I independently reach the implementer's conclusion, and
the "is it the ε-boundary / a missed real dependency / a replay mis-classification"
alternatives are all ruled out: a *missed* dependency would cause
under-serialization (a missing edge), never a cycle; the cycle is *extra* edges.

### (2) Is it Chunk 4? — almost certainly not, with one cheap confirmation worth doing

Strong evidence, independently checked:
- **The Chunk 4 diff touches no analysis code** — I confirmed `loader.go`/`gate.go`/
  `wcanalyze` are untouched; it is purely additive emit.
- **Every op in every cycle is a Chunk-2 kind** (`call_exec`/`call Query.moduleSource`
  + `load module:` spans) — **zero** Chunk 4 kinds. The cycle lives entirely in the
  Chunk 2 singleflight layer × module loading.
- **Chunk 4's spans cannot alter the cycle.** They are leaf children of the
  module-runtime `call_exec`s; adding children does not change a parent op's
  `StartNS`/`EndNS`, and the cycle turns on those intervals (the implicit join + the
  `waitEnd=195=targetEnd` boundary). The strip test (remove Chunk 4 ops → identical
  5 cycles) confirms this from the data.
- **Module-free traces are cycle-free**, so it correlates with module loading.

**Residual gap the strip test cannot close:** it removes Chunk 4 ops *post-hoc from
a trace captured with Chunk 4's runtime present*. It cannot rule out Chunk 4's
runtime *timing* perturbing the concurrency that produced the cross-join (module
loads are concurrent and the cache has an acknowledged redundant-execution race,
`cache.go:3660-3664`). Given the cycle is concurrency-dependent, the **from-source
rebuild at Chunk 3 HEAD** (re-capture module loading with Chunk 4's emit entirely
absent at the source) is the definitive confirmation. The conclusion (not Chunk 4)
is already well-supported; I'd run the rebuild as cheap insurance, not because I
doubt it.

### (3) Disposition — a real §3.1 gap; the decisive diagnostic is missing

This violates **§3.1's "joiners are in a different subtree (the execution is not
nested under them)"** assumption. That assumption holds for ordinary resolver
calls: a joiner did not create the execution's `call_exec` (the executor did, under
the executor's ctx), so the execution is in the executor's subtree, not the
joiner's. Module loading breaks it: concurrent work-sharing `moduleSource`
executions, run in detached goroutines and nested by context propagation (amplified
by nested-client module-runtime spans and the redundant-execution race), end up
**mutually reachable** — one execution's subtree contains the other while the other
singleflight-joins the first. §1.1/§2.2 anticipate the *hazard class*; §3.1 did not
carve out this *specific* cross-subtree-join case. That is a genuine design gap to
record.

**But the fix-vs-seam choice hinges on a diagnostic nobody has run: does *native*
wcprof also cycle on this same module-loading workload?** This matters because
native's `call_exec` (`execOp`) is *also* begun on the executor's detached
`callCtx` and its resolver sub-calls nest via the **wcprof context key**, not the
live stack (`cache.go:3669-3717`) — the *same* context-propagation nesting OTel
uses. So my **strong hypothesis is that native cycles too.** If so:
- It is a **shared native+OTel limitation** of the detached-`call_exec` model, the
  OTel source is *faithfully reproducing native's graph* (the cross-source oracle
  would still agree — both cycle, both break it identically), and the validated
  native replay's **cycle-break** (`replay.go:341-345`, assume recorded duration)
  already yields an approximately-correct result. ⟹ **§9 reserve seam**, plus a
  **§6.1 gate refinement**: an unconditional `CycleWarnings == 0` hard-fail is then
  *too strict* (native's own ground-truth graph would fail it on module loading);
  the gate should treat the documented module-source cross-join as a tolerated,
  reported seam (or, in the oracle, require OTel's cycle count to *match* native's
  rather than be zero).
- If instead **native is cycle-free**, then OTel's nesting diverges from native's →
  a genuine **OTel emit faithfulness bug to fix** (do not ship a seam). The fix
  belongs at **emit** (avoid the unfaithful nesting), not in the loader/replay —
  reclassifying or suppressing the closing wait is design-forbidden (§6.3: don't add
  replay machinery to paper over bad data), and the wait is a *real* join anyway.

So the right next step is **not** to pick fix-or-seam now, but to run the
**scope-matched cross-source oracle (per the Chunk 3 §6.2 reconcile) on this
module-loading workload**: it answers both questions at once — does native cycle,
and do the two sources still agree on the top-N ranking *despite* the cycle? If
native cycles and the rankings agree, the cycle is a non-event for the product
(module loading isn't the headline bottleneck; the cycle-break gives correct-enough
moduleSource durations) → document as a §9 seam + relax the §6.1 cycle invariant
accordingly. If native is clean or the rankings diverge, fix the emit.

**Reproduction I judge necessary:** (a) the native-vs-OTel oracle on the
module-loading workload (decisive for disposition — cheap, just enable both sources
on one run), and (b) the from-source rebuild at Chunk 3 HEAD (definitive for
Chunk-4-innocence). Extracting the minimal cycle subgraph would help pin the exact
nesting mechanism but is secondary to (a).

**Severity / urgency:** not a Chunk 4 blocker and not a Chunk 5 *start* blocker —
the cycle-break keeps the analysis running and loudly flagged. But it **is a
prerequisite for the §6.4 standing drift gate (Chunk 5)** to be green on real
workloads: virtually every real CI run loads modules, so the gate cannot ship as an
unconditional `CycleWarnings == 0` until this is dispositioned. Treat it as a
**MEDIUM, must-resolve-in/with-Chunk-5** item, owned jointly by Chunk 2
(singleflight) and the module-loading/nested-client path.

---

## Issues summary

- **REAL / MEDIUM — module-loading cycle (Part 2).** Real §3.1 "different subtree"
  gap, over-serialization artifact. Not Chunk 4 (confirm via from-source rebuild).
  Disposition blocked on the native-vs-OTel oracle diagnostic; resolve in/with
  Chunk 5 (seam + §6.1 cycle-invariant relaxation if native also cycles, else emit
  fix). Must precede the §6.4 standing gate.
- **NOISE / expected seam — finer exec phases.** `containerStart` folds the
  setup-func time native splits; captured + correctly engine-classed, coarser
  granularity (oracle drifts on `exec.run`-vs-`exec.<phase>` labels). Designed §3.3
  seam; fold into the Chunk-5 oracle scope accounting.
- **NOISE — idle daemon `processRun`** shows large user self-time but never ranks;
  future idle-vs-active seam.
- **NOISE — pre-existing vet** (`WithTimeoutCause`/`lostcancel`), not Chunk 4.

## Bottom line

Chunk 4 faithfully delivers the exec engine/user split and service start, holds
Invariant T, composes with zero analysis-path churn (4th time), and lands
user-work-first-class — sound to build Chunk 5 on. The cycle is independently
confirmed as an over-serialization artifact (false implicit-join edge), not a replay
bug or deadlock, and not Chunk 4; it is a real §3.1 module-loading gap whose
fix-vs-seam disposition is **blocked on one cheap diagnostic — does native cycle on
the same workload (run the scope-matched oracle).** My strong prior: shared
native+OTel seam ⟹ document (§9) + relax the §6.1 cycle invariant; resolve with
Chunk 5 before the standing drift gate. **Proceed to Chunk 5**, carrying the cycle
diagnostic as a Chunk-5 prerequisite, and fold the §3.1 assumption-refinement +
finer-phase oracle note back into the design.
```
