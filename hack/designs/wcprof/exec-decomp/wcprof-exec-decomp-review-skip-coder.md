# Review — wcprof exec decomposition design

Reviewer: council (implementer of the landed wcprof × OTel code), reviewing the NEW
design objectively. Verified every load-bearing `file:line` against the landed code
in this worktree at HEAD `c28d55ae7a` (the same HEAD the doc targets).

## Verdict

**Sound enough to implement in 1–2 stages — the core bet is verified correct — but
ONE design claim is wrong in a way that silently breaks the headline, and must be
corrected first.** The central mechanism (relabel `op.Class` → every consumer
re-buckets via `op.Key()`, replay untouched) holds against the code. The op-set
rejection (§3 R-N) is correct. Two must-fixes and three precision fixes below.

Ranked:
1. **[HIGH / MUST-FIX] The ordering claim (§4.4) is wrong: the gate runs a simulation, memoizing the program before the report.** Defeats the relabel for the what-if ranking unless `ClassifyExecs` runs *before* `CheckStructural`.
2. **[MEDIUM / SHOULD-FIX] OTel argv is a string *slice* (OTLP ArrayValue); only the otlpdump path is verified, not the Cloud trace API — the production path.** Use a NUL-joined *string* (like native + the completeness count) for guaranteed Cloud survival and identical wire encoding.
3. **[LOW-MED] §4.5 over-claims `DupExecuted`** for user execs (they are never on its path).
4. **[LOW] OTel-loader → `Op.Argv` seam under-specified** (shared `Build` reads native `MetaID`).
5. **[LOW] empty-argv exec** edge case in the `len(argv)>0` is-exec predicate.

## What I verified TRUE (the core bet is real)

- **`Op.Key()` is a pure function of `{Kind, Class}`** — `graph.go:405-408`
  (`ClassKey{Kind: op.Kind, Class: op.Class}`), `ClassKey` at `graph.go:84-87`. `Op`
  carries `Class`/`Ident` and **no argv/meta field** (`graph.go:22-23` — doc says
  24-25; trivial drift). Relabeling `Class` moves the key. ✔
- **Every downstream consumer keys off `op.Key()`** — replay `compileProgram`
  buckets `key := op.Key()` (`replay.go:165-172`), `maxWhatIfClasses = 200`
  (`replay.go:665`), candidate selection `totalSelf[op.Key()]` (`replay.go:679-681`);
  report `AggregateClasses` `key := op.Key()` (`report.go:36`). So a `Class` relabel
  re-buckets replay + report with **no replay/aggregation change**. ✔ **The crux of
  the design is confirmed against the code.**
- **`classifyKind` does NOT read `Class`** — it keys off `WcprofOpKindAttr`,
  `hasCallExecChild`, span `Name`, `DagDigestAttr` (`loader.go:493-507`). So losing
  the `"exec.processRun"` string on relabel is safe (§4.4). ✔
- **Loader is zero-inference** — `class := s.Name`, `ident := attrStr(…DagDigestAttr)`
  (`loader.go:374-376`). ✔
- **Native emit site is exact** — `wcprof.RecordOp(…OpKindExecPhase,
  "exec.processRun", OpOpts{Ident: state.id, WorkType: WorkTypeUser}…)` at
  `executor_spec.go:1417`; `state.id` is the `Ident`. ✔
- **argv is available at both emit sites** — `executor.Meta.Args []string`
  (`internal/buildkit/executor/executor.go:15`); `state.procInfo.Meta` is in scope at
  the emit site (`state.procInfo.Meta.ValidExitCodes` used at `executor_spec.go:1431`).
  OTel split call `emitOTelExecSplit(ctx, state.id, …)` at `executor_spec.go:1430`,
  user-phase span at `otelprof.go:92`. ✔
- **Scrub reuse is feasible** — `NewSecretScrubReader(r, env, secretEnvNames,
  secretFilePaths)` is reader/`censor`-based (`secret_scrub.go:19,49`), already wired
  at `executor_spec.go:876` with `secretFilePaths` built at `:858-869`. A
  `ScrubString` extraction (build the trie once, scrub each token) is the better of
  the two offered options. ✔
- **Oracle compares by `ClassKey`** (`oracle.go:130-140`, `otelByKey[c.Key]`) — so
  identical per-command `ClassKey`s on both graphs keep the oracle valid. ✔
