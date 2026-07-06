# Chunk 4 — items 1 & 2 review + item 3 from first principles (design author)

Grounded in the patch (`e8c0dfe498`), `replay.go` `Run()`/`spawnTo`/`advance`, and
`graph.go` nested-client reparenting. Analysis only.

## 0. Alignment with the governing principle — endorsed, with one clarification

**I am aligned, strongly.** "The analysis is a rational function of the data and
never compensates for it; the data must be faithful; debug them separately; when a
rational model reports something odd, fix the EMIT" is not just good hygiene — it is
**load-bearing for this entire workstream**. The cross-source oracle (§6.2) only
works if the replay is a *pure function of the data*: native data and OTel data, both
faithful, must land on the same answer through the *same* analysis. Every fallback,
chaining inference, or recorded-offset approximation inside the analysis is a place
the two sources can silently diverge. So the principle is the precondition for "OTel
as a second source" being coherent at all. It is also why my job here is the EMIT:
the burden of causal truth belongs on the data.

**One clarification, not a disagreement:** "no inference" must be read as *honor the
recorded causal structure, infer nothing beyond it.* The recorded structure is three
things — the **nesting edge** (`parentId`/`wcprof.parent`, = synchronous nesting), the
**wait edges** (explicit blocking), and the **recorded intervals/self-segments**. The
replay's implicit join (a parent absorbs children ending by t) is **not** forbidden
inference — it is honoring the nesting edge, *predicated on the emit guaranteeing that
edge is a genuine synchronous nesting* (Invariant E). So the principle actually
**sharpens** the emit's burden: the nesting edge must be faithful, or the analysis
must be given a wait edge instead. This is the same discriminator I've applied at
every choke point (singleflight, lazy, exec). Where I land below — "remove chaining,
remove the fallback, push the missing edges to emit" — is the principle applied
literally.

## 1. Item 1 (zero-dur wait-target) — correct, and it's a *model* fix the principle endorses

This is a genuine **rational-model bug** (the analysis gave a wrong answer for
*faithful* data: makespan 250 vs the correct 300), so fixing it *in the analysis* is
exactly right — the principle says make the model rational, and this does.

Verified both parts of the fix:
- **Defer in `joinUpTo`:** `if s.p.startNS[c] == t { return }` — the only way an
  in-range child is unstarted at `t` is a zero-duration child whose spawn *and* end
  both equal `t`. Deferring lets the max-gate at `t` (rank 0) raise the clock first,
  then the child's own spawn anchors it at the gated clock. Correct: the early
  `return` defers *all* remaining same-`t` children, but they're rejoined at the next
  `joinUpTo` and the join is `max` (order-independent), and any already-started child
  in that tail is unaffected. ✓
- **`actionRank` spawn(1) before self(2):** so a deferred zero-dur child anchors
  *concurrent* with a same-instant self start, not serialized after it. The scoping
  claim holds rigorously: a normal child's non-empty interval is carved out of the
  self by `SelfSegments`, so **only** a zero-dur child can share a spawn instant with
  a self-segment start — the rank swap touches nothing else, which is why real traces
  are bit-unchanged. ✓ Gate-before-spawn (rank 0 < 1) is preserved, so the inclusive
  `waitEnd == spawn ⇒ gated` boundary still holds. ✓
- Tests cover both faces (`TestZeroDurWaitTargetPropagation` → 300;
  `TestZeroDurChildAtSelfStart` → concurrent at 100). ✓

**Bonus that matters for item 2:** this *eliminates* the benign zero-dur
`SimStartConflict`. So `SimStartConflicts > 0` now cleanly implies a recorded-offset
fallback — which retires the only reason I gave last round for keeping it report-only.
(More below.)

## 2. Item 2 (`FallbackAnchors` hard-fail) — under the principle it's a BAND-AID; the real fix is item 3

