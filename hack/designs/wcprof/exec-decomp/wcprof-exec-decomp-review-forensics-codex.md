# wcprof exec decomposition review - Codex

Review target:
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-exec-decomp-design-63001337-87e98616/hack/designs/wcprof-exec-decomp-design.md`

Landed code checked in the same worktree at HEAD `c28d55ae7a`.

## Verdict

The core architecture is the right one: emit explicit argv on the user
`exec.processRun` op, load it as data, and do an offline `op.Class` relabel before
the existing replay/report/oracle run. The landed code confirms the central bet:
`Op.Key()` is exactly `{Kind, Class}` (`engine/wcprof/wcanalyze/graph.go:405-407`),
`AggregateClasses` groups by `op.Key()` (`engine/wcprof/wcanalyze/report.go:30-40`),
and replay memoizes class indices from `op.Key()` in `compileProgram`
(`engine/wcprof/wcanalyze/replay.go:153-172`). No replay/model change is needed.

But the design is not safe to implement as written. I found two load-bearing
correctness holes that must be fixed in the plan/implementation before coding:

1. The cited argv source is post-`dagger-init`, not the user command.
2. The OTel structural gate currently calls replay before the report, so
   `ClassifyExecs` must run before the gate or it silently loses to `progOnce`.

With those fixed, the feature is sound enough for a 1-2 stage implementation.

## Findings

### BLOCKER: `state.procInfo.Meta.Args` at the emit site is not the user argv

The design says both native and OTel should read `state.procInfo.Meta.Args` at
`engine/engineutil/executor_spec.go:1417` and `:1430`. In current code, that slice
has already been mutated by `injectInit`: unless `NoInit` is set,
`state.procInfo.Meta.Args = append([]string{"/.init"}, state.procInfo.Meta.Args...)`
(`engine/engineutil/executor_spec.go:364-375`).

The setup order proves this mutation precedes the process run and the later emit:
`Client.Run` calls `injectInit` before `generateBaseSpec` and `runContainer`
(`engine/engineutil/executor.go:143-160`), and the native/OTel processRun emits
happen only after `c.callWithIO` returns (`engine/engineutil/executor_spec.go:1405-1430`).

The true user command exists earlier: `container.command` returns the final user
command or errors on empty (`core/container.go:6859-6876`), `metaSpec` puts it in
`executor.Meta.Args` (`core/container_exec.go:323-334`), and `Client.Run` receives
that `procInfo` by value (`engine/engineutil/executor.go:76-103`). After
`injectInit`, the runtime OCI argv is the wrapper argv, not the user grouping key.

Impact: with the design as written, normal execs would default-class as `.init`
instead of `go build`, `npm install`, etc. That defeats the feature on the default
path.

Required change: preserve a cloned pre-init user argv on `execState` at creation
time, scrub/bound that preserved argv, and use it for both native `OpOpts.Argv`
and OTel `wcprof.exec.argv`. Add a test with default `NoInit=false`; a test with
`NoInit=true` alone would miss this.

### HIGH: `ClassifyExecs` must run before `CheckStructural`, not just before report

The ordering constraint in §4.4 is real. `g.program()` is memoized by `progOnce`
(`engine/wcprof/wcanalyze/replay.go:122-126`) and captures `classOf` from
`op.Key()` (`engine/wcprof/wcanalyze/replay.go:153-172`). `NewSimulation` is the
entry point that triggers it (`engine/wcprof/wcanalyze/replay.go:330-334`).

The current OTel analyzer calls the structural gate before the report:
`runFiles` loads then calls `analyze` (`cmd/wcprof-otel-analyze/main.go:93-97`);
`analyze` calls `wcotel.CheckStructural` before `WriteReport`
(`cmd/wcprof-otel-analyze/main.go:135-144`). The gate itself calls
`wcanalyze.NewSimulation(g, nil)` (`engine/wcprof/wcotel/gate.go:94-120`).

Same issue in the oracle CLI: it loads OTel, runs the OTel gate, then calls
`wcotel.Oracle` (`cmd/wcprof-oracle/main.go:71-84`).

If an implementation follows the doc's "before `WriteReport`" wording but places
classification after the gate, the replay program is already memoized with
`exec.processRun`. Later relabeling changes `AggregateClasses`, but what-if
simulation uses stale class indices. For new command keys, the factor map will not
match the old `p.classKeys`, so command what-ifs can be silently ignored or
mis-bucketed.

Required change: wire `ClassifyExecs` immediately after every load/build and
before any `CheckStructural`, `WriteReport`, `TopBottlenecks`, `Oracle`, or direct
`RunWhatIfs`. The OTel gate is included in "before first `g.program()`".

Strong recommendation: make `ClassifyExecs` defensive inside `wcanalyze` by either
clearing `g.prog`/`g.progOnce` when it mutates classes, or detecting that the
program has already been built and failing loudly. The current design advertises
idempotent regrouping; without a cache reset or guard that is only true before any
simulation has run.

### MEDIUM: scrub plumbing needs an explicit state boundary

Reusing the existing scrubber is the right policy, but the exact data flow in §5
does not exist in current code. The resolved `secretFilePaths` slice is local to
`setupSecretScrubbing` (`engine/engineutil/executor_spec.go:842-881`), while the
processRun emit happens later in `runContainer` (`engine/engineutil/executor_spec.go:1405-1430`).
The design says to pass `secretFilePaths` at the emit site, but that variable is
out of scope there.

Required implementation detail: build one scrub context from the same env,
secret env names, and resolved secret file paths used for stdout/stderr
scrubbing, then store the scrubbed/bounded pre-init argv on `execState`. Avoid
recomputing from files after the process runs; that can diverge from the stdout
scrub set and introduces late I/O/error behavior on the emit path.

The policy itself is sound: `NewSecretScrubReader` loads registered env/file
secrets and censors exact byte sequences (`engine/engineutil/secret_scrub.go:19-58`).
Scrub before bounding so a long secret is not truncated into an unrecognizable
partial. The known residual - user-baked literal secrets not registered as Dagger
secrets - is acceptable because `dag.call` already carries call payloads
(`core/telemetry.go:83-96`).

### LOW: `DupExecuted` should not be part of the Stage 1 success claim

The design correctly says `Ident` must not be overloaded. Keeping exec `Ident` as
the execution/call identity preserves wait resolution (`engine/wcprof/wcanalyze/graph.go:236-260`).

But the validation plan's "repeated identical exec" should not expect existing
`DupExecuted` to light up for user process phases. `AggregateClasses` only counts
dups for `Outcome == "executed"` or `Kind == "call_exec"`
(`engine/wcprof/wcanalyze/report.go:50-57`); native processRun emits `OutcomeOK`
(`engine/engineutil/executor_spec.go:1411-1419`), and the OTel loader maps a
normal processRun success to `ok` (`engine/wcprof/wcotel/loader.go:514-524`).

This is not a blocker for per-command ranking. If duplicate command drill-down is
desired, make it an additive argv drill-down, not an assumption about existing
`DupExecuted`.

### LOW: prefix grouping should avoid accidental string-prefix bleed

The proposed `strings.Join(argv, " ")` prefix rule is simple and uses explicit
data, so it does not violate the no-inference principle. It does lose token
boundaries: `go build` also prefixes `go buildx`, and args containing spaces can
collide in the joined representation.

This is not a replay/model issue. For Stage 2, prefer a boundary-aware rule over
the joined display string, e.g. parse the match into tokens and require token
prefix equality, or require that a string prefix ends at an argv boundary. Keep
`contains:` out of v1 unless there is a real `sh -c` UX need after default use.

### LOW: validation needs one real complex trace, not only a toy workload

The synthetic workload in §6 is good for deterministic assertions, and the
cross-source oracle is the right parity check. It will not catch every production
wire/ordering mistake by itself. The `/.init` bug above is exactly the kind of
issue a realistic default-`NoInit` run would catch.

Before merge, add a real module/CI-ish trace with multiple concurrent execs,
default init enabled, and both local OTel/native sources. Assert:

- no residual top-level `exec_phase:exec.processRun` for user process ops with argv,
- no `.init`/`/.init` command class dominates,
- native and OTel command `ClassKey`s match under the same rules,
- Cloud/local preserves the string-slice argv attr at least once.

## Confirmed Sound

- **Core relabel mechanism:** sound. `Build` fills `Class` from emitted strings
  without derivation (`engine/wcprof/wcanalyze/graph.go:180-195`), `Op.Key()` reads
  `Class` (`engine/wcprof/wcanalyze/graph.go:405-407`), `AggregateClasses` and
  `RunWhatIfs` consume `op.Key()` (`engine/wcprof/wcanalyze/report.go:30-40`,
  `engine/wcprof/wcanalyze/replay.go:678-681`).
- **Replay unchanged:** sound, if classification happens before memoization.
  No overlapping op-set machinery is needed; rejecting it is the right call.
- **Loader remains zero-inference:** sound. Native would split a carried meta
  string; OTel would read a carried string-slice attr. The loader should not parse
  names or infer commands.
- **`sh -c` refusal:** correct. Parsing `argv[2]` to recover `go build` would be
  shell inference. Defaulting to `sh` and allowing an explicit user grouping rule
  preserves the principle.
- **Argv placement:** `Op.Argv` is the right field. Do not put argv in `Ident`
  because wait resolution indexes exec ops by ident (`engine/wcprof/wcanalyze/graph.go:236-260`);
  do not bake it into emitted `Class` because offline regrouping would be lost.
- **Native/OTel parity:** feasible. Native and OTel emit sites are adjacent and can
  use the same preserved scrubbed argv (`engine/engineutil/executor_spec.go:1415-1430`).
  Local otlpdump preserves OTLP array attrs as `[]any`
  (`hack/otlpdump/main.go:62-78`), and the Cloud front-end carries attrs as
  `map[string]any` (`internal/cloud/trace.go:81-92`) into the same loader
  (`engine/wcprof/wccloud/cloud.go:52-62`). Add tests for both wire shapes.
- **Scope/staging:** Stage 1 default grouping and Stage 2 user rules are a clean
  split. Stage 1 is independently valuable once the two blockers are fixed.

## Open Decision Recommendations

1. **Default class shape:** choose `basename(argv[0])` plus `argv[1]` when
   `argv[1]` exists and is not a flag. Do not shell-parse or try to understand
   flag arity in v1.
2. **Grouping syntax:** `--exec-group='<match>=<label>'` is fine. Keep literal
   prefix only for v1, but make the matching boundary-aware rather than raw
   `strings.Join` prefix if it is cheap.
3. **`sh -c`:** accept default class `sh`; no curated built-in shell parsing.
   Users can add an explicit rule for wrapper-heavy traces.
4. **Secret redaction:** registered-secret scrubbing plus bounds only. Do not add
   heuristic redaction by default; it is neither complete nor free of grouping
   damage.
5. **Native wire:** interned NUL-joined `MetaID` is acceptable and compact. JSON
   can carry NUL as `\u0000`, and Linux argv tokens cannot contain NUL. Do not bump
   `DumpSchemaVersion` unless the reader is made compatible with old v1 dumps;
   current `ReadDump` rejects version mismatches
   (`engine/wcprof/dump.go:169-178`), so an additive `omitempty` field is safer.
6. **Bounds:** 64 tokens / 256 bytes per token / 4 KiB total is reasonable for v1.
   Scrub first, then bound. Use an ASCII sentinel in code.

## Verification Run

Focused current-code test pass:

```text
go test ./engine/wcprof/wcanalyze ./engine/wcprof/wcotel ./engine/wcprof/wccloud ./cmd/wcprof-analyze ./cmd/wcprof-otel-analyze ./cmd/wcprof-oracle -count=1
```

Result: all passed.

---

# Round 2 Review - Codex

Review target remains:
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-exec-decomp-design-63001337-87e98616/hack/designs/wcprof-exec-decomp-design.md`

