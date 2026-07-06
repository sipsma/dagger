# wcprof OTel Chunk 4 cycle fix review - Codex fresh

Scope: adversarial review of `hack/designs/wcprof-otel-chunk4-cycle-findings.md`, the proposed shared replay start-only anchor, the proposed shared `service.start` re-root, and the landed lazy-triggered-exec test `8c331d8272`. I did not have the raw 9.3MB trace or native dump, so the empirical sibling/native-identical claims are evaluated from the report plus the replay/build code.

## Verdict

The revised diagnosis is substantially more convincing than the earlier false-nesting theory. The current replay really does overreach: when an out-of-order referenced op has not been started, `finish(child)` calls `finish(parent)` and therefore replays the parent's full finish, including implicit joins and waits, just to discover the child's start (`engine/wcprof/wcanalyze/replay.go:313-333`, `engine/wcprof/wcanalyze/replay.go:371-389`). A true wait between concurrent sibling module loads can close a cycle through that artificial anchor path. The claim that this is shared native+OTel is plausible because the relevant mechanism is entirely in the shared graph/replay after both sources have compiled to the same IR.

However, I would not land the proposed `startOf(parentStart + recordedOffset)` exactly as shown. It fixes the observed cycle, but it creates a counterfactual correctness risk: when the parent's pre-spawn work is itself scaled by a what-if, the simulated child start should move according to the simulated parent clock at the spawn point, not the recorded offset. That is the main remaining blocker in the proposed replay fix.

The fundamental direction is right: replace "finish parent to start child" with "advance only far enough to establish the child's simulated spawn/start." The implementation should preserve simulated pre-spawn scaling semantics, keep an out-of-order start diagnostic, and add tests that specifically cover the sibling singleflight cycle and the parent-pre-spawn counterfactual case.

The `service.start` erasure finding is real. The proposed fix, re-rooting the long-lived service availability/daemon work out of `service.start` in both native and OTel, is the correct direction.

## Findings

### High - Proposed `startOf` uses recorded offset, not the simulated spawn point

The proposed code:

```go
s.setStart(i, s.startOf(par)+(s.p.startNS[i]-s.p.startNS[par]))
```

is safe for baseline-ish anchoring only insofar as the parent's pre-child timeline has not changed. But the analyzer's core product is counterfactual replay. In the normal in-order path, the parent reaches an `actSpawn` after replaying earlier self segments, waits, and joins; that replay applies the class factor to self segments (`engine/wcprof/wcanalyze/replay.go:375-379`). Therefore the child's simulated start is the parent's simulated clock at the spawn point, not "parent simulated start + recorded offset."

That difference matters for an out-of-order wait target. Example shape:

- parent `P` has 50ms of self before spawning child `C`;
- sibling `S` waits on `C` before `P` has been replayed;
- a what-if scales `P`'s pre-spawn class by 0.5.

The existing in-order semantics would spawn `C` at simulated +25ms. The proposed `startOf` anchors it at the recorded +50ms. That can understate savings from `P`'s pre-spawn work, shift wait propagation, and affect rankings. Existing tests passing does not rule this out because the current `wcanalyze` tests cover ordinary sequential/singleflight cases, not an out-of-order reference whose parent's pre-spawn work is scaled (`engine/wcprof/wcanalyze/replay_test.go:122-189`).

This is not a reason to keep the old anchor. The old anchor is wrong too: it asks for parent finish when only parent progress to spawn is needed (`engine/wcprof/wcanalyze/replay.go:313-333`). But the replacement should be a start-to-spawn anchor, not a recorded-offset anchor.

Recommended shape: add a replay primitive that can establish `start(i)` by advancing the parent to the child's spawn point under the current factors, without replaying the parent's full implicit joins past that point. It must remain memoized/linear enough that many out-of-order references do not devolve into repeated parent scans. If that is too much for the first patch, then the recorded-offset version needs a written proof that out-of-order start anchoring intentionally preserves original offsets under counterfactuals; I do not see that proof in the design or current replay semantics.

