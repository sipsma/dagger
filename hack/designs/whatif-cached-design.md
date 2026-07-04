# wcprof what-if-cached — design & implementation plan

Track B of the Cache Performance Analysis workstream · 2026-07-04
Basis: wcprof + OTel source + exec-decomp stack (`db1caae0`). Every
load-bearing claim re-verified against this code, independently of the prior
Opus scoping.

**Status: approved by Erik 2026-07-04 (all §6 recommendations adopted).**
This file is the committed markdown rendition of the approved design page;
where they could ever disagree, the page (the lead's artifact) wins.

## TL;DR

- **Concur with the prior scoping's verdict**: this is more than a CLI flag.
  Factor-0 self-time scaling is semantically wrong for exactly the ops caching
  targets. A new replay operation is needed.
- **But its mechanism is under-specified and partly wrong.** The proposed
  "`finish()` short-circuit + guard" cannot work as sketched: any outside wait
  into an elided subtree makes the replay *resurrect* that subtree through
  prefix-anchoring. Its Stage-2 premise ("dataflow edges are emitted
  natively") is false — those link kinds have **zero emit sites**.
- **The design: cache hypotheses are recipe digests, not op instances.**
  "Cached" = every call of that digest becomes a hit; its producing subtree is
  elided *as a unit* by a static pre-pass, or — if anything outside still
  demands work inside it — kept whole and reported. No partial elision in v1:
  it keeps the replay order-independent, which this simulator treats as a
  correctness invariant.
- **v1 models a local warm hit** (`finish = start + pullCost`, pullCost = 0),
  works on **both sources day one** (native ident and OTel `dag.digest` are
  the same key — verified), and adds a *what-if-cached ranking table* as the
  headline, mirroring the existing what-if report.
- **Trust is built in, not asserted**: baseline-invariance gate, loud
  kept/elided counters in the data-faithfulness style, and a **cold-vs-warm
  calibration harness** — simulate the cold run with the warm run's hit set,
  compare against the warm run's actual makespan. That number is what lets the
  remote-cache team replace empirical A/B runs.
- **Four chunks**: ① elision engine in the replay, ② selection + report + CLI,
  ③ calibration harness, ④ (Stage 2, shared with Track A) cache-DAG input
  edges. Pull-cost stays a seam.

**In plain words.** *"Local warm hit model"* — v1 pretends the cached result
was already sitting, fully materialized, in this engine's in-memory cache:
hitting it costs nothing (`finish = start + 0`). We are *not* yet modeling a
remote cache where the hit costs a pull — that's the `pullCost` knob, plumbed
but set to zero. *"Whole-region elide-or-keep with loud residuals"* — for each
result we pretend was cached, the work that produced it is removed from the
simulated schedule *as one block*. If anything outside that block still needed
a piece of it, we don't remove *any* of it — we keep the whole block as
recorded and print exactly what was kept and why. So the headline number is
never silently wrong; imprecision always shows up as ink in the report.

## 0. The doctrine (read first — binding for every line of this feature)

This section exists so nobody working on wcprof ever re-litigates it.

1. **The analyzer's model must make sense on its own.** For any hypothetical
   change — "what if this were cached", "what if this class were 2× faster" —
   there is a single reasonable answer derivable by logic from the recorded
   data. Sometimes it takes real work to reason it out, but it always exists,
   and *that* is the answer the analyzer must produce. Nothing else.
2. **The analyzer takes the data exactly as it is.** It never compensates for
   missing, suspect, or inconvenient data. No smoothing, no fallbacks that
   guess, no "the emit probably meant…". Heuristic compensation in the
   analyzer is the gate to hell: it turns every future wrong answer into an
   unfalsifiable debugging swamp. Prior agents went down this path; it is how
   this project fails.
3. **The data/model boundary is absolute.** If the analyzer's honest answer
   disagrees with reality, the defect is on the *data* side — go fix the emit,
   the recording, the ingestion. The analyzer is only required to accept data
   that obeys the laws of physics; where recorded data violates them, it is
   counted and fails a gate loudly (never absorbed). Existing embodiments:
   `SimStartConflicts`, `UnschedulableOps`, `CycleWarnings`, the
   simulated-vs-actual drift check, the OTel structural gate and span-count
   completeness checksum. This feature adds its own (`ElidedOpDemanded`,
   kept-region residuals) in the same spirit.
4. **Because of 1–3, validation is reason-first.** For every test scenario we
   can derive the expected outcome by logic *before* running the simulator,
   and then assert it exactly. That makes the validation catalog (§4.5) the
   primary QA instrument of this feature — not a nice-to-have. A scenario
   whose expected answer cannot be reasoned out is a sign the model has a
   hole, and that finding is itself valuable.
5. **Model simplifications are stated, bounded, and visible in output.** A v1
   simplification is acceptable only when (a) it is written down with its
   error direction where knowable, (b) the run report shows its residual
   (counts/durations, not adjectives), and (c) the refinement path is a *data*
   path, not a heuristic. The e-graph note in §3.1 and the lock note in §3.3
   are the two such simplifications in this design.

## 1. The goal, in one paragraph

Take one profiled, mostly-uncached run and answer counterfactually: *"how much
wall-clock time would this run have saved if operations X, Y, Z had been cache
hits?"* — without re-running anything. The consumer is the remote-caching
effort, which today answers this empirically (run cold, run warm, diff), which
is slow, expensive, and doesn't scale across candidate caching strategies. The
substrate is wcprof's replay simulator: a compiled, order-independent
discrete-event re-execution of the recorded op graph that already answers
"what if class X's self-time were scaled by *f*". Long-term, a remote hit has
a pull cost, so the question becomes "pull or recompute?" — explicitly not
designed now, but the mechanism leaves the seam.

## 2. Critical review of the prior scoping

Re-derived from the code rather than trusting the Opus doc
(`whatif-cached-simulation-scoping.md`). Its core analysis is right; the
closer it gets to mechanism, the shakier it gets.

### 2.1 What stands (independently verified)

| Claim | Verdict | Evidence |
|---|---|---|
| The replay's only knob is per-*class* self-time scaling; nothing can express "skip this subtree". | Confirmed | `Simulation.Factors map[ClassKey]float64`; factor applied at exactly one place (`actSelf`); `finish/advance` always spawn children and join waits. `replay.go` |
| Factor-0 is honest for leaf ops, and a confidently-wrong ≈0 for orchestrator `call`/`call_exec` ops — the ops remote caching targets. | Confirmed | An orchestrator's cost is child intervals, which are subtracted from self-time by construction (`SelfSegments`, `graph.go`). |
| A hit call op returns before any execution op is minted; an executing call parents the shared `call_exec`, under which all nested resolver work records; joiners reach it only via wait edges. | Confirmed | `dagql/cache.go:3614–3841`: call op → `SetIdent(callKey)` → miss mints `call_exec` child (`:3772`) → joiner waits (`:3999`). `publishResult` is parented under the `call_exec` (`:4072`), so it elides with it. |
| Cache keys are recipe-structural, so "the producing subtree never runs" is a coherent counterfactual — a hit is determinable without evaluating inputs. | Confirmed | `deriveRecipeDigest` / `selfDigestAndInputRefs` at the call site (`cache.go:3705–3722`). |
| Verdict: "more than a CLI flag, bounded, stageable; needs a new instance-level replay operation distinct from `Factors`." | Concur | — |

### 2.2 What doesn't stand

| Prior claim | Verdict | What the code actually says |
|---|---|---|
| "Dataflow `result`/`reused_result` edges are **emitted natively**, dropped by the loader" (Stage 2 is built on loading them). | False on the emit side | `LinkKindResult`/`LinkKindReusedResult` are *defined* (`wcprof.go:210–222`) but have **zero emit sites**: the only `wcprof.Link` call in the engine is the nested-client link (`engineutil/executor.go:144`). What exists is `Op.ResultID` stamped at op end (`cache.go:3629, :3827`) — a per-op field, not an edge, and engine-local. Stage 2 needs an *emit* (or the OTel `dag.inputs` route), not a loader fix. |
| Stage-1 wiring: "`cached []bool` + a short-circuit at the top of `finish()`; the conservative wait-target guard lives where an inside-op is reached from outside (`spawnTo`/`actWaitJoin`)." | Mechanically insufficient | A short-circuit at `finish(X)` does *not* remove X's subtree from the schedule. If any live op waits on an op *inside* the subtree, `finish(inner)` → `spawnTo` walks the ancestor chain and replays each ancestor's *prefix* — and the prefix replay's `joinUpTo` **fully finishes every earlier-ending sibling** along the way. One stray wait resurrects most of the "elided" subtree, at full recorded cost — silently. Runtime guards inside `spawnTo` would make anchors depend on evaluation order — exactly the order-dependence this replay's design treats as a data-faithfulness *failure* (`SimStartConflicts`). Elision must be a **static pre-pass**, resolved before the replay runs. (§3.3) |
| The Stage-1 boundary yields "a defensible **upper bound** on the saving." | Unproven either way | The two residual error sources push in *opposite* directions: keeping externally-shared subtrees understates savings; a partial-elision scheme that kept recorded (early) anchors for surviving inner work would overstate them. No bound direction is established. This design doesn't claim one — it makes the residual *measured and reported* (kept-subtree counters, §3.5) instead of labeled. |
| Recommendations: select by *op instance / class + outcome filter*; *native-first*, OTel as a fast follow; surface leaf-only `save@0` as "Stage 0" now. | Overruled (§3.1, §3.6) | The cacheable unit is the **recipe digest**, not an op instance — caching a result makes *every* caller of that recipe hit, executor and joiners alike; per-op-id sets can't express that and don't survive across sources or runs. Digest selection also makes OTel work *day one*: native `Ident` = `callKey` and OTel `Ident` = `dag.digest` are the same digest (verified), and executed-vs-joined — the native-only outcome — is *irrelevant* under digest-level elision. And leaf-only `save@0` should not be marketed as a caching answer at all. |

## 3. Design

### 3.1 Semantics: the hypothesis is a set of recipe digests

A what-if-cached hypothesis is **CachedSet: a set of recipe digests** (wcprof
`Ident`s of `call` ops), plus a `pullCost` (v1: 0). The counterfactual
meaning, applied uniformly:

- Every `call` op whose ident ∈ CachedSet — executor *and* joiners — becomes a
  hit: `finish = simStart + pullCost`. Its recorded self-time, waits, and
  children are not replayed.
- The producing work — the executing caller's `call_exec` child and everything
  nested under it (exec phases, publish, nested module clients re-parented by
  links) — is **elided**: it never ran, so it contributes nothing and holds no
  locks.
- Everything else replays exactly as the baseline does. Consumers of the
  cached result are untouched — they simply stop being blocked so long.

**v1 models a local warm hit**: the payload is materialized and free to
return. Remote-hit realism (pull time, lazy decode) is deliberately the
`pullCost` seam (§5), not v1 semantics.

> **Stated simplification #1 — recipe-digest identity only (Erik reviewed &
> accepted, 2026-07-04).** The real cache is not a digest→result map: it's an
> e-graph — a union-find over digest equivalence classes with content-digest
> evidence and congruence repair (`internal-docs/egraph.md`). Two consequences
> v1 does *not* model: a recorded hit may have arrived through *equivalence*
> rather than exact recipe identity, and — the interesting one — caching
> digest D could counterfactually make *other* recipes hit too (same output
> eq-class, or congruence downstream of it). Why v1 ignores this, on
> principle: the equivalence facts are **not in the recorded data** — ops
> carry recipe digest, outcome, and result ID, nothing about eq-classes — so
> modeling equivalence would mean the analyzer *guessing* engine state
> (doctrine §0.2 forbids it), and the honest alternative (emit equivalence
> facts and replicate the union-find offline) is a large data-path project
> that v1's value doesn't need. Error direction is knowable and safe:
> unmodeled equivalence-induced hits could only have elided *more* work, so v1
> **understates** savings through this simplification. If real traces ever
> show it matters, the path is emitting equivalence facts (extra digests /
> merges) — a data fix, never an analyzer heuristic.

### 3.2 Selection surface and eligibility

Selectors resolve to digest sets before the sim runs (all repeatable, both
analyzers):

| Flag | Meaning |
|---|---|
| `--cached <digest>` | one recipe digest (as printed in reports / `dag.digest`) |
| `--cached @file` | digest list, one per line — the "hand me a candidate cache manifest" form for the remote-cache team |
| `--cached-class <pattern>` | all *executed* digests whose call class matches (e.g. `Container.withExec`, `myMod:Foo.bar`) — sugar that resolves to digests |
| `--cached-exec <argv-pattern>` | exec-group-style argv predicate over user execs, resolved to the owning `withExec` digests — reuses the exec-decomp matcher ergonomics |

**Eligibility is checked, loudly** (report section, not silent filtering):

- ident has a `do_not_cache` outcome → *ineligible*, warn (the engine refuses
  to cache it; simulating it cached is fiction);
- ident's executions all errored/canceled → ineligible, warn (failed results
  aren't cached);
- ident already all-hits in this run → no-op, note it;
- ident's ops open at dump time → ineligible, warn.

### 3.3 The elision rule: static pre-pass, whole subtrees, elide-or-keep

The replay anchors an op reached out-of-order by replaying its parent's
timeline *prefix* (`spawnTo`/`advance`), and any prefix walk *fully finishes*
every child that ended before the stop point (`joinUpTo`). So "skip X in
`finish()`" does not remove X's subtree: one wait from outside into any
descendant re-enters the subtree through the ancestor chain and replays most
of it at full recorded cost — silently, under a hypothesis that claims it was
elided. And per-reference runtime guards would make simulated starts depend on
evaluation order, which this simulator's whole design treats as a correctness
failure (order-independence is asserted via `SimStartConflicts`).

Therefore elision is resolved **statically, per hypothesis, before the
replay**:

1. **Roots.** For each ident in CachedSet, its cached-call roots are all
   `call` ops with that ident. Candidate elision regions are their nesting
   subtrees (children transitively, including link-re-parented nested-client
   work).
2. **External-demand test.** A region may be elided only if no *live* op (an
   op outside every elided region and not itself a short-circuited cached
   call) has a wait edge targeting an op inside it. Waits from same-digest
   callers don't count — those waiters are short-circuited hits. Waits from
   *elided* ops don't count — they never run.
3. **Fixpoint.** Keeping a region can make its ops live again, which can add
   demand into another region; iterate keep-decisions to fixpoint (monotone,
   converges in ≤ #regions rounds; in practice 1–2).
4. **Elide-or-keep, whole regions.** An elided region vanishes: its ops'
   actions are never replayed, its lock (fixed-delay) waits never charge, and
   — by the demand test — nothing live references it. A kept region replays
   *exactly as recorded*, and its cached root is *not* short-circuited;
   instead it's reported: `kept: externally shared (N ops, X.Xs)`.

> **Why no partial elision in v1.** The faithful refinement — elide the
> non-shared branches of a kept region and re-anchor the shared service at its
> external demander's simulated demand time — requires either demand-time
> anchoring (order-dependent under memoized `finish()`, i.e. a new class of
> `SimStartConflicts`) or keeping recorded anchors for surviving work
> (counterfactually early starts → overstated savings). Both violate the
> "rational function of faithful data" bar in ways that are *invisible in the
> output*. Elide-or-keep is coarser but sound, order-independent, and its
> imprecision is **printed, not hidden**. If the kept-counters turn out to be
> material on real traces, that's the data-driven case for Stage 2 — and we'll
> know its size before building it.

> **Stated simplification #2 — lock contention is not relieved.**
> Named-resource (lock) waits record the *waiter* and the duration — not who
> held the lock. So when an elided region's ops vanish, their own lock waits
> vanish with them (a hit acquires nothing), but an *outside* waiter's
> recorded lock delay is unchanged even if the contending holder was inside
> the elided region — the data doesn't say so, and the analyzer will not guess
> (doctrine §0.2). This is the same fixed-delay treatment the baseline replay
> already applies to locks, extended consistently. Error direction: savings
> **understated** where an elided op was in fact the contending holder. If it
> matters, the fix is recording holder identity on lock waits — a data fix.

### 3.4 Reporting: the what-if-cached ranking + explicit sets

Two modes, mirroring how the existing what-if table earns its keep:

1. **Ranking mode (default on):** enumerate candidate digest groups — per
   class of executed calls, plus the top-N individual executed digests by
   subtree wall-clock — and run one sim per candidate (the program compiles
   once; each sim is a cheap array DP; the existing report already runs ~600
   sims). Output, ranked by makespan saved:

   ```
   what-if-cached: makespan saved if these results had been cache hits
   class/digest                                   execs   elided     kept   save@pull=0
   Container.withExec (all 41 executed digests)      41    38m12s    2m01s        6m44s
   myMod:Build.compile xxh3:9f31…                     1     9m48s        0        3m10s
   ```

2. **Explicit-set mode:** `--cached`/`@file` — one sim, full detail: baseline
   vs counterfactual makespan, per-ident eligibility notes, kept-region
   report, and the counterfactual blocking chain (reusing
   `BlockingChain`/`ExplainFinish`) so you can see what the *new* bottleneck
   would be.

Savings of a joint set are not the sum of individual savings (dependency
chains, critical-path shifts) — the ranking table says so in its header, same
honesty style as the existing what-if table. The saving is a property of the
re-simulated schedule, not "sum of elided time": elided work off the critical
path saves nothing; elided work on it can save less than its duration (a
second chain takes over).

### 3.5 Gates and validation (how this earns trust)

| Gate | What it asserts |
|---|---|
| **Baseline invariance** | Empty CachedSet reproduces the baseline simulation *bit-for-bit* (same makespan, same per-op sim times). A unit test, run always. |
| **Unreachability assertion** | An elided op demanded during replay (`finish`/`spawnTo`/`advance` reaching one) is a hard counter (`ElidedOpDemanded`), zero by construction of the pre-pass — same never-compensate doctrine as `UnschedulableOps`. Non-zero fails the run's what-if-cached section. |
| **Residual visibility** | Per hypothesis: elided op count/duration, kept regions (why + size), ineligible idents (why). Nothing silently dropped or absorbed. |
| **Cold/warm calibration** (Chunk 3) | The empirical loop this tool replaces is also its ground truth: profile a workload cold (run 1) and warm (run 2); feed run 2's *actual hit digests* as run 1's CachedSet; compare simulated makespan against run 2's actual makespan. Report the drift like the existing simulated-vs-actual baseline check. This is the number that tells the remote-cache team whether to trust the tool — and it doubles as the regression harness for every future model refinement (incl. pullCost later). |

### 3.6 Both sources, day one

- **Identity parity:** native `call` ops are ident'd with `callKey` (the
  recipe digest, `cache.go:3722`); the OTel loader idents call ops with
  `dag.digest` — the same digest. A digest CachedSet means the same thing
  against a native dump, a local otlpdump capture, or a Cloud trace.
- **Outcome asymmetry doesn't matter here:** OTel can't distinguish
  executed/joined — but elision doesn't need to: all same-digest callers
  short-circuit, and the `call_exec` unit (present in OTel traces from the
  augmented engine) elides with its parent region.
- **Real OTel caveats, stated:** `do_not_cache` isn't visible (eligibility
  check weakens to "not marked cached"); the reflection/introspection class is
  skipped at emit. Both are report-level caveats on the OTel path, not model
  changes. If they matter in practice, the fix is a small emit — decision #5.

