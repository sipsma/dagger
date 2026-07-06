# Chunk 5 review (v1 capstone) — design author, commit `9555281f27`

Productionization: Cloud ingest swap + standing drift gate (design §5/§6.4/§6.6, §7
steps 7–8). Reviewed `git diff 814df0173c..9555281f27` against the canonical design at
file:line.

## Verdict: SIGN OFF. v1 complete.

Chunk 5 fulfills §5/§6.4/§6.6 and the north-star with zero inference and no canonical
conflict. The front-end swap changes only the loader's input; the compile/replay are
reused byte-for-byte; the wire format survives Cloud bit-exact; the drift gate is the
standing self-defense I specified. Two tiny non-blocking observations below.

## (a) Fulfills the design

- **§5 front-end swap — exactly as specified: only the INPUT changes.** The diff adds a
  new `engine/wcprof/wccloud` package + tests + a CLI input option, and touches **nothing**
  in the compile/replay (`grep` confirms no change to `wcotel/loader.go` or
  `wcanalyze/{replay,graph,report}.go`). `wccloud.Load` = `Fetch → wcotel.Compile →
  wcanalyze.Build` — the same Compile/Build the otlpdump path uses (cloud.go:104-118). The
  otlpdump front-end is kept for the dev loop (a new front-end *alongside*, not a
  replacement). This is the §5 input-abstraction realized.
- **§6.6 wire-format survival — proven at both levels, exactly the float64-dodge I
  specified.** (1) Unit, non-gated: `TestCloudWaitNSBitExactThroughConverter` pushes a
  19-digit (>2^53) wait value as a string through `Load`→`Compile` and asserts the
  epoch-rebased wait event equals exact integer arithmetic (cloud_test.go:193-229) — any
  float parse in the converter/loader loses the low digits and fails; the epoch is
  deliberately chosen above 2^53 so a small-ns value couldn't trivially pass. (2)
  End-to-end, env-gated oracle: `TestCloudRoundTrip` compares a real Cloud fetch to the
  local capture of the same run and asserts **bit-exact** `wcprof.wait.*_unix_ns` strings on
  every link in both (roundtrip_cloud_test.go:102-112), explicitly failing with "Cloud
  coerced the decimal string to float64," with guards against a 0-link trivial pass and
  against ns too short to exercise the boundary. 753 real links checked. This is the §3.0
  decimal-string design point validated through real Cloud ingest.
- **§6.4 standing drift gate — the self-defending gate I specified.**
  `TestStandingDriftGate` asserts §6.1 completeness FIRST (a drift number over an
  incomplete graph is meaningless), then `|baseline−actual|/actual ≤ 2%` against a
  committed complex fixture (drift_gate_test.go:42-69, using the pre-existing
  `ActualMakespanNS` at replay.go:649 + `RunWhatIfs` — no new replay logic). It is a
  standing test in the suite, so a future change that regresses the nesting faithfulness
  trips it. Correct ordering, correct mechanism.
- **North-star DoD — met.** A real Cloud trace fetched → the unchanged compile/replay →
  a faithful "why was my CI slow?" report; complete traces give cloud==local + gate 0/0 +
  drift ~0%, user/module work first-class (module load stays on non-reflection receivers,
  profiled). The empirical GREEN run substantiates it.

## (b) Zero-inference + no canonical conflict

- **`SpanFromCloud` synthesizes nothing** (cloud.go:36-63): every field is a direct copy
  from `cloud.SpanData` — IDs lower-cased (consistent normalization applied uniformly to
  span/parent/link ids, not inference), nil `EndTime` → `EndUnixNS=0` (in-flight, same as
  otlpdump), `Status.Code==STATUS_CODE_ERROR` → `StatusError` (a direct status read),
  `Attributes`/`Links` copied verbatim (the wait strings ride untouched). No parent
  fabrication, no timestamp computation, no attribute transformation. The
  `TestSpanFromCloudFieldMap` unit test pins each mapping.
