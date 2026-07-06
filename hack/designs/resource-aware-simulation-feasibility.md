# Resource-constraint-aware wcprof simulation — Phase 1: feasibility and scoping

Status: PHASE-1 DELIVERABLE, awaiting go/no-go before any phase-2 design work.
Author: resource-model-designer-2 (Fable), 2026-07-06.
Lineage: builds on the wcprof substrate (engine/wcprof), the what-if-cached
simulator (hack/designs/whatif-cached-design.md) and the invalidation-tracing
feature (hack/designs/cache-invalidation-tracing-design.md). Nothing here
changes any of those; this document only assesses what is practically possible
and proposes a bounded scope.

Claims discipline: every statement about this codebase carries a file:line
citation and was read directly, not inferred. Statements from the scheduling /
performance-modeling literature are labeled "(literature)". Statements about
what a future model *will* do are labeled as expectations to be validated,
never as facts.

---

## 0. What this document is

The wcprof replay simulator currently assumes an infinite machine: unlimited
CPU, unlimited memory, any number of processes runnable at once. That
assumption is deliberate, documented (engine/wcprof/README.md:121-124), and
correct for what the simulator has been asked so far. It becomes the dominant
error source the moment a counterfactual *increases concurrency* — which is
exactly what the next class of questions does.

This document answers, in order:

1. Where exactly the infinite-resource assumption lives in the algorithm (§1).
2. What resource data the engine actually measures today, what reaches
   captures, and what an additive emit could realistically add (§2).
3. Which classes of resource-aware simulation are tractable — not a PhD
   thesis, not an NP-hard optimization — and battle-tested elsewhere (§3).
4. Which of the motivating questions each candidate class can honestly answer
   (§4).
5. A proposed bounded v1: what it does, what it refuses, what data it needs,
   what fidelity to expect (§5, §6).
6. How it would be validated, carrying the wcprof doctrine unchanged (§7).
7. The seams to name but not build (§8), and the open questions that need a
   ruling before phase 2 (§9).

The three motivating questions (Erik, near-verbatim):

- (a) Simulate a run parallelized MORE, capturing that more parallelism
  eventually hits a resource bottleneck and stops mattering.
- (b) Answer "if we had more CPUs, would this have gone faster?"
- (c) Eventually: multi-machine scale-out planning — how to split work, what
  machines, how many.

Explicitly out of scope by prior ruling: attributing CPU/memory usage to
engine-internal operations. Only user execs have usable resource metrics.

---

## 1. Where infinite resources live in the replay today

The replay simulator (engine/wcprof/wcanalyze/replay.go) re-executes the
recorded op graph as a discrete-event schedule. Its header states the
assumption outright: *"assuming unlimited resources (never CPU/disk bound)"*
(replay.go:11-13); the README lists it as a deliberate v1 simplification
(engine/wcprof/README.md:121-124). Mechanically the assumption enters at
exactly three points:

1. **Spawns are free.** When an op's timeline reaches a child spawn, the child
   is anchored at the parent's current simulated clock, unconditionally:
   `s.setStart(a.ref, clock)` (replay.go:580). There is no admission control,
   no queue, no notion of "the machine is busy" — any number of ops can be
   in flight at the same simulated instant.

2. **Durations are exogenous constants.** A self segment advances the op's
   clock by its recorded duration times the hypothesis factor:
   `clock += int64(float64(a.dur) * factor)` (replay.go:565). A duration never
   depends on what else is running — no contention can stretch it, no idle
   machine can shrink it.

3. **Lock delays are frozen.** Waits on named resources (locks) replay as
   fixed delays with their recorded durations (replay.go:603-610, and the
   already-documented what-if-cached simplification #2,
   hack/designs/whatif-cached-design.md — contention is not re-derived when
   the schedule changes).

Two structural properties of the current algorithm matter enormously for what
a resource-aware version can look like:

**The replay is a lazy, memoized recursion, not a global event loop.**
`finish(i)` recursively finishes dependencies on demand (replay.go:451-503);
an op's simulated schedule depends *only* on its own dependencies' finishes.
That is what makes the replay order-independent (asserted by the
`SimStartConflicts` counter, replay.go:349-355) and what makes each simulation
a cheap array DP: the per-op timelines are compiled once per graph
(`compileProgram`, replay.go:138-280) and hundreds of counterfactuals run over
multi-million-op traces (replay.go:54-57), e.g. the what-if ranking table
runs up to 200 classes × several factors (replay.go:797-803).