## 4. Implementation plan

Chunked to land independently with tests (each chunk reviewable alone; no
pushes/PRs until called).

### Chunk 1 — the elision engine in the replay (`wcanalyze`)

New file `engine/wcprof/wcanalyze/cached.go`:

- `CachedHypothesis{Idents map[string]struct{}, PullCostNS int64}` and the
  resolution pre-pass: subtree intervals over the nesting forest (one
  Euler-tour pass, computed once per graph and reused across candidate sims),
  the external-demand test over wait edges, the keep fixpoint, and the
  resulting per-op state (`elided[i]`, `hitShort[i]`, kept-region report
  data).
- Eligibility classification per ident (executed / do_not_cache / error-only /
  all-hit / open).

`replay.go` (small, surgical): `NewCachedSimulation(g, hyp)` wraps
`NewSimulation` with the resolved state; `finish(i)` gains the hit
short-circuit (`simStart + pullCost`); `advance`/`joinUpTo` skip actions whose
`ref` is elided; `spawnTo`/`finish` increment `ElidedOpDemanded` if an elided
op is ever demanded (assert-style, §3.5). Roots that are themselves elided
(none expected — roots are session-scoped) counted, not guessed.

Tests: catalog rows **V1–V16** (§4.5). The chunk is not review-complete until
they're all covered.

