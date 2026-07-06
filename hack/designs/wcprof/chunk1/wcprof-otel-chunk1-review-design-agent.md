# wcprof × OTel — Chunk 1 review (by the design author)

**Scope:** single-chunk review of Chunk 1 only (the foundation). Reviewed commit
`e689e9b007` on `wcprof-otel-implementer-7a7ee34b` (base `upstream/main`
`b442cd2533`) against the contract of record `hack/designs/wcprof-otel-design.md`
+ `hack/designs/wcprof-otel-impl-plan.md`. Not the holistic cross-chunk review
(this is the first chunk).

## Verdict

**Sound enough to build Chunk 2 on.** The loader's §5 mapping is correct, the
§6.1 gate is faithful, the wire vocabulary and the `LinkCountLimit` plumbing match
the design (including the post-review refinements), and the Chunk 1 DoD is
genuinely met on a real captured trace. I verified locally:

- `go build` of every touched package: **OK**; `go vet ./engine/wcprof/wcotel/...`: **OK**.
- `go test ./engine/wcprof/wcotel/...`: **ok, 0.007s** — and that 0.007s on the
  120-span fixture is the first datapoint against accidental superlinear cost.
- The fixture (`testdata/baseline-simple-noservice.jsonl`) is a genuine otlpdump
  capture of `container | from alpine | with-exec echo hello | stdout` (no
  service), with real `startNs ≈ 1.78e18` (> 2^53) and in-flight (`endNs:0`) spans
  — so the baseline test exercises the float64 trap and the open-ops path on real
  data.

No correctness bug exists within Chunk 1's actual scope (compiling un-augmented
baselines + the structural gate). The findings below are (1) two forward-looking
gaps that only bite once Chunk 2 emits real wait links — worth deciding now, and
(2) minor robustness/test-coverage notes. None blocks Chunk 2.

## Divergences (implementer self-classified) — I agree with all four

- **(a) `LinkPurposeWait = "wait"` value in `engine/telemetryattrs`, key stays
  `telemetry.LinkPurposeAttr`** (`engine/telemetryattrs/attrs.go:90-93`, used at
  `loader.go:299`). `github.com/dagger/otel-go` is a versioned external module that
  can't be edited here, and the design itself located the *value* "alongside"
  cause/error_origin only illustratively. Emit and load both reference
  `telemetryattrs.LinkPurposeWait`, so they can't diverge. **Zero behavioral
  change — agree.**
