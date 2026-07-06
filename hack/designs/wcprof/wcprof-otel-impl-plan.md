# wcprof × OTel — execution roadmap (chunk plan)

**Status:** plan for review (no code written). **For:** a fresh implementer.
**Contract of record:** [`wcprof-otel-design.md`](./wcprof-otel-design.md) — the
approved design. This roadmap only *sequences* that design into reviewable chunks;
it does not change it. Every chunk boundary cites design sections (§) and the
engine `file:line` anchors those sections already established.

This is a planning deliverable. **Do not implement from it yet** — it is reviewed
first (Erik + lead), same as the design rounds.

---

## 0. How to use this plan

### The per-chunk cycle (the unit of progress)

> implement one chunk → Codex review until convergence → **only then** start the
> next chunk.

A chunk is **done** when *both* hold:

1. its **gating validation** (the design §6 fixtures / §6.1 invariants / §6.2
   oracle checks named in the chunk) is **green**, and
2. **Codex has converged** on it, reviewed **both ways**:
   - **Individually** — the chunk's own changes: correctness + detail-alignment.
   - **Holistically** — how chunks `1..N` come together: is the whole converging
     on the north star, or did something surface a new problem?

### What every review checks (build the chunks so this is answerable)

1. **Details + correctness.**
2. **Fidelity to the design doc (the contract).** Is the implementation sticking
   to the doc? Every divergence is surfaced and **classified**:
   - a **miss** (an error) → fix the code; or
   - a **justified new discovery** (something real surfaced) → **update the design
     doc** and record why. The doc stays *living*: accepted divergences fold back
     so it never lies. Cite the doc § you changed in the commit/PR notes.
3. **Still on-goal?** Heading toward the north star — *"why was my CI run slow?"*
   answered from one Dagger Cloud trace, **user work first-class**, via the
   **same** `wcanalyze` analyzer/replay (unchanged) — or did something signal a new
   problem? Watch both the new changes and the cumulative whole.

### North star (the holistic yardstick, restated)

A Dagger Cloud trace (local or CI) compiles — with **zero causal inference in the
loader** (design §5) — into the existing wcprof IR and produces a faithful
bottleneck ranking from the **unchanged** replay (design §1, §1.1). A slow
`go build` in the user's own code is a valid headline answer (design §3.3). The
**cross-source oracle** (native wcprof vs OTel, same run, agreeing `RunWhatIfs`
top-N — design §6.2) is the contract that proves each chunk *converged* rather
than *drifted*.

---

## 1. Process & environment (self-contained — this is no longer design-only)

- **Branch off current `upstream/main`.** It already contains `engine/wcprof/**`,
  the native recorder, *and* `resumedCallbackSpan` (`dagql/cache.go:2797`). Do
  **not** branch off a stale `main` — an earlier base hid the lazy re-point break
  that the whole §3.2 design exists to handle.
- **Commits:** authored `Erik Sipsma <erik@sipsma.dev>` with a matching
  `Signed-off-by: Erik Sipsma <erik@sipsma.dev>` trailer. **No agent/Claude
  attribution.** **Never push** — local commits only; humans handle remotes.
- **Testbed = engine-dev.** The `engine-debugging` skill covers building/running
  engine-dev and replaying Cloud traces; the `telemetry-capture` skill covers
  capturing the engine's OTel via `hack/otlpdump` to JSONL. Build/iterate the
  loader against captured JSONL fixtures (offline, fast) before the Cloud swap.
- **Oracle runbook (design §6.2):** on one dev-engine run with a single workload,
  enable **both** sources — native wcprof (`_DAGGER_WCPROF=1`, or `--profile` for
  per-session) **and** the OTel augmentation under build — then dump native via the
  `/debug/wcprof/dump` endpoint and capture the OTel trace via `otlpdump`. Compile
  **both** to the IR and compare `wcanalyze.RunWhatIfs` top-N (`replay.go:485`).
  They must be from the *same* run to be comparable.
- **Cloud creds** live at `~/.config/dagger/{credentials.json,org}`. **Read** them
  for the Cloud round-trip (chunk 5); **never print** them.

