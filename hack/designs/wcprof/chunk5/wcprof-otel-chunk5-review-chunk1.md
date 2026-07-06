# wcprof × OTel — Chunk 5 (productionization) review (Chunk 1 / loader+gate owner)

**Reviewer:** Chunk 1 owner (offline loader + §6.1 gate). Reviewed `9555281f27` atop
`814df0173c` (unpushed). Focus: the Cloud front-end stays zero-inference and my gate is
what makes the export gap safe. No code modified.

## SIGN-OFF ✅ — v1 capstone

The Cloud front-end is a pure mechanical field map that synthesizes nothing, my
`Compile`/`Build`/gate are byte-for-byte unchanged (only the input source swapped), and
the export gap is gate-safe by construction **and locked by a test that fatals if the
gate fails to refuse an incomplete trace**. Bit-exactness, the drift gate, and the
persisted fixture are all sound. No blocker.

## (a) Zero-inference + compile/replay UNCHANGED — the load-bearing check

**The analysis is byte-for-byte unchanged.** `git diff 814df0173c..9555281f27` touches
**no** core analysis file — `wcotel/loader.go` (Compile), `wcotel/gate.go`, and all of
`wcanalyze/` (Build/replay/graph/report) are untouched. The change is a new `wccloud`
package + a new CLI input path + new tests + one testdata fixture. So Chunk 5 swaps only
the loader's *input source*; the rational-function-of-faithful-data core is identical to
the otlpdump path it already validated.

**`SpanFromCloud` (cloud.go:36-63) is a pure field map — verified field by field:**
- Every field is copied verbatim: `TraceID`/`SpanID`/`ParentID`/link-`SpanID`
  lower-cased (normalization, not inference — see below), `Name`, `StartUnixNS`,
  `EndUnixNS`, `Attrs`, `Links`, `StatusError = (Status.Code == STATUS_CODE_ERROR)`.
- **Absences are passed through faithfully, never synthesized:** `ParentID nil → ""`
  (the loader's "no parent"/root) and `EndTime nil → 0` (the loader's in-flight/open
  op). It does **not** fabricate a parent, an end time, an edge, or a fallback to fill
  an absence. This is precisely the property that makes the gate work: a dropped span's
  dangling parent/target reference is preserved verbatim so the **unchanged** gate
  catches it, rather than being papered over.
- No causal `wcprof.parent`/edge is computed — the override rides in `Attrs` verbatim
  and the unchanged `causalParentSpanID` (`wcprof.parent ?? parentId`) reads it.

**Id lower-casing is consistent with the loader, so no false orphans.**
`normalizeSpanID` (loader.go) does **only** empty/all-zeros → "" handling — it is
case-preserving. `SpanFromCloud` lower-cases *all* ids uniformly (span + parent + link),
so within the Cloud set the `opIDBySpan[rawSpanID]` key and the
`normalizeSpanID(parentId)` lookup are consistently lower-cased and match — even if
Cloud reports mixed-case hex for the same logical id (the stated reason for the
lower-casing). The §6.6 round-trip's *graph == local* assertion empirically proves the
two paths produce the identical structure.

## (b) The export gap is gate-safe — and the test ENFORCES it

The mechanism: a CLI→Cloud BSP-dropped span leaves its parent/target references
dangling in the *surviving* spans; the verbatim field map preserves those references;
my unchanged loader resolves them to a missing op → `OrphanedParents` /
`UnresolvedWaitTargets > 0` → `gate.Err()` refuses the trace. The Cloud path inherits
this safety from the unchanged gate; it does not need to re-prove it.

**`TestCloudRoundTrip` (roundtrip_cloud_test.go) locks both directions, not just the
happy one:**
- Complete (cloud == local): asserts `cc.SpanCount == lc.SpanCount && gate.WaitEdges ==
  lgate.WaitEdges` (graph matches) **and** `gate.Err() == nil` (gate 0/0).
- Incomplete (short): the message is "the structural gate correctly refuses it" and the
  test **`t.Fatalf`s if the gate did *not* fail** (`gate.Err() == nil`) — i.e. it
  enforces that an incomplete Cloud trace MUST trip the gate, then logs the
  `UnresolvedWaitTargets`/`OrphanedParents` it tripped on. This is exactly the "loud, not
  wrong-ranked" guarantee, asserted against real Cloud.

The live observation (92%-complete run refused; complete in-band run == local, gate 0/0)
is consistent with this. The env-gating of this test (it needs creds) is acceptable
because the *core* property — a dropped span → dangling ref → gate refusal — is
unit-locked at the loader/gate level (the `OrphanedParents` signal + the skip fix's
invalid-target-detector test), and this test adds the live end-to-end confirmation.