### Chunk 2 — selection, report, CLI (`wcanalyze` · `cmd`)

- Selector parsing (`--cached`, `--cached @file`, `--cached-class`,
  `--cached-exec`) shared by `cmd/wcprof-analyze` and
  `cmd/wcprof-otel-analyze`; applied after `ClassifyExecs` (same ordering
  discipline exec-decomp established — argv-based selection needs the
  classified graph).
- `RunWhatIfCached(g, candidates)` ranking pass (per-class groups of executed
  digests + top-N individual digests, parallel sims, budget like
  `maxWhatIfClasses`) and the explicit-set detail section: baseline vs
  counterfactual, eligibility + kept-region reporting, counterfactual
  blocking chain.
- Report wiring in `report.go`; ranking table default-on when any executed
  calls exist, detail section behind the flags.

Tests: selector resolution (digest / file / class / argv; unknown digest →
loud error), ranking on a synthetic multi-class graph, report golden-file
checks, OTel-source end-to-end on the committed testdata capture. Catalog
rows **V17–V21**.

### Chunk 3 — cold/warm calibration harness (validation)

- `hack/wcprof-cached-calibrate` (script + doc): run a chosen workload twice
  against a dev engine (cold, then warm), capture both profiles, extract run
  2's hit digests, simulate run 1 under that CachedSet, and print *simulated
  vs actual warm makespan drift*.
