# what-if-cached — cold/warm calibration results (V23)

Chunk 3's empirical deliverable (design `whatif-cached-design.md` §3.5 gate 4,
catalog row V23): the honest drift number on two real workloads, plus the
accounting of its gap sources. No threshold is enforced — the number and its
explanation are the product. Captured 2026-07-04 against a fresh dev engine
built from this branch (`hack/wcprof-cached-calibrate` flow, run manually with
an isolated container/port; native wcprof dumps).

## Method

Per workload: reset the dev engine (container + cache volume), enable wcprof,
run the workload (cold), dump (flushing), run it again (warm), dump. Then
`wcprof-analyze -cached-from-run warm.wcprof cold.wcprof`: extract the warm
run's actual hit digests (`HitDigests`, V22), simulate the cold run under that
CachedSet, and compare the simulated counterfactual makespan against the warm
run's actual makespan.

## Results

| workload | cold actual | warm actual | cold baseline (sim) | counterfactual (sim) | saved | drift vs warm |
|---|---|---|---|---|---|---|
| withExec pipeline¹ | 5.4s wall | 833.9ms | 5.28s | 5.28s | 858µs (0.0%) | **+533%** |
| module build² | 22.7s wall | 121.6ms | 22.55s | 20.88s | 1.67s (7.4%) | **+17069%** |

¹ `container | from alpine:3.20 | with-exec apk add curl | with-exec sh -c
"seq 2000000 | sha256sum" | with-exec echo done | stdout` (dagger shell, empty
cwd). Warm hit set: 1481 digests, 1477 found in the cold trace; 3,059 ops
elided (10.22s self) + 21.24s hit-call self removed.
² `dagger -m modules/alpine functions` (the dagger-repo alpine module's
load+build pipeline). Warm hit set: 1437 digests, 1435 found; 2,996 ops elided
(9.33s self) + 15.75s hit-call self removed.

Both drifts are large and both have a precise, verified cause. Neither is
simulator noise: the elision engine did exactly what the recorded data lets it
do, and the counterfactual blocking chain names the residual in each case.

## Gap source 1 — consumer-parented lazy execution (withExec pipeline)

The counterfactual blocking chain after caching all 1,477 digests:

```
call       Container.stdout
call_exec  Container.stdout
lazy       Container.withExec     <- the container run lives HERE
exec_phase withExec.applyOutputs
```

The `Container.withExec` digest IS in the warm hit set and its call's region
elides — but that region contains only the thin resolver. The actual container
run executes at `Evaluate` time under a **lazy op parented to the consumer**
(`Container.stdout`'s call_exec), outside the producer's nesting subtree —
exactly the appendix fact "consumer-triggered lazy work is outside a
producer's elision region by construction". Under the hypothesis (result
materialized in the local warm cache), that lazy execution would not run; v1's
call-subtree region cannot remove it, so savings honestly report ≈ 0.

The data is not silent about the attribution: the `exec.run` op under the lazy
op carries `Ident = the withExec call digest` (an explicit emit —
`executor.go` `execIdent = execMD.CallDigest`). Extending elision regions with
ident-attributed exec subtrees would close this gap with zero inference, but
it changes the approved §3.3 region definition (call subtrees only) — a design
decision escalated to the workstream lead, not taken unilaterally here.

## Gap source 2 — run-specific recipe digests on the module chain (module build)

The cold run's dominant op is one `exec_phase: codegen generate-typedefs`
(20.37s user self) inside `ModuleSource.asModule`'s region. The ranking proves
the machinery can sweep it: caching `ModuleSource.asModule
xxh3:c6f4402935eaf72a` saves **22.17s of the 22.55s baseline**. But the
calibration only saved 1.67s, because the warm run's hit set does not contain
that digest:

```
cold  ModuleSource.asModule  xxh3:c6f4402935eaf72a  executed   warm-hit=false
cold  Query.moduleSource     xxh3:ee887a5837434661  executed   warm-hit=false
warm-hit digests NOT present in the cold trace: xxh3:93ff5f87f53c8db0, xxh3:9d63dd6b70dedbdb
```

The module-source call chain's recipe digests differ between the two runs
(run-specific inputs in the recipe); the real warm run still hit through the
cache's **equivalence machinery** (content digests / e-graph), which the
recorded data does not carry — exactly stated simplification #1, whose error
direction (savings understated) this measurement now quantifies at scale for
module loads. Only the 1,435 stable inner typedef digests transferred,
yielding the 1.67s. The remedy is the simplification's stated data path
(emitting equivalence facts), never an analyzer heuristic.

## Secondary gap sources (minor here)

- Session-root self-time (client think time between queries) replays
  unscaled; on these single-invocation traces it is small.
- 4 (resp. 2) warm hit digests were not present in the cold trace at all —
  run-specific digests in the other direction, listed not-found by
  eligibility.
- Unlimited-resource scheduling and uninstrumented I/O — not observed as
  material on these traces (baseline sim drift vs cold actual was ≈ 0 on
  both).

## What this means

The calibration harness did its §3.5 job: it converts "trust the simulator"
into two named, quantified data gaps. For workloads whose warm hits transfer
by recipe digest (the stable inner-call layer), the elision engine measures
real savings; for the two dominant cost carriers of these workloads —
lazy-deferred execution and equivalence-resolved module chains — the v1 model
understates savings, in the direction the design predicted, by amounts the
report now prints instead of hiding. Both remedies are data-path work
(exec-attribution regions; equivalence-fact emit), consistent with doctrine
§0.3: fix the data, never the model.
