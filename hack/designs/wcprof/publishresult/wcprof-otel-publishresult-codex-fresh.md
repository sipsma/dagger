# wcprof OTel publishResult Emit-Gap Review

Reviewer: Codex fresh pass

Scope: item 3 (`98ee73047c`) plus the newly surfaced `dagql.publishResult` root finding. I did not find a raw trace artifact in this worktree, so I could not independently reproduce the exact `330/331` count. This review separates that empirical count from what the code and design prove.

## Verdict

The framing is likely correct: `dagql.publishResult` is intended to be causally parented under the `call_exec` op, the loader would honor that parent mechanically if it were present, and the current emit path has a gap because it starts `publishResult` from a context but does not explicitly publish the already-known `oc.execSpanCtx` as causal parent data.

The strongest attempted refutation is that the raw spans might contain a non-empty parent ID that the loader drops because the parent span is missing/truncated from the span set. Without the raw trace, I cannot rule that out for the `330/331` measurement. But that would still be a data/front-end completeness problem, not an analysis fix. If the raw `publishResult` records have empty/all-zero parent IDs and no `wcprof.parent`, the emit-gap diagnosis holds.

The fundamental fix is emit-side: record the `publishResult -> call_exec` causal parent explicitly. Prefer making the OTel `parentId` itself the `call_exec` span if feasible; otherwise stamp `wcprof.parent=<call_exec span id>` from `oc.execSpanCtx`. Do not infer this in the loader and do not reintroduce root chaining.

## Item 3 Sanity Check

The item 3 replay change is aligned with the governing principle. `Run` now pre-anchors every root at its recorded start before finishing any root (`engine/wcprof/wcanalyze/replay.go:360`-`383` at `98ee73047c`). The `par < 0` branch in `spawnTo` is now exact root anchoring, uncounted (`engine/wcprof/wcanalyze/replay.go:523`-`530`), while the remaining unschedulable parent/prefix cases are counted as unfaithful data (`engine/wcprof/wcanalyze/replay.go:543`-`562`, `engine/wcprof/wcanalyze/replay.go:565`-`581`).

That change did not create graph roots. Graph root detection is still the ordinary parent wiring: if an op's parent ID does not resolve, it is appended to `g.Roots` (`engine/wcprof/wcanalyze/graph.go:281`-`300`). Item 3 only stopped serializing those roots in replay. So if `publishResult` ops are roots in the graph, the parent information was already missing/unresolved before item 3; chaining merely hid the consequence.

## Loader vs Emit

I do not see a loader bug in the parent mapping.

The loader's parent rule is deliberately zero-inference: `wcprof.parent` overrides `parentId`, otherwise it uses `parentId` (`engine/wcprof/wcotel/loader.go:442`-`450`). During compile it maps that causal span ID to an op ID (`engine/wcprof/wcotel/loader.go:282`-`287`). `Build` then wires `ParentID` to `Parent`, or makes the op a root if no parent resolves (`engine/wcprof/wcanalyze/graph.go:281`-`300`).

There is no special case that would drop a valid `publishResult` parent. The loader already has a direct test that `wcprof.parent` overrides `parentId` (`engine/wcprof/wcotel/loader_test.go:149`-`168`). Therefore:

- if a `publishResult` span has `parentId=<call_exec span id>`, the loader will parent it;
- if it has `wcprof.parent=<call_exec span id>`, the loader will parent it;
- if it becomes a root, either both fields are absent/zero, or they name a span missing from the input.

That is why the raw-trace distinction matters: empty parent is an emit parent gap; non-empty but unresolved parent is trace/front-end truncation or missing parent-span data. Neither points to a replay fallback.

## Is publishResult Parentless By Design?

No. The design says the opposite.

The design explicitly says to emit `dagql.publishResult` as a child of `call_exec`, using the already-ended `call_exec` `SpanContext` stashed on `oc` (`hack/designs/wcprof-otel-design.md:516`-`525`). It also states the IR result should be a `dagql.publishResult` internal op under `call_exec` (`hack/designs/wcprof-otel-design.md:592`-`596`). The implementation comments repeat that intent: `beginOTelPublishResult` "starts the dagql.publishResult span as a child of the call_exec span carried by ctx" (`dagql/otelprof_hooks.go:68`-`75`), and the `wait` path says this is the OTel analog of native's `pubOp`, child of the already-ended call_exec span (`dagql/cache.go:4013`-`4018`).

Native does publish the parent explicitly: it begins the internal op with a context containing `oc.profOpID` (`dagql/cache.go:4003`-`4011`). OTel already has the analog identity as `oc.execSpanCtx`, stashed under `callsMu` before publication (`dagql/cache.go:3755`-`3759`). The problem is that `beginOTelPublishResult` does not take that `SpanContext`; it relies on the current span in `oc.sharedWorkCtx` (`dagql/otelprof_hooks.go:76`-`83`, call site `dagql/cache.go:4016`-`4018`).

That reliance is exactly the fragile part. The code knows the desired causal parent (`oc.execSpanCtx`) but does not put it on the span as durable data. Any context discontinuity leaves the loader with no basis to recover.

## Strongest Refutations

1. Could `context.WithoutCancel` or leases be stripping parentage?

Weak refutation. `context.WithoutCancel` preserves values, and the operation lease wrappers use `context.WithValue` around the existing context (`dagql/operation_lease.go:26`-`45`, `engine/snapshots/lease.go:25`-`40`). Those are not obvious parent strippers. This makes it less obvious why live spans are parentless, but it does not save the current emit contract: the parent should be recorded from `oc.execSpanCtx`, not inferred from context survival.

2. Could the unit tests prove emit is fine?