- **otlpdump renders `ArrayValue → []any`** (`hack/otlpdump/main.go:71-77`). ✔ (dev
  path only — see issue #2.)
- **Completeness checksum unaffected** — adding an attribute to the existing
  `processRun` span creates no span, so `wcprof.session_span_count` is untouched.
  Trivially true. ✔
- **Op-set rejection (§3 R-N) is correct** — a partition (each exec → one `Class`)
  maps 1:1 onto the existing `ClassKey` model; overlapping op-sets would need an op in
  multiple scalable sets → a forbidden `replay.go` change. Overlapping *views* =
  re-run the offline analyzer (cheap). Sound. ✔

## REAL issues

### 1. [HIGH / MUST-FIX] The gate memoizes the replay program before the report — §4.4's "natural flow" is wrong

§4.4 says *"It must run after `Build`, before the first `g.program()` … The natural
flow Load → ClassifyExecs → report satisfies this."* **It does not — the structural
gate runs a simulation between Load and report.**

- `CheckStructural` calls `wcanalyze.NewSimulation(g, nil)` (`gate.go:119`) to get its
  cycle/unschedulable/start-conflict signals.
- `NewSimulation` calls `g.program()` (`replay.go:333`), which memoizes `classOf` /
  `classKeys` via `progOnce` (`replay.go:122-126`, `graph.go:78-80`).
- The analyzer flow is **Load → `CheckStructural` (`main.go:136`) → `WriteReport`
  (`main.go:143`)**, and `WriteReport` reuses that memoized program
  (`NewSimulation` at `report.go:162`, `RunWhatIfs` at `report.go:167`).

So if `ClassifyExecs` runs *after* the gate (a natural reading of §5's "after load,
before `WriteReport`", since the gate is also "before `WriteReport`"), the program is
already compiled with the **blob** classes. The precise failure:

- `RunWhatIfs` selects candidates by **live** `op.Key()` = `exec_phase:go build`
  (`replay.go:679-681`), and builds `Factors{ "exec_phase:go build": f }`.
- But `NewSimulation` maps factors onto the **memoized** `prog.classKeys`, which still
  contains `exec_phase:exec.processRun`, not `go build`. The factor matches no class
  index → **no scaling is applied** → the what-if "saving" for `go build` is **0**.
- Meanwhile `AggregateClasses` re-buckets **live** and shows `exec_phase:go build`
  with the correct self-time. Net: the class table shows "`go build` = 140s self" but
  the what-if ranking shows "`go build` saves 0s" — internally inconsistent and a
  **silently-wrong headline**, the exact failure class this effort exists to kill.

**Fix (small, but the design must state it):** `ClassifyExecs` must run **before
`CheckStructural`**, i.e. pin the call site in `runFiles`/`runCloud` as
`c, g := load(); ClassifyExecs(g, rules); analyze(c, g)` — not "before
`WriteReport`". Running it before the gate is harmless to the gate (its signals are
structural, class-granularity-independent). Add a test that asserts a per-command
exec class has a **non-zero** what-if saving **after** going through the full gate +
report path (i.e. catches the memoization-order regression specifically). Note: the
rules come from a CLI flag, so this cannot live inside `wcotel.Load`; it's a distinct
call in `main` between load and gate.

### 2. [MEDIUM / SHOULD-FIX] OTel argv as a string *slice* is verified only for otlpdump, not for Cloud — the production path

§4.1/§4.2 stamp `attribute.StringSlice(WcprofExecArgvAttr, argv)` and rely on
"the slice rides as an OTLP `ArrayValue`", validated via
`hack/otlpdump/main.go:71-77`. But the feature's purpose is **Cloud** traces, and the
Cloud path is different: `internal/cloud/trace.go:91` decodes `attributes` as
`map[string]any` from the Cloud API's JSON, and `wccloud.SpanFromCloud` passes it
straight through (`cloud.go:59`). **Whether the Dagger Cloud backend preserves an
OTLP array-valued attribute as a JSON array is not verifiable from this repo** (it's
the dagger.io backend), and is left unverified by the doc.

This matters because the codebase already learned this lesson and chose **strings**
for everything that must survive Cloud: the completeness count is string-encoded
"so it survives Cloud's JSON without float64 coercion" (`telemetryattrs/attrs.go`
WcprofSessionSpanCountAttr), and the wait-ns ride as decimal strings for the same
reason. A string slice departs from that established pattern.

**Recommendation:** encode the OTel argv as a single **NUL-joined string** attribute
(`attribute.String(WcprofExecArgvAttr, strings.Join(argv, "\x00"))`), identical to the
native `MetaID` wire form (§4.1). This (a) is guaranteed to survive Cloud like every
other wcprof attr, (b) makes the two wire encodings **byte-identical** — the strongest
possible parity, removing the "two encodings reconstruct to the same slice" assumption
— and (c) simplifies the loader: `attrStr` + split on NUL, no new `attrStrSlice`
helper, no `[]any`→`[]string` coercion, no array-survival dependency. If the team
prefers the slice, it is a **blocker to verify against a real Cloud trace before the
OTel side ships** — do not assume.

### 3. [LOW-MED] §4.5 over-claims `DupExecuted` for user execs

§4.5: *"`DupExecuted` is unchanged and correct … redundant re-execution is still
detected within each new per-command class."* The user `processRun` op is `OutcomeOK`
(`"ok"`) + `Kind = exec_phase`, and the dup path is gated on
`op.Outcome == "executed" || op.Kind == "call_exec"` (`report.go:39`). **So the user
exec op is never on the `DupExecuted` path at all** — dup count is 0 for these ops
regardless of grouping. The doc's own §1.4/§4.5 parentheticals admit the `OutcomeOK`
fact, contradicting the headline claim. There is **no regression** (it was never
counted), but the drill-down "individual execs with counts" (§4.5) needs its **own**
new logic over `Op.Argv`/`Ident`, not the existing `DupExecuted`. Correct the claim;
keep the drill-down as explicitly-new, additive work.

### 4. [LOW] The OTel-loader → `Op.Argv` seam is under-specified

The OTel loader builds `wcprof.DumpEvent`s and calls the **shared**
`wcanalyze.Build`. §4.1 has `Build` recover argv from the native interned
`MetaID` (split on NUL). §5 only says the loader should "read `WcprofExecArgvAttr`
into the op's argv" — it doesn't say how that reaches the shared `Build`. The loader
must either intern the joined argv into its own string table and set
`DumpEvent.MetaID` (so the one `Build` path works for both sources — the natural
choice, and it falls out for free if issue #2 adopts the NUL-joined string), or
`DumpEvent` gains a parallel `Argv` field. Specify it; it's the cleanest argument for
the NUL-joined-string encoding.

### 5. [LOW] empty-argv exec edge case

§4.1 states `Op.Argv != nil ⟺ user-process op` and §4.4 keys the relabel on
`len(op.Argv) > 0`. A real exec whose `Meta.Args` is empty (unusual but possible for a
degenerate spec) would carry no argv → it is silently **not** relabeled and keeps the
`exec.processRun` blob class. That's graceful (no crash), but the predicate isn't a
clean exec identity. Specify the fallback (keep `exec.processRun`, or a stable
`exec (unknown)` label) so argv-less execs don't quietly mix with the blob across
sources.

## NOISE (checked, dismissed)

- **Line-number drift**: `Op.Class`/`Ident` are at `graph.go:22-23` (doc: 24-25);
  similar ±2 offsets elsewhere. Substantively every claim resolves to the right code.
  Not material.
- "Adding an attribute changes the completeness count" — a non-issue; I verified it's
  trivially fine (no new span). The doc correctly pre-empts it.
- "Replay must change" — verified it does **not**; the doc's §4.7 claim is accurate.

## Does it achieve the goal on a real/concurrent trace?

Yes, **with fix #1 in**. Class bucketing is orthogonal to concurrency — the replay
already handles parallelism; classes are labels. Parallel `go build`s each get argv →
class `go build` → aggregated → the what-if scales the class's **total** self-time
across all instances = "what if go build were faster," the right counterfactual, and
the top-200-by-self-time cap (`replay.go:665`) guarantees the heavy command is
simulated. The honest misses: (a) `sh -c "<cmd>"` groups as `sh` until a user rule
(correctly refusing to shell-parse = inference); (b) **the most important miss is
issue #2** — if argv doesn't survive Cloud, the feature works in otlpdump dev but
silently does nothing on the production Cloud trace, which is the entire use case.

## Open Decisions (§8) — recommendations

1. **Default class shape:** `basename(argv[0]) + first-non-flag-argv[1]`. **Agree.**
   It's a total pure projection and the headline literally needs the subcommand
   ("`go build`", not "`go`"). Don't over-think; it's trivially retunable.
2. **Grouping-rule syntax:** literal-prefix `--exec-group='prefix=label'`. **Agree;
   ship without `contains:`** (YAGNI; add later if a real workload needs it). The
   no-`filepath.Match` lesson is correct — argv is full of `/`.
3. **`sh -c`:** accept the `sh` default + user rule; **no built-in parsing.** **Agree
   strongly** — parsing `-c` is inference, a hard no. A curated built-in wrapper list
   is also a slippery slope toward name-parsing; leave it to explicit user rules.
4. **Heuristic secret redaction:** registered-secret scrub + bounds **only**; defer
   flag-redaction. **Agree.** Over-redaction destroys the grouping signal; under-
   redaction is false security; the literal-secret-in-argv exposure already exists via
   the `dag.call` attribute, so this adds no exposure class. Document the scope as an
   accepted limitation — **not a blocker.**
5. **Native wire encoding:** interned NUL-joined `MetaID uint32`. **Agree for native**
   (mirrors `IdentID`, keeps `Event` flat, dumps compact) — **and extend the same
   NUL-joined-string form to OTel** (issue #2) for parity + Cloud survival. On schema:
   additive `omitempty` is enough for read-compat, but **bump `DumpSchemaVersion`
   anyway** (cheap; makes the new capability explicit and lets an old analyzer say so).
6. **Bounds (64 tok / 256 B / 4 KiB + sentinel):** **Accept.** The group key is
   `argv[0..1]` (basename + subcommand), which the caps always preserve, so even
   aggressive truncation never harms grouping; 4 KiB total is generous for drill-down.
   Confirm the sentinel is a fixed non-argv token (e.g. `"…(+N more)"`) so it can't be
   mistaken for a real arg by a prefix rule.

## Bottom line

Implementable in the proposed 2 stages. **Before Stage 1: (a) correct §4.4 and pin
`ClassifyExecs` before `CheckStructural`, with a regression test that a per-command
exec class has non-zero what-if saving through the full gate→report path; (b) switch
the OTel carrier to a NUL-joined string and/or verify array-attr survival on a real
Cloud trace.** Fix the §4.5 `DupExecuted` wording and specify the loader seam (#4) and
empty-argv fallback (#5). With those, the design is faithful to the principles
(zero-inference grouping on emitted argv, replay untouched, native↔OTel parity by
construction) and achieves the user-work-first-class goal.

---

# Round 2 — verification of the revised design

Re-verified against the same HEAD `c28d55ae7a`. Architecture not re-litigated (it's
confirmed). I checked each R1 fix against the code and hunted for new issues.

## Verdict: **CONVERGED** — sound to implement in 1–2 stages. No remaining blocker.

All three blockers are correctly specified against the landed code; both should-fixes
land; and the B3 encoding choice is **better than my R1 suggestion**. Only LOW
implementation notes remain (below) — none gating.

## Blocker fixes — verified

### B1 — capture argv before the `/.init` shim (the designer's own catch; correct)
Confirmed the premise and the fix against code:
- `injectInit` prepends the shim: `initPath := "/.init"` (`executor_spec.go:369`),
  `state.procInfo.Meta.Args = append([]string{initPath}, state.procInfo.Meta.Args...)`
  (`executor_spec.go:375`), `NoInit` early-return at `:365`. It runs as setup func **#2**
  inside `c.run`, and the emit lives in `runContainer` (setup func **#16**) —
  `executor.go:143-159` lists the order — so at emit time `argv[0] == "/.init"`. The B1
  premise is real.
- The fix point is exact: `state := newExecState(&procInfo,…)` then `c.run(…)` at
  `executor.go:~102-143`; capturing `slices.Clone(procInfo.Meta.Args)` in that gap is
  **before** any setup func mutates it. `injectInit` does `append([]string{initPath}, …)`
  (fresh slice) so the clone is clean regardless; `NoInit` is handled for free (capture
  precedes the prepend either way). The entrypoint is container config resolved upstream
  (`core/container.go:1967` `cfg.Entrypoint = …`), i.e. already in `Meta.Args` at entry —
  so "keeps the entrypoint, excludes `/.init`" holds. ✔
- The never-started path is real and symmetric: native records only `containerStart` in
  the `else` (`executor_spec.go:1418`), OTel mirrors it (`otelprof.go:81-86`) — no
  `processRun`, no argv, ranks as engine overhead on both sources (§4.1d). ✔

### B2 — classify before the gate + invalidate the program memo (my R1 blocker; fixed)
- `progOnce`/`prog` is the **only** memoized, class-derived cache on `Graph`
  (`graph.go:78`; `selfSegments` on `Op` is a per-op interval, not class-derived), so
  resetting it is **sufficient** — nothing else caches a stale class map. ✔
- The reset is **lint-clean**: I compiled + `go vet`'d `g.progOnce = sync.Once{}; g.prog
  = nil` in a scratch module — **no copylocks complaint, exit 0** (composite literals
  aren't flagged). So `invalidateProgram()` is valid Go, not a vet violation. ✔
- The gate is genuinely **class-independent**: `CheckStructural`'s `NewSimulation(g, nil)`
  (`gate.go:119`) replays with all-1.0 factors, so the schedule (and hence cycles /
  unschedulable / start-conflicts / self>makespan) is identical regardless of how ops
  bucket into classes — classifying first cannot change the verdict. ✔
- Ordering pinned correctly per entry point: OTel CLI calls `ClassifyExecs` as the first
  line of `analyze()` before `CheckStructural` (`main.go:136`) → `WriteReport`
  (`:143`); native CLI has **no gate** (`LoadMulti` `:73` → `WriteReport` `:77`), so
  classify-after-load precedes `WriteReport`'s own `baseSim` (`report.go:162`). ✔ The
  belt-and-suspenders memo reset makes it order-independent regardless. *(Caveat, LOW:
  the reset is safe because the analyzers process one graph single-threaded; it would be
  a data race only if a `Graph` were shared across goroutines mid-simulation, which is
  not how the analyzers use it — worth one sentence in the doc.)*

### B3 — Cloud-survivable, byte-identical encoding (my R1 issue; fixed, and improved)
- The scalar-string Cloud-survival convention is real and documented: `wccloud`
  decodes attrs to `map[string]any` and `SpanFromCloud` passes them straight through
  (`cloud.go:59`); the doc comment at `cloud.go:29-33` spells out that scalar strings
  round-trip and int64s ride as decimal strings for exactly this reason. A single
  scalar **JSON-array string** rides this proven path; both sources `json.Marshal` the
  **same** scrubbed slice → byte-identical → the oracle's reconstructed `Op.Argv` is
  provably equal. ✔
- The loader seam (my R1 #4) is now well-specified and feasible: the loader already
  has a string table (`newStringTable` `loader.go:351`, `str.intern` `:611`) and emits
  `DumpEvent`s with interned `ClassID/IdentID` (`:393-394, 400`); adding
  `MetaID: str.intern(argvJSON)` mirrors them, and `DumpEvent.IdentID/ClassID` are
  `omitempty` (`dump.go:34-35`) so `MetaID` is additive. One shared `Build`
  (`json.Unmarshal(str(ev.MetaID))`) serves both sources. ✔
- **Credit where due:** the designer's JSON-array string is *better* than my R1
  raw-NUL suggestion. A raw `\x00` separator is a control byte that string stores reject
  or truncate (Postgres `TEXT` forbids `0x00`) — it could itself fail Cloud storage and
  defeat the very goal. The JSON form is control-byte-free, unambiguous, and drops the
  `attrStrSlice` helper. Good catch; adopt it.

## Should-fixes — verified
- **Scrub plumbing:** confirmed `secretFilePaths` is a **local** in
  `setupSecretScrubbing` (`executor_spec.go:~858`, used at `NewSecretScrubReader`
  `:876`), with the no-secrets early-return at `:846`; it runs as setup func **#10**,
  before the emit (#16) — so stashing it on `state.profSecretFilePaths` is both
  necessary and correctly ordered. ✔
- **`DupExecuted` (§4.5):** now framed correctly — the `processRun` op
  (`OutcomeOK`/`exec_phase`, `Ident=state.id` unique-per-invocation) is never on the dup
  path (`report.go:50`), which is right (distinct runs) and a non-regression; dup
  detection stays on the `call_exec` class (`Ident=callKey`, repeatable), which
  decomposition doesn't touch, and §6's dup assertion correctly targets `call_exec`. ✔
- **Empty-argv predicate (§4.1d):** the `len(Argv)>0 ⟹ user-process op` one-directional
  framing + the argv-less-stays-blob and never-started outcomes are correct and
  cross-source-consistent. ✔
- **Boundary-aware `--exec-group` (§4.6):** `joined == match || prefix match + " "`
  closes the `go build`-matches-`go buildx` footgun; the opt-in `contains:` modifier
  directly addresses the `sh -c` limitation. Sound. ✔

## New issues (all LOW — implementation notes, not gating)
1. **Pseudocode signature.** `attribute.String(WcprofExecArgvAttr, json.Marshal(argv))`
   (§4.1c, §5) doesn't compile as written — `json.Marshal` returns `([]byte, error)`.
   Impl: `b, _ := json.Marshal(argv); attribute.String(…, string(b))` (Marshal of a
   `[]string` never errors). Same shorthand in the native `RecordOp` path. Trivial; just
   don't copy-paste.
2. **Guard empty `MetaID` before `Unmarshal`.** `Build`'s
   `json.Unmarshal(str(ev.MetaID))` must short-circuit when `ev.MetaID == 0` /
   `str == ""` (non-exec ops) → `Op.Argv = nil`; `json.Unmarshal([]byte(""), …)` errors.
   §4.1c says "empty `MetaID` ⇒ `Op.Argv == nil`", so the intent is right — make the
   empty check explicit so non-exec ops don't generate spurious decode errors.
3. **Concurrency note on the memo reset** (see B2 caveat) — add one sentence that the
   reset assumes single-threaded per-graph analysis.
4. **Entrypoint-presence** is architecture-consistent (`core/container.go:1967`) but the
   exact upstream prepend into `Meta.Args` wasn't pinned to a line; the load-bearing fact
   (no `/.init` at capture) **is** verified, so this is informational.

## One-line verdict
**CONVERGED** — all three R1 blockers correctly fixed and verified against the code
(B1 `/.init` capture, B2 classify-before-gate + lint-clean memo reset, B3 scalar
JSON-array string riding the proven Cloud path); should-fixes resolved; only LOW
impl-notes remain. Ship Stage 1.

---

# Round 3 — final convergence check (B4 delta only)

Scoped to the one new blocker (the QEMU second-shim) and whether the revision
introduced anything new. Architecture + the three R1 fixes + the encoding were
confirmed in R2 and not re-litigated. Verified against HEAD `c28d55ae7a`.

## Verdict: **CONVERGED.** No remaining blocker.

B4 is correctly fixed and the cache-key safety — the one load-bearing claim the doc
deferred to "implementer verification" — **I performed it: it holds decisively.** All
five R2 minor folds are correctly applied, including the schema correction that
overturns my own R2 advice.

## B4 — verified

**1. Resolves the blocker + subsumes `/.init`.** Two shims at two layers, both
confirmed and both before the emit: QEMU is prepended in core —
`metaSpec.Args = append([]string{engineutil.DaggerQemuEmulatorMountPoint}, metaSpec.Args...)`
(`core/container_exec.go:2043`; `DaggerQemuEmulatorMountPoint = "/dev/.dagger_qemu_emulator"`,
`executor_spec.go:73`) — then `/.init` later in the executor (`executor_spec.go:375`).
So the R2 `Run`-entry capture sits below **both** for emulated execs — and emulated
multi-arch builds are exactly the slow, high-value candidates. The new capture point
(just before `:2043`, where the QEMU prepend is the **only** in-file mutation of
`metaSpec.Args`, so the value there is the fully-resolved command) is **before both
shims** and a single pre-everything capture, so it **subsumes B1** (no per-shim
stripping, robust to any future shim). The strip-at-emit alternative is correctly
rejected (it couples the emit to an enumerated shim list). ✔

**2. Cache-key safety — DECISIVELY SAFE (I did the deferred verification).** The
run-time `execMD` is a **fresh struct**, not aliased to any digested value:
`execMeta` does `execMD := engineutil.ExecutionMetadata{}` (`container_exec.go:229`),
copies parent **by value** (`= *parent`, `:231`), reads the **recipe** digest and
stores it (`execMD.CallDigest = curCall.RecipeDigest(ctx)`, `:244-248`), and returns
`&execMD` (`:313`); the run path re-binds `execMD, err := container.execMeta(…)`
(`:1287`). The dagql call/recipe digest is a property of the call args (the serialized
`ExecMD` field, `core/schema/container.go:1452`) fixed at **call construction**;
`ProfArgs` is set at **run** time (`:2042`) on the fresh struct, **after**. The digest
sites — `go_sdk.go:424`, `module_typedefs.go:107`, and the recipe digest — all predate
the run. And I grep-traced the run path **after `:2042`**: `execMD` is only **read**
(`:2084` `UseRecipeIDsByDefault`, `:2114` passed to the executor, which uses it for
run-time only — `SecretEnvNames`, `CallDigest`-as-Ident — never digesting it). **No
re-serialize/re-digest of `execMD` after the capture.** It is set unconditionally
(`if execMD != nil`, not profiling-gated) and is a deterministic clone of the args, so
it cannot perturb a key, cause a spurious miss, or make caching depend on profiling
state. The doc's "implementer verification" ask is therefore **already satisfied** —
no implementer action needed. ✔✔

**3. Coverage boundary — honest, no silent gap.** `executor.Run` is a chokepoint also
reached by the Dockerfile frontend, but the capture lives only on the core `withExec`
path, so Dockerfile-`RUN` / `execMD == nil` execs carry no `ProfArgs` → empty
`Op.Argv` → they **stay the `exec.processRun` blob**: the pre-feature status quo,
identical on both sources, and **never a shim mislabel** (the failure mode the whole
fix exists to prevent). Stated explicitly (§4.1a:300-312) with an additive extension
path. The emit-site read is nil-guarded — §4.2:404-408 spells out that `execMD == nil`
(or empty `ProfArgs`) emits no argv, not a panic, and `execMD` is genuinely nilable at
the executor (`executor_spec.go:1215` already nil-checks it). ✔

**4. Minor folds — all applied correctly.**
- `json.Marshal` `([]byte, error)` form fixed (§4.1c:337-340), with omit-on-error. ✔
- Empty-`MetaID` guard in `Build`: `json.Unmarshal` **only when `str(ev.MetaID) != ""`**
  (mirrors the `Ident` `str()` guard), malformed ⇒ `Op.Argv == nil`, never a panic
  (§4.1c:352-356). ✔
- Nil-safe scrub incl. the `execMD == nil` emit case (§4.2:404-408). ✔
- **Don't bump `DumpSchemaVersion`** (§8.5) — and this corrects **my own R2 advice**.
  I verified the reader **hard-rejects** any mismatch:
  `if header.SchemaVersion != DumpSchemaVersion { return …"unsupported dump schema
  version"… }` (`dump.go:176-177`, `DumpSchemaVersion = 1` `:12`). Bumping would make
  the new reader reject old dumps and vice-versa; the additive `omitempty` `MetaID`
  field is read-compatible both directions (unknown field ignored; absent ⇒ 0 ⇒ nil
  argv). The designer's call is right; mine was wrong. ✔
- Bound applies to the **raw scrubbed `[]string`**, never truncating `argv[0..1]`, with
  the JSON-size note (+~200 B) within OTel limits (§4.2:410-415). ✔

## New issues introduced by the revision?
**None.** I checked the capture point (the only in-file `metaSpec.Args` mutation is the
QEMU prepend, so the value captured is clean), the fresh-struct cache argument
(holds), the nil paths (guarded), and the parity (both emit sites read the same
`state.execMD.ProfArgs`, so emulated execs now headline as the user command on **both**
sources consistently). No regression, no over-claim, no broken seam.

(NOISE: the prompt's hint "`NoInit` at `executor.go:72`" is off-by-one — it's `:73`;
the doc itself cites the `NoInit` check correctly at `executor_spec.go:365`.)

## One-line verdict
**CONVERGED** — B4 resolves the QEMU/`/.init` double-shim with an upstream
`execMD.ProfArgs` capture that is verified cache-key-safe (fresh run-time struct, no
re-digest after the capture point), coverage-honest, and parity-preserving; all five
minor folds (incl. the correct no-schema-bump) are applied; no new issue. Ship.
