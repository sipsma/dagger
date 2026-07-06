# Round-3 review (final) — wcprof exec-decomposition — chunk4/exec-split implementer

Scoped to the one delta: the B4 fix (QEMU second-shim). Did not re-litigate the
architecture / B1–B3 / encoding (CONVERGED in R2). Verified B4 + the minor folds
against the landed code.

## Verdict: **CONVERGED.** No blocker. Two soft implementer-precision notes below.

The B4 fix is correct: it resolves the QEMU-shim mislabel, subsumes B1, is
cache-safe, reaches both emit sites, and characterizes its coverage boundary
honestly. The five Round-2 folds are applied correctly — including a code-grounded
**reversal of my own R2 suggestion** that I confirm is right.

---

## B4 — verified against code

**The blocker is real and the capture point is correct.** The QEMU prepend is at
`core/container_exec.go:2043`
(`metaSpec.Args = append([]string{engineutil.DaggerQemuEmulatorMountPoint}, …)`,
inside `if emu != nil {` at :2042), upstream of the executor's `Run` (:2106). So a
`Run`-entry capture (B1) would headline emulated execs as the QEMU shim — and
emulated multi-arch execs are exactly the slow, high-value candidates. Capturing in
core before both shims is the right fix. I confirmed `metaSpec` is built at :332 and
**`metaSpec.Args =` appears only at :2043** in the file, so between resolution and
:2042 it holds the fully-resolved `[entrypoint, user-args]` (from
`container.command(opts)`); capturing there is faithful and subsumes `/.init` (added
far downstream in the executor). `slices.Clone` is correct.

**Cache-key safety — CONFIRMED (stronger than the doc's timing argument alone).**
`execMD` is whole-struct serialized for a digest in exactly two places, both
construction-time SDK paths: `core/sdk/go_sdk.go:424` and
`core/sdk/module_typedefs.go:107` (`dagql.NewDigestedSerializedString(&execMD, …)`).
There is **no** `execMD` re-serialization in `container_exec.go`'s run path after
:2042, and none in the executor — `executor_spec.go:1219` only *reads*
`execMD.CallDigest` (a field) for a span attribute. And `execMD.CallDigest` itself is
the **recipe** digest (`curCall.RecipeDigest`, set at :248), not an `execMD`
serialization. So `ProfArgs`, set at run-time (:2042) after all construction-time
digesting, cannot perturb any cache key. The withExec result cache is keyed by the
LLB exec op (args/mounts/env), not `execMD`, so it's likewise unaffected. ✓

**Both emit sites read one value, one type.** `state.execMD` is in scope at both
emit sites (`executor_spec.go:846` already uses `state.execMD.SecretEnvNames`); set
at :2042 < passed to `Run` at :2106, so the executor receives `execMD` with
`ProfArgs` populated. `ExecutionMetadata` is the shared `engineutil` type in core and
executor, so native `RecordOp` (:1417) and `emitOTelExecSplit` (:1430) read the same
identical slice — parity by construction. ✓

**Coverage boundary — correctly characterized, no silent gap.** `executor.Run` is a
chokepoint also reached by the Dockerfile/buildkit frontend, but the capture lives
only on the core `Container.withExec` path. Frontend `RUN` execs and `execMD==nil`
internal execs carry no `ProfArgs` → empty `Op.Argv` → they stay the aggregated
`exec.processRun` blob. Because `ClassifyExecs` only relabels `len(Argv)>0`, these are
**not mislabeled** (they never carry a shim argv either) — they're the pre-feature
status quo, consistent across both sources. Honest and additive-extensible. ✓

## Minor folds — all correctly applied

- **`json.Marshal` form (§4.1c):** fixed to the compiling form
  `b, err := json.Marshal(argv); … string(b)`, attr/field omitted on error. ✓
- **Empty-`MetaID` guard (§4.1c):** `json.Unmarshal` only when `str(ev.MetaID) != ""`,
  the same `str()`-returns-`""` guard `Ident` uses (`graph.go:189-190`); empty ⇒
  `Op.Argv == nil`. ✓
- **No schema bump (§8.5) — CORRECT, and it overrides my R2 note.** I had suggested a
  `DumpSchemaVersion` bump as optional-nice. The designer verified
  `dump.go:176-177` **hard-rejects** a version mismatch (`unsupported dump schema
  version %d (want %d)`), so a bump would break old/new dump interop; additive
  `omitempty` (version stays 1) is the right call. I confirm the reversal — my R2
  suggestion was wrong, this is the code-grounded fix. ✓
- **Nil-safe scrub + bound-on-raw-slice notes:** carried into §4.2 as in R2. ✓

## Two soft precision notes (implementer-level; neither blocks)

1. **Place the `ProfArgs` capture UNCONDITIONALLY — before `if emu != nil {`
   (:2042), not on the line immediately before the prepend (:2043, inside that
   block).** The `~:2042` line anchor is the correct spot (the `if emu` line), and the
   intent is unambiguous (it subsumes B1, which covered *all* execs), but the prose
   "immediately before the QEMU prepend" could be misread as *inside* the emu block —
   which would capture `ProfArgs` only for emulated execs and silently leave the
   common non-emulated case as the blob (the exact thing the feature fixes). One word
   of explicitness ("before the `if emu != nil` block") removes the trap.
2. **Tag the new field `ProfArgs []string \`json:"profArgs,omitempty"\`.** The two SDK
   serializations digest with an explicit constant (`goSDKExecMDDigest` /
   `moduleTypesExecMDDigest`), so even a non-omitempty field wouldn't move the *key* —
   but `omitempty` keeps the serialized `execMD` value byte-identical when `ProfArgs`
   is empty (always, at construction time), so nothing downstream of those
   serializations sees a spurious `"profArgs":null`. Pure hygiene; matches the other
   additive fields.

Neither note touches the design's soundness; both are one-line implementation
details. I found nothing new that the revision broke (citations spot-checked:
:2043 QEMU prepend, :248 CallDigest, :332 metaSpec, :2106 Run, dump.go:176 reject —
all accurate).

## One-line verdict

**CONVERGED** — B4 correctly fixes the QEMU second-shim mislabel, is cache-safe and
parity-clean, and introduced nothing new; ship the two staged PRs, applying the two
soft precision notes during implementation.
