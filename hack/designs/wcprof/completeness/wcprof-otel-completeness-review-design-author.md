# Completeness-checksum review (merge gate) — design author, commit `501653ddcf`

The last safety piece: closing the dropped-LEAF blind spot (a leaf breaks no edge, so
the reference-based §6.1 signals miss it → silently wrong ranking). Reviewed
`git diff 9555281f27..501653ddcf` at file:line.

## Verdict: SIGN OFF. v1 is fully safe + complete.

The checksum realizes "faithful data or refuse, never a wrong answer" by detect-and-refuse
with zero inference, joins the §6.1 hard-fail family cleanly, and leaves the validated
compile/replay core untouched. The multi-query monotonic-accumulate fix is correct and the
wiring is right. Three documented residuals, none blocking.

## (a) Principle-faithful — yes, zero inference

The loader (loader.go:258-282) counts distinct received `wcprof.engine_span` spans and the
MAX declared `wcprof.session_span_count`, and the gate (gate.go:160-168) hard-fails when
`MissingSpans = declared − received > 0` OR the marker is absent. It **refuses** — it never
synthesizes, estimates, or back-fills the missing leaf. `MissingSpans` and
`SessionMarkerPresent` are new members of the §6.1 `=0`/hard-fail family (added to
`violations`, surfaced in `Write`), sitting beside `OrphanedParents`/`UnschedulableOps` —
the right home. The declared count is string-encoded (`strconv.Itoa`/`Atoi`), so it rides
through Cloud's JSON without float64 coercion exactly as §3.0/§6.6 require; the marker is a
plain bool. This is the principle applied to the one loss class that leaves no trace
evidence.

## (b) No compile/replay logic change — confirmed

`wcanalyze/replay.go` and `graph.go` are untouched (diff name-only). The checksum is purely
additive: an emit-side `SpanProcessor` + per-query stamp (engine/server), a loader count in
`Compile`, and a gate signal. The analysis core stays the validated model.

## (c) Sound design

- **Engine-vs-CLI population split — principled.** `N` counts only engine spans (the
  ranking-critical class: call/call_exec/exec/lazy/service + user/module work), marked at
  `OnStart` by the engine's shared processor (wcprofcount.go:54-64). CLI-shell / otelhttp /
  buildkit spans are a **different producer** the engine cannot declare for, so they are
  unmarked and excluded from both sides — they "cannot cause a false pass/fail"
  (attrs.go). The CLI→Cloud export carries the engine's forwarded spans, so an engine span
  dropped there *is* caught (it's marked, declared, and now missing); only the CLI's *own*
  shell spans are out of scope, which matches the engine-work-first north-star.
- **Monotonic-accumulate + per-query Stamp + keep-MAX + Reap-once — correct, and the right
  fix for the real bug.** A single command issues many main queries under one trace, so a
  per-query reset fragmented the count; the fix accumulates per trace, stamps the running
  total on each `POST /query` root (Stamp, query end), the loader keeps the MAX (the final
  total once the last query stamped), and the entry is reaped once at session teardown.
  Wiring verified: one shared `newWcprofSpanCounter()` (server.go), registered
  `WithSpanProcessor(srv.wcprofSpanCount)`, `defer srv.wcprofSpanCount.Stamp(ctx)` pinned to
  the `POST /query` span, `Reap(sess.wcprofTraceID)` in `removeDaggerSession`. The count
  spans the nested-client tree (shared instance on every per-client provider), so
  module-runtime engine spans are included. Dedup-safe: received counts the **deduped**
  set (the ended copy), matching the producer's one-count-per-span at `OnStart`; validated
  declared==received exactly on complete traces.
- **Fail-by-default (marker absent → refuse) — the right conservative posture.** An
  unstamped/pre-checksum/old capture is refused rather than trusted, and it does not
  over-refuse: a stamped complete trace carries the marker and passes (validated). This is
  exactly "unverifiable ⇒ refuse."

## (d) The leaf-drop hole is genuinely closed

For the common failure mode — individual engine leaf spans dropped under BSP pressure, with
the declaring `POST /query` root surviving — received < declared → refused. The validation
is honest and sufficient: complete local + 3 complete real-Cloud round-trips reconcile
exactly; an **induced real leaf-drop → MissingSpans=1 → REFUSED** exercises the failure arm
on real engine data; and since both front-ends share this loader, a Cloud drop is handled
identically. The "live Cloud came back complete at these sizes (no organic drop)" caveat is
acceptable — the failure path is what matters and it's exercised.

## (e) Tightenings + canonical alignment

- Round-trip "complete" now asserts **structural graph-equality** (a front-end-independent
  identity), stronger than a span-count match. Good.
- The CLI now separates a report-write I/O error from a gate failure — correct (don't
  conflate "couldn't print" with "trace refused").
- No canonical conflict; this is a faithful completion of §6.1 — the missing member of the
  faithfulness family that the reference-based signals structurally could not cover.

## Residuals — documented, none blocking

1. **Post-last-Stamp engine spans** (session shutdown, async service-availability) are
   excluded from `N` — documented in wcprofcount.go: they can only make received ≥ N (never
   a false fail) and are non-ranking-critical (the availability span is already
   non-self-time-bearing per §3.4). Acceptable.
2. **Whole-trailing-query drop** (the final query lost *in its entirety*, including the root
   that carries the max declaration) → keep-MAX reverts to the prior total → received ==
   declared → silent pass. This is the fundamental limit of a per-unit declaration (if the
   unit that declares the total is itself wholly lost, nothing references it). It is far
   rarer than the individual-leaf case this closes — the common BSP failure drops individual
   spans, and a final-root drop with *any* surviving child is caught by `OrphanedParents`;
   only the entire-final-query-gone case escapes. Non-blocking; worth documenting alongside
   residual #1, and fully closeable later by stamping the final count at teardown on a
   persistent session-level span (so the declaration survives a trailing-query drop).
3. **CLI-side span completeness** is out of scope (a separate producer the engine can't
   declare for); consistent with engine-work-first ranking. A CLI-side checksum is the
   follow-up if CLI-shell time ever needs to headline.
   *(Tiny, safe-either-way note: whether a start-only/end-dropped span's mark lands on the
   start snapshot depends on span-processor order; both outcomes are safe — counted-missing
   → refuse, or flagged via OpenSpanCount — so no silent wrong answer.)*

## Bottom line

**Signed off — merge gate cleared; v1 fully safe.** The completeness checksum closes the
dropped-leaf blind spot the way the principle demands: the producer declares its
engine-span total, the loader refuses any trace that received fewer or carries no
declaration, with zero inference and no change to the validated analysis core. It fits the
§6.1 faithfulness family cleanly, the multi-query accounting is correct and wired right, and
the failure arm is proven on real induced loss. The residuals (post-stamp spans, whole-
trailing-query drop, CLI-side completeness) are rare, non-ranking-critical, or out of the
engine's declarative scope — documented, not papered over. With this, every loss class is
either caught by an edge break or by the count, and the wcprof × OTel second source is
complete and safe to merge.