- **(b) wcprof×OTel vocabulary in `engine/telemetryattrs`** rather than a new
  wcprof package (`attrs.go:39-93`). This is arguably the *most* natural home: it
  already holds the engine's `dagger.io/*` telemetry-attribute constants
  (`DagBlockedAttr`, `ProgressItemAttr`), it's a zero-dep leaf already imported by
  the emit side (`dagql/cache.go:31`), and it keeps `wcanalyze`/`wcotel` out of the
  engine binary. (Minor: the stated rationale "keep the loader's `wcanalyze`
  dependency out" is slightly imprecise — putting constants in `engine/wcprof`
  wouldn't have pulled in `wcanalyze` either — but the *choice* is good.) **Agree.**
- **(c) filled-in mapping details** the design left unspecified — all sound:
  - un-augmented non-cached call → `"ok"` not `"executed"` (`loader.go:385-396`):
    this is *good* judgment, not just a default. `report.go` dup-exec detection
    keys on `Outcome=="executed" || Kind=="call_exec"`; emitting `"executed"` on a
    baseline would manufacture false dup-exec the trace can't substantiate. **Agree.**
  - `dag.output` → a `uint64` interner for `ResultID` (`loader.go:259-262,495-512`):
    correct as the design's result-link *seam*. Note for later (not now): these
    synthetic ids are loader-local and won't equal native's real shared-result ids
    — fine, because nothing (replay/oracle) compares `ResultID`. **Agree.**
  - in-flight (`endNs==0`) → native `OpenOps` (`loader.go:264-277`): correct;
    `Build` ends them at dump time, mirroring native. **Agree.**
  - malformed wait timing → zero-duration no-op + `MalformedWaitTimings` counter
    (`loader.go:319-325`): conservative and replay-safe (an `actWaitNoop`). **Agree.**
- **Fixture from `dagger v0.21.7` not engine-dev@b442cd2533:** acceptable for
  Chunk 1 — the loader consumes only the *stable* un-augmented telemetry shape
  (`dag.digest`/`dag.call`/`ui.passthrough`/`cached`/span names), and the baseline
  is "deliberately wrong" anyway. **Agree, with one cheap follow-up** (LOW, below):
  re-capture the baseline from engine-dev at the real base when Chunk 2 brings that
  build online for the oracle, so the baseline and the augmented traces share an
  engine.

I also hunted for **un-flagged** divergences and found none that are violations.
The loader ignoring `PendingAttr`, and unclassified spans (session root, `connect`)
getting `Kind==""` (`loader.go:377`), are both consistent with the design (pending
is the Chunk 3 lazy path; session phases are the §3.5 seam) — not divergences.

## REAL issues (forward-looking; none block Chunk 2, but decide before/with it)

### 1. MEDIUM — unresolved wait *target* is silent (no provenance, no gate signal)

When a `purpose=wait` link's target span id isn't in the trace, the loader sets
`targetID = 0` (`loader.go:308-311`: `opIDBySpan[...]` misses → 0). For a non-lock
reason that becomes a **targetless** wait, which `wcanalyze.Build` (graph.go:255-261)
leaves `Target=nil`, and replay then treats as an `actWaitFixed` *fixed delay*
(replay.go:170-173). That's a reasonable fallback (it preserves the blocked time
without inventing a wrong edge) — but it is **silent**: unlike dropped links, there
is no counter and no gate check. On the Cloud path a truncated/absent target span
would thus *quietly* convert a real `call_exec`/`lazy` dependency into a fixed
delay, drifting the cross-source oracle with no signal pointing at why.

This is invisible in Chunk 1 (the baseline emits no wait links) but lands the
moment Chunk 2 does. The design's §6.1 gate vocabulary covers dropped *links* but
not unresolved *targets* — a genuine gap the implementation surfaces.

**Recommendation (Chunk 2):** count wait links with `reason != lock` whose target
span id doesn't resolve, expose it on `Compiled` + `GateReport`, and treat a
non-zero count as a loud gate signal (same family as `WaitBearingDroppedLinks`).
Fold a one-line note into design §6.1 when you do.

### 2. MEDIUM — Cloud preservation of the `wcprof.*` namespace is assumed, validated only at Chunk 5

Now that the concrete keys exist, the single biggest *unvalidated* assumption is
visible: the wire format uses a **non-`dagger.io/` namespace** — `wcprof.op.kind`,
`wcprof.work_type`, `wcprof.parent`, `wcprof.wait.*` (`attrs.go:53-88`) and the
link-purpose *value* `"wait"`. The design's "strings round-trip exactly through
Cloud's `map[string]any`" argument (§3.0) is about *encoding* (string vs float64);
it presumes the attribute is **retained at all**. Whether the Dagger Cloud trace
API preserves arbitrary `wcprof.*` span/link attributes (and a custom
`link.purpose` value) is only checked by the §6.6 round-trip — **at Chunk 5**, after
all the emit work is built.

If Cloud allowlists or strips unknown-namespace attributes, the entire wire format
fails and Chunks 2–4's emit is built on sand. This isn't a Chunk 1 defect (Chunk 1
is the local otlpdump path, which is fine), but it's the highest-leverage thing to
de-risk early.

**Recommendation:** a cheap spike *before* investing in Chunk 2–4 emit — emit one
hand-crafted span carrying a `wcprof.*` attr + a `purpose=wait` link through real
Cloud ingest and confirm it round-trips. Cheap now; expensive to discover at
Chunk 5. (No design change implied unless it fails.)

## Noise / minor (LOW — fix opportunistically, not blockers)

