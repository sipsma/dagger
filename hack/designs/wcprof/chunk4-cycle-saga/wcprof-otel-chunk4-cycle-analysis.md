# Chunk 4 implementer's cycle analysis — TO BE EVALUATED INDEPENDENTLY, NOT TRUSTED

This is the Chunk 4 implementer's report on a **cycle** the §6.1 gate flagged on a module-loading workload, plus its theory of the cause. The project owner's instruction: **do not take any of this for granted, do not believe it offhand — each reviewer must independently look at the loop, decide what evidence we actually have for the cause, and what (if anything) we could do about it.** Treat this as a hypothesis to verify/refute, not a conclusion.

## How it surfaced

On a `dagger -c <shell>` workload (which loads the dagger repo's own modules — `cli`, `go-sdk`, `engine-dev`, `docs`), the §6.1 structural gate **fails with 5 cycles + 18 fallback anchors**. Module-free `dagger query` exec/service workloads are cycle-free and gate-green. This is the **first module-loading workload exercised live with the OTel augmentation** (Chunks 2/3 were validated on core-API and lazy-Directory workloads).

## The cycle, concretely (implementer's instrumentation of the replay's cycle-break point)

5 cycles, **all in `Query.moduleSource`**. One representative cycle:

```
op#94 call_exec Query.moduleSource   [160..195ms, self 16ms]
  ⇒ ... (join/anchor chain through "load module: cli/go-sdk" + nested
         call/call_exec moduleSource ops) ⇒
op#54 call_exec Query.moduleSource   [160..205ms, self 16ms]
op#54 --wait:singleflight--> op#94    ← closes the loop
```

The closing edge, with timestamps:
```
closing wait "singleflight": waiter[160..205] target[160..195]  waitEnd=195 targetEnd=195
  -> classified = actWaitJoin (RECURSES)
```

## Implementer's theory: real graph cycle, over-serialization artifact (NOT a true deadlock, NOT a replay bug)

- op#94 and op#54 are **two concurrent `moduleSource` executions** (both start at 160ms — distinct module loads sharing work).
- op#54's execution makes a sub-call that **singleflight-joins op#94's in-flight execution** → a *genuine* dependency `op#54 → op#94` (its wait ends exactly at op#94's end, 195ms → classified `actWaitJoin`).
- The reverse edge `op#94 ⇒ … ⇒ op#54` is the **implicit join** (design §1.1) inferring op#94 synchronously waited for everything nested in its subtree. The implementer claims **this edge is FALSE**: op#94 did not synchronously wait for op#54 — they are concurrent singleflight peers whose OTel trace subtrees *interleave* (one module's load triggered the other's lookup, so context propagation nests them) even though op#94 never blocked on op#54.
- The real singleflight edge + the (claimed-false) implicit-join edge form the loop. The workload **completed (rc=0, no real deadlock)** — which the implementer takes as confirming the circular dependency does not reflect real execution.
- Framed as the §1.1/§2.2 hazard: *OTel parentage is built from context propagation, not the live call stack; where work is detached/concurrent, the implicit join can invent dependencies (over-serialization) or even cycles.* The replay's cycle-break (assume recorded duration) is the safety valve; the gate flags it per §6.1.

## Implementer's claim: NOT caused by Chunk 4 — and its evidence

- **Strip test:** removed **every** Chunk 4 op (`exec.run`/`containerStart`/`processRun`/`service.start`) from the captured trace and re-ran the gate → ops 11162→11159, **identical 5 cycles + 18 fallback anchors**. Every op in every cycle is `call_exec`/`call Query.moduleSource` or a `load module:` span — **zero Chunk 4 kinds**.
- Its exec spans are **leaf children** of the module-runtime `call_exec`s; they add no back-edges and (claimed) don't alter the moduleSource ops' intervals.
- Module-free exec/service traces (which still emit its `exec.run`/`processRun`) are cycle-free.
- Conclusion (its): the cycle correlates with **module loading**, not its emit — a pre-existing Chunk 2 / module-loading / nested-client interaction.
- **Note:** the Chunk 4 commit `4d6987fdc2` diff touches no `replay.go`, no `wcotel/loader.go`/`gate.go` — purely additive emit (corroborates the claim that Chunk 4 didn't restructure the analysis path).

## Implementer's "in scope / expected?" assessment

- The design **anticipates the hazard class** (§1.1, §2.2) and takes the position (§6.1) that "a cycle ⇒ unfaithful emit, fix at the emit side."
- But the **specific case is not handled**: §3.1's singleflight fix assumes *"the joiners are in a different subtree (the execution is not nested under them)"* — concurrent module-source loads that **cross-join through nesting** violate that assumption.
- Routing options it offered: (1) owner = whoever owns Chunk 2 (singleflight) + module-loading/nested-client; (2) investigate why two concurrent `moduleSource` executions singleflight-relate *and* nest such that the implicit join over-serializes — is the nesting avoidable at emit, or do we need to suppress/reclassify a singleflight wait whose target is also an implicit-join ancestor/peer?; (3) **or accept as a §9 reserve seam** (the cycle-break already yields an approximately-correct result; document rather than block v1); (4) **definitive confirmation available:** rebuild the engine at Chunk 3 HEAD (no Chunk 4) and re-capture to show the cycle reproduces with Chunk 4 entirely absent (the strip test proves it from the data; the from-source rebuild would close the loop).

## Evidence access (for reviewers)

The raw artifacts (`/tmp/otel-exec.jsonl` = the 9.3MB cycle trace, `/tmp/otel-exec-noch4.jsonl` = stripped) live in the **Chunk 4 implementer's container** — they are **not** directly accessible to reviewers. Reviewers should reason from the cycle structure described above + the **code** (the Chunk 2 `call_exec`/singleflight emit, the module-loading/nested-client path, the implicit-join + `actWaitJoin` classification in `wcanalyze/replay.go`/`graph.go`) + the **design** (§1.1, §2.1, §2.2, §3.1's "different subtree" assumption). If a reviewer judges the raw trace or a from-source rebuild necessary to reach a confident conclusion, say so explicitly (the lead can arrange a reproduction or have the implementer extract the relevant cycle subgraph).
