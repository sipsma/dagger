# Review — wcprof exec-decomposition design — chunk4/exec-split implementer

Reviewer: the exec-split implementer (`exec.run`/`containerStart`/`processRun`).
Verified every load-bearing claim against the landed code in the designer's
worktree (file:line below). Separates REAL issues from NOISE; severity-ranked.

## Verdict

**Sound enough to implement in 1–2 stages.** The core bet — relabel `op.Class`
and let `op.Key()` re-bucket every consumer with zero replay change — is **real
and verified**, and the design is appropriately minimal (the rejections in §3 are
correct). Two things MUST be nailed before/within implementation: **(1) the
`ClassifyExecs`-ordering vs the gate** (silent wrong ranking if mis-wired), and
**(2) the OTel argv wire encoding for the Cloud path** (the production target,
unverified). Both have clean fixes. The `sh -c` out-of-box limitation is a real
expectation-setter, not a blocker.

---

## REAL issues (ranked)

### 1. [HIGH] `ClassifyExecs` must run before the GATE, not merely before `WriteReport` — else the what-if silently ranks the stale blob

The mechanism depends on relabeling `op.Class` *before* the replay program
memoizes `classOf`. I traced the trigger precisely:

- `compileProgram` freezes `classOf[i]` from `op.Key()` at compile time
  (`replay.go:165-172`), and `g.program()` memoizes it once via `progOnce`
  (`graph.go:78-79`, `replay.go:122-127`).
- The **first** `g.program()` trigger in the OTel CLI is the **gate**:
  `analyze()` calls `CheckStructural` at `cmd/wcprof-otel-analyze/main.go:136`
  **before** `WriteReport` at `:143`; `CheckStructural` →
  `NewSimulation(g,nil)` (`gate.go:119`) → `g.program()` (`replay.go:333`).
- `WriteReport` then reuses the **already-memoized** program: `NewSimulation`
  (`report.go:162`) and `RunWhatIfs` (`:167`) both get the frozen `g.prog`
  (progOnce is now a no-op).

So if `ClassifyExecs` is inserted "after load, before `WriteReport`" (§5's
wording) but **after** the gate, the result is **silently inconsistent**: the
class **table** (`AggregateClasses`, reads `op.Key()` live, `report.go:192,227`)
shows `go build`/`git clone`, while the **what-if savings** (the actual ranking)
are computed on the frozen lumped `exec.processRun`. The ranking and its own table
disagree — exactly the "ship known-wrong output" failure the gate exists to
prevent.

§4.4 *names* the constraint ("before the first `g.program()`") but its "natural
flow Load → ClassifyExecs → report" and §5's "before `WriteReport`" both
**omit that the gate is the first trigger**. This must be made explicit.

**Fix (either; I recommend both):**
- **Place `ClassifyExecs` before `CheckStructural`** in the OTel CLI (and before
  `TopBottlenecks` in the oracle, which the design already gets right — `oracle.go:51`
  `RunWhatIfs` is *its* first trigger). I verified this is **safe**: the gate's
  verdict is class-independent — its Simulation schedules by causal structure
  (parent/child/waits), not by `Class`; `self>makespan`/cycles/unresolved-waits
  don't read `Class`. So classifying first cannot change the gate result.
- **Defensively, have `ClassifyExecs` reset the memoization** (`g.progOnce =
  sync.Once{}; g.prog = nil`) after mutating classes, making the pass
  **order-independent**. Then a future caller that simulates before classifying
  can't silently defeat it. Given this is a shared mutable `*Graph` and the whole
  design rests on the relabel landing, the reset is cheap insurance worth taking.

### 2. [MEDIUM → HIGH if Cloud drops arrays] The OTel argv encoding (`StringSlice`) is unverified on the production (Cloud) path; use a scalar string

The design verifies otlpdump renders an `ArrayValue` as `[]any`
(`hack/otlpdump/main.go:72-78`) — true. But **otlpdump is the local dev capture;
the OTel source's whole point is reading a Dagger *Cloud* trace** (`wccloud.Load`).
There:
- `cloud.SpanData.Attributes` is `map[string]any` from a GraphQL/JSON decode
  (`internal/cloud/trace.go:91`), passed straight to the loader
  (`wccloud/cloud.go:59`).