### Medium - Zeroing `FallbackAnchors` loses a useful structural signal

The prototype reportedly makes fallback anchors go to zero. That follows from replacing the current fallback block (`engine/wcprof/wcanalyze/replay.go:324-338`) with unconditional `startOf`. But `FallbackAnchors` is currently surfaced by the structural gate (`engine/wcprof/wcotel/gate.go:91-99`) and can be bounded/reported (`engine/wcprof/wcotel/gate.go:131-132`, `engine/wcprof/wcotel/gate.go:158-163`). The failing `TestGateFallbackAnchorsReportOnlyAndThreshold` is not just stale test text; it is pointing at a diagnostic that would disappear (`engine/wcprof/wcotel/gate_test.go:227-254`).

I agree that the old fallback count should not remain as-is if start anchoring becomes a first-class replay operation. But it should be replaced, not silently erased. A counter such as `OutOfOrderStarts` / `StartAnchors`, with sample ops, would preserve the operational signal that the trace contains cross-tree or out-of-order dependencies. That signal is especially useful here because these shapes are exactly where replay fidelity is hardest.

### Medium - Empirical native-identical/topology claim should be reduced to a checked regression artifact

I can verify the mechanism from code, but not the reported raw topology or the native-identical cycle count because the trace/native dump are not accessible. The sibling correction is consistent with the graph builder: parentage is built from parent IDs and nested-client links (`engine/wcprof/wcanalyze/graph.go:281-294`), while waits are attached separately (`engine/wcprof/wcanalyze/graph.go:247-268`). The replay can then add an artificial path by full-parent anchoring.

Before landing the replay change, I would ask the implementer for a minimal extracted native+OTel subgraph or test fixture that reproduces the cycle and asserts:

- op#54/op#94 are siblings, not parent/descendant;
- removing only the anchor edge removes the SCC;
- native and OTel both hit the same replay cycle on that fixture;
- the fixed replay produces zero cycles without deleting or weakening the true wait edge.

This is evidence hygiene rather than a competing theory. I do not see a better code-level explanation than the replay anchor overreach.

### Medium - `service.start` re-root is correct, but it must fix both native and OTel and update the fixture shape

The erasure is code-confirmed. In `startWithKey`, native `wcprof.BeginOp` and OTel `beginOTelServiceStart` both wrap `svcCtx` before `svc.Start` is called (`core/services.go:1021-1035`, `core/services.go:1051`). `Service.startContainer` then starts the long-lived service availability span from that same context (`core/service.go:748-755`), and the executor begins native/OTel `exec.run` under the propagated context (`engine/engineutil/executor.go:121-142`). Because self time subtracts child intervals (`engine/wcprof/wcanalyze/graph.go:379-392`), a long daemon child can erase the start/health-check window in both sources.

Re-rooting the long-lived availability span and daemon `exec.run` out of `service.start` is the right fix, provided the start operation still covers the synchronous start and health-check path and installer waits still target the `service.start` span/op (`core/services.go:995-1011`, `core/services.go:1091-1092`). An OTel-only re-root would be wrong because it would hide the native bug and break the oracle.

The existing Chunk 4 fixture bakes in the bad shape: OTel nests `exec daemon-cmd` under `service.start` (`engine/wcprof/wcotel/chunk4_test.go:223-229`), and native nests daemon `exec.run` directly under `service.start` (`engine/wcprof/wcotel/chunk4_test.go:307-314`). The service fix should update that fixture and add a direct assertion that a slow start/health-check window gives `service.start` meaningful self-time and can headline, while an idle daemon remains off the critical path.

### Low - Landed lazy-triggered-exec test is good coverage, not a cycle fix