**Finite capacity breaks exactly that locality.** Under a shared resource,
an op's progress rate depends on *everything else running at the same
instant* — a global coupling. Any honest finite-capacity model therefore
needs a global time-ordered event loop (a priority queue over simulated
time), not a per-op recursion. Consequences, stated now so phase 2 doesn't
discover them:

- Per-simulation cost rises from "flat DP over the program" to "event loop
  with rate bookkeeping". Tractable (§3), but not free — which argues for
  capacity-awareness as a *mode* applied to a handful of simulations
  (baseline, explicit hypotheses, a capacity-sensitivity grid), not to the
  200-class ranking grid, at least in v1.
- The order-independence invariant is replaced by
  determinism-by-construction: a global event loop with explicit,
  documented tie-breaking (simulated time, then op ID) is deterministic by
  design. The existing `SimStartConflicts` signal keeps its role in the
  infinite mode; the capacity mode needs its own equivalent gates (§7).
- The compiled per-op action programs survive unchanged — the event loop is
  a different *executor* over the same compiled timelines, not a new
  compilation. The infinite-resource mode remains exactly as it is.

One more recorded-data fact belongs here because it changes what "more CPUs"
means: **the engine imposes no concurrency cap on user execs today.** The
`oci-max-parallelism` flag is parsed into config (cmd/engine/main.go:782-793)
but nothing in the Dagger executor path consumes it (verified: zero references
to `MaxParallelism`/`ParallelismSem` outside config parsing;
internal/buildkit/cmd/buildkitd/config/config.go:123-145 is the only other
site). The only concurrency limit found engine-side is an internal cap of 8 on
parallel module resolution (engine/server/session_workspaces.go:445) — an
engine-internal policy, not an exec gate. So in reality, as in the current
simulator, the only brakes on exec concurrency are the dependency structure
and the physical machine. This is good news for model fidelity: the real
engine's "scheduling policy" for execs is *greedy work-conserving* (start
everything as soon as its dependencies allow; let the kernel share the CPUs),
which is the easiest policy class to simulate faithfully (§3).

---

## 2. Data reality: what is measured, what reaches captures, what an emit could add

### 2.1 What the engine measures today (verified)

Dagger already runs a **per-exec cgroup v2 sampler** — not buildkit's monitor
(vendored but unused; no `resources.Monitor` references in engine code), but
Dagger's own `engine/engineutil/resources` package:

- One sampler per container exec, created in `runContainer` when the exec has
  a cgroup path and a call digest (engine/engineutil/executor_spec.go:1220-1242).
- Sampled every 5 seconds (`cgroupSampleInterval`, executor_spec.go:75,
  1252-1273), plus **one final sample when the container exits**
  (executor_spec.go:1258-1264). Cleanups run LIFO
  (util/cleanups/cleanup.go:49-55), and the sampler-cancel cleanup is
  registered *after* the `runc delete` cleanup (executor_spec.go:1216 vs
  1247), so the final sample reads the cgroup before it is destroyed.
- What it reads, per exec cgroup:
  - `cpu.stat`: cumulative `usage_usec`, `user_usec`, `system_usec`
    (engine/engineutil/resources/cpustat.go:16-22).
  - `cpu.pressure`: cumulative PSI stall totals, `some` and `full`
    (cpustat.go:106-167). PSI ("pressure stall information", a Linux kernel
    facility) measures time tasks were runnable but stalled waiting for CPU:
    `some` = time at least one task in the cgroup was stalled; `full` = time
    all non-idle tasks were stalled simultaneously. This is *direct measured
    evidence of CPU contention*, per exec.
  - `memory.current` and `memory.peak`
    (engine/engineutil/resources/memorystat.go:15-18).
  - `io.stat` read/write bytes and `io.pressure`
    (engine/engineutil/resources/iostat.go:15-16).
  - Network rx/tx bytes/packets/drops via the network namespace sampler
    (engine/engineutil/resources/netstat.go).
- Everything is emitted as OTel Int64Gauge metrics tagged with the exec's
  call digest and, when present, span and trace IDs
  (executor_spec.go:1224-1236).

