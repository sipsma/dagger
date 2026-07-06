# wcprof × OTel — Chunk 3 review (by the design author)

**Scope:** (A) Chunk 3 in isolation — lazy / deferred-eval faithfulness + the
`wcprof.parent` stamping processor (design §3.2/§3.0.2/§3.0.1), and (B) the
holistic pass across Chunks 1+2+3. Reviewed commit `a460633b0f` (on Chunk 2
`b0e7cd9931`, base `b442cd2533`) against the contract of record
`hack/designs/wcprof-otel-design.md`.

## Verdict

**Chunk 3 is sound to build Chunk 4 on, and the Chunks-1–3 trajectory is sound.**
This is the subtlest break and it is handled faithfully: the UI re-point is
untouched (dagui unchanged), the causal re-home is an explicit emit (`wcprof.parent`)
with a correctly-discriminating processor, Invariant T holds, and — for the third
time — the loader and gate needed **zero change**. Verified locally (temp worktree
at `a460633b0f`):

- `go build ./dagql/... ./engine/wcprof/... ./engine/server/...`: **OK**.
- `go test ./engine/wcprof/wcotel/...`: **ok, 0.041s**; lazy emit-path tests
  `go test ./dagql/`: **ok, 0.006s**. No quadratic.
- **Invariant T**: `shared.lazyEvalSpanCtx` is minted via `beginOTelLazyOp` and
  stashed **under `lazyMu`, before** `shared.lazyEvalWaitCh = waitCh`
  (`cache.go:2982-3001`) — joiners read it under the same lock
  (`cache.go:2947-2952`).
- **Loader/gate untouched** (`git diff … -- wcotel/loader.go wcotel/gate.go` is
  empty): Chunk 3 only adds `dagql/*` emit + `session.go` registration + tests.
- **`lostcancel` still pre-existing** (`cache.go:3691/3718` at Chunk 3 — same
  warning as base, line-shifted by the added lazy code); no new vet findings.

Three divergences, all justified (two want a §3.2 reconcile, one a §9 reconcile).
The one genuinely significant finding is **(B)**: the empirical low jaccard exposes
a real gap in *my own §6.2/§6.4 oracle methodology* (scope-matching). It is a
validation-methodology gap, not unfaithfulness or product drift — detailed below.

---

## (A) Chunk 3 in isolation — §3.2/§3.0.2 faithfulness

**The stamping processor (`dagql/otelprof_lazy.go`) — correct, and machine-tested
on real spans.** `wcprofLazyParentProcessor.OnStart` stamps `wcprof.parent` iff the
ctx carries the override **and** `s.Parent().SpanID() == producerSpanID`
(`otelprof_lazy.go:97-103`). That parent-id test is the §3.0.2 discriminator: only
the callback's *direct* re-pointed children have the producer as their recorded
parent; descendants have their real parent and fall through unstamped, so the work
subtree's internal structure survives. This is not just code-reviewed — the
emit-path test drives the *real* processor against an in-memory SDK and asserts the
direct child carries `wcprof.parent` while the descendant does **not**
(`otelprof_lazy_test.go:131-138`), and that the loader then re-homes the direct
work under the lazy op while the descendant nests under the work by `parentId`
(`:175-184`). The cross-package worry from the Chunk 1/2 reviews — "does the SDK
pass the parent ctx to OnStart, and does `Parent()` expose it" — is now settled
empirically.

**`beginOTelLazyOp` — both existence cases faithful (`otelprof_lazy.go:130-177`):**
- *Producer context:* the `lazy` op **is** the existing `resume <field>` span,
  minted earlier (under the lock); `resumedCallbackSpan` is reproduced byte-for-byte
  (`sc: originalSpanCtx`), so the deferred work keeps `parentId = producer` and
  dagui renders unchanged. The callback ctx gets the override pointing work's causal
  parent at the lazy op. Verified end-to-end: the direct work span's `Parent()` is
  the producer (UI), its `wcprof.parent` is the lazy op (causal)
  (`otelprof_lazy_test.go:120-138`).
- *No producer context:* a new hidden `ui.passthrough` lazy op under the consumer,
  named by `profCallClass`, work nests under it by ordinary `parentId`, no override
  (`otelprof_lazy_test.go:187-238`). dagui elides the passthrough node, so the
  visible tree is unchanged (one extra hidden span).

**Invariant T + the waits (`cache.go` `evaluateOne`):** the lazy op is minted under
`lazyMu` before the channel publish (above); the **joiner** wait
(`emitOTelWait(stackCtx, lazyOpSpanCtx, "lazy", …)`, `cache.go:2960-2965`) is the
load-bearing edge; the **leader** wait (`cache.go:3071-3079`) is
redundant-but-harmless and *matches native*, which emits its own leader wait one
line above (`profWait := wcprof.BeginWait(stackCtx, lazyOp.ID(), WaitReasonLazy)`,
`cache.go:3070`). The lazy logic moved out of the goroutine into `beginOTelLazyOp`
(called under the lock) — a clean refactor whose only behavioral delta is the
intended earlier span-start (design §3.2 step 1).

