# wcprof × OTel — items 1 & 2 review + item 3 reframed from FIRST PRINCIPLES

## THE GOVERNING PRINCIPLE (Erik — read first; it has been lost by several of us and must not be again)

This is the most important thing in this document. It governs item 3, every open question, and
every problem on this workstream going forward. Read it, and confirm in your review that you are
aligned with it.

**The analysis/model layer must be RATIONAL and work from FIRST PRINCIPLES. It does NOT compensate
for missing data, ambiguity, or problems in the data. It TRUSTS the data.**

- The analysis's only job: given the recorded data, faithfully report "here is what would happen
  if you adjusted this time" — derivable from first principles, the model, and logic. Nothing more.
- **NO INFERENCE.** The analysis does not guess whether something was sequential or parallel,
  independent or dependent, gating or concurrent. It honors the causal structure the data records,
  and nothing it does not.
- **NO FALLBACKS, NO APPROXIMATIONS** to paper over a data gap. A recorded-offset fallback, a
  chaining heuristic, a "best-effort" anchor — any analysis machinery whose purpose is to
  COMPENSATE for what the data didn't say — is forbidden. That is the anti-pattern.
- **BOTH-AND iteration.** You verify TWO things, separately, each to its own standard: (1) is the
  MODEL rational — does it give the logically-correct counterfactual for the data it is given? (2)
  is the DATA faithful — does the emitted graph actually record reality? Both. Both. Both.
- **If the model is rational and solid but reports something ODD, the bug is in the DATA. Go fix
  the EMIT. Do NOT bend the analysis to compensate.**
- The anti-pattern to eradicate is the bizarre mix we have been doing: emit data, then try to
  compensate for its gaps inside the logic/model with fallbacks, chaining inferences,
  recorded-offset approximations and the like. That is exactly backwards.

Clean separation of concerns: the analysis is a rational function of the data; the data must be
faithful; they are debugged separately and each held to its own standard. Neither compensates for
the other.

---

## Items 1 & 2 — review the implementer's commit `e8c0dfe498`

Two changes (the net diff is `git show e8c0dfe498`; the lead has verified both against code + green
suite + vet):