- An analyzer flag (`--cached-from-run <warm dump/trace>`) so the digest-set
  extraction is a first-class, testable step, not shell glue — it is also
  exactly the "comparing runs" primitive in miniature (hit-set diff between
  two runs), so it seeds Track A's later work.
- Acceptance: drift reported and understood on at least two real workloads
  (working default: a dagger-repo module build + a `Container.withExec`-heavy
  pipeline). No hard threshold in v1 — the deliverable is the honest number
  and the explanation of its gap sources (scheduler assumptions,
  uninstrumented I/O, warm-run lazy decode). Catalog rows **V22–V23**.

### Chunk 4 — Stage 2 substrate: cache-DAG input edges (shared with Track A)

The one genuinely new data path, and it serves both tracks at once:

- **OTel side (pure parsing):** the loader starts consuming `dag.inputs`
  (already emitted on every call span; currently discarded) into first-class
  cache-dependency edges in the IR — Track A's entire phase-1 gap.
- **Native side (tiny emit at the perfect seam):** `getOrInitCallInner`
  already computes the input digests for the term lookup (`requestInputs`,
  `cache.go:3713–3720`) — recording them on the call op costs no extra digest
  work. This closes the native/OTel asymmetry Track A flagged.
- **Then, and only if the kept-counters justify it:** per-branch elision
  refinement inside kept regions, designed against real residual data rather
  than speculation.

