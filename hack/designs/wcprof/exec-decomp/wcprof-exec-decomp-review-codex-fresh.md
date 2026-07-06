# wcprof exec decomposition review - Codex fresh pass

## Verdict

The core idea is right: emit scrubbed argv as explicit data, keep it out of `Ident`, relabel only `Op.Class` offline, and let the existing `ClassKey{Kind,Class}` machinery do the rebucketing. That preserves the rational-model rule: the loader still maps fields, and grouping is a deterministic projection of emitted argv, not span-name parsing or shell inference.

I would not approve the design exactly as written. There is one blocker in the classify/replay ordering contract: current OTel gate and oracle paths call `g.program()` before the report/oracle, so a CLI that wires `ClassifyExecs` only "before WriteReport" can silently compute what-ifs with the old `exec.processRun` class buckets. Fix that before implementation. The rest is implementation tightening and validation scope.

## Findings

### BLOCKER: `ClassifyExecs` must invalidate the memoized replay program, or the OTel paths can silently ignore relabels

The design correctly observes that replay buckets by `op.Key()`: `compileProgram` records `key := op.Key()` into `p.classKeys`/`p.classOf` in `engine/wcprof/wcanalyze/replay.go:153-172`, and `op.Key()` reads `op.Class` in `engine/wcprof/wcanalyze/graph.go:405-408`. The catch is that `Graph.program()` memoizes this once with `progOnce` in `engine/wcprof/wcanalyze/replay.go:122-127`, and `NewSimulation` always reuses that cached program (`replay.go:332-356`).

The current OTel CLI calls the structural gate before the report: `cmd/wcprof-otel-analyze/main.go:135-143`. The gate itself runs `wcanalyze.NewSimulation(g, nil)` at `engine/wcprof/wcotel/gate.go:118-120`, which initializes `g.program()`. The oracle has the same ordering: `cmd/wcprof-oracle/main.go:76-84` gates the OTel graph before `wcotel.Oracle`, and `TopBottlenecks` uses `RunWhatIfs` at `engine/wcprof/wcotel/oracle.go:50-52`.

If classification happens after that gate, `AggregateClasses` will show the new classes because it calls `op.Key()` live (`report.go:30-38`), but the what-if simulation will still have the old `p.classOf` and old `p.classKeys`. Worse, `RunWhatIfs` computes candidate keys from the relabeled graph (`replay.go:678-709`) and then passes factors for those new keys into a simulation whose cached program only knows `exec.processRun`; the factors do not match (`NewSimulation` linearly matches factor keys against `p.classKeys`, `replay.go:350-355`). That can produce a plausible-looking report with zero or wrong savings for the new per-command classes.

Required fix: make `ClassifyExecs` reset the replay cache whenever it changes classes, e.g. inside `wcanalyze` set `g.prog = nil` and `g.progOnce = sync.Once{}`. Also wire classification immediately after load and before `CheckStructural`, `WriteReport`, and oracle comparison. The cache reset is still not a replay-algorithm change; it is the necessary invalidation for mutating `op.Class`, and it makes the doc's "idempotent under re-grouping" claim true even after a previous report/gate.

### MEDIUM: The scrub touchpoint needs a real data path for resolved secret file paths

The design says to reuse the existing stdout/stderr secret scrubber and pass `secretFilePaths` into `scrubArgv` from `executor_spec.go:1417`. The current resolved paths are local to `setupSecretScrubbing`: they are built in `engine/engineutil/executor_spec.go:858-869` and passed to `NewSecretScrubReader` at `executor_spec.go:875-882`. The native and OTel exec emit sites are later, at `executor_spec.go:1415-1430`; that local slice is not in scope there.