Scope: R1 deltas only. I did not re-litigate the already-validated relabel
architecture.

## Verdict

CONVERGED. The revised design fixes the two real blockers from R1 and the scalar
JSON-array encoding is the right production-path adjustment. I found no remaining
blocker to implementing this in 1-2 stages.

There are three non-blocking implementation constraints to carry into coding:

- do not bump `DumpSchemaVersion` unless `ReadDump` accepts both old and new
  versions;
- make the argv scrub path nil-safe for manually constructed `execState`s and, if
  the goal is exact reuse of stdout/stderr's secret set, stash loaded secrets/trie
  rather than only paths;
- fix the doc's empty-argv example: current code rejects empty resolved commands,
  so argv-less `processRun` is a defensive case, not a normal default-CMD case.

## R1 Blocker Fixes

### B1 `/.init` Shim: fixed

The proposed capture point is correct. `Client.Run` receives `procInfo` by value,
validates it, constructs `state := newExecState(..., &procInfo, ...)`, and only
then calls `c.run` with setup funcs (`engine/engineutil/executor.go:76-114`,
`:143-160`). `injectInit` is one of those setup funcs and prepends `/.init` later
(`engine/engineutil/executor.go:145`; `engine/engineutil/executor_spec.go:364-375`).
The processRun native and OTel emits happen still later after `callWithIO`
(`engine/engineutil/executor_spec.go:1405-1430`).

