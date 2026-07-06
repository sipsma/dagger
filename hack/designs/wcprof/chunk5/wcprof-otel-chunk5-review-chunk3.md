# Chunk 5 capstone review — `9555281f27` (Cloud ingest + drift gate) — Chunk 3 implementer (lazy / wcprof.parent owner)

**Reviewed:** `git diff 814df0173c..9555281f27` + the new files, in the coder worktree.
file:line are that worktree. Focus: Cloud front-end zero-inference + the persisted/lazy
fixture (my domain). Review only — did not modify the branch.

## SIGN-OFF — v1 capstone. No blocker.

I tried to break the Cloud front-end on the precision/fidelity axes and it holds: it is a
faithful, pure field map with the loader/replay **untouched** (verified: the diff touches no
`wcotel/loader.go` or `wcanalyze` core). My carried items are fully closed across the feature.
Four LOW/non-blocking residuals noted at the end.

### (a) Cloud front-end — ZERO causal inference, faithful map

`SpanFromCloud` (cloud.go:36-63) synthesizes **no causal structure** — it maps
`cloud.SpanData` → `wcotel.Span` verbatim, mirroring `ParseOTLPDumpJSONL` field-for-field. I
cross-checked every field against the otlpdump parser and the `Span` IR:
- **Timestamps — the trap I most expected, and it's avoided.** `s.Timestamp`/`s.EndTime` are
  Go `time.Time`/`*time.Time` (cloud/trace.go:87-89), i.e. RFC3339Nano via JSON — **not** a
  float64 unix-nano, so there is no >2^53 precision loss at the span level. The
  precision-sensitive **wait** timestamps ride as decimal STRINGS in `Attributes`
  (`map[string]any`, passed through untouched), and the live §6.6 test asserts them
  **bit-exact through real Cloud** (see (c)). ✓
- **Ids:** `strings.ToLower` on span/parent/link ids — matches the otlpdump lowercase
  convention and keeps the Cloud set internally consistent for parent/link resolution
  (the loader's `normalizeSpanID` is belt-and-suspenders). Graph node numbering (sort by
  start,spanID) is unaffected since both sources are lowercase. ✓
- **Attrs:** `s.Attributes`/link `Attributes` passed verbatim as `map[string]any`; every
  loader-read attr is a string (op-kind, digest, wait reason, wait ns decimal-strings), so the
  `attrStr` type-assertion path is satisfied. ✓
- **Status:** `s.Status.Code == "STATUS_CODE_ERROR"` matches `isErrorStatus` for Cloud's proto
  enum name (the const comment asserts that is what Cloud returns). Minor asymmetry: the otel
  parser also accepts the legacy `"Error"`; `SpanFromCloud` does not — harmless for Cloud's
  enum, and even a missed error status is an outcome *label*, not timing (ranking unaffected).
- **`Partial` is not a mis-classification trap:** `SpanFromCloud` keys "ended" on
  `EndTime != nil` and ignores `Partial` — and Cloud's own SpanData→OTLP code does the same
  (`if s.EndTime != nil` at cloud/trace.go:322), so `EndTime`-nil is the consistent in-flight
  signal across both. ✓
