# wcprof exec decomposition implementation review (Codex fresh)

Reviewer: Codex fresh reviewer
Implementation reviewed: `/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-exec-decomp-impl-dea4c5e3-f1564aaf`
Commits reviewed: `463f05b0ba`, `a691bec084`
Base: `c28d55ae7a`

## Verdict

**Ready to merge, modulo the already-deferred live-engine oracle.** I found no correctness blocker and no silent divergence from the converged design. The implementation keeps replay/causal analysis unchanged, carries argv as explicit emitted data, classifies before gate/report/oracle use the memoized replay program, and preserves native/OTel parity through the shared `MetaID` seam.

## Findings

No real merge-blocking findings.

Non-blocking residual validation notes:

- The unit tests synthetic-check that per-command classes are not `/.init` or the QEMU shim, but they do not run a live emulated and non-emulated `Container.withExec` through the full core -> executor path. I do not consider this a blocker because the capture point is directly verified in code and the prompt already scoped the live-engine oracle as deferred.
- There is no dedicated test that JSON-serializing `ExecutionMetadata` excludes `ProfArgs` from cache-key material. The code is correct (`json:"-"`), and the cache-key serialization path is verified below; this is an optional regression guard, not a required fix.

## Core mechanism

The implementation follows the converged mechanism: `ClassifyExecs` relabels `op.Class`, and all replay/report aggregation continues to key through `op.Key()`.

- `Op.Argv` is additive IR metadata only, documented as explicit emitted argv and never inferred from span names in `engine/wcprof/wcanalyze/graph.go:17`.
- `op.Key()` still returns only `{Kind, Class}` at `engine/wcprof/wcanalyze/graph.go:428`, so relabeling `op.Class` is sufficient to re-bucket existing replay/report consumers.
- `ClassifyExecs` relabels only ops with `len(op.Argv) > 0`, derives the class from argv/rules, and invalidates the replay program memo at `engine/wcprof/wcanalyze/classify.go:94`.
- `invalidateProgram` resets `progOnce` and `prog` at `engine/wcprof/wcanalyze/graph.go:433`, which covers the ordering hazard if anything compiled the program before classification.
- CLI/oracle ordering is correct: native CLI classifies before report at `cmd/wcprof-analyze/main.go:91`, OTel CLI classifies before `CheckStructural` at `cmd/wcprof-otel-analyze/main.go:152`, and oracle classifies both graphs before gate/comparison at `cmd/wcprof-oracle/main.go:96`.
- Replay/report/gate causal logic is not changed; the only touched analysis files in that area are `graph.go` and `loader.go`, not `replay.go`, `report.go`, or `gate.go`.

## Emit placement and cache-key safety

The two critical placement fixes are implemented correctly.

- Core resolves command args before capture: `opts.Args` expansion happens at `core/container_exec.go:1254`, `metaSpec` is resolved at `core/container_exec.go:1295`, and `container.command` applies default cmd plus entrypoint at `core/container.go:6859`.
- `ProfArgs` is captured unconditionally before the QEMU prepend at `core/container_exec.go:2042`; the assignment is outside `if emu != nil`, so normal non-emulated `withExec` is covered too.
- The executor `/.init` prepend happens later in `engine/engineutil/executor_spec.go:368`, with the `NoInit` skip at `engine/engineutil/executor_spec.go:369`, so captured argv excludes `/.init` in both cases.
- `ExecutionMetadata.ProfArgs` is `json:"-"` at `engine/engineutil/executor.go:75`, which is load-bearing: `SerializedString.MarshalJSON` serializes `Self` at `dagql/types.go:671`, `containerExecArgs.ExecMD` is a `SerializedString` at `core/schema/container.go:1451`, and existing digesting paths use `NewDigestedSerializedString` at `core/sdk/go_sdk.go:423` and `core/sdk/module_typedefs.go:107`.
- The handoff remains in-process: core passes the same `execMD` pointer into `engineClient.Run` at `core/container_exec.go:2119`, and the executor copies it into state at `engine/engineutil/executor_spec.go:148`. The JSON exclusion prevents cache-key pollution without dropping data before emit.

## Native and OTel parity

Native and OTel carry the same scrubbed/bounded argv bytes through the same scalar JSON-array encoding.

