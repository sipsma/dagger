# wcprof × OTel — consolidated findings (investigation, pre-fix)

Status: **investigation complete; no fixes applied.** This is a durable capture of
what is wrong with the OTel-source branch (12 commits + the `wcprof-otel-source.md`
design doc), the root causes, what is trustworthy vs suspect, and the validation gap.

Method: two independent investigations (a Claude run and a Codex run) were given the
same brief in separate worktrees, with no coordination. They **converged** on the
same root cause and the same prime driver, and each contributed sharper concrete
confirmations. This doc is the Claude-lead synthesis; every adopted claim was
re-verified against the current source. Findings are tagged:

- **[confirmed-code]** — read directly from the source on this branch.
- **[confirmed-empirical]** — observed in a reproduced analyzer run.
- **[strong-hypothesis]** — mechanism confirmed from code; dominance/magnitude inferred, named confirmation pending.
- **[hypothesis]** — reasoned, not yet pinned.

Line references are to this worktree's tree (`engine/wcprof/**`, `dagql/cache.go`,
`core/**`, `cmd/wcprof-analyze/**`).

---

## 0. Bottom line

The symptoms are not a scattering of bugs. They are **one design-level mismatch plus
a small number of concrete defects that follow from it.** The branch rests on an
invariant from the design doc:

> *inherit native wcprof's drift-validated implicit-join for synchronous parent→child
> via nesting + timestamps; every cross-tree edge arrives explicit.*

The fatal assumption is that **OTel `parentId` nesting == native's synchronous
parent→child call tree.** It does not.

- **Native's** op tree is built by inline `wcprof.BeginOp` on the live call stack
  (`engine/wcprof/record.go:101,121` — `parentID: CurrentOpID(ctx)`), so a parent op
  is *genuinely blocked inside* its child. Implicit-join is valid **by construction**.
- **OTel's** tree is built by **context propagation**, which the engine uses for many
  relationships that are *not* "parent blocked inside child": detached lazy-eval
  goroutines, work reparented to a different span for log/failure attribution,
  concurrent dependency resolution, suppressed cache-hit spans, and a whole second
  span source (buildkit/IO) with no wcprof semantics at all.

Feeding that tree into native's replay injects **false serialization** and **duplicates
wall-clock time**, so on a complex workload the IR the counterfactual runs over is not
the synchronous call-tree the replay assumes. The absolute numbers are therefore
untrustworthy — which is the entire point of the tool. This is **primarily a design
problem**, with implementation-level fallout.

The single prime driver is the **lazy-resume span**, which is structurally orphaned in
*both* directions: it has no real children (its work is reparented elsewhere) and it
outlives its own parent (it runs in a detached goroutine). It therefore books tens of
seconds of *duplicate* "engine self-time," and the analyzer's #1 recommendation on the
real trace is to optimize `lazy:resume withExec` — a **phantom**, not an actionable
bottleneck.

---

## 1. Reproduced symptoms

Augmented dev-engine build trace `defb713e9f6db4f8420a522cc4d77efe`
(`./bin/wcprof-analyze -source=cloud -cloud-trace=…`): **[confirmed-empirical]**

```
ops: 8940   roots: 1   (one tree — NOT independent CI roots)
actual makespan: 137.44s
simulated baseline makespan: 226.96s   (drift vs actual: +65.1%)   ← over-serialization
sim diagnostics: 8 broken cycles, 273 fallback anchors
self-time by work type: engine 764s + unknown 343s + user 237s ≈ 1344s in a 137s trace
diagnostics: 4133 / 8940 ops lack wcprof.kind   (46% of ops un-augmented)
lazy:resume withExec — max self-time 62.81s   (≈ half the whole trace in ONE op's self-time)
top individual ops: processRun self=100.99s → save@0 = 0ns; resume withExec self=62.81s → save@0 = 0ns
```

Cross-checks:
- Simple trace `303c520ea4df25c7e79fb9a55942ab3a` (79 ops, 66% un-augmented):
  **+5.9% drift, 0 cycles, 1 fallback.** The blowup scales with **nesting depth /
  singleflight + lazy collisions**, not with the un-augmented fraction. **[confirmed-empirical]**
