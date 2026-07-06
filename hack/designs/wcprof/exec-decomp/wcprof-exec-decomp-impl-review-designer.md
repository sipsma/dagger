# Implementation review — wcprof exec decomposition (DESIGN-AUTHOR / fidelity review)

Reviewer: the design author (charge: **fidelity** — does the code match what the
design specified, and does any divergence matter?). Plus the standard correctness,
principles, placement, tests, and pre-existing-claim criteria.

Subject: branch `wcprof-exec-decomp-impl-dea4c5e3`, two commits on base `c28d55ae7a`:
- `463f05b0ba` — Stage 1 (per-command argv emit + classify, 21 files)
- `a691bec084` — Stage 2 (`--exec-group` offline rule, 6 files)

Every claim below is verified against the code (file:line) in the implementer's
worktree, and against pristine `c28d55ae7a` (my own worktree) where stated. Tests
were run.

## Verdict

**Correct, faithful to the design, and ready to merge** (modulo the deferred
live-engine Cloud oracle, which the design itself defers). **Zero MUST-change items.**
This is an unusually high-fidelity implementation: every load-bearing design decision —
including the subtle ones (the `json:"-"` two-horn argument, `path.Base` vs
`filepath.Base`, compute-once cross-source parity, the program-memo reset, bounds that
never touch `argv[0..1]`, the pre-existing-test fix) — is implemented as specified and
covered by a meaningful test. The two critical placements I sharpened in the design
folds are both correct. The pre-existing-test claim is true and the fix is sound. The
only findings are NOISE-level edge observations (below); none block merge.

---

## Fidelity audit (my special charge) — design element → code

| Design element | Spec (§) | Code | Faithful? |
|---|---|---|---|
| Capture clean command in core, **unconditional, before `if emu != nil`** | §4.1a | `core/container_exec.go:2039+` — `if execMD != nil { execMD.ProfArgs = slices.Clone(metaSpec.Args) }` placed after the `getEmulator` err-check and **before** `if emu != nil` (`:2052`) | ✓ exact |
| `ExecutionMetadata.ProfArgs []string` with `json:"-"` | §4.1a, B4 | `engine/engineutil/executor.go:85` — `ProfArgs []string `json:"-"`` + load-bearing comment | ✓ exact |
| Scrub (shared registered-secret trie) + nil-safe + omit-on-error | §4.2 | `secret_scrub.go` `newSecretCensor`/`ScrubStrings` (shared trie), `wcprof_argv.go` `execProfArgv` (nil-guards, returns nil on scrub err) | ✓ exact |
| Bound 64 tok / 256 B / 4 KiB, **never truncate `argv[0..1]`**, sentinel | §4.2, §8.6 | `wcprof_argv.go` `boundProfArgv` (`i >= 2` byte-cap; `keep := min(len,2)`; sentinel `"…(+%d more)"`) | ✓ exact |
| Compute argv **once**, feed both sources (byte-identical) | §4.1b | `executor_spec.go:1414` — `profArgv := execProfArgv(state)` once, → native `OpOpts.Argv` and `emitOTelExecSplit(...,profArgv)` | ✓ exact |
| Scalar **JSON-array** encoding, native interned `MetaID` | §4.1c | `record.go` `internArgv` = `json.Marshal` → `Intern`; `Event.MetaID`/`openOp.metaID`; `dump.go` `DumpEvent.MetaID "m,omitempty"` | ✓ exact |
| Scalar JSON-array on OTel attr `wcprof.exec.argv` | §4.1c | `otelprof.go` `attribute.String(WcprofExecArgvAttr, string(b))`, `b,_=json.Marshal(argv)`, user phase only; `attrs.go:90` const | ✓ exact |
| Loader routes OTel attr through the **same `MetaID` seam** → shared `Build` | §4.1c | `loader.go:380,400,416` — `MetaID: str.intern(attrStr(...WcprofExecArgvAttr))` on both op + open-op | ✓ exact |
| `Op.Argv`; `Build` decodes (empty/malformed ⇒ nil, never inferred) | §4.1c/d | `graph.go` `Op.Argv`, `decodeArgv` (`""`→nil, unmarshal-err→nil), set on op + open-op | ✓ exact |
| `ClassifyExecs` relabels `op.Class`; **replay/report untouched** | §4.4, §4.7 | `classify.go`; `git diff c28d..HEAD` shows **replay.go/report.go NOT touched**; `Op.Key()` unchanged | ✓ exact |
| `invalidateProgram` memo reset; **classify before the gate** | §4.4, B2 | `graph.go invalidateProgram` (`progOnce=sync.Once{}; prog=nil`); all 3 CLIs call `ClassifyExecs` before `CheckStructural`/`WriteReport`; oracle classifies **both** graphs | ✓ exact |
| Default class `path.Base(argv[0])` + first non-flag `argv[1]` | §4.3 | `classify.go defaultExecClass` — uses **`path.Base`** (correct for container paths), `!HasPrefix(argv[1],"-")` | ✓ exact (+ better) |
| `--exec-group` boundary-aware prefix + `contains:`; offline; partition; split-on-first-`=` | §4.6 | `classify.go` `matches` (`==` or `HasPrefix(m+" ")`; `Contains`→`strings.Contains`), `ParseExecGroupRule` (`Cut(spec,"=")`, `CutPrefix("contains:")`) | ✓ exact |
| Never-started exec emits no processRun → no argv (blob) | §4.1d | `otelprof.go` `started.IsZero()`→containerStart only, `nil` argv; native `else` branch unchanged | ✓ exact |
| Additive `omitempty`, **no `DumpSchemaVersion` bump** | §8.5 | `dump.go` adds `MetaID ... omitempty`; `DumpSchemaVersion` unchanged | ✓ exact |

