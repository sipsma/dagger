# wcprof x OTel Chunk 4 review - Codex fresh

Reviewed commit: `4d6987fdc2` on Chunk 3 `107ebe5c0c`, base `b442cd2533`.

Scope reviewed:

- Chunk 4 isolation: exec split (`engine/engineutil`) and service start (`core/services.go`).
- Holistic Chunks 1-4 composition.
- Independent cycle investigation from the implementer's report, code, and design. The raw 9.3 MB trace was not accessible, so the cycle verdict is based on the described subgraph plus code/design mechanics.

## Verdict

Chunk 4 is not fully sound to build Chunk 5 on yet. The exec engine/user split looks faithful and well tested. The service implementation has a real attribution bug: the long-lived service availability span is a child of `service.start`, which strips most of `service.start` self-time and can make service wait joins replay as if the service became ready near process launch rather than after the health-check window.

Holistically, Chunks 1-4 are still pointed at the right north star, and user work is now first-class for plain execs. But there are now two significant blockers before productionization: the service-start shape above, and the module-loading cycle. I would not treat the cycle as a harmless reserve seam for v1; module loading is normal CI behavior, and the structural gate is correctly failing.

## Findings

### High: `service.start` is undercut by the long-lived service availability child

The code mints `service.start` under `ss.l` before publishing `ss.starting[key]`, which satisfies Invariant T for wait-target availability (`core/services.go:1027-1047`). It then calls `svc.Start(svcCtx, ...)` while `svcCtx` carries the `service.start` span (`core/services.go:1051`). In the container service path, `svc.Start` starts the existing long-lived service span with that same context (`core/service.go:748-754`), so the availability span is a child of `service.start`.

That child remains open until service exit, not until readiness. The health-check wait happens after that child span has started (`core/service.go:859-879`), and the service span ends later on service exit (`core/service.go:912-913`). Meanwhile `service.start` ends when `svc.Start` returns, after the start/health-check window (`core/services.go:1091-1092`).

The analyzer subtracts all child intervals from parent self-time (`engine/wcprof/wcanalyze/graph.go:379-392`). The replay only implicit-joins children whose recorded end is at or before the parent action/end time (`engine/wcprof/wcanalyze/replay.go:353-389`). So a long-lived child that starts near process launch and ends after `service.start` both:

- subtracts `[availability.start, service.start.end]` from `service.start` self-time, erasing the health-check/start interval from the target class; and
- is not joined by `service.start`, so a service wait edge to `service.start` can simulate as finished near the availability span's start instead of at readiness.

The Chunk 4 synthetic fixture encodes exactly this shape: `service.start` `[8,60]` with an availability child `[10,122]` (`engine/wcprof/wcotel/chunk4_test.go:221-229`). But it only asserts that the wait resolves and the idle daemon does not rank (`engine/wcprof/wcotel/chunk4_test.go:242-290`). The oracle comparison also misses this because `Oracle.Agrees` compares top-N overlap/drift only (`engine/wcprof/wcotel/oracle.go:112-125`, `:179-184`), and the service fixture filters small-self classes with `minSelf = 20ms` (`engine/wcprof/wcotel/chunk4_test.go:317-323`). That can hide the fact that OTel `service.start` no longer matches native's self-bearing `service.start`.

Impact: a slow service start or health check may not headline as `service.start`, and installer waits may not preserve the correct critical path through service readiness. That misses Chunk 4's service DoD even though Invariant T and the wait link itself are present.

Suggested direction: make the availability span non-causal for the analyzer. Options include starting it outside the `service.start` causal subtree, stamping an explicit `wcprof.parent` to a non-`service.start` parent, or adding a loader-recognized non-self/non-op convention for this availability marker. The important invariant is that `service.start` self-time and replay finish must cover the start/health-check window that installers waited on.

### High: module-loading cycle is a real unfaithful emit shape, not a replay bug

I agree with the implementer's core cycle diagnosis, with the caveat that I could not inspect the raw trace. The described closing wait is a true `actWaitJoin`: replay classifies a wait as a join when the target is known and `wait.EndNS >= target.EndNS - 1ms` (`engine/wcprof/wcanalyze/replay.go:41`, `:162-167`). The reported `waitEnd=195ms` and `targetEnd=195ms` therefore correctly recurses into the target, not a replay boundary bug.

The other side of the loop is exactly the design's known hazard: OTel parentage comes from context propagation, not the live stack, and false synchronous nesting can invent dependencies or cycles (`hack/designs/wcprof-otel-design.md:60-73`). The replay's implicit join waits for child ops ending by the current time (`engine/wcprof/wcanalyze/replay.go:353-368`). If two concurrent `Query.moduleSource` executions become nested by propagated context, the replay will read the nested one as a synchronous child even if the parent did not block on it.

