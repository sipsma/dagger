# wcprof OSS↔dagger.io migration audit (PR-1 / PR-2 cut)

Owner: wcprof migration-audit owner (profiler-rescue lead). Status: **AUDIT COMPLETE**.
Base: branch `wcprof-exec-decomp-impl-dea4c5e3` @ `db1caae0db`, true base = merge-base
`bc39a70141` (25 wcprof commits). Verified against the real import graph + `go list -deps`,
not the working split on faith.

Erik's ruling: the wcprof **analyzer** goes closed-source to **dagger.io** (PR-2); the
engine-side **recorder/emit stays OSS** (PR-1 = emit + TUI + REMOVAL of the analyzer subtree).

---

## 0. Verdict (the cut is clean and provable)

- **Production cut is CLEAN, proven transitively.** `go list -deps` over every STAYS
  production package (recorder, `telemetryattrs`, `dagql`, `core`, `engineutil`,
  `engine/server`, `engine/telemetry`) contains **zero** analyzer packages. The emit side
  compiles with the analyzer removed.
- The analyzer depends on OSS **only** via `engine/wcprof` (recorder) + `engine/telemetryattrs`
  (vocab) → both are module-importable from dagger.io (house-standard direction).
- **Two cross-boundary edges to resolve** (both enumerated below, neither is a showstopper):
  1. **7 white-box emit tests** import the analyzer (as an end-to-end oracle). They are
     `package dagql`/`core`/`engineutil` → cannot move → **stay OSS, refactor** to assert
     emit-shape without the analyzer import.
  2. **`wccloud` + `cmd/wcprof-otel-analyze` import `internal/cloud` + `internal/cloud/auth`.**
     Go `internal/` visibility forbids dagger.io from importing these → the re-home must
     **swap the trace-fetch for dagger.io-native access** (dagger.io is the Cloud backend).

---

## 1. INVENTORY — STAYS-OSS vs MOVES-dagger.io (file-precise)

