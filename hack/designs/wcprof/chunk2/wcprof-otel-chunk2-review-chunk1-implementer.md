# wcprof × OTel — Chunk 2 review (by the Chunk 1 implementer)

**Reviewer context:** I built Chunk 1 (the loader, the hardened §6.1 gate, the
vocabulary). This review is two-part: **(A)** Chunk 2 in isolation (the
singleflight central fix, design §3.1), and **(B)** holistic — do Chunks 1+2
compose and is the cumulative trajectory still on the north star. Reviewed
commit `f127e5662b` against Chunk 1 `71b69f1f16` (`git diff 71b69f1f16..f127e5662b`)
and base `b442cd2533`.

**I verified by running, not just reading:** checked out `f127e5662b` in a
throwaway worktree — `go test ./engine/wcprof/wcotel/` is **green (28 tests:** my
22 Chunk 1 tests regression-free **+ 6 new Chunk 2 tests)**, `go build ./dagql/`
**OK**, and `go vet ./dagql/` shows **only** the pre-existing `lostcancel`.

## Verdict

**Sound to build Chunk 3 on, and the Chunks-1+2 trajectory is sound.** Chunk 2
is a faithful, correct, simple implementation of §3.1. Invariant T is correctly
satisfied (I traced the lock scope myself), the wire format matches §3.0, and —
the holistic headline — **the emit composes cleanly with my Chunk 1 foundation:
it satisfies every wait-loss invariant the hardened gate enforces, and it needed
zero loader changes.** The IR-contract design held under first real-emit contact.
No blocking issues. The findings below are LOW robustness notes, one holistic
test-coverage gap (inherent to the plan's otlpdump-first approach), and one
product sign-off item (always-on) the implementer correctly flagged.

---

## (A) Chunk 2 in isolation

### Faithfulness — verified, not assumed

**Invariant T (§3.0.1) — correctly satisfied.** I traced the full `callsMu`
scope in `getOrInitCallInner` (`dagql/cache.go:3648`–`3742`): the call_exec span
is minted (`:3684`, `beginOTelCallExec` on the detached `callCtx`), then `oc` is
constructed and `oc.execSpanCtx = execSpan.SpanContext()` is stashed (`:3713`),
then `c.ongoingCalls[callConcKeys] = oc` publishes (`:3719`) — **all under one
contiguous `callsMu` hold, unlock only at `:3740`** (after the goroutine spawn).
The joiner branch (`:3658`–`3664`) reads `oc` under its own `callsMu` acquire,
so it always observes a fully-populated `oc.execSpanCtx`. This mirrors native's
`oc.profOpID` ordering exactly. The read in `c.wait` is unsynchronized but safe:
`execSpanCtx` is written-once-before-publish and immutable after, and `SpanContext`
is an immutable value, so ending the span on the resolver goroutine cannot race
the readers. **Joiners always have a valid wait target — the gate's
`UnresolvedWaitTargets` invariant cannot fire on a faithful trace.** ✔

**Wire format (§3.0) — exact.** `emitOTelCallWait` (`otelprof_hooks.go:90`)
attaches the edge to the *waiter's* span via `span.AddLink` (never fanned onto
the target), with `link.purpose=wait`, `wcprof.wait.reason`, and start/end as
`strconv.FormatInt` **decimal strings** of absolute Unix nanos captured around
the blocking `select` (`cache.go:3909`,`:3917`). This is precisely what my
loader parses and what the gate's malformed-timing invariant expects. ✔

**The three fixes land:**
- **Break #3 (emitter≠executor) — fixed structurally.** call_exec is minted on
  the executor's `callCtx` and threaded into `sharedWorkCtx`
  (`cache.go:3684`,`:3695`), so the resolver's sub-calls nest under it
  *regardless* of AroundFunc suppression. `TestChunk2EmitterNotExecutor` asserts
  the sub-call parents under call_exec and call_exec under the ancestor. ✔
- **Breaks #1–#2 (joiner attribution) — fixed.** The joiner's only edge to the
  execution is its `singleflight` wait link; `TestChunk2SingleflightOracle`
  asserts joiner self-time ≈ 0 and call_exec ranks #1 in both sources. ✔
- **Native parity:** call_exec uses `profCallClass(req.ResultCall)` as both its
  OTel span name and the native execOp class (`cache.go:3684`,`:3680`), and
  publishResult's name matches native's pubOp class — so the oracle's per-class
  table lines up by construction.

### Correctness / robustness

No correctness bugs. Concurrency is correct (analyzed above). No degenerate
performance: per cache *miss*, two `Tracer.Start` + two ends + one `AddLink` per
blocked caller — linear, bounded to misses, and **cheaper than a caller span**
because `dag.call`/`CallPB().Encode()` is omitted (divergence 1). The
telemetry-off path is allocation-free (`otelProfActive` is one context lookup +
`IsRecording()`).