- **`traceEnd` ignores in-flight spans** (`loader.go:215-221`): it's `max(ended
  end)`, used as the open-ops dump time. A span that *starts after* the last ended
  span and is still in-flight gets clamped to zero duration (its start ≥ dump
  time). The common in-flight span — the CLI root, which starts first — is handled
  correctly (it gets the full trace span). Late in-flight spans are rare on
  completed traces (what the oracle runs on). Tiny robustness win: `traceEnd =
  max(maxEndedEnd, maxStart)` so dump time never precedes a span's start.
- **A `startNs==0` span would drag the epoch to 0** (`loader.go:213`) and inflate
  all rebased intervals — but it would then trip the gate's `interval > tracespan`
  loudly rather than corrupt silently, and a real SDK span always has a start. Very
  low risk; noting for completeness.
- **`classifyKind` hardcodes `s.Name == "Container.withExec"`** (`loader.go:371`):
  a magic string, but it's a *baseline-only* heuristic, suppressed the instant
  `wcprof.op.kind`/a `call_exec` child exists, and the design used exactly this
  example. Acceptable; just flagging it's brittle to a span-name format change (it
  only affects the deliberately-wrong baseline if so).
- **Empty-`SpanID` spans are silently skipped** in dedup (`loader.go:182-184`) with
  no counter. Harmless (can't be mapped) but invisible.
- **Test-coverage gaps** (the code handles these; tests don't cover them): the
  `MalformedWaitTimings` path, the unresolved-target case (issue 1), the
  self-parent guard (`loader.go:249-251`), and empty/malformed input (the "no spans
  to compile" error, `loader.go:194-196`). All cheap to add; worth doing alongside
  Chunk 2 when waits make them live.
- **`WaitBearingDroppedLinks` over-counts** by attributing *all* dropped links on a
  wait-bearing span to "wait" (`loader.go:337-339`) even if the dropped one was a
  cause link. This is the safe direction (fail-loud, which the design wants) and is
  near-impossible to hit at `LinkCountLimit=16384`. Fine as a backstop.

## On-goal? (criterion 5 — did building it reveal a design problem?)

**Still firmly on the north star,** and the design held up well against first
contact with code. Three observations:

1. The clean split of `ParseOTLPDumpJSONL` (front-end) from `Compile`
   (front-end-agnostic, producing neutral `Span` values, `loader.go:34-64,176`) is
   exactly the seam the Cloud swap (Chunk 5) needs — the implementer realized the
   design's "same `Compile`, swap the front-end" intent cleanly. Good sign for the
   productionization chunk.
2. The two MEDIUM items above are the design's, not the implementation's: the gate
   never enumerated "unresolved wait target," and Cloud attribute *retention*
   (vs encoding) was deferred to the latest chunk. Both are now visible because the
   concrete wire format exists. Neither invalidates the design; #1 wants a one-line
   §6.1 addition at Chunk 2, #2 wants a re-ordering of *when* we validate (earlier),
   not a redesign.
3. **User-work-first-class is on track:** `wcprof.work_type` is plumbed end-to-end
   (`attrs.go:57`, `loader.go:255-258`, defaulting to `engine`), ready for the
   Chunk 4 exec split to set `user` on `processRun`. Nothing here forecloses it.

The §6.1 gate doing real work is well-demonstrated: `gate_test.go` drives the
cycle, dropped-wait-link, and fallback-anchor paths with *synthetic* wait links
(`gate_test.go:55-120`), proving the gate fires before any real emit exists — the
right way to have the strongest-feasible gate live from day one (oracle-early
principle, applied as far as Chunk 1 allows).

## Bottom line

Chunk 1 meets its DoD, is faithful to §5/§6.1/§3.0 (including the post-review
refinements: abs-unix-ns strings, `NewSpanLimits`-then-`WithRawSpanLimits`,
otlpdump dropped-counts, op-kind precedence, lower-hex `wcprof.parent`), builds,
vets, and tests green with no performance concern. **Proceed to Chunk 2.** Carry
issue #1 (unresolved-target provenance) into Chunk 2's wait-edge work, and treat
issue #2 (Cloud namespace survival) as a cheap de-risking spike to run before the
emit chunks rather than discovering it at Chunk 5.