Note the prior doc's Stage 2 ("load the dropped reused-result links") is not
buildable as written — those links were never emitted (§2.2). This chunk is
the corrected form.

### Forward (not scheduled) — pull-cost model (seam only)

`pullCost` is plumbed per-hypothesis from Chunk 1 (v1 constant 0; a
`--cached-pull-cost` escape hatch for crude sensitivity checks is nearly free
and useful to the remote-cache team immediately). A *real* model — "pull or
recompute?" — needs result/layer **sizes**, which nothing records today, plus
a transfer-rate model. That's a future emit decision to design with the
cachemoney folks; the calibration harness from Chunk 3 is already the right
test bench for it when it comes.

## 4.5 Validation catalog — reasoned scenarios (doctrine §0.4 made concrete)

The authoritative test enumeration. Every row states the expected outcome
*and why it must be so*; each becomes at least one committed test (synthetic
graphs in the existing fixture style unless noted). Reviewers should treat a
chunk as incomplete until its rows are covered.

| # | Scenario (setup) | Expected outcome — derived by reason | Chunk |
|---|---|---|---|
| V1 | Empty CachedSet on any graph (incl. the committed OTel testdata capture). | Bit-for-bit equal to the baseline sim: same makespan, same per-op sim times, zero new counters. Nothing was hypothesized, so nothing may differ. | 1 |
| V2 | Leaf digest cached: one executed `call` whose subtree is a single self-time block on the critical path. | Makespan shrinks by exactly that block minus any slack; the call's finish = start + pullCost. Equals the factor-0 answer for a true leaf — the one case where the old proxy was honest. | 1 |
| V3 | Orchestrator digest cached: `call` → `call_exec` → children carrying all the cost (call self ≈ 0). | Savings ≈ the subtree's critical contribution. Factor-0 on the same graph saves ≈ 0 — the pair of assertions *is* the motivating bug, pinned as a test. | 1 |
| V4 | Executor + joiner of one digest; a downstream op waits on the joiner's result. | Both callers become hits at their own recorded starts; the joiner's waiter is unblocked at joiner.start + pullCost; the elided `call_exec` is never demanded. | 1 |
| V5 | Same digest executed twice (dup execution, the `DupExecuted` case). | Both executions' regions elide; both callers hit. Digest-level semantics has no "first" caller. | 1 |
| V6 | Ident with one executed and one already-hit call op. | The recorded hit op is untouched (it was already a lookup); the executed one short-circuits; report notes the mixed ident. | 1 |
| V7 | Elided region containing a fixed-delay (lock) wait. | The delay charges nobody: its owner never runs. Makespan reflects its absence. | 1 |
| V8 | Outside op holds its own recorded lock delay on the same named resource an elided op also waited on. | The outside waiter's delay is *unchanged* (simplification #2, §3.3): the data names no holder, so no relief is granted. Pins the limitation so it can never silently "improve". | 1 |
| V9 | External wait into a candidate region (Figure 2: another client waits on a service started inside). | Whole region kept, recorded schedule preserved exactly, cached root NOT short-circuited, `kept: externally shared (N ops, X.Xs)` reported, savings for that ident = 0. | 1 |
| V10 | Keep-fixpoint chain: keeping region A revives a waiter whose wait targets region B. | B is kept too, in the second fixpoint round. Elide-decisions may only flip toward keep; the fixpoint terminates in ≤ #regions rounds. | 1 |
| V11 | All waits into a region come from same-digest callers (the singleflight norm). | Region elides: those waiters are themselves short-circuited hits, so no live demand exists. The common case must not be blocked by its own joiners. | 1 |
| V12 | Nested cached digests: inner digest's region strictly inside outer's. | Union elides once; both roots report as hits; no double-counting of elided duration in the residual report. | 1 |
| V13 | Critical-path shift: elided chain slightly longer than a parallel non-elided chain. | Saving = the *difference* of the chains, not the elided duration. Asserts savings come from re-simulation, not subtraction. | 1 |
| V14 | Ineligibility matrix: idents with `do_not_cache` / error-only / canceled-only / open-at-dump ops. | Each ineligible with its specific reason in the report; the sim result equals baseline for those idents. Simulating the engine caching what it refuses to cache is fiction, and fiction is refused loudly. | 1 |
| V15 | Corrupted fixture: an op inside an "elided" region referenced by a live wait the pre-pass was (deliberately) blinded to. | `ElidedOpDemanded` > 0 and the what-if-cached section fails its gate. The assert must be reachable and loud, never compensating. | 1 |
| V16 | pullCost > 0 on V2's graph. | Finish shifts by exactly pullCost; savings shrink by exactly pullCost when the hit is on the critical path, by less (or zero) when slack absorbs it. The seam works before any real cost model exists. | 1 |
| V17 | Selector resolution: digest, `@file`, class pattern, argv pattern; unknown digest; pattern matching zero executed idents. | Each resolves to the reasoned digest set; unknowns and empty matches are loud errors, not silent no-ops. | 2 |
| V18 | Ranking non-additivity: two digests on one dependency chain, ranked individually and simulated jointly. | joint saving < sum of individual savings; table header carries the non-additivity note. (Reason: the chains overlap; elision of one shifts the other off the critical path.) | 2 |
| V19 | Cross-source parity: the same synthetic workload expressed as native events and as OTel spans (existing dual-fixture style). | Identical CachedSet → identical elision decisions and savings. Digest identity is the same key on both sources; any divergence is a loader bug, not a model choice. | 2 |
| V20 | OTel end-to-end on committed testdata: cache a digest visible in `baseline-simple-noservice.jsonl`. | Elision + savings line appear; gate-clean. Exercises the real front-end path, not just Build(). | 2 |
| V21 | Report goldens: ranking table, explicit-set detail, eligibility + kept-region sections. | Stable, deterministic output (the replay is deterministic by design; the report must not introduce map-order nondeterminism). | 2 |
| V22 | Calibration, mechanical half: extract hit digests from a warm capture (`--cached-from-run`) on fixtures where the hit set is known. | Exactly the digests whose warm outcome is hit; nothing inferred. | 3 |
| V23 | Calibration, empirical half: cold+warm runs of ≥2 real workloads; simulate cold under warm's hit set. | Deliverable is the honest drift number vs the warm run's actual makespan, plus an accounting of gap sources (unlimited-resource assumption, uninstrumented I/O, warm-run lazy decode). No threshold-gaming; the number is the product. | 3 |