- The emit site computes `profArgv` once when either native wcprof or OTel profiling is active at `engine/engineutil/executor_spec.go:1413`.
- Native records it only on the user `exec.processRun` phase at `engine/engineutil/executor_spec.go:1429`.
- OTel emits it only on the user `exec.processRun` span at `engine/engineutil/otelprof.go:78` and `engine/engineutil/otelprof.go:102`; never-started execs emit no processRun/argv.
- Native interns the JSON-array string into `MetaID` at `engine/wcprof/record.go:96` and stores it on recorded ops at `engine/wcprof/record.go:304`.
- The OTel loader maps the scalar attr verbatim into `MetaID` at `engine/wcprof/wcotel/loader.go:380`; `Build` decodes that same `MetaID` into `Op.Argv` at `engine/wcprof/wcanalyze/graph.go:150`.

This preserves the no-inference rule: malformed/absent argv decodes to nil and the op stays in the existing `exec.processRun` blob.

## Grouping and anti-inference

The default and offline grouping rules match the design.

- Default class is `path.Base(argv[0])` plus `argv[1]` only when it is not a flag at `engine/wcprof/wcanalyze/classify.go:133`, so `sh -c ...` remains `sh` unless the user supplies an explicit offline group rule.
- `--exec-group` uses a boundary-aware literal prefix by default and an explicit `contains:` mode for shell-wrapped commands at `engine/wcprof/wcanalyze/classify.go:29`.
- The parser splits on the first `=` and rejects empty match/label forms at `engine/wcprof/wcanalyze/classify.go:41`.

There is no shell parsing and no span-name parsing.

## Scrub and bounds

The scrub/bound path is sound and scoped to registered Dagger secrets.

- Resolved secret file paths are stashed for the profile argv scrubber at `engine/engineutil/executor_spec.go:846`.
- `execProfArgv` reuses `ScrubStrings` with process env, registered secret env names, and resolved secret file paths at `engine/engineutil/wcprof_argv.go:22`.
- On any scrub error, argv is omitted rather than emitted partially at `engine/engineutil/wcprof_argv.go:29`.
- Bounds preserve `argv[0..1]` and cap trailing tokens/bytes at `engine/engineutil/wcprof_argv.go:41`.

The honest limitation remains the one accepted in the design: literals the user baked directly into argv and not registered as Dagger secrets are already visible elsewhere and cannot be inferred as secrets here.

## Tests

The new tests are meaningful, and the focused package suite passes:

```text
go test ./engine/wcprof/... ./engine/engineutil -count=1
ok  	github.com/dagger/dagger/engine/wcprof
ok  	github.com/dagger/dagger/engine/wcprof/wcanalyze
ok  	github.com/dagger/dagger/engine/wcprof/wccloud
ok  	github.com/dagger/dagger/engine/wcprof/wcotel
ok  	github.com/dagger/dagger/engine/engineutil
```

Coverage highlights:

- Default class projection, memo reset, idempotent regrouping, and boundary-aware `--exec-group` are covered in `engine/wcprof/wcanalyze/classify_test.go:97`, `:171`, and `:275`.
- OTel loader, gate->report ordering, native/OTel oracle parity, Cloud-shaped scalar attr survival, and argv-less coverage boundary are covered in `engine/wcprof/wcotel/exec_decomp_test.go:110`, `:134`, `:169`, `:201`, and `:233`.
- Scrub/bound/nil behavior is covered in `engine/engineutil/wcprof_argv_test.go:12`, `:45`, and `:89`.
- The real emit fixture now asserts the `wcprof.exec.argv` attr on processRun, absence on containerStart, and compile-through-loader/gate at `engine/engineutil/otelprof_test.go:132`.

The pre-existing-test claim is confirmed. At pristine `c28d55ae7a`, `go test ./engine/engineutil -run TestEmitExecSplitProducesLoaderShape -count=1` fails because the structural gate rejects the in-memory trace for missing the session span-count declaration. The implementation fixes the fixture by stamping a faithful test-only completeness marker at `engine/engineutil/otelprof_test.go:84`, while the gate remains fail-by-default on missing markers at `engine/wcprof/wcotel/gate.go:169`.

## Summary

No blocker. The implementation is faithful to the converged design and preserves the governing principle: the loader/replay do not infer, and the replay algorithm is unchanged. The main remaining risk is the deferred live-engine oracle for real emulated/non-emulated execs, not a code issue found in this review.