- `--root-mode=cli` on `defb` produces **byte-identical** output → the +65% is purely
  intra-tree recursive-replay inflation, **not** root coupling / root-mode. **[confirmed-empirical]**
- Both independent investigations measured identical top-line numbers on `defb`.

Reading of the headline data: `engine 764s` self-time in a 137s trace, with a single
`lazy:resume withExec` op at 62.81s self, and huge *real* ops (`processRun` 100.99s)
that **save 0ns when zeroed** — i.e. the simulator's critical path runs through the
duplicated/phantom branch, leaving the genuinely heavy work off-path. That is the
fingerprint of false serialization, not of a correct counterfactual.

---

## 2. Root causes

### RC1 — Implicit-join applied to non-synchronous OTel nesting  ·  **DESIGN**  ·  [confirmed-code]

The umbrella cause (§0). `replay.go` treats every `parentId` nesting edge as a
synchronous join: `joinUpTo` (`replay.go:379`) makes a parent absorb the simulated
finish of every child that originally ended by time *t*; `SelfSegments`
(`graph.go:407`) defines self = duration − children − waits. Both are correct for
native's inline tree and **invalid** for the subset of OTel nesting that is
asynchronous/detached/reparented. Everything below is a specific way the OTel tree
violates the synchronous-nesting assumption.

### RC2 — Lazy-resume spans are structurally orphaned (the prime driver)  ·  **DESIGN**  ·  [confirmed-code] + [confirmed-empirical]

This single defect dominates the +65%. The `resume <field>` span (`dagql/cache.go:3008`,
tagged `kind=lazy`) is wrong in **two** complementary ways, both flowing from how the
engine emits lazy-eval telemetry. The two independent investigations each found one
facet; they are the same root:

**(a) No real children → inflated self-time (double-counts exec work).**
The lazy callback runs under `callbackCtx`, whose active span is
`resumedCallbackSpan{sc: originalSpanCtx}` and whose `SpanContext()` returns the
**install span**, not the resume span (`dagql/cache.go:2814`, `:2820`). So all real
eval work (execs, sub-calls) created during `lazyEval` nests under the **install span**,
never under the resume span. The resume span is near-childless → its self-time ≈ its
full wall-clock duration. The same wall-clock interval is now counted **twice**: once
as the resume span's "engine self-time," once as the real subtree under the install
span. The 62.81s max-self is exactly this. The design's R-P claim ("self-time is glue,
children are the real resolution work") holds for native and is **false for OTel.**