**Fidelity gaps: none that matter.** The only un-implemented design items are the ones
the design explicitly marked **optional** or **deferred**, so their absence is faithful,
not a divergence:
- *Optional* `report.go` drill-down section (§4.5/§4.7) — not added; the class table
  already renders per-command rows via `Key.String()`, so the headline works without it.
- *Deferred* coverage extension to Dockerfile-`RUN` / service-start execs (§9) — not
  added; correctly tested instead as the "stays-blob" boundary (`TestCoverageBoundaryNoArgvStaysBlob`).
- *Deferred* live-engine Cloud round-trip oracle (§6 test 6, live) — the in-repo
  `map[string]any` proxy is implemented (`TestArgvAttrSurvivesCloudJSONDecode`); the
  live run remains the agreed deferral.

No silent simplification, no dropped requirement, no unspecified behavior.

---

## Criterion 3 — the two critical placements (verified in code)

**(a) `ProfArgs` capture is UNCONDITIONAL, before `if emu != nil` — ✓.**
`core/container_exec.go:2039-2052`: the capture (`if execMD != nil { execMD.ProfArgs =
slices.Clone(metaSpec.Args) }`) sits **after** the `getEmulator` error check and
**before** the `if emu != nil { metaSpec.Args = append([]{QEMU}, ...) }` block. So it
runs for **all** withExec execs and captures the command **before** the QEMU shim (and
before the executor's later `/.init`). `metaSpec.Args` is the fully-resolved command
(`metaSpec.Args = args`, `:333`, from `container.command(opts)` = entrypoint + args).
The implementer's comment states the unconditional/outside-the-branch requirement
verbatim. This is exactly the fold-in I sharpened. **NOT** placed inside the emulated
branch (the backwards bug) — verified.

**(b) `json:"-"` is NECESSARY *and* doesn't drop the data — both horns verified.**
- *Necessity (cache-key leak):* `dagql.SerializedString[T].MarshalJSON()` is
  `json.Marshal(s.Self)` (`dagql/types.go:670`), and `ToLiteral()`/`String()` feed that
  JSON into the dagql call literal (`:666,679`) — i.e. into the **cache key**. So
  `ExecutionMetadata` *is* JSON-serialized into exec cache keys; a tagged-in `ProfArgs`
  could perturb them. `json:"-"` closes that structurally. **The implementer's claim
  (and the lead's `dagql/types.go:672` pointer) is correct.**
- *No pre-emit drop (in-process):* withExec calls `engineClient.Run(execCtx, "",
  rootMount, execMounts, procInfo, nil, causeCtx, execMD, ...)` **directly**
  (`core/container_exec.go:2119`), passing `execMD` as a live Go pointer; the executor
  reads `state.execMD.ProfArgs` from that same pointer. No serialization sits between
  capture and emit, so `json:"-"` cannot drop it. (The serialized buildkit-gateway
  `executor.Run` at `gateway/container/container.go:373` is a *different* path that
  withExec does not take.)

My design's must-verify item is resolved exactly as written: both horns checked,
`json:"-"` chosen, in-process hand-off confirmed. The 4-line struct comment in
`executor.go:78-84` documents this correctly.

---

## Criterion 5 — the pre-existing-test claim (verified carefully, DECISIVE)

Claim: `TestEmitExecSplitProducesLoaderShape` was **already failing on pristine
`c28d55ae7a`** (the completeness-checksum fail-by-default gate refusing an unmarked
in-memory-SDK trace), fixed by stamping the real completeness marker.

**Verified true on three independent checks:**
1. **It fails on pristine HEAD.** Ran the unmodified test in my pristine `c28d55ae7a`
   worktree → `--- FAIL`: *"incomplete-or-unverifiable trace: no engine span-count
   declaration … fails by default (design §6.1)"* (`otelprof_test.go:201`, the
   `CheckStructural(...).Err()` assertion).
2. **It predates the gate.** The test's last touch is `4d6987fdc2` (Chunk 4); the
   completeness checksum is a *later* effort (`501653ddcf`/`4074ad7867`/`272b89ba8d`),
   and those commits did **not** update this test — so it was left broken on HEAD.
3. **The fix is correct and does NOT weaken the gate.** `markSpansComplete`
   (`otelprof_test.go:84`) stamps `WcprofEngineSpanAttr=true` on every span and
   `WcprofSessionSpanCountAttr=len(spans)` on the first — exactly what the engine's
   per-client span-count processor does (and what `loader_test`'s `markComplete` does
   for JSONL fixtures). The trace then has `declared == received == len`, marker
   present, `MissingSpans == 0` → the gate passes **legitimately**. The gate's invariant
   (received ≥ declared, marker present) is still fully enforced; the test now satisfies
   it honestly rather than bypassing it. Transparently disclosed in the commit message
   ("…which the fail-by-default leaf-drop gate had been refusing").

This is the right call (don't ship a known-broken test; fix the adjacent breakage at the
seam, per the project's "fix adjacent unsoundness" principle) — not scope creep.

---

## Criterion 4 — tests are real and meaningful (22 new; all pass)

`go test ./engine/wcprof/... ./engine/engineutil/...` → all green. The suite faithfully
exercises the §6 plan and avoids the usual vacuity traps:
- **Cross-source oracle** (`TestCrossSourceOracleSameGroups`, `TestReGroupCrossSource`):
  native vs OTel of the same workload, `Oracle(...).Agrees(1.0, 0.0)` — **exact**
  jaccard=1.0/drift=0.0 — **with** a `len(cmp.Shared) >= 2` guard so it cannot pass on
  an empty set. This is the strongest faithfulness check and it is done right.
- **B2 ordering** (`TestGateThenReportOrderFullPath`, `TestClassifyExecsMemoReset`):
  compile the program on the blob classes *first* (as the gate does), then classify,
  then assert `go build` has a **non-zero** what-if saving — directly catching the
  memoization bug the `invalidateProgram` reset prevents.
- **Cloud survival** (`TestArgvAttrSurvivesCloudJSONDecode`): bit-exact round-trip of
  the scalar string through a `map[string]any` decode with **adversarial** argv
  (`-ldflags=-X main.v=1.2`, a unicode `…`, embedded `"`quotes) — proving the §4.1c
  JSON-string choice over an OTLP array.
- **B1+B4 shim guard** (`TestClassifyExecsDecomposesBlob`): asserts no class is
  `exec_phase:.init` or `exec_phase:dagger_qemu_emulator`, plus `go build` ranks #1 and
  `Op.Argv` round-trips the dump exactly.
- **Scrub/bounds/nil-safe** (`wcprof_argv_test.go`): `--password=hunter2`→`***`;
  `argv[0..1]` retained even over-budget and never byte-truncated; sentinel present;
  nil `execMD`/empty `ProfArgs`→nil.
- **Default projection** (`TestDefaultExecClass`): `go build`, `git clone`,
  `/usr/local/go/bin/go test`→`go test`, `sh -c …`→`sh`, single-token, flag-`argv[1]`.
- **Coverage boundary** (`TestCoverageBoundaryNoArgvStaysBlob`): argv-less processRun
  stays the blob.

None vacuous, none over-asserting.

---

## Criterion 2 — principles hold in the code

- **Replay/graph causal logic UNCHANGED.** `git diff c28d55ae7a HEAD` touches neither
  `replay.go` nor `report.go`. The only `graph.go` additions are the `Op.Argv` field,
  `decodeArgv`, `Build` population, and `invalidateProgram` (an explicit *cache reset*,
  not an algorithm change). `Op.Key()` is byte-for-byte unchanged. ✓
- **Zero inference.** Default class is a pure projection of explicit `argv`; the
  loader maps the attr string through untouched (`loader.go` comment: "Zero inference");
  `decodeArgv` never infers; no `sh -c` parsing (the `classify.go` comment calls it out
  as forbidden). ✓
- **Native↔OTel parity.** One `profArgv` slice, `json.Marshal`ed identically on both
  sources (same bytes) — proven empirically by the oracle test (jaccard=1.0, drift=0). ✓
- **Bounded + scrubbed, `argv[0..1]` preserved.** Verified in `boundProfArgv` and its
  test. ✓

Build is clean across all changed packages (`cmd/*`, `engine/*`, `core/...`).

---

## REAL vs NOISE

**REAL issues: none.** No bug, no incorrect file:line, no over-claim, no scope creep.

**NOISE (verified, non-blocking — listed so they're closed):**
- *UTF-8 byte truncation of `argv[2+]`* (`boundProfArgv`, `tok[:256]`) can split a
  multibyte rune. Harmless: `json.Marshal` coerces invalid UTF-8 to `U+FFFD` **identically
  on both sources** (parity holds), and it never touches `argv[0..1]` (the group key). Not
  a bug; per-spec byte cap. Optional nicety: truncate on a rune boundary.
- *`defaultExecClass` with an empty `argv[0]`* → `path.Base("")` = `"."`. Requires a
  degenerate empty program token (effectively impossible — you can't exec ""); harmless,
  no panic. `ClassifyExecs` already guards `len(argv) > 0`.
- *`go vet` reports 3 `WithTimeoutCause` cancel-discard warnings* in
  `engine/engineutil/executor.go:532/616/683`. **Pre-existing** — they also appear on
  pristine `c28d55ae7a` (3), and the impl's `executor.go` diff is only the struct field
  at `@@ -71 @@`. Not this change's concern.
- *`contains:` rule could substring-match the sentinel text.* Only when argv was
  truncated, and only for a pathological user pattern; benign.

---

## Summary

**Verdict: correct + faithful + ready to merge** (modulo the deferred live-engine Cloud
oracle the design already defers). No MUST-change items.

- **Fidelity:** every load-bearing design element is implemented exactly, including the
  subtle ones; the only unimplemented pieces are those the design marked *optional*
  (report drill-down) or *deferred* (Dockerfile/service coverage, live Cloud oracle).
- **Critical placements:** (a) the `ProfArgs` capture is unconditional and before
  `if emu != nil` (`container_exec.go:2039+`); (b) `json:"-"` is genuinely necessary —
  `ExecutionMetadata` is JSON-serialized into cache keys via
  `SerializedString.MarshalJSON` (`dagql/types.go:670`) — **and** doesn't drop the data,
  because withExec hands `execMD` to `engineClient.Run` as an in-process pointer
  (`container_exec.go:2119`). My must-verify item is resolved exactly as specified.
- **Pre-existing-test claim:** verified true (the unmodified test fails on pristine
  `c28d55ae7a` via the fail-by-default completeness gate; last touched in Chunk 4,
  pre-dating the gate); the `markSpansComplete` fix supplies the real marker and does
  **not** weaken the gate.
- **Tests:** 22 new, all passing, all meaningful — the cross-source oracle (with an
  empty-set guard), the B2 memo-reset path, and the adversarial Cloud-decode are the
  standouts.
- **Principles:** `replay.go`/`report.go` untouched across both stages; zero inference;
  byte-identical cross-source parity; `argv[0..1]` never truncated.
- **Issues:** zero REAL; four NOISE-level edges (UTF-8 byte-truncation→U+FFFD, empty
  `argv[0]`→".", 3 pre-existing vet warnings, sentinel-contains) — none block merge.