---

## 2. Dependency DAG (not a straight line)

```
            ┌─────────────────────────────────────────────┐
            │ CHUNK 1  Foundation                          │
            │ offline loader + telemetry vocabulary +      │
            │ LinkCountLimit + §6.1 structural gate        │
            └───────────────┬─────────────────────────────┘
                            │ (loader validates everything below;
                            │  wait-link vocabulary; cap)
                            ▼
            ┌─────────────────────────────────────────────┐
            │ CHUNK 2  Singleflight central fix            │
            │ call_exec + per-caller wait links +          │
            │ publishResult; ORACLE (§6.2) COMES ONLINE    │
            └───────┬───────────────────────────┬─────────┘
                    │                            │
   (lazy work's    │                            │ (exec.run is a CHILD of
    sub-calls need │                            │  call_exec — hard dep)
    faithful       ▼                            ▼
    call_exec) ┌────────────────────┐   ┌────────────────────────────┐
               │ CHUNK 3  Lazy       │   │ CHUNK 4  Exec split +      │
               │ lazy op (Inv. T) +  │   │ Services                   │
               │ wcprof.parent +     │   │ exec.run/containerStart/   │
               │ stamping processor  │   │ processRun + service.start │
               └─────────┬───────────┘   └─────────────┬──────────────┘
                         │                              │
                         └──────────────┬───────────────┘
                                        ▼
            ┌─────────────────────────────────────────────┐
            │ CHUNK 5  Productionization                   │
            │ Cloud ingest swap (§6.6) + standing drift    │
            │ gate (§6.4); v1 complete                     │
            └─────────────────────────────────────────────┘
```

Edges that are **not** obvious linear order (flagged in §4 below):

- **Chunk 4 (exec) depends on Chunk 2**, not just on Chunk 1: `exec.run` is emitted
  as a child of the `call_exec` span (design §3.3), which Chunk 2 introduces.
- **Chunk 3 (lazy) and Chunk 4 (exec/services) are siblings** off Chunk 2 — neither
  depends on the other. They are *sequenced* 3-then-4 (matching design §7) for
  review focus, but the DAG permits either order. Services in particular depends
  only on Chunk 1's wait vocabulary.

The plan-review's validation-plumbing additions don't change this DAG (no new
nodes or edges): the `hack/otlpdump` dropped-count fields live inside Chunk 1, and
the persisted-cache drift fixture inside Chunk 2's DoD (see those entries and §4
below). The dependency they create is intra-/adjacent-chunk: Chunk 2's cap-stress
fixture consumes Chunk 1's otlpdump extension.

---

## 3. The chunks

Sequencing principles honored throughout (design §7): **oracle-early** (strongest
gate live from Chunk 2), **otlpdump-JSONL first** (Cloud swap is the last chunk),
**mergeable units** (additive emit keeps the engine working; the loader is
developed against fixtures ahead of emit), **reserve seams stay out of v1**
(§5 below).

### Chunk 1 — Foundation: offline loader + telemetry vocabulary + structural gate

- **Coherent unit:** the entire *offline analysis path* plus the engine's *wcprof
  telemetry vocabulary*. After this, you can point the loader at any Dagger OTel
  trace and get a wcprof report — the deliberately-**wrong** baseline that
  motivates every later chunk — and the engine speaks the wcprof wire format
  (inertly).