**(b) Outlives its own parent → parent-interval violation.**
The resume span runs in a **detached goroutine** (`go func()` at `dagql/cache.go:2992`)
on `evalCtx = context.WithoutCancel(stackCtx)`. The triggering caller's
`waitForLazyEvaluation` returns immediately on `ctx.Done()` (cancellation), and only the
*last* waiter cancels the eval — so the parent call span (the resume span's OTel parent)
routinely ends while the detached goroutine keeps running. Result: `resume.End >
parent.End`, a child that ends after its parent — which the implicit-join/spawn model
cannot represent. Codex measured **1635 parent-interval violations** on `defb`, top
category **`call → lazy` with 1113 cases and ~47m32s cumulative child-after-parent
overrun**, with a concrete example of a `lazy resume withExec` running 62.8s under a
`Container.directory` call that had already ended. The mechanism is **[confirmed-code]**
(detached `WithoutCancel` goroutine + early-return on cancel); the exact counts are
**Codex-measured / [confirmed-empirical-by-Codex]** and corroborated by Claude's
independent observation of the same ~62.8s `resume withExec` span.

**Native contrast (why this is OTel-specific):** native parents the real lazy work
*under the lazy op* via `BeginOp`'s own context op-stack (`record.go:101,121`), which the
OTel `resumedCallbackSpan` wrapper never touches — so native's `lazy` self-time is
genuine glue and there is no detached-child-outlives-parent shape. The two sources
diverge at exactly this choke point.

**Net effect:** RC2 injects ~370s of `lazy:resume*` self-time (314s `resume withExec` +
48s `resume from` + …) that is largely a duplicate of real exec time; the implicit-join +
spawn-anchoring then cascades the inflation (once a clock is pushed right, every later
sibling's spawn anchors at the inflated clock). Hence the +65% **and** the phantom #1
ranking. Dominance is **[strong-hypothesis]**; the named confirmation is a
`DriftOrigins`/`BaselineDrift` run (functions already exist, unexposed) expected to show
`lazy:resume*` ops dominating drift origins.

### RC3 — the 8 cycles are a REPLAY-MODEL flaw (shared with native), not a loader-reparent bug  ·  **DESIGN**  ·  [confirmed-code+empirical]

> **Corrected by the chunk-1 implementation (see §RC-cycle for the definitive analysis).** The
> initial diagnosis below — "the unguarded `synthesizeCallExec` reparent makes a joiner a child
> of the `call_exec` it waits on; guard the reparent and the cycle goes away" — was **wrong**.
> Chunk-1 implemented exactly that transitive guard, and on `defb` **all 8 cycles persisted**
> (`DroppedCycleWaits=0`; the ancestor-targeting cycle-breaker caught none). The cycle is **not**
> an ancestor-shaped reparent artifact: it is a **mutual near-instant singleflight join** between
> two concurrent `Query.moduleSource` evaluations, which the replay turns into an impossible
> constraint by combining the implicit join with `actWaitJoin` pinning to the target's *full*
> recursive finish (discarding the wait window). It **persists even with explicit `call_exec`
> emit** and is **shared with native** (same op/wait structure + same replay). The
> `synthesizeCallExec` reparent is a *separate* problem — it is **loader-side causal inference**
> (RC-cycle 3c), an anti-inference breach to be deleted — but it is not the cycle's root.

The original (now-superseded) mechanism: `synthesizeCallExec` (`loadotel_synth.go:102`) builds a
bounded `call_exec` op and reparents the call span's in-window children into it by interval
containment (`:135-144`), retargeting joiner waits to it (`loadotel.go:106-114`). A re-entrant
joiner reparented under the `call_exec` it waits on *does* form one cycle shape — but guarding it
does not reach the real cycle. Full root cause: **§RC-cycle**.

### RC4 — `exec.processRun` is a full-duration leaf that double-counts in-container nested work  ·  **DESIGN/IMPLEMENTATION**  ·  [confirmed-code]

`synthesizeExecPhases` (`loadotel_synth.go:76-81`) adds a `processRun` child
`[Container started, exited]` with **no children**, so its self-time is the whole
process window. For a plain `go build` exec this is correct. But the R-C propagation
change deliberately nests an exec's **in-container nested-dagger-client work** under the
exec span — so for module execs that nested work is a *sibling* subtree covering the
same interval, and `processRun`'s leaf self-time **duplicates** it. Same
"synthetic full-duration leaf duplicating a real subtree" pattern as RC2(a), narrower
scope (module/SDK execs). Contributes to over-serialization for module-heavy builds.

### RC5 — Suppression mis-attributes causal edges (LOAD-BEARING, not minor)  ·  **DESIGN**  ·  [confirmed-code]

> **Re-classified from "minor / tree holes" to load-bearing** (see §RC-cycle). It was filed as a
> completeness nicety; it is actually a **causal-edge mis-attribution** that helped manufacture the
> cycle.

`AroundFunc` returns `NoopDone` when `ShouldEmitTelemetry` is false (`core/telemetry.go:63`): a
suppressed repeated call starts **no span**, so during its resolution the ambient active span stays
the *parent*. A singleflight wait edge is emitted with `trace.SpanFromContext(ctx)` as the waiter
(`AddSingleflightWaitEdge`, `dagql/cache.go:3950`) — so a **suppressed re-entrant `moduleSource`
call that joins another execution emits its wait attributed to its parent** (e.g. a `withName`
span), producing the **semantically impossible "`withName` joins `moduleSource`" edge** (different
recipe digests can never singleflight-join) that tangles the cycle (§RC-cycle 3a). Beyond the
cycle, this corrupts the causal graph and the work-type/identity breakdown generally. Native records
every caller (incl. hits), so it doesn't mis-attribute this way. Fix is load-bearing: a call on the
join path must not be suppressed (so its wait identifies the real joiner).

### RC6 — Coverage gap: ~half of a real build is un-augmented buildkit/IO, and the analyzer runs anyway  ·  **DESIGN (scope) + product-gating**  ·  [confirmed-empirical]

4133/8940 ops on `defb` have `kind=unknown`, no work-type, **no wait edges**: the root
`engine-dev container sync`, 2924 `POST /query`, and all `HTTP GET / fetching / copy /
pulling / resolving / git` (buildkit + IO — the actual heavy lifting). The emit
augmentation touches dagql/core choke points but **not** buildkit/IO, which is a
*separate span source*. So even a fully-augmented engine emits a ~half-unaugmented trace
for a real build, and that half is modeled by nesting + implicit-join alone (no causal
edges). Worse, the analyzer treats a low-fidelity trace as valid: missing `wcprof.kind`
only prints a diagnostic (`report.go:305`) and the counterfactual still runs and ranks.
There is **no fidelity gate** that refuses or degrades when required augmentation is
absent — so an authoritative-looking but causally-meaningless answer is produced. The
design's Phase-0 spike confirmed Cloud *returns* links/attrs/events but never measured
*what fraction of a real build is augmentable.*

### RC7 — Fallback anchors (273)  ·  mechanism **[confirmed-code]**, exact classification **[hypothesis]**

`finish()` fallback-anchors a child whose parent is **in-flight** (mid-replay)
(`replay.go:350-364`) — pervasive once wait edges point *within* the single tree
(joiner and first-caller in the same session; first-triggerer waiting on its own nested
resume span). Mostly downstream of RC1/RC2/RC3 (invalid parent tree + cycles). Both
investigations agree the count is a structural consequence; neither exhaustively
classified all 273 (a `DriftOrigins`/graph-dump next-round task).

### RC-cycle — the chunk-1 finding: the 8 cycles are MANUFACTURED by the model, and the root is the replay  ·  **DESIGN**  ·  [confirmed-code+empirical]

Implementing the loader fixes (chunk 1) and running the real-data gate ("8 `defb` cycles → 0")
**failed**: still 8 cycles, drift +64.6%, and the ancestor-targeting cycle-breaker dropped **none**
(`DroppedCycleWaits=0`). Investigation gave the definitive root cause. **The cycles are manufactured
by our analysis; the data is acyclic** (the run finished in 137s).

**The canonical cycle** (one of the 8, all ~0 duration at ~24.74s): two `Query.moduleSource`
evaluations run concurrently; each, mid-resolution, makes a re-entrant `moduleSource`-keyed call
that **singleflight-joins the other in-flight execution**:
`exec_A → (nested) → reentrant_B → (singleflight wait) → exec_C → (nested) → reentrant_D →
(singleflight wait) → exec_A`. Three tangled causes:

- **3a — suppression mis-attribution (load-bearing; = RC5).** A suppressed re-entrant joiner emits
  its singleflight wait on its *parent* span → the impossible "`withName` joins `moduleSource`"
  edge. Compounds the tangle. The cycle forms **even without** 3a (genuine re-entrant concurrent
  joins suffice), but 3a corrupts attribution.
- **3b — the replay discards timing (THE ROOT; shared with native).** `actWaitJoin` pins the joiner
  to the target's **full recursive simulated finish** (`replay.go:~406`, `clock = max(clock,
  finish(target))`), **ignoring the recorded `[waitStart,waitEnd]` window**. Two mutual near-instant
  joins (real blocked time ≈ 0) become `A ≥ finish(C) ∧ C ≥ finish(A)` → the recursive `finish()`
  re-enters an in-flight op → "cycle." Same disease as the lazy bug (temporal adjacency treated as
  hard synchronous causality, timing discarded). **Persists with explicit `call_exec` emit**, and
  **native shares the replay + the op/wait structure → native almost certainly exhibits the
  identical cycle** (the cross-source oracle on a re-entrant-concurrent-module workload is the
  arbiter). This is *the* thing to fix, and it lives in the shared replay, not the OTel loader.
- **3c — the loader synthesis is anti-inference (must be deleted regardless).** `synthesizeCallExec`
  *invents* `call_exec` nodes not in the trace and *reparents real spans into them by interval
  containment* (+ retargets waits); `synthesizeExecPhases`/processRun reparenting is the same. This
  is exactly the causal guessing the anti-inference invariant forbids — native records these as
  *explicit* nodes. It manufactures an equivalent cycle in OTel, but is a separate breach: it must
  be **deleted** (not guarded) on principle, independent of whether it caused this particular cycle.

**Erik's ruling (internalized):** a known falsehood in the foundation is **disqualifying regardless
of its (negligible) time-impact** — the cycle is a *canary* for unsound inference + timing-discard.
**No masking** (no edge-dropping / "break-and-count"), **no unjustified inference**, **no
"it's small so it's fine."** The earlier "negligible/not-urgent" framing was the real failure, and
it traced to hand-waves carried from this very findings doc (RC5 "minor"; RC3 "guard the reparent";
treating the replay core as untouchable). The rework (`wcprof-otel-rewrite-plan.md`) deletes the
inference (3c), fixes the replay to honor recorded timing so cycles are impossible by construction
(3b), fixes suppression attribution (3a), and removes all cycle-masking.

### Symptom → cause map

| Symptom | Primary cause(s) | Class |
|---|---|---|
| +65% over-serialization | RC2 (prime) + RC4, cascaded via RC1 | design |
| 8 broken cycles | RC-cycle 3b (replay discards wait window; shared w/ native) + 3a/3c | design |
| 273 fallback anchors | RC1/RC2 + RC-cycle downstream (invalid parent tree + cycles) | design |
| huge real ops save 0ns / phantom `lazy` #1 | RC2 | design |
| Σself ≈ 1344s ≫ makespan | RC2(a) + RC4 double-counts | design |
| un-augmented "nonsense" | RC6 (no fidelity gate) | design/product |

---

## 3. Trustworthy vs suspect

**Trustworthy:**
- The **native wcprof model + replay** (`graph.go` self-time, `replay.go`
  counterfactual) on *valid native graphs* — the original, drift-validated foundation.
- The **op-set generalization** (`opset.go`, per-op `opFactor`) — a clean, faithful
  generalization of the per-class what-if; works on native dumps day one.
- **Ingest plumbing** (`wcotel` ProfSpan/`Dedup`/otlpdump + Cloud readers) — preserves
  attrs/events/links and integer timestamps; the only known Cloud gap (dropped-counts
  not exposed) is documented.
- **Emit-side attrs and wait-windows themselves** — the `kind/work/owner` attrs and the
  `wcprof.wait.*` windows look correctly computed; the **exec** propagation/log split
  (R-C) is structurally sound (real propagation parent + separate log target).
- The branch's "matched native exactly on simple workloads" claim is *plausibly true* —
  on shallow-lazy / no-collision traces the RC2/RC3 divergences are tiny (303c = +5.9%),
  which is precisely why it passed and why the validation was under-powered.

**Suspect:**
- The premise that OTel `parentId` is the runtime structural tree (**RC1**), especially
  for **lazy/resumed work** (**RC2**). The lazy path never got the propagation/log split
  the exec path did — runtime structure and UI/log provenance are still conflated there.
- `call_exec` child reparenting by timestamp containment (**RC3**).
- `processRun` modeled as a full-duration leaf for module execs (**RC4**).
- Importing all `unknown` spans as scheduled work with no fidelity gate (**RC6**).
- **Availability elision** (`elideAvailability`, `loadotel.go:169`) — plausible but
  **not validated** on a real complex service trace; the 100.99s `processRun` classed
  `user_process` (not `availability`) is either a legitimate long `go build` or a missed
  service-daemon classification — unresolved. **(Resolved in the rework plan §1.4a:** kept as the
  one permitted tag-driven loader pass — explicitly carved out from the no-inference principle,
  since it's classification-driven and up-only, not interval-inference — and validated by a service
  unit fixture (§4.1) + the daemon-availability injection (§4.4).)*
- **Every absolute counterfactual number** the design changelog presents as validated:
  measured on workloads too simple to exercise the failure.

---

## 4. Validation gap

**What should have caught this and didn't.** The design specified the right instruments
(standing simulated-vs-actual drift gate; cross-source native↔OTel oracle) but **never
ran them on a representative complex workload.** They were exercised on trivially simple
traces where RC2/RC3 are invisible. The +65% / 8-cycles / 273-fallbacks were behind a
one-line summary the moment a real build was analyzed. There is **no cross-source oracle,
no real-Cloud-trace regression fixture, and no graph-invariant check** checked in; the
focused unit tests are idealized and pass. The changelog's confidence ("−0.0%",
"matched native EXACTLY") is **survivorship bias from an under-powered test set.**

**What a robust harness must add (loud, early, automatic):**

1. **Hard structural invariants in the loader, every trace, fail-fast:**
   - dependency-graph **acyclicity** (cycles == 0; any cycle is a bug);
   - **no parent-interval violations** (`child.End ≤ parent.End + ε`) — would have fired
     1635 times on `defb`;
   - fallback anchors **≈ 0**;
   - **no single op's self-time > makespan** (the 62.81s-in-137s trips instantly);
   - flag synthetic leaves (`resume`/`processRun`) whose interval is also covered by a
     real descendant subtree (the **duplication detector**);
   - flag total Σself / makespan beyond a sane parallelism bound.
   These are cheap and each would have failed on day one.
2. **Standing drift gate on a *representative* workload** (the engine build), not a
   hello-world — treat >~10–15% as stop-and-look, as the design intended but never
   enforced at scale. Check in `defb713e…` as a **failing regression fixture**.
3. **Cross-source oracle on a complex workload, per-class self-time** (native vs OTel,
   same run, with the design's mapping table + exclusions). `lazy:resume*` self-time is
   ~glue in native and ~370s in OTel; that one divergence *is* RC2, caught mechanically.
4. **Known-answer injection on a complex trace** — serial vs parallel sleep (parallel
   asserted *not* ranked), plus lazy, singleflight, service-start, and daemon-availability
   cases — directly testing the over-serialization the tool exists to avoid.
5. **A fidelity gate:** counterfactuals are declared invalid (refuse or degrade, don't
   emit confident rankings) when required wcprof coverage is absent or
   drift/cycles/fallbacks exceed threshold (closes RC6's product hole).
6. Expose the existing **`DriftOrigins`/`BaselineDrift`/`ExplainFinish`** via a
   `--diagnose` path so "where did sim-finish inflate?" is one command, not dead code.

---

## 5. Confirmation backlog (next round — not done here)

These would convert the remaining [strong-hypothesis]/[hypothesis] items to confirmed:

- **`DriftOrigins` on `defb`** → confirm RC2 dominance (expect `lazy:resume*` as top
  drift origins) and **enumerate the 8 cycles' anchor ops** (confirm RC3 is the only/
  main cycle source). Cheapest highest-value step.
- **Re-derive the 1635 parent-interval-violation count** independently (Claude did not
  re-run Codex's overlay diagnostic; mechanism is code-confirmed, count is Codex-measured).
- **Classify the 273 fallback anchors** by cause (RC1 vs RC2 vs RC3).
- **Resolve the 100.99s `processRun`**: real `go build` vs missed service-daemon
  availability classification (validates `elideAvailability`).
- **Fresh augmented otlpdump capture** + `jq` dissection of resume-span child counts and
  parent intervals — ground-truth cross-check of RC2 on a controlled local run.

---

## 6. Credits (independent convergence)

- **Convergent (both investigations, independently):** RC1 root mismatch; RC2 lazy-resume
  mechanism (`resumedCallbackSpan.SpanContext()` → install span); reproduction numbers;
  the validation gap and the "no fidelity gate for unaugmented traces" framing.
- **Codex contributed (verified here against code):** the **parent-interval-violation**
  framing + measured counts (1635 / `call→lazy` 1113 / ~47m32s) [RC2(b)]; the **named
  cycle SCC** `call_exec:Query.moduleSource ↔ ModuleSource.withName` pinned to the
  containment reparent at `loadotel_synth.go:135` [RC3]; the observation that the **exec
  split is cleaner than the lazy path** (the lazy path lacks an equivalent split).
- **Claude contributed:** the childless-resume self-time **double-count** framing [RC2(a)];
  the **`processRun` leaf double-count** for module execs [RC4]; **cache-hit/repeat
  suppression holes** [RC5]; the **buildkit/IO coverage** quantification (~46%, separate
  span source) [RC6]; the **root-mode invariance** and **lazy-density gradient**
  experiments; the precise **native `BeginOp` op-stack** contrast (`record.go:101,121`).

---

*No code was changed and nothing was committed during this investigation. The analyzer
binary was built to `bin/wcprof-analyze` for reproduction only.*