No. `TestEmitHooksProduceLoaderShape` calls `beginOTelPublishResult(execCtx)` immediately on the direct `call_exec` context (`dagql/otelprof_hooks_test.go:131`-`145`). It asserts classification, wait links, and gate pass, but does not assert the publishResult parent after the real `c.wait`/`initCompletedResultOnce` path (`dagql/otelprof_hooks_test.go:162`-`235`). The fixture tests hand-build publishResult with a parent under `call_exec` (`engine/wcprof/wcotel/chunk2_test.go:164`-`184`), so they validate the intended loader shape, not the problematic production context path.

3. Could the count be wrong?

Possible; I cannot verify it without the raw trace. The right audit is straightforward: for every raw span named `dagql.publishResult`, print `spanId`, `parentId`, `attrs["wcprof.parent"]`, whether the parent span ID exists in the span set, and the loaded op parent/root status. If `330` have empty parent and no override, the implementer is exactly right. If they have parent IDs pointing to absent spans, the immediate bug is trace completeness/unresolved parent accounting, but the analysis still must not compensate.

4. Did removing chaining introduce the issue?

No for graph structure. Chaining lived only in replay scheduling. It could make many roots produce a plausible makespan, but it could not make parentless `publishResult` spans roots. Those roots come from the loaded graph's missing/unresolved parent relation.

## Baseline and Drift Findings

Finding #1, "bit-identical was wrong," is sound. The old chained-root baseline could differ at factor 1 because later roots inherited `chainSimEnd`, not recorded root end. If an earlier root had replay drift, the drift shifted later roots. Removing chaining makes roots start at recorded starts, so it is expected that the baseline moves toward the recorded trace span.

However, `baseline == recorded makespan` is not a sufficient faithfulness criterion. A graph with 330 fake leaf roots can still match recorded makespan exactly because those roots are anchored at their recorded starts and, as leaves, replay to their recorded ends. Exact makespan proves the root-chaining artifact is gone; it does not prove the OTel root structure is faithful.

Finding #2, "the -2.4% was 100% chaining," is sound in the narrow numeric sense if the only replay change between the measured runs is item 3 and the OTel baseline moved from `5.96s` to the recorded `6.11s`. That accounts for the baseline drift that was being discussed. It is not a proof that OTel multi-root what-if rankings are now trustworthy, because the `publishResult` root structure remains unfaithful until the emit gap is fixed and gated.

## Fundamental Fix

The data must publish the true causal parent of `dagql.publishResult`: the `call_exec` span for the shared execution.

The cleanest fix is direct OTel parentage if feasible:

- change the publish hook to accept `oc.execSpanCtx`;
- get the tracer provider from the existing recording context;
- start `dagql.publishResult` with a context whose parent span context is `oc.execSpanCtx`;
- assert in the real `c.wait`/publication test that the exported span's `Parent().SpanID()` equals `oc.execSpanCtx.SpanID()`, and that the loader parents it under the `call_exec` op.

If direct parentage is awkward or risks provider/noop mistakes, the existing `wcprof.parent` mechanism is the robust analysis-facing fix:

- set `wcprof.parent=<oc.execSpanCtx.SpanID()>` on `dagql.publishResult`;
- keep whatever UI parent the SDK records;
- rely on the loader's already-existing `wcprof.parent ?? parentId` rule.

I would prefer direct parentId plus a test, because publishResult has no stated need for UI parentage to differ from causal parentage. But stamping `wcprof.parent` is still principled: it is emitted causal data, not loader inference, and it uses the same explicit-causality mechanism already used for lazy re-homing.

Do not fix this by teaching the loader "if name == dagql.publishResult, find the nearby call_exec." That would violate the principle. It is temporal/name inference and will fail exactly when there are many overlapping cache misses.

## Gate

Add a hard gate for this class of unfaithful roots.

Minimum targeted signal: `InternalRootOps` = roots with `Kind == "internal"`, especially class `dagql.publishResult`. A `publishResult` internal op is not a session root. It is engine-internal work caused by a cache execution, so root status means missing/unresolved parent data.

Better broader signal: hard-fail classified non-session roots that are not explicitly allowed root kinds. True roots should be request/session roots; `call_exec`, `lazy`, `exec`, `service_start`, and `internal` roots generally indicate missing parent/launch data. Start with internal roots if the broader allowlist is too risky.

Also consider an unresolved-parent provenance counter in the loader. Today a non-empty parent span ID that is absent from the span set quietly becomes `ParentID=0`. That is fine for unclassified external spans, but for classified wcprof ops it should be visible. This would distinguish "emit wrote no parent" from "front-end lost the parent span."

## Final Summary

Framing correct? Likely yes. Strongest attempted refutation: raw `publishResult` spans might have non-empty parent IDs whose parent spans are missing from the input, which would make the immediate issue parent-span loss rather than empty-parent emit. I cannot resolve that without the raw trace. Code/design still say `publishResult` must have a causal parent and the loader would use it if present.

Findings #1/#2 sound? Yes, with scope. Removing chaining explains the baseline drift disappearing, but exact baseline makespan is not enough to certify root-structure faithfulness. The `-2.4%` baseline drift can be all chaining while OTel what-ifs remain untrustworthy due to fake internal roots.

Fundamental fix: emit the `publishResult -> call_exec` causal parent. Prefer real `parentId` if feasible; otherwise stamp `wcprof.parent` from `oc.execSpanCtx`. Add a real production-path emit test and an internal-root gate.

Alignment/pushback: aligned with the principle. No loader/replay compensation. The only pushback is to not overstate "baseline exact" as structural faithfulness; it is a useful sanity check, not the gate.