**`emitOTelCallWait → emitOTelWait` rename** is a clean generalization shared by the
call_exec (§3.1) and lazy (§3.2) waits; the targetless-wait observability behavior
(the Chunk 2 §3.0.1 reconcile) is preserved verbatim (`otelprof_hooks.go:96-118`).

**Robustness / perf:**
- No span races: the leader reads `lazySpan.SpanContext()` (immutable) after the
  eval completes; joiners read the copied `SpanContext` value under the lock; the
  goroutine ends the span. The processor is stateless.
- **LOW — `lazyMu` lock-hold extension.** Minting the lazy op under `lazyMu` (for
  Invariant T) now runs the SDK span `Start` — including the processors' `OnStart`
  — inside the lock, where before (the goroutine) it ran lock-free. The cost is the
  stamping `OnStart` (a cheap ctx lookup) plus each `LiveSpanProcessor.OnStart`,
  which `SnapshotSpan`s + enqueues to a *batch* processor (no synchronous I/O). For
  a per-result lock contended only by concurrent consumers of the *same* pending
  result, this is microseconds — acceptable, and the analog of native's
  `wcprof.BeginOp` under the same lock. Worth knowing, not a blocker.
- **LOW — universal per-span `OnStart`.** The stamping processor runs `OnStart` for
  *every* span on *every* traced client, not just lazy work; for non-lazy spans it
  is a single `ctx.Value` miss and returns. Cheap and bounded, but it is now a
  universal hot-path cost (part of the always-on posture, divergence #2 from
  Chunk 2).

**Simplicity:** appropriate. One small processor + one mint function + a tight
`evaluateOne` integration. Not over-abstracted.

---

## (B) Holistic — Chunks 1+2+3

