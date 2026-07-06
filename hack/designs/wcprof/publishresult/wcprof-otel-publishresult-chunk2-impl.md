# wcprof × OTel — the `publishResult` "emit-gap" finding: INVESTIGATION (by the Chunk 2 implementer)

**Bottom line up front.** The framing is **wrong in its mechanism**, and the lead's suggested fix
(stamp `publishResult`'s parent via `wcprof.parent`) is the **wrong fix** — redundant, ineffective,
and a violation of the governing principle. The parent **edge is not missing**: the engine emits
`parentId` for **100%** of `publishResult` spans (the *single* truly-parentless root in each capture
is the actual entry command). What is missing is the parent **span** — the `call_exec` is **absent
from the local capture** — and I prove by direct measurement that the absence is **random tail
batch-drop in the `OTEL_EXPORTER_OTLP_TRACES_LIVE` drain**, not a structural emit gap and not the
deferred cross-session case. The **emit is faithful**; the gap is in local capture *completeness*.
The gate **already hard-fails** these traces today. Net: nothing to fix in the emit; the harmony the
principle asks for is already present at the source.

---

## 1. What the data actually says (emit / loader / trace evidence)

I reproduced the finding on a module-load workload (`dagger functions` against the dev module),
captured two ways through my Chunk-2 augmented engine (`dagger-engine.dev`):

| capture | how | lines | spans | trace ids | span length | loader roots | loader cycles |
|---|---|---|---|---|---|---|---|
| `/tmp/pubres-mod.jsonl` | otlpdump, `sleep 2` then kill (old method) | 21297 | 10885 | **1** | 5733 ms | **371** | 8 |
| `/tmp/pubres-clean.jsonl` | otlpdump, **poll-until-stable then kill** (clean drain) | 21170 | 10801 | **1** | 2019 ms | **225** | 14 |

Both reproduce the headline shape: **all-but-one root is `dagql.publishResult`** (370/371 and
224/225). One real root: the `dagger functions` command. So the *count* the implementer/lead reported
(330/331) is real and reproducible — but the *diagnosis* is not what it looks like.

### 1a. The parent EDGE is emitted for 100% of `publishResult` — it is NOT a missing edge

Computing roots in raw `jq`, independently of the loader, mirroring the loader's rule
(`causalParent = attrs["wcprof.parent"] // parentId`; root ⇔ that span id is absent from the captured
set):

```
root_parent_status (truncated capture):  370 EDGE_present_parent_span_ABSENT,  1 EMPTY_no_edge
root_parent_status (clean capture):       224 EDGE_present_parent_span_ABSENT,  1 EMPTY_no_edge
```

The lone `EMPTY_no_edge` root in each is the `dagger functions` command — correctly parentless. **Every
single `publishResult` carries a non-empty `parentId`.** There is no missing edge. The roots arise
because the span the edge *points at* (the `call_exec`) is absent from the capture.

This is also what the loader does — confirmed in `loader.go`: `parentID =
opIDBySpan[causalParentSpanID(s)]`, and `causalParentSpanID(s) = wcprof.parent ?? parentId`. The edge
is read faithfully; the target span simply isn't in `opIDBySpan` because it isn't in the trace.

### 1b. The absent span is the `call_exec`, and it is same-trace by construction (not cross-session)

`beginOTelCallExec` (`otelprof_hooks.go:53`) names the `call_exec` span by **call class** (e.g.
`Query.sourceMap`), not a fixed name — so the `call_exec`s hide among the class-named counts, keyed by
`wcprof.op.kind=call_exec`. The wiring in `cache.go`:

- `3692`: `callCtx, execSpan = beginOTelCallExec(callCtx, …)` — `call_exec` started on `callCtx`.
- `3717`: `oc.execSpanCtx = execSpan.SpanContext()`.
- `3976`: `pubSpan = beginOTelPublishResult(ctx.WithoutCancel(oc.sharedWorkCtx))`, **guarded by
  `oc.execSpanCtx.IsValid()`** — `sharedWorkCtx` descends from the same `callCtx` that carries the
  `call_exec`.

So `publishResult` is created **1:1 with `call_exec`, on the same ctx lineage, in the same
trace/session** — confirmed empirically: **single trace id** in both captures. There is **no code path
that emits a `publishResult` without a `call_exec`**, and the parent is **never** in a different
session. This rules out the deferred cross-session case for this finding entirely.

### 1c. The absence is random tail batch-drop — proven, not asserted

I was wrong before by asserting unverified premises (the finish-invariance "proof"). Here every claim
is **direct measurement**:

1. **100% tail-concentrated, both captures.** Bucketing the absent-parent `publishResult`s by their
   start position in the trace timeline: **all 370 (and all 224) fall in decile 9** — the final ~10%.
   A structural emit gap would spread across the timeline (ops of the affected class run throughout);
   a clean property is dead-flat at the tail.

2. **Count scales with trace length and is unstable across runs.** 370 over a 5733 ms span; 224 over a
   2019 ms span; the lead's 330 is a third point. A stable structural property does not vary 224↔370
   on the same workload — a *capture artifact* does.

3. **Bidirectional loss — impossible for any emit rule.** In the clean capture I measured **both**
   directions of the 1:1 relationship dropped, both 100% in decile 9:
   - **224** `publishResult`s missing their `call_exec` **parent** (orphaned children);
   - **344** `call_exec`s missing their `publishResult` **child** (orphaned parents);
   - and the aggregate counts disagree (`call_exec`=4779 vs `publishResult`=4659), which a 1:1 emit
     cannot produce.
   A structural emit bug that failed to record parents would **never also** drop the children. Only
   random batch-drop loses both ends of the same relationship.

4. **Out-of-order loss — not even a clean time-cut.** Of the 224 orphaned children, **135 have `endNs`
   set** (fully *ended*). An ended `publishResult` has a `call_exec` parent that ended *even earlier*
   (the parent closes before the child starts, by construction). Capturing the later-ending child
   while dropping the earlier-ending parent is impossible under a "cut at time T" truncation — it is
   **out-of-order batch loss** in the live-export drain.

**Conclusion.** The `call_exec` spans are dropped randomly from the **tail of the engine→CLI→otlpdump
live-export stream** when the CLI process exits before the stream fully drains (the clean capture was
"stable" the instant `dagger functions` returned — the CLI flushes only what it has *received*, and
the engine's final telemetry batches are still in flight). This is a property of my **local capture
method**, not of the emit. The "true" complete-trace root count is **1** (the entry command), by the
1:1-same-trace construction in §1b.

---

## 2. The fundamental fix: there is **no emit fix**, because the emit is already faithful

The principle says: model rational + data honest + **in harmony**; fix the *emit* when the data is
unfaithful; never compensate. Applying it here:

- **The data is honest at the emit.** `parentId` is recorded for 100% of `publishResult`s, pointing at
  the real `call_exec`, same trace. The engine publishes true and complete causal structure.
- **The gap is in local capture completeness**, downstream of the emit — a drain race in *my otlpdump
  method*, almost certainly the same in the implementer's (same testbed). It is not in the engine's
  emit, and it is not intrinsic to Cloud (the session force-flushes on close; Cloud ingests the
  complete set). The right response to "my measurement instrument truncated the tail" is **capture
  completely** and **let the gate reject anything that wasn't**, not to change a faithful emit.

### Why the lead's `wcprof.parent` stamping is the wrong fix (adopt → **reject**)

1. **Redundant.** `wcprof.parent` would carry the *same value* the loader already reads from
   `parentId` (`causalParent = wcprof.parent ?? parentId`). The edge is not missing; duplicating it
   changes nothing.
2. **Ineffective.** The failure is a *dropped target span*, not a missing edge. Stamping the
   `call_exec`'s id into `wcprof.parent` still leaves the loader with no `call_exec` span to resolve —
   the `publishResult` stays a root. It cannot fix a drop by re-emitting the pointer.
3. **Principle violation.** Its only conceivable effect would be to *re-home* the orphan onto some
   surviving ancestor to suppress the root — i.e., compensate in the emit/loader for a
   capture-completeness gap. That is exactly the anti-pattern: papering over missing data instead of
   refusing it. (It also differs fundamentally from Chunk 3's lazy `wcprof.parent`, which records a
   *real, otherwise-unrecorded* causal edge for an in-trace span — there is no missing edge here to
   record.)

If anything is ever worth adding to the emit, it is **not** for this finding.

---

## 3. The gate: it already catches this; an internal-root signal is a defensible *direct* diagnostic

**The gate does not pass on these traces today — it hard-fails.** Full gate block on the clean
capture:

```
structural gate: FAIL
  cycles=14   ...   wait-loss: unresolved-targets=2040  malformed-timing=0
  ! 14 wait/join cycle(s) ...
  ! 2040 non-lock wait(s) with an unresolved target span — Invariant T regression or a
    truncated/lost target; the join degrades to a fixed delay
```

This is the existing machinery working exactly as designed. **Every `call_exec` is the target of at
least one wait edge** (the executor's own `call_exec` wait, §3.1), so a dropped `call_exec` trips
`UnresolvedWaitTargets` — and the violation text *already names the cause*: "a **truncated/lost
target**." 2040 unresolved targets + 14 cycles is the incomplete capture being **loudly rejected**.
So the lead's "the current gate does not catch this — it passes today" is **not what I observe**; the
trace is already refused.

**`RootCount` is reported but is not itself a violation** (`gate.go`: it is printed at line 153, never
appended to `violations`). So the *specific* "internal/`call_exec` root" signal is not gated. Should
it be? **As a direct, defensible diagnostic — yes; as the "fix" for an emit gap — no.**

- A faithful **complete** trace has **exactly one root: the entry command**. Any root whose
  `wcprof.op.kind ∈ {call_exec, internal, lazy, exec, …}` is structurally a *child* of something and
  can **never** be a legitimate root — so "non-entry-kind root count == 0" holds **by construction for
  faithful (complete) data**, fitting the gate's "counters = 0" property cleanly.
- It closes a real **corner** the unresolved-target invariant can miss: if a drop takes both a
  `call_exec` *and* its waiter span, no unresolved-target fires, yet the surviving `publishResult`
  leaf is orphaned. The internal-root signal catches that.
- Crucially it must be **named for what it is** — an **incomplete-capture / orphaned-internal-root**
  detector — not "missing parent edge." And it must key on the **sub-operation `op.kind`**, never on
  raw root *count*: concurrent top-level sessions (Erik's cross-session case) legitimately produce
  multiple **entry-command** roots, which this signal must not false-positive.

So: keep the existing hard-fail (it already rejects these traces); **add** the orphaned-internal-root
signal as a sharper, corner-closing diagnostic; understand it as a completeness check, not an emit
trigger.

---

## 4. The −2.4% / baseline assessment

The implementer is **right on the mechanics and right to remove the chaining**, and I add one
refinement so we don't over-claim.

- **Correct:** removing the root chaining makes the factor-1 baseline equal the recorded makespan
  exactly (each root anchored at its own recorded start; no propagated drift). On *this* data the
  −2.4% was indeed the chaining artifact, now +0.0%. And removing chaining was right **regardless** —
  it was the forbidden temporal inference.
- **Refinement (don't declare the data faithful because the baseline is exact):** the chaining only
  ever had something to distort because there were **224–370 fake roots to chain**, and those fake
  roots exist **only because of the incomplete-capture tail-drop** (§1c). On a *complete* trace there
  is **one** root, the baseline is trivially exact with or without chaining, and there is no −2.4% to
  explain. So the chaining was **compensating for the incomplete capture**; removing it (correct) did
  not make the data faithful — it **exposed** the underlying completeness problem (the now-visible
  roots). That is the principle working: remove the compensation, the data gap becomes visible.
- The implementer already flagged this honestly ("baseline faithful, but root **structure** largely
  unfaithful → multi-root what-ifs sit on bad data"). **I affirm that flag and sharpen the why:** the
  unfaithfulness is **incomplete local capture**, caught by the gate (2040 unresolved targets), and
  it means OTel **multi-root what-ifs must run only on a gate-passing (complete) trace** — which these
  local captures are not. The exact baseline is necessary, not sufficient.

---

## 5. Alignment / pushback

**Aligned with the governing principle.** The model is already rational (once chaining was removed it
correctly *surfaces* the roots and the gate *rejects* the trace — no compensation); the data is
already honest **at the emit** (`parentId` recorded 1:1, same trace); the only gap is in my
local-capture *instrument*. Harmony is achievable here with **no emit change and no analysis change** —
capture completely, and let the gate refuse what isn't. That is the principle in its cleanest form.

**Aligned** with the implementer's root-structure flag (§4) and with the lead's instinct that a
gate signal belongs here (§3) — reinterpreted as a completeness detector.

**Pushback:**
1. On the framing "missing parent **edge** / **emit** gap": the edge is present for 100% of
   `publishResult`s; the lone empty-parent root is the real command. It is a missing parent **span**
   from an **incomplete capture** (random tail batch-drop, proven bidirectional + out-of-order +
   decile-concentrated), not an emit gap.
2. On the `wcprof.parent` stamping fix: reject — redundant, ineffective against a dropped target, and
   a principle violation (§2).
3. On "the gate passes today, it shouldn't": it **fails** today (2040 unresolved-targets + 14 cycles),
   already naming "truncated/lost target." Add the internal-root signal as a sharper diagnostic, not
   as a remedy for a non-existent emit gap.

**On Erik's cross-session clarification:** correct to separate it — and this finding is **not** that
case. `publishResult`'s parent is same-session by construction (§1b), so it is neither the deferred
cross-session problem **nor** an intra-trace emit gap. "Fixable now" applies only in the sense that
the instrument is fixable (capture completely; trust the gate); there is nothing to fix **now in the
emit**.

---

### Appendix — commands (all read-only; investigation only, no code, no commits)

- Root computation (raw jq, loader-faithful rule) over `/tmp/pubres-{mod,clean}.jsonl`.
- Decile / bidirectional / endNs analysis (jq) — §1c numbers.
- `go run ./cmd/wcprof-otel-analyze /tmp/pubres-clean.jsonl` — gate block (§3).
- Clean-drain capture: `/tmp/clean-capture.sh` (otlpdump → `dagger functions` → poll-until-stable →
  stop). Engine `dagger-engine.dev` (Chunk-2 augmented build).
