# Implementation review — wcprof exec decomposition

Reviewer: council (designed/converged the contract + built the completeness checksum).
Verified the two commits (`463f05b0ba` Stage 1, `a691bec084` Stage 2) on branch
`wcprof-exec-decomp-impl-dea4c5e3` against the actual code and the converged design.
Ran the tests. `file:line` are in the implementer's worktree.

## Verdict: **CORRECT + FAITHFUL + READY TO MERGE** (modulo the deferred live-engine oracle).

The implementation realizes the converged design with no silent divergence. Both
critical placements are right, the principles hold in the code, replay/causal logic is
untouched, the 21 new tests are real and pass, and the one pre-existing-test fix is mine
to own and does not weaken the completeness gate. No must-change items.

## 1. Faithfulness — implements the contract

- **`execMD.ProfArgs` capture** (`core/container_exec.go`): `if execMD != nil {
  execMD.ProfArgs = slices.Clone(metaSpec.Args) }` sits **before** `if emu != nil`,
  unconditional — so the common non-emulated `withExec` is captured too, and emulated
  execs are captured before the QEMU shim. ✔
- **Scalar JSON-array encoding on BOTH sources, byte-identical.** Native:
  `internArgv` does `json.Marshal(argv)` → `Intern` → `MetaID` (`record.go`). OTel:
  `emitOTelExecPhase` does the **same** `json.Marshal(argv)` → `attribute.String(
  WcprofExecArgvAttr, …)` on the user phase only (`otelprof.go`). Same slice, same
  marshal ⇒ identical bytes. ✔