## 5. Sequencing note: how this meets Track A

Chunks 1–3 are self-contained inside `wcanalyze`/`cmd` — no engine changes, no
emit changes, usable on existing captures the day Chunk 2 lands. Chunk 4 is
the deliberate convergence point: one data-path change (cache-DAG edges in the
IR, both sources) that simultaneously unblocks Track A's invalidation frontier
walk, Track A's run-comparison Merkle-diff, and Track B's precision
refinement. Sequenced after Chunk 3 (decision #3): the calibration number is
what makes everything downstream credible.

## 6. Decisions — settled (Erik, 2026-07-04)

Erik adopted all recommendations; the list is kept for the record. On #4
(calibration workloads) the working default is a dagger-repo module build + a
`withExec`-heavy pipeline, to be revisited with the remote-cache (cachemoney)
team. Erik also reviewed the e-graph question and accepted the
recipe-digest-only simplification for v1 (stated as simplification #1, §3.1).

1. **v1 semantics:** digest-level hypotheses + local-warm-hit model +
   whole-region elide-or-keep with loud residuals (no partial elision, no
   bound claims). **Adopted.**
2. **Ranking candidates:** per-class groups + top-N individual digests,
   default-on. **Adopted.**
3. **Chunk 4 timing:** after Chunk 3 — the calibration number first.
   **Adopted.**
4. **Calibration workloads:** dagger-repo module build + a `withExec`-heavy
   pipeline (working default, to revisit with cachemoney). **Adopted.**
5. **OTel emit nits** (`do_not_cache` visibility, executed/joined): batched
   into Chunk 4's emit change. **Adopted.**

## 6.5 Implementation notes (Chunk 1) — corners the page leaves implicit

These are not design changes; they are the resolutions of underdetermined
corners, each derived from the doctrine (§0) and the elide-or-keep semantics
(§3.3), recorded here so review can attack them explicitly.

1. **Cached calls inside kept regions are not short-circuited.** Forced by
   "a kept region replays exactly as recorded": short-circuiting a nested
   cached call would shift the kept region's internal clock (its parent's
   joins land earlier), silently deforming the exact replay V9 requires — and
   eliding that call's own sub-region while its parent region is kept would
   trip `ElidedOpDemanded` mechanically. Such calls are tallied as `KeptCalls`
   and their sub-regions are kept by the fixpoint (the "structural demand"
   rule: a region whose root is live must be kept). This is also what makes
   V10's fixpoint chain work.
2. **A kept region's root is live for the demand test.** The root replays as
   recorded (only that anchors and gates the kept region), so its own gating
   waits — including any into other regions — count as demand.
3. **All non-hit calls of an eligible ident short-circuit, including
   errored/canceled ones.** Derivation: under the hypothesis, the lookup
   precedes execution/join (verified at `cache.go:3731` — the cache lookup
   happens before singleflight), and a hit cannot fail while waiting for a
   production that doesn't happen. The per-ident outcome tallies (successes /
   failures) are in the eligibility report, so the reader sees exactly what
   was hypothesized into a hit. Recorded hits stay untouched (V6): their
   recorded duration IS the measured hit cost — data, not hypothesis.