Three properties of this data matter for modeling:

1. **The CPU and PSI counters are cumulative**, so the *final* sample carries
   exact whole-exec totals regardless of the 5s interval. Even a 300ms exec
   gets exact totals from the final sample. The 5s interval limits only the
   *time-series shape* within long execs, not end-of-exec truth.
2. **Absence is silent.** Every sampler returns nil on `os.ErrNotExist`
   (cpustat.go:82-83, memorystat.go:48-49, 92-94) — `memory.peak` needs a
   recent kernel, PSI needs `CONFIG_PSI`. Any wcprof emit must therefore carry
   explicit per-family presence flags; a zero must never be conflated with
   "not collected" (per-source capability honesty).
3. **Only execs with a call digest are sampled** (executor_spec.go:1221) —
   which is the same population the causal graph can attach demands to.

### 2.2 What reaches wcprof captures today: nothing

The capture record types carry identity, timing, causality, cache structure —
and no resource fields at all: `DumpEvent` (engine/wcprof/dump.go:65-112) and
the reconstructed `Op` (engine/wcprof/wcanalyze/graph.go:18-86) have none.
Host capacity is also absent everywhere: the engine reports `runtime.NumCPU()`
via the GetInfo RPC and a container env var
(engine/engineutil/executor_spec.go:1067), but no capture — native or OTel —
records the machine's core count, total memory, or the engine's own cgroup
limits. Today a capture cannot even say what machine it ran on.

### 2.3 What an additive emit can realistically add

All of the following read data the engine already touches, at sites that
already exist. Per doctrine, each field in phase 2 gets a justified-or-refused
entry with its deciding code path; this is the candidate list with feasibility
verified:

- **E-R1 — per-exec final resource totals** on the existing `exec.run` op
  (created at engine/engineutil/executor.go:135-146, ended at executor.go:175
  — which is *after* `c.run`'s deferred cleanups fire at executor.go:203, i.e.
  after the final cgroup sample; the values are in hand before the op record
  closes). Fields: cpu usage/user/system µs, PSI cpu some/full stall µs,
  memory peak bytes, io read/write bytes, plus presence flags and the
  container-run window (the started-callback boundary is already recorded for
  the OTel exec split: profStartedNS, executor_spec.go:1276-1289). Fixed-size
  numeric fields on an existing record: negligible volume.
- **E-R2 — capture-header machine facts**: effective CPU capacity (parse the
  engine cgroup's `cpuset.cpus.effective` and `cpu.max` quota — the engine
  itself may be capacity-limited, in which case host core count is the wrong
  capacity), host core count, `MemTotal`, and whether PSI/memory.peak are
  available on this kernel. One-time, header-level.
- **E-R3 (optional, deferrable) — coarse per-exec CPU time-series**: the 5s
  cumulative samples the sampler already takes, as deltas. Volume scales with
  exec duration (~12 samples/minute/exec); only refines *long* execs. §9 asks
  whether v1 needs it; recommendation: defer until calibration shows
  whole-exec averaging is the dominant error.
- **OTel-source parity**: the same data already leaves the engine as OTel
  metrics joined to spans by span-id attrs. The wcotel loader could ingest a
  metrics stream and join it to exec spans. Real but separate plumbing
  (metrics pipeline vs the span pipeline the loader reads today); v1
  recommendation is native-first with the OTel capability honestly labeled
  "resource fields: not available from this source (yet)" in the per-source
  capability table.

What no emit can add: resource usage of engine-internal work (ruled out), and
any measurement of demand *beyond* what the machine allowed to run (an
observed usage of 4 cores is a lower bound on what the process would have
consumed on a bigger machine — see §6).

---

## 3. The tractable model space

### 3.1 The line that keeps this out of NP-hard territory

Finding an *optimal* schedule for precedence-constrained tasks on finite
machines (P|prec|Cmax and its resource-constrained generalizations, RCPSP) is
NP-hard (literature: Ullman 1975; Blazewicz et al. 1983). **Simulating a
given policy under finite capacity is not**: it is a discrete-event simulation
that costs O(E log E) in events, the bread and butter of cluster simulators,
network simulators, and CI schedulers.

So the rule that bounds everything below: **the simulator only ever executes a
fixed, documented scheduling policy under a hypothesis — it never searches for
a good schedule.** All capacity questions become "re-simulate the same policy
under different capacity" (sensitivity analysis by re-simulation), never
"find the best...".