**LOW-1 — `emitOTelCallWait` silently drops on an invalid target
(`otelprof_hooks.go:91`).** If `oc.execSpanCtx` is invalid, no link is emitted —
and a *never-emitted* wait is invisible to the gate (no dropped-count, no
unresolved-target). This can only happen when the executor's `callCtx` was
non-recording while a joiner's ctx *is* recording — i.e. **non-uniform
telemetry** (e.g. native's per-session `--profile` scoping). The OTel source's
real model is telemetry-on-globally, where this can't arise, so it's an edge,
not a live bug. But note the asymmetry vs native: native records the joiner's
wait against `profOpID=0` (a targetless fixed delay the gate *can* see as
unresolved), whereas OTel drops it entirely. Worth a one-line code comment, and
a counter if mixed-telemetry flows ever become real. *Not a Chunk 2 blocker.*

**NOISE — call_exec error path** (`cache.go:3699`): on `withOperationLease`
failure the span ends via `execSpan.End()` (no error status), vs native's
`execOp.End(OutcomeError)`. Rare path, no joiners exist yet (oc unpublished), no
graph effect. Fine.

### The two divergences — I agree with both

**Divergence 1 (omit `dag.call` on call_exec/publishResult) — justified.** These
are passthrough internal spans; the loader/oracle read span name + `dag.digest`
only, `dag.call` is unused, and emitting it would double the expensive
`CallPB().Encode()` on every cache miss. The §3.1 attribute list was
illustrative; this is a sound justified-discovery, not a miss. Fold a one-line
note into §3.1 noting `dag.call` is intentionally omitted from these spans.

**Divergence 2 (always-on, gated on telemetry-active not a profiling flag) —
correct, and it *must* be.** I agree strongly: the north star is "analyze *any*
Cloud trace after the fact," which is impossible if the profiling spans weren't
already emitted during the slow run — you can't retroactively enable them. A
flag would defeat the entire use case. So always-on follows necessarily. **The
sign-off is genuinely Erik's to give** because it is not free: every
telemetry-enabled run, forever, gets +2 passthrough spans per cache *miss* + one
link per blocked caller, all flowing through SQLite/OTLP/Cloud ingest. It's
bounded (hits emit nothing; misses are the expensive ops you want anyway) and
the spans are cheap (no `dag.call`), so the multiplier is modest — but on a
10k-miss CI build that's ~20k extra spans/run of ingest cost. My read:
**acceptable and correct-for-the-goal; the only honest alternative (sampling/
opt-out) would break the "any trace is analyzable" property.** Recommend Erik
sign off explicitly and the design state the always-on posture + its volume
profile so it's a recorded decision, not an implicit one.

### The mixed-workload `jaccard=0` — sound, not hiding a problem

The reasoning holds, and the *structure* of the validation is what makes it
safe — I checked specifically for the failure mode where "expected drift" masks
a real bug:

- The **gating** oracle is the *singleflight-isolated* one
  (`TestChunk2SingleflightOracle`: `cmp.Agrees(0.99, 0.02)`, call_exec #1) and it
  is **green**. That independently proves the Chunk 2 emit (call_exec +
  singleflight) is faithful.
- The mixed `jaccard=0` is an **empirical, informational** result — it is *not* a
  committed gate (correctly so). The committed suite gates only on
  choke-point-isolating workloads, exactly as impl-plan §4-#2 mandates.
- The mixed top-N is dominated by exec/lazy/leaf-I/O classes that Chunks 3/4/§3.5
  don't emit yet, so OTel attributes that time elsewhere (exec→withExec/stdout
  self-time, I/O→caller) while native attributes it to the real classes →
  disjoint top-N → jaccard=0. The drift localizing precisely to those unbuilt
  classes is the corroboration.

The danger of "jaccard=0 is expected" masking a regression is real *in general*,
but it's defused here because the isolating gate is green and independent. **The
one guardrail I'd insist on:** the mixed oracle must stay *informational/
drift-localization* and never be silently promoted to a passing gate — at
Chunk 5 it should converge and become the §6.4 standing gate, but only then.

---

## (B) Holistic — Chunks 1+2 compose, trajectory on-target

This is where my Chunk 1 knowledge is most useful, so I checked composition
concretely rather than abstractly:

**The gate ↔ emit compose — the emit satisfies every invariant I built.** I
confirmed each:
- *Invariant T → resolvable targets:* verified the lock ordering;
  `TestChunk2SingleflightOracle` asserts `UnresolvedWaitTargets == 0`. ✔
- *Valid timings:* decimal-string abs-nanos around the select;
  `MalformedWaitTimings == 0` asserted. ✔
- *Under the 16384 cap:* `TestChunk2CapStressFanIn` (5000 joiners) asserts
  `TotalDroppedLinks == 0`, then injects a drop and asserts the gate *fails* —
  exercising my `WaitEdges>0 && TotalDroppedLinks>0` rule end-to-end. ✔

**My `hasCallExecChild` precedence fires correctly on real emit.** This is the
satisfying composition point: in Chunk 1 I built (and could only *synthetically*
test) the rule that a `call_exec` child reclassifies a `withExec` call span from
the deliberately-wrong `exec` fallback to `call`. Chunk 2's call_exec span is
parented under the caller's withExec span, so that precedence now fires on real
data — the withExec span becomes `call` (matching native, which models withExec
as call + call_exec). **The loader genuinely needed no change** (verified: the
Chunk 2 diff touches zero Chunk 1 files) — the strongest evidence the IR-contract
chunking is working as designed.

**North star: on track.** The replay is untouched (the oracle drives
`RunWhatIfs` unchanged). Chunk 2 delivers its slice — singleflight/joiner/
cache-miss attribution is now faithful (oracle jaccard=1.00 isolated). "User
work first-class" is *not* yet here, and correctly so: it's Chunk 4's exec split
(`work_type=user`), which the mixed `jaccard=0` precisely reflects. No drift from
the goal; the remaining gap is the explicitly-scoped Chunks 3/4/§3.5.

### Holistic concerns to carry forward (not Chunk 2 defects)

**HOLISTIC-1 (LOW→MEDIUM) — the emit path has no committed automated test.** The
deterministic suite is thorough, but it validates the loader/gate/oracle on
fixtures that *hand-mirror* the emit (`chunk2_test.go` `callExecAttrs`/`otWait`
builders), not the emit code itself. The real `otelprof_hooks.go` + `cache.go`
integration is validated only by my/their code review + the **empirical engine
oracle**, which needs a live augmented engine and isn't in CI. This is the same
limitation as Chunk 1's captured fixture and is *inherent to the plan's
otlpdump-first approach* — not a miss. But the fixture↔emit correspondence is
**manually maintained**, and the gap compounds as Chunks 3/4 add more emit. Two
cheap mitigations: (a) a unit test driving the three hooks against an in-memory
SDK tracer to machine-check that the emitted span names/attrs/links match what
the fixtures assume; (b) **re-run the empirical oracle every chunk** (it's the
only thing exercising the real emit), with Chunk 5's §6.4 gate as the eventual CI
backstop. Flagging so the cumulative test story is deliberate.

**HOLISTIC-2 (LOW) — `LinkCountLimit=16384` provider coverage.** The cap is set
only on the per-client tracer providers I added in `session.go:684`. Wait links
land (via `AddLink`) on whatever span is current in the waiter's ctx; if any
waiter span were ever created by a *different* provider (default 128 cap), its
links could evict at 128. The empirical cap-stress (5001 waits, 0 dropped on the
real engine) **confirms the validated session path has the cap**, so this is
theoretical — but it's the same family as the §9 "stamping-processor coverage"
residual (Chunk 3 will need the processor on every provider too). Worth tracking
as a single "all client providers configured" assertion when Chunk 3 lands the
processor.

**NOISE — duplicated `publishResultSpanName` const.** Defined in both
`dagql/otelprof_hooks.go` (the emit) and `wcotel/chunk2_test.go` (the fixture),
different packages so no conflict, but a manual-correspondence point — same root
as HOLISTIC-1.

---

## lostcancel verification (requested)

**Confirmed pre-existing.** `go vet ./dagql/` at `f127e5662b` reports only
`cache.go:3674`/`:3701` (`the cancel function is not used on all paths`). That is
the `callCtx, cancel := context.WithCancelCause(...)` at base `b442cd2533:3668`
(`cancel` is stashed in `oc.cancel` and called on cleanup — a store-and-call-later
pattern vet's analysis can't follow). Chunk 2's additions above it shifted it
`3668→3674`; the Chunk 2 diff touches no cancel handling. **Not introduced by
Chunk 2.** ✔

---

## Bottom line

Chunk 2 faithfully implements the singleflight central fix: Invariant T correct,
wire format exact, all three breaks closed, both divergences justified, clean and
performant, thoroughly tested at the model level, and the empirical oracle's
identity-level parity is structurally consistent. **It composes cleanly with the
Chunk 1 loader + hardened gate — the emit satisfies the wait-loss invariants and
needed zero loader changes — and the cumulative trajectory is firmly on the north
star.** **Proceed to Chunk 3.** Carry into later chunks: the always-on sign-off
(Erik), the emit-path CI gap (HOLISTIC-1, re-run the empirical oracle each chunk),
the cap/processor provider-coverage assertion (HOLISTIC-2, due with Chunk 3), and
the rule that the mixed-workload oracle stays informational until Chunk 5.
Suggested design-doc touch-ups for the lead to reconcile: note `dag.call`'s
intentional omission from the call_exec/publishResult spans (§3.1), and record the
always-on posture + its volume profile as an explicit decision (§3.1/§4.1).
