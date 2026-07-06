# wcprof × OTel — Chunk 3 review (by the Chunk 1 implementer)

**Reviewer context:** I built Chunk 1 (loader, hardened §6.1 gate, vocabulary).
Two-part review: **(A)** Chunk 3 in isolation (lazy / deferred eval + the
`wcprof.parent` stamping processor, design §3.2/§3.0.2), **(B)** holistic — do
Chunks 1+2+3 compose and is the trajectory on the north star. Reviewed
`a460633b0f` against Chunk 2 (post-convergence) `b0e7cd9931`
(`git diff b0e7cd9931..a460633b0f`) and base `b442cd2533`.

**Verified by running, not just reading** (throwaway worktree at `a460633b0f`):
`go test ./engine/wcprof/wcotel/` **green**; the new **emit-path** tests in
package `dagql` — `TestLazyEmitProducerRepointStampsDirectChildrenOnly`,
`TestLazyEmitNoProducerNestsUnderHiddenLazyOp`,
`TestWcprofLazyParentProcessorStampsAllExports` — **all PASS**; `go build
./dagql/ ./engine/server/` **OK**; `go vet ./dagql/` shows **only** the
pre-existing `lostcancel` (now at `cache.go:3691`, shifted by Chunk 3's
additions; `otelprof_lazy.go` is vet-clean).

## Verdict