1. **Zero-duration wait-target bug (the showstopper fresh Codex found, implementer verified on the
   harness: makespan 250 vs the correct 300).** Fix has two parts: (a) `joinUpTo` no longer anchors
   an unstarted child whose own spawn is still pending at the same instant (only a zero-dur child
   can be in that state) — it defers so the gate raises the clock first and the spawn anchors it;
   (b) `actionRank` now orders spawn before self, so a deferred zero-dur child anchors concurrent
   with a same-time self segment. The lead verified the rank change is scoped to zero-dur children
   only (a normal child's interval carves the self out of its spawn point) and real traces are
   bit-unchanged.
2. **`FallbackAnchors > 0` now hard-fails the OTel gate** (was report-only); `MaxFallbackAnchors`
   is the explicit opt-out.

**Review ask:** standard critical review of the code (correct? backward-compatible? properly
scoped?). PLUS — in light of the principle above — **reconsider item 2**: a hard-fail on
`FallbackAnchors` treats "the analysis used a recorded-offset fallback" as a gate failure. But the
principle says that fallback should not EXIST — the analysis must not compensate. So is the hard-fail
a legitimate gate, or is it a **band-aid over the anti-pattern**, where the real fix is to remove the
fallback by anchoring roots from first principles (item 3)? Weigh it explicitly.

---

## Item 3 — cross-root anchoring, reframed from first principles

### The concrete situation + the answer the model MUST give

A CI run fires two dagger queries that overlap in time (parallel `dagger call`s, or one invocation
fanning out concurrent sessions):
- **R_A** (root) starts t=0. **R_B** (root) also starts t=0, concurrently.
- Both need module `foo`; singleflight loads it once, in R_B: R_B does 100ms setup, spawns
  `T=load foo` at t=100, T runs 100→300. R_A dedups: R_A's `W` **waits on R_B's T** (a cross-root
  wait), unblocks at 300. Makespan ≈ 300.
- **What-if:** scale R_B's 100ms setup → 0. **The physically correct answer:** both roots started
  independently, so neither start moves; R_B reaches T's spawn at 0, T runs 0→200, and R_A's W
  unblocks at 200 → makespan ≈ 200, a 100ms saving that propagates across the root boundary through
  the wait.

### Where the current analysis violates the principle

The current model handles cross-root references with TWO instances of the forbidden anti-pattern:
- **The CHAINING model** (`Run()`): it infers that successive roots are sequentially dependent from
  their temporal order — a guess, with no recorded causal edge. That is INFERENCE.
- **The recorded-offset FALLBACK** (`spawnTo`, par<0): when a wait reaches a not-yet-scheduled
  root, it anchors that root at its recorded offset to keep going. That is COMPENSATION for not
  knowing a start.

And a cross-root WAIT is positive evidence the two roots are CONCURRENT (W can only block on T if
they overlap) — which makes the chaining model **provably wrong** for exactly these roots. The
"SimStartConflict" the analysis reports is the chaining heuristic fighting the (correct) independent
anchor; first-write-wins keeps the right value, so the conflict is a false alarm produced by the
analysis's own compensation machinery.

### The rational model (trust the data)

A root has no incoming causal edge — so the data says it is **independent**: anchor it at its own
recorded start, full stop. A recorded wait/edge is honored: the counterfactual propagates through
it. **No chaining inference. No fallback.** On the concrete example this gives exactly the correct
answer (makespan 200), with `FallbackAnchors` and conflicts **0 by construction** (the anchor is the
root's own recorded start — an exact fact from the data, not a fallback).

### What we need from you (think from first principles, with the test cases)

1. **State the rational first-principles model** for roots and cross-root references, trusting only
   the recorded data.
2. **The three test cases — for each, what does the DATA actually record, and what is the
   logically-correct counterfactual answer for that data?**
   - **(a) Concurrent cross-root dedup** (above): saving propagates, both starts fixed, makespan 200.
   - **(b) Sequential CLI** (root B starts after root A ends — successive commands): the shell
     serialized them OUTSIDE the engine, so there is likely NO recorded causal edge A→B. The
     rational model then treats them as independent → scaling A does NOT shift B. Is that the right
     answer? Or is the chaining model (which shifts B) compensating for a missing edge? If the
     latter: the principle says remove the chaining; if cross-session serialization genuinely
     matters, that is a DATA problem (the edge must be emitted, or it is out of engine scope — say
     which).
   - **(c) Sub-session** (R_B launched by R_A mid-flight — concurrent, R_B's start depends on R_A
     reaching the launch point): does the data record the launch as a causal edge? If YES, R_B is
     not a pure root — anchor it through that edge, no ambiguity. If NO, the data says independent →
     anchor independently; and if that is odd, the FIX IS IN THE EMIT (record the launch edge), NOT
     a fallback in the analysis.
3. **What changes to the analysis/model code** give the rational answer for each case (candidly:
   likely removing the chaining inference and the recorded-offset fallback; anchoring roots
   independently; honoring recorded edges only)?
4. **Where the recorded data CANNOT yield an unambiguous correct answer, the conclusion is NOT "add
   a fallback/approximation."** It is: "the data is insufficient — here is the specific edge the
   EMIT must record." Identify those DATA fixes explicitly.

---

## Deliverable

Write to `hack/designs/wcprof-otel-chunk4-firstprinciples-<yourname>.md`:
- (a) your critical review of items 1 & 2 (incl. whether the `FallbackAnchors` hard-fail survives
  the principle, or is a band-aid for the anti-pattern);
- (b) your first-principles analysis of item 3 — the rational model; the three test cases + their
  logically-correct answers; the analysis/model code changes; and the explicit DATA (emit) fixes
  for any case the recorded data cannot answer unambiguously;
- (c) explicitly confirm your alignment with the governing principle, and flag anywhere you think it
  is wrong or incomplete (push back if so).
Review/analysis only — NO code, NO commits.
