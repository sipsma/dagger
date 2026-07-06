# wcprof × OTel — item 3 + the publishResult finding (replay owner)

**Reviewer:** replay owner (I designed the item-3 `Run()` rewrite). Verified
item 3, the chaining algebra, and the publishResult emit path against the code at
`98ee73047c`. No code, no commits.

## (1a) Item 3 — confirmed: it is the rational model I designed ✔

`Run()` at `98ee73047c` is exactly the design: pre-anchor **every** root at its own
recorded start (`for r := range roots { setStart(r, startNS[r]) }`) before finishing
any, then finish each independently, `makespan = max(finish) − min(start)`. No
`chainOrigEnd`/`chainSimEnd`, no shift propagation. The pre-anchor pass is what makes
a cross-root wait resolve to an already-anchored target, so the `par<0` fallback is
gone (it was the exact recorded-root anchor, mislabeled because the chaining gave a
competing start). The handoff says the `spawnTo` in-flight / orphan corners were
reclassified to unfaithful-data errors and the counters are now faithfulness signals
— consistent with my design; I confirmed `Run()` directly and rely on the lead's code
verification for the `spawnTo` reclassification (worth a glance that the three
corners each *gate-fail* rather than silently anchor, per the design).

## (2) Bit-identical adjudication — we were WRONG; I concede

**The implementer is right, and I (and the council, and the lead) were wrong.** The
chaining anchored each later root at `chainSimEnd + gap`, and `chainSimEnd` is the
**simulated** finish of the previous root, *not* its recorded end. At factor 1 those
are equal **only if the replay has zero drift** — and it doesn't (the −0.1% / −2.4%
baselines *are* that drift). So the chaining **accumulated each root's per-root
replay drift into the next root's start**. Removing it doesn't preserve the baseline
— it *corrects* it: each root now anchors at its own recorded start, so the baseline
makespan equals the recorded makespan exactly (native 5.87, OTel 6.11). My "I
verified the algebra / bit-identical" claim assumed `simFinish == recordedEnd`, which
the drift contradicts. The −2.4% OTel "accepted 2nd-source compression" mystery was
**100% the chaining artifact** — it dissolves. This is the principle paying off:
remove the compensation, the baseline becomes faithful, the mystery disappears.

**Is "baseline sim makespan == recorded makespan" the right faithfulness criterion?
Necessary, but NOT sufficient — and the publishResult finding proves it.** At factor
1 every op sits at its own recorded start and runs to ~its recorded finish
**regardless of its parent edges**, so `max(finish) − min(start)` reproduces the
recorded makespan *even when the root/parent structure is wrong*. The 330 fake
publishResult roots are exactly that: a badly-wrong root structure that still sums to
the exact recorded baseline. So the exact-baseline-match validates **timing** (each
op honored at its recorded interval) but says **nothing about structure** (parent
edges). Structure is only exercised by **what-ifs** (a mis-rooted op won't shift with
its true parent) and by **faithfulness signals** (cycles, unresolved waits, and the
new internal-root signal below). **Do not read baseline-match as proof of
faithfulness** — it is one necessary check among several, and it is structure-blind by
construction.

## (1b)/(3) The publishResult finding — verify before fixing; the framing is suspect

**The handoff asks me not to take the read on faith. Taking it at face value, I
can't: the emit code parents publishResult under call_exec, so "330 roots from a
missing parent edge" contradicts the code as written.** What I verified:

- `beginOTelPublishResult(context.WithoutCancel(oc.sharedWorkCtx))` (cache.go:4017)
  starts the span with its parent = the current span in `sharedWorkCtx`.
- `sharedWorkCtx = withOperationLease(withoutOperationLease(callCtx))`, and `callCtx`
  was reassigned by `beginOTelCallExec` to carry `execSpan` (the call_exec span). I
  checked both lease helpers (`operation_lease.go`): they only add/clear *lease*
  context values (`leases.WithLease`, `snapshots.WithoutLazyLease`,
  `provider.WithOperationLease`) — they do **not** touch the trace-span context key.
  So `sharedWorkCtx`'s current span **is** `execSpan`.