**Sound to build Chunk 4 on, and the Chunks-1–3 trajectory is sound.** This is
the subtlest break and the implementation handles it correctly: Invariant T is
satisfied (I traced the `lazyMu` scope), the stamping discriminator is right and
**tested on the real processor** (the descendant is genuinely left unstamped),
the UI re-point is untouched, and — the holistic headline — **the lazy emit
composes with my Chunk 1 foundation: the loader needed no change a third time,
the gate stays green on lazy work with `CycleWarnings == 0` (the §2.5 cycle risk
closed), and my wait-loss invariants hold.** All three of my Chunk 2 findings
(emit-path tests, provider coverage, the invalid-target silent-drop) are folded
in — the review loop is working. One real holistic watch-item (the empirical
oracle's scope mismatch) to resolve before §6.4; no blockers.

---

## (A) Chunk 3 in isolation

### Faithfulness — verified

**The stamping discriminator (§3.0.2) — correct, and the SDK contract holds.**
`wcprofLazyParentProcessor.OnStart(parent, s)` (`otelprof_lazy.go:96`) stamps
`wcprof.parent` iff the ctx carries the override **and**
`s.Parent().SpanID() == ov.producerSpanID` — direct re-pointed children only. I
confirmed the load-bearing SDK assumption: `tracer.Start(ctx,…)` calls
`OnStart(ctx, span)` with the **input** context, so `parent` carries the override
set on `callbackCtx`; and `s.Parent()` is the producer because
`resumedCallbackSpan.SpanContext()` returns the producer for direct children. A
descendant's Start ctx still carries the override (inherited) but its
`s.Parent()` is the intermediate work span, so the discriminator correctly skips
it — **`TestLazyEmitProducerRepointStampsDirectChildrenOnly` asserts the
descendant is unstamped on the real processor**, which is the precise guardrail
against subtree-flattening. ✔

**Invariant T (§3.0.1) — correct.** In `evaluateOne` (`cache.go:2982`–`3006`):
`beginOTelLazyOp` mints the lazy-op span → `shared.lazyEvalSpanCtx =
lazySpan.SpanContext()` stashes it → `shared.lazyEvalWaitCh = waitCh` publishes →
`shared.lazyMu.Unlock()` — **all under one `lazyMu` hold**, mirroring the native
`lazyEvalProfOpID` set just above. The joiner branch reads `shared.lazyEvalSpanCtx`
under the same lock before unlocking (`:2951`). Critically, the **old in-goroutine
span creation — the original Invariant-T violation the design flagged — is
removed**: the goroutine now just `callbackCtx := lazyCallbackCtx` (`:3009`) and
adopts the pre-minted span. Concurrency is safe (write-once-before-publish,
immutable `SpanContext`, same pattern as Chunk 2's `execSpanCtx`). ✔

**Two existence cases (§3.2 step 1) — both correct and tested.**
- *Producer context:* the lazy op **IS** the `resume <field>` span, minted in
  `beginOTelLazyOp` (`otelprof_lazy.go:152`) and the `resumedCallbackSpan`
  re-point is byte-for-byte unchanged → **UI unchanged**; the callback ctx gets
  the `wcprof.parent` override. `isResume=true`.
- *No producer:* a new hidden `ui.passthrough` lazy op named by the producing
  field, work nests under it by ordinary `parentId`, **no override**.
  `TestLazyEmitNoProducerNestsUnderHiddenLazyOp` asserts the work is unstamped
  and nests under the hidden op. ✔

**`emitOTelWait` on an invalid target — resolves my Chunk 2 LOW-1.** The
silent-drop early-return is gone: the wait is now emitted **targetless**
(`otelprof_hooks.go:101`+), the SDK retains it because it carries attributes
(verified: `recordingSpan.addLink` only drops an invalid-context link when it
*also* has no attributes/tracestate), the loader counts an `UnresolvedWaitTarget`,
and the gate fails loud — mirroring native's targetless `BeginWait`. This landed
in Chunk 2's convergence (`b0e7cd9931`), exactly the direction I recommended. ✔

### Correctness / robustness / performance

No correctness bugs. The lazy op correctly nests under the **consumer**
(`Tracer(evalCtx).Start`, a genuine synchronous nesting — the consumer blocks in
`waitForLazyEvaluation`). `lazyIsResume` correctly gates `DagBlockedAttr` to the
producer-case resume span only. No degenerate perf: the lazy emit is per-eval
(bounded), and the stamping processor's `OnStart` is **O(1)** — one
`context.Value` lookup that early-returns for every non-lazy span (negligible
beside the `LiveSpanProcessor.OnStart` export that runs anyway).

### The three divergences — I agree with all three

1. **Lazy-op class label (`"resume <field>"` vs native `profCallClass`) —
   benign, verified.** The lazy op's self-time is ~0 (the work is its child), so
   it's filtered by `minSelf` and never ranks. `TestChunk3LazyRepointFidelity`
   proves it: `lazyOp.SelfNS() ≤ 10ms`, the oracle filters it (`minSelf=5ms`) and
   still hits `Agrees(1.0, 0.01)` with the deferred work's `call_exec` self-time
   matching native **exactly**. The class-label mismatch cannot move the ranking.
   §3.2 already documents this. ✔
2. **Redundant leader wait link — harmless, verified.** The leader emits a `lazy`
   wait on its own lazy op (mirrors native's leader wait + Chunk 2's executor
   wait). The join is idempotent (`max`), and there is no over-subtraction: the
   lazy op is also the leader's child, but `SelfSegments` subtracts the **union**
   of child+wait intervals (`graph.go`), so the overlapping interval is removed
   once. Native does the same → oracle parity. ✔
3. **Provider-coverage reframe — correct, and it resolves my Chunk 2 HOLISTIC-2.**
   §9 framed "every per-client provider," but there is **one** provider per
   client with multiple export *processors*; one stamping processor prepended to
   `tracerOpts` (`session.go:696`) sets the attr on the shared span object before
   any export's live-start snapshot, covering own-DB + all parent exports.
   `TestWcprofLazyParentProcessorStampsAllExports` asserts the stamp survives to
   **both** exporters. ✔ Suggest updating §9's wording to "one prepended
   processor per provider covers all its export processors."

---

## (B) Holistic — Chunks 1–3 compose, trajectory on-target

**The loader needed no change a 3rd time** (verified empty diff over
`engine/wcprof/wcotel/**` + `telemetryattrs`). `wcprof.parent ?? parentId` reads
the new stamps with zero loader awareness of lazy semantics — the strongest
possible evidence the §3.0.2 "emit, never infer" contract holds.

**My hardened gate stays green on lazy work** — I checked each invariant against
`TestChunk3LazyRepointFidelity` on the loaded graph:
- *§2.5 cycle risk closed:* `CycleWarnings == 0` (the producer→work impossible
  edge never forms because work re-homes to the lazy op, not the ended producer). ✔
- *Wait-loss invariants hold:* both lazy waits resolve →
  `UnresolvedWaitTargets == 0`, `MalformedWaitTimings == 0`. ✔
- *No double-count:* the loader re-homes the direct work under the lazy op, so
  `producer.SelfNS() == 10ms` (its own, **excluding** the 114ms work),
  `producer` has **0 causal children**, `lazyOp.SelfNS() ≈ 0`, and total self ≤
  makespan. The deferred work's `call_exec` carries the real ~114ms. ✔
- *Counterfactual propagates through the lazy op:* scaling the work's real
  `call_exec` class shortens the consumer; scaling the generic lazy class does
  not (assertion 4). ✔

**My three Chunk 2 findings are all folded in** — emit-path now has real
in-memory-SDK tests feeding genuinely-exported spans through my loader+gate
(HOLISTIC-1), provider coverage is behaviorally tested (HOLISTIC-2), and the
invalid-target wait is gate-observable (LOW-1). Good signal that the chunked
review loop converges.

**Forward-looking (good for Chunk 4):** the direct-child discriminator naturally
leaves a lazy-triggered exec's descendants (the future `exec.run`/`containerStart`/
`processRun` spans) to nest via `parentId` under their stamped `call_exec`
ancestor — so Chunk 4's exec split should compose with lazy without extra
stamping, exactly as §3.2 step 3 anticipates.

**North star: on track.** Singleflight (Chunk 2) + lazy (Chunk 3) are both
faithful via the **unchanged** replay. "User work first-class" is still ahead
(Chunk 4's exec split) — correctly, and the empirical low jaccard reflects that
plus the scope issue below, not unfaithfulness.

### KEY HOLISTIC ITEM — the empirical `jaccard=0.23` scope mismatch (MEDIUM)

**The argument is well-reasoned and the supporting evidence is strong, but it is
an *interpretation* that must be nailed down before §6.4 — and the disposition
(defer to §6.4 with scope matching) is right.** My assessment:

- *Why it's probably scope, not unfaithfulness:* the **deterministic** oracle
  (matched/filtered scopes) is `jaccard=1.00` with the deferred work's `call_exec`
  self-time matching native **exactly** — proving the emit mechanism is faithful.
  And the implementer reports the empirically **overlapping** classes converge at
  **drift 0.00**. If OTel were mis-attributing shared work, those shared classes
  would drift; they don't. So the non-overlap is most plausibly the
  native-GLOBAL (`_DAGGER_WCPROF=1`) vs OTel-single-CLIENT-trace span-population
  difference (native sees core-schema/other-session work the one client trace
  doesn't; OTel sees client-infra native doesn't hook).
- *Why it's not yet airtight:* "the non-overlapping classes are scope artifacts"
  is asserted by interpretation, not measured. The risk that a *real* OTel
  mis-attribution hides among the OTel-only classes is **low** (drift-0.00 on
  shared classes argues against it) but not zero from the committed artifacts.
- *Cheap corroboration I'd suggest now* (not a blocker): re-run the empirical
  oracle with native **`--profile`** (session-scoped) instead of global — if the
  jaccard jumps up, the scope hypothesis is confirmed; if it stays low, there's
  hidden unfaithfulness to chase. Perfect matching is genuinely hard (native
  `--profile` records the session **and** its nested clients, which span multiple
  OTel client traces), which is *why* §6.4 is the proper home — so the deferral is
  justified, but the `--profile` run is a quick confidence boost.
- *The bar for §6.4:* the standing gate **must** use principled scope matching
  (aligned trace/session boundary), not loose class-filtering — otherwise the
  global-vs-client jaccard is structurally low forever and the gate is
  meaningless. The implementer flags exactly this. Right disposition; I'd just
  insist §6.4 resolve it rigorously and would do the `--profile` corroboration in
  the meantime.

Severity MEDIUM not because a bug is likely, but because an **unresolved
measurement-scope mismatch makes the strongest gate (§6.2 oracle) non-actionable
on real workloads** until §6.4 fixes it — and that's the gate the whole effort
leans on.

### Noise (LOW)
- Producer-case lazy op name is `"resume lazy evaluation"` when `Field == ""`
  (`otelprof_lazy.go:155`) — still self ~0/filtered, cosmetic.
- The stamping processor adds a `context.Value` lookup to every span's `OnStart`;
  cheap relative to the export that already runs. Acceptable.

---

## lostcancel (re-confirmed)

Still the pre-existing `context.WithCancelCause` (`cache.go:3691`, shifted from
Chunk 2's `:3674` by Chunk 3's additions above it; `cancel` is stashed in
`oc.cancel`/`shared.lazyEvalCancel` and called on cleanup — a pattern vet can't
follow). Not introduced or worsened by Chunk 3. ✔

---

## Bottom line

Chunk 3 faithfully implements the hardest break: Invariant T correct, the
stamping discriminator correct and tested on the real processor, both existence
cases right, the UI re-point untouched, all three divergences justified, and my
LOW-1 fixed. **It composes cleanly with the Chunk 1 loader + hardened gate — the
loader needed no change, the gate stays green with the §2.5 cycle risk closed,
and no double-count is proven on real exported spans — and the Chunks-1–3
trajectory is firmly on the north star.** **Proceed to Chunk 4.** The one item to
carry: resolve the empirical-oracle scope mismatch before/at §6.4 (a `--profile`
scope-matched corroboration run now would make the faithfulness case airtight).
Suggested design-doc touch-up for the lead: reword §9 provider coverage to "one
prepended processor per client provider covers all its export processors" (the
multi-provider framing was imprecise; the behavior is verified).
