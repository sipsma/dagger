# wcprof × OTel — design brief (fresh start)

You are designing, from scratch off `main`, the **OTel data source for wcprof**. This brief
gives you the goal, the settled constraints, the crux of the problem, where the hard parts
live, the pitfalls to avoid, and your deliverable. It deliberately does **not** recount any
history — you are a fresh pair of eyes and should reach your own conclusions from the engine
source. **Question anything here you believe is wrong, missing, or over-constrained, and say
so — don't silently work around it.**

## 1. The goal
`wcprof` (the wall-clock profiler, merged in PR #13393, `engine/wcprof/**` on `main`) answers
*"why was this run slow — what would I actually fix to make it finish sooner?"* The subtlety
it exists for: **total time ≠ bottleneck** — an op can run a long time yet sit off the critical
path (parallel to the real long pole), so speeding it up saves nothing. wcprof records a graph
of operations + their dependencies, then runs a **counterfactual replay** ("if op X were
faster, how much sooner does the *whole run* finish?") to rank the true bottlenecks.

Today wcprof is fed by **cheap in-process engine hooks**, available only on a special dev
engine. **Your job: design a SECOND data source — the engine's OTel telemetry** (the traces
that already flow to Dagger Cloud from every local and CI run) — **that compiles into the SAME
causal model and is analyzed by the SAME analyzer.** The motivating, eventually user-facing
feature: *"why was my CI run slow?"* on any Cloud trace. **User work is first-class** — a slow
`go build` in the user's own code is a valid headline answer, not just engine internals.

## 2. Your foundation (on `main`, trustworthy — reuse it)
- The native **recorder + analyzer** live in `engine/wcprof/**`: the op/wait/link model, the
  graph reconstruction, the **counterfactual replay** (`wcanalyze/replay.go`), the report.
  **Treat the analyzer and its replay as correct and validated** — your design *feeds* them; it
  does not rewrite them.
- Native builds its op graph by **inline recording on the live call stack**, so a parent op is
  genuinely *blocked inside* its child. That is exactly why the analyzer's core assumption —
  **a parent synchronously waits for its nested children (the "implicit join")** — is valid for
  native. Read the analyzer to understand this assumption precisely; it is central to §4.

## 3. Settled invariants (solid ground — honor these)
- **Anti-inference.** The analyzer does **no causal guessing**. The only causal inference
  allowed is the one native already relies on: the implicit-join for *synchronous* parent→child
  nesting. **Every other dependency must arrive as an EXPLICIT, correctly-attributed edge**
  emitted by the engine. The loader must not reconstruct or guess causality.
- **Faithful emit, trivial loader.** Where OTel's current emission does not honestly reflect the
  execution, **fix it at the emit side** (make the spans + edges honest). The loader should be a
  near-trivial `spans → ops → wait-edges` pass.
- **Two graphs, one IR.** Runtime wait edges (drive the counterfactual) are distinct from
  cache-key *input* edges (`dag.inputs`, for a later cache-diff sibling). Don't conflate them;
  the cache sibling is out of scope now — leave seams only.
- **Ingest = the Dagger Cloud trace API.** Every run forwards telemetry to Cloud, so one source
  covers local + CI (`otlpdump`/local captures are dev conveniences). Reading engine-internal
  per-client storage was ruled out.
- **Respect OTel volume.** Don't emit a span for everything; but emit *enough* that the model is
  faithful without guessing. Balance, with a bias toward correctness.

## 4. The crux (the central design problem)
**OTel span nesting is NOT always native's synchronous parent→child.** Native nesting means
"blocked inside." OTel's span tree is built by **context propagation**, which is faithful for
synchronous calls but not for everything the engine does: detached/async work, work re-pointed
to a different span for UI/log reasons, **deduplicated or suppressed calls that emit no span of
their own**, and concurrent dependency resolution. If the analyzer treats an *unfaithful*
nesting as a synchronous join, it invents dependencies that never existed — false serialization,
and even impossible **cycles** (an op appearing to wait on itself).

So your central task: **make the OTel-emitted graph faithful** — every nesting the analyzer
reads as synchronous must *actually* be synchronous, and every cross-op dependency must arrive
as an explicit wait edge **attributed to the right op**. Design the engine's OTel augmentation
so this holds at the choke points in §5.

## 5. Where faithfulness tends to break (scrutinize these, in the source)
Verify, from the code, how each emits its spans + causal edges, and whether that emission
honestly reflects what synchronously waited for what:
- **dagql call resolution + the cache** (`dagql/cache.go`; the telemetry hooks in `core`/
  `dagql`): how a result is computed, cached, and **singleflight-deduplicated**; how a
  **joiner** (a second caller whose result is already in-flight) is represented in telemetry. In
  particular, **verify whether suppression of repeated calls** (`ShouldEmitTelemetry` /
  `AroundFunc`) leaves a joiner *without its own span*, and if so, **where its causal edge then
  attaches** (e.g. to its parent rather than itself) and what that does to the graph.
- **Lazy / deferred evaluation** (deferred result evaluation in the dagql cache): where the
  deferred work actually nests vs. where its "resume" span is — and whether they coincide.
- **Container exec / the executor:** how in-container work and nested-client (module-runtime)
  work nests under the exec span.
- **Services / long-running daemons:** availability (a daemon idling until torn down) vs. real
  work.
Several of these are subtle; expect to read carefully and *verify* each, not assume.
(Practical note: OTel SDKs cap the number of links/events per span — if you express causal edges
as span links, make sure they can't be silently dropped.)

## 6. Pitfalls to avoid (hard-won — do not repeat these)
1. **Design from the engine CODE, not from trace measurements.** Do NOT reverse-engineer the
   design by running a workload and dissecting the resulting trace — a trace from an
   incomplete/incorrect emit is *garbage*, and chasing its artifacts goes in circles. Read what
   the engine *does*; design the emit + loader to be faithful. **The first trace you trust is
   one your *corrected* design produces.**
2. **The loader does NO inference.** No synthesizing nodes absent from the trace; no reparenting
   spans by guessing (e.g. timestamp containment); no heuristic causality. If structure is
   wrong, fix it at emit. Inference manufactures false structure — exactly what §3 forbids.
3. **Cycles / over-serialization in the analyzer mean unfaithful EMIT, not a replay flaw.**
   Nothing waits on itself in a run that finished — a cycle is *always* mis-emitted or
   mis-attributed data. The native replay is validated; **do NOT add replay machinery**
   (cycle-breakers, alternative simulators) to paper over bad data. Fix the data.
4. **Ground every claim in the source.** Don't assert how the engine emits or behaves — read it,
   cite it (file:line). Most wasted effort comes from confident claims the code later contradicts.
5. **Keep it simple.** The correct design is likely: faithful emit at the choke points + a
   trivial loader + the reused replay + first-class validation. If your design grows complex
   (new simulators, node synthesis, heuristics), step back — you're probably compensating for
   unfaithful emit, which is the wrong layer.

## 7. Validation is first-class (not an afterthought)
The design must include a validation plan that **loudly catches over-serialization, impossible
structure (cycles), and wrong counterfactuals early**, and that runs only on traces produced by
the **corrected** engine — never on uncorrected output. Strongest check: the **cross-source
oracle** — run the same workload on a dev engine with BOTH native wcprof and OTel active,
compile both to the model, and compare (native is the validated ground truth). Plus: structural
invariants on the loaded graph (no impossible structure; no op self-time exceeding the
makespan); known-answer injection (inject a known delay → assert it ranks; assert parallel /
off-path work does NOT rank); and a standing simulated-vs-actual **drift gate** on a
*representative complex* workload — toy workloads hide these failures.

## 8. Your deliverable
A single, holistic **design + implementation-plan** document: the engine-side OTel augmentation
(what spans / edges / attributes to emit at each choke point, and why — grounded in the code),
the loader (the trivial spans→ops→waits compile), the validation plan (§7), and a sensible
implementation sequence. Assume a clean start from `main`. Ground every design decision in the
engine source (cite file:line). Then **stop** — this is a design deliverable for human review;
do not implement, and do not run any external review yet.

## 9. Authority
Everything above is our current best understanding, but you are a fresh, critical pair of eyes.
If any goal, invariant, pitfall, or pointer here looks wrong, incomplete, or over-constrained,
**say so and explain** rather than working around it silently.