- `Tracer(ctx).Start` records the parent from the context's current span **even when
  that span has ended** (a `SpanContext` is immutable and valid post-`End`).

So **the emitted publishResult should carry `parentId = call_exec.spanId`, and my
loader resolves `parentId` to the call_exec op** — i.e. publishResult should *not* be
a root. The 330-root observation is empirically real (the implementer ran it), so a
real mechanism is breaking this — but it is **not obviously "the emit omits the
parent."** Before fixing, one cheap check on the actual otlpdump disambiguates the
three possibilities, and each implies a *different* fix:

1. **publishResult's `parentId` is EMPTY** ⇒ a genuine emit bug (the span context
   wasn't carried — e.g. a path where `sharedWorkCtx` was rebuilt without the span,
   or a join-vs-executor path). *Fix: the emit, by ensuring the parent is set.*
2. **`parentId` is set and points at a call_exec span that is ABSENT from this
   trace** ⇒ not a missing *edge* but a missing *node*: the call_exec is on a
   different trace-id / provider (the nested-client / module-runtime boundary —
   module loading is exactly where this trace came from) or was sampled/dropped.
   *Fix: get the call_exec into the same trace, or re-home publishResult — and note
   `wcprof.parent` would NOT help here (it would point at the same absent span).*
3. **`parentId` is set and the call_exec IS present** ⇒ a loader resolution issue.
   I wrote that resolution (`opIDBySpan[normalizeSpanID(parentId)]`); it's a direct
   map lookup, so this is the least likely — but the check rules it in or out.

**The single check:** `jq` the otlpdump for a `dagql.publishResult` span — is its
`parentId` empty, and if not, is that id present as a `spanId` in the same file? That
one query tells us which of (1)/(2)/(3) we're in. **My strong prior from the code is
(2)** — the nested-client/trace boundary — because the code so clearly sets the
parent, and module loading runs the heavy call_exec work *inside container sessions*.
If it's (2), "emit gap / missing parent edge" is a mis-diagnosis; the real issue is
that the call_exec node lives in a sibling sub-trace.

### The replay impact (this is real, not cosmetic)

- **Baseline: fine.** Everything at recorded times regardless of rooting (per the
  criterion adjudication above).
- **What-ifs: genuinely wrong, and possibly broadly muting.** A mis-rooted
  publishResult is anchored at its *recorded* start and **does not shift with its
  true parent** under a factor. Worse than "one op is off": publishResult runs *late*
  (after call_exec + the caller wait), so a publishResult near the trace's max end,
  pinned as an independent root, **pins the makespan** — and then scaling upstream
  classes can't shrink it, **under-crediting the entire what-if sweep**. That is
  almost certainly why the implementer flags "OTel multi-root what-ifs sit on bad
  data." So this is a correctness bug for the headline (the rankings), not a
  diagnostic nicety — it must be fixed before the OTel what-ifs are trusted.

### The fundamental fix — in harmony (data publishes the true parent)

Per the principle, the fix is **on the data side: the emit must publish
publishResult's true causal parent (call_exec), and the analysis honors it.** Which
mechanism depends on the check:

- **If (1) (parentId empty):** fix the **natural parenting** — ensure the call_exec
  span context is on the context publishResult is started from. This is the cleanest,
  most first-principles fix (the true edge, recorded the normal way).
- **If (2) (call_exec in a sibling trace):** the call_exec *node* is the gap. Either
  ensure the trace boundary keeps publishResult and its call_exec in one trace, or —
  since publishResult is a same-session internal op — it should not be emitted across
  a boundary that orphans it. (This is the intra-trace case Erik says is fixable now;
  the genuinely-deferred thing is cross-*session* orchestrator links, which this is
  not.)

**On the lead's `wcprof.parent` suggestion — evaluate critically:** it is the right
*shape* (an explicit causal-parent override the loader already honors,
`wcprof.parent ?? parentId`, proven for lazy in Chunk 3) **but only the right fix in
one of the cases, and a band-aid in another:**
- It is a **band-aid** if the natural `parentId` *should* already be call_exec and
  merely needs fixing (case 1) — stamping an override for an edge that ought to be the
  ordinary parentId is compensating in the emit for an emit bug, not publishing the
  true structure the clean way. Fix the natural edge instead.
