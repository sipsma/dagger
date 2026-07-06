# Completeness-checksum review — `501653ddcf` (leaf-drop gap fix) — Chunk 3 implementer (lazy / nested-client owner)

**Reviewed:** `git diff 9555281f27..501653ddcf` + the new files, in the coder worktree.
Adversarial pass for a residual silent-incompleteness path. file:line are that worktree.
Review only — did not modify the branch.

## Verdict: ONE residual found — name it. Everything else signs off.

The design is mostly very strong, with one elegant invariant that closes the holes I went
looking for. But I did find a residual silent-incompleteness path — the **N-vs-received
population cutoff asymmetry** — which is exactly the class this checksum exists to eliminate.
It is bounded, the primary threat (BSP overflow) is still caught, and the implementer
*documented* it — but "documented residual" in the last safety piece is not "closed." I
recommend closing it (a teardown final-count stamp) or, if accepted for v1, stating the exact
bound rather than the "common case" hedge. Owner's call on block vs fast-follow.

---

## (a) The residual silent-incompleteness path I found — and the holes I ruled out

### THE HOLE: late-span (post-last-Stamp) padding can mask an equal number of early drops
`N` (declared) is `counts[tid]` read at the **last query's `Stamp`** (wcprofcount.go:88-99,
called via `defer ...Stamp(ctx)` at query end). `received` is the loader counting **every**
distinct `WcprofEngineSpanAttr` span it got (loader.go: `if attrBool(...) c.ReceivedEngineSpans++`).
The gate fails only on `received < declared` (`DeclaredEngineSpans > ReceivedEngineSpans`).