**Composition is clean — third time.** The loader's Chunk 1 rule `causal parent =
wcprof.parent ?? parentId` is exactly what makes the lazy re-home work with **no
loader change**; the `wcprof.parent` vocabulary defined inert in Chunk 1 is now
consumed; `emitOTelWait` generalized from Chunk 2's `emitOTelCallWait`. The gate is
green on real lazy work (`CycleWarnings == 0`) — the §2.5 lazy-cycle risk is
closed, proven on the loaded graph (`chunk3_test.go:113-116`). The chunks are
genuinely additive: each new emit slots into the unchanged offline path.

**North star intact.** The §6.5 fidelity fixture asserts all five properties on the
loaded graph + replay — no double-count (`producer.SelfNS()==10ms`,
`workExec.SelfNS()≥100ms`, `lazyOp.SelfNS()≈0`, `Σself ≤ makespan`,
`chunk3_test.go:138-160`) and that the **consumer's critical path includes the
eval** (scaling the work's real `call_exec` class saves ≫ scaling the lazy class,
`:163-176`). That is the whole point of the lazy fix and it holds.

### The empirical low jaccard (0.23) — sound, and it surfaces a real methodology gap

This is the item to weigh as the design owner, and my read is: **the implementer's
explanation is correct, the result is not unfaithfulness, but it exposes a genuine
under-specification in my §6.2/§6.4 oracle methodology that should be fixed before
the §6.4 standing gate.**

Why the explanation is sound, not hand-waving:
- The two sources cover **structurally different scopes**. The OTel source compiles
  **one Cloud trace** = the *client-session* scope: it includes client-infra spans
  (session start `POST /query`, `connect`, trace export) that the engine-side
  native recorder never sees, and it sees the engine work done *for that session*.
  Native wcprof is an **engine dump**: with `_DAGGER_WCPROF=1` it is engine-global
  (all sessions + core-schema construction), and it has no client-side spans. So a
  raw top-N class comparison is disjoint by construction → low jaccard.
- The clean controls rule out emit unfaithfulness: the **deterministic** oracle
  (matched scopes, synthetic) is **jaccard=1.00 / drift=0.00** for both Chunk 2 and
  Chunk 3 (`chunk2_test.go`, `chunk3_test.go:206-221`), and the empirical
  **overlap** drift is 0.00 — i.e. where both sources see the same resolver work,
  OTel reconstructs native's ranking exactly. Unfaithfulness would show as drift on
  *shared* classes; there is none.

Could the scope mismatch *mask* unfaithfulness in the non-overlapping classes? Not
in a way the controls leave open, **with one honest caveat**: the matched-scope
deterministic oracle proves the emit *mechanics* are faithful, and the 0.00 overlap
drift proves the shared empirical classes agree — but the empirical *disjoint* set
has only been *inspected* (and found to be the expected client-infra-vs-engine-
internal split), not *proven* under a scope-matched run. The clean closing proof is
a **scope-matched empirical oracle**, which is exactly what the §6.4 disposition
should require.

**This is a design gap in my methodology, and the right disposition is to fix the
oracle, not the emit.** §6.2/§6.4 implicitly assumed the two sources share a scope;
they don't. The standing drift gate (§6.4) must compare **scope-matched** sources:
1. run native with **`--profile <session>`** (per-session, README) scoped to the
   *same* session whose Cloud trace is compared — not engine-global
   `_DAGGER_WCPROF`; and
2. **class-filter** to the engine-resolver work both sources instrument
   (`call`/`call_exec`/`lazy`/`exec`/`service`/`session_phase`), excluding the
   irreducibly OTel-only client-infra and any native-only engine-internal-no-span
   work. The oracle harness already exposes `NativeOnly`/`OTelOnly`
   (`oracle.go:108-130`) to make the residual auditable.
Crucially, the low jaccard does **not** signal product drift: the Cloud trace *is*
the client-session scope, which is exactly the scope "why was my CI run slow?"
operates on. The product analyzes the trace it has; the oracle just needs
like-for-like to validate it. I'll fold this scope-matching requirement into
§6.2/§6.4 (justified-discovery).

No other cross-cutting concern is emerging beyond the accumulating always-on
overhead (named above, accepted as divergence #2).

---

## Divergences (all justified)

1. **Lazy-op class label `"resume <field>"` vs native `profCallClass`** — AGREE,
   reconcile §3.2. The lazy op's self-time is **eval-overhead only** (the small head/
   tail gaps around the deferred sub-work — ~2ms in the fixture, `:11→12` + `:128→129`),
   so it never reaches the **top-N bottleneck** ranking the product cares about; the
   oracle's `minSelf` filter (`chunk3_test.go:218`) makes the class-label mismatch
   invisible. *Nuance for the doc:* "self-time ~0 / never ranks" is slightly
   imprecise — it is eval-overhead-small, not literally zero, so a class-label miss
   *would* surface in a full-ranking (non-top-N) comparison; the honest framing is
   "never reaches the top-N." A clean future fix exists if exact full-ranking parity
   is ever wanted: emit `profCallClass` as a separate `wcprof.class` attribute the
   loader prefers over the span name for the lazy op (avoids the forbidden UI-name
   change). Benign for v1.
2. **Leader wait link** — AGREE, reconcile §3.2 step 4. Verified native emits a
   leader lazy wait (`cache.go:3070`), so the OTel leader wait is native parity, not
   an addition; redundant-but-harmless (the lazy op already nests under the leader;
   union-subtraction means no over-credit), exactly mirroring Chunk 2's executor
   wait. §3.2 step 4 named only the joiner wait; add the leader wait.
3. **Provider-coverage wording** — AGREE, reconcile §9. The §9 "parent-client export
   *providers*" framing was wrong: there is **one** tracer provider per client with
   multiple export **processors** (own DB + parents,
   `session.go:684-715`), so a single stamping processor **prepended** covers all
   exports. Behaviorally guarded by `TestWcprofLazyParentProcessorStampsAllExports`
   (`otelprof_lazy_test.go:262-300`), which asserts the stamp lands in both the own
   and parent exporters and descendants stay unstamped in both. A simplification.

---

## Issues summary (severity)

- **REAL / MEDIUM (design methodology):** §6.2/§6.4 oracle needs explicit
  scope-matching (per-session `--profile` native + class-filtering) — surfaced by
  the empirical jaccard=0.23. Fix the oracle methodology, not the emit; run a
  scope-matched empirical oracle as the closing proof (by/at the §6.4 standing gate).
- **REAL / LOW (doc reconciles):** §3.2 lazy-op class label (+ the "never reaches
  top-N" framing); §3.2 step 4 leader wait; §9 single-provider wording.
- **NOISE / verified-fine:** `lostcancel` (pre-existing, line-shifted); `lazyMu`
  lock-hold extension (microseconds, batch-enqueue not sync I/O); universal per-span
  `OnStart` (cheap ctx miss); the lazy op's eval-overhead self-time (sub-top-N).

## Bottom line

Chunk 3 faithfully closes the subtlest break: UI unchanged, causal re-home via an
explicit override with a correctly-discriminating, machine-tested processor,
Invariant T satisfied, gate green, loader untouched a third time. Holistically the
chunks compose and the north star holds. The empirical low jaccard is **not**
unfaithfulness — it is a scope mismatch the matched-scope deterministic oracle
(jaccard=1.00) and 0.00 overlap drift rule out as an emit problem — but it correctly
exposes that my §6.2/§6.4 oracle must scope-match (per-session native + class
filtering) to be a clean validator; that is the right disposition and I'll reconcile
it into the design. **Proceed to Chunk 4.** Fold the four reconciles (§6.2/§6.4
scope-matching; §3.2 class label; §3.2 leader wait; §9 single provider) back into
`wcprof-otel-design.md`.
```