Two further facts make policy simulation unusually honest *for this system*
(rather than merely tractable):

- The real engine has no exec scheduler to mis-model: execs start when their
  dependencies allow and the kernel time-shares the CPUs (§1). A greedy
  work-conserving policy is not an approximation of the engine's policy — it
  *is* the engine's policy.
- (literature) Graham's bound: any greedy work-conserving schedule has
  makespan within (2 − 1/m) of optimal on m processors. Even where our
  simulated policy diverges from what the kernel actually did, the makespan
  estimate cannot be pathologically far from any achievable schedule. This is
  a robustness argument, not a precision claim.

### 3.2 Candidate shape A: discrete core slots + list scheduling — REJECTED

Model the machine as C integer cores; each op occupies an integer number of
cores for its duration; ready ops queue for free slots in priority order.

Rejected for three reasons:

1. **Wrong granularity for the data we have.** Measured exec demands are
   fractional (an exec that used 2.7 core-equivalents on average); forcing
   them into integer slots either wastes modeled capacity or invents
   parallelism the process never had.
2. **Scheduling anomalies.** (literature: Graham 1966/1969) List schedules
   are non-monotone: *adding* a processor can *lengthen* the schedule. A
   capacity-sensitivity report where 8 cores beats 16 because of a slot
   artifact is indefensible in a tool whose doctrine is "a single reasonable
   answer derivable by logic".
3. Durations-as-constants under fewer cores is simply wrong for
   multi-threaded processes, which degrade gracefully (they get less CPU and
   stretch), not by queueing whole.

### 3.3 Candidate shape B: work-conserving fluid sharing — RECOMMENDED CORE

"Fluid" means CPU is treated as a divisible rate, not slots. Each exec op
carries measured CPU work W (cpu-seconds, from E-R1) over its container-run
window of recorded length T, giving an observed average demand rate
d = W/T (in cores). The machine has capacity C (from E-R2). At any simulated
instant the active execs share C by **max-min fairness with per-task caps**:
every exec gets at most its demand d, and if Σd over active execs exceeds C,
the surplus is shared fairly (each contended exec gets an equal share of
what remains after satisfying smaller demands — "water-filling"). An exec
whose allocated rate a falls below its demand d makes proportionally slower
progress: its remaining compute stretches by d/a for as long as the deficit
lasts.

Concrete v1 semantics (B1, whole-exec average):

- Demand attaches to the exec op's container-run window (the recorded
  started→exit interval), not to setup phases (image pull, mount prep run
  before the cgroup has user processes; the started boundary is already
  recorded, §2.3).
- Non-exec ops have **zero modeled demand and unchanged recorded durations**
  — the declared model boundary from the prior ruling. They still schedule
  (spawn/join/lock) exactly as today.
