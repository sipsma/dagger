# wcprof exec decomposition — design + implementation plan

Status: design, **revised after Round 1 council review**. No code written.
Author: (wcprof × OTel effort)
Scope: the final piece of the wcprof × OTel feature — break the single user-exec
blob into per-command classes, and give the user an offline rule to re-group them.

This document is **self-contained**. It cites `file:line` for every load-bearing
claim about the landed code (branch `wcprof-otel-skip-coder-daa3a9d2`, HEAD
`c28d55ae7a`). The landed code is the source of truth; the original vision in
`hack/designs/wcprof-otel-source.md` is reassessed critically in §3, not transcribed.

## What changed across council rounds

Reviewers verified the **core bet** against the code and confirmed it: relabel
`op.Class` → `op.Key()` re-buckets every consumer (replay + report) with **no replay
change**; the §3 rejections (op-sets / glob / Meta-map) are correct.

**Round 1 blockers (folded in, then re-verified converged in Round 2):**
- **B1 (`/.init` shim):** the argv at the emit site is already mutated — `injectInit`
  prepends `/.init` (`executor_spec.go:375`) before the emit. **Superseded by B4** —
  there are *two* shims at *two* layers; the fix is now a single upstream capture (§4.1a).
- **B2 (classify before the GATE):** the gate compiles+memoizes the replay program
  before the report (`gate.go:119` → `program()`), so classifying after the gate makes
  the class table and the what-if savings **disagree**. Fixed in §4.4: classify before
  `CheckStructural`, and invalidate the program memo.
- **B3 (Cloud survival of argv):** a string-*slice* OTLP attr is verified only on the
  dev otlpdump path; the production Cloud path decodes attrs as `map[string]any` and
  every existing wcprof attr is a **scalar string** by design. Fixed in §4.1c: carry
  argv as a single **scalar JSON-array string** on both sources (one encoding,
  byte-identical, rides the proven Cloud-safe convention).

**Round 2 — new blocker (verified in the code):**
- **B4 (a SECOND engine shim, prepended in core *before* `Run`):** for emulated /
  cross-platform execs, core prepends the QEMU emulator
  (`DaggerQemuEmulatorMountPoint` = `/dev/.dagger_qemu_emulator`) to the args at
  `core/container_exec.go:2043` — *before* the executor's `Run`, on top of which the
  executor later prepends `/.init`. So a `Run`-entry capture (the B1 fix) sits below
  **two** shims at **two** layers and headlines emulated execs (the SLOWEST,
  highest-value "why is CI slow" candidates) as the QEMU shim. **Fix (§4.1a): capture
  the resolved user command upstream in core, before BOTH shims, and thread it via
  `execMD.ProfArgs` to both emit sites — which subsumes and replaces the B1
  `Run`-entry capture.** Rationale and the rejected alternative (strip-at-emit) are in
  §4.1a; coverage scope is stated there.

Plus Round-2 precision fixes folded in: the non-compiling `json.Marshal` shorthand and
the empty-`MetaID` guard (§4.1c, §5), nil-safe scrub plumbing and the bound-is-on-the-raw-slice
note (§4.2), and corrected schema back-compat guidance — **do not bump
`DumpSchemaVersion`**, because the current reader hard-rejects a version mismatch
(`dump.go`); rely on the additive `omitempty` field (§8.5). Round-1 precision fixes
remain: `DupExecuted` wording (§4.5), the empty-argv predicate and never-started path
(§4.1d), the loader→`Op.Argv` seam (§4.1c), and a boundary-aware `--exec-group` grammar
with an optional `contains:` modifier (§4.6).

**Round 3 — converged 4/4; three completeness folds (not design changes), making this
the implementer's contract:** (i) the core `ProfArgs` capture is **unconditional, placed
before the `if emu != nil` block** — inside it would decompose only *emulated* execs and
leave the common case a blob, backwards (§4.1a); (ii) **service-start execs** join the
documented "stays-blob" coverage boundary (own `metaSpec`/`Run` at `core/service.go:710,834`,
§4.1a); (iii) a recorded **implementer-verification item** for `ProfArgs` json-tag hygiene
— no cache-key leak *and* no pre-emit drop (verify the core→executor hand-off is
in-process; §4.1a). The six Open Decisions are **resolved** (§8).

---

## 0. North star (do not lose sight of it)