- **Every** existing wcprof OTel attr is a **scalar string** — digest, work_type,
  op_kind, and even int64s sent as *decimal strings* to survive Cloud's
  `map[string]any` JSON decode without float64 precision loss (`wccloud/cloud.go:31`
  spells this out). An argv `StringSlice` would be the **first array-valued
  attribute** in the wcprof vocabulary, and its survival through Cloud ingest +
  the GraphQL `attributes` field is **unverified**. The validation plan (§6 test
  #2) exercises only the otlpdump fixture, not a Cloud round-trip.

If Cloud stringifies or drops array attrs, the per-command feature works locally
but **not from a Cloud trace** — i.e., not for the actual "why was my CI slow"
use case.

**Fix:** encode the OTel argv as a **scalar string** (NUL-joined, mirroring the
native `MetaID` form the design already chose — or a JSON-array string the loader
`json.Unmarshal`s). This (a) matches the proven Cloud-safe scalar convention,
(b) **unifies** native and OTel onto *one* encoding so "same reconstructed
`[]string`" is trivially true (strengthening the very parity §4.1 has to argue
for), and (c) removes a dependency on unverified Cloud array support. The loader
splits on NUL — same `Build`-side helper for both sources. If the team would
rather keep `StringSlice`, then **add a Cloud round-trip test for an array attr**
to §6 and confirm the backend before Stage 1 lands.

### 3. [MEDIUM] `sh -c "<cmd>"` — the out-of-box win is degraded for the common CI case

The principled refusal to shell-parse `-c` (correct — that *is* inference) means
the **default** groups every `["sh","-c","go build ./..."]` as `sh` (§4.3). A large
fraction of real CI execs are shell-wrapped, so feature #1's headline ("a slow
`go build` headlines as `go build`") lands out-of-box mainly for **direct**
execs; shell-wrapped builds stay a `sh` blob until the user writes a Stage-2 rule.
This is honestly disclosed, but the council/Erik should weight it: the
**no-config** value is narrower than the headline implies. Not a blocker (the rule
mechanism recovers it, and parsing would violate the principle), but it argues for
prioritizing the `contains:` rule modifier (Open Decision #2) — a literal *prefix*
on the joined argv is awkward for `sh -c "cd x && go build"`-style variants, where
substring match is what the user actually needs.

### 4. [LOW] §4.5's `DupExecuted` framing is imprecise (no bug, but the validation will mis-target)

`DupExecuted` is a **call-side** metric: it counts repeats only for
`Outcome=="executed" || Kind=="call_exec"` ops, keyed on `Ident`
(`report.go:50-56`). The user-process op is `OutcomeOK`/`exec_phase`, **and** its
`Ident` is `state.id` — *unique per invocation* — so two identical `go build`s
produce two distinct `state.id`s and would never be dup-counted even if they were
on that path. Redundant-execution detection lives on the `call_exec`
(`Container.withExec`, `Ident=callKey`) class, which decomposition doesn't touch.
The design's "no regression" conclusion is **correct**, but "redundant
re-execution is still detected within each new per-command class" will mislead;
**validation test #1's dup assertion should target the `call_exec` class, not the
per-command `exec_phase` classes.** Keeping `Ident=state.id` is the right call
(it also drives `execByIdent` wait resolution, `graph.go:237-244`).

### 5. [LOW] `Op.Argv != nil ⟺ user-process op` is one-directional

A real exec relying on the image's default CMD/entrypoint can have empty
`Meta.Args` → empty `Op.Argv` → it falls through `ClassifyExecs`'s
`len(op.Argv)>0` and stays the blob. That's the *correct* outcome (nothing to
group on), but state the predicate as `⟹`, not `⟺`, so no future code treats
"no argv" as "not an exec." Also confirm the never-started path is intended: when
the process never starts, **neither** source emits `processRun`
(`executor_spec.go:1419` else-branch; `otelprof.go:82-86`), so there's no argv and
the time correctly ranks as engine `exec.containerStart` — consistent across
sources, no user self-time lost. Verified; just make it explicit.

---

## NOISE (verified non-issues — stated so they're closed)

- **The relabel mechanism is real.** `op.Key()` = `{Kind, Class}`
  (`graph.go:406-408`); `compileProgram` (`replay.go:165-172`) and
  `AggregateClasses` (`report.go:37`) both key off it. Mutating `op.Class`
  re-buckets both, with no replay/aggregation change. Confirmed.