- Rate reallocation happens only when an exec starts or finishes — the event
  loop re-projects in-flight execs' completion times at those instants.
  Event count is O(#execs); per-event work is O(active execs). Even a trace
  with 10^6 ops but 10^4 execs and dozens active at once simulates in
  milliseconds. The 200-class ranking grid stays in infinite mode in v1;
  capacity mode runs where it's asked for.

Why B1 fits the doctrine unusually well — two exact reductions:

- **C = ∞ reduces to the current replay, bit for bit.** Every exec always
  receives its full demand, no stretch ever occurs, and the event loop
  executes the same compiled timelines to the same schedule. This is a
  reason-derived gate (§7, R1), not an aspiration.
- **Baseline at recorded capacity should stretch (almost) nothing.** The
  recorded overlap actually ran on the real machine, so measured demands
  already satisfied Σd ≤ C at every recorded instant, up to measurement noise
  and unattributed non-exec usage. A baseline capacity-mode replay therefore
  reproduces the infinite-mode baseline within a small, *quantified,
  printed* residual (§7, R2). Capacity only bites when a counterfactual
  *packs more work into the same window* — which is precisely when it should.

The stretch-only rule ("an exec never runs faster than its recorded rate,
only slower under deficit") is the honesty keystone: observed demand is a
lower bound on true demand, so simulating *extra* speed at higher capacity
would be guessing data we don't have. What higher capacity *can* honestly do
is un-stretch work that a tighter counterfactual had stretched, and remove
queueing the hypothesis itself created. The "would more CPUs help" question
gets its answer from evidence, not fabricated speedups — see §4(b).

Refinement B2 (deferrable): with E-R3 time-series, demand becomes piecewise
(per 5s window) instead of whole-exec average, so bursty execs (idle 20s,
compile hard 10s) contend only where they actually burned CPU. Same event
loop, more rate-change events. Worth doing only if calibration shows the
averaging smear dominates error.

### 3.4 Candidate shape C: measured-evidence layer, no scheduling — ADOPTED AS COMPLEMENT

No simulator at all: compute, from measured data,

- the machine **saturation timeline** — Σ active exec demand rates vs C
  across the trace (exactly computable from per-exec windows and averages;
  finer with E-R3);
- the **work-law and span-law bounds** (literature; standard parallel
  computing): makespan ≥ total exec cpu-seconds / C, and
  makespan ≥ critical-path length — the two hard floors any schedule obeys;
- the **PSI contention ledger**: per-exec measured stall totals — where the
  kernel itself recorded "this process was starved".

This layer answers "was the machine ever the bottleneck?" from measurement
alone, with zero model risk. It cannot simulate counterfactuals (no answer to
question (a), no composition with what-if-cached), which is why it is a
complement inside the report, not the model. Everything in it is cheap and
should exist regardless of which simulator shape is chosen.

### 3.5 Rejected outright

- **Full-system microsimulation** (memory bandwidth, cache hierarchies, IO
  interleaving, scheduler quanta): the pre-ruled "PhD thesis". Wrong fidelity
  class for 5s cgroup counters, and the nuance is explicitly conceded in the
  mission statement.
- **Closed-form queueing / scalability laws** (M/M/k, Universal Scalability
  Law): no representation of the DAG structure that dominates CI makespans;
  they answer throughput questions about steady-state streams, which this is
  not.
- **Per-internal-op resource attribution**: ruled out ("ridiculous"). §9
  raises one narrow adjacent question (an *aggregate* engine-cgroup lane, no
  attribution) for an explicit ruling rather than sneaking it in.
- **Memory-contention timing prediction**: swap/reclaim/OOM behavior is
  nonlinear and unmodelable from peak counters; v1 treats memory as a
  reporting-and-feasibility concern, never a timing input (§5).

---

## 4. What each shape answers, against the motivating questions

**(a) "Parallelize more → where does it stop mattering?"** Needs a
counterfactual scheduler with capacity: **shape B**, directly. Any hypothesis
that increases overlap (what-if-cached elisions pulling work together; factor
scaling; a future "remove this serialization" hypothesis class) runs under
finite C and shows the plateau. The capacity-sensitivity grid (re-simulate at
C × {½, 1, 2, 4, ∞}) makes "stops mattering" a printed table, per hypothesis.
Shape C cannot answer this; shape A answers it with slot artifacts.

**(b) "Would more CPUs have made this run faster?"** Layered answer, all
evidence-grounded:

1. *Measured verdict* (shape C): if the saturation timeline never approaches
   C and PSI stall totals are ~0, the answer is **no** — stated from
   measurement, the strongest possible epistemic position. This is Erik's
   question answered exactly as often as the data allows.
2. *Measured bound* (shape C): where PSI shows stalls, print per-exec stall
   totals as the measured ceiling on what more CPU could relieve — "the
   kernel recorded these processes waiting for CPU for a total of X" — a
   bound and a verdict, not a fabricated point prediction.
3. *Simulated relief* (shape B): where the *baseline* capacity replay at
   recorded C had to stretch nothing, upward re-simulation honestly reports
   zero structural gain (the recorded schedule already fit). Where a
   *hypothesis* schedule is capacity-bound, upward re-simulation shows
   exactly how much of the hypothesis' promise returns at 2×C.

The fidelity contract to state up front: the model **detects and bounds** CPU
starvation; it does not invent unconstrained demand curves for processes the
machine never allowed to spread out. That is the honest limit of the data,
and it should be written into the report text itself.

**(c) "Multi-machine scale-out planning."** Named seam, not v1 (§8). The
event-loop model generalizes naturally (machines = capacity pools; an
assignment *policy* is simulated, never optimized), but the data gaps are
real: no cross-machine transfer costs, no recorded multi-engine traces to
calibrate against. A bonus v1 *can* deliver toward (c): downward capacity
sensitivity ("would this run fit on a machine half the size, at what cost?")
is machine right-sizing — the same grid read in the other direction, plus the
memory-peak feasibility check per §5.

---

## 5. Proposed v1 scope

**Does:**

1. **Additive emits E-R1 + E-R2** (native source), every field
   justified-or-refused with its deciding code path, presence flags for
   kernel-dependent families, zero behavior change when wcprof is off.
2. **Loader + graph**: exec ops gain resource fields with explicit
   "not recorded" states; the capture header gains machine capacity; the
   per-source capability table gains a resources row (native: yes; OTel: not
   yet — labeled).
3. **Capacity-aware replay mode** (opt-in flag): the fluid B1 event loop over
   the existing compiled programs. Infinite mode remains the default and is
   untouched. Capacity mode runs: baseline, explicit what-if/cached
   hypotheses on request, and the capacity-sensitivity grid.
4. **Report section**: machine capacity line; exec demand ledger (top classes
   by cpu-seconds); saturation timeline summary; PSI contention ledger;
   capacity-sensitivity table; and for capacity-mode counterfactuals, the
   stretched-vs-unstretched makespan with the residual ledger. Gates refuse
   the section when required data is absent (§7).
5. **Composition**: what-if-cached hypotheses runnable under capacity mode,
   with the reason-derived relation gate: finite-capacity savings ≤
   infinite-capacity savings for the same hypothesis (capacity can only
   delay).

**Refuses, printed as refusals where a user could expect otherwise:**

- Timing predictions from memory pressure (reports peaks and flags
  counterfactual windows where Σ concurrent exec memory peaks exceed machine
  memory — a feasibility annotation, never a simulated slowdown).
- IO and network contention modeling (measured IO bytes/pressure reported as
  context only).
- Any resource demand for non-exec ops (zero-demand declared boundary; §6
  states the error direction).
- Speedups without evidence (stretch-only; upward gains only via PSI-bounded
  evidence or relief of hypothesis-created contention).
- Schedule or partition optimization of any kind.
- Re-derivation of lock contention under changed schedules (fixed delays
  stay, as today; already-documented understatement direction).

**Needs:** E-R1, E-R2. E-R3 explicitly deferred pending calibration.

---

## 6. Expected fidelity, honestly

Error sources, each with direction where derivable:

- **Measurement noise** (5s sampling, cgroup counter coarseness, final-sample
  race tolerances): accepted by the mission statement. Mitigated by using
  cumulative totals (exact at exec end) rather than interpolated series.
- **Unattributed non-exec usage**: the engine itself, snapshotters,
  network setup, API serving all burn CPU that the model does not subtract
  from C. Direction: the model is *optimistic about available capacity*, so
  counterfactual makespans are biased LOW exactly when the machine gets
  busy. This is the largest structural bias and the subject of the §9
  aggregate-lane question.
- **Whole-exec averaging (B1)**: bursty execs contend uniformly instead of in
  bursts. Direction: not derivable in general (can hide real collisions and
  invent false ones); measured by calibration; the designated fix (B2) is
  data-driven, not heuristic.
- **Non-exec durations frozen under changed contention**: if a counterfactual
  saturates the CPUs, engine-internal ops would really slow down too but
  replay at recorded durations (optimistic); if it frees the machine,
  the reverse. Stated in the report.
- **Observed-demand-is-a-lower-bound**: upward-capacity extrapolation is
  bounded by evidence (PSI), never point-predicted. Stated in the report
  text itself (§4(b)).
- **Contention nonlinearity** (memory bandwidth, LLC, hyperthreading): not
  modeled; fluid sharing assumes cores are interchangeable and additive.
  Direction: optimistic under heavy multi-core contention. Stated; the
  calibration harness (§7) measures how much it costs in practice.

The calibration standard the what-if-cached work set (drift vs a real
counterpart run, hack/designs/whatif-cached-calibration.md) applies here with
a *better-controlled* experiment available: capacity is directly manipulable
on a real engine (cgroup `cpu.max` on the engine container), whereas cache
state never fully was.

---

## 7. Validation approach (preview — full catalog is phase-2 material)

Reason-derived expectations, assertable before any code exists:

- **R1 — infinite reduction**: capacity mode at C=∞ reproduces the current
  replay bit-for-bit (every op's sim times equal). Unit-tested always.
- **R2 — baseline residual**: capacity mode at recorded C over the recorded
  schedule stretches ≈ nothing; the residual (count + total stretch ns) is
  computed, printed, and gated with a documented tolerance derived from
  measurement noise — never silently absorbed.
- **R3 — sensitivity sanity**: makespan is non-increasing in C for fluid
  sharing. Expectation to validate, not a claimed theorem (dependency
  release-time shifts make a general proof subtle); violations fail loudly
  and get investigated, never smoothed.
- **R4 — hard floors**: simulated makespan ≥ work-law floor and ≥ span-law
  floor, always (these ARE theorems about any capacity-respecting schedule).
- **R5 — hand-derivable scenarios**: the synthetic catalog pattern from the
  what-if work — e.g. N identical CPU-saturating execs, no dependencies, on C
  cores under fluid sharing have an exactly derivable finish schedule;
  assert it exactly. A catalog of such scenarios covering caps, deficits,
  staggered arrivals, mixed exec/non-exec chains.
- **R6 — composition relation**: for any cached hypothesis,
  finite-capacity saving ≤ infinite-capacity saving.
- **R7 — real-engine calibration**: capture a workload on an unconstrained
  engine; predict its makespan at C=k via capacity mode; run the same
  workload on the same engine constrained to k cores (cgroup `cpu.max`);
  compare, both directions (also: capture constrained, predict unconstrained,
  compare against the unconstrained run — the direction where stretch-only
  semantics claims real relief). Evidence in-tree under hack/logs/, per
  standing rules.

Gates refuse rather than decorate: no E-R1 data ⇒ no capacity section (with
the reason printed); missing PSI ⇒ the contention ledger says "PSI not
available on this kernel", never zeros; presence flags flow through to every
number's provenance.

---

## 8. Seams to name, not build

- **What-if-cached**: composition is in v1 (§5.5), but deeper integration —
  e.g. capacity-aware savings in the default ranking table — waits until the
  event loop's cost profile is known.
- **Remote-cache upload-decision intelligence**: the planned smart cache
  service consumes wcprof analysis as policy input. Capacity-aware savings
  estimates make "value of caching X" honest on busy machines (an elision
  that mostly relieves CPU contention is worth more than infinite-resource
  replay suggests, and vice versa). The interface is unchanged: hypotheses
  in, honest savings out.
- **Multi-machine planning** (question (c)): capacity pools + a simulated
  assignment policy generalize the event loop; blocked on data (transfer
  costs, multi-engine traces) and explicitly not v1. Downward sensitivity
  (right-sizing) is the v1 down-payment.

---

## 9. Recommendation and open questions for the phase-2 go

**Recommendation**: adopt **shape B1 (fluid work-conserving sharing, whole-exec
average demands, stretch-only) as the v1 simulator core**, with **shape C
(measured saturation + PSI evidence layer) built into the report
unconditionally**, emits E-R1+E-R2, composition with what-if-cached, and the
R1-R7 validation skeleton. Defer E-R3/B2 (time-series) and any IO modeling
until calibration data argues for them. Reject discrete slots (shape A).

Questions needing a ruling before phase 2:

1. **Aggregate engine-usage lane**: may v1 emit the *engine cgroup's own
   total* cpu/memory usage (one aggregate number series, no attribution to
   internal ops) so capacity honesty improves (C_effective = C − measured
   background instead of C)? It does not attribute anything to internal
   operations, but it is adjacent to the ruled-out territory, so it needs an
   explicit yes/no. v1 works without it; the §6 optimism bias just stays
   larger and stated.
2. **Verdict+bound framing for (b)**: confirm that "measured verdict + PSI
   bound + simulated relief" is an acceptable shape for the "more CPUs?"
   answer — i.e. that v1 is NOT expected to point-predict speedups for
   never-observed larger machines.
3. **OTel parity timing**: is native-only resource data acceptable for v1
   (with the capability table labeling OTel captures "resources: not
   available"), with the metrics-join as a follow-up?
4. **Sensitivity grid definition**: is C × {½, 1, 2, 4, ∞} the right default
   grid, or should it be configurable-only?