The code change is correct and I verified it (any fallback now fails;
`MaxFallbackAnchors` is the opt-out; `gate_test.go` updated). But the principle forces
the harder question, and the answer is: **the hard-fail detects the symptom of the
anti-pattern instead of removing it.** Here is the load-bearing observation, which I
only saw by reading `Run()` and `spawnTo` together:

**The `spawnTo(par<0)` "fallback" anchors a true root at its own recorded start — and
for a true root that is EXACT, not an approximation.** A true root has no incoming
causal edge, so *nothing* in any what-if can shift its start; its recorded start is a
fact. The reason the code *calls* it a fallback — and the reason it "disagrees under a
shifting factor" (the `SimStartConflict`) — is **the chaining model in `Run()`**:
`Run()` sets each root's start to `chainSimEnd + idle_gap` (or a displacement),
i.e. **a function of the previous root's *simulated* finish.** That is the forbidden
inference. So the root has *two* candidate starts — the chained one (`Run`) and the
recorded one (`spawnTo`) — and the "fallback/conflict" is the analysis's two pieces of
compensation machinery fighting each other. Remove the chaining and anchor roots at
their recorded start, and the two agree by construction: the `par<0` anchor *is* the
root's real anchor, exact, no conflict, **`FallbackAnchors = 0` by construction** —
precisely the handoff's claim.

So:
- The hard-fail is **acceptable as an interim safety net** (don't ship a result the
  analysis had to compensate for), and it's free today (0 on real traces).
- But it is **not the resolution.** The resolution is item 3: delete the chaining,
  anchor roots independently, and **reclassify the true-root anchor as exact** (stop
  counting it). After that the recorded-offset approximation *does not exist*, the
  hard-fail becomes a **never-fires invariant assertion**, and the genuine remaining
  `spawnTo` corners are no longer "fallbacks" at all (see §4).
- The **`MaxFallbackAnchors` opt-out is itself a small principle violation** — a knob
  to *tolerate* compensation. After item 3 it should be removed, or quarantined as an
  explicit "best-effort, known-unfaithful offline" mode that is never used for a
  trusted ranking.

Net: keep the hard-fail wired (it costs nothing and guards the interim), but the
design must commit to item 3 as the actual fix, not bank the hard-fail as the answer.

## 3. The rational first-principles model for roots and cross-root references

- **A root is an op with no recorded incoming causal edge** (no `parentId`, no
  `nested_client` link, no `wcprof.parent`). The data says it is **independent**:
  anchor it at its **own recorded start**, full stop. That is an exact fact, not a
  fallback.
- **A recorded edge is honored, and only a recorded edge.** A nesting edge →
  synchronous nesting (implicit join, Invariant E). A wait edge → the counterfactual
  propagates through it (the waiter's clock takes the target's simulated finish),
  across root boundaries if the edge crosses them. A spawn → prefix-replay the
  producer *with the factor* up to the spawn (the Chunk-4 mechanism).
- **No chaining. No recorded-offset fallback. No idle-gap inheritance.** Successive
  roots are not assumed dependent; overlapping roots are not assumed displaced.
- **Makespan = max(simulated finish) − min(recorded root start)** — same formula,
  computed over independently-anchored roots.

This is baseline-neutral (at factor 1 the chained start equals the recorded start, so
existing baselines and the −0.1%/−2.4% drifts are unchanged) and changes only
multi-root *what-ifs*, toward correctness.

## 4. The three test cases — what the DATA records, the correct answer, the EMIT burden

### (a) Concurrent cross-root dedup → makespan 200, **data already sufficient, no emit fix**

The cross-root singleflight wait (R_A's `W` waits on R_B's `T`) is **already a
faithful recorded edge** — it is exactly the §3.1/§3.0 wait-link the emit produces,
and the loader builds the `W→T` edge without caring which root each side is under.
Under the rational model: both R_A and R_B are true roots, anchored at their recorded
starts (~0). Scaling R_B's setup→0 prefix-replays R_B from 0 to T's spawn *with the
factor* → T spawns at 0, runs 0→200; the recorded `W→T` wait carries the saving across
the root boundary → `W` unblocks at 200 → **makespan 200**, with `FallbackAnchors=0`
and conflicts 0. **No emit change.** This case is the proof that the *data* is right
and only the *analysis* (chaining/fallback) was wrong.