This is fixable, but the design should say how: store the resolved paths on `execState`, or factor the path-resolution logic into a helper used once by both stdout/stderr scrubbing and argv scrubbing. Also handle `state.execMD == nil` exactly like `setupSecretScrubbing` does (`executor_spec.go:842-848`). Avoid a naive `NewSecretScrubReader(strings.NewReader(tok), ...)` loop that reloads secret files and rebuilds the trie per argv token; bounds keep it from exploding, but a single scrubber/censor setup per exec is the cleaner implementation.

### MEDIUM: The `--exec-group='<match>=<label>'` grammar is under-specified for real argv

The no-glob, offline prefix rule is directionally correct, but matching `strings.Join(op.Argv, " ")` (`wcprof-exec-decomp-design.md:340-350`) is ambiguous for argv tokens containing spaces, and parsing the flag by an unqualified `=` is ambiguous for common args like `--flag=value`. This is grouping-policy ambiguity, not causal inference, but it can put a command in the wrong user group.

Recommended v1 tightening: define the separator rule explicitly, preferably split on the last `=`; and consider matching over a token-safe canonical form rather than a plain space-join. A NUL-joined internal match string mirrors the native wire encoding and avoids token-boundary ambiguity, though the CLI syntax must still be human-friendly. I would not add `contains:` in v1.

### LOW/MEDIUM: The validation plan overstates `DupExecuted` for user process ops

The design is right not to put argv in `Ident`: exec wait resolution indexes exec ops by `Ident` in `engine/wcprof/wcanalyze/graph.go:236-260`, and `Ident` is the per-invocation identity. But the doc's validation language around repeated identical execs and `DupExecuted` needs correction. `AggregateClasses` only counts duplicate executed idents when `op.Outcome == "executed"` or `op.Kind == "call_exec"` (`engine/wcprof/wcanalyze/report.go:50-57`). The user `exec.processRun` op is `OpKindExecPhase` and is recorded with outcome `ok`/`error` at `engine/engineutil/executor_spec.go:1411-1417`, so repeated user-process execs do not currently contribute to `DupExecuted`.

Preserving `Ident` still prevents regressions, but tests should assert argv round-trip, per-command class rebucketing, and optional drill-down counts. Do not make a test expect `DupExecuted` for `exec_phase` unless the outcome semantics are explicitly changed.

### LOW: Make argv decoding robust across both OTel front ends

Local otlpdump renders OTLP arrays as `[]any` (`hack/otlpdump/main.go:72-77`), and Cloud unmarshals GraphQL `attributes` into `map[string]any` (`internal/cloud/trace.go:81-94`, `trace.go:171-180`), so real Cloud arrays should also arrive as `[]any`. Still, the `attrStrSlice` helper should accept both `[]any` and `[]string`; the Cloud test fakes already construct `cloud.SpanData` directly (`engine/wcprof/wccloud/cloud_test.go` style), and direct Go fixtures naturally use `[]string`. Malformed mixed arrays should be treated as absent or surfaced in a test; do not coerce non-strings.

## Non-Issues / Noise

- Keeping argv out of `Ident` is correct. `Ident` participates in exec wait resolution (`graph.go:236-260`) and duplicate bookkeeping (`report.go:50-57`); argv belongs in a separate `Op.Argv`.
- Keeping argv out of emitted `Class` is correct. Offline re-grouping is only possible if the raw argv survives and classification runs in the analyzer.
- Rejecting overlapping op-sets is correct for this feature. Current replay is partitioned by `ClassKey`, and a one-class-per-op relabel gets the goal without changing `replay.go`.
- Refusing to parse `sh -c` is correct. Shell parsing would infer semantics from an argument string. The honest default is `sh`; user grouping rules are the right non-inferential override.
- Adding a string-slice attribute to the existing OTel `exec.processRun` span should not affect the completeness checksum, because it does not add spans. The OTel phase span is already emitted at `engine/engineutil/otelprof.go:99-117`.

## Open Decisions

1. Default class shape: choose `basename(argv[0]) + argv[1] only when argv[1] is present and not a flag`. Do not scan later args or parse flag arity; that becomes command-specific inference. Commands like `go -C dir build` can use an explicit rule.