## (c) Bit-exactness, drift gate, persisted fixture — all sound

- **Bit-exactness, tested both hermetically and live.** `TestCloudWaitNSBitExactThroughConverter`
  (cloud_test.go, CI-running) sends a 19-digit (>2^53) wait-ns as a Go string and
  asserts it survives the converter bit-exact (a float64 path would truncate); the test
  epoch is deliberately above 2^53. `TestCloudRoundTrip` then re-asserts bit-exactness on
  every wait link present in both cloud and local through *real* Cloud JSON, `t.Fatalf`ing
  on any divergence ("Cloud coerced the decimal string to float64") and requiring the
  value be genuinely >2^53. The float64 trap is closed and proven. Plus dedup
  (keep-max-end) and a 3000-way fan-in cap-stress at the converter.
- **§6.4 drift gate is correctly composed.** `TestStandingDriftGate` runs the §6.1
  structural gate **first** and `t.Fatalf`s if it isn't clean ("a drift number over an
  impossible/incomplete graph is [meaningless]"), *then* asserts the simulated baseline
  makespan tracks the actual recorded makespan within the band. That is the right
  pairing — structure-faithful (§6.1) **and** timing-faithful (makespan-tracking) —
  which answers the "baseline-match is necessary but not sufficient" point I raised in
  the publishResult review: it is sufficient here *because* §6.1 gates it. Runs on a
  committed fixture, so it's a standing CI regression gate; the observed -0.012%/-0.042%
  leaves huge margin under ±2%.
- **Persisted fixture faithful.** `TestChunk5PersistedResultDecodeFaithful` drives the
  imported-result lazy-decode shape (decode emitted as a `lazy` op; a consumer forces it
  and emits a `lazy` wait) through the unchanged loader + gate, asserting the decode wait
  resolves to the decode op (no dangle), gate clean, no drift. It confirms the loader
  needs **no** special persisted-import logic (same `parentId`/wait-link mechanism) and
  that `ResultCall.ProfileSkip` surviving import (its JSON tag) means an imported
  *reflection* result is correctly not profiled. This also closes the
  adopted/imported-not-live-captured caveat I left open in the skip-fix code review.

## (d) The named gap + deviations — characterization is right

The residual ~8% large-trace loss is the **CLI→Cloud `LiveSpanProcessor`/BSP export
drop** — emit/export-pipeline infrastructure on the *producer* side, genuinely outside
this loader-side front-end chunk, and the correct follow-up (the deferred BSP
backpressure backstop, already scoped out with Erik's sign-off). It is **not** papered
over: the gate refuses the incomplete trace, so the failure is loud and the analysis is
never wrong-ranked. Deviation — the Cloud front-end is a *new source alongside* otlpdump
(the dev loop is kept), not a replacement — is correct and matches the design.

## Residual notes (minor — not blockers)

- **Cloud cannot self-report per-span/per-link dropped counts (`DroppedLinks`/
  `DroppedAttrs` stay 0),** so the otlpdump dropped-link signal is blind on the Cloud
  path. This is safe because the two link-loss mechanisms are covered elsewhere: per-span
  link *truncation* at emit is engineered out by the engine's `LinkCountLimit` (so 0 is
  faithful, validated by the 753-link round-trip + 3000-way cap-stress), and export
  *span* drops surface as `OrphanedParents`/`UnresolvedWaitTargets`. The one theoretical
  residual — the Cloud API re-capping links *below* the engine limit and not reporting it
  — would manifest as a wait-edge-count mismatch in the round-trip's *graph == local*
  check (or a dangling target), so it is caught where it can be observed. Worth a comment,
  not a fix.
- The standing Cloud round-trip test is env-gated; the structural safety it exercises is
  unit-locked at the gate level, so unit CI still protects the load-bearing property.

## Verdict

**Sign off — v1 capstone.** The Cloud front-end adds zero causal inference (pure
verbatim field map, absences preserved), my `Compile`/`Build`/gate are byte-for-byte
unchanged, the export gap is gate-safe and the round-trip test *enforces* that an
incomplete trace is refused, bit-exactness is closed (hermetic + live, float64 trap), the
drift gate is correctly gated on §6.1 before asserting makespan-tracking, and the
persisted fixture proves the imported-lazy path is faithful through the unchanged loader.
The named export gap is correctly scoped out and made safe by the gate. Ship v1.