### (b) Sequential CLI → **independent is the correct in-scope answer; chaining is illegitimate; the edge, if wanted, is harness-level**

The shell serialized `dagger call A && dagger call B` **outside the engine**. B did
not start until A's *process exited*; the engine never observed a causal link, so
there is **no recorded A→B edge** — and there *cannot* be one at engine scope, because
the engine cannot see the shell. Under the principle:
- The rational model treats A and B as **independent** → scaling A does **not** shift
  B. **That is the correct in-scope answer** for the data the engine has.
- The **chaining model is an illegitimate compensation** and must be removed: it
  *infers* a causal edge from temporal succession, which is (i) provably wrong for
  concurrent roots (case a — a cross-root wait proves they overlap, yet chaining would
  still displace them) and (ii) wrong *in mechanism* even for sequential roots (it
  ties B to A's engine-*completion*, when the real coupling is A's *process exit*).
  Being "right" for sequential roots is a coincidence of conflating two different
  instants.
- **If cross-session serialization matters** (it does for "total CI wall-clock"), the
  fix is **a recorded edge at the layer that observed it** — the CI/shell orchestrator
  emitting a wrapping span with A→B as a gating edge (or a wait). At engine/emit scope
  this is **explicitly out of scope**, not a fallback. State it that way: "scaling A
  does not move B because the engine recorded no dependency; to answer the
  cross-session question, emit the dependency from the orchestrator."

### (c) Sub-session (R_B launched by R_A mid-flight) → **already faithful for nested clients via a recorded launch edge; make it an invariant**