- It **does not work at all** if call_exec is absent from the trace (case 2) — a
  `wcprof.parent` pointing at an absent span is as unresolvable as the `parentId`.
- It *is* the correct mechanism only if there is a genuine reason the **natural**
  parentId cannot carry the true parent (as for the lazy UI re-point, where parentId
  is load-bearingly the *producer*). publishResult has no such UI constraint — its
  parentId is *free* to be call_exec — so there is no lazy-style reason to need an
  override. **Verdict: prefer fixing the natural parent edge; reach for
  `wcprof.parent` only if the check shows a genuine reason the natural edge can't be
  recorded — and never if the call_exec node itself is missing (fix the node first).**

## (1c)/(3) The gate signal — yes, add an unfaithful-root signal

**Add an "internal-kind root" (more precisely: any root whose kind is not a
session/command root) faithfulness signal, and hard-fail on it.** Rationale, and why
it fits the "counters == 0 by construction for faithful data" property: in a faithful
trace **only the session/command span(s) are roots** — every `internal` / `call_exec`
/ `exec` / `lazy` op is, by construction, work *under* some call, so it must have a
resolvable causal parent. A root of one of those kinds is therefore *proof* of a
missing parent edge (or missing node) — exactly the unfaithful-data class item 3
made gate-failing. It is `0` on a faithful trace by construction, and it would have
caught these 330 **today** (the current gate doesn't — a real coverage gap). This is
strictly better than chasing publishResult specifically: it generalizes to *any*
internal op that loses its parent. Make it hard-fail (consistent with the
`FallbackAnchors`/cycle posture), with the usual opt-out only for best-effort offline
analysis of a known-degraded trace.

## (d) Alignment / pushback

**Fully aligned with the principle and the harmony goal**, and the publishResult
finding is the principle *working*: removing the chaining compensation made a
long-hidden data gap visible (instead of papered over by chaining 331 fake roots into
a plausible makespan). One emphasis I'd add, not a pushback: **harmony requires the
verification to be honest in both directions.** Here the analysis is now rational, but
the *diagnosis of the data gap* must itself be verified before we "fix the emit" — the
emit code says it parents publishResult correctly, so fixing a "missing parent edge"
that is actually a missing-*node* / trace-boundary issue would be fixing the wrong
thing. Run the one-line check first; then fix the real gap.

## Summary

- **Item 3 sound?** **Yes** — `Run()` is exactly the rational pre-anchor-all-roots
  design; cross-root resolves with no fallback; counters are faithfulness signals.
- **Bit-identical / baseline-faithfulness:** we were **wrong** (I concede) — chaining
  accumulated per-root *simulated*-finish drift, so removing it corrects the baseline
  to the recorded makespan exactly and the −2.4% was 100% the chaining. And
  **baseline-match is necessary but NOT sufficient for faithfulness** — the 330 fake
  roots prove a wrong structure sums to the right baseline; structure is validated by
  what-ifs + faithfulness signals, not by the baseline total.
- **publishResult framing + replay impact:** the framing is **suspect** — the emit
  code parents publishResult under call_exec, so "missing parent edge" needs the
  one-line otlpdump check to tell (1) empty parentId / (2) parentId→absent call_exec
  (my strong prior: the nested-client/trace boundary) / (3) loader. Replay impact is
  **real**: baseline fine, but mis-rooted late publishResults can pin the makespan and
  mute the entire what-if sweep — a headline-correctness bug, fix before trusting OTel
  what-ifs.
- **Fundamental fix:** publish the **true parent** on the data side — fix the natural
  call_exec parentId (case 1) or the trace-boundary that drops the call_exec node
  (case 2). `wcprof.parent` is the right *shape* but a band-aid for case 1 and
  useless for case 2; prefer the natural edge, reserve the override for a genuine
  no-natural-edge reason.
- **Gate signal:** add a hard-failing **non-command-kind root** (internal / call_exec
  / exec / lazy as a root) faithfulness signal — 0 by construction on faithful data,
  catches this class generally, closes a real current gate gap.