2. Grouping syntax: keep a repeatable literal prefix flag, but specify parsing and token ambiguity before implementation. I recommend no `contains:` for v1, split the rule on the last `=`, and prefer a token-safe match representation internally.

3. `sh -c`: accept default `sh`; no built-in shell parsing and no curated wrapper rules. Stage 2 rules should be available soon, because wrapper-heavy real CI traces will otherwise still headline as `sh`.

4. Secret redaction: ship registered-secret scrubbing plus bounds only. Heuristic redaction of `--password=*` risks destroying grouping while still not proving safety. Keep the scope honest: sensitive `dag.call` args are already redacted in the call encoder test path (`dagql/result_call_frame_test.go:492-527`), but literal user-baked secrets that are not modeled as Dagger secrets can still appear in existing telemetry; argv should not claim to solve that.

5. Native wire encoding: interned NUL-joined `MetaID uint32` is fine if `0` means absent. The recorder string table reserves ID 0 for empty string (`engine/wcprof/wcprof.go:430-435`), so old dumps can keep `MetaID == 0` and execs stay blobbed. I would not bump `DumpSchemaVersion` for this additive JSON field; old analyzers will ignore it, and new analyzers can read old dumps.

6. Bounds: 64 tokens / 256 bytes per token / 4 KiB total is reasonable. Preserve `argv[0]` and immediate `argv[1]` whenever possible, since the default classifier depends on them. Use an ASCII sentinel in code, e.g. `"...(+N more)"`.

## Recommendation

Sound enough to implement in 1-2 stages after fixing the replay-cache ordering contract. Stage 1 delivers real value for direct execs (`go build`, `git clone`, `npm install`) with no replay change. Stage 2 is important for wrapper-heavy workloads (`sh -c ...`) and should not drift far behind, but it can be a thin CLI layer over the same `ClassifyExecs` pass.

Must change first:

1. `ClassifyExecs` must invalidate `g.program()` after relabeling, and the CLIs/oracle must classify before structural gate/report/oracle simulation.
2. The scrub design must expose resolved secret file paths safely to argv scrubbing or share a path-resolution helper.
3. The grouping-rule parser must define how `=` and token boundaries work before shipping Stage 2.

---

# Round 2 review

## Verdict

Most R1 fixes converge. The design now correctly fixes `/.init`, gate ordering, scalar argv encoding, scrub plumbing, `DupExecuted` framing, and the empty-argv predicate.

I found one remaining blocker in the same family as the `/.init` issue: cross-platform execs can have the Dagger QEMU emulator prepended before `engineutil.Client.Run` is called, so the proposed Run-entry capture still records an engine shim as `argv[0]`. Fix the capture seam to preserve the pre-QEMU argv too. After that, the design is sound to implement in the proposed 1-2 stages.

## R2 Findings

### BLOCKER: Run-entry capture fixes `/.init`, but not the earlier QEMU emulator prepend

The revised B1 fix is correct for `/.init`: `engine/engineutil/executor.go:100-114` constructs `state`, then `c.run` starts at `executor.go:143`; `injectInit` is one of the setup funcs at `executor.go:145`, and it prepends `/.init` at `engine/engineutil/executor_spec.go:364-375`. `NoInit` is also correctly covered: `injectInit` returns before the prepend when `state.execMD.NoInit` is true (`executor_spec.go:365-366`), and the proposed capture before `c.run` works either way.

The doc's entrypoint claim is also correct for normal withExecs: `core/container_exec.go:316-333` builds `executor.Meta.Args` from `container.command`, and `core/container.go:6863-6870` applies the configured entrypoint before the engine call. Service args do the same class of explicit command construction before `Run` (`core/container.go:6741-6755`, then `core/service.go:839-849` / `core/service.go:1011-1031`).