So `state.profRawArgs = slices.Clone(procInfo.Meta.Args)` immediately after
`newExecState` captures pre-shim argv and excludes `/.init`. The `NoInit` case is
also covered: `ExecutionMetadata.NoInit` skips `injectInit`
(`engine/engineutil/executor.go:72-73`;
`engine/engineutil/executor_spec.go:364-367`), but the same capture remains the
right command.

The captured argv keeps the container entrypoint when the user requested it:
`container.command` starts from `opts.Args` or image `Cmd`, prepends
`cfg.Entrypoint` when `opts.UseEntrypoint` is true, and errors if the resolved
command is empty (`core/container.go:6859-6876`); `metaSpec` stores that resolved
command in `executor.Meta.Args` before `Client.Run` sees it
(`core/container_exec.go:322-334`). Service construction has the same upstream
entrypoint handling in `AsService` (`core/container.go:6731-6764`).

I searched for late `Meta.Args` mutations in the relevant engine/core paths. The
only `Client.Run` mutation that should be excluded is the `/.init` prepend
(`engine/engineutil/executor_spec.go:375`). `installCACerts` has a separate
manual `execState` path (`engine/engineutil/executor_spec.go:1092-1139`), discussed
below under scrub plumbing.

### B2 Gate Ordering: fixed