I checked the code: this case is **already handled correctly** for the real
mechanism. `graph.go` reads a `nested_client` link (launcher exec → client id) and
**reparents** the nested session's root under the hosting exec (`op.Parent = hostExec;
Reparented = true`), and the reparented root is appended to the exec's `Children`, so
`compileProgram` gives it a **spawn action** at its recorded start. The nesting is
**faithful Invariant-E**: the exec *blocks* on the container, which blocks on the
nested session, so the exec's finish genuinely depends on R_B — synchronous nesting is
the truthful model. Consequence under the rational model: R_B is **not a pure root**;
it is anchored by **prefix-replaying the exec to the launch point with the factor**, so
scaling R_A's pre-launch work *does* shift R_B. ✓ The launch is honored as the
recorded causal edge it is — no fallback, no independence error. **No emit change for
nested clients.**

The general statement this forces (the design's burden): **every in-engine sub-session
launch must emit a causal edge to its launching op** — so the sub-session is never a
*pure* root. Nested clients satisfy this via the `nested_client` link. Any *other*
sub-session shape that currently surfaces a session root with no link to its launcher
is a **faithfulness gap to close in emit** (reuse the `nested_client` link, or the
§3.0.2 `wcprof.parent` override), with the same discriminator as everywhere else:
synchronous nesting iff the launcher blocks; otherwise a **spawn at the launch instant
plus an explicit wait** where it actually joins (so a launcher that continues working
concurrently is not falsely credited with the sub-session's whole duration).

## 5. Analysis/model code changes (to give the rational answers)

1. **Delete the chaining in `Run()`.** Anchor every root at `s.p.startNS[r]`
   (recorded start), finish each independently, `makespan = max(finish) − min(start)`.
   No `chainSimEnd`, no idle-gap inheritance.
2. **Reclassify `spawnTo(par<0)`:** anchor the root at its recorded start **without**
   incrementing `FallbackAnchors` — it is exact. (Cleanest: anchor all roots up front
   in `Run()`; then a cross-root reference always finds the root already anchored, and
   the prefix-replay down to the referenced op is exact.)
3. **The remaining `spawnTo` corners stop being "fallbacks":** the *par-in-flight*
   case is a **genuine cycle** → route it to `CycleWarnings` (not a silent
   recorded-offset); the *prefix-never-reached-the-spawn* case is **malformed data**
   (a child whose claimed parent never spawns it) → a **data-faithfulness gate
   failure**, not an anchor. After this, `FallbackAnchors` as a concept dissolves; keep
   it only as a `== 0` invariant assertion.
4. **Update tests:** `TestCrossRootAnchor` must flip (independent anchoring → no
   fallback, no conflict, the cross-root wait carries the saving); audit
   `TestRunWhatIfsRanking`/any multi-root fixture for chaining assumptions.
5. **Secondary (also the principle):** the pre-existing `joinUpTo` *orphan* branch
   (`setStart(c, clock)` for a child whose spawn is outside the op's reachable
   actions) is itself a quiet compensation. A genuine orphan is a **data
   inconsistency** (a recorded child the recorded parent never spawns); it should be
   *counted/flagged* as a faithfulness signal, not silently anchored. Low frequency,
   but it's the same anti-pattern.

## 5.5. The native-headline concern + how to validate (the real reason item 3 is stuck)

The honest blocker isn't the principle — it's that a prior experiment showed *fully
decoupling* roots **changes native headline rankings** (e.g. `Host.directory` 392 ms →
dropped; baseline 5.86 → 5.87 s) and there is **no ground-truth** multi-root trace to
say the new numbers are right. I want to be precise about what's safe and what's not:

- **The baseline is provably safe.** At factor 1 the chained start *equals* the
  recorded start (`chainSimEnd == chainOrigEnd`, zero shift), so removing chaining
  leaves every baseline makespan **bit-identical**. The 5.86→5.87 wobble is a
  *what-if* artifact, not a baseline regression.
- **The what-if change is the principle working, not a regression — but it needs a
  witness.** A `Host.directory` saving that *only exists because chaining propagated
  it across roots* is, by the principle, an **artifact** (the analysis crediting a
  cross-root coupling the data never recorded). Dropping it is correctness. But
  "correctness by argument" is exactly the confidence I over-claimed on
  finish-invariance, so I won't bank it without a witness.
- **The witness is constructible, cheaply.** The decisive test is a **known-answer
  concurrent-query trace**: two overlapping roots with a recorded cross-root wait
  (case a), where the physically-correct what-if saving is hand-computable (200 ms).
  The rational model must produce it and the chaining model must get it wrong. That is
  a synthetic fixture (like the Chunk-4 battery), **no real capture required** — and it
  pins the one thing the real dumps can't (they have **0 cross-root waits**, per the
  prior finding, so they never exercise the cross-root path at all).
- **Therefore the disagreement on the real native dump is NOT cross-root at all** — it
  is the *sequential-root* (case b) behavior: chaining shifts a later root when an
  earlier class is scaled. Per case (b) that shift is the illegitimate inference, so
  the rational answer is "no shift." The native headline moving is chaining's case-(b)
  artifact disappearing. Validate it by asserting case (b) directly (scaling A leaves
  B's recorded start fixed), not by trusting the old native ranking as ground truth —
  the old ranking *is* the thing under suspicion.

So the path that unsticks item 3: adopt the principle's verdict (independence), land it
behind the synthetic case-(a) and case-(b) fixtures as ground truth, and treat the
native-headline change as the artifact those fixtures prove it is — **don't** gate the
decision on reproducing the pre-existing chained native numbers.

## 6. EMIT fixes (the design's burden under the principle)

- **(a)** None — the cross-root wait edge is already faithful.
- **(b)** None at engine scope — the A→B dependency is unobservable to the engine.
  If the product wants the cross-session answer, it is a **harness/orchestrator-level
  emit** (a wrapping trace recording the serialization), explicitly out of the current
  design's scope. Not a fallback.
- **(c)** None for nested clients (the `nested_client` launch edge is faithful).
  **New emit invariant to add to the design:** *no sub-session launched by an in-engine
  op is a pure root — the launch is always a recorded causal edge* (nested-client link
  or `wcprof.parent`), with synchronous-nesting-vs-spawn+wait chosen by whether the
  launcher blocks. Close any sub-session shape that violates it the same way the other
  choke points were closed.

## 7. Doc reconciles (specify, don't edit)

- **Remove the chaining model from the design** (§ on roots / `Run()`): roots are
  independent, anchored at recorded start; successive-root sequencing is **not**
  inferred. Rewrite the `replay.go:38-45` roots comment accordingly (it still
  describes idle-gap chaining).
- **Remove the recorded-offset fallback** from the design's replay description; state
  `FallbackAnchors`/`SimStartConflicts` are `== 0` **invariants by construction**, not
  tolerated metrics. Drop or quarantine `MaxFallbackAnchors`.
- **§6.1 gate:** `FallbackAnchors > 0` and `SimStartConflicts > 0` become
  faithfulness/invariant **violations** (a genuine cycle is `CycleWarnings`; malformed
  parent/child is its own data violation). The hard-fail stays but as an assertion that
  should never trip on faithful data.
- **New emit invariant (§3.x):** sub-session launches are recorded causal edges (case
  c). Generalize the §3.1↔replay note to: *a wait — join, fixed, or cross-root — gates
  the op's finish but never serializes a concurrently-spawned child or a concurrent
  root.*
- **Cross-session (case b)** documented as **out of engine scope**: the analysis
  reports per-session; cross-session sequencing requires an orchestrator-level edge.
- The principle itself should be written into the design as the **governing invariant**
  (analysis = rational function of faithful data; no compensation), since it now
  decides every open question.

## Summary

- **Items 1 & 2 verdict:** Item 1 is a correct, well-scoped *model* fix (rational-model
  bug → fixed in the analysis, exactly as the principle prescribes) and it cleans up
  the benign `SimStartConflict`. Item 2's hard-fail is **a band-aid under the
  principle**: the `par<0` "fallback" is the *exact* anchor for a true root, mislabeled
  because the **chaining model** gives roots a competing simulated start. Keep the
  hard-fail as a free interim safety net, but the real fix is item 3 (remove chaining
  → the fallback dissolves, `FallbackAnchors = 0` by construction); remove the
  `MaxFallbackAnchors` tolerance knob.
- **Rational model:** roots have no incoming edge → anchored at their recorded start
  (exact); recorded edges (nesting, wait, spawn) are honored and propagate the
  counterfactual; no chaining, no fallback, no idle-gap inheritance.
- **Three cases:** (a) **200**, data already sufficient (cross-root wait), no emit fix;
  (b) **independent — scaling A does not shift B** is the correct in-scope answer,
  chaining is illegitimate and removed, the cross-session edge (if wanted) is
  harness-level and out of engine scope; (c) **already faithful** for nested clients
  via the recorded `nested_client` launch edge + faithful synchronous nesting (R_B is
  not a pure root, it's prefix-replayed through the launch).
- **EMIT fixes (the burden):** none for (a); none at engine scope for (b) — push
  cross-session to the orchestrator or call it out of scope; for (c) add the **emit
  invariant** that every in-engine sub-session launch is a recorded causal edge, and
  close any shape that violates it (reuse `nested_client`/`wcprof.parent`,
  nesting-vs-spawn+wait by whether the launcher blocks). Plus: flag the orphan-child
  anchor as a data signal, not a silent compensation.
- **Principle:** endorsed and load-bearing (it is what makes the cross-source oracle
  coherent); the one clarification is that "honor the recorded structure" includes the
  nesting edge, whose faithfulness is Invariant E — which only deepens the emit's
  burden. No pushback.
```