`wcprof` ranks the **true wall-clock bottlenecks** of an engine run **by class**,
via a counterfactual discrete-event replay (PR #13393, `engine/wcprof/**`). This
effort added a **second source** — the engine's OTel telemetry that already flows
to Dagger Cloud — compiled into the **same** causal IR and analyzed by the **same,
unchanged** replay, to answer *"why was my CI run slow?"* from a Cloud trace, with
**user work first-class**.

The remaining gap: **all user execs collapse into one blob.** Every `go build`,
`git clone`, `npm install`, `pytest` is ranked as the single class
`exec_phase:exec.processRun`. The tool can say *"user work = 240s"* but not *"your
`go build` took 140s."* That headline is unactionable, and user-work-first-class
is the entire point.

**Feature, in two parts:**
1. **Per-command classes** — a slow `go build` headlines as `go build`, not as an
   anonymous `processRun`. (out of the box, no config)
2. **User-supplied grouping** — a simple rule that aggregates execs sensibly
   (e.g. all `go build *` together), applied **offline** so a captured trace can
   be re-grouped without re-running the build.

---

## 1. How aggregation works today (the machinery we extend)

The replay and the report rank by an **op class key**, and that key is the only
lever this feature needs to move.

### 1.1 The op and its class key

`Op` (`engine/wcprof/wcanalyze/graph.go:17-42`) carries `Class` and `Ident`
(`graph.go:24-25`) but **no argv / no meta field**. Its aggregation key is:

```go
// graph.go:84-87, 406-408
type ClassKey struct { Kind  string; Class string }
func (op *Op) Key() ClassKey { return ClassKey{Kind: op.Kind, Class: op.Class} }
```

`Build` fills `Class`/`Ident` straight from the dump event's interned strings —
`Class: str(ev.ClassID)`, `Ident: str(ev.IdentID)` (`graph.go:189-190`). Nothing
is derived.

### 1.2 Everything downstream is parameterized by `op.Key()`

- **Replay** (`replay.go`): `compileProgram` buckets ops into classes purely by
  `op.Key()` — `key := op.Key(); … p.classKeys = append(…); p.classOf[i] = ci`
  (`replay.go:165-171`). The per-class scaling factor is
  `Simulation.Factors map[ClassKey]float64` (`replay.go:283`), and `RunWhatIfs`
  selects candidate classes by `totalSelf[op.Key()]` (`replay.go:679-681`),
  capped at `maxWhatIfClasses = 200` (`replay.go:665`).
- **Report / aggregation** (`report.go`): `AggregateClasses` groups by `op.Key()`
  (`report.go:37`) and prints `Key.String()` rows (`report.go:213, 244`).

**Consequence (the crux of this whole design):** if the `Class` an exec op carries
changes, *every* downstream consumer — replay bucketing, what-if candidate
selection, the class table — re-buckets automatically, **with no change to
`replay.go` or the aggregation logic.** The class key is a pure function of
`op.Class`; relabeling `Class` is the entire mechanism. *(One caveat the gate
introduces: the relabel must happen before the program is memoized — §4.4.)*

### 1.3 `Ident` and `DupExecuted` (per-invocation identity — must be preserved)

`AggregateClasses` counts redundant re-execution **per class, keyed by `Ident`**,
and only for ops with `Outcome == "executed"` or `Kind == "call_exec"`:
`executedIdents[key][op.Ident]++`, reporting `n-1` as `DupExecuted`
(`report.go:50-57, 68-72`). `Ident` is also how exec waits resolve to their target
op — `execByIdent[op.Ident]` (`graph.go:237-245`).

So **`Ident` is the stable per-invocation identity** within a class. For the user
exec it is the executor `state.id` (`executor_spec.go:1417`) — **unique per
invocation**; for the call side it is the call digest `callKey`
(`dagql/cache.go:3722`) — **repeatable**. This feature must **not** overload `Ident`
(§4.1), or it breaks dup-detection and wait resolution.

### 1.4 Where the blob comes from (native + OTel emit)

Native, the user-process op is recorded at `engine/engineutil/executor_spec.go:1417`:

```go
wcprof.RecordOp(ctx, wcprof.OpKindExecPhase, "exec.processRun",
    wcprof.OpOpts{Ident: state.id, WorkType: wcprof.WorkTypeUser}, startedNS, endNS, outcome)
```

`Class` is the **constant** `"exec.processRun"` for every command; `Ident` is the
opaque `state.id`, not the argv. So every user exec shares one `ClassKey` →
**one ranking line**. (`OutcomeOK` here — string `"ok"`, `wcprof.go:154`.)

The argv exists in the engine at exec time as `process.Meta.Args`
(`spec.Process.Args = process.Meta.Args`, `executor.go:343`;
`ProcessInfo.Meta` is `executor.Meta`, `internal/buildkit/executor/executor.go:52`).
**But by the emit site it has been wrapped by two engine shims — see §4.1a.** The clean
command is instead captured upstream in core and threaded via `execMD.ProfArgs` (§4.1a),
reachable at the emit site as `state.execMD.ProfArgs` (the same `state` whose
`state.execMD`/`state.procInfo` are used at `executor_spec.go:846, 1405, 1431`).

The OTel mirror is the `exec.processRun` phase span emitted by
`emitOTelExecPhase(…, user=true, …)` (`engine/engineutil/otelprof.go:99-117`, the
user branch at `:104-106`), via `emitOTelExecSplit` (`otelprof.go:77-93`), called
at `executor_spec.go:1430` — again with `state` in scope.

The loader reads spans with **zero inference**: `class = s.Name`, `ident =
DagDigestAttr`, `workType = WcprofWorkTypeAttr` (`wcotel/loader.go:374-376`). The
user exec phase span's name is `"exec.processRun"` → same blob on the OTel side.

### 1.5 The cross-source oracle ranks by `ClassKey`

`wcotel.Oracle` compares the two sources' **`ClassKey` rankings**:
`CompareTopN` matches classes by `ClassKey` and computes Jaccard / drift
(`wcotel/oracle.go:130-155, 159-184`). **Implication for this feature:** native and
OTel must produce the **same** per-command `ClassKey`s for the same run, or the
oracle breaks. §4 guarantees this by construction (same argv source, same scrub,
same canonical encoding, same shared classify function).

---

## 2. Design principles this must honor (settled; violating one fails review)

- **Analysis = a rational function of FAITHFUL data. ZERO inference.** Grouping
  operates on **explicit emitted argv**, never by parsing a span name or
  pattern-matching a description.
- **Fix the EMIT, not the model.** `replay.go` / the causal core stay **unchanged**.
- **Faithful-emit / trivial-loader.** The loader stays a zero-inference field-mapper.
- **Bounded OTel volume.** Argv on a span is capped (count + length).
- **Argv is SCRUBBED engine-side** before it leaves the process.
- **Native ↔ OTel oracle parity.** Both sources carry the same argv and produce
  the same groups.

See `wcprof-analysis-rational-function-principle` and
`erik-no-incorrect-code-no-scope-excuse` in the project memory.

---

## 3. Critical reassessment of the original vision

`hack/designs/wcprof-otel-source.md` anticipated this feature: §1.2 ("the analyzer's
only jobs: **group** ops into op-sets by explicit, possibly user-supplied rules, and
simulate"), the `WcprofExecArgvAttr` vocabulary (`source.md:210`), and
`op.Meta = buildMeta(s)` (`source.md:490`). **The spirit is right. Several concrete
decisions were made before the implementation existed and must be adapted or
rejected.**

| Original (`source.md`) | Verdict | Why, for where we actually landed |
|---|---|---|
| **R-Q** (`:565-570`): `processRun` carries argv so user class-rules target the op with the user self-time | **ADOPT** | Confirms §4.1: argv lives on the user-process op, where the replay's scalable self-time is. Re-derived independently; the chunk4 reviewer re-verified `exec.run`'s self ≈ 0 (its phases tile it). |
| **R-K** (`:402-405`): argv out of the span name; bounded + scrubbed in the attr only; honor `SecretEnvNames/SecretFilePaths` | **ADOPT** | Span names are more exposed and harder to scrub. §4.2 reuses the engine's secret scrubber and bounds. |
| **R-M** (`:535-538`): "rich Meta is **OTel-source-primary**; native stays lean; defer native argv; if needed add one interned `MetaID uint32`" | **REJECT the deferral; ADOPT the `MetaID` hint** | Native MUST carry argv now — the oracle (§1.5) compares **both** sources' `ClassKey`s; OTel-only argv would make the per-command classes native-invisible and break parity. The fix is exactly the interned `MetaID` R-M proposed (§4.1c). |
| **R-M encoding** (string slice / array on OTel) | **REJECT; use a scalar string** | The production OTel path is **Cloud**, which decodes attrs as `map[string]any` and where every existing wcprof attr is a **scalar string** by design (`wccloud/cloud.go:29-33, 59`); an array attr is unverified end-to-end. Carry argv as a single **scalar JSON-array string** on both sources (§4.1c). |
| **R-N** (`:639`) + §5.2: overlapping **op-sets**, `RunWhatIfsForOpSets`, each scaled independently | **REJECT** | Overlapping op-sets are **partition-breaking** and would require changing `replay.go` (forbidden, §2). Our grouping is a **partition** — each exec op gets exactly one label, mapping 1:1 onto the existing `ClassKey` model with **zero** replay change. Overlapping views = re-run the offline analyzer with different rules. |
| **ClassRule/MetaMatch** (`:621-625`): multi-field ANDed matcher; **"glob dropped — path-glob mishandles `/`"** | **NARROW + heed the gotcha** | One field (argv), simplest match. Adopt the documented lesson: **no `filepath.Match` glob** (it special-cases `/`, which pervades argv). §4.6 uses a boundary-aware literal **prefix** (+ optional `contains:`). |
| Rich `Meta map[string]string` (argv/image/exit/owner/module) (`:490, 530`) | **NARROW to argv only** | This feature groups on argv. Image/exit/owner/module are scope creep; add later if a feature needs them. |
| `WcprofExecArgvAttr = "dagger.io/wcprof.exec.argv"` (`:210`) | **ADOPT name, FIX prefix** | Landed wcprof attrs are bare `wcprof.*` (`telemetryattrs/attrs.go:53,58,68`). Use `wcprof.exec.argv`. |

The net effect of the rejections is a **much smaller** design than the original
op-set vision: an additive emit field + one offline classify pass, with the replay
and aggregation logic untouched.

---

## 4. The design

### 4.1 Where argv lives, and how it is carried (design question 1)

**Decision: argv lives on the user-process op (`exec.processRun`, `work_type=user`),
as a new dedicated field `Op.Argv []string` — never on `Ident`, never on `Class`,
never on the `call_exec`/`withExec` call.**

Justification:
- **The user-process op is where the scalable self-time is.** The what-if scales a
  class's *self-time* (`replay.go:489`); the user's CPU time lives in `processRun`'s
  self-interval (`exec.run`'s own self-time is ~0 — its children `containerStart` +
  `processRun` tile it). Scaling `go build`'s self-time = "what if go build were
  faster," the exact actionable counterfactual.
- **Not `Ident`.** `Ident` is the per-invocation identity that drives `DupExecuted`
  and exec-wait resolution (§1.3). If argv were the `Ident`, two genuinely distinct
  `go build ./foo` invocations with identical argv would corrupt those. Keeping
  `Ident = state.id` preserves them exactly; argv is **separate** data.
- **Not `Class` (at emit).** Baking the group into `Class` engine-side would (a)
  freeze the group in the trace (no offline re-grouping), and (b) push classification
  policy into the hot path and both emit sites. The raw argv must be in the IR
  regardless, for drill-down and re-grouping. So argv is emitted **raw**; the group
  is **derived offline** (§4.4).

**IR change (additive, faithful):** `Op.Argv []string` on `graph.go`'s `Op` (the
scrubbed, bounded user-process argv; empty for non-exec ops).

#### 4.1a — capture the user command UPSTREAM IN CORE, before BOTH engine shims (BLOCKER 4, subsumes B1)

The user command is wrapped by **two** engine shims, prepended at **two** layers, both
*before* the wcprof emit:

1. **QEMU emulator (core), for emulated / cross-platform execs:** when an emulator is
   needed, core prepends it —
   `metaSpec.Args = append([]string{engineutil.DaggerQemuEmulatorMountPoint}, metaSpec.Args...)`
   (`core/container_exec.go:2043`; `DaggerQemuEmulatorMountPoint = "/dev/.dagger_qemu_emulator"`,
   `executor_spec.go:73`) — **before** the executor's `Run` (the resulting
   `meta`/`procInfo` is built at `:2051,2088` and handed to the executor).
