# Resource-constraint-aware wcprof simulation — full technical design

Status: PHASE-2 DESIGN, in adversarial review. Follows the phase-1 feasibility
assessment (hack/designs/resource-aware-simulation-feasibility.md, commit
b865c9d6b); the recommended shape there (fluid work-conserving sharing +
measured-evidence layer) is the shape designed here. This document is
self-contained: it restates everything it needs, and defines every term at
first use. The HTML page form of this design (what Erik reviews) renders the
same content with figures; this markdown is the in-repo record.

Claims discipline: statements about current code carry file:line citations
read firsthand. Statements from the scheduling literature are labeled
"(literature)". Numeric constants that are design choices (not measurements)
are declared as such, with rationale and a revisit trigger.

---

## Table of contents

- §1 What this is and what it answers
- §2 Vocabulary: the recorded graph, the replay, and the three
  infinite-resource points
- §3 The machine and demand model (exact semantics)
- §4 The capacity-aware event loop (exact algorithm)
- §5 Worked examples (used by tests, figures, and the catalog)
- §6 The emits: field-by-field, justified or refused
- §7 Loader, graph, and per-source capability changes
- §8 The measured-evidence layer and the "more CPUs?" verdict
- §9 Report and CLI design
- §10 Gates: every refusal and residual, with exact conditions
- §11 Validation: reason-derived catalog + real-engine calibration
- §12 Performance and complexity
- §13 Composition with what-if-cached
- §14 Seams named, not built
- §15 Ratification status of the four defaults
- Appendix A: aggregate engine-usage lane (designed, deactivated)
- Appendix B: time-series demand refinement (B2, deferred)

---

## §1 What this is and what it answers