But there is another engine shim before `engineutil.Client.Run`: the QEMU emulator path. `core/container_exec.go:2038-2044` prepends `engineutil.DaggerQemuEmulatorMountPoint` to `metaSpec.Args` before `meta := *metaSpec` (`core/container_exec.go:2051`), `procInfo := executor.ProcessInfo{Meta: meta}` (`core/container_exec.go:2088`), and the actual `engineClient.Run` call (`core/container_exec.go:2106-2114`). The constant is `/dev/.dagger_qemu_emulator` (`engine/engineutil/executor_spec.go:73`). Therefore the proposed `state.profRawArgs = slices.Clone(procInfo.Meta.Args)` at Run entry still captures `argv[0] == "/dev/.dagger_qemu_emulator"` on emulated cross-platform execs.

This is the same correctness class as `/.init`: a Dagger-injected launcher becomes the headline instead of the user's command. The fix should move the "profile argv" seam before the QEMU prepend, or carry a separate profile-only argv alongside the runtime argv. For example, set a `ProcessInfo.ProfileArgs` or equivalent from `metaSpec.Args` before `core/container_exec.go:2043`, then have `engineutil.Run` prefer that for `state.profRawArgs`. Stripping only `/.init` inside `Run` is insufficient.

### CONFIRMED: Classify-before-gate plus memo invalidation fixes B2

The revised design correctly identifies the old stale-program path. `cmd/wcprof-otel-analyze/main.go:135-143` runs `CheckStructural` before `WriteReport`, and `CheckStructural` calls `wcanalyze.NewSimulation(g, nil)` at `engine/wcprof/wcotel/gate.go:118-120`, which reaches `g.program()` (`engine/wcprof/wcanalyze/replay.go:122-127`, `replay.go:332-356`). So classification must occur before the gate, not only before reporting.

Classifying before the gate is safe: the gate reports topology/timing/completeness facts and baseline replay diagnostics (`wcotel/gate.go:100-150`), while class labels only affect replay factors and aggregation keys. A nil-factor baseline simulation compiles class labels but does not make gate validity depend on their values.

Resetting `g.progOnce` and `g.prog` after relabeling is sufficient for the analyzer path. The stale class map lives in `replayProgram.classKeys/classOf` built by `compileProgram` (`replay.go:153-172`). `AggregateClasses` reads `op.Key()` live (`engine/wcprof/wcanalyze/report.go:30-38`), and op self segments are class-independent. There is no concurrency hazard in the CLI/oracle flow as long as `ClassifyExecs` is run before any parallel what-if simulations; the helper should document that graph relabeling is not concurrent with simulation.

### CONFIRMED: Scalar JSON-array string is the right encoding for B3

The revised carrier is sounder than the R1 string-slice plan. Cloud's front end preserves attributes as `map[string]any` without translation (`engine/wcprof/wccloud/cloud.go:52-60`), and the loader's scalar helpers already accept strings (`engine/wcprof/wcotel/loader.go:555-562`). Encoding argv as a JSON array in one scalar string therefore rides the same proven path as the existing decimal-string wcprof attributes.

Native and OTel parity is plausible by construction if the implementation does exactly what the doc says: scrub/bound once, then use `json.Marshal` over the same `[]string` for both the native `MetaID` and `wcprof.exec.argv`. `json.Marshal([]string)` is deterministic, avoids NUL/control-byte storage risk, and does not interfere with token bounds because bounding happens before encoding.

Implementation detail to preserve: `MetaID == 0` must mean absent. The wcprof string table reserves ID 0 for empty string (`engine/wcprof/wcprof.go:430-435`), so old dumps and argv-less execs can stay blobbed. If `DumpOpenOp.MetaID` is added, `Build` should populate `Op.Argv` for open ops too (`engine/wcprof/wcanalyze/graph.go:216-233`), even though current `processRun` is emitted only after completion (`engine/engineutil/executor_spec.go:1415-1430`).

### CONFIRMED WITH SMALL IMPLEMENTATION NOTE: Scrub plumbing is now specified