- **Scope IN:**
  - The **loader** (design §5, all four steps): otlpdump-JSONL → `[]wcprof.DumpEvent`
    + `wcprof.DumpHeader` → `wcanalyze.Build` (`graph.go:147`) → `WriteReport`.
    Implements *all* mapping now, even for signals nothing emits yet: live-span
    dedup (keep ended copy); **causal parent = `wcprof.parent ?? parentId`** (reads
    the override if present — none yet); class = span name; ident = `dag.digest`;
    work-type/outcome from attrs; **absolute-unix-ns → trace-min-start rebasing**
    for op intervals *and* (when present) `wcprof.wait.*_unix_ns` link intervals.
  - **Op-kind classification precedence (spell it out so `withExec` can't drift).**
    `wcprof.op.kind` (when present) **always wins**. A DagQL **call** span — which
    always carries `dag.digest` + a `Type.Field` name (`core/telemetry.go:45-51,83-86`)
    — stays `call`, including `Container.withExec` (native models it as a `call`
    *plus* a `call_exec`, `dagql/cache.go:3515,3673`; the executor work is the
    separate `exec.run`, `engine/engineutil/executor.go:122`, which Chunk 4 stamps
    `wcprof.op.kind=exec`). The structural `withExec ⇒ exec` fallback exists **only**
    to give the intentionally-wrong un-augmented baseline *some* shape, and **must
    not** override the corrected shape once `call_exec`/`exec.run` are emitted —
    i.e. once a span has `wcprof.op.kind`, or a `call_exec` child exists, the
    fallback never fires. This keeps the class table converging to native, not
    drifting (design §5 step 2; the precedence here makes design §5's
    "structural not causal" rule unambiguous).
  - The **wcprof vocabulary as shared constants** (design §3.0): `link.purpose="wait"`,
    `wcprof.wait.{start_unix_ns,end_unix_ns,reason,ident}`, `wcprof.op.kind`,
    `wcprof.work_type`, `wcprof.parent` — one definition referenced by both loader
    and the later emit chunks. **`wcprof.parent` wire encoding is the lower-hex OTel
    span-id string** (the 16-char form `hex.EncodeToString(spanID[:])`, matching how
    otlpdump and the Cloud API render span ids) — pin it here so the Chunk 3 stamping
    processor and the loader cannot pick different encodings (design §3.0.2 says
    "span id" but not the representation).
  - **`LinkCountLimit = 16384`** on the engine's per-client tracer provider
    (design §3.0; `engine/server/session.go:684-715`) — raises the default 128 so
    the suppressed-fan-in waits of Chunk 2+ are never silently evicted. Build it
    **from `sdktrace.NewSpanLimits()` then set `LinkCountLimit = 16384`** (so the
    other limits keep their defaults): `WithRawSpanLimits` treats a zero-valued
    struct as *real* zero limits (`provider.go:461`), so a naive
    `WithRawSpanLimits(SpanLimits{LinkCountLimit:16384})` would silently zero every
    other limit. (`WithSpanLimits` coerces zeros→defaults, `provider.go:421-443`, so
    it is also safe — but populating from `NewSpanLimits()` is the unambiguous
    pattern.) Inert today (nothing emits many links yet); additive config.
  - **Extend `hack/otlpdump` to emit dropped-count fields** so the structural gate is
    actually observable on the local path. Current otlpdump serializes links as
    `{traceId, spanId, attrs}` only (`hack/otlpdump/main.go:124-134`) and omits the
    OTLP `Span.DroppedLinksCount` / per-link `DroppedAttributesCount` (carried in the
    proto, e.g. `go.opentelemetry.io/proto/otlp .../trace.pb.go:556,870`). Add them
    to the JSON (additive: `droppedLinks` on the span rec, `droppedAttrs` per link).
    Without this, "zero dropped links" (below, and Chunk 2's cap-stress) is
    unverifiable from captured JSONL.
  - The **§6.1 structural gate** as a runnable check after `Build`: `CycleWarnings
    == 0`, no op self-time > makespan / no op interval > trace span, bounded
    `FallbackAnchors`, and (otlpdump path, now observable via the extension above)
    zero dropped links / dropped link-attributes.
- **Scope OUT:** any *emission* of wait links, `call_exec`, `wcprof.parent`, the
  stamping processor, or phase spans (Chunks 2–4). The oracle (no augmented data
  to compare yet).
- **Engine touch-points:** design §5 (loader); §3.0 (constants, `LinkCountLimit`);
  `engine/wcprof/dump.go` (IR types), `engine/wcprof/wcanalyze/graph.go:147`
  (`Build`); a loader entrypoint alongside `cmd/wcprof-analyze`;
  `engine/server/session.go:684-715` (tracer-provider config + span limits);
  `hack/otlpdump/main.go:124-134` (add dropped-count fields).
- **Gating validation = DoD:** the loader compiles a captured otlpdump trace of a
  **simple, no-service** workload on the **un-augmented** engine with no error and
  renders a report; the §6.1 gate runs and passes on that baseline (it will show the
  *known-wrong* shape — joiner waits as self-time, lazy work under the producer —
  but **no cycles, no malformed graph, no crash**). Keep the baseline workload
  service-free on purpose: a long-lived service-availability span could trip the
  "no op interval > trace span" / self-time invariants for exactly the reason §3.4
  fixes later (Chunk 4), a false positive on the baseline. The otlpdump extension
  is exercised here (the gate reads the new dropped-count fields). The oracle does
  **not** run yet (expected). **+ Codex converged** (individual + holistic: "is this
  a sound foundation?").
- **Dependencies:** none (first chunk).
- **Cumulative state after Chunk 1:** an offline loader that turns any Dagger OTel
  trace into a wcprof report — but the *baseline* report is deliberately wrong
  (the four breaks of design §2 are all present). The engine carries the wcprof
  vocabulary + raised link cap, all inert. This is the measuring stick: every later
  chunk is "this break is now fixed; the oracle now agrees here."

### Chunk 2 — The singleflight central fix (call_exec + wait links + publishResult)

- **Coherent unit:** the central repair (design §3.1) — and the chunk that brings
  the **oracle online**. After it, cache-miss / singleflight / joiner attribution
  is faithful and provable.
- **Scope IN (design §3.1, Invariant T §3.0.1):**
  - The **`call_exec` span**, minted on the call's detached context **under
    `callsMu`, before** the `ongoingCalls` publish and unlock (Invariant T;
    `dagql/cache.go:3669-3677`, store on `oc` at `:3693`, publish `:3697`, unlock
    `:3715`); the resolver `fn` runs under it (`:3700-3713`); attrs
    `wcprof.op.kind=call_exec` + `ui.passthrough` + `dag.digest`.
  - **Per-caller wait links** emitted from the cache layer `c.wait`
    (`dagql/cache.go:3853-3880`, target `oc`'s `call_exec`; reason `singleflight`
    for joiners, `call_exec` for the executor), via `span.AddLink` on the live
    caller-or-ancestor span — so **suppressed** callers' waits land on the ancestor
    (design §3.1; the §3.0 cap is what makes that safe).
  - The **`dagql.publishResult` span** under `call_exec` (`dagql/cache.go:3922-3947`)
    — a **native-parity diagnostic** (design §3.1): emitted so the per-class table
    matches native, explicitly **not** a counterfactual-attribution fix.
  - The **oracle harness** (test infra reused by Chunks 2–5): run native + OTel on
    one workload, compile both, diff `RunWhatIfs` top-N (design §6.2).
- **Scope OUT:** lazy, exec-split, services, Cloud (later); the stamping processor
  (Chunk 3).
- **Engine touch-points:** design §3.1; `dagql/cache.go:3505-3717` (`getOrInitCall`),
  `:3853-3947` (`c.wait` + `publishResult`); loader already handles these spans
  (Chunk 1).
- **Gating validation = DoD:**
  - **§6.2 oracle (now live):** native↔OTel top-N agree within tolerance on
    **singleflight-heavy** workloads (concurrent identical calls, fan-in).
  - **§6.3 known-answer:** an injected `sleep N` ranks with `SavedNS ≈ N`; a
    parallel off-critical-path `sleep` does **not** rank.
  - **§6.5 fixtures:** singleflight fan-in (all callers credit the shared
    execution); emitter≠executor race (resolver children never mis-parent);
    many-suppressed-siblings **cap-stress** (thousands of concurrent suppressed
    waits on one ancestor, otlpdump path, **zero dropped links** at 16384 —
    asserted via the dropped-count fields Chunk 1 added to otlpdump).
  - **§6.5 persisted-cache import/decode drift fixture** (design §6.5; scheduled
    here because Chunk 2 owns the cache/singleflight oracle harness and touches
    `dagql/cache.go`). Run after an engine restart with imported cache: assert (a)
    the first-occurrence decode shows as call-span self-time, and (b) a
    repeated/concurrent suppressed-decode workload does **not** drift native↔OTel
    top-N. This fixture is the **gate that decides whether the persisted-decode
    reserve seam** (§5 below; the `attachDepsWaitCh`/`persistDecodeWaitCh` waits with
    no `wcprof.BeginWait`, `dagql/cache_persistence_import.go:563-620`) ever needs
    promoting — so it must actually run, not just exist. If it shows non-trivial
    drift, that promotion is triggered (fix native first). *Fallback:* if it cannot
    land in Chunk 2, it is a hard prerequisite for declaring v1 (no later than
    Chunk 5), but Chunk 2 is the intended home.
  - **§6.1** still green — and the baseline's joiner-self-time inflation is now
    **gone**.
  - **+ Codex converged.**
- **Dependencies:** Chunk 1 (loader to validate; wait vocabulary; `LinkCountLimit`;
  and the otlpdump dropped-count fields the cap-stress fixture asserts on).
- **Cumulative state after Chunk 2:** singleflight/joiner/cache-miss attribution is
  faithful; the oracle is **live** and converges on singleflight-heavy workloads.
  Lazy-heavy and exec/service workloads still drift — the oracle on those is
  *expected* to disagree, which scopes Chunks 3–4.

### Chunk 3 — Lazy / deferred evaluation (+ the stamping processor)

- **Coherent unit:** the lazy choke point (design §3.2) and the one mechanism it
  needs — the `wcprof.parent` stamping processor (§3.0.2). They are reviewed
  together because the processor has **no purpose without** the lazy emit that
  drives it (this is the deliberate placement choice; see §4 below).
- **Scope IN (design §3.2 + §3.0.2):**
  - **Mint the `lazy` op under `lazyMu`, before `lazyEvalWaitCh`** (Invariant T;
    `dagql/cache.go:2957`, publish `:2962`, unlock `:2966`), with the two existence
    cases made explicit: **reuse** the existing resume span as the `lazy` op
    (producer-context case) vs. mint a **new hidden `ui.passthrough`** `lazy` op
    (no-producer-context case). Stash its `SpanContext` next to `lazyEvalProfOpID`.
  - **Keep `resumedCallbackSpan` unchanged** (`dagql/cache.go:2990`) — deferred
    work keeps `parentId = producer`; **the UI-visible tree does not change.**
  - The **stamping span processor** (§3.0.2): registered in the per-client
    `tracerOpts` (`engine/server/session.go:684-715`), **before** the
    `LiveSpanProcessor`; in `OnStart`, stamp `wcprof.parent = lazyOpSpanID` **iff**
    the ctx carries the `{lazyOpSpanID, producerSpanID}` override **and**
    `span.Parent().SpanID() == producerSpanID` (direct re-pointed children only;
    descendants fall through). Set that override ctx on `callbackCtx`.
  - **Joiner wait links** (reason `lazy`) to the stashed `lazy` op
    (`dagql/cache.go:2935-2943`).
- **Scope OUT:** exec/services (Chunk 4).
- **Engine touch-points:** design §3.2 (`dagql/cache.go:2885-3014` `evaluateOne`,
  `:2797-2809` `resumedCallbackSpan`, `:436-459` span-ctx capture); §3.0.2
  (`engine/server/session.go:684-715` processor registration).
- **Gating validation = DoD:**
  - **§6.5 lazy re-point fidelity fixture** — all five assertions: (1) UI parentage
    unchanged (`parentId == producer`, golden-trace diff); (2) causal re-home —
    `wcprof.parent` on **direct** children only (a descendant exec carries none);
    (3) no double-count (Σ self ≤ makespan; producer self excludes the work; `lazy`
    self ≈ 0); (4) consumer critical path includes the eval (scaling the work's
    real class shortens the consumer; scaling a generic "lazy" class does not); (5)
    Invariant T (a joiner arriving before the resume span would have existed still
    gets a valid target).
  - **§6.2 oracle** now converges on **lazy-heavy Directory/File/Container
    pipelines**.
  - **§6.1** green — the lazy cycle risk (design §2.5) is closed (`CycleWarnings
    == 0`).
  - **+ Codex converged** (holistic: do singleflight + lazy compose cleanly?).
- **Dependencies:** Chunk 1 (loader's `wcprof.parent ?? parentId`; vocabulary) and
  Chunk 2 (lazy work's sub-calls go through the singleflight path; their fidelity
  needs `call_exec`). So validate lazy *after* Chunk 2.
- **Cumulative state after Chunk 3:** lazy + singleflight both faithful; the oracle
  converges on lazy-heavy **and** singleflight-heavy workloads. Still missing: the
  engine-vs-user split inside execs (a leaf exec's self-time is one lump), and a
  distinct blocking op for service starts.

### Chunk 4 — Exec engine/user split + Services

- **Coherent unit:** the two remaining choke points (design §3.3, §3.4) — both
  additive, both Invariant-T / Invariant-E-safe. They are independent subsystems
  (executor vs services) bundled as "finish the choke points"; **split into 4a/4b
  if it reviews heavy** — there is no dependency between them.
- **Scope IN:**
  - **Exec split (design §3.3):** an `exec.run` span (`wcprof.op.kind=exec`) as a
    child of the **`call_exec` span** (mirroring native `engine/engineutil/executor.go:122`),
    with `containerStart` (engine) and `processRun` (user) children split at the
    started-callback (`engine/engineutil/executor_spec.go:1407-1412`),
    `wcprof.work_type=user` on `processRun` **only**. Nested-client work stays a
    sibling under the withExec execution via the existing `causeCtx`
    (`core/container_exec.go:1304`) — fine for the counterfactual.
  - **Services (design §3.4):** a `service.start` span (`core/services.go:974`)
    minted **before** `ss.starting[key]` is published (Invariant T,
    `core/services.go:985`); installer **wait links** (reason `service`) from
    `core/services.go:955`; the long-lived availability span left
    non-self-time-bearing (`ui.passthrough`).
- **Scope OUT (post-v1):** finer exec phases (setupNetwork / prepareMounts /
  applyOutputs); leaf-I/O tagging. See §5 below.
- **Engine touch-points:** design §3.3 (`engine/engineutil/executor.go:122,188-203`,
  `executor_spec.go:1407-1412`); §3.4 (`core/services.go:955,974,985`).
- **Gating validation = DoD:**
  - **§6.5 withExec delayed-setup-vs-runtime fixture:** engine overhead →
    `containerStart`, user time → `processRun`; the headline correctly fingers
    whichever is slow. **North-star check:** a slow `go build` headlines as
    user/`processRun` (user work first-class).
  - **Services fixture:** a service-dependent workload — installer waits credit
    `service.start`; the idle daemon does **not** rank.
  - **§6.2 oracle** converges on exec-heavy and service-using workloads.
  - **§6.1** green. **+ Codex converged.**
- **Dependencies:** **Chunk 2** (exec.run is a child of `call_exec`); Chunk 1
  (wait vocabulary, loader). Independent of Chunk 3.
- **Cumulative state after Chunk 4:** **all four breaks fixed; every choke point
  faithful.** The oracle converges per-choke-point across singleflight / lazy /
  exec / services workloads, and **user work is first-class**. What remains is
  productionization: the Cloud ingest path and a standing whole-workload gate.

### Chunk 5 — Productionization: Cloud ingest swap + standing drift gate

- **Coherent unit:** move from offline fixtures to the production source, and lock
  in a standing regression gate. After it, v1 is complete and self-defending.
- **Scope IN (design §7 steps 7–8):**
  - **Swap the loader front-end** from otlpdump-JSONL to the **Dagger Cloud trace
    API** (`internal/cloud/trace.go`); the compile/`Build` stage is unchanged
    (design §5). Cloud creds per §1 above (read, never print).
  - **§6.6 Cloud round-trip test:** emit a known augmented trace → real Cloud
    ingest → fetch back → compile; assert every `purpose=wait` link survived with
    `wcprof.wait.*_unix_ns` **bit-exact** (proves the string encoding dodged
    float64), the compiled graph is byte-identical (modulo ordering) to the
    otlpdump compile of the same run, and §6.1 holds — **sized to exceed realistic
    fan-in** (a thousands-scale suppressed-sibling span, validating the 16384 cap
    against Cloud's own truncation).
  - **§6.4 standing drift gate** in CI on a *representative complex* workload
    (engine-dev build or a module pipeline mixing services + lazy dirs + nested
    clients): `simulated baseline drift vs actual` within band + §6.1 invariants.
- **Scope OUT (post-v1, §5 below):** all reserve seams.
- **Engine touch-points:** design §5 (front-end swap), §6.6, §6.4;
  `internal/cloud/trace.go`.
- **Gating validation = DoD:** §6.6 green; §6.4 standing gate green — **now on the
  full complex workload, since all choke points are faithful** (this is the first
  point the complex-workload oracle is *expected* to converge end-to-end); §6.1 +
  §6.2 still green. North-star check: a real Cloud CI trace → a faithful "why was
  my CI slow?" report with user work first-class. **+ Codex converged.**
- **Dependencies:** **all** prior chunks (round-trip + drift gate exercise the
  complete emit).
- **Cumulative state after Chunk 5 — v1 done:** any Dagger Cloud trace (local or
  CI) compiles to the wcprof model and yields a faithful bottleneck ranking from
  the unchanged analyzer/replay, validated by the oracle + Cloud round-trip +
  standing drift gate.

---

## 4. Gaps / ordering issues surfaced by chunking (flagged for review)

Chunking forced these to the surface; better found now than mid-build:

1. **`exec.run` ⟶ `call_exec` is a hard cross-chunk dependency.** Design §3.3
   parents `exec.run` under the `call_exec` span, so **Chunk 4 cannot precede
   Chunk 2.** The DAG (§2) makes this explicit; a naive "do the executor work
   early" would break it. *Not a design gap — a sequencing constraint to honor.*
2. **The complex-workload oracle only fully converges at Chunk 5.** Per-chunk
   oracle runs must use **choke-point-isolating** workloads (singleflight-only,
   lazy-only, etc.), because a *mixed* workload will drift on whatever choke point
   isn't built yet. Concretely: a lazy pipeline that triggers execs will show
   residual exec-split drift until Chunk 4 — so **Chunk 3's oracle gate should use
   lazy-dominated workloads** (lazy Directory ops without heavy execs), and the
   *complex* §6.4 gate is deferred to Chunk 5 by design. Reviewers should not read
   "complex workload still drifts at Chunk 3" as a regression. *This is an honest
   consequence of additive emit; flagging so the holistic review calibrates
   expectations per chunk.*
3. **Stamping-processor placement (Chunk 3, not Chunk 1).** Design §3.0.2 presents
   the processor as a prerequisite for §3.2. I place the *convention* (the
   `wcprof.parent` key + the loader's `?? parentId` rule) in Chunk 1, but the
   *processor* (the engine mechanism that sets it) in Chunk 3 with the lazy emit
   that drives it — reviewing inert machinery in the foundation is worse than
   reviewing it next to its sole consumer. The "prerequisite" becomes intra-chunk
   ordering (processor before the lazy ctx wiring, within Chunk 3). *A boundary
   choice, justified; raise it if you'd rather have the processor land inert in
   Chunk 1.*
4. **Lazy ⊥ Exec ordering is a preference, not a constraint.** Either could come
   first off Chunk 2 (§2 DAG). The roadmap keeps design §7's lazy-then-exec order
   (riskiest mechanism — the override + processor + Invariant T — validated
   earlier; the satisfying "user work first-class" milestone as the faithfulness
   capstone). If the implementer prefers cleaner mixed-workload oracle stories
   sooner, exec-before-lazy is defensible (lazy-triggered execs would already be
   split). *Flagging the trade so the choice is deliberate, not accidental.*
5. **Services is under-constrained in the DAG.** It depends only on Chunk 1's wait
   vocabulary, so it *could* land as early as Chunk 2's neighborhood. It is bundled
   into Chunk 4 for the "finish the choke points" grouping; pull it earlier (or
   into its own chunk) if Chunk 4 reviews heavy.
6. **Oracle requires both sources live on one run (process detail, not a doc
   gap).** The native recorder (PR #13393) and the OTel augmentation are
   independent; the runbook (§1 above) runs both on one workload and compares. No code
   conflict, but the *same-run* requirement is load-bearing for comparability and
   is easy to get wrong (e.g. comparing a native dump and an OTel trace from two
   different invocations). Called out so the implementer wires the harness to
   capture both from a single run.
7. **Two validation-plumbing items the roadmap review caught, now folded (not open
   gaps).** (a) The "zero dropped links" gate was unobservable from current
   `hack/otlpdump` JSONL (it omits OTLP dropped-counts) → the otlpdump extension is
   now in **Chunk 1** scope/touch-points, a prerequisite for Chunk 2's cap-stress.
   (b) The design's §6.5 persisted-cache drift fixture had no scheduled home → now
   in **Chunk 2's DoD** (fallback: before v1). Both were validation/plumbing
   precision, not design changes; recorded here for traceability.

None of these change the approved design; they are sequencing / review-calibration
notes. If implementation surfaces a genuine design gap (per the §0 fidelity
protocol), fold it back into `wcprof-otel-design.md` and record why.

---

## 5. Post-v1 reserve seams (explicitly OUT — do not pull into v1)

All are already marked as seams in the design (§9 / §3.x); listed here so the
implementer does not scope-creep into them:

- **Wait-link fan-in merge** (design §9, qualified reserve-only): emit-side merge
  of same-waiter/same-target waits. Held in reserve *only if* the 16384 cap is
  approached on real traces; needs cross-goroutine aggregation, so not free.
- **`dagql.publishResult` as a real wait target** (design §3.1, §9): only if
  publication proves hot — and then fixed in **both** native and OTel.
- **Persisted-import decode singleflight** wait edges (design §4.1, §9): only if
  the §6.5 persisted-cache fixture shows non-trivial native↔OTel ranking drift;
  fix native first.
- **Finer exec phases** (setupNetwork / prepareMounts / applyOutputs — design
  §3.3): additive follow-up when dead-air shows they matter.
- **Leaf-I/O instrumentation** (git / pull / filesync → `OpKindIO` + `external` —
  design §3.5).
- **Multi-trace / whole-CI-job aggregation** (design §10 decision 2): a separate
  product+ingest effort *above* this loader — never inside it (it would require
  cross-trace inference, forbidden by design §3).

---

## 6. At-a-glance

| Chunk | Unit | Brings online | Oracle converges on | Depends on |
|------|------|---------------|---------------------|-----------|
| 1 | Foundation: loader + vocabulary + cap + otlpdump dropped-counts + §6.1 gate | §6.1 structural gate (no-service baseline) | — (no augmented data) | — |
| 2 | Singleflight: `call_exec` + wait links + `publishResult` | **§6.2 oracle** | singleflight-heavy (+ persisted-cache drift fixture) | 1 |
| 3 | Lazy + stamping processor | §6.5 lazy fidelity | + lazy-heavy (Dir/File/Container) | 1, 2 |
| 4 | Exec split + Services | user-work-first-class headline | + exec-heavy, service-using | 1, **2** |
| 5 | Cloud ingest + standing drift gate | §6.6 round-trip, §6.4 gate | full complex workload | all |

Design §7 maps to these as: step 1→Chunk 1; step 2→Chunk 1 (vocabulary) + Chunks
2–4 (the actual emission); step 3→Chunk 2; step 4→Chunk 3; steps 5–6→Chunk 4;
steps 7–8→Chunk 5. The "1–3 prove the thesis / 4–6 complete faithfulness / 7–8
productionize" framing becomes "Chunks 1–2 prove the thesis / 3–4 complete
faithfulness / 5 productionizes."