### MOVES → dagger.io (the analyzer; PR-2). Manifest for the re-home implementer:
```
engine/wcprof/wcanalyze/          classify.go graph.go replay.go report.go
                                  classify_test.go replay_test.go replay_cycle_test.go
engine/wcprof/wcotel/             loader.go gate.go oracle.go
                                  baseline_test.go chunk2_test.go chunk3_test.go chunk4_test.go
                                  chunk5_persisted_test.go completeness_test.go drift_gate_test.go
                                  exec_decomp_test.go gate_test.go loader_test.go oracle_test.go
engine/wcprof/wcotel/testdata/    baseline-simple-noservice.jsonl
                                  drift-module-functions.otlpdump.jsonl
engine/wcprof/wccloud/            cloud.go  cloud_test.go roundtrip_cloud_test.go   [internal/cloud — swap]
cmd/wcprof-analyze/main.go        (v1, ALREADY ON UPSTREAM MAIN — remove from OSS in PR-1)
cmd/wcprof-oracle/main.go
cmd/wcprof-otel-analyze/main.go   [internal/cloud — swap]
```
`wcanalyze` + `cmd/wcprof-analyze` are the **v1 analyzer already merged on upstream main**
(PR #13393); the rest are this branch's additions. PR-1 REMOVES the former and EXCLUDES the
latter.

### STAYS → OSS (recorder + vocab + emit; PR-1):
```
engine/wcprof/            record.go wcprof.go dump.go README.md         (recorder + dump format)
                          record_argv_test.go wcprof_test.go            (recorder tests — no analyzer import)
engine/telemetryattrs/    attrs.go                                      (emit+analyze shared vocab; analyzer imports via module)
hack/otlpdump/            main.go                                       (generic OTLP debug tool — RECOMMEND stays; see §5 flag)
emit-side changes (by area; full list in hack/logs/branch-only-files.txt):
  dagql/                  cache.go + otelprof hooks                     (call_exec / lazy / waits emit)
  core/                   telemetry.go container_exec.go services.go    (profileSkip, ProfArgs capture, service.start)
  engine/engineutil/      executor*.go otelprof.go secret_scrub.go wcprof_argv.go  (exec split, scrub, argv)
  engine/server/          session.go wcprofcount.go                     (completeness checksum + teardown flush)
  engine/telemetry/       livespan.go                                  (BSP-queue backstop)
  internal/cmd/dagger/    engine.go                                    (CLI telemetry config)
emit-side tests (STAY, but REFACTOR to drop the analyzer import — see §2):
  dagql/                  otelprof_hooks_test.go otelprof_lazy_test.go otelprof_lazy_exec_test.go
                          otelprof_lazy_retry_test.go cache_profileskip_emit_test.go
  core/                   otelprof_services_test.go
  engine/engineutil/      otelprof_test.go
```

---

## 2. IMPORT-CUT PROOF + the two cross-boundary resolutions

**Production (0 edges, proven):** `go list -deps <every STAYS prod pkg>` ∌ `wcanalyze|wcotel|wccloud`.
**Analyzer → OSS (module-importable):** `engine/wcprof`, `engine/telemetryattrs`, `engine/slog`,
`engine/distconsts`, `internal/buildkit/*` (only transitively, through the recorder — resolved
inside the OSS module, not a dagger.io import).

**Edge 1 — 7 white-box emit tests (STAY + refactor).** All are in-package (`package dagql`/
`core`/`engineutil`), so they access unexported emit internals and cannot relocate. Resolution:
in OSS, keep each test's **emit-shape assertions on the raw recorded SDK spans/attributes**
(the emit is what the OSS side must protect) and **remove the `wcotel`/`wcanalyze` "compiles
through the loader" tail**. That end-to-end coverage is retained on the dagger.io side by the
moving `wcotel` loader/gate/oracle tests (which drive the same shapes via fixtures). **Zero
coverage dropped — it is split by side.**

**Edge 2 — `internal/cloud` in `wccloud` + `wcprof-otel-analyze` (re-home swap).** Direct imports
of `github.com/dagger/dagger/internal/cloud` + `/auth` (Cloud auth + `StreamSpans` trace fetch).
Go forbids importing another module's `internal/`. The `SpanFromCloud` field-converter and the
`Load`/`Fetch` structure are portable; only the underlying **trace source** must be re-pointed to
dagger.io-native access (it owns the trace store). This is the intended closed-source seam.

---

## 3. TEST CENSUS (zero-drop) — every test file's disposition

| Test | Disposition |
|---|---|
| `wcanalyze/{classify,replay,replay_cycle}_test.go` | MOVE (inside package) |
| `wcotel/{baseline,chunk2,chunk3,chunk4,chunk5_persisted,completeness,drift_gate,exec_decomp,gate,loader,oracle}_test.go` | MOVE (inside package) |
| `wccloud/{cloud,roundtrip_cloud}_test.go` | MOVE (inside package; `internal/cloud` test import swaps with prod) |
| `engine/wcprof/{record_argv,wcprof}_test.go` | STAY (recorder; no analyzer import) |
| `dagql/otelprof_hooks_test.go`, `otelprof_lazy_test.go`, `otelprof_lazy_exec_test.go`, `otelprof_lazy_retry_test.go`, `cache_profileskip_emit_test.go` | STAY + REFACTOR (drop analyzer import) |
| `core/otelprof_services_test.go` | STAY + REFACTOR |
| `engine/engineutil/otelprof_test.go` | STAY + REFACTOR |

**Fixtures that travel (with `wcotel`):**
- `baseline-simple-noservice.jsonl` — loader baseline. Regen: `hack/otlpdump` (OSS) capture of a
  minimal `dagger query` container|from|withExec|stdout run against an augmented engine.
- `drift-module-functions.otlpdump.jsonl` — the **standing drift gate** fixture. Regen: `hack/otlpdump`
  capture of a re-entrant module-functions workload; the drift gate compares the analyzer's ranking
  against this baseline.
- **Cross-source oracle** (`oracle_test.go`) regen: one workload captured **both** ways — native
  `--profile` dump + OTel (`hack/otlpdump` or a Cloud trace) — then `cmd/wcprof-oracle` compares.
  `hack/otlpdump` STAYS in OSS, so the recipe is: capture with OSS otlpdump → store the fixture in
  dagger.io. Document this cross-repo recipe in the PR-2 test README.

---

## 4. PR-1 ASSEMBLY PLAN (upstream dagger/dagger)

Start from **current** `upstream/main` (`c7d72b6ce8` — NB: 23 commits past the branch's `bc39a70141`
base; re-rebase first, see §5). Produce a reviewable series (multiple commits, detailed messages
preserving the review-hardened reasoning; consolidate only where two commits are one logical change —
never information-destroying squashes):

1. **Emit-side commits** — the recorder/dump extensions, `telemetryattrs` vocab, the dagql/core/
   engineutil/server/telemetry emit changes, the CLI telemetry config. The 25-commit lineage already
   groups cleanly by chunk; regroup to logical units (e.g. "exec engine/user split", "per-command
   argv emit", "completeness checksum + teardown flush", "BSP-queue backstop"). Keep the exec-decomp
   `ProfArgs` capture, the completeness force-flush, and the skip-predicate as their own well-messaged
   commits.
2. **Emit-test refactor commit(s)** — refactor the 7 white-box tests to drop the analyzer import
   (Edit 1). Must leave OSS `go test ./dagql ./core ./engine/engineutil` green.
3. **Analyzer-removal commit(s)** — `git rm -r engine/wcprof/wcanalyze cmd/wcprof-analyze` (the v1
   already on main) and ensure the branch never adds `wcotel`/`wccloud`/`wcprof-oracle`/
   `wcprof-otel-analyze`. Net OSS state: recorder+emit present, analyzer absent.
4. **TUI changes** — (per Erik's PR-1 scope; not in this branch's 25 commits — confirm source/owner).
5. `.changes/` — keep emit-side changelog entries; drop/redirect analyzer-only entries.

**DoD:** OSS builds (`go build ./...`), `go test ./core ./engine/... ./dagql` green with the analyzer
gone, no dangling analyzer import, authorship Erik + signed-off preserved.

## 5. Open flags for Erik / cache-chief
- **[confirm] `hack/otlpdump` STAYS** — generic OTLP debug tool, zero analyzer dependency; useful for
  emit debugging + fixture regen. Recommend OSS. (Working split agreed; flagging per instruction.)
- **[process] Re-rebase** the branch onto current `upstream/main` `c7d72b6ce8` (23 commits ahead of
  the `bc39a70141` base) before PR-1 assembly, so the PR is mergeable.
- **[scope] TUI changes** for PR-1 are not in this branch's 25 commits — need the source branch/owner.
- **[PR-2 handoff] The `internal/cloud` swap** (§2 edge 2) is the one non-mechanical re-home step —
  called out precisely for the dagger.io implementer.