2. **`/.init` (executor), for every non-`NoInit` exec:** `injectInit` prepends
   `initPath := "/.init"` (`executor_spec.go:369,375`) as an early setup func in `c.run`
   (`executor.go:145`), **before** the emit at `:1417`/`:1430`.

So a capture at the executor's `Run` entry (the Round-1 B1 fix) sits **below both
shims**: for a direct exec `argv[0] == "/.init"`; for an **emulated** exec
`argv[0] == "/dev/.dagger_qemu_emulator"`. The default `basename(argv[0])` would then
headline emulated execs as the QEMU shim — and emulated (multi-arch) execs are exactly
the **slow, high-value bottleneck candidates** we must not mislabel.

**Fix (chosen): capture the resolved user command upstream in core, before BOTH shims,
and thread it as explicit metadata on `execMD`.** Concretely:
- `engine/engineutil.ExecutionMetadata` (`executor.go:45`) gains `ProfArgs []string`.
- In `core/container_exec.go`, **unconditionally — *before* the `if emu != nil {`
  block at `:2042`** (e.g. just after the `getEmulator` error check at `:2040`), and
  after `metaSpec.Args` is the fully-resolved command (`metaSpec.Args = args` where
  `args, _ = container.command(opts)` combines entrypoint + args, `:323,333`), set
  `if execMD != nil { execMD.ProfArgs = slices.Clone(metaSpec.Args) }`.
  **It must NOT be placed inside the `if emu != nil` block** — that would capture only
  *emulated* execs and silently leave the common non-emulated `withExec` as the blob,
  exactly backwards. Placing it before the block (and before the executor's later
  `/.init`) captures the clean command for **all** `withExec` execs, emulated or not.
- Both emit sites read `state.execMD.ProfArgs` (the same `execMD` already in scope —
  `state.execMD.SecretEnvNames` is used at `executor_spec.go:846`). `ExecutionMetadata`
  is the **same type** in core and the executor (`core/container_exec.go:95,108,1164`),
  so this is one clean channel, and **both sources read one identical value** —
  strengthening parity further.

This **subsumes and replaces the B1 `Run`-entry capture** (a single pre-everything
capture is before both shims, so no per-shim stripping is needed) and is robust to
**any future shim** added between this point and the emit.

**Cache-key safety (verified by timing).** `ExecutionMetadata` is serialized/digested
in some paths (e.g. `dagql.NewDigestedSerializedString(&execMD, …)` at
`core/sdk/go_sdk.go:424`, `module_typedefs.go:107`), and the withExec call digest is
fixed at call/recipe-construction (`execMeta`, `container_exec.go:242-248`). All of
those happen **before** the lazy exec runs, whereas `ProfArgs` is set at
exec-**run** time (`:2042`). So it cannot perturb a previously-computed cache key, and
it merely duplicates the command that already determines the result.