- **`Op.Argv`** additive on `graph.go`'s `Op`; `Build` fills it via `decodeArgv(
  str(ev.MetaID))` for both ended and open ops. ✔
- **`ClassifyExecs` before the gate + `invalidateProgram`** — see §3. ✔
- **Default projection** (`classify.go` `defaultExecClass`): `path.Base(argv[0])` + first
  non-flag `argv[1]`. Correctly uses `path.Base` (container path), not OS `filepath.Base`.
  Yields `go build`, `git clone`, `sh` for `sh -c`. ✔
- **`--exec-group` boundary-aware prefix** (`classify.go` `matches`): `joined == match ||
  HasPrefix(match+" ")` (so `go build` ∤ `go buildx`), plus opt-in `contains:`. Repeatable
  flag parsed first-match-wins. ✔

## 2. Principles hold in the CODE

- **Replay/causal logic UNCHANGED.** `replay.go` is **not in the diff**. The only
  `graph.go` changes are the additive `Op.Argv`, `decodeArgv`, `Build` copying it, and
  `invalidateProgram` (`g.progOnce = sync.Once{}; g.prog = nil` — a memo reset, not an
  algorithm change). ✔
- **Zero inference.** `defaultExecClass` is a pure argv projection (no shell-parse, no
  name-parse); `sh -c` deliberately groups as `sh`. The loader maps the attr through
  untouched (`MetaID: str.intern(argv)`, `loader.go`); `decodeArgv` is `json.Unmarshal`
  of the explicit emitted string, empty/malformed ⇒ nil (defensive, never a panic, never
  inferred). ✔
- **Native↔OTel parity.** Same scrubbed slice → same `json.Marshal` → identical wire form
  → identical `Op.Argv` → identical `ClassKey`. Proven by `TestCrossSourceOracleSameGroups`
  asserting `cmp.Agrees(1.0, 0.0)` — exact jaccard 1.0, zero drift. ✔
- **Bounded + scrubbed, never truncating `argv[0..1]`.** `execProfArgv` reuses the
  registered-secret censor (the stashed `state.profSecretFilePaths`); `boundProfArgv` caps
  64 tok / 256 B / 4 KiB and **only byte-truncates tokens at `i >= 2`**, always keeping
  `min(len,2)`, with an ellipsis sentinel. `TestScrubStringsRedactsSecret` proves a
  registered secret value in argv becomes `***`; `TestBoundProfArgvPreservesKeyTokens`
  guards the key tokens. ✔

## 3. The two critical placements — VERIFIED

**(a) Capture is unconditional, before `if emu != nil`.** Confirmed in
`container_exec.go` (above). It clones `metaSpec.Args` (the resolved command, before the
QEMU prepend, the only in-file `metaSpec.Args` mutation). ✔

**(b) `json:"-"` is correct, and the data path survives.** Three things, all verified:
- `ExecutionMetadata` **is** JSON-serialized into exec cache keys:
  `SerializedString[T].MarshalJSON` does `json.Marshal(s.Self)` (`dagql/types.go:671`),
  and the `withExec` `ExecMD` arg is a `dagql.SerializedString[*ExecutionMetadata]`. So
  `json:"-"` on `ProfArgs` (`executor.go`) is the right structural guard — it can never
  enter a cache key, even if a future path re-serialized the run-time `execMD` (e.g. a
  nested exec inheriting it via `execMD = *parent` before its own overwrite). Necessary +
  robust, not merely defensive.
- **The hand-off is in-process, so `json:"-"` cannot drop the data before the emit.**
  `engineClient = query.Engine(ctx)` (`container_exec.go:1300`) and the dispatch is a
  direct `engineClient.Run(…, execMD, …)` passing the live `*ExecutionMetadata` (param
  `execMD *ExecutionMetadata`, `executor.go:98`); the emit reads `state.execMD.ProfArgs`
  off that **same pointer**. No serialization between set and read. ✔
- It is also set only at run time (after every construction-time digest), so even the
  timing argument holds independently. Belt-and-suspenders done right.

This is actually a **stronger** safety than the design's R3 timing-only argument — the
implementer found the real serialization site and closed it structurally.

## 4. Tests real + meaningful

21 new test funcs (+ 2 augmented `otelprof_test`), all green
(`go test ./engine/wcprof/... ./engine/engineutil/` passes). Spot-checked the load-bearing
ones — not vacuous:
- **`TestGateThenReportOrderFullPath`** drives classify → `CheckStructural` (compiles the
  program) → `RunWhatIfs`, and asserts `exec_phase:go build` has a **non-zero** what-if
  saving — the precise stale-memo regression (it would be 0 if classify ran after the
  gate / the memo weren't reset). The exact B2 guard I asked for.
- **`TestCrossSourceOracleSameGroups`** asserts `Agrees(1.0, 0.0)` — exact native==OTel.
- **`TestScrubStringsRedactsSecret`** asserts the real secret value → `***`.
- Coverage maps to the §6 plan: default classes, memo-reset, idempotence/argv-less,
  rule matching/parsing, re-group-without-re-emit (native + cross-source), loader→classes,
  Cloud-JSON-decode survival, coverage boundary (no-argv stays blob), native MetaID
  intern + empty-argv, nil-safe scrub, bounds, end-to-end argv.

## 5. The pre-existing-test claim — CONFIRMED, and it is MINE

`TestEmitExecSplitProducesLoaderShape` **genuinely fails on pristine `c28d55ae7a`** — I
ran it in my worktree (HEAD `c28d55ae7a`) and got `FAIL … no engine span-count
declaration … fails by default`, i.e. **my own completeness-checksum fail-by-default gate**
(272b89ba8d) refusing the test's unmarked in-memory SDK trace. My completeness work
updated the `wcotel` fixtures but **missed this `engine/engineutil` test** (a package I
didn't run). The implementer's fix is correct and does **not** weaken the gate:
`markSpansComplete` stamps `WcprofEngineSpanAttr` on every span + `WcprofSessionSpanCountAttr`
on the first — the same complete-by-construction form as my own `loader_test.markComplete`.
The gate still runs and still checks; `declared == received == len(spans)`; the marker is
legitimately supplied, not bypassed. The honest comment correctly attributes it to a
separate effort. The exec-split assertions remain meaningful (the marker only lets the
orthogonal completeness check pass). Resolved on the impl branch.

## 6. Issues (ranked) — none blocking

1. **[INFO — mine, not the impl's] `c28d55ae7a` shipped a RED test in `engine/engineutil`.**
   My completeness-checksum branch should have run `./engine/engineutil/`; it didn't, so
   `TestEmitExecSplitProducesLoaderShape` was left failing. The impl correctly fixes it.
   For the lead: no impl action; a note that the completeness branch's CI/validation
   missed a cross-package fixture. (NOISE for this review; FYI for Erik.)
2. **[LOW/NOISE] `defaultExecClass(argv[0]=="")` → `"."`** (`path.Base("")`). Pathological
   (a real exec always has a non-empty `argv[0]`); cosmetic only. Optional guard.
3. **[DEFERRED — acknowledged] Live-engine cross-source oracle.** Parity is proven
   *synthetically* (`Agrees(1.0,0.0)` on in-memory native+OTel graphs). The full one-real-
   workload, both-sources-live dev-engine oracle is deferred (per the task). Acceptable;
   recommend it as the final empirical sign-off before/at merge, not a code blocker.

No file:line errors, over-claims, or scope creep found. The two commits are tightly
scoped (Stage 1 emit+classify, Stage 2 the offline rule).

## Summary
**Ready to merge.** Faithful to the converged design; principles hold in the code (replay
untouched, zero inference, exact native↔OTel parity, argv scrubbed/bounded preserving the
key tokens); both critical placements verified (unconditional pre-shim capture; `json:"-"`
necessary-and-safe with the in-process pointer hand-off); tests real and passing; the
pre-existing-test fix is mine to own and does not weaken the completeness gate. Only the
live-engine oracle remains as deferred empirical validation.