- **`DroppedLinks`/`DroppedAttrs = 0`** (Cloud can't self-report) is faithful *by
  construction*, not a hidden inference: the link cap is engineered out (`LinkCountLimit`), so
  there are no emit-time drops to report (otlpdump would show 0 too), and span-level CLI→Cloud
  BSP loss surfaces through the **structural** gate (orphans / unresolved targets), not this
  counter. The only theoretically-uncaught case (a link dropped at emit that still reaches
  Cloud) cannot arise given `LinkCountLimit`. Documented, safe.

`Fetch`/`Load` (cloud.go:88-118) just accumulate `StreamSpans` batches into the **unchanged**
`wcotel.Compile` → `wcanalyze.Build`; dedup/multi-trace-reject are delegated to `Compile`. The
CLI (`-trace`) is pure wiring (auth + `cloud.NewClient` + `wccloud.Load`), no inference. So the
entire new surface adds nothing causal. ✓

### (b) Persisted/imported lazy fixture — FAITHFUL (my domain)

`TestChunk5PersistedResultDecodeFaithful` confirms the persisted-decode reserve-seam is
**zero-inference**: an imported result's first-use decode is just a `lazy` op + a consumer wait
edge, resolved by the **same** parentId/wait-link mechanism as any deferred eval — no special
loader logic. The fixture asserts the right invariants: `WaitEdges==1`,
`UnresolvedWaitTargets==0`, `OrphanedParents==0`; the consumer's single wait targets the lazy
decode op; and the decode work **nests under the lazy op** (subtree intact, not re-rooted —
consistent with my Chunk 3 `wcprof.parent` re-homing). `ProfileSkip`-survives-import is real and
already verified structurally (the JSON-tag round-trip test from the skip fix); this fixture
correctly uses a non-reflection `Directory` (profiled) and references that.

Honest scope (correctly labeled in the test): it is a **loader-shape** fixture (hand-built
spans), not an engine-emit test — emit faithfulness for persisted/imported results is deferred
to the §6.4 real-capture drift gate. That split is adequate because the emit path *is* the
established `evaluateOne` lazy path (which I verified in the round-2 code review handles
imported/adopted results via `sharedResult.profileSkip` keyed on the stored producer recipe),
so the assumed shape (imported decode ⇒ `lazy` op) is the real one. ✓

### (c) §6.6 bit-exactness genuine; §6.4 drift gate sound; export-gap right

- **§6.6 (`roundtrip_cloud_test.go`) is a LIVE test** (env-gated on real `dagger login` creds +
  a same-run local capture), not synthetic — the strongest possible form. It (a) asserts every
  `wcprof.wait.*_unix_ns` present in both Cloud and local is **bit-exact**, failing with "Cloud
  coerced the decimal string to float64" — directly targeting the precision trap — and **cannot
  vacuously pass** (fails if no shared wait links); (b) asserts Cloud ids are a faithful subset
  of local; (c) on a complete trace, asserts the compiled graph matches local (op + wait
  counts) and the gate is 0/0. Genuine. ✓
- **§6.4 (`drift_gate_test.go`) is a real standing gate:** it requires the §6.1 structural gate
  clean *first* (drift over an incomplete graph is meaningless), then asserts
  `|baseline−actual|/actual ≤ 2.0%` with a hard `t.Fatalf` (observed −0.012%/−0.042%). This is
  exactly the baseline==recorded faithfulness probe I recommended in round 2 — a faithful
  emit replays to the recorded makespan; an unfaithful nesting regression pushes it out of
  band. Sound. ✓
- **Export-gap characterization is right:** the residual ~8% large-trace loss is the CLI→Cloud
  BSP (a deferred backstop), genuinely out of this loader-side chunk, and the gate makes an
  incomplete Cloud trace **safe** — §6.6's subset check + the "incomplete ⇒ gate refuses
  (logged), not silently analyzed" path confirm loss is caught, never papered over. Not
  papered-over; correctly named.

### (d) Deviations + anything missed

Deviations acceptable. Residuals, all **non-blocking**:
1. **Span-interval fidelity on the LIVE path** rests on the Cloud backend storing full-ns span
   timestamps. §6.6 asserts wait-ns bit-exact + op/wait *counts*, but not span-interval
   bit-exactness; a µs-resolution backend would introduce ≤µs interval noise — negligible for
   ms-scale bottleneck ranking (won't reorder), and it's a backend property outside
   `SpanFromCloud` (which faithfully maps whatever `time.Time` it's given). Worth confirming the
   backend keeps nanos, but not a blocker.
2. **Status check** narrower than `isErrorStatus` (no `"Error"` fallback) — consider calling
   `isErrorStatus` for symmetry. Label-only impact. Minor.
3. **`DroppedLinks=0`** blinds the Cloud path to the dropped-link gate signal — safe by
   construction (above), documented.
4. **≥100k-span traces** are out of the stated scope (the incremental-listen protocol isn't
   wired in `Fetch`); such a trace would come back incomplete and the gate refuses it (safe),
   so it degrades to a loud failure, not a wrong answer.

### Carried items — fully closed across the whole feature

- **service.start §3.4:** closed + retired in the producer-completion round (`814df0173c`); not
  reopened here. ✓
- **publishResult-parentless:** moot (the skip fix removed the BSP-drop amplifier that produced
  the parentless roots; and a parentless internal op is a root, not an orphan, so gate-invisible
  regardless); not reopened by the Chunk 5 persisted fixture. ✓

## Verdict: SIGN OFF — v1 capstone lands. Nothing further owed from me.