**Implementer-verification item (record, do NOT resolve in design): `ProfArgs` tag
hygiene.** Whichever json tag `ProfArgs` gets must satisfy **both**:
- **(a) no cache-key leak** — it must not enter any cache-key/digest serialization of
  `ExecutionMetadata` (e.g. `dagql.NewDigestedSerializedString(&execMD, …)`,
  `core/sdk/go_sdk.go:424`, `module_typedefs.go:107`; the call digest at
  `container_exec.go:242-248`). The timing argument above makes it empty at those points,
  but a `json:"-"` tag closes the hole structurally.
- **(b) no pre-emit drop** — `ProfArgs` (set in core at `:2042`) must still be readable
  at the executor emit (`state.execMD.ProfArgs`). Confirm the core→executor
  `ExecutionMetadata` hand-off is **in-process** (a live pointer through the gateway to
  `executor.Run`, not serialize-then-deserialize); if any serialization sits on that
  path, a `json:"-"` tag would silently drop it.

These pull opposite ways only if execMD is serialized *between* `:2042` and the emit —
which is **not expected** (the engine runs core and the executor in one process). The
likely-correct answer is `json:"-"` *given* an in-process hand-off; the implementer must
**verify the hand-off is in-process** before locking the tag. (Separately, confirm
`execMD` is not re-serialized/re-digested after the capture point — it is not in the
traced paths.)

**Why not strip the shims at the emit site (the rejected alternative).** Stripping a
leading `/.init` / `/dev/.dagger_qemu_emulator` in the executor is simpler but
**reconstructs** the command by enumerating every engine shim, coupling the wcprof
emit to that list; a future arg-prepending shim silently re-breaks the headline.
Capturing before any shim needs no such list. (Stripping remains the natural mechanism
*if* one later wants to extend coverage to chokepoint-only paths — see below.)

**Coverage scope (stated honestly).** `executor.Run` is a chokepoint reached from both
core's `Container.withExec` path **and** the buildkit/Dockerfile frontend
(`NewContainer` → `gateway/container/container.go:373`, called from
`grpcclient/client.go:909`), but the QEMU prepend and this capture live only on the
core withExec path. So Stage 1 decomposes **`Container.withExec`** (the feature's
headline target: `go build`, `npm install`, …). Execs that do **not** flow through the
core capture carry no `ProfArgs` → empty `Op.Argv` → they **remain the aggregated
`exec.processRun` blob** — consistent across **both** sources, the pre-feature status
quo, and crucially **not a mislabel** (never a shim path). Such paths are:
- **Dockerfile `RUN`** via the buildkit/gateway frontend (`grpcclient/client.go:909` →
  `gateway/container/container.go:373`).
- **Service starts** — `Service.startContainer` builds its **own** `metaSpec`
  (`core/service.go:710`) and calls `bk.Run` directly (`core/service.go:834`), a path
  separate from `container_exec.go`, so it never hits the `:2042` capture.
- **Internal execs with `execMD == nil`**.

Extending decomposition to those paths is additive future work: set `ProfArgs` at
*their* arg-resolution site with the same pattern (e.g. before `core/service.go:710`'s
metaSpec is finalized) — or, for a pure chokepoint approach, capture at `Run` entry and
strip the leading QEMU constant (`/.init` is added later and is auto-excluded by capture
timing).

#### 4.1b — what the carriers do, end to end

Both sources read the **same** `state.execMD.ProfArgs`, apply the **same** scrub+bound
helper (§4.2) producing one `[]string`, and serialize it to the **same canonical
scalar string** (§4.1c). The two emit sites are adjacent in one function
(`executor_spec.go:1405-1431`), so the scrub+bound+encode runs **once** and feeds
both the native `RecordOp` (`:1417`) and `emitOTelExecSplit` (`:1430`).

#### 4.1c — canonical encoding: one scalar JSON-array string, both sources (BLOCKER 3)

The production OTel target is a **Dagger Cloud trace**: `wccloud.SpanFromCloud` maps
`cloud.SpanData.Attributes` (a `map[string]any` JSON decode) straight through
(`wccloud/cloud.go:59`), and the codebase deliberately uses **scalar strings** for
everything that must survive that decode — the doc comment spells it out
(`cloud.go:29-33`), and int64s ride as **decimal strings** for exactly this reason
(`telemetryattrs/attrs.go:112-120`, `WcprofSessionSpanCountAttr`). A string-*slice*
(OTLP `ArrayValue`) would be the **first** array-valued wcprof attr — unverified
through Cloud ingest + the GraphQL `attributes` field. If Cloud stringifies or drops
it, the feature works in the local otlpdump dev loop but **silently does nothing on
the production Cloud trace**, which is the entire use case.

**Decision: carry argv as a single scalar string — the JSON encoding of the scrubbed
`[]string` (a JSON array, e.g. `["go","build","./..."]`) — identical on both sources.**
Note `json.Marshal` returns `([]byte, error)`; the real form is
`b, err := json.Marshal(argv); if err == nil { … string(b) … }` (marshal of a
`[]string` does not error in practice, but on any error the attr/field is simply
omitted — never a partial or panicking emit).
- **Native:** intern `string(b)` into the recorder string table as a new
  `MetaID uint32` (mirroring `IdentID`) on `Event`/`openOp`
  (`wcprof.go`), `DumpEvent`/`DumpOpenOp` (`dump.go`), threaded through `toDumpEvent`
  and `WriteDump`'s open-op loop. `OpOpts` gains `Argv []string`; the recorder
  marshals + interns it.
- **OTel:** stamp `attribute.String(WcprofExecArgvAttr, string(b))` on the
  `exec.processRun` phase span (`otelprof.go:104-106`).
  `WcprofExecArgvAttr = "wcprof.exec.argv"` (new, `telemetryattrs/attrs.go` ~`:88`).
- **Loader → shared `Build` (the seam):** the OTel loader interns the
  `WcprofExecArgvAttr` string into **its own** string table and sets
  `DumpEvent.MetaID` — the **same** field the native dump uses — so the **one**
  `wcanalyze.Build` path serves both sources. `Build` recovers `Op.Argv` by
  `json.Unmarshal([]byte(str(ev.MetaID)))` **only when `str(ev.MetaID) != ""`** (the
  same `str()`-returns-`""` guard `Ident` uses, `graph.go:189-190`); empty ⇒
  `Op.Argv == nil` and the op stays the blob (§4.1d). A malformed `MetaID` that fails
  to unmarshal leaves `Op.Argv == nil` (defensive — never a panic).