4. **The open-at-dump check is region-inclusive.** "Ident's ops open at dump
   time" covers the ident's call ops AND any op inside its producing regions:
   an open op inside the region means the production is not fully recorded,
   so what its elision would remove is not determinable. Refusal direction:
   more refusals, never fabrication.
5. **Abandoned waits never demand a keep.** The demand test uses exactly the
   predicate the program compiler uses for gating joins (`joinWait`, one
   shared function): a wait the waiter abandoned before its target ended
   gates nothing in the replay, so eliding its target cannot corrupt the
   waiter's schedule. Consistency between compiler and demand test is what
   makes `ElidedOpDemanded == 0` provable rather than hoped.
6. **Orphan waits (no owning op) never demand a keep, but are reported.** The
   replay cannot model them at all (there is no waiter op to gate), so the
   elision engine stays consistent with the baseline replay's model of the
   same data; `OrphanWaitsIntoElided` (count + duration) is printed so the
   hint of unmodeled demand is never silent.
7. **Residual durations are self-time sums.** Elided/kept sizes are reported
   as op counts + total `SelfNS` over the region's ops (each op exactly once,
   maximal-region union for the elided side) — the same currency as the class
   table, and immune to nesting double-counts (V12).
8. **`call_exec` ops carrying a cached ident outside every region of that
   ident** (an executor call op missing from the data) are counted per-ident
   as `UnanchoredExecs`: their work cannot be attributed to a producing
   region and keeps running. Loud residual; the missing parent is separately
   a gate concern on the OTel side (`OrphanedParents`).