The doc now explicitly stashes resolved secret file paths from `setupSecretScrubbing`, which is the right data path. The existing path resolution is local to `setupSecretScrubbing` (`engine/engineutil/executor_spec.go:842-873`) and runs before `runContainer` (`engine/engineutil/executor.go:153` before `executor.go:159`), so storing `state.profSecretFilePaths` makes the same resolved, stat-filtered files available at the emit site (`executor_spec.go:1405-1430`).

Implementation note: the later scrub call must handle `state.execMD == nil` without touching `state.execMD.SecretEnvNames`. The current setup function returns early for nil metadata (`executor_spec.go:842-848`); argv scrubbing should mirror that with an empty secret set.

### CONFIRMED: `DupExecuted` and empty-argv framing are corrected

The doc now states the right current behavior. `DupExecuted` only counts `Outcome == "executed"` or `Kind == "call_exec"` (`engine/wcprof/wcanalyze/report.go:50-57`), while user process runs are `exec_phase` and `ok`/`error` (`engine/engineutil/executor_spec.go:1411-1417`). Keeping duplicate detection on the call side is accurate.

The one-directional `len(op.Argv) > 0 => relabelable user-process op` predicate is also correct. The never-started path emits only `exec.containerStart` natively (`executor_spec.go:1415-1420`) and in OTel (`engine/engineutil/otelprof.go:81-92`), so there is no bogus argv-less processRun to classify.

### LOW: Grouping grammar is acceptable, with known explicit limitations

Boundary-aware prefix fixes the `go buil`/`go buildx` class of accidental matches. The optional `contains:` rule is not causal inference because it is an explicit user-supplied grouping policy over emitted argv, not a loader/replay guess. The remaining limitations are now documented: a pattern containing literal `=` is not expressible with split-on-first-`=`, and matching over `strings.Join(argv, " ")` is still token-ambiguous for args containing spaces. I do not consider either a blocker for Stage 2 as long as tests cover the boundary behavior and the `contains:` shell-wrapper case.

## R2 Recommendation

Fix the QEMU profile-argv seam before implementation. With that correction, I confirm convergence: the revised design keeps analysis zero-inference, preserves native/OTel parity, leaves replay algorithmically untouched, and is simple enough for the proposed Stage 1/Stage 2 split.

One-line verdict: **BLOCKER: QEMU/emulator prepend happens before the proposed Run-entry argv capture; carry a separate pre-QEMU profile argv or move the capture earlier.**

---

# Round 3 review

## Verdict

CONVERGED. The B4 revision moves the profile argv seam to the right layer: core already has the fully resolved user command, and this is before both the core QEMU prepend and the executor `/.init` prepend. I found no remaining blocker in the one-delta scope.

## R3 Findings

### CONFIRMED: `execMD.ProfArgs` capture resolves the QEMU blocker and subsumes `/.init`

The proposed capture point is after the user command has been resolved. `ContainerExecState.Evaluate` expands `opts.Args` before metadata creation (`core/container_exec.go:1254-1263`), then `container.metaSpec(ctx, opts, true)` builds `executor.Meta.Args` from `container.command(opts)` (`core/container_exec.go:1295-1298`). `container.command` applies default args and optionally prepends the container entrypoint (`core/container.go:6859-6876`), and `metaSpec` stores that resolved command in `metaSpec.Args` (`core/container_exec.go:316-333`).

It is also before both shims. The QEMU emulator is prepended only later at `core/container_exec.go:2038-2044`, before `meta := *metaSpec` and the `engineClient.Run` call (`core/container_exec.go:2051, 2088, 2106-2114`). The executor `/.init` shim is still below that, added by `injectInit` at `engine/engineutil/executor_spec.go:364-375` after `Run` has constructed `state` (`engine/engineutil/executor.go:100-114`) and entered `c.run` (`executor.go:143-159`). `NoInit` remains covered because `injectInit` returns before prepending when `state.execMD.NoInit` is true (`executor_spec.go:365-366`).