Why JSON-array string over a raw NUL-joined string (the council's first suggestion;
chunk4 offered this JSON form as the alternative): a JSON array contains **no control
bytes**, so it rides the *already-proven* "scalar string round-trips bit-exact through
Cloud" machinery (the §6.6 round-trip test) with nothing new to verify, whereas a raw
`\x00` separator is a fresh byte that common string stores/transports reject or
truncate (e.g. Postgres `TEXT` forbids `0x00`) — which would defeat B3's own
Cloud-survival goal. JSON is also unambiguous (no separator-collision with argv
content) and self-describing in a dump/trace. It keeps both wire forms **byte-identical**
(same `json.Marshal` of the same scrubbed slice), so the oracle's reconstructed
`Op.Argv` is provably identical, and it **drops** the `attrStrSlice`/`[]any`→`[]string`
helper entirely. *(Adding an attribute to an existing span creates no span, so the
completeness checksum `wcprof.session_span_count`, `telemetryattrs/attrs.go:120`, is
unaffected. The encoding form is gated by the Cloud round-trip test, §6 test 6.)*

#### 4.1d — the relabel predicate is one-directional; argv-less and never-started execs

`ClassifyExecs` (§4.4) relabels exactly the ops with `len(op.Argv) > 0`. The relation
is `len(Argv) > 0 ⟹ user-process op` (sufficient to relabel safely), **not** a
biconditional: a real exec relying on the image default CMD with an empty resolved
`Meta.Args` carries empty `Op.Argv` and **stays the `exec.processRun` blob** — the
correct outcome (nothing to group on), consistent across both sources (both emit no
argv). No future code may treat "no argv" as "not an exec." Likewise, when the process
**never starts**, neither source emits `processRun` at all (`executor_spec.go:1419`
else-branch records only `containerStart`; `otelprof.go:81-86` mirrors it), so the
time correctly ranks as engine `exec.containerStart`, no user self-time, no argv —
consistent across sources.

### 4.2 Scrub + bound (engine-side, before emit)

Command args carry secrets (`--password=…`, tokens, cred URLs). The engine already
scrubs registered session secrets from exec stdout/stderr with a trie-based censor:
`NewSecretScrubReader(r, env, secretEnvNames, secretFilePaths)`
(`engine/engineutil/secret_scrub.go:19-58`), wired at `executor_spec.go:876` from
`state.spec.Process.Env`, `state.execMD.SecretEnvNames`, and a `secretFilePaths`
resolved (rootfs-relative + stat-filtered) at `executor_spec.go:858-869`.

**Reuse it (with explicit, nil-safe plumbing).** The argv to scrub is the clean
`state.execMD.ProfArgs` (§4.1a). The resolved file paths are **local** to
`setupSecretScrubbing` today, so the data path must be made explicit:
`setupSecretScrubbing` runs as a setup func (`executor.go:153`) **before** the emit
inside `runContainer`, so have it stash the resolved list on
`state.profSecretFilePaths`. Then factor a `ScrubString`/`scrubArgv` helper out of the
censor that builds the secret trie **once** and scrubs each token of
`state.execMD.ProfArgs`, fed from
`(state.spec.Process.Env, state.execMD.SecretEnvNames, state.profSecretFilePaths)` —
the **same** secret set the stdout/stderr scrubbers use, so a value scrubbed from
output is scrubbed from argv. **All three inputs may be nil** (no registered secrets;
`NoInit`; the `installCACerts` / no-secret early return at `:846-848` leaving the stash
nil; or `execMD == nil`): the helper must nil-guard and, with an empty secret set, pass
the argv through unscrubbed (there is nothing to scrub). If `execMD == nil` or
`ProfArgs` is empty, no argv is emitted (the op stays the blob, §4.1d).

Then **bound**: cap token count (64), per-token length (256 B), total (4 KiB) — the
caps apply to the **raw scrubbed `[]string`**. On overflow, **truncate trailing tokens
only — never `argv[0]` or `argv[1]`** (the group key depends on them) — and append a
fixed sentinel token `"…(+N more)"` that cannot be mistaken for a real arg by a prefix
rule. The JSON encoding (§4.1c) of the bounded slice is marginally larger than the raw
bytes (quotes/commas/brackets/escapes — roughly +200 B at 64 tokens), well within OTel
attribute limits — stated, not a concern. Both emit sites call the **same** helper on
the **same** `ProfArgs`, so native and OTel argv are byte-identical.

**Honest scope.** This redacts secrets the **engine knows** (registered session
secrets). A literal secret a user bakes directly into argv is not in the scrub set —
but that exposure **already exists**: the withExec args already flow to Cloud via the
`dag.call` attribute (`source.md:405`). We match, not widen, the existing exposure,
and additionally scrub registered secrets `dag.call` may not. Heuristic
flag-redaction is **out of scope** (resolved, §8.4).

### 4.3 Default classification (design question 2)

**Default group = `basename(argv[0])` + (the first non-flag `argv[1]`).** A pure,
deterministic projection of explicit data — not a heuristic guess:

```
prog = basename(argv[0])
if len(argv) >= 2 and not argv[1].startswith("-"):
    class = prog + " " + argv[1]
else:
    class = prog
```

Operating on the **clean** argv (§4.1a), this yields `go build`, `git clone`,
`npm install`, `pytest tests/`, `sh` (for `sh -c …`, since `-c` is a flag). Total, no
config, delivers feature #1.

**The honest hard case: `sh -c "<real command>"`.** Many CI execs are shell-wrapped.
The *faithful* program is `sh`; recovering `go build` would require **shell-parsing
the `-c` string = inference = forbidden**. So the default groups these as `sh`, and the
out-of-box win lands fully for **direct** execs; shell-wrapped builds need a user rule
(§4.6, where the `contains:` modifier targets exactly this). This is the deliberate
honest limitation (resolved, §8.3) — no built-in shell parsing, ever.

### 4.4 The classify pass — runs BEFORE the gate; replay untouched (BLOCKER 2)

A new, single analyzer entry point:

```go
// engine/wcprof/wcanalyze/classify.go (new)
func ClassifyExecs(g *Graph, rules []ExecGroupRule)
```