Commit `8c331d8272` adds `dagql/otelprof_lazy_exec_test.go`, which checks the live composition claim: direct re-pointed `exec.run` receives `wcprof.parent` for the lazy op while descendant phases do not (`dagql/otelprof_lazy_exec_test.go:96-110`), the loader re-homes the exec subtree under the lazy op, `work_type=user` survives, and the structural gate passes (`dagql/otelprof_lazy_exec_test.go:112-139`). That is useful coverage for Chunk 3/4 composition. It does not materially validate the replay anchor fix or the `service.start` re-root.

## Cycle Diagnosis

The findings add up:

- Current replay starts an unstarted op by replaying its parent to completion (`engine/wcprof/wcanalyze/replay.go:313-333`). That is an anchor edge, not a causal dependency present in the graph.
- Parent replay includes implicit joins of children by recorded end time (`engine/wcprof/wcanalyze/replay.go:351-368`, `engine/wcprof/wcanalyze/replay.go:389`) and explicit wait joins (`engine/wcprof/wcanalyze/replay.go:379-381`).
- A wait ending at the target end is classified as a join, not a fixed delay, by the epsilon rule in compile (`engine/wcprof/wcanalyze/replay.go:162-173`). So the reported `waitEnd == targetEnd` edge is a real replay dependency, not a boundary misclassification.
- If two sibling executions truly wait across singleflight, a request to finish one sibling before its parent replay has started it can force the parent to join the other sibling, which can wait back on the first. The in-flight guard then reports/breaks the cycle (`engine/wcprof/wcanalyze/replay.go:341-345`).

So yes: the root cause can be the replay anchor, not OTel parent stamping or loader behavior. The "native cycles identically" claim is load-bearing for changing shared replay, but it is also plausible because native and OTel share this exact replay after graph construction.

## Alignment

Changing `wcanalyze` breaks the original "OTel loader feeds the unchanged native replay" premise. In this case that is justified if the native replay is itself unsound. The owner ruling also removes the option of treating native parity as sufficient. The right principle is: preserve the shared IR and shared replay, but fix the replay when the shared replay is the bug.

That said, the proposed implementation must preserve replay semantics under what-if scaling. A recorded-offset `startOf` is a tempting small patch, but the replay's actual semantics define child start at simulated parent progress to spawn. The fundamental fix is start-only in the sense of "start by replaying only to spawn," not "start by ignoring simulated pre-spawn progress."

## Better Alternative

Implement a partial parent replay / start-to-spawn anchor:

- `finish(i)` should call a helper that establishes `i`'s start.
- If the parent has not reached the child's spawn point, advance the parent only through actions needed before that spawn, using the same factor-scaled self/wait semantics as full replay.
- Do not run the parent's implicit joins or actions beyond the child's spawn just to answer `start(i)`.
- Memoize enough state to avoid repeated O(children * references) behavior on large traces.
- Count/report this as an out-of-order start anchor so the gate/debug output remains informative.

Tests needed before I would sign off:

- minimal sibling singleflight cycle fixture proving the old replay cycles and the new replay does not;
- counterfactual fixture where parent pre-spawn self is scaled and an out-of-order wait references the child, proving the child start follows simulated parent progress, not recorded offset;
- baseline/gate fixture replacing `FallbackAnchors` with the new diagnostic;
- shared service fixture proving slow `service.start` self ranks after daemon re-root, in both native and OTel.

## Final Position

Cycle correctly diagnosed: mostly yes, from code and reported evidence. I would still want the extracted native+OTel subgraph as a regression fixture.

Fix correct/fundamental/side-effect-free: direction yes; exact proposed `startOf` no. It is not side-effect-free because it can mis-anchor starts under counterfactual scaling of parent pre-spawn work. Make it "advance to simulated spawn," not "recorded offset."

Service fix: yes. Re-root availability/daemon out of `service.start` in both sources, keep installer waits targeting `service.start`, and update fixtures to assert slow starts can headline.

Sound to proceed: do not proceed with the simple recorded-offset replay patch as the final fix. Proceed with a shared replay fix only after the counterfactual start semantics and diagnostics are addressed.