So `if execMD != nil { execMD.ProfArgs = slices.Clone(metaSpec.Args) }` immediately before `core/container_exec.go:2043` gives normal and emulated `Container.withExec` the real command, including the container entrypoint when requested, and excluding both `/dev/.dagger_qemu_emulator` and `/.init`.

### CONFIRMED: cache-key neutrality holds under the specified timing

The load-bearing point is timing. The withExec call digest is computed before evaluation: `execMeta` fills `execMD.CallDigest` from `curCall.RecipeDigest(ctx)` at `core/container_exec.go:242-248`, and the lazy state stores that metadata when the call is constructed (`core/container_exec.go:1160-1176`). The explicit `execMD` selector digests in the SDK/runtime paths are also constructed before the lazy exec runs, e.g. `dagql.NewDigestedSerializedString(&execMD, goSDKExecMDDigest)` at `core/sdk/go_sdk.go:423-424` and the module-types equivalent at `core/sdk/module_typedefs.go:99-108`.

The B4 write happens during lazy evaluation, after `execMeta` and `metaSpec` are rebuilt for the run (`core/container_exec.go:1287-1298`) and before `engineClient.Run` (`core/container_exec.go:2106-2114`). I did not find a re-serialization/re-digest of `execMD` after that point in the run path. On successful evaluation, `container.Lazy` is cleared (`core/container_exec.go:2188`), so the `ContainerExecLazy.EncodePersisted` path that serializes `lazy.State.ExecMD` (`core/container_exec.go:157-180`) is not the normal post-run persisted representation.

Non-blocking implementation guard: keep `ProfArgs` profile-only. The cleanest way to make the neutrality mechanically obvious is to give the field `json:"-"` or otherwise ensure it is overwritten/cleared and never intentionally used as a schema/persisted input. The design's timing argument is enough for convergence, but a normal serialized field would be easier to misuse later because `ExecutionMetadata` is used in digested serialized strings.

### CONFIRMED: the coverage boundary is honest

Stage 1 covers the core `Container.withExec` path. That is the path that resolves `metaSpec.Args`, applies the QEMU prepend, and calls `engineClient.Run` (`core/container_exec.go:1295-1298, 2038-2044, 2106-2114`). Direct service starts also call the executor (`core/service.go:708-715, 833-856`) but do not flow through this B4 capture point; Dockerfile/frontend execs likewise bypass it. With no `ProfArgs`, both native and OTel keep empty `Op.Argv` and stay aggregated as `exec.processRun`, which is the pre-feature behavior and not a shim mislabel.

That boundary is acceptable for the stated v1 goal because the headline target is user `Container.withExec` work (`go build`, `npm install`, `git clone`). Extending decomposition to services or Dockerfile `RUN` later should use the same explicit-argv pattern at their own arg-resolution sites, not an analyzer fallback.

### CONFIRMED: minor R2 folds are correctly specified

The doc now uses the real `json.Marshal` form and omits argv on marshal error, guards empty `MetaID` before unmarshal, keeps scrub nil-safe for `execMD == nil`, avoids a `DumpSchemaVersion` bump, and states bounds on the raw scrubbed slice before JSON encoding. I did not find a new inconsistency in those folds.

Interactive note: the only `InteractiveCommand` rewrite I found is in the terminal-error path (`core/container_exec.go:1847-1872`), not the normal profiled process run. It does not undermine the B4 capture for the actual `exec.processRun` being decomposed.

## R3 Recommendation

Proceed with the 1-2 stage implementation. The one implementation detail I would explicitly carry into code review is to make `ProfArgs` non-digest/non-persisted by construction if feasible (`json:"-"`), or add a test proving it cannot change the serialized `execMD` cache key. That is a hardening note, not a design blocker.

One-line verdict: **CONVERGED: B4's `execMD.ProfArgs` capture before QEMU closes the remaining shim blocker; no new blocker found.**
