# Workstream summary: Profiler Multi-Source (OTel) — ws-31155a5903a038dc03b30238aaaec5e9

Recon report by profiler-summarizer (cl-dda50005b609f361f447ed7b69d8120f), 2026-07-06.
All claims verified against git objects, on-disk artifacts, and agent transcripts on this
machine; verification method noted inline where it matters.

---

## 0. TL;DR for cache-chief

- **Everything the downstream workstreams need code-wise is pushed and they have it.**
  The single branch `wcprof-exec-decomp-impl-dea4c5e3` @ `db1caae0db` on `origin`
  (git@github.com:sipsma/dagger.git — Erik's fork) contains the ENTIRE workstream output:
  25 commits on top of `upstream/main` `bc39a70141`. Verified live via `git ls-remote`;
  local tip == origin tip. All four downstream worktrees on this machine
  (cache-perf-analysis, cache-invalidation-tracing, exec-time-progress-ux,
  whatif-cached-sim) sit exactly at `db1caae0db`.
- **What downstream does NOT have: the design corpus.** `hack/designs/wcprof-otel-design.md`
  (1353 lines, the contract of record), `wcprof-otel-impl-plan.md` (501 lines), and ~100
  review/handoff docs are **untracked files in agent worktrees, committed nowhere**.
  Downstream worktrees were given only an untracked copy of
  `wcprof-exec-decomp-design.md` (805 lines). If a dagger1 agent needs the main design
  doc, it must be copied over explicitly (canonical copy:
  `wcprof-otel-fresh-design-v2-c29dd793-dbba5bb7/hack/designs/wcprof-otel-design.md`,
  md5 `301d02fd…`, identical in 10 worktrees).
- **Nothing is upstream yet** beyond native-wcprof v1 (PR #13393, already in
  `upstream/main`). The 25 commits are fork-only, PR-ready (authored Erik + Signed-off-by,
  zero AI attribution), no PR opened — Erik calls the push/PR.
- Unit tests on the pushed branch re-verified green by me today
  (`go test ./engine/wcprof/... ./engine/engineutil` — all ok).

---

## 1. What wcprof IS

`wcprof` is a wall-clock profiler for the Dagger engine. It answers "why was this run
slow?" — not "what used the most time" but "what actually gated the makespan". It records
causal events during a run, compiles them into an IR (op graph + wait edges), and runs a
counterfactual discrete-event **replay**: re-simulate the recorded schedule under "class X
self-time × f" hypotheses (f ∈ {0, 0.5, 0.9}) and rank classes by how much end-to-end
wall-clock each would really save. This accounts for critical-path shifts, singleflight
dedup, and dependency chains — a long op off the critical path ranks low.

### The IR / data model

Three event types (see `engine/wcprof/README.md` on the branch — it is thorough and current):

- **ops** — timed intervals with a *kind* (`call`, `call_exec`, `lazy`, `exec`,
  `exec_phase`, `service_start`, `session_phase`, …), a *class* (e.g.
  `Container.withExec`, `exec_phase:go build`), an instance ident (recipe digest / exec
  ID), a structural parent (from context), an outcome (`hit`/`executed`/`joined`/…), and
  (since exec-decomp) an `Argv`.
- **waits** — exact blocked-on intervals: who waited, on which op or named resource, why,
  from when to when. Recorded **at the choke points themselves** (dagql singleflight,
  lazy-eval waiters, service starts, cache-volume locks, exec completion) — never
  inferred from span nesting.
- **links** — non-blocking correlations, chiefly "exec op X hosts nested client Y" so
  module calls back into the API stitch under their hosting exec.

### The two sources

**Native source** (v1, merged upstream as PR #13393 — `engine/wcprof` exists at the
branch base `bc39a70141`): fixed-size structs appended to sharded in-memory buffers via
cheap hooks in `dagql/cache.go`, `engine/engineutil/executor.go`, `core/container_exec.go`,
`core/services.go`, `engine/server/session.go`. Enable per-session with the hidden
`dagger --profile <x>` flag, or engine-global with `--wcprof` / `_DAGGER_WCPROF=1` /
`POST /debug/wcprof/enabled`. Dump: `GET :6060/debug/wcprof/dump`.

**OTel source** (this workstream): the engine's ordinary telemetry (the same spans that
flow to Dagger Cloud) is *augmented at the emit side* so that a **zero-inference loader**
can compile it into the SAME IR, analyzed by the SAME, unchanged replay. Key mechanics:

- A real `call_exec` span per actual resolver execution (`dagql/otelprof_hooks.go`,
  minted in `getOrInitCall` before the waiter-observable primitive is published —
  "Invariant T" — so wait targets always exist).
- **Wait edges as OTel span links** with `link.purpose="wait"` and the wait/exec windows
  carried in link attributes as **decimal-string nanoseconds** (survives Cloud's
  `map[string]any` JSON round-trip bit-exact — the "float64 dodge", proven by the §6.6
  round-trip test).
