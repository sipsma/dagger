# wcprof × OTel — GROUND-UP forensics: is data actually "missing," where, and why? (trust nothing)

## Mandate (Erik): debug this from scratch, adversarially. Trust NOTHING from prior investigations.

We have spent many rounds chasing a finding and may be running on **stale assumptions** about how the
system actually works. Re-derive everything from first principles + direct measurement. The ONLY
things settled — never relitigate — are: (1) the GOVERNING PRINCIPLE: the analysis is a rational
function of FAITHFUL data, never compensates (no inference/fallback/chaining); when a rational model
reports something odd, fix the DATA/EMIT, not the model; debug model vs data SEPARATELY. (2) the GOAL:
answer "why was my CI run slow?" from a Dagger Cloud trace, user work first-class, via the same
counterfactual replay native wcprof uses. Everything else below is a HYPOTHESIS to verify or destroy.

## Background to get up to speed on (read widely; don't take summaries on faith)

- **wcprof** = the wall-clock profiler (merged PR #13393, `engine/wcprof/**`): records ops/waits/links
  from engine hooks, runs a counterfactual discrete-event replay to rank true bottlenecks. Read PR
  #13393, `engine/wcprof/wcanalyze/{replay.go,graph.go}`, `internal-docs/`.
- **The OTel source** = compile a Dagger Cloud OTel trace into the SAME IR, analyzed by the same
  replay. Emit faithfulness is fixed at engine choke points; the loader is zero-inference. Read the
  design doc `hack/designs/wcprof-otel-design.md` (the contract), the emit (`dagql/cache.go`,
  `dagql/otelprof_hooks.go`, `dagql/telemetry.go`, `dagql/tracing.go`), the loader
  (`engine/wcprof/wcotel/loader.go`, `gate.go`), the local capture tool (`hack/otlpdump/`).
- The whole feedback history is in `hack/designs/wcprof-otel-*.md` (design reviews + the cycle-fix +
  the rational-model rework + the publishResult investigation). Read what's relevant; question all of it.

## THE CURRENT PROBLEM (the thing to get to the bottom of)

When a local OTel capture (`otlpdump`) of a module-load workload is loaded, ~330 of the ~331 graph
"roots" are `dagql.publishResult` ops — internal spans that by design are children of a `call_exec`.
We have been interpreting this as "data is missing" (the `call_exec` parent SPANS are absent from the
capture). **But we do not actually know that's what's happening, where it happens, or why.** Re-derive it.

## What prior investigation FOUND (verify each claim independently — some were wrong/withdrawn)

- The emit records `parentId` for ~100% of `publishResult` (0 empty parentId across 4 local captures);
  the lone empty-parent root is the real command. ⇒ "emitted parentless" (an emit gap) appears FALSE
  — but re-confirm: is parentId REALLY always set? on what captures? could there be a subset?
- The live-export double-emits each span (start `endNs=0` + end). Counts must DEDUPE (keep max-end, as
  the loader does). Deduped on `/tmp/otel-exec.jsonl`: 330 orphan publishResults (parentId set, parent
  span absent), 575 call_execs missing their publishResult child (bidirectional). Re-verify.
- The gate currently PASSES on `/tmp/otel-exec.jsonl` (the exec capture) despite 330 orphan roots
  (a dropped parent need not be a wait target) — so the unresolved-target check has a coverage hole.
- **WITHDRAWN by the implementer (do NOT treat as known):** "the loss is the engine→otlpdump HTTP
  export choking on the burst." No evidence; engine logs show no drop/queue/export errors; the export
  path was never instrumented. The loss location is UNKNOWN — capture instrument? export pipeline?
  ingest? a real emit/id bug? Pin it with direct evidence or say you can't.
- **NEW + confusing (Cloud):** reading the SAME run from Cloud via `cloud.StreamSpans(root:true)` (the
  `dagger trace` TUI API) returns **2784 spans vs the local capture's 11033** — a SUBSET (~1/5 of each
  op-kind), internally complete (0 orphans, childCounts match). So the only Cloud read path we have
  returns LESS than local, not more. "local lossy, Cloud complete" is UNVERIFIED and maybe backwards.

## THE THREADS TO FOLLOW (from the ground up — design experiments, gather concrete evidence)

1. **Is data actually missing, and is "parentId set but parent span absent" the right frame?** Or
   could the parentId point cross-trace? could the loader mis-resolve / mis-dedupe? could the
   "double-emit" be more complex (3 copies? heartbeats? `LiveSpanProcessor` semantics)? Re-derive what
   the capture actually contains, span-by-span, for a few concrete orphan publishResults + their named
   call_exec parents.
2. **Re-derive the ACTUAL telemetry pipeline** end to end: engine span processors (`LiveSpanProcessor`,
   `SpanHeartbeater`) → SDK exporter → the OTLP transport → BOTH sinks (local `otlpdump` receiver AND
   the Cloud exporter `engine/server` / `cloud.StreamSpans`). At EACH hop: is data dropped,
   transformed, rolled-up, deduped, sampled, batched-with-loss? Instrument or trace it — don't grep logs.
3. **WHERE are the call_exec spans?** For a specific orphan publishResult, its parentId names a
   call_exec spanId. Is that span: (a) never emitted? (b) emitted but dropped in export? (c) present in
   Cloud but not local? (d) present in local but mis-counted? Find it or prove it's gone, at which hop.
4. **The Cloud APIs — start from basics, are we missing something obvious?** `cloud.StreamSpans` with
   `root:true` returns a rolled-up TUI view (2784). Is there another API / parameter for the FULL,
   non-rolled-up trace? What does Cloud actually STORE vs what these read APIs RETURN? Did we capture
   local WRONG (a misconfigured otlpdump / wrong endpoint / partial drain)? Read the Cloud client
   (`cloud.*`), the engine's Cloud export wiring, and the trace API surface. Use Erik's token
   (`~/.config/dagger/credentials.json`, org in `~/.config/dagger/org` — read, NEVER print) freely.
5. **Root cause + impact:** once you know WHERE and WHY, what is the true impact on the OTel source's
   correctness (baseline, the cross-source oracle, the multi-root what-if rankings)? Is the production
   path (Cloud) affected, or only the dev-loop local capture? Is the right fix a capture/pipeline fix,
   an emit fix, a different read API, or something else — and does it honor the principle?

## Resources

- Captures (shared `/tmp`): `/tmp/otel-exec.jsonl` (exec workload, the one analyzed), plus chunk2's
  `/tmp/pubres-{mod,clean}.jsonl` (module workload). You can capture FRESH ones (preferred for
  forensics) with the dev CLI against the shared dev engine `dagger-engine.dev` (free to rebuild).
- Cloud token: `~/.config/dagger/credentials.json` (+ `~/.config/dagger/org`). Use it; never print it.
- The analyzer CLI: `cmd/wcprof-otel-analyze <capture.jsonl>` (runs the loader + gate + report).
- A gate signal `OrphanedParents` was just committed (flags ops whose recorded parent span is absent)
  — it's defensible regardless of cause, but it is NOT the diagnosis. The diagnosis is what you're after.

## Deliverable

Concrete, evidence-backed forensics: is data missing (yes/no/where), the exact mechanism (proven, not
hypothesized — or an explicit "unproven, here's what's needed"), the Cloud-vs-local reconciliation
(rollup view vs genuine loss vs our mistake), the true impact, and the principled fix. Flag every place
you are UNCERTAIN. Question the lead's and every reviewer's prior claims — including that data is
"missing" at all.