9. **An ended call op with no recorded outcome makes its ident ineligible**
   (`IdentUnknownOutcome`). An outcome-less ended call is suspect data — both
   sources always stamp call outcomes — and hypothesizing it into a hit would
   be guessing (§0.2). Open calls are NOT counted here: an open op's outcome
   doesn't exist yet (that is the open condition, refused on its own terms).
10. **V12's "both roots report as hits" reporting contract.** A cached call
    covered by another cached digest's elided region never occurs under the
    counterfactual (its parent region is gone), so it carries no `hitShort`
    mark in the replay; in the report it is SATISFIED and rendered as a hit
    (`ShortCircuited + ElidedCalls`). The data keeps the two tallies distinct
    so the replay state stays honest while the report matches the row's
    letter.
11. **The ranking's `removed-self` column and its candidate budget.** The
    column sums elided-region self-time and the short-circuited calls' own
    self-time (`HitCallSelfNS`) — on un-augmented OTel captures, where the
    producing work is folded into the call span, the latter is the ONLY
    removed work. The split stays visible in the detail residual line. The
    individual-digest candidates are a stated top-N budget ordered by the
    ident's producing wall-clock (max non-hit successful call duration — the
    interval bounding its producing subtree, an upper-bound proxy); the
    budget and the ordering are printed in the table header, never silent,
    and every emitted row's saving is a full re-simulation.

## 7. Appendix — verified code facts this design rests on

| Fact | Where (branch `db1caae0`) |
|---|---|
| Call op minted per caller; ident = recipe digest (`callKey`); outcome hit/executed/joined/do_not_cache; ResultID stamped at end. | `dagql/cache.go:3614, 3722, 3619–3629` |
| Input digests already computed at the call site (for term lookup) — the free native emit seam for Chunk 4. | `dagql/cache.go:3709–3720` |
| `call_exec` is a child of the executing caller; joiners wait on it (`singleflight`/`call_exec` reasons); `publishResult` parented under it. | `dagql/cache.go:3772, 3999, 4072` |
| Lazy ops are parented under the *triggering* op and reached by `lazy` waits — so consumer-triggered lazy work is outside a producer's elision region by construction. | `dagql/cache.go:3045, 2988, 3141` |
| `LinkKindResult`/`LinkKindReusedResult`: defined, never emitted; only link emit is nested-client. | `wcprof.go:210–235; engineutil/executor.go:144` |
| Replay: per-class factors only; applied at `actSelf`; `finish/advance` always descend; `joinUpTo` finishes ended children at every action point; `spawnTo` prefix-anchors out-of-order references; order-independence enforced via `SimStartConflicts`/`UnschedulableOps`. | `wcanalyze/replay.go` |
| OTel loader: call-op ident = `dag.digest`; outcome from `CachedAttr` → hit else ok/error/canceled; `dag.inputs` present on spans (incl. committed testdata) and currently unread; only `nested_client` links consumed by the graph builder. | `wcotel/loader.go:375–415, 505–532; wcanalyze/graph.go:296; wcotel/testdata/*.jsonl` |

Governing principle throughout: analysis is a rational function of faithful
data — residuals are measured and printed, never silently absorbed.
