# wcprof × OTel — Chunk 3 review (by the Chunk 2 implementer)

**Reviewer context:** I implemented Chunk 2 (the `call_exec` singleflight emit,
Invariant T for `call_exec`, the `emitOTelWait` gate-observable-on-missing-target
pattern, the always-on posture). Chunk 3 **directly extends** those conventions,
so this review focuses hardest where my Chunk 2 knowledge is sharpest: does the
lazy emit *compose with and faithfully extend* my Chunk 2 patterns? Reviewed
Chunk 3 `a460633b0f` against Chunk 2 `b0e7cd9931` (`git diff b0e7cd9931..a460633b0f`)
and the full Chunks 1+2+3 (`git diff b442cd2533..a460633b0f`).

**Verified by running** (throwaway worktree at `a460633b0f`): `go build` of
`dagql/... engine/server/... engine/wcprof/... cmd/wcprof-oracle` **OK**; all six
dagql emit-path tests **PASS** (the 3 Chunk-2 + 3 new lazy, incl. the real
stamping processor and the all-exports coverage test); `go test ./engine/wcprof/wcotel/`
**ok** (chunk3 fixture + full regression); full `go test ./dagql/` **ok**; `go vet
./dagql/` shows **only** the pre-existing `lostcancel` (shifted to `cache.go:3691/3718`
by Chunk 3's insertions — same `WithCancelCause` lease-error path, not introduced here).

## Verdict

**Sound to build Chunk 4 on, and the Chunks-1+2+3 trajectory is sound.** Chunk 3
is a faithful, correct implementation of §3.2/§3.0.2. Invariant T is satisfied for
the lazy op exactly as I did for `call_exec` (stash under `lazyMu` before publishing
`lazyEvalWaitCh`); the `wcprof.parent` stamping discriminator is correct and
**verified on real exported spans**; and — the holistic headline — **it composes
cleanly with my Chunk 2 emit and Chunk 1's loader**: the shared `emitOTelWait`
preserves the gate-observable behavior I added, and Chunk 3 needed **zero
loader/gate changes** (the `wcprof.parent ?? parentId` rule Chunk 1 built handles
the re-homing). No blocking issues. Findings are LOW/NOISE robustness + one
holistic empirical-methodology note (the jaccard=0.23 framing is incomplete but the
disposition is right, and the faithfulness proof does not rest on it).

---

## (A) Chunk 3 in isolation — §3.2/§3.0.2 faithfulness + correctness

### The stamping discriminator (§3.0.2) — correct, and verified on real spans

`wcprofLazyParentProcessor.OnStart` (`otelprof_lazy.go:102-114`) stamps
`wcprof.parent = lazyOpSpanID` iff `s.Parent().SpanID() == ov.producerSpanID`. I
traced the mechanism and confirm it is sound:

- The override is set on `callbackCtx` (`otelprof_lazy.go:157`). Direct work spans
  created under `callbackCtx` have parent = the producer's SpanContext (because
  `resumedCallbackSpan.SpanContext()` returns `originalSpanCtx`, `cache.go:2797-2805`),
  so `s.Parent().SpanID() == producerSpanID` ⇒ stamped.
- A *descendant* inherits the override **value** (context values are inherited) but
  its recorded parent is the intermediate work span, not the producer ⇒
  `s.Parent().SpanID() != producerSpanID` ⇒ falls through unstamped. This is the
  §3.0.2 guardrail and it is exactly right — without the parent-id test, the
  inherited override value would over-stamp the whole subtree and flatten it.

**The claim "the SDK passes the parent ctx to OnStart" is verified empirically:**
`TestLazyEmitProducerRepointStampsDirectChildrenOnly` (`otelprof_lazy_test.go:58-160`)
drives the *real* processor — direct child `w1` stamped, descendant `w2` **not**
stamped — and **passes**. If the SDK did not hand OnStart the override-carrying
Start context, `w1` would not be stamped and the test would fail. ✔

