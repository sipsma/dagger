# Implementation review — wcprof exec-decomposition — chunk4/exec-split implementer

Reviewed commits `463f05b0ba` (Stage 1) + `a691bec084` (Stage 2) against the
converged design. Verified against actual code (file:line below), ran the tests,
and confirmed the pre-existing-failure claim by running at pristine `c28d55ae7a` in
a throwaway worktree. The exec-split emit is my territory; I verified the argv
threading on both sources end to end.

## Verdict: **CORRECT + FAITHFUL — ready to merge (modulo the deferred live-engine oracle), with ONE should-fix.**

The implementation realizes the converged design faithfully, the principles hold in
the code, and both critical placements are right. The only actionable item is a
**pre-existing** sibling red test the implementer already has the exact fix for
(below). No feature-correctness bug found.

---

## Criterion-by-criterion

### 1. Faithfulness — every design element implemented, no silent divergence
- **`execMD.ProfArgs` capture** in core, **unconditional, before `if emu != nil`**
  (`container_exec.go`, with the comment "before the `if emu != nil` block so the
  common non-emulated withExec is captured too"); `slices.Clone(metaSpec.Args)`. ✓
- **Argv threaded through BOTH emit sites reading one value (my territory):**
  `execProfArgv(state)` runs **once**, gated on `wcprof.Enabled(ctx) ||
  dagql.OTelProfActive(ctx)` (`executor_spec.go:1418`), and the same `profArgv`
  slice feeds the native `RecordOp(…, OpOpts{…, Argv: profArgv})` (`:1431`, processRun
  phase only) **and** `emitOTelExecSplit(…, profArgv)` (`:1444`) →
  `emitOTelExecPhase(…, "exec.processRun", …, argv)` (user phase only). `RecordOp`
  interns it via `internArgv`→`MetaID` (`record.go`); OTel stamps
  `attribute.String(WcprofExecArgvAttr, string(json.Marshal(argv)))`. Both
  `json.Marshal` the same slice → **byte-identical wire forms**. Never-started exec
  emits no processRun and no argv on **both** sources. ✓
- **Scalar JSON-array encoding both sources, shared `MetaID`→`Build` seam:** loader
  reads the scalar string via `attrStr` (Cloud-safe) and interns into the same
  `MetaID` dump field (`loader.go:400,416`); `Build` decodes via `decodeArgv`
  (`graph.go`). One path, both sources. ✓
- **`Op.Argv` additive**, populated in `Build` from `decodeArgv(str(ev.MetaID))`. ✓
- **`ClassifyExecs` before the gate + `invalidateProgram`:** OTel CLI runs
  `ClassifyExecs(g, rules)` at `cmd/wcprof-otel-analyze/main.go:88` **before**
  `CheckStructural` at `:89`; native CLI before `WriteReport`; oracle classifies
  **both** graphs with identical rules before comparison. `ClassifyExecs` also calls
  `g.invalidateProgram()` (`classify.go`). ✓
- **Default projection** `defaultExecClass`: `path.Base(argv[0])` (correctly *not*
  `filepath.Base` — container paths are forward-slash) + first non-flag `argv[1]`. ✓
- **`--exec-group` boundary-aware prefix** + opt-in `contains:`: `matches` is
  `joined == Match || HasPrefix(joined, Match+" ")` (so `go build` ∌ `go buildx`),
  `contains:` → `strings.Contains`; first-`=` split, validated. ✓

### 2. Principles hold in the CODE
- **Replay/graph causal logic UNCHANGED:** `replay.go` and `report.go` are **not in
  either commit's file list**. The only `graph.go` changes are the additive `Op.Argv`
  field, `decodeArgv`, two `Build` lines, and `invalidateProgram` (a memo reset,
  explicitly "NOT a change to the replay algorithm"). ✓
- **Zero inference:** `decodeArgv` (empty→nil, malformed→nil, never recovers),
  `ClassifyExecs`/`defaultExecClass` derive only from explicit emitted argv, never a
  span name or `sh -c` parse. ✓
- **Parity:** both sources `json.Marshal` the identical scrubbed+bounded slice →
  identical `Op.Argv` → identical `ClassKey`s. Verified by
  `TestCrossSourceOracleSameGroups`, `TestReGroupCrossSource`,
  `TestArgvAttrSurvivesCloudJSONDecode` (all pass). ✓
- **Bounded + scrubbed, never `argv[0..1]`:** `boundProfArgv` byte-truncates only
  `i >= 2`, keeps `argv[0..1]` always (`keep := min(len,2)`), drops excess trailing
  tokens behind an ellipsis-prefixed sentinel; `execProfArgv` scrubs via the shared
  registered-secret set (`profSecretFilePaths` stashed by `setupSecretScrubbing`),
  returns nil on scrub error. ✓
- **No schema bump:** `DumpSchemaVersion` stays `1` (dump.go:12 hard-rejects
  mismatches); `MetaID` is `omitempty` on `DumpEvent`/`DumpOpenOp` → old dumps read
  (absent → 0 → blob). ✓

### 3. The two critical placements — both confirmed
- **(a) Unconditional capture:** the `ProfArgs` capture sits **before** the
  `if emu != nil {` block in `container_exec.go` (verified in the diff), so
  non-emulated `withExec` — the common case — decomposes, not only emulated execs. My
  R3 note #1 was heeded. ✓
- **(b) `json:"-"` necessary AND non-dropping:** `ExecMD *ExecutionMetadata
  json:"execMD,omitempty"` (`container_exec.go:108`) is JSON-serialized into cache
  keys via `SerializedString.MarshalJSON` → `json.Marshal(s.Self)` (`dagql/types.go`),
  so `json:"-"` on `ProfArgs` is **load-bearing** (excludes it from every key —
  *stronger* than my R3 omitempty suggestion, which only held if serialization
  preceded population). And the core→executor hand-off is **in-process**: execMD comes
  from `execMeta` (`:1287`, `*ExecutionMetadata`), `ProfArgs` is set on that pointer
  (`:2042`), and the **same pointer** is passed to `engineClient.Run(execMD, …)`
  (`:2119/2127`) — a Go pointer, definitively not serialized — and read at the emit
  via `state.execMD.ProfArgs`. So `json:"-"` cannot drop the data before emit. Both
  halves of the implementer's claim are correct. ✓

### 4. Tests — real, meaningful, passing
22 new tests mapping directly to the design's §6 plan: default classes
(`TestClassifyExecsDecomposesBlob`, `TestDefaultExecClass`), **gate→report ordering**
(`TestGateThenReportOrderFullPath` — non-vacuous: asserts `go build` has a *non-zero
what-if saving* after the gate compiled the program, the exact B2 failure mode),
OTel loader (`TestLoaderArgvToClasses`), **cross-source oracle native==OTel**
(`TestCrossSourceOracleSameGroups`), **re-group-without-re-emit**
(`TestReGroupWithoutReEmit`/`…CrossSource`), scrub/bounds/nil-safe
(`TestScrubStringsRedactsSecret`, `TestBoundProfArgvPreservesKeyTokens`,
`TestScrubStringsNilInputsPassthrough`), coverage boundary
(`TestCoverageBoundaryNoArgvStaysBlob`), memo reset (`TestClassifyExecsMemoReset`),
Cloud survival (`TestArgvAttrSurvivesCloudJSONDecode`), native MetaID round-trip
(`TestRecordOpArgvInternsMetaID`/`…EmptyArgvNoMetaID`). All pass (`go test
./engine/wcprof/... ./engine/engineutil/` green); the ones I ran `-v` exercise real
`Compile`/`Build`/`CheckStructural`/`Oracle` paths. Not vacuous. ✓

### 5. Pre-existing-test claim — CONFIRMED definitively
Ran at pristine `c28d55ae7a` in a throwaway worktree: **both**
`TestEmitExecSplitProducesLoaderShape` (engineutil) **and**
`TestEmitServiceStartProducesLoaderShape` (core) FAIL with the **same** completeness-
gate error ("no engine span-count declaration … fails by default"). So the failure
is **genuinely pre-existing**, not introduced by this change. The fix
(`markSpansComplete` stamping `WcprofSessionSpanCountAttr = len(spans)` — the real
count the engine's processor stamps) is **correct and does not weaken the gate**: it
declares the true count, so a dropped span (received < declared) still fails. ✓

---

## REAL issues (ranked)

### [MEDIUM] The sibling pre-existing red test is left unfixed
`TestEmitServiceStartProducesLoaderShape` (`core/otelprof_services_test.go`) fails on
pristine `c28d55ae7a` with the identical completeness-gate root cause as the
engineutil twin the implementer **did** fix in Stage 1. It is **pre-existing** (not a
regression), but it leaves `go test ./core/` red on the branch, and the fix is the
*same one-line* `markSpansComplete`/`WcprofSessionSpanCountAttr` stamp already written
for the engineutil twin. Fixing one twin and not the byte-for-byte identical other is
inconsistent (cf. "fix adjacent unsoundness"). **Should-fix before merge:** apply the
same stamp to `core/otelprof_services_test.go`, or explicitly defer it with a
tracking note so reviewers/CI aren't surprised by a red `core`. Not a feature-
correctness blocker; a clean-merge / known-red-test item.

## NOISE (verified — explicitly not issues)
- **`json:"-"` dropping argv:** does not happen — the hand-off is an in-process Go
  pointer (verified above). The implementer's choice is more robust than my R3
  omitempty.
- **Two `json.Marshal` calls (native intern + OTel attr):** deterministic on the same
  `[]string` → byte-identical; cross-source oracle tests confirm.
- **4 KiB bound is "soft" for `argv[0..1]`** (always retained even if alone they
  exceed it): intended — the group key must survive — and documented; OTel attr
  limits are far larger.
- **Coverage boundary** (Dockerfile/`execMD==nil` execs stay the blob): faithful to
  the design, consistent across sources, `TestCoverageBoundaryNoArgvStaysBlob` covers
  it.
- **No scope creep:** diff is contained to emit + IR + analyzer + CLIs + tests; no
  replay/report/causal change.

---

## Bottom line
Faithful, correct, principle-preserving, and well-tested; the two critical
placements and the native↔OTel parity are verified in the code, and the
pre-existing-failure claim is definitively confirmed. **Ready to merge modulo the
deferred live-engine oracle, after applying (or explicitly deferring) the one-line
completeness-marker stamp to the `core` sibling test.**

### Ranked issues
1. **[MEDIUM]** `core/otelprof_services_test.go` (`TestEmitServiceStartProducesLoaderShape`)
   left red — pre-existing, same trivial fix as the engineutil twin; stamp it or
   defer explicitly.
(No other REAL issues. All faithfulness/principle/placement/test/pre-existing-claim
criteria verified clean.)