The trap is real and the revised ordering is right. `Graph.program()` memoizes the
compiled replay program via `progOnce` (`engine/wcprof/wcanalyze/replay.go:122-126`);
`compileProgram` snapshots class indices from `op.Key()`
(`engine/wcprof/wcanalyze/replay.go:153-172`). The OTel gate is currently the first
simulation caller: `CheckStructural` calls `wcanalyze.NewSimulation(g, nil)`
(`engine/wcprof/wcotel/gate.go:94-120`), and `NewSimulation` calls `g.program()`
(`engine/wcprof/wcanalyze/replay.go:330-334`).

The revised insertion points cover all entry points:

- native CLI: after `LoadMulti`, before `WriteReport`
  (`cmd/wcprof-analyze/main.go:73-77`);
- OTel CLI: first line of `analyze`, before `CheckStructural` and `WriteReport`
  (`cmd/wcprof-otel-analyze/main.go:135-144`);
- oracle: both graphs classified before the OTel gate and before `Oracle`
  (`cmd/wcprof-oracle/main.go:61-84`).

The gate verdict is class-independent. It uses class indices only because the
baseline replay program always has them; with nil factors, every `factorOf` entry
is 1 (`engine/wcprof/wcanalyze/replay.go:347-350`), and scheduling advances by
duration times that factor (`engine/wcprof/wcanalyze/replay.go:448-490`). The hard
gate checks are structural/timing/provenance: unresolved waits, malformed timings,
orphan parents, missing spans, cycles, unschedulable ops, self > makespan, interval
> trace span (`engine/wcprof/wcotel/gate.go:96-180`). None depends on label text.

Resetting `g.progOnce = sync.Once{}` and `g.prog = nil` is sufficient because the
only stale class state is in the replay program. `selfSegments` are independent of
class labels. This is safe for the intended offline single-threaded use; do not
mutate/classify a graph concurrently with an active simulation.

### B3 Scalar JSON-Array Encoding: fixed

The scalar JSON-array string is the right correction. Cloud's front-end maps
`cloud.SpanData.Attributes map[string]any` straight into the loader `Span.Attrs`
(`engine/wcprof/wccloud/cloud.go:24-62`), and `internal/cloud` receives the
GraphQL `attributes` field as `map[string]any` (`internal/cloud/trace.go:26-45`,
`:81-92`, `:152-180`). The loader already has scalar string handling via
`attrStr` (`engine/wcprof/wcotel/loader.go:555-562`), and existing wcprof
Cloud-critical numbers are deliberately string encoded.

Routing both native and OTel through the same scalar JSON string and then the same
`DumpEvent.MetaID -> wcanalyze.Build` seam is clean. Today `Build` already consumes
class/ident through the dump string table (`engine/wcprof/wcanalyze/graph.go:180-195`);
adding `MetaID` and unmarshalling `str(ev.MetaID)` there keeps the loader as a
field mapper. JSON string encoding also avoids the raw-NUL storage hazard I raised
implicitly in R1: current dump strings are normal JSON strings, but production
storage surfaces are much better proven for ordinary scalar strings than embedded
NUL.