- **`wcprof.parent`** attribute as an explicit causal-parent override for lazy-evaluated
  work (a stamping span processor prepended to each client's provider chain), so dagui's
  visible tree never changes (owner decision §10.1) while the analyzer reads the truth.
  Loader parent rule: `wcprof.parent ?? parentId`, nothing else.
- Engine/user **exec split**: `exec.containerStart` (engine overhead) vs
  `exec.processRun` (user work), so "a slow `go build` is a valid headline".
- **Structural gate** (`wcotel/gate.go`, design §6.1) runs before any analysis and
  hard-fails on: cycles, unresolved wait targets, orphaned parents, self-time > makespan,
  unschedulable ops, dropped links, and **incompleteness** (below). The analyzer refuses
  rather than compensates.
- **Completeness checksum**: the engine counts every span it exports per trace
  (`engine/server/wcprofcount.go`) and, at session teardown, declares the EXACT final
  count on a trailing `wcprof.session_complete` carrier span (excluded from the graph),
  force-flushed under `context.WithoutCancel`. Loader fails `received < declared`
  (dropped spans) and `received > declared` (declaration wasn't final) — fail-by-default
  when the marker is absent.
- **Emit-volume discipline**: the OTel emit path honors the engine's
  introspection/reflection telemetry suppression via a static per-call receiver-type
  `ProfileSkip` predicate (`dagql/result_call_frame.go`) — OTel-only; native records full
  detail (Erik's ruling). Plus `NewLargeQueueLiveSpanProcessor`
  (`engine/telemetry/livespan.go`): 256Ki-slot / 16Ki-batch bounded BSP queues on every
  Cloud span hop (BSP default 2048 silently drops on burst; never block, and if anything
  still overflows the completeness gate catches it).

### The cross-source oracle

`cmd/wcprof-oracle -native <dump> -otel <otlpdump.jsonl>`: run ONE dev-engine run with
BOTH sources active, compile both to the IR, run `RunWhatIfs` on both, compare top-N
bottleneck classes (jaccard ≥ 0.8, per-class savings drift ≤ 0.1 by default). Native is
ground truth. `NativeOnly`/`OTelOnly` residual classes are surfaced, not hidden
(`wcotel/oracle.go`). The runbook is in the file-header comment of
`cmd/wcprof-oracle/main.go`. Critical methodology caveat (design §6.2): the two sources
have different vantage points (native `_DAGGER_WCPROF` = engine-global; one Cloud trace =
client-session scope + client-infra spans) — scope-match with per-session `--profile` and
class-filtering, or the comparison is apples-to-oranges (a raw jaccard of 0.23–0.30 was
observed that is NOT unfaithfulness; matched-scope deterministic oracle is jaccard=1.00 /
drift=0.00, and on the live exec-decomp run every user-work class matched with drift 0.00).

### Analyses available today

1. **What-if bottleneck ranking** (the headline) — native, local OTel capture, or Cloud trace.
2. **Per-class self-time tables** + outcome counts + duplicate-execution detection.
3. **End-of-workload blocking chain** and **dead air** (uninstrumented gaps).
4. **Exec decomposition**: per-command classes from real emitted argv
   (`exec_phase:go build`, `exec_phase:tar`, …; argv captured in core as
   `execMD.ProfArgs` before the QEMU and `/.init` shims, scrubbed + bounded, carried as a
   scalar JSON-array string attr on both sources) + **offline re-grouping** via repeatable
   `--exec-group '<match>=<label>'` rules (also `contains:` matching) — regroup a captured
   trace without re-running anything.
5. **Simulated-baseline drift** vs actual makespan as a standing sanity check, plus a
   standing drift-gate test on a representative fixture (`wcotel/drift_gate_test.go`).

### Where the code lives (all on the pushed branch)

| Path | What |
|---|---|
| `engine/wcprof/{wcprof.go,record.go,dump.go}` | native recorder + dump format |
| `engine/wcprof/wcanalyze/{graph.go,replay.go,report.go,classify.go}` | IR graph, counterfactual replay, reporting, exec classification |
| `engine/wcprof/wcotel/{loader.go,gate.go,oracle.go}` | OTel loader (zero inference), structural gate, cross-source oracle |
| `engine/wcprof/wccloud/cloud.go` | Dagger Cloud front-end (pure field-map of `cloud.SpanData`; `spansUpdated(root:true, listen:nil)` full store dump) |
| `cmd/wcprof-analyze`, `cmd/wcprof-otel-analyze`, `cmd/wcprof-oracle` | CLIs |
| `engine/telemetryattrs/attrs.go` | the wcprof OTel attribute vocabulary |
| `dagql/cache.go`, `dagql/otelprof_hooks.go`, `dagql/result_call_frame.go` | call/call_exec/publishResult emit, wait links, ProfileSkip |
| `core/container_exec.go`, `core/services.go`, `engine/engineutil/executor*` | ProfArgs capture, service.start, exec phases |
| `engine/server/{session.go,wcprofcount.go}`, `engine/telemetry/livespan.go` | completeness counting/carrier, LinkCountLimit=16384, large BSP queues |
| `hack/otlpdump/` | local OTLP capture tool (+dropped-count reporting) |

### How to run end-to-end on a real trace

```bash
# dev engine
./hack/dev                        # build + start dagger-engine.dev (debug port 6060)

# (a) native
./hack/with-dev ./bin/dagger --profile x call engine-dev container sync
curl -s localhost:6060/debug/wcprof/dump > /tmp/native.dump
go run ./cmd/wcprof-analyze /tmp/native.dump

# (b) OTel, local capture
go run ./hack/otlpdump -out /tmp/otel.jsonl &
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:43180 OTEL_EXPORTER_OTLP_TRACES_LIVE=1 \
  ./hack/with-dev ./bin/dagger call engine-dev container sync
go run ./cmd/wcprof-otel-analyze /tmp/otel.jsonl

# (c) OTel, straight from Dagger Cloud (requires `dagger login`; creds ~/.config/dagger/)
go run ./cmd/wcprof-otel-analyze -trace <traceID> -top 40 [-org <orgID>] [--exec-group 'go build=builds']

# (d) cross-source oracle (both sources, SAME run)
go run ./cmd/wcprof-oracle -native /tmp/native.dump -otel /tmp/otel.jsonl
```
The gate runs first in (b)/(c) and exits non-zero on any hard invariant, including an
uncertified (marker-absent / incomplete) trace.

---

## 2. Current state (branches, SHAs, pushed vs unpushed)

**Pushed to origin (verified live via `git ls-remote origin`):**
- `wcprof-exec-decomp-impl-dea4c5e3` @ **`db1caae0db`** — THE branch. 25 commits on
  `upstream/main` `bc39a70141` (Merge PR #13548). Local branch == origin exactly. Pushed
  2026-07-01 by the exec-decomp implementer on Erik's explicit one-off authorization,
  precisely so dagger1 could fork it.
- `wcprof` @ `0a31956d38` — the old v1 branch (native wcprof), superseded by merged
  upstream PR #13393. Historical only.

**The 25-commit chain on the pushed branch** (`git log bc39a70141..db1caae0db`), oldest first:

```
b5d218b1e1 feat: OTel profiling source foundation (Chunk 1)
696b5310c3 fix: harden OTel structural gate against wait-edge loss (Chunk 1 review)
ab0588e567 feat: OTel singleflight call_exec + wait edges (Chunk 2)
061518b3d8 fix: make missing OTel wait target gate-observable + emit-path test (Chunk 2 review)
8355c7efda feat: OTel lazy re-point faithfulness + wcprof.parent stamping (Chunk 3)
7f97d6a383 fix: reset lazy wait target per attempt + nested-override test (Chunk 3 review)
4e7ddf0037 feat: OTel exec engine/user split + service start (Chunk 4)
b24f29ecde test: committed lazy-triggered-exec composition test (Chunk 4 review)
40b50af58c fix: order-independent prefix-anchor replay (kill false cycles)
416aec5755 fix: keep fixed delays start-ordered; harden cycle-fix per final review
3206c5439b fix: model fixed delays as concurrent non-scalable segments
4bf7e4b521 fix: zero-dur wait-target wrong answer; hard-fail fallback anchors
d2287dacde fix: rational root model — remove chaining inference + fallbacks
3ed0e62af6 feat: gate signal for ops with an absent recorded parent
19d47ecaac fix: skip the reflection/introspection class at the OTel emit
ee560460b9 feat: finish the OTel producer — service.start wait symmetry + gate cleanup
e8b013cf8e feat: Chunk 5 — Dagger Cloud ingest front-end + standing drift gate
ca4359b6cd feat: span-count completeness checksum for the OTel source
1be8d5c903 fix: exact final span count at teardown (close tail-drop holes)
88a902ffb8 fix: fail loud on received > declared; refresh teardown comments
0688b8b7af fix(telemetry): large bounded BSP queues on the Cloud span hops
af1bfc1062 feat: per-command exec classes from emitted argv (native + OTel)   [exec-decomp S1]
6cbca44c99 feat: offline --exec-group rule to re-group execs without re-emit  [exec-decomp S2]
7e690fad9b fix: stamp completeness marker on the service-start emit test
db1caae0db fix: force-flush the completeness carrier under an uncanceled ctx
```

All 25 authored `Erik Sipsma <erik@sipsma.dev>` + `Signed-off-by`, zero AI attribution
(audited during the rebase; spot-checked by me).

**Local-only (unpushed) branches — all superseded lineage, NOT needed by downstream:**
- `wcprof-otel-skip-coder-daa3a9d2` @ `c28d55ae7a` — the pre-rebase lineage of the same
  21 OTel commits (old SHAs `e689e9b007…c28d55ae7a`; memory notes cite `9555281f27` =
  pre-rebase Chunk 5). I diffed it against the pushed branch on all wcprof paths: the
  delta is EXACTLY the exec-decomp feature (+982 lines) — the pushed branch is a strict
  content superset.
- Chunk implementer branches (`wcprof-otel-implementer-7a7ee34b`, `-chunk2-196d5660`,
  `-chunk3-d0ddc7e0`, `-chunk4-7ad02bcf`), `profiler-otel-feasibility-67420ff1` (the
  superseded 12-commit prototype), `backup-pre-rebase-1782801021` (rebase recovery point).

**What dagger1's fork base contains vs what it's missing:**
- Contains: ALL code, ALL tests (chunk1–5 fixtures, drift gate, completeness, exec-decomp,
  oracle harness), `engine/wcprof/README.md`.
- Missing: the entire design/review corpus (untracked; see §0). Also note the code
  comments reference design §-numbers (e.g. `cmd/wcprof-oracle/main.go`,
  `core/services.go`, `cmd/wcprof-otel-analyze/main.go`) that resolve only against the
  uncommitted `hack/designs/wcprof-otel-design.md` — a known, deliberately deferred
  cleanup (see §5).

**Sign-off status (each verified in the written review doc named):**
- Chunks 1–4 + cycle-fix: converged over multiple review rounds (docs in the reviewer
  worktrees; the chunk-4 saga alone has ~8 rounds of docs).
- Introspection-skip fix: SIGN OFF (`wcprof-otel-skip-code-review2-codex-fresh.md`).
- Producer completion (service.start symmetry): SIGN OFF (`wcprof-otel-round1-review-codex-fresh.md`).
- Chunk 5 Cloud front-end: SIGN OFF (`wcprof-otel-chunk5-review-codex-fresh.md`) — with
  the explicit caveat that motivated the completeness checksum.
- Completeness checksum: round 1 **BLOCKED** (`wcprof-otel-completeness-review-codex-fresh.md`
  — surplus-masking false-pass), teardown-carrier redesign then SIGN OFF
  (`wcprof-otel-teardown-review-codex-fresh.md`).
- exec-decomp: design converged in 3 council rounds; implementation "ready to merge"
  **unanimously** from 4 written reviews (codex-fresh, designer, chunk4-impl, skip-coder)
  + live-validated (below).

---

## 3. History — how it got here

**Phase 0 — feasibility prototype** (branch `profiler-otel-feasibility-67420ff1`,
12 commits): first OTel source. It worked by loader-side *synthesis* — synthesizing
`call_exec` join targets, re-parenting, and "breaking" residual cycles.

**Phase 1 — the rescue** (agents `profiler-rescue-claude` cl-afecac68… +
`profiler-rescue-codex`, two independent investigations that converged;
`wcprof-otel-findings.md` + `wcprof-otel-rewrite-plan.md` in the rescue worktree): the
prototype's symptoms were "one design-level mismatch, not scattered bugs" — OTel span
nesting is NOT the synchronous parent→child the replay assumes, and the prototype's
loader-side synthesize-and-reparent **is causal inference**, with cycle-breaking as
**masking**. Erik ruled both disqualifying. Mandate: in-place hard cut — keep the
trustworthy foundation (native model + replay), fix emission at the engine choke points,
loader stays trivial. The rescue-claude agent then became the standing **lead** of the
whole workstream.

**Phase 2 — fresh design** (`wcprof-otel-fresh-design` v1 then v2 agents):
`wcprof-otel-design.md` — the four faithfulness breaks grounded in code (singleflight
joiners, suppression-deleted spans, emitter≠executor mis-parenting, lazy re-pointing),
choke-point-by-choke-point emit fixes, the wait-edge wire format, the zero-inference
loader, and a first-class validation plan (§6: structural gate, cross-source oracle,
known-answer injection, standing drift gate, adversarial fixtures, Cloud round-trip).
Passed three external design-review passes (docs `wcprof-otel-design-review{,-2,-3}.md`)
plus an impl-plan review. Two owner decisions locked (§10): no dagui/UI change
(`wcprof.parent` override instead), and one Cloud trace = the unit of analysis (no
cross-trace inference, ever).

**Phase 3 — the chunk build** (`wcprof-otel-impl-plan.md`; fresh implementer agent per
chunk, stacked branches; every chunk got a multi-agent review round then a convergence
commit):
- **Chunk 1** foundation: loader + vocabulary + gate + `LinkCountLimit=16384` + otlpdump
  dropped-counts + CLI.
- **Chunk 2** the central singleflight fix: shared `call_exec` + wait links +
  `publishResult`. Deterministic oracle jaccard=1.00.
- **Chunk 3** lazy re-pointing + the `wcprof.parent` stamping processor. Empirical oracle
  looked bad (jaccard≈0.23) → diagnosed as **scope mismatch** (engine-global native vs
  client-session trace), NOT unfaithfulness → §6.2 scope-match methodology written into
  the design doc.
- **Chunk 4** exec engine/user split + services — then the **false-cycle crisis**: the
  replay produced cycles on real traces. Extended forensic + first-principles review
  saga (the `chunk4-cycle-FUNDAMENTAL` / `firstprinciples` / `reconfirm` / `final` docs)
  concluded the *replay itself* had order-dependence and inference remnants. Fixes:
  order-independent prefix-anchor replay, fixed delays as concurrent non-scalable
  segments, zero-duration wait-target fix, and the **rational root model** — delete root
  chaining inference + recorded-offset fallbacks entirely; anchor roots independently;
  absent recorded parents become a gate signal, not a fallback. This is the
  trust-the-data principle applied to the model's own internals.
- **publishResult investigation**: ~330 "orphan" publishResult roots in local captures
  looked like an emit gap. Investigation + later forensics proved the emit is faithful
  1:1 (parentId always set; `call_exec` created under lock before publication); the
  *capture* was lossy. A proposed `wcprof.parent`-stamping workaround was REJECTED as
  compensation; the gate already refuses such traces.
- **Volume-regression forensics** (fresh "trust nothing" codex, tc-6646589…;
  `wcprof-otel-forensics-codex.md`): the OTel emit bypassed the engine's
  introspection-telemetry suppression → ~33k extra spans (≈16.6k `call_exec` + 16.6k
  `publishResult`) on a module-load workload that normally emits ~3.3k → the
  non-blocking 2048-slot BSP queues silently dropped spans as far up as the engine's
  per-client DB. Also proved the local capture wasn't "the wrong trace" and that
  `StreamSpans(root:true)` as then used wasn't a sufficient read path.
  → **Fixes**: (i) the static receiver-type `ProfileSkip` predicate at the OTel emit
  (design doc'd by the skip-implementer agent, coded by skip-coder; first version gated
  native too — review caught it; Erik ruled native stays un-gated); (ii) later, the
  large-queue BSP processors; (iii) the completeness checksum as the universal backstop.
- **Producer completion**: service.start wait symmetry; `FallbackAnchors` →
  `UnschedulableOps` hard-fail.
- **Chunk 5** productionization: `wccloud` Cloud ingest front-end (pure field map;
  `spansUpdated(root:true, listen:nil)` full dump), §6.6 round-trip test (string-ns
  encoding survives Cloud bit-exact), standing drift gate. Signed off with the explicit
  caveat that a structural gate cannot see a dropped *unreferenced* leaf → directly
  motivated:
- **Completeness checksum**: v1 (count + max-marker) **blocked in review** (surplus spans
  can mask a dropped counted leaf; final marker itself droppable) → redesigned as the
  exact-count teardown carrier (`wcprof.session_complete`, declared==received, marker
  fail-by-default) → signed off; `received > declared` made fail-loud per reviewer
  suggestion. Live-validated: induced real drops (leaf / 7-span subtree / the carrier
  itself) all caught; certified `declared==received` on real Cloud traces.
- **BSP queue fix**: cold engine build (~15k spans, live-double-emitted ≈30k records)
  overflowed default BSP queues including the carrier → `NewLargeQueueLiveSpanProcessor`
  on every Cloud hop; validated before/after (declared==received==15100 vs dropped carrier).
- **exec-decomp feature** (separate designer + implementer agents, 3-round design
  council, 2-stage impl): kills the `exec.processRun` blob. Design blockers found and
  fixed across rounds: the `/.init` shim mislabeling every exec; classify-before-gate +
  `progOnce` memo invalidation; Cloud-safe scalar JSON-array argv encoding; the QEMU
  second shim (→ capture resolved argv in core as `execMD.ProfArgs` before BOTH shims;
  cache-key-safe via `json:"-"` — necessary because `ExecutionMetadata` IS serialized
  into exec cache keys, safe because core→executor hand-off is an in-process pointer).
  Implementation reviewed unanimously ready-to-merge, then live-validated (§below), then
  the whole 25-commit chain was **rebased onto `bc39a70141`** (2 semantic conflicts in
  `engine/server/session.go` vs upstream's session-lifecycle refactor `0e21927e46`,
  resolved keep-both; all tests green including `engine/server`) and **pushed**.

**Live validation results** (from the implementer transcript, trace IDs cited):
- Synthetic 6-exec workload, Cloud trace `35bbc66ac8f5cc5816ce28d12cc66857`: gate PASS
  complete 62/62; per-command classes with `sleep 5` correctly the headline; **native ==
  OTel with drift 0.00 on every user command**; `--exec-group` regrouping exact; the
  JSON-array argv survived real Cloud ingest. Honest caveat: overall oracle jaccard=0.30
  (< the 0.8 bar) — every divergent class is engine/client/buildkit-internal
  (vantage-point residual, §5.1), zero user-work divergence.
- Cold real engine build (`dagger call engine-dev container sync`, ~148s, 450 execs,
  ~30k spans): first run `09d54b8f…` **gate-REFUSED** (marker absent — the completeness
  gate correctly catching the teardown-flush race, surfaced as a finding, not papered
  over) → root-caused (carrier stamped after `beginClosing()` cancels the ctx; flush
  raced shutdown) → force-flush fix → `de069fc7…` PASS (15098==15098) → final certified
  run `9a1e298d79341bc88fd9ff2386c05933` PASS (declared==received==15101, marker=true):
  ranking `exec_phase:runtime` 33×/335s > `go build` 16×/128s > `codegen
  generate-typedefs` 19×/57s > `tar` > `apk add`…, critical-path what-if headline =
  `codegen generate-typedefs`. This is the north-star demo: "why is my engine build
  slow?" answered per-command from a real, certified Dagger Cloud trace.

---

## 4. Principles & hard-won lessons

1. **Analysis is a rational function of FAITHFUL data** (the governing principle, stamped
   as SETTLED into every agent brief). The model NEVER compensates: no inference, no
   fallbacks, no chaining, no cycle-breaking. An odd analysis result means the DATA
   (emit/capture) is wrong — fix the emitter, never bend the model. Enforced repeatedly
   and expensively: the entire feasibility prototype was discarded over loader-side
   synthesis; the chunk-4 rework deleted the replay's own root-chaining inference and
   fallback anchors (`d2287dacde`); a `wcprof.parent`-stamping workaround for the
   publishResult artifact was rejected; the gate REFUSES incomplete traces rather than
   the analyzer tolerating them.
2. **Debug data vs model separately** — the false-cycle saga and the publishResult
   artifact were both initially misattributed; separating "is the data faithful?" from
   "is the model rational?" is what cracked them.
3. **Fail loud, fail closed**: marker-absent ⇒ fail; `received>declared` ⇒ fail;
   unschedulable ops ⇒ hard-fail; a gate-refused trace is the system WORKING (the cold
   build FAIL was reported as a finding, and its fix made the gate stronger, not looser).
4. **Validation is first-class, and scope-match or the oracle lies**: the cross-source
   oracle is the centerpiece proof, but comparing engine-global native against one
   client-session Cloud trace produces false alarm (jaccard 0.23/0.30) — like-for-like
   scoping + class filtering is methodology, not drift.
5. **Fresh eyes on demand**: when the long-running team's assumptions got tangled
   (publishResult, volume regression), Erik's pattern was a brand-new agent with an
   explicit "trust nothing, verify everything, experiments not reasoning" brief. It
   worked both times.
6. **Mistakes made and corrected** (worth knowing so they aren't repeated): feasibility's
   loader synthesis; the replay's hidden order-dependence; completeness-v1's
   cardinality-only checksum (blocked in review); the first skip-fix gating native
   emission too; the implementer's withdrawn "HTTP export choking" claim (unproven —
   triggered the forensics reset); the completeness branch shipping a red test in a
   package it didn't run (`engine/engineutil`) — found and fixed by the next feature's
   implementer.

---

## 5. Open loose ends

1. **§6.2 oracle-methodology residual (the one open canonical-doc reconcile).** The
   canonical design doc's §6.2 has the Chunk-3-era scope-match/class-filter methodology,
   but NOT the post-skip-fix reconcile (native now records reflection/introspection
   classes the OTel source deliberately skips, whose time OTel folds into visible
   ancestors — the empirical oracle needs a fold-normalize / rank-compare treatment).
   `wcotel/oracle.go` surfaces `NativeOnly`/`OTelOnly` but implements no folding. The
   lead's last words on it: "parked, non-blocking — the engine-internal vantage-point
   divergence." Concretely visible as the live jaccard=0.30-with-zero-user-work-divergence
   result. Reconciling = update design §6.2 + (optionally) teach the oracle harness
   fold-normalization, or formally document it as an accepted residual.
2. **Deferred end-of-build comment cleanup (Erik-acknowledged).** Code comments across
   the branch cite design §-numbers, but the design doc is not committed anywhere. Either
   rewrite the comments self-contained (the recorded plan) or commit the design doc.
   Blocks nothing, but must happen before upstream PR.
3. **The design corpus has no durable home.** ~1353-line design doc + 501-line impl plan
   + 805-line exec-decomp design + ~100 review docs exist ONLY as untracked worktree
   files on this machine. Deleting worktrees loses them. Decide: commit under
   `hack/designs/`, or archive elsewhere.
4. **Upstream landing.** 25 commits sit on Erik's fork only; no PR. The branch is
   PR-ready by authorship convention, but upstream main moves (the session.go conflicts
   already happened once) — expect another rebase at PR time. Erik explicitly reserved
   the push/PR call.
5. **Design §9 reserve seams** (explicitly out of v1, waiting): leaf I/O instrumentation
   (git/pull/filesync), persisted-import decode singleflight, publishResult as a real
   wait target, wait-link fan-in merge (has non-obvious interval preconditions — the doc
   warns against naive collapse), multi-engine scale-out traces, cross-host clock-skew
   probe, LinkCountLimit headroom. **Directly relevant to cache-chief: §9's first seam is
   the "cache-diff sibling" — `dag.inputs` is the cache-key edge set, and the doc
   reserves a `wcprof.inputs.*` loader seam for exactly the cache-analysis work now
   running on dagger1, with the instruction "do not conflate with wait edges."**
6. **exec-decomp deferred edges**: `--exec-group` can't express a literal `=` in a
   pattern; non-`withExec` execs (Dockerfile `RUN` via frontend, service starts,
   `execMD==nil`) stay the aggregated blob by documented boundary, with a stated
   extension path (`ProfArgs` at their arg-resolution sites).
7. **Housekeeping**: `profiler-rescue-claude`'s worktree has uncommitted MODIFICATIONS to
   `wcanalyze/{graph.go,replay.go,replay_test.go}` (rescue-era experiments — should be
   deliberately discarded, not accidentally committed); stale local branches (skip-coder
   lineage, chunk branches, feasibility, backup-pre-rebase) can be pruned once nobody
   needs the pre-rebase SHAs; two untracked review docs sit in the skip-coder worktree.

"Reconciling everything at the end" = items 1–4 (+7 trivially): settle §6.2 in doc+harness,
de-§ the comments or commit the doc, give the corpus a home, rebase + upstream PR.

---

## 6. Working patterns (as practiced here)

The loop that built this, per chunk/feature:
1. **Design author** (Claude, own worktree) writes/updates the canonical design doc;
   copies are synced to every participant's worktree (verified: all 10 copies md5-identical).
2. **Fresh implementer** (Claude Fable, max effort, own worktree + branch stacked on the
   previous lineage) implements one chunk, self-classifies divergences from the design,
   hands off via an .md.
3. **Review round**: the standing fresh-eyes Codex reviewer + (earlier) a second Codex +
   prior-chunk implementers + the design author each independently write
   `hack/designs/wcprof-otel-<topic>-review-<role>.md` in their own worktrees; patches
   circulate as `.patch` files. The lead triages findings (accept/modify/reject — never
   wholesale), the implementer lands a convergence commit, reviewers re-verify.
4. **The lead** (`profiler-rescue-claude`) is Erik's single interface: spawns/directs
   everyone, verifies claims itself (re-runs tests, audits diffs/authorship), reports up.
   Erik's rulings (native un-gated; no UI change; one trace; hard cut) enter the docs as
   SETTLED and are never relitigated.
5. Commits: author Erik + Signed-off-by, no AI attribution; NO push without Erik's
   explicit per-instance authorization.
6. Testing doctrine: unit/fixture tests per package; live validation against the shared
   dev engine (`./hack/dev` / `./hack/with-dev`); integration tests via stable dagger CLI
   + the engine-dev-test workflow (skills/engine-debugging), never `./bin/dagger` for
   integration; Cloud validation via `dagger login` creds (never printed).

**Agents still addressable on this server** (all idle):
| Agent | ID | Value |
|---|---|---|
| profiler-rescue-claude (the LEAD) | `cl-afecac68dce5a43a7ae04acdac19411a` | full-history coordinator; Erik's interface; deepest context |
| wcprof-otel-skip-coder | `cl-1a1e3364e042efb307f1e6bb27fd7515` | implemented skip fix, chunk 5, completeness, BSP fix; reviewed exec-decomp |
| wcprof-otel-implementer-chunk4 | `cl-7225bc74989554be1224510615e180d5` | chunk 4 + the whole cycle-fix/replay rework |
| wcprof-exec-decomp-impl | `cl-a58186e6ba55515d62424b83d5f5dd5f` | exec-decomp, live certs, the rebase, the push |
| wcprof-exec-decomp-design | `cl-53471075fd49466ed09e7fa7b279ac6d` | exec-decomp design author |
| wcprof-otel-chunk-review-codex-fresh | `tc-66f3a75f45ae17304de5517fc36f7556` | standing Codex reviewer of every chunk |
| wcprof-otel-forensics-codex | `tc-664658907783abd793c48cfb58eeb488` | telemetry-pipeline forensics specialist |
| dagger-io-backend-checkout | `cl-d05f2cd2bef0e3740bdb04f5509438e6` | dagger.io repo checkout (Cloud-side) |

No longer listed/addressable (archived; artifacts persist in their worktrees): chunk 1–3
implementers, both fresh-design agents (incl. the OTel design author), both design-review
codices, the skip-implementer (skip design doc author), profiler-rescue-codex, feasibility.

---

## 7. Reading list for cache-chief

1. `wcprof-otel-fresh-design-v2-c29dd793-dbba5bb7/hack/designs/wcprof-otel-design.md` —
   THE contract (1353 lines); §1.1 the one assumption, §2 the four breaks, §3.0 wire
   format, §5 loader, §6 validation, §9 seams (incl. the cache-diff seam), §10 decisions.
2. Same dir, `wcprof-otel-impl-plan.md` — the chunk plan + process doctrine + post-v1 reserve.
3. `git log --stat bc39a70141..db1caae0db` in any worktree — the 25 commits; the messages
   are unusually good documentation (esp. `0688b8b7af`, `ca4359b6cd`, `d2287dacde`).
4. `engine/wcprof/README.md` (on the branch) — how to run everything native-side.
5. `cmd/wcprof-oracle/main.go` header — the oracle runbook.
6. `wcprof-exec-decomp-design-63001337-87e98616/hack/designs/wcprof-exec-decomp-design.md`
   — the exec-decomp contract (downstream already has copies).
7. `wcprof-otel-forensics-codex-e38258d1-9813e7b0/hack/designs/wcprof-otel-forensics-codex.md`
   — the telemetry-pipeline forensics (how spans actually flow engine→DB→CLI→Cloud; what
   drops where).
8. `profiler-rescue-claude-0b0fc9a0-630bf6b3/hack/designs/wcprof-otel-findings.md` +
   `wcprof-otel-rewrite-plan.md` — why the prototype died; the hard-cut mandate.
9. Transcript of `cl-afecac68…` (the lead) — the whole workstream's decision history in
   final-message form; transcript of `cl-a58186e6…` — the live validations, rebase, push.
10. For replay internals: the chunk4 cycle docs in
    `wcprof-otel-chunk-review-codex-fresh-e6d76076-f31d810a/hack/designs/` (esp.
    `wcprof-otel-chunk4-cycle-FUNDAMENTAL-*.md`).

---

## 8. Bullshit check

What I verified vs what I'm repeating:

- **Verified directly**: origin push state (`git ls-remote`, live); local==origin tips;
  the 25-commit chain and its base; pushed-branch content superset of the skip-coder
  lineage (path-scoped diff); downstream fork SHAs; design-doc copies' identity (md5) and
  non-committed status (`git ls-tree`); unit tests green on the pushed branch (ran them
  today); §-references in code comments; every "SIGN OFF"/"BLOCKER" quoted from the
  actual review file; the live trace IDs/gate outputs/rankings quoted from the
  implementer/lead transcripts.
- **Overstated in circulation, corrected here**: (a) *"North-star MET"* is true for the
  demonstrated workloads (certified cold engine-dev build + synthetic multi-exec run,
  both via real Cloud traces) — it is NOT yet validated on arbitrary third-party CI
  traces (multi-engine, cross-host clock skew are un-probed §9 seams). (b) *"Oracle
  passes"* needs the nuance: exact (drift 0.00) on user-work classes; overall jaccard on
  live runs is 0.30 — below the tool's own 0.8 bar — with all divergence in the
  documented vantage-point residual (§5.1). Anyone re-running the oracle naively will see
  a "failure" that isn't one. (c) Memory notes said the feasibility branch was "10
  commits" (it's 12) and cited pre-rebase SHAs (`9555281f27` etc.) — use the post-rebase
  SHAs in §2. (d) Memory said the branch was "unpushed" — stale; it's pushed (I updated
  the memory).
- **Evidence-limited (flagged, not suspect)**: live-run numbers (span counts, rankings,
  Cloud gate outputs) come from transcript-pasted tool output, mutually consistent across
  agents/commits, but I did not re-run cold builds or Cloud fetches; `read_agent`
  surfaces only final messages, so intermediate tool output is unverifiable in principle.
  The forensics doc itself marks its one unproven link (which exact sub-hop between
  span-creation and the engine DB dropped spans) — the BSP-overflow mechanism was
  subsequently established by the validated before/after in `0688b8b7af`, but "proven at
  the sub-hop level" would overstate it.
- **No unevidenced "done" claims found**: every "signed off" I chased had a written
  review with a verdict line; the one review that said "not landable" (completeness v1)
  was in fact treated as a blocker and the design was redone.