wcprof is the engine's wall-clock profiler: it records timed operation
intervals ("ops"), blocked-on intervals ("waits"), and correlations ("links")
at the engine's choke points, and an offline analyzer (wcanalyze) rebuilds
the causal graph and replays it under counterfactual hypotheses ("what if
this class were 2× faster", "what if these results had been cache hits").

The replay today assumes an infinite machine. That is stated in the code
(engine/wcprof/wcanalyze/replay.go:11-13: "assuming unlimited resources
(never CPU/disk bound)") and in the README (engine/wcprof/README.md:121-124).
The assumption is harmless for hypotheses that only *remove or shrink* work
on an otherwise-unchanged schedule, and it becomes the dominant error source
for hypotheses that *increase concurrency* — which is exactly what
cache-elision counterfactuals do when they pull work earlier and pack it
together.

This design adds a **capacity-aware replay mode**: the same recorded graph,
the same compiled per-op timelines, executed under a finite CPU capacity,
with per-exec CPU demands taken from measurement. It answers, honestly:

- (a) When a counterfactual parallelizes a run *more*, where does added
  parallelism stop paying because the machine is full?
- (b) Would more CPUs have made this run faster? (Answered in layers:
  a measured verdict, a measured bound, and simulated relief — never a
  fabricated point prediction. §8.)
- (c) (Down-payment only) Would this run fit on a smaller machine, and at
  what cost? Full multi-machine planning is a named seam (§14).

Two prior rulings bound everything here:

- Only user execs (container processes) have usable resource measurements.
  Engine-internal operations are **never** attributed resource usage.
- The simulator only ever *executes a fixed, documented scheduling policy*
  under a hypothesis. It never searches for a good schedule (that is the
  NP-hard problem, and it is out).

---

## §2 Vocabulary: the recorded graph, the replay, and the three infinite-resource points

Terms used throughout, defined once:

- **Op**: one recorded operation interval, with identity (kind, class,
  ident), timestamps StartNS/EndNS, a structural parent, children, and wait
  edges (engine/wcprof/wcanalyze/graph.go:18-86). All times are nanoseconds
  relative to the recorder's epoch.
- **Exec op**: an op of kind `exec` — one container run. Created around the
  whole container lifecycle in engine/engineutil/executor.go:135-146
  (`exec.run`), with one child op per setup phase (`exec.<phaseName>`,
  kind `exec_phase`, executor.go:216-231), the last of which,
  `exec.runContainer`, contains the actual container lifetime. Inside that
  phase, the profiler records two further exec_phase ops — **grandchildren**
  of exec.run — splitting engine overhead from user runtime:
  `exec.containerStart` (create/start) and `exec.processRun` (the user
  process, WorkType user), recorded with the runContainer phase as their
  parent (executor_spec.go:1429-1434). A never-started exec records no
  processRun (executor_spec.go:1432-1434).
- **Open op**: an op that had not ended when the capture was dumped; its
  EndNS is the dump time and it is flagged Open (graph.go:73-74, 391-412).
- **Hit-short op**: in a what-if-cached simulation, a call of a cached
  digest: it finishes at its start plus the hypothesis pull cost, and its
  recorded self-time, waits, and children are not replayed
  (replay.go:482-489).
- **Self segments**: an op's interval minus its children's intervals and its
  own wait intervals — "the time the op was plausibly doing its own work"
  (graph.go:589-606).
- **Makespan**: latest root finish minus earliest root start; the run's
  simulated wall-clock (replay.go:400-430).
- **The replay**: each op's timeline is compiled once into a flat action
  program — self segments, child spawns, waits — sorted by recorded time
  (compileProgram, replay.go:138-280). A simulation executes these programs
  and derives each op's simulated start/finish.
- **Fixed-delay wait**: a wait on a named resource (a lock). Replayed as a
  recorded-duration delay that runs concurrently with the op's other work
  (replay.go:79-87, 603-610). This design does not change that.

**The three infinite-resource points**, precisely:

1. **Spawns are free.** A spawn action anchors the child at the parent's
   current simulated clock, unconditionally: `s.setStart(a.ref, clock)`
   (replay.go:580). Unbounded concurrency.
2. **Durations are exogenous.** A self segment advances the op's clock by
   its recorded duration times the hypothesis factor:
   `clock += int64(float64(a.dur) * factor)` (replay.go:565). No contention
   can stretch it.
3. **Lock delays are frozen** (replay.go:603-610). Kept frozen in this
   design; the already-documented understatement direction stands.

**What must structurally change.** The current simulation is a lazy memoized
recursion — `finish(i)` recursively finishes dependencies on demand
(replay.go:451-503). An op's schedule depends only on its dependencies, which
is what makes the replay order-independent and each simulation a cheap array
DP. Under finite capacity, an op's progress rate depends on *everything
running at the same instant* — a global coupling. The capacity mode therefore
uses a **global time-ordered event loop** (§4) over the *same compiled
programs*. The infinite mode keeps the recursion untouched; the two modes
share compilation, hypotheses, and reporting.

---

## §3 The machine and demand model (exact semantics)

### §3.1 The machine

One machine with CPU capacity **C**, in cores (fractional allowed). C comes
from the capture header (E-R2, §6.2). All the capacity facts can constrain
the engine *simultaneously* (a container can have both a `cpu.max` quota and
a restricted cpuset), so C is the **minimum over every present constraint**,
never a precedence pick:

    C_rec = min( cpu.max quota/period    [if present and not "max"],
                 |cpuset.cpus.effective|  [if present],
                 runtime.NumCPU()         [present whenever E-R2 is] )

The engine may itself be capacity-limited (a CI engine container with a CPU
quota) — that is why host core count alone would be wrong. The report prints
*each component* and which one bound (the provenance line), not just the
resolved number. If E-R2 is absent entirely (old captures; non-Linux),
capacity mode **refuses** (§10, G1). A CLI override (`-capacity-cores`)
exists for sensitivity analysis and always prints as an override, never
silently.

Terminology used from here on: **C_rec** is the recorded machine's resolved
capacity (above); **C′** is whatever capacity a given simulation runs at —
C_rec by default, a grid multiple or an override otherwise.
Data-consistency checks (gate G5) compare measured demand against **C_rec
only**: simulating at a *smaller* C′ deliberately puts demand above capacity
— that is the sensitivity analysis working as intended, not an
inconsistency.

Memory is NOT part of the scheduling model. Measured per-exec memory peaks
feed feasibility checks and reporting only (§8.4, §9): simulating
memory-constrained *timing* (swap, reclaim, OOM) is refused as unmodelable
from peak counters.

### §3.2 Which ops carry demand

Only **exec ops** (kind `exec`) carry CPU demand, because only they have a
measured cgroup (one cgroup per container exec,
engine/engineutil/executor_spec.go:1220-1242). Every other op — calls,
lazies, session phases, exec setup phases outside the demand window — has
**zero modeled demand and unchanged recorded durations**. This is the
declared model boundary from the prior ruling, restated in every
capacity-mode report (§9): engine-internal CPU use exists, is not modeled,
and biases the model *optimistic about available capacity*.

### §3.3 The demand window

Each exec op carries (from emit E-R1, §6.1):

- **W** — total CPU work of the container, in core-microseconds: the final
  cumulative `usage_usec` from the exec cgroup's `cpu.stat`.
- **[ws, we)** — the **process-run window**: `ws` is the container-started
  boundary (the started-callback instant the profiler already captures for
  the exec split, executor_spec.go:1276-1289); `we` is the **process-exit
  boundary** — the instant `callWithIO` returns
  (executor_spec.go:1411-1424), *before* any cleanup runs. These are exactly
  the boundaries the recorded `exec.processRun` phase op carries
  (executor_spec.go:1430-1431), so the emitted window is redundant with a
  recorded fact — deliberately: the loader cross-checks them (gate G4), and
  a never-started exec (no processRun recorded,
  executor_spec.go:1432-1434) explicitly carries no window. Setup phases
  (image pull, mount prep, network setup) lie outside the window; the
  cgroup has no user processes before `ws`. The cgroup *files* are read
  later, in cleanup (§6.1) — safe because the counters are cumulative and
  the container's processes are dead by then; the (negligible, teardown-
  only) tail between `we` and the read is declared in §11.4.

Define the exec's **in-window self time**:

    SW = Σ length( selfSegment ∩ [ws, we) )
         over the self segments of the exec op and ALL exec_phase ops in
         its nesting subtree (transitive descendants).

Two details of that traversal, both load-bearing:

- **Descendants, not children.** The user-process phase `exec.processRun`
  is a child of the `exec.runContainer` phase — a *grandchild* of exec.run
  (executor_spec.go:1429-1434). A children-only rule would exclude it, and
  since a parent's self segments exclude its children's intervals
  (graph.go:589-606), the runContainer phase's self time is ~zero exactly
  where the window lives — SW would degenerate on every normal exec.
- **No double counting.** Self segments are disjoint from child intervals
  by construction (graph.go:589-606), so summing self segments across the
  exec_phase subtree counts every recorded instant at most once. The
  traversal stops at non-exec_phase boundaries, which is what excludes
  nested-client subtrees (they are call/lazy/exec kinds, §3.5).

Rationale for the wait/child exclusion — and its exact extent: the
exclusion covers recorded waits and child intervals **of the dilation-set
ops themselves** (that is what SelfSegments subtracts). It does NOT cover
**nested-client subtrees**: nested session ops are reparented as children
of the exec op (graph.go:489-502) — *siblings* of the phase chain — so
their intervals overlap `exec.processRun` without being subtracted from
its self segments. That is deliberate, not an accident of the graph shape:
the recorded data carries **no container-side blocking edge** for nested
calls (SDK clients routinely issue them asynchronously and keep
computing), so treating a nested op's span as "the container was blocked"
would be inventing a blocking fact. Demand therefore **continues** through
nested-client overlap; the placement smear this causes for a host that
really did block synchronously is part of the declared simplification
above, with the same B2 remedy. What "demand inactive while blocked"
(§3.5) means is exactly and only: a recorded wait or child interval OF a
dilation-set op suspends demand, because SelfSegments already excludes it.
In practice, for a leaf exec (no nested clients, no mid-run engine waits),
SW ≈ we − ws.

The exec's **demand rate**:

    d = W / SW   (cores)

- d is a *measured average* over the container's own working time. Its
  noise sources and the deliberate choice of a whole-window average (rather
  than a time series) are stated in §11.4 and Appendix B.
- **Declared simplification (attribution within the window).** W is an
  aggregate counter for the whole container; the data cannot say *when*
  within the window the CPU was burned. The model places all of W on the
  in-window self segments — equivalently, it assumes the container's CPU
  use during engine-recorded waits and nested-client children is
  negligible. For a multi-process container that computes in the
  background while its nested client blocks, this misplaces demand in time
  (total W is conserved; the placement, not the amount, is wrong). No
  denominator choice fixes this from an aggregate counter — every choice
  assumes a placement — and guessing a distribution would be compensation.
  So: stated here, restated in the report's method note (§11.4), error
  direction not derivable in general, and the designed empirical remedy is
  the B2 time-series refinement (Appendix B), triggered by calibration
  evidence, not by heuristics.
- d = 0 (a container that used ~no CPU) is legal: such an exec never
  contends and never stretches.
- SW = 0 with W > 0 is degenerate recorded data (work with no working
  time): counted, the op excluded from demand, gate G4 fails (§10). Never
  silently absorbed.
- d is **never capped**. Against the *recorded* capacity, d > C_rec means
  the measured data and the machine facts disagree (e.g. a quota-derived
  C_rec while the cpuset allowed more cores — a provenance defect); it is
  gate G5's condition (§10). Against a *simulated* C′ < C_rec, d > C′ is
  simply a deep deficit: the allocator caps the exec at a = C′ and the
  window stretches by d/C′ — the sensitivity analysis working as intended
  (V-R4, V-R18).

### §3.4 What dilation means (the stretch rule)

At any simulated instant, the **active set** A is the set of exec ops
currently advancing an in-window self segment (not blocked on a join, not in
setup, not finished). Capacity is allocated to A by **max-min fairness with
caps** ("water-filling"):

    sort active demands ascending; repeatedly give each exec
    min(its demand d_i, an equal share of what remains).

Formally: find the unique water level L such that Σ_i min(d_i, L) = C
(or L = ∞ if Σ d_i ≤ C); exec i receives a_i = min(d_i, L).

Each active exec then progresses through its in-window self-segment time at
**rate r_i = a_i / d_i ≤ 1** (recorded-seconds per simulated-second); an
exec with d_i = 0 has r_i = 1 always. This is the **stretch-only rule**:

- If the machine can give every active exec its measured demand, everything
  runs at recorded speed. Nothing ever runs *faster* than recorded — the
  measured demand is a lower bound on what the process would have used on a
  larger machine, and simulating extra speed would be inventing data.
- If demand exceeds capacity, every contended exec's window time dilates by
  d_i/a_i for as long as the deficit lasts.

Why max-min fair sharing and not something else: the real engine imposes no
exec scheduling of its own — `oci-max-parallelism` is parsed
(cmd/engine/main.go:782-793) but consumed nowhere in the Dagger executor
path (verified by exhaustive grep; the only other reference is the config
struct, internal/buildkit/cmd/buildkitd/config/config.go:123-145) — so
containers compete under the kernel's fair scheduler. Max-min fairness is
the standard fluid abstraction of a fair scheduler (literature), it is
deterministic, and it has no integer-slot artifacts (the phase-1 assessment
rejected discrete core slots because measured demands are fractional and
list schedules are non-monotone in processor count — Graham's anomalies
(literature)).

### §3.5 Exactly which recorded time dilates

The **dilation set** of an exec is: the exec op itself plus every
`exec_phase` op in its nesting subtree (transitive descendants; the same
traversal SW uses in §3.3 — in particular it includes the
`exec.processRun` grandchild where the user work actually lives, and stops
at non-exec_phase boundaries, which excludes nested-client subtrees).
Nothing else. Within the dilation set, the portions of self segments inside
[ws, we) dilate; everything outside the window is rigid.

Consequences, each deliberate:

- **Action timestamps shift, ordering is preserved.** Spawns, waits, and
  joins on a dilated timeline are sequenced between self segments in
  recorded order (the compiled program's ordering is by recorded time,
  replay.go:188-211, and ordering constraints are recorded facts —
  the "implicit join" mechanism that bakes observed ordering in,
  replay.go:32-36, compares recorded times and is untouched). Their
  *simulated* occurrence shifts later by the accumulated dilation at that
  point in the timeline.
- **Nested-client subtrees do not dilate — their anchors are remapped
  through the window transform.** A nested client (a dagger CLI inside the
  container) is *in* the cgroup, so its compute is part of W and its
  recorded activity is part of the window; but the engine-side ops it
  triggers (calls, lazies, further execs — reparented under the exec via
  nested-client links, graph.go:489-502) are engine work outside this
  cgroup, and their own timelines are rigid (exec ops underneath carry
  their own windows and demands). Their *starts* must still move with the
  host's stretched work — the host process had to execute to the point of
  issuing the call — and that cannot fall out of the compiled programs
  alone: the spawn action lives on the exec op's program, whose own self
  time is nearly empty (the phases cover it), so dilating the processRun
  grandchild would never move the parent's clock. The mechanism is
  explicit:

  **The window transform M_e.** For exec e, let D_e be its in-window
  dilated self-segment portions in recorded order (§3.3's demand
  intervals), and p(t) = the D_e length in [ws, t] (the recorded
  window-work position of instant t). As the event loop executes e's
  dilated fragments it builds the monotone map M_e from recorded window
  position to sim time (piecewise: proportional-by-rate inside fragments;
  rigid offsets across gaps — the recorded waits/child intervals of
  dilation-set ops, which replay by their own join/fixed-delay
  semantics). A non-exec_phase child of a dilation-set op whose spawn
  falls at recorded t inside the window is anchored at **M_e(t)**: the
  event loop registers a deferred spawn against the transform and fires
  it when the window's executed position reaches p(t). Outside the
  window, spawns anchor at the local clock as usual. If the window's
  execution never reaches p(t) (truncated by unfaithful data), the
  deferred spawn fires at the window's end image and a defensive counter
  (gate G6 family) records it — never silent.
- **Demand is inactive while blocked — exactly where the data says
  blocked.** A recorded wait or child interval OF a dilation-set op is a
  gap in D_e: the exec leaves the active set there (SelfSegments already
  excludes those intervals from SW, so the measured W was never placed on
  them). A *sibling* nested-client overlap is NOT a recorded block and
  does not suspend demand (§3.3 — no invented blocking facts).
- **Setup phases are rigid.** Their recorded durations (image pull etc.)
  replay as today. Their CPU/IO cost is engine-side and unattributed —
  restated as the §3.2 boundary.

### §3.6 What deliberately does NOT change

- The infinite-resource mode: default, byte-for-byte untouched.
- Compiled programs (replay.go:138-280): shared by both modes.
- Fixed-delay lock waits: recorded durations, both modes.
- What-if-cached hypothesis resolution (elide-or-keep pre-pass): identical;
  the capacity mode only changes the replay executor underneath it (§13).
- The what-if class ranking table (up to 200 classes × factors,
  replay.go:797-891): stays infinite-mode in v1, with a printed label
  (§9); rationale in §12.

---

## §4 The capacity-aware event loop (exact algorithm)

### §4.1 State

Per op (dense arrays, same indexing as the compiled program):

- `pc` — index of the next action in the op's compiled timeline.
- `localClock` — the op's simulated clock (as in the current replay).
- `status` — one of: notStarted, runnable, inSelf(remaining recorded ns,
  dilated?), blockedOnJoin(target), blockedOnPend(child), finished.
- For exec ops: window [ws,we), work W, in-window self time SW, demand d
  (precomputed at graph load), plus a running ledger of allocated
  core-seconds (for the conservation gate G7).

Global:

- An event queue (binary heap) of (simTime, rank, opID, generation)
  entries. `rank` breaks ties: gate-events before spawn-events before
  segment-completions before bookkeeping — the same relative order the
  compiled programs use at equal recorded times (replay.go:188-211) so that
  equal-time semantics match the infinite mode. `opID` breaks remaining
  ties. `generation` invalidates superseded entries (the standard
  discrete-event trick for re-projection: stale entries are popped and
  discarded).
- The active set A: exec ops currently inSelf on a dilated segment, with
  current allocations a_i (recomputed only at §4.3 events).
- A reverse-wait index: op → waiters blocked on its finish (built lazily;
  the compiled program already knows every join's target).

### §4.2 Execution

Initialization: every root op is scheduled at its recorded start — an exact
fact, exactly as the current replay pre-anchors roots (replay.go:408-419).

An op's coroutine, when it runs at sim time t, executes its compiled actions
in order, exactly mirroring the infinite replay's action semantics
(replay.go:560-617) with these operational differences:

- **actSelf (non-dilated, or outside the window)**: schedule a completion
  event at localClock + dur × factor. (Hypothesis factors compose exactly
  as today.)
- **actSelf (dilated portion)**: enter the active set with remaining
  recorded time; trigger a rate event (§4.3). Completion is projected at
  localClock + remaining/r_i and re-projected whenever rates change. A self
  segment straddling the window boundary is split at the boundary (rigid
  part, then dilated part, or vice versa) — in a **capacity-mode-only
  precompute overlay** (derived arrays keyed by action index), never in the
  shared compiled program: the infinite mode keeps executing the untouched
  program byte-for-byte, which is what makes gate G8/V-R1 hold by
  construction rather than by re-verification.

  **Fragment rounding rule** (what makes the C′=∞ equivalence exact to the
  nanosecond): the current replay rounds once per action —
  `int64(float64(dur) × factor)` (replay.go:565). Splitting first and
  rounding each fragment independently could differ by ±1ns per fragment.
  The overlay therefore computes fragment lengths as differences of
  *cumulative* rounded positions: fragment i spans
  `round(prefix_{i+1} × f) − round(prefix_i × f)` where prefix_i is the
  fragment's recorded start offset within the action. The lengths
  telescope to exactly the once-rounded total, so any fragmentation
  reproduces the unsplit action's arithmetic (asserted in V-R15).
- **Hypothesis factors on dilated segments (exact rule)**: a class factor f
  applied to a demand-carrying exec scales each dilated self segment's
  recorded time by f AND scales the exec's conservation target to
  **W′ = f·W** (the simulated work), leaving the demand rate d unchanged.
  Interpretation: "this class is f× faster" means the same job takes f×
  the time at the same CPU intensity — both the seconds and the
  core-seconds shrink together. W remains untouched as the *measured*
  provenance value; every simulated ledger, the G7 conservation check, and
  the report's stretched-exec accounting use W′ (= W when no factor
  applies). Consequences, all asserted by V-R16: G7 checks delivered
  core-time against W′; d (the allocator's input) is factor-invariant; at
  C′=∞ the dilated segment's simulated length is exactly the once-rounded
  dur × f, matching the infinite mode (G8). Any other composition (scaling
  time but not work) would make a "faster" hypothesis change the exec's
  CPU intensity — a physical claim the hypothesis does not make.
- **actSpawn**: set the child's start to the current localClock and
  schedule its first event — same anchor rule as replay.go:580, but
  executed forward in time. No out-of-order prefix replay exists in this
  mode: children are reached when their parents reach them. (The current
  recursion's `spawnTo`/prefix machinery, replay.go:627-678, is
  specifically a lazy-evaluation device; a forward event loop does not need
  it. On faithful data both strategies anchor every child identically — a
  catalog assertion, V-R1.)
- **actWaitJoin**: if the target is finished, localClock = max(localClock,
  target's finish) and continue; else block (status blockedOnJoin), and on
  the target's finish, wake at that time.
- **Implicit joins** (the pend list, replay.go:520-558): unchanged
  *ordering* semantics — before executing an action at recorded time `at`,
  join every child whose recorded end ≤ `at` — but "join" now means "block
  until that child's simulated finish" instead of recursive evaluation.
- **actWaitFixedStart/End** (locks): same max-gate arithmetic as
  replay.go:603-610, on the local clock.
- **What-if-cached states**: elided ops are never scheduled (their spawns
  are skipped, waived where the resolution says so), hit-short ops finish
  at start + pullCost — the identical skip/waive logic the current
  simulation applies (replay.go:456-489, 567-599), executed by this loop.

An op finishes when its program is exhausted plus the final implicit join
sweep (mirror of replay.go:616); its waiters wake.

### §4.3 Rate events

Rates change only when the active set or its demands change:

- a dilated segment starts (op enters A),
- a dilated segment completes or its op blocks on a join (op leaves A).

At each such event: recompute water-filling over A (O(|A| log |A|), §12);
for every op whose allocation changed, convert its remaining recorded time
under the old rate into the same remaining recorded time under the new rate
and re-project its completion event (bump generation, push new entry).
Between rate events, rates are constant — the simulation is exact, not
time-stepped: no numerical integration, no dt, closed-form piecewise-linear
progress.

### §4.4 Termination, faithfulness signals, determinism

- **Quiescence with unfinished ops** = the recorded causal structure could
  not schedule itself forward (a recorded cycle of waits, an inverted
  reference, a child its parent never spawns). These are exactly the
  unfaithful-data classes the current replay counts (CycleWarnings,
  UnschedulableOps, replay.go:326-343). The event loop detects them
  *structurally*: nothing runnable, ops unfinished. Handling: count per op
  (gate G6 fails), break deterministically — anchor the lowest-op-ID
  blocked op at its recorded offset from its nearest started ancestor
  (the same damage-bounding anchor the recursion uses,
  replay.go:694-704) — and continue. Never silent, never absorbed.
- **Determinism by construction**: the event order (time, rank, opID) is
  total; water-filling is deterministic; no map iteration order reaches
  results. Catalog V-R11 asserts bit-identical results across input
  permutations. The infinite mode's `SimStartConflicts` order-independence
  signal (replay.go:349-355) has no analog here because a forward loop
  computes each start exactly once; what replaces it is the loop's own
  never-two-starts assertion (a defensive counter, expected 0 always).
- **Machine-seconds conservation** (gate G7): the loop maintains
  Σ_i (allocated core-time of exec i) and asserts, at each exec's window
  completion, that it equals the exec's measured W within fixed-point
  arithmetic error. This is bookkeeping exactness, not a tolerance: the
  fluid model *defines* window completion as "W core-seconds delivered".

---

## §5 Worked examples

These three examples are used identically in: this document, the HTML
figures, and the validation catalog (§11, V-R3/V-R4/V-R5) — hand-derived
expected values, asserted exactly by tests.

### §5.1 W1 — the symmetric pair (the headline example)

Machine C = 4 cores. Two pipelines, each a slow low-CPU producer feeding a
CPU-heavy exec:

- P1: window [0s, 10s), d ≈ 0 → produces input for A.
- P2: window [0s, 20s), d ≈ 0 → produces input for B.
- A: recorded window [10s, 20s), d = 3 (W = 30 core-s), starts when P1 done.
- B: recorded window [20s, 30s), d = 3 (W = 30 core-s), starts when P2 done.

Recorded/baseline makespan: 30s. In the catalog scenario the producers'
demand is exactly 0 (the figure draws them with a token visual height,
labeled d≈0); recorded concurrency never exceeds d_A = 3 ≤ 4, so the
baseline capacity replay stretches nothing and reproduces 30s exactly
(gate G2 behavior).

Counterfactual: what-if-cached {P1, P2} — both producers elided, A and B
start at t = 0 together.

- **Infinite mode**: A and B run concurrently at recorded durations:
  makespan = 10s. (Over-promise: it schedules 6 cores of demand on a
  4-core machine.)
- **Capacity mode**: active set {A: d=3, B: d=3}, C = 4. Water level:
  2 each (both capped above the equal share). Rates r = 2/3. Both windows
  dilate ×1.5: both finish at **15s**. Work check: each delivers
  2 cores × 15s = 30 core-s = W. ✓
- **Sensitivity**: makespan(C) = max(10, 60/C) for this counterfactual —
  30s at C=2, 15s at C=4, 10s at C≥6 (the plateau: beyond 6 cores, added
  capacity buys nothing because structure, not capacity, binds). This
  curve is the sensitivity-grid figure and catalog entry V-R3.

### §5.2 W2 — water-filling with mixed demands

Active set {d=0.5, d=1.5, d=3.0} on C = 4. Ascending pass: 0.5 ≤ 4/3 —
satisfied; remaining C = 3.5 over 2 execs, share 1.75: 1.5 ≤ 1.75 —
satisfied; remaining 2.0 to the last: a = min(3.0, 2.0) = 2.0.
Allocations (0.5, 1.5, 2.0); only the d=3 exec stretches (r = 2/3).
Catalog V-R4 asserts these numbers, plus the no-contention case
(Σd ≤ C ⇒ a_i = d_i for all).

### §5.3 W3 — mid-flight entry and re-projection

Counterfactual state of W1 (A and B active from t=0, d=3 each, C=4), plus a
root-anchored exec Z: recorded window [5s, 10s), d = 1.

- t=0: A,B enter. Water level 2: allocations (2, 2). Projected completions:
  t=15 each.
- t=5: Z enters. Demands {3, 3, 1}: 1 ≤ 4/3 — Z satisfied (a=1, r=1);
  remaining 3 over A,B: (1.5, 1.5), r = 1/2. Re-projection: A and B each
  have 30 − 2×5 = 20 core-s remaining.
- t=10: Z's 5s window completes at full rate (5 core-s delivered ✓).
  A,B each delivered 1.5×5 = 7.5 more (12.5 core-s remaining); allocations
  back to (2, 2).
- t=16.25: A and B complete (12.5 / 2 = 6.25s after t=10).

Conservation: total delivered = 65 core-s = 30+30+5. Machine-time:
4×5 + 4×5 + 4×6.25 = 65. ✓ Catalog V-R5 asserts the event times and final
makespan 16.25s exactly. This example is the worked event-loop timeline
figure: four events, three water-filling solves, two re-projections (the
in-flight execs re-project at t=5 and again at t=10).

---

## §6 The emits: field-by-field, justified or refused

Doctrine: every emitted field must have a consumer in the analysis that
*decides something* with it; fields without a deciding consumer are refused
(listed, with reasons, revisited when a consumer exists). All emits are
additive: zero behavior change when wcprof is off; no existing field
changes meaning.

### §6.1 E-R1 — per-exec final resource totals

**Mechanism.** One read of the exec cgroup's files at container exit —
*independent of the OTel metrics sampler and its gates*. The existing
sampler only runs when the exec has a call digest and builds an OTel meter
(executor_spec.go:1221-1242); the wcprof emit must not inherit that gate
(execs without call digests exist — `execIdent` falls back to the raw exec
id, executor.go:130-133 — and a capacity model missing *any* exec's demand
refuses, §10 G3, so the emit must cover every container).

Two boundaries, deliberately different:

- **Timestamps at the process-exit boundary.** WindowStartNS is the
  started-callback instant (executor_spec.go:1276-1289); WindowEndNS is
  captured where `callWithIO` returns (executor_spec.go:1411-1424) —
  the same instants the recorded `exec.processRun` phase uses
  (executor_spec.go:1430-1431). NOT at cleanup time: cleanup latency
  (teardown, cgroup reads) is engine overhead, not container runtime, and
  must not widen the window.
- **File reads in cleanup.** The cgroup files are read in `runContainer`'s
  cleanup path, immediately before the sampler-cancel cleanup, with the
  same path resolution the sampler uses — the cgroup path from the OCI
  spec (`state.spec.Linux.CgroupsPath`, executor_spec.go:1220) joined
  under the cgroupfs mountpoint `/sys/fs/cgroup` exactly as
  `resources.NewSampler` does (sampler.go:14, 40); the raw spec path alone
  is relative and would be the wrong path. Reading after process exit is
  safe: the counters are cumulative and the container's processes are dead
  (the post-`we` tail is teardown-only, declared in §11.4); the ordering
  guarantee that the cgroup still exists is the existing one (cleanups run
  LIFO, util/cleanups/cleanup.go:49-55; the runc-delete cleanup is
  registered earlier at executor_spec.go:1216 and so runs later).

The outer `RunContainer` then attaches the snapshot to the profiler op
before ending it (`execOp` is ended at executor.go:175, after `c.run`'s
deferred cleanups at executor.go:203 have completed — verified ordering).

New recording API: `Op.SetResources(snap ResourceSnapshot)` in
engine/wcprof/record.go, storing fixed-size numeric fields on the op's
event (no string interning involved).

**Fields** (all on the exec op's record; wire encoding: numeric fields on
`Event`/`DumpEvent` with short JSON keys, omitted when the presence bit is
unset):

| field | source file | consumer that decides with it | verdict |
|---|---|---|---|
| CPUUsageUS (uint64, core-µs) | `cpu.stat` `usage_usec` (engine/engineutil/resources/cpustat.go:19) | W in the demand model (§3.3); saturation timeline (§8.1); conservation gate G7 | **EMIT** |
| PSICPUSomeUS (uint64, µs) | `cpu.pressure` `some total` (cpustat.go:145-167, parse at :159) | the "more CPUs?" measured bound and verdict thresholds (§8.2) | **EMIT** |
| PSICPUFullUS (uint64, µs) | `cpu.pressure` `full total` (gauge cpustat.go:129, parse at :159-161) | severity split in the contention ledger (§8.2: `full` = all tasks stalled — pure loss windows) | **EMIT** |
| PSIIOSomeUS (uint64, µs) | `io.pressure` `some total` (file const iostat.go:17, read/parse iostat.go:122-138) | the verdict's IO caveat: "stalls here are IO, more CPUs won't relieve them" (§8.3) | **EMIT** |
| MemPeakBytes (uint64) | `memory.peak` (engine/engineutil/resources/memorystat.go:17) | memory-feasibility annotation on counterfactual schedules (§8.4) | **EMIT** |
| WindowStartNS (int64) | the started-callback boundary already captured (executor_spec.go:1276-1289) | ws in the demand model (§3.3) | **EMIT** |
| WindowEndNS (int64) | the process-exit boundary — where `callWithIO` returns (executor_spec.go:1411-1424), the same instant `exec.processRun` records as its end | we in the demand model; cross-checked against the recorded processRun interval (G4) | **EMIT** |
| Presence bitmask | which files existed/parsed | every gate in §10; absence ≠ zero, per-family (`memory.peak` needs a recent kernel; PSI needs CONFIG_PSI; samplers today silently skip missing files — cpustat.go:82-83, memorystat.go:48-49,92-94 — the emit must not) | **EMIT** |
| CPUUserUS / CPUSystemUS | `cpu.stat` | no analysis decides anything with the user/system split | **REFUSED** (revisit when a consumer exists) |
| MemCurrentBytes | `memory.current` at exit | meaningless at exit; peak dominates every consumer | **REFUSED** |
| IOReadBytes / IOWriteBytes | `io.stat` | no v1 consumer (IO contention not modeled; right-sizing uses memory peak) | **REFUSED** |
| Net* (rx/tx bytes/packets/drops) | netstat sampler | no v1 consumer | **REFUSED** |
| per-sample time series | periodic sampler | Appendix B (B2); no v1 consumer | **DEFERRED** |

Volume: ≤ 7 numeric fields + 1 bitmask, exec ops only. On a capture with
10⁴ execs this is ~10⁵ numbers — negligible against the existing event
volume.

Failure honesty: a failed read (not just missing file — a parse error, a
race with teardown) records the family absent in the bitmask; it never
records zero. An absent bitmask entirely (old engine, wcprof-off runs)
loads as `Resources == nil` (§7).

### §6.2 E-R2 — capture-header machine facts

Read once at dump time (the dump handler already assembles the header;
engine/wcprof/dump.go:23-44), from the engine's own view:

| field | source | consumer | verdict |
|---|---|---|---|
| HostNumCPU (int) | `runtime.NumCPU()` (already used for the container env var, executor_spec.go:1067) | capacity resolution step 3 (§3.1) | **EMIT** |
| EngineCPUMaxMilli (int64, millicores; −1 = "max") | engine cgroup `cpu.max` | capacity resolution step 1 | **EMIT** |
| EngineCPUSetCount (int) | engine cgroup `cpuset.cpus.effective` | capacity resolution step 2 | **EMIT** |
| MemTotalBytes (uint64) | `/proc/meminfo` MemTotal | memory-feasibility annotations (§8.4) | **EMIT** |
| Presence bitmask | which sources resolved | gate G1; provenance line in the report | **EMIT** |
| load averages, engine cgroup memory.max, io limits | — | no v1 consumer | **REFUSED** |

The engine's cgroup path comes from `/proc/self/cgroup`; on non-Linux or on
resolution failure every field is absent (bitmask), and capacity mode
refuses with that stated (G1). Facts are read at dump time; a mid-run limit
change is therefore invisible — stated in the header's provenance line
(revisit trigger: if calibration ever shows drift attributable to mid-run
limit changes, move the read to recorder init and dump both).

### §6.3 Where the OTel source stands

The identical measurements already leave the engine as OTel gauges joined
to spans by attributes (executor_spec.go:1224-1236). Loading them in the
OTel capture path means ingesting a *metrics* stream and joining it to
exec spans — a different pipeline from the span stream the wcotel loader
reads. v1: the OTel source loads `Resources == nil` / `Machine == nil` and
every capacity output refuses with "resource data: not available from this
source" in the per-source capability table (§7). Parity is a named seam.
(Ratification default #3, §15.)

---

## §7 Loader, graph, and per-source capability changes

- `Op` gains `Resources *OpResources` — nil means "not recorded" (old
  captures, OTel source, non-exec ops). `OpResources` carries the E-R1
  fields plus per-family presence booleans decoded from the bitmask.
  Never zero-filled.
- `Graph` gains `Machine *MachineFacts` (nil = not recorded) with ALL the
  E-R2 component fields and their presence flags (the full observed
  constraint set — consumers can see every input), plus the resolved
  capacity C_rec (the §3.1 minimum) and a provenance enum naming which
  constraint was **binding** (quota | cpuset | numcpu | absent). The enum
  means only "which one bound"; the non-binding inputs are not discarded.
- Demand derivation (d, SW, window intersection with self segments) happens
  once at load into dense per-op arrays, alongside the existing program
  compilation; degenerate cases counted for G4.
- The per-source capability table (the analyzer's existing honesty surface
  for native-vs-OTel differences) gains a "resource data" row: native =
  yes (with kernel-dependent families flagged per capture); OTel = no
  (v1).
- `LoadMulti` (multi-dump merges of one recorder run, graph.go:249-284):
  machine facts must agree across dumps of the same epoch; a mismatch is a
  load error (same epoch = same engine process; disagreement means the
  facts changed mid-run — surfaced, not averaged).

---

## §8 The measured-evidence layer and the "more CPUs?" verdict

Everything in this section is computed from *measured* data (E-R1/E-R2),
independent of any simulation. It exists so that the strongest available
answer is always the one given from measurement alone, with simulation
reserved for counterfactual packing.

### §8.1 Saturation timeline

S(t) := Σ of d_i over execs with an **in-window self segment** containing t
— the same demand intervals the simulator charges (§3.3/§3.4: demand is
inactive during recorded waits, nested-client children, and outside the
window). Summing over whole windows instead would fabricate saturation
during known blocking — exactly the intervals the model excludes. S(t) is
piecewise-constant with breakpoints at in-window self-segment edges
(computable exactly by an event sweep; with Appendix B time-series it
refines further within segments). Report values:

- SAT := max_t S(t) / C_rec — peak measured demand as a fraction of the
  recorded machine's capacity.
- The total duration where S(t) > 0.9·C_rec ("saturation windows"), and
  where S(t) > C_rec (measurement/capacity inconsistency — gate G5's
  timeline condition).

### §8.2 CPU contention ledger

Per exec (and aggregated per exec class): PSI some/full stall totals, as
absolute time and as a fraction of the exec's window.
STALL% := Σ PSICPUSomeUS / Σ window durations — the workload-level stall
fraction. `full` totals are split out: `full` time is "all runnable tasks
stalled" — pure loss, the strongest starvation evidence.

### §8.3 The layered verdict (exact rules)

Rendered as a decision list, every line with its numbers:

1. **Refusal**: Machine facts absent, or any exec missing CPU totals, or
   PSI absent on this kernel → the verdict line for the missing layer
   REFUSES with the reason (PSI-absent still permits the saturation layer;
   the verdict then says exactly which evidence is unavailable).
2. **Measured "no" — scoped to what is measured**: SAT < 0.8 AND
   STALL% < 1% → "No evidence in the measured exec population that more
   CPUs would have helped: exec demand never approached capacity
   (SAT = …) and the kernel recorded negligible CPU stalls (STALL% = …)."
   The sentence is *scoped* because the model's boundary (§3.2) is real:
   engine-internal CPU is unmodeled, so low exec-side numbers cannot prove
   the whole run was CPU-unconstrained. The verdict therefore always
   prints its coverage context beside it, as two distinct numbers with
   distinct meanings:
   - **wall-clock coverage** = |union of all modeled demand intervals| /
     makespan — the fraction of the run's wall time during which *any*
     modeled demand was active. A union, not a sum: a sum of parallel
     self times can exceed 100% and would mask exactly the condition
     this line exists to disclose (most wall time outside the modeled
     region while summed parallel self time looks large). Always ≤ 1.
   - **work intensity** = Σ W / (C_rec × makespan) — how much of the
     machine's total capacity the measured exec work accounts for.
   When wall-clock coverage is small, the verdict line itself says the
   measured population explains too little of the run to carry a global
   claim (a refusal of the global phrasing, not a hedge).
3. **Measured evidence of contention**: otherwise print: saturation
   windows (§8.1), the top-N execs by PSI stall (with `full` split),
   Σ stall time as the **measured ceiling** on what more CPU could
   relieve — explicitly labeled a bound from kernel evidence, not a
   prediction — and the IO caveat where PSIIOSomeUS dominates
   PSICPUSomeUS for the stalling execs ("these stalls are IO waits; CPUs
   won't relieve them").
4. **Simulated relief**: capacity-grid re-simulation (§9) of the recorded
   schedule and of any requested hypothesis. For the *recorded* schedule
   at recorded capacity, the stretch-only model finds no relief above C by
   construction (the recorded overlap already fit); the report says this
   in one sentence so the number is never mistaken for "we predict zero
   benefit ever" — the honest claim is "the recorded schedule's structure
   did not queue on capacity; the measured layers above carry the
   within-exec evidence." For *hypotheses* that pack work (where capacity
   binds in-sim), the grid shows exactly how much of the promise returns
   at each capacity step.

The two thresholds (0.8, 1%) are declared design constants for the verdict
sentence boundary only — every underlying number prints regardless — with a
revisit trigger: after the first calibration round (§11.5), re-derive them
from the measured noise floor (CAL-1/CAL-3 data), and record the derivation
in the calibration doc.

### §8.4 Memory feasibility (annotation, never timing)

For any capacity-mode schedule (baseline or counterfactual): sweep
Σ MemPeakBytes over sim-concurrent exec windows; where the sum exceeds
MemTotalBytes, annotate the schedule: "this schedule co-locates exec peaks
summing to X on a machine with Y — infeasible as scheduled; timing impact
NOT modeled". Peaks need not coincide in time within windows, so the sum is
a conservative over-estimate — stated in the annotation text. It never
alters timing (refused: swap/reclaim dynamics are not derivable from peak
counters).

---

## §9 Report and CLI design

CLI (cmd/wcprof-analyze/main.go, plain flag surface as today):

- `-capacity` — enable capacity mode (off by default; every existing
  output is unchanged when off).
- `-capacity-cores <float>` — override resolved C (always printed as an
  override with the resolved value beside it).
- `-capacity-grid "0.5,1,2,4,inf"` — multipliers of C for the sensitivity
  table (default shown; ratification default #4, §15).
- Composes with the existing `-cached*` selector flags (§13): each
  explicit cached hypothesis runs under both modes when `-capacity` is on.

Report additions (order within the existing report):

1. **Machine line**: resolved C + provenance, MemTotal, kernel capability
   flags, and the model-boundary sentence (engine-internal usage
   unmodeled; direction stated).
2. **Exec demand ledger**: top exec classes by total W; per class: W,
   Σ window, mean d, PSI stall totals. Every number's source is a
   measured field.
3. **Saturation + verdict** (§8): timeline summary, contention ledger,
   the layered more-CPUs verdict.
4. **Capacity baseline**: capacity-mode baseline makespan vs infinite-mode
   baseline vs actual; the baseline-stretch residual (G2) with its
   tolerance line.
5. **Sensitivity table**: makespan at each grid point, for the baseline
   and for each explicit hypothesis; the plateau called out (first grid
   point where the makespan stops improving by more than the G2 residual).
6. **Per-hypothesis capacity detail** (when `-cached*` + `-capacity`):
   infinite-mode saving vs capacity-mode saving (the honest number), the
   stretched execs ledger (who stretched, by how much), memory-feasibility
   annotations, and gate/residual lines.

The what-if class ranking table stays infinite-mode with the printed label
"rankings computed under infinite resources — capacity-aware detail
available per hypothesis via -capacity" (rationale: §12; revisit trigger:
if the event loop's measured cost makes ranking-grid capacity sims cheap,
lift the restriction).

---

## §10 Gates: every refusal and residual, exact conditions

Doctrine: gates refuse rather than decorate; residuals print rather than
absorb; a non-zero faithfulness counter means fix the emit, never bend the
model. All counters print with samples (op IDs) capped at 10, as the
existing signals do (replay.go:343, 356).

| gate | condition | action |
|---|---|---|
| **G1 machine facts** | `Machine == nil` or capacity unresolvable from present fields | capacity mode refuses entirely; reason printed; measured-evidence layers that need only E-R1 still render, each with its own gate |
| **G2 baseline residual** | capacity baseline vs infinite baseline: total stretched time and makespan delta | printed always; FAILS if makespan delta > 2% of makespan (declared constant; revisit from CAL-1 measured distribution — the residual on real feasible baselines is measurement noise, and the tolerance must trace to its measured floor, never be widened to make runs pass) |
| **G3 demand coverage** | any non-elided exec op with `Resources == nil` or CPU family absent; any OPEN exec op (no final totals — dump preceded exit) | capacity mode refuses; count + samples printed ("N execs lack resource data: fix the emit / capture after exits") |
| **G4 degenerate demand** | SW = 0 with W > 0; window outside the op interval; we ≤ ws; emitted window disagrees with the recorded `exec.processRun` interval when both exist (§3.3 — two records of the same boundary must match) | counted per op, capacity mode refuses (unfaithful recorded data — window/self-segment structure contradicts measured work) |
| **G5 recorded-capacity consistency** | against **C_rec only** (never a grid C′ or an override — §3.1), both limbs tolerance-governed by the same measured-floor discipline as G2 (constants set by CAL-1): d_i > C_rec·(1+τ_d) for any exec, or S(t) > C_rec on the recorded timeline for more than the declared duration tolerance. (Small overshoots of either limb are quantization/attribution noise — §11.4 — and print as residuals.) | below tolerance: printed residual (measurement noise). Above tolerance: capacity mode **refuses** — the demand data and the machine facts contradict each other (e.g. wrong capacity provenance), and results built on contradicted inputs would be decoration. The measured numbers still print as diagnostics with the refusal. |
| **G6 forward-schedulability** | quiescence with unfinished ops (recorded cycles / inversions / malformed nestings, §4.4); also the defensive deferred-spawn counter (a window truncated before a registered nested anchor, §3.5) | counted per break; capacity results for the run marked FAILED-GATE, mirroring UnschedulableOps/CycleWarnings doctrine (replay.go:326-343) |
| **G7 conservation** | per-exec delivered core-time ≠ **W′** (the simulated work target: f·W under a class factor f, = W otherwise — §4.2) beyond fixed-point error at window completion; raw measured W is provenance only | hard assertion (a bug in the loop, not a data condition): fail the run loudly |
| **G8 mode agreement** | capacity mode at C=∞ differs from infinite mode on any op's times | hard assertion in tests (V-R1); not evaluated at runtime (cost), enforced by the shared-compilation design |

---

## §11 Validation: reason-derived catalog + real-engine calibration

### §11.1 Method

As with the what-if-cached work: for every scenario the expected outcome is
derived by logic *before* running the simulator, then asserted exactly.
Synthetic graphs are constructed in-test (the existing test idiom across
replay_*_test.go); every example in §5 is a catalog entry with its
hand-derived numbers.

### §11.2 Reason-derived catalog (unit)

| id | scenario | exact expectation |
|---|---|---|
| V-R1 | every existing replay test graph + randomized graphs, capacity mode at C=∞ | per-op simStart/simFinish identical to the current Simulation (bit-for-bit); this also proves the forward loop's anchors match the recursion's on faithful data |
| V-R2 | constructed graphs whose windows/demands satisfy S(t) ≤ C everywhere | capacity baseline == infinite baseline exactly (zero stretch, zero residual) |
| V-R3 | W1 (§5.1) at C ∈ {2, 4, 6, 8} | counterfactual makespan = max(10, 60/C)s exactly: 30, 15, 10, 10 |
| V-R4 | W2 (§5.2) allocator unit tests | allocations (0.5, 1.5, 2.0); Σd ≤ C ⇒ a=d; single exec d > C ⇒ a=C; d=0 ⇒ r=1 |
| V-R5 | W3 (§5.3) | events at t=0, 5, 10; final makespan 16.25s; conservation ledger 65 core-s |
| V-R6 | zero-demand execs under heavy contention | never stretch, never affect allocations |
| V-R7 | one exec with Resources=nil / one OPEN exec | G3 refusal, count 1, correct op sampled |
| V-R8 | monotonicity sweep: every synthetic scenario at ascending C grid | makespan non-increasing; any violation fails the test and must be understood and documented before merge (labeled expectation, not theorem — release-time coupling makes a general proof subtle) |
| V-R9 | work/span floors on every catalog run | makespan ≥ Σ(W of scheduled execs)/C and ≥ the infinite-mode critical path — these ARE theorems for any capacity-respecting schedule (literature: work law / span law); asserted at runtime too |
| V-R10 | W1's cached hypothesis under capacity | makespan_capacity(hyp) ≥ makespan_infinite(hyp) — the per-schedule relation (15 ≥ 10 on W1); elided execs never enter the active set (counter = 0). NOTE: the earlier savings-difference inequality (savings_cap ≤ savings_inf) is NOT asserted — it is false in general: if the capacity baseline carries stretch, a contention-relieving hypothesis can save more under capacity than under infinity. The report prints both savings side by side; only the per-schedule ≥ relations are gates. |
| V-R11 | determinism | permuted op/event insertion order ⇒ bit-identical outputs |
| V-R12 | dilated anchors via the window transform (§3.5 M_e) | a nested-client child of the exec op, spawned at recorded t inside a stretched window, starts at the hand-computed M_e(t) (deferred spawn fires when the window's executed position reaches p(t)); the nested subtree's own timeline stays rigid |
| V-R21 | the real sibling topology end-to-end: exec.run → runContainer phase → processRun grandchild, PLUS a nested-client child of exec.run overlapping processRun | processRun's self segments are NOT cut by the sibling nested interval (demand continues through the overlap — the §3.3 no-invented-blocking rule); SW and d hand-derived accordingly; the nested child's spawn remaps through M_e; conservation ledger balances |
| V-R13 | join inside window | exec blocked on a nested op mid-window leaves the active set; the conservation ledger proves no work delivered while blocked |
| V-R14 | recorded cycle / inverted reference under capacity mode | G6 break at the lowest-ID blocked op, counted, gate failed — mirroring the recursion's counters on the same graph |
| V-R15 | window straddling self-segment boundaries; window fully inside a wait; window at op edges; fragmented action under a factor | overlay split correctness (hand-derived per case); fragment lengths telescope to the once-rounded dur×f total (the §4.2 fragment rounding rule) — including the 3ns/f=0.9 adversarial case |
| V-R16 | hypothesis factors × capacity (the §4.2 exact rule) | factor f on a demand-carrying exec scales dilated segment time AND its W share by f with d invariant: G7 target = f·W; at C′=∞ the segment length is exactly f·dur (G8); plus a hand-derived mixed case with a factor-scaled non-exec segment composing with a dilated window |
| V-R17 | real exec anatomy: containerStart + processRun as grandchildren under the runContainer phase (the executor_spec.go:1429-1434 shape) | SW comes out equal to the processRun-window self time (non-zero); a children-only traversal would yield SW=0 — asserted against the descendant rule; emitted window == recorded processRun interval (G4 cross-check passes) |
| V-R18 | downward grid: W1 at C′=2 where d=3 > C′ | valid simulation, no G5: allocator caps a=2, stretch d/a=1.5 applies — G5 fires only against C_rec (§3.1 terminology) |
| V-R19 | an exec with a mid-window recorded wait OWNED BY a dilation-set op (e.g. a lock wait on the runContainer phase) | S(t) excludes the wait interval (saturation over in-window self segments, §8.1); the sim's active set drops the exec for the same interval — the two use identical demand intervals by construction. Contrast with V-R21: a *sibling* nested overlap suspends nothing. |
| V-R20 | capacity resolution with quota=8 cores, cpuset=4 cores, NumCPU=16 | C_rec = 4 (the minimum), provenance line lists all three and marks cpuset as binding — never a precedence pick |

### §11.3 Runtime gates

G1–G7 (§10) evaluate on every capacity-mode run; the report prints each
gate's line (pass, value, threshold+provenance where applicable).

### §11.4 Noise: stated, bounded, never compensated

Known noise sources, each stated in the report's method note: 5s sampling
does not affect final totals (cumulative counters; the final sample is
end-of-life — §6.1); window timestamps are callback-time, not cgroup-file
time (sub-ms skew); PSI totals include kernel bookkeeping granularity;
whole-window averaging smears bursts (Appendix B is the designed
refinement, deferred until CAL data shows it is the dominant error); the
in-window placement of the aggregate W is the §3.3 declared simplification
(the container is assumed CPU-idle during engine-recorded waits — total W
conserved, placement not derivable from an aggregate counter); the post-we
teardown tail is included in W but not in the window (negligible,
teardown-only). None of these is corrected for; they are why G2/G5 have
measured-floor tolerances instead of zero.

### §11.5 Real-engine calibration (evidence in hack/logs/, per standing rules)

Method mirrors the what-if-cached calibration harness (same workloads, same
dump discipline), with the advantage that capacity is *directly
manipulable*:

- **CAL-1 (baseline residual floor)**: N unconstrained captures across the
  workload set; distribution of G2 residuals; sets/validates the G2 and
  G5 tolerance constants (documented in the calibration doc, constants
  updated by evidence only).
- **CAL-2 (constrained prediction, both directions)**: capture on an
  unconstrained engine; predict makespan at C=k (engine cores / 4, via
  grid); rerun the same workload on the same engine under cgroup
  `cpu.max = k`; compare — per-class stretch and makespan, decomposed gap
  sources per the calibration-doc precedent (context lines, no single
  grade). Then the reverse: capture constrained, predict at C′=host,
  compare with the unconstrained run. The honest expectation, stated
  before running: stretch-only semantics predicts (approximately) **no
  relief** in this direction — the constrained capture's demands were
  measured under throttling, so they fit its C_rec, and raising capacity
  un-stretches nothing. The comparison against the real unconstrained run
  therefore *quantifies the under-prediction gap* — the
  observed-demand-lower-bound limit made empirical — and the calibration
  reports that gap as the characterized limit, never as model failure and
  never as something to tune away. As a companion characterization (not an
  assertion): compare the constrained run's PSI stall totals against the
  actually-measured speedup, to characterize how the §8.3 measured-ceiling
  layer relates to realized relief on this workload set.
- **CAL-3 (evidence-layer sanity)**: on CAL-2's constrained run, PSI
  stall totals must be large and the verdict must say "yes-evidence"; on
  the unconstrained run, near-zero and "no". A failure here is a data
  pipeline defect (fix the emit), not a model tuning knob.

---

## §12 Performance and complexity

- Events: one per compiled action plus one per dilated-segment
  boundary — O(total actions), the same asymptotic count the recursion
  touches. Heap cost O(log queue).
- Rate events: only exec window entry/exit/block/wake. Water-filling is
  O(|A| log |A|) per rate event; re-projection touches only active dilated
  execs. |A| is the number of *concurrently running containers* — dozens,
  not thousands, on real captures.
- Expected cost vs the current DP: constant-factor worse (heap + ledger
  bookkeeping). Hypothesis, to be measured in implementation and recorded:
  ≤ 5× the flat replay on multi-million-op captures — comfortably fine for
  the places capacity mode runs (baseline + explicit hypotheses +
  grid ≈ tens of sims), and the stated reason the 200-class ranking grid
  stays infinite-mode in v1 (§9; revisit trigger attached there).
- Memory: O(n) dense arrays alongside the existing program (statuses,
  ledgers, queue).

---

## §13 Composition with what-if-cached

The what-if-cached machinery is unchanged through resolution: hypothesis →
static elide-or-keep pre-pass → per-op elision/hit state (cached.go's
selection → resolution → simulation → report flow). The capacity mode
plugs in at the executor level only:

- Elided ops: never scheduled; their spawns skipped or waived exactly per
  the resolution (the same flags the recursion consults,
  replay.go:456-489, 567-599). An elided exec never enters the active set
  and never contends — asserted (V-R10).
- Hit-short ops: finish at start + pullCost; no demand.
- The `ElidedOpDemanded` faithfulness counter carries over unchanged: a
  demand on an elided op in the forward loop is the same pre-pass
  contradiction, counted, gate-failing.
- New relation gate (V-R10, also a runtime print): **per schedule**, for
  the same hypothesis, capacity-mode makespan ≥ infinite-mode makespan
  (and likewise for the baseline) — capacity can only delay a given
  schedule. NOTE the gate is deliberately NOT the savings-difference
  inequality (savings_cap ≤ savings_inf): that is false in general — when
  the capacity baseline carries stretch, a hypothesis that relieves
  contention can save *more* under capacity than under infinity. The
  per-hypothesis report prints both savings side by side — the honest
  headline is the capacity-mode one.

---

## §14 Seams named, not built

- **Ranking under capacity**: per-class capacity-aware rankings once the
  measured event-loop cost allows a 200-sim grid (trigger recorded in §9).
- **OTel parity** (§6.3): metrics-stream ingestion + span join in wcotel.
- **Remote-cache upload intelligence**: the planned cache service consumes
  what-if savings as its value-of-caching input; capacity-aware savings
  make that honest on busy machines. Interface unchanged: hypothesis in,
  honest saving out.
- **Multi-machine planning** (motivating question c): the event loop
  generalizes to multiple capacity pools plus a *simulated assignment
  policy* (never an optimizer). Blocked on data that does not exist yet:
  cross-machine transfer costs, multi-engine traces. The v1 down-payment
  is the downward half of the sensitivity grid (right-sizing).
- **Aggregate engine-usage lane**: Appendix A, designed but deactivated
  pending ratification.

---

## §15 Ratification status of the four defaults

Adopted as design defaults for phase 2. Interim rulings from cache-chief
arrived with the conditional phase-2 GO (2026-07-06); Erik may override any
of them — each is a bounded edit if he does:

1. **Aggregate engine-usage lane: OUT of v1** (Appendix A designed,
   deactivated). **HELD FOR ERIK** — deliberately designed so a later yes
   is an enable, not a redesign. The §3.2 optimism bias stays and is
   stated in every report.
2. **The "more CPUs?" answer is verdict + bound + simulated relief**
   (§8.3), never a point prediction beyond evidence. **Interim GO** — "it
   is the fidelity contract."
3. **Native-only resource data in v1**; OTel loads nil and refuses with
   capability labeling (§6.3). **Interim GO** — matches the per-source
   capability honesty precedent.
4. **Sensitivity grid default C × {0.5, 1, 2, 4, ∞}**, configurable via
   `-capacity-grid`. **Interim GO** — as a flag, which it is (§9).

---

## Appendix A: aggregate engine-usage lane (designed, deactivated)

If ratified: E-R2 gains one additional emit — the *engine cgroup's own*
cumulative `cpu.stat usage_usec` at dump time (a single aggregate number;
under LoadMulti, per-dump samples give a coarse background series). Consumer:
capacity honesty — C_effective(t) = C − (engine aggregate rate − Σ exec
rates), clamped at ≥ 0, replacing the constant C in §3.4's water-filling and
§8.1's saturation. No attribution to any internal operation ever occurs: the
lane is one anonymous background band. Gates: the lane requires the engine
cgroup to *contain* the exec cgroups so the subtraction is well-defined.
That containment is a **precondition to verify at implementation time, not
a code-proven fact**: the citations available (the OCI spec's cgroup path,
executor_spec.go:1220; the sampler's mountpoint join, sampler.go:14,40)
establish only where paths are read from, not the parent/child
relationship. Containment is expected from runc/OCI cgroup defaults, and
the emit would gate on verifying it (comparing the engine's own cgroup
path against each exec's) — refusing the lane, presence-flagged, where it
does not hold. Deactivated until ratified because it is adjacent to the
attribution ruling.

## Appendix B: time-series demand refinement (B2, deferred)

If CAL-2 shows whole-window averaging is the dominant gap source: E-R1 gains
the periodic sampler's 5s cumulative CPU samples as deltas (piecewise demand
d_i(τ) over window time); §3.4's water-filling uses the current interval's
demand; rate events gain sample-boundary crossings. Everything else —
allocator, gates, catalog — is unchanged in shape; V-R3/V-R5 gain piecewise
variants. Volume: ~12 samples/minute/exec, interned as a packed array field.
Deferred: no consumer until the calibration evidence demands it.