But engine spans are created on the marked providers **after** the last `Stamp` — the
implementer names them: "session shutdown, async service-availability." Each such span:
- is marked + counted into `counts[tid]` by `OnStart` (so it's a real engine span), but
- is **not** in the stamped `N` (the stamp already happened), yet
- **is** counted in `received` if it is received (it's marked).

So `received = N_at_stamp − (early drops) + (late received)`. If `late_received ≥ early_drops`,
then `received ≥ declared` → `MissingSpans = 0` → **a dropped early leaf is silently masked.**
That is a missed-drop path in the checksum's own domain. The code comment
(wcprofcount.go: "can only make received >= N (never a false fail); they cannot mask a
synchronous drop **in the common case**") is honest about the no-false-fail direction, but the
"common case" qualifier *is* the hole — it can mask in the adversarial case.

**Why it is bounded (and the primary threat is still caught):** the masking window is exactly
the number of post-last-Stamp marked-and-received engine spans (a handful: shutdown / async
availability). The motivating threat — BSP-overflow leaf-drops — happens *during* the workload
(those spans are created before the query's stamp, so they're in `N`), and an overflow drops
*many* spans → `received ≪ N` even with the handful of late pads → still caught. The residual
only bites a **small/transient** drop (1…K, K = late-span count) co-occurring with ≥K late
spans. Narrow, but non-zero, and it's a silent wrong ranking when it bites.

**Recommended close (preferred):** stamp the **final** `counts[tid]` at session teardown
(`removeDaggerSession`, where `Reap` already runs) onto a guaranteed-last-ending marked span,
so `N` covers all engine spans and `received ≤ N` always → every drop yields `received < N`.
Then the checksum has *no* silent window. **Alternative (if accepted for v1):** replace the
"common case" comment with the exact bound — "`received` can mask up to K dropped spans, K =
engine spans created after the last query stamp" — so it's an explicit owner-accepted limit,
not a hedge (the principle: be precise about the boundary, don't hand-wave it).

### Holes I went looking for and RULED OUT
- **Non-engine spans padding `received`:** can't happen. The marker is set only by
  `wcprofSpanCounter.OnStart`, which runs in the **engine process** on its per-client
  providers; CLI-shell / otelhttp spans (separate process / unmarked providers) are never
  marked, so the loader never counts them as received-engine. ✓
- **A ranking-critical span outside the counted population (my domains: lazy / exec / service /
  nested-client):** ruled out by a clean invariant — `OnStart` **marks and counts in the same
  call**, so *counted ⇔ marked* by construction (no "counted-but-unmarked" or vice-versa). And
  the marker rides **provider inheritance**: every span is created via `Tracer(ctx) =
  SpanFromContext(ctx).TracerProvider()`, so every descendant of a marked per-client root
  (main + nested-client, both registered — session.go) is on a marked provider → marked +
  counted. lazy (`beginOTelLazyOp` on `evalCtx`), exec/service (Chunk 4, on the client ctx),
  publishResult, and nested-client subtrees all inherit the marked provider. ✓
- **MAX-stamp regression masking a drop (multi-query):** the loader keeps `max` declared
  (loader.go: `if n > c.DeclaredEngineSpans`). If the largest-stamping session-root **drops**,
  `N` falls to a smaller surviving stamp — but a dropped session-root **orphans its received
  children** (`OrphanedParents`), which the existing structural gate catches. The only
  sub-case that escapes orphan-detection (the *entire* query subtree drops) folds into the
  late-span residual above (its spans are counted but not in the surviving stamp). ✓ (caught or
  folds into the named residual)
- **Dedup double-count:** `received` is counted over `deduped` (distinct), and `OnStart` fires
  once per span, so `N` is also distinct — both sides distinct, live+ended copies collapse to
  one. ✓
- **Off-by-one (root counted on one side):** the session-root's own `OnStart` increments
  `counts` and it carries the marker, so it's on both sides; the "complete → 0/0" validation
  confirms no systematic ±1. ✓

## (b) Count correctness — sound (modulo the (a) cutoff)
Per-trace keyed (`counts map[trace.TraceID]int`, OnStart guards `tid.IsValid()`); concurrent
traces independent; `mu`-guarded. Monotonic-accumulate + loader-MAX + reap-once at
`removeDaggerSession` correctly handles the many-queries-per-trace case the implementer found
(no per-query fragmentation). The mark==count invariant is the load-bearing correctness
property and it holds. The one correctness gap is the (a) cutoff asymmetry, not the counting
mechanics.

## (c) Marker-absent fail-by-default — right; both front-ends; no over-refusal
`!SessionMarkerPresent` → hard-fail with a clear "old/unstamped capture fails by default"
violation (gate.go) — an unstamped or pre-checksum trace is correctly refused. The
count/compare logic lives in the **shared** `wcotel.Compile`/gate, so both otlpdump and Cloud
front-ends get it identically. No over-refusal: a complete trace stamps the marker and
`received == declared` (or `≥`, with late spans) → `MissingSpans = max(0, declared−received) =
0` → passes; the validation (complete local + complete real-Cloud → 0/0) confirms it. ✓

## (d) Tightenings + zero compile/replay change
- **Zero compile/replay change confirmed** — the diff touches no `wcanalyze` replay/graph core
  (only loader/gate get the additive count fields). ✓
- Round-trip "complete" upgraded to **structural graph-equality** (stronger than Chunk 5's
  op/wait-count check — a real tightening I'd asked the spirit of). ✓
- CLI separating report-write I/O error from gate failure is a correct robustness fix (don't
  conflate an output-write error with an analysis refusal). ✓
- The string-encoded `wcprof.session_span_count` (Atoi/Itoa) correctly dodges the Cloud
  float64 coercion, consistent with the wait-ns design. ✓

## Bottom line
This is a strong final safety piece — the mark==count invariant + provider inheritance close
the population-coverage holes cleanly, and the marker-absent fail-by-default is exactly right.
The **one residual** is the N-vs-received cutoff asymmetry: late (post-last-Stamp) engine spans
pad `received` and can mask up to that many early leaf-drops — a narrow but genuine silent path
in the checksum's own domain. The primary BSP-overflow threat is still caught, and it's
documented, so this is not catastrophic — but per the "no silent incompleteness" principle I
**recommend closing it** (teardown final-count stamp) before calling v1 complete, or at minimum
replacing the "common case" hedge with the exact bound. With that resolved (or explicitly
accepted as a stated bound), **I sign off** — everything else holds.

**(Carried items — fully closed across the feature: service.start §3.4 retired;
publishResult-parentless moot/capture-artifact. Nothing else owed from me.)**