- **`processRun` is the right argv home.** It's the leaf phase `[started,end]`
  carrying the user wall-time; `exec.run`'s self ≈ 0 (its `containerStart`+
  `processRun` children tile it). Scaling `processRun` self = "what if the command
  were faster." Confirmed against the split at `executor_spec.go:1414-1421`.
- **Parity by construction.** `state.procInfo.Meta` is in scope at **both** the
  native `RecordOp` (`:1417`) and the OTel `emitOTelExecSplit` (`:1431`) sites, so
  the same `Meta.Args` + the same `scrubArgv` feed both. Same source → same scrub
  → same classify → same `ClassKey`. The oracle (`oracle.go` `ClassKey` compare)
  holds *provided* §1/§2 fixes keep the reconstructed slice identical.
- **Loader stays zero-inference.** `class=s.Name; ident=attrStr(DagDigest);
  workType=attrStr(...)` (`loader.go:374-376`); `attrStrSlice` is the same trivial
  field-map. No name-parsing.
- **`Argv` not on `Ident`/`Class`** is correct — preserves `DupExecuted`
  +`execByIdent`, enables offline re-group. Confirmed.
- **Rejecting overlapping op-sets (R-N)** is correct: they'd force a `replay.go`
  change; a first-match partition maps 1:1 onto `ClassKey` with none.
- **Scrubber reuse is sound** (`NewSecretScrubReader` trie, `secret_scrub.go:19-58`);
  extract a `ScrubString` so the trie is built once per exec, not per token. The
  honest scope (user-baked literal secrets already flow via `dag.call`) is
  acceptable — it matches, not widens, the existing exposure.
- **Checksum unaffected** — an added attribute is not an added span. Trivially true.
- **Cardinality** — the top-200-by-self-time what-if cap (`replay.go:665`) means
  the slow command is always simulated. Real.

---

## Recommendations on the Open Decisions (§8)

1. **Default shape** → `basename(argv[0]) + first-non-flag arg`. **Adopt** — it's
   what makes the "`go build`" headline possible, and it's one pure function.
2. **Rule syntax** → literal **prefix** now; **add a `contains:` modifier in
   Stage 2** (don't defer indefinitely). The `sh -c` reality (#3) makes substring
   match the practically-needed form; prefix-on-joined-argv alone is awkward there.
3. **`sh -c`** → **accept default = `sh`; no built-in shell parsing** (parsing is
   inference — agree with the design). But set expectations that the no-config win
   is mainly for direct execs (#3), and lean on `contains:` + a documented example
   rule for the shell-wrapped case rather than any curated parser.
4. **Heuristic secret redaction** → **No.** Registered-secret scrub + bounds only.
   Flag-pattern redaction risks both leaks (false negatives) and destroying the
   grouping signal (false positives); the residual exposure is pre-existing via
   `dag.call`. Agree with the design.
5. **Native encoding** → interned NUL-joined `MetaID` (**adopt**) — *and* use the
   **same scalar NUL-joined string for OTel** (see #2), not `StringSlice`. Additive
   `omitempty` is sufficient for old-dump compat; a `DumpSchemaVersion` bump is
   optional/nice-to-have, not required.
6. **Bounds** (64 / 256B / 4KiB) → fine, with one hard rule: **never truncate
   `argv[0]` or `argv[1]`** — the group key depends on them, so truncate trailing
   tokens first (the design's "keep leading tokens" already implies this; make it
   explicit and test it, §6 test #5).

---

## Bottom line

Implement it, in the 2 stages proposed (Stage 1 independently shippable). The
architecture is correct and minimal and the replay genuinely stays untouched.
Before Stage 1 is considered done: **(a)** wire `ClassifyExecs` ahead of
`CheckStructural` and reset `progOnce` (issue 1 — the only path to a silent wrong
ranking), and **(b)** settle the OTel argv encoding for Cloud (issue 2 — scalar
string recommended, else prove Cloud preserves arrays). Treat issues 3–5 as
clarity/expectation fixes in the doc + tests. The cross-source oracle remains the
right strongest check (§6 test #3) — keep it, run it *after* both fixes.
