# Teardown-final-count review (merge gate) — design author, commit `4074ad7867`

Closes the tail-drop residual I flagged on the completeness checksum (the whole-trailing-
query drop + post-stamp padding) by replacing the per-query running-total *lower bound* with
ONE exact declaration at teardown. Reviewed `git diff 501653ddcf..4074ad7867` at file:line.

## Verdict: SIGN OFF. The wcprof × OTel second source is complete + safe to merge.

The exact-at-teardown model makes completeness an exact invariant (`received ≤ declared`,
`==` iff complete), principle-faithful (detect-and-refuse, zero inference), with the carrier
excluded from the analysis so the validated graph/replay is untouched. The session-lifecycle
reorder is safe and a genuine improvement. One non-blocking hardening recommended.

## (a) Principle-faithful + EXACT — yes

- **Exact invariant.** `Final(tid)` (wcprofcount.go) reads the per-trace engine-span count
  at teardown, *after* the query drain, so it is the EXACT total — not a per-query running
  floor. The loader reads it off the carrier and computes `MissingSpans = declared −
  received`. Because the declaration is exact and every received marked span was counted at
  `OnStart`, `received ≤ declared` always holds, so any drop — individual leaf, whole
  trailing query, or post-query padding — surfaces as `received < declared`. This is the
  exact invariant my last review asked for; the lower-bound escape (trailing-query →
  `received == undercounted-max`) is gone.
- **Zero inference.** Still pure detect-and-refuse — nothing synthesized/estimated.
  `MissingSpans`/marker-absent remain in the §6.1 hard-fail family. Count is string-encoded
  (`strconv.Itoa`/`Atoi`) — the §3.0/§6.6 float64-dodge.
- **Carrier excluded from the analysis.** The carrier (`wcprof.session_complete`,
  `WcprofSessionCompleteAttr`) is filtered from the compiled ops *after* its count is read
  (loader.go:283-296), and it is never marked `WcprofEngineSpanAttr` (OnStart skips it by
  name), so it counts toward neither `declared` nor `received` and never becomes a graph op
  (no false root/orphan). Graph/replay see exactly the same ops as before.

## (b) No compile/replay change — confirmed

`replay.go`/`graph.go` untouched (name-only diff). The change is the loader carrier-filter
(additive, after the count read) + the emit-side teardown stamp + the `Stamp→Final` rename.
The analysis core stays the validated model.

## (c) Sound + safe — the session-lifecycle reorder verified

- **Carrier survives a trailing-query drop.** It is parented at the recorded session-root
  (`trace.NewSpanContext(wcprofTraceID, wcprofRootSpanID)`), i.e. the *first* outermost
  query's `POST /query` span (captured once via `wcprofTraceOnce`), which is early and
  survives; the carrier itself is created at teardown on the main client's still-live
  tracerProvider and `End()`ed so the live exporter ships it before telemetry shutdown. So
  the declaration no longer rides on the last query's root — exactly the fix for the residual.
  If the carrier itself drops → marker absent → fail-by-default. If the session-root drops →
  its first-query children orphan → `OrphanedParents`. Both covered.
- **The drain-reorder is safe and a real improvement.** Moving the dagql drain
  (`dagqlClosing=true; wait dagqlInFlight==0`) *ahead* of the completeness stamp + telemetry
  shutdown + container/cache release means: (1) the counter is final before `Final` reads it;
  (2) a late query's spans are recorded while its provider is still up (the latent
  late-query-loss fix); (3) in-flight queries finish while their containers/cache are still
  alive — strictly safer than the old position (drain *after* container release + analytics
  close, where a draining query could block on already-released resources). Nothing between
  the new drain and the cache release re-opens dagql (`dagqlClosing` stays true; the carrier
  stamp creates a telemetry span, not a dagql query), so the cache release still sees a
  quiescent dagql. No deadlock/use-after-free introduced; the comment correctly notes dagql
  is already quiescent at the cache-release point.
- **Fail-by-default unchanged** (no carrier / zero count → refused).

## (d) Every loss class caught — within engine scope

1. Non-leaf drop (parent/wait target) → `OrphanedParents` / `UnresolvedWaitTargets`.
2. Individual leaf drop → `received < declared` (`MissingSpans`).
3. **Whole trailing-query drop** → the exact carrier total includes the trailing query, so
   `received < declared` — **now caught** (the residual closed).
4. Carrier drop → marker absent → fail-by-default.
5. CLI-side drop → out of engine scope (separate producer; documented; engine-work-first).
Validation substantiates it: complete local + real Cloud reconcile `declared=received=5312`
exactly (gate 0/0, roots=1, 0 carrier ops); induced early-leaf-with-padding → `missing=1`;
induced whole 7-span trailing `POST /query` → `missing=7` (edges clean — the exact proof of
the residual closure); carrier drop → fail-by-default; 1724 nested queries reconcile exactly.

## One non-blocking hardening (recommend) + residuals

- **Recommend: also flag `received > declared`.** The design's exact invariant is
  `received ≤ declared`, but the loader only flags `declared > received`. The one theoretical
  silent path left is *post-`Final` marked padding*: a span created on a counted (per-client)
  tracer between the `Final` read and telemetry shutdown would inflate `received` without
  being in `declared`, and if such padding ≥ a concurrent small drop it could mask it
  (`received ≥ declared` → pass). The producer doc-comment argues — and the exact
  `5312==5312` reconciliation empirically confirms — that this is **nil** (release work is on
  non-per-client tracers / past shutdown, so it's unmarked). It is verified-nil today, but it
  is a load-bearing invariant a future change could break silently. A one-line guard —
  `received > declared` is *also* a violation (the exact invariant broke) → refuse — would
  make the "exact" claim airtight and is the natural completion of "received ≤ declared
  always." Cheap defense-in-depth; not a blocker (current state verified nil, and a mask
  needs padding ≥ drop, whereas real drops are typically larger than the fixed teardown set).
- **Residual (documented, verified nil):** the post-teardown padding above.
- **Residual (3) CLI-side completeness** remains out of scope (separate producer) — consistent
  with the engine-work-first north-star.

## Bottom line

**Signed off — the last piece lands.** The exact-final-at-teardown declaration closes the
trailing-query and padding residuals I raised: completeness is now an exact invariant
(`received == declared` iff complete), enforced by detect-and-refuse with zero inference, the
carrier filtered out so the validated analysis core is untouched, and the lifecycle reorder
is safe and fixes a latent late-query loss besides. Every loss class within engine scope is
caught — by an edge break, the exact count, or marker-absence — proven on real induced
trailing-query loss (`missing=7`). The single remaining theoretical path (post-teardown
marked padding) is verified nil and cheaply closeable by also flagging `received > declared`;
I recommend that as defense-in-depth, not a merge blocker. The wcprof × OTel second source is
complete and safe to merge.
