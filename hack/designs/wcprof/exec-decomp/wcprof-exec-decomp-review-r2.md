# Round-2 review — wcprof exec-decomposition design — chunk4/exec-split implementer

Reviewer: the exec-split implementer. Verified the three blocker fixes + the
precision fixes against the landed code (file:line below). Did not re-litigate the
architecture (confirmed in R1). Hunted for new issues.

## Verdict: **CONVERGED** — sound to implement in 1–2 stages. No remaining blocker.

All three R1 blockers are correctly specified against the code, every should-fix is
folded in faithfully, the six Open Decisions resolved as recommended, and the
revision introduced nothing that rises above micro-nit. Citations I spot-checked
are accurate.

---

## Blocker fixes — verified

### B1 (`/.init` capture point) — CORRECT, and it's a real catch I missed in R1

The mutation is exactly as the doc states: `injectInit`
(`executor_spec.go:364-378`) — NoInit guard `:365`, `initPath := "/.init"` `:369`,
`state.procInfo.Meta.Args = append([]string{initPath}, …)` `:375` — and it runs as a
**setup func inside `c.run`** (`executor.go:144`, second in the list), *after*
`newExecState` (`:99-114`) and *before* the emit in `runContainer` (last setup func,
`:159`). So capturing `state.profRawArgs = slices.Clone(procInfo.Meta.Args)` between
`newExecState` and `c.run` precedes the prepend.

