# wcprof × OTel — first-principles review: items 1&2 + item 3 (by the Chunk 3 implementer)

**Analysis only — no code, no commits.** Reviewing `e8c0dfe498` (items 1&2) against
my worktree's `replay.go`, and the item-3 reframing, under Erik's governing principle.

## (c) Alignment with the governing principle — I AGREE, strongly, and it corrects me

**The principle is right, and it is the through-line of this entire workstream.** The
analysis is a rational function of faithful data; it must not compensate for data gaps
with inference, fallbacks, or approximations; if a rational model reports something odd,
the bug is in the DATA — fix the EMIT. I confirm alignment without reservation.

Two reasons it is clearly correct here:
1. **It retroactively validates Chunks 1–3.** The entire point of `wcprof.parent`, the
   wait edges, Invariant E, and the stamping processor was to make the OTel *data*
   faithful so the *unchanged* `wcanalyze` replay stays rational. We made the data carry
   the causal structure precisely so the analysis would not have to infer it. The
   cross-root **chaining** and the **recorded-offset fallback** are the two places we
   violated our own design — analysis machinery whose only job is to compensate for a
   data gap. They are the anti-pattern, full stop.
2. **It exposes two of my OWN review errors,** both from the same root cause — I
   tolerated analysis-side approximations instead of demanding faithful data:
   - I called the zero-duration `SimStartConflict` "benign" and proposed *excluding*
     zero-dur ops from the counter. That is exactly backwards: the conflict was the
     symptom of a real wrong-anchor bug (item 1), and excluding it would have *hidden a
     wrong answer* (the wait-target propagation makespan of 250 vs 300). The principle
     ("if the model reports something odd, find the real cause; don't suppress the
     signal") would have stopped me. I own this.
   - I recommended `FallbackAnchors` be *report-only*, framing a hard-fail as a
     false-positive on "faithful cross-root." Under the principle that is wrong: the
     cross-root fallback is the analysis doing the forbidden thing, and "faithful data
     analyzed with a forbidden fallback" is not a result to tolerate — it is a result to
     refuse until the analysis is fixed (item 3). The hard-fail is more principled than
     my "report." I own this too.

**Where I'd extend the principle (not push back — sharpen):**
- The implicit join and the join/abandoned wait classification are *model
  interpretations of present data*, not forbidden inference — they read structure the
  data records. **But the wait classification's `joinEpsilonNS` (1ms) tolerance** is a
  small instance of the anti-pattern: the analysis infers "join vs abandoned" from
  *timing* ± a fudge factor. The principled form is an **emit fact** — record the wait's
  resolution (completed-join vs cancelled/abandoned) on the edge — so the analysis reads
  it instead of guessing from `EndNS` proximity. Minor, but it's the same shape.
- The **−2.4% OTel baseline drift** (the model not charging unexplained idle gaps) is,
  under the principle, a **data-faithfulness probe, not a model tolerance**: debug them
  separately. Either the idle is an *unrecorded wait* (an EMIT gap — the engine blocked
  on something it didn't emit a wait edge for) → fix the emit; or it is genuine
  scheduling latency the model correctly does not charge (and the −2.4% is then a true
  property, with savings unaffected). Investigate which; do not let −2.4% sit as an
  accepted "compression."

---

## (a) Items 1 & 2

### Item 1 — zero-duration wait-target wrong answer: CORRECT, scoped, and the right approach

I verified the fix against the diff and it is sound. The bug: `joinUpTo` runs before the
action tie-break, so it anchored an unstarted zero-dur child (`endNS == t == the gate's
end`) at the **pre-gate** clock. I had called this benign — **wrong: I reasoned only
about the leaf case** (`TestZeroDurChildAtJoinWaitEnd`, where the child's finish is
absorbed). When the zero-dur child is itself a **wait target**
(`TestZeroDurWaitTargetPropagation`), the wrong-early finish propagates down a wait chain
→ makespan 250 instead of 300, **with no FallbackAnchor to flag it** — a silent wrong
answer. Fresh Codex was right; my "benign" was a leaf-only blind spot.

The fix is correct and the implementer's approach is *better than what I proposed*:
- `joinUpTo` **defers** a child whose own spawn is still pending at this instant
  (`startNS[c] == t`, the only way an in-range child is unstarted here is a zero-dur
  child) — returns without advancing `pendCur`, so the gate raises the clock and the
  spawn action anchors the child at the gated clock; the next `joinUpTo` joins it. I
  traced the loop: the deferred child is re-examined after its same-instant spawn action
  starts it, so there is no infinite defer (and the genuine-orphan `startNS != t` branch
  is preserved). ✔
- `actionRank` now orders **spawn before self** (gate → spawn → self) so a deferred
  zero-dur child at a self-segment *start* anchors concurrent with the self, not after it
  (`TestZeroDurChildAtSelfStart`). **Properly scoped:** only a zero-duration child can
  share a spawn instant with a self-segment start — a normal child's interval carves the
  self out of that point — so the swap touches nothing else (the lead's "real traces
  bit-unchanged" confirms). I also checked it does not perturb the fixed-wait model (the
  fixed markers are rank 0 / rank 3, unaffected by the spawn/self swap). ✔
- **Crucially, it FIXES the early anchor rather than hiding the conflict** — so it
  *eliminates* the benign zero-dur `SimStartConflict` and makes that signal clean
  (a conflict now implies a fallback), **without** the unsound exclusion I proposed
  (which would have suppressed exactly this class of wrong answer). This is the principle
  in action: don't suppress the odd signal, fix the cause.

### Item 2 — `FallbackAnchors` hard-fail: LEGITIMATE under the principle (not a band-aid in the pejorative sense), with item 3 as the real fix

**Under the principle, hard-failing is correct — and my earlier "report-only" was the
mistake.** The reasoning:
- A fallback anchor is the analysis using the forbidden approximation (recorded-offset
  compensation for an op it could not compute from causal prefixes). A finite what-if
  sweep cannot prove the resulting rankings sound. So a result that *used* a fallback is
  a result we cannot trust → **refuse it**. That is exactly "the analysis must not
  compensate," enforced.
- My prior "false-positive on faithful cross-root" framing is wrong under the principle:
  the cross-root case is *not* a legitimate fallback use — it is the analysis doing the
  anti-pattern. Failing it is correct; the fix is to make the analysis *not need* the
  fallback (item 3), not to tolerate it.
- The `MaxFallbackAnchors` opt-out is the right escape hatch: explicit, opt-in,
  best-effort offline analysis of a trace knowingly needing the approximation.

**BUT — it is only the right *interim*; item 3 is the real fix, and the two must not be
confused.** Hard-failing on `FallbackAnchors` while the fallback still *exists for
faithful data* (the cross-root case) means a faithful concurrent-dedup trace fails the
gate today. That is acceptable as a "fail loud until we fix the analysis" interim, but
the END-STATE is item 3: anchor roots from first principles so `FallbackAnchors` is **0
by construction for faithful data**, and any non-zero then means the **DATA** is
unfaithful. The commit message says this ("the cross-root shape … is item-3 territory")
— good; just don't let the hard-fail become a permanent substitute for removing the
fallback. **Land item 2; prioritize item 3.**

---

## (b) The gate/counters' role under the principle — YES, faithfulness signals, not tolerances

This is the reframe, and it is correct. After item 3 (remove chaining + fallback, anchor
roots independently, honor recorded edges):

- **`FallbackAnchors` → a DATA-faithfulness signal, 0 by construction for faithful
  data.** A root anchored at its own recorded start is a *fact* (its independent start),
  not a fallback — so the cross-root case stops incrementing it. The only remaining way
  to hit it is the **in-flight-ancestor** case, which requires a forward reference (a
  wait that completed before a spawn referencing that spawn's descendant) — **impossible
  on faithful data** (the referenced op did not exist when the wait ran). So non-zero ⇒
  the EMIT recorded an a-causal edge ⇒ fix the data.
- **`SimStartConflicts` → 0 by construction.** Item 1 removed the zero-dur conflict;
  removing the chaining removes the chaining-vs-independent-anchor conflict (the
  "false alarm produced by the analysis's own compensation" the handoff names). What
  remains can only be a genuine order-dependence — which a rational model on faithful
  data does not produce, so non-zero is again a faithfulness/soundness signal.
- **`CycleWarnings` → already this** (a genuine re-entry ⇒ unfaithful emit or a real
  recorded circular dependency).

So my property restates from **"== 0 ⟺ clean replay (analysis didn't approximate)"** to
**"== 0 ⟺ faithful data under a rational model"** — and a non-zero count is a
**"fix the EMIT"** signal, never a tolerance to threshold. The gate hard-failing on any
non-zero is then exactly right: the gate's job *is* to catch unfaithful data. This is
the principled end-state, and it is strictly cleaner than the "tolerate/threshold"
framing several of us (me included) drifted into.

---

## (b) Item 3 — the rational model, the three cases, and the EMIT fixes

### The rational first-principles model
- **A root has no incoming causal edge ⇒ the data says it is INDEPENDENT.** Anchor it at
  its **own recorded start** — an exact fact, not a fallback. No chaining inference, no
  shifting one root because another moved.
- **A recorded edge (a wait, a nested-client/traceparent parentage) is HONORED.** The
  counterfactual propagates through it: a cross-root wait's target is reached by
  prefix-replaying *its* root's timeline under the factor (so a saving crosses the root
  boundary through the wait, exactly as within a tree).
- **No fallback.** If the replay reaches an op it cannot compute from causal prefixes,
  that is not a thing to approximate — it is either a root (anchor at recorded start, a
  fact) or an a-causal forward reference (unfaithful DATA → fail).

### The three cases — data recorded / logically-correct answer / does the rational model give it

**(a) Concurrent cross-root dedup.** *Data records:* two roots both at recorded start 0
(concurrent), and a cross-root wait `R_A.W → R_B.T` (positive evidence they overlap).
*Correct answer:* both starts fixed; scaling R_B's setup→0 makes R_B reach T's spawn at
0, T runs 0–200, W unblocks at 200 ⇒ **makespan 200**, the saving propagating across the
root boundary through the wait. *Rational model:* anchors both roots at recorded start 0,
honors W, prefix-replays R_B (with the factor) to anchor T ⇒ **200, with FallbackAnchors
= 0 and SimStartConflicts = 0**. ✔ The current chaining (infers R_A→R_B sequential — and
the cross-root wait *proves that wrong*) + the par<0 fallback are the two violations; the
"SimStartConflict" the current analysis reports is its own chaining fighting the correct
independent anchor. **No EMIT fix needed — the data is sufficient.**

**(b) Sequential CLI (R_B after R_A, shell-serialized).** *Data records:* **no causal
edge A→B** — the shell, outside the engine, serialized them. *Logically-correct answer
for that data:* they are independent, so scaling A does **not** shift B. *Rational model:*
gives exactly that (B at its own recorded start; A's saving is local). The chaining model
(which shifts B) is **inference compensating for a missing edge** — and it is the *same*
inference that is provably wrong in case (a). **Remove it.** If the *whole-CLI-session*
view ("A faster ⇒ B starts earlier ⇒ session faster") is wanted, that is **NOT an
analysis fallback** — it is either out of scope (design §10: one Cloud trace = one
engine's view of one session; cross-session/CLI aggregation is a separate multi-trace
product *above* this loader) **or** a DATA question (the serializing edge would have to
be recorded — but the engine cannot emit a *shell* edge it never saw). So: rational model
+ per-trace scope is correct; cross-session serialization is **out of engine scope**, not
a thing to fake with chaining.

**(c) Sub-session (R_B launched by R_A mid-flight).** *Data records:* **the launch IS a
recorded edge** in the normal case — OTel nests the nested-client's spans under the
`withExec` span via traceparent (§2.6), and native records an explicit
`LinkKindNestedClient` (§5). So R_B is **not a pure root**: its work parents through that
edge. *Rational model:* anchor R_B through its recorded parent/launch edge — no ambiguity,
no fallback. ✔ **EMIT fix only if a launch is ever unrecorded:** if some sub-session
genuinely depends on R_A reaching a launch point but the trace records R_B as an
independent root with no edge, the data is insufficient and **the fix is to EMIT the
launch edge** (record R_A→R_B), never an analysis fallback that guesses the dependency.

### Analysis/model code changes (candidly)
1. **`Run()`: remove the chaining.** Anchor every root at its own recorded start; makespan
   = `max(root finishes) − min(root starts)`. (Baseline is unchanged — recorded starts
   already encode the gaps; only the *what-if* stops inferring a cross-root shift.)
2. **`spawnTo` par<0: stop calling it a fallback.** Anchoring a root referenced out of
   order at its recorded start is a fact — `setStart(root, recordedStart)` with **no
   `FallbackAnchors++`**. Then cross-root references propagate correctly through the wait
   (prefix-replay the target's root under the factor), with the counter staying 0.
3. **`spawnTo` in-flight-ancestor: treat as unfaithful data, not a fallback.** If it ever
   fires (an a-causal forward reference), surface it as a faithfulness failure (it pairs
   with a non-zero counter the gate fails on), not a recorded-offset patch.
4. Net effect: the recorded-offset `fallbackAnchor` helper is deleted from the
   faithful-data path; `FallbackAnchors`/`SimStartConflicts` become 0-by-construction
   faithfulness signals; case (a) yields 200; case (b) yields B-independent; case (c)
   yields edge-anchored.

### EMIT (data) fixes for the data-insufficient cases
- **(b) cross-session CLI serialization:** the engine cannot emit a shell-level edge — so
  this is **out of engine scope** (a multi-trace product per §10), not an emit fix and not
  an analysis fallback. State the limitation honestly.
- **(c) unrecorded sub-session launch:** **emit the launch edge** (the nested-client /
  parentage edge) so R_B anchors through it. In the common path this is already recorded;
  the fix only applies if a launch path is found that drops it.
- **(general, minor) wait join/abandoned classification:** consider emitting the wait's
  resolution explicitly rather than inferring it from `EndNS ± joinEpsilonNS`.

---

## Bottom line

- **Items 1 & 2 verdict:** Item 1 (zero-dur) is **correct, scoped, and the right
  approach** — it fixes the early anchor (I was wrong to call the conflict benign — it was
  a silent wrong-makespan when the zero-dur child is a wait target) and cleans
  `SimStartConflicts` *without* the unsound exclusion I'd proposed. Item 2
  (`FallbackAnchors` hard-fail) is **legitimate under the principle** (refuse a result
  that used the forbidden approximation; my prior "report-only" tolerated the
  anti-pattern) — land it, but it is the *interim*; **item 3 is the real fix.**
- **Gate/counters role:** **faithfulness signals on the DATA, 0 by construction for
  faithful data — not approximation-tolerances.** After item 3, `CycleWarnings +
  FallbackAnchors + SimStartConflicts == 0 ⟺ faithful data under a rational model`, and
  any non-zero is a "fix the EMIT" signal the gate rightly hard-fails on.
- **Rational model + three answers:** roots independent at recorded start, recorded edges
  honored, no chaining/fallback ⇒ (a) makespan 200 with counters 0; (b) B independent
  (cross-session serialization out of engine scope); (c) anchor through the recorded
  launch edge.
- **EMIT fixes:** (b) out of engine scope (multi-trace product); (c) emit the launch edge
  if ever unrecorded; (minor) emit the wait resolution instead of timing-inferring it.
- **Principle:** I align fully; it corrected two of my own errors, and it is the right
  permanent governance for this workstream.

**(Carried, separate):** the service.start §3.4 self-erasure re-root is still owed in both
sources.