**Nested-lazy composition is sound by construction** (the empirical run claims "0
over-stamped descendants" on a nested chain): each lazy eval calls
`withLazyParentOverride` on *its own* `callbackCtx` subtree, replacing the key for
that subtree only, with an exact span-id discriminator (different evals have
different producer span ids), so an inner eval's work cannot be mis-stamped to an
outer lazy op and vice-versa. **LOW gap:** this is validated only by the empirical
engine run, not a committed unit test — the committed `w2` is a plain descendant,
not a *nested lazy eval* with its own override. Worth a forward unit test (a
re-pointed work span that itself triggers a second `beginOTelLazyOp`), but the
reasoning + empirical evidence make it sound for now.

### Invariant T (§3.0.1) for the lazy op — correct, mirrors my Chunk 2 pattern

`shared.lazyEvalSpanCtx` (`cache.go:1518-1524`) is the OTel analog of
`lazyEvalProfOpID`. In the leader path it is set (`cache.go:2997-2999`,
`shared.lazyEvalSpanCtx = lazySpan.SpanContext()`) **before** `shared.lazyEvalWaitCh
= waitCh` (`cache.go:3001`), both under `lazyMu` (unlock at `:3004`). A joiner reads
it under its own `lazyMu` acquire (`cache.go:2951`) after observing
`lazyEvalWaitCh != nil`. This is **exactly** the ordering I used for `call_exec`
under `callsMu`, and it guarantees a joiner always has a valid target in the uniform
model. ✔ The lazy op span (`beginOTelLazyOp`) is minted under `lazyMu` (`cache.go:2995`)
— same lock-held-during-`Tracer.Start` shape as my `call_exec`; same (accepted)
minor perf note applies.

### Two existence cases (§3.2 step 1) — both faithful, both tested

`beginOTelLazyOp` (`otelprof_lazy.go:135-177`):
- **Producer context captured:** the `lazy` op **is** the `resume <field>` span,
  minted here (earlier, under the lock) with the same name/links/passthrough; the
  `resumedCallbackSpan` re-point is untouched (`cache.go:3007-3010` adopts
  `lazyCallbackCtx`; the goroutine no longer creates a resume span — the old block
  is removed), so dagui renders unchanged. Override set for the stamping processor.
  ✔ `TestLazyEmitProducerRepointStampsDirectChildrenOnly` asserts UI parent stays
  the producer, causal re-home on the direct child, lazy op under the consumer.
- **No producer context:** a new hidden `ui.passthrough` lazy op under the
  consumer, named `profCallClass(resultCall)` (so its class **matches native**),
  work nests by ordinary `parentId`, no override. ✔ `TestLazyEmitNoProducerNestsUnderHiddenLazyOp`.

The `lazyIsResume` flag correctly gates the `DagBlockedAttr` UI failure-attribution
to the producer case only (`cache.go:3019-3027`) — the hidden op is not a resume
span, so no blocked-attr, correct. The `lazyResumeLinks` (cause-purpose failure
cascade) are preserved (`otelprof_lazy.go:160`). So the UI failure behavior is
unchanged.

### Robustness / correctness / perf / simplicity

No correctness bug. Concurrency mirrors my Chunk 2 analysis (write-once-before-
publish, immutable `SpanContext`, span created on one goroutine ended on another —
fine). The §2.5 cycle risk is closed: the work's causal parent is the lazy op (not
the already-ended producer), with the consumer→lazy wait — no `producer→work`
causal edge, no cycle (`chunk3_test.go` asserts `CycleWarnings==0`). Simplicity is
good: one processor + one mint function, slotted beside the native hooks.

**NOISE — per-span cost of the stamping processor.** Registered on the client
provider, its `OnStart` runs for **every** span engine-wide and does
`parent.Value(lazyParentOverrideKey{})` — for the common (non-lazy) span this walks
the context chain to the root without finding the key, i.e. O(depth). It is the
designed §3.0.2 mechanism and the per-level cost is a pointer compare (sub-µs at
realistic depths), so acceptable — but it is a new always-on per-span cost worth
keeping under empirical watch as span volume grows.

**NOISE — joiner-branch comment slightly overclaims.** `cache.go:2950-2953` says
`lazyEvalSpanCtx` is "set whenever this branch is reachable." It is set only when
the *leader's* `evalCtx` was recording (`cache.go:2995`); `lazyEvalWaitCh` is
published unconditionally, so a leader-untraced/joiner-traced trace reaches this
branch with an invalid target. The behavior is still correct (the next line's
`emitOTelWait` is gate-observable on the invalid target), and the claim holds in the
uniform model — but the comment would be more precise as "set whenever the leader
was recording; the mixed-recording case is handled gate-observably by emitOTelWait."

---

## (B) Holistic — Chunks 1+2+3 compose; Chunk 3 faithfully extends Chunk 2

This is where my Chunk 2 authorship is most useful; I checked each composition
point concretely:

- **Shared `emitOTelWait` preserves my gate-observable behavior.** The rename
  `emitOTelCallWait → emitOTelWait` (`otelprof_hooks.go:95`) keeps the body
  byte-identical (recording-check first, then emit even on invalid target); only
  the doc generalizes to "work owner" (`oc.execSpanCtx` for call_exec,
  `shared.lazyEvalSpanCtx` for lazy). The lazy **joiner** wait reuses it
  (`cache.go:3024`) with `lazyOpSpanCtx`, so a recording joiner on an untraced
  leader's eval gets the same gate-observable targetless link the §6.1 gate catches
  — the exact behavior I added in the Chunk 2 convergence pass, now correctly
  inherited by lazy. `TestEmitWaitGateObservableOnMissingTarget` still passes. ✔