The encoding does not interfere with scrub/bound counting as long as the order is:
capture raw tokens -> scrub tokens -> bound tokens -> `json.Marshal([]string)`.
The same marshalled string feeding native and OTel gives byte-identical inputs to
the oracle.

## Should-Fixes From R1

### Scrub Plumbing: mostly fixed, with two implementation constraints

The doc now correctly acknowledges that resolved `secretFilePaths` are local to
`setupSecretScrubbing` (`engine/engineutil/executor_spec.go:842-881`) and proposes
stashing them on `state`. That is the right boundary.

Two details should be explicit in implementation:

1. The scrub code must be nil-safe. `newExecState` normally installs a non-nil
   empty `ExecutionMetadata` even when the caller passed nil
   (`engine/engineutil/executor_spec.go:144-158`), but `installCACerts` manually
   constructs `caExecState` without `execMD` and calls `c.run` directly
   (`engine/engineutil/executor_spec.go:1092-1139`). A runContainer-level
   scrub/emit helper must treat nil `state.execMD` as empty.
2. If the claim is "same secret set as stdout/stderr", stashing only file paths is
   slightly weaker than stashing the loaded secret values or a scrubber/trie at
   setup time. `NewSecretScrubReader` loads file contents when the reader is built
   (`engine/engineutil/secret_scrub.go:19-58`, `:81-98`). Re-reading paths at emit
   time probably works for readonly secret mounts, but the stricter implementation
   is to build/cache the scrub set once during `setupSecretScrubbing` and reuse it
   for argv.

These are not design blockers; both are local coding constraints.

### `DupExecuted`: fixed

The revised doc now states the current behavior precisely. `AggregateClasses`
counts duplicates only for `Outcome == "executed"` or `Kind == "call_exec"`
(`engine/wcprof/wcanalyze/report.go:50-57`). ProcessRun is `exec_phase` with
generic success/error outcome (`engine/engineutil/executor_spec.go:1411-1419`;
`engine/wcprof/wcotel/loader.go:514-524`), so per-command exec classes should not
be expected to show `dup-exec`. Any repeated-command display should be additive
drill-down over `Op.Argv`.

### Empty Argv Predicate: implementation is fine; example is wrong

`ClassifyExecs` using `len(op.Argv) > 0` is the right sufficient predicate. The
doc's "real exec relying on image default CMD with an empty resolved Meta.Args"
example does not match current code: `container.command` falls back to image `Cmd`,
optionally prepends entrypoint, and returns `ErrNoCommand` if the resolved command
is still empty (`core/container.go:6859-6876`). So a started `processRun` with empty
resolved args should be defensive/legacy/malformed, not a normal default-CMD case.

No blocker: argv-less ops staying `exec.processRun` is conservative and does not
violate the model.

### Boundary-Aware `--exec-group`: acceptable

The boundary-aware prefix rule fixes the `go build` vs `go buildx` bleed from R1.
The optional `contains:` modifier is still explicit-data matching, not shell
parsing, so it does not violate the no-inference principle. I would still keep this
as Stage 2, as the doc says.

## New / Remaining Non-Blockers

### Dump schema bump wording is unsafe if implemented literally

The doc calls a `DumpSchemaVersion` bump optional and "recommended - cheap". In
current code, `ReadDump` rejects any version other than `DumpSchemaVersion`
(`engine/wcprof/dump.go:11-12`, `:169-178`). Adding `MetaID` with `omitempty` is
backward-compatible with old v1 dumps if the version stays v1. If the version is
bumped, the reader must explicitly accept both v1 and v2 or old dumps stop loading.

Recommendation: do not bump for this additive field, or add dual-version read
support in the same change. This is an implementation constraint, not a blocker to
the exec-decomposition design.

### Bound wording around `argv[0]`/`argv[1]`

The design says preserve `argv[0..1]` while also applying a 256-byte per-token cap.
That should be read as "do not drop those tokens"; very long token contents can
still be truncated. For default-class faithfulness, preserve `basename(argv[0])`
when truncating a long path-like argv0, or classify before destructive per-token
truncation and emit the bounded display value separately. Realistic Dagger commands
are unlikely to hit this, so I am not raising it above a coding note.

## Verification Run

Focused current-code test pass:

```text
go test ./engine/wcprof/wcanalyze ./engine/wcprof/wcotel ./engine/wcprof/wccloud ./cmd/wcprof-analyze ./cmd/wcprof-otel-analyze ./cmd/wcprof-oracle -count=1
```

Result: all passed.

One-line verdict: **CONVERGED**.

# Round 3 Review - Codex