For every op with `len(op.Argv) > 0`, it sets `op.Class = group(op.Argv, rules)` (the
first matching rule's label, else the §4.3 default). Ops without argv are untouched.
Because `op.Key()` reads `op.Class` (`graph.go:407`) and every consumer reads
`op.Key()` (§1.2), this **relabel-in-place** re-buckets the replay and the report with
**no other code change**.

**The ordering trap (B2), and the fix.** The replay program memoizes `classOf` once,
via `progOnce` (`graph.go:78-80`, `replay.go:122-127`), and the **gate is the first
trigger** of that memoization: `CheckStructural` → `NewSimulation(g, nil)`
(`gate.go:119`) → `g.program()` (`replay.go:333`); `WriteReport` then reuses the frozen
program (`report.go:162, 167`). So if `ClassifyExecs` runs *after* the gate, the
what-if savings are computed on the **stale blob** `classKeys` while
`AggregateClasses` re-buckets **live** — the class table shows `go build` but its
what-if saving is 0. A silent, self-contradicting headline: precisely the failure this
effort exists to kill.

**Fix (both, belt-and-suspenders):**
1. **Call `ClassifyExecs` before `CheckStructural`** in every entry point (§5). This is
   safe: the gate's verdict is **class-independent** — its Simulation schedules by
   causal structure (parent/child/waits) and its invariants (`self>makespan`, cycles,
   unresolved waits) never read `Class`. Classifying first cannot change the gate.
2. **`ClassifyExecs` invalidates the program memo** after relabeling:
   `g.progOnce = sync.Once{}; g.prog = nil` (a small `g.invalidateProgram()` helper in
   package `wcanalyze`). This makes the pass **order-independent** — a future caller
   that simulated before classifying can't silently defeat it. This is a **cache
   invalidation on the Graph, not a change to the replay algorithm** — §4.7 still holds.

Other properties: keys off `op.Argv` (immutable), so **idempotent** under re-grouping;
**always on** (empty `rules` ⇒ the §4.3 default ⇒ per-command classes out of the box);
**shared by all three CLIs and the oracle**, so both sources group identically. Losing
the literal `"exec.processRun"` label on relabel is intended and safe — `classifyKind`
keys off the span *name* and `WcprofOpKindAttr`, not `Class` (`loader.go:493-507`).

### 4.5 Drill-down + `DupExecuted` (design question 4) — precise

- **Full per-invocation identity is retained:** `Op.Argv` holds the exact scrubbed
  argv, and `Op.Ident` still holds `state.id`. A group can be expanded to its distinct
  argvs with occurrence counts — but that is **new, additive display logic over
  `Op.Argv`/`Ident`**, not the existing `DupExecuted`. Not a change to ranking logic.
- **`DupExecuted` is a call-side metric and is unchanged.** It fires only for
  `Outcome == "executed" || Kind == "call_exec"`, keyed on `Ident` (`report.go:50`).
  The user `processRun` op is `OutcomeOK`/`exec_phase` with `Ident = state.id`
  (**unique per invocation**), so it is **never on the dup path** — per-command exec
  classes show **no** dup-exec count. That is **correct** (each container run is
  genuinely distinct work) and a **non-regression** (it was never counted). Redundant-
  execution detection remains on the `call_exec` (`Container.withExec`, `Ident=callKey`)
  class, which decomposition does not touch. *(Validation's dup assertion therefore
  targets the `call_exec` class, not the per-command `exec_phase` classes — §6.)*

### 4.6 The user-supplied grouping rule (design question 3)

**Decision: a repeatable CLI flag `--exec-group='<match>=<label>'`, applied purely
OFFLINE in `ClassifyExecs`.** No new pattern language.

- `<match>` matches against the space-joined scrubbed argv
  (`strings.Join(op.Argv, " ")`). Default mode is a **boundary-aware literal prefix**:
  it matches iff `joined == match` **or** `joined` starts with `match + " "`. So
  `go build` matches `go build` and `go build ./...` but **not** `go buildx ...` (the
  boundary guard avoids the `go buil`-matches-`go build` footgun).
- An optional `contains:` modifier (`--exec-group='contains:go build=builds'`) switches
  that pattern to a **substring** match on the joined argv — the form needed for
  shell-wrapped commands like `sh -c "cd x && go build"`, where a fixed prefix does not
  reach the real program. Prefix stays the simple default; `contains:` is opt-in.
- First rule (in flag order) that matches assigns `<label>`; unmatched falls through to
  the §4.3 default. The `=` is split on the **first** occurrence (label is everything
  after it); a pattern that itself needs a literal `=` is not expressible in this simple
  form (acceptable — command prefixes rarely contain `=`; a two-token
  `--exec-group <pattern> <label>` form is the fallback if it ever matters).
- **No `filepath.Match` glob** — the documented `/`-mishandling gotcha
  (`source.md:621-625`). A literal prefix/substring is simpler *and* correct.

**Why offline, not at emit:** re-grouping a *captured* trace must not re-run the build.
Because the raw argv is in the IR (§4.1) and classification is in the analyzer (§4.4),
re-grouping is just **re-invoking the offline analyzer with different `--exec-group`
flags over the same dump/trace**. This is the "re-group without re-emit" property, for
free.

**Composition with `ClassKey`:** the rule produces the op's `Class`; the rest is the
existing `ClassKey{Kind, Class}` partition. Rules define a **partition**
(first-match-wins, total via the default), never overlapping sets — which is why the
replay needs no change (§3, R-N).

### 4.7 Replay untouched? (design question 5) — **definitively yes**

`replay.go` and the analysis logic in `graph.go` are **unchanged**. The only
`graph.go` changes are the additive `Op.Argv` field, `Build` populating it, and the
`invalidateProgram()` memo-reset helper (a cache reset, **not** a replay-algorithm
change). The only new analyzer code is `classify.go`. `AggregateClasses` is unchanged
(it re-buckets via `op.Key()`). The report needs **no** change for the core win (the
class table already prints `Key.String()` = `exec_phase:go build` once `Class` is
relabeled); an optional drill-down section is additive.

One **non-issue worth stating**: decomposition raises exec-class cardinality. The
what-if candidate set is already capped at the top `maxWhatIfClasses = 200` **by total
self-time** (`replay.go:665, 689-709`), so the heaviest commands are always simulated —
the slow `go build` is exactly the one that makes the cut. No change needed.

---

## 5. Exact touch points (enumerated)

**Capture — core (`core/`), the single clean-command source (B4):**
- `engine/engineutil/executor.go` (`:45`): `ExecutionMetadata` gains `ProfArgs []string`
  (json-tag hygiene is a recorded implementer-verification item — §4.1a).
- `core/container_exec.go` (**unconditionally, before the `if emu != nil {` block at
  `:2042`** — *not* inside it — after `metaSpec.Args` is resolved at `:333`):
  `if execMD != nil { execMD.ProfArgs = slices.Clone(metaSpec.Args) }`. *(Replaces the
  Round-1 `Run`-entry capture entirely. Cache-key-neutral by timing — §4.1a.)*

**Emit — native (`engine/`):**
- `engine/engineutil/executor_spec.go` `setupSecretScrubbing` (`:842-873`): stash the
  resolved `secretFilePaths` on `state.profSecretFilePaths`.
- `engine/engineutil/secret_scrub.go`: extract `ScrubString`/`scrubArgv` (build the
  trie once, scrub each token; nil-safe inputs, §4.2).
- `engine/engineutil/executor_spec.go` (~`:1406`, once, when either source is active and
  `state.execMD != nil && len(state.execMD.ProfArgs) > 0`):
  `argv := scrubAndBound(state.execMD.ProfArgs, …)`; pass into the native `processRun`
  `OpOpts.Argv` (`:1417`) and `emitOTelExecSplit` (`:1430`).
- `engine/wcprof/record.go`: `OpOpts.Argv []string` (`:80-87`); `json.Marshal` (handle
  err) + intern in `RecordOp`/`BeginOp` → `Event.MetaID` (mirror `IdentID`,
  `:104-108, 285-297`).
- `engine/wcprof/wcprof.go`: `Event.MetaID uint32`, `openOp.metaID` (`:246-266, 278-287`).
- `engine/wcprof/dump.go`: `DumpEvent.MetaID`/`DumpOpenOp` field + `toDumpEvent`
  + `WriteDump` open-op loop (`:80-98, 118-140`). Additive `omitempty`, **no
  `DumpSchemaVersion` bump** — the reader hard-rejects a version mismatch (§8.5).

**Emit — OTel (`engine/`):**
- `engine/telemetryattrs/attrs.go`: `WcprofExecArgvAttr = "wcprof.exec.argv"` (~`:88`).
- `engine/engineutil/otelprof.go`: thread argv into `emitOTelExecSplit` /
  `emitOTelExecPhase`; stamp `attribute.String(WcprofExecArgvAttr, string(b))` where
  `b, err := json.Marshal(argv)` (omit on err) on the `user` branch (`:104-106`).

**IR / loader:**
- `engine/wcprof/wcanalyze/graph.go`: `Op.Argv []string`; `Build` fills it via
  `json.Unmarshal([]byte(str(ev.MetaID)))` **guarded on `str(ev.MetaID) != ""`** (empty
  or malformed ⇒ `Op.Argv == nil`) (`:180-214`); `invalidateProgram()` helper.
- `engine/wcprof/wcotel/loader.go`: intern the `WcprofExecArgvAttr` string into the
  loader string table → `DumpEvent.MetaID` (the shared `Build` seam; `:358-413`). No
  `attrStrSlice` helper needed.

**Analyzer (classify BEFORE the gate everywhere):**
- `engine/wcprof/wcanalyze/classify.go` (new): `ExecGroupRule`, `ClassifyExecs`,
  default projection, boundary-aware prefix + `contains:` matcher.
- `cmd/wcprof-analyze/main.go`: `--exec-group` flag; `ClassifyExecs(graph, rules)`
  after `LoadMulti` (`:73`), before `WriteReport` (`:77`). *(No gate in the native CLI;
  `WriteReport`'s own `baseSim` is the first `program()`.)*
- `cmd/wcprof-otel-analyze/main.go`: `--exec-group` flag; call `ClassifyExecs(g, rules)`
  as the **first** line of `analyze()` — **before** `CheckStructural` (`:136`) and
  `WriteReport` (`:143`).
- `cmd/wcprof-oracle/main.go` (+ `wcotel/oracle.go`): `--exec-group` flag; call
  `ClassifyExecs` on **both** the native and OTel graphs with **identical** rules,
  **before** the OTel `CheckStructural` (`:78`) and before `Oracle`/`TopBottlenecks`
  (`oracle.go:51`).
- (optional) `report.go`: additive per-group drill-down section.

---

## 6. Validation / oracle plan (design question 6)

**Workload (the known answer).** A run with three deliberately distinct, deliberately
timed execs: (1) a slow `go build ./...` (the headline — ranks #1 by user self-time);
(2) a `git clone <repo>` (a separate class); (3) a **repeated** identical exec
(e.g. two `sleep N`/`echo hi`) for drill-down counts. A `sleep N` exec gives a precise,
machine-checkable user self-time.

**Tests:**
1. **Native unit (`engine/wcprof`):** record the workload (or a synthetic event
   stream), dump, reload, `ClassifyExecs(nil)`. Assert classes `exec_phase:go build`,
   `exec_phase:git clone`, `exec_phase:echo hi` exist, the blob `exec.processRun` is
   gone, `go build` ranks #1 by what-if saving, and **no class's `argv[0]` is an engine
   shim** — neither `.init` nor `/dev/.dagger_qemu_emulator` (the B1+B4 regression
   guard; assert directly that `execMD.ProfArgs[0]` is the user program). Assert
   `Op.Argv` round-trips through the dump exactly.
   - **Integration (B4 specifically):** an **emulated / cross-platform** `withExec`
     (where `getEmulator` returns non-nil, `container_exec.go:2038`) must still headline
     by the user command (`go build`), **not** `dagger_qemu_emulator` — the highest-value
     case the blocker protects.
2. **Gate→report order regression (catches B2):** drive the **full** OTel CLI path
   (`ClassifyExecs` → `CheckStructural` → `WriteReport`) and assert a per-command exec
   class has a **non-zero** what-if saving — i.e. the savings are computed on the
   relabeled classes, not the frozen blob. (Also assert the memo-reset path: classify,
   build the program, classify again with different rules, and confirm the second
   grouping takes effect.)
3. **OTel loader unit (`wcotel`):** a fixture otlpdump JSONL (model
   `engine/wcprof/wcotel/testdata/*.jsonl`) with `WcprofExecArgvAttr` scalar
   JSON-array-string attrs on the `exec.processRun` spans. Assert the loader reconstructs
   `Op.Argv` and the same per-command classes appear; the structural gate still passes.
4. **Cross-source oracle (strongest check, `oracle_test.go` style):** run one workload
   with **both** sources live, `ClassifyExecs` **both** graphs with the same rules, and
   assert `OracleComparison.Agrees(minJaccard, maxRelDrift)` — native and OTel produce
   the **same** per-command groups and savings (`oracle.go:182-184`).
5. **Re-group-without-re-emit:** take **one** captured dump/trace; run the analyzer
   twice — no rules (default), then `--exec-group='go build=builds'
   --exec-group='git clone=builds'`. Assert the class table changes (two commands
   collapse into `builds`) with **no re-capture**, and native/OTel re-group identically.
6. **Cloud round-trip of the argv attr (gates the §4.1c encoding):** assert the scalar
   JSON-array-string survives a Cloud-shaped `map[string]any` decode bit-exact
   (extending the §6.6 round-trip test), and `json.Unmarshal` recovers the exact argv.
   *(If anyone insists on a slice/raw-NUL form instead, this test must prove Cloud
   preserves it — the JSON-array string passes by riding the existing scalar-string
   guarantee.)*
7. **Scrub + bounds:** an exec whose argv contains a registered session secret value;
   assert the emitted `Op.Argv` shows `***`, not the secret; assert bounds truncate an
   over-long argv while **preserving `argv[0..1]`** and appending the sentinel. Include a
   **nil-input** case (no registered secrets / `execMD == nil`): argv passes through
   unscrubbed without panicking, and `execMD == nil` emits no argv.
8. **Coverage boundary (B4 scope):** an exec that does **not** flow through the core
   capture (no `ProfArgs` — e.g. a **service start**, a Dockerfile `RUN`, or an
   `execMD == nil` internal exec) carries empty `Op.Argv` and **stays the
   `exec.processRun` blob** — consistent across native and OTel, and never a shim
   mislabel. (Confirms the §4.1a scope is a graceful non-goal, not a wrong answer.)

Runbook: the `telemetry-capture` skill (otlpdump) for the OTel side; the native
`/debug/wcprof/dump` endpoint for native; `cmd/wcprof-oracle` to compare; the
`engine-debugging` skill for the dev-engine loop.

---

## 7. Staging (minimal — 2 stages; Stage 1 independently shippable)

**Stage 1 — emit argv + faithful default per-command classes (the headline win).**
- Capture the clean command upstream in core via `execMD.ProfArgs`, before both engine
  shims (B4, subsumes B1); scrub+bound; emit on the `processRun` op, native (`MetaID`) +
  OTel (`wcprof.exec.argv` scalar JSON string), one canonical encoding (B3).
- IR `Op.Argv` via the shared `MetaID` seam; loader reads it.
- `ClassifyExecs(g, nil)` default projection (§4.3, §4.4) wired **before the gate** in
  the three CLIs + oracle, with the memo reset (B2).
- Validation tests 1–4, 6, 7 (§6).
- Delivers feature #1: `go build` headlines as `go build`, native==OTel.

**Stage 2 — the user-supplied grouping rule (feature #2).**
- `--exec-group='<match>=<label>'` (boundary-aware prefix default + optional
  `contains:` modifier, §4.6), applied offline in `ClassifyExecs`.
- Validation test 5 (re-group without re-emit) + the oracle under rules.
- Optional drill-down report section.

Stage 1 is the architecture-defining, high-value piece; Stage 2 is a thin CLI/UX layer
over the same classify pass.

---

## 8. Resolved decisions (council, converged)

1. **Default class shape:** `basename(argv[0]) + first-non-flag argv[1]` — unanimous;
   the headline literally needs the subcommand. One pure function, trivially retunable.
2. **Grouping-rule syntax:** boundary-aware literal **prefix** is Stage 2's default;
   ship the optional **`contains:` modifier** alongside it (the `sh -c` reality makes
   substring matching the practically-needed form; it stays opt-in so prefix remains the
   simple common case). No `filepath.Match` glob.
3. **`sh -c`:** default groups as `sh`; **no built-in shell parsing** (inference), and
   no curated wrapper list — recover wrapped commands via an explicit `contains:` rule.
   Set the expectation that the no-config win is mainly for direct execs.
4. **Secret scrub:** registered-secret scrub + bounds **only**; **defer** heuristic
   flag-redaction (over-redaction destroys the grouping signal; the residual literal-
   secret-in-argv exposure pre-exists via `dag.call`). Documented accepted scope; not a
   blocker.
5. **Encoding:** native interned `MetaID` **and** the OTel `wcprof.exec.argv` attr both
   carry the **same scalar JSON-array string**, routed through the shared
   `DumpEvent.MetaID` → `Build` seam (§4.1c). **Do NOT bump `DumpSchemaVersion`:**
   `ReadDump` hard-rejects a version mismatch (`if header.SchemaVersion !=
   DumpSchemaVersion { error }`, `dump.go`), so a bump would make the new reader
   **reject all existing v1 dumps**. The additive `omitempty` `MetaID` field is fully
   back/forward compatible at version 1 (old readers ignore the unknown JSON field; new
   readers see `MetaID == 0` on old dumps → empty argv → the blob). If a bump is ever
   wanted to advertise the capability, the reader must **first** be relaxed to accept
   `version <= DumpSchemaVersion`. *(This Round-2 correction supersedes the earlier
   "optional bump is fine." The encoding itself refines the council's raw-NUL suggestion
   to the no-control-byte JSON form chunk4 offered, for Cloud-storage survival; gated by
   §6 test 6.)*
6. **Bounds:** 64 tokens / 256 B per token / 4 KiB total **on the raw scrubbed slice**
   (the JSON attr is ~+200 B larger, §4.2), **never truncating `argv[0]` or `argv[1]`**
   (truncate trailing tokens first), with a fixed sentinel token that cannot be mistaken
   for a real arg.
7. **B4 capture point (Round 2):** capture the resolved user command **in core**, before
   both engine shims, via `execMD.ProfArgs` (§4.1a) — chosen over strip-at-emit
   (fragile, enumerates shims) and over the Run-entry capture (below both shims). Covers
   `Container.withExec`; other exec paths gracefully stay the blob.

## 9. Remaining open / deferred

- **Stage 2 grammar edge:** a `--exec-group` pattern containing a literal `=`
  (split-on-first-`=` can't express it) — accept the limitation or add a two-token form
  if a real workload needs it. Decide during Stage 2.
- **Coverage extension (post-Stage-1):** decompose non-`withExec` execs (Dockerfile
  `RUN` via the gateway frontend; **service starts** at `core/service.go:710,834`) by
  setting `ProfArgs` at their arg-resolution site — additive, same pattern (§4.1a).
  Decide if/when a real workload needs it.

## 10. Non-goals / risks

- **Not** changing the replay or the causal model (§4.7). The only `Graph` mutation
  beyond the additive field is the program-memo reset (a cache invalidation). If anyone
  finds a reason the replay *algorithm* must change, escalate — do not quietly do it.
- **Not** overlapping op-sets / `RunWhatIfsForOpSets` (§3, R-N); **not** a generic
  `Meta` map — argv only.
- **Risk:** one-off command cardinality — mitigated by the top-200-by-self-time what-if
  cap (§4.7), the class-table `--top` bound, and the user rule.
- **Risk:** argv-encoding survival on the Cloud path — mitigated by the scalar
  JSON-array string riding the proven convention, and gated by §6 test 6.
- **Risk:** argv scrub gaps for user-baked literal secrets — pre-existing via
  `dag.call`, documented; heuristic redaction deferred (§8.4).
- **Scope (not a risk — a stated boundary):** Stage 1 decomposes `Container.withExec`;
  execs that bypass the core capture (Dockerfile `RUN`, service starts, `execMD == nil`)
  stay the aggregated blob — consistent across sources and never a shim mislabel
  (§4.1a, §6 test 8).
- **Risk (cache):** `execMD.ProfArgs` must not perturb the exec cache key — argued safe
  by timing (set at run-time, after all execMD digests) in §4.1a; flagged for implementer
  verification.
