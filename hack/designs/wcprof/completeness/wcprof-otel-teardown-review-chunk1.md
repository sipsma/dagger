# wcprof × OTel — teardown final-count review (Chunk 1 / loader+gate owner)

**Reviewer:** Chunk 1 owner (offline loader + §6.1 gate). This closes the P-window
residual I named when I signed off `MissingSpans` — it is my recommended tightening.
Reviewed `4074ad7867` atop `501653ddcf` (unpushed). Merge gate. No code modified.

## SIGN-OFF ✅ — the gate is now EXACTLY sound; v1 complete

The per-query running-floor is replaced by a single exact declaration read after the
session has quiesced, so `received ≤ declared` holds **by construction** and any drop —
a leaf, a whole trailing query, or post-query padding — shows up as `received <
declared` and hard-fails. The carrier is excluded from both sides and filtered from the
compiled ops (graph/replay untouched), the teardown reorder is safe (and fixes a real
late-query telemetry loss), and the documented post-teardown residual is genuinely nil.
The residual I previously named is gone. No blocker.

## (a) The P-window is eliminated — `received ≤ declared` by construction

`Final(traceID)` is read only after span emission for the trace has quiesced, verified
in `removeDaggerSession`:
1. `StopSessionServices` (session.go ~:28) → no more service spans.
2. drain in-flight queries (~:40) → no more query spans (and their nested-client
   subtrees, which share the one counter).
3. `stampSessionComplete` (~:59) reads `Final` — now the EXACT final count — and stamps
   it on the carrier.

So every marked span that can be *received* was created before `Final` and therefore
counted ⇒ `received ⊆ declared` ⇒ `received ≤ declared`. The old lower-bound `P` window
(post-stamp received-but-undeclared spans, plus the trailing-query-drop that reverted
the loader-MAX) is closed: the only spans created after `Final` are the carrier
(excluded both sides) and post-shutdown cache/container release (residual (e)). The
old per-query `Stamp` + loader-MAX is removed; `Final` is a single non-reaping read.

## (b) `MissingSpans` is now an EXACT invariant, not a lower bound

With one exact declaration, `received == declared` iff complete and `received <
declared` iff *any* counted span dropped (leaf, trailing query, padding). The loader
reconciliation is unchanged (`declared − received` when `declared > received`), but it
now operates on an exact `declared`, so the clamp can no longer hide a sub-P drop.
Backstop intact: if the **carrier itself** drops, `SessionMarkerPresent == false` →
fail-by-default, so the declaring span being lost is also caught. Tested:
- `TestCompletenessCarrierExactExcludedFromOps`: complete carrier-form trace →
  `missing=0` exact; drop the leaf → `received(4) < declared(5)` → `missing=1` caught
  while `OrphanedParents`/`UnresolvedWaitTargets` stay 0.
- `TestCompletenessGateCatchesDroppedLeaf`: same catch via the marker on a root span
  (the count read is location-agnostic, so both forms exercise the reconciliation).
- `TestCompletenessGateFailsByDefaultWithoutMarker`: marker absent → refused.
- Live: `declared=received=5312` exact, trailing-query → `missing=7`, multi-query (1724
  nested) reconciles exactly — the `declared` rising 5310→5312 is the direct evidence
  the P-window spans are now declared (so padding can no longer mask).

## (c) Carrier excluded both sides + filtered from ops — additive, graph/replay untouched

- **Self-excluded from the count:** `OnStart` returns early for
  `wcprof.session_complete` (no `WcprofEngineSpanAttr`, no increment), so it inflates
  neither `declared` nor `received`.
- **Filtered from compiled ops:** the loader drops any `WcprofSessionCompleteAttr` span
  from `deduped` *after* reading its declared count ("read just above") and *before* the
  op-construction loop, then sets `SpanCount` on the filtered set. So the carrier never
  becomes an op — the op/parentage/wait logic is byte-for-byte unchanged on the
  real-span set (`TestCompletenessCarrierExactExcludedFromOps` asserts `SpanCount=5` and
  "no carrier leaked into the graph"). The validation `declared=5312, gate 0/0` confirms
  the read-then-filter order (a filter-before-read would zero the marker and
  fail-by-default a complete trace — it doesn't).
- **No false orphan even if unfiltered:** the carrier is `Start`ed with a parent set to
  the recorded session root (`wcprofTraceID`/`wcprofRootSpanID`), so it lands in the
  trace and, were the filter ever removed, would still resolve a real parent rather than
  read as an orphaned-parent false root. Good defense in depth.

## (d) The dagql-drain reorder is safe, and the late-query fix is real

Ordering in `removeDaggerSession` is intact and correct: stop services → drain → stamp
→ reap → parallel release group (container release ∥ `ShutdownTelemetry` ∥ DB close).
Moving the drain ahead of telemetry shutdown is strictly an improvement: a late/draining
query now exports its spans *before* its provider closes instead of after (the "latent
late-query loss" — genuine, not a regression), and it is what makes `Final` exact (no
query still creating spans). The resource release runs after, with dagql quiescent
(comment + structure confirm). The drain is bounded by the teardown `ctx`'s `cancel`, so
no new hang. Empirically the engine tears down cleanly and the 1724-nested multi-query
trace reconciles exactly, so the reorder breaks no teardown invariant.

## (e) The post-teardown residual is genuinely nil

Spans created after the `Final` stamp (e.g. container/lease release) do not reach
`received`: they run on non-per-client tracers (so unmarked — not counted, not in
`received`) and/or after the per-client telemetry shutdown (so unexported). Either way
they neither inflate `received` (no mask) nor are expected in it (no false fail). The
exact `declared=received` reconciliation on every complete capture (including the
1724-nested case) is the empirical confirmation. This is as airtight as the architecture
allows — the count is declared at the last point a span can still be both counted and
exported.

## One optional hardening (forward-looking, NOT a blocker)

`received ≤ declared` is now a load-bearing invariant, but the loader still *clamps*
`received > declared` to `missing=0` (a leftover from the lower-bound era). Under the
exact invariant `received > declared` is impossible in a correct capture (mark and count
are coupled in one `OnStart`, dedup collapses live duplicates, the carrier is excluded),
so a `received > declared ⇒ violation` guard would **never** false-fail yet would make a
future quiescence regression (a marked-and-exported span slipping in after `Final`) fail
*loud* instead of silently re-opening the masking window. Cheap, pure-upside
self-check on the new invariant. I'd add it, but it does not block — the quiescence holds
and is validated.

## Verdict

**Sign off — v1 complete.** The teardown final-count makes `MissingSpans` an exact
checksum: `received ≤ declared` by construction, so any dropped span (leaf or otherwise)
is caught, and the carrier-drop fail-by-default backstops the declaration itself. The
carrier is neutral on both sides and filtered from the graph (replay untouched), the
reorder is safe and fixes a real telemetry loss, and the post-teardown residual is nil.
My gate now makes incomplete traces *exactly* safe — faithful data or refuse, with no
remaining masking window. Recommend the small `received > declared` guard as a follow-up
to keep the invariant self-checking. Ship v1.