Verdict: **CONVERGED**. The B4 fix resolves the QEMU second-shim blocker and
does not introduce a new blocker.

## B4 Capture Point: fixed

The revised capture point is the right one. `container.metaSpec` resolves the
user command through `container.command(opts)` (`core/container_exec.go:316-333`;
`core/container.go:6859-6876`), including image `Cmd`, optional entrypoint, and
expanded `opts.Args` from the caller (`core/container_exec.go:1255-1263`). The
QEMU shim is prepended later, only if `getEmulator` returns non-nil, at
`core/container_exec.go:2038-2048`. The executor `/.init` shim is still later:
`Client.Run` builds the exec state and runs setup funcs
(`engine/engineutil/executor.go:100-159`), and `injectInit` prepends `/.init`
unless `NoInit` is set (`engine/engineutil/executor_spec.go:364-375`).

So setting `execMD.ProfArgs = slices.Clone(metaSpec.Args)` immediately before
`core/container_exec.go:2038` captures the fully resolved user command before
both shims. It subsumes the previous Run-entry fix: normal execs no longer
headline as `.init`, `NoInit` still captures the same real command, and emulated
execs no longer headline as `/dev/.dagger_qemu_emulator`. The default class
projection will therefore see the real command for `Container.withExec`,
including multi-arch/emulated runs.

I found no later mutation of `metaSpec.Args` on the actual run path before the
QEMU prepend besides the QEMU line itself. The terminal-error path mutates a
local `meta` copy for an interactive recovery shell (`core/container_exec.go:1869-1872`);
that is not the user process being classified.

## Cache-Key Safety: no blocker

The cache-key argument checks out with the proposed timing. The `withExec` lazy
state stores `ExecMD` as part of the call/lazy payload
(`core/container_exec.go:90-108`, `:1160-1177`), and persisted lazy encoding would
serialize `lazy.State.ExecMD` (`core/container_exec.go:157-180`). But the proposed
`ProfArgs` value is not present when the call is constructed; it is set during lazy
evaluation after `execMeta` and `metaSpec` have been computed
(`core/container_exec.go:1287-1298`) and immediately before the runtime QEMU
mutation. A successful evaluation clears the lazy (`core/container_exec.go:2175-2189`),
so there is no normal path that re-persists a populated pending lazy exec as a new
cache key.

The cited internal SDK serialized execMD paths are also safe for identity:
`goSDK` and `moduleTypes` pass digested serialized strings
(`core/sdk/go_sdk.go:423-425`; `core/sdk/module_typedefs.go:103-108`), and
`LiteralDigestedString` contributes only its digest to call bytes, not its JSON
payload (`dagql/call/id.go:977-982`; value/digest are preserved separately at
`dagql/call_request_input.go:196-201`). For ordinary `SerializedString` inputs,
the JSON value itself is the literal (`dagql/types.go:667-689`), so the implementer
must preserve the invariant that `ProfArgs` is only populated at run time, never at
call construction. A `json:"-"` tag on `ProfArgs` would make that invariant
mechanically harder to violate, but it is not required by the current design as long
as the proposed timing is followed.

## Coverage Boundary: acceptable

The boundary is now honest: Stage 1 decomposes `Container.withExec`; paths without
the core capture produce no `ProfArgs`, so `Op.Argv` stays empty and classification
leaves them as the existing `exec.processRun` blob. Dockerfile/frontend execs and
other `execMD == nil` runs therefore remain status quo rather than being mislabeled
by an engine shim.

One small documentation fold for implementation: service starts are another nearby
executor caller that resolves command args in `Service`/`metaSpec`
(`core/service.go:708-715`, `:834-850`) rather than through the `Container.withExec`
runtime capture. They should be treated the same as the documented non-withExec
boundary unless a future stage adds a service-side `ProfArgs` capture. This is not a
blocker because absence of argv means no false command headline.

## Minor Folds

The five Round-2 minor notes are correctly reflected in the revised doc:

- `json.Marshal` is written in the real two-return-value form, with omit-on-error.
- `Build` is specified to guard empty `MetaID` before `json.Unmarshal`.
- scrub plumbing is nil-safe for `execMD == nil` and no-secret paths.
- `DumpSchemaVersion` is explicitly not bumped for the additive field.
- the bounds apply to the raw scrubbed slice, while preserving argv0/argv1 tokens
  and appending a sentinel for dropped trailing args.

No tests run in this round; this was a design-only delta and the reviewed change is
not implemented in the worktree.

One-line verdict: **CONVERGED**.
