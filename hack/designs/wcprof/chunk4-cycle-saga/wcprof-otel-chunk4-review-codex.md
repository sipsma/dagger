# wcprof x OTel Chunk 4 review

Reviewed commit: `4d6987fdc2` on Chunk 3 `107ebe5c0c`, base `b442cd2533`.

Scope: Chunk 4 exec engine/user split (§3.3), service start waits (§3.4), and the module-loading cycle addendum. I reviewed the Chunk 4 isolation diff and the accumulated Chunks 1-4 behavior. Focused tests run at `4d6987fdc2`:

```sh
go test ./dagql ./engine/engineutil ./core ./engine/wcprof/wcotel
```

Result: PASS.

## Overall verdict

Chunk 4's code is sound enough to use as the base for Chunk 5. I did not find an isolation blocker in the exec split or service-start emit.

The Chunks 1-4 trajectory is still broadly on the north star, and Chunk 4 adds the missing user-work-first-class `exec.processRun` surface. However, the module-loading cycle is a real high-severity holistic issue in the accumulated OTel graph. I do not think it is caused by Chunk 4, but I also do not think it should be accepted as a §9 reserve seam for v1 if module-loading traces are in scope. It needs an emit-side/module-loading fix or a raw-trace-backed owner decision before final acceptance.

## REAL issues

### HIGH: Module-loading traces can still produce a false causal cycle; not a Chunk 4 regression, but a v1 blocker unless fixed or explicitly scoped out

Verdict: REAL issue, holistic across Chunks 1-4. I agree with the broad cause in the cycle note, with the caveat that the raw 9.3MB trace was not available to me, so I could not independently inspect the exact op IDs beyond the supplied cycle structure.

Evidence:

- The design's binding invariant says OTel parentage is context propagation, not the live call stack, and any causal parent edge must be a genuine synchronous nesting; otherwise the replay's implicit join invents dependencies or cycles (`hack/designs/wcprof-otel-design.md:45-86`).
- The replay really does classify the reported closing edge as a join, not a no-op: a wait with `target != op` and `wait.End >= target.End - epsilon` becomes `actWaitJoin` (`engine/wcprof/wcanalyze/replay.go:162-173`), and join waits recurse into `finish(target)` (`engine/wcprof/wcanalyze/replay.go:379-381`). The reported `waitEnd=195 == targetEnd=195` is exactly a completed wait, so this is not an epsilon-boundary replay bug.
- Cycles are intentionally a hard gate failure: `CheckStructural` runs the replay and fails if `CycleWarnings > 0` (`engine/wcprof/wcotel/gate.go:91-111`), matching the design's "cycle => unfaithful emit" rule (`hack/designs/wcprof-otel-design.md:955-962`).
- DagQL can create the concurrency required for this shape: sibling selections are resolved in parallel (`dagql/server.go:1120-1163`), and module loading recursively issues nested `dag.Select(..., Field: "asModule")` calls through dependency/toolchain loading (`core/schema/modulesource.go:3270-3272`, `core/schema/modulesource.go:3430-3481`) plus more `moduleSource` selects for related module refs (`core/schema/modulesource.go:2108-2138`).
- The singleflight edge itself is real: joiners attach to an existing `ongoingCall` under `callsMu` and block in `c.wait` (`dagql/cache.go:3694-3704`), and `c.wait` emits the OTel wait link with reason `singleflight`/`call_exec` over the actual blocked interval (`dagql/cache.go:3935-3958`).
- The cycle note reports all 5 cycles in `Query.moduleSource`, with a representative loop `op#54 --wait:singleflight--> op#94` closing against an implicit-join path back through nested module loading (`hack/designs/wcprof-otel-chunk4-cycle-analysis.md:11-32`). A real synchronous dependency from `op#94` back to work ending at `op#54` would imply `op#94` could not complete before that dependency; the reported intervals have `op#94` ending at 195ms and `op#54` ending at 205ms, so the reverse edge is very plausibly an invented implicit join.

Why I do not attribute it to Chunk 4:

- The Chunk 4 diff does not touch loader, gate, or replay; the changed files are exec/service emit and tests (`git diff --name-only 107ebe5c0c..4d6987fdc2`).
- The reported strip test removed every Chunk 4 op (`exec.run`, `exec.containerStart`, `exec.processRun`, `service.start`) and got identical 5 cycles + 18 fallback anchors (`hack/designs/wcprof-otel-chunk4-cycle-analysis.md:35-41`). I could not rerun that without the raw trace, but if the reported strip result is accurate, it is strong evidence that Chunk 4 spans are not on the cycle path.
- Module-free exec/service traces remain cycle-free per the note (`hack/designs/wcprof-otel-chunk4-cycle-analysis.md:35-40`), and the focused Chunk 4 fixtures pass the structural gate.

Disposition:

Do not paper this over in the loader or accept the replay cycle-break as "good enough" for v1. The design explicitly treats this as an emit-side faithfulness failure (`hack/designs/wcprof-otel-design.md:68-86`, `hack/designs/wcprof-otel-design.md:955-962`). The likely fix direction is to investigate the module-loading/nested-client/singleflight emit shape and remove the false parent/implicit-join edge while preserving the genuine singleflight wait. Suppressing the wait link in the loader would throw away the real dependency and violate the anti-inference/trivial-loader contract.