This violates the Chunk 2 assumption that singleflight joiners are in a different subtree from the shared execution (`hack/designs/wcprof-otel-design.md:563-573`). In the reported graph, one module-source execution genuinely waits on the other via a `singleflight` wait link, while the reverse dependency is an implicit join over false nesting. That is sufficient to produce a real graph cycle. The gate is therefore doing the right thing: cycles are specified as unfaithful emit, not a replay flaw (`hack/designs/wcprof-otel-design.md:955-962`).

I also agree this is very likely not caused by Chunk 4. The isolation diff does not touch `wcanalyze/replay.go`, `wcanalyze/graph.go`, `wcotel/loader.go`, or `wcotel/gate.go`. The implementer's strip test removed every Chunk 4 op and got identical 5 cycles + 18 fallback anchors, with cycle ops all in `call`/`call_exec Query.moduleSource` and `load module:` spans (`hack/designs/wcprof-otel-chunk4-cycle-analysis.md:35-41`). Module-free exec/service traces being cycle-free is consistent with the cause being module loading plus Chunk 2/nested-client parentage, not exec/service emit.

Disposition: do not accept this as a reserve seam without more work. Module loading is a common path for the target CI workload, and a standing Cloud gate cannot start from "module-loading traces fail structural invariants." The next step should be a focused repro or extracted cycle subgraph, then an emit-side fix. Candidate directions are: prevent the false nested parentage at module-loading/nested-client boundaries, emit a `wcprof.parent` override for the offending concurrent module-load spans, or explicitly model the real dependency and detach the concurrent peer from the implicit-join subtree. I would not suppress/reclassify the wait in the loader; the loader is explicitly forbidden from breaking cycles or massaging over-serialization (`hack/designs/wcprof-otel-design.md:931-939`).

## Non-findings / Confirmed Behavior

- Exec split parentage is correct in the normal withExec path. `exec.run` is started around `c.run` under the propagated executor context (`engine/engineutil/executor.go:133-164`), and the split spans are emitted as children of that `exec.run` with `containerStart` and `processRun` intervals split at the started callback (`engine/engineutil/executor_spec.go:1270-1285`, `:1405-1430`). `processRun` alone gets `work_type=user` (`engine/engineutil/otelprof.go:77-116`).
- The never-started exec case is handled: `emitOTelExecSplit` emits only `exec.containerStart` for `[start,end]` and charges the error there (`engine/engineutil/otelprof.go:77-86`).
- Service Invariant T itself is implemented: `service.start` is minted and its span context is stashed before `ss.starting[key]` is published (`core/services.go:1027-1047`), and installers emit `service` wait links to that target (`core/services.go:991-1012`).
- Exporting `dagql.OTelProfActive` and `dagql.EmitOTelWait` is a reasonable simplification. It keeps wait-link wire encoding identical across call_exec, lazy, and services (`dagql/otelprof_hooks.go:32-43`, `:86-135`).
- No degenerate performance stood out. Chunk 4 adds O(1) spans per exec/service start and O(1) links per service waiter. Starting `service.start` under `ss.l` is a lock-hold to keep an eye on, but it mirrors the already accepted Invariant T pattern and is not an unbounded algorithmic issue.
- The `go vet` warnings in touched packages are pre-existing. Running the same vet command at parent `107ebe5c0c` reports the same `lostcancel` warnings with shifted line numbers.

## Holistic Chunks 1-4

The pieces mostly compose cleanly:

- Chunk 1 loader/gate remains unchanged and continues to mechanically read emitted ops/waits.
- Chunk 2's `call_exec` is the right parent for Chunk 4 `exec.run`.
- Chunk 3 lazy parent stamping composes with lazy-triggered execs because exec work follows ordinary span parentage beneath the stamped lazy work ancestor.
- Chunk 4 makes plain user process time first-class through `exec.processRun` and `work_type=user`.

The emerging problem is not the unchanged replay; it is incomplete emit faithfulness in nontrivial real traces. The design's statement that nested-client stitching needs no loader logic because OTel nests it "for free" (`hack/designs/wcprof-otel-design.md:941-945`) is now suspect for module-loading concurrency. The current implementation has proven the happy-path choke points, but the module-loading cycle shows that context-propagation parentage still needs an emit-side correction beyond the original four breaks.

## Verification

Run at `4d6987fdc2` in a detached worktree:

- `go test ./dagql ./core ./engine/engineutil ./engine/wcprof/... ./cmd/wcprof-otel-analyze ./cmd/wcprof-oracle ./hack/otlpdump` passed.
- `go test ./engine/server` passed.
- `git diff --check 107ebe5c0c..4d6987fdc2` passed.
- `go vet ./dagql ./core ./engine/engineutil ./engine/wcprof/...` reported only warnings that also reproduce at `107ebe5c0c`.