- **No loader/replay logic change** (diffstat + targeted grep). `DroppedLinks/DroppedAttrs`
  stay 0 because Cloud can't self-report them — correct: the link cap is engineered out via
  the engine `LinkCountLimit` (and bounded by the 3000-way cap-stress + the 753-link real
  trace), not papered over in the loader.
- **Wait-edge §3.0 wire format survives intact** — see §6.6 above; the converter copies the
  map[string]any attrs and the decimal strings parse exactly.

## (c) Band / persisted / export-gap

- **±2% drift band — sound.** Gated on §6.1 first; generous enough to absorb the
  end-ordered gating model's discretization, tight enough to catch a regressed nesting.
  (Observation, non-blocking: measured drift is ~0.01–0.04%, so the band has ~50× headroom;
  a tighter band, e.g. ±0.5%, would be a stronger standing guard. ±2% is a defensible
  conservative choice to avoid false-fails across workloads — keep or tighten at will.)
- **Persisted fixture — faithful, no special loader logic.**
  `TestChunk5PersistedResultDecodeFaithful` closes the Chunk-2-reserved persisted seam: an
  imported result's lazy persisted-decode is emitted as a `lazy` op and a forcing consumer
  blocks on it via the same `parentId`/wait-link mechanism, gate clean, drift tracks. It
  also confirms the skip-fix `ResultCall.ProfileSkip` JSON round-trip — an imported
  *reflection* result is correctly not profiled, a non-reflection (Directory) one is. No
  import-specific loader code; consistent with §5.
- **Export-gap characterization — correct.** The residual ~8% large-trace loss is the
  **CLI→Cloud exporter BSP** (write side), not the loader: the skip fix removed the engine
  amplifier and dropped it from ~80% to ~8% (cloud.go:81-87 documents this), the Cloud read
  is full-fidelity below 100k, and the structural gate **refuses** any incomplete Cloud
  trace (OrphanedParents/UnresolvedWaitTargets) rather than ranking it. Out of this
  loader-side chunk; the deferred client-BSP backstop is the right follow-up. Gate-safe.

## (d) Deviations — acceptable

- **Byte-identical only for complete traces** — correct and principled: an incomplete Cloud
  trace is refused by the gate, never silently ranked. "cloud==local" holds where the
  capture is complete.
- **16384 link cap** — bounded by the 3000-way cap-stress (`TestCloudCapStressThousandsOfWaitLinks`)
  and the 753-link real trace; engineered out via `LinkCountLimit`, with the gate catching
  any drop. Acceptable.

## (e) Anything missing — nothing that blocks

- Tiny: the float64-dodge unit test passes the string directly in the Go map rather than
  through a `json.Marshal→Unmarshal` round-trip. The "JSON string → Go string" property is
  stdlib-guaranteed and the real-Cloud oracle covers the end-to-end, so this is adequate; a
  marshal/unmarshal step would make the unit test fully self-contained. Optional.
- Standing canonical note (carried from the native-ungate convergence review, NOT a Chunk 5
  issue): the **§6.2 cross-source oracle** — with native un-gated, native keeps reflection
  ops as children while the OTel source folds that time into kept ancestors, so a
  reflection-triggering ancestor shows `OTel self > native self`; the §6.2 oracle should
  rank-compare (robust) and/or fold-normalize native. The §6.4 drift gate here is
  within-OTel (simulated vs actual recorded), so it's unaffected. This is the one remaining
  §6.2 doc reconcile when the canonical design is updated.

## Bottom line

**Signed off — v1 capstone complete.** §5 swap is faithful (input-only; compile/replay
untouched), `SpanFromCloud` is a verified zero-inference field map, §6.6 proves the §3.0
decimal-string wire format survives Cloud bit-exact at both unit and real-Cloud levels,
§6.4 is the standing self-defending drift gate (§6.1-first, ±2%), the persisted seam is
closed with no special loader logic, and the export-gap is correctly scoped out and made
gate-safe. The north-star — a real Cloud trace compiled by the unchanged analyzer into a
faithful, user-work-first bottleneck report — is met. The wcprof × OTel second source is
complete.