A from-source rebuild at Chunk 3 HEAD would be useful for attribution hygiene, but I would not block the diagnosis on it. The strip test plus unchanged analysis path are enough to treat this as a pre-existing Chunk 2/module-loading gap surfaced during Chunk 4 validation.

## Chunk 4 isolation

### Exec split: NOISE, correct as implemented

The `exec.run` span is minted before the executor run and under the propagated executor context (`engine/engineutil/executor.go:116-142`). For normal `withExec`, that context is the resolver execution context; `container_exec.go` launches `engineClient.Run(execCtx, ...)` and then waits for it to finish (`core/container_exec.go:2104-2150`), so the nesting is a real synchronous child of `call_exec`. For services, the context is the long-lived service exec span, which is also a genuine parent for the daemon run (`core/service.go:748-856`).

The helper emits exactly the approved v1 shape: `exec.run` is passthrough and classified as `OpKindExec` (`engine/engineutil/otelprof.go:46-53`), and `exec.containerStart` / `exec.processRun` are backdated child phases with `wcprof.work_type=user` only on `processRun` (`engine/engineutil/otelprof.go:77-117`). The split point matches native's started callback: native stores the callback time and records `exec.containerStart` / `exec.processRun` from the same boundary (`engine/engineutil/executor_spec.go:1270-1288`, `engine/engineutil/executor_spec.go:1405-1430`).

The never-started case is also correct: native emits only `exec.containerStart` over the full interval when `startedNS` is absent (`engine/engineutil/executor_spec.go:1415-1420`), and OTel mirrors that when `started.IsZero()` (`engine/engineutil/otelprof.go:81-86`).

One caveat, not a defect: earlier executor setup phases still land as `exec.run` self-time in v1. Native has per-setup phase ops for each setup function (`engine/engineutil/executor.go:202-217`), while Chunk 4 intentionally implements only the approved `runContainer` split and leaves finer phases as the follow-up seam (`hack/designs/wcprof-otel-design.md:741-770`). So "slow engine => containerStart" is true for the run-container started-callback window, not for every pre-runc setup phase.

### Services: NOISE, correct as implemented

`service.start` is minted under `ss.l` before publishing `ss.starting[key]`, satisfying Invariant T for installer wait targets (`core/services.go:1020-1047`; design requirement at `hack/designs/wcprof-otel-design.md:790-799`). Joining installers emit a `service` wait link to the stashed `service.start` span context over the actual blocked interval (`core/services.go:991-1012`). The emit helper marks `service.start` passthrough, classified as `OpKindServiceStart`, and carries the service digest as `dag.digest` (`core/services.go:206-223`).

The long-lived service availability span does not become a false bottleneck. It is already `ui.passthrough` (`core/service.go:726-754`) and the actual daemon `exec.run` nests below it through `bk.Run(ctx, ...)` (`core/service.go:833-856`). In replay, an outliving child is spawned but not implicitly joined by `service.start` because `joinUpTo` only joins children whose recorded end is `<= t` (`engine/wcprof/wcanalyze/replay.go:353-389`). Self-time subtraction also clips child cuts to the parent's interval (`engine/wcprof/wcanalyze/graph.go:339-397`). The synthetic service fixture exercises exactly this: the idle daemon has the largest raw self-time but saves zero makespan, while the real consumer work ranks (`engine/wcprof/wcotel/chunk4_test.go:201-326`).

### Exported helpers: NOISE, reasonable boundary

Exporting `dagql.OTelProfActive` and `dagql.EmitOTelWait` is a small, coherent package boundary for non-`dagql` choke points. `OTelProfActive` is just "current span is recording" (`dagql/otelprof_hooks.go:32-43`), and `EmitOTelWait` centralizes the wait-link wire format and targetless-fail-loud behavior (`dagql/otelprof_hooks.go:86-135`). That keeps service waits byte-identical to singleflight/lazy waits and avoids duplicating string attributes outside the existing emit package.

### Performance/simplicity: NOISE

Chunk 4 adds constant telemetry work per exec run (`exec.run` plus one or two child phase spans) and constant work per service start/waiter (`service.start` plus one wait link per blocked installer). I did not find any new quadratic behavior or unbounded per-trace scan. The only high-fan-in volume surface remains the wait-link fan-in already covered by Chunks 1-2 and `LinkCountLimit = 16384` (`engine/server/session.go:682-705`).

## Validation assessment

The validation added in this chunk is directionally right:

- Real exec emit is checked against an in-memory SDK tracer, then compiled through the loader and structural gate (`engine/engineutil/otelprof_test.go:112-222`, `engine/engineutil/otelprof_test.go:224-253`).
- Real service-start emit is similarly checked through the loader/gate (`core/otelprof_services_test.go:98-200`).
- Replay/oracle fixtures assert the counterfactual behavior for slow user process vs engine container-start and for service idle-daemon non-ranking (`engine/wcprof/wcotel/chunk4_test.go:81-199`, `engine/wcprof/wcotel/chunk4_test.go:201-326`).

The module-loading cycle is exactly the kind of failure the §6.1 gate was supposed to catch. That is good validation, but it is also a real red flag: a module-loading workload is not an exotic shape for Dagger Cloud CI traces. Treating this as a loud gate failure is correct; accepting it as a reserve seam would weaken the approved design's "faithful emit / no replay surgery" contract.
