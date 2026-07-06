# wcprof × OTel — completeness-checksum (`MissingSpans`) review (Chunk 1 / loader+gate owner)

**Reviewer:** Chunk 1 owner (offline loader + §6.1 gate). `MissingSpans` is a new
signal in my gate family. Reviewed `501653ddcf` atop `9555281f27` (unpushed). Merge
gate — scrutinized hard. No code modified.

## SIGN-OFF ✅ — with one residual named (acceptable for v1)

The signal is correctly built and in my gate family, the count/reconciliation is sound
(distinct, monotonic-accumulate + loader-keeps-MAX, reap-once, per-trace, engine-only
population), marker-absent fail-by-default is right, and it closes the leaf-drop hole my
reference-based signals genuinely miss — proven by a hermetic test. **It makes incomplete
traces safe for the failure mode that actually occurs (the BSP bulk export drop) and is
exact on the validated workloads.** There is one bounded, documented residual (a `received
> declared` masking window) I analyze precisely below; it does not cover the bulk-drop
mode and is empirically zero, so it is a v1-acceptable limitation, not a blocker.

## (a) `MissingSpans` is correctly in the gate family

- **Hard-fails** (gate.go): marker absent → violation (fail-by-default); marker present &&
  `MissingSpans > 0` → violation. Both make `Err() != nil`.
- **`=0`-by-construction** on faithful + complete data (received == declared when complete
  and the timing residual P = 0).
- **Clear messages** — both explicitly explain *why* a dropped leaf is invisible to the
  edge-based signals, so a future reader understands this is the only signal that catches
  it.
- **Composes cleanly, no harmful interaction.** It's a separate boolean violation, never
  summed with the others. A dropped *leaf* is caught only here; a dropped *parent* trips
  both `OrphanedParents` and `MissingSpans` — redundant-but-correct (both true, the trace
  is incomplete), which is good defense in depth, not double-counting. The two cover
  complementary drop classes (edge-breaking vs leaf).

## (b) Count + reconciliation — sound

- **Distinct, and the two sides count the SAME population by construction.** `OnStart`
  (wcprofcount.go) does `SetAttributes(WcprofEngineSpanAttr)` **and** `counts[tid]++` in
  the *same call*, so every counted span is marked and vice versa — declared and received
  are definitionally over the identical engine population. Received is counted over the
  **deduped** span set (loader.go), so it's distinct; the count increments once per span
  at start, so declared is distinct. The only declared−received gap is therefore *drops*
  (what we want) plus the P timing residual (below) — no mismatched-population skew.
- **Monotonic-accumulate + loader-keeps-MAX → a stale/lower total can never win.** The
  count only `++`s (reset only by `Reap` at session end), so successive `Stamp`s are
  non-decreasing; the loader keeps `max` (`if n > DeclaredEngineSpans`). The highest
  (final) stamp wins regardless of export ordering. The "real bug" (per-query reap
  fragmenting the count) is genuinely fixed: `defer Stamp(ctx)` at query end + a single
  `Reap(traceID)` at `removeDaggerSession` (session.go) — verified.
  - *Edge handled by composition:* if the root carrying the highest stamp itself drops,
    the loader's max degrades to an earlier stamp — but that dropped root orphans its
    whole subtree, so `OrphanedParents` fires. The two signals back each other up.
- **Stamp runs at query END (`defer`), so every synchronous span — including all
  ranking-critical work — is counted before P.** Confirmed at session.go:1482.
- **Concurrent traces keyed correctly** — `counts[trace.TraceID]`, mutex-protected; one
  trace per analysis (Compile rejects multi-trace).
- **`wcprof.engine_span` delimits the population** — the shared processor is on the
  per-client engine providers (main + nested module-runtime), so CLI-shell/otelhttp/
  buildkit spans are unmarked and excluded from both sides. Nested-client subtrees share
  the one counter, so the per-trace total spans the whole tree.

## (c) Marker-absent fail-by-default — right

