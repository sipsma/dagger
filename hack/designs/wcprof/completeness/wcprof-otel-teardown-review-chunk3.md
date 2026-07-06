# Teardown-final-count review — `4074ad7867` (closes the cutoff asymmetry) — Chunk 3 implementer

**Reviewed:** `git diff 501653ddcf..4074ad7867` + the files, in the coder worktree. This closes
the residual I pinned last round. Adversarial pass for the cutoff + a third path. file:line are
that worktree. Review only — did not modify the branch.

## SIGN-OFF — the residual I pinned is closed. No third path in scope. v1 complete + safe.

The fix is exactly the close I recommended (stamp the exact final count at teardown on a
guaranteed-last carrier), and I verified it eliminates the asymmetry by construction and does
not open a new window. One theoretical out-of-scope edge noted, non-blocking.

### (a) My cutoff asymmetry is ELIMINATED — `received ≤ declared` by construction

The count is now read at teardown **after** span emission has quiesced, so the post-stamp
window I flagged is gone. Verified the quiescence chain in `removeDaggerSession` (session.go):
1. `sess.services.StopSessionServices(ctx, ...)` (:439) — services stopped.
2. **drain** `sess.dagqlMu.Lock(); sess.dagqlClosing = true; for sess.dagqlInFlight > 0 {
   dagqlCond.Wait() }` (:449-455) — every in-flight query (and its lazy/exec/nested-client
   subtree, all on the session-wide `dagqlInFlight`) has finished creating spans.
3. `stampSessionComplete` → `Final(traceID)` (:471) — reads `counts[tid]` with no further
   marked-span creation pending → the **exact** total.

So `declared` is the exact final count, and `received` (distinct marked spans) can only be ≤ it
— any drop (leaf, whole trailing query, or the post-query async padding I flagged) now yields
`received < declared` and is caught. The empirical `declared==received==5312` (up from the
5310 floor — it now includes the post-stamp window I flagged) confirms the window is captured.

**Quiescence of my domains specifically:**
- **lazy** — forced via `Cache.Evaluate` inside resolvers, so it runs under an in-flight dagql
  op → covered by the `dagqlInFlight` drain.
- **exec** — created in the `withExec` resolver → in-flight dagql → drained.
- **service** — stopped at :439 *before* `Final`; and a service *stop* creates no new marked
  span (Chunk 4 emits `service.start` + its waits only at START; stop merely ENDS the
  long-lived availability span, and `OnEnd` doesn't touch the count) → no post-`Final` padding.
- **nested-client** — nested module-runtime clients are sub-clients of the one `daggerSession`
  (`sess.clients`), so their spans increment the same per-trace counter and their work is
  drained by the same session-wide `dagqlInFlight`; `stampSessionComplete`/`Reap` run once per
  session (not per nested client), so there is no nested-teardown race.

### (b) Third-path hunt — no in-scope path; one out-of-scope edge

I tried each candidate the brief named:
- **A marked span created after `Final`, then exported:** the only post-`Final` activity is
  container/buildkit/cache release (releaseGroup), which runs on the **span-less teardown ctx**
  (stampSessionComplete itself has to *construct* a span context because `ctx` carries none),
  so `Tracer(ctx)` is the global/no-op provider, NOT a counted per-client provider → such spans
  are unmarked → never counted in `received`. The implementer's residual ("a container-release
  span on a non-per-client tracer … neither inflates received nor escapes the count") is
  therefore genuinely nil, and the session is removed from the map (:424) before the drain so
  no NEW query can arrive. ✓
- **Async service span after stamp:** no — stop ends spans, doesn't create marked ones (above).
- **Nested-client teardown race:** no — sub-clients of one session, covered by the single
  drain + single stamp/reap (above).
- **Carrier in a different trace:** the carrier is parented at `trace.NewSpanContext{TraceID:
  wcprofTraceID, SpanID: wcprofRootSpanID, FlagsSampled}` → it lands in the session trace; an
  invalid/uncaptured trace id makes `stampSessionComplete` return early → no carrier → the
  loader fails by default (safe over-refusal, never a silent pass). ✓
- **THE ONE THEORETICAL EDGE (out of scope):** two distinct `daggerSession`s sharing ONE
  traceID (explicit cross-invocation traceparent propagation). The per-trace counter +
  reap-once-on-first-teardown + the loader's MAX-of-carriers could then mis-reconcile (a first
  session's reap zeroes the shared counter for a second). This is outside the design §10 model
  ("one Cloud trace = one engine's view of one session") that the whole loader assumes, and it
  does not arise from nested clients (sub-clients, not sessions). Note for the record; not a
  blocker under the stated scope.

### (c) Carrier is fully neutral — verified on all three axes

- **Not in declared:** `OnStart` early-returns on `s.Name() == wcprofSessionCompleteSpanName`
  (wcprofcount.go) — no mark, no increment — so the carrier is excluded from the total it
  carries.
- **Not in received:** because it's unmarked (`WcprofEngineSpanAttr` never set on it), the
  loader's `ReceivedEngineSpans` (counts marked spans) excludes it.
- **Not a graph node / no false orphan:** the loader reads the count first
  (`WcprofSessionSpanCountAttr`), then filters every `WcprofSessionCompleteAttr` span out of
  `deduped` (loader.go) before the op-id sort — so it never becomes an op, never an orphan,
  never a root. Belt-and-suspenders: it's also given a real parent (the session-root), so even
  unfiltered it wouldn't read as an orphaned-parent false root. The filter is correctly placed
  *after* the declared-count read and *before* the sort/op-build (verified). ✓
- **Survives a trailing-query (session-root) drop:** the carrier is a separate teardown span
  exported on its own; if the session-root drops, the carrier still lands in the trace carrying
  the full count, `received` is short by the dropped subtree → `missing>0` caught, and the
  filter keeps the carrier from adding a spurious orphan. Matches the "missing=7" validation. ✓

### (d) Tightenings + zero compile/replay change

- **Drain reorder is safe and a genuine improvement.** Moving the drain ahead of telemetry
  shutdown (and the cache release) doesn't regress: the resolver is already nil'd and the
  session removed from the map before it, so no new ops; in-flight ops keep their captured
  resolver/containers (released later) and finish. As the implementer notes, it also fixes a
  latent late-query telemetry loss (a late query now records before its provider closes). ✓
- **Marker-absent fail-by-default holds** — `stampSessionComplete` returns without a carrier
  when the trace wasn't captured or the count is 0, and the loader refuses a trace with no
  `wcprof.session_span_count` marker. ✓
- **Zero compile/replay change confirmed** — the diff touches no `wcanalyze` replay/graph core;
  loader changes are the additive carrier filter only. ✓

## Bottom line

The N-vs-received cutoff asymmetry I pinned is closed by construction (`declared` = exact
`Final` read after the drain + service-stop, with post-`Final` release spans living on unmarked
tracers), the carrier is fully neutral on all three axes, and I could not find an in-scope
third silent-incompleteness path — only the out-of-§10 multi-session-per-trace edge, which the
loader's single-session scope already excludes. **SIGN OFF — v1 is complete and safe; the
effort is done.**

**(Carried items remain fully closed: service.start §3.4 retired; publishResult-parentless
moot/capture-artifact. Nothing further owed from me on this workstream.)**
