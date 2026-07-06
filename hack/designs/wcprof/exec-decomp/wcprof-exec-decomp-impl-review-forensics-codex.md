# wcprof exec decomposition implementation review - Codex

Reviewed implementation worktree:
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-exec-decomp-impl-dea4c5e3-f1564aaf`

Commits reviewed: `463f05b0ba` and `a691bec084`, full diff from `c28d55ae7a`.

## Verdict

**READY TO MERGE for the exec-decomposition implementation**, modulo the already
deferred live-engine oracle. I found no product-code blocker against the converged
design: argv is captured before both engine shims, emitted as the same scalar JSON
array string on native and OTel, compiled through the shared `MetaID -> Op.Argv`
seam, and classified before gate/report/oracle without replay changes.

## Findings

### Ambient test-suite failure, not an exec-decomp regression

`go test ./core` fails on `TestEmitServiceStartProducesLoaderShape` because that
test still feeds the structural gate an unmarked in-memory SDK trace:
`core/otelprof_services_test.go:172-182`. I verified the same failure on pristine
`c28d55ae7a`, with the same fail-by-default completeness error, so this is not
introduced by exec decomposition. It is still a practical CI risk if `./core` is run
as a package test without an adjacent baseline fix.

The implementer's specific pre-existing-test claim is correct. On pristine
`c28d55ae7a`, `go test ./engine/engineutil -run
'^TestEmitExecSplitProducesLoaderShape$' -count=1` fails because
`CheckStructural` refuses an unstamped trace. The implementation's helper stamps
only test spans with `WcprofEngineSpanAttr` and a matching
`WcprofSessionSpanCountAttr` (`engine/engineutil/otelprof_test.go:84-100`) before
calling `wcotel.Compile` (`:220-229`). That does not weaken the product gate; the
gate still fails missing markers in product code (`engine/wcprof/wcotel/gate.go:165-172`).

### Low: argv0/argv1 are deliberately not strictly bounded

`boundProfArgv` never byte-truncates `argv[0]` or `argv[1]`
(`engine/engineutil/wcprof_argv.go:45-59`), and the test codifies that those two
tokens may exceed the total budget by themselves (`engine/engineutil/wcprof_argv_test.go:76-81`).
This matches the converged "never corrupt the group key" choice, but strictly
speaking means the OTel attr/native string is not absolutely bounded for adversarial
or malformed commands with huge first tokens. I do not consider this a merge blocker
because the design intentionally chose class faithfulness for the first two tokens;
if Erik wants a hard attribute-size ceiling, the clean follow-up is to classify from
the untruncated tokens while emitting a separately bounded display argv.

### Low: critical placement is code-reviewed, not directly exercised by an integration test

The unit tests are meaningful, but no test runs a real `Container.withExec` through
core and asserts `execMD.ProfArgs` is captured before QEMU and `/.init`. The code is
simple and correct, so I am not blocking on this, but the deferred live-engine oracle
should include a normal and, ideally, emulated/multi-arch exec.

## Contract Verification

### Capture and cache-key safety

`ExecutionMetadata.ProfArgs` is present and `json:"-"`:
`engine/engineutil/executor.go:75-87`. The tag is load-bearing. `withExec` accepts
`execMD` as a `dagql.SerializedString[*engineutil.ExecutionMetadata]`
(`core/schema/container.go:1444-1452`), and `SerializedString` marshals the struct
into the literal string (`dagql/types.go:667-689`). Without `json:"-"`, a run-time
profile field could become observable in serialized call inputs if ever populated
too early. For digested serialized strings, the call digest uses only the explicit
digest (`dagql/call/id.go:977-982`), but the tag is still the right structural guard.

The capture is correctly unconditional and before QEMU:
`core/container_exec.go:2038-2055`. `metaSpec.Args` is already the resolved user
command after entrypoint/default/arg expansion (`core/container_exec.go:1287-1298`
and `core/container.go:6859-6876`), and QEMU is prepended only after the clone
(`core/container_exec.go:2052-2056`). The executor `/.init` prepend is later in
`injectInit` (`engine/engineutil/executor_spec.go:364-375`). The core-to-executor
handoff is in-process: `engineClient.Run(..., execMD, ...)`
(`core/container_exec.go:2106-2118`) and `newExecState` shallow-copies that struct
before emit (`engine/engineutil/executor_spec.go:148-152`), so the JSON tag does not
drop the captured value before use.

### Emit and loader parity

The executor computes `profArgv` once and feeds the same slice to native and OTel:
`engine/engineutil/executor_spec.go:1411-1431` and `:1441-1444`. `execProfArgv`
is nil-safe, scrubs registered secrets, and then bounds the slice
(`engine/engineutil/wcprof_argv.go:17-34`). The scrub helper reuses the same censor
construction as stdout/stderr (`engine/engineutil/secret_scrub.go:32-94`).

Native marshals argv with `json.Marshal` and interns the scalar string as `MetaID`
(`engine/wcprof/record.go:96-110`, `:304-324`), including open ops
(`engine/wcprof/record.go:116-146`, `:204-221`; dump fields at
`engine/wcprof/dump.go:29-38`, `:41-59`, `:77-101`, `:128-139`). OTel stamps the
same scalar JSON array string on only the user `exec.processRun` phase
(`engine/engineutil/otelprof.go:102-128`) using `wcprof.exec.argv`
(`engine/telemetryattrs/attrs.go:90-99`). The loader maps that scalar string
verbatim into `DumpEvent.MetaID` (`engine/wcprof/wcotel/loader.go:373-385`,
`:397-416`), and `wcanalyze.Build` decodes `Op.Argv` only from that emitted string
(`engine/wcprof/wcanalyze/graph.go:150-162`, `:201-217`, `:243-252`). That preserves
the zero-inference principle.

### Classification and replay invariants

`ClassifyExecs` relabels only ops with explicit argv, using a deterministic
projection or the first matching user rule (`engine/wcprof/wcanalyze/classify.go:76-121`).
The default is `path.Base(argv[0])` plus `argv[1]` only when it is not a flag
(`:124-139`); no shell parsing is present. The Stage 2 `--exec-group` parser uses a
boundary-aware literal prefix by default and opt-in `contains:` (`:24-57`).

The replay and causal model are untouched: no diff in `replay.go`, `report.go`,
`gate.go`, `oracle.go`, or `dagql/cache.go`. The graph change is additive
`Op.Argv` plus `invalidateProgram` (`engine/wcprof/wcanalyze/graph.go:17-34`,
`:429-442`). All entry points classify before gate/report/oracle:
`cmd/wcprof-analyze/main.go:91-99`,
`cmd/wcprof-otel-analyze/main.go:152-169`, and
`cmd/wcprof-oracle/main.go:96-110`.

### Tests

The added tests are substantive, not vacuous:

- default projection, blob decomposition, memo reset, idempotence, no-argv boundary,
  and offline regrouping: `engine/wcprof/wcanalyze/classify_test.go:99-297`;
- OTel loader, gate-before-report ordering, Cloud-shaped scalar JSON survival,
  cross-source oracle parity, and cross-source regrouping:
  `engine/wcprof/wcotel/exec_decomp_test.go:113-299`;
- scrub/bounds/nil-safe emit helper: `engine/engineutil/wcprof_argv_test.go:15-111`;
- native recorder/dump `MetaID` round-trip:
  `engine/wcprof/record_argv_test.go:13-77`;
- real OTel emit helper shape and argv attr on `processRun` only:
  `engine/engineutil/otelprof_test.go:138-250`.

## Test Runs

Passed:

```text
go test ./engine/engineutil ./engine/wcprof ./engine/wcprof/wcanalyze ./engine/wcprof/wcotel ./cmd/wcprof-analyze ./cmd/wcprof-otel-analyze ./cmd/wcprof-oracle -count=1
go test ./core -run '^$' -count=1
```

Failed, pre-existing and not introduced by this feature:

```text
go test ./core -count=1
```

Failure: `TestEmitServiceStartProducesLoaderShape` lacks the completeness marker,
same as pristine `c28d55ae7a`.

## Summary

No merge-blocking exec-decomposition issues found. The implementation is faithful to
the converged design and preserves the governing principle: faithful emitted argv,
zero-inference loader/classifier, unchanged replay. Ranked issues: (1) ambient
pre-existing `./core` test failure, not introduced; (2) intentional argv0/argv1
strict-bound exception; (3) no direct integration test for the core capture placement.