`SessionMarkerPresent == false` → hard-fail (the gate refuses an unstamped/pre-checksum/
old trace rather than trusting it). Tested by `TestCompletenessGateFailsByDefaultWithout
Marker`. It does **not** over-refuse a complete trace: a complete trace from a stamping
engine carries the marker (the session root is always present), so `SessionMarkerPresent`
is true and the count check runs. It lives in the shared `Compile`, so it protects **both**
front-ends (otlpdump + Cloud) identically.

## (d) Does the gate now truly make incomplete traces safe? — Yes for the real mode; one named residual

**The hole is closed for the failure mode that motivated this.** `TestCompletenessGate
CatchesDroppedLeaf` is the proof and is hermetic + rigorous: it asserts the *precondition*
(a dropped user-work leaf leaves `OrphanedParents==0`/`UnresolvedWaitTargets==0` — the hole
is real) and then that `MissingSpans=1` makes the gate hard-fail "(it previously passed)."
The BSP bulk export drop (the ~8% large-trace loss, hundreds of spans) has drop size `X`
≫ any P, so it is always caught.

**The residual I was asked to hunt — `received > declared` masking — is real, bounded,
and documented.** The reconciliation is a *lower-bound* check: declared `D = T − P`, where
`P` = engine spans that **start after the last query's Stamp** (the producer excludes them
— session teardown, post-query async). A drop of `X` is caught iff `X > P`; a drop of
`X ≤ P` keeps `received ≥ declared` and is masked (`MissingSpans` clamped to 0). The
producer documents exactly this ("can only make received ≥ N… cannot mask a synchronous
drop *in the common case*"). My assessment:
- **Empirically P = 0** on the validated workloads ("declared == received exactly"), so the
  check is *exact* there — any drop is caught.
- **Safe-direction:** a complete trace always has `received = T ≥ D = declared`, so it
  always passes — **no false refusal** of a good trace. The residual is false-*negatives*
  (missed sub-P drops), never false-positives.
- **The bulk mode is always caught** (`X ≫ P`), so the motivating risk is fully closed.
- **The limitation:** for a workload with `P > 0`, a small drop (`X ≤ P`) — of *any*
  spans, including ranking-critical ones — can be masked. This is inherent to stamping the
  count on an in-trace span (you cannot stamp after the root ends). It is narrow and
  documented, not papered over.
- **Suggested future tightening (not a v1 blocker):** emit a single teardown "session
  complete" span at `removeDaggerSession` carrying the *final* count; that drives `P → 0`
  by construction and makes the checksum exact for all workloads. Worth a follow-up note;
  the current lower-bound check + documented residual is a reasonable v1 cut given the
  bulk mode is covered and P is empirically 0.

## (e) Tightenings correct; zero replay-logic change

- **Round-trip "complete" is now STRUCTURAL graph-equality** (`graphFingerprint` /
  `assertSameGraph`), a front-end-independent identity rather than the loader's internal
  `uint64` opID — strictly more rigorous than the old count-only check (two graphs with
  the same counts but different structure now diverge), and it requires the local
  reference to itself be complete (marker + 0/0) "for the comparison to mean anything."
  Sound.
- **CLI separates report-write I/O error from gate failure** (`gateOK, werr := analyze(…)`)
  — a disk-write error is surfaced as-is, not conflated with a gate refusal. Correct.
- **Zero compile/replay LOGIC change.** `wcanalyze/` is untouched (verified); the loader's
  reconciliation is a new **additive** block over the deduped spans — it changes none of
  the existing Compile parentage/wait/op logic. The signal is purely additive, consistent
  with the principle.

## Verdict

**Sign off — last safety piece for v1.** `MissingSpans` is a correctly-built, sound,
hermetically-tested addition to my gate family that closes the leaf-drop hole the
edge-based signals miss, with mark/count coupling guaranteeing population alignment,
monotonic-accumulate + MAX guaranteeing a stale total can't win, and marker-absent
fail-by-default. It makes incomplete traces safe for the BSP bulk-export-drop failure mode
(always caught) and is exact on the validated workloads. The one residual — a sub-`P`
masking window from stamping the count on an in-trace span — is bounded, empirically zero,
safe-direction, and honestly documented; I recommend the teardown-span tightening as a
follow-up to make it exact, but it is not a v1 blocker. The tightenings are correct and the
change is additive (no replay logic touched). Ship v1.