The load-bearing fact that makes capture-at-entry **faithful**, which I verified by
grep: **`:375` is the *only* non-test `Meta.Args` mutation in the entire executor**
(`engine/engineutil/*.go`). So the pristine entry value is `[entrypoint, user-args]`
— the entrypoint is necessarily upstream (no setup func adds it, yet it runs), and
`/.init` is the only thing stripped. `NoInit` is handled for free (capture precedes
the prepend whether or not it happens). `slices.Clone` is correct and defensive
(injectInit reassigns the field, so even an in-place mutation by a future setup func
can't corrupt the captured copy). Both emit sites (`:1417`/`:1430`) read the one
captured `profRawArgs`. **Faithful.**

### B2 (classify before the gate) — CORRECT; reset mechanism is vet-clean and race-free

§4.4 folds in **both** my R1 recommendations: (1) call `ClassifyExecs` before
`CheckStructural` in every entry point, and (2) `invalidateProgram()` resetting
`g.progOnce`/`g.prog`. The gate-class-independence justification is right (I
reconfirmed: `CheckStructural`→`NewSimulation`→`g.program()` at `gate.go:119`/
`replay.go:333`; the gate's invariants schedule by causal structure, never read
`Class`).

I checked the two things that could have made the *reset mechanism itself* a new
bug:
- **`g.progOnce = sync.Once{}` and `go vet` copylocks:** I built a scratch module
  with exactly this assignment and ran `go vet` — **clean, no copylocks**. So the
  specified reset compiles lint-clean. (I flagged this as a possible new issue and
  it isn't.)
- **Concurrency:** `RunWhatIfs` (`replay.go:665-712`) is **sequential** — no
  goroutines/errgroup around `NewSimulation`/`program()`. So resetting the memo
  cannot race a concurrent compile. The reset is sufficient and safe.

The "replay untouched (§4.7)" claim survives: `invalidateProgram` is a cache
invalidation on the `Graph`, not a change to `compileProgram`/the replay algorithm.
Fair.

### B3 (Cloud-safe encoding) — CORRECT; the shared seam is real

§4.1c carries argv as **one scalar `json.Marshal(argv)` string on both sources**,
routed through a shared `DumpEvent.MetaID` → `Build` seam. Verified the seam exists:
the OTel loader's `Compile` produces `[]wcprof.DumpEvent` (`wcotel/loader.go:75`,
appended `:400`/`:456`) that flows through the **same** `wcanalyze.Build`
(`loader.go:150`). So both the native dump and the OTel loader can set `MetaID`
(interning the JSON string into their own string table), and one `Build` recovers
`Op.Argv` via `json.Unmarshal(str(ev.MetaID))`. Cloud survival holds — `SpanData.
Attributes` is `map[string]any` passed straight through (`wccloud/cloud.go:59`), and
a scalar string is the proven-safe form (`cloud.go:29-33`; int64s already ride as
decimal strings). Choosing JSON over raw-NUL is well-justified (no control bytes →
no `0x00`-in-`TEXT` store rejection; unambiguous; drops the `attrStrSlice` helper).
Both wire forms are byte-identical (`json.Marshal` of the same scrubbed slice), so
oracle parity is provable. This adopts my R1 recommendation in its stronger form.

---

## Should-fixes — all folded in faithfully

- **Scrub plumbing (§4.2):** `setupSecretScrubbing` is verified at `executor.go:153`,
  ahead of `runContainer` (`:159`) where the emit lives, so stashing the resolved
  list on `state.profSecretFilePaths` is sound; the no-secrets early-return
  (`:846-848`) leaving the stash nil → passthrough is correctly handled. Bounds now
  **never truncate `argv[0]`/`argv[1]`** with a non-arg sentinel — exactly my R1 ask.
- **`DupExecuted` (§4.5):** reframed precisely — it's a call-side metric
  (`report.go:50`, `Ident=callKey`); the per-command `exec_phase` classes have a
  unique `state.id` per run, are *never* on the dup path, and that's a correct
  non-regression; validation's dup assertion now targets the `call_exec` class. Fixes
  my R1 #4 verbatim.
- **Empty-argv predicate (§4.1d):** stated as `len(Argv)>0 ⟹ user-process op`
  (one-directional), with the never-started path (no `processRun`, no argv,
  consistent across sources) called out. Correct.
- **Grammar (§4.6):** boundary-aware prefix (`joined==match` or starts-with
  `match+" "`) closes the `go build`-matches-`go buildx` footgun; opt-in `contains:`
  handles the `sh -c` case. First-match partition, no glob. Right.
- **Open Decisions (§8):** all six resolved as I recommended (default
  `basename+subcommand`; prefix+`contains:`; `sh -c`→`sh` no parsing; registered-
  scrub+bounds only; scalar JSON-array string both sources; bounds with protected
  `argv[0..1]`). `DumpSchemaVersion` bump correctly noted optional-but-recommended.

---

## New-issue hunt — only micro-nits (NOISE; none blocks)

- **`json.Marshal` may run twice** — once in the recorder (native `OpOpts.Argv
  []string`) and once at the OTel emit (`json.Marshal(argv)`). Deterministic →
  byte-identical, so parity is unaffected; could marshal once and share the string,
  but it's a micro-optimization, not a correctness point. (§4.1b says "encode runs
  once" while §4.1c implies two marshals — harmless mismatch.)
- **`Build` must guard the empty `MetaID`** before `json.Unmarshal` (i.e.
  `str(MetaID)=="" ⇒ Op.Argv=nil`, not `json.Unmarshal("")` which errors). The doc
  states the nil-on-empty behavior; it's the same `str()`-returns-"" pattern as the
  existing `Ident`, so it's an implementation reminder, not a gap.
- **The 4 KiB bound is on the raw slice**, so the JSON-encoded attribute is
  marginally larger (≈ +2 B/token + escapes, ~200 B for 64 tokens). Negligible;
  worth one clause if the cap is meant to bound the emitted attribute.

I specifically re-checked that the B1 capture covers service/nested-client execs
(they go through the same `executor.Run`, so they get `profRawArgs`) and that the
`MetaID` interning preserves `LoadMulti`'s "string table only grows" invariant (it
interns like `Class`/`Ident`) — both fine.

---

## One-line verdict

**CONVERGED** — all three blockers and every should-fix are correctly specified
against the landed code, no new blocker introduced; implement in the 2 staged PRs as
written.