- **Invariant-T pattern faithfully extended** (above): lazy op stashed under
  `lazyMu` before `lazyEvalWaitCh`, identical shape to `call_exec` under `callsMu`. ✔
- **Always-on posture carried through:** `beginOTelLazyOp` is gated on
  `otelProfActive(evalCtx)` (`cache.go:2993`), not `wcprof.Enabled` — same gate I
  used, same Cloud-trace rationale. ✔
- **Leader/joiner wait shape mirrors my executor/joiner waits:** joiner wait is the
  load-bearing edge (lazy op is in the leader's subtree, `cache.go:2954-3001`);
  leader wait (`cache.go:3071-3079`) is redundant-but-harmless (the lazy op nests
  under the leader, so the implicit join already serializes; the wait+child overlap
  is subtracted as a union, `graph.go` `SelfSegments`) — exactly my Chunk 2
  executor-wait reasoning. ✔
- **Zero Chunk-1 loader/gate changes** (verified: `git diff --name-only` shows no
  `wcotel/loader.go` or `gate.go`). The work re-homes via the Chunk 1
  `wcprof.parent ?? parentId` rule — the same clean composition that retro-validated
  the loader at Chunk 2. ✔
- **Provider coverage (my Chunk 2 review's HOLISTIC-2/D, now closed).** I flagged
  in the Chunk 2 review that the cap (and the future stamping processor) must be on
  every client export path. Chunk 3 lands exactly that: the processor is registered
  **first** on the single per-client provider (`session.go:696-705`), and
  `TestWcprofLazyParentProcessorStampsAllExports` (`otelprof_lazy_test.go:230-291`)
  asserts the stamp reaches both the own-DB and parent exports (and every exported
  copy, so the live-start snapshot carries it too). ✔ The D item is discharged.

**North star — on track.** Lazy + singleflight are now both faithful: the
deterministic cross-source oracle (matched scopes) converges at **jaccard=1.00,
drift=0.00** with `CycleWarnings==0` (`chunk3_test.go`). The replay is untouched.
User-work-first-class remains correctly Chunk 4 (the exec split).

### KEY HOLISTIC ITEM — the empirical `jaccard=0.23`: is the scope-mismatch story airtight?

I cannot independently re-run the empirical engine oracle (same limitation every
reviewer has: it needs an augmented engine-dev + a same-run native dump), but I hit
the *analogous* divergence in Chunk 2, so I can judge the family.

**My assessment: the scope-mismatch explanation is VALID but INCOMPLETE — and,
crucially, the faithfulness proof does NOT rest on the ranking jaccard, so the
mismatch cannot mask unfaithfulness.**

1. **It is the same family as my Chunk 2 `jaccard=0`, plus an avoidable confound.**
   In Chunk 2 I diagnosed two distinct effects: (a) a *scope* asymmetry —
   engine-**global** native vs **client/session** OTel (my run-1: native 21272
   events vs OTel ~50) — and (b) the workload's *bottleneck living in not-yet-built
   chunks*. The Chunk 3 implementer attributes 0.23 to (a) ("native-GLOBAL vs
   OTel-CLIENT; native sees core-schema, OTel sees client-infra"). That is real and
   the **drift 0.00 on the overlapping classes** is genuinely reassuring. But the
   framing under-weights (b): a real lazy workload's bottleneck is usually a
   lazily-materialized **container exec**, whose engine/user split is **Chunk 4** and
   whose pull/I-O is the **§3.5 seam** — so native ranks `exec.processRun`/`exec.run`
   while OTel (no split yet) ranks the un-split `withExec`/`call_exec`, diverging for
   the *same* reason Chunk 2 did. So 0.23 conflates a scope artifact with the
   expected unbuilt-chunk divergence.

2. **It cannot mask unfaithfulness, because lazy re-homing is verified
   independently of the ranking.** The masking worry ("a lazy bug hides in a
   non-overlapping class") is defused by three checks that do **not** use the
   empirical jaccard: the structural emit-path test
   (`TestLazyEmitProducerRepointStampsDirectChildrenOnly`: re-home correct, 0
   over-stamped, producer not double-charged, gate green on real spans); the
   **deterministic** oracle (matched scopes, a lazy-deferred *non-exec* `call_exec`
   — `Directory.withNewFile` — as the bottleneck, `jaccard=1.00`); and the empirical
   `cycles=0 / fallback-anchors=0 / unresolved-targets=0`. The lazy fix's
   correctness is proven there, structurally, not by the noisy ranking number.

3. **Disposition is right; one concrete improvement.** Deferring clean empirical
   convergence to the §6.4 standing gate with scope-matched sources / class
   filtering is correct (it is exactly the "mixed oracle stays informational until
   Chunk 5" guardrail). The concrete improvement I'd push, from Chunk 2 experience:
   **the scope confound is removable now** — re-run the empirical with `--profile`
   (session-scoped native), which gave me comparable scopes in Chunk 2 (run-2:
   native 79 vs OTel 62) and isolated the residual to purely the expected
   unbuilt-chunk classes. *Caveat I'd want the implementer to address explicitly:*
   lazy eval can span producer/consumer **across sessions**, so `--profile` (one
   session) might miss a cross-session producer's native work — if that is why
   engine-global was chosen, say so, because then the scope asymmetry is *inherent*
   to cross-session lazy and the §6.4 class-filtering disposition is the only fix.

**Bottom line on 0.23:** not new, not unfaithfulness — same family as Chunk 2 with
an added (removable) scope confound; the faithfulness is carried by the structural
+ deterministic proofs, and the flag-to-§6.4 disposition is the right call.

---

## The three divergences — I agree with all three

1. **Lazy-op class label (producer case `"resume <field>"` vs native
   `profCallClass`) — JUSTIFIED, verified.** The lazy op's self-time is ~0 (the
   deferred work is its analyzer-child via `wcprof.parent`, so it is subtracted out),
   so it never ranks in `RunWhatIfs`. `chunk3_test.go` filters it with `minSelf=5ms`
   and the oracle still reaches `jaccard=1.00`. Note the no-producer case **does**
   use `profCallClass` (matches native), so only the producer case diverges, and
   only on a never-ranking op. Benign. *(NOISE sub-note: the native fixture labels
   the lazy op `"Directory.directory"` (`chunk3_test.go:189`) where `profCallClass`
   for a `Query.directory` producer would be `"Query.directory"`; immaterial because
   the op is filtered, but the illustrative label could match `profCallClass` for
   clarity.)*
2. **Leader wait link (redundant-but-harmless) — JUSTIFIED, verified.** Mirrors my
   Chunk 2 executor wait exactly: the lazy op nests under the leader, so the implicit
   join already serializes it; the explicit edge is idempotent (`max`), and the
   child∪wait interval is union-subtracted so self-time is unchanged. Emitted for
   oracle parity with native's leader wait. ✔
3. **Provider-coverage wording (§9 "multi-provider" → one provider/client with
   multiple export *processors*) — JUSTIFIED, verified.** There is one
   `sdktrace.NewTracerProvider` per client; the parent exports are additional
   `LiveSpanProcessor`s on the *same* provider, so one prepended stamping processor
   covers all exports. The §9 prose ("every per-client tracer provider") is loosely
   worded; the implementation reality is accurate and behaviorally tested. The
   design owner should reconcile the §9 wording to "the one per-client provider's
   processor chain."

---

## Issues summary (severity)

- **No blocking issues.**
- **LOW (forward):** no committed unit test for *nested* lazy override composition
  (a re-pointed work span that itself triggers `beginOTelLazyOp`); validated only
  empirically. Add one with Chunk 4-era tests.
- **LOW (holistic methodology):** the empirical `jaccard=0.23` framing is incomplete
  (scope confound + expected unbuilt-chunk divergence conflated); re-run with
  `--profile` to disentangle, or document why cross-session lazy forces
  engine-global. Faithfulness is unaffected (carried by structural + deterministic
  proofs); §6.4 disposition is correct.
- **NOISE / verified-fine:** stamping-processor per-span context walk (designed,
  bounded — watch item); joiner-branch comment overclaim; native fixture lazy-op
  class label; `lostcancel` (pre-existing).
- **Doc reconciles for the lead (all flagged by the implementer):** (1) §3.2 — note
  the producer-case lazy op keeps the `"resume <field>"` UI name (class divergence,
  benign/filtered); (2) §3.1/§3.2 — the leader/executor redundant-but-harmless wait
  is intentional for native parity; (3) §9 — reword provider-coverage to "the one
  per-client provider's processor chain."

## Bottom line

Chunk 3 faithfully implements the lazy re-point fix: the stamping discriminator is
correct and verified on real spans, Invariant T holds (mirroring my Chunk 2
`call_exec` pattern), both existence cases are handled, the §2.5 cycle risk is
closed, and the three divergences are justified. **It composes cleanly with my
Chunk 2 emit** — the shared `emitOTelWait` preserves the gate-observable behavior,
the Invariant-T/always-on/leader-joiner patterns are faithfully extended, and it
needed **zero loader/gate changes** — and it **closes my Chunk 2 review's
provider-coverage (D) item** with a real all-exports test. The cumulative
trajectory is firmly on the north star (lazy + singleflight both faithful;
user-work-first-class correctly deferred to Chunk 4). The empirical `jaccard=0.23`
is the same expected-divergence family I hit in Chunk 2 (with a removable scope
confound), not unfaithfulness — and the faithfulness proof rightly rests on the
structural emit-path tests + the matched-scope deterministic oracle, not on that
number. **Proceed to Chunk 4.**
