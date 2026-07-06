# wcprof × OTel — item 3 landed; the `publishResult` emit-gap finding — INVESTIGATE + fundamentally fix

## THE GOVERNING PRINCIPLE (unchanged; it decides this) + the GOAL stated as harmony

The analysis/model is a RATIONAL function of the data and NEVER compensates for it (no inference, no
fallbacks, no approximations). The data must be FAITHFUL. Debug them separately; when a rational
model reports something odd, fix the EMIT, not the analysis.

**Erik's framing of the goal — what we're driving toward:** the MODEL/ANALYSIS must be rational,
AND the DATA must be honest (true + full information), AND **the two in HARMONY** — the analysis
does not compensate for the data, and the data publishes true and complete causal information. Both
sides held to their own standard; neither papers over the other. That harmony is the target.

## What the implementer did — item 3, committed `98ee73047c` (the rational root model)

Removed the root CHAINING from `Run()` (it inferred a dependency from temporal order — the forbidden
inference); pre-anchors every root at its own recorded start (independent, no incoming edge), so a
cross-root wait resolves to an already-anchored target and the `par<0` "fallback" never fires (it was
the EXACT root anchor, mislabeled because the chaining gave a competing start). Reclassified the
`spawnTo` in-flight-inversion / prefix-never-reached / `joinUpTo`-orphan corners as UNFAITHFUL-DATA
errors (flagged, gate-failing — fix the emit, never silently approximate). Counters are now
faithfulness signals: `CycleWarnings + FallbackAnchors + SimStartConflicts == 0` by construction for
faithful data. Tests: `TestRootChaining→TestRootsIndependent` (case b: scaling A does NOT shift the
independent B), `TestCrossRootAnchor→TestCrossRootDedup` (case a: makespan 200, saving crosses the
root boundary through the recorded wait, counters 0). Full suite + vet green (lead verified the code).

## TWO empirical findings from the implementer (the important part)

**1. "Baseline is bit-identical" was WRONG — including the lead and 5/6 reviewers — and the
correction is a WIN.** We claimed at factor 1 the chained start equals the recorded start. False:
`chainSimEnd` is the *simulated* finish (not the recorded end) — equal only with zero replay drift,
and there IS drift (that's what the −0.1%/−2.4% baseline numbers ARE). So the chaining propagated
each root's drift into later roots' starts. Removing it changes the baseline — TOWARD faithfulness:
both baselines now match the recorded makespan EXACTLY (native 5.86→5.87 = recorded; OTel
5.96→**6.11** = recorded). **The −2.4% OTel drift — open the whole arc, hand-waved as "accepted
compression / 2nd-source bucketing" — was 100% the chaining artifact, now +0.0%.** The principle
paid off: remove the compensation, the baseline becomes faithful, a long-standing mystery dissolves.

**2. THE `publishResult` EMIT GAP (the finding to investigate).** Removing the chaining surfaced
that **330 of the 331 OTel "roots" are `dagql.publishResult` internal-kind spans** — only ONE root
is the actual command. The implementer's read: they are internal operations surfaced as independent
roots because the capture is **MISSING their parent edge**, and the chaining was papering over it
(chaining 331 fake roots together produced a plausible-enough makespan). Consequences it flagged
honestly:
- The **baseline is faithful** (6.11 = recorded — at baseline everything sits at its recorded time
  regardless of root structure), but the OTel **root STRUCTURE is largely unfaithful**, so OTel
  *multi-root what-ifs* sit on bad data until the gap is closed.
- The **current gate does NOT catch this** (there is no internal-kind-root signal) — it passes
  today, but it shouldn't.

## The lead's synthesis

I verified item 3 in code (the `Run()` rewrite, the orphan reclassification, `TestRootsIndependent`,
suite green). I OWN the bit-identical error — I told Erik I'd "verified the algebra," and I made the
same mistake the reviewers did (assumed simulated = recorded at factor 1, which the drift
contradicts). Both findings are sound, and #2 is exactly the principle working: removing the
compensation makes the data gap visible instead of hidden.

## THE QUESTIONS FOR YOU — this is an INVESTIGATION + framing-check + fundamental-fix-design

1. **Is the framing CORRECT, or are we missing something?** Investigate against the actual emit +
   loader + trace: are the 330 `publishResult` roots really internal ops **missing their parent
   edge** (an EMIT gap)? Or is it a LOADER issue (the parent edge IS recorded but the loader treats
   them as roots)? Or are `publishResult` spans parentless BY DESIGN and the real problem is
   elsewhere? Or is the count/diagnosis itself off? Don't take the implementer's read on faith —
   verify it.
2. **If the framing is correct, what is the FUNDAMENTAL fix** that holds BOTH the model (rational,
   no compensation) AND the data (honest, true + full) in HARMONY? We do NOT want to compensate in
   the analysis; we want the data to publish the true parent structure. What does the emit need to
   record, and how?
3. **The gate:** should there be an "internal-kind root" (or broader unfaithful-root) faithfulness
   signal, and should it hard-fail until the emit is fixed? How does it fit the "counters = 0 by
   construction for faithful data" property?

**The lead's SUGGESTION (just a suggestion — evaluate it critically, adopt/modify/reject):** stamp
the `publishResult` parent via the existing `wcprof.parent` mechanism (the same one already used to
re-home lazy ops in Chunk 3) so each is anchored through its real parent instead of surfacing as a
fake root — plus a new internal-kind-root gate signal. Is that the right fundamental fix, or is
there a cleaner one?

## Erik's cross-session clarification (fold in — important)

Erik is NOT saying "we can't fix anything anymore." The thing he is **deferring** is the genuinely
hard CROSS-SESSION case: separate top-level command sessions serialized by the **test framework's
semaphores / parallelism limits** (an orchestrator-level causal link the engine never observes) —
that is a later, inter-session problem, not for now. **The `publishResult` finding is DIFFERENT** —
it is intra-trace (internal ops missing their parent in ONE session), and it MAY be fixable NOW,
depending on what it really is. If the capture is genuinely missing the parent edge, investigate
what we can do to record it. So: cross-SESSION orchestrator links = deferred; the `publishResult`
intra-trace parent edge = investigate now, and fix now if the data fix is tractable.

## Deliverable

Write to `hack/designs/wcprof-otel-publishresult-<yourname>.md`: (a) your INVESTIGATION — is the
framing correct (publishResult = missing parent edge / emit gap), or are we missing something? cite
the emit/loader/trace evidence; (b) the FUNDAMENTAL fix that gets model + data into harmony (the
emit change, concretely); (c) the gate signal; (d) alignment / pushback. Investigation + analysis
only — NO code, NO commits.
